package main

// A SAÍDA POR REFERÊNCIA: O INTERRUPTOR E A MEDIÇÃO (AOS-499, ADR-038 §2.1 e §2.4).
//
// Desde o AOS-501 o interruptor tem um terceiro valor, `on`, que liga a ENTREGA por referência
// das saídas que o plano declara (entrega_por_referencia.go). O que este ficheiro descreve a
// seguir é o modo `observe`, que continua a só medir.
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
// SEM CONTEÚDO. O log e as métricas levam estados, classes e tamanhos. Não levam o texto final,
// os bytes designados, nem nenhuma linha, número ou digest deles — nem nenhum valor que o nó
// tenha devolvido sem passar por um vocabulário fechado deste ficheiro.
//
// O QUE SE COMPARA (revisão adversarial de 2026-10-05, achado I1). Em produção o resultado
// designado não é o documento: é o ENVELOPE que a sandbox escreve (`stdout_text`, `exit_code`, e
// o ficheiro lido outra vez em `artifacts`). A primeira versão comparava o texto final com esse
// envelope — por digest e por tamanho — e por isso dizia «diferente» de uma transcrição fiel, e
// classificava como resumo um documento curto transcrito byte a byte. A medição passa a
// DESEMBRULHAR o conteúdo ([desembrulharEnvelope]) e a comparar conteúdo com conteúdo
// ([relacaoComOConteudo]), dentro deste processo, publicando só as classes.

import (
	"encoding/json"
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
	// saidaPorReferenciaOn — a ENTREGA por referência (AOS-501): um plano que declare a origem de
	// uma saída corre, e o nó seguinte recebe o resultado designado pelo kernel. Não mede por
	// estrutura: quem diz que uma saída é por referência é o plano.
	saidaPorReferenciaOn = "on"
)

// ErrSaidaPorReferencia — o interruptor tem um valor que este binário não aceita.
var ErrSaidaPorReferencia = errors.New("aos-orq: AOS_ORQ_SAIDA_POR_REFERENCIA invalido")

// modoDaSaidaPorReferenciaDoAmbiente lê o interruptor. Vazio ⇒ `off`. Um valor desconhecido
// recusa o arranque.
//
// OS ESPAÇOS NAS PONTAS APARAM-SE, como nas outras variáveis deste binário: ` observe ` (um valor
// com um espaço ou uma quebra de linha a mais num `.env`) é `observe`. As maiúsculas NÃO se
// dobram: `Observe` é um valor desconhecido e recusa o arranque. Só espaços é vazio — `off`.
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
		return saidaPorReferenciaOn, nil
	default:
		return "", fmt.Errorf("%w: os valores aceites sao off (a omissao), observe e on", ErrSaidaPorReferencia)
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
	case modo == saidaPorReferenciaOn:
		return bannerDaEntrega(posturaDe(modo, a))
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

// Classes de tamanho do resultado designado TAL COMO SE TRANSPORTA, contra o tecto de transporte
// de um payload ([maxPayloadBytes], 128 KiB) — o rótulo `classe` de [metricaOrigemTamanho]. Com a
// tool a correr na sandbox é o tamanho do ENVELOPE, e não o do documento: a leitura de um
// ficheiro leva-o em `stdout_text` e outra vez, em base64, em `artifacts` — mais do dobro. `vazio`
// só acontece com uma tool que devolva zero bytes; um documento vazio lido na sandbox são os
// bytes do envelope.
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

