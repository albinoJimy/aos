package planapproval

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/control-plane/governance/autonomy"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// --- version.go ------------------------------------------------------------------

func TestVersion_ParseCompareCompatibleClassify(t *testing.T) {
	if _, err := ParsePlanCardSchemaVersion(""); err != ErrInvalidSchemaVersion {
		t.Fatalf("vazio: %v", err)
	}
	if _, err := ParsePlanCardSchemaVersion("1.0"); err != ErrInvalidSchemaVersion {
		t.Fatalf("2 componentes: %v", err)
	}
	if _, err := ParsePlanCardSchemaVersion("1..0"); err != ErrInvalidSchemaVersion {
		t.Fatalf("componente vazio: %v", err)
	}
	if _, err := ParsePlanCardSchemaVersion("1.x.0"); err != ErrInvalidSchemaVersion {
		t.Fatalf("nao-inteiro: %v", err)
	}
	if _, err := ParsePlanCardSchemaVersion("1.-1.0"); err != ErrInvalidSchemaVersion {
		t.Fatalf("negativo: %v", err)
	}
	v, err := ParsePlanCardSchemaVersion(" 2.3.4 ")
	if err != nil || v.Major != 2 || v.Minor != 3 || v.Patch != 4 {
		t.Fatalf("parse valido: %v %+v", err, v)
	}
	if v.String() != "2.3.4" {
		t.Fatalf("String: %q", v.String())
	}

	a := PlanCardSchemaVersion{1, 0, 0}
	b := PlanCardSchemaVersion{1, 2, 0}
	c := PlanCardSchemaVersion{2, 0, 0}
	if a.Compare(b) != -1 || b.Compare(a) != 1 || a.Compare(a) != 0 {
		t.Fatal("Compare")
	}
	if (PlanCardSchemaVersion{1, 0, 1}).Compare(PlanCardSchemaVersion{1, 0, 0}) != 1 {
		t.Fatal("Compare patch")
	}
	if !a.Equal(PlanCardSchemaVersion{1, 0, 0}) || a.Equal(b) {
		t.Fatal("Equal")
	}
	if !a.Compatible(b) || a.Compatible(c) {
		t.Fatal("Compatible (mesmo MAJOR)")
	}
	if Classify(a, a) != ChangeNone || Classify(a, b) != ChangeMinor ||
		Classify(a, c) != ChangeMajor || Classify(a, PlanCardSchemaVersion{1, 0, 9}) != ChangePatch {
		t.Fatal("Classify")
	}
	// Um downgrade de MAJOR também é ChangeMajor (simétrico).
	if Classify(c, a) != ChangeMajor {
		t.Fatal("Classify downgrade MAJOR")
	}
	if ChangeNone.String() != "none" || ChangePatch.String() != "patch" ||
		ChangeMinor.String() != "minor" || ChangeMajor.String() != "major" || ChangeKind(99).String() != "unknown" {
		t.Fatal("ChangeKind.String")
	}
}

// --- ports.go / plan validation ---------------------------------------------------

func TestPlan_ValidateFailClosed(t *testing.T) {
	base := func() Plan {
		return Plan{RunID: "r", Agent: "a", Nodes: []PlanNode{{TaskID: "x"}}, Edges: nil}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("plano valido: %v", err)
	}
	bad := []Plan{
		{RunID: "", Agent: "a", Nodes: []PlanNode{{TaskID: "x"}}},                                  // run vazio
		{RunID: "r", Agent: "", Nodes: []PlanNode{{TaskID: "x"}}},                                  // agente vazio
		{RunID: "r", Agent: "a", Nodes: []PlanNode{{TaskID: ""}}},                                  // task vazio
		{RunID: "r", Agent: "a", Nodes: []PlanNode{{TaskID: "x"}, {TaskID: "x"}}},                  // duplicado
		{RunID: "r", Agent: "a", Nodes: []PlanNode{{TaskID: "x"}}, Edges: [][2]string{{"x", "x"}}}, // auto-laço
		{RunID: "r", Agent: "a", Nodes: []PlanNode{{TaskID: "x"}}, Edges: [][2]string{{"x", "y"}}}, // aresta p/ no ausente
	}
	for i, p := range bad {
		if err := p.Validate(); err != ErrInvalidPlan {
			t.Fatalf("caso %d: esperava ErrInvalidPlan, obtive %v", i, err)
		}
	}
}

