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
// `conditional_on`: o nó `recuperacao` só devia despachar se `recolha` REPROVAR
// (`verdict eq fail`) — o ramo de recuperação de ADR-022 §2.1. `plan_version` é 1.1.0
// porque `conditional_on` é aditivo dessa MINOR (semver do schema).
const planoCondicional = `{
  "plan_version": "1.1.0",
  "objective": "ramo condicional de recuperacao",
  "budget_total": {"tokens": 100, "cost_micro_usd": 100},
  "planner_meta": {"model":"FORJADO","prompt_version":"9.9.9","capabilities_hash":"sha256:FORJADO"},
  "nodes": [
    {"node_id":"recolha","role":"worker","objective":"recolher","depends_on":[],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}},
    {"node_id":"recuperacao","role":"worker","objective":"recuperar se falhar","depends_on":[],
     "conditional_on":[{"from":"recolha","when":[{"subject":"verdict","op":"eq","enum":"fail"}]}],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}}
  ]
}`

// TestAOS390_PlanDocCondicionalAdmitido prova, pela CADEIA REAL do binário (superfície
// `--plan-doc`), a mudança de semântica do ADR-024: a materialização é ADMISSÃO-PURA,
// pelo que um plano APROVADO com `conditional_on` é ADMITIDO (nós pendentes no DAG,
// `plan.materialized` apenso) em vez de recusado. Admitir NÃO é executar — nenhum efeito
// nasce da materialização; a avaliação da condição e a poda `branch_not_taken` são do
// despacho governado, que esta via (`--plan-doc`, sem Delegator) não compõe.
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
	// Admitido: os dois nós (recolha, recuperacao) materializam.
	if !strings.Contains(r.stdout, "materializado:") || !strings.Contains(r.stdout, "nos=2") {
		t.Fatalf("a materialização não admitiu os 2 nós do plano condicional:\n%s", r.stdout)
	}
}
