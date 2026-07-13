package scheduler_test

// Testes do EXECUTOR de degradação graciosa (AOS-031): as quatro acções
// shed/defer/downgrade/reject na ordem de preferência, downgrade via a porta
// ModelTierRouter com variância explícita, reversibilidade ao normalizar, e
// eventos append-only reconstruídos por replay. Todos deterministas: relógio
// injectável, iteração ordenada, sem time.Now/rand nas decisões. Reutilizam os
// helpers de admission_test.go / backpressure_test.go (mesmo pacote
// scheduler_test): fixedClock, baseFixedClock, testKey.
//
// Cobrem os Testes Requeridos do ticket:
//   - unit: cada acção isolada (shed/defer/downgrade/reject) com o seu evento;
//   - unit: shed NÃO descarta crítico silenciosamente (fail-closed);
//   - integração: cadeia completa sob pressão crescente segue a ORDEM de
//     preferência (shed→defer→downgrade→reject), conduzida pela política de AOS-030;
//   - integração: downgrade encaminha para tier barato via a porta GW e regista
//     VARIÂNCIA (replay fiel); reject é FAIL-CLOSED para irreversíveis;
//   - reversibilidade: ao normalizar a carga, degradações reversíveis revertem
//     (tier restaurado); a variância permanece no log.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/scheduler"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// Fixtures.
// ---------------------------------------------------------------------------

// tierLadder é a escada de tiers de referência (premium→standard→economy), do
// mais caro (CostRank alto) ao mais barato.
func tierRouter() *scheduler.StaticModelTierRouter {
	return scheduler.NewStaticModelTierRouter(
		scheduler.ModelTier{Tier: "premium", Model: "claude-opus", CostRank: 30},
		scheduler.ModelTier{Tier: "standard", Model: "claude-sonnet", CostRank: 20},
		scheduler.ModelTier{Tier: "economy", Model: "claude-haiku", CostRank: 10},
	)
}

// newDegrader constrói um Degrader de teste sobre um Event Store real (AOS-002),
// com relógio fixo e (opcionalmente) mais opções.
func newDegrader(t *testing.T, opts ...scheduler.DegraderOption) (*scheduler.Degrader, *eventstore.Store) {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	base := []scheduler.DegraderOption{
		scheduler.WithDegradationLog(es),
		scheduler.WithDegradationClock(baseFixedClock()),
	}
	d, err := scheduler.NewDegrader(tierRouter(), append(base, opts...)...)
	if err != nil {
		t.Fatalf("NewDegrader: %v", err)
	}
	return d, es
}

// baseItem é um item opcional/diferível/reversível padrão no tier premium. Optional
// é EXPLÍCITO: o guard do shed é fail-closed (só descarta trabalho provadamente
// opcional), logo o fixture do trabalho descartável marca-o.
func baseItem() scheduler.DegradationItem {
	return scheduler.DegradationItem{
		ID:           "w1",
		Tenant:       "acme",
		Priority:     "P2",
		Class:        "batch",
		Optional:     true,
		Deferrable:   true,
		CurrentTier:  "premium",
		CurrentModel: "claude-opus",
		Key:          testKey,
	}
}

func baseTrigger() scheduler.DegradationTrigger {
	return scheduler.DegradationTrigger{Reason: "fila saturada", PolicyVersion: "1.0.0", Partition: "acme:P2", FillRatio: 0.95}
}

