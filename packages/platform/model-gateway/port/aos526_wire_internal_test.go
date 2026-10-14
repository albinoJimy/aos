package port

import (
	"bytes"
	"encoding/json"
	"testing"
)

// AOS-526 — O `tool_choice` NO WIRE. Vazio não vai; um modo vai como texto; um nome de tool vai na
// forma OpenAI de chat/completions que nomeia a função. As duas serializações — a de sempre e a
// montada à mão de um pedido com estado — dão os mesmos bytes.
func TestAOS526_Wire_AFormaDoToolChoice(t *testing.T) {
	msgs := []Message{{Role: RoleUser, Content: "olá"}}
	tools := []Tool{{Type: "function", Function: FunctionDef{Name: "doc_read", Parameters: json.RawMessage(`{"type":"object"}`)}}}
	const cabeca = `{"model":"m","messages":[{"role":"user","content":"olá"}],"tools":[{"type":"function","function":{"name":"doc_read","parameters":{"type":"object"}}}]`
	for escolha, quero := range map[string]string{
		"":         cabeca + `}`,
		"auto":     cabeca + `,"tool_choice":"auto"}`,
		"none":     cabeca + `,"tool_choice":"none"}`,
		"required": cabeca + `,"tool_choice":"required"}`,
		"doc_read": cabeca + `,"tool_choice":{"type":"function","function":{"name":"doc_read"}}}`,
		"a<&>b":    cabeca + `,"tool_choice":{"type":"function","function":{"name":"a\u003c\u0026\u003eb"}}}`,
	} {
		got, err := ChatRequest{Model: "m", Messages: msgs, Tools: tools, ToolChoice: escolha}.MarshalWire(false)
		if err != nil || string(got) != quero {
			t.Fatalf("tool_choice %q:\n veio:  %s\n quero: %s (%v)", escolha, got, quero, err)
		}
		w := wireChatRequest{Model: "m", Messages: msgs, Tools: tools, ToolChoice: wireToolChoice(escolha)}
		aMao, err := marshalComEstado(w)
		if err != nil || !bytes.Equal(aMao, got) {
			t.Fatalf("tool_choice %q: o corpo montado a mao diverge do codificador:\n veio:  %s\n quero: %s (%v)", escolha, aMao, got, err)
		}
	}
}

// ContractToForce é metadado de plataforma: nunca vai no wire, nem se lê de JSON.
func TestAOS526_Wire_ContractPendingNaoSai(t *testing.T) {
	r := ChatRequest{Model: "m", Messages: []Message{{Role: RoleUser, Content: "x"}}, ContractToForce: []string{"PENDENTE-526"}}
	got, err := r.MarshalWire(false)
	if err != nil || bytes.Contains(got, []byte("PENDENTE-526")) || string(got) != `{"model":"m","messages":[{"role":"user","content":"x"}]}` {
		t.Fatalf("o contrato por cumprir saiu no wire: %s (%v)", got, err)
	}
	var lido ChatRequest
	if err := json.Unmarshal([]byte(`{"model":"m","messages":[],"ContractToForce":["a"],"contract_pending":["a"]}`), &lido); err != nil || lido.ContractToForce != nil {
		t.Fatalf("o contrato por cumprir leu-se de JSON: %+v (%v)", lido, err)
	}
}

