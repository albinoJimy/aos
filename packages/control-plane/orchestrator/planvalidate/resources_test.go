package planvalidate

import (
	"testing"

	"github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// fixedPricer é um [Pricer] determinístico para os testes: devolve o custo tabelado
// por node_id (ou zero). Puro — não lê relógio/rand. Prova que o custo é RE-PREÇADO
// da tabela, não ecoado do documento.
type fixedPricer struct {
	byNode map[string]budget.Amount
}

func (p fixedPricer) Price(node plan.Node, _ plan.PlannerMeta) budget.Amount {
	return p.byNode[node.NodeID]
}

// dangerCap é uma capability PINADA com efeito IRREVERSÍVEL (o eixo que força
// `danger` no classificador, independentemente do egress/sensibilidade).
func dangerCap(name string) Capability {
	return Capability{
		Name: name, Version: "1.0.0", Digest: "sha256:" + name, Admissible: true,
		Sensitivity:   risk.SensitivityInternal,
		Egress:        risk.EgressNone,
		Reversibility: risk.Irreversible,
	}
}

// safeCap é uma capability PINADA local, reversível, sem egress ⇒ deriva `safe`.
func safeCap(name string) Capability {
	return Capability{
		Name: name, Version: "1.0.0", Digest: "sha256:" + name, Admissible: true,
		Sensitivity:   risk.SensitivityPublic,
		Egress:        risk.EgressNone,
		Reversibility: risk.Reversible,
	}
}

// exfilCap é uma capability PINADA com egress EXTERNO de dados SENSÍVEIS
// (reversível) ⇒ deriva `danger` pelo vector de exfiltração, sem ser irreversível.
func exfilCap(name string) Capability {
	return Capability{
		Name: name, Version: "1.0.0", Digest: "sha256:" + name, Admissible: true,
		Sensitivity:   risk.SensitivitySensitive,
		Egress:        risk.EgressExternal,
		Reversibility: risk.Reversible,
	}
}

// zeroCostPolicy é uma [ResourcePolicy] que aceita qualquer custo (tectos e raiz
// generosos, tolerância larga), para isolar os testes de RISCO das guardas de
// orçamento.
func zeroCostPolicy() ResourcePolicy {
	return ResourcePolicy{
		Budget: BudgetPolicy{
			Pricer:        fixedPricer{byNode: map[string]budget.Amount{}},
			RootRemaining: budget.Amount{Tokens: 1 << 40, CostMicroUSD: 1 << 40},
			Tolerance:     budget.Amount{Tokens: 1 << 40, CostMicroUSD: 1 << 40},
		},
		Risk: nil, // DefaultPolicy
	}
}

// TestDowngradeDeRiskClassEIgnorado — DoD central: um documento que DECLARA "safe"
// num nó cujas tools pinadas DERIVAM "danger" resolve para `danger` — o rótulo do
// LLM é IGNORADO no sentido do downgrade (o piso derivado vence).
//
// FALHA-ANTES: se a resolução lesse o rótulo do documento (ou fizesse min em vez de
// «só eleva»), Resolved seria `safe` e o nó ficaria auto-aprovável — exactamente o
// caminho que o DoD proíbe.
func TestDowngradeDeRiskClassEIgnorado(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{
			NodeID: "irr", Role: "r", Objective: "o",
			Tools:     []plan.ToolRef{{Name: "delete", Version: "1.0.0", Digest: "sha256:delete"}},
			RiskClass: plan.RiskSafe, // o LLM MENTE: declara safe
		},
	}
	snap := Snapshot{Hash: capHash, Tools: []Capability{dangerCap("delete")}}

	res, v := ValidateResources(doc, snap, zeroCostPolicy())
	if v.Rejected() {
		t.Fatalf("regra 6 não rejeita (resolve); veio rejeição: %+v", v)
	}
	nr := res.Nodes["irr"]
	if nr.Derived != plan.RiskDanger {
		t.Fatalf("piso derivado devia ser danger (tool irreversível), veio %q", nr.Derived)
	}
	if nr.Declared != plan.RiskSafe {
		t.Fatalf("declarado devia preservar o rótulo do LLM (safe) para audit, veio %q", nr.Declared)
	}
	if nr.Resolved != plan.RiskDanger {
		t.Fatalf("DOWNGRADE NÃO IGNORADO: resolved=%q (esperado danger — o piso vence)", nr.Resolved)
	}
	if nr.AutoApprovable() {
		t.Fatalf("nó danger NUNCA pode ser auto-aprovável")
	}
}

