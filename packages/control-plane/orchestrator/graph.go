package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/aos-ref/control-plane/orchestrator/contract"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// ErrEdgeClosesCycle é devolvido pela admissão de uma aresta que fecharia um
// ciclo no DAG. A aresta NÃO é adicionada (fail-closed) — nunca se aceita e
// "corrige depois". Ver [DAG.AddEdge] / [GraphBuilder.AddEdge].
var ErrEdgeClosesCycle = errors.New("orchestrator: aresta rejeitada — fecharia um ciclo (aciclicidade fail-closed)")

// ErrNodeExists é devolvido ao admitir um task_id já presente no DAG.
var ErrNodeExists = errors.New("orchestrator: nó já existe no grafo")

// ErrNodeNotFound é devolvido quando uma aresta referencia um task_id ausente.
var ErrNodeNotFound = errors.New("orchestrator: nó inexistente no grafo")

// ErrEmptyTaskID é devolvido ao admitir um nó com task_id vazio.
var ErrEmptyTaskID = errors.New("orchestrator: task_id vazio")

// ErrLogAhead — o Event Store JÁ continha este facto, mas o DAG em memória tratou-o
// como NOVO. Significa uma coisa só: este [GraphBuilder] está a construir CEGO sobre um
// run que já existe no log.
//
// # O DEFEITO QUE TORNA VISÍVEL
//
// O `emit` descartava o [eventstore.AppendResult], pelo que um `StatusDuplicate` — que
// vem com erro NIL — era indistinguível de um commit fresco. Consequência medida a
// 2026-08-30: um builder «retomado» sobre um run existente faz `AddNode` e `MarkRunning`,
// AMBOS devolvem nil, ZERO eventos novos entram no stream, e o chamador fica com a
// ILUSÃO de retoma segura. Depois admite arestas cegas às que já estão duráveis — e uma
// aresta que no log fecharia um ciclo é aceite, porque a memória vazia não a vê. O log
// passa a ter a→b E b→a, e o [RebuildDAG] falha PARA SEMPRE (é função pura do log e o
// log é append-only: não há reparação em banda).
//
// O erro NÃO reverte nada: quando ele dispara, a memória e o log ACABARAM de concordar.
// O que está errado não é o estado — é o chamador, que se julgava dono de um run novo.
// Ver [NewGraphBuilder], que ainda não sabe re-hidratar a partir de [RebuildDAG] (AOS-281).
var ErrLogAhead = errors.New("orchestrator: o log ja contem este facto e o DAG em memoria tratou-o como novo — builder cego sobre um run existente")

// NodeSpec descreve um nó-tarefa a admitir no DAG.
type NodeSpec struct {
	// TaskID identifica o nó dentro do run. É a CHAVE ESTÁVEL de desempate na
	// ordenação topológica (nunca a ordem de um mapa Go).
	TaskID string
	// Priority é a prioridade do nó (maior = mais prioritário). Governa a
	// escolha determinística da vítima na resolução de deadlock.
	Priority int
	// Agent é a identidade não-humana do nó e a sua cadeia de delegação (ADR-003).
	Agent contract.AgentIdentity
	// Task é a tool call do nó (propagada nos eventos; espelha o stub AOS-012).
	Task contract.TaskSpec
}

// dagNode é o nó interno do DAG. seq é a ordem de admissão (monotónica por DAG),
// usada como critério de desempate ESTÁVEL "mais recente" na escolha da vítima.
type dagNode struct {
	spec  NodeSpec
	state state.State
	deps  map[string]bool // predecessores (task_ids de que este nó depende)
	seq   int
}

// DAG é o grafo de tarefas ACÍCLICO de um run (AOS-025). É PURO — sem I/O — e
// impõe a aciclicidade INCREMENTALMENTE na admissão de cada aresta (o fecho
// transitivo do destino não pode conter a origem). A persistência durável é do
// [GraphBuilder]; a reconstrução por replay é de [RebuildDAG].
//
// Não é seguro para uso concorrente sem sincronização externa (o Orquestrador
// serializa a construção de um run).
type DAG struct {
	runID string
	nodes map[string]*dagNode
	succ  map[string]map[string]bool // from -> {to}: aresta From→To (From precede To)
	next  int
}

