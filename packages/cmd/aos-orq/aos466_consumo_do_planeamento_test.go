package main

// AOS-466 — O `aos-orq` MEDE O CONSUMO DO MODELO DE PLANEAMENTO E DECLARA-O AO NÓ.
//
// O nó reserva uma quantia de planeamento contra a quota de quem submeteu o pedido e liquida-a pelo
// que este processo declara no desfecho. Estes testes fixam o que se mede, o que conta como NÃO
// MEDIDO, o nome dos campos que o nó lê, e — com o binário real e o gateway vivo contra um upstream
// falso — que o consumo atravessa `consume` → `serve` → modelo → desfecho.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aos-ref/platform/model-gateway/port"
)

func TestAOS466OMedidorSomaEMarcaONaoMedido(t *testing.T) {
	uso := func(p, c int64) port.Usage { return port.Usage{PromptTokens: p, CompletionTokens: c} }
	casos := []struct {
		nome    string
		chamar  func(m *medidorDoPlaneamento)
		consumo consumoDoPlaneamento
	}{
		{"sem chamadas e zero medido", func(*medidorDoPlaneamento) {}, consumoDoPlaneamento{TokensMedidos: true, CustoMedido: true}},
		{"soma prompt e completion", func(m *medidorDoPlaneamento) {
			m.registar(uso(10, 5), nil)
			m.registar(uso(7, 3), nil)
		}, consumoDoPlaneamento{Tokens: 25, TokensMedidos: true}},
		{"uma chamada falhada nao e medida", func(m *medidorDoPlaneamento) {
			m.registar(uso(10, 5), nil)
			m.registar(port.Usage{}, errors.New("upstream"))
		}, consumoDoPlaneamento{Tokens: 15}},
		{"o total do provider vale quando e maior", func(m *medidorDoPlaneamento) {
			m.registar(port.Usage{PromptTokens: 10, TotalTokens: 18}, nil)
		}, consumoDoPlaneamento{Tokens: 18, TokensMedidos: true}},
		{"resposta sem usage nao e medida", func(m *medidorDoPlaneamento) {
			m.registar(uso(0, 0), nil)
		}, consumoDoPlaneamento{}},
		{"completion negativo nao e medido", func(m *medidorDoPlaneamento) {
			m.registar(uso(10, -3), nil)
		}, consumoDoPlaneamento{}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			m := &medidorDoPlaneamento{}
			c.chamar(m)
			if got := m.consumo(); got != c.consumo {
				t.Fatalf("consumo %+v, esperava %+v", got, c.consumo)
			}
		})
	}
	// Um medidor nil não viu as chamadas: não sabe, e diz que não sabe.
	var nulo *medidorDoPlaneamento
	nulo.registar(port.Usage{PromptTokens: 1}, nil)
	if got := nulo.consumo(); got.TokensMedidos || got.CustoMedido {
		t.Fatalf("um medidor nil declarou consumo medido: %+v", got)
	}
}

// gatewayComUsage devolve uma resposta com `usage`, ou um erro.
type gatewayComUsage struct {
	fakeGateway
	usage port.Usage
	erro  error
}

func (g *gatewayComUsage) Chat(ctx context.Context, req port.ChatRequest) (port.ChatResponse, error) {
	if g.erro != nil {
		return port.ChatResponse{Usage: g.usage}, g.erro
	}
	r, _ := g.fakeGateway.Chat(ctx, req)
	r.Usage = g.usage
	return r, nil
}

