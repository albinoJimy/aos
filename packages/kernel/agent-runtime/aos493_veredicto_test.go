package agentruntime

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// AOS-493 — o veredicto do kernel sobre a conclusão de um run ([ConcludeRun]) como função pura:
// vocabulário fechado, contagem das chamadas efectivas, ordem das razões e o que cada modo faz
// com um veredicto negativo. O comportamento do loop e do motor de replay com ela é do teste
// diferencial (`replay/aos493_diferencial_veredicto_test.go`).

func TestAOS493_Vocabularios_Fechados(t *testing.T) {
	t.Parallel()
	quer := []OutcomeReason{"contract_unmet_no_call", "contract_unmet_after_denial", "contract_unmet_after_tool_error", "truncated", "empty_output"}
	if got := OutcomeReasons(); !reflect.DeepEqual(got, quer) {
		t.Fatalf("OutcomeReasons() = %q, quero %q", got, quer)
	}
	for _, m := range []string{"off", "observe", "enforce"} {
		if got, err := ParseCompletionMode(m); err != nil || string(got) != m {
			t.Fatalf("ParseCompletionMode(%q) = %q, %v", m, got, err)
		}
	}
	for _, m := range []string{"", "Observe", "ENFORCE", " enforce", "on", "true", "impor"} {
		if _, err := ParseCompletionMode(m); !errors.Is(err, ErrUnknownCompletionMode) {
			t.Fatalf("ParseCompletionMode(%q) tinha de recusar; veio %v", m, err)
		}
	}
}

func aos493Resultado(tool string, negada bool, falhou bool) CapturedToolResult {
	r := CapturedToolResult{Invocation: ToolInvocation{ToolID: tool}, Result: Untrusted([]byte("x"))}
	if negada {
		r.Denial = &ToolDenial{Effect: "deny", Code: "E_X", DeniedBy: "hook"}
	}
	if falhou {
		r.ToolError = errors.New("falhou a jusante")
	}
	return r
}

// SÓ É EFECTIVA A CHAMADA SEM RECUSA E SEM ERRO DE TOOL. Uma escalada é uma recusa: nenhum efeito
// ocorreu.
func TestAOS493_RunEvidence_ContaSoAsEfectivas(t *testing.T) {
	t.Parallel()
	e := NewRunEvidence()
	escalada := aos493Resultado("ler", false, false)
	escalada.Denial = &ToolDenial{Effect: "escalate"}
	e.Observe([]CapturedToolResult{
		aos493Resultado("ler", false, false),
		aos493Resultado("ler", true, false),
		aos493Resultado("ler", false, true),
		escalada,
		aos493Resultado("outra", false, false),
	})
	if got, quer := e.linha("ler"), (ToolEvidence{Tool: "ler", Requested: 4, Effective: 1, Denied: 2, Failed: 1, Last: ToolOutcomeDenied}); got != quer {
		t.Fatalf("evidencia de ler = %+v, quero %+v", got, quer)
	}
	if got := e.linha("nunca"); got != (ToolEvidence{Tool: "nunca"}) {
		t.Fatalf("uma tool nunca pedida tem a linha a zero; veio %+v", got)
	}
	if e.ToolCallsRequested() != 5 {
		t.Fatalf("total pedido = %d, quero 5", e.ToolCallsRequested())
	}
}

