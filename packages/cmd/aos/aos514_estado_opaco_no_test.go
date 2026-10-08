package main

// AOS-514 — O ESTADO OPACO DO PROVIDER, NO NÓ COMPOSTO (ADR-040).
//
// O nó é o de [aos486ComporCom], com o cliente de modelo de [parseModelFromEnv]; o provider é o
// de ensaio. O run de referência é o do AOS-490 (`run-490-nativo`), e os goldens são os MEDIDOS
// NA BASE pelo AOS-505 (aos505_goldens_da_base_test.go) — gerados antes de qualquer código deste
// ticket, e que os testes do AOS-505 e do AOS-507 continuam a exigir.

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
)

// aos514Compor compõe o nó com a captura do estado dada no ambiente (a governação da rota e a
// medição da forma desligadas). `definir` false ⇒ a variável NÃO existe no ambiente do processo.
func aos514Compor(t *testing.T, modo string, definir bool) *aos486No {
	t.Helper()
	if definir {
		t.Setenv("AOS_MODEL_PROVIDER_STATE", modo)
	} else {
		aos504SemVariavel(t, "AOS_MODEL_PROVIDER_STATE")
	}
	aos504SemVariavel(t, "AOS_MODEL_PROVIDER_STATE_MAX_BYTES")
	aos504SemVariavel(t, "AOS_MODEL_RESPONSE_SHAPE")
	return aos505NoCompor(t, "", false, "", nil)
}

// COM A CAPTURA DESLIGADA, O NÓ GRAVA OS BYTES DA BASE. Com a variável ausente, vazia e em `off`:
// os pedidos ao provider são os goldens do AOS-490; os `turn.recorded` (com o `prompt_hash` e o
// layout de sempre) e a parte em claro das capturas são, byte a byte, os medidos na base; os
// eventos são os mesmos, pela mesma ordem; o `/metrics` tem as mesmas famílias, as mesmas séries
// de layout e nenhuma do estado; e o arranque não declara linha nenhuma.
func TestAOS514_No_Off_SaoOsBytesDaBase(t *testing.T) {
	const runID = "run-490-nativo"
	for _, c := range []struct {
		nome, modo string
		definir    bool
	}{{"variavel ausente", "", false}, {"variavel vazia", "", true}, {"off", "off", true}, {"off com espacos", " off  ", true}} {
		t.Run(c.nome, func(t *testing.T) {
			n := aos514Compor(t, c.modo, c.definir)
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
			metrics := aos505NoMetrics(t, n)
			if got := aos505NoFamilias(metrics); !reflect.DeepEqual(got, aos505BaseFamiliasDoMetrics) {
				t.Errorf("as familias do /metrics mudaram:\n veio:  %v\n quero: %v", got, aos505BaseFamiliasDoMetrics)
			}
			if strings.Contains(metrics, "provider_state") || strings.Contains(metrics, agentruntime.AssemblyVersion150) || n.node.estadoDoProvider != nil {
				t.Errorf("com a captura desligada o no nao tem contadores, familia nem serie de layout do estado")
			}
			for _, ev := range m.eventos {
				if strings.Contains(string(ev.Payload), "provider_state") || strings.Contains(string(ev.Payload), agentruntime.StateDigestLabel) {
					t.Errorf("com a captura desligada o evento %s fala do estado", ev.Type)
				}
			}
			// O conteúdo selado também não: a captura decifrada não tem estado.
			for i, st := range aos514Replay(t, n, runID, m).Steps {
				if st.Response.State != nil {
					t.Errorf("turno %d: com a captura desligada a captura leva estado", i+1)
				}
			}
		})
	}
	if got := modelProviderStateBanner(true, "off", modelgateway.DefaultProviderStateMaxBytes); got != nil {
		t.Errorf("com off o banner nao tem linha nenhuma; veio %v", got)
	}
}

