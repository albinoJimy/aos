package bancoensaio

import (
	"fmt"
	"sort"
	"strings"
)

// O RELATÓRIO DA EXPERIÊNCIA `conselheiro` (AOS-528): por braço e por caso, sem veredicto e sem
// texto — nem do executor, nem das perguntas, nem dos conselhos.

// RegraDoCasoCumprido é a regra, por extenso, do agregado «caso cumprido». Vai no relatório.
const RegraDoCasoCumprido = "Uma passagem por um caso (um caso, numa amostra, num braco) conta como cumprida pela regra do degrau cumprido da experiencia degraus: todos os nos do caso tem um run que fechou cumprido (o contrato de conclusao), com todos os factos exigidos na saida e nenhum facto proibido nela, comparados como nos degraus. Uma passagem interrompida por uma causa passageira sai da amostra do braco e conta a parte, como nos degraus. No braco com conselheiro, um run em que uma consulta ficou sem resposta por uma causa que nao e do modelo (429, 5xx, erro de transporte, conta ou rota, tecto ou contador) interrompe a passagem SEMPRE, tenha o run acertado ou errado: o braco mede o executor com o conselho, e esse run nao o teve. Tirar so os que erraram deixava os acertos e subia o braco. Uma consulta com 200 e sem texto e falha do conselheiro, e conta."

// InterrompidoPeloConselheiro é a causa de uma passagem interrompida por uma consulta sem
// resposta por causa passageira ([RegraDoCasoCumprido]).
const InterrompidoPeloConselheiro = "conselheiro_sem_resposta"

// PrecosDeclarados são os preços com que o custo da experiência se estima: os da tabela de
// preços declarada, pelo modelo de cada um. nil ⇒ sem preço.
type PrecosDeclarados struct {
	Executor    *Preco `json:"executor"`
	Conselheiro *Preco `json:"conselheiro"`
}

// GastoDoPapel é o que um dos dois modelos — o executor ou o conselheiro — gastou num braço.
type GastoDoPapel struct {
	Pedidos         int64 `json:"pedidos"`
	TokensDeEntrada int64 `json:"tokens_de_entrada"`
	TokensDeSaida   int64 `json:"tokens_de_saida"`
	// MicroUSDEstimado é o custo estimado pelos tokens e pelo preço declarado; nil sem preço (e
	// zero quando o papel não fez pedido nenhum).
	MicroUSDEstimado *int64 `json:"micro_usd_estimado"`
}

// CasoNoBraco é um caso num braço.
type CasoNoBraco struct {
	Caso          string `json:"caso"`
	Passagens     int    `json:"passagens"`
	Interrompidas int    `json:"passagens_interrompidas"`
	// Cumpridos é o agregado [RegraDoCasoCumprido] sobre as passagens não interrompidas.
	Cumpridos Taxa `json:"casos_cumpridos"`
	// RunsMedidos são os runs das passagens não interrompidas; RunsComConsulta, os deles em que o
	// executor chamou `aconselhar` pelo menos uma vez; Consultas, as chamadas.
	RunsMedidos     int `json:"runs_medidos"`
	RunsComConsulta int `json:"runs_com_consulta"`
	Consultas       int `json:"consultas_pedidas"`
	// RespostasErradas são os runs medidos com um facto proibido na saída.
	RespostasErradas int `json:"runs_com_facto_proibido"`
}

