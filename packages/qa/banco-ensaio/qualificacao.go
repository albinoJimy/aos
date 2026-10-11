package bancoensaio

import (
	"net/http"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/port"
)

// A QUALIFICAÇÃO DO MODELO (AOS-518).
//
// A experiência `qualificacao` corre a bateria oito vezes com um perfil candidato e dá um
// VEREDICTO sobre a rota: `qualificado`, `recusado` ou `inconclusivo`. O veredicto é CALCULADO —
// função pura das contagens que vão no relatório ([ContagensDaQualificacao]) —, com a regra em
// texto fixo ([RegraDoVeredictoDoModelo]) e as razões em vocabulário fechado, numa ordem fixa.
// Ninguém o declara, e nenhum modelo avalia outro modelo.
//
// A qualificação é da ROTA — modelo, fornecedor, imagem do proxy e perfil —, não do modelo: mudar
// um dos quatro é qualificar outra vez.
//
// O QUE A A2 ENSINOU E ESTA REGRA RESPEITA (desenho A3 §5): conta-se o que o fornecedor ACEITOU e
// o kernel fechou `cumprido`, nunca uma decisão tomada antes de o pedido sair; um erro passageiro
// (429, 5xx, transporte, tecto) nunca conta como «não cumprido» nem como «cumprido» — tira o nó da
// amostra, e uma amostra incompleta não qualifica; e o intervalo de confiança vai ao lado de cada
// contagem e NÃO entra na regra.

// Os limiares da decisão D2 do dono (2026-10-11). São um só conjunto para todos os modelos.
const (
	// PassagensDaQualificacao é o número de passagens pela bateria de uma qualificação.
	PassagensDaQualificacao = 8
	// NosComToolsDaQualificacao é a amostra exigida: os cinco nós com tools da bateria, oito vezes.
	NosComToolsDaQualificacao = 40
	// LimiarL1NaoCumpridos — L1: nós com tools NÃO cumpridos no fim das tentativas (todos cumpridos).
	LimiarL1NaoCumpridos = 0
	// LimiarL2SemToolCall — L2: nós com tools sem tool call à primeira tentativa (no máximo 4 de 40).
	LimiarL2SemToolCall = 4
	// LimiarL3QuatroXX — L3: respostas 4xx do provider nos segundos turnos.
	LimiarL3QuatroXX = 0
	// LimiarL4Vazias — L4: respostas vazias no fim das tentativas.
	LimiarL4Vazias = 0
	// LimiarL5ModeloTrocado — L5: turnos com o modelo servido diferente do esperado.
	LimiarL5ModeloTrocado = 0
	// NosSemToolsDaQualificacao é a amostra exigida dos nós SEM tools: os dois da bateria, oito vezes.
	NosSemToolsDaQualificacao = 16
	// LimiarL6SemToolsNaoCumpridos — L6: nós sem tools NÃO cumpridos no fim das tentativas. Não
	// estava na D2 original: entrou por decisão do dono de 2026-10-11, depois de a revisão
	// adversarial mostrar que um provider que falha só nos pedidos sem tools saía `qualificado`.
	LimiarL6SemToolsNaoCumpridos = 0
)

// Os veredictos sobre o modelo. Vocabulário FECHADO.
const (
	// VeredictoQualificado — a corrida completou, a amostra está inteira e nenhum limiar foi violado.
	VeredictoQualificado = "qualificado"
	// VeredictoRecusado — há pelo menos uma razão FIRME: repetir a corrida não a tira.
	VeredictoRecusado = "recusado"
	// VeredictoInconclusivo — só há razões PASSAGEIRAS: a corrida não chega para decidir.
	VeredictoInconclusivo = "inconclusivo"
)

