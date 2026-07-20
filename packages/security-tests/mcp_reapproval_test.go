package securitytests

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	regdomain "github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/tofu"
)

// ===========================================================================
// CENÁRIO 7 — RE-APROVAÇÃO DE SCHEMA MCP / ANTI RUG-PULL (AOS-049, ADR-012)
//
// Um servidor MCP confiado no "Dia 1" MUDA o seu schema/manifesto no "Dia 7" (rug-pull):
// o digest do manifesto DIVERGE do pinado. A máquina TOFU DETECTA o drift (ErrSchemaDrift),
// transita a identidade para `changed` e BLOQUEIA a utilização até re-aprovação EXPLÍCITA
// com uma NOVA versão SemVer — recusando a re-aprovação in-band na MESMA versão
// (ErrInBandReapproval), que é o próprio vector do rug-pull. Cada transição é SELADA no
// audit WORM tamper-evident. ORQUESTRA tofu.Monitor real; não o reimplementa. O digest é
// tratado como OPACO (o conteúdo do schema permanece untrusted, ADR-005).
// ===========================================================================

// mustVer parseia uma versão SemVer ou falha o teste.
func mustVer(t *testing.T, s string) regdomain.Version {
	t.Helper()
	v, err := regdomain.ParseVersion(s)
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", s, err)
	}
	return v
}

// newTofuMonitor constrói a máquina TOFU sobre um audit.MemStore real com relógio
// determinista.
func newTofuMonitor(t *testing.T) (*tofu.Monitor, audit.Store) {
	t.Helper()
	store := audit.NewMemStore()
	m, err := tofu.NewMonitor(store, tofu.WithClock(func() time.Time {
		return time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	}))
	if err != nil {
		t.Fatalf("NewMonitor: %v", err)
	}
	return m, store
}

const (
	mcpIdentity = "mcp://tools.example"
	digestDay1  = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	digestDay7  = "sha256:7777777777777777777777777777777777777777777777777777777777777777"
)

// TestMCPReapproval_SchemaDrift_BlockedAndReapprovalGated é a prova central do cenário: um
// servidor MCP pinado no Dia 1 sofre schema drift no Dia 7 → BLOQUEADO; a re-aprovação na
// MESMA versão SemVer é RECUSADA (rug-pull in-band); só uma NOVA versão SemVer recupera a
// confiança. Controlos de não-tautologia intercalados provam que o gate admite o legítimo.
func TestMCPReapproval_SchemaDrift_BlockedAndReapprovalGated(t *testing.T) {
	t.Parallel()
	m, store := newTofuMonitor(t)
	ctx := context.Background()
	v1 := mustVer(t, "1.0.0")

	// Dia 1: primeira observação (first_seen, NÃO admitida) e ratificação → pinned.
	if _, err := m.Observe(ctx, tofu.Observation{Identity: mcpIdentity, Version: v1, Digest: digestDay1}); err != nil {
		t.Fatalf("Observe (first_seen): %v", err)
	}
	if err := m.Ratify(ctx, mcpIdentity, v1, digestDay1); err != nil {
		t.Fatalf("Ratify: %v", err)
	}
	// Não-tautologia: um manifesto congelado cujo digest CASA o pinado é ADMITIDO.
	if ok, _, _ := m.Admits(mcpIdentity); !ok {
		t.Fatal("identidade pinada com digest coincidente não é admitida (gate tautológico?)")
	}
	okOut, err := m.Observe(ctx, tofu.Observation{Identity: mcpIdentity, Version: v1, Digest: digestDay1})
	if err != nil || !okOut.Admitted || okOut.Drift {
		t.Fatalf("re-observação idêntica: out=%+v err=%v, quer admitida sem drift", okOut, err)
	}

	// Dia 7: RUG-PULL — o mesmo servidor apresenta um digest DIVERGENTE.
	drift, err := m.Observe(ctx, tofu.Observation{Identity: mcpIdentity, Version: v1, Digest: digestDay7})
	if !errors.Is(err, tofu.ErrSchemaDrift) {
		t.Fatalf("schema drift: err=%v, quer ErrSchemaDrift", err)
	}
	if drift.Admitted || !drift.Drift {
		t.Fatalf("após drift: out=%+v, quer NÃO admitida e Drift=true", drift)
	}
	if ok, st, _ := m.Admits(mcpIdentity); ok || st != tofu.StateChanged {
		t.Fatalf("após drift: admits=%v state=%q, quer (false, changed)", ok, st)
	}

	// Re-aprovação IN-BAND (mesma versão 1.0.0, ainda que com o novo digest) → RECUSADA.
	// É o próprio vector do rug-pull: uma mudança de schema exige uma NOVA versão SemVer.
	if _, err := m.Reapprove(ctx, tofu.Observation{Identity: mcpIdentity, Version: v1, Digest: digestDay7}); !errors.Is(err, tofu.ErrInBandReapproval) {
		t.Fatalf("re-aprovação in-band: err=%v, quer ErrInBandReapproval", err)
	}
	// Continua bloqueado após a tentativa in-band.
	if ok, _, _ := m.Admits(mcpIdentity); ok {
		t.Fatal("identidade admitida após re-aprovação in-band recusada (fail-open?)")
	}

	// Recuperação legítima: re-aprovação com uma NOVA versão SemVer → pinned + admitida.
	v2 := mustVer(t, "2.0.0")
	rec, err := m.Reapprove(ctx, tofu.Observation{Identity: mcpIdentity, Version: v2, Digest: digestDay7})
	if err != nil {
		t.Fatalf("re-aprovação com nova versão: %v", err)
	}
	if !rec.Admitted || rec.State != tofu.StatePinned {
		t.Fatalf("re-aprovação com nova versão: out=%+v, quer admitida e pinned", rec)
	}

	// AUDIT WORM: cada transição (first_seen, pinned, drift, in-band recusada, reapproved)
	// foi selada tamper-evident na partição TOFU.
	verifyWORM(t, store, tofu.DefaultPartition)

	ledger := audit.NewMemStore()
	attestBlock(t, ledger, "mcp_reapproval_drift", "registry.tofu", tofu.ErrSchemaDrift.Error())
	verifyWORM(t, ledger, suiteLedgerPartition)
}

