package planvalidate

import (
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

const capHash = "cap-hash-abc123"

// searchTool é uma tool resolúvel no snapshot base.
func searchTool() plan.ToolRef {
	return plan.ToolRef{Name: "search", Version: "1.0.0", Digest: "sha256:search"}
}

// baseSnapshot é um snapshot PINADO com a tool `search` admissível e uma tool
// `legacy` DEPRECADA, mais uma `blocked` inadmissível. O Hash liga ao capHash.
func baseSnapshot() Snapshot {
	return Snapshot{
		Hash: capHash,
		Tools: []Capability{
			{Name: "search", Version: "1.0.0", Digest: "sha256:search", Admissible: true},
			{Name: "legacy", Version: "2.0.0", Digest: "sha256:legacy", Deprecated: true, Admissible: true},
			{Name: "blocked", Version: "1.0.0", Digest: "sha256:blocked", Admissible: false},
		},
	}
}

// baseDoc é um plano VÁLIDO: cadeia a<-b<-c (c depende de b, b depende de a), com
// a tool `search` resolúvel. Passa [plan.Decode] (round-trip abaixo confirma-o),
// pelo que qualquer rejeição de [Validate] vem das regras 1–4, não da forma.
func baseDoc() plan.PlanDocument {
	return plan.PlanDocument{
		PlanVersion: plan.CurrentPlanVersion,
		Objective:   "objetivo de topo",
		BudgetTotal: plan.BudgetEstimate{Tokens: 100, CostMicroUSD: 1000},
		PlannerMeta: plan.PlannerMeta{
			Model:            "modelo-x",
			PromptVersion:    "1.2.3",
			CapabilitiesHash: capHash,
		},
		Nodes: []plan.Node{
			{NodeID: "a", Role: "r", Objective: "o-a", Tools: []plan.ToolRef{searchTool()}},
			{NodeID: "b", Role: "r", Objective: "o-b", DependsOn: []string{"a"}},
			{NodeID: "c", Role: "r", Objective: "o-c", DependsOn: []string{"b"}},
		},
	}
}

// mustBeShapeValid garante que o doc base passa [plan.Decode] — assim os testes de
// rejeição provam que a rejeição é SEMÂNTICA (regras 1–4), não de forma.
func mustBeShapeValid(t *testing.T, doc plan.PlanDocument) {
	t.Helper()
	raw, err := plan.Encode(doc)
	if err != nil {
		t.Fatalf("Encode base: %v", err)
	}
	if _, err := plan.Decode(raw); err != nil {
		t.Fatalf("doc base devia ser válido na forma, mas Decode rejeitou: %v", err)
	}
}

// TestAcceptaPlanoValido é a linha de base de NÃO-VACUIDADE: um plano válido é
// aceite. FALHA-ANTES: se qualquer regra rejeitasse espuriamente um plano correcto,
// OK seria falso aqui.
func TestAcceptaPlanoValido(t *testing.T) {
	doc := baseDoc()
	mustBeShapeValid(t, doc)
	v := Validate(doc, baseSnapshot(), Ceilings{MaxNodes: 10, MaxDepth: 10, MaxFanout: 10})
	if v.Rejected() {
		t.Fatalf("plano válido devia ser aceite, veio: %+v", v)
	}
	if v != accepted {
		t.Fatalf("veredicto de aceitação devia ser canónico, veio: %+v", v)
	}
}

// TestDeterminismo prova que o mesmo input dá o MESMO veredicto — tanto num caso
// aceite como num REJEITADO com múltiplos ofensores (dois nós com tool
// desconhecida). FALHA-ANTES: se [Validate] dependesse da ordem de iteração de um
// mapa, o node_id ofensor (ou o OK) variaria entre execuções.
func TestDeterminismo(t *testing.T) {
	// Caso rejeitado: dois nós, cada um com uma tool desconhecida distinta. A
	// ordem do slice tem de fazer o nó "n1" ser SEMPRE o ofensor devolvido.
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "n1", Role: "r", Objective: "o", Tools: []plan.ToolRef{{Name: "ghost1", Version: "9.9.9", Digest: "x"}}},
		{NodeID: "n2", Role: "r", Objective: "o", Tools: []plan.ToolRef{{Name: "ghost2", Version: "9.9.9", Digest: "y"}}},
	}
	snap := baseSnapshot()
	ceil := Ceilings{MaxNodes: 10, MaxDepth: 10, MaxFanout: 10}

	first := Validate(doc, snap, ceil)
	if !first.Rejected() {
		t.Fatalf("esperava rejeição, veio aceite")
	}
	for i := 0; i < 200; i++ {
		got := Validate(doc, snap, ceil)
		if got != first {
			t.Fatalf("não-determinismo na iteração %d: %+v != %+v", i, got, first)
		}
	}
	if first.Locator.NodeID != "n1" {
		t.Fatalf("ofensor determinístico devia ser o primeiro nó do slice (n1), veio %q", first.Locator.NodeID)
	}

	// Caso aceite: também tem de ser estável.
	ok := baseDoc()
	accept := Validate(ok, snap, ceil)
	for i := 0; i < 200; i++ {
		if got := Validate(ok, snap, ceil); got != accept {
			t.Fatalf("não-determinismo no caso aceite iteração %d", i)
		}
	}
}

