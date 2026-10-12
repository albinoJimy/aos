package main

// AOS-521 — A REGRA DO DISJUNTOR, COMO FUNÇÃO PURA (ADR-041 §2.9).
//
// Duas tabelas: a regra sobre a sequência das observações ([avaliarDisjuntor]) e o sinal que o fim de
// um run deixa ([sinalDoDesfecho]). Nenhuma toca no Event Store, no relógio ou no nó.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/pipeline"
)

// aos521Seq constrói uma sequência de desfechos: uma letra por run, cada um na sua unidade.
// `.` cumprido, `1` F1, `2` F2, `3` F3, `4` F4, `5` F5 como DESFECHO de run, `T` um TURNO com o
// modelo servido diferente.
func aos521Seq(t *testing.T, s string) []observacaoDoDisjuntor {
	t.Helper()
	sinais := map[rune]string{'.': sinalCumprido, '1': sinalNaoCumprido, '2': sinalRespostaVazia, '3': sinalRespostaRejeitada, '4': sinalErroDoProvider, '5': sinalModeloDiferente}
	var out []observacaoDoDisjuntor
	for i, c := range s {
		run := fmt.Sprintf("run-%02d", i+1)
		if c == 'T' {
			out = append(out, observacaoDoDisjuntor{turno: true, run: run})
			continue
		}
		sinal, tem := sinais[c]
		if !tem {
			t.Fatalf("letra %q desconhecida", c)
		}
		out = append(out, observacaoDoDisjuntor{run: run, unidade: run, sinal: sinal})
	}
	return out
}

