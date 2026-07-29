package activity_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/activity"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/saga"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// Harness: RM real (AOS-003) + Event Store real (AOS-002) + StepLedger real
// (AOS-014). O "efeito externo" é uma tool-espia registada no RM com um contador
// atómico — a ÚNICA via de a incrementar é rm.Mediate sob permit.
// ---------------------------------------------------------------------------

const (
	testTool = "spy.tool"
	testRun  = "run-activity-1"
	testStep = "step-000001-tool-1"
)

// spyTool é o efeito externo instrumentado: conta as execuções REAIS (só sobem sob
// permit + despacho do RM) e devolve um output determinístico. costMicroUSD, se != 0,
// é o custo MEDIDO que a tool REPORTA ao RM (registada via RegisterCosting) — a fonte
// declarada do custo do efeito de AOS-212.
type spyTool struct {
	calls        atomic.Int64
	output       []byte
	err          error
	costMicroUSD int64
}

func (s *spyTool) fn(_ context.Context, in []byte) ([]byte, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	if s.output != nil {
		return s.output, nil
	}
	return append([]byte("out:"), in...), nil
}

// costingFn é a forma [referencemonitor.CostingToolFunc] da spy: reporta o custo
// medido do efeito (costMicroUSD) além do output.
func (s *spyTool) costingFn(ctx context.Context, in []byte) ([]byte, int64, error) {
	out, err := s.fn(ctx, in)
	return out, s.costMicroUSD, err
}

// denyHook nega sempre — simula uma política que barra o efeito (fail-closed).
type denyHook struct{}

func (denyHook) Name() string { return "test-deny" }
func (denyHook) Evaluate(context.Context, *referencemonitor.Call) (referencemonitor.HookResult, error) {
	return referencemonitor.HookResult{Decision: referencemonitor.HookDeny, Reason: "negado no teste"}, nil
}

type harness struct {
	store  *eventstore.Store
	rm     *referencemonitor.Monitor
	ledger *durable.StepLedger
	spy    *spyTool
}

// newHarness constrói o harness. deny=true instala uma cadeia que nega; caso
// contrário usa os stubs neutros (permit). toolErr injecta um erro na tool.
func newHarness(t testing.TB, deny bool, toolErr error) *harness {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	spy := &spyTool{err: toolErr}
	sink := referencemonitor.NewEventStoreSink(store)
	var rm *referencemonitor.Monitor
	if deny {
		rm = referencemonitor.New(referencemonitor.WithEventSink(sink), referencemonitor.WithHooks(denyHook{}))
	} else {
		rm = referencemonitor.New(referencemonitor.WithEventSink(sink))
	}
	// Registada via RegisterCosting: a spy pode reportar o custo MEDIDO do efeito
	// (h.spy.costMicroUSD, lido no momento da execução). Com custo 0 o comportamento é
	// idêntico a Register — é o default dos restantes testes.
	if err := rm.RegisterCosting(testTool, spy.costingFn); err != nil {
		t.Fatalf("RegisterCosting(%s): %v", testTool, err)
	}
	ledger, err := durable.NewStepLedger(store)
	if err != nil {
		t.Fatalf("NewStepLedger: %v", err)
	}
	return &harness{store: store, rm: rm, ledger: ledger, spy: spy}
}

func baseActivity() activity.Activity {
	return activity.Activity{
		RunID:      testRun,
		StepID:     testStep,
		ToolID:     testTool,
		Capability: "cap:spy",
		Input:      []byte("payload"),
	}
}

// ---------------------------------------------------------------------------
// MEDIAÇÃO / NO-BYPASS: nenhuma activity executa sem passar pelo RM.
// ---------------------------------------------------------------------------

func TestDispatch_MediacaoPermitExecutaUmaVez(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	d, err := activity.NewDispatcher(h.rm, h.ledger)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}

	res, err := d.Dispatch(context.Background(), baseActivity())
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got := h.spy.calls.Load(); got != 1 {
		t.Fatalf("efeito devia correr 1x sob permit, correu %d", got)
	}
	if !res.Output.IsUntrusted() {
		t.Fatalf("resultado devia vir untrusted, taint=%q", res.Output.Taint)
	}
	if string(res.Output.Value) != "out:payload" {
		t.Fatalf("output inesperado: %q", res.Output.Value)
	}
	if res.Deduplicated || res.Replayed {
		t.Fatalf("primeira execução não devia ser dedup/replay: %+v", res)
	}
}

