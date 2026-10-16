package main

// AOS-530 (peça 3a) — A TOOL `ocr_read`, peça a peça, sobre um gateway FALSO que conta os pedidos.
//
// O caminho composto (o RM, o gateway real, a identidade do run, o replay) está em
// aos530_ocr_read_no_test.go. Aqui provam-se as regras que são da tool e que não precisam do nó
// para serem vistas: a ordem «identidade antes de ler», os limites antes do custo, as falhas do
// modelo com causa fechada e sem nova tentativa, a forma do pedido e a do resultado, a reescrita do
// efeito e a leitura do bloco `modelo` no registo.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
)

// aos530MarcaBinaria é uma sequência de bytes que só existe nas imagens destes testes: anexada a
// um PNG válido, deixa-o PNG pelos primeiros bytes e permite procurá-la — e ao seu base64 — em tudo
// o que o nó escreve.
const aos530MarcaBinaria = "\x00\xffMARCA-BINARIA-AOS530-7f3c\x01\xfe"

// aos530PNG devolve um PNG pequeno (8x8), determinista, com a marca binária no fim.
func aos530PNG(t testing.TB) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := 0; x < 8; x++ {
		img.Set(x, x, color.RGBA{R: 255, A: 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return append(b.Bytes(), aos530MarcaBinaria...)
}

// aos530JPEG devolve um JPEG pequeno, determinista.
func aos530JPEG(t testing.TB) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 8, 8))
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 50}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// aos530GwFalso é o gateway falso: grava cada pedido e responde o que lhe mandarem.
type aos530GwFalso struct {
	mu      sync.Mutex
	pedidos []port.ChatRequest
	resp    port.ChatResponse
	err     error
	// esperar faz o pedido esperar pelo fim do contexto (o prazo da tool).
	esperar bool
}

func (g *aos530GwFalso) Chat(ctx context.Context, req port.ChatRequest) (port.ChatResponse, error) {
	g.mu.Lock()
	g.pedidos = append(g.pedidos, req)
	g.mu.Unlock()
	if g.esperar {
		<-ctx.Done()
		return port.ChatResponse{}, fmt.Errorf("provider: %w", ctx.Err())
	}
	return g.resp, g.err
}

func (g *aos530GwFalso) contagem() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.pedidos)
}

// aos530Resposta é a resposta do modelo de visão falso.
func aos530Resposta(texto, fim string, custo int64) port.ChatResponse {
	return port.ChatResponse{
		Model:   "openrouter/anthropic/modelo-de-visao",
		Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: texto}, FinishReason: fim}},
		Usage:   port.Usage{PromptTokens: 900, CompletionTokens: 40, TotalTokens: 940, CostMicroUSD: custo},
		Route:   port.ServedRoute{Model: "openrouter/anthropic/modelo-de-visao"},
	}
}

// aos530LigacaoFalsa monta a ligação sobre a pasta e o gateway dados.
func aos530LigacaoFalsa(t *testing.T, pasta string, gw gatewayDoOCR) *ligacaoOCR {
	t.Helper()
	raiz, err := os.OpenRoot(pasta)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raiz.Close() })
	l := novaLigacaoOCR("ocr-visao", "eu", "board-eu", "sha256:perfil-do-registo", pasta, raiz)
	l.gw = gw
	return l
}

// aos530Entrada é o input da tool depois da reescrita.
func aos530Entrada(doc string) []byte {
	raw, _ := json.Marshal(entradaOCR{Schema: ocrInputSchema, RunID: "run-530", StepID: "passo-7", DocID: doc})
	return raw
}

// aos530ComCredencial é o contexto do despacho com o token do run.
func aos530ComCredencial() context.Context {
	return withModelCredential(context.Background(), "token-do-run")
}

