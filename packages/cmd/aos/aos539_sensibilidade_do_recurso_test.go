package main

import (
	"encoding/json"
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

// AOS-539 — o que MUDA em produção, escrito aqui para não ser descoberto por acaso. Com o manifesto
// REAL (deploy/server/model-tools/tools.json), o destino das tools lido pela MESMA função que o
// Bootstrap usa, e o contexto trusted com a sensibilidade que o loop entrega (vazia):
//
//   - a `ocr_read` e a `video_read` passam a ter egress externo na classe de risco e ficam `gray`
//     (a pasta é declarada pública): em L4 continuam a correr sem humano;
//   - a `doc_read` passa de `gray` a `safe` (local e pública) — a classe que o `aos-orq` já lhe dava;
//   - nenhuma passa a escalar em L4.
//
// A reclassificação da simulação de autonomia usa o mesmo destino e dá a mesma classe.

// aos539Classe classifica, pela reclassificação da simulação (o classificador real), o selo de uma
// call de `s` com o contexto trusted.
func aos539Classe(s modelToolSpec, dests rm.ToolDestinations) risk.Class {
	rec := audit.AuditRecord{ToolID: s.Name, Capability: s.Capability, Resource: audit.Resource{Type: s.ResourceType, Value: "doc://x"}}
	rec.Context.Taint = taint.StringTrusted
	rec.Context.Reversibility = s.Reversibility
	return reclassificar(rec, dests)
}

func TestAOS539_ManifestoDeProducao_ClasseDeRiscoEEscaladaEmL4(t *testing.T) {
	t.Setenv("AOS_MODEL_TOOLS", toolsJSONProducao)
	specs, err := readModelToolSpecs()
	if err != nil {
		t.Fatal(err)
	}
	dests, err := toolDestinationsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	quer := map[string]struct{ antes, depois risk.Class }{
		"doc_read":   {risk.ClassGray, risk.ClassSafe},
		"ocr_read":   {risk.ClassGray, risk.ClassGray},
		"video_read": {risk.ClassGray, risk.ClassGray},
	}
	for _, s := range specs {
		q, ok := quer[s.Name]
		if !ok {
			t.Fatalf("tool %q nova no manifesto de producao: decide aqui a classe dela", s.Name)
		}
		if got := aos539Classe(s, rm.ToolDestinations{}); got != q.antes {
			t.Errorf("%s sem o destino: %v, queria %v", s.Name, got, q.antes)
		}
		depois := aos539Classe(s, dests)
		if depois != q.depois {
			t.Errorf("%s com o destino: %v, queria %v", s.Name, depois, q.depois)
		}
		if autonomy.Oversight(autonomy.L4, depois).RequiresHumanGate() {
			t.Errorf("%s em L4 passou a exigir humano (classe %v) — o que o dono recusou (opcao A)", s.Name, depois)
		}
	}
}

// aos539ManifestoDerivado copia o manifesto de produção e aplica `mudar` à entrada `nome`.
func aos539ManifestoDerivado(t *testing.T, nome string, mudar func(map[string]any)) {
	t.Helper()
	raw, err := os.ReadFile(toolsJSONProducao)
	if err != nil {
		t.Fatal(err)
	}
	var specs []map[string]any
	if err := json.Unmarshal(raw, &specs); err != nil {
		t.Fatal(err)
	}
	achou := false
	for _, s := range specs {
		if s["name"] == nome {
			mudar(s)
			achou = true
		}
	}
	if !achou {
		t.Fatalf("o manifesto de producao ja nao tem %q", nome)
	}
	out, err := json.Marshal(specs)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "tools.json")
	if err := os.WriteFile(p, out, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AOS_MODEL_TOOLS", p)
}

// Um recurso SEM sensibilidade declarada com egress externo continua a ler-se sensível: `danger`, e
// em L4 escala. É a tentativa revertida do AOS-538, agora só para o recurso que ninguém classificou.
func TestAOS539_SemSensibilidadeDeclaradaComEgressExternoEscalaEmL4(t *testing.T) {
	for _, nome := range []string{"ocr_read", "video_read"} {
		t.Run(nome, func(t *testing.T) {
			aos539ManifestoDerivado(t, nome, func(s map[string]any) { delete(s, "resource_sensitivity") })
			specs, err := readModelToolSpecs()
			if err != nil {
				t.Fatal(err)
			}
			dests, err := toolDestinationsFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range specs {
				if s.Name != nome {
					continue
				}
				c := aos539Classe(s, dests)
				if c != risk.ClassDanger || !autonomy.Oversight(autonomy.L4, c).RequiresHumanGate() {
					t.Fatalf("%s sem resource_sensitivity: classe %v (escala em L4=%v), queria danger e escalada", nome, c, autonomy.Oversight(autonomy.L4, c).RequiresHumanGate())
				}
				return
			}
			t.Fatalf("%s desapareceu do manifesto derivado", nome)
		})
	}
}

// Uma `resource_sensitivity` fora do vocabulário, ou declarada numa tool que não é uma leitura (o
// classificador ignorá-la-ia), ABORTA a leitura do manifesto — como os outros eixos.
func TestAOS539_SensibilidadeIlegivelOuForaDeUmaLeituraAbortaOArranque(t *testing.T) {
	casos := map[string]func(map[string]any){
		"fora-do-vocabulario": func(s map[string]any) { s["resource_sensitivity"] = "publico" },
		"maiusculas":          func(s map[string]any) { s["resource_sensitivity"] = "PUBLIC" },
		"confidential":        func(s map[string]any) { s["resource_sensitivity"] = "confidential" },
		"tool-que-muta":       func(s map[string]any) { s["mutation"] = "mutates" },
		"mutacao-implicita":   func(s map[string]any) { delete(s, "mutation") },
		"capability-de-rede":  func(s map[string]any) { s["capability"] = "cap:http.get" },
	}
	for nome, mudar := range casos {
		t.Run(nome, func(t *testing.T) {
			aos539ManifestoDerivado(t, "doc_read", mudar)
			if _, err := readModelToolSpecs(); !errors.Is(err, ErrBadModelTools) || !strings.Contains(err.Error(), "resource_sensitivity") {
				t.Fatalf("readModelToolSpecs devia recusar pela resource_sensitivity (ErrBadModelTools), veio %v", err)
			}
			if _, err := toolDestinationsFromEnv(); err == nil {
				t.Fatal("toolDestinationsFromEnv (o caminho do Bootstrap) aceitou o manifesto")
			}
		})
	}
	// Controlo: os três valores do vocabulário passam.
	for _, v := range rm.ResourceSensitivities {
		aos539ManifestoDerivado(t, "doc_read", func(s map[string]any) { s["resource_sensitivity"] = v })
		if _, err := readModelToolSpecs(); err != nil {
			t.Fatalf("resource_sensitivity %q devia passar: %v", v, err)
		}
	}
}
