package engine

import (
	"context"
	"errors"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/activity"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// Erros sentinela do adaptador de engine (AOS-022, fase feature).
// ---------------------------------------------------------------------------

var (
	// ErrNilStore — [NewOwnContractEngine] foi construído sem um Event Store (nil).
	// O Event Store replicado é a fonte de verdade do substrato (ADR-007): sem ele
	// não há ledger, checkpoint, resume nem replay.
	ErrNilStore = errors.New("engine: event store em falta")
	// ErrNilMediator — [NewOwnContractEngine] foi construído sem um Reference Monitor
	// (nil). Nenhum efeito externo corre fora do gate (ADR-002); sem Mediator o
	// dispatcher não teria a única via de despacho.
	ErrNilMediator = errors.New("engine: reference monitor (mediator) em falta")
)

// ---------------------------------------------------------------------------
// A PORTA: Engine — durable execution AGNÓSTICA AO BACKEND (Princípio 8, ADR-015).
// ---------------------------------------------------------------------------

// Engine é a PORTA de execução durável que o Agent Runtime usa SEM saber qual
// backend está por baixo (Princípio 8 / anti lock-in; ADR-015 ratificado). Expõe as
// operações do contrato de execução durável — despacho de efeitos, checkpoint/resume
// de cursor e replay determinístico — de forma independente do substrato que as
// implementa. O RT programa contra ESTA interface; trocar o backend (o adaptador de
// referência sobre o contrato próprio ↔ um engine externo Temporal/Restate/DBOS)
// NÃO altera a API nem o uso do RT.
//
// # Operações
//
//   - Dispatch: despacha UM efeito externo de forma IDEMPOTENTE (chave f(run_id,
//     step_id), AOS-014), MEDIADA pelo Reference Monitor (ADR-002) e REGISTADA no log
//     para replay (AOS-016). Delega no [activity.Dispatcher] (AOS-021).
//   - Checkpoint: persiste o cursor de progresso intra-iteração (AOS-015). O RT
//     chama-o por fase confirmada do turno.
//   - Resume: reconstrói o cursor de retoma de um run após crash/failover, para
//     retomar resume-from-step sem repetir passos confirmados (AOS-015).
//   - Replay: reconstrói a trajectória do log com fidelidade determinística e ZERO
//     efeitos externos (AOS-016) — a ferramenta de RCA / eval-driven development.
//   - Mode: o modo de despacho do engine ([activity.ModeNormal] executa e memoriza;
//     [activity.ModeReplay] só devolve o registado, sem efeito).
//
// # Contrato observável (o que qualquer backend TEM de honrar)
//
// Um backend conforme, seja o adaptador de referência ou um engine externo, tem de
// satisfazer as MESMAS invariantes observáveis pelo RT:
//   - idempotência: re-despachar o MESMO (run_id, step_id) NÃO produz um novo efeito
//     observável e devolve o resultado registado ([activity.Result.Deduplicated]);
//   - taint: o output vem SEMPRE untrusted (ADR-005);
//   - resume: após um crash, Resume devolve um cursor coerente (não FromScratch se
//     houve progresso confirmado) apontando ao próximo passo não confirmado;
//   - replay: Replay reconstrói a trajectória sem efeitos externos e reporta a
//     fidelidade (1.0 quando fiel), localizando qualquer divergência.
//
// É este contrato — e NÃO a forma do backend — que o teste de contrato de AOS-022
// exercita: o mesmo código de RT corre sobre o adaptador de referência e sobre um
// stub, provando o isolamento por contrato (Princípio 8).
//
// # Modelo canónico dos tipos de retorno (custo de tradução declarado, ADR-015)
//
// Os TIPOS que a porta devolve — [durable.ResumePoint] (cursor de retoma) e
// [replay.ReplayResult] (com Fidelity e *Divergence.Reason) — são o MODELO CANÓNICO
// do contrato PRÓPRIO (AOS-014/015/016), não um esperanto neutro ao backend. Tal como
// o adaptador externo subordina o seu log de durabilidade ao Event Store (ADR-007),
// subordina também o seu modelo de retoma/replay a ESTES DTOs: um backend
// Temporal/Restate/DBOS teria de TRADUZIR o seu estado interno (event history, journal,
// time-travel) para [durable.ResumePoint]/[replay.ReplayResult]. É um tradeoff
// deliberado do ADR-015 — a reversibilidade prometida vem ao custo de os backends
// externos adoptarem o modelo canónico do contrato próprio, não o inverso. Esta
// subordinação é parte declarada do ADR-015 (à semelhança da nota sobre o log de
// durabilidade), não um acoplamento acidental.
type Engine interface {
	// Dispatch despacha UMA activity (idempotente + mediada + registada). Ver
	// [activity.Dispatcher.Dispatch].
	Dispatch(ctx context.Context, act activity.Activity) (activity.Result, error)
	// Checkpoint persiste o cursor de progresso intra-iteração. Ver
	// [durable.EventStoreCheckpointer.Checkpoint].
	Checkpoint(ctx context.Context, cp agentruntime.Checkpoint) error
	// Resume reconstrói o cursor de retoma de um run. Ver [durable.Resumer.Resume].
	Resume(ctx context.Context, runID string) (durable.ResumePoint, error)
	// Replay reconstrói a trajectória do run com fidelidade determinística e zero
	// efeitos externos. Ver [replay.ReplayEngine.Replay].
	Replay(ctx context.Context, runID string, opts replay.Options) (replay.ReplayResult, error)
	// Mode devolve o modo de despacho do engine.
	Mode() activity.Mode
}

// ---------------------------------------------------------------------------
// O ADAPTADOR DE REFERÊNCIA: OwnContractEngine — o contrato próprio (AOS-014/015/
// 016/021) EXPOSTO pela porta Engine. COMPÕE as peças já Done; não reimplementa nada.
// ---------------------------------------------------------------------------

// OwnContractEngine é o adaptador de REFERÊNCIA da porta [Engine] sobre o CONTRATO
// PRÓPRIO do AOS (a decisão ratificada em ADR-015). É uma COMPOSIÇÃO — não
// reimplementa nenhuma garantia:
//
//	Dispatch   → *[activity.Dispatcher]              (AOS-021: idempotência AOS-014 +
//	                                                  mediação RM AOS-003 + taint +
//	                                                  registo para replay AOS-016)
//	Checkpoint → *[durable.EventStoreCheckpointer]   (AOS-015: cursor intra-iteração)
//	Resume     → *[durable.Resumer]                  (AOS-015: retoma resume-from-step)
//	Replay     → *[replay.ReplayEngine]              (AOS-016: replay determinístico)
//
// Todas as peças assentam no MESMO Event Store replicado (ADR-007, fonte de verdade
// única) — é precisamente o que distingue o contrato próprio dos engines externos,
// que trariam um segundo log de durabilidade (ver ADR-015 §2). A durabilidade do
// engine É o log append-only do ES; não há reconciliação de duas fontes de verdade.
//
// # Como um backend EXTERNO implementaria a MESMA porta (mapeamento; não implementado)
//
// A reversibilidade prometida pelo ADR-015 é concreta: um engine externo satisfaria
// [Engine] sem tocar no RT, mapeando cada operação ao seu modelo (ver também doc.go):
//
//	Operação    | Temporal                  | Restate                | DBOS
//	------------|---------------------------|------------------------|----------------------
//	Dispatch    | Activity idempotente       | Handler c/ idem. key   | @step transaccional
//	            | (activity-id determinístico| (journal + dedup)      | (registo em Postgres)
//	            |  = run_id:step_id)         |                        |
//	Checkpoint  | Event history (implícito)  | Journal do invocation  | Estado do workflow
//	Resume      | Replay do workflow         | Recuperação do journal | Recovery do workflow
//	Replay      | Replayer do SDK            | Re-execução do journal  | Time-travel do estado
//
// Em TODOS os casos o RT continuaria a chamar apenas os métodos de [Engine]; o
// adaptador externo subordinaria o seu log de durabilidade ao ES (ADR-007) — a
// fronteira que o ADR-015 documenta como custo de adoptar um engine externo.
//
// Sem estado mutável partilhado além das peças compostas (todas seguras para
// concorrência) ⇒ seguro para uso concorrente e -race limpo.
type OwnContractEngine struct {
	dispatcher   *activity.Dispatcher
	checkpointer *durable.EventStoreCheckpointer
	resumer      *durable.Resumer
	replayer     *replay.ReplayEngine
	ledger       *durable.StepLedger
}

// config acumula as opções antes de construir as peças.
type config struct {
	tracer       agentruntime.Tracer
	producer     eventstore.Producer
	stepIdentity agentruntime.StepIdentity
	ledger       *durable.StepLedger
	sensitive    bool
}

// Option configura o [OwnContractEngine].
type Option func(*config)

// WithTracer injecta o tracer de observabilidade propagado ao dispatcher (AOS-021)
// e ao replayer (AOS-016). Default: [agentruntime.NoopTracer].
func WithTracer(t agentruntime.Tracer) Option { return func(c *config) { c.tracer = t } }

// WithProducer define a identidade emissora (NHI + cadeia de delegação) gravada nos
// eventos do ledger (AOS-014) e de checkpoint (AOS-015). Default: Producer zero.
func WithProducer(p eventstore.Producer) Option { return func(c *config) { c.producer = p } }

// WithStepIdentity injecta o derivador de step_id usado pelo [durable.Resumer] para
// nomear o próximo passo numa fronteira de turno. DEVE ser o MESMO derivador que o
// loop usa (AOS-014), senão o Resume fail-closed com [durable.ErrStepIdentityMismatch].
// Default: um [durable.StepSequencer] com o formato canónico.
func WithStepIdentity(s agentruntime.StepIdentity) Option {
	return func(c *config) { c.stepIdentity = s }
}

// WithLedger injecta um step-ledger JÁ construído (AOS-014) em vez de o engine criar
// um novo. É o ponto de ligação para o CRASH/FAILOVER: um worker novo constrói um
// ledger, chama [durable.StepLedger.Rebuild] para recuperar o estado durável do log,
// e passa-o aqui — assim o novo engine partilha o estado reconstruído com o
// dispatcher. Sem esta opção, o engine cria um ledger vazio (fast-path in-memory
// frio; a correcção mantém-se pela dedup durável do ES + idempotência downstream).
func WithLedger(l *durable.StepLedger) Option { return func(c *config) { c.ledger = l } }

// WithSensitiveResults activa a guarda de segredos do ledger ([durable.WithSensitiveResults]):
// recusa memorizar Payload de resultado em claro (o chamador tem de passar uma
// referência). Só se aplica quando o engine cria o ledger (sem [WithLedger]).
func WithSensitiveResults() Option { return func(c *config) { c.sensitive = true } }

// NewOwnContractEngine constrói o adaptador de referência COMPONDO as peças já Done
// sobre o Event Store replicado dado e o Reference Monitor. store e rm são
// obrigatórios (não-nil). É o único ponto onde as peças são cabladas; o RT recebe a
// interface [Engine] e nunca vê o backend.
func NewOwnContractEngine(store durable.EventStore, rm activity.Mediator, opts ...Option) (*OwnContractEngine, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if rm == nil {
		return nil, ErrNilMediator
	}
	cfg := &config{tracer: agentruntime.NoopTracer{}}
	for _, o := range opts {
		o(cfg)
	}
	if cfg.tracer == nil {
		cfg.tracer = agentruntime.NoopTracer{}
	}

	// Ledger (AOS-014): injectado (worker que retomou, com Rebuild feito) ou novo.
	ledger := cfg.ledger
	if ledger == nil {
		ledgerOpts := []durable.LedgerOption{durable.WithProducer(cfg.producer)}
		if cfg.sensitive {
			ledgerOpts = append(ledgerOpts, durable.WithSensitiveResults())
		}
		var err error
		ledger, err = durable.NewStepLedger(store, ledgerOpts...)
		if err != nil {
			return nil, err
		}
	}

	// Dispatcher (AOS-021): idempotência + mediação + taint + registo. ModeNormal.
	dispatcher, err := activity.NewDispatcher(rm, ledger, activity.WithTracer(cfg.tracer))
	if err != nil {
		return nil, err
	}

	// Checkpointer (AOS-015): cursor intra-iteração append-only no ES.
	checkpointer, err := durable.NewCheckpointer(store, durable.WithCheckpointProducer(cfg.producer))
	if err != nil {
		return nil, err
	}

	// Resumer (AOS-015): retoma resume-from-step a partir dos checkpoints.
	resumerOpts := []durable.ResumerOption{}
	if cfg.stepIdentity != nil {
		resumerOpts = append(resumerOpts, durable.WithStepIdentity(cfg.stepIdentity))
	}
	resumer, err := durable.NewResumer(store, resumerOpts...)
	if err != nil {
		return nil, err
	}

	// Replayer (AOS-016): replay determinístico, só-leitura (zero efeitos estrutural).
	// store é durable.EventStore (Append+Read); o replayer só precisa de Read
	// (replay.EventReader) — a atribuição é válida porque o método set inclui Read.
	replayer, err := replay.NewEngine(store, replay.WithTracer(cfg.tracer))
	if err != nil {
		return nil, err
	}

	return &OwnContractEngine{
		dispatcher:   dispatcher,
		checkpointer: checkpointer,
		resumer:      resumer,
		replayer:     replayer,
		ledger:       ledger,
	}, nil
}

// Dispatch delega no [activity.Dispatcher]: idempotência (AOS-014) + mediação pelo
// Reference Monitor (ADR-002) + taint (ADR-005) + registo para replay (AOS-016).
func (e *OwnContractEngine) Dispatch(ctx context.Context, act activity.Activity) (activity.Result, error) {
	return e.dispatcher.Dispatch(ctx, act)
}

// Checkpoint delega no [durable.EventStoreCheckpointer]: persiste o cursor de
// progresso intra-iteração como evento append-only no Event Store (AOS-015).
func (e *OwnContractEngine) Checkpoint(ctx context.Context, cp agentruntime.Checkpoint) error {
	return e.checkpointer.Checkpoint(ctx, cp)
}

// Resume delega no [durable.Resumer]: reconstrói o cursor de retoma resume-from-step
// a partir dos checkpoints do run (AOS-015), sobrevivendo a failover de worker.
func (e *OwnContractEngine) Resume(ctx context.Context, runID string) (durable.ResumePoint, error) {
	return e.resumer.Resume(ctx, runID)
}

// Replay delega no [replay.ReplayEngine]: reconstrói a trajectória do run a partir
// do log com fidelidade determinística e ZERO efeitos externos (AOS-016).
func (e *OwnContractEngine) Replay(ctx context.Context, runID string, opts replay.Options) (replay.ReplayResult, error) {
	return e.replayer.Replay(ctx, runID, opts)
}

// Mode devolve o modo de despacho do engine (o do [activity.Dispatcher] composto).
func (e *OwnContractEngine) Mode() activity.Mode { return e.dispatcher.Mode() }

// Ledger expõe o step-ledger composto (AOS-014) — útil para um worker que retoma
// chamar [durable.StepLedger.Rebuild] e depois reconstruir um engine com
// [WithLedger] sobre o estado recuperado. Não faz parte da porta [Engine] (o RT não
// precisa dele); é uma conveniência de composição/operação.
func (e *OwnContractEngine) Ledger() *durable.StepLedger { return e.ledger }

// Verificação em tempo de compilação: o adaptador de referência SATISFAZ a porta.
// É a prova estática de que o contrato próprio implementa a durable execution
// agnóstica ao backend (ADR-015, fase feature).
var _ Engine = (*OwnContractEngine)(nil)