func TestDispatch_DenyNaoExecutaEfeito(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true, nil)
	d, err := activity.NewDispatcher(h.rm, h.ledger)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}

	_, err = d.Dispatch(context.Background(), baseActivity())
	if !errors.Is(err, activity.ErrMediationDenied) {
		t.Fatalf("deny devia dar ErrMediationDenied, veio %v", err)
	}
	// A prova de no-bypass: o efeito NÃO correu porque o RM negou antes do despacho.
	if got := h.spy.calls.Load(); got != 0 {
		t.Fatalf("efeito NÃO devia correr sob deny (no-bypass), correu %d", got)
	}
	// Nada foi memorizado: o passo não ficou aplicado.
	if _, ok := h.ledger.Applied(testRun + ":" + testStep); ok {
		t.Fatalf("uma activity negada não devia ficar no ledger")
	}
}

// TestDispatch_ToolPermitidaFalhaDownstream: a tool foi PERMITIDA (efeito correu) mas
// falhou a jusante — ErrToolExecution, nada memorizado, retriável.
func TestDispatch_ToolPermitidaFalhaDownstream(t *testing.T) {
	t.Parallel()
	toolErr := errors.New("timeout downstream")
	h := newHarness(t, false, toolErr)
	d, err := activity.NewDispatcher(h.rm, h.ledger)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}

	_, err = d.Dispatch(context.Background(), baseActivity())
	if !errors.Is(err, activity.ErrToolExecution) {
		t.Fatalf("erro de tool devia dar ErrToolExecution, veio %v", err)
	}
	var te *activity.ToolError
	if !errors.As(err, &te) || !errors.Is(te.Err, toolErr) {
		t.Fatalf("ToolError devia embrulhar o erro original, veio %v", err)
	}
	if _, ok := h.ledger.Applied(testRun + ":" + testStep); ok {
		t.Fatalf("uma tool falhada não devia ficar memorizada (retriável)")
	}
}

// ---------------------------------------------------------------------------
// IDEMPOTÊNCIA: reexecutar a activity NÃO duplica o efeito (cruza AOS-014).
// ---------------------------------------------------------------------------

func TestDispatch_IdempotenteNaoDuplica(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	d, err := activity.NewDispatcher(h.rm, h.ledger)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	ctx := context.Background()

	first, err := d.Dispatch(ctx, baseActivity())
	if err != nil {
		t.Fatalf("Dispatch #1: %v", err)
	}
	second, err := d.Dispatch(ctx, baseActivity())
	if err != nil {
		t.Fatalf("Dispatch #2: %v", err)
	}

	if got := h.spy.calls.Load(); got != 1 {
		t.Fatalf("reexecução NÃO devia duplicar o efeito, correu %d vezes", got)
	}
	if !second.Deduplicated {
		t.Fatalf("segunda chamada devia ser dedup (already-applied)")
	}
	if second.Replayed {
		t.Fatalf("dedup não é replay")
	}
	if string(first.Output.Value) != string(second.Output.Value) {
		t.Fatalf("resultado do dedup devia ser idêntico: %q vs %q", first.Output.Value, second.Output.Value)
	}
	if !second.Output.IsUntrusted() {
		t.Fatalf("resultado do dedup também vem untrusted")
	}
}

// TestDispatch_IdempotenteConcorrente: Applies concorrentes da mesma key colapsam num
// único efeito (single-flight do ledger).
func TestDispatch_IdempotenteConcorrente(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	d, err := activity.NewDispatcher(h.rm, h.ledger)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	ctx := context.Background()

	const n = 16
	var wg sync.WaitGroup
	errs := make([]error, n)
	outs := make([][]byte, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, e := d.Dispatch(ctx, baseActivity())
			errs[i], outs[i] = e, res.Output.Value
		}(i)
	}
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Fatalf("Dispatch concorrente #%d: %v", i, e)
		}
		if string(outs[i]) != "out:payload" {
			t.Fatalf("output concorrente #%d divergiu: %q", i, outs[i])
		}
	}
	if got := h.spy.calls.Load(); got != 1 {
		t.Fatalf("efeito concorrente devia colapsar em 1, correu %d", got)
	}
}