// TestCicloRejeitado — REGRA 2. a e b dependem um do outro. FALHA-ANTES: sem a
// reutilização do DAG de AOS-025, o ciclo passaria despercebido e o plano seria
// aceite.
func TestCicloRejeitado(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "a", Role: "r", Objective: "o", DependsOn: []string{"b"}},
		{NodeID: "b", Role: "r", Objective: "o", DependsOn: []string{"a"}},
	}
	v := Validate(doc, baseSnapshot(), Ceilings{})
	if !v.Rejected() {
		t.Fatalf("ciclo devia ser rejeitado, veio aceite")
	}
	if v.Rule != plannerevents.RuleAcyclicity || v.Reason != ReasonCycle {
		t.Fatalf("esperava acyclicity/cycle, veio %s/%s", v.Rule, v.Reason)
	}
}

// TestAutoLacoRejeitado — um nó que depende de si próprio é um ciclo trivial.
// FALHA-ANTES: um detector que só olhasse arestas entre nós distintos deixaria
// passar o auto-laço.
func TestAutoLacoRejeitado(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{{NodeID: "a", Role: "r", Objective: "o", DependsOn: []string{"a"}}}
	v := Validate(doc, baseSnapshot(), Ceilings{})
	if !v.Rejected() || v.Rule != plannerevents.RuleAcyclicity {
		t.Fatalf("auto-laço devia ser ciclo rejeitado, veio %+v", v)
	}
}

// TestToolDesconhecidaRejeitadaSemTrimming — REGRA 3, tool INEXISTENTE. Prova o
// invariante "NUNCA trimming silencioso": o nó ofensor continua no documento após
// [Validate] E continua a causar a rejeição numa segunda passagem. FALHA-ANTES: um
// validador que removesse a tool/nó ofensor aceitaria o plano na segunda passagem.
func TestToolDesconhecidaRejeitadaSemTrimming(t *testing.T) {
	doc := baseDoc()
	doc.Nodes[1].Tools = []plan.ToolRef{{Name: "does-not-exist", Version: "1.0.0", Digest: "d"}}

	v := Validate(doc, baseSnapshot(), Ceilings{})
	if !v.Rejected() {
		t.Fatalf("tool inexistente devia rejeitar")
	}
	if v.Rule != plannerevents.RuleToolResolution || v.Reason != ReasonToolUnknown {
		t.Fatalf("esperava tool_resolution/tool_unknown, veio %s/%s", v.Rule, v.Reason)
	}
	if v.Locator.NodeID != "b" {
		t.Fatalf("ofensor devia ser o nó b, veio %q", v.Locator.NodeID)
	}
	// Prova de NÃO trimming: o documento não foi mutado.
	if len(doc.Nodes) != 3 {
		t.Fatalf("trimming detectado: nº de nós mudou para %d", len(doc.Nodes))
	}
	if len(doc.Nodes[1].Tools) != 1 || doc.Nodes[1].Tools[0].Name != "does-not-exist" {
		t.Fatalf("trimming detectado: a tool ofensora foi removida do nó")
	}
	// E o ofensor continua a causar rejeição numa segunda passagem (idempotente).
	if v2 := Validate(doc, baseSnapshot(), Ceilings{}); v2 != v {
		t.Fatalf("segunda passagem divergiu — indício de estado/trimming: %+v != %+v", v2, v)
	}
}

