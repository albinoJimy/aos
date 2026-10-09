package main

// model_route_params.go — OS 4XX DOS TURNOS COM PARÂMETROS DO PERFIL DA ROTA (AOS-513).
//
// O perfil de uma rota pode declarar parâmetros do pedido (`thinking`, `reasoning_effort`,
// `max_tokens`), e o proxy reencaminha-os todos ao provider: um que o provider não aceite dá
// 4xx em TODOS os turnos da rota. O nó não retira o parâmetro nem repete o pedido — o turno
// falha como falhava —; o que passa a haver é o NOME: cada 4xx de um turno em que o perfil
// enviou parâmetros conta em `aos_model_route_params_rejected_total{rota,codigo}`.
//
// As séries existem só para as rotas cujo perfil declara parâmetros. Com a tabela de perfis de
// hoje não há nenhuma: a família não aparece no /metrics, que é o de antes.

import (
	"sync/atomic"

	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// contadoresDeParametros conta, por processo, os 4xx do provider em turnos com parâmetros, por
// rota e por código — os dois de vocabulário fechado.
type contadoresDeParametros struct {
	rotas   []string
	codigos []string
	total   map[string]map[string]*atomic.Int64
}

// novosContadoresDeParametros prepara uma série por rota com parâmetros e por código.
func novosContadoresDeParametros(rotas []string) *contadoresDeParametros {
	c := &contadoresDeParametros{rotas: rotas, codigos: modelgateway.RouteParamsRejectionStatuses(), total: map[string]map[string]*atomic.Int64{}}
	for _, r := range rotas {
		c.total[r] = map[string]*atomic.Int64{}
		for _, k := range c.codigos {
			c.total[r][k] = new(atomic.Int64)
		}
	}
	return c
}

// observar conta uma recusa. Uma rota ou um código fora dos conjuntos preparados não conta: o
// rótulo nunca leva texto que não seja de vocabulário fechado.
func (c *contadoresDeParametros) observar(r modelgateway.RouteParamsRejection) {
	if c == nil {
		return
	}
	if n := c.total[r.Route][r.Status]; n != nil {
		n.Add(1)
	}
}

// lido devolve o valor de uma série.
func (c *contadoresDeParametros) lido(rota, codigo string) int64 {
	if c == nil || c.total[rota] == nil || c.total[rota][codigo] == nil {
		return 0
	}
	return c.total[rota][codigo].Load()
}

// parametrosRecusados são os contadores do processo. O nó trabalha só com a tabela de perfis em
// código, pelo que as rotas com parâmetros se conhecem no arranque.
var parametrosRecusados = novosContadoresDeParametros((*modelgateway.RouteProfileSet)(nil).WithParams())

// contadoresDaDevolucao conta, por processo, os pedidos a rotas que devolvem o estado opaco do
// provider, pelo resultado (AOS-515) — a família `aos_model_provider_state_returned_total`.
type contadoresDaDevolucao struct {
	// activo diz que algum perfil da tabela devolve estado: só então a família existe.
	activo     bool
	resultados []string
	total      map[string]*atomic.Int64
}

func novosContadoresDaDevolucao(activo bool) *contadoresDaDevolucao {
	c := &contadoresDaDevolucao{activo: activo, resultados: modelgateway.StateReturnResults(), total: map[string]*atomic.Int64{}}
	for _, r := range c.resultados {
		c.total[r] = new(atomic.Int64)
	}
	return c
}

// observar conta um pedido. Um resultado fora do vocabulário não conta.
func (c *contadoresDaDevolucao) observar(o modelgateway.StateReturnObservation) {
	if c == nil {
		return
	}
	if n := c.total[o.Result]; n != nil {
		n.Add(1)
	}
}

func (c *contadoresDaDevolucao) lido(resultado string) int64 {
	if c == nil || c.total[resultado] == nil {
		return 0
	}
	return c.total[resultado].Load()
}

// algumPerfilDevolveEstado diz se a tabela de perfis em código tem alguma rota cuja classe de
// estado não seja `nunca`.
func algumPerfilDevolveEstado() bool {
	for _, p := range modelgateway.RouteProfiles() {
		if p.StateReturn != modelgateway.StateReturnNever {
			return true
		}
	}
	return false
}

// estadoDevolvido são os contadores do processo.
var estadoDevolvido = novosContadoresDaDevolucao(algumPerfilDevolveEstado())