// ---------------------------------------------------------------------------
// REPLAY: activity em modo replay devolve o resultado REGISTADO, ZERO efeito.
// ---------------------------------------------------------------------------

func TestDispatch_ReplayZeroEfeito(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	ctx := context.Background()

	// 1) Execução normal grava o resultado no ledger (append-only no Event Store).
	normal, err := activity.NewDispatcher(h.rm, h.ledger)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	want, err := normal.Dispatch(ctx, baseActivity())
	if err != nil {
		t.Fatalf("Dispatch normal: %v", err)
	}
	if h.spy.calls.Load() != 1 {
		t.Fatalf("execução normal devia correr o efeito 1x")
	}

	// 2) Um dispatcher de REPLAY sobre a MESMA fonte (o ledger, alimentado por AOS-014/016)
	//    devolve o registado — SEM RM, SEM efeito.
	replay, err := activity.NewReplayDispatcher(h.ledger)
	if err != nil {
		t.Fatalf("NewReplayDispatcher: %v", err)
	}
	got, err := replay.Dispatch(ctx, baseActivity())
	if err != nil {
		t.Fatalf("Dispatch replay: %v", err)
	}
	if h.spy.calls.Load() != 1 {
		t.Fatalf("REPLAY não devia correr efeito: contador subiu para %d", h.spy.calls.Load())
	}
	if !got.Replayed {
		t.Fatalf("resultado de replay devia ter Replayed=true: %+v", got)
	}
	if !got.Output.IsUntrusted() {
		t.Fatalf("resultado de replay também vem untrusted")
	}
	if string(got.Output.Value) != string(want.Output.Value) {
		t.Fatalf("replay devia devolver o resultado registado: %q vs %q", got.Output.Value, want.Output.Value)
	}
	if replay.Mode() != activity.ModeReplay {
		t.Fatalf("modo devia ser replay")
	}
}

func TestDispatch_ReplayMiss(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	replay, err := activity.NewReplayDispatcher(h.ledger) // ledger vazio
	if err != nil {
		t.Fatalf("NewReplayDispatcher: %v", err)
	}
	_, err = replay.Dispatch(context.Background(), baseActivity())
	if !errors.Is(err, activity.ErrReplayMiss) {
		t.Fatalf("replay sem registo devia dar ErrReplayMiss, veio %v", err)
	}
	// Confirmação estrutural: nenhum efeito, nem sequer há RM ligado.
	if h.spy.calls.Load() != 0 {
		t.Fatalf("replay-miss não devia correr efeito")
	}
}

// ---------------------------------------------------------------------------
// COMPENSAÇÃO (AOS-020): a activity regista a acção inversa no registry pelo step_id.
// ---------------------------------------------------------------------------

func TestDispatch_RegistaCompensacao(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	reg := saga.NewCompensationRegistry()
	d, err := activity.NewDispatcher(h.rm, h.ledger, activity.WithCompensationRegistry(reg))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}

	var undone atomic.Bool
	act := baseActivity()
	act.Compensation = &activity.Compensation{
		Action: func(context.Context) error { undone.Store(true); return nil },
		Reason: "desfazer spy",
	}
	if _, err := d.Dispatch(context.Background(), act); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	if reg.Len() != 1 {
		t.Fatalf("compensação devia ficar registada, Len=%d", reg.Len())
	}
	comp, ok := reg.Lookup(testStep)
	if !ok {
		t.Fatalf("compensação devia estar ancorada ao step_id %q", testStep)
	}
	if comp.Reason != "desfazer spy" {
		t.Fatalf("rótulo da compensação inesperado: %q", comp.Reason)
	}
	if err := comp.Action(context.Background()); err != nil || !undone.Load() {
		t.Fatalf("acção inversa registada devia ser executável")
	}
}