// TestToolDeprecadaRejeitada — REGRA 3, tool DEPRECADA (existe no snapshot mas
// retirada). FALHA-ANTES: tratar "existe no snapshot" como suficiente aceitaria a
// deprecada.
func TestToolDeprecadaRejeitada(t *testing.T) {
	doc := baseDoc()
	doc.Nodes[0].Tools = []plan.ToolRef{{Name: "legacy", Version: "2.0.0", Digest: "sha256:legacy"}}
	v := Validate(doc, baseSnapshot(), Ceilings{})
	if !v.Rejected() || v.Rule != plannerevents.RuleToolResolution || v.Reason != ReasonToolDeprecated {
		t.Fatalf("tool deprecada devia rejeitar com tool_deprecated, veio %+v", v)
	}
	if v.Locator.NodeID != "a" || v.Locator.Tool.Name != "legacy" {
		t.Fatalf("locator devia apontar o nó a / tool legacy, veio %+v", v.Locator)
	}
}

// TestToolInadmissivelRejeitada — REGRA 3, fora da allowlist. FALHA-ANTES: ignorar
// o campo Admissible aceitaria a tool bloqueada.
func TestToolInadmissivelRejeitada(t *testing.T) {
	doc := baseDoc()
	doc.Nodes[0].Tools = []plan.ToolRef{{Name: "blocked", Version: "1.0.0", Digest: "sha256:blocked"}}
	v := Validate(doc, baseSnapshot(), Ceilings{})
	if !v.Rejected() || v.Reason != ReasonToolInadmissible {
		t.Fatalf("tool inadmissível devia rejeitar com tool_inadmissible, veio %+v", v)
	}
}

// TestToolDigestDivergenteRejeitada — REGRA 3, versão existe mas digest não bate.
// FALHA-ANTES: resolver só por nome+versão (ignorando o digest pinado) aceitaria
// um binário adulterado.
func TestToolDigestDivergenteRejeitada(t *testing.T) {
	doc := baseDoc()
	doc.Nodes[0].Tools = []plan.ToolRef{{Name: "search", Version: "1.0.0", Digest: "sha256:ADULTERADO"}}
	v := Validate(doc, baseSnapshot(), Ceilings{})
	if !v.Rejected() || v.Reason != ReasonToolDigestMismatch {
		t.Fatalf("digest divergente devia rejeitar com tool_digest_mismatch, veio %+v", v)
	}
}

// TestTectoNosExcedido — REGRA 4, max_nodes. FALHA-ANTES: sem o tecto próprio do
// plano, um organigrama arbitrariamente largo passaria.
func TestTectoNosExcedido(t *testing.T) {
	doc := baseDoc() // 3 nós
	v := Validate(doc, baseSnapshot(), Ceilings{MaxNodes: 2})
	if !v.Rejected() || v.Rule != plannerevents.RuleStructuralCeiling || v.Reason != ReasonMaxNodesExceeded {
		t.Fatalf("esperava structural_ceiling/max_nodes_exceeded, veio %+v", v)
	}
}

