package modelgateway_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-516 — O PERFIL DA ROTA DECLARA ONDE O ESTADO VOLTA (`devolver_em`) E COMO O RACIOCÍNIO SE
// PEDE (`params.reasoning`). Contrato da porta 1.10.0.
//
// O provider destes testes é o que se MEDIU atrás da imagem fixada do proxy numa rota de um
// agregador (2026-10-10): a resposta chega ao gateway com `reasoning_content` em `message` e com
// `reasoning` e `reasoning_details` dentro de `message.provider_specific_fields`; e o fornecedor
// só lê `reasoning_details` no TOPO da mensagem `assistant` que lhe volta.

// aos516Detalhes são os bytes dos `reasoning_details` do turno n, com espaços, `<`, `&` e um
// escape: nada disto sobrevive a um codificador, e tudo tem de voltar igual.
func aos516Detalhes(n int) []byte {
	return []byte(fmt.Sprintf(`[ {"type" : "reasoning.text","text":"S-PENSA-%d <b> & \u00e9" ,  "signature":"U0lH-%d+/==","index":0} ]`, n, n))
}

// aos516Agregador faz de proxy mais fornecedor: emite o estado no saco, e exige-o no topo.
type aos516Agregador struct {
	Turnos int

	mu      sync.Mutex
	corpos  [][]byte
	recusas []string
}

func (a *aos516Agregador) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	corpo, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.corpos = append(a.corpos, corpo)
	var pedido struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal(corpo, &pedido)
	turnos, causa := 0, ""
	for _, m := range pedido.Messages {
		var chamadas []json.RawMessage
		_ = json.Unmarshal(m["tool_calls"], &chamadas)
		if string(m["role"]) != `"assistant"` || len(chamadas) == 0 {
			continue
		}
		switch {
		case m["provider_specific_fields"] != nil:
			causa = "o fornecedor nao conhece provider_specific_fields"
		case m["reasoning_details"] == nil:
			causa = "reasoning_details em falta no topo da mensagem"
		case !bytes.Equal(m["reasoning_details"], aos516Detalhes(turnos)):
			causa = "reasoning_details alterado"
		}
		turnos++
	}
	w.Header().Set("Content-Type", "application/json")
	if causa != "" {
		a.recusas = append(a.recusas, causa)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"` + causa + `","code":400}}`))
		return
	}
	if turnos >= a.Turnos {
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"resumo final"}}],"usage":{"prompt_tokens":5,"completion_tokens":5}}`))
		return
	}
	_, _ = fmt.Fprintf(w, `{"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"reasoning_content":"S-RACIOCINIO-%d",`+
		`"tool_calls":[{"id":"toolu_%02d","type":"function","function":{"name":"doc_read","arguments":"{\"doc_id\":\"notas\"}"}}],`+
		`"provider_specific_fields":{"reasoning":"S-RACIOCINIO-%d","reasoning_details":%s}}}],"usage":{"prompt_tokens":5,"completion_tokens":5}}`,
		turnos, turnos, turnos, aos516Detalhes(turnos))
}

func (a *aos516Agregador) visto() (corpos [][]byte, recusas []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([][]byte(nil), a.corpos...), append([]string(nil), a.recusas...)
}

