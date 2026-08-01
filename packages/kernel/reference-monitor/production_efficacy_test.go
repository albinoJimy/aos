package referencemonitor_test

import (
	"context"
	"errors"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/taint"
)

// AOS-219 — eficácia-vs-presença do TaintGate. O ápice arranca com o conjunto privileged
// VAZIO (conjunto real DEFERIDO em AOS-183/DEF-808) — este é o caso concreto que o achado
// do RM visa: um gate wired mas com conjunto VAZIO é INERTE.
//
// ATRIBUIÇÃO DE EVIDÊNCIA (precisa — não confundir as duas classes):
//
//   - FALHA-ANTES DO PREDICADO (prova directa do fix eficácia-vs-presença): apenas
//     [TestHasActiveTaintGate_EmptySetIsInert] e [TestNewProductionHardenedTaint_FailClosedOnInert]
//     dependem de [Monitor.hasActiveTaintGate]. Com a guarda antiga `g.privileged != nil` o
//     predicado era TRUE para o conjunto vazio ⇒ AMBOS FALHAM (verificado empiricamente).
//     Sentido oposto (não-vácuo): [TestHasActiveTaintGate_NonEmptySetIsActive] e
//     [TestNewProductionHardenedTaint_AcceptsEffective].
//   - CONSEQUÊNCIA MATERIAL (corroboração, NÃO falha-antes do predicado):
//     [TestTaintEnforcement_EmptyVsNonEmpty] exercita [TaintGate.Evaluate] no runtime, que
//     NÃO consulta o predicado corrigido — MANTÉM-SE VERDE sob a guarda antiga. Prova por que
//     razão o conjunto vazio não conta como enforcement (a mesma call passa vs. é barrada),
//     mas não é evidência falsificável DO predicado. Rotulá-lo como tal seria impreciso.

// TestHasActiveTaintGate_EmptySetIsInert: a metade FALHA-ANTES. Com [NewStaticPrivilegedSet]
// VAZIO, NewProduction ainda CONSTRÓI (a barreira está wired ⇒ retro-compat; o nó arranca),
// mas o predicado de EFICÁCIA é FALSO — o nó NÃO pode alegar postura de taint endurecida.
// Com a guarda antiga (presença), este predicado seria TRUE e o teste FALHARIA.
func TestHasActiveTaintGate_EmptySetIsInert(t *testing.T) {
	empty := referencemonitor.NewStaticPrivilegedSet() // conjunto VAZIO ⇒ gate inerte
	m, err := referencemonitor.NewProduction(empty, referencemonitor.WithEventSink(&spySink{}))
	if err != nil {
		t.Fatalf("NewProduction com conjunto vazio devia CONSTRUIR (gate wired; retro-compat), erro=%v", err)
	}
	if m.HasActiveTaintGate() {
		t.Fatal("FALHA-ANTES: HasActiveTaintGate()=true com conjunto VAZIO — a guarda afere presença, não eficácia (AOS-219)")
	}
}

// TestHasActiveTaintGate_NonEmptySetIsActive: o sentido OPOSTO. Com um conjunto não-vazio o
// predicado é VERDADEIRO — a guarda não é vácua (não devolve sempre false).
func TestHasActiveTaintGate_NonEmptySetIsActive(t *testing.T) {
	priv := referencemonitor.NewStaticPrivilegedSet(capPrivileged) // conjunto não-vazio
	m, err := referencemonitor.NewProduction(priv, referencemonitor.WithEventSink(&spySink{}))
	if err != nil {
		t.Fatalf("NewProduction: %v", err)
	}
	if !m.HasActiveTaintGate() {
		t.Fatal("HasActiveTaintGate()=false com conjunto NÃO-VAZIO — a guarda rejeitaria um gate eficaz")
	}
}

