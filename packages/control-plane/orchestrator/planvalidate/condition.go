package planvalidate

import (
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// condition.go — As duas regras de ADMISSÃO que faltavam às arestas condicionais de
// ADR-022 §2.1, ambas apanhadas pela auditoria adversarial da wave. Vivem AQUI, no
// validador puro, e não no despachante, por uma razão de fronteira: uma
// impossibilidade descoberta no DESPACHO chega demasiado tarde — o humano já
// aprovou o organigrama no gate (ADR-013) e o plano já consumiu planeamento.
//
// Ambas são PURAS e determinísticas (iteram slices, nunca mapas) e devolvem um
// [Verdict] com sub-código PRÓPRIO: o feedback ao re-planeamento tem de dizer o que
// está errado, não «rejeitado».

// NOTA HISTÓRICA — o interruptor datado do observável `verdict`. Enquanto AOS-271
// esteve aberto, este ficheiro recusava na ADMISSÃO qualquer ramo sobre `verdict`
// (constante `verdictSupported = false`): a gramática de AOS-270 aceitava o símbolo,
// mas nada impedia que o veredicto tivesse sido emitido pelo PRÓPRIO nó produtor —
// ramificação sobre auto-certificação, exactamente o que ADR-022 §2.2 proíbe.
// Recusar era fail-closed e loud, mas cego. AOS-271 substituiu o interruptor pelas
// regras REAIS de §2.2 (verifier.go: só um verificador emite veredicto; o verificador
// não certifica a sua própria sub-árvore de delegação; um verificador é read-only por
// construção). O `verdict` deixou de ser recusado em bloco — passou a ser ATRIBUÍDO.

// branchConstraint é UMA condição que a ancestralidade de um nó IMPÕE sobre uma
// origem. `From` é a origem observada; `When` é a conjunção declarada sobre ela.
// Comparável pela forma canónica (`key`), que é content-free e já é o material do
// digest de ramo.
type branchConstraint struct {
	From string
	Key  string
	When []plan.Predicate
}

// checkBranchReachability — REGRA 2-bis: recusa um nó cuja ancestralidade exija
// DOIS ramos MUTUAMENTE EXCLUSIVOS sobre a MESMA origem.
//
// # O PROBLEMA QUE ISTO RESOLVE (o «ponto de junção»)
//
// Um planeador — humano ou LLM — escreve naturalmente:
//
//	review → ok  (verdict/terminal_state eq X)
//	       → fix (verdict/terminal_state eq Y)
//	       publish depends_on [ok, fix]
//
// `publish` é a junção, e é o OBJECTIVO do plano. Só que a poda de um ramo não
// tomado propaga-se pela descendência por QUALQUER dos dois canais de aresta
// (`plandispatch.propagateNotTaken`) — e tem de o fazer, senão a descendência
// apodrecia em `waiting_deps`. Logo `publish` morre SEMPRE: uma das duas origens é
// obrigatoriamente podada. O plano é estruturalmente impossível.
//
// Sem esta regra, o validador ADMITIA-O (não há regra de alcançabilidade), o humano
// aprovava no gate um organigrama cuja metade é inalcançável sem sinal nenhum, e a
// impossibilidade só aparecia como contadores `NotTaken` numa passagem de despacho.
// Decidir isto na ADMISSÃO — e não no despacho — é a mesma disciplina que faz o
// validador recusar um ciclo em vez de o diferir para sempre.
//
// # O QUE DETECTA, E O QUE DELIBERADAMENTE NÃO DETECTA
//
// Detecta contradição sobre observáveis SIMBÓLICOS (`terminal_state`, `verdict`):
// `eq A` vs `eq B` com A≠B, e `eq A` vs `ne A`. É EXACTO — nunca acusa um plano
// satisfazível, porque um resultado registado é um único símbolo e não pode
// satisfazer as duas condições. NÃO faz raciocínio de INTERVALOS sobre `metric`
// (`gt 10` vs `lt 5` fica por detectar): seria aritmética de intervalos com risco
// de falso-positivo, e a direcção segura de um validador de admissão é recusar só o
// que é PROVADAMENTE morto. A junção correcta escreve-se duplicando a cauda por
// ramo — documentado em `tecnica/18` §3.3.1.
//
// # DETERMINISMO
//
// Corre em ordem topológica de Kahn (o mesmo primitivo de [maxDepth]), propagando
// conjuntos de restrições por listas ORDENADAS pela ordem de declaração — nunca por
// iteração de mapas. Assume aciclicidade (a regra 2 já passou), pelo que termina.
func checkBranchReachability(doc plan.PlanDocument) Verdict {
	if !anyConditionalNode(doc) {
		// Sem condições no plano não há ramos que se excluam: caminho pré-ADR-022
		// literalmente inalterado (zero alocações).
		return accepted
	}
	indeg := make(map[string]int, len(doc.Nodes))
	dependents := make(map[string][]string, len(doc.Nodes))
	byID := make(map[string]plan.Node, len(doc.Nodes))
	for _, n := range doc.Nodes {
		byID[n.NodeID] = n
		in := incomingEdges(n)
		indeg[n.NodeID] = len(in)
		for _, dep := range in {
			dependents[dep] = append(dependents[dep], n.NodeID)
		}
	}
	// inherited[node] = restrições herdadas (lista ordenada) + índice de dedupe.
	inherited := make(map[string][]branchConstraint, len(doc.Nodes))
	seen := make(map[string]map[string]bool, len(doc.Nodes))

	// own acrescenta as restrições PRÓPRIAS de um nó ao seu conjunto herdado e
	// devolve a primeira contradição encontrada (ou "" e ""), pela ordem declarada.
	add := func(nodeID string, c branchConstraint) (string, string) {
		if seen[nodeID] == nil {
			seen[nodeID] = make(map[string]bool)
		}
		k := c.From + "\x00" + c.Key
		if seen[nodeID][k] {
			return "", ""
		}
		for _, prev := range inherited[nodeID] {
			if prev.From == c.From && contradictory(prev.When, c.When) {
				return prev.Key, c.Key
			}
		}
		seen[nodeID][k] = true
		inherited[nodeID] = append(inherited[nodeID], c)
		return "", ""
	}

	queue := make([]string, 0, len(doc.Nodes))
	for _, n := range doc.Nodes {
		if indeg[n.NodeID] == 0 {
			queue = append(queue, n.NodeID)
		}
	}
	// Semeia as restrições próprias das raízes antes de relaxar (uma raiz não tem
	// condicionais — a origem teria de existir — mas a simetria mantém o código uno).
	for i := 0; i < len(queue); i++ {
		id := queue[i]
		n := byID[id]
		for _, ce := range n.ConditionalOn {
			if a, b := add(id, branchConstraint{From: ce.From, Key: plan.CanonicalConditional([]plan.ConditionalEdge{ce}), When: ce.When}); a != "" || b != "" {
				return reject(plannerevents.RuleSchema, ReasonUnreachableJunction, Locator{NodeID: id})
			}
		}
		for _, m := range dependents[id] {
			for _, c := range inherited[id] {
				if a, b := add(m, c); a != "" || b != "" {
					return reject(plannerevents.RuleSchema, ReasonUnreachableJunction, Locator{NodeID: m})
				}
			}
			indeg[m]--
			if indeg[m] == 0 {
				queue = append(queue, m)
			}
		}
	}
	return accepted
}

// anyConditionalNode indica se algum nó declara arestas condicionais.
func anyConditionalNode(doc plan.PlanDocument) bool {
	for _, n := range doc.Nodes {
		if len(n.ConditionalOn) > 0 {
			return true
		}
	}
	return false
}

// contradictory indica se duas conjunções sobre a MESMA origem não podem ser
// satisfeitas pelo mesmo resultado registado. SOM (nunca acusa o satisfazível):
// só conclui contradição quando dois predicados sobre o MESMO observável simbólico
// se excluem por construção. Puro.
func contradictory(a, b []plan.Predicate) bool {
	for _, pa := range a {
		if pa.Subject != plan.SubjectTerminalState && pa.Subject != plan.SubjectVerdict {
			continue
		}
		for _, pb := range b {
			if pb.Subject != pa.Subject {
				continue
			}
			switch {
			case pa.Op == plan.OpEq && pb.Op == plan.OpEq && pa.Enum != pb.Enum:
				// Um observável tem UM símbolo: não pode ser dois ao mesmo tempo.
				return true
			case pa.Op == plan.OpEq && pb.Op == plan.OpNe && pa.Enum == pb.Enum:
				return true
			case pa.Op == plan.OpNe && pb.Op == plan.OpEq && pa.Enum == pb.Enum:
				return true
			}
		}
	}
	return false
}
