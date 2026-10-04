package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// AOS-491 — a captura do turno guarda o motivo de paragem e o replay devolve-o igual; uma captura
// anterior ao ticket reproduz-se com o motivo vazio, sem divergência.

func aos491Resposta(motivo agentruntime.StopReason) agentruntime.ModelResponse {
	return agentruntime.ModelResponse{
		Text:       "cortado a mei",
		Usage:      agentruntime.Usage{InputTokens: 380, OutputTokens: 12},
		StopReason: motivo,
	}
}

// INLINE: cada motivo do vocabulário vai e volta igual; um texto fora dele é guardado como `other`.
func TestAOS491_Captura_Inline_MotivoVoltaIgual(t *testing.T) {
	for _, motivo := range agentruntime.StopReasons() {
		raw := aos490Capturar(t, nil, aos491Resposta(motivo))
		var p capturePayload
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		if got := p.Response.decode().StopReason; got != motivo {
			t.Fatalf("motivo %q voltou como %q", motivo, got)
		}
	}
	const bruto = "MOTIVO-BRUTO-DO-PROVIDER"
	raw := aos490Capturar(t, nil, aos491Resposta(bruto))
	if bytes.Contains(raw, []byte(bruto)) || !bytes.Contains(raw, []byte(`"stop_reason":"other"`)) {
		t.Fatalf("um motivo fora do vocabulario tinha de ser guardado como other: %s", raw)
	}
}

// SELADO (o modo de PRODUÇÃO) e MODE 3: o motivo é medição — fica em claro no resumo de consumo,
// como os contadores, e também dentro do conteúdo selado, que é o que o replay lê.
func TestAOS491_Captura_SeladaEMode3_MotivoNoResumoDeConsumo(t *testing.T) {
	cipher := newCapFakeCipher()
	raw := aos490Capturar(t, []CapturerOption{WithContentSealer(cipher)}, aos491Resposta(agentruntime.StopLength))
	var p capturePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.Response.StopReason != "length" || p.Response.Text != "" {
		t.Fatalf("o resumo em claro devia levar o motivo e nao o texto: %+v", p.Response)
	}
	if got := aos490Abrir(t, cipher, p).Response.decode().StopReason; got != agentruntime.StopLength {
		t.Fatalf("o envelope devolve o motivo %q, quero length", got)
	}

	raw = aos490Capturar(t, []CapturerOption{WithPayloadStore(NewInMemoryPayloadStore(), writerAccessor)}, aos491Resposta(agentruntime.StopContentFilter))
	if !bytes.Contains(raw, []byte(`"stop_reason":"content_filter"`)) {
		t.Fatalf("o evento de mode 3 devia levar o motivo no resumo de consumo: %s", raw)
	}
}

