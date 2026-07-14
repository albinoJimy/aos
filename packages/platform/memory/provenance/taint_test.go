package provenance_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/provenance"
)

// fixedTime é um relógio determinístico para os testes (sem time.Now no caminho).
var fixedTime = time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)

// semanticRecord constrói um Record semântico VÁLIDO (todos os metadados
// obrigatórios preenchidos), exceto a proveniência, que os testes controlam.
func semanticRecord(id string, prov domain.Provenance) domain.Record {
	return domain.Record{
		ID:    id,
		Class: domain.ClassSemantic,
		Metadata: domain.Metadata{
			AgentID:       "agent-1",
			RunID:         "run-1",
			Provenance:    prov,
			CreatedAt:     fixedTime,
			TTLClass:      domain.TTLStandard,
			SchemaVersion: "1.0.0",
		},
		Body: domain.SemanticBody{Subject: "s", Predicate: "p", Object: "o", Confidence: 0.9},
	}
}

// TestClassify_AutoMarkUntrusted prova a marcação AUTOMÁTICA de untrusted na
// ingestão a partir da FONTE — tool result / web / MCP são untrusted; só
// system/utilizador-autenticado é trusted; uma fonte desconhecida cai em untrusted
// (fail-closed).
func TestClassify_AutoMarkUntrusted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  provenance.Source
		want domain.Provenance
	}{
		{"system e trusted", provenance.SourceSystem, provenance.Trusted},
		{"utilizador autenticado e trusted", provenance.SourceAuthenticatedUser, provenance.Trusted},
		{"tool result e untrusted", provenance.SourceToolResult, provenance.Untrusted},
		{"web e untrusted", provenance.SourceWeb, provenance.Untrusted},
		{"schema MCP e untrusted", provenance.SourceMCPSchema, provenance.Untrusted},
		{"memoria derivada nao se classifica por fonte -> untrusted", provenance.SourceDerivedMemory, provenance.Untrusted},
		{"fonte desconhecida cai em untrusted (fail-closed)", provenance.Source("desconhecida"), provenance.Untrusted},
		{"fonte vazia cai em untrusted (fail-closed)", provenance.Source(""), provenance.Untrusted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := provenance.Classify(tt.src); got != tt.want {
				t.Fatalf("Classify(%q)=%q, esperava %q", tt.src, got, tt.want)
			}
		})
	}
}

// TestIngest_ImposesProvenanceFromSource prova que a ingestão IMPÕE a proveniência
// classificada da fonte, prevalecendo sobre o que o chamador tenha posto no campo:
// um registo "marcado" trusted pelo chamador mas ingerido de uma tool result é
// SELADO como untrusted (a marcação automática é a autoridade).
func TestIngest_ImposesProvenanceFromSource(t *testing.T) {
	t.Parallel()
	ing := provenance.NewIngestor(nil) // impl de referencia
	// O chamador tenta forjar trusted...
	rec := semanticRecord("mem-1", provenance.Trusted)

	// ...mas a fonte e uma tool result: a ingestao impoe untrusted.
	got, err := ing.Ingest(context.Background(), rec, provenance.SourceToolResult)
	if err != nil {
		t.Fatalf("Ingest inesperado: %v", err)
	}
	if got.Provenance() != provenance.Untrusted {
		t.Fatalf("proveniencia selada=%q, esperava untrusted (a fonte prevalece)", got.Provenance())
	}
	if got.IsTrusted() {
		t.Fatal("IsTrusted devia ser false para conteudo de tool result")
	}
	// E um registo de system e selado trusted mesmo que o campo venha vazio.
	trusted, err := ing.Ingest(context.Background(), semanticRecord("mem-2", ""), provenance.SourceSystem)
	if err != nil {
		t.Fatalf("Ingest system inesperado: %v", err)
	}
	if !trusted.IsTrusted() {
		t.Fatal("conteudo de system devia ser trusted")
	}
}

// TestDerive_TransitiveTaint prova a propagação TRANSITIVA do taint: memória
// derivada de untrusted herda untrusted, uma mistura trusted+untrusted resulta
// untrusted (contágio, sem lavagem), e só todos-trusted resulta trusted.
func TestDerive_TransitiveTaint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		parents []domain.Provenance
		want    domain.Provenance
	}{
		{"sem pais e untrusted (fail-closed)", nil, provenance.Untrusted},
		{"um pai trusted -> trusted", []domain.Provenance{provenance.Trusted}, provenance.Trusted},
		{"todos trusted -> trusted", []domain.Provenance{provenance.Trusted, provenance.Trusted}, provenance.Trusted},
		{"um pai untrusted -> untrusted", []domain.Provenance{provenance.Untrusted}, provenance.Untrusted},
		{"mistura trusted+untrusted -> untrusted (sem lavagem)", []domain.Provenance{provenance.Trusted, provenance.Untrusted}, provenance.Untrusted},
		{"untrusted+trusted (ordem inversa) -> untrusted", []domain.Provenance{provenance.Untrusted, provenance.Trusted}, provenance.Untrusted},
		{"valor nao-canonico contamina -> untrusted", []domain.Provenance{provenance.Trusted, domain.Provenance("x")}, provenance.Untrusted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := provenance.Derive(tt.parents...); got != tt.want {
				t.Fatalf("Derive(%v)=%q, esperava %q", tt.parents, got, tt.want)
			}
			// A porta (DefaultTaintController) e a funcao pura concordam.
			if got := (provenance.DefaultTaintController{}).Derive(tt.parents...); got != tt.want {
				t.Fatalf("DefaultTaintController.Derive(%v)=%q, esperava %q", tt.parents, got, tt.want)
			}
		})
	}
}

