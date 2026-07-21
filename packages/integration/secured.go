package integration

import (
	"context"
	"errors"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/registry/revalidation"
	"github.com/aos-ref/platform/registry/toolset"
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
	// EventSink é o sink de auditoria durável do RM (opcional; produção liga um
	// [referencemonitor.NewEventStoreSink]).
	EventSink referencemonitor.EventSink
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

// NewSecuredRuntime compõe o runtime seguro. Constrói o RM com a cadeia de mediação
// canónica MAIS o [RevalidationHook] inserido logo a seguir à identidade
// (identity → revalidation → policy → budget → egress → audit), constrói o Runtime
// e prepara o registo de tool sets congelados por run.
//
// Fail-closed: qualquer colaborador obrigatório em falta é recusado.
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
	}

	toolsets := NewRunToolSets()
	hook, err := NewRevalidationHook(cfg.Revalidator, toolsets, CatalogResolver{Cat: cfg.Catalog}, cfg.Policy, cfg.HookOptions...)
	if err != nil {
		return nil, err
	}

	// Cadeia de mediação: os stubs neutros de AOS-003 na ordem canónica, com a
	// revalidação inserida a seguir à identidade (o gate de supply-chain corre cedo,
	// antes de política/orçamento/egress). Produção substitui os stubs pelos hooks
	// reais; a posição da revalidação mantém-se.
	hooks := []referencemonitor.Hook{
		referencemonitor.IdentityStub{},
		hook,
		referencemonitor.PolicyStub{},
		referencemonitor.BudgetStub{},
		referencemonitor.EgressStub{},
		referencemonitor.AuditStub{},
	}
	rmOpts := []referencemonitor.Option{referencemonitor.WithHooks(hooks...)}
	if cfg.EventSink != nil {
		rmOpts = append(rmOpts, referencemonitor.WithEventSink(cfg.EventSink))
	}
	rm := referencemonitor.New(rmOpts...)

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