// TestDispatch_DedupRegistaCompensacao prova a correcção de AOS021-Q1: a INTENÇÃO de
// compensar sobrevive ao dedup. Um worker NOVO (registry vazio) que RE-DESPACHA um
// passo já aplicado sobre o MESMO ledger obtém dedup (efeito NÃO re-corre) MAS restaura
// a compensação no seu registry — sem isto, a saga percorreria um registry vazio e
// transitaria compensating→ready sem reverter nada.
func TestDispatch_DedupRegistaCompensacao(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	ctx := context.Background()

	// Worker A: aplica o efeito e regista a compensação.
	regA := saga.NewCompensationRegistry()
	dA, err := activity.NewDispatcher(h.rm, h.ledger, activity.WithCompensationRegistry(regA))
	if err != nil {
		t.Fatalf("NewDispatcher A: %v", err)
	}
	act := baseActivity()
	act.Compensation = &activity.Compensation{
		Action: func(context.Context) error { return nil },
		Reason: "desfazer spy",
	}
	if _, err := dA.Dispatch(ctx, act); err != nil {
		t.Fatalf("Dispatch A: %v", err)
	}
	if h.spy.calls.Load() != 1 {
		t.Fatalf("efeito devia correr 1x no worker A, correu %d", h.spy.calls.Load())
	}

	// Worker B: registry VAZIO, MESMO ledger (crash-resume cross-worker). Re-despacha.
	regB := saga.NewCompensationRegistry()
	dB, err := activity.NewDispatcher(h.rm, h.ledger, activity.WithCompensationRegistry(regB))
	if err != nil {
		t.Fatalf("NewDispatcher B: %v", err)
	}
	res, err := dB.Dispatch(ctx, act)
	if err != nil {
		t.Fatalf("Dispatch B (dedup): %v", err)
	}
	if !res.Deduplicated {
		t.Fatalf("re-despacho no worker B devia ser dedup (already-applied)")
	}
	if h.spy.calls.Load() != 1 {
		t.Fatalf("dedup NÃO devia re-correr o efeito, contador=%d", h.spy.calls.Load())
	}
	if regB.Len() != 1 {
		t.Fatalf("worker B devia RESTAURAR a compensação no dedup, Len=%d", regB.Len())
	}
	if _, ok := regB.Lookup(testStep); !ok {
		t.Fatalf("compensação devia ficar ancorada ao step_id %q após dedup", testStep)
	}
}

// TestDispatch_CompensacaoActionNil: uma compensação com Action nil é recusada
// fail-closed ANTES de qualquer efeito (AOS021-Q1).
func TestDispatch_CompensacaoActionNil(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	reg := saga.NewCompensationRegistry()
	d, err := activity.NewDispatcher(h.rm, h.ledger, activity.WithCompensationRegistry(reg))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	act := baseActivity()
	act.Compensation = &activity.Compensation{Action: nil, Reason: "sem accao"}

	_, err = d.Dispatch(context.Background(), act)
	if !errors.Is(err, activity.ErrNilCompensationAction) {
		t.Fatalf("Action nil devia dar ErrNilCompensationAction, veio %v", err)
	}
	if h.spy.calls.Load() != 0 {
		t.Fatalf("recusa ANTES do efeito: a tool não devia correr, correu %d", h.spy.calls.Load())
	}
	if reg.Len() != 0 {
		t.Fatalf("nada devia ficar registado, Len=%d", reg.Len())
	}
}

