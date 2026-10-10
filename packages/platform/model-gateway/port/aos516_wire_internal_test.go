package port

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// AOS-516 — o contrato 1.10.0: ONDE volta o estado que veio no saco do proxy
// ([MessageState.Placement]) e o parâmetro `reasoning` ([RequestParams.Reasoning]).

// aos516Mensagem é um turno `assistant` com uma tool call e o estado dado.
func aos516Mensagem(st *MessageState) Message {
	return Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "s-tool-1", Type: "function", Function: FunctionCall{Name: "f", Arguments: "{}"}}}, State: st}
}

func aos516Wire(t *testing.T, st *MessageState) string {
	t.Helper()
	got, err := ChatRequest{Model: "m", Messages: []Message{aos516Mensagem(st)}}.MarshalWire(false)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(got) {
		t.Fatalf("o pedido nao e JSON valido: %s", got)
	}
	return string(got)
}

// COM `topo`, os campos do saco voltam como chaves da mensagem, com os BYTES com que vieram, e o
// saco NÃO volta. Com a omissão, a forma é a de sempre. A forma exacta está presa aqui.
func TestAOS516_Placement_OSacoVoltaNoTopoESemOSaco(t *testing.T) {
	cru := []byte(`[ {"b" : "<&>é" ,  "signature":"U0lH+/=="} ]`)
	campos := []ProviderStateField{
		{Where: StateWhereMessage, Name: "reasoning_content", Raw: []byte(`"r"`)},
		{Where: StateWherePSF, Name: "reasoning", Raw: []byte(`"p"`)},
		{Where: StateWherePSF, Name: "reasoning_details", Raw: cru},
	}
	const cabeca = `{"model":"m","messages":[{"role":"assistant","content":"","tool_calls":[{"id":"s-tool-1","type":"function","function":{"name":"f","arguments":"{}"}}],"reasoning_content":"r"`
	origem := aos516Wire(t, &MessageState{Return: true, Fields: campos})
	if quero := cabeca + `,"provider_specific_fields":{"reasoning":"p","reasoning_details":` + string(cru) + `}}]}`; origem != quero {
		t.Fatalf("a omissao mudou de forma:\n veio:  %s\n quero: %s", origem, quero)
	}
	topo := aos516Wire(t, &MessageState{Return: true, Placement: StatePlacementTop, Fields: campos})
	if quero := cabeca + `,"reasoning":"p","reasoning_details":` + string(cru) + `}]}`; topo != quero {
		t.Fatalf("o estado no topo:\n veio:  %s\n quero: %s", topo, quero)
	}
	if strings.Contains(topo, "provider_specific_fields") {
		t.Fatalf("com o estado no topo o saco nao volta: %s", topo)
	}
	// Sem campos no saco, `topo` e a omissao escrevem os mesmos bytes.
	soMensagem := []ProviderStateField{{Where: StateWhereMessage, Name: "thinking_blocks", Raw: cru}}
	if a, b := aos516Wire(t, &MessageState{Return: true, Fields: soMensagem}), aos516Wire(t, &MessageState{Return: true, Placement: StatePlacementTop, Fields: soMensagem}); a != b {
		t.Fatalf("sem saco as duas formas tinham de ser iguais:\n%s\n%s", a, b)
	}
	// Sem Return, o sitio nao muda um byte do pedido.
	if a, b := aos516Wire(t, nil), aos516Wire(t, &MessageState{Placement: StatePlacementTop, Fields: campos}); a != b {
		t.Fatalf("um estado que nao sai mudou o pedido:\n%s\n%s", a, b)
	}
}

