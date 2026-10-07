package main

// tentativa_por_vazio.go — A SEGUNDA CLASSE DE NOVA TENTATIVA: O RUN QUE RESPONDEU VAZIO
// (AOS-510, emenda ao ADR-039 §2.3, §2.7 e §2.8).
//
// # O PROBLEMA
//
// Medido em produção a 2026-10-07 (v0.1.51), com a recuperação do AOS-502 ligada: 3 planos em 140
// saíram 13 por `empty_output`. Sempre o nó de resumo, que não tem tools: um só turno, motivo
// `stop`, texto final vazio. A prova do AOS-502 exige `contract_unmet_no_call`, que um run sem
// contrato de conclusão nunca dá, e o nó recusava repetir esse run.
//
// # A FORMA
//
// É uma segunda classe, com PROVA PRÓPRIA e INTERRUPTOR PRÓPRIO (`AOS_RUN_RETRY_EMPTY`); a
// primeira não muda. O pedido é o de sempre — `plan_request.attempt` — e NÃO ESCOLHE A CLASSE:
// nada do corpo diz porque se pede a tentativa. O nó lê a razão do veredicto da tentativa
// anterior no SEU log ([apiHandler.provarTentativa]) e aplica a prova dessa razão:
//
//   - `contract_unmet_no_call` ⇒ a prova do AOS-502, sem alteração;
//   - `empty_output` ⇒ a prova deste ficheiro ([julgarTentativaVazia]), e só com o interruptor
//     ligado;
//   - qualquer outra ⇒ recusa, como sempre.
//
// # PORQUE É QUE `empty_output` NÃO CHEGA
//
// O kernel fecha `empty_output` em dois casos, e só um é repetível. Sem origem vinculativa da
// saída, é o texto do turno final que veio vazio: num run que não pediu tool nenhuma, nada
// aconteceu. COM origem vinculativa, é a tool designada que devolveu zero bytes — houve uma tool
// call, e nunca se repete. E um run que chamou uma tool e depois respondeu vazio fecha com a
// mesma razão. Daí a prova exigir, além da razão: zero tool calls pelas três fontes do AOS-502,
// um só turno com `stop`, e um manifesto sem contrato de tools e sem origem vinculativa.
//
// # O TECTO É UM SÓ
//
// As tentativas desta classe contam para o mesmo `AOS_RUN_RETRY_MAX`: no máximo três runs do
// mesmo nó do plano, qualquer que seja a classe. A prova decide-se por elo — cada tentativa
// prova a imediatamente anterior, na classe da razão dela.
//
// # A OMISSÃO É `off`
//
// Com `off`, [apiHandler.provarTentativa] nunca chega a este ficheiro: um pedido de tentativa
// sobre um run `empty_output` é recusado com `anterior_outra_razao`, na série do AOS-502, como
// antes; o `GET /tools` e o `/metrics` têm os bytes de antes.

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// ErrBadRunRetryEmpty — AOS_RUN_RETRY_EMPTY tem um valor fora de {off, on}. Fail-closed: o nó não
// arranca. Cair para um dos dois em silêncio deixava o operador a medir uma série de planos
// convencido de que o nó repetia (ou não) os runs que respondem vazio.
var ErrBadRunRetryEmpty = errors.New("aos: AOS_RUN_RETRY_EMPTY invalido — valores aceites: off (a omissao: o no recusa a nova tentativa de um run que fechou empty_output) ou on (o no admite-a, com a prova propria do AOS-510)")

// apiRunRetryEmptyOptionFromEnv lê AOS_RUN_RETRY_EMPTY. Vazia ou `off` ⇒ (nil, false, nil):
// nenhuma opção. Sem normalização de caixa, e com os espaços à volta a não contarem — a regra das
// variáveis irmãs ([apiRunRetryNoticeOptionFromEnv]).
func apiRunRetryEmptyOptionFromEnv() (APIOption, bool, error) {
	switch v := strings.TrimSpace(os.Getenv("AOS_RUN_RETRY_EMPTY")); v {
	case "", "off":
		return nil, false, nil
	case "on":
		return WithRunRetryEmpty(), true, nil
	default:
		return nil, false, fmt.Errorf("%w (veio %q)", ErrBadRunRetryEmpty, v)
	}
}

