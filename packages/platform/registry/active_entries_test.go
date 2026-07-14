package registry

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/substrate/eventstore"
)

// TestActiveEntries_OnlyActivePinnedSorted prova a base de enumeração do
// congelamento de tool set (AOS-050): ActiveEntries devolve APENAS entradas active,
// cada uma por versão pinada, na ordem estável (id, version) — nunca a ordem de
// mapa. Publica os três tipos de artefacto em estados variados e verifica que só as
// active (e íntegras) saem, ordenadas.
func TestActiveEntries_OnlyActivePinnedSorted(t *testing.T) {
	t.Parallel()
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	v := ver(1, 0, 0)

	// active (entram) — publicados fora de ordem alfabética de propósito.
	mustPublish(t, reg, toolReq("tool.http", v))
	mustSetStatus(t, reg, "tool.http", v, domain.StatusActive)
	mustPublish(t, reg, mcpReq("mcp.fs", v))
	mustSetStatus(t, reg, "mcp.fs", v, domain.StatusActive)
	mustPublish(t, reg, skillReq("skill.sum", v))
	mustSetStatus(t, reg, "skill.sum", v, domain.StatusActive)

	// staging (NÃO entra) — publicado mas nunca promovido.
	mustPublish(t, reg, toolReq("tool.staged", v))

	// deprecated (NÃO entra) — active e depois deprecado.
	mustPublish(t, reg, toolReq("tool.old", v))
	mustSetStatus(t, reg, "tool.old", v, domain.StatusActive)
	mustSetStatus(t, reg, "tool.old", v, domain.StatusDeprecated)

	// revoked (NÃO entra).
	mustPublish(t, reg, toolReq("tool.bad", v))
	mustSetStatus(t, reg, "tool.bad", v, domain.StatusActive)
	mustSetStatus(t, reg, "tool.bad", v, domain.StatusRevoked)

	got, err := reg.ActiveEntries(ctx)
	if err != nil {
		t.Fatalf("ActiveEntries: %v", err)
	}
	wantIDs := []string{"mcp.fs", "skill.sum", "tool.http"} // ordem estável por id
	if len(got) != len(wantIDs) {
		t.Fatalf("ActiveEntries devolveu %d entradas (%v), quer %v", len(got), idsOf(got), wantIDs)
	}
	for i, e := range got {
		if e.ID != wantIDs[i] {
			t.Fatalf("ordem[%d]=%s, quer %s (ordem estável id,version)", i, e.ID, wantIDs[i])
		}
		if e.Status != domain.StatusActive {
			t.Fatalf("%s estado %s, só active devia entrar", e.ID, e.Status)
		}
		if e.Version.IsZero() {
			t.Fatalf("%s versão não pinada (0.0.0) — resolução flutuante proibida", e.ID)
		}
		if e.Digest == "" {
			t.Fatalf("%s sem digest", e.ID)
		}
	}
}

// TestActiveEntries_SortStableAcrossVersions verifica a ordenação total (id, depois
// version) quando o mesmo id tem várias versões active.
func TestActiveEntries_SortStableAcrossVersions(t *testing.T) {
	t.Parallel()
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	for _, v := range []domain.Version{ver(2, 0, 0), ver(1, 0, 0), ver(1, 5, 0)} {
		mustPublish(t, reg, toolReq("tool.multi", v))
		mustSetStatus(t, reg, "tool.multi", v, domain.StatusActive)
	}
	got, err := reg.ActiveEntries(ctx)
	if err != nil {
		t.Fatalf("ActiveEntries: %v", err)
	}
	want := []string{"1.0.0", "1.5.0", "2.0.0"}
	if len(got) != 3 {
		t.Fatalf("quer 3 versões, got %d", len(got))
	}
	for i, e := range got {
		if e.Version.String() != want[i] {
			t.Fatalf("ordem[%d]=%s, quer %s", i, e.Version.String(), want[i])
		}
	}
}

// TestActiveEntries_DigestMismatchFailsClosed prova que a enumeração é fail-closed
// quanto à integridade (AOS-047): uma entrada active cujo conteúdo já não coincide
// com o digest pinado ABORTA a enumeração com ErrDigestMismatch — nunca se congela
// um tool set adulterado. Modela-se com uma segunda instância do REG sobre o MESMO
// Event Store cujo Digester recalcula um valor diferente.
func TestActiveEntries_DigestMismatchFailsClosed(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	v := ver(1, 0, 0)

	pubReg, err := New(store, WithClock(fixedClock()), WithAdmissionVerifier(allowVerifier{}))
	if err != nil {
		t.Fatalf("New(pub): %v", err)
	}
	mustPublish(t, pubReg, toolReq("tool.x", v))
	mustSetStatus(t, pubReg, "tool.x", v, domain.StatusActive)

	tamperReg, err := New(store,
		WithClock(fixedClock()),
		WithDigester(constDigester{val: "sha256:0000000000000000000000000000000000000000000000000000000000000000"}),
	)
	if err != nil {
		t.Fatalf("New(tamper): %v", err)
	}
	if _, err := tamperReg.ActiveEntries(ctx); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("ActiveEntries com digest divergente = %v, quer ErrDigestMismatch", err)
	}
}

// TestActiveEntries_ClonesIndependent verifica que as entradas devolvidas são
// clones — mutar a fatia devolvida não afecta uma segunda chamada (o estado
// guardado é a ES, imutável para o chamador).
func TestActiveEntries_ClonesIndependent(t *testing.T) {
	t.Parallel()
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	v := ver(1, 0, 0)
	mustPublish(t, reg, toolReq("tool.http", v))
	mustSetStatus(t, reg, "tool.http", v, domain.StatusActive)

	first, err := reg.ActiveEntries(ctx)
	if err != nil {
		t.Fatalf("ActiveEntries: %v", err)
	}
	first[0].ID = "MUTATED"
	first[0].Digest = "MUTATED"

	second, err := reg.ActiveEntries(ctx)
	if err != nil {
		t.Fatalf("ActiveEntries(2): %v", err)
	}
	if second[0].ID != "tool.http" {
		t.Fatalf("mutação do clone vazou para o estado: %s", second[0].ID)
	}
}

// TestActiveEntries_Empty devolve fatia vazia (não erro) quando não há nada active.
func TestActiveEntries_Empty(t *testing.T) {
	t.Parallel()
	reg, _ := newTestRegistry(t)
	got, err := reg.ActiveEntries(context.Background())
	if err != nil {
		t.Fatalf("ActiveEntries: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("quer vazio, got %d", len(got))
	}
}

func idsOf(es []domain.Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	return out
}
