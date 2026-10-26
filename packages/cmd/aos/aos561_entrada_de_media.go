package main

// aos561_entrada_de_media.go — A VIA DE ENTRADA: UM FICHEIRO NO PEDIDO DE UM RUN (AOS-561).
//
// Até aqui NENHUMA rota do nó aceitava um ficheiro. A única via de media eram as tools de modelo
// (`ocr_read`, `media_view`, `video_read`) a ler de uma montagem SÓ DE LEITURA de uma pasta que o
// OPERADOR semeia (`AOS_OCR_SEED_DIR`, ADR-042 §2.4). Tudo o que está provado em produção — a
// série vid1–vid5, a prova do OCR — depende de alguém com acesso ao servidor pôr lá o ficheiro.
//
// Este ficheiro é a linha que faltava: quem submete um run manda os bytes NO PEDIDO, o nó
// confere-os na porta e sela-os no espaço de handles do run antes de o run existir. Nada é copiado
// para o servidor por mãos humanas, e o operador sai do meio.
//
// # O QUE NÃO NASCE AQUI (§3.5 do desenho de 2026-10-25)
//
//   - NÃO nasce armazém novo. O espaço de handles (AOS-550, `handle_store.go`) é ele: cifra por
//     titular com o run nos dados associados, proveniência selada em `handle.created`, tectos
//     próprios, emissor próprio e crypto-shred do titular.
//   - NÃO nasce rota de leitura. Ingerir conteúdo não obriga a servi-lo de volta, e nenhuma rota
//     HTTP serve o conteúdo do espaço de handles — o banner do AOS-550 declara-o, e continua
//     verdade depois desta via.
//   - NÃO nasce `multipart`, nem rota de upload de duas fases. O corpo do `POST /runs` tem UMA
//     forma, e isso é uma propriedade: audita-se, repete-se e compara-se. O gatilho honesto para
//     reabrir a via de duas fases é o dia em que o tamanho de uma entrada LEGÍTIMA deixar de ser
//     razoável num corpo de pedido (um vídeo, um PDF de muitas páginas) — não uma fotografia.
//   - A entrada NÃO vai ao armazém de artefactos. Ele é a SAÍDA: tem rota de download, vocabulário
//     fechado de texto e validação de conteúdo desenhada para o que o run PRODUZ. Misturar as duas
//     direcções juntaria uma superfície de leitura a conteúdo que vem de fora.
//
// # PORQUE NÃO É UM `plan_input` COM UM TIPO, QUE É O QUE O DESENHO PROPÔS
//
// MEDIDO, e inverte a §6 do desenho. Um `plan_input` é, em TODOS os layouts até à 1.7.0 — que é
// aquele em que a produção corre, porque a media no turno está ligada ([layoutDoRunNovo]) —, um
// payload que entra no CONTEXTO DO MODELO: [agentruntime.TailFromPlanInput] põe o
// `PlanInput.Content` no corpo do segmento. Dar `media_type` a um `plan_input` e nada mais punha
// bytes de imagem no prompt.
//
// E o selo que a §6 nomeia para o impedir ([selarEntradasDoPlano]) SÓ CORRE NA 1.8.0
// ([agentruntime.HasPlanInputsByHandle]), que é a fase R2 e está desligada à espera da cerimónia
// da chave do dono. Não salvava a produção.
//
// Por isso a entrada de media é uma lista PRÓPRIA no mesmo pedido, e o seu selo corre em TODOS os
// layouts: o que mantém bytes de imagem fora do contexto do modelo não é o layout, é o facto de
// serem media. Fica assim UM produtor para esta lista, e não dois para o mesmo campo.
//
// # OS TECTOS, E QUAL DELES MANDA
//
// Um tecto de entrada maior do que o do destino só serve para aceitar o que vai ser recusado
// depois, com o custo já pago. O caminho de uma entrada de media tem QUATRO tectos, e o que manda
// é o MENOR:
//
//	corpo do pedido      1 MiB    DefaultMaxBodyBytes (api.go) — 768 KiB de conteúdo em base64
//	conjunto de media    1 MiB    [maxMediaInputsBytes], 4× o tecto de uma entrada
//	entrada de media   256 KiB    [maxMediaInputBytes], o menor dos dois abaixo
//	handle             256 KiB    handleMaxBytes = agentruntime.MaxToolResultBytes
//	parte de imagem      4 MiB    port.MaxImageBytes, no Model Gateway
//
// O desenho mediu três destes e NÃO mediu o do handle — e é ele que manda. [maxMediaInputBytes] é
// por isso o MENOR dos dois tectos a jusante, e não um número escolhido. **Não é derivado com
// `min`**, porque `min` não é expressão constante em Go: quem garante o invariante é
// [TestAOS561_TectoDaPortaNaoPassaONenhumDosDestinos], que falha se algum dos dois mudar e este
// deixar de ser o menor. Dizer «derivado em código (min)» era a afirmação que a segunda passagem
// adversarial apanhou aqui — e era grave porque é ela que faria ninguém verificar.
//
// CONSEQUÊNCIA PARA O DONO, medida e não suposta: uma entrada de ATÉ 256 KiB entra HOJE, sem
// mexer em nada — 256 KiB em base64 são 341 KiB, que cabem no corpo de 1 MiB. O que NÃO entra é
// uma fotografia de telemóvel de 4 MiB, e para essa são precisas DUAS mudanças de postura, não
// uma: o tecto do corpo do pedido (AOS-562 abre-lhe a superfície, e o número é dele) E o tecto
// por handle. A §3.4 do desenho nomeia só a primeira.
//
// # A CONSEQUÊNCIA QUE PERTENCE AO DONO E NÃO A ESTE TICKET
//
// O CONTEÚDO INGERIDO NÃO É REDIGIDO. A fronteira de ingestão trata só o objectivo
// ([integration.IngestionGateway.IngestObjective]); os payloads do pedido nunca passaram por lá, e
// bytes de imagem não passam por um redactor de texto. Isto JÁ É VERDADE para o que o dono semeia
// na pasta — o que esta via muda é QUEM o pode causar: passa a ser quem submete.
//
// O que a contém é o que já existe: o SA-ROC classifica pela sensibilidade efectiva, o taint
// eleva, o egress é default-deny com allowlist, e o apagamento é o crypto-shred do titular. O que
// NÃO existe é redacção de imagem, e desenhá-la não é trabalho desta via. Está declarado no banner
// de arranque, para que nenhum operador ligue a via sem o ler.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/port"
)

