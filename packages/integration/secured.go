package integration

import (
	"context"
	"errors"
	"fmt"

	pdp "github.com/aos-ref/control-plane/pdp"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/activity"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/authz"
	"github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/platform/registry/revalidation"
	"github.com/aos-ref/platform/registry/toolset"
	network "github.com/aos-ref/substrate/sandbox/network"
)

// Erros de construção do [SecuredRuntime].
var (
	// ErrNoModel — sem cliente de modelo.
	ErrNoModel = errors.New("integration: model client nil")
	// ErrNoRecorder — sem gravador de turnos.
	ErrNoRecorder = errors.New("integration: turn recorder nil")
	// ErrNoCatalog — sem catálogo (REG) para congelar o tool set.
	ErrNoCatalog = errors.New("integration: catalog nil")
	// ErrNoRevalidator — sem revalidador por chamada.
	ErrNoRevalidator = errors.New("integration: revalidator nil")
	// ErrNoPolicy — sem política de scopes/egress.
	ErrNoPolicy = errors.New("integration: policy provider nil")
	// ErrNoWORM — sem [audit.Store] WORM. É o registo durável tamper-evident ÚNICO
	// partilhado pelo EventSink do RM (via [audit.NewMediationSink]) e pelo sink de
	// segurança de egress (via [network.NewWORMSecuritySink]); sem ele a auditoria
	// não seria durável e o fail-closed de audit do RM nunca dispararia.
	ErrNoWORM = errors.New("integration: WORM audit store nil")
)

