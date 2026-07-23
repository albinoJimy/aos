// AOS-164a — o LOOP DE SERVIÇO do nó `aos`. O bootstrap de AOS-163 compõe a cadeia
// REAL mas corre UM run síncrono (one-shot). Este ficheiro gradua o nó para
// LONG-RUNNING: um [NodeService] que aceita submissões de run, hospeda MÚLTIPLOS runs
// concorrentemente, isola a falha de cada run (panic-recovery por-run) e encerra
// graciosamente (drena os em curso, nunca os mata cegamente — AOS-023, e liberta os
// leases).
//
// FRONTEIRA de escopo (AOS-164a vs 164b/170):
//
//   - A POSSE de um run é por LEASE DURÁVEL, REUTILIZANDO [worker.Assigner] sobre um
//     [durable.LeaseManager] construído sobre o Event Store do nó — NÃO se reinventa o
//     escalonador nem o mecanismo de lease/fencing. Um run cujo lease é detido por outra
//     réplica NÃO é roubado (sem coordenação intra-processo).
//   - A PERSISTÊNCIA durável do shutdown e o substrato durável são AOS-164b/AOS-170
//     (deferidos): aqui o substrato é o Event Store de REFERÊNCIA do nó. O drain é
//     cooperativo (cancelamento de contexto na fronteira de fim-de-turno), nunca um kill
//     cego.
//   - NÃO há rede aqui (a API HTTP é AOS-166); NÃO há fronteira nó↔ORQ/SCH (AOS-164b).
//     O NodeService é a superfície in-process que a CLI/API conduzirão.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/worker"
)

// DefaultLeaseTTL é o TTL do lease de posse de um run quando não configurado. Um run é
// possuído enquanto o lease durar; a posse é RENOVADA por heartbeat enquanto o run corre
// (ver [NodeService.heartbeat]), e só expira por TTL quando o detentor deixa de renovar
// (a liveness é por lease/TTL, nunca por PID — AOS-018).
const DefaultLeaseTTL = 2 * time.Minute

// DefaultCompletedRetention limita quantos desfechos de runs TERMINADOS o serviço retém
// em memória (para [NodeService.Wait]/[NodeService.Outcome]/inspecção). Num loop de
// serviço long-running o registo de terminados cresceria sem limite; ao exceder o limite
// os desfechos MAIS ANTIGOS são podados (FIFO). <= 0 desliga a poda (retenção ilimitada).
const DefaultCompletedRetention = 1024

// Erros do loop de serviço (todos fail-closed).
var (
	// ErrServiceShuttingDown — [NodeService.Submit] recusou porque o shutdown gracioso
	// já começou: o nó PARA de aceitar novos runs (drena os em curso). Fail-closed.
	ErrServiceShuttingDown = errors.New("aos: no em shutdown — nao aceita novos runs")

	// ErrRunAlreadyInProgress — submeteu-se um RunID JÁ hospedado (em curso) por esta
	// réplica. O registo por RunID recusa a duplicação (não se hospeda o mesmo run duas
	// vezes).
	ErrRunAlreadyInProgress = errors.New("aos: run ja em curso nesta replica (RunID duplicado)")

	// ErrRunAlreadyCompleted — submeteu-se um RunID cujo desfecho ESTA réplica já retém
	// como TERMINADO. A re-submissão de um run terminado é recusada explicitamente
	// (fail-closed) enquanto o desfecho estiver retido — em vez de re-executar e
	// SOBRESCREVER silenciosamente o desfecho anterior, ou de a mapear ao enganador
	// [ErrRunLeaseHeldElsewhere] (o lease residual é DESTA réplica, não de outra). Após
	// a poda do desfecho (ver [DefaultCompletedRetention]) o RunID volta a ser submetível.
	ErrRunAlreadyCompleted = errors.New("aos: run ja terminado nesta replica (desfecho retido) — re-submissao recusada")

	// ErrRunLeaseHeldElsewhere — o lease do run é detido por OUTRA réplica (lease vivo):
	// o run NÃO é hospedado aqui (sem roubo de partição — AOS-018/AC3). É a resposta
	// legítima a uma submissão de um run que já corre noutro lado.
	ErrRunLeaseHeldElsewhere = errors.New("aos: lease do run detido por outra replica — run nao hospedado (sem roubo)")

	// ErrEmptyRunID — o goal submetido não tem RunID (é a chave de registo e de lease).
	ErrEmptyRunID = errors.New("aos: goal sem RunID")

	// ErrRunPanicked — sentinela que embrulha o valor de um panic recuperado num run. O
	// panic de um run é CAPTURADO (isolamento por-run): não derruba o nó nem os outros
	// runs; o run é marcado falhado com este erro.
	ErrRunPanicked = errors.New("aos: run entrou em panic (isolado — no continua)")
)

