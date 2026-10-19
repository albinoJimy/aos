package main

// AOS-536 (parte 2) — A TOOL `video_read`, peça a peça, sobre o gateway FALSO do AOS-530
// ([aos530GwFalso]).
//
// O caminho composto (o RM, o gateway real, a identidade, o custo, o replay e o arranque) está em
// aos536_video_read_no_test.go. Aqui: a forma do pedido e do resultado, os limites antes do custo
// (o formato MP4, a duração e o seu tecto, a leitura confinada partilhada com a `ocr_read`), as
// falhas do modelo, a leitura da `mvhd`, a fórmula da reserva, a reescrita do efeito e o bloco
// `modelo` no registo.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/metering/cost"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/platform/model-gateway/pricing"
)

// aos536MarcaBinaria só existe nos vídeos destes testes: vai no fim da `mdat`, e procura-se — e ao
// base64 do vídeo — em tudo o que o nó escreve.
const aos536MarcaBinaria = "\x00\xffMARCA-BINARIA-AOS536-91d2\x01\xfe"

// aos536Caixa escreve uma caixa MP4 com o tipo e o corpo dados.
func aos536Caixa(tipo string, corpo []byte) []byte {
	out := make([]byte, 8, 8+len(corpo))
	binary.BigEndian.PutUint32(out, uint32(8+len(corpo))) // #nosec G115 -- corpos pequenos de teste
	copy(out[4:], tipo)
	return append(out, corpo...)
}

// aos536Mvhd devolve o corpo de uma `mvhd` da versão dada (0 ou 1), com a escala e a duração dadas.
func aos536Mvhd(versao byte, escala uint32, duracao uint64) []byte {
	if versao == 1 {
		c := make([]byte, 112)
		c[0] = 1
		binary.BigEndian.PutUint32(c[20:], escala)
		binary.BigEndian.PutUint64(c[24:], duracao)
		return c
	}
	c := make([]byte, 100)
	binary.BigEndian.PutUint32(c[12:], escala)
	binary.BigEndian.PutUint32(c[16:], uint32(duracao)) // #nosec G115 -- duracoes de teste pequenas
	return c
}

// aos536MP4 gera um MP4 SINTÉTICO no molde do banco (`aos534MP4`, AOS-534): `ftyp` (`isom`), uma
// `moov` com a `mvhd` dada e uma `mdat` com bytes deterministas e a marca binária no fim. Válido
// como contentor (passa [port.VideoPart.Validate]) e não como vídeo.
func aos536MP4(mvhd []byte) []byte {
	dados := make([]byte, 64)
	for i := range dados {
		dados[i] = byte(i*31 + 7)
	}
	out := aos536Caixa("ftyp", []byte("isom\x00\x00\x02\x00isomiso2mp41"))
	out = append(out, aos536Caixa("moov", aos536Caixa("mvhd", mvhd))...)
	return append(out, aos536Caixa("mdat", append(dados, aos536MarcaBinaria...))...)
}

// aos536Video é o vídeo de 2 s dos testes.
func aos536Video() []byte { return aos536MP4(aos536Mvhd(0, 1000, 2000)) }

// aos536Resposta é a resposta do modelo de vídeo falso.
func aos536Resposta(texto, fim string, custo int64) port.ChatResponse {
	return port.ChatResponse{
		Model:   "openrouter/google/modelo-de-video",
		Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: texto}, FinishReason: fim}},
		Usage:   port.Usage{PromptTokens: 1000, CompletionTokens: 50, TotalTokens: 1050, CostMicroUSD: custo},
		Route:   port.ServedRoute{Model: "openrouter/google/modelo-de-video"},
	}
}

// aos536LigacaoFalsa monta a ligação sobre a pasta e o gateway dados.
func aos536LigacaoFalsa(t *testing.T, pasta string, gw gatewayDoOCR) *ligacaoVideo {
	t.Helper()
	raiz, err := os.OpenRoot(pasta)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raiz.Close() })
	l := novaLigacaoVideo("video-visao", "eu", "board-eu", "sha256:perfil-do-registo-de-video", pasta, raiz)
	l.gw = gw
	return l
}

// aos536Entrada é o input da tool depois da reescrita.
func aos536Entrada(doc string) []byte {
	raw, _ := json.Marshal(entradaOCR{Schema: videoInputSchema, RunID: "run-536", StepID: "passo-3", DocID: doc})
	return raw
}

