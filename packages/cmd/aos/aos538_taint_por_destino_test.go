package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	rm "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/taint"
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
	}{{"doc_read", true}, {"media_view", true}, {"ocr_read", false}, {"video_read", false}} {
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
	if !strings.Contains(linha, "livres por leitura local: doc_read,media_view.") || !strings.Contains(linha, "AOS-538") {
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
	// A doc_read dos demos PASSA, e isto mudou no AOS-556: ela declarava `egress` e `mutation` mas
	// NÃO a `reversibility`, e com um eixo implícito ficava com a regra da capability — negada. O
	// AOS-556 declarou-a, porque com a `allow_fs_read` partida por classe de destino um eixo
	// implícito dá `unknown` e deixa de satisfazer regra nenhuma: a leitura ficava negada pela
	// POLÍTICA, não só pelo taint. Declarar os três eixos passou a ser pré-condição da permissão,
	// e os demos passaram a comportar-se como produção, que já os declarava.
	//
	// O fail-closed do eixo implícito continua coberto, e com sujeito: ver
	// `TestAOS538_MutacaoImplicitaNaoLibertaNada` neste ficheiro, o caso `eixo-implicito` de
	// `aos069_fase1_no_composto_test.go`, e os de `aos530`/`aos536`.
	if dec := got["doc_read"]; dec.Effect != rm.EffectPermit {
		t.Fatalf("doc_read dos demos declara os tres eixos desde o AOS-556 e devia PASSAR, veio %s/%q", dec.Effect, dec.DeniedBy)
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

// M-3: o loader guarda os eixos TAL COMO DECLARADOS. Uma tool com `mutation` omitida não é leitura
// local, mesmo com `egress: "none"` e `reversibility: "reversible"` explícitos: o gate armado com
// cap:fs.read nega-a com o contexto untrusted, e o banner não a lista como livre. Se o loader
// guardasse o valor resolvido, a ausência valeria "none" e a tool ganharia a excepção.
func TestAOS538_LoaderMutacaoImplicitaNaoELeituraLocal(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tools.json")
	if err := os.WriteFile(p, []byte(`[{"name":"le_sem_mutacao","capability":"cap:fs.read","resource_type":"file","resource_value":"doc://{doc_id}","resource_region":"eu","egress":"none","reversibility":"reversible"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	priv, specs := aos538GateDoManifesto(t, p)
	if d := destinosDoClassificador(priv); d == nil || len(d.livres) != 0 {
		t.Fatalf("uma tool com mutation implicita nao pode ser listada como livre: %+v", d)
	}
	got := aos538MediarUntrusted(t, priv, specs)
	if dec := got["le_sem_mutacao"]; dec.Effect != rm.EffectDeny || dec.DeniedBy != "taint" {
		t.Fatalf("mutation implicita: devia ser NEGADA pelo taint, veio %s/%q", dec.Effect, dec.DeniedBy)
	}
}
