package main

// A SAÍDA POR REFERÊNCIA, MEDIDA (AOS-499, ADR-038 §2.1 e §2.4).
//
// O kernel do nó `aos` designa e sela a origem da saída de um run — o resultado da chamada
// efectiva da tool declarada (AOS-497) — e o nó aceita a declaração e devolve a âncora (AOS-498).
// Este ficheiro é a metade do `aos-orq` que MEDE, antes de alguém entregar por referência
// (AOS-501):
//
//   - um interruptor, `AOS_ORQ_SAIDA_POR_REFERENCIA`, desligado por omissão;
//   - em `observe`, os nós CANDIDATOS POR ESTRUTURA declaram a origem ao nó `aos`, sempre com o
//     vínculo «só medição», e só a um nó que anuncie suportá-lo;
//   - o que o kernel designou vai para o log da drenagem e para o ficheiro de métricas.
//
// O QUE NÃO MUDA. Nada aqui decide coisa nenhuma: o nó do plano conclui pela regra de sempre, e a
// saída que se publica e se entrega ao consumidor é o TEXTO FINAL do run, com o digest do texto
// final. Os bytes designados que o nó devolve não são guardados, publicados nem entregues. Com o
// vínculo «só medição» o kernel dá ao run o desfecho que ele teria sem declaração, qualquer que
// seja o modo do nó.
//
// SEM CONTEÚDO. O log e as métricas levam estados, classes e números. Não levam o texto final,
// os bytes designados, nem nenhum valor que o nó tenha devolvido sem passar por um vocabulário
// fechado deste ficheiro.

