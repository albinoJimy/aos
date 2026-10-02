package cache_sli_test

// AOS-397 — o agregado de cache-hit-rate por (run, tenant) tem tecto, com despejo do
// menos-recentemente-usado. Antes era um mapa sem remoção.

import (
	"context"
	"fmt"
	"testing"

	"github.com/aos-ref/platform/model-gateway/metering/cache_sli"
)

func TestAOS397_CacheSLI_RetencaoTemTecto(t *testing.T) {
	t.Parallel()
	rec := cache_sli.NewRecorder(cache_sli.WithClock(fixedClock()))
	ctx := context.Background()
	const n = cache_sli.DefaultRetainedKeys + 500
	for i := 0; i < n; i++ {
		rec.Observe(ctx, nil, cache_sli.Sample{RunID: fmt.Sprintf("run-%d", i), Tenant: "board-eu", PromptTokens: 100, CacheReadTokens: 90})
	}
	if got := rec.RetainedKeys(); got != cache_sli.DefaultRetainedKeys {
		t.Fatalf("retidas=%d, o tecto e %d", got, cache_sli.DefaultRetainedKeys)
	}
	if _, ok := rec.Snapshot(cache_sli.Key{RunID: "run-0", Tenant: "board-eu"}); ok {
		t.Fatal("a chave mais antiga tinha de ter sido despejada")
	}
	if _, ok := rec.Snapshot(cache_sli.Key{RunID: fmt.Sprintf("run-%d", n-1), Tenant: "board-eu"}); !ok {
		t.Fatal("a chave mais recente tinha de estar retida")
	}
}

func TestAOS397_CacheSLI_RunActivoMantemOAgregado(t *testing.T) {
	t.Parallel()
	rec := cache_sli.NewRecorder(cache_sli.WithClock(fixedClock()), cache_sli.WithRetention(2))
	ctx := context.Background()
	activo := cache_sli.Key{RunID: "activo", Tenant: "board-eu"}
	rec.Observe(ctx, nil, cache_sli.Sample{RunID: "activo", Tenant: "board-eu", PromptTokens: 100, CacheReadTokens: 100})
	rec.Observe(ctx, nil, cache_sli.Sample{RunID: "b", Tenant: "board-eu", PromptTokens: 100, CacheReadTokens: 0})
	rec.Observe(ctx, nil, cache_sli.Sample{RunID: "activo", Tenant: "board-eu", PromptTokens: 100, CacheReadTokens: 50})
	rec.Observe(ctx, nil, cache_sli.Sample{RunID: "c", Tenant: "board-eu", PromptTokens: 100, CacheReadTokens: 0}) // despeja b

	agg, ok := rec.Snapshot(activo)
	if !ok || agg.Samples != 2 || agg.CacheReadTokens != 150 {
		t.Fatalf("o run activo tinha de manter as 2 amostras; veio %+v ok=%v", agg, ok)
	}
	if _, ok := rec.Snapshot(cache_sli.Key{RunID: "b", Tenant: "board-eu"}); ok {
		t.Fatal("b era a menos recente e tinha de ter sido despejada")
	}
}