// countByType conta os eventos de degradação de um tipo no replay.
func countByType(recs []scheduler.DegradationRecord, evType string) int {
	n := 0
	for _, r := range recs {
		if r.Type == evType {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Unit: NewDegrader fail-closed sem router.
// ---------------------------------------------------------------------------

func TestNewDegrader_NilRouterFailsClosed(t *testing.T) {
	if _, err := scheduler.NewDegrader(nil); err == nil {
		t.Fatal("NewDegrader com router nil devia falhar (fail-closed)")
	}
}

// ---------------------------------------------------------------------------
// Unit: cada acção isolada com o seu evento.
// ---------------------------------------------------------------------------

func TestShed_DiscardsOptionalWithEventAndReason(t *testing.T) {
	ctx := context.Background()
	d, _ := newDegrader(t)

	res, err := d.Shed(ctx, baseItem(), baseTrigger())
	if err != nil {
		t.Fatalf("Shed: %v", err)
	}
	if !res.Applied || res.Action != scheduler.ActionShed {
		t.Fatalf("resultado inesperado: %+v", res)
	}
	recs, err := d.ReplayDegradation(ctx)
	if err != nil {
		t.Fatalf("ReplayDegradation: %v", err)
	}
	if countByType(recs, scheduler.EventWorkShed) != 1 {
		t.Fatalf("esperado 1 evento work_shed, obtido %d (%+v)", countByType(recs, scheduler.EventWorkShed), recs)
	}
	if recs[0].Reason == "" {
		t.Fatal("work_shed deve carregar a RAZÃO")
	}
}

func TestDefer_PreservesWithRetryAfterAndEvent(t *testing.T) {
	ctx := context.Background()
	sink := &recordingDeferSink{}
	d, _ := newDegrader(t, scheduler.WithDeferRetry(5*time.Second), scheduler.WithDeferSink(sink))

	res, err := d.Defer(ctx, baseItem(), baseTrigger())
	if err != nil {
		t.Fatalf("Defer: %v", err)
	}
	if !res.Applied || res.RetryAfter != 5*time.Second {
		t.Fatalf("defer inesperado: %+v", res)
	}
	if sink.count != 1 || sink.lastRetry != 5*time.Second {
		t.Fatalf("DeferSink não preservou o trabalho: count=%d retry=%v", sink.count, sink.lastRetry)
	}
	recs, _ := d.ReplayDegradation(ctx)
	if countByType(recs, scheduler.EventWorkDeferred) != 1 {
		t.Fatalf("esperado 1 evento work_deferred (%+v)", recs)
	}
	if recs[0].RetryAfter != 5*time.Second {
		t.Fatalf("retry_after no evento=%v, esperado 5s", recs[0].RetryAfter)
	}
}

func TestDefer_SinkErrorFailsClosed(t *testing.T) {
	ctx := context.Background()
	sink := &recordingDeferSink{err: errors.New("fila cheia")}
	d, _ := newDegrader(t, scheduler.WithDeferSink(sink))

	if _, err := d.Defer(ctx, baseItem(), baseTrigger()); err == nil {
		t.Fatal("Defer devia propagar o erro do sink (fail-closed: não afirma um defer que não preservou)")
	}
	// Não deve ter emitido work_deferred se não preservou.
	recs, _ := d.ReplayDegradation(ctx)
	if countByType(recs, scheduler.EventWorkDeferred) != 0 {
		t.Fatalf("não devia emitir work_deferred quando o sink falha (%+v)", recs)
	}
}

func TestDowngrade_RoutesToCheaperTierWithExplicitVariance(t *testing.T) {
	ctx := context.Background()
	d, _ := newDegrader(t)

	res, err := d.Downgrade(ctx, baseItem(), baseTrigger())
	if err != nil {
		t.Fatalf("Downgrade: %v", err)
	}
	if !res.Applied || res.FromTier != "premium" || res.ToTier != "standard" {
		t.Fatalf("downgrade inesperado: %+v (esperado premium→standard)", res)
	}
	if res.FromModel != "claude-opus" || res.ToModel != "claude-sonnet" {
		t.Fatalf("modelos do swap inesperados: %+v", res)
	}
	if !res.Reversible {
		t.Fatal("downgrade deve ser reversível")
	}
	// VARIÂNCIA EXPLÍCITA registada no log (nunca silenciosa).
	recs, _ := d.ReplayDegradation(ctx)
	if countByType(recs, scheduler.EventModelDowngraded) != 1 {
		t.Fatalf("esperado 1 evento model_downgraded (%+v)", recs)
	}
	v := recs[0]
	if v.FromTier != "premium" || v.ToTier != "standard" {
		t.Fatalf("variância no log incompleta: %+v", v)
	}
}

func TestDowngrade_AlreadyCheapestNoOp(t *testing.T) {
	ctx := context.Background()
	d, _ := newDegrader(t)
	item := baseItem()
	item.CurrentTier = "economy" // já no mais barato
	item.CurrentModel = "claude-haiku"

	res, err := d.Downgrade(ctx, item, baseTrigger())
	if err != nil {
		t.Fatalf("Downgrade: %v", err)
	}
	if res.Applied {
		t.Fatalf("não devia degradar (já no mais barato): %+v", res)
	}
	recs, _ := d.ReplayDegradation(ctx)
	if countByType(recs, scheduler.EventModelDowngraded) != 0 {
		t.Fatalf("sem tier mais barato ⇒ sem variância (%+v)", recs)
	}
}

func TestReject_ReturnsActionableErrorWithEvent(t *testing.T) {
	ctx := context.Background()
	d, _ := newDegrader(t)

	res, err := d.Reject(ctx, baseItem(), baseTrigger())
	if !errors.Is(err, scheduler.ErrWorkRejected) {
		t.Fatalf("Reject devia devolver ErrWorkRejected, obtido %v", err)
	}
	if !res.Applied || res.Action != scheduler.ActionReject {
		t.Fatalf("reject inesperado: %+v", res)
	}
	recs, _ := d.ReplayDegradation(ctx)
	if countByType(recs, scheduler.EventWorkRejected) != 1 {
		t.Fatalf("esperado 1 evento work_rejected (%+v)", recs)
	}
}

// ---------------------------------------------------------------------------
// Unit: shed NÃO descarta crítico silenciosamente (fail-closed).
// ---------------------------------------------------------------------------

func TestShed_CriticalNeverDiscardedSilently(t *testing.T) {
	ctx := context.Background()
	d, _ := newDegrader(t)
	item := baseItem()
	item.Critical = true
	item.Priority = "P0"

	res, err := d.Shed(ctx, item, baseTrigger())
	if !errors.Is(err, scheduler.ErrCannotShedCritical) {
		t.Fatalf("shed de crítico devia ser ErrCannotShedCritical, obtido %v", err)
	}
	if res.Applied {
		t.Fatal("shed de crítico NÃO devia ser aplicado")
	}
	// NENHUM evento work_shed — o crítico não foi descartado (nem silenciosa nem
	// audivelmente).
	recs, _ := d.ReplayDegradation(ctx)
	if countByType(recs, scheduler.EventWorkShed) != 0 {
		t.Fatalf("crítico não pode gerar work_shed (%+v)", recs)
	}
}

func TestExecute_UnknownActionFailsClosed(t *testing.T) {
	ctx := context.Background()
	d, _ := newDegrader(t)
	if _, err := d.Execute(ctx, scheduler.DegradationAction("bogus"), baseItem(), baseTrigger()); !errors.Is(err, scheduler.ErrUnknownDegradationAction) {
		t.Fatalf("acção desconhecida devia falhar fail-closed, obtido %v", err)
	}
}

// ---------------------------------------------------------------------------
// Integração: cadeia sob PRESSÃO CRESCENTE segue a ordem shed→defer→downgrade→reject,
// conduzida pela POLÍTICA de AOS-030 (o executor executa a selecção).
// ---------------------------------------------------------------------------

func TestChain_IncreasingPressureFollowsPreferenceOrder(t *testing.T) {
	ctx := context.Background()

	// Política de AOS-030: mapeia enchimento crescente → acção crescente na cadeia.
	// Primeira regra que casa vence (ordem de declaração).
	policy := scheduler.PolicyDoc{
		Version: "1.0.0",
		Rules: []scheduler.PolicyRule{
			{MinFillRatio: 0.95, Action: scheduler.ActionReject},
			{MinFillRatio: 0.85, Action: scheduler.ActionDowngrade},
			{MinFillRatio: 0.70, Action: scheduler.ActionDefer},
			{MinFillRatio: 0.50, Action: scheduler.ActionShed},
		},
		DefaultAction: scheduler.ActionShed,
	}
	eng, err := scheduler.NewPolicyEngine(policy)
	if err != nil {
		t.Fatalf("NewPolicyEngine: %v", err)
	}
	d, _ := newDegrader(t)

	// Pressão crescente: cada nível selecciona a próxima acção da ordem de preferência.
	levels := []struct {
		fill float64
		want scheduler.DegradationAction
	}{
		{0.55, scheduler.ActionShed},
		{0.75, scheduler.ActionDefer},
		{0.90, scheduler.ActionDowngrade},
		{0.98, scheduler.ActionReject},
	}
	var gotOrder []scheduler.DegradationAction
	for i, lv := range levels {
		cond := scheduler.SaturationCondition{Tenant: "acme", Priority: "P2", FillRatio: lv.fill, Depth: int(lv.fill * 100), Capacity: 100}
		action, ver, err := eng.Select(ctx, cond)
		if err != nil {
			t.Fatalf("Select[%d]: %v", i, err)
		}
		if action != lv.want {
			t.Fatalf("nível fill=%.2f: política escolheu %q, esperado %q", lv.fill, action, lv.want)
		}
		item := baseItem()
		item.ID = "chain-item" // mesmo item a atravessar a cadeia
		trigger := scheduler.TriggerFromCondition(cond, ver, "pressão crescente")
		_, execErr := d.Execute(ctx, action, item, trigger)
		// reject devolve erro sentinela; as restantes não.
		if action == scheduler.ActionReject {
			if !errors.Is(execErr, scheduler.ErrWorkRejected) {
				t.Fatalf("reject devia devolver ErrWorkRejected, obtido %v", execErr)
			}
		} else if execErr != nil {
			t.Fatalf("Execute[%d] %q: %v", i, action, execErr)
		}
		gotOrder = append(gotOrder, action)
	}

	// A ordem executada tem de ser exactamente shed→defer→downgrade→reject.
	want := []scheduler.DegradationAction{scheduler.ActionShed, scheduler.ActionDefer, scheduler.ActionDowngrade, scheduler.ActionReject}
	if len(gotOrder) != len(want) {
		t.Fatalf("ordem incompleta: %v", gotOrder)
	}
	for i := range want {
		if gotOrder[i] != want[i] {
			t.Fatalf("ordem de preferência violada: %v, esperado %v", gotOrder, want)
		}
	}

	// E os eventos no log seguem a mesma ordem canónica.
	recs, _ := d.ReplayDegradation(ctx)
	wantEv := []string{scheduler.EventWorkShed, scheduler.EventWorkDeferred, scheduler.EventModelDowngraded, scheduler.EventWorkRejected}
	if len(recs) != len(wantEv) {
		t.Fatalf("esperado %d eventos, obtido %d (%+v)", len(wantEv), len(recs), recs)
	}
	for i, want := range wantEv {
		if recs[i].Type != want {
			t.Fatalf("evento[%d]=%q, esperado %q (ordem no log)", i, recs[i].Type, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Integração: ExecuteChain respeita a ordem de preferência com FALLBACK — crítico
// salta o shed (nunca descartado), item não-diferível salta o defer, etc.
// ---------------------------------------------------------------------------

func TestExecuteChain_CriticalSkipsShedGoesToDefer(t *testing.T) {
	ctx := context.Background()
	d, _ := newDegrader(t)
	item := baseItem()
	item.Critical = true   // não pode ser descartado
	item.Deferrable = true // mas pode ser adiado

	res, err := d.ExecuteChain(ctx, item, baseTrigger(), nil) // ordem por omissão
	if err != nil {
		t.Fatalf("ExecuteChain: %v", err)
	}
	if res.Action != scheduler.ActionDefer {
		t.Fatalf("crítico devia SALTAR o shed e ser adiado, obtido %q", res.Action)
	}
	recs, _ := d.ReplayDegradation(ctx)
	if countByType(recs, scheduler.EventWorkShed) != 0 {
		t.Fatal("crítico nunca gera work_shed")
	}
	if countByType(recs, scheduler.EventWorkDeferred) != 1 {
		t.Fatalf("esperado work_deferred para o crítico (%+v)", recs)
	}
}

func TestExecuteChain_NonDeferrableNoTierEndsInReject(t *testing.T) {
	ctx := context.Background()
	d, _ := newDegrader(t)
	item := baseItem()
	item.Critical = true         // salta shed
	item.Deferrable = false      // salta defer
	item.CurrentTier = "economy" // já no mais barato ⇒ salta downgrade
	item.CurrentModel = "claude-haiku"
	item.Irreversible = true

	res, err := d.ExecuteChain(ctx, item, baseTrigger(), nil)
	if !errors.Is(err, scheduler.ErrWorkRejected) {
		t.Fatalf("cadeia sem degrau aplicável devia terminar em reject, obtido %v", err)
	}
	if res.Action != scheduler.ActionReject {
		t.Fatalf("degrau terminal devia ser reject, obtido %q", res.Action)
	}
	recs, _ := d.ReplayDegradation(ctx)
	if countByType(recs, scheduler.EventWorkRejected) != 1 {
		t.Fatalf("esperado work_rejected terminal (%+v)", recs)
	}
	// Fail-closed para irreversível: shed/defer/downgrade NÃO ocorreram.
	if countByType(recs, scheduler.EventWorkShed)+countByType(recs, scheduler.EventWorkDeferred)+countByType(recs, scheduler.EventModelDowngraded) != 0 {
		t.Fatalf("irreversível não pode sofrer shed/defer/downgrade antes do reject (%+v)", recs)
	}
}

// ---------------------------------------------------------------------------
// Integração: reject é FAIL-CLOSED para irreversíveis — o trabalho rejeitado não
// é ressuscitado ao normalizar (só downgrades revertem).
// ---------------------------------------------------------------------------

func TestReject_IrreversibleFailClosedNotResurrectedByNormalize(t *testing.T) {
	ctx := context.Background()
	d, _ := newDegrader(t)
	item := baseItem()
	item.Irreversible = true

	if _, err := d.Reject(ctx, item, baseTrigger()); !errors.Is(err, scheduler.ErrWorkRejected) {
		t.Fatalf("Reject: %v", err)
	}
	// Normalize NÃO ressuscita trabalho rejeitado (não há downgrade activo).
	restored, err := d.Normalize(ctx, "carga normalizada")
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(restored) != 0 {
		t.Fatalf("reject irreversível não pode ser revertido, mas Normalize restaurou %d", len(restored))
	}
	if len(d.ActiveDowngrades()) != 0 {
		t.Fatal("não deve haver degradações reversíveis activas")
	}
}

// ---------------------------------------------------------------------------
// Reversibilidade: ao NORMALIZAR, os downgrades revertem (tier restaurado); a
// variância PERMANECE no log (replay fiel).
// ---------------------------------------------------------------------------

func TestNormalize_ReversibleDowngradeRevertsTierVariancePersists(t *testing.T) {
	ctx := context.Background()
	d, _ := newDegrader(t)

	// Downgrade premium→standard: fica activo (reversível).
	if _, err := d.Downgrade(ctx, baseItem(), baseTrigger()); err != nil {
		t.Fatalf("Downgrade: %v", err)
	}
	if len(d.ActiveDowngrades()) != 1 {
		t.Fatalf("esperado 1 downgrade activo, obtido %d", len(d.ActiveDowngrades()))
	}

	// Normaliza a carga: reverte o downgrade (restaura o tier).
	restored, err := d.Normalize(ctx, "backpressure_cleared")
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("esperado 1 restauração, obtido %d", len(restored))
	}
	if restored[0].FromTier != "standard" || restored[0].ToTier != "premium" {
		t.Fatalf("restauração inesperada: %+v (esperado standard→premium)", restored[0])
	}
	if len(d.ActiveDowngrades()) != 0 {
		t.Fatal("após normalizar não deve haver downgrades activos")
	}

	// A VARIÂNCIA (model_downgraded) PERMANECE no log mesmo após reversão + o
	// evento de restauração está presente.
	recs, _ := d.ReplayDegradation(ctx)
	if countByType(recs, scheduler.EventModelDowngraded) != 1 {
		t.Fatalf("a variância do downgrade deve PERMANECER no log (%+v)", recs)
	}
	if countByType(recs, scheduler.EventTierRestored) != 1 {
		t.Fatalf("esperado 1 evento tier_restored (%+v)", recs)
	}

	// Normalize é idempotente: uma segunda passagem sem novos downgrades é no-op.
	restored2, err := d.Normalize(ctx, "again")
	if err != nil {
		t.Fatalf("Normalize (2ª): %v", err)
	}
	if len(restored2) != 0 {
		t.Fatalf("segunda Normalize devia ser no-op, restaurou %d", len(restored2))
	}
}

func TestNormalize_CascadingDowngradeRestoresOriginalTier(t *testing.T) {
	ctx := context.Background()
	d, _ := newDegrader(t)

	// premium→standard.
	item := baseItem()
	if _, err := d.Downgrade(ctx, item, baseTrigger()); err != nil {
		t.Fatalf("Downgrade 1: %v", err)
	}
	// Segundo downgrade do MESMO item, agora a partir de standard→economy.
	item.CurrentTier = "standard"
	item.CurrentModel = "claude-sonnet"
	if _, err := d.Downgrade(ctx, item, baseTrigger()); err != nil {
		t.Fatalf("Downgrade 2: %v", err)
	}

	// A restauração deve devolver ao tier ORIGINAL (premium), não ao intermédio.
	restored, err := d.Normalize(ctx, "clear")
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("esperado 1 restauração (cascata do mesmo item), obtido %d", len(restored))
	}
	if restored[0].ToTier != "premium" {
		t.Fatalf("cascata devia restaurar ao tier ORIGINAL premium, obtido %q", restored[0].ToTier)
	}
}

// ---------------------------------------------------------------------------
// Determinismo: OTel span por acção; replay reconstrói a sequência.
// ---------------------------------------------------------------------------

func TestDegradation_SpanPerActionAndReplayDeterministic(t *testing.T) {
	ctx := context.Background()
	tr := &agentruntime.RecordingTracer{}
	d, _ := newDegrader(t, scheduler.WithDegradationTracer(tr))

	_, _ = d.Shed(ctx, baseItem(), baseTrigger())
	_, _ = d.Downgrade(ctx, baseItem(), baseTrigger())

	spans := tr.SpansByOperation("degradation_execute")
	if len(spans) != 2 {
		t.Fatalf("esperado 1 span por acção (2), obtido %d", len(spans))
	}
	for _, s := range spans {
		if !s.Ended {
			t.Fatal("span não foi fechado (End)")
		}
		if _, ok := s.Attributes["aos.degradation.action"]; !ok {
			t.Fatal("span sem atributo de acção")
		}
	}

	// Replay reconstrói a sequência por ordem de seq (determinístico).
	recs, _ := d.ReplayDegradation(ctx)
	if len(recs) != 2 || recs[0].Type != scheduler.EventWorkShed || recs[1].Type != scheduler.EventModelDowngraded {
		t.Fatalf("replay não reconstrói a sequência: %+v", recs)
	}
}

// ---------------------------------------------------------------------------
// Impl de referência da porta: escada de tiers determinística.
// ---------------------------------------------------------------------------

func TestStaticModelTierRouter_LadderDeterministic(t *testing.T) {
	ctx := context.Background()
	r := tierRouter()

	cases := []struct {
		from       string
		wantDown   bool
		wantToTier string
	}{
		{"premium", true, "standard"},
		{"standard", true, "economy"},
		{"economy", false, ""}, // já no mais barato
		{"desconhecido", false, ""},
	}
	for _, c := range cases {
		dec, err := r.Cheaper(ctx, scheduler.TierRouteRequest{CurrentTier: c.from})
		if err != nil {
			t.Fatalf("Cheaper(%q): %v", c.from, err)
		}
		if dec.Downgraded != c.wantDown {
			t.Fatalf("Cheaper(%q).Downgraded=%v, esperado %v", c.from, dec.Downgraded, c.wantDown)
		}
		if dec.Downgraded && dec.ToTier != c.wantToTier {
			t.Fatalf("Cheaper(%q).ToTier=%q, esperado %q", c.from, dec.ToTier, c.wantToTier)
		}
	}
}

// ---------------------------------------------------------------------------
// Opções de instância: NHI emissora e nome de stream próprios.
// ---------------------------------------------------------------------------

func TestDegrader_ProducerAndNameOptions(t *testing.T) {
	ctx := context.Background()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	nhi := eventstore.Producer{NHIID: "nhi:test/degrader"}
	d, err := scheduler.NewDegrader(tierRouter(),
		scheduler.WithDegradationLog(es),
		scheduler.WithDegradationClock(baseFixedClock()),
		scheduler.WithDegradationProducer(nhi),
		scheduler.WithDegradationName("tenantX"),
	)
	if err != nil {
		t.Fatalf("NewDegrader: %v", err)
	}

	// Execute despacha a acção seleccionada (defer) para o handler.
	cond := scheduler.SaturationCondition{Tenant: "acme", Priority: "P2", FillRatio: 0.8, Depth: 8, Capacity: 10}
	trigger := scheduler.TriggerFromCondition(cond, "2.0.0", "saturação")
	if trigger.Partition != "acme:P2" || trigger.FillRatio != 0.8 {
		t.Fatalf("TriggerFromCondition inesperado: %+v", trigger)
	}
	item := baseItem()
	if _, err := d.Execute(ctx, scheduler.ActionDefer, item, trigger); err != nil {
		t.Fatalf("Execute(defer): %v", err)
	}

	// O evento foi projectado no stream nomeado, com a NHI dada.
	recs, err := d.ReplayDegradation(ctx)
	if err != nil {
		t.Fatalf("ReplayDegradation: %v", err)
	}
	if len(recs) != 1 || recs[0].Type != scheduler.EventWorkDeferred {
		t.Fatalf("esperado 1 work_deferred no stream nomeado (%+v)", recs)
	}
	// Confirma que o stream nomeado ("degradation/tenantX") recebeu o evento.
	evs, err := es.Read(ctx, "degradation/tenantX", 1)
	if err != nil || len(evs) != 1 {
		t.Fatalf("stream nomeado sem evento: len=%d err=%v", len(evs), err)
	}
	if evs[0].Producer.NHIID != "nhi:test/degrader" {
		t.Fatalf("NHI emissora=%q, esperado nhi:test/degrader", evs[0].Producer.NHIID)
	}
}

// ---------------------------------------------------------------------------
// Shed fail-closed: opcionalidade explícita, irreversível protegido, razão exigida.
// ---------------------------------------------------------------------------

func TestShed_NonOptionalFailsClosed(t *testing.T) {
	ctx := context.Background()
	d, _ := newDegrader(t)
	item := baseItem()
	item.Optional = false // não marcado opcional (nem crítico): default protege

	res, err := d.Shed(ctx, item, baseTrigger())
	if !errors.Is(err, scheduler.ErrCannotShedNonOptional) {
		t.Fatalf("shed de não-opcional devia ser ErrCannotShedNonOptional (fail-closed), obtido %v", err)
	}
	if res.Applied {
		t.Fatal("não-opcional NÃO devia ser descartado")
	}
	recs, _ := d.ReplayDegradation(ctx)
	if countByType(recs, scheduler.EventWorkShed) != 0 {
		t.Fatalf("não-opcional não pode gerar work_shed (%+v)", recs)
	}
}

func TestShed_IrreversibleNeverDiscarded(t *testing.T) {
	ctx := context.Background()
	d, _ := newDegrader(t)
	item := baseItem()
	item.Irreversible = true // opcional MAS irreversível: nunca perdido por shed

	res, err := d.Shed(ctx, item, baseTrigger())
	if !errors.Is(err, scheduler.ErrCannotShedIrreversible) {
		t.Fatalf("shed de irreversível devia ser ErrCannotShedIrreversible, obtido %v", err)
	}
	if res.Applied {
		t.Fatal("irreversível NÃO devia ser descartado por shed")
	}
	recs, _ := d.ReplayDegradation(ctx)
	if countByType(recs, scheduler.EventWorkShed) != 0 {
		t.Fatalf("irreversível não pode gerar work_shed (%+v)", recs)
	}
}

func TestDegradation_MissingReasonFailsClosed(t *testing.T) {
	ctx := context.Background()
	d, _ := newDegrader(t)
	noReason := scheduler.DegradationTrigger{Partition: "acme:P2"} // Reason vazia

	// Cada acção que emite recusa fail-closed sem razão auditável.
	if _, err := d.Shed(ctx, baseItem(), noReason); !errors.Is(err, scheduler.ErrMissingReason) {
		t.Fatalf("Shed sem razão devia ser ErrMissingReason, obtido %v", err)
	}
	if _, err := d.Defer(ctx, baseItem(), noReason); !errors.Is(err, scheduler.ErrMissingReason) {
		t.Fatalf("Defer sem razão devia ser ErrMissingReason, obtido %v", err)
	}
	if _, err := d.Downgrade(ctx, baseItem(), noReason); !errors.Is(err, scheduler.ErrMissingReason) {
		t.Fatalf("Downgrade sem razão devia ser ErrMissingReason, obtido %v", err)
	}
	if _, err := d.Reject(ctx, baseItem(), noReason); !errors.Is(err, scheduler.ErrMissingReason) {
		t.Fatalf("Reject sem razão devia ser ErrMissingReason, obtido %v", err)
	}
	// NENHUM evento entrou no log sem razão.
	recs, _ := d.ReplayDegradation(ctx)
	if len(recs) != 0 {
		t.Fatalf("nenhuma degradação sem razão pode entrar no log (%+v)", recs)
	}
}

// ---------------------------------------------------------------------------
// Irreversível é LOAD-BEARING: não é degradado por downgrade nem shed; o campo
// (não a mera ausência de active) decide o comportamento. Contraste explícito com
// o item reversível idêntico (critério 4 / DoD asseverado de facto).
// ---------------------------------------------------------------------------

func TestDowngrade_IrreversibleNotDegradedReversibleIs(t *testing.T) {
	ctx := context.Background()
	d, _ := newDegrader(t)

	// Reversível: degrada (premium→standard).
	rev := baseItem()
	rev.ID = "rev"
	resR, err := d.Downgrade(ctx, rev, baseTrigger())
	if err != nil || !resR.Applied {
		t.Fatalf("reversível devia degradar: res=%+v err=%v", resR, err)
	}

	// Irreversível idêntico: NÃO degrada (Applied=false, sem variância) — escala.
	irr := baseItem()
	irr.ID = "irr"
	irr.Irreversible = true
	resI, err := d.Downgrade(ctx, irr, baseTrigger())
	if err != nil {
		t.Fatalf("Downgrade irreversível não devia dar erro (escala via Applied=false): %v", err)
	}
	if resI.Applied {
		t.Fatal("irreversível NÃO devia ser degradado (silenciosamente)")
	}
	recs, _ := d.ReplayDegradation(ctx)
	if countByType(recs, scheduler.EventModelDowngraded) != 1 {
		t.Fatalf("só o reversível gera model_downgraded (1), obtido %+v", recs)
	}
	// Só o reversível ficou activo (o irreversível nunca entra em active).
	if len(d.ActiveDowngrades()) != 1 || d.ActiveDowngrades()[0].ItemID != "rev" {
		t.Fatalf("só o reversível fica activo: %+v", d.ActiveDowngrades())
	}
}

func TestExecuteChain_IrreversibleReachesRejectReversibleDowngrades(t *testing.T) {
	ctx := context.Background()
	d, _ := newDegrader(t)

	// Base: não-opcional (salta shed), não-diferível (salta defer), tier premium
	// (há tier mais barato). A ÚNICA diferença entre os dois itens é Irreversible.
	mk := func(id string, irr bool) scheduler.DegradationItem {
		it := baseItem()
		it.ID = id
		it.Optional = false
		it.Deferrable = false
		it.Irreversible = irr
		return it
	}

	// Irreversível: shed/defer/downgrade não se aplicam ⇒ REJECT fail-closed.
	resI, errI := d.ExecuteChain(ctx, mk("irr", true), baseTrigger(), nil)
	if !errors.Is(errI, scheduler.ErrWorkRejected) || resI.Action != scheduler.ActionReject {
		t.Fatalf("irreversível devia terminar em reject: res=%+v err=%v", resI, errI)
	}

	// Reversível idêntico: salta shed/defer mas DEGRADA (não rejeita).
	resR, errR := d.ExecuteChain(ctx, mk("rev", false), baseTrigger(), nil)
	if errR != nil || resR.Action != scheduler.ActionDowngrade {
		t.Fatalf("reversível devia degradar (não rejeitar): res=%+v err=%v", resR, errR)
	}

	recs, _ := d.ReplayDegradation(ctx)
	if countByType(recs, scheduler.EventWorkRejected) != 1 {
		t.Fatalf("esperado 1 work_rejected (o irreversível): %+v", recs)
	}
	if countByType(recs, scheduler.EventModelDowngraded) != 1 {
		t.Fatalf("esperado 1 model_downgraded (o reversível): %+v", recs)
	}
}

// ---------------------------------------------------------------------------
// Execute (acção única) não deixa a pressão sem alívio em silêncio: um no-op
// (downgrade já no tier mais barato) devolve ErrDegradationNotApplied para escalar.
// ---------------------------------------------------------------------------

func TestExecute_NoOpDowngradeSignalsEscalation(t *testing.T) {
	ctx := context.Background()
	d, _ := newDegrader(t)
	item := baseItem()
	item.CurrentTier = "economy" // já no mais barato ⇒ downgrade no-op
	item.CurrentModel = "claude-haiku"

	res, err := d.Execute(ctx, scheduler.ActionDowngrade, item, baseTrigger())
	if !errors.Is(err, scheduler.ErrDegradationNotApplied) {
		t.Fatalf("no-op via Execute devia sinalizar ErrDegradationNotApplied, obtido %v", err)
	}
	if res.Applied {
		t.Fatal("no-op não devia reportar Applied")
	}
	// Nada foi emitido (não houve variância).
	recs, _ := d.ReplayDegradation(ctx)
	if len(recs) != 0 {
		t.Fatalf("no-op não devia emitir evento (%+v)", recs)
	}
}

// ---------------------------------------------------------------------------
// ExecuteChain respeita a ORDEM configurável do executor (não só a DefaultPreferenceOrder).
// ---------------------------------------------------------------------------

func TestExecuteChain_CustomOrderRespected(t *testing.T) {
	ctx := context.Background()
	d, _ := newDegrader(t)
	item := baseItem() // opcional+diferível, tier premium (todos os degraus aplicáveis)

	// Ordem PRÓPRIA (invertida face à default): downgrade primeiro.
	order := []scheduler.DegradationAction{
		scheduler.ActionDowngrade, scheduler.ActionDefer, scheduler.ActionShed, scheduler.ActionReject,
	}
	res, err := d.ExecuteChain(ctx, item, baseTrigger(), order)
	if err != nil {
		t.Fatalf("ExecuteChain (ordem própria): %v", err)
	}
	// Com a default seria shed; com a ordem própria é downgrade (primeiro aplicável).
	if res.Action != scheduler.ActionDowngrade {
		t.Fatalf("ordem própria devia aplicar downgrade primeiro, obtido %q", res.Action)
	}
	recs, _ := d.ReplayDegradation(ctx)
	if countByType(recs, scheduler.EventModelDowngraded) != 1 || countByType(recs, scheduler.EventWorkShed) != 0 {
		t.Fatalf("ordem própria não respeitada no log: %+v", recs)
	}
}

// ---------------------------------------------------------------------------
// Normalize sob ERRO PARCIAL do Event Store: os itens ainda não restaurados
// PERMANECEM em active e são reprocessados na próxima Normalize (nunca perdidos).
// ---------------------------------------------------------------------------

func TestNormalize_PartialFailureKeepsUnrestored(t *testing.T) {
	ctx := context.Background()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	fl := &failAfterLog{inner: es, err: errors.New("event store indisponível")}
	d, err := scheduler.NewDegrader(tierRouter(),
		scheduler.WithDegradationLog(fl),
		scheduler.WithDegradationClock(baseFixedClock()),
	)
	if err != nil {
		t.Fatalf("NewDegrader: %v", err)
	}

	// Dois downgrades activos (a1, a2), ambos premium→standard.
	a1 := baseItem()
	a1.ID = "a1"
	a2 := baseItem()
	a2.ID = "a2"
	if _, err := d.Downgrade(ctx, a1, baseTrigger()); err != nil {
		t.Fatalf("Downgrade a1: %v", err)
	}
	if _, err := d.Downgrade(ctx, a2, baseTrigger()); err != nil {
		t.Fatalf("Downgrade a2: %v", err)
	}
	if len(d.ActiveDowngrades()) != 2 {
		t.Fatalf("esperado 2 downgrades activos, obtido %d", len(d.ActiveDowngrades()))
	}

	// Arma a falha para o SEGUNDO tier_restored (ordem por ID: a1 ok, a2 falha).
	fl.failFrom = fl.calls + 2

	restored, err := d.Normalize(ctx, "clear")
	if err == nil {
		t.Fatal("Normalize devia propagar o erro do store (fail-closed)")
	}
	if len(restored) != 1 || restored[0].ItemID != "a1" {
		t.Fatalf("só a1 devia ter sido restaurado antes da falha: %+v", restored)
	}
	// a2 NÃO se perdeu: continua activo para a próxima Normalize.
	active := d.ActiveDowngrades()
	if len(active) != 1 || active[0].ItemID != "a2" {
		t.Fatalf("a2 devia permanecer activo após erro parcial: %+v", active)
	}

	// Store recupera: a próxima Normalize restaura a2 (reversão não perdida).
	fl.failFrom = 0
	restored2, err := d.Normalize(ctx, "clear-again")
	if err != nil {
		t.Fatalf("Normalize (recuperação): %v", err)
	}
	if len(restored2) != 1 || restored2[0].ItemID != "a2" {
		t.Fatalf("a2 devia ser restaurado na recuperação: %+v", restored2)
	}
	if len(d.ActiveDowngrades()) != 0 {
		t.Fatal("nada deve ficar activo após a recuperação")
	}
}

// ---------------------------------------------------------------------------
// RehydrateActive: reconstrói os downgrades activos do log após um restart, para
// que Normalize restaure fielmente (o estado reversível é durável, não só memória).
// ---------------------------------------------------------------------------

func TestRehydrateActive_RebuildsFromLogAfterRestart(t *testing.T) {
	ctx := context.Background()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	mk := func() *scheduler.Degrader {
		d, err := scheduler.NewDegrader(tierRouter(),
			scheduler.WithDegradationLog(es),
			scheduler.WithDegradationClock(baseFixedClock()),
			scheduler.WithDegradationName("shared"),
		)
		if err != nil {
			t.Fatalf("NewDegrader: %v", err)
		}
		return d
	}

	// d1 degrada e "morre" (deixa model_downgraded no stream partilhado).
	d1 := mk()
	if _, err := d1.Downgrade(ctx, baseItem(), baseTrigger()); err != nil {
		t.Fatalf("Downgrade: %v", err)
	}

	// Restart: d2 arranca com o mapa vazio até rehydratar.
	d2 := mk()
	if len(d2.ActiveDowngrades()) != 0 {
		t.Fatal("antes de RehydrateActive o mapa devia estar vazio (só memória)")
	}
	n, err := d2.RehydrateActive(ctx)
	if err != nil {
		t.Fatalf("RehydrateActive: %v", err)
	}
	if n != 1 || len(d2.ActiveDowngrades()) != 1 {
		t.Fatalf("RehydrateActive devia reconstruir 1 downgrade activo, obtido n=%d active=%d", n, len(d2.ActiveDowngrades()))
	}

	// E agora Normalize (pós-restart) restaura fielmente o tier original.
	restored, err := d2.Normalize(ctx, "cleared-after-restart")
	if err != nil {
		t.Fatalf("Normalize pós-restart: %v", err)
	}
	if len(restored) != 1 || restored[0].FromTier != "standard" || restored[0].ToTier != "premium" {
		t.Fatalf("restauração pós-restart infiel: %+v (esperado standard→premium)", restored)
	}

	// Um terceiro arranque, já com o tier_restored no log, rehydrata para ZERO
	// activos (model_downgraded menos tier_restored = ∅).
	d3 := mk()
	n3, err := d3.RehydrateActive(ctx)
	if err != nil {
		t.Fatalf("RehydrateActive (3º): %v", err)
	}
	if n3 != 0 || len(d3.ActiveDowngrades()) != 0 {
		t.Fatalf("após tier_restored no log, rehydrate devia dar 0 activos, obtido %d", n3)
	}
}

// ---------------------------------------------------------------------------
// Helper: EventLog que falha o Append a partir da N-ésima chamada (erro parcial).
// ---------------------------------------------------------------------------

type failAfterLog struct {
	inner    scheduler.EventLog
	calls    int
	failFrom int // 0 = nunca falha; senão falha quando calls >= failFrom (após ++)
	err      error
}

func (l *failAfterLog) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	l.calls++
	if l.failFrom > 0 && l.calls >= l.failFrom {
		return eventstore.AppendResult{}, l.err
	}
	return l.inner.Append(ctx, streamID, in, opts...)
}

func (l *failAfterLog) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	return l.inner.Read(ctx, streamID, fromSeq)
}

// ---------------------------------------------------------------------------
// Helper: DeferSink de teste.
// ---------------------------------------------------------------------------

type recordingDeferSink struct {
	count     int
	lastRetry time.Duration
	err       error
}

func (s *recordingDeferSink) Defer(_ context.Context, _ scheduler.DegradationItem, retry time.Duration) error {
	if s.err != nil {
		return s.err
	}
	s.count++
	s.lastRetry = retry
	return nil
}
