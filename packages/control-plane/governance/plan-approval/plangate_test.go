package planapproval

import (
	"context"
	"encoding/json"
	"testing"

	approvalcard "github.com/aos-ref/control-plane/governance/approval-card"
	"github.com/aos-ref/control-plane/governance/autonomy"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// TEST (a) PRÉ-SPAWN: NENHUM sub-agente é lançado antes da aprovação do plano. O Spawner
// spy prova ZERO Spawn antes de Approve devolver Approve; o SpawnGuard RECUSA Spawn de
// run não-aprovado ([ErrPlanNotApproved]). (AC1)
func TestPreSpawn_NoSpawnBeforePlanApproval(t *testing.T) {
	ctx := context.Background()
	runID := "run-A"
	spy := &spySpawner{}
	guard, err := NewSpawnGuard(spy)
	if err != nil {
		t.Fatalf("NewSpawnGuard: %v", err)
	}

	// ANTES de qualquer aprovação: o guard recusa o spawn e o spy fica a ZERO.
	if err := guard.Spawn(ctx, runID); err != ErrPlanNotApproved {
		t.Fatalf("Spawn de run nao-aprovado: esperava ErrPlanNotApproved, obtive %v", err)
	}
	if spy.count() != 0 {
		t.Fatalf("custo de tokens adiantado: %d spawns antes da aprovacao (esperava 0)", spy.count())
	}

	// L0 força o gate humano (Oversight(L0, safe)=suggest). Canal aprova.
	ch := &spyChannel{approved: true, approver: "human-1"}
	gate, err := NewPlanGate(fakeOracle{level: autonomy.L0}, ch, WithSpawnGuard(guard))
	if err != nil {
		t.Fatalf("NewPlanGate: %v", err)
	}

	// Um run DIFERENTE, não aprovado, continua recusado (o guard é por-run).
	if err := guard.Spawn(ctx, "run-outro"); err != ErrPlanNotApproved {
		t.Fatalf("run diferente: esperava ErrPlanNotApproved, obtive %v", err)
	}
	if spy.count() != 0 {
		t.Fatalf("spawns antes da aprovacao: %d (esperava 0)", spy.count())
	}

	dec, err := gate.Approve(ctx, samplePlan(runID, risk.ClassSafe))
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if dec.Verdict != VerdictApprove {
		t.Fatalf("veredicto: %s (esperava approve)", dec.Verdict)
	}
	if ch.callCount() == 0 {
		t.Fatal("gate humano: o canal NAO foi chamado num plano gatado (L0)")
	}

	// SÓ APÓS Approve==Approve o spawn desse run é libertado.
	if err := guard.Spawn(ctx, runID); err != nil {
		t.Fatalf("Spawn apos aprovacao: %v", err)
	}
	if spy.count() != 1 {
		t.Fatalf("spawns apos aprovacao: %d (esperava 1)", spy.count())
	}
	// O outro run, nunca aprovado, permanece recusado (fail-closed).
	if err := guard.Spawn(ctx, "run-outro"); err != ErrPlanNotApproved {
		t.Fatalf("run nao-aprovado apos aprovacao doutro: esperava ErrPlanNotApproved, obtive %v", err)
	}
}

// TEST (b) EDIÇÃO: uma decisão Edit devolve RevisedNodes/RevisedEdges (grafo podado/
// reordenado) que o "orquestrador" (fake) reconstrói num novo DAG. (AC2)
func TestEdit_RevisedPlanReturnedToOrchestrator(t *testing.T) {
	ctx := context.Background()
	plan := samplePlan("run-B", risk.ClassGray)

	// O humano PODA o nó "c" e reordena: mantém a→b→d, remove c.
	revised := PlanDecision{
		Verdict:  VerdictEdit,
		Approver: "human-editor",
		RevisedNodes: []PlanNode{
			{TaskID: "a", Class: risk.ClassGray, Capability: "http.get", Preview: "cap:http.get -> https://x/a", Resource: "https://x/a"},
			{TaskID: "b", Class: risk.ClassGray, Capability: "http.get", Preview: "cap:http.get -> https://x/b", Resource: "https://x/b"},
			{TaskID: "d", Class: risk.ClassGray, Capability: "http.get", Preview: "cap:http.get -> https://x/d", Resource: "https://x/d"},
		},
		RevisedEdges: [][2]string{{"a", "b"}, {"b", "d"}},
	}
	reviewer := &fakeReviewer{dec: revised}
	ch := &spyChannel{approved: true, approver: "human-editor"} // assina a revisão (não-repúdio)

	gate, err := NewPlanGate(fakeOracle{level: autonomy.L1}, ch, WithReviewer(reviewer))
	if err != nil {
		t.Fatalf("NewPlanGate: %v", err)
	}
	dec, err := gate.Approve(ctx, plan)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if dec.Verdict != VerdictEdit {
		t.Fatalf("veredicto: %s (esperava edit)", dec.Verdict)
	}
	if len(dec.RevisedNodes) != 3 || len(dec.RevisedEdges) != 2 {
		t.Fatalf("grafo revisto: %d nos / %d arestas (esperava 3/2)", len(dec.RevisedNodes), len(dec.RevisedEdges))
	}

	// O "orquestrador" fake RECONSTRÓI o DAG a partir do plano revisto e verifica que o
	// nó podado desapareceu e a ordem topológica é a esperada (a,b,d).
	rebuilt := dec.RevisedPlan(plan)
	if err := rebuilt.Validate(); err != nil {
		t.Fatalf("plano revisto invalido: %v", err)
	}
	order, err := rebuilt.TopoOrder()
	if err != nil {
		t.Fatalf("TopoOrder do plano revisto: %v", err)
	}
	if len(order) != 3 || order[0] != "a" || order[1] != "b" || order[2] != "d" {
		t.Fatalf("ordem reconstruida: %v (esperava [a b d])", order)
	}
	for _, id := range order {
		if id == "c" {
			t.Fatal("no podado 'c' sobreviveu a reconstrucao")
		}
	}
}

// TEST (c) SEPARAÇÃO: o PlanCard opera sobre o GRAFO (multi-nó + arestas), distinto do
// ApprovalCard por-acção. Prova domínios/schemas distintos (aos.plan.card.v1 !=
// aos.approval.card.v1) e round-trip estável. (AC3)
func TestSeparation_PlanCardOverGraphDistinctFromActionCard(t *testing.T) {
	if SchemaDomain == approvalcard.SchemaDomain {
		t.Fatalf("dominios coincidem: %q == %q (deviam ser distintos)", SchemaDomain, approvalcard.SchemaDomain)
	}
	if SchemaDomain != "aos.plan.card.v1" || approvalcard.SchemaDomain != "aos.approval.card.v1" {
		t.Fatalf("dominios inesperados: plan=%q action=%q", SchemaDomain, approvalcard.SchemaDomain)
	}

	plan := samplePlan("run-C", risk.ClassGray)
	// Um nó irreversível para provar a agregação.
	plan.Nodes[3].Irreversible = true

	card, err := BuildPlanCard(plan, WithEstimatedCost(1000, 42))
	if err != nil {
		t.Fatalf("BuildPlanCard: %v", err)
	}
	if card.NodeCount() != 4 {
		t.Fatalf("node_count: %d (esperava 4 — multi-no, nao uma tool call)", card.NodeCount())
	}
	if len(card.NodeCards) != 4 {
		t.Fatalf("um card por no: %d (esperava 4)", len(card.NodeCards))
	}
	if len(card.Edges) != 3 {
		t.Fatalf("arestas: %d (esperava 3 — o card opera sobre topologia)", len(card.Edges))
	}
	if !card.AggregateIrreversible {
		t.Fatal("agregado irreversivel: esperava true (no 'd' e irreversivel)")
	}
	// Cada card por-nó É um approvalcard.ApprovalCard (o efeito por-acção de AOS-120).
	var _ []approvalcard.ApprovalCard = card.NodeCards
	// A ordem é topológica estável.
	if card.Order[0] != "a" {
		t.Fatalf("ordem topologica: %v (esperava comecar em 'a')", card.Order)
	}

	// Round-trip estável: marshal → unmarshal preserva a estrutura e valida fail-closed.
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back PlanCard
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.NodeCount() != 4 || len(back.NodeCards) != 4 || back.RunID != "run-C" {
		t.Fatalf("round-trip divergente: %+v", back)
	}
	if back.AggregateClass != risk.ClassGray {
		t.Fatalf("classe agregada apos round-trip: %v (esperava gray)", back.AggregateClass)
	}

	// O wire carimba o schema_version e a contagem de nós.
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("wire: %v", err)
	}
	if wire["schema_version"] != "1.1.0" {
		t.Fatalf("schema_version no wire: %v (esperava 1.1.0)", wire["schema_version"])
	}
	if wire["node_count"].(float64) != 4 {
		t.Fatalf("node_count no wire: %v (esperava 4)", wire["node_count"])
	}
}

