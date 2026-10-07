package main

// aviso_da_tentativa.go — O AVISO NA NOVA TENTATIVA (AOS-506, emenda ao ADR-039 §2.7).
//
// # O PROBLEMA
//
// Medido em produção a 2026-10-07 (v0.1.50): em 33 de 34 runs que fecharam sem chamar a tool, a
// resposta do modelo era uma tool call ESCRITA COMO TEXTO. A nova tentativa do AOS-502 repete o
// mesmo pedido, e recupera a maior parte; mas 3 de 6 (projecção 1.0.0) e 8 de 25 (1.1.0) das
// tentativas voltaram a falhar da mesma maneira.
//
// # A FORMA
//
// Com `AOS_RUN_RETRY_NOTICE=on`, o run de uma tentativa que o nó ADMITIU com a prova do ADR-039
// §2.3 leva, na semente do tail, um segmento `notice` de texto CONSTANTE a seguir ao objectivo. O
// texto é do kernel ([agentruntime.RetryNoticeNoFunctionCall]); o nó só declara o valor.
//
// # QUEM O ACRESCENTA É O NÓ, E SÓ ELE
//
// O `POST /runs` não ganhou campo nenhum: o `aos-orq` continua a enviar o mesmo pedido, e não tem
// por onde pedir, recusar ou escrever o aviso. O nó declara-o no ponto em que a prova passou
// ([apiHandler.avisoDaTentativa]), e em mais nenhum: um run sem `attempt`, uma tentativa recusada
// ou um `POST /runs` directo nunca o levam.
//
// # NENHUM BYTE DO RUN ANTERIOR
//
// O que a tentativa anterior respondeu não é lido, nem aqui nem no kernel — o nó nem tem o texto
// em claro (está na captura cifrada do titular). O aviso diz o facto que a prova estabeleceu.
//
// # A OMISSÃO É `off`
//
// Com `off`, o run de uma tentativa é o de antes, byte a byte: o mesmo prompt, o mesmo
// `run.plan_origin`, o mesmo registo de retoma, as mesmas séries no `/metrics`.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// ErrBadRunRetryNotice — AOS_RUN_RETRY_NOTICE tem um valor fora de {off, on}. Fail-closed: o nó
// não arranca. Cair para um dos dois em silêncio deixava o operador a medir uma série de planos
// convencido de que as tentativas levavam (ou não) o aviso.
var ErrBadRunRetryNotice = errors.New("aos: AOS_RUN_RETRY_NOTICE invalido — valores aceites: off (a omissao: a nova tentativa repete o pedido tal e qual) ou on (a nova tentativa leva um aviso constante do runtime, AOS-506)")

// apiRunRetryNoticeOptionFromEnv lê AOS_RUN_RETRY_NOTICE. Vazia ou `off` ⇒ (nil, false, nil):
// nenhuma opção. Sem normalização de caixa: `On` não é `on`, pela regra das outras variáveis de
// vocabulário fechado do nó.
func apiRunRetryNoticeOptionFromEnv() (APIOption, bool, error) {
	switch v := strings.TrimSpace(os.Getenv("AOS_RUN_RETRY_NOTICE")); v {
	case "", "off":
		return nil, false, nil
	case "on":
		return WithRunRetryNotice(), true, nil
	default:
		return nil, false, fmt.Errorf("%w (veio %q)", ErrBadRunRetryNotice, v)
	}
}

// WithRunRetryNotice liga o aviso na nova tentativa (AOS-506). Só tem efeito com o tecto de
// tentativas acima de zero ([WithRunRetryMax]): sem tentativas admitidas não há a que o juntar.
func WithRunRetryNotice() APIOption {
	return func(c *apiConfig) { c.runRetryNotice = true }
}

// runRetryNoticeBanner declara o aviso. Só se imprime com `on`; com o tecto a zero diz que não
// tem efeito. Com `off` o arranque do nó escreve exactamente o que escrevia.
func runRetryNoticeBanner(tecto int) string {
	if tecto <= 0 {
		return "aviso na nova tentativa (EPIC-06/AOS-506, ADR-039 §2.7): AOS_RUN_RETRY_NOTICE=on SEM EFEITO — o tecto de tentativas e zero (AOS_RUN_RETRY_MAX), e o no recusa toda a nova tentativa: nao ha a que juntar o aviso"
	}
	return "aviso na nova tentativa (EPIC-06/AOS-506, ADR-039 §2.7): LIGADO — AOS_RUN_RETRY_NOTICE=on: o run de uma tentativa que o no admitiu com a prova leva, a seguir ao objectivo, um segmento notice de texto CONSTANTE escrito pelo runtime (a tentativa anterior nao fez nenhuma function call; uma tool so se pede pelo mecanismo de function calling). O texto nao leva um byte do run anterior nem do pedido, e o POST /runs nao ganhou campo nenhum. O prompt da tentativa passa a diferir do da anterior nesse segmento, e so nele: aos_runs_retry_prompt_hash_diferente_total compara com o hash esperado e continua a ter de ser zero. O run.plan_origin da tentativa leva retry_notice. Remova a variavel ou defina off para a tentativa repetir o pedido tal e qual"
}

