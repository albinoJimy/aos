package mcp

import (
	"context"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/registry/domain"
)

// TestDiscovery_ProducesStagingNeverActive prova que a descoberta produz entradas
// CANDIDATAS no REG em staging — NUNCA directamente active (EPIC-05 §4). Verifica o
// servidor MCP e cada tool descoberta.
func TestDiscovery_ProducesStagingNeverActive(t *testing.T) {
	t.Parallel()
	h, reg := newTestHost(t, nil)
	tr, err := NewSTDIOTransport(context.Background(), &fakeLauncher{}, LaunchSpec{Command: "server"}, seqFrom(1))
	if err != nil {
		t.Fatalf("NewSTDIOTransport: %v", err)
	}
	defer func() { _ = tr.Close() }()

	conn := testConn("mcp.filesystem")
	res, err := h.Discover(context.Background(), tr, conn)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// 1 servidor + 2 tools = 3 entradas candidatas.
	if len(res.Staged) != 3 {
		t.Fatalf("staged = %d, quer 3 (1 servidor + 2 tools)", len(res.Staged))
	}

	// TODAS em staging; NENHUMA active.
	for _, e := range res.Staged {
		if e.Status != domain.StatusStaging {
			t.Fatalf("%s@%s status = %q, quer staging", e.ID, e.Version, e.Status)
		}
	}

	// O servidor é do tipo mcp_server; as tools do tipo tool.
	server := res.Staged[0]
	if server.ID != "mcp.filesystem" || server.Kind != domain.KindMCPServer {
		t.Fatalf("primeira entrada = %s/%s, quer mcp.filesystem/mcp_server", server.ID, server.Kind)
	}
	for _, e := range res.Staged[1:] {
		if e.Kind != domain.KindTool {
			t.Fatalf("%s kind = %q, quer tool", e.ID, e.Kind)
		}
	}

	// DEFAULT-DENY end-to-end: uma entrada descoberta NÃO é despachável (não é active).
	ok, reason, err := reg.IsAdmissible(context.Background(), server.ID, conn.Version)
	if err != nil {
		t.Fatalf("IsAdmissible: %v", err)
	}
	if ok {
		t.Fatal("uma entrada recem-descoberta NAO pode ser admissivel (esta em staging)")
	}
	if reason == "" {
		t.Fatal("IsAdmissible devia dar uma razao de recusa")
	}

	// Resolvível por versão pinada (o RT consegue lê-la), mesmo em staging.
	if _, err := reg.Resolve(context.Background(), server.ID, conn.Version); err != nil {
		t.Fatalf("Resolve da entrada em staging: %v", err)
	}
}

// TestDiscovery_InvalidConnection prova a validação fail-closed da ConnectionInfo.
func TestDiscovery_InvalidConnection(t *testing.T) {
	t.Parallel()
	h, _ := newTestHost(t, nil)
	tr, err := NewSTDIOTransport(context.Background(), &fakeLauncher{}, LaunchSpec{Command: "server"}, seqFrom(1))
	if err != nil {
		t.Fatalf("NewSTDIOTransport: %v", err)
	}
	defer func() { _ = tr.Close() }()

	bad := []ConnectionInfo{
		{Version: ver(1, 0, 0), RunID: "r", AgentID: "a"},    // sem ServerID
		{ServerID: "x", RunID: "r", AgentID: "a"},            // versão zero
		{ServerID: "x", Version: ver(1, 0, 0), AgentID: "a"}, // sem RunID
		{ServerID: "x", Version: ver(1, 0, 0), RunID: "r"},   // sem AgentID
	}
	for i, conn := range bad {
		if _, err := h.Discover(context.Background(), tr, conn); err == nil {
			t.Fatalf("caso %d: Discover devia recusar ConnectionInfo mal-formada", i)
		}
	}
}

// TestIsolation_STDIOViaSandboxPort_NoHostSocket prova o isolamento (ADR-004): o
// STDIO passa SEMPRE pela porta SandboxLauncher (o launcher é invocado) e o processo
// NÃO recebe o ambiente do host (Env vazio = sem sockets/segredos via variáveis).
func TestIsolation_STDIOViaSandboxPort_NoHostSocket(t *testing.T) {
	t.Parallel()
	launcher := &fakeLauncher{}
	spec := LaunchSpec{Command: "mcp-server-bin"} // Env deliberadamente vazio
	tr, err := NewSTDIOTransport(context.Background(), launcher, spec, seqFrom(1))
	if err != nil {
		t.Fatalf("NewSTDIOTransport: %v", err)
	}
	defer func() { _ = tr.Close() }()

	h, _ := newTestHost(t, nil)
	if _, err := h.Handshake(context.Background(), tr); err != nil {
		t.Fatalf("Handshake: %v", err)
	}

	launched, lastSpec := launcher.stats()
	if launched != 1 {
		t.Fatalf("SandboxLauncher invocado %d vezes, quer 1 (STDIO corre SEMPRE via a porta)", launched)
	}
	// Sem ambiente do host: nenhuma variável (logo nenhum socket/segredo) vaza para
	// dentro da sandbox.
	if len(lastSpec.Env) != 0 {
		t.Fatalf("LaunchSpec.Env = %v, quer vazio (sem ambiente do host na sandbox)", lastSpec.Env)
	}
	if lastSpec.Command != "mcp-server-bin" {
		t.Fatalf("LaunchSpec.Command = %q", lastSpec.Command)
	}
}

// TestDiscovery_EmitsSpansNoSecrets prova que a descoberta emite spans OTel GenAI e
// que NENHUM segredo (token/sessão) aparece nos atributos.
func TestDiscovery_EmitsSpansNoSecrets(t *testing.T) {
	t.Parallel()
	tracer := &agentruntime.RecordingTracer{}
	h, _ := newTestHost(t, tracer)
	tr, err := NewSTDIOTransport(context.Background(), &fakeLauncher{}, LaunchSpec{Command: "server"}, seqFrom(1))
	if err != nil {
		t.Fatalf("NewSTDIOTransport: %v", err)
	}
	defer func() { _ = tr.Close() }()

	if _, err := h.Discover(context.Background(), tr, testConn("mcp.fs")); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	spans := tracer.SpansByOperation(opDiscover)
	if len(spans) == 0 {
		t.Fatal("esperava pelo menos um span registry.mcp.discover")
	}
	for _, s := range tracer.Spans() {
		for k, v := range s.Attributes {
			if str, ok := v.(string); ok {
				if containsSecret(str) {
					t.Fatalf("span %s atributo %s contem material sensivel: %q", s.Operation, k, str)
				}
			}
		}
	}
}

// containsSecret é uma heurística de teste: nenhum token/sessão de exemplo deve
// aparecer em spans.
func containsSecret(s string) bool {
	for _, needle := range []string{"Bearer", "s3cr3t", "session-abc", "id_rsa"} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