// TestDispatch_CustoSoNoApplied prova AOS021-Q5 + AOS-212 (fidelidade de replay, o eixo
// de risco #1): o custo por span vem do DESFECHO do efeito (a tool reporta-o ao RM), é
// emitido SÓ no efeito REAL (applied) e NUNCA num dedup do mesmo step_id — senão um
// agregador somaria o custo por retry. A 1.ª passagem emite C; a 2.ª (already-applied,
// sem re-incorrer o efeito) emite 0 — porque o custo não vive no durable.Result do ledger.
func TestDispatch_CustoSoNoApplied(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	h.spy.costMicroUSD = 1_000_000 // 1.0 USD — custo MEDIDO reportado pela tool ao RM
	tracer := &agentruntime.RecordingTracer{}
	d, err := activity.NewDispatcher(h.rm, h.ledger, activity.WithTracer(tracer))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	act := baseActivity()
	ctx := context.Background()

	if _, err := d.Dispatch(ctx, act); err != nil {
		t.Fatalf("Dispatch #1 (applied): %v", err)
	}
	if _, err := d.Dispatch(ctx, act); err != nil {
		t.Fatalf("Dispatch #2 (dedup): %v", err)
	}

	spans := tracer.SpansByOperation(activity.OpActivity)
	if len(spans) != 2 {
		t.Fatalf("esperados 2 spans aos.activity, houve %d", len(spans))
	}
	// span[0] = applied: emite custo (USD float + micro-USD inteiro). span[1] = dedup: NÃO.
	if got, ok := spans[0].Attributes[agentruntime.AttrCostUSD]; !ok || got != 1.0 {
		t.Fatalf("span applied devia ter custo 1.0, veio %v (ok=%v)", got, ok)
	}
	if got, ok := spans[0].Attributes[agentruntime.AttrCostMicroUSD]; !ok || got != int64(1_000_000) {
		t.Fatalf("span applied devia ter custo micro-USD 1_000_000 (fonte de verdade), veio %v (ok=%v)", got, ok)
	}
	if spans[0].Attributes[activity.AttrDecision] != "permit" {
		t.Fatalf("span[0] devia ser permit, veio %v", spans[0].Attributes[activity.AttrDecision])
	}
	if _, ok := spans[1].Attributes[agentruntime.AttrCostUSD]; ok {
		t.Fatalf("span dedup NÃO devia emitir custo (nenhum custo incorrido no retry)")
	}
	if _, ok := spans[1].Attributes[agentruntime.AttrCostMicroUSD]; ok {
		t.Fatalf("span dedup NÃO devia emitir custo micro-USD (o custo não vive no ledger)")
	}
	if spans[1].Attributes[activity.AttrDecision] != "dedup" {
		t.Fatalf("span[1] devia ser dedup, veio %v", spans[1].Attributes[activity.AttrDecision])
	}
	if h.spy.calls.Load() != 1 {
		t.Fatalf("o efeito real devia correr EXACTAMENTE 1 vez (dedup nao re-executa), correu %d", h.spy.calls.Load())
	}
}

// TestDispatch_ReplayEmiteZeroCusto é a prova DEDICADA da fidelidade de replay (AOS-212,
// risco #1): um dispatcher em ModeReplay reproduz o desfecho REGISTADO do ledger com ZERO
// efeito — e o span aos.activity do replay NÃO traz custo, porque o custo é canal lateral
// da closure de Apply (que só corre no efeito real) e NUNCA foi gravado no durable.Result.
func TestDispatch_ReplayEmiteZeroCusto(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	h.spy.costMicroUSD = 3_000_000 // 3.0 USD no efeito REAL
	ctx := context.Background()

	// 1.ª passagem: efeito real (applied) — grava o desfecho no ledger.
	appliedTracer := &agentruntime.RecordingTracer{}
	dNormal, err := activity.NewDispatcher(h.rm, h.ledger, activity.WithTracer(appliedTracer))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if _, err := dNormal.Dispatch(ctx, baseActivity()); err != nil {
		t.Fatalf("Dispatch (applied): %v", err)
	}
	if got, ok := appliedTracer.SpansByOperation(activity.OpActivity)[0].Attributes[agentruntime.AttrCostMicroUSD]; !ok || got != int64(3_000_000) {
		t.Fatalf("applied devia emitir custo 3_000_000, veio %v (ok=%v)", got, ok)
	}

	// Reconstrói o ledger a partir do stream e cria um dispatcher de REPLAY sobre ele.
	if err := h.ledger.Rebuild(ctx, testRun); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	replayTracer := &agentruntime.RecordingTracer{}
	dReplay, err := activity.NewReplayDispatcher(h.ledger, activity.WithTracer(replayTracer))
	if err != nil {
		t.Fatalf("NewReplayDispatcher: %v", err)
	}
	res, err := dReplay.Dispatch(ctx, baseActivity())
	if err != nil {
		t.Fatalf("Dispatch (replay): %v", err)
	}
	if !res.Replayed {
		t.Fatalf("o resultado devia vir do log (Replayed), veio %+v", res)
	}
	// O efeito NÃO voltou a correr (replay não medeia nem executa).
	if h.spy.calls.Load() != 1 {
		t.Fatalf("replay nao pode re-incorrer o efeito; a tool correu %d vezes", h.spy.calls.Load())
	}
	spans := replayTracer.SpansByOperation(activity.OpActivity)
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span aos.activity no replay, houve %d", len(spans))
	}
	if spans[0].Attributes[activity.AttrDecision] != "replay" {
		t.Fatalf("o span devia ser replay, veio %v", spans[0].Attributes[activity.AttrDecision])
	}
	// A PROVA: o replay do MESMO step emite ZERO custo (o custo nunca esteve no ledger).
	if _, ok := spans[0].Attributes[agentruntime.AttrCostUSD]; ok {
		t.Fatalf("replay NAO pode emitir custo USD — o efeito nao foi re-incorrido")
	}
	if _, ok := spans[0].Attributes[agentruntime.AttrCostMicroUSD]; ok {
		t.Fatalf("replay NAO pode emitir custo micro-USD — o custo nunca foi gravado no durable.Result")
	}
}