// Os dois valores de AOS_ENTRADA_DE_MEDIA.
const (
	entradaDeMediaOff = "off"
	entradaDeMediaOn  = "on"
)

// ErrBadEntradaDeMedia — AOS_ENTRADA_DE_MEDIA com um valor fora do vocabulário. O nó recusa
// arrancar, pela regra das outras variáveis de vocabulário fechado: um valor mal escrito não pode
// cair em silêncio para nenhum dos lados — um operador que escreve `ON` não deve ficar com a via
// desligada a pensar que a ligou.
var ErrBadEntradaDeMedia = errors.New("aos: AOS_ENTRADA_DE_MEDIA invalida — valores aceites: off (por omissao; um pedido com media_inputs e RECUSADO) e on (o no aceita entradas de media no POST /runs, confere-as pelos bytes e sela-as no espaco de handles)")

// ErrEntradaDeMediaSemHandles — AOS_ENTRADA_DE_MEDIA=on num nó sem espaço de handles. RECUSA O
// ARRANQUE, e não a submissão: sem `AOS_HANDLE_DIR` não há onde selar os bytes, pelo que a via
// estaria ligada e nenhuma entrada poderia ser aceite. Um nó que anuncia uma via que não tem é
// pior do que um nó que a não anuncia.
var ErrEntradaDeMediaSemHandles = errors.New("aos: AOS_ENTRADA_DE_MEDIA=on exige AOS_HANDLE_DIR — os bytes de uma entrada de media sao selados no espaco de handles do run, e sem ele nao ha onde os por; ligue o espaco de handles ou deixe a via desligada")

// parseEntradaDeMediaFromEnv lê AOS_ENTRADA_DE_MEDIA. Vazia ⇒ false (`off`). Sem normalização de
// caixa, pela regra das outras variáveis de vocabulário fechado do nó.
func parseEntradaDeMediaFromEnv() (bool, error) {
	switch raw := strings.TrimSpace(os.Getenv("AOS_ENTRADA_DE_MEDIA")); raw {
	case "", entradaDeMediaOff:
		return false, nil
	case entradaDeMediaOn:
		return true, nil
	default:
		return false, fmt.Errorf("%w (veio %q)", ErrBadEntradaDeMedia, raw)
	}
}

// mediaInputWire é a representação de wire de uma entrada de media (AOS-561).
//
// TRÊS CAMPOS, e nenhum a mais. Um `plan_input` leva `from`/`output` porque nomeia o CONTRATO do
// nó do plano que o produziu; uma entrada de media vem de quem submete, e não há nó produtor para
// nomear. Um campo que não decide nada é peso morto, e o nome pelo qual o objectivo do run a
// refere é CUNHADO PELO NÓ ([agentruntime.MediaInput.Name]) — nunca escolhido por quem submete.
type mediaInputWire struct {
	// MediaType é o tipo DECLARADO. É untrusted: quem decide é [port.ImagePart.Validate], pelos
	// primeiros bytes. Uma declaração que não bata com os bytes RECUSA o pedido.
	MediaType string `json:"media_type"`
	// Digest é `sha256:<hex>` dos BYTES — não da string base64 em que eles viajam. Ver
	// [validarMediaInputs].
	Digest string `json:"digest"`
	// Content são os bytes em `base64` padrão (com padding), porque o `content` de um JSON é uma
	// string. Custo: +33% no corpo do pedido, contado contra o tecto do corpo.
	Content string `json:"content"`
}

