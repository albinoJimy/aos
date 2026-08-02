package planapproval

import (
	"sort"

	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// RoleCapabilities é a AGRUPAÇÃO "tools por papel" (CA1): para um PAPEL (a identidade
// não-humana / NHI que age em um ou mais nós do plano), lista as TOOLS (capabilities)
// que esse papel exerce, os task_ids correspondentes e a classe mais severa entre eles.
// É a vista que apresenta o grafo por QUEM age e o QUE cada papel pode fazer — em vez de
// só uma lista plana de nós. Determinista (papéis, capabilities e task_ids ordenados) e
// sem segredos (só papel + capability + task_id + classe).
type RoleCapabilities struct {
	// Role é o papel/NHI (o [approvalcard.ApprovalCard.Requester] do(s) nó(s), que herda
	// o [Plan.Agent] quando o nó não fixa um agente próprio).
	Role string `json:"role"`
	// Capabilities são as tools DISTINTAS que o papel exerce no plano (ordenadas).
	Capabilities []string `json:"capabilities"`
	// TaskIDs são os nós que o papel executa, na ordem topológica do card.
	TaskIDs []string `json:"task_ids"`
	// WorstClass é a classe mais severa entre os nós do papel (danger > gray > safe) — o
	// piso de escrutínio do papel.
	WorstClass risk.Class `json:"worst_class"`
}

// RolesView agrupa os nós do plan-card por PAPEL (o Requester de cada card por-nó),
// devolvendo, por papel, as tools (capabilities) que exerce, os task_ids e a classe mais
// severa — a vista "papéis, tools por papel" da CA1. Deriva-se dos cards por-nó JÁ
// construídos (a ordem de [PlanCard.NodeCards] espelha [PlanCard.Order]); não reclassifica
// nem inventa. Determinista: os papéis são devolvidos por ordem lexicográfica, as
// capabilities de cada papel ordenadas e sem duplicados, e os task_ids na ordem
// topológica do card.
func (c PlanCard) RolesView() []RoleCapabilities {
	type acc struct {
		caps  map[string]bool
		tasks []string
		worst risk.Class
	}
	byRole := make(map[string]*acc)
	var roles []string
	for i := range c.NodeCards {
		role := c.NodeCards[i].Requester
		a, ok := byRole[role]
		if !ok {
			a = &acc{caps: make(map[string]bool), worst: risk.ClassSafe}
			byRole[role] = a
			roles = append(roles, role)
		}
		if capName := c.NodeCards[i].Capability; capName != "" {
			a.caps[capName] = true
		}
		if i < len(c.Order) {
			a.tasks = append(a.tasks, c.Order[i])
		}
		if classSeverity(c.NodeCards[i].Class) > classSeverity(a.worst) {
			a.worst = c.NodeCards[i].Class
		}
	}
	sort.Strings(roles)

	out := make([]RoleCapabilities, 0, len(roles))
	for _, role := range roles {
		a := byRole[role]
		caps := make([]string, 0, len(a.caps))
		for capName := range a.caps {
			caps = append(caps, capName)
		}
		sort.Strings(caps)
		out = append(out, RoleCapabilities{
			Role:         role,
			Capabilities: caps,
			TaskIDs:      a.tasks,
			WorstClass:   a.worst,
		})
	}
	return out
}
