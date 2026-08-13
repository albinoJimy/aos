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
	// Regra 1-ter — SEMÂNTICA DE SISTEMA DO VERIFICADOR (ADR-022 §2.2, AOS-271;
	// verifier.go). Sub-códigos PRÓPRIOS: o feedback ao re-planeamento tem de dizer
	// QUAL das quatro propriedades de §2.2 o plano violou — «rejeitado» não se corrige.
	//
	// ReasonVerdictNotFromVerifier — o ramo consome o `verdict` de um nó que NÃO
	// declara o papel reservado [plan.RoleVerifier]. É a forma mais directa da
	// auto-certificação: o produtor do trabalho a assinar o seu próprio pass.
	ReasonVerdictNotFromVerifier Reason = "verdict_not_from_verifier"
	// ReasonVerifierWithoutSubject — o verificador cujo veredicto o ramo consome não
	// tem arestas de entrada: não observa nó nenhum, logo o seu veredicto não tem
	// SUJEITO (um pass/fail sobre nada).
	ReasonVerifierWithoutSubject Reason = "verifier_without_subject"
	// ReasonVerifierSelfSubtree — «produtor ≠ verificador»: um produtor do consumidor
	// está na SUB-ÁRVORE DE DELEGAÇÃO do verificador (é alcançável a partir dele),
	// pelo que o veredicto certifica trabalho que o próprio verificador comissionou.
	ReasonVerifierSelfSubtree Reason = "verifier_self_subtree"
	// ReasonVerifierEffectTool — «read-only por construção»: um nó verificador pina
	// uma tool DE EFEITO (egress ≠ none ou irreversível — ver [IsEffectTool]).
	ReasonVerifierEffectTool Reason = "verifier_effect_tool"
	// ReasonVerifierCommissionsWork — (V4) algum nó declara o verificador em
	// `depends_on`: o verificador encabeçaria uma sub-árvore de delegação (o «spawn»
	// que §2.2 nomeia como efeito) e julgaria trabalho que ele próprio comissionou.
	// Quem consome o VEREDICTO usa `conditional_on`, não `depends_on`.
	ReasonVerifierCommissionsWork Reason = "verifier_commissions_work"
	// ReasonVerifierProducesWork — (V5) um verificador declara um `output` de forma
	// ABERTA (summary/record/artifact): isso é trabalho, e quem produz trabalho é um
	// produtor. A saída de um verificador é o veredicto (forma fechada).
	ReasonVerifierProducesWork Reason = "verifier_produces_work"
	// ReasonVerifierNotObservingWork — (V6) o verificador cujo veredicto liberta o
	// consumidor NÃO é descendente de algum dos outros produtores desse consumidor:
	// assina o pass de trabalho que nunca observou.
	ReasonVerifierNotObservingWork Reason = "verifier_not_observing_work"

	// Regra 1-quater — CONTRATOS TIPADOS DE PAYLOAD (ADR-022 §2.3, AOS-272;
	// payload.go). Sub-códigos PRÓPRIOS pela mesma razão de sempre: as três rejeições
	// que o ADR nomeia corrigem-se de maneiras diferentes, e «rejeitado» não se corrige.
	//
	// ReasonConsumesUnknownEdge — o `consumes` nomeia uma origem que NÃO é aresta de
	// entrada declarada do consumidor. Uma aresta de dados sem aresta de execução por
	// baixo é uma leitura em corrida com o produtor — e um canal de aresta invisível
	// ao DAG de admissão.
	ReasonConsumesUnknownEdge Reason = "consumes_unknown_edge"
	// ReasonConsumesUnknownOutput — a origem não declara nenhum output com o nome
	// pedido (a rejeição literal «consome um output inexistente»).
	ReasonConsumesUnknownOutput Reason = "consumes_unknown_output"
	// ReasonConsumesTypeMismatch — o tipo esperado pelo consumidor difere do declarado
	// pelo produtor. A compatibilidade é IDENTIDADE: sem subtipagem nem coerção.
	ReasonConsumesTypeMismatch Reason = "consumes_type_mismatch"
	// ReasonConsumesTaintAuthority — um payload de taint EFECTIVO `untrusted` alimenta
	// um consumidor com AUTORIDADE PRIVILEGIADA (ADR-005: untrusted não autoriza
	// elevação). É a barreira P0 do TaintGate aplicada na admissão, antes do spawn.
	ReasonConsumesTaintAuthority Reason = "consumes_taint_authority"

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
