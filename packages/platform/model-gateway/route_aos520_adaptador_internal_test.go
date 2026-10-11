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

// O ENVELOPE DO ESTADO OPACO DE UM RUN FIXADO É O DE UM RUN NÃO FIXADO (A2). Governação da rota
// DESLIGADA, captura LIGADA: o gateway não comparou nada, e o envelope do estado fecha-se SEM
// digest de perfil — é por esse campo vazio que a devolução decide `estado_sem_rota`
// ([StateCauseNoRoute]): um estado de um turno que não provou a rota não volta ao provider. O
// digest fixado é declarado para o manifesto DEPOIS de o envelope fechar; declarado antes,
// entrava no envelope, mudava-lhe os bytes e o digest, e fazia passar por «da rota» um estado
// cuja rota ninguém comparou.
func TestAOS520_Adaptador_OEnvelopeDoEstadoNaoLevaODigestFixado(t *testing.T) {
	const alias = "alias-do-run"
	set, fixado := aos520ConjuntoDoAlias(t, alias)
	turno := func(naVista string) agentruntime.ModelResponse {
		t.Helper()
		resp := aos520RespostaFinal()
		resp.State = aos514RespostaComEstado().State
		a := NewModelClient(&aos520GatewayFixo{resp: resp}, alias, WithRouteProfileSet(set))
		// O nonce é fixo: com o mesmo conteúdo, o envelope é byte a byte o mesmo.
		a.estado = &capturaDoEstado{maxBytes: DefaultProviderStateMaxBytes, nonce: func(n []byte) error {
			for i := range n {
				n[i] = 7
			}
			return nil
		}}
		out, err := a.Call(context.Background(), agentruntime.PromptView{Materialized: []byte("ola"), RouteProfileDigest: naVista})
		if err != nil {
			t.Fatal(err)
		}
		if out.State == nil || out.State.Status != agentruntime.ProviderStateCaptured || len(out.State.Bytes) == 0 {
			t.Fatalf("o estado tinha de ficar capturado: %+v", out.State)
		}
		return out
	}
	naoFixado, comFixacao := turno(""), turno(fixado)
	if comFixacao.RouteProfileDigest != fixado || naoFixado.RouteProfileDigest != "" {
		t.Fatalf("digest declarado: fixado=%q nao fixado=%q", comFixacao.RouteProfileDigest, naoFixado.RouteProfileDigest)
	}
	if string(comFixacao.State.Bytes) != string(naoFixado.State.Bytes) {
		t.Fatalf("o envelope do estado mudou com a fixacao:\n fixado:     %s\n nao fixado: %s", comFixacao.State.Bytes, naoFixado.State.Bytes)
	}
	env, err := port.UnmarshalProviderStateEnvelope(comFixacao.State.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	// Sem digest e sem comparação no envelope: a devolução continua a recusá-lo por
	// `estado_sem_rota`, como recusava antes da fixação.
	if env.RouteProfileDigest != "" || env.RouteCheck != "" {
		t.Fatalf("o envelope de um turno NAO comparado ganhou rota: digest=%q check=%q", env.RouteProfileDigest, env.RouteCheck)
	}
	if StateCauseNoRoute != "estado_sem_rota" {
		t.Fatalf("a causa mudou de nome: %q", StateCauseNoRoute)
	}
}
