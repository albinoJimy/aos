package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// snapshotCondicional resolve a `fs.read` usada pelos nós do plano condicional.
const snapshotCondicional = `{
  "hash": "sha256:snap-cond",
  "tools": [
    {"name":"fs.read","version":"1.0.0","digest":"sha256:aaa","admissible":true,
     "sensitivity":"public","egress":"none","reversibility":"reversible"}
  ]
}`

// planoCondicional é um PlanDocument bem-formado (aceite por plan.Decode) com uma aresta
// `conditional_on`: o nó `recuperacao` só devia despachar se a `verificacao` da recolha
// REPROVAR (`verdict eq fail`) — o ramo de recuperação de ADR-022 §2.1. Desde o AOS-412 o
// `--plan-doc` valida (regra AOS-231), pelo que o veredicto vem de um VERIFICADOR que
// observa a recolha (V1/V1-bis) e o `plan_version` é 1.2.0, o piso do `role: verifier`.
// AOS-408: o capabilities_hash DECLARA o snapshot contra o qual o plano foi construido, e o
// caminho do --plan-doc passou a exigir que corresponda (a mesma regra que o validador AOS-231 ja
// impunha e que este caminho nunca chamava). O modelo e o prompt ficam FORJADOS de proposito — no
// caminho do --goal o Decomposer sobrescreve-os —, mas o hash do snapshot nao pode ser forjado,
// porque e dele que sai o risco de cada no.
const planoCondicional = `{
  "plan_version": "1.2.0",
  "objective": "ramo condicional de recuperacao",
  "budget_total": {"tokens": 100, "cost_micro_usd": 100},
"planner_meta": {"model":"FORJADO","prompt_version":"9.9.9","capabilities_hash":"sha256:snap-cond"},
  "nodes": [
    {"node_id":"recolha","role":"worker","objective":"recolher","depends_on":[],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}},
    {"node_id":"verificacao","role":"verifier","objective":"verificar a recolha","depends_on":["recolha"],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}},
    {"node_id":"recuperacao","role":"worker","objective":"recuperar se falhar","depends_on":[],
     "conditional_on":[{"from":"verificacao","when":[{"subject":"verdict","op":"eq","enum":"fail"}]}],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}}
  ]
}`

// TestAOS390_PlanDocCondicionalAdmitido prova, pela CADEIA REAL do binário (superfície
// `--plan-doc`), a mudança de semântica do ADR-024: a materialização é ADMISSÃO-PURA,
// pelo que um plano APROVADO com `conditional_on` é ADMITIDO (nós pendentes no DAG,
// `plan.materialized` apenso) em vez de recusado. Admitir NÃO é executar — nenhum efeito
// nasce da materialização; a avaliação da condição e a poda `branch_not_taken` são do
// despacho governado, que esta via compõe desde o AOS-412.
//
// Inverte o antigo TestAOS389_PlanDocCondicionalRecusado: o guard interino que recusava
// deixou de existir porque admitir um nó condicional no DAG não produz efeito nenhum.
func TestAOS390_PlanDocCondicionalAdmitido(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	wal := filepath.Join(dir, "es.wal")
	snapPath := filepath.Join(dir, "snapshot.json")
	escrever(t, snapPath, snapshotCondicional)
	docPath := filepath.Join(dir, "plano.json")
	escrever(t, docPath, planoCondicional)

	r := correr(t, bin, "serve", "--wal", wal, "--run", "run-cond", "--plan", "plan-cond",
		"--plan-doc", docPath, "--snapshot", snapPath, "--worker", "p1")

	if r.code != exitOK {
		t.Fatalf("--plan-doc condicional foi RECUSADO quando devia ADMITIR (admit-only, ADR-024)\nstdout:\n%s\nstderr:\n%s", r.stdout, r.stderr)
	}
	// Admitido: os três nós (recolha, verificacao, recuperacao) materializam.
	if !strings.Contains(r.stdout, "materializado:") || !strings.Contains(r.stdout, "nos=3") {
		t.Fatalf("a materialização não admitiu os 3 nós do plano condicional:\n%s", r.stdout)
	}
	// Admitir não é executar (AOS-412): esta via despacha, e o ramo de recuperação NÃO pode
	// arrancar sem o veredicto `fail` da verificação — nenhum veredicto foi emitido.
	if !strings.Contains(r.stdout, "despachado:") {
		t.Fatalf("o despacho governado não correu:\n%s", r.stdout)
	}
	if strings.Contains(r.stdout, "folha recuperacao") || strings.Contains(r.stdout, "papel recuperacao") {
		t.Fatalf("o ramo condicional despachou sem o veredicto que o liberta:\n%s", r.stdout)
	}
}