// NewDAG constrói um DAG vazio para o run dado.
func NewDAG(runID string) *DAG {
	return &DAG{
		runID: runID,
		nodes: make(map[string]*dagNode),
		succ:  make(map[string]map[string]bool),
	}
}

// RunID devolve o identificador do run (stream_id no Event Store).
func (d *DAG) RunID() string { return d.runID }

// Len devolve o número de nós.
func (d *DAG) Len() int { return len(d.nodes) }

// Has indica se o task_id está no DAG.
func (d *DAG) Has(taskID string) bool { _, ok := d.nodes[taskID]; return ok }

// HasEdge indica se a aresta from→to já está admitida. Existe para o [GraphBuilder]
// poder distinguir «acabei de adicionar» de «já lá estava» ANTES de chamar [DAG.AddEdge]
// — distinção que ele não conseguia fazer, porque o AddEdge devolve nil nos dois casos
// (o curto-circuito idempotente não muta). Ver [GraphBuilder.AddEdge].
func (d *DAG) HasEdge(from, to string) bool { return d.succ[from][to] }

// State devolve o estado corrente do nó (state.State de AOS-017) e se existe.
func (d *DAG) State(taskID string) (state.State, bool) {
	n, ok := d.nodes[taskID]
	if !ok {
		return "", false
	}
	return n.state, true
}

// AddNode admite um nó no DAG em estado ready (o estado inicial de todo o nó,
// coerente com a máquina durável de AOS-017). Falha se o task_id for vazio ou já
// existir.
func (d *DAG) AddNode(spec NodeSpec) error {
	if spec.TaskID == "" {
		return ErrEmptyTaskID
	}
	if _, ok := d.nodes[spec.TaskID]; ok {
		return fmt.Errorf("%w: %q", ErrNodeExists, spec.TaskID)
	}
	d.nodes[spec.TaskID] = &dagNode{
		spec:  spec,
		state: state.Ready,
		deps:  make(map[string]bool),
		seq:   d.next,
	}
	d.next++
	return nil
}

// AddEdge admite a aresta From→To (To DEPENDE de From; From precede To). Impõe a
// aciclicidade INCREMENTAL: se From já for alcançável a partir de To (existe um
// caminho To→…→From), a aresta fecharia um ciclo e é REJEITADA com
// [ErrEdgeClosesCycle], sem alterar o grafo (fail-closed). Falha também se algum
// dos nós não existir ou se From==To (auto-laço é sempre um ciclo).
func (d *DAG) AddEdge(from, to string) error {
	if _, ok := d.nodes[from]; !ok {
		return fmt.Errorf("%w: from=%q", ErrNodeNotFound, from)
	}
	if _, ok := d.nodes[to]; !ok {
		return fmt.Errorf("%w: to=%q", ErrNodeNotFound, to)
	}
	if from == to {
		return fmt.Errorf("%w: auto-laço em %q", ErrEdgeClosesCycle, from)
	}
	if d.succ[from][to] {
		return nil // aresta idempotente já presente
	}
	if d.reachable(to, from) {
		return fmt.Errorf("%w: %s→%s (existe caminho %s→…→%s)", ErrEdgeClosesCycle, from, to, to, from)
	}
	if d.succ[from] == nil {
		d.succ[from] = make(map[string]bool)
	}
	d.succ[from][to] = true
	d.nodes[to].deps[from] = true
	return nil
}

// reachable indica se dst é alcançável a partir de src seguindo as arestas
// existentes (DFS iterativo, determinístico e sem recursão profunda).
func (d *DAG) reachable(src, dst string) bool {
	if src == dst {
		return true
	}
	seen := make(map[string]bool, len(d.nodes))
	stack := []string{src}
	for len(stack) > 0 {
		u := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[u] {
			continue
		}
		seen[u] = true
		if u == dst {
			return true
		}
		for v := range d.succ[u] {
			if !seen[v] {
				stack = append(stack, v)
			}
		}
	}
	return false
}

