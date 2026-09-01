package otelgenai

import (
	"reflect"
	"sort"
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
		OpActivity: spanWith(OpActivity, map[string]string{
			AttrToolName: "search",
			AttrRunID:    "run-1",
			AttrStepID:   "step-1",
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
	// O conjunto é afirmado por EXTENSO, e não por contagem.
	//
	// A contagem («esperava 5») dizia que algo mudou mas não O QUÊ, e passava na mesma
	// se uma operação fosse trocada por outra. Enumerar torna a revisão de uma operação
	// nova uma leitura de uma linha, e é o que se quer de um contrato: acrescentar uma
	// operação é uma decisão, não um efeito secundário.
	esperadas := []string{
		OpActivity,
		OpLogAppend,
		OpLogRead,
		OpChat,
		OpExecuteTool,
		OpEvaluation,
		OpInvokeAgent,
	}
	sort.Strings(esperadas)
	if ops := KnownOperations(); !reflect.DeepEqual(ops, esperadas) {
		t.Errorf("KnownOperations = %v, esperava %v", ops, esperadas)
	}
}

// TestAOS211_ActivityUnderContractIsNotVacuouslyAccepted é a prova de NÃO-VACUIDADE do
// EIXO 1. Antes de AOS-211, `aos.activity` não tinha entrada em requiredAttrs: um span
// com esse Name resolvia a operação por fallback, não encontrava contrato e era ACEITE
// SEM VALIDAR — o único span da árvore durável isento da semconv de AOS-076. Este teste
// afirma o oposto: um aos.activity SEM gen_ai.operation.name é agora RECUSADO, e o erro
// nomeia os atributos em falta.
//
// Porque é falha-antes/passa-depois num só commit: correr esta asserção contra o
// requiredAttrs ANTERIOR (sem a chave OpActivity) devolvia nil de ValidateSpanData e o
// `if err == nil` disparava — a prova de que o contrato antes NÃO se aplicava a este span.
func TestAOS211_ActivityUnderContractIsNotVacuouslyAccepted(t *testing.T) {
	// Um span `aos.activity` cru — só o Name, SEM gen_ai.operation.name e sem os
	// atributos de correlação. É a forma exacta que era vacuosamente aceite.
	bare := SpanData{Name: OpActivity}
	err := ValidateSpanData(bare)
	if err == nil {
		t.Fatal("aos.activity sem gen_ai.operation.name devia ser RECUSADO — antes de AOS-211 era aceite vacuosamente (sem contrato)")
	}
	// O erro tem de nomear os obrigatórios que faltam (o contrato aplica-se a sério).
	for _, want := range []string{AttrOperationName, AttrToolName, AttrRunID, AttrStepID} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("erro devia nomear o atributo em falta %q: %v", want, err)
		}
	}

	// E o span COMPLETO (como o emite startSpan de activity/dispatch.go) é conforme.
	full := spanWith(OpActivity, map[string]string{
		AttrToolName: "counter",
		AttrRunID:    "run-1",
		AttrStepID:   "step-1",
	})
	if err := ValidateSpanData(full); err != nil {
		t.Errorf("aos.activity com operation.name+tool+run_id+step_id devia validar: %v", err)
	}
}
