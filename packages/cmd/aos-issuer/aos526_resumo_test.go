package main

import (
	"bytes"
	"strings"
	"testing"

	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-526 — O RESUMO DO QUE SE VAI ASSINAR MOSTRA A REGRA `tool_choice`. Ela só muda o pedido em
// certos turnos, e o manifesto de um turno só a grava quando o pedido a levou: sem isto, um perfil
// só com a regra aparecia ao dono como «params do pedido: nenhum».
func TestAOS526_RouteProfileSign_OResumoMostraARegra(t *testing.T) {
	perfil := modelgateway.RouteProfile{Requested: "gpt-4o", ExpectedModel: "openai/k3", WireClass: modelgateway.WireOpenAIChat,
		Capabilities: []string{modelgateway.CapabilityTools}}
	for _, c := range []struct {
		params *port.RequestParams
		quero  string
	}{
		{&port.RequestParams{ToolChoice: port.ToolChoiceWhileContractPending}, "params do pedido: tool_choice=enquanto_o_contrato_nao_estiver_satisfeito\n"},
		{&port.RequestParams{MaxTokens: 16000, ToolChoice: port.ToolChoiceWhileContractPending}, "params do pedido: max_tokens=16000, tool_choice=enquanto_o_contrato_nao_estiver_satisfeito\n"},
		{&port.RequestParams{MaxTokens: 16000}, "params do pedido: max_tokens=16000\n"},
		{nil, "params do pedido: nenhum\n"},
	} {
		p := perfil
		p.Params = c.params
		var out bytes.Buffer
		resumoDoRegisto(&out, routeentry.Entry{Profile: p})
		if !strings.Contains(out.String(), c.quero) {
			t.Errorf("o resumo nao diz %q:\n%s", c.quero, out.String())
		}
	}
}
