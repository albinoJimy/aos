package pdp

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// mustOpen carrega o bundle de referência committado em policies/.
func mustOpen(t testing.TB) *PDP {
	t.Helper()
	p, err := Open("policies")
	if err != nil {
		t.Fatalf("Open(policies): %v", err)
	}
	return p
}

// httpPost devolve um Input base para cap:http.post que PERMITE (region=eu,
// taint trusted, authority com a capability). Os casos derivam mutando-o.
func httpPost() Input {
	return Input{
		RequestID:  "req-1",
		Principal:  Principal{ID: "nhi-1", AgentClass: "agent-worker", Authority: []string{"cap:http.post", "cap:fs.read"}},
		Capability: "cap:http.post",
		Resource:   Resource{Type: "url", Value: "https://api.example.com/orders", Region: "eu"},
		Context:    DecisionContext{Taint: "trusted", Sensitivity: "public"},
	}
}

func fsRead() Input {
	return Input{
		RequestID:  "req-2",
		Principal:  Principal{ID: "nhi-1", AgentClass: "agent-worker", Authority: []string{"cap:fs.read"}},
		Capability: "cap:fs.read",
		Resource:   Resource{Type: "file", Value: "/etc/data", Region: "eu"},
		Context:    DecisionContext{Taint: "trusted"},
	}
}

var (
	obAudit  = Obligation{Type: "audit", Params: map[string]string{"level": "full"}}
	obRedact = Obligation{Type: "redact_pii", Fields: []string{"email", "phone"}}
)

// TestDecide_GoldenTruthTable cobre a tabela-verdade completa da política de
// referência (tecnica/12 §9): allow/deny + reason + obligations.
func TestDecide_GoldenTruthTable(t *testing.T) {
	t.Parallel()
	p := mustOpen(t)
	ctx := context.Background()

	mut := func(base Input, f func(*Input)) Input {
		in := base
		if f != nil {
			f(&in)
		}
		return in
	}

	tests := []struct {
		name       string
		in         Input
		wantEffect Effect
		wantOblig  []Obligation
		reasonHas  string
	}{
		{
			name:       "http_post_permitido_publico",
			in:         httpPost(),
			wantEffect: Permit,
			wantOblig:  []Obligation{obAudit},
			reasonHas:  "allow_http_post",
		},
		{
			name:       "http_post_permitido_confidencial_redige_pii",
			in:         mut(httpPost(), func(i *Input) { i.Context.Sensitivity = "confidential" }),
			wantEffect: Permit,
			wantOblig:  []Obligation{obRedact, obAudit},
			reasonHas:  "allow_http_post",
		},
		{
			name:       "http_post_regiao_nao_eu_negado",
			in:         mut(httpPost(), func(i *Input) { i.Resource.Region = "us" }),
			wantEffect: Deny,
			reasonHas:  "default-deny",
		},
		{
			name:       "http_post_taint_untrusted_negado",
			in:         mut(httpPost(), func(i *Input) { i.Context.Taint = "untrusted" }),
			wantEffect: Deny,
			reasonHas:  "default-deny",
		},
		{
			name:       "http_post_sem_capability_na_authority_negado",
			in:         mut(httpPost(), func(i *Input) { i.Principal.Authority = []string{"cap:fs.read"} }),
			wantEffect: Deny,
			reasonHas:  "default-deny",
		},
		{
			name:       "fs_read_permitido",
			in:         fsRead(),
			wantEffect: Permit,
			wantOblig:  []Obligation{obAudit},
			reasonHas:  "allow_fs_read",
		},
		{
			name:       "fs_read_sem_capability_negado",
			in:         mut(fsRead(), func(i *Input) { i.Principal.Authority = []string{"cap:http.get"} }),
			wantEffect: Deny,
			reasonHas:  "default-deny",
		},
		{
			name:       "capability_nao_coberta_default_deny",
			in:         mut(httpPost(), func(i *Input) { i.Capability = "cap:http.get" }),
			wantEffect: Deny,
			reasonHas:  "default-deny",
		},
		{
			// fs.read não depende de region nem taint: mesmo untrusted+us permite,
			// espelhando a regra Rego allow_fs_read (acção reversível).
			name:       "fs_read_untrusted_us_ainda_permite",
			in:         mut(fsRead(), func(i *Input) { i.Context.Taint = "untrusted"; i.Resource.Region = "us" }),
			wantEffect: Permit,
			wantOblig:  []Obligation{obAudit},
			reasonHas:  "allow_fs_read",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := p.Decide(ctx, tc.in)
			if err != nil {
				t.Fatalf("Decide erro inesperado: %v", err)
			}
			if d.Effect != tc.wantEffect {
				t.Fatalf("Effect=%q, esperava %q (reason=%q)", d.Effect, tc.wantEffect, d.Reason)
			}
			if tc.reasonHas != "" && !contains(d.Reason, tc.reasonHas) {
				t.Errorf("reason=%q nao contem %q", d.Reason, tc.reasonHas)
			}
			if d.Effect == Permit {
				if !reflect.DeepEqual(d.Obligations, tc.wantOblig) {
					t.Errorf("obligations=%+v, esperava %+v", d.Obligations, tc.wantOblig)
				}
			} else if len(d.Obligations) != 0 {
				t.Errorf("deny nao deve ter obligations, obtive %+v", d.Obligations)
			}
			// A policy_version acompanha SEMPRE a decisão (permit e deny).
			if d.PolicyVersion != p.Version() || d.PolicyVersion == "" {
				t.Errorf("PolicyVersion=%q, esperava %q", d.PolicyVersion, p.Version())
			}
		})
	}
}

