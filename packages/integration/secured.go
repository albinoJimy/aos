package integration

import (
	"context"
	"errors"
	"fmt"

	pdp "github.com/aos-ref/control-plane/pdp"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/activity"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/saga"
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

	// CompensationRegistry liga o registo de compensações de AOS-020 ao dispatcher durável
	// (AOS-021) via [activity.WithCompensationRegistry]. É a composição que a saga de rollback
	// de AOS-254 exige: uma activity que traga uma [activity.Compensation] passa a ter ONDE
	// registar a acção inversa (no momento do permit/dedup), e o [saga.SagaCoordinator] do
	// caminho de FALHA DURÁVEL (failed→compensating) tem de ONDE as ler. É o MESMO ponteiro que
	// o nó entrega ao coordinator — um único registo por instância de runtime.
	//
	// OPCIONAL: nil ⇒ [activity.WithCompensationRegistry] é omitida e uma activity com
	// Compensation é recusada ([activity.ErrNoRegistry]) — o estado anterior a AOS-254. SÓ TEM
	// EFEITO com execução durável (Ledger != nil): sem ledger não há dispatcher durável a que
	// ligar o registrar, e sem ele a idempotência das compensações (0 reversões duplicadas) não
	// teria substrato. Ver [NewSecuredRuntime].
	CompensationRegistry *saga.CompensationRegistry

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
	// LivenessBreaker liga o CIRCUIT BREAKER multi-sinal do agente vivo (AOS-080/081) ao
	// loop base via [agentruntime.WithLivenessBreaker]: na fronteira de fim-de-turno o run
	// pára com um VEREDICTO (sem progresso / wall-clock / velocidade de queima), já
	// materializado como transição durável, em vez de esgotar MaxTurns cegamente. É o
	// concreto [LivenessBreakerAdapter] sobre o [breaker.Breaker] por-run composto pelo nó.
	// OPCIONAL e ADITIVO: nil ⇒ o loop nunca o consulta e o comportamento de AOS-013 é
	// byte-idêntico (mesmo padrão de SteerSource/Capturer/Ledger).
	//
	// Fecha a lacuna de o breaker existir COMPLETO (limiares por classe, sinais,
	// transição durável, alerta) sem NUNCA ter tido chamador de produção.
	LivenessBreaker agentruntime.LivenessBreaker
	// EscalationSink liga o bridge de aprovação (AOS-021) ao loop: um veredicto `escalate`
	// do Reference Monitor suspende o run (running → waiting_on_human) e regista o pendente
	// para o operador decidir. OPCIONAL e ADITIVO: nil ⇒ o escalate é tratado como negação
	// e o run prossegue (comportamento anterior byte-idêntico).
	EscalationSink agentruntime.EscalationSink
	// ApprovalEvidence é o outro lado do bridge: na RETOMA, resolve por PREVIEW se existe
	// aprovação humana por usar para a call que vai ser mediada, e devolve a evidência a
	// anexar. É infraestrutura TRUSTED (a store de grants), nunca o modelo. OPCIONAL: nil ⇒
	// nenhuma call leva evidência.
	ApprovalEvidence agentruntime.ApprovalEvidenceSource
	// ApprovalVerifier fecha o bridge DENTRO da cadeia de mediação: é o hook que verifica a
	// evidência anexada e, só em sucesso, carimba a call com a prova não-forjável. Sem ele
	// os dois lados acima ficam inertes — a evidência viaja mas nada a lê, e o escalate
	// repete-se para sempre (aprovar nunca satisfaz quem exigiu a aprovação).
	//
	// O gate entra a seguir à IDENTIDADE (a preview inclui o principal RESOLVIDO do token,
	// pelo que antes disso não seria calculável) e ANTES da política: o oráculo de autonomia
	// tem de poder ver que o gate humano JÁ ocorreu, senão volta a rebaixar para escalate.
	// NUNCA nega e nunca concede: no máximo mantém tudo fechado. OPCIONAL: nil ⇒ gate ausente.
	ApprovalVerifier referencemonitor.ApprovalVerifier
	// Budget é o ORÇAMENTO POR-RUN (AOS-008, ligado em AOS-256/AOS-257). Quando fornecido:
	//
	//   - o ponto de injecção "budget" da cadeia deixa de ser o [referencemonitor.BudgetStub]
	//     e passa a ser o adaptador REAL ([budget.BudgetCheck]), que RESERVA headroom por
	//     tool call e NEGA fail-closed quando não há;
	//   - cada run regista o seu nó de orçamento em [SecuredRuntime.Run] e liberta-o no fim
	//     (incl. erro e panic) — sem esse nó o adaptador negaria 100% das tool calls;
	//   - o dispatcher de efeitos é decorado para SALDAR a reserva (commit em permit;
	//     release em deny/escalate/erro).
	//
	// ALCANCE, na declaração fixada em AOS-255: TOOL-ONLY (o turno de modelo é invocado fora
	// da cadeia e nenhuma reserva o admite) e TOKEN-ONLY (o canal de custo micro-USD não está
	// ligado ponta a ponta — eixo AOS-259). Construir com [NewRunBudget].
	//
	// OPCIONAL: nil ⇒ [referencemonitor.BudgetStub] como antes (nenhuma decisão consulta
	// custo) — e é ESSE o estado que o banner do nó tem de declarar.
	Budget *RunBudget

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
	budget    *RunBudget // AOS-256: nil ⇒ nenhum nó por-run (e nenhum hook de orçamento)
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

	// CADEIA REAL na ordem canónica de AOS-154. O ponto de injecção "budget" é o
	// [budget.BudgetCheck] REAL quando cfg.Budget é fornecido (AOS-257) e o
	// [referencemonitor.BudgetStub] neutro quando não — nunca um terceiro estado; todos os
	// outros são hooks reais.
	budgetHook := referencemonitor.Hook(referencemonitor.BudgetStub{})
	if cfg.Budget != nil {
		budgetHook = cfg.Budget.check
	}
	var hooks []referencemonitor.Hook
	// APROVAÇÃO HUMANA (AOS-021) — ANTES de tudo o resto. Duas razões, ambas obrigatórias:
	//
	//  1. A preview é o digest da acção, e o hook de identidade SUBSTITUI Call.Principal a
	//     partir do token. Um gate colocado depois dele recalcularia a preview sobre um
	//     principal DIFERENTE do que o loop usou para registar o pendente e para procurar a
	//     evidência — e a amarra nunca casaria. (Observado ao vivo: a aprovação era emitida,
	//     a evidência viajava, e a acção voltava a escalar indefinidamente.)
	//  2. O oráculo de autonomia — dentro da política — é QUEM exige o gate humano; tem de
	//     poder ver que o gate já ocorreu, senão volta a rebaixar o permit para escalate.
	//
	// O gate nunca nega e nunca concede: no máximo carimba uma prova verificada. Toda a
	// autorização real continua a jusante (identidade, revalidação, política, taint, escopo,
	// orçamento, egress). OPCIONAL: nil ⇒ gate ausente, escalate degrada para negação.
	if cfg.ApprovalVerifier != nil {
		hooks = append(hooks, referencemonitor.NewApprovalGate(cfg.ApprovalVerifier))
	}
	hooks = append(hooks,
		identity.NewIdentityCheck(verifier),       // identity — resolve Call.Principal
		revalHook,                                 // revalidation (AOS-051)
		pdp.NewPolicyCheck(policyDP),              // policy (PDP, AOS-004)
		referencemonitor.NewTaintGate(privileged), // taint (AOS-069)
		referencemonitor.NewScopeGate(authority),  // scope (AOS-071)
		budgetHook,                                // budget (AOS-008): real com cfg.Budget, stub sem ele
		egressHook,                                // egress (AOS-067)
	)

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
	// AOS-080/081: liga o disjuntor do agente vivo ao loop de PRODUÇÃO — a composição que
	// faltava para o [breaker.Breaker] deixar de ser código órfão (Observe sem chamador).
	// nil ⇒ opção omitida ⇒ retro-compatibilidade byte-idêntica.
	if cfg.LivenessBreaker != nil {
		runtimeOpts = append(runtimeOpts, agentruntime.WithLivenessBreaker(cfg.LivenessBreaker))
	}
	// AOS-021: bridge negação→aprovação→reexecução. As duas portas são independentes (a
	// escalada suspende; a evidência destrava na retoma) e ambas aditivas — nil ⇒ omitidas
	// ⇒ comportamento anterior byte-idêntico.
	if cfg.EscalationSink != nil {
		runtimeOpts = append(runtimeOpts, agentruntime.WithEscalationSink(cfg.EscalationSink))
	}
	if cfg.ApprovalEvidence != nil {
		runtimeOpts = append(runtimeOpts, agentruntime.WithApprovalEvidence(cfg.ApprovalEvidence))
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
		// AOS-254: liga o REGISTO DE COMPENSAÇÕES (AOS-020) ao dispatcher durável. É a
		// composição de produção de [activity.WithCompensationRegistry] — a costura que
		// FALTAVA: sem ela uma [activity.Compensation] seria recusada ([activity.ErrNoRegistry])
		// e a saga de rollback (failed→compensating) nunca teria de onde LER as acções inversas
		// (registry só com chamadores de teste). O MESMO *saga.CompensationRegistry que aqui
		// RECEBE as compensações é o que o [saga.SagaCoordinator] do caminho de falha PERCORRE.
		// nil ⇒ opção omitida (estado anterior a AOS-254, byte-idêntico). Vive DENTRO do ramo
		// do ledger durável de propósito: sem ledger não há dispatcher a que ligar o registrar,
		// e a idempotência das compensações assenta nesse mesmo ledger (AOS-014).
		if cfg.CompensationRegistry != nil {
			actOpts = append(actOpts, activity.WithCompensationRegistry(cfg.CompensationRegistry))
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

	// AOS-257: SALDO DA RESERVA no decorator do dispatcher — commit em permit, release em
	// deny/escalate/erro/panic. É OUTERMOST (envolve o durável quando existe) para ver o
	// desfecho FINAL; num dedup/replay do ledger não houve mediação, logo não há reserva
	// pendente e o saldo é no-op. Sem execução durável não há dispatcher nenhum para
	// decorar — e entregar um [agentruntime.WithActivityDispatcher] SUBSTITUI o default do
	// runtime, pelo que o decorator assenta em [mediateDispatcher] (o mesmo Mediate directo
	// que o default do kernel faz).
	if cfg.Budget != nil {
		inner := durDisp
		if inner == nil {
			inner = mediateDispatcher{rm: rm}
		}
		durDisp = budgetSettlingDispatcher{inner: inner, check: cfg.Budget.check}
	}

	if durDisp != nil {
		runtimeOpts = append(runtimeOpts, agentruntime.WithActivityDispatcher(durDisp))
	}
	// FORMA FINAL DO EFEITO (AOS-005/AOS-064): a reescrita entra pela porta do LOOP, não
	// como decorator do dispatcher. É deliberado e foi corrigido depois de a via anterior
	// falhar ao vivo: como decorator, a reescrita corria DEPOIS de o loop já ter descrito o
	// efeito ao exterior (preview de aprovação, pendente mostrado ao operador), pelo que o
	// humano aprovava os args do modelo e o RM mediava o ExecRequest reescrito — duas
	// descrições do mesmo passo, e a aprovação nunca casava. Na porta do loop há uma só
	// descrição, e é a do efeito que realmente corre.
	//
	// O no-bypass NÃO depende disto: continua a ser estrutural no MediatedLauncher (a tool
	// só corre sob permit não-forjável) e no dispatcher (Mediate é a única via de efeito).
	if cfg.EffectRewriter != nil {
		runtimeOpts = append(runtimeOpts, agentruntime.WithCallRewriter(cfg.EffectRewriter))
	}

	rt := agentruntime.New(cfg.Model, rm, cfg.Recorder, runtimeOpts...)

	return &SecuredRuntime{
		rt:        rt,
		rm:        rm,
		catalog:   cfg.Catalog,
		toolsets:  toolsets,
		freezeOpt: cfg.FreezeOptions,
		ledger:    cfg.Ledger,
		budget:    cfg.Budget, // AOS-256: o ciclo de vida do nó por-run vive em Run
	}, nil
}

// CodeEffectRewrite é o código de Deny quando o [SecuredConfig.EffectRewriter] recusa a
// Call (ex.: args do modelo malformados). Fail-closed: nenhum efeito ocorre; a tool call
// materializa-se como Deny no tail do turno. É o MESMO símbolo do kernel — não uma segunda
// cópia da string — para o código não poder divergir entre as duas camadas.
const CodeEffectRewrite = agentruntime.CodeEffectRewrite

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

	// AOS-256 — NÓ DE ORÇAMENTO POR-RUN, no MESMO seam por-run do tool set congelado.
	//
	// Tem de acontecer ANTES do primeiro turno: o hook de orçamento debita o nó cujo id é o
	// RunID e, sem esse nó registado, [budget.Budget.Reserve] devolve ErrUnknownNode, que o
	// adaptador converte em deny — 100% das tool calls negadas por um defeito de wiring
	// disfarçado de falta de orçamento (o risco 4 do desafio A1).
	//
	// A libertação é `defer`: cobre o retorno normal, o erro do loop E o panic. Sem ela cada
	// run deixaria um nó vivo para sempre e a retoma do mesmo RunID colidiria.
	//
	// Fail-closed: se o nó não se consegue registar, o run NÃO arranca — correr sem nó seria
	// correr com tudo negado, e o operador procuraria a causa no sítio errado.
	if s.budget != nil {
		releaseBudget, berr := s.budget.acquire(goal.RunID)
		if berr != nil {
			return agentruntime.Result{}, nil, berr
		}
		defer releaseBudget()
	}

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
