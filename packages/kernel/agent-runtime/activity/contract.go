package activity

import (
	"context"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/saga"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// StatusOK é o Status memorizado por omissão no ledger para uma activity aplicada
// com sucesso. O chamador pode sobrepô-lo em [Activity.Status].
const StatusOK = "ok"

// Mode é o modo de operação do [Dispatcher].
type Mode int

const (
	// ModeNormal executa o efeito: deriva a key, verifica already-applied no ledger,
	// medeia pelo RM antes de executar e memoriza o resultado (append-only) — o fluxo
	// completo do contrato. Requer [Mediator] e [Ledger].
	ModeNormal Mode = iota
	// ModeReplay NÃO executa nada: devolve o resultado REGISTADO da [ReplaySource] com
	// ZERO efeito, SEM mediação nem execução (AOS-016). Requer [ReplaySource] e, por
	// construção, NÃO detém um [Mediator] — a ausência de efeito é estrutural.
	ModeReplay
)

func (m Mode) String() string {
	switch m {
	case ModeNormal:
		return "normal"
	case ModeReplay:
		return "replay"
	default:
		return "unknown"
	}
}

// Compensation é a acção INVERSA (reversível) de uma activity, a registar em AOS-020.
// O step_id é o da própria activity — o dispatcher deriva a [saga.Compensation] a
// partir dele, pelo que o chamador não o repete aqui.
type Compensation struct {
	// Action é o efeito inverso. PRÉ-CONDIÇÃO (contrato de AOS-020, não imposta aqui):
	// DEVE ser idempotente — o ledger é at-least-once. Não deve conter segredos observáveis.
	Action func(ctx context.Context) error
	// Reason é um rótulo legível (auditoria); NUNCA um segredo. Opcional.
	Reason string
}

// Activity encapsula UM efeito externo (tool call / I/O / rede) a isolar do loop.
// É uma DESCRIÇÃO declarativa: NÃO contém uma função de efeito directamente
// invocável — o efeito é a tool identificada por [Activity.ToolID], registada no
// Reference Monitor, e só corre sob permit de [referencemonitor.Monitor.Mediate].
// É esta indirecção que sustenta o no-bypass estrutural (ver doc.go).
//
// A identidade durável da activity é (RunID, StepID): a idempotency key
// run_id:step_id (AOS-014) é estável entre execução, retry e replay, o que permite
// que o MESMO efeito lógico seja identificado, deduplicado e reproduzido.
type Activity struct {
	// RunID é o run (stream do Event Store) a que a activity pertence.
	RunID string
	// StepID é o step_id ESTÁVEL da activity (tipicamente um sub-passo do turno, via
	// [durable.StepSequencer.SubStepID]) — puro e reprodutível entre tentativas/replay.
	//
	// PRÉ-CONDIÇÃO DE SEGURANÇA (AOS021-Q2). A idempotency key liga-se APENAS a
	// (RunID, StepID) — NÃO aos parâmetros do call (ToolID/Capability/Resource/Input).
	// No caminho dedup (already-applied) a mediação do RM é SALTADA e o resultado
	// registado é devolvido SEM re-verificar que o call actual corresponde ao que foi
	// mediado da primeira vez. Logo o chamador DEVE garantir que o MESMO (RunID, StepID)
	// identifica SEMPRE o MESMO call lógico: um step_id reutilizado ou derivado de forma
	// não-determinística (p.ex. um modelo que emite uma tool call DIFERENTE no mesmo
	// índice de sub-passo) devolveria o resultado obsoleto do primeiro call e contornaria
	// o RM na segunda chamada. A estabilidade determinística do step_id — via o
	// [durable.StepSequencer] alimentado por âncoras reprodutíveis — é o que sustenta
	// esta invariante. (A ligação forte call→resultado por fingerprint durável fica para
	// o adaptador de engine / evolução do evento de ledger; ver doc.go, "Âncora de
	// identidade: step_id".)
	StepID string
	// ToolID identifica a tool registada no RM (o efeito externo). Default-deny: uma
	// tool não registada é negada.
	ToolID string
	// Capability é o direito escopado que a política avalia (ex.: "cap:http.post").
	Capability string
	// Resource é o alvo concreto do efeito (contrato C1 do RM).
	Resource referencemonitor.Resource
	// Principal é a identidade não-humana que origina o efeito (resolvida/validada
	// pelo hook de identidade do RM).
	Principal referencemonitor.Principal
	// Input é o payload opaco entregue à tool após permit.
	Input []byte
	// Reversibility/Sensitivity/Budget alimentam o [referencemonitor.CallContext] que
	// a política avalia. O Taint é SEMPRE forçado a untrusted (ADR-005): a intenção de
	// tool call vem do modelo, logo é untrusted; o campo não é configurável aqui.
	Reversibility         string
	Sensitivity           string
	BudgetTokensRemaining int64
	// Status é o rótulo de desfecho memorizado no ledger (default [StatusOK]).
	Status string
	// (AOS-212) O custo do efeito NÃO é um campo de ENTRADA da Activity. O span
	// aos.activity anota-o a partir do DESFECHO real do efeito — o custo MEDIDO que a
	// tool reporta ao Reference Monitor ([referencemonitor.Decision.CostMicroUSD]),
	// captado por canal lateral em [Dispatcher.Dispatch] (ver dispatch.go). Um custo
	// declarado à cabeça na Activity seria estimativa, não medição, e — pior — sobreviveria
	// ao dedup/replay; a fonte correcta é o desfecho, que só ocorre no efeito real.
	// Compensation, se não-nil, é registada no [CompensationRegistrar] no momento do
	// permit (AOS-020). Exige um registrar configurado ([WithCompensationRegistry]).
	Compensation *Compensation
	// Credential é o token NHI (AOS-005/AOS-152) que autentica o Principal do efeito.
	// É PROPAGADO ao [referencemonitor.Call] (Credential), onde o hook de identidade o
	// verifica e resolve a autoridade — vazio ⇒ chamada anónima, negada fail-closed sob
	// o hook de identidade real. Preserva a identidade quando o loop do Agent Runtime
	// (AOS-157) despacha por este contrato: sem ele, encaminhar o despacho pelo
	// Dispatcher perderia o Credential do Call e degradaria toda a tool call a anónima.
	// NÃO entra na idempotency key (é (RunID, StepID)); é material de mediação, não de
	// identidade da activity.
	Credential string
	// ApprovalEvidence é a evidência BRUTA de aprovação humana (AOS-021) anexada à call.
	// É PROPAGADA ao [referencemonitor.Call], onde o ApprovalGate a VERIFICA — sem ela,
	// encaminhar o despacho por este contrato perderia a prova e a acção escalada nunca
	// destravaria (o run ficaria a escalar para sempre). Opaca e untrusted: nada nela é
	// acreditado até ser verificada. NÃO entra na idempotency key (é (RunID, StepID)) nem
	// é memorizada no ledger — é material de mediação, como o Credential.
	ApprovalEvidence []byte
}

func (a Activity) validate() error {
	if a.RunID == "" {
		return ErrEmptyRunID
	}
	if a.StepID == "" {
		return ErrEmptyStepID
	}
	if a.ToolID == "" {
		return ErrEmptyToolID
	}
	return nil
}

// toCall traduz a activity num [referencemonitor.Call]. O Taint é SEMPRE untrusted.
func (a Activity) toCall() referencemonitor.Call {
	return referencemonitor.Call{
		RunID:      a.RunID,
		StepID:     a.StepID,
		ToolID:     a.ToolID,
		Capability: a.Capability,
		Resource:   a.Resource,
		Principal:  a.Principal,
		Credential: a.Credential,
		Context: referencemonitor.CallContext{
			Taint:                 agentruntime.TaintUntrusted,
			BudgetTokensRemaining: a.BudgetTokensRemaining,
			Reversibility:         a.Reversibility,
			Sensitivity:           a.Sensitivity,
		},
		Input:            a.Input,
		ApprovalEvidence: a.ApprovalEvidence,
	}
}

// Result é o desfecho de [Dispatcher.Dispatch].
type Result struct {
	// Output é o resultado do efeito, SEMPRE marcado untrusted (ADR-005) — quer venha
	// de uma execução AGORA, de um dedup do ledger, ou do log em replay.
	Output agentruntime.Tainted
	// Status é o rótulo de desfecho memorizado (ou registado) para a activity.
	Status string
	// Deduplicated indica que o resultado veio do ledger SEM re-executar o efeito
	// (already-applied, AOS-014) em [ModeNormal].
	Deduplicated bool
	// Replayed indica que o resultado veio da [ReplaySource] em [ModeReplay] (ZERO
	// efeito, sem mediação).
	Replayed bool
}

// ---------------------------------------------------------------------------
// Portas (agnósticas ao engine) — as peças que o dispatcher COMPÕE.
// ---------------------------------------------------------------------------

// Mediator é a superfície do Reference Monitor de que o dispatcher depende: a ÚNICA
// via de despacho de um efeito. *[referencemonitor.Monitor] satisfaz-a.
type Mediator interface {
	Mediate(ctx context.Context, call referencemonitor.Call) (referencemonitor.Decision, error)
}

// Ledger é o subconjunto do step-ledger de AOS-014 de que o dispatcher depende em
// modo normal: Apply (efeito idempotente com already-applied ANTES do efeito).
// *[durable.StepLedger] satisfá-lo.
type Ledger interface {
	Apply(ctx context.Context, key string, effect func(context.Context) (durable.Result, error)) (durable.Result, bool, error)
}

// ReplaySource devolve o resultado REGISTADO de uma activity pela sua idempotency
// key, SEM qualquer efeito. É o ponto de cruzamento com AOS-016 (o resultado vive no
// log): *[durable.StepLedger] satisfá-la via Applied (após [durable.StepLedger.Rebuild]
// do stream do run), e um adaptador de AOS-016/engine externo pode implementá-la
// sobre o seu journal.
type ReplaySource interface {
	Applied(key string) (durable.Result, bool)
}

// CompensationRegistrar é o subconjunto do registo de compensações de AOS-020 usado
// para associar a acção inversa ao step_id. *[saga.CompensationRegistry] satisfá-lo.
type CompensationRegistrar interface {
	Register(c saga.Compensation) error
}

// ---------------------------------------------------------------------------
// Observabilidade (contadores; sem SDK OTel — EPIC-08). Chaves SEMPRE opacas.
// ---------------------------------------------------------------------------

// Observer é o gancho de observabilidade do dispatcher. Recebe a key na forma OPACA
// (hash) — nunca a chave em claro nem o payload — para honrar "sem segredos em logs".
// Default: [NopObserver].
type Observer interface {
	// Applied — o efeito correu AGORA (permit + execução) e foi memorizado.
	Applied(keyHash string)
	// Deduplicated — o resultado veio do ledger (already-applied), sem novo efeito.
	Deduplicated(keyHash string)
	// Replayed — o resultado veio do log em modo replay (zero efeito, sem mediação).
	Replayed(keyHash string)
	// Denied — o RM negou/escalou o efeito (nenhum efeito ocorreu).
	Denied(keyHash string)
}

// NopObserver descarta os eventos de observabilidade. É o default.
type NopObserver struct{}

func (NopObserver) Applied(string)      {}
func (NopObserver) Deduplicated(string) {}
func (NopObserver) Replayed(string)     {}
func (NopObserver) Denied(string)       {}

// Verificações em tempo de compilação de que as peças reais satisfazem as portas.
var (
	_ Mediator              = (*referencemonitor.Monitor)(nil)
	_ Ledger                = (*durable.StepLedger)(nil)
	_ ReplaySource          = (*durable.StepLedger)(nil)
	_ CompensationRegistrar = (*saga.CompensationRegistry)(nil)
)