// SecuredConfig configura o [SecuredRuntime].
type SecuredConfig struct {
	// Model é o cliente do Model Gateway (obrigatório).
	Model agentruntime.ModelClient
	// Recorder grava os turnos no Event Store (obrigatório).
	Recorder *agentruntime.TurnRecorder
	// Catalog é o REG a congelar no arranque de cada run (obrigatório).
	// *registry.Registry satisfá-lo via ActiveEntries.
	Catalog toolset.Catalog
	// Revalidator é o revalidador por chamada (AOS-051), tipicamente construído com
	// [revalidation.WithQuarantiner] ligado a [ProvenanceQuarantiner] e
	// [revalidation.WithAlerter] a [RecordingAlerter] (obrigatório).
	Revalidator *revalidation.Revalidator
	// Policy é a política de scopes/egress do run (obrigatória; use [StaticPolicy]
	// para o caso comum).
	Policy PolicyProvider
	// WORM é o [audit.Store] tamper-evident ÚNICO (obrigatório). É partilhado pelo
	// EventSink de mediação do RM e pelo sink de segurança de egress — ambos selam no
	// MESMO WORM (ver [NewSecuredRuntime]). [audit.NewMemStore] satisfá-lo.
	WORM audit.Store
	// ToolSetStore torna o registo de tool sets congelados DURÁVEL (AOS-155): cada
	// arranque de run persiste o snapshot no Event Store e a retoma reconstrói-o
	// ([RunToolSets.Rebuild]), evitando que um failover colapse a revalidação para
	// default-deny. OPCIONAL: nil ⇒ registo in-memory (sem crash-safety).
	// *[eventstore.Store] satisfá-lo.
	ToolSetStore ToolSetStore

	// --- Execução durável (AOS-180) ---------------------------------------------
	// Quando TODOS os colaboradores abaixo são fornecidos, o runtime corre com
	// idempotência por passo, checkpoint intra-iteração e captura de não-determinismo
	// sobre o Event Store. Quando nil, o runtime usa os defaults no-op (AOS-013).
	// O dispatcher durável é construído internamente a partir do ledger + RM, para
	// garantir que a mediação é a única via de execução.
	Checkpointer agentruntime.Checkpointer
	Capturer     agentruntime.Capturer
	Ledger       *durable.StepLedger

	// --- Colaboradores de segurança da cadeia REAL (AOS-154) --------------------
	// Todos são OPCIONAIS: quando nil caem para um default demo-grade que é um hook
	// REAL fail-closed (NUNCA um stub neutro). Os colaboradores "reais"
	// não-forjáveis — token NHI assinado, bundle de política assinado, allowlist de
	// egress por deployment — são AOS-156 (gated por D4), fora deste ticket.

	// Verifier verifica o token NHI (AOS-005) no hook de identidade. nil ⇒
	// [identity.NewVerifier] sem trust anchors (nega toda a NHI — fail-closed).
	Verifier *identity.Verifier
	// PDP é o Policy Decision Point (AOS-004) no hook de política. nil ⇒
	// [pdp.NewUnloaded] (sem bundle carregado ⇒ deny fail-closed).
	PDP *pdp.PDP
	// Privileged classifica capabilities privilegiadas para o [TaintGate] (AOS-069) e
	// é a barreira control/data-plane exigida por [referencemonitor.NewProductionSecure].
	// nil ⇒ [referencemonitor.NewStaticPrivilegedSet] vazio (classificador real).
	Privileged referencemonitor.PrivilegedAuthorizer
	// Authority é a fonte de autoridade user∩classe para o [ScopeGate] (AOS-071).
	// nil ⇒ [authz.NewStaticAuthoritySource] vazio (fonte real ⇒ scope fail-closed).
	Authority authz.AuthoritySource
	// EgressResolver resolve a allowlist de egress por principal (AOS-067). nil ⇒
	// [network.NewEmbeddedResolver] (allowlist de referência embutida).
	EgressResolver network.EgressPolicyResolver

	// SteerSource liga o CANAL DE CONTROLO out-of-band (AOS-023) ao loop base via
	// [agentruntime.WithSteerSource] (AOS-218): a partir daqui o runtime de PRODUÇÃO
	// consulta a pausa graciosa + a correcção TRUSTED na fronteira de fim-de-turno. É o
	// concreto [control.LoopSteer] (SteerChannel + resolvedor de [control.StateGate]
	// por-run) composto pelo nó. OPCIONAL e ADITIVO: nil ⇒ o loop nunca o consulta e o
	// comportamento de AOS-013 é byte-idêntico (o steer é opt-in, como o capturer/ledger).
	// Fechar o ACHADO-2 (steer inerte): sem esta ligação, [control.NewLoopSteer]/
	// [WithSteerSource] não tinham chamador de produção e a correcção nunca chegava ao loop.
	SteerSource agentruntime.SteerSource
	// HookOptions são opções do [RevalidationHook] (ex.: [WithEgressHost]).
	HookOptions []HookOption
	// RuntimeOptions são opções do [agentruntime.Runtime].
	RuntimeOptions []agentruntime.Option
	// FreezeOptions são opções de [toolset.FreezeToolSet] (ex.: WithClock/WithTracer).
	FreezeOptions []toolset.Option

	// EffectRewriter reescreve a [referencemonitor.Call] IMEDIATAMENTE antes do
	// despacho (AOS-005). É o decorator OUTERMOST do dispatcher: recebe a Call já
	// construída pelo loop (com RunID/StepID/ToolID/Input) e devolve-a reescrita.
	//
	// PORQUE É SEGURO (não relaxa a mediação): a decisão do RM avalia Principal/
	// Capability/Resource/Taint/Context — NUNCA o Input. Este hook só reshapa o
	// PAYLOAD do efeito (Call.Input), pelo que a decisão de política é idêntica com
	// ou sem ele. O uso canónico é traduzir os args OPACOS do modelo num envelope de
	// execução da sandbox (ExecRequest) que exige o RunID/StepID reais da Call — que
	// só existem aqui, no dispatcher, não no enriquecimento do ModelClient.
	//
	// FAIL-CLOSED: um erro de reescrita NÃO aborta o run — materializa-se como uma
	// [referencemonitor.Decision] Deny (nenhum efeito, visível no tail), como uma tool
	// call malformada. nil ⇒ sem reescrita (comportamento byte-idêntico).
	EffectRewriter func(referencemonitor.Call) (referencemonitor.Call, error)

	// Tracer é a VIA EXPLÍCITA de observabilidade dos colaboradores que este
	// composition root constrói INTERNAMENTE — hoje, o dispatcher durável de AOS-021
	// (AOS-210). OPCIONAL: nil ⇒ [agentruntime.NoopTracer] (comportamento byte-idêntico
	// ao de antes da instrumentação; zero spans novos).
	//
	// PORQUE UM CAMPO PRÓPRIO e não a extracção de [RuntimeOptions]/[FreezeOptions]:
	// essas listas são OPACAS por construção — `[]agentruntime.Option` e
	// `[]toolset.Option` são fatias de FUNÇÕES que mutam o alvo; não há forma de as
	// inspeccionar para recuperar o tracer que lá foi posto sem as aplicar a um alvo
	// falso (uma via reflexiva, frágil e que dependeria da ordem das opções). Um campo
	// explícito torna a dependência VISÍVEL na assinatura da config e deixa o chamador
	// declarar o MESMO tracer nas três vias (RT/RM, freeze e dispatcher durável), que é
	// exactamente o que o nó faz.
	//
	// INVARIANTE DO CHAMADOR (o preço do campo explícito): quando este campo é preenchido,
	// TEM de ser o MESMO valor de tracer entregue em [RuntimeOptions] via
	// [agentruntime.WithTracer]. Entregar aqui um tracer DIFERENTE do do Runtime produz uma
	// ÁRVORE PARTIDA — o aos.activity num trace e o execute_tool noutro —, exactamente o
	// contrário do que este campo existe para garantir, e sem erro em tempo de construção:
	// as duas vias são independentes por desenho, e nada aqui as pode comparar (os
	// [agentruntime.Tracer] não são comparáveis de forma fiável nem inspeccionáveis a
	// partir das opções opacas). Só um teste de TOPOLOGIA o apanha — ver
	// packages/cmd/aos/observability_durable_test.go, que sela o único chamador de
	// produção. Enforcement estrutural (o composition root derivar as três vias de UM
	// campo) seria uma mudança de assinatura maior, deliberadamente fora de AOS-210.
	//
	// NÃO substitui [agentruntime.WithTracer]: o span execute_tool continua a ser aberto
	// SÓ pelo Reference Monitor (AOS-076), que recebe o tracer do Runtime. Este campo
	// acrescenta apenas a camada INTERMÉDIA aos.activity — ver [activity.OpActivity].
	Tracer agentruntime.Tracer
}

