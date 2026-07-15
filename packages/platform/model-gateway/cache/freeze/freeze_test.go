package freeze_test

import (
	"context"
	"errors"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/toolset"

	"github.com/aos-ref/platform/model-gateway/cache/freeze"
	"github.com/aos-ref/platform/model-gateway/cache/layout"
)

// --- composição com o FrozenToolSet REAL de AOS-050 ------------------------

func ver(mj, mn, p int) domain.Version { return domain.Version{Major: mj, Minor: mn, Patch: p} }

func entry(id string, v domain.Version, digest string) domain.Entry {
	return domain.Entry{
		ID: id, Version: v, Kind: domain.KindTool, Digest: digest,
		Contract:   domain.Contract{Egress: domain.EgressInternal},
		Provenance: domain.Provenance{Origin: "mcp://" + id, Trust: domain.TrustFirstSeen},
		Status:     domain.StatusActive,
	}
}

// fakeCatalog é a porta Catalog controlada (o mesmo padrão dos testes de AOS-050):
// devolve um snapshot atómico de entradas active, permitindo mutar o "catálogo"
// entre congelamentos para simular uma tool nova a meio de um run.
type fakeCatalog struct{ entries []domain.Entry }

func (f fakeCatalog) ActiveEntries(context.Context) ([]domain.Entry, error) {
	out := make([]domain.Entry, len(f.entries))
	copy(out, f.entries)
	return out, nil
}

// freezeRun congela o tool set REAL (AOS-050) para um run a partir do catálogo dado.
func freezeRun(t *testing.T, cat toolset.Catalog, runID string) *toolset.FrozenToolSet {
	t.Helper()
	fts, err := toolset.FreezeToolSet(context.Background(), cat, runID, nil)
	if err != nil {
		t.Fatalf("FreezeToolSet(%s): %v", runID, err)
	}
	return fts
}

// TestFreeze_NewToolOnlyInNewRun é o teste de COMPORTAMENTO central de AOS-060: uma
// nova tool MCP NÃO altera um run em curso — o prefixo congelado do run permanece
// byte-idêntico —; a tool nova entra SÓ num RUN NOVO. Compõe o FrozenToolSet real.
func TestFreeze_NewToolOnlyInNewRun(t *testing.T) {
	t.Parallel()
	cat := &fakeCatalog{entries: []domain.Entry{
		entry("tool.a", ver(1, 0, 0), "sha256:da"),
		entry("tool.b", ver(1, 0, 0), "sha256:db"),
	}}

	// Arranque do RUN-1: congela {a, b}.
	fts1 := freezeRun(t, cat, "run-1")
	rp1, err := freeze.Freeze("run-1", "SYS", fts1)
	if err != nil {
		t.Fatalf("Freeze(run-1): %v", err)
	}
	if rp1.RunID() != "run-1" {
		t.Fatalf("RunID = %q, quer run-1", rp1.RunID())
	}
	prefixBefore := rp1.PrefixHash()
	tsHashBefore := rp1.ToolSetHash()

	// A guarda pina o run-1 a partir do primeiro turno.
	g := layout.NewGuard(nil)
	if err := g.Admit(rp1.Turn(1, []agentruntime.TailSegment{{Kind: agentruntime.TailObjective, Content: []byte("obj")}})); err != nil {
		t.Fatalf("run-1 turno 1: %v", err)
	}

	// --- Uma tool NOVA é adicionada ao catálogo A MEIO do run-1 ---
	cat.entries = append(cat.entries, entry("tool.c", ver(1, 0, 0), "sha256:dc"))

	// O run-1 EM CURSO NÃO vê a tool nova: o RunPrefix é imutável (o FrozenToolSet é
	// um snapshot). O prefixo e o tool-set-hash mantêm-se byte-idênticos.
	if rp1.PrefixHash() != prefixBefore {
		t.Errorf("prefixo do run-1 mudou a meio do run: %s != %s", rp1.PrefixHash(), prefixBefore)
	}
	if rp1.ToolSetHash() != tsHashBefore {
		t.Errorf("tool-set-hash do run-1 mudou a meio do run")
	}
	// E um novo turno do run-1 continua a ser admitido (prefixo/tool-set inalterados).
	if err := g.Admit(rp1.Turn(2, []agentruntime.TailSegment{
		{Kind: agentruntime.TailObjective, Content: []byte("obj")},
		{Kind: agentruntime.TailToolResult, Content: []byte("res")},
	})); err != nil {
		t.Fatalf("run-1 turno 2 (apos tool nova no catalogo): %v", err)
	}

	// --- RUN-2 (novo) VÊ a tool nova ---
	fts2 := freezeRun(t, cat, "run-2")
	rp2, err := freeze.Freeze("run-2", "SYS", fts2)
	if err != nil {
		t.Fatalf("Freeze(run-2): %v", err)
	}
	if rp2.ToolSetHash() == tsHashBefore {
		t.Errorf("run-2 devia ver um tool set DIFERENTE (com a tool nova)")
	}
	if rp2.PrefixHash() == prefixBefore {
		t.Errorf("run-2 devia ter um prefixo DIFERENTE (tool nova no prefixo imutavel)")
	}
	// O run-2 pina o seu PRÓPRIO prefixo, sem colidir com o run-1.
	if err := g.Admit(rp2.Turn(1, nil)); err != nil {
		t.Fatalf("run-2 turno 1: %v", err)
	}
}

