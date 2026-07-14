package provenance_test

import (
	"context"
	"testing"

	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/provenance"
)

// TestPartition_RoutesBySealedProvenance prova que a admissão encaminha cada
// registo pela sua proveniência SELADA — trusted para o control-plane, untrusted
// para a quarentena — e que os dois caminhos são disjuntos.
func TestPartition_RoutesBySealedProvenance(t *testing.T) {
	t.Parallel()
	ing := provenance.NewIngestor(nil)
	part := provenance.NewPartition(nil)

	sources := []struct {
		id  string
		src provenance.Source
	}{
		{"t-sys", provenance.SourceSystem},             // trusted
		{"t-user", provenance.SourceAuthenticatedUser}, // trusted
		{"u-tool", provenance.SourceToolResult},        // untrusted
		{"u-web", provenance.SourceWeb},                // untrusted
		{"u-mcp", provenance.SourceMCPSchema},          // untrusted
	}
	for _, s := range sources {
		in, err := ing.Ingest(context.Background(), semanticRecord(s.id, ""), s.src)
		if err != nil {
			t.Fatalf("Ingest %s: %v", s.id, err)
		}
		part.Admit(in)
	}

	if got := part.TrustedView().Len(); got != 2 {
		t.Fatalf("control-plane tem %d entradas, esperava 2 (só trusted)", got)
	}
	if got := part.Quarantine().Len(); got != 3 {
		t.Fatalf("quarentena tem %d registos, esperava 3 (só untrusted)", got)
	}

	// O control-plane só contém memória trusted (nenhum id untrusted vazou).
	for _, e := range part.TrustedView().Entries() {
		id := e.Record().ID
		if id != "t-sys" && id != "t-user" {
			t.Fatalf("id untrusted %q vazou para o control-plane", id)
		}
	}
	// A quarentena serve tudo como dados untrusted.
	for _, item := range part.Quarantine().Items() {
		if item.Taint() != provenance.Untrusted {
			t.Fatalf("item em quarentena com taint %q, esperava untrusted", item.Taint())
		}
	}
}

// TestQuarantine_ServesClonedData prova que os DataItem servem CLONES (o estado em
// quarentena nunca é partilhado mutavelmente com o consumidor).
func TestQuarantine_ServesClonedData(t *testing.T) {
	t.Parallel()
	ing := provenance.NewIngestor(nil)
	part := provenance.NewPartition(nil)

	in, err := ing.Ingest(context.Background(), semanticRecord("u-1", ""), provenance.SourceWeb)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	part.Admit(in)

	item := part.Quarantine().Items()[0]
	content := item.Content()
	// Mutar o clone não deve afetar o próximo Content().
	content.Metadata.Provenance = provenance.Trusted
	again := part.Quarantine().Items()[0].Content()
	if again.Metadata.Provenance != provenance.Untrusted {
		t.Fatal("mutação de um clone de DataItem alterou o estado em quarentena")
	}
}

// TestReferenceDataPlane_MarksUntrusted confirma que a impl de referência do
// data-plane marca sempre o conteúdo como untrusted.
func TestReferenceDataPlane_MarksUntrusted(t *testing.T) {
	t.Parallel()
	dp := provenance.ReferenceDataPlane{}
	item := dp.Serve(semanticRecord("x", provenance.Trusted))
	if item.Taint() != provenance.Untrusted {
		t.Fatalf("data-plane marcou %q, esperava untrusted", item.Taint())
	}
}

// TestValidationMethod_Valid cobre o predicado canónico do método de validação.
func TestValidationMethod_Valid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		m    provenance.ValidationMethod
		want bool
	}{
		{provenance.ValidationPolicy, true},
		{provenance.ValidationHuman, true},
		{provenance.ValidationMethod("auto"), false},
		{provenance.ValidationMethod(""), false},
	}
	for _, tt := range tests {
		if got := tt.m.Valid(); got != tt.want {
			t.Fatalf("ValidationMethod(%q).Valid()=%v, esperava %v", tt.m, got, tt.want)
		}
	}
}

// TestTrustedEntry_Record confirma que TrustedEntry devolve o registo trusted.
func TestTrustedEntry_Record(t *testing.T) {
	t.Parallel()
	ing := provenance.NewIngestor(nil)
	part := provenance.NewPartition(nil)
	in, _ := ing.Ingest(context.Background(), semanticRecord("t-1", ""), provenance.SourceSystem)
	part.Admit(in)
	rec := part.TrustedView().Entries()[0].Record()
	if rec.Class != domain.ClassSemantic || rec.ID != "t-1" {
		t.Fatalf("registo inesperado: %+v", rec)
	}
}
