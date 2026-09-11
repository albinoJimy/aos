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

// TestAOS389_PlanDocCondicionalRecusado prova, pela CADEIA REAL do binário (superfície
// `--plan-doc`), que um plano APROVADO com `conditional_on` é RECUSADO fail-closed em vez
// de materializar em silêncio. É a prova ponta-a-ponta do defeito medido em 2026-09-10,
// na superfície de produção onde ele era alcançável.
//
// SUPERADO POR AOS-390: quando o avaliador de ramos estiver composto, este `--plan-doc`
// passa a materializar o plano (com a condição avaliada), e este teste inverte-se.
func TestAOS389_PlanDocCondicionalRecusado(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	wal := filepath.Join(dir, "es.wal")
	snapPath := filepath.Join(dir, "snapshot.json")
	escrever(t, snapPath, snapshotCondicional)
	docPath := filepath.Join(dir, "plano.json")
	escrever(t, docPath, planoCondicional)

	r := correr(t, bin, "serve", "--wal", wal, "--run", "run-cond", "--plan", "plan-cond",
		"--plan-doc", docPath, "--snapshot", snapPath, "--worker", "p1")

	if r.code == exitOK {
		t.Fatalf("--plan-doc condicional foi ACEITE quando devia recusar (fail-open)\nstdout:\n%s", r.stdout)
	}
	// A recusa nomeia o eixo que a fecha por avaliação real (AOS-390).
	if !strings.Contains(r.stderr, "AOS-390") && !strings.Contains(r.stderr, "conditional_on") {
		t.Fatalf("a recusa nao menciona conditional_on/AOS-390\nstderr:\n%s", r.stderr)
	}
}