// WithRunRetryEmpty liga a nova tentativa por resposta vazia (AOS-510). Só tem efeito com o tecto
// de tentativas acima de zero ([WithRunRetryMax]): com o tecto a zero o nó recusa toda a
// tentativa antes de ler o log.
func WithRunRetryEmpty() APIOption {
	return func(c *apiConfig) { c.runRetryEmpty = true }
}

// runRetryEmptyBanner declara a segunda classe. Só se imprime com `on`; com o tecto a zero diz
// que não tem efeito. Com `off` o arranque do nó escreve exactamente o que escrevia.
func runRetryEmptyBanner(tecto int) string {
	if tecto <= 0 {
		return "nova tentativa por resposta vazia (EPIC-19/AOS-510, ADR-039 §2.3): AOS_RUN_RETRY_EMPTY=on SEM EFEITO — o tecto de tentativas e zero (AOS_RUN_RETRY_MAX), e o no recusa toda a nova tentativa antes de ler o log"
	}
	return fmt.Sprintf("nova tentativa por resposta vazia (EPIC-19/AOS-510, ADR-039 §2.3): ACEITE — AOS_RUN_RETRY_EMPTY=on: o no hospeda a tentativa n de um no do plano tambem quando PROVA no seu log que a anterior fechou failed por empty_output, sem nenhuma tool call pedida, com um so turno e o motivo de paragem stop, sem contrato de tools e sem origem vinculativa da saida. O pedido nao escolhe a classe: e a razao do veredicto, lida do log, que a decide. Conta para o MESMO tecto (AOS_RUN_RETRY_MAX=%d). Esta tentativa NAO leva o aviso do AOS-506, mesmo com AOS_RUN_RETRY_NOTICE=on. O suporte anuncia-se no GET /tools (run_retry.empty_output). Remova a variavel ou defina off para o no voltar a recusar", tecto)
}

// razaoDaTentativaVazia é o `retry_reason` do `run.plan_origin` de uma tentativa desta classe: a
// razão do veredicto do run anterior, no vocabulário fechado do kernel.
const razaoDaTentativaVazia = string(agentruntime.OutcomeEmptyOutput)

// razaoDaClasse devolve o `retry_reason` a gravar na origem do run: [razaoDaTentativaVazia] numa
// tentativa admitida por esta classe, vazio em qualquer outro run (a primeira classe e o run sem
// tentativa gravam os bytes de sempre). Não recebe o pedido: o valor sai da prova.
func razaoDaClasse(prova *provaDaTentativa) string {
	if prova == nil || !prova.vazia {
		return ""
	}
	return razaoDaTentativaVazia
}

// fechouPorRespostaVazia diz se o veredicto selado do run é a razão desta classe.
func fechouPorRespostaVazia(desfecho state.Outcome) bool {
	v := desfecho.Verdict
	return v != nil && !v.Fulfilled && v.Reason == agentruntime.OutcomeEmptyOutput
}

// Causas de recusa de uma tentativa DEPOIS de o nó ler que a anterior fechou `empty_output` — o
// rótulo `causa` de `aos_runs_retry_empty_refused_total`. Vocabulário FECHADO. As recusas
// anteriores a essa leitura (tecto, forma, origem, sequência, estado) não têm classe, e contam
// na série do AOS-502 como sempre. Os nomes partilhados com o AOS-502 são as MESMAS constantes.
const (
	// causaVaziaEstadoImpossivel — o manifesto do run declara um contrato de tools (ou não
	// declara veredicto nenhum) e o run fechou `empty_output` sem tool calls: um estado que o
	// kernel não produz (o contrato tem precedência e fechava `contract_unmet_no_call`). Um log
	// assim não se interpreta, e não se repete.
	causaVaziaEstadoImpossivel = "estado_impossivel"
	// causaVaziaOrigemVinculativa — o manifesto declara a origem da saída com o vínculo que
	// obriga. Aí «vazio» quer dizer que a tool designada devolveu zero bytes.
	causaVaziaOrigemVinculativa = "anterior_origem_vinculativa"
)

// causasDeRecusaDaTentativaVazia é a lista fechada, pela ordem em que se documentam.
var causasDeRecusaDaTentativaVazia = []string{
	causaRetryIndisponivel, causaRetryPediuTools, causaRetryTurnos, causaRetryParagem,
	causaVaziaEstadoImpossivel, causaVaziaOrigemVinculativa, causaRetryResidencia,
}

