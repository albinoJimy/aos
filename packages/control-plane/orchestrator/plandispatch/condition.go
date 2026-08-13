package plandispatch

import "github.com/aos-ref/control-plane/orchestrator/plan"

// condition.go — A AVALIAÇÃO das arestas condicionais (ADR-022 §2.1, AOS-270).
//
// «Avaliada pelo despachante SEM ESTADO — nunca pelo LLM em runtime.» Este ficheiro
// é essa avaliação, e é deliberadamente TODO composto por funções PURAS: recebem a
// expressão (do documento) e o resultado REGISTADO (do log), devolvem um booleano.
// Sem context, sem portas, sem relógio, sem mapas iterados — logo sem ordem
// dependente de runtime. É o que permite afirmar «função pura do resultado
// registado» e prová-lo com uma tabela de testes, em vez de o declarar.

// branchEval é a disposição de uma conjunção de arestas condicionais. Enum fechado
// de TRÊS valores — e é a existência do terceiro que importa: «ainda não dá para
// decidir» NÃO é «falso». Confundir os dois podaria ramos que só estavam à espera
// da origem terminar.
type branchEval int

const (
	// branchUndecided — alguma origem ainda não tem resultado REGISTADO. O nó espera;
	// nada é debitado nem registado (a decisão ainda não existe).
	branchUndecided branchEval = iota
	// branchTaken — todas as condições satisfeitas: o ramo é tomado.
	branchTaken
	// branchNotTaken — pelo menos uma condição DECIDIDA como falsa: o ramo não é
	// tomado, DEFINITIVAMENTE (os resultados registados são imutáveis, logo a decisão
	// é estável). O regresso a este nó só pode vir de replan de subgrafo (AOS-239).
	branchNotTaken
)

// evalConditional avalia a CONJUNÇÃO das arestas condicionais de um nó contra os
// resultados registados, obtidos por `lookup` (ok=false ⇒ ainda não registado).
//
// Percorre SEMPRE todas as arestas — não faz curto-circuito — e só depois combina.
// A razão é determinismo, não estética: com curto-circuito, o resultado dependeria
// da ORDEM em que as origens terminassem (uma aresta falsa a seguir a uma indecisa
// daria «indeciso» hoje e «não tomado» amanhã). Percorrendo tudo, uma aresta
// DECIDIDA COMO FALSA vence sempre a indecisão — o que é sólido porque um resultado
// registado nunca muda: se um conjunto já falsifica a conjunção, nenhuma origem
// que ainda falte a poderá salvar.
//
// Ordem de precedência: qualquer falso ⇒ [branchNotTaken]; senão qualquer ausente
// ⇒ [branchUndecided]; senão [branchTaken]. Pura.
func evalConditional(edges []plan.ConditionalEdge, lookup func(nodeID string) (NodeResultRecord, bool)) branchEval {
	pending := false
	for _, e := range edges {
		rec, ok := lookup(e.From)
		if !ok {
			pending = true
			continue
		}
		if !evalPredicates(e.When, rec) {
			return branchNotTaken
		}
	}
	if pending {
		return branchUndecided
	}
	return branchTaken
}

// evalPredicates avalia a CONJUNÇÃO PLANA de uma aresta: todos os predicados têm de
// valer. Uma lista vazia é impossível ([plan.Decode] recusa-a) e devolveria `true`
// por vacuidade — pelo que a defesa está a montante, no schema, e não aqui. Pura.
func evalPredicates(preds []plan.Predicate, rec NodeResultRecord) bool {
	for _, p := range preds {
		if !evalPredicate(p, rec) {
			return false
		}
	}
	return true
}

// evalPredicate avalia UM predicado do subconjunto fechado contra o resultado
// registado. TOTAL e fail-closed: qualquer observável AUSENTE (estado terminal por
// registar, veredicto inexistente, métrica não declarada) devolve `false` — nunca
// erro, nunca `true` por omissão. A direcção do fail-closed é deliberada: `false`
// significa «não ramificar para aqui», e não despachar é a única disposição sem
// efeito.
//
// Note-se que a ausência falsifica mesmo com o operador `ne`: `verdict ne fail`
// sobre um nó SEM veredicto é FALSO, não verdadeiro. Um observável que não existe
// não se compara — a alternativa (tratar a ausência como «diferente de tudo») deixaria
// um plano ramificar com base em nada.
//
// Pura: sem I/O, sem relógio, sem iteração de mapas (a métrica é acedida por chave).
func evalPredicate(p plan.Predicate, rec NodeResultRecord) bool {
	switch p.Subject {
	case plan.SubjectTerminalState:
		if rec.Terminal == TerminalUnset {
			return false
		}
		return compareSymbol(string(rec.Terminal), string(p.Enum), p.Op)
	case plan.SubjectVerdict:
		if rec.Verdict == VerdictAbsent {
			return false
		}
		return compareSymbol(string(rec.Verdict), string(p.Enum), p.Op)
	case plan.SubjectMetric:
		if p.Number == nil {
			// Impossível num documento aceite por [plan.Decode]; defesa-em-profundidade.
			return false
		}
		v, ok := rec.Metrics[p.Metric]
		if !ok {
			return false
		}
		return compareInt(v, *p.Number, p.Op)
	default:
		// Observável fora do enum fechado: impossível a jusante de [plan.Decode];
		// fail-closed em vez de um default permissivo.
		return false
	}
}

// compareSymbol compara dois símbolos do alfabeto fechado. SÓ igualdade: os
// operadores de ordem não se definem sobre símbolos e são recusados pelo schema —
// se algum chegasse aqui, devolve `false` (fail-closed), nunca uma ordem inventada
// sobre bytes. Pura.
func compareSymbol(observed, operand string, op plan.ConditionOp) bool {
	switch op {
	case plan.OpEq:
		return observed == operand
	case plan.OpNe:
		return observed != operand
	default:
		return false
	}
}

// compareInt compara o valor observado com o operando na ordem total dos inteiros.
// Todos os seis operadores se aplicam. Pura.
func compareInt(observed, operand int64, op plan.ConditionOp) bool {
	switch op {
	case plan.OpEq:
		return observed == operand
	case plan.OpNe:
		return observed != operand
	case plan.OpLt:
		return observed < operand
	case plan.OpLte:
		return observed <= operand
	case plan.OpGt:
		return observed > operand
	case plan.OpGte:
		return observed >= operand
	default:
		return false
	}
}

// sourcesOf projecta as origens das arestas condicionais pela ORDEM DECLARADA —
// a mesma ordem de [plan.CanonicalConditional], para que o facto registado
// (`plan.branch_decided`) e o digest falem da mesma sequência. Pura.
func sourcesOf(edges []plan.ConditionalEdge) []string {
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, e.From)
	}
	return out
}
