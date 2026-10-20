package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
	pdp "github.com/aos-ref/control-plane/pdp"
	integration "github.com/aos-ref/integration"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/authz"
	"github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/platform/registry/domain"
)

// AOS-539 — a CABLAGEM DE PRODUÇÃO no nó composto. A revisão do AOS-538 mostrou que a ligação do
// destino das tools ao anotador de risco em `integration/secured.go` podia sair sem que nenhuma
// suite avermelhasse. Aqui levanta-se o nó REAL (Bootstrap + API, execução durável, PDP com o
// oráculo de autonomia, a classe da NHI em L4 no domínio fs — o nível de produção) e corre-se um
// run: com a tool `counter` declarada com os eixos da `doc_read` de PRODUÇÃO, e com a `ocr_read`
// REAL (bloco `modelo`, gateway do nó, harness do AOS-530). Mede-se o que o nó SELOU: a classe de
// risco da mediação e se correu ou escalou.
//
// Sem o destino na cadeia (a mutação «secured.go sem destinos»), a `ocr_read` sem sensibilidade
// deixa de dar `danger` (fica `gray`, egress derivado da capability) e a `doc_read` deixa de dar
// `safe` (fica `gray`, vazio lido sensível): os dois casos avermelham.

// aos539Mediacao é o que o Event Store do nó selou sobre a mediação da call do turno 1.
type aos539Mediacao struct {
	tipo, deniedBy, classe string
	execs                  int64
	// simulada é a classe que a simulação de autonomia do nó (`reclassificar` com o destino que o
	// Node guarda) dá ao selo WORM da mesma call. Vazia sem selo da tool.
	simulada string
}

// aos539ManifestoDe escreve um AOS_MODEL_TOOLS com a tool `counter` declarada com os eixos de risco
// (`egress`, `mutation`, `reversibility`, `resource_sensitivity`) da entrada `nome` do manifesto de
// produção. `semSensibilidade` apaga a `resource_sensitivity` — o recurso que ninguém classificou.
func aos539ManifestoDe(t *testing.T, nome string, semSensibilidade bool) {
	t.Helper()
	raw, err := os.ReadFile(toolsJSONProducao)
	if err != nil {
		t.Fatal(err)
	}
	var prod []map[string]any
	if err := json.Unmarshal(raw, &prod); err != nil {
		t.Fatal(err)
	}
	var e map[string]any
	for _, p := range prod {
		if p["name"] == nome {
			e = p
		}
	}
	if e == nil {
		t.Fatalf("o manifesto de producao ja nao tem %q", nome)
	}
	if e["capability"] != durCap {
		t.Fatalf("%s mudou de capability (%v): o teste usa a do nó de teste (%s)", nome, e["capability"], durCap)
	}
	spec := map[string]any{
		"name": "counter", "capability": durCap,
		"resource_type": e["resource_type"], "resource_value": "doc://notes", "resource_region": "eu",
	}
	for _, k := range []string{"egress", "mutation", "reversibility", "resource_sensitivity"} {
		if v, ok := e[k]; ok && !(semSensibilidade && k == "resource_sensitivity") {
			spec[k] = v
		}
	}
	out, err := json.Marshal([]map[string]any{spec})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "tools.json")
	if err := os.WriteFile(p, out, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AOS_MODEL_TOOLS", p)
}

