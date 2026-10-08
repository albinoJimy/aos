package bancoensaio

import (
	"math"
	"net/http"
	"sort"
	"strconv"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AS TAXAS. Tudo o que o banco devolve é contagem sobre observações em VOCABULÁRIO FECHADO: uma
// [Observacao] não tem um byte de texto de pedido nem de resposta.

// Os desfechos de um run do banco. Aos do kernel ([agentruntime.OutcomeReasons], copiados tal e
// qual quando o veredicto é negativo) juntam-se os do banco.
const (
	DesfechoCumprido         = "cumprido"
	DesfechoTurnosEsgotados  = "turnos_esgotados"
	DesfechoErroHTTP         = "erro_http"
	DesfechoRespostaRecusada = "resposta_recusada"
	DesfechoTempoEsgotado    = "tempo_esgotado"
	DesfechoErroOutro        = "erro_outro"
	DesfechoTectoAtingido    = "tecto_atingido"
	DesfechoContador         = "contador_inutilizavel"
)

// O resultado da verificação de factos de um run.
const (
	// FactosPresentes — todos os factos do nó estão, literalmente, na saída do run.
	FactosPresentes = "presentes"
	// FactosAusentes — pelo menos um não está.
	FactosAusentes = "ausentes"
	// FactosNaoAplicavel — o run não concluiu com texto, ou o nó não tem factos.
	FactosNaoAplicavel = "nao_aplicavel"
)

// A chegada ao segundo turno com tools de um run.
const (
	// SegundoTurnoNaoTentado — nenhum pedido do run levou uma mensagem `tool`. É o valor-zero.
	SegundoTurnoNaoTentado = ""
	// SegundoTurnoAceite — o primeiro pedido com o `assistant` das tool calls e a mensagem
	// `tool` teve HTTP 200 e deu um turno.
	SegundoTurnoAceite = "aceite"
	// SegundoTurnoRecusado — esse pedido não teve 200, ou a resposta não deu um turno.
	SegundoTurnoRecusado = "recusado"
)

// As classes de ficha que não vêm da sonda do gateway.
const (
	// FichaSemResposta — o pedido não teve resposta que a sonda pudesse ler (erro).
	FichaSemResposta = "sem_resposta"
	// FichaNaoMedida — o gateway não devolveu ficha.
	FichaNaoMedida = "nao_medida"
	// FichaIlegivel — a sonda não conseguiu ler o corpo.
	FichaIlegivel = "ilegivel"
)

// Ficha é a classe da forma de uma resposta: os rótulos da ficha do AOS-507
// ([port.ResponseShape]), juntos numa chave. Vocabulário fechado, sem conteúdo.
type Ficha struct {
	Classe string `json:"classe"`
}

// fichaDaResposta agrega a ficha do gateway numa classe. A ficha é a de produção
// ([port.ProbeResponseShape], pelo adaptador HTTP do gateway): aqui só se juntam rótulos.
func fichaDaResposta(s *port.ResponseShape) Ficha {
	if s == nil {
		return Ficha{Classe: FichaNaoMedida}
	}
	if s.Unreadable {
		return Ficha{Classe: FichaIlegivel}
	}
	chamadas := "0"
	switch {
	case s.ToolCallsN == 1:
		chamadas = "1"
	case s.ToolCallsN > 1:
		chamadas = "varias"
	}
	return Ficha{Classe: "content=" + rotuloDaFicha(s.Content) + " reasoning=" + rotuloDaFicha(s.Reasoning) +
		" refusal=" + rotuloDaFicha(s.Refusal) + " psf_refusal=" + rotuloDaFicha(s.PSFRefusal) +
		" psf_reasoning=" + rotuloDaFicha(s.PSFReasoning) + " tool_calls=" + chamadas}
}

// rotuloDaFicha deixa passar um rótulo da ficha só se tiver a forma de um rótulo: minúsculas,
// dígitos e '_', até 32 bytes. A ficha de produção já é vocabulário fechado; isto garante que,
// mesmo que deixasse de ser, texto de uma resposta não chegava ao relatório por aqui.
func rotuloDaFicha(v string) string {
	if v == "" {
		return "nenhum"
	}
	if len(v) > 32 {
		return "outro"
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return "outro"
		}
	}
	return v
}

