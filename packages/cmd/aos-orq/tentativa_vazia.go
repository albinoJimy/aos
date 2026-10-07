package main

// A NOVA TENTATIVA DE UM NÓ DO PLANO QUE RESPONDEU VAZIO, DO LADO DO `aos-orq` (AOS-511, ADR-039).
//
// Medido em produção a 2026-10-07 (v0.1.51), com a recuperação do AOS-503 ligada: 3 planos em 140
// saíram 13 por `empty_output`. Sempre o nó de resumo, que não tem tools: um turno, motivo `stop`,
// texto final vazio. A elegibilidade do AOS-503 só cobre `contract_unmet_no_call`.
//
// Este ficheiro é a SEGUNDA CLASSE da recuperação: quando o run de um nó do plano SEM tools e sem
// origem de saída declarada fecha `failed` por `empty_output` sem ter pedido tool nenhuma, o
// `aos-orq` grava o facto no log do plano (`plan.node_attempt_started`, `reason=empty_output`),
// volta a submeter o MESMO pedido como um run novo e só a última tentativa decide o nó.
//
// QUEM AUTORIZA É O NÓ `aos` (AOS-510), com a prova própria desta classe. A condição daqui
// ([elegivelParaTentativaVazia]) é filtro: serve para não pedir o que o nó ia recusar.
//
// O QUE NÃO SE LÊ. A decisão usa o estado, a razão do veredicto e um contador, de vocabulário
// fechado, e a estrutura do plano. O texto final e o raciocínio do run falhado não entram.
//
// O INTERRUPTOR É PRÓPRIO: `AOS_ORQ_NOVA_TENTATIVA_VAZIA` (`off`, a omissão; `observe`; `on`),
// independente do `AOS_ORQ_NOVA_TENTATIVA`. OS TECTOS SÃO OS MESMOS: as tentativas das duas
// classes contam para o mesmo tecto por nó e para o mesmo tecto por plano — um plano não ganha
// tentativas a mais por ter as duas.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	plan "github.com/aos-ref/control-plane/orchestrator/plan"
	plannerevents "github.com/aos-ref/control-plane/orchestrator/plannerevents"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// modoDaTentativaVaziaDoAmbiente lê o interruptor desta classe. Vazio ⇒ `off`. Um valor
// desconhecido recusa o arranque. O `os.Getenv` é LITERAL de propósito: o gate da superfície de
// variáveis de ambiente enumera-as pelo nome escrito no código.
func modoDaTentativaVaziaDoAmbiente() (string, error) {
	return modoDaTentativaVazia(strings.TrimSpace(os.Getenv("AOS_ORQ_NOVA_TENTATIVA_VAZIA")))
}

// modoDaTentativaVazia valida um valor JÁ LIDO. O valor recusado não vai na mensagem.
func modoDaTentativaVazia(bruto string) (string, error) {
	switch bruto {
	case "", novaTentativaOff:
		return novaTentativaOff, nil
	case novaTentativaObserve:
		return novaTentativaObserve, nil
	case novaTentativaOn:
		return novaTentativaOn, nil
	default:
		return "", fmt.Errorf("%w: AOS_ORQ_NOVA_TENTATIVA_VAZIA aceita off (a omissao), observe e on", ErrNovaTentativa)
	}
}

// vaziaLigada diz se o `serve` começa tentativas desta classe.
func (c configDaNovaTentativa) vaziaLigada() bool { return c.modoVazia == novaTentativaOn }

