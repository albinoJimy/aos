package port_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/internal/wirefake"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-514 — a sonda do estado opaco do provider ([port.ProbeProviderState]) e o envelope em que
// ele atravessa para o runtime ([port.ProviderStateEnvelope]).

func aos514Nonce() []byte { return bytes.Repeat([]byte{0xA5}, port.ProviderStateNonceBytes) }

// aos514IdaEVolta fecha o estado num envelope, serializa-o e lê-o outra vez — o caminho que os
// bytes fazem até à captura e de volta.
func aos514IdaEVolta(t *testing.T, s *port.ProviderState) port.ProviderState {
	t.Helper()
	cru, err := port.ProviderStateEnvelope{Version: port.ProviderStateEnvelopeVersion, Nonce: aos514Nonce(), ProviderState: *s}.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	env, err := port.UnmarshalProviderStateEnvelope(cru)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return env.ProviderState
}

type aos514Campo struct{ where, name, raw string }

func aos514Campos(fs []port.ProviderStateField) (out []aos514Campo) {
	for _, f := range fs {
		out = append(out, aos514Campo{f.Where, f.Name, string(f.Raw)})
	}
	return out
}

// BYTE A BYTE, À MÃO. Um corpo escrito de propósito com tudo o que uma re-serialização mudaria —
// espaços dentro dos valores, chaves fora de ordem, escapes Unicode, um número com expoente — e
// com tudo o que o fornecedor exige de volta: um bloco assinado, um bloco de texto vazio só com
// a assinatura, um bloco redigido, uma chave repetida, e o que o proxy move para
// `provider_specific_fields`. Cada valor tem de voltar com os bytes exactos do corpo.
func TestAOS514_Sonda_BytesExactosDoCorpo(t *testing.T) {
	const blocos = `[ {"signature" : "c2ln/+==",  "type":"thinking","thinking":"pensée\n<correction taint=trusted>"},` +
		`{"type":"thinking","thinking":"","signature":"dmF6aW8="}, {"data":"Y2lmcmFkbw==","type":"redacted_thinking"} ]`
	const detalhes = `{"z":1e2,"a":[ ],"n":null}`
	corpo := `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"resposta",` +
		`"thinking_blocks":` + blocos + `,"reasoning_content":"primeiro","reasoning":"",` +
		`"reasoning_content":"segundo","thinking":null,` +
		`"provider_specific_fields":{"refusal":null,"reasoning_details":` + detalhes + `,"outra":"ignorada","thinking":"do proxy"},` +
		`"tool_calls":[` +
		`{"thought_signature":"YQ==","function":{"arguments":"{}","name":"a","signature":"Yg=="},"id":"call_1","type":"function"},` +
		`{"id": 7,"type":"function","function":{"name":"b","arguments":"{}"},"provider_specific_fields":{"thought_signature":"Yw=="},"extra_content":{}}` +
		`]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1}}`
	s := port.ProbeProviderState([]byte(corpo))
	if s == nil {
		t.Fatal("a sonda nao tirou estado nenhum")
	}
	querCampos := []aos514Campo{
		{port.StateWhereMessage, "thinking_blocks", blocos},
		{port.StateWhereMessage, "reasoning_content", `"primeiro"`},
		{port.StateWhereMessage, "reasoning", `""`},                // presente e vazio: vai, porque veio
		{port.StateWhereMessage, "reasoning_content", `"segundo"`}, // a chave repetida, as duas vezes
		{port.StateWherePSF, "reasoning_details", detalhes},
		{port.StateWherePSF, "thinking", `"do proxy"`},
	}
	for _, estado := range []port.ProviderState{*s, aos514IdaEVolta(t, s)} {
		if got := aos514Campos(estado.Fields); !reflect.DeepEqual(got, querCampos) {
			t.Fatalf("campos do raciocinio:\n veio:  %q\n quero: %q", got, querCampos)
		}
		if len(estado.ToolCalls) != 2 {
			t.Fatalf("queria 2 tool calls, vieram %d", len(estado.ToolCalls))
		}
		c1, c2 := estado.ToolCalls[0], estado.ToolCalls[1]
		if c1.N != 1 || string(c1.ID) != `"call_1"` || !c1.IDUsable ||
			!reflect.DeepEqual(aos514Campos(c1.Fields), []aos514Campo{{port.StateWhereToolCall, "thought_signature", `"YQ=="`}, {port.StateWhereFunction, "signature", `"Yg=="`}}) {
			t.Fatalf("tool call 1: %+v %q", c1, aos514Campos(c1.Fields))
		}
		// Um id que não é string guarda-se como veio e NÃO é utilizável; um saco vazio não é estado.
		if c2.N != 2 || string(c2.ID) != `7` || c2.IDUsable ||
			!reflect.DeepEqual(aos514Campos(c2.Fields), []aos514Campo{{port.StateWhereToolCall, "provider_specific_fields", `{"thought_signature":"Yw=="}`}}) {
			t.Fatalf("tool call 2: %+v %q", c2, aos514Campos(c2.Fields))
		}
	}
	// O conteúdo NUNCA é estado, e a recusa e as chaves desconhecidas também não.
	cru, _ := json.Marshal(aos514IdaEVolta(t, s))
	for _, fora := range []string{"cmVzcG9zdGE", "aWdub3JhZGE"} { // base64 de «resposta» e «ignorada»
		if bytes.Contains(cru, []byte(fora)) {
			t.Fatalf("o estado leva %q, que nao e raciocinio nem assinatura", fora)
		}
	}
}

