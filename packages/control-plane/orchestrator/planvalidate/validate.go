package planvalidate

import (
	"github.com/aos-ref/control-plane/orchestrator"
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// Ceilings são os tectos estruturais PRÓPRIOS do plano (regra 4, §3.3): a forma do
// organigrama que o intake admite. São DISTINTOS do tecto de concorrência em
// run-time de AOS-028 (quantos nós correm ao mesmo tempo) — aqui limita-se a
// TOPOLOGIA declarada, não a execução. São política, passada como argumento para
// manter o validador puro e determinístico.
//
// Um tecto <= 0 significa "sem limite" nessa dimensão (dimensão desligada), não
// "rejeita tudo": a ausência de política não é uma violação.
type Ceilings struct {
	// MaxNodes limita o número total de nós do plano.
	MaxNodes int
	// MaxDepth limita o comprimento (em nós) da cadeia de dependências mais longa.
	MaxDepth int
	// MaxFanout limita o out-degree: quantos nós podem depender directamente de um
	// mesmo nó.
	MaxFanout int
}

// Validate é o VALIDADOR PURO de AOS-231. Aplica as regras 1–4 (mais as duas
// regras de ADMISSÃO das arestas condicionais, em condition.go) por ORDEM FIXA
// sobre o documento (assumido já aprovado na FORMA por [plan.Decode]), o snapshot
// pinado e os tectos, e devolve o PRIMEIRO veredicto de rejeição — ou [accepted].
//
// É determinística e sem I/O: itera sempre os nós e as `depends_on`/`tools` pela
// ordem do slice (nunca a ordem de um mapa), pelo que o mesmo input dá sempre o
// mesmo [Verdict]. NÃO muta o documento: em particular, uma tool inadmissível
// causa REJEIÇÃO — nunca trimming silencioso do nó ofensor.
func Validate(doc plan.PlanDocument, snap Snapshot, ceil Ceilings) Verdict {
	if v := checkSemantics(doc, snap); v.Rejected() {
		return v
	}
	if v := checkVerdictSupport(doc); v.Rejected() {
		return v
	}
	if v := checkAcyclic(doc); v.Rejected() {
		return v
	}
	// ALCANÇABILIDADE dos ramos (condition.go). Depois da aciclicidade — precisa da
	// ordem topológica — e ANTES da resolução de tools, para que um plano
	// estruturalmente morto seja recusado pela sua razão real e não por uma tool.
	if v := checkBranchReachability(doc); v.Rejected() {
		return v
	}
	if v := checkTools(doc, snap); v.Rejected() {
		return v
	}
	return checkCeilings(doc, ceil)
}

// checkSemantics — REGRA 1: invariantes SEMÂNTICAS que a forma ([plan.Decode]) não
// cobre. Puro. Ordem: compatibilidade de MAJOR, binding do snapshot, integridade
// referencial das arestas.
func checkSemantics(doc plan.PlanDocument, snap Snapshot) Verdict {
	// Compatibilidade de MAJOR (a fronteira que [plan.Decode] deixa explicitamente
	// para AOS-231): um plano de MAJOR diferente do corrente volta a planeamento.
	if !plan.CurrentPlanVersion.Compatible(doc.PlanVersion) {
		return reject(plannerevents.RuleSchema, ReasonVersionIncompatible, Locator{})
	}
	// Binding do snapshot pinado: o plano tem de ter sido validado CONTRA este
	// snapshot. Não computamos o hash (AOS-243) — só conferimos a igualdade do
	// carimbo que o plano trouxe com o Hash do snapshot recebido. Fail-closed.
	if doc.PlannerMeta.CapabilitiesHash != snap.Hash {
		return reject(plannerevents.RuleSchema, ReasonCapabilitiesHashMismatch, Locator{})
	}
	// Grammar do node_id: [plan.Decode] só garante não-vazio+único; esta invariante
	// SEMÂNTICA garante que cada node_id — o ÚNICO campo do documento propagado para
	// o [Locator]/feedback — é um identificador ESTRUTURAL limitado (charset fechado,
	// comprimento máximo), nunca texto livre arbitrário (blobs, novas linhas, frases
	// com PII). Corre ANTES da integridade referencial (que usa os ids) e rejeita com
	// um Locator VAZIO: um id malformado é untrusted e NÃO é ecoado no veredicto.
	for _, n := range doc.Nodes {
		if !validNodeID(n.NodeID) {
			return reject(plannerevents.RuleSchema, ReasonInvalidNodeID, Locator{})
		}
	}
	// Integridade referencial: cada depends_on tem de apontar para um node_id
	// existente. Uma aresta pendente é lixo semântico (e impediria construir o DAG).
	ids := make(map[string]struct{}, len(doc.Nodes))
	for _, n := range doc.Nodes {
		ids[n.NodeID] = struct{}{}
	}
	for _, n := range doc.Nodes {
		for _, dep := range n.DependsOn {
			if _, ok := ids[dep]; !ok {
				return reject(plannerevents.RuleSchema, ReasonDanglingDependency, Locator{NodeID: n.NodeID})
			}
		}
	}
	// Integridade referencial das ARESTAS CONDICIONAIS (ADR-022 §2.1, AOS-270). A
	// forma já foi validada por [plan.Decode] (gramática do subconjunto fechado); o
	// que falta é SEMÂNTICA de grafo, e é aqui que vive.
	//
	// (a) A origem tem de existir NO MESMO PLANO. É esta regra que dá corpo ao «o
	// ramo aponta para nós DECLARADOS À PRIORI do mesmo plano» do ADR: uma condição
	// sobre um nó de fora do documento não é uma aresta — é um oráculo externo.
	//
	// (b) A MESMA origem não pode aparecer em `depends_on` E em `conditional_on` do
	// mesmo nó. As duas têm semânticas de espera DIFERENTES (a dependência exige
	// CONCLUSÃO; a condicional exige TERMINALIDADE + predicado), pelo que a
	// sobreposição é ou contraditória (`depends_on X` + `X terminal_state eq failed`
	// ⇒ nó morto) ou redundante — e é exactamente a forma que um plano hostil usaria
	// para esconder a semântica real de uma aresta ao revisor humano do gate. Como
	// efeito colateral útil, a rejeição garante que os dois canais são DISJUNTOS, o
	// que permite contar o fanout/profundidade sobre a sua UNIÃO sem duplicados.
	for _, n := range doc.Nodes {
		deps := make(map[string]struct{}, len(n.DependsOn))
		for _, dep := range n.DependsOn {
			deps[dep] = struct{}{}
		}
		for _, ce := range n.ConditionalOn {
			if _, ok := ids[ce.From]; !ok {
				return reject(plannerevents.RuleSchema, ReasonDanglingConditional, Locator{NodeID: n.NodeID})
			}
			if _, dup := deps[ce.From]; dup {
				return reject(plannerevents.RuleSchema, ReasonConditionalShadowsDependency, Locator{NodeID: n.NodeID})
			}
		}
	}
	return accepted
}

// maxNodeIDLen é o tecto de comprimento (bytes) de um node_id. Um valor generoso:
// cobre identificadores realistas mas rejeita blobs de texto livre que um planeador
// comprometido pudesse embeber num id para vazar via feedback.
const maxNodeIDLen = 128

// validNodeID confere que id é um IDENTIFICADOR ESTRUTURAL limitado: 1..maxNodeIDLen
// bytes de um charset ASCII FECHADO (letras, dígitos e os separadores `_ - . :`).
// É a invariante que mantém o node_id — o único campo do documento propagado para o
// feedback allowlisted — livre de texto arbitrário (espaços, novas linhas, pontuação
// de prosa) do documento untrusted. Puro, sem alocação.
func validNodeID(id string) bool {
	if len(id) == 0 || len(id) > maxNodeIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == '-' || c == '.' || c == ':':
		default:
			return false
		}
	}
	return true
}

