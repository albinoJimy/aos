package modelgateway_test

// AOS-505 — A ROTA SOB GOVERNAÇÃO, medida na composição de produção do gateway (NewProduction)
// contra um proxy em httptest que emite os MESMOS cabeçalhos que a imagem de produção do LiteLLM
// emite (medidos em 2026-10-06 com a 1.96.2: nome do modelo, api_base e o identificador do
// deployment). É o equivalente offline do guião com o proxy real
// (deploy/server/litellm/ensaio-rota/).

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
	"github.com/aos-ref/substrate/eventstore"
)

// O que o proxy de ensaio emite. O identificador do deployment e o api_base levam MARCAS que não
// podem aparecer em lado nenhum do que o gateway devolve, sela ou reporta.
const (
	aos505MarcaDoID      = "49a2733634d98e6913cebc7bba9c1d21e5377b2383bdd941f545526c168b5dbe"
	aos505MarcaDoCaminho = "caminho-marcado-505"
	aos505MarcaDaQuery   = "chave-marcada-505"
	aos505MarcaDoUser    = "utilizador-marcado-505"
	aos505APIBaseA       = "https://" + aos505MarcaDoUser + ":segredo@api.a.exemplo.test:8443/" + aos505MarcaDoCaminho + "/v1?k=" + aos505MarcaDaQuery
	aos505HostA          = "api.a.exemplo.test:8443"
	aos505APIBaseB       = "https://api.b.exemplo.test/" + aos505MarcaDoCaminho + "/v1"
)

// aos505Proxy é o proxy de ensaio: grava o corpo de cada pedido e responde o wire OpenAI com o
// `model` do corpo CARIMBADO com o nome pedido (o que o proxy real faz) e os cabeçalhos dados.
type aos505Proxy struct {
	mu      sync.Mutex
	corpos  [][]byte
	headers map[string]string
}

func (p *aos505Proxy) definir(h map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.headers = h
}

func (p *aos505Proxy) pedidos() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]byte(nil), p.corpos...)
}

