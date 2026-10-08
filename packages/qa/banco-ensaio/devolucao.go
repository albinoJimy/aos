package bancoensaio

import (
	"net/http"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// A DEVOLUÇÃO DO ESTADO OPACO NO BANCO (AOS-516).
//
// Um perfil candidato com `devolver` diferente de `nunca` faz o nó de ensaio compor, com as
// peças de produção, o que a devolução exige (ADR-040 §2.11): a captura do estado ligada, a
// governação da rota em `observe` — é ela que grava no envelope de que rota o estado é —, o
// layout 1.5.0 e a projecção que o perfil nomeia (a 1.3.0). Sem um perfil desses nada disto se
// liga, e o banco é o de antes: preso por teste, pelos digests dos pedidos e do relatório.
//
// O QUE O BANCO REGISTA é só contagem em vocabulário fechado: quantos turnos tiveram o estado
// capturado, o que o gateway fez em cada pedido que levava turnos anteriores com tool calls, a
// causa quando não devolveu, e os 4xx do provider nos pedidos que levaram estado. Nenhum byte do
// raciocínio, de uma assinatura ou do corpo de uma resposta chega aqui.

// DesfechoEstadoNaoDevolvido — o run parou porque a rota exige o estado de volta e o de um turno
// não se podia devolver: o gateway não enviou o pedido ([modelgateway.StateReturnError]).
const DesfechoEstadoNaoDevolvido = "estado_nao_devolvido"

// foraDoVocabulario é o rótulo de um valor que não é de nenhum dos vocabulários fechados: o
// texto recebido nunca chega a uma chave do relatório.
const foraDoVocabulario = "outro"

// doVocabulario devolve v se for um dos valores aceites, e [foraDoVocabulario] se não.
func doVocabulario(v string, aceites []string) string {
	for _, a := range aceites {
		if v == a {
			return v
		}
	}
	return foraDoVocabulario
}

// ComposicaoDoEstado é o que o nó de ensaio ligou por o perfil candidato devolver estado. Vai no
// relatório, em `protocolo.estado_opaco`.
type ComposicaoDoEstado struct {
	// Captura é o modo da captura do estado no gateway (`capture`).
	Captura string `json:"captura"`
	// GovernacaoDaRota é o modo da governação da rota (`observe`).
	GovernacaoDaRota string `json:"governacao_da_rota"`
	// EndpointComparado diz se o ensaio declarou o host esperado do endpoint. O host não vai.
	EndpointComparado bool `json:"endpoint_comparado"`
	// Layout é o layout de montagem do prompt dos runs (1.5.0).
	Layout string `json:"layout_do_prompt"`
	// Projeccao é a versão da projecção nativa que o perfil nomeia.
	Projeccao string `json:"projeccao_do_perfil"`
	// Devolver é a classe de estado do perfil; ToolCallID, o id de tool call que vai no wire.
	Devolver   string `json:"devolver"`
	ToolCallID string `json:"tool_call_id"`
}

// composicaoDoEstado devolve a composição para o perfil dado, ou nil se ele não devolve estado.
func composicaoDoEstado(perfil *modelgateway.RouteProfile, hostEsperado string) *ComposicaoDoEstado {
	if perfil == nil || perfil.StateReturn == modelgateway.StateReturnNever {
		return nil
	}
	id := modelgateway.ToolCallIDRuntimeName
	if perfil.ToolCallID == modelgateway.ToolCallIDProvider {
		id = modelgateway.ToolCallIDProvider
	}
	return &ComposicaoDoEstado{
		Captura: modelgateway.ProviderStateCapture, GovernacaoDaRota: modelgateway.RouteGovernanceObserve,
		EndpointComparado: hostEsperado != "", Layout: agentruntime.AssemblyVersion150,
		Projeccao: perfil.ProjectionVersion, Devolver: perfil.StateReturn, ToolCallID: id,
	}
}

// EstadoDoRun é o que o banco viu do estado opaco num run. Só existe em corridas cujo perfil
// devolve estado.
type EstadoDoRun struct {
	// Capturas conta os turnos do run cuja resposta trouxe estado, pelo resultado da captura
	// ([modelgateway.ProviderStateResults]). Um turno sem estado na resposta não é contado.
	Capturas map[string]int `json:"turnos_com_estado_por_captura,omitempty"`
	// Devolucoes conta os pedidos do run que levavam pelo menos um turno anterior com tool
	// calls, pelo que o gateway fez ([modelgateway.StateReturnResults]): `devolvido` — todos os
	// turnos anteriores levaram o seu estado; `recusado` — o pedido NÃO foi enviado.
	Devolucoes map[string]int `json:"pedidos_por_devolucao,omitempty"`
	// Causas conta, por causa ([modelgateway.StateReturnCauses]), os pedidos em que o estado de
	// algum turno não foi devolvido (a causa do primeiro turno sem estado).
	Causas map[string]int `json:"nao_devolvido_por_causa,omitempty"`
	// HTTP4xxComEstado conta os pedidos que saíram com estado e a que o provider respondeu 4xx.
	HTTP4xxComEstado int `json:"http_4xx_em_pedidos_com_estado"`
}

func novoEstadoDoRun(capturas map[string]int) *EstadoDoRun {
	e := &EstadoDoRun{}
	if len(capturas) > 0 {
		e.Capturas = capturas
	}
	return e
}

// contarPedido junta ao run o que o gateway reportou da devolução num pedido.
func (e *EstadoDoRun) contarPedido(c chamadaObservada) {
	if c.devolucao == "" {
		return
	}
	if e.Devolucoes == nil {
		e.Devolucoes = map[string]int{}
	}
	e.Devolucoes[c.devolucao]++
	if c.causaDaDevolucao != "" {
		if e.Causas == nil {
			e.Causas = map[string]int{}
		}
		e.Causas[c.causaDaDevolucao]++
	}
	levouEstado := c.devolucao == modelgateway.StateReturnAll || c.devolucao == modelgateway.StateReturnPartial
	if levouEstado && c.status >= http.StatusBadRequest && c.status < http.StatusInternalServerError {
		e.HTTP4xxComEstado++
	}
}

// Devolucao são as contagens da devolução do estado opaco sobre um conjunto de observações — a
// corrida, um braço ou um caso. Só existe quando alguma observação tem [Observacao.Estado].
type Devolucao struct {
	// TurnosComEstadoCapturado é o número de turnos cuja resposta trouxe estado e em que a
	// captura o guardou (`capturado`). CapturasPorResultado tem todos os resultados.
	TurnosComEstadoCapturado int            `json:"turnos_com_estado_capturado"`
	CapturasPorResultado     map[string]int `json:"turnos_com_estado_por_captura"`
	// PedidosComTurnosAnteriores são os pedidos que levavam pelo menos um turno anterior com
	// tool calls: cada um é o «pedido seguinte» de um turno. PedidosPorDevolucao reparte-os pelo
	// que o gateway fez.
	PedidosComTurnosAnteriores int            `json:"pedidos_com_turnos_anteriores"`
	PedidosPorDevolucao        map[string]int `json:"pedidos_por_devolucao"`
	// Devolvidos são os que saíram com o estado de TODOS os turnos anteriores.
	Devolvidos int `json:"devolvidos"`
	// NaoDevolvidoPorCausa reparte pela causa os pedidos em que o estado de algum turno não foi.
	NaoDevolvidoPorCausa map[string]int `json:"nao_devolvido_por_causa"`
	// Recusas são os pedidos que o gateway NÃO enviou por faltar o estado numa rota
	// `obrigatorio` ([modelgateway.StateReturnError]).
	Recusas int `json:"recusas_por_falta_de_estado"`
	// HTTP4xxComEstado são os pedidos que saíram com estado e tiveram resposta 4xx.
	HTTP4xxComEstado int `json:"http_4xx_em_pedidos_com_estado"`
	// TaxaDeDevolucao é Devolvidos sobre PedidosComTurnosAnteriores (a medida do critério P4).
	TaxaDeDevolucao Taxa `json:"taxa_de_devolucao"`
}

// calcularDevolucao soma o estado dos runs; nil se nenhuma observação o tem.
func calcularDevolucao(obs []Observacao) *Devolucao {
	var d *Devolucao
	for _, o := range obs {
		if o.Estado == nil {
			continue
		}
		if d == nil {
			d = &Devolucao{CapturasPorResultado: map[string]int{}, PedidosPorDevolucao: map[string]int{}, NaoDevolvidoPorCausa: map[string]int{}}
		}
		for r, n := range o.Estado.Capturas {
			d.CapturasPorResultado[doVocabulario(r, modelgateway.ProviderStateResults())] += n
		}
		for r, n := range o.Estado.Devolucoes {
			d.PedidosPorDevolucao[doVocabulario(r, modelgateway.StateReturnResults())] += n
			d.PedidosComTurnosAnteriores += n
		}
		for c, n := range o.Estado.Causas {
			d.NaoDevolvidoPorCausa[doVocabulario(c, modelgateway.StateReturnCauses())] += n
		}
		d.HTTP4xxComEstado += o.Estado.HTTP4xxComEstado
	}
	if d == nil {
		return nil
	}
	d.TurnosComEstadoCapturado = d.CapturasPorResultado[modelgateway.ProviderStateResultCaptured]
	d.Devolvidos = d.PedidosPorDevolucao[modelgateway.StateReturnAll]
	d.Recusas = d.PedidosPorDevolucao[modelgateway.StateReturnRefused]
	d.TaxaDeDevolucao = NovaTaxa("pedidos com pelo menos um turno anterior com tool calls que sairam com o estado opaco de todos esses turnos (o contador do AOS-515; a medida do criterio P4)", d.Devolvidos, d.PedidosComTurnosAnteriores)
	return d
}
