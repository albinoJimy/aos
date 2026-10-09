package modelgateway_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/internal/wirefake"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-515 — AS LACUNAS QUE A REVISÃO ADVERSARIAL ENCONTROU: os ids do provider repetidos, a
// devolução parcial, o streaming, o estado desalinhado, e o estado acima do tecto pelo caminho
// real.

// aos515Turno é uma mensagem `assistant` com tool calls e um estado devolvível para `perfil`,
// com os ids do provider dados (um por chamada; os ids do runtime são `<passo>-tool-<n>`).
func aos515Turno(perfil modelgateway.RouteProfile, passo string, idsDoProvider ...string) port.Message {
	m := port.Message{Role: port.RoleAssistant, State: &port.MessageState{
		RouteProfileDigest: perfil.Digest(), ServedModel: perfil.ExpectedModel, RouteCheck: port.RouteCheckEqual,
		Fields: []port.ProviderStateField{{Where: port.StateWhereMessage, Name: "thinking_blocks", Raw: []byte(`[{"type":"thinking","thinking":"S-PENSA"}]`)}},
	}}
	for n, id := range idsDoProvider {
		m.ToolCalls = append(m.ToolCalls, port.ToolCall{ID: fmt.Sprintf("%s-tool-%d", passo, n+1), Type: "function", Function: port.FunctionCall{Name: "doc_read", Arguments: "{}"}})
		m.State.ToolCalls = append(m.State.ToolCalls, port.ProviderStateToolCall{N: n + 1, ID: []byte(`"` + id + `"`), IDUsable: id != "", IDValue: id})
	}
	return m
}