// TestMCPReapproval_VersionRegression_Refused prova a face de downgrade: re-aprovar com
// uma versão INFERIOR à pinada re-introduziria um schema anterior sob aparência de
// re-aprovação — recusado (ErrVersionRegression).
func TestMCPReapproval_VersionRegression_Refused(t *testing.T) {
	t.Parallel()
	m, _ := newTofuMonitor(t)
	ctx := context.Background()
	v2 := mustVer(t, "2.0.0")

	if _, err := m.Observe(ctx, tofu.Observation{Identity: mcpIdentity, Version: v2, Digest: digestDay1}); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if err := m.Ratify(ctx, mcpIdentity, v2, digestDay1); err != nil {
		t.Fatalf("Ratify: %v", err)
	}
	if _, err := m.Observe(ctx, tofu.Observation{Identity: mcpIdentity, Version: v2, Digest: digestDay7}); !errors.Is(err, tofu.ErrSchemaDrift) {
		t.Fatalf("drift: %v, quer ErrSchemaDrift", err)
	}
	// Downgrade para 1.0.0 é recusado.
	if _, err := m.Reapprove(ctx, tofu.Observation{Identity: mcpIdentity, Version: mustVer(t, "1.0.0"), Digest: digestDay7}); !errors.Is(err, tofu.ErrVersionRegression) {
		t.Fatalf("re-aprovação com downgrade: %v, quer ErrVersionRegression", err)
	}
}

// TestMetaDetects_MCPReapproval_WhenDigestUnchanged — com o controlo CONTORNADO (o schema
// trocado apresentaria o MESMO digest, como se a comparação não fosse feita), a
// re-observação NÃO é drift e a identidade CONTINUA admitida: o "swap" passa. Prova que o
// bloqueio do cenário vem MESMO da recomputação/comparação do digest — se o digest não
// fosse comparado, o schema trocado passaria despercebido.
func TestMetaDetects_MCPReapproval_WhenDigestUnchanged(t *testing.T) {
	t.Parallel()
	m, _ := newTofuMonitor(t)
	ctx := context.Background()
	v1 := mustVer(t, "1.0.0")

	if _, err := m.Observe(ctx, tofu.Observation{Identity: mcpIdentity, Version: v1, Digest: digestDay1}); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if err := m.Ratify(ctx, mcpIdentity, v1, digestDay1); err != nil {
		t.Fatalf("Ratify: %v", err)
	}
	// Swap com o MESMO digest (comparação contornada): sem drift, continua admitida.
	out, err := m.Observe(ctx, tofu.Observation{Identity: mcpIdentity, Version: v1, Digest: digestDay1})
	if err != nil || !out.Admitted || out.Drift {
		t.Fatalf("com o digest inalterado, o swap devia PASSAR admitido sem drift; out=%+v err=%v (deteção vácua?)", out, err)
	}
}