func TestPlan_TopoOrderCycleFailClosed(t *testing.T) {
	p := Plan{
		RunID: "r", Agent: "a",
		Nodes: []PlanNode{{TaskID: "x"}, {TaskID: "y"}},
		Edges: [][2]string{{"x", "y"}, {"y", "x"}},
	}
	if _, err := p.TopoOrder(); err != ErrPlanCycle {
		t.Fatalf("ciclo: esperava ErrPlanCycle, obtive %v", err)
	}
	// BuildPlanCard propaga o ciclo (fail-closed).
	if _, err := BuildPlanCard(p); err != ErrPlanCycle {
		t.Fatalf("BuildPlanCard ciclo: %v", err)
	}
	// BuildPlanCard rejeita um plano invalido antes da topo.
	if _, err := BuildPlanCard(Plan{RunID: ""}); err != ErrInvalidPlan {
		t.Fatalf("BuildPlanCard invalido: %v", err)
	}
}

// --- plancard.go validation -------------------------------------------------------

func TestPlanCard_ValidateAndUnmarshalFailClosed(t *testing.T) {
	good, err := BuildPlanCard(samplePlan("r", risk.ClassGray))
	if err != nil {
		t.Fatalf("BuildPlanCard: %v", err)
	}

	// MAJOR incompatível.
	bad := good
	bad.SchemaVersion = PlanCardSchemaVersion{Major: 99}
	if err := bad.Validate(); err != ErrIncompatibleSchema {
		t.Fatalf("MAJOR incompativel: %v", err)
	}
	// RunID vazio.
	bad = good
	bad.RunID = ""
	if err := bad.Validate(); err != ErrInvalidPlanCard {
		t.Fatalf("run vazio: %v", err)
	}
	// Contagem cards != ordem.
	bad = good
	bad.Order = append([]string{}, good.Order...)
	bad.Order = bad.Order[:len(bad.Order)-1]
	if err := bad.Validate(); err != ErrInvalidPlanCard {
		t.Fatalf("contagem divergente: %v", err)
	}
	// Agregado irreversível sem nenhum card irreversível.
	bad = good
	bad.AggregateIrreversible = true
	if err := bad.Validate(); err != ErrInvalidPlanCard {
		t.Fatalf("agregado irreversivel incoerente: %v", err)
	}

	// UnmarshalJSON rejeita schema malformado.
	var pc PlanCard
	if err := pc.UnmarshalJSON([]byte(`{"schema_version":"nao-semver"}`)); err != ErrInvalidSchemaVersion {
		t.Fatalf("unmarshal schema malformado: %v", err)
	}
	// UnmarshalJSON rejeita JSON invalido.
	if err := pc.UnmarshalJSON([]byte(`{`)); err == nil {
		t.Fatal("unmarshal JSON invalido: esperava erro")
	}

	// parseClass fail-closed (danger para desconhecido) + safe.
	safeCard, err := BuildPlanCard(samplePlan("r2", risk.ClassSafe))
	if err != nil {
		t.Fatalf("BuildPlanCard safe: %v", err)
	}
	if safeCard.AggregateClass != risk.ClassSafe {
		t.Fatalf("classe agregada safe: %v", safeCard.AggregateClass)
	}
}

// --- decision.go ------------------------------------------------------------------

func TestVerdict_StringAndApprovedAndRevisedPlanPassthrough(t *testing.T) {
	if VerdictReject.String() != "reject" || VerdictApprove.String() != "approve" ||
		VerdictEdit.String() != "edit" || Verdict(99).String() != "reject" {
		t.Fatal("Verdict.String")
	}
	if VerdictReject.Approved() || !VerdictApprove.Approved() || !VerdictEdit.Approved() {
		t.Fatal("Verdict.Approved")
	}
	// RevisedPlan de um veredicto não-edit devolve o original inalterado.
	orig := samplePlan("r", risk.ClassGray)
	got := PlanDecision{Verdict: VerdictApprove}.RevisedPlan(orig)
	if len(got.Nodes) != len(orig.Nodes) || got.RunID != orig.RunID {
		t.Fatal("RevisedPlan passthrough (approve)")
	}
}

// --- gate.go error branches -------------------------------------------------------

func TestNewPlanGate_FailClosedDeps(t *testing.T) {
	ch := &spyChannel{}
	if _, err := NewPlanGate(nil, ch); err != ErrNilOracle {
		t.Fatalf("oracle nil: %v", err)
	}
	if _, err := NewPlanGate(fakeOracle{}, nil); err != ErrNilChannel {
		t.Fatalf("channel nil: %v", err)
	}
}