// TestRiskClassSoEleva — o lado simétrico: um rótulo do LLM ACIMA do piso É aceite
// (só eleva). Tools derivam `safe`; o LLM declara `gray` ⇒ resolved=gray.
//
// FALHA-ANTES: se a resolução IGNORASSE sempre o rótulo (usasse só o piso), um nó
// que o modelo marcou como mais arriscado seria rebaixado a safe — perder-se-ia o
// sinal de elevação legítimo.
func TestRiskClassSoEleva(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{
			NodeID: "s", Role: "r", Objective: "o",
			Tools:     []plan.ToolRef{{Name: "read", Version: "1.0.0", Digest: "sha256:read"}},
			RiskClass: plan.RiskGray, // eleva acima do piso safe
		},
	}
	snap := Snapshot{Hash: capHash, Tools: []Capability{safeCap("read")}}

	res, v := ValidateResources(doc, snap, zeroCostPolicy())
	if v.Rejected() {
		t.Fatalf("inesperado: %+v", v)
	}
	nr := res.Nodes["s"]
	if nr.Derived != plan.RiskSafe {
		t.Fatalf("piso devia ser safe, veio %q", nr.Derived)
	}
	if nr.Resolved != plan.RiskGray {
		t.Fatalf("rótulo acima do piso devia ELEVAR: resolved=%q (esperado gray)", nr.Resolved)
	}
}

// TestNoIrreversivelClassificadoDanger — um nó com uma tool IRREVERSÍVEL classifica
// `danger` mesmo com o rótulo ausente (RiskUnset). Prova a derivação pura pelo eixo
// de reversibilidade.
//
// FALHA-ANTES: se o eixo de reversibilidade da capability não fosse consumido, o nó
// derivaria safe/gray e um efeito irreversível passaria sem approval-card.
func TestNoIrreversivelClassificadoDanger(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "send", Role: "r", Objective: "o",
			Tools: []plan.ToolRef{{Name: "send", Version: "1.0.0", Digest: "sha256:send"}}},
	}
	snap := Snapshot{Hash: capHash, Tools: []Capability{dangerCap("send")}}

	res, _ := ValidateResources(doc, snap, zeroCostPolicy())
	nr := res.Nodes["send"]
	if nr.Derived != plan.RiskDanger || nr.Resolved != plan.RiskDanger {
		t.Fatalf("tool irreversível devia derivar danger, veio derived=%q resolved=%q", nr.Derived, nr.Resolved)
	}
	if !nr.Classification.Irreversible() {
		t.Fatalf("classificação devia marcar irreversível")
	}
}

// TestEgressExternoSensivelDanger — o vector de EXFILTRAÇÃO (egress externo de dados
// sensíveis, reversível) também deriva `danger`, sem depender da irreversibilidade.
func TestEgressExternoSensivelDanger(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "exfil", Role: "r", Objective: "o",
			Tools: []plan.ToolRef{{Name: "post", Version: "1.0.0", Digest: "sha256:post"}}},
	}
	snap := Snapshot{Hash: capHash, Tools: []Capability{exfilCap("post")}}

	res, _ := ValidateResources(doc, snap, zeroCostPolicy())
	if got := res.Nodes["exfil"].Resolved; got != plan.RiskDanger {
		t.Fatalf("egress externo sensível devia derivar danger, veio %q", got)
	}
}