func (p *aos505Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	corpo, _ := io.ReadAll(r.Body)
	var pedido struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(corpo, &pedido)
	p.mu.Lock()
	p.corpos = append(p.corpos, corpo)
	h := p.headers
	p.mu.Unlock()
	for k, v := range h {
		w.Header().Set(k, v)
	}
	w.Header().Set("Content-Type", "application/json")
	modelo, _ := json.Marshal(pedido.Model)
	_, _ = w.Write([]byte(`{"id":"cmpl-505","object":"chat.completion","model":` + string(modelo) + `,` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
}

// aos505Cabecalhos são os cabeçalhos do proxy para um modelo e um api_base.
func aos505Cabecalhos(modelo, apiBase string) map[string]string {
	h := map[string]string{"x-litellm-model-id": aos505MarcaDoID, "x-litellm-model-group": "gpt-4o"}
	if modelo != "" {
		h["x-litellm-model-name"] = modelo
	}
	if apiBase != "" {
		h["x-litellm-model-api-base"] = apiBase
	}
	return h
}

// aos505Observado junta o que o gateway reportou por fora da resposta.
type aos505Observado struct {
	mu         sync.Mutex
	obs        []modelgateway.RouteObservation
	variancias []modelgateway.VarianceEvent
}

func (o *aos505Observado) observar(x modelgateway.RouteObservation) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.obs = append(o.obs, x)
}

func (o *aos505Observado) Emit(_ context.Context, ev modelgateway.VarianceEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.variancias = append(o.variancias, ev)
}

type aos505Montagem struct {
	gw    *modelgateway.Gateway
	proxy *aos505Proxy
	store *audit.MemStore
	visto *aos505Observado
}

// aos505Compor monta o gateway de produção à frente do proxy de ensaio, com a governação dada.
func aos505Compor(t *testing.T, modo, hostEsperado string, cabecalhos map[string]string) aos505Montagem {
	t.Helper()
	proxy := &aos505Proxy{headers: cabecalhos}
	srv := httptest.NewServer(proxy)
	t.Cleanup(srv.Close)
	store := audit.NewMemStore()
	visto := &aos505Observado{}
	cfg := prodConfig(store, srv.URL, srv.Client(),
		[]modelgateway.InfraAccount{{KeyID: "acct-eu-1", Provider: "openai", Region: "eu"}})
	cfg.Variance = visto
	cfg.Route = modelgateway.RouteGovernance{Mode: modo, ExpectedAPIHost: hostEsperado, Observer: visto.observar}
	gw, err := modelgateway.NewProduction(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewProduction(%q): %v", modo, err)
	}
	return aos505Montagem{gw: gw, proxy: proxy, store: store, visto: visto}
}

func aos505Pedido(modelo string) port.ChatRequest {
	return port.ChatRequest{
		Model: modelo, Principal: "tok", Board: "board-eu", Region: "eu",
		RunID: "run-505", StepID: "step-000002",
		Messages: []port.Message{{Role: port.RoleUser, Content: "olá"}},
	}
}

// aos505Selos devolve os selos de variância de rota da partição do board, pela ordem.
func aos505Selos(t *testing.T, store *audit.MemStore) []audit.AuditRecord {
	t.Helper()
	ctx := context.Background()
	const particao = "modelgw-gov:board-eu"
	head, err := store.Head(ctx, particao)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	var out []audit.AuditRecord
	for i := uint64(1); i <= head; i++ {
		rec, ok, err := store.At(ctx, particao, i)
		if err != nil || !ok {
			t.Fatalf("At(%d): ok=%v err=%v", i, ok, err)
		}
		for _, o := range rec.Obligations {
			if o.Params["variance"] == "served_route" {
				out = append(out, rec)
			}
		}
	}
	return out
}

// aos505TotalDeRegistos conta todos os registos do audit, em todas as partições conhecidas.
func aos505TotalDeRegistos(t *testing.T, store *audit.MemStore) uint64 {
	t.Helper()
	var total uint64
	for _, p := range []string{"modelgw-gov:board-eu", "modelgw-gov:allowlist-changelog"} {
		head, err := store.Head(context.Background(), p)
		if err != nil {
			t.Fatalf("Head(%s): %v", p, err)
		}
		total += head
	}
	return total
}

// aos505SemMarcas falha se `texto` tiver qualquer das marcas que não podem sair do adaptador: o
// identificador do deployment (derivado de segredo), o caminho, a query e as credenciais do
// api_base.
func aos505SemMarcas(t *testing.T, onde, texto string) {
	t.Helper()
	for _, marca := range []string{aos505MarcaDoID, aos505MarcaDoID[:16], aos505MarcaDoCaminho, aos505MarcaDaQuery, aos505MarcaDoUser, "segredo@"} {
		if strings.Contains(texto, marca) {
			t.Errorf("%s contem %q — o identificador do deployment e o caminho/credenciais do api_base nao podem sair do adaptador:\n%s", onde, marca, texto)
		}
	}
}

func aos505JSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	return string(raw)
}

