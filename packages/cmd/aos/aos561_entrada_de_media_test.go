package main

// aos561_entrada_de_media_test.go — A VIA DE ENTRADA (AOS-561) e a superfície do tecto do corpo
// (AOS-562).
//
// O QUE ESTES TESTES AMARRAM, e porque cada um existe:
//
//  1. O TECTO DA PORTA NUNCA PASSA O DO DESTINO. É a medição que o desenho de 2026-10-25 NÃO fez:
//     o tecto por handle são 256 KiB, não os 4 MiB do gateway, e é ele que manda. Um teste fixa a
//     derivação para que subir um dos dois não deixe a porta a aceitar o que o selo recusa.
//  2. OS BYTES DECIDEM O TIPO, não o campo. Um PNG declarado sobre bytes de JPEG recusa.
//  3. O SELO LIMPA OS BYTES do Goal — é o que mantém o conteúdo de media fora do registo de retoma.
//  4. UMA RECUSA A MEIO NÃO DEIXA BYTES no espaço de handles.
//  5. OS PORTÕES DO AOS-557 COBREM O SELO DA MEDIA: sem isto um run só com media selava os bytes
//     antes dos portões fail-closed, que é o defeito que o AOS-557 fechou pelo outro lado.
//  6. A PORTA RECUSA `media_inputs` num nó que não habilita a via — nunca aceita-e-ignora.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/substrate/eventstore"
)

// Bytes mínimos de cada formato: a magia e um corpo, que é o que [port.ImagePart.Validate] confere.
func aos561PNG(n int) []byte {
	return append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, bytes.Repeat([]byte{0x7f}, n)...)
}

func aos561JPEG(n int) []byte {
	return append([]byte{0xff, 0xd8, 0xff}, bytes.Repeat([]byte{0x7f}, n)...)
}

// aos561Wire é a entrada de wire bem formada para estes bytes: o digest é dos BYTES e o conteúdo
// vai em base64 padrão, que é o contrato da porta.
func aos561Wire(tipo string, b []byte) mediaInputWire {
	soma := sha256.Sum256(b)
	return mediaInputWire{
		MediaType: tipo,
		Digest:    "sha256:" + hex.EncodeToString(soma[:]),
		Content:   base64.StdEncoding.EncodeToString(b),
	}
}

// O TECTO DA PORTA NUNCA PASSA O DO DESTINO — a medição que faltava ao desenho.
//
// O caminho de uma entrada de media tem dois tectos a jusante da porta: o do espaço de handles,
// onde os bytes são selados (`handleMaxBytes`, que o KERNEL também impõe em
// [agentruntime.Quarantine.Seal]), e o do Model Gateway, que é quem os recebe
// ([port.MaxImageBytes]). Aceitar na porta mais do que o MENOR dos dois é aceitar o que vai ser
// recusado depois, com o corpo já lido e o custo já pago.
//
// Este teste é o que impede essa divergência de voltar: muda-se um dos dois tectos e ele fala.
func TestAOS561_TectoDaPortaNaoPassaONenhumDosDestinos(t *testing.T) {
	t.Parallel()
	if maxMediaInputBytes > handleMaxBytes {
		t.Errorf("maxMediaInputBytes=%d passa o tecto por handle=%d: a porta aceitaria o que o selo recusa", maxMediaInputBytes, handleMaxBytes)
	}
	if maxMediaInputBytes > port.MaxImageBytes {
		t.Errorf("maxMediaInputBytes=%d passa o tecto do gateway=%d: a porta aceitaria o que o modelo recusa", maxMediaInputBytes, port.MaxImageBytes)
	}
	// E NÃO É MENOR DO QUE PRECISA: é exactamente o menor dos dois, não um número à mão.
	if esperado := min(handleMaxBytes, port.MaxImageBytes); maxMediaInputBytes != esperado {
		t.Errorf("maxMediaInputBytes=%d; quero o MENOR dos dois tectos a jusante (%d)", maxMediaInputBytes, esperado)
	}
	// O AGREGADO TEM DE MORDER, e esta é a asserção que faltava à primeira versão: ela tinha um
	// agregado IGUAL ao produto (inerte), a correcção foi TIRÁ-LO, e tirá-lo prendeu o conjunto ao
	// tecto do CORPO — que é do operador e sobe. Morde ⇔ é estritamente menor que o produto.
	if produto := maxMediaInputs * maxMediaInputBytes; maxMediaInputsBytes >= produto {
		t.Errorf("maxMediaInputsBytes=%d nao e menor que o produto contagem x entrada (%d): e INERTE, e um tecto inerte anunciado como proteccao e pior do que nenhum", maxMediaInputsBytes, produto)
	}
	// E UMA ENTRADA NO TECTO TEM DE CABER NO AGREGADO, senão o agregado proibia o caso de uso.
	if maxMediaInputsBytes < maxMediaInputBytes {
		t.Errorf("maxMediaInputsBytes=%d nao chega para UMA entrada no tecto (%d)", maxMediaInputsBytes, maxMediaInputBytes)
	}
	// O agregado tem de caber no orçamento de bytes por run do espaço de handles: senão o selo
	// recusaria um conjunto que a porta aceitou.
	if maxMediaInputsBytes > handleMaxBytesPorRun {
		t.Errorf("maxMediaInputsBytes=%d passa o orcamento de bytes por run do espaco de handles=%d", maxMediaInputsBytes, handleMaxBytesPorRun)
	}
	// A FOLGA DA CONTAGEM É ASSERIDA, e não deixada numa frase: a media gasta no máximo um QUARTO
	// das posições de handle do run, para sobrarem as dos `tool_result`. Com `>` simples, um tecto
	// por handle mais baixo levava `maxMediaInputs` a 64 — todas as posições — e o teste passava.
	if maxMediaInputs*4 > handleMaxPorRun {
		t.Errorf("maxMediaInputs=%d nao deixa folga de 4x contra o tecto de handles por run=%d: uma submissao podia esgotar as posicoes e nao sobrava nenhuma para os tool_result", maxMediaInputs, handleMaxPorRun)
	}
}