// TEST (d) NÍVEL: a L4/L5 não-danger o plano AUTO-APROVA (Oversight().Runs()==true, sem
// chamar o Channel); a L0-L3 ou danger exige o gate humano (Channel chamado). Prova que
// o gate CONSOME o nível (não o decide/promove). (AC4)
func TestLevel_AutoApproveHighLevelsElseHumanGate(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name       string
		level      autonomy.Level
		class      risk.Class
		wantAuto   bool
		wantCalled bool
	}{
		{"L4 safe auto-aprova", autonomy.L4, risk.ClassSafe, true, false},
		{"L4 gray auto-aprova", autonomy.L4, risk.ClassGray, true, false},
		{"L5 gray auto-aprova", autonomy.L5, risk.ClassGray, true, false},
		{"L4 danger exige gate", autonomy.L4, risk.ClassDanger, false, true},
		{"L3 gray exige gate", autonomy.L3, risk.ClassGray, false, true},
		{"L0 safe exige gate", autonomy.L0, risk.ClassSafe, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch := &spyChannel{approved: true, approver: "human-x"}
			gate, err := NewPlanGate(fakeOracle{level: tc.level}, ch)
			if err != nil {
				t.Fatalf("NewPlanGate: %v", err)
			}
			dec, err := gate.Approve(ctx, samplePlan("run-D", tc.class))
			if err != nil {
				t.Fatalf("Approve: %v", err)
			}
			if dec.AutoApproved != tc.wantAuto {
				t.Fatalf("auto_approved=%v (esperava %v)", dec.AutoApproved, tc.wantAuto)
			}
			if called := ch.callCount() > 0; called != tc.wantCalled {
				t.Fatalf("canal chamado=%v (esperava %v)", called, tc.wantCalled)
			}
			if dec.Verdict != VerdictApprove {
				t.Fatalf("veredicto=%s (esperava approve)", dec.Verdict)
			}
		})
	}
}

