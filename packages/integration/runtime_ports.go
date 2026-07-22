package integration

import (
	"context"
	"errors"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/activity"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/memory/compression"
	"github.com/aos-ref/platform/memory/working"
)

// Este ficheiro adapta os CONCRETOS dos pilares às PORTAS RT/RM que o loop base define
// (AOS-157, idioma AOS-060 — porta no kernel, adaptador no apex). São opt-in: passam-se
// ao runtime via [agentruntime.Option] (ex.: em SecuredConfig.RuntimeOptions). Sem eles o
// loop corre com os defaults byte-idênticos de AOS-013.
//
//   - [WindowManagerFactory] → agentruntime.WindowFactory (AOS-037): o working.WindowManager
//     passa a ser o DONO ÚNICO do tail/assembly do run (resolução D-TAIL).
//   - [CompactionTriggerAdapter] → agentruntime.CompactionTrigger (AOS-043): o
//     compression.CheckpointTrigger observa o sinal de fim-de-turno e enfileira compressão.
//   - [DurableDispatcher] → agentruntime.ActivityDispatcher (AOS-021): o activity.Dispatcher
//     acrescenta idempotência/replay durável à volta de Mediate, PRESERVANDO o Credential
//     (AOS-152) do Call construído pelo loop.

// Erros de construção dos adaptadores (fail-closed, comparáveis com errors.Is).
var (
	// ErrNilTrigger — [NewCompactionTriggerAdapter] sem CheckpointTrigger.
	ErrNilTrigger = errors.New("integration: compression.CheckpointTrigger nil")
	// ErrNilSourceBuilder — [NewCompactionTriggerAdapter] sem construtor de source.
	ErrNilSourceBuilder = errors.New("integration: CompactionSourceBuilder nil")
	// ErrNilDispatcher — [NewDurableDispatcher] sem activity.Dispatcher.
	ErrNilDispatcher = errors.New("integration: activity.Dispatcher nil")
	// ErrInvalidTokenLimit — [NewWindowManagerFactory] com limite de tokens não-positivo.
	ErrInvalidTokenLimit = errors.New("integration: ModelTokenLimit tem de ser > 0")
)

// ---------------------------------------------------------------------------
// AOS-037 — WindowManagerFactory → agentruntime.WindowFactory
// ---------------------------------------------------------------------------

// WindowFactoryOption configura a [WindowManagerFactory].
type WindowFactoryOption func(*WindowManagerFactory)

// WithExhaustionRatio sobrepõe o rácio de exaustão graciosa (default ~0.80).
func WithExhaustionRatio(r float64) WindowFactoryOption {
	return func(f *WindowManagerFactory) { f.ratio = r }
}

// WithTokenEstimator injecta o estimador de tokens (default DefaultTokenEstimator).
func WithTokenEstimator(e working.TokenEstimator) WindowFactoryOption {
	return func(f *WindowManagerFactory) {
		if e != nil {
			f.estimator = e
		}
	}
}

// WithWindowTracer injecta o tracer OTel da janela (default NoopTracer).
func WithWindowTracer(t agentruntime.Tracer) WindowFactoryOption {
	return func(f *WindowManagerFactory) {
		if t != nil {
			f.tracer = t
		}
	}
}

// WithEvictionSink injecta o sink de preservação de eviction (Princípio 4, AOS-036).
func WithEvictionSink(s working.EvictionSink) WindowFactoryOption {
	return func(f *WindowManagerFactory) { f.sink = s }
}

// WindowManagerFactory constrói um [working.WindowManager] por run e adapta-o à
// [agentruntime.WindowFactory]. É o DONO ÚNICO do tail/assembly (D-TAIL): quando ligada
// via [agentruntime.WithWindowFactory], o loop delega-lhe a posse do tail e a montagem,
// pelo que existe um só prefix-hash por run — o do WindowManager.
type WindowManagerFactory struct {
	modelTokenLimit int
	ratio           float64
	estimator       working.TokenEstimator
	tracer          agentruntime.Tracer
	sink            working.EvictionSink
}

// NewWindowManagerFactory constrói a fábrica. modelTokenLimit é o limite de tokens do
// modelo para a janela (obrigatório, > 0).
func NewWindowManagerFactory(modelTokenLimit int, opts ...WindowFactoryOption) (*WindowManagerFactory, error) {
	if modelTokenLimit <= 0 {
		return nil, ErrInvalidTokenLimit
	}
	f := &WindowManagerFactory{modelTokenLimit: modelTokenLimit}
	for _, o := range opts {
		o(f)
	}
	return f, nil
}