// TestCapabilitySemEixosDerivaDanger — FAIL-CLOSED: uma capability PINADA sem eixos
// de risco explícitos (valores-zero) deriva `danger` — uma ferramenta por
// classificar trata-se como perigosa, nunca como segura.
//
// FALHA-ANTES: se a baseline/combinação partisse do pior-caso "seguro" mas os
// zeros da capability NÃO dominassem, uma tool não-classificada derivaria safe.
func TestCapabilitySemEixosDerivaDanger(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "unk", Role: "r", Objective: "o",
			Tools: []plan.ToolRef{{Name: "mystery", Version: "1.0.0", Digest: "sha256:mystery"}}},
	}
	// Capability sem Sensitivity/Egress/Reversibility (zeros ⇒ fail-closed).
	snap := Snapshot{Hash: capHash, Tools: []Capability{
		{Name: "mystery", Version: "1.0.0", Digest: "sha256:mystery", Admissible: true},
	}}

	res, _ := ValidateResources(doc, snap, zeroCostPolicy())
	if got := res.Nodes["unk"].Resolved; got != plan.RiskDanger {
		t.Fatalf("capability sem eixos devia derivar danger (fail-closed), veio %q", got)
	}
}

// TestNoSemToolsDerivaSafe — um nó SEM tools não tem efeito nem egress ⇒ deriva
// `safe`. Prova a baseline mínima (não fail-closed-danger espúrio para nós de puro
// raciocínio).
func TestNoSemToolsDerivaSafe(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{{NodeID: "think", Role: "r", Objective: "o"}}
	snap := Snapshot{Hash: capHash}

	res, _ := ValidateResources(doc, snap, zeroCostPolicy())
	if got := res.Nodes["think"].Resolved; got != plan.RiskSafe {
		t.Fatalf("nó sem tools devia derivar safe, veio %q", got)
	}
}

// TestOverrunPorRamoBloqueia — REGRA 5: um custo RE-PREÇADO por-nó que excede o TETO
// duro por-nó dispara o breaker (rejeição fail-closed), NÃO um overrun silencioso.
//
// FALHA-ANTES: se a regra 5 ecoasse o `budget_estimate` do documento (que aqui está
// DENTRO do tecto) em vez de re-preçar, o overrun passaria despercebido.
func TestOverrunPorRamoBloqueia(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "cheap", Role: "r", Objective: "o",
			// O documento DECLARA um custo baixo (mentira/optimismo do LLM).
			BudgetEstimate: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 100}},
	}
	snap := Snapshot{Hash: capHash}

	pol := zeroCostPolicy()
	// O pricer RE-PREÇA o nó bem acima do tecto por-nó.
	pol.Budget.Pricer = fixedPricer{byNode: map[string]budget.Amount{
		"cheap": {Tokens: 10, CostMicroUSD: 5_000_000},
	}}
	pol.Budget.NodeCeiling = budget.Amount{CostMicroUSD: 1_000_000} // teto: 1 USD
	// Tolerância larga para ISOLAR o teto da guarda de divergência.
	pol.Budget.Tolerance = budget.Amount{Tokens: 1 << 40, CostMicroUSD: 1 << 40}

	_, v := ValidateResources(doc, snap, pol)
	if !v.Rejected() {
		t.Fatalf("overrun por-ramo devia BLOQUEAR (breaker), veio aceite")
	}
	if v.Rule != plannerevents.RuleBudget || v.Reason != ReasonNodeCeilingExceeded {
		t.Fatalf("esperado RuleBudget/node_ceiling_exceeded, veio %s/%s", v.Rule, v.Reason)
	}
	if v.Locator.NodeID != "cheap" {
		t.Fatalf("locator devia apontar o nó ofensor, veio %q", v.Locator.NodeID)
	}
}

// TestDivergenciaDeCustoRejeita — REGRA 5: o custo re-preçado diverge do declarado
// acima da tolerância ⇒ REJEITA (política escolhida: rejeitar, não clamp). Prova
// que o custo é re-preçado e CONFERIDO, não ecoado.
func TestDivergenciaDeCustoRejeita(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "n", Role: "r", Objective: "o",
			BudgetEstimate: plan.BudgetEstimate{Tokens: 100, CostMicroUSD: 1_000}},
	}
	snap := Snapshot{Hash: capHash}

	pol := zeroCostPolicy()
	pol.Budget.Pricer = fixedPricer{byNode: map[string]budget.Amount{
		"n": {Tokens: 100, CostMicroUSD: 50_000}, // 50x o declarado
	}}
	pol.Budget.Tolerance = budget.Amount{Tokens: 0, CostMicroUSD: 500} // tolerância apertada

	_, v := ValidateResources(doc, snap, pol)
	if !v.Rejected() || v.Reason != ReasonBranchCostDivergence {
		t.Fatalf("esperado rejeição por branch_cost_divergence, veio %+v", v)
	}
}

