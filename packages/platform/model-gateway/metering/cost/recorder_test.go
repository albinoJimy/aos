package cost

import (
	"context"
	"errors"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/port"
)

func newTestRecorder(t *testing.T, opts ...Option) (*Recorder, *MemoryMetricSink, *MemoryBurndownSink) {
	t.Helper()
	metrics := &MemoryMetricSink{}
	burn := NewMemoryBurndownSink()
	base := []Option{
		WithMetricSink(metrics),
		WithBurndownSink(burn),
		WithClock(func() time.Time { return time.Unix(42, 0) }),
	}
	r := NewRecorder(NewCalculator(testTable(t)), append(base, opts...)...)
	return r, metrics, burn
}

func newSpan() agentruntime.Span {
	_, span := (&agentruntime.RecordingTracer{}).StartSpan(context.Background(), agentruntime.OpChat)
	return span
}

func TestObserveAggregatesRunAndTree(t *testing.T) {
	r, _, burn := newTestRecorder(t)
	ctx := context.Background()
	s := Sample{
		RunID: "run-1", TreeID: "tree-1", Tenant: "board-x", Region: "eu", Model: "m",
		Tokens: TokenCounts{PromptTokens: 1000, CompletionTokens: 500},
	}
	// custo por chamada = 3000 + 7500 = 10500 micro-USD
	rd1 := r.Observe(ctx, newSpan(), s)
	if rd1.Err != nil {
		t.Fatalf("Observe: %v", rd1.Err)
	}
	if rd1.Amount.CostMicroUSD != 10500 {
		t.Fatalf("custo por chamada errado: %d", rd1.Amount.CostMicroUSD)
	}
	rd2 := r.Observe(ctx, newSpan(), s)
	if rd2.RunCumulative.CostMicroUSD != 21000 || rd2.TreeCumulative.CostMicroUSD != 21000 {
		t.Fatalf("cumulativos errados: run=%d tree=%d", rd2.RunCumulative.CostMicroUSD, rd2.TreeCumulative.CostMicroUSD)
	}
	// Leitura pull (burn-down/admission).
	runAgg, ok := r.CostForRun(RunKey{RunID: "run-1", Tenant: "board-x"})
	if !ok || runAgg.CostMicroUSD != 21000 || runAgg.Tokens != 3000 {
		t.Fatalf("CostForRun errado: %+v ok=%v", runAgg, ok)
	}
	treeAgg, ok := r.CostForTree(TreeKey{TreeID: "tree-1", Tenant: "board-x"})
	if !ok || treeAgg.CostMicroUSD != 21000 {
		t.Fatalf("CostForTree errado: %+v ok=%v", treeAgg, ok)
	}
	// Burn-down sink recebeu os incrementos por eixo (run + tree por chamada).
	if got, _ := burn.Run("run-1", "board-x"); got.CostMicroUSD != 21000 {
		t.Fatalf("burn-down run cumulativo errado: %d", got.CostMicroUSD)
	}
	if got, _ := burn.Tree("tree-1", "board-x"); got.CostMicroUSD != 21000 {
		t.Fatalf("burn-down tree cumulativo errado: %d", got.CostMicroUSD)
	}
}

func TestObserveIsolatesDistinctKeys(t *testing.T) {
	r, _, _ := newTestRecorder(t)
	ctx := context.Background()
	base := TokenCounts{PromptTokens: 1000, CompletionTokens: 500}
	r.Observe(ctx, nil, Sample{RunID: "run-a", TreeID: "tree-a", Tenant: "board-1", Region: "eu", Model: "m", Tokens: base})
	r.Observe(ctx, nil, Sample{RunID: "run-b", TreeID: "tree-b", Tenant: "board-1", Region: "eu", Model: "m", Tokens: base})
	// runs/árvores distintos não se contaminam.
	a, _ := r.CostForRun(RunKey{RunID: "run-a", Tenant: "board-1"})
	b, _ := r.CostForRun(RunKey{RunID: "run-b", Tenant: "board-1"})
	if a.CostMicroUSD != 10500 || b.CostMicroUSD != 10500 {
		t.Fatalf("isolamento por run falhou: a=%d b=%d", a.CostMicroUSD, b.CostMicroUSD)
	}
	// mesmo tenant, árvore diferente → chaves distintas.
	ta, _ := r.CostForTree(TreeKey{TreeID: "tree-a", Tenant: "board-1"})
	if ta.CostMicroUSD != 10500 {
		t.Fatalf("arvore a errada: %d", ta.CostMicroUSD)
	}
	// tenant diferente com mesmo runID é isolado.
	if _, ok := r.CostForRun(RunKey{RunID: "run-a", Tenant: "board-2"}); ok {
		t.Fatalf("tenant diferente nao devia partilhar agregado")
	}
}

