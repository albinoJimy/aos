package main

// nova_tentativa.go — A NOVA TENTATIVA DE UM NÓ DO PLANO, E A PROVA QUE O NÓ FAZ ANTES DE A
// HOSPEDAR (AOS-502, ADR-039).
//
// # O PROBLEMA
//
// Medido em produção de 2026-10-04 a 2026-10-06: em 12 de 74 planos o nó de leitura terminou num
// turno sem chamar a tool. Desde o AOS-493 o run fecha `failed` com
// `outcome_reason=contract_unmet_no_call`, e o plano sai 13. O mesmo pedido, repetido, dá os dois
// desfechos; repetir é a recuperação que os dados favorecem.
//
// Repetir é um run NOVO, com chaves de idempotência novas (`f(run_id, step_id)`): se o run
// anterior tivesse aplicado um efeito, o novo aplicava-o outra vez. Por isso a tentativa só é
// segura quando a anterior NÃO PEDIU TOOL NENHUMA — e quem tem esse facto selado é o nó.
//
// # A FORMA
//
// O `plan_request` do `POST /runs` ganha `attempt` (n ≥ 2). O id do run é
// `<plano>~<nó escapado>~<n>` ([idDaTentativa]). O `aos-orq` PEDE; o nó AUTORIZA, depois de ler
// do SEU log, sobre a tentativa imediatamente anterior ([julgarTentativaAnterior]):
//
//   - foi hospedada por este nó com o vínculo verificado ao MESMO pedido e ao MESMO nó
//     (`run.plan_origin`, escrito pelo nó);
//   - está `failed`, com o veredicto selado `contract_unmet_no_call` e zero tool calls pedidas;
//   - o stream não tem nenhum `tool.call.*`;
//   - tem um só `turn.recorded`, sem tool calls, que parou com o motivo `stop`;
//   - a residência dela é a região de quem pede.
//
// Nada disto vem do corpo. É a lição do AOS-408: um veredicto recalculado do que o chamador
// fornece não é um gate.
//
// # PORQUE É QUE A RAZÃO DO VEREDICTO NÃO CHEGA
//
// `contract_unmet_no_call` diz que a tool EM FALTA do contrato nunca foi pedida — não que o run
// não pediu nenhuma. Um run cujo contrato exige `a` e `b`, que chamou `a` e parou, fecha com essa
// mesma razão e com um efeito aplicado. Daí as três fontes independentes do mesmo facto: o total
// do vector selado, a ausência de eventos de mediação e o contador do turno.
//
// # O TECTO É DO NÓ
//
// `AOS_RUN_RETRY_MAX` ∈ {0, 1, 2}; a omissão é 0. Com 0 todo o pedido com `attempt` é recusado,
// o `GET /tools` não anuncia nada e o `/metrics` não ganha séries: o nó é o de antes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// maxRunRetry é o maior tecto de tentativas A MAIS que o nó aceita configurar: duas, isto é,
// três runs no máximo por nó do plano (decisão do dono de 2026-10-06).
const maxRunRetry = 2

// ErrBadRunRetryMax — AOS_RUN_RETRY_MAX tem um valor fora de {0, 1, 2}. Fail-closed: o nó não
// arranca. Cair para um valor em silêncio deixava o operador convencido de um tecto que não é o
// que está em vigor.
var ErrBadRunRetryMax = errors.New("aos: AOS_RUN_RETRY_MAX invalido — valores aceites: 0 (a omissao: o no recusa toda a nova tentativa), 1 ou 2 (tentativas a mais por no do plano)")

