package planvalidate

import (
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// AOS-542 (emenda de 2026-10-20 ao ADR-034, decisão do dono): com o anúncio do nó executor
// (Snapshot.ConsumerFirstTurnTrusted), a regra (P4) deixa um consumidor de material untrusted ter
// tools de EFEITO — a fronteira passa a ser o primeiro turno do run, imposta pelo nó. Sem o
// anúncio, recusa como antes. O resto da direita da resolução continua fail-closed.

func aos542Snapshot(primeiroTurno bool) Snapshot {
	s := verifierSnapshot()
	s.ConsumerFirstTurnTrusted = primeiroTurno
	return s
}

func TestAOS542_P4RelaxadaSoComOAnuncio(t *testing.T) {
	for nome, tools := range map[string][]plan.ToolRef{"egress": {egressTool()}, "efeito": {effectTool()}} {
		doc := payloadDoc([]plan.PayloadEdge{{From: "src", Output: "resumo", Type: plan.PayloadSummary}}, tools)
		mustBeShapeValid(t, doc)
		// Sem o anúncio: a regra de antes.
		v := Validate(doc, aos542Snapshot(false), Ceilings{})
		if !v.Rejected() || v.Rule != plannerevents.RuleSchema || v.Reason != ReasonConsumesTaintAuthority {
			t.Fatalf("%s sem o anuncio: tinha de recusar por P4, veio %+v", nome, v)
		}
		// Com o anúncio: aceita.
		if v := Validate(doc, aos542Snapshot(true), Ceilings{}); v.Rejected() {
			t.Fatalf("%s com o anuncio: tinha de aceitar, veio %+v", nome, v)
		}
	}
}

// Uma tool que não resolve no snapshot continua a recusar o plano com o anúncio (fail-closed): o
// anúncio relaxa o EFEITO de uma tool conhecida, nunca a dúvida sobre o que a tool é.
func TestAOS542_ToolDesconhecidaContinuaARecusar(t *testing.T) {
	fantasma := plan.ToolRef{Name: "fantasma", Version: "1.0.0", Digest: "sha256:x"}
	doc := payloadDoc([]plan.PayloadEdge{{From: "src", Output: "resumo", Type: plan.PayloadSummary}}, []plan.ToolRef{fantasma})
	if v := Validate(doc, aos542Snapshot(true), Ceilings{}); !v.Rejected() {
		t.Fatalf("tool desconhecida com o anuncio: tinha de recusar, veio %+v", v)
	}
	n := plan.Node{NodeID: "x", Tools: []plan.ToolRef{fantasma}}
	if !privilegedAuthority(n, aos542Snapshot(true).index(), true) {
		t.Fatal("tool desconhecida com o anuncio deixou de contar como privilegiada")
	}
}

// O anúncio não entra no digest nem no hash do snapshot: não é conteúdo do catálogo.
func TestAOS542_AnuncioForaDoHash(t *testing.T) {
	a, b := aos542Snapshot(false), aos542Snapshot(true)
	if a.Hash != b.Hash {
		t.Fatalf("o anuncio mudou o hash: %q vs %q", a.Hash, b.Hash)
	}
}
