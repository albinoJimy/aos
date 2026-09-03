package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// EventTypeWorkerStep é o tipo canónico do MARCADOR DE PROGRESSO fenced que o
// supervisor appenda ANTES de cada efeito. Não carrega o efeito — é o GATE que,
// escrito via [durable.FencedAppender] sob o token do lease, prova que este worker
// ainda é o escritor legítimo. Um token superado (ou lease expirado) é rejeitado
// com [durable.ErrStaleFencingToken] e o marcador NÃO entra no log.
const EventTypeWorkerStep = "worker.step.dispatched"

// workerStepPrefix namespaceia o step_id do marcador no envelope do Event Store
// (run_id + ":wstep-" + step_id), para que a sua idempotency_key seja DISTINTA da
// do turno (run_id:step_id), da do ledger (run_id:ledger-…) e da do checkpoint
// (run_id:ckpt-…) — senão a dedup GLOBAL do ES colidiria o marcador com um evento
// homónimo. Numa re-execução após crash, re-escrever o mesmo marcador dá
// StatusDuplicate (sem duplicar) — a própria escrita do gate é idempotente.
const workerStepPrefix = "wstep-"

// attrStepApplied é o atributo de observabilidade que marca se o efeito do passo
// CORREU (true) ou foi DEDUPLICADO pelo ledger (false, já aplicado — resume/retry).
// É um rótulo booleano, nunca um segredo.
const attrStepApplied = "aos.worker.step.applied"

// defaultHeartbeatInterval é o intervalo por omissão entre heartbeats do lease.
// Deve ser bem inferior ao TTL do lease para que a posse não expire sob operação
// normal. O chamador ajusta-o com [WithHeartbeatInterval] em função do seu TTL.
const defaultHeartbeatInterval = time.Second

// TickerFactory constrói o canal de disparo do heartbeat periódico. É INJECTÁVEL
// para tornar os testes de heartbeat DETERMINÍSTICOS (um ticker manual que o teste
// bombeia), sem sleeps frágeis nem dependência do relógio de parede. O default é um
// [time.Ticker] do intervalo configurado. stop() liberta o ticker (chamado no fim
// do heartbeat — sem fugas de goroutine).
type TickerFactory func(d time.Duration) (ch <-chan time.Time, stop func())

