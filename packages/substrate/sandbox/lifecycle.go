package sandbox

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"

	"github.com/aos-ref/substrate/sandbox/seccomp"
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
	// seccomp é o perfil seccomp default-deny aplicado à microVM (AOS-066). O seu
	// HASH entra no manifesto de cada execução. NUNCA nil após [NewLauncher]
	// (default: o perfil embebido) — a microVM não corre sem perfil (fail-closed).
	seccomp *seccomp.Profile
	// imageVersion é a versão da imagem base read-only da microVM (AOS-066),
	// gravada no manifesto. Vazia se não configurada.
	imageVersion ImageVersion
	// snapshot é o snapshot base IMUTÁVEL (AOS-065) a partir do qual cada execução
	// materializa uma montagem raiz-read-only + overlay efémero (AOS-066). Quando
	// configurado ([WithSnapshot]), o rootfs é EFETIVAMENTE montado e imposto no
	// caminho de execução (o base nunca é mutado; o overlay é descartado no destroy)
	// e o manifesto atesta o base digest/overlay REAIS. Nil mantém o jail in-memory
	// legado (a raiz read-only permanece uma declaração de config no manifesto).
	snapshot *Snapshot
	seq      atomic.Uint64 // contador determinista de ids de instância (fallback)
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

// WithSeccompProfile sobrepõe o perfil seccomp aplicado (default: o perfil embebido
// de [seccomp.Load]). Um perfil nil é ignorado (mantém-se o default) — a microVM
// nunca corre sem perfil seccomp (AOS-066, fail-closed).
func WithSeccompProfile(p *seccomp.Profile) LauncherOption {
	return func(l *Launcher) {
		if p != nil {
			l.seccomp = p
		}
	}
}

// WithImageVersion regista a versão da imagem base read-only da microVM no
// manifesto de cada execução (AOS-066). Opcional.
func WithImageVersion(v ImageVersion) LauncherOption {
	return func(l *Launcher) { l.imageVersion = v }
}

// WithSnapshot LIGA o modelo raiz-read-only + overlay efémero (AOS-066 sobre
// AOS-065) ao caminho de execução: por CADA execução, o [Launcher] restaura um
// [Overlay] novo do snapshot base imutável, monta-o read-only ([MountReadOnly]) e
// propaga o [RootFS] para o driver via [Spec.RootFS] — as escritas fazem copy-up
// para o overlay, a raiz nunca é mutada, e o overlay é DESCARTADO no destroy (a
// execução N+1 não observa a de N). Sem esta opção, a raiz read-only permanece
// apenas uma declaração de configuração no manifesto (o jail in-memory legado
// corre). Um snapshot nil é ignorado.
func WithSnapshot(s *Snapshot) LauncherOption {
	return func(l *Launcher) {
		if s != nil {
			l.snapshot = s
		}
	}
}

