package plandispatch

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// Sentinelas de erro — fail-closed, comparáveis por errors.Is.
var (
	// ErrDeps — dependências obrigatórias do Dispatcher em falta (gate/lifecycle/
	// headroom/cards/sink).
	ErrDeps = errors.New("plandispatch: dependências em falta (gate/lifecycle/headroom/cards/sink)")
	// ErrInvalidPlan — plano malformado (plan_id vazio, node_id vazio/duplicado, ou
	// aresta depends_on inexistente no conjunto materializado).
	ErrInvalidPlan = errors.New("plandispatch: plano de despacho inválido")
	// ErrNodeNotMaterialized — [PlanFrom] encontrou um nó materializado sem
	// correspondência no documento aprovado: incoerência ORQ↔doc. Fail-closed (não
	// se inventa a topologia ausente). Nota: o INVERSO não é erro — o documento pode
	// conter nós que a materialização (legitimamente) não inclui; o conjunto
	// despachável é EXACTAMENTE o materializado, um subconjunto do documento.
	ErrNodeNotMaterialized = errors.New("plandispatch: nó materializado sem correspondência no documento aprovado")
	// ErrDispatchSink — o sink falhou o despacho de um nó. O slot é devolvido e o erro
	// é propagado (nunca silencioso).
	ErrDispatchSink = errors.New("plandispatch: sink recusou o despacho")
)

// Node é um nó despachável do plano APROVADO/MATERIALIZADO. Content-free: só o id, as
// arestas `depends_on` (do documento aprovado) e se o nó exige um cartão resolvido
// antes de despachar (danger ou gap de capacidade). NÃO carrega conteúdo untrusted.
type Node struct {
	NodeID    string
	DependsOn []string
	// RequiresCard marca um nó `danger` ou `waiting_on_capability`: só despacha com o
	// cartão resolvido ([CardOracle.Cleared]). Um nó safe/gray sem gap é inerentemente
	// autorizado quanto a cartão.
	RequiresCard bool
}

// Plan é o conjunto de nós de UM plano a despachar. É a projecção do que o ORQ
// MATERIALIZOU (o SCH nunca despacha nada fora deste conjunto).
type Plan struct {
	PlanID string
	Nodes  []Node
}

// Outcome é a disposição de despacho de um nó numa passagem. Enum fechado.
type Outcome string

const (
	// OutcomeDispatched — o nó foi entregue ao sink nesta passagem.
	OutcomeDispatched Outcome = "dispatched"
	// OutcomeDeferredHeadroom — elegível mas sem headroom AGORA (TOCTOU): spawn
	// diferido. O escalonador re-invoca quando o headroom libertar.
	OutcomeDeferredHeadroom Outcome = "deferred_headroom"
	// OutcomeWaitingGate — o plano ainda não passou o gate/materialização.
	OutcomeWaitingGate Outcome = "waiting_gate"
	// OutcomeWaitingDeps — uma ou mais `depends_on` ainda não concluídas.
	OutcomeWaitingDeps Outcome = "waiting_deps"
	// OutcomeWaitingCard — nó `waiting_on_capability`/`danger` sem cartão resolvido.
	OutcomeWaitingCard Outcome = "waiting_card"
	// OutcomeInflightOrDone — o nó já não está pendente (running/complete/failed) ou o
	// seu estado é desconhecido: nada a despachar.
	OutcomeInflightOrDone Outcome = "inflight_or_done"
)

// NodeResult é a disposição de um nó nesta passagem (falsificável: o teste lê o
// outcome e a razão por nó).
type NodeResult struct {
	NodeID  string
	Outcome Outcome
	Reason  string
}

// Result é o resultado de uma passagem de despacho. Determinístico: os resultados
// vêm em ordem canónica de node_id.
type Result struct {
	Materialized bool
	Results      []NodeResult
	Dispatched   int
	Deferred     int
}

// Dispatcher despacha nós de um plano aprovado, a jusante do gate. SEM ESTADO
// próprio de escalonamento — é seguro para uso concorrente (a segurança é a das
// portas ligadas). Construir com [NewDispatcher].
type Dispatcher struct {
	gate      Gate
	lifecycle LifecycleView
	headroom  Headroom
	cards     CardOracle
	sink      DispatchSink
}

// NewDispatcher constrói um Dispatcher. TODAS as portas são obrigatórias — a sua
// ausência é fail-closed ([ErrDeps]).
func NewDispatcher(gate Gate, lifecycle LifecycleView, headroom Headroom, cards CardOracle, sink DispatchSink) (*Dispatcher, error) {
	if gate == nil || lifecycle == nil || headroom == nil || cards == nil || sink == nil {
		return nil, ErrDeps
	}
	return &Dispatcher{gate: gate, lifecycle: lifecycle, headroom: headroom, cards: cards, sink: sink}, nil
}

