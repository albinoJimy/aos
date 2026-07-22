package integration

import (
	"context"
	"errors"

	pdp "github.com/aos-ref/control-plane/pdp"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
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

	// HookOptions são opções do [RevalidationHook] (ex.: [WithEgressHost]).
	HookOptions []HookOption
	// RuntimeOptions são opções do [agentruntime.Runtime].
	RuntimeOptions []agentruntime.Option
	// FreezeOptions são opções de [toolset.FreezeToolSet] (ex.: WithClock/WithTracer).
	FreezeOptions []toolset.Option
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
	// (logo a seguir à identidade — o gate de supply-chain corre cedo).
	toolsets := NewRunToolSets()
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

	rt := agentruntime.New(cfg.Model, rm, cfg.Recorder, cfg.RuntimeOptions...)

	return &SecuredRuntime{
		rt:        rt,
		rm:        rm,
		catalog:   cfg.Catalog,
		toolsets:  toolsets,
		freezeOpt: cfg.FreezeOptions,
	}, nil
}

// Register associa um ToolID a uma [referencemonitor.ToolFunc] no RM. O despacho
// real de uma tool só acontece sob permit do RM (no-bypass); registá-la aqui é a
// pré-condição de ela poder ser despachada de todo (default-deny para não
// registadas). Delega em [referencemonitor.Monitor.Register].
func (s *SecuredRuntime) Register(toolID string, fn referencemonitor.ToolFunc) error {
	return s.rm.Register(toolID, fn)
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
	s.toolsets.Put(frozen)
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