// Formas do resultado designado — o rótulo `forma` de [metricaOrigemForma]. Diz o que é que o nó
// serviu em `output`, e portanto COM O QUE é que o texto final se pôde comparar.
const (
	// formaEnvelope — os bytes servidos têm a forma do envelope da sandbox, com `exit_code` zero
	// e o stdout em texto: o conteúdo comparado é o `stdout_text` (o documento).
	formaEnvelope = "envelope"
	// formaEnvelopeFalhou — envelope com `exit_code` diferente de zero: a tool correu e falhou. É
	// uma chamada efectiva e o kernel designa-a; o stdout de uma falha não é o documento, e não
	// se compara.
	formaEnvelopeFalhou = "envelope_exit_nao_zero"
	// formaEnvelopeBinario — envelope com o stdout em base64 (não é UTF-8). Não é texto que se
	// compare com um texto final.
	formaEnvelopeBinario = "envelope_binario"
	// formaCru — os bytes servidos NÃO são um envelope reconhecível: a tool não corre na sandbox,
	// ou a forma do envelope mudou. O conteúdo comparado são os bytes tal como vieram.
	formaCru = "cru"
	// formaSemBytes — o nó não serviu bytes que confiram com a âncora (ver [metricaOrigemTransporte]
	// para a causa). Não há conteúdo para comparar.
	formaSemBytes = "sem_bytes"
)

var formasDaOrigem = []string{formaEnvelope, formaEnvelopeFalhou, formaEnvelopeBinario, formaCru, formaSemBytes}

// chavesDoEnvelope são os campos do envelope que a sandbox escreve (`resultDTO` em
// `substrate/sandbox/mediated.go`). Um objecto com qualquer outra chave não é esse envelope.
var chavesDoEnvelope = map[string]bool{"stdout_text": true, "stdout": true, "artifacts": true, "exit_code": true}

// desembrulharEnvelope reconhece nos bytes servidos pelo nó a forma do envelope da sandbox e
// devolve o conteúdo do envelope e a forma.
//
// TEM DOIS CHAMADORES. A medição do AOS-499 ([medirOrigem]) compara o conteúdo com o texto final
// e não o publica. A ENTREGA do AOS-501 ([extrairEntrega]) usa a MESMA leitura para derivar o
// que o nó seguinte recebe: com o interruptor em `on`, o conteúdo que esta função devolve para
// um envelope com `exit_code` zero É o que se publica e se entrega. As duas têm de ler o
// envelope da mesma maneira — é por isso que é uma só função.
//
// # A forma exacta, lida de forma tolerante
//
// O codificador da sandbox não é exportado e este binário não depende do pacote dela; por isso
// descodifica aqui só o que precisa, e exige o que distingue o envelope de outro JSON qualquer:
//
//   - um objecto JSON, sem nada depois dele;
//   - com `exit_code` inteiro — o único campo que o codificador escreve sempre;
//   - só com as chaves do envelope (`stdout_text`, `stdout`, `artifacts`, `exit_code`);
//   - `stdout_text` texto e `stdout` base64, e nunca os dois com conteúdo (o descodificador da
//     sandbox recusa esse resultado por ambíguo).
//
// Tudo o resto é [formaCru], e compara-se com os bytes tal como vieram. Reconhecer a forma não
// prova que os bytes vieram da sandbox: uma tool que devolva `{"exit_code":0}` por conta própria
// conta como envelope vazio. Os ficheiros de fio `substrate/sandbox/testdata/aos499_envelope/`
// prendem esta leitura ao codificador real: se a forma mudar, a classe passa a `cru` e o teste
// que os consome falha.
//
// Os `artifacts` não se comparam: na leitura de um documento são o mesmo ficheiro outra vez.
func desembrulharEnvelope(servido string) (conteudo, forma string) {
	var campos map[string]json.RawMessage
	if err := json.Unmarshal([]byte(servido), &campos); err != nil || campos == nil {
		return servido, formaCru
	}
	for chave := range campos {
		if !chavesDoEnvelope[chave] {
			return servido, formaCru
		}
	}
	var (
		saida  int
		texto  string
		binary []byte
	)
	if cru, tem := campos["exit_code"]; !tem || json.Unmarshal(cru, &saida) != nil {
		return servido, formaCru
	}
	if cru, tem := campos["stdout_text"]; tem && json.Unmarshal(cru, &texto) != nil {
		return servido, formaCru
	}
	if cru, tem := campos["stdout"]; tem && json.Unmarshal(cru, &binary) != nil {
		return servido, formaCru
	}
	switch {
	case texto != "" && len(binary) > 0:
		return servido, formaCru
	case saida != 0:
		return "", formaEnvelopeFalhou
	case len(binary) > 0:
		return "", formaEnvelopeBinario
	default:
		return texto, formaEnvelope
	}
}