// A PORTA, CASO A CASO. A ordem das recusas é a da gravidade, e o caso que mais importa é o do
// tipo que não bate com os bytes: um `media_type` vem de quem submete e é untrusted.
func TestAOS561_PortaConfereOsBytesNaoOCampo(t *testing.T) {
	t.Parallel()
	bom := aos561Wire(port.ImageMediaPNG, aos561PNG(32))

	comTipo := func(w mediaInputWire, tipo string) mediaInputWire { w.MediaType = tipo; return w }

	for nome, c := range map[string]struct {
		wire []mediaInputWire
		erro string // substring esperada; vazia ⇒ aceita
	}{
		"png bem formado":            {[]mediaInputWire{bom}, ""},
		"jpeg bem formado":           {[]mediaInputWire{aos561Wire(port.ImageMediaJPEG, aos561JPEG(32))}, ""},
		"tipo fora do vocabulario":   {[]mediaInputWire{comTipo(bom, "image/gif")}, "media_type fora do vocabulario"},
		"tipo vazio":                 {[]mediaInputWire{comTipo(bom, "")}, "media_type fora do vocabulario"},
		"pdf, que o gateway nao tem": {[]mediaInputWire{comTipo(bom, "application/pdf")}, "media_type fora do vocabulario"},
		"base64 invalido": {[]mediaInputWire{{MediaType: port.ImageMediaPNG,
			Digest: bom.Digest, Content: "nao e base64!!"}}, "nao e base64 padrao"},
		"conteudo vazio": {[]mediaInputWire{{MediaType: port.ImageMediaPNG,
			Digest: bom.Digest, Content: ""}}, "content vazio"},
		"acima do tecto de uma entrada": {[]mediaInputWire{aos561Wire(port.ImageMediaPNG,
			aos561PNG(maxMediaInputBytes))}, "acima do tamanho maximo"},
		"sem prefixo de digest": {[]mediaInputWire{{MediaType: port.ImageMediaPNG,
			Digest: "deadbeef", Content: bom.Content}}, "sem digest sha256"},
		"digest de outros bytes": {[]mediaInputWire{{MediaType: port.ImageMediaPNG,
			Digest: aos561Wire(port.ImageMediaPNG, aos561PNG(64)).Digest, Content: bom.Content}}, "nao corresponde aos bytes"},
		"demasiadas entradas": {aos561Repetir(bom, maxMediaInputs+1), "media_inputs acima do tecto"},
	} {
		got, msg := validarMediaInputs(c.wire)
		switch {
		case c.erro == "" && msg != "":
			t.Errorf("%s: recusado com %q; queria aceitar", nome, msg)
		case c.erro == "" && len(got) != len(c.wire):
			t.Errorf("%s: %d entradas convertidas, quero %d", nome, len(got), len(c.wire))
		case c.erro != "" && !strings.Contains(msg, c.erro):
			t.Errorf("%s: recusa = %q; quero conter %q", nome, msg, c.erro)
		case c.erro != "" && got != nil:
			t.Errorf("%s: recusou E devolveu %d entradas — a recusa tem de ser total", nome, len(got))
		}
	}

	// O CASO QUE MAIS IMPORTA, à parte porque merece a sua própria asserção: o tipo DECLARADO é
	// válido, o digest BATE, o tamanho cabe — e os bytes são de outro formato. A porta recusa, e a
	// causa vem do vocabulário fechado do gateway.
	mentiroso := aos561Wire(port.ImageMediaPNG, aos561JPEG(32))
	_, msg := validarMediaInputs([]mediaInputWire{mentiroso})
	if !strings.Contains(msg, port.ImageCauseMagic) {
		t.Fatalf("png declarado sobre bytes de jpeg: recusa = %q; quero a causa %q", msg, port.ImageCauseMagic)
	}

	// A CONTAGEM CORTA exactamente no tecto: nele passa, uma acima recusa (já na tabela).
	noTecto := aos561Repetir(aos561Wire(port.ImageMediaPNG, aos561PNG(8)), maxMediaInputs)
	if _, msg := validarMediaInputs(noTecto); msg != "" {
		t.Fatalf("%d entradas (exactamente no tecto) recusadas com %q", maxMediaInputs, msg)
	}

	// E O AGREGADO MORDE: entradas que cabem UMA A UMA, em número abaixo do tecto de contagem, e
	// que somadas passam o agregado. É a asserção que a primeira versão não tinha — e sem ela um
	// agregado inerte (ou ausente) passava verde.
	quantas := maxMediaInputsBytes/maxMediaInputBytes + 1
	if quantas > maxMediaInputs {
		t.Fatalf("o agregado (%d) nao e alcancavel abaixo do tecto de contagem (%d entradas de %d bytes): e inerte", maxMediaInputsBytes, maxMediaInputs, maxMediaInputBytes)
	}
	somadas := aos561Repetir(aos561Wire(port.ImageMediaPNG, aos561PNG(maxMediaInputBytes-8)), quantas)
	if _, msg := validarMediaInputs(somadas); !strings.Contains(msg, "agregado") {
		t.Fatalf("%d entradas no tecto individual: recusa = %q; quero a do agregado", quantas, msg)
	}
}

func aos561Repetir(w mediaInputWire, n int) []mediaInputWire {
	out := make([]mediaInputWire, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, w)
	}
	return out
}

