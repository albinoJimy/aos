package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// AOS-448 — o `replay.captured` leva o consumo medido ou uma marca explícita de não medido, nunca
// um zero mudo.
//
// O defeito medido em produção (`plan-e2e-docread-1790340990~n1`, v0.1.33): o `turn.recorded` do
// turno tinha 434+1523 tokens e o `replay.captured` do MESMO turno tinha 0+0. A captura recebia o
// usage — é o mesmo `resp` do loop — e selava-o dentro do envelope por-titular; o que ficava à
// vista era o valor-zero de `responseCapture{}`, sem `omitempty` nos campos de consumo. O mode 3
// (PayloadStore) tinha a mesma forma.

// zeroMudo é a INVARIANTE do ticket, aplicada aos bytes do evento tal como vão ao WAL: um
// `response.input_tokens` a zero só é legítimo com `usage_ausente: true`. Zero tokens de entrada
// nunca é uma medição (não há chamada de modelo sem system+user — [agentruntime.Usage.Definido]).
func zeroMudo(raw []byte) error {
	var ev struct {
		Response struct {
			InputTokens  *int64 `json:"input_tokens"`
			UsageAusente bool   `json:"usage_ausente"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return fmt.Errorf("evento ilegível: %w", err)
	}
	if ev.Response.InputTokens == nil {
		return fmt.Errorf("evento sem response.input_tokens: %s", raw)
	}
	if *ev.Response.InputTokens <= 0 && !ev.Response.UsageAusente {
		return fmt.Errorf("zero mudo: input_tokens=%d sem usage_ausente: %s", *ev.Response.InputTokens, raw)
	}
	return nil
}

// modosDeCaptura são os três caminhos de escrita do capturer: inline (AOS-016), selado
// por-titular (AOS-093 — o de PRODUÇÃO, `cmd/aos/bootstrap.go`) e mode 3 (AOS-079).
func modosDeCaptura() map[string]func() []CapturerOption {
	return map[string]func() []CapturerOption{
		"inline": func() []CapturerOption { return nil },
		"selado": func() []CapturerOption {
			return []CapturerOption{WithContentSealer(newCapFakeCipher())}
		},
		"mode3": func() []CapturerOption {
			return []CapturerOption{WithPayloadStore(NewInMemoryPayloadStore(), writerAccessor)}
		},
	}
}

func capturarEm(t *testing.T, opts []CapturerOption, usage agentruntime.Usage) []byte {
	t.Helper()
	fa := &fakeAppender{}
	c, err := NewCapturer(fa, opts...)
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	tc := sampleTurnCapture()
	tc.Subject = "nhi:agente-aos448" // o selador só actua com titular, como no nó
	tc.Response.Usage = usage
	if err := c.Capture(context.Background(), tc); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if len(fa.got) != 1 {
		t.Fatalf("esperava 1 evento, obtive %d", len(fa.got))
	}
	return fa.got[0].Payload
}

// TestAOS448_CapturaNuncaGravaZeroMudo é o teste que avermelha o defeito: com o código anterior os
// casos `selado/medido` e `mode3/medido` falham com «zero mudo».
func TestAOS448_CapturaNuncaGravaZeroMudo(t *testing.T) {
	usages := map[string]agentruntime.Usage{
		"medido":             {InputTokens: 434, OutputTokens: 1523},
		"nao-medido-zero":    {},
		"nao-medido-marcado": {InputTokens: 7, OutputTokens: 3, Ausente: true},
	}
	for modo, opts := range modosDeCaptura() {
		for nome, u := range usages {
			t.Run(modo+"/"+nome, func(t *testing.T) {
				raw := capturarEm(t, opts(), u)
				if err := zeroMudo(raw); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// TestAOS448_MarcaNosBytesDoEventoEmCadaModo — o caso que o `zeroMudo` não vê: um usage marcado
// Ausente pelo gateway com números > 0 (input 7 passa a invariante sem marca nenhuma). A marca tem
// de estar NOS BYTES do evento que vai ao WAL, em cada um dos três modos — fora do inline é o
// resumo de consumo ([responseCapture.consumo]) que a tem de carregar.
func TestAOS448_MarcaNosBytesDoEventoEmCadaModo(t *testing.T) {
	for modo, opts := range modosDeCaptura() {
		t.Run(modo, func(t *testing.T) {
			raw := capturarEm(t, opts(), agentruntime.Usage{InputTokens: 7, OutputTokens: 3, Ausente: true})
			var ev struct {
				Response struct {
					InputTokens  int64 `json:"input_tokens"`
					OutputTokens int64 `json:"output_tokens"`
					UsageAusente *bool `json:"usage_ausente"`
				} `json:"response"`
			}
			if err := json.Unmarshal(raw, &ev); err != nil {
				t.Fatal(err)
			}
			if ev.Response.UsageAusente == nil || !*ev.Response.UsageAusente {
				t.Fatalf("a marca de não medido não está nos bytes do evento %s: %s", modo, raw)
			}
			if ev.Response.InputTokens != 7 || ev.Response.OutputTokens != 3 {
				t.Fatalf("os números do turno marcado perderam-se no evento %s: %s", modo, raw)
			}
		})
	}
}

// TestAOS448_ConsumoMedidoFicaEmClaro — num turno medido, o evento de cada modo leva os números
// REAIS e nenhuma marca; e, fora do inline, leva SÓ o consumo (nenhum conteúdo volta ao WAL).
func TestAOS448_ConsumoMedidoFicaEmClaro(t *testing.T) {
	for modo, opts := range modosDeCaptura() {
		t.Run(modo, func(t *testing.T) {
			raw := capturarEm(t, opts(), agentruntime.Usage{InputTokens: 434, OutputTokens: 1523})
			var p capturePayload
			if err := json.Unmarshal(raw, &p); err != nil {
				t.Fatal(err)
			}
			r := p.Response
			if r.InputTokens != 434 || r.OutputTokens != 1523 || r.CostMicroUSD != 1200 {
				t.Fatalf("consumo perdido no evento %s: %+v", modo, r)
			}
			if r.UsageAusente {
				t.Fatalf("turno medido marcado como não medido (%s)", modo)
			}
			if modo == "inline" {
				return
			}
			if r.Text != "" || len(r.ToolCalls) != 0 || len(p.ToolResults) != 0 {
				t.Fatalf("conteúdo voltou ao evento em %s: %+v", modo, p)
			}
			for _, needle := range []string{"olá", "echoed:x", `"tool_calls"`} {
				if bytes.Contains(raw, []byte(needle)) {
					t.Fatalf("o evento %s contém %q — só o consumo pode ficar em claro: %s", modo, needle, raw)
				}
			}
		})
	}
}

// TestAOS448_ConsumoSeladoIgualAoDoEnvelope — o que fica em claro é EXACTAMENTE a medição que está
// selada: o replay (que substitui o `response` pelo decifrado) e o leitor do WAL vêem o mesmo.
func TestAOS448_ConsumoSeladoIgualAoDoEnvelope(t *testing.T) {
	cipher := newCapFakeCipher()
	raw := capturarEm(t, []CapturerOption{WithContentSealer(cipher)}, agentruntime.Usage{InputTokens: 915, OutputTokens: 165})
	var p capturePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	// O duplo de teste é um XOR com (chave ^ 0x5c) atrás de um byte de cabeçalho — invertível aqui.
	k := cipher.keys[p.SealedSubject]
	plain := make([]byte, len(p.SealedContent)-1)
	for i, b := range p.SealedContent[1:] {
		plain[i] = b ^ k ^ 0x5c
	}
	var sc sealedContent
	if err := json.Unmarshal(plain, &sc); err != nil {
		t.Fatalf("envelope ilegível: %v", err)
	}
	claro, _ := json.Marshal(p.Response)
	selado, _ := json.Marshal(sc.Response.consumo())
	if !bytes.Equal(claro, selado) || sc.Response.InputTokens != 915 {
		t.Fatalf("o consumo em claro diverge do selado:\n claro:  %+v\n selado: %+v", p.Response, sc.Response.consumo())
	}
}

// TestAOS448_MarcaAtravessaARetoma — a marca volta na descodificação (o cliente de replay entrega a
// resposta ao loop, que a regrava no `turn.recorded` da retoma). Sem isto, um usage marcado Ausente
// com números > 0 voltaria da captura como medição.
func TestAOS448_MarcaAtravessaARetoma(t *testing.T) {
	c := &EventStoreCapturer{}
	resp := agentruntime.ModelResponse{Text: "x", Usage: agentruntime.Usage{InputTokens: 7, OutputTokens: 3, Ausente: true}}
	bs, err := json.Marshal(c.encodeResponse(resp))
	if err != nil {
		t.Fatal(err)
	}
	var rc responseCapture
	if err := json.Unmarshal(bs, &rc); err != nil {
		t.Fatal(err)
	}
	if got := rc.decode().Usage; got.Definido() || !got.Ausente {
		t.Fatalf("a marca perdeu-se na captura: %+v (%s)", got, bs)
	}
}

// TestAOS448_CapturasAntigasDescodificamComoAntes — COMPATIBILIDADE DO REPLAY. As capturas gravadas
// antes deste ticket não têm `usage_ausente`, e as seladas têm o `response` exterior a zeros. As
// duas têm de continuar legíveis com o significado de sempre, e um turno medido tem de continuar a
// gravar os bytes de sempre (sem o campo novo) — é isso que mantém as goldens no mesmo digest.
func TestAOS448_CapturasAntigasDescodificamComoAntes(t *testing.T) {
	// (1) Inline medido, bytes anteriores ao AOS-448.
	antigaInline := []byte(`{"schema_version":"1.0","turn":1,"response":{"text":"olá","final":true,"input_tokens":10,"output_tokens":5,"cost_micro_usd":1200},"observed_at_unix_nano":1}`)
	var p capturePayload
	if err := json.Unmarshal(antigaInline, &p); err != nil {
		t.Fatal(err)
	}
	got := p.Response.decode()
	if got.Usage != (agentruntime.Usage{InputTokens: 10, OutputTokens: 5}) || got.CostMicroUSD != 1200 || !got.Final {
		t.Fatalf("captura antiga descodificada de outra forma: %+v", got)
	}

	// (2) Selada, exterior a zeros (a forma em produção até à v0.1.34): continua a descodificar, e o
	// replay nunca lê este exterior — substitui-o pelo decifrado ([resolveSealed]).
	antigaSelada := []byte(`{"schema_version":"1.0","turn":1,"response":{"final":false,"input_tokens":0,"output_tokens":0,"cost_micro_usd":0},"observed_at_unix_nano":1,"sealed_content":"fw==","sealed_subject":"nhi:x"}`)
	p = capturePayload{}
	if err := json.Unmarshal(antigaSelada, &p); err != nil {
		t.Fatal(err)
	}
	if p.Response.UsageAusente || p.Response.decode().Usage.Ausente {
		t.Fatal("uma captura antiga ganhou uma marca que nunca teve")
	}

	// (3) Um turno medido serializa-se sem o campo novo: bytes de sempre.
	bs, err := json.Marshal((&EventStoreCapturer{}).encodeResponse(agentruntime.ModelResponse{
		Text: "olá", Final: true, Usage: agentruntime.Usage{InputTokens: 10, OutputTokens: 5}, CostMicroUSD: 1200,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"text":"olá","final":true,"input_tokens":10,"output_tokens":5,"cost_micro_usd":1200}`; string(bs) != want {
		t.Fatalf("a resposta medida mudou de bytes (digest da captura):\n got:  %s\n want: %s", bs, want)
	}

	// (4) O EVENTO INTEIRO de um turno medido inline, fixado byte a byte na forma anterior ao
	// AOS-448 (a dos structs da HEAD de então, sem `usage_ausente`): é esta a forma das goldens.
	fa := &fakeAppender{}
	c, err := NewCapturer(fa, WithClock(func() time.Time { return time.Unix(1000, 0).UTC() }))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Capture(context.Background(), sampleTurnCapture()); err != nil {
		t.Fatal(err)
	}
	const evento = `{"schema_version":"1.0","turn":1,` +
		`"response":{"text":"olá","tool_calls":[{"tool_id":"echo","capability":"cap:echo","input":"eA=="}],` +
		`"final":false,"input_tokens":10,"output_tokens":5,"cost_micro_usd":1200},` +
		`"tool_results":[{"taint":"untrusted","output":"ZWNob2VkOng="}],` +
		`"observed_at_unix_nano":1000000000000}`
	if got := string(fa.got[0].Payload); got != evento {
		t.Fatalf("o replay.captured inline de um turno medido mudou de bytes:\n got:  %s\n want: %s", got, evento)
	}
}