// SecuredRuntime é o Agent Runtime COM a revalidação por chamada e o congelamento
// do tool set por run já ligados (AOS-050 + AOS-051). Corre o loop base real, mas
// arranca cada run congelando o tool set do REG e faz o RM revalidar CADA tool call
// antes de a despachar. Construir com [NewSecuredRuntime].
type SecuredRuntime struct {
	rt        *agentruntime.Runtime
	rm        *referencemonitor.Monitor
	catalog   toolset.Catalog
	toolsets  *RunToolSets
	freezeOpt []toolset.Option
	ledger    *durable.StepLedger
}

// NewSecuredRuntime compõe o runtime seguro sobre a CADEIA DE MEDIAÇÃO REAL
// (AOS-154), construída via [referencemonitor.NewProductionSecure] — a via
// sancionada estrita, que recusa fail-closed uma cadeia com o [IdentityStub] ou o
// [EgressStub] neutros ou sem um [ScopeGate] com autoridade. A ordem dos hooks é:
//
//	identity (IdentityCheck, AOS-005) → revalidation (AOS-051) → policy (PDP, AOS-004)
//	→ taint (TaintGate, AOS-069) → scope (ScopeGate, AOS-071) → budget → egress
//	(EgressHook, AOS-067)
//
// A identidade corre PRIMEIRO e resolve [Call.Principal] a partir do token NHI antes
// de taint/scope/egress/revalidation (que dependem do principal resolvido). O
// "audit" NÃO é um hook: é o EventSink durável (via [referencemonitor.WithEventSink]),
// um adaptador ([audit.NewMediationSink]) que sela cada MediationRecord no WORM.
//
// UM ÚNICO WORM: o MESMO [audit.Store] (cfg.WORM) alimenta o EventSink do RM E o
// sink de segurança de egress ([network.NewWORMSecuritySink]) — logo mediações e
// bloqueios de egress selam-se na MESMA hash-chain tamper-evident.
//
// Fail-closed: qualquer colaborador obrigatório em falta é recusado. Os colaboradores
// de segurança da cadeia (Verifier/PDP/Privileged/Authority/EgressResolver) caem para
// defaults demo-grade que são hooks REAIS fail-closed quando não fornecidos.
func NewSecuredRuntime(cfg SecuredConfig) (*SecuredRuntime, error) {
	switch {
	case cfg.Model == nil:
		return nil, ErrNoModel
	case cfg.Recorder == nil:
		return nil, ErrNoRecorder
	case cfg.Catalog == nil:
		return nil, ErrNoCatalog
	case cfg.Revalidator == nil:
		return nil, ErrNoRevalidator
	case cfg.Policy == nil:
		return nil, ErrNoPolicy
	case cfg.WORM == nil:
		return nil, ErrNoWORM
	}

	// Defaults demo-grade (fail-closed): cada um é um hook REAL, nunca um stub. Os
	// colaboradores não-forjáveis (token NHI assinado, bundle de política assinado,
	// allowlist de egress por deployment) são AOS-156 — gated por D4, fora deste ticket.
	verifier := cfg.Verifier
	if verifier == nil {
		verifier = identity.NewVerifier() // sem trust anchors ⇒ nega toda a NHI
	}
	policyDP := cfg.PDP
	if policyDP == nil {
		policyDP = pdp.NewUnloaded() // sem bundle ⇒ deny fail-closed
	}
	privileged := cfg.Privileged
	if privileged == nil {
		privileged = referencemonitor.NewStaticPrivilegedSet() // classificador real (vazio)
	}
	authority := cfg.Authority
	if authority == nil {
		authority = authz.NewStaticAuthoritySource() // fonte real (vazia) ⇒ scope fail-closed
	}
	resolver := cfg.EgressResolver
	if resolver == nil {
		r, err := network.NewEmbeddedResolver()
		if err != nil {
			return nil, err
		}
		resolver = r
	}

	// Revalidação por chamada (AOS-051): o MESMO hook já existente, na MESMA posição
	// (logo a seguir à identidade — o gate de supply-chain corre cedo). O registo de
	// tool sets é DURÁVEL se cfg.ToolSetStore for fornecido (AOS-155, crash-safe).
	var tsOpts []RunToolSetsOption
	if cfg.ToolSetStore != nil {
		tsOpts = append(tsOpts, WithToolSetStore(cfg.ToolSetStore))
	}
	toolsets := NewRunToolSets(tsOpts...)
	revalHook, err := NewRevalidationHook(cfg.Revalidator, toolsets, CatalogResolver{Cat: cfg.Catalog}, cfg.Policy, cfg.HookOptions...)
	if err != nil {
		return nil, err
	}

	// Egress real (AOS-067) sobre o MESMO WORM: o sink de segurança sela os bloqueios
	// de egress no cfg.WORM — a MESMA hash-chain onde o RM sela as mediações.
	egressFilter, err := network.NewEgressFilter(resolver,
		network.WithSecurityAuditSink(network.NewWORMSecuritySink(cfg.WORM)),
	)
	if err != nil {
		return nil, err
	}
	egressHook, err := network.NewEgressHook(egressFilter)
	if err != nil {
		return nil, err
	}

	// CADEIA REAL na ordem canónica de AOS-154. BudgetStub é aceitável (AOS-008 é o
	// admission control real, fora deste ticket); todos os outros são hooks reais.
	hooks := []referencemonitor.Hook{
		identity.NewIdentityCheck(verifier),       // identity — resolve Call.Principal (1º)
		revalHook,                                 // revalidation (AOS-051)
		pdp.NewPolicyCheck(policyDP),              // policy (PDP, AOS-004)
		referencemonitor.NewTaintGate(privileged), // taint (AOS-069)
		referencemonitor.NewScopeGate(authority),  // scope (AOS-071)
		referencemonitor.BudgetStub{},             // budget (stub aceitável)
		egressHook,                                // egress (AOS-067)
	}

	// EventSink durável = adaptador sancionado MediationRecord→AuditRecord sobre o
	// MESMO WORM (partição por RunID). É o "audit" da cadeia — não um hook.
	eventSink := audit.NewMediationSink(cfg.WORM)

	// RM via a via ESTRITA: recusa fail-closed IdentityStub/EgressStub e exige
	// ScopeGate+TaintGate activos e audit durável. Nunca [referencemonitor.New] cru.
	rm, err := referencemonitor.NewProductionSecure(privileged,
		referencemonitor.WithHooks(hooks...),
		referencemonitor.WithEventSink(eventSink),
	)
	if err != nil {
		return nil, err
	}

	// Ligação AOS-180: execução durável. Os colaboradores são opcionais em conjunto;
	// quando fornecidos, substituem os defaults no-op do loop. O dispatcher durável
	// é construído sobre o ledger e o RM, garantindo que a mediação permanece a única
	// via de execução de efeitos.
	runtimeOpts := cfg.RuntimeOptions
	if cfg.Checkpointer != nil {
		runtimeOpts = append(runtimeOpts, agentruntime.WithCheckpointer(cfg.Checkpointer))
	}
	if cfg.Capturer != nil {
		runtimeOpts = append(runtimeOpts, agentruntime.WithCapturer(cfg.Capturer))
	}
	// AOS-218: liga o canal de steer ao loop de PRODUÇÃO. É a composição que faltava
	// (ACHADO-2) — [WithSteerSource] passa a ter chamador de produção, logo a pausa
	// graciosa e a injecção da correcção TRUSTED tornam-se efectivas na fronteira de
	// fim-de-turno. nil ⇒ opção omitida ⇒ retro-compatibilidade byte-idêntica.
	if cfg.SteerSource != nil {
		runtimeOpts = append(runtimeOpts, agentruntime.WithSteerSource(cfg.SteerSource))
	}
	var durDisp agentruntime.ActivityDispatcher // nil quando a execução durável está desligada
	if cfg.Ledger != nil {
		// OBSERVABILIDADE do escopo durável (AOS-210). Sem esta opção o dispatcher fica
		// com o default [agentruntime.NoopTracer] e o span aos.activity — a camada que
		// carrega dedup/replay e o CUSTO DO EFEITO REAL — nunca é exportado, mesmo com a
		// observabilidade do nó ligada. Passar o tracer NÃO duplica o execute_tool: o
		// aos.activity nasce PAI dele (o ctx derivado propaga-se ao Mediate) e o RM
		// continua a ser a ÚNICA autoridade que o abre (ver [activity.OpActivity]).
		// cfg.Tracer nil ⇒ nenhuma opção ⇒ NoopTracer ⇒ retro-compatibilidade estrita.
		var actOpts []activity.Option
		if cfg.Tracer != nil {
			actOpts = append(actOpts, activity.WithTracer(cfg.Tracer))
		}
		actDisp, err := activity.NewDispatcher(rm, cfg.Ledger, actOpts...)
		if err != nil {
			return nil, fmt.Errorf("integration: dispatcher durável: %w", err)
		}
		dd, err := NewDurableDispatcher(actDisp)
		if err != nil {
			return nil, fmt.Errorf("integration: adaptador do dispatcher durável: %w", err)
		}
		durDisp = dd
	}

	// Selecção do dispatcher FINAL. Quando há EffectRewriter (AOS-005) ele é o decorator
	// OUTERMOST: reescreve a Call.Input ANTES do despacho e delega no dispatcher durável
	// (se composto) ou em Mediate directo — SEM abrir spans nem tocar na decisão, pelo que
	// a topologia de observabilidade (AOS-210) e o no-bypass ficam intactos. Sem rewriter,
	// o comportamento é byte-idêntico ao anterior (durDisp directo, ou default se nil).
	switch {
	case cfg.EffectRewriter != nil:
		runtimeOpts = append(runtimeOpts, agentruntime.WithActivityDispatcher(
			rewritingDispatcher{inner: durDisp, rm: rm, rewrite: cfg.EffectRewriter}))
	case durDisp != nil:
		runtimeOpts = append(runtimeOpts, agentruntime.WithActivityDispatcher(durDisp))
	}

	rt := agentruntime.New(cfg.Model, rm, cfg.Recorder, runtimeOpts...)

	return &SecuredRuntime{
		rt:        rt,
		rm:        rm,
		catalog:   cfg.Catalog,
		toolsets:  toolsets,
		freezeOpt: cfg.FreezeOptions,
		ledger:    cfg.Ledger,
	}, nil
}

