package planvalidate

import (
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// payload.go — A VALIDAÇÃO ESTÁTICA DOS CONTRATOS TIPADOS DE ARESTA (ADR-022 §2.3,
// AOS-272).
//
// O ADR nomeia TRÊS rejeições e uma fronteira. As rejeições: consumir um output
// INEXISTENTE, de TIPO INCOMPATÍVEL, ou com TAINT INCOMPATÍVEL com a autoridade do
// consumidor («ADR-005 — *untrusted* continua a não autorizar elevação»). A fronteira:
// o transporte é por REFERÊNCIA com proveniência, nunca um blackboard — e essa metade
// vive no facto `plan.payload_published` (emissão) e no `plandispatch.PayloadResolver`
// (consumo por contrato), não aqui.
//
// Este ficheiro tem QUATRO regras porque a integridade referencial se parte em duas, e
// as duas metades merecem sub-códigos próprios (o feedback ao re-planeamento tem de
// dizer o que corrigir, não «rejeitado»):
//
//	(P1) a origem do `consumes` é uma ARESTA DE ENTRADA declarada do consumidor;
//	(P2) essa origem DECLARA um output com o nome pedido;
//	(P3) o tipo declarado pelo consumidor é IGUAL ao declarado pelo produtor;
//	(P4) um payload de taint EFECTIVO `untrusted` não alimenta um consumidor com
//	     AUTORIDADE PRIVILEGIADA.
//
// # (P1) — PORQUE A ARESTA DE DADOS TEM DE MONTAR NUMA ARESTA DE EXECUÇÃO
//
// Sem esta regra, `consumes` seria um segundo canal de aresta invisível ao DAG de
// admissão: um nó leria o trabalho de outro sem esperar por ele (corrida com o
// produtor, resultado dependente do escalonador — adeus determinismo de ADR-010) e o
// grafo de dados poderia fechar ciclos que a aciclicidade de AOS-025 nunca veria.
// Exigindo que `from` já seja aresta de entrada, o grafo de payload passa a ser um
// SUB-GRAFO do DAG de admissão: acíclico por construção, sem travessia nova e sem um
// detector que se possa esquecer de correr. É o mesmo movimento de AOS-270 (as arestas
// guardadas por condição entram no MESMO DAG) — reutilizar o primitivo em vez de
// duplicar a garantia.
//
// # (P4) — «AUTORIDADE DO CONSUMIDOR», E DE ONDE SE DERIVA
//
// O ADR remete para ADR-005, cuja invariante P0 é imposta em runtime pelo `TaintGate`
// do RM: conteúdo *untrusted* não satisfaz uma capability PRIVILEGIADA. O RM classifica
// «privilegiado» com um [PrivilegedAuthorizer] — uma allowlist de NOMES de capability
// fornecida pelo operador. O validador de planos não tem esse conjunto (nem devia: é
// política de ápice, não do control-plane) — mas tem o que os nomes representam: os
// EIXOS DE RISCO PINADOS do snapshot.
//
// O critério deriva-se daí e é EXACTAMENTE o de [IsEffectTool] (AOS-271): uma
// capability que fala para fora (`egress ≠ none`) ou que não se desfaz
// (`reversibility = irreversible`) é uma capability cuja autorização ADR-005 exige que
// venha de dados trusted. Uma só definição, agora com DUAS perguntas: «um verificador
// pode pinar isto?» (§2.2) e «este consumidor detém autoridade privilegiada?» (§2.3).
// Inventar aqui uma segunda taxonomia — uma lista de nomes, um eixo novo — daria duas
// respostas que envelheceriam em direcções diferentes.
//
// Consequência prática, e é a desejada: um nó que pina uma tool de egress NÃO pode
// consumir um resumo produzido por outro nó. Não é um bug — é a barreira P0 aplicada
// ANTES de queimar um token, em vez de no RM depois do spawn. O organigrama exprime o
// caminho legítimo declarando um verificador (§2.2) ou um nó `danger` com
// approval-card (ADR-013) entre o material untrusted e a acção privilegiada; o que
// deixa de existir é o caminho SILENCIOSO.
//
// # O QUE (P4) PASSOU A LER, E PORQUÊ (correcção da auditoria da wave)
//
// (P4) lia o taint do OUTPUT isolado, derivado só do `type`. Duas evasões, ambas
// demonstradas com planos ADMITIDOS: declarar `type: metrics` num output de conteúdo
// (o `type` é palavra do documento untrusted), e LAVAR o taint por um nó intermédio
// sem tools — que, não sendo privilegiado, consumia untrusted à vontade e re-publicava
// como `metrics`, entregando ao nó com egress material derivado integralmente de
// conteúdo untrusted com rótulo trusted.
//
// A regra passou a interrogar o PRODUTOR ([plan.Node.EffectiveOutputTaint]): só as
// formas fechadas de um nó `role: verifier` são trusted. As duas evasões morrem pela
// mesma linha, e a propagação transitiva pelo reticulado fica SUBSUMIDA — um
// não-verificador não pode publicar trusted, logo não há salto que lave nada. Ver a
// nota de [plan.Node.EffectiveOutputTaint] para o argumento completo.
//
// # PUREZA E ORDEM
//
// Todas as regras são puras e determinísticas (iteram slices, nunca mapas). Correm
// DEPOIS de [checkTools], de propósito: (P4) pergunta «que autoridade tem este nó?», e
// só faz sentido perguntá-lo de tools que RESOLVEM contra o snapshot pinado — senão o
// veredicto acusaria o contrato por uma referência que era, afinal, apenas lixo.

// checkPayloadContracts — REGRA 1-quater: os contratos tipados de ADR-022 §2.3. Ver a
// nota de topo do ficheiro para (P1)–(P4) e o seu porquê.
//
// Todas as rejeições usam [plannerevents.RuleSchema] com sub-código PRÓPRIO: o defeito
// é de CONTRATO (o documento pede algo que o grafo não sustenta), e é o sub-código —
// não a regra — que carrega a atribuição, como em AOS-271. O [Locator] aponta ao
// CONSUMIDOR: é quem declarou a aresta ofensora e quem o re-planeamento tem de corrigir.
func checkPayloadContracts(doc plan.PlanDocument, snap Snapshot) Verdict {
	if !anyPayloadContract(doc) {
		// Sem contratos declarados não há nada a resolver: caminho pré-AOS-272
		// literalmente inalterado (zero alocações).
		return accepted
	}
	byID := make(map[string]plan.Node, len(doc.Nodes))
	for _, n := range doc.Nodes {
		byID[n.NodeID] = n
	}
	idx := snap.index()
	for _, n := range doc.Nodes { // n é o CONSUMIDOR
		if len(n.Consumes) == 0 {
			continue
		}
		// A autoridade do consumidor é a MESMA para todas as suas arestas de dados:
		// deriva-se uma vez por nó, não uma vez por contrato.
		privileged := privilegedAuthority(n, idx)
		edges := incomingEdges(n) // união dos dois canais, ordem declarada
		for _, c := range n.Consumes {
			// (P1) A origem tem de ser uma ARESTA DE ENTRADA declarada.
			if !containsEdge(edges, c.From) {
				return reject(plannerevents.RuleSchema, ReasonConsumesUnknownEdge, Locator{NodeID: n.NodeID})
			}
			src, ok := byID[c.From]
			if !ok {
				// Inalcançável: a regra 1 já garantiu a integridade referencial dos dois
				// canais de aresta, e (P1) acabou de exigir que `from` fosse um deles.
				// Defesa-em-profundidade — fail-closed em vez de continuar sem origem.
				return reject(plannerevents.RuleSchema, ReasonConsumesUnknownEdge, Locator{NodeID: n.NodeID})
			}
			// (P2) A origem tem de DECLARAR o output pedido. É a rejeição literal do
			// ADR («consome um output inexistente»): sem ela, o contrato era uma
			// esperança sobre o que o produtor viesse a fazer.
			out, ok := src.FindOutput(c.Output)
			if !ok {
				return reject(plannerevents.RuleSchema, ReasonConsumesUnknownOutput, Locator{NodeID: n.NodeID})
			}
			// (P3) Os tipos têm de ser IGUAIS. Sem subtipagem nem coerção — ver
			// [plan.PayloadType].
			if out.Type != c.Type {
				return reject(plannerevents.RuleSchema, ReasonConsumesTypeMismatch, Locator{NodeID: n.NodeID})
			}
			// (P4) TAINT vs AUTORIDADE. O rótulo é o EFECTIVO do PRODUTOR
			// ([plan.Node.EffectiveOutputTaint]) — forma fechada E papel verificador,
			// elevado (nunca baixado) pelo advisory —, pelo que nem declarar
			// `taint: trusted` num resumo nem declarar `type: metrics` num nó qualquer
			// abre caminho nenhum. Interrogar `src` e não o `Output` isolado é a
			// correcção da wave: o rótulo que vale depende de QUEM produz.
			if privileged && src.EffectiveOutputTaint(out) == plan.TaintUntrusted {
				return reject(plannerevents.RuleSchema, ReasonConsumesTaintAuthority, Locator{NodeID: n.NodeID})
			}
		}
	}
	return accepted
}

// privilegedAuthority indica se a autoridade do nó — as capabilities que a
// materialização vai emitir na NHI a partir das suas tools PINADAS — inclui alguma
// PRIVILEGIADA no sentido de ADR-005. O critério é [IsEffectTool]; ver a nota de topo.
//
// FAIL-CLOSED EM TODA A DIREITA DA RESOLUÇÃO, como [Snapshot.EffectOracle]: uma tool
// que não resolve, que resolve com digest divergente, deprecada ou inadmissível conta
// como privilegiada. Não se declara um consumidor inofensivo com base numa referência
// que ninguém reconhece — e o caminho normal nem chega aqui, porque [checkTools] já
// rejeitou o plano.
//
// Um nó SEM tools não tem autoridade nenhuma: não é privilegiado, e pode consumir
// material untrusted à vontade (é o caso do nó que resume, agrega ou redige). Puro.
func privilegedAuthority(n plan.Node, idx map[toolKey]Capability) bool {
	for _, t := range n.Tools {
		c, ok := idx[toolKey{name: t.Name, version: t.Version}]
		if !ok || c.Digest != t.Digest || c.Deprecated || !c.Admissible {
			return true
		}
		if IsEffectTool(c) {
			return true
		}
	}
	return false
}

// containsEdge indica se id consta da lista de arestas de entrada. Linear e sem
// alocação: as listas são curtas por tecto de aridade, e uma varredura linear evita um
// mapa cuja ordem de iteração nunca é a do documento.
func containsEdge(edges []string, id string) bool {
	for _, e := range edges {
		if e == id {
			return true
		}
	}
	return false
}

// anyPayloadContract indica se algum nó do plano declara arestas de dados. É o
// interruptor que mantém o caminho sem-contratos inalterado.
func anyPayloadContract(doc plan.PlanDocument) bool {
	for _, n := range doc.Nodes {
		if len(n.Consumes) > 0 {
			return true
		}
	}
	return false
}
