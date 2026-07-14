package provenance_test

import (
	"context"
	"testing"

	"github.com/aos-ref/platform/memory/provenance"
)

// TestForgery_ZeroValueTrustedEntryCannotAuthorize é a prova do AOS042-Q2: o
// zero-value provenance.TrustedEntry{} — construível por qualquer pacote — NÃO
// consegue forjar autoridade de control-plane. AuthorizeToolCall num entry não
// admitido devolve uma autorização NÃO concedida (granted=false), sem taint
// trusted, ainda que o tipo satisfaça PrivilegedAuthorizer.
func TestForgery_ZeroValueTrustedEntryCannotAuthorize(t *testing.T) {
	t.Parallel()

	// O zero-value satisfaz a interface (compile-time), mas é INERTE em runtime.
	var forged provenance.PrivilegedAuthorizer = provenance.TrustedEntry{}
	authz := forged.AuthorizeToolCall("fs:write:/etc/shadow")

	if authz.Granted() {
		t.Fatal("BARREIRA VIOLADA: TrustedEntry{} zero-value concedeu autorização")
	}
	if authz.Taint == provenance.Trusted {
		t.Fatalf("BARREIRA VIOLADA: taint=%q, um zero-value não pode produzir trusted", authz.Taint)
	}
}

// TestForgery_DirectAuthorizationIsNotGranted é a prova do AOS042-Q3: uma
// Authorization construída directamente por outro pacote (campos exportados) NÃO é
// um token genuíno — Granted() é false, apesar de Taint poder ser posto a trusted.
// O consumidor de control-plane distingue-a de uma emitida por memória trusted.
func TestForgery_DirectAuthorizationIsNotGranted(t *testing.T) {
	t.Parallel()

	forged := provenance.Authorization{Capability: "any", Taint: provenance.Trusted}
	if forged.Granted() {
		t.Fatal("BARREIRA VIOLADA: Authorization forjada directamente reporta Granted()=true")
	}
}

// TestForgery_GenuineTokenIsGranted confirma o lado positivo: um token emitido por
// memória trusted REAL (admitida via Partition.Admit) é concedido e trusted.
func TestForgery_GenuineTokenIsGranted(t *testing.T) {
	t.Parallel()
	ing := provenance.NewIngestor(nil)
	part := provenance.NewPartition(nil)

	in, err := ing.Ingest(context.Background(), semanticRecord("t-genuine", ""), provenance.SourceSystem)
	if err != nil {
		t.Fatalf("Ingest system: %v", err)
	}
	part.Admit(in)

	entry := part.TrustedView().Entries()[0]
	authz := entry.AuthorizeToolCall("fs:write:/reports/*")
	if !authz.Granted() {
		t.Fatal("um token de memória trusted genuína devia ser concedido (Granted()=true)")
	}
	if authz.Taint != provenance.Trusted {
		t.Fatalf("taint=%q, esperava trusted", authz.Taint)
	}
}
