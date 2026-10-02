package main

// AOS-476 — as dependências do plano ficam no LOG, e é do log que um dono seguinte as recebe.
//
// Medido três vezes antes deste ticket (2026-09-15 e 2026-10-01 no `serve --goal` local, e no
// `consume.wal` de produção): `task.node.created` por nó e ZERO `task.edge.added`. O despacho
// respeitava `analise depends_on recolha` só porque o lia do documento em memória; o `inspect`
// imprimia `ordem=analise,recolha` e um segundo dono re-hidratava dois nós independentes.
//
// Tudo aqui corre pelo BINÁRIO compilado, um processo por papel — o ficheiro WAL é a única coisa
// que os processos partilham.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// aos476Snapshot e aos476Plano são os ficheiros de entrada do passo 15 do roteiro E2E
// (`docs/testing/e2e-pegadas-visao-19.md`), tal e qual: `analise` depende de `recolha`.
const aos476Snapshot = `{
  "hash": "sha256:snap-e2e",
  "tools": [
    {"name":"fs.read","version":"1.0.0","digest":"sha256:aaa","admissible":true,
     "sensitivity":"public","egress":"none","reversibility":"reversible","mutation":"none"},
    {"name":"http.post","version":"2.0.0","digest":"sha256:bbb","admissible":true,
     "sensitivity":"public","egress":"external","reversibility":"reversible","mutation":"mutates"}
  ]
}`

const aos476Plano = `{
  "plan_version": "1.0.0",
  "objective": "recolher e analisar",
  "budget_total": {"tokens": 100, "cost_micro_usd": 100},
  "planner_meta": {"model":"FORJADO","prompt_version":"9.9.9","capabilities_hash":"sha256:FORJADO"},
  "nodes": [
    {"node_id":"recolha","role":"worker","objective":"recolher","depends_on":[],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}},
    {"node_id":"analise","role":"worker","objective":"analisar","depends_on":["recolha"],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}}
  ]
}`

// aos476Tipo devolve a posição, no ficheiro WAL, de cada ocorrência de um tipo de evento. O WAL é
// apensado por ordem de escrita, pelo que a posição no ficheiro É a ordem de escrita — entre
// streams diferentes também (o grafo vive no stream do run, `plan.materialized` no do plano).
func aos476Tipo(raw []byte, tipo string) []int {
	agulha := []byte(`"type":"` + tipo + `"`)
	var pos []int
	for desde := 0; ; {
		i := bytes.Index(raw[desde:], agulha)
		if i < 0 {
			return pos
		}
		pos = append(pos, desde+i)
		desde += i + len(agulha)
	}
}

