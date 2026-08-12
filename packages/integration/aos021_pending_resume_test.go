package integration

// COBERTURA DO CAMINHO DE EXPIRAÇÃO E DE RETOMA (AOS-021).
//
// O `EventStoreApprovalStore` (os GRANTS já emitidos) tinha testes; o registo de PENDENTES
// e o de RETOMA não tinham nenhum — 160 statements a zero num módulo que o gate `apex`
// mede, e, mais importante, no caminho que decide QUANDO uma aprovação four-eyes expira.
// Um defeito silencioso aqui tem duas faces, ambas más: expirar o que não devia (mata runs
// retomáveis que só esperavam por um humano) ou não expirar o que devia (pendente eterno).
//
// Os testes abaixo asseram COMPORTAMENTO declarado, não implementação: cada um nomeia a
// propriedade que trancaria se alguém a partisse.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// pendingFixture monta o registo de pendentes sobre um Event Store real (não um double: a
// semântica de idempotency_key e de ordem do stream é parte do que se está a testar).
func pendingFixture(t *testing.T) (*PendingApprovals, *eventstore.Store) {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	p, err := NewPendingApprovals(es)
	if err != nil {
		t.Fatalf("NewPendingApprovals: %v", err)
	}
	return p, es
}

func pendingRec(runID, stepID string, criadoEm time.Time) PendingRecord {
	rec := PendingRecord{
		RunID:      runID,
		StepID:     stepID,
		Turn:       1,
		ToolID:     "doc_write",
		Capability: "cap:fs.write",
		Preview:    []byte("preview-" + runID + "-" + stepID),
	}
	if !criadoEm.IsZero() {
		rec.CreatedAt = criadoEm.Format(time.RFC3339Nano)
	}
	return rec
}

// TestPendingApprovals_ConstrutorRecusaStoreNil — fail-closed na composição: um registo de
// pendentes sem substrato não é "vazio", é inconstruível.
func TestPendingApprovals_ConstrutorRecusaStoreNil(t *testing.T) {
	if _, err := NewPendingApprovals(nil); !errors.Is(err, ErrNilApprovalStore) {
		t.Fatalf("store nil devia recusar com ErrNilApprovalStore; deu %v", err)
	}
}