// checkAcyclic — REGRA 2: aciclicidade. REUTILIZA o DAG de AOS-025 (o primitivo do
// pacote raiz `orchestrator`) — a aciclicidade é imposta INCREMENTALMENTE na
// admissão de cada aresta, exactamente o mecanismo fail-closed de AOS-025. Não se
// reimplementa nenhuma detecção de ciclos aqui.
//
// Direcção das arestas: em `depends_on`, cada dep PRECEDE o nó; logo a aresta é
// dep→node (o mesmo sentido de [orchestrator.DAG.AddEdge], "To depende de From").
//
// ARESTAS CONDICIONAIS (ADR-022 §2.1, AOS-270) — «uma aresta condicional NUNCA
// pode fechar ciclo». O enforcement é ESTRUTURAL e NÃO tem código próprio: as
// arestas condicionais entram NO MESMO DAG, com a MESMA direcção (from→node), pelo
// que a aciclicidade incremental de AOS-025 as recusa pelo mesmo primitivo. É esta
// unificação — um só grafo de admissão, dois canais de aresta — que fecha o vector
// «ciclo disfarçado de condicional»: uma condicional que aponte para a região JÁ
// EXECUTADA do plano teria de apontar para um ANTECESSOR, e apontar para um
// antecessor É fechar um ciclo. Não há terceira hipótese, e não há detector novo
// que se possa esquecer de correr.
//
// Ordem deliberada em DUAS passagens (não é um detalhe): primeiro TODAS as
// `depends_on`, depois TODAS as condicionais. Assim, um ciclo que já exista só nas
// dependências é reportado como [ReasonCycle], e só um ciclo que EXIJA a aresta
// condicional para se fechar produz [ReasonConditionalCycle] — o feedback nomeia o
// canal culpado, e o teste adversarial pode prová-lo em vez de o presumir.
func checkAcyclic(doc plan.PlanDocument) Verdict {
	dag := orchestrator.NewDAG("planvalidate")
	for _, n := range doc.Nodes {
		if err := dag.AddNode(orchestrator.NodeSpec{TaskID: n.NodeID}); err != nil {
			// Defesa-em-profundidade: node_id vazio/duplicado já é recusado por
			// [plan.Decode]; se ainda assim falhar, fail-closed em vez de continuar.
			return reject(plannerevents.RuleSchema, ReasonMalformedNode, Locator{NodeID: n.NodeID})
		}
	}
	for _, n := range doc.Nodes {
		for _, dep := range n.DependsOn {
			if err := dag.AddEdge(dep, n.NodeID); err != nil {
				// A única razão possível aqui é fecho de ciclo (auto-laço incluído):
				// os nós existem (regra 1 garantiu a integridade referencial).
				return reject(plannerevents.RuleAcyclicity, ReasonCycle, Locator{NodeID: n.NodeID})
			}
		}
	}
	for _, n := range doc.Nodes {
		for _, ce := range n.ConditionalOn {
			if err := dag.AddEdge(ce.From, n.NodeID); err != nil {
				return reject(plannerevents.RuleAcyclicity, ReasonConditionalCycle, Locator{NodeID: n.NodeID})
			}
		}
	}
	return accepted
}

