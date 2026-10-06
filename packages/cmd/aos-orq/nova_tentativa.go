package main

// A NOVA TENTATIVA DE UM NÓ DO PLANO, DO LADO DO `aos-orq` (AOS-503, ADR-039).
//
// Medido em produção de 2026-10-04 a 2026-10-06: em 12 de 74 planos o nó de leitura terminou num
// turno sem chamar a tool. O nó `aos` fecha esse run `failed` com `contract_unmet_no_call`
// (ADR-037), o nó do plano fechava `failed`, e o plano saía 13. O mesmo pedido, repetido, dá os
// dois desfechos.
//
// Este ficheiro é a recuperação: quando o run de um nó do plano fecha por essa razão SEM TER
// PEDIDO TOOL NENHUMA, o `aos-orq` não fecha o nó do plano — grava o facto no log do plano,
// volta a submeter o MESMO pedido como um run novo (a tentativa seguinte) e só a última decide.
//
// QUEM AUTORIZA É O NÓ `aos` (AOS-502). O `aos-orq` pede; o nó só hospeda a tentativa depois de
// provar, no log dele, que a anterior não pediu tools. A condição daqui ([elegivelParaNovaTentativa])
// é a mesma, lida do que o nó devolve, e serve para NÃO PEDIR o que o nó ia recusar — não é ela
// que protege de repetir um efeito.
//
// O QUE NÃO SE LÊ. A decisão usa o estado, a razão do veredicto e um contador, todos de
// vocabulário fechado. O texto final do run falhado não entra (o nó nem o devolve), e nada aqui
// julga a forma de texto nenhum.
//
// O INTERRUPTOR. `AOS_ORQ_NOVA_TENTATIVA`: `off` (a omissão) é o binário de antes; `observe`
// conta e diz o que tentaria, e não tenta; `on` tenta. Os factos do log LÊEM-SE em qualquer modo:
// um plano que ficou a meio de uma tentativa continua a ser seguido pelo run certo depois de o
// interruptor ser desligado — o que deixa de acontecer é começar tentativas.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	plan "github.com/aos-ref/control-plane/orchestrator/plan"
	plannerevents "github.com/aos-ref/control-plane/orchestrator/plannerevents"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// Modos do interruptor da nova tentativa.
const (
	// novaTentativaOff — desligado: um nó cujo run não concluiu fecha `failed`, como sempre.
	novaTentativaOff = "off"
	// novaTentativaObserve — conta e diz o que tentaria; não tenta.
	novaTentativaObserve = "observe"
	// novaTentativaOn — tenta, dentro dos tectos e contra um nó que anuncie o suporte.
	novaTentativaOn = "on"
)

// ErrNovaTentativa — a configuração da nova tentativa tem um valor que este binário não aceita.
var ErrNovaTentativa = errors.New("aos-orq: configuracao da nova tentativa invalida")

// modoDaNovaTentativaDoAmbiente lê o interruptor. Vazio ⇒ `off`. Um valor desconhecido recusa o
// arranque. As pontas aparam-se; as maiúsculas não se dobram — a regra de
// [modoDaSaidaPorReferenciaDoAmbiente]. O `os.Getenv` é LITERAL de propósito: o gate da superfície
// de variáveis de ambiente enumera-as pelo nome escrito no código.
func modoDaNovaTentativaDoAmbiente() (string, error) {
	return modoDaNovaTentativa(strings.TrimSpace(os.Getenv("AOS_ORQ_NOVA_TENTATIVA")))
}

// modoDaNovaTentativa valida um valor JÁ LIDO. O valor recusado não vai na mensagem.
func modoDaNovaTentativa(bruto string) (string, error) {
	switch bruto {
	case "", novaTentativaOff:
		return novaTentativaOff, nil
	case novaTentativaObserve:
		return novaTentativaObserve, nil
	case novaTentativaOn:
		return novaTentativaOn, nil
	default:
		return "", fmt.Errorf("%w: AOS_ORQ_NOVA_TENTATIVA aceita off (a omissao), observe e on", ErrNovaTentativa)
	}
}

