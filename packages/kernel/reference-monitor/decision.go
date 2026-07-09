package referencemonitor

import (
	"sync/atomic"
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
// chamador pode ramificar por igualdade sem fazer parse de strings livres. Os
// que espelham um sentinela ([MonitorError.Code]) reutilizam o mesmo valor.
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
	// CodeToolNotRegistered — tool não registada (default-deny). Espelha
	// [ErrToolNotRegistered].Code.
	CodeToolNotRegistered = "E_TOOL_NOT_REGISTERED"
	// CodeAuditUnavailable — falha de auditoria no caminho de permit; degradou
	// para deny. Espelha [ErrAuditUnavailable].Code.
	CodeAuditUnavailable = "E_AUDIT_UNAVAILABLE"
)

// Obligation é uma obrigação que o PEP deve impor sobre uma decisão permit
// (ex.: redigir PII, TTL, nível de audit). No AOS-003 os stubs não produzem
// obrigações; o contrato existe para AOS-004 as devolver.
type Obligation struct {
	Type   string            // ex.: "redact_pii", "audit", "ttl"
	Fields []string          // ex.: ["email", "phone"]
	Params map[string]string // parâmetros genéricos (ex.: {"seconds": "3600"})
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

	// permit é o token não-forjável emitido só em Permit (nil caso contrário).
	// É não-exportado: código externo não o consegue construir nem inspeccionar,
	// o que sustenta o no-bypass estrutural.
	permit *Permit
}

// Permitted indica se a decisão autorizou (e despachou) a tool call, com um
// permit válido associado.
func (d Decision) Permitted() bool {
	return d.Effect == EffectPermit && d.permit != nil && d.permit.tok != nil
}

// permitToken é o segredo não-forjável de um Permit. Todos os campos são
// não-exportados e o tipo é não-exportado: nenhum pacote externo consegue
// construir um permitToken válido, logo nenhum consegue forjar um Permit
// aceite por dispatch.
type permitToken struct {
	fingerprint uint64      // liga o permit ao call que o originou
	nonce       uint64      // valor único por emissão (evidência anti-replay)
	used        atomic.Bool // uso único: dispatch consome via CompareAndSwap
}

// Permit é a prova não-forjável de que uma tool call foi autorizada por
// Mediate. Só o pacote reference-monitor consegue mintar um Permit com token
// válido (ver Monitor.mint). Um Permit{} zero (ou qualquer um construído fora
// deste pacote) tem tok == nil e é rejeitado por dispatch com [ErrInvalidPermit].
//
// A ligação ao call que o originou vive no fingerprint do token (permitToken.
// fingerprint), validado por dispatch — não é preciso embeber o Call.
type Permit struct {
	tok *permitToken
}