// OS IDS DO PROVIDER: um por chamada, utilizáveis, diferentes entre si, diferentes de todos os já
// usados no pedido, e nunca com a forma de um id do runtime. Fora disso o turno não leva estado
// (`id_do_provider_inutilizavel`): numa rota obrigatória o pedido não sai; numa opcional segue
// com os ids do runtime.
func TestAOS515_IdsDoProvider_RepetidosOuComFormaDeRuntimeNaoServem(t *testing.T) {
	obrigatorio := aos515Rota(t, "gpt-4o", `,"devolver":"obrigatorio","tool_call_id":"provider"`)
	opcional := aos515Rota(t, "gpt-4o", `,"devolver":"opcional","tool_call_id":"provider"`)
	for nome, c := range map[string]struct {
		turnos     [][]string // os ids do provider de cada turno
		devolvidos int        // em `opcional`
	}{
		"todos diferentes":                         {[][]string{{"toolu_A", "toolu_B"}, {"toolu_C"}}, 2},
		"repetido dentro do turno":                 {[][]string{{"toolu_A", "toolu_A"}}, 0},
		"repetido entre turnos":                    {[][]string{{"toolu_A"}, {"toolu_A"}}, 1},
		"em falta numa das chamadas":               {[][]string{{"toolu_A", ""}}, 0},
		"com a forma de um id do runtime":          {[][]string{{"step-000002-tool-1"}, {"toolu_B"}}, 1},
		"igual a um id do runtime ja usado":        {[][]string{{"toolu_A", "toolu_A"}, {"step-000001-tool-1"}}, 0},
		"o primeiro turno falha e o segundo passa": {[][]string{{"toolu_A", "toolu_A"}, {"toolu_B"}}, 1},
	} {
		t.Run(nome, func(t *testing.T) {
			montar := func(perfil modelgateway.RouteProfile) port.ChatRequest {
				req := port.ChatRequest{Model: "gpt-4o", Messages: []port.Message{{Role: port.RoleUser, Content: "o"}}}
				for i, ids := range c.turnos {
					m := aos515Turno(perfil, fmt.Sprintf("step-%06d", i+1), ids...)
					req.Messages = append(req.Messages, m)
					for _, tc := range m.ToolCalls {
						req.Messages = append(req.Messages, port.Message{Role: port.RoleTool, ToolCallID: tc.ID, Content: "r"})
					}
				}
				return req
			}
			turnos, devolvidos, causa, err := modelgateway.ArmarDevolucaoParaTeste(montar(opcional), opcional)
			if err != nil || turnos != len(c.turnos) || devolvidos != c.devolvidos {
				t.Fatalf("opcional: turnos=%d devolvidos=%d (quero %d) err=%v", turnos, devolvidos, c.devolvidos, err)
			}
			tudo := c.devolvidos == len(c.turnos)
			if tudo != (causa == "") || (!tudo && causa != modelgateway.StateCauseProviderID) {
				t.Fatalf("opcional: causa %q", causa)
			}
			_, _, causa, err = modelgateway.ArmarDevolucaoParaTeste(montar(obrigatorio), obrigatorio)
			var se *modelgateway.StateReturnError
			if tudo {
				if err != nil {
					t.Fatalf("obrigatorio: tinha de devolver tudo; veio %v", err)
				}
				return
			}
			if !errors.As(err, &se) || se.Cause != modelgateway.StateCauseProviderID || causa != modelgateway.StateCauseProviderID {
				t.Fatalf("obrigatorio: queria a recusa por %s; veio %v", modelgateway.StateCauseProviderID, err)
			}
		})
	}
	// O que sai no wire nunca tem dois ids iguais: o caso em que o provider deu, no primeiro
	// turno, o id que o runtime dá ao segundo.
	gravador := &aos513Provider{}
	m := aos513Compor(t, gravador, opcional)
	req := aos505Pedido("gpt-4o")
	t1 := aos515Turno(opcional, "step-000001", "step-000002-tool-1")
	t2 := aos515Turno(opcional, "step-000002", "toolu_A", "toolu_A")
	req.Messages = append(req.Messages, t1, port.Message{Role: port.RoleTool, ToolCallID: "step-000001-tool-1", Content: "r"},
		t2, port.Message{Role: port.RoleTool, ToolCallID: "step-000002-tool-1", Content: "r"}, port.Message{Role: port.RoleTool, ToolCallID: "step-000002-tool-2", Content: "r"})
	if _, err := m.gw.Chat(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	corpo := gravador.pedidos()[0]
	for _, id := range []string{`"id":"step-000001-tool-1"`, `"id":"step-000002-tool-1"`, `"id":"step-000002-tool-2"`} {
		if bytes.Count(corpo, []byte(id)) != 1 {
			t.Fatalf("o pedido tinha de levar %s exactamente uma vez:\n%s", id, corpo)
		}
	}
}

// A DEVOLUÇÃO PARCIAL. Numa rota `opcional`, com o estado do primeiro turno devolvível e o do
// segundo em falta, o pedido sai com o do primeiro e conta `parcial`, com a causa do que faltou.
func TestAOS515_Opcional_DevolucaoParcialConta(t *testing.T) {
	gravador := &aos513Provider{}
	falso := &wirefake.Exigente{Turnos: 3}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(port.HeaderServedModel, aos515Modelo)
		if len(falso.Pedidos()) < 2 {
			falso.ServeHTTP(w, r)
			return
		}
		gravador.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	run := aos515ComporEm(t, srv.URL, srv.Client(), "", aos515Rota(t, "gpt-4o", `,"devolver":"opcional"`))
	var primeiro string
	for i := 1; i <= 2; i++ {
		if _, err := run.passo("gpt-4o"); err != nil {
			t.Fatalf("turno %d: %v", i, err)
		}
		if i == 1 {
			for k := range run.estados {
				primeiro = k
			}
		}
	}
	// O estado do SEGUNDO turno fica só com a referência (os bytes desaparecem).
	for k := range run.estados {
		if k != primeiro {
			delete(run.estados, k)
		}
	}
	run.obs = nil
	if _, err := run.passo("gpt-4o"); err != nil {
		t.Fatalf("o pedido parcial tinha de sair: %v", err)
	}
	if len(run.obs) != 1 || run.obs[0] != (modelgateway.StateReturnObservation{Result: modelgateway.StateReturnPartial, Cause: port.StateMissingReference}) {
		t.Fatalf("contado %+v; queria parcial/estado_so_referencia", run.obs)
	}
	corpo := gravador.pedidos()[0]
	if !bytes.Contains(corpo, wirefake.BlocosEmitidos(0)) || bytes.Contains(corpo, wirefake.BlocosEmitidos(1)) {
		t.Fatalf("o pedido parcial leva o estado do primeiro turno e nao o do segundo:\n%s", corpo)
	}
}

// O STREAMING NÃO DEVOLVE ESTADO, qualquer que seja o perfil e mesmo que o chamador traga o
// estado já marcado para sair.
func TestAOS515_Streaming_NaoDevolveEstado(t *testing.T) {
	var mu sync.Mutex
	var corpos [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		corpo, _ := io.ReadAll(r.Body)
		mu.Lock()
		corpos = append(corpos, corpo)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	perfil := aos515Rota(t, "gpt-4o", `,"devolver":"obrigatorio"`)
	cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(), []modelgateway.InfraAccount{{KeyID: "acct-eu-1", Provider: "openai", Region: "eu"}})
	cfg.RouteProfiles = []modelgateway.RouteProfile{perfil}
	gw, err := modelgateway.NewProduction(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := aos505Pedido("gpt-4o")
	turno := aos515Turno(perfil, "step-000001", "toolu_A")
	turno.State.Return, turno.State.ProviderIDs = true, true // o chamador marca-o para sair
	req.Messages = append(req.Messages, turno, port.Message{Role: port.RoleTool, ToolCallID: "step-000001-tool-1", Content: "r"})
	s, err := gw.ChatStream(context.Background(), req)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if _, err := port.CollectStream(s); err != nil {
		t.Fatalf("CollectStream: %v", err)
	}
	if len(corpos) != 1 {
		t.Fatalf("queria um pedido de streaming, vieram %d", len(corpos))
	}
	for _, proibido := range []string{"S-PENSA", "thinking_blocks", "toolu_A"} {
		if bytes.Contains(corpos[0], []byte(proibido)) {
			t.Fatalf("o pedido de streaming leva %q:\n%s", proibido, corpos[0])
		}
	}
	if !turno.State.Return {
		t.Fatalf("a mensagem do chamador foi alterada")
	}
}

// O ESTADO DESALINHADO. Um envelope com outro número de tool calls que o turno projectado (o
// turno escalou a meio) não se devolve: `obrigatorio` recusa com a causa; `opcional` conta e o
// pedido SEGUE, sem estado — não falha na serialização.
func TestAOS515_EstadoDesalinhado_NaoSeDevolve(t *testing.T) {
	for _, classe := range []string{"obrigatorio", "opcional"} {
		falso := &wirefake.Exigente{Proibe: true}
		run := aos515Compor(t, falso, aos515Modelo, aos515Rota(t, "gpt-4o", `,"devolver":"`+classe+`"`))
		if _, err := run.passo("gpt-4o"); err != nil {
			t.Fatal(err)
		}
		// O envelope do turno passa a ter DUAS tool calls; o tail só tem uma. Rótulo e chave são
		// os do envelope novo, para que a verificação do digest passe.
		var antigo string
		for k := range run.estados {
			antigo = k
		}
		env, err := port.UnmarshalProviderStateEnvelope(run.estados[antigo])
		if err != nil || len(env.ToolCalls) != 1 {
			t.Fatalf("envelope do primeiro turno: %v (%d tool calls)", err, len(env.ToolCalls))
		}
		env.ToolCalls = append(env.ToolCalls, port.ProviderStateToolCall{N: 2, ID: []byte(`"toolu_2"`), IDUsable: true, IDValue: "toolu_2"})
		cru, err := env.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		soma := sha256.Sum256(cru)
		novo := "sha256:" + hex.EncodeToString(soma[:])
		run.estados = map[string][]byte{novo: cru}
		for i := range run.segs {
			for j := range run.segs[i].Meta {
				if run.segs[i].Meta[j].Key == agentruntime.StateDigestLabel {
					run.segs[i].Meta[j].Value = novo
				}
			}
		}
		_, err = run.passo("gpt-4o")
		var se *modelgateway.StateReturnError
		if classe == "obrigatorio" {
			if !errors.As(err, &se) || se.Cause != port.StateMissingMisaligned || len(falso.Pedidos()) != 1 {
				t.Fatalf("obrigatorio: queria a recusa por %s; veio %v", port.StateMissingMisaligned, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("opcional: o pedido segue sem estado; veio %v", err)
		}
		aos515SemSentinelas(t, "pedido com o estado desalinhado", falso.Pedidos()[1].Corpo)
		if len(run.obs) != 1 || run.obs[0] != (modelgateway.StateReturnObservation{Result: modelgateway.StateReturnNone, Cause: port.StateMissingMisaligned}) {
			t.Fatalf("contado %+v", run.obs)
		}
	}
}

// ACIMA DO TECTO, PELO CAMINHO REAL. O provider manda um raciocínio que não cabe no tecto do nó:
// o adaptador marca o turno «não devolvível», o loop não lhe dá rótulo, e no turno seguinte uma
// rota obrigatória recusa (`estado_ausente`) e uma opcional segue sem estado.
func TestAOS515_AcimaDoTecto_OEstadoNaoDevolvivelNaoSai(t *testing.T) {
	grande := strings.Repeat("S-PENSA ", 400) // 3200 bytes: não cabe no tecto mínimo (1024)
	for _, classe := range []string{"obrigatorio", "opcional"} {
		var mu sync.Mutex
		var corpos [][]byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			corpo, _ := io.ReadAll(r.Body)
			mu.Lock()
			corpos = append(corpos, corpo)
			n := len(corpos)
			mu.Unlock()
			w.Header().Set(port.HeaderServedModel, aos515Modelo)
			w.Header().Set("Content-Type", "application/json")
			if n == 1 {
				_, _ = io.WriteString(w, `{"id":"c1","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":"`+grande+`",`+
					`"tool_calls":[{"id":"toolu_1","type":"function","function":{"name":"doc_read","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":5}}`)
				return
			}
			_, _ = w.Write(wirefake.Corpo("content_texto"))
		}))
		t.Cleanup(srv.Close)
		run := aos515ComporEm(t, srv.URL, srv.Client(), "", aos515Rota(t, "gpt-4o", `,"devolver":"`+classe+`"`))
		run.maxBytes = modelgateway.MinProviderStateMaxBytes
		out, err := run.passo("gpt-4o")
		if err != nil || out.State == nil || out.State.Status != agentruntime.ProviderStateNotReturnable || len(run.estados) != 0 {
			t.Fatalf("%s: o primeiro turno tinha de ficar nao devolvivel: %+v err=%v", classe, out.State, err)
		}
		_, err = run.passo("gpt-4o")
		var se *modelgateway.StateReturnError
		if classe == "obrigatorio" {
			if !errors.As(err, &se) || se.Cause != port.StateMissingAbsent || len(corpos) != 1 {
				t.Fatalf("obrigatorio: queria a recusa por %s e nenhum pedido; veio %v (%d pedidos)", port.StateMissingAbsent, err, len(corpos))
			}
			continue
		}
		if err != nil || len(corpos) != 2 || bytes.Contains(corpos[1], []byte("S-PENSA")) {
			t.Fatalf("opcional: o pedido segue sem o estado; err=%v pedidos=%d", err, len(corpos))
		}
	}
}