// Observacao é o que o banco regista de UM run (uma tentativa de um nó de um caso, num braço).
type Observacao struct {
	Caso      string `json:"caso"`
	No        string `json:"no"`
	Braco     string `json:"braco"`
	Amostra   int    `json:"amostra"`
	Tentativa int    `json:"tentativa"`
	// ExigeTool diz que o nó tem contrato de conclusão (exige pelo menos uma tool).
	ExigeTool bool `json:"exige_tool"`
	// Desfecho é o desfecho do run, no vocabulário fechado.
	Desfecho string `json:"desfecho"`
	// Turnos são os pedidos do run que deram um turno (resposta aceite pelo gateway).
	Turnos  int `json:"turnos"`
	Pedidos int `json:"pedidos"`
	// ToolCalls é o total de tool calls NATIVAS que o modelo pediu (o campo `tool_calls` das
	// respostas), contadas na porta do gateway.
	ToolCalls int `json:"tool_calls_nativas"`
	// SemToolCall — o nó exige uma tool, o run não pediu nenhuma e o último turno parou com o
	// motivo `stop` (o `contract_unmet_no_call` do kernel). Não se lê texto para o decidir.
	SemToolCall bool `json:"sem_tool_call"`
	// NomeDaToolNoTexto — HEURÍSTICA DECLARADA, só para contagem: num run com SemToolCall, o
	// texto do turno continha o nome exacto de uma tool oferecida.
	NomeDaToolNoTexto bool `json:"nome_da_tool_no_texto"`
	// UltimoDesfechoDeTool é o [agentruntime.Result.LastToolOutcome] do run.
	UltimoDesfechoDeTool string `json:"ultimo_desfecho_de_tool,omitempty"`
	// SegundoTurno é a chegada ao segundo turno com tools.
	SegundoTurno string `json:"segundo_turno_com_tools,omitempty"`
	// Factos é o resultado da verificação de factos.
	Factos string `json:"factos"`
	// MotivosDeParagem, um por turno, no vocabulário do runtime.
	MotivosDeParagem []string `json:"motivos_de_paragem,omitempty"`
	// HTTP são os códigos HTTP dos pedidos do run (0 = sem resposta HTTP).
	HTTP []int `json:"http,omitempty"`
	// TiposDeErro são os tipos, em vocabulário fechado ([TiposDeErro]), dos pedidos do run com
	// resposta HTTP que não foi 200 — um por cada, pela ordem em que aparecem em HTTP.
	TiposDeErro []string `json:"tipos_de_erro,omitempty"`
	// Fichas, uma por pedido.
	Fichas          []Ficha `json:"fichas,omitempty"`
	TokensDeEntrada int64   `json:"tokens_de_entrada"`
	TokensDeSaida   int64   `json:"tokens_de_saida"`

	erroDaUltimaChamada string
}

// Taxa é uma taxa com o seu numerador, denominador e intervalo de confiança a 95% (Wilson).
type Taxa struct {
	Definicao   string `json:"definicao"`
	Numerador   int    `json:"numerador"`
	Denominador int    `json:"denominador"`
	// Valor e o intervalo são nil quando o denominador é zero: não há taxa.
	Valor        *float64 `json:"taxa"`
	IC95Inferior *float64 `json:"ic95_inferior"`
	IC95Superior *float64 `json:"ic95_superior"`
}

// zDe95 é o quantil da normal para um intervalo bilateral a 95%.
const zDe95 = 1.959963984540054

// NovaTaxa calcula a taxa e o seu intervalo de Wilson a 95%.
func NovaTaxa(definicao string, numerador, denominador int) Taxa {
	t := Taxa{Definicao: definicao, Numerador: numerador, Denominador: denominador}
	if denominador <= 0 {
		return t
	}
	n := float64(denominador)
	p := float64(numerador) / n
	z2 := zDe95 * zDe95
	centro := (p + z2/(2*n)) / (1 + z2/n)
	meia := zDe95 * math.Sqrt(p*(1-p)/n+z2/(4*n*n)) / (1 + z2/n)
	inf, sup := math.Max(0, centro-meia), math.Min(1, centro+meia)
	// Nos extremos o intervalo de Wilson toca exactamente 0 ou 1; fixa-se para não sair
	// 1e-17 por arredondamento.
	if numerador == 0 {
		inf = 0
	}
	if numerador == denominador {
		sup = 1
	}
	t.Valor, t.IC95Inferior, t.IC95Superior = &p, &inf, &sup
	return t
}