// TestDecide_PolicyVersionRegistada (unit) assevera que a versão de política em
// vigor é registada na Decision — permit E deny — para propagar ao audit.
func TestDecide_PolicyVersionRegistada(t *testing.T) {
	t.Parallel()
	p := mustOpen(t)
	if p.Version() != "1.0.0" {
		t.Fatalf("Version()=%q, esperava 1.0.0", p.Version())
	}
	permit, _ := p.Decide(context.Background(), httpPost())
	if permit.Effect != Permit || permit.PolicyVersion != "1.0.0" {
		t.Errorf("permit: Effect=%q PolicyVersion=%q", permit.Effect, permit.PolicyVersion)
	}
	denyIn := httpPost()
	denyIn.Resource.Region = "us"
	deny, _ := p.Decide(context.Background(), denyIn)
	if deny.Effect != Deny || deny.PolicyVersion != "1.0.0" {
		t.Errorf("deny: Effect=%q PolicyVersion=%q", deny.Effect, deny.PolicyVersion)
	}
}

// TestDecide_Determinismo assevera pureza: a mesma Input produz sempre a mesma
// Decision.
func TestDecide_Determinismo(t *testing.T) {
	t.Parallel()
	p := mustOpen(t)
	ctx := context.Background()
	in := httpPost()
	in.Context.Sensitivity = "confidential"
	first, _ := p.Decide(ctx, in)
	for i := 0; i < 50; i++ {
		d, _ := p.Decide(ctx, in)
		if !reflect.DeepEqual(d, first) {
			t.Fatalf("decisao nao-determinista na iteracao %d: %+v != %+v", i, d, first)
		}
	}
}

// TestDecide_Malformed cobre E_MALFORMED_REQUEST (capability em falta) →
// fail-closed deny.
func TestDecide_Malformed(t *testing.T) {
	t.Parallel()
	p := mustOpen(t)
	in := httpPost()
	in.Capability = ""
	d, err := p.Decide(context.Background(), in)
	if !errors.Is(err, ErrMalformedRequest) {
		t.Fatalf("esperava ErrMalformedRequest, obtive %v", err)
	}
	if d.Effect != Deny {
		t.Errorf("Effect=%q, esperava Deny (fail-closed)", d.Effect)
	}
}

// TestDecide_PolicyUnavailable cobre E_POLICY_UNAVAILABLE: PDP sem bundle nega
// tudo.
func TestDecide_PolicyUnavailable(t *testing.T) {
	t.Parallel()
	p := NewUnloaded()
	d, err := p.Decide(context.Background(), httpPost())
	if !errors.Is(err, ErrPolicyUnavailable) {
		t.Fatalf("esperava ErrPolicyUnavailable, obtive %v", err)
	}
	if d.Effect != Deny {
		t.Errorf("Effect=%q, esperava Deny", d.Effect)
	}
	if d.Permitted() {
		t.Error("Permitted() deve ser false sem politica")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
