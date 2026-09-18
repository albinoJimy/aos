package planapproval

// AOS-408 — uma LACUNA DE CAPACIDADE nunca auto-aprova.
//
// FALHA-ANTES: a auto-aprovação lia só (nível, classe agregada) pela [autonomy.Oversight], que não
// conhece o gap. Um plano com um nó cuja capability não está concedida — mas com classe `safe` —
// auto-aprovava a nível alto, sem o canal ser chamado e sem humano nenhum ver o cartão. O contrato
// do campo diz o contrário («EXIGE revisão item-a-item, não colapsável») e a triagem do cartão já
// o forçava: era a decisão de auto-aprovação que via um critério e não o outro.

import (
	"context"
	"testing"

	"github.com/aos-ref/control-plane/governance/autonomy"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// aos408PlanoComGap é um plano cuja classe agregada é `safe` (nada a gatar pelo risco) e que tem
// UMA lacuna de capacidade. É a combinação exacta que passava.
func aos408PlanoComGap(gap bool) Plan {
	return Plan{
		RunID:  "run-aos408-gap",
		Agent:  "agt-planner",
		Domain: "fs",
		Nodes: []PlanNode{
			{TaskID: "n1", Class: risk.ClassSafe, Preview: "fs.read -> doc://notes", Capability: "cap:tool:fs.read"},
			{TaskID: "n2", Class: risk.ClassSafe, Preview: "skill inexistente", Capability: "cap:tool:resumir", CapabilityGap: gap},
		},
		Edges: [][2]string{{"n1", "n2"}},
	}
}

func TestAOS408_LacunaDeCapacidadeNaoAutoAprova(t *testing.T) {
	canal := &spyChannel{approved: false, approver: "human:alice"}
	// L5 é o nível mais alto: se houvesse caminho de auto-aprovação, era aqui.
	gate, err := NewPlanGate(fakeOracle{level: autonomy.L5}, canal)
	if err != nil {
		t.Fatalf("NewPlanGate: %v", err)
	}

	dec, err := gate.Approve(context.Background(), aos408PlanoComGap(true))
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if dec.AutoApproved {
		t.Fatal("um plano com lacuna de capacidade NÃO pode auto-aprovar, mesmo a L5 e com classe safe")
	}
	if canal.callCount() != 1 {
		t.Fatalf("a decisão tinha de ser devolvida ao canal (humano): chamadas=%d", canal.callCount())
	}
	if dec.Verdict != VerdictReject {
		t.Fatalf("o canal recusou; veredicto = %v", dec.Verdict)
	}

	// CONTRAPROVA de não-tautologia: o MESMO plano sem o gap auto-aprova e o canal nem é
	// chamado. Sem esta metade, um gate que recusasse tudo passaria o teste de cima.
	canalSemGap := &spyChannel{approved: false}
	gateSemGap, err := NewPlanGate(fakeOracle{level: autonomy.L5}, canalSemGap)
	if err != nil {
		t.Fatalf("NewPlanGate: %v", err)
	}
	decSemGap, err := gateSemGap.Approve(context.Background(), aos408PlanoComGap(false))
	if err != nil {
		t.Fatalf("Approve sem gap: %v", err)
	}
	if !decSemGap.AutoApproved || decSemGap.Verdict != VerdictApprove {
		t.Fatalf("sem gap e a L5 o plano safe auto-aprova: auto=%v veredicto=%v", decSemGap.AutoApproved, decSemGap.Verdict)
	}
	if canalSemGap.callCount() != 0 {
		t.Fatalf("auto-aprovação não chama o canal: chamadas=%d", canalSemGap.callCount())
	}
}

// TestAOS408_LacunaNaoAutoAprovaMesmoSozinha isola o predicado: um plano de UM nó, safe, com gap.
func TestAOS408_LacunaNaoAutoAprovaMesmoSozinha(t *testing.T) {
	if !temLacunaDeCapacidade([]PlanNode{{TaskID: "n1", Class: risk.ClassSafe, CapabilityGap: true}}) {
		t.Fatal("o predicado tem de ver um gap num plano de um nó")
	}
	if temLacunaDeCapacidade([]PlanNode{{TaskID: "n1", Class: risk.ClassDanger}}) {
		t.Fatal("classe danger não é lacuna de capacidade — são dois critérios distintos")
	}
	if temLacunaDeCapacidade(nil) {
		t.Fatal("plano sem nós não tem lacuna")
	}
}
