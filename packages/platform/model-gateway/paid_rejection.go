package modelgateway

import (
	"errors"

	"github.com/aos-ref/platform/model-gateway/port"
)

// PaidRejectionError é o erro de uma chamada que o provider RESPONDEU, cujo custo o gateway JÁ
// CONTOU pela tabela de preços, e que mesmo assim foi recusada depois disso — hoje, a comparação
// do modelo servido com o perfil da rota em `enforce` (ADR-036 §2.8) e um estágio de metering que
// falhe a seguir ao custo.
//
// PORQUE EXISTE (AOS-530, ADR-042 §2.6). O efeito de uma tool cujo efeito é uma chamada a um
// modelo custou dinheiro mesmo quando o gateway recusa a resposta — «custou e grava-se». Sem este
// tipo, [Gateway.Chat] devolvia só o erro e o custo perdia-se no caminho; a tool não o podia gravar
// no evento durável nem saldar a reserva no tecto em dólares do run.
//
// SÓ PARA PEDIDOS DE UMA TOOL. O gateway só o devolve a um pedido com [port.ChatRequest.OriginTool]
// preenchido. Um turno do executor nunca o preenche, e vê exactamente o erro de antes — o mesmo
// valor, não um invólucro —, pelo que nada muda no que o loop, a admissão e as métricas de um
// turno observam.
//
// O invólucro é transparente: [PaidRejectionError.Error] é o texto do erro de dentro e
// [PaidRejectionError.Unwrap] devolve-o, pelo que `errors.Is(err, ErrRouteVariance)` e
// `errors.As(err, &*RouteVarianceError)` continuam a valer.
type PaidRejectionError struct {
	// Err é a recusa, tal como o gateway a devolveria sem o invólucro.
	Err error
	// Usage é o usage medido da resposta recusada, com [port.Usage.CostMicroUSD] já calculado.
	Usage port.Usage
	// ServedModel é o modelo que o proxy declarou ter servido (sanitizado), ou vazio.
	ServedModel string
}

// Error devolve o texto do erro de dentro, sem acrescentar nada.
func (e *PaidRejectionError) Error() string { return e.Err.Error() }

// Unwrap liga ao erro de dentro.
func (e *PaidRejectionError) Unwrap() error { return e.Err }

// PaidRejectionOf devolve o usage medido e o modelo servido de uma recusa paga, e se o erro o é.
func PaidRejectionOf(err error) (port.Usage, string, bool) {
	var p *PaidRejectionError
	if errors.As(err, &p) {
		return p.Usage, p.ServedModel, true
	}
	return port.Usage{}, "", false
}