// bannerDaTentativaVazia declara, no arranque do `serve`, o que este processo faz quando um nó
// sem tools responde vazio. Só se imprime fora de `off`.
func bannerDaTentativaVazia(c configDaNovaTentativa) string {
	switch {
	case c.modoVazia == novaTentativaObserve:
		return "nova tentativa por resposta vazia (AOS-511, ADR-039): EM OBSERVACAO — um no nao-verificador sem tools e sem origem de saida declarada cujo run feche failed por empty_output sem nenhuma tool call pedida e CONTADO e dito neste log, e NAO e tentado outra vez: os estados, os eventos do plano e o codigo de saida sao os de off"
	case c.tectoDoNo <= 0 || !c.vaziaAnunciada:
		return "nova tentativa por resposta vazia (AOS-511, ADR-039): LIGADA E NAO APLICADA — o no nao anuncia o suporte desta classe (GET /tools sem run_retry.empty_output: no anterior ao AOS-510, AOS_RUN_RETRY_EMPTY desligado, ou AOS_RUN_RETRY_MAX a zero). Nenhuma tentativa e pedida; um no que responda vazio fecha failed, como antes"
	default:
		return fmt.Sprintf("nova tentativa por resposta vazia (AOS-511, ADR-039): LIGADA — um no nao-verificador sem tools e sem origem de saida declarada cujo run feche failed por empty_output sem nenhuma tool call pedida volta a ser submetido como um run novo, ate %d tentativa(s) a mais por no (o no anuncia %d) e %d por plano — os MESMOS tectos da nova tentativa do AOS-503, partilhados. O facto de cada tentativa fica no log do plano antes do pedido; quem autoriza e o no, com a prova propria desta classe", c.porNo(), c.tectoDoNo, c.tectoDoPlano)
	}
}

// elegivelParaTentativaVazia diz se o run de um nó do plano admite, do lado do `aos-orq`, uma
// nova tentativa pela classe da resposta vazia. Lê SÓ vocabulário fechado e a estrutura do plano:
//
//   - o nó não é verificador, NÃO tem tools atribuídas (logo, não tem contrato de conclusão) e
//     não declara a origem de nenhuma saída (`from_tool`);
//   - o nó `aos` conhece o run, e a resposta é sobre ESTE run;
//   - o run está `failed`, com a razão exactamente `empty_output`;
//   - o vector do veredicto diz zero tool calls pedidas.
//
// O texto final e o raciocínio não entram: a função não os lê.
func elegivelParaTentativaVazia(n plan.Node, tools []string, runID string, st estadoDoRun, existe bool) bool {
	if !existe || n.IsVerifier() || len(tools) != 0 || n.DeclaresOutputSource() {
		return false
	}
	if st.RunID != runID || st.Status != "failed" || st.OutcomeReason != string(agentruntime.OutcomeEmptyOutput) {
		return false
	}
	return st.Verdict != nil && !st.Verdict.Fulfilled && st.Verdict.ToolCallsRequested == 0
}

// tentativaPorVazio diz se a tentativa corrente do nó — a que o log do plano regista — é da
// classe da resposta vazia. É pela razão do FACTO, e não pelo interruptor: um plano que ficou a
// meio de uma tentativa conta na classe certa depois de o interruptor mudar.
func (e *executorDeNos) tentativaPorVazio(nodeID string) bool {
	return e.tentativaDe(nodeID) > 1 && e.razoesDeTentativa[nodeID] == plannerevents.AttemptReasonEmptyOutput
}

// fixarRazaoDaTentativa regista a razão do facto da tentativa corrente do nó.
func (e *executorDeNos) fixarRazaoDaTentativa(nodeID string, razao plannerevents.AttemptReason) {
	if e.razoesDeTentativa == nil {
		e.razoesDeTentativa = map[string]plannerevents.AttemptReason{}
	}
	e.razoesDeTentativa[nodeID] = razao
}

// marcarEsgotadoPorVazio regista que o nó esgotou as tentativas nesta classe.
func (e *executorDeNos) marcarEsgotadoPorVazio(nodeID string) {
	if e.esgotadosPorVazio == nil {
		e.esgotadosPorVazio = map[string]bool{}
	}
	e.esgotadosPorVazio[nodeID] = true
}

// classeLigada diz se o `serve` começa (ou retoma) tentativas da classe da tentativa corrente do
// nó: o interruptor desta classe para um facto `empty_output`, o do AOS-503 para os outros.
func (e *executorDeNos) classeLigada(nodeID string) bool {
	if e.tentativaPorVazio(nodeID) {
		return e.nt.vaziaLigada()
	}
	return e.nt.ligada()
}

