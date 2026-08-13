package plandispatch

import (
	"context"
	"fmt"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// branches.go — A FASE DE DECISÃO DE RAMOS de uma passagem de despacho (ADR-022
// §2.1, AOS-270). Compõe três coisas que o resto do pacote mantém separadas: a
// avaliação PURA (condition.go), o registo APPEND-ONLY da decisão (o facto que o
// replay lê) e o DÉBITO de orçamento da árvore (ADR-008).
//
// A ordem em que essas três acontecem é a substância deste ficheiro, não um
// detalhe de implementação:
//
//  1. LER O REGISTO PRIMEIRO — E INCONDICIONALMENTE. Se a decisão de um nó já é um
//     facto, é ELA que vale: não se lê o resultado, não se avalia, não se debita,
//     não se apensa nada. É assim, e só assim, que «o replay reproduz o ramo SEM
//     re-avaliação» (ADR-022 §2.4(3)) é uma propriedade ESTRUTURAL: no caminho de
//     replay o avaliador nem sequer é alcançado.
//
//     «Incondicionalmente» quer dizer: INDEPENDENTEMENTE do estado do ciclo de
//     vida do nó. Um facto apenso é imutável; o estado do nó não é. Filtrar a
//     LEITURA por `NodePending` — como esta fase fazia — significava que, assim
//     que o ciclo de vida marcasse o nó podado como failed/cancelado, a decisão
//     registada deixava de semear [propagateNotTaken] e toda a descendência
//     regressava a `waiting_deps` silencioso. A disposição do MESMO plano sobre o
//     MESMO journal deixava de ser monótona entre passagens — exactamente a
//     podridão que a propagação existe para evitar.
//  2. AVALIAR SÓ O QUE FALTA, sobre resultados REGISTADOS, e SÓ para nós ainda
//     PENDENTES (um nó que já correu tem o ramo materializado pelo run).
//  3. DEBITAR E REGISTAR SÓ O QUE FICOU DECIDIDO, e nessa ordem: RESERVA →
//     REGISTA → CONFIRMA. Uma condição ainda indecisa (a origem não terminou) não
//     reserva nada e não regista nada — caso contrário cada re-invocação do
//     escalonador durante a espera drenaria o orçamento da árvore, e o custo de uma
//     decisão deixaria de ser um número: seria uma função de quantas vezes o
//     escalonador acordou.

// decideBranches decide o ramo de cada nó PENDENTE com arestas condicionais e
// devolve (decisões por node_id, nº de decisões NOVAS registadas, erro).
//
// Fail-closed em três direcções: um plano com condições sem as portas ligadas é
// recusado ([ErrConditionalUnsupported]); um digest divergente da decisão registada
// é recusado ([ErrBranchDigestMismatch]); uma falha do registo aborta a passagem
// ([ErrBranchJournal]). Uma falha de ORÇAMENTO não aborta — deixa o nó em espera
// (é pressão, não corrupção).
//
// Determinística: percorre `nodes` já em ordem canónica de node_id, lê cada
// resultado no máximo uma vez por passagem, e propaga a poda por travessia estável.
func (d *Dispatcher) decideBranches(ctx context.Context, p Plan, nodes []Node, states map[string]NodeState) (map[string]branchEval, int, error) {
	if !anyConditional(nodes) {
		// Caminho de AOS-238 intacto: sem condições no plano, esta fase não existe —
		// zero chamadas a portas, zero alocações, comportamento byte-a-byte o de antes.
		return nil, 0, nil
	}
	if !d.conditionalReady() {
		return nil, 0, ErrConditionalUnsupported
	}

	recorded, err := d.journal.Decisions(ctx, p.PlanID)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: ler decisões do plano %q: %v", ErrBranchJournal, p.PlanID, err)
	}

	decisions := make(map[string]branchEval, len(nodes))
	cache := newResultCache(ctx, d.results, p.PlanID, states)
	evaluated := 0

	for _, n := range nodes {
		if len(n.ConditionalOn) == 0 {
			continue
		}

		digest := plan.ConditionDigest(n.ConditionalOn)

		// (1) REPLAY — a decisão já é um facto, e um facto LÊ-SE SEMPRE (ver a nota
		// (1) no topo do ficheiro: a leitura NÃO é filtrada pelo estado do ciclo de
		// vida, senão a poda deixava de propagar assim que o nó podado mudasse de
		// estado). Note-se o que NÃO acontece a seguir: nenhuma leitura de resultado,
		// nenhuma avaliação, nenhum débito, nenhum evento novo. O ramo é LIDO.
		if rec, ok := recorded[n.NodeID]; ok {
			if rec.ConditionDigest != digest {
				return nil, 0, fmt.Errorf("%w: nó %q (registado %s, documento %s)", ErrBranchDigestMismatch, n.NodeID, rec.ConditionDigest, digest)
			}
			if rec.Taken {
				decisions[n.NodeID] = branchTaken
			} else {
				decisions[n.NodeID] = branchNotTaken
			}
			continue
		}

		// Sem facto registado: só um nó ainda PENDENTE se avalia. Um nó que já correu
		// (ou falhou) tem o ramo materializado pelo run e não se re-decide; a guarda
		// evita também que uma passagem tardia debite orçamento por uma condição cuja
		// resposta o run já deu. Esta guarda cobre APENAS os passos (2)-(4) — nunca a
		// leitura do registo acima.
		if states[n.NodeID] != NodePending {
			continue
		}

		// (2) AVALIAÇÃO — função pura do resultado registado (condition.go).
		ev := evalConditional(n.ConditionalOn, cache.lookup)
		if cache.err != nil {
			return nil, 0, fmt.Errorf("plandispatch: resultado registado de uma origem do nó %q: %w", n.NodeID, cache.err)
		}
		if ev == branchUndecided {
			// Nada decidido ⇒ nada debitado, nada registado. O nó espera.
			decisions[n.NodeID] = branchUndecided
			continue
		}

		// (3) RESERVA — avaliar custa (ADR-008), e o custo verifica-se ATOMICAMENTE em
		// toda a ancestralidade ANTES de a decisão se tornar facto. Sem headroom, a
		// decisão NÃO se toma: o nó fica em espera e a passagem seguinte re-tenta.
		// Fail-closed no sentido de não despachar, nunca no sentido de podar por falta
		// de dinheiro.
		if err := d.meter.ReserveConditionEval(ctx, p.PlanID, n.NodeID); err != nil {
			decisions[n.NodeID] = branchUndecided
			continue
		}

		// (4) REGISTO — o facto que fixa o ramo. Uma falha aqui aborta a passagem: sem
		// facto durável não há replay reproduzível, e despachar sobre uma decisão que
		// ninguém conseguiu registar seria exactamente o não-determinismo que ADR-010
		// proíbe. E LIBERTA a reserva: sem facto não há decisão, logo não há nada a
		// pagar. Confirmar antes de registar (como esta fase fazia) transformava uma
		// indisponibilidade do Event Store num DRENO: N re-invocações do escalonador
		// = N débitos pelo mesmo nó, que é o modo de falha que o invariante «débito
		// por DECISÃO, não por tentativa» existe para impedir.
		if err := d.journal.Record(ctx, p.PlanID, BranchDecision{
			NodeID:          n.NodeID,
			Taken:           ev == branchTaken,
			ConditionDigest: digest,
			Sources:         sourcesOf(n.ConditionalOn),
		}); err != nil {
			_ = d.meter.ReleaseConditionEval(ctx, p.PlanID, n.NodeID)
			return nil, 0, fmt.Errorf("%w: registar decisão do nó %q: %v", ErrBranchJournal, n.NodeID, err)
		}

		// (5) CONFIRMAÇÃO — o facto existe, logo a decisão existe, logo paga-se. Uma
		// falha a confirmar NÃO desfaz o facto (é append-only e a decisão é válida):
		// deixa a reserva por libertar do lado do orçamento, que é o lado seguro —
		// contabiliza a mais, nunca a menos, e nunca despacha sobre trabalho não pago.
		if err := d.meter.CommitConditionEval(ctx, p.PlanID, n.NodeID); err != nil {
			return nil, 0, fmt.Errorf("%w: confirmar débito da decisão do nó %q: %v", ErrBranchJournal, n.NodeID, err)
		}
		evaluated++
		decisions[n.NodeID] = ev
	}

	propagateNotTaken(nodes, decisions)
	return decisions, evaluated, nil
}

