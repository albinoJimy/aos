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

// AOS-530 — O RESUMO DO QUE SE VAI ASSINAR MOSTRA O USO: `tool` di-lo, e um registo sem o membro
// (uso run) também o diz.
func TestAOS530_RouteProfileSign_OResumoMostraOUso(t *testing.T) {
	perfil := modelgateway.RouteProfile{Requested: "gpt-4o-mini", ExpectedModel: "openai/visao", WireClass: modelgateway.WireOpenAIChat,
		Capabilities: []string{modelgateway.CapabilityImage}}
	for uso, quero := range map[string]string{routeentry.UseTool: "uso: tool — ", routeentry.UseRun: "uso: run — ", "": "uso: run — "} {
		var out bytes.Buffer
		resumoDoRegisto(&out, routeentry.Entry{Profile: perfil, Use: uso})
		if !strings.Contains(out.String(), quero) {
			t.Errorf("uso %q: o resumo nao diz %q:\n%s", uso, quero, out.String())
		}
	}
	// AOS-536: o resumo diz a experiência da media do perfil — o dono não assina um registo de vídeo
	// com «experiencia ocr» à frente, e o de imagem fica o de antes.
	for media, quero := range map[string]string{modelgateway.CapabilityImage: "da experiencia ocr\n", modelgateway.CapabilityVideo: "da experiencia video (perfil de video, AOS-536)\n"} {
		var out bytes.Buffer
		resumoDoRegisto(&out, routeentry.Entry{Profile: perfil, Use: routeentry.UseTool, Media: media})
		if !strings.Contains(out.String(), quero) {
			t.Errorf("media %q: o resumo nao diz %q:\n%s", media, quero, out.String())
		}
	}
}
