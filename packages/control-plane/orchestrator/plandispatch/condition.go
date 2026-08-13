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
// # A ATRIBUIÇÃO DO VEREDICTO É VERIFICADA AQUI (correcção da auditoria da wave)
//
// Para uma aresta que observa `verdict`, não basta que o veredicto EXISTA e diga
// `pass`: os seus SUJEITOS têm de COBRIR os produtores que esta aresta guarda — as
// outras arestas de entrada do nó. Sem isto, um verificador legítimo emitia `pass`
// sobre o nó X e o ramo libertava o trabalho do nó Y, com o log a mostrar uma
// atribuição que ninguém confrontava. A falha é fail-closed no sentido de ESPERAR
// (indeciso), nunca de podar: uma atribuição que não cobre o trabalho é um defeito de
// emissão, e podar por causa dele registaria um facto terminal sobre um erro alheio.
//
// Ordem de precedência: qualquer falso DECIDIDO ⇒ [branchNotTaken]; senão qualquer
// indeciso ⇒ [branchUndecided]; senão [branchTaken]. Pura.
func evalConditional(n Node, lookup func(nodeID string) (NodeResultRecord, bool)) branchEval {
	pending := false
	for _, e := range n.ConditionalOn {
		rec, ok := lookup(e.From)
		if !ok {
			pending = true
			continue
		}
		if observesVerdict(e.When) && !subjectsCoverGuarded(n, e.From, rec.Subjects) {
			pending = true
			continue
		}
		sat, decided := evalPredicates(e.When, rec)
		if !decided {
			pending = true
			continue
		}
		if !sat {
			return branchNotTaken
		}
	}
	if pending {
		return branchUndecided
	}
	return branchTaken
}

// observesVerdict indica se a conjunção contém algum predicado sobre `verdict` — o
// único observável cuja ATRIBUIÇÃO tem de ser confrontada (`terminal_state` e `metric`
// são propriedades do próprio nó de origem, não juízos sobre terceiros). Pura.
func observesVerdict(preds []plan.Predicate) bool {
	for _, p := range preds {
		if p.Subject == plan.SubjectVerdict {
			return true
		}
	}
	return false
}

// subjectsCoverGuarded indica se os sujeitos do veredicto observado cobrem TODOS os
// produtores que esta aresta condicional guarda: as outras arestas de entrada do nó,
// por qualquer dos dois canais, excluindo a própria origem do veredicto.
//
// Um nó guardado APENAS pelo veredicto (sem outras arestas de entrada) não tem
// trabalho a cobrir e passa por vacuidade — é a admissão que garante que um
// verificador tem sujeito ([planvalidate], regras V1-bis/V6), e duplicar aqui essa
// regra estrutural seria uma segunda fonte de verdade sobre a topologia. Pura, linear
// (as listas são curtas por tecto estrutural) e sem alocação.
func subjectsCoverGuarded(n Node, verdictFrom string, subjects []string) bool {
	for _, dep := range n.DependsOn {
		if dep == verdictFrom {
			continue
		}
		if !containsID(subjects, dep) {
			return false
		}
	}
	for _, ce := range n.ConditionalOn {
		if ce.From == verdictFrom {
			continue
		}
		if !containsID(subjects, ce.From) {
			return false
		}
	}
	return true
}

// containsID indica se id consta da lista. Linear e sem alocação: um mapa teria ordem
// de iteração própria e este pacote não itera mapas no caminho de decisão.
func containsID(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// evalPredicates avalia a CONJUNÇÃO PLANA de uma aresta e devolve (satisfeita,
// decidida). Um predicado DECIDIDO COMO FALSO fecha a conjunção imediatamente — um
// resultado registado não muda, pelo que nenhum predicado indeciso a poderá salvar.
// Se nenhum falsifica mas algum está indeciso, a conjunção fica INDECIDA.
//
// Uma lista vazia é impossível ([plan.Decode] recusa-a) e devolveria `(true,true)` por
// vacuidade — a defesa está a montante, no schema, e não aqui. Pura.
func evalPredicates(preds []plan.Predicate, rec NodeResultRecord) (bool, bool) {
	decided := true
	for _, p := range preds {
		sat, dec := evalPredicate(p, rec)
		if !dec {
			decided = false
			continue
		}
		if !sat {
			return false, true
		}
	}
	if !decided {
		return false, false
	}
	return true, true
}

// evalPredicate avalia UM predicado do subconjunto fechado contra o resultado
// registado e devolve (satisfeito, DECIDIDO). TOTAL: nunca erra, nunca entra em
// pânico.
//
// # A AUSÊNCIA É INDECISÃO, NÃO FALSIDADE (correcção da auditoria da wave)
//
// A versão anterior devolvia `false` para qualquer observável AUSENTE, e a
// justificação era plausível: «false significa não ramificar, e não despachar é a
// única disposição sem efeito». Estava errada num ponto que só se vê a jusante — no
// despachante, um predicado falso não é uma espera: é [branchNotTaken], que é
// TERMINAL, PODA a descendência inteira e fica REGISTADO como facto imutável
// (`plan.branch_decided`). Com um ramo de qualidade cuja origem nunca emite veredicto
// (o estado real do repositório enquanto o emissor não tiver chamador de produção),
// isso significava: plano ADMITIDO, metade do organigrama aprovado PODADA em silêncio,
// e a decisão apensa ao log como se alguém a tivesse tomado. É exactamente a poda
// silenciosa que o interruptor datado de AOS-270 existia para impedir e que AOS-271
// removeu antes de haver emissor.
//
// A disposição correcta de «não sei» é ESPERAR: o nó fica em `waiting_condition`,
// nenhum facto é apenso, nenhum orçamento é debitado, e o defeito é VISÍVEL (um nó que
// não avança) em vez de silencioso (um ramo que desapareceu). Um observável que não
// existe não se compara — nem sequer com `ne`: `verdict ne fail` sobre um nó SEM
// veredicto fica INDECIDO, não verdadeiro nem falso.
//
// Pura: sem I/O, sem relógio, sem iteração de mapas (a métrica é acedida por chave).
func evalPredicate(p plan.Predicate, rec NodeResultRecord) (bool, bool) {
	switch p.Subject {
	case plan.SubjectTerminalState:
		if rec.Terminal == TerminalUnset {
			return false, false
		}
		return compareSymbol(string(rec.Terminal), string(p.Enum), p.Op), true
	case plan.SubjectVerdict:
		if rec.Verdict == VerdictAbsent {
			return false, false
		}
		return compareSymbol(string(rec.Verdict), string(p.Enum), p.Op), true
	case plan.SubjectMetric:
		if p.Number == nil {
			// Impossível num documento aceite por [plan.Decode]; defesa-em-profundidade.
			// INDECIDO e não falso: um predicado malformado é um defeito, e um defeito
			// não deve produzir um facto terminal sobre o plano.
			return false, false
		}
		v, ok := rec.Metrics[p.Metric]
		if !ok {
			return false, false
		}
		return compareInt(v, *p.Number, p.Op), true
	default:
		// Observável fora do enum fechado: impossível a jusante de [plan.Decode];
		// indeciso em vez de um default permissivo (e em vez de um falso que podaria).
		return false, false
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
