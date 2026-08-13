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
	// ErrConditionalUnsupported — o plano declara arestas condicionais (ADR-022 §2.1)
	// mas o Dispatcher não foi construído com as portas que as suportam
	// ([WithConditionalBranches]). FAIL-CLOSED POR OMISSÃO: um despachante que não
	// sabe avaliar um guarda NÃO o ignora — recusa o plano inteiro. Ignorar seria
	// despachar nós que o plano condicionou, que é precisamente o efeito que a
	// extensão existe para impedir.
	ErrConditionalUnsupported = errors.New("plandispatch: plano com arestas condicionais num despachante sem suporte de ramos")
	// ErrBranchDigestMismatch — há uma decisão de ramo REGISTADA para o nó, mas o
	// digest da condição no documento diverge do digest registado: o documento mudou
	// desde a decisão. Não é um replay — é um plano diferente, e um plano diferente
	// volta ao gate. Fail-closed (ADR-010: o replay reproduz, não reconcilia).
	ErrBranchDigestMismatch = errors.New("plandispatch: digest da condição diverge da decisão de ramo registada")
	// ErrBranchJournal — o registo append-only das decisões de ramo falhou (leitura
	// ou escrita). Sem o facto durável não há garantia de replay sem re-avaliação,
	// pelo que a passagem ABORTA em vez de despachar sobre uma decisão que ninguém
	// conseguiu registar.
	ErrBranchJournal = errors.New("plandispatch: registo de decisões de ramo indisponível")
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
	// ConditionalOn são as arestas CONDICIONAIS do documento APROVADO (ADR-022 §2.1).
	// Content-free como o resto do nó: a expressão é feita de enums fechados, ids e
	// inteiros — nada de texto livre do modelo. Vazio (o caso de todos os planos
	// pré-ADR-022) ⇒ o despacho comporta-se exactamente como antes.
	ConditionalOn []plan.ConditionalEdge
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
	// OutcomeWaitingCondition — o nó tem arestas condicionais cuja decisão ainda NÃO
	// é possível: alguma origem não tem resultado registado, ou o débito de orçamento
	// da avaliação não passou. Espera — e, como as outras esperas, NÃO consome
	// headroom.
	OutcomeWaitingCondition Outcome = "waiting_condition"
	// OutcomeBranchNotTaken — a condição foi DECIDIDA como falsa (directamente, ou
	// por herança de uma origem cujo ramo não foi tomado): o nó não será despachado
	// neste plano. É um estado TERMINAL da passagem, não uma espera — os resultados
	// registados são imutáveis, pelo que a decisão não muda. Voltar a este nó é
	// replan de subgrafo (AOS-239), nunca uma re-avaliação.
	OutcomeBranchNotTaken Outcome = "branch_not_taken"
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
	// NotTaken conta os nós PODADOS nesta passagem ([OutcomeBranchNotTaken]) — o
	// ramo decidido como falso e a sua descendência. Contador próprio (e não uma
	// variante de Deferred) porque a disposição é oposta: um diferido volta, um
	// podado não.
	NotTaken int
	// BranchesEvaluated conta as decisões de ramo TOMADAS E REGISTADAS nesta
	// passagem — as que debitaram orçamento. Uma passagem de replay, que lê as
	// decisões do registo, deixa este contador a ZERO: é a falsificação directa de
	// «o replay não re-avalia».
	BranchesEvaluated int
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
	// Portas das ARESTAS CONDICIONAIS (ADR-022 §2.1). OPCIONAIS por construção — um
	// despachante sem elas continua a ser o de AOS-238 e despacha planos sem
	// condições exactamente como antes. Fail-closed por omissão: um plano QUE USE
	// condições contra um despachante sem estas portas é recusado
	// ([ErrConditionalUnsupported]), nunca despachado com os guardas ignorados.
	results ResultView
	journal BranchJournal
	meter   BranchBudget
}

// Option configura capacidades OPCIONAIS do [Dispatcher]. Variádica em
// [NewDispatcher] de propósito: as cinco portas de AOS-238 continuam obrigatórias e
// posicionais (nenhum chamador existente muda), e o que é opcional declara-se.
type Option func(*Dispatcher)

