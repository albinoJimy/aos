package main

// AOS-536 (parte 2) — A `video_read` NO NÓ COMPOSTO: o RM, o gateway REAL do nó, a identidade do
// run, o custo, o replay e o arranque fail-closed. O molde é o de aos530_ocr_read_no_test.go: o
// cliente de modelo sai de [parseModelFromEnv], e um upstream OpenAI-compatível em httptest serve o
// titular (`gpt-4o`), o alias da `video_read` (`video-visao`) e — quando as duas tools estão no
// registo — o da `ocr_read` (`ocr-visao`), distinguindo-os pelo `model` do corpo. Os vídeos são MP4
// sintéticos pequenos (aos536_video_read_test.go), com uma marca binária na `mdat`.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	pdp "github.com/aos-ref/control-plane/pdp"
	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/authz"
	"github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/platform/model-gateway/metering/cost"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	rt "github.com/aos-ref/platform/model-gateway/policy/routeentry/routeentrytest"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/platform/model-gateway/pricing"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/substrate/eventstore"
)

const (
	// aos536AliasVideo é o alias da `video_read`: um nome que só a allowlist externa do teste
	// autoriza.
	aos536AliasVideo    = "video-visao"
	aos536Servido       = "openrouter/google/modelo-de-video" // o expected_model do perfil de vídeo
	aos536Doc           = "clip-536.mp4"
	aos536TextoMarcador = "DESCRICAO-DO-VIDEO-MARCADOR-536-a9b0"
)

// aos536PerfilVideo é o perfil do registo de uso `tool` de vídeo para o alias da `video_read`.
var aos536PerfilVideo = strings.Replace(rt.VideoToolProfileJSON, `"requested":"gpt-4o-mini"`, `"requested":"`+aos536AliasVideo+`"`, 1)

// aos536PrecoComVideo é a tabela de preços: o titular quase de graça, o alias da `ocr_read` como no
// AOS-530 e o da `video_read` a 0,1 USD/Mtok de entrada e 0,4 USD/Mtok de saída.
const aos536PrecoComVideo = `{"version":"t-536","entries":[` +
	`{"model":"gpt-4o","region":"eu","rate":{"input_per_mtok_micro_usd":1,"output_per_mtok_micro_usd":1,"cache_read_per_mtok_micro_usd":1,"cache_write_per_mtok_micro_usd":1}},` +
	`{"model":"ocr-visao","region":"eu","rate":{"input_per_mtok_micro_usd":800000,"output_per_mtok_micro_usd":4000000,"cache_read_per_mtok_micro_usd":80000,"cache_write_per_mtok_micro_usd":1000000}},` +
	`{"model":"video-visao","region":"eu","rate":{"input_per_mtok_micro_usd":100000,"output_per_mtok_micro_usd":400000,"cache_read_per_mtok_micro_usd":10000,"cache_write_per_mtok_micro_usd":100000}}]}`

// aos536ComAllowlistExterna assina uma allowlist externa que autoriza, no board e na região dos
// testes, o titular, o alias da `ocr_read` e o da `video_read`, e põe-na no ambiente (o molde de
// [aos530ComAllowlistExterna]).
func aos536ComAllowlistExterna(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	policyJSON, err := json.Marshal(map[string]any{
		"version": "gw-allowlist/v1", "default": "deny",
		"rules": []map[string]any{{"id": "ext-aos536", "board": rt.Board, "models": []string{aos486Modelo, aos530AliasTool, aos536AliasVideo}, "regions": []string{rt.Region}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := allowlist.Digest(policyJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, allowlist.BundlePolicyFile), policyJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, allowlist.BundleSigFile), []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(digest)))), 0o600); err != nil {
		t.Fatal(err)
	}
	aos530AllowlistExterna = dir
	t.Cleanup(func() { aos530AllowlistExterna = "" })
	t.Setenv("AOS_MODEL_ALLOWLIST_BUNDLE_DIR", dir)
	t.Setenv("AOS_MODEL_ALLOWLIST_TRUST_ANCHOR", base64.StdEncoding.EncodeToString(pub))
}

// aos536Upstream é o provider: o titular segue um guião de tool calls (uma por turno) e conclui; o
// alias da `video_read` responde a descrição; o da `ocr_read`, a transcrição.
type aos536Upstream struct {
	mu     sync.Mutex
	corpos [][]byte
	guiao  [][2]string
	// fim é o finish_reason da descrição (vazio ⇒ stop); semUsage responde sem `usage`; falhas é
	// quantos pedidos do vídeo (os primeiros) recebem um 500.
	fim      string
	semUsage bool
	falhas   int
}