// DESLIGADA, NADA SAI DO GATEWAY. Com o modo ausente e com `off`, e o proxy a emitir todos os
// cabeçalhos: a resposta é a de sempre (o `model` do corpo, a rota a zero), o turno do runtime é
// o de sempre, o audit tem os mesmos registos que sem cabeçalhos, e nem o observador nem o sink
// de variância são chamados.
func TestAOS505_Off_NadaSaiDoGateway(t *testing.T) {
	semCabecalhos := aos505Compor(t, "", "", nil)
	if _, err := semCabecalhos.gw.Chat(context.Background(), aos505Pedido("gpt-4o")); err != nil {
		t.Fatalf("Chat de referencia: %v", err)
	}
	referencia := aos505TotalDeRegistos(t, semCabecalhos.store)

	for _, modo := range []string{"", modelgateway.RouteGovernanceOff} {
		m := aos505Compor(t, modo, aos505HostA, aos505Cabecalhos("openai/outro-modelo", aos505APIBaseB))
		resp, err := m.gw.Chat(context.Background(), aos505Pedido("gpt-4o"))
		if err != nil {
			t.Fatalf("modo %q: Chat: %v", modo, err)
		}
		if resp.Route != (port.ServedRoute{}) {
			t.Errorf("modo %q: a rota saiu do gateway com a governacao desligada: %+v", modo, resp.Route)
		}
		if resp.Model != "gpt-4o" {
			t.Errorf("modo %q: resp.Model = %q, quero o `model` do corpo como sempre", modo, resp.Model)
		}
		if got := aos505TotalDeRegistos(t, m.store); got != referencia {
			t.Errorf("modo %q: o audit tem %d registos, quero os %d de um gateway sem cabecalhos", modo, got, referencia)
		}
		if len(m.visto.obs) != 0 || len(m.visto.variancias) != 0 {
			t.Errorf("modo %q: observador=%d variancias=%d, quero zero", modo, len(m.visto.obs), len(m.visto.variancias))
		}
		// O turno, como o runtime o recebe.
		out, err := modelgateway.NewModelClient(m.gw, "gpt-4o", modelgateway.WithPrincipal("tok"), modelgateway.WithRegionBoard("eu", "board-eu")).
			Call(context.Background(), agentruntime.PromptView{Materialized: []byte("olá")})
		if err != nil {
			t.Fatalf("modo %q: Call: %v", modo, err)
		}
		if out.Model != "gpt-4o" || out.RouteCheck != "" || out.RouteProfileDigest != "" {
			t.Errorf("modo %q: turno = modelo %q check %q digest %q; quero %q e os dois vazios", modo, out.Model, out.RouteCheck, out.RouteProfileDigest, "gpt-4o")
		}
	}
}