// apiRunRetryMaxOptionFromEnv lê AOS_RUN_RETRY_MAX. Vazia ou `0` ⇒ (nil, 0, nil): nenhuma opção,
// e o handler fica com o tecto a zero. O valor recusado não vai na mensagem além do que o
// operador escreveu na sua própria configuração.
func apiRunRetryMaxOptionFromEnv() (APIOption, int, error) {
	v := strings.TrimSpace(os.Getenv("AOS_RUN_RETRY_MAX"))
	if v == "" {
		return nil, 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > maxRunRetry || strconv.Itoa(n) != v {
		return nil, 0, fmt.Errorf("%w (veio %q)", ErrBadRunRetryMax, v)
	}
	if n == 0 {
		return nil, 0, nil
	}
	return WithRunRetryMax(n), n, nil
}

// WithRunRetryMax define o tecto de tentativas a mais por nó do plano (AOS-502). Um valor fora
// de 1..[maxRunRetry] deixa o tecto a zero — a direcção segura.
func WithRunRetryMax(n int) APIOption {
	return func(c *apiConfig) {
		if n >= 1 && n <= maxRunRetry {
			c.runRetryMax = n
		}
	}
}

// runRetryBanner declara o tecto em vigor. Só se imprime com o tecto acima de zero: com zero o
// arranque do nó escreve exactamente o que escrevia.
func runRetryBanner(n int) string {
	return fmt.Sprintf("nova tentativa de um no do plano (EPIC-19/AOS-502, ADR-039): ACEITE — ate %d tentativa(s) a mais por no do plano (AOS_RUN_RETRY_MAX=%d). O no so hospeda a tentativa n depois de PROVAR no seu log que a anterior fechou failed por contract_unmet_no_call, sem nenhuma tool call pedida e com o motivo de paragem stop. O suporte anuncia-se no GET /tools (run_retry). Para desligar remova a variavel", n, n)
}

// anuncioDaNovaTentativa é o que o `GET /tools` diz sobre a nova tentativa. A PRESENÇA do
// objecto é o anúncio; um nó com o tecto a zero não o devolve, e quem submete não envia
// `attempt`.
type anuncioDaNovaTentativa struct {
	// Max é o tecto de tentativas a mais por nó do plano em vigor neste nó.
	Max int `json:"max"`
}

// anuncioDaNovaTentativaDoNo compõe o anúncio, ou nil com o tecto a zero.
func anuncioDaNovaTentativaDoNo(tecto int) *anuncioDaNovaTentativa {
	if tecto <= 0 {
		return nil
	}
	return &anuncioDaNovaTentativa{Max: tecto}
}

// idDaTentativa é o id do run da tentativa `n` do nó `nodeID` do pedido `plano`: o id do run
// filho ([idDoRunFilho]) para a primeira, e esse id com `~<n>` para as seguintes. O `n` vai em
// decimal canónico. O `aos-orq` compõe o mesmo (`idDaTentativa` em
// `packages/cmd/aos-orq/nova_tentativa.go`); os dois binários não se importam, e
// [TestAOS502_Fio_VectoresDoId] prende os mesmos vectores dos dois lados.
//
// O `node_id` escapado nunca contém o separador, pelo que dentro de um pedido a decomposição é
// única: um separador depois do pedido é a primeira tentativa, dois é uma tentativa.
func idDaTentativa(plano, nodeID string, n int) string {
	base := idDoRunFilho(plano, nodeID)
	if n < 2 {
		return base
	}
	return base + separadorDoRunFilho + strconv.Itoa(n)
}

// errFormaDaTentativa — o pedido traz `attempt` e o resto do vínculo não tem a forma de uma
// tentativa. Junta-se ao [errVinculoRecusado], para a contagem saber que foi a forma.
var errFormaDaTentativa = errors.New("forma da nova tentativa invalida")

// formaDaTentativa confere a forma de um pedido com `attempt`: `n ≥ 2`, com o plano e o nó
// declarados, e o `run_id` igual ao que o nó compõe a partir deles. A comparação é por
// IGUALDADE: o sufixo do id recebido nunca se interpreta, pelo que `~02` e `~+2` não passam.
func formaDaTentativa(v vinculoAoPedido, runID string) error {
	switch {
	case v.Attempt < 2:
		return fmt.Errorf("%w: attempt tem de ser >= 2", errFormaDaTentativa)
	case v.PlanID == "" || v.NodeID == "":
		return fmt.Errorf("%w: attempt exige plan_id e node_id", errFormaDaTentativa)
	case runID != idDaTentativa(v.RunID, v.NodeID, v.Attempt):
		return fmt.Errorf("%w: run_id nao e <plano>%s<no>%s<attempt> do pedido nomeado", errFormaDaTentativa, separadorDoRunFilho, separadorDoRunFilho)
	}
	return nil
}

// Causas de recusa de uma nova tentativa — o rótulo `causa` de
// `aos_runs_retry_refused_total`. Vocabulário FECHADO: nenhum valor vem de fora deste ficheiro.
const (
	// causaRetryTecto — o tecto do nó é zero, ou `attempt` passa `tecto + 1`.
	causaRetryTecto = "tecto"
	// causaRetryForma — `attempt` sem a forma de uma tentativa ([formaDaTentativa]).
	causaRetryForma = "forma"
	// causaRetryIndisponivel — o log não se leu AGORA (substrato, WORM): 503, sem hospedar.
	causaRetryIndisponivel = "indisponivel"
	// causaRetryInexistente — o run anterior não existe neste nó.
	causaRetryInexistente = "anterior_inexistente"
	// causaRetryIlegivel — um evento do run anterior não se descodifica.
	causaRetryIlegivel = "anterior_ilegivel"
	// causaRetrySemOrigem — o run anterior não tem `run.plan_origin` escrito pelo nó: não foi
	// hospedado com o vínculo verificado (outro submissor, ou a origem não ficou gravada).
	causaRetrySemOrigem = "anterior_sem_origem"
	// causaRetryOutroPedido — a origem do run anterior nomeia outro pedido, outro nó, ou uma
	// geração posterior à do pedido.
	causaRetryOutroPedido = "anterior_de_outro_pedido"
	// causaRetrySequencia — a tentativa anterior não é a `n − 1` (salto, ou um run com o id de
	// uma tentativa que não foi admitido como tentativa).
	causaRetrySequencia = "anterior_fora_de_sequencia"
	// causaRetryEmCurso — o run anterior ainda não está num estado terminal.
	causaRetryEmCurso = "anterior_em_curso"
	// causaRetryNaoFalhou — terminal, e não `failed` (concluiu, expirou, foi morto).
	causaRetryNaoFalhou = "anterior_nao_falhou"
	// causaRetryOutraRazao — `failed` sem o veredicto `contract_unmet_no_call`.
	causaRetryOutraRazao = "anterior_outra_razao"
	// causaRetryPediuTools — o run anterior pediu pelo menos uma tool call (efectiva, negada,
	// falhada ou escalada). É a recusa que protege de repetir um efeito.
	causaRetryPediuTools = "anterior_pediu_tools"
	// causaRetryTurnos — o run anterior não tem exactamente um turno gravado.
	causaRetryTurnos = "anterior_turnos"
	// causaRetryParagem — o turno não parou com o motivo `stop` (cortado, filtrado, outro, ou
	// não reportado).
	causaRetryParagem = "anterior_paragem"
	// causaRetryResidencia — a residência do run anterior não está selada, ou não é a região de
	// quem pede.
	causaRetryResidencia = "anterior_residencia"
)

// causasDeRecusaDaTentativa é a lista fechada, pela ordem em que se documentam.
var causasDeRecusaDaTentativa = []string{
	causaRetryTecto, causaRetryForma, causaRetryIndisponivel,
	causaRetryInexistente, causaRetryIlegivel, causaRetrySemOrigem, causaRetryOutroPedido,
	causaRetrySequencia, causaRetryEmCurso, causaRetryNaoFalhou, causaRetryOutraRazao,
	causaRetryPediuTools, causaRetryTurnos, causaRetryParagem, causaRetryResidencia,
}

// prefixoDeToolCall é a família dos eventos de mediação do Reference Monitor
// (`tool.call.mediated`, `.denied`, `.escalated`, `.outcome`). Compara-se pela família e não por
// uma lista: um tipo novo de evento de tool call recusa a tentativa sem ninguém se lembrar.
const prefixoDeToolCall = "tool.call."

// quesitoDaTentativa é o que o pedido afirma e o nó vai conferir no log do run anterior.
type quesitoDaTentativa struct {
	anterior  string // o id do run da tentativa n − 1
	plano     string // o `run_id` do pedido de plano
	nodeID    string // o nó do plano
	tentativa int    // n
	geracao   int    // a geração da reclamação do pedido da tentativa
}

// turnoDaProva é o que a prova lê de um `turn.recorded`: contadores, o motivo de paragem e o
// hash do prompt. Sem conteúdo — o evento não o tem.
type turnoDaProva struct {
	Manifest struct {
		PromptHash string `json:"prompt_hash"`
	} `json:"manifest"`
	ToolCallsRequested int                     `json:"tool_calls_requested"`
	StopReason         agentruntime.StopReason `json:"stop_reason"`
}

// runJaNaoMuda diz se o estado durável de um run já não muda sozinho.
func runJaNaoMuda(st state.State) bool {
	switch st {
	case state.Complete, state.Failed, state.TimedOut, state.Killed:
		return true
	}
	return false
}

// julgarTentativaAnterior decide, só com o que o NÓ gravou, se o run anterior admite uma nova
// tentativa. Devolve a causa da recusa (vazia ⇒ admite) e o `prompt_hash` do turno dele.
//
// `eventos` é o stream inteiro do run anterior; `estado` e `desfecho` são o que a máquina de
// estados reconstrói dele ([NodeService.DurableOutcome]). A função é pura: não lê nada, e não
// recebe nada do corpo do pedido além do que está em `q`.
func julgarTentativaAnterior(eventos []eventstore.Event, estado state.State, desfecho state.Outcome, q quesitoDaTentativa) (string, string) {
	if !streamDeRun(q.anterior, eventos) {
		return causaRetryInexistente, ""
	}
	var (
		origem     *origemDoRunFilho
		turnos     int
		turno      turnoDaProva
		pediuTools bool
	)
	for _, ev := range eventos {
		switch {
		case strings.HasPrefix(ev.Type, prefixoDeToolCall):
			pediuTools = true
		case ev.Type == EventTypeRunPlanOrigin:
			if ev.Producer.NHIID != origemNHI || origem != nil {
				// Uma origem que não foi o nó a escrever não prova nada; e a primeira é o facto.
				continue
			}
			var o origemDoRunFilho
			if json.Unmarshal(ev.Payload, &o) != nil {
				return causaRetryIlegivel, ""
			}
			origem = &o
		case ev.Type == agentruntime.EventTypeTurnRecorded:
			turnos++
			if json.Unmarshal(ev.Payload, &turno) != nil {
				return causaRetryIlegivel, ""
			}
		}
	}
	switch {
	case origem == nil:
		return causaRetrySemOrigem, ""
	case origem.Pedido.Stream != planRequestStream || origem.Pedido.RunID != q.plano || origem.NodeID != q.nodeID:
		return causaRetryOutroPedido, ""
	case origem.Pedido.Geracao < 1 || origem.Pedido.Geracao > q.geracao:
		// A geração do run anterior pode ser ANTERIOR à do pedido (um `serve` morreu e outro
		// retomou); posterior é impossível num log honesto.
		return causaRetryOutroPedido, ""
	}
	// A SEQUÊNCIA: a anterior da tentativa 2 é a primeira (sem `attempt`); a de `n ≥ 3` foi ela
	// própria admitida como tentativa `n − 1`, sobre a que a precede.
	if q.tentativa == 2 {
		if origem.Attempt != 0 || origem.RetryOf != "" {
			return causaRetrySequencia, ""
		}
	} else if origem.Attempt != q.tentativa-1 || origem.RetryOf != idDaTentativa(q.plano, q.nodeID, q.tentativa-2) {
		return causaRetrySequencia, ""
	}
	switch {
	case !runJaNaoMuda(estado):
		return causaRetryEmCurso, ""
	case estado != state.Failed:
		return causaRetryNaoFalhou, ""
	}
	v := desfecho.Verdict
	if v == nil || v.Fulfilled || v.Reason != agentruntime.OutcomeContractNoCall {
		return causaRetryOutraRazao, ""
	}
	// ZERO TOOL CALLS PEDIDAS, por três fontes: o vector selado, os eventos de mediação e o turno.
	if v.ToolCallsRequested != 0 || pediuTools {
		return causaRetryPediuTools, ""
	}
	if turnos != 1 {
		return causaRetryTurnos, ""
	}
	if turno.ToolCallsRequested != 0 {
		return causaRetryPediuTools, ""
	}
	if turno.StopReason != agentruntime.StopStop {
		return causaRetryParagem, ""
	}
	return "", turno.Manifest.PromptHash
}

// provaDaTentativa é o que a prova deixa a quem hospeda: o run anterior e o hash do prompt dele.
type provaDaTentativa struct {
	anterior   string
	promptHash string
}

// provarTentativa faz a prova no log do nó. Devolve a causa da recusa (vazia ⇒ admite) e se ela
// é TRANSITÓRIA — o log não se leu agora, e quem chama responde 503 em vez de recusar.
//
// Chama-se DEPOIS de o vínculo ao pedido estar verificado ([apiHandler.submissorDoPedido]): quem
// chega aqui é o drenador com a reclamação viva do pedido, e a forma do id já foi conferida.
func (h *apiHandler) provarTentativa(ctx context.Context, chamador readerIdentity, v vinculoAoPedido) (provaDaTentativa, string, bool) {
	if h.cfg.runRetryMax <= 0 || v.Attempt > h.cfg.runRetryMax+1 {
		return provaDaTentativa{}, causaRetryTecto, false
	}
	if h.node == nil || h.node.EventStore == nil || h.readGov == nil || h.svc == nil {
		return provaDaTentativa{}, causaRetryIndisponivel, true
	}
	q := quesitoDaTentativa{
		anterior:  idDaTentativa(v.RunID, v.NodeID, v.Attempt-1),
		plano:     v.RunID,
		nodeID:    v.NodeID,
		tentativa: v.Attempt,
		geracao:   v.Geracao,
	}
	eventos, err := h.node.EventStore.Read(ctx, q.anterior, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return provaDaTentativa{}, causaRetryInexistente, false
		}
		return provaDaTentativa{}, causaRetryIndisponivel, true
	}
	estado, desfecho, err := h.svc.DurableOutcome(ctx, q.anterior)
	if err != nil {
		return provaDaTentativa{}, causaRetryIndisponivel, true
	}
	causa, hash := julgarTentativaAnterior(eventos, estado, desfecho, q)
	if causa != "" {
		return provaDaTentativa{}, causa, false
	}
	regiao, selada, err := h.readGov.runResidency(ctx, q.anterior)
	if err != nil {
		return provaDaTentativa{}, causaRetryIndisponivel, true
	}
	if !selada || regiao == "" || regiao != chamador.region {
		return provaDaTentativa{}, causaRetryResidencia, false
	}
	return provaDaTentativa{anterior: q.anterior, promptHash: hash}, "", false
}

