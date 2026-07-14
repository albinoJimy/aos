package memory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	memory "github.com/aos-ref/platform/memory"
	"github.com/aos-ref/platform/memory/adapters"
	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/ports"
	"github.com/aos-ref/platform/memory/provenance"
)

var fixed = time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)

func newSvc(t *testing.T) *memory.Service {
	t.Helper()
	return memory.NewService(
		adapters.NewInMemoryAdapter(),
		memory.WithClock(func() time.Time { return fixed }),
	)
}

func metaTrusted() domain.Metadata {
	return domain.Metadata{
		AgentID:       "agent-a",
		RunID:         "run-1",
		Provenance:    domain.ProvenanceTrusted,
		TTLClass:      domain.TTLStandard,
		SchemaVersion: "1.0.0",
		// CreatedAt deixado a zero de propósito: a fachada preenche-o.
	}
}

// TestService_Remember_FillsSystemFields prova que a fachada preenche created_at
// (relógio injectado) e o id (gerador), e persiste um registo válido.
func TestService_Remember_FillsSystemFields(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()

	rec, err := svc.Remember(ctx, domain.ClassEpisodic, provenance.SourceSystem, metaTrusted(),
		domain.EpisodicBody{TraceID: "t", Outcome: "success"})
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if rec.ID == "" {
		t.Fatal("id nao foi gerado")
	}
	if !rec.Metadata.CreatedAt.Equal(fixed) {
		t.Fatalf("created_at nao foi preenchido pelo relogio: %v", rec.Metadata.CreatedAt)
	}
	got, err := svc.Get(ctx, domain.ClassEpisodic, rec.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != rec.ID {
		t.Fatalf("id divergente: %q vs %q", got.ID, rec.ID)
	}
}

// TestService_DeterministicIDs prova que o gerador injectado torna os ids
// determinísticos (sem rand).
func TestService_DeterministicIDs(t *testing.T) {
	seq := 0
	gen := func() string { seq++; return "fixed-id" }
	svc := memory.NewService(
		adapters.NewInMemoryAdapter(),
		memory.WithClock(func() time.Time { return fixed }),
		memory.WithIDGenerator(gen),
	)
	ctx := context.Background()
	r, err := svc.Remember(ctx, domain.ClassWorking, provenance.SourceSystem, metaTrusted(), domain.WorkingBody{TurnIndex: 1})
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if r.ID != "fixed-id" {
		t.Fatalf("gerador injectado ignorado: %q", r.ID)
	}
}

// TestService_FailClosed_NoSilentDefaults prova que a fachada NÃO inventa
// schema_version: a sua ausência falha-fecha já no serviço.
func TestService_FailClosed_NoSilentDefaults(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()

	t.Run("no_schema_version", func(t *testing.T) {
		m := metaTrusted()
		m.SchemaVersion = ""
		body := domain.SemanticBody{Subject: "s", Predicate: "p", Object: "o"}
		if _, err := svc.Remember(ctx, domain.ClassSemantic, provenance.SourceSystem, m, body); !errors.Is(err, domain.ErrMissingSchemaVersion) {
			t.Fatalf("quer ErrMissingSchemaVersion, obteve %v", err)
		}
	})
}

// TestService_ImposesProvenanceFromSource é a prova do AOS042-C2: a fachada faz a
// marcação automática ESTRUTURAL — deriva a proveniência da FONTE e sobrepõe-se ao
// campo do chamador. Conteúdo de tool_result NÃO pode ser escrito como trusted,
// mesmo que o chamador ponha Provenance=trusted; e a fonte é persistida.
func TestService_ImposesProvenanceFromSource(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	body := domain.SemanticBody{Subject: "s", Predicate: "p", Object: "o"}

	// O chamador TENTA forjar trusted em meta...
	m := metaTrusted()
	rec, err := svc.Remember(ctx, domain.ClassSemantic, provenance.SourceToolResult, m, body)
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}
	// ...mas a fonte tool_result impõe untrusted (a tag in-band não é autoridade).
	if rec.Metadata.Provenance != domain.ProvenanceUntrusted {
		t.Fatalf("a fachada devia impor untrusted para tool_result, obteve %q", rec.Metadata.Provenance)
	}
	if rec.Metadata.Source != domain.SourceToolResult {
		t.Fatalf("fonte não persistida: %q, esperava tool_result", rec.Metadata.Source)
	}

	// Confirma-se após leitura (a imposição sobrevive ao round-trip do backend).
	got, err := svc.Get(ctx, domain.ClassSemantic, rec.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Metadata.Provenance != domain.ProvenanceUntrusted || got.Metadata.Source != domain.SourceToolResult {
		t.Fatalf("registo lido não reflete a imposição: prov=%q source=%q", got.Metadata.Provenance, got.Metadata.Source)
	}

	// Via Put, o mesmo: um registo montado trusted pelo chamador é reclassificado
	// pela fonte web → untrusted.
	in := domain.Record{ID: "caller-x", Class: domain.ClassSemantic, Metadata: metaTrusted(), Body: body}
	out, err := svc.Put(ctx, in, provenance.SourceWeb)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if out.Metadata.Provenance != domain.ProvenanceUntrusted || out.Metadata.Source != domain.SourceWeb {
		t.Fatalf("Put não impôs untrusted/web: prov=%q source=%q", out.Metadata.Provenance, out.Metadata.Source)
	}
}

// TestService_Put_RespectsCallerID prova que Put respeita o id fornecido e
// preenche created_at se estiver a zero.
func TestService_Put_RespectsCallerID(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	in := domain.Record{
		ID:       "caller-chosen",
		Class:    domain.ClassSemantic,
		Metadata: metaTrusted(),
		Body:     domain.SemanticBody{Subject: "a", Predicate: "b", Object: "c"},
	}
	out, err := svc.Put(ctx, in, provenance.SourceSystem)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if out.ID != "caller-chosen" {
		t.Fatalf("id do chamador ignorado: %q", out.ID)
	}
	if !out.Metadata.CreatedAt.Equal(fixed) {
		t.Fatalf("created_at nao preenchido: %v", out.Metadata.CreatedAt)
	}
}

// TestService_PortVersion prova que a fachada expõe a versão do contrato.
func TestService_PortVersion(t *testing.T) {
	svc := newSvc(t)
	if svc.PortVersion() != ports.PortVersion {
		t.Fatalf("PortVersion = %q, quer %q", svc.PortVersion(), ports.PortVersion)
	}
}

// TestService_QueryAndDelete cobre a delegação de Query/Delete pela fachada.
func TestService_QueryAndDelete(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	r, err := svc.Remember(ctx, domain.ClassProcedural, provenance.SourceSystem, metaTrusted(),
		domain.ProceduralBody{SkillName: "sk", Version: "1.0.0", Stage: "staging"})
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}
	q, err := svc.Query(ctx, ports.Query{Class: domain.ClassProcedural})
	if err != nil || len(q) != 1 {
		t.Fatalf("Query: len=%d err=%v", len(q), err)
	}
	dc := ports.DeleteContext{AgentID: "agent-a", RunID: "run-1", Provenance: domain.ProvenanceTrusted}
	if err := svc.Delete(ctx, domain.ClassProcedural, r.ID, dc); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(ctx, domain.ClassProcedural, r.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("registo devia estar apagado, err=%v", err)
	}
}