// EM OBSERVAÇÃO: cada caso da comparação, com o turno a seguir sempre.
func TestAOS505_Observe_ComparaEContaSemMudarOTurno(t *testing.T) {
	perfil, _ := modelgateway.RouteProfileFor("gpt-4o")
	for _, c := range []struct {
		nome                    string
		hostEsperado            string
		cabecalhos              map[string]string
		check, causa            string
		servido, rotulo         string
		querSelo                bool
		esperadoNoSeloEServidoS [2]string
	}{
		{"igual, sem host esperado", "", aos505Cabecalhos("openai/k3", aos505APIBaseA), port.RouteCheckEqual, "", "openai/k3", "openai/k3", false, [2]string{}},
		{"igual, com host esperado", aos505HostA, aos505Cabecalhos("openai/k3", aos505APIBaseA), port.RouteCheckEqual, "", "openai/k3", "openai/k3", false, [2]string{}},
		{"modelo de outro perfil", "", aos505Cabecalhos("openai/kimi-for-coding", aos505APIBaseA), port.RouteCheckDifferent, modelgateway.RouteCauseModelDifferent, "openai/kimi-for-coding", "openai/kimi-for-coding", true, [2]string{"openai/k3", "openai/kimi-for-coding"}},
		{"modelo fora de todos os perfis", "", aos505Cabecalhos("openai/nome-inventado-por-quem-trocou", aos505APIBaseA), port.RouteCheckDifferent, modelgateway.RouteCauseModelDifferent, "openai/nome-inventado-por-quem-trocou", modelgateway.ServedLabelOther, true, [2]string{"openai/k3", "openai/nome-inventado-por-quem-trocou"}},
		{"modelo nao reportado", "", aos505Cabecalhos("", aos505APIBaseA), port.RouteCheckUnreported, modelgateway.RouteCauseModelUnreported, "", modelgateway.ServedLabelUnreported, true, [2]string{"openai/k3", ""}},
		{"nenhum cabecalho", aos505HostA, nil, port.RouteCheckUnreported, modelgateway.RouteCauseModelUnreported, "", modelgateway.ServedLabelUnreported, true, [2]string{"openai/k3", ""}},
		{"endpoint diferente", aos505HostA, aos505Cabecalhos("openai/k3", aos505APIBaseB), port.RouteCheckDifferent, modelgateway.RouteCauseEndpointDifferent, "openai/k3", "openai/k3", true, [2]string{"openai/k3", "openai/k3"}},
		{"endpoint nao reportado", aos505HostA, aos505Cabecalhos("openai/k3", ""), port.RouteCheckUnreported, modelgateway.RouteCauseEndpointUnreported, "openai/k3", "openai/k3", true, [2]string{"openai/k3", "openai/k3"}},
	} {
		t.Run(c.nome, func(t *testing.T) {
			m := aos505Compor(t, modelgateway.RouteGovernanceObserve, c.hostEsperado, c.cabecalhos)
			resp, err := m.gw.Chat(context.Background(), aos505Pedido("gpt-4o"))
			if err != nil {
				t.Fatalf("em observacao o turno segue sempre; veio %v", err)
			}
			quer := port.ServedRoute{Model: c.servido, Check: c.check, ProfileDigest: perfil.Digest()}
			if resp.Route != quer {
				t.Fatalf("rota = %+v\n quero %+v", resp.Route, quer)
			}
			if resp.Route.APIHost != "" {
				t.Fatalf("o host do endpoint saiu do gateway: %q", resp.Route.APIHost)
			}
			if len(m.visto.obs) != 1 || m.visto.obs[0] != (modelgateway.RouteObservation{Check: c.check, Served: c.rotulo, Cause: c.causa}) {
				t.Fatalf("observador = %+v; quero uma observacao {%s %s %s}", m.visto.obs, c.check, c.rotulo, c.causa)
			}
			selos := aos505Selos(t, m.store)
			if !c.querSelo {
				if len(selos) != 0 || len(m.visto.variancias) != 0 {
					t.Fatalf("uma rota igual nao sela nem emite variancia: selos=%d variancias=%d", len(selos), len(m.visto.variancias))
				}
			} else {
				if len(selos) != 1 || len(m.visto.variancias) != 1 {
					t.Fatalf("queria 1 selo e 1 variancia, vieram %d e %d", len(selos), len(m.visto.variancias))
				}
				s := selos[0]
				p := s.Obligations[0].Params
				if s.Decision != audit.DecisionAllow || p["reason"] != c.causa || p["expected_model"] != c.esperadoNoSeloEServidoS[0] || p["served_model"] != c.esperadoNoSeloEServidoS[1] {
					t.Errorf("selo = decisao %s params %v; quero allow, causa %s, esperado %q, servido %q", s.Decision, p, c.causa, c.esperadoNoSeloEServidoS[0], c.esperadoNoSeloEServidoS[1])
				}
				if s.RunID != "run-505" || s.StepID != "step-000002" || s.Resource.Value != "gpt-4o" || s.Principal.NHIID == "" {
					t.Errorf("o selo nao e atribuivel: run %q passo %q modelo %q principal %q", s.RunID, s.StepID, s.Resource.Value, s.Principal.NHIID)
				}
				v := m.visto.variancias[0]
				if v.Kind != "served_route" || v.Reason != c.causa || v.ExpectedModel != "openai/k3" || v.ServedModel != c.servido {
					t.Errorf("variancia = %+v", v)
				}
				aos505SemMarcas(t, "o selo", aos505JSON(t, s))
				aos505SemMarcas(t, "o evento de variancia", aos505JSON(t, v))
			}
			aos505SemMarcas(t, "a resposta", aos505JSON(t, resp)+aos505JSON(t, resp.Route))
			aos505SemMarcas(t, "a observacao", aos505JSON(t, m.visto.obs))

			// O turno, como o runtime o recebe: o modelo servido é o que o proxy declarou, e
			// fica VAZIO quando ele não o declarou — nunca o nome pedido, nunca o do corpo.
			out, err := modelgateway.NewModelClient(m.gw, "gpt-4o", modelgateway.WithPrincipal("tok"), modelgateway.WithRegionBoard("eu", "board-eu")).
				Call(context.Background(), agentruntime.PromptView{Materialized: []byte("olá")})
			if err != nil {
				t.Fatalf("Call: %v", err)
			}
			if out.Model != c.servido || string(out.RouteCheck) != c.check || out.RouteProfileDigest != perfil.Digest() {
				t.Errorf("turno = modelo %q check %q digest %q; quero %q %q %q", out.Model, out.RouteCheck, out.RouteProfileDigest, c.servido, c.check, perfil.Digest())
			}
			if c.servido == "" && out.Model != "" {
				t.Errorf("modelo nao reportado foi preenchido com %q", out.Model)
			}
		})
	}
}