// checkTools — REGRA 3: resolução de cada [plan.ToolRef] contra o SNAPSHOT pinado.
// Casa nome+versão+digest e confere admissibilidade. Uma tool inexistente,
// deprecada, com digest divergente ou inadmissível REJEITA o plano INTEIRO — o nó
// ofensor continua a causar a rejeição (NUNCA trimming silencioso). Puro e
// determinístico (itera nós e tools pela ordem do slice).
func checkTools(doc plan.PlanDocument, snap Snapshot) Verdict {
	idx := snap.index()
	for _, n := range doc.Nodes {
		for _, t := range n.Tools {
			coord := ToolCoord{Name: t.Name, Version: t.Version, Digest: t.Digest}
			loc := Locator{NodeID: n.NodeID, Tool: coord}
			resolved, ok := idx[toolKey{name: t.Name, version: t.Version}]
			if !ok {
				return reject(plannerevents.RuleToolResolution, ReasonToolUnknown, loc)
			}
			if resolved.Digest != t.Digest {
				return reject(plannerevents.RuleToolResolution, ReasonToolDigestMismatch, loc)
			}
			if resolved.Deprecated {
				return reject(plannerevents.RuleToolResolution, ReasonToolDeprecated, loc)
			}
			if !resolved.Admissible {
				return reject(plannerevents.RuleToolResolution, ReasonToolInadmissible, loc)
			}
		}
	}
	return accepted
}

// checkCeilings — REGRA 4: tectos estruturais próprios do plano. Só corre depois de
// a aciclicidade estar garantida (regra 2), pelo que o cálculo de profundidade
// termina. Ordem determinística: nós, depois fanout, depois profundidade. Um tecto
// <= 0 está desligado.
func checkCeilings(doc plan.PlanDocument, ceil Ceilings) Verdict {
	if ceil.MaxNodes > 0 && len(doc.Nodes) > ceil.MaxNodes {
		return reject(plannerevents.RuleStructuralCeiling, ReasonMaxNodesExceeded, Locator{})
	}
	if ceil.MaxFanout > 0 {
		if node, deg := maxFanout(doc); deg > ceil.MaxFanout {
			return reject(plannerevents.RuleStructuralCeiling, ReasonMaxFanoutExceeded, Locator{NodeID: node})
		}
	}
	if ceil.MaxDepth > 0 {
		if node, depth := maxDepth(doc); depth > ceil.MaxDepth {
			return reject(plannerevents.RuleStructuralCeiling, ReasonMaxDepthExceeded, Locator{NodeID: node})
		}
	}
	return accepted
}

