package bancoensaio

import (
	"fmt"
	"sort"
	"strings"
	"time"

	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
)

// O RESUMO DE UMA PÁGINA (AOS-522): o que o dono lê antes de assinar.
//
// É função pura do relatório e do que o `qualificar` preparou. Só leva contagens, digests,
// identificadores e valores de vocabulário fechado: nem texto de respostas, nem conteúdo de
// documentos, nem chaves, nem caminhos.
//
// A ORDEM É FIXA: os avisos (com `exigencia_nao_provada` antes de tudo, incluindo o veredicto);
// depois veredicto, razões, amostra, o que os controlos negativos mostraram, o que a corrida não
// prova, e o gasto; depois o que vai ser assinado, e o relógio. O que a corrida NÃO prova está na
// página que se assina, e não em rodapé: na A2 `cumprida` foi lida como prova de leitura e não era.

const (
	// LinhasDoResumoDeUmaPagina e ColunasDoResumoDeUmaPagina são o tamanho de uma página.
	LinhasDoResumoDeUmaPagina  = 60
	ColunasDoResumoDeUmaPagina = 100
)

// Os títulos das secções do resumo, pela ordem em que aparecem.
const (
	TituloDoVeredicto       = "1. VEREDICTO:"
	TituloDasRazoes         = "2. RAZOES:"
	TituloDaAmostra         = "3. AMOSTRA:"
	TituloDosControlos      = "4. CONTROLOS NEGATIVOS:"
	TituloDoQueNaoProva     = "5. O QUE ESTA CORRIDA NAO PROVA:"
	TituloDoGasto           = "6. GASTO:"
	TituloDoQueSeAssina     = "O QUE VAIS ASSINAR"
	TituloDeNadaParaAssinar = "NADA PARA ASSINAR"
	TituloDoRelogio         = "RELOGIO (UTC)"
	// LinhaDoControloNaoCorrido é o que o resumo diz de um controlo negativo que não foi feito —
	// nunca o mostra como passado.
	LinhaDoControloNaoCorrido = "CONTROLO NAO CORRIDO"
)

// DadosDoResumo é o que o resumo leva além do relatório: o que se preparou para assinar.
type DadosDoResumo struct {
	// Registo é o registo de entrada que o resumo mostra. Com Preparado, é o que foi LIDO DE VOLTA
	// DO DISCO, do ficheiro que ficou no pacote — o dono assina o que vê, e o que vê é o que está
	// escrito. Sem Preparado é o candidato, tal como passou na leitura do nó antes da corrida.
	Registo routeentry.Entry
	// Substitui diz que o alias já tem entrada na tabela de perfis em código DESTE binário, que o
	// perfil assinado substitui; ModeloEsperadoDeAntes é o modelo esperado dessa entrada. A tabela
	// do nó pode ser outra: RevisaoDoBinario diz de que revisão é esta.
	Substitui             bool
	ModeloEsperadoDeAntes string
	RevisaoDoBinario      string
	// Preparado diz que ficou um registo por assinar. Sem ele, Motivo diz porque não há nada para
	// assinar.
	Preparado bool
	Motivo    string
	// Fases é o relógio do comando.
	Fases []FaseMedida
}

// paramsDoPerfil escreve os parâmetros do pedido que o perfil declara, por ordem do nome — a
// mesma forma do resumo do `aos-issuer route-profile-sign`.
func paramsDoPerfil(p modelgateway.RouteProfile) string {
	if p.Params == nil || p.Params.IsZero() {
		return "nenhum"
	}
	m := p.Params.Declared()
	nomes := make([]string, 0, len(m))
	for k := range m {
		nomes = append(nomes, k)
	}
	sort.Strings(nomes)
	pares := make([]string, 0, len(nomes))
	for _, k := range nomes {
		pares = append(pares, k+"="+m[k])
	}
	return strings.Join(pares, ", ")
}

// partir parte um texto em linhas de, no máximo, `largura` colunas. A primeira linha mantém o recuo
// do texto; as de continuação levam `margem`. Parte nos espaços; uma palavra maior do que a linha
// é cortada.
func partir(texto, margem string, largura int) []string {
	prefixo := texto[:len(texto)-len(strings.TrimLeft(texto, " "))]
	var linhas []string
	actual := ""
	fechar := func() {
		linhas = append(linhas, prefixo+actual)
		prefixo, actual = margem, ""
	}
	for _, palavra := range strings.Fields(texto) {
		for palavra != "" {
			separador := ""
			if actual != "" {
				separador = " "
			}
			livre := largura - len([]rune(prefixo)) - len([]rune(actual))
			if len([]rune(separador+palavra)) <= livre {
				actual, palavra = actual+separador+palavra, ""
				continue
			}
			if actual != "" {
				fechar()
				continue
			}
			// A palavra sozinha não cabe numa linha: corta-se.
			letras := []rune(palavra)
			actual, palavra = string(letras[:livre]), string(letras[livre:])
			fechar()
		}
	}
	if actual != "" {
		linhas = append(linhas, prefixo+actual)
	}
	return linhas
}

