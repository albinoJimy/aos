package bancoensaio

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/platform/model-gateway/port"

	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// AOS-516 — A QUALIFICAÇÃO DA DEVOLUÇÃO NA FORMA DA OPENROUTER, NO MODO FALSO.
//
// O nó de ensaio, com um perfil candidato de uma rota `openrouter/…`, à frente do provider falso
// do estado na forma da OpenRouter: `reasoning` (texto) e `reasoning_details` (blocos, com
// assinatura) na resposta, exigidos de volta no topo da mensagem `assistant` de cada turno.
// Sem proxy: os bytes que o gateway devolve são os que recebeu.

// aos516PerfilDaOpenRouter lê um perfil candidato da rota `openrouter/…` de ensaio.
func aos516PerfilDaOpenRouter(t *testing.T, params, devolver string) *modelgateway.RouteProfile {
	t.Helper()
	p, err := LerPerfilCandidato([]byte(aos516PerfilOpenRouter("openrouter/autor-de-ensaio/modelo-de-ensaio", params, devolver)))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// OBRIGATÓRIO + EXIGENTE ⇒ `cumprida`. Os `reasoning_details` de cada turno voltam no topo da
// mensagem, com os mesmos valores E os mesmos bytes; o texto de `reasoning` volta também.
func TestAOS516_Falso_OpenRouter_ObrigatorioCumpre(t *testing.T) {
	for nome, c := range map[string]struct {
		params string
		quer   map[string]int
	}{
		"thinking":         {aos516ParamsThinking, map[string]int{"thinking": 4, "thinking.type": 4, "thinking.budget_tokens": 4, "max_tokens": 4}},
		"reasoning_effort": {aos516ParamsEffort, map[string]int{"reasoning_effort": 4, "max_tokens": 4}},
	} {
		t.Run(nome, func(t *testing.T) {
			const amostras = 3
			perfil := aos516PerfilDaOpenRouter(t, c.params, "obrigatorio")
			falso := &FalsoDeEstado{Turnos: 3, FormaDoEstado: FormaOpenRouter, ServidoComo: perfil.ExpectedModel}
			r, forma := aos516Correr(t, perfil, falso, "", amostras)
			aos516Veredicto(t, r, QualificacaoCumprida)
			aos516Contagens(t, "desfechos", r.Taxas.Desfechos, map[string]int{DesfechoCumprido: amostras})
			aos516Contagens(t, "http", r.Taxas.HTTP, map[string]int{"200": 4 * amostras})
			d := r.Taxas.Devolucao
			if d == nil || d.TurnosComRaciocinioCapturado != 3*amostras || d.PedidosQueDeviamLevarRaciocinio != 3*amostras || d.AceitesPeloFornecedor != 3*amostras || d.Recusas != 0 {
				t.Fatalf("devolucao = %+v", d)
			}
			if forma == nil || forma.Wire != WireDeChat || forma.Turnos != 6*amostras || len(forma.Recusas) != 0 {
				t.Fatalf("forma no fornecedor = %+v", forma)
			}
			aos516Contagens(t, "estado que voltou", forma.Estado, map[string]int{
				"reasoning_details.intacto": 6 * amostras, "reasoning_details.bytes_iguais": 6 * amostras,
				"reasoning.presente": 6 * amostras, "tool_call.id_do_runtime": 6 * amostras,
			})
			aos516Contagens(t, "forma do assistant", forma.Assistant, map[string]int{
				"content=vazio chaves=content,reasoning,reasoning_details,role,tool_calls": 6 * amostras,
			})
			quer := map[string]int{}
			for k, v := range c.quer {
				quer[k] = v * amostras
			}
			aos516Contagens(t, "parametros", forma.Parametros, quer)
		})
	}
}

// OS CONTROLOS NEGATIVOS da forma da OpenRouter.
func TestAOS516_Falso_OpenRouter_ControlosNegativos(t *testing.T) {
	const amostras = 3
	t.Run("perfil nunca com o exigente: o falso recusa por estado em falta (o veredicto esta no teste da linha de comandos)", func(t *testing.T) {
		perfil := aos516PerfilDaOpenRouter(t, aos516ParamsThinking, "nunca")
		r, forma := aos516Correr(t, perfil, &FalsoDeEstado{Turnos: 3, FormaDoEstado: FormaOpenRouter, ServidoComo: perfil.ExpectedModel}, "", amostras)
		aos516Contagens(t, "http", r.Taxas.HTTP, map[string]int{"200": amostras, "400": amostras})
		aos516Contagens(t, "recusas do falso", forma.Recusas, map[string]int{RecusaEstadoEmFalta: amostras})
		aos516Contagens(t, "estado que voltou", forma.Estado, map[string]int{"reasoning_details.em_falta": amostras, "tool_call.id_do_runtime": amostras})
	})
	t.Run("perfil obrigatorio com o provider que proibe estado: nao_cumprida", func(t *testing.T) {
		perfil := aos516PerfilDaOpenRouter(t, aos516ParamsThinking, "obrigatorio")
		r, forma := aos516Correr(t, perfil, &FalsoDeEstado{Turnos: 3, Proibe: true, FormaDoEstado: FormaOpenRouter, ServidoComo: perfil.ExpectedModel}, "", amostras)
		if q := r.Qualificacao; q == nil || q.Veredicto != QualificacaoNaoCumprida {
			t.Errorf("qualificacao = %+v, quer %s", q, QualificacaoNaoCumprida)
		}
		aos516Contagens(t, "recusas do falso", forma.Recusas, map[string]int{RecusaEstadoPresente: amostras})
	})
	t.Run("o modelo servido nao e o do perfil: o estado nao sai e a devolucao nao se cumpre", func(t *testing.T) {
		perfil := aos516PerfilDaOpenRouter(t, aos516ParamsThinking, "obrigatorio")
		r, forma := aos516Correr(t, perfil, &FalsoDeEstado{Turnos: 3, FormaDoEstado: FormaOpenRouter, ServidoComo: "openrouter/outro-autor/outro-modelo"}, "", amostras)
		if q := r.Qualificacao; q == nil || q.Veredicto == QualificacaoCumprida {
			t.Errorf("qualificacao = %+v: com outra rota servida nao pode ser cumprida", q)
		}
		if forma.Estado["reasoning_details.intacto"] != 0 {
			t.Errorf("o estado saiu para uma rota que nao se provou igual: %v", forma.Estado)
		}
	})
}

// A forma da OpenRouter que o falso emite passa pelas listas FECHADAS do gateway: a sonda do
// estado guarda `reasoning` e `reasoning_details` de `message`, com os bytes recebidos.
func TestAOS516_Falso_OpenRouter_AFormaCabeNasListasDoGateway(t *testing.T) {
	esperado := detalhesDoTurno(0)
	if !mesmosValores(esperado, []byte(`[{"index":0,"format":"anthropic-claude-v1","id":"reasoning-text-0","signature":"`+assinaturaDoTurno(0)+`","text":"S-PENSA-0 <b> & e","type":"reasoning.text"},`+
		` {"type":"reasoning.encrypted","data":"`+redigidoDoTurno(0)+`","id":"reasoning-encrypted-0","format":"anthropic-claude-v1","index":1}]`)) {
		t.Errorf("os detalhes do turno 0 mudaram de forma: %s", esperado)
	}
	if mesmosValores(esperado, detalhesDoTurno(1)) || mesmosValores(esperado, []byte(`[]`)) {
		t.Error("a comparacao por valores aceita o que nao e igual")
	}
}

// O VERDE FALSO QUE A CORRIDA REAL PODIA DAR. Atrás do proxy, a resposta da OpenRouter chega ao
// gateway com `reasoning_details` SÓ dentro de `provider_specific_fields` (medido), e o gateway
// devolve-o lá. Um fornecedor real que ignore esse saco responde 2xx sem ter lido o estado — e
// o banco, que só vê o código, contava o pedido como aceite. Aqui o handler faz de proxy mais
// fornecedor tolerante: aceita tudo. O veredicto NÃO pode ser `cumprida`.
func TestAOS516_OpenRouter_EstadoSoNoSacoDoProxyNaoDaCumprida(t *testing.T) {
	const amostras = 3
	perfil := aos516PerfilDaOpenRouter(t, aos516ParamsEffort, "obrigatorio")
	tolerante := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		corpo, _ := io.ReadAll(r.Body)
		var pedido struct {
			Messages []struct {
				Role      string            `json:"role"`
				ToolCalls []json.RawMessage `json:"tool_calls"`
			} `json:"messages"`
			pedidoAoFalso
		}
		_ = json.Unmarshal(corpo, &pedido)
		_ = json.Unmarshal(corpo, &pedido.pedidoAoFalso)
		turnos := 0
		for _, m := range pedido.Messages {
			if m.Role == "assistant" && len(m.ToolCalls) > 0 {
				turnos++
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(port.HeaderServedModel, perfil.ExpectedModel)
		if len(pedido.Tools) == 0 || turnos >= 2 {
			_, _ = w.Write([]byte(`{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"` + SentinelaDeTexto + ` fim"}}],"usage":{"prompt_tokens":5,"completion_tokens":5}}`))
			return
		}
		tool, args := chamadaDoFalso(corpo, pedido.Tools[0].Function.Name, pedido.Tools[0].Function.Parameters.Properties)
		argumentos, _ := json.Marshal(args)
		texto, _ := json.Marshal(pensamentoDoTurno(turnos))
		// A forma em que a imagem fixada do proxy entrega a resposta da OpenRouter.
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"reasoning_content":` + string(texto) +
			`,"tool_calls":[{"id":"` + idDoTurnoOpenRouter(turnos) + `","type":"function","function":{"name":"` + tool + `","arguments":` + string(argumentos) + `}}],` +
			`"provider_specific_fields":{"reasoning":` + string(texto) + `,"reasoning_details":` + string(detalhesDoTurno(turnos)) + `}}}],"usage":{"prompt_tokens":5,"completion_tokens":5}}`))
	})
	// COM `devolver_em: topo`, O MESMO FORNECEDOR, AGORA EXIGENTE: so aceita o segundo turno com
	// `reasoning_details` no topo e sem o saco. O veredicto e `cumprida`, e a guarda nao dispara.
	exigiu := 0
	exigente := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		corpo, _ := io.ReadAll(r.Body)
		var pedido struct {
			Messages []map[string]json.RawMessage `json:"messages"`
		}
		_ = json.Unmarshal(corpo, &pedido)
		turnos := 0
		for _, m := range pedido.Messages {
			if string(m["role"]) != `"assistant"` || m["tool_calls"] == nil {
				continue
			}
			if m["provider_specific_fields"] != nil || !mesmosValores(m["reasoning_details"], detalhesDoTurno(turnos)) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"estado fora do topo","code":400}}`))
				return
			}
			turnos++
			exigiu++
		}
		r.Body = io.NopCloser(strings.NewReader(string(corpo)))
		tolerante.ServeHTTP(w, r)
	})
	noTopo, err := LerPerfilCandidato([]byte(aos516PerfilOpenRouterEm("openrouter/autor-de-ensaio/modelo-de-ensaio", aos516ParamsReasoning, "obrigatorio", "topo")))
	if err != nil {
		t.Fatal(err)
	}
	rt := aos516CorrerContra(t, noTopo, exigente, amostras)
	aos516Veredicto(t, rt, QualificacaoCumprida)
	if dt := rt.Taxas.Devolucao; dt == nil || dt.TurnosSoNoSaco != 0 || dt.AceitesPeloFornecedor != 2*amostras || exigiu != 3*amostras {
		t.Fatalf("com devolver_em topo: devolucao = %+v, turnos conferidos %d (quero %d)", dt, exigiu, 3*amostras)
	}
	if e := rt.Protocolo.Estado; e == nil || e.DevolverEm != "topo" {
		t.Errorf("o relatorio nao diz onde o estado volta: %+v", e)
	}
	// E o mesmo fornecedor exigente, com o perfil SEM `devolver_em`, recusa: e o controlo.
	if rc := aos516CorrerContra(t, perfil, exigente, amostras); rc.Qualificacao == nil || rc.Qualificacao.Veredicto != QualificacaoNaoCumprida {
		t.Fatalf("controlo sem devolver_em contra o exigente: %+v", rc.Qualificacao)
	}

	r := aos516CorrerContra(t, perfil, tolerante, amostras)
	d := r.Taxas.Devolucao
	if d == nil || d.TurnosComRaciocinioCapturado != 2*amostras || d.TurnosSoNoSaco != 2*amostras || d.AceitesPeloFornecedor != 2*amostras {
		t.Fatalf("devolucao = %+v; quer %d turnos com raciocinio, todos so no saco, e todos os pedidos com 2xx", d, 2*amostras)
	}
	aos516Veredicto(t, r, QualificacaoInconclusiva, RazaoEstadoSoNoSaco)
	if !strings.Contains(ResumoEmTexto(r), "so veio no saco do proxy") {
		t.Error("o resumo em texto nao avisa que o estado so voltou no saco do proxy")
	}
	// Com o mesmo campo TAMBEM em `message`, o saco nao conta: o campo volta onde o fornecedor o le.
	if estadoSoNoSaco(&port.ProviderState{Fields: []port.ProviderStateField{
		{Where: port.StateWhereMessage, Name: "reasoning_details"}, {Where: port.StateWherePSF, Name: "reasoning_details"},
	}}) || !estadoSoNoSaco(&port.ProviderState{Fields: []port.ProviderStateField{
		{Where: port.StateWhereMessage, Name: "reasoning_content"}, {Where: port.StateWherePSF, Name: "reasoning_details"},
	}}) || estadoSoNoSaco(nil) || estadoSoNoSaco(&port.ProviderState{Misaligned: true}) {
		t.Error("estadoSoNoSaco: so conta um campo que venha no saco e nao em message")
	}
}

