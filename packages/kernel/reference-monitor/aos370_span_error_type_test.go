package referencemonitor

import (
	"context"
	"errors"
	"strings"
	"testing"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// AOS-370: o atributo de span error.type do execute_tool passou a ser um código de
// conjunto FECHADO ([spanErrorType]) em vez do err.Error() cru da tool despachada. O
// span SAI do processo para um colector (fail-open async), e a mensagem de uma tool a
// jusante ecoa frequentemente input/credenciais — exactamente o que o hash de input em
// [Monitor.Mediate] foi posto a remover. Estes testes provam que:
//   - AC4: um erro de tool com uma credencial-canário mapeia para "tool_error" e o
//     canário NÃO aparece em NENHUM atributo de NENHUM span gravado;
//   - AC5: uma tool que tem SUCESSO não escreve error.type nenhum;
//   - erros de contexto mapeiam para os seus códigos estáveis.

// boomCall devolve um call de baseCall() redireccionado para a tool dada, preservando a
// capability/authority (os stubs de [DefaultHooks] são neutros e permitem).
func boomCall(toolID string) Call {
	c := baseCall()
	c.ToolID = toolID
	return c
}

// TestAOS370_CanaryNaoVazaParaSpan (AC4): a tool falha com um erro que contém uma
// credencial-canário; o span traz error.type == "tool_error" e o canário não aparece em
// atributo nenhum de span nenhum.
//
// NÃO-VACUIDADE: reverter monitor.go (o SetAttribute de AttrErrorType) para
// `dec.ToolErr.Error()` faz o canário aparecer no atributo error.type do span — a
// asserção de ausência do marcador avermelha. Verificado por mutação em AOS-370.
func TestAOS370_CanaryNaoVazaParaSpan(t *testing.T) {
	t.Parallel()
	const canary = "sk-CANARIO-1234"
	sink := &fakeSink{}
	tr := otelgenai.NewRecordingTracer(nil)
	m := New(WithEventSink(sink), WithTracer(tr))
	if err := m.Register("tool.boom", ToolFunc(func(_ context.Context, _ []byte) ([]byte, error) {
		return nil, errors.New("invalid token " + canary)
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}

	dec, err := m.Mediate(context.Background(), boomCall("tool.boom"))
	if err != nil {
		t.Fatalf("Mediate erro inesperado: %v", err)
	}
	if dec.Effect != EffectPermit {
		t.Fatalf("esperava EffectPermit (o erro é da tool, não da política): %q (%s)", dec.Effect, dec.Reason)
	}
	// O erro cru CONTINUA a fluir ao chamador via Decision.ToolErr (AC3): só o span é reduzido.
	if dec.ToolErr == nil || !strings.Contains(dec.ToolErr.Error(), canary) {
		t.Fatalf("Decision.ToolErr devia conter o erro cru da tool, obtive: %v", dec.ToolErr)
	}

	spans := tr.SpansByOperation(otelgenai.OpExecuteTool)
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span execute_tool, obtive %d", len(spans))
	}
	if got := spans[0].Attributes[otelgenai.AttrErrorType]; got != "tool_error" {
		t.Fatalf("error.type=%v, esperava \"tool_error\"", got)
	}

	// O canário NÃO pode aparecer em NENHUM valor de atributo de NENHUM span gravado.
	for _, s := range tr.Spans() {
		for k, v := range s.Attributes {
			if str, ok := v.(string); ok && strings.Contains(str, canary) {
				t.Fatalf("canário %q vazou para o atributo %q do span %q: %q", canary, k, s.Operation, str)
			}
		}
	}
}

// TestAOS370_SucessoNaoEscreveErrorType (AC5): uma tool que devolve (out, nil) não faz o
// span escrever error.type — a chave nem sequer existe no mapa de atributos.
func TestAOS370_SucessoNaoEscreveErrorType(t *testing.T) {
	t.Parallel()
	sink := &fakeSink{}
	tr := otelgenai.NewRecordingTracer(nil)
	m := New(WithEventSink(sink), WithTracer(tr))
	if err := m.Register("tool.ok", ToolFunc(func(_ context.Context, _ []byte) ([]byte, error) {
		return []byte("ok"), nil
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}

	dec, err := m.Mediate(context.Background(), boomCall("tool.ok"))
	if err != nil {
		t.Fatalf("Mediate erro inesperado: %v", err)
	}
	if dec.Effect != EffectPermit || dec.ToolErr != nil {
		t.Fatalf("esperava permit sem ToolErr, obtive Effect=%q ToolErr=%v", dec.Effect, dec.ToolErr)
	}

	spans := tr.SpansByOperation(otelgenai.OpExecuteTool)
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span execute_tool, obtive %d", len(spans))
	}
	if v, ok := spans[0].Attributes[otelgenai.AttrErrorType]; ok {
		t.Fatalf("caminho de sucesso NÃO devia escrever error.type, obtive %v", v)
	}
}

// TestAOS370_ErrosDeContextoMapeiam: uma tool que devolve um erro de contexto mapeia para
// o código estável correspondente (não para o catch-all "tool_error").
func TestAOS370_ErrosDeContextoMapeiam(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		toolErr error
		want    string
	}{
		{"deadline", context.DeadlineExceeded, "deadline_exceeded"},
		{"canceled", context.Canceled, "context_canceled"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &fakeSink{}
			tr := otelgenai.NewRecordingTracer(nil)
			m := New(WithEventSink(sink), WithTracer(tr))
			if err := m.Register("tool.ctx", ToolFunc(func(_ context.Context, _ []byte) ([]byte, error) {
				return nil, tc.toolErr
			})); err != nil {
				t.Fatalf("Register: %v", err)
			}
			if _, err := m.Mediate(context.Background(), boomCall("tool.ctx")); err != nil {
				t.Fatalf("Mediate erro inesperado: %v", err)
			}
			spans := tr.SpansByOperation(otelgenai.OpExecuteTool)
			if len(spans) != 1 {
				t.Fatalf("esperava 1 span execute_tool, obtive %d", len(spans))
			}
			if got := spans[0].Attributes[otelgenai.AttrErrorType]; got != tc.want {
				t.Fatalf("error.type=%v, esperava %q", got, tc.want)
			}
		})
	}
}

// TestAOS370_SpanErrorTypeUnit exercita o mapeador directamente (conjunto fechado).
func TestAOS370_SpanErrorTypeUnit(t *testing.T) {
	t.Parallel()
	if got := spanErrorType(nil); got != "" {
		t.Errorf("nil ⇒ %q, esperava vazio", got)
	}
	if got := spanErrorType(errors.New("invalid token sk-XYZ")); got != "tool_error" {
		t.Errorf("erro arbitrário ⇒ %q, esperava tool_error", got)
	}
	if got := spanErrorType(context.Canceled); got != "context_canceled" {
		t.Errorf("Canceled ⇒ %q", got)
	}
	if got := spanErrorType(context.DeadlineExceeded); got != "deadline_exceeded" {
		t.Errorf("DeadlineExceeded ⇒ %q", got)
	}
	// Erros de contexto embrulhados (errors.Is) continuam a mapear.
	if got := spanErrorType(fmtWrap(context.DeadlineExceeded)); got != "deadline_exceeded" {
		t.Errorf("DeadlineExceeded embrulhado ⇒ %q", got)
	}
}

// fmtWrap embrulha um erro para provar que spanErrorType usa errors.Is e não igualdade.
func fmtWrap(err error) error { return errWrap{err} }

type errWrap struct{ err error }

func (e errWrap) Error() string { return "wrapped: " + e.err.Error() }
func (e errWrap) Unwrap() error { return e.err }
