package eventstore_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// wallock_test.go — A POSSE EXCLUSIVA DE ESCRITA DO WAL (AOS-285).
//
// # Porque há um subprocesso aqui
//
// O AC do ticket exige que a prova corra em DOIS PROCESSOS REAIS, e a exigência não é
// cerimónia: em Unix o `flock` é por DESCRITOR, pelo que duas goroutines do mesmo
// processo com descritores diferentes exibem a contenção; mas em Windows o
// `CreateFile` sem partilha é por HANDLE em todo o sistema. Um teste in-process
// passaria nas duas plataformas por razões diferentes e não provaria a propriedade que
// interessa — que é entre PROCESSOS.
//
// O subprocesso é este mesmo binário de teste, re-invocado com uma variável de
// ambiente que o faz correr só a sonda. Não há binário auxiliar a construir.

// envSonda faz o binário de teste correr apenas a sonda de posse.
const envSonda = "AOS_TEST_WALLOCK_PROBE"

// TestMain intercepta a re-invocação: com [envSonda] definido, o processo tenta tomar a
// posse do WAL indicado e sai com um código que distingue as três respostas.
//
//	0 — obteve a posse
//	3 — recusado por posse já tomada (ErrWALHeld)
//	1 — qualquer outro erro
//
// Os códigos são distintos de propósito: um teste que só soubesse «falhou» não
// distinguiria a recusa legítima de um disco avariado, que é exactamente a distinção
// que o operador precisa de fazer.
func TestMain(m *testing.M) {
	if wal := os.Getenv(envSonda); wal != "" {
		unlock, err := eventstore.LockWAL(wal)
		switch {
		case errors.Is(err, eventstore.ErrWALHeld):
			os.Exit(3)
		case err != nil:
			os.Exit(1)
		}
		// Segura a posse enquanto o pai o quiser: o pai mata-nos quando tiver medido.
		defer func() { _ = unlock() }()
		if _, err := os.Stdout.WriteString("posse-obtida\n"); err != nil {
			os.Exit(1)
		}
		// Bloqueia até o pai fechar o stdin (ou nos matar).
		var b [1]byte
		_, _ = os.Stdin.Read(b[:])
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// sondaEmSubprocesso arranca a sonda a segurar a posse do WAL e devolve o processo e
// uma função que o encerra. Espera até a posse estar de facto tomada.
func sondaEmSubprocesso(t *testing.T, wal string) (*exec.Cmd, func()) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), envSonda+"="+wal)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin da sonda: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout da sonda: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("arranque da sonda: %v", err)
	}
	// Espera pelo sinal de posse obtida — sem isto haveria corrida entre o pai medir e
	// o filho ter chegado a tomar a posse, e o teste seria intermitente.
	buf := make([]byte, len("posse-obtida\n"))
	if _, err := stdout.Read(buf); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("a sonda não anunciou a posse: %v", err)
	}
	return cmd, func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
}

// ---------------------------------------------------------------------------
// TESTE — DOIS PROCESSOS, UM SÓ ESCRITOR.
// ---------------------------------------------------------------------------

func TestLockWAL_SegundoProcessoERecusado(t *testing.T) {
	wal := filepath.Join(t.TempDir(), "events.wal")

	_, parar := sondaEmSubprocesso(t, wal)
	defer parar()

	// Este processo tenta a mesma posse: RECUSADO, e com o sentinela certo.
	unlock, err := eventstore.LockWAL(wal)
	if err == nil {
		_ = unlock()
		t.Fatal("a posse foi concedida a um SEGUNDO detentor — o guard não arbitra entre processos")
	}
	if !errors.Is(err, eventstore.ErrWALHeld) {
		t.Fatalf("erro = %v, quer eventstore.ErrWALHeld — a recusa tem de ser distinguível de uma avaria de I/O", err)
	}
}

// ---------------------------------------------------------------------------
// TESTE — A MORTE DO DETENTOR LIBERTA A POSSE (sem TTL, sem posse órfã).
//
// É a propriedade que justifica ter escolhido o SO como árbitro em vez de um lease. Um
// lock-file ingénuo falharia aqui: deixaria um ficheiro órfão a bloquear para sempre.
// ---------------------------------------------------------------------------

