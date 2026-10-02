package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// aos399_model_audit_posse_test.go — O AUDIT DO MODEL GATEWAY PEDE POSSE (AOS-399).
//
// O nó abre o seu `model-audit.wal` (AOS_MODEL_AUDIT_PATH) em [parseModelFromEnv], ANTES
// de o Bootstrap tomar a posse do Event Store e do WORM. Até AOS-399 esse caminho não
// pedia posse nenhuma: dois nós, ou um nó e um `aos-orq`, apontados ao mesmo ficheiro
// abriam-no ambos, e o segundo corria o replay (que trunca uma cauda incompleta) sobre a
// escrita do primeiro e selava a sua activação na mesma partição — a hash-chain
// bifurcava e o WORM deixava de abrir no arranque seguinte.

// buildAOSNode compila o binário do nó (`packages/cmd/aos`) para um caminho temporário.
func buildAOSNode(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "aos")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Env = append(os.Environ(), "GOPROXY=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compilar aos: %v\n%s", err, out)
	}
	return bin
}

// envDoNoComModelAudit é o ambiente mínimo de um nó fora de produção com o Model Gateway
// composto e o audit durável em `path`. Parte do ambiente do teste SEM as variáveis AOS_*,
// para que a config de quem corre os testes não decida o resultado.
func envDoNoComModelAudit(path string, extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "AOS_") {
			env = append(env, kv)
		}
	}
	env = append(env,
		// O endpoint não é contactado: o nó só compõe o gateway no arranque.
		"AOS_MODEL_ENDPOINT=http://127.0.0.1:9/v1",
		"AOS_MODEL_NAME=modelo-aos399",
		"AOS_MODEL_AUDIT_PATH="+path,
	)
	return append(env, extra...)
}

// ---------------------------------------------------------------------------
// TESTE — DOIS NÓS REAIS, UM SÓ ESCRITOR DO AUDIT DO GATEWAY.
//
// O nó A (processo real, a servir a API) detém o caminho. O nó B (outro processo real,
// Event Store e WORM próprios — in-memory) aponta ao MESMO AOS_MODEL_AUDIT_PATH e tem de
// ser RECUSADO antes de abrir o ficheiro: sai com erro, diz qual o ficheiro e porquê, e
// não toca num byte do WAL de A. Morto A, o mesmo B arranca — a recusa era a posse de A,
// e não uma config inválida.
//
// Falha-antes (medido por mutação, AOS-399): sem a posse em [parseModelAuditFromEnv], B
// sai 0 e o WAL de A cresce com a activação selada por B na mesma partição.
// ---------------------------------------------------------------------------

func TestAOS399_ProcessoReal_SegundoNoNoMesmoModelAuditRecusa(t *testing.T) {
	if testing.Short() {
		t.Skip("compila o binário do nó e arranca dois processos")
	}
	bin := buildAOSNode(t)
	dir := t.TempDir()
	wal := filepath.Join(dir, "model-audit.wal")

	// NÓ A: serve (fica vivo) e detém o caminho.
	a := exec.Command(bin, "serve")
	a.Env = envDoNoComModelAudit(wal, "AOS_API_ADDR=127.0.0.1:0")
	saidaA, err := a.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout de A: %v", err)
	}
	a.Stderr = io.Discard
	if err := a.Start(); err != nil {
		t.Fatalf("arranque de A: %v", err)
	}
	pararA := func() {
		_ = a.Process.Kill()
		_ = a.Wait()
	}
	defer pararA()
	pronto := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(saidaA)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			if strings.Contains(sc.Text(), "a levantar a API HTTP") {
				pronto <- true
				_, _ = io.Copy(io.Discard, saidaA)
				return
			}
		}
		pronto <- false
	}()
	select {
	case ok := <-pronto:
		if !ok {
			t.Fatal("o nó A terminou antes de levantar a API — não chegou a deter o caminho")
		}
	case <-time.After(60 * time.Second):
		t.Fatal("o nó A não levantou a API em 60s")
	}
	antes, err := os.ReadFile(wal)
	if err != nil || len(antes) == 0 {
		t.Fatalf("o nó A não selou a activação no audit durável (%d bytes, err=%v) — o cenário não se montou", len(antes), err)
	}

	// NÓ B: mesmo caminho, tudo o resto seu.
	b := exec.Command(bin, "serve")
	b.Env = envDoNoComModelAudit(wal)
	var errB bytes.Buffer
	b.Stdout, b.Stderr = io.Discard, &errB
	errRun := b.Run()
	var exitErr *exec.ExitError
	if !errors.As(errRun, &exitErr) {
		t.Fatalf("o nó B arrancou (err=%v) com o audit do gateway DETIDO pelo nó A — dois escritores na mesma hash-chain", errRun)
	}
	for _, exigido := range []string{"já detido", "AOS_MODEL_AUDIT_PATH", filepath.Base(wal), "Pare a outra réplica", "AOS-399"} {
		if !strings.Contains(errB.String(), exigido) {
			t.Errorf("a recusa não menciona %q — stderr: %s", exigido, errB.String())
		}
	}
	depois, err := os.ReadFile(wal)
	if err != nil {
		t.Fatalf("reler o WAL de A: %v", err)
	}
	if !bytes.Equal(antes, depois) {
		t.Fatalf("o nó B recusado ESCREVEU no WAL de A (%d → %d bytes) — a recusa tem de vir antes de abrir", len(antes), len(depois))
	}

	// Controlo positivo: A morre, o SO larga a posse, e o MESMO B arranca e reabre a cadeia.
	pararA()
	b2 := exec.Command(bin, "serve")
	b2.Env = envDoNoComModelAudit(wal)
	var err2 bytes.Buffer
	b2.Stdout, b2.Stderr = io.Discard, &err2
	if err := b2.Run(); err != nil {
		t.Fatalf("com A parado, B devia arrancar e reabrir a cadeia de A: %v\n%s", err, err2.String())
	}
}

