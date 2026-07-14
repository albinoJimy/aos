package domain

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestParseVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		want    Version
		wantErr bool
	}{
		{"exacta", "1.2.3", Version{1, 2, 3}, false},
		{"zeros", "0.0.0", Version{0, 0, 0}, false},
		{"espacos", "  2.0.1 ", Version{2, 0, 1}, false},
		{"latest flutuante", "latest", Version{}, true},
		{"main flutuante", "main", Version{}, true},
		{"parcial", "1.2", Version{}, true},
		{"extra", "1.2.3.4", Version{}, true},
		{"prerelease", "1.2.3-rc1", Version{}, true},
		{"intervalo", "^1.2.3", Version{}, true},
		{"negativo", "1.-2.3", Version{}, true},
		{"vazio", "", Version{}, true},
		{"campo vazio", "1..3", Version{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseVersion(tc.in)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidVersion) {
					t.Fatalf("ParseVersion(%q) err = %v, quer ErrInvalidVersion", tc.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseVersion(%q) err inesperado: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ParseVersion(%q) = %v, quer %v", tc.in, got, tc.want)
			}
			if rt := got.String(); rt != "" {
				if back, _ := ParseVersion(rt); back != got {
					t.Fatalf("round-trip String/Parse falhou: %q -> %v -> %v", rt, got, back)
				}
			}
		})
	}
}

func TestVersion_CompareAndZero(t *testing.T) {
	t.Parallel()
	if !(Version{}).IsZero() {
		t.Fatal("value-zero deve ser IsZero")
	}
	if (Version{0, 0, 1}).IsZero() {
		t.Fatal("0.0.1 nao e zero")
	}
	if Compare := (Version{1, 0, 0}).Compare(Version{1, 0, 1}); Compare >= 0 {
		t.Fatalf("1.0.0 < 1.0.1 esperado, got %d", Compare)
	}
	if !(Version{1, 2, 3}).Equal(Version{1, 2, 3}) {
		t.Fatal("Equal falhou")
	}
	if !(Version{1, 2, 3}).Less(Version{2, 0, 0}) {
		t.Fatal("Less falhou")
	}
}

func TestClassify(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from, to Version
		want     ChangeKind
	}{
		{Version{1, 0, 0}, Version{1, 0, 0}, ChangeNone},
		{Version{1, 0, 0}, Version{1, 0, 1}, ChangePatch},
		{Version{1, 0, 0}, Version{1, 1, 0}, ChangeMinor},
		{Version{1, 0, 0}, Version{2, 0, 0}, ChangeMajor},
		{Version{2, 0, 0}, Version{1, 9, 9}, ChangeMajor}, // downgrade de MAJOR = quebra
	}
	for _, tc := range tests {
		if got := Classify(tc.from, tc.to); got != tc.want {
			t.Fatalf("Classify(%v,%v) = %v, quer %v", tc.from, tc.to, got, tc.want)
		}
	}
	if ChangeMajor.String() != "major" {
		t.Fatalf("String major = %q", ChangeMajor.String())
	}
}

func TestArtifactKind_Valid(t *testing.T) {
	t.Parallel()
	for _, k := range AllKinds() {
		if !k.Valid() {
			t.Fatalf("%q devia ser valido", k)
		}
	}
	if ArtifactKind("plugin").Valid() {
		t.Fatal("kind desconhecido nao deve ser valido")
	}
	if len(AllKinds()) != 3 {
		t.Fatalf("esperados 3 tipos, got %d", len(AllKinds()))
	}
}

func TestStatus_Transitions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from, to Status
		ok       bool
	}{
		{StatusStaging, StatusActive, true},
		{StatusStaging, StatusRevoked, true},
		{StatusStaging, StatusDeprecated, false}, // nao pode deprecar sem verificar
		{StatusActive, StatusDeprecated, true},
		{StatusActive, StatusRevoked, true},
		{StatusActive, StatusStaging, false}, // nunca volta a staging
		{StatusDeprecated, StatusActive, true},
		{StatusDeprecated, StatusRevoked, true},
		{StatusRevoked, StatusActive, false}, // terminal
		{StatusRevoked, StatusStaging, false},
		{StatusStaging, StatusStaging, false}, // mesmo estado nao e transicao
		{Status("bogus"), StatusActive, false},
	}
	for _, tc := range tests {
		if got := CanTransition(tc.from, tc.to); got != tc.ok {
			t.Fatalf("CanTransition(%s,%s) = %v, quer %v", tc.from, tc.to, got, tc.ok)
		}
	}
}