// TestTectoProfundidadeExcedido — REGRA 4, max_depth. Cadeia a<-b<-c tem
// profundidade 3. FALHA-ANTES: não calcular a profundidade da cadeia deixaria
// passar uma árvore demasiado funda.
func TestTectoProfundidadeExcedido(t *testing.T) {
	doc := baseDoc()
	v := Validate(doc, baseSnapshot(), Ceilings{MaxDepth: 2})
	if !v.Rejected() || v.Reason != ReasonMaxDepthExceeded {
		t.Fatalf("esperava max_depth_exceeded, veio %+v", v)
	}
	if v.Locator.NodeID != "c" {
		t.Fatalf("ofensor de profundidade devia ser o fim da cadeia (c), veio %q", v.Locator.NodeID)
	}
}

// TestTectoFanoutExcedido — REGRA 4, max_fanout. b e c dependem ambos de a ⇒
// out-degree de a = 2. FALHA-ANTES: não medir o out-degree deixaria passar um nó
// com fan-out excessivo.
func TestTectoFanoutExcedido(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "a", Role: "r", Objective: "o"},
		{NodeID: "b", Role: "r", Objective: "o", DependsOn: []string{"a"}},
		{NodeID: "c", Role: "r", Objective: "o", DependsOn: []string{"a"}},
	}
	v := Validate(doc, baseSnapshot(), Ceilings{MaxFanout: 1})
	if !v.Rejected() || v.Reason != ReasonMaxFanoutExceeded {
		t.Fatalf("esperava max_fanout_exceeded, veio %+v", v)
	}
	if v.Locator.NodeID != "a" {
		t.Fatalf("ofensor de fanout devia ser a, veio %q", v.Locator.NodeID)
	}
}

// TestTectosDesligadosNaoRejeitam — um tecto <= 0 está desligado, não rejeita tudo.
// FALHA-ANTES: tratar 0 como "máximo zero" rejeitaria qualquer plano.
func TestTectosDesligadosNaoRejeitam(t *testing.T) {
	doc := baseDoc()
	if v := Validate(doc, baseSnapshot(), Ceilings{}); v.Rejected() {
		t.Fatalf("tectos a zero (desligados) não deviam rejeitar, veio %+v", v)
	}
}

// TestDependenciaPendenteRejeitada — REGRA 1, integridade referencial. FALHA-ANTES:
// não conferir que cada depends_on existe deixaria uma aresta pendente materializar
// um grafo incoerente.
func TestDependenciaPendenteRejeitada(t *testing.T) {
	doc := baseDoc()
	doc.Nodes[0].DependsOn = []string{"fantasma"}
	v := Validate(doc, baseSnapshot(), Ceilings{})
	if !v.Rejected() || v.Rule != plannerevents.RuleSchema || v.Reason != ReasonDanglingDependency {
		t.Fatalf("esperava schema/dangling_dependency, veio %+v", v)
	}
	if v.Locator.NodeID != "a" {
		t.Fatalf("ofensor devia ser a, veio %q", v.Locator.NodeID)
	}
}

// TestMajorIncompativelRejeitado — REGRA 1, compatibilidade de MAJOR. FALHA-ANTES:
// [plan.Decode] deixa a compatibilidade de MAJOR para AOS-231; se não a
// conferíssemos, um plano de MAJOR futuro seria materializado por um leitor que não
// o entende.
func TestMajorIncompativelRejeitado(t *testing.T) {
	doc := baseDoc()
	doc.PlanVersion = plan.PlanVersion{Major: plan.CurrentPlanVersion.Major + 1}
	v := Validate(doc, baseSnapshot(), Ceilings{})
	if !v.Rejected() || v.Reason != ReasonVersionIncompatible {
		t.Fatalf("esperava plan_version_incompatible, veio %+v", v)
	}
}

