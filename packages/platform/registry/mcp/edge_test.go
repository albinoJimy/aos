package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aos-ref/platform/memory/provenance"
)

// TestRPCError_String cobre a formatação do erro JSON-RPC do servidor.
func TestRPCError_String(t *testing.T) {
	t.Parallel()
	e := &rpcError{Code: -32601, Message: "method not found"}
	if got := e.Error(); got == "" || got[:8] != "jsonrpc " {
		t.Fatalf("rpcError.Error() = %q", got)
	}
}

// TestDefaultIDSequence prova que os transportes funcionam com o gerador de id por
// omissão (monotónico interno), sem seq injectada.
func TestDefaultIDSequence(t *testing.T) {
	t.Parallel()
	// STDIO com next por omissão.
	stdio, err := NewSTDIOTransport(context.Background(), &fakeLauncher{}, LaunchSpec{Command: "s"}, nil)
	if err != nil {
		t.Fatalf("NewSTDIOTransport: %v", err)
	}
	defer func() { _ = stdio.Close() }()
	if _, err := stdio.Call(context.Background(), methodToolsList, nil); err != nil {
		t.Fatalf("stdio Call (default id): %v", err)
	}

	// SSE e Streamable HTTP com next por omissão.
	srv := httptest.NewTLSServer(sseHandler())
	defer srv.Close()
	allow := NewStaticEgressAllowlist(hostOf(t, srv.URL))
	sse, err := NewSSETransport(srv.URL, allow, srv.Client(), nil)
	if err != nil {
		t.Fatalf("NewSSETransport: %v", err)
	}
	if _, err := sse.Call(context.Background(), methodToolsList, nil); err != nil {
		t.Fatalf("sse Call (default id): %v", err)
	}

	rec := &streamableRecorder{}
	jsrv := httptest.NewTLSServer(rec.handler())
	defer jsrv.Close()
	jallow := NewStaticEgressAllowlist(hostOf(t, jsrv.URL))
	sh, err := NewStreamableHTTPTransport(jsrv.URL, jallow, jsrv.Client(), WithoutAuth())
	if err != nil {
		t.Fatalf("NewStreamableHTTPTransport: %v", err)
	}
	if _, err := sh.Call(context.Background(), methodInitialize, nil); err != nil {
		t.Fatalf("streamable Call (default id): %v", err)
	}
}

// TestWithIngestor prova que um Ingestor de proveniência injectado é usado na taint.
func TestWithIngestor(t *testing.T) {
	t.Parallel()
	reg := newTestRegistry(t)
	ing := provenance.NewIngestor(provenance.DefaultTaintController{})
	h, err := NewHost(reg, WithClock(fixedClock()), WithIngestor(ing))
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	tr, err := NewSTDIOTransport(context.Background(), &fakeLauncher{}, LaunchSpec{Command: "s"}, seqFrom(1))
	if err != nil {
		t.Fatalf("NewSTDIOTransport: %v", err)
	}
	defer func() { _ = tr.Close() }()
	res, err := h.Discover(context.Background(), tr, testConn("mcp.x"))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if res.Taint.Quarantine().Len() == 0 {
		t.Fatal("ingestor injectado devia produzir quarentena")
	}
}

// errServer devolve um erro JSON-RPC no método dado (para exercitar as vias de erro
// do handshake e a propagação do rpcError do servidor).
func errServer(failMethod string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var resp rpcResponse
		if req.Method == failMethod {
			resp = rpcResponse{JSONRPC: jsonRPCVersion, ID: req.ID, Error: &rpcError{Code: -32000, Message: "boom"}}
		} else {
			resp = handleRPC(req)
		}
		b, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
	})
}

// TestHandshake_ServerErrorPropagates prova que um erro do servidor no handshake é
// propagado como ErrHandshakeFailed (fail-closed).
func TestHandshake_ServerErrorPropagates(t *testing.T) {
	t.Parallel()
	cases := []string{methodInitialize, methodToolsList}
	for _, fail := range cases {
		fail := fail
		t.Run(fail, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewTLSServer(errServer(fail))
			defer srv.Close()
			allow := NewStaticEgressAllowlist(hostOf(t, srv.URL))
			tr, err := NewStreamableHTTPTransport(srv.URL, allow, srv.Client(), WithIDSequence(seqFrom(1)), WithoutAuth())
			if err != nil {
				t.Fatalf("transport: %v", err)
			}
			h, _ := newTestHost(t, nil)
			if _, err := h.Handshake(context.Background(), tr); !errors.Is(err, ErrHandshakeFailed) {
				t.Fatalf("erro = %v, quer ErrHandshakeFailed", err)
			}
		})
	}
}

// TestResourcesListOptional prova que um servidor sem resources/list (erro nesse
// método) ainda descobre tools — resources/list é informativo/opcional.
func TestResourcesListOptional(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(errServer(methodResourcesList))
	defer srv.Close()
	allow := NewStaticEgressAllowlist(hostOf(t, srv.URL))
	tr, err := NewStreamableHTTPTransport(srv.URL, allow, srv.Client(), WithIDSequence(seqFrom(1)), WithoutAuth())
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	h, _ := newTestHost(t, nil)
	m, err := h.Handshake(context.Background(), tr)
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	if len(m.Tools) != 2 {
		t.Fatalf("tools = %d, quer 2", len(m.Tools))
	}
	if len(m.Resources) != 0 {
		t.Fatalf("resources = %d, quer 0 (servidor sem resources)", len(m.Resources))
	}
}

// TestCallAfterClose prova que Call após Close falha fail-closed.
func TestCallAfterClose(t *testing.T) {
	t.Parallel()
	tr, err := NewSTDIOTransport(context.Background(), &fakeLauncher{}, LaunchSpec{Command: "s"}, seqFrom(1))
	if err != nil {
		t.Fatalf("NewSTDIOTransport: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := tr.Call(context.Background(), methodToolsList, nil); !errors.Is(err, ErrTransportClosed) {
		t.Fatalf("Call apos Close = %v, quer ErrTransportClosed", err)
	}
}

// TestNoRegistry prova que o Host exige um Registry.
func TestNoRegistry(t *testing.T) {
	t.Parallel()
	if _, err := NewHost(nil); !errors.Is(err, ErrNoRegistry) {
		t.Fatalf("NewHost(nil) = %v, quer ErrNoRegistry", err)
	}
}

// TestEgressAllowlist_Empty prova o default-deny do StaticEgressAllowlist vazio.
func TestEgressAllowlist_Empty(t *testing.T) {
	t.Parallel()
	if NewStaticEgressAllowlist().Allowed("x") {
		t.Fatal("allowlist vazia devia negar tudo")
	}
	if NewStaticEgressAllowlist("a").Allowed("") {
		t.Fatal("host vazio devia ser negado")
	}
	var nilAllow *StaticEgressAllowlist
	if nilAllow.Allowed("a") {
		t.Fatal("allowlist nil devia negar")
	}
}
