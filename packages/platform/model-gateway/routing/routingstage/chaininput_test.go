package routingstage_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/routing/router"
	"github.com/aos-ref/platform/model-gateway/routing/routingstage"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
)

// chaininput_test.go prova a regra «RESOLVIDA-PRIMEIRO» de AOS-280 no ESTÁGIO (a
// prova pela cadeia real, ao nível do gateway composto, está em
// routing_chain_aos280_test.go, na raiz do módulo).

// TestStage_UsesResolvedRegionWhenPresent — encadeado a jusante de um estágio que já
// resolveu a região, o refino parte DESSA região; a região PEDIDA é apenas o
// fallback de quando não há estágio anterior. Ler sempre a pedida era o encadeamento
// ingénuo: descartaria em silêncio a decisão de soberania/failover.
func TestStage_UsesResolvedRegionWhenPresent(t *testing.T) {
	// SEM candidatos explícitos, a região de ENTRADA é a única sobrevivente da
	// partição de soberania — logo é ela, e só ela, que decide a região resolvida.
	// O teste fica assim a discriminar exactamente a regra em causa.
	r := router.New(ladder(), router.WithAllowlist(allowAll{}))
	st := routingstage.NewStage(r, routingstage.WithClassifier(func(*pipeline.Exchange) routingstage.Task {
		return routingstage.Task{Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch}
	}))

	ex := &pipeline.Exchange{
		Board: "board-eu", RequestedModel: "big", RequestedProvider: "openai",
		RequestedRegion: "eu", ResolvedRegion: "eu-west", // o estágio anterior já decidiu
	}
	if err := st.Process(context.Background(), ex); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if ex.ResolvedRegion != "eu-west" {
		t.Fatalf("a região resolvida a montante tem de sobreviver ao refino; ficou %q", ex.ResolvedRegion)
	}
	// GÉMEO (não-vacuidade): o MESMO router alimentado com a região PEDIDA — o que o
	// encadeamento ingénuo faria — resolve "eu". A asserção acima discrimina.
	naive, err := r.Route(context.Background(), router.Request{
		Board: "board-eu", Tenant: "board-eu", Provider: "openai", Region: "eu",
		Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch,
	})
	if err != nil {
		t.Fatalf("gémeo: Route: %v", err)
	}
	if naive.Region != "eu" {
		t.Fatalf("gémeo não-vácuo: partir da região PEDIDA tinha de dar %q, deu %q", "eu", naive.Region)
	}

	// FALLBACK (uso standalone, sem estágio anterior): a região PEDIDA é a entrada —
	// é o que preserva byte-a-byte o comportamento de AOS-059/AOS-063.
	alone := &pipeline.Exchange{
		Board: "board-eu", RequestedModel: "big", RequestedProvider: "openai", RequestedRegion: "eu",
	}
	if err := st.Process(context.Background(), alone); err != nil {
		t.Fatalf("Process (standalone): %v", err)
	}
	if alone.ResolvedRegion != "eu" {
		t.Fatalf("sem estágio anterior a entrada é a região PEDIDA; ficou %q", alone.ResolvedRegion)
	}
}

// TestStage_NoRefinePreservesUpstreamRoute — quando o classificador declara que não
// caracterizou a chamada, o estágio PRESERVA a rota do estágio anterior e regista a
// razão. Nem recusa (uma escada mal declarada não pode interromper o caminho quente),
// nem rota às cegas.
func TestStage_NoRefinePreservesUpstreamRoute(t *testing.T) {
	r := router.New(ladder(), router.WithAllowlist(allowAll{}))
	st := routingstage.NewStage(r, routingstage.WithClassifier(func(*pipeline.Exchange) routingstage.Task {
		return routingstage.Task{NoRefine: "sem refino: modelo fora da escada declarada"}
	}))
	ex := &pipeline.Exchange{
		Board: "board-eu", RequestedModel: "desconhecido", RequestedProvider: "openai", RequestedRegion: "eu",
		ResolvedModel: "desconhecido", ResolvedProvider: "openai", ResolvedRegion: "eu-west",
	}
	if err := st.Process(context.Background(), ex); err != nil {
		t.Fatalf("um NoRefine não pode falhar a chamada: %v", err)
	}
	if ex.ResolvedModel != "desconhecido" || ex.ResolvedRegion != "eu-west" {
		t.Fatalf("a rota do estágio anterior tem de ficar intacta: modelo=%q regiao=%q", ex.ResolvedModel, ex.ResolvedRegion)
	}
	found := false
	for _, d := range ex.Decisions {
		if d.Result == "no-refine" && strings.Contains(d.Reason, "fora da escada") {
			found = true
		}
	}
	if !found {
		t.Fatalf("saltar o refino tem de ficar REGISTADO com razão: %+v", ex.Decisions)
	}

	// Standalone (sem estágio anterior): o Exchange nunca fica sem rota — espelha o
	// pedido, como o pass-through de AOS-055.
	alone := &pipeline.Exchange{RequestedModel: "desconhecido", RequestedProvider: "openai", RequestedRegion: "eu"}
	if err := st.Process(context.Background(), alone); err != nil {
		t.Fatalf("Process (standalone): %v", err)
	}
	if alone.ResolvedModel != "desconhecido" || alone.ResolvedRegion != "eu" || alone.ResolvedProvider != "openai" {
		t.Fatalf("o Exchange não pode ficar sem rota: %+v", alone)
	}
}

// TestStage_InputHelpersMatchStageRule — os ajudantes exportados (que o classificador
// de produção usa para derivar candidatos) aplicam a MESMA regra do estágio. Duas
// definições da mesma regra divergiriam em silêncio.
func TestStage_InputHelpersMatchStageRule(t *testing.T) {
	resolved := &pipeline.Exchange{
		RequestedRegion: "eu", ResolvedRegion: "eu-west",
		RequestedProvider: "a", ResolvedProvider: "b",
	}
	if got := routingstage.InputRegion(resolved); got != "eu-west" {
		t.Fatalf("InputRegion = %q", got)
	}
	if got := routingstage.InputProvider(resolved); got != "b" {
		t.Fatalf("InputProvider = %q", got)
	}
	pending := &pipeline.Exchange{RequestedRegion: "eu", RequestedProvider: "a"}
	if got := routingstage.InputRegion(pending); got != "eu" {
		t.Fatalf("InputRegion (fallback) = %q", got)
	}
	if got := routingstage.InputProvider(pending); got != "a" {
		t.Fatalf("InputProvider (fallback) = %q", got)
	}
}