// CodeEffectRewrite é o código de Deny quando o [SecuredConfig.EffectRewriter] falha a
// reescrita da Call antes do despacho (ex.: args do modelo malformados). Fail-closed:
// nenhum efeito ocorre; a tool call materializa-se como Deny no tail do turno.
const CodeEffectRewrite = "E_EFFECT_REWRITE"

// rewritingDispatcher é o decorator do [agentruntime.ActivityDispatcher] que aplica o
// [SecuredConfig.EffectRewriter] à Call ANTES do despacho. Delega no dispatcher durável
// (inner) quando composto, ou em [referencemonitor.Monitor.Mediate] directo (via rm)
// quando a execução durável está desligada — preservando o no-bypass (o único despacho
// continua a ser Mediate) e a topologia de spans (não abre spans; o aos.activity e o
// execute_tool nascem a jusante).
type rewritingDispatcher struct {
	inner   agentruntime.ActivityDispatcher // nil ⇒ Mediate directo
	rm      *referencemonitor.Monitor
	rewrite func(referencemonitor.Call) (referencemonitor.Call, error)
}

func (d rewritingDispatcher) Dispatch(ctx context.Context, call referencemonitor.Call) (referencemonitor.Decision, error) {
	rc, err := d.rewrite(call)
	if err != nil {
		// Fail-closed: reescrita falhou ⇒ Deny (nenhum efeito). NÃO é fatal para o loop —
		// materializa-se no tail como uma tool call negada (dec.Output nil).
		return referencemonitor.Decision{
			Effect:   referencemonitor.EffectDeny,
			Code:     CodeEffectRewrite,
			Reason:   err.Error(),
			DeniedBy: "effect_rewriter",
		}, nil
	}
	if d.inner != nil {
		return d.inner.Dispatch(ctx, rc)
	}
	return d.rm.Mediate(ctx, rc)
}

