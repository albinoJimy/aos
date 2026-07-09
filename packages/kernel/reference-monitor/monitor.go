package referencemonitor

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"
)

// ToolFunc é a assinatura de uma tool despachável. Recebe o input opaco do call
// e devolve o resultado (marcado untrusted a jusante) ou um erro de execução.
//
// IMPORTANTE (no-bypass): um valor ToolFunc NUNCA deve ser invocado
// directamente fora do RM. A única via legítima de execução é [Monitor.Mediate],
// que só despacha após permit. O lint de arquitectura (subpacote archlint)
// sinaliza invocações directas de ToolFunc fora deste pacote.
type ToolFunc func(ctx context.Context, input []byte) ([]byte, error)

// Metrics são contadores de observabilidade leve (sem SDK OTel — isso é
// EPIC-08). Todos os acessos são atómicos.
type Metrics struct {
	Permits     atomic.Uint64
	Denials     atomic.Uint64
	Escalations atomic.Uint64
}

// Snapshot devolve uma leitura consistente-o-suficiente dos contadores.
func (m *Metrics) Snapshot() (permits, denials, escalations uint64) {
	return m.Permits.Load(), m.Denials.Load(), m.Escalations.Load()
}

// Monitor é o Reference Monitor: o PEP mandatório do AOS. Construir com [New].
type Monitor struct {
	hooks []Hook
	sink  EventSink

	mu    sync.RWMutex
	tools map[string]ToolFunc

	metrics Metrics

	now  func() time.Time
	rand func() uint64
}

// Option configura o Monitor na construção.
type Option func(*Monitor)

// WithHooks substitui a cadeia de hooks. A ORDEM dada é a ordem de invocação —
// o RM não a reordena nem valida a presença de hooks específicos; a ordem
// canónica de mediação (identity → policy → budget → egress → audit) é a que
// [DefaultHooks] fornece, e produção deve fornecer a cadeia completa. Uma cadeia
// VAZIA não abre exceção: [Monitor.Mediate] nega-a fail-closed (não permite
// tudo silenciosamente). Use [DefaultHooks] como base.
func WithHooks(hooks ...Hook) Option {
	return func(m *Monitor) { m.hooks = hooks }
}

// WithEventSink injecta o sink de auditoria durável (ver [NewEventStoreSink]).
func WithEventSink(s EventSink) Option {
	return func(m *Monitor) { m.sink = s }
}

// withClock injecta um relógio (uso interno/testes).
func withClock(f func() time.Time) Option {
	return func(m *Monitor) { m.now = f }
}

// withNonce injecta a fonte de nonce (uso interno/testes determinísticos).
func withNonce(f func() uint64) Option {
	return func(m *Monitor) { m.rand = f }
}