// O CAMINHO VERDE, COM O CONTROLO AO LADO. Com `devolver_em: topo` o fornecedor que só lê o
// estado no topo aceita três turnos seguidos; o segundo pedido leva os `reasoning_details` no
// topo, byte a byte, e não leva o saco. Com a omissão — o comportamento de sempre — o MESMO
// fornecedor recusa o segundo pedido: o estado foi, mas dentro do saco.
func TestAOS516_DevolverEm_TopoOFornecedorAceitaEAOmissaoNao(t *testing.T) {
	for nome, extra := range map[string]string{
		"obrigatorio": `,"devolver":"obrigatorio","devolver_em":"topo"`,
		"opcional":    `,"devolver":"opcional","devolver_em":"topo"`,
	} {
		t.Run(nome, func(t *testing.T) {
			falso := &aos516Agregador{Turnos: 3}
			run := aos515Compor(t, falso, aos515Modelo, aos515Rota(t, "gpt-4o", extra))
			for i := 1; i <= 4; i++ {
				out, err := run.passo("gpt-4o")
				if err != nil {
					t.Fatalf("turno %d: %v", i, err)
				}
				aos515SemSentinelas(t, "texto do turno", []byte(out.Text))
			}
			corpos, recusas := falso.visto()
			if len(corpos) != 4 || len(recusas) != 0 {
				t.Fatalf("pedidos %d, recusas %v", len(corpos), recusas)
			}
			segundo := string(corpos[1])
			if !strings.Contains(segundo, `"reasoning_details":`+string(aos516Detalhes(0))) || !strings.Contains(segundo, `"reasoning":"S-RACIOCINIO-0"`) || !strings.Contains(segundo, `"reasoning_content":"S-RACIOCINIO-0"`) {
				t.Fatalf("o segundo pedido nao leva o estado no topo, byte a byte:\n%s", segundo)
			}
			if strings.Contains(segundo, "provider_specific_fields") {
				t.Fatalf("o saco voltou:\n%s", segundo)
			}
			// O primeiro pedido nao leva estado nenhum, e o estado nunca e texto de mensagem (D3).
			aos515SemSentinelas(t, "primeiro pedido", corpos[0])
			var pedido struct {
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(corpos[3], &pedido); err != nil {
				t.Fatal(err)
			}
			for i, m := range pedido.Messages {
				aos515SemSentinelas(t, fmt.Sprintf("content da mensagem %d", i), []byte(m.Content))
			}
			if len(run.obs) != 3 {
				t.Fatalf("queria 3 pedidos contados, vieram %d", len(run.obs))
			}
			for _, o := range run.obs {
				if o.Result != modelgateway.StateReturnAll || o.Cause != "" {
					t.Fatalf("contado %+v; todos os pedidos devolveram tudo", o)
				}
			}
		})
	}
	t.Run("controlo: sem devolver_em o mesmo fornecedor recusa", func(t *testing.T) {
		falso := &aos516Agregador{Turnos: 3}
		run := aos515Compor(t, falso, aos515Modelo, aos515Rota(t, "gpt-4o", `,"devolver":"obrigatorio"`))
		if _, err := run.passo("gpt-4o"); err != nil {
			t.Fatal(err)
		}
		if _, err := run.passo("gpt-4o"); err == nil {
			t.Fatal("o segundo pedido tinha de ser recusado pelo fornecedor")
		}
		corpos, recusas := falso.visto()
		if len(recusas) != 1 || !strings.Contains(recusas[0], "provider_specific_fields") {
			t.Fatalf("recusas = %v", recusas)
		}
		// O estado FOI — o gateway armou-o —, mas no sitio de onde veio.
		if !strings.Contains(string(corpos[1]), `"provider_specific_fields":{"reasoning":"S-RACIOCINIO-0","reasoning_details":`+string(aos516Detalhes(0))+`}`) {
			t.Fatalf("a omissao mudou de forma:\n%s", corpos[1])
		}
		if len(run.obs) != 1 || run.obs[0].Result != modelgateway.StateReturnAll {
			t.Fatalf("contado %+v", run.obs)
		}
	})
}

// AS GUARDAS DA ROTA VALEM IGUAL. Com `topo`, o estado de uma rota que não se provou igual à do
// perfil não sai: numa rota `obrigatorio` o segundo pedido NÃO é enviado.
func TestAOS516_DevolverEm_RotaNaoProvadaNaoRecebeOEstado(t *testing.T) {
	falso := &aos516Agregador{Turnos: 3}
	run := aos515Compor(t, falso, "openai/outro-modelo", aos515Rota(t, "gpt-4o", `,"devolver":"obrigatorio","devolver_em":"topo"`))
	if _, err := run.passo("gpt-4o"); err != nil {
		t.Fatal(err)
	}
	_, err := run.passo("gpt-4o")
	var recusa *modelgateway.StateReturnError
	if !errors.As(err, &recusa) {
		t.Fatalf("queria StateReturnError; veio %v", err)
	}
	if corpos, _ := falso.visto(); len(corpos) != 1 {
		t.Fatalf("o fornecedor viu %d pedidos; o segundo nao podia sair", len(corpos))
	}
}

// O SÍTIO É O DO PERFIL, E SÓ O DELE. O que um chamador ponha em [port.MessageState.Placement] é
// sobreposto — nos dois sentidos.
func TestAOS516_DevolverEm_OChamadorNaoEscolheOSitio(t *testing.T) {
	com := aos515Rota(t, "gpt-4o", `,"devolver":"obrigatorio","devolver_em":"topo"`)
	sem := aos515Rota(t, "gpt-4o", `,"devolver":"obrigatorio"`)
	estado := func(perfil modelgateway.RouteProfile, sitio string) *port.MessageState {
		return &port.MessageState{RouteProfileDigest: perfil.Digest(), ServedModel: perfil.ExpectedModel, RouteCheck: port.RouteCheckEqual, Placement: sitio,
			Fields: []port.ProviderStateField{{Where: port.StateWherePSF, Name: "reasoning_details", Raw: []byte(`["x"]`)}}}
	}
	for nome, c := range map[string]struct {
		perfil modelgateway.RouteProfile
		sitio  string
		noTopo bool
	}{
		"perfil topo, chamador origem": {com, port.StatePlacementOrigin, true},
		"perfil origem, chamador topo": {sem, port.StatePlacementTop, false},
		"perfil origem, chamador lixo": {sem, "cabecalho", false},
	} {
		req := port.ChatRequest{Model: "gpt-4o", Messages: []port.Message{
			{Role: port.RoleUser, Content: "x"},
			{Role: port.RoleAssistant, ToolCalls: []port.ToolCall{{ID: "s-tool-1", Type: "function", Function: port.FunctionCall{Name: "f", Arguments: "{}"}}}, State: estado(c.perfil, c.sitio)},
		}}
		wire, err := modelgateway.ArmarEEscreverParaTeste(req, c.perfil)
		if err != nil {
			t.Fatalf("%s: %v", nome, err)
		}
		noTopo := strings.Contains(string(wire), `}}],"reasoning_details":["x"]}`)
		noSaco := strings.Contains(string(wire), `"provider_specific_fields":{"reasoning_details":["x"]}`)
		if noTopo != c.noTopo || noSaco == c.noTopo {
			t.Errorf("%s: no topo %v, no saco %v:\n%s", nome, noTopo, noSaco, wire)
		}
	}
}

// UM NOME DE CAMPO REPETIDO, COM `topo`, TORNA O ESTADO DO TURNO NÃO DEVOLVÍVEL (revisão do
// AOS-516): em `obrigatorio` o pedido não sai, em `opcional` conta-se e segue sem o estado desse
// turno. Com a omissão o mesmo estado é armado, como sempre.
func TestAOS516_DevolverEm_NomeRepetidoNaoEDevolvido(t *testing.T) {
	campo := func(onde, nome, cru string) port.ProviderStateField {
		return port.ProviderStateField{Where: onde, Name: nome, Raw: []byte(cru)}
	}
	repetidos := map[string][]port.ProviderStateField{
		"repetido em message":        {campo(port.StateWhereMessage, "reasoning", `"um"`), campo(port.StateWhereMessage, "reasoning", `"dois"`)},
		"repetido no saco":           {campo(port.StateWherePSF, "reasoning_details", `["um"]`), campo(port.StateWherePSF, "reasoning_details", `["dois"]`)},
		"nos dois, bytes diferentes": {campo(port.StateWhereMessage, "thinking_blocks", `["m"]`), campo(port.StateWherePSF, "thinking_blocks", `["s"]`)},
	}
	pedido := func(perfil modelgateway.RouteProfile, campos []port.ProviderStateField) port.ChatRequest {
		return port.ChatRequest{Model: "gpt-4o", Messages: []port.Message{
			{Role: port.RoleUser, Content: "x"},
			{Role: port.RoleAssistant, ToolCalls: []port.ToolCall{{ID: "s-tool-1", Type: "function", Function: port.FunctionCall{Name: "f", Arguments: "{}"}}},
				State: &port.MessageState{RouteProfileDigest: perfil.Digest(), ServedModel: perfil.ExpectedModel, RouteCheck: port.RouteCheckEqual, Fields: campos}},
		}}
	}
	obrigatorio := aos515Rota(t, "gpt-4o", `,"devolver":"obrigatorio","devolver_em":"topo"`)
	opcional := aos515Rota(t, "gpt-4o", `,"devolver":"opcional","devolver_em":"topo"`)
	origem := aos515Rota(t, "gpt-4o", `,"devolver":"obrigatorio"`)
	for nome, campos := range repetidos {
		_, devolvidos, causa, err := modelgateway.ArmarDevolucaoParaTeste(pedido(obrigatorio, campos), obrigatorio)
		var recusa *modelgateway.StateReturnError
		if !errors.As(err, &recusa) || recusa.Cause != modelgateway.StateCauseRepeatedName || causa != modelgateway.StateCauseRepeatedName || devolvidos != 0 {
			t.Errorf("%s, obrigatorio: err %v, causa %q, devolvidos %d", nome, err, causa, devolvidos)
		}
		turnos, devolvidos, causa, err := modelgateway.ArmarDevolucaoParaTeste(pedido(opcional, campos), opcional)
		if err != nil || turnos != 1 || devolvidos != 0 || causa != modelgateway.StateCauseRepeatedName {
			t.Errorf("%s, opcional: turnos %d, devolvidos %d, causa %q, err %v", nome, turnos, devolvidos, causa, err)
		}
		// Em `opcional` o pedido sai, sem um byte do estado desse turno.
		wire, err := modelgateway.ArmarEEscreverParaTeste(pedido(opcional, campos), opcional)
		if err != nil || strings.Contains(string(wire), "reasoning") || strings.Contains(string(wire), "thinking_blocks") {
			t.Errorf("%s, opcional: o pedido tinha de sair sem o estado: %v %s", nome, err, wire)
		}
		// Com a omissao, o mesmo estado e armado e sai como sempre.
		if _, devolvidos, causa, err := modelgateway.ArmarDevolucaoParaTeste(pedido(origem, campos), origem); err != nil || devolvidos != 1 || causa != "" {
			t.Errorf("%s, origem: devolvidos %d, causa %q, err %v", nome, devolvidos, causa, err)
		}
	}
	// O mesmo nome nos dois sitios com os MESMOS bytes nao e ambiguo: arma-se e escreve-se uma vez.
	iguais := []port.ProviderStateField{campo(port.StateWhereMessage, "thinking_blocks", `["a"]`), campo(port.StateWherePSF, "thinking_blocks", `["a"]`)}
	wire, err := modelgateway.ArmarEEscreverParaTeste(pedido(obrigatorio, iguais), obrigatorio)
	if err != nil || strings.Count(string(wire), `"thinking_blocks":["a"]`) != 1 || strings.Contains(string(wire), "provider_specific_fields") {
		t.Errorf("mesmos bytes nos dois sitios: %v %s", err, wire)
	}
	// A causa nova esta no vocabulario fechado.
	conhecida := false
	for _, c := range modelgateway.StateReturnCauses() {
		conhecida = conhecida || c == modelgateway.StateCauseRepeatedName
	}
	if !conhecida {
		t.Error("a causa nova nao esta no vocabulario fechado")
	}
}

// O PERFIL: `devolver_em` é vocabulário fechado, só tem leitor numa rota que devolve estado, e
// só entra no digest quando declara alguma coisa.
func TestAOS516_Perfil_DevolverEm(t *testing.T) {
	const devolve = aos513Base + `,"projection_version":"1.3.0","devolver":"obrigatorio"`
	omisso := aos513Perfil(t, `{`+devolve+`}`)
	origem := aos513Perfil(t, `{`+devolve+`,"devolver_em":"origem"}`)
	topo := aos513Perfil(t, `{`+devolve+`,"devolver_em":"topo"}`)
	if omisso.StateReturnAt != modelgateway.StateReturnAtOrigin || origem.StateReturnAt != modelgateway.StateReturnAtOrigin || topo.StateReturnAt != modelgateway.StateReturnAtTop {
		t.Fatalf("lidos: %q %q %q", omisso.StateReturnAt, origem.StateReturnAt, topo.StateReturnAt)
	}
	if omisso.Digest() != origem.Digest() {
		t.Error("a omissao escrita por extenso mudou o digest")
	}
	if topo.Digest() == omisso.Digest() {
		t.Error("devolver_em: topo nao entra no digest: dois perfis diferentes com o mesmo digest")
	}
	for nome, doc := range map[string]string{
		"fora do vocabulario":          `{` + devolve + `,"devolver_em":"cabecalho"}`,
		"maiusculas":                   `{` + devolve + `,"devolver_em":"Topo"}`,
		"topo numa rota que nunca":     `{` + aos513Base + `,"devolver_em":"topo"}`,
		"topo com nunca por extenso":   `{` + aos513Base + `,"devolver":"nunca","devolver_em":"topo"}`,
		"topo com a projeccao 1.2.0":   `{` + aos513Base + `,"projection_version":"1.2.0","devolver":"obrigatorio","devolver_em":"topo"}`,
		"topo sem versao de projeccao": `{` + aos513Base + `,"devolver":"opcional","devolver_em":"topo"}`,
		"tipo errado":                  `{` + devolve + `,"devolver_em":true}`,
		"chave quase igual":            `{` + devolve + `,"devolver_onde":"topo"}`,
	} {
		if _, err := modelgateway.ParseRouteProfile([]byte(doc)); !errors.Is(err, modelgateway.ErrBadRouteProfile) {
			t.Errorf("%s: tinha de ser recusado; deu %v", nome, err)
		}
	}
	// A forma escrita so vale na leitura: num perfil em codigo, a omissao e o campo vazio.
	p := topo
	p.StateReturnAt = modelgateway.StateReturnAtOriginName
	if err := p.Validate(); !errors.Is(err, modelgateway.ErrBadRouteProfile) {
		t.Errorf("a forma escrita num perfil em codigo: %v", err)
	}
}

// O PERFIL: `params.reasoning` lê-se pela leitura fechada, vai no pedido à rota e no manifesto,
// e não se declara ao lado das outras formas.
func TestAOS516_Perfil_Reasoning(t *testing.T) {
	esforco := aos513Perfil(t, `{`+aos513Base+`,"params":{"reasoning":{"effort":"medium"},"max_tokens":16000}}`)
	if esforco.Params == nil || esforco.Params.Reasoning == nil || esforco.Params.Reasoning.Effort != "medium" {
		t.Fatalf("lido: %+v", esforco.Params)
	}
	semParams := aos513Perfil(t, `{`+aos513Base+`}`)
	if esforco.Digest() == semParams.Digest() || esforco.Digest() == aos513Perfil(t, `{`+aos513Base+`,"params":{"reasoning_effort":"medium","max_tokens":16000}}`).Digest() {
		t.Error("o reasoning nao distingue o digest do perfil")
	}
	for nome, doc := range map[string]string{
		"chave desconhecida no reasoning": `{` + aos513Base + `,"params":{"reasoning":{"effort":"medium","exclude":true}}}`,
		"reasoning vazio":                 `{` + aos513Base + `,"params":{"reasoning":{}}}`,
		"os dois campos":                  `{` + aos513Base + `,"params":{"reasoning":{"effort":"low","max_tokens":2048}}}`,
		"effort fora do vocabulario":      `{` + aos513Base + `,"params":{"reasoning":{"effort":"maximo"}}}`,
		"ao lado de thinking":             `{` + aos513Base + `,"params":{"reasoning":{"effort":"low"},"thinking":{"type":"enabled"}}}`,
		"ao lado de reasoning_effort":     `{` + aos513Base + `,"params":{"reasoning":{"effort":"low"},"reasoning_effort":"low"}}`,
		"tipo errado":                     `{` + aos513Base + `,"params":{"reasoning":"medium"}}`,
		"chave em maiusculas":             `{` + aos513Base + `,"params":{"reasoning":{"Effort":"low"}}}`,
	} {
		if _, err := modelgateway.ParseRouteProfile([]byte(doc)); !errors.Is(err, modelgateway.ErrBadRouteProfile) {
			t.Errorf("%s: tinha de ser recusado; deu %v", nome, err)
		}
	}
	// No pedido que sai: o parametro do perfil, no fim, e nada do que o chamador la pos.
	falso := &aos516Agregador{Turnos: 0}
	run := aos515Compor(t, falso, aos515Modelo, esforco)
	if _, err := run.passo("gpt-4o"); err != nil {
		t.Fatal(err)
	}
	corpos, _ := falso.visto()
	if len(corpos) != 1 || !bytes.HasSuffix(corpos[0], []byte(`,"max_tokens":16000,"reasoning":{"effort":"medium"}}`)) {
		t.Fatalf("o pedido nao leva o reasoning do perfil no fim:\n%s", corpos)
	}
	for _, proibido := range []string{`"thinking"`, `"reasoning_effort"`} {
		if bytes.Contains(corpos[0], []byte(proibido)) {
			t.Errorf("o pedido leva %s sem o perfil o declarar", proibido)
		}
	}
}

// A INÉRCIA. Os digests dos perfis de antes do AOS-516 são os de antes: os quatro da tabela
// (presos em aos513Digests), e dois perfis candidatos que já declaravam tudo o que havia
// para declarar — calculados no commit anterior a esta alteração.
func TestAOS516_Inercia_OsDigestsDeAntes(t *testing.T) {
	for _, p := range modelgateway.RouteProfiles() {
		if quer := aos513Digests[p.Requested]; p.Digest() != quer || p.StateReturnAt != modelgateway.StateReturnAtOrigin {
			t.Errorf("%s: digest %s, quero %s", p.Requested, p.Digest(), quer)
		}
	}
	for doc, quer := range map[string]string{
		`{"requested":"rota-de-ensaio","expected_model":"anthropic/PREENCHER-NOME-DO-MODELO","wire_class":"openai-chat-completions","capabilities":["tools"],"params":{"thinking":{"type":"enabled","budget_tokens":2048},"max_tokens":16000},"projection_version":"1.3.0","devolver":"obrigatorio"}`: "sha256:b27665e779dc8cbc70339a7e66d016bdabaccd11da549ef3637435e71117769f",
		`{"requested":"r","expected_model":"openrouter/a/m","wire_class":"openai-chat-completions","capabilities":["tools"],"params":{"reasoning_effort":"medium","max_tokens":16000},"projection_version":"1.3.0","devolver":"opcional","tool_call_id":"provider"}`:                                  "sha256:6717a0159a8e2138c181565cd534df8dbcee91a948eb45f854f53a31121c93a7",
	} {
		if p := aos513Perfil(t, doc); p.Digest() != quer {
			t.Errorf("o digest de um perfil de antes mudou: %s, quero %s", p.Digest(), quer)
		}
	}
}

// O ESTADO CAPTURADO COM O PERFIL EM `origem` NÃO VOLTA DEPOIS DE O PERFIL PASSAR A `topo`.
// `devolver_em` entra no digest do perfil, e o envelope gravou o digest de antes: é o estado de
// OUTRA rota. Em `obrigatorio` o pedido seguinte não sai; em `opcional` sai sem estado.
func TestAOS516_DevolverEm_PerfilMudadoAMeioERotaDiferente(t *testing.T) {
	for _, classe := range []string{"obrigatorio", "opcional"} {
		antes := aos515Rota(t, "gpt-4o", `,"devolver":"`+classe+`"`)
		depois := aos515Rota(t, "gpt-4o", `,"devolver":"`+classe+`","devolver_em":"topo"`)
		if antes.Digest() == depois.Digest() {
			t.Fatal("os dois perfis tem o mesmo digest")
		}
		falso := &aos516Agregador{Turnos: 3}
		run := aos515Compor(t, falso, aos515Modelo, antes)
		if _, err := run.passo("gpt-4o"); err != nil {
			t.Fatal(err)
		}
		// O MESMO run — os mesmos segmentos e os mesmos envelopes — numa composicao com o perfil mudado.
		gravador := &aos513Provider{}
		outro := aos515Compor(t, gravador, aos515Modelo, depois)
		outro.segs, outro.estados, outro.turno = append([]agentruntime.TailSegment(nil), run.segs...), run.estados, run.turno
		_, err := outro.passo("gpt-4o")
		var recusa *modelgateway.StateReturnError
		if classe == "obrigatorio" {
			if !errors.As(err, &recusa) || recusa.Cause != modelgateway.StateCauseOtherRoute || len(gravador.pedidos()) != 0 {
				t.Fatalf("obrigatorio: queria a recusa por outra rota e nenhum pedido; veio %v (%d pedidos)", err, len(gravador.pedidos()))
			}
			continue
		}
		if err != nil || len(gravador.pedidos()) != 1 {
			t.Fatalf("opcional: o pedido sai sem estado; veio %v (%d pedidos)", err, len(gravador.pedidos()))
		}
		aos515SemSentinelas(t, "pedido com o perfil mudado", gravador.pedidos()[0])
		if len(outro.obs) != 1 || outro.obs[0].Result != modelgateway.StateReturnNone || outro.obs[0].Cause != modelgateway.StateCauseOtherRoute {
			t.Fatalf("opcional: contado %+v", outro.obs)
		}
	}
}

// REPRODUÇÃO A PARTIR DA CAPTURA, COM `topo`. Os segmentos do tail e os envelopes por digest dão,
// numa composição nova, os MESMOS pedidos, byte a byte — com o estado no topo e sem o saco.
func TestAOS516_DevolverEm_Replay_OsPedidosReproduzemSeByteAByte(t *testing.T) {
	perfil := aos515Rota(t, "gpt-4o", `,"devolver":"obrigatorio","devolver_em":"topo","params":{"reasoning":{"effort":"medium"},"max_tokens":16000}`)
	falso := &aos516Agregador{Turnos: 3}
	run := aos515Compor(t, falso, aos515Modelo, perfil)
	var cortes []int
	for i := 1; i <= 4; i++ {
		cortes = append(cortes, len(run.segs))
		if _, err := run.passo("gpt-4o"); err != nil {
			t.Fatal(err)
		}
	}
	originais, recusas := falso.visto()
	if len(originais) != 4 || len(recusas) != 0 {
		t.Fatalf("pedidos %d, recusas %v", len(originais), recusas)
	}
	// A «captura»: os envelopes passam por JSON, como na captura do turno, e voltam.
	gravado, err := json.Marshal(run.estados)
	if err != nil {
		t.Fatal(err)
	}
	var estados map[string][]byte
	if err := json.Unmarshal(gravado, &estados); err != nil {
		t.Fatal(err)
	}
	for turno := 2; turno <= 4; turno++ {
		gravador := &aos513Provider{}
		outro := aos515Compor(t, gravador, aos515Modelo, perfil)
		outro.segs, outro.estados, outro.turno = append([]agentruntime.TailSegment(nil), run.segs[:cortes[turno-1]]...), estados, turno-1
		if _, err := outro.passo("gpt-4o"); err != nil {
			t.Fatalf("turno %d reproduzido: %v", turno, err)
		}
		got := gravador.pedidos()[0]
		if !bytes.Equal(got, originais[turno-1]) {
			t.Fatalf("turno %d: o pedido reproduzido diverge do original:\n veio:  %s\n quero: %s", turno, got, originais[turno-1])
		}
		if !bytes.Contains(got, append([]byte(`"reasoning_details":`), aos516Detalhes(turno-2)...)) || bytes.Contains(got, []byte("provider_specific_fields")) {
			t.Fatalf("turno %d: o pedido reproduzido nao leva o estado no topo, ou leva o saco:\n%s", turno, got)
		}
	}
}
