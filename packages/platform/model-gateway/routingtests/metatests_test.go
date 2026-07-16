package routingtests

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/routing/router"
	"github.com/aos-ref/platform/model-gateway/routing/sovereignty"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
)

// ===========================================================================
// META-TESTES (não-vacuidade) — o coração do padrão AOS-054. Para CADA cenário, com
// o controlo real CONTORNADO/desligado, o ataque PASSA. Se um destes deixasse de
// detectar (o ataque a ser bloqueado mesmo sem o controlo), o teste principal seria
// verde-VAZIO. Provam que os cenários DETECTAM mesmo.
// ===========================================================================

// META 1 — sem o admission GLOBAL (coordenação removida), quatro boards que
// partilham o tecto são TODOS despachados: o colapso agregado ACONTECE. Prova que o
// cenário 1(B) detecta a coordenação (com ela, o 4.º é adiado).
func TestMetaDetects_AggregateCollapse(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.adm.SetLimit(provider, modelStandard, regEUCentral, 300, 1_000_000) // tecto partilhado 300
	r := h.routerNoAdmission()                                            // CONTROLO DESLIGADO
	granted := 0
	for _, b := range []string{"board-eu", "board-fr", "board-de", "board-pt"} {
		rq := router.Request{Board: b, Tenant: b, Provider: provider, Region: regEUCentral,
			Capability: tiering.CapabilityStandard, Class: tiering.ClassBatch, EstimatedTokens: 100}
		dec := mustRoute(t, r, rq)
		if dec.Outcome == router.OutcomeRouted {
			granted++
		}
	}
	if granted != 4 {
		t.Fatalf("meta não-vácua falhou: sem admission esperava colapso (4 despachados > tecto 300), obtive %d", granted)
	}
}

// META 2 — sem o sinal de CARGA (LoadProvider removido), o router escolhe às cegas
// (desempate estável por KeyID) e cai no endpoint SATURADO. Prova que o cenário
// 1(A) detecta a selecção least-loaded (com o sinal, evita o saturado).
func TestMetaDetects_BlindUnderSaturation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cands := []sovereignty.Endpoint{ep("k-euc", regEUCentral), ep("k-euw", regEUWest)}
	// k-euc (eu-central) é o primeiro por KeyID; saturamo-lo e deixamos eu-west folgado.
	h.load.Set(provider, regEUCentral, router.Headroom{Saturated: true, WorstUsed: 100, WorstLimit: 100})
	h.load.Set(provider, regEUWest, router.Headroom{WorstUsed: 1, WorstLimit: 100})

	blind := mustRoute(t, h.routerNoLoad(), req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 10, cands...))
	if blind.Region != regEUCentral {
		t.Fatalf("meta não-vácua falhou: sem carga esperava a escolha cega do saturado %s, obtive %s", regEUCentral, blind.Region)
	}
	// Contraprova: COM o sinal de carga, o mesmo pedido evita o saturado.
	aware := mustRoute(t, h.router(), req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 10, cands...))
	if aware.Region != regEUWest {
		t.Fatalf("contraprova: com carga esperava %s, obtive %s", regEUWest, aware.Region)
	}
}

// META 3 — ignorando o piso de CAPACIDADE, uma selecção "cheapest ingénua" serve uma
// tarefa Frontier com um tier INCAPAZ. Para paridade de rigor com os restantes metas
// (que instanciam um controlo REALMENTE contornado e observam o ataque PASSAR), esta
// via NÃO compara campos: EXECUTA de facto uma selecção na escada REAL via Select, mas
// com o piso de capacidade contornado — pede só o mínimo (Basic) em vez do Frontier
// exigido, sob um filtro allow-all (sem soberania/allowlist). É a via que uma
// implementação ingénua tomaria; resolve para um tier incapaz — o ataque a PASSAR.
// Prova que o cenário 2 detecta a regra "mais barato que SATISFAZ a capacidade".
func TestMetaDetects_IncapableTierWhenCapabilityIgnored(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	// CONTROLO CONTORNADO: Select com o piso de capacidade colapsado (Basic no lugar
	// de Frontier) e filtro fail-open — a mesma escada REAL, a regra de capacidade
	// desligada. Executa-se (não é uma comparação estática de Tiers()[0]).
	allowAllTier := func(tiering.Tier) bool { return true }
	naive, ok := h.ladder.Select(
		tiering.Request{Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch},
		allowAllTier,
	)
	if !ok {
		t.Fatal("meta inválida: a selecção ingénua não resolveu nenhum tier")
	}
	// O ATAQUE PASSOU: a via ingénua serviria a tarefa Frontier com um tier INCAPAZ.
	if naive.Capability >= tiering.CapabilityFrontier {
		t.Fatalf("meta não-vácua falhou: a selecção ingénua resolveu um tier capaz (%s) — não prova a fuga", naive.Name)
	}
	// O router REAL, para a mesma tarefa Frontier, NUNCA escolhe o incapaz.
	real := mustRoute(t, h.router(), req(regEUWest, tiering.CapabilityFrontier, tiering.ClassBatch, 10))
	realCap := tierCapability(t, h, real.Tier)
	if realCap < tiering.CapabilityFrontier {
		t.Fatalf("o router escolheu um tier incapaz para Frontier (%s)", real.Tier)
	}
	// Prova diferencial: a via ingénua escolhe um tier ESTRITAMENTE menos capaz que o real.
	if naive.Capability >= realCap {
		t.Fatal("meta não-vácua falhou: a via ingénua não escolhe um tier menos capaz que o real")
	}
}

