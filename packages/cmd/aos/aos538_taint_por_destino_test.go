package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/governance/autonomy"
	rm "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/kernel/reference-monitor/taint"
	"github.com/aos-ref/platform/audit"
)

// AOS-538 — o critério de produção, sem mexer no servidor: com o manifesto REAL de produção
// (deploy/server/model-tools/tools.json) e o AOS_PRIVILEGED_CAPS do `.env` de produção
// (cap:http.post,cap:fs.read), composto pelas MESMAS funções que o Bootstrap usa
// ([toolDestinationsFromEnv] + [privilegedComDestinos]), o gate de taint deixa passar a doc_read
// com o contexto untrusted e continua a negar a ocr_read e a video_read (egress externo: a leitura
// única por run mantém-se). O web_post vem do manifesto dos demos (dev-hardened) e continua negado.

const aos538CapsDoEnvDeProducao = "cap:http.post,cap:fs.read"

// aos538GateDoManifesto compõe o classificador do gate como o Bootstrap o compõe, para o manifesto
// dado, e devolve-o com as specs lidas.
func aos538GateDoManifesto(t *testing.T, manifesto string) (rm.PrivilegedAuthorizer, []modelToolSpec) {
	t.Helper()
	t.Setenv("AOS_MODEL_TOOLS", manifesto)
	specs, err := readModelToolSpecs()
	if err != nil || len(specs) == 0 {
		t.Fatalf("manifesto %s: %v (%d specs)", manifesto, err, len(specs))
	}
	dests, err := toolDestinationsFromEnv()
	if err != nil {
		t.Fatalf("toolDestinationsFromEnv: %v", err)
	}
	return privilegedComDestinos(rm.NewStaticPrivilegedSet(splitCSV(aos538CapsDoEnvDeProducao)...), dests), specs
}

// aos538MediarUntrusted medeia, com o contexto untrusted, uma call de cada tool do manifesto
// (capability e recurso do binding) e devolve o veredicto por tool.
func aos538MediarUntrusted(t *testing.T, priv rm.PrivilegedAuthorizer, specs []modelToolSpec) map[string]rm.Decision {
	t.Helper()
	m := rm.New(rm.WithHooks(rm.DefaultHooksWithTaint(priv)...))
	out := map[string]rm.Decision{}
	for _, s := range specs {
		if err := m.Register(s.Name, func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
			t.Fatalf("Register(%s): %v", s.Name, err)
		}
		dec, err := m.Mediate(context.Background(), rm.Call{
			RunID: "run-538-prod", StepID: "s-" + s.Name, ToolID: s.Name, Capability: s.Capability,
			Resource:  rm.Resource{Type: s.ResourceType, Value: "doc://x", Region: s.ResourceRegion},
			Principal: rm.Principal{NHIID: "nhi-538"},
			Context:   rm.CallContext{Taint: taint.StringUntrusted, Reversibility: s.Reversibility},
		})
		if err != nil {
			t.Fatalf("Mediate(%s): %v", s.Name, err)
		}
		out[s.Name] = dec
	}
	return out
}

func TestAOS538_ManifestoDeProducao_DocReadPassaOCRVideoNegadas(t *testing.T) {
	priv, specs := aos538GateDoManifesto(t, toolsJSONProducao)
	got := aos538MediarUntrusted(t, priv, specs)
	for _, quer := range []struct {
		tool  string
		passa bool
	}{{"doc_read", true}, {"ocr_read", false}, {"video_read", false}} {
		dec, ok := got[quer.tool]
		if !ok {
			t.Fatalf("o manifesto de producao ja nao oferece %q — o criterio ficaria vacuo", quer.tool)
		}
		if quer.passa && dec.Effect != rm.EffectPermit {
			t.Errorf("%s com o contexto untrusted devia PASSAR (leitura local), veio %s/%q", quer.tool, dec.Effect, dec.DeniedBy)
		}
		if !quer.passa && (dec.Effect != rm.EffectDeny || dec.DeniedBy != "taint") {
			t.Errorf("%s com o contexto untrusted devia ser NEGADA pelo taint, veio %s/%q", quer.tool, dec.Effect, dec.DeniedBy)
		}
	}
	// O banner nomeia exactamente as tools livres por leitura local.
	linha := strings.Join(taintGatePostureBanner(true, destinosDoClassificador(priv)), "\n")
	if !strings.Contains(linha, "livres por leitura local: doc_read.") || !strings.Contains(linha, "AOS-538") {
		t.Errorf("o banner nao diz a regra nova nem as tools livres: %q", linha)
	}
	if strings.Contains(linha, "um no que ja leu um documento nao le outro") {
		t.Errorf("o banner ainda afirma a regra antiga: %q", linha)
	}
}

