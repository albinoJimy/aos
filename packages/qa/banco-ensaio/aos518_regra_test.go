package bancoensaio

import (
	"reflect"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// AOS-518 — A REGRA DO VEREDICTO DO MODELO.
//
// O veredicto é função pura das contagens. Estes testes não correm nada: dão contagens à regra e
// conferem o veredicto e as razões, pela ordem. Cada limiar tem aqui os dois lados da fronteira —
// é o que as mutações dirigidas (Estado do ticket) avermelham.

// contagensBoas são as contagens de uma qualificação sem falha nenhuma: 40 de 40.
func contagensBoas() ContagensDaQualificacao {
	return ContagensDaQualificacao{
		Terminou: TerminouCompleta, NosComToolsExigidos: NosComToolsDaQualificacao,
		NosComToolsNaAmostra: 40, NosComToolsCumpridos: 40,
		NosSemToolsExigidos: NosSemToolsDaQualificacao, NosSemToolsNaAmostra: 16, NosSemToolsCumpridos: 16,
		NaoCumpridosPorDesfecho: map[string]int{}, InterrompidosPorCausa: map[string]int{},
		NosSemToolsNaoCumpridosPorDesfecho: map[string]int{}, NosSemToolsInterrompidosPorCausa: map[string]int{},
		PrimeirasTentativasComTools: 40, PedidosComMensagemTool: 40, NosObservados: 56,
		Turnos: 96, TurnosComModeloIgual: 96, PedidosComResposta: 96, PedidosCom2xx: 96,
	}
}

func TestAOS518_Regra_OsLimiaresSaoOsDaDecisaoD2(t *testing.T) {
	if NosSemToolsDaQualificacao != 16 || LimiarL6SemToolsNaoCumpridos != 0 || QualificacoesPorRotaPorDia != 4 {
		t.Errorf("nos sem tools %d, L6 %d, qualificacoes por rota %d; quer 16, 0 e 4", NosSemToolsDaQualificacao, LimiarL6SemToolsNaoCumpridos, QualificacoesPorRotaPorDia)
	}
	if n := nosSemToolsPorPassagem(mustBateria(t)) * PassagensDaQualificacao; n != NosSemToolsDaQualificacao {
		t.Errorf("a bateria da %d nos sem tools em 8 passagens; a regra conta com %d", n, NosSemToolsDaQualificacao)
	}
	if PassagensDaQualificacao != 8 || NosComToolsDaQualificacao != 40 {
		t.Errorf("amostra da qualificacao = %d passagens, %d nos com tools; a D2 fixa 8 e 40", PassagensDaQualificacao, NosComToolsDaQualificacao)
	}
	if LimiarL1NaoCumpridos != 0 || LimiarL2SemToolCall != 4 || LimiarL3QuatroXX != 0 || LimiarL4Vazias != 0 || LimiarL5ModeloTrocado != 0 {
		t.Errorf("limiares = %d, %d, %d, %d, %d; a D2 fixa 0, 4, 0, 0, 0", LimiarL1NaoCumpridos, LimiarL2SemToolCall, LimiarL3QuatroXX, LimiarL4Vazias, LimiarL5ModeloTrocado)
	}
	if TectoDePedidosDaQualificacao != 300 || TectoMicroUSDDaQualificacao != 3_000_000 || QualificacoesPorPerfilPorDia != 2 {
		t.Errorf("orcamento = %d pedidos, %d micro-USD, %d por dia; a D7 fixa 300, 3 USD e 2", TectoDePedidosDaQualificacao, TectoMicroUSDDaQualificacao, QualificacoesPorPerfilPorDia)
	}
	b := mustBateria(t)
	if n := nosComToolsPorPassagem(b) * PassagensDaQualificacao; n != NosComToolsDaQualificacao {
		t.Errorf("a bateria da %d nos com tools em %d passagens; a regra conta com %d", n, PassagensDaQualificacao, NosComToolsDaQualificacao)
	}
	if maximo, _ := PlanoDaQualificacao(b, 1).PedidosMaximos(b); maximo != 672 {
		t.Errorf("pior caso de 8 passagens = %d pedidos; o ticket declara 672 (673 com a sonda)", maximo)
	}
}

func TestAOS518_Regra_TabelaDoVeredicto(t *testing.T) {
	type caso struct {
		nome      string
		muda      func(c *ContagensDaQualificacao)
		veredicto string
		razoes    []string
	}
	casos := []caso{
		{"tudo dentro", func(*ContagensDaQualificacao) {}, VeredictoQualificado, []string{}},

		// Cada razão FIRME sozinha, no primeiro valor que viola o limiar.
		{"L1: um no por cumprir", func(c *ContagensDaQualificacao) { c.NosComToolsCumpridos = 39 }, VeredictoRecusado, []string{RazaoModeloNaoCumpridos}},
		{"L2: quatro sem tool call a primeira ainda cabem", func(c *ContagensDaQualificacao) { c.SemToolCallAPrimeira = 4 }, VeredictoQualificado, []string{}},
		{"L2: cinco ja nao", func(c *ContagensDaQualificacao) { c.SemToolCallAPrimeira = 5 }, VeredictoRecusado, []string{RazaoModeloSemToolCall}},
		{"L3: um 4xx num segundo turno", func(c *ContagensDaQualificacao) { c.QuatroXXComMensagemTool = 1 }, VeredictoRecusado, []string{RazaoModelo4xxNoSegundo}},
		{"L4: uma resposta vazia no fim", func(c *ContagensDaQualificacao) { c.RespostasVaziasNoFim = 1 }, VeredictoRecusado, []string{RazaoModeloRespostasVazias}},
		{"L5: um turno com outro modelo", func(c *ContagensDaQualificacao) { c.TurnosComModeloIgual, c.TurnosComModeloDiferente = 95, 1 }, VeredictoRecusado, []string{RazaoModeloServidoDiferente}},
		{"L5: um turno sem modelo reportado", func(c *ContagensDaQualificacao) { c.TurnosComModeloIgual, c.TurnosSemModeloReportado = 95, 1 }, VeredictoRecusado, []string{RazaoModeloServidoNaoReportado}},
		{"L6: um no sem tools por cumprir", func(c *ContagensDaQualificacao) { c.NosSemToolsCumpridos = 15 }, VeredictoRecusado, []string{RazaoSemToolsNaoCumpridos}},
		{"devolucao declarada e cumprida", func(c *ContagensDaQualificacao) { c.Devolucao = QualificacaoCumprida }, VeredictoQualificado, []string{}},
		{"devolucao declarada e nao cumprida", func(c *ContagensDaQualificacao) { c.Devolucao = QualificacaoNaoCumprida }, VeredictoRecusado, []string{RazaoDevolucaoNaoCumprida}},
		{"devolucao declarada, sem raciocinio", func(c *ContagensDaQualificacao) { c.Devolucao = QualificacaoSemRaciocinio }, VeredictoInconclusivo, []string{RazaoDevolucaoNaoMedida}},
		{"devolucao declarada, inconclusiva", func(c *ContagensDaQualificacao) { c.Devolucao = QualificacaoInconclusiva }, VeredictoInconclusivo, []string{RazaoDevolucaoNaoMedida}},
		{"devolucao com um valor que a regra nao conhece", func(c *ContagensDaQualificacao) { c.Devolucao = "outra_coisa" }, VeredictoInconclusivo, []string{RazaoDevolucaoNaoMedida}},
		{"amostra de 15 nos sem tools", func(c *ContagensDaQualificacao) { c.NosSemToolsNaAmostra, c.NosSemToolsCumpridos = 15, 15 }, VeredictoInconclusivo, []string{RazaoAmostraIncompleta}},
		{"um no SEM tools interrompido por 5xx", func(c *ContagensDaQualificacao) {
			c.NosSemToolsNaAmostra, c.NosSemToolsCumpridos, c.NosSemToolsInterrompidosPorCausa = 15, 15, map[string]int{InterrompidoPor5xx: 1}
		}, VeredictoInconclusivo, []string{RazaoAmostraIncompleta, RazaoInterrompidos5xx}},
		{"os 16 nos SEM tools interrompidos por 5xx", func(c *ContagensDaQualificacao) {
			c.NosSemToolsNaAmostra, c.NosSemToolsCumpridos, c.NosSemToolsInterrompidosPorCausa = 0, 0, map[string]int{InterrompidoPor5xx: 16}
		}, VeredictoInconclusivo, []string{RazaoAmostraIncompleta, RazaoInterrompidos5xx}},
		{"perfil recusado em todos os pedidos", func(c *ContagensDaQualificacao) {
			*c = ContagensDaQualificacao{Terminou: TerminouPerfilRecusado, NosComToolsExigidos: 40, NosComToolsNaAmostra: 3,
				PedidosComResposta: 3, Pedidos4xxDeParametro: 3}
		}, VeredictoRecusado, []string{RazaoModeloNaoCumpridos, RazaoPerfilRecusado, RazaoAmostraIncompleta}},
		{"um 2xx entre os 4xx ja nao e perfil recusado", func(c *ContagensDaQualificacao) {
			c.PedidosComResposta, c.PedidosCom2xx, c.Pedidos4xxDeParametro = 10, 1, 9
		}, VeredictoQualificado, []string{}},

		// Cada razão PASSAGEIRA sozinha: inconclusivo, nunca qualificado, nunca recusado.
		{"nao coube no tecto", func(c *ContagensDaQualificacao) {
			*c = ContagensDaQualificacao{Terminou: TerminouNaoCoube, NosComToolsExigidos: 40}
		}, VeredictoInconclusivo, []string{RazaoPiorCasoNaoCabe, RazaoAmostraIncompleta}},
		{"amostra de 39", func(c *ContagensDaQualificacao) { c.NosComToolsNaAmostra, c.NosComToolsCumpridos = 39, 39 }, VeredictoInconclusivo, []string{RazaoAmostraIncompleta}},
		{"um no interrompido por 429", func(c *ContagensDaQualificacao) {
			c.NosComToolsNaAmostra, c.NosComToolsCumpridos, c.InterrompidosPorCausa = 39, 39, map[string]int{InterrompidoPor429: 1}
		}, VeredictoInconclusivo, []string{RazaoAmostraIncompleta, RazaoInterrompidos429}},
		{"um no interrompido por 5xx", func(c *ContagensDaQualificacao) {
			c.NosComToolsNaAmostra, c.NosComToolsCumpridos, c.InterrompidosPorCausa = 39, 39, map[string]int{InterrompidoPor5xx: 1}
		}, VeredictoInconclusivo, []string{RazaoAmostraIncompleta, RazaoInterrompidos5xx}},
		{"um no interrompido pelo transporte", func(c *ContagensDaQualificacao) {
			c.NosComToolsNaAmostra, c.NosComToolsCumpridos, c.InterrompidosPorCausa = 39, 39, map[string]int{InterrompidoPorTransporte: 1}
		}, VeredictoInconclusivo, []string{RazaoAmostraIncompleta, RazaoInterrompidosTransporte}},
		{"um no interrompido pela conta", func(c *ContagensDaQualificacao) {
			c.NosComToolsNaAmostra, c.NosComToolsCumpridos, c.InterrompidosPorCausa = 39, 39, map[string]int{InterrompidoPorConta: 1}
		}, VeredictoInconclusivo, []string{RazaoAmostraIncompleta, RazaoInterrompidosConta}},
		{"um no interrompido pelo tecto", func(c *ContagensDaQualificacao) {
			c.NosComToolsNaAmostra, c.NosComToolsCumpridos, c.InterrompidosPorCausa = 39, 39, map[string]int{InterrompidoPeloTecto: 1}
		}, VeredictoInconclusivo, []string{RazaoAmostraIncompleta, RazaoInterrompidosTecto}},
		// Uma corrida que parou e, por acaso, ja tinha os 40: continua a nao qualificar.
		{"parou a meio com a amostra inteira", func(c *ContagensDaQualificacao) { c.Terminou = TerminouInterrompid }, VeredictoInconclusivo, []string{RazaoCorridaInterrompida}},

		// AS PRECEDÊNCIAS: firme ganha a passageira; as firmes saem primeiro, pela ordem L1..L5.
		{"firme com passageira ao lado", func(c *ContagensDaQualificacao) {
			c.Terminou, c.NosComToolsNaAmostra, c.NosComToolsCumpridos, c.RespostasVaziasNoFim = TerminouTecto, 10, 10, 1
			c.InterrompidosPorCausa = map[string]int{InterrompidoPeloTecto: 1}
		}, VeredictoRecusado, []string{RazaoModeloRespostasVazias, RazaoTectoDoDia, RazaoAmostraIncompleta, RazaoInterrompidosTecto}},
		{"L2 violado numa amostra parcial", func(c *ContagensDaQualificacao) {
			c.Terminou, c.NosComToolsNaAmostra, c.NosComToolsCumpridos, c.SemToolCallAPrimeira = TerminouSerieDe429, 12, 12, 5
		}, VeredictoRecusado, []string{RazaoModeloSemToolCall, RazaoLimiteDeRitmo, RazaoAmostraIncompleta}},
		{"todas as firmes, pela ordem", func(c *ContagensDaQualificacao) {
			c.NosComToolsCumpridos, c.SemToolCallAPrimeira, c.QuatroXXComMensagemTool, c.RespostasVaziasNoFim = 30, 9, 2, 3
			c.TurnosComModeloDiferente, c.TurnosSemModeloReportado, c.NosSemToolsCumpridos, c.Devolucao = 1, 1, 14, QualificacaoNaoCumprida
		}, VeredictoRecusado, []string{RazaoModeloNaoCumpridos, RazaoModeloSemToolCall, RazaoModelo4xxNoSegundo, RazaoModeloRespostasVazias, RazaoModeloServidoDiferente, RazaoModeloServidoNaoReportado, RazaoSemToolsNaoCumpridos, RazaoDevolucaoNaoCumprida}},
	}
	// Cada estado final que não é `completa`, sozinho, com a sua razão.
	for terminou, razao := range map[string]string{
		TerminouTecto: RazaoTectoDoDia, TerminouTectoDaQualificacao: RazaoTectoDaQualificacao,
		TerminouChaveRecusada: RazaoChaveRecusada, TerminouSaldoInsuficiente: RazaoSaldoInsuficiente,
		TerminouLimiteDeRitmo: RazaoLimiteDeRitmo, TerminouSo429: RazaoLimiteDeRitmo, TerminouSerieDe429: RazaoLimiteDeRitmo,
		TerminouModeloDesconhecido: RazaoSondaFalhou, TerminouRotaIndisponivel: RazaoSondaFalhou, TerminouSondaFalhou: RazaoSondaFalhou,
		TerminouInterrompid: RazaoCorridaInterrompida, TerminouContador: RazaoCorridaInterrompida,
		"um_estado_que_a_regra_nao_conhece": RazaoCorridaInterrompida,
	} {
		terminou, razao := terminou, razao
		casos = append(casos, caso{"parou: " + terminou, func(c *ContagensDaQualificacao) {
			*c = ContagensDaQualificacao{Terminou: terminou, NosComToolsExigidos: 40}
		}, VeredictoInconclusivo, []string{razao, RazaoAmostraIncompleta}})
	}
	vistas := map[string]bool{}
	for _, c := range casos {
		contagens := contagensBoas()
		c.muda(&contagens)
		v := qualificarModelo(contagens)
		if v.Veredicto != c.veredicto || !reflect.DeepEqual(v.Razoes, c.razoes) {
			t.Errorf("%s: %s %v, quer %s %v", c.nome, v.Veredicto, v.Razoes, c.veredicto, c.razoes)
		}
		// FUNÇÃO PURA: as mesmas contagens, o mesmo veredicto e as mesmas razões, pela mesma ordem.
		if outra := qualificarModelo(contagens); !reflect.DeepEqual(v, outra) {
			t.Errorf("%s: duas chamadas com as mesmas contagens deram veredictos diferentes", c.nome)
		}
		if v.Regra != RegraDoVeredictoDoModelo {
			t.Errorf("%s: a regra nao vai ao lado do veredicto", c.nome)
		}
		for _, r := range v.Razoes {
			vistas[r] = true
		}
	}
	// A tabela cobre TODAS as razões do vocabulário, e a regra não inventa nenhuma fora dele.
	vocabulario := append(RazoesFirmesDoModelo(), RazoesPassageirasDoModelo()...)
	for _, r := range vocabulario {
		if !vistas[r] {
			t.Errorf("a razao %q nao tem caso na tabela", r)
		}
		delete(vistas, r)
	}
	for r := range vistas {
		t.Errorf("a regra deu uma razao fora do vocabulario: %q", r)
	}
}

// Um veredicto `qualificado` exige, sempre, os 40 nos cumpridos: nao ha caminho que la chegue com
// contagens a zero (uma corrida que nao correu) nem com a amostra por completar.
func TestAOS518_Regra_SemAmostraNaoHaQualificado(t *testing.T) {
	for nome, c := range map[string]ContagensDaQualificacao{
		"contagens a zero":           {},
		"completa sem observacoes":   {Terminou: TerminouCompleta, NosComToolsExigidos: 40},
		"completa com 39 cumpridos":  {Terminou: TerminouCompleta, NosComToolsExigidos: 40, NosComToolsNaAmostra: 39, NosComToolsCumpridos: 39},
		"40 com tools, 15 sem tools": {Terminou: TerminouCompleta, NosComToolsExigidos: 40, NosComToolsNaAmostra: 40, NosComToolsCumpridos: 40, NosSemToolsExigidos: 16, NosSemToolsNaAmostra: 15, NosSemToolsCumpridos: 15},
		"40 na amostra, estado nulo": {NosComToolsExigidos: 40, NosComToolsNaAmostra: 40, NosComToolsCumpridos: 40},
	} {
		if v := qualificarModelo(c); v.Veredicto == VeredictoQualificado {
			t.Errorf("%s: deu qualificado", nome)
		}
	}
}

func TestAOS518_Regra_TabelaDeLimiaresComIntervaloQueNaoEntraNaRegra(t *testing.T) {
	c := contagensBoas()
	c.SemToolCallAPrimeira = 4
	limiares := limiaresDaQualificacao(c)
	if len(limiares) != 6 {
		t.Fatalf("limiares = %d, quer 6", len(limiares))
	}
	verTaxa(t, "L6", limiares[5].Medida, 0, 16)
	for i, id := range []string{"L1", "L2", "L3", "L4", "L5", "L6"} {
		l := limiares[i]
		if l.ID != id || l.Medida.IC95Superior == nil || l.Violado {
			t.Errorf("limiar %d = %+v", i, l)
		}
	}
	verTaxa(t, "L1", limiares[0].Medida, 0, 40)
	verTaxa(t, "L2", limiares[1].Medida, 4, 40)
	// 0 em 40: o limite superior do intervalo e cerca de 9% (8,76%). Vai no relatorio.
	if sup := *limiares[0].Medida.IC95Superior; sup < 0.085 || sup > 0.09 {
		t.Errorf("limite superior de 0/40 = %.4f, quer cerca de 0,088", sup)
	}
	// 4 em 40 tem um limite superior de 23%: se o intervalo entrasse na regra, L2 nao passava.
	if sup := *limiares[1].Medida.IC95Superior; sup < 0.2 {
		t.Errorf("limite superior de 4/40 = %.4f", sup)
	}
	if v := qualificarModelo(c); v.Veredicto != VeredictoQualificado {
		t.Errorf("4 de 40 sem tool call a primeira = %s %v: o limiar aplica-se a contagem, nao ao intervalo", v.Veredicto, v.Razoes)
	}
}

func TestAOS518_Regra_EstadoFinalDoNo(t *testing.T) {
	obs := func(desfecho string, http []int, tipos ...string) Observacao {
		return Observacao{ExigeTool: true, Desfecho: desfecho, HTTP: http, TiposDeErro: tipos}
	}
	for nome, c := range map[string]struct {
		o             Observacao
		classe, causa string
	}{
		"cumprido":                {obs(DesfechoCumprido, []int{200, 200}), noCumprido, ""},
		"sem tool call":           {obs(string(agentruntime.OutcomeContractNoCall), []int{200}), noNaoCumprido, ""},
		"vazia":                   {obs(string(agentruntime.OutcomeEmptyOutput), []int{200, 200}), noNaoCumprido, ""},
		"cortada":                 {obs(string(agentruntime.OutcomeTruncated), []int{200, 200}), noNaoCumprido, ""},
		"turnos esgotados":        {obs(DesfechoTurnosEsgotados, []int{200, 200, 200, 200}), noNaoCumprido, ""},
		"estado nao devolvido":    {obs(DesfechoEstadoNaoDevolvido, []int{200}), noNaoCumprido, ""},
		"resposta recusada":       {obs(DesfechoRespostaRecusada, []int{200}), noNaoCumprido, ""},
		"400 de parametro":        {obs(DesfechoErroHTTP, []int{200, 400}, TipoOutro), noNaoCumprido, ""},
		"429":                     {obs(DesfechoErroHTTP, []int{200, 429}, TipoLimiteDeRitmo), noInterrompido, InterrompidoPor429},
		"429 de saldo":            {obs(DesfechoErroHTTP, []int{429}, TipoSaldoInsuficiente), noInterrompido, InterrompidoPor429},
		"500":                     {obs(DesfechoErroHTTP, []int{500}, TipoOutro), noInterrompido, InterrompidoPor5xx},
		"503":                     {obs(DesfechoErroHTTP, []int{200, 503}, TipoOutro), noInterrompido, InterrompidoPor5xx},
		"401":                     {obs(DesfechoErroHTTP, []int{401}, TipoChaveRecusada), noInterrompido, InterrompidoPorConta},
		"402":                     {obs(DesfechoErroHTTP, []int{402}, TipoSaldoInsuficiente), noInterrompido, InterrompidoPorConta},
		"404 de modelo":           {obs(DesfechoErroHTTP, []int{404}, TipoModeloDesconhecido), noInterrompido, InterrompidoPorConta},
		"408":                     {obs(DesfechoErroHTTP, []int{408}, TipoOutro), noInterrompido, InterrompidoPorConta},
		"sem resposta":            {obs(DesfechoErroOutro, []int{200, 0}), noInterrompido, InterrompidoPorTransporte},
		"sem pedido nenhum":       {obs(DesfechoErroOutro, nil), noInterrompido, InterrompidoPorTransporte},
		"erro depois de um 200":   {obs(DesfechoErroOutro, []int{200}), noNaoCumprido, ""},
		"tempo esgotado":          {obs(DesfechoTempoEsgotado, []int{0}), noInterrompido, InterrompidoPorTransporte},
		"tecto":                   {obs(DesfechoTectoAtingido, nil), noInterrompido, InterrompidoPeloTecto},
		"contador":                {obs(DesfechoContador, nil), noInterrompido, InterrompidoPeloTecto},
		"desfecho que nao existe": {obs("um_desfecho_novo", []int{200}), noNaoCumprido, ""},
	} {
		if classe, causa := estadoFinalDoNo(c.o); classe != c.classe || causa != c.causa {
			t.Errorf("%s: %s/%s, quer %s/%s", nome, classe, causa, c.classe, c.causa)
		}
	}
}

// As contagens saem das observacoes: so conta a ULTIMA tentativa de cada no, um erro passageiro
// tira o no da amostra em vez de o dar como falhado, e um turno sem comparacao do modelo servido
// conta como nao reportado.
func TestAOS518_Regra_ContagensDasObservacoes(t *testing.T) {
	no := func(caso string, amostra, tentativa int, exige bool, desfecho string, http []int, m *MedidasDoRun, tipos ...string) Observacao {
		turnos := 0
		for _, s := range http {
			if s == 200 {
				turnos++
			}
		}
		return Observacao{Caso: caso, No: "n", Braco: "A", Amostra: amostra, Tentativa: tentativa, ExigeTool: exige,
			Desfecho: desfecho, HTTP: http, TiposDeErro: tipos, Turnos: turnos, Pedidos: len(http), Qualificacao: m,
			ToolCalls: map[bool]int{true: 0, false: 1}[desfecho == string(agentruntime.OutcomeContractNoCall)]}
	}
	igual := func(turnos, comTool, quatroXX int) *MedidasDoRun {
		return &MedidasDoRun{TurnosComModeloIgual: turnos, PedidosComMensagemTool: comTool, QuatroXXComMensagemTool: quatroXX}
	}
	obs := []Observacao{
		// T1: sem tool call a primeira, recuperado a segunda.
		no("T1", 0, 1, true, string(agentruntime.OutcomeContractNoCall), []int{200}, igual(1, 0, 0)),
		no("T1", 0, 2, true, DesfechoCumprido, []int{200, 200}, igual(2, 1, 0)),
		// T2: vazia tres vezes.
		no("T2", 0, 1, true, string(agentruntime.OutcomeEmptyOutput), []int{200, 200}, igual(2, 1, 0)),
		no("T2", 0, 2, true, string(agentruntime.OutcomeEmptyOutput), []int{200, 200}, igual(2, 1, 0)),
		no("T2", 0, 3, true, string(agentruntime.OutcomeEmptyOutput), []int{200, 200}, igual(2, 1, 0)),
		// T3: sem tools, vazio no fim — conta em L4 e nao na amostra de nos com tools.
		no("T3", 0, 1, false, string(agentruntime.OutcomeEmptyOutput), []int{200}, igual(1, 0, 0)),
		// T4: 400 ao segundo turno.
		no("T4", 0, 1, true, DesfechoErroHTTP, []int{200, 400}, igual(1, 1, 1), TipoOutro),
		// T5: 503 — passageiro, fora da amostra.
		no("T5", 0, 1, true, DesfechoErroHTTP, []int{503}, igual(0, 0, 0), TipoOutro),
		// T6: cumprido, com um turno de outro modelo e um turno que ninguem comparou.
		no("T6", 0, 1, true, DesfechoCumprido, []int{200, 200, 200}, &MedidasDoRun{TurnosComModeloIgual: 1, TurnosComModeloDiferente: 1, PedidosComMensagemTool: 2}),
	}
	c := contarParaQualificacao(TerminouCompleta, 5, 1, obs)
	quer := ContagensDaQualificacao{
		Terminou: TerminouCompleta, NosComToolsExigidos: 5, NosComToolsNaAmostra: 4, NosComToolsCumpridos: 2,
		NosSemToolsExigidos: 1, NosSemToolsNaAmostra: 1, NosSemToolsCumpridos: 0,
		NosSemToolsNaoCumpridosPorDesfecho: map[string]int{string(agentruntime.OutcomeEmptyOutput): 1}, NosSemToolsInterrompidosPorCausa: map[string]int{},
		NaoCumpridosPorDesfecho:     map[string]int{string(agentruntime.OutcomeEmptyOutput): 1, DesfechoErroHTTP: 1},
		InterrompidosPorCausa:       map[string]int{InterrompidoPor5xx: 1},
		PrimeirasTentativasComTools: 4, SemToolCallAPrimeira: 1,
		PedidosComMensagemTool: 7, QuatroXXComMensagemTool: 1,
		NosObservados: 6, RespostasVaziasNoFim: 2,
		Turnos: 14, TurnosComModeloIgual: 12, TurnosComModeloDiferente: 1, TurnosSemModeloReportado: 1,
		PedidosComResposta: 16, PedidosCom2xx: 14, Pedidos4xxDeParametro: 1,
	}
	if !reflect.DeepEqual(c, quer) {
		t.Errorf("contagens =\n%+v\nquer\n%+v", c, quer)
	}
	if outra := contarParaQualificacao(TerminouCompleta, 5, 1, obs); !reflect.DeepEqual(c, outra) {
		t.Errorf("a contagem nao e determinista")
	}
	v := qualificarModelo(c)
	if quer := []string{RazaoModeloNaoCumpridos, RazaoModelo4xxNoSegundo, RazaoModeloRespostasVazias, RazaoModeloServidoDiferente,
		RazaoModeloServidoNaoReportado, RazaoSemToolsNaoCumpridos, RazaoAmostraIncompleta, RazaoInterrompidos5xx}; v.Veredicto != VeredictoRecusado || !reflect.DeepEqual(v.Razoes, quer) {
		t.Errorf("veredicto = %s %v, quer recusado %v", v.Veredicto, v.Razoes, quer)
	}
}

// Um 4xx so e «de parametro» quando fala do pedido: a chave, a conta, o ritmo e o tempo nao contam.
func TestAOS518_Regra_QuatroXXDeParametro(t *testing.T) {
	for _, c := range []struct {
		status int
		tipo   string
		quer   bool
	}{
		{400, TipoOutro, true}, {422, TipoOutro, true}, {404, TipoOutro, true}, {403, TipoOutro, true},
		{401, TipoChaveRecusada, false}, {403, TipoChaveRecusada, false}, {402, TipoSaldoInsuficiente, false},
		{404, TipoModeloDesconhecido, false}, {404, TipoRotaIndisponivel, false}, {400, TipoSaldoInsuficiente, false},
		{429, TipoOutro, false}, {429, TipoLimiteDeRitmo, false}, {408, TipoOutro, false},
		{200, "", false}, {500, TipoOutro, false}, {0, "", false}, {399, TipoOutro, false},
	} {
		if tem := quatroXXDeParametro(c.status, c.tipo); tem != c.quer {
			t.Errorf("quatroXXDeParametro(%d, %q) = %v, quer %v", c.status, c.tipo, tem, c.quer)
		}
	}
}

// O controlo sem devolucao e os avisos. So `falhou_como_devido` prova a exigencia, e so com TODOS
// os segundos turnos recusados, a amostra de nos com tools completa, nenhum cumprido e a corrida
// completa: um 4xx solto entre respostas aceites, ou um controlo interrompido, e `inconclusivo`.
func TestAOS518_Regra_ControloEAvisos(t *testing.T) {
	passou := ContagensDaQualificacao{Terminou: TerminouCompleta, NosComToolsExigidos: 5, NosComToolsNaAmostra: 5, NosComToolsCumpridos: 5,
		NosSemToolsExigidos: 2, NosSemToolsNaAmostra: 2, NosSemToolsCumpridos: 2, PedidosComMensagemTool: 5}
	// O controlo que falha como deve: os 5 segundos turnos com 4xx, os 5 nos por cumprir; o no sem
	// tools que depende de um deles nao chega a correr (a amostra dos sem tools fica a 1 de 2).
	falhou := ContagensDaQualificacao{Terminou: TerminouCompleta, NosComToolsExigidos: 5, NosComToolsNaAmostra: 5, NosComToolsCumpridos: 0,
		NosSemToolsExigidos: 2, NosSemToolsNaAmostra: 1, NosSemToolsCumpridos: 1, PedidosComMensagemTool: 5, QuatroXXComMensagemTool: 5}
	com := func(base ContagensDaQualificacao, muda func(c *ContagensDaQualificacao)) ContagensDaQualificacao {
		muda(&base)
		return base
	}
	for nome, c := range map[string]struct {
		c    ContagensDaQualificacao
		quer string
	}{
		"correu tudo bem sem o estado":        {passou, ControloPassou},
		"todos os segundos turnos recusados":  {falhou, ControloFalhouComoDevido},
		"um so 4xx em cinco":                  {com(passou, func(c *ContagensDaQualificacao) { c.QuatroXXComMensagemTool, c.NosComToolsCumpridos = 1, 4 }), ControloInconclusivo},
		"quatro 4xx em cinco":                 {com(falhou, func(c *ContagensDaQualificacao) { c.QuatroXXComMensagemTool, c.NosComToolsCumpridos = 4, 1 }), ControloInconclusivo},
		"todos recusados, mas um no cumprido": {com(falhou, func(c *ContagensDaQualificacao) { c.NosComToolsCumpridos = 1 }), ControloInconclusivo},
		"todos recusados, amostra incompleta": {com(falhou, func(c *ContagensDaQualificacao) {
			c.NosComToolsNaAmostra, c.PedidosComMensagemTool, c.QuatroXXComMensagemTool = 4, 4, 4
		}), ControloInconclusivo},
		"interrompido com um 4xx":            {com(falhou, func(c *ContagensDaQualificacao) { c.Terminou = TerminouInterrompid }), ControloInconclusivo},
		"parou no tecto com todos recusados": {com(falhou, func(c *ContagensDaQualificacao) { c.Terminou = TerminouTecto }), ControloInconclusivo},
		"parou a meio sem 4xx":               {com(passou, func(c *ContagensDaQualificacao) { c.Terminou = TerminouTecto }), ControloInconclusivo},
		"amostra incompleta sem 4xx":         {com(passou, func(c *ContagensDaQualificacao) { c.NosComToolsNaAmostra, c.NosComToolsCumpridos = 4, 4 }), ControloInconclusivo},
		"falhou, mas nao por 4xx":            {com(passou, func(c *ContagensDaQualificacao) { c.NosComToolsCumpridos = 3 }), ControloInconclusivo},
		"um no sem tools por cumprir":        {com(passou, func(c *ContagensDaQualificacao) { c.NosSemToolsCumpridos = 1 }), ControloInconclusivo},
		"nenhum pedido com mensagem tool":    {com(falhou, func(c *ContagensDaQualificacao) { c.PedidosComMensagemTool, c.QuatroXXComMensagemTool = 0, 0 }), ControloInconclusivo},
		"contagens a zero (nada correu)":     {ContagensDaQualificacao{}, ControloInconclusivo},
	} {
		if tem := resultadoDoControlo(c.c); tem != c.quer {
			t.Errorf("%s: %s, quer %s", nome, tem, c.quer)
		}
	}
	for nome, c := range map[string]struct {
		controlo *ControloSemDevolucao
		factos   int
		semUSD   bool
		quer     []string
	}{
		"perfil sem devolucao obrigatoria": {nil, 0, false, []string{}},
		"controlo falhou como devido":      {&ControloSemDevolucao{Resultado: ControloFalhouComoDevido}, 0, false, []string{}},
		"controlo passou":                  {&ControloSemDevolucao{Resultado: ControloPassou}, 0, false, []string{AvisoExigenciaNaoProvada}},
		"controlo inconclusivo":            {&ControloSemDevolucao{Resultado: ControloInconclusivo}, 0, false, []string{AvisoExigenciaNaoProvada}},
		"controlo nao correu":              {&ControloSemDevolucao{Resultado: ControloNaoCorreu}, 0, false, []string{AvisoExigenciaNaoProvada}},
		"resultado fora do vocabulario":    {&ControloSemDevolucao{Resultado: "outra_coisa"}, 0, false, []string{AvisoExigenciaNaoProvada}},
		"factos ausentes":                  {nil, 3, false, []string{AvisoFactosAusentes}},
		"tecto em dolares nao imposto":     {nil, 0, true, []string{AvisoTectoUSDNaoImposto}},
		"os tres":                          {&ControloSemDevolucao{Resultado: ControloPassou}, 1, true, []string{AvisoExigenciaNaoProvada, AvisoFactosAusentes, AvisoTectoUSDNaoImposto}},
	} {
		if tem := avisosDaQualificacao(c.controlo, c.factos, c.semUSD); !reflect.DeepEqual(tem, c.quer) {
			t.Errorf("%s: avisos %v, quer %v", nome, tem, c.quer)
		}
	}
	for _, aviso := range []string{AvisoExigenciaNaoProvada, AvisoFactosAusentes, AvisoTectoUSDNaoImposto} {
		if strings.TrimSpace(frasesDosAvisos[aviso]) == "" {
			t.Errorf("o aviso %q nao tem frase no resumo", aviso)
		}
	}
	if len(frasesDosAvisos) != 3 {
		t.Errorf("ha %d frases de avisos para 3 avisos", len(frasesDosAvisos))
	}
	// O perfil de controlo muda SO a devolucao: a versao da projeccao fica a do perfil.
	perfil, err := LerPerfilCandidato([]byte(aos518PerfilObrigatorio))
	if err != nil {
		t.Fatal(err)
	}
	doControlo := PerfilDeControlo(*perfil)
	if doControlo.StateReturn != modelgateway.StateReturnNever || doControlo.StateReturnAt != "" || doControlo.ToolCallID != "" ||
		doControlo.ProjectionVersion != perfil.ProjectionVersion || doControlo.ExpectedModel != perfil.ExpectedModel || doControlo.Requested != perfil.Requested {
		t.Errorf("perfil de controlo = %+v", doControlo)
	}
	if err := doControlo.Validate(); err != nil {
		t.Errorf("o perfil de controlo nao e um perfil valido: %v", err)
	}
}

// L2 NAO DEPENDE DO MOTIVO DE PARAGEM. Uma primeira tentativa de um no com tools que fechou sem
// nenhuma tool call nativa conta, feche o turno com `stop`, `end_turn`, `content_filter` ou sem
// motivo — mesmo que o no cumpra a segunda. (A revisao adversarial mediu `qualificado` com 0 de
// 40 em L2 para um provider assim: a contagem so via o motivo `stop`.)
func TestAOS518_Regra_L2ContaQualquerMotivoDeParagem(t *testing.T) {
	for _, motivo := range []string{"stop", "end_turn", "content_filter", ""} {
		var obs []Observacao
		for i := 0; i < 5; i++ {
			primeira := Observacao{Caso: "T1", No: "n", Braco: "A", Amostra: i, Tentativa: 1, ExigeTool: true, Turnos: 1, Pedidos: 1,
				HTTP: []int{200}, Desfecho: string(agentruntime.OutcomeContractNoCall), SemToolCall: motivo == "stop",
				Qualificacao: &MedidasDoRun{TurnosComModeloIgual: 1}}
			if motivo != "" {
				primeira.MotivosDeParagem = []string{motivo}
			}
			segunda := Observacao{Caso: "T1", No: "n", Braco: "A", Amostra: i, Tentativa: 2, ExigeTool: true, Turnos: 2, Pedidos: 2,
				HTTP: []int{200, 200}, Desfecho: DesfechoCumprido, ToolCalls: 1, Qualificacao: &MedidasDoRun{TurnosComModeloIgual: 2, PedidosComMensagemTool: 1}}
			obs = append(obs, primeira, segunda)
		}
		c := contarParaQualificacao(TerminouCompleta, 5, 0, obs)
		if c.SemToolCallAPrimeira != 5 || c.PrimeirasTentativasComTools != 5 || c.NosComToolsCumpridos != 5 {
			t.Errorf("motivo %q: sem tool call a primeira %d de %d (cumpridos %d); quer 5 de 5", motivo, c.SemToolCallAPrimeira, c.PrimeirasTentativasComTools, c.NosComToolsCumpridos)
		}
		if v := qualificarModelo(c); v.Veredicto != VeredictoRecusado || !reflect.DeepEqual(v.Razoes, []string{RazaoModeloSemToolCall}) {
			t.Errorf("motivo %q: %s %v, quer recusado so por L2", motivo, v.Veredicto, v.Razoes)
		}
	}
}
