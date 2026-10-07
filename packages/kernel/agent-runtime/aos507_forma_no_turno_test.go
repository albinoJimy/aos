package agentruntime

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// AOS-507 — o `turn.recorded` guarda a ficha da forma da resposta do provider, declarada pelo
// cliente de modelo. Campo aditivo: um turno sem ficha grava os bytes de sempre.

func aos507Ficha() *ResponseShape {
	tokens := int64(77)
	return &ResponseShape{
		Content: ShapeContentNull, Reasoning: ShapeReasoningContent, ReasoningForm: ShapeFormString, ReasoningBytes: 31,
		ReasoningSigned: ShapeNo, Refusal: ShapeRefusalAbsent, ToolCallID: ShapeIDNone, LegacyFunctionCall: ShapeNo,
		ChoicesN: 1, FinishReasonMapped: ShapeYes, ReasoningTokens: &tokens, SystemFingerprint: ShapeNo,
		ShapeDigest: aos505DigestDoPerfil,
	}
}

func TestAOS507_Turno_SemFicha_BytesDeSempre(t *testing.T) {
	base := aos505Gravar(t, TurnRecord{Manifest: Manifest{Model: ModelManifest{ModelID: "gpt-4o", ServedModelID: "gpt-4o"}}})
	const quer = `{"turn":1,"manifest":{"schema_version":"1.0","prompt_hash":"","system_hash":"","assembly_version":"` + AssemblyVersion + `","model":{"model_id":"gpt-4o","served_model_id":"gpt-4o","seed":0}},"input_tokens":10,"output_tokens":5,"cost_micro_usd":0,"tool_calls_requested":0,"final":false}`
	if base != quer {
		t.Fatalf("os bytes de um turno sem ficha mudaram:\n veio:  %s\n quero: %s", base, quer)
	}
}

func TestAOS507_Turno_ComFicha_GravaOCampo(t *testing.T) {
	got := aos505Gravar(t, TurnRecord{Manifest: Manifest{Model: ModelManifest{ModelID: "gpt-4o"}}, ResponseShape: aos507Ficha()})
	const quer = `,"response_shape":{"content":"nulo","content_bytes":0,"reasoning":"reasoning_content","reasoning_form":"string","reasoning_bytes":31,"reasoning_signed":"nao","refusal":"ausente","tool_calls_n":0,"tool_call_id":"nenhum","tool_call_id_max_bytes":0,"legacy_function_call":"nao","choices_n":1,"finish_reason_mapped":"sim","reasoning_tokens":77,"system_fingerprint":"nao","unknown_keys_n":0,"shape_digest":"` + aos505DigestDoPerfil + `"}}`
	if !strings.HasSuffix(got, quer) {
		t.Fatalf("o turno nao gravou a ficha:\n veio:  %s\n quero o fim: %s", got, quer)
	}
	// Sem a ficha, o resto do evento é o de um turno sem ela.
	sem := aos505Gravar(t, TurnRecord{Manifest: Manifest{Model: ModelManifest{ModelID: "gpt-4o"}}})
	if strings.Replace(got, quer, "}", 1) != sem {
		t.Fatalf("fora do campo novo o evento mudou:\n com: %s\n sem: %s", got, sem)
	}
	ilegivel := aos505Gravar(t, TurnRecord{ResponseShape: &ResponseShape{Unreadable: true, Content: "texto", ChoicesN: 9}})
	if !strings.HasSuffix(ilegivel, `,"response_shape":{"ilegivel":true}}`) {
		t.Fatalf("uma ficha ilegivel grava-se so com `ilegivel`: %s", ilegivel)
	}
}

// O recorder é exportado e o cliente de modelo é a fronteira untrusted: um valor fora do
// vocabulário torna a ficha ilegível, e nenhum texto livre chega ao evento.
func TestAOS507_Turno_ForaDoVocabulario(t *testing.T) {
	campos := map[string]func(*ResponseShape){
		"content":              func(s *ResponseShape) { s.Content = "TEXTO-LIVRE" },
		"reasoning":            func(s *ResponseShape) { s.Reasoning = "TEXTO-LIVRE" },
		"reasoning_form":       func(s *ResponseShape) { s.ReasoningForm = "TEXTO-LIVRE" },
		"reasoning_signed":     func(s *ResponseShape) { s.ReasoningSigned = "TEXTO-LIVRE" },
		"refusal":              func(s *ResponseShape) { s.Refusal = "TEXTO-LIVRE" },
		"tool_call_id":         func(s *ResponseShape) { s.ToolCallID = "TEXTO-LIVRE" },
		"arguments_form":       func(s *ResponseShape) { s.ArgumentsForm = "TEXTO-LIVRE" },
		"legacy_function_call": func(s *ResponseShape) { s.LegacyFunctionCall = "TEXTO-LIVRE" },
		"finish_reason_mapped": func(s *ResponseShape) { s.FinishReasonMapped = "TEXTO-LIVRE" },
		"system_fingerprint":   func(s *ResponseShape) { s.SystemFingerprint = "TEXTO-LIVRE" },
		"campo por preencher":  func(s *ResponseShape) { s.Content = "" },
	}
	for nome, estragar := range campos {
		f := aos507Ficha()
		estragar(f)
		got := aos505Gravar(t, TurnRecord{ResponseShape: f})
		if strings.Contains(got, "TEXTO-LIVRE") || !strings.HasSuffix(got, `,"response_shape":{"ilegivel":true}}`) {
			t.Errorf("%s: o recorder deixou passar um valor fora do vocabulario: %s", nome, got)
		}
	}
	// O digest só passa com a forma certa; os inteiros ficam entre zero e o tecto.
	f := aos507Ficha()
	f.ShapeDigest, f.ContentBytes, f.ChoicesN = "TEXTO-LIVRE", -4, 1<<60
	menos := int64(-1)
	f.ReasoningTokens = &menos
	got := aos505Gravar(t, TurnRecord{ResponseShape: f})
	if strings.Contains(got, "TEXTO-LIVRE") || strings.Contains(got, "shape_digest") || !strings.Contains(got, `"content_bytes":0,`) ||
		!strings.Contains(got, `"choices_n":1099511627776,`) || !strings.Contains(got, `"reasoning_tokens":0,`) {
		t.Errorf("digest ou inteiros por sanear: %s", got)
	}
	if (*ResponseShape)(nil).Normalizado() != nil {
		t.Errorf("uma ficha nil fica nil")
	}
}

