package planmaterialize

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// Fakes das portas (falsificáveis: capturam ORDEM e conteúdo dos efeitos).
// ---------------------------------------------------------------------------

type fakeAdmission struct {
	deny  map[string]string // node_id -> reason (ausente = admitido)
	calls []string          // ordem de admissão (node_ids)
	// Captura dos inteiros do AdmitRequest, por nó — a materialização é hoje o ÚNICO
	// ponto que consome clampU64ToInt64 (o RoleSpawn saiu; ver materialize_clamp_test.go).
	tokens       map[string]int64
	costMicroUSD map[string]int64
}

func (f *fakeAdmission) Admit(_ context.Context, req AdmitRequest) (AdmitVerdict, error) {
	f.calls = append(f.calls, req.NodeID)
	if f.tokens == nil {
		f.tokens = map[string]int64{}
	}
	if f.costMicroUSD == nil {
		f.costMicroUSD = map[string]int64{}
	}
	f.tokens[req.NodeID] = req.Tokens
	f.costMicroUSD[req.NodeID] = req.CostMicroUSD
	if reason, ok := f.deny[req.NodeID]; ok {
		return AdmitVerdict{Admitted: false, Reason: reason}, nil
	}
	return AdmitVerdict{Admitted: true}, nil
}

type leafCall struct {
	nodeID string
	toolID string
	caps   []string
}

type fakeLeaf struct{ calls []leafCall }

func (f *fakeLeaf) AdmitLeaf(_ context.Context, n LeafNode) error {
	f.calls = append(f.calls, leafCall{nodeID: n.NodeID, toolID: n.ToolID, caps: n.Capabilities})
	return nil
}

type fakeRecorder struct {
	payloads []plannerevents.MaterializedPayload
}

func (f *fakeRecorder) RecordMaterialized(_ context.Context, p plannerevents.MaterializedPayload) (uint64, error) {
	f.payloads = append(f.payloads, p)
	return uint64(len(f.payloads)), nil
}

// harness agrupa o materializer e os fakes.
type harness struct {
	m   *Materializer
	adm *fakeAdmission
	lf  *fakeLeaf
	rec *fakeRecorder
}

func newHarness(t *testing.T, deny map[string]string, opts ...Option) harness {
	t.Helper()
	adm := &fakeAdmission{deny: deny}
	lf := &fakeLeaf{}
	rec := &fakeRecorder{}
	m, err := NewMaterializer(adm, lf, rec, opts...)
	if err != nil {
		t.Fatalf("NewMaterializer: %v", err)
	}
	return harness{m: m, adm: adm, lf: lf, rec: rec}
}

// leafCallFor devolve a admissão no DAG de um nó (folha OU papel — pós ADR-024 ambos
// passam por AdmitLeaf) e se ela existe.
func leafCallFor(calls []leafCall, id string) (leafCall, bool) {
	for _, c := range calls {
		if c.nodeID == id {
			return c, true
		}
	}
	return leafCall{}, false
}

// nodeInPayload devolve o nó materializado (Kind + autoridade CLAMPADA em Tools) do
// facto plan.materialized — a fonte de verdade da classificação folha-vs-papel e da
// autoridade, agora que o spawn saiu da materialização.
func nodeInPayload(p plannerevents.MaterializedPayload, id string) (plannerevents.MaterializedNode, bool) {
	for _, n := range p.Nodes {
		if n.NodeID == id {
			return n, true
		}
	}
	return plannerevents.MaterializedNode{}, false
}

// tool constrói uma ToolRef pinada (name+version+digest) — forma exigida pelo schema.
func tool(name string) plan.ToolRef {
	return plan.ToolRef{Name: name, Version: "1.0.0", Digest: "sha256:" + name}
}

func node(id string, tools []plan.ToolRef, deps ...string) plan.Node {
	return plan.Node{NodeID: id, Role: "role-" + id, Objective: "obj-" + id, Tools: tools, DependsOn: deps}
}

