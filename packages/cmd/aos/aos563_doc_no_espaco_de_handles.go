package main

// aos563_doc_no_espaco_de_handles.go — O `doc_id` RESOLVE-SE PRIMEIRO NO ESPAÇO DE HANDLES DO RUN
// (AOS-563).
//
// O AOS-561 abriu a VIA DE ENTRADA: quem submete um run manda os bytes de uma imagem no corpo do
// `POST /runs`, o nó confere-os pelos primeiros bytes e sela-os no espaço de handles do run antes
// de ele abrir. Mas era um PRODUTOR SEM LEITOR, e estava declarado no banner: a `ocr_read` e a
// `media_view` resolviam o `doc_id` contra a pasta semeada (`AOS_OCR_SEED_DIR`) e mais nada, pelo
// que a via selava conteúdo que nenhum run consumia.
//
// Este ficheiro fecha a volta: as duas tools resolvem o `doc_id` PRIMEIRO contra as entradas de
// media do run, e só depois contra a pasta semeada.
//
// # PORQUE AS DUAS ORIGENS E NÃO A SUBSTITUIÇÃO
//
// A pasta semeada é o que o DONO controla, e é a origem de tudo o que está provado em produção (as
// séries vid1–vid5, a prova do OCR). Trocá-la por esta via numa só alteração juntava uma capacidade
// nova a uma regressão possível no que já funciona. Sai quando esta via tiver prova em produção.
//
// # E NUNCA AS DUAS PARA O MESMO `doc_id`
//
// Um `doc_id` que resolva nos dois sítios é AMBÍGUO, e a ambiguidade decide-se fail-closed: o
// espaço do RUN ganha, e o caso é contado ([hCausaDoisEspacos]). Sem isto, um ficheiro semeado com
// o nome que o nó cunha (`media_input-1`) mudava o que uma tool lê sem ninguém o pedir — e quem
// semeia a pasta é o operador, quem submete a entrada é outra pessoa.
//
// A ORDEM É A SEGURA NOS DOIS SENTIDOS, e vale dizer porquê: o espaço do run é conteúdo que ESTE
// run trouxe, cifrado por titular, com o run nos dados associados e com o digest conferido depois
// de decifrar; a pasta é um recurso do NÓ, partilhado por todos os runs. Entre um conteúdo que é
// do run e um que é de todos, para um nome que ambos nomeiam, é o do run que o run queria.
//
// # O QUE NÃO MUDA, E É A METADE QUE IMPORTA
//
// A LEITURA DA PASTA SEMEADA FICA BYTE A BYTE IGUAL. As regras do ADR-042 §2.4 vivem em
// [lerConfinado] e este ficheiro NÃO as toca: o [os.Root], só o nível de topo, só ficheiros
// regulares, uma só ligação física, o Stat do DESCRITOR e o tecto na leitura. A razão é de desenho
// e não de preguiça: [lerConfinado] tem TRÊS chamadores, e o terceiro é a `video_read`, que está
// fora do âmbito deste ticket (o tecto de um vídeo não é o de uma imagem, e 256 KiB não é um
// vídeo). Uma alteração lá alcançava-a sem que nada neste ticket a tivesse pedido.
//
// A CAPABILITY, O DESTINO DECLARADO E A CLASSE DE DESTINO TAMBÉM NÃO MUDAM — `cap:fs.read`,
// `external_read`, e a classe do ADR-044. O que muda é DE ONDE a tool lê os bytes, e isso é config
// trusted do operador (`AOS_ENTRADA_DE_MEDIA`, `AOS_HANDLE_DIR`), não autorização. Nenhuma
// cerimónia de chave, e nenhuma rota HTTP nova serve o conteúdo.
//
// # O TIPO É O CONFERIDO NA PORTA, E NÃO UMA SEGUNDA INFERÊNCIA
//
// Pela pasta semeada o tipo sai dos PRIMEIROS BYTES, porque não há mais nada que o diga — é o que
// as duas tools já fazem. Pelo espaço de handles o tipo sai dos FACTOS do handle
// ([handleCriado.MediaType]), que é o tipo que a porta do AOS-561 conferiu pelos primeiros bytes
// ANTES de o run existir, e que ficou selado no `handle.created`. Duas derivações do mesmo tipo
// divergiriam, e a que vale é a que está selada.
//
// # O TITULAR VEM DO EVENTO, E PORQUE ISSO É O GUARDA E NÃO UM ATALHO
//
// [armazemDeHandles.abrir] precisa do titular para escolher a chave. A tool não o tem: o ctx do
// despacho não o leva, e o `principal` que o adaptador do gateway conhece é o token NHI do run,
// não o titular. Lê-se por isso do PRÓPRIO `handle.created` ([handleCriado.SealedSubject]), que é
// o que a rota dos artefactos já faz para servir um artefacto (`abrir(ctx, achado.SealedSubject,
// …)`, artifact_api.go).
//
// NÃO ENFRAQUECE NADA, e a razão é medível: um run tem UM titular — [planoDeHandles.selarEmQuarentena]
// é sempre chamada com `goal.Titular()`, e numa re-hospedagem o `Subject` está na classe «recusado
// se divergir» (aos559_rehospedagem_confere_o_selo.go) —, pelo que exigir que o chamador o traga
// compararia o titular do run consigo mesmo. O que amarra os bytes continua a ser o que sempre
// amarrou, e são três coisas independentes: o GUARDA (b) do AOS-550 (sem um `handle.created` no
// stream DESTE run não há bytes), os DADOS ASSOCIADOS (o blob foi selado para este run, e um blob
// copiado de outro não abre) e o DIGEST conferido DEPOIS de decifrar. O titular só escolhe a chave —
// e um titular crypto-shredded faz a decifra falhar, que é o lado seguro.
//
// ASSIMETRIA DECLARADA, e porque NÃO se fecha: as outras duas leituras do espaço COMPARAM o titular
// do evento com o do chamador ([quarentenaDoArmazem.OpenQuarantined] exige
// `h.SealedSubject == subject`; a travessia da aresta recusa com `travessia_outro_titular`). As
// duas revisões independentes deste ticket notaram-no e chamaram-lhe «paridade sem custo». Não é
// sem custo, e é por isso que fica assim: a paridade seria PARCIAL. O executor da tool podia receber
// o titular pela reescrita do efeito (é o que a `artifact_write` faz), mas a OUTRA metade da
// `media_view` — o resolvedor do adaptador, que é onde os bytes chegam ao modelo — só conhece o
// token NHI do run, não o titular, e não há por onde lho dar sem alargar o contrato do gateway. Uma
// comparação em metade do caminho lê-se como uma defesa inteira e não é: vale menos do que esta
// ausência escrita. Fecha-se com o titular no contrato do resolvedor, e é ticket próprio.