// Tectos do canal de entrada de MEDIA (AOS-561). SEPARADOS dos de texto (`maxPlanInput*`), e
// contados ANTES de o run existir, como os de texto já se contam.
const (
	// maxMediaInputBytes é o tecto de UMA entrada de media: tem de ser o MENOR dos dois tectos a
	// jusante — o do espaço de handles, onde os bytes são selados (`handleMaxBytes`, que é o do
	// corpo de um `tool_result` e que o KERNEL também impõe em [agentruntime.Quarantine.Seal]), e
	// o do Model Gateway, que é quem os vai receber ([port.MaxImageBytes]).
	//
	// HOJE O MENOR É O DO ESPAÇO DE HANDLES, e é essa a medição que o desenho de 2026-10-25 NÃO
	// fez: são 256 KiB, não os 4 MiB do gateway. Não se escreve aqui `min(…)` porque não é
	// expressão constante em Go; quem guarda o invariante é
	// [TestAOS561_TectoDaPortaNaoPassaONenhumDosDestinos], que falha se algum dos dois mudar e
	// este deixar de ser o menor — sem ele a porta aceitaria o que o selo recusa.
	maxMediaInputBytes = handleMaxBytes
	// maxMediaInputsBytes é o tecto do CONJUNTO, em bytes: 1 MiB. É o que bota o que o nó
	// descodifica e hasheia ANTES de autenticar, e é a única razão de ele existir — o canal de TEXTO
	// tem o seu pela mesma razão (`maxPlanInputsBytes`, 512 KiB, deliberadamente 4× abaixo do
	// produto do seu tecto individual pela contagem: «o agregado é o que conta»).
	//
	// NÃO É DERIVADO DOS OUTROS TECTOS, e isso é a correcção de uma correcção — as três tentativas
	// ficam registadas porque cada uma falhou pela razão que a seguinte teve de evitar:
	//
	//  1. Era `port.MaxImageBytes`, IGUAL ao produto da contagem pelo tecto de uma entrada, pelo que
	//     `total > agregado` nunca disparava. INERTE, e foi um teste que o apanhou.
	//  2. A correcção foi TIRÁ-LO («o conjunto é o produto»). Errada: sem agregado próprio, o que
	//     limitava o conjunto passava a ser o tecto do CORPO, que é do OPERADOR e sobe (AOS-562) —
	//     logo o trabalho pré-autenticação ficava preso a um botão dele.
	//  3. Voltou DERIVADO, `4 × maxMediaInputBytes`. Também errada, e a segunda passagem adversarial
	//     mediu-a: o produto é SEMPRE [port.MaxImageBytes] (porque a contagem é
	//     `MaxImageBytes / maxMediaInputBytes`), pelo que um agregado que acompanhe o tecto de uma
	//     entrada **ultrapassa o produto** assim que esse tecto suba de 512 KiB — e a mudança de
	//     postura que o README prescreve ao dono (subir o tecto por handle para caber uma fotografia
	//     de 4 MiB) fazia o invariante «o agregado morde» FALHAR. Uma derivação que proíbe a mudança
	//     documentada é pior do que um número.
	//
	// Por isso é um NÚMERO, com a sua razão: o tecto do trabalho pré-autenticação, que não se move
	// com nenhum dos outros. Subir o tecto por handle para caber uma fotografia exige subir TAMBÉM
	// este — são TRÊS mudanças de postura, não duas, e é o que a documentação diz.
	maxMediaInputsBytes = 1 << 20
	// maxMediaInputs é o nº de entradas de media por pedido — o tecto das POSIÇÕES de handle que um
	// pedido pode gastar, que é um recurso diferente dos bytes. É o que o gateway aceita na soma das
	// partes de imagem de um pedido, dividido pelo tecto de uma ([port.MaxImageBytes] /
	// [maxMediaInputBytes] = 16), e fica com folga de 4× contra o tecto de handles por run
	// (`handleMaxPorRun`, 64) — folga que existe para os `tool_result` do mesmo run, e que um teste
	// assere em vez de a deixar numa frase.
	maxMediaInputs = port.MaxImageBytes / maxMediaInputBytes
	// mediaInputDigestAlgo é o prefixo do digest, como no canal de texto.
	mediaInputDigestAlgo = "sha256:"
)

// tipoDeMediaAceite diz se o tipo declarado está no vocabulário FECHADO da via. Comparação
// EXACTA, e os valores são os do Model Gateway — não uma segunda cópia das strings: abrir aqui
// mais do que o gateway aceita era prometer o que não se cumpre, e uma cópia divergiria no PR
// seguinte.
func tipoDeMediaAceite(tipo string) bool {
	return tipo == port.ImageMediaPNG || tipo == port.ImageMediaJPEG
}

// tiposDeMediaAceites é o vocabulário, para as mensagens e para o banner.
func tiposDeMediaAceites() string {
	return port.ImageMediaPNG + ", " + port.ImageMediaJPEG
}