const (
	// maxTentativasAMaisPorNo é o tecto do `aos-orq` por nó do plano: duas tentativas a mais, três
	// runs no máximo (decisão do dono de 2026-10-06). Vence o mais apertado entre este e o que o
	// nó `aos` anuncia.
	maxTentativasAMaisPorNo = 2
	// tectoDeTentativasPorPlanoPorOmissao é o tecto de tentativas a mais POR PLANO sem a variável.
	tectoDeTentativasPorPlanoPorOmissao = 4
	// maxTectoDeTentativasPorPlano limita o que a variável aceita: acima disto é um engano de
	// configuração, e não um tecto.
	maxTectoDeTentativasPorPlano = 64
)

// tectoDeTentativasPorPlanoDoAmbiente lê `AOS_ORQ_NOVA_TENTATIVA_MAX_POR_PLANO`: um inteiro de 0 a
// [maxTectoDeTentativasPorPlano]. Vazio ⇒ [tectoDeTentativasPorPlanoPorOmissao]. Zero é um valor:
// nenhuma tentativa em plano nenhum. Um valor inválido recusa o arranque.
func tectoDeTentativasPorPlanoDoAmbiente() (int, error) {
	return tectoDeTentativasPorPlano(strings.TrimSpace(os.Getenv("AOS_ORQ_NOVA_TENTATIVA_MAX_POR_PLANO")))
}

// tectoDeTentativasPorPlano valida um valor JÁ LIDO.
func tectoDeTentativasPorPlano(bruto string) (int, error) {
	if bruto == "" {
		return tectoDeTentativasPorPlanoPorOmissao, nil
	}
	n, err := strconv.Atoi(bruto)
	if err != nil || n < 0 || n > maxTectoDeTentativasPorPlano || strconv.Itoa(n) != bruto {
		return 0, fmt.Errorf("%w: AOS_ORQ_NOVA_TENTATIVA_MAX_POR_PLANO aceita um inteiro de 0 a %d (a omissao e %d)", ErrNovaTentativa, maxTectoDeTentativasPorPlano, tectoDeTentativasPorPlanoPorOmissao)
	}
	return n, nil
}

// configDaNovaTentativa é a nova tentativa tal como o `serve` a compõe.
type configDaNovaTentativa struct {
	// modo é o interruptor; o zero ("") porta-se como `off`.
	modo string
	// tectoDoNo é o tecto de tentativas a mais que o nó `aos` anuncia (`run_retry.max` do
	// `GET /tools`). Zero ⇒ o nó não anuncia o suporte, e não se tenta.
	tectoDoNo int
	// tectoDoPlano é o tecto de tentativas a mais por plano.
	tectoDoPlano int
	// prazo é o fim do prazo do `serve`: uma tentativa não começa depois dele.
	prazo time.Time
}

// ligada diz se o `serve` começa tentativas.
func (c configDaNovaTentativa) ligada() bool { return c.modo == novaTentativaOn }

// porNo é o tecto efectivo por nó: o mais apertado entre o do `aos-orq` e o que o nó anuncia.
func (c configDaNovaTentativa) porNo() int {
	if c.tectoDoNo < maxTentativasAMaisPorNo {
		return c.tectoDoNo
	}
	return maxTentativasAMaisPorNo
}