func TestLockWAL_MorteDoDetentorLibertaAPosse(t *testing.T) {
	wal := filepath.Join(t.TempDir(), "events.wal")

	_, parar := sondaEmSubprocesso(t, wal)

	// Confirma que está mesmo tomada antes de matar — senão o teste passaria mesmo que
	// a posse nunca tivesse sido concedida.
	if _, err := eventstore.LockWAL(wal); !errors.Is(err, eventstore.ErrWALHeld) {
		parar()
		t.Fatalf("a posse não estava tomada antes do kill (err=%v) — o teste não mediria nada", err)
	}

	parar() // mata o detentor SEM ele largar a posse

	unlock, err := eventstore.LockWAL(wal)
	if err != nil {
		t.Fatalf("posse após a MORTE do detentor = %v — ficou órfã; é exactamente o modo de falha que o árbitro do SO existe para não ter", err)
	}
	if err := unlock(); err != nil {
		t.Fatalf("unlock: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TESTE — LARGAR A POSSE DEVOLVE-A, E É IDEMPOTENTE.
// ---------------------------------------------------------------------------

func TestLockWAL_LargarDevolveEEIdempotente(t *testing.T) {
	wal := filepath.Join(t.TempDir(), "events.wal")

	unlock, err := eventstore.LockWAL(wal)
	if err != nil {
		t.Fatalf("primeira posse: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatalf("largar: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatalf("largar SEGUNDA vez devia ser no-op, deu %v", err)
	}

	segunda, err := eventstore.LockWAL(wal)
	if err != nil {
		t.Fatalf("posse após largar = %v — a posse não foi devolvida", err)
	}
	_ = segunda()
}

// ---------------------------------------------------------------------------
// TESTE — QUEM SÓ LÊ NÃO É BLOQUEADO.
//
// `aos wal-inspect` e `aos wal-summary` abrem o MESMO WAL com o nó a correr, e são o
// que um operador usa a meio de um incidente. Uma tranca sobre o próprio WAL — em vez
// do ficheiro irmão — parti-los-ia, e parti-los-ia no pior momento possível.
// ---------------------------------------------------------------------------

func TestLockWAL_NaoBloqueiaQuemLe(t *testing.T) {
	wal := filepath.Join(t.TempDir(), "events.wal")

	// Um WAL com conteúdo real, aberto por quem detém a posse.
	unlock, err := eventstore.LockWAL(wal)
	if err != nil {
		t.Fatalf("posse: %v", err)
	}
	defer func() { _ = unlock() }()

	escritor, err := eventstore.Open(wal)
	if err != nil {
		t.Fatalf("Open do detentor: %v", err)
	}
	if _, err := escritor.Append(t.Context(), "run-1", eventstore.EventInput{
		Type: "probe", Payload: []byte(`{}`), RunID: "run-1", StepID: "s1",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := escritor.Close(); err != nil {
		t.Fatalf("Close do escritor: %v", err)
	}

	// COM a posse ainda tomada, um leitor abre o MESMO WAL e vê o facto.
	leitor, err := eventstore.Open(wal)
	if err != nil {
		t.Fatalf("Open de LEITURA com a posse tomada = %v — a tranca está no ficheiro errado e parte o wal-inspect", err)
	}
	defer func() { _ = leitor.Close() }()
	evs, err := leitor.Read(t.Context(), "run-1", 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("eventos lidos = %d, quer 1", len(evs))
	}
}

// ---------------------------------------------------------------------------
// TESTE — o ficheiro de posse que FICA em disco não bloqueia o arranque seguinte.
//
// É a diferença entre isto e um lock-file ingénuo. O que arbitra é a posse do
// descritor, não a existência do ficheiro.
// ---------------------------------------------------------------------------

func TestLockWAL_FicheiroResidualNaoBloqueia(t *testing.T) {
	wal := filepath.Join(t.TempDir(), "events.wal")

	unlock, err := eventstore.LockWAL(wal)
	if err != nil {
		t.Fatalf("posse: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatalf("largar: %v", err)
	}
	if _, err := os.Stat(wal + ".lock"); err != nil {
		t.Skipf("o ficheiro de posse não ficou em disco (%v) — nada a provar nesta plataforma", err)
	}
	again, err := eventstore.LockWAL(wal)
	if err != nil {
		t.Fatalf("posse com o ficheiro residual presente = %v — está a arbitrar pela EXISTÊNCIA do ficheiro, que é o defeito do lock-file ingénuo", err)
	}
	_ = again()
}
