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
	calls []string
}

func (f *fakeAdmission) Admit(_ context.Context, req AdmitRequest) (AdmitVerdict, error) {
	f.calls = append(f.calls, req.NodeID)
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

type spawnCall struct {
	nodeID    string
	authority []string
	childID   string
	parentBud string
}

type fakeSpawner struct{ calls []spawnCall }

func (f *fakeSpawner) Spawn(_ context.Context, r RoleSpawn) error {
	f.calls = append(f.calls, spawnCall{
		nodeID:    r.NodeID,
		authority: r.Child.Authority,
		childID:   r.Child.AgentID,
		parentBud: r.ParentBudgetNode,
	})
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
	sp  *fakeSpawner
	rec *fakeRecorder
}

func newHarness(t *testing.T, deny map[string]string, opts ...Option) harness {
	t.Helper()
	adm := &fakeAdmission{deny: deny}
	lf := &fakeLeaf{}
	sp := &fakeSpawner{}
	rec := &fakeRecorder{}
	m, err := NewMaterializer(adm, lf, sp, rec, opts...)
	if err != nil {
		t.Fatalf("NewMaterializer: %v", err)
	}
	return harness{m: m, adm: adm, lf: lf, sp: sp, rec: rec}
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
// 1) Papel-que-expande → Spawn (sub-árvore); folha → task.node.created (nó único).
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

	// Não-vacuidade: AMBOS os ramos foram exercidos.
	if len(h.sp.calls) != 1 || len(h.lf.calls) != 1 {
		t.Fatalf("esperava 1 spawn + 1 folha; got spawns=%v folhas=%v", h.sp.calls, h.lf.calls)
	}
	// Papel: arch → Spawn com a estrutura certa.
	if h.sp.calls[0].nodeID != "arch" {
		t.Errorf("papel esperado 'arch', got %q", h.sp.calls[0].nodeID)
	}
	if h.sp.calls[0].childID != "run-1/arch" || h.sp.calls[0].parentBud != "root" {
		t.Errorf("estrutura do spawn errada: %+v", h.sp.calls[0])
	}
	if !reflect.DeepEqual(h.sp.calls[0].authority, []string{"cap:tool:toolA"}) {
		t.Errorf("authority do papel = %v, esperado [cap:tool:toolA]", h.sp.calls[0].authority)
	}
	// Folha: impl → task.node.created (nó único) com a sua tool call.
	if h.lf.calls[0].nodeID != "impl" || h.lf.calls[0].toolID != "toolB" {
		t.Errorf("folha errada: %+v", h.lf.calls[0])
	}
	// Cruzamento: arch NÃO virou folha; impl NÃO virou spawn.
	for _, c := range h.lf.calls {
		if c.nodeID == "arch" {
			t.Errorf("arch (papel) não devia virar folha")
		}
	}
	for _, c := range h.sp.calls {
		if c.nodeID == "impl" {
			t.Errorf("impl (folha) não devia virar spawn")
		}
	}
	// plan.materialized reflecte os kinds, em ordem canónica [arch, impl].
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
	if len(h.sp.calls) != 1 {
		t.Fatalf("esperava 1 spawn, got %d", len(h.sp.calls))
	}
	auth := h.sp.calls[0].authority
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
	if !reflect.DeepEqual(h1.sp.calls, h2.sp.calls) {
		t.Errorf("spawns não determinísticos: %v vs %v", h1.sp.calls, h2.sp.calls)
	}
	if !reflect.DeepEqual(h1.lf.calls, h2.lf.calls) {
		t.Errorf("folhas não determinísticas: %v vs %v", h1.lf.calls, h2.lf.calls)
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
	if !reflect.DeepEqual(h1.sp.calls, h3.sp.calls) || !reflect.DeepEqual(h1.lf.calls, h3.lf.calls) {
		t.Errorf("ordem do slice afectou a materialização (não canónica): sp %v vs %v ; lf %v vs %v",
			h1.sp.calls, h3.sp.calls, h1.lf.calls, h3.lf.calls)
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
	// ZERO efeitos: nem spawn, nem folha, nem plan.materialized.
	if len(h.sp.calls) != 0 || len(h.lf.calls) != 0 || len(h.rec.payloads) != 0 {
		t.Errorf("materialização parcial após negação: spawns=%v folhas=%v rec=%d",
			h.sp.calls, h.lf.calls, len(h.rec.payloads))
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
	m, err := NewMaterializer(&fakeAdmission{}, &fakeLeaf{}, &fakeSpawner{}, realRec)
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
	if _, err := NewMaterializer(nil, &fakeLeaf{}, &fakeSpawner{}, &fakeRecorder{}); !errors.Is(err, ErrDeps) {
		t.Errorf("admission nil deveria dar ErrDeps, got %v", err)
	}
	if _, err := NewMaterializer(&fakeAdmission{}, nil, &fakeSpawner{}, &fakeRecorder{}); !errors.Is(err, ErrDeps) {
		t.Errorf("leaf nil deveria dar ErrDeps, got %v", err)
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
// 8) Orçamento achatado (fronteira §5): TODO spawn de papel pende do nó de orçamento
//    RAIZ do run, mesmo um papel que depende de OUTRO papel (sem aninhamento).
//    Falha-antes: se o materializador aninhasse o orçamento (ParentBudgetNode = papel-
//    pai), o spawn de 'b' teria parentBud "a", não "root".
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
	if len(h.sp.calls) != 2 {
		t.Fatalf("esperava 2 spawns de papel (a,b), got %d: %v", len(h.sp.calls), h.sp.calls)
	}
	for _, c := range h.sp.calls {
		if c.parentBud != "root" {
			t.Errorf("spawn %q: parentBud = %q, esperado 'root' (orçamento achatado, sem aninhamento)", c.nodeID, c.parentBud)
		}
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