// WithConditionalBranches liga o suporte de ARESTAS CONDICIONAIS (ADR-022 §2.1,
// AOS-270). As TRÊS portas andam juntas e são indivisíveis:
//
//   - `results` é o RESULTADO REGISTADO sobre o qual a condição é avaliada;
//   - `journal` é o registo append-only da decisão — sem ele não há replay sem
//     re-avaliação, logo não há conformidade com ADR-010;
//   - `meter` é o débito de orçamento da árvore (ADR-008) — sem ele a avaliação
//     seria trabalho grátis escondido no despachante.
//
// Passar uma delas a nil é o mesmo que não ligar nada (fail-closed): a opção é
// ignorada e um plano com condições volta a ser recusado por
// [ErrConditionalUnsupported]. Não há meio-suporte.
func WithConditionalBranches(results ResultView, journal BranchJournal, meter BranchBudget) Option {
	return func(d *Dispatcher) {
		if results == nil || journal == nil || meter == nil {
			return
		}
		d.results, d.journal, d.meter = results, journal, meter
	}
}

// NewDispatcher constrói um Dispatcher. TODAS as portas posicionais são
// obrigatórias — a sua ausência é fail-closed ([ErrDeps]). As capacidades
// opcionais ligam-se por [Option].
func NewDispatcher(gate Gate, lifecycle LifecycleView, headroom Headroom, cards CardOracle, sink DispatchSink, opts ...Option) (*Dispatcher, error) {
	if gate == nil || lifecycle == nil || headroom == nil || cards == nil || sink == nil {
		return nil, ErrDeps
	}
	d := &Dispatcher{gate: gate, lifecycle: lifecycle, headroom: headroom, cards: cards, sink: sink}
	for _, o := range opts {
		if o != nil {
			o(d)
		}
	}
	return d, nil
}

// conditionalReady indica se as três portas de ramos condicionais estão ligadas.
func (d *Dispatcher) conditionalReady() bool {
	return d.results != nil && d.journal != nil && d.meter != nil
}