// Reachable indica se existe um caminho dirigido src→…→dst no grafo, contando
// src==dst como alcançável (fecho REFLEXIVO-transitivo). Devolve falso se algum dos
// task_ids não existir — a ausência não é um caminho.
//
// É a MESMA travessia que [DAG.AddEdge] usa para impor a aciclicidade incremental
// ([DAG.reachable]): EXPOSTA, não duplicada. Existe porque a «sub-árvore de
// delegação» de ADR-022 §2.2 (AOS-271) é exactamente uma pergunta de alcançabilidade
// sobre este grafo, e a alternativa — o validador escrever a sua própria travessia —
// criaria uma segunda noção de descendência que podia divergir desta em silêncio.
// Não muta o grafo; a leitura é segura enquanto ninguém admitir arestas em paralelo
// (o DAG não é seguro para uso concorrente, como o resto do tipo).
func (d *DAG) Reachable(src, dst string) bool {
	if _, ok := d.nodes[src]; !ok {
		return false
	}
	if _, ok := d.nodes[dst]; !ok {
		return false
	}
	return d.reachable(src, dst)
}

// TopoOrder devolve uma ordenação topológica ESTÁVEL e REPRODUZÍVEL do DAG
// (Kahn com desempate por task_id lexicográfico — NUNCA a ordem de um mapa Go).
// Para os mesmos nós/arestas o plano é IDÊNTICO, independentemente da ordem de
// admissão — é isto que torna o plano reconstruível por replay resume-from-step
// com ordem idêntica (ADR-010). Devolve erro se o grafo tiver um ciclo (não deve
// acontecer: a admissão é fail-closed), defesa-em-profundidade.
func (d *DAG) TopoOrder() ([]string, error) {
	indeg := make(map[string]int, len(d.nodes))
	for id, n := range d.nodes {
		indeg[id] = len(n.deps)
	}
	// Conjunto de prontos (in-degree 0), mantido ordenado lexicograficamente.
	ready := make([]string, 0)
	for id, deg := range indeg {
		if deg == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)

	order := make([]string, 0, len(d.nodes))
	for len(ready) > 0 {
		u := ready[0]
		ready = ready[1:]
		order = append(order, u)
		// Sucessores por ordem estável.
		succs := make([]string, 0, len(d.succ[u]))
		for v := range d.succ[u] {
			succs = append(succs, v)
		}
		sort.Strings(succs)
		for _, v := range succs {
			indeg[v]--
			if indeg[v] == 0 {
				ready = insertSorted(ready, v)
			}
		}
	}
	if len(order) != len(d.nodes) {
		return nil, fmt.Errorf("orchestrator: DAG contém ciclo (topo incompleta: %d de %d nós)", len(order), len(d.nodes))
	}
	return order, nil
}

// insertSorted insere v mantendo xs ordenado lexicograficamente.
func insertSorted(xs []string, v string) []string {
	i := sort.SearchStrings(xs, v)
	xs = append(xs, "")
	copy(xs[i+1:], xs[i:])
	xs[i] = v
	return xs
}

// MarkRunning transita um nó ready→running (o claim da máquina de estados de
// AOS-017). É a entrada em contenção activa sob lease: um nó em running pode ser
// escolhido como vítima de deadlock e abortado (running→failed). A validade é
// imposta pela tabela declarativa de AOS-017 (fail-closed). NOTA: o Orquestrador
// NÃO detém o fencing token do claim (isso é do Escalonador/AOS-018); ao nível do
// grafo só se regista a transição de estado por-nó, não o enforcement do lease.
func (d *DAG) MarkRunning(taskID string) error {
	_, err := d.transitionNode(taskID, state.Running)
	return err
}

// transitionNode aplica uma transição de estado a um nó, VALIDADA pela tabela
// declarativa da máquina de estados durável de AOS-017 (state.IsValidTransition).
// Um par fora da tabela é rejeitado fail-closed sem mutar o estado.
func (d *DAG) transitionNode(taskID string, to state.State) (from state.State, err error) {
	n, ok := d.nodes[taskID]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrNodeNotFound, taskID)
	}
	from = n.state
	if !state.IsValidTransition(from, to) {
		return from, fmt.Errorf("%w: %s→%s (nó %s)", state.ErrInvalidTransition, from, to, taskID)
	}
	n.state = to
	return from, nil
}

// Agent devolve a identidade não-humana (NHI + cadeia de delegação) do nó e se
// existe. Torna a identidade OBSERVÁVEL após persist+replay: RebuildDAG
// reconstrói-a de task.node.created, pelo que a NHI/cadeia sobrevive à
// reconstrução (critério de aceitação 6, ADR-003).
func (d *DAG) Agent(taskID string) (contract.AgentIdentity, bool) {
	n, ok := d.nodes[taskID]
	if !ok {
		return contract.AgentIdentity{}, false
	}
	return n.spec.Agent, true
}

