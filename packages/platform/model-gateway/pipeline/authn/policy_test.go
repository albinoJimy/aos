package authn_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/pipeline/authn"
)

// TestLoadPolicy_Embedded carrega a política embebida e confirma a versão
// tamper-evident (versão#digest) + default-deny.
func TestLoadPolicy_Embedded(t *testing.T) {
	t.Parallel()
	p, err := authn.LoadPolicy()
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	v := p.Version()
	if !strings.HasPrefix(v, "gw-token-policy/v1#") {
		t.Fatalf("versao = %q, quer prefixo gw-token-policy/v1#", v)
	}
	// A versão é estável (digest determinista) entre cargas.
	p2, _ := authn.LoadPolicy()
	if p2.Version() != v {
		t.Fatalf("versao instavel: %q vs %q", v, p2.Version())
	}
}

// TestLoadPolicy_RejectsFailOpen: uma política cujo default não é deny é REJEITADA
// no carregamento (fail-closed — o gateway não arranca fail-open).
func TestLoadPolicy_RejectsFailOpen(t *testing.T) {
	t.Parallel()
	bad := []byte(`{"version":"x/v1","default":"allow","rules":[]}`)
	if _, err := authn.LoadPolicyFromBytes(bad); !errors.Is(err, authn.ErrPolicyMalformed) {
		t.Fatalf("erro = %v, quer ErrPolicyMalformed", err)
	}
	// Sem versão também é malformada.
	noVer := []byte(`{"default":"deny","rules":[]}`)
	if _, err := authn.LoadPolicyFromBytes(noVer); !errors.Is(err, authn.ErrPolicyMalformed) {
		t.Fatalf("sem versao: erro = %v, quer ErrPolicyMalformed", err)
	}
}

// TestPolicy_Evaluate_AllowDeny_DefaultDeny é o teste allow/deny com default-deny.
func TestPolicy_Evaluate_AllowDeny_DefaultDeny(t *testing.T) {
	t.Parallel()
	p, err := authn.LoadPolicy()
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	cases := []struct {
		name string
		in   authn.PolicyInput
		want authn.Effect
	}{
		{
			name: "chat com model:invoke -> allow",
			in:   authn.PolicyInput{Operation: "chat", AgentClass: "reader", Authority: []string{"model:invoke"}},
			want: authn.EffectAllow,
		},
		{
			name: "embeddings com model:invoke -> allow",
			in:   authn.PolicyInput{Operation: "embeddings", AgentClass: "any", Authority: []string{"model:invoke", "x"}},
			want: authn.EffectAllow,
		},
		{
			name: "sem a capability exigida -> deny (default-deny)",
			in:   authn.PolicyInput{Operation: "chat", AgentClass: "reader", Authority: []string{"fs:read"}},
			want: authn.EffectDeny,
		},
		{
			name: "operacao desconhecida -> deny (sem regra aplicavel)",
			in:   authn.PolicyInput{Operation: "delete", AgentClass: "reader", Authority: []string{"model:invoke"}},
			want: authn.EffectDeny,
		},
		{
			name: "autoridade vazia -> deny",
			in:   authn.PolicyInput{Operation: "chat", AgentClass: "reader", Authority: nil},
			want: authn.EffectDeny,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.Evaluate(tc.in); got != tc.want {
				t.Fatalf("Evaluate(%+v) = %v, quer %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestPolicy_VersionChangesWithContent: alterar o conteúdo muda o digest (a versão
// é tamper-evident).
func TestPolicy_VersionChangesWithContent(t *testing.T) {
	t.Parallel()
	base, _ := authn.LoadPolicy()
	altered := []byte(`{"version":"gw-token-policy/v1","default":"deny","rules":[{"id":"z","operations":["chat"],"agent_classes":["*"],"require_capabilities":["model:invoke","extra"]}]}`)
	other, err := authn.LoadPolicyFromBytes(altered)
	if err != nil {
		t.Fatalf("LoadPolicyFromBytes: %v", err)
	}
	if base.Version() == other.Version() {
		t.Fatalf("digest nao mudou com o conteudo: %q", base.Version())
	}
}