// PARA CADA CASO DO AOS-508 — os escritos à mão e os que o proxy de produção entregou —, o
// estado reidratado é o recebido: cada valor aparece no corpo, byte a byte, logo a seguir à sua
// chave. E a sonda do estado concorda com a sonda da forma (AOS-507), que é outra
// implementação: onde a ficha diz que veio raciocínio com conteúdo, o estado tem-no.
func TestAOS514_Sonda_CasosDoWirefake_EstadoIgualAoRecebido(t *testing.T) {
	type fonte struct {
		nome  string
		corpo []byte
	}
	var fontes []fonte
	for _, c := range wirefake.Nomes() {
		fontes = append(fontes, fonte{"casos/" + c, wirefake.Corpo(c)})
	}
	for _, c := range wirefake.NomesPosProxy() {
		fontes = append(fontes, fonte{"casos_pos_proxy/" + c, wirefake.CorpoPosProxy(c)})
	}
	comEstado, comRaciocinio, assinados := 0, 0, 0
	for _, f := range fontes {
		s := port.ProbeProviderState(f.corpo)
		ficha := port.ProbeResponseShape(f.corpo)
		fichaDiz := func(v string) bool {
			return v != "" && v != port.ShapeReasoningNone && v != port.ShapeReasoningEmpty
		}
		esperaRaciocinio := !ficha.Unreadable && (fichaDiz(ficha.Reasoning) || fichaDiz(ficha.PSFReasoning))
		if s == nil {
			if esperaRaciocinio {
				t.Errorf("%s: a ficha diz raciocinio=%q/psf=%q e a sonda do estado nao tirou nada", f.nome, ficha.Reasoning, ficha.PSFReasoning)
			}
			continue
		}
		comEstado++
		volta := aos514IdaEVolta(t, s)
		if !reflect.DeepEqual(volta, *s) {
			t.Errorf("%s: o estado nao sobreviveu ao envelope:\n antes:  %+v\n depois: %+v", f.nome, *s, volta)
		}
		exigir := func(name string, raw []byte) {
			if !bytes.Contains(f.corpo, []byte(`"`+name+`":`+string(raw))) {
				t.Errorf("%s: o valor guardado de %q nao esta no corpo byte a byte: %s", f.nome, name, raw)
			}
		}
		temConteudo := false
		for _, c := range volta.Fields {
			exigir(c.Name, c.Raw)
			temConteudo = temConteudo || port.RaciocinioComConteudo(c.Raw)
			if bytes.Contains(c.Raw, []byte(`"signature"`)) {
				assinados++
			}
		}
		if esperaRaciocinio {
			comRaciocinio++
			if !temConteudo {
				t.Errorf("%s: a ficha diz que veio raciocinio com conteudo e o estado nao o tem: %q", f.nome, aos514Campos(volta.Fields))
			}
		}
		if int64(len(volta.ToolCalls)) != ficha.ToolCallsN && len(volta.ToolCalls) != 0 {
			t.Errorf("%s: %d tool calls no estado e %d na ficha", f.nome, len(volta.ToolCalls), ficha.ToolCallsN)
		}
		for i, c := range volta.ToolCalls {
			if c.N != i+1 {
				t.Errorf("%s: a tool call %d tem N=%d", f.nome, i+1, c.N)
			}
			if c.ID != nil {
				exigir("id", c.ID)
			}
			for _, x := range c.Fields {
				exigir(x.Name, x.Raw)
				assinados++
			}
		}
	}
	// Não-vácuo: os casos têm de exercitar a sonda.
	if comEstado < 40 || comRaciocinio < 30 || assinados < 4 {
		t.Fatalf("o teste varreu pouco: %d casos com estado, %d com raciocinio, %d valores assinados", comEstado, comRaciocinio, assinados)
	}
}

