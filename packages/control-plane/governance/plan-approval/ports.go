package planapproval

import (
	"context"
	"sort"

	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// PlanNode é um nó-tarefa do GRAFO proposto, na representação local sobre a qual o gate
// opera. É a PORTA que desacopla o gate do [orchestrator.NodeSpec] em construção (o
// orchestrator.DAG mapeia para isto no wiring, documentado — não se importa o
// orquestrador). Carrega o que o gate precisa para (a) formar o par (agente, domínio)
// da consulta de autonomia, e (b) construir o [approvalcard.ApprovalCard] por-nó (o
// efeito concreto, já redigido). SEM segredos: o Preview é o efeito resolvido, nunca o
// input da tool.
type PlanNode struct {
	// TaskID identifica o nó dentro do run. É a CHAVE ESTÁVEL de desempate na
	// ordenação topológica (nunca a ordem de um mapa Go).
	TaskID string
	// Priority é a prioridade do nó (maior = mais prioritário). Reservado para a
	// reordenação por edição (o orquestrador reconstrói o DAG com NodeSpec.Priority).
	Priority int
	// Agent é a identidade não-humana do nó. Vazio ⇒ herda o [Plan.Agent].
	Agent string
	// Domain é o domínio de autonomia do nó (ex.: "fs"/"http"/"mail"). Vazio ⇒ herda o
	// [Plan.Domain]. Deriva-se de [autonomy.DomainOf] no wiring.
	Domain string
	// Class é a classe de risco do nó (LIDA da classificação — o gate não reclassifica).
	Class risk.Class
	// Irreversible marca uma acção não-desfazível do nó (motiva o dual-control no card).
	Irreversible bool
	// Preview é o efeito CONCRETO resolvido do nó, JÁ redigido. Nunca o input da tool.
	Preview string
	// Capability e Resource identificam a acção resolvida do nó.
	Capability string
	Resource   string
}

// Plan é a representação local do GRAFO DE TAREFAS proposto pelo orquestrador — o
// "plano" reproduzível sobre o qual o gate de aprovação opera (AC3: multi-nó + arestas,
// distinto de uma tool call individual). O [orchestrator.DAG] mapeia para ele no
// wiring; a EDIÇÃO devolve um [Plan] revisto que o orquestrador RECONSTRÓI num novo DAG
// (não há poda in-place — a mutação do DAG é unexported).
type Plan struct {
	// RunID liga o plano ao run (stream_id). É a chave que o [SpawnGuard] regista como
	// aprovado e o span [OpPlanApproval] carimba (AttrRunID).
	RunID string
	// Agent é o agente-raiz do plano (o solicitante — base do 4-eyes) e o omissão dos
	// nós sem Agent próprio. Forma o par (agente, domínio) da consulta de autonomia.
	Agent string
	// Domain é o domínio de autonomia do plano (omissão dos nós sem Domain próprio).
	Domain string
	// Nodes são os nós-tarefa do grafo.
	Nodes []PlanNode
	// Edges são as arestas de dependência From→To (To depende de From; From precede To).
	Edges [][2]string
}

// Spawner é a fronteira PRE-SPAWN: cria o(s) sub-agente(s) de um run (onde o custo de
// tokens começa). É uma PORTA local — o [scheduler.SubtreeSpawner]/[orchestrator.Delegator]
// adapta-se a ela no wiring (não se importa o scheduler). O gate NUNCA a invoca; é o
// [SpawnGuard] (que a envolve) a recusar um Spawn de run não-aprovado, provando o AC1.
type Spawner interface {
	Spawn(ctx context.Context, runID string) error
}

// Validate impõe as invariantes fail-closed do plano/grafo (AC2/AC3): RunID e Agent
// não-vazios, cada nó com task_id não-vazio e ÚNICO, e cada aresta a referenciar nós
// existentes (From != To). Não computa a topologia — [Plan.TopoOrder] fá-lo e rejeita
// ciclos.
func (p Plan) Validate() error {
	if p.RunID == "" || p.Agent == "" {
		return ErrInvalidPlan
	}
	seen := make(map[string]bool, len(p.Nodes))
	for _, n := range p.Nodes {
		if n.TaskID == "" {
			return ErrInvalidPlan
		}
		if seen[n.TaskID] {
			return ErrInvalidPlan
		}
		seen[n.TaskID] = true
	}
	for _, e := range p.Edges {
		if e[0] == e[1] {
			return ErrInvalidPlan // auto-laço é sempre um ciclo
		}
		if !seen[e[0]] || !seen[e[1]] {
			return ErrInvalidPlan
		}
	}
	return nil
}

// TopoOrder devolve uma ordenação topológica ESTÁVEL e REPRODUZÍVEL do grafo (Kahn com
// desempate por task_id lexicográfico — NUNCA a ordem de um mapa Go), espelhando a
// semântica do [orchestrator.DAG.TopoOrder] (ADR-010): para os mesmos nós/arestas o
// plano é IDÊNTICO. Devolve [ErrPlanCycle] se o grafo tiver um ciclo (fail-closed — um
// plano com ciclo não é aprovável). Assume [Plan.Validate] já passou.
func (p Plan) TopoOrder() ([]string, error) {
	indeg := make(map[string]int, len(p.Nodes))
	for _, n := range p.Nodes {
		if _, ok := indeg[n.TaskID]; !ok {
			indeg[n.TaskID] = 0
		}
	}
	succ := make(map[string][]string, len(p.Nodes))
	for _, e := range p.Edges {
		succ[e[0]] = append(succ[e[0]], e[1])
		indeg[e[1]]++
	}

	ready := make([]string, 0, len(p.Nodes))
	for id, deg := range indeg {
		if deg == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)

	order := make([]string, 0, len(p.Nodes))
	for len(ready) > 0 {
		u := ready[0]
		ready = ready[1:]
		order = append(order, u)
		succs := append([]string(nil), succ[u]...)
		sort.Strings(succs)
		for _, v := range succs {
			indeg[v]--
			if indeg[v] == 0 {
				ready = insertSorted(ready, v)
			}
		}
	}
	if len(order) != len(indeg) {
		return nil, ErrPlanCycle
	}
	return order, nil
}

// insertSorted insere v mantendo xs ordenado lexicograficamente (molde do DAG de
// AOS-025, para uma ordenação estável sem re-sort completo).
func insertSorted(xs []string, v string) []string {
	i := sort.SearchStrings(xs, v)
	xs = append(xs, "")
	copy(xs[i+1:], xs[i:])
	xs[i] = v
	return xs
}

// nodeAgentDomain devolve o par (agente, domínio) do nó, herdando os do plano quando o
// nó não os fixa. É o par que a consulta de autonomia usa por-nó (defesa-em-profundidade
// — o gate agrega ao nível do plano, mas o par por-nó fica disponível ao wiring).
func (p Plan) nodeAgentDomain(n PlanNode) (string, string) {
	agent := n.Agent
	if agent == "" {
		agent = p.Agent
	}
	domain := n.Domain
	if domain == "" {
		domain = p.Domain
	}
	return agent, domain
}

// aggregateClass devolve a classe de risco AGREGADA do plano: a MAIS SEVERA entre os
// nós (danger > gray > safe). Um plano sem nós agrega [risk.ClassSafe] (nada a gatar).
// FAIL-CLOSED: a severidade de danger domina — um único nó danger torna o plano danger,
// forçando o gate humano (a auto-aprovação a níveis altos respeita a invariante ADR-013
// via [autonomy.Oversight]).
func aggregateClass(nodes []PlanNode) risk.Class {
	worst := risk.ClassSafe
	for _, n := range nodes {
		if classSeverity(n.Class) > classSeverity(worst) {
			worst = n.Class
		}
	}
	return worst
}

// classSeverity ordena as classes por severidade para a agregação: danger(3) > gray(2)
// > safe(1). Espelha a semântica fail-closed de [risk.Class] (danger é o valor-zero, o
// pior caso).
func classSeverity(c risk.Class) int {
	switch c {
	case risk.ClassSafe:
		return 1
	case risk.ClassGray:
		return 2
	default: // risk.ClassDanger (valor-zero) + desconhecidas
		return 3
	}
}

// aggregateIrreversible indica se ALGUM nó do plano é irreversível.
func aggregateIrreversible(nodes []PlanNode) bool {
	for _, n := range nodes {
		if n.Irreversible {
			return true
		}
	}
	return false
}