// bannerDaNovaTentativa declara, no arranque do `serve`, o que este processo faz quando um nó
// termina sem chamar a tool. Só se imprime fora de `off`.
func bannerDaNovaTentativa(c configDaNovaTentativa) string {
	switch {
	case c.modo == novaTentativaObserve:
		return "nova tentativa (AOS-503, ADR-039): EM OBSERVACAO — um no nao-verificador com tools cujo run feche failed por contract_unmet_no_call sem nenhuma tool call pedida e CONTADO e dito neste log, e NAO e tentado outra vez: os estados, os eventos do plano e o codigo de saida sao os de off"
	case c.tectoDoNo <= 0:
		return "nova tentativa (AOS-503, ADR-039): LIGADA E NAO APLICADA — o no nao anuncia o suporte (GET /tools sem run_retry: no anterior ao AOS-502, ou AOS_RUN_RETRY_MAX a zero). Nenhuma tentativa e pedida; um no que termine sem chamar a tool fecha failed, como antes"
	default:
		return fmt.Sprintf("nova tentativa (AOS-503, ADR-039): LIGADA — um no nao-verificador com tools cujo run feche failed por contract_unmet_no_call sem nenhuma tool call pedida volta a ser submetido como um run novo, ate %d tentativa(s) a mais por no (o no anuncia %d) e %d por plano. O facto de cada tentativa fica no log do plano antes do pedido; quem autoriza e o no, que prova no log dele que a anterior nao pediu tools", c.porNo(), c.tectoDoNo, c.tectoDoPlano)
	}
}

// idDaTentativa é o id do run da tentativa `n` do nó `nodeID` do run `runID`: o [childRunID] para
// a primeira, e esse id com `~<n>` para as seguintes. O nó `aos` compõe o mesmo
// (`idDaTentativa` em `packages/cmd/aos/nova_tentativa.go`) e só aceita essa forma; os dois
// binários não se importam, e os vectores partilhados prendem-nos ([TestAOS503_Fio_VectoresDoId]).
func idDaTentativa(runID, nodeID string, n int) string {
	base := childRunID(runID, nodeID)
	if n < 2 {
		return base
	}
	return base + separadorDoRunFilho + strconv.Itoa(n)
}

// elegivelParaNovaTentativa diz se o run de um nó do plano admite, do lado do `aos-orq`, uma nova
// tentativa. Lê SÓ vocabulário fechado:
//
//   - o nó não é verificador e tem tools atribuídas (com ou sem `consumes`: decisão 3 do dono);
//   - o nó `aos` conhece o run, e a resposta é sobre ESTE run;
//   - o run está `failed`, com a razão exactamente `contract_unmet_no_call`;
//   - o vector do veredicto diz zero tool calls pedidas — de qualquer tool. A razão sozinha não
//     chega: um run que chamou uma tool e deixou outra do contrato por pedir tem a mesma razão.
//
// O texto final não entra: o nó não o devolve num run que não concluiu, e a função não o lê.
func elegivelParaNovaTentativa(n plan.Node, tools []string, runID string, st estadoDoRun, existe bool) bool {
	if !existe || n.IsVerifier() || len(tools) == 0 {
		return false
	}
	if st.RunID != runID || st.Status != "failed" || st.OutcomeReason != string(agentruntime.OutcomeContractNoCall) {
		return false
	}
	return st.Verdict != nil && !st.Verdict.Fulfilled && st.Verdict.ToolCallsRequested == 0
}

// Motivos por que uma tentativa a que o nó tinha direito NÃO foi feita — o rótulo `causa` de
// [metricaTentativasRecusadas] e o `tentativa_recusada=` do resumo. Vocabulário FECHADO.
const (
	// recusaQuota — o nó `aos` respondeu 429 à submissão da tentativa (quota do principal, taxa
	// ou tecto de runs em curso). Não se insiste: a tentativa conta no orçamento de quem pediu.
	recusaQuota = "quota"
	// recusaTectoDoNo — o tecto que o nó `aos` anuncia é mais apertado do que o do `aos-orq`.
	recusaTectoDoNo = "tecto_do_no"
	// recusaTectoDoPlano — o plano já gastou as suas tentativas a mais.
	recusaTectoDoPlano = "tecto_do_plano"
	// recusaPrazo — o prazo do `serve` acabou.
	recusaPrazo = "prazo"
	// recusaNaoAnunciado — o nó `aos` não anuncia o suporte.
	recusaNaoAnunciado = "nao_anunciado"
	// recusaPeloNo — o nó `aos` recusou a submissão da tentativa (403: a prova dele não passou, ou
	// o vínculo ao pedido já não é o vivo; 409: já existe um run com esse id que não é deste plano).
	recusaPeloNo = "recusada_pelo_no"
)

