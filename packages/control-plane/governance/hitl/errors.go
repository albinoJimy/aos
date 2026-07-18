package hitl

import "errors"

// Sentinelas de erro do módulo. Comparáveis com errors.Is inclusive quando
// embrulhadas com fmt.Errorf("%w: …", …). TODAS as rejeições resolvem-se pelo lado
// seguro (fail-closed): ausência de aprovador autorizado, de assinatura válida ou
// de resposta dentro do prazo é sempre RECUSA, nunca acção silenciosa.
var (
	// ErrNilDeps — dependências obrigatórias do Channel/SignApproval em falta
	// (registry, source ou sealer nil). Fail-closed na construção.
	ErrNilDeps = errors.New("hitl: dependencias obrigatorias em falta")

	// ErrInvalidApproval — a decisão a assinar/verificar está malformada
	// (request-id/aprovador em falta, nonce curto, timestamp zero ou assinatura de
	// dimensão errada). Fail-closed.
	ErrInvalidApproval = errors.New("hitl: aprovacao invalida (request-id/aprovador/nonce/timestamp/assinatura malformados)")
)

// Motivos de decisão do gate, estáveis e legíveis-por-máquina. Selados no audit
// (numa [audit.Obligation]) para que cada decisão seja atribuível ao aprovador (ou,
// quando não autenticado, à tentativa) e à natureza do desfecho.
const (
	// ReasonApproved — decisão assinada de APROVAÇÃO, verificada e por aprovador
	// autorizado (o único caminho que devolve Approved=true).
	ReasonApproved = "approved"
	// ReasonRefused — decisão assinada de RECUSA (o aprovador negou explicitamente;
	// é também não-repúdio e é selada).
	ReasonRefused = "refused"
	// ReasonTimeout — acção (tipicamente irreversível) negada por TIMEOUT/silêncio:
	// o fail-closed do Art. 14. Conta em Timeouts.
	ReasonTimeout = "timeout_fail_closed"
	// ReasonUnknownApprover — o aprovador não consta do registo (sem chave pública
	// pinada): não é sequer autenticável. Fail-closed.
	ReasonUnknownApprover = "unknown_approver"
	// ReasonAuthorityNotCovered — o aprovador é autêntico mas a sua autoridade NÃO
	// cobre a classe da acção (principal sem autoridade). Fail-closed (AC2).
	ReasonAuthorityNotCovered = "authority_not_covered"
	// ReasonForgedSignature — a assinatura não valida contra a chave pinada do
	// aprovador clamado (forjada/adulterada). Fail-closed (AC4).
	ReasonForgedSignature = "forged_signature"
	// ReasonInvalidApproval — a resposta assinada está malformada ou não corresponde
	// ao request-id apresentado (replay de uma aprovação de outro pedido). Fail-closed.
	ReasonInvalidApproval = "invalid_approval"
	// ReasonDualControl — 4-eyes violado: para danger, o aprovador é o MESMO que o
	// solicitante. Fail-closed (AC6, dual-control).
	ReasonDualControl = "dual_control_4eyes"
	// ReasonNotGated — a classe não é gatável (safe corre sem gate): se uma acção
	// safe chega ao canal, é um erro de wiring — nega fail-closed.
	ReasonNotGated = "not_gated"
)
