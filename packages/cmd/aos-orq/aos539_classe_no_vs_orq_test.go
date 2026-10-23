package main

// aos539_classe_no_vs_orq_test.go — AOS-539: o `aos-orq` e o nó derivam a MESMA classe de risco
// para a mesma tool. O nó classifica cada call com o anotador de risco do RM e o destino que o
// manifesto declara (egress e `resource_sensitivity`); o `aos-orq` classifica o nó do plano pelos
// eixos do snapshot pinado. Se divergirem, o humano aprova um plano com uma classe e o nó decide a
// autonomia de cada call com outra.
//
// O snapshot de produção (`/opt/aos/orq/snapshot.json`) não está no repositório; usa-se a forma
// documentada em deploy/server/README.md (`"sensitivity": "public"`, os eixos de destino iguais aos
// do nó). O nó não se importa daqui (é outro `main`): o manifesto lê-se pelos mesmos nomes JSON e o
// destino constrói-se com o tipo do RM, como o `toolDestinationsFromEnv` do nó.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planvalidate"
	rm "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/kernel/reference-monitor/taint"
)

// aos539ManifestoDeProducao é o manifesto de tools do nó de produção.
const aos539ManifestoDeProducao = "../../../deploy/server/model-tools/tools.json"

// aos539ToolDoManifesto é a parte de uma entrada do manifesto que entra na classe de risco.
type aos539ToolDoManifesto struct {
	Name                string `json:"name"`
	Capability          string `json:"capability"`
	ResourceType        string `json:"resource_type"`
	Egress              string `json:"egress"`
	Mutation            string `json:"mutation"`
	Reversibility       string `json:"reversibility"`
	ResourceSensitivity string `json:"resource_sensitivity"`
}

func aos539LerManifesto(t *testing.T) []aos539ToolDoManifesto {
	t.Helper()
	raw, err := os.ReadFile(aos539ManifestoDeProducao)
	if err != nil {
		t.Fatalf("ler o manifesto de producao: %v", err)
	}
	var tools []aos539ToolDoManifesto
	if err := json.Unmarshal(raw, &tools); err != nil {
		t.Fatalf("manifesto ilegivel: %v", err)
	}
	return tools
}