// runState é o estado de UM run hospedado, protegido pelo mutex do [NodeService]. É
// criado no Submit (reserva o RunID), preenchido com o lease e o cancel quando o run é
// hospedado, e move-se de `runs` para `completed` quando termina.
type runState struct {
	runID  string
	lease  durable.Lease
	cancel context.CancelFunc
	done   chan struct{} // fechado quando o run termina e sai do registo de em-curso

	// outcome — escrito UMA vez sob o mutex do serviço, antes de close(done).
	result   agentruntime.Result
	err      error
	panicked bool
}

// RunOutcome é a fotografia imutável do desfecho de um run terminado (inspecção/testes).
type RunOutcome struct {
	RunID    string
	Result   agentruntime.Result
	Err      error
	Panicked bool
}

// NodeService é o LOOP DE SERVIÇO long-running do nó `aos` (AOS-164a). Envolve um [Node]
// (a cadeia REAL de AOS-163) e:
//
//   - ACEITA submissões de run ([Submit]) e HOSPEDA vários concorrentemente;
//   - REGISTA os runs em curso por RunID (mapa protegido por mutex, concorrente-seguro);
//   - possui cada run por LEASE DURÁVEL via [worker.Assigner] (sem roubo entre réplicas);
//   - ISOLA a falha por-run (cada run corre numa goroutine com panic-recovery);
//   - encerra graciosamente ([Shutdown]): para de aceitar, drena os em curso (ou
//     cancela-os limpo no deadline do ctx — nunca mata cego), liberta os leases.
//
// Seguro para uso concorrente: Submit, Shutdown e a conclusão de um run correm em
// goroutines diferentes e coordenam-se pelo mutex + WaitGroup.
type NodeService struct {
	node     *Node
	assigner *worker.Assigner
	leases   *durable.LeaseManager
	logw     io.Writer

	hbInterval   time.Duration // período de renovação (heartbeat) da posse; <= 0 desliga
	completedCap int           // teto de desfechos retidos (FIFO); <= 0 = ilimitado

	mu             sync.Mutex
	runs           map[string]*runState // em curso (por RunID)
	completed      map[string]*runState // terminados (inspecção/observabilidade)
	completedOrder []string             // ordem de término (FIFO) para poda do `completed`
	closed         bool                 // shutdown iniciado — recusa novos runs (governa admissão, sob mu)
	wg             sync.WaitGroup       // conta as goroutines de run hospedadas

	// draining ESPELHA `closed` para a sonda de prontidão: é armado sob `mu` no MESMO
	// ponto onde `closed=true` (fonte de verdade única e monotónica — o drain nunca
	// reverte), mas é LIDO lock-free por [Draining] (/readyz) sem tocar em `mu`. Assim a
	// sonda pode ser sondada à frequência de um probe sem contender com o mutex que
	// serializa a admissão/conclusão de runs — simétrico a [eventstore.Store.Healthy],
	// que foi feito atómico pela mesma razão. `closed` continua a governar a admissão
	// sob `mu` (a secção crítica closed+registo que impede um run escapar ao Shutdown
	// fica intacta); só a LEITURA da sonda deixa de adquirir o lock.
	draining atomic.Bool
}