// TestTotalExcedeRaizRejeita — REGRA 5: a SOMA re-preçada excede o orçamento raiz
// remanescente ⇒ rejeita. Prova que o total é a soma re-preçada, não o
// `budget_total` do documento (que aqui é zero).
func TestTotalExcedeRaizRejeita(t *testing.T) {
	doc := baseDoc()
	doc.BudgetTotal = plan.BudgetEstimate{} // documento declara total ZERO
	doc.Nodes = []plan.Node{
		{NodeID: "a", Role: "r", Objective: "o"},
		{NodeID: "b", Role: "r", Objective: "o"},
	}
	snap := Snapshot{Hash: capHash}

	pol := zeroCostPolicy()
	pol.Budget.Pricer = fixedPricer{byNode: map[string]budget.Amount{
		"a": {Tokens: 600, CostMicroUSD: 600},
		"b": {Tokens: 600, CostMicroUSD: 600}, // soma 1200 > raiz 1000
	}}
	pol.Budget.RootRemaining = budget.Amount{Tokens: 1000, CostMicroUSD: 1000}
	pol.Budget.Tolerance = budget.Amount{Tokens: 1 << 40, CostMicroUSD: 1 << 40}

	res, v := ValidateResources(doc, snap, pol)
	if !v.Rejected() || v.Reason != ReasonBudgetTotalExceeded {
		t.Fatalf("esperado rejeição por budget_total_exceeded, veio %+v", v)
	}
	if !res.RepricedTotal.IsZero() {
		t.Fatalf("numa rejeição a Resolution devia vir vazia, veio %+v", res)
	}
}

// TestPricerNilFailClosed — REGRA 5: uma política sem [Pricer] rejeita todo o plano
// (fail-closed) — sem re-preçar não há como validar o custo, e ecoar o do LLM seria
// a falha que a regra previne.
func TestPricerNilFailClosed(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{{NodeID: "n", Role: "r", Objective: "o"}}
	pol := zeroCostPolicy()
	pol.Budget.Pricer = nil

	_, v := ValidateResources(doc, Snapshot{Hash: capHash}, pol)
	if !v.Rejected() || v.Reason != ReasonNoPricer {
		t.Fatalf("Pricer nil devia rejeitar fail-closed (no_pricer), veio %+v", v)
	}
}

// TestTotalReprecadoNaoEcoado — o total RESOLVIDO é a soma re-preçada, INDEPENDENTE
// do `budget_total` do documento. FALHA-ANTES: se a Resolution ecoasse
// doc.BudgetTotal, o total viria 999 (o valor mentiroso) em vez de 300.
func TestTotalReprecadoNaoEcoado(t *testing.T) {
	doc := baseDoc()
	doc.BudgetTotal = plan.BudgetEstimate{Tokens: 999, CostMicroUSD: 999} // mentira
	doc.Nodes = []plan.Node{
		{NodeID: "a", Role: "r", Objective: "o"},
		{NodeID: "b", Role: "r", Objective: "o"},
	}
	pol := zeroCostPolicy()
	pol.Budget.Pricer = fixedPricer{byNode: map[string]budget.Amount{
		"a": {Tokens: 100, CostMicroUSD: 100},
		"b": {Tokens: 200, CostMicroUSD: 200},
	}}

	res, v := ValidateResources(doc, Snapshot{Hash: capHash}, pol)
	if v.Rejected() {
		t.Fatalf("inesperado: %+v", v)
	}
	want := budget.Amount{Tokens: 300, CostMicroUSD: 300}
	if res.RepricedTotal != want {
		t.Fatalf("total re-preçado devia ser %+v (soma), veio %+v (ecoou o documento?)", want, res.RepricedTotal)
	}
}

