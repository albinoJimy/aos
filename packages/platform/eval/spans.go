package eval

import (
	"crypto/sha256"
	"encoding/binary"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// Atributos de span PRÓPRIOS deste harness (não poluem o vocabulário semconv do
// módulo folha otel-genai). Transportam o comportamento produzido do candidato dentro
// do [otelgenai.EvalTarget] para que o [Runner] o marque contra o golden-set. Não são
// segredos — são o case-id e o output final observável do candidato.
const (
	// attrEvalCaseID liga cada span ao caso do golden-set que o produziu (agrupamento).
	attrEvalCaseID = "aos.eval.case_id"
	// attrEvalCaseOutput carrega o output final observável do candidato para o caso.
	attrEvalCaseOutput = "aos.eval.case_output"
)

// caseBehavior emparelha um caso do golden-set com o comportamento que o candidato
// produziu para ele. É a entrada de [deriveTraceID] e de [encodeBehavior] — capturar o
// comportamento ANTES de derivar o trace é o que torna a trajectória sensível ao
// candidato (candidatos distintos ⇒ traces distintos).
type caseBehavior struct {
	id string
	b  Behavior
}

// deriveTraceID deriva um trace_id determinista (16 bytes, não-nulo) da identidade da
// avaliação (evalID = suite+version+dataset) E do comportamento produzido do candidato.
// Determinismo: sem rand/relógio — a MESMA avaliação (mesmo golden-set + mesmo
// candidato) dá sempre o mesmo trace, pelo que a ligação eval→trajectória é reprodutível.
// Sensível ao candidato: dois candidatos que produzem comportamento distinto contra o
// mesmo golden-set obtêm trajectórias DISTINTAS — o gen_ai.evaluation.result de cada
// execução liga ao trace da SUA execução, não a um trace partilhado pela suite.
func deriveTraceID(evalID string, behaviors []caseBehavior) [16]byte {
	h := sha256.New()
	h.Write([]byte("aos.eval.trace.v1:" + evalID))
	for _, cb := range behaviors {
		h.Write([]byte{0x00}) // separador de registo (evita colisões id|output|action)
		h.Write([]byte(cb.id))
		h.Write([]byte{0x1f})
		h.Write([]byte(cb.b.Output))
		for _, a := range cb.b.Actions {
			h.Write([]byte{0x1e})
			h.Write([]byte(a))
		}
		// Usage (AOS-115) folda no trace SÓ quando presente, tornando a trajectória
		// sensível ao custo/tokens do candidato (candidatos que só diferem no custo obtêm
		// traces distintos, ligando cada eval à sua execução). Guardado por hasUsage para
		// preservar BYTE-A-BYTE o trace dos candidatos AOS-114 (usage tudo-zero).
		if cb.b.hasUsage() {
			h.Write([]byte{0x1d})
			var u [24]byte
			binary.BigEndian.PutUint64(u[0:8], uint64(cb.b.InputTokens))
			binary.BigEndian.PutUint64(u[8:16], uint64(cb.b.OutputTokens))
			binary.BigEndian.PutUint64(u[16:24], uint64(cb.b.CostMicroUSD))
			h.Write(u[:])
		}
	}
	sum := h.Sum(nil)
	var id [16]byte
	copy(id[:], sum[:16])
	if id == ([16]byte{}) { // fail-safe: um trace-id all-zero seria inválido
		id[0] = 1
	}
	return id
}

// spanIDAt produz um span_id determinista (8 bytes, não-nulo) a partir de um índice.
func spanIDAt(n uint64) [8]byte {
	var id [8]byte
	binary.BigEndian.PutUint64(id[:], n+1) // +1: nunca all-zero
	return id
}

// encodeBehavior codifica o Behavior produzido para um caso como um grupo de spans
// dentro da trajectória traceID: um span-raiz [otelgenai.OpInvokeAgent] com o output e
// o case-id, seguido de um span [otelgenai.OpExecuteTool] por acção (com o nome de
// tool e o case-id). O *next é o contador partilhado de span-ids da trajectória
// (mantém os ids únicos e deterministas entre casos). Devolve os spans do caso.
func encodeBehavior(traceID [16]byte, caseID string, b Behavior, next *uint64) []otelgenai.SpanData {
	root := otelgenai.SpanData{
		Name:         otelgenai.OpInvokeAgent,
		SpanContext:  otelgenai.SpanContext{TraceID: traceID, SpanID: spanIDAt(*next)},
		ParentSpanID: [8]byte{},
		Attributes: []otelgenai.KeyValue{
			{Key: otelgenai.AttrOperationName, Value: otelgenai.OpInvokeAgent},
			{Key: attrEvalCaseID, Value: caseID},
			{Key: attrEvalCaseOutput, Value: b.Output},
		},
	}
	rootSpanID := root.SpanContext.SpanID
	*next++
	spans := make([]otelgenai.SpanData, 0, 2+len(b.Actions))
	spans = append(spans, root)
	// Span chat (AOS-115): emitido SÓ quando o comportamento carrega usage. Carrega os
	// tokens/custo do turno de modelo (a unidade-verdade que trajectoryUsage/TraceDiff
	// somam) para que a dimensão custo/tokens do trace-diffing aflore um SALTO DE CUSTO.
	// Filho da raiz do caso, marcado com o case-id. Sem usage ⇒ não é emitido (o
	// comportamento AOS-114 fica byte-a-byte inalterado — o scoring nunca lê chats).
	if b.hasUsage() {
		spans = append(spans, otelgenai.SpanData{
			Name:         otelgenai.OpChat,
			SpanContext:  otelgenai.SpanContext{TraceID: traceID, SpanID: spanIDAt(*next)},
			ParentSpanID: rootSpanID,
			Attributes: []otelgenai.KeyValue{
				{Key: otelgenai.AttrOperationName, Value: otelgenai.OpChat},
				{Key: otelgenai.AttrInputTokens, Value: b.InputTokens},
				{Key: otelgenai.AttrOutputTokens, Value: b.OutputTokens},
				{Key: otelgenai.AttrCostMicroUSD, Value: b.CostMicroUSD},
				{Key: attrEvalCaseID, Value: caseID},
			},
		})
		*next++
	}
	for _, action := range b.Actions {
		spans = append(spans, otelgenai.SpanData{
			Name:         otelgenai.OpExecuteTool,
			SpanContext:  otelgenai.SpanContext{TraceID: traceID, SpanID: spanIDAt(*next)},
			ParentSpanID: rootSpanID,
			Attributes: []otelgenai.KeyValue{
				{Key: otelgenai.AttrOperationName, Value: otelgenai.OpExecuteTool},
				{Key: otelgenai.AttrToolName, Value: action},
				{Key: attrEvalCaseID, Value: caseID},
			},
		})
		*next++
	}
	return spans
}

// decodeBehaviors reconstrói, a partir dos spans de um [otelgenai.EvalTarget], o
// Behavior produzido por caso (agrupado por [attrEvalCaseID]). A ordem das acções
// segue a ordem dos spans na lista (o encode preserva-a); é a leitura simétrica de
// [encodeBehavior] que fecha o ciclo produzir→marcar do harness sobre a porta
// [otelgenai.EvalRunner].
func decodeBehaviors(spans []otelgenai.SpanData) map[string]Behavior {
	out := make(map[string]Behavior)
	for _, sd := range spans {
		caseID := attrStr(sd, attrEvalCaseID)
		if caseID == "" {
			continue
		}
		b := out[caseID]
		switch operationOf(sd) {
		case otelgenai.OpInvokeAgent:
			b.Output = attrStr(sd, attrEvalCaseOutput)
		case otelgenai.OpExecuteTool:
			if name := attrStr(sd, otelgenai.AttrToolName); name != "" {
				b.Actions = append(b.Actions, name)
			}
		}
		out[caseID] = b
	}
	return out
}

// operationOf lê o gen_ai.operation.name de uma SpanData (vazio se ausente). Local ao
// harness (o helper homónimo de otel-genai é não-exportado).
func operationOf(sd otelgenai.SpanData) string { return attrStr(sd, otelgenai.AttrOperationName) }

// attrStr lê um atributo string de uma SpanData (vazio se ausente/de outro tipo).
func attrStr(sd otelgenai.SpanData, key string) string {
	if v, ok := sd.Attribute(key); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