// TestAOS466OModeloContaCadaChamada — o adaptador do planeador conta o `usage` de cada chamada,
// mesmo quando a resposta não serve (a chamada foi cobrada), e uma chamada falhada fica NÃO MEDIDA.
func TestAOS466OModeloContaCadaChamada(t *testing.T) {
	ctx := context.Background()
	m := &medidorDoPlaneamento{}
	ok := gatewayDecomposeModel{gw: &gatewayComUsage{fakeGateway: fakeGateway{resposta: "{}"}, usage: port.Usage{PromptTokens: 10, CompletionTokens: 5}}, medidor: m}
	if _, err := ok.Complete(ctx, "s", "u"); err != nil {
		t.Fatal(err)
	}
	vazio := gatewayDecomposeModel{gw: &gatewayComUsage{fakeGateway: fakeGateway{semEscolha: true}, usage: port.Usage{PromptTokens: 20, CompletionTokens: 1}}, medidor: m}
	if _, err := vazio.Complete(ctx, "s", "u"); err == nil {
		t.Fatal("sem escolhas devia falhar")
	}
	if got := m.consumo(); got.Tokens != 36 || !got.TokensMedidos || got.CustoMedido {
		t.Fatalf("consumo %+v, esperava 36 tokens medidos e custo por medir", got)
	}
	falha := gatewayDecomposeModel{gw: &gatewayComUsage{erro: errors.New("upstream"), usage: port.Usage{PromptTokens: 99, CompletionTokens: 1}}, medidor: m}
	if _, err := falha.Complete(ctx, "s", "u"); err == nil {
		t.Fatal("o erro do gateway devia propagar")
	}
	if got := m.consumo(); got.Tokens != 36 || got.TokensMedidos {
		t.Fatalf("uma chamada falhada nao pode ser medida nem somada: %+v", got)
	}
}

