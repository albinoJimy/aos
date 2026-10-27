package main

// aos563_doc_no_espaco_de_handles_test.go — O `doc_id` RESOLVE-SE PRIMEIRO NO ESPAÇO DE HANDLES
// (AOS-563).
//
// O QUE ESTES TESTES AMARRAM, e porque cada um existe:
//
//  1. A ORDEM: uma entrada de media do run ganha ao ficheiro semeado com o MESMO nome, e a colisão
//     é contada. Sem este teste, semear `media_input-1` mudava em silêncio o que a tool lê — e é
//     essa a razão de a ordem existir, não uma preferência de desenho.
//  2. A NÃO-REGRESSÃO: um run SEM entradas de media lê da pasta exactamente o que lia. É a metade
//     que paga a decisão de não substituir a pasta semeada.
//  3. O TIPO vem dos FACTOS do handle (o conferido na porta), não de uma segunda inferência.
//  4. O GUARDA (b) MANTÉM-SE: sem um `handle.created` deste run não há bytes, e um handle de OUTRO
//     run não resolve neste.
//  5. AS DUAS METADES DA `media_view` RESOLVEM IGUAL: o executor e o resolvedor do turno seguinte.
//     É o defeito que mataria a tool — a tool devolvia uma referência que o turno seguinte não
//     conseguia honrar, e o run falhava com `media_ilegivel`.
//  6. O ANÚNCIO do `GET /tools` acende e apaga pela MESMA condição que a porta confere.
//
// O QUE NÃO TEM TESTE AQUI, E PORQUÊ: que a `video_read` não ganhou este caminho. Ela está fora de
// âmbito (o tecto de um vídeo não é o de uma imagem), e o que a mantém fora é o plano de handles
// viver na `ligacaoOCR` e não na `ligacaoDeModelo`, que é a parte COMUM às duas tools — ou seja, é
// uma garantia do COMPILADOR, não um comportamento que um teste possa observar. Um teste que
// tentasse observá-lo pela tool teria de pedir uma entrada de media (uma IMAGEM) à `video_read`,
// que a recusa pelo formato de qualquer modo: passaria com o caminho ligado ou desligado, que é o
// controlo negativo que não controla nada.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/substrate/sandbox"
)

// aos563Semeado abre uma raiz confinada sobre uma pasta com os ficheiros dados.
func aos563Semeado(t *testing.T, ficheiros map[string][]byte) (raizDoOCR, string) {
	t.Helper()
	pasta := t.TempDir()
	for nome, b := range ficheiros {
		if err := os.WriteFile(filepath.Join(pasta, nome), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raiz, err := os.OpenRoot(pasta)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raiz.Close() })
	return raizOS{raiz}, pasta
}

// aos563SelarEntrada sela uma entrada de media num run, pelo PRODUTOR real do AOS-561, e devolve o
// Goal selado. É o produtor que dá o nome cunhado (`media_input-1..n`), e usá-lo em vez de escrever
// o evento à mão é o que faz este teste poder desmentir o leitor.
func aos563SelarEntrada(t *testing.T, plano *planoDeHandles, runID string, entradas []agentruntime.MediaInput) agentruntime.Goal {
	t.Helper()
	goal := aos561Goal(entradas)
	goal.RunID = runID
	selado, err := selarMediaDoPedido(context.Background(), plano, goal)
	if err != nil {
		t.Fatalf("selar: %v", err)
	}
	return selado
}

// aos563Entrada é uma entrada de media válida com os bytes dados.
func aos563Entrada(tipo string, dados []byte) agentruntime.MediaInput {
	return agentruntime.MediaInput{MediaType: tipo, Digest: mediaInputDigestAlgo + digestHex(dados), Content: dados}
}

// A ORDEM, E A COLISÃO.
//
// O MESMO `doc_id` nos dois espaços com conteúdos DIFERENTES: a tool tem de ler o do RUN, e o
// contador tem de se mover. É o teste que o critério pede nominalmente («um ficheiro semeado com o
// nome cunhado não muda o que a tool lê»), e é também o único que distingue a ordem certa da
// inversa — com a ordem trocada, tudo o resto deste ficheiro continuava verde.
func TestAOS563_AEntradaDoRunGanhaAoFicheiroSemeadoComOMesmoNome(t *testing.T) {
	doRun := aos561PNG(64)
	daPasta := aos561PNG(128)
	plano, espaco, _ := aos561PlanoComVolume(t, 1<<30)
	selado := aos563SelarEntrada(t, plano, "run-563-colisao", []agentruntime.MediaInput{aos563Entrada(port.ImageMediaPNG, doRun)})
	nome := mediaInputHandleTool + "-1"
	if selado.MediaInputs[0].Handle == "" {
		t.Fatal("a entrada nao foi selada")
	}
	// A PASTA SEMEADA NOMEIA O MESMO `doc`, com OUTROS bytes.
	raiz, _ := aos563Semeado(t, map[string][]byte{nome: daPasta})
	antes := espaco.lido(hCausaDoisEspacos)

	dados, tipo, causa := resolverDocDeImagem(context.Background(), plano, raiz, selado.RunID, nome, port.MaxImageBytes)
	if causa != "" {
		t.Fatalf("causa = %q", causa)
	}
	if string(dados) != string(doRun) {
		t.Fatalf("leu %d bytes; queria os %d do RUN (a pasta tem %d)", len(dados), len(doRun), len(daPasta))
	}
	if tipo != port.ImageMediaPNG {
		t.Fatalf("tipo = %q", tipo)
	}
	if got := espaco.lido(hCausaDoisEspacos) - antes; got != 1 {
		t.Fatalf("colisoes contadas = %d, quer 1", got)
	}
}