// aos536Escrever escreve um ficheiro na pasta.
func aos536Escrever(t *testing.T, pasta, nome string, dados []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(pasta, nome), dados, 0o600); err != nil {
		t.Fatal(err)
	}
}

// SEM CREDENCIAL A TOOL FALHA ANTES DE LER O FICHEIRO.
func TestAOS536_Video_SemCredencialFalhaAntesDeLer(t *testing.T) {
	gw := &aos530GwFalso{resp: aos536Resposta("x", port.FinishStop, 1)}
	l := aos536LigacaoFalsa(t, t.TempDir(), gw)
	out, custo, err := l.executar(context.Background(), aos536Entrada("nao-existe.mp4"))
	if causaDaFalhaDeTool(err) != ocrCausaSemNHI || out != nil || custo != 0 || gw.contagem() != 0 || l.lido(ocrCausaSemNHI) != 1 {
		t.Fatalf("quer sem_credencial sem ler nem chamar; veio %v pedidos=%d", err, gw.contagem())
	}
	// Um input com o schema da OUTRA tool de modelo é um erro de entrada.
	raw, _ := json.Marshal(entradaOCR{Schema: ocrInputSchema, RunID: "r", StepID: "s", DocID: "a.mp4"})
	if _, _, err := l.executar(aos530ComCredencial(), raw); causaDaFalhaDeTool(err) != ocrCausaEntrada {
		t.Fatalf("o schema da ocr_read nao e o da video_read: %v", err)
	}
}

// O PEDIDO E O RESULTADO. A instrução fixa na forma medida pelo banco (system + user com o pedido e
// o vídeo), sem tools e sem imagens, max_tokens 8192, com o token, o run, o passo e a tool de origem
// `video_read`; o resultado `aos-video/v1` com o digest, o tamanho e a duração do vídeo e o modelo
// servido, sem os bytes. A `mvhd` da versão 0 e a da 1.
func TestAOS536_Video_OPedidoEOResultado(t *testing.T) {
	pasta := t.TempDir()
	videos := map[string]struct {
		dados []byte
		ms    int64
	}{
		"v0.mp4": {aos536Video(), 2000},
		"v1.mp4": {aos536MP4(aos536Mvhd(1, 90000, 90000*45/10)), 4500},
	}
	for nome, v := range videos {
		aos536Escrever(t, pasta, nome, v.dados)
	}
	for nome, v := range videos {
		t.Run(nome, func(t *testing.T) {
			gw := &aos530GwFalso{resp: aos536Resposta("CENA 1\nFACTS\nTEXT: OLA", port.FinishStop, 120)}
			l := aos536LigacaoFalsa(t, pasta, gw)
			out, custo, err := l.executar(aos530ComCredencial(), aos536Entrada(nome))
			if err != nil || custo != 120 || gw.contagem() != 1 {
				t.Fatalf("executar: %v custo=%d pedidos=%d", err, custo, gw.contagem())
			}
			req := gw.pedidos[0]
			if req.Model != "video-visao" || req.Principal != "token-do-run" || req.RunID != "run-536" || req.StepID != "passo-3" ||
				req.OriginTool != videoToolName || req.MaxTokens != videoMaxTokens || videoMaxTokens != 8192 || len(req.Tools) != 0 || req.ToolChoice != "" ||
				req.Region != "eu" || req.Board != "board-eu" || req.Stream {
				t.Fatalf("pedido ao gateway fora da forma: %+v", req)
			}
			if len(req.Messages) != 2 || req.Messages[0].Role != port.RoleSystem || req.Messages[0].Content != videoInstrucaoSistema ||
				len(req.Messages[0].Videos) != 0 || req.Messages[1].Role != port.RoleUser || req.Messages[1].Content != videoInstrucaoPedido ||
				len(req.Messages[1].Images) != 0 || len(req.Messages[1].Videos) != 1 ||
				req.Messages[1].Videos[0].MediaType != port.VideoMediaMP4 || !bytes.Equal(req.Messages[1].Videos[0].Data, v.dados) {
				t.Fatalf("mensagens fora da forma medida: %+v", req.Messages)
			}
			var r resultadoVideo
			dec := json.NewDecoder(bytes.NewReader(out))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&r); err != nil {
				t.Fatalf("resultado ilegivel: %v (%s)", err, out)
			}
			quer := resultadoVideo{Schema: videoResultSchema, Texto: "CENA 1\nFACTS\nTEXT: OLA",
				SHA256: port.VideoPart{Data: v.dados}.Digest(), Bytes: len(v.dados), DuracaoMs: v.ms, Alias: "video-visao",
				ModeloServido: "openrouter/google/modelo-de-video", PerfilDigest: "sha256:perfil-do-registo-de-video",
				InstrucaoDigest: videoDigestDaInstrucao(videoInstrucaoSistema, videoInstrucaoPedido)}
			if r != quer {
				t.Fatalf("resultado:\n veio %+v\n quer %+v", r, quer)
			}
			if bytes.Contains(out, []byte(aos536MarcaBinaria)) || bytes.Contains(out, []byte(base64.StdEncoding.EncodeToString(v.dados))) {
				t.Fatal("o resultado leva os bytes do video")
			}
			if l.lido(videoResultadoDescrito) != 1 {
				t.Fatal("a metrica tinha de contar o descrito")
			}
		})
	}
}