// ---------------------------------------------------------------------------
// TESTE — a recusa é a do guard de posse, e vem antes de abrir o WAL.
//
// É a mesma prova no processo do teste (a posse modelada pelo mecanismo, como em
// aos285_guard_arranque_test.go): o erro é [ErrEventStoreJaDetido] e NÃO
// [ErrBadModelAudit] — a acção do operador é parar o outro escritor, não corrigir o
// caminho —, e o WAL não chega a ser criado.
// ---------------------------------------------------------------------------

func TestAOS399_ModelAuditDetidoRecusaSemAbrir(t *testing.T) {
	wal := filepath.Join(t.TempDir(), "model-audit.wal")
	largar, err := eventstore.LockWAL(wal)
	if err != nil {
		t.Fatalf("posse inicial: %v", err)
	}
	defer func() { _ = largar() }()

	t.Setenv("AOS_MODEL_AUDIT_PATH", wal)
	store, _, err := parseModelAuditFromEnv()
	if err == nil {
		closeAudit(t, store)
		t.Fatal("o audit do gateway abriu um caminho DETIDO por outro escritor")
	}
	if !errors.Is(err, ErrEventStoreJaDetido) {
		t.Fatalf("erro = %v, quer ErrEventStoreJaDetido", err)
	}
	if errors.Is(err, ErrBadModelAudit) {
		t.Errorf("a posse alheia foi classificada como config inválida (ErrBadModelAudit): %v", err)
	}
	if _, statErr := os.Stat(wal); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("o WAL foi criado/aberto apesar da recusa (stat=%v) — a posse tem de vir antes do OpenFileStore", statErr)
	}
}

// ---------------------------------------------------------------------------
// TESTE — fechar o store larga a posse; e o caminho não pode ser o WORM do nó.
//
// O Close do store devolvido é o que liberta o caminho para uma reabertura. E como a
// posse do audit do gateway é tomada antes do Bootstrap, um AOS_MODEL_AUDIT_PATH igual
// ao AOS_WORM_PATH do mesmo nó é recusado quando o Bootstrap pede a posse do WORM: seriam
// dois FileStore no mesmo ficheiro.
// ---------------------------------------------------------------------------

func TestAOS399_CloseLargaAPosseEOWORMDoNoNaoPartilhaOCaminho(t *testing.T) {
	wal := filepath.Join(t.TempDir(), "model-audit.wal")
	t.Setenv("AOS_MODEL_AUDIT_PATH", wal)

	store, _, err := parseModelAuditFromEnv()
	if err != nil {
		t.Fatalf("abrir: %v", err)
	}
	if _, err := tomarPosseDoWAL(Config{WORMPath: wal}); !errors.Is(err, ErrEventStoreJaDetido) {
		t.Fatalf("WORM do nó no caminho do audit do gateway: erro = %v, quer ErrEventStoreJaDetido", err)
	}
	c, ok := store.(io.Closer)
	if !ok {
		t.Fatalf("o store durável (%T) não expõe Close — a posse ficaria presa até ao fim do processo", store)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close idempotente: %v", err)
	}
	store2, _, err := parseModelAuditFromEnv()
	if err != nil {
		t.Fatalf("reabrir depois do Close = %v — o Close não largou a posse", err)
	}
	closeAudit(t, store2)
}