// recusasDeTentativa é a lista fechada dos motivos, pela ordem em que se documentam.
var recusasDeTentativa = []string{recusaQuota, recusaTectoDoNo, recusaTectoDoPlano, recusaPrazo, recusaNaoAnunciado, recusaPeloNo}

// Desfechos de uma tentativa a mais — o rótulo `desfecho` de [metricaTentativas].
const (
	// tentativaRecuperou — o nó do plano fechou `complete` nesta tentativa.
	tentativaRecuperou = "recuperado"
	// tentativaVoltouAFalhar — o run da tentativa fechou outra vez por `contract_unmet_no_call`
	// sem tool calls. A taxa deste valor é a recorrência.
	tentativaVoltouAFalhar = "voltou_a_falhar"
	// tentativaOutraCausa — a tentativa acabou de outra maneira: outra razão do veredicto, run
	// perdido, saída que não se entrega.
	tentativaOutraCausa = "outra_causa"
)

// desfechosDeTentativa é a lista fechada.
var desfechosDeTentativa = []string{tentativaRecuperou, tentativaVoltouAFalhar, tentativaOutraCausa}

// rotuloComConsumes é o valor do rótulo `com_consumes`: as métricas separam os nós com e sem
// entradas, para a taxa da classe alargada pelo dono não se esconder na da classe medida.
func rotuloComConsumes(n plan.Node) string {
	if len(n.Consumes) > 0 {
		return "true"
	}
	return "false"
}

// tentativaDe devolve a tentativa corrente do nó do plano: 1, ou a maior que o log regista.
func (e *executorDeNos) tentativaDe(nodeID string) int {
	if t := e.tentativas[nodeID]; t > 1 {
		return t
	}
	return 1
}

// runDoNo é o id do run que faz AGORA o trabalho do nó do plano: o da tentativa corrente.
func (e *executorDeNos) runDoNo(nodeID string) string {
	return idDaTentativa(e.runID, nodeID, e.tentativaDe(nodeID))
}

// lerFactoDeTentativa regista um `plan.node_attempt_started` lido do log do plano. A tentativa
// corrente de um nó é a maior que o log regista; o contador do plano é o número de factos.
func (e *executorDeNos) lerFactoDeTentativa(a plannerevents.NodeAttemptStartedPayload) {
	if a.NodeID == "" || a.Attempt < 2 {
		return
	}
	e.factosDeTentativa++
	if a.Attempt > e.tentativas[a.NodeID] {
		e.tentativas[a.NodeID] = a.Attempt
	}
}

// motivoDaRecusaDaTentativa classifica o erro da submissão de uma tentativa. Devolve o motivo e
// true quando o nó RECUSOU (e não se insiste); false quando o erro é de outra natureza — rede,
// 5xx, IdP —, e quem chama propaga-o como propaga o de uma primeira submissão.
func motivoDaRecusaDaTentativa(err error) (string, bool) {
	var es *erroDeSubmissao
	switch {
	case errors.Is(err, errRunFilhoJaExiste):
		return recusaPeloNo, true
	case errors.Is(err, errRequerenteForaDoMandato):
		// Determinista para o pedido inteiro: é o `consume` que o fecha como terminal.
		return "", false
	case errors.As(err, &es) && es.status == 429:
		return recusaQuota, true
	case errors.As(err, &es) && es.status == 403:
		return recusaPeloNo, true
	}
	return "", false
}