// New constrói um Monitor. Por omissão: cadeia de stubs neutros ([DefaultHooks])
// e [discardSink] (não-durável — produção DEVE injectar [WithEventSink] com um
// sink real, senão o fail-closed de auditoria não tem efeito).
func New(opts ...Option) *Monitor {
	m := &Monitor{
		hooks: DefaultHooks(),
		sink:  discardSink{},
		tools: make(map[string]ToolFunc),
		now:   time.Now,
		rand:  rand.Uint64,
	}
	for _, o := range opts {
		o(m)
	}
	if m.sink == nil {
		m.sink = discardSink{}
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.rand == nil {
		m.rand = rand.Uint64
	}
	return m
}

// Register associa um ToolID a uma ToolFunc. O registo é imutável: re-registar o
// mesmo ToolID devolve [ErrToolAlreadyRegistered]. Uma tool não registada é
// negada por omissão (default-deny). Register é seguro para concorrência.
func (m *Monitor) Register(toolID string, fn ToolFunc) error {
	if toolID == "" || fn == nil {
		return ErrInvalidRegistration
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.tools[toolID]; exists {
		return ErrToolAlreadyRegistered
	}
	m.tools[toolID] = fn
	return nil
}

// Metrics devolve o ponteiro para os contadores de observabilidade.
func (m *Monitor) Metrics() *Metrics { return &m.metrics }

// Mediate é a SUPERFÍCIE ÚNICA de autorização e despacho de tool calls. Executa
// a cadeia de hooks configurada pela ordem em que foi fornecida (a ordem
// canónica identity → policy → budget → egress → audit é a de [DefaultHooks]) e,
// só se todos permitirem, grava o evento de mediação e despacha a tool via o
// dispatcher interno. Uma cadeia de hooks vazia é negada fail-closed.
//
// Garantias (fail-closed, ADR-002 / contrato C1):
//   - qualquer hook que devolva deny/escalate, erro ou panic → Decision Deny/
//     Escalate, evento de negação gravado (best-effort), tool NÃO despachada;
//   - tool não registada → Deny (default-deny);
//   - no caminho de permit, o evento de mediação é gravado ANTES do despacho;
//     se o registo falhar, a decisão degrada para Deny (auditoria fail-closed);
//   - Mediate nunca devolve Permit sem ter despachado sob um Permit válido.
//
// O erro devolvido é reservado a cancelamento de contexto; as negações de
// política são comunicadas via Decision.Effect, não via error.
func (m *Monitor) Mediate(ctx context.Context, call Call) (Decision, error) {
	start := m.now()
	if err := ctx.Err(); err != nil {
		// Contexto já cancelado: fail-closed, sem sequer avaliar. Esta negação é
		// DELIBERADAMENTE não-auditada: gravar no Event Store exigiria o mesmo
		// contexto (já cancelado) e falharia de qualquer forma. É o único caminho
		// de deny sem registo; todos os outros passam por fail() (best-effort).
		d := Decision{Effect: EffectDeny, Code: CodeContextCanceled, DeniedBy: "context", Reason: err.Error(), Latency: m.now().Sub(start)}
		m.metrics.Denials.Add(1)
		return d, err
	}

	// 0) fail-closed de configuração: uma cadeia de hooks vazia NÃO permite tudo.
	//    A ausência de pontos de decisão é tratada como negação (o default seguro
	//    é [DefaultHooks]; [WithHooks] com cadeia vazia é misconfiguração).
	if len(m.hooks) == 0 {
		return m.fail(ctx, call, EffectDeny, CodeEmptyHookChain, "config",
			"cadeia de hooks vazia (fail-closed)", start), nil
	}

	// 1) Cadeia de hooks pela ordem fornecida (ver [WithHooks]; a ordem canónica
	//    identity → policy → budget → egress → audit é a de [DefaultHooks]). O
	//    call é partilhado por ponteiro para permitir resolução de identidade e
	//    propagação de contexto entre hooks.
	var obligations []Obligation
	for _, h := range m.hooks {
		res, err := safeEvaluate(ctx, h, &call)
		switch {
		case err != nil:
			return m.fail(ctx, call, EffectDeny, CodeHookError, h.Name(), fmt.Sprintf("hook %q: %v", h.Name(), err), start), nil
		case res.Decision == HookDeny:
			reason := res.Reason
			if reason == "" {
				reason = fmt.Sprintf("negado por %q", h.Name())
			}
			return m.fail(ctx, call, EffectDeny, CodeDeniedByHook, h.Name(), reason, start), nil
		case res.Decision == HookEscalate:
			reason := res.Reason
			if reason == "" {
				reason = fmt.Sprintf("escalado por %q", h.Name())
			}
			return m.fail(ctx, call, EffectEscalate, CodeEscalated, h.Name(), reason, start), nil
		}
		obligations = append(obligations, res.Obligations...)
	}

	// 2) default-deny: a tool tem de estar registada para poder ser despachada.
	m.mu.RLock()
	_, registered := m.tools[call.ToolID]
	m.mu.RUnlock()
	if !registered {
		return m.fail(ctx, call, EffectDeny, CodeToolNotRegistered, "dispatch", "tool nao registada (default-deny)", start), nil
	}

	// 3) Auditoria ANTES do efeito (audit-before-effect). Se falhar, fail-closed.
	rec := MediationRecord{
		RequestID: call.RequestID,
		RunID:     call.RunID, StepID: call.StepID, ParentStepID: call.ParentStepID,
		Effect: EffectPermit, ToolID: call.ToolID, Capability: call.Capability,
		Resource: call.Resource, Context: call.Context,
		Principal: call.Principal, Latency: m.now().Sub(start), Obligations: obligations,
	}
	seq, err := m.sink.RecordMediation(ctx, rec)
	if err != nil {
		// Uma acção não-auditável não é permitida (ADR-002/010).
		d := m.fail(ctx, call, EffectDeny, CodeAuditUnavailable, "audit-sink",
			fmt.Sprintf("%s: %v", ErrAuditUnavailable.msg, err), start)
		return d, nil
	}

	// 4) Permit: mintar o Permit não-forjável e despachar via dispatcher interno.
	p := m.mint(call)
	out, toolErr := m.dispatch(ctx, p, call)

	m.metrics.Permits.Add(1)
	return Decision{
		Effect:       EffectPermit,
		Reason:       "permitido pela cadeia de mediacao",
		Obligations:  obligations,
		Latency:      m.now().Sub(start),
		MediationSeq: seq,
		Output:       out,
		ToolErr:      toolErr,
		permit:       p,
	}, nil
}

// fail constrói uma Decision de negação/escalonamento, grava o evento
// correspondente (best-effort — a negação nunca deve ser bloqueada por uma
// falha de registo) e actualiza métricas. Nunca despacha a tool.
func (m *Monitor) fail(ctx context.Context, call Call, eff Effect, code, deniedBy, reason string, start time.Time) Decision {
	latency := m.now().Sub(start)
	// Registo best-effort: em deny/escalate o efeito já está bloqueado, pelo que
	// uma falha de auditoria não altera a decisão (contrasta com o permit path).
	seq, _ := m.sink.RecordMediation(ctx, MediationRecord{
		RequestID: call.RequestID,
		RunID:     call.RunID, StepID: call.StepID, ParentStepID: call.ParentStepID,
		Effect: eff, Code: code, DeniedBy: deniedBy, Reason: reason,
		ToolID: call.ToolID, Capability: call.Capability,
		Resource: call.Resource, Context: call.Context,
		Principal: call.Principal, Latency: latency,
	})
	if eff == EffectEscalate {
		m.metrics.Escalations.Add(1)
	} else {
		m.metrics.Denials.Add(1)
	}
	return Decision{
		Effect:       eff,
		Code:         code,
		Reason:       reason,
		DeniedBy:     deniedBy,
		Latency:      latency,
		MediationSeq: seq,
	}
}

// mint emite um Permit não-forjável ligado ao fingerprint do call. Só este
// método (invocado dentro de Mediate) consegue construir um permitToken válido.
func (m *Monitor) mint(call Call) *Permit {
	return &Permit{
		tok: &permitToken{
			fingerprint: fingerprint(call),
			nonce:       m.rand(),
		},
	}
}

// dispatch é o dispatcher INTERNO (não-exportado): o único caminho que executa
// uma tool. Exige um Permit válido — não-nil, com token minta­do por este RM,
// correspondente ao call e ainda não usado (uso único). Código externo não
// consegue construir um Permit aceite aqui nem alcançar este método.
func (m *Monitor) dispatch(ctx context.Context, p *Permit, call Call) ([]byte, error) {
	if p == nil || p.tok == nil {
		return nil, ErrInvalidPermit
	}
	if p.tok.fingerprint != fingerprint(call) {
		return nil, ErrInvalidPermit
	}
	// Uso único: consome o permit atomicamente. Uma segunda tentativa falha.
	if !p.tok.used.CompareAndSwap(false, true) {
		return nil, ErrInvalidPermit
	}
	m.mu.RLock()
	fn, ok := m.tools[call.ToolID]
	m.mu.RUnlock()
	if !ok {
		return nil, ErrToolNotRegistered
	}
	return fn(ctx, call.Input)
}

// safeEvaluate invoca um hook com recuperação de panic. Um panic converte-se em
// erro (fail-closed): a mediação nega em vez de propagar a falha.
func safeEvaluate(ctx context.Context, h Hook, call *Call) (res HookResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			res = HookResult{Decision: HookDeny}
			err = fmt.Errorf("%w: %q: %v", ErrHookPanic, h.Name(), r)
		}
	}()
	return h.Evaluate(ctx, call)
}
