package modelgateway

import (
	"errors"

	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
)

// port_codes.go expõe os códigos de porta ESTÁVEIS do contrato C4 (GW ↔ Provider,
// tecnica/12 §7) e a tradução dos erros do gateway para esses códigos.
//
// PORQUÊ. O gateway não expunha códigos de porta: as recusas são sentinelas por
// subpacote do pipeline ([allowlist.ErrModelNotAllowed] na soberania regional,
// [ErrUnknownProvider]/[ErrNoBaseURL] na montagem do adaptador). Estas constantes
// dão à porta C4 os dois códigos que o contrato nomeia e que têm equivalente REAL
// no gateway, sem mudar a semântica de nenhum erro nem partir quem já compara por
// `errors.Is` — é adição, não renomeação.
//
// E_RATE_LIMITED do contrato NÃO vive aqui: o admission control global é do
// scheduler (control-plane), não uma condição desta porta — ver a nota em
// tecnica/12 §7.
const (
	// CodeRegionDenied — o par (board, modelo, região) está fora da allowlist
	// regional (soberania default-deny). Contrato C4.
	CodeRegionDenied = "E_REGION_DENIED"
	// CodeModelUnavailable — não há adaptador/endpoint conhecido para o provider
	// pedido. Contrato C4.
	CodeModelUnavailable = "E_MODEL_UNAVAILABLE"
)

// PortErrorCode traduz um erro devolvido pela porta do gateway no código de porta
// ESTÁVEL do contrato C4, ou "" quando não é um erro de porta reconhecido (nil
// incluído). ADITIVA: o mapeamento é por [errors.Is], pelo que embrulhos
// (`fmt.Errorf("%w", …)`) continuam a resolver-se.
func PortErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, allowlist.ErrModelNotAllowed):
		return CodeRegionDenied
	case errors.Is(err, ErrUnknownProvider), errors.Is(err, ErrNoBaseURL):
		return CodeModelUnavailable
	}
	return ""
}
