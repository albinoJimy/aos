package planvalidate

import "github.com/aos-ref/control-plane/orchestrator/plannerevents"

// Reason é o sub-código ALLOWLISTED que refina a [Rule] violada. É content-free —
// um rótulo estável de enum FECHADO, nunca texto do documento. Existe para dar ao
// re-planeamento um sinal accionável sem vazar o conteúdo untrusted (ADR-005).
type Reason string

const (
	// ReasonNone — sem violação (veredicto OK) ou violação sem sub-código.
	ReasonNone Reason = ""

	// Regra 1 — invariantes semânticas.
	ReasonVersionIncompatible      Reason = "plan_version_incompatible"  // MAJOR != corrente
	ReasonCapabilitiesHashMismatch Reason = "capabilities_hash_mismatch" // snapshot != carimbo do plano
	ReasonDanglingDependency       Reason = "dangling_dependency"        // depends_on para node inexistente
	ReasonMalformedNode            Reason = "malformed_node"             // node rejeitado pelo DAG (defesa)
	ReasonInvalidNodeID            Reason = "invalid_node_id"            // node_id fora da grammar de identificador estrutural

	// Regra 1-bis — arestas condicionais (ADR-022 §2.1, AOS-270).
	ReasonDanglingConditional          Reason = "dangling_conditional"           // conditional_on.from para node inexistente
	ReasonConditionalShadowsDependency Reason = "conditional_shadows_dependency" // a mesma origem em depends_on E conditional_on
	// ReasonVerdictUnsupported — o plano ramifica sobre o observável `verdict` e a
	// SEMÂNTICA DE SISTEMA do veredicto (read-only, produtor ≠ verificador, schema
	// tipado) ainda não existe: é AOS-271. Ver [verdictSupported].
	ReasonVerdictUnsupported Reason = "verdict_unsupported"

	// Regra 2-bis — alcançabilidade dos ramos (ADR-022 §2.1).
	// ReasonUnreachableJunction — a ancestralidade do nó exige DOIS ramos
	// MUTUAMENTE EXCLUSIVOS sobre a MESMA origem, logo o nó é inalcançável em
	// qualquer execução. Ver [checkBranchReachability].
	ReasonUnreachableJunction Reason = "unreachable_junction"

	// Regra 2 — aciclicidade (reutiliza AOS-025).
	ReasonCycle Reason = "cycle"
	// ReasonConditionalCycle — o ciclo FECHA-SE numa aresta *condicional* (o «ciclo
	// disfarçado de condicional», ADR-022 §5). Sub-código distinto de [ReasonCycle]
	// de propósito: o feedback tem de dizer QUAL canal de aresta fechou o ciclo, e o
	// teste adversarial prova que foi a condicional — não uma coincidência.
	ReasonConditionalCycle Reason = "conditional_cycle"

	// Regra 3 — resolução de tools contra o snapshot pinado.
	ReasonToolUnknown        Reason = "tool_unknown"         // nome+versão ausentes do snapshot
	ReasonToolDigestMismatch Reason = "tool_digest_mismatch" // versão existe, digest não bate
	ReasonToolDeprecated     Reason = "tool_deprecated"      // capability retirada
	ReasonToolInadmissible   Reason = "tool_inadmissible"    // fora da allowlist do snapshot

	// Regra 4 — tectos estruturais PRÓPRIOS do plano (distintos de AOS-028).
	ReasonMaxNodesExceeded  Reason = "max_nodes_exceeded"
	ReasonMaxDepthExceeded  Reason = "max_depth_exceeded"
	ReasonMaxFanoutExceeded Reason = "max_fanout_exceeded"

	// Regra 5 — orçamento RE-PREÇADO com teto por nó (AOS-232).
	ReasonNoPricer             Reason = "no_pricer"              // política sem Pricer (fail-closed)
	ReasonBranchCostDivergence Reason = "branch_cost_divergence" // re-preçado vs declarado > tolerância
	ReasonNodeCeilingExceeded  Reason = "node_ceiling_exceeded"  // teto duro por-nó (breaker AOS-029)
	ReasonBudgetOverflow       Reason = "budget_overflow"        // soma dos custos transborda int64
	ReasonBudgetTotalExceeded  Reason = "budget_total_exceeded"  // total re-preçado > raiz remanescente
)

// ToolCoord são as coordenadas ESTRUTURAIS de uma tool ofensora (a referência
// pinada do plano). São identificadores/hashes — NÃO conteúdo cru — e por isso
// admissíveis no feedback allowlisted (espelham [plannerevents.MaterializedNode],
// que também carrega ids de tool).
type ToolCoord struct {
	Name    string
	Version string
	Digest  string
}

// Locator aponta, de forma ESTRUTURADA, para o sítio da violação: o node_id e
// (quando aplicável) as coordenadas da tool. O node_id é um IDENTIFICADOR ESTRUTURAL
// LIMITADO — a regra 1 ([validNodeID]) garante, ANTES de o propagar, que conforma a
// uma grammar de charset fechado e comprimento máximo; um id malformado é rejeitado
// com um Locator VAZIO, pelo que o valor untrusted nunca é ecoado. O Locator NUNCA
// carrega os campos de TEXTO LIVRE do documento (objectivos, papéis, prosa do
// modelo): esses permanecem no documento untrusted e nunca entram no veredicto.
// Todos os campos são comparáveis, pelo que [Verdict] é comparável por `==` (usado
// no teste de determinismo).
type Locator struct {
	NodeID string
	Tool   ToolCoord
}

// Verdict é o resultado ESTRUTURADO de uma passagem de validação. É determinístico
// e comparável por `==`. Quando OK é falso, Rule é a regra allowlisted violada
// ([plannerevents.Rule], reutilizada — sem tipos de evento novos), Reason o
// sub-código e Locator as coordenadas estruturais. Não há campo de "detalhe cru":
// os campos de TEXTO LIVRE do documento untrusted (objectivos, papéis, prosa do
// modelo) nunca entram no veredicto; o único dado derivado do documento que o
// Locator propaga é o node_id, um identificador estrutural LIMITADO pela regra 1
// ([validNodeID]) — nunca texto livre arbitrário (DoD).
type Verdict struct {
	OK      bool
	Rule    plannerevents.Rule
	Reason  Reason
	Locator Locator
}

// accepted é o veredicto de aceitação canónico.
var accepted = Verdict{OK: true}

// reject constrói um veredicto de rejeição estruturado.
func reject(rule plannerevents.Rule, reason Reason, loc Locator) Verdict {
	return Verdict{OK: false, Rule: rule, Reason: reason, Locator: loc}
}

// Rejected indica se o veredicto rejeitou a proposta.
func (v Verdict) Rejected() bool { return !v.OK }