func defaultTickerFactory(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// Worker é o LOOP SUPERVISOR DE POSSE-DE-PARTIÇÃO-POR-RUN de AOS-099 (ver doc.go).
// É STATELESS quanto a estado durável de run: guarda apenas HANDLES para as portas
// duráveis e CONFIGURAÇÃO imutável — nunca um mapa de run→estado nem o cursor de
// progresso em campos do processo. Todo o estado de execução de um run vive na
// pilha de [Worker.Run] (numa [runSession]) e, de forma durável, no Event Store.
// Um mesmo Worker serve vários runs em paralelo com segurança; um processo worker
// NOVO reconstrói o ponto de retoma inteiramente a partir do log.
type Worker struct {
	leases       *durable.LeaseManager
	fenced       *durable.FencedAppender
	ledger       *durable.StepLedger
	resumer      *durable.Resumer
	checkpointer agentruntime.Checkpointer
	monitor      Mediator

	seq        *durable.StepSequencer
	tracer     otelgenai.Tracer
	producer   eventstore.Producer
	workerID   string
	hbInterval time.Duration
	newTicker  TickerFactory
}

// WorkerOption configura o [Worker].
type WorkerOption func(*Worker)

// WithStepSequencer injecta o derivador de step_ids. DEVE ser o MESMO que o
// [durable.Resumer] usa ([durable.WithStepIdentity]) — senão o NextStepID e as
// chaves de idempotência divergem e o resume-from-step falha (o Resumer fail-closed
// com ErrStepIdentityMismatch). Default: [durable.NewStepSequencer] canónico.
func WithStepSequencer(s *durable.StepSequencer) WorkerOption {
	return func(w *Worker) {
		if s != nil {
			w.seq = s
		}
	}
}

// WithTracer injecta o [otelgenai.Tracer] em que o worker emite um span
// execute_tool por passo, com o custo por passo (AttrRunID/AttrStepID/
// AttrCostMicroUSD). Default: [otelgenai.NoopTracer] (sem observabilidade).
func WithTracer(t otelgenai.Tracer) WorkerOption {
	return func(w *Worker) {
		if t != nil {
			w.tracer = t
		}
	}
}

// WithProducer define a identidade emissora (NHI + cadeia de delegação) gravada nos
// marcadores de progresso fenced. Default: Producer zero (aceitável em teste; em
// produção o worker injecta o principal para responsabilização, ADR-003).
func WithProducer(p eventstore.Producer) WorkerOption {
	return func(w *Worker) { w.producer = p }
}

// WithWorkerID rotula o worker (só observabilidade — NUNCA decide liveness; a
// posse é decidida por lease/TTL/fencing, não por identidade nem PID). Default: "".
func WithWorkerID(id string) WorkerOption {
	return func(w *Worker) { w.workerID = id }
}

// WithHeartbeatInterval define o intervalo entre heartbeats do lease (default 1s).
// Deve ser bem inferior ao TTL do lease do [durable.LeaseManager].
//
// # PORQUE AQUI NÃO HÁ RECUSA FAIL-CLOSED, ao contrário de aos.WithLeaseHeartbeat (AOS-297)
//
// A AC de AOS-297 pedia a mesma validação aqui «ou razão escrita para não a ter». É esta, e
// começa por uma honestidade: HOJE nenhuma das duas opções tem chamador de produção — nem esta
// nem `aos.WithLeaseHeartbeat`, como o próprio EPIC-21 regista. O que separa os dois casos é o
// CUSTO, não a cobertura.
//
// Do lado do nó a guarda custou ZERO: nada a chamava, nada partiu. Aqui custa cinco fixtures em
// três módulos — worker/, platform/dr e qa/dr-e2e —, todas a passar `time.Hour` sobre um TTL de
// 30s com relógio MANUAL. Nesse contexto o valor não é um intervalo de renovação: é a forma
// idiomática de dizer «o heartbeat real não dispara durante este teste». Recusá-lo obrigaria as
// cinco a escrever um número arbitrariamente abaixo do TTL que significa exactamente o mesmo e
// lê-se pior — pagar essa troca para proteger um caminho que ninguém percorre é mau negócio.
//
// Há uma segunda diferença, mais fraca mas real: `NewNodeService` ESTÁ no caminho de produção,
// pelo que um futuro chamador de `WithLeaseHeartbeat` chega lá de imediato; [NewWorker] não é
// composto em lado nenhum, pelo que um futuro chamador desta opção continuaria fora de produção
// até alguém compor o worker.
//
// Se um dia [NewWorker] ganhar um chamador de produção, esta decisão caduca: o TTL vive no
// [durable.LeaseManager] que este construtor já recebe — falta-lhe só um acessor —, e a guarda
// passa a valer o que custa. Não se acrescentou o acessor agora: material exportado sem
// consumidor é o defeito que AOS-296 persegue no mesmo epic.
func WithHeartbeatInterval(d time.Duration) WorkerOption {
	return func(w *Worker) {
		if d > 0 {
			w.hbInterval = d
		}
	}
}

// WithTickerFactory injecta a fábrica do ticker do heartbeat (teste determinístico).
// Default: um [time.Ticker].
func WithTickerFactory(f TickerFactory) WorkerOption {
	return func(w *Worker) {
		if f != nil {
			w.newTicker = f
		}
	}
}

// NewWorker constrói o supervisor COMPONDO os primitivos duráveis. Todos os handles
// são obrigatórios (fail-closed na construção): sem lease não há posse; sem fenced
// appender não há enforcement de escritor único; sem ledger não há idempotência; sem
// resumer não há resume-from-step; sem checkpointer o cursor nunca avança; sem
// mediador não há caminho legítimo para um efeito (ADR-002).
//
// Resultados sensíveis: o worker persiste o output da tool como o Payload do resultado
// do ledger. Os spans/logs do worker NÃO carregam esse output (só IDs, custo e
// rótulos booleanos), mas o Event Store guarda o Payload em claro. Para tools cujo
// output transporta segredos, o chamador DEVE construir o [durable.StepLedger] com
// [durable.WithSensitiveResults] (que faz o Apply RECUSAR resultados em claro) e a
// tool DEVE devolver uma REFERÊNCIA (hash/URI) em vez do valor sensível. O worker
// COMPÕE o ledger que recebe — não relaxa essa guarda.
func NewWorker(
	leases *durable.LeaseManager,
	fenced *durable.FencedAppender,
	ledger *durable.StepLedger,
	resumer *durable.Resumer,
	checkpointer agentruntime.Checkpointer,
	monitor Mediator,
	opts ...WorkerOption,
) (*Worker, error) {
	if leases == nil {
		return nil, ErrNilLeaseManager
	}
	if fenced == nil {
		return nil, ErrNilFencedAppender
	}
	if ledger == nil {
		return nil, ErrNilLedger
	}
	if resumer == nil {
		return nil, ErrNilResumer
	}
	if checkpointer == nil {
		return nil, ErrNilCheckpointer
	}
	if monitor == nil {
		return nil, ErrNilMonitor
	}
	w := &Worker{
		leases:       leases,
		fenced:       fenced,
		ledger:       ledger,
		resumer:      resumer,
		checkpointer: checkpointer,
		monitor:      monitor,
		seq:          durable.NewStepSequencer(),
		tracer:       otelgenai.NoopTracer{},
		hbInterval:   defaultHeartbeatInterval,
		newTicker:    defaultTickerFactory,
	}
	for _, o := range opts {
		o(w)
	}
	if w.seq == nil {
		w.seq = durable.NewStepSequencer()
	}
	if w.tracer == nil {
		w.tracer = otelgenai.NoopTracer{}
	}
	if w.hbInterval <= 0 {
		w.hbInterval = defaultHeartbeatInterval
	}
	if w.newTicker == nil {
		w.newTicker = defaultTickerFactory
	}
	return w, nil
}

// Run reclama a posse da partição do run (lease/fencing token — NUNCA PID) e serve-o
// resume-from-step até ao fim ou até perder a posse. Se um lease AINDA VÁLIDO for
// detido por outra réplica, devolve [durable.ErrLeaseHeld] (sem roubo, sem
// rebalancing — a réplica deixa a partição em paz). Perda de posse a meio devolve
// [ErrLeaseLost] (fail-closed, sem duplicar). Uma negação de política devolve
// [ErrDenied].
func (w *Worker) Run(ctx context.Context, plan *RunPlan) (RunOutcome, error) {
	if plan == nil {
		return RunOutcome{}, ErrNilPlan
	}
	if plan.RunID == "" {
		return RunOutcome{}, ErrEmptyRunID
	}
	lease, err := w.leases.Claim(ctx, plan.RunID)
	if err != nil {
		return RunOutcome{RunID: plan.RunID}, err
	}
	return w.serve(ctx, newSession(plan, lease))
}

// Adopt serve um run cuja posse JÁ foi reclamada (o lease vem de um [Assigner] que
// fez o Claim) — para compor atribuição de partições e execução sem duplo claim. O
// lease TEM de ser do mesmo run do plano. Semântica de retoma/perda idêntica a [Run].
func (w *Worker) Adopt(ctx context.Context, plan *RunPlan, lease durable.Lease) (RunOutcome, error) {
	if plan == nil {
		return RunOutcome{}, ErrNilPlan
	}
	if plan.RunID == "" {
		return RunOutcome{}, ErrEmptyRunID
	}
	if lease.RunID != plan.RunID {
		return RunOutcome{RunID: plan.RunID},
			fmt.Errorf("worker: lease do run %q não corresponde ao plano %q", lease.RunID, plan.RunID)
	}
	return w.serve(ctx, newSession(plan, lease))
}

// serve corre o loop de passos sob um heartbeat periódico. A ordem é fail-closed:
// arranca o heartbeat (goroutine), reconstrói o ledger e o cursor de retoma A PARTIR
// DO LOG (statelessness), salta os turnos já confirmados e executa os restantes. O
// heartbeat, ao perder a posse, CANCELA o ctx dos passos para o loop parar de
// escrever de imediato.
func (w *Worker) serve(ctx context.Context, sess *runSession) (RunOutcome, error) {
	stepCtx, cancelStep := context.WithCancel(ctx)
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		w.runHeartbeat(stepCtx, sess, cancelStep)
	}()
	// Cancela o heartbeat e JUNTA-O antes de devolver — sem fugas de goroutine, -race
	// limpo. (cancelStep antes do join: a goroutine sai do select por ctx.Done.)
	defer func() {
		cancelStep()
		<-hbDone
	}()

	out := RunOutcome{RunID: sess.runID}

	// Reconstrução a partir do log (AC2): um processo NOVO recupera o estado do ledger
	// e o cursor de retoma sem herdar nada do processo morto.
	if err := w.ledger.Rebuild(stepCtx, sess.runID); err != nil {
		return out, err
	}
	rp, err := w.resumer.Resume(stepCtx, sess.runID)
	if err != nil {
		return out, err
	}
	out.ResumeTurn = rp.NextTurn

	for i := range sess.plan.Steps {
		turn := i + 1
		if turn < rp.NextTurn {
			// Turno já confirmado no log: resume-from-step salta-o (não re-executa).
			out.Skipped++
			continue
		}
		if err := w.executeStep(stepCtx, sess, turn, &sess.plan.Steps[i]); err != nil {
			return out, err
		}
		out.Executed++
	}
	out.Completed = true
	return out, nil
}