// TestDeclaredCostOverflowFailClosed — REGRA 5, guarda de divergência: um custo
// DECLARADO adversarial no documento (uint64 >= 2^63) tem de ser saturado antes de
// entrar na aritmética int64 da guarda. Com a saturação em MaxInt64 o declarado
// fica ORDENADO acima de qualquer re-preçado não-negativo ⇒ absDiff é um positivo
// enorme ⇒ diverge ⇒ REJEITA.
//
// FALHA-ANTES (não-vacuoso — provado contra a versão com `int64(u)` directo): um
// declarado de 2^63+42 embrulhava para o int64 NEGATIVO MinInt64+42; a seguir
// absDiff(repriced=42, MinInt64+42) calcula 42 − (MinInt64+42) = 2^63, que POR SUA
// VEZ transborda o int64 para um valor NEGATIVO. A comparação `negativo > tol` é
// falsa ⇒ a divergência não disparava e o nó adversarial era ACEITE com um custo
// re-preçado modesto. A saturação mantém ambos os operandos não-negativos e
// limitados, eliminando os dois overflows de forma estrutural.
func TestDeclaredCostOverflowFailClosed(t *testing.T) {
	doc := baseDoc()
	// Declarado 2^63 + 42: int64(u) directo embrulha para MinInt64+42; o absDiff a
	// seguir transborda de novo para negativo, colapsando a divergência aparente.
	const adversarial = uint64(1<<63) + 42
	doc.Nodes = []plan.Node{
		{NodeID: "ovf", Role: "r", Objective: "o",
			BudgetEstimate: plan.BudgetEstimate{Tokens: 42, CostMicroUSD: adversarial}},
	}
	snap := Snapshot{Hash: capHash}

	pol := zeroCostPolicy()
	// Re-preça o nó a um custo modesto — o «alvo» do embrulho adversarial.
	pol.Budget.Pricer = fixedPricer{byNode: map[string]budget.Amount{
		"ovf": {Tokens: 42, CostMicroUSD: 42},
	}}
	// Tolerância pequena: uma divergência real TEM de disparar.
	pol.Budget.Tolerance = budget.Amount{Tokens: 0, CostMicroUSD: 1_000}

	_, v := ValidateResources(doc, snap, pol)
	if !v.Rejected() || v.Reason != ReasonBranchCostDivergence {
		t.Fatalf("custo declarado adversarial (>=2^63) devia divergir fail-closed, veio %+v", v)
	}
}

// TestValidatePlanCorreRegras1a6 — o ponto de entrada composto rejeita nas regras
// 1–4 ANTES de resolver risco/orçamento. Um MAJOR incompatível (regra 1) rejeita e
// a Resolution vem vazia.
func TestValidatePlanCorreRegras1a6(t *testing.T) {
	doc := baseDoc()
	doc.PlanVersion = plan.PlanVersion{Major: plan.CurrentPlanVersion.Major + 1}
	snap := baseSnapshot()
	ceil := Ceilings{MaxNodes: 10, MaxDepth: 10, MaxFanout: 10}

	res, v := ValidatePlan(doc, snap, ceil, zeroCostPolicy())
	if !v.Rejected() || v.Rule != plannerevents.RuleSchema {
		t.Fatalf("regra 1 devia rejeitar antes das regras 5–6, veio %+v", v)
	}
	if res.Nodes != nil {
		t.Fatalf("Resolution devia vir vazia numa rejeição estrutural, veio %+v", res)
	}
}

// TestDeterminismoResolucao — mesmo input ⇒ mesma Resolution e mesmo veredicto
// (regras 5–6 são puras; não dependem de ordem de iteração de mapa).
func TestDeterminismoResolucao(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "a", Role: "r", Objective: "o",
			Tools: []plan.ToolRef{{Name: "delete", Version: "1.0.0", Digest: "sha256:delete"}}},
		{NodeID: "b", Role: "r", Objective: "o",
			Tools: []plan.ToolRef{{Name: "read", Version: "1.0.0", Digest: "sha256:read"}}},
	}
	snap := Snapshot{Hash: capHash, Tools: []Capability{dangerCap("delete"), safeCap("read")}}
	pol := zeroCostPolicy()

	res0, v0 := ValidateResources(doc, snap, pol)
	for i := 0; i < 100; i++ {
		res, v := ValidateResources(doc, snap, pol)
		if v != v0 {
			t.Fatalf("veredicto não-determinístico na iteração %d", i)
		}
		if res.Nodes["a"] != res0.Nodes["a"] || res.Nodes["b"] != res0.Nodes["b"] {
			t.Fatalf("resolução não-determinística na iteração %d", i)
		}
	}
}
