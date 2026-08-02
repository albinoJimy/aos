package planapproval

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/control-plane/governance/autonomy"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// TEST (AOS-236 / CA1) PAPÉIS, TOOLS POR PAPEL: o plan-card expõe uma vista dedicada que
// AGRUPA as tools (capabilities) sob cada PAPEL (Requester do nó), com os task_ids e a
// classe mais severa do papel — não uma lista plana de nós. Determinista: papéis e
// capabilities ordenados, task_ids na ordem topológica.
func TestRolesView_GroupsToolsByRole(t *testing.T) {
	plan := Plan{
		RunID: "run-roles", Agent: "agent:planner", Domain: "http",
		Nodes: []PlanNode{
			// Papel "agent:reader" exerce DUAS tools distintas (http.get + fs.read).
			{TaskID: "a", Agent: "agent:reader", Class: risk.ClassSafe, Capability: "http.get", Preview: "p", Resource: "https://x/a"},
			{TaskID: "b", Agent: "agent:reader", Class: risk.ClassGray, Capability: "fs.read", Preview: "p", Resource: "/tmp/b"},
			// Repete a tool http.get sob o MESMO papel → deve deduplicar.
			{TaskID: "b2", Agent: "agent:reader", Class: risk.ClassSafe, Capability: "http.get", Preview: "p", Resource: "https://x/b2"},
			// Papel "agent:writer" exerce uma tool danger.
			{TaskID: "c", Agent: "agent:writer", Class: risk.ClassDanger, Capability: "http.post", Preview: "p", Resource: "https://x/c"},
		},
		Edges: [][2]string{{"a", "b"}, {"a", "b2"}, {"a", "c"}},
	}
	card, err := BuildPlanCard(plan)
	if err != nil {
		t.Fatalf("BuildPlanCard: %v", err)
	}

	roles := card.RolesView()
	if len(roles) != 2 {
		t.Fatalf("papeis: %d (esperava 2 — reader + writer)", len(roles))
	}
	// Ordem lexicográfica dos papéis: reader < writer.
	if roles[0].Role != "agent:reader" || roles[1].Role != "agent:writer" {
		t.Fatalf("ordem dos papeis: %q,%q (esperava agent:reader,agent:writer)", roles[0].Role, roles[1].Role)
	}

	reader := roles[0]
	// Tools DISTINTAS e ORDENADAS (http.get deduplicado apesar de dois nós o exercerem).
	if len(reader.Capabilities) != 2 || reader.Capabilities[0] != "fs.read" || reader.Capabilities[1] != "http.get" {
		t.Fatalf("tools do papel reader: %v (esperava [fs.read http.get] deduplicado/ordenado)", reader.Capabilities)
	}
	// A classe do papel é a mais severa dos seus nós (gray, de "b").
	if reader.WorstClass != risk.ClassGray {
		t.Fatalf("classe do papel reader: %v (esperava gray)", reader.WorstClass)
	}
	// Task_ids na ordem topológica do card (a, b, b2 — todos do reader).
	if len(reader.TaskIDs) != 3 {
		t.Fatalf("task_ids do papel reader: %v (esperava 3 — a,b,b2)", reader.TaskIDs)
	}

	writer := roles[1]
	if len(writer.Capabilities) != 1 || writer.Capabilities[0] != "http.post" {
		t.Fatalf("tools do papel writer: %v (esperava [http.post])", writer.Capabilities)
	}
	if writer.WorstClass != risk.ClassDanger {
		t.Fatalf("classe do papel writer: %v (esperava danger)", writer.WorstClass)
	}
	if len(writer.TaskIDs) != 1 || writer.TaskIDs[0] != "c" {
		t.Fatalf("task_ids do papel writer: %v (esperava [c])", writer.TaskIDs)
	}
}