// runHeartbeat renova periodicamente o lease. Uma renovação recusada
// ([durable.ErrLeaseSuperseded] — superado por um novo claim; ou
// [durable.ErrLeaseExpired] — TTL esgotado) marca a perda de posse e cancela o ctx
// dos passos: o loop para de escrever de imediato (fail-closed), sem duplicar. Um
// ctx cancelado (fim normal do run ou paragem) sai em silêncio.
func (w *Worker) runHeartbeat(ctx context.Context, sess *runSession, onLoss func()) {
	ch, stop := w.newTicker(w.hbInterval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			renewed, err := w.leases.Heartbeat(ctx, sess.currentLease())
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				sess.lose(err)
				onLoss()
				return
			}
			sess.renew(renewed)
		}
	}
}

// executeStep processa UM passo sob a ordem fail-closed (ver doc.go): span+custo →
// GATE fenced → efeito idempotente via ledger (com mediação RM dentro) → checkpoint.
// A mediação corre DENTRO do effect do ledger: numa re-execução already-applied, o
// effect NÃO corre — sem re-mediação, sem re-despacho, sem efeito duplicado.
func (w *Worker) executeStep(ctx context.Context, sess *runSession, turn int, step *Step) error {
	if lost := sess.loss(); lost != nil {
		return fmt.Errorf("%w: %v", ErrLeaseLost, lost)
	}
	stepID := w.seq.StepID(sess.runID, turn)

	// Span execute_tool do worker, pai do span de mediação do RM (propagação via
	// spanCtx), mantendo a árvore de trace coesa. O CUSTO POR PASSO NÃO é anotado
	// aqui: só é emitido DEPOIS de o ledger revelar que o efeito CORREU de facto
	// (applied==true), para não sobre-contabilizar o custo de um passo de fronteira
	// re-executado e DEDUPLICADO na retoma (crash entre ledger.Apply e Checkpoint).
	// Ver a anotação condicional após a aplicação, abaixo.
	spanCtx, span := w.tracer.StartSpan(ctx, otelgenai.OpExecuteTool)
	span.SetAttribute(otelgenai.AttrOperationName, otelgenai.OpExecuteTool)
	span.SetAttribute(otelgenai.AttrRunID, sess.runID)
	span.SetAttribute(otelgenai.AttrStepID, stepID)
	defer span.End()

	// (1) GATE FENCED: prova de posse ANTES de qualquer efeito. Token superado/lease
	//     expirado ⇒ rejeitado e o worker para fail-closed.
	if err := w.fencedGate(spanCtx, sess, stepID); err != nil {
		span.SetAttribute(otelgenai.AttrErrorType, spanErrorType(err))
		return err
	}

	// (2) EFEITO idempotente: key = f(run_id, step_id) (ADR-001). A mediação RM e o
	//     efeito correm SÓ na primeira aplicação; a re-execução deduplica.
	key, err := durable.IdempotencyKey(sess.runID, stepID)
	if err != nil {
		span.SetAttribute(otelgenai.AttrErrorType, spanErrorType(err))
		return err
	}
	_, applied, err := w.ledger.Apply(spanCtx, key, func(ec context.Context) (durable.Result, error) {
		call := step.Call
		call.RunID = sess.runID
		call.StepID = stepID
		dec, merr := w.monitor.Mediate(ec, call)
		if merr != nil {
			return durable.Result{}, merr
		}
		if !dec.Permitted() {
			// Negação/escalada de política: fail-closed, sem mascarar. Nada fica
			// registado no ledger (o passo não ficou aplicado).
			return durable.Result{}, fmt.Errorf("%w: effect=%s code=%s", ErrDenied, dec.Effect, dec.Code)
		}
		if dec.ToolErr != nil {
			// Erro de EXECUÇÃO da tool (não é negação): o passo não converge; propaga
			// para o ledger não memorizar um resultado falhado.
			return durable.Result{}, dec.ToolErr
		}
		return durable.Result{Status: "ok", Payload: dec.Output}, nil
	})
	if err != nil {
		err = sess.wrapLoss(err) // uma perda de posse concorrente reporta-se como ErrLeaseLost
		span.SetAttribute(otelgenai.AttrErrorType, spanErrorType(err))
		return err
	}
	span.SetAttribute(attrStepApplied, applied)
	// CUSTO POR PASSO (AOS-078): anotado SÓ quando o efeito CORREU (applied==true). Um
	// passo re-executado na retoma cujo efeito o ledger DEDUPLICOU (applied==false) não
	// re-contabiliza custo — a agregação de custo (fonte de verdade AttrCostMicroUSD)
	// conta cada passo exactamente uma vez através da fronteira de kill/resume.
	if applied {
		span.SetAttribute(otelgenai.AttrCostMicroUSD, step.CostMicroUSD)
		span.SetAttribute(otelgenai.AttrCostUSD, otelgenai.MicroUSDToUSD(step.CostMicroUSD))
	}

	// (3) CHECKPOINT: avança o cursor (turno verificado) para que um worker de
	//     substituição retome DEPOIS deste passo.
	if err := w.checkpointer.Checkpoint(spanCtx, agentruntime.Checkpoint{
		RunID:  sess.runID,
		StepID: stepID,
		Turn:   turn,
		Phase:  agentruntime.PhaseVerified,
	}); err != nil {
		err = sess.wrapLoss(err)
		span.SetAttribute(otelgenai.AttrErrorType, spanErrorType(err))
		return err
	}
	return nil
}