func TestDispatch_CompensacaoSemRegistryFalha(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	d, err := activity.NewDispatcher(h.rm, h.ledger) // sem WithCompensationRegistry
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	act := baseActivity()
	act.Compensation = &activity.Compensation{Action: func(context.Context) error { return nil }}

	_, err = d.Dispatch(context.Background(), act)
	if !errors.Is(err, activity.ErrNoRegistry) {
		t.Fatalf("compensação sem registry devia dar ErrNoRegistry, veio %v", err)
	}
	if h.spy.calls.Load() != 0 {
		t.Fatalf("recusa antes do efeito: a tool não devia correr")
	}
}

// ---------------------------------------------------------------------------
// OBSERVABILIDADE: custo por span + contadores; resultado untrusted (taint).
// ---------------------------------------------------------------------------

// countingObserver conta os desfechos observados (chaves sempre opacas).
type countingObserver struct {
	applied, dedup, replayed, denied atomic.Int64
	lastHash                         atomic.Value
}

func (o *countingObserver) Applied(h string)      { o.applied.Add(1); o.lastHash.Store(h) }
func (o *countingObserver) Deduplicated(h string) { o.dedup.Add(1); o.lastHash.Store(h) }
func (o *countingObserver) Replayed(h string)     { o.replayed.Add(1); o.lastHash.Store(h) }
func (o *countingObserver) Denied(h string)       { o.denied.Add(1); o.lastHash.Store(h) }

func TestDispatch_ObservabilidadeCustoPorSpan(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	tracer := &agentruntime.RecordingTracer{}
	obs := &countingObserver{}
	d, err := activity.NewDispatcher(h.rm, h.ledger, activity.WithTracer(tracer), activity.WithObserver(obs))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}

	h.spy.costMicroUSD = 2_500_000 // 2.5 USD — custo MEDIDO reportado pela tool ao RM
	act := baseActivity()
	if _, err := d.Dispatch(context.Background(), act); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	spans := tracer.SpansByOperation(activity.OpActivity)
	if len(spans) != 1 {
		t.Fatalf("devia haver 1 span aos.activity, houve %d", len(spans))
	}
	s := spans[0]
	if !s.Ended {
		t.Fatalf("o span devia estar fechado")
	}
	if got := s.Attributes[agentruntime.AttrToolName]; got != testTool {
		t.Fatalf("span devia anotar a tool, veio %v", got)
	}
	if got := s.Attributes[agentruntime.AttrCostUSD]; got != 2.5 {
		t.Fatalf("custo por span devia ser 2.5 USD, veio %v", got)
	}
	if got := s.Attributes[activity.AttrDecision]; got != "permit" {
		t.Fatalf("decisão do span devia ser permit, veio %v", got)
	}
	if obs.applied.Load() != 1 {
		t.Fatalf("observer devia contar 1 applied, veio %d", obs.applied.Load())
	}
	// A hash observada é OPACA (não é a chave em claro).
	if hs, _ := obs.lastHash.Load().(string); hs == "" || hs == testRun+":"+testStep {
		t.Fatalf("observer devia receber a key OPACA (hash), veio %q", hs)
	}
}

