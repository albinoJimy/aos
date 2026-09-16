package main

// AOS-395 — PROCESSO REAL. Corre o binário com o Model Gateway vivo (upstream OpenAI-compatível
// em httptest) e AOS_MODEL_AUDIT_PATH definido, e depois de o processo TERMINAR relê o selo do
// ficheiro: é isso que prova a durabilidade (com o MemStore anterior não havia ficheiro nenhum) e
// a correlação com o run e a tentativa.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	audit "github.com/aos-ref/platform/audit"
	eventstore "github.com/aos-ref/substrate/eventstore"
)

// planoDoModelo é o PlanDocument que o "LLM" devolve: duas folhas com a tool do snapshot.
const planoDoModelo = `{
  "plan_version": "1.0.0",
  "objective": "recolher e analisar",
  "budget_total": {"tokens": 100, "cost_micro_usd": 100},
  "planner_meta": {"model":"x","prompt_version":"1.0.0","capabilities_hash":"sha256:snap-goal"},
  "nodes": [
    {"node_id":"recolha","role":"worker","objective":"recolher","depends_on":[],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}},
    {"node_id":"analise","role":"worker","objective":"analisar","depends_on":[],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}}
  ]
}`

// upstreamComPlano devolve o wire OpenAI com o plano acima como conteúdo da resposta.
func upstreamComPlano(t *testing.T) *httptest.Server {
	t.Helper()
	conteudo, err := json.Marshal(planoDoModelo)
	if err != nil {
		t.Fatalf("json.Marshal do plano: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"cmpl-1","object":"chat.completion","model":"gpt-4o",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":%s},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`, conteudo)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// selosDoGateway relê a partição de governação do WORM em ficheiro, DEPOIS de o processo sair.
func selosDoGateway(t *testing.T, path, particao string) []audit.AuditRecord {
	t.Helper()
	st, err := audit.OpenFileStoreReadOnly(path)
	if err != nil {
		t.Fatalf("OpenFileStoreReadOnly(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	head, err := st.Head(ctx, particao)
	if err != nil {
		t.Fatalf("Head(%s): %v", particao, err)
	}
	var out []audit.AuditRecord
	for i := uint64(1); i <= head; i++ {
		rec, ok, err := st.At(ctx, particao, i)
		if err != nil || !ok {
			t.Fatalf("At(%s,%d): ok=%v err=%v", particao, i, ok, err)
		}
		out = append(out, rec)
	}
	return out
}

// TestAOS395_ProcessoReal_SeloDuravelComRunEPasso: o selo da chamada de decomposição sobrevive ao
// fim do processo e identifica o run e a tentativa.
func TestAOS395_ProcessoReal_SeloDuravelComRunEPasso(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, snapshotDuasTools)
	wal := filepath.Join(dir, "es.wal")
	auditPath := filepath.Join(dir, "model-audit.wal")

	// A credencial de infra é exigida por este caminho (o `staticCredencialModelo` do aos-orq
	// não tem seam de dev com segredo vazio): material por FICHEIRO, nunca por env.
	keyPath := filepath.Join(dir, "model.key")
	escrever(t, keyPath, "sk-teste-aos395")

	srv := upstreamComPlano(t)
	t.Setenv("AOS_MODEL_ENDPOINT", srv.URL)
	t.Setenv("AOS_MODEL_NAME", "gpt-4o")
	t.Setenv("AOS_MODEL_REGION", "eu")
	t.Setenv("AOS_MODEL_BOARD", "board-eu")
	t.Setenv("AOS_MODEL_API_KEY_PATH", keyPath)
	t.Setenv("AOS_MODEL_AUDIT_PATH", auditPath)

	const runID = "run-aos395"
	r := correr(t, bin, "serve", "--wal", wal, "--run", runID,
		"--goal", "recolher e analisar dados", "--snapshot", snapPath, "--worker", "p1")
	if r.code != 0 {
		t.Fatalf("serve --goal com gateway vivo saiu %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, "decomposto: objectivo -> plano de 2 nos") {
		t.Fatalf("o gateway vivo nao decompos em 2 nos:\n%s", r.stdout)
	}
	// A postura do audit é declarada, com o caminho.
	if !strings.Contains(r.stdout, "audit de governacao do model gateway (AOS-395): DURAVEL") {
		t.Fatalf("a postura DURAVEL tinha de ser declarada no arranque:\n%s", r.stdout)
	}

	// O selo sobrevive ao processo: relê-se do FICHEIRO, já sem o processo a correr.
	selos := selosDoGateway(t, auditPath, "modelgw-gov:board-eu")
	if len(selos) == 0 {
		t.Fatal("nenhum selo de governação no WORM durável — a durabilidade é o ponto deste teste")
	}
	var daDecomposicao int
	for _, s := range selos {
		if s.Capability != "model:invoke" {
			continue // o changelog de activação da allowlist vive noutra partição, mas defende-se
		}
		daDecomposicao++
		if s.RunID != runID || s.StepID != "planstep:decompose:1" {
			t.Fatalf("selo com RunID=%q StepID=%q; quero %s/planstep:decompose:1", s.RunID, s.StepID, runID)
		}
	}
	if daDecomposicao == 0 {
		t.Fatalf("nenhum selo model:invoke na particao; selos=%d", len(selos))
	}
}

// TestAOS395_ProcessoReal_AuditMalConfiguradoAbortaSemPosse: um AOS_MODEL_AUDIT_PATH que não abre
// é fail-closed de config — o processo sai != 0 e NÃO chega a materializar nada.
func TestAOS395_ProcessoReal_AuditMalConfiguradoAbortaSemPosse(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, snapshotDuasTools)
	wal := filepath.Join(dir, "es.wal")

	srv := upstreamComPlano(t)
	t.Setenv("AOS_MODEL_ENDPOINT", srv.URL)
	t.Setenv("AOS_MODEL_NAME", "gpt-4o")
	// Caminho impossível: o "directório" pai é um ficheiro regular, pelo que nem a posse (que
	// cria os pais) nem o FileStore o conseguem abrir.
	naoPasta := filepath.Join(dir, "sou-um-ficheiro")
	escrever(t, naoPasta, "x")
	t.Setenv("AOS_MODEL_AUDIT_PATH", filepath.Join(naoPasta, "model-audit.wal"))

	r := correr(t, bin, "serve", "--wal", wal, "--run", "run-aos395-bad",
		"--goal", "x", "--snapshot", snapPath, "--worker", "p1")
	if r.code == 0 {
		t.Fatalf("um audit mal configurado tinha de abortar; saiu 0\nstdout:\n%s", r.stdout)
	}
	if !strings.Contains(r.stderr, "AOS_MODEL_AUDIT_PATH") {
		t.Fatalf("o erro tinha de nomear a variavel; stderr:\n%s", r.stderr)
	}
	if strings.Contains(r.stdout, "posse:") {
		t.Fatalf("o erro de config tinha de abortar ANTES de reclamar o run; stdout:\n%s", r.stdout)
	}
	// Não tomou posse nem materializou: o log do run fica sem nós.
	insp := correr(t, bin, "inspect", "--wal", wal, "--run", "run-aos395-bad")
	if strings.Contains(insp.stdout, "nos=2") {
		t.Fatalf("nada podia ter sido materializado; inspect:\n%s", insp.stdout)
	}
}

// TestAOS395_ProcessoReal_CaminhoDetidoPorOutroEscritorSai5: um segundo escritor do MESMO
// AOS_MODEL_AUDIT_PATH bifurcaria a hash-chain; o SO arbitra a posse e o processo sai com 5
// (o código do WAL detido) ANTES de reclamar o run — nem abre o ficheiro do outro.
func TestAOS395_ProcessoReal_CaminhoDetidoPorOutroEscritorSai5(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, snapshotDuasTools)
	wal := filepath.Join(dir, "es.wal")
	auditPath := filepath.Join(dir, "model-audit.wal")

	// O "outro escritor" é este processo de teste: detém a posse do caminho.
	largar, err := eventstore.LockWAL(auditPath)
	if err != nil {
		t.Fatalf("LockWAL: %v", err)
	}
	t.Cleanup(func() { _ = largar() })

	srv := upstreamComPlano(t)
	t.Setenv("AOS_MODEL_ENDPOINT", srv.URL)
	t.Setenv("AOS_MODEL_NAME", "gpt-4o")
	t.Setenv("AOS_MODEL_AUDIT_PATH", auditPath)

	r := correr(t, bin, "serve", "--wal", wal, "--run", "run-aos395-detido",
		"--goal", "x", "--snapshot", snapPath, "--worker", "p2")
	if r.code != exitWALDetido {
		t.Fatalf("caminho detido tinha de sair %d; saiu %d\nstdout:\n%s\nstderr:\n%s", exitWALDetido, r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stderr, "AOS_MODEL_AUDIT_PATH") {
		t.Fatalf("o erro tinha de nomear a variavel; stderr:\n%s", r.stderr)
	}
	if strings.Contains(r.stdout, "posse:") {
		t.Fatalf("nao podia ter reclamado o run com o audit detido; stdout:\n%s", r.stdout)
	}
}

// TestAOS395_ProcessoReal_ServeSemGatewayIgnoraOAudit: um `serve` que não decompõe pelo gateway
// (sem `--goal`, ou com fixture) não sela nada, pelo que não abre nem tranca o caminho — senão
// réplicas que partilham o ambiente seriam recusadas sem nunca chamarem o modelo (AOS-100).
func TestAOS395_ProcessoReal_ServeSemGatewayIgnoraOAudit(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	auditPath := filepath.Join(dir, "model-audit.wal")
	largar, err := eventstore.LockWAL(auditPath)
	if err != nil {
		t.Fatalf("LockWAL: %v", err)
	}
	t.Cleanup(func() { _ = largar() })

	t.Setenv("AOS_MODEL_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("AOS_MODEL_NAME", "gpt-4o")
	t.Setenv("AOS_MODEL_AUDIT_PATH", auditPath)

	r := correr(t, bin, "serve", "--wal", filepath.Join(dir, "es.wal"), "--run", "run-aos395-sem-goal", "--nodes", "a")
	if r.code != 0 {
		t.Fatalf("serve sem --goal nao pode depender do audit do gateway; saiu %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
	}
	if strings.Contains(r.stdout, "audit de governacao do model gateway") {
		t.Fatalf("sem decomposicao pelo gateway nao ha postura a declarar:\n%s", r.stdout)
	}
}