// spanErrorType mapeia um erro para um CÓDIGO estável e LIMITADO para o atributo de
// span error.type, em vez do err.Error() cru — o único campo do span do worker que,
// de outra forma, derivaria de uma string ARBITRÁRIA de uma tool a jusante (que
// poderia ecoar fragmentos de input/credenciais). O detalhe completo permanece no
// erro propagado ao chamador (e no registo de auditoria não-exportado), nunca no span.
func spanErrorType(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrLeaseLost):
		return "lease_lost"
	case errors.Is(err, ErrDenied):
		return "policy_denied"
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	default:
		// Catch-all limitado: erro de execução da tool, derivação de chave ou
		// serialização. Estável e sem texto arbitrário a jusante.
		return "step_error"
	}
}

// workerStepRecord é o corpo JSON do marcador de progresso fenced. Sem segredos: só
// identidade/posição do passo e o rótulo do worker.
type workerStepRecord struct {
	RunID  string `json:"run_id"`
	StepID string `json:"step_id"`
	Worker string `json:"worker,omitempty"`
}

// fencedGate appenda o marcador de progresso via [durable.FencedAppender] sob o
// token do lease. Uma rejeição por token obsoleto ([durable.ErrStaleFencingToken])
// — worker superado ou lease expirado — marca a perda de posse e devolve
// [ErrLeaseLost]: a escrita NÃO entrou no log (no máximo um escritor efectivo).
func (w *Worker) fencedGate(ctx context.Context, sess *runSession, stepID string) error {
	payload, err := json.Marshal(workerStepRecord{RunID: sess.runID, StepID: stepID, Worker: w.workerID})
	if err != nil {
		return err
	}
	_, err = w.fenced.Append(ctx, sess.runID, sess.token, eventstore.EventInput{
		Type:     EventTypeWorkerStep,
		Payload:  payload,
		RunID:    sess.runID,
		StepID:   workerStepPrefix + stepID,
		Producer: w.producer,
	})
	if err != nil {
		if errors.Is(err, durable.ErrStaleFencingToken) {
			sess.lose(err)
			return fmt.Errorf("%w: %v", ErrLeaseLost, err)
		}
		if errors.Is(err, context.Canceled) {
			if lost := sess.loss(); lost != nil {
				return fmt.Errorf("%w: %v", ErrLeaseLost, lost)
			}
		}
		return err
	}
	return nil
}

