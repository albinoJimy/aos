package adapters

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/aos-ref/platform/model-gateway/port"
)

// secretishPattern reconhece formas típicas de segredo (portador Bearer, chaves
// de provedor sk-/AIza.../ya29.) que um provedor mal-comportado poderia reflectir
// no corpo de um erro. [sanitizeProviderBody] redige-as antes de o corpo subir a
// montante (ADR-006: um segredo de infra nunca atravessa a fronteira RT/agente).
var secretishPattern = regexp.MustCompile(`(?i)(bearer\s+[A-Za-z0-9._~+/=-]{8,}|sk-[A-Za-z0-9._-]{8,}|AIza[0-9A-Za-z._-]{10,}|ya29\.[0-9A-Za-z._-]{10,})`)

// sanitizeProviderBody torna o corpo de resposta do provedor seguro para o embrulho
// num erro que sobe ao runtime/agente: trunca-o (o corpo não é controlado pelo GW)
// e redige padrões que pareçam segredos, para que um provedor comprometido que
// ecoe o header Authorization/bearer num 4xx não faça o token de infra vazar a
// montante. O corpo completo permanece disponível server-side (não é logado aqui).
func sanitizeProviderBody(body []byte) string {
	const maxLen = 512
	s := string(body)
	if len(s) > maxLen {
		s = s[:maxLen] + "…(truncado)"
	}
	return secretishPattern.ReplaceAllString(s, "[REDACTED]")
}

// ErrTruncatedStream — o corpo SSE terminou SEM o sentinela "[DONE]" nem um
// finish_reason terminal: o stream foi cortado a meio (ex.: ligação perdida a
// meio de uma tool call). É fail-closed — nunca é normalizado como uma conclusão
// limpa (que forjaria finish_reason="stop" e argumentos de tool parciais/inválidos).
var ErrTruncatedStream = errors.New("adapters: stream SSE truncado (sem [DONE] nem finish_reason terminal)")

// OpenAIHTTPAdapter é o adaptador REAL de referência: fala o wire OpenAI-compatible
// por HTTP (net/http + JSON stdlib), sem qualquer SDK de provider. É
// estruturalmente completo — constrói o pedido, injecta a credencial no header
// Authorization server-side, faz POST e desserializa a resposta (síncrona ou SSE
// para streaming). É testado contra httptest.Server (não contra um provider real).
//
// A credencial entra por [Credential] (segredo não-exportado); NUNCA é hard-coded
// nem registada — o header Authorization é construído localmente e não é logado.
type OpenAIHTTPAdapter struct {
	provider string
	baseURL  string
	client   *http.Client
}

// NewOpenAIHTTPAdapter constrói o adaptador HTTP. baseURL é a raiz da API
// compatível OpenAI (ex.: "https://host/v1"); client permite injectar um
// http.Client de teste (httptest). Se client for nil, usa http.DefaultClient.
func NewOpenAIHTTPAdapter(provider, baseURL string, client *http.Client) *OpenAIHTTPAdapter {
	if client == nil {
		client = http.DefaultClient
	}
	return &OpenAIHTTPAdapter{
		provider: provider,
		baseURL:  strings.TrimRight(baseURL, "/"),
		client:   client,
	}
}

// Provider implementa [Adapter].
func (a *OpenAIHTTPAdapter) Provider() string { return a.provider }

// Chat implementa [Adapter]: POST {baseURL}/chat/completions com o wire OpenAI.
func (a *OpenAIHTTPAdapter) Chat(ctx context.Context, req port.ChatRequest, cred Credential) (port.ChatResponse, error) {
	body, err := req.MarshalWire(false)
	if err != nil {
		return port.ChatResponse{}, err
	}
	respBody, err := a.do(ctx, "/chat/completions", body, cred)
	if err != nil {
		return port.ChatResponse{}, err
	}
	return port.UnmarshalChatResponse(respBody)
}

// ChatStream implementa [Adapter]: POST com stream=true e leitura de SSE
// (linhas "data: {json}", terminadas por "data: [DONE]").
func (a *OpenAIHTTPAdapter) ChatStream(ctx context.Context, req port.ChatRequest, cred Credential) (port.ChatStream, error) {
	body, err := req.MarshalWire(true)
	if err != nil {
		return nil, err
	}
	httpReq, err := a.newRequest(ctx, "/chat/completions", body, cred)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "text/event-stream")
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("adapters: provider %s devolveu status %d: %s", a.provider, resp.StatusCode, sanitizeProviderBody(msg))
	}
	return newSSEStream(resp.Body), nil
}