func (u *aos536Upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	corpo, _ := io.ReadAll(r.Body)
	var cab struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(corpo, &cab)
	u.mu.Lock()
	u.corpos = append(u.corpos, corpo)
	guiao, fim, semUsage, falha := u.guiao, u.fim, u.semUsage, false
	if u.falhas > 0 && cab.Model == aos536AliasVideo {
		u.falhas--
		falha = true
	}
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch cab.Model {
	case aos536AliasVideo:
		if falha {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"falha"}`))
			return
		}
		if fim == "" {
			fim = "stop"
		}
		w.Header().Set(port.HeaderServedModel, aos536Servido)
		resp := map[string]any{
			"id": "cmpl-video", "object": "chat.completion", "model": aos536Servido,
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": aos536TextoMarcador + "\nFACTS\nTEXT: OLA"}, "finish_reason": fim}},
			"usage":   map[string]any{"prompt_tokens": 1000, "completion_tokens": 50, "total_tokens": 1050},
		}
		if semUsage {
			delete(resp, "usage")
		}
		_ = json.NewEncoder(w).Encode(resp)
		return
	case aos530AliasTool:
		w.Header().Set(port.HeaderServedModel, aos530Servido)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "cmpl-ocr", "object": "chat.completion", "model": aos530Servido,
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": aos530TextoMarcador}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 900, "completion_tokens": 30, "total_tokens": 930},
		})
		return
	}
	feitos := strings.Count(string(corpo), `\`+`u003ctool_result`)
	if feitos < len(guiao) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "cmpl-exec", "object": "chat.completion", "model": aos486Modelo,
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "",
				"tool_calls": []any{map[string]any{"id": "call-" + guiao[feitos][0], "type": "function",
					"function": map[string]any{"name": guiao[feitos][0], "arguments": guiao[feitos][1]}}}}, "finish_reason": "tool_calls"}},
			"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
		})
		return
	}
	_, _ = w.Write([]byte(`{"id":"cmpl-fim","object":"chat.completion","model":"gpt-4o",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":"feito"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`))
}

// porModelo divide os pedidos pelo `model` do corpo.
func (u *aos536Upstream) porModelo(t *testing.T) map[string][][]byte {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	out := map[string][][]byte{}
	for _, c := range u.corpos {
		var cab struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(c, &cab); err != nil {
			t.Fatalf("pedido ilegivel: %v", err)
		}
		out[cab.Model] = append(out[cab.Model], c)
	}
	return out
}

// aos536Opcoes são as variações do nó composto.
type aos536Opcoes struct {
	directo      bool
	privilegiada bool
	semPasta     bool
	alias        string // alias do bloco `modelo` da video_read (vazio ⇒ aos536AliasVideo)
	semRegisto   bool   // a pasta de perfis sem nenhum registo de uso tool
	soImagem     bool   // a pasta de perfis com o registo de IMAGEM para o alias da video_read
	comOCR       bool   // a ocr_read também no registo de tools (com o seu registo de imagem)
	preco        string
	ambiente     map[string]string
	// video, quando preenchido, é o vídeo semeado (vazio ⇒ [aos536Video]).
	video []byte
}

// aos536AmbienteVideo põe no ambiente o gateway, a allowlist externa, a pasta de perfis, a pasta
// semeada e o registo de tools com `doc_read` e `video_read` (e a `ocr_read` com `comOCR`).
func aos536AmbienteVideo(t *testing.T, o aos536Opcoes) (*aos536Upstream, []byte) {
	t.Helper()
	up := &aos536Upstream{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	t.Setenv("AOS_MODEL_ENDPOINT", srv.URL)
	t.Setenv("AOS_MODEL_NAME", aos486Modelo)
	for _, v := range []string{"AOS_MODEL_REGION", "AOS_MODEL_BOARD", "AOS_MODEL_API_KEY_PATH", "AOS_MODEL_AUDIT_PATH",
		"AOS_MODEL_EGRESS_TIMEOUT", "AOS_MODEL_ROUTE_GOVERNANCE", "AOS_MODEL_ROUTE_API_HOST", "AOS_MODEL_PROVIDER_STATE",
		"AOS_MODEL_RESPONSE_SHAPE", "AOS_MODEL_PRICING_PATH", "AOS_MODEL_CANDIDATE"} {
		t.Setenv(v, "")
	}
	t.Setenv("AOS_MODEL_PROJECTION", "text")
	aos504SemVariavel(t, "AOS_MODEL_PROJECTION_VERSION")
	aos536ComAllowlistExterna(t)
	var tools []aos530Tool
	switch {
	case o.semRegisto:
	case o.soImagem:
		tools = append(tools, aos530Tool{rotulo: "video", perfil: strings.Replace(rt.ToolProfileJSON, `"requested":"gpt-4o-mini"`, `"requested":"`+aos536AliasVideo+`"`, 1)})
	default:
		tools = append(tools, aos530Tool{rotulo: "video", perfil: aos536PerfilVideo, video: true})
	}
	if o.comOCR {
		tools = append(tools, aos530Tool{rotulo: "ocr", perfil: aos530PerfilTool})
	}
	aos530PastaCom(t, nil, tools...)
	if o.preco != "" {
		p := filepath.Join(t.TempDir(), "pricing.json")
		if err := os.WriteFile(p, []byte(o.preco), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("AOS_MODEL_PRICING_PATH", p)
	}
	video := o.video
	if video == nil {
		video = aos536Video()
	}
	if o.semPasta {
		t.Setenv(ocrSeedDirEnv, "")
	} else {
		pasta := t.TempDir()
		aos536Escrever(t, pasta, aos536Doc, video)
		aos536Escrever(t, pasta, aos530Doc, aos530PNG(t))
		t.Setenv(ocrSeedDirEnv, pasta)
	}
	alias := o.alias
	if alias == "" {
		alias = aos536AliasVideo
	}
	registo := `[{"name":"doc_read","description":"le um documento","capability":"` + durCap + `","resource_type":"file","resource_value":"doc://{doc_id}","resource_region":"eu"},` +
		`{"name":"video_read","description":"descreve um video","parameters":{"type":"object","properties":{"doc_id":{"type":"string"}},"required":["doc_id"]},` +
		`"capability":"` + durCap + `","resource_type":"file","resource_value":"doc://{doc_id}","resource_region":"eu","egress":"external","modelo":{"alias":"` + alias + `"}}`
	if o.comOCR {
		registo += `,{"name":"ocr_read","description":"transcreve o texto de uma imagem","parameters":{"type":"object","properties":{"doc_id":{"type":"string"}},"required":["doc_id"]},` +
			`"capability":"` + durCap + `","resource_type":"file","resource_value":"doc://{doc_id}","resource_region":"eu","egress":"external","modelo":{"alias":"` + aos530AliasTool + `"}}`
	}
	t.Setenv("AOS_MODEL_TOOLS", writeTools(t, registo+`]`))
	for k, v := range o.ambiente {
		t.Setenv(k, v)
	}
	return up, video
}

// aos536No é o nó composto com a `video_read`.
type aos536No struct {
	node     *Node
	svc      *NodeService
	h        http.Handler
	upstream *aos536Upstream
	tok      string
	log      *bytes.Buffer
	video    []byte
	dir      string
}

// aos536Compor levanta o nó com a `video_read`, no molde de [aos530Compor].
func aos536Compor(t *testing.T, o aos536Opcoes) *aos536No {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	up, video := aos536AmbienteVideo(t, o)
	modelo, binder, err := parseModelFromEnv(false)
	if err != nil {
		t.Fatalf("parseModelFromEnv: %v", err)
	}
	signer := durSigner(t)
	entries := []domain.Entry{aos486Entry(signer, "doc_read"), aos486Entry(signer, videoToolName)}
	if o.comOCR {
		entries = append(entries, aos486Entry(signer, ocrToolName))
	}
	cfg := tnBaseConfig()
	cfg.DurableExecution = !o.directo
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	cfg.DSARVault = audit.NewInMemoryKeyVault(nil)
	cfg.Model = modelo
	cfg.ModelID = aos486Modelo
	cfg.ModelIdentityBinder = binder
	cfg.Catalog = catalogStub{entries: entries}
	cfg.SignedToolRegistry = nodeSignedRegistrySpec(signer, nil, entries...)
	cfg.IssuerClasses = map[string]identity.ClassPolicy{durClass: {TTL: 15 * time.Minute, Scope: []string{durCap, "model:invoke"}}}
	cfg.Policy = integration.StaticPolicy{MaxEgress: domain.EgressInternal}
	if cfg.PDP, err = pdp.Open(pdpPoliciesDir); err != nil {
		t.Fatal(err)
	}
	cfg.Authority = authz.NewStaticAuthoritySource().
		Set("human:"+tnHuman, durCap, "model:invoke").Set(durAgent, durCap, "model:invoke").Set("agent:"+durClass, durCap, "model:invoke")
	if o.privilegiada {
		cfg.Privileged = referencemonitor.NewStaticPrivilegedSet(durCap)
	}
	log := &bytes.Buffer{}
	node, err := Bootstrap(ctx, cfg, log)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	n := &aos536No{node: node, upstream: up, log: log, video: video, dir: dir}
	if err := node.Runtime.Register("doc_read", func(context.Context, []byte) ([]byte, error) { return []byte("conteudo"), nil }); err != nil {
		t.Fatal(err)
	}
	tok, err := node.Authority.MintForHuman(ctx, tnHuman, durAgent, durClass, []string{durCap, "model:invoke"})
	if err != nil {
		t.Fatal(err)
	}
	n.tok = tok.Compact
	n.svc, n.h = newAPI(t, node)
	return n
}

// correr submete um run com o guião dado e espera-o.
func (n *aos536No) correr(t *testing.T, runID string, guiao ...[2]string) []eventstore.Event {
	t.Helper()
	n.upstream.mu.Lock()
	n.upstream.guiao = guiao
	n.upstream.mu.Unlock()
	rec := postJSON(n.h, "POST", "/runs", map[string]any{
		"run_id": runID, "objective": "Descreve o video", "principal_nhi": durAgent, "credential": n.tok, "max_turns": 4,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs: %d (%s)", rec.Code, rec.Body.String())
	}
	wctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	oc, ok, err := n.svc.Wait(wctx, runID)
	if err != nil || !ok || oc.Err != nil {
		t.Fatalf("o run tinha de concluir: ok=%v err=%v run=%v", ok, err, oc.Err)
	}
	evs, err := n.node.EventStore.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// metricas devolve o `/metrics` do nó.
func (n *aos536No) metricas(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	n.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics: %d", rec.Code)
	}
	return rec.Body.String()
}

// selos devolve os selos por chamada do audit de governação do gateway para o modelo dado.
func (n *aos536No) selos(t *testing.T, modelo string) []audit.AuditRecord {
	t.Helper()
	l := n.node.video
	if l == nil || l.govAudit == nil {
		t.Fatal("a ligacao da video_read tinha de ter o audit de governacao do gateway")
	}
	recs, err := l.govAudit.Read(context.Background(), "modelgw-gov:board-eu", 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var out []audit.AuditRecord
	for _, r := range recs {
		if r.Resource.Value == modelo {
			out = append(out, r)
		}
	}
	return out
}

// aos536Chamada é a tool call do guião sobre o vídeo semeado.
var aos536Chamada = [2]string{videoToolName, `{"doc_id":"` + aos536Doc + `"}`}

// A TOOL MEDIADA E O TITULAR QUE NÃO VÊ VÍDEO — pelo caminho durável e pelo directo.
//
// O RM medeia a `video_read` e sela `doc://<id>`; o provider recebe UMA chamada ao alias, com o vídeo
// como `video_url` numa data URL `video/mp4` com os bytes exactos, a instrução, `max_tokens` 8192 e
// nenhuma tool; nenhum turno do titular leva vídeo; o resultado `aos-video/v1` chega ao titular como
// resultado de tool `untrusted`; o selo do gateway é do principal do run, do run, do passo e da tool
// de origem `video_read`; os bytes do vídeo e o texto do modelo não aparecem em claro em lado nenhum.
func TestAOS536_No_AToolMediadaPeloGatewayDoNo(t *testing.T) {
	for _, via := range []string{"duravel", "directo"} {
		t.Run(via, func(t *testing.T) {
			n := aos536Compor(t, aos536Opcoes{directo: via == "directo"})
			const runID = "run-536-video"
			evs := n.correr(t, runID, aos536Chamada)

			med := aos530Mediacoes(t, evs, referencemonitor.EventTypeMediated)
			if len(med) != 1 || med[0].ToolID != videoToolName || med[0].Capab != durCap || med[0].Resource.Value != "doc://"+aos536Doc || med[0].Principal.NHIID != durAgent {
				t.Fatalf("a video_read tinha de ser mediada uma vez, sobre doc://%s: %+v", aos536Doc, med)
			}

			pedidos := n.upstream.porModelo(t)
			titular, tool := pedidos[aos486Modelo], pedidos[aos536AliasVideo]
			if len(tool) != 1 || len(titular) != 2 || len(pedidos) != 2 {
				t.Fatalf("quer 2 turnos do titular e 1 chamada da tool; vieram %d e %d (%d modelos)", len(titular), len(tool), len(pedidos))
			}
			dataURL := `"video_url":{"url":"data:video/mp4;base64,` + base64.StdEncoding.EncodeToString(n.video) + `"}`
			if !bytes.Contains(tool[0], []byte(dataURL)) || bytes.Contains(tool[0], []byte(`"tools"`)) || bytes.Contains(tool[0], []byte("image_url")) ||
				!bytes.Contains(tool[0], []byte(`"max_tokens":8192`)) || !bytes.Contains(tool[0], []byte(`"text":"Describe this video."`)) ||
				!bytes.Contains(tool[0], []byte(`You are a video description engine.`)) {
				t.Fatalf("a chamada da tool tinha de levar o video exacto, a instrucao, max_tokens 8192 e nenhuma tool: %.400s", tool[0])
			}
			for i, c := range titular {
				if bytes.Contains(c, []byte("video_url")) || bytes.Contains(c, []byte("data:video")) || bytes.Contains(c, []byte("image_url")) {
					t.Fatalf("o turno %d do titular levou uma parte de media", i+1)
				}
			}

			prompt := aos486LerPrompt(t, titular[1])
			for _, quer := range []string{`"schema":"aos-video/v1"`, `"sha256":"` + port.VideoPart{Data: n.video}.Digest() + `"`, `"duracao_ms":2000`,
				`"modelo_servido":"` + aos536Servido + `"`, `"alias":"` + aos536AliasVideo + `"`, aos536TextoMarcador} {
				if !strings.Contains(prompt, quer) {
					t.Fatalf("o resultado da tool no prompt do titular nao tem %s:\n%s", quer, prompt)
				}
			}
			if !strings.Contains(prompt, "taint=untrusted") {
				t.Fatalf("o resultado da tool tinha de entrar marcado untrusted:\n%s", prompt)
			}

			selos := n.selos(t, aos536AliasVideo)
			if len(selos) != 1 || selos[0].Principal.NHIID != durAgent || selos[0].RunID != runID || selos[0].StepID != med[0].step || selos[0].Decision != audit.DecisionAllow {
				t.Fatalf("o selo do gateway tinha de ser do principal %q, do run %q e do passo %q: %+v", durAgent, runID, med[0].step, selos)
			}
			if origem := aos530OrigemDoSelo(selos[0]); origem != videoToolName {
				t.Fatalf("o selo da chamada tinha de levar a tool de origem %q; veio %q", videoToolName, origem)
			}
			for _, s := range n.selos(t, aos486Modelo) {
				if aos530OrigemDoSelo(s) != "" {
					t.Fatalf("um turno do titular nao leva tool de origem: %+v", s.Obligations)
				}
			}

			metricas := n.metricas(t)
			if !strings.Contains(metricas, `aos_video_read_total{resultado="descrito"} 1`) || strings.Contains(metricas, "aos_ocr_read_total") {
				t.Fatalf("a metrica tinha de contar a descricao (e sem a ocr_read, sem a familia dela):\n%s", metricas)
			}
			if !strings.Contains(n.log.String(), "tool video_read LIGADA") {
				t.Fatalf("o arranque declara a tool:\n%s", n.log.String())
			}

			b64 := base64.StdEncoding.EncodeToString(n.video)
			var sitios [][]byte
			for _, e := range evs {
				raw, _ := json.Marshal(e)
				sitios = append(sitios, raw, e.Payload)
			}
			for _, f := range []string{"events.wal", "worm.wal"} {
				raw, err := os.ReadFile(filepath.Join(n.dir, f))
				if err != nil {
					t.Fatalf("ler %s: %v", f, err)
				}
				sitios = append(sitios, raw)
			}
			for _, r := range selos {
				raw, _ := json.Marshal(r)
				sitios = append(sitios, raw)
			}
			sitios = append(sitios, n.log.Bytes(), []byte(metricas))
			for i, s := range sitios {
				if bytes.Contains(s, []byte("MARCA-BINARIA-AOS536")) || bytes.Contains(s, []byte(b64)) {
					t.Fatalf("os bytes do video aparecem num registo do no (%d): %.200s", i, s)
				}
				if bytes.Contains(s, []byte(aos536TextoMarcador)) {
					t.Fatalf("o texto do modelo aparece em claro num registo do no (%d): %.300s", i, s)
				}
			}
		})
	}
}

// aos536CustoEsperado é o custo que a tabela dá ao alias da `video_read` por estes tokens.
func aos536CustoEsperado(t *testing.T, entrada, saida int64) int64 {
	t.Helper()
	tabela, err := pricing.LoadTable([]byte(aos536PrecoComVideo))
	if err != nil {
		t.Fatal(err)
	}
	amt, err := cost.NewCalculator(tabela).Cost(cost.TokenCounts{PromptTokens: entrada, CompletionTokens: saida}, aos536AliasVideo, "eu")
	if err != nil {
		t.Fatal(err)
	}
	return amt.CostMicroUSD
}

// CUSTO NO RUN = O DO GATEWAY. O evento de custo (o mesmo da `ocr_read`, com a tool `video_read`)
// tem o valor que o gateway calculou pela tabela, os tokens e o modelo servido; o consumo durável do
// run soma-o; uma resposta cortada grava-o na mesma. O arranque declara a reserva função da duração.
func TestAOS536_No_CustoNoRun(t *testing.T) {
	for _, fim := range []string{"stop", "length"} {
		t.Run(fim, func(t *testing.T) {
			n := aos536Compor(t, aos536Opcoes{preco: aos536PrecoComVideo, ambiente: aos530Orcamento("1000000")})
			n.upstream.fim = fim
			runID := "run-536-custo-" + fim
			evs := n.correr(t, runID, aos536Chamada)
			med := aos530Mediacoes(t, evs, referencemonitor.EventTypeMediated)
			custos := aos530Custos(t, evs)
			quer := aos536CustoEsperado(t, 1000, 50)
			resultado := videoResultadoDescrito
			if fim == "length" {
				resultado = videoCausaCortado
			}
			if len(med) != 1 || len(custos) != 1 || quer == 0 {
				t.Fatalf("quer 1 mediacao e 1 evento de custo; vieram %d e %d", len(med), len(custos))
			}
			c := custos[0]
			if c.envelope != ocrCustoStepPrefix+med[0].step || c.p.Tool != videoToolName || c.p.StepID != med[0].step || c.p.CostMicroUSD != quer ||
				c.p.InputTokens != 1000 || c.p.OutputTokens != 50 || c.p.Alias != aos536AliasVideo || c.p.ModeloServido != aos536Servido || c.p.Resultado != resultado || c.p.UsageAusente {
				t.Fatalf("o evento de custo (quer %d micro-USD, %s): %+v", quer, resultado, c)
			}
			consumo, err := consumoDuravelParaOrcamento(newTurnLedgerBurndown(n.node.EventStore))(context.Background(), runID)
			if err != nil || consumo.CostMicroUSD != quer || consumo.Tokens < 1050 {
				t.Fatalf("o consumo duravel do run soma o custo da tool: %+v (%v)", consumo, err)
			}
			reservaMax := aos536CustoEsperado(t, 37_024, 8192)
			if !strings.Contains(n.log.String(), "funcao da duracao do video — 2 x (segundos x 300 + 512) tokens de entrada + 8192 de saida") ||
				!strings.Contains(n.log.String(), "para 1m0s, 45216 tokens e ") || !strings.Contains(n.log.String(), " e "+strconv.FormatInt(reservaMax, 10)+" micro-USD —") {
				t.Fatalf("o arranque declara a reserva (max %d micro-USD):\n%s", reservaMax, n.log.String())
			}
		})
	}
}

// A RESERVA É FUNÇÃO DA DURAÇÃO E NEGA ANTES DO PROVIDER. Com o tecto em dólares de 5000 micro-USD:
// o vídeo de 2 s reserva ~3500 e sai; o de 50 s reserva ~6400 e é negado com `tecto_do_run`, sem
// pedido ao provider e sem evento de custo.
func TestAOS536_No_ReservaNegadaPeloTecto(t *testing.T) {
	curta := aos536CustoEsperado(t, videoTokensDeEntrada(2*time.Second), videoMaxTokens)
	longa := aos536CustoEsperado(t, videoTokensDeEntrada(50*time.Second), videoMaxTokens)
	if curta >= 5000 || longa <= 5000 {
		t.Fatalf("pre-condicao: a reserva de 2 s (%d) cabe em 5000 e a de 50 s (%d) nao", curta, longa)
	}
	for _, c := range []struct {
		nome  string
		video []byte
		sai   bool
	}{
		{"2 s cabe", aos536Video(), true},
		{"50 s nao cabe", aos536MP4(aos536Mvhd(0, 1000, 50_000)), false},
	} {
		t.Run(c.nome, func(t *testing.T) {
			n := aos536Compor(t, aos536Opcoes{preco: aos536PrecoComVideo, ambiente: aos530Orcamento("5000"), video: c.video})
			evs := n.correr(t, "run-536-tecto", aos536Chamada)
			tool := n.upstream.porModelo(t)[aos536AliasVideo]
			if c.sai {
				if len(tool) != 1 || n.node.video.lido(videoResultadoDescrito) != 1 || len(aos530Custos(t, evs)) != 1 {
					t.Fatalf("o video curto cabia e tinha de sair: pedidos=%d", len(tool))
				}
				return
			}
			if len(tool) != 0 || n.node.video.lido(ocrCausaTecto) != 1 || len(aos530Custos(t, evs)) != 0 {
				t.Fatalf("a reserva nao cabia: pedidos=%d tecto=%d", len(tool), n.node.video.lido(ocrCausaTecto))
			}
			if !strings.Contains(n.metricas(t), `aos_video_read_total{resultado="tecto_do_run"} 1`) {
				t.Fatal("a metrica conta a causa")
			}
		})
	}
}

// UMA RESPOSTA PAGA E NÃO MEDIDA COBRA A RESERVA INTEIRA DESTA CHAMADA — a da duração do vídeo — e
// o evento leva `usage_ausente` com esses tectos. Uma chamada SEM RESPOSTA devolve a reserva.
func TestAOS536_No_NaoMedidaESemResposta(t *testing.T) {
	t.Run("nao medida", func(t *testing.T) {
		n := aos536Compor(t, aos536Opcoes{preco: aos536PrecoComVideo, ambiente: aos530Orcamento("1000000")})
		n.upstream.semUsage = true
		evs := n.correr(t, "run-536-nao-medida", aos536Chamada)
		custos := aos530Custos(t, evs)
		quer := aos536CustoEsperado(t, 2224, 8192)
		if len(custos) != 1 {
			t.Fatalf("quer 1 evento de custo: %+v", custos)
		}
		if p := custos[0].p; !p.UsageAusente || p.InputTokens != 2224 || p.OutputTokens != 8192 || p.CostMicroUSD != quer || p.Tool != videoToolName {
			t.Fatalf("o evento leva usage_ausente e a reserva desta chamada (%d micro-USD): %+v", quer, p)
		}
		consumo, err := consumoDuravelParaOrcamento(newTurnLedgerBurndown(n.node.EventStore))(context.Background(), "run-536-nao-medida")
		if err != nil || consumo.CostMicroUSD != quer {
			t.Fatalf("o consumo duravel soma a reserva cobrada: %+v (%v)", consumo, err)
		}
	})
	t.Run("sem resposta", func(t *testing.T) {
		// O tecto só comporta UMA reserva de 2 s de cada vez: a segunda só cabe se a primeira voltou.
		reserva := aos536CustoEsperado(t, 2224, 8192)
		n := aos536Compor(t, aos536Opcoes{preco: aos536PrecoComVideo, ambiente: aos530Orcamento(strconv.FormatInt(reserva+reserva/2, 10))})
		n.upstream.falhas = 1
		evs := n.correr(t, "run-536-sem-resposta", aos536Chamada, aos536Chamada)
		if n.node.video.lido(ocrCausaGateway) != 1 || n.node.video.lido(videoResultadoDescrito) != 1 || n.node.video.lido(ocrCausaTecto) != 0 {
			t.Fatalf("a primeira falha sem resposta e a segunda descreve: gateway=%d descritos=%d tecto=%d",
				n.node.video.lido(ocrCausaGateway), n.node.video.lido(videoResultadoDescrito), n.node.video.lido(ocrCausaTecto))
		}
		if c := aos530Custos(t, evs); len(c) != 1 {
			t.Fatalf("so a chamada respondida grava custo: %+v", c)
		}
	})
}

// DURAÇÃO ACIMA DO TECTO NO NÓ: a tool é mediada (o RM decidiu sobre `doc://`) e falha no efeito
// com `video_longo`, sem pedido ao provider e sem custo.
func TestAOS536_No_DuracaoAcimaDoTectoSemModelo(t *testing.T) {
	n := aos536Compor(t, aos536Opcoes{preco: aos536PrecoComVideo, ambiente: aos530Orcamento("1000000"), video: aos536MP4(aos536Mvhd(0, 1000, 61_000))})
	evs := n.correr(t, "run-536-longo", aos536Chamada)
	if len(aos530Mediacoes(t, evs, referencemonitor.EventTypeMediated)) != 1 || n.node.video.lido(videoCausaLongo) != 1 {
		t.Fatal("a tool e mediada e falha com video_longo")
	}
	if tool := n.upstream.porModelo(t)[aos536AliasVideo]; len(tool) != 0 || len(aos530Custos(t, evs)) != 0 {
		t.Fatalf("nenhum pedido e nenhum custo: %d", len(tool))
	}
}

// REPLAY SEM NOVA CHAMADA: o replay reproduz o run a partir do capturado — o resultado da
// `video_read` incluído — sem chamar a tool nem o modelo, e sem gravar eventos.
func TestAOS536_No_ReplaySemNovaChamada(t *testing.T) {
	n := aos536Compor(t, aos536Opcoes{})
	const runID = "run-536-replay"
	evs := n.correr(t, runID, aos536Chamada)
	turnos := aos486LerTurnos(t, evs)
	if len(turnos) != 2 || len(aos530Custos(t, evs)) != 1 {
		t.Fatalf("quer 2 turnos e 1 evento de custo; vieram %d e %d", len(turnos), len(aos530Custos(t, evs)))
	}
	antesPedidos, antesEventos := len(n.upstream.porModelo(t)[aos536AliasVideo]), len(evs)
	descritos := n.node.video.lido(videoResultadoDescrito)
	eng, err := replay.NewEngine(n.node.EventStore, replay.WithContentOpener(n.node.contentOpener, replay.Accessor{
		Principal: "nhi:leitor-aos536", Scopes: []string{replay.DefaultSovereignContentScope},
	}))
	if err != nil {
		t.Fatal(err)
	}
	res, err := eng.Replay(context.Background(), runID, replay.Options{Spec: replay.TrajectorySpec{
		Objective: "Descreve o video", Tools: turnos[0].specs, Model: agentruntime.ModelConfig{ModelID: aos486Modelo},
	}})
	if err != nil || res.Divergence != nil {
		t.Fatalf("o replay nao pode divergir: %+v %v", res.Divergence, err)
	}
	depois, err := n.node.EventStore.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(n.upstream.porModelo(t)[aos536AliasVideo]) != antesPedidos || len(depois) != antesEventos || n.node.video.lido(videoResultadoDescrito) != descritos {
		t.Fatal("o replay chamou o modelo, gravou eventos ou executou a tool")
	}
}

// A `ocr_read` INTACTA E UMA LEITURA POR RUN PARTILHADA. Com as duas tools no registo, cada uma
// chama o seu alias com a sua media (a `ocr_read` uma imagem e nenhum vídeo, a `video_read` um
// vídeo e nenhuma imagem), cada uma conta na sua métrica, e — com `cap:fs.read` armada — uma
// `video_read` gasta a leitura do run: a `ocr_read` a seguir é negada.
func TestAOS536_No_AOCRIntactaEALeituraPartilhada(t *testing.T) {
	t.Run("as duas tools", func(t *testing.T) {
		n := aos536Compor(t, aos536Opcoes{comOCR: true})
		n.correr(t, "run-536-ocr", [2]string{ocrToolName, `{"doc_id":"` + aos530Doc + `"}`})
		n.correr(t, "run-536-video", aos536Chamada)
		pedidos := n.upstream.porModelo(t)
		ocr, video := pedidos[aos530AliasTool], pedidos[aos536AliasVideo]
		if len(ocr) != 1 || len(video) != 1 {
			t.Fatalf("uma chamada a cada alias; vieram ocr=%d video=%d", len(ocr), len(video))
		}
		if !bytes.Contains(ocr[0], []byte("data:image/png;base64,")) || bytes.Contains(ocr[0], []byte("video_url")) || !bytes.Contains(ocr[0], []byte(`"max_tokens":4096`)) {
			t.Fatalf("a ocr_read manda a imagem e nenhum video: %.300s", ocr[0])
		}
		if !bytes.Contains(video[0], []byte("data:video/mp4;base64,")) || bytes.Contains(video[0], []byte("image_url")) {
			t.Fatalf("a video_read manda o video e nenhuma imagem: %.300s", video[0])
		}
		if n.node.ocr.lido(ocrResultadoTranscrito) != 1 || n.node.video.lido(videoResultadoDescrito) != 1 {
			t.Fatal("cada tool conta na sua metrica")
		}
		m := n.metricas(t)
		if !strings.Contains(m, `aos_ocr_read_total{resultado="transcrito"} 1`) || !strings.Contains(m, `aos_video_read_total{resultado="descrito"} 1`) {
			t.Fatalf("as duas familias:\n%s", m)
		}
		// O MESMO doc_id, a tool errada: o formato decide (sem pedido e sem custo).
		n.correr(t, "run-536-trocadas", [2]string{videoToolName, `{"doc_id":"` + aos530Doc + `"}`}, [2]string{ocrToolName, `{"doc_id":"` + aos536Doc + `"}`})
		if n.node.video.lido(ocrCausaFormato) != 1 || n.node.ocr.lido(ocrCausaFormato) != 1 || len(n.upstream.porModelo(t)[aos536AliasVideo]) != 1 {
			t.Fatal("cada tool recusa a media da outra pelo formato, sem pedido")
		}
	})
	t.Run("uma leitura por run", func(t *testing.T) {
		n := aos536Compor(t, aos536Opcoes{comOCR: true, privilegiada: true})
		evs := n.correr(t, "run-536-duas", aos536Chamada, [2]string{ocrToolName, `{"doc_id":"` + aos530Doc + `"}`})
		med := aos530Mediacoes(t, evs, referencemonitor.EventTypeMediated)
		neg := aos530Mediacoes(t, evs, referencemonitor.EventTypeDenied)
		if len(med) != 1 || med[0].ToolID != videoToolName || len(neg) != 1 || neg[0].ToolID != ocrToolName || neg[0].Context.Taint != "untrusted" {
			t.Fatalf("a video_read passa e a ocr_read a seguir e negada: med=%+v neg=%+v", med, neg)
		}
		if len(n.upstream.porModelo(t)[aos530AliasTool]) != 0 {
			t.Fatal("a ocr_read negada chamou o modelo")
		}
	})
}

// ARRANQUE FAIL-CLOSED com a `video_read` no registo de tools: sem a pasta, com o alias fora da
// allowlist, sem registo de uso tool, só com um registo de IMAGEM para o alias, e — com a
// contabilidade composta — sem preço. Com tudo, compõe-se com o gateway do nó; com um cliente de
// modelo injectado, o Bootstrap recusa.
func TestAOS536_No_ArranqueFailClosed(t *testing.T) {
	const precoSoTitular = `{"version":"t-536","entries":[{"model":"gpt-4o","region":"eu","rate":{"input_per_mtok_micro_usd":1,"output_per_mtok_micro_usd":1,"cache_read_per_mtok_micro_usd":1,"cache_write_per_mtok_micro_usd":1}}]}`
	casos := []struct {
		nome string
		o    aos536Opcoes
		erro error
	}{
		{"sem a pasta", aos536Opcoes{semPasta: true}, ErrVideoSemPasta},
		{"alias fora da allowlist", aos536Opcoes{alias: "video-nao-autorizado"}, ErrVideoAliasForaDaAllowlist},
		{"sem registo de uso tool", aos536Opcoes{semRegisto: true}, ErrVideoSemRegistoTool},
		{"so um registo de uso tool de imagem para o alias", aos536Opcoes{soImagem: true}, ErrVideoSemRegistoTool},
		{"sem preco com a contabilidade composta", aos536Opcoes{preco: precoSoTitular}, ErrVideoSemPreco},
		{"com preco", aos536Opcoes{preco: aos536PrecoComVideo}, nil},
		{"sem contabilidade composta", aos536Opcoes{}, nil},
		{"com a ocr_read tambem", aos536Opcoes{comOCR: true, preco: aos536PrecoComVideo}, nil},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			aos536AmbienteVideo(t, c.o)
			cliente, _, err := parseModelFromEnv(false)
			if c.erro != nil {
				if !errors.Is(err, c.erro) || cliente != nil {
					t.Fatalf("quer %v; veio %v", c.erro, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("tinha de compor: %v", err)
			}
			l := videoDoCliente(cliente)
			if l == nil || l.gw == nil || l.govAudit == nil || l.alias != aos536AliasVideo || l.perfilDigest != rt.ProfileDigest(t, aos536PerfilVideo) {
				t.Fatalf("a ligacao tinha de ficar composta com o gateway do no: %+v", l)
			}
			if o := ocrDoCliente(cliente); (o != nil) != c.o.comOCR || (o != nil && (o.gw != l.gw || o.alias != aos530AliasTool)) {
				t.Fatalf("a ocr_read so com ela no registo, e sobre o MESMO gateway: %+v", o)
			}
			if c.o.preco != "" && (l.tabela == nil || l.reservaMax.CostMicroUSD != aos536CustoEsperado(t, 37_024, 8192)) {
				t.Fatalf("com preco, a reserva do video mais longo: %+v", l.reservaMax)
			}
			l.fechar()
			ocrDoCliente(cliente).fechar()
		})
	}
	t.Run("cliente injectado", func(t *testing.T) {
		aos536AmbienteVideo(t, aos536Opcoes{})
		cfg := tnBaseConfig()
		dir := t.TempDir()
		cfg.EventStorePath = filepath.Join(dir, "events.wal")
		cfg.WORMPath = filepath.Join(dir, "worm.wal")
		cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
		cfg.Model = aos486ClienteSobre(&aos486Gravador{})
		node, err := Bootstrap(context.Background(), cfg, io.Discard)
		if node != nil {
			_ = node.Close()
		}
		if !errors.Is(err, ErrVideoSemGateway) {
			t.Fatalf("quer ErrVideoSemGateway; veio %v", err)
		}
	})
}

// INÉRCIA. Sem a `video_read` no registo de tools nada muda: sem ligação, sem a família no
// `/metrics`, sem a linha no arranque.
func TestAOS536_No_SemAToolNadaMuda(t *testing.T) {
	n := aos486Compor(t, nil)
	if n.node.video != nil {
		t.Fatal("sem a tool no registo nao ha ligacao")
	}
	rec := httptest.NewRecorder()
	n.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if strings.Contains(rec.Body.String(), "aos_video_read_total") || strings.Contains(n.log.String(), videoToolName) {
		t.Fatal("sem a tool nem a familia nem a linha do arranque")
	}
}
