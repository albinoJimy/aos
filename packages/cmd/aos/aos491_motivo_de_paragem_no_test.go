package main

import (
	"context"
	"strconv"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/replay"
)

// AOS-491 — o motivo de paragem, medido pelo nó COMPOSTO: do `finish_reason` que o provider
// escreve no wire até ao `turn.recorded`, à captura selada e ao `/metrics`.

// aos491RespostaDoProvider: o turno que pede a tool diz `tool_calls`; a conclusão vem CORTADA
// (`length`), que é o caso que o ticket quer tornar visível.
func aos491RespostaDoProvider(pedeTool bool, tool string) []byte {
	if pedeTool {
		return []byte(`{"id":"cmpl-491","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"",` +
			`"tool_calls":[{"id":"call-1","type":"function","function":{"name":"` + tool + `","arguments":"{}"}}]},"finish_reason":"tool_calls"}],` +
			`"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)
	}
	return []byte(`{"id":"cmpl-491","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"o documento diz que"},"finish_reason":"length"}],` +
		`"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)
}

// TestAOS491_No_MotivoDoWireAteAoRegisto: nas duas projecções, um run de dois turnos grava
// `tool_calls` e `length` no `turn.recorded`, com as tools oferecidas; o contador do nó soma um
// turno em cada motivo; e o replay — que lê a captura selada por titular — devolve os mesmos
// motivos. O run termina no turno 2, como terminava: um `length` sem tool calls acaba o run.
func TestAOS491_No_MotivoDoWireAteAoRegisto(t *testing.T) {
	for _, projeccao := range []string{"text", "native"} {
		t.Run(projeccao, func(t *testing.T) {
			const runID = "run-491-motivo"
			n := aos486ComporCom(t, projeccao, nil)
			n.upstream.pede = "arquivo"
			n.upstream.responde = aos491RespostaDoProvider
			m := n.correr(t, runID, nil)

			_, payloads := aos490Manifestos(t, m.eventos)
			if len(payloads) != 2 || len(m.pedidos) != 2 {
				t.Fatalf("queria 2 turnos e 2 pedidos; vieram %d e %d", len(payloads), len(m.pedidos))
			}
			for i, quer := range []string{`"tool_calls"`, `"length"`} {
				if got := string(payloads[i]["stop_reason"]); got != quer {
					t.Fatalf("turno %d: stop_reason = %s, quero %s", i+1, got, quer)
				}
				oferta := len(m.pedidos[i].tools)
				if oferta == 0 {
					t.Fatalf("turno %d: o pedido nao levou tools — o cenario nao exercita tools_offered", i+1)
				}
				if got, quer := string(payloads[i]["tools_offered"]), strconv.Itoa(oferta); got != quer {
					t.Fatalf("turno %d: tools_offered = %s, quero %s (os schemas que o pedido levou)", i+1, got, quer)
				}
			}
			if got := string(payloads[1]["final"]); got != "false" {
				t.Fatalf("o turno cortado devia continuar a gravar final=false, gravou %s", got)
			}

			for motivo, quer := range map[agentruntime.StopReason]int64{
				agentruntime.StopToolCalls: 1, agentruntime.StopLength: 1, agentruntime.StopStop: 0, agentruntime.StopUnreported: 0, agentruntime.StopOther: 0,
			} {
				if got := n.node.turnosPorMotivo.lido(motivo); got != quer {
					t.Fatalf("contador do motivo %q = %d, quero %d", motivo, got, quer)
				}
			}

			eng, err := replay.NewEngine(n.node.EventStore, replay.WithContentOpener(n.node.contentOpener, replay.Accessor{
				Principal: "nhi:leitor-aos491", Scopes: []string{replay.DefaultSovereignContentScope},
			}))
			if err != nil {
				t.Fatalf("replay.NewEngine: %v", err)
			}
			rep, err := eng.Replay(context.Background(), runID, replay.Options{Spec: replay.TrajectorySpec{
				Objective: aos490Objectivo, Tools: m.turnos[0].specs, Model: agentruntime.ModelConfig{ModelID: aos486Modelo},
			}})
			if err != nil {
				t.Fatalf("Replay: %v", err)
			}
			if rep.Divergence != nil || len(rep.Steps) != 2 || !rep.Terminated {
				t.Fatalf("o replay tinha de reproduzir os 2 turnos e terminar: divergencia=%+v turnos=%d terminou=%v", rep.Divergence, len(rep.Steps), rep.Terminated)
			}
			if a, b := rep.Steps[0].Response.StopReason, rep.Steps[1].Response.StopReason; a != agentruntime.StopToolCalls || b != agentruntime.StopLength {
				t.Fatalf("o replay devolveu os motivos %q e %q; quero tool_calls e length", a, b)
			}
		})
	}
}

// TestAOS491_MetricaDosTurnosPorMotivo: a família chega ao `/metrics` com uma amostra por valor do
// vocabulário fechado, sempre presentes; o vazio sai com o rótulo `unreported`; e um motivo fora
// do vocabulário NÃO vira rótulo — soma em `other`.
func TestAOS491_MetricaDosTurnosPorMotivo(t *testing.T) {
	h := noComTodasAsFamilias(t)
	corpo := metricasDe(t, h)
	rotulos := []string{"stop", "tool_calls", "length", "content_filter", "other", "unreported"}
	for _, r := range rotulos {
		if quero := `aos_model_turns_total{stop_reason="` + r + `"} 0`; !strings.Contains(corpo, quero+"\n") {
			t.Fatalf("faltou %q no /metrics de um no acabado de arrancar:\n%s", quero, amostrasDe(corpo, "aos_model_turns_total"))
		}
	}
	if !strings.Contains(corpo, "# TYPE aos_model_turns_total counter\n") {
		t.Fatalf("a familia tinha de ser um counter:\n%s", amostrasDe(corpo, "aos_model_turns_total"))
	}

	obs := h.node.turnosPorMotivo.observar
	obs("run-a", agentruntime.StopStop)
	obs("run-a", agentruntime.StopStop)
	obs("run-a", agentruntime.StopLength)
	obs("run-b", agentruntime.StopUnreported)
	obs("run-b", agentruntime.StopReason(`x"} 1`+"\n"+`aos_ready{x="`))
	corpo = metricasDe(t, h)
	for _, quero := range []string{
		`aos_model_turns_total{stop_reason="stop"} 2`,
		`aos_model_turns_total{stop_reason="tool_calls"} 0`,
		`aos_model_turns_total{stop_reason="length"} 1`,
		`aos_model_turns_total{stop_reason="content_filter"} 0`,
		`aos_model_turns_total{stop_reason="other"} 1`,
		`aos_model_turns_total{stop_reason="unreported"} 1`,
	} {
		if !strings.Contains(corpo, quero+"\n") {
			t.Fatalf("faltou %q:\n%s", quero, amostrasDe(corpo, "aos_model_turns_total"))
		}
	}
	if n := strings.Count(corpo, "aos_model_turns_total{"); n != len(agentruntime.StopReasons()) {
		t.Fatalf("a familia tem %d amostras; queria uma por motivo do vocabulario (%d)", n, len(agentruntime.StopReasons()))
	}

	// Nil-safe, como a medição do AOS-489: um nó montado à mão sem a medição não rebenta.
	var semMedicao *turnosPorMotivo
	semMedicao.observar("run", agentruntime.StopStop)
	if semMedicao.lido(agentruntime.StopStop) != 0 {
		t.Fatal("uma medicao nil devia ler zero")
	}
}
