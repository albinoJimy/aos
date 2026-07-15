package port_test

import (
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/port"
)

// TestVersion_SemVer confirma que a versão da porta é SemVer VÁLIDO — MAJOR.MINOR.
// PATCH com componentes NUMÉRICOS (não apenas três campos separados por ponto). O
// contrato é versionado (critério AOS-055) à imagem do gate SemVer do registry
// (AOS-052); a validação é auto-contida (sem dependência externa, mantendo o
// build offline/zero-dep do módulo).
func TestVersion_SemVer(t *testing.T) {
	t.Parallel()
	parts := strings.Split(port.Version, ".")
	if len(parts) != 3 {
		t.Fatalf("Version %q nao e SemVer MAJOR.MINOR.PATCH", port.Version)
	}
	for i, p := range parts {
		if p == "" {
			t.Fatalf("Version %q: componente %d vazio", port.Version, i)
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			t.Fatalf("Version %q: componente %d=%q nao e inteiro nao-negativo", port.Version, i, p)
		}
		if len(p) > 1 && p[0] == '0' {
			t.Fatalf("Version %q: componente %d=%q tem zero a esquerda (nao-canonico)", port.Version, i, p)
		}
	}
}

// TestChatRequest_Normalize é table-driven sobre a normalização/validação do
// contrato compatível OpenAI.
func TestChatRequest_Normalize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      port.ChatRequest
		wantErr error
		check   func(t *testing.T, out port.ChatRequest)
	}{
		{
			name:    "sem modelo",
			in:      port.ChatRequest{Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}}},
			wantErr: port.ErrNoModel,
		},
		{
			name:    "sem mensagens",
			in:      port.ChatRequest{Model: "m"},
			wantErr: port.ErrNoMessages,
		},
		{
			name:    "papel invalido",
			in:      port.ChatRequest{Model: "m", Messages: []port.Message{{Role: "root", Content: "x"}}},
			wantErr: port.ErrBadRole,
		},
		{
			name: "preenche tool type default",
			in: port.ChatRequest{
				Model:    "m",
				Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
				Tools:    []port.Tool{{Function: port.FunctionDef{Name: "f"}}},
			},
			check: func(t *testing.T, out port.ChatRequest) {
				if out.Tools[0].Type != "function" {
					t.Errorf("Type default = %q, quer function", out.Tools[0].Type)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := tc.in.Normalize()
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, quer %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Normalize erro inesperado: %v", err)
			}
			if tc.check != nil {
				tc.check(t, out)
			}
		})
	}
}

// TestChatRequest_MarshalWire_NaoVazaMetadados garante que Principal/Region/Board
// (metadados de plataforma) NUNCA aparecem no wire enviado ao provider (ADR-006:
// sem segredos; e soberania não vaza).
func TestChatRequest_MarshalWire_NaoVazaMetadados(t *testing.T) {
	t.Parallel()
	req := port.ChatRequest{
		Model:     "claude-x",
		Messages:  []port.Message{{Role: port.RoleUser, Content: "oi"}},
		Principal: "token-SECRETO-do-principal",
		Region:    "eu-west",
		Board:     "board-1",
	}
	wire, err := req.MarshalWire(false)
	if err != nil {
		t.Fatalf("MarshalWire: %v", err)
	}
	s := string(wire)
	for _, forbidden := range []string{"token-SECRETO", "eu-west", "board-1", "Principal", "principal", "region", "board"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("wire contem metadado de plataforma %q: %s", forbidden, s)
		}
	}
	if !strings.Contains(s, "claude-x") {
		t.Errorf("wire nao contem o modelo: %s", s)
	}
}

// TestChatRequest_MarshalWire_Estavel confirma serialização determinista
// (byte-idêntica em repetição).
func TestChatRequest_MarshalWire_Estavel(t *testing.T) {
	t.Parallel()
	temp := 0.0
	req := port.ChatRequest{
		Model:       "m",
		Messages:    []port.Message{{Role: port.RoleSystem, Content: "sys"}, {Role: port.RoleUser, Content: "u"}},
		Temperature: &temp,
	}
	a, _ := req.MarshalWire(true)
	b, _ := req.MarshalWire(true)
	if string(a) != string(b) {
		t.Fatalf("serializacao nao determinista:\n%s\n%s", a, b)
	}
	// stream reflectido.
	if !strings.Contains(string(a), `"stream":true`) {
		t.Errorf("stream=true nao serializado: %s", a)
	}
}

