package main

// AOS-408 — o gate de aprovação de plano, pelo PROCESSO REAL.
//
// O par que importa é este: o MESMO pipeline, dois planos. Um usa uma tool que o snapshot pinado
// declara irreversível com egress externo de dados sensíveis (`danger` derivado) e por isso NÃO
// materializa — fica pendente, com saída 6. O outro usa só leitura local e materializa e despacha
// como antes.
//
// FALHA-ANTES (medida antes de existir o gate): o plano de risco materializava e despachava, com
// saída 0. E o despacho ainda o deixaria passar hoje sem o cartão, porque o `needsCard` derivava do
// `risk_class` ADVISORY do documento — um plano que se declarasse `safe` sobre uma tool
// irreversível não exigia cartão nenhum.

import (
	"path/filepath"
	"strings"
	"testing"
)

// aos408SnapshotComPerigo declara uma tool IRREVERSÍVEL com egress externo de dados sensíveis —
// `danger` pelo classificador — ao lado de uma leitura local inócua.
const aos408SnapshotComPerigo = `{
  "hash": "sha256:snap-aos408",
  "tools": [
    {"name":"fs.read","version":"1.0.0","digest":"sha256:aaa","admissible":true,
     "sensitivity":"public","egress":"none","reversibility":"reversible"},
    {"name":"http.post","version":"2.0.0","digest":"sha256:bbb","admissible":true,
     "sensitivity":"sensitive","egress":"external","reversibility":"irreversible"}
  ]
}`

// aos408PlanoQueSeDizSeguro é o caso adversarial: o nó que usa a tool perigosa DECLARA-SE `safe`.
// O piso derivado das tools pinadas vence (`elevateOnly`), e é ele que gata.
const aos408PlanoQueSeDizSeguro = `{
  "plan_version": "1.0.0",
  "objective": "recolher e publicar",
  "budget_total": {"tokens": 100, "cost_micro_usd": 100},
  "planner_meta": {"model":"fixture","prompt_version":"1.2.0","capabilities_hash":"sha256:snap-aos408"},
  "nodes": [
    {"node_id":"recolha","role":"worker","objective":"recolher","depends_on":[],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}},
    {"node_id":"publicacao","role":"worker","objective":"publicar","depends_on":["recolha"],
     "risk_class":"safe",
     "tools":[{"name":"http.post","version":"2.0.0","digest":"sha256:bbb"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}}
  ]
}`

// TestAOS408_PlanoDeRiscoNaoMaterializaSemAprovacao é metade do par.
func TestAOS408_PlanoDeRiscoNaoMaterializaSemAprovacao(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, aos408SnapshotComPerigo)
	fixPath := filepath.Join(dir, "plano.json")
	escrever(t, fixPath, aos408PlanoQueSeDizSeguro)
	docPendente := filepath.Join(dir, "pendente.json")
	wal := filepath.Join(dir, "es.wal")

	r := correr(t, bin, "serve", "--wal", wal, "--run", "run-aos408-risco",
		"--goal", "recolher e publicar", "--snapshot", snapPath,
		"--decompose-fixture", fixPath, "--plan-out", docPendente, "--worker", "p1")

	if r.code != exitPendenteDeAprovacao {
		t.Fatalf("um plano danger tinha de sair %d (pendente), saiu %d\nstdout:\n%s\nstderr:\n%s",
			exitPendenteDeAprovacao, r.code, r.stdout, r.stderr)
	}
	// A decomposição ACONTECEU: o gate não é uma recusa a montante, é uma barreira a jusante.
	if !strings.Contains(r.stdout, "decomposto: objectivo -> plano de 2 nos") {
		t.Fatalf("o plano tinha de ser decomposto e validado antes de gatar:\n%s", r.stdout)
	}
	// E NADA materializou nem despachou.
	if strings.Contains(r.stdout, "materializado:") {
		t.Fatalf("o plano de risco NAO podia materializar:\n%s", r.stdout)
	}
	if strings.Contains(r.stdout, "despachado:") {
		t.Fatalf("o plano de risco NAO podia despachar:\n%s", r.stdout)
	}
	// O operador tem de saber o que esperar e o que assinar: o nó de risco e o hash.
	if !strings.Contains(r.stdout, "pendente de aprovacao humana:") || !strings.Contains(r.stdout, "publicacao") {
		t.Fatalf("o pendente tinha de nomear o no de risco:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, "plan_hash=sha256:") {
		t.Fatalf("o pendente tinha de trazer o plan_hash:\n%s", r.stdout)
	}
	// O documento pendente ficou em ficheiro (o log só leva o hash — ADR-005).
	if !strings.Contains(r.stdout, "plano pendente escrito:") {
		t.Fatalf("o documento pendente tinha de ser escrito para o humano o rever:\n%s", r.stdout)
	}

	// O grafo durável está VAZIO — o pendente não é «materializado e à espera».
	insp := correr(t, bin, "inspect", "--wal", wal, "--run", "run-aos408-risco")
	if !strings.Contains(insp.stdout, "nos=0") {
		t.Fatalf("nenhum no podia estar no grafo duravel:\n%s", insp.stdout)
	}
}

// TestAOS408_PlanoSemRiscoPassaSemAtrito é a outra metade — a contraprova de não-tautologia. Sem
// ela, um gate que recusasse TODOS os planos passaria o teste de cima.
func TestAOS408_PlanoSemRiscoPassaSemAtrito(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, aos408SnapshotComPerigo) // MESMO snapshot: a tool perigosa existe
	fixPath := filepath.Join(dir, "plano.json")
	escrever(t, fixPath, planoFixtureDuasFolhas) // mas o plano só usa fs.read
	wal := filepath.Join(dir, "es.wal")

	r := correr(t, bin, "serve", "--wal", wal, "--run", "run-aos408-seguro",
		"--goal", "recolher e analisar", "--snapshot", snapPath,
		"--decompose-fixture", fixPath, "--worker", "p1")
	if r.code != exitOK {
		t.Fatalf("um plano sem risco tinha de passar, saiu %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, "gate de plano: APROVADO sem humano") {
		t.Fatalf("o plano passou PELO gate (auto-aprovado), não por um atalho:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, "materializado:") || !strings.Contains(r.stdout, "nos=2") {
		t.Fatalf("o plano sem risco tinha de materializar os 2 nos:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, "nos_despachados=2") {
		t.Fatalf("o plano sem risco tinha de despachar os 2 nos:\n%s", r.stdout)
	}
}