// Embeddings implementa [Adapter]: POST {baseURL}/embeddings.
func (a *OpenAIHTTPAdapter) Embeddings(ctx context.Context, req port.EmbeddingsRequest, cred Credential) (port.EmbeddingsResponse, error) {
	body, err := json.Marshal(struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}{Model: req.Model, Input: req.Input})
	if err != nil {
		return port.EmbeddingsResponse{}, err
	}
	respBody, err := a.do(ctx, "/embeddings", body, cred)
	if err != nil {
		return port.EmbeddingsResponse{}, err
	}
	return port.UnmarshalEmbeddingsResponse(respBody)
}

// newRequest constrói o *http.Request com a credencial injectada no header
// Authorization: Bearer. O segredo é lido do Credential (não-exportado) e usado
// apenas para o header — nunca é logado.
func (a *OpenAIHTTPAdapter) newRequest(ctx context.Context, path string, body []byte, cred Credential) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// Fail-closed: uma credencial sem segredo NÃO produz um pedido não-autenticado
	// ao provedor (que seria um fail-open silencioso). Recusa-se antes de sair.
	s := cred.secretValue()
	if s == "" {
		return nil, ErrEmptyCredential
	}
	httpReq.Header.Set("Authorization", "Bearer "+s)
	return httpReq, nil
}

// do executa uma chamada síncrona e devolve o corpo da resposta.
func (a *OpenAIHTTPAdapter) do(ctx context.Context, path string, body []byte, cred Credential) ([]byte, error) {
	httpReq, err := a.newRequest(ctx, path, body, cred)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("adapters: provider %s devolveu status %d: %s", a.provider, resp.StatusCode, sanitizeProviderBody(respBody))
	}
	return respBody, nil
}

// sseStream é um [port.ChatStream] sobre um corpo SSE (text/event-stream) do wire
// OpenAI de streaming. Cada linha "data: {json}" é um chunk; "data: [DONE]"
// termina o stream.
type sseStream struct {
	body io.ReadCloser
	sc   *bufio.Scanner
	// sawTerminal fica true quando um fim LEGÍTIMO é observado — o sentinela
	// "[DONE]" ou um chunk com finish_reason não-vazio. Sem ele, um EOF do corpo
	// é uma truncagem, não um fim limpo.
	sawTerminal bool
	closed      bool
}

func newSSEStream(body io.ReadCloser) *sseStream {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	return &sseStream{body: body, sc: sc}
}

// wireStreamChunk é a forma de um chunk de streaming OpenAI (subconjunto).
type wireStreamChunk struct {
	Choices []struct {
		Delta struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *port.Usage `json:"usage"`
}

// Recv lê o próximo delta do SSE ou [io.EOF] no fim ("[DONE]" ou EOF do corpo).
func (s *sseStream) Recv() (port.ChatStreamDelta, error) {
	if s.closed {
		return port.ChatStreamDelta{}, io.EOF
	}
	for s.sc.Scan() {
		line := strings.TrimSpace(s.sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			s.sawTerminal = true
			return port.ChatStreamDelta{}, io.EOF
		}
		var chunk wireStreamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return port.ChatStreamDelta{}, err
		}
		delta := port.ChatStreamDelta{Usage: chunk.Usage}
		if len(chunk.Choices) > 0 {
			c := chunk.Choices[0]
			delta.Role = port.Role(c.Delta.Role)
			delta.Content = c.Delta.Content
			delta.FinishReason = c.FinishReason
			for _, tc := range c.Delta.ToolCalls {
				delta.ToolCalls = append(delta.ToolCalls, port.ToolCallDelta{
					Index:             tc.Index,
					ID:                tc.ID,
					Name:              tc.Function.Name,
					ArgumentsFragment: tc.Function.Arguments,
				})
			}
		}
		if delta.FinishReason != "" {
			s.sawTerminal = true
		}
		return delta, nil
	}
	if err := s.sc.Err(); err != nil {
		return port.ChatStreamDelta{}, err
	}
	// Scanner esgotou o corpo. Só é um fim LIMPO se um terminal legítimo foi
	// observado; caso contrário o stream foi truncado — fail-closed, nunca
	// forjamos uma conclusão limpa.
	if !s.sawTerminal {
		return port.ChatStreamDelta{}, ErrTruncatedStream
	}
	return port.ChatStreamDelta{}, io.EOF
}

// Close fecha o corpo subjacente (idempotente).
func (s *sseStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.body.Close()
}