// TEST (d bis) CONSUMO, NÃO DECISÃO: o gate consulta o [autonomy.LevelRegistry] REAL e
// NUNCA lhe altera o nível (nenhum SetLevel/History novo). Prova que consome o nível de
// EPIC-09 sem o promover.
func TestLevel_ConsumesRegistryDoesNotMutate(t *testing.T) {
	ctx := context.Background()
	reg := autonomy.NewLevelRegistry()
	if _, err := reg.SetLevel(ctx, "agent:planner", "http", autonomy.L4, "setup", "admin"); err != nil {
		t.Fatalf("SetLevel: %v", err)
	}
	before := len(reg.History())

	ch := &spyChannel{approved: true, approver: "human-x"}
	gate, err := NewPlanGate(reg, ch) // o LevelRegistry satisfaz autonomy.Oracle
	if err != nil {
		t.Fatalf("NewPlanGate: %v", err)
	}
	// L4 + gray → auto-aprova (consome); o canal não é chamado.
	if _, err := gate.Approve(ctx, samplePlan("run-D2", risk.ClassGray)); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if ch.callCount() != 0 {
		t.Fatalf("canal chamado numa auto-aprovacao: %d (esperava 0)", ch.callCount())
	}
	if after := len(reg.History()); after != before {
		t.Fatalf("o gate mutou o registo de niveis: history %d -> %d (nao devia decidir/promover)", before, after)
	}
}