// LIMITES ANTES DO CUSTO. A leitura confinada é a da `ocr_read`, com o tecto de bytes do vídeo; o
// formato é o MP4 da porta; a duração lê-se da `mvhd` e tem tecto. Cada recusa tem causa própria e o
// gateway conta zero pedidos.
func TestAOS536_Video_LimitesAntesDoCusto(t *testing.T) {
	fora, pasta := t.TempDir(), t.TempDir()
	aos536Escrever(t, fora, "segredo.mp4", aos536Video())
	if err := os.Mkdir(filepath.Join(pasta, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	aos536Escrever(t, pasta, filepath.Join("sub", "dentro.mp4"), aos536Video())
	// O tecto de bytes é o do vídeo (4 MiB), não o da imagem: um MP4 de 5 MiB é grande.
	grande := aos536MP4(aos536Mvhd(0, 1000, 2000))
	grande = append(grande, aos536Caixa("free", make([]byte, port.MaxVideoBytes))...)
	aos536Escrever(t, pasta, "grande.mp4", grande)
	aos536Escrever(t, pasta, "imagem.mp4", aos530PNG(t))
	aos536Escrever(t, pasta, "vazio.mp4", nil)
	// moov depois da mdat (sem +faststart).
	semFaststart := aos536Caixa("ftyp", []byte("isom\x00\x00\x02\x00isom"))
	semFaststart = append(semFaststart, aos536Caixa("mdat", []byte("dados"))...)
	semFaststart = append(semFaststart, aos536Caixa("moov", aos536Caixa("mvhd", aos536Mvhd(0, 1000, 2000)))...)
	aos536Escrever(t, pasta, "sem-faststart.mp4", semFaststart)
	// moov sem mvhd; mvhd sem escala; mvhd curta; duração zero.
	semMvhd := aos536Caixa("ftyp", []byte("isom\x00\x00\x02\x00isom"))
	semMvhd = append(semMvhd, aos536Caixa("moov", aos536Caixa("trak", nil))...)
	semMvhd = append(semMvhd, aos536Caixa("mdat", []byte("dados"))...)
	aos536Escrever(t, pasta, "sem-mvhd.mp4", semMvhd)
	aos536Escrever(t, pasta, "sem-escala.mp4", aos536MP4(aos536Mvhd(0, 0, 2000)))
	aos536Escrever(t, pasta, "mvhd-curta.mp4", aos536MP4(make([]byte, 10)))
	aos536Escrever(t, pasta, "zero.mp4", aos536MP4(aos536Mvhd(0, 1000, 0)))
	// A duração acima do tecto, por 1 ms e muito.
	aos536Escrever(t, pasta, "longo.mp4", aos536MP4(aos536Mvhd(0, 1000, 60_001)))
	aos536Escrever(t, pasta, "muito-longo.mp4", aos536MP4(aos536Mvhd(1, 1000, 3_600_000)))
	aos536Escrever(t, pasta, "absurdo.mp4", aos536MP4(aos536Mvhd(1, 1, 1<<41)))

	casos := []struct{ doc, causa string }{
		{"../" + filepath.Base(fora) + "/segredo.mp4", ocrCausaForaDaRaiz},
		{filepath.Join(fora, "segredo.mp4"), ocrCausaForaDaRaiz},
		{"sub/dentro.mp4", ocrCausaForaDaRaiz},
		{`sub\dentro.mp4`, ocrCausaForaDaRaiz},
		{"c:segredo.mp4", ocrCausaForaDaRaiz},
		{"nao-existe.mp4", ocrCausaEmFalta},
		{"sub", ocrCausaNaoRegular},
		{"segredo.mp4.", ocrCausaNomeNaoCanonico},
		{strings.Repeat("a", ocrMaxNome) + ".mp4", ocrCausaEntrada},
		{"grande.mp4", ocrCausaGrande},
		{"imagem.mp4", ocrCausaFormato},
		{"vazio.mp4", ocrCausaFormato},
		{"sem-faststart.mp4", ocrCausaFormato},
		{"sem-mvhd.mp4", ocrCausaFormato},
		{"sem-escala.mp4", ocrCausaFormato},
		{"mvhd-curta.mp4", ocrCausaFormato},
		{"zero.mp4", ocrCausaFormato},
		{"absurdo.mp4", ocrCausaFormato},
		{"longo.mp4", videoCausaLongo},
		{"muito-longo.mp4", videoCausaLongo},
	}
	if err := os.Symlink(filepath.Join(fora, "segredo.mp4"), filepath.Join(pasta, "ligacao.mp4")); err == nil {
		casos = append(casos, struct{ doc, causa string }{"ligacao.mp4", ocrCausaNaoRegular})
	} else {
		t.Logf("sem ligacoes simbolicas neste sistema (%v): o caso da ligacao para fora da raiz nao corre aqui", err)
	}
	for _, c := range casos {
		t.Run(c.doc, func(t *testing.T) {
			gw := &aos530GwFalso{resp: aos536Resposta("texto", port.FinishStop, 1)}
			l := aos536LigacaoFalsa(t, pasta, gw)
			_, custo, err := l.executar(aos530ComCredencial(), aos536Entrada(c.doc))
			if causaDaFalhaDeTool(err) != c.causa || custo != 0 || gw.contagem() != 0 || l.lido(c.causa) != 1 {
				t.Fatalf("doc %q: quer %s sem pedido; veio %v custo=%d pedidos=%d", c.doc, c.causa, err, custo, gw.contagem())
			}
			if strings.Contains(err.Error(), c.doc) || !strings.HasPrefix(err.Error(), "video_read: falha=") {
				t.Fatalf("o erro da tool so leva a causa: %v", err)
			}
		})
	}
	// Controlo: no tecto exacto (60 s) a tool chama o modelo.
	aos536Escrever(t, pasta, "no-tecto.mp4", aos536MP4(aos536Mvhd(0, 1000, 60_000)))
	gw := &aos530GwFalso{resp: aos536Resposta("texto", port.FinishStop, 1)}
	if _, _, err := aos536LigacaoFalsa(t, pasta, gw).executar(aos530ComCredencial(), aos536Entrada("no-tecto.mp4")); err != nil || gw.contagem() != 1 {
		t.Fatalf("um video de 60 s cabe no tecto: %v", err)
	}
}

// A LEITURA CONFINADA É A DA `ocr_read`: as defesas sobre o descritor, com a raiz falsa do AOS-530.
// O nome trocado por outro ficheiro da raiz, o descritor que deixou de ser regular ou que cresceu
// acima do tecto do VÍDEO — e o controlo, que lê.
func TestAOS536_Video_LeituraComARaizFalsa(t *testing.T) {
	mp4 := aos536Video()
	info := aos530Info{tam: int64(len(mp4)), id: "clip.mp4"}
	casos := []struct {
		nome  string
		raiz  *aos530RaizFalsa
		causa string
	}{
		{"trocado por outro ficheiro da raiz", &aos530RaizFalsa{lstatInfo: info, ficheiro: &aos530Ficheiro{r: bytes.NewReader(mp4), info: aos530Info{tam: int64(len(mp4)), id: "outro.mp4"}}}, ocrCausaTrocado},
		{"descritor nao regular", &aos530RaizFalsa{lstatInfo: info, ficheiro: &aos530Ficheiro{r: bytes.NewReader(mp4), info: aos530Info{modo: fs.ModeNamedPipe}}}, ocrCausaNaoRegular},
		{"descritor acima do tecto do video", &aos530RaizFalsa{lstatInfo: info, ficheiro: &aos530Ficheiro{r: bytes.NewReader(mp4), info: aos530Info{tam: port.MaxVideoBytes + 1, id: "clip.mp4"}}}, ocrCausaGrande},
		{"sem permissao", &aos530RaizFalsa{lstatErr: &fs.PathError{Op: "lstat", Path: "x", Err: fs.ErrPermission}}, ocrCausaIlegivel},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			gw := &aos530GwFalso{resp: aos536Resposta("texto", port.FinishStop, 1)}
			l := aos536LigacaoFalsa(t, t.TempDir(), gw)
			l.raiz = c.raiz
			if _, custo, err := l.executar(aos530ComCredencial(), aos536Entrada("clip.mp4")); causaDaFalhaDeTool(err) != c.causa || custo != 0 || gw.contagem() != 0 {
				t.Fatalf("quer %s sem pedido; veio %v", c.causa, err)
			}
		})
	}
	// Controlo: a mesma raiz falsa, sem nada trocado, descreve — os casos acima não falham por outra
	// razão.
	gw := &aos530GwFalso{resp: aos536Resposta("texto", port.FinishStop, 1)}
	l := aos536LigacaoFalsa(t, t.TempDir(), gw)
	l.raiz = &aos530RaizFalsa{lstatInfo: info, ficheiro: &aos530Ficheiro{r: bytes.NewReader(mp4), info: info}}
	if _, _, err := l.executar(aos530ComCredencial(), aos536Entrada("clip.mp4")); err != nil || gw.contagem() != 1 {
		t.Fatalf("o controlo tinha de descrever: %v", err)
	}
}

