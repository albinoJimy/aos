package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/aos-ref/control-plane/orchestrator/contract"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// PolicyAbortVictim é o rótulo da política de resolução determinística: abortar a
// vítima de MENOR prioridade da espera circular e libertar os seus recursos. Ver
// [selectVictim] para o critério de desempate estável.
const PolicyAbortVictim = "abort_lowest_priority_victim"

// WaitForGraph é o GRAFO DE ESPERA (wait-for graph) sobre recursos partilhados
// (leases, filas, orçamento): uma aresta Waiter→Holder significa que Waiter está
// bloqueado à espera de um recurso detido por Holder. Um ciclo é uma espera
// circular (deadlock). É PURO — sem I/O.
type WaitForGraph struct {
	waits map[string]map[string]bool // waiter -> {holder}
	nodes map[string]bool
}

// NewWaitForGraph constrói um wait-for graph vazio.
func NewWaitForGraph() *WaitForGraph {
	return &WaitForGraph{waits: make(map[string]map[string]bool), nodes: make(map[string]bool)}
}

// AddWait regista que waiter espera por um recurso detido por holder (aresta
// Waiter→Holder). Auto-esperas (waiter==holder) são ignoradas.
func (w *WaitForGraph) AddWait(waiter, holder string) {
	if waiter == "" || holder == "" || waiter == holder {
		return
	}
	if w.waits[waiter] == nil {
		w.waits[waiter] = make(map[string]bool)
	}
	w.waits[waiter][holder] = true
	w.nodes[waiter] = true
	w.nodes[holder] = true
}

// FindCycle procura uma espera circular e devolve o CONJUNTO de tarefas do ciclo
// ordenado lexicograficamente (determinístico), ou nil se o grafo for acíclico.
// A procura é DFS com marcação a três cores; a raiz é iterada por ordem estável
// para que, havendo vários ciclos, o resultado seja reprodutível.
func (w *WaitForGraph) FindCycle() []string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(w.nodes))

	// Raízes por ordem estável.
	roots := make([]string, 0, len(w.nodes))
	for n := range w.nodes {
		roots = append(roots, n)
	}
	sort.Strings(roots)

	var stack []string
	// dfs devolve o conjunto do ciclo assim que o encontrar.
	var dfs func(u string) []string
	dfs = func(u string) []string {
		color[u] = gray
		stack = append(stack, u)
		// Vizinhos por ordem estável.
		nbrs := make([]string, 0, len(w.waits[u]))
		for v := range w.waits[u] {
			nbrs = append(nbrs, v)
		}
		sort.Strings(nbrs)
		for _, v := range nbrs {
			switch color[v] {
			case gray:
				// Ciclo: extrai o segmento da pilha desde v até ao topo.
				return cycleSet(stack, v)
			case white:
				if cyc := dfs(v); cyc != nil {
					return cyc
				}
			}
		}
		color[u] = black
		stack = stack[:len(stack)-1]
		return nil
	}

	for _, r := range roots {
		if color[r] == white {
			stack = stack[:0]
			if cyc := dfs(r); cyc != nil {
				return cyc
			}
		}
	}
	return nil
}

// cycleSet extrai o conjunto ordenado de nós do ciclo: o sufixo da pilha desde a
// primeira ocorrência de start até ao topo.
func cycleSet(stack []string, start string) []string {
	idx := 0
	for i, s := range stack {
		if s == start {
			idx = i
			break
		}
	}
	seg := make([]string, 0, len(stack)-idx)
	seen := make(map[string]bool)
	for _, s := range stack[idx:] {
		if !seen[s] {
			seen[s] = true
			seg = append(seg, s)
		}
	}
	sort.Strings(seg)
	return seg
}

// cycleKey deriva uma chave estável e determinística de um conjunto de tarefas
// (já ordenado): serve de sufixo à idempotency_key dos eventos de deadlock, para
// que o MESMO deadlock produza o MESMO step_id — reemissões são deduplicadas.
func cycleKey(tasks []string) string { return strings.Join(tasks, "|") }

// ---------------------------------------------------------------------------
// Ledger de recursos + detector/resolvedor de deadlock.
// ---------------------------------------------------------------------------

// ResourceLedger regista quem DETÉM e quem ESPERA por cada recurso partilhado
// (leases, filas, orçamento). É a fonte a partir da qual se deriva o wait-for
// graph. É PURO — sem I/O.
type ResourceLedger struct {
	holder map[string]string          // recurso -> task_id que o detém
	waits  map[string]map[string]bool // task_id -> {recursos que espera}
}