// TestIngestDerived_InheritsTaint prova, ao nível da ingestão, que memória
// derivada de PELO MENOS uma fonte untrusted é selada untrusted (herança
// transitiva no caminho de escrita real). Os pais são [provenance.Ingested] REAIS
// (selados) — o taint deriva do selo de cada pai, não de valores afirmados.
func TestIngestDerived_InheritsTaint(t *testing.T) {
	t.Parallel()
	ing := provenance.NewIngestor(nil)

	// Pais SELADOS a partir das suas fontes reais.
	trustedParent, err := ing.Ingest(context.Background(), semanticRecord("p-sys", ""), provenance.SourceSystem)
	if err != nil {
		t.Fatalf("Ingest pai trusted: %v", err)
	}
	untrustedParent, err := ing.Ingest(context.Background(), semanticRecord("p-tool", ""), provenance.SourceToolResult)
	if err != nil {
		t.Fatalf("Ingest pai untrusted: %v", err)
	}

	derived, err := ing.IngestDerived(context.Background(), semanticRecord("mem-d", provenance.Trusted),
		trustedParent, untrustedParent) // mistura de pais REAIS
	if err != nil {
		t.Fatalf("IngestDerived inesperado: %v", err)
	}
	if derived.Provenance() != provenance.Untrusted {
		t.Fatalf("derivado de mistura=%q, esperava untrusted (contagio)", derived.Provenance())
	}

	allTrusted, err := ing.IngestDerived(context.Background(), semanticRecord("mem-t", ""),
		trustedParent, trustedParent)
	if err != nil {
		t.Fatalf("IngestDerived trusted inesperado: %v", err)
	}
	if !allTrusted.IsTrusted() {
		t.Fatal("derivado de todos-trusted devia ser trusted")
	}

	// Sem pais → untrusted (fail-closed), no caminho de escrita real.
	orphan, err := ing.IngestDerived(context.Background(), semanticRecord("mem-o", ""))
	if err != nil {
		t.Fatalf("IngestDerived sem pais: %v", err)
	}
	if orphan.IsTrusted() {
		t.Fatal("derivado sem pais devia ser untrusted (fail-closed)")
	}
}

// TestIngestDerived_NoLaunderingViaForgedParent prova que a derivação NÃO se lava
// por afirmação incorrecta: como os pais são [provenance.Ingested] selados, um
// chamador não consegue "afirmar" trusted para memória de facto derivada de
// untrusted — o taint lê-se do selo do pai untrusted real.
func TestIngestDerived_NoLaunderingViaForgedParent(t *testing.T) {
	t.Parallel()
	ing := provenance.NewIngestor(nil)

	untrustedParent, err := ing.Ingest(context.Background(), semanticRecord("real-untrusted", ""), provenance.SourceWeb)
	if err != nil {
		t.Fatalf("Ingest pai untrusted: %v", err)
	}
	// O chamador marca o registo derivado como trusted na tag in-band...
	derived, err := ing.IngestDerived(context.Background(), semanticRecord("laundered", provenance.Trusted), untrustedParent)
	if err != nil {
		t.Fatalf("IngestDerived: %v", err)
	}
	// ...mas o selo do pai untrusted contamina: o derivado é untrusted.
	if derived.Provenance() != provenance.Untrusted {
		t.Fatalf("derivado=%q, esperava untrusted (sem lavagem via pai forjado)", derived.Provenance())
	}
}

// TestSeal_RejectsMissingProvenance prova o fail-closed "escrita sem proveniência
// REJEITADA" ao nível desta camada: um registo com proveniência ausente ou
// não-canónica não pode ser admitido.
func TestSeal_RejectsMissingProvenance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		prov domain.Provenance
	}{
		{"proveniencia vazia", domain.Provenance("")},
		{"proveniencia nao-canonica", domain.Provenance("maybe")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := provenance.Seal(semanticRecord("mem-x", tt.prov))
			if !errors.Is(err, domain.ErrMissingProvenance) {
				t.Fatalf("Seal com prov=%q: erro=%v, esperava ErrMissingProvenance", tt.prov, err)
			}
		})
	}

	// Um registo com proveniencia canonica e admitido e sela o valor tal-qual.
	sealed, err := provenance.Seal(semanticRecord("mem-ok", provenance.Untrusted))
	if err != nil {
		t.Fatalf("Seal valido inesperado: %v", err)
	}
	if sealed.Provenance() != provenance.Untrusted {
		t.Fatalf("Seal preservou %q, esperava untrusted", sealed.Provenance())
	}
}