// As razões de um veredicto que não é `qualificado`. Vocabulário FECHADO, pela ordem em que saem.
const (
	// As FIRMES: uma só dá `recusado`.
	RazaoModeloNaoCumpridos     = "nos_com_tools_nao_cumpridos"              // L1
	RazaoModeloSemToolCall      = "sem_tool_call_a_primeira_acima_do_limiar" // L2
	RazaoModelo4xxNoSegundo     = "http_4xx_nos_segundos_turnos"             // L3
	RazaoModeloRespostasVazias  = "respostas_vazias_no_fim_das_tentativas"   // L4
	RazaoModeloServidoDiferente = "modelo_servido_diferente_do_esperado"     // L5
	// RazaoModeloServidoNaoReportado — L5, pelo lado fechado: um turno em que o proxy não declarou
	// o modelo servido não se prova igual ao esperado.
	RazaoModeloServidoNaoReportado = "modelo_servido_nao_reportado"
	// RazaoSemToolsNaoCumpridos — L6: um nó sem tools falhou por uma causa que não é passageira.
	RazaoSemToolsNaoCumpridos = "nos_sem_tools_nao_cumpridos"
	// RazaoDevolucaoNaoCumprida — o perfil declara devolução do estado e o veredicto da devolução
	// (`qualificacao_da_devolucao`) é `nao_cumprida`.
	RazaoDevolucaoNaoCumprida = "devolucao_do_estado_nao_cumprida"
	// RazaoPerfilRecusado — o provider respondeu 4xx de parâmetro a TODOS os pedidos da corrida.
	RazaoPerfilRecusado = "perfil_recusado_pelo_provider"

	// As PASSAGEIRAS: sozinhas dão `inconclusivo`.
	RazaoPiorCasoNaoCabe         = "pior_caso_nao_cabe_no_tecto"
	RazaoTectoDoDia              = "tecto_do_dia_atingido"
	RazaoTectoDaQualificacao     = "tecto_da_qualificacao_atingido"
	RazaoChaveRecusada           = "chave_recusada"
	RazaoSaldoInsuficiente       = "saldo_insuficiente"
	RazaoLimiteDeRitmo           = "limite_de_ritmo_ou_serie_de_429"
	RazaoSondaFalhou             = "sonda_falhou"
	RazaoCorridaInterrompida     = "corrida_interrompida"
	RazaoAmostraIncompleta       = "amostra_incompleta"
	RazaoInterrompidos429        = "nos_interrompidos_por_429"
	RazaoInterrompidos5xx        = "nos_interrompidos_por_5xx"
	RazaoInterrompidosTransporte = "nos_interrompidos_por_erro_de_transporte"
	RazaoInterrompidosConta      = "nos_interrompidos_por_conta_ou_rota"
	RazaoInterrompidosTecto      = "nos_interrompidos_pelo_tecto"
	// RazaoDevolucaoNaoMedida — o perfil declara devolução do estado e a corrida não a mediu: o
	// veredicto da devolução é `sem_raciocinio` ou `inconclusiva`. O perfil promete uma coisa que
	// esta corrida não viu.
	RazaoDevolucaoNaoMedida = "devolucao_do_estado_nao_medida"
)

// RazoesFirmesDoModelo devolve as razões firmes, pela ordem em que saem.
func RazoesFirmesDoModelo() []string {
	return []string{
		RazaoModeloNaoCumpridos, RazaoModeloSemToolCall, RazaoModelo4xxNoSegundo, RazaoModeloRespostasVazias,
		RazaoModeloServidoDiferente, RazaoModeloServidoNaoReportado, RazaoSemToolsNaoCumpridos, RazaoDevolucaoNaoCumprida, RazaoPerfilRecusado,
	}
}

// RazoesPassageirasDoModelo devolve as razões passageiras, pela ordem em que saem.
func RazoesPassageirasDoModelo() []string {
	return []string{
		RazaoPiorCasoNaoCabe, RazaoTectoDoDia, RazaoTectoDaQualificacao, RazaoChaveRecusada, RazaoSaldoInsuficiente,
		RazaoLimiteDeRitmo, RazaoSondaFalhou, RazaoCorridaInterrompida, RazaoAmostraIncompleta,
		RazaoInterrompidos429, RazaoInterrompidos5xx, RazaoInterrompidosTransporte, RazaoInterrompidosConta, RazaoInterrompidosTecto, RazaoDevolucaoNaoMedida,
	}
}

// Os estados finais que só a qualificação tem (os outros estão em corrida.go).
const (
	// TerminouNaoCoube — o pior caso da corrida não cabia no que restava do tecto do dia: nenhum
	// pedido saiu.
	TerminouNaoCoube = "nao_coube_no_tecto"
	// TerminouTectoDaQualificacao — a corrida bateu no tecto PRÓPRIO da qualificação (pedidos ou
	// dólares), e não no do dia.
	TerminouTectoDaQualificacao = "tecto_da_qualificacao"
	// TerminouPerfilRecusado — os primeiros pedidos da corrida tiveram todos 4xx de parâmetro: o
	// provider não aceita o perfil, e continuar só gastava o tecto.
	TerminouPerfilRecusado = "perfil_recusado"
)

