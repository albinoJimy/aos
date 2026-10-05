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
// estado e pela razão do veredicto do kernel; à parte, os que concluíram sem pedir nenhuma
// tool call tendo tools no tool set; e, noutra família, SOBRE O QUE cada run acabou.
//
// Os vocabulários são todos FECHADOS: os estados que [runGate.sealTerminal] materializa, as
// razões de [agentruntime.OutcomeReasons], os dois valores do veredicto ([rotuloVeredictoNegativo]
// e [rotuloSemRazao]) e os desfechos de [agentruntime.LastToolOutcomes]. O rótulo nunca leva
// texto de terceiros.
//
// É por processo e desde o arranque. Em modo de observação um veredicto negativo aparece com
// `outcome="complete"`: é essa a série que diz quantos runs o modo de imposição teria fechado
// em `failed`.
type desfechosDeRuns struct {
	estados []state.State
	razoes  []agentruntime.OutcomeReason
	total   map[state.State]map[agentruntime.OutcomeReason]*atomic.Int64
	// semToolCall são os runs selados `complete` sem nenhuma tool call pedida, com pelo menos
	// uma tool no TOOL SET DO RUN ([agentruntime.Result.ToolsOffered], que é `len(Goal.Tools)`).
	// Não é «o que o gateway enviou ao provider»: essa contagem é por turno
	// ([agentruntime.ModelResponse.ToolsOffered], AOS-491) e pode ser menor — um run com tools
	// no tool set cujo cliente de modelo não as ofereceu conta aqui na mesma.
	semToolCall atomic.Int64
	// ultimos são os desfechos do último turno que despachou tool calls
	// ([agentruntime.LastToolOutcomes]), na ordem do kernel.
	ultimos []string
	// sobre conta os runs selados por (estado, veredicto negativo ou não, desfecho do último
	// turno com tool calls). É a medição da classe que o veredicto NÃO apanha (revisão I6): o
	// run que acaba sobre uma recusa ou uma falha de tool com o contrato cumprido, ou sem
	// contrato, tem veredicto positivo e sai `reason="none"` na outra família.
	sobre map[chaveSobre]*atomic.Int64
	// origens conta os runs selados que DECLARARAM a origem da saída (AOS-497, ADR-038), pelo
	// vínculo da declaração e pelo estado da designação — os dois vocabulários fechados do
	// kernel ([agentruntime.OutputSourceBindings], [agentruntime.OutputSourceStates]). É por
	// aqui que uma declaração «só medição» se lê: a designação não entra no veredicto, e o que
	// a imposição teria fechado é `binding="measure"` com `state` diferente de `designated`.
	// Só conta um run cujo desfecho ficou SELADO: sem selo não há âncora no log, e contar aqui
	// uma que ninguém consegue ler de volta era a métrica a dizer mais do que o registo.
	origens map[chaveOrigem]*atomic.Int64
}

// chaveOrigem é a chave de [desfechosDeRuns.origens].
type chaveOrigem struct {
	vinculo agentruntime.OutputSourceBinding
	estado  agentruntime.OutputSourceState
}

// chaveSobre é a chave de [desfechosDeRuns.sobre].
type chaveSobre struct {
	estado   state.State
	negativo bool
	ultimo   string
}

// rotuloVeredictoNegativo é o valor do rótulo `verdict` de um run com veredicto negativo; o de
// um run sem ele (veredicto positivo, ou run sem veredicto) é [rotuloSemRazao].
const rotuloVeredictoNegativo = "negative"

// rotuloDoVeredicto é o valor do rótulo `verdict`.
func rotuloDoVeredicto(negativo bool) string {
	if negativo {
		return rotuloVeredictoNegativo
	}
	return rotuloSemRazao
}

func novoDesfechosDeRuns() *desfechosDeRuns {
	d := &desfechosDeRuns{
		estados: []state.State{state.Complete, state.Failed, state.TimedOut},
		razoes:  append([]agentruntime.OutcomeReason{agentruntime.OutcomeFulfilled}, agentruntime.OutcomeReasons()...),
		total:   map[state.State]map[agentruntime.OutcomeReason]*atomic.Int64{},
		ultimos: agentruntime.LastToolOutcomes(),
		sobre:   map[chaveSobre]*atomic.Int64{},
		origens: map[chaveOrigem]*atomic.Int64{},
	}
	for _, vinculo := range agentruntime.OutputSourceBindings() {
		for _, estado := range agentruntime.OutputSourceStates() {
			d.origens[chaveOrigem{vinculo, estado}] = new(atomic.Int64)
		}
	}
	for _, e := range d.estados {
		d.total[e] = map[agentruntime.OutcomeReason]*atomic.Int64{}
		for _, r := range d.razoes {
			d.total[e][r] = new(atomic.Int64)
		}
		for _, negativo := range []bool{false, true} {
			for _, u := range d.ultimos {
				d.sobre[chaveSobre{e, negativo, u}] = new(atomic.Int64)
			}
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
	// `res.ToolsOffered` é o tamanho do tool set do run, não o que o gateway enviou em cada
	// turno — ver o campo [desfechosDeRuns.semToolCall].
	if selado == state.Complete && res.ToolCallsRequested == 0 && res.ToolsOffered > 0 {
		d.semToolCall.Add(1)
	}
	// SOBRE O QUE O RUN ACABOU. Um Result que não passou pelo loop (o run recusado antes do
	// primeiro turno) não despachou nada: `none`. Um valor fora do vocabulário não tem
	// contador e não soma — não se cria série nenhuma a partir do que o Result traga.
	ultimo := res.LastToolOutcome
	if ultimo == "" {
		ultimo = agentruntime.ToolOutcomeNone
	}
	negativo := res.Verdict != nil && !res.Verdict.Fulfilled
	if c := d.sobre[chaveSobre{selado, negativo, ultimo}]; c != nil {
		c.Add(1)
	}
	// A ORIGEM DA SAÍDA (AOS-497): só os runs que a declararam, e só quando o desfecho ficou no
	// log (um dos três estados contados). Um par fora dos vocabulários não tem contador.
	if o := res.OutputSource; o != nil && d.total[selado] != nil {
		if c := d.origens[chaveOrigem{o.Binding, o.State}]; c != nil {
			c.Add(1)
		}
	}
}

// lidoOrigem devolve o total de um par (vínculo, estado da designação) dos vocabulários.
func (d *desfechosDeRuns) lidoOrigem(vinculo agentruntime.OutputSourceBinding, estado agentruntime.OutputSourceState) int64 {
	if d == nil {
		return 0
	}
	if c := d.origens[chaveOrigem{vinculo, estado}]; c != nil {
		return c.Load()
	}
	return 0
}

// lidoSobre devolve o total de um trio (estado, veredicto negativo, desfecho do último turno
// com tool calls) dos vocabulários.
func (d *desfechosDeRuns) lidoSobre(estado state.State, negativo bool, ultimo string) int64 {
	if d == nil {
		return 0
	}
	if c := d.sobre[chaveSobre{estado, negativo, ultimo}]; c != nil {
		return c.Load()
	}
	return 0
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