// Relação entre o TEXTO FINAL e o CONTEÚDO do resultado designado — o rótulo `comparacao` de
// [metricaOrigemTextoFinal]. Classes FECHADAS e mutuamente exclusivas, da mais forte para a mais
// fraca; decide-se pela primeira que se verifica. «Normalizar» é reduzir cada sequência de
// espaços, tabulações e quebras de linha a um espaço e aparar as pontas.
const (
	// comparacaoConteudoVazio — o conteúdo não tem nada além de espaços. Não há o que perder.
	comparacaoConteudoVazio = "conteudo_vazio"
	// comparacaoTextoVazio — o conteúdo tem alguma coisa e o texto final não tem nada.
	comparacaoTextoVazio = "texto_vazio"
	// comparacaoIgual — o texto final É o conteúdo, depois de normalizar: uma transcrição.
	comparacaoIgual = "igual"
	// comparacaoContem — o texto final contém o conteúdo INTEIRO e seguido, depois de normalizar:
	// uma transcrição com moldura (uma frase antes, um fecho depois).
	comparacaoContem = "contem"
	// As quatro seguintes contam a fracção das linhas não vazias do conteúdo que aparecem no
	// texto final (cada linha normalizada, procurada no texto normalizado): todas (mas não
	// seguidas, ou com outra ordem), pelo menos 0,9, pelo menos 0,5, menos de 0,5.
	comparacaoLinhasTodas    = "linhas_todas"
	comparacaoLinhasDe09     = "linhas_de_0_9_a_1"
	comparacaoLinhasDe05     = "linhas_de_0_5_a_0_9"
	comparacaoLinhasAbaixo05 = "linhas_abaixo_de_0_5"
	// naoComparado — não houve conteúdo de texto para comparar (ver a `forma`). É também classe
	// das outras duas séries de conteúdo, para as três somarem o mesmo.
	naoComparado = "nao_comparado"
)

var comparacoesDoTexto = []string{
	comparacaoIgual, comparacaoContem, comparacaoLinhasTodas, comparacaoLinhasDe09, comparacaoLinhasDe05,
	comparacaoLinhasAbaixo05, comparacaoTextoVazio, comparacaoConteudoVazio, naoComparado,
}

// Os NÚMEROS do conteúdo no texto final — o rótulo `resultado` de [metricaOrigemNumeros]. Um
// número é uma sequência de dígitos ASCII; conta-se cada número distinto do conteúdo, e está
// presente se o texto final tem a MESMA sequência (inteira: `12` não está em `2012`).
const (
	// numerosTodos — todos os números do conteúdo aparecem no texto final.
	numerosTodos = "todos"
	// numerosEmFalta — pelo menos um número do conteúdo não aparece no texto final.
	numerosEmFalta = "em_falta"
	// numerosNenhum — o conteúdo não tem números.
	numerosNenhum = "sem_numeros"
)

var resultadosDosNumeros = []string{numerosTodos, numerosEmFalta, numerosNenhum, naoComparado}

// maxProcurasDeLinha limita o trabalho de [relacaoComOConteudo] sobre conteúdo que este binário
// não controla. Uma linha do conteúdo que seja também uma linha inteira do texto final decide-se
// por um conjunto; só as outras se procuram dentro do texto, e só as primeiras
// `maxProcurasDeLinha` — as restantes contam como ausentes. Com o tecto de transporte de 128 KiB
// isto limita a comparação a poucas centenas de MiB percorridos no pior caso construído.
const maxProcurasDeLinha = 4096