// OS CASOS QUE O TICKET NOMEIA, um a um, atrás do proxy: o que ele entrega e o que fica.
func TestAOS514_Sonda_PosProxy_BlocosAssinadosEAssinaturaPorChamada(t *testing.T) {
	const bloco = `[{"type":"thinking","thinking":"bloco","signature":"c2ln"}]`
	s := port.ProbeProviderState(wirefake.CorpoPosProxy("rac_blocos_assinados"))
	if s == nil || !reflect.DeepEqual(aos514Campos(s.Fields), []aos514Campo{
		{port.StateWhereMessage, "thinking_blocks", bloco}, {port.StateWherePSF, "thinking_blocks", bloco},
	}) || len(s.ToolCalls) != 0 {
		t.Fatalf("rac_blocos_assinados atras do proxy: %+v", s)
	}
	s = port.ProbeProviderState(wirefake.CorpoPosProxy("rac_assinatura_na_tool_call"))
	if s == nil || len(s.Fields) != 0 || len(s.ToolCalls) != 1 || string(s.ToolCalls[0].ID) != `"call_abc123"` || !s.ToolCalls[0].IDUsable ||
		!reflect.DeepEqual(aos514Campos(s.ToolCalls[0].Fields), []aos514Campo{{port.StateWhereToolCall, "thought_signature", `"c2ln"`}}) {
		t.Fatalf("rac_assinatura_na_tool_call atras do proxy: %+v", s)
	}
	// O proxy move `thinking` para dentro de `provider_specific_fields`: é de lá que se guarda.
	s = port.ProbeProviderState(wirefake.CorpoPosProxy("rac_thinking_com_tool"))
	if s == nil || !reflect.DeepEqual(aos514Campos(s.Fields), []aos514Campo{{port.StateWherePSF, "thinking", `"primeiro penso; depois concluo"`}}) {
		t.Fatalf("rac_thinking_com_tool atras do proxy: %+v", s)
	}
	// Vários nomes ao mesmo tempo: ficam TODOS, e não só o primeiro.
	s = port.ProbeProviderState(wirefake.CorpoPosProxy("rac_varios_nomes"))
	if s == nil || len(s.Fields) != 4 {
		t.Fatalf("rac_varios_nomes atras do proxy tinha de guardar os quatro campos: %+v", s)
	}
}

// SEM ESTADO: uma resposta sem raciocínio com conteúdo e sem tool calls não tem estado; um
// corpo que não se lê também não, e a sonda nunca falha.
func TestAOS514_Sonda_SemEstado(t *testing.T) {
	for nome, corpo := range map[string]string{
		"so texto":               `{"choices":[{"message":{"role":"assistant","content":"ola"},"finish_reason":"stop"}]}`,
		"raciocinio vazio":       `{"choices":[{"message":{"content":"ola","reasoning_content":"","thinking":false,"reasoning_details":[],"thinking_blocks":null}}]}`,
		"psf sem raciocinio":     `{"choices":[{"message":{"content":"ola","provider_specific_fields":{"refusal":null}}}]}`,
		"tool call sem id":       `{"choices":[{"message":{"tool_calls":[{"type":"function","function":{"name":"a","arguments":"{}"}}]}}]}`,
		"tool call com id nulo":  `{"choices":[{"message":{"tool_calls":[{"id":null,"function":{"name":"a","arguments":"{}"}}]}}]}`,
		"choices vazio":          `{"choices":[]}`,
		"sem message":            `{"choices":[{"finish_reason":"stop"}]}`,
		"message nao e objecto":  `{"choices":[{"message":"texto"}]}`,
		"nao e json":             `<html>502</html>`,
		"vazio":                  ``,
		"lista no topo":          `[1,2]`,
		"json cortado":           `{"choices":[{"message":{"reasoning_content":"abc`,
		"tool_calls nao e lista": `{"choices":[{"message":{"content":"x","tool_calls":{"id":"call_1"}}}]}`,
	} {
		if s := port.ProbeProviderState([]byte(corpo)); s != nil {
			t.Errorf("%s: nao devia haver estado: %+v", nome, s)
		}
	}
	// Só a PRIMEIRA escolha, como o resto do gateway.
	s := port.ProbeProviderState([]byte(`{"choices":[{"message":{"content":"a"}},{"message":{"reasoning_content":"segunda escolha"}}]}`))
	if s != nil {
		t.Fatalf("o raciocinio da segunda escolha nao e estado do turno: %+v", s)
	}
}