// avisoDaTentativa decide o aviso do run que o `POST /runs` vai hospedar. É o ÚNICO sítio do nó
// que declara um [agentruntime.RetryNotice], e a condição é a conjunção dos dois factos que só o
// nó tem: o interruptor está ligado, e ESTE pedido passou a prova da nova tentativa (`prova` não
// é nil só depois de [apiHandler.provarTentativa] devolver a causa vazia).
//
// Não recebe o pedido: nada do corpo entra na decisão, e o valor devolvido é uma constante.
func (h *apiHandler) avisoDaTentativa(prova *provaDaTentativa) agentruntime.RetryNotice {
	if !h.cfg.runRetryNotice || prova == nil {
		return agentruntime.RetryNoticeNone
	}
	return agentruntime.RetryNoticeNoFunctionCall
}

// sementeDaTentativa é o que a medição do prompt guarda do run de uma tentativa com aviso: o que
// semeia o tail, e mais nada (a credencial e o resto do Goal não ficam à espera do fim do run).
type sementeDaTentativa struct {
	system    string
	objective string
	memory    []byte
	inputs    []agentruntime.PlanInput
	aviso     agentruntime.RetryNotice
}

// sementeDoGoal tira do Goal o que a medição precisa.
func sementeDoGoal(goal agentruntime.Goal) sementeDaTentativa {
	return sementeDaTentativa{
		system: goal.System, objective: goal.Objective, memory: goal.MemoryContext,
		inputs: goal.Inputs, aviso: goal.RetryNotice,
	}
}

// hashDaSementeCom calcula, do que o nó tem, o `prompt_hash` que o primeiro turno de um run com
// esta semente e com o aviso dado TERIA.
//
// O prefixo monta-se com o layout e o tool set que a própria tentativa gravou no manifesto do seu
// primeiro turno (escritos pelo nó), e a semente com a construção do kernel
// ([agentruntime.SeedTail]): não há aqui uma segunda definição do prompt.
func hashDaSementeCom(s sementeDaTentativa, aviso agentruntime.RetryNotice, turno turnoDaProva) (string, error) {
	tools := make([]agentruntime.ToolSpec, len(turno.Manifest.Tools))
	for i, dep := range turno.Manifest.Tools {
		tools[i] = agentruntime.ToolSpec(dep)
	}
	asm, err := agentruntime.NewPromptAssemblerFor(turno.Manifest.AssemblyVersion, s.system, tools)
	if err != nil {
		return "", err
	}
	semente, err := agentruntime.SeedTail(turno.Manifest.AssemblyVersion, agentruntime.Goal{
		System: s.system, Objective: s.objective, MemoryContext: s.memory, Inputs: s.inputs, RetryNotice: aviso,
	})
	if err != nil {
		return "", err
	}
	return asm.Assemble(1, semente).PromptHash, nil
}

// promptDaTentativaDifere diz se o prompt do primeiro turno de uma tentativa NÃO é o esperado.
// `avisoAnterior` é o aviso com que a tentativa ANTERIOR foi semeada (lido do `run.plan_origin`
// dela, que só o nó escreve), e `hashAnterior` o `prompt_hash` do turno dela.
//
// Sem aviso em nenhuma das duas, o esperado é o hash da anterior: a tentativa repete o pedido tal
// e qual (a regra do AOS-502, sem um byte nem um cálculo de diferença).
//
// Com aviso em alguma, os dois prompts diferem DE PROPÓSITO, e a comparação directa deixava de
// dizer alguma coisa. O que continua a ter de ser verdade é que a ÚNICA diferença é o aviso. Um
// hash não se estende, pelo que o nó recalcula os dois prompts a partir da MESMA semente — a do
// pedido desta tentativa — e exige ambos: com o aviso da anterior dá o hash que a anterior gravou
// (o pedido é o mesmo), e com o aviso desta dá o hash que esta gravou (não entrou mais nada).
// Qualquer outra diferença — outro objectivo, outras entradas, outro system, outras tools, um
// aviso com outros bytes — falha uma das duas igualdades. Um erro no recálculo conta como
// diferença: a medição que não se consegue fazer não pode ler-se como «igual».
func promptDaTentativaDifere(s sementeDaTentativa, avisoAnterior agentruntime.RetryNotice, hashAnterior string, turno turnoDaProva) bool {
	if s.aviso == agentruntime.RetryNoticeNone && avisoAnterior == agentruntime.RetryNoticeNone {
		return turno.Manifest.PromptHash != hashAnterior
	}
	daAnterior, err := hashDaSementeCom(s, avisoAnterior, turno)
	if err != nil {
		return true
	}
	desta, err := hashDaSementeCom(s, s.aviso, turno)
	if err != nil {
		return true
	}
	return daAnterior != hashAnterior || desta != turno.Manifest.PromptHash
}

// avisoDaOrigem lê, do stream de um run, o aviso com que o nó o semeou: o `retry_notice` do
// primeiro `run.plan_origin` escrito pelo nó. Vazio quando o run não o levou, ou quando a origem
// não se lê — a prova já recusou, antes, um run sem origem legível.
func avisoDaOrigem(eventos []eventstore.Event) agentruntime.RetryNotice {
	for _, ev := range eventos {
		if ev.Type != EventTypeRunPlanOrigin || ev.Producer.NHIID != origemNHI {
			continue
		}
		var o origemDoRunFilho
		if json.Unmarshal(ev.Payload, &o) != nil {
			return agentruntime.RetryNoticeNone
		}
		return agentruntime.RetryNotice(o.RetryNotice)
	}
	return agentruntime.RetryNoticeNone
}