// normalizarEspacos reduz cada sequência de espaços a um espaço e apara as pontas.
func normalizarEspacos(s string) string { return strings.Join(strings.Fields(s), " ") }

// numerosDe devolve o conjunto das sequências de dígitos ASCII de um texto.
func numerosDe(s string) map[string]struct{} {
	conjunto := map[string]struct{}{}
	inicio := -1
	for i := 0; i <= len(s); i++ {
		digito := i < len(s) && s[i] >= '0' && s[i] <= '9'
		switch {
		case digito && inicio < 0:
			inicio = i
		case !digito && inicio >= 0:
			conjunto[s[inicio:i]] = struct{}{}
			inicio = -1
		}
	}
	return conjunto
}

// relacaoComOConteudo compara o texto final com o conteúdo do resultado designado e devolve DUAS
// classes: a da relação entre os textos e a dos números. É a única função deste ficheiro que lê
// conteúdo, e NÃO DEVOLVE CONTEÚDO: só constantes do vocabulário fechado.
//
// # O que as classes concluem, e o que não concluem
//
// `igual`, `contem` e `linhas_todas` dizem que o documento está no texto final. As classes de
// fracção e `em_falta` dizem que o texto final NÃO traz, letra a letra, tudo o que o documento
// tinha — o que acontece num resumo, mas também numa transcrição que o modelo reformatou (uma
// data escrita por extenso, um número com separador de milhares, uma lista renumerada). São um
// limite superior à perda, não uma prova de perda: `todos` e `linhas_todas` são evidência forte de
// fidelidade; `em_falta` e uma fracção baixa pedem que se vá ver.
func relacaoComOConteudo(textoFinal, conteudo string) (comparacao, numeros string) {
	doConteudo, doTexto := numerosDe(conteudo), numerosDe(textoFinal)
	numeros = numerosTodos
	if len(doConteudo) == 0 {
		numeros = numerosNenhum
	}
	for n := range doConteudo {
		if _, tem := doTexto[n]; !tem {
			numeros = numerosEmFalta
			break
		}
	}

	texto, alvo := normalizarEspacos(textoFinal), normalizarEspacos(conteudo)
	switch {
	case alvo == "":
		return comparacaoConteudoVazio, numeros
	case texto == "":
		return comparacaoTextoVazio, numeros
	case texto == alvo:
		return comparacaoIgual, numeros
	case strings.Contains(texto, alvo):
		return comparacaoContem, numeros
	}
	linhasDoTexto := map[string]struct{}{}
	for _, l := range strings.Split(textoFinal, "\n") {
		if n := normalizarEspacos(l); n != "" {
			linhasDoTexto[n] = struct{}{}
		}
	}
	total, presentes, procuras := 0, 0, 0
	for _, l := range strings.Split(conteudo, "\n") {
		n := normalizarEspacos(l)
		if n == "" {
			continue
		}
		total++
		if _, inteira := linhasDoTexto[n]; inteira {
			presentes++
			continue
		}
		if procuras < maxProcurasDeLinha {
			procuras++
			if strings.Contains(texto, n) {
				presentes++
			}
		}
	}
	switch {
	case presentes == total:
		return comparacaoLinhasTodas, numeros
	case 10*presentes >= 9*total:
		return comparacaoLinhasDe09, numeros
	case 2*presentes >= total:
		return comparacaoLinhasDe05, numeros
	default:
		return comparacaoLinhasAbaixo05, numeros
	}
}

