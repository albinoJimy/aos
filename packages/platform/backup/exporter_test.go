package backup

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
)

func TestExport_IncrementalCreatesSegments(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	seed(t, src, "run-a", 3, "m")

	exp, _ := newExporter(t, src, "eu-west")

	// Primeiro ciclo: cria um segmento com 3 eventos.
	r1, err := exp.Export(ctx)
	if err != nil {
		t.Fatalf("Export#1: %v", err)
	}
	if !r1.Created || r1.Events != 3 || r1.Cycle != 1 {
		t.Fatalf("Export#1 inesperado: %+v", r1)
	}

	// Mais eventos → segundo ciclo incremental exporta SÓ os novos.
	seed(t, src, "run-a", 2, "m")
	seed(t, src, "run-b", 1, "m")
	r2, err := exp.Export(ctx)
	if err != nil {
		t.Fatalf("Export#2: %v", err)
	}
	if !r2.Created || r2.Events != 3 || r2.Cycle != 2 {
		t.Fatalf("Export#2 inesperado (esperava 3 novos): %+v", r2)
	}
	if r2.StreamHeads["run-a"] != 5 || r2.StreamHeads["run-b"] != 1 {
		t.Fatalf("heads cumulativos errados: %+v", r2.StreamHeads)
	}

	m := exp.Manifest()
	if len(m.Segments) != 2 {
		t.Fatalf("manifesto tem %d segmentos, quero 2", len(m.Segments))
	}
}

func TestExport_NoopWhenCaughtUp(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	seed(t, src, "run-a", 2, "m")
	exp, _ := newExporter(t, src, "eu-west")

	if _, err := exp.Export(ctx); err != nil {
		t.Fatalf("Export#1: %v", err)
	}
	r2, err := exp.Export(ctx)
	if err != nil {
		t.Fatalf("Export#2: %v", err)
	}
	if r2.Created {
		t.Fatalf("segundo ciclo sem novidade não devia criar segmento: %+v", r2)
	}
	if len(exp.Manifest().Segments) != 1 {
		t.Fatalf("noop não devia acrescentar segmento")
	}
}

func TestExport_EncryptedAtRest(t *testing.T) {
	ctx := context.Background()
	const marker = "TOPSECRET-PII-MARKER"
	src := newSourceStore(t, "board-eu", "eu-west")
	seed(t, src, "run-a", 3, marker)

	exp, dst := newExporter(t, src, "eu-west")
	if _, err := exp.Export(ctx); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Nenhum blob em repouso pode conter o plaintext do payload.
	for _, seg := range exp.Manifest().Segments {
		blob, err := dst.Get(seg.Ref)
		if err != nil {
			t.Fatalf("Get %s: %v", seg.Ref, err)
		}
		if bytes.Contains(blob, []byte(marker)) {
			t.Fatalf("plaintext do payload encontrado em repouso no segmento %s", seg.Ref)
		}
	}
}

// TestExport_ProductionDefaults exercita os caminhos por omissão (cryptoRand real
// via crypto/rand) e a REUTILIZAÇÃO explícita da porta audit.KeyVault, mais os
// acessores públicos usados no wiring de produção.
func TestExport_ProductionDefaults(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	seed(t, src, "run-a", 2, "m")

	dst := NewInMemoryImmutableStore("eu-west")
	vault := audit.NewInMemoryKeyVault(nil) // nil ⇒ crypto/rand de produção
	// Sem WithRandSource: usa cryptoRand (crypto/rand) por omissão.
	exp, err := NewExporter(src, dst, newSigner(t), WithKeyVault(vault))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	if exp.Immutable() != dst {
		t.Fatalf("Immutable() não devolveu o destino configurado")
	}
	if exp.Periodicity() != 30*time.Second {
		t.Fatalf("Periodicity()=%v, quero 30s (default)", exp.Periodicity())
	}
	if _, err := exp.Export(ctx); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Round-trip completo com as chaves de produção reutilizadas do mesmo vault.
	rst, err := NewRestorer(dst, vault, exp.Public())
	if err != nil {
		t.Fatalf("NewRestorer: %v", err)
	}
	out := freshDest(t, "board-eu", "eu-west")
	ev, err := rst.RestoreTo(ctx, exp.Manifest(), exp.Checkpoint(), 0, nil, out)
	if err != nil {
		t.Fatalf("RestoreTo: %v", err)
	}
	if !ev.Verified || ev.EventsRestored != 2 {
		t.Fatalf("round-trip com defaults inesperado: %+v", ev)
	}
}

func TestExport_RetentionObjectLock(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	seed(t, src, "run-a", 1, "m")

	policy := audit.NewRetentionPolicy(map[audit.DataClass]time.Duration{audit.ClassAudit: time.Hour})
	exp, dst := newExporter(t, src, "eu-west", WithRetention(policy, audit.ClassAudit), WithClock(fixedClock(t0)))
	if _, err := exp.Export(ctx); err != nil {
		t.Fatalf("Export: %v", err)
	}
	ref := exp.Manifest().Segments[0].Ref

	// Dentro do object-lock (1h) → remoção recusada.
	if err := dst.Delete(ref, t0.Add(30*time.Minute)); !errors.Is(err, ErrObjectLocked) {
		t.Fatalf("Delete dentro do lock=%v, quero ErrObjectLocked", err)
	}
	// Após o object-lock → remoção permitida.
	if err := dst.Delete(ref, t0.Add(2*time.Hour)); err != nil {
		t.Fatalf("Delete após o lock: %v", err)
	}
}