// META 4 — removendo o PISO de capacidade da degradação (Cheaper com filtro
// allow-all em vez de capableAllowlistFilter), a exaustão degradaria uma tarefa
// Frontier para um tier INCAPAZ (standard-fast). Prova que o cenário 3 detecta que a
// degradação nunca desce abaixo da capacidade exigida.
func TestMetaDetects_DegradationBelowCapability(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	// SEM piso: Cheaper a partir do Frontier mais barato desce para um Standard incapaz.
	bypassed, ok := h.ladder.Cheaper("frontier-batch", func(tiering.Tier) bool { return true })
	if !ok {
		t.Fatal("meta inválida: sem piso, Cheaper devia encontrar um degrau mais barato")
	}
	if bypassed.Capability >= tiering.CapabilityFrontier {
		t.Fatalf("meta não-vácua falhou: sem piso o degrau (%s) ainda é capaz — não prova a fuga", bypassed.Name)
	}
	// COM o piso (o router real): sob esgotamento a rota Frontier MANTÉM-SE capaz.
	h.setBudget(100, 100)
	real := mustRoute(t, h.router(), req(regEUWest, tiering.CapabilityFrontier, tiering.ClassBatch, 10))
	if tierCapability(t, h, real.Tier) < tiering.CapabilityFrontier {
		t.Fatalf("o router degradou Frontier abaixo da capacidade (%s) — o piso falhou", real.Tier)
	}
}

// META 4b — sem o admission (coordenação removida), a 2.ª chamada sob rate-limit que o
// router REAL ADIA é, em vez disso, DESPACHADA às cegas. Fecha a não-vacuidade do
// sub-caso "defer sob rate-limit" do cenário 3 ao nível dos outros metas: prova que é o
// admission a CAUSAR o adiamento (com ele o 2.º defere; sem ele despacha).
func TestMetaDetects_DispatchWhenAdmissionOffUnderRateLimit(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.adm.SetLimit(provider, modelStandard, regEUWest, 100, 1_000_000) // tecto 100 = custo de 1 chamada
	// Contraprova (router REAL, com admission): a 2.ª chamada ADIA.
	real := h.router()
	_ = mustRoute(t, real, req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 100))
	if second := mustRoute(t, real, req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 100)); second.Outcome != router.OutcomeDeferred {
		t.Fatalf("contraprova: com admission a 2.ª chamada devia ADIAR, obtive %s", second.Outcome)
	}
	// CONTROLO DESLIGADO: sem admission, ambas as chamadas são DESPACHADAS (o ataque passa).
	r := h.routerNoAdmission()
	first := mustRoute(t, r, req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 100))
	second := mustRoute(t, r, req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 100))
	if first.Outcome != router.OutcomeRouted || second.Outcome != router.OutcomeRouted {
		t.Fatalf("meta não-vácua falhou: sem admission esperava ambas DESPACHADAS, obtive 1ª=%s 2ª=%s", first.Outcome, second.Outcome)
	}
}

