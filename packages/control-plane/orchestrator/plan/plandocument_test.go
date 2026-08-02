package plan

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// validDoc devolve um PlanDocument totalmente populado e bem-formado — a base dos
// testes de round-trip e o ponto de partida das mutações inválidas.
func validDoc() PlanDocument {
	return PlanDocument{
		PlanVersion: PlanVersion{Major: 1, Minor: 0, Patch: 0},
		Objective:   "montar uma equipa de entrega",
		BudgetTotal: BudgetEstimate{Tokens: 100000, CostMicroUSD: 250000},
		PlannerMeta: PlannerMeta{
			Model:            "modelo-decompositor",
			PromptVersion:    "3.1.0",
			CapabilitiesHash: "sha256:cafe",
		},
		Nodes: []Node{
			{
				NodeID:    "n1",
				Role:      "gestor-de-entrega",
				Objective: "coordenar os ramos",
				Tools: []ToolRef{
					{Name: "planner.compose", Version: "1.2.0", Digest: "sha256:aaa"},
				},
				DependsOn:      nil,
				BudgetEstimate: BudgetEstimate{Tokens: 40000, CostMicroUSD: 100000},
				RiskClass:      RiskSafe,
			},
			{
				NodeID:    "n2",
				Role:      "engenheiro-backend",
				Objective: "implementar o servico",
				Tools: []ToolRef{
					{Name: "repo.write", Version: "2.0.1", Digest: "sha256:bbb"},
				},
				DependsOn:      []string{"n1"},
				BudgetEstimate: BudgetEstimate{Tokens: 60000, CostMicroUSD: 150000},
				RiskClass:      RiskGray,
			},
		},
	}
}

// TestDecode_RoundTrip prova que Encode→Decode preserva o documento inteiro.
//
// FALSIFICAÇÃO: compara os VALORES das structs com reflect.DeepEqual (não uma
// re-serialização), pelo que apanha divergência real de campo. Se um campo mudasse
// de tipo, ou o plan_version não serializasse como string, o valor reconstruído
// divergiria do original e o reflect.DeepEqual FALHA. NÃO-VÁCUO: o documento base
// cobre todos os campos por-nó e de topo, incluindo tools[], depends_on e
// risk_class.
func TestDecode_RoundTrip(t *testing.T) {
	t.Parallel()
	orig := validDoc()
	raw, err := Encode(orig)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	back, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !reflect.DeepEqual(orig, back) {
		t.Fatalf("round-trip divergiu:\n orig = %+v\n back = %+v", orig, back)
	}
}

// TestDecode_RejectsUnknownField prova que um campo desconhecido — em qualquer
// nível — é REJEITADO (DisallowUnknownFields), como na disciplina de config do nó.
//
// PROVA DE FALHA-ANTES (não-vácuo do controlo): o mesmo JSON passa por
// json.Unmarshal SEM DisallowUnknownFields e é ACEITE (o assert intermédio garante
// que o payload é, de resto, válido) — logo é EXACTAMENTE o DisallowUnknownFields
// de Decode que o rejeita. Sem essa chamada, Decode aceitaria e o teste FALHA.
func TestDecode_RejectsUnknownField(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"topo":         injectUnknownTopLevel(),
		"no":           injectUnknownInNode(),
		"planner_meta": injectUnknownInPlannerMeta(),
		"tool":         injectUnknownInTool(),
	}
	for name, raw := range cases {
		// Falha-antes: um decode PERMISSIVO (sem DisallowUnknownFields) aceita.
		var permissive PlanDocument
		if err := json.Unmarshal([]byte(raw), &permissive); err != nil {
			t.Fatalf("[%s] o payload base devia ser aceite por json.Unmarshal permissivo (senao o teste seria vacuo): %v", name, err)
		}
		// Fail-closed: Decode (com DisallowUnknownFields) rejeita.
		if _, err := Decode([]byte(raw)); err == nil {
			t.Fatalf("[%s] Decode devia rejeitar o campo desconhecido", name)
		} else if !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("[%s] erro devia mencionar 'unknown field', obteve: %v", name, err)
		}
	}
}

// TestDecode_RejectsTrailingData prova que bytes JSON a seguir ao documento são
// recusados (um plano por payload).
//
// FALSIFICAÇÃO: sem a verificação dec.More(), o segundo objecto seria ignorado em
// silêncio e o teste FALHA.
func TestDecode_RejectsTrailingData(t *testing.T) {
	t.Parallel()
	raw, _ := Encode(validDoc())
	polluted := append(append([]byte{}, raw...), []byte(` {"extra":1}`)...)
	if _, err := Decode(polluted); !errors.Is(err, ErrTrailingData) {
		t.Fatalf("esperava ErrTrailingData, obteve %v", err)
	}
}