// baseReq monta um Request com nós dados (ordem do slice preservada no doc gravado).
func baseReq(nodes ...plan.Node) Request {
	return Request{
		RunID:          "run-1",
		PlanID:         "plan-1",
		PlanHash:       "sha256:approved",
		ParentToken:    "parent.tok.sig",
		RootBudgetNode: "root",
		Doc:            plan.PlanDocument{Objective: "top", Nodes: nodes},
	}
}

// ---------------------------------------------------------------------------
// 1) Papel-que-expande e folha são AMBOS admitidos no DAG (pós ADR-024): o papel
//    SEM tool (placeholder pendente), a folha com a sua tool call. A distinção
//    folha-vs-papel e a autoridade clampada lêem-se do payload plan.materialized.
// ---------------------------------------------------------------------------

func TestRoleExpandsLeafBecomesNode(t *testing.T) {
	// impl depends_on arch ⇒ arch tem dependente ⇒ PAPEL; impl sem dependente ⇒ FOLHA.
	req := baseReq(
		node("arch", []plan.ToolRef{tool("toolA")}),
		node("impl", []plan.ToolRef{tool("toolB")}, "arch"),
	)
	h := newHarness(t, nil)

	if _, err := h.m.Materialize(context.Background(), req); err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	// Não-vacuidade: AMBOS os nós foram admitidos no DAG (não há mais chamada de spawn
	// na materialização — o spawn do papel é do DispatchSink, noutro package).
	if len(h.lf.calls) != 2 {
		t.Fatalf("esperava 2 nós admitidos (arch, impl); got %+v", h.lf.calls)
	}
	// Papel: arch admitido SEM tool (placeholder pendente).
	archLeaf, ok := leafCallFor(h.lf.calls, "arch")
	if !ok {
		t.Fatalf("papel 'arch' não foi admitido no DAG: %+v", h.lf.calls)
	}
	if archLeaf.toolID != "" {
		t.Errorf("papel 'arch' devia ser admitido SEM tool (placeholder), got toolID=%q", archLeaf.toolID)
	}
	// Folha: impl admitida com a sua tool call concreta.
	implLeaf, ok := leafCallFor(h.lf.calls, "impl")
	if !ok || implLeaf.toolID != "toolB" {
		t.Errorf("folha 'impl' errada: %+v (ok=%v)", implLeaf, ok)
	}
	// plan.materialized reflecte os kinds e a autoridade clampada, em ordem canónica
	// [arch, impl]. É daqui (não de um spawn) que se lê papel-vs-folha e a autoridade.
	if len(h.rec.payloads) != 1 {
		t.Fatalf("esperava 1 plan.materialized, got %d", len(h.rec.payloads))
	}
	got := h.rec.payloads[0].Nodes
	want := []plannerevents.MaterializedNode{
		{NodeID: "arch", Kind: plannerevents.SpawnRole, Tools: []string{"cap:tool:toolA"}},
		{NodeID: "impl", Kind: plannerevents.SpawnLeaf, Tools: []string{"cap:tool:toolB"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plan.materialized.nodes = %+v, esperado %+v", got, want)
	}
}

// ---------------------------------------------------------------------------
// 2) Authority da NHI filha LIMITADA às tools do papel (clamp).
//    Falha-antes: sem o clamp (usando as tools do PLANO INTEIRO), a tool de outro
//    papel entraria na Authority.
// ---------------------------------------------------------------------------

func TestChildAuthorityClampedToRoleTools(t *testing.T) {
	req := baseReq(
		node("arch", []plan.ToolRef{tool("toolA")}),         // papel (tem dependente)
		node("impl", []plan.ToolRef{tool("toolB")}, "arch"), // folha
	)
	h := newHarness(t, nil)
	if _, err := h.m.Materialize(context.Background(), req); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	// A autoridade CLAMPADA do papel lê-se do payload (Nodes[].Tools), não de um spawn.
	archNode, ok := nodeInPayload(h.rec.payloads[0], "arch")
	if !ok {
		t.Fatalf("nó 'arch' ausente do payload: %+v", h.rec.payloads[0].Nodes)
	}
	auth := archNode.Tools
	// A tool do PRÓPRIO papel está presente.
	if !contains(auth, "cap:tool:toolA") {
		t.Errorf("authority deveria conter a tool do papel cap:tool:toolA; got %v", auth)
	}
	// A tool de OUTRO papel (a folha 'impl') NÃO entra — este é o clamp. Sem ele
	// (tools do plano inteiro) cap:tool:toolB apareceria.
	if contains(auth, "cap:tool:toolB") {
		t.Errorf("CLAMP falhou: authority do papel 'arch' contém a tool alheia cap:tool:toolB: %v", auth)
	}
}

// ---------------------------------------------------------------------------
// 3) Materialização determinística: mesmo documento → mesma sequência de efeitos.
//    Reforço: a ordem é CANÓNICA (por node_id), independente da ordem do slice.
// ---------------------------------------------------------------------------

func TestDeterministicMaterialization(t *testing.T) {
	nodes := []plan.Node{
		node("gamma", []plan.ToolRef{tool("g")}),
		node("alpha", []plan.ToolRef{tool("a")}, "gamma"),
		node("beta", []plan.ToolRef{tool("b")}, "gamma"),
	}
	req := baseReq(nodes...)

	h1 := newHarness(t, nil)
	p1, err := h1.m.Materialize(context.Background(), req)
	if err != nil {
		t.Fatalf("run1: %v", err)
	}
	h2 := newHarness(t, nil)
	p2, err := h2.m.Materialize(context.Background(), req)
	if err != nil {
		t.Fatalf("run2: %v", err)
	}

	if !reflect.DeepEqual(h1.adm.calls, h2.adm.calls) {
		t.Errorf("ordem de admissão não determinística: %v vs %v", h1.adm.calls, h2.adm.calls)
	}
	if !reflect.DeepEqual(h1.lf.calls, h2.lf.calls) {
		t.Errorf("admissões no DAG não determinísticas: %v vs %v", h1.lf.calls, h2.lf.calls)
	}
	if !reflect.DeepEqual(p1, p2) {
		t.Errorf("payloads plan.materialized divergem: %+v vs %+v", p1, p2)
	}
	// Ordem canónica esperada: admissão por node_id ordenado.
	if !reflect.DeepEqual(h1.adm.calls, []string{"alpha", "beta", "gamma"}) {
		t.Errorf("admissão não em ordem canónica: %v", h1.adm.calls)
	}

	// Ordem do slice EMBARALHADA no doc gravado → mesma sequência (canónica).
	shuffled := baseReq(nodes[1], nodes[2], nodes[0]) // alpha, beta, gamma
	h3 := newHarness(t, nil)
	if _, err := h3.m.Materialize(context.Background(), shuffled); err != nil {
		t.Fatalf("run3: %v", err)
	}
	if !reflect.DeepEqual(h1.lf.calls, h3.lf.calls) {
		t.Errorf("ordem do slice afectou a materialização (não canónica): lf %v vs %v",
			h1.lf.calls, h3.lf.calls)
	}
}

// ---------------------------------------------------------------------------
// 4) Fail-closed: nenhum nó materializa sem admissão global (AOS-027/028).
//    Duas fases ⇒ uma negação aborta ANTES de qualquer efeito (zero parciais).
// ---------------------------------------------------------------------------

func TestNodeNotAdmittedFailsClosed(t *testing.T) {
	req := baseReq(
		node("arch", []plan.ToolRef{tool("toolA")}),
		node("impl", []plan.ToolRef{tool("toolB")}, "arch"),
	)
	h := newHarness(t, map[string]string{"impl": "sem capacidade global"})

	_, err := h.m.Materialize(context.Background(), req)
	if !errors.Is(err, ErrNodeNotAdmitted) {
		t.Fatalf("esperava ErrNodeNotAdmitted, got %v", err)
	}
	// ZERO efeitos: nenhum nó admitido no DAG, nenhum plan.materialized apenso.
	if len(h.lf.calls) != 0 || len(h.rec.payloads) != 0 {
		t.Errorf("materialização parcial após negação: admissões=%v rec=%d",
			h.lf.calls, len(h.rec.payloads))
	}
}

// ---------------------------------------------------------------------------
// 5) Emite plan.materialized reutilizando a CONSTANTE plannerevents.EventMaterialized
//    (via o Recorder real sobre um Appender falso — prova a fronteira do catálogo).
// ---------------------------------------------------------------------------

type fakeAppender struct {
	lastType    string
	lastPayload []byte
	count       int
}

func (f *fakeAppender) Append(_ context.Context, _ string, in eventstore.EventInput, _ ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	f.lastType = in.Type
	f.lastPayload = in.Payload
	f.count++
	return eventstore.AppendResult{Seq: uint64(f.count)}, nil
}

func TestEmitsPlanMaterializedConstant(t *testing.T) {
	app := &fakeAppender{}
	realRec, err := plannerevents.NewRecorder(app)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	m, err := NewMaterializer(&fakeAdmission{}, &fakeLeaf{}, realRec)
	if err != nil {
		t.Fatalf("NewMaterializer: %v", err)
	}
	req := baseReq(node("solo", []plan.ToolRef{tool("t")}))

	if _, err := m.Materialize(context.Background(), req); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if app.lastType != plannerevents.EventMaterialized {
		t.Fatalf("evento apenso = %q, esperado a constante %q", app.lastType, plannerevents.EventMaterialized)
	}
	var got plannerevents.MaterializedPayload
	if err := json.Unmarshal(app.lastPayload, &got); err != nil {
		t.Fatalf("payload ilegível: %v", err)
	}
	if got.PlanID != "plan-1" || len(got.Nodes) != 1 || got.Nodes[0].NodeID != "solo" {
		t.Errorf("payload inesperado: %+v", got)
	}
	// 'solo' não tem dependentes ⇒ folha.
	if got.Nodes[0].Kind != plannerevents.SpawnLeaf {
		t.Errorf("nó solo deveria ser folha, got %q", got.Nodes[0].Kind)
	}
}

// ---------------------------------------------------------------------------
// 6) Guardas de construção / entrada (fail-closed).
// ---------------------------------------------------------------------------

func TestConstructionAndRequestGuards(t *testing.T) {
	if _, err := NewMaterializer(nil, &fakeLeaf{}, &fakeRecorder{}); !errors.Is(err, ErrDeps) {
		t.Errorf("admission nil deveria dar ErrDeps, got %v", err)
	}
	if _, err := NewMaterializer(&fakeAdmission{}, nil, &fakeRecorder{}); !errors.Is(err, ErrDeps) {
		t.Errorf("leaf nil deveria dar ErrDeps, got %v", err)
	}
	if _, err := NewMaterializer(&fakeAdmission{}, &fakeLeaf{}, nil); !errors.Is(err, ErrDeps) {
		t.Errorf("recorder nil deveria dar ErrDeps, got %v", err)
	}
	h := newHarness(t, nil)
	if _, err := h.m.Materialize(context.Background(), baseReq()); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("doc sem nós deveria dar ErrInvalidRequest, got %v", err)
	}
	// node_id vazio.
	bad := baseReq(node("", []plan.ToolRef{tool("t")}))
	if _, err := h.m.Materialize(context.Background(), bad); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("node_id vazio deveria dar ErrInvalidRequest, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 7) Folha multi-tool (fronteira §5): o DAG (single-tool) leva a PRIMEIRA tool em
//    ordem canónica, MAS o conjunto coarse COMPLETO sobrevive sem perda em
//    LeafNode.Capabilities e em plan.materialized.Nodes[].Tools (registo autoritativo).
//    Falha-antes: se a redução ao single-tool do DAG apagasse a autoridade, caps/Tools
//    teriam <2 entradas ou perderiam a segunda tool.
// ---------------------------------------------------------------------------

func TestLeafMultiToolPreservesAuthority(t *testing.T) {
	// 'solo' sem dependentes ⇒ folha; duas tools (ordem canónica das caps: a<z).
	req := baseReq(node("solo", []plan.ToolRef{tool("ztool"), tool("atool")}))
	h := newHarness(t, nil)
	if _, err := h.m.Materialize(context.Background(), req); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if len(h.lf.calls) != 1 {
		t.Fatalf("esperava 1 folha, got %d", len(h.lf.calls))
	}
	// DAG single-tool: leva a PRIMEIRA tool em ordem do documento (ztool).
	if h.lf.calls[0].toolID != "ztool" {
		t.Errorf("toolID do DAG = %q, esperado a primeira do documento 'ztool'", h.lf.calls[0].toolID)
	}
	// SEM PERDA: o conjunto coarse completo (ordenado) viaja em Capabilities.
	wantCaps := []string{"cap:tool:atool", "cap:tool:ztool"}
	if !reflect.DeepEqual(h.lf.calls[0].caps, wantCaps) {
		t.Errorf("Capabilities da folha = %v, esperado o conjunto completo %v", h.lf.calls[0].caps, wantCaps)
	}
	// Registo AUTORITATIVO: plan.materialized carrega o conjunto completo.
	got := h.rec.payloads[0].Nodes
	if len(got) != 1 || !reflect.DeepEqual(got[0].Tools, wantCaps) {
		t.Errorf("plan.materialized.Tools = %+v, esperado %v (conjunto completo)", got, wantCaps)
	}
}

// ---------------------------------------------------------------------------
// 8) Cadeia de papéis: 'a' e 'b' (que dependem um do outro) são AMBOS classificados
//    papel-que-expande e admitidos no DAG SEM tool; 'c' é folha.
//
//    NOTA (AOS-390, ADR-024): a propriedade original — "orçamento achatado à raiz"
//    (ParentBudgetNode == root, sem aninhamento) — era do RoleSpawn e SAIU da
//    materialização. O orçamento dos spawns é hoje reconstruído pelo DispatchSink
//    (noutro package) a partir do payload; não há sink a inventar aqui. O que a
//    materialização ainda garante, e é o que se assevera, é a CLASSIFICAÇÃO topológica
//    (papel-vs-folha) e a admissão sem-tool dos papéis.
// ---------------------------------------------------------------------------

func TestRoleBudgetIsFlatToRoot(t *testing.T) {
	// a ← b ← c : 'a' e 'b' têm dependentes ⇒ papéis; 'c' é folha.
	req := baseReq(
		node("a", []plan.ToolRef{tool("ta")}),
		node("b", []plan.ToolRef{tool("tb")}, "a"),
		node("c", []plan.ToolRef{tool("tc")}, "b"),
	)
	h := newHarness(t, nil)
	if _, err := h.m.Materialize(context.Background(), req); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	// 'a' e 'b' são papéis no payload e foram admitidos no DAG SEM tool; 'c' é folha.
	for _, id := range []string{"a", "b"} {
		n, ok := nodeInPayload(h.rec.payloads[0], id)
		if !ok || n.Kind != plannerevents.SpawnRole {
			t.Errorf("%q devia ser papel no payload, got %+v (ok=%v)", id, n, ok)
		}
		lc, ok := leafCallFor(h.lf.calls, id)
		if !ok || lc.toolID != "" {
			t.Errorf("papel %q devia ser admitido SEM tool: %+v (ok=%v)", id, lc, ok)
		}
	}
	if n, ok := nodeInPayload(h.rec.payloads[0], "c"); !ok || n.Kind != plannerevents.SpawnLeaf {
		t.Errorf("'c' devia ser folha no payload, got %+v (ok=%v)", n, ok)
	}
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