// As causas de um nó com tools ter ficado FORA da amostra. Vocabulário fechado.
const (
	InterrompidoPor429        = "http_429"
	InterrompidoPor5xx        = "http_5xx"
	InterrompidoPorTransporte = "erro_de_transporte"
	InterrompidoPorConta      = "conta_ou_rota"
	InterrompidoPeloTecto     = "tecto"
	// InterrompidoPeloCancelamento — o run foi apanhado pelo cancelamento da corrida
	// ([MedidasDoRun.CorridaCancelada]).
	InterrompidoPeloCancelamento = "cancelamento_da_corrida"
)

// RegraDoVeredictoDoModelo é a regra aplicada, em texto fixo. Vai no relatório ao lado do veredicto.
const RegraDoVeredictoDoModelo = "qualificado so com a corrida completa, os 40 nos com tools e os 16 nos sem tools na amostra (um no entra na amostra quando a sua ultima tentativa fechou cumprido ou falhou por uma causa que nao e passageira) e nenhum limiar violado: L1 zero nos com tools por cumprir no fim das tentativas; L2 no maximo 4 nos com tools cuja primeira tentativa fechou sem nenhuma tool call nativa, seja qual for o motivo de paragem; L3 zero respostas 4xx de parametro a pedidos que levavam uma mensagem tool; L4 zero nos com resposta vazia no fim das tentativas; L5 zero turnos com o modelo servido diferente do esperado, ou nao reportado; L6 zero nos sem tools por cumprir no fim das tentativas; o provider nao ter respondido 4xx de parametro a todos os pedidos; e, num perfil que declara devolucao do estado, o veredicto da devolucao ser cumprida (nao_cumprida e razao firme; sem_raciocinio e inconclusiva sao passageiras). Uma razao firme da recusado, mesmo com razoes passageiras ao lado, porque as contagens so crescem: um limiar violado numa amostra parcial continua violado na amostra inteira. So razoes passageiras (tecto, conta, limite de ritmo, 5xx, transporte, corrida parada, amostra incompleta) dao inconclusivo: um no interrompido por uma causa passageira, com ou sem tools, sai da amostra. Os limiares aplicam-se as contagens; o intervalo de confianca vai ao lado e nao entra na regra."

// MedidasDoRun é o que a qualificação precisa de um run e que as outras experiências não registam.
// Só existe em observações de uma corrida de qualificação. Só contagens.
type MedidasDoRun struct {
	// PedidosComMensagemTool são os pedidos do run que levavam pelo menos uma mensagem `tool` (os
	// «segundos turnos»: todos os que vêm depois de uma tool call).
	PedidosComMensagemTool int `json:"pedidos_com_mensagem_tool"`
	// QuatroXXComMensagemTool são as respostas 4xx DE PARÂMETRO a esses pedidos ([quatroXXDeParametro]).
	QuatroXXComMensagemTool int `json:"http_4xx_de_parametro_com_mensagem_tool"`
	// Os turnos do run, pelo resultado da comparação do modelo servido com o esperado.
	TurnosComModeloIgual     int `json:"turnos_com_modelo_igual"`
	TurnosComModeloDiferente int `json:"turnos_com_modelo_diferente"`
	TurnosSemModeloReportado int `json:"turnos_sem_modelo_reportado"`
	// CorridaCancelada diz que o run NÃO fechou cumprido e que, quando acabou, o contexto da
	// corrida já estava cancelado (um Ctrl-C, o fecho da consola). O que esse run deixou não diz
	// nada sobre o modelo: o cancelamento pode ter apanhado o run DEPOIS de um pedido com 200 — o
	// runtime devolve então um erro sem classe, e o run parecia uma falha do modelo. O nó fica
	// interrompido por causa passageira e o run não alimenta contagem nenhuma de limiar.
	CorridaCancelada bool `json:"corrida_cancelada,omitempty"`
}

// cancelada diz se a observação é de um run apanhado pelo cancelamento da corrida.
func (o Observacao) cancelada() bool {
	return o.Qualificacao != nil && o.Qualificacao.CorridaCancelada
}

// quatroXXDeParametro diz se uma resposta é um 4xx que fala do PEDIDO — e não da conta, da chave,
// do ritmo ou da existência do modelo, que são passageiros ou do ficheiro de chaves. O tipo é o do
// vocabulário fechado dos erros ([TiposDeErro]); o 408 é um tempo esgotado.
func quatroXXDeParametro(status int, tipo string) bool {
	return status >= 400 && status < 500 && status != http.StatusTooManyRequests &&
		status != http.StatusRequestTimeout && tipo == TipoOutro
}