// A REGRA, pela função que o gateway aplica: nenhuma tool por cumprir oferecida ⇒ o campo não vai
// (e o de um chamador é apagado); uma ⇒ nomeada; mais ⇒ `required`; um nome que é um modo ⇒
// `required`. Sem a regra, o pedido não é tocado.
func TestAOS526_Regra_ApplyToolChoice(t *testing.T) {
	tools := []Tool{{Function: FunctionDef{Name: "a"}}, {Function: FunctionDef{Name: "b"}}, {Function: FunctionDef{Name: "none"}}}
	regra := RequestParams{ToolChoice: ToolChoiceWhileContractPending}
	casos := []struct {
		nome        string
		tools       []Tool
		pendentes   []string
		escolha     string
		doManifesto string
	}{
		{"uma por cumprir", tools, []string{"a"}, "a", ToolChoiceManifestNamed},
		{"duas por cumprir", tools, []string{"b", "a"}, ToolChoiceRequired, ToolChoiceRequired},
		{"repetida conta uma vez", tools, []string{"a", "a"}, "a", ToolChoiceManifestNamed},
		{"so uma das duas oferecida", tools[:1], []string{"a", "b"}, "a", ToolChoiceManifestNamed},
		{"nome que e um modo", tools, []string{"none"}, ToolChoiceRequired, ToolChoiceRequired},
		{"contrato satisfeito", tools, nil, "", ""},
		{"por cumprir nao oferecida", tools, []string{"c"}, "", ""},
		{"sem tools oferecidas", nil, []string{"a"}, "", ""},
		{"nome vazio", tools, []string{""}, "", ""},
	}
	for _, c := range casos {
		req := ChatRequest{Tools: c.tools, ContractToForce: c.pendentes, ToolChoice: ToolChoiceAuto}
		if got := regra.ApplyToolChoice(&req); got != c.doManifesto || req.ToolChoice != c.escolha {
			t.Errorf("%s: tool_choice %q, manifesto %q; quero %q, %q", c.nome, req.ToolChoice, got, c.escolha, c.doManifesto)
		}
	}
	semRegra := ChatRequest{Tools: tools, ContractToForce: []string{"a"}, ToolChoice: ToolChoiceAuto}
	if got := (RequestParams{}).ApplyToolChoice(&semRegra); got != "" || semRegra.ToolChoice != ToolChoiceAuto {
		t.Fatalf("sem a regra o pedido foi tocado: %q, %q", semRegra.ToolChoice, got)
	}
}

// O VOCABULÁRIO da regra é fechado, e a regra sozinha não é um parâmetro do manifesto.
func TestAOS526_Regra_VocabularioFechado(t *testing.T) {
	for _, v := range []string{"", ToolChoiceWhileContractPending} {
		if err := (RequestParams{ToolChoice: v}).Validate(); err != nil {
			t.Errorf("%q tinha de ser aceite: %v", v, err)
		}
	}
	for _, v := range []string{"required", "auto", "none", "sempre", ToolChoiceNeverName, "Enquanto_o_contrato_nao_estiver_satisfeito"} {
		if err := (RequestParams{ToolChoice: v}).Validate(); err == nil {
			t.Errorf("%q tinha de ser recusado", v)
		}
	}
	regra := RequestParams{ToolChoice: ToolChoiceWhileContractPending}
	if regra.IsZero() || regra.Manifest() != nil {
		t.Fatalf("a regra declara-se (IsZero=%v) e nao entra no manifesto por si: %v", regra.IsZero(), regra.Manifest())
	}
	if m := (RequestParams{MaxTokens: 10, ToolChoice: ToolChoiceWhileContractPending}).Manifest(); len(m) != 1 || m[ParamKeyMaxTokens] != "10" {
		t.Fatalf("manifesto com a regra e max_tokens: %v", m)
	}
}

// O QUE SE MOSTRA a quem assina inclui a regra: um perfil só com ela não aparece como «nenhum».
func TestAOS526_Regra_DeclaradaAQuemAssina(t *testing.T) {
	if (RequestParams{}).Declared() != nil {
		t.Fatal("um perfil sem nada declarou alguma coisa")
	}
	so := RequestParams{ToolChoice: ToolChoiceWhileContractPending}.Declared()
	if len(so) != 1 || so[ParamKeyToolChoice] != ToolChoiceWhileContractPending {
		t.Fatalf("a regra sozinha: %v", so)
	}
	com := RequestParams{MaxTokens: 10, ToolChoice: ToolChoiceWhileContractPending}.Declared()
	if len(com) != 2 || com[ParamKeyMaxTokens] != "10" || com[ParamKeyToolChoice] != ToolChoiceWhileContractPending {
		t.Fatalf("a regra com max_tokens: %v", com)
	}
	if m := (RequestParams{MaxTokens: 10}).Declared(); len(m) != 1 || m[ParamKeyMaxTokens] != "10" {
		t.Fatalf("sem a regra, o declarado e o manifesto: %v", m)
	}
}
