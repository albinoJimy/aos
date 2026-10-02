package main

// AOS-476 — as dependências do plano ficam no LOG, e é do log que um dono seguinte as recebe.
//
// Medido três vezes antes deste ticket (2026-09-15 e 2026-10-01 no `serve --goal` local, e no
// `consume.wal` de produção): `task.node.created` por nó e ZERO `task.edge.added`. O despacho
// respeitava `analise depends_on recolha` só porque o lia do documento em memória; o `inspect`
// imprimia `ordem=analise,recolha` e um segundo dono re-hidratava dois nós independentes.
//
// Os testes de aceitação correm pelo BINÁRIO compilado, um processo por papel — o ficheiro WAL é a
// única coisa que os processos partilham. Só a contagem de arestas impressa tem teste in-process.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator"
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

// TestAOS476_ArestasDoGrafoConta: a contagem que o `serve` imprime na re-hidratação é a do DAG, e
// não uma constante que por acaso bate com o plano de dois nós do roteiro.
func TestAOS476_ArestasDoGrafoConta(t *testing.T) {
	d := orchestrator.NewDAG("run-conta")
	for _, id := range []string{"a", "b", "c", "d"} {
		if err := d.AddNode(orchestrator.NodeSpec{TaskID: id}); err != nil {
			t.Fatalf("AddNode %s: %v", id, err)
		}
	}
	ordem := func() []string {
		o, err := d.TopoOrder()
		if err != nil {
			t.Fatalf("TopoOrder: %v", err)
		}
		return o
	}
	if n := arestasDoGrafo(d, ordem()); n != 0 {
		t.Fatalf("grafo sem arestas contou %d", n)
	}
	for _, e := range [][2]string{{"a", "b"}, {"a", "c"}, {"b", "d"}, {"c", "d"}} {
		if err := d.AddEdge(e[0], e[1]); err != nil {
			t.Fatalf("AddEdge %v: %v", e, err)
		}
	}
	if n := arestasDoGrafo(d, ordem()); n != 4 {
		t.Fatalf("grafo em losango contou %d arestas, quer 4", n)
	}
}

// aos476Registo é um registo do WAL de ficheiro, já descodificado: o envelope do evento como
// JSON cru, mais o tipo e o stream, para se poder filtrar e reescrever.
type aos476Registo struct {
	tipo, stream string
	env          map[string]json.RawMessage
}

// aos476LerWAL lê o WAL pelo seu enquadramento (uint32 len big-endian, JSON, uint32 crc32 IEEE).
func aos476LerWAL(t *testing.T, path string) []aos476Registo {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ler o WAL: %v", err)
	}
	var regs []aos476Registo
	for i := 0; i < len(raw); {
		n := int(binary.BigEndian.Uint32(raw[i:]))
		var env map[string]json.RawMessage
		if err := json.Unmarshal(raw[i+4:i+4+n], &env); err != nil {
			t.Fatalf("registo ilegível no offset %d: %v", i, err)
		}
		var tipo, stream string
		_ = json.Unmarshal(env["type"], &tipo)
		_ = json.Unmarshal(env["stream_id"], &stream)
		regs = append(regs, aos476Registo{tipo: tipo, stream: stream, env: env})
		i += 8 + n
	}
	return regs
}

// aos476EscreverWAL reescreve o WAL com os registos dados, pelo mesmo enquadramento.
func aos476EscreverWAL(t *testing.T, path string, regs []aos476Registo) {
	t.Helper()
	var out bytes.Buffer
	for _, r := range regs {
		corpo, err := json.Marshal(r.env)
		if err != nil {
			t.Fatalf("serializar registo: %v", err)
		}
		var cab [4]byte
		binary.BigEndian.PutUint32(cab[:], uint32(len(corpo)))
		out.Write(cab[:])
		out.Write(corpo)
		binary.BigEndian.PutUint32(cab[:], crc32.ChecksumIEEE(corpo))
		out.Write(cab[:])
	}
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatalf("escrever o WAL: %v", err)
	}
}

// aos476MorteAMeio reescreve o WAL como se o primeiro dono tivesse morrido a meio da
// materialização: fica tudo o que veio antes do primeiro `task.edge.added` (com, no máximo,
// `nos` dos `task.node.created`) e o `lease.released` dele — o anúncio que permite ao dono
// seguinte entrar sem esperar o TTL, como fez a revisão (registos 0–5 + `lease.released`).
// `mexer`, se não for nil, altera os registos guardados antes de os escrever.
func aos476MorteAMeio(t *testing.T, wal string, nos int, mexer func(*aos476Registo)) {
	t.Helper()
	var guardar []aos476Registo
	var largada *aos476Registo
	criados := 0
	cortado := false
	for _, r := range aos476LerWAL(t, wal) {
		switch {
		case r.tipo == "lease.released" && largada == nil:
			r := r
			largada = &r
		case cortado:
		case r.tipo == "task.edge.added" || r.tipo == "plan.materialized":
			cortado = true
		case r.tipo == "task.node.created":
			if criados == nos {
				cortado = true
				continue
			}
			criados++
			if mexer != nil {
				mexer(&r)
			}
			guardar = append(guardar, r)
		default:
			guardar = append(guardar, r)
		}
	}
	if largada == nil || criados != nos {
		t.Fatalf("WAL inesperado: largada=%v nós guardados=%d (quer %d)", largada != nil, criados, nos)
	}
	aos476EscreverWAL(t, wal, append(guardar, *largada))
}

