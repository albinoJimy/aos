package provenance_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/memory/provenance"
)

// mustUntrusted admite um registo untrusted (via tool result) para promoção.
func mustUntrusted(t *testing.T, id string) provenance.Ingested {
	t.Helper()
	ing := provenance.NewIngestor(nil)
	in, err := ing.Ingest(context.Background(), semanticRecord(id, ""), provenance.SourceToolResult)
	if err != nil {
		t.Fatalf("Ingest untrusted: %v", err)
	}
	if in.IsTrusted() {
		t.Fatal("pré-condição: a entrada devia ser untrusted")
	}
	return in
}

// TestPromote_Auditable prova a promoção auditável untrusted → trusted: com
// validação explícita, produz um registo trusted NOVO e regista a promoção na
// hash-chain tamper-evident (AOS-011), com o taint de origem e a validação selados.
// O original untrusted permanece imutável.
func TestPromote_Auditable(t *testing.T) {
	t.Parallel()
	store := audit.NewMemStore()
	promoter, err := provenance.NewPromoter(store, provenance.WithClock(func() time.Time { return fixedTime }))
	if err != nil {
		t.Fatalf("NewPromoter: %v", err)
	}

	entry := mustUntrusted(t, "mem-promote")
	req := provenance.PromotionRequest{
		Entry:          entry,
		Method:         provenance.ValidationHuman,
		Validator:      "human:reviewer-7",
		Justification:  "revisto e ratificado",
		AgentID:        "agent-1",
		RunID:          "run-1",
		AuditPartition: "run-1",
	}

	promoted, sealed, err := promoter.Promote(context.Background(), req)
	if err != nil {
		t.Fatalf("Promote inesperado: %v", err)
	}

	// A entrada promovida é trusted; o original permanece untrusted (imutável).
	if !promoted.IsTrusted() {
		t.Fatalf("promovida=%q, esperava trusted", promoted.Provenance())
	}
	if entry.Provenance() != provenance.Untrusted {
		t.Fatalf("original mutado=%q, esperava permanecer untrusted", entry.Provenance())
	}

	// O registo de audit foi selado com o taint de ORIGEM e a validação.
	if sealed.AuditSeq != 1 {
		t.Fatalf("AuditSeq=%d, esperava 1 (primeiro da partição)", sealed.AuditSeq)
	}
	if sealed.Decision != audit.DecisionAllow {
		t.Fatalf("Decision=%q, esperava allow", sealed.Decision)
	}
	if sealed.Context.Taint != string(provenance.Untrusted) {
		t.Fatalf("Context.Taint=%q, esperava untrusted (o taint de origem é selado)", sealed.Context.Taint)
	}
	if sealed.Capability != "memory:promote:untrusted->trusted" {
		t.Fatalf("Capability inesperada: %q", sealed.Capability)
	}
	if len(sealed.EntryHash) == 0 {
		t.Fatal("EntryHash vazio: a promoção não foi selada na cadeia")
	}
	if len(sealed.Obligations) != 1 {
		t.Fatalf("esperava 1 obligation, obtive %d", len(sealed.Obligations))
	}
	p := sealed.Obligations[0].Params
	if p["validator"] != "human:reviewer-7" || p["method"] != string(provenance.ValidationHuman) {
		t.Fatalf("validação não selada corretamente: %v", p)
	}
	if p["from"] != string(provenance.Untrusted) || p["to"] != string(provenance.Trusted) {
		t.Fatalf("transição não selada corretamente: %v", p)
	}

	// A cadeia de audit continua íntegra (tamper-evident).
	if err := audit.Verify(context.Background(), store, "run-1", 1, 1); err != nil {
		t.Fatalf("Verify da cadeia falhou: %v", err)
	}

	// A promoção é observável no log (não há promoção silenciosa).
	head, _ := store.Head(context.Background(), "run-1")
	if head != 1 {
		t.Fatalf("head da partição=%d, esperava 1", head)
	}
}