// BracoDoConselheiro são os números de um braço da experiência.
type BracoDoConselheiro struct {
	Braco                 string         `json:"braco"`
	Passagens             int            `json:"passagens"`
	Interrompidas         int            `json:"passagens_interrompidas"`
	InterrompidasPorCausa map[string]int `json:"interrompidas_por_causa"`
	// CasosCumpridos é o agregado [RegraDoCasoCumprido], sobre todos os casos.
	CasosCumpridos Taxa `json:"casos_cumpridos"`
	// TaxaDeConsulta: runs medidos em que o executor chamou `aconselhar`, sobre os runs medidos
	// que a tinham. Sem denominador no braço sem conselheiro.
	TaxaDeConsulta Taxa `json:"taxa_de_consulta"`
	// Consultas são as chamadas da tool nos runs medidos; ConsultasPorRun, a média por run medido
	// com a tool (nil sem runs).
	Consultas       int      `json:"consultas_pedidas"`
	ConsultasPorRun *float64 `json:"consultas_por_run"`
	// As consultas de TODOS os runs do braço, pelo resultado.
	ConsultasComResposta         int `json:"consultas_com_resposta"`
	ConsultasVazias              int `json:"consultas_com_resposta_vazia"`
	ConsultasRecusadasPeloLimite int `json:"consultas_recusadas_pelo_limite_do_run"`
	ConsultasSemRespostaPassag   int `json:"consultas_sem_resposta_por_causa_passageira"`
	// Executor e Conselheiro são o que cada modelo gastou em TODOS os runs do braço, com os das
	// passagens interrompidas: é o que se pagou.
	Executor    GastoDoPapel `json:"executor"`
	Conselheiro GastoDoPapel `json:"conselheiro"`
	// MicroUSDTotal é a soma dos dois; MicroUSDPorCasoCumprido, o total a dividir pelos casos
	// cumpridos, arredondado ao micro-USD. nil sem preço, ou sem casos cumpridos.
	MicroUSDTotal           *int64        `json:"micro_usd_estimado_total"`
	MicroUSDPorCasoCumprido *int64        `json:"micro_usd_estimado_por_caso_cumprido"`
	PorCaso                 []CasoNoBraco `json:"por_caso"`
}

// RelatorioDoConselheiro é o que a experiência `conselheiro` acrescenta ao relatório.
type RelatorioDoConselheiro struct {
	VersaoDaBateria string `json:"versao_da_bateria"`
	Regra           string `json:"regra_do_caso_cumprido"`
	// Rota é a do conselheiro (fornecedor, modelo e o digest da rota); nil sem conselheiro.
	Rota               *RotaDoRelatorio `json:"rota_do_conselheiro,omitempty"`
	MaxConsultasPorRun int              `json:"max_consultas_por_run"`
	// PiorCasoPorFornecedor é o máximo de pedidos que a corrida podia fazer a cada fornecedor —
	// o executor e o conselheiro, com as sondas —, declarado antes do primeiro pedido.
	PiorCasoPorFornecedor map[string]int64 `json:"pior_caso_de_pedidos_por_fornecedor"`
	// Pedidos e Custo são os do conselheiro, como os da corrida o são do executor; nil sem ele.
	Pedidos *Pedidos `json:"pedidos_do_conselheiro,omitempty"`
	Custo   *Custo   `json:"custo_do_conselheiro,omitempty"`
	// Sonda é a sonda da rota do conselheiro, quando houve.
	Sonda  *Sonda               `json:"sonda_do_conselheiro,omitempty"`
	Precos PrecosDeclarados     `json:"precos_declarados"`
	Bracos []BracoDoConselheiro `json:"bracos"`
}

// estadoFinalComConselho é a classificação do estado final de um nó da experiência: a da
// qualificação ([estadoFinalDoNo]) e, no braço com conselheiro, a interrupção por uma consulta
// sem resposta por causa passageira ([RegraDoCasoCumprido]). A interrupção vale seja qual for o
// desfecho do run: só tirar os runs que erraram deixava ficar os que acertaram, e um conselheiro
// instável subia o braço.
func estadoFinalComConselho(o Observacao) (string, string) {
	classe, causa := estadoFinalDoNo(o)
	if c := o.Conselho; c != nil && c.FalhadasPorCausaPassageira > 0 && classe != noInterrompido {
		return noInterrompido, InterrompidoPeloConselheiro
	}
	return classe, causa
}

