package durable

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// countingFenceObs conta escritas aceites e rejeitadas pelo enforcement.
type countingFenceObs struct {
	accepted int
	rejected int
}

func (o *countingFenceObs) Accepted(string, uint64)         { o.accepted++ }
func (o *countingFenceObs) Rejected(string, uint64, uint64) { o.rejected++ }

// businessWrite é um "efeito" durável: um Append ao stream de negócio do run.
func businessWrite(runID, stepID string) eventstore.EventInput {
	return eventstore.EventInput{
		Type:    "work.effect",
		Payload: []byte(`{"effect":"` + stepID + `"}`),
		RunID:   runID,
		StepID:  stepID,
	}
}

// committedWork conta os eventos work.effect no stream de negócio do run — o número
// de EFEITOS efectivamente materializados (base para provar zero duplicação).
func committedWork(t *testing.T, store *eventstore.Store, runID string) int {
	t.Helper()
	evs, err := store.Read(context.Background(), runID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return 0
		}
		t.Fatalf("Read stream de negócio: %v", err)
	}
	n := 0
	for _, e := range evs {
		if e.Type == "work.effect" {
			n++
		}
	}
	return n
}

// committedEffectStepIDs devolve os step_ids dos eventos work.effect materializados no
// stream de negócio, por ordem. Usa-se para provar QUAL efeito passou — isolando o
// FENCING da dedup por idempotency_key do Event Store: efeitos com step_ids DISTINTOS
// não são deduplicados pelo ES, pelo que só o fencing os pode bloquear.
func committedEffectStepIDs(t *testing.T, store *eventstore.Store, runID string) []string {
	t.Helper()
	evs, err := store.Read(context.Background(), runID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return nil
		}
		t.Fatalf("Read stream de negócio: %v", err)
	}
	var ids []string
	for _, e := range evs {
		if e.Type == "work.effect" {
			ids = append(ids, e.StepID)
		}
	}
	return ids
}

func TestNewFencedAppenderValidation(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	m := newManager(t, store, newTestClock())
	tests := []struct {
		name   string
		store  EventStore
		tokens TokenSource
		want   error
	}{
		{"nil store", nil, m, ErrNilStore},
		{"nil tokens", store, nil, ErrNilTokenSource},
		{"ok", store, m, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewFencedAppender(tc.store, tc.tokens)
			if !errors.Is(err, tc.want) {
				t.Fatalf("erro = %v, quer %v", err, tc.want)
			}
		})
	}
}

// TestFencedAppenderRejectsInvalidToken: token ausente/0 é fenced-out sem tocar no ES.
func TestFencedAppenderRejectsInvalidToken(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	m := newManager(t, store, newTestClock())
	fa, err := NewFencedAppender(store, m)
	if err != nil {
		t.Fatalf("NewFencedAppender: %v", err)
	}
	ctx := context.Background()
	if _, err := fa.Append(ctx, "run-x", FencingToken(0), businessWrite("run-x", "s1")); !errors.Is(err, ErrStaleFencingToken) {
		t.Fatalf("token 0 = %v, quer ErrStaleFencingToken", err)
	}
	if _, err := fa.Append(ctx, "", FencingToken(1), businessWrite("run-x", "s1")); !errors.Is(err, ErrEmptyRunID) {
		t.Fatalf("runID vazio = %v, quer ErrEmptyRunID", err)
	}
	if committedWork(t, store, "run-x") != 0 {
		t.Fatalf("escrita fenced-out deixou rasto no Event Store")
	}
}

