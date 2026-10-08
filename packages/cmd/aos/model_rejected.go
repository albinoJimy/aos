package main

// AS RESPOSTAS RECUSADAS DO PROVIDER (AOS-509) — `aos_model_response_rejected_total{causa}`.
//
// Uma resposta que o gateway não consegue transformar num turno faz falhar o turno. A causa é
// contada num vocabulário FECHADO ([modelgateway.ResponseRejectionCauses]), sem nenhum byte da
// resposta. Não tem interruptor: existe sempre que o gateway de modelo está composto.

import (
	"context"
	"sync/atomic"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// contadoresDeRejeicao conta, por processo, as respostas recusadas por causa.
type contadoresDeRejeicao struct {
	causas []string
	total  map[string]*atomic.Int64
}

func novosContadoresDeRejeicao() *contadoresDeRejeicao {
	c := &contadoresDeRejeicao{causas: modelgateway.ResponseRejectionCauses(), total: map[string]*atomic.Int64{}}
	for _, causa := range c.causas {
		c.total[causa] = new(atomic.Int64)
	}
	return c
}

// observar é o [modelgateway.ResponseRejectedObserver]. Uma causa fora do vocabulário não é
// contada: o texto recebido nunca vira rótulo.
func (c *contadoresDeRejeicao) observar(causa string) {
	if c == nil {
		return
	}
	if n := c.total[causa]; n != nil {
		n.Add(1)
	}
}

// lido devolve o total de uma causa do vocabulário.
func (c *contadoresDeRejeicao) lido(causa string) int64 {
	if c == nil || c.total[causa] == nil {
		return 0
	}
	return c.total[causa].Load()
}

// modelResponseRejected devolve os contadores das respostas recusadas e a opção do adaptador
// que os alimenta.
func modelResponseRejected() (*contadoresDeRejeicao, modelgateway.RuntimeAdapterOption) {
	c := novosContadoresDeRejeicao()
	return c, modelgateway.WithResponseRejectedObserver(c.observar)
}

// clienteComRejeicoes é o cliente de modelo do nó com os contadores das respostas recusadas à
// mão, no molde de [clienteComRota]: não muda nenhuma chamada.
type clienteComRejeicoes struct {
	inner      agentruntime.ModelClient
	contadores *contadoresDeRejeicao
}

// Call implementa [agentruntime.ModelClient], sem tocar em nada.
func (c clienteComRejeicoes) Call(ctx context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	return c.inner.Call(ctx, view)
}

func (c clienteComRejeicoes) respostasRecusadas() *contadoresDeRejeicao { return c.contadores }

// rotaDoModelo e formaDaResposta passam adiante os contadores do cliente envolvido.
func (c clienteComRejeicoes) rotaDoModelo() *contadoresDaRota     { return rotaDoCliente(c.inner) }
func (c clienteComRejeicoes) formaDaResposta() *contadoresDaForma { return formaDoCliente(c.inner) }

// fonteDeRejeicoes é o que um cliente de modelo (ou um decorador dele) expõe para os contadores
// das respostas recusadas serem encontrados.
type fonteDeRejeicoes interface {
	respostasRecusadas() *contadoresDeRejeicao
}

// rejeicoesDoCliente devolve os contadores das respostas recusadas do cliente dado, ou nil — o
// modelo de referência, ou um cliente injectado que não os tem.
func rejeicoesDoCliente(c agentruntime.ModelClient) *contadoresDeRejeicao {
	if f, ok := c.(fonteDeRejeicoes); ok {
		return f.respostasRecusadas()
	}
	return nil
}
