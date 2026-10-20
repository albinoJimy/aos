package main

// AOS-530 (peça 3a) — A `ocr_read` NO NÓ COMPOSTO: o RM, o gateway REAL do nó, a identidade do
// run, o replay e o arranque fail-closed.
//
// O MOLDE é o do AOS-486 (aos486_oferta_no_composto_test.go): o cliente de modelo sai de
// [parseModelFromEnv], como no arranque do nó, e fala com um upstream OpenAI-compatível em
// httptest que grava o corpo de cada pedido. O mesmo upstream serve os turnos do executor (o
// titular `gpt-4o`) e a chamada da tool (o alias `ocr-visao`), e distingue-os pelo `model` do
// corpo. As imagens são pequenas (um PNG de 8x8): o upstream é o httptest e não o falso de wire
// do gateway, pelo que o corte de 1 MiB do `internal/wirefake/validador.go` não se aplica.
//
// A IMAGEM. As imagens da bateria do AOS-529 desenham-se em memória pelo banco de ensaio
// (`packages/qa/banco-ensaio/ocr_imagens.go`), que é um módulo-folha que o nó não importa; o
// repositório não guarda binários. A imagem destes testes é um PNG gerado aqui, com uma marca
// binária no fim que se procura em tudo o que o nó escreve.

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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	autonomy "github.com/aos-ref/control-plane/governance/autonomy"
	pdp "github.com/aos-ref/control-plane/pdp"
	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/authz"
	"github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	rt "github.com/aos-ref/platform/model-gateway/policy/routeentry/routeentrytest"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// O texto que o modelo de visão falso devolve: um marcador único, que se procura em claro em tudo
// o que o nó escreve fora do conteúdo cifrado por titular.
const (
	aos530TextoMarcador = "TEXTO-TRANSCRITO-MARCADOR-530-c4e1"
	aos530Servido       = "openrouter/anthropic/modelo-de-visao" // o expected_model do perfil tool
	aos530Doc           = "fatura-530.png"
)

// aos530Upstream é o provider: o titular segue um guião de tool calls (uma por turno, pela ordem,
// contadas pelos resultados de tool que o prompt já tem) e conclui; o alias da tool responde a
// transcrição.
type aos530Upstream struct {
	mu     sync.Mutex
	corpos [][]byte
	// guiao são as tool calls do titular: nome e argumentos JSON.
	guiao [][2]string
	// ocrServido é o modelo que o proxy declara ter servido à chamada da tool.
	ocrServido string
	// ocrFim é o finish_reason da transcrição (vazio ⇒ stop).
	ocrFim string
	// ocrEntrada, quando > 0, substitui os prompt_tokens da transcrição (900).
	ocrEntrada int64
	// titularServido, quando preenchido, é o modelo que o proxy declara ter servido aos turnos do
	// titular (a governação da rota em `enforce` exige-o).
	titularServido string
	// apiBase, quando preenchido, é o endpoint que o proxy declara em todas as respostas.
	apiBase string
	// ocrSemUsage faz a transcrição responder 200 sem `usage`; ocrCorpo, quando preenchido, é o
	// corpo cru da resposta 200 da transcrição; ocrFalhas é quantos pedidos da tool (os primeiros)
	// recebem um 500 sem resposta.
	ocrSemUsage bool
	ocrCorpo    string
	ocrFalhas   int
}

