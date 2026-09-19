package main

// AOS-412 — com o modelo vivo, um plano de risco APROVADO passa a ter por onde correr.
//
// O modelo vivo não é determinístico: aprovado o organigrama H1, repetir o `serve --goal`
// re-decompõe e produz H2. Simula-se isso com DOIS fixtures diferentes para o mesmo run. Antes do
// AOS-412 não havia saída:
//   - o `--goal` repetido deixava H2 «pendente», mas o `plan_id` já tinha decisão terminal e o
//     `decide` recusava-o — um beco;
//   - o `--plan-doc` com o H1 aprovado admitia os nós e PARAVA (sem despacho, com um token de
//     faz-de-conta, e sem sequer validar o documento).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// aos412PlanoRedecomposto é o que o modelo devolveria na segunda decomposição: o mesmo objectivo,
// outro organigrama (outro hash).
var aos412PlanoRedecomposto = strings.Replace(aos408PlanoRiscoIndependente,
	`"objective": "recolher e publicar",`, `"objective": "recolher e publicar (segunda decomposicao)",`, 1)

// aos412Aprovado corre a cerimónia sobre o H1 e devolve (wal, snap, doc aprovado).
func aos412Aprovado(t *testing.T, bin, dir, run string) (string, string, string) {
	t.Helper()
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, aos408SnapshotComPerigo)
	fixH1 := filepath.Join(dir, "h1.json")
	escrever(t, fixH1, aos408PlanoRiscoIndependente)
	doc := filepath.Join(dir, "aprovado.json")
	wal := filepath.Join(dir, "es.wal")

	r := correr(t, bin, "serve", "--wal", wal, "--run", run, "--goal", "recolher-e-publicar",
		"--snapshot", snapPath, "--decompose-fixture", fixH1, "--plan-out", doc, "--worker", "p1")
	if r.code != exitPendenteDeAprovacao {
		t.Fatalf("H1 tinha de ficar pendente, saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	pl := correr(t, bin, "plans", "--wal", wal, "--run", run)
	m := reRequestID.FindStringSubmatch(pl.stdout)
	if m == nil {
		t.Fatalf("sem request_id:\n%s", pl.stdout)
	}
	priv, aprovadores := aos408Aprovador(t, dir, "human:alice")
	aprovacao := aos408Assinar(t, dir, "aprovacao.json", m[1], "human:alice", priv, true)
	d := correr(t, bin, "decide", "--wal", wal, "--run", run, "--plan-doc", doc, "--snapshot", snapPath,
		"--decision", "approve", "--approval", aprovacao, "--approvers", aprovadores)
	if d.code != exitOK {
		t.Fatalf("a aprovação de H1 falhou: %d\n%s\n%s", d.code, d.stdout, d.stderr)
	}
	return wal, snapPath, doc
}

// TestAOS412_ComModeloVivoOPlanoAprovadoCorrePeloPlanDoc é o caso para que o gate existe, com o
// planeador real: aprovar um organigrama de risco e VÊ-LO correr.
func TestAOS412_ComModeloVivoOPlanoAprovadoCorrePeloPlanDoc(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	const run = "run-aos412-vivo"
	wal, snapPath, doc := aos412Aprovado(t, bin, dir, run)

	// (1) O modelo vivo re-decompõe: H2 ≠ H1. FALHA-ANTES: saía 6 («pendente») num plano que já
	// tinha decisão terminal — indecidível. Agora recusa e diz qual é o caminho.
	fixH2 := filepath.Join(dir, "h2.json")
	escrever(t, fixH2, aos412PlanoRedecomposto)
	r2 := correr(t, bin, "serve", "--wal", wal, "--run", run, "--goal", "recolher-e-publicar",
		"--snapshot", snapPath, "--decompose-fixture", fixH2, "--worker", "p2")
	if r2.code != exitDecisaoRecusada {
		t.Fatalf("H2 num plano já decidido tinha de sair %d (não pendente), saiu %d\n%s\n%s",
			exitDecisaoRecusada, r2.code, r2.stdout, r2.stderr)
	}
	if !strings.Contains(r2.stderr, "--plan-doc") {
		t.Fatalf("a recusa tinha de indicar o caminho do plano aprovado (--plan-doc):\n%s", r2.stderr)
	}
	if strings.Contains(r2.stdout, "materializado:") {
		t.Fatalf("o organigrama NÃO aprovado não pode materializar:\n%s", r2.stdout)
	}

	// (2) O organigrama APROVADO corre pelo --plan-doc: gate reconhecido, materializado e
	// DESPACHADO — incluindo o nó de risco. FALHA-ANTES: o --plan-doc parava na admissão.
	r3 := correr(t, bin, "serve", "--wal", wal, "--run", run, "--plan-doc", doc, "--snapshot", snapPath, "--worker", "p3")
	if r3.code != exitOK {
		t.Fatalf("o plano aprovado tinha de correr pelo --plan-doc, saiu %d\n%s\n%s", r3.code, r3.stdout, r3.stderr)
	}
	for _, quer := range []string{"gate de plano: APROVADO por humano", "materializado:", "folha publicacao a arrancar", "nos_despachados=2"} {
		if !strings.Contains(r3.stdout, quer) {
			t.Fatalf("faltou %q na execução do plano aprovado:\n%s", quer, r3.stdout)
		}
	}
}

// TestAOS412_PlanDocValidaAEstrutura: o documento de um ficheiro é untrusted como o do modelo.
// FALHA-ANTES: o --plan-doc não corria a regra AOS-231.
func TestAOS412_PlanDocValidaAEstrutura(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, aos408SnapshotComPerigo)
	// Uma tool que o snapshot pinado não tem.
	docPath := filepath.Join(dir, "plano.json")
	escrever(t, docPath, aos412PlanoComToolDesconhecida)
	wal := filepath.Join(dir, "es.wal")

	r := correr(t, bin, "serve", "--wal", wal, "--run", "run-aos412-estrutura", "--plan-doc", docPath, "--snapshot", snapPath, "--worker", "p1")
	if r.code == exitOK || strings.Contains(r.stdout, "materializado:") {
		t.Fatalf("uma tool fora do snapshot tinha de ser recusada: saiu %d\n%s", r.code, r.stdout)
	}
	if !strings.Contains(r.stderr, "AOS-231") {
		t.Fatalf("a recusa tinha de ser da validação estrutural (AOS-231):\n%s", r.stderr)
	}
}