// NodeServiceOption configura o [NodeService].
type NodeServiceOption func(*nodeServiceConfig)

type nodeServiceConfig struct {
	ttl             time.Duration
	hbInterval      time.Duration // 0 ⇒ derivado de ttl/3 em NewNodeService
	completedCap    int           // 0 ⇒ DefaultCompletedRetention; < 0 ⇒ ilimitado
	completedCapSet bool
	workerID        string
	leaseClock      durable.Clock
	logw            io.Writer
}

// WithLeaseTTL define o TTL do lease de posse de run (default [DefaultLeaseTTL]).
// Valores <= 0 são ignorados (mantém o default).
func WithLeaseTTL(ttl time.Duration) NodeServiceOption {
	return func(c *nodeServiceConfig) {
		if ttl > 0 {
			c.ttl = ttl
		}
	}
}

// WithLeaseHeartbeat define o período de renovação (heartbeat) da posse do lease durante
// um run. Default: TTL/3. Um valor <= 0 é ignorado (mantém o default). Deve ser
// confortavelmente inferior ao TTL para a posse nunca expirar a meio de um run vivo.
func WithLeaseHeartbeat(interval time.Duration) NodeServiceOption {
	return func(c *nodeServiceConfig) {
		if interval > 0 {
			c.hbInterval = interval
		}
	}
}

// WithCompletedRetention limita quantos desfechos de runs terminados o serviço retém em
// memória (poda FIFO dos mais antigos ao exceder). Default [DefaultCompletedRetention];
// um valor <= 0 desliga a poda (retenção ilimitada — usar só quando o desfecho é colhido
// e libertado por outra via).
func WithCompletedRetention(max int) NodeServiceOption {
	return func(c *nodeServiceConfig) {
		c.completedCap = max
		c.completedCapSet = true
	}
}

// WithServiceWorkerID rotula os leases desta réplica (só observabilidade — a liveness é
// por lease/TTL, nunca por identidade nem PID).
func WithServiceWorkerID(id string) NodeServiceOption {
	return func(c *nodeServiceConfig) { c.workerID = id }
}

// WithLeaseClock injecta o relógio do [durable.LeaseManager] (determinismo em teste de
// expiração de lease, sem sleeps). Ignora nil.
func WithLeaseClock(clk durable.Clock) NodeServiceOption {
	return func(c *nodeServiceConfig) {
		if clk != nil {
			c.leaseClock = clk
		}
	}
}

// WithServiceLog injecta o destino dos logs do serviço (arranque/panic/drain). nil ⇒
// sem logs.
func WithServiceLog(w io.Writer) NodeServiceOption {
	return func(c *nodeServiceConfig) { c.logw = w }
}