// novaTentativa decide, com o run da tentativa corrente de um nó já TERMINAL, se o nó do plano
// continua em voo com uma tentativa nova. Devolve true quando a tentativa seguinte foi submetida:
// o nó do plano NÃO fecha. Com false, quem chama fecha o nó pela regra de sempre.
//
// A ORDEM É O CONTRATO: o facto `plan.node_attempt_started` fica no log do plano ANTES do pedido
// ao nó. Um processo que morra entre os dois deixa um facto sem run, que a retoma resolve lendo
// o estado do id da tentativa ([executorDeNos.retomarTentativa]).
func (e *executorDeNos) novaTentativa(ctx context.Context, nodeID string, st estadoDoRun, existe bool) (bool, error) {
	if e.nt.modo != novaTentativaObserve && e.nt.modo != novaTentativaOn {
		return false, nil
	}
	if e.geracaoDoPedido <= 0 || !e.declararOrigem {
		// SEM O VÍNCULO AO PEDIDO, COM O PLANO E O NÓ DECLARADOS, NÃO HÁ TENTATIVA: é por ele que o
		// nó `aos` deriva o submissor e prova a tentativa. É o caso de um `serve` manual, e de um
		// nó anterior ao AOS-477. Não se conta nem se grava nada.
		return false, nil
	}
	n := e.nos[nodeID]
	actual := e.tentativaDe(nodeID)
	if !elegivelParaNovaTentativa(n, nomesDasTools(e.tools[nodeID]), e.runDoNo(nodeID), st, existe) {
		return false, nil
	}
	consumes := rotuloComConsumes(n)
	if actual == 1 {
		e.medicao.primeiraFalhaSemChamar(consumes)
	} else {
		// A tentativa `actual` voltou a falhar do mesmo modo: conta-se aqui, uma vez, e o fecho
		// do nó não a volta a contar.
		e.medicao.tentativaFeita(actual, tentativaVoltouAFalhar, consumes)
		e.tentativaContada[nodeID] = true
	}
	if e.nt.modo == novaTentativaObserve {
		e.medicao.tentativaEmObservacao(consumes)
		fmt.Printf("  execucao: no %s NOVA TENTATIVA EM OBSERVACAO — tentaria outra vez (causa=%s, zero tool calls pedidas, com_consumes=%s); nao tenta: o no fecha como em off\n",
			nodeID, agentruntime.OutcomeContractNoCall, consumes)
		return false, nil
	}
	recusar := func(motivo string) (bool, error) {
		e.recusasDeTentativa[nodeID] = motivo
		e.medicao.tentativaRecusada(motivo)
		fmt.Printf("  execucao: no %s NOVA TENTATIVA NAO FEITA tentativa_recusada=%s (tentativa corrente %d) — o no fecha failed com a causa do run\n", nodeID, motivo, actual)
		return false, nil
	}
	switch {
	case e.nt.tectoDoNo <= 0:
		return recusar(recusaNaoAnunciado)
	case actual-1 >= e.nt.porNo():
		if e.nt.porNo() < maxTentativasAMaisPorNo {
			return recusar(recusaTectoDoNo)
		}
		// As tentativas ESGOTARAM-SE: não é uma recusa, é o fim do que a decisão do dono permite.
		e.esgotados[nodeID] = true
		fmt.Printf("  execucao: no %s tentativas_esgotadas tentativas=%d — o run voltou a fechar por %s sem chamar a tool; o no fecha failed\n",
			nodeID, actual, agentruntime.OutcomeContractNoCall)
		return false, nil
	case e.factosDeTentativa >= e.nt.tectoDoPlano:
		return recusar(recusaTectoDoPlano)
	case !e.nt.prazo.IsZero() && !e.agora().Before(e.nt.prazo):
		return recusar(recusaPrazo)
	}
	proxima := actual + 1
	// O FACTO, ANTES DO PEDIDO.
	if _, err := e.rec.RecordNodeAttemptStarted(ctx, plannerevents.NodeAttemptStartedPayload{
		NodeID: nodeID, Attempt: proxima, RetryOf: idDaTentativa(e.runID, nodeID, actual),
		Reason: plannerevents.AttemptReasonContractUnmetNoCall,
	}, n); err != nil {
		return false, fmt.Errorf("facto da tentativa %d de %q: %w", proxima, nodeID, err)
	}
	e.tentativas[nodeID] = proxima
	e.factosDeTentativa++
	delete(e.tentativaContada, nodeID)
	fmt.Printf("  execucao: no %s NOVA TENTATIVA %d — o run %s fechou failed por %s sem nenhuma tool call pedida; o facto esta no log do plano e o mesmo pedido volta a ser submetido como %s\n",
		nodeID, proxima, idDaTentativa(e.runID, nodeID, actual), agentruntime.OutcomeContractNoCall, e.runDoNo(nodeID))
	if err := e.submeterTentativa(ctx, nodeID, proxima); err != nil {
		motivo, recusada := motivoDaRecusaDaTentativa(err)
		if !recusada {
			return false, err
		}
		// O nó recusou. O facto fica (a tentativa foi pedida); o nó do plano fecha com a causa
		// do run ANTERIOR, que é o que `st` descreve — e por isso a tentativa corrente, em
		// memória, volta a ser a dele.
		e.tentativas[nodeID] = actual
		e.tentativaContada[nodeID] = true
		fmt.Printf("  execucao: no %s a submissao da tentativa %d foi RECUSADA pelo no aos\n", nodeID, proxima)
		return recusar(motivo)
	}
	return true, nil
}

