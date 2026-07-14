package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
)

// mcpSessionHeader é o header de sessão do Streamable HTTP (spec MCP). O servidor
// devolve-o no initialize; o host reenvia-o nas chamadas seguintes. NUNCA é gravado
// em spans/logs (é material de sessão, tratado como segredo).
const mcpSessionHeader = "Mcp-Session-Id"

// streamableHTTPTransport é o transporte remoto RECOMENDADO: request/response e
// streaming num único endpoint HTTP. Exige TLS OBRIGATÓRIO + egress allowlist (como
// o SSE) e acrescenta AUTENTICAÇÃO DO HOST (Bearer) e SESSÕES (Mcp-Session-Id).
type streamableHTTPTransport struct {
	cfg   remoteConfig
	token string // Bearer do host; SEGREDO — nunca em spans/logs.
	// noAuth marca que o transporte foi EXPLICITAMENTE construído sem autenticação
	// (via [WithoutAuth]). Sem esta marca, a ausência de token é recusada fail-closed.
	noAuth bool

	mu        sync.Mutex
	sessionID string // capturado do initialize; reenviado nas chamadas seguintes.
	id        int64
	next      func() int64
}

// StreamableHTTPOption configura o transporte.
type StreamableHTTPOption func(*streamableHTTPTransport)

// WithBearerToken injecta o token de autenticação do host (enviado como
// Authorization: Bearer …). É um SEGREDO: é usado no header mas NUNCA colocado em
// spans, logs ou erros.
func WithBearerToken(token string) StreamableHTTPOption {
	return func(t *streamableHTTPTransport) { t.token = token }
}

// WithIDSequence injecta o gerador de ids JSON-RPC (determinismo em teste).
func WithIDSequence(seq func() int64) StreamableHTTPOption {
	return func(t *streamableHTTPTransport) {
		if seq != nil {
			t.next = seq
		}
	}
}

// WithoutAuth desactiva EXPLICITAMENTE a autenticação do host no Streamable HTTP. Por
// omissão a auth é OBRIGATÓRIA (fail-closed): sem token nem esta opção, a construção é
// recusada com [ErrAuthRequired]. Só se deve usar em cenários deliberados (ex.: um
// servidor MCP sem auth num perímetro já confiado) e fica documentado no call-site.
func WithoutAuth() StreamableHTTPOption {
	return func(t *streamableHTTPTransport) { t.noAuth = true }
}

// NewStreamableHTTPTransport valida o endpoint (TLS + egress), EXIGE autenticação do
// host (Bearer) e constrói o transporte recomendado. Recusas fail-closed, por esta
// ordem: [ErrTLSRequired] → [ErrEgressBlocked] → [ErrAuthRequired] (a auth só é
// avaliada depois de o endpoint passar TLS+egress). A auth é dispensável apenas com a
// opção explícita [WithoutAuth].
func NewStreamableHTTPTransport(endpoint string, allow EgressAllowlist, client *http.Client, opts ...StreamableHTTPOption) (Transport, error) {
	cfg, err := validateRemote(endpoint, allow, client)
	if err != nil {
		return nil, err
	}
	t := &streamableHTTPTransport{cfg: cfg}
	for _, o := range opts {
		o(t)
	}
	// Auth OBRIGATÓRIA fail-closed (simétrica a TLS/egress): sem credencial e sem
	// WithoutAuth() explícito, recusa a construção.
	if t.token == "" && !t.noAuth {
		return nil, ErrAuthRequired
	}
	if t.next == nil {
		t.next = t.monotonic
	}
	return t, nil
}

func (t *streamableHTTPTransport) monotonic() int64 { t.id++; return t.id }

// Kind implementa [Transport].
func (t *streamableHTTPTransport) Kind() TransportKind { return TransportStreamableHTTP }

// Call faz POST do pedido com auth do host e o Mcp-Session-Id corrente; desembrulha a
// resposta (application/json OU text/event-stream). Se a resposta trouxer um
// Mcp-Session-Id novo (tipicamente no initialize), captura-o para as chamadas
// seguintes.
func (t *streamableHTTPTransport) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	t.mu.Lock()
	id := t.next()
	session := t.sessionID
	t.mu.Unlock()

	req, err := newRequest(id, method, params)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{}
	if t.token != "" {
		headers["Authorization"] = "Bearer " + t.token
	}
	if session != "" {
		headers[mcpSessionHeader] = session
	}
	resp, err := doPost(ctx, t.cfg.client, t.cfg.endpoint.String(), req, headers)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, ErrProtocol
	}
	// Captura/actualiza a sessão devolvida pelo servidor.
	if sid := resp.Header.Get(mcpSessionHeader); sid != "" {
		t.mu.Lock()
		t.sessionID = sid
		t.mu.Unlock()
	}
	return decodeResult(resp.Header.Get("Content-Type"), resp.Body, id)
}

// SessionID devolve o Mcp-Session-Id corrente (para asserção em teste de que a sessão
// foi estabelecida). NÃO é logado nem colocado em spans.
func (t *streamableHTTPTransport) SessionID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessionID
}

// Close é um no-op (sem ligação persistente entre Calls neste host).
func (t *streamableHTTPTransport) Close() error { return nil }