// NewLauncher constrói um Launcher sobre o driver dado. Por omissão: isolação
// hardened (AOS-064), [NoopTracer] e [discardSink] (não-durável — produção DEVE
// injectar [WithEventSink] com um sink real).
func NewLauncher(driver SandboxDriver, opts ...LauncherOption) (*Launcher, error) {
	if driver == nil {
		return nil, ErrNilDriver
	}
	// Perfil seccomp default-deny embebido (AOS-066). Fail-closed: se o perfil não
	// carregar/validar, o Launcher não é construído (a microVM nunca corre sem um
	// perfil seccomp válido). As opções podem sobrepô-lo por [WithSeccompProfile].
	prof, err := seccomp.Load()
	if err != nil {
		return nil, fmt.Errorf("sandbox seccomp: %w", err)
	}
	l := &Launcher{
		driver:    driver,
		sink:      discardSink{},
		tracer:    NoopTracer{},
		isolation: HardenedIsolation(),
		seccomp:   prof,
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
	if l.seccomp == nil {
		return nil, ErrNilSeccompProfile
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
	// Fail-closed AOS-066: a raiz de FS TEM de ser read-only (o overlay efémero é a
	// única camada de escrita). Nunca correr com a raiz escrevível.
	if !l.isolation.RootFSReadOnly {
		return ExecResult{}, ErrReadOnlyRootRequired
	}

	// Manifesto de segurança AOS-066: hash + versão do perfil seccomp aplicado (e a
	// versão da imagem base read-only). Entra no span e em cada evento do ciclo de
	// vida — por trajectória, para replay/auditoria. NÃO é segredo (ADR-006). O
	// perfil é o MESMO objecto propagado ao driver (spec.Seccomp) — o hash atesta o
	// perfil EFETIVAMENTE imposto, não uma declaração desligada.
	seccompHash := l.seccomp.Hash()
	seccompVersion := l.seccomp.Version()

	// AOS-066: monta a raiz read-only + overlay efémero DESTA execução a partir do
	// snapshot base imutável, se configurado ([WithSnapshot]). O [RootFS] é propagado
	// ao driver (imposição real: copy-up para o overlay, base nunca mutado) e o
	// manifesto atesta o base digest/overlay id REAIS. Sem snapshot, o jail in-memory
	// legado corre e a raiz read-only fica como declaração de configuração.
	imageVersion := l.imageVersion
	var rootfs *RootFS
	var rootfsBaseDigest, overlayID string
	if l.snapshot != nil {
		ov, _ := l.snapshot.Restore()
		fs, err := MountReadOnly(ov)
		if err != nil {
			return ExecResult{}, fmt.Errorf("sandbox rootfs mount: %w", err)
		}
		rootfs = fs
		rootfsBaseDigest = fs.BaseDigest()
		overlayID = fs.OverlayID()
		if imageVersion == "" {
			imageVersion = fs.ImageVersion()
		}
		// O overlay efémero é SEMPRE descartado (mesmo em erro/panic, e mesmo que o
		// Create falhe antes de registar o destroy): nada desta execução persiste.
		defer rootfs.Discard()
	}

	ctx, span := l.tracer.StartSpan(ctx, OpExecuteTool)
	span.SetAttribute(AttrOperationName, OpExecuteTool)
	span.SetAttribute(AttrRunID, req.RunID)
	span.SetAttribute(AttrStepID, req.StepID)
	span.SetAttribute(AttrToolName, req.Call.ToolID)
	span.SetAttribute(AttrDriver, string(l.driver.Kind()))
	span.SetAttribute(AttrTaint, string(TaintUntrusted))
	span.SetAttribute(AttrRootFSReadOnly, l.isolation.RootFSReadOnly)
	span.SetAttribute(AttrSeccompHash, seccompHash)
	span.SetAttribute(AttrSeccompVersion, seccompVersion)
	if imageVersion != "" {
		span.SetAttribute(AttrImageVersion, string(imageVersion))
	}
	// Prova, no manifesto/span, do rootfs EFETIVAMENTE montado (não só o booleano):
	// o base digest liga a execução à imagem base imutável exacta e o overlay id ao
	// overlay efémero desta trajectória. Só presentes quando o rootfs é montado.
	if rootfsBaseDigest != "" {
		span.SetAttribute(AttrRootFSBaseDigest, rootfsBaseDigest)
	}
	if overlayID != "" {
		span.SetAttribute(AttrOverlayID, overlayID)
	}
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
		Seccomp:   l.seccomp,
		RootFS:    rootfs,
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
			CredentialsHandle:     req.CredentialsHandle,
			ImageVersion:          string(imageVersion),
			SeccompProfileHash:    seccompHash,
			SeccompProfileVersion: seccompVersion,
			RootFSBaseDigest:      rootfsBaseDigest,
			OverlayID:             overlayID,
		})
	}()

	// (2) AUDIT-BEFORE-EFFECT — o create sela ANTES do efeito de exec. Fail-closed:
	// se a transição não puder ser auditada, o exec NÃO corre (mas o destroy sim).
	if _, err := l.sink.RecordLifecycle(ctx, LifecycleEvent{
		RunID: req.RunID, StepID: req.StepID, Phase: PhaseCreated,
		InstanceID: inst.ID, Driver: inst.Kind, Isolation: l.isolation,
		CredentialsHandle:     req.CredentialsHandle,
		ImageVersion:          string(imageVersion),
		SeccompProfileHash:    seccompHash,
		SeccompProfileVersion: seccompVersion,
		RootFSBaseDigest:      rootfsBaseDigest,
		OverlayID:             overlayID,
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
		ImageVersion:          string(imageVersion),
		SeccompProfileHash:    seccompHash,
		SeccompProfileVersion: seccompVersion,
		RootFSBaseDigest:      rootfsBaseDigest,
		OverlayID:             overlayID,
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