// COMPATIBILIDADE: um turno sem motivo grava os bytes de sempre; com motivo, a chave nova vem no
// fim e é a única nova; uma captura gravada antes do ticket descodifica com o motivo vazio.
func TestAOS491_Captura_SemMotivo_BytesDeSempre(t *testing.T) {
	const antiga = `{"text":"olá","final":true,"input_tokens":10,"output_tokens":5,"cost_micro_usd":1200}`
	bs, err := json.Marshal((&EventStoreCapturer{}).encodeResponse(agentruntime.ModelResponse{
		Text: "olá", Final: true, Usage: agentruntime.Usage{InputTokens: 10, OutputTokens: 5}, CostMicroUSD: 1200,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if string(bs) != antiga {
		t.Fatalf("a resposta sem motivo mudou de bytes:\n got:  %s\n want: %s", bs, antiga)
	}
	var rc responseCapture
	if err := json.Unmarshal([]byte(antiga), &rc); err != nil {
		t.Fatal(err)
	}
	if got := rc.decode(); got.StopReason != agentruntime.StopUnreported || got.Text != "olá" || !got.Final {
		t.Fatalf("captura antiga descodificada de outra forma: %+v", got)
	}
	bs, _ = json.Marshal((&EventStoreCapturer{}).encodeResponse(agentruntime.ModelResponse{
		Text: "olá", Final: true, Usage: agentruntime.Usage{InputTokens: 10, OutputTokens: 5}, CostMicroUSD: 1200, StopReason: agentruntime.StopStop,
	}))
	if want := antiga[:len(antiga)-1] + `,"stop_reason":"stop"}`; string(bs) != want {
		t.Fatalf("resposta com motivo:\n got:  %s\n want: %s", bs, want)
	}
}

// aos491ComMotivos devolve o guião de referência do AOS-489 com um motivo de paragem por turno.
func aos491ComMotivos() ([]agentruntime.ModelResponse, []agentruntime.StopReason) {
	motivos := []agentruntime.StopReason{
		agentruntime.StopToolCalls, agentruntime.StopOther, agentruntime.StopLength, agentruntime.StopUnreported, agentruntime.StopStop,
	}
	guiao := aos489Guiao()
	for i := range guiao {
		guiao[i].StopReason = motivos[i]
	}
	return guiao, motivos
}

// O REPLAY DEVOLVE O MOTIVO IGUAL, turno a turno, com fidelidade 1.0 — e o motivo não entra no
// prompt: o mesmo run sem motivos grava os mesmos `prompt_hash`.
func TestAOS491_Replay_DevolveOMotivoDeCadaTurno(t *testing.T) {
	guiao, motivos := aos491ComMotivos()
	b := novaBancada(t)
	goal := aos489Goal("run-aos491-com-motivo")
	if res, err := b.correr(goal, guiao); err != nil || !res.Terminated || res.Turns != 5 {
		t.Fatalf("Run: res=%+v err=%v", res, err)
	}
	spec := aos489SpecDe(goal)
	spec.AssemblyVersion = agentruntime.AssemblyVersion140
	res, err := b.motor().Replay(context.Background(), goal.RunID, Options{Spec: spec, VerifyAuthority: true})
	exigirFiel(t, res, err, 5)
	if !res.Terminated || res.FinalText != "concluido" {
		t.Fatalf("o replay devia terminar onde o run terminou: %+v", res)
	}
	for i, st := range res.Steps {
		if st.Response.StopReason != motivos[i] {
			t.Fatalf("turno %d: o replay devolveu o motivo %q, quero %q", st.Turn, st.Response.StopReason, motivos[i])
		}
	}

	semMotivo := novaBancada(t)
	goalSem := aos489Goal("run-aos491-sem-motivo")
	if res, err := semMotivo.correr(goalSem, aos489Guiao()); err != nil || !res.Terminated || res.Turns != 5 {
		t.Fatalf("Run sem motivo: res=%+v err=%v", res, err)
	}
	com, sem := manifestosDe(t, b.eventos(goal.RunID)), manifestosDe(t, semMotivo.eventos(goalSem.RunID))
	if len(com) != 5 || len(sem) != 5 {
		t.Fatalf("queria 5 turnos em cada run: %d e %d", len(com), len(sem))
	}
	for i := range com {
		if com[i].PromptHash != sem[i].PromptHash {
			t.Fatalf("turno %d: o motivo de paragem mudou o prompt_hash (%s contra %s)", i+1, com[i].PromptHash, sem[i].PromptHash)
		}
	}
}

// UM LOG GRAVADO ANTES DO TICKET — o `testdata/aos489_log_1_3_0.json`, escrito por código que não
// conhecia o campo — reproduz-se com o motivo VAZIO em todos os turnos, fidelidade 1.0 e a
// trajectória inteira: nem `prompt_hash` nem o turno em que o run termina mudam.
func TestAOS491_Replay_LogAnteriorAoTicket_MotivoVazioSemDivergencia(t *testing.T) {
	fx := carregarFixture130(t)
	for _, r := range fx.Runs {
		turnos := len(manifestosDe(t, r.Events))
		if turnos == 0 {
			t.Fatalf("run %s da fixture sem turnos", r.RunID)
		}
		for _, ev := range r.Events {
			if bytes.Contains(ev.Payload, []byte("stop_reason")) || bytes.Contains(ev.Payload, []byte("tools_offered")) {
				t.Fatalf("a fixture devia ser anterior ao AOS-491 e tem os campos novos: %s", ev.Payload)
			}
		}
		res, err := motorSobre(t, r).Replay(context.Background(), r.RunID, Options{Spec: r.spec()})
		exigirFiel(t, res, err, turnos)
		for _, st := range res.Steps {
			if st.Response.StopReason != agentruntime.StopUnreported {
				t.Fatalf("run %s turno %d: uma captura antiga devolveu o motivo %q", r.RunID, st.Turn, st.Response.StopReason)
			}
		}
	}
}