// A NÃO-REGRESSÃO: SEM ENTRADAS DE MEDIA, A PASTA LÊ-SE COMO LIA.
//
// Com o plano composto e um run que não tem entrada nenhuma, e também com plano nil (um nó sem
// espaço de handles). Os dois casos no mesmo teste porque é a mesma afirmação: nada do AOS-563 se
// mete no caminho de quem não o usa, e `tipo` vazio é o que diz ao chamador para inferir dos bytes
// como sempre fez.
func TestAOS563_SemEntradasDeMediaAPastaSemeadaLeOQueLia(t *testing.T) {
	bytesDaPasta := aos561PNG(32)
	raiz, _ := aos563Semeado(t, map[string][]byte{"sala.png": bytesDaPasta})
	plano, _, _ := aos561PlanoComVolume(t, 1<<30)

	for nome, p := range map[string]*planoDeHandles{"com espaco de handles": plano, "sem espaco de handles": nil} {
		dados, tipo, causa := resolverDocDeImagem(context.Background(), p, raiz, "run-563-vazio", "sala.png", port.MaxImageBytes)
		if causa != "" || string(dados) != string(bytesDaPasta) {
			t.Fatalf("%s: causa=%q, %d bytes", nome, causa, len(dados))
		}
		// VAZIO É O CONTRATO: o chamador infere o tipo dos bytes, que é o caminho inalterado.
		if tipo != "" {
			t.Fatalf("%s: tipo = %q, quer vazio (a pasta nao declara tipo)", nome, tipo)
		}
	}
	// E as regras do ADR-042 §2.4 continuam a valer na pasta, pelo mesmo caminho.
	if _, _, causa := resolverDocDeImagem(context.Background(), plano, raiz, "run-563-vazio", "../sala.png", port.MaxImageBytes); causa != ocrCausaForaDaRaiz {
		t.Fatalf("travessia = %q, quer %q", causa, ocrCausaForaDaRaiz)
	}
	if _, _, causa := resolverDocDeImagem(context.Background(), plano, raiz, "run-563-vazio", "nada.png", port.MaxImageBytes); causa != ocrCausaEmFalta {
		t.Fatalf("ausente = %q, quer %q", causa, ocrCausaEmFalta)
	}
}

// O TIPO VEM DOS FACTOS, E NÃO DE UMA SEGUNDA INFERÊNCIA.
//
// Um JPEG selado como JPEG resolve como JPEG, com a pasta semeada a não ter nada. A asserção que
// importa é a do TIPO: uma segunda inferência dos bytes daria o mesmo resultado para estes bytes,
// pelo que o teste usa o formato que a `ocr_read` derivaria — é por ele que se vê de onde o tipo
// veio.
func TestAOS563_OTipoEODosFactosDoHandle(t *testing.T) {
	jpeg := aos561JPEG(48)
	plano, _, _ := aos561PlanoComVolume(t, 1<<30)
	selado := aos563SelarEntrada(t, plano, "run-563-tipo", []agentruntime.MediaInput{aos563Entrada(port.ImageMediaJPEG, jpeg)})
	raiz, _ := aos563Semeado(t, nil)

	h, achou, _ := plano.entradaDeMediaDoRun(context.Background(), selado.RunID, mediaInputHandleTool+"-1")
	if !achou || h.MediaType != port.ImageMediaJPEG || h.Tool != mediaInputHandleTool {
		t.Fatalf("factos = %+v (achou=%v)", h, achou)
	}
	_, tipo, causa := resolverDocDeImagem(context.Background(), plano, raiz, selado.RunID, mediaInputHandleTool+"-1", port.MaxImageBytes)
	if causa != "" || tipo != port.ImageMediaJPEG {
		t.Fatalf("tipo = %q, causa = %q", tipo, causa)
	}
	if f := formatoDoTipoDeImagem(tipo); f != "jpeg" {
		t.Fatalf("formato = %q", f)
	}
	// O vocabulário do formato é FECHADO: um tipo que não seja imagem não tem formato.
	if f := formatoDoTipoDeImagem("text/plain"); f != "" {
		t.Fatalf("formato de um tipo nao-imagem = %q", f)
	}
}