// TestAOS412_PlanDocSemRiscoAutoAprovaEDespacha: um plano sem risco por --plan-doc passa pelo
// MESMO gate (auto-aprova, com os factos no log) e corre.
func TestAOS412_PlanDocSemRiscoAutoAprovaEDespacha(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, aos408SnapshotComPerigo)
	docPath := filepath.Join(dir, "plano.json")
	escrever(t, docPath, planoFixtureDuasFolhasComSnapshotAOS408)
	wal := filepath.Join(dir, "es.wal")

	r := correr(t, bin, "serve", "--wal", wal, "--run", "run-aos412-seguro", "--plan-doc", docPath, "--snapshot", snapPath, "--worker", "p1")
	if r.code != exitOK {
		t.Fatalf("um plano sem risco tinha de correr, saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, "gate de plano: APROVADO sem humano") || !strings.Contains(r.stdout, "nos_despachados=2") {
		t.Fatalf("tinha de passar pelo gate e despachar os 2 nós:\n%s", r.stdout)
	}
	pl := correr(t, bin, "plans", "--wal", wal, "--run", "run-aos412-seguro")
	if !strings.Contains(pl.stdout, "decisao=approved") {
		t.Fatalf("a auto-aprovação tinha de ficar como facto no log:\n%s", pl.stdout)
	}
}

// aos412PlanoComToolDesconhecida referencia uma tool que o snapshot pinado não declara.
const aos412PlanoComToolDesconhecida = `{
  "plan_version": "1.0.0",
  "objective": "executar",
  "budget_total": {"tokens": 100, "cost_micro_usd": 100},
  "planner_meta": {"model":"fixture","prompt_version":"1.2.0","capabilities_hash":"sha256:snap-aos408"},
  "nodes": [
    {"node_id":"execucao","role":"worker","objective":"executar","depends_on":[],
     "tools":[{"name":"shell.exec","version":"9.9.9","digest":"sha256:zzz"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}}
  ]
}`