// tiposPorPedido alinha os tipos de erro de uma observação com os seus códigos HTTP: um tipo por
// pedido, vazio nos que tiveram 200 ou não tiveram resposta.
func tiposPorPedido(o Observacao) []string {
	out := make([]string, len(o.HTTP))
	semDuzentos := 0
	for i, status := range o.HTTP {
		if status != 0 && status != http.StatusOK {
			if semDuzentos < len(o.TiposDeErro) {
				out[i] = tipoDoVocabulario(o.TiposDeErro[semDuzentos])
			}
			semDuzentos++
		}
	}
	return out
}

// As classes do estado final de um nó (a sua ÚLTIMA tentativa).
const (
	noCumprido     = "cumprido"
	noNaoCumprido  = "nao_cumprido"
	noInterrompido = "interrompido"
)

// estadoFinalDoNo classifica a última tentativa de um nó. Um erro PASSAGEIRO não diz nada sobre o
// modelo: o nó fica `interrompido`, com a causa, e sai da amostra. Tudo o resto que não seja
// `cumprido` é `nao_cumprido`.
func estadoFinalDoNo(o Observacao) (classe, causa string) {
	ultimo, tipo := -1, ""
	if n := len(o.HTTP); n > 0 {
		ultimo, tipo = o.HTTP[n-1], tiposPorPedido(o)[n-1]
	}
	if o.Desfecho == DesfechoCumprido {
		return noCumprido, ""
	}
	if o.cancelada() {
		// ANTES de olhar para o desfecho: seja ele qual for, veio depois do cancelamento.
		return noInterrompido, InterrompidoPeloCancelamento
	}
	switch o.Desfecho {
	case DesfechoTectoAtingido, DesfechoContador:
		return noInterrompido, InterrompidoPeloTecto
	case DesfechoTempoEsgotado:
		return noInterrompido, InterrompidoPorTransporte
	case DesfechoErroHTTP:
		switch {
		case ultimo == http.StatusTooManyRequests:
			return noInterrompido, InterrompidoPor429
		case ultimo >= 500 && ultimo < 600:
			return noInterrompido, InterrompidoPor5xx
		case quatroXXDeParametro(ultimo, tipo):
			return noNaoCumprido, ""
		case ultimo >= 400 && ultimo < 500:
			// 401, 403, 402, 404 de modelo, 408: a chave, a conta ou a rota — não o modelo.
			return noInterrompido, InterrompidoPorConta
		}
		return noInterrompido, InterrompidoPorTransporte
	case DesfechoErroOutro:
		if ultimo <= 0 {
			// Sem resposta HTTP no último pedido (ligação recusada ou fechada), ou sem pedido nenhum.
			return noInterrompido, InterrompidoPorTransporte
		}
		return noNaoCumprido, ""
	}
	return noNaoCumprido, ""
}

