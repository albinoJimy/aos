package integritytests

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/memory/provenance"
)

// TestProvenanceQuarantineCannotAuthorize — conteúdo derivado de tool_result entra em
// quarentena (untrusted), é servido como DataItem e é ESTRUTURALMENTE incapaz de
// autorizar; simetricamente, memória trusted (system) autoriza uma tool call genuína.
func TestProvenanceQuarantineCannotAuthorize(t *testing.T) {
	ctx := context.Background()

	// Barreira: untrusted em quarentena não autoriza (verificador partilhado).
	if err := verifyToolResultQuarantined(ctx, provenance.DefaultTaintController{}); err != nil {
		t.Fatalf("quarentena furada: %v", err)
	}

	// Simétrico: memória trusted (system) produz uma autorização GENUÍNA.
	ing := provenance.NewIngestor(provenance.DefaultTaintController{})
	trusted, err := ing.Ingest(ctx, semanticRec("kb-system"), provenance.SourceSystem)
	if err != nil {
		t.Fatalf("Ingest(system): %v", err)
	}
	part := provenance.NewPartition(nil)
	part.Admit(trusted)
	if part.TrustedView().Len() != 1 {
		t.Fatalf("TrustedView.Len = %d, quero 1", part.TrustedView().Len())
	}
	authz := part.TrustedView().Entries()[0].AuthorizeToolCall("fs:read:/x")
	if !authz.Granted() {
		t.Fatal("memória trusted devia autorizar uma tool call genuína (Granted)")
	}
}

// TestProvenanceTaintTransitive — o taint propaga TRANSITIVAMENTE e é contagioso: uma
// mistura trusted+untrusted resulta untrusted (sem lavagem), e derivar de memória
// untrusted-derivada continua untrusted.
func TestProvenanceTaintTransitive(t *testing.T) {
	ctx := context.Background()
	ing := provenance.NewIngestor(provenance.DefaultTaintController{})

	tp, err := ing.Ingest(ctx, semanticRec("p-system"), provenance.SourceSystem) // trusted
	if err != nil {
		t.Fatalf("Ingest(system): %v", err)
	}
	up, err := ing.Ingest(ctx, semanticRec("p-web"), provenance.SourceWeb) // untrusted
	if err != nil {
		t.Fatalf("Ingest(web): %v", err)
	}

	// Mistura trusted + untrusted → untrusted (contágio, sem lavagem).
	mix, err := ing.IngestDerived(ctx, semanticRec("d-mix"), tp, up)
	if err != nil {
		t.Fatalf("IngestDerived(mix): %v", err)
	}
	if mix.Provenance() != provenance.Untrusted {
		t.Fatalf("mistura trusted+untrusted = %v, quero untrusted", mix.Provenance())
	}

	// Todos trusted → trusted.
	allTrusted, err := ing.IngestDerived(ctx, semanticRec("d-trusted"), tp)
	if err != nil {
		t.Fatalf("IngestDerived(trusted): %v", err)
	}
	if allTrusted.Provenance() != provenance.Trusted {
		t.Fatalf("derivação all-trusted = %v, quero trusted", allTrusted.Provenance())
	}

	// TRANSITIVO: derivar de memória untrusted-derivada permanece untrusted.
	transitive, err := ing.IngestDerived(ctx, semanticRec("d-transitive"), mix)
	if err != nil {
		t.Fatalf("IngestDerived(transitive): %v", err)
	}
	if transitive.Provenance() != provenance.Untrusted {
		t.Fatalf("derivação de untrusted-derivado = %v, quero untrusted", transitive.Provenance())
	}
}

// TestProvenancePromotionAudited — a promoção untrusted→trusted exige validação
// EXPLÍCITA (senão é recusada) e sela-se na hash-chain tamper-evident (que verifica).
func TestProvenancePromotionAudited(t *testing.T) {
	ctx := context.Background()
	store := audit.NewMemStore()
	prom, err := provenance.NewPromoter(store, provenance.WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewPromoter: %v", err)
	}
	ing := provenance.NewIngestor(provenance.DefaultTaintController{})
	un, err := ing.Ingest(ctx, semanticRec("kb-web"), provenance.SourceWeb) // untrusted
	if err != nil {
		t.Fatalf("Ingest(web): %v", err)
	}

	// Sem validação explícita: RECUSADA (fail-closed).
	if _, _, err := prom.Promote(ctx, provenance.PromotionRequest{
		Entry: un, RunID: "run-1", AgentID: "agent-1",
	}); !errors.Is(err, provenance.ErrPromotionNotValidated) {
		t.Fatalf("promoção sem validação devolveu %v, quero ErrPromotionNotValidated", err)
	}

	// Com validação humana: promove e regista na cadeia.
	promoted, rec, err := prom.Promote(ctx, provenance.PromotionRequest{
		Entry: un, Method: provenance.ValidationHuman, Validator: "alice",
		RunID: "run-1", AgentID: "agent-1",
	})
	if err != nil {
		t.Fatalf("Promote (validada): %v", err)
	}
	if !promoted.IsTrusted() {
		t.Fatal("registo promovido devia ser trusted")
	}
	if err := verifyChainIntact(ctx, store, rec.Partition); err != nil {
		t.Fatalf("hash-chain da promoção não verifica: %v", err)
	}
}