// applyDurableState repõe o estado de um nó a partir de um facto DURÁVEL já
// committed (p.ex. deadlock.resolved), SEM revalidar a transição: o evento é a
// autoridade de que ela foi aplicada e o replay limita-se a reproduzir o estado
// terminal committed. Fail-closed contra estados forjados de um log corrompido
// (state.IsKnown). É usado só na reconstrução por [RebuildDAG].
func (d *DAG) applyDurableState(taskID string, to state.State) error {
	n, ok := d.nodes[taskID]
	if !ok {
		return fmt.Errorf("%w: %q", ErrNodeNotFound, taskID)
	}
	if !state.IsKnown(to) {
		return fmt.Errorf("orchestrator: estado durável desconhecido %q para o nó %q", to, taskID)
	}
	n.state = to
	return nil
}

// removeNode desfaz uma admissão de nó (revert de [GraphBuilder.AddNode] quando o
// Append falha), mantendo o DAG consistente com o log. Só um nó recém-admitido
// (sem arestas ainda) é removível; devolve o contador de sequência se for o
// último admitido, para que a próxima admissão bem-sucedida seja determinística.
func (d *DAG) removeNode(taskID string) {
	n, ok := d.nodes[taskID]
	if !ok {
		return
	}
	if n.seq == d.next-1 {
		d.next--
	}
	delete(d.nodes, taskID)
	delete(d.succ, taskID)
}

// removeEdge desfaz uma admissão de aresta (revert de [GraphBuilder.AddEdge]
// quando o Append falha).
func (d *DAG) removeEdge(from, to string) {
	if d.succ[from] != nil {
		delete(d.succ[from], to)
	}
	if n, ok := d.nodes[to]; ok {
		delete(n.deps, from)
	}
}

// restoreState repõe directamente o estado de um nó (revert de uma transição
// já mutada em memória quando o Append do facto falha). Não revalida — é o
// inverso exacto de uma transição acabada de aplicar.
func (d *DAG) restoreState(taskID string, to state.State) {
	if n, ok := d.nodes[taskID]; ok {
		n.state = to
	}
}

// ---------------------------------------------------------------------------
// Persistência durável do grafo como eventos append-only (ADR-007).
// ---------------------------------------------------------------------------

// EventStore é o subconjunto do Event Store (AOS-002) de que o Orquestrador
// depende para persistir e reconstruir o grafo. *eventstore.Store satisfá-lo.
type EventStore interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error)
}

// GraphBuilder constrói um DAG persistindo cada admissão como um evento
// append-only no Event Store (nós, arestas e rejeições de ciclo). É a
// materialização de "o grafo é persistido como eventos" (ADR-007) e a base do
// replay determinístico.
type GraphBuilder struct {
	store    EventStore
	producer eventstore.Producer
	dag      *DAG
	tracer   agentruntime.Tracer
}

// GraphOption configura um [GraphBuilder].
type GraphOption func(*GraphBuilder)

// WithGraphTracer injecta a porta de observabilidade (spans OTel GenAI da
// decomposição). Default: [agentruntime.NoopTracer].
func WithGraphTracer(t agentruntime.Tracer) GraphOption {
	return func(b *GraphBuilder) {
		if t != nil {
			b.tracer = t
		}
	}
}

// NewGraphBuilder constrói um GraphBuilder para o run dado. store e runID são
// obrigatórios; producer identifica a NHI emissora dos eventos do grafo.
func NewGraphBuilder(store EventStore, runID string, producer eventstore.Producer, opts ...GraphOption) (*GraphBuilder, error) {
	if store == nil {
		return nil, errors.New("orchestrator: event store nil")
	}
	if runID == "" {
		return nil, errors.New("orchestrator: run_id vazio")
	}
	b := &GraphBuilder{store: store, producer: producer, dag: NewDAG(runID), tracer: agentruntime.NoopTracer{}}
	for _, opt := range opts {
		opt(b)
	}
	if b.tracer == nil {
		b.tracer = agentruntime.NoopTracer{}
	}
	return b, nil
}