// TestAOS476_ArestaNoLogEDonoSeguinteRehidrataComEla cobre as duas vias que materializam
// (`--goal` e `--plan-doc`):
//
//   - o WAL fica com UM `task.edge.added`, escrito depois dos dois `task.node.created` e antes do
//     `plan.materialized` (AC1, visto no ficheiro e não num duplo);
//   - o `inspect` — um processo que só tem o log — ordena `recolha,analise` (AC3);
//   - um SEGUNDO DONO, `serve` sem `--goal` nem `--plan-doc`, noutro processo, re-hidrata os dois
//     nós E a aresta (AC4).
func TestAOS476_ArestaNoLogEDonoSeguinteRehidrataComEla(t *testing.T) {
	bin := construir(t)
	// O `--goal` carimba a `planner_meta` (a forjada do roteiro não sobrevive); o `--plan-doc`
	// recebe o documento já com a do snapshot pinado, como o `--plan-out` o escreveria.
	docDoSnapshot := strings.Replace(aos476Plano, `"model":"FORJADO","prompt_version":"9.9.9","capabilities_hash":"sha256:FORJADO"`,
		`"model":"x","prompt_version":"1.0.0","capabilities_hash":"sha256:snap-e2e"`, 1)
	casos := []struct {
		nome  string
		plano string
		args  func(snap, doc string) []string
	}{
		{"goal", aos476Plano, func(snap, doc string) []string {
			return []string{"--goal", "recolher e analisar dados", "--snapshot", snap, "--decompose-fixture", doc}
		}},
		{"plan-doc", docDoSnapshot, func(snap, doc string) []string {
			return []string{"--plan-doc", doc, "--snapshot", snap}
		}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			dir := t.TempDir()
			snapPath := filepath.Join(dir, "snapshot.json")
			escrever(t, snapPath, aos476Snapshot)
			docPath := filepath.Join(dir, "plano.json")
			escrever(t, docPath, c.plano)
			wal := filepath.Join(dir, "orq.wal")
			const run = "run-aos476"

			args := append([]string{"serve", "--wal", wal, "--run", run, "--worker", "p1", "--release"}, c.args(snapPath, docPath)...)
			r := correr(t, bin, args...)
			if r.code != exitOK || !strings.Contains(r.stdout, "nos=2") {
				t.Fatalf("o primeiro dono não materializou os 2 nós (saiu %d)\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
			}

			raw, err := os.ReadFile(wal)
			if err != nil {
				t.Fatalf("ler o WAL: %v", err)
			}
			nos := aos476Tipo(raw, "task.node.created")
			arestas := aos476Tipo(raw, "task.edge.added")
			mat := aos476Tipo(raw, "plan.materialized")
			if len(nos) != 2 || len(arestas) != 1 || len(mat) != 1 {
				t.Fatalf("WAL com task.node.created=%d task.edge.added=%d plan.materialized=%d, quer 2/1/1 — sem a aresta, o grafo durável diz que os nós são independentes",
					len(nos), len(arestas), len(mat))
			}
			if !(nos[1] < arestas[0] && arestas[0] < mat[0]) {
				t.Fatalf("ordem no WAL: nós em %v, aresta em %d, plan.materialized em %d — a aresta tem de vir depois dos nós e antes de plan.materialized", nos, arestas[0], mat[0])
			}
			if !bytes.Contains(raw, []byte(`"payload":{"run_id":"`+run+`","from":"recolha","to":"analise"}`)) {
				t.Fatal("o task.edge.added não é recolha→analise")
			}

			insp := correr(t, bin, "inspect", "--wal", wal, "--run", run)
			if insp.code != exitOK || !strings.Contains(insp.stdout, "ordem=recolha,analise") {
				t.Fatalf("o inspect não vê a dependência (esperava ordem=recolha,analise), saiu %d:\n%s\n%s", insp.code, insp.stdout, insp.stderr)
			}

			r2 := correr(t, bin, "serve", "--wal", wal, "--run", run, "--worker", "p2", "--release")
			if r2.code != exitOK {
				t.Fatalf("o segundo dono saiu %d\nstdout:\n%s\nstderr:\n%s", r2.code, r2.stdout, r2.stderr)
			}
			for _, quer := range []string{"token=2", "grafo re-hidratado: nos=2", "grafo re-hidratado: arestas=1 ordem=recolha,analise"} {
				if !strings.Contains(r2.stdout, quer) {
					t.Fatalf("o segundo dono não re-hidratou o grafo com a aresta (falta %q):\n%s", quer, r2.stdout)
				}
			}
		})
	}
}