// NewResourceLedger constrói um ledger vazio.
func NewResourceLedger() *ResourceLedger {
	return &ResourceLedger{holder: make(map[string]string), waits: make(map[string]map[string]bool)}
}

// Acquire regista que task passa a deter resource (idempotente).
func (l *ResourceLedger) Acquire(task, resource string) {
	l.holder[resource] = task
	// Adquirir um recurso satisfaz uma espera pendente por ele.
	if l.waits[task] != nil {
		delete(l.waits[task], resource)
	}
}

// Wait regista que task fica bloqueada à espera de resource (idempotente).
func (l *ResourceLedger) Wait(task, resource string) {
	if l.waits[task] == nil {
		l.waits[task] = make(map[string]bool)
	}
	l.waits[task][resource] = true
}

// Release liberta todos os recursos detidos por task e limpa as suas esperas.
// Idempotente: chamar duas vezes não tem efeito adicional.
func (l *ResourceLedger) Release(task string) []string {
	freed := make([]string, 0)
	for res, h := range l.holder {
		if h == task {
			freed = append(freed, res)
		}
	}
	sort.Strings(freed)
	for _, res := range freed {
		delete(l.holder, res)
	}
	delete(l.waits, task)
	return freed
}

// buildWaitFor deriva o wait-for graph do ledger: para cada task que espera um
// recurso detido por outra, cria a aresta Waiter→Holder.
func (l *ResourceLedger) buildWaitFor() *WaitForGraph {
	w := NewWaitForGraph()
	// Iteração determinística por waiter e recurso ordenados.
	waiters := make([]string, 0, len(l.waits))
	for t := range l.waits {
		waiters = append(waiters, t)
	}
	sort.Strings(waiters)
	for _, waiter := range waiters {
		resources := make([]string, 0, len(l.waits[waiter]))
		for r := range l.waits[waiter] {
			resources = append(resources, r)
		}
		sort.Strings(resources)
		for _, res := range resources {
			if holder, ok := l.holder[res]; ok {
				w.AddWait(waiter, holder)
			}
		}
	}
	return w
}

// Resolution é o resultado de uma resolução de deadlock.
type Resolution struct {
	// Tasks é o conjunto de tarefas na espera circular (ordenado).
	Tasks []string
	// Victim é a tarefa abortada pela política.
	Victim string
	// ReleasedResources são os recursos libertados pela vítima (ordenado).
	ReleasedResources []string
	// VictimFrom/VictimTo é a transição de estado aplicada ao nó vítima.
	VictimFrom state.State
	VictimTo   state.State
	// Applied indica se os EFEITOS (libertação + transição) foram aplicados nesta
	// chamada. É false num replay/reentrada em que o deadlock.resolved já era
	// durável (duplicado): os efeitos não são reaplicados (idempotência, ADR-001).
	Applied bool
}

// DeadlockDetector detecta esperas circulares sobre recursos e aplica a política
// de resolução determinística, persistindo deadlock.detected/deadlock.resolved
// como eventos append-only e integrando a máquina de estados de AOS-017 na
// transição do nó vítima. Coordena o DAG (prioridade/ordem dos nós), o
// ResourceLedger (recursos) e o Event Store (durabilidade).
type DeadlockDetector struct {
	dag      *DAG
	ledger   *ResourceLedger
	store    EventStore
	producer eventstore.Producer
	tracer   agentruntime.Tracer
}

// DetectorOption configura um [DeadlockDetector].
type DetectorOption func(*DeadlockDetector)

// WithDetectorTracer injecta a porta de observabilidade (span da resolução de
// deadlock). Default: [agentruntime.NoopTracer].
func WithDetectorTracer(t agentruntime.Tracer) DetectorOption {
	return func(dd *DeadlockDetector) {
		if t != nil {
			dd.tracer = t
		}
	}
}

// NewDeadlockDetector constrói o detector. dag, ledger e store são obrigatórios.
func NewDeadlockDetector(dag *DAG, ledger *ResourceLedger, store EventStore, producer eventstore.Producer, opts ...DetectorOption) (*DeadlockDetector, error) {
	if dag == nil || ledger == nil || store == nil {
		return nil, errors.New("orchestrator: detector de deadlock exige dag, ledger e store")
	}
	dd := &DeadlockDetector{dag: dag, ledger: ledger, store: store, producer: producer, tracer: agentruntime.NoopTracer{}}
	for _, opt := range opts {
		opt(dd)
	}
	if dd.tracer == nil {
		dd.tracer = agentruntime.NoopTracer{}
	}
	return dd, nil
}

