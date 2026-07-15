package registry

import (
	"encoding/json"
	"errors"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/registry/domain"
)

func entry(t *testing.T, id, version, digest string, kind domain.ArtifactKind, origin string) domain.Entry {
	t.Helper()
	v, err := domain.ParseVersion(version)
	if err != nil {
		t.Fatalf("versao invalida %q: %v", version, err)
	}
	return domain.Entry{
		ID:      id,
		Version: v,
		Kind:    kind,
		Digest:  digest,
		Contract: domain.Contract{
			Egress: domain.EgressInternal,
		},
		Provenance: domain.Provenance{Origin: origin, Trust: domain.TrustPinned},
		Status:     domain.StatusActive,
	}
}

// TestDependencyManifestRecordsVersionsDigestsModelPrompt é o teste de DOMÍNIO: o
// manifesto grava, por trajectória, as versões+digests de cada dependência JUNTO do
// model-id e do hash do prompt.
func TestDependencyManifestRecordsVersionsDigestsModelPrompt(t *testing.T) {
	t.Parallel()
	entries := []domain.Entry{
		entry(t, "tool.http.get", "1.2.0", "sha256:aaa", domain.KindTool, ""),
		entry(t, "skill.summarize", "0.9.0", "sha256:bbb", domain.KindSkill, ""),
		entry(t, "mcp.files", "3.0.0", "sha256:ccc", domain.KindMCPServer, "mcp://host"),
	}
	m, err := NewDependencyManifest("traj-1", "claude-opus-4-8", "sha256:prompt", entries)
	if err != nil {
		t.Fatalf("NewDependencyManifest: %v", err)
	}
	if m.TrajectoryID() != "traj-1" {
		t.Fatalf("trajectoryID = %q", m.TrajectoryID())
	}
	if m.ModelID() != "claude-opus-4-8" {
		t.Fatalf("modelID = %q", m.ModelID())
	}
	if m.PromptHash() != "sha256:prompt" {
		t.Fatalf("promptHash = %q", m.PromptHash())
	}
	deps := m.Deps()
	if len(deps) != 3 {
		t.Fatalf("nº deps = %d, quer 3", len(deps))
	}
	// Ordem estável (por nome).
	byName := map[string]agentruntime.PinnedDep{}
	for _, d := range deps {
		byName[d.Name] = d
	}
	if d := byName["tool.http.get"]; d.Version != "1.2.0" || d.Digest != "sha256:aaa" {
		t.Fatalf("tool.http.get = %+v", d)
	}
	if d := byName["skill.summarize"]; d.Version != "0.9.0" || d.Digest != "sha256:bbb" {
		t.Fatalf("skill.summarize = %+v", d)
	}
	// Servidor MCP: proveniência do transporte preservada.
	if d := byName["mcp.files"]; d.Version != "3.0.0" || d.Digest != "sha256:ccc" || d.MCPServer != "mcp://host" {
		t.Fatalf("mcp.files = %+v", d)
	}
	if m.Fingerprint() == "" {
		t.Fatalf("fingerprint vazio")
	}
}

// TestDependencyManifestImmutable garante a IMUTABILIDADE: mutar o slice devolvido
// por Deps() nunca afecta o manifesto, e o fingerprint é estável.
func TestDependencyManifestImmutable(t *testing.T) {
	t.Parallel()
	entries := []domain.Entry{
		entry(t, "tool.a", "1.0.0", "sha256:a", domain.KindTool, ""),
		entry(t, "tool.b", "1.0.0", "sha256:b", domain.KindTool, ""),
	}
	src := make([]domain.Entry, len(entries))
	copy(src, entries)
	m, err := NewDependencyManifest("traj", "model", "sha256:p", src)
	if err != nil {
		t.Fatalf("NewDependencyManifest: %v", err)
	}
	fp0 := m.Fingerprint()

	// Mutar o slice devolvido...
	got := m.Deps()
	got[0].Version = "9.9.9"
	got[0].Digest = "sha256:tampered"
	// ...não afecta o manifesto.
	again := m.Deps()
	if again[0].Version == "9.9.9" || again[0].Digest == "sha256:tampered" {
		t.Fatalf("manifesto mutavel via Deps(): %+v", again[0])
	}

	// Mutar o slice de ENTRADAS original tambem nao afecta (cópia possuída).
	src[0] = entry(t, "tool.a", "5.0.0", "sha256:x", domain.KindTool, "")
	third := m.Deps()
	if third[0].Version != "1.0.0" {
		t.Fatalf("manifesto afectado por mutacao do input: %+v", third[0])
	}

	if m.Fingerprint() != fp0 {
		t.Fatalf("fingerprint instavel: %q != %q", m.Fingerprint(), fp0)
	}
}