// TestSnapshotHashDivergenteRejeitado — REGRA 1, binding do snapshot. Se o snapshot
// recebido não é aquele contra o qual o plano foi carimbado, resolver tools contra
// ele seria enganoso. FALHA-ANTES: aceitar sem conferir o binding permitiria validar
// contra o snapshot errado.
func TestSnapshotHashDivergenteRejeitado(t *testing.T) {
	doc := baseDoc()
	snap := baseSnapshot()
	snap.Hash = "cap-hash-OUTRO"
	v := Validate(doc, snap, Ceilings{})
	if !v.Rejected() || v.Reason != ReasonCapabilitiesHashMismatch {
		t.Fatalf("esperava capabilities_hash_mismatch, veio %+v", v)
	}
}

// TestFeedbackSemConteudoCru — DoD: o veredicto é allowlisted e NÃO ecoa conteúdo
// cru do documento (objectivos/papéis, texto livre do modelo). Injecta um objectivo
// "sensível" e confirma que não aparece em nenhum campo string do veredicto.
// FALHA-ANTES: se o Locator/Verdict carregasse o Objective ou Role, o segredo
// vazaria no feedback de retry.
func TestFeedbackSemConteudoCru(t *testing.T) {
	const secret = "SEGREDO-PII-nao-deve-vazar"
	doc := baseDoc()
	doc.Objective = secret
	doc.Nodes[1].Objective = secret
	doc.Nodes[1].Role = secret
	doc.Nodes[1].Tools = []plan.ToolRef{{Name: "does-not-exist", Version: "1.0.0", Digest: "d"}}

	v := Validate(doc, baseSnapshot(), Ceilings{})
	if !v.Rejected() {
		t.Fatalf("esperava rejeição")
	}
	for _, s := range []string{
		string(v.Rule), string(v.Reason),
		v.Locator.NodeID, v.Locator.Tool.Name, v.Locator.Tool.Version, v.Locator.Tool.Digest,
	} {
		if s == secret {
			t.Fatalf("conteúdo cru vazou no veredicto: %q", s)
		}
	}
	// A Rule é uma constante allowlisted de plannerevents (não texto livre).
	if _, err := plannerevents.NewValidationFailed(plannerevents.ValidationOutcome{
		PlanID: "p", PlanHash: "h", Rule: v.Rule, Attempt: 1, MaxAttempts: MaxAttempts,
	}); err != nil {
		t.Fatalf("a Rule do veredicto devia ser allowlisted em plannerevents, veio erro: %v", err)
	}

	// Vector node_id — o node_id é o ÚNICO campo derivado do documento que o Locator
	// propaga. Um id com texto livre (espaços/dois-pontos/prosa) é rejeitado pela
	// regra 1 (grammar de identificador) com um Locator VAZIO, pelo que o valor
	// untrusted NUNCA aparece no veredicto. FALHA-ANTES: sem validNodeID o node_id
	// malformado fluía verbatim para Locator.NodeID e vazava no feedback de retry.
	freeText := "exfiltra PII " + secret + " agora"
	doc2 := baseDoc()
	doc2.Nodes[1].NodeID = freeText
	v2 := Validate(doc2, baseSnapshot(), Ceilings{})
	if !v2.Rejected() {
		t.Fatalf("node_id de texto livre devia rejeitar")
	}
	if v2.Rule != plannerevents.RuleSchema || v2.Reason != ReasonInvalidNodeID {
		t.Fatalf("esperava schema/invalid_node_id, veio %s/%s", v2.Rule, v2.Reason)
	}
	if v2.Locator.NodeID != "" {
		t.Fatalf("node_id untrusted vazou no locator: %q", v2.Locator.NodeID)
	}
}

