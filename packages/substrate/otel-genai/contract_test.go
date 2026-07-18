package otelgenai

import (
	"strings"
	"testing"
)

// spanWith constrói uma SpanData com a operação e os atributos dados (chave→valor
// string), para exercitar o validador de contrato.
func spanWith(op string, attrs map[string]string) SpanData {
	kvs := []KeyValue{{Key: AttrOperationName, Value: op}}
	for k, v := range attrs {
		kvs = append(kvs, KeyValue{Key: k, Value: v})
	}
	return SpanData{Name: op, Attributes: kvs}
}

func TestValidateSpanDataPerOperation(t *testing.T) {
	valid := map[string]SpanData{
		OpInvokeAgent: spanWith(OpInvokeAgent, nil),
		OpChat: spanWith(OpChat, map[string]string{
			AttrRequestModel: "claude-opus-4-8",
			AttrPrincipalNHI: "nhi:agent-1",
		}),
		OpExecuteTool: spanWith(OpExecuteTool, map[string]string{
			AttrToolName:     "search",
			AttrToolCallHash: "abc123",
			AttrResultTaint:  "untrusted",
		}),
		OpEvaluation: spanWith(OpEvaluation, map[string]string{
			AttrEvalVerdict: string(EvalPass),
			AttrEvalDataset: string(EvalDatasetGolden),
		}),
	}
	for op, sd := range valid {
		if err := ValidateSpanData(sd); err != nil {
			t.Errorf("%s conforme devia validar: %v", op, err)
		}
	}
}

func TestValidateSpanDataMissingAttrs(t *testing.T) {
	// chat sem a NHI do principal → não conforme, e o erro nomeia o atributo.
	chat := spanWith(OpChat, map[string]string{AttrRequestModel: "m"})
	err := ValidateSpanData(chat)
	if err == nil {
		t.Fatal("chat sem principal.nhi_id devia falhar")
	}
	if !strings.Contains(err.Error(), AttrPrincipalNHI) {
		t.Errorf("erro devia nomear %q: %v", AttrPrincipalNHI, err)
	}

	// execute_tool sem hash → não conforme.
	tool := spanWith(OpExecuteTool, map[string]string{AttrToolName: "x", AttrTaint: "untrusted"})
	if err := ValidateSpanData(tool); err == nil || !strings.Contains(err.Error(), AttrToolCallHash) {
		t.Errorf("execute_tool sem hash devia falhar nomeando %q: %v", AttrToolCallHash, err)
	}
}

func TestValidateSpanDataUsesNameWhenNoOperationAttr(t *testing.T) {
	// Sem gen_ai.operation.name mas com Name conhecido: o contrato aplica-se na
	// mesma (e falha por faltar tudo).
	sd := SpanData{Name: OpChat}
	if err := ValidateSpanData(sd); err == nil {
		t.Fatal("chat sem atributos devia falhar mesmo derivando a op do Name")
	}
}

func TestValidateSpanDataUnknownOperationPasses(t *testing.T) {
	// Uma operação sem contrato não impõe requisitos.
	if err := ValidateSpanData(SpanData{Name: "custom_op"}); err != nil {
		t.Errorf("operação sem contrato não devia falhar: %v", err)
	}
}

func TestRequiredAttributesIsolation(t *testing.T) {
	req := RequiredAttributes(OpExecuteTool)
	if len(req) == 0 {
		t.Fatal("execute_tool devia ter atributos obrigatórios")
	}
	req[0] = "MUTADO"
	// A tabela interna não pode ser mutável pelo chamador.
	if RequiredAttributes(OpExecuteTool)[0] == "MUTADO" {
		t.Fatal("RequiredAttributes devolveu a fatia interna (mutável)")
	}
	if RequiredAttributes("desconhecida") != nil {
		t.Error("operação desconhecida devia devolver nil")
	}
	if ops := KnownOperations(); len(ops) != 4 {
		t.Errorf("KnownOperations = %v, esperava 4", ops)
	}
}