// gastoDoPapel estima o custo de um papel. Sem pedidos nem tokens é zero, haja ou não preço.
func gastoDoPapel(preco *Preco, pedidos, entrada, saida int64) GastoDoPapel {
	g := GastoDoPapel{Pedidos: pedidos, TokensDeEntrada: entrada, TokensDeSaida: saida}
	switch {
	case pedidos == 0 && entrada == 0 && saida == 0:
		zero := int64(0)
		g.MicroUSDEstimado = &zero
	case preco != nil:
		custo := custoMicroUSD(*preco, entrada, saida)
		g.MicroUSDEstimado = &custo
	}
	return g
}

// porCasoCumprido divide o custo pelos casos cumpridos, arredondado ao micro-USD mais próximo.
func porCasoCumprido(total *int64, cumpridos int) *int64 {
	if total == nil || cumpridos <= 0 {
		return nil
	}
	v := (2**total + int64(cumpridos)) / (2 * int64(cumpridos))
	return &v
}

// CalcularBracosDoConselheiro calcula os números de cada braço sobre as observações de uma
// corrida da experiência `conselheiro`. É determinista: a mesma lista dá os mesmos números.
func CalcularBracosDoConselheiro(b *Bateria, bracos []string, precos PrecosDeclarados, obs []Observacao) []BracoDoConselheiro {
	var out []BracoDoConselheiro
	for _, nome := range bracos {
		var doBraco []Observacao
		for _, o := range obs {
			if o.BracoDoConselheiro == nome {
				doBraco = append(doBraco, o)
			}
		}
		br := BracoDoConselheiro{Braco: nome, InterrompidasPorCausa: map[string]int{}}
		cumpridas, comTool, comConsulta := 0, 0, 0
		for _, c := range b.Casos {
			k := contarPassagensDoCaso(c, doBraco, estadoFinalComConselho)
			br.Passagens += k.passagens
			br.Interrompidas += k.interrompidas
			for causa, n := range k.porCausa {
				br.InterrompidasPorCausa[causa] += n
			}
			cumpridas += k.cumpridas
			caso := CasoNoBraco{Caso: c.ID, Passagens: k.passagens, Interrompidas: k.interrompidas,
				Cumpridos: NovaTaxa("passagens nao interrompidas pelo caso que cumpriram a regra do caso cumprido", k.cumpridas, k.passagens-k.interrompidas)}
			for _, o := range doBraco {
				if o.Caso != c.ID || !k.medidas[o.Amostra] {
					continue
				}
				caso.RunsMedidos++
				if o.Proibidos == ProibidosPresente {
					caso.RespostasErradas++
				}
				if o.Conselho == nil {
					continue
				}
				comTool++
				caso.Consultas += o.Conselho.Pedidas
				if o.Conselho.Pedidas > 0 {
					caso.RunsComConsulta++
				}
			}
			br.Consultas += caso.Consultas
			comConsulta += caso.RunsComConsulta
			br.PorCaso = append(br.PorCaso, caso)
		}
		br.CasosCumpridos = NovaTaxa("passagens nao interrompidas (um caso, numa amostra) que cumpriram a regra do caso cumprido, sobre todos os casos do braco", cumpridas, br.Passagens-br.Interrompidas)
		br.TaxaDeConsulta = NovaTaxa("runs medidos (das passagens nao interrompidas) em que o executor chamou a tool aconselhar pelo menos uma vez, sobre os runs medidos que a tinham", comConsulta, comTool)
		if comTool > 0 {
			media := float64(br.Consultas) / float64(comTool)
			br.ConsultasPorRun = &media
		}
		var exec, cons [3]int64
		for _, o := range doBraco {
			exec[0] += int64(o.Pedidos)
			exec[1] += o.TokensDeEntrada
			exec[2] += o.TokensDeSaida
			if c := o.Conselho; c != nil {
				cons[0] += int64(c.Pedidos)
				cons[1] += c.TokensDeEntrada
				cons[2] += c.TokensDeSaida
				br.ConsultasComResposta += c.ComResposta
				br.ConsultasVazias += c.Vazias
				br.ConsultasRecusadasPeloLimite += c.RecusadasPeloLimite
				br.ConsultasSemRespostaPassag += c.FalhadasPorCausaPassageira
			}
		}
		br.Executor = gastoDoPapel(precos.Executor, exec[0], exec[1], exec[2])
		br.Conselheiro = gastoDoPapel(precos.Conselheiro, cons[0], cons[1], cons[2])
		if br.Executor.MicroUSDEstimado != nil && br.Conselheiro.MicroUSDEstimado != nil {
			total := *br.Executor.MicroUSDEstimado + *br.Conselheiro.MicroUSDEstimado
			br.MicroUSDTotal = &total
		}
		br.MicroUSDPorCasoCumprido = porCasoCumprido(br.MicroUSDTotal, cumpridas)
		out = append(out, br)
	}
	return out
}