// Dispatch corre UMA passagem de despacho sobre o plano, DETERMINISTICAMENTE:
//
//  1. GATE — se o plano não está materializado, TODOS os nós ficam em espera de gate
//     e NENHUM slot de headroom é tocado (fail-closed, a jusante do gate);
//  2. ordena os nós por node_id (ordem canónica, independente da ordem do slice);
//  3. lê o estado de cada nó UMA vez (vista do ciclo de vida) e calcula a
//     elegibilidade: pendente + deps concluídas + (se exige) cartão resolvido. Esta
//     avaliação NÃO toca no headroom — esperar é gratuito em concorrência;
//  4. só para os nós ELEGÍVEIS, e por ordem, tenta [Headroom.Acquire] (re-verificação
//     TOCTOU atómica). O primeiro false (pressão) adia esse e os restantes elegíveis
//     (spawn diferido) — nunca oversubscreve;
//  5. um Acquire bem-sucedido entrega o nó ao [DispatchSink]. Uma falha do sink
//     DEVOLVE o slot ([Headroom.Release]) e propaga o erro (nunca silencioso, nunca
//     spawn parcial silencioso).
//
// Não corre loop nem retentativa: o escalonador externo re-invoca (ADR-018 — este
// pacote não é autoridade concorrente do ciclo de vida).
func (d *Dispatcher) Dispatch(ctx context.Context, p Plan) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := validatePlan(p); err != nil {
		return Result{}, err
	}

	// Ordem canónica por node_id (nunca a ordem do slice de entrada).
	nodes := make([]Node, len(p.Nodes))
	copy(nodes, p.Nodes)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeID < nodes[j].NodeID })

	// 1) GATE. A jusante do gate: sem materialização, ninguém despacha e o headroom
	// fica INTOCADO (a espera de gate não consome concorrência).
	materialized, err := d.gate.Materialized(ctx, p.PlanID)
	if err != nil {
		return Result{}, fmt.Errorf("plandispatch: consultar gate: %w", err)
	}
	if !materialized {
		res := Result{Materialized: false, Results: make([]NodeResult, 0, len(nodes))}
		for _, n := range nodes {
			res.Results = append(res.Results, NodeResult{NodeID: n.NodeID, Outcome: OutcomeWaitingGate, Reason: "plano não materializado"})
		}
		return res, nil
	}

	// 3) Lê o estado de cada nó UMA vez (para a própria elegibilidade e para a
	// satisfação de dependentes). Fail-closed em erro.
	states := make(map[string]NodeState, len(nodes))
	for _, n := range nodes {
		st, err := d.lifecycle.State(ctx, p.PlanID, n.NodeID)
		if err != nil {
			return Result{}, fmt.Errorf("plandispatch: estado do nó %q: %w", n.NodeID, err)
		}
		states[n.NodeID] = st
	}

	// Calcula a elegibilidade SEM tocar no headroom. Os não-elegíveis recebem já o seu
	// outcome de espera; os elegíveis entram na fila de Acquire por ordem canónica.
	res := Result{Materialized: true, Results: make([]NodeResult, 0, len(nodes))}
	eligible := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		// Só um nó PENDENTE é candidato. Running/complete/failed/unknown → nada a fazer.
		if states[n.NodeID] != NodePending {
			res.Results = append(res.Results, NodeResult{NodeID: n.NodeID, Outcome: OutcomeInflightOrDone, Reason: reasonForState(states[n.NodeID])})
			continue
		}
		// Dependências: TODAS têm de estar CONCLUÍDAS. Fail-closed: uma dep desconhecida
		// (não presente no plano/estado) conta como não satisfeita.
		if dep, ok := unmetDependency(n, states); !ok {
			res.Results = append(res.Results, NodeResult{NodeID: n.NodeID, Outcome: OutcomeWaitingDeps, Reason: fmt.Sprintf("depends_on %q não concluída", dep)})
			continue
		}
		// Cartão: nós danger / waiting_on_capability só passam com cartão resolvido.
		if n.RequiresCard {
			cleared, err := d.cards.Cleared(ctx, p.PlanID, n.NodeID)
			if err != nil {
				return Result{}, fmt.Errorf("plandispatch: cartão do nó %q: %w", n.NodeID, err)
			}
			if !cleared {
				res.Results = append(res.Results, NodeResult{NodeID: n.NodeID, Outcome: OutcomeWaitingCard, Reason: "cartão (danger/capability) por resolver"})
				continue
			}
		}
		eligible = append(eligible, n)
	}

	// 4+5) Só agora se toca no headroom, e SÓ para elegíveis. Re-verificação TOCTOU
	// atómica por nó: o primeiro false adia esse e todos os restantes elegíveis.
	ceilingHit := false
	for i, n := range eligible {
		if ceilingHit {
			res.Results = append(res.Results, NodeResult{NodeID: n.NodeID, Outcome: OutcomeDeferredHeadroom, Reason: "sem headroom (spawn diferido)"})
			res.Deferred++
			continue
		}
		ok, err := d.headroom.Acquire(ctx)
		if err != nil {
			// Erro do headroom é fail-closed: adia (não oversubscreve) e não tenta mais.
			ceilingHit = true
			res.Results = append(res.Results, NodeResult{NodeID: n.NodeID, Outcome: OutcomeDeferredHeadroom, Reason: fmt.Sprintf("headroom indisponível: %v", err)})
			res.Deferred++
			continue
		}
		if !ok {
			// Pressão: slot NÃO reservado — adia este e os restantes elegíveis.
			ceilingHit = true
			res.Results = append(res.Results, NodeResult{NodeID: n.NodeID, Outcome: OutcomeDeferredHeadroom, Reason: "sem headroom (spawn diferido)"})
			res.Deferred++
			continue
		}
		// Slot reservado (TOCTOU-safe). Entrega ao sink; falha devolve o slot e propaga.
		if err := d.sink.Dispatch(ctx, n); err != nil {
			_ = d.headroom.Release(ctx)
			res.Results = append(res.Results, NodeResult{NodeID: n.NodeID, Outcome: OutcomeDeferredHeadroom, Reason: fmt.Sprintf("sink recusou: %v", err)})
			res.Deferred++
			// Aborto da passagem: os restantes elegíveis NÃO são tentados. Completa o
			// Result (NUNCA parcial) marcando-os como diferidos-não-tentados, para que
			// cada nó do plano tenha um outcome mesmo no caminho de erro.
			for _, m := range eligible[i+1:] {
				res.Results = append(res.Results, NodeResult{NodeID: m.NodeID, Outcome: OutcomeDeferredHeadroom, Reason: "não tentado (aborto por falha do sink)"})
				res.Deferred++
			}
			// Ordena antes de propagar (resultado COMPLETO e SURFACED, nunca silencioso).
			sortResults(res.Results)
			return res, fmt.Errorf("%w: nó %q: %v", ErrDispatchSink, n.NodeID, err)
		}
		res.Results = append(res.Results, NodeResult{NodeID: n.NodeID, Outcome: OutcomeDispatched, Reason: ""})
		res.Dispatched++
	}

	sortResults(res.Results)
	return res, nil
}