// TestDecode_RejectsInvalidTypesAndCardinalities prova que tipos e cardinalidades
// inválidos são recusados fail-closed — cobrindo cada regra de forma.
//
// FALSIFICAÇÃO: cada entrada tem de disparar o erro sentinela esperado; se
// validateShape não verificasse (ex.) node_id duplicado, ou se o decoder aceitasse
// um número negativo em uint64, a entrada respectiva FALHA. NÃO-VÁCUO: cada caso
// isola uma única violação partindo de um documento de resto válido.
func TestDecode_RejectsInvalidTypesAndCardinalities(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		raw     string
		wantErr error // nil ⇒ basta ser erro (violação de TIPO apanhada pelo decoder)
	}{
		{"tokens_negativo", mutate(func(m map[string]any) {
			m["budget_total"].(map[string]any)["tokens"] = -5
		}), nil},
		{"tokens_fraccionario", mutate(func(m map[string]any) {
			m["budget_total"].(map[string]any)["tokens"] = 1.5
		}), nil},
		{"risk_class_string_como_numero", mutate(func(m map[string]any) {
			node0(m)["risk_class"] = 3
		}), nil},
		{"sem_nos", mutate(func(m map[string]any) {
			m["nodes"] = []any{}
		}), ErrNoNodes},
		{"objective_topo_vazio", mutate(func(m map[string]any) {
			m["objective"] = ""
		}), ErrMissingObjective},
		{"planner_meta_incompleto", mutate(func(m map[string]any) {
			m["planner_meta"].(map[string]any)["capabilities_hash"] = ""
		}), ErrMissingPlannerMeta},
		{"plan_version_ausente", mutate(func(m map[string]any) {
			delete(m, "plan_version")
		}), ErrInvalidPlanVersion},
		{"node_id_duplicado", mutate(func(m map[string]any) {
			node0(m)["node_id"] = "dup"
			node1(m)["node_id"] = "dup"
		}), ErrDuplicateNodeID},
		{"node_field_vazio", mutate(func(m map[string]any) {
			node0(m)["role"] = ""
		}), ErrEmptyNodeField},
		{"risk_class_desconhecido", mutate(func(m map[string]any) {
			node0(m)["risk_class"] = "critical"
		}), ErrInvalidRiskClass},
		{"tool_sem_digest", mutate(func(m map[string]any) {
			tool00(m)["digest"] = ""
		}), ErrIncompleteToolRef},
		{"depends_on_vazio", mutate(func(m map[string]any) {
			node1(m)["depends_on"] = []any{""}
		}), ErrInvalidDependsOn},
		{"depends_on_duplicado", mutate(func(m map[string]any) {
			node1(m)["depends_on"] = []any{"n1", "n1"}
		}), ErrInvalidDependsOn},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, err := Decode([]byte(c.raw))
			if err == nil {
				t.Fatalf("Decode devia falhar em %q", c.name)
			}
			if c.wantErr != nil && !errors.Is(err, c.wantErr) {
				t.Fatalf("%q: esperava %v, obteve %v", c.name, c.wantErr, err)
			}
		})
	}
}

// TestDecode_AcceptsOmittedAdvisoryRiskClass prova que risk_class é OPCIONAL
// (advisory): um nó sem o campo é aceite (o vazio é um valor de enum admissível).
//
// FALSIFICAÇÃO: se validateShape exigisse risk_class não-vazio, este documento
// válido seria rejeitado e o teste FALHA. Fixa a semântica advisory do §3.3.
func TestDecode_AcceptsOmittedAdvisoryRiskClass(t *testing.T) {
	t.Parallel()
	raw := mutate(func(m map[string]any) {
		delete(node0(m), "risk_class")
	})
	doc, err := Decode([]byte(raw))
	if err != nil {
		t.Fatalf("risk_class omitido devia ser aceite (advisory): %v", err)
	}
	if doc.Nodes[0].RiskClass != RiskUnset {
		t.Fatalf("risk_class omitido devia ser RiskUnset, obteve %q", doc.Nodes[0].RiskClass)
	}
}

// --- utilitários de teste (sem I/O) ---

// mutate parte do documento válido, aplica a mutação sobre o mapa genérico e
// re-serializa — produzindo um payload com UMA violação isolada.
func mutate(fn func(map[string]any)) string {
	raw, _ := Encode(validDoc())
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		panic(err)
	}
	fn(m)
	out, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return string(out)
}

func node0(m map[string]any) map[string]any { return m["nodes"].([]any)[0].(map[string]any) }
func node1(m map[string]any) map[string]any { return m["nodes"].([]any)[1].(map[string]any) }
func tool00(m map[string]any) map[string]any {
	return node0(m)["tools"].([]any)[0].(map[string]any)
}

func injectUnknownTopLevel() string {
	return mutate(func(m map[string]any) { m["bogus_top"] = "x" })
}
func injectUnknownInNode() string {
	return mutate(func(m map[string]any) { node0(m)["bogus_node"] = "x" })
}
func injectUnknownInPlannerMeta() string {
	return mutate(func(m map[string]any) { m["planner_meta"].(map[string]any)["bogus_meta"] = "x" })
}
func injectUnknownInTool() string {
	return mutate(func(m map[string]any) { tool00(m)["bogus_tool"] = "x" })
}