// TestDependencyManifestFingerprintStableAcrossOrder garante que a mesma
// composição (independente da ordem de entrada) produz o MESMO fingerprint
// (determinismo tamper-evident).
func TestDependencyManifestFingerprintStableAcrossOrder(t *testing.T) {
	t.Parallel()
	a := entry(t, "tool.a", "1.0.0", "sha256:a", domain.KindTool, "")
	b := entry(t, "tool.b", "2.0.0", "sha256:b", domain.KindTool, "")
	m1, err := NewDependencyManifest("t", "m", "sha256:p", []domain.Entry{a, b})
	if err != nil {
		t.Fatalf("m1: %v", err)
	}
	m2, err := NewDependencyManifest("t", "m", "sha256:p", []domain.Entry{b, a}) // ordem invertida
	if err != nil {
		t.Fatalf("m2: %v", err)
	}
	if m1.Fingerprint() != m2.Fingerprint() {
		t.Fatalf("fingerprint depende da ordem de entrada: %q != %q", m1.Fingerprint(), m2.Fingerprint())
	}
	// Uma composição diferente (digest diferente) produz fingerprint diferente.
	c := entry(t, "tool.b", "2.0.0", "sha256:DIFFERENT", domain.KindTool, "")
	m3, err := NewDependencyManifest("t", "m", "sha256:p", []domain.Entry{a, c})
	if err != nil {
		t.Fatalf("m3: %v", err)
	}
	if m1.Fingerprint() == m3.Fingerprint() {
		t.Fatalf("fingerprint colide para conteudo diferente")
	}
}

// TestDependencyManifestFailClosed rejeita âncoras de replay em falta.
func TestDependencyManifestFailClosed(t *testing.T) {
	t.Parallel()
	e := []domain.Entry{entry(t, "tool.a", "1.0.0", "sha256:a", domain.KindTool, "")}
	cases := []struct{ traj, model, prompt string }{
		{"", "m", "p"},
		{"t", "", "p"},
		{"t", "m", ""},
	}
	for _, c := range cases {
		if _, err := NewDependencyManifest(c.traj, c.model, c.prompt, e); !errors.Is(err, ErrInvalidManifest) {
			t.Fatalf("NewDependencyManifest(%q,%q,%q) err = %v, quer ErrInvalidManifest", c.traj, c.model, c.prompt, err)
		}
	}
}

// TestFromRuntimeManifest liga o manifesto por turno do RT ao manifesto de
// dependências imutável do REG (reutilização, sem reimplementar).
func TestFromRuntimeManifest(t *testing.T) {
	t.Parallel()
	rt := agentruntime.Manifest{
		PromptHash: "sha256:prompt",
		Model:      agentruntime.ModelManifest{ModelID: "claude-opus-4-8"},
		Tools: []agentruntime.PinnedDep{
			{Name: "tool.http.get", Version: "1.2.0", Digest: "sha256:aaa"},
		},
		Skills: []agentruntime.PinnedDep{
			{Name: "skill.summarize", Version: "0.9.0", Digest: "sha256:bbb"},
		},
	}
	m, err := FromRuntimeManifest("traj-9", rt)
	if err != nil {
		t.Fatalf("FromRuntimeManifest: %v", err)
	}
	if m.ModelID() != "claude-opus-4-8" || m.PromptHash() != "sha256:prompt" {
		t.Fatalf("model/prompt nao reutilizados: %q %q", m.ModelID(), m.PromptHash())
	}
	if len(m.Deps()) != 2 {
		t.Fatalf("nº deps = %d, quer 2 (tools+skills)", len(m.Deps()))
	}

	// Fail-closed: manifesto RT sem model-id.
	bad := agentruntime.Manifest{PromptHash: "sha256:p"}
	if _, err := FromRuntimeManifest("t", bad); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("esperava ErrInvalidManifest, obteve %v", err)
	}
}

// TestDependencyManifestJSON confirma que a serialização reflecte o conteúdo
// coberto pelo fingerprint (round-trip estável dos campos).
func TestDependencyManifestJSON(t *testing.T) {
	t.Parallel()
	m, err := NewDependencyManifest("traj", "model", "sha256:p",
		[]domain.Entry{entry(t, "tool.a", "1.0.0", "sha256:a", domain.KindTool, "")})
	if err != nil {
		t.Fatalf("NewDependencyManifest: %v", err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var wire struct {
		TrajectoryID string `json:"trajectory_id"`
		ModelID      string `json:"model_id"`
		PromptHash   string `json:"prompt_hash"`
		Deps         []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			Digest  string `json:"digest"`
		} `json:"deps"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if wire.TrajectoryID != "traj" || wire.ModelID != "model" || wire.PromptHash != "sha256:p" {
		t.Fatalf("wire = %+v", wire)
	}
	if len(wire.Deps) != 1 || wire.Deps[0].Name != "tool.a" || wire.Deps[0].Digest != "sha256:a" {
		t.Fatalf("deps = %+v", wire.Deps)
	}
}
