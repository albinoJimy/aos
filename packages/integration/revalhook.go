package integration

import (
	"context"
	"errors"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/revalidation"
	"github.com/aos-ref/platform/registry/toolset"
)

// Erros de construção do wiring (fail-closed: um adaptador mal composto nunca é
// silenciosamente permissivo).
var (
	// ErrNilRevalidator — [NewRevalidationHook] sem revalidador.
	ErrNilRevalidator = errors.New("integration: revalidator nil")
	// ErrNilFrozenProvider — sem fonte de tool sets congelados.
	ErrNilFrozenProvider = errors.New("integration: frozen provider nil")
	// ErrNilCurrentResolver — sem resolvedor da definição actual.
	ErrNilCurrentResolver = errors.New("integration: current resolver nil")
	// ErrNilPolicyProvider — sem fonte de política de scopes/egress.
	ErrNilPolicyProvider = errors.New("integration: policy provider nil")
)

// FrozenProvider fornece o tool set CONGELADO de um run (AOS-050) — a expectativa
// imutável que a revalidação por chamada consulta. [RunToolSets] satisfá-lo.
type FrozenProvider interface {
	// Frozen devolve o snapshot congelado do run e um booleano de presença. Um run
	// sem snapshot devolve (nil, false) — default-deny na revalidação.
	Frozen(runID string) (*toolset.FrozenToolSet, bool)
}

// CurrentResolver resolve a definição ACTUAL de uma tool em backing store — o que
// EXECUTARIA agora se não fosse revalidada. É sobre esta definição que o digest é
// recalculado e a assinatura revalidada; um servidor MCP que mutou o schema a meio
// do run entrega aqui a definição mutada. [CatalogResolver] satisfá-lo sobre o REG.
type CurrentResolver interface {
	// Current devolve a definição actual da toolID e um booleano de presença. Um
	// erro é fail-closed a montante (a mediação nega). Uma tool ausente devolve
	// (_, false, nil) — default-deny.
	Current(ctx context.Context, toolID string) (domain.Entry, bool, error)
}

// PolicyProvider fornece a política de scopes/egress do run contra a qual o passo
// (4) da revalidação verifica o contract. [StaticPolicy] satisfá-lo para o caso
// comum (uma política igual para todos os runs).
type PolicyProvider interface {
	// Policy devolve a política do run. O zero-value ([revalidation.Policy]{}) é o
	// mais restritivo (fail-closed): nenhum scope permitido, egress máximo "none".
	Policy(runID string) revalidation.Policy
}

// CatalogResolver satisfaz [CurrentResolver] sobre um [toolset.Catalog] (o REG):
// resolve a definição actual pela enumeração atómica das entradas active
// (ActiveEntries) e devolve a que casa com o id. Em cada chamada consulta o
// catálogo VIVO — logo reflecte mutações posteriores ao congelamento, que é
// exactamente o que a revalidação precisa de detectar (drift entre o congelado e
// o actual). Num catálogo íntegro cada id tem no máximo uma versão active
// (o congelamento recusa ambiguidade), pelo que a correspondência é inequívoca.
type CatalogResolver struct {
	// Cat é o catálogo consultado. *registry.Registry satisfá-lo via ActiveEntries.
	Cat toolset.Catalog
}

// Current implementa [CurrentResolver].
func (c CatalogResolver) Current(ctx context.Context, toolID string) (domain.Entry, bool, error) {
	entries, err := c.Cat.ActiveEntries(ctx)
	if err != nil {
		return domain.Entry{}, false, err
	}
	for _, e := range entries {
		if e.ID == toolID {
			return e, true, nil
		}
	}
	return domain.Entry{}, false, nil
}

// StaticPolicy é um [PolicyProvider] que devolve a MESMA política para todos os
// runs. É o caso comum: a fronteira de scopes/egress é uma configuração do
// deployment, não varia por run.
type StaticPolicy revalidation.Policy

// Policy implementa [PolicyProvider].
func (p StaticPolicy) Policy(string) revalidation.Policy { return revalidation.Policy(p) }

// RevalidationHook adapta o [revalidation.Revalidator] (AOS-051) à cadeia de
// mediação do Reference Monitor ([referencemonitor.Hook]). É a peça que põe a
// revalidação criptográfica por chamada em TODO o caminho quente: o RM invoca
// [RevalidationHook.Evaluate] a cada tool call ANTES de mintar o seu permit e
// despachar; um veredicto de bloqueio devolve [referencemonitor.HookDeny] e a tool
// NUNCA é despachada (mediação total, ADR-002).
//
// A costura é limpa por construção: o pacote referencemonitor (folha) não conhece
// o revalidator; a revalidação é injectada aqui, no composition root, via
// [referencemonitor.WithHooks]. Os efeitos de divergência (quarentena + alerta +
// audit de bloqueio) são os do próprio revalidator — este hook só traduz o
// veredicto Allowed em Allow/Deny.
type RevalidationHook struct {
	rv       *revalidation.Revalidator
	frozen   FrozenProvider
	current  CurrentResolver
	policy   PolicyProvider
	egressOf func(referencemonitor.Call) string
	name     string
}

