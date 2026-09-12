package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// planner_wiring_test.go — T2-A do AOS-388, através de PROCESSOS REAIS.
//
// O `--goal` corre o PLANEADOR GOVERNADO (mediação RM, reserva CAS, NHI agent:planner, N
// tentativas) sobre o fixture NÃO-PRODUÇÃO, VALIDA (AOS-231) e materializa com o DELEGATOR
// REAL. Prova-se com o binário compilado, snapshot e fixture de ficheiros — como um
// operador faria — que o pipeline goal→DAG multi-nó corre ponta-a-ponta.

// snapshotDuasTools é o snapshot PINADO com duas tools admissíveis.
const snapshotDuasTools = `{
  "hash": "sha256:snap-goal",
  "tools": [
    {"name":"fs.read","version":"1.0.0","digest":"sha256:aaa","admissible":true,
     "sensitivity":"public","egress":"none","reversibility":"reversible"},
    {"name":"http.post","version":"2.0.0","digest":"sha256:bbb","admissible":true,
     "sensitivity":"public","egress":"external","reversibility":"reversible"}
  ]
}`

// planoFixtureDuasFolhas é o PlanDocument que o decompositor-fixture devolve: duas folhas
// INDEPENDENTES (sem expansão). A `planner_meta` traz valores FORJADOS de propósito — o
// Decomposer carimba a proveniência autoritativa, pelo que não sobrevivem.
const planoFixtureDuasFolhas = `{
  "plan_version": "1.0.0",
  "objective": "recolher e analisar",
  "budget_total": {"tokens": 100, "cost_micro_usd": 100},
  "planner_meta": {"model":"FORJADO","prompt_version":"9.9.9","capabilities_hash":"sha256:FORJADO"},
  "nodes": [
    {"node_id":"recolha","role":"worker","objective":"recolher","depends_on":[],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}},
    {"node_id":"analise","role":"worker","objective":"analisar","depends_on":[],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}}
  ]
}`

