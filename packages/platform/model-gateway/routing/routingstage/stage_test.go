package routingstage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/routing/degradation"
	"github.com/aos-ref/platform/model-gateway/routing/router"
	"github.com/aos-ref/platform/model-gateway/routing/routingstage"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
)

func ladder() *tiering.Ladder {
	return tiering.NewLadder(
		tiering.Tier{Name: "frontier", Model: "big", CostRank: 3, Capability: tiering.CapabilityFrontier},
		tiering.Tier{Name: "standard", Model: "mid", CostRank: 2, Capability: tiering.CapabilityStandard},
		tiering.Tier{Name: "economy", Model: "small", CostRank: 1, Capability: tiering.CapabilityBasic},
	)
}

type allowAll struct{}

func (allowAll) Allows(_, _, _ string) bool { return true }

func TestStageResolvesRoute(t *testing.T) {
	r := router.New(ladder(), router.WithAllowlist(allowAll{}))
	st := routingstage.NewStage(r, routingstage.WithClassifier(func(*pipeline.Exchange) routingstage.Task {
		return routingstage.Task{Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch}
	}))

	ex := &pipeline.Exchange{Op: pipeline.OpChat, Board: "b1", RequestedModel: "whatever", RequestedProvider: "openai", RequestedRegion: "eu"}
	if err := st.Process(context.Background(), ex); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if ex.ResolvedModel != "small" {
		t.Errorf("estágio deve resolver o modelo do tier escolhido (small), obtive %q", ex.ResolvedModel)
	}
	if ex.ResolvedRegion != "eu" {
		t.Errorf("regiao resolvida esperada eu, obtive %q", ex.ResolvedRegion)
	}
	// A decisão de roteamento fica no rasto (modelo/tier/razão).
	last := ex.Decisions[len(ex.Decisions)-1]
	if last.Stage != "roteamento" || last.Reason == "" {
		t.Errorf("estágio deve registar a decisao de roteamento no rasto, obtive %+v", last)
	}
}

func TestStageDeferFailsClosed(t *testing.T) {
	// Admission com headroom já ESGOTADO (RPM=1 pré-consumido) ⇒ defer ⇒ o estágio
	// FALHA-FECHA (o provider não corre sem débito reservado).
	adm := router.NewStaticAdmissionCoordinator(1000, 1, 500)
	// Pré-consome o único request disponível.
	if _, err := adm.Reserve(context.Background(), router.AdmissionRequest{Provider: "openai", Model: "small", Region: "eu", EstimatedTokens: 10}); err != nil {
		t.Fatal(err)
	}
	r := router.New(ladder(), router.WithAllowlist(allowAll{}), router.WithAdmission(adm))
	st := routingstage.NewStage(r, routingstage.WithClassifier(func(*pipeline.Exchange) routingstage.Task {
		return routingstage.Task{Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch, EstimatedTokens: 10}
	}))

	ex := &pipeline.Exchange{Op: pipeline.OpChat, Board: "b1", RequestedProvider: "openai", RequestedRegion: "eu"}
	err := st.Process(context.Background(), ex)
	if !errors.Is(err, routingstage.ErrRouteDeferred) {
		t.Fatalf("sem headroom global o estágio deve falhar com ErrRouteDeferred, obtive %v", err)
	}
}

func TestStageRejectFailsClosed(t *testing.T) {
	// Nenhum tier dentro da allowlist ⇒ reject fail-closed.
	deny := denyAll{}
	r := router.New(ladder(), router.WithAllowlist(deny))
	st := routingstage.NewStage(r)
	ex := &pipeline.Exchange{Op: pipeline.OpChat, Board: "b1", RequestedProvider: "openai", RequestedRegion: "eu"}
	err := st.Process(context.Background(), ex)
	if !errors.Is(err, routingstage.ErrRouteRejected) {
		t.Fatalf("sem tier na allowlist o estágio deve falhar com ErrRouteRejected, obtive %v", err)
	}
}