// retomarTentativa trata o nó do plano que este processo encontrou `running` com uma tentativa
// registada no log e cujo run o nó `aos` NÃO conhece: o `serve` anterior morreu entre o facto e o
// pedido. Submete-a — uma vez. Devolve true quando o nó continua em voo.
//
// NUNCA REENVIA ÀS CEGAS: só se chega aqui depois de ler o estado do id da tentativa e de o nó
// responder 404. Um 409 nesta submissão é o pedido do `serve` anterior a ter chegado entretanto:
// o run existe, e segue-se.
func (e *executorDeNos) retomarTentativa(ctx context.Context, nodeID string) (bool, error) {
	actual := e.tentativaDe(nodeID)
	e.retomadas[nodeID] = true
	fmt.Printf("  execucao: no %s RETOMA da tentativa %d — o log do plano regista-a e o no aos nao conhece o run %s: submete-se agora\n", nodeID, actual, e.runDoNo(nodeID))
	err := e.submeterTentativa(ctx, nodeID, actual)
	if err == nil || errors.Is(err, errRunFilhoJaExiste) {
		return true, nil
	}
	motivo, recusada := motivoDaRecusaDaTentativa(err)
	if !recusada {
		return false, err
	}
	// Recusada: o nó do plano fecha com a causa do run anterior, que se lê agora.
	e.tentativas[nodeID] = actual - 1
	e.tentativaContada[nodeID] = true
	e.recusasDeTentativa[nodeID] = motivo
	e.medicao.tentativaRecusada(motivo)
	fmt.Printf("  execucao: no %s NOVA TENTATIVA NAO FEITA tentativa_recusada=%s (retoma da tentativa %d) — o no fecha failed com a causa do run anterior\n", nodeID, motivo, actual)
	st, existe, serr := e.cli.Status(ctx, e.runDoNo(nodeID))
	if serr != nil {
		return false, fmt.Errorf("estado do run anterior de %q: %w", nodeID, serr)
	}
	return false, e.fechar(ctx, nodeID, st, existe)
}