// TestFencingRejectsStaleWrite: um worker obsoleto (token antigo) tenta escrever depois
// de o run ter sido reclamado por outro (token maior) → rejeitado, sem duplicação.
func TestFencingRejectsStaleWrite(t *testing.T) {
	t.Parallel()
	clk := newTestClock()
	store := newStore(t)
	m := newManager(t, store, clk)
	obs := &countingFenceObs{}
	fa, err := NewFencedAppender(store, m, WithFenceObserver(obs))
	if err != nil {
		t.Fatalf("NewFencedAppender: %v", err)
	}
	ctx := context.Background()
	const run = "run-fence"

	// Worker A reclama (token 1) e escreve — aceite (token == corrente).
	a, err := m.Claim(ctx, run)
	if err != nil {
		t.Fatalf("Claim A: %v", err)
	}
	if _, err := fa.Append(ctx, run, a.Token, businessWrite(run, "a-1")); err != nil {
		t.Fatalf("escrita de A (token corrente) = %v, quer nil", err)
	}

	// A expira; B reclama (token 2). Agora o corrente é 2.
	clk.Advance(ttl + time.Nanosecond)
	b, err := m.Claim(ctx, run)
	if err != nil {
		t.Fatalf("Claim B: %v", err)
	}
	if b.Token.Value() <= a.Token.Value() {
		t.Fatalf("token de B (%d) não é > token de A (%d)", b.Token.Value(), a.Token.Value())
	}

	// A, obsoleto, tenta escrever com o token antigo → ErrStaleFencingToken.
	if _, err := fa.Append(ctx, run, a.Token, businessWrite(run, "a-2")); !errors.Is(err, ErrStaleFencingToken) {
		t.Fatalf("escrita obsoleta de A = %v, quer ErrStaleFencingToken", err)
	}

	// B escreve com o token corrente → aceite.
	if _, err := fa.Append(ctx, run, b.Token, businessWrite(run, "b-1")); err != nil {
		t.Fatalf("escrita de B (token corrente) = %v, quer nil", err)
	}

	// Só as escritas legítimas de A(a-1) e B(b-1) foram materializadas: 2, sem duplicação.
	if got := committedWork(t, store, run); got != 2 {
		t.Fatalf("efeitos materializados = %d, quer 2 (a-1, b-1)", got)
	}
	if obs.rejected != 1 || obs.accepted != 2 {
		t.Fatalf("observer: aceites=%d rejeitadas=%d, quer 2/1", obs.accepted, obs.rejected)
	}
}

// TestZeroDoubleExecutionUnderReassignment é o TESTE-CHAVE de AOS-018 (cross-host
// simulado por relógio + fencing). Worker A fica LENTO após reclamar; o lease expira;
// worker B reclama; A "acorda" e VOLTA a executar o passo → REJEITADO; B executou com
// o token novo → OK. O mesmo passo lógico NÃO é materializado duas vezes.
//
// # Isolamento do fencing face à dedup do Event Store
//
// O efeito re-executado por A carrega um step_id DISTINTO ("step-42-retryA") do de B
// ("step-42"). Assim o efeito de A NÃO é idempotente com o de B: a dedup por
// idempotency_key do Event Store NÃO o apanharia. Se a escrita de A se materializasse,
// apareceria como um SEGUNDO efeito. Logo o facto de só o efeito de B sobreviver prova
// que foi o FENCING (token obsoleto) — e não a dedup do ES — a bloquear o duplicado.
func TestZeroDoubleExecutionUnderReassignment(t *testing.T) {
	t.Parallel()
	clk := newTestClock()
	store := newStore(t)
	ctx := context.Background()
	const run = "run-zero-dup"

	// Dois managers = dois workers (hosts distintos), mesmo Event Store e relógio.
	mA := newManager(t, store, clk, WithWorkerID("A"))
	mB := newManager(t, store, clk, WithWorkerID("B"))
	faA, _ := NewFencedAppender(store, mA)
	faB, _ := NewFencedAppender(store, mB)

	// A reclama (token 1) e começa a processar o passo "step-42".
	a, err := mA.Claim(ctx, run)
	if err != nil {
		t.Fatalf("Claim A: %v", err)
	}

	// A fica lento (não faz heartbeat). O TTL esgota-se: o run fica reclamável.
	clk.Advance(ttl + time.Nanosecond)

	// B, vendo o lease expirado, reclama (token 2) e executa "step-42" com sucesso.
	b, err := mB.Claim(ctx, run)
	if err != nil {
		t.Fatalf("Claim B: %v", err)
	}
	if _, err := faB.Append(ctx, run, b.Token, businessWrite(run, "step-42")); err != nil {
		t.Fatalf("B executa step-42: %v", err)
	}

	// A "acorda" e tenta re-materializar o passo com o token 1 (obsoleto) e um efeito
	// de step_id DISTINTO — não-idempotente com o de B, logo invisível à dedup do ES.
	// Só o fencing o pode barrar → REJEITADO.
	_, err = faA.Append(ctx, run, a.Token, businessWrite(run, "step-42-retryA"))
	if !errors.Is(err, ErrStaleFencingToken) {
		t.Fatalf("A obsoleto voltou a executar (efeito distinto): err=%v, quer ErrStaleFencingToken", err)
	}

	// Um heartbeat de A também é recusado (lease expirado/superado) — A deve abortar.
	if _, err := mA.Heartbeat(ctx, a); err == nil {
		t.Fatalf("Heartbeat de A obsoleto devia falhar")
	}

	// SÓ o efeito de B (step-42) se materializou. Se o fencing tivesse falhado, o efeito
	// distinto de A (step-42-retryA) escaparia à dedup do ES e apareceria aqui — a sua
	// AUSÊNCIA prova que é o fencing, não a dedup, a garantir zero execução dupla.
	ids := committedEffectStepIDs(t, store, run)
	if len(ids) != 1 || ids[0] != "step-42" {
		t.Fatalf("efeitos materializados = %v, quer exactamente [step-42] (fencing barrou o efeito distinto de A)", ids)
	}
}