func TestAOS538_ManifestoDosDemos_WebPostContinuaNegado(t *testing.T) {
	priv, specs := aos538GateDoManifesto(t, toolsJSONComWebPost)
	got := aos538MediarUntrusted(t, priv, specs)
	if dec := got["web_post"]; dec.Effect != rm.EffectDeny || dec.DeniedBy != "taint" {
		t.Fatalf("web_post com o contexto untrusted devia ser NEGADO pelo taint, veio %s/%q", dec.Effect, dec.DeniedBy)
	}
	// A doc_read dos demos NÃO declara `reversibility` — um eixo implícito —, pelo que fica com a
	// regra da capability e continua negada. É o fail-closed da regra a funcionar sobre um
	// manifesto real: só os três eixos explícitos libertam.
	if dec := got["doc_read"]; dec.Effect != rm.EffectDeny || dec.DeniedBy != "taint" {
		t.Fatalf("doc_read dos demos (eixo implicito) devia continuar NEGADA pelo taint, veio %s/%q", dec.Effect, dec.DeniedBy)
	}
}

// Sem AOS_PRIVILEGED_CAPS o classificador não é embrulhado (o gate fica desarmado, nada muda), e
// sem tools declaradas fica o de antes.
func TestAOS538_PrivilegedComDestinosSoEmbrulhaGateArmado(t *testing.T) {
	t.Setenv("AOS_MODEL_TOOLS", toolsJSONProducao)
	dests, err := toolDestinationsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if got := privilegedComDestinos(nil, dests); got != nil {
		t.Fatalf("sem AOS_PRIVILEGED_CAPS o gate tinha de ficar nil, veio %#v", got)
	}
	base := rm.NewStaticPrivilegedSet("cap:fs.read")
	if _, ok := privilegedComDestinos(base, rm.NewToolDestinations(nil)).(rm.StaticPrivilegedSet); !ok {
		t.Fatal("sem tools declaradas o classificador tinha de ser o de antes")
	}
	if _, ok := privilegedComDestinos(base, dests).(rm.DestinationPrivileged); !ok {
		t.Fatal("com o gate armado e tools declaradas o classificador tinha de levar o destino")
	}
	if destinosDoClassificador(base) != nil {
		t.Fatal("o banner do classificador de antes nao pode afirmar a regra pelo destino")
	}
}

// Um egress ilegível aborta também a leitura dos destinos, mesmo sem AOS_MODEL_ENDPOINT — não se
// classifica uma tool por um eixo adivinhado.
func TestAOS538_EgressIlegivelAbortaOsDestinos(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tools.json")
	if err := os.WriteFile(p, []byte(`[{"name":"x","capability":"cap:fs.read","resource_type":"file","resource_value":"doc://x","egress":"lá-fora","mutation":"none","reversibility":"reversible"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AOS_MODEL_TOOLS", p)
	if _, err := toolDestinationsFromEnv(); !errors.Is(err, ErrBadModelTools) {
		t.Fatalf("egress ilegivel tinha de abortar com ErrBadModelTools, veio %v", err)
	}
}

// O egress declarado chega ao classificador de risco do nó (AOS-538) — e o que isso MUDA em
// produção, escrito aqui para não ser descoberto por acaso. Com o manifesto real e a sensibilidade
// que o loop entrega (vazia ⇒ sensível, fail-closed), a `ocr_read` e a `video_read` passam de
// `gray` a `danger` (egress externo de dados sensíveis); a `doc_read` fica `gray`. Em L4 (o nível
// de produção no domínio fs) `danger` CONFIRMA: as duas passam a exigir aprovação humana. A
// simulação de autonomia reclassifica igual ao nó.
func TestAOS538_ManifestoDeProducao_EgressDeclaradoNoRisco(t *testing.T) {
	t.Setenv("AOS_MODEL_TOOLS", toolsJSONProducao)
	specs, err := readModelToolSpecs()
	if err != nil {
		t.Fatal(err)
	}
	dests, err := toolDestinationsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	quer := map[string]struct {
		antes, depois risk.Class
		escalaEmL4    bool
	}{
		"doc_read":   {risk.ClassGray, risk.ClassGray, false},
		"ocr_read":   {risk.ClassGray, risk.ClassDanger, true},
		"video_read": {risk.ClassGray, risk.ClassDanger, true},
	}
	for _, s := range specs {
		q, ok := quer[s.Name]
		if !ok {
			t.Fatalf("tool %q nova no manifesto de producao: decide aqui o que muda para ela", s.Name)
		}
		rec := audit.AuditRecord{ToolID: s.Name, Capability: s.Capability, Resource: audit.Resource{Type: s.ResourceType, Value: "doc://x"}}
		rec.Context.Taint = taint.StringTrusted
		rec.Context.Reversibility = s.Reversibility
		if got := reclassificar(rec, rm.ToolDestinations{}); got != q.antes {
			t.Errorf("%s sem o destino: %v, queria %v", s.Name, got, q.antes)
		}
		depois := reclassificar(rec, dests)
		if depois != q.depois {
			t.Errorf("%s com o destino: %v, queria %v", s.Name, depois, q.depois)
		}
		if got := autonomy.Oversight(autonomy.L4, depois).RequiresHumanGate(); got != q.escalaEmL4 {
			t.Errorf("%s em L4: exige humano=%v, queria %v", s.Name, got, q.escalaEmL4)
		}
	}
}
