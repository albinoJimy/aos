package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// AOS-093 — cifra POR-TITULAR do conteúdo não-determinístico do capturer de replay.
// Prova que, com [WithContentSealer] e um titular ([TurnCapture.Subject]), a resposta
// do modelo e os resultados de tools NÃO tocam o evento em claro (migram para dentro do
// envelope cifrado) e que o titular é registado — o texto-claro nunca vai ao WAL.
//
// O envelope real é provado em platform/audit; aqui usa-se um TEST DOUBLE que torna o
// plaintext ausente dos bytes selados (sem cruzar a fronteira de módulo).

type capFakeCipher struct {
	mu   sync.Mutex
	keys map[string]byte
}

func newCapFakeCipher() *capFakeCipher { return &capFakeCipher{keys: map[string]byte{}} }

func (c *capFakeCipher) SealContent(_ context.Context, subject, _ string, pt []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k, ok := c.keys[subject]
	if !ok {
		k = byte(len(c.keys) + 1)
		c.keys[subject] = k
	}
	out := make([]byte, len(pt)+1)
	out[0] = 0x7f
	for i, b := range pt {
		out[i+1] = b ^ k ^ 0x5c
	}
	return out, nil
}

func sealedSampleCapture(subject string) agentruntime.TurnCapture {
	tc := sampleTurnCapture()
	tc.Subject = subject
	// Injecta PII SINTÉTICA no texto do modelo e no output da tool.
	tc.Response.Text = "resposta com dado sintetico ZORG-SYNTH-9911"
	tc.ToolResults[0].Result = agentruntime.Untrusted([]byte("output: SYNTH-SECRET-ABC"))
	return tc
}

func TestCapturer_ContentSealer_NoClearContentInEvent(t *testing.T) {
	fa := &fakeAppender{}
	cipher := newCapFakeCipher()
	const subject = "nhi:agent-cap-synth"
	c, err := NewCapturer(fa, WithContentSealer(cipher))
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	if err := c.Capture(context.Background(), sealedSampleCapture(subject)); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if len(fa.got) != 1 {
		t.Fatalf("esperava 1 evento, obtive %d", len(fa.got))
	}
	raw := fa.got[0].Payload

	// Confidencialidade: a PII sintética NÃO aparece no evento (que iria ao WAL).
	for _, needle := range []string{"ZORG-SYNTH-9911", "SYNTH-SECRET-ABC", "resposta com dado"} {
		if bytes.Contains(raw, []byte(needle)) {
			t.Fatalf("evento contém conteúdo em CLARO %q — confidencialidade violada", needle)
		}
	}

	var p capturePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// O conteúdo migrou para o envelope selado; os campos em claro estão vazios.
	if len(p.SealedContent) == 0 {
		t.Fatal("SealedContent devia estar preenchido")
	}
	if p.SealedSubject != subject {
		t.Fatalf("SealedSubject = %q, esperava %q", p.SealedSubject, subject)
	}
	if p.Response.Text != "" || len(p.Response.ToolCalls) != 0 || len(p.ToolResults) != 0 {
		t.Fatalf("campos em claro deviam estar vazios sob cifra: %+v", p)
	}
	// Metadados não-PII preservados.
	if p.Turn != 1 {
		t.Fatalf("Turn devia ser preservado: %d", p.Turn)
	}
}

// TestCapturer_NoSealer_InlineByteIdentical confirma retro-compat: sem sealer, o
// conteúdo é inline (SealedContent vazio) — o comportamento AOS-016 mantém-se.
func TestCapturer_NoSealer_InlineByteIdentical(t *testing.T) {
	fa := &fakeAppender{}
	c, _ := NewCapturer(fa)
	if err := c.Capture(context.Background(), sealedSampleCapture("nhi:x")); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	var p capturePayload
	if err := json.Unmarshal(fa.got[0].Payload, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(p.SealedContent) != 0 || p.SealedSubject != "" {
		t.Fatal("sem sealer não devia haver conteúdo selado (retro-compat)")
	}
	if p.Response.Text == "" {
		t.Fatal("sem sealer o conteúdo devia ser inline em claro (AOS-016)")
	}
}