// validarMediaInputs verifica as entradas de media do pedido e converte-as para o Goal. Devolve a
// mensagem de erro (vazia quando está tudo bem) — a fronteira RECUSA antes de o run existir, como
// o canal de texto.
//
// A ORDEM É A DA GRAVIDADE, e é a de [port.ImagePart.Validate]: um tipo fora do vocabulário não
// chega a ter os bytes descodificados.
//
//  1. TIPO no vocabulário fechado. Barato, e não toca nos bytes.
//  2. `base64` padrão ESTRITO. Uma string que não descodifica não é conteúdo.
//  3. TECTO de uma entrada, e TECTO AGREGADO. Antes do digest: não se gasta um sha256 sobre bytes
//     que já não cabem. A CONTAGEM é conferida à cabeça, antes de tudo.
//  4. DIGEST sobre os BYTES. Ver abaixo.
//  5. OS BYTES DECIDEM O TIPO — [port.ImagePart.Validate], a MESMA função que o gateway usa. Um
//     ficheiro que diga `image/png` e não o seja é recusado aqui, antes de o run existir.
//
// O DIGEST É DOS BYTES, NÃO DA STRING. O desenho diz «o digest conferido como hoje», e a
// DISCIPLINA é a mesma — conferido na porta, fail-closed, antes de o run existir. O PRÉ-IMAGEM é
// que não pode ser a string: (a) o `base64` tem várias escritas dos mesmos bytes, e um digest da
// string fazia a mesma imagem dar números diferentes; (b) o que tem de chegar intacto ao
// consumidor são os bytes; e (c) é dos bytes que o espaço de handles deriva o nome do ficheiro
// (`digestHex`), pelo que um digest da string punha DOIS números a descrever uma coisa — que é
// como este repositório se morde.
//
// CORRE PRÉ-AUTENTICAÇÃO, como a conferência do canal de TEXTO: a ordem do `handleSubmit` é
// rate-limit → tecto de runs em curso → tecto do CORPO → esta conferência → autenticação. É
// deliberado e é o idioma da porta (recusar um corpo malformado não deve exigir autenticar
// primeiro), mas tem um custo que vale nomear: um chamador NÃO AUTENTICADO pode impor um
// `base64` e um `sha256` sobre os bytes que a porta aceita. O que o contém é o TECTO AGREGADO
// ([maxMediaInputsBytes], 1 MiB) e o token-bucket — e é o agregado, de propósito, para que
// SUBIR O TECTO DO CORPO (AOS-562) NÃO SUBA ESTE CUSTO. É a mesma forma que o canal de texto
// tem (agregado de 512 KiB), e foi um achado da revisão adversarial: sem agregado próprio,
// este número ficava preso a um botão do operador.
//
// A COMPARAÇÃO DO TECTO APARECE DUAS VEZES, aqui e dentro de [port.ImagePart.Validate], e não é
// duplicação a limpar: os números são DIFERENTES e o papel é diferente. O daqui é o tecto do NÓ
// ([maxMediaInputBytes], 256 KiB) e morde primeiro; o de lá é o do GATEWAY (4 MiB) e é a última
// linha antes de os bytes saírem. Tirar o daqui deixava a porta a aceitar 4 MiB para o selo
// recusar depois, com o corpo já lido.
func validarMediaInputs(wire []mediaInputWire) ([]agentruntime.MediaInput, string) {
	if len(wire) == 0 {
		return nil, ""
	}
	if len(wire) > maxMediaInputs {
		return nil, "media_inputs acima do tecto"
	}
	out := make([]agentruntime.MediaInput, 0, len(wire))
	total := 0
	for i, in := range wire {
		if !tipoDeMediaAceite(in.MediaType) {
			return nil, fmt.Sprintf("media_inputs[%d].media_type fora do vocabulario (aceites: %s)", i, tiposDeMediaAceites())
		}
		dados, err := base64.StdEncoding.Strict().DecodeString(in.Content)
		if err != nil {
			return nil, fmt.Sprintf("media_inputs[%d].content nao e base64 padrao", i)
		}
		if causa := conferirUmaEntrada(i, in.MediaType, in.Digest, dados, &total); causa != "" {
			return nil, causa
		}
		out = append(out, agentruntime.MediaInput{MediaType: in.MediaType, Digest: in.Digest, Content: dados})
	}
	return out, ""
}

// conferirUmaEntrada é a conferência de UMA entrada sobre os BYTES JÁ DESCODIFICADOS, partilhada
// pela porta ([validarMediaInputs]) e pelo selo ([conferirMediaDoGoal]). `total` acumula o
// agregado entre chamadas; `i` é o índice que a mensagem mostra, e vem de quem chama.
//
// RECEBE BYTES, E NÃO WIRE, por duas razões que a segunda passagem adversarial mediu. A primeira
// é custo: a versão anterior fazia o selo RE-CODIFICAR em base64 para chamar a conferência da
// porta, que RE-DESCODIFICAVA — um encode, um decode e um sha256 a mais por hospedagem, num
// comentário que afirmava não redescodificar nada. A segunda é ATRIBUIÇÃO: o selo salta as
// entradas já seladas, pelo que os índices da lista filtrada apontavam para a entrada errada no
// Goal. Com o índice a vir de quem chama, as duas vias nomeiam a mesma entrada.
func conferirUmaEntrada(i int, tipo, digest string, dados []byte, total *int) string {
	if !tipoDeMediaAceite(tipo) {
		return fmt.Sprintf("media_inputs[%d].media_type fora do vocabulario (aceites: %s)", i, tiposDeMediaAceites())
	}
	if len(dados) == 0 {
		return fmt.Sprintf("media_inputs[%d].content vazio", i)
	}
	if len(dados) > maxMediaInputBytes {
		return fmt.Sprintf("media_inputs[%d] acima do tamanho maximo (%d bytes)", i, maxMediaInputBytes)
	}
	*total += len(dados)
	if *total > maxMediaInputsBytes {
		return fmt.Sprintf("media_inputs acima do tamanho agregado maximo (%d bytes)", maxMediaInputsBytes)
	}
	if !strings.HasPrefix(digest, mediaInputDigestAlgo) {
		return fmt.Sprintf("media_inputs[%d] sem digest sha256", i)
	}
	soma := sha256.Sum256(dados)
	if !hmac.Equal([]byte(hex.EncodeToString(soma[:])), []byte(strings.TrimPrefix(digest, mediaInputDigestAlgo))) {
		return fmt.Sprintf("digest de media_inputs[%d] nao corresponde aos bytes", i)
	}
	// OS BYTES DECIDEM. A causa vem do vocabulário FECHADO da porta do gateway
	// ([port.ImagePartError.Cause]) e não leva um byte de conteúdo nem o tipo declarado.
	if verr := (port.ImagePart{MediaType: tipo, Data: dados}).Validate(); verr != nil {
		var pe *port.ImagePartError
		if errors.As(verr, &pe) {
			return fmt.Sprintf("media_inputs[%d] recusado pelos bytes: %s", i, pe.Cause)
		}
		return fmt.Sprintf("media_inputs[%d] recusado pelos bytes", i)
	}
	return ""
}

