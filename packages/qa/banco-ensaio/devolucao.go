package bancoensaio

import (
	"net/http"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
)

// A DEVOLUÇÃO DO ESTADO OPACO NO BANCO (AOS-516).
//
// Um perfil candidato com `devolver` diferente de `nunca` faz o nó de ensaio compor, com as
// peças de produção, o que a devolução exige (ADR-040 §2.11): a captura do estado ligada, a
// governação da rota em `observe` — é ela que grava no envelope de que rota o estado é —, o
// layout 1.5.0 e a projecção que o perfil nomeia (a 1.3.0). Sem um perfil desses nada disto se
// liga, e o banco é o de antes: preso por teste, pelos digests dos pedidos e do relatório.
//
// DUAS COISAS QUE NÃO SE CONFUNDEM (revisão do AOS-516):
//
//   - A DECISÃO do gateway ([modelgateway.StateReturnObservation]) toma-se ANTES de o pedido
//     sair: «devolvido» aí quer dizer «o gateway armou o estado no pedido». Não diz que o pedido
//     saiu, nem que o fornecedor o aceitou.
//   - A ACEITAÇÃO pelo fornecedor: o pedido que levava o estado teve resposta 2xx. É só isto
//     que o banco conta como devolução conseguida, e é sobre isto que a taxa de P4 se calcula.
//
// E UMA TERCEIRA: um envelope de estado pode ter só os ids das tool calls, sem raciocínio nem
// assinatura nenhuma. Esse estado «captura-se» e «devolve-se» sem que haja nada a provar. O
// banco separa os turnos cuja resposta trouxe campos de raciocínio ou de assinatura — lido da
// sonda do gateway ([port.ProviderState]), pelos NOMES dos campos, nunca pelos valores — dos que
// só trouxeram ids, e mede P4 sobre os primeiros.
//
// O QUE O BANCO REGISTA é só contagem em vocabulário fechado. Nenhum byte do raciocínio, de uma
// assinatura ou do corpo de uma resposta chega aqui.

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

// As classes do estado que a resposta de um turno trouxe.
const (
	// estadoComRaciocinio — a resposta trouxe pelo menos um campo de raciocínio na mensagem, ou
	// um campo de assinatura numa tool call.
	estadoComRaciocinio = "com_raciocinio"
	// estadoSoComIDs — a resposta só trouxe o id que o provider deu às tool calls.
	estadoSoComIDs = "so_ids"
)

