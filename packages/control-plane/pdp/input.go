package pdp

// Tipos do contrato C1 RM↔PDP (tecnica/12 §4), alinhados campo-a-campo com o
// request/response da porta. São puros (sem dependência do RM) para que o PDP
// seja testável e reutilizável isoladamente; o adaptador rmadapter.go faz a
// tradução Call↔Input e Decision↔HookResult.

// PortVersion é o SemVer da porta C1 RM↔PDP (tecnica/12 §4) que este PDP
// implementa. O adaptador é IN-PROCESS ([PolicyCheck]): não há fronteira de wire
// onde negociar `port_version`, pelo que o campo não é transportado no [Input] /
// [Decision] (o RM e o PDP compilam contra o mesmo contrato). A constante existe
// para paridade com o schema C1 e para o dia em que o PDP for exposto por RPC:
// o servidor anuncia PortVersion e deve suportar as MINOR anteriores da mesma
// MAJOR (regra de compatibilidade de §4).
const PortVersion = "1.0.0"

// Effect é o veredicto de uma decisão de política (contrato C1).
type Effect string

const (
	// Permit — a capability é autorizada; o PEP pode despachar (impondo as
	// obligations devolvidas).
	Permit Effect = "permit"
	// Deny — negação de política (fail-closed): ausência de permit explícito.
	Deny Effect = "deny"
	// Escalate — requer gate humano (ADR-013). A política de referência §9 não
	// escala; o efeito existe para completude do contrato.
	Escalate Effect = "escalate"
)

// Principal é a identidade não-humana (NHI) que origina a tool call, a sua
// cadeia de delegação e a autoridade (capabilities) delegada — a intersecção
// utilizador ∩ classe já resolvida pelo RM (contrato C1; tecnica/12 §9).
type Principal struct {
	// ID é o identificador estável da NHI (NHIID). Usado como id da entidade
	// Cedar do principal.
	ID string
	// AgentClass é a classe da NHI (claim `agent_class` do token, AOS-005). É a
	// CHAVE da allowlist de capabilities (AOS-007): o gate default-deny de
	// [PDP.Decide] só concede uma capability que conste explicitamente da
	// allowlist assinada desta classe. Vazia ⇒ nenhuma allowlist ⇒ deny.
	AgentClass string
	// Board é o board de governação a que a NHI pertence (codificado no escopo de
	// identidade — EPIC-01). É a CHAVE da soberania por board (AOS-094): quando um
	// registo board→região está ligado ([WithBoardRegions]), o PDP resolve o board
	// para a sua região autorizada e devolve-a como obrigação `region` que o PEP
	// impõe (AOS-087). Vazio ⇒ sem board no escopo; um board DESCONHECIDO ao registo
	// é negado fail-closed (nunca cross-border por omissão — ADR-011).
	Board string
	// DelegationChain é a cadeia on-behalf-of (raiz humana → agente actual). Não
	// é avaliada pela política de referência §9; presente por fidelidade ao C1.
	DelegationChain []string
	// Authority são as capabilities que o principal pode exercer (allowlist);
	// mapeada para o atributo Cedar `principal.authority` (Set<String>).
	Authority []string
}

// Resource é o alvo concreto da tool call (contrato C1).
type Resource struct {
	Type   string // ex.: "url", "file", "db"
	Value  string // ex.: "https://api.example.com/orders"
	Region string // ex.: "eu" (soberania de dados); atributo Cedar `resource.region`
}