// mediaInputHandleTool é o nome que a PROVENIÊNCIA de um handle de entrada de media leva no
// `handle.created` e no [agentruntime.HandleFacts.Name] que o nó cunha (`<tool>-<ordinal>`).
//
// É ELE QUE DISTINGUE UMA ENTRADA INGERIDA DE UM `tool_result`, que é o que a peça B do desenho
// pede. E é o campo CERTO para o fazer: o `taint.Origin` NÃO distingue, de propósito e por uma
// razão medida que já está registada em [planoDeHandles.quarentenaDoRun] — a reconstrução da
// quarentena de um run adopta TODOS os handles com [taint.OriginToolResult], pelo que uma origem
// diferente na selagem tornava o handle VIVO e o handle RECONSTRUÍDO dois objectos diferentes. As
// duas origens classificam untrusted, pelo que a etiqueta não decide nada — o que ela serve é o
// forense, e para o forense é o `Tool` do evento que se lê.
//
// É UM RÓTULO, e nenhum guarda depende dele: a resolução confere o par `(digest, titular selador)`
// contra os `handle.created` do run, e o token é cunhado de `(run, digest, ordinal)`. Como em
// [planInputHandleTool], NÃO se afirma aqui que o nó não oferece uma tool com este nome — as tools
// vêm de um ficheiro do operador e uma colisão é possível. O que uma colisão produz é proveniência
// AMBÍGUA num evento de auditoria, nunca um handle resolvível por quem não devia.
const mediaInputHandleTool = "media_input"

// mediaInputHandleStep é o passo que a proveniência de um handle de entrada de media leva. As
// entradas são seladas ANTES do primeiro turno, logo não há passo de tool call a nomear; o que as
// distingue entre si é o ORDINAL do token, não o passo.
const mediaInputHandleStep = "media-input"

// mediaPorSelar diz se [selarMediaDoPedido] vai CUNHAR pelo menos um handle para este Goal — a
// mesma pergunta que [haEntradasPorSelar] faz para o âmbito dos portões do arranque, e a mesma
// que o interruptor da via faz no caminho em-processo: vai ser escrito algum byte novo?
//
// NÃO É «TEM MEDIA?», e a diferença é o que impede o interruptor de alcançar o que está em voo:
// um run retomado chega com as entradas JÁ SELADAS e não escreve byte nenhum.
func mediaPorSelar(goal agentruntime.Goal) bool {
	for i := range goal.MediaInputs {
		if goal.MediaInputs[i].Handle == "" {
			return true
		}
	}
	return false
}

// conferirMediaDoGoal confere as entradas de media de um Goal pelas MESMAS regras da porta, e
// devolve a causa da primeira que não passe. É o que torna [selarMediaDoPedido] independente de
// quem o chama: pela porta HTTP a conferência é REDUNDANTE — corre uma segunda vez sobre os mesmos
// bytes, e esse custo fica MEDIDO aqui em vez de negado: um sha256 e um prefixo por entrada, sobre
// bytes JÁ DESCODIFICADOS. A versão anterior re-codificava para base64 para a conferência da porta
// os voltar a descodificar, e o comentário dizia que não redescodificava nada — achado da segunda
// passagem adversarial. Pelo caminho em-processo é a única que corre.
//
// NÃO REPETE AS REGRAS: chama [conferirUmaEntrada], a MESMA função que a porta usa, sobre os bytes
// que já tem. Duas definições do que é uma entrada válida divergiriam no PR seguinte, que é a
// razão pela qual o vocabulário de tipos também não é uma segunda cópia das strings do gateway.
func conferirMediaDoGoal(entradas []agentruntime.MediaInput) string {
	if len(entradas) > maxMediaInputs {
		return "media_inputs acima do tecto"
	}
	total := 0
	for i := range entradas {
		// Uma entrada JÁ SELADA não traz bytes para conferir — o selo limpa-os —, e o que a
		// amarra aos bytes é o `handle.created` do run, não este Goal. Salta, e NÃO conta no
		// agregado: o que o agregado limita é o que esta chamada vai descodificar e selar.
		if entradas[i].Handle != "" {
			continue
		}
		// O ÍNDICE É O DO GOAL, não o de uma lista filtrada — ver [conferirUmaEntrada].
		if causa := conferirUmaEntrada(i, entradas[i].MediaType, entradas[i].Digest, entradas[i].Content, &total); causa != "" {
			return causa
		}
	}
	return ""
}

