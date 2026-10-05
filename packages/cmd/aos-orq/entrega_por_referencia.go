package main

// A ENTREGA POR REFERÊNCIA (AOS-501, ADR-038 §2.6 a §2.9).
//
// Quando o plano aprovado declara a origem de uma saída (`outputs[].from_tool`), o nó seguinte
// recebe o que a TOOL devolveu, e não o que o modelo escreveu sobre isso. Este ficheiro é a
// metade do `aos-orq` que o faz:
//
//   - decide, por `serve`, se a entrega está ACTIVA ([posturaDaEntrega]): o interruptor em `on`
//     E um nó `aos` que anuncia o vínculo vinculativo;
//   - a partir da resposta do nó, deriva o que se entrega ([entregaDoRun]) — os bytes
//     designados, conferidos INTEIROS contra a âncora que o kernel selou, e só depois o texto
//     do documento, quando esses bytes são o envelope da sandbox;
//   - nomeia, num vocabulário fechado, a causa de cada saída que não se entrega.
//
// NUNCA HÁ QUEDA PARA O TEXTO DO MODELO. Uma saída com origem declarada ou se publica a partir
// dos bytes designados, ou não se publica: o produtor fecha `failed` com causa e o consumidor
// não corre. O texto final do produtor continua capturado no run filho e não é publicado nem
// entregue.
//
// QUEM DECIDE SE UM NÓ É POR REFERÊNCIA É O DOCUMENTO APROVADO, e não o interruptor do `serve`
// que o recolhe. O interruptor governa a ADMISSÃO de planos com origem (origem_no_plano.go); o
// executor age sobre o que foi aprovado e sobre o que ficou no log.
//
// SEM CONTEÚDO. O log, as métricas e o `detail` levam causas, formas e tamanhos. Nunca os bytes
// designados, o texto entregue, o texto final, nem um digest ou um passo que tenha vindo do nó.