var _ agentruntime.ActivityDispatcher = rewritingDispatcher{}

// Register associa um ToolID a uma [referencemonitor.ToolFunc] no RM. O despacho
// real de uma tool só acontece sob permit do RM (no-bypass); registá-la aqui é a
// pré-condição de ela poder ser despachada de todo (default-deny para não
// registadas). Delega em [referencemonitor.Monitor.Register].
func (s *SecuredRuntime) Register(toolID string, fn referencemonitor.ToolFunc) error {
	return s.rm.Register(toolID, fn)
}

// RegisterCosting associa um ToolID a uma [referencemonitor.CostingToolFunc] no RM — uma
// tool que REPORTA o custo medido do seu efeito real (AOS-212). Idêntica a [Register] nas
// garantias de no-bypass/default-deny; o custo reportado alimenta o span aos.activity na
// via durável. O produtor real (Model Gateway / tools pagas) é EPIC-06; as tools de
// referência de produção do nó usam [Register] e reportam 0 (honesto — sem custo).
func (s *SecuredRuntime) RegisterCosting(toolID string, fn referencemonitor.CostingToolFunc) error {
	return s.rm.RegisterCosting(toolID, fn)
}

// Run arranca um run: CONGELA o tool set do REG (AOS-050), materializa-o no goal
// (prefixo imutável + manifesto via [ApplyFrozenToGoal]), regista o snapshot para a
// revalidação o consultar e corre o loop base. Cada tool call do run atravessa a
// revalidação por chamada no RM (AOS-051). Liberta o snapshot no fim.
//
// sel restringe (opcionalmente) o congelamento a um subconjunto de ids (nil = todo
// o conjunto active). Devolve o resultado do run e o snapshot congelado (útil para
// asserção/observabilidade). Um erro de congelamento aborta antes de correr o loop
// (fail-closed: sem tool set congelado, nenhuma tool executaria de qualquer forma).
func (s *SecuredRuntime) Run(ctx context.Context, goal agentruntime.Goal, sel *toolset.Selector) (agentruntime.Result, *toolset.FrozenToolSet, error) {
	frozen, err := toolset.FreezeToolSet(ctx, s.catalog, goal.RunID, sel, s.freezeOpt...)
	if err != nil {
		return agentruntime.Result{}, nil, err
	}
	// Registo DURÁVEL do arranque (AOS-155): persiste o snapshot (se durável) antes de
	// correr, para a revalidação o reconstruir após um failover em vez de negar tudo.
	// Fail-closed: uma falha de persistência aborta o run (não seria crash-safe).
	if err := s.toolsets.Freeze(ctx, frozen); err != nil {
		return agentruntime.Result{}, nil, err
	}
	defer s.toolsets.Release(frozen.RunID())

	res, err := s.rt.Run(ctx, ApplyFrozenToGoal(goal, frozen))
	return res, frozen, err
}

// Metrics devolve os contadores de mediação do RM (permits/denials/escalations) —
// úteis para observar que uma tool call não-revalidada foi negada (denials++,
// permits inalterado).
func (s *SecuredRuntime) Metrics() *referencemonitor.Metrics { return s.rm.Metrics() }

// Monitor expõe o Reference Monitor subjacente (para composições avançadas). O
// único caminho de execução de tools continua a ser [referencemonitor.Monitor.Mediate].
func (s *SecuredRuntime) Monitor() *referencemonitor.Monitor { return s.rm }

// ToolSets expõe o registo de tool sets congelados por run (para inspecção).
func (s *SecuredRuntime) ToolSets() *RunToolSets { return s.toolsets }

// RebuildLedger repõe o step-ledger em memória a partir dos eventos duráveis do run.
// Deve ser chamado antes de retomar um run após crash/failover (AOS-180). Sem ledger
// durável é um no-op.
func (s *SecuredRuntime) RebuildLedger(ctx context.Context, runID string) error {
	if s.ledger == nil {
		return nil
	}
	return s.ledger.Rebuild(ctx, runID)
}