// contarFechoDaTentativa conta, no fecho DEFINITIVO de um nó do plano que teve tentativas a mais,
// o desfecho da última — a não ser que ela já tenha sido contada ([executorDeNos.novaTentativa]).
func (e *executorDeNos) contarFechoDaTentativa(nodeID string, concluiu bool) {
	actual := e.tentativaDe(nodeID)
	if actual < 2 || e.tentativaContada[nodeID] {
		return
	}
	n := e.nos[nodeID]
	if concluiu {
		e.medicao.tentativaFeita(actual, tentativaRecuperou, rotuloComConsumes(n))
		e.medicao.noRecuperado(rotuloComConsumes(n))
		return
	}
	e.medicao.tentativaFeita(actual, tentativaOutraCausa, rotuloComConsumes(n))
}

// sufixoDasTentativas é o que a linha de fecho de um nó `failed` acrescenta quando houve
// tentativas: a contagem, e porque não houve mais. Vazio para um nó sem tentativas.
func (e *executorDeNos) sufixoDasTentativas(nodeID string) string {
	var b strings.Builder
	if t := e.tentativaDe(nodeID); t > 1 {
		fmt.Fprintf(&b, " tentativas=%d", t)
	}
	if e.esgotados[nodeID] {
		b.WriteString(" tentativas_esgotadas")
	}
	if m := e.recusasDeTentativa[nodeID]; m != "" {
		b.WriteString(" tentativa_recusada=" + m)
	}
	return b.String()
}

// fecharMedicaoDasTentativas entrega à medição o que o plano fica a dizer sobre as tentativas,
// quando o `serve` acaba: quantas o log regista, quantos nós concluíram numa tentativa a mais, e
// quantos as esgotaram ou viram uma recusada. Com o interruptor desligado e sem factos no log
// não escreve nada.
func (e *executorDeNos) fecharMedicaoDasTentativas(concluido func(nodeID string) bool) {
	if e == nil || e.medicao == nil {
		return
	}
	if !e.nt.ligada() && e.factosDeTentativa == 0 {
		return
	}
	r := &resumoDasTentativas{ligada: e.nt.ligada(), tentativas: e.factosDeTentativa, esgotados: len(e.esgotados)}
	for id, t := range e.tentativas {
		if t > 1 && concluido(id) {
			r.recuperados++
		}
	}
	vistos := map[string]bool{}
	for _, m := range e.recusasDeTentativa {
		vistos[m] = true
	}
	for m := range vistos {
		r.recusadas = append(r.recusadas, m)
	}
	sort.Strings(r.recusadas)
	e.medicao.tentativasDoPlano = r
}

// resumoDasTentativas é o que UM `serve` deixa dito sobre as tentativas a mais do plano.
type resumoDasTentativas struct {
	// ligada — o `serve` correu com o interruptor em `on`.
	ligada bool
	// tentativas é o número de factos `plan.node_attempt_started` do plano — durável, soma as de
	// todos os `serve` que o trabalharam.
	tentativas int
	// recuperados são os nós do plano `complete` cuja tentativa corrente é a segunda ou a terceira.
	recuperados int
	// esgotados são os nós que este `serve` fechou `failed` com as tentativas esgotadas.
	esgotados int
	// recusadas são os motivos, sem repetição e por ordem, das tentativas que não se fizeram.
	recusadas []string
}

// classeDeTentativasPorPlano é o rótulo `tentativas` de [metricaTentativasPorPlano].
func classeDeTentativasPorPlano(n int) string {
	if n >= 4 {
		return "4_ou_mais"
	}
	if n < 0 {
		n = 0
	}
	return strconv.Itoa(n)
}

// classesDeTentativasPorPlano é a lista fechada do rótulo.
var classesDeTentativasPorPlano = []string{"0", "1", "2", "3", "4_ou_mais"}

// As medições da nova tentativa (medicao_do_contrato.go guarda-as). Todas nil-safe: num `serve`
// manual não há ficheiro de métricas.

func (m *medicaoDoContrato) primeiraFalhaSemChamar(comConsumes string) {
	if m == nil {
		return
	}
	if m.primeirasFalhas == nil {
		m.primeirasFalhas = map[string]int{}
	}
	m.primeirasFalhas[comConsumes]++
}