// aos514Replay reproduz o run com o motor de replay, atrás do gate soberano, e exige fidelidade.
func aos514Replay(t *testing.T, n *aos486No, runID string, m aos486Medido) replay.ReplayResult {
	t.Helper()
	eng, err := replay.NewEngine(n.node.EventStore, replay.WithContentOpener(n.node.contentOpener, replay.Accessor{
		Principal: "nhi:leitor-aos514", Scopes: []string{replay.DefaultSovereignContentScope},
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
	if rep.Divergence != nil || len(rep.Steps) != len(m.turnos) {
		t.Fatalf("o replay tinha de reproduzir os %d turnos sem divergir: %+v", len(m.turnos), rep.Divergence)
	}
	return rep
}

// aos514Sentinelas são os valores que o provider de ensaio põe em cada campo do estado.
var aos514Sentinelas = []string{"S514-RC", "S514-BLOCO", "S514-ASSINATURA", "S514-REDIGIDO", "S514-PSF", "S514-ID", "S514-TSIG", "S514-FINAL"}

// aos514RespostaComEstado é a resposta do provider de ensaio com tudo o que um fornecedor de
// raciocínio assinado manda atrás do proxy: `reasoning_content`, `thinking_blocks` com um bloco
// assinado, um de texto vazio e um redigido, o mesmo em `provider_specific_fields`, e uma tool
// call com id do provider e assinatura por chamada. Os valores têm espaços e ordem de chaves que
// uma re-serialização mudaria.
func aos514RespostaComEstado(pedeTool bool, tool string) []byte {
	const blocos = `[ {"signature":"S514-ASSINATURA==","type":"thinking","thinking":"S514-BLOCO"},{"type":"thinking","thinking":"","signature":"dmF6aW8="}, {"type":"redacted_thinking","data":"S514-REDIGIDO"} ]`
	msg := `{"role":"assistant","content":"feito","reasoning_content":"S514-FINAL"}`
	finish := "stop"
	if pedeTool {
		msg = `{"role":"assistant","content":"","reasoning_content":"S514-RC","thinking_blocks":` + blocos + `,` +
			`"provider_specific_fields":{"refusal":null,"thinking_blocks":` + blocos + `,"thinking":"S514-PSF"},` +
			`"tool_calls":[{"thought_signature":"S514-TSIG","id":"S514-ID","type":"function","function":{"name":"` + tool + `","arguments":"{}"}}]}`
		finish = "tool_calls"
	}
	return []byte(`{"id":"cmpl-514","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":` + msg + `,"finish_reason":"` + finish + `"}],` +
		`"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18,"prompt_tokens_details":{"cached_tokens":8}}}`)
}

// EM CAPTURA, ÀS ESCURAS. Com a resposta de sempre do AOS-490 (que traz `reasoning_content` e o
// id de tool call do provider):
//
//   - os PEDIDOS ao provider continuam a ser os goldens do AOS-490, byte a byte — o estado do
//     turno 1 foi capturado e não volta no turno 2;
//   - os eventos são os mesmos, pela mesma ordem; a parte em claro das capturas é a da base;
//   - cada `turn.recorded` é o da base com o layout 1.5.0, e o `prompt_hash` do turno 2 — e só
//     ele — muda: o tail passou a comprometer-se com o estado do turno 1;
//   - a captura decifrada tem o estado dos dois turnos, o replay reproduz sem divergir, e o
//     contador conta dois turnos capturados.
func TestAOS514_No_Capture_AsEscurasEComOTailComprometido(t *testing.T) {
	const runID = "run-490-nativo"
	n := aos514Compor(t, "capture", true)
	if n.node.estadoDoProvider == nil {
		t.Fatalf("com a captura ligada o no tem os contadores do estado")
	}
	m := n.correr(t, runID, nil)
	if len(m.pedidos) != 2 {
		t.Fatalf("queria 2 pedidos, vieram %d", len(m.pedidos))
	}
	for i, quer := range []string{aos490Pedido1, aos490Pedido2} {
		if got := string(m.pedidos[i].cru); got != quer {
			t.Errorf("AS ESCURAS: o pedido %d nao e o golden do AOS-490:\n veio:  %s\n quero: %s", i+1, got, quer)
		}
	}
	if got := aos504TiposDeEvento(m); !reflect.DeepEqual(got, aos505BaseTiposDeEvento) {
		t.Errorf("a captura acrescentou ou tirou eventos:\n veio:  %v\n quero: %v", got, aos505BaseTiposDeEvento)
	}
	turnos, capturas := aos507Partes(t, m)
	if !reflect.DeepEqual(capturas, aos505BaseCapturas) {
		t.Errorf("a parte em claro das capturas mudou:\n veio:  %v\n quero: %v", capturas, aos505BaseCapturas)
	}
	if len(turnos) != 2 {
		t.Fatalf("queria 2 turnos, vieram %d", len(turnos))
	}
	no150 := func(s string) string {
		return strings.Replace(s, `"assembly_version":"1.4.0"`, `"assembly_version":"1.5.0"`, 1)
	}
	if turnos[0] != no150(aos505BaseTurnos[0]) {
		t.Errorf("o turno 1 tinha de ser o da base com o layout 1.5.0 (o mesmo prompt_hash):\n veio:  %s\n quero: %s", turnos[0], no150(aos505BaseTurnos[0]))
	}
	const hashDaBase = `"prompt_hash":"sha256:46dabc9057d403243983af34bda627de8f10eee866ca112b72270d7804e822d0"`
	if !strings.Contains(aos505BaseTurnos[1], hashDaBase) {
		t.Fatalf("pre-condicao: o golden do turno 2 mudou")
	}
	i := strings.Index(turnos[1], `"prompt_hash":"sha256:`)
	if i < 0 || strings.Contains(turnos[1], hashDaBase) {
		t.Fatalf("o prompt_hash do turno 2 tinha de mudar: o tail refere o estado do turno 1: %s", turnos[1])
	}
	if got := turnos[1][:i] + hashDaBase + turnos[1][i+len(hashDaBase):]; got != no150(aos505BaseTurnos[1]) {
		t.Errorf("fora do prompt_hash e do layout o turno 2 mudou:\n veio:  %s\n quero: %s", got, no150(aos505BaseTurnos[1]))
	}

	rep := aos514Replay(t, n, runID, m)
	digestDoTurno1 := ""
	for i, st := range rep.Steps {
		if st.Response.State == nil || st.Response.State.Status != agentruntime.ProviderStateCaptured {
			t.Fatalf("turno %d: a captura nao tem o estado: %+v", i+1, st.Response.State)
		}
		env, err := port.UnmarshalProviderStateEnvelope(st.Response.State.Bytes)
		if err != nil {
			t.Fatalf("turno %d: envelope ilegivel: %v", i+1, err)
		}
		quer := port.ProbeProviderState(aos490RespostaDoProvider(i == 0, "arquivo"))
		if quer == nil || !reflect.DeepEqual(env.ProviderState, *quer) {
			t.Fatalf("turno %d: o estado capturado nao e o que o provider mandou:\n veio:  %+v\n quero: %+v", i+1, env.ProviderState, quer)
		}
		if env.RequestedModel != aos486Modelo || env.ServedModel != "gpt-4o" {
			t.Errorf("turno %d: o estado nao esta ligado a rota: pedido=%q servido=%q", i+1, env.RequestedModel, env.ServedModel)
		}
		if i == 0 {
			digestDoTurno1 = st.Response.State.Digest
			if len(env.ToolCalls) != 1 || string(env.ToolCalls[0].ID) != `"tool_PROVIDER"` || !env.ToolCalls[0].IDUsable {
				t.Errorf("o id de tool call do provider nao foi capturado: %+v", env.ToolCalls)
			}
		}
	}
	// O digest do estado não está em nenhum evento em claro, nem em nenhum pedido.
	for _, ev := range m.eventos {
		if bytes.Contains(ev.Payload, []byte(digestDoTurno1)) || bytes.Contains(ev.Payload, []byte("provider_state")) {
			t.Errorf("o evento %s leva o digest ou o campo do estado: %s", ev.Type, ev.Payload)
		}
	}
	for i, p := range m.pedidos {
		if bytes.Contains(p.cru, []byte(digestDoTurno1)) || bytes.Contains(p.cru, []byte("tool_PROVIDER")) || bytes.Contains(p.cru, []byte("reasoning")) {
			t.Errorf("o pedido %d leva o digest, o id do provider ou o raciocinio", i+1)
		}
	}
	metrics := aos505NoMetrics(t, n)
	for _, quer := range []string{
		`aos_model_provider_state_total{resultado="capturado"} 2`,
		`aos_model_provider_state_total{resultado="nao_devolvivel_tecto"} 0`,
		`aos_model_provider_state_total{resultado="nao_devolvivel_nonce"} 0`,
		`aos_runs_hosted_total{assembly_version="1.5.0"} 1`,
		`aos_runs_hosted_total{assembly_version="1.4.0"} 0`,
	} {
		if !strings.Contains(metrics, quer) {
			t.Errorf("o /metrics nao tem %q", quer)
		}
	}
	// As famílias são as da base mais a do estado, e mais nenhuma.
	quer := append(append([]string(nil), aos505BaseFamiliasDoMetrics...), "aos_model_provider_state_total counter")
	got := aos505NoFamilias(metrics)
	falta := map[string]bool{}
	for _, f := range quer {
		falta[f] = true
	}
	for _, f := range got {
		if !falta[f] {
			t.Errorf("familia inesperada no /metrics: %s", f)
		}
		delete(falta, f)
	}
	if len(falta) != 0 {
		t.Errorf("faltam familias no /metrics: %v", falta)
	}
}

// SELADO, POR SENTINELAS, E BYTE A BYTE. O provider manda um estado com uma sentinela em cada
// campo, em cada assinatura e no id: nenhuma aparece em nenhum evento do run, no `/metrics` nem
// no pedido seguinte; e a captura decifrada devolve cada valor com os bytes exactos que vieram.
// O texto do run é o do `content` — o raciocínio não é resposta —, e o run acaba como acabava.
func TestAOS514_No_Capture_SeladoEByteAByte(t *testing.T) {
	const runID = "run-514-sentinelas"
	t.Setenv("AOS_MODEL_PROVIDER_STATE", "capture")
	aos504SemVariavel(t, "AOS_MODEL_PROVIDER_STATE_MAX_BYTES")
	t.Setenv("AOS_MODEL_RESPONSE_SHAPE", "observe")
	n := aos505NoCompor(t, "", false, "", nil)
	n.upstream.responde = aos514RespostaComEstado
	m := n.correr(t, runID, nil)

	fichas := 0
	for _, ev := range m.eventos {
		for _, s := range aos514Sentinelas {
			if bytes.Contains(ev.Payload, []byte(s)) {
				t.Errorf("o evento %s leva a sentinela %s em claro: %s", ev.Type, s, ev.Payload)
			}
		}
		if ev.Type == agentruntime.EventTypeTurnRecorded && bytes.Contains(ev.Payload, []byte(`"provider_state":"capturado","provider_state_bytes":`)) {
			fichas++
		}
	}
	if fichas != 2 {
		t.Errorf("com a medicao da forma ligada, a ficha dos 2 turnos conta o estado; contou em %d", fichas)
	}
	metrics := aos505NoMetrics(t, n)
	for _, s := range aos514Sentinelas {
		if strings.Contains(metrics, s) {
			t.Errorf("o /metrics leva a sentinela %s", s)
		}
		for i, p := range m.pedidos {
			if bytes.Contains(p.cru, []byte(s)) {
				t.Errorf("o pedido %d leva a sentinela %s: o estado saiu", i+1, s)
			}
		}
	}

	rep := aos514Replay(t, n, runID, m)
	if rep.FinalText != "feito" {
		t.Fatalf("o texto final do run e %q; so pode vir do content", rep.FinalText)
	}
	for i, st := range rep.Steps {
		if st.Response.State == nil {
			t.Fatalf("turno %d sem estado na captura", i+1)
		}
		env, err := port.UnmarshalProviderStateEnvelope(st.Response.State.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		corpo := aos514RespostaComEstado(i == 0, "arquivo")
		for _, f := range env.Fields {
			if !bytes.Contains(corpo, []byte(`"`+f.Name+`":`+string(f.Raw))) {
				t.Errorf("turno %d: o valor capturado de %s (%s) nao sao os bytes que o provider mandou: %s", i+1, f.Name, f.Where, f.Raw)
			}
		}
		if i == 0 {
			if len(env.Fields) != 4 { // reasoning_content, thinking_blocks, e os dois do saco do proxy
				t.Errorf("turno 1: queria 4 campos de raciocinio, vieram %d", len(env.Fields))
			}
			if len(env.ToolCalls) != 1 || string(env.ToolCalls[0].ID) != `"S514-ID"` || len(env.ToolCalls[0].Fields) != 1 || string(env.ToolCalls[0].Fields[0].Raw) != `"S514-TSIG"` {
				t.Errorf("turno 1: id e assinatura da tool call: %+v", env.ToolCalls)
			}
		}
		// O Text do turno nunca vem do estado.
		for _, s := range aos514Sentinelas {
			if strings.Contains(st.Response.Text, s) {
				t.Errorf("turno %d: o texto leva a sentinela %s", i+1, s)
			}
		}
	}
}

// O TECTO, NO NÓ. Com o tecto no mínimo, o estado do turno 1 (blocos assinados) não cabe: não é
// guardado nem truncado, o contador diz a causa, o run segue e conclui, e — sem digest a referir
// — o `prompt_hash` do turno 2 é o de um run sem estado.
func TestAOS514_No_Capture_AcimaDoTectoSegueSemEstado(t *testing.T) {
	const runID = "run-514-tecto"
	grande := strings.Repeat("QUJD", 400)
	t.Setenv("AOS_MODEL_PROVIDER_STATE", "capture")
	t.Setenv("AOS_MODEL_PROVIDER_STATE_MAX_BYTES", "1024")
	aos504SemVariavel(t, "AOS_MODEL_RESPONSE_SHAPE")
	n := aos505NoCompor(t, "", false, "", nil)
	n.upstream.responde = func(pedeTool bool, tool string) []byte {
		if !pedeTool {
			return []byte(`{"model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"feito"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`)
		}
		return []byte(`{"model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"","thinking_blocks":[{"type":"thinking","thinking":"","signature":"` + grande + `"}],` +
			`"tool_calls":[{"id":"call_1","type":"function","function":{"name":"` + tool + `","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`)
	}
	m := n.correr(t, runID, nil)
	rep := aos514Replay(t, n, runID, m)
	if st := rep.Steps[0].Response.State; st == nil || st.Status != agentruntime.ProviderStateNotReturnable || len(st.Bytes) != 0 {
		t.Fatalf("turno 1: o estado acima do tecto tinha de ficar marcado, sem bytes: %+v", st)
	}
	if st := rep.Steps[1].Response.State; st != nil {
		t.Fatalf("turno 2: a resposta nao traz estado: %+v", st)
	}
	if rep.FinalText != "feito" || !rep.Terminated {
		t.Fatalf("o run tinha de concluir como concluia: %+v", rep)
	}
	for _, ev := range m.eventos {
		if bytes.Contains(ev.Payload, []byte(grande[:64])) {
			t.Fatalf("o evento %s leva um bocado do bloco assinado", ev.Type)
		}
	}
	metrics := aos505NoMetrics(t, n)
	if !strings.Contains(metrics, `aos_model_provider_state_total{resultado="nao_devolvivel_tecto"} 1`) || !strings.Contains(metrics, `aos_model_provider_state_total{resultado="capturado"} 0`) {
		t.Errorf("o contador nao diz a causa")
	}
	// O mesmo run SEM estado nenhum no turno 1 tem o mesmo prompt_hash no turno 2.
	sem := aos514Compor(t, "capture", true)
	sem.upstream.responde = func(pedeTool bool, tool string) []byte {
		if !pedeTool {
			return []byte(`{"model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"feito"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`)
		}
		return []byte(`{"model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"type":"function","function":{"name":"` + tool + `","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`)
	}
	ms := sem.correr(t, runID, nil)
	if len(m.turnos) != 2 || len(ms.turnos) != 2 || m.turnos[1].promptHash != ms.turnos[1].promptHash {
		t.Fatalf("um estado nao guardado nao pode mudar o prompt_hash do turno seguinte")
	}
}

// O INTERRUPTOR: vocabulário fechado, omissão `off`, tecto validado, e um valor inválido recusa
// o arranque.
func TestAOS514_Env_VocabularioFechadoTectoEBanner(t *testing.T) {
	if defaultModelProviderState != "off" {
		t.Fatalf("a omissao tem de ser off; e %q", defaultModelProviderState)
	}
	aos504SemVariavel(t, "AOS_MODEL_PROVIDER_STATE_MAX_BYTES")
	for _, mau := range []string{"on", "CAPTURE", "observe", "enforce", "1", "true"} {
		t.Setenv("AOS_MODEL_PROVIDER_STATE", mau)
		if _, _, err := parseModelProviderStateFromEnv(); !errors.Is(err, ErrBadModelProviderState) {
			t.Errorf("%q devia recusar o arranque: %v", mau, err)
		}
		if _, _, _, err := modelProviderStateFromEnv(); !errors.Is(err, ErrBadModelProviderState) {
			t.Errorf("%q devia recusar a composicao: %v", mau, err)
		}
	}
	t.Setenv("AOS_MODEL_PROVIDER_STATE", " capture ")
	modo, max, err := parseModelProviderStateFromEnv()
	if err != nil || modo != "capture" || max != 65536 || modelgateway.DefaultProviderStateMaxBytes != 65536 {
		t.Fatalf("capture: modo=%q tecto=%d err=%v", modo, max, err)
	}
	for _, mau := range []string{"0", "-1", "1023", "262145", "64k", "1e5", "abc", "1024.0"} {
		t.Setenv("AOS_MODEL_PROVIDER_STATE_MAX_BYTES", mau)
		if _, _, err := parseModelProviderStateFromEnv(); !errors.Is(err, ErrBadModelProviderStateMaxBytes) {
			t.Errorf("tecto %q devia recusar o arranque: %v", mau, err)
		}
		// O tecto valida-se mesmo com a captura desligada.
		t.Setenv("AOS_MODEL_PROVIDER_STATE", "off")
		if _, _, err := parseModelProviderStateFromEnv(); !errors.Is(err, ErrBadModelProviderStateMaxBytes) {
			t.Errorf("tecto %q com off devia recusar o arranque: %v", mau, err)
		}
		t.Setenv("AOS_MODEL_PROVIDER_STATE", "capture")
	}
	for _, bom := range []string{"1024", " 262144 ", "65536"} {
		t.Setenv("AOS_MODEL_PROVIDER_STATE_MAX_BYTES", bom)
		if _, _, err := parseModelProviderStateFromEnv(); err != nil {
			t.Errorf("tecto %q devia ser aceite: %v", bom, err)
		}
	}
	linhas := modelProviderStateBanner(true, "capture", 2048)
	if len(linhas) != 1 || !strings.Contains(linhas[0], "AOS_MODEL_PROVIDER_STATE=capture") || !strings.Contains(linhas[0], "2048 bytes") || !strings.Contains(linhas[0], "1.5.0") {
		t.Fatalf("banner: %v", linhas)
	}
	if modelProviderStateBanner(false, "capture", 2048) != nil {
		t.Fatalf("sem gateway composto nao ha banner")
	}
	if layoutDosRunsNovos(false) != agentruntime.AssemblyVersion || layoutDosRunsNovos(true) != agentruntime.AssemblyVersion150 {
		t.Fatalf("o layout dos runs novos so muda com a captura ligada")
	}
	// Os contadores: vocabulário fechado, e um resultado desconhecido não cria série.
	c := novosContadoresDoEstado()
	c.observar(modelgateway.ProviderStateResultCaptured)
	c.observar("TEXTO-LIVRE")
	(*contadoresDoEstado)(nil).observar("capturado")
	if c.lido(modelgateway.ProviderStateResultCaptured) != 1 || len(c.total) != 3 || c.lido("TEXTO-LIVRE") != 0 {
		t.Fatalf("contadores: %+v", c.total)
	}
}