// TestAOS476_DonoSeguinteDespachaSoPelaRetoma re-mede, na base com o executor de nós (AOS-413) e
// a retoma por documento (AOS-442), a segunda metade do PR #300: «nenhum dono seguinte despacha
// nada». Três processos sobre o mesmo WAL, contra um nó `aos` falso:
//
//  1. `serve --goal --plan-out` despacha `recolha`, que fica a correr além do prazo (sai 8);
//  2. `serve` SEM documento re-hidrata o grafo com a aresta e NÃO despacha nada — não tem com quê:
//     o despacho precisa do `PlanDocument` (predicados de `conditional_on`, `risk_class`, tools),
//     e o log só leva o hash dele (ADR-005);
//  3. `serve --plan-doc` (a via que o `consume` usa numa retoma, AOS-442) retoma do log, recolhe
//     `recolha` e despacha `analise` — o plano chega ao fim.
//
// O que fica medido: a afirmação do PR #300 deixou de ser verdade para a via de retoma, e continua
// verdade só para o `serve` sem documento, que é por desenho.
func TestAOS476_DonoSeguinteDespachaSoPelaRetoma(t *testing.T) {
	f := &aos413No{nuncaAcaba: true}
	srv := f.servidor(t)
	bin := construir(t)
	dir := t.TempDir()
	cred := filepath.Join(dir, "nhi.jwt")
	escrever(t, cred, "nhi-do-operador")
	env := []string{"AOS_ORQ_NODE_URL=" + srv.URL, "AOS_ORQ_NODE_CREDENTIAL_FILE=" + cred, "AOS_MODE="}
	snapPath := filepath.Join(dir, "snapshot.json")
	// O snapshot é o do catálogo do nó falso (AOS-441 confere-os antes da posse).
	escrever(t, snapPath, aos408SnapshotComPerigo)
	fix := filepath.Join(dir, "plano.json")
	escrever(t, fix, aos476Plano)
	doc := filepath.Join(dir, "validado.json")
	wal := filepath.Join(dir, "orq.wal")
	const run = "run-aos476-retoma"

	r1 := correrComEnv(t, env, bin, "serve", "--wal", wal, "--run", run, "--goal", "recolher e analisar dados",
		"--snapshot", snapPath, "--decompose-fixture", fix, "--plan-out", doc, "--worker", "p1",
		"--poll-interval", "20ms", "--plan-timeout", "300ms")
	if r1.code != exitNosEmVoo {
		t.Fatalf("com `recolha` a correr além do prazo o primeiro dono tinha de sair %d, saiu %d\n%s\n%s", exitNosEmVoo, r1.code, r1.stdout, r1.stderr)
	}
	if ids := f.submetidos(); len(ids) != 1 || ids[0] != run+"~recolha" {
		t.Fatalf("o primeiro dono devia ter submetido só `recolha` (analise espera por ela), submeteu %v", ids)
	}

	f.mu.Lock()
	f.nuncaAcaba = false
	f.mu.Unlock()

	r2 := correrComEnv(t, env, bin, "serve", "--wal", wal, "--run", run, "--worker", "p2", "--release")
	if r2.code != exitOK || !strings.Contains(r2.stdout, "grafo re-hidratado: arestas=1 ordem=recolha,analise") {
		t.Fatalf("o segundo dono não re-hidratou o grafo com a aresta (saiu %d)\n%s\n%s", r2.code, r2.stdout, r2.stderr)
	}
	if strings.Contains(r2.stdout, "despachado:") || len(f.submetidos()) != 1 {
		t.Fatalf("o `serve` sem documento não tem com que despachar e despachou: %v\n%s", f.submetidos(), r2.stdout)
	}

	r3 := correrComEnv(t, env, bin, "serve", "--wal", wal, "--run", run, "--plan-doc", doc, "--snapshot", snapPath,
		"--worker", "p3", "--poll-interval", "20ms")
	if r3.code != exitOK || !strings.Contains(r3.stdout, "materializado (retoma, do log)") ||
		!strings.Contains(r3.stdout, "execucao: analise=complete recolha=complete") {
		t.Fatalf("a retoma por documento tinha de levar o plano ao fim (saiu %d)\n%s\n%s", r3.code, r3.stdout, r3.stderr)
	}
	if ids := f.submetidos(); len(ids) != 2 || ids[0] != run+"~analise" {
		t.Fatalf("a retoma devia ter submetido `analise` uma vez e não repetir `recolha`, submetidos=%v", ids)
	}
}