// TEST (AOS-236 / CA2 / AOS-120) DUAL-CONTROL POR-EFEITO IMPOSTO INLINE: com
// [WithPerEffectDualControl], um nó danger IRREVERSÍVEL só deixa o plano ser aprovado se o
// seu efeito obtiver dois aprovadores DISTINTOS — imposto INLINE no Approve, ANTES da
// decisão agregada assinada, sem round-trip ao LLM. FALHA-ANTES: sem a opção, um ÚNICO
// aprovador (o 4-eyes agregado do canal) aprova o mesmo plano danger.
func TestPerEffectDualControl_EnforcedInlineForDangerNode(t *testing.T) {
	ctx := context.Background()

	dangerPlan := func(runID string) Plan {
		p := samplePlan(runID, risk.ClassGray) // base gray; SÓ "d" é o efeito danger irreversível
		p.Nodes[3].Class = risk.ClassDanger
		p.Nodes[3].Irreversible = true
		p.Nodes[3].Capability = "http.post"
		p.Nodes[3].Preview = "cap:http.post -> https://x/delete-all"
		p.Nodes[3].Resource = "https://x/delete-all"
		return p
	}

	// --- FALHA-ANTES: SEM a opção, um único aprovador (4-eyes agregado) aprova o plano
	// danger — o dual-control por-efeito NÃO é imposto inline. ---
	chSolo, _ := realChannel(t, []approvalStep{{approver: "solo", approved: true}}, map[string]byte{"solo": 7})
	gateOff, err := NewPlanGate(fakeOracle{level: autonomy.L3}, chSolo)
	if err != nil {
		t.Fatalf("NewPlanGate: %v", err)
	}
	decOff, err := gateOff.Approve(ctx, dangerPlan("run-pedc-off"))
	if err != nil {
		t.Fatalf("FALHA-ANTES Approve: %v", err)
	}
	if decOff.Verdict != VerdictApprove {
		t.Fatalf("FALHA-ANTES: sem imposicao por-efeito, um so aprovador devia aprovar; verdict=%s", decOff.Verdict)
	}

	// --- COM a opção mas SÓ UM aprovador disponível: o efeito danger irreversível não
	// atinge o quórum de dois aprovadores DISTINTOS → RECUSA fail-closed, ANTES de assinar
	// a decisão agregada. ---
	chOne, _ := realChannel(t,
		[]approvalStep{{approver: "approver-1", approved: true}},
		map[string]byte{"approver-1": 11, "approver-2": 22})
	gateShort, err := NewPlanGate(fakeOracle{level: autonomy.L3}, chOne, WithPerEffectDualControl())
	if err != nil {
		t.Fatalf("NewPlanGate: %v", err)
	}
	decShort, err := gateShort.Approve(ctx, dangerPlan("run-pedc-short"))
	if !errors.Is(err, ErrEffectDualControlFailed) {
		t.Fatalf("efeito sem quorum: esperava ErrEffectDualControlFailed, obtive %v", err)
	}
	if decShort.Verdict != VerdictReject {
		t.Fatalf("efeito sem quorum: verdict=%s (esperava reject)", decShort.Verdict)
	}

	// --- COM a opção e DOIS aprovadores distintos (+ um para a decisão agregada): o efeito
	// obtém dual-control por-efeito e o plano é aprovado. ---
	chTwo, store := realChannel(t,
		[]approvalStep{
			{approver: "approver-1", approved: true}, // efeito danger: 1º aprovador
			{approver: "approver-2", approved: true}, // efeito danger: 2º aprovador DISTINTO
			{approver: "approver-1", approved: true}, // decisão agregada assinada
		},
		map[string]byte{"approver-1": 11, "approver-2": 22})
	gateOK, err := NewPlanGate(fakeOracle{level: autonomy.L3}, chTwo, WithPerEffectDualControl())
	if err != nil {
		t.Fatalf("NewPlanGate: %v", err)
	}
	decOK, err := gateOK.Approve(ctx, dangerPlan("run-pedc-ok"))
	if err != nil {
		t.Fatalf("Approve (dual-control por-efeito satisfeito): %v", err)
	}
	if decOK.Verdict != VerdictApprove {
		t.Fatalf("dual-control por-efeito satisfeito: verdict=%s (esperava approve)", decOK.Verdict)
	}
	// A decisão agregada foi assinada/selada pelo hitl.Channel REAL (composição AOS-095).
	head, herr := store.Head(ctx, "hitl:agent:planner")
	if herr != nil || head == 0 {
		t.Fatalf("a decisao agregada nao foi selada no audit (head=%d, err=%v)", head, herr)
	}
}
