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
// run cujo modelo pede a tool `counter` declarada no AOS_MODEL_TOOLS com os eixos de uma entrada do
// manifesto de PRODUÇÃO. Mede-se o que o nó SELOU: a classe de risco da mediação e se escalou.
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
		if r.ToolID == "counter" {
			out.simulada = reclassificar(r, node.toolDestinations).String()
		}
	}
	return out
}

// TestAOS539_NoComposto_ClasseDeRiscoEEscaladaEmL4 — o critério de produção no nó composto: em L4,
// a `ocr_read` e a `video_read` (egress externo, pasta declarada pública) selam `gray` e correm
// sem humano; a `doc_read` sela `safe`; um recurso sem sensibilidade declarada com egress externo
// sela `danger` e ESCALA em vez de correr.
func TestAOS539_NoComposto_ClasseDeRiscoEEscaladaEmL4(t *testing.T) {
	casos := []struct {
		nome, tool       string
		semSensibilidade bool
		classe           string
		escala           bool
	}{
		{"ocr_read", "ocr_read", false, "gray", false},
		{"video_read", "video_read", false, "gray", false},
		{"doc_read", "doc_read", false, "safe", false},
		{"ocr_read-sem-sensibilidade", "ocr_read", true, "danger", true},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			aos539ManifestoDe(t, c.tool, c.semSensibilidade)
			m := aos539Node(t, autonomy.L4)
			if m.classe != c.classe {
				t.Fatalf("classe selada %q, queria %q (evento %s, denied_by %q)", m.classe, c.classe, m.tipo, m.deniedBy)
			}
			// A simulação de autonomia reclassifica o selo WORM com o MESMO destino que o nó usou.
			if m.simulada != m.classe {
				t.Fatalf("a simulacao de autonomia reclassifica %q e o no selou %q", m.simulada, m.classe)
			}
			if c.escala {
				if m.tipo == referencemonitor.EventTypeMediated || m.execs != 0 {
					t.Fatalf("em L4 um recurso sem sensibilidade com egress externo tinha de ESCALAR, nao correr: evento=%s execs=%d", m.tipo, m.execs)
				}
				if m.tipo != referencemonitor.EventTypeEscalated && m.deniedBy != "policy" {
					t.Fatalf("a call danger em L4 devia ter sido escalada pela politica, veio evento=%s denied_by=%q", m.tipo, m.deniedBy)
				}
				return
			}
			if m.tipo != referencemonitor.EventTypeMediated || m.execs != 1 {
				t.Fatalf("em L4 a call %s tinha de correr sem humano: evento=%s denied_by=%q execs=%d", c.classe, m.tipo, m.deniedBy, m.execs)
			}
		})
	}
}