// classeDoEstadoDaResposta diz o que a sonda do gateway tirou da resposta, sem ler um valor: se
// há campos de raciocínio ou de assinatura, se só há ids, ou (vazio) se não há estado.
func classeDoEstadoDaResposta(st *port.ProviderState) string {
	if st == nil || st.Misaligned {
		return ""
	}
	if len(st.Fields) > 0 {
		return estadoComRaciocinio
	}
	for _, tc := range st.ToolCalls {
		if len(tc.Fields) > 0 {
			return estadoComRaciocinio
		}
	}
	if len(st.ToolCalls) > 0 {
		return estadoSoComIDs
	}
	return ""
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

// PedidosComEstado conta o que aconteceu, NO TRANSPORTE, aos pedidos em que o gateway armou
// estado (decisão `devolvido` ou `parcial`). Conta-se por TENTATIVA de transporte: se o gateway
// repetir um pedido, cada tentativa conta, e um erro intermédio não se perde.
type PedidosComEstado struct {
	// Pedidos é o número de pedidos em que o gateway armou estado.
	Pedidos int `json:"pedidos"`
	// HTTP2xx são as tentativas a que o fornecedor respondeu 2xx.
	HTTP2xx int `json:"http_2xx"`
	// HTTP4xx são as respostas 4xx, SEM o 429.
	HTTP4xx int `json:"http_4xx"`
	// HTTP429 são as respostas 429 (limite de taxa: não diz nada sobre o estado).
	HTTP429 int `json:"http_429"`
	// HTTP5xx são as respostas 5xx.
	HTTP5xx int `json:"http_5xx"`
	// HTTPOutro são as respostas com outro código (1xx, 3xx).
	HTTPOutro int `json:"http_outro"`
	// ErroDeTransporte são as tentativas sem resposta HTTP (ligação recusada ou fechada, tempo).
	ErroDeTransporte int `json:"erro_de_transporte"`
	// NaoEnviado são os pedidos que NÃO SAÍRAM depois de o gateway ter armado o estado: o tecto
	// do dia recusou a reserva, ou o gateway falhou antes do transporte.
	NaoEnviado int `json:"nao_enviado"`
}

func (p *PedidosComEstado) somar(o PedidosComEstado) {
	p.Pedidos += o.Pedidos
	p.HTTP2xx += o.HTTP2xx
	p.HTTP4xx += o.HTTP4xx
	p.HTTP429 += o.HTTP429
	p.HTTP5xx += o.HTTP5xx
	p.HTTPOutro += o.HTTPOutro
	p.ErroDeTransporte += o.ErroDeTransporte
	p.NaoEnviado += o.NaoEnviado
}

// EstadoDoRun é o que o banco viu do estado opaco num run. Só existe em corridas cujo perfil
// devolve estado.
type EstadoDoRun struct {
	// Capturas conta os turnos do run cuja resposta trouxe estado, pelo resultado da captura
	// ([modelgateway.ProviderStateResults]). Um turno sem estado na resposta não é contado.
	Capturas map[string]int `json:"turnos_com_estado_por_captura,omitempty"`
	// TurnosComRaciocinio são os turnos capturados cuja resposta trouxe campos de raciocínio ou
	// de assinatura; TurnosSoComIDs, os capturados que só trouxeram ids de tool call.
	TurnosComRaciocinio int `json:"turnos_com_raciocinio_capturado"`
	TurnosSoComIDs      int `json:"turnos_so_com_ids_capturados"`
	// Decisoes conta os pedidos do run que levavam pelo menos um turno anterior com tool calls,
	// pela DECISÃO do gateway ([modelgateway.StateReturnResults]), tomada antes do envio.
	Decisoes map[string]int `json:"pedidos_por_decisao_do_gateway,omitempty"`
	// Causas conta, por causa ([modelgateway.StateReturnCauses]), os pedidos em que o estado de
	// algum turno não foi armado (a causa do primeiro turno sem estado).
	Causas map[string]int `json:"nao_devolvido_por_causa,omitempty"`
	// DeviamLevarRaciocinio são os pedidos com turnos anteriores em que pelo menos um desses
	// turnos trouxe raciocínio: é o denominador de P4. Aceites são, desses, os que o gateway
	// armou com o estado de TODOS os turnos e a que o fornecedor respondeu 2xx.
	DeviamLevarRaciocinio int `json:"pedidos_que_deviam_levar_raciocinio"`
	Aceites               int `json:"aceites_pelo_fornecedor"`
	// ComEstado é o que aconteceu no transporte aos pedidos em que o gateway armou estado.
	ComEstado PedidosComEstado `json:"pedidos_com_estado"`
}

// capturasDoRun é o que a porta do nó de ensaio juntou das capturas de um run.
type capturasDoRun struct {
	porResultado            map[string]int
	comRaciocinio, soComIDs int
}

func novoEstadoDoRun(c capturasDoRun) *EstadoDoRun {
	e := &EstadoDoRun{TurnosComRaciocinio: c.comRaciocinio, TurnosSoComIDs: c.soComIDs}
	if len(c.porResultado) > 0 {
		e.Capturas = c.porResultado
	}
	return e
}

// contarPedido junta ao run o que aconteceu a um pedido. `haviaRaciocinio` diz se algum turno
// ANTERIOR do run trouxe raciocínio (é o que faz do pedido um dos que o deviam levar).
func (e *EstadoDoRun) contarPedido(c chamadaObservada, haviaRaciocinio bool) {
	if c.devolucao == "" {
		// O gateway não reportou nada: o pedido não levava turnos anteriores com tool calls.
		return
	}
	if e.Decisoes == nil {
		e.Decisoes = map[string]int{}
	}
	e.Decisoes[c.devolucao]++
	if c.causaDaDevolucao != "" {
		if e.Causas == nil {
			e.Causas = map[string]int{}
		}
		e.Causas[c.causaDaDevolucao]++
	}
	if haviaRaciocinio {
		e.DeviamLevarRaciocinio++
	}
	armado := c.devolucao == modelgateway.StateReturnAll || c.devolucao == modelgateway.StateReturnPartial
	if !armado {
		return
	}
	e.ComEstado.Pedidos++
	aceite := false
	for _, s := range c.tentativas {
		switch {
		case s == 0:
			e.ComEstado.ErroDeTransporte++
		case s >= 200 && s < 300:
			e.ComEstado.HTTP2xx++
			aceite = true
		case s == http.StatusTooManyRequests:
			e.ComEstado.HTTP429++
		case s >= 400 && s < 500:
			e.ComEstado.HTTP4xx++
		case s >= 500 && s < 600:
			e.ComEstado.HTTP5xx++
		default:
			e.ComEstado.HTTPOutro++
		}
	}
	e.ComEstado.NaoEnviado += c.naoEnviados
	if len(c.tentativas) == 0 && c.naoEnviados == 0 {
		// O gateway armou o estado e o pedido nem chegou ao transporte.
		e.ComEstado.NaoEnviado++
	}
	// SÓ CONTA COMO DEVOLVIDO O QUE O FORNECEDOR ACEITOU: a decisão `devolvido` (todos os turnos
	// armados) e uma resposta 2xx. A decisão `parcial` levou estado, mas não o de todos.
	if haviaRaciocinio && c.devolucao == modelgateway.StateReturnAll && aceite {
		e.Aceites++
	}
}

// Devolucao são as contagens da devolução do estado opaco sobre um conjunto de observações — a
// corrida, um braço ou um caso. Só existe quando alguma observação tem [Observacao.Estado].
type Devolucao struct {
	// TurnosComEstadoCapturado é o número de turnos cuja resposta trouxe estado — QUALQUER
	// estado, incluindo o que só tem ids — e em que a captura o guardou.
	TurnosComEstadoCapturado int            `json:"turnos_com_estado_capturado"`
	CapturasPorResultado     map[string]int `json:"turnos_com_estado_por_captura"`
	// TurnosComRaciocinioCapturado são, desses, os que trouxeram campos de raciocínio ou de
	// assinatura: é sobre eles que há alguma coisa a devolver. TurnosSoComIDs são os restantes.
	TurnosComRaciocinioCapturado int `json:"turnos_com_raciocinio_capturado"`
	TurnosSoComIDs               int `json:"turnos_so_com_ids_capturados"`
	// PedidosComTurnosAnteriores são os pedidos que levavam pelo menos um turno anterior com
	// tool calls; PedidosPorDecisao reparte-os pela DECISÃO do gateway, antes do envio.
	PedidosComTurnosAnteriores int            `json:"pedidos_com_turnos_anteriores"`
	PedidosPorDecisao          map[string]int `json:"pedidos_por_decisao_do_gateway"`
	// DecididosADevolver são os pedidos em que o gateway armou o estado de TODOS os turnos. É
	// uma decisão, não um resultado.
	DecididosADevolver int `json:"decididos_a_devolver"`
	// NaoDevolvidoPorCausa reparte pela causa os pedidos em que o estado de algum turno não foi.
	NaoDevolvidoPorCausa map[string]int `json:"nao_devolvido_por_causa"`
	// Recusas são os pedidos que o gateway NÃO enviou por faltar o estado numa rota
	// `obrigatorio` ([modelgateway.StateReturnError]).
	Recusas int `json:"recusas_por_falta_de_estado"`
	// ComEstado é o que aconteceu no transporte aos pedidos em que o gateway armou estado.
	ComEstado PedidosComEstado `json:"pedidos_com_estado"`
	// PedidosQueDeviamLevarRaciocinio é o denominador de P4; AceitesPeloFornecedor, o numerador.
	PedidosQueDeviamLevarRaciocinio int `json:"pedidos_que_deviam_levar_raciocinio"`
	AceitesPeloFornecedor           int `json:"aceites_pelo_fornecedor"`
	// TaxaDeDevolucao é AceitesPeloFornecedor sobre PedidosQueDeviamLevarRaciocinio.
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
			d = &Devolucao{CapturasPorResultado: map[string]int{}, PedidosPorDecisao: map[string]int{}, NaoDevolvidoPorCausa: map[string]int{}}
		}
		for r, n := range o.Estado.Capturas {
			d.CapturasPorResultado[doVocabulario(r, modelgateway.ProviderStateResults())] += n
		}
		for r, n := range o.Estado.Decisoes {
			d.PedidosPorDecisao[doVocabulario(r, modelgateway.StateReturnResults())] += n
			d.PedidosComTurnosAnteriores += n
		}
		for c, n := range o.Estado.Causas {
			d.NaoDevolvidoPorCausa[doVocabulario(c, modelgateway.StateReturnCauses())] += n
		}
		d.TurnosComRaciocinioCapturado += o.Estado.TurnosComRaciocinio
		d.TurnosSoComIDs += o.Estado.TurnosSoComIDs
		d.PedidosQueDeviamLevarRaciocinio += o.Estado.DeviamLevarRaciocinio
		d.AceitesPeloFornecedor += o.Estado.Aceites
		d.ComEstado.somar(o.Estado.ComEstado)
	}
	if d == nil {
		return nil
	}
	d.TurnosComEstadoCapturado = d.CapturasPorResultado[modelgateway.ProviderStateResultCaptured]
	d.DecididosADevolver = d.PedidosPorDecisao[modelgateway.StateReturnAll]
	d.Recusas = d.PedidosPorDecisao[modelgateway.StateReturnRefused]
	d.TaxaDeDevolucao = NovaTaxa("pedidos que deviam levar raciocinio (tinham pelo menos um turno anterior cuja resposta trouxe raciocinio ou assinatura) em que o gateway armou o estado de todos os turnos E o fornecedor respondeu 2xx (a medida do criterio P4; a decisao do gateway, sozinha, nao conta)", d.AceitesPeloFornecedor, d.PedidosQueDeviamLevarRaciocinio)
	return d
}

