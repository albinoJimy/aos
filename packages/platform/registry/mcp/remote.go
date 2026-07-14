package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultRemoteTimeout é o timeout POR OMISSÃO imposto ao *http.Client interno dos
// transportes remotos quando o chamador não fornece um. Sem isto, um remoto
// slow-loris (ou um stream SSE que nunca fecha) penduraria o host indefinidamente
// (http.DefaultClient tem Timeout=0 = sem limite). Fail-safe por omissão, mesmo que o
// chamador esqueça o deadline no ctx.
const defaultRemoteTimeout = 30 * time.Second

// maxRedirectHops é o número máximo de redirects 3xx que o host segue. Cada hop é
// re-validado contra a egress allowlist; este limite é uma defesa adicional contra
// cadeias de redirect abusivas.
const maxRedirectHops = 5

// remoteConfig é a validação COMUM aos transportes remotos (SSE, Streamable HTTP): o
// TLS obrigatório e a egress allowlist. Ambos são fail-closed e aplicados ANTES de
// qualquer ligação.
type remoteConfig struct {
	endpoint *url.URL
	client   *http.Client
}

// validateRemote impõe, por esta ordem e fail-closed, as duas fronteiras dos
// transportes remotos:
//
//  1. TLS OBRIGATÓRIO — o esquema tem de ser exactamente "https"; http:// puro (ou
//     qualquer esquema sem TLS) é recusado com [ErrTLSRequired]. Nunca há downgrade.
//  2. EGRESS ALLOWLIST — o host do endpoint tem de estar na [EgressAllowlist]; caso
//     contrário [ErrEgressBlocked]. allowlist nil = nega tudo (default-deny).
func validateRemote(rawURL string, allow EgressAllowlist, client *http.Client) (remoteConfig, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return remoteConfig{}, fmt.Errorf("%w: %q", ErrInvalidEndpoint, rawURL)
	}
	// (1) TLS obrigatório: recusa qualquer coisa que não seja https.
	if u.Scheme != "https" {
		return remoteConfig{}, fmt.Errorf("%w: esquema %q", ErrTLSRequired, u.Scheme)
	}
	// (2) Egress allowlist (fail-closed): allowlist nil nega tudo.
	if allow == nil || !allow.Allowed(u.Hostname()) {
		return remoteConfig{}, fmt.Errorf("%w: %q", ErrEgressBlocked, u.Hostname())
	}
	// (3) Cliente ENDURECIDO: a allowlist tem de ser consultada no ALVO EFECTIVO da
	// ligação, não só no endpoint declarado. Um servidor allowlisted (ou comprometido)
	// que responda 3xx Location: https://exfil.evil/ faria o cliente por omissão seguir
	// o redirect para fora da allowlist (fail-open, contornando ADR-004). Construímos um
	// *http.Client próprio — SEM mutar o client injectado/partilhado — que:
	//   - re-valida CADA hop de redirect (scheme https + host na allowlist);
	//   - impõe um timeout por omissão (defesa contra slow-loris).
	base := client
	if base == nil {
		base = http.DefaultClient
	}
	timeout := base.Timeout
	if timeout == 0 {
		timeout = defaultRemoteTimeout
	}
	hardened := &http.Client{
		Transport:     base.Transport, // preserva a config (ex.: TLS do httptest)
		Jar:           base.Jar,
		Timeout:       timeout,
		CheckRedirect: redirectGuard(allow),
	}
	return remoteConfig{endpoint: u, client: hardened}, nil
}

// redirectGuard devolve uma política CheckRedirect que re-valida cada hop de redirect
// contra as MESMAS fronteiras fail-closed do endpoint inicial (TLS obrigatório + egress
// allowlist) e limita o número de hops. Assim o alvo efectivo da ligação — mesmo após
// uma cadeia de 3xx — nunca escapa à allowlist.
func redirectGuard(allow EgressAllowlist) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirectHops {
			return fmt.Errorf("%w: demasiados redirects (%d)", ErrProtocol, len(via))
		}
		if req.URL.Scheme != "https" {
			return fmt.Errorf("%w: redirect para esquema %q", ErrTLSRequired, req.URL.Scheme)
		}
		if allow == nil || !allow.Allowed(req.URL.Hostname()) {
			return fmt.Errorf("%w: redirect para %q", ErrEgressBlocked, req.URL.Hostname())
		}
		return nil
	}
}

// decodeResult desembrulha um Result JSON-RPC de um corpo HTTP que pode ser
// application/json (uma resposta) OU text/event-stream (SSE — uma ou mais frames
// data:). Devolve o Result correspondente ao id pedido.
func decodeResult(contentType string, body io.Reader, wantID int64) (json.RawMessage, error) {
	if strings.Contains(contentType, "text/event-stream") {
		return decodeSSEResult(body, wantID)
	}
	var resp rpcResponse
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("%w: decode json: %v", ErrProtocol, err)
	}
	return resultOf(resp, wantID)
}

// decodeSSEResult lê frames SSE (`data: {json}`) e devolve o Result da primeira
// resposta JSON-RPC cujo id corresponde ao pedido. Frames de evento sem id
// correspondente (notificações) são ignoradas.
func decodeSSEResult(body io.Reader, wantID int64) (json.RawMessage, error) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal([]byte(payload), &resp); err != nil {
			return nil, fmt.Errorf("%w: unmarshal frame SSE: %v", ErrProtocol, err)
		}
		if resp.ID != wantID {
			continue
		}
		return resultOf(resp, wantID)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%w: scan SSE: %v", ErrProtocol, err)
	}
	return nil, fmt.Errorf("%w: sem resposta para id %d no stream SSE", ErrProtocol, wantID)
}

// resultOf valida o envelope de resposta e devolve o Result (ou o erro do servidor).
func resultOf(resp rpcResponse, wantID int64) (json.RawMessage, error) {
	if resp.Error != nil {
		return nil, resp.Error
	}
	if resp.ID != wantID {
		return nil, fmt.Errorf("%w: id de resposta %d != pedido %d", ErrProtocol, resp.ID, wantID)
	}
	return resp.Result, nil
}

// doPost envia um pedido JSON-RPC por POST com os headers dados e devolve a resposta
// HTTP. É partilhado por SSE e Streamable HTTP.
func doPost(ctx context.Context, client *http.Client, endpoint string, body []byte, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("%w: new request: %v", ErrProtocol, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		// Duplo %w: preserva o ErrProtocol E a cadeia interna (ex.: um redirect
		// bloqueado devolve, via *url.Error, o ErrEgressBlocked/ErrTLSRequired do
		// redirectGuard — detectável por errors.Is).
		return nil, fmt.Errorf("%w: http: %w", ErrProtocol, err)
	}
	return resp, nil
}
