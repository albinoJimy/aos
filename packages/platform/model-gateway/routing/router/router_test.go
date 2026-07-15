package router

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/platform/model-gateway/routing/degradation"
	"github.com/aos-ref/platform/model-gateway/routing/sovereignty"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
)

func refLadder() *tiering.Ladder {
	return tiering.NewLadder(
		tiering.Tier{Name: "frontier", Model: "big", CostRank: 3, Capability: tiering.CapabilityFrontier},
		tiering.Tier{Name: "standard", Model: "mid", CostRank: 2, Capability: tiering.CapabilityStandard, Fast: true},
		tiering.Tier{Name: "economy", Model: "small", CostRank: 1, Capability: tiering.CapabilityBasic, Fast: true},
	)
}

// degradeLadder tem DOIS tiers na MESMA capacidade (Frontier) a custos diferentes:
// um rápido/caro e um lento/barato. É o único cenário em que a degradação por
// orçamento PRESERVA a capacidade exigida — sacrifica latência/custo, nunca a
// capacidade. Uma tarefa interactiva Frontier começa no rápido/caro (favorece
// latência) e degrada para o Frontier lento/barato.
func degradeLadder() *tiering.Ladder {
	return tiering.NewLadder(
		tiering.Tier{Name: "frontier-fast", Model: "big-fast", CostRank: 3, Capability: tiering.CapabilityFrontier, Fast: true},
		tiering.Tier{Name: "frontier-cheap", Model: "big-cheap", CostRank: 2, Capability: tiering.CapabilityFrontier},
		tiering.Tier{Name: "economy", Model: "small", CostRank: 1, Capability: tiering.CapabilityBasic, Fast: true},
	)
}

// allowlist de teste: permite qualquer modelo nas regiões dadas de um board.
type fakeAllow struct {
	// allowed[board][model][region] = true
	allowed map[string]map[string]map[string]bool
	all     bool
}

func (f fakeAllow) Allows(board, model, region string) bool {
	if f.all {
		return true
	}
	if m, ok := f.allowed[board]; ok {
		if r, ok := m[model]; ok {
			return r[region]
		}
	}
	return false
}

func allowAllModels(regions ...string) fakeAllow {
	f := fakeAllow{all: true}
	return f
}

func ctx() context.Context { return context.Background() }