// SEM CREDENCIAL A TOOL FALHA ANTES DE LER O FICHEIRO. O ficheiro pedido não existe: se a tool o
// tentasse ler, a causa seria `ficheiro_em_falta`. Vem `sem_credencial`, e o gateway não é chamado.
func TestAOS530_OCR_SemCredencialFalhaAntesDeLer(t *testing.T) {
	gw := &aos530GwFalso{resp: aos530Resposta("texto", port.FinishStop, 1)}
	l := aos530LigacaoFalsa(t, t.TempDir(), gw)
	out, custo, err := l.executar(context.Background(), aos530Entrada("nao-existe.png"))
	if causaDaFalhaOCR(err) != ocrCausaSemNHI || out != nil || custo != 0 || gw.contagem() != 0 {
		t.Fatalf("quer sem_credencial sem ler nem chamar; veio %v custo=%d pedidos=%d", err, custo, gw.contagem())
	}
	if l.lido(ocrCausaSemNHI) != 1 {
		t.Fatal("a metrica tinha de contar a causa")
	}
}

// LIMITES ANTES DO CUSTO. Cada recusa tem causa própria e o gateway conta zero pedidos.
func TestAOS530_OCR_LimitesAntesDoCusto(t *testing.T) {
	fora := t.TempDir()
	pasta := t.TempDir()
	escrever := func(nome string, dados []byte) {
		if err := os.WriteFile(filepath.Join(pasta, nome), dados, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(fora, "segredo.png"), aos530PNG(t), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(pasta, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	escrever(filepath.Join("sub", "dentro.png"), aos530PNG(t))
	grande := append(append([]byte(nil), aos530PNG(t)...), make([]byte, port.MaxImageBytes)...)
	escrever("grande.png", grande)
	escrever("doc.pdf", []byte("%PDF-1.4\n%marca\n"))
	escrever("texto.png", []byte("nao sou uma imagem"))
	escrever("vazio.png", nil)

	casos := []struct{ doc, causa string }{
		{"../" + filepath.Base(fora) + "/segredo.png", ocrCausaForaDaRaiz},
		{filepath.Join(fora, "segredo.png"), ocrCausaForaDaRaiz},
		{"/etc/passwd", ocrCausaForaDaRaiz},
		{"sub/dentro.png", ocrCausaForaDaRaiz},
		{`sub\dentro.png`, ocrCausaForaDaRaiz},
		{"..", ocrCausaForaDaRaiz},
		{"c:segredo.png", ocrCausaForaDaRaiz},
		{"nao-existe.png", ocrCausaEmFalta},
		{"sub", ocrCausaNaoRegular},
		{"grande.png", ocrCausaGrande},
		{"doc.pdf", ocrCausaFormato},
		{"texto.png", ocrCausaFormato},
		{"vazio.png", ocrCausaFormato},
	}
	// UMA LIGAÇÃO SIMBÓLICA NO TOPO não se segue (nem para dentro, nem para fora): não é um ficheiro
	// regular. Onde o sistema deixa criar uma (no Windows exige um privilégio que o CI pode não ter;
	// aí o caso é dito e não corre; o caso com a raiz falsa, abaixo, corre em todos).
	if err := os.Symlink(filepath.Join(fora, "segredo.png"), filepath.Join(pasta, "ligacao.png")); err == nil {
		casos = append(casos, struct{ doc, causa string }{"ligacao.png", ocrCausaNaoRegular})
	} else {
		t.Logf("sem ligacoes simbolicas neste sistema (%v): o caso da ligacao para fora da raiz nao corre aqui", err)
	}
	for _, c := range casos {
		t.Run(c.doc, func(t *testing.T) {
			gw := &aos530GwFalso{resp: aos530Resposta("texto", port.FinishStop, 1)}
			l := aos530LigacaoFalsa(t, pasta, gw)
			_, custo, err := l.executar(aos530ComCredencial(), aos530Entrada(c.doc))
			if causaDaFalhaOCR(err) != c.causa || custo != 0 || gw.contagem() != 0 {
				t.Fatalf("doc %q: quer %s sem pedido; veio %v custo=%d pedidos=%d", c.doc, c.causa, err, custo, gw.contagem())
			}
			if strings.Contains(err.Error(), c.doc) && c.doc != ".." {
				t.Fatalf("o erro da tool nao leva o doc_id: %v", err)
			}
		})
	}
}

// O PEDIDO E O RESULTADO. A instrução fixa na forma medida (system + user com o pedido e a imagem),
// sem tools, max_tokens 4096, com o token, o run, o passo e a tool de origem; o resultado
// `aos-ocr/v1` com o digest certo da imagem e o modelo servido, sem os bytes. PNG e JPEG.
func TestAOS530_OCR_OPedidoEOResultado(t *testing.T) {
	pasta := t.TempDir()
	imagens := map[string][]byte{"fatura.png": aos530PNG(t), "recibo.jpg": aos530JPEG(t)}
	for nome, dados := range imagens {
		if err := os.WriteFile(filepath.Join(pasta, nome), dados, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for nome, formato := range map[string]string{"fatura.png": "png", "recibo.jpg": "jpeg"} {
		t.Run(nome, func(t *testing.T) {
			gw := &aos530GwFalso{resp: aos530Resposta("FATURA 530\nTotal: 12,34 EUR", port.FinishStop, 123)}
			l := aos530LigacaoFalsa(t, pasta, gw)
			out, custo, err := l.executar(aos530ComCredencial(), aos530Entrada(nome))
			if err != nil || custo != 123 {
				t.Fatalf("executar: %v custo=%d", err, custo)
			}
			if gw.contagem() != 1 {
				t.Fatalf("uma chamada ao gateway; vieram %d", gw.contagem())
			}
			req := gw.pedidos[0]
			if req.Model != "ocr-visao" || req.Principal != "token-do-run" || req.RunID != "run-530" || req.StepID != "passo-7" ||
				req.OriginTool != ocrToolName || req.MaxTokens != ocrMaxTokens || len(req.Tools) != 0 || req.ToolChoice != "" ||
				req.Region != "eu" || req.Board != "board-eu" || req.Stream {
				t.Fatalf("pedido ao gateway fora da forma: %+v", req)
			}
			if len(req.Messages) != 2 || req.Messages[0].Role != port.RoleSystem || req.Messages[0].Content != ocrInstrucaoSistema ||
				len(req.Messages[0].Images) != 0 || req.Messages[1].Role != port.RoleUser || req.Messages[1].Content != ocrInstrucaoPedido ||
				len(req.Messages[1].Images) != 1 || !bytes.Equal(req.Messages[1].Images[0].Data, imagens[nome]) {
				t.Fatalf("mensagens fora da forma medida: %+v", req.Messages)
			}
			var r resultadoOCR
			dec := json.NewDecoder(bytes.NewReader(out))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&r); err != nil {
				t.Fatalf("resultado ilegivel: %v (%s)", err, out)
			}
			parte := port.ImagePart{Data: imagens[nome]}
			quer := resultadoOCR{Schema: ocrResultSchema, Motor: ocrMotorModelo, Texto: "FATURA 530\nTotal: 12,34 EUR",
				SHA256: parte.Digest(), Bytes: len(imagens[nome]), Formato: formato, Alias: "ocr-visao",
				ModeloServido: "openrouter/anthropic/modelo-de-visao", PerfilDigest: "sha256:perfil-do-registo",
				InstrucaoDigest: ocrDigestDaInstrucao(ocrInstrucaoSistema, ocrInstrucaoPedido)}
			if r != quer {
				t.Fatalf("resultado:\n veio %+v\n quer %+v", r, quer)
			}
			if bytes.Contains(out, []byte(aos530MarcaBinaria)) || bytes.Contains(out, []byte(base64.StdEncoding.EncodeToString(imagens[nome]))) {
				t.Fatal("o resultado leva os bytes da imagem")
			}
		})
	}
}

// FALHAS DO MODELO: causa fechada, nenhuma leva texto do modelo nem o corpo do provider, uma só
// chamada (sem nova tentativa), e o custo medido volta quando o provider respondeu.
func TestAOS530_OCR_FalhasDoModelo(t *testing.T) {
	pasta := t.TempDir()
	if err := os.WriteFile(filepath.Join(pasta, "a.png"), aos530PNG(t), 0o600); err != nil {
		t.Fatal(err)
	}
	const segredo = "TEXTO-DO-MODELO-OU-DO-PROVIDER-530"
	semEscolhas := aos530Resposta("", port.FinishStop, 5)
	semEscolhas.Choices = nil
	comTool := aos530Resposta(segredo, port.FinishToolCalls, 6)
	comTool.Choices[0].Message.ToolCalls = []port.ToolCall{{ID: "c", Type: "function", Function: port.FunctionCall{Name: "x", Arguments: "{}"}}}
	casos := []struct {
		nome    string
		gw      *aos530GwFalso
		causa   string
		custo   int64
		prazo   time.Duration
		cancela bool
	}{
		{nome: "cortada", gw: &aos530GwFalso{resp: aos530Resposta(segredo, port.FinishLength, 77)}, causa: ocrCausaCortado, custo: 77},
		{nome: "vazia", gw: &aos530GwFalso{resp: aos530Resposta(" \n\t", port.FinishStop, 8)}, causa: ocrCausaVazio, custo: 8},
		{nome: "sem escolhas", gw: &aos530GwFalso{resp: semEscolhas}, causa: ocrCausaVazio, custo: 5},
		{nome: "tool call", gw: &aos530GwFalso{resp: comTool}, causa: ocrCausaRespostaForma, custo: 6},
		{nome: "modelo servido diferente", gw: &aos530GwFalso{err: fmt.Errorf("gw: %w", &modelgateway.RouteVarianceError{Check: "diferente", Cause: modelgateway.RouteCauseModelDifferent})}, causa: ocrCausaModeloServido},
		{nome: "erro do gateway", gw: &aos530GwFalso{err: errors.New("provider 500: " + segredo)}, causa: ocrCausaGateway},
		{nome: "prazo esgotado", gw: &aos530GwFalso{esperar: true}, causa: ocrCausaTempo, prazo: 20 * time.Millisecond},
		{nome: "cancelado", gw: &aos530GwFalso{esperar: true}, causa: ocrCausaCancelado, cancela: true},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			l := aos530LigacaoFalsa(t, pasta, c.gw)
			if c.prazo > 0 {
				l.prazo = c.prazo
			}
			ctx := aos530ComCredencial()
			if c.cancela {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				go func() { time.Sleep(10 * time.Millisecond); cancel() }()
			}
			out, custo, err := l.executar(ctx, aos530Entrada("a.png"))
			if causaDaFalhaOCR(err) != c.causa || out != nil || custo != c.custo {
				t.Fatalf("quer %s custo=%d; veio %v custo=%d", c.causa, c.custo, err, custo)
			}
			if strings.Contains(err.Error(), segredo) || err.Error() != "ocr_read: falha="+c.causa {
				t.Fatalf("o erro da tool so leva a causa: %q", err)
			}
			if c.gw.contagem() != 1 {
				t.Fatalf("sem nova tentativa: quer 1 pedido, vieram %d", c.gw.contagem())
			}
			if l.lido(c.causa) != 1 || l.lido(ocrResultadoTranscrito) != 0 {
				t.Fatal("a metrica tinha de contar so a causa")
			}
		})
	}
}

// A REESCRITA DO EFEITO: só o `doc_id`, texto não vazio; o run e o passo da Call entram no input;
// outra tool passa inalterada; uma reescrita recusada é um erro (Deny no loop).
func TestAOS530_OCR_AReescritaDoEfeito(t *testing.T) {
	rw := newOCREffectRewriter()
	call := referencemonitor.Call{ToolID: ocrToolName, RunID: "r", StepID: "s", Input: []byte(`{"doc_id":"fatura.png"}`)}
	out, err := rw(call)
	if err != nil {
		t.Fatal(err)
	}
	var in entradaOCR
	if err := json.Unmarshal(out.Input, &in); err != nil || in != (entradaOCR{Schema: ocrInputSchema, RunID: "r", StepID: "s", DocID: "fatura.png"}) {
		t.Fatalf("entrada reescrita: %+v %v", in, err)
	}
	for _, mau := range []string{`{}`, `{"doc_id":""}`, `{"doc_id":3}`, `{"doc_id":"a","alias":"outro"}`, `nao json`, `{"caminho":"a"}`} {
		if _, err := rw(referencemonitor.Call{ToolID: ocrToolName, RunID: "r", StepID: "s", Input: []byte(mau)}); err == nil {
			t.Fatalf("args %s tinham de ser recusados", mau)
		}
	}
	if _, err := rw(referencemonitor.Call{ToolID: ocrToolName, Input: []byte(`{"doc_id":"a"}`)}); err == nil {
		t.Fatal("sem run nem passo tinha de recusar")
	}
	outra := referencemonitor.Call{ToolID: "doc_read", Input: []byte(`{"doc_id":"x"}`)}
	if got, err := rw(outra); err != nil || string(got.Input) != string(outra.Input) {
		t.Fatalf("outra tool passa inalterada: %v %s", err, got.Input)
	}
	// Composta com a da sandbox: cada uma só toca nas suas.
	sb := func(c referencemonitor.Call) (referencemonitor.Call, error) {
		c.Input = []byte("sandbox")
		return c, nil
	}
	junta := comporReescritas(sb, rw)
	if got, _ := junta(outra); string(got.Input) != "sandbox" {
		t.Fatal("a reescrita da sandbox tinha de correr para as outras tools")
	}
	if got, _ := junta(call); !strings.Contains(string(got.Input), ocrInputSchema) {
		t.Fatal("a reescrita da ocr_read tinha de correr para ela")
	}
	if comporReescritas(nil, nil) != nil {
		t.Fatal("sem nenhuma reescrita fica nil (byte-identico)")
	}
}

// O BLOCO `modelo` NO REGISTO: exclusivo de `sandbox`, só na `ocr_read`, com alias, a capability e
// o recurso da tool. Fora disto o registo não carrega.
func TestAOS530_OCR_OBlocoModeloNoRegisto(t *testing.T) {
	const boa = `{"name":"ocr_read","capability":"cap:fs.read","resource_type":"file","resource_value":"doc://{doc_id}","resource_region":"eu","modelo":{"alias":"ocr-visao"}}`
	t.Setenv("AOS_MODEL_TOOLS", writeTools(t, "["+boa+"]"))
	specs, err := readModelToolSpecs()
	if err != nil || specDoOCR(specs) == nil || specDoOCR(specs).Modelo.Alias != "ocr-visao" {
		t.Fatalf("a entrada boa tinha de carregar: %v", err)
	}
	if semExec := toolsSemExecutor(specs, nil); len(semExec) != 0 {
		t.Fatalf("a ocr_read tem executor (o gateway): %v", semExec)
	}
	casos := map[string]string{
		"os dois blocos":   strings.Replace(boa, `"modelo"`, `"sandbox":{"command":"read","path_arg":"doc_id"},"modelo"`, 1),
		"outro nome":       strings.Replace(boa, `"ocr_read"`, `"le_imagem"`, 1),
		"sem alias":        strings.Replace(boa, `"alias":"ocr-visao"`, `"alias":" "`, 1),
		"outra capability": strings.Replace(boa, `"cap:fs.read"`, `"cap:net.connect"`, 1),
		"recurso fixo":     strings.Replace(boa, `doc://{doc_id}`, `doc://tudo`, 1),
		// UM NOME REPETIDO: a segunda entrada, sem o bloco e com outra capability e outro recurso,
		// seria a que o enriquecedor do modelo lia (a última), enquanto o arranque conferia a
		// primeira. Recusado.
		"nome repetido":             boa + `,{"name":"ocr_read","capability":"cap:net.connect","resource_type":"file","resource_value":"doc://tudo","resource_region":"eu"}`,
		"nome repetido sem o bloco": `{"name":"doc_read","capability":"cap:fs.read"},{"name":" doc_read","capability":"cap:fs.read"}`,
	}
	for nome, mau := range casos {
		t.Run(nome, func(t *testing.T) {
			t.Setenv("AOS_MODEL_TOOLS", writeTools(t, "["+mau+"]"))
			_, err := readModelToolSpecs()
			if err == nil {
				t.Fatal("tinha de recusar")
			}
			if nome == "os dois blocos" && !errors.Is(err, ErrModelToolBindingConflict) {
				t.Fatalf("quer ErrModelToolBindingConflict; veio %v", err)
			}
			if nome != "os dois blocos" && !errors.Is(err, ErrBadModelTools) {
				t.Fatalf("quer ErrBadModelTools; veio %v", err)
			}
		})
	}
}