// contagemDasTentativas conta, por processo, as tentativas admitidas e as recusadas por causa, e
// os prompts de tentativa que diferiram do da anterior.
type contagemDasTentativas struct {
	admitidas       atomic.Int64
	promptDiferente atomic.Int64
	once            sync.Once
	recusadas       map[string]*atomic.Int64
}

func (c *contagemDasTentativas) iniciar() {
	c.once.Do(func() {
		c.recusadas = make(map[string]*atomic.Int64, len(causasDeRecusaDaTentativa))
		for _, causa := range causasDeRecusaDaTentativa {
			c.recusadas[causa] = new(atomic.Int64)
		}
	})
}

// recusar conta uma recusa. Uma causa fora do vocabulário não tem contador e não soma.
func (c *contagemDasTentativas) recusar(causa string) {
	c.iniciar()
	if n := c.recusadas[causa]; n != nil {
		n.Add(1)
	}
}

// recusadasPor devolve o total de uma causa do vocabulário.
func (c *contagemDasTentativas) recusadasPor(causa string) int64 {
	c.iniciar()
	if n := c.recusadas[causa]; n != nil {
		return n.Load()
	}
	return 0
}

// houve diz se algum contador já saiu de zero.
func (c *contagemDasTentativas) houve() bool {
	c.iniciar()
	if c.admitidas.Load() != 0 || c.promptDiferente.Load() != 0 {
		return true
	}
	for _, n := range c.recusadas {
		if n.Load() != 0 {
			return true
		}
	}
	return false
}

