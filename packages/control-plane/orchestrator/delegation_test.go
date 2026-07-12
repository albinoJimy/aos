package orchestrator_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	budget "github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/orchestrator"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	rm "github.com/aos-ref/kernel/reference-monitor"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// Harness determinístico: emissor NHI, orçamento CAS, Reference Monitor real.
// ---------------------------------------------------------------------------

const testIssuerID = "aos-issuer-aos026"

var baseTime = time.Unix(1_700_000_000, 0).UTC()

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// classes cobre um coordenador (pode delegar) e um worker (folha).
func classes() map[string]identity.ClassPolicy {
	return map[string]identity.ClassPolicy{
		"coordinator": {TTL: 30 * time.Minute, Scope: []string{"cap:agent.spawn", "cap:work", "cap:read"}},
		"worker":      {TTL: 30 * time.Minute, Scope: []string{"cap:work", "cap:read"}},
	}
}

// newIssuer devolve um emissor determinístico (relógio fixo, jti thread-safe) e a
// chave pública para o verificador.
func newIssuer(t *testing.T) (*identity.Issuer, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	var seq atomic.Int64
	iss, err := identity.NewIssuer(testIssuerID, priv, classes(),
		identity.WithIssuerClock(fixedClock(baseTime)),
		identity.WithIDSource(func() string { return "jti-" + itoa(seq.Add(1)) }),
	)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	return iss, pub
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// issueParent emite o token NHI da raiz (agt-root, coordenador) on-behalf-of
// human:alice, com autoridade para delegar (cap:agent.spawn) e trabalhar.
func issueParent(t *testing.T, iss *identity.Issuer) identity.Token {
	t.Helper()
	tok, err := iss.Issue(context.Background(), identity.IssueRequest{
		UserID: "human:alice", AgentID: "agt-root", AgentClass: "coordinator",
		PolicyRef: "policy://coord@1", UserAuthority: []string{"cap:agent.spawn", "cap:work"},
	})
	if err != nil {
		t.Fatalf("Issue parent: %v", err)
	}
	return tok
}

// childReq constrói um pedido de identidade filha (worker) dentro do escopo do pai.
func childReq(agentID string) identity.ChildRequest {
	return identity.ChildRequest{
		AgentID: agentID, AgentClass: "coordinator", PolicyRef: "policy://c@1",
		Authority: []string{"cap:agent.spawn", "cap:work"},
	}
}

// permittingRM constrói um Reference Monitor REAL cuja cadeia neutra permite e cujo
// tool de spawn está registado (default-deny satisfeito). O Permit é genuíno.
func permittingRM(t *testing.T) *rm.Monitor {
	t.Helper()
	m := rm.New()
	if err := m.Register("agent.spawn", func(context.Context, []byte) ([]byte, error) { return nil, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return m
}

// denyHook nega sempre (fail-closed) — para exercitar a recusa da mediação.
type denyHook struct{}

func (denyHook) Name() string { return "test-deny" }
func (denyHook) Evaluate(context.Context, *rm.Call) (rm.HookResult, error) {
	return rm.HookResult{Decision: rm.HookDeny, Reason: "negado pelo teste"}, nil
}

func amt(tokens, microUSD int64) budget.Amount {
	return budget.Amount{Tokens: tokens, CostMicroUSD: microUSD}
}

// ---------------------------------------------------------------------------
// Unit: CAS de reserva sob concorrência — nunca excede o orçamento do pai.
// ---------------------------------------------------------------------------

// TestSpawnCASNoOvershootUnderConcurrency lança N goroutines a fazer spawn de
// sub-agentes distintos sob o MESMO orçamento de pai. A reserva CAS do budget
// garante que a soma das fatias reservadas NUNCA excede o limite do pai (0
// overshoot) e que exactamente as que cabem são admitidas — sem corrida (-race).
func TestSpawnCASNoOvershootUnderConcurrency(t *testing.T) {
	t.Parallel()
	const runID = "run-cas"
	// Pai com espaço para exactamente 10 fatias de 100 tokens.
	b, err := budget.New(runID, amt(1000, 10_000_000))
	if err != nil {
		t.Fatalf("budget.New: %v", err)
	}
	iss, _ := newIssuer(t)
	parent := issueParent(t, iss)
	const goroutines = 40
	// Fan-out amplo (> goroutines): o ÚNICO cap deste teste é o orçamento (CAS), não
	// o fan-out. Prova o invariante de 0-overshoot sob contenção máxima na reserva.
	del, err := orchestrator.NewDelegator(b, permittingRM(t), iss,
		orchestrator.WithMaxFanOut(goroutines))
	if err != nil {
		t.Fatalf("NewDelegator: %v", err)
	}

	slice := amt(100, 1_000_000)
	var wg sync.WaitGroup
	var ok atomic.Int64
	var denied atomic.Int64
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // largada simultânea: maximiza a contenção na CAS.
			h, err := del.Spawn(context.Background(), orchestrator.SpawnRequest{
				RunID:            runID,
				ParentBudgetNode: runID,
				ChildBudgetNode:  "node-" + itoa(int64(i)),
				InheritedBudget:  slice,
				SpawnReserve:     slice,
				Depth:            1,
				FanOutIndex:      0, // descritivo apenas; gate = nº real de filhos (MaxFanOut alto aqui)
				ParentToken:      parent.Compact,
				Child:            childReq("agt-" + itoa(int64(i))),
			})
			switch {
			case err == nil:
				ok.Add(1)
				_ = h
			case errors.Is(err, orchestrator.ErrNoDelegationBudget):
				denied.Add(1)
			default:
				t.Errorf("erro inesperado no spawn %d: %v", i, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	// MaxFanOut > goroutines: o único cap é o orçamento (CAS), não o fan-out.
	if got := ok.Load(); got != 10 {
		t.Fatalf("admitidos=%d, esperava exactamente 10 (1000/100)", got)
	}
	if got := denied.Load(); got != goroutines-10 {
		t.Fatalf("recusados=%d, esperava %d", got, goroutines-10)
	}
	// Invariante forte: reserved na raiz NUNCA excede o limite do pai.
	snap := b.Snapshot()
	root := snap[runID]
	if root.Reserved.Tokens > root.Limit.Tokens || root.Reserved.CostMicroUSD > root.Limit.CostMicroUSD {
		t.Fatalf("OVERSHOOT: reserved=%s > limit=%s", root.Reserved, root.Limit)
	}
	if root.Reserved.Tokens != 1000 {
		t.Fatalf("reserved=%s, esperava exactamente o limite consumido (1000 tokens)", root.Reserved)
	}
}

// ---------------------------------------------------------------------------
// Unit: herança de sub-orçamento + recusa fail-closed quando remanescente = 0.
// ---------------------------------------------------------------------------

// TestInheritanceAndFailClosedNoBudget prova (a) que a fatia herdada não pode
// exceder o remanescente do pai e (b) que um spawn sem orçamento é recusado
// fail-closed com o evento subagent.spawn_denied_no_budget, sem deadlock.
func TestInheritanceAndFailClosedNoBudget(t *testing.T) {
	t.Parallel()
	const runID = "run-inherit"
	store := newStore(t)
	b, err := budget.New(runID, amt(150, 150_000))
	if err != nil {
		t.Fatalf("budget.New: %v", err)
	}
	iss, _ := newIssuer(t)
	parent := issueParent(t, iss)
	del, err := orchestrator.NewDelegator(b, permittingRM(t), iss,
		orchestrator.WithDelegationStore(store, eventstore.Producer{NHIID: "nhi:orq"}))
	if err != nil {
		t.Fatalf("NewDelegator: %v", err)
	}
	ctx := context.Background()

	// Primeiro filho: 100 de 150 → admitido.
	if _, err := del.Spawn(ctx, orchestrator.SpawnRequest{
		RunID: runID, ParentBudgetNode: runID, ChildBudgetNode: "nA",
		InheritedBudget: amt(100, 100_000), SpawnReserve: amt(100, 100_000),
		Depth: 1, ParentToken: parent.Compact, Child: childReq("agt-a"),
	}); err != nil {
		t.Fatalf("spawn A: %v", err)
	}

	// Segundo filho quer herdar 100 mas só restam 50 → recusa fail-closed.
	done := make(chan error, 1)
	go func() {
		_, e := del.Spawn(ctx, orchestrator.SpawnRequest{
			RunID: runID, ParentBudgetNode: runID, ChildBudgetNode: "nB",
			InheritedBudget: amt(100, 100_000), SpawnReserve: amt(100, 100_000),
			Depth: 1, FanOutIndex: 1, ParentToken: parent.Compact, Child: childReq("agt-b"),
		})
		done <- e
	}()
	select {
	case e := <-done:
		if !errors.Is(e, orchestrator.ErrNoDelegationBudget) {
			t.Fatalf("spawn B devia recusar por falta de orçamento, got %v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("DEADLOCK: spawn sem orçamento não devia bloquear")
	}

	// Evento explícito de recusa presente no stream.
	if n := countEvents(t, store, runID, orchestrator.EventSpawnDeniedNoBudget); n != 1 {
		t.Fatalf("esperava 1 evento spawn_denied_no_budget, got %d", n)
	}
	// A reserva do primeiro filho continua intacta (sem leak): remanescente = 50.
	av, _ := b.Available(runID)
	if av.Tokens != 50 {
		t.Fatalf("remanescente=%s, esperava 50 tokens", av)
	}
}

// ---------------------------------------------------------------------------
// Integração: árvore de delegação recursiva (map-reduce) consolida consumo real
// por replay idempotente.
// ---------------------------------------------------------------------------

// TestRecursiveDelegationConsolidatesByReplay constrói uma árvore de delegação
// (root → A,B ; A → A1,A2), consolida o consumo real (Commit) e prova que o
// burn-down se reconstrói por replay dos eventos do budget (budget.Rebuild) E que
// um retry de Finish NÃO duplica o consumo (idempotência por passo, ADR-001). A
// árvore de sub-agentes reconstrói-se dos eventos subagent.spawned.
func TestRecursiveDelegationConsolidatesByReplay(t *testing.T) {
	t.Parallel()
	const runID = "run-mapreduce"
	store := newStore(t)
	producer := eventstore.Producer{NHIID: "nhi:orq"}
	b, err := budget.New(runID, amt(1000, 1_000_000),
		budget.WithEmitter(budget.NewEventStoreEmitter(store, producer)))
	if err != nil {
		t.Fatalf("budget.New: %v", err)
	}
	iss, _ := newIssuer(t)
	parent := issueParent(t, iss)
	del, err := orchestrator.NewDelegator(b, permittingRM(t), iss,
		orchestrator.WithDelegationStore(store, producer))
	if err != nil {
		t.Fatalf("NewDelegator: %v", err)
	}
	ctx := context.Background()

	spawn := func(parentNode, childNode, childID, parentTok string, inherited, slice budget.Amount, depth, fan int) *orchestrator.SpawnHandle {
		h, err := del.Spawn(ctx, orchestrator.SpawnRequest{
			RunID: runID, ParentBudgetNode: parentNode, ChildBudgetNode: childNode,
			InheritedBudget: inherited, SpawnReserve: slice, Depth: depth, FanOutIndex: fan,
			ParentToken: parentTok, Child: childReq(childID), ChildTaskID: childID,
		})
		if err != nil {
			t.Fatalf("spawn %s: %v", childID, err)
		}
		return h
	}

	hA := spawn(runID, "nA", "agt-A", parent.Compact, amt(400, 400_000), amt(50, 50_000), 1, 0)
	hB := spawn(runID, "nB", "agt-B", parent.Compact, amt(300, 300_000), amt(80, 80_000), 1, 1)
	// A delega recursivamente (usa o token filho de A como pai).
	hA1 := spawn("nA", "nA1", "agt-A1", hA.ChildToken.Compact, amt(150, 150_000), amt(100, 100_000), 2, 0)
	hA2 := spawn("nA", "nA2", "agt-A2", hA.ChildToken.Compact, amt(150, 150_000), amt(120, 120_000), 2, 1)

	// Consolida o consumo real de TODOS (sucesso → Commit).
	for _, h := range []*orchestrator.SpawnHandle{hA1, hA2, hA, hB} {
		if err := del.Finish(ctx, h, true); err != nil {
			t.Fatalf("Finish %s: %v", h.ChildTaskID, err)
		}
	}

	// Burn-down reconstruído por replay do log do budget.
	total := amt(50+80+100+120, 50_000+80_000+100_000+120_000)
	assertCommitted := func(when string) {
		events, err := store.Read(ctx, runID, 1)
		if err != nil {
			t.Fatalf("Read (%s): %v", when, err)
		}
		states, err := budget.Rebuild(events)
		if err != nil {
			t.Fatalf("Rebuild (%s): %v", when, err)
		}
		if got := states[runID].Committed; got != total {
			t.Fatalf("[%s] committed na raiz=%s, esperava %s", when, got, total)
		}
		// nA acumula o consumo do subárvore A (A + A1 + A2 = 50+100+120=270).
		if got := states["nA"].Committed; got.Tokens != 270 {
			t.Fatalf("[%s] committed em nA=%s, esperava 270 tokens", when, got)
		}
	}
	assertCommitted("consolidação")

	// Árvore de sub-agentes reconstruída dos eventos subagent.spawned.
	tree := rebuildTree(t, store, runID)
	if len(tree) != 4 {
		t.Fatalf("esperava 4 sub-agentes spawned, got %d", len(tree))
	}
	if tree["nA"] != runID || tree["nB"] != runID || tree["nA1"] != "nA" || tree["nA2"] != "nA" {
		t.Fatalf("árvore reconstruída incorrecta: %+v", tree)
	}

	// RETRY idempotente: refinalizar não duplica o consumo (ADR-001).
	for _, h := range []*orchestrator.SpawnHandle{hA1, hA2, hA, hB} {
		if err := del.Finish(ctx, h, true); err != nil {
			t.Fatalf("Finish retry %s: %v", h.ChildTaskID, err)
		}
	}
	assertCommitted("retry")
}

// ---------------------------------------------------------------------------
// Integração: falha a meio do sub-agente liberta a reserva sem duplicar efeitos.
// ---------------------------------------------------------------------------

// TestFailureReleasesWithoutDuplicateEffects prova que uma falha do sub-agente
// liberta a reserva (Release) sem leak e que um retry da libertação é idempotente
// (0 efeitos duplicados, ADR-001): o remanescente do pai é integralmente
// restaurado e a árvore só regista uma libertação efectiva.
func TestFailureReleasesWithoutDuplicateEffects(t *testing.T) {
	t.Parallel()
	const runID = "run-fail"
	store := newStore(t)
	producer := eventstore.Producer{NHIID: "nhi:orq"}
	b, err := budget.New(runID, amt(500, 500_000),
		budget.WithEmitter(budget.NewEventStoreEmitter(store, producer)))
	if err != nil {
		t.Fatalf("budget.New: %v", err)
	}
	iss, _ := newIssuer(t)
	parent := issueParent(t, iss)
	del, err := orchestrator.NewDelegator(b, permittingRM(t), iss,
		orchestrator.WithDelegationStore(store, producer))
	if err != nil {
		t.Fatalf("NewDelegator: %v", err)
	}
	ctx := context.Background()

	h, err := del.Spawn(ctx, orchestrator.SpawnRequest{
		RunID: runID, ParentBudgetNode: runID, ChildBudgetNode: "nC",
		InheritedBudget: amt(200, 200_000), SpawnReserve: amt(200, 200_000),
		Depth: 1, ParentToken: parent.Compact, Child: childReq("agt-c"),
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	// A meio da reserva: remanescente reduzido.
	if av, _ := b.Available(runID); av.Tokens != 300 {
		t.Fatalf("remanescente durante execução=%s, esperava 300", av)
	}

	// Falha do sub-agente → liberta.
	if err := del.Finish(ctx, h, false); err != nil {
		t.Fatalf("Finish(false): %v", err)
	}
	// Retry da libertação (idempotente): sem duplicar efeitos.
	if err := del.Finish(ctx, h, false); err != nil {
		t.Fatalf("Finish(false) retry: %v", err)
	}

	// Remanescente integralmente restaurado (sem leak, sem dupla libertação).
	if av, _ := b.Available(runID); av.Tokens != 500 {
		t.Fatalf("remanescente após libertação=%s, esperava 500 (restaurado)", av)
	}
	// O log do budget regista exactamente UMA libertação committed (idempotente).
	events, _ := store.Read(ctx, runID, 1)
	states, err := budget.Rebuild(events)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if st := states[runID]; !st.Reserved.IsZero() || !st.Committed.IsZero() {
		t.Fatalf("após libertação idempotente: reserved=%s committed=%s, esperava ambos zero", st.Reserved, st.Committed)
	}
	// Um commit após release é rejeitado (a reserva consome-se uma vez).
	if err := del.Finish(ctx, h, true); !errors.Is(err, budget.ErrCommitAfterRelease) {
		t.Fatalf("commit após release devia falhar com ErrCommitAfterRelease, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Profundidade/fan-out configuráveis + identidade filha mediada pelo RM.
// ---------------------------------------------------------------------------

// TestDepthAndFanOutConfigurable prova que os limites de profundidade e fan-out
// são configuráveis (não constantes) e respeitados fail-closed, com evento.
func TestDepthAndFanOutConfigurable(t *testing.T) {
	t.Parallel()
	const runID = "run-limits"
	store := newStore(t)
	b, err := budget.New(runID, amt(1000, 1_000_000))
	if err != nil {
		t.Fatalf("budget.New: %v", err)
	}
	iss, _ := newIssuer(t)
	parent := issueParent(t, iss)
	del, err := orchestrator.NewDelegator(b, permittingRM(t), iss,
		orchestrator.WithMaxDepth(2), orchestrator.WithMaxFanOut(3),
		orchestrator.WithDelegationStore(store, eventstore.Producer{NHIID: "nhi:orq"}))
	if err != nil {
		t.Fatalf("NewDelegator: %v", err)
	}
	ctx := context.Background()

	// Profundidade efectiva 3 > MaxDepth 2 → recusa (a declarada nunca é MENOR que a
	// autoritativa aqui — o gate usa o máximo — logo o cap de profundidade dispara).
	_, err = del.Spawn(ctx, orchestrator.SpawnRequest{
		RunID: runID, ParentBudgetNode: runID, ChildBudgetNode: "nd", InheritedBudget: amt(10, 10),
		Depth: 3, ParentToken: parent.Compact, Child: childReq("agt-d"),
	})
	if !errors.Is(err, orchestrator.ErrMaxDepthExceeded) {
		t.Fatalf("profundidade excedida devia recusar, got %v", err)
	}

	// Fan-out imposto sobre o nº REAL de filhos: admitem-se MaxFanOut=3 filhos vivos
	// do mesmo pai; o 4º é recusado — independentemente do FanOutIndex declarado.
	for i := 0; i < 3; i++ {
		if _, err := del.Spawn(ctx, orchestrator.SpawnRequest{
			RunID: runID, ParentBudgetNode: runID, ChildBudgetNode: "nf" + itoa(int64(i)),
			InheritedBudget: amt(10, 10), SpawnReserve: amt(10, 10),
			Depth: 1, FanOutIndex: 0, ParentToken: parent.Compact, Child: childReq("agt-f" + itoa(int64(i))),
		}); err != nil {
			t.Fatalf("spawn filho %d (dentro do fan-out): %v", i, err)
		}
	}
	// O 4º filho excede o fan-out real, mesmo declarando FanOutIndex=0 (contorno).
	_, err = del.Spawn(ctx, orchestrator.SpawnRequest{
		RunID: runID, ParentBudgetNode: runID, ChildBudgetNode: "nf3", InheritedBudget: amt(10, 10),
		SpawnReserve: amt(10, 10), Depth: 1, FanOutIndex: 0, ParentToken: parent.Compact, Child: childReq("agt-f3"),
	})
	if !errors.Is(err, orchestrator.ErrMaxFanOutExceeded) {
		t.Fatalf("fan-out excedido (nº real de filhos) devia recusar, got %v", err)
	}
	if n := countEvents(t, store, runID, orchestrator.EventSpawnDenied); n != 2 {
		t.Fatalf("esperava 2 eventos spawn_denied (depth+fanout), got %d", n)
	}
	// As recusas (profundidade e fan-out) NÃO tocam no orçamento; só os 3 filhos
	// admitidos reservaram (3×10=30). Remanescente = 970.
	if av, _ := b.Available(runID); av.Tokens != 970 {
		t.Fatalf("recusas não deviam tocar no orçamento (3 filhos=30 reservados), remanescente=%s", av)
	}
}

// TestChildIdentityMediatedByRM prova que a criação da identidade filha é MEDIADA
// pelo RM (ADR-002): com o token do pai VÁLIDO o RM permite e a NHI filha é criada
// (cadeia estendida on-behalf-of); com o token do pai AUSENTE/inválido o hook de
// identidade nega e o spawn é recusado, libertando a reserva (sem leak).
func TestChildIdentityMediatedByRM(t *testing.T) {
	t.Parallel()
	const runID = "run-mediated"
	iss, pub := newIssuer(t)
	parent := issueParent(t, iss)

	// RM real com o hook de identidade (verificador que confia no emissor).
	v := identity.NewVerifier(
		identity.WithTrustedIssuer(testIssuerID, pub),
		identity.WithVerifierClock(fixedClock(baseTime.Add(time.Minute))),
	)
	m := rm.New(rm.WithHooks(identity.NewIdentityCheck(v)))
	if err := m.Register("agent.spawn", func(context.Context, []byte) ([]byte, error) { return nil, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}

	b, err := budget.New(runID, amt(1000, 1_000_000))
	if err != nil {
		t.Fatalf("budget.New: %v", err)
	}
	del, err := orchestrator.NewDelegator(b, m, iss)
	if err != nil {
		t.Fatalf("NewDelegator: %v", err)
	}
	ctx := context.Background()

	// Token do pai válido → RM permite → identidade filha criada e cadeia estendida.
	h, err := del.Spawn(ctx, orchestrator.SpawnRequest{
		RunID: runID, ParentBudgetNode: runID, ChildBudgetNode: "nk",
		InheritedBudget: amt(100, 100_000), SpawnReserve: amt(100, 100_000),
		Depth: 1, ParentToken: parent.Compact, Child: childReq("agt-k"),
	})
	if err != nil {
		t.Fatalf("spawn mediado (válido): %v", err)
	}
	if h.ChildToken.Claims.AgentID != "agt-k" {
		t.Fatalf("NHI filha errada: %q", h.ChildToken.Claims.AgentID)
	}
	// A cadeia do filho estende a do pai (raiz humana preservada, +1 elo).
	if got := len(h.ChildToken.Claims.DelegationChain); got != len(parent.Claims.DelegationChain)+1 {
		t.Fatalf("cadeia filha depth=%d, esperava pai+1=%d", got, len(parent.Claims.DelegationChain)+1)
	}
	root, _ := h.ChildToken.Claims.DelegationChain.Root()
	if root.Sub != "human:alice" {
		t.Fatalf("raiz da cadeia filha=%q, esperava human:alice", root.Sub)
	}
	if h.Agent.NHIID != "agt-k" || len(h.Agent.DelegationChain) == 0 {
		t.Fatalf("AgentIdentity do DAG mal projectada: %+v", h.Agent)
	}

	// Token do pai AUSENTE → hook de identidade nega → spawn recusado, sem leak.
	_, err = del.Spawn(ctx, orchestrator.SpawnRequest{
		RunID: runID, ParentBudgetNode: runID, ChildBudgetNode: "nz",
		InheritedBudget: amt(100, 100_000), SpawnReserve: amt(100, 100_000),
		Depth: 1, ParentToken: "", Child: childReq("agt-z"),
	})
	if !errors.Is(err, orchestrator.ErrSpawnMediationDenied) {
		t.Fatalf("spawn sem credencial devia ser negado pela mediação, got %v", err)
	}
	// A reserva do spawn negado foi libertada: só a fatia do filho válido (100) fica.
	if av, _ := b.Available(runID); av.Tokens != 900 {
		t.Fatalf("remanescente=%s, esperava 900 (reserva do spawn negado libertada)", av)
	}
}

// TestSpawnDeniedByHookReleasesReservation cobre o caminho de recusa por um hook
// de negação genérico (deny) — a reserva é libertada e o spawn recusado.
func TestSpawnDeniedByHookReleasesReservation(t *testing.T) {
	t.Parallel()
	const runID = "run-deny"
	iss, _ := newIssuer(t)
	parent := issueParent(t, iss)
	m := rm.New(rm.WithHooks(denyHook{}))
	if err := m.Register("agent.spawn", func(context.Context, []byte) ([]byte, error) { return nil, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	b, err := budget.New(runID, amt(500, 500_000))
	if err != nil {
		t.Fatalf("budget.New: %v", err)
	}
	del, err := orchestrator.NewDelegator(b, m, iss)
	if err != nil {
		t.Fatalf("NewDelegator: %v", err)
	}
	_, err = del.Spawn(context.Background(), orchestrator.SpawnRequest{
		RunID: runID, ParentBudgetNode: runID, ChildBudgetNode: "nx",
		InheritedBudget: amt(100, 100_000), SpawnReserve: amt(100, 100_000),
		Depth: 1, ParentToken: parent.Compact, Child: childReq("agt-x"),
	})
	if !errors.Is(err, orchestrator.ErrSpawnMediationDenied) {
		t.Fatalf("esperava ErrSpawnMediationDenied, got %v", err)
	}
	if av, _ := b.Available(runID); av.Tokens != 500 {
		t.Fatalf("reserva devia ter sido libertada, remanescente=%s", av)
	}
}

// ---------------------------------------------------------------------------
// Integração com o DAG: o sub-agente é admitido como nó-tarefa filho.
// ---------------------------------------------------------------------------

// TestSpawnIntegratesWithDAG prova que, com o GraphBuilder ligado, o sub-agente é
// admitido como nó-tarefa filho e a sua identidade sobrevive ao replay do DAG.
func TestSpawnIntegratesWithDAG(t *testing.T) {
	t.Parallel()
	const runID = "run-dag"
	store := newStore(t)
	producer := eventstore.Producer{NHIID: "nhi:orq"}
	gb, err := orchestrator.NewGraphBuilder(store, runID, producer,
		orchestrator.WithGraphTracer(&agentruntime.RecordingTracer{}))
	if err != nil {
		t.Fatalf("NewGraphBuilder: %v", err)
	}
	b, err := budget.New(runID, amt(1000, 1_000_000))
	if err != nil {
		t.Fatalf("budget.New: %v", err)
	}
	iss, _ := newIssuer(t)
	parent := issueParent(t, iss)
	tracer := &agentruntime.RecordingTracer{}
	del, err := orchestrator.NewDelegator(b, permittingRM(t), iss,
		orchestrator.WithDelegationGraph(gb), orchestrator.WithDelegationTracer(tracer),
		orchestrator.WithDelegationStore(store, producer))
	if err != nil {
		t.Fatalf("NewDelegator: %v", err)
	}
	ctx := context.Background()

	if _, err := del.Spawn(ctx, orchestrator.SpawnRequest{
		RunID: runID, ParentBudgetNode: runID, ChildBudgetNode: "ng",
		InheritedBudget: amt(100, 100_000), SpawnReserve: amt(100, 100_000),
		Depth: 1, ParentToken: parent.Compact, Child: childReq("agt-g"), ChildTaskID: "task-g",
	}); err != nil {
		t.Fatalf("spawn: %v", err)
	}

	if !gb.DAG().Has("task-g") {
		t.Fatal("o sub-agente devia ter sido admitido como nó-tarefa no DAG")
	}
	// Span invoke_agent do filho com custo USD.
	spans := tracer.SpansByOperation(agentruntime.OpInvokeAgent)
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span invoke_agent, got %d", len(spans))
	}
	if spans[0].Attributes[agentruntime.AttrCostUSD] != 0.1 {
		t.Fatalf("custo USD do span=%v, esperava 0.1", spans[0].Attributes[agentruntime.AttrCostUSD])
	}

	// A identidade filha sobrevive ao replay do DAG.
	rebuilt, err := orchestrator.RebuildDAG(ctx, store, runID)
	if err != nil {
		t.Fatalf("RebuildDAG: %v", err)
	}
	ag, ok := rebuilt.Agent("task-g")
	if !ok || ag.NHIID != "agt-g" {
		t.Fatalf("identidade filha não sobreviveu ao replay: %+v (ok=%v)", ag, ok)
	}
}

// ---------------------------------------------------------------------------
// Validação de construção e de entrada.
// ---------------------------------------------------------------------------

func TestDelegatorValidation(t *testing.T) {
	t.Parallel()
	iss, _ := newIssuer(t)
	b, _ := budget.New("r", amt(1, 1))
	m := permittingRM(t)

	if _, err := orchestrator.NewDelegator(nil, m, iss); !errors.Is(err, orchestrator.ErrDelegatorDeps) {
		t.Fatalf("reserver nil devia falhar, got %v", err)
	}
	if _, err := orchestrator.NewDelegator(b, nil, iss); !errors.Is(err, orchestrator.ErrDelegatorDeps) {
		t.Fatalf("mediator nil devia falhar, got %v", err)
	}
	if _, err := orchestrator.NewDelegator(b, m, nil); !errors.Is(err, orchestrator.ErrDelegatorDeps) {
		t.Fatalf("issuer nil devia falhar, got %v", err)
	}

	del, err := orchestrator.NewDelegator(b, m, iss)
	if err != nil {
		t.Fatalf("NewDelegator: %v", err)
	}
	// Finish com handle nil.
	if err := del.Finish(context.Background(), nil, true); !errors.Is(err, orchestrator.ErrNilHandle) {
		t.Fatalf("Finish(nil) devia falhar, got %v", err)
	}
	// Spawn malformado (campos obrigatórios em falta).
	if _, err := del.Spawn(context.Background(), orchestrator.SpawnRequest{RunID: ""}); !errors.Is(err, orchestrator.ErrInvalidSpawn) {
		t.Fatalf("spawn sem run_id devia falhar, got %v", err)
	}
	// Fatia maior que o orçamento herdado.
	parent := issueParent(t, iss)
	b2, _ := budget.New("r2", amt(1000, 1000))
	del2, _ := orchestrator.NewDelegator(b2, m, iss)
	if _, err := del2.Spawn(context.Background(), orchestrator.SpawnRequest{
		RunID: "r2", ParentBudgetNode: "r2", ChildBudgetNode: "c",
		InheritedBudget: amt(10, 10), SpawnReserve: amt(20, 20),
		Depth: 1, ParentToken: parent.Compact, Child: childReq("agt-q"),
	}); !errors.Is(err, orchestrator.ErrInvalidSpawn) {
		t.Fatalf("fatia > herdado devia falhar, got %v", err)
	}
	// Contexto cancelado.
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := del.Spawn(cctx, orchestrator.SpawnRequest{
		RunID: "r", ParentBudgetNode: "r", ChildBudgetNode: "c", InheritedBudget: amt(1, 1),
		SpawnReserve: amt(1, 1), Depth: 1, ParentToken: parent.Compact, Child: childReq("agt-y"),
	}); err == nil {
		t.Fatal("contexto cancelado devia falhar")
	}
}

// TestWithSpawnCapability prova que o ToolID/Capability da mediação são
// configuráveis: o RM só permite se o tool configurado estiver registado.
func TestWithSpawnCapability(t *testing.T) {
	t.Parallel()
	const runID = "run-cap"
	iss, _ := newIssuer(t)
	parent := issueParent(t, iss)
	m := rm.New()
	if err := m.Register("custom.spawn", func(context.Context, []byte) ([]byte, error) { return nil, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	b, _ := budget.New(runID, amt(1000, 1_000_000))
	del, err := orchestrator.NewDelegator(b, m, iss,
		orchestrator.WithSpawnCapability("custom.spawn", "cap:custom.spawn"))
	if err != nil {
		t.Fatalf("NewDelegator: %v", err)
	}
	if _, err := del.Spawn(context.Background(), orchestrator.SpawnRequest{
		RunID: runID, ParentBudgetNode: runID, ChildBudgetNode: "nc",
		InheritedBudget: amt(100, 100_000), SpawnReserve: amt(100, 100_000),
		Depth: 1, ParentToken: parent.Compact, Child: childReq("agt-c"),
	}); err != nil {
		t.Fatalf("spawn com tool configurado: %v", err)
	}
}

// ---------------------------------------------------------------------------
// AOS-026 remediação — profundidade autoritativa, fan-out real, span→pai.
// ---------------------------------------------------------------------------

// TestDepthGateIsAuthoritativeNotSelfDeclared prova (finding authority-control-
// bypass) que o gate de profundidade NÃO confia no campo auto-declarado req.Depth:
// (a) uma SUBDECLARAÇÃO (req.Depth abaixo da profundidade autoritativa derivada da
// cadeia do pai) é recusada fail-closed sem tocar no orçamento; (b) a profundidade
// autoritativa (len da cadeia do pai) é a que o cap aplica — um neto de um pai à
// profundidade == MaxDepth é recusado mesmo declarando uma profundidade baixa.
func TestDepthGateIsAuthoritativeNotSelfDeclared(t *testing.T) {
	t.Parallel()
	const runID = "run-depth-auth"
	store := newStore(t)
	b, err := budget.New(runID, amt(1000, 1_000_000))
	if err != nil {
		t.Fatalf("budget.New: %v", err)
	}
	iss, _ := newIssuer(t)
	parent := issueParent(t, iss) // cadeia len 1 → profundidade autoritativa 1
	del, err := orchestrator.NewDelegator(b, permittingRM(t), iss,
		orchestrator.WithDelegationStore(store, eventstore.Producer{NHIID: "nhi:orq"}))
	if err != nil {
		t.Fatalf("NewDelegator: %v", err)
	}
	ctx := context.Background()

	// (a) Subdeclaração: req.Depth=0 < autoritativa 1 → ErrDepthMismatch, orçamento intacto.
	_, err = del.Spawn(ctx, orchestrator.SpawnRequest{
		RunID: runID, ParentBudgetNode: runID, ChildBudgetNode: "n0",
		InheritedBudget: amt(100, 100_000), SpawnReserve: amt(100, 100_000),
		Depth: 0, ParentToken: parent.Compact, Child: childReq("agt-u"),
	})
	if !errors.Is(err, orchestrator.ErrDepthMismatch) {
		t.Fatalf("subdeclaração de profundidade devia recusar, got %v", err)
	}
	if av, _ := b.Available(runID); av.Tokens != 1000 {
		t.Fatalf("recusa por profundidade não devia tocar no orçamento, remanescente=%s", av)
	}
	if n := countEvents(t, store, runID, orchestrator.EventSpawnDenied); n != 1 {
		t.Fatalf("esperava 1 spawn_denied (depth_mismatch), got %d", n)
	}

	// Constrói um pai à profundidade autoritativa 2 (root → mid) com um delegator
	// de profundidade folgada.
	mid, err := del.Spawn(ctx, orchestrator.SpawnRequest{
		RunID: runID, ParentBudgetNode: runID, ChildBudgetNode: "nmid",
		InheritedBudget: amt(400, 400_000), SpawnReserve: amt(50, 50_000),
		Depth: 1, ParentToken: parent.Compact, Child: childReq("agt-mid"), ChildTaskID: "agt-mid",
	})
	if err != nil {
		t.Fatalf("spawn mid: %v", err)
	}

	// (b) Um delegator com MaxDepth=1 recusa um neto cuja profundidade AUTORITATIVA
	// (len da cadeia do pai 'mid' == 2) excede o cap — provando que o gate usa a
	// cadeia, não o campo. Declara-se Depth=2 (verdade) para isolar o cap do
	// cross-check de subdeclaração.
	delShallow, err := orchestrator.NewDelegator(b, permittingRM(t), iss, orchestrator.WithMaxDepth(1))
	if err != nil {
		t.Fatalf("NewDelegator shallow: %v", err)
	}
	_, err = delShallow.Spawn(ctx, orchestrator.SpawnRequest{
		RunID: runID, ParentBudgetNode: "nmid", ChildBudgetNode: "nleaf",
		InheritedBudget: amt(100, 100_000), SpawnReserve: amt(100, 100_000),
		Depth: 2, ParentToken: mid.ChildToken.Compact, Child: childReq("agt-leaf"),
	})
	if !errors.Is(err, orchestrator.ErrMaxDepthExceeded) {
		t.Fatalf("neto além do MaxDepth autoritativo devia recusar, got %v", err)
	}
}

// TestFanOutEnforcedByRealChildCount prova (finding authority-control-bypass) que o
// fan-out é imposto sobre o número REAL de filhos admitidos por nó-pai, e NÃO sobre
// o índice auto-reportado: N irmãos concorrentes, TODOS com FanOutIndex=0 (o
// "furo" original), admitem exactamente MaxFanOut e recusam os restantes com
// ErrMaxFanOutExceeded — sem corrida (-race), com orçamento sobrado.
func TestFanOutEnforcedByRealChildCount(t *testing.T) {
	t.Parallel()
	const runID = "run-fanout-real"
	const maxFan = 5
	const siblings = 30
	b, err := budget.New(runID, amt(1000, 1_000_000)) // orçamento não é o cap aqui
	if err != nil {
		t.Fatalf("budget.New: %v", err)
	}
	iss, _ := newIssuer(t)
	parent := issueParent(t, iss)
	del, err := orchestrator.NewDelegator(b, permittingRM(t), iss, orchestrator.WithMaxFanOut(maxFan))
	if err != nil {
		t.Fatalf("NewDelegator: %v", err)
	}

	var wg sync.WaitGroup
	var ok, fanDenied atomic.Int64
	start := make(chan struct{})
	for i := 0; i < siblings; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := del.Spawn(context.Background(), orchestrator.SpawnRequest{
				RunID: runID, ParentBudgetNode: runID, ChildBudgetNode: "cf-" + itoa(int64(i)),
				InheritedBudget: amt(1, 1_000), SpawnReserve: amt(1, 1_000),
				Depth: 1, FanOutIndex: 0, // TODOS declaram 0: só um contador real distingue.
				ParentToken: parent.Compact, Child: childReq("agt-cf-" + itoa(int64(i))),
			})
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, orchestrator.ErrMaxFanOutExceeded):
				fanDenied.Add(1)
			default:
				t.Errorf("erro inesperado no spawn %d: %v", i, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if got := ok.Load(); got != maxFan {
		t.Fatalf("admitidos=%d, esperava exactamente MaxFanOut=%d (nº real de filhos)", got, maxFan)
	}
	if got := fanDenied.Load(); got != siblings-maxFan {
		t.Fatalf("recusados por fan-out=%d, esperava %d", got, siblings-maxFan)
	}
}

// TestSpawnSpanLinksChildToParent prova (finding observability) que o span
// invoke_agent do filho carrega a LIGAÇÃO explícita ao pai: o NHI do pai imediato
// (aos.node.parent_nhi = Sub da folha da cadeia do filho) e o step do pai
// (aos.parent.step_id = req.ParentStepID), tornando a aresta filho→pai
// reconstruível a partir do span isolado.
func TestSpawnSpanLinksChildToParent(t *testing.T) {
	t.Parallel()
	const runID = "run-span-parent"
	b, err := budget.New(runID, amt(1000, 1_000_000))
	if err != nil {
		t.Fatalf("budget.New: %v", err)
	}
	iss, _ := newIssuer(t)
	parent := issueParent(t, iss)
	tracer := &agentruntime.RecordingTracer{}
	del, err := orchestrator.NewDelegator(b, permittingRM(t), iss, orchestrator.WithDelegationTracer(tracer))
	if err != nil {
		t.Fatalf("NewDelegator: %v", err)
	}
	if _, err := del.Spawn(context.Background(), orchestrator.SpawnRequest{
		RunID: runID, ParentStepID: "step-parent", ParentBudgetNode: runID, ChildBudgetNode: "nsp",
		InheritedBudget: amt(100, 100_000), SpawnReserve: amt(100, 100_000),
		Depth: 1, ParentToken: parent.Compact, Child: childReq("agt-sp"),
	}); err != nil {
		t.Fatalf("spawn: %v", err)
	}

	spans := tracer.SpansByOperation(agentruntime.OpInvokeAgent)
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span invoke_agent, got %d", len(spans))
	}
	attrs := spans[0].Attributes
	// O pai imediato é o agente-raiz (Sub da folha da cadeia do filho).
	if got := attrs["aos.node.parent_nhi"]; got != "agt-root" {
		t.Fatalf("aos.node.parent_nhi=%v, esperava agt-root", got)
	}
	if got := attrs["aos.parent.step_id"]; got != "step-parent" {
		t.Fatalf("aos.parent.step_id=%v, esperava step-parent", got)
	}
}

// ---------------------------------------------------------------------------
// Auxiliares de replay.
// ---------------------------------------------------------------------------

func countEvents(t *testing.T, store *eventstore.Store, runID, evType string) int {
	t.Helper()
	events, err := store.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	n := 0
	for _, e := range events {
		if e.Type == evType {
			n++
		}
	}
	return n
}

// rebuildTree reconstrói a topologia da árvore de delegação (childNode→parentNode)
// a partir dos eventos subagent.spawned.
func rebuildTree(t *testing.T, store *eventstore.Store, runID string) map[string]string {
	t.Helper()
	events, err := store.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	tree := map[string]string{}
	for _, e := range events {
		if e.Type != orchestrator.EventSubagentSpawned {
			continue
		}
		var p orchestrator.DelegationEventPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("unmarshal spawned: %v", err)
		}
		tree[p.ChildNode] = p.ParentNode
	}
	return tree
}