// aos561PlanoComVolume compõe o espaço de handles com um tecto de VOLUME próprio — o molde do
// `aos550Plano`, que o fixa em 1 GiB. É o que permite alcançar a única classe de recusa que ainda
// chega ao meio de uma selagem: a do substrato.
func aos561PlanoComVolume(t *testing.T, maximo int64) (*planoDeHandles, *armazemDeHandles, eventstore.EventStore) {
	t.Helper()
	esp, err := abrirArmazemDeHandles(t.TempDir(), "", maximo, newContentSealer(audit.NewInMemoryKeyVault(nil), audit.NewInMemorySubjectPartitionIndex()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(esp.fechar)
	es, err := eventstore.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = es.Close() })
	plano, err := comporPlanoDeHandles(esp, es, aos550Specs)
	if err != nil {
		t.Fatal(err)
	}
	return plano, esp, es
}

func aos561Goal(entradas []agentruntime.MediaInput) agentruntime.Goal {
	return agentruntime.Goal{
		RunID:           "run-561-media",
		Principal:       referencemonitor.Principal{NHIID: "nhi:a"},
		Subject:         "titular-561",
		AssemblyVersion: agentruntime.AssemblyVersion170,
		MediaInputs:     entradas,
	}
}

// O SELO CUNHA A REFERÊNCIA, DECLARA O TIPO NOS FACTOS E LIMPA OS BYTES.
//
// As três coisas num teste porque são a MESMA propriedade vista de três lados: depois do selo, os
// bytes vivem no espaço de handles (cifrados, com o run nos dados associados) e em mais lado
// nenhum. Se o `Content` sobrevivesse no Goal, o registo de retoma — que é escrito a seguir e
// serializa o Goal — gravava uma segunda cópia do conteúdo em claro no corpo de um evento.
//
// E CORRE NA 1.7.0, que é o layout da produção: o selo da media NÃO depende do layout, ao contrário
// do [selarEntradasDoPlano].
func TestAOS561_OSeloCunhaDeclaraELimpaOsBytes(t *testing.T) {
	plano, esp, es, _ := aos550Plano(t)
	ctx := context.Background()
	bytesDaFoto := aos561PNG(64)
	soma := sha256.Sum256(bytesDaFoto)
	goal := aos561Goal([]agentruntime.MediaInput{{
		MediaType: port.ImageMediaPNG,
		Digest:    "sha256:" + hex.EncodeToString(soma[:]),
		Content:   bytesDaFoto,
	}})

	selado, err := selarMediaDoPedido(ctx, plano, goal)
	if err != nil {
		t.Fatalf("o selo recusou: %v", err)
	}
	ref := selado.MediaInputs[0].Handle
	if _, ok := agentruntime.ParseHandleRef(ref); !ok {
		t.Fatalf("handle = %q; quero uma referencia que ParseHandleRef aceite", ref)
	}
	if selado.MediaInputs[0].Content != nil {
		t.Errorf("o selo deixou %d bytes no Goal — o registo de retoma gravava uma segunda copia do conteudo", len(selado.MediaInputs[0].Content))
	}
	// O GOAL DO CHAMADOR NÃO FOI TOCADO: o selo trabalha sobre uma cópia.
	if goal.MediaInputs[0].Handle != "" || goal.MediaInputs[0].Content == nil {
		t.Error("o selo mexeu no Goal do chamador em vez de devolver um novo")
	}
	// O EVENTO DECLARA O TIPO E A PROVENIÊNCIA — é ele que distingue uma entrada ingerida de um
	// `tool_result`, e é o que a peça C vai ler para resolver o `doc_id`.
	eventos, rerr := es.Read(ctx, goal.RunID, 1)
	if rerr != nil {
		t.Fatal(rerr)
	}
	declarados, ignorados := handlesDoRun(eventos)
	if ignorados != 0 {
		t.Fatalf("a leitura do stream deixou %d eventos de fora", ignorados)
	}
	if len(declarados) != 1 {
		t.Fatalf("handles declarados = %d, quero 1", len(declarados))
	}
	d := declarados[0]
	if d.MediaType != port.ImageMediaPNG {
		t.Errorf("media_type do handle = %q, quero %q", d.MediaType, port.ImageMediaPNG)
	}
	if d.Tool != mediaInputHandleTool {
		t.Errorf("tool do handle = %q, quero %q — e o que distingue uma entrada ingerida de um tool_result", d.Tool, mediaInputHandleTool)
	}
	if d.Digest != goal.MediaInputs[0].Digest {
		t.Errorf("digest do handle = %q, quero o da porta %q — os dois tem de ser o MESMO numero", d.Digest, goal.MediaInputs[0].Digest)
	}
	// O NOME É PREVISÍVEL PELA ORDEM, que é como o objectivo do run pode nomear a entrada.
	if d.Name != mediaInputHandleTool+"-1" {
		t.Errorf("nome cunhado = %q, quero %q", d.Name, mediaInputHandleTool+"-1")
	}
	// E OS BYTES ESTÃO LÁ, cifrados: a não-vacuidade de tudo acima.
	if usado := esp.usado.Load(); usado == 0 {
		t.Error("o espaco de handles ficou vazio — nada foi selado")
	}
}