// EM IMPOSIÇÃO: o que não se prova igual FALHA, com causa em vocabulário fechado; o que é igual
// passa.
func TestAOS505_Enforce_FalhaComCausa(t *testing.T) {
	for _, c := range []struct {
		nome         string
		hostEsperado string
		cabecalhos   map[string]string
		check, causa string
	}{
		{"modelo diferente", "", aos505Cabecalhos("openai/outro", aos505APIBaseA), port.RouteCheckDifferent, modelgateway.RouteCauseModelDifferent},
		{"modelo nao reportado", "", nil, port.RouteCheckUnreported, modelgateway.RouteCauseModelUnreported},
		{"endpoint diferente", aos505HostA, aos505Cabecalhos("openai/k3", aos505APIBaseB), port.RouteCheckDifferent, modelgateway.RouteCauseEndpointDifferent},
		{"endpoint nao reportado", aos505HostA, aos505Cabecalhos("openai/k3", ""), port.RouteCheckUnreported, modelgateway.RouteCauseEndpointUnreported},
	} {
		t.Run(c.nome, func(t *testing.T) {
			m := aos505Compor(t, modelgateway.RouteGovernanceEnforce, c.hostEsperado, c.cabecalhos)
			_, err := m.gw.Chat(context.Background(), aos505Pedido("gpt-4o"))
			var rerr *modelgateway.RouteVarianceError
			if !errors.Is(err, modelgateway.ErrRouteVariance) || !errors.As(err, &rerr) {
				t.Fatalf("em imposicao o turno tinha de falhar com ErrRouteVariance; veio %v", err)
			}
			if rerr.Check != c.check || rerr.Cause != c.causa {
				t.Fatalf("erro = {%s %s}; quero {%s %s}", rerr.Check, rerr.Cause, c.check, c.causa)
			}
			aos505SemMarcas(t, "a mensagem do erro", err.Error())
			if strings.Contains(err.Error(), "openai/outro") || strings.Contains(err.Error(), "exemplo.test") {
				t.Errorf("a mensagem do erro leva texto do proxy: %v", err)
			}
			selos := aos505Selos(t, m.store)
			if len(selos) != 1 || selos[0].Decision != audit.DecisionDeny || selos[0].Obligations[0].Params["reason"] != c.causa {
				t.Fatalf("queria 1 selo deny com a causa %s; vieram %+v", c.causa, selos)
			}
			// E pelo adaptador do runtime o erro chega igual.
			_, err = modelgateway.NewModelClient(m.gw, "gpt-4o", modelgateway.WithPrincipal("tok"), modelgateway.WithRegionBoard("eu", "board-eu")).
				Call(context.Background(), agentruntime.PromptView{Materialized: []byte("olá")})
			if !errors.Is(err, modelgateway.ErrRouteVariance) {
				t.Fatalf("Call: queria ErrRouteVariance, veio %v", err)
			}
		})
	}
	m := aos505Compor(t, modelgateway.RouteGovernanceEnforce, aos505HostA, aos505Cabecalhos("openai/k3", aos505APIBaseA))
	if _, err := m.gw.Chat(context.Background(), aos505Pedido("gpt-4o")); err != nil {
		t.Fatalf("uma rota igual passa em imposicao; veio %v", err)
	}
	if n := len(aos505Selos(t, m.store)); n != 0 {
		t.Fatalf("uma rota igual nao sela variancia; vieram %d selos", n)
	}
}

// UMA ROTA SEM PERFIL está fora do que é governado: conta como `diferente` em observação e falha
// em imposição, e o digest fica vazio.
func TestAOS505_RotaSemPerfil(t *testing.T) {
	const semPerfil = "text-embedding-3-large" // está na allowlist embebida e não tem perfil de chat
	m := aos505Compor(t, modelgateway.RouteGovernanceObserve, "", aos505Cabecalhos("openai/k3", aos505APIBaseA))
	resp, err := m.gw.Chat(context.Background(), aos505Pedido(semPerfil))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Route.Check != port.RouteCheckDifferent || resp.Route.ProfileDigest != "" {
		t.Fatalf("rota sem perfil = %+v; quero diferente e digest vazio", resp.Route)
	}
	if len(m.visto.obs) != 1 || m.visto.obs[0].Cause != modelgateway.RouteCauseNoProfile {
		t.Fatalf("observacao = %+v", m.visto.obs)
	}
	e := aos505Compor(t, modelgateway.RouteGovernanceEnforce, "", aos505Cabecalhos("openai/k3", aos505APIBaseA))
	_, err = e.gw.Chat(context.Background(), aos505Pedido(semPerfil))
	var rerr *modelgateway.RouteVarianceError
	if !errors.As(err, &rerr) || rerr.Cause != modelgateway.RouteCauseNoProfile {
		t.Fatalf("em imposicao uma rota sem perfil falha com rota_sem_perfil; veio %v", err)
	}
}