// TestAOS412_SegundoOrganigramaSobrePendenteRecusa: com o modelo vivo, repetir o `--goal` ANTES
// da decisão produz outro organigrama. O `plan.validated` é de primeira-escrita e o `decide`
// ancora no primeiro hash — o segundo não seria decidível. FALHA-ANTES: saía 6 («pendente») e
// reescrevia o `--plan-out`, perdendo o documento do plano que era decidível.
func TestAOS412_SegundoOrganigramaSobrePendenteRecusa(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	const run = "run-aos412-duplo-pendente"
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, aos408SnapshotComPerigo)
	fixH1 := filepath.Join(dir, "h1.json")
	escrever(t, fixH1, aos408PlanoRiscoIndependente)
	fixH2 := filepath.Join(dir, "h2.json")
	escrever(t, fixH2, aos412PlanoRedecomposto)
	doc := filepath.Join(dir, "pendente.json")
	wal := filepath.Join(dir, "es.wal")

	r1 := correr(t, bin, "serve", "--wal", wal, "--run", run, "--goal", "recolher-e-publicar",
		"--snapshot", snapPath, "--decompose-fixture", fixH1, "--plan-out", doc, "--worker", "p1")
	if r1.code != exitPendenteDeAprovacao {
		t.Fatalf("H1 tinha de ficar pendente, saiu %d\n%s\n%s", r1.code, r1.stdout, r1.stderr)
	}
	pendente := ler(t, doc)

	r2 := correr(t, bin, "serve", "--wal", wal, "--run", run, "--goal", "recolher-e-publicar",
		"--snapshot", snapPath, "--decompose-fixture", fixH2, "--plan-out", doc, "--worker", "p2")
	if r2.code != exitDecisaoRecusada {
		t.Fatalf("H2 sobre o H1 pendente tinha de sair %d (não outro pendente), saiu %d\n%s\n%s",
			exitDecisaoRecusada, r2.code, r2.stdout, r2.stderr)
	}
	if !strings.Contains(r2.stderr, "--plan-doc") {
		t.Fatalf("a recusa tinha de indicar o caminho do documento pendente:\n%s", r2.stderr)
	}
	if ler(t, doc) != pendente {
		t.Fatal("o documento pendente do H1 foi reescrito pelo H2 — o plano decidível perdeu-se")
	}
}

// TestAOS412_AprovacaoReutilizadaExigeOSnapshotSelado: um humano aprova o H1 de risco sob o
// catálogo S; o `--plan-doc` com um S' que copia o rótulo e dá eixos benignos à tool perigosa
// deixa de ver nós de risco e caía no ramo «ja APROVADO» sem verificar o catálogo. FALHA-ANTES:
// materializava e despachava com o risco, as capabilities e o cartão derivados de S'.
func TestAOS412_AprovacaoReutilizadaExigeOSnapshotSelado(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	const run = "run-aos412-snap-benigno"
	wal, _, doc := aos412Aprovado(t, bin, dir, run)

	benigno := filepath.Join(dir, "snap-benigno.json")
	escrever(t, benigno, strings.Replace(aos408SnapshotComPerigo,
		`"sensitivity":"sensitive","egress":"external","reversibility":"irreversible"`,
		`"sensitivity":"public","egress":"none","reversibility":"reversible"`, 1))

	r := correr(t, bin, "serve", "--wal", wal, "--run", run, "--plan-doc", doc, "--snapshot", benigno, "--worker", "p2")
	if r.code == exitOK || strings.Contains(r.stdout, "materializado:") {
		t.Fatalf("um catálogo que não é o selado tinha de ser recusado, saiu %d\n%s", r.code, r.stdout)
	}
	if !strings.Contains(r.stderr, "nao e o selado") {
		t.Fatalf("a recusa tinha de ser a do snapshot selado:\n%s", r.stderr)
	}
}

func ler(t *testing.T, caminho string) string {
	t.Helper()
	b, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatalf("ler %s: %v", caminho, err)
	}
	return string(b)
}