// unmetDependency devolve (dep, true) se TODAS as dependências estão concluídas, ou
// (nomeDaDepFalhada, false) na primeira dependência não concluída. Fail-closed: uma
// dep sem estado conhecido (não no plano) conta como NÃO concluída.
func unmetDependency(n Node, states map[string]NodeState) (string, bool) {
	for _, dep := range n.DependsOn {
		st, known := states[dep]
		if !known || st != NodeComplete {
			return dep, false
		}
	}
	return "", true
}

// reasonForState descreve, sem PII, porque um nó não-pendente não é candidato.
func reasonForState(st NodeState) string {
	switch st {
	case NodeRunning:
		return "já em execução"
	case NodeComplete:
		return "já concluído"
	case NodeFailed:
		return "falha terminal"
	default:
		return "estado desconhecido (fail-closed)"
	}
}

// validatePlan confere a forma do plano de despacho (fail-closed).
func validatePlan(p Plan) error {
	if p.PlanID == "" {
		return fmt.Errorf("%w: plan_id vazio", ErrInvalidPlan)
	}
	if len(p.Nodes) == 0 {
		return fmt.Errorf("%w: sem nós", ErrInvalidPlan)
	}
	ids := make(map[string]struct{}, len(p.Nodes))
	for _, n := range p.Nodes {
		if n.NodeID == "" {
			return fmt.Errorf("%w: node_id vazio", ErrInvalidPlan)
		}
		if _, dup := ids[n.NodeID]; dup {
			return fmt.Errorf("%w: node_id duplicado %q", ErrInvalidPlan, n.NodeID)
		}
		ids[n.NodeID] = struct{}{}
	}
	// Arestas depends_on têm de referir nós do PRÓPRIO conjunto materializado (o SCH
	// não despacha para fora do que o ORQ materializou).
	for _, n := range p.Nodes {
		for _, dep := range n.DependsOn {
			if dep == "" {
				return fmt.Errorf("%w: depends_on vazio no nó %q", ErrInvalidPlan, n.NodeID)
			}
			if _, ok := ids[dep]; !ok {
				return fmt.Errorf("%w: depends_on %q do nó %q fora do conjunto materializado", ErrInvalidPlan, dep, n.NodeID)
			}
		}
	}
	// Deteção de ciclo (inclui auto-dependência). Um conjunto cíclico de depends_on
	// NUNCA seria despachável — as arestas jamais concluiriam — e ficaria preso em
	// silêncio (diferido para sempre). Fail-closed na VALIDAÇÃO: SURFACED como plano
	// inválido em vez de ficar bloqueado sem sinal.
	if cyc, ok := findDependencyCycle(p.Nodes); !ok {
		return fmt.Errorf("%w: ciclo de depends_on detectado (envolvendo o nó %q)", ErrInvalidPlan, cyc)
	}
	return nil
}

