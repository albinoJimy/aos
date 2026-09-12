package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	audit "github.com/aos-ref/platform/audit"
)

// AOS-373 — O CLI DE LEITURA NÃO ENCURTA A PROVA.
//
// `aos audit-trail` abria o WORM por [audit.OpenFileStore], que trunca uma cauda rasgada a
// validEnd antes de reabrir em append. Correr o subcomando de LEITURA sobre a prova forense
// apagava o registo em voo. Depois de AOS-373 abre por [audit.OpenFileStoreReadOnly]: este
// teste in-process prova que a trilha íntegra continua a sair E que o ficheiro não encolhe.

const aos373Partition = "run-aos373"

// aos373BuildTornWORM constrói um WORM com n registos na partição de teste, corta os últimos 5
// bytes (cauda GENUINAMENTE rasgada: perde trailer + 1 byte de payload do último registo) e
// devolve o caminho e o tamanho do ficheiro JÁ rasgado.
func aos373BuildTornWORM(t *testing.T, n int) (path string, sizeRasgado int64) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "worm.wal")
	fs, err := audit.OpenFileStore(path)
	if err != nil {
		t.Fatalf("open file store: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if _, err := fs.Append(ctx, audit.AuditRecord{
			Partition:  aos373Partition,
			Decision:   audit.DecisionAllow,
			Capability: "cap:aos373.record",
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if err := closeIfCloser(fs); err != nil {
		t.Fatalf("close store: %v", err)
	}

	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read wal: %v", err)
	}
	rasgado := full[:len(full)-5]
	if err := os.WriteFile(path, rasgado, 0o600); err != nil {
		t.Fatalf("write wal: %v", err)
	}
	return path, int64(len(rasgado))
}

// TestAOS373_CmdAuditTrail_NaoEncurtaAProva invoca cmdAuditTrail in-process sobre um WORM com
// cauda rasgada e prova as duas metades do ticket: (a) a trilha íntegra (o prefixo, 2 dos 3
// registos) aparece na saída, e (b) o ficheiro NÃO foi encurtado — o CLI de leitura já não
// destrói a prova em voo que uma investigação vai ver.
func TestAOS373_CmdAuditTrail_NaoEncurtaAProva(t *testing.T) {
	path, sizeAntes := aos373BuildTornWORM(t, 3)

	var out bytes.Buffer
	if err := cmdAuditTrail([]string{"--path", path, "--run", aos373Partition}, &out); err != nil {
		t.Fatalf("cmdAuditTrail: %v", err)
	}

	// (a) o prefixo íntegro (2 registos) aparece; o 3.º foi rasgado.
	got := out.String()
	for _, seq := range []int{1, 2} {
		marca := fmt.Sprintf("seq=%d ", seq)
		if !strings.Contains(got, marca) {
			t.Errorf("saída devia conter %q (registo íntegro), veio:\n%s", marca, got)
		}
	}
	if strings.Contains(got, "seq=3 ") {
		t.Errorf("o 3.º registo foi rasgado e não devia aparecer, veio:\n%s", got)
	}

	// (b) o ficheiro NÃO encolheu — a leitura já não trunca a prova.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat depois: %v", err)
	}
	if fi.Size() != sizeAntes {
		t.Fatalf("o CLI de LEITURA encurtou a prova: size antes=%d depois=%d", sizeAntes, fi.Size())
	}
}