// aos476PrimeiroDono corre o primeiro dono (`--goal`, com o documento validado escrito em
// `--plan-out`) e devolve os caminhos.
func aos476PrimeiroDono(t *testing.T, bin, run string) (wal, snap, doc string) {
	t.Helper()
	dir := t.TempDir()
	snap = filepath.Join(dir, "snapshot.json")
	escrever(t, snap, aos476Snapshot)
	fix := filepath.Join(dir, "plano.json")
	escrever(t, fix, aos476Plano)
	doc = filepath.Join(dir, "validado.json")
	wal = filepath.Join(dir, "orq.wal")
	r := correr(t, bin, "serve", "--wal", wal, "--run", run, "--goal", "recolher e analisar dados",
		"--snapshot", snap, "--decompose-fixture", fix, "--plan-out", doc, "--worker", "p1", "--release")
	if r.code != exitOK {
		t.Fatalf("primeiro dono saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	return wal, snap, doc
}

// TestAOS476_MaterializacaoMortaAMeioRetoma (MÉDIO-1 da revisão), por PROCESSO real: o primeiro
// dono morre depois de escrever os nós (os dois, ou só o primeiro) e antes da aresta e do
// `plan.materialized`. A retoma (`serve --plan-doc`, a via do `consume`) re-hidrata os nós SEM
// aresta, aceita os que coincidem com o documento, escreve o que falta e chega ao despacho.
// Antes falhava com «nó já existe no grafo» e saída 1 — que o `consume` retentava até ao tecto de
// gerações.
func TestAOS476_MaterializacaoMortaAMeioRetoma(t *testing.T) {
	bin := construir(t)
	for _, nos := range []int{2, 1} {
		t.Run(map[int]string{2: "depois-dos-nos", 1: "entre-os-nos"}[nos], func(t *testing.T) {
			const run = "run-aos476-morte"
			wal, snap, doc := aos476PrimeiroDono(t, bin, run)
			aos476MorteAMeio(t, wal, nos, nil)

			r := correr(t, bin, "serve", "--wal", wal, "--run", run, "--plan-doc", doc, "--snapshot", snap,
				"--worker", "p2", "--release")
			if r.code != exitOK {
				t.Fatalf("a retoma de uma materialização morta a meio saiu %d (o run ficava irrecuperável)\n%s\n%s", r.code, r.stdout, r.stderr)
			}
			for _, quer := range []string{"grafo re-hidratado: arestas=0", "materializado: plano=" + run + "-plan nos=2", "despachado: plano=" + run + "-plan nos_despachados=1"} {
				if !strings.Contains(r.stdout, quer) {
					t.Fatalf("falta %q na retoma:\n%s", quer, r.stdout)
				}
			}
			raw, err := os.ReadFile(wal)
			if err != nil {
				t.Fatalf("ler o WAL: %v", err)
			}
			if n, a, m := len(aos476Tipo(raw, "task.node.created")), len(aos476Tipo(raw, "task.edge.added")), len(aos476Tipo(raw, "plan.materialized")); n != 2 || a != 1 || m != 1 {
				t.Fatalf("WAL depois da retoma: task.node.created=%d task.edge.added=%d plan.materialized=%d, quer 2/1/1 (nenhum nó reescrito)", n, a, m)
			}
			insp := correr(t, bin, "inspect", "--wal", wal, "--run", run)
			if !strings.Contains(insp.stdout, "ordem=recolha,analise") {
				t.Fatalf("depois da retoma o grafo não ordena recolha,analise:\n%s", insp.stdout)
			}
		})
	}
}

// TestAOS476_MaterializacaoMortaComNoDivergenteRecusa: se o nó que ficou no log NÃO é o do
// documento (aqui, `analise` com outra tool), a retoma recusa com a saída DETERMINISTA do
// documento (10) — não com 1, que seria retentado — e não escreve nada.
func TestAOS476_MaterializacaoMortaComNoDivergenteRecusa(t *testing.T) {
	bin := construir(t)
	const run = "run-aos476-divergente"
	wal, snap, doc := aos476PrimeiroDono(t, bin, run)
	aos476MorteAMeio(t, wal, 2, func(r *aos476Registo) {
		var p map[string]any
		_ = json.Unmarshal(r.env["payload"], &p)
		if p["task_id"] != "analise" {
			return
		}
		p["tool_id"], p["capability"] = "http.post", "cap:tool:http.post"
		r.env["payload"], _ = json.Marshal(p)
	})
	antes, err := os.ReadFile(wal)
	if err != nil {
		t.Fatalf("ler o WAL: %v", err)
	}

	r := correr(t, bin, "serve", "--wal", wal, "--run", run, "--plan-doc", doc, "--snapshot", snap,
		"--worker", "p2", "--release")
	if r.code != exitDocumentoRecusado || !strings.Contains(r.stderr, "diverge") || !strings.Contains(r.stderr, `"analise"`) {
		t.Fatalf("um nó divergente tinha de ser recusado com %d e dizê-lo, saiu %d\n%s\n%s", exitDocumentoRecusado, r.code, r.stdout, r.stderr)
	}
	depois, err := os.ReadFile(wal)
	if err != nil {
		t.Fatalf("ler o WAL: %v", err)
	}
	for _, tipo := range []string{"task.node.created", "task.edge.added", "plan.materialized"} {
		if a, d := len(aos476Tipo(antes, tipo)), len(aos476Tipo(depois, tipo)); a != d {
			t.Fatalf("a retoma recusada escreveu %s: %d → %d", tipo, a, d)
		}
	}
}