// supersedeOnAppendStore envolve o Event Store e, na PRIMEIRA escrita ao stream de
// NEGÓCIO de um run (streamID == run), executa antes o callback onFirst — usado para
// injectar uma SUPERSESSÃO (avanço do relógio + novo claim) ENTRE a verificação do
// fencing e o Append efectivo, forçando de forma determinística o interleaving
// supersessão-durante-append (a janela TOCTOU documentada em fencing.go). À semelhança
// de conflictOnceStore, mas para o stream de negócio.
type supersedeOnAppendStore struct {
	*eventstore.Store
	run     string
	fired   bool
	onFirst func()
}

func (s *supersedeOnAppendStore) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	if !s.fired && streamID == s.run {
		s.fired = true
		s.onFirst()
	}
	return s.Store.Append(ctx, streamID, in, opts...)
}

// TestFencingTOCTOUWindowBoundary força o interleaving supersessão-durante-append e
// fixa HONESTAMENTE o limite conhecido: o fencing fecha de forma FIRME o caso
// token-estritamente-inferior (sempre rejeitado), mas o boundary token-IGUAL na janela
// entre a verificação e o Append fica delegado ao CAS durável do Event Store de
// produção (o token não é dobrado no envelope neste reference impl). Prova as duas
// coisas: (a) uma escrita cujo token ERA o corrente comita apesar da supersessão a meio
// (limite documentado); (b) a seguir, uma escrita estritamente inferior é SEMPRE barrada.
func TestFencingTOCTOUWindowBoundary(t *testing.T) {
	t.Parallel()
	clk := newTestClock()
	raw := newStore(t)
	ctx := context.Background()
	const run = "run-toctou"

	mA := newManager(t, raw, clk, WithWorkerID("A"))
	mB := newManager(t, raw, clk, WithWorkerID("B"))

	a, err := mA.Claim(ctx, run) // token 1, corrente = 1
	if err != nil {
		t.Fatalf("Claim A: %v", err)
	}

	// A escrita de A passa as verificações (token 1 == corrente 1, lease vivo NAQUELE
	// instante); o wrapper injecta então a supersessão (B reclama token 2) ANTES do
	// Append delegado. A escrita de A comita mesmo assim — é o limite TOCTOU.
	var b Lease
	var claimErr error
	ws := &supersedeOnAppendStore{Store: raw, run: run, onFirst: func() {
		clk.Advance(ttl + time.Nanosecond)
		b, claimErr = mB.Claim(ctx, run)
	}}
	faA, err := NewFencedAppender(ws, mA)
	if err != nil {
		t.Fatalf("NewFencedAppender: %v", err)
	}

	if _, err := faA.Append(ctx, run, a.Token, businessWrite(run, "boundary")); err != nil {
		t.Fatalf("escrita de A na janela TOCTOU = %v, quer nil (limite documentado)", err)
	}
	if claimErr != nil {
		t.Fatalf("supersessão de B falhou: %v", claimErr)
	}
	if b.Token.Value() <= a.Token.Value() {
		t.Fatalf("supersessão não ocorreu: token B=%d, A=%d", b.Token.Value(), a.Token.Value())
	}

	// INVARIANTE PROVADA: depois da supersessão, uma escrita ESTRITAMENTE INFERIOR de A
	// (corrente é agora 2) é sempre rejeitada — o fencing fecha o caso < de forma firme.
	if _, err := faA.Append(ctx, run, a.Token, businessWrite(run, "stale")); !errors.Is(err, ErrStaleFencingToken) {
		t.Fatalf("escrita estritamente inferior de A = %v, quer ErrStaleFencingToken", err)
	}
	// B (token corrente, lease vivo) escreve → aceite.
	if _, err := faA.Append(ctx, run, b.Token, businessWrite(run, "b-legit")); err != nil {
		t.Fatalf("escrita de B (corrente) = %v, quer nil", err)
	}
}