// ContagensDaQualificacao são as contagens de que o veredicto é função. Vão inteiras no relatório:
// com elas e com a regra, qualquer pessoa refaz o veredicto.
type ContagensDaQualificacao struct {
	// Terminou é o estado final da corrida ([Relatorio.Terminou]).
	Terminou string `json:"terminou"`
	// Devolucao é o veredicto da devolução do estado ([QualificacaoDaDevolucao.Veredicto]) quando
	// o perfil a declara; vazio quando o perfil não devolve estado.
	Devolucao string `json:"veredicto_da_devolucao_do_estado"`
	// NosComToolsExigidos é a amostra exigida (40).
	NosComToolsExigidos int `json:"nos_com_tools_exigidos"`
	// NosComToolsNaAmostra são os nós com tools cuja última tentativa fechou `cumprido` ou falhou
	// por uma causa que não é passageira; NosComToolsCumpridos, os primeiros.
	NosComToolsNaAmostra int `json:"nos_com_tools_na_amostra"`
	NosComToolsCumpridos int `json:"nos_com_tools_cumpridos"`
	// NaoCumpridosPorDesfecho reparte os nós com tools por cumprir pelo desfecho da última tentativa.
	NaoCumpridosPorDesfecho map[string]int `json:"nos_com_tools_nao_cumpridos_por_desfecho"`
	// InterrompidosPorCausa reparte os nós com tools que ficaram FORA da amostra pela causa.
	InterrompidosPorCausa map[string]int `json:"nos_com_tools_interrompidos_por_causa"`
	// Os nós SEM tools, na mesma forma: a amostra exigida (16), os que entraram na amostra, os
	// cumpridos, os por cumprir pelo desfecho e os interrompidos pela causa.
	NosSemToolsExigidos                int            `json:"nos_sem_tools_exigidos"`
	NosSemToolsNaAmostra               int            `json:"nos_sem_tools_na_amostra"`
	NosSemToolsCumpridos               int            `json:"nos_sem_tools_cumpridos"`
	NosSemToolsNaoCumpridosPorDesfecho map[string]int `json:"nos_sem_tools_nao_cumpridos_por_desfecho"`
	NosSemToolsInterrompidosPorCausa   map[string]int `json:"nos_sem_tools_interrompidos_por_causa"`
	// PrimeirasTentativasComTools são as primeiras tentativas de nós com tools que deram pelo
	// menos um turno; SemToolCallAPrimeira, as que fecharam com ZERO tool calls nativas — seja
	// qual for o motivo de paragem do último turno (`stop`, `end_turn`, `content_filter`, nenhum).
	PrimeirasTentativasComTools int `json:"primeiras_tentativas_de_nos_com_tools"`
	SemToolCallAPrimeira        int `json:"sem_tool_call_a_primeira_tentativa"`
	// PedidosComMensagemTool e QuatroXXComMensagemTool: ver [MedidasDoRun].
	PedidosComMensagemTool  int `json:"pedidos_com_mensagem_tool"`
	QuatroXXComMensagemTool int `json:"http_4xx_de_parametro_com_mensagem_tool"`
	// NosObservados são todos os nós (com e sem tools) com pelo menos uma tentativa;
	// RespostasVaziasNoFim, os que fecharam `empty_output` na última.
	NosObservados        int `json:"nos_observados"`
	RespostasVaziasNoFim int `json:"respostas_vazias_no_fim_das_tentativas"`
	// Turnos são os pedidos que deram um turno, repartidos pela comparação do modelo servido.
	Turnos                   int `json:"turnos"`
	TurnosComModeloIgual     int `json:"turnos_com_modelo_igual"`
	TurnosComModeloDiferente int `json:"turnos_com_modelo_diferente"`
	TurnosSemModeloReportado int `json:"turnos_sem_modelo_reportado"`
	// PedidosComResposta são os pedidos da corrida com resposta HTTP (a sonda não conta);
	// PedidosCom2xx e Pedidos4xxDeParametro repartem-nos.
	PedidosComResposta    int `json:"pedidos_com_resposta"`
	PedidosCom2xx         int `json:"pedidos_com_2xx"`
	Pedidos4xxDeParametro int `json:"pedidos_com_4xx_de_parametro"`
}