func (u *aos530Upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	corpo, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.corpos = append(u.corpos, corpo)
	guiao, servido, fim, entrada, titularServido := u.guiao, u.ocrServido, u.ocrFim, u.ocrEntrada, u.titularServido
	if u.apiBase != "" {
		w.Header().Set("x-litellm-model-api-base", u.apiBase)
	}
	semUsage, corpoCru, falha := u.ocrSemUsage, u.ocrCorpo, false
	if u.ocrFalhas > 0 && strings.Contains(string(corpo), `"model":"`+aos530AliasTool+`"`) {
		u.ocrFalhas--
		falha = true
	}
	u.mu.Unlock()
	var cab struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(corpo, &cab)
	w.Header().Set("Content-Type", "application/json")
	if cab.Model == aos530AliasTool {
		if servido == "" {
			servido = aos530Servido
		}
		if fim == "" {
			fim = "stop"
		}
		if entrada == 0 {
			entrada = 900
		}
		if falha {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"falha"}`))
			return
		}
		w.Header().Set(port.HeaderServedModel, servido)
		if corpoCru != "" {
			_, _ = w.Write([]byte(corpoCru))
			return
		}
		r := map[string]any{
			"id": "cmpl-ocr", "object": "chat.completion", "model": servido,
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": aos530TextoMarcador + "\nTotal: 12,34 EUR"}, "finish_reason": fim}},
			"usage":   map[string]any{"prompt_tokens": entrada, "completion_tokens": 30, "total_tokens": entrada + 30},
		}
		if semUsage {
			delete(r, "usage")
		}
		_ = json.NewEncoder(w).Encode(r)
		return
	}
	if titularServido != "" {
		w.Header().Set(port.HeaderServedModel, titularServido)
	}
	// O delimitador do segmento na forma do wire (o encoding/json escapa `<`), escrito por partes.
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

func (u *aos530Upstream) pedidos() [][]byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([][]byte(nil), u.corpos...)
}

// separar divide os pedidos em os do titular e os da tool.
func (u *aos530Upstream) separar(t *testing.T) (titular, tool [][]byte) {
	t.Helper()
	for _, c := range u.pedidos() {
		var cab struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(c, &cab); err != nil {
			t.Fatalf("pedido ilegivel: %v", err)
		}
		if cab.Model == aos530AliasTool {
			tool = append(tool, c)
		} else {
			titular = append(titular, c)
		}
	}
	return titular, tool
}

// aos530Opcoes são as variações do nó composto.
type aos530Opcoes struct {
	directo      bool // execução NÃO durável (o caminho directo do despacho)
	privilegiada bool // cap:fs.read armada (AOS_PRIVILEGED_CAPS): uma leitura por run
	semPasta     bool
	alias        string // alias do bloco `modelo` (vazio ⇒ o do registo tool)
	semRegisto   bool   // a pasta de perfis sem o registo de uso tool
	// registoDeVideo põe, para o alias da tool, um registo de uso tool de VÍDEO (AOS-536) em vez do
	// de imagem.
	registoDeVideo bool
	preco          string // conteúdo de AOS_MODEL_PRICING_PATH (vazio ⇒ sem a variável)
	// ambiente são variáveis a mais (o orçamento, a governação da rota), postas depois das outras.
	ambiente map[string]string
	// exportador recebe os spans do nó (cfg.OTLPExporter).
	exportador otelgenai.Exporter
	// nats, quando preenchido, é o cluster JetStream onde o nó guarda o Event Store (em vez do WAL),
	// com o stream natsStream.
	nats, natsStream string
	// soberano compõe o gate soberano de leitura (board→região) e o four-eyes (que compõe o registo
	// de retoma): é o que o `GET /runs/{id}/replay` do AOS-533 exige.
	soberano bool
	// autonomiaL4 liga o oráculo de autonomia ao PDP e põe a classe da NHI em L4 no domínio fs — o
	// nível de produção (AOS-539).
	autonomiaL4 bool
}

// aos530No é o nó composto com a `ocr_read`.
type aos530No struct {
	node     *Node
	svc      *NodeService
	h        http.Handler
	upstream *aos530Upstream
	tok      string
	log      *bytes.Buffer
	imagem   []byte
	docReads *int64
	dir      string
}

// aos530AmbienteOCR põe no ambiente o gateway (como [aos486ComporCom]), a allowlist externa com o
// alias, a pasta de perfis com o registo de uso tool, a pasta semeada com a imagem e o registo de
// tools com `doc_read` e `ocr_read`. Devolve o upstream e a imagem.
func aos530AmbienteOCR(t *testing.T, o aos530Opcoes) (*aos530Upstream, []byte) {
	t.Helper()
	up := &aos530Upstream{}
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
	aos530ComAllowlistExterna(t)
	switch {
	case o.semRegisto:
		aos530PastaCom(t, nil)
	case o.registoDeVideo:
		aos530PastaCom(t, nil, aos530Tool{rotulo: "ocr", perfil: aos536PerfilVideoTool, video: true})
	default:
		aos530PastaCom(t, nil, aos530Tool{rotulo: "ocr", perfil: aos530PerfilTool})
	}
	if o.preco != "" {
		p := filepath.Join(t.TempDir(), "pricing.json")
		if err := os.WriteFile(p, []byte(o.preco), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("AOS_MODEL_PRICING_PATH", p)
	}
	imagem := aos530PNG(t)
	if o.semPasta {
		t.Setenv(ocrSeedDirEnv, "")
	} else {
		pasta := t.TempDir()
		if err := os.WriteFile(filepath.Join(pasta, aos530Doc), imagem, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(ocrSeedDirEnv, pasta)
	}
	alias := o.alias
	if alias == "" {
		alias = aos530AliasTool
	}
	t.Setenv("AOS_MODEL_TOOLS", writeTools(t, `[`+
		`{"name":"doc_read","description":"le um documento","capability":"`+durCap+`","resource_type":"file","resource_value":"doc://{doc_id}","resource_region":"eu"},`+
		`{"name":"ocr_read","description":"transcreve o texto de uma imagem","parameters":{"type":"object","properties":{"doc_id":{"type":"string"}},"required":["doc_id"]},`+
		`"capability":"`+durCap+`","resource_type":"file","resource_value":"doc://{doc_id}","resource_region":"eu","egress":"external","modelo":{"alias":"`+alias+`"}}]`))
	for k, v := range o.ambiente {
		t.Setenv(k, v)
	}
	return up, imagem
}

// aos530Compor levanta o nó com a `ocr_read`, no molde de [aos486ComporCom].
func aos530Compor(t *testing.T, o aos530Opcoes) *aos530No {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	up, imagem := aos530AmbienteOCR(t, o)
	modelo, binder, err := parseModelFromEnv(false)
	if err != nil {
		t.Fatalf("parseModelFromEnv: %v", err)
	}
	signer := durSigner(t)
	entries := []domain.Entry{aos486Entry(signer, "doc_read"), aos486Entry(signer, ocrToolName)}
	cfg := tnBaseConfig()
	cfg.DurableExecution = !o.directo
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	if o.nats != "" {
		cfg.EventStorePath = ""
		cfg.EventStoreNATS = o.nats
		cfg.EventStoreNATSStream = o.natsStream
		cfg.EventStoreNATSReplicas = 3
		cfg.EventStoreNATSNKeyFile = os.Getenv("AOS_NATS_NKEY_FILE")
	}
	cfg.OTLPExporter = o.exportador
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
	var pdpOpts []pdp.Option
	if o.autonomiaL4 {
		cfg.Autonomy = buildAutonomyOracle([]autonomyLevelSpec{{agent: autonomy.ClassPrefix + durClass, domain: "fs", level: autonomy.L1}}, autonomy.L0)
		pdpOpts = append(pdpOpts, pdp.WithAutonomyOracle(cfg.Autonomy.oracle()))
	}
	if cfg.PDP, err = pdp.Open(pdpPoliciesDir, pdpOpts...); err != nil {
		t.Fatal(err)
	}
	cfg.Authority = authz.NewStaticAuthoritySource().
		Set("human:"+tnHuman, durCap, "model:invoke").Set(durAgent, durCap, "model:invoke").Set("agent:"+durClass, durCap, "model:invoke")
	if o.privilegiada {
		cfg.Privileged = referencemonitor.NewStaticPrivilegedSet(durCap)
	}
	if o.soberano {
		cfg.BoardRegions = map[string]string{govBoard: govRegion}
		cfg.Approvers = aos533Aprovadores(t)
	}
	log := &bytes.Buffer{}
	node, err := Bootstrap(ctx, cfg, log)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	if o.autonomiaL4 {
		// A subida a L4 pelo ambiente exige provas assinadas (AOS-377); o efeito na decisão é o mesmo.
		if _, err := node.Autonomy.registry.SetLevel(ctx, autonomy.ClassPrefix+durClass, "fs", autonomy.L4, "AOS-539: nivel de producao", "op:teste"); err != nil {
			t.Fatal(err)
		}
	}
	n := &aos530No{node: node, upstream: up, log: log, imagem: imagem, docReads: new(int64), dir: dir}
	if err := node.Runtime.Register("doc_read", func(context.Context, []byte) ([]byte, error) {
		atomic.AddInt64(n.docReads, 1)
		return []byte("conteudo"), nil
	}); err != nil {
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

// correr submete um run e espera-o.
func (n *aos530No) correr(t *testing.T, runID string, guiao ...[2]string) []eventstore.Event {
	t.Helper()
	n.upstream.mu.Lock()
	n.upstream.guiao = guiao
	n.upstream.mu.Unlock()
	rec := postJSON(n.h, "POST", "/runs", map[string]any{
		"run_id": runID, "objective": "Le a fatura e diz o total", "principal_nhi": durAgent, "credential": n.tok, "max_turns": 4,
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

// aos530Mediacoes devolve os payloads dos eventos de mediação do tipo dado, com o step do evento.
type aos530Mediacao struct {
	step     string
	ToolID   string `json:"tool_id"`
	Code     string `json:"code"`
	DeniedBy string `json:"denied_by"`
	Capab    string `json:"capability"`
	Resource struct {
		Type, Value string
	} `json:"resource"`
	Context struct {
		Taint string `json:"taint"`
	} `json:"context"`
	Principal struct {
		NHIID string `json:"nhi_id"`
	} `json:"principal"`
}

func aos530Mediacoes(t *testing.T, evs []eventstore.Event, tipo string) []aos530Mediacao {
	t.Helper()
	var out []aos530Mediacao
	for _, e := range evs {
		if e.Type != tipo {
			continue
		}
		var m aos530Mediacao
		if err := json.Unmarshal(e.Payload, &m); err != nil {
			t.Fatalf("payload de %s: %v", tipo, err)
		}
		// O passo da CALL: o envelope de `denied`/`escalated` leva a chave do tipo desde o
		// AOS-481, e o passo da call vem no payload; o do `mediated` é o próprio envelope.
		var passo struct {
			StepID string `json:"step_id"`
		}
		_ = json.Unmarshal(e.Payload, &passo)
		m.step = e.StepID
		if passo.StepID != "" {
			m.step = passo.StepID
		}
		out = append(out, m)
	}
	return out
}

// aos530SelosDoGateway devolve os selos por chamada do audit de governação do gateway do nó para o
// alias dado (a partição do board).
func aos530SelosDoGateway(t *testing.T, n *aos530No, modelo string) []audit.AuditRecord {
	t.Helper()
	l := n.node.ocr
	if l == nil || l.govAudit == nil {
		t.Fatal("a ligacao da ocr_read tinha de ter o audit de governacao do gateway")
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

// A TOOL MEDIADA E O TITULAR QUE NÃO VÊ IMAGENS — pelo caminho durável e pelo directo.
//
// Um run pede `ocr_read` sobre a imagem: o RM medeia-a e o evento sela `doc://<id>`; o provider
// recebe UMA chamada ao alias, com a imagem como `image_url` numa data URL com os bytes exactos e
// sem tools; nenhum turno do titular leva uma parte de imagem; o resultado `aos-ocr/v1` chega ao
// turno seguinte do titular como resultado de tool, com o digest certo e o modelo servido; o selo
// do gateway da chamada é do principal do token do run, do run e do passo da tool call; nem os
// bytes da imagem nem o seu base64 aparecem em eventos, no log, no `/metrics` ou nos selos; e o
// texto do modelo não aparece em claro em evento nenhum.
func TestAOS530_No_AToolMediadaPeloGatewayDoNo(t *testing.T) {
	for _, via := range []string{"duravel", "directo"} {
		t.Run(via, func(t *testing.T) {
			n := aos530Compor(t, aos530Opcoes{directo: via == "directo"})
			const runID = "run-530-ocr"
			evs := n.correr(t, runID, [2]string{ocrToolName, `{"doc_id":"` + aos530Doc + `"}`})

			// O RM mediou a call e selou o recurso efectivo.
			med := aos530Mediacoes(t, evs, referencemonitor.EventTypeMediated)
			if len(med) != 1 || med[0].ToolID != ocrToolName || med[0].Capab != durCap || med[0].Resource.Value != "doc://"+aos530Doc || med[0].Principal.NHIID != durAgent {
				t.Fatalf("a ocr_read tinha de ser mediada uma vez, sobre doc://%s: %+v", aos530Doc, med)
			}

			// O provider: UMA chamada ao alias, com a imagem exacta e sem tools; o titular sem imagens.
			titular, tool := n.upstream.separar(t)
			if len(tool) != 1 || len(titular) != 2 {
				t.Fatalf("quer 2 turnos do titular e 1 chamada da tool; vieram %d e %d", len(titular), len(tool))
			}
			dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(n.imagem)
			if !bytes.Contains(tool[0], []byte(dataURL)) || bytes.Contains(tool[0], []byte(`"tools"`)) || !bytes.Contains(tool[0], []byte(`"max_tokens":4096`)) {
				t.Fatalf("a chamada da tool tinha de levar a imagem exacta, max_tokens 4096 e nenhuma tool: %.300s", tool[0])
			}
			for i, c := range titular {
				if bytes.Contains(c, []byte("image_url")) || bytes.Contains(c, []byte("data:image")) {
					t.Fatalf("o turno %d do titular levou uma parte de imagem", i+1)
				}
			}

			// O resultado chega ao turno seguinte do titular, com o digest certo e o modelo servido.
			prompt := aos486LerPrompt(t, titular[1])
			digest := port.ImagePart{Data: n.imagem}.Digest()
			for _, quer := range []string{`"schema":"aos-ocr/v1"`, `"sha256":"` + digest + `"`, `"modelo_servido":"` + aos530Servido + `"`, `"motor":"modelo"`, aos530TextoMarcador} {
				if !strings.Contains(prompt, quer) {
					t.Fatalf("o resultado da tool no prompt do titular nao tem %s:\n%s", quer, prompt)
				}
			}
			if !strings.Contains(prompt, "taint=untrusted") {
				t.Fatalf("o resultado da tool tinha de entrar marcado untrusted:\n%s", prompt)
			}

			// O selo do gateway da chamada da tool: o principal do token do run, o run e o passo.
			selos := aos530SelosDoGateway(t, n, aos530AliasTool)
			if len(selos) != 1 || selos[0].Principal.NHIID != durAgent || selos[0].RunID != runID || selos[0].StepID != med[0].step || selos[0].Decision != audit.DecisionAllow {
				t.Fatalf("o selo do gateway tinha de ser do principal %q, do run %q e do passo %q: %+v", durAgent, runID, med[0].step, selos)
			}
			// E a tool de origem, numa obligation própria; os selos dos turnos do titular não a têm.
			if origem := aos530OrigemDoSelo(selos[0]); origem != ocrToolName {
				t.Fatalf("o selo da chamada da tool tinha de levar a tool de origem %q; veio %q (%+v)", ocrToolName, origem, selos[0].Obligations)
			}
			turnos := aos530SelosDoGateway(t, n, aos486Modelo)
			if len(turnos) != 2 {
				t.Fatalf("quer 2 selos de turnos do titular; vieram %d", len(turnos))
			}
			for _, s := range turnos {
				if s.RunID != runID || len(s.Obligations) != 1 || aos530OrigemDoSelo(s) != "" {
					t.Fatalf("o selo de um turno do titular nao leva tool de origem: %+v", s.Obligations)
				}
			}

			// A métrica.
			metricas := aos530Metricas(t, n)
			if !strings.Contains(metricas, `aos_ocr_read_total{resultado="transcrito"} 1`) {
				t.Fatalf("a metrica tinha de contar a transcricao:\n%s", metricas)
			}

			// NENHUM BINÁRIO, NENHUM TEXTO EM CLARO: os bytes marcados da imagem e o seu base64 não
			// aparecem em evento nenhum do log do nó, no log de arranque, no /metrics nem nos selos do
			// gateway; o texto do modelo não aparece em claro em evento nenhum (o step-ledger e a
			// captura levam-no cifrado por titular).
			b64 := base64.StdEncoding.EncodeToString(n.imagem)
			var sitios [][]byte
			for _, e := range evs {
				raw, _ := json.Marshal(e)
				sitios = append(sitios, raw, e.Payload)
			}
			// Os ficheiros inteiros do log do nó e do WORM: todos os streams, não só o do run.
			for _, f := range []string{"events.wal", "worm.wal"} {
				raw, err := os.ReadFile(filepath.Join(n.dir, f))
				if err != nil {
					t.Fatalf("ler %s: %v", f, err)
				}
				sitios = append(sitios, raw)
			}
			for _, r := range aos530SelosDoGateway(t, n, aos530AliasTool) {
				raw, _ := json.Marshal(r)
				sitios = append(sitios, raw)
			}
			sitios = append(sitios, n.log.Bytes(), []byte(metricas))
			for i, s := range sitios {
				if bytes.Contains(s, []byte(aos530MarcaBinaria)) || bytes.Contains(s, []byte(b64)) || bytes.Contains(s, []byte("MARCA-BINARIA-AOS530")) {
					t.Fatalf("os bytes da imagem aparecem num registo do no (%d): %.200s", i, s)
				}
				if bytes.Contains(s, []byte(aos530TextoMarcador)) {
					t.Fatalf("o texto do modelo aparece em claro num registo do no (%d): %.300s", i, s)
				}
			}
		})
	}
}

// aos530Metricas devolve o texto do `/metrics` do nó.
func aos530Metricas(t *testing.T, n *aos530No) string {
	t.Helper()
	rec := httptest.NewRecorder()
	n.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics: %d", rec.Code)
	}
	return rec.Body.String()
}

// REPLAY SEM NOVA CHAMADA. O replay do run reproduz todos os `prompt_hash` a partir do que ficou
// capturado — o resultado da `ocr_read` incluído — sem chamar a tool nem o modelo: o provider conta
// zero pedidos novos e o log do run não ganha eventos (nenhum custo, nenhuma mediação).
func TestAOS530_No_ReplaySemNovaChamada(t *testing.T) {
	n := aos530Compor(t, aos530Opcoes{})
	const runID = "run-530-replay"
	evs := n.correr(t, runID, [2]string{ocrToolName, `{"doc_id":"` + aos530Doc + `"}`})
	turnos := aos486LerTurnos(t, evs)
	if len(turnos) != 2 {
		t.Fatalf("quer 2 turnos gravados; vieram %d", len(turnos))
	}
	antesPedidos, antesEventos := len(n.upstream.pedidos()), len(evs)
	transcritos := n.node.ocr.lido(ocrResultadoTranscrito)
	if c := aos530Custos(t, evs); len(c) != 1 {
		t.Fatalf("o run original grava 1 evento de custo da ocr_read; vieram %d", len(c))
	}

	eng, err := replay.NewEngine(n.node.EventStore, replay.WithContentOpener(n.node.contentOpener, replay.Accessor{
		Principal: "nhi:leitor-aos530", Scopes: []string{replay.DefaultSovereignContentScope},
	}))
	if err != nil {
		t.Fatal(err)
	}
	res, err := eng.Replay(context.Background(), runID, replay.Options{Spec: replay.TrajectorySpec{
		Objective: "Le a fatura e diz o total", Tools: turnos[0].specs, Model: agentruntime.ModelConfig{ModelID: aos486Modelo},
	}})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if res.Divergence != nil {
		t.Fatalf("o replay nao pode divergir: %+v", res.Divergence)
	}
	depois, err := n.node.EventStore.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(n.upstream.pedidos()); got != antesPedidos {
		t.Fatalf("o replay chamou o provider: %d pedidos novos", got-antesPedidos)
	}
	if len(depois) != antesEventos {
		t.Fatalf("o replay gravou %d eventos no run", len(depois)-antesEventos)
	}
	if c := aos530Custos(t, depois); len(c) != 1 {
		t.Fatalf("o replay nao grava custo: o run continua com 1 evento de custo; vieram %d", len(c))
	}
	if n.node.ocr.lido(ocrResultadoTranscrito) != transcritos {
		t.Fatal("o replay executou a tool")
	}
}

// UMA LEITURA POR RUN. Com `cap:fs.read` armada (AOS_PRIVILEGED_CAPS), a primeira leitura — a
// `ocr_read`, com o contexto só com o objectivo — passa; o resultado dela é `untrusted` e torna o
// contexto do run `untrusted`; a segunda leitura é negada, venha da `doc_read` ou de outra
// `ocr_read`.
func TestAOS530_No_SegundaLeituraNegada(t *testing.T) {
	for _, segunda := range []string{"doc_read", ocrToolName} {
		t.Run(segunda, func(t *testing.T) {
			n := aos530Compor(t, aos530Opcoes{privilegiada: true})
			evs := n.correr(t, "run-530-duas-"+segunda,
				[2]string{ocrToolName, `{"doc_id":"` + aos530Doc + `"}`},
				[2]string{segunda, `{"doc_id":"` + aos530Doc + `"}`})
			med := aos530Mediacoes(t, evs, referencemonitor.EventTypeMediated)
			neg := aos530Mediacoes(t, evs, referencemonitor.EventTypeDenied)
			if len(med) != 1 || med[0].ToolID != ocrToolName || med[0].Context.Taint != "trusted" {
				t.Fatalf("a primeira leitura (ocr_read, contexto trusted) tinha de passar: %+v", med)
			}
			if len(neg) != 1 || neg[0].ToolID != segunda || neg[0].Context.Taint != "untrusted" {
				t.Fatalf("a segunda leitura tinha de ser negada com o contexto untrusted: %+v", neg)
			}
			if _, tool := n.upstream.separar(t); len(tool) != 1 {
				t.Fatalf("so a primeira leitura chama o modelo de visao; vieram %d", len(tool))
			}
			if atomic.LoadInt64(n.docReads) != 0 {
				t.Fatal("a doc_read negada executou")
			}
		})
	}
}

// UM ALIAS QUE O ESTÁGIO DA ALLOWLIST NEGA NÃO CHEGA AO PROVIDER. O arranque já o recusa (ver
// abaixo); aqui troca-se o alias da ligação já composta, para provar que a barreira por pedido do
// gateway também lá está: a tool falha com `erro_do_gateway`, o provider conta zero chamadas e a
// recusa fica selada.
func TestAOS530_No_AliasNegadoPelaAllowlistNaoSai(t *testing.T) {
	n := aos530Compor(t, aos530Opcoes{})
	n.node.ocr.alias = "alias-fora-da-allowlist"
	n.correr(t, "run-530-negado", [2]string{ocrToolName, `{"doc_id":"` + aos530Doc + `"}`})
	if _, tool := n.upstream.separar(t); len(tool) != 0 {
		t.Fatalf("o pedido a um alias negado saiu: %d", len(tool))
	}
	if n.node.ocr.lido(ocrCausaGateway) != 1 {
		t.Fatal("a tool tinha de falhar com erro_do_gateway")
	}
	selos := aos530SelosDoGateway(t, n, "alias-fora-da-allowlist")
	if len(selos) != 1 || selos[0].Decision != audit.DecisionDeny {
		t.Fatalf("a recusa da allowlist tinha de ficar selada: %+v", selos)
	}
}

// ARRANQUE FAIL-CLOSED com a `ocr_read` no registo de tools: sem a pasta, com o alias fora da
// allowlist em vigor, sem o registo de uso tool, e — com a contabilidade de custo composta — sem
// preço para o par. Com tudo, compõe-se; e com um cliente de modelo injectado (sem gateway por
// ambiente) o Bootstrap recusa.
func TestAOS530_No_ArranqueFailClosed(t *testing.T) {
	const (
		precoSoTitular = `{"version":"t-530","entries":[{"model":"gpt-4o","region":"eu","rate":{"input_per_mtok_micro_usd":1,"output_per_mtok_micro_usd":1,"cache_read_per_mtok_micro_usd":1,"cache_write_per_mtok_micro_usd":1}}]}`
		precoComTool   = `{"version":"t-530","entries":[{"model":"gpt-4o","region":"eu","rate":{"input_per_mtok_micro_usd":1,"output_per_mtok_micro_usd":1,"cache_read_per_mtok_micro_usd":1,"cache_write_per_mtok_micro_usd":1}},{"model":"ocr-visao","region":"eu","rate":{"input_per_mtok_micro_usd":800000,"output_per_mtok_micro_usd":4000000,"cache_read_per_mtok_micro_usd":80000,"cache_write_per_mtok_micro_usd":1000000}}]}`
	)
	casos := []struct {
		nome string
		o    aos530Opcoes
		erro error
	}{
		{"sem a pasta", aos530Opcoes{semPasta: true}, ErrOCRSemPasta},
		{"alias fora da allowlist", aos530Opcoes{alias: "ocr-nao-autorizado"}, ErrOCRAliasForaDaAllowlist},
		{"sem registo de uso tool", aos530Opcoes{semRegisto: true}, ErrOCRSemRegistoTool},
		{"so um registo de uso tool de video para o alias", aos530Opcoes{registoDeVideo: true}, ErrOCRSemRegistoTool},
		{"sem preco com a contabilidade composta", aos530Opcoes{preco: precoSoTitular}, ErrOCRSemPreco},
		{"com preco", aos530Opcoes{preco: precoComTool}, nil},
		{"sem contabilidade composta", aos530Opcoes{}, nil},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			aos530AmbienteOCR(t, c.o)
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
			l := ocrDoCliente(cliente)
			if l == nil || l.gw == nil || l.govAudit == nil || l.alias != aos530AliasTool || l.perfilDigest != rt.ProfileDigest(t, aos530PerfilTool) {
				t.Fatalf("a ligacao tinha de ficar composta com o gateway do no: %+v", l)
			}
			l.fechar()
		})
	}
	t.Run("cliente injectado", func(t *testing.T) {
		aos530AmbienteOCR(t, aos530Opcoes{})
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
		if !errors.Is(err, ErrOCRSemGateway) {
			t.Fatalf("quer ErrOCRSemGateway; veio %v", err)
		}
	})
}

