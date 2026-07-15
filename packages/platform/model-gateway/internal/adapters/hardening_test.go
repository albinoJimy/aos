package adapters_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/port"
)

// TestOpenAIHTTPAdapter_Stream_Truncado prova que um corpo SSE cortado a meio
// (sem "[DONE]" nem finish_reason terminal) é FAIL-CLOSED: Recv/CollectStream
// devolvem ErrTruncatedStream em vez de forjar uma conclusão limpa (finish=stop,
// argumentos de tool parciais).
func TestOpenAIHTTPAdapter_Stream_Truncado(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Tool call ABERTA e depois o corpo simplesmente termina — truncagem.
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"role":"assistant","content":"a chamar"}}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f","arguments":"{\"x\":"}}]}}]}`+"\n\n")
		// Sem [DONE], sem finish_reason: a ligação fecha aqui.
	}))
	defer srv.Close()

	a := adapters.NewOpenAIHTTPAdapter("openai", srv.URL, srv.Client())
	stream, err := a.ChatStream(context.Background(), port.ChatRequest{
		Model: "m", Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	}, adapters.NewCredential("openai", "eu", "sk"))
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	_, err = port.CollectStream(stream)
	if !errors.Is(err, adapters.ErrTruncatedStream) {
		t.Fatalf("stream truncado devia falhar ErrTruncatedStream, obtido %v", err)
	}
}

// TestOpenAIHTTPAdapter_Stream_DoneLimpo confirma que um stream terminado APENAS
// por finish_reason terminal (sem [DONE]) é aceite como fim limpo (sem truncagem).
func TestOpenAIHTTPAdapter_Stream_FinishReasonSemDone(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		// Sem [DONE]: mas o finish_reason terminal já marca fim legítimo.
	}))
	defer srv.Close()

	a := adapters.NewOpenAIHTTPAdapter("openai", srv.URL, srv.Client())
	stream, err := a.ChatStream(context.Background(), port.ChatRequest{
		Model: "m", Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	}, adapters.NewCredential("openai", "eu", "sk"))
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	resp, err := port.CollectStream(stream)
	if err != nil {
		t.Fatalf("CollectStream (fim limpo por finish_reason): %v", err)
	}
	if resp.Choices[0].Message.Content != "ok" || resp.Choices[0].FinishReason != "stop" {
		t.Errorf("resposta = %+v", resp)
	}
}

// TestOpenAIHTTPAdapter_SegredoVazio_FailClosed prova que uma credencial SEM
// segredo é recusada (ErrEmptyCredential) em vez de produzir um pedido
// não-autenticado ao provedor.
func TestOpenAIHTTPAdapter_SegredoVazio_FailClosed(t *testing.T) {
	t.Parallel()
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","object":"chat.completion","model":"m","choices":[]}`)
	}))
	defer srv.Close()

	a := adapters.NewOpenAIHTTPAdapter("openai", srv.URL, srv.Client())
	_, err := a.Chat(context.Background(), port.ChatRequest{
		Model: "m", Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	}, adapters.NewCredential("openai", "eu", "")) // segredo vazio
	if !errors.Is(err, adapters.ErrEmptyCredential) {
		t.Fatalf("segredo vazio devia falhar ErrEmptyCredential, obtido %v", err)
	}
	if hit {
		t.Error("adaptador enviou um pedido NAO-autenticado ao provedor (fail-open)")
	}
	// O mesmo no caminho de streaming.
	if _, err := a.ChatStream(context.Background(), port.ChatRequest{
		Model: "m", Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	}, adapters.NewCredential("openai", "eu", "")); !errors.Is(err, adapters.ErrEmptyCredential) {
		t.Fatalf("stream com segredo vazio devia falhar ErrEmptyCredential, obtido %v", err)
	}
}

// TestStaticCredentialSource_SegredoVazio_FailClosed: um segredo registado VAZIO é
// tratado como ausência de credencial (ErrNoCredential), nunca devolvido.
func TestStaticCredentialSource_SegredoVazio_FailClosed(t *testing.T) {
	t.Parallel()
	cs := adapters.NewStaticCredentialSource()
	cs.Set("openai", "eu", "") // registado mas vazio
	if _, err := cs.Fetch(context.Background(), "openai", "eu"); !errors.Is(err, adapters.ErrNoCredential) {
		t.Fatalf("segredo vazio devia falhar ErrNoCredential, obtido %v", err)
	}
}
