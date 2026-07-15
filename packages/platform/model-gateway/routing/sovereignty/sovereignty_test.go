package sovereignty_test

import (
	"testing"

	"github.com/aos-ref/platform/model-gateway/routing/sovereignty"
)

func ep(keyID, region string) sovereignty.Endpoint {
	return sovereignty.Endpoint{KeyID: keyID, Region: region}
}

// TestFailover_IntraBoundary — primário caído + alternativo INTRA-região → failover
// intra-fronteira (mesma soberania).
func TestFailover_IntraBoundary(t *testing.T) {
	g := sovereignty.NewGuard()
	d := g.Failover("eu", []sovereignty.Endpoint{
		ep("acct-eu-2", "eu"),
		ep("acct-us-1", "us-east"), // cross-border: nunca elegível
	})
	if d.Outcome != sovereignty.OutcomeFailover {
		t.Fatalf("Outcome = %q; quero failover_intra", d.Outcome)
	}
	if d.Chosen.KeyID != "acct-eu-2" || d.Chosen.Region != "eu" {
		t.Fatalf("Chosen = %+v; quero acct-eu-2/eu", d.Chosen)
	}
	// O candidato cross-border foi DESCARTADO (não ordenado ao fundo).
	if len(d.Dropped) != 1 || d.Dropped[0].KeyID != "acct-us-1" {
		t.Fatalf("Dropped = %+v; quero [acct-us-1]", d.Dropped)
	}
}

// TestFailover_NoIntra_Rejects — sem alternativo intra-região → REJEIÇÃO (nunca
// cross-border), mesmo havendo capacidade cross-border.
func TestFailover_NoIntra_Rejects(t *testing.T) {
	g := sovereignty.NewGuard()
	d := g.Failover("eu", []sovereignty.Endpoint{
		ep("acct-us-1", "us-east"),
		ep("acct-us-2", "us-west"),
	})
	if d.Outcome != sovereignty.OutcomeReject {
		t.Fatalf("Outcome = %q; quero reject_no_intra", d.Outcome)
	}
	if d.Chosen.KeyID != "" {
		t.Fatalf("nenhum endpoint devia ser escolhido; got %+v", d.Chosen)
	}
	// É o caso de GOVERNAÇÃO: havia capacidade, mas só cross-border → deny atribuível.
	if !d.CrossBorderBlocked() {
		t.Fatal("devia ser CrossBorderBlocked (rejeicao por so haver cross-border)")
	}
	if len(d.Dropped) != 2 {
		t.Fatalf("Dropped = %d; quero 2", len(d.Dropped))
	}
}

// TestFailover_NoCandidates_PlainReject — sem quaisquer candidatos → rejeição
// simples (não é o caso cross-border: não havia para onde ir).
func TestFailover_NoCandidates_PlainReject(t *testing.T) {
	g := sovereignty.NewGuard()
	d := g.Failover("eu", nil)
	if d.Outcome != sovereignty.OutcomeReject {
		t.Fatalf("Outcome = %q; quero reject", d.Outcome)
	}
	if d.CrossBorderBlocked() {
		t.Fatal("sem candidatos NÃO é cross-border blocked")
	}
}

// TestFailover_StructuralNeverCrossBorder — prova estrutural: para QUALQUER conjunto
// de candidatos, o Chosen (quando existe) está SEMPRE na fronteira da região pedida.
func TestFailover_StructuralNeverCrossBorder(t *testing.T) {
	g := sovereignty.NewGuard()
	candidateSets := [][]sovereignty.Endpoint{
		{ep("z-us", "us-east"), ep("a-eu", "eu")},
		{ep("a-us", "us-east"), ep("b-eu", "eu"), ep("c-eu", "eu-west-but-mapped")},
		{ep("only-eu", "eu")},
	}
	for i, cs := range candidateSets {
		d := g.Failover("eu", cs)
		if d.Outcome == sovereignty.OutcomeFailover && g.BoundaryOf(d.Chosen.Region) != g.BoundaryOf("eu") {
			t.Fatalf("set %d: escolhido cross-border %+v (fronteira %q != eu)", i, d.Chosen, g.BoundaryOf(d.Chosen.Region))
		}
	}
}