// NewNodeService constrói o loop de serviço sobre um [Node] já composto (AOS-163). O
// [durable.LeaseManager] é construído sobre o Event Store do nó (a posse durável partilha
// o substrato do nó) e embrulhado num [worker.Assigner] REUTILIZADO — não se reinventa o
// escalonador. node e node.EventStore são obrigatórios (fail-closed).
func NewNodeService(node *Node, opts ...NodeServiceOption) (*NodeService, error) {
	if node == nil {
		return nil, errors.New("aos: NodeService exige um Node (nil)")
	}
	if node.EventStore == nil {
		return nil, errors.New("aos: NodeService exige o Event Store do no (nil)")
	}
	cfg := nodeServiceConfig{ttl: DefaultLeaseTTL}
	for _, o := range opts {
		o(&cfg)
	}
	// Período de heartbeat: explícito, ou derivado de TTL/3 (renova ~3x por TTL, dando
	// margem folgada para a posse nunca expirar a meio de um run vivo).
	hbInterval := cfg.hbInterval
	if hbInterval <= 0 {
		hbInterval = cfg.ttl / 3
	}
	// Retenção de desfechos: explícita (incl. <= 0 = ilimitada), ou o default.
	completedCap := DefaultCompletedRetention
	if cfg.completedCapSet {
		completedCap = cfg.completedCap
	}

	var leaseOpts []durable.LeaseOption
	if cfg.leaseClock != nil {
		leaseOpts = append(leaseOpts, durable.WithLeaseClock(cfg.leaseClock))
	}
	if cfg.workerID != "" {
		leaseOpts = append(leaseOpts, durable.WithWorkerID(cfg.workerID))
	}
	leases, err := durable.NewLeaseManager(node.EventStore, cfg.ttl, leaseOpts...)
	if err != nil {
		return nil, fmt.Errorf("aos: lease manager do loop de servico: %w", err)
	}
	assigner, err := worker.NewAssigner(leases)
	if err != nil {
		return nil, fmt.Errorf("aos: assigner (posse por lease) do loop de servico: %w", err)
	}

	s := &NodeService{
		node:         node,
		assigner:     assigner,
		leases:       leases,
		logw:         cfg.logw,
		hbInterval:   hbInterval,
		completedCap: completedCap,
		runs:         make(map[string]*runState),
		completed:    make(map[string]*runState),
	}
	s.log("loop de servico do no `aos` pronto (AOS-164a): TTL de lease=%s, heartbeat=%s, worker=%q, retencao=%d",
		cfg.ttl, hbInterval, cfg.workerID, completedCap)
	return s, nil
}

// Submit aceita um run e hospeda-o. Passos (fail-closed):
//
//  1. valida o RunID e recusa se o nó está em shutdown;
//  2. RESERVA o RunID no registo de em-curso (recusa um RunID já em curso —
//     [ErrRunAlreadyInProgress] — ou já terminado e ainda retido — [ErrRunAlreadyCompleted]);
//     a reserva bloqueia uma segunda submissão concorrente do mesmo RunID;
//  3. adquire a POSSE por lease via [worker.Assigner.TryAcquire]; se o lease é detido por
//     outra réplica ((_, false, nil)) o run NÃO é hospedado (sem roubo) e a reserva é
//     desfeita;
//  4. re-verifica o shutdown (uma corrida com Shutdown durante a aquisição do lease) e,
//     livre, arranca a goroutine do run com panic-recovery.
//
// O ctx dado é usado SÓ para a aquisição do lease (I/O no Event Store); a EXECUÇÃO do run
// corre sob um contexto próprio do serviço (o run sobrevive ao retorno de Submit e só é
// cancelado por [Shutdown]).
func (s *NodeService) Submit(ctx context.Context, goal agentruntime.Goal) error {
	if goal.RunID == "" {
		return ErrEmptyRunID
	}
	runID := goal.RunID

	// (1)+(2) Reserva sob o mutex: recusa shutdown e duplicação de RunID.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrServiceShuttingDown
	}
	if _, dup := s.runs[runID]; dup {
		s.mu.Unlock()
		return ErrRunAlreadyInProgress
	}
	if _, done := s.completed[runID]; done {
		// Desfecho ainda retido: re-submissão recusada explicitamente (não re-executa nem
		// sobrescreve o desfecho, nem finge que o lease é de outra réplica). Após a poda
		// do desfecho o RunID volta a ser submetível.
		s.mu.Unlock()
		return ErrRunAlreadyCompleted
	}
	rs := &runState{runID: runID, done: make(chan struct{})}
	s.runs[runID] = rs // RESERVA — bloqueia duplicados enquanto adquirimos o lease
	s.mu.Unlock()

	// (3) POSSE por lease FORA do mutex (I/O no Event Store). Sem roubo: um lease vivo
	// detido por outra réplica ⇒ (_, false, nil) ⇒ não hospeda.
	lease, acquired, err := s.assigner.TryAcquire(ctx, runID)
	if err != nil {
		s.unreserve(rs)
		return fmt.Errorf("aos: aquisicao de lease do run %q: %w", runID, err)
	}
	if !acquired {
		s.unreserve(rs)
		return ErrRunLeaseHeldElsewhere
	}

	// (4) Arranque sob o mutex: a re-verificação de `closed` e o wg.Add ficam no MESMO
	// secção crítica que o set de `closed` do Shutdown — garantindo que um run contado no
	// WaitGroup NUNCA escapa a um Shutdown que já começou (e vice-versa).
	runCtx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if s.closed {
		// Shutdown começou durante a aquisição do lease: aborta limpo (larga a posse
		// em-processo; o lease durável expira por TTL — sem revogação).
		delete(s.runs, runID)
		s.mu.Unlock()
		cancel()
		s.assigner.Release(runID)
		close(rs.done)
		return ErrServiceShuttingDown
	}
	rs.lease = lease
	rs.cancel = cancel
	s.wg.Add(1)
	s.mu.Unlock()

	go s.hostRun(runCtx, rs, goal)
	return nil
}

