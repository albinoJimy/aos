package agentruntime

// aos440_titular_test.go — O TITULAR DOS DADOS É SEPARADO DE QUEM CHAMA (AOS-440).
//
// Uma regra, e três perguntas que a usam: a captura do turno, o Principal de cada tool call (que
// leva o titular pela via durável até ao step-ledger) e o registo de retoma. O teste fixa a regra e
// o que ela NÃO muda: o NHIID continua a ser o produtor dos eventos.

import (
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

func TestAOS440TitularDoGoal(t *testing.T) {
	g := Goal{Principal: referencemonitor.Principal{NHIID: "drenador"}}
	if g.Titular() != "drenador" {
		t.Fatalf("sem Subject o titular e o NHIID (compativel com todos os runs anteriores): %q", g.Titular())
	}
	if p := g.callPrincipal(); p.Subject != "drenador" || p.Titular() != "drenador" {
		t.Fatalf("o Principal da call leva o titular derivado: %+v", p)
	}

	g.Subject = "sub-alice"
	if g.Titular() != "sub-alice" {
		t.Fatalf("com Subject, o titular e o Subject: %q", g.Titular())
	}
	p := g.callPrincipal()
	if p.Subject != "sub-alice" || p.Titular() != "sub-alice" {
		t.Fatalf("o Principal da call tem de levar o titular dos dados: %+v", p)
	}
	if p.NHIID != "drenador" {
		t.Fatalf("o NHIID (quem chamou, produtor dos eventos) nao muda: %q", p.NHIID)
	}
	if g.Principal.Subject != "" {
		t.Fatal("callPrincipal nao pode mutar o Principal do Goal")
	}
}
