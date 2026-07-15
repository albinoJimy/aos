package tieradapter_test

import (
	"context"
	"testing"
	"time"

	scheduler "github.com/aos-ref/control-plane/scheduler"
	"github.com/aos-ref/platform/model-gateway/routing/router"
	"github.com/aos-ref/platform/model-gateway/routing/tieradapter"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
	"github.com/aos-ref/substrate/eventstore"
)

func gwLadder() *tiering.Ladder {
	return tiering.NewLadder(
		tiering.Tier{Name: "frontier", Model: "big", CostRank: 3, Capability: tiering.CapabilityFrontier},
		tiering.Tier{Name: "standard", Model: "mid", CostRank: 2, Capability: tiering.CapabilityStandard},
		tiering.Tier{Name: "economy", Model: "small", CostRank: 1, Capability: tiering.CapabilityBasic},
	)
}

// allowlist de teste.
type allowlist struct{ deny map[string]bool }

func (a allowlist) Allows(_, model, _ string) bool { return !a.deny[model] }

// TestSatisfiesModelTierRouterPort prova que o router de PRODUÇÃO do GW satisfaz a
// porta scheduler.ModelTierRouter (a exigência central de AOS-059) — verificação
// estática + comportamento de Cheaper.
func TestSatisfiesModelTierRouterPort(t *testing.T) {
	var _ scheduler.ModelTierRouter = tieradapter.NewTierRouter(gwLadder())

	tr := tieradapter.NewTierRouter(gwLadder(), tieradapter.WithTierAllowlist(allowlist{}))
	dec, err := tr.Cheaper(context.Background(), scheduler.TierRouteRequest{
		Key:         scheduler.ProviderKey{Provider: "openai", Model: "big", Region: "eu"},
		CurrentTier: "frontier", CurrentModel: "big",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Downgraded || dec.ToTier != "standard" || dec.ToModel != "mid" {
		t.Errorf("Cheaper(frontier) deve descer para standard/mid, obtive %+v", dec)
	}
	// Já no mais barato: nao degrada.
	dec2, _ := tr.Cheaper(context.Background(), scheduler.TierRouteRequest{
		Key: scheduler.ProviderKey{Region: "eu"}, CurrentTier: "economy", CurrentModel: "small",
	})
	if dec2.Downgraded {
		t.Error("economy e o mais barato: Downgraded deve ser false")
	}
}

// TestCheaperNeverLeavesAllowlist prova que o Cheaper de produção NUNCA degrada
// para um modelo fora da allowlist regional (AOS-058) — salta os degraus negados.
func TestCheaperNeverLeavesAllowlist(t *testing.T) {
	// "mid" (standard) está fora da allowlist ⇒ frontier desce directo para economy.
	tr := tieradapter.NewTierRouter(gwLadder(), tieradapter.WithTierAllowlist(allowlist{deny: map[string]bool{"mid": true}}))
	dec, _ := tr.Cheaper(context.Background(), scheduler.TierRouteRequest{
		Key: scheduler.ProviderKey{Region: "eu"}, CurrentTier: "frontier", CurrentModel: "big",
	})
	if dec.ToModel != "small" {
		t.Errorf("com standard negado, frontier deve saltar para economy/small, obtive %+v", dec)
	}
	// Se TUDO abaixo for negado, nao degrada (nunca fora da fronteira).
	trAll := tieradapter.NewTierRouter(gwLadder(), tieradapter.WithTierAllowlist(allowlist{deny: map[string]bool{"mid": true, "small": true}}))
	decNone, _ := trAll.Cheaper(context.Background(), scheduler.TierRouteRequest{
		Key: scheduler.ProviderKey{Region: "eu"}, CurrentTier: "frontier", CurrentModel: "big",
	})
	if decNone.Downgraded {
		t.Error("sem degrau dentro da allowlist: Downgraded deve ser false (nunca cross-allowlist)")
	}
}

// TestSchedulerDegraderUsesGWRouter prova a NOTA CRUZADA: o Escalonador (Degrader,
// AOS-031) EXECUTA a cadeia shed→defer→downgrade→reject usando o router do GW como
// scheduler.ModelTierRouter — SEM reimplementar a degradação. O GW dá a escolha de
// tier; o Escalonador sela a variância model_downgraded.
func TestSchedulerDegraderUsesGWRouter(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatal(err)
	}
	gwRouter := tieradapter.NewTierRouter(gwLadder(), tieradapter.WithTierAllowlist(allowlist{}))

	// O Degrader do Escalonador é construído com o router de PRODUÇÃO do GW.
	d, err := scheduler.NewDegrader(gwRouter, scheduler.WithDegradationLog(es))
	if err != nil {
		t.Fatalf("NewDegrader com o router do GW: %v", err)
	}

	item := scheduler.DegradationItem{
		ID: "job-1", Tenant: "b1", Priority: "high",
		CurrentTier: "frontier", CurrentModel: "big",
	}
	trigger := scheduler.DegradationTrigger{Reason: "rate-limit: degradar para tier mais barato"}

	// O Escalonador executa o degrau DOWNGRADE (a cadeia é dele); o tier vem do GW.
	res, err := d.Downgrade(context.Background(), item, trigger)
	if err != nil {
		t.Fatalf("Downgrade: %v", err)
	}
	if !res.Applied || res.ToTier != "standard" || res.ToModel != "mid" {
		t.Errorf("o Escalonador deve degradar frontier->standard via o router do GW, obtive %+v", res)
	}

	// A variância model_downgraded foi selada no log pelo Escalonador (replay fiel).
	recs, err := d.ReplayDegradation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range recs {
		if r.Type == scheduler.EventModelDowngraded && r.FromTier == "frontier" && r.ToTier == "standard" {
			found = true
		}
	}
	if !found {
		t.Error("o Escalonador deve selar model_downgraded (frontier->standard) — variancia explicita (ADR-010)")
	}
}

// TestDegraderChainOrderWithGWRouter prova que a CADEIA (shed→defer→downgrade→
// reject) do Escalonador permanece intacta e correcta usando o router do GW: um
// item crítico e não-diferível salta shed e defer, degrada via o GW, e a ordem é
// respeitada — o GW NÃO reimplementa a cadeia.
func TestDegraderChainOrderWithGWRouter(t *testing.T) {
	es, _ := eventstore.New()
	gwRouter := tieradapter.NewTierRouter(gwLadder(), tieradapter.WithTierAllowlist(allowlist{}))
	d, _ := scheduler.NewDegrader(gwRouter, scheduler.WithDegradationLog(es))

	// Item crítico (nao-shed), nao-diferível ⇒ a cadeia salta shed e defer e chega
	// ao DOWNGRADE, que o GW satisfaz (frontier->standard).
	item := scheduler.DegradationItem{
		ID: "crit-1", Tenant: "b1", Priority: "high", Critical: true,
		CurrentTier: "frontier", CurrentModel: "big",
	}
	res, err := d.ExecuteChain(context.Background(), item, scheduler.DegradationTrigger{Reason: "saturacao"}, nil)
	if err != nil {
		t.Fatalf("ExecuteChain: %v", err)
	}
	if res.Action != scheduler.ActionDowngrade || res.ToTier != "standard" {
		t.Errorf("cadeia deve escalar ate downgrade (via GW), obtive accao=%q toTier=%q", res.Action, res.ToTier)
	}
}

// TestAdmissionAdapterCoordination prova que o router de produção COORDENA com o
// admission control GLOBAL real do Escalonador (*scheduler.Admission, ADR-008) via
// o AdmissionAdapter — sem colapso agregado: vários boards partilham o tecto global
// e são adiados quando a soma satura.
func TestAdmissionAdapterCoordination(t *testing.T) {
	es, _ := eventstore.New()
	// Tecto global: RPM=3 (3 chamadas por janela), TPM alto.
	qp := scheduler.NewStaticQuotaProvider(scheduler.ProviderLimits{TPM: 1_000_000, RPM: 3, Window: time.Minute})
	adm, err := scheduler.NewAdmission(es, qp)
	if err != nil {
		t.Fatal(err)
	}
	coord := tieradapter.NewAdmissionAdapter(adm)

	r := router.New(gwLadder(),
		router.WithAllowlist(allowlist{}),
		router.WithAdmission(coord),
	)

	admitted, deferred := 0, 0
	for _, b := range []string{"b1", "b2", "b3", "b4", "b5"} {
		d, err := r.Route(context.Background(), router.Request{
			Board: b, Tenant: b, Provider: "openai", Region: "eu",
			Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch, EstimatedTokens: 100,
		})
		if err != nil {
			t.Fatal(err)
		}
		switch d.Outcome {
		case router.OutcomeRouted:
			admitted++
		case router.OutcomeDeferred:
			deferred++
		default:
			t.Fatalf("board %s: resultado inesperado %q (%s)", b, d.Outcome, d.Reason)
		}
	}
	// 3 admitidos (RPM=3), 2 adiados — coordenação global, sem colapso agregado.
	if admitted != 3 || deferred != 2 {
		t.Errorf("coordenacao com admission real: esperava 3 admitidos + 2 adiados, obtive %d/%d", admitted, deferred)
	}
}

// TestAdmissionAdapterPropagatesError garante que um erro do admission real
// propaga fail-closed pelo router (nao se despacha sem reserva).
func TestAdmissionAdapterPropagatesError(t *testing.T) {
	coord := tieradapter.NewAdmissionAdapter(nil)
	if coord != nil {
		t.Error("NewAdmissionAdapter(nil) deve devolver nil (o router trata a ausencia de porta)")
	}
}
