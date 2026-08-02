package planapproval

import (
	"sort"

	approvalcard "github.com/aos-ref/control-plane/governance/approval-card"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// NodeReview modela o estado de TRIAGEM de um nó no [PlanCard]: se a sua revisão é
// FORÇADA (item-a-item, não colapsável) e porquê. Um nó é forçado sse Class >= gray
// (gray/danger) OU é um capability_gap; os restantes são COLAPSÁVEIS (Collapsible ==
// !Forced). É o modelo que a superfície de edição usa para decidir o que expandir por
// omissão e o que o humano TEM de rever — e que o gate IMPÕE: aprovar sem rever um nó
// forçado é recusado fail-closed (via [WithForcedReview]).
type NodeReview struct {
	// TaskID é o nó a que esta triagem se refere (na ordem topológica do card).
	TaskID string `json:"task_id"`
	// Forced indica que a revisão é item-a-item (não colapsável): Class >= gray ou capability_gap.
	Forced bool `json:"forced"`
	// CapabilityGap ecoa o gap de capacidade do nó (uma das razões de Forced).
	CapabilityGap bool `json:"capability_gap"`
}

// Collapsible é o complemento de Forced: um nó safe sem capability_gap é colapsável na
// apresentação (o humano não tem de o expandir/rever item-a-item).
func (r NodeReview) Collapsible() bool { return !r.Forced }

// forcedReview indica se um nó EXIGE revisão item-a-item (não é colapsável): Class >= gray
// (gray/danger) OU um capability_gap. Fail-closed: a severidade decide — safe sem gap é o
// único caso colapsável (a classe-zero danger é sempre forçada).
func forcedReview(n PlanNode) bool {
	return classSeverity(n.Class) >= classSeverity(risk.ClassGray) || n.CapabilityGap
}

// buildNodeReviews computa a triagem por-nó na ORDEM dada (a ordem topológica do card).
func buildNodeReviews(order []string, byID map[string]PlanNode) []NodeReview {
	reviews := make([]NodeReview, 0, len(order))
	for _, id := range order {
		n := byID[id]
		reviews = append(reviews, NodeReview{
			TaskID:        id,
			Forced:        forcedReview(n),
			CapabilityGap: n.CapabilityGap,
		})
	}
	return reviews
}

// ForcedTaskIDs devolve os task_ids cuja revisão é FORÇADA (Class >= gray ou
// capability_gap), na ordem topológica do card. É o conjunto que o gate exige que a
// decisão evidencie ter revisto (via [PlanDecision.ReviewedNodes]) quando a imposição de
// revisão forçada está ligada — senão a aprovação é recusada fail-closed.
func (c PlanCard) ForcedTaskIDs() []string {
	var out []string
	for _, r := range c.NodeReviews {
		if r.Forced {
			out = append(out, r.TaskID)
		}
	}
	return out
}

// DangerEffectCards devolve os cards INDIVIDUAIS por-efeito-concreto dos nós danger
// (composição de AOS-120): cada nó danger força um card PRÓPRIO (nunca um lote), redigido
// pelo efeito resolvido do nó. Um nó danger IRREVERSÍVEL traz DualControlRequired — o
// [approvalcard.DualControlCollector] impõe os dois aprovadores DISTINTOS por-efeito, sem
// qualquer round-trip ao LLM. Preserva a ordem topológica do card.
func (c PlanCard) DangerEffectCards() []approvalcard.ApprovalCard {
	out := make([]approvalcard.ApprovalCard, 0, len(c.NodeCards))
	for i := range c.NodeCards {
		if c.NodeCards[i].Class == risk.ClassDanger {
			out = append(out, c.NodeCards[i])
		}
	}
	return out
}

// PlanDiff é o DIFF ESTRUTURAL de uma edição do plano (antes→depois) ao nível dos NÓS e
// das ARESTAS: o que foi acrescentado/removido. É o registo mínimo, determinista e sem
// segredos que acompanha uma decisão de edição — o humano/audit vê EXACTAMENTE o que a
// revisão mudou no grafo (não um "editou" opaco). As fatias são ordenadas para um diff
// reproduzível.
type PlanDiff struct {
	// AddedNodes/RemovedNodes são os task_ids acrescentados/removidos (ordenados).
	AddedNodes   []string `json:"added_nodes,omitempty"`
	RemovedNodes []string `json:"removed_nodes,omitempty"`
	// AddedEdges/RemovedEdges são as arestas From→To acrescentadas/removidas (ordenadas).
	AddedEdges   [][2]string `json:"added_edges,omitempty"`
	RemovedEdges [][2]string `json:"removed_edges,omitempty"`
}

// Empty indica que o diff não regista nenhuma mudança estrutural (nós e arestas iguais).
func (d PlanDiff) Empty() bool {
	return len(d.AddedNodes) == 0 && len(d.RemovedNodes) == 0 &&
		len(d.AddedEdges) == 0 && len(d.RemovedEdges) == 0
}

// DiffPlans computa o diff estrutural before→after ao nível dos nós (por task_id) e das
// arestas (por par ordenado From→To). Determinista: as diferenças são ordenadas. Ignora
// reordenações puras de prioridade que não mudem o conjunto de nós/arestas (o diff é de
// TOPOLOGIA, não de atributos de apresentação).
func DiffPlans(before, after Plan) PlanDiff {
	beforeNodes := nodeIDSet(before.Nodes)
	afterNodes := nodeIDSet(after.Nodes)
	beforeEdges := edgeSet(before.Edges)
	afterEdges := edgeSet(after.Edges)

	d := PlanDiff{
		AddedNodes:   missingKeys(afterNodes, beforeNodes),
		RemovedNodes: missingKeys(beforeNodes, afterNodes),
		AddedEdges:   missingEdges(after.Edges, beforeEdges),
		RemovedEdges: missingEdges(before.Edges, afterEdges),
	}
	return d
}

func nodeIDSet(nodes []PlanNode) map[string]bool {
	s := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		s[n.TaskID] = true
	}
	return s
}

func edgeSet(edges [][2]string) map[[2]string]bool {
	s := make(map[[2]string]bool, len(edges))
	for _, e := range edges {
		s[e] = true
	}
	return s
}

// missingKeys devolve, ordenadas, as chaves de have que faltam em other.
func missingKeys(have, other map[string]bool) []string {
	var out []string
	for k := range have {
		if !other[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// missingEdges devolve, na ordem estável (From, To), as arestas de src ausentes de other,
// sem duplicados.
func missingEdges(src [][2]string, other map[[2]string]bool) [][2]string {
	seen := make(map[[2]string]bool, len(src))
	var out [][2]string
	for _, e := range src {
		if other[e] || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}