import (
	"context"
	"strings"

	"github.com/aos-ref/platform/model-gateway/port"
)

// formatoDoTipoDeImagem é o nome curto do formato que o resultado da `ocr_read` leva, derivado do
// tipo. Vocabulário FECHADO, e os mesmos dois nomes que [ligacaoOCR.lerImagem] já produzia pela
// inferência dos bytes — um tipo fora dele devolve "", e nenhum chega aqui porque
// [tipoDeMediaAceite] o filtrou antes.
func formatoDoTipoDeImagem(tipo string) string {
	switch tipo {
	case port.ImageMediaPNG:
		return "png"
	case port.ImageMediaJPEG:
		return "jpeg"
	}
	return ""
}

// entradaDeMediaDoRun procura, nas entradas de media que o stream do run declara, a que o nó
// cunhou com o nome `doc`. Devolve os factos do `handle.created` e se a encontrou.
//
// O PREDICADO É O MÍNIMO, E TEM TRÊS PARTES — nenhuma a mais, e nenhuma a menos:
//
//  1. `Tool == mediaInputHandleTool`: é o campo que DISTINGUE uma entrada ingerida de um
//     `tool_result`, e é para isso que o AOS-561 o escreve.
//  2. `Name == doc`: o nome é CUNHADO PELO NÓ (`<tool>-<ordinal>`, com o ordinal global do run), e
//     é por ele que o objectivo do run nomeia a entrada. Nunca um nome escolhido por quem submete.
//  3. O TIPO no vocabulário FECHADO de imagem. Esta terceira parte não é redundante, e a razão
//     está declarada em [mediaInputHandleTool] e em [planInputHandleTool]: o `Tool` é um RÓTULO, as
//     tools vêm de um ficheiro do OPERADOR (`AOS_MODEL_TOOLS`) e uma colisão de nomes é possível —
//     uma tool chamada `media_input` selaria os seus `tool_result` com `Tool=media_input` e
//     `Name=media_input-N`. O tipo fecha isso sem fazer nenhum guarda depender do rótulo, e fecha-o
//     pela MESMA função que a porta do AOS-561 usa.
//
// A PRIMEIRA PELA ORDEM DOS EVENTOS, e não a última: os nomes são únicos por construção (o ordinal
// é global e monótono dentro do run), pelo que a escolha não se observa; parar na primeira é o que
// não depende dessa unicidade para dar sempre a mesma resposta.
// A TERCEIRA DEVOLUÇÃO É UMA RECUSA, e não a ausência: não-vazia, a resolução INTEIRA recusa e não
// cai para a pasta semeada. Só o erro de leitura do stream a produz — ver o corpo.
func (p *planoDeHandles) entradaDeMediaDoRun(ctx context.Context, runID, doc string) (handleCriado, bool, string) {
	if p == nil || runID == "" || doc == "" {
		return handleCriado{}, false, ""
	}
	// UM PRÉ-FILTRO SOBRE O NOME, ANTES DE TOCAR NO EVENT STORE, e não é um guarda: é a MESMA
	// condição vista de outro lado. O `Name` é cunhado num só sítio, como `<tool>-<ordinal>`
	// ([armazemDeHandles.selar]), pelo que um handle que case com `Tool == mediaInputHandleTool` E
	// `Name == doc` obriga `doc` a ter este prefixo. Recusar aqui não pode mudar o resultado.
	//
	// EXISTE POR CUSTO: sem ele, CADA chamada da `ocr_read` e da `media_view` — e cada referência de
	// media em CADA turno, porque o adaptador re-resolve — lia o stream INTEIRO do run
	// (`es.Read(ctx, runID, 1)`) para procurar uma entrada que um `doc_id` com outra forma nunca pode
	// nomear. É o custo que [planoDeHandles.quarentenaDoRun] baixou de propósito, para uma leitura
	// única em vez de uma por resolução (achados M-1 e A-1 da revisão de segurança do AOS-550):
	// reintroduzi-lo num caminho mais quente era desfazer essa medição.
	//
	// O QUE O PRÉ-FILTRO COMPRA, DITO COM PRECISÃO, porque a primeira versão deste comentário dizia
	// mais do que é verdade e a segunda passagem adversarial mediu-o: quem escolhe o `doc_id` é o
	// MODELO, que é a parte untrusted — não o nó. Não se pode portanto afirmar que «um nó não lê o
	// stream»: um modelo que peça `media_input-7` força a leitura, haja ou não essa entrada. O que o
	// pré-filtro garante é mais estreito e é o que importa: um `doc_id` que NÃO tenha a forma de um
	// nome cunhado — todos os da pasta semeada, que é a forma provada em produção — não lê o stream
	// nenhuma vez. E o tecto do que um modelo pode forçar é o do run: o `Bootstrap` só liga o plano
	// às tools quando a VIA DE ENTRADA está composta, pelo que num nó que a não habilite nenhum
	// `doc_id` chega aqui.
	if !strings.HasPrefix(doc, mediaInputHandleTool+"-") {
		return handleCriado{}, false, ""
	}
	declarados, err := p.armazem.declarados(ctx, runID)
	if err != nil {
		// FAIL-CLOSED, E NÃO A PASTA SEMEADA. Um stream que não se lê não diz que o run NÃO tem esta
		// entrada — diz que não se sabe. Cair para a pasta nessa dúvida era servir o ficheiro do
		// OPERADOR em vez do conteúdo que o run trouxe, em silêncio e sem rasto: a substituição
		// exacta que a ordem de resolução existe para impedir, a acontecer num erro transitório do
		// Event Store em vez de numa colisão declarada.
		//
		// E O ALCANCE É ESTREITO por causa do pré-filtro acima: só um `doc_id` com a FORMA de um
		// nome cunhado chega aqui. Um `doc_id` da pasta semeada (`fatura.png`) nunca lê o stream,
		// pelo que esta recusa NÃO põe a leitura da pasta a depender do Event Store — que seria a
		// regressão de pagar uma capacidade nova com o que já funciona. O único `doc_id` que ela
		// pode recusar é um que o operador semeou com o nome que o nó cunha, e para esse a recusa é
		// a resposta certa: é a ambiguidade, sem o meio de a decidir.
		return handleCriado{}, false, p.armazem.conta(handleCausaIlegivel)
	}
	for _, h := range declarados {
		if h.Tool == mediaInputHandleTool && h.Name == doc && tipoDeMediaAceite(h.MediaType) {
			return h, true, ""
		}
	}
	return handleCriado{}, false, ""
}

