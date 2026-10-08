package port

import (
	"bytes"
	"encoding/json"
	"testing"
)

// AOS-515 — o corpo montado à mão para um pedido com estado é, para um pedido SEM estado, byte a
// byte o do codificador de sempre. É o que garante que a serialização à mão não muda mais nada.
func TestAOS515_MarshalComEstado_SemEstadoEOCodificadorDeSempre(t *testing.T) {
	temp, seed := 0.5, int64(7)
	msgs := []Message{
		{Role: RoleSystem, Content: "protocolo <kind> & \"aspas\" \u2028 é"},
		{Role: RoleUser, Content: "<objective>\nfaz\n", Name: "n"},
		{Role: RoleAssistant, Content: "", ToolCalls: []ToolCall{{ID: "s-tool-1", Type: "function", Function: FunctionCall{Name: "doc_read", Arguments: `{"a":"<b>"}`}}}},
		{Role: RoleTool, ToolCallID: "s-tool-1", Content: "resultado"},
	}
	for nome, w := range map[string]wireChatRequest{
		"minimo":   {Model: "m", Messages: msgs[:2]},
		"completo": {Model: "m<&>", Messages: msgs, Tools: []Tool{{Type: "function", Function: FunctionDef{Name: "doc_read", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}}}, ToolChoice: "auto", Stream: true, Temperature: &temp, Seed: &seed, MaxTokens: 9, Thinking: &ThinkingParam{Type: ThinkingTypeEnabled, BudgetTokens: 2048}, ReasoningEffort: ReasoningEffortHigh},
		"sem nada": {Model: "m", Messages: []Message{}},
	} {
		quero, err := json.Marshal(w)
		if err != nil {
			t.Fatal(err)
		}
		got, err := marshalComEstado(w)
		if err != nil || !bytes.Equal(got, quero) {
			t.Fatalf("%s: o corpo montado a mao diverge do codificador:\n veio:  %s\n quero: %s (%v)", nome, got, quero, err)
		}
	}
}

// Os bytes do estado são COPIADOS: espaços, a ordem das chaves, `<`, `&` e escapes saem como
// vieram, em todos os sítios (mensagem, provider_specific_fields, tool call, function).
func TestAOS515_MarshalComEstado_OsBytesSaoCopiados(t *testing.T) {
	cru := []byte(`[ {"b" : "<&>\u00e9" ,  "a":1} ]`)
	m := Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "s-tool-1", Type: "function", Function: FunctionCall{Name: "f", Arguments: "{}"}}},
		State: &MessageState{Return: true, ProviderIDs: true,
			Fields:    []ProviderStateField{{Where: StateWhereMessage, Name: "thinking_blocks", Raw: cru}, {Where: StateWherePSF, Name: "thinking", Raw: cru}, {Where: StateWhereMessage, Name: "reasoning_content", Raw: []byte(`"r"`)}, {Where: StateWherePSF, Name: "reasoning", Raw: []byte(`"p"`)}},
			ToolCalls: []ProviderStateToolCall{{N: 1, IDUsable: true, IDValue: "toolu_1", Fields: []ProviderStateField{{Where: StateWhereToolCall, Name: "thought_signature", Raw: cru}, {Where: StateWhereFunction, Name: "signature", Raw: cru}}}},
		}}
	got, err := ChatRequest{Model: "m", Messages: []Message{m, {Role: RoleTool, ToolCallID: "s-tool-1", Content: "r"}}}.MarshalWire(false)
	if err != nil {
		t.Fatal(err)
	}
	c := string(cru)
	quero := `{"model":"m","messages":[{"role":"assistant","content":"","tool_calls":[{"id":"toolu_1","type":"function","function":{"name":"f","arguments":"{}","signature":` + c + `},"thought_signature":` + c + `}],"thinking_blocks":` + c + `,"reasoning_content":"r","provider_specific_fields":{"thinking":` + c + `,"reasoning":"p"}},{"role":"tool","content":"r","tool_call_id":"toolu_1"}]}`
	if string(got) != quero {
		t.Fatalf("o pedido com estado:\n veio:  %s\n quero: %s", got, quero)
	}
	if !json.Valid(got) {
		t.Fatal("o pedido com estado nao e JSON valido")
	}
	// O raciocínio de sempre continua a ser retirado, com ou sem estado.
	m.ReasoningContent = "RACIOCINIO-DE-SEMPRE"
	got, _ = ChatRequest{Model: "m", Messages: []Message{m}}.MarshalWire(false)
	if bytes.Contains(got, []byte("RACIOCINIO-DE-SEMPRE")) {
		t.Fatalf("o ReasoningContent saiu num pedido com estado: %s", got)
	}
	// Um número de entradas do estado que não é o das tool calls, ou um id inutilizável com os
	// ids do provider pedidos, não se serializa.
	mau := m
	st := *m.State
	st.ToolCalls = append(st.ToolCalls, ProviderStateToolCall{N: 2})
	mau.State = &st
	if _, err := (ChatRequest{Model: "m", Messages: []Message{mau}}).MarshalWire(false); err == nil {
		t.Fatal("estado desalinhado com as tool calls tinha de recusar")
	}
	st2 := *m.State
	st2.ToolCalls = []ProviderStateToolCall{{N: 1}}
	mau.State = &st2
	if _, err := (ChatRequest{Model: "m", Messages: []Message{mau}}).MarshalWire(false); err == nil {
		t.Fatal("id do provider inutilizavel tinha de recusar")
	}
}
