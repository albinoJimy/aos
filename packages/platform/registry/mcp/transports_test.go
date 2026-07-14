package mcp

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"testing"
)

// buildTransport constrói cada um dos três transportes contra um servidor de teste
// determinista. Devolve o transporte e uma função de limpeza.
type transportCase struct {
	name string
	kind TransportKind
	// build devolve o transporte pronto a usar.
	build func(t *testing.T) (Transport, func())
}

func allTransports() []transportCase {
	return []transportCase{
		{
			name: "stdio_fake_launcher",
			kind: TransportSTDIO,
			build: func(t *testing.T) (Transport, func()) {
				tr, err := NewSTDIOTransport(context.Background(), &fakeLauncher{}, LaunchSpec{Command: "server"}, seqFrom(1))
				if err != nil {
					t.Fatalf("NewSTDIOTransport: %v", err)
				}
				return tr, func() { _ = tr.Close() }
			},
		},
		{
			name: "sse_tls",
			kind: TransportSSE,
			build: func(t *testing.T) (Transport, func()) {
				srv := httptest.NewTLSServer(sseHandler())
				allow := NewStaticEgressAllowlist(hostOf(t, srv.URL))
				tr, err := NewSSETransport(srv.URL, allow, srv.Client(), seqFrom(1))
				if err != nil {
					t.Fatalf("NewSSETransport: %v", err)
				}
				return tr, func() { _ = tr.Close(); srv.Close() }
			},
		},
		{
			name: "streamable_http_tls_auth_session",
			kind: TransportStreamableHTTP,
			build: func(t *testing.T) (Transport, func()) {
				rec := &streamableRecorder{}
				srv := httptest.NewTLSServer(rec.handler())
				allow := NewStaticEgressAllowlist(hostOf(t, srv.URL))
				tr, err := NewStreamableHTTPTransport(srv.URL, allow, srv.Client(),
					WithBearerToken("s3cr3t-token"), WithIDSequence(seqFrom(1)))
				if err != nil {
					t.Fatalf("NewStreamableHTTPTransport: %v", err)
				}
				return tr, func() { _ = tr.Close(); srv.Close() }
			},
		},
	}
}

// TestIntegration_ThreeTransports_ToolsList prova que a ligação + handshake +
// tools/list funciona nos TRÊS transportes contra servidores de teste.
func TestIntegration_ThreeTransports_ToolsList(t *testing.T) {
	t.Parallel()
	h, _ := newTestHost(t, nil)
	for _, tc := range allTransports() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr, cleanup := tc.build(t)
			defer cleanup()
			if tr.Kind() != tc.kind {
				t.Fatalf("Kind = %s, quer %s", tr.Kind(), tc.kind)
			}
			manifest, err := h.Handshake(context.Background(), tr)
			if err != nil {
				t.Fatalf("Handshake: %v", err)
			}
			if manifest.ServerInfo.Name != "test-mcp-server" {
				t.Fatalf("serverInfo.name = %q", manifest.ServerInfo.Name)
			}
			if len(manifest.Tools) != 2 {
				t.Fatalf("tools/list devolveu %d tools, quer 2", len(manifest.Tools))
			}
			if manifest.Tools[0].Name != "read_file" {
				t.Fatalf("primeira tool = %q", manifest.Tools[0].Name)
			}
			if len(manifest.Resources) != 1 {
				t.Fatalf("resources/list devolveu %d, quer 1", len(manifest.Resources))
			}
			// O digest do manifesto é RESERVADO (AOS-047): deve ficar vazio.
			if manifest.Digest != "" {
				t.Fatalf("Digest devia ser reservado/vazio, veio %q", manifest.Digest)
			}
		})
	}
}

// TestStreamableHTTP_AuthAndSession prova que a auth do host (Bearer) e a sessão
// (Mcp-Session-Id) são transportadas: o token chega ao servidor e a sessão emitida no
// initialize é reutilizada na chamada seguinte.
func TestStreamableHTTP_AuthAndSession(t *testing.T) {
	t.Parallel()
	rec := &streamableRecorder{}
	srv := httptest.NewTLSServer(rec.handler())
	defer srv.Close()
	allow := NewStaticEgressAllowlist(hostOf(t, srv.URL))
	tr, err := NewStreamableHTTPTransport(srv.URL, allow, srv.Client(),
		WithBearerToken("s3cr3t-token"), WithIDSequence(seqFrom(1)))
	if err != nil {
		t.Fatalf("NewStreamableHTTPTransport: %v", err)
	}
	defer func() { _ = tr.Close() }()

	h, _ := newTestHost(t, nil)
	if _, err := h.Handshake(context.Background(), tr); err != nil {
		t.Fatalf("Handshake: %v", err)
	}

	gotAuth, sessions := rec.snapshot()
	if gotAuth != "Bearer s3cr3t-token" {
		t.Fatalf("Authorization no servidor = %q, quer Bearer s3cr3t-token", gotAuth)
	}
	// A primeira chamada (initialize) não traz sessão; as seguintes trazem a emitida.
	if len(sessions) < 2 {
		t.Fatalf("esperava >= 2 chamadas, veio %d", len(sessions))
	}
	if sessions[0] != "" {
		t.Fatalf("initialize nao devia trazer sessao, trouxe %q", sessions[0])
	}
	if sessions[1] != "session-abc-123" {
		t.Fatalf("2a chamada devia reutilizar a sessao, trouxe %q", sessions[1])
	}
	// A sessão está estabelecida no transporte.
	if st, ok := tr.(*streamableHTTPTransport); ok && st.SessionID() != "session-abc-123" {
		t.Fatalf("SessionID = %q", st.SessionID())
	}
}