// O GUARDA (b) MANTÉM-SE, E O PREDICADO É O MÍNIMO.
//
// Três negativas sobre o MESMO espaço cheio, que é o que as torna controlos e não tautologias — a
// entrada EXISTE, e cada caso falha por uma razão diferente:
//   - o nome que o nó NÃO cunhou;
//   - o mesmo nome, mas pedido para OUTRO run (o guarda (b): o stream do outro run não a declara);
//   - um handle cujo `tool` não é o da via (um `tool_result`), que o predicado não adopta.
func TestAOS563_OGuardaDoEventoEOPredicadoDaEntrada(t *testing.T) {
	plano, _, _ := aos561PlanoComVolume(t, 1<<30)
	selado := aos563SelarEntrada(t, plano, "run-563-guarda", []agentruntime.MediaInput{aos563Entrada(port.ImageMediaPNG, aos561PNG(16))})
	nome := mediaInputHandleTool + "-1"
	// O controlo POSITIVO primeiro: sem ele, as negativas abaixo passariam com o espaço vazio.
	if _, achou, _ := plano.entradaDeMediaDoRun(context.Background(), selado.RunID, nome); !achou {
		t.Fatal("a entrada selada nao se encontra — as negativas abaixo nao controlam nada")
	}
	if _, achou, _ := plano.entradaDeMediaDoRun(context.Background(), selado.RunID, "sala.png"); achou {
		t.Fatal("um nome fora da forma de um nome cunhado resolveu")
	}
	// O CONJUNTO `Name == doc`, isolado: um nome COM a forma certa e com OUTRO ordinal. Sem este
	// caso o pré-filtro pelo prefixo deixava a igualdade do nome sem cobertura — achado da segunda
	// passagem adversarial, que notou que `sala.png` passou a sair no pré-filtro, antes do
	// predicado.
	if _, achou, _ := plano.entradaDeMediaDoRun(context.Background(), selado.RunID, mediaInputHandleTool+"-2"); achou {
		t.Fatalf("o ordinal %s-2 resolveu num run que so tem o -1", mediaInputHandleTool)
	}
	if _, achou, _ := plano.entradaDeMediaDoRun(context.Background(), "run-563-outro", nome); achou {
		t.Fatal("a entrada de um run resolveu NOUTRO run")
	}
	// UM `tool_result` COM O MESMO NOME NÃO É UMA ENTRADA DE MEDIA. É a colisão que o predicado
	// fecha pelo TIPO e não pelo rótulo: as tools vêm de um ficheiro do operador, e uma chamada
	// `media_input` selaria `media_input-N` neste mesmo espaço.
	outro := aos561Goal(nil)
	outro.RunID = "run-563-rotulo"
	ref, causa := plano.selarEmQuarentena(context.Background(), outro.Titular(), outro.RunID, "s1", mediaInputHandleTool, "text/plain", []byte("nao sou uma imagem"))
	if causa != "" || ref == "" {
		t.Fatalf("selar o tool_result: %q", causa)
	}
	if h, achou, _ := plano.entradaDeMediaDoRun(context.Background(), outro.RunID, nome); achou {
		t.Fatalf("um tool_result de tipo %q passou por entrada de media", h.MediaType)
	}
}

// AS DUAS METADES DA `media_view` RESOLVEM IGUAL.
//
// É O TESTE QUE APANHA O DEFEITO QUE MATARIA A TOOL. A `media_view` entrega os bytes em DOIS
// momentos: devolve a REFERÊNCIA num turno, e no turno seguinte o adaptador do gateway relê os
// bytes pelo resolvedor para os anexar ao pedido. Se só o executor soubesse do espaço de handles, a
// releitura procurava na pasta semeada um documento que lá não está — o digest não batia com a
// referência e o turno falhava. Aqui o resolvedor é chamado com o MESMO run, como o adaptador o
// chama, e tem de devolver bytes que casam com a referência.
func TestAOS563_MediaView_OExecutorEOResolvedorLeemAMesmaEntrada(t *testing.T) {
	imagem := aos561PNG(40)
	plano, _, _ := aos561PlanoComVolume(t, 1<<30)
	selado := aos563SelarEntrada(t, plano, "run-563-media-view", []agentruntime.MediaInput{aos563Entrada(port.ImageMediaPNG, imagem)})
	nome := mediaInputHandleTool + "-1"

	// A ligação com a pasta semeada REAL (que não tem este nome) e o plano ligado, como o nó a compõe.
	l, _, err := aos546Ligacao(t, aos546SpecBoa)
	if err != nil {
		t.Fatal(err)
	}
	l.handles = plano

	raw, err := json.Marshal(entradaOCR{Schema: mediaViewInputSchema, RunID: selado.RunID, StepID: "s1", DocID: nome})
	if err != nil {
		t.Fatal(err)
	}
	out, err := l.executar(context.Background(), raw)
	if err != nil {
		t.Fatalf("executar: %v", err)
	}
	var ref agentruntime.MediaRef
	if err := json.Unmarshal(out, &ref); err != nil {
		t.Fatal(err)
	}
	quer := port.MediaPart{MediaType: port.ImageMediaPNG, Data: imagem}
	if ref.Doc != nome || ref.SHA256 != quer.Digest() || ref.MediaType != port.ImageMediaPNG || ref.Bytes != int64(len(imagem)) {
		t.Fatalf("referencia = %+v", ref)
	}
	// A REFERÊNCIA NÃO LEVA OS BYTES.
	if strings.Contains(string(out), string(imagem[:8])) {
		t.Fatal("a referencia leva os bytes da entrada")
	}
	// O TURNO SEGUINTE: o resolvedor com o MESMO run devolve bytes que casam com a referência —
	// que é exactamente o que o adaptador confere ([modelgateway.anexarMedia]).
	parte, err := l.resolver(context.Background(), selado.RunID, ref)
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	if parte.Digest() != ref.SHA256 || parte.MediaType != ref.MediaType || int64(len(parte.Data)) != ref.Bytes {
		t.Fatalf("o resolvedor nao honra a referencia: %s/%d vs %s/%d", parte.Digest(), len(parte.Data), ref.SHA256, ref.Bytes)
	}
	// E SEM O RUN não resolve: é o que acontece num turno sem run no ctx, e cai na pasta semeada,
	// que não tem este documento. Fail-closed, e o adaptador recusa o turno.
	if _, err := l.resolver(context.Background(), "", ref); err == nil {
		t.Fatal("resolveu a entrada de um run sem saber o run")
	}
}