// TestFencingRejectsExpiredHolder fecha o fail-open de liveness (AOS-018-Q5): um
// detentor cujo token AINDA é o corrente mas cujo lease EXPIROU por ausência de
// heartbeat — janela expirado-mas-não-superado, sem qualquer novo claim — deixava de
// ser fenced-out (1 >= 1). Com a capacidade [LeaseExpiryAuthority] o enforcement
// rejeita-o, honrando o contrato de ErrLeaseExpired.
func TestFencingRejectsExpiredHolder(t *testing.T) {
	t.Parallel()
	clk := newTestClock()
	store := newStore(t)
	m := newManager(t, store, clk)
	obs := &countingFenceObs{}
	fa, err := NewFencedAppender(store, m, WithFenceObserver(obs))
	if err != nil {
		t.Fatalf("NewFencedAppender: %v", err)
	}
	ctx := context.Background()
	const run = "run-expired"

	a, err := m.Claim(ctx, run) // token 1
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	// Escrita enquanto o lease é vivo → aceite.
	if _, err := fa.Append(ctx, run, a.Token, businessWrite(run, "s1")); err != nil {
		t.Fatalf("escrita com lease vivo = %v, quer nil", err)
	}

	// O TTL esgota-se e NINGUÉM reclama (sem supersessão): o token 1 continua a ser o
	// corrente. Antes de Q5 a escrita passava (1 >= 1); agora é fenced-out porque o
	// lease do detentor EXPIROU.
	clk.Advance(ttl + time.Nanosecond)
	if _, err := fa.Append(ctx, run, a.Token, businessWrite(run, "s2")); !errors.Is(err, ErrStaleFencingToken) {
		t.Fatalf("escrita de detentor com lease expirado = %v, quer ErrStaleFencingToken", err)
	}

	// O corrente NÃO mudou (não houve supersessão) — prova que a rejeição veio da
	// EXPIRAÇÃO, não de um token superior.
	cur, err := m.CurrentToken(ctx, run)
	if err != nil {
		t.Fatalf("CurrentToken: %v", err)
	}
	if cur.Value() != a.Token.Value() {
		t.Fatalf("token corrente = %d, quer %d (sem supersessão)", cur.Value(), a.Token.Value())
	}

	// Só s1 (pré-expiração) se materializou; s2 foi fenced por lease expirado.
	if got := committedWork(t, store, run); got != 1 {
		t.Fatalf("efeitos materializados = %d, quer 1 (s2 fenced por lease expirado)", got)
	}
	if obs.rejected != 1 || obs.accepted != 1 {
		t.Fatalf("observer: aceites=%d rejeitadas=%d, quer 1/1", obs.accepted, obs.rejected)
	}
}

// TestNoPIDLivenessDecision é a AUDITORIA exigida por AOS-018: NENHUM caminho de
// código do pacote durable decide liveness por PID. Analisa a AST dos ficheiros de
// PRODUÇÃO (.go não-teste) e falha se algum REFERENCIAR os símbolos de PID do
// runtime/os (Getpid/Getppid/os.Process). Usar a AST — e não uma varredura textual —
// ignora deliberadamente os COMENTÁRIOS: uma nota de doc pode mencionar "os.Getpid"
// para explicar a proibição sem que isso conte como uso.
func TestNoPIDLivenessDecision(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	// Selectores que denunciariam uma decisão de liveness por PID (X.Sel).
	forbidden := map[string]bool{"Getpid": true, "Getppid": true, "Process": true}
	fset := token.NewFileSet()
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		// Sem ParseComments: os comentários não entram na análise de código.
		f, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if (pkg.Name == "os" || pkg.Name == "syscall") && forbidden[sel.Sel.Name] {
				t.Errorf("%s refere %s.%s — liveness NÃO pode depender de PID (AOS-018)",
					name, pkg.Name, sel.Sel.Name)
			}
			return true
		})
		scanned++
	}
	if scanned == 0 {
		t.Fatal("auditoria não varreu nenhum ficheiro de produção")
	}
}