// Taxas é o conjunto de taxas e distribuições de um conjunto de observações.
type Taxas struct {
	// N é o número de runs (tentativas) observados; Unidades, o de (caso, nó, braço, amostra).
	N        int `json:"runs"`
	Unidades int `json:"unidades"`

	SemToolCallNaPrimeira Taxa `json:"sem_tool_call_na_primeira_tentativa"`
	NomeDaToolNoTexto     Taxa `json:"nome_da_tool_no_texto"`
	SegundoTurnoAceite    Taxa `json:"segundo_turno_com_tools_aceite"`
	FactosAusentes        Taxa `json:"factos_ausentes"`
	RespostaVazia         Taxa `json:"resposta_vazia"`
	Cortada               Taxa `json:"cortada"`
	ErroDoProvider        Taxa `json:"erro_do_provider"`
	LimiteDeTaxa429       Taxa `json:"limite_de_taxa_429"`
	CumpridoAPrimeira     Taxa `json:"cumprido_a_primeira_tentativa"`
	RecuperadoASegunda    Taxa `json:"recuperado_a_segunda_tentativa"`
	RecuperadoATerceira   Taxa `json:"recuperado_a_terceira_tentativa"`

	Desfechos        map[string]int `json:"desfechos"`
	MotivosDeParagem map[string]int `json:"motivos_de_paragem"`
	HTTP             map[string]int `json:"http"`
	// TiposDeErro conta, por tipo do vocabulário fechado, os pedidos com resposta sem 200.
	TiposDeErro map[string]int `json:"tipos_de_erro"`
	Fichas      map[string]int `json:"fichas"`
}

// desfechoRepetivel diz se um desfecho é dos que o banco volta a tentar: o run que acabou sem
// pedir a tool (AOS-503) e o que acabou com a resposta vazia (AOS-511).
func desfechoRepetivel(d string) bool {
	return d == string(agentruntime.OutcomeContractNoCall) || d == string(agentruntime.OutcomeEmptyOutput)
}

