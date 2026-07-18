package router

import (
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/routing/sovereignty"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
)

// AOS-094 — soberania por board no roteamento do Model Gateway (compõe a guarda de
// EPIC-06/AOS-058). Confirma e prova, na fronteira do GW:
//   - o failover está PROIBIDO de cruzar fronteira (fail-closed);
//   - a allowlist regional é respeitada (nenhuma chamada sai da região autorizada);
//   - a tentativa cross-border é um EVENTO AUDITÁVEL (deny + motivo de soberania +
//     região, registado no DecisionSink e no span).

// TestAOS094_CrossBorderFailover_Audited (AC3 + AC5): um board UE que perde
// capacidade (só resta um endpoint us-east) NÃO faz failover cross-border — rejeita
// fail-closed — e a rejeição é REGISTADA no sink com o candidato cross-border
// descartado e a região de origem (o evento auditável de soberania).
func TestAOS094_CrossBorderFailover_Audited(t *testing.T) {
	guard := sovereignty.NewGuard(
		sovereignty.WithBoundary("eu-west", "eu"),
		sovereignty.WithBoundary("us-east", "us"),
	)
	sink := &captureSink{}
	r := New(refLadder(),
		WithGuard(guard),
		WithAllowlist(allowAllModels()),
		WithDecisionSink(sink),
	)
	d, err := r.Route(ctx(), Request{
		Board: "board-eu", Provider: "openai", Region: "eu-west",
		Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch,
		Candidates: []sovereignty.Endpoint{
			{KeyID: "acct-us", Region: "us-east"}, // perda de capacidade UE: só resta US
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Fail-closed: rejeita, NUNCA encaminha para us-east.
	if d.Outcome != OutcomeRejected {
		t.Fatalf("esperava REJECT (sem failover cross-border), obtive %q regiao=%q", d.Outcome, d.Region)
	}
	if len(d.Dropped) != 1 || d.Dropped[0].Region != "us-east" {
		t.Fatalf("o candidato cross-border deve ser DESCARTADO, dropped=%v", d.Dropped)
	}
	if !strings.Contains(d.Reason, "soberania") {
		t.Errorf("a razao do deny deve nomear a soberania, obtive %q", d.Reason)
	}
	// AC5 — a tentativa cross-border é auditável: a decisão (deny + motivo + região +
	// descartados) foi REGISTADA no sink para análise post-hoc.
	if len(sink.decisions) != 1 {
		t.Fatalf("esperava 1 decisao registada (evento auditavel), obtive %d", len(sink.decisions))
	}
	rec := sink.decisions[0]
	if rec.Outcome != OutcomeRejected || len(rec.Dropped) != 1 || rec.Dropped[0].Region != "us-east" {
		t.Errorf("o evento selado deve registar o deny cross-border com o candidato descartado, obtive %+v", rec)
	}
	if !strings.Contains(rec.Reason, "soberania") {
		t.Errorf("o evento selado deve carregar o motivo de soberania, obtive %q", rec.Reason)
	}
}

// TestAOS094_AllowlistRegional_DeniesOutsideRegion (AC4): a allowlist regional do
// board só autoriza o modelo na sua região (eu). Uma chamada cuja região escolhida é
// `us` — fora da região autorizada — NÃO tem modelo elegível e é NEGADA: nenhuma
// chamada de modelo sai da região autorizada.
func TestAOS094_AllowlistRegional_DeniesOutsideRegion(t *testing.T) {
	allow := fakeAllow{allowed: map[string]map[string]map[string]bool{
		"board-eu": {"small": {"eu": true}, "mid": {"eu": true}, "big": {"eu": true}},
	}}
	r := New(refLadder(), WithAllowlist(allow))

	// Dentro da região autorizada (eu): permitido.
	in, _ := r.Route(ctx(), Request{Board: "board-eu", Provider: "openai", Region: "eu", Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch})
	if in.Outcome != OutcomeRouted {
		t.Fatalf("chamada intra-regiao (eu) devia ser roteada, obtive %q", in.Outcome)
	}

	// Fora da região autorizada (us): nenhum modelo na allowlist regional ⇒ REJECT.
	out, _ := r.Route(ctx(), Request{Board: "board-eu", Provider: "openai", Region: "us", Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch})
	if out.Outcome != OutcomeRejected {
		t.Fatalf("chamada fora da regiao autorizada (us) devia ser NEGADA, obtive %q modelo=%q", out.Outcome, out.Model)
	}
	if out.Model != "" {
		t.Errorf("nenhuma chamada de modelo deve sair da regiao autorizada, obtive modelo=%q", out.Model)
	}
}