// incomingEdges devolve a UNIÃO das arestas de entrada de um nó — `depends_on`
// mais as origens das arestas CONDICIONAIS — pela ordem declarada (dependências
// primeiro), que é a ordem determinística usada por todo este ficheiro.
//
// PORQUE A UNIÃO, e não só `depends_on`: os tectos estruturais (regra 4) existem
// para limitar a TOPOLOGIA admitida. Se as condicionais não contassem, um plano
// exaustivo escapava ao MaxFanout/MaxDepth apenas por declarar as suas arestas no
// outro canal — o mesmo grafo, o mesmo custo, tecto nenhum. Contam. A regra 1
// garantiu que os dois canais são DISJUNTOS por nó ([ReasonConditionalShadowsDependency]),
// pelo que a união nunca conta a mesma aresta duas vezes. Puro, sem alocação quando
// não há condicionais (o caso de todos os planos pré-ADR-022).
func incomingEdges(n plan.Node) []string {
	if len(n.ConditionalOn) == 0 {
		return n.DependsOn
	}
	out := make([]string, 0, len(n.DependsOn)+len(n.ConditionalOn))
	out = append(out, n.DependsOn...)
	for _, ce := range n.ConditionalOn {
		out = append(out, ce.From)
	}
	return out
}

// maxFanout devolve o nó com maior out-degree (quantos nós dependem dele
// DIRECTAMENTE, por qualquer dos dois canais de aresta) e esse grau. Desempate
// estável pela ordem dos nós no slice (o PRIMEIRO a atingir o máximo vence). Puro.
func maxFanout(doc plan.PlanDocument) (string, int) {
	outdeg := make(map[string]int, len(doc.Nodes))
	for _, n := range doc.Nodes {
		for _, dep := range incomingEdges(n) {
			outdeg[dep]++ // dep→n: n conta para o fanout de dep
		}
	}
	best, bestDeg := "", 0
	for _, n := range doc.Nodes { // ordem do slice ⇒ desempate determinístico
		if d := outdeg[n.NodeID]; d > bestDeg {
			best, bestDeg = n.NodeID, d
		}
	}
	return best, bestDeg
}

// maxDepth devolve o nó final da cadeia de dependências mais longa e o seu
// comprimento em NÓS (um nó isolado tem profundidade 1). Assume aciclicidade
// (regra 2 já passou). É ITERATIVO (ordenação topológica de Kahn, sem recursão
// nativa), pelo que o consumo de pilha é O(1) e não estoura em cadeias muito fundas
// — mesmo que o operador ligue MaxDepth mas deixe MaxNodes desligado. As
// profundidades são o caminho mais longo do DAG, independentes da ordem de
// processamento; o desempate do nó devolvido é estável pela ordem do slice. Puro.
func maxDepth(doc plan.PlanDocument) (string, int) {
	// indeg[node] = nº de dependências por resolver; dependents[dep] = nós que
	// dependem directamente de dep (aresta dep→node). Construídos pela ordem do slice.
	indeg := make(map[string]int, len(doc.Nodes))
	dependents := make(map[string][]string, len(doc.Nodes))
	for _, n := range doc.Nodes {
		in := incomingEdges(n) // união dos dois canais (ver [incomingEdges])
		indeg[n.NodeID] = len(in)
		for _, dep := range in {
			dependents[dep] = append(dependents[dep], n.NodeID)
		}
	}
	depth := make(map[string]int, len(doc.Nodes))
	// Semeia a fila com as raízes (sem dependências) na ordem do slice: profundidade 1.
	queue := make([]string, 0, len(doc.Nodes))
	for _, n := range doc.Nodes {
		if indeg[n.NodeID] == 0 {
			depth[n.NodeID] = 1
			queue = append(queue, n.NodeID)
		}
	}
	// Relaxa as arestas em ordem topológica: depth[m] = max(depth[m], depth[id]+1).
	for i := 0; i < len(queue); i++ {
		id := queue[i]
		for _, m := range dependents[id] {
			if d := depth[id] + 1; d > depth[m] {
				depth[m] = d
			}
			indeg[m]--
			if indeg[m] == 0 {
				queue = append(queue, m)
			}
		}
	}
	bestNode, bestDepth := "", 0
	for _, n := range doc.Nodes { // ordem do slice ⇒ desempate determinístico
		if d := depth[n.NodeID]; d > bestDepth {
			bestNode, bestDepth = n.NodeID, d
		}
	}
	return bestNode, bestDepth
}
