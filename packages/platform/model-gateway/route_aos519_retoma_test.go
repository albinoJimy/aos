package modelgateway_test

// AOS-519 — LIMITE CONHECIDO, FIXADO POR TESTE: um run retomado num nó que arrancou com OUTRO
// perfil para o mesmo alias CONTINUA, com o perfil novo, e nada o detecta.
//
// Por run só fica fixada a VERSÃO DA PROJECÇÃO. O digest do perfil não fica: vai no manifesto de
// cada turno, e ninguém o compara com o do turno anterior. A causa `estado_de_outra_rota` só nasce
// na devolução do estado opaco, e por registo assinado só entram perfis que não devolvem estado
// (D8) — logo para estes perfis não há guarda nenhuma. O turno seguinte de um run começado com o
// perfil A vai para o provider com o modelo esperado e os parâmetros do perfil B.
//
// A regra é OPERACIONAL: drena-se a rota antes de recriar o nó com outro perfil (runbook).
//
// QUEM MUDA ISTO É O AOS-520 («modelo fixado por run»): o digest do perfil passa a ficar fixado
// no run e uma retoma com outro digest falha fechado, com causa própria. Quando isso entrar, ESTE
// TESTE TEM DE MUDAR — de propósito: é ele que obriga a que a mudança seja consciente.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
)

// aos519ProxyDaRetoma responde 200 e declara ter servido o modelo dado; grava os corpos.
type aos519ProxyDaRetoma struct {
	mu      sync.Mutex
	servido string
	corpos  [][]byte
}

func (p *aos519ProxyDaRetoma) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var corpo bytes.Buffer
	_, _ = corpo.ReadFrom(r.Body)
	p.mu.Lock()
	p.corpos = append(p.corpos, corpo.Bytes())
	servido := p.servido
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-litellm-model-name", servido)
	_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
}

func TestAOS519_LimiteConhecido_RunRetomadoComOutroPerfilContinuaEmSilencio(t *testing.T) {
	const alias = "gpt-4o"
	perfilA := aos513Perfil(t, `{"requested":"gpt-4o","expected_model":"openai/modelo-a","wire_class":"openai-chat-completions","capabilities":["tools"],"params":{"max_tokens":1000}}`)
	perfilB := aos513Perfil(t, `{"requested":"gpt-4o","expected_model":"openai/modelo-b","wire_class":"openai-chat-completions","capabilities":["tools"],"params":{"max_tokens":2000}}`)
	if perfilA.Digest() == perfilB.Digest() {
		t.Fatal("os dois perfis tem o mesmo digest")
	}
	// Um «arranque do nó»: um gateway composto com UM perfil para o alias, em observação.
	arrancar := func(perfil modelgateway.RouteProfile) (agentruntime.ModelClient, *aos519ProxyDaRetoma) {
		proxy := &aos519ProxyDaRetoma{servido: perfil.ExpectedModel}
		srv := httptest.NewServer(proxy)
		t.Cleanup(srv.Close)
		cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(), []modelgateway.InfraAccount{{KeyID: "acct-eu-1", Provider: "openai", Region: "eu"}})
		cfg.RouteProfiles = []modelgateway.RouteProfile{perfil}
		cfg.Route = modelgateway.RouteGovernance{Mode: modelgateway.RouteGovernanceObserve}
		gw, err := modelgateway.NewProduction(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		set, err := modelgateway.NewRouteProfileSet(perfil)
		if err != nil {
			t.Fatal(err)
		}
		return modelgateway.NewModelClient(gw, alias, modelgateway.WithPrincipal("tok"), modelgateway.WithRegionBoard("eu", "board-eu"),
			modelgateway.WithProjection(modelgateway.ProjectionNative), modelgateway.WithRouteProfileSet(set)), proxy
	}
	// O MESMO run nos dois arranques: o contexto leva o run e o passo, como o runtime os escreve.
	turno := func(cliente agentruntime.ModelClient, passo string) agentruntime.ModelResponse {
		ctx := agentruntime.ContextWithModelCall(context.Background(), "run-519-retomado", passo)
		// O que o run traz FIXADO de um arranque para o outro é só a versão da projecção.
		out, err := cliente.Call(ctx, agentruntime.PromptView{Materialized: []byte("ola"), ProjectionVersion: modelgateway.NativeProjectionVersion})
		if err != nil {
			t.Fatalf("passo %s: o turno FALHOU (%v) — se foi a fixacao do perfil por run a entrar (AOS-520), este teste tem de mudar", passo, err)
		}
		return out
	}

	noA, proxyA := arrancar(perfilA)
	primeiro := turno(noA, "step-000001")
	if primeiro.RouteProfileDigest != perfilA.Digest() || string(primeiro.RouteCheck) != port.RouteCheckEqual {
		t.Fatalf("turno 1: digest %s check %s; quer o do perfil A e igual", primeiro.RouteProfileDigest, primeiro.RouteCheck)
	}

	// O nó é recriado com o perfil B para o MESMO alias, e o run é retomado.
	noB, proxyB := arrancar(perfilB)
	segundo := turno(noB, "step-000002")

	// O COMPORTAMENTO DE HOJE: o turno segue; o manifesto leva o digest de B; a comparação dá
	// `igual` contra o modelo de B; e o pedido levou os parâmetros de B.
	if segundo.RouteProfileDigest != perfilB.Digest() || string(segundo.RouteCheck) != port.RouteCheckEqual || segundo.Model != perfilB.ExpectedModel {
		t.Fatalf("turno 2: digest %s check %s modelo %s; hoje o run retomado segue com o perfil B (%s)", segundo.RouteProfileDigest, segundo.RouteCheck, segundo.Model, perfilB.Digest())
	}
	if len(proxyA.corpos) != 1 || len(proxyB.corpos) != 1 || !bytes.Contains(proxyA.corpos[0], []byte(`"max_tokens":1000`)) || !bytes.Contains(proxyB.corpos[0], []byte(`"max_tokens":2000`)) {
		t.Fatalf("os pedidos do run levaram os parametros de cada perfil?\n A: %s\n B: %s", proxyA.corpos, proxyB.corpos)
	}
	// A ÚNICA pista é esta: os digests dos dois turnos do mesmo run são diferentes.
	if primeiro.RouteProfileDigest == segundo.RouteProfileDigest {
		t.Fatal("os dois turnos do run tem o mesmo digest de perfil: o teste nao mostra a troca")
	}
}
