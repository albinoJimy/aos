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
	// Cost é o custo ESTIMADO deste nó/RAMO do grafo (opcional). É o custo POR RAMO
	// (distinto do agregado do plano em [CostEstimate] agregado): o wiring popula-o a
	// partir da agregação por-subárvore do scheduler (AOS-027) + otel-genai (AOS-078). O
	// [PlanCard] EXIBE-o por-nó; o gate não o calcula. Nil ⇒ sem custo por-ramo conhecido.
	Cost *CostEstimate
	// CapabilityGap marca um nó cuja capability requerida NÃO está (ainda) concedida — um
	// GAP de capacidade que EXIGE revisão item-a-item (não colapsável), a par dos nós
	// Class >= gray. LIDO do planeamento/orchestrator no wiring; o gate não o infere.
	CapabilityGap bool

	// --- Extensões declarativas de ADR-022, projectadas para o gate (DEF-274) ---
	//
	// Os quatro campos seguintes são o que torna VERDADEIRO o invariante §2.4(5) do
	// ADR-022 («o humano no gate vê o organigrama COM as condições e os verificadores
	// declarados»). São LIDOS do PlanDocument no wiring — o gate não os infere, não os
	// valida semanticamente e não deriva taint: isso é do validador puro a montante
	// (AOS-231). O gate impõe só a FORMA CANÓNICA (símbolos de charset fechado,
	// inteiros, referências a nós do próprio plano) — ver extensions.go, que é onde a
	// regra de ouro do cartão («sem segredos») deixa de ser convenção e passa a ser
	// [ErrNonCanonicalExtension] fail-closed. Todos são OPCIONAIS e ADITIVOS: um plano
	// que não os use produz o mesmo cartão de sempre.

	// Role é o PAPEL DECLARADO do nó no plano (ADR-022 §2.2) — [RoleVerifier] é o
	// único reservado. NÃO confundir com o `Role` de [RoleCapabilities], que é a
	// identidade não-humana (o [PlanNode.Agent]): aqui é o papel de EXECUÇÃO — quem
	// PRODUZ e quem JULGA. Vazio ⇒ papel não declarado.
	Role string
	// ConditionalOn são as arestas CONDICIONAIS que governam a ENTRADA deste nó
	// (ADR-022 §2.1) — «sob que condição este ramo corre de todo». Contam também como
	// PRECEDÊNCIA na ordenação do cartão (ver [Plan.effectiveEdges]).
	ConditionalOn []PlanCondition
	// Outputs são os contratos de SAÍDA declarados pelo nó (ADR-022 §2.3), em forma
	// canónica (nome + tipo + taint EFECTIVO). Nunca o conteúdo do output.
	Outputs []PlanOutput
	// Consumes são as arestas de DADOS que entram no nó (ADR-022 §2.3) — que output de
	// que origem alimenta este nó, com que tipo. Nunca o payload.
	Consumes []PlanConsume
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
//
// Desde DEF-274 valida TAMBÉM a FORMA CANÓNICA das extensões de ADR-022 de cada nó
// ([PlanNode.validateExtensions]): papel, condições e contratos de dados têm de ser
// símbolos/inteiros/referências ao próprio plano — nunca conteúdo. Fail-closed com
// [ErrNonCanonicalExtension]: um plano cuja extensão não caiba na forma canónica NÃO é
// aprovável, porque apresentá-lo exigiria mostrar conteúdo do run no cartão ou mentir
// por omissão.
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
	// Segunda passagem: as referências das extensões só se conferem depois de o
	// conjunto de task_ids estar completo (uma aresta condicional pode observar um nó
	// declarado mais à frente na fatia) — e o mesmo vale para as arestas declaradas, de
	// que a invariante de montante do `consumes` depende.
	declared := make(map[[2]string]bool, len(p.Edges))
	for _, e := range p.Edges {
		declared[e] = true
	}
	for _, n := range p.Nodes {
		if err := n.validateExtensions(seen, declared); err != nil {
			return err
		}
	}
	return nil
}