// OS CABEÇALHOS DO ADAPTADOR. O falso diz se o valor é o da omissão do LiteLLM ou outro, sem o
// guardar; e o contentor do proxy não recebe do banco nenhuma variável de onde esse valor pudesse
// vir (o adaptador lê-o de `OR_SITE_URL` e `OR_APP_NAME`).
func TestAOS516_OpenRouter_CabecalhosDoAdaptador(t *testing.T) {
	falso := &FalsoDeEstado{FormaDoEstado: FormaOpenRouter}
	srv := httptest.NewServer(falso)
	defer srv.Close()
	pedir := func(referer, titulo string) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"x"}]}`))
		if referer != "" {
			req.Header.Set("HTTP-Referer", referer)
		}
		if titulo != "" {
			req.Header.Set("X-Title", titulo)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	pedir("https://litellm.ai", "liteLLM")
	pedir("https://posto-do-dono.exemplo.test/segredo", "aplicacao-local")
	pedir("", "")
	aos516Contagens(t, "parametros", falso.Forma().Parametros, map[string]int{
		"cabecalho:http-referer": 2, "cabecalho:http-referer=" + ValorDoLiteLLM: 1, "cabecalho:http-referer=" + OutroValor: 1,
		"cabecalho:x-title": 2, "cabecalho:x-title=" + ValorDoLiteLLM: 1, "cabecalho:x-title=" + OutroValor: 1,
	})
	if cru := string(mustJSON(t, falso.Forma())); strings.Contains(cru, "posto-do-dono") || strings.Contains(cru, "aplicacao-local") {
		t.Errorf("a forma leva o valor de um cabecalho: %s", cru)
	}
	// O contentor do proxy so recebe as tres variaveis do banco, pelo nome.
	args, ambiente := arranqueDoContentorDoProxy("c", "r", "mestra", "chave", "http://base", time.Hour, time.Minute)
	for _, a := range append(append([]string(nil), args...), ambiente...) {
		if strings.Contains(a, "OR_SITE_URL") || strings.Contains(a, "OR_APP_NAME") || strings.Contains(a, "OPENROUTER_") {
			t.Errorf("o contentor do proxy recebe uma variavel do adaptador openrouter: %q", a)
		}
	}
	if len(ambiente) != 3 {
		t.Errorf("o contentor do proxy recebe %d variaveis do banco; quero 3", len(ambiente))
	}
}