// UMA TROCA DE MODELO POR BAIXO, A MEIO DE UM RUN, É DETECTADA. O equivalente offline do guião
// com o proxy real: dois turnos pelo adaptador do runtime; entre eles a configuração do proxy
// muda — (i) o modelo, (ii) só o endpoint. O turno seguinte regista a variância em observação e
// falha com causa em imposição.
func TestAOS505_TrocaPorBaixoAMeioDoRun(t *testing.T) {
	for _, troca := range []struct {
		nome          string
		depois        map[string]string
		causa, servid string
	}{
		{"(i) outro modelo, mesmo endpoint", aos505Cabecalhos("openai/outro-nome", aos505APIBaseA), modelgateway.RouteCauseModelDifferent, "openai/outro-nome"},
		{"(ii) mesmo modelo, outro endpoint", aos505Cabecalhos("openai/k3", aos505APIBaseB), modelgateway.RouteCauseEndpointDifferent, "openai/k3"},
	} {
		for _, modo := range []string{modelgateway.RouteGovernanceObserve, modelgateway.RouteGovernanceEnforce} {
			t.Run(troca.nome+"/"+modo, func(t *testing.T) {
				m := aos505Compor(t, modo, aos505HostA, aos505Cabecalhos("openai/k3", aos505APIBaseA))
				cliente := modelgateway.NewModelClient(m.gw, "gpt-4o", modelgateway.WithPrincipal("tok"), modelgateway.WithRegionBoard("eu", "board-eu"))
				ctx := agentruntime.ContextWithModelCall(context.Background(), "run-troca", "step-000001")
				antes, err := cliente.Call(ctx, agentruntime.PromptView{Materialized: []byte("turno 1")})
				if err != nil || antes.RouteCheck != agentruntime.RouteEqual || antes.Model != "openai/k3" {
					t.Fatalf("turno 1: quero igual e openai/k3; veio %q %q err=%v", antes.RouteCheck, antes.Model, err)
				}
				m.proxy.definir(troca.depois) // a troca por baixo
				ctx = agentruntime.ContextWithModelCall(context.Background(), "run-troca", "step-000002")
				depois, err := cliente.Call(ctx, agentruntime.PromptView{Materialized: []byte("turno 2")})
				if modo == modelgateway.RouteGovernanceEnforce {
					var rerr *modelgateway.RouteVarianceError
					if !errors.As(err, &rerr) || rerr.Cause != troca.causa {
						t.Fatalf("turno 2 em imposicao: queria falhar com %s; veio %v", troca.causa, err)
					}
				} else {
					if err != nil || depois.RouteCheck != agentruntime.RouteDifferent || depois.Model != troca.servid {
						t.Fatalf("turno 2 em observacao: quero diferente e %q; veio %q %q err=%v", troca.servid, depois.RouteCheck, depois.Model, err)
					}
				}
				selos := aos505Selos(t, m.store)
				if len(selos) != 1 || selos[0].RunID != "run-troca" || selos[0].StepID != "step-000002" || selos[0].Obligations[0].Params["reason"] != troca.causa {
					t.Fatalf("queria 1 selo, do turno 2, com a causa %s; vieram %+v", troca.causa, selos)
				}
				var contagem []string
				for _, o := range m.visto.obs {
					contagem = append(contagem, o.Check)
				}
				if strings.Join(contagem, ",") != "igual,diferente" {
					t.Fatalf("contador = %v; quero igual,diferente", contagem)
				}
			})
		}
	}
}