// relatorioDoConselheiro monta o bloco da experiência `conselheiro`. `previstos` é o pior caso do
// executor (sem a sonda).
func relatorioDoConselheiro(cfg CfgDaCorrida, previstos int64, obs []Observacao) (*RelatorioDoConselheiro, error) {
	r := &RelatorioDoConselheiro{
		VersaoDaBateria: cfg.Bateria.Versao, Regra: RegraDoCasoCumprido, MaxConsultasPorRun: cfg.Plano.MaxConsultas,
		PiorCasoPorFornecedor: map[string]int64{}, Sonda: cfg.sondaDoConselheiro,
	}
	sonda := int64(0)
	if cfg.Sondar {
		sonda = 1
	}
	r.PiorCasoPorFornecedor[cfg.Rota.Fornecedor] += previstos + sonda
	if cfg.Conselho != nil {
		r.Precos = PrecosDeclarados{Executor: cfg.Conselho.PrecoDoExecutor, Conselheiro: cfg.Conselho.PrecoDoConselheiro}
	}
	r.Bracos = CalcularBracosDoConselheiro(cfg.Bateria, cfg.Plano.BracosDoConselheiro, r.Precos, obs)
	cs := cfg.conselheiro()
	if cs == nil {
		return r, nil
	}
	rota := cfg.Conselho.Rota
	r.Rota = &rota
	consultas, err := cfg.Plano.PedidosMaximosDoConselheiro(cfg.Bateria)
	if err != nil {
		return nil, err
	}
	r.PiorCasoPorFornecedor[rota.Fornecedor] += consultas + sonda
	r.Pedidos = &Pedidos{PrevistosMax: consultas}
	r.Custo = &Custo{Estimativa: true, Nota: "gasto estimado do conselheiro pelos tokens do usage e pelo preco declarado do modelo dele; nao e a factura do fornecedor"}
	if s := cfg.sondaDoConselheiro; s != nil {
		if s.enviada {
			r.Pedidos.Enviados++
		}
		r.Custo.TokensDeEntrada += s.tokensDeEntrada
		r.Custo.TokensDeSaida += s.tokensDeSaida
	}
	for _, o := range obs {
		if c := o.Conselho; c != nil {
			r.Pedidos.Enviados += int64(c.Pedidos)
			r.Custo.TokensDeEntrada += c.TokensDeEntrada
			r.Custo.TokensDeSaida += c.TokensDeSaida
		}
	}
	if cs.contador != nil {
		e, err := cs.contador.Estado()
		if err != nil {
			return nil, err
		}
		// Com contador, «enviados» são as reservas que o contador do conselheiro fez.
		r.Pedidos.Enviados = e.EnviadosNestaCorrida
		r.Pedidos.TectoDoDia, r.Pedidos.GastosHoje, r.Pedidos.RestantesHoje = &e.TectoPedidos, &e.PedidosHoje, &e.PedidosRestantes
		r.Pedidos.DiaUTC = e.Dia
		if e.TemUSD {
			r.Custo.MicroUSDHoje, r.Custo.TectoMicroUSD, r.Custo.MicroUSDRestante = &e.MicroUSDHoje, &e.TectoMicroUSD, &e.MicroUSDRestante
		}
	}
	return r, nil
}