// linhaDoControlo diz o que os controlos negativos mostraram. Um controlo que o perfil pedia e
// não está no relatório, que não correu, ou com um resultado que este leitor não conhece, sai
// como [LinhaDoControloNaoCorrido]: nunca como passado.
func linhaDoControlo(r *Relatorio) string {
	q := r.QualificacaoDoModelo
	pedido := r.Protocolo.Estado != nil && r.Protocolo.Estado.Devolver == modelgateway.StateReturnRequired
	if q.Controlo == nil {
		if pedido {
			return LinhaDoControloNaoCorrido + " — o perfil declara devolucao obrigatoria e o relatorio nao traz a corrida de controlo sem devolucao"
		}
		return "nao se aplica — o perfil nao declara devolucao obrigatoria: nao ha corrida de controlo sem devolucao. Os falsos maus (um por modo de falha) correm no CI do banco, e nao nesta corrida"
	}
	switch q.Controlo.Resultado {
	case ControloFalhouComoDevido:
		return "controlo sem devolucao: falhou_como_devido — sem o estado de volta, o fornecedor recusou todos os pedidos com mensagem tool. Mostra que o EXIGE; nao prova que o le"
	case ControloPassou:
		return "controlo sem devolucao: passou — sem o estado de volta correu tudo bem: o fornecedor NAO exige o estado, e a corrida principal nao prova que ele o le"
	case ControloInconclusivo:
		return "controlo sem devolucao: inconclusivo — correu e nao deu nem uma coisa nem outra"
	}
	return LinhaDoControloNaoCorrido + " — a corrida de controlo sem devolucao nao foi feita (a corrida principal nao deu qualificado)"
}

