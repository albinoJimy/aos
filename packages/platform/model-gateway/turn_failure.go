package modelgateway

import (
	"errors"
	"net/http"

	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/port"
)

// A FALHA DE UM TURNO, POR CLASSE (AOS-521).
//
// Quem conta falhas por modelo — o disjuntor do candidato, no nó — precisa de saber, do erro de um
// turno, se foi a RESPOSTA do provider, o STATUS do provider, o limite de ritmo, ou a rota. Tudo em
// vocabulário fechado, lido dos tipos do erro e nunca do seu texto.

// Classes de falha de um turno.
const (
	// TurnFailureResponseRejected — o gateway recusou a resposta do provider
	// ([ResponseRejectionCause] devolve uma causa).
	TurnFailureResponseRejected = "resposta_rejeitada"
	// TurnFailureProviderStatus — o provider respondeu com um status que não é 200 nem 429.
	TurnFailureProviderStatus = "status_do_provider"
	// TurnFailureRateLimited — o provider respondeu 429: o limite de ritmo, que é da conta e não do
	// modelo.
	TurnFailureRateLimited = "limite_de_ritmo"
	// TurnFailureRouteDifferent — a governação da rota, em imposição, recusou o turno porque a rota
	// servida é DIFERENTE da do perfil. A rota não reportada não é esta classe.
	TurnFailureRouteDifferent = "rota_diferente"
)

// ClassifyTurnFailure classifica o erro de um turno numa das classes acima, ou devolve vazio: um
// erro de transporte sem resposta (rede, tempo esgotado), uma negação da pipeline do gateway, a
// rota não reportada, e qualquer outro erro não são de nenhuma delas.
func ClassifyTurnFailure(err error) string {
	var (
		rota   *RouteVarianceError
		status *adapters.StatusError
	)
	switch {
	case err == nil:
		return ""
	case errors.As(err, &rota):
		if rota.Check == port.RouteCheckDifferent {
			return TurnFailureRouteDifferent
		}
		return ""
	case ResponseRejectionCause(err) != "":
		return TurnFailureResponseRejected
	case errors.As(err, &status):
		if status.Status == http.StatusTooManyRequests {
			return TurnFailureRateLimited
		}
		return TurnFailureProviderStatus
	}
	return ""
}