// DecisionContext transporta o contexto de decisão que a política avalia
// (contrato C1): taint, orçamento, reversibilidade e sensibilidade.
type DecisionContext struct {
	// Taint marca conteúdo untrusted que não pode autorizar acções privilegiadas
	// (ADR-005). Ex.: "trusted", "untrusted". Mapeado para `context.taint`.
	Taint string
	// BudgetTokensRemaining é o headroom de orçamento (ADR-008). Não avaliado
	// pela política §9 (o gate de orçamento é AOS-008); presente por fidelidade.
	BudgetTokensRemaining int64
	// Reversibility ex.: "reversible", "irreversible".
	Reversibility string
	// Sensitivity ex.: "public", "confidential". Mapeada para
	// `context.sensitivity`; dispara a obligation redact_pii quando "confidential".
	Sensitivity string
	// RiskClass é a CLASSE DE RISCO SA-ROC já computada pelo classificador do RM
	// ("safe"/"gray"/"danger"), transportada para o overlay de autonomia (AOS-089)
	// compor o oversight nível × classe. É OPCIONAL e aditiva ao contrato C1: só é
	// consultada quando um [autonomy.Oracle] está ligado; vazia ou desconhecida ⇒
	// tratada como "danger" (fail-closed, o pior caso). Não afecta a política Cedar
	// base nem as decisões sem oráculo de autonomia.
	RiskClass string
	// HumanGateSatisfied diz que o gate humano que o oversight de autonomia exigiria JÁ
	// ocorreu para ESTA acção concreta — uma aprovação four-eyes verificada, amarrada à
	// preview da call, de uso-único e dentro do TTL (AOS-021/AOS-162).
	//
	// NÃO é auto-declarável: o adaptador do RM só a põe a true a partir de
	// [rm.Call.HumanApproval], um campo NÃO-EXPORTADO que só o ApprovalGate escreve depois
	// de verificar a evidência. Nem o modelo nem o conteúdo untrusted lhe chegam.
	//
	// Efeito: a autonomia deixa de REBAIXAR o permit para escalate — porque o escalate
	// significa "requer gate humano" e o gate humano aconteceu. Não converte deny em permit,
	// não altera a política Cedar base e não toca no taint: uma acção negada por qualquer
	// outra razão continua negada.
	HumanGateSatisfied bool
}

// Input é o pedido de decisão submetido a [PDP.Decide] (request do contrato C1).
type Input struct {
	// RequestID correlaciona a decisão com o tracing (OTel); não afecta o
	// veredicto (a avaliação é pura).
	RequestID string
	// Principal é a identidade e a autoridade delegada.
	Principal Principal
	// Capability é o direito escopado a avaliar (ex.: "cap:http.post"); mapeado
	// para a Action Cedar.
	Capability string
	// Resource é o alvo da acção.
	Resource Resource
	// Context é o contexto de decisão.
	Context DecisionContext
}

// validate assegura o mínimo estrutural do request (contrato C1,
// E_MALFORMED_REQUEST). Uma capability vazia não tem Action a avaliar: é
// malformada e, por fail-closed, resulta em deny.
func (in Input) validate() error {
	if in.Capability == "" {
		return &Error{Code: ErrMalformedRequest.Code, Msg: "capability em falta"}
	}
	return nil
}

// Obligation é uma condição que o PEP deve impor após um permit (redação de
// PII, audit, TTL). Espelha o modelo do RM para tradução directa no adaptador.
type Obligation struct {
	Type   string            `json:"type"`
	Fields []string          `json:"fields,omitempty"`
	Params map[string]string `json:"params,omitempty"`
}

// Decision é o resultado de [PDP.Decide] (response do contrato C1). É SEMPRE
// devolvida (mesmo em negação): fail-closed produz uma Decision Deny, nunca a
// ausência de resposta.
type Decision struct {
	// Effect é o veredicto final.
	Effect Effect
	// Reason descreve a decisão (regra que permitiu, ou motivo da negação).
	Reason string
	// PolicyVersion é a versão (SemVer) do bundle que produziu a decisão; é
	// registada no evento de audit pelo RM (contrato C1, campo policy_version).
	PolicyVersion string
	// Obligations são as obrigações a impor (só em permit).
	Obligations []Obligation
}

// Permitted indica se a decisão autorizou a acção.
func (d Decision) Permitted() bool { return d.Effect == Permit }