var (
	// errMediaInvalida — uma entrada de media do Goal não passa a conferência da porta. Só é
	// alcançável pelo caminho EM-PROCESSO: pela porta HTTP a mesma conferência já recusou com 400.
	errMediaInvalida = errors.New("aos: entrada de media invalida")
	// errMediaViaDesligada — um run com entradas de media num no que NAO habilita a via. Recusa a
	// hospedagem: a porta HTTP ja o recusa com 400, e esta guarda fecha o caminho EM-PROCESSO, que
	// nao passa pela porta. Um interruptor que so metade dos caminhos respeita nao e um interruptor.
	errMediaViaDesligada = errors.New("aos: run com entradas de media num no que nao habilita a via (AOS_ENTRADA_DE_MEDIA)")
	// errMediaSemEspacoDeHandles — um run com entradas de media POR SELAR num nó sem espaço de
	// handles ligado. Recusa a hospedagem.
	//
	// É ALCANÇÁVEL, e dizer o contrário era a afirmação que a segunda passagem adversarial apanhou
	// aqui: o arranque recusa `on` sem `AOS_HANDLE_DIR` ([ErrEntradaDeMediaSemHandles]), mas o
	// operador pode fazer o recuo DOCUMENTADO das duas variáveis e um run com entradas POR SELAR
	// chega cá pelo caminho em-processo. O que a guarda estreita de [selarMediaDoPedido] garante é
	// que um run cuja media está toda SELADA nunca bate aqui — esse continua.
	errMediaSemEspacoDeHandles = errors.New("aos: run com entradas de media num no sem espaco de handles ligado (AOS_HANDLE_DIR)")
	// errMediaNaoSelada — a selagem de uma entrada de media recusou. A causa vem do vocabulário
	// fechado do espaço de handles.
	errMediaNaoSelada = errors.New("aos: a selagem de uma entrada de media recusou")
)

