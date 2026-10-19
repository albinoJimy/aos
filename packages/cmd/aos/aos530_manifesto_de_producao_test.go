package main

// AOS-530 — O CASO DE PRODUÇÃO DEPOIS DA RELEASE.
//
// Desde a emenda de 2026-10-18 ao ADR-034 §2.7 (decisão do dono: «aceito a excepção, e resolva
// para a ocr_read») o manifesto de PRODUÇÃO (`deploy/server/model-tools/tools.json`) oferece a
// `ocr_read` ao lado da `doc_read`. Os outros testes do AOS-530 compõem o nó com um registo de
// tools escrito pelo teste; este usa o ficheiro REAL, com o Model Gateway composto pelo harness do
// AOS-530 ([aos530AmbienteOCR]), e prova que o nó arranca, liga a tool e a serve no `GET /tools`
// — o que o nó de produção faz depois da release, com os actos do dono já feitos (allowlist,
// registo de uso tool, preço, rota, pasta semeada).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sandbox "github.com/aos-ref/substrate/sandbox"
)

// aos530NoComOManifestoDeProducao compõe o nó com o manifesto de produção REAL e o gateway do
// harness do AOS-536, com os registos de uso tool das duas tools de modelo — desde a emenda de
// 2026-10-19 (AOS-536) o manifesto também tem a `video_read`. Escreve o arranque em w.
func aos530NoComOManifestoDeProducao(t *testing.T, w io.Writer) *Node {
	t.Helper()
	aos536AmbienteVideo(t, aos536Opcoes{comOCR: true})
	t.Setenv("AOS_MODEL_TOOLS", aos441ManifestoDeProducao)
	modelo, binder, err := parseModelFromEnv(false)
	if err != nil {
		t.Fatalf("parseModelFromEnv: %v", err)
	}
	dir := t.TempDir()
	cfg := tnBaseConfig()
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	cfg.Model = modelo
	cfg.ModelID = aos486Modelo
	cfg.ModelIdentityBinder = binder
	cfg.SteerClock = tnClock()
	node, err := Bootstrap(context.Background(), cfg, w)
	if err != nil {
		t.Fatalf("Bootstrap com o manifesto de producao e o gateway composto: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	return node
}

// TestAOS530_No_ManifestoDeProducaoComGatewayServeAOCR: com o manifesto de produção real e o
// gateway composto, o nó arranca com a `ocr_read` LIGADA e o servidor que o entrypoint constrói
// ([serveAPI]) serve-a no `GET /tools` com os eixos que o manifesto declara.
func TestAOS530_No_ManifestoDeProducaoComGatewayServeAOCR(t *testing.T) {
	clearIngressEnv(t)
	var arranque bytes.Buffer
	node := aos530NoComOManifestoDeProducao(t, &arranque)
	if node.ocr == nil {
		t.Fatal("com a ocr_read no manifesto de producao e o gateway composto, a ligacao tinha de existir")
	}
	if !strings.Contains(arranque.String(), "tool "+ocrToolName+" LIGADA") {
		t.Fatalf("o arranque tinha de declarar a tool ligada:\n%s", arranque.String())
	}
	if node.video == nil || !strings.Contains(arranque.String(), "tool "+videoToolName+" LIGADA") {
		t.Fatalf("com a video_read no manifesto de producao, a ligacao tinha de existir e ser declarada:\n%s", arranque.String())
	}

	addr := portaLivreLoopback(t)
	ctx, cancel := context.WithCancel(context.Background())
	fim := make(chan error, 1)
	go func() { fim <- serveAPI(ctx, io.Discard, node, addr) }()
	defer func() {
		cancel()
		select {
		case err := <-fim:
			if err != nil {
				t.Errorf("serveAPI devia encerrar graciosamente, veio %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("serveAPI nao encerrou apos cancelamento do ctx")
		}
	}()
	esperarPorta(t, addr)

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + addr + "/tools")
	if err != nil {
		t.Fatalf("GET /tools: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var corpo respostaDoCatalogo
	if err := json.NewDecoder(resp.Body).Decode(&corpo); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /tools: %d %v", resp.StatusCode, err)
	}
	porNome := map[string]entradaDoCatalogo{}
	for _, e := range corpo.Tools {
		porNome[e.Name] = e
	}
	if len(porNome) != 3 {
		t.Fatalf("o GET /tools de producao tinha de servir doc_read, ocr_read e video_read; serviu %+v", corpo.Tools)
	}
	if v, ok := porNome[videoToolName]; !ok || v.Egress != "external" || v.Reversibility != "reversible" || v.Mutation != "none" || len(v.Digest) != len("sha256:")+64 {
		t.Fatalf("video_read: %+v — esperado egress external, reversible, mutation none e um digest sha256", v)
	}
	ocr, ok := porNome[ocrToolName]
	if !ok {
		t.Fatalf("o GET /tools nao serve a ocr_read: %+v", corpo.Tools)
	}
	if ocr.Egress != "external" || ocr.Reversibility != "reversible" || ocr.Mutation != "none" || ocr.Version != "1.0.0" || len(ocr.Digest) != len("sha256:")+64 {
		t.Fatalf("ocr_read: %+v — esperado egress external, reversible, mutation none, 1.0.0 e um digest sha256", ocr)
	}
	if _, ok := porNome["doc_read"]; !ok {
		t.Fatalf("o GET /tools nao serve a doc_read: %+v", corpo.Tools)
	}
}

// TestAOS530_ProducaoArrancaComOManifestoRealEOGateway: o mesmo, pela composição do entrypoint
// ([nodeConfigFromEnv] + [Bootstrap], o que [run] faz) em AOS_MODE=production, com todas as
// colunas de postura fechadas ([aos344ProducaoQuaseCompleta]), o driver gvisor e a `cap:fs.read`
// armada como no compose de produção. É o controlo de que a guarda ErrOCRSemGateway tem saída em
// produção: com o gateway composto, o nó arranca com a `ocr_read` ligada, e o catálogo que o
// `GET /tools` serve tem-na. Não se chama [run] directamente só porque o audit de governação do
// gateway (AOS_MODEL_AUDIT_PATH) não é fechado pelo Node.Close — fica aberto até ao fim do
// processo —, e o teste tem de o fechar para o directório temporário se apagar.
func TestAOS530_ProducaoArrancaComOManifestoRealEOGateway(t *testing.T) {
	aos344ProducaoQuaseCompleta(t)
	aos536AmbienteVideo(t, aos536Opcoes{comOCR: true})
	t.Setenv("AOS_MODEL_TOOLS", aos441ManifestoDeProducao)
	t.Setenv("AOS_SANDBOX_DRIVER", string(sandbox.DriverGVisor))
	t.Setenv("AOS_SANDBOX_GVISOR_URL", "")
	t.Setenv("AOS_PRIVILEGED_CAPS", "cap:http.post,cap:fs.read")
	// O que a produção exige ao gateway e o harness não põe: a credencial do upstream por ficheiro
	// (AOS-247), o egress em https (AOS-366; a composição só valida o URL, não liga) e o audit de
	// governação durável (os perfis de rota assinados em produção).
	chave, _ := aos247EscreverCredencial(t, t.TempDir())
	t.Setenv("AOS_MODEL_API_KEY_PATH", chave)
	tls := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(tls.Close)
	t.Setenv("AOS_MODEL_ENDPOINT", tls.URL)
	t.Setenv("AOS_MODEL_AUDIT_PATH", filepath.Join(t.TempDir(), "gw-audit.wal"))

	cfg, err := nodeConfigFromEnv()
	if err != nil {
		t.Fatalf("producao com o manifesto real e o gateway composto tinha de compor a config, veio: %v", err)
	}
	var sb strings.Builder
	node, err := Bootstrap(context.Background(), cfg, &sb)
	if err != nil {
		t.Fatalf("producao com o manifesto real e o gateway composto tinha de arrancar, veio: %v", err)
	}
	if node.ocr != nil {
		if c, ok := node.ocr.govAudit.(io.Closer); ok {
			t.Cleanup(func() { _ = c.Close() })
		}
	}
	t.Cleanup(func() { _ = node.Close() })
	if node.ocr == nil || !strings.Contains(sb.String(), "tool "+ocrToolName+" LIGADA") {
		t.Fatalf("o arranque de producao tinha de ligar e declarar a ocr_read:\n%s", sb.String())
	}
	if node.video == nil || !strings.Contains(sb.String(), "tool "+videoToolName+" LIGADA") {
		t.Fatalf("o arranque de producao tinha de ligar e declarar a video_read:\n%s", sb.String())
	}
	cat, err := catalogoDeToolsDoAmbiente()
	if err != nil {
		t.Fatalf("catalogoDeToolsDoAmbiente: %v", err)
	}
	nomes := make([]string, 0, len(cat))
	for _, e := range cat {
		nomes = append(nomes, e.Name)
	}
	if strings.Join(nomes, ",") != "doc_read,ocr_read,video_read" {
		t.Fatalf("o catalogo de producao tinha de ser doc_read,ocr_read,video_read; e %v", nomes)
	}
}
