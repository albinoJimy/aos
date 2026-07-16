package routingtests

import (
	"context"
	"os"
	"testing"

	"github.com/aos-ref/platform/model-gateway/routing/router"
	"github.com/aos-ref/platform/model-gateway/routing/sovereignty"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
)

// TestSelftestRoutingBypassReddensGate é um teste-VENENO: só corre com
// AOS_ROUTING_SELFTEST=1. Reproduz o cenário CROSS-BORDER mas com a soberania
// CONTORNADA (guarda com fronteiras colapsadas + allowlist a permitir tudo), pelo
// que a rota resolve PARA us-east (cross-border) — e depois assere FALSAMENTE que
// foi REJEITADA. Como o failover cross-border passou, a asserção FALHA de propósito,
// PROVANDO que um cenário desbloqueado torna o gate scripts/ci/routing.sh VERMELHO
// (fail-closed). O self-test scripts/ci/selftest.sh (secção G) corre-o com a env var
// e EXIGE que falhe. Fora do self-test é ignorado (não polui a suite verde).
func TestSelftestRoutingBypassReddensGate(t *testing.T) {
	if os.Getenv("AOS_ROUTING_SELFTEST") != "1" {
		t.Skip("teste-veneno do self-test (correr com AOS_ROUTING_SELFTEST=1 via scripts/ci/selftest.sh)")
	}
	h := newHarness(t)
	// CONTROLO DESLIGADO: fronteiras colapsadas + allowlist fail-open (cross-border passa).
	collapsed := sovereignty.NewGuard(
		sovereignty.WithBoundary(regEUWest, "global"),
		sovereignty.WithBoundary(regUSEast, "global"),
	)
	r := router.New(h.ladder,
		router.WithGuard(collapsed),
		router.WithAllowlist(allowAll{}),
		router.WithLoadProvider(h.load),
		router.WithAdmission(h.adm),
		router.WithBudget(h.budget),
		router.WithKeyPool(h.keys),
	)
	dec, err := r.Route(context.Background(), req(regEUWest, tiering.CapabilityFrontier, tiering.ClassInteractive, 10, ep("k-us", regUSEast)))
	if err != nil {
		t.Fatalf("Route erro inesperado: %v", err)
	}
	// Asserção do self-test: assevera (FALSAMENTE) que o cross-border foi BLOQUEADO.
	// Com a soberania contornada, resolveu para us-east — e esta asserção FALHA de
	// propósito, tornando o gate VERMELHO como o self-test exige.
	if dec.Outcome == router.OutcomeRejected {
		t.Fatal("cross-border rejeitado mesmo com a soberania desligada (inesperado no self-test)")
	}
	t.Fatalf("cross-border NÃO bloqueado com o controlo desligado (esperado no self-test): rota resolveu para region=%s outcome=%s", dec.Region, dec.Outcome)
}