// unreserve desfaz a reserva de um RunID que não chegou a ser hospedado (falha de
// aquisição de lease). Larga a posse em-processo por segurança (idempotente) e fecha o
// done da reserva.
func (s *NodeService) unreserve(rs *runState) {
	s.mu.Lock()
	delete(s.runs, rs.runID)
	s.mu.Unlock()
	s.assigner.Release(rs.runID)
	close(rs.done)
}

// hostRun corre UM run numa goroutine ISOLADA. O defer garante que:
//
//   - um PANIC do run é CAPTURADO (recover) — não derruba o nó nem os outros runs; o run
//     é marcado falhado com [ErrRunPanicked];
//   - o heartbeat de posse é PARADO e AGUARDADO (sem goroutine órfã) antes de largar o
//     lease;
//   - a posse em-processo do lease é largada e o run migra de `runs` para `completed`;
//   - o WaitGroup é decrementado (o Shutdown drena por ele).
//
// A ordem dos defers (LIFO) é: recover() primeiro (captura o panic de Run), depois parar
// o heartbeat (join), depois finish() (liberta lease + move para completed + fecha done),
// e por fim wg.Done().
func (s *NodeService) hostRun(ctx context.Context, rs *runState, goal agentruntime.Goal) {
	defer s.wg.Done()
	defer s.finish(rs)

	// Renovador de posse por-run: mantém o lease vivo enquanto o run corre (fecha a janela
	// em que um run mais longo que o TTL veria a posse EXPIRAR — liveness classificá-lo-ia
	// zombie e outra réplica reclamaria o lease, arrancando uma 2ª execução concorrente do
	// mesmo RunID). A goroutine é PARADA e AGUARDADA aqui (join) antes de finish largar o
	// lease — nem heartbeat órfão nem corrida com a libertação. rs.lease e rs.cancel foram
	// escritos em Submit sob o mutex ANTES deste `go hostRun` (happens-before): lê-los sem
	// lock é seguro.
	hbStop := make(chan struct{})
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		s.heartbeat(ctx, rs.runID, rs.lease, rs.cancel, hbStop)
	}()
	defer func() {
		close(hbStop)
		<-hbDone
	}()

	defer func() {
		if r := recover(); r != nil {
			s.mu.Lock()
			rs.panicked = true
			rs.err = fmt.Errorf("%w: %v", ErrRunPanicked, r)
			s.mu.Unlock()
			s.log("run %q PANIC recuperado (isolamento por-run) — no continua: %v", rs.runID, r)
		}
	}()

	res, _, err := s.node.Runtime.Run(ctx, goal, nil)
	s.mu.Lock()
	rs.result = res
	rs.err = err
	s.mu.Unlock()
}