// contarParaQualificacao tira das observações as contagens da qualificação. `exigidos` é a amostra
// exigida; `terminou`, o estado final da corrida. É determinista.
func contarParaQualificacao(terminou string, exigidos, semToolsExigidos int, obs []Observacao) ContagensDaQualificacao {
	c := ContagensDaQualificacao{
		Terminou: terminou, NosComToolsExigidos: exigidos, NosSemToolsExigidos: semToolsExigidos,
		NaoCumpridosPorDesfecho: map[string]int{}, InterrompidosPorCausa: map[string]int{},
		NosSemToolsNaoCumpridosPorDesfecho: map[string]int{}, NosSemToolsInterrompidosPorCausa: map[string]int{},
	}
	type unidade struct {
		caso, no, braco string
		amostra         int
	}
	ultima := map[unidade]Observacao{}
	var ordem []unidade
	for _, o := range obs {
		u := unidade{o.Caso, o.No, o.Braco, o.Amostra}
		if anterior, visto := ultima[u]; !visto {
			ordem = append(ordem, u)
			ultima[u] = o
		} else if o.Tentativa >= anterior.Tentativa {
			ultima[u] = o
		}
		if o.cancelada() {
			// DEFESA EM PROFUNDIDADE: um run apanhado pelo cancelamento não alimenta L2, L3, L5 nem
			// a recusa do perfil. Numa corrida interrompida, uma razão firme só vem de runs que
			// fecharam ANTES da interrupção.
			continue
		}
		if o.ExigeTool && o.Tentativa == 1 && o.Turnos >= 1 {
			c.PrimeirasTentativasComTools++
			// NÃO se usa [Observacao.SemToolCall], que exige o motivo de paragem `stop` (é a medida
			// da tool call em texto): um provider que fecha sem tool call com `end_turn`,
			// `content_filter` ou sem motivo falhou a primeira tentativa do mesmo modo.
			if o.ToolCalls == 0 {
				c.SemToolCallAPrimeira++
			}
		}
		tipos := tiposPorPedido(o)
		for i, status := range o.HTTP {
			if status == 0 {
				continue
			}
			c.PedidosComResposta++
			switch {
			case status >= 200 && status < 300:
				c.PedidosCom2xx++
			case quatroXXDeParametro(status, tipos[i]):
				c.Pedidos4xxDeParametro++
			}
		}
		c.Turnos += o.Turnos
		if m := o.Qualificacao; m != nil {
			c.PedidosComMensagemTool += m.PedidosComMensagemTool
			c.QuatroXXComMensagemTool += m.QuatroXXComMensagemTool
			c.TurnosComModeloIgual += m.TurnosComModeloIgual
			c.TurnosComModeloDiferente += m.TurnosComModeloDiferente
			c.TurnosSemModeloReportado += m.TurnosSemModeloReportado
		}
	}
	// Um turno sem comparação nenhuma (a governação da rota não o viu) não se prova igual: conta
	// como não reportado. É o lado fechado de L5.
	if semComparacao := c.Turnos - c.TurnosComModeloIgual - c.TurnosComModeloDiferente - c.TurnosSemModeloReportado; semComparacao > 0 {
		c.TurnosSemModeloReportado += semComparacao
	}
	for _, u := range ordem {
		o := ultima[u]
		c.NosObservados++
		if o.Desfecho == string(agentruntime.OutcomeEmptyOutput) && !o.cancelada() {
			c.RespostasVaziasNoFim++
		}
		if !o.ExigeTool {
			switch classe, causa := estadoFinalDoNo(o); classe {
			case noCumprido:
				c.NosSemToolsNaAmostra++
				c.NosSemToolsCumpridos++
			case noNaoCumprido:
				c.NosSemToolsNaAmostra++
				c.NosSemToolsNaoCumpridosPorDesfecho[desfechoDoVocabulario(o.Desfecho)]++
			default:
				c.NosSemToolsInterrompidosPorCausa[causa]++
			}
			continue
		}
		switch classe, causa := estadoFinalDoNo(o); classe {
		case noCumprido:
			c.NosComToolsNaAmostra++
			c.NosComToolsCumpridos++
		case noNaoCumprido:
			c.NosComToolsNaAmostra++
			c.NaoCumpridosPorDesfecho[desfechoDoVocabulario(o.Desfecho)]++
		default:
			c.InterrompidosPorCausa[causa]++
		}
	}
	return c
}

// desfechoDoVocabulario deixa passar um desfecho só se for do banco ou do kernel.
func desfechoDoVocabulario(d string) string {
	switch d {
	case DesfechoCumprido, DesfechoTurnosEsgotados, DesfechoErroHTTP, DesfechoRespostaRecusada, DesfechoTempoEsgotado,
		DesfechoErroOutro, DesfechoTectoAtingido, DesfechoContador, DesfechoEstadoNaoDevolvido:
		return d
	}
	if agentruntime.OutcomeReason(d).NoVocabulario() {
		return d
	}
	return foraDoVocabulario
}

// Os predicados dos limiares. Cada um é a ÚNICA escrita do seu limiar: servem a regra e a tabela
// de limiares do relatório.

func (c ContagensDaQualificacao) naoCumpridos() int {
	return c.NosComToolsNaAmostra - c.NosComToolsCumpridos
}
func (c ContagensDaQualificacao) violaL1() bool { return c.naoCumpridos() > LimiarL1NaoCumpridos }
func (c ContagensDaQualificacao) violaL2() bool { return c.SemToolCallAPrimeira > LimiarL2SemToolCall }
func (c ContagensDaQualificacao) violaL3() bool { return c.QuatroXXComMensagemTool > LimiarL3QuatroXX }
func (c ContagensDaQualificacao) violaL4() bool { return c.RespostasVaziasNoFim > LimiarL4Vazias }
func (c ContagensDaQualificacao) modeloTrocado() int {
	return c.TurnosComModeloDiferente + c.TurnosSemModeloReportado
}
func (c ContagensDaQualificacao) violaL5() bool { return c.modeloTrocado() > LimiarL5ModeloTrocado }

// perfilRecusado: houve respostas, nenhuma foi 2xx, e todas foram 4xx de parâmetro.
func (c ContagensDaQualificacao) perfilRecusado() bool {
	return c.PedidosComResposta > 0 && c.PedidosCom2xx == 0 && c.Pedidos4xxDeParametro == c.PedidosComResposta
}
func (c ContagensDaQualificacao) semToolsNaoCumpridos() int {
	return c.NosSemToolsNaAmostra - c.NosSemToolsCumpridos
}
func (c ContagensDaQualificacao) violaL6() bool {
	return c.semToolsNaoCumpridos() > LimiarL6SemToolsNaoCumpridos
}
func (c ContagensDaQualificacao) amostraComToolsIncompleta() bool {
	return c.NosComToolsNaAmostra < c.NosComToolsExigidos
}
func (c ContagensDaQualificacao) amostraIncompleta() bool {
	return c.amostraComToolsIncompleta() || c.NosSemToolsNaAmostra < c.NosSemToolsExigidos
}