// effectiveEdges devolve as arestas de PRECEDÊNCIA efectivas do plano: as declaradas
// em [Plan.Edges] MAIS as induzidas pelas arestas condicionais de cada nó
// (From→TaskID), sem duplicados e em ordem estável.
//
// PORQUE UMA ARESTA *condicional* É TAMBÉM PRECEDÊNCIA: a condição avalia-se sobre o
// RESULTADO REGISTADO da origem (ADR-022 §2.1), logo a origem TEM de correr primeiro.
// Se a ordenação as ignorasse, um cartão com um ramo condicional podia mostrar o nó
// guardado ANTES da origem que o guarda — o humano aprovaria uma ordem que não é a que
// vai correr, que é o defeito exacto que DEF-274 fecha. E como entram na topologia,
// entram também na detecção de ciclo ([ErrPlanCycle]): o invariante 1 do ADR
// (aciclicidade) vale no cartão sem uma travessia nova.
//
// Retrocompatível por construção: sem `conditional_on`, devolve as arestas declaradas
// tal-quais.
func (p Plan) effectiveEdges() [][2]string {
	total := 0
	for _, n := range p.Nodes {
		total += len(n.ConditionalOn)
	}
	if total == 0 {
		return p.Edges
	}
	seen := make(map[[2]string]bool, len(p.Edges)+total)
	out := make([][2]string, 0, len(p.Edges)+total)
	add := func(e [2]string) {
		if seen[e] {
			return
		}
		seen[e] = true
		out = append(out, e)
	}
	for _, e := range p.Edges {
		add(e)
	}
	for _, n := range p.Nodes {
		for _, c := range n.ConditionalOn {
			add([2]string{c.From, n.TaskID})
		}
	}
	return out
}

// conditionalEdges devolve as arestas de precedência INDUZIDAS por `conditional_on` e
// que NÃO estão declaradas em [Plan.Edges] — a diferença exacta entre o grafo que
// ORDENA ([Plan.effectiveEdges]) e o grafo que o cartão MOSTRA.
//
// É esta diferença que o cartão tem de expor separadamente (ver [PlanCard.ConditionalEdges]):
// enquanto ela viveu só dentro de `node_extensions`, o único campo do cartão com forma de
// GRAFO omitia precisamente as arestas que DEF-274 introduziu, e uma superfície que
// desenhasse o organigrama a partir de `edges` mostrava um nó guardado por condição como
// raiz sem entrada — «corre incondicionalmente desde o início», a leitura oposta à verdade.
//
// Ordem estável: nós pela ordem do slice, condições pela ordem declarada; sem duplicados.
func (p Plan) conditionalEdges() [][2]string {
	declared := make(map[[2]string]bool, len(p.Edges))
	for _, e := range p.Edges {
		declared[e] = true
	}
	var out [][2]string
	seen := make(map[[2]string]bool)
	for _, n := range p.Nodes {
		for _, c := range n.ConditionalOn {
			e := [2]string{c.From, n.TaskID}
			if declared[e] || seen[e] {
				continue
			}
			seen[e] = true
			out = append(out, e)
		}
	}
	return out
}

// TopoOrder devolve uma ordenação topológica ESTÁVEL e REPRODUZÍVEL do grafo (Kahn com
// desempate por task_id lexicográfico — NUNCA a ordem de um mapa Go), espelhando a
// semântica do [orchestrator.DAG.TopoOrder] (ADR-010): para os mesmos nós/arestas o
// plano é IDÊNTICO. Devolve [ErrPlanCycle] se o grafo tiver um ciclo (fail-closed — um
// plano com ciclo não é aprovável). Assume [Plan.Validate] já passou.
//
// Desde DEF-274 ordena sobre as arestas EFECTIVAS ([Plan.effectiveEdges]): as
// declaradas mais as induzidas pelas arestas condicionais de ADR-022 §2.1.
func (p Plan) TopoOrder() ([]string, error) {
	indeg := make(map[string]int, len(p.Nodes))
	for _, n := range p.Nodes {
		if _, ok := indeg[n.TaskID]; !ok {
			indeg[n.TaskID] = 0
		}
	}
	succ := make(map[string][]string, len(p.Nodes))
	for _, e := range p.effectiveEdges() {
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