// DAG devolve o DAG em construção (leitura; a topo é reconstruível por replay).
func (b *GraphBuilder) DAG() *DAG { return b.dag }

// AddNode admite um nó no DAG e persiste task.node.created. A idempotência é por
// passo (step_id = node:<task_id>): reemitir o mesmo nó é deduplicado pelo Event
// Store, nunca duplicado. Abre um span invoke_agent da decomposição do nó (custo
// por span + atributos de decomposição).
//
// DURABILIDADE FAIL-CLOSED: se o Append falhar, a admissão é REVERTIDA em memória
// (o nó não fica no DAG sem o facto correspondente no log), para que o grafo vivo
// nunca divirja do log e sobreviva a RebuildDAG.
func (b *GraphBuilder) AddNode(ctx context.Context, spec NodeSpec) error {
	if err := b.dag.AddNode(spec); err != nil {
		return err
	}
	stepID := contract.StepNodeCreated(spec.TaskID)
	ctx, span := startNodeSpan(ctx, b.tracer, b.dag.runID, stepID, spec)
	defer span.End()
	payload := contract.TaskNodeCreatedPayload{
		RunID:      b.dag.runID,
		TaskID:     spec.TaskID,
		State:      string(state.Ready),
		Prio:       spec.Priority,
		Agent:      spec.Agent,
		ToolID:     spec.Task.ToolID,
		Capability: spec.Task.Capability,
	}
	st, err := b.emit(ctx, contract.EventTaskNodeCreated, stepID, payload)
	if err != nil {
		b.dag.removeNode(spec.TaskID) // revert: mantém o DAG consistente com o log
		return err
	}
	if st == eventstore.StatusDuplicate {
		// O log já tinha este nó e a memória tratou-o como novo: builder cego sobre um
		// run existente. NÃO reverte — memória e log acabaram de concordar; quem está
		// errado é o chamador. Ver [ErrLogAhead].
		return fmt.Errorf("%w: %s %q", ErrLogAhead, contract.EventTaskNodeCreated, spec.TaskID)
	}
	return nil
}

// MarkRunning transita um nó ready→running (o claim da máquina de AOS-017) E
// persiste a transição como task.node.state_changed, para que o estado de
// execução por-nó seja DURÁVEL e reconstruível por replay (sem este facto,
// RebuildDAG reporia o nó como ready). A validade da transição é imposta pela
// tabela declarativa de AOS-017 (fail-closed). Se o Append falhar, a transição é
// revertida em memória (consistência DAG↔log).
func (b *GraphBuilder) MarkRunning(ctx context.Context, taskID string) error {
	from, err := b.dag.transitionNode(taskID, state.Running)
	if err != nil {
		return err
	}
	payload := contract.TaskNodeStateChangedPayload{
		RunID:  b.dag.runID,
		TaskID: taskID,
		From:   string(from),
		To:     string(state.Running),
	}
	step := contract.StepNodeStateChanged(taskID, string(state.Running))
	st, eerr := b.emit(ctx, contract.EventTaskNodeStateChanged, step, payload)
	if eerr != nil {
		b.dag.restoreState(taskID, from) // revert: transição não durável
		return eerr
	}
	if st == eventstore.StatusDuplicate {
		// Idem [GraphBuilder.AddNode]: o log já tinha esta transição. Sem isto, um nó
		// abortado por política (running→failed pelo detector de deadlock) voltava a
		// `running` num builder retomado, com ZERO eventos novos a registá-lo.
		return fmt.Errorf("%w: %s %q→%s", ErrLogAhead, contract.EventTaskNodeStateChanged, taskID, state.Running)
	}
	return nil
}