// TestSeal_RejectsInvalidRecord confirma que Seal também falha-fecha noutros
// metadados obrigatórios em falta (não só a proveniência).
func TestSeal_RejectsInvalidRecord(t *testing.T) {
	t.Parallel()
	rec := semanticRecord("", provenance.Trusted) // id vazio
	if _, err := provenance.Seal(rec); !errors.Is(err, domain.ErrMissingID) {
		t.Fatalf("Seal sem id: erro=%v, esperava ErrMissingID", err)
	}
}

// TestSeal_RejectsTrustedInBandTag é a prova central do AOS042-Q1: Seal NÃO pode
// fabricar memória trusted a partir da tag in-band Metadata.Provenance. Um registo
// VÁLIDO marcado trusted é rejeitado — a promoção a trusted só é legítima via
// Ingest (classificação pela fonte) ou Promote (audit). Sem esta guarda, bastava
// Seal(record com Provenance=trusted) para aterrar no control-plane sem Classify
// nem audit.
func TestSeal_RejectsTrustedInBandTag(t *testing.T) {
	t.Parallel()
	rec := semanticRecord("mem-forge", provenance.Trusted) // registo válido, tag trusted
	if _, err := provenance.Seal(rec); !errors.Is(err, provenance.ErrSealTrustedForbidden) {
		t.Fatalf("Seal de tag trusted: erro=%v, esperava ErrSealTrustedForbidden", err)
	}

	// E a barreira é confirmada end-to-end: mesmo que se tentasse admitir, não há
	// Ingested trusted produzido por Seal, logo nada chega à TrustedView por esta via.
	if _, err := provenance.Seal(semanticRecord("ok-untrusted", provenance.Untrusted)); err != nil {
		t.Fatalf("Seal untrusted válido devia passar: %v", err)
	}
}

// TestIngest_PersistsSource prova o AOS042-C1: a ingestão ESTAMPA a fonte no
// registo persistido (não só a classificação), completando o triplo (fonte,
// classificação, run_id).
func TestIngest_PersistsSource(t *testing.T) {
	t.Parallel()
	ing := provenance.NewIngestor(nil)
	cases := []struct {
		src  provenance.Source
		want domain.ProvenanceSource
	}{
		{provenance.SourceToolResult, domain.SourceToolResult},
		{provenance.SourceWeb, domain.SourceWeb},
		{provenance.SourceMCPSchema, domain.SourceMCPSchema},
		{provenance.SourceSystem, domain.SourceSystem},
	}
	for _, c := range cases {
		in, err := ing.Ingest(context.Background(), semanticRecord("mem-src", ""), c.src)
		if err != nil {
			t.Fatalf("Ingest %q: %v", c.src, err)
		}
		if got := in.Record().Metadata.Source; got != c.want {
			t.Fatalf("Source persistida=%q, esperava %q (fonte não sobreviveu à escrita)", got, c.want)
		}
	}
}

// TestIngested_ProvenanceIsImmutable prova que a proveniência SELADA é imutável:
// mutar o registo do chamador APÓS a ingestão não altera o selo (a ingestão clona;
// não há mutador exposto para o campo prov).
func TestIngested_ProvenanceIsImmutable(t *testing.T) {
	t.Parallel()
	ing := provenance.NewIngestor(nil)
	rec := semanticRecord("mem-imut", provenance.Trusted)

	got, err := ing.Ingest(context.Background(), rec, provenance.SourceToolResult)
	if err != nil {
		t.Fatalf("Ingest inesperado: %v", err)
	}
	if got.Provenance() != provenance.Untrusted {
		t.Fatalf("selo inicial=%q, esperava untrusted", got.Provenance())
	}

	// O chamador tenta "lavar" o taint mutando o registo original...
	rec.Metadata.Provenance = provenance.Trusted
	// ...e mutando o clone devolvido por Record().
	clone := got.Record()
	clone.Metadata.Provenance = provenance.Trusted

	// O selo permanece untrusted (nem a mutacao do original nem a do clone o tocam).
	if got.Provenance() != provenance.Untrusted {
		t.Fatalf("selo apos mutacao externa=%q, esperava untrusted (imutavel)", got.Provenance())
	}
	if got.Record().Metadata.Provenance != provenance.Untrusted {
		t.Fatal("o registo selado nao devia refletir a mutacao externa")
	}
}