// INÉRCIA. Sem a `ocr_read` no registo de tools nada muda: o nó não compõe a ligação, não regista a
// tool, e o `/metrics` não tem a família. (O pedido ao titular byte a byte é o dos goldens do
// AOS-486/490, que correm sem a tool e ficam verdes sem alteração.)
func TestAOS530_No_SemAToolNadaMuda(t *testing.T) {
	n := aos486Compor(t, nil)
	if n.node.ocr != nil {
		t.Fatal("sem a tool no registo nao ha ligacao")
	}
	rec := httptest.NewRecorder()
	n.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if strings.Contains(rec.Body.String(), "aos_ocr_read_total") {
		t.Fatal("sem a tool a familia nao existe")
	}
	// O arranque: sem a linha da tool (nem a postura do custo dela). O log não é vazio — o resto do
	// banner do nó está lá —, para que a ausência meça alguma coisa.
	arranque := n.log.String()
	if !strings.Contains(arranque, "[aos]") {
		t.Fatalf("o arranque tinha de ter escrito o banner do no: %s", arranque)
	}
	for _, linha := range []string{"tool " + ocrToolName + " LIGADA", "CUSTO: ", EventTypeToolModelCost, ocrToolName} {
		if strings.Contains(arranque, linha) {
			t.Fatalf("sem a tool o arranque nao fala dela (%q): %s", linha, arranque)
		}
	}
}