// TestNodeIDGrammarLimites — a grammar de node_id ([validNodeID]) aceita
// identificadores estruturais realistas e rejeita texto livre e blobs. FALHA-ANTES:
// sem a invariante, qualquer string não-vazia passaria e poderia ser ecoada no
// feedback.
func TestNodeIDGrammarLimites(t *testing.T) {
	valid := []string{"a", "node-1", "stage.build", "svc:worker", "A_b.C-1:2", "x"}
	for _, id := range valid {
		if !validNodeID(id) {
			t.Fatalf("id estrutural válido foi rejeitado: %q", id)
		}
	}
	// O último caso é o TECTO DE COMPRIMENTO (128 bytes em [plan.ValidNodeID], onde a
	// grammar passou a viver desde AOS-271): 129 bytes de charset VÁLIDO, para que a
	// rejeição prove o comprimento e não o alfabeto.
	invalid := []string{"", "has space", "line\nbreak", "quote\"", "unicodé", "a/b", strings.Repeat("a", 129)}
	for _, id := range invalid {
		if validNodeID(id) {
			t.Fatalf("id inválido/texto-livre foi aceite: %q", id)
		}
	}
}

// TestProfundidadeCaminhoMaisLongo — REGRA 4, CORRECÇÃO do cálculo iterativo (Kahn)
// de maxDepth num grafo NÃO-linear. Grafo: a→b→d e a→c (b/c dependem de a; d depende
// de b). O caminho mais longo é a→b→d (profundidade 3); o ramo curto a→c (2) NÃO pode
// mascarar o longo. FALHA-ANTES: uma relaxação que usasse nível-BFS, ou que fixasse a
// profundidade de d antes de b estar resolvido, reportaria profundidade errada ou o
// ofensor errado.
func TestProfundidadeCaminhoMaisLongo(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "a", Role: "r", Objective: "o"},
		{NodeID: "b", Role: "r", Objective: "o", DependsOn: []string{"a"}},
		{NodeID: "c", Role: "r", Objective: "o", DependsOn: []string{"a"}},
		{NodeID: "d", Role: "r", Objective: "o", DependsOn: []string{"b"}},
	}
	// Aceite com MaxDepth 3 (profundidade real == 3, não excede).
	if v := Validate(doc, baseSnapshot(), Ceilings{MaxDepth: 3}); v.Rejected() {
		t.Fatalf("profundidade real 3 não devia exceder MaxDepth 3, veio %+v", v)
	}
	// Rejeita com MaxDepth 2: o ofensor é o fim do caminho MAIS LONGO (d), não c.
	v := Validate(doc, baseSnapshot(), Ceilings{MaxDepth: 2})
	if !v.Rejected() || v.Reason != ReasonMaxDepthExceeded {
		t.Fatalf("esperava max_depth_exceeded, veio %+v", v)
	}
	if v.Locator.NodeID != "d" {
		t.Fatalf("ofensor devia ser o fim do caminho mais longo (d), veio %q", v.Locator.NodeID)
	}
}

// TestProfundidadeCadeiaFundaTermina — REGRA 4, ROBUSTEZ: uma cadeia linear muito
// funda (MaxDepth ligado, MaxNodes DESLIGADO) é avaliada em consumo de pilha O(1),
// porque maxDepth passou a ser ITERATIVO (Kahn) em vez de DFS recursivo por nó. Não
// afirma que a versão recursiva ESTOURAVA a esta profundidade (as pilhas de goroutine
// do Go crescem dinamicamente); afirma que o caminho iterativo termina correctamente
// à escala e devolve o ofensor certo, sem depender do crescimento de pilha nativo.
func TestProfundidadeCadeiaFundaTermina(t *testing.T) {
	const n = 60000
	nodes := make([]plan.Node, n)
	for i := 0; i < n; i++ {
		nd := plan.Node{NodeID: "n" + itoa(i), Role: "r", Objective: "o"}
		if i > 0 {
			nd.DependsOn = []string{"n" + itoa(i-1)}
		}
		nodes[i] = nd
	}
	doc := baseDoc()
	doc.Nodes = nodes

	v := Validate(doc, baseSnapshot(), Ceilings{MaxDepth: n - 1})
	if !v.Rejected() || v.Reason != ReasonMaxDepthExceeded {
		t.Fatalf("esperava max_depth_exceeded numa cadeia de %d nós, veio %+v", n, v)
	}
	if v.Locator.NodeID != "n"+itoa(n-1) {
		t.Fatalf("ofensor devia ser o fim da cadeia (n%d), veio %q", n-1, v.Locator.NodeID)
	}
}