// TestUnmarshalChatResponse confirma round-trip do wire OpenAI para a forma
// normalizada, incluindo tool_calls.
func TestUnmarshalChatResponse(t *testing.T) {
	t.Parallel()
	wire := `{
		"id":"cmpl-1","object":"chat.completion","model":"m","created":123,
		"choices":[{"index":0,"finish_reason":"tool_calls","message":{
			"role":"assistant","content":"",
			"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"lx\"}"}}]
		}}],
		"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
	}`
	resp, err := port.UnmarshalChatResponse([]byte(wire))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if resp.Model != "m" || resp.Usage.PromptTokens != 10 || resp.Usage.CompletionTokens != 5 {
		t.Errorf("campos base errados: %+v", resp)
	}
	if len(resp.Choices) != 1 || len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("tool_calls nao desserializadas: %+v", resp.Choices)
	}
	tc := resp.Choices[0].Message.ToolCalls[0]
	if tc.Function.Name != "get_weather" || !strings.Contains(tc.Function.Arguments, "lx") {
		t.Errorf("tool call errada: %+v", tc)
	}
}

// TestSliceStream_And_Collect testa o iterador de streaming e a reconstrução
// agregada (texto + tool calls por índice).
func TestSliceStream_And_Collect(t *testing.T) {
	t.Parallel()
	deltas := []port.ChatStreamDelta{
		{Role: port.RoleAssistant, Content: "Olá "},
		{Content: "mundo"},
		{ToolCalls: []port.ToolCallDelta{{Index: 0, ID: "c1", Name: "f", ArgumentsFragment: `{"a":`}}},
		{ToolCalls: []port.ToolCallDelta{{Index: 0, ArgumentsFragment: `1}`}}},
		{FinishReason: "tool_calls", Usage: &port.Usage{PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7}},
	}
	s := port.NewSliceStream(deltas)
	resp, err := port.CollectStream(s)
	if err != nil {
		t.Fatalf("CollectStream: %v", err)
	}
	msg := resp.Choices[0].Message
	if msg.Content != "Olá mundo" {
		t.Errorf("texto agregado = %q", msg.Content)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Arguments != `{"a":1}` {
		t.Errorf("tool call agregada errada: %+v", msg.ToolCalls)
	}
	if resp.Choices[0].FinishReason != "tool_calls" || resp.Usage.TotalTokens != 7 {
		t.Errorf("finish/usage errados: %+v", resp)
	}
	// Após Close, Recv devolve EOF.
	_ = s.Close()
	if _, err := s.Recv(); err != io.EOF {
		t.Errorf("Recv apos Close = %v, quer io.EOF", err)
	}
}

// TestEmbeddingsRequest_Normalize cobre a validação de embeddings.
func TestEmbeddingsRequest_Normalize(t *testing.T) {
	t.Parallel()
	if _, err := (port.EmbeddingsRequest{Input: []string{"x"}}).Normalize(); !errors.Is(err, port.ErrNoModel) {
		t.Errorf("sem modelo devia falhar ErrNoModel")
	}
	if _, err := (port.EmbeddingsRequest{Model: "m"}).Normalize(); !errors.Is(err, port.ErrNoInput) {
		t.Errorf("sem input devia falhar ErrNoInput")
	}
	if _, err := (port.EmbeddingsRequest{Model: "m", Input: []string{"x"}}).Normalize(); err != nil {
		t.Errorf("valido nao devia falhar: %v", err)
	}
}

// TestMessage_JSONTags confirma que os campos wire usam snake_case OpenAI.
func TestMessage_JSONTags(t *testing.T) {
	t.Parallel()
	m := port.Message{Role: port.RoleTool, Content: "r", ToolCallID: "c1"}
	b, _ := json.Marshal(m)
	if !strings.Contains(string(b), `"tool_call_id":"c1"`) {
		t.Errorf("tag snake_case em falta: %s", b)
	}
}