// NewWindow implementa [agentruntime.WindowFactory]: congela o prefixo do run e devolve
// o gestor da janela adaptado à [agentruntime.WindowPort].
func (f *WindowManagerFactory) NewWindow(runID, system string, tools []agentruntime.ToolSpec) (agentruntime.WindowPort, error) {
	wm, err := working.NewWindowManager(working.Config{
		RunID:           runID,
		System:          system,
		Tools:           tools,
		ModelTokenLimit: f.modelTokenLimit,
		ExhaustionRatio: f.ratio,
		Estimator:       f.estimator,
		Tracer:          f.tracer,
		Sink:            f.sink,
	})
	if err != nil {
		return nil, err
	}
	return &windowManagerPort{wm: wm}, nil
}

// windowManagerPort adapta o [working.WindowManager] à [agentruntime.WindowPort].
type windowManagerPort struct {
	wm *working.WindowManager
}

// Append delega em WindowManager.Append. working.TailKind é um alias de
// agentruntime.TailKind, logo o Kind atravessa sem conversão. A prioridade de eviction
// fica no default (0): não afecta os bytes materializados (só a ordem de eviction).
func (p *windowManagerPort) Append(seg agentruntime.TailSegment) {
	p.wm.Append(working.TailInput{Kind: seg.Kind, Content: string(seg.Content)})
}

// Assemble materializa o turno via WindowManager.Turn. O WindowManager usa o SEU
// contador de turno interno (0-based) para o span/SLI; o índice de turno da vista é
// SOBREPOSTO pelo do loop (a fonte da verdade do índice), o que é seguro porque os bytes
// materializados e os hashes NÃO dependem do turno — só do prefixo e do tail.
func (p *windowManagerPort) Assemble(ctx context.Context, turn int) agentruntime.PromptView {
	tr := p.wm.Turn(ctx)
	view := tr.View
	view.Turn = turn
	return view
}

// SystemHash delega no assembler congelado do WindowManager (para o manifesto do loop).
func (p *windowManagerPort) SystemHash() string { return p.wm.SystemHash() }

// Signal projecta o sinal de exaustão do WindowManager no tipo kernel-local.
func (p *windowManagerPort) Signal() agentruntime.WindowSignal {
	s := p.wm.Signal()
	return agentruntime.WindowSignal{
		Triggered:       s.Triggered,
		Action:          string(s.Action),
		OccupancyTokens: s.Occupancy.Total(),
		LimitTokens:     s.Occupancy.Limit,
	}
}

var _ agentruntime.WindowFactory = (*WindowManagerFactory)(nil)
var _ agentruntime.WindowPort = (*windowManagerPort)(nil)

// ---------------------------------------------------------------------------
// AOS-043 — CompactionTriggerAdapter → agentruntime.CompactionTrigger
// ---------------------------------------------------------------------------

// CompactionSourceBuilder constrói a [compression.CompactionSource] a enfileirar para um
// run na fronteira de fim-de-turno. Devolve ok=false quando ainda não há nada a compactar
// (o adaptador não enfileira). O apex fornece-o porque é ele que detém o registo/trajectória
// (Event Store) com o CONTEÚDO a compactar — a porta do loop só entrega o SINAL, nunca o
// conteúdo (o kernel não conhece a semântica de compressão).
type CompactionSourceBuilder func(runID string, turn int) (compression.CompactionSource, bool)

// CompactionTriggerAdapter adapta o [compression.CheckpointTrigger] à
// [agentruntime.CompactionTrigger]: mapeia o sinal kernel-local para working.Exhaustion e
// observa-o no trigger, que enfileira a compressão para correr FORA do turno.
type CompactionTriggerAdapter struct {
	trigger *compression.CheckpointTrigger
	source  CompactionSourceBuilder
}

// NewCompactionTriggerAdapter constrói o adaptador. Ambos obrigatórios (fail-closed).
func NewCompactionTriggerAdapter(trigger *compression.CheckpointTrigger, source CompactionSourceBuilder) (*CompactionTriggerAdapter, error) {
	if trigger == nil {
		return nil, ErrNilTrigger
	}
	if source == nil {
		return nil, ErrNilSourceBuilder
	}
	return &CompactionTriggerAdapter{trigger: trigger, source: source}, nil
}