// UMA MENSAGEM NÃO LEVA DUAS CHAVES IGUAIS: um campo do saco com o nome de um que já está no
// topo — porque veio também em `message`, ou repetido no saco — não se escreve.
func TestAOS516_Placement_NomeRepetidoNaoSeEscreveDuasVezes(t *testing.T) {
	topo := aos516Wire(t, &MessageState{Return: true, Placement: StatePlacementTop, Fields: []ProviderStateField{
		{Where: StateWhereMessage, Name: "thinking_blocks", Raw: []byte(`["da mensagem"]`)},
		{Where: StateWherePSF, Name: "thinking_blocks", Raw: []byte(`["do saco"]`)},
		{Where: StateWherePSF, Name: "reasoning_details", Raw: []byte(`["primeiro"]`)},
		{Where: StateWherePSF, Name: "reasoning_details", Raw: []byte(`["segundo"]`)},
	}})
	for texto, vezes := range map[string]int{`"thinking_blocks":["da mensagem"]`: 1, `do saco`: 0, `"reasoning_details":["primeiro"]`: 1, `segundo`: 0} {
		if n := strings.Count(topo, texto); n != vezes {
			t.Errorf("%q aparece %d vez(es), quero %d: %s", texto, n, vezes, topo)
		}
	}
	// O descodificador de sempre le o pedido sem chaves repetidas.
	var doc struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal([]byte(topo), &doc); err != nil || len(doc.Messages) != 1 {
		t.Fatalf("%v: %s", err, topo)
	}
}

// FAIL-CLOSED: um sítio fora do vocabulário, ou um nome fora das listas fechadas num campo do
// saco, não se serializa — com `topo` como com a omissão.
func TestAOS516_Placement_ForaDoVocabularioNaoSeSerializa(t *testing.T) {
	bom := []ProviderStateField{{Where: StateWherePSF, Name: "reasoning_details", Raw: []byte(`[]`)}}
	for nome, st := range map[string]*MessageState{
		"sitio desconhecido":         {Return: true, Placement: "cabecalho", Fields: bom},
		"nome fora da lista no topo": {Return: true, Placement: StatePlacementTop, Fields: []ProviderStateField{{Where: StateWherePSF, Name: "content", Raw: []byte(`"x"`)}}},
		"role pelo saco":             {Return: true, Placement: StatePlacementTop, Fields: []ProviderStateField{{Where: StateWherePSF, Name: "role", Raw: []byte(`"system"`)}}},
		"bytes que nao sao JSON":     {Return: true, Placement: StatePlacementTop, Fields: []ProviderStateField{{Where: StateWherePSF, Name: "reasoning", Raw: []byte(`"x","content":"y"`)}}},
		"sitio do campo inventado":   {Return: true, Placement: StatePlacementTop, Fields: []ProviderStateField{{Where: "message.outro", Name: "reasoning", Raw: []byte(`"x"`)}}},
	} {
		if _, err := (ChatRequest{Model: "m", Messages: []Message{aos516Mensagem(st)}}).MarshalWire(false); !errors.Is(err, ErrStateReturnWire) {
			t.Errorf("%s: tinha de recusar com ErrStateReturnWire; deu %v", nome, err)
		}
	}
}

// O PARÂMETRO `reasoning`: vai no FIM do pedido, só quando o perfil o declara; um pedido sem ele
// tem os bytes de sempre, com e sem estado.
func TestAOS516_Reasoning_NoWire(t *testing.T) {
	req := ChatRequest{Model: "m", Messages: []Message{{Role: RoleUser, Content: "x"}}, MaxTokens: 9}
	const deSempre = `{"model":"m","messages":[{"role":"user","content":"x"}],"max_tokens":9}`
	if got, _ := req.MarshalWire(false); string(got) != deSempre {
		t.Fatalf("o pedido sem reasoning mudou: %s", got)
	}
	// O que um chamador ponha no pedido e apagado pelo perfil — mesmo por um perfil vazio.
	req.Reasoning = &ReasoningParam{Effort: ReasoningEffortHigh}
	RequestParams{}.Apply(&req)
	if got, _ := req.MarshalWire(false); string(got) != deSempre || req.Reasoning != nil {
		t.Fatalf("o reasoning do chamador nao foi apagado: %s", got)
	}
	for quero, p := range map[string]RequestParams{
		`,"reasoning":{"effort":"medium"}}`: {Reasoning: &ReasoningParam{Effort: ReasoningEffortMedium}},
		`,"reasoning":{"max_tokens":2048}}`: {Reasoning: &ReasoningParam{MaxTokens: 2048}},
	} {
		if err := p.Validate(); err != nil {
			t.Fatal(err)
		}
		r := req
		p.Apply(&r)
		got, _ := r.MarshalWire(false)
		if string(got) != strings.TrimSuffix(deSempre, "}")+quero {
			t.Errorf("veio %s; quero o pedido de sempre com %s no fim", got, quero)
		}
		// O perfil nao fica a partilhar o ponteiro com o pedido.
		r.Reasoning.Effort = "adulterado"
		if p.Reasoning.Effort == "adulterado" {
			t.Error("o pedido partilha o parametro com o perfil")
		}
	}
	// Com estado, o corpo montado a mao e o do codificador.
	w := wireChatRequest{Model: "m", Messages: []Message{{Role: RoleUser, Content: "x"}}, MaxTokens: 9, Reasoning: &ReasoningParam{MaxTokens: 2048}}
	quero, _ := json.Marshal(w)
	if got, err := marshalComEstado(w); err != nil || !bytes.Equal(got, quero) {
		t.Fatalf("o corpo montado a mao diverge do codificador:\n veio:  %s\n quero: %s (%v)", got, quero, err)
	}
}