// resolverDocDeImagem resolve o `doc` para bytes de imagem: PRIMEIRO nas entradas de media do run,
// depois na pasta semeada. É a função ÚNICA de ordem de resolução, partilhada pela `ocr_read` e
// pela `media_view` — duas ordens divergiriam, e a divergência seria entre o que uma tool lê e o
// que a outra lê para o MESMO `doc_id` do MESMO run.
//
// O `tipo` devolvido é NÃO VAZIO só no caminho do espaço de handles, e é o CONFERIDO NA PORTA. Com
// `tipo` vazio o chamador infere-o dos bytes como sempre fez — é o caminho da pasta semeada, e fica
// inalterado. Não se devolve um booleano à parte de propósito: o que o chamador precisa de saber do
// espaço é precisamente o tipo, e um booleano seria um segundo sítio onde a mesma pergunta se faz.
//
// `maximoDaPasta` é o tecto da leitura da PASTA, e vem de quem chama porque é dele: as duas tools
// em âmbito passam [port.MaxImageBytes]. Os bytes do espaço de handles não levam tecto aqui — o
// deles é por construção o do handle ([handleMaxBytes], 256 KiB, imposto na selagem pela porta do
// AOS-561 E pelo kernel em [agentruntime.Quarantine.Seal]), que é MENOR, e
// [armazemDeHandles.abrir] limita a leitura do cifrado.
func resolverDocDeImagem(ctx context.Context, p *planoDeHandles, raiz raizDoOCR, runID, doc string, maximoDaPasta int) (dados []byte, tipo string, causa string) {
	h, achou, recusa := p.entradaDeMediaDoRun(ctx, runID, doc)
	if recusa != "" {
		return nil, "", recusa
	}
	if !achou {
		dados, causa = lerConfinado(raiz, doc, maximoDaPasta)
		return dados, "", causa
	}
	// A COLISÃO CONTA-SE ANTES DE DECIFRAR, e é só um Lstat: se a pasta semeada também nomeia este
	// `doc`, o operador e quem submeteu nomearam a mesma coisa. Resolve pelo espaço do run — e o
	// operador fica a saber que aconteceu, que é tudo o que o contador compra. Um erro do Lstat (o
	// ficheiro não existir, que é o caso normal) NÃO é colisão.
	if _, err := raiz.Lstat(doc); err == nil {
		p.armazem.conta(hCausaDoisEspacos)
	}
	// O GUARDA (b) ESTÁ SATISFEITO POR CONSTRUÇÃO: o `handle.created` que nomeia este digest acabou
	// de ser lido do stream deste run. O [armazemDeHandles.abrir] confere ainda os dados associados
	// (o blob foi selado para ESTE run) e o digest depois de decifrar.
	conteudo, err := p.armazem.abrir(ctx, h.SealedSubject, runID, h.Digest)
	if err != nil {
		// A CAUSA NÃO LEVA UM BYTE DE CONTEÚDO, nem o digest, nem o titular, nem diz PORQUE é que a
		// abertura falhou (ficheiro ausente, titular shredded, digest que não bate — tudo uma só
		// causa). O que ela NÃO esconde é o espaço: `handle_ilegivel` nomeia-o, e é deliberado —
		// um `doc_id` com a forma de um nome cunhado já revelou a forma, e esconder o espaço
		// custaria uma causa ambígua a quem opera. O critério do ticket é sobre o `doc_id` que não
		// resolve em NENHUM dos dois, e esse sai com a causa da pasta (`ficheiro_em_falta`), que
		// não distingue nada.
		//
		// NÃO CAI PARA A PASTA: isso faria a tool ler, em silêncio, um conteúdo DIFERENTE do que o
		// run trouxe — a ambiguidade que este ticket fecha, e a mesma razão do ramo do erro de
		// leitura do stream, acima.
		return nil, "", p.armazem.conta(handleCausaIlegivel)
	}
	return conteudo, h.MediaType, ""
}
