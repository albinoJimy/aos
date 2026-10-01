package main

// aos409_eixo_de_mutacao_test.go — AOS-409 (fecha o DEF-275), a metade do NÓ: o catálogo de
// produção (`GET /tools`, a partir de `deploy/server/model-tools/tools.json`) serve o eixo de
// MUTAÇÃO, e só `"mutation":"none"` declarado o serve como leitura.
//
// O critério `planvalidate.IsEffectTool` sobre este MESMO manifesto prova-se do lado do `aos-orq`
// (packages/cmd/aos-orq/aos409_eixo_de_mutacao_test.go): o nó não pode importar o módulo do
// orquestrador, nem em teste via go.mod (ADR-018, AOS-164b — boundary_orq_sch_test.go).

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// aos409ManifestoDerivado copia o manifesto de PRODUÇÃO e altera só o campo `mutation` do
// `doc_read` (apagar = `nil`). Tudo o resto — nome, egress, reversibilidade, sandbox — fica o de
// produção, para que a única diferença medida seja a do eixo.
func aos409ManifestoDerivado(t *testing.T, mutation any) string {
	t.Helper()
	raw, err := os.ReadFile(aos441ManifestoDeProducao)
	if err != nil {
		t.Fatalf("ler o manifesto de produção: %v", err)
	}
	var specs []map[string]any
	if err := json.Unmarshal(raw, &specs); err != nil {
		t.Fatalf("manifesto de produção ilegível: %v", err)
	}
	mexeu := false
	for _, s := range specs {
		if s["name"] != "doc_read" {
			continue
		}
		if mutation == nil {
			delete(s, "mutation")
		} else {
			s["mutation"] = mutation
		}
		mexeu = true
	}
	if !mexeu {
		t.Fatal("o manifesto de produção deixou de nomear `doc_read`")
	}
	out, err := json.Marshal(specs)
	if err != nil {
		t.Fatalf("serializar: %v", err)
	}
	p := filepath.Join(t.TempDir(), "tools.json")
	if err := os.WriteFile(p, out, 0o600); err != nil {
		t.Fatalf("escrever: %v", err)
	}
	return p
}

// aos409DocRead devolve a entrada do `doc_read` tal como o catálogo do nó a serve.
func aos409DocRead(t *testing.T, manifesto string) entradaDoCatalogo {
	t.Helper()
	aos441Ambiente(t, manifesto)
	cat, err := catalogoDeToolsDoAmbiente()
	if err != nil {
		t.Fatalf("catalogoDeToolsDoAmbiente: %v", err)
	}
	for _, e := range cat {
		if e.Name == "doc_read" {
			return e
		}
	}
	t.Fatalf("o catálogo não tem `doc_read`: %+v", cat)
	return entradaDoCatalogo{}
}

// TestAOS409CatalogoDeProducaoServeAMutacao — o `doc_read` de produção é `egress none` +
// `reversible`: pelos dois eixos antigos é leitura em QUALQUER caso. O que o catálogo diz da
// mutação é o que decide, e mede-se nos três estados do manifesto com tudo o resto igual:
//
//   - o manifesto de produção declara `"mutation":"none"` ⇒ o catálogo serve `none`;
//   - o mesmo manifesto SEM o campo ⇒ `mutates` (o vazio é mutador, fail-closed);
//   - o mesmo manifesto com `"mutates"` ⇒ `mutates`.
func TestAOS409CatalogoDeProducaoServeAMutacao(t *testing.T) {
	prod := aos409DocRead(t, aos441ManifestoDeProducao)
	if prod.Egress != "none" || prod.Reversibility != "reversible" {
		t.Fatalf("o doc_read de produção deixou de ser egress none + reversible (%+v) — o eixo de mutação deixou de ser o único a decidir", prod)
	}
	if prod.Mutation != "none" {
		t.Fatalf("o manifesto de produção tem de declarar `\"mutation\":\"none\"` no doc_read (transição do AOS-409), o catálogo serviu %q", prod.Mutation)
	}
	for nome, mutation := range map[string]any{"sem o campo": nil, "mutates": "mutates"} {
		t.Run(nome, func(t *testing.T) {
			if got := aos409DocRead(t, aos409ManifestoDerivado(t, mutation)).Mutation; got != "mutates" {
				t.Fatalf("o nó tinha de servir `mutates`, serviu %q", got)
			}
		})
	}
}

// Um valor fora do vocabulário aborta o arranque — não cai no silêncio fail-closed, porque o
// operador julgaria ter declarado outra coisa (a mesma regra de `reversibility`).
func TestAOS409MutacaoForaDoVocabularioAborta(t *testing.T) {
	aos441Ambiente(t, aos409ManifestoDerivado(t, "nenhuma"))
	if _, err := readModelToolSpecs(); !errors.Is(err, ErrBadMutation) {
		t.Fatalf("mutation \"nenhuma\" tinha de abortar com ErrBadMutation, veio %v", err)
	}
	if _, err := catalogoDeToolsDoAmbiente(); !errors.Is(err, ErrBadMutation) {
		t.Fatalf("o catálogo tinha de recusar servir o eixo, veio %v", err)
	}
}

// "none" numa tool cujo sandbox ESCREVE o argumento do modelo é uma contradição: aborta, em vez
// de se promover em silêncio a "mutates".
func TestAOS409WriteArgComMutacaoNoneAborta(t *testing.T) {
	m := filepath.Join(t.TempDir(), "tools.json")
	escrita := func(mutation string) []byte {
		return []byte(fmt.Sprintf(`[{"name":"doc_write","capability":"cap:fs.write","resource_type":"file",
	  "resource_value":"doc://{doc_id}","resource_region":"eu-west","egress":"none","reversibility":"reversible",
	  "mutation":%q,"sandbox":{"command":"write","path_arg":"doc_id","write_arg":"conteudo"}}]`, mutation))
	}
	if err := os.WriteFile(m, escrita("none"), 0o600); err != nil {
		t.Fatal(err)
	}
	aos441Ambiente(t, m)
	if _, err := readModelToolSpecs(); !errors.Is(err, ErrBadMutation) {
		t.Fatalf("write_arg com mutation none tinha de abortar com ErrBadMutation, veio %v", err)
	}

	// Não-vacuidade: a mesma tool a declarar "mutates" arranca, e o catálogo serve-a mutadora.
	if err := os.WriteFile(m, escrita("mutates"), 0o600); err != nil {
		t.Fatal(err)
	}
	cat, err := catalogoDeToolsDoAmbiente()
	if err != nil || len(cat) != 1 || cat[0].Mutation != "mutates" {
		t.Fatalf("a escrita declarada tinha de servir `mutates`: %+v err=%v", cat, err)
	}
}