// aos539Node compõe o nó com o oráculo de autonomia e a classe da NHI em `nivel` no domínio fs, e
// corre um run de um só nó (sem inputs: o contexto do turno 1 é trusted).
func aos539Node(t *testing.T, nivel autonomy.Level) aos539Mediacao {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()

	signer := durSigner(t)
	entry := counterEntry(t, signer)

	cfg := tnBaseConfig()
	cfg.DurableExecution = true
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	cfg.DSARVault = audit.NewInMemoryKeyVault(nil)
	// O decorador REAL do cliente de modelo (o que o main compõe com AOS_MODEL_TOOLS): preenche a
	// capability, o recurso e a reversibilidade da call a partir do binding do manifesto.
	_, bindings, err := loadModelToolsFromEnv()
	if err != nil {
		t.Fatalf("loadModelToolsFromEnv: %v", err)
	}
	cfg.Model = &toolEnrichingClient{inner: &fase1Model{}, bindings: bindings}
	cfg.Catalog = catalogStub{entries: []domain.Entry{entry}}
	cfg.SignedToolRegistry = nodeSignedRegistrySpec(signer, nil, entry)
	cfg.IssuerClasses = map[string]identity.ClassPolicy{
		durClass: {TTL: 15 * time.Minute, Scope: []string{durCap}},
	}
	cfg.Policy = integration.StaticPolicy{MaxEgress: domain.EgressInternal}
	// O oráculo de autonomia como o compõe [loadPolicyBundleFromEnv]: o registo ligado ao PDP. O
	// nível sobe a seguir ao Bootstrap (a subida a L4 pelo ambiente exige provas assinadas,
	// AOS-377); o efeito na decisão é o mesmo.
	cfg.Autonomy = buildAutonomyOracle([]autonomyLevelSpec{{agent: autonomy.ClassPrefix + durClass, domain: "fs", level: autonomy.L1}}, autonomy.L0)
	cfg.PDP, err = pdp.Open(pdpPoliciesDir, pdp.WithAutonomyOracle(cfg.Autonomy.oracle()))
	if err != nil {
		t.Fatalf("abrir bundle de politica de referencia: %v", err)
	}
	cfg.Authority = authz.NewStaticAuthoritySource().
		Set("human:"+tnHuman, durCap).
		Set(durAgent, durCap).
		Set("agent:"+durClass, durCap)

	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	if _, err := node.Autonomy.registry.SetLevel(ctx, autonomy.ClassPrefix+durClass, "fs", nivel, "AOS-539: nivel de producao", "op:teste"); err != nil {
		t.Fatalf("SetLevel: %v", err)
	}

	var execs int64
	if err := node.Runtime.Register("counter", func(_ context.Context, _ []byte) ([]byte, error) {
		atomic.AddInt64(&execs, 1)
		return []byte("conteudo"), nil
	}); err != nil {
		t.Fatalf("Register(counter): %v", err)
	}
	tok, err := node.Authority.MintForHuman(ctx, tnHuman, durAgent, durClass, []string{durCap})
	if err != nil {
		t.Fatalf("MintForHuman: %v", err)
	}

	svc, h := newAPI(t, node)
	const runID = "plan-aos539~n1"
	rec := postJSON(h, "POST", "/runs", map[string]any{
		"run_id":        runID,
		"objective":     "Le o documento notes e resume",
		"principal_nhi": durAgent,
		"credential":    tok.Compact,
		"tools":         []string{"counter"},
		"inputs":        []map[string]string{},
		"max_turns":     3,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, _, _ = svc.Wait(wctx, runID)

	events, err := node.EventStore.Read(ctx, runID, 0)
	if err != nil {
		t.Fatalf("ler o stream do run: %v", err)
	}
	out := aos539Mediacao{execs: atomic.LoadInt64(&execs)}
	for _, e := range events {
		if e.Type != referencemonitor.EventTypeDenied && e.Type != referencemonitor.EventTypeMediated && e.Type != referencemonitor.EventTypeEscalated {
			continue
		}
		var p struct {
			DeniedBy string `json:"denied_by"`
			Context  struct {
				RiskClass string `json:"risk_class"`
			} `json:"context"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("payload de mediacao ilegivel (%s): %v", e.Type, err)
		}
		out.tipo, out.deniedBy, out.classe = e.Type, p.DeniedBy, p.Context.RiskClass
		break
	}
	if out.tipo == "" {
		t.Fatalf("nenhuma mediacao selada no stream do run %q (%d eventos)", runID, len(events))
	}
	for _, r := range lerMediacoes(ctx, node.WORM, 1000) {
		if r.ToolID == "counter" && r.Capability == durCap { // só a mediação, não a revalidação
			out.simulada = reclassificar(r, node.toolDestinations).String()
		}
	}
	return out
}

// TestAOS539_NoComposto_ClasseDeRiscoEEscaladaEmL4 — o critério de produção no nó composto, em L4:
// a `doc_read` de produção sela `safe` e corre; sem a sensibilidade declarada sela `gray` (o vazio é
// sensível) e corre. A `ocr_read` REAL (bloco `modelo`, gateway do nó) é exercida em
// [TestAOS539_NoComposto_OCRRealEmL4].
func TestAOS539_NoComposto_ClasseDeRiscoEEscaladaEmL4(t *testing.T) {
	casos := []struct {
		nome             string
		semSensibilidade bool
		classe           string
	}{
		{"doc_read", false, "safe"},
		{"doc_read-sem-sensibilidade", true, "gray"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			aos539ManifestoDe(t, "doc_read", c.semSensibilidade)
			m := aos539Node(t, autonomy.L4)
			if m.classe != c.classe {
				t.Fatalf("classe selada %q, queria %q (evento %s, denied_by %q)", m.classe, c.classe, m.tipo, m.deniedBy)
			}
			// A simulação de autonomia reclassifica o selo WORM com o MESMO destino que o nó usou.
			if m.simulada != m.classe {
				t.Fatalf("a simulacao de autonomia reclassifica %q e o no selou %q", m.simulada, m.classe)
			}
			if m.tipo != referencemonitor.EventTypeMediated || m.execs != 1 {
				t.Fatalf("em L4 a call %s tinha de correr sem humano: evento=%s denied_by=%q execs=%d", c.classe, m.tipo, m.deniedBy, m.execs)
			}
		})
	}
}

// aos539ManifestoOCR é o registo de tools com a `ocr_read` na forma de PRODUÇÃO (egress externo,
// reversível, sem mutação, bloco `modelo`), com ou sem a `resource_sensitivity`, para o harness do
// AOS-530 (o alias e a região dele).
func aos539ManifestoOCR(t *testing.T, sensibilidade string) string {
	t.Helper()
	sens := ""
	if sensibilidade != "" {
		sens = `"resource_sensitivity":"` + sensibilidade + `",`
	}
	return writeTools(t, `[`+
		`{"name":"doc_read","description":"le um documento","capability":"`+durCap+`","resource_type":"file","resource_value":"doc://{doc_id}","resource_region":"eu"},`+
		`{"name":"ocr_read","description":"transcreve o texto de uma imagem","parameters":{"type":"object","properties":{"doc_id":{"type":"string"}},"required":["doc_id"]},`+
		`"capability":"`+durCap+`","resource_type":"file","resource_value":"doc://{doc_id}","resource_region":"eu",`+
		`"reversibility":"reversible","mutation":"none",`+sens+`"egress":"external","modelo":{"alias":"`+aos530AliasTool+`"}}]`)
}

// TestAOS539_NoComposto_OCRRealEmL4 — a `ocr_read` REAL no nó composto (gateway do nó, harness do
// AOS-530), com o oráculo de autonomia e a classe em L4: com a pasta declarada pública sela `gray`
// e corre sem humano (a chamada ao modelo de visão acontece); sem a sensibilidade declarada sela
// `danger` e ESCALA — não corre e o modelo de visão não é chamado.
func TestAOS539_NoComposto_OCRRealEmL4(t *testing.T) {
	for _, c := range []struct {
		nome, sens, classe string
		escala             bool
	}{
		{"public", "public", "gray", false},
		{"sem-sensibilidade", "", "danger", true},
	} {
		t.Run(c.nome, func(t *testing.T) {
			n := aos530Compor(t, aos530Opcoes{autonomiaL4: true, ambiente: map[string]string{"AOS_MODEL_TOOLS": aos539ManifestoOCR(t, c.sens)}})
			n.upstream.mu.Lock()
			n.upstream.guiao = [][2]string{{ocrToolName, `{"doc_id":"` + aos530Doc + `"}`}}
			n.upstream.mu.Unlock()
			const runID = "run-539-ocr"
			rec := postJSON(n.h, "POST", "/runs", map[string]any{
				"run_id": runID, "objective": "Le a fatura e diz o total", "principal_nhi": durAgent, "credential": n.tok, "max_turns": 4,
			})
			if rec.Code != http.StatusCreated {
				t.Fatalf("POST /runs: %d (%s)", rec.Code, rec.Body.String())
			}
			wctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_, _, _ = n.svc.Wait(wctx, runID)
			evs, err := n.node.EventStore.Read(context.Background(), runID, 1)
			if err != nil {
				t.Fatal(err)
			}
			var tipo, classe string
			for _, e := range evs {
				if e.Type != referencemonitor.EventTypeDenied && e.Type != referencemonitor.EventTypeMediated && e.Type != referencemonitor.EventTypeEscalated {
					continue
				}
				var p struct {
					ToolID  string `json:"tool_id"`
					Context struct {
						RiskClass string `json:"risk_class"`
					} `json:"context"`
				}
				if err := json.Unmarshal(e.Payload, &p); err != nil {
					t.Fatal(err)
				}
				if p.ToolID == ocrToolName {
					tipo, classe = e.Type, p.Context.RiskClass
					break
				}
			}
			if classe != c.classe {
				t.Fatalf("a ocr_read selou a classe %q (evento %q), queria %q", classe, tipo, c.classe)
			}
			_, chamadas := n.upstream.separar(t)
			if c.escala {
				if tipo != referencemonitor.EventTypeEscalated || len(chamadas) != 0 {
					t.Fatalf("em L4 a ocr_read sem sensibilidade tinha de ESCALAR sem chamar o modelo de visao: evento=%s chamadas=%d", tipo, len(chamadas))
				}
				return
			}
			if tipo != referencemonitor.EventTypeMediated || len(chamadas) != 1 {
				t.Fatalf("em L4 a ocr_read de producao tinha de correr sem humano: evento=%s chamadas ao modelo de visao=%d", tipo, len(chamadas))
			}
			simulou := false
			for _, r := range lerMediacoes(context.Background(), n.node.WORM, 1000) {
				// Só o selo da MEDIAÇÃO (o WORM guarda também a revalidação do registo, com outra
				// capability).
				if r.ToolID == ocrToolName && r.Capability == durCap {
					simulou = true
					if got := reclassificar(r, n.node.toolDestinations).String(); got != classe {
						t.Fatalf("a simulacao de autonomia reclassifica %q e o no selou %q", got, classe)
					}
				}
			}
			if !simulou {
				t.Fatal("nenhum selo WORM da mediacao da ocr_read para a simulacao reclassificar")
			}
		})
	}
}
