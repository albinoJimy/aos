package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/platform/memory/domain"
)

var ts = time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)

func validMeta() domain.Metadata {
	return domain.Metadata{
		AgentID:       "agent-a",
		RunID:         "run-1",
		Provenance:    domain.ProvenanceTrusted,
		CreatedAt:     ts,
		TTLClass:      domain.TTLStandard,
		SchemaVersion: "1.0.0",
	}
}

// TestMetadata_Validate_FailClosed cobre a tabela de metadados obrigatórios: a
// ausência de CADA um falha-fecha com o erro sentinela respectivo.
func TestMetadata_Validate_FailClosed(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*domain.Metadata)
		wantErr error
	}{
		{"valid", func(*domain.Metadata) {}, nil},
		{"missing_agent_id", func(m *domain.Metadata) { m.AgentID = "" }, domain.ErrMissingAgentID},
		{"missing_run_id", func(m *domain.Metadata) { m.RunID = "" }, domain.ErrMissingRunID},
		{"missing_provenance", func(m *domain.Metadata) { m.Provenance = "" }, domain.ErrMissingProvenance},
		{"invalid_provenance", func(m *domain.Metadata) { m.Provenance = "maybe" }, domain.ErrMissingProvenance},
		{"missing_created_at", func(m *domain.Metadata) { m.CreatedAt = time.Time{} }, domain.ErrMissingCreatedAt},
		{"missing_ttl_class", func(m *domain.Metadata) { m.TTLClass = "" }, domain.ErrMissingTTLClass},
		{"invalid_ttl_class", func(m *domain.Metadata) { m.TTLClass = "forever-ish" }, domain.ErrMissingTTLClass},
		{"missing_schema_version", func(m *domain.Metadata) { m.SchemaVersion = "" }, domain.ErrMissingSchemaVersion},
		{"source_absent_ok", func(m *domain.Metadata) { m.Source = "" }, nil},
		{"source_canonical_ok", func(m *domain.Metadata) { m.Source = domain.SourceToolResult }, nil},
		{"source_non_canonical", func(m *domain.Metadata) { m.Source = "hackernews" }, domain.ErrInvalidProvenanceSource},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validMeta()
			tc.mutate(&m)
			err := m.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("quer nil, obteve %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("quer %v, obteve %v", tc.wantErr, err)
			}
		})
	}
}

// TestRecord_Validate cobre as invariantes do registo, incluindo o mismatch de
// classe/corpo (as quatro classes não se cruzam ao nível do domínio).
func TestRecord_Validate(t *testing.T) {
	base := domain.Record{
		ID:       "id1",
		Class:    domain.ClassEpisodic,
		Metadata: validMeta(),
		Body:     domain.EpisodicBody{TraceID: "t"},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("registo valido rejeitado: %v", err)
	}

	cases := []struct {
		name    string
		mutate  func(*domain.Record)
		wantErr error
	}{
		{"missing_id", func(r *domain.Record) { r.ID = "" }, domain.ErrMissingID},
		{"invalid_class", func(r *domain.Record) { r.Class = "bogus" }, domain.ErrInvalidClass},
		{"nil_body", func(r *domain.Record) { r.Body = nil }, domain.ErrNilBody},
		{"class_mismatch", func(r *domain.Record) { r.Body = domain.SemanticBody{} }, domain.ErrClassMismatch},
		{"bad_metadata", func(r *domain.Record) { r.Metadata.SchemaVersion = "" }, domain.ErrMissingSchemaVersion},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			tc.mutate(&r)
			if err := r.Validate(); !errors.Is(err, tc.wantErr) {
				t.Fatalf("quer %v, obteve %v", tc.wantErr, err)
			}
		})
	}
}

// TestClass_ValidAndAll cobre o enum das quatro classes.
func TestClass_ValidAndAll(t *testing.T) {
	all := domain.AllClasses()
	if len(all) != 4 {
		t.Fatalf("quer 4 classes, obteve %d", len(all))
	}
	for _, c := range all {
		if !c.Valid() {
			t.Fatalf("classe %s devia ser valida", c)
		}
	}
	if domain.MemoryClass("nope").Valid() {
		t.Fatal("classe desconhecida nao devia validar")
	}
	// Cada classe tem um Body tipado distinto.
	if (domain.EpisodicBody{}).Class() != domain.ClassEpisodic ||
		(domain.SemanticBody{}).Class() != domain.ClassSemantic ||
		(domain.ProceduralBody{}).Class() != domain.ClassProcedural ||
		(domain.WorkingBody{}).Class() != domain.ClassWorking {
		t.Fatal("mapeamento Body->Class inconsistente")
	}
}