// UM BINÁRIO ANTERIOR LÊ O EVENTO: quem não conhece `response_shape` descodifica o resto como
// sempre (é a ordem de recuo — voltar a `off`, ou a uma imagem anterior). E a ficha gravada
// lê-se de volta igual.
func TestAOS507_Turno_LeituraPorBinarioAnterior(t *testing.T) {
	got := aos505Gravar(t, TurnRecord{Manifest: Manifest{Model: ModelManifest{ModelID: "gpt-4o"}}, StopReason: StopStop, Final: true, ResponseShape: aos507Ficha()})
	// O payload tal como um binário anterior ao campo o declarava.
	var antigo struct {
		Turn         int        `json:"turn"`
		Manifest     Manifest   `json:"manifest"`
		InputTokens  int64      `json:"input_tokens"`
		OutputTokens int64      `json:"output_tokens"`
		Final        bool       `json:"final"`
		StopReason   StopReason `json:"stop_reason,omitempty"`
	}
	if err := json.Unmarshal([]byte(got), &antigo); err != nil {
		t.Fatalf("um leitor anterior nao le o evento: %v", err)
	}
	if antigo.Turn != 1 || antigo.Manifest.Model.ModelID != "gpt-4o" || antigo.InputTokens != 10 || antigo.OutputTokens != 5 || !antigo.Final || antigo.StopReason != StopStop {
		t.Fatalf("o leitor anterior leu outra coisa: %+v", antigo)
	}
	var novo turnPayload
	if err := json.Unmarshal([]byte(got), &novo); err != nil {
		t.Fatalf("o evento nao se le de volta: %v", err)
	}
	if !reflect.DeepEqual(novo.ResponseShape, aos507Ficha()) {
		t.Fatalf("a ficha lida nao e a gravada:\n veio:  %+v\n quero: %+v", novo.ResponseShape, aos507Ficha())
	}
	var ilegivel ResponseShape
	if err := json.Unmarshal([]byte(`{"ilegivel":true}`), &ilegivel); err != nil || !ilegivel.Unreadable {
		t.Fatalf("uma ficha ilegivel nao se le de volta: %+v, %v", ilegivel, err)
	}
}

// A FICHA NÃO DECIDE NADA: a regra de terminação e o veredicto são os mesmos com e sem ela.
func TestAOS507_AFichaNaoDecide(t *testing.T) {
	respostas := []ModelResponse{
		{Text: "", StopReason: StopStop, Final: true},
		{Text: "resposta", StopReason: StopStop, Final: true},
		{Text: "cortad", StopReason: StopLength},
		{ToolCalls: []ToolInvocation{{ToolID: "echo"}}, StopReason: StopToolCalls},
	}
	fichas := []*ResponseShape{aos507Ficha(), {Unreadable: true}, {Content: ShapeContentText, ContentBytes: 400}}
	for i, sem := range respostas {
		for _, versao := range SupportedAssemblyVersions() {
			fimSem, errSem := TurnEndsRun(sem, versao)
			for _, f := range fichas {
				com := sem
				com.Shape = f
				if fim, err := TurnEndsRun(com, versao); fim != fimSem || (err == nil) != (errSem == nil) {
					t.Errorf("resposta %d, layout %s: a ficha mudou a regra de terminacao", i, versao)
				}
			}
		}
		for _, c := range []*Completion{nil, {Mode: CompletionEnforce}, {Mode: CompletionObserve}} {
			vSem, errSem := ConcludeRun(sem, c, nil)
			for _, f := range fichas {
				com := sem
				com.Shape = f
				if v, err := ConcludeRun(com, c, nil); !reflect.DeepEqual(v, vSem) || (err == nil) != (errSem == nil) {
					t.Errorf("resposta %d: a ficha mudou o veredicto: %+v contra %+v", i, v, vSem)
				}
			}
		}
	}
}
