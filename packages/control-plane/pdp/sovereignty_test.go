package pdp

import (
	"context"
	"encoding/json"
	"testing"

	govsov "github.com/aos-ref/control-plane/governance/sovereignty"
	rm "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-094 — soberania por board: o PDP resolve o board do escopo de identidade para
// a sua região autorizada (registo GOV) e EMITE-a como obrigação `region` que o PEP
// (AOS-087) impõe. Fail-closed: board desconhecido/vazio ⇒ deny.

// boardRegistry é o registo de referência dos testes (board-eu→eu, board-us→us).
func boardRegistry() *govsov.Registry {
	return govsov.NewRegistry(map[string]string{"board-eu": "eu", "board-us": "us"})
}

// findRegionObligation devolve a obrigação `region` (se houver) e a região exigida.
func findRegionObligation(obs []Obligation) (Obligation, bool) {
	for _, o := range obs {
		if o.Type == ObligationRegion {
			return o, true
		}
	}
	return Obligation{}, false
}

// TestDecide_BoardRegion_EmitsObligation (AC2, PDP-emite): um permit de um principal
// com board conhecido leva a obrigação `region` = região autorizada do board.
func TestDecide_BoardRegion_EmitsObligation(t *testing.T) {
	t.Parallel()
	p := mustOpen(t)
	WithBoardRegions(boardRegistry())(p)

	in := httpPost()
	in.Principal.Board = "board-eu"
	dec, err := p.Decide(context.Background(), in)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !dec.Permitted() {
		t.Fatalf("Effect=%q; quero permit (%s)", dec.Effect, dec.Reason)
	}
	ob, ok := findRegionObligation(dec.Obligations)
	if !ok {
		t.Fatalf("sem obrigacao region; obrigacoes=%+v", dec.Obligations)
	}
	if ob.Params["region"] != "eu" {
		t.Fatalf("region=%q; quero eu", ob.Params["region"])
	}
}

// TestDecide_UnknownBoard_DenyFailClosed (fail-closed): board não registado ⇒ deny,
// nunca cross-border por omissão (ADR-011).
func TestDecide_UnknownBoard_DenyFailClosed(t *testing.T) {
	t.Parallel()
	p := mustOpen(t)
	WithBoardRegions(boardRegistry())(p)

	in := httpPost()
	in.Principal.Board = "board-desconhecido"
	dec, err := p.Decide(context.Background(), in)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.Effect != Deny {
		t.Fatalf("Effect=%q; quero deny fail-closed (%s)", dec.Effect, dec.Reason)
	}
	if !contains(dec.Reason, "sovereignty") || !contains(dec.Reason, "board-desconhecido") {
		t.Fatalf("reason=%q; quero motivo de soberania nomeando o board", dec.Reason)
	}
	if _, ok := findRegionObligation(dec.Obligations); ok {
		t.Fatal("um deny nao devia emitir obrigacao region")
	}
}

// TestDecide_EmptyBoard_DenyFailClosed (fail-closed): com registo ligado, um
// principal SEM board não tem fronteira resolvível ⇒ deny.
func TestDecide_EmptyBoard_DenyFailClosed(t *testing.T) {
	t.Parallel()
	p := mustOpen(t)
	WithBoardRegions(boardRegistry())(p)

	in := httpPost() // Principal.Board == ""
	dec, err := p.Decide(context.Background(), in)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.Effect != Deny {
		t.Fatalf("Effect=%q; quero deny fail-closed (%s)", dec.Effect, dec.Reason)
	}
}

// TestDecide_NoRegistry_NoRegionObligation (compat): sem registo ligado, a soberania
// é inerte — nenhuma obrigação region, o PDP decide como antes.
func TestDecide_NoRegistry_NoRegionObligation(t *testing.T) {
	t.Parallel()
	p := mustOpen(t) // sem WithBoardRegions

	in := httpPost()
	in.Principal.Board = "board-eu" // ignorado sem registo
	dec, err := p.Decide(context.Background(), in)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !dec.Permitted() {
		t.Fatalf("Effect=%q; quero permit (%s)", dec.Effect, dec.Reason)
	}
	if _, ok := findRegionObligation(dec.Obligations); ok {
		t.Fatal("sem registo ligado nao devia haver obrigacao region")
	}
}

// TestSovereigntyRegistry_Accessor expõe o registo ligado (nil sem WithBoardRegions)
// — a mesma fonte de verdade GOV que o Model Gateway pode compor.
func TestSovereigntyRegistry_Accessor(t *testing.T) {
	t.Parallel()
	p := mustOpen(t)
	if p.SovereigntyRegistry() != nil {
		t.Fatal("sem WithBoardRegions o registo deve ser nil")
	}
	reg := boardRegistry()
	WithBoardRegions(reg)(p)
	if got := p.SovereigntyRegistry(); got != reg {
		t.Fatalf("SovereigntyRegistry devolveu %p; quero o registo ligado %p", got, reg)
	}
}

// TestInputFromCall_CarriesBoard (regressão do adaptador de produção): o adaptador
// [inputFromCall] — o caminho que PolicyCheck.Evaluate usa em produção — TEM de
// transportar o board do escopo de identidade ao Input do PDP. Sem isto a soberania
// por board só funcionaria através de um hook de teste (o defeito que esta correcção
// fecha): o board resolvido pela identidade seria descartado antes do PDP.
func TestInputFromCall_CarriesBoard(t *testing.T) {
	t.Parallel()
	call := &rm.Call{
		Capability: "cap:http.post",
		Principal:  rm.Principal{NHIID: "nhi-1", AgentClass: "agent-worker", Board: "board-eu"},
	}
	in := inputFromCall(call)
	if in.Principal.Board != "board-eu" {
		t.Fatalf("inputFromCall descartou o board: Board=%q; quero board-eu", in.Principal.Board)
	}
}

// boardIdentityHook é um hook de IDENTIDADE de teste que RESOLVE o board no escopo do
// principal — exactamente o papel do [identity.IdentityCheck] real, que substitui o
// Principal do *Call partilhado a partir do token NHI verificado (AOS-005) ANTES do
// hook de política. Em produção o board chega deste token verificado (uma claim de
// board é EPIC-01, fora do âmbito de AOS-094); aqui injecta-se para exercitar a cadeia
// com o adaptador de política REAL a jusante.
//
// A diferença face à versão anterior do teste é deliberada: o hook de política é agora
// o [PolicyCheck] de PRODUÇÃO ([NewPolicyCheck]) — provando que o board flui pelo
// adaptador real [inputFromCall], não por uma réplica do adaptador embutida no teste.
type boardIdentityHook struct{ board string }

func (boardIdentityHook) Name() string { return "identity" }

func (h boardIdentityHook) Evaluate(_ context.Context, call *rm.Call) (rm.HookResult, error) {
	// Mutação do *Call partilhado (o mecanismo já usado pelo IdentityCheck real): o
	// board resolvido da NHI verificada passa a integrar o escopo de identidade que o
	// PolicyCheck a jusante lê via inputFromCall.
	call.Principal.Board = h.board
	return rm.HookResult{Decision: rm.HookAllow}, nil
}

// buildSovereignRM compõe um RM com a cadeia de PRODUÇÃO [boardIdentityHook →
// NewPolicyCheck(PDP)] e os stubs neutros, gravando no Event Store real (para provar a
// selagem do deny). O board é resolvido pelo hook de identidade e o adaptador de
// política REAL (inputFromCall) transporta-o ao PDP — a cadeia PDP-emite → PEP-impõe
// atravessa o código de produção, não um hook de política de teste.
func buildSovereignRM(t testing.TB, store eventstore.EventStore, board string) *rm.Monitor {
	t.Helper()
	p := mustOpen(t)
	WithBoardRegions(boardRegistry())(p)
	m := rm.New(
		rm.WithHooks(
			boardIdentityHook{board: board},
			NewPolicyCheck(p),
			rm.BudgetStub{},
			rm.EgressStub{},
			rm.AuditStub{},
		),
		rm.WithEventSink(rm.NewEventStoreSink(store)),
	)
	if err := m.Register("tool.http", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return m
}

// sovereignCall é um Call permitido pela política base (region eu, taint trusted).
func sovereignCall(region string) rm.Call {
	return rm.Call{
		RequestID: "req-sov", RunID: "run-sov", StepID: "s1",
		ToolID: "tool.http", Capability: "cap:http.post",
		Resource:  rm.Resource{Type: "url", Value: "https://api.example.com/orders", Region: region},
		Principal: rm.Principal{NHIID: "nhi-1", AgentClass: "agent-worker", Authority: []string{"cap:http.post"}},
		Context:   rm.CallContext{Taint: "trusted"},
		Input:     []byte(`{"body":"order"}`),
	}
}

// TestIntegration_PDP_PEP_RegionObligation_BlocksCrossBorder (AC2 cadeia + AC3 + AC5):
// um board cuja região autorizada é `us` que tenta um roteamento para um recurso em
// `eu` é NEGADO pelo PEP (enforceRegion) — a obrigação `region` emitida pelo PDP
// impõe-se ANTES do dispatch — e o deny é SELADO no audit com o motivo de soberania.
// Nota: o recurso está em `eu` (que a política BASE permite), pelo que o deny provém
// EXCLUSIVAMENTE da obrigação de soberania do board, não da regra Cedar de base.
func TestIntegration_PDP_PEP_RegionObligation_BlocksCrossBorder(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()

	m := buildSovereignRM(t, store, "board-us")

	var dispatched bool
	_ = m.Register("tool.http", func(_ context.Context, in []byte) ([]byte, error) {
		dispatched = true
		return in, nil
	})

	// Board-us (região autorizada us) com recurso em `eu`: cross-border. A política
	// base PERMITE `eu`; o deny vem da obrigação de soberania (região exigida us).
	call := sovereignCall("eu")
	d, err := m.Mediate(context.Background(), call)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if d.Effect != rm.EffectDeny {
		t.Fatalf("Effect=%q; quero DENY cross-border (%s)", d.Effect, d.Reason)
	}
	if d.DeniedBy != "obligation" {
		t.Errorf("DeniedBy=%q; quero obligation (o PEP impôs a região)", d.DeniedBy)
	}
	if dispatched {
		t.Error("a tool NÃO devia ser despachada: efeito cross-border negado antes do dispatch")
	}
	// O deny cross-border é um EVENTO SELADO (AC5): motivo de região no evento.
	ev := readOne(t, store, "run-sov")
	if ev.Type != rm.EventTypeDenied {
		t.Errorf("Type=%q; quero %q", ev.Type, rm.EventTypeDenied)
	}
	var pl mediationPayloadView
	if err := json.Unmarshal(ev.Payload, &pl); err != nil {
		t.Fatalf("payload invalido: %v", err)
	}
	if !contains(pl.Reason, "regiao") && !contains(pl.Reason, "cross-border") {
		t.Errorf("reason selada=%q; quero motivo de soberania (regiao/cross-border)", pl.Reason)
	}
}

// TestIntegration_PDP_PEP_RegionObligation_AllowsIntraBorder (AC2 cadeia, simétrico):
// o mesmo board UE com um recurso em `eu` é PERMITIDO — a obrigação de região é
// satisfeita e o efeito é despachado.
func TestIntegration_PDP_PEP_RegionObligation_AllowsIntraBorder(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()

	m := buildSovereignRM(t, store, "board-eu")

	call := sovereignCall("eu")
	d, err := m.Mediate(context.Background(), call)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if !d.Permitted() {
		t.Fatalf("Effect=%q; quero permit intra-fronteira (%s)", d.Effect, d.Reason)
	}
}