// propagateNotTaken propaga a PODA pela descendência: um nó cuja origem (por
// qualquer dos dois canais de aresta) não foi tomada nunca poderá correr — a sua
// dependência jamais concluirá.
//
// PORQUE ISTO EXISTE. Sem propagação, a descendência de um ramo podado ficaria
// eternamente em `waiting_deps`: bloqueada em silêncio, indistinguível de trabalho
// legítimo à espera. A disciplina deste pacote é a oposta — uma disposição
// impossível é SURFACED, não deixada a apodrecer (o mesmo raciocínio que faz
// [validatePlan] recusar um ciclo em vez de o deixar diferido para sempre).
//
// A poda derivada VENCE uma decisão própria de «tomado»: a condição do nó pode ter
// sido satisfeita e, ainda assim, a sua dependência estar morta. O facto registado
// do nó não é reescrito — continua a dizer a verdade sobre a CONDIÇÃO DELE; o que
// muda é a disposição desta passagem, que é derivada do grafo.
//
// Travessia iterativa (sem recursão) por ordem canónica, logo determinística.
func propagateNotTaken(nodes []Node, decisions map[string]branchEval) {
	if len(decisions) == 0 {
		return
	}
	// dependents[origem] = nós que dependem dela por QUALQUER canal de aresta.
	dependents := make(map[string][]string, len(nodes))
	for _, n := range nodes {
		for _, dep := range n.DependsOn {
			dependents[dep] = append(dependents[dep], n.NodeID)
		}
		for _, ce := range n.ConditionalOn {
			dependents[ce.From] = append(dependents[ce.From], n.NodeID)
		}
	}
	// Semente: os nós PODADOS por decisão própria, pela ordem canónica de `nodes`.
	queue := make([]string, 0, len(decisions))
	for _, n := range nodes {
		if decisions[n.NodeID] == branchNotTaken {
			queue = append(queue, n.NodeID)
		}
	}
	for i := 0; i < len(queue); i++ {
		for _, dep := range dependents[queue[i]] {
			if decisions[dep] == branchNotTaken {
				continue // já podado: não re-enfileira (a travessia termina)
			}
			decisions[dep] = branchNotTaken
			queue = append(queue, dep)
		}
	}
}