// julgarTentativaVazia decide, só com o que o NÓ gravou, se um run que fechou `empty_output`
// admite uma nova tentativa. Devolve a causa da recusa (vazia ⇒ admite) e o `prompt_hash` do
// turno dele. É pura, e faz a prova INTEIRA: não assume que alguém conferiu o elo antes.
//
// O elo — o run existe, a origem é a do mesmo pedido e do mesmo nó, a sequência, `failed` — é o
// do AOS-502 ([factosDoElo]). A seguir, tudo o que se segue, e a falta de qualquer ponto recusa:
//
//   - o veredicto selado é `empty_output` (outra razão ⇒ esta prova não se aplica);
//   - ZERO tool calls pedidas, pelas três fontes: o total do vector selado, a ausência de
//     qualquer evento `tool.call.*`, e o contador do turno;
//   - UM SÓ `turn.recorded`, que parou com o motivo `stop`;
//   - o manifesto desse turno declara o veredicto, SEM contrato de tools;
//   - e SEM origem da saída com o vínculo que obriga.
func julgarTentativaVazia(eventos []eventstore.Event, estado state.State, desfecho state.Outcome, q quesitoDaTentativa) (string, string) {
	f, causa := factosDoElo(eventos, estado, q)
	if causa != "" {
		return causa, ""
	}
	if !fechouPorRespostaVazia(desfecho) {
		return causaRetryOutraRazao, ""
	}
	if desfecho.Verdict.ToolCallsRequested != 0 || f.pediuTools {
		return causaRetryPediuTools, ""
	}
	if f.turnos != 1 {
		return causaRetryTurnos, ""
	}
	if f.turno.ToolCallsRequested != 0 {
		return causaRetryPediuTools, ""
	}
	if f.turno.StopReason != agentruntime.StopStop {
		return causaRetryParagem, ""
	}
	// O MANIFESTO: o que o run declarou ao kernel, gravado pelo kernel no turno. Um veredicto
	// selado sem a declaração no manifesto, ou com tools exigidas, é um log que o kernel não
	// escreve; e as linhas do vector selado são as tools do contrato, pelo que têm de faltar.
	c := f.turno.Manifest.Completion
	if c == nil || len(c.Requires) != 0 || len(desfecho.Verdict.Tools) != 0 {
		return causaVaziaEstadoImpossivel, ""
	}
	if c.OutputBinding == agentruntime.OutputSourceBinds || (desfecho.OutputSource != nil && desfecho.OutputSource.Binding == agentruntime.OutputSourceBinds) {
		return causaVaziaOrigemVinculativa, ""
	}
	return "", f.turno.Manifest.PromptHash
}

// contagemDasTentativasVazias conta, por processo, as tentativas desta classe: as admitidas e as
// recusadas por causa. A comparação do prompt conta na série comum
// (`aos_runs_retry_prompt_hash_diferente_total`): o que ela vigia é o mesmo nas duas classes.
type contagemDasTentativasVazias struct {
	admitidas atomic.Int64
	once      sync.Once
	recusadas map[string]*atomic.Int64
}

func (c *contagemDasTentativasVazias) iniciar() {
	c.once.Do(func() {
		c.recusadas = make(map[string]*atomic.Int64, len(causasDeRecusaDaTentativaVazia))
		for _, causa := range causasDeRecusaDaTentativaVazia {
			c.recusadas[causa] = new(atomic.Int64)
		}
	})
}

// recusar conta uma recusa. Uma causa fora do vocabulário não tem contador e não soma.
func (c *contagemDasTentativasVazias) recusar(causa string) {
	c.iniciar()
	if n := c.recusadas[causa]; n != nil {
		n.Add(1)
	}
}

// recusadasPor devolve o total de uma causa do vocabulário.
func (c *contagemDasTentativasVazias) recusadasPor(causa string) int64 {
	c.iniciar()
	if n := c.recusadas[causa]; n != nil {
		return n.Load()
	}
	return 0
}

// houve diz se algum contador já saiu de zero.
func (c *contagemDasTentativasVazias) houve() bool {
	c.iniciar()
	if c.admitidas.Load() != 0 {
		return true
	}
	for _, n := range c.recusadas {
		if n.Load() != 0 {
			return true
		}
	}
	return false
}
