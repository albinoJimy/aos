package main

// AOS-415 — pelo processo real: uma decomposição recusada pela validação (AOS-231) gera nova
// tentativa, e o plano da 2.ª tentativa materializa. FALHA-ANTES: o `serve` saía com 1 à primeira
// recusa — foi o que aconteceu nas duas validações em produção (AOS-412 e AOS-414).

import (
	"path/filepath"
	"strings"
	"testing"
)

// aos415PlanoRecusado é admissível no DECODE mas recusado pelo VALIDADOR: um nó com autoridade
// privilegiada (a tool de efeito) a consumir um payload untrusted de um nó que não é verificador
// — `consumes_taint_authority`, a MESMA razão das duas recusas em produção.
const aos415PlanoRecusado = `{
  "plan_version": "1.2.0",
  "objective": "ler e publicar",
  "budget_total": {"tokens": 100, "cost_micro_usd": 100},
  "planner_meta": {"model":"fixture","prompt_version":"1.2.0","capabilities_hash":"sha256:snap-aos408"},
  "nodes": [
    {"node_id":"leitura","role":"worker","objective":"ler","depends_on":[],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10},
     "outputs":[{"name":"conteudo","type":"record","taint":"untrusted"}]},
    {"node_id":"publicacao","role":"worker","objective":"publicar","depends_on":["leitura"],
     "tools":[{"name":"http.post","version":"2.0.0","digest":"sha256:bbb"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10},
     "consumes":[{"from":"leitura","output":"conteudo","type":"record"}]}
  ]
}`

func TestAOS415_RecusaDaValidacaoGeraNovaTentativaNoBinario(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, aos408SnapshotComPerigo)
	recusado := filepath.Join(dir, "recusado.json")
	escrever(t, recusado, aos415PlanoRecusado)
	valido := filepath.Join(dir, "valido.json")
	escrever(t, valido, planoFixtureDuasFolhasComSnapshotAOS408)
	wal := filepath.Join(dir, "es.wal")

	r := correr(t, bin, "serve", "--wal", wal, "--run", "run-aos415-recupera", "--goal", "ler-e-publicar",
		"--snapshot", snapPath, "--decompose-fixture", recusado+","+valido, "--worker", "p1")
	if r.code != exitOK {
		t.Fatalf("a 2.ª tentativa tinha de materializar, saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, "materializado:") {
		t.Fatalf("o plano da 2.ª tentativa tinha de materializar:\n%s", r.stdout)
	}
	// O planeador registou que houve DUAS tentativas — a 1.ª recusada pelo validador.
	if !strings.Contains(r.stdout, "tentativas=2") {
		t.Fatalf("a saída tinha de declarar as 2 tentativas:\n%s", r.stdout)
	}
}

// Esgotado o tecto, o `serve` recusa — e LARGA a posse, para a invocação seguinte com o mesmo
// run não sair com 3 («lease detido»), que foi o que se passou na validação do AOS-414.
func TestAOS415_TectoEsgotadoLargaAPosse(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, aos408SnapshotComPerigo)
	recusado := filepath.Join(dir, "recusado.json")
	escrever(t, recusado, aos415PlanoRecusado)
	valido := filepath.Join(dir, "valido.json")
	escrever(t, valido, planoFixtureDuasFolhasComSnapshotAOS408)
	wal := filepath.Join(dir, "es.wal")
	const run = "run-aos415-tecto"

	r := correr(t, bin, "serve", "--wal", wal, "--run", run, "--goal", "ler-e-publicar",
		"--snapshot", snapPath, "--decompose-fixture", recusado, "--worker", "p1")
	if r.code != exitPlanoRecusado {
		t.Fatalf("esgotadas as tentativas, a saída tinha de ser %d (recusa que LARGA a posse), foi %d\n%s\n%s",
			exitPlanoRecusado, r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stderr, "consumes_taint_authority") {
		t.Fatalf("a recusa tinha de trazer a razão do validador:\n%s", r.stderr)
	}
	// A posse foi largada: o serve seguinte com o MESMO run toma-a (e agora decompõe bem).
	r2 := correr(t, bin, "serve", "--wal", wal, "--run", run, "--goal", "ler-e-publicar",
		"--snapshot", snapPath, "--decompose-fixture", valido, "--worker", "p2")
	if r2.code != exitOK {
		t.Fatalf("a invocação seguinte tinha de tomar a posse e materializar, saiu %d\n%s\n%s", r2.code, r2.stdout, r2.stderr)
	}
}
