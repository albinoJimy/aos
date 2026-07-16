package sandbox

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
)

// Launcher orquestra o ciclo de vida mediado de uma execução na sandbox:
// create → exec → destroy, selando cada transição no Event Store (run_id/step_id)
// e cobrindo o conjunto com um span execute_tool. O destroy é GARANTIDO (defer)
// mesmo em erro ou panic — não há microVMs órfãs. É a peça de execução; a
// FRONTEIRA de invocação (só o RM) é imposta por [MediatedLauncher].
type Launcher struct {
	driver    SandboxDriver
	sink      EventSink
	tracer    Tracer
	injector  CredentialInjector
	isolation Isolation
	seq       atomic.Uint64 // contador determinista de ids de instância (fallback)
}

// LauncherOption configura o [Launcher].
type LauncherOption func(*Launcher)

// WithEventSink injecta o sink durável do ciclo de vida (ver [NewEventStoreSink]).
func WithEventSink(s EventSink) LauncherOption { return func(l *Launcher) { l.sink = s } }

// WithTracer injecta a porta de observabilidade (default [NoopTracer]).
func WithTracer(t Tracer) LauncherOption { return func(l *Launcher) { l.tracer = t } }

// WithCredentialInjector injecta o resolvedor de credenciais por handle server-side
// (ADR-006). Default: nenhum (o handle é apenas propagado, opaco).
func WithCredentialInjector(ci CredentialInjector) LauncherOption {
	return func(l *Launcher) { l.injector = ci }
}

// WithIsolation sobrepõe a isolação exigida (default [HardenedIsolation]). O
// Launcher rejeita fail-closed uma isolação que não seja hardened.
func WithIsolation(iso Isolation) LauncherOption { return func(l *Launcher) { l.isolation = iso } }

// NewLauncher constrói um Launcher sobre o driver dado. Por omissão: isolação
// hardened (AOS-064), [NoopTracer] e [discardSink] (não-durável — produção DEVE
// injectar [WithEventSink] com um sink real).
func NewLauncher(driver SandboxDriver, opts ...LauncherOption) (*Launcher, error) {
	if driver == nil {
		return nil, ErrNilDriver
	}
	l := &Launcher{
		driver:    driver,
		sink:      discardSink{},
		tracer:    NoopTracer{},
		isolation: HardenedIsolation(),
	}
	for _, o := range opts {
		o(l)
	}
	if l.sink == nil {
		l.sink = discardSink{}
	}
	if l.tracer == nil {
		l.tracer = NoopTracer{}
	}
	return l, nil
}

