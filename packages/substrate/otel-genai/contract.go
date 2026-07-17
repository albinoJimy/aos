package otelgenai

import (
	"fmt"
	"sort"
)

// stringify dá uma representação textual estável de um valor de atributo para os
// caminhos de fallback (OTLP AnyValue e mensagens de erro do validador).
func stringify(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// requiredAttrs é a tabela de conformidade semconv: os atributos OBRIGATÓRIOS por
// operação. É a fonte do teste de contrato (CA de AOS-076) — um span que não os
// traga todos falha a validação.
//
//   - invoke_agent: identifica a operação.
//   - chat: modelo pedido + a NHI do principal que executa (CA1).
//   - execute_tool: nome da tool + hash(tool+args) + a marca untrusted do resultado
//     (CA2). O resultado é SEMPRE untrusted (AttrResultTaint, ADR-005); AttrTaint —
//     a taint da AUTORIZAÇÃO — é observabilidade adicional (AOS-069), não o mesmo
//     eixo, pelo que a conformidade CA2 exige [AttrResultTaint].
var requiredAttrs = map[string][]string{
	OpInvokeAgent: {AttrOperationName},
	OpChat:        {AttrOperationName, AttrRequestModel, AttrPrincipalNHI},
	OpExecuteTool: {AttrOperationName, AttrToolName, AttrToolCallHash, AttrResultTaint},
}

// RequiredAttributes devolve (uma cópia d)os atributos obrigatórios para a
// operação op, ou nil se a operação não tem contrato definido.
func RequiredAttributes(op string) []string {
	req, ok := requiredAttrs[op]
	if !ok {
		return nil
	}
	out := make([]string, len(req))
	copy(out, req)
	return out
}

// KnownOperations devolve as operações com contrato semconv definido, ordenadas.
func KnownOperations() []string {
	ops := make([]string, 0, len(requiredAttrs))
	for op := range requiredAttrs {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	return ops
}

// operationOf determina a operação de uma SpanData: o valor de gen_ai.operation.
// name se presente e string, senão o Name do span.
func operationOf(sd SpanData) string {
	if v, ok := sd.Attribute(AttrOperationName); ok {
		if s, isStr := v.(string); isStr && s != "" {
			return s
		}
	}
	return sd.Name
}

// ValidateSpanData confirma que sd traz todos os atributos obrigatórios da sua
// operação (conformidade semconv GenAI). Devolve nil se conforme, ou um erro que
// enumera os atributos em falta. Uma operação sem contrato definido é aceite
// (não há requisitos a impor).
func ValidateSpanData(sd SpanData) error {
	op := operationOf(sd)
	req, ok := requiredAttrs[op]
	if !ok {
		return nil
	}
	var missing []string
	for _, key := range req {
		if _, present := sd.Attribute(key); !present {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("span %q não conforme com a semconv GenAI: atributos em falta %v", op, missing)
	}
	return nil
}
