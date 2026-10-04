package main

// O VEREDICTO DE CONCLUSÃO NO NÓ (AOS-493, ADR-037) — `AOS_COMPLETION_VERDICT`.
//
// O kernel calcula o desfecho de um run a partir do que foi de facto executado, contra o
// contrato de conclusão que o run declara. O nó escolhe, no arranque, o que faz com esse
// veredicto nos runs NOVOS:
//
//   - `observe` (por omissão) — calcula-o, grava-o na transição terminal, conta-o no
//     `/metrics` e di-lo no log; o desfecho do run é o de sempre;
//   - `enforce` — um veredicto negativo fecha o run em `failed`, com razão própria e sem
//     texto final;
//   - `off` — não calcula nada; os eventos ficam com os bytes de antes.
//
// O modo é fixado POR RUN, como o layout do prompt: fica no registo de retoma e no manifesto
// de cada turno, e um run retomado continua no modo em que começou, qualquer que seja o valor
// corrente desta variável.

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
)

// defaultCompletionVerdict é o modo de um nó que não define AOS_COMPLETION_VERDICT.
const defaultCompletionVerdict = agentruntime.CompletionObserve

// ErrBadCompletionVerdict — AOS_COMPLETION_VERDICT está definida com um valor fora do
// vocabulário fechado. Fail-closed: o nó não arranca. Cair para um dos modos em silêncio
// deixaria o operador convencido de que o veredicto é imposto com ele só a ser observado.
var ErrBadCompletionVerdict = errors.New("aos: AOS_COMPLETION_VERDICT invalida — valores aceites: observe (calcula, regista e conta o veredicto sem mudar o desfecho; por omissao), enforce (um veredicto negativo fecha o run em failed) ou off (nao calcula)")

// parseCompletionVerdictFromEnv lê AOS_COMPLETION_VERDICT. Vazia ⇒
// [defaultCompletionVerdict]. Um valor fora do vocabulário ⇒ [ErrBadCompletionVerdict]. Sem
// normalização de caixa, pela regra das outras variáveis de vocabulário fechado do nó.
func parseCompletionVerdictFromEnv() (agentruntime.CompletionMode, error) {
	raw := strings.TrimSpace(os.Getenv("AOS_COMPLETION_VERDICT"))
	if raw == "" {
		return defaultCompletionVerdict, nil
	}
	mode, err := agentruntime.ParseCompletionMode(raw)
	if err != nil {
		return "", fmt.Errorf("%w (veio %q)", ErrBadCompletionVerdict, raw)
	}
	return mode, nil
}

// completionVerdictBanner declara o modo com que o nó trata o veredicto dos runs novos.
func completionVerdictBanner(mode agentruntime.CompletionMode) string {
	switch mode {
	case agentruntime.CompletionEnforce:
		return "veredicto de conclusao (EPIC-02/AOS-493): IMPOSTO — um run cujo ultimo turno foi cortado pelo limite de tokens, nao trouxe texto, ou nao cumpriu o contrato de conclusao termina failed (razao objective_unfulfilled, com outcome_reason e o vector na transicao terminal) e sem texto final. Vale para os runs novos: um run retomado continua no modo em que comecou"
	case agentruntime.CompletionOff:
		return "veredicto de conclusao (EPIC-02/AOS-493): DESLIGADO — AOS_COMPLETION_VERDICT=off: o no nao calcula o veredicto; um turno sem tool calls fecha o run como concluido, qualquer que seja o texto. Para o voltar a medir remova a variavel"
	default:
		return "veredicto de conclusao (EPIC-02/AOS-493): EM OBSERVACAO — o veredicto calcula-se, vai na transicao terminal (outcome_reason e verdict) e em aos_runs_finished_total, e o desfecho do run NAO muda. AOS_COMPLETION_VERDICT=enforce passa a fechar em failed os runs com veredicto negativo"
	}
}