// interrompidos devolve quantos nós, com e sem tools, ficaram fora da amostra por uma causa.
func (c ContagensDaQualificacao) interrompidos(causa string) int {
	return c.InterrompidosPorCausa[causa] + c.NosSemToolsInterrompidosPorCausa[causa]
}

// razaoDoTerminou traduz o estado final de uma corrida que não completou na sua razão.
func razaoDoTerminou(terminou string) string {
	switch terminou {
	case TerminouCompleta, TerminouPerfilRecusado:
		// A corrida parada por o perfil ser recusado já tem a sua razão firme.
		return ""
	case TerminouNaoCoube:
		return RazaoPiorCasoNaoCabe
	case TerminouTecto:
		return RazaoTectoDoDia
	case TerminouTectoDaQualificacao:
		return RazaoTectoDaQualificacao
	case TerminouChaveRecusada:
		return RazaoChaveRecusada
	case TerminouSaldoInsuficiente:
		return RazaoSaldoInsuficiente
	case TerminouLimiteDeRitmo, TerminouSo429, TerminouSerieDe429:
		return RazaoLimiteDeRitmo
	case TerminouModeloDesconhecido, TerminouRotaIndisponivel, TerminouSondaFalhou:
		return RazaoSondaFalhou
	}
	// `interrompida`, `contador_inutilizavel` e qualquer valor que a regra não conheça: fechado.
	return RazaoCorridaInterrompida
}

// VeredictoDoModelo é o veredicto do banco sobre a rota qualificada. É calculado, não interpretado.
type VeredictoDoModelo struct {
	// Veredicto é um de `qualificado`, `recusado`, `inconclusivo`.
	Veredicto string `json:"veredicto"`
	// Razoes são as razões de o veredicto não ser `qualificado`, numa ordem fixa: primeiro as
	// firmes, depois as passageiras. Vazio em `qualificado`.
	Razoes []string `json:"razoes"`
	// Regra é a regra aplicada, em texto fixo.
	Regra string `json:"regra"`
}

// qualificarModelo calcula o veredicto. É função PURA das contagens: as mesmas contagens dão o
// mesmo veredicto e as mesmas razões, pela mesma ordem.
func qualificarModelo(c ContagensDaQualificacao) VeredictoDoModelo {
	v := VeredictoDoModelo{Razoes: []string{}, Regra: RegraDoVeredictoDoModelo}
	juntar := func(se bool, razao string) {
		if se {
			v.Razoes = append(v.Razoes, razao)
		}
	}
	juntar(c.violaL1(), RazaoModeloNaoCumpridos)
	juntar(c.violaL2(), RazaoModeloSemToolCall)
	juntar(c.violaL3(), RazaoModelo4xxNoSegundo)
	juntar(c.violaL4(), RazaoModeloRespostasVazias)
	juntar(c.TurnosComModeloDiferente > LimiarL5ModeloTrocado, RazaoModeloServidoDiferente)
	juntar(c.TurnosSemModeloReportado > LimiarL5ModeloTrocado, RazaoModeloServidoNaoReportado)
	juntar(c.violaL6(), RazaoSemToolsNaoCumpridos)
	juntar(c.Devolucao == QualificacaoNaoCumprida, RazaoDevolucaoNaoCumprida)
	juntar(c.perfilRecusado(), RazaoPerfilRecusado)
	firmes := len(v.Razoes)

	r := razaoDoTerminou(c.Terminou)
	if r == "" && c.interrompidos(InterrompidoPeloCancelamento) > 0 {
		// O cancelamento apanhou o ÚLTIMO passo: a corrida deu a volta toda e não está completa.
		r = RazaoCorridaInterrompida
	}
	if r != "" {
		v.Razoes = append(v.Razoes, r)
	}
	juntar(c.amostraIncompleta(), RazaoAmostraIncompleta)
	juntar(c.interrompidos(InterrompidoPor429) > 0, RazaoInterrompidos429)
	juntar(c.interrompidos(InterrompidoPor5xx) > 0, RazaoInterrompidos5xx)
	juntar(c.interrompidos(InterrompidoPorTransporte) > 0, RazaoInterrompidosTransporte)
	juntar(c.interrompidos(InterrompidoPorConta) > 0, RazaoInterrompidosConta)
	juntar(c.interrompidos(InterrompidoPeloTecto) > 0, RazaoInterrompidosTecto)
	// Fechado: com devolução declarada, tudo o que não seja `cumprida` nem `nao_cumprida` é «não medida».
	juntar(c.Devolucao != "" && c.Devolucao != QualificacaoCumprida && c.Devolucao != QualificacaoNaoCumprida, RazaoDevolucaoNaoMedida)

	switch {
	case firmes > 0:
		v.Veredicto = VeredictoRecusado
	case len(v.Razoes) > 0:
		v.Veredicto = VeredictoInconclusivo
	default:
		v.Veredicto = VeredictoQualificado
	}
	return v
}