// TestDispatch_TracerPartilhadoUmSoExecuteTool prova a reconciliação de AOS-076 no
// caminho DURÁVEL (AOS-021): quando o MESMO tracer é partilhado pelo dispatcher e pelo
// Reference Monitor — a forma natural de os dois spans caírem na mesma árvore — cada
// tool call produz EXACTAMENTE UM span execute_tool (aberto SÓ pelo RM, a autoridade
// única, ADR-002) a carregar os atributos obrigatórios de CA2 (hash(tool+args) +
// result_taint), e UM span de escopo durável aos.activity (aberto pelo dispatcher) com
// o desfecho. O execute_tool nasce FILHO do aos.activity (mesma trace, parent = span do
// dispatcher). Sem a distinção de operações de AOS-076, esta configuração emitiria DOIS
// spans execute_tool (duplo-contar) e o do dispatcher falharia a semconv CA2.
func TestDispatch_TracerPartilhadoUmSoExecuteTool(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	// Um único tracer partilhado: o RM abre o execute_tool, o dispatcher o aos.activity.
	tracer := &agentruntime.RecordingTracer{}
	h.rm.SetTracer(tracer)
	d, err := activity.NewDispatcher(h.rm, h.ledger, activity.WithTracer(tracer))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}

	if _, err := d.Dispatch(context.Background(), baseActivity()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// EXACTAMENTE um execute_tool, e é o do RM (não o do dispatcher).
	tools := tracer.SpansByOperation(agentruntime.OpExecuteTool)
	if len(tools) != 1 {
		t.Fatalf("esperava 1 span execute_tool (aberto SÓ pelo RM), obtive %d", len(tools))
	}
	tool := tools[0]
	// CA2: o execute_tool traz hash(tool+args) e a marca untrusted do resultado.
	if hash, _ := tool.Attributes[agentruntime.AttrToolCallHash].(string); len(hash) == 0 {
		t.Errorf("execute_tool sem hash(tool+args) (CA2): %v", tool.Attributes[agentruntime.AttrToolCallHash])
	}
	if tool.Attributes[agentruntime.AttrResultTaint] != "untrusted" {
		t.Errorf("execute_tool result_taint = %v, esperava \"untrusted\" (CA2)", tool.Attributes[agentruntime.AttrResultTaint])
	}

	// Um span de escopo durável aos.activity, com o desfecho — e SEM se apresentar como
	// execute_tool (não duplica nem falha CA2).
	acts := tracer.SpansByOperation(activity.OpActivity)
	if len(acts) != 1 {
		t.Fatalf("esperava 1 span aos.activity (do dispatcher), obtive %d", len(acts))
	}
	if got := acts[0].Attributes[activity.AttrDecision]; got != "permit" {
		t.Errorf("aos.activity decisão = %v, esperava permit", got)
	}

	// Topologia: o execute_tool é FILHO do aos.activity (mesma trace, parent = dispatcher).
	if tool.SpanContext.TraceID != acts[0].SpanContext.TraceID {
		t.Errorf("execute_tool devia partilhar o trace_id do aos.activity")
	}
	if tool.ParentSpanID != acts[0].SpanContext.SpanID {
		t.Errorf("execute_tool devia ser filho do aos.activity (parent_span_id != span_id do dispatcher)")
	}
}