// fixarConclusao escreve no Goal o modo de aplicação do veredicto em que o run fica FIXADO
// (AOS-493). Um Goal sem modo é um run NOVO e fica no modo do nó. Um Goal que já o traz — o de
// uma retoma, que o lê do registo ([integration.ResumeRecord.GoalWith]) — fica como está: um
// run começado antes deste ticket chega com `off` e continua sem veredicto.
func (n *Node) fixarConclusao(goal agentruntime.Goal) agentruntime.Goal {
	if goal.CompletionMode != "" {
		return goal
	}
	goal.CompletionMode = defaultCompletionVerdict
	if n != nil && n.completionVerdict != "" {
		goal.CompletionMode = n.completionVerdict
	}
	return goal
}

// rotuloSemRazao é o valor do rótulo `reason` de um run sem veredicto negativo: a razão vazia
// de [agentruntime.OutcomeFulfilled], que em Prometheus se confundiria com rótulo nenhum.
const rotuloSemRazao = "none"

// desfechosDeRuns conta, por processo, os runs que este nó SELOU num estado terminal, pelo
// estado e pela razão do veredicto do kernel; e, à parte, os que concluíram sem pedir nenhuma
// tool call tendo tools na oferta.
//
// Os dois vocabulários são FECHADOS: os estados que [runGate.sealTerminal] materializa e as
// razões de [agentruntime.OutcomeReasons]. O rótulo nunca leva texto de terceiros.
//
// É por processo e desde o arranque. Em modo de observação um veredicto negativo aparece com
// `outcome="complete"`: é essa a série que diz quantos runs o modo de imposição teria fechado
// em `failed`.
type desfechosDeRuns struct {
	estados []state.State
	razoes  []agentruntime.OutcomeReason
	total   map[state.State]map[agentruntime.OutcomeReason]*atomic.Int64
	// semToolCall são os runs selados `complete` sem nenhuma tool call pedida, com pelo menos
	// uma tool oferecida ao modelo.
	semToolCall atomic.Int64
}

func novoDesfechosDeRuns() *desfechosDeRuns {
	d := &desfechosDeRuns{
		estados: []state.State{state.Complete, state.Failed, state.TimedOut},
		razoes:  append([]agentruntime.OutcomeReason{agentruntime.OutcomeFulfilled}, agentruntime.OutcomeReasons()...),
		total:   map[state.State]map[agentruntime.OutcomeReason]*atomic.Int64{},
	}
	for _, e := range d.estados {
		d.total[e] = map[agentruntime.OutcomeReason]*atomic.Int64{}
		for _, r := range d.razoes {
			d.total[e][r] = new(atomic.Int64)
		}
	}
	return d
}

// contar regista um run selado em `selado`. Nil-safe. Um estado fora dos três (o selo foi
// no-op: o run está parado, não acabou) não conta.
func (d *desfechosDeRuns) contar(selado state.State, res agentruntime.Result) {
	if d == nil {
		return
	}
	razao := agentruntime.OutcomeFulfilled
	if res.Verdict != nil {
		razao = res.Verdict.Reason
	}
	if porRazao := d.total[selado]; porRazao != nil {
		if c := porRazao[razao]; c != nil {
			c.Add(1)
		}
	}
	if selado == state.Complete && res.ToolCallsRequested == 0 && res.ToolsOffered > 0 {
		d.semToolCall.Add(1)
	}
}

// lido devolve o total de um par (estado, razão) dos vocabulários.
func (d *desfechosDeRuns) lido(estado state.State, razao agentruntime.OutcomeReason) int64 {
	if d == nil || d.total[estado] == nil || d.total[estado][razao] == nil {
		return 0
	}
	return d.total[estado][razao].Load()
}

// rotuloDaRazao é o valor do rótulo `reason` de uma razão do vocabulário.
func rotuloDaRazao(r agentruntime.OutcomeReason) string {
	if r == agentruntime.OutcomeFulfilled {
		return rotuloSemRazao
	}
	return string(r)
}