// AddEdge admite a aresta From→To (To depende de From). Se fechar um ciclo,
// REJEITA na admissão (fail-closed): persiste task.edge.rejected_cycle com a
// razão explícita e devolve [ErrEdgeClosesCycle] SEM alterar o grafo. Caso
// contrário adiciona a aresta e persiste task.edge.added.
func (b *GraphBuilder) AddEdge(ctx context.Context, from, to string) error {
	// JÁ EXISTIA? A pergunta tem de ser feita ANTES, e é o que fecha o defeito abaixo.
	//
	// [DAG.AddEdge] devolve nil em DOIS casos que o builder tratava como um só: quando
	// acabou de adicionar a aresta, e quando ela JÁ LÁ ESTAVA (curto-circuito
	// idempotente, que NÃO muta). Em erro do `emit`, o revert corria nos dois — e no
	// segundo removia da memória uma aresta que estava DURÁVEL no log.
	//
	// Medido a 2026-08-30 com o Event Store REAL: o vivo perdia a→b, o log mantinha-a, e
	// a inversa b→a deixava de parecer um ciclo, era admitida e persistida. O log ficava
	// com as duas e o [RebuildDAG] falhava para sempre. A `TopoOrder` viva passava a
	// devolver uma ordem que VIOLA uma dependência ainda durável no log.
	//
	// O gatilho não é uma falha genérica de Append — o store deduplica antes de tudo e
	// devolve nil. São as verificações que correm ANTES da dedup: ctx cancelado, store
	// fechado, sem líder.
	jaExistia := b.dag.HasEdge(from, to)
	err := b.dag.AddEdge(from, to)
	if errors.Is(err, ErrEdgeClosesCycle) {
		rej := contract.EdgeRejectedCyclePayload{
			RunID:  b.dag.runID,
			From:   from,
			To:     to,
			Reason: err.Error(),
		}
		if _, perr := b.emit(ctx, contract.EventEdgeRejectedCycle, contract.StepEdgeRejected(from, to), rej); perr != nil {
			// O registo da rejeição falhou, mas a aresta CONTINUA rejeitada por
			// ciclo: preserva o sentinel para o chamador (errors.Is(…,
			// ErrEdgeClosesCycle) continua verdadeiro) sem mascarar o erro de infra.
			return errors.Join(err, perr)
		}
		return err
	}
	if err != nil {
		return err
	}
	ctx, span := b.tracer.StartSpan(ctx, opAddEdge)
	span.SetAttribute(agentruntime.AttrOperationName, opAddEdge)
	span.SetAttribute(agentruntime.AttrRunID, b.dag.runID)
	span.SetAttribute(attrEdgeFrom, from)
	span.SetAttribute(attrEdgeTo, to)
	defer span.End()
	added := contract.TaskEdgeAddedPayload{RunID: b.dag.runID, From: from, To: to}
	st, eerr := b.emit(ctx, contract.EventTaskEdgeAdded, contract.StepEdgeAdded(from, to), added)
	if eerr != nil {
		if !jaExistia {
			// Só reverte o que ESTA chamada adicionou. Reverter uma aresta pré-existente
			// removeria da memória algo que está durável no log — era o defeito.
			b.dag.removeEdge(from, to)
		}
		return eerr
	}
	if st == eventstore.StatusDuplicate && !jaExistia {
		// A memória tratou a aresta como nova e o log já a tinha: builder cego. Com a
		// aresta já em memória a coisa é outra — é reemissão idempotente legítima, e
		// devolve nil como sempre.
		return fmt.Errorf("%w: %s %s→%s", ErrLogAhead, contract.EventTaskEdgeAdded, from, to)
	}
	return nil
}

// TopoOrder devolve a ordenação topológica estável do DAG em construção.
func (b *GraphBuilder) TopoOrder() ([]string, error) { return b.dag.TopoOrder() }

// emit serializa payload e escreve-o como evento no stream = run_id, e DEVOLVE O
// ESTADO do append.
//
// Devolver o status não é arrumação: era o descarte dele que tornava o
// [StatusDuplicate] — que vem com erro NIL — indistinguível de um commit fresco, e é
// isso que [ErrLogAhead] passa a denunciar. O irmão `DeadlockDetector.emit` já o
// consumia para gatear efeitos; este não.
func (b *GraphBuilder) emit(ctx context.Context, evType, stepID string, payload any) (eventstore.Status, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	res, err := b.store.Append(ctx, b.dag.runID, eventstore.EventInput{
		Type:     evType,
		Payload:  raw,
		RunID:    b.dag.runID,
		StepID:   stepID,
		Producer: b.producer,
	})
	return res.Status, err
}