// aos539SnapshotDocumentado escreve o snapshot na forma documentada de produção: cada tool do
// manifesto com `sensitivity` = `sens(tool)` e os eixos de destino do manifesto.
func aos539SnapshotDocumentado(t *testing.T, tools []aos539ToolDoManifesto, sens func(aos539ToolDoManifesto) string) planvalidate.Snapshot {
	t.Helper()
	var linhas []string
	for _, tl := range tools {
		s := ""
		if v := sens(tl); v != "" {
			s = fmt.Sprintf(`"sensitivity":%q,`, v)
		}
		linhas = append(linhas, fmt.Sprintf(`{"name":%q,"version":"1.0.0","digest":"sha256:%s","admissible":true,%s"egress":%q,"reversibility":%q,"mutation":%q}`,
			tl.Name, tl.Name, s, tl.Egress, tl.Reversibility, tl.Mutation))
	}
	p := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(p, []byte(`{"hash":"sha256:snap-aos539","tools":[`+strings.Join(linhas, ",")+`]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	snap, err := carregarSnapshot(p)
	if err != nil {
		t.Fatalf("carregarSnapshot: %v", err)
	}
	return snap
}

// aos539ClasseDoOrq é a classe DERIVADA de um nó do plano que usa só a tool dada.
func aos539ClasseDoOrq(snap planvalidate.Snapshot, tool string) risk.Class {
	doc := plan.PlanDocument{
		PlanVersion: plan.CurrentPlanVersion,
		Objective:   "AOS-539",
		Nodes: []plan.Node{{NodeID: "n1", Role: "worker", Objective: "ler",
			Tools: []plan.ToolRef{{Name: tool, Version: "1.0.0", Digest: "sha256:" + tool}}}},
	}
	return planvalidate.ResolveRisks(doc, snap, nil)["n1"].Classification.Class
}

// aos539ClasseDoNo é a classe que o anotador de risco do nó sela para uma call da tool, com o
// contexto trusted e a sensibilidade que o loop entrega (vazia).
func aos539ClasseDoNo(t *testing.T, tools []aos539ToolDoManifesto, tl aos539ToolDoManifesto, sens func(aos539ToolDoManifesto) string) risk.Class {
	t.Helper()
	m := make(map[string]rm.ToolDestination, len(tools))
	for _, x := range tools {
		m[x.Name] = rm.ToolDestination{Capability: x.Capability, Egress: x.Egress, Mutation: x.Mutation, Reversibility: x.Reversibility, Sensitivity: sens(x)}
	}
	call := &rm.Call{ToolID: tl.Name, Capability: tl.Capability, Resource: rm.Resource{Type: tl.ResourceType, Value: "doc://x"},
		Context: rm.CallContext{Taint: taint.StringTrusted, Reversibility: tl.Reversibility}}
	if _, err := rm.NewRiskClassifier(nil).WithToolDestinations(rm.NewToolDestinations(m)).Evaluate(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	switch call.Context.RiskClass {
	case "safe":
		return risk.ClassSafe
	case "gray":
		return risk.ClassGray
	default:
		return risk.ClassDanger
	}
}

// TestAOS539_NoEOrqDerivamAMesmaClasse — com o manifesto e o snapshot de produção, cada tool tem a
// mesma classe nos dois lados (doc_read safe; ocr_read, video_read e artifact_write gray). E o controlo fail-closed:
// sem a sensibilidade declarada nos dois, as tools de egress externo saem danger nos dois.
func TestAOS539_NoEOrqDerivamAMesmaClasse(t *testing.T) {
	tools := aos539LerManifesto(t)
	quer := map[string]risk.Class{"doc_read": risk.ClassSafe, "media_view": risk.ClassSafe, "ocr_read": risk.ClassGray, "video_read": risk.ClassGray,
		// AOS-549: a escrita local do run muta (reversivel, sem rede) ⇒ gray nos dois lados.
		"artifact_write": risk.ClassGray}

	declarada := func(x aos539ToolDoManifesto) string { return x.ResourceSensitivity }
	snap := aos539SnapshotDocumentado(t, tools, func(aos539ToolDoManifesto) string { return "public" })
	for _, tl := range tools {
		q, ok := quer[tl.Name]
		if !ok {
			t.Fatalf("tool %q nova no manifesto de producao: decide aqui a classe dela", tl.Name)
		}
		// Numa tool que escreve (AOS-549) a sensibilidade do recurso não se aplica nos dois lados.
		if tl.Mutation == "none" && tl.ResourceSensitivity != "public" {
			t.Errorf("%s: o manifesto declara resource_sensitivity %q e o snapshot de producao public — divergem", tl.Name, tl.ResourceSensitivity)
		}
		doOrq, doNo := aos539ClasseDoOrq(snap, tl.Name), aos539ClasseDoNo(t, tools, tl, declarada)
		if doOrq != doNo {
			t.Errorf("%s: o aos-orq deriva %v e o no %v", tl.Name, doOrq, doNo)
		}
		if doNo != q {
			t.Errorf("%s: classe %v, queria %v", tl.Name, doNo, q)
		}
	}

	// Sem sensibilidade nos dois lados: o vazio é sensível nos dois, e o egress externo dá danger.
	nenhuma := func(aos539ToolDoManifesto) string { return "" }
	snapSem := aos539SnapshotDocumentado(t, tools, nenhuma)
	for _, tl := range tools {
		doOrq, doNo := aos539ClasseDoOrq(snapSem, tl.Name), aos539ClasseDoNo(t, tools, tl, nenhuma)
		if doOrq != doNo {
			t.Errorf("%s sem sensibilidade: o aos-orq deriva %v e o no %v", tl.Name, doOrq, doNo)
		}
		if tl.Egress == "external" && doNo != risk.ClassDanger {
			t.Errorf("%s sem sensibilidade e egress externo: %v, queria danger", tl.Name, doNo)
		}
	}
}
