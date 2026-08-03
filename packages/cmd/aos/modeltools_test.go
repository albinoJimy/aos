package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// stubModel devolve uma tool call fixa (o modelo "escolhe" a tool pelo nome). Prova o enriquecimento
// sem falar com um upstream real.
type stubModel struct{ toolID string }

func (m stubModel) Call(_ context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	return agentruntime.ModelResponse{
		ToolCalls: []agentruntime.ToolInvocation{{ToolID: m.toolID, Input: []byte(`{"url":"https://x"}`)}},
	}, nil
}

func writeTools(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tools.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLoadModelTools_Unset — sem AOS_MODEL_TOOLS não há tools nem bindings (comportamento inalterado).
func TestLoadModelTools_Unset(t *testing.T) {
	t.Setenv("AOS_MODEL_TOOLS", "")
	tools, bindings, err := loadModelToolsFromEnv()
	if err != nil || tools != nil || bindings != nil {
		t.Fatalf("esperado (nil,nil,nil): tools=%v bindings=%v err=%v", tools, bindings, err)
	}
}

// TestLoadModelTools_FailClosed — registry mal-formado (tool sem capability) ABORTA fail-closed.
func TestLoadModelTools_FailClosed(t *testing.T) {
	t.Setenv("AOS_MODEL_TOOLS", writeTools(t, `[{"name":"web_post"}]`))
	if _, _, err := loadModelToolsFromEnv(); err == nil {
		t.Fatal("esperado erro fail-closed para tool sem capability")
	}
	t.Setenv("AOS_MODEL_TOOLS", filepath.Join(t.TempDir(), "nao-existe.json"))
	if _, _, err := loadModelToolsFromEnv(); err == nil {
		t.Fatal("esperado erro para ficheiro ausente")
	}
}

// TestToolEnrichment_BindsCapabilityNotTaint — o enriquecedor preenche Capability/Resource do
// registry TRUSTED mas NUNCA AuthorizationTaint (invariante AOS-069: fica untrusted → o TaintGate
// nega a capability privilegiada). É a peça que torna a mediação PDP demonstrável fim-a-fim.
func TestToolEnrichment_BindsCapabilityNotTaint(t *testing.T) {
	t.Setenv("AOS_MODEL_TOOLS", writeTools(t, `[{
		"name":"web_post","description":"HTTP POST","capability":"cap:http.post",
		"resource_type":"http","resource_value":"https://api.example.com","resource_region":"eu"
	}]`))
	_, bindings, err := loadModelToolsFromEnv()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	c := &toolEnrichingClient{inner: stubModel{toolID: "web_post"}, bindings: bindings}
	resp, err := c.Call(context.Background(), agentruntime.PromptView{Turn: 1})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("esperado 1 tool call, got %d", len(resp.ToolCalls))
	}
	inv := resp.ToolCalls[0]
	if inv.Capability != "cap:http.post" {
		t.Errorf("Capability não enriquecida do registry: %q", inv.Capability)
	}
	if inv.ResourceType != "http" || inv.ResourceRegion != "eu" {
		t.Errorf("Resource não enriquecido: type=%q region=%q", inv.ResourceType, inv.ResourceRegion)
	}
	// INVARIANTE: AuthorizationTaint tem de ficar VAZIO (untrusted, fail-closed). Se algum dia for
	// preenchido a partir da saída do modelo, a escalada de privilégio (P4) fica aberta.
	if inv.AuthorizationTaint != "" {
		t.Errorf("AuthorizationTaint NÃO pode ser preenchido pelo enriquecedor, got %q", inv.AuthorizationTaint)
	}
}

// TestToolEnrichment_UnknownToolStaysDenied — uma tool fora do registry fica com Capability vazia ⇒
// default-deny no RM. O modelo não pode inventar uma capability.
func TestToolEnrichment_UnknownToolStaysDenied(t *testing.T) {
	c := &toolEnrichingClient{
		inner:    stubModel{toolID: "rm_rf_root"},
		bindings: map[string]toolBinding{"web_post": {capability: "cap:http.post"}},
	}
	resp, err := c.Call(context.Background(), agentruntime.PromptView{Turn: 1})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.ToolCalls[0].Capability != "" {
		t.Errorf("tool fora do registry devia ficar sem capability, got %q", resp.ToolCalls[0].Capability)
	}
}