// novaTentativaVazia é a [executorDeNos.novaTentativa] da classe da resposta vazia. Devolve
// `tratado` true quando o run É desta classe (elegível, com o interruptor fora de `off`): quem
// chama não o passa à outra. Com `tratado` false nada foi contado, gravado nem escrito.
//
// A ORDEM É O CONTRATO, como no AOS-503: o facto fica no log do plano ANTES do pedido ao nó. OS
// TECTOS SÃO OS DO AOS-503, e os contadores são os mesmos: a tentativa corrente do nó e o número
// de factos do plano, de qualquer classe.
func (e *executorDeNos) novaTentativaVazia(ctx context.Context, nodeID string, st estadoDoRun, existe bool) (outra, tratado bool, err error) {
	if e.nt.modoVazia != novaTentativaObserve && e.nt.modoVazia != novaTentativaOn {
		return false, false, nil
	}
	if e.geracaoDoPedido <= 0 || !e.declararOrigem {
		// Sem o vínculo ao pedido, com o plano e o nó declarados, não há tentativa (ver
		// [executorDeNos.novaTentativa]). Não se conta nem se grava nada.
		return false, false, nil
	}
	n := e.nos[nodeID]
	actual := e.tentativaDe(nodeID)
	if !elegivelParaTentativaVazia(n, nomesDasTools(e.tools[nodeID]), e.runDoNo(nodeID), st, existe) {
		return false, false, nil
	}
	if actual == 1 {
		e.medicao.primeiraRespostaVazia()
	} else {
		// A tentativa `actual` voltou a responder vazio: conta-se aqui, uma vez.
		e.medicao.tentativaVaziaFeita(actual, tentativaVoltouAFalhar)
		e.tentativaContada[nodeID] = true
	}
	if e.nt.modoVazia == novaTentativaObserve {
		e.medicao.tentativaVaziaEmObservacao()
		fmt.Printf("  execucao: no %s NOVA TENTATIVA POR RESPOSTA VAZIA EM OBSERVACAO — tentaria outra vez (causa=%s, zero tool calls pedidas, no sem tools); nao tenta: o no fecha como em off\n",
			nodeID, agentruntime.OutcomeEmptyOutput)
		return false, true, nil
	}
	recusar := func(motivo string) (bool, bool, error) {
		e.recusasDeTentativa[nodeID] = motivo
		e.medicao.tentativaVaziaRecusada(motivo)
		fmt.Printf("  execucao: no %s NOVA TENTATIVA POR RESPOSTA VAZIA NAO FEITA tentativa_recusada=%s (tentativa corrente %d) — o no fecha failed com a causa do run\n", nodeID, motivo, actual)
		return false, true, nil
	}
	switch {
	case e.nt.tectoDoNo <= 0 || !e.nt.vaziaAnunciada:
		return recusar(recusaNaoAnunciado)
	case actual-1 >= e.nt.porNo():
		if e.nt.porNo() < maxTentativasAMaisPorNo {
			return recusar(recusaTectoDoNo)
		}
		e.esgotados[nodeID] = true
		e.marcarEsgotadoPorVazio(nodeID)
		fmt.Printf("  execucao: no %s tentativas_esgotadas tentativas=%d — o run voltou a fechar por %s; o no fecha failed\n",
			nodeID, actual, agentruntime.OutcomeEmptyOutput)
		return false, true, nil
	case e.factosDeTentativa >= e.nt.tectoDoPlano:
		return recusar(recusaTectoDoPlano)
	case !e.nt.prazo.IsZero() && !e.agora().Before(e.nt.prazo):
		return recusar(recusaPrazo)
	}
	proxima := actual + 1
	anterior := idDaTentativa(e.runID, nodeID, actual)
	// O FACTO, ANTES DO PEDIDO.
	if _, err := e.rec.RecordNodeAttemptStarted(ctx, plannerevents.NodeAttemptStartedPayload{
		NodeID: nodeID, Attempt: proxima, RetryOf: anterior,
		Reason: plannerevents.AttemptReasonEmptyOutput,
	}, n); err != nil {
		if errors.Is(err, plannerevents.ErrInvalidNodeAttempt) {
			fmt.Printf("  execucao: no %s o facto da tentativa %d nao tem forma admissivel (%v)\n", nodeID, proxima, err)
			return recusar(recusaFactoInvalido)
		}
		return false, true, fmt.Errorf("facto da tentativa %d de %q: %w", proxima, nodeID, err)
	}
	e.tentativas[nodeID] = proxima
	e.fixarRazaoDaTentativa(nodeID, plannerevents.AttemptReasonEmptyOutput)
	e.factosDeTentativa++
	e.factosDeTentativaVazia++
	delete(e.tentativaContada, nodeID)
	fmt.Printf("  execucao: no %s NOVA TENTATIVA %d POR RESPOSTA VAZIA — o run %s fechou failed por %s sem nenhuma tool call pedida; o facto esta no log do plano e o mesmo pedido volta a ser submetido como %s\n",
		nodeID, proxima, anterior, agentruntime.OutcomeEmptyOutput, e.runDoNo(nodeID))
	if err := e.submeterTentativa(ctx, nodeID, proxima); err != nil {
		motivo, recusada := motivoDaRecusaDaTentativa(err)
		if !recusada {
			return false, true, err
		}
		// O nó recusou. O facto fica; o nó do plano fecha com a causa do run ANTERIOR, e a
		// tentativa corrente, em memória, volta a ser a dele.
		e.tentativas[nodeID] = actual
		e.tentativaContada[nodeID] = true
		fmt.Printf("  execucao: no %s a submissao da tentativa %d foi RECUSADA pelo no aos\n", nodeID, proxima)
		return recusar(motivo)
	}
	return true, true, nil
}

