package modelgateway

import "github.com/aos-ref/platform/model-gateway/port"

// ArmarDevolucaoParaTeste expõe [armarDevolucao] aos testes externos do pacote.
func ArmarDevolucaoParaTeste(req port.ChatRequest, perfil RouteProfile) (turnos, devolvidos int, causa string, err error) {
	return armarDevolucao(&req, perfil, true)
}

// ArmarEEscreverParaTeste arma a devolução para o perfil dado e devolve o pedido tal como iria
// no wire (AOS-516).
func ArmarEEscreverParaTeste(req port.ChatRequest, perfil RouteProfile) ([]byte, error) {
	if _, _, _, err := armarDevolucao(&req, perfil, true); err != nil {
		return nil, err
	}
	return req.MarshalWire(false)
}