// O ANÚNCIO ACENDE E APAGA PELA MESMA CONDIÇÃO QUE A PORTA CONFERE.
//
// A porta recusa `media_inputs` com 400 quando `h.node == nil || !h.node.entradaDeMedia`; o anúncio
// é a negação exacta disso. Um teste sobre as três formas do nó (nil, via desligada, via ligada)
// é o que impede a porta e o anúncio de divergirem — anunciar o que se recusa, ou recusar o que se
// anuncia, são os dois defeitos que o resíduo do AOS-561 nomeou.
func TestAOS563_OAnuncioDaViaSegueACondicaoDaPorta(t *testing.T) {
	if a := anuncioDaEntradaDeMediaDoNo(nil); a != nil {
		t.Fatal("no nil anuncia a via")
	}
	if a := anuncioDaEntradaDeMediaDoNo(&Node{entradaDeMedia: false}); a != nil {
		t.Fatal("no com a via desligada anuncia-a")
	}
	a := anuncioDaEntradaDeMediaDoNo(&Node{entradaDeMedia: true})
	if a == nil {
		t.Fatal("no com a via ligada nao a anuncia")
	}
	// OS NÚMEROS SÃO OS DA PORTA, não uma segunda cópia: se um deles mudar e o anúncio não, o
	// submissor prepara um pedido que vai ser recusado.
	if a.MaxBytes != maxMediaInputBytes || a.MaxBytesTotal != maxMediaInputsBytes || a.MaxInputs != maxMediaInputs {
		t.Fatalf("tectos anunciados = %+v", a)
	}
	// O VOCABULÁRIO é o que [tipoDeMediaAceite] confere — todos aceites, e nada a mais.
	if len(a.MediaTypes) == 0 {
		t.Fatal("anuncio sem vocabulario")
	}
	for _, tipo := range a.MediaTypes {
		if !tipoDeMediaAceite(tipo) {
			t.Fatalf("anuncia %q, que a porta recusa", tipo)
		}
	}
	// A ORDEM DE RESOLUÇÃO vai no anúncio: é o que diz que o nome cunhado é utilizável num `doc_id`.
	if len(a.DocIDResolution) != 2 || a.DocIDResolution[0] != "run_media_input" || a.DocIDResolution[1] != "seed_dir" {
		t.Fatalf("ordem anunciada = %v", a.DocIDResolution)
	}
}