func TestStatus_NoDirectJumpToActive(t *testing.T) {
	t.Parallel()
	// TODA a aresta que produz active passa pelo gate de admissao (staging->active
	// E deprecated->active): nenhuma promocao a active escapa a re-verificacao
	// criptografica (AOS-048 Q1 — fecha a janela de revogacao na reactivacao).
	if !RequiresAdmissionGate(StatusStaging, StatusActive) {
		t.Fatal("staging->active tem de exigir o gate de admissao")
	}
	if !RequiresAdmissionGate(StatusDeprecated, StatusActive) {
		t.Fatal("deprecated->active (reactivacao) tem de exigir o gate de admissao (re-verificacao)")
	}
	// O gate SO se aplica a promocoes a active; as restantes transicoes nao.
	for _, tc := range []struct{ from, to Status }{
		{StatusActive, StatusDeprecated},
		{StatusActive, StatusRevoked},
		{StatusStaging, StatusRevoked},
		{StatusDeprecated, StatusRevoked},
	} {
		if RequiresAdmissionGate(tc.from, tc.to) {
			t.Fatalf("%s->%s nao devia exigir o gate de admissao", tc.from, tc.to)
		}
	}
	// Nenhum estado transita para active a nao ser staging e deprecated.
	for _, from := range []Status{StatusActive, StatusRevoked} {
		if CanTransition(from, StatusActive) {
			t.Fatalf("%s->active nao devia ser permitido", from)
		}
	}
}

func TestDigest_Determinism(t *testing.T) {
	t.Parallel()
	d := PlaceholderDigester{}
	c1 := Contract{
		InputSchema:      json.RawMessage(`{"type":"object"}`),
		Egress:           EgressExternal,
		CredentialScopes: []string{"vault:a", "vault:b"},
	}
	// Mesmo conteudo (ordem de scopes diferente) -> MESMO digest (canonicalizacao).
	c2 := Contract{
		InputSchema:      json.RawMessage(`{"type":"object"}`),
		Egress:           EgressExternal,
		CredentialScopes: []string{"vault:b", "vault:a", "vault:a"}, // dup + ordem
	}
	if d.Digest(KindTool, c1) != d.Digest(KindTool, c2) {
		t.Fatal("mesmo conteudo canonico deve produzir mesmo digest")
	}
	// Mudanca minima -> digest diferente.
	c3 := c1
	c3.Egress = EgressInternal
	if d.Digest(KindTool, c1) == d.Digest(KindTool, c3) {
		t.Fatal("mudanca de egress deve mudar o digest")
	}
	// Tipo diferente -> digest diferente (mesmo contrato).
	if d.Digest(KindTool, c1) == d.Digest(KindSkill, c1) {
		t.Fatal("tipo diferente deve mudar o digest")
	}
	// Prefixo de placeholder presente (ponto de extensao explicito, nao SHA-256).
	if got := d.Digest(KindTool, c1); got[:len(placeholderPrefix)] != placeholderPrefix {
		t.Fatalf("digest %q sem prefixo de placeholder", got)
	}
}

func TestEntry_Validate(t *testing.T) {
	t.Parallel()
	base := Entry{
		ID:         "tool.x",
		Version:    Version{1, 0, 0},
		Kind:       KindTool,
		Digest:     "placeholder-fnv1a:abc",
		Contract:   Contract{Egress: EgressNone},
		Provenance: Provenance{Trust: TrustFirstSeen},
		Status:     StatusStaging,
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("entrada valida rejeitada: %v", err)
	}
	tests := []struct {
		name string
		mut  func(*Entry)
		want error
	}{
		{"sem id", func(e *Entry) { e.ID = "" }, ErrEmptyID},
		{"kind invalido", func(e *Entry) { e.Kind = "x" }, ErrInvalidKind},
		{"status invalido", func(e *Entry) { e.Status = "x" }, ErrInvalidStatus},
		{"egress invalido", func(e *Entry) { e.Contract.Egress = "x" }, ErrInvalidEgress},
		{"trust invalido", func(e *Entry) { e.Provenance.Trust = "x" }, ErrInvalidTrust},
		{"sem digest", func(e *Entry) { e.Digest = "" }, ErrMissingDigest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := base
			tc.mut(&e)
			if err := e.Validate(); !errors.Is(err, tc.want) {
				t.Fatalf("Validate() = %v, quer %v", err, tc.want)
			}
		})
	}
}

func TestEntry_CloneIsolation(t *testing.T) {
	t.Parallel()
	e := Entry{
		ID: "t", Version: Version{1, 0, 0}, Kind: KindTool, Digest: "d",
		Contract:   Contract{Egress: EgressNone, CredentialScopes: []string{"a"}, InputSchema: json.RawMessage(`{}`)},
		Provenance: Provenance{Trust: TrustFirstSeen}, Status: StatusStaging,
	}
	cp := e.Clone()
	cp.Contract.CredentialScopes[0] = "mutated"
	cp.Contract.InputSchema[0] = 'X'
	if e.Contract.CredentialScopes[0] != "a" {
		t.Fatal("clone partilhou o slice de scopes")
	}
	if string(e.Contract.InputSchema) != "{}" {
		t.Fatal("clone partilhou o InputSchema")
	}
}