// limitesDoConselheiro são as frases, fixas, do que uma corrida da experiência não prova.
func limitesDoConselheiro() []string {
	return []string{
		"A experiencia conselheiro NAO da veredicto: nao diz se o conselheiro compensa. Da, por braco e por caso, os casos cumpridos, a taxa de consulta e o custo estimado; a decisao e do dono.",
		"A bateria 3.0.0 e sintetica e pequena: sete casos de um no, documentos inventados. Com poucas passagens por braco os intervalos de 95% sao quase todo o intervalo: uma diferenca entre bracos so se le com --amostras e com os intervalos.",
		"O conselheiro so ve o que o executor lhe passa — a pergunta e o contexto que ele escolher —, nao o objectivo nem os documentos. Um conselho errado pode ser culpa do que o executor mandou, e o relatorio nao separa as duas causas.",
		"A taxa de consulta depende do texto da tool aconselhar (fixo, e no digest da configuracao), do prompt de sistema e do esforco do modelo executor: outro texto ou outro modelo da outra taxa. Nao e uma propriedade do conselheiro.",
		"A consulta sai directamente para a rota do conselheiro no proxy efemero, como a sonda: nao passa pelo Model Gateway do no de ensaio (sem projeccao do tail, sem allowlist, sem ficha da forma da resposta). O que se mede do conselheiro e o codigo HTTP, os tokens e se trouxe texto.",
		"O custo e uma estimativa pelos tokens do usage e pela tabela de precos declarada, e conta TODOS os runs do braco, incluindo os das passagens interrompidas: e o que se pagou. O custo por caso cumprido divide esse total pelos casos cumpridos; um braco sem casos cumpridos nao tem custo por caso.",
		"O tecto — o modelo forte sozinho — nao e um braco desta corrida: e outra corrida da mesma experiencia, sem conselheiro, com o modelo forte como executor e a mesma semente. Os casos comparam-se entre os dois relatorios pelo digest da bateria; uma deriva da rota entre as duas corridas nao se reparte.",
		"Os bracos correm intercalados por amostra, numa ordem baralhada pela semente; dentro de cada braco os casos correm pela ordem X1 a X7.",
		"Um facto proibido conta como presente onde quer que apareca na saida, mesmo numa negacao, como nos degraus. Uma resposta errada que nao seja um dos proibidos listados conta como facto ausente.",
		"Um run com uma consulta sem resposta por causa passageira interrompe a passagem, acerte ou erre: com um conselheiro instavel o braco com ele fica com menos amostras, e le-se pela coluna das interrompidas e pela causa conselheiro_sem_resposta.",
		"A taxa de acerto por palpite nao e zero: nos casos X1, X2, X3, X6 e X7 a resposta e um de tres ou quatro valores parecidos que estao nos documentos, e um modelo que da um so deles ao acaso acerta cerca de 1 em 3 a 1 em 4. Uma diferenca entre bracos le-se contra essa base, e com amostras.",
		"O caso X5 mede obediencia a varias restricoes ao mesmo tempo (calar valores que estao ao lado dos pedidos): o conselheiro so ve o que o executor lhe manda, e ajuda pouco a uma falha que esta na escrita da resposta do executor.",
		"Uma consulta que esgota o prazo, ou que tem 200 com um corpo ilegivel, nao regista uso: o fornecedor pode te-la cobrado, e o gasto estimado e o tecto em dolares do conselheiro ficam subestimados nesse caso — como os do executor.",
		"Um contador desaparecido reconstroi-se dos relatorios de hoje das pastas do dono, para todos os fornecedores que neles aparecem (do executor e do conselheiro); um gasto feito fora destes relatorios nao se reconstitui.",
	}
}