func TestDispatch_ObserverDedupEReplay(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	obs := &countingObserver{}
	ctx := context.Background()

	d, err := activity.NewDispatcher(h.rm, h.ledger, activity.WithObserver(obs))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if _, err := d.Dispatch(ctx, baseActivity()); err != nil {
		t.Fatalf("Dispatch #1: %v", err)
	}
	if _, err := d.Dispatch(ctx, baseActivity()); err != nil {
		t.Fatalf("Dispatch #2: %v", err)
	}
	if obs.applied.Load() != 1 || obs.dedup.Load() != 1 {
		t.Fatalf("esperado applied=1 dedup=1, veio applied=%d dedup=%d", obs.applied.Load(), obs.dedup.Load())
	}

	robs := &countingObserver{}
	replay, err := activity.NewReplayDispatcher(h.ledger, activity.WithObserver(robs))
	if err != nil {
		t.Fatalf("NewReplayDispatcher: %v", err)
	}
	if _, err := replay.Dispatch(ctx, baseActivity()); err != nil {
		t.Fatalf("Dispatch replay: %v", err)
	}
	if robs.replayed.Load() != 1 {
		t.Fatalf("observer de replay devia contar 1 replayed, veio %d", robs.replayed.Load())
	}
}

func TestDispatch_ObserverDenied(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true, nil)
	obs := &countingObserver{}
	d, err := activity.NewDispatcher(h.rm, h.ledger, activity.WithObserver(obs))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if _, err := d.Dispatch(context.Background(), baseActivity()); !errors.Is(err, activity.ErrMediationDenied) {
		t.Fatalf("esperado ErrMediationDenied, veio %v", err)
	}
	if obs.denied.Load() != 1 {
		t.Fatalf("observer devia contar 1 denied, veio %d", obs.denied.Load())
	}
}

// ---------------------------------------------------------------------------
// CONSTRUÇÃO / VALIDAÇÃO.
// ---------------------------------------------------------------------------

func TestNewDispatcher_Validacao(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	if _, err := activity.NewDispatcher(nil, h.ledger); !errors.Is(err, activity.ErrNilMediator) {
		t.Fatalf("rm nil devia dar ErrNilMediator, veio %v", err)
	}
	if _, err := activity.NewDispatcher(h.rm, nil); !errors.Is(err, activity.ErrNilLedger) {
		t.Fatalf("ledger nil devia dar ErrNilLedger, veio %v", err)
	}
	if _, err := activity.NewReplayDispatcher(nil); !errors.Is(err, activity.ErrNilReplaySource) {
		t.Fatalf("replay source nil devia dar ErrNilReplaySource, veio %v", err)
	}
}

func TestDispatch_ActivityInvalida(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	d, err := activity.NewDispatcher(h.rm, h.ledger)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	cases := []struct {
		name string
		mut  func(a *activity.Activity)
		want error
	}{
		{"run vazio", func(a *activity.Activity) { a.RunID = "" }, activity.ErrEmptyRunID},
		{"step vazio", func(a *activity.Activity) { a.StepID = "" }, activity.ErrEmptyStepID},
		{"tool vazia", func(a *activity.Activity) { a.ToolID = "" }, activity.ErrEmptyToolID},
		{"run com ':'", func(a *activity.Activity) { a.RunID = "run:x" }, durable.ErrDelimiterInInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := baseActivity()
			tc.mut(&a)
			if _, err := d.Dispatch(context.Background(), a); !errors.Is(err, tc.want) {
				t.Fatalf("%s: esperado %v, veio %v", tc.name, tc.want, err)
			}
		})
	}
}

// TestMode_String cobre a representação textual dos modos.
func TestMode_String(t *testing.T) {
	t.Parallel()
	if activity.ModeNormal.String() != "normal" || activity.ModeReplay.String() != "replay" {
		t.Fatalf("String() dos modos inesperada")
	}
	if activity.Mode(99).String() != "unknown" {
		t.Fatalf("modo desconhecido devia ser 'unknown'")
	}
}

// TestNopObserver exercita o observador default (no-op).
func TestNopObserver(t *testing.T) {
	t.Parallel()
	var o activity.Observer = activity.NopObserver{}
	o.Applied("h")
	o.Deduplicated("h")
	o.Replayed("h")
	o.Denied("h")
}