// TEST (e) AUDITORIA: a decisão emite o span plan_approval ligado ao run (AttrRunID),
// com node_count/autonomy_level/auto_approved/verdict/schema_version — e SEM segredos
// (o preview de nenhum nó vaza para os spans). (AC5)
func TestAudit_PlanApprovalSpanLinkedToRunNoSecrets(t *testing.T) {
	ctx := context.Background()
	tr := &agentruntime.RecordingTracer{}

	plan := samplePlan("run-E", risk.ClassGray)
	// Um segredo no preview de um nó — NUNCA pode aparecer num atributo de span.
	plan.Nodes[0].Preview = "cap:http.post -> https://x SECRET-TOKEN-XYZ"

	ch := &spyChannel{approved: true, approver: "human-audit"}
	gate, err := NewPlanGate(fakeOracle{level: autonomy.L3}, ch, WithTracer(tr))
	if err != nil {
		t.Fatalf("NewPlanGate: %v", err)
	}
	dec, err := gate.Approve(ctx, plan)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}

	spans := tr.SpansByOperation(OpPlanApproval)
	if len(spans) != 1 {
		t.Fatalf("spans plan_approval: %d (esperava 1)", len(spans))
	}
	sp := spans[0]
	if sp.Attributes[agentruntime.AttrRunID] != "run-E" {
		t.Fatalf("span nao ligado ao run: run_id=%v", sp.Attributes[agentruntime.AttrRunID])
	}
	if sp.Attributes[AttrPlanNodeCount] != 4 {
		t.Fatalf("node_count no span: %v (esperava 4)", sp.Attributes[AttrPlanNodeCount])
	}
	if sp.Attributes[AttrPlanVerdict] != dec.Verdict.String() {
		t.Fatalf("verdict no span: %v (esperava %s)", sp.Attributes[AttrPlanVerdict], dec.Verdict)
	}
	if sp.Attributes[AttrPlanAutonomyLevel] != "L3" {
		t.Fatalf("autonomy_level no span: %v (esperava L3)", sp.Attributes[AttrPlanAutonomyLevel])
	}
	if sp.Attributes[AttrPlanAutoApproved] != false {
		t.Fatalf("auto_approved no span: %v (esperava false)", sp.Attributes[AttrPlanAutoApproved])
	}
	if sp.Attributes[AttrPlanSchemaVersion] != "1.1.0" {
		t.Fatalf("schema_version no span: %v (esperava 1.1.0)", sp.Attributes[AttrPlanSchemaVersion])
	}
	if !sp.Ended {
		t.Fatal("span plan_approval nao foi fechado")
	}

	// O span de nível de AOS-089 é também emitido (nível/oversight observáveis).
	if len(tr.SpansByOperation(autonomy.OpAutonomyLevel)) != 1 {
		t.Fatalf("span aos.autonomy.level: %d (esperava 1)", len(tr.SpansByOperation(autonomy.OpAutonomyLevel)))
	}

	// SEM SEGREDOS: nenhum span carrega o preview secreto de um nó.
	for _, s := range tr.Spans() {
		if attrsContain(s.Attributes, "SECRET-TOKEN-XYZ") {
			t.Fatalf("segredo vazou para o span %q", s.Operation)
		}
	}
}

// TEST (composição hitl REAL): um plano danger gatado passa a decisão binária pelo
// [hitl.Channel] REAL, que ASSINA (ed25519), impõe 4-eyes (aprovador != solicitante) e
// SELA no audit. Prova que o plan-gate COMPÕE AOS-095 (não reimplementa o não-repúdio).
func TestGatedPlan_ComposesRealHITLChannelNonRepudiation(t *testing.T) {
	ctx := context.Background()
	// Um aprovador humano DISTINTO do solicitante (agent:planner), com autoridade danger.
	ch, store := realChannel(t, []approvalStep{{approver: "human-approver", approved: true}}, map[string]byte{"human-approver": 7})

	spy := &spySpawner{}
	guard, err := NewSpawnGuard(spy)
	if err != nil {
		t.Fatalf("NewSpawnGuard: %v", err)
	}
	// L3 + danger → gate humano (Oversight(L3, danger)=confirm).
	gate, err := NewPlanGate(fakeOracle{level: autonomy.L3}, ch, WithSpawnGuard(guard))
	if err != nil {
		t.Fatalf("NewPlanGate: %v", err)
	}

	dec, err := gate.Approve(ctx, samplePlan("run-F", risk.ClassDanger))
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if dec.Verdict != VerdictApprove {
		t.Fatalf("veredicto: %s (esperava approve)", dec.Verdict)
	}
	if dec.Approver != "human-approver" {
		t.Fatalf("aprovador (nao-repudio): %q (esperava human-approver)", dec.Approver)
	}
	// O Channel SELOU a decisão assinada no audit WORM (a prova de composição): a
	// decisão verificada sela na partição do solicitante ("hitl:<requester>").
	head, herr := store.Head(ctx, "hitl:agent:planner")
	if herr != nil || head == 0 {
		t.Fatalf("o hitl.Channel nao selou a decisao no audit (head=%d, err=%v)", head, herr)
	}
	// E só agora o spawn é libertado.
	if err := guard.Spawn(ctx, "run-F"); err != nil {
		t.Fatalf("Spawn apos aprovacao assinada: %v", err)
	}
	if spy.count() != 1 {
		t.Fatalf("spawns: %d (esperava 1)", spy.count())
	}
}
