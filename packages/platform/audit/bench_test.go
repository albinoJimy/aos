package audit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

// BenchmarkAppend mede a escrita encadeada (assign seq + hash + persist).
func BenchmarkAppend(b *testing.B) {
	ctx := context.Background()
	store := NewMemStore()
	rec := sampleRecord("bench", DecisionAllow)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.Append(ctx, rec); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkVerify mede a verificação completa de uma cadeia de 10k registos.
func BenchmarkVerify(b *testing.B) {
	ctx := context.Background()
	store := NewMemStore()
	const n = 10_000
	rec := sampleRecord("bench", DecisionAllow)
	for i := 0; i < n; i++ {
		if _, err := store.Append(ctx, rec); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := Verify(ctx, store, "bench", 1, n); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkVerifyFromCheckpoint mede a verificação ancorada de um intervalo
// recente numa cadeia longa (deve ser barata: só verifica cp+1..to).
func BenchmarkVerifyFromCheckpoint(b *testing.B) {
	ctx := context.Background()
	store := NewMemStore()
	const n = 10_000
	rec := sampleRecord("bench", DecisionAllow)
	for i := 0; i < n; i++ {
		if _, err := store.Append(ctx, rec); err != nil {
			b.Fatal(err)
		}
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := NewSigner(priv)
	cp, err := signer.Seal(ctx, store, "bench", n-100)
	if err != nil {
		b.Fatal(err)
	}
	pub := signer.Public()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := VerifyFromCheckpoint(ctx, store, pub, cp, n); err != nil {
			b.Fatal(err)
		}
	}
}