// selarMediaDoPedido é o PRODUTOR da entrada de media (AOS-561): CONFERE cada entrada, sela-lhe os
// bytes no espaço de handles do run, escreve-lhe a referência e LIMPA os bytes do Goal.
//
// CONFERE O QUE SELA, e não confia no que o chamador traz — achado da revisão adversarial, e o
// defeito era real: pela porta HTTP as entradas passaram por [validarMediaInputs], mas o caminho
// EM-PROCESSO ([NodeService.Submit]/[NodeService.SubmitObservando]) não passa pela porta, e ali um
// `MediaType` que ninguém conferiu ia direito aos factos do `handle.created` e um `Digest` podia
// mentir sobre os bytes (o espaço deriva o seu próprio e nunca compara com o declarado). A
// conferência é a MESMA função da porta: um só sítio decide o que é uma entrada de media válida.
//
// CORRE EM TODOS OS LAYOUTS, ao contrário de [selarEntradasDoPlano]. É a diferença que o cabeçalho
// deste ficheiro explica: o que põe uma entrada de media fora do contexto do modelo não é o
// layout, é o facto de ela não ser um segmento de tail em layout nenhum.
//
// CORRE ANTES DE [selarEntradasDoPlano], e a ordem é um CONTRATO, não uma conveniência: o
// [agentruntime.HandleFacts.Name] é `<tool>-<ordinal>` com o ordinal GLOBAL do run
// ([armazemDeHandles.selar]), pelo que selar a media primeiro dá-lhe os nomes `media_input-1..k`,
// previsíveis pela ordem de submissão — que é como o OBJECTIVO do run as pode nomear. Selar
// depois das entradas do plano fazia o nome depender de quantas elas fossem.
//
// OS BYTES SAEM DO GOAL. Depois do selo, `Content` fica vazio: o registo de retoma do run é
// escrito DEPOIS desta função ([NodeService.hostRun]) e serializa o Goal, pelo que deixar os bytes
// aqui gravava uma SEGUNDA cópia do conteúdo fora do espaço de handles — em claro no corpo de um
// evento, sem a cifra por titular e sem o run nos dados associados. É o oposto do que a via
// promete. O que fica é o que basta: a referência, o digest, o tipo e o nome.
//
// O QUE ESTA CHAMADA SELAR DESFAZ-SE SE UMA ENTRADA A SEGUIR RECUSAR, pelo molde do AOS-556: sem
// isto, uma recusa na entrada k deixava no espaço os bytes das k-1 anteriores, de um run que nunca
// arranca, que ninguém consegue resolver (o guarda da resolução exige o par (titular, run, digest)
// de um run que existe) nem apagar (nada apaga ficheiros deste espaço).
//
// E O ALCANCE DO DESFAZER ESTREITOU com a conferência acima, que é o que vale registar: NENHUMA
// causa accionável por quem submete chega ao meio de uma selagem — um tipo mau, um digest que mente
// ou uma entrada acima do tecto recusam ANTES do primeiro selo, e aí não há nada para desfazer. O
// que resta são as causas do SUBSTRATO (volume cheio, tecto de handles do run, cifra, disco,
// registo), e por elas o alcance é o tecto AGREGADO do canal ([maxMediaInputsBytes], 1 MiB). O
// desfazer não é código morto, e um teste entra por essa porta em vez de o deixar numa frase.
//
// O `handle.created` de cada uma FICA: o stream é append-only e não há evento de compensação. Uma
// declaração de um run que nunca corre é inofensiva — o que ela declarava deixou de existir, e
// nenhum run a pode usar.
//
// A JANELA DO CRASH é a mesma que [selarEntradasDoPlano] declara: o processo morrer entre os selos
// e o registo de retoma não tem `defer` que sobreviva, e o ticket é o reaper por run, AOS-554.
func selarMediaDoPedido(ctx context.Context, p *planoDeHandles, goal agentruntime.Goal) (agentruntime.Goal, error) {
	if len(goal.MediaInputs) == 0 {
		return goal, nil
	}
	// A MESMA PERGUNTA ESTREITA, e não «tem media?» — achado da SEGUNDA passagem adversarial, que
	// encontrou aqui o defeito que a correcção da primeira tinha fechado uma linha acima. Um run
	// cuja media está TODA selada não precisa do espaço para continuar (nenhuma tool o lê ainda), e
	// recusá-lo matava um run RECUPERÁVEL: basta o operador fazer o recuo DOCUMENTADO das duas
	// variáveis — `AOS_ENTRADA_DE_MEDIA=off` e `AOS_HANDLE_DIR` vazia, que tem de ser das duas
	// porque `on` sem handles recusa o arranque — e a retoma pela API ou a varredura de crash
	// reconstituem o Goal do registo, com as entradas seladas, para este `if` fechar o run `failed`
	// sem um turno.
	//
	// O PARALELO DO PLANO É DIFERENTE, e é por isso que ele pode ser largo: lá
	// ([errEntradasSemEspacoDeHandles]) o layout do run EXIGE o espaço em cada turno, pelo que um
	// run nessas condições morre de qualquer modo e mais tarde; uma entrada de media selada não
	// precisa de nada para o run continuar.
	// A LIMPEZA DOS BYTES VEM PRIMEIRO, antes de qualquer decisão — e a ordem é uma correcção de
	// correcção que um teste apanhou. A guarda estreita acima (`!mediaPorSelar`) retorna cedo para
	// um Goal cujas entradas estão TODAS seladas; posta antes da limpeza, saltava-a, e uma entrada
	// que chegasse com referência E bytes levava-os ao registo de retoma. A limpeza é sempre
	// correcta e não depende de nada, pelo que corre primeiro.
	//
	// Cópia: o Goal do chamador não fica meio-selado se uma entrada a seguir recusar.
	entradas := make([]agentruntime.MediaInput, len(goal.MediaInputs))
	copy(entradas, goal.MediaInputs)
	// O DESFAZER precisa de saber que ficheiros são de entradas JÁ SELADAS antes de os bytes delas
	// desaparecerem — ver o bloco de `intocaveis`, abaixo.
	intocaveis := map[string]struct{}{}
	for i := range entradas {
		if entradas[i].Handle == "" {
			continue
		}
		// Só uma entrada selada que AINDA traga bytes nomeia um digest derivavel aqui. Uma selada
		// SEM bytes não o nomeia — e não precisa: se o conteúdo dela fosse igual ao de uma por
		// selar, as duas teriam o mesmo digest e a mesma referência, logo a por selar já viria
		// selada. Fica declarado em vez de assumido.
		if len(entradas[i].Content) > 0 {
			intocaveis[digestHex(entradas[i].Content)] = struct{}{}
		}
		// OS BYTES SAEM DAQUI. Uma entrada que chegue com referência E bytes não é re-selada, mas
		// deixar-lhe o `Content` punha uma segunda cópia do conteúdo no registo de retoma —
		// exactamente o que esta função promete evitar.
		entradas[i].Content = nil
	}
	goal.MediaInputs = entradas
	// A MESMA PERGUNTA ESTREITA do interruptor, e não «é preciso o espaço?»: um Goal todo selado
	// sai AQUI, já limpo, e sem tocar no espaço de handles.
	if !mediaPorSelar(goal) {
		return goal, nil
	}
	if p == nil {
		return goal, fmt.Errorf("%w (run %q)", errMediaSemEspacoDeHandles, goal.RunID)
	}
	if causa := conferirMediaDoGoal(entradas); causa != "" {
		return goal, fmt.Errorf("%w: run %q: %s", errMediaInvalida, goal.RunID, causa)
	}
	// O DESFAZER DESARMA-SE POR DIGEST, e não para a chamada inteira — achado da SEGUNDA passagem
	// adversarial. A razão do desarmar é a de [selarEntradasDoPlano]: um Goal com entradas já
	// seladas vem do REGISTO DE RETOMA, e [armazemDeHandles.gravar] reaproveita o ficheiro do mesmo
	// conteúdo no mesmo run, pelo que apagar tirava o chão a uma entrada que veio do registo.
	//
	// MAS DESARMAR TUDO ERA LARGO DE MAIS: num Goal MISTO (uma selada, outras por selar — forma que
	// [mediaPorSelar] admite) uma recusa do substrato na entrada k deixava no espaço os bytes das
	// que ESTA chamada selou, que é exactamente o resíduo que o desfazer existe para evitar. O que
	// não se pode apagar é o ficheiro que uma entrada JÁ SELADA partilha; os outros apagam-se.
	seladas := map[string]struct{}{}
	desfazer := func() {
		for hexd := range seladas {
			if _, protegido := intocaveis[hexd]; protegido {
				continue
			}
			p.armazem.apagar(goal.RunID, hexd)
		}
	}
	for i := range entradas {
		if entradas[i].Handle != "" {
			continue // já selada, e os bytes dela já saíram acima
		}
		ref, causa := p.selarEmQuarentena(ctx, goal.Titular(), goal.RunID, mediaInputHandleStep, mediaInputHandleTool, entradas[i].MediaType, entradas[i].Content)
		if causa != "" {
			desfazer()
			return goal, fmt.Errorf("%w: entrada %d (%s) do run %q: %s", errMediaNaoSelada, i, entradas[i].MediaType, goal.RunID, causa)
		}
		// Pela MESMA função com que o espaço nomeia o ficheiro ([digestHex] em
		// [armazemDeHandles.selar]): uma segunda derivação do nome fazia o desfazer deixar de
		// encontrar o que apagar, e [armazemDeHandles.apagar] é best-effort, pelo que falharia em
		// SILÊNCIO.
		seladas[digestHex(entradas[i].Content)] = struct{}{}
		entradas[i].Handle = ref
		// OS BYTES SAEM DAQUI. Ver o cabeçalho desta função.
		entradas[i].Content = nil
	}
	goal.MediaInputs = entradas
	return goal, nil
}

