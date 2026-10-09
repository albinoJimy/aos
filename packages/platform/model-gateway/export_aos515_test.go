package modelgateway

import "github.com/aos-ref/platform/model-gateway/port"

// ArmarDevolucaoParaTeste expõe [armarDevolucao] aos testes externos do pacote.
func ArmarDevolucaoParaTeste(req port.ChatRequest, perfil RouteProfile) (turnos, devolvidos int, causa string, err error) {
	return armarDevolucao(&req, perfil, true)
}