// FALHAS DO MODELO: causa fechada (as do vídeo para a resposta cortada e vazia), nenhuma leva texto
// do modelo, uma só chamada, e o custo medido volta quando o provider respondeu.
func TestAOS536_Video_FalhasDoModelo(t *testing.T) {
	pasta := t.TempDir()
	aos536Escrever(t, pasta, "a.mp4", aos536Video())
	const segredo = "TEXTO-DO-MODELO-OU-DO-PROVIDER-536"
	semEscolhas := aos536Resposta("", port.FinishStop, 5)
	semEscolhas.Choices = nil
	comTool := aos536Resposta(segredo, port.FinishToolCalls, 6)
	comTool.Choices[0].Message.ToolCalls = []port.ToolCall{{ID: "c", Type: "function", Function: port.FunctionCall{Name: "x", Arguments: "{}"}}}
	casos := []struct {
		nome    string
		gw      *aos530GwFalso
		causa   string
		custo   int64
		prazo   time.Duration
		cancela bool
	}{
		{nome: "cortada", gw: &aos530GwFalso{resp: aos536Resposta(segredo, port.FinishLength, 77)}, causa: videoCausaCortado, custo: 77},
		{nome: "vazia", gw: &aos530GwFalso{resp: aos536Resposta(" \n\t", port.FinishStop, 8)}, causa: videoCausaVazio, custo: 8},
		{nome: "sem escolhas", gw: &aos530GwFalso{resp: semEscolhas}, causa: videoCausaVazio, custo: 5},
		{nome: "tool call", gw: &aos530GwFalso{resp: comTool}, causa: ocrCausaRespostaForma, custo: 6},
		{nome: "modelo servido diferente", gw: &aos530GwFalso{err: fmt.Errorf("gw: %w", &modelgateway.RouteVarianceError{Check: "diferente", Cause: modelgateway.RouteCauseModelDifferent})}, causa: ocrCausaModeloServido},
		{nome: "erro do gateway", gw: &aos530GwFalso{err: errors.New("provider 500: " + segredo)}, causa: ocrCausaGateway},
		{nome: "prazo esgotado", gw: &aos530GwFalso{esperar: true}, causa: ocrCausaTempo, prazo: 20 * time.Millisecond},
		{nome: "cancelado", gw: &aos530GwFalso{esperar: true}, causa: ocrCausaCancelado, cancela: true},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			l := aos536LigacaoFalsa(t, pasta, c.gw)
			if l.prazo != videoPrazo || videoPrazo != 120*time.Second {
				t.Fatalf("o prazo da chamada e o da tool: %s", l.prazo)
			}
			if c.prazo > 0 {
				l.prazo = c.prazo
			}
			ctx := aos530ComCredencial()
			if c.cancela {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				go func() { time.Sleep(10 * time.Millisecond); cancel() }()
			}
			out, custo, err := l.executar(ctx, aos536Entrada("a.mp4"))
			if causaDaFalhaDeTool(err) != c.causa || out != nil || custo != c.custo {
				t.Fatalf("quer %s custo=%d; veio %v custo=%d", c.causa, c.custo, err, custo)
			}
			if strings.Contains(err.Error(), segredo) || err.Error() != "video_read: falha="+c.causa {
				t.Fatalf("o erro da tool so leva a causa: %q", err)
			}
			if c.gw.contagem() != 1 || l.lido(c.causa) != 1 || l.lido(videoResultadoDescrito) != 0 {
				t.Fatalf("sem nova tentativa e a metrica conta so a causa: pedidos=%d", c.gw.contagem())
			}
		})
	}
}