// Os veredictos da qualificação da devolução. Vocabulário FECHADO.
const (
	// QualificacaoCumprida — houve raciocínio para devolver, todos os pedidos que o deviam levar
	// foram aceites pelo fornecedor, e nada falhou pelo caminho.
	QualificacaoCumprida = "cumprida"
	// QualificacaoNaoCumprida — a devolução falhou: há uma razão que não é passageira.
	QualificacaoNaoCumprida = "nao_cumprida"
	// QualificacaoSemRaciocinio — nenhum turno da corrida trouxe raciocínio nem assinatura: não
	// havia nada a devolver, e a corrida não prova nada sobre a devolução.
	QualificacaoSemRaciocinio = "sem_raciocinio"
	// QualificacaoInconclusiva — a corrida não chega para decidir: só há razões passageiras
	// (limite de taxa, erro do servidor ou do transporte, tecto do dia, corrida parada a meio).
	QualificacaoInconclusiva = "inconclusiva"
)

// As razões de um veredicto que não é `cumprida`. Vocabulário FECHADO. As primeiras fazem
// `nao_cumprida`; as passageiras, sozinhas, fazem `inconclusiva`.
const (
	RazaoPerfilNaoDevolve       = "perfil_nao_devolve_estado"
	RazaoRecusas                = "recusas_por_falta_de_estado"
	RazaoHTTP4xxComEstado       = "http_4xx_em_pedidos_com_estado"
	RazaoRunsNaoCumpridos       = "runs_com_tools_nao_cumpridos"
	RazaoRunsParadosPor4xx      = "runs_com_tools_parados_por_4xx"
	RazaoPedidosNaoAceites      = "pedidos_que_deviam_levar_raciocinio_nao_aceites"
	RazaoSemTurnosComRaciocinio = "nenhum_turno_com_raciocinio"
	// As passageiras.
	RazaoHTTP429ComEstado    = "http_429_em_pedidos_com_estado"
	RazaoHTTP5xxComEstado    = "http_5xx_em_pedidos_com_estado"
	RazaoHTTPOutroComEstado  = "http_outro_em_pedidos_com_estado"
	RazaoTransporteComEstado = "erro_de_transporte_em_pedidos_com_estado"
	RazaoNaoEnviadoComEstado = "pedidos_com_estado_nao_enviados"
	RazaoRunsInterrompidos   = "runs_com_tools_interrompidos_por_erro_passageiro"
	RazaoCorridaIncompleta   = "corrida_incompleta"
	RazaoSemPedidosSeguintes = "nenhum_pedido_que_devesse_levar_raciocinio"
)