// CalcularTaxas calcula as taxas de um conjunto de observações. É determinista: a mesma lista
// dá os mesmos números.
func CalcularTaxas(obs []Observacao) Taxas {
	t := Taxas{
		N: len(obs), Desfechos: map[string]int{}, MotivosDeParagem: map[string]int{},
		HTTP: map[string]int{}, Fichas: map[string]int{}, TiposDeErro: map[string]int{},
	}
	type unidade struct {
		caso, no, braco string
		amostra         int
	}
	porUnidade := map[unidade]map[int]string{}
	var (
		exigeEResponde, semCall, nomeNoTexto int
		segTentado, segAceite                int
		comFactos, factosAusentes            int
		comTurno, vazias, cortadas           int
		pedidos, pedidosComErro, pedidos429  int
		primeiras, cumpridasAPrimeira        int
	)
	for _, o := range obs {
		t.Desfechos[o.Desfecho]++
		for _, m := range o.MotivosDeParagem {
			t.MotivosDeParagem[m]++
		}
		for _, f := range o.Fichas {
			t.Fichas[f.Classe]++
		}
		for _, tipo := range o.TiposDeErro {
			t.TiposDeErro[tipoDoVocabulario(tipo)]++
		}
		for _, s := range o.HTTP {
			pedidos++
			chave := "sem_resposta_http"
			if s != 0 {
				chave = strconv.Itoa(s)
			}
			t.HTTP[chave]++
			// Os 429 contam-se À PARTE: tanto os dá o fornecedor (limite de taxa) como o
			// próprio proxy (uma rota em arrefecimento), e pelo código não se distinguem.
			switch {
			case s == http.StatusTooManyRequests:
				pedidos429++
			case s != http.StatusOK:
				pedidosComErro++
			}
		}
		u := unidade{o.Caso, o.No, o.Braco, o.Amostra}
		if porUnidade[u] == nil {
			porUnidade[u] = map[int]string{}
		}
		porUnidade[u][o.Tentativa] = o.Desfecho
		if o.Tentativa == 1 {
			primeiras++
			if o.Desfecho == DesfechoCumprido {
				cumpridasAPrimeira++
			}
			if o.ExigeTool && o.Turnos >= 1 {
				exigeEResponde++
				if o.SemToolCall {
					semCall++
					if o.NomeDaToolNoTexto {
						nomeNoTexto++
					}
				}
			}
		}
		if o.SegundoTurno != SegundoTurnoNaoTentado {
			segTentado++
			if o.SegundoTurno == SegundoTurnoAceite {
				segAceite++
			}
		}
		if o.Factos == FactosPresentes || o.Factos == FactosAusentes {
			comFactos++
			if o.Factos == FactosAusentes {
				factosAusentes++
			}
		}
		if o.Turnos >= 1 {
			comTurno++
			switch o.Desfecho {
			case string(agentruntime.OutcomeEmptyOutput):
				vazias++
			case string(agentruntime.OutcomeTruncated):
				cortadas++
			}
		}
	}
	var comSegunda, aSegunda, comTerceira, aTerceira int
	for _, tentativas := range porUnidade {
		if d, houve := tentativas[2]; houve {
			comSegunda++
			if d == DesfechoCumprido {
				aSegunda++
			}
		}
		if d, houve := tentativas[3]; houve {
			comTerceira++
			if d == DesfechoCumprido {
				aTerceira++
			}
		}
	}
	t.Unidades = len(porUnidade)
	t.SemToolCallNaPrimeira = NovaTaxa("primeiras tentativas de nos que exigem uma tool, com pelo menos um turno, em que o run nao pediu nenhuma tool call nativa e o ultimo turno parou com o motivo stop (a tool call em texto; nao se le texto)", semCall, exigeEResponde)
	t.NomeDaToolNoTexto = NovaTaxa("das anteriores, aquelas em que o texto do turno continha o nome exacto de uma tool oferecida (heuristica declarada, so para contagem)", nomeNoTexto, semCall)
	t.SegundoTurnoAceite = NovaTaxa("runs que enviaram um pedido com o assistant das tool calls e a mensagem tool, em que o primeiro desses pedidos teve HTTP 200 e deu um turno", segAceite, segTentado)
	t.FactosAusentes = NovaTaxa("runs concluidos de nos com factos, em que pelo menos um facto do documento sintetico nao esta na saida (substituto determinista da recusa do proprio objectivo)", factosAusentes, comFactos)
	t.RespostaVazia = NovaTaxa("runs com pelo menos um turno que fecharam empty_output", vazias, comTurno)
	t.Cortada = NovaTaxa("runs com pelo menos um turno que fecharam truncated", cortadas, comTurno)
	t.ErroDoProvider = NovaTaxa("pedidos HTTP cuja resposta nao foi 200 nem 429 (inclui os que nao tiveram resposta HTTP)", pedidosComErro, pedidos)
	t.LimiteDeTaxa429 = NovaTaxa("pedidos HTTP com resposta 429: limite de taxa do fornecedor ou, se o proxy puser a rota em arrefecimento, do proprio proxy — pelo codigo nao se distinguem", pedidos429, pedidos)
	t.CumpridoAPrimeira = NovaTaxa("primeiras tentativas que fecharam cumprido", cumpridasAPrimeira, primeiras)
	t.RecuperadoASegunda = NovaTaxa("unidades que tiveram segunda tentativa (a primeira fechou contract_unmet_no_call ou empty_output), em que a segunda fechou cumprido", aSegunda, comSegunda)
	t.RecuperadoATerceira = NovaTaxa("unidades que tiveram terceira tentativa, em que a terceira fechou cumprido", aTerceira, comTerceira)
	return t
}

// tipoDoVocabulario deixa passar um tipo de erro só se for do vocabulário fechado.
func tipoDoVocabulario(tipo string) string {
	for _, t := range TiposDeErro() {
		if tipo == t {
			return t
		}
	}
	return TipoOutro
}

// chavesOrdenadas devolve as chaves de um mapa de contagens, por ordem.
func chavesOrdenadas(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