// UM EVENTO DE CUSTO POR RESPOSTA PAGA, com o nome da tool `video_read` — o mesmo evento da
// `ocr_read` — e nenhum sem resposta.
func TestAOS536_Video_UmEventoPorRespostaPaga(t *testing.T) {
	pasta := t.TempDir()
	aos536Escrever(t, pasta, "a.mp4", aos536Video())
	for _, c := range []struct {
		nome      string
		gw        *aos530GwFalso
		resultado string
	}{
		{"descrito", &aos530GwFalso{resp: aos536Resposta("DESCRICAO", port.FinishStop, 120)}, videoResultadoDescrito},
		{"cortada", &aos530GwFalso{resp: aos536Resposta("DESCRICAO", port.FinishLength, 120)}, videoCausaCortado},
		{"sem resposta", &aos530GwFalso{err: errors.New("provider 500")}, ""},
	} {
		t.Run(c.nome, func(t *testing.T) {
			l := aos536LigacaoFalsa(t, pasta, c.gw)
			reg := &aos530Registos{}
			l.ligarCusto(nil, reg.registar, nil)
			_, _, _ = l.executar(aos530ComCredencial(), aos536Entrada("a.mp4"))
			if c.resultado == "" {
				if len(reg.p) != 0 {
					t.Fatalf("sem resposta nao ha evento: %+v", reg.p)
				}
				return
			}
			if len(reg.p) != 1 || reg.run[0] != "run-536" {
				t.Fatalf("quer 1 evento do run-536: %+v", reg.p)
			}
			p := reg.p[0]
			if p.Tool != videoToolName || p.StepID != "passo-3" || p.Alias != "video-visao" || p.Resultado != c.resultado || p.CostMicroUSD != 120 ||
				p.InputTokens != 1000 || p.OutputTokens != 50 || p.ModeloServido != "openrouter/google/modelo-de-video" {
				t.Fatalf("payload do evento: %+v", p)
			}
			raw, _ := json.Marshal(p)
			if strings.Contains(string(raw), "DESCRICAO") || strings.Contains(string(raw), "a.mp4") {
				t.Fatalf("o evento nao leva texto nem o doc_id: %s", raw)
			}
		})
	}
}