func aos521Repetir(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func TestAOS521_Regra_Tabela(t *testing.T) {
	for _, c := range []struct {
		nome, seq     string
		dispara       bool
		regra         string
		falhas, janel int
	}{
		{"sequencia vazia", "", false, "", 0, 0},
		{"vinte runs bons", aos521Repetir(".", 20), false, "", 0, 20},
		{"duzentos runs bons", aos521Repetir(".", 200), false, "", 0, 20},
		{"uma falha", "1", false, "", 1, 1},
		{"duas falhas em vinte nao abrem", "1" + aos521Repetir(".", 18) + "2", false, "", 2, 20},
		{"duas falhas seguidas nao abrem", "34", false, "", 2, 2},
		{"a terceira falha abre", "1" + aos521Repetir(".", 17) + "2" + "3", true, regraFalhasNaJanela, 3, 20},
		{"tres falhas seguidas abrem logo, com a janela por encher", "123", true, regraFalhasNaJanela, 3, 3},
		{"tres falhas nos extremos de vinte abrem", "1" + aos521Repetir(".", 9) + "4" + aos521Repetir(".", 8) + "4", true, regraFalhasNaJanela, 3, 20},
		// A 1.ª falha sai da janela antes de a 3.ª entrar: posições 1, 11 e 21.
		{"tres falhas espalhadas por vinte e um runs nao abrem", "1" + aos521Repetir(".", 9) + "1" + aos521Repetir(".", 9) + "1", false, "", 2, 20},
		{"tres falhas espalhadas por sessenta runs nao abrem", aos521Repetir("1"+aos521Repetir(".", 19), 3), false, "", 1, 20},
		{"cada um dos cinco sinais e uma falha: 1,2,3", "1.2.3", true, regraFalhasNaJanela, 3, 5},
		{"cada um dos cinco sinais e uma falha: 3,4,5", "3.4.5", true, regraFalhasNaJanela, 3, 5},
		{"um turno com o modelo diferente abre sozinho, com a janela vazia", "T", true, regraModeloDiferente, 0, 0},
		{"um turno com o modelo diferente abre depois de vinte runs bons", aos521Repetir(".", 20) + "T", true, regraModeloDiferente, 0, 20},
		{"o desfecho F5 de um run e uma falha como as outras, e sozinho nao abre", "5", false, "", 1, 1},
	} {
		t.Run(c.nome, func(t *testing.T) {
			got := avaliarDisjuntor(aos521Seq(t, c.seq))
			if got.disparou != c.dispara || got.regra != c.regra || got.falhasNaJanela() != c.falhas || len(got.janela) != c.janel {
				t.Fatalf("disparou=%v regra=%q falhas=%d janela=%d; quer %v %q %d %d", got.disparou, got.regra, got.falhasNaJanela(), len(got.janela), c.dispara, c.regra, c.falhas, c.janel)
			}
			// PURA: a mesma sequência dá o mesmo estado.
			if outra := avaliarDisjuntor(aos521Seq(t, c.seq)); !reflect.DeepEqual(got, outra) {
				t.Fatalf("a mesma sequencia deu dois estados:\n%+v\n%+v", got, outra)
			}
		})
	}
}

// DISPARADA, FICA DISPARADA, e o estado é o do momento do disparo: as observações seguintes — 200
// runs bons que sejam — não o mudam, nem os contadores, nem a janela.
func TestAOS521_Regra_DisparadaFicaComOEstadoDoDisparo(t *testing.T) {
	ate := avaliarDisjuntor(aos521Seq(t, "..1.2.3"))
	depois := avaliarDisjuntor(aos521Seq(t, "..1.2.3"+aos521Repetir(".", 200)+"44T"))
	if !ate.disparou || !depois.disparou || ate.regra != depois.regra || !reflect.DeepEqual(ate.contadores, depois.contadores) || !reflect.DeepEqual(ate.janela, depois.janela) {
		t.Fatalf("o estado do disparo mudou com as observacoes seguintes:\n no disparo: %+v\n depois:     %+v", ate, depois)
	}
	// E CONTINUA A MEDIR: a janela e os contadores do FIM da sequência andam — é o que um nó em
	// observação mostra depois do «teria aberto». No disparo, os dois são os mesmos.
	if !reflect.DeepEqual(ate.janelaAgora, ate.janela) || !reflect.DeepEqual(ate.contadoresAgora, ate.contadores) {
		t.Fatalf("no momento do disparo, o «agora» e o do disparo: %+v", ate)
	}
	if len(depois.janelaAgora) != 20 || depois.falhasAgora() != 2 || depois.contadoresAgora[sinalErroDoProvider] != 2 || depois.contadoresAgora[sinalModeloDiferente] != 1 || depois.contadoresAgora[sinalNaoCumprido] != 1 {
		t.Fatalf("a medicao depois do disparo: janela=%d falhas=%d contadores=%v", len(depois.janelaAgora), depois.falhasAgora(), depois.contadoresAgora)
	}
	quer := map[string]int{sinalNaoCumprido: 1, sinalRespostaVazia: 1, sinalRespostaRejeitada: 1}
	if !reflect.DeepEqual(ate.contadores, quer) {
		t.Fatalf("contadores no disparo = %v; quer %v", ate.contadores, quer)
	}
	janela := []itemDaJanela{{"run-01", sinalCumprido}, {"run-02", sinalCumprido}, {"run-03", sinalNaoCumprido}, {"run-04", sinalCumprido}, {"run-05", sinalRespostaVazia}, {"run-06", sinalCumprido}, {"run-07", sinalRespostaRejeitada}}
	if !reflect.DeepEqual(ate.janela, janela) {
		t.Fatalf("janela no disparo = %+v", ate.janela)
	}
}

// A UNIDADE É O NÓ DO PLANO: a nova tentativa substitui a anterior. Um nó que falha à primeira e
// cumpre na nova tentativa não fica como falha na janela — e duas falhas noutras unidades, com ela,
// não abrem. Sem a nova tentativa, as mesmas três falhas abrem.
func TestAOS521_Regra_ANovaTentativaSubstituiAAnterior(t *testing.T) {
	falha := func(run, unidade string) observacaoDoDisjuntor {
		return observacaoDoDisjuntor{run: run, unidade: unidade, sinal: sinalNaoCumprido}
	}
	bom := func(run, unidade string) observacaoDoDisjuntor {
		return observacaoDoDisjuntor{run: run, unidade: unidade, sinal: sinalCumprido}
	}
	seq := []observacaoDoDisjuntor{falha("p~a", "p~a"), bom("p~a~2", "p~a"), falha("p~b", "p~b"), falha("p~c", "p~c")}
	got := avaliarDisjuntor(seq)
	if got.disparou || got.falhasNaJanela() != 2 || len(got.janela) != 3 {
		t.Fatalf("a tentativa recuperada continuou a contar: %+v", got)
	}
	// A unidade recuperada fica com o run da tentativa MAIS RECENTE.
	if got.janela[0] != (itemDaJanela{"p~a~2", sinalCumprido}) || got.janela[1] != (itemDaJanela{"p~b", sinalNaoCumprido}) || got.janela[2] != (itemDaJanela{"p~c", sinalNaoCumprido}) {
		t.Fatalf("janela = %+v", got.janela)
	}
	// O contador de F1 conta as três observações: é o que se viu, não o que a janela tem.
	if got.contadores[sinalNaoCumprido] != 3 {
		t.Fatalf("contador de nao_cumprido = %d; quer 3", got.contadores[sinalNaoCumprido])
	}
	// CONTROLO: sem a nova tentativa, as três falhas abrem.
	if sem := avaliarDisjuntor([]observacaoDoDisjuntor{falha("p~a", "p~a"), falha("p~b", "p~b"), falha("p~c", "p~c")}); !sem.disparou {
		t.Fatal("tres falhas em tres unidades tinham de abrir")
	}
	// A tentativa falhada conta ENQUANTO a seguinte não fecha: com mais duas falhas antes dela, abriu.
	if antes := avaliarDisjuntor([]observacaoDoDisjuntor{falha("p~a", "p~a"), falha("p~b", "p~b"), falha("p~c", "p~c"), bom("p~a~2", "p~a")}); !antes.disparou {
		t.Fatal("a regra disparou antes de a nova tentativa fechar, e fica disparada")
	}
	// Uma nova tentativa que TAMBÉM falha conta uma vez na janela.
	if duas := avaliarDisjuntor([]observacaoDoDisjuntor{falha("p~a", "p~a"), falha("p~a~2", "p~a"), falha("p~b", "p~b")}); duas.disparou || duas.falhasNaJanela() != 2 {
		t.Fatalf("duas tentativas falhadas do mesmo no contaram duas unidades: %+v", duas)
	}
}

// Um sinal que não é dos seis, e um desfecho sem unidade, não entram: a regra não inventa falhas.
func TestAOS521_Regra_OQueNaoEDoVocabularioNaoEntra(t *testing.T) {
	got := avaliarDisjuntor([]observacaoDoDisjuntor{
		{run: "a", unidade: "a", sinal: "limite_de_ritmo"}, {run: "b", unidade: "b", sinal: ""}, {run: "c", unidade: "", sinal: sinalNaoCumprido},
	})
	if got.disparou || len(got.janela) != 0 || len(got.contadores) != 0 {
		t.Fatalf("entrou o que nao e do vocabulario: %+v", got)
	}
	if n := len(sinaisDeFalha()); n != 7 {
		t.Fatalf("os sinais de falha sao %d; quer os cinco da D5, o que nao convergiu e a falha nao classificada", n)
	}
	if disjuntorFalhasParaAbrir != 3 || disjuntorJanela != 20 {
		t.Fatalf("a regra e %d em %d; a D5 e 3 em 20", disjuntorFalhasParaAbrir, disjuntorJanela)
	}
}

// aos521Verdicto é um veredicto do kernel com a razão dada, sobre um CONTRATO declarado (uma tool).
func aos521Verdicto(razao agentruntime.OutcomeReason) *agentruntime.Verdict {
	return &agentruntime.Verdict{Fulfilled: razao == agentruntime.OutcomeFulfilled, Reason: razao, Tools: []agentruntime.ToolEvidence{{Tool: "arquivo"}}}
}

// aos521SemContrato é o veredicto de um run que não declarou contrato de conclusão.
func aos521SemContrato(razao agentruntime.OutcomeReason) *agentruntime.Verdict {
	return &agentruntime.Verdict{Fulfilled: razao == agentruntime.OutcomeFulfilled, Reason: razao}
}

// O SINAL QUE O FIM DE UM RUN DEIXA — uma linha por entrada das duas listas do ADR-041 §2.9: o que
// conta, e a lista FECHADA do que não conta. O princípio: o que não está na lista de exclusões conta.
// (Os status do provider — 5xx, 408, 429, 401, 403 — e o 200 sem `usage` como o gateway o devolve
// estão presos no gateway, em `TestAOS521_ClassifyTurnFailure`, e no nó composto.)
func TestAOS521_SinalDoDesfecho_Tabela(t *testing.T) {
	doTurno := func(err error) error {
		return fmt.Errorf("aos: run: %w", fmt.Errorf("%w: turno 2: %w", agentruntime.ErrModelCall, err))
	}
	rota := func(check string) error {
		return doTurno(&modelgateway.RouteVarianceError{Check: check, Cause: "x"})
	}
	for _, c := range []struct {
		nome     string
		selado   state.State
		res      agentruntime.Result
		err      error
		panicked bool
		sinal    string
		conta    bool
	}{
		// ── O QUE CONTA ──────────────────────────────────────────────────────────────────────
		{"cumprido: contrato declarado e veredicto positivo", state.Complete, agentruntime.Result{Terminated: true, Verdict: aos521Verdicto(agentruntime.OutcomeFulfilled)}, nil, false, sinalCumprido, true},
		{"F1: nao cumprido, imposto (failed)", state.Failed, agentruntime.Result{Unfulfilled: true, Verdict: aos521Verdicto(agentruntime.OutcomeContractNoCall)}, nil, false, sinalNaoCumprido, true},
		{"F1: nao cumprido, so observado (complete)", state.Complete, agentruntime.Result{Terminated: true, Verdict: aos521Verdicto(agentruntime.OutcomeContractNoCall)}, nil, false, sinalNaoCumprido, true},
		{"F1: truncado, num run sem contrato", state.Failed, agentruntime.Result{Unfulfilled: true, Verdict: aos521SemContrato(agentruntime.OutcomeTruncated)}, nil, false, sinalNaoCumprido, true},
		{"F2: resposta vazia, imposta", state.Failed, agentruntime.Result{Unfulfilled: true, Verdict: aos521Verdicto(agentruntime.OutcomeEmptyOutput)}, nil, false, sinalRespostaVazia, true},
		{"F2: resposta vazia, so observada, num run sem contrato", state.Complete, agentruntime.Result{Terminated: true, Verdict: aos521SemContrato(agentruntime.OutcomeEmptyOutput)}, nil, false, sinalRespostaVazia, true},
		{"F3: resposta rejeitada pelo gateway", state.Failed, agentruntime.Result{}, doTurno(modelgateway.ErrRespostaSemChoices), false, sinalRespostaRejeitada, true},
		{"F5: rota diferente em imposicao", state.Failed, agentruntime.Result{}, rota("diferente"), false, sinalModeloDiferente, true},
		{"F6: tecto de turnos", state.TimedOut, agentruntime.Result{}, agentruntime.ErrMaxTurnsExceeded, false, sinalNaoConvergiu, true},
		{"F6: o disjuntor por-run interrompeu (timed_out)", state.TimedOut, agentruntime.Result{Tripped: true, BreakerTarget: "timed_out"}, nil, false, sinalNaoConvergiu, true},
		{"F6: o disjuntor por-run interrompeu (pausado: sem progresso, ciclo de recusas)", state.Paused, agentruntime.Result{Tripped: true, BreakerTarget: "paused"}, nil, false, sinalNaoConvergiu, true},
		{"F7: 200 sem usage", state.Failed, agentruntime.Result{}, doTurno(modelgateway.ErrUsageAusente), false, sinalFalhaNaoClassificada, true},
		{"F7: um erro do turno que o no nao conhece", state.Failed, agentruntime.Result{}, doTurno(errors.New("qualquer coisa nova")), false, sinalFalhaNaoClassificada, true},
		{"F7: um erro do loop que o no nao conhece", state.Failed, agentruntime.Result{}, errors.New("agent-runtime: ledger"), false, sinalFalhaNaoClassificada, true},
		{"F7: failed sem erro e sem veredicto", state.Failed, agentruntime.Result{}, nil, false, sinalFalhaNaoClassificada, true},
		// ── A LISTA FECHADA DO QUE NAO CONTA ────────────────────────────────────────────────
		{"panico", state.Failed, agentruntime.Result{}, nil, true, "", false},
		{"o selo nao aconteceu", "", agentruntime.Result{Terminated: true, Verdict: aos521Verdicto(agentruntime.OutcomeFulfilled)}, nil, false, "", false},
		{"veredicto negativo e o selo nao aconteceu", "", agentruntime.Result{Unfulfilled: true, Verdict: aos521Verdicto(agentruntime.OutcomeContractNoCall)}, nil, false, "", false},
		{"erro e o selo nao aconteceu", "", agentruntime.Result{}, doTurno(modelgateway.ErrUsageAusente), false, "", false},
		{"parou a espera de humano", state.WaitingOnHuman, agentruntime.Result{Escalated: true, Verdict: aos521Verdicto(agentruntime.OutcomeEmptyOutput)}, nil, false, "", false},
		{"pausado por um operador", state.Paused, agentruntime.Result{Paused: true}, nil, false, "", false},
		{"complete sem veredicto", state.Complete, agentruntime.Result{Terminated: true}, nil, false, "", false},
		{"complete com veredicto positivo e SEM contrato", state.Complete, agentruntime.Result{Terminated: true, Verdict: aos521SemContrato(agentruntime.OutcomeFulfilled)}, nil, false, "", false},
		{"orcamento esgotado", state.TimedOut, agentruntime.Result{BudgetExhausted: true}, nil, false, "", false},
		{"recusado pela fixacao do modelo", state.Failed, agentruntime.Result{}, fmt.Errorf("run: %w", recusaDaFixacao(causaLivroIlegivel, nil)), false, "", false},
		{"modelo fixado nao servido", state.Failed, agentruntime.Result{}, doTurno(fmt.Errorf("%w: causa=x", ErrModelNotServed)), false, "", false},
		{"cancelado", state.Failed, agentruntime.Result{}, doTurno(context.Canceled), false, "", false},
		{"prazo do contexto esgotado", state.Failed, agentruntime.Result{}, doTurno(context.DeadlineExceeded), false, "", false},
		{"transporte sem resposta: ligacao recusada", state.Failed, agentruntime.Result{}, doTurno(&url.Error{Op: "Post", URL: "http://proxy", Err: errors.New("connection refused")}), false, "", false},
		{"transporte sem resposta: ligacao cortada", state.Failed, agentruntime.Result{}, doTurno(&url.Error{Op: "Post", URL: "http://proxy", Err: io.ErrUnexpectedEOF}), false, "", false},
		{"negado por um estagio da pipeline do gateway", state.Failed, agentruntime.Result{}, doTurno(&pipeline.StageError{Stage: "authn", Err: errors.New("token expirado")}), false, "", false},
		{"rota nao reportada em imposicao", state.Failed, agentruntime.Result{}, rota("nao_reportado"), false, "", false},
		{"run fixado num perfil que o no nao tem", state.Failed, agentruntime.Result{}, doTurno(&modelgateway.PinnedRouteProfileError{Cause: modelgateway.PinnedCauseModelNotServed}), false, "", false},
		{"a rota exige estado que nao ha", state.Failed, agentruntime.Result{}, doTurno(&modelgateway.StateReturnError{Cause: "x"}), false, "", false},
	} {
		t.Run(c.nome, func(t *testing.T) {
			sinal, conta := sinalDoDesfecho(c.selado, c.res, c.err, c.panicked)
			if sinal != c.sinal || conta != c.conta {
				t.Fatalf("sinal=%q conta=%v; quer %q %v", sinal, conta, c.sinal, c.conta)
			}
			if conta && !sinalDeRunConhecido(sinal) {
				t.Fatalf("o sinal %q nao e do vocabulario do livro", sinal)
			}
		})
	}
}