func TestAOS493_ConcludeRun_Tabela(t *testing.T) {
	t.Parallel()
	casos := []struct {
		nome      string
		resp      ModelResponse
		requires  []string
		observado []CapturedToolResult
		razao     OutcomeReason
	}{
		{"texto, sem contrato", ModelResponse{Text: "feito", StopReason: StopStop}, nil, nil, OutcomeFulfilled},
		{"texto, motivo nao declarado", ModelResponse{Text: "feito"}, nil, nil, OutcomeFulfilled},
		{"cortado", ModelResponse{Text: "feit", StopReason: StopLength}, nil, nil, OutcomeTruncated},
		{"cortado ganha ao vazio", ModelResponse{StopReason: StopLength}, nil, nil, OutcomeTruncated},
		{"vazio", ModelResponse{StopReason: StopStop}, nil, nil, OutcomeEmptyOutput},
		{"so espacos e vazio", ModelResponse{Text: " \n\t", StopReason: StopStop}, nil, nil, OutcomeEmptyOutput},
		{"filtro de conteudo nao e veredicto negativo", ModelResponse{Text: "x", StopReason: StopContentFilter}, nil, nil, OutcomeFulfilled},
		{"nunca pedida", ModelResponse{Text: "x"}, []string{"ler"}, nil, OutcomeContractNoCall},
		{"pedida outra tool", ModelResponse{Text: "x"}, []string{"ler"}, []CapturedToolResult{aos493Resultado("outra", false, false)}, OutcomeContractNoCall},
		{"so negada", ModelResponse{Text: "x"}, []string{"ler"}, []CapturedToolResult{aos493Resultado("ler", true, false)}, OutcomeContractAfterDenial},
		{"so falhada", ModelResponse{Text: "x"}, []string{"ler"}, []CapturedToolResult{aos493Resultado("ler", false, true)}, OutcomeContractAfterToolError},
		{"falhada e depois negada", ModelResponse{Text: "x"}, []string{"ler"}, []CapturedToolResult{aos493Resultado("ler", false, true), aos493Resultado("ler", true, false)}, OutcomeContractAfterDenial},
		{"efectiva e depois negada cumpre", ModelResponse{Text: "x"}, []string{"ler"}, []CapturedToolResult{aos493Resultado("ler", false, false), aos493Resultado("ler", true, false)}, OutcomeFulfilled},
		{"contrato ganha ao vazio", ModelResponse{}, []string{"ler"}, nil, OutcomeContractNoCall},
		{"contrato cumprido e saida vazia", ModelResponse{}, []string{"ler"}, []CapturedToolResult{aos493Resultado("ler", false, false)}, OutcomeEmptyOutput},
		{"a primeira tool em falta da a razao", ModelResponse{Text: "x"}, []string{"a", "b"}, []CapturedToolResult{aos493Resultado("b", true, false)}, OutcomeContractNoCall},
	}
	for _, c := range casos {
		for _, modo := range []CompletionMode{CompletionObserve, CompletionEnforce} {
			t.Run(c.nome+"/"+string(modo), func(t *testing.T) {
				e := NewRunEvidence()
				e.Observe(c.observado)
				fim, err := ConcludeRun(c.resp, &Completion{Mode: modo, Requires: c.requires}, e)
				if err != nil {
					t.Fatalf("ConcludeRun: %v", err)
				}
				if fim.Verdict == nil || fim.Verdict.Reason != c.razao || fim.Verdict.Fulfilled != (c.razao == OutcomeFulfilled) || fim.Verdict.Mode != modo {
					t.Fatalf("veredicto = %+v, quero razao %q", fim.Verdict, c.razao)
				}
				if len(fim.Verdict.Tools) != len(c.requires) {
					t.Fatalf("o vector tem %d linha(s) e o contrato %d tool(s)", len(fim.Verdict.Tools), len(c.requires))
				}
				imposto := c.razao != OutcomeFulfilled && modo == CompletionEnforce
				if fim.Unfulfilled != imposto || fim.Terminated == imposto {
					t.Fatalf("Unfulfilled=%v Terminated=%v; queria Unfulfilled=%v", fim.Unfulfilled, fim.Terminated, imposto)
				}
				if imposto && fim.FinalText != "" {
					t.Fatalf("um run nao cumprido nao tem texto final; veio %q", fim.FinalText)
				}
				if !imposto && fim.FinalText != c.resp.Text {
					t.Fatalf("texto final %q, queria %q", fim.FinalText, c.resp.Text)
				}
			})
		}
	}
}

// SEM VEREDICTO: manifesto sem o campo, modo vazio e modo desligado dão o desfecho de sempre e
// nenhum veredicto, qualquer que seja a resposta.
func TestAOS493_ConcludeRun_Desligado(t *testing.T) {
	t.Parallel()
	resp := ModelResponse{StopReason: StopLength}
	for _, c := range []*Completion{nil, {}, {Mode: CompletionOff, Requires: []string{"ler"}}} {
		fim, err := ConcludeRun(resp, c, NewRunEvidence())
		if err != nil || !fim.Terminated || fim.Unfulfilled || fim.Verdict != nil {
			t.Fatalf("%+v: fim=%+v err=%v", c, fim, err)
		}
	}
	if _, err := ConcludeRun(resp, &Completion{Mode: "impor"}, nil); !errors.Is(err, ErrUnknownCompletionMode) {
		t.Fatalf("um modo desconhecido tinha de dar erro; veio %v", err)
	}
}