// As medições desta classe (medicao_do_contrato.go guarda-as). Todas nil-safe.

func (m *medicaoDoContrato) primeiraRespostaVazia() {
	if m == nil {
		return
	}
	m.primeirasVazias++
}

func (m *medicaoDoContrato) tentativaVaziaFeita(tentativa int, desfecho string) {
	if m == nil {
		return
	}
	if m.tentativasVaziasFeitas == nil {
		m.tentativasVaziasFeitas = map[chaveDeTentativa]int{}
	}
	m.tentativasVaziasFeitas[chaveDeTentativa{tentativa: strconv.Itoa(tentativa), desfecho: desfecho}]++
}

func (m *medicaoDoContrato) tentativaVaziaRecusada(motivo string) {
	if m == nil {
		return
	}
	if m.tentativasVaziasRecusadas == nil {
		m.tentativasVaziasRecusadas = map[string]int{}
	}
	m.tentativasVaziasRecusadas[motivo]++
}

func (m *medicaoDoContrato) tentativaVaziaEmObservacao() {
	if m == nil {
		return
	}
	m.tentativasVaziasEmObservacao++
}

// registarTentativaVazia soma às séries DESTA classe o que um `serve` mediu. Só escreve rótulos do
// vocabulário fechado, e só séries com valor: com o interruptor em `off` e sem factos desta classe
// no log, o ficheiro de métricas é o de antes. Nenhuma série do AOS-503 é tocada aqui.
func (m *metricasDoConsumo) registarTentativaVazia(c *medicaoDoContrato) {
	if c == nil {
		return
	}
	if c.primeirasVazias > 0 {
		m.somar(metricaPrimeirasVazias, float64(c.primeirasVazias))
	}
	if c.tentativasVaziasEmObservacao > 0 {
		m.somar(metricaTentativasVaziaEmObservacao, float64(c.tentativasVaziasEmObservacao))
	}
	for _, t := range []string{"2", "3"} {
		for _, d := range desfechosDeTentativa {
			if n := c.tentativasVaziasFeitas[chaveDeTentativa{tentativa: t, desfecho: d}]; n > 0 {
				m.somar(serie(metricaTentativasVazia, "tentativa", t, "desfecho", d), float64(n))
			}
		}
	}
	for _, motivo := range recusasDeTentativa {
		if n := c.tentativasVaziasRecusadas[motivo]; n > 0 {
			m.somar(serie(metricaTentativasVaziaRecusadas, "causa", motivo), float64(n))
		}
	}
}