// heartbeat renova periodicamente a posse do lease do run enquanto ele corre. Pára quando
// hbStop fecha (run terminou) ou o ctx do run é cancelado. Se perder a partição —
// [durable.ErrLeaseSuperseded] (outro claim superou-nos) ou [durable.ErrLeaseExpired] (TTL
// esgotado apesar da renovação, ex.: pausa de GC/relógio) — CANCELA o run cooperativamente
// (cancel): já não somos o dono, e o fencing rejeitaria as nossas escritas tardias; parar
// já evita duplo-efeito. Erros transitórios do Event Store são registados e re-tentados no
// tick seguinte. O lease durável é o mesmo (o heartbeat não minta token), pelo que se
// reutiliza a cópia imutável capturada no arranque.
func (s *NodeService) heartbeat(ctx context.Context, runID string, lease durable.Lease, cancel context.CancelFunc, hbStop <-chan struct{}) {
	if s.hbInterval <= 0 {
		return // heartbeat desligado (ex.: TTL demasiado curto para derivar período)
	}
	t := time.NewTicker(s.hbInterval)
	defer t.Stop()
	for {
		select {
		case <-hbStop:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := s.leases.Heartbeat(ctx, lease); err != nil {
				if errors.Is(err, durable.ErrLeaseSuperseded) || errors.Is(err, durable.ErrLeaseExpired) {
					s.log("run %q PERDEU a posse do lease (%v) — a cancelar (sem duplo-efeito; fencing barra escritas tardias)", runID, err)
					if cancel != nil {
						cancel()
					}
					return
				}
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return // ctx do run terminou durante o I/O do heartbeat
				}
				// Transitório (contenção/ES): loga e re-tenta no próximo tick.
				s.log("run %q heartbeat de posse falhou (transitorio, re-tenta): %v", runID, err)
			}
		}
	}
}

// finish liberta a posse em-processo do lease (o lease durável expira por TTL — não há
// revogação, AOS-018), cancela o contexto do run e move-o de em-curso para terminado,
// fechando o seu done. É o ponto único de saída de um run do registo de em-curso.
func (s *NodeService) finish(rs *runState) {
	s.assigner.Release(rs.runID)
	if rs.cancel != nil {
		rs.cancel()
	}
	s.mu.Lock()
	delete(s.runs, rs.runID)
	if _, seen := s.completed[rs.runID]; !seen {
		s.completedOrder = append(s.completedOrder, rs.runID)
	}
	s.completed[rs.runID] = rs
	s.pruneCompletedLocked()
	s.mu.Unlock()
	close(rs.done)
}

// pruneCompletedLocked poda os desfechos MAIS ANTIGOS (FIFO) enquanto o registo de
// terminados exceder o teto de retenção. Chamado sob o mutex. completedCap <= 0 desliga a
// poda. A compactação é in-place (append sobre o próprio prefixo) — sem crescimento do
// array de suporte nem retenção fantasma das strings podadas.
func (s *NodeService) pruneCompletedLocked() {
	if s.completedCap <= 0 {
		return
	}
	for len(s.completedOrder) > s.completedCap {
		oldest := s.completedOrder[0]
		delete(s.completed, oldest)
		s.completedOrder = append(s.completedOrder[:0], s.completedOrder[1:]...)
	}
}

// Shutdown encerra o nó GRACIOSAMENTE: para de aceitar novos runs e espera os em curso
// drenarem. Se o ctx expirar antes do dreno, CANCELA os runs em curso de forma LIMPA
// (cancelamento de contexto — o loop pára na fronteira de fim-de-turno, nunca a meio nem
// por kill cego, AOS-023) e espera-os desenrolar (libertando os leases). Devolve nil se
// drenou a tempo, ou o erro do ctx se teve de cancelar. Idempotente.
func (s *NodeService) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.draining.Store(true) // espelha o drain (armado SOB mu, no mesmo ponto que closed) para leitura lock-free da sonda
	inflight := len(s.runs)
	s.mu.Unlock()
	s.log("shutdown gracioso iniciado — %d run(s) em curso a drenar (nao aceita novos)", inflight)

	drained := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(drained)
	}()

	select {
	case <-drained:
		s.log("shutdown: todos os runs drenaram graciosamente")
		return nil
	case <-ctx.Done():
		// Deadline: cancela os em curso LIMPO (cooperativo, nunca kill cego) e espera-os
		// desenrolar — cada um liberta o seu lease no finish().
		s.log("shutdown: deadline atingido — a cancelar (limpo) os runs em curso")
		s.cancelInFlight()
		s.wg.Wait()
		s.log("shutdown: runs cancelados desenrolaram e libertaram os leases")
		return ctx.Err()
	}
}

