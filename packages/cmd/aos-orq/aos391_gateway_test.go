package main

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/platform/model-gateway/port"
)

// fakeGateway implementa port.Gateway sem rede: captura o pedido e devolve uma resposta
// canned. Prova o adaptador gatewayDecomposeModel OFFLINE (o caminho vivo é env-gated).
type fakeGateway struct {
	resposta   string
	semEscolha bool
	ultimoReq  port.ChatRequest
}

func (f *fakeGateway) PortVersion() string { return "test" }

func (f *fakeGateway) Chat(_ context.Context, req port.ChatRequest) (port.ChatResponse, error) {
	f.ultimoReq = req
	if f.semEscolha {
		return port.ChatResponse{}, nil // zero escolhas
	}
	return port.ChatResponse{Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: f.resposta}}}}, nil
}

func (f *fakeGateway) ChatStream(context.Context, port.ChatRequest) (port.ChatStream, error) {
	return nil, errors.New("não usado")
}

func (f *fakeGateway) Embeddings(context.Context, port.EmbeddingsRequest) (port.EmbeddingsResponse, error) {
	return port.EmbeddingsResponse{}, errors.New("não usado")
}

// TestAOS391_GatewayDecomposeModel_PassaSystemEUser prova que o adaptador entrega o prompt
// como DUAS mensagens (system + user) — não colapsadas numa só, como o ModelClientAdapter
// faria — com o Principal (token que sela model:invoke) e o modelo, e devolve o conteúdo.
func TestAOS391_GatewayDecomposeModel_PassaSystemEUser(t *testing.T) {
	fake := &fakeGateway{resposta: `{"plan_version":"1.0.0","nodes":[]}`}
	m := gatewayDecomposeModel{gw: fake, model: "gpt-test", region: "eu", board: "board-eu", principal: "tok.run.compact"}

	out, err := m.Complete(context.Background(), "SYSTEM-PROMPT", "USER-GOAL")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if out != fake.resposta {
		t.Fatalf("conteúdo = %q, quer %q", out, fake.resposta)
	}
	if len(fake.ultimoReq.Messages) != 2 {
		t.Fatalf("mensagens = %d, quer 2 (system + user, não colapsadas)", len(fake.ultimoReq.Messages))
	}
	if fake.ultimoReq.Messages[0].Role != port.RoleSystem || fake.ultimoReq.Messages[0].Content != "SYSTEM-PROMPT" {
		t.Errorf("mensagem[0] = %+v, quer {system, SYSTEM-PROMPT}", fake.ultimoReq.Messages[0])
	}
	if fake.ultimoReq.Messages[1].Role != port.RoleUser || fake.ultimoReq.Messages[1].Content != "USER-GOAL" {
		t.Errorf("mensagem[1] = %+v, quer {user, USER-GOAL}", fake.ultimoReq.Messages[1])
	}
	if fake.ultimoReq.Principal != "tok.run.compact" {
		t.Errorf("Principal = %q, quer o token do run (sela model:invoke)", fake.ultimoReq.Principal)
	}
	if fake.ultimoReq.Model != "gpt-test" {
		t.Errorf("Model = %q, quer gpt-test", fake.ultimoReq.Model)
	}
}

// TestAOS391_GatewayDecomposeModel_SemEscolhasFailClosed — um gateway que responde sem
// escolhas é erro (fail-closed): o planeador não avança com um plano fantasma.
func TestAOS391_GatewayDecomposeModel_SemEscolhasFailClosed(t *testing.T) {
	m := gatewayDecomposeModel{gw: &fakeGateway{semEscolha: true}, model: "gpt-test"}
	if _, err := m.Complete(context.Background(), "s", "u"); err == nil {
		t.Fatal("resposta sem escolhas foi aceite — devia falhar fail-closed")
	}
}

// TestAOS391_GatewayConfigFromEnv cobre o parsing fail-closed da config do gateway.
func TestAOS391_GatewayConfigFromEnv(t *testing.T) {
	// Sem endpoint ⇒ nil, nil (não há gateway; o --goal sem fixture recusa a montante).
	t.Setenv("AOS_MODEL_ENDPOINT", "")
	if cfg, err := gatewayConfigFromEnv(); err != nil || cfg != nil {
		t.Fatalf("sem endpoint: cfg=%v err=%v, quer nil,nil", cfg, err)
	}

	// Endpoint sem modelo ⇒ erro (par obrigatório).
	t.Setenv("AOS_MODEL_ENDPOINT", "https://api.example.com")
	t.Setenv("AOS_MODEL_NAME", "")
	if _, err := gatewayConfigFromEnv(); err == nil {
		t.Fatal("endpoint sem AOS_MODEL_NAME foi aceite — devia falhar")
	}

	// Endpoint + modelo ⇒ cfg com defaults de região/board.
	t.Setenv("AOS_MODEL_NAME", "gpt-4o")
	cfg, err := gatewayConfigFromEnv()
	if err != nil || cfg == nil {
		t.Fatalf("endpoint+modelo: cfg=%v err=%v, quer cfg não-nil", cfg, err)
	}
	if cfg.model != "gpt-4o" || cfg.region != "eu" || cfg.board != "board-eu" {
		t.Errorf("cfg = %+v, quer model=gpt-4o region=eu board=board-eu (defaults)", cfg)
	}
}