// razoesPassageiras são as que, sozinhas, não condenam a devolução.
var razoesPassageiras = map[string]bool{
	RazaoHTTP429ComEstado: true, RazaoHTTP5xxComEstado: true, RazaoHTTPOutroComEstado: true, RazaoTransporteComEstado: true,
	RazaoNaoEnviadoComEstado: true, RazaoRunsInterrompidos: true, RazaoCorridaIncompleta: true, RazaoSemPedidosSeguintes: true,
	// Um pedido não aceite tem sempre outra razão ao lado que diz porquê; sozinha não condena.
	RazaoPedidosNaoAceites: true,
}

// QualificacaoDaDevolucao é o VEREDICTO do banco sobre a devolução do estado opaco numa corrida
// (a leitura do critério P4). É calculado, não interpretado: quem lê o relatório lê este campo.
type QualificacaoDaDevolucao struct {
	// Veredicto é um de `cumprida`, `nao_cumprida`, `sem_raciocinio`, `inconclusiva`.
	Veredicto string `json:"veredicto"`
	// Razoes são as razões de o veredicto não ser `cumprida`, numa ordem fixa. Vazio em
	// `cumprida`.
	Razoes []string `json:"razoes"`
	// Regra é a regra aplicada, em texto fixo.
	Regra string `json:"regra"`
}