// TestTaintEnforcement_EmptyVsNonEmpty prova a CONSEQUÊNCIA DE MEDIAÇÃO da inércia: a MESMA
// call privilegiada+untrusted que um conjunto não-vazio BLOQUEIA (atribuída ao gate "taint")
// é PERMITIDA quando o conjunto é vazio. É a razão material pela qual o conjunto vazio não
// conta como enforcement — não uma verificação de estrutura em vácuo.
//
// ATRIBUIÇÃO (ver cabeçalho): este teste exercita [TaintGate.Evaluate], que NÃO consulta o
// predicado [Monitor.hasActiveTaintGate] corrigido em AOS-219 — mantém-se VERDE mesmo sob a
// guarda antiga de PRESENÇA. É corroboração da consequência material da inércia, NÃO uma
// prova falha-antes DO predicado (essas são as duas de [Monitor.hasActiveTaintGate] acima).
func TestTaintEnforcement_EmptyVsNonEmpty(t *testing.T) {
	newRM := func(t *testing.T, priv referencemonitor.PrivilegedAuthorizer) *referencemonitor.Monitor {
		t.Helper()
		m, err := referencemonitor.NewProduction(priv, referencemonitor.WithEventSink(&spySink{}))
		if err != nil {
			t.Fatalf("NewProduction: %v", err)
		}
		if err := m.Register("exfil", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
			t.Fatalf("Register: %v", err)
		}
		return m
	}

	// Conjunto NÃO-VAZIO: promoção tainted EFECTIVAMENTE barrada.
	mActive := newRM(t, referencemonitor.NewStaticPrivilegedSet(capPrivileged))
	dec, err := mActive.Mediate(context.Background(), privilegedCall("exfil", taint.StringUntrusted))
	if err != nil {
		t.Fatalf("Mediate (activo): %v", err)
	}
	if dec.Effect != referencemonitor.EffectDeny || dec.DeniedBy != "taint" {
		t.Fatalf("conjunto não-vazio: privilegiada+untrusted devia ser DENY por \"taint\", got effect=%s deniedBy=%q", dec.Effect, dec.DeniedBy)
	}

	// Conjunto VAZIO: gate INERTE — a MESMA call NÃO é barrada por taint (a promoção passa).
	mInert := newRM(t, referencemonitor.NewStaticPrivilegedSet())
	dec2, err := mInert.Mediate(context.Background(), privilegedCall("exfil", taint.StringUntrusted))
	if err != nil {
		t.Fatalf("Mediate (inerte): %v", err)
	}
	if dec2.Effect == referencemonitor.EffectDeny && dec2.DeniedBy == "taint" {
		t.Fatal("conjunto VAZIO: o taint NÃO devia barrar (gate inerte) — se barrou, o cenário de inércia não se reproduz e a guarda de eficácia perde sentido")
	}
}

// TestNewProductionHardenedTaint_FailClosedOnInert: a via ENDURECIDA recusa fail-closed um
// gate inerte. Conjunto VAZIO ⇒ [ErrTaintGateInert] e Monitor nil (o nó não arranca a alegar
// barreira control/data-plane com um no-op). Falha-antes: sem a guarda de eficácia a
// construção devolveria um Monitor (como NewProductionSecure).
func TestNewProductionHardenedTaint_FailClosedOnInert(t *testing.T) {
	empty := referencemonitor.NewStaticPrivilegedSet()
	m, err := referencemonitor.NewProductionHardenedTaint(
		empty,
		referencemonitor.WithEventSink(&spySink{}),
		referencemonitor.WithHooks(realChain(empty)...),
	)
	if !errors.Is(err, referencemonitor.ErrTaintGateInert) {
		t.Fatalf("erro=%v want ErrTaintGateInert (conjunto vazio ⇒ gate inerte)", err)
	}
	if m != nil {
		t.Error("Monitor devia ser nil quando o TaintGate é inerte na via endurecida")
	}
}

// TestNewProductionHardenedTaint_AcceptsEffective: o sentido oposto — com um conjunto
// não-vazio a via endurecida CONSTRÓI e o gate é activo.
func TestNewProductionHardenedTaint_AcceptsEffective(t *testing.T) {
	priv := referencemonitor.NewStaticPrivilegedSet(capPrivileged)
	m, err := referencemonitor.NewProductionHardenedTaint(
		priv,
		referencemonitor.WithEventSink(&spySink{}),
		referencemonitor.WithHooks(realChain(priv)...),
	)
	if err != nil {
		t.Fatalf("NewProductionHardenedTaint com conjunto não-vazio: erro inesperado %v", err)
	}
	if m == nil || !m.HasActiveTaintGate() {
		t.Fatal("via endurecida com conjunto não-vazio devia devolver Monitor com gate activo")
	}
}