// TestFreeze_PrefixByteIdenticalAcrossTurns prova que o prefixo materializado é
// byte-idêntico em todos os turnos do mesmo run (a base do cache-hit).
func TestFreeze_PrefixByteIdenticalAcrossTurns(t *testing.T) {
	t.Parallel()
	cat := &fakeCatalog{entries: []domain.Entry{
		entry("tool.a", ver(1, 0, 0), "sha256:da"),
		entry("tool.b", ver(2, 1, 0), "sha256:db"),
	}}
	rp, err := freeze.Freeze("run-x", "SYSTEM PROMPT", freezeRun(t, cat, "run-x"))
	if err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	v1 := rp.Assemble(1, []agentruntime.TailSegment{{Kind: agentruntime.TailObjective, Content: []byte("a")}})
	v2 := rp.Assemble(2, []agentruntime.TailSegment{{Kind: agentruntime.TailObjective, Content: []byte("a")}, {Kind: agentruntime.TailHistory, Content: []byte("b")}})
	if string(v1.Prefix) != string(v2.Prefix) {
		t.Errorf("prefixo NAO byte-identico entre turnos")
	}
	if v1.PrefixHash != v2.PrefixHash {
		t.Errorf("PrefixHash divergente entre turnos: %s != %s", v1.PrefixHash, v2.PrefixHash)
	}
}

// TestFreeze_FailClosed cobre as recusas do congelamento do prefixo.
func TestFreeze_FailClosed(t *testing.T) {
	t.Parallel()
	cat := &fakeCatalog{entries: []domain.Entry{entry("tool.a", ver(1, 0, 0), "sha256:da")}}
	fts := freezeRun(t, cat, "run-1")

	if _, err := freeze.Freeze("", "SYS", fts); !errors.Is(err, layout.ErrNoRunID) {
		t.Errorf("runID vazio: err = %v, quer ErrNoRunID", err)
	}
	if _, err := freeze.Freeze("run-1", "SYS", nil); !errors.Is(err, freeze.ErrNoToolSet) {
		t.Errorf("tool set nil: err = %v, quer ErrNoToolSet", err)
	}
	// Tool set de OUTRO run (run-1) usado para congelar run-2 ⇒ mismatch.
	if _, err := freeze.Freeze("run-2", "SYS", fts); !errors.Is(err, freeze.ErrRunMismatch) {
		t.Errorf("run mismatch: err = %v, quer ErrRunMismatch", err)
	}
}

// TestFreeze_SatisfiesPort garante (em compile-time + runtime) que o
// *toolset.FrozenToolSet real satisfaz a porta freeze.FrozenToolSet.
func TestFreeze_SatisfiesPort(t *testing.T) {
	t.Parallel()
	cat := &fakeCatalog{entries: []domain.Entry{entry("tool.a", ver(1, 0, 0), "sha256:da")}}
	var _ freeze.FrozenToolSet = freezeRun(t, cat, "run-1")
}