// O contrato sai sem nomes vazios nem repetidos, na ordem dada — no veredicto e no manifesto.
func TestAOS493_Contrato_Normalizado(t *testing.T) {
	t.Parallel()
	c, err := completionDoGoal(Goal{CompletionMode: CompletionEnforce, CompletionRequires: []string{"b", "", "a", "b"}})
	if err != nil || !reflect.DeepEqual(c, &Completion{Mode: CompletionEnforce, Requires: []string{"b", "a"}}) {
		t.Fatalf("completionDoGoal = %+v, %v", c, err)
	}
	if c, err := completionDoGoal(Goal{CompletionRequires: []string{"a"}}); err != nil || c != nil {
		t.Fatalf("sem modo nao ha veredicto a gravar: %+v, %v", c, err)
	}
	// A forma em JSON é a que o `turn.recorded` e a transição terminal gravam.
	raw, _ := json.Marshal(Verdict{Mode: CompletionEnforce, Reason: OutcomeContractNoCall, Tools: []ToolEvidence{{Tool: "ler"}}})
	const quer = `{"mode":"enforce","fulfilled":false,"reason":"contract_unmet_no_call","tools":[{"tool":"ler","requested":0,"effective":0,"denied":0,"failed":0}],"tool_calls_requested":0}`
	if string(raw) != quer {
		t.Fatalf("forma do veredicto:\n  %s\nquero\n  %s", raw, quer)
	}
}

// SOBRE O QUE O RUN ACABOU (revisão I6): o desfecho do ÚLTIMO TURNO que despachou tool calls, de
// qualquer tool. O pior do turno, e não a última chamada; um turno sem tool calls não o muda.
func TestAOS493_RunEvidence_SobreOQueORunAcabou(t *testing.T) {
	t.Parallel()
	if got, quer := LastToolOutcomes(), []string{"none", "effective", "denied", "tool_error"}; !reflect.DeepEqual(got, quer) {
		t.Fatalf("LastToolOutcomes() = %q, quero %q", got, quer)
	}
	var nula *RunEvidence
	if nula.LastToolOutcome() != ToolOutcomeNone {
		t.Fatal("contadores nil dizem none")
	}
	ok, negada, falhada := aos493Resultado("a", false, false), aos493Resultado("b", true, false), aos493Resultado("c", false, true)
	for _, c := range []struct {
		nome   string
		turnos [][]CapturedToolResult
		quer   string
	}{
		{"nenhuma tool call", nil, ToolOutcomeNone},
		{"so turnos sem tool calls", [][]CapturedToolResult{nil, {}}, ToolOutcomeNone},
		{"efectiva", [][]CapturedToolResult{{ok}}, ToolOutcomeEffective},
		{"negada", [][]CapturedToolResult{{negada}}, ToolOutcomeDenied},
		{"falhada", [][]CapturedToolResult{{falhada}}, ToolOutcomeFailed},
		{"o pior do turno: negada antes de efectiva", [][]CapturedToolResult{{negada, ok}}, ToolOutcomeDenied},
		{"o pior do turno: falhada antes de efectiva", [][]CapturedToolResult{{falhada, ok}}, ToolOutcomeFailed},
		{"a recusa ganha a falha", [][]CapturedToolResult{{falhada, negada, ok}}, ToolOutcomeDenied},
		{"so o ultimo turno conta", [][]CapturedToolResult{{negada}, {ok}}, ToolOutcomeEffective},
		{"recusa depois de efectiva", [][]CapturedToolResult{{ok}, {negada}}, ToolOutcomeDenied},
		{"um turno sem tool calls nao apaga o anterior", [][]CapturedToolResult{{falhada}, nil}, ToolOutcomeFailed},
	} {
		e := NewRunEvidence()
		for _, turno := range c.turnos {
			e.Observe(turno)
		}
		if got := e.LastToolOutcome(); got != c.quer {
			t.Fatalf("%s: acabou sobre %q, quero %q", c.nome, got, c.quer)
		}
	}
}

func TestAOS493_OutcomeReason_NoVocabulario(t *testing.T) {
	t.Parallel()
	for _, r := range append([]OutcomeReason{OutcomeFulfilled}, OutcomeReasons()...) {
		if !r.NoVocabulario() {
			t.Fatalf("%q e do vocabulario", r)
		}
	}
	for _, r := range []OutcomeReason{"objective_unfulfilled", "Truncated", " truncated", "run_failed", "x\ny"} {
		if r.NoVocabulario() {
			t.Fatalf("%q NAO e do vocabulario", r)
		}
	}
}
