package planvalidate

import (
	"github.com/aos-ref/control-plane/orchestrator"
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// verifier.go — A SEMÂNTICA DE SISTEMA DO PAPEL VERIFICADOR (ADR-022 §2.2, AOS-271).
//
// # O BURACO QUE ISTO FECHA
//
// AOS-270 entregou o observável `verdict` na gramática das condições: um plano JÁ
// PODIA ramificar sobre um veredicto. O que faltava era garantir que esse veredicto
// não tinha sido emitido por quem produziu o trabalho. Enquanto essa metade não
// existiu, a admissão recusou o observável inteiro (o interruptor datado
// `verdictSupported`, que este ficheiro substitui): fail-closed e loud, mas cego.
//
// Agora a admissão diz três coisas concretas sobre um ramo de qualidade:
//
//	(V1) só um nó com o papel RESERVADO [plan.RoleVerifier] emite veredicto;
//	(V2) o verificador não certifica trabalho da sua PRÓPRIA sub-árvore de delegação;
//	(V3) um verificador não pina tools de EFEITO — é read-only por construção;
//	(V4) NENHUM nó declara um verificador em `depends_on` — o verificador é SUMIDOURO
//	     do canal de execução, logo nunca encabeça uma sub-árvore de delegação;
//	(V5) um verificador só declara `outputs` de FORMA FECHADA — produz veredicto, não
//	     trabalho;
//	(V6) o verificador OBSERVA o trabalho que liberta — os outros produtores do
//	     consumidor são seus ANCESTRAIS.
//
// (V4)–(V6) entraram com a auditoria adversarial da wave. As três lacunas eram da
// mesma família e vale a pena nomeá-las, porque a lição é a que fica: (V2) estava
// ancorada no CONSUMIDOR — interrogava as OUTRAS arestas de entrada de quem lê o
// veredicto — e bastava o consumidor NÃO declarar o trabalho como aresta de entrada
// para o verificador certificar trabalho que ele próprio comissionou. Uma regra
// ancorada em quem OBSERVA a violação é uma regra que se contorna deixando de
// observar; (V4) ancora-a no VERIFICADOR, onde a propriedade vive. Do mesmo modo,
// nada impedia que o «verificador» PRODUZISSE o payload que o consumidor lê e
// assinasse o pass que o liberta ((V5)), nem que emitisse veredicto sobre trabalho
// que nunca viu ((V6)).
//
// # (V2) — «PRODUTOR ≠ VERIFICADOR», E PORQUE A DIRECÇÃO É ESTA
//
// A frase do ADR — «o validador rejeita plano em que o veredicto de um nó é emitido
// pelo próprio nó produtor ou por nó da sua sub-árvore de delegação» — admite, à
// letra, uma leitura que é IMPOSSÍVEL de satisfazer, e vale a pena dizer porquê em
// vez de a escolher em silêncio. Se «sub-árvore de W» fosse «os descendentes de W no
// grafo», então o organigrama canónico
//
//	W (produz) → V (verifica: depends_on W) → C (conditional_on V: verdict eq pass)
//
// seria SEMPRE rejeitado: um verificador tem obrigatoriamente de LER o trabalho, logo
// depende dele, logo é seu descendente. A regra recusaria todos os planos, incluindo
// os que o ADR desenha. Não é essa a leitura.
//
// A leitura que este ficheiro impõe é a que resta, e é a que tem conteúdo de
// segurança: o que a verificação exige é INDEPENDÊNCIA entre quem julga e quem
// trabalha, e no organigrama a dependência existe numa só direcção. O verificador
// pode estar A JUSANTE do trabalho (lê-o: é o que o torna um verificador); o que não
// pode é estar A MONTANTE dele — porque um nó de que o trabalho DESCENDE é o nó que
// encabeça a sub-árvore que o produziu. É a definição do próprio módulo
// (`planmaterialize.DefaultClassifier`: «um nó de que outros dependem encabeça uma
// sub-árvore de trabalho a jusante e materializa-se como sub-agente delegado»).
//
// Em concreto, para cada aresta condicional `C ← V` com um predicado sobre `verdict`:
// nenhum dos OUTROS produtores de C (as suas outras arestas de entrada, por qualquer
// dos dois canais) pode ser alcançável a partir de V. Se for, o veredicto de V está a
// certificar trabalho que V comissionou — auto-certificação com um passo de
// indirecção. E o caso degenerado (V é ele próprio produtor de C) é o mesmo teste,
// pela reflexividade de [orchestrator.DAG.Reachable].
//
// # (V3) — O CRITÉRIO DE «TOOL DE EFEITO», DECLARADO
//
// §2.2 diz «sem tools de efeito (escrita MEM, egress, spawn)» — uma enumeração de
// exemplos, não um critério. Inventar aqui uma lista de nomes de ferramentas seria
// uma allowlist mágica que envelhece mal e que qualquer tool nova contorna por não
// constar dela. O critério DERIVA-SE dos EIXOS DE RISCO PINADOS que o snapshot já
// carrega e que a regra 6 (AOS-232, risk.go) já consome — ver [IsEffectTool].
//
// # ORDEM E PUREZA
//
// Ambas as regras são puras e determinísticas (iteram slices, nunca mapas) e devolvem
// um [Verdict] com sub-código PRÓPRIO. (V1)/(V2) correm depois da aciclicidade
// (precisam de um grafo com descendência bem-definida); (V3) corre depois da
// resolução de tools (só se pergunta o efeito de uma tool que RESOLVE).

// IsEffectTool declara O CRITÉRIO de «tool de efeito» de ADR-022 §2.2 — a fronteira
// entre o que um verificador pode pinar e o que o torna, por construção, algo mais
// do que um leitor.
//
// UMA TOOL TEM EFEITO SE, PELOS EIXOS PINADOS DA SUA CAPABILITY:
//
//	(a) fala PARA FORA — `Egress != EgressNone`; ou
//	(b) NÃO é desfazível — `Reversibility.IsIrreversible()`.
//
// PORQUE ESTES DOIS, E PORQUE SÃO SUFICIENTES. São os mesmos eixos que a regra 6
// (AOS-232, [deriveNodeAction]) usa para derivar o risco SA-ROC de um nó: não há
// taxonomia nova, não há segunda fonte de verdade, e uma capability re-classificada
// no REG muda as duas decisões ao mesmo tempo. A enumeração do ADR mapeia
// exactamente: a ESCRITA em MEM e o SPAWN são efeitos que não se desfazem (eixo b);
// o EGRESS é o eixo (a). O que sobra — ler um registo local, computar sobre ele — é
// `EgressNone` + `Reversible`, e é precisamente o que um verificador precisa.
//
// FAIL-CLOSED PELO TIPO, sem uma linha para isso: os valores-zero de ambos os eixos
// são os perigosos ([risk.EgressUnknown] ≠ [risk.EgressNone];
// [risk.Reversibility.IsIrreversible] é verdadeiro para tudo o que não seja
// explicitamente [risk.Reversible]). Uma capability PINADA SEM eixos declarados conta
// como de efeito — uma ferramenta por classificar nunca é read-only por omissão.
//
// A SENSIBILIDADE não entra. É deliberado: ler dados sensíveis é uma leitura, e um
// verificador que não pudesse observar material sensível não conseguiria verificar o
// trabalho que interessa. O que a sensibilidade governa é o RISCO do nó (regra 6) e a
// fricção do gate — não a fronteira read-only.
//
// # O QUE ESTE CRITÉRIO PRESSUPÕE DO REG (declarado, não escondido)
//
// Não há eixo de MUTAÇÃO nos eixos pinados: os três que o snapshot carrega são
// sensibilidade, egress e reversibilidade. A ponte «escrita ⇒ efeito» apoia-se por
// isso numa INVARIANTE DE CLASSIFICAÇÃO do REG — *toda a capability que muta estado é
// classificada `Irreversible`* — e essa invariante é uma suposição sobre o catálogo,
// não algo que este código imponha. Uma escrita local com undo classificada
// `EgressNone` + `Reversible` seria contada como leitura, e um verificador podia
// pinar a tool que mexe no que ele revê.
//
// Acrescentar aqui um quarto eixo fail-closed (desconhecido ⇒ mutador) tornaria a
// invariante EXECUTÁVEL, e é a direcção certa — mas hoje não existe NENHUMA construção
// de [Capability] fora de testes (o snapshot pinado chega pelo wiring do REG, a jusante
// de AOS-238), pelo que o eixo novo classificaria só fixtures e daria a ilusão de uma
// garantia que ninguém alimenta. Fica registado como residual COM eixo
// (`docs/governance/REGISTO-Deferimentos.md`, DEF-275): o eixo de mutação entra junto
// com o construtor real do snapshot, e o teste que o acompanha é sobre o CATÁLOGO, não
// sobre um literal de teste. Até lá, a invariante está escrita onde é lida.
//
// Puro.
func IsEffectTool(c Capability) bool {
	return c.Egress != risk.EgressNone || c.Reversibility.IsIrreversible()
}

// EffectOracle devolve o predicado «esta [plan.ToolRef] PINADA tem efeito?» ancorado
// NESTE snapshot — a projecção de [IsEffectTool] para a superfície que o
// materializador consome (AOS-237: o clamp da NHI do verificador).
//
// LIGA-SE POR TIPO ESTRUTURAL, não por import: `planmaterialize` declara a sua porta
// como `func(plan.ToolRef) bool` e o composition root passa-lhe isto. É a mesma
// disciplina de `planbudget`↔`plandispatch` — o critério vive UMA vez, no pacote que
// detém o snapshot pinado, e os dois pontos de enforcement (validador e
// materializador) partilham-no sem se conhecerem.
//
// FAIL-CLOSED EM TODA A DIREITA DA RESOLUÇÃO: uma tool que não resolve no snapshot,
// que resolve com digest divergente, deprecada ou inadmissível conta como DE EFEITO.
// Não se concede autoridade read-only a uma referência que ninguém reconhece — e o
// caminho normal nem chega aqui, porque [checkTools] já rejeitou o plano.
//
// O índice é construído UMA vez, no fecho; o predicado devolvido é puro e seguro para
// uso concorrente (só lê o mapa).
func (s Snapshot) EffectOracle() func(plan.ToolRef) bool {
	idx := s.index()
	return func(t plan.ToolRef) bool {
		c, ok := idx[toolKey{name: t.Name, version: t.Version}]
		if !ok || c.Digest != t.Digest || c.Deprecated || !c.Admissible {
			return true
		}
		return IsEffectTool(c)
	}
}

// checkVerdictSource — REGRAS (V1) e (V2): quem pode emitir o veredicto que um ramo
// de qualidade consome. Pura e determinística: percorre os nós e as arestas pela
// ordem do slice, e interroga o DAG DE ADMISSÃO já construído (nunca uma travessia
// própria). Ver a nota de topo do ficheiro para a direcção de (V2) e o seu porquê.
func checkVerdictSource(doc plan.PlanDocument, dag *orchestrator.DAG) Verdict {
	if !anyVerdictBranch(doc) {
		// Sem ramos de qualidade não há veredicto a atribuir: caminho pré-AOS-271
		// literalmente inalterado (zero alocações).
		return accepted
	}
	byID := make(map[string]plan.Node, len(doc.Nodes))
	for _, n := range doc.Nodes {
		byID[n.NodeID] = n
	}
	for _, n := range doc.Nodes { // n é o CONSUMIDOR do veredicto
		for _, ce := range n.ConditionalOn {
			if !observesVerdict(ce.When) {
				continue
			}
			src, ok := byID[ce.From]
			if !ok {
				// Inalcançável: a regra 1 já garantiu a integridade referencial.
				// Defesa-em-profundidade — fail-closed em vez de continuar sem origem.
				return reject(plannerevents.RuleSchema, ReasonDanglingConditional, Locator{NodeID: n.NodeID})
			}

			// (V1) SÓ UM VERIFICADOR EMITE VEREDICTO. É esta linha que fecha o buraco
			// que AOS-270 deixou aberto: sem ela, `conditional_on: [{from: A, when:
			// [verdict eq pass]}]` com A = o PRÓPRIO produtor do trabalho era um plano
			// admissível — ramificação sobre auto-certificação. O Locator aponta ao
			// consumidor, que é quem declarou a aresta ofensora.
			if !src.IsVerifier() {
				return reject(plannerevents.RuleSchema, ReasonVerdictNotFromVerifier, Locator{NodeID: n.NodeID})
			}

			// (V1-bis) UM VEREDICTO É SOBRE ALGUMA COISA. Um «verificador» sem arestas
			// de entrada não observa nó nenhum do plano: o seu veredicto não tem
			// SUJEITO, e um pass/fail sobre nada não é um ramo de qualidade — é uma
			// constante com nome de veredicto. É também a forma mais barata de lavar
			// uma auto-certificação (declarar um verificador solto e fazer o ramo
			// depender dele), pelo que se recusa na admissão.
			if len(incomingEdges(src)) == 0 {
				return reject(plannerevents.RuleSchema, ReasonVerifierWithoutSubject, Locator{NodeID: src.NodeID})
			}

			// NOTA sobre o vector «o verificador PRODUZ o que o consumidor lê» (auditoria
			// da wave): não se trata AQUI, e não é esquecimento. Fechá-lo com «a origem
			// do veredicto não pode ser origem de um `consumes` do mesmo consumidor»
			// rejeitaria também o caminho LEGÍTIMO — o consumidor a ler as métricas do
			// veredicto que o liberta, que é exactamente a desclassificação que §2.2
			// sanciona. O vector morre um nível acima, em (V5): um verificador só declara
			// outputs de FORMA FECHADA, pelo que o `outputs: [{report, summary}]` do
			// ataque nunca é admitido. A regra fica onde a propriedade vive.

			// (V2) PRODUTOR ≠ VERIFICADOR e (V6) O VEREDICTO TEM SUJEITO. Para cada um
			// dos OUTROS produtores do consumidor:
			//
			//	(V2) não pode ser alcançável A PARTIR do verificador — seria trabalho da
			//	     sub-árvore que o verificador encabeça (auto-certificação com um passo
			//	     de indirecção). `Reachable` é reflexivo, pelo que o caso degenerado
			//	     (o verificador é ele próprio produtor do consumidor) cai no mesmo
			//	     teste;
			//	(V6) TEM de ser ancestral do verificador — se `publish` só corre quando
			//	     `review` diz pass, então `review` tem de ter OBSERVADO o que
			//	     `publish` consome. Sem isto, um «verificador» ligado a outro ramo do
			//	     grafo libertava trabalho que nunca viu, e o `subjects[]` do facto era
			//	     uma atribuição decorativa.
			//
			// As duas não podem ser verdadeiras ao mesmo tempo (o grafo é acíclico). A
			// ordem — (V2) primeiro — é deliberada: a auto-certificação é o defeito mais
			// grave e é o sub-código que o re-planeamento tem de ver.
			for _, p := range incomingEdges(n) {
				if p == ce.From {
					continue // a própria aresta do veredicto não é o trabalho verificado
				}
				if dag.Reachable(ce.From, p) {
					return reject(plannerevents.RuleSchema, ReasonVerifierSelfSubtree, Locator{NodeID: n.NodeID})
				}
				if !dag.Reachable(p, ce.From) {
					return reject(plannerevents.RuleSchema, ReasonVerifierNotObservingWork, Locator{NodeID: n.NodeID})
				}
			}
		}
	}
	return accepted
}

// checkVerifierRole — REGRAS (V4) e (V5): as duas propriedades de §2.2 que valem para
// TODO o nó verificador, olhe ou não alguém para o seu veredicto. Puras, sem grafo
// (iteram slices pela ordem declarada) e sem snapshot.
//
// # (V4) — O VERIFICADOR É SUMIDOURO DO CANAL `depends_on`
//
// §2.2 enumera os efeitos que a NHI do verificador não pode ter: «escrita MEM, egress,
// SPAWN». Os dois primeiros são tools, e (V3) trata-os. O terceiro NÃO É UMA TOOL: no
// organigrama, «spawn» é encabeçar uma sub-árvore de delegação, e quem decide isso é a
// TOPOLOGIA — um nó de que outros dependem materializa-se como sub-agente delegado
// (`planmaterialize.DefaultClassifier`). Um verificador com dependentes ganhava, por
// via da topologia, exactamente a autoridade que a enumeração do ADR lhe nega, e era o
// mesmo nó que depois assinava o veredicto sobre o trabalho que comissionou.
//
// A regra é por isso ESTRUTURAL e não sobre tools: nenhum nó pode declarar um
// verificador em `depends_on`. Quem consome o VEREDICTO usa `conditional_on` — que é o
// canal que §2.1 desenhou para isso e o único que faz sentido (esperar por um
// verificador sem observar o seu veredicto é esperar por nada). É também esta regra que
// fecha, na raiz, o contorno de (V2) que a auditoria demonstrou: sem dependentes, não
// há sub-árvore que o verificador possa certificar.
//
// # (V5) — O VERIFICADOR PRODUZ VEREDICTO, NÃO TRABALHO
//
// §2.2 dá ao verificador uma saída: «um objecto tipado (pass/fail + razões +
// métricas)». Um `outputs: [{name: report, type: summary}]` num nó `role: verifier` é
// TRABALHO — prosa de modelo — e um nó que produz trabalho é um produtor, por muito que
// o `role` diga o contrário. Recusá-lo é o que torna coerente a derivação de taint de
// [plan.Node.EffectiveOutputTaint]: se o verificador é o ponto de desclassificação
// sancionado, então TUDO o que ele publica tem de ser de forma fechada — senão o
// privilégio de desclassificar aplicava-se a um resumo.
func checkVerifierRole(doc plan.PlanDocument) Verdict {
	verifiers := make(map[string]struct{})
	for _, n := range doc.Nodes {
		if n.IsVerifier() {
			verifiers[n.NodeID] = struct{}{}
		}
	}
	if len(verifiers) == 0 {
		// Sem verificadores não há semântica de papel a impor: caminho pré-AOS-271
		// literalmente inalterado.
		return accepted
	}
	for _, n := range doc.Nodes {
		// (V4) — o Locator aponta ao nó que DECLAROU a dependência ofensora, que é
		// quem o re-planeamento tem de corrigir.
		for _, dep := range n.DependsOn {
			if _, isVerifier := verifiers[dep]; isVerifier {
				return reject(plannerevents.RuleSchema, ReasonVerifierCommissionsWork, Locator{NodeID: n.NodeID})
			}
		}
		// (V5) — aqui o Locator aponta ao próprio verificador: o contrato ofensor é dele.
		if !n.IsVerifier() {
			continue
		}
		for _, o := range n.Outputs {
			if !o.Type.ClosedForm() {
				return reject(plannerevents.RuleSchema, ReasonVerifierProducesWork, Locator{NodeID: n.NodeID})
			}
		}
	}
	return accepted
}

// checkVerifierAuthority — REGRA (V3): READ-ONLY POR CONSTRUÇÃO. Um nó com o papel
// reservado [plan.RoleVerifier] não pode pinar uma tool de EFEITO ([IsEffectTool]).
//
// PORQUE REJEITAR AQUI, E NÃO SÓ CLAMPAR NA MATERIALIZAÇÃO. O clamp da NHI
// (`planmaterialize`) é a garantia de que a autoridade EMITIDA nunca tem efeito — e é
// ele que cumpre a letra de §2.2. Mas se a admissão aceitasse o plano, o humano
// aprovaria no gate (ADR-013) um organigrama em que o verificador aparece com uma
// ferramenta de escrita, e o que corria seria outra coisa: o approval-card deixaria de
// descrever a execução. A mesma disciplina de [checkTools] — uma tool inadmissível
// REJEITA o plano inteiro, nunca sofre trimming silencioso — aplica-se ao papel.
//
// A regra corre para TODOS os nós verificadores, mesmo aqueles cujo veredicto ninguém
// consome: `role: verifier` é uma declaração de semântica de sistema, não uma opção
// que só vale quando alguém está a olhar.
//
// O Locator carrega as coordenadas ESTRUTURAIS da tool ofensora (nome/versão/digest —
// identificadores, nunca conteúdo), como em [checkTools]. Pura e determinística.
func checkVerifierAuthority(doc plan.PlanDocument, snap Snapshot) Verdict {
	idx := snap.index()
	for _, n := range doc.Nodes {
		if !n.IsVerifier() {
			continue
		}
		for _, t := range n.Tools {
			coord := ToolCoord{Name: t.Name, Version: t.Version, Digest: t.Digest}
			c, ok := idx[toolKey{name: t.Name, version: t.Version}]
			if !ok {
				// Inalcançável a jusante de [checkTools]; fail-closed (uma referência
				// não resolúvel conta como de efeito, como em [Snapshot.EffectOracle]).
				return reject(plannerevents.RuleToolResolution, ReasonVerifierEffectTool, Locator{NodeID: n.NodeID, Tool: coord})
			}
			if IsEffectTool(c) {
				return reject(plannerevents.RuleToolResolution, ReasonVerifierEffectTool, Locator{NodeID: n.NodeID, Tool: coord})
			}
		}
	}
	return accepted
}

// anyVerdictBranch indica se algum nó do plano ramifica sobre o observável `verdict`.
// É o interruptor que mantém o caminho sem-ramos-de-qualidade inalterado.
func anyVerdictBranch(doc plan.PlanDocument) bool {
	for _, n := range doc.Nodes {
		for _, ce := range n.ConditionalOn {
			if observesVerdict(ce.When) {
				return true
			}
		}
	}
	return false
}

// observesVerdict indica se a conjunção contém algum predicado sobre o observável
// `verdict` — o ÚNICO que ADR-022 §2.2 amarra ao papel verificador (`terminal_state`
// e `metric` são propriedades de QUALQUER nó e não carecem de emissor privilegiado).
func observesVerdict(preds []plan.Predicate) bool {
	for _, p := range preds {
		if p.Subject == plan.SubjectVerdict {
			return true
		}
	}
	return false
}