// DetectAndResolve procura uma espera circular; se existir, emite
// deadlock.detected com o conjunto de tarefas, escolhe a vítima pela política
// determinística, e emite deadlock.resolved. Os EFEITOS (libertar recursos da
// vítima + transitar o nó vítima na máquina de estados) só são aplicados se o
// evento deadlock.resolved for COMMITTED — num duplicado (mesma espera circular
// já resolvida, p.ex. em replay) os efeitos NÃO são reaplicados: idempotência por
// passo, sem efeitos duplicados (ADR-001).
//
// Devolve (nil, nil) se não houver deadlock.
//
// UM CICLO POR CHAMADA: [WaitForGraph.FindCycle] devolve um único ciclo, logo esta
// chamada resolve exactamente um. Se puderem coexistir vários deadlocks disjuntos,
// use [DeadlockDetector.DetectAndResolveAll] (itera até (nil,nil)) ou chame esta
// em ciclo até devolver (nil,nil).
func (dd *DeadlockDetector) DetectAndResolve(ctx context.Context) (*Resolution, error) {
	wf := dd.ledger.buildWaitFor()
	tasks := wf.FindCycle()
	if tasks == nil {
		return nil, nil
	}
	key := cycleKey(tasks)

	// POLÍTICA TOTAL: escolhe a vítima ABORTÁVEL (running→failed válido) ANTES de
	// emitir qualquer facto. Se nenhuma tarefa do ciclo puder abortar, falha SEM
	// deixar um deadlock.detected órfão (sem "detected sem resolved" durável).
	victim, from, ok := dd.selectVictim(tasks)
	if !ok {
		return nil, fmt.Errorf("%w: espera circular %v sem vítima abortável (nenhum nó em running)", state.ErrInvalidTransition, tasks)
	}
	const victimTarget = state.Failed

	// Span da resolução (custo por span + atributos do deadlock).
	ctx, span := dd.tracer.StartSpan(ctx, opResolveDeadlock)
	span.SetAttribute(agentruntime.AttrOperationName, opResolveDeadlock)
	span.SetAttribute(agentruntime.AttrRunID, dd.dag.runID)
	span.SetAttribute(attrDeadlockTaskCount, len(tasks))
	span.SetAttribute(attrDeadlockVictim, victim)
	span.SetAttribute(attrDeadlockPolicy, PolicyAbortVictim)
	span.SetAttribute(agentruntime.AttrCostUSD, 0.0)
	defer span.End()

	// 1) deadlock.detected — o conjunto de tarefas envolvidas.
	detected := contract.DeadlockDetectedPayload{
		RunID:     dd.dag.runID,
		Tasks:     tasks,
		Resources: dd.disputedResources(tasks),
	}
	if err := dd.emit(ctx, contract.EventDeadlockDetected, contract.StepDeadlockDetected(key), detected, nil); err != nil {
		return nil, err
	}

	freed := dd.heldBy(victim)
	resolved := contract.DeadlockResolvedPayload{
		RunID:             dd.dag.runID,
		Tasks:             tasks,
		Victim:            victim,
		Policy:            PolicyAbortVictim,
		ReleasedResources: freed,
		VictimFrom:        string(from),
		VictimTo:          string(victimTarget),
	}

	// 2) deadlock.resolved — os efeitos são gated pelo status do Append.
	var status eventstore.Status
	if err := dd.emit(ctx, contract.EventDeadlockResolved, contract.StepDeadlockResolved(key), resolved, &status); err != nil {
		return nil, err
	}

	res := &Resolution{
		Tasks:             tasks,
		Victim:            victim,
		ReleasedResources: freed,
		VictimFrom:        from,
		VictimTo:          victimTarget,
	}

	// 3) Efeitos APENAS se o facto foi committed agora (não num duplicado). É o
	// que garante "sem efeitos duplicados" perante retries/replay: o Event Store
	// deduplica a idempotency_key e nós não reaplicamos a libertação/transição.
	// (Num crash entre o commit e este ponto, o estado terminal da vítima é
	// reconstruído por RebuildDAG ao reprocessar deadlock.resolved.)
	if status == eventstore.StatusCommitted {
		dd.ledger.Release(victim)
		if _, terr := dd.dag.transitionNode(victim, victimTarget); terr != nil {
			return nil, terr
		}
		res.Applied = true
	}
	return res, nil
}

