package replay

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// fakeAppender captura os EventInput submetidos (isola o capturer do ES real).
type fakeAppender struct {
	got []eventstore.EventInput
	err error
	seq uint64
}

func (f *fakeAppender) Append(_ context.Context, _ string, in eventstore.EventInput, _ ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	if f.err != nil {
		return eventstore.AppendResult{}, f.err
	}
	f.got = append(f.got, in)
	f.seq++
	return eventstore.AppendResult{Seq: f.seq, Status: eventstore.StatusCommitted}, nil
}

func sampleTurnCapture() agentruntime.TurnCapture {
	return agentruntime.TurnCapture{
		RunID:  "run1",
		StepID: "step-000001",
		Turn:   1,
		Response: agentruntime.ModelResponse{
			Text:         "olá",
			ToolCalls:    []agentruntime.ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("x")}},
			Usage:        agentruntime.Usage{InputTokens: 10, OutputTokens: 5},
			CostMicroUSD: 1200,
		},
		ToolResults: []agentruntime.CapturedToolResult{
			{
				Invocation: agentruntime.ToolInvocation{ToolID: "echo"},
				Result:     agentruntime.Untrusted([]byte("echoed:x")),
			},
		},
	}
}

func TestCapturerPersistsCanonicalEvent(t *testing.T) {
	fa := &fakeAppender{}
	c, err := NewCapturer(fa, WithClock(func() time.Time { return time.Unix(1000, 0).UTC() }))
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	if err := c.Capture(context.Background(), sampleTurnCapture()); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if len(fa.got) != 1 {
		t.Fatalf("esperava 1 evento, obtive %d", len(fa.got))
	}
	ev := fa.got[0]
	if ev.Type != EventTypeCaptured {
		t.Fatalf("tipo = %q, esperava %q", ev.Type, EventTypeCaptured)
	}
	// Envelope namespaced "cap-" (distinto do turn.recorded / ledger / checkpoint).
	if ev.StepID != "cap-step-000001" {
		t.Fatalf("step_id do envelope = %q, esperava cap-step-000001", ev.StepID)
	}
	if ev.ParentStepID != "step-000001" {
		t.Fatalf("parent_step_id = %q", ev.ParentStepID)
	}
	var p capturePayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.Turn != 1 || p.Response.Text != "olá" || p.Response.CostMicroUSD != 1200 {
		t.Fatalf("payload de resposta errado: %+v", p.Response)
	}
	if len(p.Response.ToolCalls) != 1 || p.Response.ToolCalls[0].ToolID != "echo" {
		t.Fatalf("tool calls não capturadas: %+v", p.Response.ToolCalls)
	}
	if len(p.ToolResults) != 1 || string(p.ToolResults[0].Output) != "echoed:x" {
		t.Fatalf("resultado de tool não capturado: %+v", p.ToolResults)
	}
	if p.ObservedAtUnixNano != time.Unix(1000, 0).UTC().UnixNano() {
		t.Fatalf("relógio de captura errado: %d", p.ObservedAtUnixNano)
	}
}

// Serialização canónica/estável: os mesmos inputs produzem sempre os mesmos bytes.
func TestCaptureSerializationDeterministic(t *testing.T) {
	fa1, fa2 := &fakeAppender{}, &fakeAppender{}
	clk := func() time.Time { return time.Unix(1000, 0).UTC() }
	c1, _ := NewCapturer(fa1, WithClock(clk))
	c2, _ := NewCapturer(fa2, WithClock(clk))
	if err := c1.Capture(context.Background(), sampleTurnCapture()); err != nil {
		t.Fatal(err)
	}
	if err := c2.Capture(context.Background(), sampleTurnCapture()); err != nil {
		t.Fatal(err)
	}
	if string(fa1.got[0].Payload) != string(fa2.got[0].Payload) {
		t.Fatalf("serialização não determinística:\n%s\n%s", fa1.got[0].Payload, fa2.got[0].Payload)
	}
}

