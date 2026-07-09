package identity

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	rm "github.com/aos-ref/kernel/reference-monitor"
)

// benchIssuer devolve um emissor com relógio/TTL reais (token válido durante o
// benchmark) e a chave pública.
func benchIssuer(b *testing.B) (*Issuer, ed25519.PublicKey) {
	b.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		b.Fatalf("GenerateKey: %v", err)
	}
	iss, err := NewIssuer(testIssuerID, priv, map[string]ClassPolicy{
		"researcher": {TTL: time.Hour, Scope: []string{"cap:http.get", "cap:fs.read"}},
	})
	if err != nil {
		b.Fatalf("NewIssuer: %v", err)
	}
	return iss, pub
}

// BenchmarkVerify mede o custo de verificação de um token NHI (parse + ed25519 +
// janela temporal + revogação). É a medição autoritativa do overhead de
// identidade por tool call.
func BenchmarkVerify(b *testing.B) {
	iss, pub := benchIssuer(b)
	tok, err := iss.Issue(context.Background(), IssueRequest{
		UserID: "u", AgentID: "a", AgentClass: "researcher",
		UserAuthority: []string{"cap:http.get"},
	})
	if err != nil {
		b.Fatalf("Issue: %v", err)
	}
	v := NewVerifier(WithTrustedIssuer(testIssuerID, pub), WithRevocations(NewRevocations(nil)))
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := v.Verify(ctx, tok.Compact); err != nil {
			b.Fatalf("Verify: %v", err)
		}
	}
}

// BenchmarkRMIdentityPolicyPath mede o caminho de mediação identity+policy do RM
// com uma NHI real: verificação da NHI (identity) + PolicyStub (permit). É o
// orçamento composto do troço de controlo que AOS-005 adiciona à tool call.
func BenchmarkRMIdentityPolicyPath(b *testing.B) {
	iss, pub := benchIssuer(b)
	tok, err := iss.Issue(context.Background(), IssueRequest{
		UserID: "u", AgentID: "a", AgentClass: "researcher",
		UserAuthority: []string{"cap:http.get"},
	})
	if err != nil {
		b.Fatalf("Issue: %v", err)
	}
	v := NewVerifier(WithTrustedIssuer(testIssuerID, pub), WithRevocations(NewRevocations(nil)))
	m := rm.New(rm.WithHooks(NewIdentityCheck(v), rm.PolicyStub{}))
	if err := m.Register("tool.fetch", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		b.Fatalf("Register: %v", err)
	}
	call := rm.Call{
		RunID: "r", StepID: "s", ToolID: "tool.fetch",
		Capability: "cap:http.get", Credential: tok.Compact,
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d, _ := m.Mediate(ctx, call)
		if d.Effect != rm.EffectPermit {
			b.Fatalf("esperava permit, obtive %q", d.Effect)
		}
	}
}
