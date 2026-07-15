package adapters_test

import (
	"context"
	"testing"

	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/port"
)

// TestFakeAdapter_Provider_And_Defaults cobre o Provider e os caminhos default
// (modelo não programado) de chat/stream/embeddings do fake.
func TestFakeAdapter_Provider_And_Defaults(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake-prov")
	if f.Provider() != "fake-prov" {
		t.Errorf("Provider = %q", f.Provider())
	}
	cred := adapters.NewCredential("fake-prov", "eu", "s")

	// Modelo não programado: default determinista (eco vazio, finish stop).
	resp, err := f.Chat(context.Background(), port.ChatRequest{Model: "desconhecido"}, cred)
	if err != nil || resp.Model != "desconhecido" {
		t.Fatalf("chat default: resp=%+v err=%v", resp, err)
	}

	stream, err := f.ChatStream(context.Background(), port.ChatRequest{Model: "desconhecido"}, cred)
	if err != nil {
		t.Fatalf("stream default: %v", err)
	}
	if _, err := port.CollectStream(stream); err != nil {
		t.Fatalf("CollectStream default: %v", err)
	}

	f.SetEmbeddingsResponse("emb", port.EmbeddingsResponse{Data: []port.Embedding{{Index: 0, Embedding: []float64{1}}}})
	er, err := f.Embeddings(context.Background(), port.EmbeddingsRequest{Model: "emb", Input: []string{"x"}}, cred)
	if err != nil || len(er.Data) != 1 {
		t.Fatalf("embeddings: resp=%+v err=%v", er, err)
	}
	// Embeddings de modelo não programado: default determinista.
	er2, err := f.Embeddings(context.Background(), port.EmbeddingsRequest{Model: "nada", Input: []string{"x"}}, cred)
	if err != nil || er2.Model != "nada" {
		t.Fatalf("embeddings default: resp=%+v err=%v", er2, err)
	}
}

// TestOpenAIHTTPAdapter_Provider cobre o getter Provider do adaptador real.
func TestOpenAIHTTPAdapter_Provider(t *testing.T) {
	t.Parallel()
	a := adapters.NewOpenAIHTTPAdapter("openai", "https://internal.aos.local/v1", nil)
	if a.Provider() != "openai" {
		t.Errorf("Provider = %q", a.Provider())
	}
}

// TestOpenAIHTTPAdapter_ConnError cobre o caminho de erro de transporte (host
// inexistente) — fail-closed com erro atribuível, sem cair para outro provedor.
func TestOpenAIHTTPAdapter_ConnError(t *testing.T) {
	t.Parallel()
	// Porta reservada/dead: a ligação falha de imediato.
	a := adapters.NewOpenAIHTTPAdapter("openai", "http://127.0.0.1:1/v1", nil)
	_, err := a.Chat(context.Background(), port.ChatRequest{
		Model: "m", Messages: []port.Message{{Role: port.RoleUser, Content: "x"}},
	}, adapters.NewCredential("openai", "eu", "sk"))
	if err == nil {
		t.Fatal("erro de transporte esperado")
	}
	// ChatStream no mesmo host morto também falha.
	if _, err := a.ChatStream(context.Background(), port.ChatRequest{
		Model: "m", Messages: []port.Message{{Role: port.RoleUser, Content: "x"}},
	}, adapters.NewCredential("openai", "eu", "sk")); err == nil {
		t.Fatal("erro de transporte esperado no stream")
	}
}