// META 4c — sem o admission, um custo que EXCEDE o próprio tecto (rejeição PERMANENTE
// fail-closed no router REAL, nunca admissível por refill) é, em vez disso, ADMITIDO.
// Fecha a não-vacuidade do sub-caso "rejeitar quando o custo excede o tecto" do
// cenário 3: prova que é o admission a REJEITAR (com ele rejeita; sem ele despacha).
func TestMetaDetects_AdmitWhenAdmissionOffCostExceedsCeiling(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.adm.SetLimit(provider, modelStandard, regEUWest, 50, 1_000_000) // tecto 50 < custo 100
	// Contraprova (router REAL, com admission): custo 100 > tecto 50 → REJECT permanente.
	if real := mustRoute(t, h.router(), req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 100)); real.Outcome != router.OutcomeRejected {
		t.Fatalf("contraprova: com admission o custo>tecto devia REJEITAR, obtive %s", real.Outcome)
	}
	// CONTROLO DESLIGADO: sem admission, o custo>tecto é DESPACHADO (o ataque passa).
	dec := mustRoute(t, h.routerNoAdmission(), req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 100))
	if dec.Outcome != router.OutcomeRouted {
		t.Fatalf("meta não-vácua falhou: sem admission o custo>tecto devia ser DESPACHADO, obtive %s", dec.Outcome)
	}
}

// META 5 — colapsando as FRONTEIRAS na guarda (us-east na mesma jurisdição que eu),
// o failover para us-east PASSA (cross-border). Prova que o cenário 4 detecta que é
// a fronteira que força a rejeição, não um acaso da lista.
func TestMetaDetects_FailoverCrossBorderWhenBoundaryCollapsed(t *testing.T) {
	t.Parallel()
	collapsed := sovereignty.NewGuard(
		sovereignty.WithBoundary(regEUWest, "global"),
		sovereignty.WithBoundary(regUSEast, "global"), // MESMA fronteira: controlo contornado
	)
	primary := ep("k-euw", regEUWest)
	down := func(e sovereignty.Endpoint) bool { return e != primary }
	d := collapsed.Route(regEUWest, primary, []sovereignty.Endpoint{ep("k-us", regUSEast)}, down)
	if d.Outcome != sovereignty.OutcomeFailover || d.Chosen.Region != regUSEast {
		t.Fatalf("meta não-vácua falhou: com fronteiras colapsadas esperava failover cross-border para %s, obtive %s(%v)", regUSEast, d.Outcome, d.Chosen)
	}
}

// META 6 — contornando a soberania NO ROUTER (guarda com fronteiras colapsadas +
// allowlist a permitir tudo), a rota resolve para us-east (cross-border). Prova que
// o cenário 5 detecta o bloqueio cross-border (com os controlos, é rejeitado).
func TestMetaDetects_CrossBorderRoutedWhenSovereigntyBypassed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	collapsed := sovereignty.NewGuard(
		sovereignty.WithBoundary(regEUWest, "global"),
		sovereignty.WithBoundary(regUSEast, "global"),
	)
	// Router com AMBOS os controlos de soberania contornados.
	r := router.New(h.ladder,
		router.WithGuard(collapsed),
		router.WithAllowlist(allowAll{}),
		router.WithLoadProvider(h.load),
		router.WithAdmission(h.adm),
		router.WithBudget(h.budget),
		router.WithKeyPool(h.keys),
	)
	dec := mustRoute(t, r, req(regEUWest, tiering.CapabilityFrontier, tiering.ClassInteractive, 10, ep("k-us", regUSEast)))
	if dec.Outcome == router.OutcomeRejected || dec.Region != regUSEast {
		t.Fatalf("meta não-vácua falhou: com a soberania contornada esperava rota cross-border para %s, obtive %s(region=%s)", regUSEast, dec.Outcome, dec.Region)
	}
}

// ===========================================================================
// RELATÓRIO da suite — emite uma linha marcada AOS_ROUTING_REPORT que o gate CI
// (scripts/ci/routing.sh) captura e sobre a qual falha-fecha (o campo agregado
// "pass" é o ÚLTIMO do objecto: …,"pass":true}, pelo que o gate ancora ao fim da
// linha). Se qualquer invariante ou detecção falhar, o agregado é false.
// ===========================================================================