// O CORPO DO PEDIDO AO PROVIDER TEM EXACTAMENTE OS CAMPOS DE HOJE, e nenhum parâmetro opcional —
// nos três modos. É o que sustenta que `drop_params: false` no proxy não altera nenhum pedido: o
// adaptador não envia nada que o proxy pudesse descartar.
func TestAOS505_CorpoDoPedido_SoOsCamposDeHoje(t *testing.T) {
	tools := []port.Tool{{Type: "function", Function: port.FunctionDef{Name: "arquivo", Description: "le", Parameters: json.RawMessage(`{"type":"object"}`)}}}
	var referencia [2]string
	for i, modo := range []string{"", modelgateway.RouteGovernanceOff, modelgateway.RouteGovernanceObserve, modelgateway.RouteGovernanceEnforce} {
		m := aos505Compor(t, modo, aos505HostA, aos505Cabecalhos("openai/k3", aos505APIBaseA))
		for j, opts := range [][]modelgateway.RuntimeAdapterOption{nil, {modelgateway.WithTools(tools)}} {
			opts = append(opts, modelgateway.WithPrincipal("tok"), modelgateway.WithRegionBoard("eu", "board-eu"))
			if _, err := modelgateway.NewModelClient(m.gw, "gpt-4o", opts...).Call(context.Background(), agentruntime.PromptView{Materialized: []byte("olá")}); err != nil {
				t.Fatalf("modo %q: Call: %v", modo, err)
			}
			corpo := m.proxy.pedidos()[j]
			var campos map[string]json.RawMessage
			if err := json.Unmarshal(corpo, &campos); err != nil {
				t.Fatalf("corpo ilegivel: %v", err)
			}
			var nomes []string
			for k := range campos {
				nomes = append(nomes, k)
			}
			sort.Strings(nomes)
			quer := "messages,model"
			if j == 1 {
				quer = "messages,model,tools"
			}
			if got := strings.Join(nomes, ","); got != quer {
				t.Errorf("modo %q: campos do pedido = %s; quero exactamente %s (nenhum parametro opcional: tool_choice, temperature, seed, max_tokens, stream)", modo, got, quer)
			}
			if i == 0 {
				referencia[j] = string(corpo)
			} else if string(corpo) != referencia[j] {
				t.Errorf("modo %q: o corpo do pedido mudou com o interruptor:\n veio:  %s\n quero: %s", modo, corpo, referencia[j])
			}
		}
	}
	if referencia[0] != `{"model":"gpt-4o","messages":[{"role":"user","content":"olá"}]}` {
		t.Errorf("o corpo do pedido sem tools nao e o de sempre: %s", referencia[0])
	}
}

// UM MODO INVÁLIDO RECUSA A COMPOSIÇÃO.
func TestAOS505_ModoInvalidoRecusaAComposicao(t *testing.T) {
	srv := okOpenAIServer(t, new(int))
	for _, modo := range []string{"Observe", "on", "enforce ", "1"} {
		cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(), nil)
		cfg.Route = modelgateway.RouteGovernance{Mode: modo}
		if _, err := modelgateway.NewProduction(context.Background(), cfg); !errors.Is(err, modelgateway.ErrBadRouteGovernance) {
			t.Errorf("modo %q: queria ErrBadRouteGovernance, veio %v", modo, err)
		}
	}
}

// aos505Assinar assina um documento de allowlist com uma chave de TESTE e carrega-o como um
// bundle externo — o caminho que o operador usa para pôr um nome real na allowlist.
func aos505Assinar(t *testing.T, doc string) *allowlist.Policy {
	t.Helper()
	seed := sha256.Sum256([]byte("aos-505-chave-de-teste"))
	priv := ed25519.NewKeyFromSeed(seed[:])
	d, err := allowlist.Digest([]byte(doc))
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	pol, err := allowlist.LoadSignedPolicy([]byte(doc),
		base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(d))),
		base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)))
	if err != nil {
		t.Fatalf("LoadSignedPolicy: %v", err)
	}
	return pol
}