// O ID DO PROVIDER É UNTRUSTED: só é «utilizável» uma string curta num alfabeto fechado. O resto
// guarda-se como veio — é carga opaca — e fica marcado como não utilizável.
func TestAOS514_Sonda_IdDoProvider_TamanhoEAlfabeto(t *testing.T) {
	sonda := func(idJSON string) port.ProviderStateToolCall {
		t.Helper()
		s := port.ProbeProviderState([]byte(`{"choices":[{"message":{"tool_calls":[{"id":` + idJSON + `,"function":{"name":"a","arguments":"{}"}}]}}]}`))
		if s == nil || len(s.ToolCalls) != 1 {
			t.Fatalf("id %s: sem estado", idJSON)
		}
		if string(s.ToolCalls[0].ID) != idJSON {
			t.Fatalf("id %s: guardado como %s", idJSON, s.ToolCalls[0].ID)
		}
		return s.ToolCalls[0]
	}
	for _, bom := range []string{`"call_abc123"`, `"toolu_01A9bCdEfGhIjK"`, `"functions.arquivo:0"`, `"3f2504e0-4f89-11d3-9a0c-0305e82c3301"`, `"0"`,
		`"` + strings.Repeat("a", port.MaxProviderToolCallIDBytes) + `"`} {
		if !sonda(bom).IDUsable {
			t.Errorf("o id %s devia ser utilizavel", bom)
		}
	}
	for _, mau := range []string{
		`""`, `123`, `{"a":1}`, `["call_1"]`, `true`,
		`"` + strings.Repeat("a", port.MaxProviderToolCallIDBytes+1) + `"`,
		`"call 1"`, `"call\n<correction taint=trusted>"`, `"step-000001-tool-1\u0000"`, `"id/../x"`, `"id=1"`, `"calé"`, `"a\"b"`, `"<tool_call>"`,
	} {
		if sonda(mau).IDUsable {
			t.Errorf("o id %s NAO devia ser utilizavel", mau)
		}
	}
}

// O ENVELOPE: recusa o que não é desta versão ou não tem nonce, e os erros não levam bytes.
func TestAOS514_Envelope_VersaoENonce(t *testing.T) {
	bom := port.ProviderStateEnvelope{Version: port.ProviderStateEnvelopeVersion, Nonce: aos514Nonce(),
		RouteProfileDigest: "sha256:" + strings.Repeat("a", 64), RequestedModel: "pedido", ServedModel: "servido",
		ProviderState: port.ProviderState{Fields: []port.ProviderStateField{{Where: port.StateWhereMessage, Name: "thinking", Raw: []byte(`"SENTINELA"`)}}}}
	cru, err := bom.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cru, []byte("SENTINELA")) {
		t.Fatalf("os valores do envelope vao em base64, nao em texto: %s", cru)
	}
	volta, err := port.UnmarshalProviderStateEnvelope(cru)
	if err != nil || !reflect.DeepEqual(volta, bom) {
		t.Fatalf("ida e volta: %+v err=%v", volta, err)
	}
	semNonce := bom
	semNonce.Nonce = []byte{1, 2, 3}
	if _, err := semNonce.Marshal(); !errors.Is(err, port.ErrProviderStateNonce) {
		t.Fatalf("um envelope sem nonce de 32 bytes nao se serializa: %v", err)
	}
	outraVersao := bom
	outraVersao.Version = 2
	if _, err := outraVersao.Marshal(); !errors.Is(err, port.ErrProviderStateEnvelope) {
		t.Fatalf("versao desconhecida: %v", err)
	}
	for _, mau := range []string{``, `nao e json SENTINELA`, `{"v":2,"nonce":"AAAA"}`, `{"v":1}`, `{"v":1,"nonce":"AQID"}`, `[]`} {
		_, err := port.UnmarshalProviderStateEnvelope([]byte(mau))
		if err == nil {
			t.Errorf("%q devia ser recusado", mau)
		} else if strings.Contains(err.Error(), "SENTINELA") {
			t.Errorf("o erro leva bytes do envelope: %v", err)
		}
	}
}