// TestRemote_TLSMandatory prova que um endpoint sem TLS (http:// puro) é recusado
// fail-closed nos dois transportes remotos.
func TestRemote_TLSMandatory(t *testing.T) {
	t.Parallel()
	allow := NewStaticEgressAllowlist("mcp.example")
	cases := []struct {
		name string
		err  error
		fn   func() (Transport, error)
	}{
		{"sse_http", ErrTLSRequired, func() (Transport, error) {
			return NewSSETransport("http://mcp.example/sse", allow, nil, nil)
		}},
		{"streamable_http_plain", ErrTLSRequired, func() (Transport, error) {
			return NewStreamableHTTPTransport("http://mcp.example/mcp", allow, nil)
		}},
		{"sse_no_scheme", ErrTLSRequired, func() (Transport, error) {
			return NewSSETransport("ftp://mcp.example/x", allow, nil, nil)
		}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := tc.fn(); !errors.Is(err, tc.err) {
				t.Fatalf("erro = %v, quer %v", err, tc.err)
			}
		})
	}
}

// TestRemote_EgressBlocked prova que um endpoint TLS mas FORA da egress allowlist é
// bloqueado fail-closed — e que uma allowlist nil nega tudo (default-deny).
func TestRemote_EgressBlocked(t *testing.T) {
	t.Parallel()
	allow := NewStaticEgressAllowlist("permitido.example")
	cases := []struct {
		name string
		fn   func() (Transport, error)
	}{
		{"sse_fora_da_allowlist", func() (Transport, error) {
			return NewSSETransport("https://malicioso.example/sse", allow, nil, nil)
		}},
		{"streamable_fora_da_allowlist", func() (Transport, error) {
			return NewStreamableHTTPTransport("https://malicioso.example/mcp", allow, nil)
		}},
		{"allowlist_nil_nega_tudo", func() (Transport, error) {
			return NewSSETransport("https://permitido.example/sse", nil, nil, nil)
		}},
		{"deny_all_nega_tudo", func() (Transport, error) {
			return NewSSETransport("https://permitido.example/sse", DenyAllEgress{}, nil, nil)
		}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := tc.fn(); !errors.Is(err, ErrEgressBlocked) {
				t.Fatalf("erro = %v, quer ErrEgressBlocked", err)
			}
		})
	}
}

// TestRemote_EgressAllowed prova o lado positivo: um endpoint TLS na allowlist liga.
func TestRemote_EgressAllowed(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(sseHandler())
	defer srv.Close()
	allow := NewStaticEgressAllowlist(hostOf(t, srv.URL))
	tr, err := NewSSETransport(srv.URL, allow, srv.Client(), seqFrom(1))
	if err != nil {
		t.Fatalf("NewSSETransport (allowlisted): %v", err)
	}
	defer func() { _ = tr.Close() }()
	if _, err := tr.Call(context.Background(), methodToolsList, nil); err != nil {
		t.Fatalf("Call apos allowlist: %v", err)
	}
}

// TestSTDIO_RealSubprocess prova que o OSSandboxLauncher lança um SUBPROCESSO real
// (re-exec do binário de teste como servidor MCP) e o handshake completa por STDIO.
func TestSTDIO_RealSubprocess(t *testing.T) {
	t.Parallel()
	launcher := OSSandboxLauncher{}
	spec := LaunchSpec{
		Command: os.Args[0],
		Env:     []string{stdioHelperEnv + "=1"},
	}
	tr, err := NewSTDIOTransport(context.Background(), launcher, spec, seqFrom(1))
	if err != nil {
		t.Fatalf("NewSTDIOTransport (real): %v", err)
	}
	defer func() { _ = tr.Close() }()

	h, _ := newTestHost(t, nil)
	manifest, err := h.Handshake(context.Background(), tr)
	if err != nil {
		t.Fatalf("Handshake (subprocesso real): %v", err)
	}
	if len(manifest.Tools) != 2 {
		t.Fatalf("tools/list (real) = %d, quer 2", len(manifest.Tools))
	}
}

// TestNoLauncher_STDIORefused prova que sem SandboxLauncher o STDIO é recusado (corre
// SEMPRE em sandbox).
func TestNoLauncher_STDIORefused(t *testing.T) {
	t.Parallel()
	if _, err := NewSTDIOTransport(context.Background(), nil, LaunchSpec{Command: "x"}, nil); !errors.Is(err, ErrNoLauncher) {
		t.Fatalf("erro = %v, quer ErrNoLauncher", err)
	}
}

// hostOf extrai o hostname (sem porta) de um URL de teste, para a allowlist.
func hostOf(t *testing.T, rawURL string) string {
	t.Helper()
	u := mustParseHost(rawURL)
	if u == "" {
		t.Fatalf("URL de teste sem host: %q", rawURL)
	}
	return u
}