const regraDaQualificacao = "cumprida so com pelo menos um turno com raciocinio capturado, todos os pedidos que o deviam levar aceites pelo fornecedor (2xx), zero recusas, zero respostas 4xx, 429 ou 5xx, zero erros de transporte e zero pedidos nao enviados entre os pedidos com estado, e a ultima tentativa de todos os nos que exigem uma tool fechada cumprido; razoes passageiras sozinhas dao inconclusiva; sem nenhum turno com raciocinio, sem_raciocinio"

// qualificarDevolucao calcula o veredicto. `devolve` diz se o perfil da corrida devolve estado;
// `completa`, se a corrida correu o plano até ao fim.
func qualificarDevolucao(devolve, completa bool, d *Devolucao, obs []Observacao) *QualificacaoDaDevolucao {
	q := &QualificacaoDaDevolucao{Razoes: []string{}, Regra: regraDaQualificacao}
	juntar := func(se bool, razao string) {
		if se {
			q.Razoes = append(q.Razoes, razao)
		}
	}
	juntar(!devolve, RazaoPerfilNaoDevolve)
	if d == nil {
		d = &Devolucao{}
	}
	juntar(d.Recusas > 0, RazaoRecusas)
	juntar(d.ComEstado.HTTP4xx > 0, RazaoHTTP4xxComEstado)
	// A ÚLTIMA tentativa de cada nó que exige uma tool: tem de ter fechado `cumprido`.
	type unidade struct {
		caso, no, braco string
		amostra         int
	}
	ultima := map[unidade]Observacao{}
	var ordem []unidade
	for _, o := range obs {
		if !o.ExigeTool {
			continue
		}
		u := unidade{o.Caso, o.No, o.Braco, o.Amostra}
		if anterior, visto := ultima[u]; !visto {
			ordem = append(ordem, u)
		} else if anterior.Tentativa > o.Tentativa {
			continue
		}
		ultima[u] = o
	}
	naoCumpridos, por4xx, interrompidos := false, false, false
	for _, u := range ordem {
		o := ultima[u]
		switch o.Desfecho {
		case DesfechoCumprido:
		case DesfechoErroHTTP, DesfechoTempoEsgotado, DesfechoTectoAtingido, DesfechoContador:
			final := 0
			if len(o.HTTP) > 0 {
				final = o.HTTP[len(o.HTTP)-1]
			}
			if final >= 400 && final < 500 && final != http.StatusTooManyRequests {
				por4xx = true
			} else {
				interrompidos = true
			}
		default:
			naoCumpridos = true
		}
	}
	juntar(naoCumpridos, RazaoRunsNaoCumpridos)
	juntar(por4xx, RazaoRunsParadosPor4xx)
	juntar(d.TurnosComRaciocinioCapturado == 0, RazaoSemTurnosComRaciocinio)
	juntar(d.AceitesPeloFornecedor < d.PedidosQueDeviamLevarRaciocinio, RazaoPedidosNaoAceites)
	juntar(d.ComEstado.HTTP429 > 0, RazaoHTTP429ComEstado)
	juntar(d.ComEstado.HTTP5xx > 0, RazaoHTTP5xxComEstado)
	juntar(d.ComEstado.HTTPOutro > 0, RazaoHTTPOutroComEstado)
	juntar(d.ComEstado.ErroDeTransporte > 0, RazaoTransporteComEstado)
	juntar(d.ComEstado.NaoEnviado > 0, RazaoNaoEnviadoComEstado)
	juntar(interrompidos, RazaoRunsInterrompidos)
	juntar(!completa, RazaoCorridaIncompleta)
	juntar(d.TurnosComRaciocinioCapturado > 0 && d.PedidosQueDeviamLevarRaciocinio == 0, RazaoSemPedidosSeguintes)

	firme := false
	for _, r := range q.Razoes {
		if !razoesPassageiras[r] && r != RazaoSemTurnosComRaciocinio {
			firme = true
		}
	}
	switch {
	case firme:
		q.Veredicto = QualificacaoNaoCumprida
	case d.TurnosComRaciocinioCapturado == 0:
		q.Veredicto = QualificacaoSemRaciocinio
	case len(q.Razoes) > 0:
		q.Veredicto = QualificacaoInconclusiva
	default:
		q.Veredicto = QualificacaoCumprida
	}
	return q
}