// HookOption configura o [RevalidationHook].
type HookOption func(*RevalidationHook)

// WithHookName sobrepõe o nome estável do hook (default "revalidation"). O nome
// aparece em DeniedBy e nos eventos de mediação.
func WithHookName(name string) HookOption {
	return func(h *RevalidationHook) {
		if name != "" {
			h.name = name
		}
	}
}

// WithEgressHost injecta a extracção do host de egress concreto a partir do
// [referencemonitor.Call] (tipicamente Call.Resource.Value quando Resource.Type é
// um host/URL). Se não dado, a revalidação verifica só a CLASSE de egress contra o
// tecto da política (o host só é verificado quando há allowlist E host).
func WithEgressHost(f func(referencemonitor.Call) string) HookOption {
	return func(h *RevalidationHook) {
		if f != nil {
			h.egressOf = f
		}
	}
}

// NewRevalidationHook constrói o hook. Fail-closed: qualquer colaborador nil é
// recusado — um hook sem revalidador/expectativa/definição-actual/política não
// poderia decidir e não deve existir.
func NewRevalidationHook(rv *revalidation.Revalidator, frozen FrozenProvider, current CurrentResolver, policy PolicyProvider, opts ...HookOption) (*RevalidationHook, error) {
	if rv == nil {
		return nil, ErrNilRevalidator
	}
	if frozen == nil {
		return nil, ErrNilFrozenProvider
	}
	if current == nil {
		return nil, ErrNilCurrentResolver
	}
	if policy == nil {
		return nil, ErrNilPolicyProvider
	}
	h := &RevalidationHook{
		rv:      rv,
		frozen:  frozen,
		current: current,
		policy:  policy,
		name:    "revalidation",
	}
	for _, o := range opts {
		o(h)
	}
	return h, nil
}

// Name implementa [referencemonitor.Hook].
func (h *RevalidationHook) Name() string { return h.name }

// Evaluate implementa [referencemonitor.Hook]: revalida a tool call e traduz o
// veredicto. Devolve HookDeny (nunca panic) em qualquer bloqueio; um erro é
// reservado a cancelamento de contexto (que o RM já trata fail-closed). É o ponto
// onde "a última linha anti rug-pull" passa a correr em todas as tool calls.
func (h *RevalidationHook) Evaluate(ctx context.Context, call *referencemonitor.Call) (referencemonitor.HookResult, error) {
	// (a) EXPECTATIVA: sem tool set congelado para o run não há contra o quê
	// revalidar — default-deny (um run não arrancado com freeze não executa tools).
	fz, ok := h.frozen.Frozen(call.RunID)
	if !ok {
		return denyResult("sem tool set congelado para o run"), nil
	}

	// (b) REALIDADE: a definição actual em backing store (o que executaria). Um erro
	// de resolução é fail-closed: cancelamento de contexto propaga-se (o RM nega e
	// não audita — contexto morto); outro erro nega com razão observável.
	cur, present, err := h.current.Current(ctx, call.ToolID)
	if err != nil {
		if ctx.Err() != nil {
			return referencemonitor.HookResult{}, err
		}
		return denyResult("falha a resolver a definição actual: " + err.Error()), nil
	}
	if !present {
		return denyResult("tool ausente do backing store"), nil
	}

	var egressHost string
	if h.egressOf != nil {
		egressHost = h.egressOf(*call)
	}

	// (c) REVALIDAÇÃO: LOOKUP→digest→assinatura→scope/egress→EXEC→AUDIT. A
	// quarentena/alerta/audit de divergência são efeitos do próprio revalidator.
	dec, err := h.rv.Revalidate(ctx, revalidation.Request{
		RunID:      call.RunID,
		StepID:     call.StepID,
		ToolID:     call.ToolID,
		Current:    cur,
		Frozen:     fz,
		Policy:     h.policy.Policy(call.RunID),
		EgressHost: egressHost,
	})
	if err != nil {
		// Só cancelamento de contexto chega aqui como erro (os bloqueios de política
		// vêm em dec.Allowed=false). Propaga-se: o RM nega fail-closed.
		return referencemonitor.HookResult{}, err
	}
	if !dec.Allowed {
		return referencemonitor.HookResult{
			Decision: referencemonitor.HookDeny,
			Reason:   "revalidação bloqueou: stage=" + string(dec.Stage) + " reason=" + string(dec.Reason),
		}, nil
	}
	// Allowed: o hook não se opõe. O RM prossegue a cadeia e, se tudo permitir,
	// minta o SEU permit e despacha. A revalidação já selou a sua decisão de
	// despacho no seu próprio partition de audit.
	return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
}

// denyResult constrói um HookResult de negação com razão (fail-closed).
func denyResult(reason string) referencemonitor.HookResult {
	return referencemonitor.HookResult{Decision: referencemonitor.HookDeny, Reason: reason}
}