// run é o ciclo de vida mediado. É NÃO-EXPORTADO por desenho: nenhum pacote
// externo o alcança — a ÚNICA via de entrada é a ToolFunc registada no RM por
// [MediatedLauncher] (no-bypass estrutural, ADR-002). Devolve o [ExecResult]
// (SEMPRE untrusted) e, separadamente, o erro DA EXECUÇÃO na sandbox (não-fatal
// para o loop: materializa-se como resultado de tool falhado) — exceto erros de
// isolamento/audit, que são fail-closed e impedem o efeito.
func (l *Launcher) run(ctx context.Context, req ExecRequest) (ExecResult, error) {
	if err := req.validate(); err != nil {
		return ExecResult{}, err
	}
	// Fail-closed de isolamento: nunca correr sem a fronteira hardened (ADR-004).
	if !l.isolation.hardened() {
		return ExecResult{}, ErrSharedNamespaceForbidden
	}
	if !l.isolation.NoHostSocket {
		return ExecResult{}, ErrHostSocketForbidden
	}

	ctx, span := l.tracer.StartSpan(ctx, OpExecuteTool)
	span.SetAttribute(AttrOperationName, OpExecuteTool)
	span.SetAttribute(AttrRunID, req.RunID)
	span.SetAttribute(AttrStepID, req.StepID)
	span.SetAttribute(AttrToolName, req.Call.ToolID)
	span.SetAttribute(AttrDriver, string(l.driver.Kind()))
	span.SetAttribute(AttrTaint, string(TaintUntrusted))
	if req.CredentialsHandle != "" {
		// O HANDLE é opaco (não-secreto); o segredo NUNCA chega ao span (ADR-006).
		span.SetAttribute(AttrCredHandle, req.CredentialsHandle)
	}
	defer span.End()

	cap := capability{launcher: l}
	spec := Spec{
		RunID:     req.RunID,
		StepID:    req.StepID,
		Kind:      l.driver.Kind(),
		Isolation: l.isolation,
	}

	// (1) CREATE — arranca a microVM.
	inst, err := l.driver.Create(ctx, cap, spec)
	if err != nil {
		return ExecResult{}, fmt.Errorf("sandbox create: %w", err)
	}
	if inst.ID == "" {
		inst.ID = l.fallbackID(req)
	}
	span.SetAttribute(AttrInstanceID, inst.ID)

	// DESTROY GARANTIDO: registado já, corre no defer mesmo em erro/panic. Sem
	// microVMs órfãs (ADR-004). O evento destroyed sela sempre.
	defer func() {
		// Contexto de cleanup independente do cancelamento do ctx de execução: o
		// destroy tem de correr mesmo que o ctx do chamador tenha expirado.
		dctx := context.WithoutCancel(ctx)
		_ = l.driver.Destroy(dctx, cap, inst)
		_, _ = l.sink.RecordLifecycle(dctx, LifecycleEvent{
			RunID: req.RunID, StepID: req.StepID, Phase: PhaseDestroyed,
			InstanceID: inst.ID, Driver: inst.Kind, Isolation: l.isolation,
			CredentialsHandle: req.CredentialsHandle,
		})
	}()

	// (2) AUDIT-BEFORE-EFFECT — o create sela ANTES do efeito de exec. Fail-closed:
	// se a transição não puder ser auditada, o exec NÃO corre (mas o destroy sim).
	if _, err := l.sink.RecordLifecycle(ctx, LifecycleEvent{
		RunID: req.RunID, StepID: req.StepID, Phase: PhaseCreated,
		InstanceID: inst.ID, Driver: inst.Kind, Isolation: l.isolation,
		CredentialsHandle: req.CredentialsHandle,
	}); err != nil {
		return ExecResult{}, fmt.Errorf("sandbox audit(created): %w", err)
	}

	// (3) CREDENCIAL POR HANDLE — resolução/injecção server-side (ADR-006), se
	// configurada. O segredo entra no guest, nunca no resultado/eventos/spans.
	if l.injector != nil && req.CredentialsHandle != "" {
		if err := l.injector.Inject(ctx, req.CredentialsHandle, inst); err != nil {
			return ExecResult{}, fmt.Errorf("sandbox credential inject: %w", err)
		}
	}

	// (4) EXEC — corre a tool call DENTRO da microVM.
	res, execErr := l.driver.Exec(ctx, cap, inst, req)

	// Custo da execução (ADR-010). A metering REAL (tokens/tempo de CPU/egress) é
	// EPIC-08; aqui o valor é um placeholder explícito (0) — mas a DIMENSÃO de custo
	// está presente no span e no evento de exec, satisfazendo o contrato observável
	// sem fabricar valores. Quando a metering existir, deriva-se aqui.
	const costMicroUSD int64 = 0

	// (5) Sela o exec (resultado untrusted; sem segredo). O custo por span/evento
	// acompanha a fase de exec.
	if _, err := l.sink.RecordLifecycle(ctx, LifecycleEvent{
		RunID: req.RunID, StepID: req.StepID, Phase: PhaseExec,
		InstanceID: inst.ID, Driver: inst.Kind, Isolation: l.isolation,
		ExitCode: res.ExitCode, CostMicroUSD: costMicroUSD, CredentialsHandle: req.CredentialsHandle,
	}); err != nil {
		return ExecResult{}, fmt.Errorf("sandbox audit(exec): %w", err)
	}
	span.SetAttribute(AttrExitCode, res.ExitCode)
	// Custo por span (ADR-010): a dimensão está sempre presente (placeholder até à
	// metering de EPIC-08), nunca um segredo.
	span.SetAttribute(AttrCostUSD, float64(costMicroUSD)/1e6)

	if execErr != nil {
		// Erro DA EXECUÇÃO na sandbox (ex.: tentativa de escape bloqueada): não-fatal
		// para o loop, mas propagado para o chamador materializar. O resultado é
		// vazio-untrusted.
		return ExecResult{}, execErr
	}
	return res, nil
}

// fallbackID deriva um id de instância determinista quando o driver não fornece
// um (não deve acontecer com os drivers first-party; é uma rede de segurança).
func (l *Launcher) fallbackID(req ExecRequest) string {
	return "sbx-" + req.RunID + "-" + req.StepID + "-" + strconv.FormatUint(l.seq.Add(1), 10)
}
