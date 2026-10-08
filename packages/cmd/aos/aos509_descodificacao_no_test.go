package main

// AOS-509 — A DESCODIFICAÇÃO TOLERANTE, NO NÓ COMPOSTO.

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// aos509Resposta é a resposta do provider de ensaio do AOS-490 ([aos490RespostaDoProvider]) com
// as MESMAS coisas ditas nas formas que o gateway passou a ler: `content` em lista de partes de
// texto, `function.arguments` em objecto e o raciocínio em `reasoning`.
func aos509Resposta(pedeTool bool, tool string) []byte {
	msg := `{"role":"assistant","content":[{"type":"text","text":"fei"},{"type":"text","text":"to"}],"reasoning":"` + aos490RaciocinioMarca + `: ja tenho o resultado."}`
	finish := "stop"
	if pedeTool {
		msg = `{"role":"assistant","content":[],"reasoning":"` + aos490RaciocinioMarca + `: preciso de ler o documento.",` +
			`"tool_calls":[{"id":"tool_PROVIDER","type":"function","function":{"name":"` + tool + `","arguments":{}}}]}`
		finish = "tool_calls"
	}
	return []byte(`{"id":"cmpl-490","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":` + msg + `,"finish_reason":"` + finish + `"}],` +
		`"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18,"completion_tokens_details":{"reasoning_tokens":3},"prompt_tokens_details":{"cached_tokens":8}}}`)
}

// A MESMA RESPOSTA, NAS FORMAS NOVAS, DÁ OS MESMOS BYTES. O run de referência do AOS-490, com o
// provider a mandar `content` em partes, `arguments` em objecto e o raciocínio em `reasoning`:
// o pedido seguinte ao provider (o tail projectado), os `turn.recorded`, a parte em claro das
// capturas e a sequência de eventos são, byte a byte, os goldens medidos na base com as formas
// de sempre. E o raciocínio não aparece em nenhum evento.
func TestAOS509_No_FormasNovas_MesmosBytesDaBase(t *testing.T) {
	const runID = "run-490-nativo"
	n := aos507Compor(t, "", false)
	n.upstream.responde = aos509Resposta
	m := n.correr(t, runID, nil)
	if len(m.pedidos) != 2 {
		t.Fatalf("queria 2 pedidos, vieram %d", len(m.pedidos))
	}
	for i, quer := range []string{aos490Pedido1, aos490Pedido2} {
		if got := string(m.pedidos[i].cru); got != quer {
			t.Errorf("pedido %d nao e o golden do AOS-490:\n veio:  %s\n quero: %s", i+1, got, quer)
		}
	}
	if got := aos504TiposDeEvento(m); !reflect.DeepEqual(got, aos505BaseTiposDeEvento) {
		t.Errorf("os eventos do run mudaram:\n veio:  %v\n quero: %v", got, aos505BaseTiposDeEvento)
	}
	turnos, capturas := aos507Partes(t, m)
	if !reflect.DeepEqual(turnos, aos505BaseTurnos) {
		t.Errorf("os turn.recorded nao sao os da base:\n veio:  %v\n quero: %v", turnos, aos505BaseTurnos)
	}
	if !reflect.DeepEqual(capturas, aos505BaseCapturas) {
		t.Errorf("a parte em claro das capturas nao e a da base:\n veio:  %v\n quero: %v", capturas, aos505BaseCapturas)
	}
	for _, ev := range m.eventos {
		if strings.Contains(string(ev.Payload), aos490RaciocinioMarca) {
			t.Errorf("o evento %s leva o raciocinio em claro", ev.Type)
		}
	}
	// O replay devolve o raciocínio que a captura selou — lido de `reasoning` — e o texto de `content`.
	rep := aos505NoReplay(t, n, runID, m)
	if len(rep.Steps) != 2 || !strings.Contains(rep.Steps[0].Response.Reasoning, aos490RaciocinioMarca) || rep.Steps[1].Response.Text != "feito" {
		t.Errorf("replay: o raciocinio tinha de estar na captura e o texto ser o de content: %+v", rep.Steps)
	}
	metrics := aos505NoMetrics(t, n)
	for _, causa := range []string{"content_parte_nao_texto", "content_forma", "arguments_forma", "json_invalido", "sem_choices"} {
		if !strings.Contains(metrics, `aos_model_response_rejected_total{causa="`+causa+`"} 0`+"\n") {
			t.Errorf("falta a amostra a zero da causa %s", causa)
		}
	}
}

// UMA PARTE QUE NÃO É TEXTO RECUSA A RESPOSTA, E A CAUSA CONTA. O run falha; o contador da causa
// sobe e os outros ficam a zero; nenhum byte da resposta chega a um evento nem ao `/metrics`.
func TestAOS509_No_RespostasRecusadasPorCausa(t *testing.T) {
	const marca = "SENTINELA-509-PARTE"
	n := aos507Compor(t, "", false)
	n.upstream.responde = func(bool, string) []byte {
		return []byte(`{"id":"cmpl-509","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant",` +
			`"content":[{"type":"text","text":"` + marca + `"},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + marca + `"}}]},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)
	}
	const runID = "run-509-recusada"
	rec := postJSON(n.h, "POST", "/runs", map[string]any{
		"run_id": runID, "objective": "Le o documento notes e resume", "principal_nhi": durAgent, "credential": n.tok, "max_turns": 3,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
	wctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	oc, ok, werr := n.svc.Wait(wctx, runID)
	if werr != nil || !ok {
		t.Fatalf("o run devia ter sido hospedado: ok=%v err=%v", ok, werr)
	}
	if oc.Err == nil || !strings.Contains(oc.Err.Error(), "parte que nao e de texto") {
		t.Fatalf("o run tinha de falhar com a causa da parte que nao e texto; veio %v", oc.Err)
	}
	if strings.Contains(oc.Err.Error(), marca) {
		t.Errorf("o erro leva bytes da resposta: %v", oc.Err)
	}
	eventos, err := n.node.EventStore.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range eventos {
		if strings.Contains(string(ev.Payload), marca) {
			t.Errorf("o evento %s leva bytes da resposta recusada", ev.Type)
		}
	}
	metrics := aos505NoMetrics(t, n)
	if strings.Contains(metrics, marca) {
		t.Errorf("o /metrics leva bytes da resposta recusada")
	}
	if !strings.Contains(metrics, "# TYPE aos_model_response_rejected_total counter\n") {
		t.Fatalf("falta a familia aos_model_response_rejected_total")
	}
	if !strings.Contains(metrics, `aos_model_response_rejected_total{causa="content_parte_nao_texto"} 1`+"\n") {
		t.Errorf("a causa nao foi contada uma vez")
	}
	amostras := 0
	for _, l := range strings.Split(metrics, "\n") {
		if strings.HasPrefix(l, "aos_model_response_rejected_total{") {
			amostras++
			if !strings.HasSuffix(l, " 0") && !strings.Contains(l, "content_parte_nao_texto") {
				t.Errorf("amostra inesperada: %s", l)
			}
		}
	}
	if amostras != 5 {
		t.Errorf("queria 5 amostras (o vocabulario fechado das causas), vieram %d", amostras)
	}
	c := novosContadoresDeRejeicao()
	c.observar("TEXTO-DO-PROVIDER")
	if len(c.total) != 5 {
		t.Errorf("uma causa fora do vocabulario criou uma serie")
	}
}