// RebuildDAG reconstrói o DAG de um run RELENDO os eventos de grafo do Event
// Store (por ordem de seq) e reproduzindo TANTO a topologia COMO o estado de
// execução por-nó:
//
//   - task.node.created  → admite o nó (ready) e reconstrói a AgentIdentity;
//   - task.edge.added    → admite a aresta de dependência;
//   - task.node.state_changed → reaplica a transição de estado do nó (p.ex.
//     ready→running), validada pela tabela de AOS-017;
//   - deadlock.resolved  → repõe o estado TERMINAL da vítima (VictimTo, p.ex.
//     failed) a partir do facto durável committed — sem o qual a resolução de
//     deadlock se perderia num crash entre o commit e a aplicação dos efeitos.
//
// Ignora as rejeições de ciclo (nunca alteraram o grafo) e deadlock.detected. O
// DAG reconstruído iguala o vivo em topologia (TopoOrder IDÊNTICA, ADR-010) E no
// estado de execução por-nó. NOTA: a libertação de leases da vítima é externa ao
// DAG — o ResourceLedger é reconstruído pelos eventos de lease do Escalonador
// (fora de AOS-025); aqui reconstrói-se o estado do NÓ, que é o que o DAG detém.
//
// Fail-closed: um evento de grafo corrompido (payload ilegível), uma aresta que
// — contra o invariante — feche um ciclo, ou uma transição/estado durável
// inválido abortam a reconstrução em vez de adoptar um grafo inválido.
func RebuildDAG(ctx context.Context, store EventStore, runID string) (*DAG, error) {
	if store == nil {
		return nil, errors.New("orchestrator: event store nil")
	}
	events, err := store.Read(ctx, runID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return NewDAG(runID), nil
		}
		return nil, err
	}
	d := NewDAG(runID)
	for i := range events {
		ev := events[i]
		switch ev.Type {
		case contract.EventTaskNodeCreated:
			var p contract.TaskNodeCreatedPayload
			if uerr := json.Unmarshal(ev.Payload, &p); uerr != nil {
				return nil, fmt.Errorf("orchestrator: replay node (seq=%d): %w", ev.Seq, uerr)
			}
			if aerr := d.AddNode(NodeSpec{
				TaskID:   p.TaskID,
				Priority: p.Prio,
				Agent:    p.Agent,
				Task:     contract.TaskSpec{ToolID: p.ToolID, Capability: p.Capability},
			}); aerr != nil {
				return nil, fmt.Errorf("orchestrator: replay node (seq=%d): %w", ev.Seq, aerr)
			}
		case contract.EventTaskEdgeAdded:
			var p contract.TaskEdgeAddedPayload
			if uerr := json.Unmarshal(ev.Payload, &p); uerr != nil {
				return nil, fmt.Errorf("orchestrator: replay edge (seq=%d): %w", ev.Seq, uerr)
			}
			if aerr := d.AddEdge(p.From, p.To); aerr != nil {
				return nil, fmt.Errorf("orchestrator: replay edge (seq=%d): %w", ev.Seq, aerr)
			}
		case contract.EventTaskNodeStateChanged:
			var p contract.TaskNodeStateChangedPayload
			if uerr := json.Unmarshal(ev.Payload, &p); uerr != nil {
				return nil, fmt.Errorf("orchestrator: replay state (seq=%d): %w", ev.Seq, uerr)
			}
			if _, aerr := d.transitionNode(p.TaskID, state.State(p.To)); aerr != nil {
				return nil, fmt.Errorf("orchestrator: replay state (seq=%d): %w", ev.Seq, aerr)
			}
		case contract.EventDeadlockResolved:
			var p contract.DeadlockResolvedPayload
			if uerr := json.Unmarshal(ev.Payload, &p); uerr != nil {
				return nil, fmt.Errorf("orchestrator: replay deadlock.resolved (seq=%d): %w", ev.Seq, uerr)
			}
			// Repõe o estado TERMINAL da vítima a partir do facto durável committed
			// (autoritativo — não se revalida a transição; o replay reproduz o
			// estado committed). É o que evita que a resolução se perca num crash.
			if aerr := d.applyDurableState(p.Victim, state.State(p.VictimTo)); aerr != nil {
				return nil, fmt.Errorf("orchestrator: replay deadlock.resolved (seq=%d): %w", ev.Seq, aerr)
			}
		default:
			// Ignora rejeições de ciclo (task.edge.rejected_cycle), deadlock.detected
			// e quaisquer outros factos do run.
		}
	}
	return d, nil
}