// Classes da razão entre o tamanho do TEXTO FINAL e o tamanho do CONTEÚDO do resultado designado
// — o rótulo `classe` de [metricaOrigemRazao]. Abaixo de 1 o modelo escreveu menos do que leu;
// perto de 1, uma transcrição; acima, acrescentou. É um tamanho, e não diz se o que se escreveu é
// o que se leu: isso é a `comparacao`. Calcula-se sobre o CONTEÚDO desembrulhado — contra o
// envelope, um documento curto transcrito byte a byte caía abaixo de 0,9 só pelo peso do JSON.
const (
	razaoOrigemVazia = "origem_vazia"
	razaoTextoVazio  = "texto_vazio"
	razaoAbaixoDe05  = "abaixo_de_0_5"
	razaoDe05A09     = "de_0_5_a_0_9"
	razaoDe09A11     = "de_0_9_a_1_1"
	razaoDe11A2      = "de_1_1_a_2"
	razaoAcimaDe2    = "acima_de_2"
)

var classesDeRazao = []string{razaoOrigemVazia, razaoTextoVazio, razaoAbaixoDe05, razaoDe05A09, razaoDe09A11, razaoDe11A2, razaoAcimaDe2, naoComparado}

// classeDeRazao põe a razão texto/conteúdo numa das classes. Só tamanhos.
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
// tamanhos em bytes. NENHUM leva conteúdo — nem uma linha, nem um número, nem um digest.
type medidaDaOrigem struct {
	estado string
	// Só com a origem designada. `bytes` e `tamanho` são do resultado designado TAL COMO SE
	// TRANSPORTA (o envelope, quando a tool corre na sandbox), lidos da âncora.
	bytes      int
	tamanho    string
	transporte string
	// O conteúdo, quando o nó o serviu e ele confere com a âncora.
	forma      string
	comparacao string
	numeros    string
	razao      string
}

// medirOrigem reduz a resposta do nó `aos` à medida. NÃO DECIDE NADA e não devolve conteúdo.
//
// Lê a âncora (o estado, o tamanho do que se transportaria) e o que o nó fez dos bytes. Se o nó
// serviu `output` E ele confere com o digest da âncora, desembrulha o conteúdo e compara-o com o
// texto final; se não, as três classes de conteúdo ficam «não comparado» — não se compara com
// bytes que não são os que o kernel selou.
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
	m.forma, m.comparacao, m.numeros, m.razao = formaSemBytes, naoComparado, naoComparado, naoComparado
	switch {
	case st.Output != nil && digestDoConteudo(*st.Output) == a.Digest:
		m.transporte = transporteConfere
		conteudo, forma := desembrulharEnvelope(*st.Output)
		m.forma = forma
		if forma == formaEnvelope || forma == formaCru {
			m.comparacao, m.numeros = relacaoComOConteudo(st.FinalText, conteudo)
			m.razao = classeDeRazao(len(st.FinalText), len(conteudo))
		}
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

// linha escreve a medida para o log da drenagem. Só vocabulário fechado e tamanhos em bytes.
func (m medidaDaOrigem) linha(texto int) string {
	if m.estado != string(agentruntime.OutputSourceDesignated) {
		return "estado=" + m.estado
	}
	return fmt.Sprintf("estado=%s bytes_da_origem=%d (%s) transporte=%s forma=%s bytes_do_texto=%d texto_final=%s numeros=%s razao=%s",
		m.estado, m.bytes, m.tamanho, m.transporte, m.forma, texto, m.comparacao, m.numeros, m.razao)
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
	contar(&m.transportes, o.transporte)
	contar(&m.formas, o.forma)
	contar(&m.comparacoes, o.comparacao)
	contar(&m.numeros, o.numeros)
	contar(&m.razoes, o.razao)
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
	somar(metricaOrigemTransporte, "resultado", resultadosDoTransporte, c.transportes)
	somar(metricaOrigemForma, "forma", formasDaOrigem, c.formas)
	somar(metricaOrigemTextoFinal, "comparacao", comparacoesDoTexto, c.comparacoes)
	somar(metricaOrigemNumeros, "resultado", resultadosDosNumeros, c.numeros)
	somar(metricaOrigemRazao, "classe", classesDeRazao, c.razoes)
}
