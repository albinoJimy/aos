package broker

import "errors"

// port_codes.go expõe os códigos de porta ESTÁVEIS do contrato C3 (RM ↔ BRK,
// tecnica/12 §6) e a tradução dos erros sentinela do broker para esses códigos.
//
// PORQUÊ. O broker modela as suas recusas com erros sentinela (`errors.New`) e um
// [DeniedError]{Effect,Code,Reason} cujo `Code` vem da decisão do Reference
// Monitor, não do contrato de porta. Um consumidor da porta C3 não tinha, por
// isso, um código ESTÁVEL por onde distinguir as três condições que o contrato
// nomeia. Estas constantes dão-lhe esse código sem mudar a semântica de nenhum
// erro existente nem partir quem já compara por `errors.Is` — é uma adição, não
// uma renomeação. A atribuição de auditoria server-side ([denialCode]) continua a
// ser um eixo SEPARADO (códigos em minúsculas, greppáveis no Event Store); estes
// são os códigos de PORTA, virados para o chamador do contrato.
const (
	// CodeNoDecision — a troca foi recusada pela mediação sem um `permit` válido
	// (o `decision_ref` não corresponde a uma autorização prévia). Contrato C3.
	CodeNoDecision = "E_NO_DECISION"
	// CodeScopeDenied — o escopo pedido excede a autoridade delegada (utilizador ∩
	// classe, eixo provider ou eixo recurso). Contrato C3.
	CodeScopeDenied = "E_SCOPE_DENIED"
	// CodeVaultUnavailable — sem material no Vault (ou Vault ausente): sem
	// credencial, sem execução (fail-closed). Contrato C3.
	CodeVaultUnavailable = "E_VAULT_UNAVAILABLE"
)

// PortErrorCode traduz um erro devolvido pela porta do broker no código de porta
// ESTÁVEL do contrato C3, ou "" quando o erro não é um erro de porta reconhecido
// (nil incluído). É ADITIVA: o mapeamento é feito por [errors.Is]/[errors.As],
// pelo que embrulhos e sentinelas re-exportados continuam a resolver-se.
//
// A ordem importa: um fora-de-escopo é primeiro escopo (E_SCOPE_DENIED) antes de
// um genérico E_NO_DECISION, para que a condição mais específica ganhe.
func PortErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrOutOfScope),
		errors.Is(err, ErrProviderOutOfScope),
		errors.Is(err, ErrResourceOutOfScope):
		return CodeScopeDenied
	case errors.Is(err, ErrNoMaterial), errors.Is(err, ErrNilVault):
		return CodeVaultUnavailable
	}
	// Uma recusa da mediação sem `permit` (o broker nunca é o primeiro gate) é o
	// E_NO_DECISION do contrato.
	var denied *DeniedError
	if errors.As(err, &denied) {
		return CodeNoDecision
	}
	return ""
}