// TestPromote_FailClosed prova que a promoção falha-fecha sem validação explícita
// e quando a entrada não está em quarentena — não há promoção silenciosa/automática.
func TestPromote_FailClosed(t *testing.T) {
	t.Parallel()
	store := audit.NewMemStore()
	promoter, err := provenance.NewPromoter(store, provenance.WithClock(func() time.Time { return fixedTime }))
	if err != nil {
		t.Fatalf("NewPromoter: %v", err)
	}

	untrusted := mustUntrusted(t, "mem-fc")

	// Entrada trusted (não em quarentena) via system.
	ing := provenance.NewIngestor(nil)
	trusted, err := ing.Ingest(context.Background(), semanticRecord("mem-t", ""), provenance.SourceSystem)
	if err != nil {
		t.Fatalf("Ingest trusted: %v", err)
	}

	tests := []struct {
		name    string
		req     provenance.PromotionRequest
		wantErr error
	}{
		{
			name:    "sem metodo de validacao -> rejeitada",
			req:     provenance.PromotionRequest{Entry: untrusted, Validator: "x", RunID: "r"},
			wantErr: provenance.ErrPromotionNotValidated,
		},
		{
			name:    "metodo invalido (auto) -> rejeitada",
			req:     provenance.PromotionRequest{Entry: untrusted, Method: provenance.ValidationMethod("auto"), Validator: "x", RunID: "r"},
			wantErr: provenance.ErrPromotionNotValidated,
		},
		{
			name:    "validador vazio -> rejeitada",
			req:     provenance.PromotionRequest{Entry: untrusted, Method: provenance.ValidationPolicy, Validator: "", RunID: "r"},
			wantErr: provenance.ErrPromotionNotValidated,
		},
		{
			name:    "entrada trusted (nao em quarentena) -> rejeitada",
			req:     provenance.PromotionRequest{Entry: trusted, Method: provenance.ValidationPolicy, Validator: "policy:eval", RunID: "r"},
			wantErr: provenance.ErrNotQuarantined,
		},
		{
			// AOS042-Q5: um Ingested zero-value (prov=="") não é canonicamente
			// untrusted; a guarda pelo lado positivo (==Untrusted) rejeita-o em vez
			// de o promover a trusted a partir de um registo vazio.
			name:    "entrada zero-value (prov nao-canonica) -> rejeitada",
			req:     provenance.PromotionRequest{Entry: provenance.Ingested{}, Method: provenance.ValidationPolicy, Validator: "policy:eval", RunID: "r"},
			wantErr: provenance.ErrNotQuarantined,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := promoter.Promote(context.Background(), tt.req)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("erro=%v, esperava %v", err, tt.wantErr)
			}
		})
	}

	// Nenhuma das tentativas rejeitadas deixou um registo no log (fail-closed antes
	// de escrever no audit).
	if head, _ := store.Head(context.Background(), "r"); head != 0 {
		t.Fatalf("promoção rejeitada escreveu no audit (head=%d, esperava 0)", head)
	}
}

// TestNewPromoter_NilStore prova o fail-closed da construção: sem audit store não
// há promoção auditável.
func TestNewPromoter_NilStore(t *testing.T) {
	t.Parallel()
	if _, err := provenance.NewPromoter(nil); !errors.Is(err, provenance.ErrNilAuditStore) {
		t.Fatalf("erro=%v, esperava ErrNilAuditStore", err)
	}
}

// TestPromote_PolicyValidationAndDefaultPartition cobre a validação por política e
// a partição default (RunID vazio → "global").
func TestPromote_PolicyValidationAndDefaultPartition(t *testing.T) {
	t.Parallel()
	store := audit.NewMemStore()
	promoter, err := provenance.NewPromoter(store, provenance.WithClock(func() time.Time { return fixedTime }))
	if err != nil {
		t.Fatalf("NewPromoter: %v", err)
	}

	_, sealed, err := promoter.Promote(context.Background(), provenance.PromotionRequest{
		Entry:     mustUntrusted(t, "mem-pol"),
		Method:    provenance.ValidationPolicy,
		Validator: "policy:curation-eval-gate",
		AgentID:   "agent-9",
		// RunID e AuditPartition vazios -> partição "global".
	})
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if sealed.Partition != "global" {
		t.Fatalf("partição=%q, esperava global (default)", sealed.Partition)
	}
	if head, _ := store.Head(context.Background(), "global"); head != 1 {
		t.Fatalf("head global=%d, esperava 1", head)
	}
}
