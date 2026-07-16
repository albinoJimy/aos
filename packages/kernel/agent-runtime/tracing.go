package agentruntime

import (
	"context"
	"sync"
)

// Atributos da semconv OTel GenAI. Os nomes são EXACTAMENTE os da convenção para
// que o adaptador OTel real (EPIC-08) seja fino: mapeia estas strings para
// attribute.Key sem renomear. NÃO puxamos go.opentelemetry.io aqui (zero-dep).
const (
	// AttrOperationName — gen_ai.operation.name ("invoke_agent"|"chat"|"execute_tool").
	AttrOperationName = "gen_ai.operation.name"
	// AttrRequestModel — gen_ai.request.model (model_id do turno).
	AttrRequestModel = "gen_ai.request.model"
	// AttrInputTokens — gen_ai.usage.input_tokens.
	AttrInputTokens = "gen_ai.usage.input_tokens"
	// AttrOutputTokens — gen_ai.usage.output_tokens.
	AttrOutputTokens = "gen_ai.usage.output_tokens"
	// AttrCostUSD — custo do span em USD (ADR-010). A semconv GenAI não fixa uma
	// chave de custo estável; usamos "gen_ai.usage.cost_usd" como atributo do AOS
	// (valor float em USD, derivado do micro-USD inteiro).
	AttrCostUSD = "gen_ai.usage.cost_usd"
	// AttrToolName — gen_ai.tool.name (span execute_tool).
	AttrToolName = "gen_ai.tool.name"
	// AttrRunID — correlação da trajectória (run_id → trace).
	AttrRunID = "aos.run_id"
	// AttrStepID — correlação do passo (step_id → span).
	AttrStepID = "aos.step_id"
	// AttrPromptHash — hash do prompt materializado do turno (âncora de replay).
	AttrPromptHash = "aos.prompt_hash"
	// AttrPrefixHash — hash do PREFIXO cache-estável (system + tool set congelado).
	// Ao contrário de AttrPromptHash (que muda a cada turno porque o tail cresce),
	// este é byte-idêntico entre turnos do mesmo run: comparar prefix_hash entre
	// turnos torna o cache-hit-rate do prefixo OBSERVÁVEL por telemetria (AOS-013
	// critério de aceitação 3). Se divergir dentro de um run, houve regressão de cache.
	AttrPrefixHash = "aos.prefix_hash"
	// AttrErrorType — error.type (semconv OTel): tipo/condição de erro do span. No
	// span execute_tool marca que a tool PERMITIDA falhou em runtime (dec.ToolErr),
	// distinguindo um output vazio legítimo de um output de tool falhada.
	AttrErrorType = "error.type"
	// AttrTaint — aos.taint: o rótulo de taint da AUTORIZAÇÃO da tool call
	// ([referencemonitor.CallContext].Taint) anotado no span execute_tool. Torna a
	// decisão de taint OBSERVÁVEL directamente do span — não só do evento de
	// mediação durável — distinguindo uma negação por taint (autorização untrusted
	// numa capability privilegiada) de uma por budget/policy (AOS-069). É o rótulo
	// de confiança, nunca o conteúdo: o Input da tool jamais é gravado no span.
	AttrTaint = "aos.taint"
	// AttrDeniedBy — aos.decision.denied_by: o nome do hook que negou/escalou
	// ([referencemonitor.Decision].DeniedBy), anotado no span execute_tool apenas
	// quando a decisão não é permit. Com AttrTaint, torna a negação por taint
	// ("taint") auto-descritível no span, sem segredos.
	AttrDeniedBy = "aos.decision.denied_by"
)

// Nomes de operação da semconv GenAI.
const (
	// OpInvokeAgent — span que envolve o run inteiro (invoke_agent).
	OpInvokeAgent = "invoke_agent"
	// OpChat — span de uma chamada ao modelo (chat).
	OpChat = "chat"
	// OpExecuteTool — span de uma tool call despachada via RM (execute_tool).
	OpExecuteTool = "execute_tool"
)

// microUSDToUSD converte micro-USD inteiro para USD (float, só para o atributo
// de span; o burn-down interno mantém-se inteiro).
func microUSDToUSD(microUSD int64) float64 { return float64(microUSD) / 1_000_000.0 }

// Span é um span aberto. SetAttribute acumula atributos; End fecha-o. É
// deliberadamente mínimo — o adaptador OTel real (EPIC-08) implementa esta porta
// sobre trace.Span.
type Span interface {
	SetAttribute(key string, value any)
	End()
}

// Tracer é a PORTA de observabilidade. StartSpan abre um span nomeado pela
// operação GenAI e devolve um contexto derivado (para propagação futura) e o
// span. O SDK OTel real é EPIC-08; aqui fornecemos [NoopTracer] (default) e
// [RecordingTracer] (testes).
type Tracer interface {
	StartSpan(ctx context.Context, operation string) (context.Context, Span)
}

// ---------------------------------------------------------------------------
// NoopTracer — default (sem observabilidade).
// ---------------------------------------------------------------------------

// NoopTracer descarta todos os spans. É o default do [Runtime].
type NoopTracer struct{}

// StartSpan implementa [Tracer].
func (NoopTracer) StartSpan(ctx context.Context, _ string) (context.Context, Span) {
	return ctx, noopSpan{}
}

type noopSpan struct{}

func (noopSpan) SetAttribute(string, any) {}
func (noopSpan) End()                     {}

// ---------------------------------------------------------------------------
// RecordingTracer — captura spans+atributos para asserção em teste.
// ---------------------------------------------------------------------------

// RecordedSpan é um span capturado pelo [RecordingTracer].
type RecordedSpan struct {
	Operation  string
	Attributes map[string]any
	Ended      bool
}

// RecordingTracer capta todos os spans abertos e os seus atributos. É seguro
// para concorrência (o loop pode, no futuro, abrir spans em goroutines).
type RecordingTracer struct {
	mu    sync.Mutex
	spans []*RecordedSpan
}

// StartSpan implementa [Tracer].
func (t *RecordingTracer) StartSpan(ctx context.Context, operation string) (context.Context, Span) {
	rs := &RecordedSpan{Operation: operation, Attributes: make(map[string]any)}
	t.mu.Lock()
	t.spans = append(t.spans, rs)
	t.mu.Unlock()
	return ctx, &recordingSpan{tracer: t, rec: rs}
}

// Spans devolve uma cópia (shallow) da lista de spans capturados.
func (t *RecordingTracer) Spans() []*RecordedSpan {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*RecordedSpan, len(t.spans))
	copy(out, t.spans)
	return out
}

// SpansByOperation devolve os spans cuja operação é a dada.
func (t *RecordingTracer) SpansByOperation(op string) []*RecordedSpan {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []*RecordedSpan
	for _, s := range t.spans {
		if s.Operation == op {
			out = append(out, s)
		}
	}
	return out
}

type recordingSpan struct {
	tracer *RecordingTracer
	rec    *RecordedSpan
}

func (s *recordingSpan) SetAttribute(key string, value any) {
	s.tracer.mu.Lock()
	s.rec.Attributes[key] = value
	s.tracer.mu.Unlock()
}

func (s *recordingSpan) End() {
	s.tracer.mu.Lock()
	s.rec.Ended = true
	s.tracer.mu.Unlock()
}