// ResumoDeUmaPagina devolve o resumo de uma página de uma qualificação. `r` tem de ser o
// relatório de uma experiência `qualificacao`.
func ResumoDeUmaPagina(r *Relatorio, d DadosDoResumo) string {
	q := r.QualificacaoDoModelo
	var linhas []string
	junta := func(texto, margem string) {
		linhas = append(linhas, partir(texto, margem, ColunasDoResumoDeUmaPagina)...)
	}
	// OS AVISOS, À CABEÇA. `exigencia_nao_provada` antes de todas as outras linhas.
	avisos := append([]string(nil), q.Avisos...)
	for i, a := range avisos {
		if a == AvisoExigenciaNaoProvada {
			avisos = append([]string{a}, append(avisos[:i:i], avisos[i+1:]...)...)
		}
	}
	for _, a := range avisos {
		frase, conhecido := frasesDosAvisos[a]
		if !conhecido {
			frase = "aviso que este resumo nao conhece; leia o relatorio"
		}
		junta(fmt.Sprintf("AVISO — %s: %s.", strings.ToUpper(a), frase), "  ")
	}
	if !q.ValeParaProducao {
		junta(fmt.Sprintf("ENSAIO SEM MODELO REAL (modo %s) — ESTE VEREDICTO NAO VALE PARA PRODUCAO: o fornecedor e um provider falso.", r.Modo), "  ")
	}
	junta("QUALIFICAR (AOS-522) — resumo de uma pagina, de "+r.DataUTC+". Le-o todo antes de assinar.", "  ")

	junta(fmt.Sprintf("%s %s  (modo %s; a corrida terminou: %s)", TituloDoVeredicto, strings.ToUpper(q.Veredicto), r.Modo, r.Terminou), "   ")
	razoes := "(nenhuma)"
	if len(q.Razoes) > 0 {
		razoes = strings.Join(q.Razoes, ", ")
	}
	junta(TituloDasRazoes+" "+razoes, "   ")
	c := q.Contagens
	violados := []string{}
	for _, l := range q.Limiares {
		if l.Violado {
			violados = append(violados, l.ID)
		}
	}
	limiares := "nenhum"
	if len(violados) > 0 {
		limiares = strings.Join(violados, ", ")
	}
	junta(fmt.Sprintf("%s %d de %d nos com tools (cumpridos %d); %d de %d nos sem tools (cumpridos %d); %d turnos, %d com o modelo esperado; limiares violados: %s",
		TituloDaAmostra, c.NosComToolsNaAmostra, c.NosComToolsExigidos, c.NosComToolsCumpridos,
		c.NosSemToolsNaAmostra, c.NosSemToolsExigidos, c.NosSemToolsCumpridos, c.Turnos, c.TurnosComModeloIgual, limiares), "   ")
	junta(TituloDosControlos+" "+linhaDoControlo(r), "   ")
	linhas = append(linhas, TituloDoQueNaoProva)
	for _, n := range q.NaoProva {
		junta("   - "+strings.ReplaceAll(n, "_", " "), "     ")
	}
	o := q.Orcamento
	gasto := fmt.Sprintf("%s %d pedidos enviados (tecto %d; pior caso declarado antes de enviar: %d)", TituloDoGasto, o.PedidosEnviados, o.TectoDePedidos, o.PiorCasoDeclarado)
	if o.MicroUSDEstimado != nil {
		gasto += fmt.Sprintf("; gasto ESTIMADO %.4f USD (tecto %.2f)", float64(*o.MicroUSDEstimado)/1e6, float64(o.TectoMicroUSD)/1e6)
	} else {
		gasto += "; sem estimativa em dolares"
	}
	if o.QualificacoesDoPerfilHoje != nil && o.QualificacoesDaRotaHoje != nil {
		gasto += fmt.Sprintf("; hoje: qualificacao %d de %d do perfil, %d de %d da rota", *o.QualificacoesDoPerfilHoje, o.MaximoPorPerfilPorDia, *o.QualificacoesDaRotaHoje, o.MaximoPorRotaPorDia)
	}
	junta(gasto, "   ")

	// O QUE SE ASSINA — ou porque não há nada para assinar. TUDO o que o registo diz, e a partir
	// do registo: o perfil inteiro (os parâmetros do pedido e a versão da projecção mudam o que vai
	// para o provider), o ambiente, a fatia e os digests.
	e := d.Registo
	p := e.Profile
	if d.Preparado {
		junta(TituloDoQueSeAssina+" — o registo de entrada, lido de volta do pacote. Assinas tu, com o aos-issuer.", "   ")
	} else {
		junta(TituloDeNadaParaAssinar+" — "+d.Motivo+". So um qualificado do modo real, com a corrida completa, deixa um registo de entrada. O candidato era:", "   ")
	}
	junta(fmt.Sprintf("   alias pedido: %q; modelo esperado: %q; fatia inicial: %d%%", p.Requested, p.ExpectedModel, e.InitialSharePercent), "     ")
	junta(fmt.Sprintf("   board: %q; regiao: %q", e.Board, e.Region), "     ")
	projeccao := p.ProjectionVersion
	if projeccao == "" {
		projeccao = "nao declara (vale a do no)"
	}
	junta(fmt.Sprintf("   wire: %q; capacidades: %v; projection_version: %s; devolver: nunca", p.WireClass, p.Capabilities, projeccao), "     ")
	junta("   params do pedido: "+paramsDoPerfil(p), "     ")
	tabela := fmt.Sprintf("segundo a tabela em codigo DESTE binario (revisao %s), que pode nao ser a do no", d.RevisaoDoBinario)
	if d.Substitui {
		junta(fmt.Sprintf("   SUBSTITUI a entrada deste alias na tabela de perfis, para o no inteiro (modelo esperado de antes: %q) — %s", d.ModeloEsperadoDeAntes, tabela), "     ")
	} else {
		junta("   ACRESCENTA-SE a tabela de perfis, que nao tem este alias — "+tabela, "     ")
	}
	relatorio, imagem := e.ReportDigest, e.ProxyImageDigest
	if !d.Preparado {
		// Sem registo escrito, o digest do relatório e o da imagem são os que o relatório traz.
		relatorio, imagem = r.DigestDoRelatorio, q.Rota.DigestDaImagemDoProxy
		if imagem == "" {
			imagem = "(modo falso: sem proxy)"
		}
	}
	linhas = append(linhas,
		"   digest do perfil:    "+e.ProfileDigest,
		"   digest do relatorio: "+relatorio,
		"   digest imagem proxy: "+imagem)
	if d.Preparado {
		linhas = append(linhas, "   digest do registo:   "+e.EntryDigest)
	}

	// O RELÓGIO: a hora de início e de fim de cada fase, para a medição do critério da fase.
	if len(d.Fases) > 0 {
		primeira, ultima := d.Fases[0], d.Fases[len(d.Fases)-1]
		relogio := fmt.Sprintf("%s — total %s -> %s (%s):", TituloDoRelogio, primeira.Inicio.Format("15:04:05"), ultima.Fim.Format("15:04:05"), ultima.Fim.Sub(primeira.Inicio).Round(time.Second))
		for i, f := range d.Fases {
			if i > 0 {
				relogio += ";"
			}
			relogio += fmt.Sprintf(" %s %s->%s (%s)", f.Nome, f.Inicio.Format("15:04:05"), f.Fim.Format("15:04:05"), f.Fim.Sub(f.Inicio).Round(time.Second))
		}
		junta(relogio, "   ")
	}
	return strings.Join(linhas, "\n") + "\n"
}