func TestSuiteReportEmitted(t *testing.T) {
	checks := []struct {
		name string
		ok   bool
	}{
		{"least_loaded", probeLeastLoaded()},
		{"no_aggregate_collapse", probeNoAggregateCollapse()},
		{"tiering_cheapest_capable", probeTieringCheapestCapable()},
		{"interactive_vs_batch", probeInteractiveVsBatch()},
		{"degrade_to_cheaper_capable", probeDegradeToCheaperCapable()},
		{"exhausted_no_hardstop", probeExhaustedNoHardStop()},
		{"defer_under_ratelimit", probeDeferUnderRateLimit()},
		{"reject_permanent", probeRejectPermanent()},
		{"failover_intra", probeFailoverIntra()},
		{"reject_no_intra", probeRejectNoIntra()},
		{"crossborder_blocked", probeCrossBorderBlocked()},
		// Detecção (meta): com o controlo desligado, o ataque PASSA (não-vazio).
		{"detection_aggregate_collapse", probeDetectAggregateCollapse()},
		{"detection_crossborder_routed", probeDetectCrossBorderRouted()},
	}
	pass := true
	var b strings.Builder
	b.WriteString("AOS_ROUTING_REPORT {")
	for i, c := range checks {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("\"" + c.name + "\":" + boolStr(c.ok))
		if !c.ok {
			pass = false
		}
	}
	b.WriteString(",\"pass\":" + boolStr(pass) + "}")
	if !pass {
		t.Fatalf("relatório da suite indica falha: %s", b.String())
	}
	fmt.Println(b.String())
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// --- Probes (espelham as invariantes dos cenários, sem *testing.T, para o relatório).

func probeLeastLoaded() bool {
	h, err := newHarnessErr()
	if err != nil {
		return false
	}
	cands := []sovereignty.Endpoint{ep("k-euc", regEUCentral), ep("k-euw", regEUWest)}
	h.load.Set(provider, regEUCentral, router.Headroom{WorstUsed: 99, WorstLimit: 100})
	h.load.Set(provider, regEUWest, router.Headroom{WorstUsed: 1, WorstLimit: 100})
	dec := mustRouteP(h.router(), req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 10, cands...))
	return dec.Outcome == router.OutcomeRouted && dec.Region == regEUWest && dec.KeyID != ""
}

func probeNoAggregateCollapse() bool {
	h, err := newHarnessErr()
	if err != nil {
		return false
	}
	h.adm.SetLimit(provider, modelStandard, regEUCentral, 300, 1_000_000)
	granted, deferred := 0, 0
	for _, b := range []string{"board-eu", "board-fr", "board-de", "board-pt"} {
		rq := router.Request{Board: b, Tenant: b, Provider: provider, Region: regEUCentral,
			Capability: tiering.CapabilityStandard, Class: tiering.ClassBatch, EstimatedTokens: 100}
		dec, e := h.router().Route(context.Background(), rq)
		if e != nil {
			return false
		}
		switch dec.Outcome {
		case router.OutcomeRouted:
			granted++
		case router.OutcomeDeferred:
			deferred++
		}
	}
	return granted == 3 && deferred == 1
}

func probeTieringCheapestCapable() bool {
	h, err := newHarnessErr()
	if err != nil {
		return false
	}
	dec := mustRouteP(h.router(), req(regEUWest, tiering.CapabilityFrontier, tiering.ClassBatch, 10))
	tr, ok := h.ladder.TierOf(dec.Tier)
	return dec.Model == modelFrontBatch && ok && tr.Capability >= tiering.CapabilityFrontier
}

func probeInteractiveVsBatch() bool {
	h, err := newHarnessErr()
	if err != nil {
		return false
	}
	inter := mustRouteP(h.router(), req(regEUWest, tiering.CapabilityStandard, tiering.ClassInteractive, 10))
	batch := mustRouteP(h.router(), req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 10))
	return inter.Model == modelStdFast && batch.Model == modelStandard && inter.Model != batch.Model
}

func probeDegradeToCheaperCapable() bool {
	h, err := newHarnessErr()
	if err != nil {
		return false
	}
	h.setBudget(80, 100)
	dec := mustRouteP(h.router(), req(regEUWest, tiering.CapabilityFrontier, tiering.ClassInteractive, 10))
	tr, ok := h.ladder.TierOf(dec.Tier)
	return dec.Outcome == router.OutcomeDegraded && dec.Degraded && dec.Model == modelFrontBatch && ok && tr.Capability >= tiering.CapabilityFrontier
}

func probeExhaustedNoHardStop() bool {
	h, err := newHarnessErr()
	if err != nil {
		return false
	}
	h.setBudget(100, 100)
	dec := mustRouteP(h.router(), req(regEUWest, tiering.CapabilityFrontier, tiering.ClassBatch, 10))
	tr, ok := h.ladder.TierOf(dec.Tier)
	return dec.Outcome != router.OutcomeRejected && dec.BudgetExhausted && ok && tr.Capability >= tiering.CapabilityFrontier
}