// A VALIDAÇÃO do `reasoning`: exactamente um dos dois campos, nos vocabulários fechados, e nunca
// ao lado das outras duas formas de pedir o raciocínio.
func TestAOS516_Reasoning_Validacao(t *testing.T) {
	for nome, c := range map[string]struct {
		p   RequestParams
		bom bool
	}{
		"effort":                        {RequestParams{Reasoning: &ReasoningParam{Effort: ReasoningEffortLow}}, true},
		"max_tokens":                    {RequestParams{Reasoning: &ReasoningParam{MaxTokens: MinThinkingBudgetTokens}, MaxTokens: 4096}, true},
		"vazio":                         {RequestParams{Reasoning: &ReasoningParam{}}, false},
		"os dois":                       {RequestParams{Reasoning: &ReasoningParam{Effort: ReasoningEffortLow, MaxTokens: 2048}}, false},
		"effort fora do vocabulario":    {RequestParams{Reasoning: &ReasoningParam{Effort: "xhigh"}}, false},
		"max_tokens abaixo do minimo":   {RequestParams{Reasoning: &ReasoningParam{MaxTokens: MinThinkingBudgetTokens - 1}}, false},
		"max_tokens acima do tecto":     {RequestParams{Reasoning: &ReasoningParam{MaxTokens: MaxRequestParamTokens + 1}}, false},
		"max_tokens negativo":           {RequestParams{Reasoning: &ReasoningParam{MaxTokens: -1}}, false},
		"orcamento que nao cabe":        {RequestParams{Reasoning: &ReasoningParam{MaxTokens: 4096}, MaxTokens: 4096}, false},
		"ao lado de thinking":           {RequestParams{Reasoning: &ReasoningParam{Effort: ReasoningEffortLow}, Thinking: &ThinkingParam{Type: ThinkingTypeEnabled}}, false},
		"ao lado de reasoning_effort":   {RequestParams{Reasoning: &ReasoningParam{Effort: ReasoningEffortLow}, ReasoningEffort: ReasoningEffortLow}, false},
		"thinking sozinho, como sempre": {RequestParams{Thinking: &ThinkingParam{Type: ThinkingTypeEnabled, BudgetTokens: 2048}}, true},
		"effort de topo, como sempre":   {RequestParams{ReasoningEffort: ReasoningEffortHigh}, true},
		"nenhum parametro, como sempre": {RequestParams{}, true},
	} {
		err := c.p.Validate()
		if (err == nil) != c.bom || (err != nil && !errors.Is(err, ErrBadRequestParams)) {
			t.Errorf("%s: Validate = %v, quero aceite=%v", nome, err, c.bom)
		}
	}
	// O manifesto e o IsZero.
	if m := (RequestParams{Reasoning: &ReasoningParam{Effort: ReasoningEffortMedium}}).Manifest(); len(m) != 1 || m[ParamKeyReasoning] != "effort:medium" {
		t.Errorf("manifesto = %v", m)
	}
	if m := (RequestParams{Reasoning: &ReasoningParam{MaxTokens: 2048}, MaxTokens: 16000}).Manifest(); len(m) != 2 || m[ParamKeyReasoning] != "max_tokens:2048" || m[ParamKeyMaxTokens] != "16000" {
		t.Errorf("manifesto = %v", m)
	}
	if (RequestParams{Reasoning: &ReasoningParam{Effort: ReasoningEffortLow}}).IsZero() || !(RequestParams{}).IsZero() {
		t.Error("IsZero nao conta o reasoning")
	}
}