// entradaDeMediaBanner é a linha de arranque da via. Com `off` não sai linha nenhuma: o arranque é
// o de antes.
//
// `tectoDoCorpo` é o tecto do corpo de um pedido EM VIGOR neste nó (`cfg.maxBodyBytes`), e entra
// aqui porque é ele que MORDE primeiro — declarar os tectos de media sem declarar qual deles manda
// seria anunciar uma capacidade que o nó não tem.
func entradaDeMediaBanner(ligado bool, tectoDoCorpo int64) []string {
	if !ligado {
		return nil
	}
	// O que cabe de bytes de conteúdo no corpo todo: a base64 gasta 4 bytes por cada 3.
	cabeNoCorpo := tectoDoCorpo / 4 * 3
	// QUEM MANDA EM CADA EIXO, calculado e não afirmado. Declarar só um dos dois anunciava uma
	// capacidade pela metade.
	mandaNaEntrada := "o tecto da ENTRADA"
	if cabeNoCorpo < maxMediaInputBytes {
		mandaNaEntrada = "o tecto do CORPO DO PEDIDO"
	}
	mandaNoConjunto := "o tecto AGREGADO da media"
	if cabeNoCorpo < maxMediaInputsBytes {
		mandaNoConjunto = "o tecto do CORPO DO PEDIDO"
	}
	return []string{fmt.Sprintf("entrada de media (AOS-561, ADR-043 fase R2): AOS_ENTRADA_DE_MEDIA=on — o POST /runs aceita media_inputs: bytes em base64 no corpo do pedido, vocabulario FECHADO (%s, o que o Model Gateway aceita), tipo conferido PELOS PRIMEIROS BYTES (um ficheiro que declare um tipo e nao o seja RECUSA com 400, antes de o run existir) e digest sha256 dos BYTES conferido na porta. Tectos PROPRIOS, separados dos de texto e contados ANTES de o run existir: %d bytes por entrada (o MENOR dos dois tectos a jusante — o do espaco de handles e os %d do gateway), %d bytes no CONJUNTO (o tecto do trabalho que o no faz ANTES de autenticar: NAO e derivado dos outros, de proposito, para nao se mover com nenhum deles) e %d entradas (posicoes de handle, com folga de 4x contra o tecto do run). QUEM MANDA HOJE: POR ENTRADA, %s; NO CONJUNTO, %s — o corpo em vigor sao %d bytes, que em base64 dao %d bytes de conteudo no TOTAL. Uma entrada de ate %d bytes ENTRA HOJE sem mexer em nada; para uma fotografia de %d bytes entrar sao precisas TRES mudancas de postura: o tecto do corpo (AOS_INGRESS_MAX_BODY, AOS-562), o tecto por handle E este agregado. Os bytes sao SELADOS no espaco de handles do run antes de o run abrir, cifrados pelo cifrador por titular com o run nos dados associados, declarados num handle.created com tool=%s e o tipo nos factos do handle, e NAO entram no contexto do modelo em layout nenhum (uma entrada de media nao e um segmento de tail: o prompt_hash nao depende dela). NENHUMA ROTA HTTP SERVE O CONTEUDO, como o resto do espaco de handles. O objectivo do run nomeia cada entrada pelo nome que o NO cunha, %s-1..n pela ordem de submissao — nunca por um nome escolhido por quem submete. CONSEQUENCIA, e e do DONO: o conteudo ingerido NAO E REDIGIDO (a fronteira de ingestao trata so o objectivo), e quem o pode causar passa a ser quem SUBMETE e nao so quem semeia a pasta do servidor; o que o contem e o SA-ROC pela sensibilidade efectiva, o taint, o egress default-deny e o crypto-shred do titular — redaccao de IMAGEM nao existe. O QUE ISTO AINDA NAO COMPRA: NENHUMA TOOL LE DESTE ESPACO POR doc_id — a ocr_read e a media_view continuam a ler SO da pasta semeada (AOS_OCR_SEED_DIR), e quem as ensina a resolver o doc_id no espaco de handles do run e o AOS-563. Ate la esta via SELA e DECLARA a entrada, e nenhum run a consome. Recuo: off",
		tiposDeMediaAceites(), maxMediaInputBytes, port.MaxImageBytes, maxMediaInputsBytes,
		maxMediaInputs, mandaNaEntrada, mandaNoConjunto, tectoDoCorpo, cabeNoCorpo,
		maxMediaInputBytes, port.MaxImageBytes, mediaInputHandleTool, mediaInputHandleTool)}
}