// O NOME REAL DO MODELO, COM OS CARACTERES QUE ELE TIVER, EM TUDO O QUE COMPÕE NOMES.
//
// A troca do nome pedido pelo nome real é um passo de produção (a allowlist é re-assinada pelo
// operador). O que se prova aqui é que o código aguenta esse nome: um bundle assinado com ele
// carrega e autoriza; o gateway serve-o, e compara e sela a rota com ele (a partição do audit, o
// recurso e o `ToolID` do selo levam o nome); e o `stream_id` de admissão que a escada de tiers
// compõe com ele é representável — excepto com `.`, onde a composição da escada RECUSA O
// ARRANQUE nomeando o modelo (AOS-425), em vez de falhar a meio de um run.
func TestAOS505_NomeRealDoModelo_EmTudoOQueCompoeNomes(t *testing.T) {
	nomes := []string{"kimi-for-coding", "k3", "openai/kimi-for-coding", "moonshot/kimi-k2.5-preview"}
	doc := `{"version":"aos505/v1","default":"deny","rules":[{"id":"r1","board":"board-eu","models":["` +
		strings.Join(nomes, `","`) + `"],"regions":["eu","eu-west"]}]}`
	for _, nome := range nomes {
		t.Run(nome, func(t *testing.T) {
			proxy := &aos505Proxy{headers: aos505Cabecalhos("openai/outro", aos505APIBaseA)}
			srv := httptest.NewServer(proxy)
			t.Cleanup(srv.Close)
			store := audit.NewMemStore()
			cfg := prodConfig(store, srv.URL, srv.Client(), []modelgateway.InfraAccount{{KeyID: "acct-eu-1", Provider: "openai", Region: "eu"}})
			cfg.Allowlist = aos505Assinar(t, doc)
			cfg.Route = modelgateway.RouteGovernance{Mode: modelgateway.RouteGovernanceObserve}
			gw, err := modelgateway.NewProduction(context.Background(), cfg)
			if err != nil {
				t.Fatalf("NewProduction: %v", err)
			}
			resp, err := gw.Chat(context.Background(), aos505Pedido(nome))
			if err != nil {
				t.Fatalf("o gateway tinha de servir o nome real %q: %v", nome, err)
			}
			var pedido struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal(proxy.pedidos()[0], &pedido); err != nil || pedido.Model != nome {
				t.Fatalf("o proxy recebeu o modelo %q (err=%v); quero %q, byte a byte", pedido.Model, err, nome)
			}
			if resp.Route.Check != port.RouteCheckDifferent {
				t.Fatalf("a rota tinha de ser comparada com o nome real: %+v", resp.Route)
			}
			selos := aos505Selos(t, store)
			if len(selos) != 1 || selos[0].Resource.Value != nome || selos[0].ToolID != "model."+nome {
				t.Fatalf("o selo da variancia tinha de levar o nome real: %+v", selos)
			}
			if _, tem := modelgateway.RouteProfileFor(nome); (nome == "kimi-for-coding" || nome == "k3") && !tem {
				t.Errorf("o nome real %q tinha de ter perfil — e o que deixa trocar o nome pedido sem outro binario", nome)
			}

			// O stream_id de admissão: `admission/bucket/<provider>:<model>:<region>`.
			chave := "admission/bucket/openai:" + nome + ":eu"
			escada := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(), twoRegionAccounts())
			escada.Allowlist = cfg.Allowlist
			escada.Routing = modelgateway.RoutingConfig{Tiers: []tiering.Tier{{Name: "t1", Model: nome}}}
			_, eerr := modelgateway.NewProduction(context.Background(), escada)
			if strings.Contains(nome, ".") {
				if eventstore.ValidarStreamID(chave) == nil {
					t.Fatalf("pre-condicao: %q devia ser irrepresentavel", chave)
				}
				if !errors.Is(eerr, modelgateway.ErrRoutingModelNaoRepresentavel) || !strings.Contains(eerr.Error(), nome) {
					t.Fatalf("um nome com ponto numa escada tinha de recusar o ARRANQUE nomeando o modelo; veio %v", eerr)
				}
				return
			}
			if err := eventstore.ValidarStreamID(chave); err != nil {
				t.Fatalf("o stream_id de admissao %q nao e representavel: %v", chave, err)
			}
			if errors.Is(eerr, modelgateway.ErrRoutingModelNaoRepresentavel) {
				t.Fatalf("a escada recusou o nome real %q: %v", nome, eerr)
			}
		})
	}
}