// DetectAndResolveAll resolve TODOS os deadlocks disjuntos correntes, chamando
// [DeadlockDetector.DetectAndResolve] até não haver ciclo (fixpoint). Devolve as
// resoluções por ordem de resolução. Pára cedo se uma resolução for um DUPLICADO
// (Applied=false): nesse caso os efeitos não foram reaplicados, o ledger não
// mudou e insistir entraria em ciclo infinito sobre a mesma espera — a
// reconstrução do estado é responsabilidade do replay (RebuildDAG), não de um
// re-loop.
func (dd *DeadlockDetector) DetectAndResolveAll(ctx context.Context) ([]*Resolution, error) {
	var out []*Resolution
	for {
		res, err := dd.DetectAndResolve(ctx)
		if err != nil {
			return out, err
		}
		if res == nil {
			return out, nil
		}
		out = append(out, res)
		if !res.Applied {
			return out, nil
		}
	}
}

// disputedResources devolve, ordenados, os recursos detidos por tarefas do ciclo
// (os recursos efectivamente em disputa na espera circular).
func (dd *DeadlockDetector) disputedResources(tasks []string) []string {
	inCycle := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		inCycle[t] = true
	}
	out := make([]string, 0)
	for res, h := range dd.ledger.holder {
		if inCycle[h] {
			out = append(out, res)
		}
	}
	sort.Strings(out)
	return out
}

// heldBy devolve, ordenados, os recursos detidos por task (sem os libertar).
func (dd *DeadlockDetector) heldBy(task string) []string {
	out := make([]string, 0)
	for res, h := range dd.ledger.holder {
		if h == task {
			out = append(out, res)
		}
	}
	sort.Strings(out)
	return out
}

// selectVictim aplica a política de resolução DETERMINÍSTICA sobre as tarefas do
// ciclo e devolve a PRIMEIRA vítima ABORTÁVEL (running→failed válido na tabela de
// AOS-017) por ordem de preferência, o seu estado de origem, e ok=false se
// nenhuma tarefa do ciclo puder abortar (política TOTAL sobre estados não-running:
// não deixa o deadlock por resolver a meio nem emite um detected órfão).
//
// Ordem de preferência da vítima (desempate ESTÁVEL e documentado):
//
//  1. MENOR prioridade (spec.Priority) — abortar o trabalho menos importante;
//  2. empate → MAIS RECENTE (maior seq de admissão) — preservar o progresso mais
//     antigo, abortar o que investiu menos;
//  3. empate → task_id lexicograficamente MAIOR — critério final totalmente
//     determinístico, independente de qualquer ordem de mapa Go.
//
// Se a vítima preferida não for abortável, passa-se à seguinte candidata. O
// resultado é função pura dos atributos/estado dos nós, logo idêntico em replay.
func (dd *DeadlockDetector) selectVictim(tasks []string) (victim string, from state.State, ok bool) {
	ordered := make([]string, len(tasks))
	copy(ordered, tasks)
	sort.SliceStable(ordered, func(i, j int) bool {
		return dd.morePreferredVictim(ordered[i], ordered[j])
	})
	for _, t := range ordered {
		st, exists := dd.dag.State(t)
		if !exists {
			continue
		}
		if state.IsValidTransition(st, state.Failed) {
			return t, st, true
		}
	}
	return "", "", false
}

// morePreferredVictim devolve true sse a deve ser abortada ANTES de b pela
// política (menor prioridade; empate → mais recente; empate → task_id maior).
func (dd *DeadlockDetector) morePreferredVictim(a, b string) bool {
	na, nb := dd.dag.nodes[a], dd.dag.nodes[b]
	if na == nil {
		return false
	}
	if nb == nil {
		return true
	}
	if na.spec.Priority != nb.spec.Priority {
		return na.spec.Priority < nb.spec.Priority
	}
	if na.seq != nb.seq {
		return na.seq > nb.seq
	}
	return a > b
}

// emit serializa payload e escreve-o como evento no stream = run_id. Se statusOut
// não for nil, devolve nele o Status do Append (committed/duplicate) — usado para
// gatilhar efeitos idempotentes apenas em commits novos.
func (dd *DeadlockDetector) emit(ctx context.Context, evType, stepID string, payload any, statusOut *eventstore.Status) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	res, err := dd.store.Append(ctx, dd.dag.runID, eventstore.EventInput{
		Type:     evType,
		Payload:  raw,
		RunID:    dd.dag.runID,
		StepID:   stepID,
		Producer: dd.producer,
	})
	if err != nil {
		return err
	}
	if statusOut != nil {
		*statusOut = res.Status
	}
	return nil
}