// Dispatch corre UMA passagem de despacho sobre o plano, DETERMINISTICAMENTE:
//
//  1. GATE — se o plano não está materializado, TODOS os nós ficam em espera de gate
//     e NENHUM slot de headroom é tocado (fail-closed, a jusante do gate);
//
//  2. ordena os nós por node_id (ordem canónica, independente da ordem do slice);
//
//  3. lê o estado de cada nó UMA vez (vista do ciclo de vida) e calcula a
//     elegibilidade: pendente + ramo condicional tomado + deps concluídas + (se
//     exige) cartão resolvido. Esta avaliação NÃO toca no headroom — esperar é
//     gratuito em concorrência;
//
//     3-bis. RAMOS CONDICIONAIS (ADR-022 §2.1, AOS-270), quando o plano os declara: a
//     decisão de cada nó é LIDA do registo append-only se já for um facto, e só é
//     avaliada — pura, sobre o resultado REGISTADO — se ainda não existir. Uma
//     decisão nova debita o orçamento da árvore (ADR-008) e é apensa como
//     `plan.branch_decided`; um ramo não tomado PODA o nó e a sua descendência;
//
//  4. só para os nós ELEGÍVEIS, e por ordem, tenta [Headroom.Acquire] (re-verificação
//     TOCTOU atómica). O primeiro false (pressão) adia esse e os restantes elegíveis
//     (spawn diferido) — nunca oversubscreve;
//
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

	// 3-bis) RAMOS CONDICIONAIS (ADR-022 §2.1). Decide-se ANTES da elegibilidade e
	// numa fase própria: a decisão de um nó pode PODAR outros (a descendência de um
	// ramo não tomado), o que é uma propriedade do GRAFO e não de um nó isolado.
	// Também não toca no headroom — decidir um ramo não é despachar.
	res := Result{Materialized: true, Results: make([]NodeResult, 0, len(nodes))}
	branches, evaluated, err := d.decideBranches(ctx, p, nodes, states)
	if err != nil {
		return Result{}, err
	}
	res.BranchesEvaluated = evaluated

	// Calcula a elegibilidade SEM tocar no headroom. Os não-elegíveis recebem já o seu
	// outcome de espera; os elegíveis entram na fila de Acquire por ordem canónica.
	eligible := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		// Só um nó PENDENTE é candidato. Running/complete/failed/unknown → nada a fazer.
		if states[n.NodeID] != NodePending {
			res.Results = append(res.Results, NodeResult{NodeID: n.NodeID, Outcome: OutcomeInflightOrDone, Reason: reasonForState(states[n.NodeID])})
			continue
		}
		// Ramo condicional: um ramo NÃO TOMADO é terminal (o nó sai do plano) e um
		// ramo por decidir é espera. Ambos ANTES das dependências, porque são o sinal
		// mais forte: um nó podado nunca correrá, por muito que as suas deps concluam.
		switch branches[n.NodeID] {
		case branchNotTaken:
			res.Results = append(res.Results, NodeResult{NodeID: n.NodeID, Outcome: OutcomeBranchNotTaken, Reason: "ramo condicional não tomado"})
			res.NotTaken++
			continue
		case branchUndecided:
			if len(n.ConditionalOn) > 0 {
				res.Results = append(res.Results, NodeResult{NodeID: n.NodeID, Outcome: OutcomeWaitingCondition, Reason: "condição por decidir (origem sem resultado registado ou orçamento indisponível)"})
				continue
			}
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
	// não despacha para fora do que o ORQ materializou). O MESMO vale para as arestas
	// CONDICIONAIS: uma condição sobre um nó fora do conjunto materializado seria um
	// oráculo externo disfarçado de aresta, e o despachante não tem — nem deve ter —
	// como o observar.
	for _, n := range p.Nodes {
		for _, dep := range n.DependsOn {
			if dep == "" {
				return fmt.Errorf("%w: depends_on vazio no nó %q", ErrInvalidPlan, n.NodeID)
			}
			if _, ok := ids[dep]; !ok {
				return fmt.Errorf("%w: depends_on %q do nó %q fora do conjunto materializado", ErrInvalidPlan, dep, n.NodeID)
			}
		}
		for _, ce := range n.ConditionalOn {
			if ce.From == "" {
				return fmt.Errorf("%w: conditional_on vazio no nó %q", ErrInvalidPlan, n.NodeID)
			}
			if _, ok := ids[ce.From]; !ok {
				return fmt.Errorf("%w: conditional_on %q do nó %q fora do conjunto materializado", ErrInvalidPlan, ce.From, n.NodeID)
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

// findDependencyCycle corre uma DFS a três cores sobre o grafo de arestas de
// entrada — `depends_on` UNIDO às origens CONDICIONAIS. Devolve ("", true) se é
// acíclico, ou (nodeID, false) num nó envolvido no primeiro ciclo encontrado
// (auto-dependência incluída: um nó cinzento revisitado). Pressupõe o grafo já
// validado quanto a arestas pendentes.
//
// A união é defesa-em-profundidade, não a defesa principal: a autoridade sobre «uma
// aresta condicional nunca fecha ciclo» é o validador puro (AOS-231), que o impõe
// no MESMO DAG de AOS-025 ANTES da admissão. Esta segunda linha existe porque
// [Plan] pode ser construído por wiring que não passe pelo validador, e um ciclo
// que chegasse aqui ficaria diferido para sempre em silêncio.
func findDependencyCycle(nodes []Node) (string, bool) {
	const (
		white = 0 // não visitado
		gray  = 1 // na pilha de recursão corrente
		black = 2 // totalmente explorado
	)
	adj := make(map[string][]string, len(nodes))
	for _, n := range nodes {
		if len(n.ConditionalOn) == 0 {
			adj[n.NodeID] = n.DependsOn
			continue
		}
		in := make([]string, 0, len(n.DependsOn)+len(n.ConditionalOn))
		in = append(in, n.DependsOn...)
		for _, ce := range n.ConditionalOn {
			in = append(in, ce.From)
		}
		adj[n.NodeID] = in
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
			NodeID:    mn.NodeID,
			DependsOn: append([]string(nil), dn.DependsOn...),
			// Arestas condicionais do documento APROVADO (ADR-022 §2.1). Cópia, como
			// as dependências: o plano de despacho não partilha slices com o documento.
			ConditionalOn: append([]plan.ConditionalEdge(nil), dn.ConditionalOn...),
			RequiresCard:  requires,
		})
	}
	return out, nil
}
