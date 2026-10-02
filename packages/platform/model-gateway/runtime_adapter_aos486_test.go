package modelgateway_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-486 — o adaptador envia ao modelo só o schema das tools que o run pode chamar.
//
// O tool set de [modelgateway.WithTools] é do nó e fixa-se uma vez; a lista-branca é do run e
// chega pelo ctx. Antes do AOS-486 o pedido levava sempre o tool set do nó inteiro.

func aos486Tool(nome string) port.Tool {
	return port.Tool{Type: "function", Function: port.FunctionDef{Name: nome, Description: "tool " + nome}}
}

// aos486Oferta devolve a fonte de ctx que o nó liga: o run é restrito se `restrito`, e admite os
// nomes em `lista`.
func aos486Oferta(lista []string, restrito bool) func(context.Context) (func(string) bool, bool) {
	return func(context.Context) (func(string) bool, bool) {
		return func(nome string) bool {
			for _, n := range lista {
				if n == nome {
					return true
				}
			}
			return false
		}, restrito
	}
}

func aos486Nomes(tools []port.Tool) []string {
	out := []string{}
	for _, t := range tools {
		out = append(out, t.Function.Name)
	}
	return out
}

func TestAOS486_Adaptador_OfertaSegueAListaDoRun(t *testing.T) {
	t.Parallel()
	view := agentruntime.PromptView{Turn: 1, Materialized: []byte("x")}
	doNo := []port.Tool{aos486Tool("x"), aos486Tool("y"), aos486Tool("z")}

	casos := []struct {
		nome   string
		oferta func(context.Context) (func(string) bool, bool)
		quer   []string
	}{
		{"run com lista vazia: nenhuma", aos486Oferta([]string{}, true), []string{}},
		{"run com lista nil mas marcado restrito: nenhuma", aos486Oferta(nil, true), []string{}},
		{"uma tool", aos486Oferta([]string{"y"}, true), []string{"y"}},
		{"a ordem é a do nó, não a da lista", aos486Oferta([]string{"z", "x"}, true), []string{"x", "z"}},
		{"um nome que o nó não tem não aparece", aos486Oferta([]string{"fora", "y"}, true), []string{"y"}},
		{
			"restrito sem predicado resolve pelo lado seguro: nenhuma",
			func(context.Context) (func(string) bool, bool) { return nil, true },
			[]string{},
		},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			t.Parallel()
			gw := &capturaGateway{}
			mc := modelgateway.NewModelClient(gw, "gpt-4o", modelgateway.WithTools(doNo), modelgateway.WithToolOfferFromContext(c.oferta))
			if _, err := mc.Call(context.Background(), view); err != nil {
				t.Fatalf("Call: %v", err)
			}
			if got := aos486Nomes(gw.req.Tools); !reflect.DeepEqual(got, c.quer) {
				t.Fatalf("o pedido leva as tools %v, quero %v", got, c.quer)
			}
			// O schema que passa é o do nó, inteiro: o filtro escolhe, não reescreve.
			for _, tool := range gw.req.Tools {
				if tool.Type != "function" || tool.Function.Description != "tool "+tool.Function.Name {
					t.Fatalf("o schema de %q foi alterado: %+v", tool.Function.Name, tool)
				}
			}
			// No wire: sem tools, o campo `tools` é omitido, não enviado vazio.
			corpo, err := json.Marshal(gw.req)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(corpo, &wire); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if _, tem := wire["tools"]; tem != (len(c.quer) > 0) {
				t.Fatalf("campo `tools` no wire: presente=%v com %d tools oferecidas (%s)", tem, len(c.quer), corpo)
			}
			// O tool set do nó não é tocado: o run seguinte, sem lista, leva-o inteiro.
			if got := aos486Nomes(doNo); !reflect.DeepEqual(got, []string{"x", "y", "z"}) {
				t.Fatalf("o filtro mutou o tool set do nó: %v", got)
			}
		})
	}
}

// TestAOS486_Adaptador_RunSemLista_PedidoInalterado: um run sem lista-branca leva o tool set do
// nó TAL COMO foi fixado — o mesmo slice, e o mesmo pedido que um adaptador sem a opção ligada.
func TestAOS486_Adaptador_RunSemLista_PedidoInalterado(t *testing.T) {
	t.Parallel()
	view := agentruntime.PromptView{Turn: 1, Materialized: []byte("x")}
	doNo := []port.Tool{aos486Tool("x"), aos486Tool("y")}

	semOpcao := &capturaGateway{}
	if _, err := modelgateway.NewModelClient(semOpcao, "gpt-4o", modelgateway.WithTools(doNo)).Call(context.Background(), view); err != nil {
		t.Fatalf("Call (sem a opcao): %v", err)
	}
	// O predicado nega tudo: se o adaptador o consultasse num run não restrito, via-se aqui.
	comOpcao := &capturaGateway{}
	mc := modelgateway.NewModelClient(comOpcao, "gpt-4o", modelgateway.WithTools(doNo), modelgateway.WithToolOfferFromContext(aos486Oferta(nil, false)))
	if _, err := mc.Call(context.Background(), view); err != nil {
		t.Fatalf("Call (com a opcao, run sem lista): %v", err)
	}
	if !reflect.DeepEqual(comOpcao.req, semOpcao.req) {
		t.Fatalf("o pedido de um run sem lista mudou com a opção ligada:\n com: %+v\n sem: %+v", comOpcao.req, semOpcao.req)
	}
	if len(comOpcao.req.Tools) != 2 || &comOpcao.req.Tools[0] != &doNo[0] {
		t.Fatal("um run sem lista tinha de levar o MESMO slice de tools do nó, sem cópia nem filtro")
	}

	// Nó sem AOS_MODEL_TOOLS: nada a oferecer, com ou sem lista — o campo fica nil como sempre.
	for nome, oferta := range map[string]func(context.Context) (func(string) bool, bool){
		"sem lista": aos486Oferta(nil, false), "lista vazia": aos486Oferta([]string{}, true), "lista com nome": aos486Oferta([]string{"x"}, true),
	} {
		gw := &capturaGateway{}
		if _, err := modelgateway.NewModelClient(gw, "gpt-4o", modelgateway.WithToolOfferFromContext(oferta)).Call(context.Background(), view); err != nil {
			t.Fatalf("Call (no sem tools, %s): %v", nome, err)
		}
		if gw.req.Tools != nil {
			t.Fatalf("nó sem tools, %s: o pedido leva %v", nome, gw.req.Tools)
		}
	}

	// Opção nil é inerte.
	inerte := &capturaGateway{}
	if _, err := modelgateway.NewModelClient(inerte, "gpt-4o", modelgateway.WithTools(doNo), modelgateway.WithToolOfferFromContext(nil)).Call(context.Background(), view); err != nil {
		t.Fatalf("Call (opcao nil): %v", err)
	}
	if !reflect.DeepEqual(inerte.req, semOpcao.req) {
		t.Fatal("WithToolOfferFromContext(nil) tinha de ser inerte")
	}
}
