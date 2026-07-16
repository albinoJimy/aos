package taint_test

import (
	"testing"

	"github.com/aos-ref/kernel/reference-monitor/taint"
)

// TestLabelZeroValueIsUntrusted prova o fail-closed pelo TIPO: o valor-zero de
// Label é Untrusted. Um dado que nunca foi explicitamente marcado trusted não é
// confiável.
func TestLabelZeroValueIsUntrusted(t *testing.T) {
	var l taint.Label // zero value
	if l.IsTrusted() {
		t.Fatalf("valor-zero de Label devia ser untrusted, got trusted")
	}
	if !l.IsUntrusted() {
		t.Fatalf("valor-zero de Label devia ser untrusted")
	}
	if l != taint.Untrusted {
		t.Fatalf("valor-zero devia igualar taint.Untrusted")
	}
}

func TestLabelPredicatesAndString(t *testing.T) {
	tests := []struct {
		name        string
		label       taint.Label
		wantTrusted bool
		wantValid   bool
		wantString  string
	}{
		{"trusted", taint.Trusted, true, true, "trusted"},
		{"untrusted", taint.Untrusted, false, true, "untrusted"},
		{"invalid-high", taint.Label(200), false, false, "untrusted"}, // fail-closed
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.label.IsTrusted(); got != tc.wantTrusted {
				t.Errorf("IsTrusted()=%v want %v", got, tc.wantTrusted)
			}
			if got := tc.label.IsUntrusted(); got != !tc.wantTrusted {
				t.Errorf("IsUntrusted()=%v want %v", got, !tc.wantTrusted)
			}
			if got := tc.label.Valid(); got != tc.wantValid {
				t.Errorf("Valid()=%v want %v", got, tc.wantValid)
			}
			if got := tc.label.String(); got != tc.wantString {
				t.Errorf("String()=%q want %q", got, tc.wantString)
			}
		})
	}
}

// TestParseLabelFailClosed prova que só a string canónica "trusted" resolve
// Trusted; tudo o resto (vazio, forjado, capitalização diferente) resolve
// Untrusted — um campo de taint ausente ou adulterado nunca é tratado como
// confiável.
func TestParseLabelFailClosed(t *testing.T) {
	tests := []struct {
		in   string
		want taint.Label
	}{
		{"trusted", taint.Trusted},
		{"untrusted", taint.Untrusted},
		{"", taint.Untrusted},
		{"Trusted", taint.Untrusted},  // capitalização não canónica
		{"TRUSTED", taint.Untrusted},  // idem
		{"trusted ", taint.Untrusted}, // espaço adulterado
		{"privileged", taint.Untrusted},
		{"true", taint.Untrusted},
	}
	for _, tc := range tests {
		if got := taint.ParseLabel(tc.in); got != tc.want {
			t.Errorf("ParseLabel(%q)=%v want %v", tc.in, got, tc.want)
		}
	}
}

// TestParseLabelRoundTrip garante que String()↔ParseLabel são coerentes nos
// valores canónicos.
func TestParseLabelRoundTrip(t *testing.T) {
	for _, l := range []taint.Label{taint.Trusted, taint.Untrusted} {
		if got := taint.ParseLabel(l.String()); got != l {
			t.Errorf("round-trip falhou: ParseLabel(%q)=%v want %v", l.String(), got, l)
		}
	}
}

// TestLabelForOrigin prova que SÓ system + utilizador autenticado são trusted; as
// restantes origens (tool result, web, MCP, memória derivada, saída do modelo) e
// qualquer origem desconhecida/forjada classificam untrusted (fail-closed).
func TestLabelForOrigin(t *testing.T) {
	tests := []struct {
		origin taint.Origin
		want   taint.Label
	}{
		{taint.OriginSystem, taint.Trusted},
		{taint.OriginAuthenticatedUser, taint.Trusted},
		{taint.OriginToolResult, taint.Untrusted},
		{taint.OriginWeb, taint.Untrusted},
		{taint.OriginMCPSchema, taint.Untrusted},
		{taint.OriginDerivedMemory, taint.Untrusted},
		{taint.OriginModelOutput, taint.Untrusted},
		{taint.Origin(""), taint.Untrusted},              // origem vazia
		{taint.Origin("forged_origin"), taint.Untrusted}, // origem forjada
	}
	for _, tc := range tests {
		if got := taint.LabelFor(tc.origin); got != tc.want {
			t.Errorf("LabelFor(%q)=%v want %v", tc.origin, got, tc.want)
		}
	}
}

// TestOriginMirrorsMemoryProvenanceSource documenta que os valores textuais das
// origens espelham platform/memory/domain.ProvenanceSource, condição da composição
// da proveniência de memória com este reticulado sem importar a memória.
func TestOriginMirrorsMemoryProvenanceSource(t *testing.T) {
	// Valores canónicos espelhados (ver memory/domain/metadata.go).
	want := map[taint.Origin]string{
		taint.OriginSystem:            "system",
		taint.OriginAuthenticatedUser: "authenticated_user",
		taint.OriginToolResult:        "tool_result",
		taint.OriginWeb:               "web",
		taint.OriginMCPSchema:         "mcp_schema",
		taint.OriginDerivedMemory:     "derived_memory",
	}
	for o, s := range want {
		if string(o) != s {
			t.Errorf("Origin %v = %q, devia espelhar ProvenanceSource %q", o, string(o), s)
		}
	}
}