// TestAOS466ODesfechoLevaOConsumoComOsNomesQueONoLe — os nomes escritos à MÃO, como no
// TestAOS423VocabularioDeClassesCasaComONo: o `aos-orq` não importa o nó, e um teste que derivasse
// os nomes das mesmas tags não provaria que as duas pontas concordam. O nó lê `consumo.tokens`,
// `consumo.tokens_medidos`, `consumo.cost_micro_usd` e `consumo.custo_medido` (plan_claim.go).
func TestAOS466ODesfechoLevaOConsumoComOsNomesQueONoLe(t *testing.T) {
	var (
		mu    sync.Mutex
		corpo map[string]any
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /plans/outcome", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(b, &corpo)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := aos413ClienteDoAmbiente(t, srv.URL)

	err := c.ReportarDesfecho(context.Background(), "plano-466", 2, "terminal", 0, "resumo",
		consumoDoPlaneamento{Tokens: 42, TokensMedidos: true, CostMicroUSD: 0, CustoMedido: false}, true)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	consumo, ok := corpo["consumo"].(map[string]any)
	if !ok {
		t.Fatalf("o desfecho nao leva `consumo`: %v", corpo)
	}
	quer := map[string]any{"tokens": float64(42), "tokens_medidos": true, "cost_micro_usd": float64(0), "custo_medido": false}
	for k, v := range quer {
		if consumo[k] != v {
			t.Fatalf("consumo.%s = %v, esperava %v (corpo %v)", k, consumo[k], v, consumo)
		}
	}
	// AOS-467: o nome do campo que o tecto de gerações do nó conta, também escrito à mão.
	if corpo["chamou_modelo"] != true {
		t.Fatalf("o desfecho tem de levar `chamou_modelo` (AOS-467): %v", corpo)
	}
}

// TestAOS466ComOBinarioReal — o consumo atravessa o caminho inteiro: `consume` reclama, o `serve`
// in-process decompõe com o GATEWAY VIVO contra um upstream falso que cobra 10+5 tokens por
// chamada, e o desfecho que chega ao nó declara-os. O desfecho do `serve` em si não importa aqui —
// o consumo reporta-se em qualquer classe. O CONTROLO é o `consume` com o fixture: zero chamadas,
// zero medido.
func TestAOS466ComOBinarioReal(t *testing.T) {
	bin := construir(t)

	consumoDe := func(t *testing.T, espiao *aos443Espiao) map[string]any {
		t.Helper()
		espiao.mu.Lock()
		defer espiao.mu.Unlock()
		if len(espiao.desfechos) != 1 {
			t.Fatalf("esperava 1 desfecho, vieram %d", len(espiao.desfechos))
		}
		c, ok := espiao.desfechos[0]["consumo"].(map[string]any)
		if !ok {
			t.Fatalf("o desfecho nao leva `consumo`: %v", espiao.desfechos[0])
		}
		return c
	}

	t.Run("gateway vivo", func(t *testing.T) {
		no := &aos442No{}
		a := novoAmbiente442(t, bin, no)
		espiao := &aos443Espiao{}
		frente := espiao.servidor(t, strings.TrimPrefix(a.env[0], "AOS_ORQ_NODE_URL="))
		a.env[0] = "AOS_ORQ_NODE_URL=" + frente.URL

		keyPath := filepath.Join(a.dir, "model.key")
		escrever(t, keyPath, "sk-teste-aos466")
		srv := upstreamComPlano(t)
		env := append(append([]string{}, a.env...),
			"AOS_MODEL_ENDPOINT="+srv.URL, "AOS_MODEL_NAME=gpt-4o", "AOS_MODEL_REGION=eu",
			"AOS_MODEL_BOARD=board-eu", "AOS_MODEL_API_KEY_PATH="+keyPath, "AOS_MODEL_AUDIT_PATH=")

		no.oferecer(pedidoReclamado{RunID: "plan-aos466", Objective: "recolher e analisar dados", Geracao: 1})
		r := correrComEnv(t, env, bin, "consume", "--wal", a.wal, "--snapshot", a.snap,
			"--poll-interval", "20ms", "--plan-timeout", "300ms", "--max", "1")
		if r.code != exitOK {
			t.Fatalf("o consume tinha de sair 0: %d\n%s\n%s", r.code, r.stdout, r.stderr)
		}
		c := consumoDe(t, espiao)
		// Uma decomposição à primeira (`tentativas=1` no stdout), a 10+5 tokens.
		if !strings.Contains(r.stdout, "decomposto: objectivo -> plano de 2 nos (tentativas=1") {
			t.Fatalf("o serve nao decompos pelo gateway vivo:\n%s\n%s", r.stdout, r.stderr)
		}
		if c["tokens"] != float64(15) || c["tokens_medidos"] != true {
			t.Fatalf("o consumo do modelo nao chegou ao no (upstream cobra 15 por chamada): %v\n%s\n%s", c, r.stdout, r.stderr)
		}
		if c["custo_medido"] != false {
			t.Fatalf("com chamadas ao modelo os dolares nao se medem aqui: %v", c)
		}
		espiao.mu.Lock()
		chamou := espiao.desfechos[0]["chamou_modelo"]
		espiao.mu.Unlock()
		if chamou != true {
			t.Fatalf("a geracao que decompos com o gateway vivo declara chamou_modelo=true (AOS-467), veio %v", chamou)
		}
	})

	t.Run("controlo: fixture sem modelo", func(t *testing.T) {
		no := &aos442No{}
		a := novoAmbiente442(t, bin, no)
		espiao := &aos443Espiao{}
		frente := espiao.servidor(t, strings.TrimPrefix(a.env[0], "AOS_ORQ_NODE_URL="))
		a.env[0] = "AOS_ORQ_NODE_URL=" + frente.URL
		no.oferecer(pedidoReclamado{RunID: "plan-aos466-fixture", Objective: "o", Geracao: 1})
		a.consumir(t, planoFixtureDuasFolhasComSnapshotAOS408, "--plan-timeout", "300ms")
		c := consumoDe(t, espiao)
		if c["tokens"] != float64(0) || c["tokens_medidos"] != true || c["custo_medido"] != true {
			t.Fatalf("sem chamadas ao modelo o consumo e zero medido: %v", c)
		}
		espiao.mu.Lock()
		chamou := espiao.desfechos[0]["chamou_modelo"]
		espiao.mu.Unlock()
		if chamou != false {
			t.Fatalf("sem chamadas ao modelo a geracao nao conta para o tecto (AOS-467): chamou_modelo=%v", chamou)
		}
	})
}
