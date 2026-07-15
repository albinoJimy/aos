package revalidation

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/signing"
	"github.com/aos-ref/platform/registry/toolset"
)

// benchContract constrói um contrato REALISTA (schemas de I/O não-triviais) para o
// benchmark medir o overhead de revalidação sobre uma definição de tamanho crível —
// incl. o recálculo SHA-256 do contrato a cada chamada (sem cache; o Ed25519 domina).
func benchContract() domain.Contract {
	in, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url":     map[string]any{"type": "string", "format": "uri", "maxLength": 2048},
			"method":  map[string]any{"type": "string", "enum": []string{"GET", "POST", "PUT", "DELETE"}},
			"headers": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
			"body":    map[string]any{"type": "string"},
			"timeout": map[string]any{"type": "integer", "minimum": 1, "maximum": 300},
		},
		"required": []string{"url", "method"},
	})
	out, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"status":  map[string]any{"type": "integer"},
			"headers": map[string]any{"type": "object"},
			"body":    map[string]any{"type": "string"},
		},
	})
	return domain.Contract{
		InputSchema:      in,
		OutputSchema:     out,
		CredentialScopes: []string{"vault:http.token", "vault:http.refresh"},
		Egress:           domain.EgressExternal,
	}
}

func benchSetup(b *testing.B) (*Revalidator, Request) {
	b.Helper()
	signer, err := signing.NewSigner("pub:bench", keyFromSeed(3))
	if err != nil {
		b.Fatalf("NewSigner: %v", err)
	}
	trust, err := signing.NewTrustStore(audit.NewMemStore())
	if err != nil {
		b.Fatalf("NewTrustStore: %v", err)
	}
	if err := trust.Add(context.Background(), signer.KeyID(), signer.PublicKey()); err != nil {
		b.Fatalf("trust.Add: %v", err)
	}
	c := benchContract()
	entry := signedEntry("tool.http", domain.Version{Major: 1}, domain.KindTool, c, signer)
	frozen, err := toolset.FreezeToolSet(context.Background(), fakeCatalog{entries: []domain.Entry{entry}}, "run-bench", nil)
	if err != nil {
		b.Fatalf("FreezeToolSet: %v", err)
	}
	rv, err := New(trust, audit.NewMemStore())
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	req := Request{
		RunID: "run-bench", StepID: "s1", ToolID: "tool.http",
		Current: entry, Frozen: frozen,
		Policy: Policy{
			AllowedScopes: []string{"vault:http.token", "vault:http.refresh"},
			MaxEgress:     domain.EgressExternal,
		},
	}
	return rv, req
}

// BenchmarkRevalidate mede o overhead de revalidação por chamada (recálculo SHA-256 +
// verificação Ed25519 + audit, sem cache). O DoD de AOS-051 exige p95 < 15 ms
// (ADR-002). Ver TestRevalidate_P95Budget para a asserção automática do orçamento.
func BenchmarkRevalidate(b *testing.B) {
	rv, req := benchSetup(b)
	ctx := context.Background()
	if _, err := rv.Revalidate(ctx, req); err != nil {
		b.Fatalf("primeira chamada: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dec, err := rv.Revalidate(ctx, req)
		if err != nil || !dec.Allowed {
			b.Fatalf("revalidate falhou: dec=%+v err=%v", dec, err)
		}
	}
}

// TestRevalidate_P95Budget é a asserção AUTOMÁTICA do orçamento de latência
// (ADR-002): mede a distribuição do overhead de revalidação (recálculo SHA-256 por
// chamada + Ed25519 + audit) e exige o p95 < 15 ms. Não é um benchmark (corre em
// `go test`), para que o gate falhe fechado se o orçamento regredir.
func TestRevalidate_P95Budget(t *testing.T) {
	if testing.Short() {
		t.Skip("orçamento de latência: ignorado em -short")
	}
	pub := newSigner(t, "pub:test", 7)
	trust := newTrust(t, pub)
	c := benchContract()
	entry := signedEntry("tool.http", ver(1, 0, 0), domain.KindTool, c, pub)
	frozen := freeze(t, "run-1", entry)
	rv, err := New(trust, audit.NewMemStore())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := Request{
		RunID: "run-1", StepID: "s1", ToolID: "tool.http", Current: entry, Frozen: frozen,
		Policy: Policy{AllowedScopes: []string{"vault:http.token", "vault:http.refresh"}, MaxEgress: domain.EgressExternal},
	}
	ctx := context.Background()
	if _, err := rv.Revalidate(ctx, req); err != nil {
		t.Fatalf("primeira chamada: %v", err)
	}

	const n = 2000
	samples := make([]time.Duration, n)
	for i := 0; i < n; i++ {
		start := time.Now()
		dec, err := rv.Revalidate(ctx, req)
		samples[i] = time.Since(start)
		if err != nil || !dec.Allowed {
			t.Fatalf("iteração %d: dec=%+v err=%v", i, dec, err)
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	p95 := samples[int(float64(n)*0.95)]
	const budget = 15 * time.Millisecond
	if p95 >= budget {
		t.Fatalf("overhead de revalidação p95 = %v, excede o orçamento de %v (ADR-002)", p95, budget)
	}
	t.Logf("overhead de revalidação: p50=%v p95=%v (orçamento %v)", samples[n/2], p95, budget)
}