// LimiarDaQualificacao é uma linha da tabela de limiares do relatório: a contagem, a amostra sobre
// que foi feita, o intervalo de confiança a 95% e o limiar. O intervalo NÃO entra na regra.
type LimiarDaQualificacao struct {
	ID string `json:"id"`
	// Maximo é o maior valor do numerador que não viola o limiar.
	Maximo int `json:"maximo_admitido"`
	// Medida é a contagem, com a amostra e o intervalo de Wilson a 95%.
	Medida Taxa `json:"medida"`
	// Violado diz se a contagem passa do máximo.
	Violado bool `json:"violado"`
}

// limiaresDaQualificacao monta a tabela de limiares a partir das contagens.
func limiaresDaQualificacao(c ContagensDaQualificacao) []LimiarDaQualificacao {
	return []LimiarDaQualificacao{
		{ID: "L1", Maximo: LimiarL1NaoCumpridos, Violado: c.violaL1(),
			Medida: NovaTaxa("nos com tools na amostra cuja ultima tentativa nao fechou cumprido", c.naoCumpridos(), c.NosComToolsNaAmostra)},
		{ID: "L2", Maximo: LimiarL2SemToolCall, Violado: c.violaL2(),
			Medida: NovaTaxa("primeiras tentativas de nos com tools, com pelo menos um turno, que fecharam sem nenhuma tool call nativa (qualquer motivo de paragem)", c.SemToolCallAPrimeira, c.PrimeirasTentativasComTools)},
		{ID: "L3", Maximo: LimiarL3QuatroXX, Violado: c.violaL3(),
			Medida: NovaTaxa("respostas 4xx de parametro a pedidos que levavam uma mensagem tool (os segundos turnos)", c.QuatroXXComMensagemTool, c.PedidosComMensagemTool)},
		{ID: "L4", Maximo: LimiarL4Vazias, Violado: c.violaL4(),
			Medida: NovaTaxa("nos, com e sem tools, cuja ultima tentativa fechou empty_output", c.RespostasVaziasNoFim, c.NosObservados)},
		{ID: "L5", Maximo: LimiarL5ModeloTrocado, Violado: c.violaL5(),
			Medida: NovaTaxa("turnos em que o modelo que o proxy declarou ter servido nao e o do perfil, ou nao foi declarado", c.modeloTrocado(), c.Turnos)},
		{ID: "L6", Maximo: LimiarL6SemToolsNaoCumpridos, Violado: c.violaL6(),
			Medida: NovaTaxa("nos sem tools na amostra cuja ultima tentativa nao fechou cumprido", c.semToolsNaoCumpridos(), c.NosSemToolsNaAmostra)},
	}
}

// verificacaoDaRota lê de uma resposta o resultado da comparação do modelo servido, fechado no
// vocabulário da porta. Vazio ⇒ a resposta não foi comparada.
func verificacaoDaRota(check string) string {
	switch check {
	case port.RouteCheckEqual, port.RouteCheckDifferent, port.RouteCheckUnreported:
		return check
	}
	return ""
}

// contarPedido junta às medidas do run o que aconteceu a um pedido.
func (m *MedidasDoRun) contarPedido(c chamadaObservada) {
	if c.levaMensagemTool {
		m.PedidosComMensagemTool++
		if quatroXXDeParametro(c.status, tipoDoVocabulario(c.tipoDeErro)) {
			m.QuatroXXComMensagemTool++
		}
	}
	if c.erro != "" {
		return
	}
	switch c.rota {
	case port.RouteCheckEqual:
		m.TurnosComModeloIgual++
	case port.RouteCheckDifferent:
		m.TurnosComModeloDiferente++
	default:
		m.TurnosSemModeloReportado++
	}
}