// blocoDoConselheiro escreve a tabela curta da experiência no resumo em texto. Sem o bloco no
// relatório não escreve nada.
func blocoDoConselheiro(b *strings.Builder, r *Relatorio) {
	c := r.Taxas.Conselheiro
	if c == nil {
		return
	}
	fracao := func(t Taxa) string {
		if t.Denominador == 0 {
			return "-"
		}
		return fmt.Sprintf("%d/%d", t.Numerador, t.Denominador)
	}
	usd := func(v *int64) string {
		if v == nil {
			return "sem preco"
		}
		return fmt.Sprintf("%.6f", float64(*v)/1e6)
	}
	rota := "sem conselheiro nesta corrida"
	if c.Rota != nil {
		rota = fmt.Sprintf("conselheiro fornecedor=%s modelo=%s, ate %d consulta(s) por run", c.Rota.Fornecedor, c.Rota.Modelo, c.MaxConsultasPorRun)
	}
	fmt.Fprintf(b, "CONSELHEIRO (bateria %s; sem veredicto; %s; os intervalos de 95%% estao no JSON)\n", c.VersaoDaBateria, rota)
	var fornecedores []string
	for f := range c.PiorCasoPorFornecedor {
		fornecedores = append(fornecedores, f)
	}
	sort.Strings(fornecedores)
	b.WriteString("  pior caso de pedidos por fornecedor (com as sondas):")
	for _, f := range fornecedores {
		fmt.Fprintf(b, " %s=%d", f, c.PiorCasoPorFornecedor[f])
	}
	b.WriteString("\n")
	if c.Pedidos != nil {
		fmt.Fprintf(b, "  pedidos ao conselheiro: previstos (maximo) %d, enviados %d", c.Pedidos.PrevistosMax, c.Pedidos.Enviados)
		if c.Pedidos.TectoDoDia != nil {
			fmt.Fprintf(b, "; dia %s: gastos %d de %d, restam %d (fornecedor do conselheiro)", c.Pedidos.DiaUTC, *c.Pedidos.GastosHoje, *c.Pedidos.TectoDoDia, *c.Pedidos.RestantesHoje)
		}
		b.WriteString("\n")
	}
	if c.Sonda != nil {
		fmt.Fprintf(b, "  sonda do conselheiro (1 pedido, conta no tecto dele): HTTP %d, resultado %s\n", c.Sonda.HTTP, c.Sonda.Resultado)
	}
	fmt.Fprintf(b, "  %-21s %-9s %-13s %-9s %-8s %-8s %-8s %-11s %-11s %s\n", "braco", "cumpridos", "interrompidas", "consulta", "cons/run", "ped.exec", "ped.cons", "USD exec", "USD cons", "USD por caso cumprido")
	for _, br := range c.Bracos {
		porRun := "-"
		if br.ConsultasPorRun != nil {
			porRun = fmt.Sprintf("%.2f", *br.ConsultasPorRun)
		}
		porCaso := usd(br.MicroUSDPorCasoCumprido)
		if br.MicroUSDTotal != nil && br.MicroUSDPorCasoCumprido == nil {
			porCaso = "- (nenhum caso cumprido)"
		}
		fmt.Fprintf(b, "  %-21s %-9s %-13s %-9s %-8s %-8d %-8d %-11s %-11s %s\n", br.Braco, fracao(br.CasosCumpridos),
			fmt.Sprintf("%d de %d", br.Interrompidas, br.Passagens), fracao(br.TaxaDeConsulta), porRun,
			br.Executor.Pedidos, br.Conselheiro.Pedidos, usd(br.Executor.MicroUSDEstimado), usd(br.Conselheiro.MicroUSDEstimado), porCaso)
	}
	b.WriteString("  por caso — cumpridos em cada braco (consultas pedidas):\n")
	for i, caso := range c.Bracos[0].PorCaso {
		fmt.Fprintf(b, "    %-4s", caso.Caso)
		for _, br := range c.Bracos {
			k := br.PorCaso[i]
			fmt.Fprintf(b, "  %s %-6s (%d)", br.Braco, fracao(k.Cumpridos), k.Consultas)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(b, "  REGRA DO CASO CUMPRIDO: %s\n\n", c.Regra)
}
