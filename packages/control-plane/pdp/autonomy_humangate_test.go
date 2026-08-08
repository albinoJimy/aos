package pdp

import (
	"context"
	"testing"

	"github.com/aos-ref/control-plane/governance/autonomy"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// TestAutonomyHumanGateSatisfiedNaoVoltaAEscalar é o fecho do ciclo de AOS-021. Sem isto o
// bridge é circular: o oráculo exige gate humano → o run suspende → o humano aprova → a
// retoma re-media → o oráculo exige gate humano OUTRA VEZ. Aprovar nunca satisfaria quem
// exigiu a aprovação, e o run escalaria para sempre.
func TestAutonomyHumanGateSatisfiedNaoVoltaAEscalar(t *testing.T) {
	oracle := stubOracle{{"nhi-H", "fs"}: autonomy.L4}
	p := openWithOracle(t, oracle)

	// Sem aprovação: danger a L4 exige confirmação individual ⇒ escalate.
	in := fsReadPermit("nhi-H", "danger")
	dec, err := p.Decide(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Effect != Escalate {
		t.Fatalf("pré-condição: sem gate humano, danger a L4 escala; obtive %s", dec.Effect)
	}

	// COM a prova verificada do gate humano: mantém o permit de BASE.
	in.Context.HumanGateSatisfied = true
	dec, err = p.Decide(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Effect != Permit {
		t.Fatalf("com o gate humano CUMPRIDO o efeito tem de ser o permit de base; obtive %s (%s)", dec.Effect, dec.Reason)
	}
	// A obligation de autonomia continua lá — a decisão é auditável, incluindo o facto de
	// que o oversight foi cumprido por um humano e não dispensado.
	ob := findObligation(dec, obligationAutonomy)
	if ob == nil {
		t.Fatal("a obligation de autonomia tem de continuar a ser emitida (auditabilidade)")
	}
	if ob.Params["oversight"] != "confirm" || ob.Params["human_gate"] != "satisfied" {
		t.Fatalf("a obligation tem de dizer QUE oversight era exigido e que foi cumprido: %+v", ob.Params)
	}
}

// TestAutonomyHumanGateNaoConverteDeny: a prova de aprovação humana remove UM obstáculo (o
// oversight de autonomia). Não é um permit e não afrouxa mais nada — um deny da política
// base continua deny mesmo com o gate humano cumprido.
func TestAutonomyHumanGateNaoConverteDeny(t *testing.T) {
	oracle := stubOracle{{"nhi-I", "fs"}: autonomy.L5}
	p := openWithOracle(t, oracle)
	dec, err := p.Decide(context.Background(), Input{
		Principal:  Principal{ID: "nhi-I", AgentClass: "agent-worker", Authority: []string{"cap:fs.delete"}},
		Capability: "cap:fs.delete", // fora da allowlist da classe ⇒ deny de base
		Resource:   Resource{Type: "file", Value: "/etc/x"},
		Context:    DecisionContext{Taint: "trusted", RiskClass: "danger", HumanGateSatisfied: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Effect != Deny {
		t.Fatalf("uma aprovacao humana NAO converte deny em permit; obtive %s", dec.Effect)
	}
}

// TestAutonomyHumanGateIrrelevanteSemGate: quando o oversight NÃO exige humano, a prova não
// muda nada (nem o efeito nem a razão ganham um ramo especial).
func TestAutonomyHumanGateIrrelevanteSemGate(t *testing.T) {
	oracle := stubOracle{{"nhi-J", "fs"}: autonomy.L5}
	p := openWithOracle(t, oracle)
	semProva := fsReadPermit("nhi-J", "safe")
	comProva := fsReadPermit("nhi-J", "safe")
	comProva.Context.HumanGateSatisfied = true

	a, err := p.Decide(context.Background(), semProva)
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.Decide(context.Background(), comProva)
	if err != nil {
		t.Fatal(err)
	}
	if a.Effect != Permit || b.Effect != a.Effect || a.Reason != b.Reason {
		t.Fatalf("sem gate exigido a prova e IRRELEVANTE: %s/%q vs %s/%q", a.Effect, a.Reason, b.Effect, b.Reason)
	}
}

// TestAutonomy_ChaveDoVeredictoNaoDiverge sela o contrato entre as duas camadas. O PDP
// emite o veredicto e o Reference Monitor impõe-no; se as duas chaves divergissem, o PEP
// deixaria de o encontrar e — fail-closed — passaria a negar TUDO o que o oráculo permite.
// A igualdade é verificada, não assumida: é exactamente o tipo de acoplamento por string
// que já divergiu uma vez neste caminho.
func TestAutonomy_ChaveDoVeredictoNaoDiverge(t *testing.T) {
	if paramRequiresHuman != referencemonitor.ParamAutonomyRequiresHuman {
		t.Fatalf("a chave do veredicto divergiu entre PDP (%q) e PEP (%q)",
			paramRequiresHuman, referencemonitor.ParamAutonomyRequiresHuman)
	}
	if obligationAutonomy != referencemonitor.ObligationAutonomy {
		t.Fatalf("o tipo da obligation divergiu: PDP %q vs PEP %q",
			obligationAutonomy, referencemonitor.ObligationAutonomy)
	}
}
