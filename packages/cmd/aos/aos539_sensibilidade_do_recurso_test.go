package main

import (
	"encoding/json"
	"errors"
	"fmt"
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
	// Controlo: os três valores do vocabulário passam — escritos aqui por extenso, para que encolher
	// ou alargar `ResourceSensitivities` avermelhe em vez de arrastar o teste com ela.
	for _, v := range []string{"public", "internal", "sensitive"} {
		aos539ManifestoDerivado(t, "doc_read", func(s map[string]any) { s["resource_sensitivity"] = v })
		if _, err := readModelToolSpecs(); err != nil {
			t.Fatalf("resource_sensitivity %q devia passar: %v", v, err)
		}
	}
}

// aos539ManifestoComUmaTool escreve um AOS_MODEL_TOOLS com uma só tool, dada em JSON.
func aos539ManifestoComUmaTool(t *testing.T, tool string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tools.json")
	if err := os.WriteFile(p, []byte("["+tool+"]"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AOS_MODEL_TOOLS", p)
}

// Achado M-1 da revisão: as formas que a regra «só numa leitura» do classificador deixava passar
// com `resource_sensitivity: public` — e que ficavam `gray` — são recusadas no ARRANQUE.
func TestAOS539_ValidadorRecusaAsFormasDoRevisor(t *testing.T) {
	casos := []struct {
		nome, tool string
		erro       error
		diz        string
	}{
		{
			// Capability local, egress externo, sandbox `upload` com args do modelo: o que sai é
			// escrito pelo modelo, não o ficheiro declarado.
			nome: "fs_sync-upload-com-args_from",
			tool: `{"name":"fs_sync","capability":"cap:fs.read","resource_type":"file","resource_value":"doc://{doc_id}",
				"resource_region":"eu-west","egress":"external","mutation":"none","reversibility":"reversible",
				"resource_sensitivity":"public","sandbox":{"command":"upload","path_arg":"doc_id","args_from":["dest"]}}`,
			erro: ErrBadMutation, diz: "upload",
		},
		{
			// O mesmo sem o verbo de escrita no comando: egress externo sem o bloco `modelo`.
			nome: "fs_sync-egress-externo-sem-modelo",
			tool: `{"name":"fs_sync","capability":"cap:fs.read","resource_type":"file","resource_value":"doc://{doc_id}",
				"resource_region":"eu-west","egress":"external","mutation":"none","reversibility":"reversible",
				"resource_sensitivity":"public","sandbox":{"command":"read","path_arg":"doc_id"}}`,
			erro: ErrBadModelTools, diz: "modelo",
		},
		{
			// Leitura local com args do modelo no comando.
			nome: "args_from-numa-leitura-local",
			tool: `{"name":"doc_grep","capability":"cap:fs.read","resource_type":"file","resource_value":"doc://{doc_id}",
				"resource_region":"eu-west","egress":"none","mutation":"none","reversibility":"reversible",
				"resource_sensitivity":"public","sandbox":{"command":"read","path_arg":"doc_id","args_from":["padrao"]}}`,
			erro: ErrBadModelTools, diz: "args_from",
		},
		{
			// `cap:fs.write` com `mutation: none` mentiroso e sandbox `write`.
			nome: "fs_wr-mutation-none-mentiroso",
			tool: `{"name":"fs_wr","capability":"cap:fs.write","resource_type":"file","resource_value":"doc://{doc_id}",
				"resource_region":"eu-west","egress":"none","mutation":"none","reversibility":"reversible",
				"resource_sensitivity":"public","sandbox":{"command":"write","path_arg":"doc_id"}}`,
			erro: ErrBadMutation, diz: "write",
		},
		{
			// Capability local com um recurso de rede (`url`): o classificador vê egress.
			nome: "resource_type-url",
			tool: `{"name":"doc_url","capability":"cap:fs.read","resource_type":"url","resource_value":"https://x.example/{doc_id}",
				"resource_region":"eu-west","egress":"none","mutation":"none","reversibility":"reversible",
				"resource_sensitivity":"public"}`,
			erro: ErrBadModelTools, diz: "resource_sensitivity",
		},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			aos539ManifestoComUmaTool(t, c.tool)
			_, err := readModelToolSpecs()
			if !errors.Is(err, c.erro) || !strings.Contains(err.Error(), c.diz) {
				t.Fatalf("o validador tinha de recusar (%v, a dizer %q), veio %v", c.erro, c.diz, err)
			}
			if _, err := toolDestinationsFromEnv(); err == nil {
				t.Fatal("toolDestinationsFromEnv (o caminho do Bootstrap) aceitou o manifesto")
			}
		})
	}
}

// A coerência de `mutation: "none"` vale para TODAS as tools, com ou sem sensibilidade: um verbo
// de escrita na capability ou no comando recusa; os verbos de leitura passam.
func TestAOS539_MutacaoNoneComVerboDeEscritaAborta(t *testing.T) {
	for _, c := range []struct {
		capability, command string
		recusa              bool
	}{
		{"cap:fs.write", "", true},
		{"cap:crm.contact.update", "", true},
		{"cap:doc.delete", "", true},
		{"cap:fs.read", "write", true},
		{"cap:fs.read", "upload", true},
		{"cap:fs.read", "read", false},
		{"cap:fs.read", "", false},
		{"cap:fs.list", "", false},
	} {
		sb := ""
		if c.command != "" {
			sb = fmt.Sprintf(`,"sandbox":{"command":%q,"path_arg":"doc_id"}`, c.command)
		}
		aos539ManifestoComUmaTool(t, fmt.Sprintf(`{"name":"x","capability":%q,"resource_type":"file","resource_value":"doc://{doc_id}",
			"resource_region":"eu-west","egress":"none","mutation":"none","reversibility":"reversible"%s}`, c.capability, sb))
		_, err := readModelToolSpecs()
		if c.recusa && !errors.Is(err, ErrBadMutation) {
			t.Errorf("%s/%q com mutation none devia abortar com ErrBadMutation, veio %v", c.capability, c.command, err)
		}
		if !c.recusa && err != nil {
			t.Errorf("%s/%q com mutation none devia passar, veio %v", c.capability, c.command, err)
		}
	}
}

// O manifesto de PRODUÇÃO continua a arrancar com as regras apertadas.
func TestAOS539_ManifestoDeProducaoContinuaValido(t *testing.T) {
	t.Setenv("AOS_MODEL_TOOLS", toolsJSONProducao)
	if _, err := readModelToolSpecs(); err != nil {
		t.Fatalf("o manifesto de producao deixou de ser aceite: %v", err)
	}
	if _, err := toolDestinationsFromEnv(); err != nil {
		t.Fatalf("toolDestinationsFromEnv: %v", err)
	}
	if _, _, err := loadModelToolsFromEnv(); err != nil {
		t.Fatalf("loadModelToolsFromEnv: %v", err)
	}
}