func TestStageDegradeRecordedAsDecision(t *testing.T) {
	// A degradação por orçamento PRESERVA a capacidade exigida: uma escada com dois
	// tiers Frontier (rápido/caro e lento/barato) degrada frontier-fast->frontier-
	// cheap a 90%% do orçamento — nunca para um tier incapaz.
	dl := tiering.NewLadder(
		tiering.Tier{Name: "frontier-fast", Model: "big-fast", CostRank: 3, Capability: tiering.CapabilityFrontier, Fast: true},
		tiering.Tier{Name: "frontier-cheap", Model: "big-cheap", CostRank: 2, Capability: tiering.CapabilityFrontier},
		tiering.Tier{Name: "economy", Model: "small", CostRank: 1, Capability: tiering.CapabilityBasic},
	)
	budget := degradation.NewStaticBudgetProvider(degradation.BudgetState{}).
		Set(degradation.BudgetKey{Board: "b1", Tenant: "b1"}, degradation.BudgetState{Used: 90, Limit: 100})
	r := router.New(dl, router.WithAllowlist(allowAll{}), router.WithBudget(budget))
	st := routingstage.NewStage(r, routingstage.WithClassifier(func(*pipeline.Exchange) routingstage.Task {
		return routingstage.Task{Capability: tiering.CapabilityFrontier, Class: tiering.ClassInteractive}
	}))
	ex := &pipeline.Exchange{Op: pipeline.OpChat, Board: "b1", RequestedProvider: "openai", RequestedRegion: "eu"}
	if err := st.Process(context.Background(), ex); err != nil {
		t.Fatal(err)
	}
	// A 90% do orçamento degrada para o tier Frontier barato; fica registado "degrade".
	if ex.ResolvedModel != "big-cheap" {
		t.Errorf("exaustao graciosa deve degradar para o Frontier barato (big-cheap), obtive %q", ex.ResolvedModel)
	}
	last := ex.Decisions[len(ex.Decisions)-1]
	if last.Result != "degrade" {
		t.Errorf("degrade deve ser registado como resultado 'degrade', obtive %q", last.Result)
	}
}

type denyAll struct{}

func (denyAll) Allows(_, _, _ string) bool { return false }

func TestStageName(t *testing.T) {
	st := routingstage.NewStage(router.New(ladder()))
	if st.Name() != "roteamento" {
		t.Errorf("nome do estágio deve preservar o slot canonico 'roteamento', obtive %q", st.Name())
	}
}

func TestNilRouterFailsClosed(t *testing.T) {
	st := routingstage.NewStage(nil)
	if err := st.Process(context.Background(), &pipeline.Exchange{RequestedRegion: "eu"}); err == nil {
		t.Error("estágio sem router deve falhar-fechar")
	}
}

// TestAllowlistFromRealPolicy prova a composição estrutural: o router, alimentado
// pela allowlist REAL assinada (AOS-058), só permite os triplos que a policy
// autoriza — a garantia de que o roteamento nunca escolhe fora da fronteira.
func TestAllowlistFromRealPolicy(t *testing.T) {
	pol, err := allowlist.LoadPolicy()
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	a := routingstage.AllowlistFrom(pol)
	if !a.Allows("board-eu", "gpt-4o", "eu") {
		t.Error("board-eu/gpt-4o/eu deve ser permitido pela allowlist real")
	}
	if a.Allows("board-eu", "gpt-4o", "us-east") {
		t.Error("board-eu/gpt-4o/us-east esta fora da fronteira — nao deve ser permitido")
	}
	if a.Allows("board-eu", "modelo-fantasma", "eu") {
		t.Error("modelo fora da allowlist nao deve ser permitido (default-deny)")
	}
	// Policy nil é fail-closed (nunca fail-open).
	if routingstage.AllowlistFrom(nil).Allows("board-eu", "gpt-4o", "eu") {
		t.Error("AllowlistFrom(nil) deve ser fail-closed")
	}
}
