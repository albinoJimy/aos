package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
)

// sseTransport é o transporte MCP de TRANSIÇÃO (Server-Sent Events). É remoto: exige
// TLS OBRIGATÓRIO e endpoint na egress allowlist (ambos impostos em construção,
// fail-closed). Cada Call faz POST do pedido JSON-RPC e lê a resposta do stream
// text/event-stream devolvido pelo servidor.
type sseTransport struct {
	cfg  remoteConfig
	mu   sync.Mutex
	id   int64
	next func() int64
}

// NewSSETransport valida o endpoint (TLS + egress) e constrói o transporte SSE. Um
// endpoint sem https devolve [ErrTLSRequired]; um host fora da allowlist devolve
// [ErrEgressBlocked] (default-deny). O client permite injectar o *http.Client de um
// httptest.NewTLSServer em teste; nil usa http.DefaultClient. idSeq injectável.
func NewSSETransport(endpoint string, allow EgressAllowlist, client *http.Client, idSeq func() int64) (Transport, error) {
	cfg, err := validateRemote(endpoint, allow, client)
	if err != nil {
		return nil, err
	}
	t := &sseTransport{cfg: cfg, next: idSeq}
	if t.next == nil {
		t.next = t.monotonic
	}
	return t, nil
}

func (t *sseTransport) monotonic() int64 { t.id++; return t.id }

// Kind implementa [Transport].
func (t *sseTransport) Kind() TransportKind { return TransportSSE }

// Call faz POST do pedido e desembrulha a resposta do stream SSE.
func (t *sseTransport) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	t.mu.Lock()
	id := t.next()
	t.mu.Unlock()

	req, err := newRequest(id, method, params)
	if err != nil {
		return nil, err
	}
	resp, err := doPost(ctx, t.cfg.client, t.cfg.endpoint.String(), req, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, ErrProtocol
	}
	return decodeResult(resp.Header.Get("Content-Type"), resp.Body, id)
}

// Close é um no-op (o SSE deste host não mantém ligação persistente entre Calls).
func (t *sseTransport) Close() error { return nil }