func TestObserveNoPriceFailClosedNoAggregation(t *testing.T) {
	r, metrics, burn := newTestRecorder(t)
	ctx := context.Background()
	rd := r.Observe(ctx, newSpan(), Sample{
		RunID: "run-x", TreeID: "tree-x", Tenant: "t", Region: "ap-south", Model: "m",
		Tokens: TokenCounts{PromptTokens: 1000},
	})
	if !errors.Is(rd.Err, ErrNoPrice) {
		t.Fatalf("esperava ErrNoPrice, obtive %v", rd.Err)
	}
	// Nada agregado nem emitido (nunca 0 silencioso).
	if _, ok := r.CostForRun(RunKey{RunID: "run-x", Tenant: "t"}); ok {
		t.Fatalf("nao devia agregar um custo nao-calculavel")
	}
	if len(metrics.Metrics()) != 0 {
		t.Fatalf("nao devia emitir metrica para custo nao-calculavel")
	}
	if len(burn.Entries()) != 0 {
		t.Fatalf("nao devia alimentar burn-down para custo nao-calculavel")
	}
}

func TestObserveEmitsSpanAndMetric(t *testing.T) {
	r, metrics, _ := newTestRecorder(t)
	tr := &agentruntime.RecordingTracer{}
	_, span := tr.StartSpan(context.Background(), agentruntime.OpChat)
	r.Observe(context.Background(), span, Sample{
		RunID: "run-1", TreeID: "tree-1", Tenant: "board-x", Region: "eu", Model: "m",
		Tokens: TokenCounts{PromptTokens: 1000, CompletionTokens: 500, CacheReadTokens: 200, CacheWriteTokens: 100},
	})
	sp := tr.SpansByOperation(agentruntime.OpChat)[0]
	// Custo exacto em micro-USD (sem float drift) + USD (conveniência).
	if sp.Attributes[AttrCostMicroUSD] == nil {
		t.Fatalf("span sem custo micro-USD")
	}
	if _, ok := sp.Attributes[AttrCostUSD].(float64); !ok {
		t.Fatalf("span sem custo USD float: %v", sp.Attributes[AttrCostUSD])
	}
	// gen_ai.usage.* de cache + modelo/região (ligacao a modelo/regiao/trajectoria).
	if sp.Attributes[AttrCacheReadTokens] != int64(200) || sp.Attributes[AttrCacheWriteTokens] != int64(100) {
		t.Fatalf("tokens de cache errados no span: %v %v", sp.Attributes[AttrCacheReadTokens], sp.Attributes[AttrCacheWriteTokens])
	}
	if sp.Attributes[AttrModel] != "m" || sp.Attributes[AttrRegion] != "eu" {
		t.Fatalf("modelo/regiao em falta no span")
	}
	if sp.Attributes[AttrPricingVersion] == nil {
		t.Fatalf("versao de preco em falta no span")
	}
	// Métrica emitida por chamada + agregados run/tree.
	scopes := map[string]bool{}
	for _, m := range metrics.Metrics() {
		if m.Name != MetricCost {
			t.Fatalf("nome de metrica errado: %s", m.Name)
		}
		scopes[m.Scope] = true
	}
	for _, want := range []string{"call", "run", "tree"} {
		if !scopes[want] {
			t.Fatalf("faltou metrica de escopo %q", want)
		}
	}
}

func TestObserveEmptyRunTreeNotAggregated(t *testing.T) {
	r, metrics, _ := newTestRecorder(t)
	// Sem RunID/TreeID: a chamada é metrada (metrica call) mas nao agregada.
	rd := r.Observe(context.Background(), nil, Sample{Tenant: "t", Region: "eu", Model: "m", Tokens: TokenCounts{PromptTokens: 1000}})
	if rd.Err != nil {
		t.Fatalf("Observe: %v", rd.Err)
	}
	// A metrica DA CHAMADA sai; nenhum agregado run/tree.
	for _, m := range metrics.Metrics() {
		if m.Scope == "run" || m.Scope == "tree" {
			t.Fatalf("nao devia haver agregado sem run/tree id")
		}
	}
}

func TestSampleFromUsage(t *testing.T) {
	u := port.Usage{PromptTokens: 1000, CompletionTokens: 500, CacheReadTokens: 200, CacheWriteTokens: 100}
	s := SampleFromUsage("r", "tr", "ten", "eu", "m", u)
	if s.Tokens.PromptTokens != 1000 || s.Tokens.CacheReadTokens != 200 || s.Tokens.CacheWriteTokens != 100 {
		t.Fatalf("projeccao errada: %+v", s.Tokens)
	}
	if s.RunID != "r" || s.TreeID != "tr" || s.Tenant != "ten" || s.Region != "eu" || s.Model != "m" {
		t.Fatalf("eixos errados: %+v", s)
	}
}
