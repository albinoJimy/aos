package trajectorysurface

import (
	"sort"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
	"github.com/aos-ref/substrate/redaction"
)

// AttrView é a apresentação SEGURA de um atributo de span no drill-down: a chave, o
// valor JÁ REDIGIDO (sem PII em claro — AC4) e a marca [AttrView.Untrusted] que o
// classifica como DADO (conteúdo não-confiável, nunca instrução). Nunca carrega o
// valor original em claro.
type AttrView struct {
	// Key é a chave semconv do atributo (gen_ai.*/aos.*/error.type).
	Key string
	// Value é o valor apresentado, redigido pela [redaction.Engine] (garantido sem
	// PII em claro: [redaction.Engine.ScanText] sobre este valor devolve vazio).
	Value string
	// Untrusted marca o valor como DADO não-confiável (proveniência untrusted, ex.
	// resultado de tool com aos.tool.result_taint=untrusted). A superfície apresenta-o
	// como conteúdo a inspeccionar, NUNCA como instrução (separação control/data-plane).
	Untrusted bool
}

// SpanDetail é a inspecção de UM span (AC2): a sua identidade/kind, os eixos de
// custo/tokens do turno, o nome da tool e a marca de taint do resultado, e a lista
// de atributos apresentados (redigidos, com o taint marcado). Para um invoke_agent,
// os campos Subtree* trazem o custo/tokens de TODA a sua sub-árvore, COMPOSTOS por
// [otelgenai.RollupByTrace] (sem reimplementar a contabilidade nem duplicar).
type SpanDetail struct {
	// Name é o Name do span (a operação, ex. "invoke_agent").
	Name string
	// Kind é a operação GenAI (ver [KindInvokeAgent] etc.).
	Kind string
	// SpanIDHex/TraceIDHex identificam o span e a trajectória (hex OTLP).
	SpanIDHex  string
	TraceIDHex string
	// ToolName é o gen_ai.tool.name (só em execute_tool; "" nos outros).
	ToolName string
	// InputTokens/OutputTokens são os tokens do turno (gen_ai.usage.*), lidos SÓ do
	// span chat — a unidade-verdade; 0 nos invoke_agent/execute_tool.
	InputTokens  int64
	OutputTokens int64
	// CostMicroUSD é o custo do span em micro-USD (fonte de verdade); CostUSD deriva-o.
	CostMicroUSD int64
	CostUSD      float64
	// ResultTaint é a marca do resultado (aos.tool.result_taint); "untrusted" em
	// qualquer tool call bem-formada (ADR-005). "" fora de execute_tool.
	ResultTaint string
	// SubtreeInputTokens/SubtreeOutputTokens/SubtreeCostMicroUSD/SubtreeCostUSD são o
	// rollup de custo/tokens de TODA a sub-árvore de um invoke_agent (chats
	// descendentes), COMPOSTO por [otelgenai.RollupByTrace]. Zero fora de invoke_agent.
	SubtreeInputTokens  int64
	SubtreeOutputTokens int64
	SubtreeCostMicroUSD int64
	SubtreeCostUSD      float64
	// Attributes são os atributos do span apresentados de forma segura (redigidos,
	// taint marcado), ordenados por chave para uma apresentação determinista.
	Attributes []AttrView
}

// Redaction empacota a política de apresentação segura: o motor de redação, o
// titular (subject) sob o qual se redige e a [redaction.Policy] a aplicar. É
// injectada no drill-down para que CADA valor de atributo seja redigido ANTES de
// entrar numa [AttrView] (AC4).
type Redaction struct {
	// Engine é o motor de redação/scan (módulo folha redaction).
	Engine *redaction.Engine
	// Subject é o titular sob o qual se redige (rótulo de tokenização; irrelevante
	// para a política de minimização pura RemoveAll, mas explícito).
	Subject string
	// Policy é a política classe->acção aplicada (default seguro: RemoveAll).
	Policy redaction.Policy
}

// present devolve o valor de um atributo REDIGIDO e um flag de segurança. Aplica
// [redaction.Engine.RedactText] e, como GATE fail-closed, confirma com
// [redaction.Engine.ScanText] que o resultado não deixou PII em claro; se (por uma
// assimetria de detector) ainda houver achado, substitui por um marcador duro. O
// valor apresentado satisfaz assim SEMPRE ScanText==[] (AC4).
func (rd *Redaction) present(raw string) string {
	if rd == nil || rd.Engine == nil {
		return "[REDACTED]"
	}
	out, _, err := rd.Engine.RedactText(raw, rd.Subject, rd.Policy)
	if err != nil {
		// Fail-closed: qualquer erro de redação => nada em claro.
		return "[REDACTED]"
	}
	if len(rd.Engine.ScanText(out)) != 0 {
		return "[REDACTED]"
	}
	return out
}