// O NÓ COMPOSTO, PONTA A PONTA: a entrada submetida no `POST /runs` é lida pela `media_view` e
// RESOLVIDA DE NOVO pelo adaptador no turno seguinte.
//
// É O TESTE QUE PROVA A CABLAGEM, e nenhum dos acima o faz: todos compõem a `ligacaoMedia` à mão e
// metem-lhe o plano de handles. Aqui o plano é o que o `Bootstrap` lhe liga — apagar essa linha
// deixava os outros verdes.
//
// E O VEREDICTO DISTINGUE, que é o que o torna prova e não cerimónia. O molde é o do
// [TestAOS546_No_MediaViewPeloRMEAGuardaDaRota]: o perfil da rota do titular não declara `imagem`,
// pelo que o gateway recusa o pedido do SEGUNDO turno com [modelgateway.ErrRouteWithoutImage] —
// mas recusa-o DEPOIS de o resolvedor ter devolvido os bytes e de eles baterem com a referência.
// Se o `doc_id` não resolvesse no espaço de handles, a falha seria OUTRA e mais cedo:
// `media_ilegivel` ([modelgateway.MediaCauseUnreadable]), porque a pasta semeada deste teste NÃO
// tem o nome cunhado. As duas causas são o controlo uma da outra.
//
// A PASTA SEMEADA TEM OUTRO FICHEIRO de propósito (`sala.png`): uma pasta vazia não distinguiria
// «resolveu no espaço do run» de «não havia mais nada para resolver».
func TestAOS563_No_AEntradaDoPedidoEhLidaPelaMediaView(t *testing.T) {
	ordem, extra, opcoes := aos486OrdemDoFicheiro, aos486CamposExtraDaSpec, aos486OpcoesExtra
	t.Cleanup(func() { aos486OrdemDoFicheiro, aos486CamposExtraDaSpec, aos486OpcoesExtra = ordem, extra, opcoes })

	pasta := t.TempDir()
	if err := os.WriteFile(filepath.Join(pasta, "sala.png"), aos546PNG(), 0o600); err != nil {
		t.Fatal(err)
	}
	raiz, err := os.OpenRoot(pasta)
	if err != nil {
		t.Fatal(err)
	}
	lig := &ligacaoMedia{root: raiz, raiz: raizOS{raiz}, pasta: pasta}
	t.Cleanup(lig.fechar)
	aos486OrdemDoFicheiro = []string{mediaViewToolName}
	aos486CamposExtraDaSpec = `,"egress":"none","reversibility":"reversible","mutation":"none"`
	aos486OpcoesExtra = lig.opcaoDoAdaptador()

	n := aos486Compor(t, func(cfg *Config) {
		cfg.PlanInputDepoisDoPrimeiroTurno = true
		cfg.MediaNoTurno = lig
		cfg.EntradaDeMedia = true
		cfg.HandleDir, cfg.HandleMaxBytes = t.TempDir(), 1<<30
	})
	// A CABLAGEM: é o `Bootstrap` que liga o plano à tool, e não este teste.
	if n.node.planoHandles == nil || lig.handles != n.node.planoHandles {
		t.Fatalf("o Bootstrap nao ligou o plano de handles a media_view: plano=%v lig=%v",
			n.node.planoHandles != nil, lig.handles != nil)
	}
	if !n.node.entradaDeMedia {
		t.Fatal("a via de entrada tinha de ficar habilitada")
	}

	// O MODELO PEDE O NOME CUNHADO, que é o único caminho: o nome não vem na resposta do POST.
	const doc = mediaInputHandleTool + "-1"
	n.upstream.mu.Lock()
	n.upstream.pede = mediaViewToolName
	n.upstream.responde = func(pedeTool bool, tool string) []byte {
		if !pedeTool {
			return []byte(`{"id":"cmpl-2","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"feito"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)
		}
		return []byte(`{"id":"cmpl-1","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call-1","type":"function","function":{"name":"` + tool + `","arguments":"{\"doc_id\":\"` + doc + `\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)
	}
	n.upstream.mu.Unlock()

	foto := aos561Wire(port.ImageMediaPNG, aos561PNG(96))
	const runID = "run-563-no"
	rec := postJSON(n.h, "POST", "/runs", map[string]any{
		"run_id": runID, "objective": "Ve a imagem " + doc + " e descreve-a",
		"principal_nhi": durAgent, "credential": n.tok,
		"tools": []string{mediaViewToolName}, "max_turns": 3,
		"media_inputs": []map[string]string{{
			"media_type": foto.MediaType, "digest": foto.Digest, "content": foto.Content,
		}},
	})
	if rec.Code != 201 {
		t.Fatalf("POST /runs com media_inputs: %d %s", rec.Code, rec.Body.String())
	}

	wctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	oc, ok, werr := n.svc.Wait(wctx, runID)
	if werr != nil || !ok {
		t.Fatalf("Wait: ok=%v err=%v", ok, werr)
	}
	// O VEREDICTO. `ErrRouteWithoutImage` só se alcança com os bytes JÁ resolvidos e a bater com a
	// referência; `media_ilegivel` seria o sintoma de a entrada não ter resolvido no espaço.
	if oc.Err == nil || !errors.Is(oc.Err, modelgateway.ErrRouteWithoutImage) {
		if oc.Err != nil && errors.Is(oc.Err, modelgateway.ErrMediaUnavailable) {
			t.Fatalf("a entrada de media do run NAO resolveu — a tool caiu na pasta semeada: %v", oc.Err)
		}
		t.Fatalf("esperava a guarda da rota (sem imagem), veio %v", oc.Err)
	}
	// AS DUAS METADES CORRERAM: a tool leu uma vez, e o adaptador resolveu ao menos uma.
	if lig.lidas.Load() != 1 || lig.resolvidas.Load() < 1 {
		t.Fatalf("a media_view leu %d e o adaptador resolveu %d", lig.lidas.Load(), lig.resolvidas.Load())
	}
	// E OS BYTES ERAM OS DA ENTRADA, não os do ficheiro semeado: o digest do `handle.created` do
	// run é o da foto submetida, e é com ele que a referência da tool bate.
	declarados, derr := n.node.planoHandles.armazem.declarados(context.Background(), runID)
	if derr != nil {
		t.Fatalf("declarados: %v", derr)
	}
	var achado *handleCriado
	for i := range declarados {
		if declarados[i].Name == doc && declarados[i].Tool == mediaInputHandleTool {
			achado = &declarados[i]
		}
	}
	if achado == nil {
		t.Fatalf("o run nao declara a entrada %q: %+v", doc, declarados)
	}
	if achado.Digest != foto.Digest || achado.MediaType != port.ImageMediaPNG {
		t.Fatalf("os factos da entrada nao sao os da foto submetida: %+v", *achado)
	}
	// CONTROLO: o ficheiro semeado continua lá e tem OUTRO digest — a tool leu o do run.
	if string(aos546PNG()) == string(aos561PNG(96)) {
		t.Fatal("meta-teste: as duas imagens sao iguais, o caso nao distingue nada")
	}
}

// O DESPACHO COMPOSTO LEVA **TODAS** AS TOOLS DO NÓ À REESCRITA DELAS, COM BINDING DE SANDBOX.
//
// É O TESTE DO DEFEITO QUE A REVISÃO ADVERSARIAL ENCONTROU, e ele era crítico: a `media_view`
// passou a EXIGIR o input reescrito, mas o despacho composto escolhia a reescrita por uma LISTA
// LITERAL de `ToolID` em que ela não estava. Com um binding de sandbox — a forma de PRODUÇÃO, onde
// o catálogo tem a `doc_read` com bloco `sandbox` — as duas reescritas ficam não-nil, a call da
// `media_view` caía na da sandbox, que devolve inalterada uma tool sem binding, e o executor
// recebia os args CRUS e recusava-os: `media_view: entrada_invalida` em TODAS as chamadas, incluindo
// as da pasta semeada. Regressão total de uma funcionalidade em produção (AOS-546).
//
// SEM BINDING DE SANDBOX O DEFEITO NÃO APARECE — e é por isso que nenhum teste o apanhava: sem
// bindings a reescrita da sandbox é nil, e o `case` devolvia a de modelo. O
// [TestAOS548_ReescritaComposta] nasceu do mesmo defeito na `artifact_write`, mas NÃO é guarda
// deste: ele passa os três rewriters de modelo a `nil`, pelo que `comporReescritas` sai no
// `case modeloRw == nil` e o despacho nunca é exercitado. É a afirmação ao contrário que a segunda
// passagem adversarial apanhou aqui.
//
// O QUE SUBSTITUIU A LISTA É UM DESPACHO POR PREDICADO, e não uma cadeia: uma call vai a UMA
// reescrita, nunca às duas (ver [comporReescritas]). «Cadeia» era a primeira tentativa de
// correcção, que partiu dois testes existentes e foi abandonada — e o texto dela ficou aqui.
//
// A TABELA COBRE AS TRÊS tools do nó cujo input reescrito é [entradaOCR]. A `artifact_write` tem o
// seu próprio (`entradaDoArtefacto`) e é reescrita por um elo PRÓPRIO da cadeia, pelo que leva uma
// asserção à parte, no fim — o defeito é da COMPOSIÇÃO e não de uma tool, e deixar uma de fora era
// deixá-la por cobrir à próxima.
func TestAOS563_ODespachoCompostoNaoPerdeNenhumaToolDoNo(t *testing.T) {
	t.Parallel()
	// Como em produção: a `doc_read` tem bloco `sandbox`, as tools do nó não.
	bindings := map[string]sandbox.SandboxBinding{"doc_read": {Command: "read", PathArg: "doc_id"}}
	ocrRw := newOCREffectRewriter()
	videoRw := newEfeitoDeModeloRewriter(videoToolName, videoInputSchema)
	mediaRw := newEfeitoDeModeloRewriter(mediaViewToolName, mediaViewInputSchema)
	cadeia := reescritaDoEfeito(bindings, ocrRw, videoRw, mediaRw, true, nil)

	for tool, schema := range map[string]string{
		ocrToolName:       ocrInputSchema,
		videoToolName:     videoInputSchema,
		mediaViewToolName: mediaViewInputSchema,
	} {
		got, err := cadeia(referencemonitor.Call{
			ToolID: tool, RunID: "run-563-rot", StepID: "s1",
			Principal: referencemonitor.Principal{NHIID: "nhi:a", Subject: "tit"},
			Input:     []byte(`{"doc_id":"sala.png"}`),
		})
		if err != nil {
			t.Fatalf("%s: a cadeia recusou: %v", tool, err)
		}
		var in entradaOCR
		if jerr := json.Unmarshal(got.Input, &in); jerr != nil {
			t.Fatalf("%s: input nao e a entrada reescrita: %s", tool, got.Input)
		}
		if in.Schema != schema || in.RunID != "run-563-rot" || in.StepID != "s1" || in.DocID != "sala.png" {
			t.Fatalf("%s: a call NAO passou pela reescrita dela — chegaria ao executor com os args crus: %+v", tool, in)
		}
	}

	// A `artifact_write` passa pelo MESMO despacho (não tem binding ⇒ vai à de modelo, que a deixa
	// inalterada) e é reescrita pelo elo PRÓPRIO que corre a seguir. O input dela é
	// [entradaDoArtefacto] e não [entradaOCR], e é por isso que não cabe na tabela acima.
	gotA, errA := cadeia(referencemonitor.Call{
		ToolID: artifactWriteToolName, RunID: "run-563-rot", StepID: "s1",
		Principal: referencemonitor.Principal{NHIID: "nhi:a", Subject: "tit"},
		Input:     []byte(`{"name":"a.json","media_type":"application/json","content":"{}"}`),
	})
	if errA != nil {
		t.Fatalf("artifact_write: a cadeia recusou: %v", errA)
	}
	var inA entradaDoArtefacto
	if jerr := json.Unmarshal(gotA.Input, &inA); jerr != nil || inA.Schema != artifactInputSchema || inA.RunID != "run-563-rot" || inA.Subject != "tit" {
		t.Fatalf("artifact_write: nao passou pela reescrita dela: %s", gotA.Input)
	}

	// E A TOOL COM BINDING continua a ir à da sandbox: o despacho não trocou os papéis.
	got, err := cadeia(referencemonitor.Call{
		ToolID: "doc_read", RunID: "run-563-rot", StepID: "s1",
		Principal: referencemonitor.Principal{NHIID: "nhi:a", Subject: "tit"},
		Input:     []byte(`{"doc_id":"notas.txt"}`),
	})
	if err != nil {
		t.Fatalf("doc_read: %v", err)
	}
	if !strings.Contains(string(got.Input), `"Command":"read"`) {
		t.Fatalf("a doc_read nao foi traduzida para ExecRequest pela reescrita da sandbox: %s", got.Input)
	}
}

// UM STREAM QUE NÃO SE LÊ NÃO SERVE O FICHEIRO DO OPERADOR.
//
// Achado da revisão adversarial, e era fail-OPEN: se a leitura das declarações do run falhasse (o
// Event Store indisponível, o ctx do turno a expirar, um erro de rede — o `declaracoesDeHandles` só
// converte `ErrStreamNotFound` em «sem handles»), a resolução caía para a pasta semeada. Com o
// operador a ter semeado um ficheiro com o nome cunhado, a tool lia o ficheiro DELE em vez do
// conteúdo que o run trouxe, em silêncio e sem rasto. Na `ocr_read` não há segundo meio que o
// apanhe: uma chamada, um resultado, conteúdo trocado.
//
// O ALCANCE DA RECUSA É ESTREITO, e é isso que a torna correcta em vez de uma regressão: só um
// `doc_id` com a FORMA de um nome cunhado chega a ler o stream (o pré-filtro de
// [planoDeHandles.entradaDeMediaDoRun]). Um `doc_id` da pasta semeada não depende do Event Store —
// e é o que a segunda metade deste teste assere, porque sem ela a correcção teria posto a leitura
// da pasta a depender do substrato, que é o oposto do que o ticket promete.
func TestAOS563_ErroAoLerOStreamNaoCaiParaAPastaSemeada(t *testing.T) {
	plano, espaco, _ := aos561PlanoComVolume(t, 1<<30)
	nome := mediaInputHandleTool + "-1"
	// A pasta semeada tem o nome cunhado — é o caso perigoso.
	raiz, _ := aos563Semeado(t, map[string][]byte{nome: aos561PNG(128), "sala.png": aos561PNG(32)})

	// O stream deixa de se ler. É um campo de função: injectar o erro é o que o torna observável.
	bom := plano.armazem.declarados
	plano.armazem.declarados = func(context.Context, string) ([]handleCriado, error) {
		return nil, errors.New("event store indisponivel")
	}
	t.Cleanup(func() { plano.armazem.declarados = bom })

	antes := espaco.lido(handleCausaIlegivel)
	dados, tipo, causa := resolverDocDeImagem(context.Background(), plano, raiz, "run-563-erro", nome, port.MaxImageBytes)
	if causa == "" {
		t.Fatalf("resolveu com o stream ilegivel: %d bytes, tipo %q — servia o ficheiro do OPERADOR", len(dados), tipo)
	}
	if causa != handleCausaIlegivel {
		t.Fatalf("causa = %q, quer %q", causa, handleCausaIlegivel)
	}
	if got := espaco.lido(handleCausaIlegivel) - antes; got != 1 {
		t.Fatalf("a recusa nao foi contada: %d", got)
	}

	// E O ALCANCE: um `doc_id` que NÃO tem a forma de um nome cunhado continua a ler da pasta com o
	// stream em baixo — a leitura da pasta semeada não passou a depender do Event Store.
	dados, tipo, causa = resolverDocDeImagem(context.Background(), plano, raiz, "run-563-erro", "sala.png", port.MaxImageBytes)
	if causa != "" || tipo != "" || len(dados) == 0 {
		t.Fatalf("a pasta semeada passou a depender do Event Store: causa=%q tipo=%q %d bytes", causa, tipo, len(dados))
	}
}

// O ANÚNCIO E A PORTA, PELA SUPERFÍCIE HTTP E NÃO PELA CONSTANTE.
//
// Achado da revisão adversarial: o teste do anúncio acima é uma função pura sobre um `Node`
// literal, pelo que a afirmação «a condição é a MESMA que a porta confere» não era verificada por
// teste nenhum — a porta (`api.go`) e o anúncio (`catalogo_de_tools.go`) continuavam duas escritas
// da mesma condição em ficheiros diferentes, mantidas juntas pela revisão e não pelo código.
//
// ESTE TESTE AMARRA-AS PELO COMPORTAMENTO: nas DUAS formas do nó, pergunta ao `GET /tools` se a via
// se anuncia e ao `POST /runs` se ela é aceite, e exige que as respostas CONCORDEM. Anunciar o que
// se recusa, ou recusar o que se anuncia, avermelha — e é indiferente a como cada lado está escrito.
func TestAOS563_OAnuncioEAPortaConcordamPelaSuperficieHTTP(t *testing.T) {
	foto := aos561Wire(port.ImageMediaPNG, aos561PNG(32))

	for nome, comEspaco := range map[string]bool{"via habilitada": true, "via pedida sem espaco de handles": false} {
		n := aos486ComporCom(t, "native", func(cfg *Config) {
			cfg.EntradaDeMedia = true // pedida pelo operador nas duas formas…
			if comEspaco {
				// …e só esta tem onde selar, que é o que a COMPÕE.
				cfg.HandleDir, cfg.HandleMaxBytes = t.TempDir(), 1<<30
			}
			cfg.Privileged = referencemonitor.NewStaticPrivilegedSet("cap:http.post", durCap)
		})

		// O QUE O ANÚNCIO DIZ, lido da rota.
		recT := postJSON(n.h, "GET", "/tools", nil)
		if recT.Code != 200 {
			t.Fatalf("%s: GET /tools: %d %s", nome, recT.Code, recT.Body.String())
		}
		var cat respostaDoCatalogo
		if err := json.Unmarshal(recT.Body.Bytes(), &cat); err != nil {
			t.Fatalf("%s: corpo do catalogo: %v", nome, err)
		}
		anuncia := cat.MediaInput != nil

		// O QUE A PORTA FAZ, com o MESMO pedido nas duas formas.
		recR := postJSON(n.h, "POST", "/runs", map[string]any{
			"run_id": "run-563-concordam-" + strconv.FormatBool(comEspaco), "objective": aos540Objectivo,
			"principal_nhi": durAgent, "credential": n.tok, "tools": []string{"arquivo"}, "max_turns": 4,
			"media_inputs": []map[string]string{{
				"media_type": foto.MediaType, "digest": foto.Digest, "content": foto.Content,
			}},
		})
		aceita := recR.Code == 201

		if anuncia != aceita {
			t.Fatalf("%s: o anuncio e a porta DIVERGEM — GET /tools anuncia=%v, POST /runs aceita=%v (%d %s)",
				nome, anuncia, aceita, recR.Code, recR.Body.String())
		}
		// E NÃO-VACUIDADE: as duas formas têm de dar respostas DIFERENTES, senão a concordância
		// acima passaria com um nó que anunciasse e aceitasse sempre (ou nunca).
		if anuncia != comEspaco {
			t.Fatalf("%s: esperava anuncia=%v", nome, comEspaco)
		}
		if !aceita && recR.Code != 400 {
			t.Fatalf("%s: a recusa tinha de ser 400 do campo: %d %s", nome, recR.Code, recR.Body.String())
		}
	}
}

// A SEGUNDA ORIGEM EXISTE PELA VIA DE ENTRADA, NÃO PELO ESPAÇO DE HANDLES.
//
// Achado da segunda passagem adversarial. O `Bootstrap` ligava o plano de handles às duas tools só
// por `AOS_HANDLE_DIR` estar definida — e um nó pode tê-la só pelo layout 1.8.0 (AOS-550), sem
// nunca habilitar a via. Consequências, as duas erradas: o caminho novo ficava VIVO numa postura
// que o operador não pediu (e um erro do Event Store passava a poder RECUSAR uma `ocr_read` que
// antes lia da pasta), e as frases que este ticket escreveu no `deploy/node/README.md` e na emenda
// ao ADR-042 §2.4 — que atribuem a segunda origem ao `AOS_ENTRADA_DE_MEDIA` — eram falsas.
//
// O TESTE PRENDE AS DUAS FORMAS, e a segunda é a que importa: com o espaço de handles composto e a
// via DESLIGADA, a tool não recebe o plano e o banner dela declara UMA origem.
func TestAOS563_AViaDeEntradaEhQueLigaASegundaOrigem(t *testing.T) {
	for nome, comVia := range map[string]bool{"via ligada": true, "via desligada, espaco ligado": false} {
		ordem, extra, opcoes := aos486OrdemDoFicheiro, aos486CamposExtraDaSpec, aos486OpcoesExtra
		pasta := t.TempDir()
		raiz, err := os.OpenRoot(pasta)
		if err != nil {
			t.Fatal(err)
		}
		lig := &ligacaoMedia{root: raiz, raiz: raizOS{raiz}, pasta: pasta}
		aos486OrdemDoFicheiro = []string{mediaViewToolName}
		aos486CamposExtraDaSpec = `,"egress":"none","reversibility":"reversible","mutation":"none"`
		aos486OpcoesExtra = lig.opcaoDoAdaptador()
		n := aos486Compor(t, func(cfg *Config) {
			cfg.PlanInputDepoisDoPrimeiroTurno = true
			cfg.MediaNoTurno = lig
			cfg.EntradaDeMedia = comVia
			// O ESPAÇO DE HANDLES ESTÁ COMPOSTO NAS DUAS FORMAS: é o que torna este teste um
			// controlo e não uma tautologia — sem ele, a ausência do plano provaria só que o
			// espaço falta.
			cfg.HandleDir, cfg.HandleMaxBytes = t.TempDir(), 1<<30
		})
		aos486OrdemDoFicheiro, aos486CamposExtraDaSpec, aos486OpcoesExtra = ordem, extra, opcoes
		lig.fechar()

		if n.node.planoHandles == nil {
			t.Fatalf("%s: o espaco de handles tinha de ficar composto nas DUAS formas", nome)
		}
		if comVia {
			if lig.handles != n.node.planoHandles {
				t.Fatalf("%s: a via esta composta e a tool nao recebeu o plano", nome)
			}
		} else if lig.handles != nil {
			t.Fatalf("%s: a via NAO esta composta e a tool recebeu o plano — a segunda origem ficou viva numa postura que o operador nao pediu", nome)
		}
		// E O BANNER DECLARA O QUE FOI COMPOSTO, não o que a variável pediu.
		banner := strings.Join(mediaNoTurnoBanner(lig), " ")
		if comVia && !strings.Contains(banner, "DUAS, nesta ordem") {
			t.Fatalf("%s: o banner nao declara as duas origens: %s", nome, banner)
		}
		if !comVia && !strings.Contains(banner, "UMA so") {
			t.Fatalf("%s: o banner declara uma origem que o no NAO tem: %s", nome, banner)
		}
	}
}
