package modelgateway

// AOS-520 (PR 0) — o que o adaptador DECLARA num run fixado, e quando. Testes internos: o gateway
// é um duplo que devolve a rota e o estado que o teste quer, e o nonce do envelope do estado é
// controlado pelo teste (correcções da revisão adversarial, A1 e A2).

import (
	"context"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/port"
)

// aos520GatewayFixo devolve sempre a mesma resposta, e conta os pedidos.
type aos520GatewayFixo struct {
	port.Gateway
	resp    port.ChatResponse
	pedidos int
}

func (g *aos520GatewayFixo) Chat(context.Context, port.ChatRequest) (port.ChatResponse, error) {
	g.pedidos++
	return g.resp, nil
}

func aos520RespostaFinal() port.ChatResponse {
	return port.ChatResponse{Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "ok"}, FinishReason: "stop"}}}
}

func aos520ConjuntoDoAlias(t *testing.T, alias string) (*RouteProfileSet, string) {
	t.Helper()
	perfil := RouteProfile{Requested: alias, ExpectedModel: "openai/modelo-do-run", WireClass: WireOpenAIChat, Capabilities: []string{CapabilityTools}}
	set, err := NewRouteProfileSet(perfil)
	if err != nil {
		t.Fatal(err)
	}
	return set, perfil.Digest()
}

// NUM TURNO QUE O GATEWAY COMPAROU, FICA O DIGEST DO GATEWAY (A1). Run fixado, governação ligada:
// o gateway declara o digest do perfil da rota que SERVIU — que pode ser outra, numa descida de
// tier. O manifesto do turno leva esse, com o resultado da comparação que lhe corresponde; o
// digest fixado não é escrito por cima. Escrevê-lo por cima deixava no registo um digest e um
// `route_check` de perfis diferentes.
func TestAOS520_Adaptador_NumTurnoComparadoFicaODigestDoGateway(t *testing.T) {
	const alias = "alias-do-run"
	set, fixado := aos520ConjuntoDoAlias(t, alias)
	daRotaQueServiu := "sha256:" + strings.Repeat("c", 64)
	if daRotaQueServiu == fixado {
		t.Fatal("os dois digests sao iguais: o teste nao distingue nada")
	}
	resp := aos520RespostaFinal()
	resp.Route = port.ServedRoute{Model: "openai/rota-de-baixo", Check: port.RouteCheckEqual, ProfileDigest: daRotaQueServiu}
	gw := &aos520GatewayFixo{resp: resp}
	out, err := NewModelClient(gw, alias, WithRouteProfileSet(set)).Call(context.Background(),
		agentruntime.PromptView{Materialized: []byte("ola"), RouteProfileDigest: fixado})
	if err != nil || gw.pedidos != 1 {
		t.Fatalf("o run fixado no perfil em vigor tinha de seguir: err=%v pedidos=%d", err, gw.pedidos)
	}
	if out.RouteProfileDigest != daRotaQueServiu || string(out.RouteCheck) != port.RouteCheckEqual || out.Model != "openai/rota-de-baixo" {
		t.Fatalf("turno comparado: digest=%q check=%q modelo=%q; quer o digest que o GATEWAY declarou (%s), e nao o fixado (%s) por cima",
			out.RouteProfileDigest, out.RouteCheck, out.Model, daRotaQueServiu, fixado)
	}
	// Controlo: sem comparação (o gateway não declara rota), o mesmo run declara o digest FIXADO.
	semRota := &aos520GatewayFixo{resp: aos520RespostaFinal()}
	out, err = NewModelClient(semRota, alias, WithRouteProfileSet(set)).Call(context.Background(),
		agentruntime.PromptView{Materialized: []byte("ola"), RouteProfileDigest: fixado})
	if err != nil || out.RouteProfileDigest != fixado || out.RouteCheck != "" {
		t.Fatalf("turno nao comparado de um run fixado: digest=%q check=%q err=%v; quer o fixado e nenhuma comparacao", out.RouteProfileDigest, out.RouteCheck, err)
	}
}