// TestAOS388_GoalPipelineGovernadoPontoAPonto: --goal decompõe (governado), valida e
// materializa os 2 nós, e o grafo durável fica com eles.
func TestAOS388_GoalPipelineGovernadoPontoAPonto(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, snapshotDuasTools)
	fixPath := filepath.Join(dir, "plano.json")
	escrever(t, fixPath, planoFixtureDuasFolhas)
	wal := filepath.Join(dir, "es.wal")

	r := correr(t, bin, "serve", "--wal", wal, "--run", "run-goal",
		"--goal", "recolher e analisar dados", "--snapshot", snapPath,
		"--decompose-fixture", fixPath, "--worker", "p1")
	if r.code != exitOK {
		t.Fatalf("serve --goal saiu %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, "decomposto: objectivo -> plano de 2 nos") {
		t.Fatalf("o Planner governado nao decompos em 2 nos:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, "materializado:") || !strings.Contains(r.stdout, "nos=2") {
		t.Fatalf("o plano nao materializou os 2 nos:\n%s", r.stdout)
	}
	// T4 (AOS-390, ADR-024): o despacho governado corre a jusante da admissão e despacha
	// os 2 nós elegíveis (folhas sem deps). O efeito nasce no DESPACHO, não na
	// materialização — que agora só admite.
	if !strings.Contains(r.stdout, "despachado:") || !strings.Contains(r.stdout, "nos_despachados=2") {
		t.Fatalf("o despacho governado nao despachou os 2 nos elegiveis:\n%s", r.stdout)
	}

	// Um terceiro processo, só de leitura, vê os 2 nós no grafo durável — a coordenação
	// passou pelo log, não por memória.
	insp := correr(t, bin, "inspect", "--wal", wal, "--run", "run-goal")
	if !strings.Contains(insp.stdout, "nos=2") {
		t.Fatalf("o grafo duravel nao tem os 2 nos materializados:\n%s", insp.stdout)
	}
}

// planoFixtureExpansao é um PlanDocument COM EXPANSÃO: `analise` depende de `recolha`,
// pelo que `recolha` tem ≥1 dependente e o classificador fá-lo um PAPEL-QUE-EXPANDE
// (SpawnRole → Delegator.Spawn), enquanto `analise` (sumidouro) é folha. É o caso que
// AOS-393 repara: sem o fix, o Delegator recusava o spawn (ErrDepthMismatch: profundidade
// declarada 0 < autoritativa 1) e a materialização abortava.
const planoFixtureExpansao = `{
  "plan_version": "1.0.0",
  "objective": "recolher e analisar",
  "budget_total": {"tokens": 100, "cost_micro_usd": 100},
  "planner_meta": {"model":"x","prompt_version":"1.0.0","capabilities_hash":"sha256:snap-goal"},
  "nodes": [
    {"node_id":"recolha","role":"worker","objective":"recolher","depends_on":[],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}},
    {"node_id":"analise","role":"worker","objective":"analisar","depends_on":["recolha"],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}}
  ]
}`

// TestAOS393_GoalExpansaoSpawnaPapel: um plano COM expansão — o papel `recolha` é
// SPAWNADO pelo Delegator REAL e `analise` é folha. Sob o ADR-024 (AOS-390), o spawn já
// NÃO acontece na materialização: acontece no DESPACHO governado, quando `recolha` (papel
// sem deps) fica elegível. As correcções de identidade do AOS-393 (Depth via ChainDepth,
// toolCaps no token do run e na classe worker, agent.spawn no RM) são PRESERVADAS no sink,
// pelo que o spawn tem sucesso. FALHA-ANTES (sem essas correcções): ErrDepthMismatch /
// E_UNKNOWN_CLASS / Authority ⊄ pai — o spawn abortava.
func TestAOS393_GoalExpansaoSpawnaPapel(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, snapshotDuasTools)
	fixPath := filepath.Join(dir, "plano.json")
	escrever(t, fixPath, planoFixtureExpansao)
	wal := filepath.Join(dir, "es.wal")

	r := correr(t, bin, "serve", "--wal", wal, "--run", "run-exp",
		"--goal", "recolher e analisar dados", "--snapshot", snapPath,
		"--decompose-fixture", fixPath, "--worker", "p1")
	if r.code != exitOK {
		t.Fatalf("serve --goal (expansao) saiu %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, "materializado:") || !strings.Contains(r.stdout, "nos=2") {
		t.Fatalf("o plano com expansao nao materializou os 2 nos:\n%s", r.stdout)
	}
	// O papel-que-expande foi SPAWNADO no DESPACHO (ADR-024): o DispatchSink imprime
	// "despacho: papel <id> spawnado" quando o Delegator cunha a NHI filha. `recolha` (sem
	// deps) fica elegível na primeira passagem; `analise` depende dele e aguarda.
	if !strings.Contains(r.stdout, "despacho: papel recolha spawnado") {
		t.Fatalf("o papel `recolha` nao foi spawnado pelo despacho governado:\n%s", r.stdout)
	}
}

// TestAOS388_GoalFailClosed: as três recusas fail-closed do --goal.
func TestAOS388_GoalFailClosed(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, snapshotDuasTools)
	fixPath := filepath.Join(dir, "plano.json")
	escrever(t, fixPath, planoFixtureDuasFolhas)

	casos := []struct {
		nome, grep string
		args       []string
	}{
		{"sem snapshot", "snapshot", []string{"--goal", "x", "--decompose-fixture", fixPath}},
		{"sem modelo", "Model Gateway", []string{"--goal", "x", "--snapshot", snapPath}},
		{"com --nodes", "combina", []string{"--goal", "x", "--snapshot", snapPath, "--decompose-fixture", fixPath, "--nodes", "a"}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			wal := filepath.Join(t.TempDir(), "es.wal")
			args := append([]string{"serve", "--wal", wal, "--run", "run-fc"}, c.args...)
			r := correr(t, bin, args...)
			if r.code == exitOK {
				t.Fatalf("%s: --goal foi aceite quando devia recusar\nstdout:\n%s", c.nome, r.stdout)
			}
			if !strings.Contains(r.stderr, c.grep) {
				t.Fatalf("%s: a recusa nao menciona %q\nstderr:\n%s", c.nome, c.grep, r.stderr)
			}
		})
	}
}