// A DURAÇÃO DA `mvhd`: versões 0 e 1, a primeira `moov` e a primeira `mvhd`, e as recusas.
func TestAOS536_Video_DuracaoDaMvhd(t *testing.T) {
	casos := []struct {
		nome string
		mp4  []byte
		quer time.Duration
		ok   bool
	}{
		{"v0", aos536MP4(aos536Mvhd(0, 1000, 2000)), 2 * time.Second, true},
		{"v1", aos536MP4(aos536Mvhd(1, 90000, 90000*30)), 30 * time.Second, true},
		{"v0 escala 600", aos536MP4(aos536Mvhd(0, 600, 900)), 1500 * time.Millisecond, true},
		{"sem escala", aos536MP4(aos536Mvhd(0, 0, 10)), 0, false},
		{"absurda", aos536MP4(aos536Mvhd(1, 1, 1<<41)), 0, false},
		// Abaixo da guarda das unidades, mas os ns não cabem num int64: sem a guarda dos ms dava a
		// volta e passava por ~290 ms (revisão adversarial do AOS-536).
		{"absurda abaixo da guarda das unidades", aos536MP4(aos536Mvhd(1, 1, 18446744074)), 0, false},
		{"maior que cabe", aos536MP4(aos536Mvhd(1, 1000, 1<<40)), time.Duration(1<<40) * time.Millisecond, true},
		{"curta", aos536MP4(make([]byte, 19)), 0, false},
		{"versao desconhecida", aos536MP4(append([]byte{2}, make([]byte, 120)...)), 0, false},
		{"sem moov", append(aos536Caixa("ftyp", []byte("isom")), aos536Caixa("mdat", nil)...), 0, false},
		{"caixa truncada", append(aos536Caixa("ftyp", []byte("isom")), 0, 0, 0, 99, 'm', 'o', 'o', 'v'), 0, false},
	}
	for _, c := range casos {
		got, ok := duracaoDoMP4(c.mp4)
		if ok != c.ok || got != c.quer {
			t.Fatalf("%s: quer %s %v; veio %s %v", c.nome, c.quer, c.ok, got, ok)
		}
	}
}