// anyConditional indica se algum nó do conjunto materializado declara arestas
// condicionais. É o interruptor que mantém o caminho sem-condições de AOS-238
// literalmente inalterado.
func anyConditional(nodes []Node) bool {
	for _, n := range nodes {
		if len(n.ConditionalOn) > 0 {
			return true
		}
	}
	return false
}

// resultCache lê cada resultado registado NO MÁXIMO UMA VEZ por passagem. Duas
// razões, ambas de correcção e não de desempenho: uma origem partilhada por vários
// ramos tem de dar a MESMA leitura a todos (vista coerente), e o número de chamadas
// à porta deixa de depender da topologia.
//
// Guarda de terminalidade: uma origem que a vista do ciclo de vida ainda dá como
// não-terminal NÃO é sequer lida — não há resultado registado antes do fim, e ler
// um meio-resultado seria a forma mais discreta de partir o determinismo.
type resultCache struct {
	ctx    context.Context
	view   ResultView
	planID string
	states map[string]NodeState
	recs   map[string]cachedResult
	err    error
}

type cachedResult struct {
	rec NodeResultRecord
	ok  bool
}

func newResultCache(ctx context.Context, view ResultView, planID string, states map[string]NodeState) *resultCache {
	return &resultCache{ctx: ctx, view: view, planID: planID, states: states, recs: make(map[string]cachedResult)}
}

// lookup devolve (resultado, existe). Um erro da porta é RETIDO em [resultCache.err]
// e devolvido como «não existe» para a avaliação em curso — o chamador confere o
// erro e aborta a passagem, pelo que nenhuma decisão é tomada sobre uma leitura
// falhada.
func (c *resultCache) lookup(nodeID string) (NodeResultRecord, bool) {
	if c.err != nil {
		return NodeResultRecord{}, false
	}
	if cached, ok := c.recs[nodeID]; ok {
		return cached.rec, cached.ok
	}
	if st, known := c.states[nodeID]; known && st != NodeComplete && st != NodeFailed {
		c.recs[nodeID] = cachedResult{}
		return NodeResultRecord{}, false
	}
	rec, ok, err := c.view.Result(c.ctx, c.planID, nodeID)
	if err != nil {
		c.err = err
		return NodeResultRecord{}, false
	}
	c.recs[nodeID] = cachedResult{rec: rec, ok: ok}
	return rec, ok
}
