package agentruntime

// A instrumentação OTel GenAI (AOS-076) vive agora na camada PARTILHADA
// substrate/otel-genai (módulo folha zero-dep), para que o Reference Monitor e o
// Agent Runtime — que não se podem importar mutuamente (o RT importa o RM) —
// partilhem o MESMO vocabulário, a mesma mecânica de spans/propagação e a mesma
// árvore de sink. Este ficheiro preserva a superfície pública HISTÓRICA do RT por
// ALIASES, para que o código e os testes existentes continuem válidos sem
// alterações: os tipos/constantes resolvem para os do módulo otelgenai.

import (
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// Portas de observabilidade — aliases para a camada partilhada.
type (
	// Tracer é a porta de observabilidade (ver [otelgenai.Tracer]).
	Tracer = otelgenai.Tracer
	// Span é um span aberto (ver [otelgenai.Span]).
	Span = otelgenai.Span
	// SpanContext é a identidade W3C/OTLP de um span (trace_id/span_id). Re-exportada
	// para que os implementadores da porta [Span] nomeiem o tipo de retorno de
	// SpanContext() sem importar otelgenai directamente.
	SpanContext = otelgenai.SpanContext
	// NoopTracer descarta todos os spans — o default do [Runtime].
	NoopTracer = otelgenai.NoopTracer
	// RecordingTracer capta spans+atributos+topologia para asserção em teste.
	RecordingTracer = otelgenai.RecordingTracer
	// RecordedSpan é um span capturado pelo [RecordingTracer].
	RecordedSpan = otelgenai.RecordedSpan
)

// Atributos da semconv OTel GenAI — aliases para a fonte única (otelgenai). Os
// nomes são EXACTAMENTE os da convenção; o adaptador OTel real (EPIC-08) mapeia-os
// para attribute.Key sem renomear.
const (
	// AttrOperationName — gen_ai.operation.name.
	AttrOperationName = otelgenai.AttrOperationName
	// AttrRequestModel — gen_ai.request.model.
	AttrRequestModel = otelgenai.AttrRequestModel
	// AttrInputTokens — gen_ai.usage.input_tokens.
	AttrInputTokens = otelgenai.AttrInputTokens
	// AttrOutputTokens — gen_ai.usage.output_tokens.
	AttrOutputTokens = otelgenai.AttrOutputTokens
	// AttrCostUSD — gen_ai.usage.cost_usd (contabilidade fina é AOS-078).
	AttrCostUSD = otelgenai.AttrCostUSD
	// AttrToolName — gen_ai.tool.name (span execute_tool).
	AttrToolName = otelgenai.AttrToolName
	// AttrToolCallHash — aos.tool_call.hash (hash(tool+args), posto pelo RM).
	AttrToolCallHash = otelgenai.AttrToolCallHash
	// AttrPrincipalNHI — aos.principal.nhi_id (a NHI do principal no span chat).
	AttrPrincipalNHI = otelgenai.AttrPrincipalNHI
	// AttrRunID — aos.run_id.
	AttrRunID = otelgenai.AttrRunID
	// AttrStepID — aos.step_id.
	AttrStepID = otelgenai.AttrStepID
	// AttrPromptHash — aos.prompt_hash.
	AttrPromptHash = otelgenai.AttrPromptHash
	// AttrPrefixHash — aos.prefix_hash (cache-hit-rate do prefixo observável).
	AttrPrefixHash = otelgenai.AttrPrefixHash
	// AttrErrorType — error.type.
	AttrErrorType = otelgenai.AttrErrorType
	// AttrTaint — aos.taint (rótulo de taint da autorização, posto pelo RM).
	AttrTaint = otelgenai.AttrTaint
	// AttrResultTaint — aos.tool.result_taint (marca untrusted do resultado, RM).
	AttrResultTaint = otelgenai.AttrResultTaint
	// AttrDecision — aos.decision (efeito da mediação, posto pelo RM).
	AttrDecision = otelgenai.AttrDecision
	// AttrDeniedBy — aos.decision.denied_by (hook atribuível, posto pelo RM).
	AttrDeniedBy = otelgenai.AttrDeniedBy
)

// Nomes de operação da semconv GenAI — aliases.
const (
	// OpInvokeAgent — span que envolve o run inteiro (invoke_agent).
	OpInvokeAgent = otelgenai.OpInvokeAgent
	// OpChat — span de uma chamada ao modelo (chat).
	OpChat = otelgenai.OpChat
	// OpExecuteTool — span de uma tool call mediada pelo RM (execute_tool).
	OpExecuteTool = otelgenai.OpExecuteTool
)

// microUSDToUSD converte micro-USD inteiro para USD (float, só para o atributo de
// span; o burn-down interno mantém-se inteiro). Delega na conversão canónica da
// camada partilhada.
func microUSDToUSD(microUSD int64) float64 { return otelgenai.MicroUSDToUSD(microUSD) }