// findDependencyCycle corre uma DFS a três cores sobre o grafo depends_on. Devolve
// ("", true) se é acíclico, ou (nodeID, false) num nó envolvido no primeiro ciclo
// encontrado (auto-dependência incluída: um nó cinzento revisitado). Pressupõe o grafo
// já validado quanto a arestas pendentes.
func findDependencyCycle(nodes []Node) (string, bool) {
	const (
		white = 0 // não visitado
		gray  = 1 // na pilha de recursão corrente
		black = 2 // totalmente explorado
	)
	adj := make(map[string][]string, len(nodes))
	for _, n := range nodes {
		adj[n.NodeID] = n.DependsOn
	}
	color := make(map[string]int, len(nodes))
	var onCycle string
	var visit func(id string) bool // true ⇒ ciclo detectado a partir de id
	visit = func(id string) bool {
		color[id] = gray
		for _, dep := range adj[id] {
			switch color[dep] {
			case gray:
				onCycle = dep
				return true
			case white:
				if visit(dep) {
					return true
				}
			}
		}
		color[id] = black
		return false
	}
	for _, n := range nodes {
		if color[n.NodeID] == white {
			if visit(n.NodeID) {
				return onCycle, false
			}
		}
	}
	return "", true
}

// sortResults ordena os resultados por node_id (determinismo de saída).
func sortResults(rs []NodeResult) {
	sort.Slice(rs, func(i, j int) bool { return rs[i].NodeID < rs[j].NodeID })
}

// PlanFrom projecta um [Plan] de despacho a partir do que o ORQ MATERIALIZOU
// ([plannerevents.MaterializedPayload]) e do documento APROVADO ([plan.PlanDocument]).
// É a fronteira honesta da integração: o conjunto DESPACHÁVEL é EXACTAMENTE o
// materializado (o SCH nunca despacha fora dele); as arestas `depends_on` e o rótulo
// de risco vêm do documento aprovado.
//
// RequiresCard = (RiskClass == danger) OU needsCard(nodeID) — este último permite ao
// wiring marcar nós com gap de capacidade aberto (waiting_on_capability), cuja
// resolução é conhecida pelo capabilitygap.Coordinator, não pelo documento. Se
// needsCard for nil, apenas o risco danger exige cartão.
//
// Fail-closed: um nó materializado ausente do documento (ou um id vazio/duplicado) é
// [ErrNodeNotMaterialized]/[ErrInvalidPlan] — não se inventa a topologia em falta.
func PlanFrom(mat plannerevents.MaterializedPayload, doc plan.PlanDocument, needsCard func(nodeID string) bool) (Plan, error) {
	if mat.PlanID == "" {
		return Plan{}, fmt.Errorf("%w: plan_id vazio no payload materializado", ErrInvalidPlan)
	}
	if len(mat.Nodes) == 0 {
		return Plan{}, fmt.Errorf("%w: payload materializado sem nós", ErrInvalidPlan)
	}
	// Index do documento aprovado por node_id (arestas + risco).
	byID := make(map[string]plan.Node, len(doc.Nodes))
	for _, dn := range doc.Nodes {
		byID[dn.NodeID] = dn
	}
	out := Plan{PlanID: mat.PlanID, Nodes: make([]Node, 0, len(mat.Nodes))}
	seen := make(map[string]struct{}, len(mat.Nodes))
	for _, mn := range mat.Nodes {
		if mn.NodeID == "" {
			return Plan{}, fmt.Errorf("%w: node_id materializado vazio", ErrInvalidPlan)
		}
		if _, dup := seen[mn.NodeID]; dup {
			return Plan{}, fmt.Errorf("%w: node_id materializado duplicado %q", ErrInvalidPlan, mn.NodeID)
		}
		seen[mn.NodeID] = struct{}{}
		dn, ok := byID[mn.NodeID]
		if !ok {
			return Plan{}, fmt.Errorf("%w: nó %q", ErrNodeNotMaterialized, mn.NodeID)
		}
		requires := dn.RiskClass == plan.RiskDanger
		if needsCard != nil && needsCard(mn.NodeID) {
			requires = true
		}
		out.Nodes = append(out.Nodes, Node{
			NodeID:       mn.NodeID,
			DependsOn:    append([]string(nil), dn.DependsOn...),
			RequiresCard: requires,
		})
	}
	return out, nil
}