// Inspect produz o [SpanDetail] de um nó (AC2): lê os tokens (gen_ai.usage.*), o
// custo (aos.cost.micro_usd -> USD via [otelgenai.MicroUSDToUSD]), o gen_ai.tool.name
// e o aos.tool.result_taint, e apresenta cada atributo REDIGIDO com o taint marcado
// (AC4). Para um invoke_agent, compõe o custo da sub-árvore com
// [otelgenai.RollupByTrace] sobre a sua sub-árvore achatada (sem reimplementar).
//
// LEITURA PURA (AC3): lê os atributos do span; nunca os muta nem re-emite o span.
func Inspect(node *SpanNode, rd *Redaction) SpanDetail {
	if node == nil {
		return SpanDetail{}
	}
	sd := node.Span
	d := SpanDetail{
		Name:       sd.Name,
		Kind:       node.Kind,
		SpanIDHex:  sd.SpanContext.SpanIDHex(),
		TraceIDHex: sd.SpanContext.TraceIDHex(),
	}

	// Eixos tipados (tokens/custo/tool/taint) lidos directamente dos atributos.
	if v, ok := sd.Attribute(otelgenai.AttrInputTokens); ok {
		d.InputTokens = asInt64(v)
	}
	if v, ok := sd.Attribute(otelgenai.AttrOutputTokens); ok {
		d.OutputTokens = asInt64(v)
	}
	d.CostMicroUSD = costMicroUSDOf(sd)
	d.CostUSD = otelgenai.MicroUSDToUSD(d.CostMicroUSD)
	d.ToolName = stringAttr(sd, otelgenai.AttrToolName)
	d.ResultTaint = stringAttr(sd, otelgenai.AttrResultTaint)

	// Drill-down de custo por SUB-ÁRVORE de um invoke_agent: COMPÕE RollupByTrace
	// sobre a sub-árvore achatada — não recalcula a contabilidade.
	if node.Kind == KindInvokeAgent {
		sub := subtreeUsage(node)
		d.SubtreeInputTokens = sub.InputTokens
		d.SubtreeOutputTokens = sub.OutputTokens
		d.SubtreeCostMicroUSD = sub.CostMicroUSD
		d.SubtreeCostUSD = sub.CostUSD()
	}

	// Atributos apresentados: redigidos e com o taint marcado. Um resultado untrusted
	// (aos.tool.result_taint=untrusted) marca como DADO as chaves de CONTEÚDO (não as
	// de metadados de control-plane, que são rótulos que o RT/RM selaram).
	resultUntrusted := d.ResultTaint == taintUntrusted
	views := make([]AttrView, 0, len(sd.Attributes))
	for _, kv := range sd.Attributes {
		views = append(views, AttrView{
			Key:       kv.Key,
			Value:     rd.present(stringify(kv.Value)),
			Untrusted: resultUntrusted && !isControlPlaneKey(kv.Key),
		})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Key < views[j].Key })
	d.Attributes = views
	return d
}

// subtreeUsage COMPÕE [otelgenai.RollupByTrace] sobre a sub-árvore achatada de um
// invoke_agent e devolve o total da sua sub-árvore (chats descendentes, sem
// dupla-contagem). Reutiliza a contabilidade de otel-genai — não a reimplementa.
func subtreeUsage(node *SpanNode) otelgenai.UsageTotals {
	spans := Flatten(node)
	rollups := otelgenai.RollupByTrace(spans)
	traceHex := node.Span.SpanContext.TraceIDHex()
	spanHex := node.Span.SpanContext.SpanIDHex()
	if tr, ok := rollups[traceHex]; ok {
		return tr.SubtreeByAgent[spanHex]
	}
	return otelgenai.UsageTotals{}
}

// taintUntrusted é a marca do resultado untrusted (aos.tool.result_taint). Espelha o
// valor que o Reference Monitor sela (ADR-005) — o resultado de qualquer tool volta
// ao loop como conteúdo não-confiável.
const taintUntrusted = "untrusted"
