package harness

import (
	"context"
	"errors"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
)

// TestScriptedModelOutOfScript cobre o ramo de erro do modelo guionado quando o
// loop pede um turno fora do guião (defensivo — não deve acontecer nas fixtures).
func TestScriptedModelOutOfScript(t *testing.T) {
	m := &scriptedModel{responses: []agentruntime.ModelResponse{{Text: "único", Final: true}}}
	if _, err := m.Call(context.Background(), agentruntime.PromptView{Turn: 2}); err == nil {
		t.Fatalf("esperava erro para turno fora do guião")
	}
	if _, err := m.Call(context.Background(), agentruntime.PromptView{Turn: 0}); err == nil {
		t.Fatalf("esperava erro para turno inválido")
	}
	// Caminho feliz: turno 1 devolve a resposta.
	if r, err := m.Call(context.Background(), agentruntime.PromptView{Turn: 1}); err != nil || r.Text != "único" {
		t.Fatalf("turno 1 devia devolver a resposta guionada: %+v err=%v", r, err)
	}
}

// TestEffectKeyError cobre a propagação do erro de derivação de chave em applyOnce
// → verifyIdempotency (a chave é inválida, p.ex. contém o delimitador ':').
func TestEffectKeyError(t *testing.T) {
	f, err := BuildEchoGolden("golden_key_err")
	if err != nil {
		t.Fatalf("BuildEchoGolden: %v", err)
	}
	defer f.Close()

	c := f.Case()
	c.Effects = []Effect{{
		StepID: "bad",
		KeyAt: func(int) (string, error) {
			// Chave inválida: o step_id contém ':' (proibido por IdempotencyKey).
			return durable.IdempotencyKey("run", "step:invalido")
		},
		Run:      func(context.Context) (durable.Result, error) { return durable.Result{Status: "ok"}, nil },
		Observed: func() int { return 0 },
	}}
	c.Faults = nil
	if _, err := Verify(context.Background(), c); err == nil {
		t.Fatalf("esperava erro operacional propagado da derivação de chave inválida")
	} else if !errors.Is(err, durable.ErrDelimiterInInput) {
		t.Fatalf("esperava ErrDelimiterInInput, obtive %v", err)
	}
}

// TestFixtureCloseNil cobre o guarda nil de Close.
func TestFixtureCloseNil(t *testing.T) {
	var f *Fixture
	f.Close() // não deve entrar em pânico
}