// Observe implementa [agentruntime.CompactionTrigger]. Só enfileira se o sinal disparou E
// o construtor devolve uma source. Backpressure de fila cheia NÃO é fatal ao run (a
// compressão é preparação, não hard-stop): absorve-se devolvendo (false, nil).
func (a *CompactionTriggerAdapter) Observe(ctx context.Context, runID string, turn int, sig agentruntime.WindowSignal) (bool, error) {
	if !sig.Triggered {
		return false, nil
	}
	src, ok := a.source(runID, turn)
	if !ok {
		return false, nil
	}
	ex := working.Exhaustion{
		Triggered: sig.Triggered,
		Action:    working.ExhaustionAction(sig.Action),
	}
	enqueued, err := a.trigger.Observe(ctx, ex, src)
	if errors.Is(err, compression.ErrQueueFull) {
		return false, nil
	}
	return enqueued, err
}

var _ agentruntime.CompactionTrigger = (*CompactionTriggerAdapter)(nil)

// ---------------------------------------------------------------------------
// AOS-021 — DurableDispatcher → agentruntime.ActivityDispatcher
// ---------------------------------------------------------------------------

// DurableDispatcher adapta o [activity.Dispatcher] à [agentruntime.ActivityDispatcher]:
// acrescenta idempotência/replay durável (step-ledger, AOS-014) à volta da MESMA mediação
// do RM, PRESERVANDO o Credential (AOS-152) do Call construído pelo loop — o campo
// Activity.Credential propaga-se ao Call que o dispatcher medeia, pelo que a identidade
// nunca se perde.
//
// SEMÂNTICA de deny: o activity.Dispatcher reporta um deny do RM como erro
// ([activity.ErrMediationDenied]); o loop base espera uma Decision de Deny não-fatal (que
// materializa um resultado vazio untrusted no tail). O adaptador traduz esse erro numa
// Decision{Effect: Deny}, mantendo o contrato do loop.
//
// LIMITAÇÃO (documentada): o [activity.Result] memoriza o Output mas NÃO transporta o
// erro de RUNTIME da tool (ToolErr) — uma tool PERMITIDA que falhe devolve Output vazio
// sem o marcador de erro. O default [directDispatcher] preserva o ToolErr; esta via
// durável, ao memoizar pelo ledger, perde-o. É aceitável para o efeito idempotente
// (o desfecho memorizado é o Output), mas o modelo não vê "tool_error=" nesta via.
type DurableDispatcher struct {
	dispatcher *activity.Dispatcher
}

// NewDurableDispatcher constrói o adaptador sobre um activity.Dispatcher (tipicamente em
// ModeNormal, sobre rm + durable.StepLedger).
func NewDurableDispatcher(d *activity.Dispatcher) (*DurableDispatcher, error) {
	if d == nil {
		return nil, ErrNilDispatcher
	}
	return &DurableDispatcher{dispatcher: d}, nil
}

// Dispatch implementa [agentruntime.ActivityDispatcher]. Traduz o Call completo do loop
// numa activity.Activity (preservando o Credential), despacha idempotentemente e converte
// o desfecho de volta numa [referencemonitor.Decision].
func (d *DurableDispatcher) Dispatch(ctx context.Context, call referencemonitor.Call) (referencemonitor.Decision, error) {
	act := activity.Activity{
		RunID:                 call.RunID,
		StepID:                call.StepID,
		ToolID:                call.ToolID,
		Capability:            call.Capability,
		Resource:              call.Resource,
		Principal:             call.Principal,
		Credential:            call.Credential, // AOS-152: a identidade não se perde
		Reversibility:         call.Context.Reversibility,
		Sensitivity:           call.Context.Sensitivity,
		BudgetTokensRemaining: call.Context.BudgetTokensRemaining,
		Input:                 call.Input,
	}
	res, err := d.dispatcher.Dispatch(ctx, act)
	if err != nil {
		if errors.Is(err, activity.ErrMediationDenied) {
			// Deny não é fatal ao loop: Decision de Deny (output vazio, untrusted).
			return referencemonitor.Decision{Effect: referencemonitor.EffectDeny}, nil
		}
		return referencemonitor.Decision{}, err // cancelamento/erro fatal do loop
	}
	// Permit (efeito corrido ou dedup do ledger): o Output volta ao loop, untrusted.
	return referencemonitor.Decision{
		Effect: referencemonitor.EffectPermit,
		Output: res.Output.Value,
	}, nil
}

var _ agentruntime.ActivityDispatcher = (*DurableDispatcher)(nil)