// TestCodec_RoundTrip prova que a serialização persistida é estável e reversível
// para as quatro classes (mesma forma partilhada pelos dois adaptadores).
func TestCodec_RoundTrip(t *testing.T) {
	recs := []domain.Record{
		{ID: "e", Class: domain.ClassEpisodic, Metadata: validMeta(), Body: domain.EpisodicBody{TraceID: "t", Goal: "g", Outcome: "success", StepCount: 2, Summary: "s"}},
		{ID: "s", Class: domain.ClassSemantic, Metadata: validMeta(), Body: domain.SemanticBody{Subject: "a", Predicate: "b", Object: "c", Confidence: 0.5}},
		{ID: "p", Class: domain.ClassProcedural, Metadata: validMeta(), Body: domain.ProceduralBody{SkillName: "sk", Version: "1.2.3", DefinitionHash: "h", Stage: "canary"}},
		{ID: "w", Class: domain.ClassWorking, Metadata: validMeta(), Body: domain.WorkingBody{TurnIndex: 3, Content: "ctx", TokenCount: 7}},
	}
	for _, in := range recs {
		t.Run(string(in.Class), func(t *testing.T) {
			raw, err := domain.MarshalRecord(in)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			out, err := domain.UnmarshalRecord(raw)
			if err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if out.ID != in.ID || out.Class != in.Class {
				t.Fatalf("identidade mudou: %+v", out)
			}
			if out.Body.Class() != in.Class {
				t.Fatalf("body class mudou: %s", out.Body.Class())
			}
			if !out.Metadata.CreatedAt.Equal(in.Metadata.CreatedAt) {
				t.Fatalf("created_at mudou: %v", out.Metadata.CreatedAt)
			}
			if err := out.Validate(); err != nil {
				t.Fatalf("round-trip nao valida: %v", err)
			}
		})
	}
}

// TestCodec_PersistsSource prova o AOS042-C1 ao nível da serialização: a FONTE
// forense sobrevive ao round-trip persistido (o "de onde veio" não se perde na
// escrita), e um registo sem fonte continua a round-tripar (retro-compatível).
func TestCodec_PersistsSource(t *testing.T) {
	m := validMeta()
	m.Provenance = domain.ProvenanceUntrusted
	m.Source = domain.SourceWeb
	in := domain.Record{ID: "src", Class: domain.ClassSemantic, Metadata: m, Body: domain.SemanticBody{Subject: "a", Predicate: "b", Object: "c"}}

	raw, err := domain.MarshalRecord(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	out, err := domain.UnmarshalRecord(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out.Metadata.Source != domain.SourceWeb {
		t.Fatalf("fonte não sobreviveu ao round-trip: %q", out.Metadata.Source)
	}

	// Sem fonte: campo omitido no envelope e round-trip permanece válido.
	noSrc := validMeta()
	rec2 := domain.Record{ID: "nosrc", Class: domain.ClassSemantic, Metadata: noSrc, Body: domain.SemanticBody{Subject: "a", Predicate: "b", Object: "c"}}
	raw2, _ := domain.MarshalRecord(rec2)
	out2, err := domain.UnmarshalRecord(raw2)
	if err != nil {
		t.Fatalf("Unmarshal sem fonte: %v", err)
	}
	if out2.Metadata.Source != "" {
		t.Fatalf("fonte devia ser vazia, obteve %q", out2.Metadata.Source)
	}
	if err := out2.Validate(); err != nil {
		t.Fatalf("registo sem fonte devia validar: %v", err)
	}
}

// TestCodec_CorruptFailsClosed prova que payloads corrompidos falham-fecham.
func TestCodec_CorruptFailsClosed(t *testing.T) {
	if _, err := domain.UnmarshalRecord([]byte("{not json")); !errors.Is(err, domain.ErrCorruptRecord) {
		t.Fatalf("quer ErrCorruptRecord, obteve %v", err)
	}
	if _, err := domain.UnmarshalRecord([]byte(`{"id":"x","class":"unknown","body":{}}`)); !errors.Is(err, domain.ErrCorruptRecord) {
		t.Fatalf("classe desconhecida devia falhar-fechar, obteve %v", err)
	}
}