// prazoDaMedicaoDoPrompt limita a espera pelo fim de uma tentativa para comparar o hash do
// prompt. Um run que dure mais do que isto fica sem a comparação — é medição.
const prazoDaMedicaoDoPrompt = 15 * time.Minute

// medirPromptDaTentativa compara, quando a tentativa acaba, o `prompt_hash` do seu primeiro turno
// com o do turno da tentativa anterior, e conta a diferença.
//
// É MEDIÇÃO E ALERTA, NÃO CONDIÇÃO: o hash só existe depois de o prompt estar montado, isto é,
// depois de o run ter sido hospedado. O que ela vigia é a promessa de que a tentativa repete o
// MESMO pedido. Vive na memória do processo: um reinício entre a admissão e o fim da tentativa
// perde a comparação desse run. O log não leva os hashes.
func (h *apiHandler) medirPromptDaTentativa(runID string, prova provaDaTentativa) {
	if prova.promptHash == "" || h.svc == nil || h.node == nil || h.node.EventStore == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), prazoDaMedicaoDoPrompt)
	defer cancel()
	if _, _, err := h.svc.Wait(ctx, runID); err != nil {
		return
	}
	eventos, err := h.node.EventStore.Read(ctx, runID, 1)
	if err != nil {
		return
	}
	for _, ev := range eventos {
		if ev.Type != agentruntime.EventTypeTurnRecorded {
			continue
		}
		var turno turnoDaProva
		if json.Unmarshal(ev.Payload, &turno) != nil {
			return
		}
		if turno.Manifest.PromptHash != prova.promptHash {
			h.tentativas.promptDiferente.Add(1)
			h.logf("submit (AOS-502): o prompt do primeiro turno da tentativa %q NAO e o do run anterior %q — a tentativa devia repetir o mesmo pedido", runID, prova.anterior)
		}
		return
	}
}