// Modo sensível: o output NÃO é gravado em claro — só uma referência (hash).
func TestCaptureSensitiveResultsStoresReferenceOnly(t *testing.T) {
	fa := &fakeAppender{}
	c, _ := NewCapturer(fa, WithSensitiveResults(), WithClock(func() time.Time { return time.Unix(1, 0) }))
	tc := sampleTurnCapture()
	tc.ToolResults[0].Result = agentruntime.Untrusted([]byte("PII-SENSÍVEL-1234"))
	if err := c.Capture(context.Background(), tc); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	var p capturePayload
	if err := json.Unmarshal(fa.got[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	tr := p.ToolResults[0]
	if !tr.Reference || tr.PayloadRef == "" {
		t.Fatalf("modo sensível devia marcar Reference + PayloadRef: %+v", tr)
	}
	if len(tr.Output) != 0 {
		t.Fatalf("output em claro NÃO devia ser persistido em modo sensível: %q", tr.Output)
	}
	// O replay de um resultado sensível devolve o marcador de referência, não a PII.
	value, _, _ := tr.decode()
	if string(value.Value) != tr.PayloadRef {
		t.Fatalf("replay sensível devia devolver a referência, obtive %q", value.Value)
	}
	if string(value.Value) == "PII-SENSÍVEL-1234" {
		t.Fatalf("replay expôs PII em claro")
	}
}

// Modo sensível: a PII no TEXTO da resposta do modelo e no INPUT/ResourceValue da
// tool call NÃO é gravada em claro — só referências (sha256). A guarda cobre o
// não-determinismo inteiro do turno, não apenas os outputs de tools.
func TestCaptureSensitiveRedactsTextAndInputs(t *testing.T) {
	const (
		textPII    = "o número é 123-45-6789"
		inputPII   = "cartao 4111-1111-1111-1111"
		resnamePII = "vitima@example.com"
	)
	fa := &fakeAppender{}
	c, _ := NewCapturer(fa, WithSensitiveResults(), WithClock(func() time.Time { return time.Unix(1, 0) }))
	tc := agentruntime.TurnCapture{
		RunID:  "run1",
		StepID: "step-000001",
		Turn:   1,
		Response: agentruntime.ModelResponse{
			Text: textPII,
			ToolCalls: []agentruntime.ToolInvocation{{
				ToolID:        "send_email",
				Capability:    "cap:email.send",
				ResourceType:  "email",
				ResourceValue: resnamePII,
				Input:         []byte(inputPII),
			}},
		},
	}
	if err := c.Capture(context.Background(), tc); err != nil {
		t.Fatalf("Capture: %v", err)
	}

	// Prova sobre os BYTES persistidos: nenhuma das PII aparece em claro no payload.
	raw := fa.got[0].Payload
	for _, secret := range []string{textPII, inputPII, resnamePII} {
		if bytesContains(raw, secret) {
			t.Fatalf("PII em claro no payload sensível: %q\npayload=%s", secret, raw)
		}
	}

	var p capturePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	// O texto e o input viraram referência sha256; type/region mantêm-se (estruturais).
	if p.Response.Text[:7] != "sha256:" {
		t.Fatalf("texto do modelo não foi redigido: %q", p.Response.Text)
	}
	call := p.Response.ToolCalls[0]
	if string(call.Input[:7]) != "sha256:" {
		t.Fatalf("input da tool não foi redigido: %q", call.Input)
	}
	if call.ResourceValue[:7] != "sha256:" {
		t.Fatalf("resource_value não foi redigido: %q", call.ResourceValue)
	}
	if call.ResourceType != "email" {
		t.Fatalf("resource_type estrutural não devia ser redigido: %q", call.ResourceType)
	}
	// O modo não-sensível continua a gravar o texto/input em claro (guarda é opt-in).
	fa2 := &fakeAppender{}
	c2, _ := NewCapturer(fa2, WithClock(func() time.Time { return time.Unix(1, 0) }))
	if err := c2.Capture(context.Background(), tc); err != nil {
		t.Fatal(err)
	}
	if !bytesContains(fa2.got[0].Payload, textPII) {
		t.Fatalf("modo não-sensível devia gravar o texto em claro (dívida EPIC-13 assumida)")
	}
}

// bytesContains reporta se sub ocorre em b (sem alocar uma cópia string de b).
func bytesContains(b []byte, sub string) bool {
	return strings.Contains(string(b), sub)
}

func TestCapturePropagatesAppendError(t *testing.T) {
	sentinel := errors.New("sem quórum")
	c, _ := NewCapturer(&fakeAppender{err: sentinel})
	if err := c.Capture(context.Background(), sampleTurnCapture()); !errors.Is(err, sentinel) {
		t.Fatalf("esperava propagar %v, obtive %v", sentinel, err)
	}
}

func TestCaptureNilStore(t *testing.T) {
	if _, err := NewCapturer(nil); !errors.Is(err, ErrNilStore) {
		t.Fatalf("esperava ErrNilStore, obtive %v", err)
	}
}

// A captura preserva o erro de execução da tool (materializado no tail do replay).
func TestCapturePreservesToolError(t *testing.T) {
	fa := &fakeAppender{}
	c, _ := NewCapturer(fa)
	tc := sampleTurnCapture()
	tc.ToolResults[0].ToolError = errors.New("falha downstream")
	if err := c.Capture(context.Background(), tc); err != nil {
		t.Fatal(err)
	}
	var p capturePayload
	_ = json.Unmarshal(fa.got[0].Payload, &p)
	if p.ToolResults[0].ToolError != "falha downstream" {
		t.Fatalf("erro de tool não preservado: %q", p.ToolResults[0].ToolError)
	}
	_, toolErr, _ := p.ToolResults[0].decode()
	if toolErr == nil || toolErr.Error() != "falha downstream" {
		t.Fatalf("erro de tool não re-hidratado: %v", toolErr)
	}
}
