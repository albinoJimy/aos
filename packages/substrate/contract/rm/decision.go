package rm

import (
	"time"
)

// Effect é o veredicto de uma mediação (contrato C1, tecnica/12 §4).
type Effect string

const (
	// EffectPermit — a tool call é autorizada e foi despachada.
	EffectPermit Effect = "permit"
	// EffectDeny — a tool call é negada (fail-closed); nenhum efeito ocorre.
	EffectDeny Effect = "deny"
	// EffectEscalate — requer gate humano (ADR-013); nenhum efeito ocorre até aprovação.
	EffectEscalate Effect = "escalate"
)

// Códigos estáveis de decisão expostos em [Decision.Code]. São contrato: um
// chamador pode ramificar por igualdade sem fazer parse de strings livres.
const (
	// CodePermit — decisão permit (código vazio por convenção).
	CodePermit = ""
	// CodeContextCanceled — negado porque o contexto já estava cancelado.
	CodeContextCanceled = "E_CONTEXT_CANCELED"
	// CodeEmptyHookChain — negado porque a cadeia de hooks está vazia
	// (misconfiguração fail-closed; ver [WithHooks]).
	CodeEmptyHookChain = "E_EMPTY_HOOK_CHAIN"
	// CodeDeniedByHook — um hook devolveu deny.
	CodeDeniedByHook = "E_DENIED_BY_HOOK"
	// CodeEscalated — um hook requereu gate humano (escalate).
	CodeEscalated = "E_ESCALATED"
	// CodeHookError — um hook devolveu erro ou entrou em panic (fail-closed).
	CodeHookError = "E_HOOK_ERROR"
	// CodeToolNotRegistered — tool não registada (default-deny).
	CodeToolNotRegistered = "E_TOOL_NOT_REGISTERED"
	// CodeAuditUnavailable — falha de auditoria no caminho de permit; degradou
	// para deny.
	CodeAuditUnavailable = "E_AUDIT_UNAVAILABLE"
	// CodeObligationUnsatisfied — uma obrigação da decisão não pôde ser cumprida
	// pelo PEP antes do efeito (região cross-border, redação não-garantida, ou uma
	// obrigação de tipo desconhecido). Fail-closed: uma obrigação que o PEP não
	// sabe/consegue impor NÃO liberta o efeito (AOS-087, ADR-002).
	CodeObligationUnsatisfied = "E_OBLIGATION_UNSATISFIED"
)

// Obligation é uma obrigação que o PEP deve impor sobre uma decisão permit
// (ex.: redigir PII, TTL, nível de audit). No AOS-003 os stubs não produzem
// obrigações; o contrato existe para AOS-004 as devolver.
type Obligation struct {
	Type   string            // ex.: "redact_pii", "audit", "ttl"
	Fields []string          // ex.: ["email", "phone"]
	Params map[string]string // parâmetros genéricos (ex.: {"seconds": "3600"})
}

// Permit é a prova opaca de que uma tool call foi autorizada por [Monitor.Mediate].
// O token concreto não-exportado só pode ser mintado pela implementação do RM,
// pelo que nenhum pacote externo consegue forjar um Permit aceite.
//
// A ligação ao call que o originou vive na implementação concreta e é validada
// pelo dispatch — não é preciso embeber o Call.
type Permit interface {
	// isPermit() é um método selo: só a implementação do RM pode satisfazer esta
	// interface, mantendo o no-bypass estrutural.
	isPermit()
}

// Decision é o resultado de [Monitor.Mediate]. É sempre devolvida (mesmo em
// negação): fail-closed produz uma Decision Deny, nunca a ausência de resposta.
type Decision struct {
	// Effect é o veredicto final.
	Effect Effect
	// Code é um código estável e legível-por-máquina da decisão (ver as
	// constantes Code*). Permite ao chamador ramificar programaticamente sem
	// fazer parse de Reason — ex.: distinguir CodeAuditUnavailable de
	// CodeDeniedByHook. Vazio (CodePermit) num permit.
	Code string
	// Reason descreve a decisão (motivo de negação ou de permissão).
	Reason string
	// DeniedBy é o nome do hook que negou/escalou (vazio em permit).
	DeniedBy string
	// Obligations são as obrigações a impor (só relevantes em permit).
	Obligations []Obligation
	// Latency é o tempo total de mediação (avaliação + registo + despacho).
	Latency time.Duration
	// MediationSeq é o seq do evento de mediação no Event Store (0 se o registo
	// não produziu seq).
	MediationSeq uint64
	// Output é o resultado devolvido pela tool despachada (só em permit).
	Output []byte
	// ToolErr é o erro devolvido pela tool despachada, se houver (só em permit).
	// Nota: um erro DA TOOL não é uma negação de política — a decisão foi Permit
	// e o efeito ocorreu; ToolErr reporta a falha de execução downstream.
	ToolErr error

	// Permit é a prova opaca de autorização. Só o RM consegue preenchê-lo; o
	// método Permitted() nunca fica true com um Permit forjado.
	Permit Permit
}

// Permitted indica se a decisão autorizou (e despachou) a tool call, com um
// Permit válido associado.
func (d Decision) Permitted() bool {
	return d.Effect == EffectPermit && d.Permit != nil
}

// FingerprintInput encapsula os campos de um Call usados para calcular a
// impressão digital determinística que liga um Permit à acção autorizada.
// A função [FingerprintOf] devolve o valor canónico.
type FingerprintInput struct {
	RunID        string
	StepID       string
	ToolID       string
	Capability   string
	ResourceType string
	ResourceValue string
	NHIID        string
}