// TestPendingApprovals_PutEListagemPorRun — o básico com a parte que engana: o ListForRun
// FILTRA por run. Um pendente de outro run não pode aparecer na lista de decisões deste.
func TestPendingApprovals_PutEListagemPorRun(t *testing.T) {
	p, _ := pendingFixture(t)
	ctx := context.Background()
	agora := time.Now()

	for _, rec := range []PendingRecord{
		pendingRec("run-A", "s1", agora),
		pendingRec("run-A", "s2", agora),
		pendingRec("run-B", "s1", agora),
	} {
		if err := p.Put(ctx, rec); err != nil {
			t.Fatalf("Put(%s/%s): %v", rec.RunID, rec.StepID, err)
		}
	}

	got, err := p.ListForRun(ctx, "run-A")
	if err != nil {
		t.Fatalf("ListForRun: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("run-A devia ter 2 pendentes, tem %d: %+v", len(got), got)
	}
	for _, rec := range got {
		if rec.RunID != "run-A" {
			t.Fatalf("fuga entre runs: ListForRun(run-A) devolveu %q", rec.RunID)
		}
	}
}

// TestPendingApprovals_ReEscalarMesmoPassoNaoDuplica — a idempotency_key deriva de
// (run, step). Re-escalar o MESMO passo (retry, reposição do loop) não pode fazer o
// operador ver a mesma decisão duas vezes.
func TestPendingApprovals_ReEscalarMesmoPassoNaoDuplica(t *testing.T) {
	p, _ := pendingFixture(t)
	ctx := context.Background()
	rec := pendingRec("run-dup", "s1", time.Now())

	for i := 0; i < 3; i++ {
		if err := p.Put(ctx, rec); err != nil {
			t.Fatalf("Put #%d: %v", i, err)
		}
	}
	got, err := p.ListForRun(ctx, "run-dup")
	if err != nil {
		t.Fatalf("ListForRun: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("3 Put do mesmo passo deviam dar 1 pendente, deram %d", len(got))
	}
}

// TestPendingApprovals_ExpirarRetiraDaListaMasNaoAprova — a propriedade CENTRAL do ticket.
// `Expire` é um facto append-only que tira o pendente da lista do operador; o que ele NÃO
// pode fazer é criar autorização. Depois de expirar, a acção continua SEM grant — uma
// retoma encontra-a por autorizar e vê-a negada.
func TestPendingApprovals_ExpirarRetiraDaListaMasNaoAprova(t *testing.T) {
	p, es := pendingFixture(t)
	ctx := context.Background()
	rec := pendingRec("run-exp", "s1", time.Now())

	if err := p.Put(ctx, rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, _ := p.ListForRun(ctx, "run-exp"); len(got) != 1 {
		t.Fatalf("pré-condição: devia haver 1 pendente, há %d", len(got))
	}

	if err := p.Expire(ctx, "run-exp", "s1"); err != nil {
		t.Fatalf("Expire: %v", err)
	}

	got, err := p.ListForRun(ctx, "run-exp")
	if err != nil {
		t.Fatalf("ListForRun pós-expire: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("o pendente expirado NÃO devia continuar à espera de decisão: %+v", got)
	}

	// A metade que dá sentido ao teste: expirar não emitiu grant nenhum.
	store, err := NewEventStoreApprovalStore(es)
	if err != nil {
		t.Fatalf("NewEventStoreApprovalStore: %v", err)
	}
	if _, ok, err := store.FindUnconsumedByPreview(ctx, rec.Preview); err != nil || ok {
		t.Fatalf("Expire NÃO pode autorizar nada — encontrou grant para a preview (ok=%v, err=%v)", ok, err)
	}
}

// TestPendingApprovals_AprovadoSaiDaListaDeDecisoes — um pendente com grant emitido deixa
// de ser "por decidir". Sem isto, o operador via para sempre uma decisão que já tomou.
func TestPendingApprovals_AprovadoSaiDaListaDeDecisoes(t *testing.T) {
	p, es := pendingFixture(t)
	ctx := context.Background()
	rec := pendingRec("run-ok", "s1", time.Now())
	if err := p.Put(ctx, rec); err != nil {
		t.Fatalf("Put: %v", err)
	}

	store, err := NewEventStoreApprovalStore(es)
	if err != nil {
		t.Fatalf("NewEventStoreApprovalStore: %v", err)
	}
	if err := store.Put(ctx, ApprovalGrant{ID: "g1", Preview: rec.Preview}); err != nil {
		t.Fatalf("Put do grant: %v", err)
	}

	got, err := p.ListForRun(ctx, "run-ok")
	if err != nil {
		t.Fatalf("ListForRun: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("pendente COM grant emitido não é 'por decidir': %+v", got)
	}
}

// TestListExpirable_SoDevolveOsQueExcederamOTTL — a aritmética do varrimento. Um pendente
// dentro da janela não pode ser varrido: matar-lhe o run seria matar exactamente quem
// esperava, legitimamente, por um humano lento.
func TestListExpirable_SoDevolveOsQueExcederamOTTL(t *testing.T) {
	p, _ := pendingFixture(t)
	ctx := context.Background()
	agora := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	const ttl = 15 * time.Minute

	velho := pendingRec("run-velho", "s1", agora.Add(-20*time.Minute)) // fora da janela
	novo := pendingRec("run-novo", "s1", agora.Add(-2*time.Minute))    // dentro
	for _, rec := range []PendingRecord{velho, novo} {
		if err := p.Put(ctx, rec); err != nil {
			t.Fatalf("Put(%s): %v", rec.RunID, err)
		}
	}

	got, err := p.ListExpirable(ctx, agora, ttl)
	if err != nil {
		t.Fatalf("ListExpirable: %v", err)
	}
	if len(got) != 1 || got[0].RunID != "run-velho" {
		t.Fatalf("só o pendente fora da janela devia ser expirável; deu %+v", got)
	}
}

// TestListExpirable_SemCreatedAtNuncaExpira — o fail-safe declarado no código: não se
// expira sozinho aquilo cuja idade se desconhece. Um pendente sem âncora de tempo fica à
// espera de decisão EXPLÍCITA, por muito que o varrimento passe.
func TestListExpirable_SemCreatedAtNuncaExpira(t *testing.T) {
	p, _ := pendingFixture(t)
	ctx := context.Background()

	semData := pendingRec("run-sem-data", "s1", time.Time{}) // CreatedAt vazio
	if semData.CreatedAt != "" {
		t.Fatalf("fixture inválida: CreatedAt devia estar vazio")
	}
	if err := p.Put(ctx, semData); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Um TTL absurdamente curto e um "agora" muito à frente: se houvesse expiração por
	// omissão, este é o cenário que a apanharia.
	got, err := p.ListExpirable(ctx, time.Now().Add(100*time.Hour), time.Nanosecond)
	if err != nil {
		t.Fatalf("ListExpirable: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("pendente SEM CreatedAt nunca pode ser expirado sozinho; deu %+v", got)
	}
}

// TestListExpirable_NaoRepeteJaExpirado — o varrimento é periódico e re-lê o mesmo stream.
// Sem esta guarda, cada passagem re-expiraria o que já expirou, enchendo o log.
func TestListExpirable_NaoRepeteJaExpirado(t *testing.T) {
	p, _ := pendingFixture(t)
	ctx := context.Background()
	agora := time.Now()
	rec := pendingRec("run-1x", "s1", agora.Add(-time.Hour))
	if err := p.Put(ctx, rec); err != nil {
		t.Fatalf("Put: %v", err)
	}

	primeira, err := p.ListExpirable(ctx, agora, time.Minute)
	if err != nil || len(primeira) != 1 {
		t.Fatalf("1.ª passagem devia devolver 1 expirável; deu %+v (err=%v)", primeira, err)
	}
	if err := p.Expire(ctx, rec.RunID, rec.StepID); err != nil {
		t.Fatalf("Expire: %v", err)
	}
	segunda, err := p.ListExpirable(ctx, agora, time.Minute)
	if err != nil {
		t.Fatalf("ListExpirable (2.ª): %v", err)
	}
	if len(segunda) != 0 {
		t.Fatalf("o varrimento não pode re-expirar o que já expirou; deu %+v", segunda)
	}
}

// ---------------------------------------------------------------------------
// Registo de RETOMA
// ---------------------------------------------------------------------------

// TestResumeRecords_ConstrutorRecusaStoreNil — fail-closed na composição.
func TestResumeRecords_ConstrutorRecusaStoreNil(t *testing.T) {
	if _, err := NewResumeRecords(nil, nil); !errors.Is(err, ErrNilResumeStore) {
		t.Fatalf("store nil devia recusar com ErrNilResumeStore; deu %v", err)
	}
}

func sampleResume(runID string) ResumeRecord {
	return ResumeRecord{
		RunID:     runID,
		Principal: referencemonitor.Principal{NHIID: "nhi:agente"},
		Scope:     []string{"cap:fs.read"},
		Objective: "objectivo do utilizador",
		MaxTurns:  8,
	}
}

// TestResumeRecords_GuardaEResolve — round-trip básico, e a ausência é ok=false (não erro):
// um run que nunca escalou simplesmente não tem registo.
func TestResumeRecords_GuardaEResolve(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	r, err := NewResumeRecords(es, nil)
	if err != nil {
		t.Fatalf("NewResumeRecords: %v", err)
	}
	ctx := context.Background()

	if _, ok, err := r.Get(ctx, "run-inexistente"); err != nil || ok {
		t.Fatalf("run sem registo devia dar (_, false, nil); deu ok=%v err=%v", ok, err)
	}

	rec := sampleResume("run-r1")
	if err := r.Put(ctx, rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := r.Get(ctx, "run-r1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Objective != rec.Objective || got.MaxTurns != rec.MaxTurns || got.Principal.NHIID != rec.Principal.NHIID {
		t.Fatalf("registo não sobreviveu ao round-trip: %+v", got)
	}
}

// TestResumeRecords_CredencialNuncaEPersistida — a omissão DELIBERADA nº1 do ficheiro, e a
// razão de segurança de todo o desenho: o log é lido pelo read-path soberano e replicado.
// Assere-se sobre os BYTES CRUS do stream, não sobre o struct — é o que um leitor do log vê.
func TestResumeRecords_CredencialNuncaEPersistida(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	r, err := NewResumeRecords(es, nil)
	if err != nil {
		t.Fatalf("NewResumeRecords: %v", err)
	}
	ctx := context.Background()

	const segredo = "bearer-que-nunca-pode-aparecer-no-log"
	rec := sampleResume("run-cred")
	if err := r.Put(ctx, rec); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// O Goal reconstruído leva a credencial FRESCA — mas ela entra AQUI, na retoma, e não
	// veio do log.
	goal := rec.GoalWith(segredo)
	if goal.Credential != segredo {
		t.Fatalf("GoalWith devia injectar a credencial fresca; deu %q", goal.Credential)
	}
	if goal.RunID != rec.RunID || goal.Objective != rec.Objective {
		t.Fatalf("GoalWith devia reconstituir o Goal; deu %+v", goal)
	}

	events, err := readApprovalStream(ctx, es)
	if err != nil {
		t.Fatalf("readApprovalStream: %v", err)
	}
	for _, ev := range events {
		if bytesContains(ev.Payload, []byte(segredo)) {
			t.Fatal("a CREDENCIAL apareceu no log durável — é a omissão deliberada nº1 de AOS-021")
		}
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(ev.Payload, &probe); err == nil {
			if _, tem := probe["credential"]; tem {
				t.Fatal("o envelope de retoma não pode ter campo `credential`")
			}
		}
	}
}

// selaFalha é um ContentCipher que RECUSA selar — o cenário que prova o fail-closed.
type selaFalha struct{ erro error }

func (c selaFalha) SealContent(context.Context, string, string, []byte) ([]byte, error) {
	return nil, c.erro
}
func (c selaFalha) OpenContent(context.Context, string, []byte) ([]byte, error) {
	return nil, c.erro
}

// TestResumeRecords_CifraFalhaAbortaEscritaNuncaDegradaParaClaro — a omissão DELIBERADA
// nº2. Sob um cifrador activo, um erro de cifra tem de ABORTAR: o Goal transporta o
// objectivo do utilizador e o contexto de memória, que o `turn.recorded` deliberadamente
// não persiste em claro. Degradar para claro abriria uma exposição nova, em silêncio.
func TestResumeRecords_CifraFalhaAbortaEscritaNuncaDegradaParaClaro(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	boom := errors.New("kek indisponível")
	r, err := NewResumeRecords(es, selaFalha{erro: boom})
	if err != nil {
		t.Fatalf("NewResumeRecords: %v", err)
	}
	ctx := context.Background()

	rec := sampleResume("run-selado")
	rec.Objective = "conteudo-sensivel-do-utilizador"
	if err := r.Put(ctx, rec); !errors.Is(err, boom) {
		t.Fatalf("cifra a falhar tem de ABORTAR a escrita; deu %v", err)
	}

	// E — o que dá sentido ao teste — nada foi escrito em claro por baixo do cifrador.
	events, err := readApprovalStream(ctx, es)
	if err != nil {
		t.Fatalf("readApprovalStream: %v", err)
	}
	for _, ev := range events {
		if ev.Type == approvalResumeEventType {
			t.Fatalf("nenhum registo de retoma podia ter sido escrito: %s", string(ev.Payload))
		}
		if bytesContains(ev.Payload, []byte(rec.Objective)) {
			t.Fatal("o objectivo do utilizador foi persistido EM CLARO sob um cifrador activo")
		}
	}
}

// TestResumeRecords_SeladoSemCifradorNaoAbre — um registo selado lido por um nó sem
// cifrador composto NÃO pode devolver corpo nenhum. Fail-closed na leitura.
func TestResumeRecords_SeladoSemCifradorNaoAbre(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	ctx := context.Background()

	comCifra, err := NewResumeRecords(es, cifraReversivel{})
	if err != nil {
		t.Fatalf("NewResumeRecords (com cifra): %v", err)
	}
	if err := comCifra.Put(ctx, sampleResume("run-selado-2")); err != nil {
		t.Fatalf("Put selado: %v", err)
	}

	semCifra, err := NewResumeRecords(es, nil)
	if err != nil {
		t.Fatalf("NewResumeRecords (sem cifra): %v", err)
	}
	if _, ok, err := semCifra.Get(ctx, "run-selado-2"); err == nil || ok {
		t.Fatalf("selado sem cifrador tem de FALHAR a leitura; deu ok=%v err=%v", ok, err)
	}
}

// cifraReversivel é o duplo mínimo que sela e abre (marcador + corpo), para exercitar o
// caminho selado sem depender do vault real.
type cifraReversivel struct{}

const selo = "SELADO:"

func (cifraReversivel) SealContent(_ context.Context, _, _ string, body []byte) ([]byte, error) {
	return append([]byte(selo), body...), nil
}
func (cifraReversivel) OpenContent(_ context.Context, _ string, sealed []byte) ([]byte, error) {
	if !bytesContains(sealed, []byte(selo)) {
		return nil, errors.New("não selado por este cifrador")
	}
	return sealed[len(selo):], nil
}

// TestResumeRecords_RoundTripSelado — com cifrador, o corpo vai selado ao log e volta
// inteiro na leitura; e o objectivo em claro NÃO aparece no stream.
func TestResumeRecords_RoundTripSelado(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	r, err := NewResumeRecords(es, cifraReversivel{})
	if err != nil {
		t.Fatalf("NewResumeRecords: %v", err)
	}
	ctx := context.Background()

	rec := sampleResume("run-rt")
	rec.Objective = "objectivo-que-nao-pode-ir-em-claro"
	if err := r.Put(ctx, rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := r.Get(ctx, "run-rt")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Objective != rec.Objective {
		t.Fatalf("round-trip selado perdeu o corpo: %+v", got)
	}

	events, err := readApprovalStream(ctx, es)
	if err != nil {
		t.Fatalf("readApprovalStream: %v", err)
	}
	for _, ev := range events {
		if ev.Type == approvalResumeEventType && bytesContains(ev.Payload, []byte(rec.Objective)) {
			t.Fatal("o objectivo devia estar SELADO no log, não em claro")
		}
	}
}

// bytesContains evita importar `bytes` só para isto e deixa a intenção explícita nos testes.
func bytesContains(hay, needle []byte) bool {
	if len(needle) == 0 || len(hay) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		igual := true
		for j := range needle {
			if hay[i+j] != needle[j] {
				igual = false
				break
			}
		}
		if igual {
			return true
		}
	}
	return false
}

var _ agentruntime.ContentCipher = cifraReversivel{}
var _ agentruntime.ContentCipher = selaFalha{}