// TestFailover_DeterministicByKeyID — entre sobreviventes intra-fronteira, a escolha
// é determinista (KeyID ascendente), independente da ordem de entrada.
func TestFailover_DeterministicByKeyID(t *testing.T) {
	g := sovereignty.NewGuard()
	a := g.Failover("eu", []sovereignty.Endpoint{ep("acct-eu-9", "eu"), ep("acct-eu-1", "eu"), ep("acct-eu-5", "eu")})
	b := g.Failover("eu", []sovereignty.Endpoint{ep("acct-eu-5", "eu"), ep("acct-eu-9", "eu"), ep("acct-eu-1", "eu")})
	if a.Chosen.KeyID != "acct-eu-1" || b.Chosen.KeyID != "acct-eu-1" {
		t.Fatalf("escolha nao determinista: a=%q b=%q; quero acct-eu-1", a.Chosen.KeyID, b.Chosen.KeyID)
	}
}

// TestBoundaryGrouping — regiões agrupadas numa jurisdição comum são intra-fronteira
// entre si (failover permitido); regiões noutra jurisdição continuam cross-border.
func TestBoundaryGrouping(t *testing.T) {
	g := sovereignty.NewGuard(
		sovereignty.WithBoundary("eu-west", "eu"),
		sovereignty.WithBoundary("eu-central", "eu"),
	)
	d := g.Failover("eu-west", []sovereignty.Endpoint{
		ep("acct-euc", "eu-central"), // mesma jurisdição EU → elegível
		ep("acct-us", "us-east"),     // cross-border
	})
	if d.Outcome != sovereignty.OutcomeFailover || d.Chosen.KeyID != "acct-euc" {
		t.Fatalf("failover intra-jurisdicao falhou: %+v", d)
	}
	if !g.SameBoundary("eu-west", "eu-central") {
		t.Fatal("eu-west e eu-central deviam partilhar fronteira")
	}
	if g.SameBoundary("eu-west", "us-east") {
		t.Fatal("eu-west e us-east NAO deviam partilhar fronteira")
	}
}

// TestRoute_PrimaryHealthy — primário saudável e intra-fronteira → PRIMARY (sem failover).
func TestRoute_PrimaryHealthy(t *testing.T) {
	g := sovereignty.NewGuard()
	healthy := func(sovereignty.Endpoint) bool { return true }
	d := g.Route("eu", ep("acct-eu-1", "eu"), []sovereignty.Endpoint{ep("acct-eu-2", "eu")}, healthy)
	if d.Outcome != sovereignty.OutcomePrimary || d.Chosen.KeyID != "acct-eu-1" {
		t.Fatalf("primário saudável devia ser usado; got %+v", d)
	}
}

// TestRoute_PrimaryDown_FailoverIntra — primário indisponível → failover intra-fronteira.
func TestRoute_PrimaryDown_FailoverIntra(t *testing.T) {
	g := sovereignty.NewGuard()
	down := func(e sovereignty.Endpoint) bool { return e.KeyID != "acct-eu-1" } // o primário está em baixo
	d := g.Route("eu", ep("acct-eu-1", "eu"), []sovereignty.Endpoint{
		ep("acct-eu-2", "eu"),
		ep("acct-us-1", "us-east"),
	}, down)
	if d.Outcome != sovereignty.OutcomeFailover || d.Chosen.KeyID != "acct-eu-2" {
		t.Fatalf("failover intra devia escolher acct-eu-2; got %+v", d)
	}
}

// TestRoute_PrimaryDown_NoIntra_Rejects — primário em baixo e só há cross-border →
// REJEIÇÃO (o diagrama de decisão termina em rejeição, nunca cross-border).
func TestRoute_PrimaryDown_NoIntra_Rejects(t *testing.T) {
	g := sovereignty.NewGuard()
	down := func(sovereignty.Endpoint) bool { return false }
	d := g.Route("eu", ep("acct-eu-1", "eu"), []sovereignty.Endpoint{ep("acct-us-1", "us-east")}, down)
	if d.Outcome != sovereignty.OutcomeReject || !d.CrossBorderBlocked() {
		t.Fatalf("devia rejeitar cross-border; got %+v", d)
	}
}