// IDENTIDADE DEPOIS DE UMA RETOMA. A credencial do run não é persistida: a retoma de um run
// suspenso (aprovação humana, AOS-021) re-hospeda-o com um token FRESCO, e é desse token que a
// `ocr_read` tem de ler o principal. O run pede a `ocr_read`, o oráculo de autonomia (L4) escala-a,
// nada sai para o provider; a cerimónia four-eyes aprova-a; a retoma corre-a — e a chamada ao
// gateway passa o estágio authn com o token da retoma, fica selada com o principal dele, o run e o
// passo da tool call.
func TestAOS530_No_IdentidadeDepoisDaRetoma(t *testing.T) {
	ctx := context.Background()
	up, _ := aos530AmbienteOCR(t, aos530Opcoes{})
	cliente, binder, err := parseModelFromEnv(false)
	if err != nil {
		t.Fatalf("parseModelFromEnv: %v", err)
	}
	assinados := perfisDoCliente(cliente)
	if assinados == nil || assinados.ocr == nil {
		t.Fatal("a ligacao da ocr_read tinha de ficar composta")
	}

	// O titular é um modelo de teste (o que se mede aqui é a tool): pede a `ocr_read` no turno 1
	// e conclui depois. Leva os perfis assinados do cliente do ambiente — é por eles que o
	// Bootstrap encontra a ligação da tool.
	titular := &acnModel{guiao: func(v agentruntime.PromptView) agentruntime.ModelResponse {
		if v.Turn == 1 {
			return agentruntime.ModelResponse{ToolCalls: []agentruntime.ToolInvocation{{
				ToolID: ocrToolName, Capability: acnCap, Input: []byte(`{"doc_id":"` + aos530Doc + `"}`),
				ResourceType: "file", ResourceValue: "doc://" + aos530Doc, ResourceRegion: "eu",
			}}}
		}
		return agentruntime.ModelResponse{Text: "concluido", Final: true}
	}}

	levels := autonomy.NewLevelRegistry()
	if _, err := levels.SetLevel(ctx, acnAgent, "fs", autonomy.L4, "teste da retoma da ocr_read", "operador"); err != nil {
		t.Fatal(err)
	}
	policyDP, err := pdp.Open(pdpPoliciesDir, pdp.WithAutonomyOracle(levels))
	if err != nil {
		t.Fatal(err)
	}
	pubA, privA, _ := ed25519.GenerateKey(rand.Reader)
	pubB, privB, _ := ed25519.GenerateKey(rand.Reader)
	signer := acnSigner(t)
	dir := t.TempDir()
	cfg := tnBaseConfig()
	cfg.DurableExecution = true
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	cfg.Model = clienteComPerfis{inner: titular, perfis: assinados}
	cfg.ModelIdentityBinder = binder
	cfg.Catalog = catalogStub{entries: []domain.Entry{acnEntry(t, signer, ocrToolName), acnEntry(t, signer, "doc_read")}}
	cfg.SignedToolRegistry = nodeSignedRegistrySpec(signer, nil)
	cfg.IssuerClasses = map[string]identity.ClassPolicy{acnClass: {TTL: 15 * time.Minute, Scope: []string{acnCap, "model:invoke"}}}
	cfg.Policy = integration.StaticPolicy{MaxEgress: domain.EgressInternal}
	cfg.PDP = policyDP
	cfg.Authority = authz.NewStaticAuthoritySource().
		Set("human:"+tnHuman, acnCap, "model:invoke").Set(acnAgent, acnCap, "model:invoke").Set("agent:"+acnClass, acnCap, "model:invoke")
	cfg.Approvers = []ApproverConfig{
		{Principal: "human:alice", PubKey: pubA, Authority: []string{"approve:danger", "approve:gray"}},
		{Principal: "human:bob", PubKey: pubB, Authority: []string{"approve:danger", "approve:gray"}},
	}
	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	svc, err := NewNodeService(node)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	h := &acnHarness{node: node, svc: svc, model: titular, keys: [2]ed25519.PrivateKey{privA, privB}, execs: map[string]*int64{}}
	token := func() string {
		tok, err := node.Authority.MintForHuman(ctx, tnHuman, acnAgent, acnClass, []string{acnCap, "model:invoke"})
		if err != nil {
			t.Fatal(err)
		}
		return tok.Compact
	}

	if err := svc.Submit(ctx, agentruntime.Goal{RunID: acnRunID, Principal: referencemonitor.Principal{NHIID: acnAgent},
		Credential: token(), Objective: "le a fatura", MaxTurns: 4}); err != nil {
		t.Fatal(err)
	}
	h.esperaFim(t, acnRunID)
	if _, susp := svc.Suspended(ctx, acnRunID); !susp {
		t.Fatal("a ocr_read tinha de escalar e o run ficar suspenso")
	}
	if _, tool := up.separar(t); len(tool) != 0 {
		t.Fatal("uma accao escalada nao chama o modelo de visao")
	}
	if p := h.aprovaPendente(t, "req-530-ocr"); p.ToolID != ocrToolName {
		t.Fatalf("o pendente tinha de ser a ocr_read: %q", p.ToolID)
	}
	if err := svc.Resume(ctx, acnRunID, token()); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	h.esperaFim(t, acnRunID)
	if fim, done := svc.Outcome(acnRunID); !done || !fim.Result.Terminated {
		t.Fatalf("o run retomado tinha de terminar: %+v", fim)
	}

	_, tool := up.separar(t)
	if len(tool) != 1 {
		t.Fatalf("depois da retoma a ocr_read chama o modelo de visao uma vez; vieram %d", len(tool))
	}
	evs, err := node.EventStore.Read(ctx, acnRunID, 1)
	if err != nil {
		t.Fatal(err)
	}
	// A tool call escalada é a que a retoma despachou: o passo é o do evento da escalada, e o
	// step-ledger desse passo tem o resultado aplicado.
	med := aos530Mediacoes(t, evs, referencemonitor.EventTypeEscalated)
	if len(med) != 1 || med[0].ToolID != ocrToolName || med[0].Resource.Value != "doc://"+aos530Doc {
		t.Fatalf("a ocr_read tinha de ter escalado uma vez, sobre doc://%s: %+v", aos530Doc, med)
	}
	aplicado := false
	for _, e := range evs {
		aplicado = aplicado || (e.Type == "step.ledger.applied" && strings.Contains(string(e.Payload), `"key":"`+acnRunID+":"+med[0].step+`","status":"ok"`))
	}
	if !aplicado {
		t.Fatal("o step-ledger da tool call tinha de ter o resultado aplicado depois da retoma")
	}
	recs, err := assinados.ocr.govAudit.Read(ctx, "modelgw-gov:board-eu", 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var selos []audit.AuditRecord
	for _, r := range recs {
		if r.Resource.Value == aos530AliasTool {
			selos = append(selos, r)
		}
	}
	if len(selos) != 1 || selos[0].Principal.NHIID != acnAgent || selos[0].RunID != acnRunID || selos[0].StepID != med[0].step || selos[0].Decision != audit.DecisionAllow {
		t.Fatalf("o selo da chamada depois da retoma tinha de ser do principal %q, do run e do passo %q: %+v", acnAgent, med[0].step, selos)
	}
	if aos530OrigemDoSelo(selos[0]) != ocrToolName {
		t.Fatalf("o selo da chamada depois da retoma tinha de levar a tool de origem: %+v", selos[0].Obligations)
	}
}

// aos530OrigemDoSelo devolve a tool de origem selada num registo do audit de governacao, ou "".
func aos530OrigemDoSelo(r audit.AuditRecord) string {
	for _, o := range r.Obligations {
		if o.Type == allowlist.ObligationOrigin {
			return o.Params["tool"]
		}
	}
	return ""
}
