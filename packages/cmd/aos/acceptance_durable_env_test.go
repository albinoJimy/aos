package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// AOS-169 — DURABILIDADE (§13.3) pela FRONTEIRA DE AMBIENTE do contentor. Prova que o
// entrypoint `run` LIGA o substrato DURÁVEL (AOS-170) a partir de AOS_EVENTSTORE_PATH /
// AOS_WORM_PATH — a costura que o contentor (deploy/node, mount gravável em /var/lib/aos) usa
// para o estado sobreviver a um kill+reinício. Sem esta ligação, o contentor correria in-memory
// e a durabilidade de kill+reinício NÃO seria alcançável ao nível do nó empacotado.
//
// NÃO-VACUOSO: (1) o banner DECLARA o substrato "duravel em disco (AOS-170)" (não in-memory);
// (2) os ficheiros WAL do Event Store e do WORM são CRIADOS em disco; (3) reabrir o Event Store
// do MESMO caminho (replay do WAL) SUCEDE — o substrato é genuinamente durável e reconstruível,
// que é a pré-condição do kill+reinício-sem-duplicação (a prova completa no/no é
// TestServiceShutdownDurable_NoLossNoDupNoDoubleExecAfterRestart).
func TestAOS169_DurableSubstrateWiredFromEnv(t *testing.T) {
	dir := t.TempDir()
	esPath := filepath.Join(dir, "events.wal")
	wormPath := filepath.Join(dir, "worm.wal")

	t.Setenv("AOS_ISSUER_ID", "iss:aos169")
	t.Setenv("AOS_HUMANS", "operator")
	t.Setenv("AOS_EVENTSTORE_PATH", esPath)
	t.Setenv("AOS_WORM_PATH", wormPath)
	// Sem AOS_API_ADDR: `run` faz bootstrap, declara o modo, e retorna (não abre socket).
	t.Setenv("AOS_API_ADDR", "")

	var banner strings.Builder
	if err := run(&banner); err != nil {
		t.Fatalf("run com substrato durável por env devia arrancar, veio: %v", err)
	}

	// (1) O banner declara o substrato durável — não o in-memory de referência.
	if !strings.Contains(banner.String(), "duravel em disco (AOS-170)") {
		t.Fatalf("o banner devia declarar o substrato durAvel (AOS-170), veio:\n%s", banner.String())
	}

	// (2) Os ficheiros WAL foram criados em disco (o estado vive fora do processo).
	for _, p := range []string{esPath, wormPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("ficheiro WAL durAvel %q nAo foi criado (%v) — o substrato nAo ficou em disco", p, err)
		}
	}

	// (3) O Event Store durável reabre do MESMO caminho por replay do WAL (crash-safe): a
	// reconstrução byte-a-byte é a pré-condição do kill+reinício-sem-duplicação.
	es, err := eventstore.Open(esPath)
	if err != nil {
		t.Fatalf("reabrir o Event Store durAvel (replay do WAL) devia suceder, veio: %v", err)
	}
	if err := es.Close(); err != nil {
		t.Fatalf("Close do Event Store reaberto: %v", err)
	}
}