func (m *medicaoDoContrato) tentativaFeita(tentativa int, desfecho, comConsumes string) {
	if m == nil {
		return
	}
	if m.tentativasFeitas == nil {
		m.tentativasFeitas = map[chaveDeTentativa]int{}
	}
	m.tentativasFeitas[chaveDeTentativa{tentativa: strconv.Itoa(tentativa), desfecho: desfecho, comConsumes: comConsumes}]++
}

func (m *medicaoDoContrato) noRecuperado(comConsumes string) {
	if m == nil {
		return
	}
	if m.nosRecuperados == nil {
		m.nosRecuperados = map[string]int{}
	}
	m.nosRecuperados[comConsumes]++
}

func (m *medicaoDoContrato) tentativaRecusada(motivo string) {
	if m == nil {
		return
	}
	if m.tentativasRecusadas == nil {
		m.tentativasRecusadas = map[string]int{}
	}
	m.tentativasRecusadas[motivo]++
}

func (m *medicaoDoContrato) tentativaEmObservacao(comConsumes string) {
	if m == nil {
		return
	}
	if m.tentativasEmObservacao == nil {
		m.tentativasEmObservacao = map[string]int{}
	}
	m.tentativasEmObservacao[comConsumes]++
}

// chaveDeTentativa é a chave de [medicaoDoContrato.tentativasFeitas].
type chaveDeTentativa struct{ tentativa, desfecho, comConsumes string }

// registarNovaTentativa soma às séries o que um `serve` mediu sobre a nova tentativa. Só escreve
// rótulos do vocabulário fechado, e só séries com valor: com o interruptor em `off` o ficheiro de
// métricas é o de antes.
func (m *metricasDoConsumo) registarNovaTentativa(c *medicaoDoContrato) {
	if c == nil {
		return
	}
	consumes := []string{"false", "true"}
	for _, v := range consumes {
		if n := c.primeirasFalhas[v]; n > 0 {
			m.somar(serie(metricaPrimeirasFalhas, "com_consumes", v), float64(n))
		}
		if n := c.nosRecuperados[v]; n > 0 {
			m.somar(serie(metricaNosRecuperados, "com_consumes", v), float64(n))
		}
		if n := c.tentativasEmObservacao[v]; n > 0 {
			m.somar(serie(metricaTentativasEmObservacao, "com_consumes", v), float64(n))
		}
		for _, t := range []string{"2", "3"} {
			for _, d := range desfechosDeTentativa {
				if n := c.tentativasFeitas[chaveDeTentativa{tentativa: t, desfecho: d, comConsumes: v}]; n > 0 {
					m.somar(serie(metricaTentativas, "tentativa", t, "desfecho", d, "com_consumes", v), float64(n))
				}
			}
		}
	}
	for _, motivo := range recusasDeTentativa {
		if n := c.tentativasRecusadas[motivo]; n > 0 {
			m.somar(serie(metricaTentativasRecusadas, "causa", motivo), float64(n))
		}
	}
}

// registarPlanoComTentativas soma as séries POR PLANO, quando o pedido chega a um desfecho
// terminal: um plano recuperado (saiu 0 com pelo menos um nó concluído numa tentativa a mais), um
// plano que esgotou as tentativas de pelo menos um nó, e a classe do número de tentativas do
// plano. Só com o interruptor em `on` ou com tentativas no log.
func (m *metricasDoConsumo) registarPlanoComTentativas(r *resumoDasTentativas, codigo int) {
	if r == nil {
		return
	}
	if codigo == exitOK && r.recuperados > 0 {
		m.somar(metricaPlanosRecuperados, 1)
	}
	if r.esgotados > 0 {
		m.somar(metricaPlanosEsgotados, 1)
	}
	m.somar(serie(metricaTentativasPorPlano, "tentativas", classeDeTentativasPorPlano(r.tentativas)), 1)
}