// A RESERVA É FUNÇÃO DA DURAÇÃO: folga 2 × (segundos arredondados para cima × 300 + 512) tokens de
// entrada, mais os 8192 de saída; o custo desses tokens pela tabela, pelo calculador do gateway.
func TestAOS536_Video_AReservaEFuncaoDaDuracao(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		quer int64
	}{
		{time.Millisecond, 2 * (300 + 512)},
		{2 * time.Second, 2 * (600 + 512)},
		{2001 * time.Millisecond, 2 * (900 + 512)},
		{videoDuracaoMax, 37_024},
	} {
		if got := videoTokensDeEntrada(c.d); got != c.quer {
			t.Fatalf("%s: quer %d tokens de entrada; veio %d", c.d, c.quer, got)
		}
	}
	tabela, err := pricing.LoadTable([]byte(aos536PrecoComVideo))
	if err != nil {
		t.Fatal(err)
	}
	amt, tecto, err := reservaDoVideo(tabela, aos536AliasVideo, "eu", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	quer, err := cost.NewCalculator(tabela).Cost(cost.TokenCounts{PromptTokens: 2224, CompletionTokens: 8192}, aos536AliasVideo, "eu")
	if err != nil {
		t.Fatal(err)
	}
	if amt.Tokens != 2224+8192 || amt.CostMicroUSD != quer.CostMicroUSD || amt.CostMicroUSD == 0 || tecto != (tectoDaChamada{entrada: 2224, saida: 8192}) {
		t.Fatalf("reserva de 2 s: %+v %+v (quer %d micro-USD)", amt, tecto, quer.CostMicroUSD)
	}
	longa, _, err := reservaDoVideo(tabela, aos536AliasVideo, "eu", videoDuracaoMax)
	if err != nil || longa.CostMicroUSD <= amt.CostMicroUSD {
		t.Fatalf("a reserva de 60 s tinha de ser maior: %+v (%v)", longa, err)
	}
	if semTabela, _, err := reservaDoVideo(nil, aos536AliasVideo, "eu", 2*time.Second); err != nil || semTabela.CostMicroUSD != 0 || semTabela.Tokens != 2224+8192 {
		t.Fatalf("sem tabela, so tokens: %+v %v", semTabela, err)
	}
	if _, _, err := reservaDoVideo(tabela, "sem-preco", "eu", time.Second); err == nil {
		t.Fatal("sem o par na tabela tinha de recusar")
	}
}

// A REESCRITA DO EFEITO: a da `ocr_read`, com o schema da `video_read`; cada uma só toca na sua, e
// as duas juntas com a da sandbox.
func TestAOS536_Video_AReescritaDoEfeito(t *testing.T) {
	video := newEfeitoDeModeloRewriter(videoToolName, videoInputSchema)
	junta := juntarReescritasDeModelo(newOCREffectRewriter(), video)
	for tool, schema := range map[string]string{ocrToolName: ocrInputSchema, videoToolName: videoInputSchema} {
		out, err := junta(referencemonitor.Call{ToolID: tool, RunID: "r", StepID: "s", Input: []byte(`{"doc_id":"x"}`)})
		var in entradaOCR
		if err != nil || json.Unmarshal(out.Input, &in) != nil || in != (entradaOCR{Schema: schema, RunID: "r", StepID: "s", DocID: "x"}) {
			t.Fatalf("%s: entrada reescrita %+v %v", tool, in, err)
		}
	}
	for _, mau := range []string{`{}`, `{"doc_id":""}`, `{"doc_id":"a","alias":"outro"}`, `nao json`} {
		if _, err := junta(referencemonitor.Call{ToolID: videoToolName, RunID: "r", StepID: "s", Input: []byte(mau)}); err == nil || !strings.HasPrefix(err.Error(), "video_read: ") {
			t.Fatalf("args %s tinham de ser recusados pela video_read: %v", mau, err)
		}
	}
	outra := referencemonitor.Call{ToolID: "doc_read", Input: []byte(`{"doc_id":"x"}`)}
	if got, err := junta(outra); err != nil || string(got.Input) != string(outra.Input) {
		t.Fatalf("outra tool passa inalterada: %v", err)
	}
	sb := func(c referencemonitor.Call) (referencemonitor.Call, error) {
		c.Input = []byte("sandbox")
		return c, nil
	}
	composta := comporReescritas(sb, junta)
	if got, _ := composta(outra); string(got.Input) != "sandbox" {
		t.Fatal("a da sandbox corre para as outras tools")
	}
	if got, _ := composta(referencemonitor.Call{ToolID: videoToolName, RunID: "r", StepID: "s", Input: []byte(`{"doc_id":"x"}`)}); !strings.Contains(string(got.Input), videoInputSchema) {
		t.Fatal("a da video_read corre para ela")
	}
	if juntarReescritasDeModelo(nil, nil) != nil {
		t.Fatal("sem nenhuma, nil")
	}
}