// UMA RECUSA A MEIO NÃO DEIXA BYTES, pelo molde do AOS-556. Sem o desfazer, uma recusa na entrada
// k deixava no espaço os bytes das k-1 anteriores, de um run que nunca arranca — que ninguém
// consegue resolver nem apagar.
//
// O ALCANCE DESTE CAMINHO MUDOU, e vale dizê-lo: desde que o selo CONFERE o que sela (correcção da
// revisão adversarial), nenhuma causa ACCIONÁVEL POR QUEM SUBMETE chega aqui — um tipo mau, um
// digest que mente ou uma entrada acima do tecto recusam ANTES do primeiro selo, e aí não há nada
// para desfazer. O que resta são as causas do SUBSTRATO (volume cheio, tecto de handles do run,
// cifra, disco, registo), e é por uma delas que este teste entra: um espaço com o volume apertado.
// Sem este caminho o desfazer seria código morto, e é por isso que o teste existe.
func TestAOS561_RecusaAMeioDesfazOsBytesJaSelados(t *testing.T) {
	// VOLUME APERTADO: 100 KiB. A primeira entrada (64 bytes) cabe; a segunda (200 KiB, abaixo do
	// tecto por entrada) não cabe no volume, e `gravar` recusa ANTES de escrever.
	plano, esp, es := aos561PlanoComVolume(t, 100<<10)
	ctx := context.Background()
	boa := aos561PNG(64)
	somaBoa := sha256.Sum256(boa)
	grande := aos561PNG(200 << 10)
	somaGrande := sha256.Sum256(grande)
	goal := aos561Goal([]agentruntime.MediaInput{
		{MediaType: port.ImageMediaPNG, Digest: "sha256:" + hex.EncodeToString(somaBoa[:]), Content: boa},
		{MediaType: port.ImageMediaPNG, Digest: "sha256:" + hex.EncodeToString(somaGrande[:]), Content: grande},
	})
	if _, err := selarMediaDoPedido(ctx, plano, goal); !errors.Is(err, errMediaNaoSelada) {
		t.Fatalf("erro = %v; quero errMediaNaoSelada", err)
	}
	if usado := esp.usado.Load(); usado != 0 {
		t.Fatalf("a recusa deixou %d bytes no espaco de handles", usado)
	}
	// O `handle.created` da primeira FICA: o stream é append-only e não há evento de compensação.
	// A contagem é também a NÃO-VACUIDADE — sem ela, uma primeira entrada que também recusasse
	// dava o mesmo verde sem nada ter sido selado nem desfeito.
	eventos, err := es.Read(ctx, goal.RunID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(eventos) != 1 {
		t.Fatalf("eventos = %d; quero exactamente o handle.created da 1.ª entrada", len(eventos))
	}
}

// UMA ENTRADA QUE JÁ TRAZ HANDLE NÃO É RE-SELADA, e o desfazer fica DESARMADO. É o caminho da
// re-hospedagem de um run em curso (AOS-559): o Goal vem do registo de retoma, com as referências
// já cunhadas e sem bytes. Re-selar daria outro ordinal, logo outro token.
func TestAOS561_ReHospedagemNaoReSelaNemApaga(t *testing.T) {
	plano, esp, _, _ := aos550Plano(t)
	ctx := context.Background()
	b := aos561PNG(64)
	soma := sha256.Sum256(b)
	goal := aos561Goal([]agentruntime.MediaInput{{
		MediaType: port.ImageMediaPNG, Digest: "sha256:" + hex.EncodeToString(soma[:]), Content: b,
	}})
	selado, err := selarMediaDoPedido(ctx, plano, goal)
	if err != nil {
		t.Fatal(err)
	}
	usadoAntes := esp.usado.Load()

	// O MESMO Goal selado, outra vez — como o `hostRun` o recebe numa re-hospedagem.
	outra, err := selarMediaDoPedido(ctx, plano, selado)
	if err != nil {
		t.Fatalf("a re-hospedagem recusou: %v", err)
	}
	if outra.MediaInputs[0].Handle != selado.MediaInputs[0].Handle {
		t.Errorf("a re-hospedagem cunhou outro handle (%q, era %q): o token mudava debaixo do run",
			outra.MediaInputs[0].Handle, selado.MediaInputs[0].Handle)
	}
	if usado := esp.usado.Load(); usado != usadoAntes {
		t.Errorf("o espaco passou de %d a %d bytes numa re-hospedagem que nao devia escrever nem apagar", usadoAntes, usado)
	}
}

// SEM ESPAÇO DE HANDLES, O RUN COM MEDIA RECUSA. Fail-closed: não há onde pôr os bytes, e correr o
// run sem eles era entregar um run que não tem o material sobre o qual trabalha. Inalcançável pelo
// arranque ([ErrEntradaDeMediaSemHandles] recusa a composição) — este é o caminho em-processo.
func TestAOS561_SemEspacoDeHandlesORunRecusa(t *testing.T) {
	t.Parallel()
	b := aos561PNG(8)
	soma := sha256.Sum256(b)
	goal := aos561Goal([]agentruntime.MediaInput{{
		MediaType: port.ImageMediaPNG, Digest: "sha256:" + hex.EncodeToString(soma[:]), Content: b,
	}})
	if _, err := selarMediaDoPedido(context.Background(), nil, goal); !errors.Is(err, errMediaSemEspacoDeHandles) {
		t.Fatalf("erro = %v; quero errMediaSemEspacoDeHandles", err)
	}
	// E um run SEM media num nó sem espaço de handles não é tocado — a não-vacuidade.
	if g, err := selarMediaDoPedido(context.Background(), nil, aos561Goal(nil)); err != nil || g.MediaInputs != nil {
		t.Fatalf("um run sem media num no sem espaco: err=%v, entradas=%v; quero passar intacto", err, g.MediaInputs)
	}
}

// OS PORTÕES DO AOS-557 COBREM O SELO DA MEDIA.
//
// Sem isto, um run com SÓ entradas de media — que é o caso da via de entrada no layout 1.7.0, o da
// produção — selava os bytes e só depois batia nos portões fail-closed do
// [agentruntime.Runtime.Run], deixando no espaço o conteúdo de um run que nunca arranca. É o
// defeito que o AOS-557 fechou para as entradas do plano, reaberto pelo outro lado.
func TestAOS561_OsPortoesDoArranqueCobremOSeloDaMedia(t *testing.T) {
	t.Parallel()
	b := aos561PNG(8)
	soma := sha256.Sum256(b)
	comMedia := aos561Goal([]agentruntime.MediaInput{{
		MediaType: port.ImageMediaPNG, Digest: "sha256:" + hex.EncodeToString(soma[:]), Content: b,
	}})
	if !haEntradasPorSelar(comMedia) {
		t.Fatal("um run com media por selar tem de entrar no ambito dos portoes do AOS-557: senao os bytes selam ANTES deles")
	}
	// E uma entrada JÁ selada não traz o run para o âmbito: não vai ser escrito byte novo.
	selada := comMedia
	selada.MediaInputs = []agentruntime.MediaInput{{MediaType: port.ImageMediaPNG, Handle: "aos-handle/v1:h1.0a1b2c3d4e5f6071"}}
	if haEntradasPorSelar(selada) {
		t.Error("um run cujas entradas de media ja trazem handle nao escreve byte novo, logo esta fora do ambito")
	}
	// NÃO-VACUIDADE: um run sem nada continua fora do âmbito, como antes deste ticket.
	if haEntradasPorSelar(aos561Goal(nil)) {
		t.Error("um run sem entradas nenhumas nao pode entrar no ambito")
	}
}

// A PORTA RECUSA `media_inputs` NUM NÓ QUE NÃO HABILITA A VIA — nunca aceita-e-ignora, pelo molde
// do `conteudo_por_handle`: aceitar o campo daria a quem submete a ideia de que os bytes entraram
// quando nada os teria selado.
//
// Este nó é composto com `cfg.EntradaDeMedia = true` e SEM espaço de handles, pelo que a
// habilitação efectiva resolve FALSE.
func TestAOS561_SubmissaoRecusadaSemHabilitacao(t *testing.T) {
	n := aos486ComporCom(t, "native", func(cfg *Config) {
		cfg.EntradaDeMedia = true // pedida pelo operador…
		cfg.Privileged = referencemonitor.NewStaticPrivilegedSet("cap:http.post", durCap)
	})
	if n.node.entradaDeMedia {
		t.Fatal("sem o espaco de handles o no NAO pode habilitar a via de entrada de media")
	}
	foto := aos561Wire(port.ImageMediaPNG, aos561PNG(32))
	corpo := func(id string, comMedia bool) map[string]any {
		m := map[string]any{
			"run_id": id, "objective": aos540Objectivo,
			"principal_nhi": durAgent, "credential": n.tok, "tools": []string{"arquivo"},
			"max_turns": 4,
		}
		if comMedia {
			m["media_inputs"] = []map[string]string{{
				"media_type": foto.MediaType, "digest": foto.Digest, "content": foto.Content,
			}}
		}
		return m
	}
	rec := postJSON(n.h, "POST", "/runs", corpo("run-561-sem-hab", true))
	if rec.Code != 400 {
		t.Fatalf("POST /runs com media_inputs: %d %s, quero 400", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "media_inputs") {
		t.Fatalf("a recusa tem de ser a do campo, nao outra: %s", rec.Body.String())
	}
	// NÃO-VACUIDADE: o MESMO pedido sem o campo é aceite. Sem isto, a recusa acima passaria num nó
	// que recusasse tudo por outra razão (credencial, tools).
	rec2 := postJSON(n.h, "POST", "/runs", corpo("run-561-sem-campo", false))
	if rec2.Code != 201 {
		t.Fatalf("o mesmo pedido SEM o campo: %d %s, quero 201", rec2.Code, rec2.Body.String())
	}
}

// AOS-562 — A SUPERFÍCIE DO OPERADOR PARA O TECTO DO CORPO.
//
// O tecto existia como constante do binário e a [APIOption] que o muda só era alcançável por
// TESTES: `grep -rn WithMaxBodyBytes` dava três ficheiros, todos `_test.go`, e não havia variável
// de ambiente nenhuma. Quem opera não tinha como o mudar — a mesma lacuna que o AOS-277 fechou
// para os três primeiros números.
//
// O NÚMERO CONTINUA A SER DO DONO: por definir, fica o default de sempre.
func TestAOS562_TectoDoCorpoTemSuperficieDeOperador(t *testing.T) {
	for nome, c := range map[string]struct {
		valor  string
		quero  int64
		recusa bool
	}{
		"por definir fica o default de sempre": {"", DefaultMaxBodyBytes, false},
		"definida aplica-se":                   {"6291456", 6 << 20, false},
		"zero recusa o arranque":               {"0", 0, true},
		"negativa recusa o arranque":           {"-1", 0, true},
		"ilegivel recusa o arranque":           {"6MiB", 0, true},
	} {
		t.Run(nome, func(t *testing.T) {
			if c.valor == "" {
				t.Setenv("AOS_INGRESS_MAX_BODY", "")
			} else {
				t.Setenv("AOS_INGRESS_MAX_BODY", c.valor)
			}
			lim, opts, err := ingressLimitsFromEnv()
			if c.recusa {
				if !errors.Is(err, ErrBadIngressLimits) {
					t.Fatalf("AOS_INGRESS_MAX_BODY=%q: err = %v; quero ErrBadIngressLimits", c.valor, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("AOS_INGRESS_MAX_BODY=%q: %v", c.valor, err)
			}
			if lim.maxBody != c.quero {
				t.Errorf("maxBody = %d, quero %d", lim.maxBody, c.quero)
			}
			// POSTURA ANUNCIADA = POSTURA LIGADA: o valor lido tem de chegar às opções da API, e
			// não só ao banner. Sem isto o número era decorativo.
			cfg := apiConfig{maxBodyBytes: -1}
			for _, o := range opts {
				o(&cfg)
			}
			if cfg.maxBodyBytes != c.quero {
				t.Errorf("as APIOption aplicaram maxBodyBytes=%d, quero %d", cfg.maxBodyBytes, c.quero)
			}
		})
	}
}

// O BANNER DECLARA QUAL TECTO MORDE EM CADA EIXO, com o número. Declarar os tectos de media sem
// dizer qual deles manda era anunciar uma capacidade que o nó não tem. Com o corpo em 1 MiB (768
// KiB de conteúdo em base64): POR ENTRADA manda o tecto da entrada, 256 KiB, e uma entrada dessas
// entra (341 KiB em base64); NO CONJUNTO manda o CORPO, porque 768 KiB são menos do que o
// agregado de 1 MiB. Com o corpo em 6 MiB inverte-se, e o agregado passa a mandar — que é a
// propriedade que ele existe para ter.
func TestAOS561_BannerDeclaraQualTectoMorde(t *testing.T) {
	t.Parallel()
	if l := entradaDeMediaBanner(false, DefaultMaxBodyBytes); l != nil {
		t.Fatalf("com a via desligada nao sai linha nenhuma: %v", l)
	}
	linhas := entradaDeMediaBanner(true, DefaultMaxBodyBytes)
	if len(linhas) != 1 {
		t.Fatalf("linhas = %d, quero 1", len(linhas))
	}
	l := linhas[0]
	for _, deve := range []string{
		"AOS_ENTRADA_DE_MEDIA=on",
		port.ImageMediaPNG, port.ImageMediaJPEG,
		"PRIMEIROS BYTES",
		"POR ENTRADA, o tecto da ENTRADA", // 256 KiB < os 768 KiB que cabem no corpo de 1 MiB
		"NO CONJUNTO, o tecto do CORPO",   // 1 MiB de agregado não cabe nos 768 KiB do corpo
		"NAO E REDIGIDO",                  // a consequência que é do dono
		"AOS-563",                         // nenhuma tool le deste espaco ainda
	} {
		if !strings.Contains(l, deve) {
			t.Errorf("o banner nao declara %q: %s", deve, l)
		}
	}
	// Com o corpo subido ao que a media exige, no CONJUNTO passa a mandar o AGREGADO da media — e é
	// essa a propriedade que o agregado existe para ter: subir o corpo não sobe o conjunto.
	if l := entradaDeMediaBanner(true, 6<<20)[0]; !strings.Contains(l, "NO CONJUNTO, o tecto AGREGADO da media") {
		t.Errorf("com o corpo em 6 MiB quem manda no conjunto e o agregado da media: %s", l)
	}
}

// O SELO CONFERE O QUE SELA — e é o caminho EM-PROCESSO que o exige.
//
// Pela porta HTTP as entradas passaram por [validarMediaInputs]. Mas `Submit`/`SubmitObservando`
// não passam pela porta, e sem esta conferência um `MediaType` que ninguém viu ia direito aos
// factos do `handle.created` e um `Digest` podia mentir sobre os bytes (o espaço deriva o seu
// próprio e nunca compara com o declarado). Achado da revisão adversarial.
func TestAOS561_OSeloConfereOQueSela(t *testing.T) {
	plano, esp, _, _ := aos550Plano(t)
	ctx := context.Background()
	b := aos561PNG(64)
	soma := sha256.Sum256(b)
	bom := agentruntime.MediaInput{MediaType: port.ImageMediaPNG, Digest: "sha256:" + hex.EncodeToString(soma[:]), Content: b}

	comTipo := func(tipo string) agentruntime.MediaInput { e := bom; e.MediaType = tipo; return e }

	for nome, entrada := range map[string]agentruntime.MediaInput{
		"tipo fora do vocabulario":        comTipo("application/x-dosexec"),
		"tipo vazio":                      comTipo(""),
		"tipo que nao bate com os bytes":  {MediaType: port.ImageMediaJPEG, Digest: bom.Digest, Content: b},
		"digest que mente sobre os bytes": {MediaType: port.ImageMediaPNG, Digest: "sha256:" + hex.EncodeToString(make([]byte, 32)), Content: b},
		"acima do tecto de uma entrada": func() agentruntime.MediaInput {
			g := aos561PNG(maxMediaInputBytes)
			s := sha256.Sum256(g)
			return agentruntime.MediaInput{MediaType: port.ImageMediaPNG, Digest: "sha256:" + hex.EncodeToString(s[:]), Content: g}
		}(),
	} {
		goal := aos561Goal([]agentruntime.MediaInput{entrada})
		goal.RunID = "run-561-confere-" + strings.NewReplacer(" ", "-", ".", "-", "/", "-").Replace(nome)
		if _, err := selarMediaDoPedido(ctx, plano, goal); !errors.Is(err, errMediaInvalida) {
			t.Errorf("%s: err = %v; quero errMediaInvalida", nome, err)
		}
	}
	// NADA FOI SELADO por nenhuma delas: a conferência corre ANTES do primeiro selo.
	if usado := esp.usado.Load(); usado != 0 {
		t.Errorf("as recusas deixaram %d bytes no espaco de handles", usado)
	}
	// NÃO-VACUIDADE: a entrada boa sela.
	if g, err := selarMediaDoPedido(ctx, plano, aos561Goal([]agentruntime.MediaInput{bom})); err != nil || g.MediaInputs[0].Handle == "" {
		t.Fatalf("a entrada boa tinha de selar: err=%v, handle=%q", err, g.MediaInputs[0].Handle)
	}
}

// UMA ENTRADA QUE CHEGA COM REFERÊNCIA **E** BYTES TAMBÉM FICA SEM OS BYTES.
//
// O `continue` do selo saltava a limpeza, e o `Content` sobrevivia para o registo de retoma — uma
// segunda cópia do conteúdo fora do espaço de handles, exactamente o que o selo promete evitar.
// Achado da revisão adversarial.
func TestAOS561_EntradaJaSeladaComBytesPerdeOsBytes(t *testing.T) {
	plano, _, _, _ := aos550Plano(t)
	b := aos561PNG(64)
	soma := sha256.Sum256(b)
	goal := aos561Goal([]agentruntime.MediaInput{{
		MediaType: port.ImageMediaPNG,
		Digest:    "sha256:" + hex.EncodeToString(soma[:]),
		Content:   b,
		Handle:    "aos-handle/v1:h1.0a1b2c3d4e5f6071",
	}})
	selado, err := selarMediaDoPedido(context.Background(), plano, goal)
	if err != nil {
		t.Fatal(err)
	}
	if selado.MediaInputs[0].Content != nil {
		t.Errorf("a entrada ja selada ficou com %d bytes no Goal — o registo de retoma gravava uma segunda copia", len(selado.MediaInputs[0].Content))
	}
	if selado.MediaInputs[0].Handle == "" {
		t.Error("a referencia que ela trazia nao pode ser perdida")
	}
}

// A FORMA DE UMA IMAGEM É VERDADEIRA NO EVENTO.
//
// `formaBemFormada` não conhecia `image/png` nem `image/jpeg`, pelo que todo o handle de media saía
// com `well_formed:false` — sobre bytes que o nó acabara de provar bem formados na porta, com a
// mesma função do gateway. Um facto errado num registo imutável, e é o único facto de forma que o
// AOS-563 vai herdar. Achado da revisão adversarial.
func TestAOS561_AFormaDeUmaImagemEVerdadeiraNoEvento(t *testing.T) {
	t.Parallel()
	for nome, c := range map[string]struct {
		tipo  string
		dados []byte
		quero bool
	}{
		"png com a magia certa":    {port.ImageMediaPNG, aos561PNG(32), true},
		"jpeg com a magia certa":   {port.ImageMediaJPEG, aos561JPEG(32), true},
		"png declarado sobre jpeg": {port.ImageMediaPNG, aos561JPEG(32), false},
		"png vazio":                {port.ImageMediaPNG, nil, false},
	} {
		if got := formaBemFormada(c.tipo, c.dados); got != c.quero {
			t.Errorf("%s: formaBemFormada = %v, quero %v", nome, got, c.quero)
		}
	}
	// E o facto chega ao EVENTO: é ele que o forense e o AOS-563 lêem.
	plano, _, es, _ := aos550Plano(t)
	ctx := context.Background()
	b := aos561PNG(64)
	soma := sha256.Sum256(b)
	goal := aos561Goal([]agentruntime.MediaInput{{
		MediaType: port.ImageMediaPNG, Digest: "sha256:" + hex.EncodeToString(soma[:]), Content: b,
	}})
	goal.RunID = "run-561-forma"
	if _, err := selarMediaDoPedido(ctx, plano, goal); err != nil {
		t.Fatal(err)
	}
	eventos, err := es.Read(ctx, goal.RunID, 1)
	if err != nil {
		t.Fatal(err)
	}
	declarados, _ := handlesDoRun(eventos)
	if len(declarados) != 1 || !declarados[0].WellFormed {
		t.Fatalf("o handle.created de uma imagem conferida tem de declarar well_formed=true: %+v", declarados)
	}
}

// OS NOMES SÃO PREVISÍVEIS PELA ORDEM DE SUBMISSÃO, e é a única forma que quem submete tem de os
// nomear no objectivo — a resposta do `POST /runs` não os devolve e não há rota que os sirva.
func TestAOS561_OsNomesSeguemAOrdemDeSubmissao(t *testing.T) {
	plano, _, es, _ := aos550Plano(t)
	ctx := context.Background()
	var entradas []agentruntime.MediaInput
	for i := 0; i < 3; i++ {
		b := aos561PNG(16 + i) // bytes diferentes ⇒ digests diferentes ⇒ ficheiros diferentes
		soma := sha256.Sum256(b)
		entradas = append(entradas, agentruntime.MediaInput{
			MediaType: port.ImageMediaPNG, Digest: "sha256:" + hex.EncodeToString(soma[:]), Content: b,
		})
	}
	goal := aos561Goal(entradas)
	goal.RunID = "run-561-ordem"
	if _, err := selarMediaDoPedido(ctx, plano, goal); err != nil {
		t.Fatal(err)
	}
	eventos, err := es.Read(ctx, goal.RunID, 1)
	if err != nil {
		t.Fatal(err)
	}
	declarados, _ := handlesDoRun(eventos)
	if len(declarados) != 3 {
		t.Fatalf("handles declarados = %d, quero 3", len(declarados))
	}
	for i, d := range declarados {
		if quero := mediaInputHandleTool + "-" + strconv.Itoa(i+1); d.Name != quero {
			t.Errorf("entrada %d: nome = %q, quero %q — sem isto quem submete nao as pode nomear no objectivo", i, d.Name, quero)
		}
	}
}

// O INTERRUPTOR NÃO ALCANÇA O QUE ESTÁ EM VOO.
//
// A primeira versão da guarda do `hostRun` perguntava «tem media?», e isso matava um run
// RECUPERÁVEL: hospedado com a via ligada, retomado depois de o operador a desligar, chega com as
// entradas JÁ SELADAS do seu registo de retoma e era recusado. É o molde exacto da «correcção
// ingénua» que o AOS-557 declara ser pior do que o defeito — perda de capacidade em vez de
// protecção. A pergunta certa é «vai selar?», e é esta função que a faz, para o interruptor e para
// o âmbito dos portões do arranque.
func TestAOS561_OInterruptorNaoAlcancaORunRetomado(t *testing.T) {
	t.Parallel()
	b := aos561PNG(8)
	soma := sha256.Sum256(b)

	porSelar := aos561Goal([]agentruntime.MediaInput{{
		MediaType: port.ImageMediaPNG, Digest: "sha256:" + hex.EncodeToString(soma[:]), Content: b,
	}})
	if !mediaPorSelar(porSelar) {
		t.Error("uma entrada SEM handle vai ser selada: o interruptor tem de a alcancar")
	}

	// A FORMA QUE O REGISTO DE RETOMA DÁ: referência cunhada, sem bytes (o selo limpou-os).
	retomado := aos561Goal([]agentruntime.MediaInput{{
		MediaType: port.ImageMediaPNG,
		Digest:    "sha256:" + hex.EncodeToString(soma[:]),
		Handle:    "aos-handle/v1:h1.0a1b2c3d4e5f6071",
	}})
	if mediaPorSelar(retomado) {
		t.Error("um run retomado nao escreve byte nenhum: recusa-lo matava um run RECUPERAVEL, e o interruptor passava a alcancar o que esta em voo")
	}

	// E UM RUN MISTO vai selar: basta uma entrada sem handle.
	misto := aos561Goal(append(append([]agentruntime.MediaInput{}, retomado.MediaInputs...), porSelar.MediaInputs...))
	if !mediaPorSelar(misto) {
		t.Error("um Goal com UMA entrada por selar vai selar")
	}

	// É A MESMA PERGUNTA do âmbito dos portões do arranque, e não uma segunda cópia dela.
	if haEntradasPorSelar(retomado) {
		t.Error("um run retomado esta FORA do ambito dos portoes: nao ha byte novo para proteger")
	}
	if !haEntradasPorSelar(porSelar) {
		t.Error("um run com media por selar esta DENTRO do ambito dos portoes")
	}
}

// A PORTA LIGA-SE AO SELO — o percurso completo, pela rota HTTP, num nó com a via habilitada.
//
// Achado da SEGUNDA passagem adversarial, e o cenário dela era decisivo: todos os outros testes
// desta via chamam `validarMediaInputs` e `selarMediaDoPedido` DIRECTAMENTE, e o único que usa
// `media_inputs` num `POST /runs` é o da RECUSA. Apagar a linha `MediaInputs: mediaInputs` de
// `api.go` deixava a suite inteira verde: o run era aceite com 201, nada era selado, nenhum
// `handle.created` nascia — exactamente o «aceitar e ignorar» que esta via existe para proibir e
// que a sua recusa de 400 invoca como razão.
//
// O QUE ESTE TESTE AMARRA é a cablagem, não as regras: 201 na porta, e um `handle.created` no
// stream DESSE run com o tipo conferido e o nome cunhado.
func TestAOS561_DaPortaAoSelo(t *testing.T) {
	n := aos486ComporCom(t, "native", func(cfg *Config) {
		cfg.EntradaDeMedia = true
		cfg.HandleDir, cfg.HandleMaxBytes = t.TempDir(), 1<<30
		cfg.Privileged = referencemonitor.NewStaticPrivilegedSet("cap:http.post", durCap)
	})
	if !n.node.entradaDeMedia || n.node.planoHandles == nil {
		t.Fatalf("o no tinha de habilitar a via E compor o espaco de handles: via=%v espaco=%v",
			n.node.entradaDeMedia, n.node.planoHandles != nil)
	}
	foto := aos561Wire(port.ImageMediaPNG, aos561PNG(96))
	const runID = "run-561-porta"
	rec := postJSON(n.h, "POST", "/runs", map[string]any{
		"run_id": runID, "objective": "Transcreve a entrada media_input-1.",
		"principal_nhi": durAgent, "credential": n.tok, "tools": []string{"arquivo"},
		"max_turns": 2,
		"media_inputs": []map[string]string{{
			"media_type": foto.MediaType, "digest": foto.Digest, "content": foto.Content,
		}},
	})
	if rec.Code != 201 {
		t.Fatalf("POST /runs com media_inputs num no que habilita a via: %d %s", rec.Code, rec.Body.String())
	}

	// O SELO CORRE NA HOSPEDAGEM, que é assíncrona: espera-se pelo FACTO (o `handle.created` do
	// stream), não por um tempo. Sem prazo isto pendurava; com prazo e sem o facto, falha e diz o
	// que falta.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var declarados []handleCriado
	for ctx.Err() == nil {
		d, err := n.node.planoHandles.armazem.declarados(ctx, runID)
		if err == nil && len(d) > 0 {
			declarados = d
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(declarados) != 1 {
		t.Fatalf("o stream do run %q declara %d handle(s), quero 1 — a porta aceitou e NADA foi selado, que e o aceitar-e-ignorar que esta via proibe.\n%s",
			runID, len(declarados), n.log.String())
	}
	d := declarados[0]
	if d.Tool != mediaInputHandleTool || d.MediaType != port.ImageMediaPNG || d.Name != mediaInputHandleTool+"-1" {
		t.Errorf("o handle selado pela porta: tool=%q media_type=%q name=%q", d.Tool, d.MediaType, d.Name)
	}
	if d.Digest != foto.Digest {
		t.Errorf("digest selado = %q, quero o que a porta conferiu (%q)", d.Digest, foto.Digest)
	}
	if !d.WellFormed {
		t.Error("a forma foi conferida na porta: o evento tem de a declarar")
	}
}