func TestApprove_FailClosedPaths(t *testing.T) {
	ctx := context.Background()

	// Plano inválido → reject + erro.
	g1, _ := NewPlanGate(fakeOracle{level: autonomy.L0}, &spyChannel{})
	dec, err := g1.Approve(ctx, Plan{RunID: ""})
	if err != ErrInvalidPlan || dec.Verdict != VerdictReject {
		t.Fatalf("plano invalido: dec=%s err=%v", dec.Verdict, err)
	}

	// Canal recusa → reject.
	g2, _ := NewPlanGate(fakeOracle{level: autonomy.L0}, &spyChannel{approved: false})
	dec, err = g2.Approve(ctx, samplePlan("r", risk.ClassGray))
	if err != nil || dec.Verdict != VerdictReject {
		t.Fatalf("canal recusa: dec=%s err=%v", dec.Verdict, err)
	}

	// Canal erra → reject (fail-closed).
	g3, _ := NewPlanGate(fakeOracle{level: autonomy.L0}, &spyChannel{err: errors.New("canal em baixo")})
	dec, _ = g3.Approve(ctx, samplePlan("r", risk.ClassGray))
	if dec.Verdict != VerdictReject {
		t.Fatalf("canal erra: dec=%s", dec.Verdict)
	}

	// Reviewer rejeita → reject sem chamar o canal.
	ch := &spyChannel{approved: true}
	rej := &fakeReviewer{dec: PlanDecision{Verdict: VerdictReject, Approver: "human"}}
	g4, _ := NewPlanGate(fakeOracle{level: autonomy.L0}, ch, WithReviewer(rej))
	dec, _ = g4.Approve(ctx, samplePlan("r", risk.ClassGray))
	if dec.Verdict != VerdictReject {
		t.Fatalf("reviewer rejeita: dec=%s", dec.Verdict)
	}
	if ch.callCount() != 0 {
		t.Fatalf("canal chamado apos rejeicao do reviewer: %d", ch.callCount())
	}

	// Reviewer erra → reject (fail-closed).
	rerr := &fakeReviewer{err: errors.New("reviewer em baixo")}
	g5, _ := NewPlanGate(fakeOracle{level: autonomy.L0}, &spyChannel{approved: true}, WithReviewer(rerr))
	dec, err = g5.Approve(ctx, samplePlan("r", risk.ClassGray))
	if err == nil || dec.Verdict != VerdictReject {
		t.Fatalf("reviewer erra: dec=%s err=%v", dec.Verdict, err)
	}

	// Reviewer approve (não edit) → o canal assina e o veredicto fica approve.
	appr := &fakeReviewer{dec: PlanDecision{Verdict: VerdictApprove}}
	g6, _ := NewPlanGate(fakeOracle{level: autonomy.L0}, &spyChannel{approved: true, approver: "h"}, WithReviewer(appr))
	dec, _ = g6.Approve(ctx, samplePlan("r", risk.ClassGray))
	if dec.Verdict != VerdictApprove || dec.Approver != "h" {
		t.Fatalf("reviewer approve: dec=%s approver=%q", dec.Verdict, dec.Approver)
	}
}

// --- spawn_guard.go ---------------------------------------------------------------

func TestSpawnGuard_NilAndIsApproved(t *testing.T) {
	if _, err := NewSpawnGuard(nil); err != ErrNilSpawner {
		t.Fatalf("spawner nil: %v", err)
	}
	g, err := NewSpawnGuard(&spySpawner{})
	if err != nil {
		t.Fatalf("NewSpawnGuard: %v", err)
	}
	if g.IsApproved("r") {
		t.Fatal("IsApproved antes de marcar: esperava false")
	}
	g.markApproved("r")
	if !g.IsApproved("r") {
		t.Fatal("IsApproved apos marcar: esperava true")
	}
}

// planConfirmationRequest usa "unknown" quando o domínio do plano é vazio.
func TestPlanConfirmationRequest_UnknownDomain(t *testing.T) {
	p := Plan{RunID: "r", Agent: "a", Nodes: []PlanNode{{TaskID: "x", Class: risk.ClassDanger}}}
	req := planConfirmationRequest(p)
	if req.Capability != "plan:"+autonomy.DomainUnknown {
		t.Fatalf("capability: %q", req.Capability)
	}
	if req.Class != risk.ClassDanger || req.Principal != "a" || req.Resource != "r" {
		t.Fatalf("req agregado inesperado: %+v", req)
	}
}