// O BLOCO `modelo` NA `video_read`: aceite com a capability e o recurso da `ocr_read`; com outros,
// recusado; as duas tools no mesmo registo; e a tool tem executor.
func TestAOS536_Video_OBlocoModeloNoRegisto(t *testing.T) {
	const ocr = `{"name":"ocr_read","capability":"cap:fs.read","resource_type":"file","resource_value":"doc://{doc_id}","resource_region":"eu","egress":"external","modelo":{"alias":"ocr-visao"}}`
	const boa = `{"name":"video_read","capability":"cap:fs.read","resource_type":"file","resource_value":"doc://{doc_id}","resource_region":"eu","egress":"external","modelo":{"alias":"video-visao"}}`
	t.Setenv("AOS_MODEL_TOOLS", writeTools(t, "["+ocr+","+boa+"]"))
	specs, err := readModelToolSpecs()
	if err != nil {
		t.Fatalf("as duas tools tinham de carregar: %v", err)
	}
	if v := specDaToolDeModelo(specs, videoToolName); v == nil || v.Modelo.Alias != "video-visao" {
		t.Fatal("a video_read tinha de ser encontrada")
	}
	if o := specDoOCR(specs); o == nil || o.Modelo.Alias != "ocr-visao" {
		t.Fatal("a ocr_read tinha de continuar a ser encontrada pelo nome")
	}
	if semExec := toolsSemExecutor(specs, nil); len(semExec) != 0 {
		t.Fatalf("as duas tem executor: %v", semExec)
	}
	casos := map[string]string{
		"outra capability":        strings.Replace(boa, `"cap:fs.read"`, `"cap:net.connect"`, 1),
		"recurso fixo":            strings.Replace(boa, `doc://{doc_id}`, `doc://tudo`, 1),
		"egress none":             strings.Replace(boa, `"egress":"external"`, `"egress":"none","mutation":"none","reversibility":"reversible"`, 1),
		"egress implicito":        strings.Replace(boa, `"egress":"external",`, ``, 1),
		"sem alias":               strings.Replace(boa, `"alias":"video-visao"`, `"alias":""`, 1),
		"outro nome":              strings.Replace(boa, `"video_read"`, `"le_video"`, 1),
		"com sandbox":             strings.Replace(boa, `"modelo"`, `"sandbox":{"command":"read","path_arg":"doc_id"},"modelo"`, 1),
		"nome repetido sem bloco": boa + `,{"name":"video_read","capability":"cap:net.connect","resource_type":"file","resource_value":"doc://tudo","resource_region":"eu"}`,
	}
	for nome, mau := range casos {
		t.Run(nome, func(t *testing.T) {
			t.Setenv("AOS_MODEL_TOOLS", writeTools(t, "["+mau+"]"))
			if _, err := readModelToolSpecs(); err == nil {
				t.Fatal("tinha de recusar")
			}
		})
	}
	// Sem o bloco, uma tool chamada `video_read` é uma tool comum (sem executor): o nó não a compõe.
	t.Setenv("AOS_MODEL_TOOLS", writeTools(t, `[{"name":"video_read","capability":"cap:fs.read","resource_type":"file","resource_value":"doc://{doc_id}","resource_region":"eu"}]`))
	if _, video, err := toolsDeModeloNoRegisto(); err != nil || video {
		t.Fatalf("sem o bloco a video_read nao e uma tool de modelo: %v %v", video, err)
	}
}
