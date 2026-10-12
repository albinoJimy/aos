package modelgateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"

	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/port"
)

// A FALHA DE UM TURNO, POR CLASSE (AOS-521).
//
// Quem conta falhas por modelo — o disjuntor do candidato, no nó — precisa de saber, do erro de um
// turno, de quem é a falha. Tudo em vocabulário fechado, lido dos TIPOS do erro e nunca do seu texto.
//
// DUAS FAMÍLIAS. As classes «do modelo» dizem o que falhou. As classes «não é do modelo» são uma
// lista FECHADA de exclusões: o que não estiver nela, e não for de uma classe conhecida, devolve
// vazio — e quem conta trata o vazio como falha não classificada, pelo lado fechado.

// Classes de falha de um turno que são do modelo (ou da rota que o serve).
const (
	// TurnFailureResponseRejected — o gateway recusou a resposta do provider
	// ([ResponseRejectionCause] devolve uma causa).
	TurnFailureResponseRejected = "resposta_rejeitada"
	// TurnFailureProviderStatus — o provider respondeu com um status que não é 200, e que não é da
	// conta (429, 401, 403).
	TurnFailureProviderStatus = "status_do_provider"
	// TurnFailureRouteDifferent — a governação da rota, em imposição, recusou o turno porque a rota
	// servida é DIFERENTE da do perfil.
	TurnFailureRouteDifferent = "rota_diferente"
)

// Classes de falha de um turno que NÃO são do modelo. Lista fechada.
const (
	// TurnFailureRateLimited — o provider respondeu 429: o limite de ritmo, que é da conta.
	TurnFailureRateLimited = "limite_de_ritmo"
	// TurnFailureAccountAccess — o provider respondeu 401 ou 403: a credencial ou a permissão da
	// conta, não o modelo.
	TurnFailureAccountAccess = "acesso_da_conta"
	// TurnFailureTransport — não houve resposta HTTP: ligação recusada ou cortada, tempo esgotado,
	// pedido cancelado. O nó não sabe se foi o modelo ou o caminho até ao proxy.
	TurnFailureTransport = "transporte_sem_resposta"
	// TurnFailureGatewayDenied — um estágio da pipeline do gateway recusou o pedido ANTES de ele
	// sair (identidade, allowlist, soberania, orçamento): não depende do que o modelo fez.
	TurnFailureGatewayDenied = "negado_pelo_gateway"
	// TurnFailureRouteUnreported — a governação da rota, em imposição, recusou o turno porque o
	// proxy NÃO declarou a rota: não prova outro modelo.
	TurnFailureRouteUnreported = "rota_nao_reportada"
	// TurnFailurePinnedProfile — o run está fixado num perfil de rota que este nó não tem em vigor,
	// ou a rota exige um estado que não há para devolver: configuração do nó, e o pedido não saiu.
	TurnFailurePinnedProfile = "perfil_do_run"
)

// TurnFailureNotOfTheModel diz se a classe é uma das exclusões: a falha não é do modelo.
func TurnFailureNotOfTheModel(classe string) bool {
	switch classe {
	case TurnFailureRateLimited, TurnFailureAccountAccess, TurnFailureTransport, TurnFailureGatewayDenied,
		TurnFailureRouteUnreported, TurnFailurePinnedProfile:
		return true
	}
	return false
}

// ClassifyTurnFailure classifica o erro de um turno numa das classes acima, ou devolve vazio: um
// erro que não é de nenhuma classe conhecida NEM de nenhuma exclusão. Um 200 sem `usage`
// ([ErrUsageAusente]) e uma falha da contabilidade de custo ou da atribuição devolvem vazio de
// propósito: o provider respondeu, e a resposta não serviu.
func ClassifyTurnFailure(err error) string {
	var (
		rota      *RouteVarianceError
		status    *adapters.StatusError
		custo     *costError
		atribuido *attributionError
		fixado    *PinnedRouteProfileError
		estado    *StateReturnError
		estagio   *pipeline.StageError
		rede      net.Error
		pedido    *url.Error
	)
	switch {
	case err == nil:
		return ""
	case errors.As(err, &rota):
		if rota.Check == port.RouteCheckDifferent {
			return TurnFailureRouteDifferent
		}
		return TurnFailureRouteUnreported
	case ResponseRejectionCause(err) != "":
		return TurnFailureResponseRejected
	case errors.As(err, &status):
		switch status.Status {
		case http.StatusTooManyRequests:
			return TurnFailureRateLimited
		case http.StatusUnauthorized, http.StatusForbidden:
			return TurnFailureAccountAccess
		}
		return TurnFailureProviderStatus
	case errors.As(err, &custo), errors.As(err, &atribuido):
		// Depois de o provider responder: não é uma exclusão.
		return ""
	case errors.As(err, &fixado), errors.As(err, &estado):
		return TurnFailurePinnedProfile
	case errors.As(err, &estagio):
		return TurnFailureGatewayDenied
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.As(err, &pedido), errors.As(err, &rede):
		return TurnFailureTransport
	}
	return ""
}