// cancelInFlight cancela o contexto de cada run em curso (cancelamento cooperativo). NÃO
// mata cegamente: o run pára ao verificar o ctx (fronteira de fim-de-turno / checkpoint)
// e desenrola pelo finish(), libertando o lease.
func (s *NodeService) cancelInFlight() {
	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.runs))
	for _, rs := range s.runs {
		if rs.cancel != nil {
			cancels = append(cancels, rs.cancel)
		}
	}
	s.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}

// InProgress devolve os RunIDs em curso, por ordem estável (observabilidade). É uma
// cópia — mutá-la não afecta o registo.
func (s *NodeService) InProgress() []string {
	s.mu.Lock()
	out := make([]string, 0, len(s.runs))
	for id := range s.runs {
		out = append(out, id)
	}
	s.mu.Unlock()
	sort.Strings(out)
	return out
}

// Draining indica se o shutdown gracioso já começou (ou seja, se [Submit] já recusa
// novos runs com [ErrServiceShuttingDown]). Lê o espelho atómico `draining` — armado
// SOB o mutex no MESMO ponto que o `closed` que governa a admissão (ver [Shutdown]),
// e monotónico (o drain nunca reverte), pelo que NÃO pode divergir do estado que
// recusa runs. A leitura é lock-free de propósito: a sonda de prontidão (/readyz) pode
// ser sondada à frequência de um probe sem contender com o `s.mu` que serializa a
// admissão/conclusão de runs — simétrico a [eventstore.Store.Healthy], que foi feito
// atómico pela mesma razão. É esta a fonte que /readyz consulta para virar 503 durante
// o drain (o orquestrador deixa de encaminhar tráfego novo), enquanto a liveness
// (/healthz) permanece 200 até o processo sair.
func (s *NodeService) Draining() bool {
	return s.draining.Load()
}

// InProgressCount devolve o número de runs em curso.
func (s *NodeService) InProgressCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runs)
}

// Outcome devolve o desfecho de um run TERMINADO (result/erro/panic) e se está registado
// como terminado. Um run ainda em curso devolve (_, false).
func (s *NodeService) Outcome(runID string) (RunOutcome, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.completed[runID]
	if !ok {
		return RunOutcome{}, false
	}
	return RunOutcome{RunID: rs.runID, Result: rs.result, Err: rs.err, Panicked: rs.panicked}, true
}

// Wait bloqueia até o run runID terminar (sair do registo de em-curso) e devolve o seu
// desfecho, ou (_, false, ctx.Err()) se o ctx expirar antes. Um run desconhecido (nunca
// submetido ou já colhido) devolve imediatamente o que houver em `completed`. Útil para
// testes determinísticos e para conduzir o run a partir da CLI.
func (s *NodeService) Wait(ctx context.Context, runID string) (RunOutcome, bool, error) {
	s.mu.Lock()
	if rs, ok := s.runs[runID]; ok {
		done := rs.done
		s.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return RunOutcome{}, false, ctx.Err()
		}
	} else {
		s.mu.Unlock()
	}
	oc, ok := s.Outcome(runID)
	return oc, ok, nil
}

// log escreve uma linha no destino de logs do serviço (se configurado).
func (s *NodeService) log(format string, args ...any) {
	if s.logw != nil {
		fmt.Fprintf(s.logw, "[aos-service] "+format+"\n", args...)
	}
}
