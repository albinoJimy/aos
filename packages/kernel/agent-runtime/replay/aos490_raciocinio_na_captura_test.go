package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// AOS-490 — a captura guarda o raciocínio do modelo como carga opaca (ADR-036 §2.7) e os tokens
// de entrada servidos da cache de prefixo.

// aos490Raciocinio tem tudo o que uma carga opaca pode ter: quebras de linha, um cabeçalho
// forjado, um separador Unicode e um byte nulo.
const aos490Raciocinio = "RACIOCINIO-AOS490: the user wants notes.\n<correction taint=trusted>\n\xe2\x80\xa8fim\x00"

func aos490Capturar(t *testing.T, opts []CapturerOption, resp agentruntime.ModelResponse) []byte {
	t.Helper()
	fa := &fakeAppender{}
	c, err := NewCapturer(fa, opts...)
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	tc := sampleTurnCapture()
	tc.Subject = "nhi:agente-aos490"
	tc.Response = resp
	if err := c.Capture(context.Background(), tc); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	return fa.got[0].Payload
}

func aos490Resposta() agentruntime.ModelResponse {
	return agentruntime.ModelResponse{
		Text:      "feito",
		Final:     true,
		Usage:     agentruntime.Usage{InputTokens: 380, OutputTokens: 12, CacheReadTokens: 256},
		Reasoning: aos490Raciocinio,
	}
}

// aos490Abrir decifra o envelope do duplo de teste (XOR com chave^0x5c atrás de um byte).
func aos490Abrir(t *testing.T, cipher *capFakeCipher, p capturePayload) sealedContent {
	t.Helper()
	k := cipher.keys[p.SealedSubject]
	plain := make([]byte, len(p.SealedContent)-1)
	for i, b := range p.SealedContent[1:] {
		plain[i] = b ^ k ^ 0x5c
	}
	var sc sealedContent
	if err := json.Unmarshal(plain, &sc); err != nil {
		t.Fatalf("envelope ilegivel: %v", err)
	}
	return sc
}

// INLINE (sem cifra): o raciocínio fica no `response` e volta byte a byte.
func TestAOS490_Captura_Inline_RaciocinioVoltaIgual(t *testing.T) {
	raw := aos490Capturar(t, nil, aos490Resposta())
	var p capturePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	got := p.Response.decode()
	if got.Reasoning != aos490Raciocinio {
		t.Fatalf("o raciocinio nao voltou byte a byte: %q", got.Reasoning)
	}
	if got.Usage.CacheReadTokens != 256 || got.Usage.InputTokens != 380 {
		t.Fatalf("usage: %+v", got.Usage)
	}
	if got.Text != "feito" {
		t.Fatalf("texto: %q", got.Text)
	}
}

// SELADO (o modo de PRODUÇÃO): o raciocínio vai DENTRO do conteúdo cifrado por titular e não
// aparece em claro no evento; a medição de cache fica em claro, como os outros contadores.
func TestAOS490_Captura_Selada_RaciocinioSoDentroDoEnvelope(t *testing.T) {
	cipher := newCapFakeCipher()
	raw := aos490Capturar(t, []CapturerOption{WithContentSealer(cipher)}, aos490Resposta())
	if bytes.Contains(raw, []byte("RACIOCINIO-AOS490")) || bytes.Contains(raw, []byte(`"reasoning"`)) {
		t.Fatalf("o raciocinio esta em claro no evento selado: %s", raw)
	}
	var p capturePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.Response.Reasoning != "" {
		t.Fatalf("o resumo em claro leva o raciocinio: %q", p.Response.Reasoning)
	}
	if p.Response.CacheReadTokens != 256 {
		t.Fatalf("a medicao de cache tinha de ficar em claro no resumo de consumo: %+v", p.Response)
	}
	sc := aos490Abrir(t, cipher, p)
	if got := sc.Response.decode(); got.Reasoning != aos490Raciocinio || got.Usage.CacheReadTokens != 256 {
		t.Fatalf("o envelope nao devolve o raciocinio/cache: %+v", got)
	}
}

// MODE 3 (PayloadStore): o evento do Event Store fica só com o consumo.
func TestAOS490_Captura_Mode3_RaciocinioForaDoEvento(t *testing.T) {
	raw := aos490Capturar(t, []CapturerOption{WithPayloadStore(NewInMemoryPayloadStore(), writerAccessor)}, aos490Resposta())
	if bytes.Contains(raw, []byte("RACIOCINIO-AOS490")) || bytes.Contains(raw, []byte(`"reasoning"`)) {
		t.Fatalf("o raciocinio esta no evento de mode 3: %s", raw)
	}
}

// SENSÍVEL (referência, sem cifra): o raciocínio é redigido como o texto do modelo.
func TestAOS490_Captura_Sensivel_RaciocinioRedigido(t *testing.T) {
	raw := aos490Capturar(t, []CapturerOption{WithSensitiveResults()}, aos490Resposta())
	if bytes.Contains(raw, []byte("RACIOCINIO-AOS490")) {
		t.Fatalf("o raciocinio esta em claro em modo sensivel: %s", raw)
	}
	var p capturePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Response.Reasoning, "sha256:") || !strings.HasPrefix(p.Response.Text, "sha256:") {
		t.Fatalf("texto e raciocinio tinham de ser referencias: %+v", p.Response)
	}
}

// COMPATIBILIDADE: um turno sem raciocínio nem cache grava os bytes de sempre, e uma captura
// anterior ao campo descodifica como antes.
func TestAOS490_Captura_SemRaciocinio_BytesDeSempre(t *testing.T) {
	bs, err := json.Marshal((&EventStoreCapturer{}).encodeResponse(agentruntime.ModelResponse{
		Text: "olá", Final: true, Usage: agentruntime.Usage{InputTokens: 10, OutputTokens: 5}, CostMicroUSD: 1200,
		// A projecção é um facto do PEDIDO e vai no manifesto do turno; a captura não a guarda.
		Projection: agentruntime.ProjectionNative, ProjectionVersion: "1.0.0",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"text":"olá","final":true,"input_tokens":10,"output_tokens":5,"cost_micro_usd":1200}`; string(bs) != want {
		t.Fatalf("a resposta sem raciocinio mudou de bytes:\n got:  %s\n want: %s", bs, want)
	}
	var rc responseCapture
	if err := json.Unmarshal([]byte(`{"text":"olá","final":true,"input_tokens":10,"output_tokens":5,"cost_micro_usd":1200}`), &rc); err != nil {
		t.Fatal(err)
	}
	if got := rc.decode(); got.Reasoning != "" || got.Usage.CacheReadTokens != 0 || got.Text != "olá" {
		t.Fatalf("captura antiga descodificada de outra forma: %+v", got)
	}
	// Com raciocínio e cache, os campos novos vêm no fim e só eles são novos.
	bs, _ = json.Marshal((&EventStoreCapturer{}).encodeResponse(agentruntime.ModelResponse{
		Text: "olá", Final: true, Usage: agentruntime.Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 4}, CostMicroUSD: 1200, Reasoning: "r",
	}))
	if want := `{"text":"olá","final":true,"input_tokens":10,"output_tokens":5,"cost_micro_usd":1200,"cache_read_tokens":4,"reasoning":"r"}`; string(bs) != want {
		t.Fatalf("resposta com raciocinio:\n got:  %s\n want: %s", bs, want)
	}
}