// runSession é o estado de execução de UM run — vive na PILHA de [Worker.serve],
// nunca em campos do [Worker]. O token do lease é imutável durante a sessão (um
// heartbeat renova o TTL mas NÃO minta novo token); só a expiração/lease e a perda
// de posse são mutáveis, guardadas por mu para o heartbeat e o loop coexistirem
// -race limpo.
type runSession struct {
	runID string
	plan  *RunPlan
	token durable.FencingToken

	mu      sync.Mutex
	lease   durable.Lease
	lostErr error
}

func newSession(plan *RunPlan, lease durable.Lease) *runSession {
	return &runSession{runID: plan.RunID, plan: plan, token: lease.Token, lease: lease}
}

func (s *runSession) currentLease() durable.Lease {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lease
}

func (s *runSession) renew(l durable.Lease) {
	s.mu.Lock()
	s.lease = l
	s.mu.Unlock()
}

func (s *runSession) lose(err error) {
	s.mu.Lock()
	if s.lostErr == nil {
		s.lostErr = err
	}
	s.mu.Unlock()
}

func (s *runSession) loss() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lostErr
}

// wrapLoss reclassifica um erro como [ErrLeaseLost] SE a posse já se perdeu — para
// que um erro secundário (p.ex. um Append que falha porque o heartbeat cancelou o
// ctx dos passos) seja reportado como a causa-raiz (perda de posse), e não como um
// context.Canceled opaco. Sem perda, devolve o erro tal como está.
func (s *runSession) wrapLoss(err error) error {
	if err == nil {
		return nil
	}
	if lost := s.loss(); lost != nil {
		return fmt.Errorf("%w: %v", ErrLeaseLost, lost)
	}
	return err
}