// TestRoute_PrimaryOutsideBoundary_NotUsed — um primário mal configurado FORA da
// fronteira nunca é usado (defesa em profundidade): força failover/rejeição.
func TestRoute_PrimaryOutsideBoundary_NotUsed(t *testing.T) {
	g := sovereignty.NewGuard()
	healthy := func(sovereignty.Endpoint) bool { return true }
	d := g.Route("eu", ep("acct-us-1", "us-east"), []sovereignty.Endpoint{ep("acct-eu-2", "eu")}, healthy)
	if d.Outcome != sovereignty.OutcomeFailover || d.Chosen.Region != "eu" {
		t.Fatalf("primário fora da fronteira nao devia ser usado; got %+v", d)
	}
}

// TestFailover_IgnoresEmptyKeyID — candidatos sem KeyID são ignorados (fail-closed).
func TestFailover_IgnoresEmptyKeyID(t *testing.T) {
	g := sovereignty.NewGuard()
	d := g.Failover("eu", []sovereignty.Endpoint{ep("", "eu"), ep("acct-eu-1", "eu")})
	if d.Outcome != sovereignty.OutcomeFailover || d.Chosen.KeyID != "acct-eu-1" {
		t.Fatalf("devia ignorar KeyID vazio; got %+v", d)
	}
}

// TestFailover_EmptyRequestedRegion_Rejects (AOS058-Q5) — região pedida vazia é
// jurisdição indefinida: fail-closed (rejeita), nunca "coincide" com candidatos.
func TestFailover_EmptyRequestedRegion_Rejects(t *testing.T) {
	g := sovereignty.NewGuard()
	d := g.Failover("", []sovereignty.Endpoint{ep("acct-x", "eu"), ep("acct-y", "")})
	if d.Outcome != sovereignty.OutcomeReject {
		t.Fatalf("região pedida vazia devia rejeitar; got %+v", d)
	}
	if d.Chosen.KeyID != "" {
		t.Fatalf("nenhum endpoint devia ser escolhido; got %+v", d.Chosen)
	}
}

// TestFailover_IgnoresEmptyCandidateRegion (AOS058-Q5) — um candidato com região vazia
// (jurisdição indefinida) NÃO é elegível, mesmo que a região pedida resolva. Só o
// candidato com região definida e intra-fronteira é escolhido.
func TestFailover_IgnoresEmptyCandidateRegion(t *testing.T) {
	g := sovereignty.NewGuard()
	d := g.Failover("eu", []sovereignty.Endpoint{ep("acct-empty", ""), ep("acct-eu-1", "eu")})
	if d.Outcome != sovereignty.OutcomeFailover || d.Chosen.KeyID != "acct-eu-1" {
		t.Fatalf("candidato com região vazia devia ser ignorado; got %+v", d)
	}
	// Um candidato com região vazia como ÚNICA opção → rejeição (nunca elegível).
	only := g.Failover("eu", []sovereignty.Endpoint{ep("acct-empty", "")})
	if only.Outcome != sovereignty.OutcomeReject {
		t.Fatalf("só candidato com região vazia devia rejeitar; got %+v", only)
	}
}

// TestRoute_EmptyRequestedRegion_NeverPrimary (AOS058-Q5) — com região pedida vazia,
// nem um primário saudável mas também sem região é usado: fail-closed (rejeita), a
// disponibilidade nunca compra jurisdição indefinida.
func TestRoute_EmptyRequestedRegion_NeverPrimary(t *testing.T) {
	g := sovereignty.NewGuard()
	healthy := func(sovereignty.Endpoint) bool { return true }
	d := g.Route("", ep("acct-x", ""), nil, healthy)
	if d.Outcome != sovereignty.OutcomeReject {
		t.Fatalf("região pedida vazia nunca devia dar PRIMARY; got %+v", d)
	}
}