func TestRouteLeastLoadedRegion(t *testing.T) {
	// Duas regiões da MESMA fronteira (eu): eu-west muito carregada, eu-central com
	// folga. O router deve escolher a MENOS carregada (eu-central).
	guard := sovereignty.NewGuard(
		sovereignty.WithBoundary("eu-west", "eu"),
		sovereignty.WithBoundary("eu-central", "eu"),
	)
	load := NewStaticLoadProvider(Headroom{WorstUsed: 0, WorstLimit: 100}).
		Set("openai", "eu-west", Headroom{WorstUsed: 95, WorstLimit: 100}).   // 95% usado
		Set("openai", "eu-central", Headroom{WorstUsed: 10, WorstLimit: 100}) // 10% usado

	r := New(refLadder(),
		WithGuard(guard),
		WithAllowlist(allowAllModels()),
		WithLoadProvider(load),
	)
	d, err := r.Route(ctx(), Request{
		Board: "b1", Provider: "openai", Region: "eu-west",
		Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch,
		Candidates: []sovereignty.Endpoint{
			{KeyID: "acct-west", Region: "eu-west"},
			{KeyID: "acct-central", Region: "eu-central"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Region != "eu-central" {
		t.Errorf("esperava a regiao menos carregada eu-central, obtive %q", d.Region)
	}
	if d.Outcome != OutcomeRouted {
		t.Errorf("esperava OutcomeRouted, obtive %q", d.Outcome)
	}
}

func TestRouteNeverCrossBorder(t *testing.T) {
	// eu-west (home) saturada; a única alternativa é us-east (cross-border). O
	// router NUNCA escolhe cross-border: rejeita (soberania sobre disponibilidade).
	guard := sovereignty.NewGuard(
		sovereignty.WithBoundary("eu-west", "eu"),
		sovereignty.WithBoundary("us-east", "us"),
	)
	r := New(refLadder(),
		WithGuard(guard),
		WithAllowlist(allowAllModels()),
	)
	d, err := r.Route(ctx(), Request{
		Board: "b1", Provider: "openai", Region: "eu-west",
		Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch,
		Candidates: []sovereignty.Endpoint{
			{KeyID: "acct-us", Region: "us-east"}, // só cross-border disponível
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != OutcomeRejected {
		t.Fatalf("esperava REJECT (sem failover cross-border), obtive %q regiao=%q", d.Outcome, d.Region)
	}
	if len(d.Dropped) != 1 || d.Dropped[0].Region != "us-east" {
		t.Errorf("o candidato cross-border deve ser DESCARTADO (prova estrutural), dropped=%v", d.Dropped)
	}
}

func TestRouteTierCostCapacity(t *testing.T) {
	r := New(refLadder(), WithAllowlist(allowAllModels()))
	// Raciocínio ⇒ frontier; extracção ⇒ economy.
	reason, _ := r.Route(ctx(), Request{Board: "b1", Provider: "openai", Region: "eu", Capability: tiering.CapabilityFrontier, Class: tiering.ClassBatch})
	if reason.Model != "big" {
		t.Errorf("raciocinio deve escolher frontier (big), obtive %q", reason.Model)
	}
	extr, _ := r.Route(ctx(), Request{Board: "b1", Provider: "openai", Region: "eu", Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch})
	if extr.Model != "small" {
		t.Errorf("extraccao deve escolher economy (small), obtive %q", extr.Model)
	}
}

func TestRouteRejectsOutsideAllowlist(t *testing.T) {
	// A allowlist do board só permite o modelo "small" em "eu". Uma tarefa que exija
	// frontier (big) não tem tier elegível dentro da fronteira ⇒ REJECT (nunca
	// escolhe um modelo fora da allowlist).
	allow := fakeAllow{allowed: map[string]map[string]map[string]bool{
		"b1": {"small": {"eu": true}},
	}}
	r := New(refLadder(), WithAllowlist(allow))
	d, _ := r.Route(ctx(), Request{Board: "b1", Provider: "openai", Region: "eu", Capability: tiering.CapabilityFrontier, Class: tiering.ClassBatch})
	if d.Outcome != OutcomeRejected {
		t.Errorf("frontier fora da allowlist deve REJEITAR, obtive %q modelo=%q", d.Outcome, d.Model)
	}
}

func TestRouteGracefulExhaustionDegrades(t *testing.T) {
	// A ~80% do orçamento, o router OFERECE degradar para um tier mais barato que
	// AINDA satisfaz a capacidade exigida (exaustão graciosa preservando capacidade):
	// uma tarefa INTERACTIVA Frontier começa no tier rápido/caro e degrada para o
	// tier Frontier lento/barato — NUNCA para um tier incapaz.
	budget := degradation.NewStaticBudgetProvider(degradation.BudgetState{}).
		Set(degradation.BudgetKey{Board: "b1", Tenant: "b1"}, degradation.BudgetState{Used: 85, Limit: 100})
	r := New(degradeLadder(),
		WithAllowlist(allowAllModels()),
		WithBudget(budget),
	)
	d, _ := r.Route(ctx(), Request{Board: "b1", Tenant: "b1", Provider: "openai", Region: "eu", Capability: tiering.CapabilityFrontier, Class: tiering.ClassInteractive})
	if !d.Degraded {
		t.Fatal("a 85%% do orcamento deve OFERECER degradacao (exaustao graciosa)")
	}
	if d.Outcome != OutcomeDegraded {
		t.Errorf("esperava OutcomeDegraded, obtive %q", d.Outcome)
	}
	if d.FromTier != "frontier-fast" || d.ToTier != "frontier-cheap" {
		t.Errorf("degrade esperado frontier-fast->frontier-cheap, obtive %s->%s", d.FromTier, d.ToTier)
	}
	// A capacidade EXIGIDA (Frontier) foi PRESERVADA na degradação (nunca abaixo).
	if to, ok := degradeLadder().TierOf(d.ToTier); !ok || to.Capability < tiering.CapabilityFrontier {
		t.Errorf("degradacao NUNCA deve descer abaixo da capacidade exigida (Frontier), destino=%q", d.ToTier)
	}
}

func TestRouteBudgetNeverDegradesBelowCapability(t *testing.T) {
	// Finding AOS-059 (tier-incapaz): com uma escada de UM tier por capacidade
	// (refLadder), uma tarefa Frontier a 85%% do orçamento NÃO tem degrau mais barato
	// CAPAZ — standard(=Standard) e economy(=Basic) estão abaixo da capacidade
	// exigida. O router NÃO degrada para um tier incapaz: mantém frontier. A
	// degradação por orçamento nunca viola a capacidade exigida.
	budget := degradation.NewStaticBudgetProvider(degradation.BudgetState{}).
		Set(degradation.BudgetKey{Board: "b1", Tenant: "b1"}, degradation.BudgetState{Used: 85, Limit: 100})
	r := New(refLadder(), WithAllowlist(allowAllModels()), WithBudget(budget))
	d, _ := r.Route(ctx(), Request{Board: "b1", Tenant: "b1", Provider: "openai", Region: "eu", Capability: tiering.CapabilityFrontier, Class: tiering.ClassBatch})
	if d.Degraded {
		t.Errorf("NUNCA degradar abaixo da capacidade exigida: obtive degrade para %q", d.ToTier)
	}
	if d.Model != "big" || d.Tier != "frontier" {
		t.Errorf("deve MANTER o tier capaz (frontier/big), obtive %q/%q", d.Tier, d.Model)
	}
	if d.Outcome != OutcomeRouted {
		t.Errorf("sem degrau capaz mais barato: mantem-se routed (a cadeia do Escalonador decide), obtive %q", d.Outcome)
	}
}

func TestRouteExhaustedNoCheaperPropagatesSignal(t *testing.T) {
	// Finding AOS-059 (orcamento-nao-aplicado): orçamento ESGOTADO (>=100%) e já no
	// tier mais barato capaz — sem degrau para onde descer. O router NÃO faz
	// hard-stop cego, mas PROPAGA o sinal BudgetExhausted (+razao distinta
	// "exhausted-no-cheaper") para o Escalonador poder rejeitar de forma informada,
	// em vez de gastar em silêncio sem sinal fiel.
	budget := degradation.NewStaticBudgetProvider(degradation.BudgetState{}).
		Set(degradation.BudgetKey{Board: "b1", Tenant: "b1"}, degradation.BudgetState{Used: 120, Limit: 100})
	sink := &captureSink{}
	r := New(refLadder(), WithAllowlist(allowAllModels()), WithBudget(budget), WithDecisionSink(sink))
	d, _ := r.Route(ctx(), Request{Board: "b1", Tenant: "b1", Provider: "openai", Region: "eu", Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch})
	if !d.BudgetExhausted {
		t.Error("orcamento esgotado deve propagar BudgetExhausted=true (sinal fiel para o Escalonador)")
	}
	if d.Degraded {
		t.Error("ja no tier mais barato: nao ha degrau (nao deve marcar Degraded)")
	}
	if d.Outcome != OutcomeRouted {
		t.Errorf("nunca hard-stop cego no router, obtive %q", d.Outcome)
	}
	if !strings.Contains(d.Reason, "exhausted-no-cheaper") {
		t.Errorf("a razao deve distinguir 'exhausted-no-cheaper' de 'routed', obtive %q", d.Reason)
	}
	if len(sink.decisions) != 1 || !sink.decisions[0].BudgetExhausted {
		t.Error("a decisao registada no sink deve carregar BudgetExhausted (observabilidade post-hoc)")
	}
}

func TestRouteBelowThresholdNoDegrade(t *testing.T) {
	budget := degradation.NewStaticBudgetProvider(degradation.BudgetState{}).
		Set(degradation.BudgetKey{Board: "b1", Tenant: "b1"}, degradation.BudgetState{Used: 50, Limit: 100})
	r := New(refLadder(), WithAllowlist(allowAllModels()), WithBudget(budget))
	d, _ := r.Route(ctx(), Request{Board: "b1", Tenant: "b1", Provider: "openai", Region: "eu", Capability: tiering.CapabilityFrontier, Class: tiering.ClassBatch})
	if d.Degraded {
		t.Error("abaixo do limiar de orcamento nao deve degradar")
	}
}

func TestRouteGracefulExhaustionNeverHardStop(t *testing.T) {
	// Orçamento ESGOTADO (100%) mas já no tier mais barato: NÃO faz hard-stop cego —
	// mantém a rota no tier mais barato (a cadeia do Escalonador é que rejeita, se
	// preciso). Aqui a tarefa é basic ⇒ economy (já o mais barato): fica routed.
	budget := degradation.NewStaticBudgetProvider(degradation.BudgetState{}).
		Set(degradation.BudgetKey{Board: "b1", Tenant: "b1"}, degradation.BudgetState{Used: 100, Limit: 100})
	r := New(refLadder(), WithAllowlist(allowAllModels()), WithBudget(budget))
	d, _ := r.Route(ctx(), Request{Board: "b1", Tenant: "b1", Provider: "openai", Region: "eu", Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch})
	if d.Outcome == OutcomeRejected {
		t.Error("esgotado no tier mais barato NAO deve hard-stop cego no router (a cadeia do Escalonador decide)")
	}
	if d.Model != "small" {
		t.Errorf("deve manter o tier mais barato (small), obtive %q", d.Model)
	}
}

func TestRouteAdmissionDefer(t *testing.T) {
	// Admission global sem headroom ⇒ DEFER (nunca despacha sem debito reservado).
	adm := NewStaticAdmissionCoordinator(1000, 10, 250*time.Millisecond)
	adm.SetLimit("openai", "small", "eu", 0, 0) // tecto 0 ⇒ rejeição permanente? cost>cap
	// Melhor: tecto pequeno já esgotado. Reservamos até saturar.
	adm2 := NewStaticAdmissionCoordinator(1000, 1, 250*time.Millisecond)
	r := New(refLadder(), WithAllowlist(allowAllModels()), WithAdmission(adm2))
	// 1.ª chamada consome o único request disponível (RPM=1).
	d1, _ := r.Route(ctx(), Request{Board: "b1", Tenant: "b1", Provider: "openai", Region: "eu", Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch, EstimatedTokens: 10})
	if d1.Outcome != OutcomeRouted {
		t.Fatalf("1a chamada deve ser admitida, obtive %q", d1.Outcome)
	}
	// 2.ª chamada: sem headroom (RPM esgotado) ⇒ DEFER.
	d2, _ := r.Route(ctx(), Request{Board: "b2", Tenant: "b2", Provider: "openai", Region: "eu", Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch, EstimatedTokens: 10})
	if d2.Outcome != OutcomeDeferred {
		t.Errorf("2a chamada sem headroom global deve ADIAR (defer), obtive %q", d2.Outcome)
	}
	if d2.RetryAfter <= 0 {
		t.Error("defer deve trazer retry_after > 0")
	}
	_ = adm
}

func TestRouteAdmissionNoAggregateCollapse(t *testing.T) {
	// ADR-008: vários boards, cada um dentro do "seu" pedido, PARTILHAM o mesmo
	// tecto global. A coordenação a montante impede o colapso agregado: quando a
	// soma satura, os boards seguintes são ADIADOS em vez de saturarem o rate limit.
	// Tecto global: 3 requests. 5 boards tentam ⇒ 3 admitidos, 2 adiados.
	adm := NewStaticAdmissionCoordinator(100000, 3, time.Second)
	r := New(refLadder(), WithAllowlist(allowAllModels()), WithAdmission(adm))
	admitted, deferred := 0, 0
	for _, b := range []string{"b1", "b2", "b3", "b4", "b5"} {
		d, err := r.Route(ctx(), Request{Board: b, Tenant: b, Provider: "openai", Region: "eu", Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch, EstimatedTokens: 10})
		if err != nil {
			t.Fatal(err)
		}
		switch d.Outcome {
		case OutcomeRouted:
			admitted++
		case OutcomeDeferred:
			deferred++
		default:
			t.Fatalf("board %s: resultado inesperado %q", b, d.Outcome)
		}
	}
	if admitted != 3 || deferred != 2 {
		t.Errorf("esperava 3 admitidos + 2 adiados (sem colapso agregado), obtive %d admitidos %d adiados", admitted, deferred)
	}
}

func TestRouteKeypoolLeastLoadedAccount(t *testing.T) {
	// A conta pooled menos-carregada é escolhida (composicao AOS-057). Usamos um
	// fake keypool que devolve um KeyID fixo, provando que o router o propaga.
	r := New(refLadder(), WithAllowlist(allowAllModels()), WithKeyPool(fakeKeyPool{key: "acct-free"}))
	d, _ := r.Route(ctx(), Request{Board: "b1", Provider: "openai", Region: "eu", Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch})
	if d.KeyID != "acct-free" {
		t.Errorf("esperava KeyID da conta pooled (acct-free), obtive %q", d.KeyID)
	}
}

func TestRouteKeypoolSaturatedDefers(t *testing.T) {
	r := New(refLadder(), WithAllowlist(allowAllModels()), WithKeyPool(fakeKeyPool{err: errSaturated}))
	d, _ := r.Route(ctx(), Request{Board: "b1", Provider: "openai", Region: "eu", Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch})
	if d.Outcome != OutcomeDeferred {
		t.Errorf("pool saturado deve ADIAR (nunca despachar acima do throughput), obtive %q", d.Outcome)
	}
}

func TestRouteRecordsDecisionForCostAnalysis(t *testing.T) {
	// Cada decisão de tiering é registada (modelo/tier/razão) para análise post-hoc.
	sink := &captureSink{}
	r := New(refLadder(), WithAllowlist(allowAllModels()), WithDecisionSink(sink))
	_, _ = r.Route(ctx(), Request{Board: "b1", Provider: "openai", Region: "eu", Capability: tiering.CapabilityFrontier, Class: tiering.ClassInteractive})
	if len(sink.decisions) != 1 {
		t.Fatalf("esperava 1 decisao registada, obtive %d", len(sink.decisions))
	}
	d := sink.decisions[0]
	if d.Model != "big" || d.Tier != "frontier" || d.Reason == "" {
		t.Errorf("decisao registada deve ter modelo/tier/razao, obtive modelo=%q tier=%q razao=%q", d.Model, d.Tier, d.Reason)
	}
}

func TestRouteDeterministic(t *testing.T) {
	guard := sovereignty.NewGuard(sovereignty.WithBoundary("eu-a", "eu"), sovereignty.WithBoundary("eu-b", "eu"))
	load := NewStaticLoadProvider(Headroom{WorstUsed: 50, WorstLimit: 100})
	r := New(refLadder(), WithGuard(guard), WithAllowlist(allowAllModels()), WithLoadProvider(load))
	req := Request{Board: "b1", Provider: "openai", Region: "eu-a", Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch,
		Candidates: []sovereignty.Endpoint{{KeyID: "a", Region: "eu-a"}, {KeyID: "b", Region: "eu-b"}}}
	first, _ := r.Route(ctx(), req)
	for i := 0; i < 50; i++ {
		got, _ := r.Route(ctx(), req)
		if got.Region != first.Region || got.Model != first.Model {
			t.Fatalf("roteamento nao-determinista: %q/%q != %q/%q", got.Region, got.Model, first.Region, first.Model)
		}
	}
}

// --- fakes de teste ---

type captureSink struct{ decisions []Decision }

func (c *captureSink) Record(_ context.Context, d Decision) { c.decisions = append(c.decisions, d) }

type fakeKeyPool struct {
	key string
	err error
}

func (f fakeKeyPool) Select(_, _ string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.key, nil
}

var errSaturated = &poolErr{}

type poolErr struct{}

func (*poolErr) Error() string { return "pool saturado" }
