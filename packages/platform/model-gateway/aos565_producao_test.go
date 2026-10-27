package modelgateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-565 — A CADEIA INTEIRA, COMPOSTA: o perfil assinado fixa o fornecedor, o corpo sai com a
// chave, a sonda liga-se SOZINHA (derivada do que foi composto) e o fornecedor a montante que o
// proxy declarar é comparado com a ordem.
//
// É este teste que impede o defeito do AOS-551 nesta peça — um leitor de uma forma que nada
// escreve: aqui o produtor é o perfil, e o consumidor é a governação da rota, no mesmo nó.

// aos565PerfilComFornecedor é o perfil candidato de `gpt-4o`, igual ao da tabela em código no
// que o AOS-505 compara, mais a fixação do fornecedor.
const aos565PerfilComFornecedor = `{"requested":"gpt-4o","expected_model":"openai/k3",` +
	`"wire_class":"openai-chat-completions","capabilities":["tools"],` +
	`"provider":{"order":["Google"],"data_collection":"deny"}}`

// aos565ProxyFixado é um proxy que declara o modelo servido nos cabeçalhos (como o LiteLLM) e o
// fornecedor a montante no corpo. `fornecedor` vazio ⇒ o corpo não leva a chave, que é o caso
// esperado em produção hoje.
func aos565ProxyFixado(t *testing.T, fornecedor string, corpoPedido *map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if corpoPedido != nil {
			cru, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(cru, corpoPedido)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(port.HeaderServedModel, "openai/k3")
		chave := ""
		if fornecedor != "" {
			chave = `"provider":` + aos565JSONString(fornecedor) + `,`
		}
		_, _ = io.WriteString(w, `{"id":"cmpl-1","object":"chat.completion","model":"gpt-4o",`+chave+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// aos565JSONString devolve o texto como literal JSON.
func aos565JSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func aos565Gateway(t *testing.T, srv *httptest.Server, perfis []string, modo string) *modelgateway.Gateway {
	t.Helper()
	cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(),
		[]modelgateway.InfraAccount{{KeyID: "acct-eu-1", Provider: "openai", Region: "eu", LimitRPM: 100, LimitTPM: 100000}})
	for _, p := range perfis {
		perfil, err := modelgateway.ParseRouteProfile([]byte(p))
		if err != nil {
			t.Fatalf("perfil: %v", err)
		}
		cfg.RouteProfiles = append(cfg.RouteProfiles, perfil)
	}
	cfg.Route = modelgateway.RouteGovernance{Mode: modo}
	gw, err := modelgateway.NewProduction(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewProduction: %v", err)
	}
	return gw
}

func aos565Pedir(gw *modelgateway.Gateway) (port.ChatResponse, error) {
	return gw.Chat(context.Background(), port.ChatRequest{
		Model: "gpt-4o", Principal: "tok", Board: "board-eu", Region: "eu",
		Messages: []port.Message{{Role: port.RoleUser, Content: "olá"}},
	})
}

// TestAOS565_Producao_ODaIdaSaiEODaVoltaEComparado.
func TestAOS565_Producao_ODaIdaSaiEODaVoltaEComparado(t *testing.T) {
	// (1) O fornecedor a montante é o da ordem: o turno passa, e o corpo do pedido levou a
	// fixação com `allow_fallbacks: false`.
	var pedido map[string]any
	gw := aos565Gateway(t, aos565ProxyFixado(t, "Google", &pedido), []string{aos565PerfilComFornecedor}, modelgateway.RouteGovernanceEnforce)
	if _, err := aos565Pedir(gw); err != nil {
		t.Fatalf("o fornecedor esta na ordem; veio %v", err)
	}
	prov, _ := pedido["provider"].(map[string]any)
	if prov == nil {
		t.Fatalf("o corpo do pedido tinha de levar a fixacao: %v", pedido)
	}
	if fb, _ := prov["allow_fallbacks"].(bool); fb {
		t.Fatalf("a ordem e uma FIXACAO: allow_fallbacks tem de ser false (%v)", prov)
	}
	if dc, _ := prov["data_collection"].(string); dc != port.DataCollectionDeny {
		t.Fatalf("retencao = %v", prov)
	}

	// (2) O proxy encaminhou outro fornecedor: variância de rota, com a causa nova, e em
	// imposição o turno falha. A sonda ligou-se sozinha — nenhum interruptor foi tocado.
	gw = aos565Gateway(t, aos565ProxyFixado(t, "OpenAI", nil), []string{aos565PerfilComFornecedor}, modelgateway.RouteGovernanceEnforce)
	_, err := aos565Pedir(gw)
	if !errors.Is(err, modelgateway.ErrRouteVariance) || !strings.Contains(err.Error(), modelgateway.RouteCauseUpstreamProviderOutOfOrder) {
		t.Fatalf("queria a variancia do fornecedor; veio %v", err)
	}

	// (3) O proxy NÃO declara o fornecedor — o caso esperado em produção hoje. O turno passa:
	// tratar a ausência como variância faria falhar todas as chamadas de uma rota fixada.
	gw = aos565Gateway(t, aos565ProxyFixado(t, "", nil), []string{aos565PerfilComFornecedor}, modelgateway.RouteGovernanceEnforce)
	if _, err := aos565Pedir(gw); err != nil {
		t.Fatalf("`nao reportado` nao e variancia; veio %v", err)
	}

	// (4) SEM perfil que fixe o fornecedor, o corpo é o de sempre: a chave não vai, e o que o
	// proxy reportar não é julgado.
	pedido = nil
	gw = aos565Gateway(t, aos565ProxyFixado(t, "OpenAI", &pedido), nil, modelgateway.RouteGovernanceEnforce)
	if _, err := aos565Pedir(gw); err != nil {
		t.Fatalf("sem fixacao nao ha nada que falhe; veio %v", err)
	}
	if _, tem := pedido["provider"]; tem {
		t.Fatalf("um pedido sem fixacao nao leva a chave: %v", pedido)
	}
}
