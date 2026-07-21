package trajectorysurface

import (
	"fmt"
	"math"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// stringify dá a representação textual de um valor de atributo para apresentação
// (antes da redação). Espelha a serialização de fallback de otel-genai: uma string
// passa intacta; qualquer outro tipo usa %v.
func stringify(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// stringAttr lê um atributo string de um span (vazio se ausente ou de outro tipo).
func stringAttr(sd otelgenai.SpanData, key string) string {
	if v, ok := sd.Attribute(key); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// asInt64 lê um valor de atributo numérico inteiro (tokens são emitidos como int64;
// aceitam-se os outros inteiros por robustez de leitura). Um uint64 acima de
// MaxInt64 é leitura inválida => 0 (nunca um total negativo por overflow).
func asInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case uint64:
		if x > math.MaxInt64 {
			return 0
		}
		return int64(x)
	default:
		return 0
	}
}

// asFloat64 lê um valor de atributo em vírgula flutuante (fallback de custo USD).
func asFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	default:
		return 0, false
	}
}

// costMicroUSDOf lê o custo do span em micro-USD inteiro, espelhando a leitura de
// otel-genai: prefere o inteiro exacto aos.cost.micro_usd; na sua ausência converte o
// USD float gen_ai.usage.cost_usd com round-half. Ausente ambos => 0.
func costMicroUSDOf(sd otelgenai.SpanData) int64 {
	if v, ok := sd.Attribute(otelgenai.AttrCostMicroUSD); ok {
		return asInt64(v)
	}
	if v, ok := sd.Attribute(otelgenai.AttrCostUSD); ok {
		if f, ok := asFloat64(v); ok {
			return int64(math.Round(f * 1_000_000))
		}
	}
	return 0
}

// controlPlaneKeys é o conjunto de chaves de CONTROL-PLANE: rótulos/metadados que o
// Runtime e o Reference Monitor selam (identidade, decisão, custo/tokens, taint,
// nomes de operação/tool, âncoras de replay/eval). São de proveniência CONFIÁVEL —
// não são conteúdo derivado de modelo/tool. Um atributo FORA deste conjunto, num span
// cujo resultado é untrusted, é tratado como DADO (marcado [AttrView.Untrusted]).
//
// Nota: mesmo os valores destas chaves são REDIGIDOS na apresentação (AC4); o
// conjunto decide apenas a MARCA de taint (control vs data-plane), não se se redige.
var controlPlaneKeys = map[string]bool{
	otelgenai.AttrOperationName:     true,
	otelgenai.AttrRequestModel:      true,
	otelgenai.AttrInputTokens:       true,
	otelgenai.AttrOutputTokens:      true,
	otelgenai.AttrCostUSD:           true,
	otelgenai.AttrCostMicroUSD:      true,
	otelgenai.AttrToolName:          true,
	otelgenai.AttrToolCallHash:      true,
	otelgenai.AttrPrincipalNHI:      true,
	otelgenai.AttrRunID:             true,
	otelgenai.AttrStepID:            true,
	otelgenai.AttrPromptHash:        true,
	otelgenai.AttrPrefixHash:        true,
	otelgenai.AttrErrorType:         true,
	otelgenai.AttrTaint:             true,
	otelgenai.AttrResultTaint:       true,
	otelgenai.AttrDecision:          true,
	otelgenai.AttrDeniedBy:          true,
	otelgenai.AttrCacheHitRate:      true,
	otelgenai.AttrTenantID:          true,
	otelgenai.AttrEvalVerdict:       true,
	otelgenai.AttrEvalScore:         true,
	otelgenai.AttrEvalDataset:       true,
	otelgenai.AttrEvalSuite:         true,
	otelgenai.AttrEvalID:            true,
	otelgenai.AttrEvalTargetTraceID: true,
}

// isControlPlaneKey reporta se key é um rótulo de control-plane (metadados selados
// pelo RT/RM), por oposição a conteúdo de data-plane derivado de modelo/tool.
func isControlPlaneKey(key string) bool { return controlPlaneKeys[key] }