// itoa converte um inteiro não-negativo em decimal sem depender de strconv (mantém o
// teste focado; equivalente a strconv.Itoa para i >= 0).
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

// TestEsgotamentoIntake — N=3 rejeições ⇒ falha de intake fail-closed. FALHA-ANTES:
// um contador que nunca esgotasse (ou esgotasse cedo/tarde de mais) daria o número
// de tentativas errado.
func TestEsgotamentoIntake(t *testing.T) {
	badDoc := baseDoc()
	badDoc.Nodes[0].Tools = []plan.ToolRef{{Name: "ghost", Version: "1", Digest: "d"}}
	snap := baseSnapshot()

	l := NewLedger()
	if l.Max() != MaxAttempts || MaxAttempts != 3 {
		t.Fatalf("tecto de tentativas devia ser 3, veio %d", l.Max())
	}

	var lastAttempt int
	var exhausted bool
	for l.Next() {
		v := Validate(badDoc, snap, Ceilings{})
		if !v.Rejected() {
			t.Fatalf("o doc mau devia rejeitar em toda a tentativa")
		}
		lastAttempt, exhausted = l.Record(v)
	}
	if lastAttempt != 3 {
		t.Fatalf("deviam ter-se gasto 3 tentativas, gastaram-se %d", lastAttempt)
	}
	if !exhausted || !l.Exhausted() {
		t.Fatalf("o intake devia estar esgotado após 3 rejeições")
	}
	if l.Next() {
		t.Fatalf("Next() devia ser falso após esgotamento (fail-closed)")
	}
	if !errors.Is(l.Err(), ErrIntakeExhausted) {
		t.Fatalf("Err() devia ser ErrIntakeExhausted, veio %v", l.Err())
	}
}

// TestSucessoAntesDoEsgotamento — uma proposta válida na 2ª tentativa encerra o
// ciclo sem esgotar. FALHA-ANTES: contar o sucesso como falha (ou continuar a
// permitir tentativas após sucesso) seria fail-open na direcção errada.
func TestSucessoAntesDoEsgotamento(t *testing.T) {
	l := NewLedger()

	// Tentativa 1: falha.
	if n, ex := l.Record(reject(plannerevents.RuleSchema, ReasonMalformedNode, Locator{})); n != 1 || ex {
		t.Fatalf("tentativa 1: esperava (1,false), veio (%d,%v)", n, ex)
	}
	// Tentativa 2: sucesso ⇒ encerra, sem esgotar.
	if n, ex := l.Record(accepted); n != 2 || ex {
		t.Fatalf("tentativa 2: esperava (2,false), veio (%d,%v)", n, ex)
	}
	if l.Exhausted() {
		t.Fatalf("sucesso não devia esgotar o intake")
	}
	if l.Next() {
		t.Fatalf("após sucesso, não deviam permitir-se mais tentativas")
	}
	if err := l.Err(); err != nil {
		t.Fatalf("Err() devia ser nil após sucesso, veio %v", err)
	}
}

// TestLedgerNaoEsgotaCedo — 2 falhas com tecto 3 ⇒ ainda NÃO esgotado; há 3ª
// tentativa. FALHA-ANTES: esgotar à segunda seria fail-closed cedo de mais.
func TestLedgerNaoEsgotaCedo(t *testing.T) {
	l := NewLedger()
	l.Record(reject(plannerevents.RuleAcyclicity, ReasonCycle, Locator{}))
	_, ex := l.Record(reject(plannerevents.RuleAcyclicity, ReasonCycle, Locator{}))
	if ex || l.Exhausted() {
		t.Fatalf("2 falhas de 3 não deviam esgotar")
	}
	if !l.Next() {
		t.Fatalf("devia ainda haver uma 3ª tentativa")
	}
}