func probeDeferUnderRateLimit() bool {
	h, err := newHarnessErr()
	if err != nil {
		return false
	}
	h.adm.SetLimit(provider, modelStandard, regEUWest, 100, 1_000_000)
	r := h.router()
	_ = mustRouteP(r, req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 100))
	second := mustRouteP(r, req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 100))
	return second.Outcome == router.OutcomeDeferred && second.RetryAfter > 0
}

func probeRejectPermanent() bool {
	h, err := newHarnessErr()
	if err != nil {
		return false
	}
	h.adm.SetLimit(provider, modelStandard, regEUWest, 50, 1_000_000)
	dec := mustRouteP(h.router(), req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 100))
	return dec.Outcome == router.OutcomeRejected
}

func probeFailoverIntra() bool {
	g := testGuard()
	primary := ep("k-euw", regEUWest)
	alt := ep("k-euc", regEUCentral)
	down := func(e sovereignty.Endpoint) bool { return e != primary }
	d := g.Route(regEUWest, primary, []sovereignty.Endpoint{primary, alt}, down)
	return d.Outcome == sovereignty.OutcomeFailover && d.Chosen == alt
}

func probeRejectNoIntra() bool {
	g := testGuard()
	primary := ep("k-euw", regEUWest)
	down := func(e sovereignty.Endpoint) bool { return e != primary }
	d := g.Route(regEUWest, primary, []sovereignty.Endpoint{ep("k-us", regUSEast)}, down)
	return d.Outcome == sovereignty.OutcomeReject && d.CrossBorderBlocked()
}

func probeCrossBorderBlocked() bool {
	h, err := newHarnessErr()
	if err != nil {
		return false
	}
	dec := mustRouteP(h.router(), req(regEUWest, tiering.CapabilityFrontier, tiering.ClassInteractive, 10, ep("k-us", regUSEast)))
	return dec.Outcome == router.OutcomeRejected && len(dec.Dropped) == 1 && dec.Dropped[0].Region == regUSEast
}

// probeDetectAggregateCollapse: com o admission removido, o 4.º board PASSA
// (colapso) — a detecção é não-vácua.
func probeDetectAggregateCollapse() bool {
	h, err := newHarnessErr()
	if err != nil {
		return false
	}
	h.adm.SetLimit(provider, modelStandard, regEUCentral, 300, 1_000_000)
	r := h.routerNoAdmission()
	granted := 0
	for _, b := range []string{"board-eu", "board-fr", "board-de", "board-pt"} {
		rq := router.Request{Board: b, Tenant: b, Provider: provider, Region: regEUCentral,
			Capability: tiering.CapabilityStandard, Class: tiering.ClassBatch, EstimatedTokens: 100}
		dec, e := r.Route(context.Background(), rq)
		if e != nil {
			return false
		}
		if dec.Outcome == router.OutcomeRouted {
			granted++
		}
	}
	return granted == 4 // colapso: mais do que o tecto permitia
}

// probeDetectCrossBorderRouted: com a soberania contornada, a rota resolve para
// us-east — a detecção do bloqueio cross-border é não-vácua.
func probeDetectCrossBorderRouted() bool {
	h, err := newHarnessErr()
	if err != nil {
		return false
	}
	collapsed := sovereignty.NewGuard(
		sovereignty.WithBoundary(regEUWest, "global"),
		sovereignty.WithBoundary(regUSEast, "global"),
	)
	r := router.New(h.ladder,
		router.WithGuard(collapsed),
		router.WithAllowlist(allowAll{}),
		router.WithLoadProvider(h.load),
		router.WithAdmission(h.adm),
		router.WithBudget(h.budget),
		router.WithKeyPool(h.keys),
	)
	dec := mustRouteP(r, req(regEUWest, tiering.CapabilityFrontier, tiering.ClassInteractive, 10, ep("k-us", regUSEast)))
	return dec.Outcome != router.OutcomeRejected && dec.Region == regUSEast
}

// mustRouteP corre o router para as probes (sem *testing.T): devolve a decisão; um
// erro real do admission devolve uma decisão vazia (a probe falha a asserção).
func mustRouteP(r *router.Router, rq router.Request) router.Decision {
	dec, err := r.Route(context.Background(), rq)
	if err != nil {
		return router.Decision{}
	}
	return dec
}