import (
	"context"
	"errors"
	"strings"

	plan "github.com/aos-ref/control-plane/orchestrator/plan"
	plannerevents "github.com/aos-ref/control-plane/orchestrator/plannerevents"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// posturaDaEntrega é o que ESTE `serve` faz com um plano que declara a origem de uma saída.
type posturaDaEntrega int

const (
	// entregaDesligada — o interruptor está em `off` ou `observe`: a linha 1.3.0 do plano não
	// corre, exactamente como antes do AOS-501. É o valor zero, de propósito: quem não disser
	// nada fica com o comportamento anterior.
	entregaDesligada posturaDaEntrega = iota
	// entregaSemNo — o interruptor está em `on`, mas não há a quem declarar a origem com o
	// vínculo vinculativo: o nó `aos` não o anuncia (anterior ao AOS-498, ou com o veredicto
	// desligado), ou o executor de nós não está composto. Um plano com origem NÃO corre.
	entregaSemNo
	// entregaActiva — `on` e um nó que anuncia o vínculo vinculativo: os planos com origem
	// correm, e as saídas declaradas entregam-se por referência.
	entregaActiva
)

// posturaDe compõe a postura a partir do interruptor e do anúncio do nó (o zero, se não houve
// nó a quem perguntar).
func posturaDe(modo string, a anuncioDoNo) posturaDaEntrega {
	switch {
	case modo != saidaPorReferenciaOn:
		return entregaDesligada
	case !a.vinculativa:
		return entregaSemNo
	default:
		return entregaActiva
	}
}

// chaveDaPostura é a chave de contexto da postura, como a do medidor do planeamento.
type chaveDaPostura struct{}

// comPostura põe a postura do `serve` no contexto: é por ele que chega à materialização, ao
// laço do planeador e ao despacho, sem mudar a assinatura de nenhum.
func comPostura(ctx context.Context, p posturaDaEntrega) context.Context {
	return context.WithValue(ctx, chaveDaPostura{}, p)
}

// posturaDoContexto lê a postura. Sem ela, [entregaDesligada] — o comportamento anterior.
func posturaDoContexto(ctx context.Context) posturaDaEntrega {
	p, _ := ctx.Value(chaveDaPostura{}).(posturaDaEntrega)
	return p
}

// errNoSemEntregaPorReferencia — o interruptor está em `on`, o plano declara a origem de uma
// saída, e não há um nó `aos` a quem a declarar com o vínculo vinculativo. O plano NÃO CORRE:
// corrê-lo sem o vínculo era publicar o texto do modelo sob um contrato que promete o resultado
// da tool. Partilha a saída 10 com o documento recusado e tem causa própria em [tipoDoErro]
// (`no_sem_saida_por_referencia`).
var errNoSemEntregaPorReferencia = errors.New("plano com origem de saida declarada (outputs[].from_tool) NAO CORRE: o no aos nao anuncia o vinculo vinculativo da origem (GET /tools sem output_source.bindings=binding — no anterior ao AOS-498, com o veredicto desligado, ou executor de nos nao composto)")

// saidaComOrigem devolve a saída do nó que declara a origem. O validador do plano admite no
// máximo uma por nó (`from_tool_multiple`); com mais do que uma não devolve nenhuma, e quem
// chama trata o nó como não entregável.
func saidaComOrigem(n plan.Node) (plan.Output, bool) {
	var saida plan.Output
	com := 0
	for _, o := range n.Outputs {
		if o.FromTool != "" {
			saida = o
			com++
		}
	}
	return saida, com == 1
}

// saidasDeTexto conta as saídas de forma aberta que se publicam do TEXTO FINAL: as abertas sem
// origem declarada. Num plano sem `from_tool` é o número de saídas abertas, como sempre.
func saidasDeTexto(n plan.Node) int {
	abertas := 0
	for _, o := range n.Outputs {
		if !o.Type.ClosedForm() && o.FromTool == "" {
			abertas++
		}
	}
	return abertas
}

// Causas de um nó do plano `failed` por a sua saída POR REFERÊNCIA não se poder entregar.
// Vocabulário FECHADO, e todas da classe «a conclusão não se cumpriu» ([causaDaConclusao]).
const (
	// causaOrigemEmFalta — o kernel não designou origem nenhuma: a tool declarada não foi pedida
	// no primeiro turno com tools, ou a chamada não foi efectiva (âncora `missing`).
	causaOrigemEmFalta = "origem_em_falta"
	// causaOrigemAmbigua — a tool declarada foi pedida mais de uma vez nesse turno (`ambiguous`).
	causaOrigemAmbigua = "origem_ambigua"
	// causaOrigemInaplicavel — o run tinha entradas, e um run com entradas nunca tem origem
	// (`inapplicable`): defeito de quem compôs o plano.
	causaOrigemInaplicavel = "origem_inaplicavel"
	// causaOrigemSemVinculo — não há prova de que o run foi pedido por referência com o vínculo
	// vinculativo: falta o facto `plan.output_source_declared` no log do plano, ou ele diz
	// «só medição» ou outra tool; ou o run não traz âncora, ou traz uma que o kernel não
	// produziria, ou com outro vínculo ou outra tool.
	causaOrigemSemVinculo = "origem_sem_vinculo"
	// causaOrigemNaoConfere — o nó serviu bytes que NÃO são os que o kernel selou (o digest ou
	// o tamanho não batem com a âncora). Não se entregam.
	causaOrigemNaoConfere = "origem_nao_confere"
	// causaOrigemIndisponivel — a origem ficou designada e os bytes não se lêem, de vez
	// (titular apagado, passo fora do step-ledger, log danificado). É o SEGUNDO sentido de
	// `output_unavailable`, separado do primeiro ([causaSaidaIndisponivel], o texto final).
	causaOrigemIndisponivel = "origem_indisponivel"
	// causaOrigemNaoTransportavel — o resultado designado não se transporta: acima do tecto,
	// não é texto válido, ou o stdout do envelope é binário. Nada é truncado.
	causaOrigemNaoTransportavel = "origem_nao_transportavel"
	// causaOrigemToolFalhou — o resultado designado é o envelope de uma execução que terminou
	// com código de saída diferente de zero: a tool correu e falhou.
	causaOrigemToolFalhou = "origem_tool_falhou"
	// causaOrigemVazia — o que se entregaria está vazio: o texto extraído do envelope, ou o
	// resultado cru, não tem nada além de espaços.
	causaOrigemVazia = "origem_vazia"
)

// causasDaOrigem é a lista fechada, pela ordem em que se documentam.
var causasDaOrigem = []string{
	causaOrigemEmFalta, causaOrigemAmbigua, causaOrigemInaplicavel, causaOrigemSemVinculo,
	causaOrigemNaoConfere, causaOrigemIndisponivel, causaOrigemNaoTransportavel,
	causaOrigemToolFalhou, causaOrigemVazia,
}

// resultadoEntregue é o rótulo do nó cuja saída por referência se entregou — com as causas,
// forma o rótulo `resultado` de [metricaEntregaPorReferencia].
const resultadoEntregue = "entregue"

// entregaPorReferencia é o que se publica e entrega de uma saída com origem: o conteúdo, e o
// que o evento de publicação regista para a derivação ser reproduzível.
type entregaPorReferencia struct {
	// conteudo é o que o consumidor recebe. É conteúdo do titular: vive na memória deste
	// processo, como os outros payloads, e não vai a log nenhum.
	conteudo string
	// extraccao diz como o conteúdo se deriva dos bytes designados.
	extraccao plannerevents.PayloadExtraction
	// passo, digestDaAncora e bytesDaAncora são da âncora selada pelo kernel: o resultado
	// INTEIRO da tool, antes da extracção.
	passo          string
	digestDaAncora string
	bytesDaAncora  int
}

// extrairEntrega deriva o que se entrega dos bytes designados. SÓ SE CHAMA DEPOIS de os bytes
// inteiros terem conferido com a âncora ([entregaDoRun]). Devolve o conteúdo e a forma da
// extracção, ou a causa de não haver entrega.
//
// # A regra (decisão do dono de 2026-10-06)
//
//   - o resultado é um envelope da sandbox reconhecível, com `exit_code` zero e o stdout em
//     texto: entrega-se SÓ o `stdout_text` — o documento, uma vez. O envelope inteiro leva-o
//     duas vezes (texto, e artefacto em base64);
//   - envelope com `exit_code` diferente de zero: a tool correu e falhou. Não se entrega;
//   - envelope com o stdout binário: não é texto que se entregue;
//   - não é um envelope: entrega-se o resultado cru, tal como a tool o devolveu;
//   - o que se entregaria está vazio (só espaços): não se entrega.
//
// É a MESMA leitura do envelope que a medição do AOS-499 faz ([desembrulharEnvelope]), presa ao
// codificador real pelos ficheiros de fio da sandbox. Reconhecer a forma não prova que os bytes
// vieram da sandbox: uma tool que devolva por conta própria um JSON com a forma do envelope é
// lida como envelope. O que se entrega é sempre uma função dos bytes que o kernel selou, e o
// evento regista a forma — quem audita refaz a derivação.
func extrairEntrega(servido string) (conteudo string, forma plannerevents.PayloadExtraction, causa string) {
	texto, formaDoResultado := desembrulharEnvelope(servido)
	switch formaDoResultado {
	case formaEnvelopeFalhou:
		return "", "", causaOrigemToolFalhou
	case formaEnvelopeBinario:
		return "", "", causaOrigemNaoTransportavel
	case formaEnvelope:
		conteudo, forma = texto, plannerevents.PayloadExtractionSandboxStdoutText
	default:
		conteudo, forma = servido, plannerevents.PayloadExtractionRaw
	}
	if strings.TrimSpace(conteudo) == "" {
		return "", "", causaOrigemVazia
	}
	if len(conteudo) > maxPayloadBytes {
		// O tecto aplica-se ao que é ENTREGUE. O nó já não serve mais de 128 KiB de resultado,
		// pelo que isto só fecha um nó que mude de tecto sem este binário saber.
		return "", "", causaOrigemNaoTransportavel
	}
	return conteudo, forma, ""
}

// entregaDoRun deriva, da resposta do nó `aos` a um run CONCLUÍDO, o que se entrega de uma saída
// cuja origem declarada é `tool`. Devolve a entrega, ou a causa — nunca as duas, e nunca o
// texto final.
//
// A ORDEM É A DAS GARANTIAS:
//
//  1. a âncora existe, tem a forma que o kernel produz, e diz o vínculo VINCULATIVO e a tool
//     DO CONTRATO. Um run pedido em «só medição» não foi julgado pelo kernel como vinculativo, e
//     não se entrega por ele;
//  2. o estado é `designated`;
//  3. o nó serviu os bytes, e `sha256(bytes)` e o tamanho são os da âncora. O QUE NÃO CONFERE
//     NÃO SE ENTREGA — e a conferência é sobre os bytes INTEIROS, antes de qualquer extracção;
//  4. só então se extrai ([extrairEntrega]).
//
// O que vem do nó passa por vocabulários fechados: um estado ou uma marca que este binário não
// conhece dão uma causa deste ficheiro, sem serem repetidos.
func entregaDoRun(tool string, st estadoDoRun) (entregaPorReferencia, string) {
	a := st.OutputSource
	if a == nil || !a.BemFormada() || a.Binding != agentruntime.OutputSourceBinds || a.Tool != tool {
		return entregaPorReferencia{}, causaOrigemSemVinculo
	}
	switch a.State {
	case agentruntime.OutputSourceDesignated:
	case agentruntime.OutputSourceMissing:
		return entregaPorReferencia{}, causaOrigemEmFalta
	case agentruntime.OutputSourceAmbiguous:
		return entregaPorReferencia{}, causaOrigemAmbigua
	case agentruntime.OutputSourceInapplicable:
		return entregaPorReferencia{}, causaOrigemInaplicavel
	default:
		return entregaPorReferencia{}, causaOrigemSemVinculo
	}
	if st.Output == nil {
		switch st.OutputOmitted {
		case transporteGrande, transporteNaoTexto:
			return entregaPorReferencia{}, causaOrigemNaoTransportavel
		default:
			// `unavailable` (de vez), e tudo o que este binário não sabe ler: os bytes
			// designados não vieram. Com o vínculo vinculativo o nó responde 503 ao que é
			// transitório, e essa resposta não chega aqui — quem recolhe volta a ler.
			return entregaPorReferencia{}, causaOrigemIndisponivel
		}
	}
	if len(*st.Output) != a.Bytes || digestDoConteudo(*st.Output) != a.Digest {
		return entregaPorReferencia{}, causaOrigemNaoConfere
	}
	conteudo, forma, causa := extrairEntrega(*st.Output)
	if causa != "" {
		return entregaPorReferencia{}, causa
	}
	return entregaPorReferencia{conteudo: conteudo, extraccao: forma, passo: a.StepID, digestDaAncora: a.Digest, bytesDaAncora: a.Bytes}, ""
}

// declaracaoDeOrigem é o facto `plan.output_source_declared` de um nó, tal como este processo o
// escreveu ou o leu do log do plano: a tool e o vínculo com que o run foi pedido.
type declaracaoDeOrigem struct {
	tool    string
	vinculo plannerevents.OutputSourceBinding
}

// vinculativa diz se o facto prova que o run foi pedido por referência, com o vínculo
// vinculativo, para a tool do contrato.
func (d declaracaoDeOrigem) vinculativa(tool string) bool {
	return d.vinculo == plannerevents.OutputSourceBindingBinds && d.tool == tool
}

// bannerDaEntrega declara, no arranque do `serve`, o que a postura faz. Só se imprime com o
// interruptor em `on`: nos outros modos o banner é o do AOS-499, byte a byte.
func bannerDaEntrega(p posturaDaEntrega) string {
	if p == entregaActiva {
		return "saida por referencia (AOS-501): modo on, ENTREGA ACTIVA — o no anuncia o vinculo binding. Um plano que declare a origem de uma saida (outputs[].from_tool, linha 1.3.0) corre: o run do no produtor leva a declaracao com o vinculo binding, e o no seguinte recebe o resultado que o kernel designou, conferido inteiro contra a ancora selada (de um envelope da sandbox, so o stdout_text). O texto final do produtor NAO e publicado nem entregue. Sem origem designavel, ou com bytes que nao conferem, o no do plano fecha failed com causa e o consumidor nao corre. Um plano sem from_tool corre como sempre. O planeador e instruido pelo prompt 1.5.0"
	}
	return "saida por referencia (AOS-501): modo on, ENTREGA NAO ACTIVA — o no nao anuncia o vinculo binding (GET /tools sem output_source.bindings=binding: no anterior ao AOS-498, com o veredicto desligado, ou executor nao composto). Um plano que declare a origem de uma saida NAO corre (saida 10, no_sem_saida_por_referencia): nunca se entrega o texto do modelo no lugar do resultado da tool. O planeador fica com o prompt 1.4.0, que nao pede o campo. Um plano sem from_tool corre como sempre"
}

// Razões do validador do plano que são das regras da origem (AOS-500) — o rótulo `razao` de
// [metricaOrigemRecusasDoValidador]. Vocabulário fechado, do `planvalidate`.
var razoesDaOrigemNoValidador = []string{
	"from_tool_on_verifier", "from_tool_with_consumes", "from_tool_multiple",
	"from_tool_output_type", "from_tool_unknown_tool", "from_tool_ambiguous_tool",
}

// entregaResolvida conta um nó com saída por referência, pelo resultado: entregue, ou a causa.
func (m *medicaoDoContrato) entregaResolvida(resultado string) {
	if m == nil {
		return
	}
	if m.entregas == nil {
		m.entregas = map[string]int{}
	}
	m.entregas[resultado]++
}

// extraccaoFeita conta uma saída entregue, pela forma da extracção.
func (m *medicaoDoContrato) extraccaoFeita(forma string) {
	if m == nil {
		return
	}
	if m.extraccoes == nil {
		m.extraccoes = map[string]int{}
	}
	m.extraccoes[forma]++
}

// candidatoSemOrigem conta um nó candidato por estrutura cujo plano NÃO declarou a origem, com
// a entrega activa: é a taxa de omissão do planeador.
func (m *medicaoDoContrato) candidatoSemOrigem() {
	if m == nil {
		return
	}
	m.candidatosSemOrigem++
}

// recusaDaOrigemNoValidador conta uma tentativa do planeador recusada por uma regra da origem.
func (m *medicaoDoContrato) recusaDaOrigemNoValidador(razao string) {
	if m == nil {
		return
	}
	if m.recusasDaOrigem == nil {
		m.recusasDaOrigem = map[string]int{}
	}
	m.recusasDaOrigem[razao]++
}

// registarEntrega soma às séries o que um `serve` fez com as saídas por referência. Só escreve
// rótulos do vocabulário fechado, e só séries com valor: com o interruptor fora de `on` o
// ficheiro de métricas é o de antes.
func (m *metricasDoConsumo) registarEntrega(c *medicaoDoContrato) {
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
	somar(metricaEntregaPorReferencia, "resultado", append([]string{resultadoEntregue}, causasDaOrigem...), c.entregas)
	somar(metricaEntregaExtraccao, "extraccao", []string{
		string(plannerevents.PayloadExtractionSandboxStdoutText), string(plannerevents.PayloadExtractionRaw),
	}, c.extraccoes)
	somar(metricaOrigemRecusasDoValidador, "razao", razoesDaOrigemNoValidador, c.recusasDaOrigem)
	if c.candidatosSemOrigem > 0 {
		m.somar(metricaCandidatosSemOrigem, float64(c.candidatosSemOrigem))
	}
}