import (
	"errors"
	"fmt"
	"os"
	"strings"

	plan "github.com/aos-ref/control-plane/orchestrator/plan"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// Modos do interruptor da saída por referência.
const (
	// saidaPorReferenciaOff — desligado: nada é declarado nem medido. É a omissão.
	saidaPorReferenciaOff = "off"
	// saidaPorReferenciaObserve — os candidatos por estrutura declaram a origem com o vínculo
	// «só medição»; a entrega é a de hoje.
	saidaPorReferenciaObserve = "observe"
	// saidaPorReferenciaOn — a entrega por referência. NÃO existe neste binário (AOS-501).
	saidaPorReferenciaOn = "on"
)

// ErrSaidaPorReferencia — o interruptor tem um valor que este binário não aceita.
var ErrSaidaPorReferencia = errors.New("aos-orq: AOS_ORQ_SAIDA_POR_REFERENCIA invalido")

// modoDaSaidaPorReferenciaDoAmbiente lê o interruptor. Vazio ⇒ `off`. Um valor desconhecido
// recusa o arranque, e `on` também: prometer a entrega por referência e fazer a de sempre era
// pior do que não arrancar.
//
// O `os.Getenv` é LITERAL de propósito (ver [tectoDoPlanoDoAmbiente]): o gate da superfície de
// variáveis de ambiente enumera-as pelo nome escrito no código.
func modoDaSaidaPorReferenciaDoAmbiente() (string, error) {
	return modoDaSaidaPorReferencia(strings.TrimSpace(os.Getenv("AOS_ORQ_SAIDA_POR_REFERENCIA")))
}

// modoDaSaidaPorReferencia valida um valor JÁ LIDO. O valor recusado não vai na mensagem.
func modoDaSaidaPorReferencia(bruto string) (string, error) {
	switch bruto {
	case "", saidaPorReferenciaOff:
		return saidaPorReferenciaOff, nil
	case saidaPorReferenciaObserve:
		return saidaPorReferenciaObserve, nil
	case saidaPorReferenciaOn:
		return "", fmt.Errorf("%w: `on` (a entrega por referencia) ainda nao existe neste binario — e do AOS-501; os valores aceites sao off e observe", ErrSaidaPorReferencia)
	default:
		return "", fmt.Errorf("%w: os valores aceites sao off (a omissao) e observe", ErrSaidaPorReferencia)
	}
}

// Classes estruturais de um nó do plano (AOS-499). Vocabulário FECHADO: é o que cada nó diz no
// log da drenagem e o rótulo `classe` de [metricaNosPorEstrutura].
const (
	// classeCandidato — pela ESTRUTURA, a saída do nó pode ser o resultado da sua tool.
	classeCandidato = "candidato"
	// classeNaoCandidato — todos os outros.
	classeNaoCandidato = "nao_candidato"
)

// classesEstruturais é a lista fechada das classes.
var classesEstruturais = []string{classeCandidato, classeNaoCandidato}

// classeEstrutural diz se o nó é um CANDIDATO ESTRUTURAL à saída por referência:
//
//   - não é verificador;
//   - tem exactamente UMA tool pinada (`tools`, os nomes da lista-branca do run);
//   - declara exactamente UMA saída de forma aberta — a que hoje se publica do texto final;
//   - não tem `consumes`: um nó com entradas tem o contexto untrusted desde o turno 1 e nunca
//     tem origem (ADR-038 §2.2).
//
// É INFERÊNCIA, E SERVE SÓ PARA MEDIR (ADR-038 §2.1). Um nó «lê e extrai as acções» tem esta
// mesma estrutura e não é de passagem directa: decidir a entrega por aqui mudava o conteúdo de
// uma aresta de um plano aprovado sem que ninguém o tivesse aprovado. A origem de uma saída
// declara-se no plano (AOS-500).
func classeEstrutural(n plan.Node, tools []string) string {
	if n.IsVerifier() || len(tools) != 1 || len(n.Consumes) != 0 {
		return classeNaoCandidato
	}
	abertas := 0
	for _, o := range n.Outputs {
		if !o.Type.ClosedForm() {
			abertas++
		}
	}
	if abertas != 1 {
		return classeNaoCandidato
	}
	return classeCandidato
}

// origemDeclaravel diz se este pedido pode levar a declaração de origem sem lhe acrescentar uma
// razão para o run NÃO ARRANCAR. «Só medição» não muda o desfecho de um run que arranca, mas uma
// declaração impossível recusa o arranque nos dois vínculos (ADR-038 §2.4). Por isso só se
// declara quando:
//
//   - o pedido já leva o contrato de conclusão sobre a MESMA tool: se ela não existir no nó, o
//     run já era recusado pelo contrato, e a declaração não acrescenta recusa nenhuma;
//   - o nome tem a forma que a âncora selada admite — perguntado à função do selo, a mesma que o
//     kernel e a porta do nó usam. É a única condição da origem que o contrato não tem.
//
// A pertença à lista-branca é por construção: a tool declarada é a única da lista do pedido.
func origemDeclaravel(tool string, contrato []string) bool {
	noContrato := false
	for _, c := range contrato {
		if c == tool {
			noContrato = true
		}
	}
	forma := agentruntime.OutputSource{Tool: tool, Binding: agentruntime.OutputSourceMeasure, State: agentruntime.OutputSourceMissing}
	return noContrato && forma.BemFormada()
}

// bannerDaSaidaPorReferencia declara, no arranque do `serve`, o modo do interruptor e o que ele
// faz contra ESTE nó. Só se chama com um anúncio que se leu.
func bannerDaSaidaPorReferencia(modo string, a anuncioDoNo) string {
	switch {
	case modo != saidaPorReferenciaObserve:
		return "saida por referencia (AOS-499): modo off — nenhum no declara a origem da saida e nada e medido. A saida de cada no e o texto final do run, como sempre (AOS_ORQ_SAIDA_POR_REFERENCIA=observe mede)"
	case !a.origem:
		return "saida por referencia (AOS-499): modo observe, NAO MEDIDO — o no nao anuncia o suporte (GET /tools sem output_source: no anterior ao AOS-498, ou com o veredicto desligado). Os nos sao submetidos sem a declaracao, como antes: enviar o campo dava 400"
	default:
		return "saida por referencia (AOS-499): modo observe — cada no candidato por estrutura (nao-verificador, uma tool, uma saida aberta, sem consumes) declara a origem da saida com o vinculo measure (so medicao). O kernel do no designa e sela a ancora; o desfecho de cada run e a saida publicada sao os de sempre (o texto final)"
	}
}

// Estados da designação tal como o `aos-orq` os conta — o rótulo `estado` de
// [metricaOrigemDesignacao]. São os do kernel ([agentruntime.OutputSourceStates]) e um deste
// ficheiro.
const estadoNaoMedido = "nao_medido"

// estadosDaDesignacao é a lista fechada do rótulo `estado`.
func estadosDaDesignacao() []string {
	estados := []string{}
	for _, e := range agentruntime.OutputSourceStates() {
		estados = append(estados, string(e))
	}
	return append(estados, estadoNaoMedido)
}

// Classes de tamanho do resultado designado, contra o tecto de transporte de um payload
// ([maxPayloadBytes], 128 KiB) — o rótulo `classe` de [metricaOrigemTamanho].
const (
	tamanhoVazio     = "vazio"
	tamanhoAte1K     = "ate_1k"
	tamanhoAte16K    = "ate_16k"
	tamanhoAte128K   = "ate_128k"
	tamanhoAcima128K = "acima_128k"
)

var classesDeTamanho = []string{tamanhoVazio, tamanhoAte1K, tamanhoAte16K, tamanhoAte128K, tamanhoAcima128K}

// classeDeTamanho põe um tamanho em bytes numa das classes.
func classeDeTamanho(n int) string {
	switch {
	case n <= 0:
		return tamanhoVazio
	case n <= 1<<10:
		return tamanhoAte1K
	case n <= 16<<10:
		return tamanhoAte16K
	case n <= maxPayloadBytes:
		return tamanhoAte128K
	default:
		return tamanhoAcima128K
	}
}

// Classes da razão entre o tamanho do TEXTO FINAL e o tamanho do RESULTADO DESIGNADO — o rótulo
// `classe` de [metricaOrigemRazao]. Abaixo de 1 o modelo escreveu menos do que leu (um resumo);
// perto de 1, uma transcrição; acima, acrescentou.
const (
	razaoOrigemVazia = "origem_vazia"
	razaoTextoVazio  = "texto_vazio"
	razaoAbaixoDe05  = "abaixo_de_0_5"
	razaoDe05A09     = "de_0_5_a_0_9"
	razaoDe09A11     = "de_0_9_a_1_1"
	razaoDe11A2      = "de_1_1_a_2"
	razaoAcimaDe2    = "acima_de_2"
)

var classesDeRazao = []string{razaoOrigemVazia, razaoTextoVazio, razaoAbaixoDe05, razaoDe05A09, razaoDe09A11, razaoDe11A2, razaoAcimaDe2}

// classeDeRazao põe a razão texto/origem numa das classes. Só tamanhos: nenhum conteúdo é lido.
func classeDeRazao(texto, origem int) string {
	switch {
	case origem <= 0:
		return razaoOrigemVazia
	case texto <= 0:
		return razaoTextoVazio
	}
	// Em décimos, com inteiros: 10*texto/origem.
	switch decimos := 10 * texto / origem; {
	case decimos < 5:
		return razaoAbaixoDe05
	case decimos < 9:
		return razaoDe05A09
	case 10*texto <= 11*origem:
		return razaoDe09A11
	case decimos < 20:
		return razaoDe11A2
	default:
		return razaoAcimaDe2
	}
}

// O texto final É ou NÃO É o resultado designado — o rótulo `comparacao` de
// [metricaOrigemTextoFinal]. Decide-se por DIGESTS: `sha256(texto final)`, calculado aqui, contra
// o digest da âncora que o kernel selou. O conteúdo não é comparado nem escrito.
const (
	textoIgualAOrigem      = "igual"
	textoDiferenteDaOrigem = "diferente"
)

var comparacoesDoTexto = []string{textoIgualAOrigem, textoDiferenteDaOrigem}

// O que o nó `aos` fez dos bytes designados na resposta — o rótulo `resultado` de
// [metricaOrigemTransporte]. É o que a entrega por referência (AOS-501) vai encontrar.
const (
	// transporteConfere — o nó serviu `output` e `sha256(output)` é o digest da âncora.
	transporteConfere = "servido_confere"
	// transporteNaoConfere — o nó serviu `output` e o digest NÃO é o da âncora.
	transporteNaoConfere = "servido_nao_confere"
	// transporteAusente — âncora designada, sem `output` e sem marca: resposta que este binário
	// não sabe ler.
	transporteAusente = "ausente"
	// As quatro marcas do nó (`output_omitted`), tal qual.
	transporteGrande       = "too_large"
	transporteNaoTexto     = "not_utf8"
	transporteIndisponivel = "unavailable"
	transporteAgoraNao     = "unavailable_now"
)

var resultadosDoTransporte = []string{
	transporteConfere, transporteNaoConfere, transporteGrande, transporteNaoTexto,
	transporteIndisponivel, transporteAgoraNao, transporteAusente,
}

// medidaDaOrigem é o que se mediu num nó candidato. Todos os campos são de vocabulário fechado ou
// números.
type medidaDaOrigem struct {
	estado string
	// Só com a origem designada:
	bytes      int
	tamanho    string
	razao      string
	comparacao string
	transporte string
}

// medirOrigem reduz a resposta do nó `aos` à medida. NÃO DECIDE NADA e não devolve conteúdo: lê a
// âncora, o comprimento e o digest do texto final, e — se o nó serviu `output` — confere-o contra
// o digest da âncora.
//
// O que vem do nó passa por vocabulários fechados: um estado, um vínculo ou uma marca que este
// binário não conhece contam como «não medido» ou «ausente», sem serem repetidos.
func medirOrigem(st estadoDoRun) medidaDaOrigem {
	a := st.OutputSource
	if a == nil || !a.BemFormada() || a.Binding != agentruntime.OutputSourceMeasure {
		// Sem âncora (o run não arrancou, ou o nó já não a tem), com uma âncora que o kernel não
		// produziria, ou com um vínculo que este binário não enviou: não há medida a registar.
		return medidaDaOrigem{estado: estadoNaoMedido}
	}
	m := medidaDaOrigem{estado: string(a.State)}
	if a.State != agentruntime.OutputSourceDesignated {
		return m
	}
	m.bytes = a.Bytes
	m.tamanho = classeDeTamanho(a.Bytes)
	m.razao = classeDeRazao(len(st.FinalText), a.Bytes)
	m.comparacao = textoDiferenteDaOrigem
	if digestDoConteudo(st.FinalText) == a.Digest {
		m.comparacao = textoIgualAOrigem
	}
	switch {
	case st.Output != nil && digestDoConteudo(*st.Output) == a.Digest:
		m.transporte = transporteConfere
	case st.Output != nil:
		m.transporte = transporteNaoConfere
	default:
		m.transporte = transporteAusente
		for _, marca := range []string{transporteGrande, transporteNaoTexto, transporteIndisponivel, transporteAgoraNao} {
			if st.OutputOmitted == marca {
				m.transporte = marca
			}
		}
	}
	return m
}

// linha escreve a medida para o log da drenagem. Só vocabulário fechado e números.
func (m medidaDaOrigem) linha(texto int) string {
	if m.estado != string(agentruntime.OutputSourceDesignated) {
		return "estado=" + m.estado
	}
	return fmt.Sprintf("estado=%s bytes_da_origem=%d (%s) bytes_do_texto=%d razao=%s texto_final=%s transporte=%s",
		m.estado, m.bytes, m.tamanho, texto, m.razao, m.comparacao, m.transporte)
}

// noPorEstrutura conta um nó do plano submetido, pela sua classe estrutural.
func (m *medicaoDoContrato) noPorEstrutura(classe string) {
	if m == nil {
		return
	}
	if m.estruturas == nil {
		m.estruturas = map[string]int{}
	}
	m.estruturas[classe]++
}

// origemMedida conta o que se mediu num nó candidato.
func (m *medicaoDoContrato) origemMedida(o medidaDaOrigem) {
	if m == nil {
		return
	}
	contar := func(mapa *map[string]int, valor string) {
		if valor == "" {
			return
		}
		if *mapa == nil {
			*mapa = map[string]int{}
		}
		(*mapa)[valor]++
	}
	contar(&m.designacoes, o.estado)
	contar(&m.tamanhos, o.tamanho)
	contar(&m.razoes, o.razao)
	contar(&m.comparacoes, o.comparacao)
	contar(&m.transportes, o.transporte)
}

// registarOrigem soma às séries o que um `serve` mediu sobre a saída por referência. Só escreve
// rótulos do vocabulário fechado.
func (m *metricasDoConsumo) registarOrigem(c *medicaoDoContrato) {
	if c == nil {
		return
	}
	somar := func(metrica, rotulo string, fechado []string, valores map[string]int) {
		for _, v := range fechado {
			if n := valores[v]; n > 0 {
				m.somar(serie(metrica, rotulo, v), float64(n))
			}
		}
	}
	somar(metricaNosPorEstrutura, "classe", classesEstruturais, c.estruturas)
	somar(metricaOrigemDesignacao, "estado", estadosDaDesignacao(), c.designacoes)
	somar(metricaOrigemTamanho, "classe", classesDeTamanho, c.tamanhos)
	somar(metricaOrigemRazao, "classe", classesDeRazao, c.razoes)
	somar(metricaOrigemTextoFinal, "comparacao", comparacoesDoTexto, c.comparacoes)
	somar(metricaOrigemTransporte, "resultado", resultadosDoTransporte, c.transportes)
}
