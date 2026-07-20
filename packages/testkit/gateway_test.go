package testkit_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	tk "github.com/aos-ref/testkit"
)

// TestFakeGateway_Chat: resposta canónica determinista + usage + registo do pedido.
func TestFakeGateway_Chat(t *testing.T) {
	t.Parallel()
	var gw tk.Gateway = tk.NewFakeGateway("ola mundo")
	resp, err := gw.Chat(context.Background(), tk.GWChatRequest{
		Model:    "modelo-x",
		Messages: []tk.GWMessage{{Role: tk.GWRoleUser, Content: "oi"}},
		RunID:    tk.FixtureRunID,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "ola mundo" || resp.Model != "modelo-x" {
		t.Fatalf("resposta inesperada: %+v", resp)
	}
	if resp.Usage.PromptTokens != 2 || resp.Usage.CompletionTokens != int64(len("ola mundo")) {
		t.Fatalf("usage inesperada: %+v", resp.Usage)
	}
	if chats := gw.(*tk.FakeGateway).Chats(); len(chats) != 1 || chats[0].RunID != tk.FixtureRunID {
		t.Fatalf("pedido nao registado: %+v", chats)
	}
}

// TestFakeGateway_SwapModelo: EffectiveModel simula um swap de modelo/provider.
func TestFakeGateway_SwapModelo(t *testing.T) {
	t.Parallel()
	gw := tk.NewFakeGateway("x")
	gw.EffectiveModel = "modelo-efectivo"
	resp, _ := gw.Chat(context.Background(), tk.GWChatRequest{Model: "modelo-pedido"})
	if resp.Model != "modelo-efectivo" {
		t.Fatalf("esperava swap para modelo-efectivo, obtive %q", resp.Model)
	}
}

// TestFakeGateway_ChatStream: os deltas reconstroem a resposta e terminam em EOF.
func TestFakeGateway_ChatStream(t *testing.T) {
	t.Parallel()
	gw := tk.NewFakeGateway("um dois tres")
	stream, err := gw.ChatStream(context.Background(), tk.GWChatRequest{Stream: true})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	var sb strings.Builder
	var finished bool
	for {
		d, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		sb.WriteString(d.Content)
		if d.FinishReason == "stop" {
			finished = true
		}
	}
	if !finished {
		t.Fatal("o stream devia terminar com FinishReason=stop")
	}
	if got := sb.String(); got != "um dois tres" {
		t.Fatalf("reconstrucao=%q, esperava 'um dois tres'", got)
	}
}

// TestFakeGateway_Embeddings: um vector determinista por input.
func TestFakeGateway_Embeddings(t *testing.T) {
	t.Parallel()
	gw := tk.NewFakeGateway("")
	resp, err := gw.Embeddings(context.Background(), tk.GWEmbeddingsRequest{
		Model: "emb", Input: []string{"aa", "bbbb"},
	})
	if err != nil {
		t.Fatalf("Embeddings: %v", err)
	}
	if len(resp.Vectors) != 2 || resp.Vectors[0][0] != 2 || resp.Vectors[1][0] != 4 {
		t.Fatalf("vectores inesperados: %+v", resp.Vectors)
	}
	if len(gw.Embeds()) != 1 {
		t.Fatalf("pedido de embeddings nao registado")
	}
}

// TestFakeGateway_ErroFailClosed: com Err, todos os métodos falham.
func TestFakeGateway_ErroFailClosed(t *testing.T) {
	t.Parallel()
	gw := tk.NewFakeGateway("x")
	gw.Err = errors.New("gw em baixo")
	if _, err := gw.Chat(context.Background(), tk.GWChatRequest{}); err == nil {
		t.Fatal("Chat devia falhar")
	}
	if _, err := gw.ChatStream(context.Background(), tk.GWChatRequest{}); err == nil {
		t.Fatal("ChatStream devia falhar")
	}
	if _, err := gw.Embeddings(context.Background(), tk.GWEmbeddingsRequest{}); err == nil {
		t.Fatal("Embeddings devia falhar")
	}
	if gw.PortVersion() != "1.0.0" {
		t.Fatalf("PortVersion inesperada: %q", gw.PortVersion())
	}
}
