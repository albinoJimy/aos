package scheduler_test

// Testes de integração do coordenador de spawn (AOS-028): composição do admission
// control global (AOS-027) com a delegação hierárquica (AOS-026). Todos
// deterministas (relógio/IDs injectáveis) e -race limpos. Cobrem os Testes
// Requeridos do ticket:
//   - reserva de headroom no admit ANTES do spawn;
//   - libertação idempotente ao terminar (sucesso/falha/timeout) SEM fuga;
//   - headroom nulo ⇒ spawn ADIADO, nunca oversubscription;
//   - AMBOS os limites (árvore + global) respeitados;
//   - two-phase: headroom concede mas sub-orçamento nega ⇒ recusa E headroom
//     libertado (sem fuga);
//   - derivação dinâmica de max_spawn a partir do headroom real;
//   - replay reconstrói a sequência de decisões.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	budget "github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/orchestrator"
	"github.com/aos-ref/control-plane/scheduler"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// SubtreeSpawner de teste, apoiado num budget hierárquico REAL (AOS-008): o
// sub-orçamento da árvore é imposto pela mesma primitiva CAS que o Delegator
// (AOS-026) usa, sem arrastar RM/identidade. Prova a composição de AMBOS os
// limites contra um sub-orçamento real, de forma determinística.
// ---------------------------------------------------------------------------

type budgetSpawner struct {
	b *budget.Budget
	// spawnCount conta chamadas EFECTIVAS (bem-sucedidas) a Spawn — prova DIRECTA de
	// que o caminho de defer NÃO toca no Delegator (ao contrário de spawnDenied, que
	// só reflecte recusas do sub-orçamento e não distingue um Spawn indevido
	// bem-sucedido de nenhuma chamada).
	spawnCount atomic.Int64
	// spawnDenied conta spawns recusados pelo sub-orçamento (fail-closed).
	spawnDenied atomic.Int64
	// finishCount conta chamadas EFECTIVAS a Finish (para provar idempotência).
	finishCount atomic.Int64
	// commitCount/releaseCount discriminam a consolidação real do sub-orçamento.
	commitCount  atomic.Int64
	releaseCount atomic.Int64
}

func (s *budgetSpawner) Spawn(ctx context.Context, req orchestrator.SpawnRequest) (*orchestrator.SpawnHandle, error) {
	slice := req.SpawnReserve
	if slice.IsZero() {
		slice = req.InheritedBudget
	}
	if err := s.b.AddNode(req.ChildBudgetNode, req.ParentBudgetNode, req.InheritedBudget); err != nil && !errors.Is(err, budget.ErrNodeExists) {
		return nil, err
	}
	res, err := s.b.Reserve(ctx, req.ChildBudgetNode, slice)
	if err != nil {
		// Sub-orçamento esgotado: recusa fail-closed (como ErrNoDelegationBudget).
		s.spawnDenied.Add(1)
		return nil, orchestrator.ErrNoDelegationBudget
	}
	s.spawnCount.Add(1)
	return &orchestrator.SpawnHandle{
		RunID: req.RunID, ChildTaskID: req.ChildTaskID, Reservation: res, Slice: slice,
	}, nil
}

func (s *budgetSpawner) Finish(ctx context.Context, h *orchestrator.SpawnHandle, success bool) error {
	s.finishCount.Add(1)
	if success {
		s.commitCount.Add(1)
		return s.b.Commit(ctx, h.Reservation)
	}
	s.releaseCount.Add(1)
	return s.b.Release(ctx, h.Reservation)
}

// ---------------------------------------------------------------------------
// Helpers.
// ---------------------------------------------------------------------------

var spawnKey = scheduler.ProviderKey{Provider: "anthropic", Model: "claude", Region: "eu"}

func fixed(t time.Time) func() time.Time { return func() time.Time { return t } }

func seqGen(prefix string) func() string {
	var n atomic.Int64
	return func() string {
		v := n.Add(1)
		return prefix + string(rune('0'+v%10)) + "-" + itoa(v)
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// newAdmForSpawn constrói uma Admission real sobre um Event Store real, com custo
// por token fixo e relógio congelado (o refill não expira durante o teste).
func newAdmForSpawn(t *testing.T, tpm, rpm int64, base time.Time) (*scheduler.Admission, *eventstore.Store) {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	qp := scheduler.NewStaticQuotaProvider(scheduler.ProviderLimits{TPM: tpm, RPM: rpm, Window: time.Minute})
	adm, err := scheduler.NewAdmission(es, qp,
		scheduler.WithClock(fixed(base)),
		scheduler.WithIDGen(seqGen("res")),
	)
	if err != nil {
		t.Fatalf("NewAdmission: %v", err)
	}
	return adm, es
}

// spawnReq compõe um SpawnAdmitRequest de teste sob um run, com um custo por
// sub-agente e uma fatia de sub-orçamento.
func spawnReq(runID, child string, costTokens int64, slice budget.Amount) scheduler.SpawnAdmitRequest {
	return scheduler.SpawnAdmitRequest{
		Key:             spawnKey,
		EstimatedTokens: costTokens,
		Spawn: orchestrator.SpawnRequest{
			RunID:            runID,
			ParentBudgetNode: runID, // raiz da árvore de budget
			ChildBudgetNode:  child,
			ChildTaskID:      child,
			InheritedBudget:  slice,
			SpawnReserve:     slice,
			Child:            identity.ChildRequest{AgentID: child},
		},
	}
}

// ---------------------------------------------------------------------------
// Reserva no admit ANTES do spawn + libertação idempotente ao terminar, sem fuga.
// ---------------------------------------------------------------------------

func TestRequestSpawn_ReserveThenReleaseNoLeak(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	adm, _ := newAdmForSpawn(t, 10_000, 1_000_000, base)
	b, _ := budget.New("run-A", budget.Amount{Tokens: 1_000_000, CostMicroUSD: 1_000_000})
	sp := &budgetSpawner{b: b}
	coord, err := scheduler.NewSpawnCoordinator(adm, sp, scheduler.WithSpawnClock(fixed(base)))
	if err != nil {
		t.Fatalf("NewSpawnCoordinator: %v", err)
	}
	ctx := context.Background()
	const cost = int64(1000)
	slice := budget.Amount{Tokens: 500, CostMicroUSD: 500}

	// Headroom inicial = TPM cheio.
	before, err := adm.Headroom(ctx, spawnKey, "")
	if err != nil {
		t.Fatalf("Headroom: %v", err)
	}
	if before.Tokens != 10_000 {
		t.Fatalf("headroom inicial = %d, quero 10000", before.Tokens)
	}

	for outcomeIdx, success := range []bool{true, false} {
		child := "child-" + itoa(int64(outcomeIdx))
		out, err := coord.RequestSpawn(ctx, spawnReq("run-A", child, cost, slice))
		if err != nil {
			t.Fatalf("RequestSpawn: %v", err)
		}
		if !out.Admitted || out.Ticket == nil {
			t.Fatalf("esperava admitido; got %+v", out)
		}
		// Enquanto a reserva está activa, o headroom baixou exactamente o custo.
		mid, _ := adm.Headroom(ctx, spawnKey, "")
		if mid.Tokens != 10_000-cost {
			t.Fatalf("headroom durante o spawn = %d, quero %d", mid.Tokens, 10_000-cost)
		}
		// Termina (sucesso ou falha/timeout): liberta o headroom.
		if err := coord.Finish(ctx, out.Ticket, success); err != nil {
			t.Fatalf("Finish: %v", err)
		}
		// Finish idempotente: repetir NÃO liberta duas vezes nem re-consolida.
		if err := coord.Finish(ctx, out.Ticket, success); err != nil {
			t.Fatalf("Finish (repeat): %v", err)
		}
		// Headroom recuperado por inteiro (sem fuga de reservas).
		after, _ := adm.Headroom(ctx, spawnKey, "")
		if after.Tokens != 10_000 {
			t.Fatalf("headroom após finish = %d, quero 10000 (fuga de reserva!)", after.Tokens)
		}
	}

	// Delegator.Finish foi chamado exactamente uma vez por spawn (guard idempotente).
	if got := sp.finishCount.Load(); got != 2 {
		t.Fatalf("Delegator.Finish chamado %d vezes, quero 2 (idempotência falhou)", got)
	}
	if sp.commitCount.Load() != 1 || sp.releaseCount.Load() != 1 {
		t.Fatalf("commit=%d release=%d, quero 1/1", sp.commitCount.Load(), sp.releaseCount.Load())
	}
}

// ---------------------------------------------------------------------------
// Headroom nulo ⇒ spawn ADIADO, nunca oversubscription; ambos os limites.
// ---------------------------------------------------------------------------

func TestRequestSpawn_DeferUnderNoHeadroom(t *testing.T) {
	t.Parallel()
	base := time.Unix(2_000_000, 0)
	// TPM = cost: cabe exactamente 1 reserva de headroom em voo.
	const cost = int64(1000)
	adm, _ := newAdmForSpawn(t, cost, 1_000_000, base)
	b, _ := budget.New("run-B", budget.Amount{Tokens: 1_000_000, CostMicroUSD: 1_000_000})
	sp := &budgetSpawner{b: b}
	coord, _ := scheduler.NewSpawnCoordinator(adm, sp, scheduler.WithSpawnClock(fixed(base)))
	ctx := context.Background()
	slice := budget.Amount{Tokens: 10, CostMicroUSD: 10}

	// 1º spawn: concede (headroom = cost).
	out1, err := coord.RequestSpawn(ctx, spawnReq("run-B", "c1", cost, slice))
	if err != nil {
		t.Fatalf("RequestSpawn#1: %v", err)
	}
	if !out1.Admitted {
		t.Fatalf("1º spawn devia ser admitido; got %+v", out1)
	}

	// 2º spawn: headroom nulo ⇒ ADIADO (defer), NÃO cria o sub-agente.
	out2, err := coord.RequestSpawn(ctx, spawnReq("run-B", "c2", cost, slice))
	if !errors.Is(err, scheduler.ErrSpawnDeferredNoHeadroom) {
		t.Fatalf("2º spawn: erro = %v, quero ErrSpawnDeferredNoHeadroom", err)
	}
	if out2.Admitted || out2.Ticket != nil {
		t.Fatalf("2º spawn NÃO devia ser admitido (oversubscription!); got %+v", out2)
	}
	if !out2.Deferred {
		t.Fatalf("2º spawn devia estar Deferred; got %+v", out2)
	}
	if out2.RetryAfter <= 0 {
		t.Fatalf("defer sem RetryAfter; got %v", out2.RetryAfter)
	}
	if out2.MaxSpawn != 0 {
		t.Fatalf("max_spawn sob headroom nulo = %d, quero 0", out2.MaxSpawn)
	}
	// O sub-orçamento da árvore NÃO foi tocado pelo spawn adiado: só 1 reserva de
	// budget existe (a do 1º), e nenhuma foi negada.
	if sp.spawnDenied.Load() != 0 {
		t.Fatalf("spawn adiado NÃO devia chegar ao Delegator; spawnDenied=%d", sp.spawnDenied.Load())
	}
	// Prova DIRECTA (não só via spawnDenied): Delegator.Spawn foi invocado
	// exactamente uma vez (o 1º admitido). Um Spawn indevido do 2º (adiado) teria
	// SUCESSO — budget folgado — sem incrementar spawnDenied; spawnCount==1 exclui-o.
	if got := sp.spawnCount.Load(); got != 1 {
		t.Fatalf("Delegator.Spawn chamado %d vezes, quero 1 (o defer não deve tocar no Delegator)", got)
	}
	// A soma das reservas activas de headroom nunca excede o TPM (1 reserva de cost).
	hr, _ := adm.Headroom(ctx, spawnKey, "")
	if hr.Tokens != 0 {
		t.Fatalf("headroom = %d, quero 0 (nunca oversubscription)", hr.Tokens)
	}

	// Liberta o 1º: o 2º passa a caber (defer é transitório, não rejeição).
	if err := coord.Finish(ctx, out1.Ticket, true); err != nil {
		t.Fatalf("Finish#1: %v", err)
	}
	out3, err := coord.RequestSpawn(ctx, spawnReq("run-B", "c2", cost, slice))
	if err != nil {
		t.Fatalf("RequestSpawn#3 após alívio: %v", err)
	}
	if !out3.Admitted {
		t.Fatalf("após libertar, o spawn devia ser admitido; got %+v", out3)
	}
}

// ---------------------------------------------------------------------------
// Two-phase: headroom concede mas o sub-orçamento nega ⇒ recusa E headroom
// libertado (sem fuga de duas-fases).
// ---------------------------------------------------------------------------

func TestRequestSpawn_TwoPhaseSubtreeDeniesReleasesHeadroom(t *testing.T) {
	t.Parallel()
	base := time.Unix(3_000_000, 0)
	// Headroom global ABUNDANTE (o admit concede sempre).
	adm, _ := newAdmForSpawn(t, 1_000_000, 1_000_000, base)
	// Sub-orçamento da árvore APERTADO: só cabe 1 fatia; a 2ª é negada.
	b, _ := budget.New("run-C", budget.Amount{Tokens: 100, CostMicroUSD: 100})
	sp := &budgetSpawner{b: b}
	coord, _ := scheduler.NewSpawnCoordinator(adm, sp, scheduler.WithSpawnClock(fixed(base)))
	ctx := context.Background()
	const cost = int64(1000)
	slice := budget.Amount{Tokens: 100, CostMicroUSD: 100} // consome toda a raiz

	// 1º spawn: ambos concedem.
	out1, err := coord.RequestSpawn(ctx, spawnReq("run-C", "c1", cost, slice))
	if err != nil || !out1.Admitted {
		t.Fatalf("1º spawn devia ser admitido; err=%v out=%+v", err, out1)
	}
	hrAfter1, _ := adm.Headroom(ctx, spawnKey, "")
	if hrAfter1.Tokens != 1_000_000-cost {
		t.Fatalf("headroom após 1º = %d, quero %d", hrAfter1.Tokens, 1_000_000-cost)
	}

	// 2º spawn: o headroom global concede, mas o sub-orçamento NEGA (raiz esgotada).
	out2, err := coord.RequestSpawn(ctx, spawnReq("run-C", "c2", cost, slice))
	if !errors.Is(err, scheduler.ErrSubtreeBudgetDenied) {
		t.Fatalf("2º spawn: erro = %v, quero ErrSubtreeBudgetDenied", err)
	}
	if out2.Admitted || out2.Ticket != nil {
		t.Fatalf("2º spawn NÃO devia ser admitido; got %+v", out2)
	}
	if sp.spawnDenied.Load() != 1 {
		t.Fatalf("o Delegator devia ter negado 1 vez; spawnDenied=%d", sp.spawnDenied.Load())
	}
	// CRÍTICO: o headroom reservado no admit do 2º spawn foi LIBERTADO — sem fuga de
	// duas-fases. Só resta a reserva do 1º (ainda activo).
	hrAfter2, _ := adm.Headroom(ctx, spawnKey, "")
	if hrAfter2.Tokens != 1_000_000-cost {
		t.Fatalf("headroom após 2º (negado) = %d, quero %d (FUGA de duas-fases!)", hrAfter2.Tokens, 1_000_000-cost)
	}

	// Termina o 1º: headroom recupera por inteiro.
	if err := coord.Finish(ctx, out1.Ticket, true); err != nil {
		t.Fatalf("Finish#1: %v", err)
	}
	hrFinal, _ := adm.Headroom(ctx, spawnKey, "")
	if hrFinal.Tokens != 1_000_000 {
		t.Fatalf("headroom final = %d, quero 1000000 (sem fuga)", hrFinal.Tokens)
	}
}

// ---------------------------------------------------------------------------
// Derivação dinâmica de max_spawn a partir do headroom REAL (não constante):
// à medida que o headroom baixa com reservas activas, o max_spawn observado desce.
// ---------------------------------------------------------------------------

func TestMaxSpawn_TracksRealHeadroom(t *testing.T) {
	t.Parallel()
	base := time.Unix(4_000_000, 0)
	const cost = int64(1000)
	// TPM = 5*cost ⇒ max_spawn inicial = 5.
	adm, _ := newAdmForSpawn(t, 5*cost, 1_000_000, base)
	b, _ := budget.New("run-D", budget.Amount{Tokens: 1_000_000, CostMicroUSD: 1_000_000})
	sp := &budgetSpawner{b: b}
	coord, _ := scheduler.NewSpawnCoordinator(adm, sp, scheduler.WithSpawnClock(fixed(base)))
	ctx := context.Background()
	slice := budget.Amount{Tokens: 10, CostMicroUSD: 10}

	// Antes de qualquer reserva: max_spawn = 5.
	m0, err := coord.MaxSpawn(ctx, spawnKey, "", cost)
	if err != nil {
		t.Fatalf("MaxSpawn: %v", err)
	}
	if m0 != 5 {
		t.Fatalf("max_spawn inicial = %d, quero 5", m0)
	}

	// Cada spawn admitido consome cost de headroom ⇒ max_spawn desce 5,4,3,2,1,0.
	prev := m0
	var tickets []*scheduler.SpawnTicket
	for i := 0; i < 5; i++ {
		out, err := coord.RequestSpawn(ctx, spawnReq("run-D", "d"+itoa(int64(i)), cost, slice))
		if err != nil || !out.Admitted {
			t.Fatalf("spawn#%d: err=%v out=%+v", i, err, out)
		}
		tickets = append(tickets, out.Ticket)
		m, _ := coord.MaxSpawn(ctx, spawnKey, "", cost)
		if m != prev-1 {
			t.Fatalf("após spawn#%d max_spawn = %d, quero %d (deve descer com o headroom)", i, m, prev-1)
		}
		prev = m
	}
	if prev != 0 {
		t.Fatalf("max_spawn após esgotar = %d, quero 0", prev)
	}
	// Prova de que NÃO é constante: variou de 5 até 0 conforme o headroom real.

	// Libertar uma reserva devolve headroom ⇒ max_spawn volta a subir (monotonia).
	if err := coord.Finish(ctx, tickets[0], false); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	mUp, _ := coord.MaxSpawn(ctx, spawnKey, "", cost)
	if mUp != 1 {
		t.Fatalf("max_spawn após libertar 1 = %d, quero 1 (mais headroom ⇒ ≥ spawns)", mUp)
	}
}

// ---------------------------------------------------------------------------
// Custo por sub-agente > tecto TPM ⇒ rejeição PERMANENTE (não defer eterno).
// ---------------------------------------------------------------------------

func TestRequestSpawn_UnsatisfiableCost(t *testing.T) {
	t.Parallel()
	base := time.Unix(5_000_000, 0)
	adm, _ := newAdmForSpawn(t, 500, 1_000_000, base) // TPM=500
	b, _ := budget.New("run-E", budget.Amount{Tokens: 1_000_000, CostMicroUSD: 1_000_000})
	sp := &budgetSpawner{b: b}
	coord, _ := scheduler.NewSpawnCoordinator(adm, sp, scheduler.WithSpawnClock(fixed(base)))
	ctx := context.Background()
	slice := budget.Amount{Tokens: 10, CostMicroUSD: 10}

	out, err := coord.RequestSpawn(ctx, spawnReq("run-E", "big", 1000, slice)) // custo 1000 > TPM 500
	if !errors.Is(err, scheduler.ErrSpawnUnsatisfiable) {
		t.Fatalf("erro = %v, quero ErrSpawnUnsatisfiable", err)
	}
	if out.Admitted || out.RetryAfter != 0 {
		t.Fatalf("rejeição permanente não devia admitir nem aconselhar retry; got %+v", out)
	}
	if sp.spawnDenied.Load() != 0 {
		t.Fatalf("custo insatisfável não devia chegar ao Delegator; spawnDenied=%d", sp.spawnDenied.Load())
	}
}

// ---------------------------------------------------------------------------
// Eventos append-only + replay reconstrói a sequência de decisões + span OTel.
// ---------------------------------------------------------------------------

func TestRequestSpawn_EventsReplayAndSpan(t *testing.T) {
	t.Parallel()
	base := time.Unix(6_000_000, 0)
	const cost = int64(1000)
	adm, es := newAdmForSpawn(t, cost, 1_000_000, base) // cabe 1 spawn de cada vez
	b, _ := budget.New("run-F", budget.Amount{Tokens: 1_000_000, CostMicroUSD: 1_000_000})
	sp := &budgetSpawner{b: b}
	tr := &agentruntime.RecordingTracer{}
	coord, _ := scheduler.NewSpawnCoordinator(adm, sp,
		scheduler.WithSpawnClock(fixed(base)),
		scheduler.WithSpawnEventLog(es, eventstore.Producer{NHIID: "nhi:test"}),
		scheduler.WithSpawnTracer(tr),
	)
	ctx := context.Background()
	slice := budget.Amount{Tokens: 10, CostMicroUSD: 10}

	// spawn admitido ⇒ headroom_reserved.
	out, err := coord.RequestSpawn(ctx, spawnReq("run-F", "c1", cost, slice))
	if err != nil || !out.Admitted {
		t.Fatalf("spawn: err=%v out=%+v", err, out)
	}
	// 2º spawn ⇒ deferido (headroom nulo) ⇒ spawn_deferred_no_headroom.
	if _, err := coord.RequestSpawn(ctx, spawnReq("run-F", "c2", cost, slice)); !errors.Is(err, scheduler.ErrSpawnDeferredNoHeadroom) {
		t.Fatalf("2º spawn: quero defer, got %v", err)
	}
	// termina o 1º ⇒ headroom_released.
	if err := coord.Finish(ctx, out.Ticket, true); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	recs, err := coord.ReplaySpawnAdmission(ctx, "run-F")
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	types := map[string]int{}
	for _, r := range recs {
		types[r.Type]++
	}
	if types[scheduler.EventHeadroomReserved] != 1 {
		t.Fatalf("headroom_reserved = %d, quero 1", types[scheduler.EventHeadroomReserved])
	}
	if types[scheduler.EventSpawnDeferredNoHeadroom] != 1 {
		t.Fatalf("spawn_deferred_no_headroom = %d, quero 1", types[scheduler.EventSpawnDeferredNoHeadroom])
	}
	if types[scheduler.EventHeadroomReleased] != 1 {
		t.Fatalf("headroom_released = %d, quero 1", types[scheduler.EventHeadroomReleased])
	}

	// Replay DETERMINÍSTICO (DoD): não basta as contagens por tipo — a sequência
	// reconstrói-se por ORDEM de Seq (estritamente crescente) e uma segunda
	// reconstrução do MESMO run é IDÊNTICA (ordem + conteúdo).
	wantOrder := []string{
		scheduler.EventHeadroomReserved,
		scheduler.EventSpawnDeferredNoHeadroom,
		scheduler.EventHeadroomReleased,
	}
	if len(recs) != len(wantOrder) {
		t.Fatalf("replay = %d registos, quero %d", len(recs), len(wantOrder))
	}
	for i, want := range wantOrder {
		if recs[i].Type != want {
			t.Fatalf("replay[%d].Type = %q, quero %q (ordem determinística por Seq)", i, recs[i].Type, want)
		}
		if i > 0 && recs[i].Seq <= recs[i-1].Seq {
			t.Fatalf("replay não é monotónico por Seq em %d: %d <= %d", i, recs[i].Seq, recs[i-1].Seq)
		}
	}
	// Segunda reconstrução do mesmo run: byte-a-byte idêntica (estabilidade do replay).
	recs2, err := coord.ReplaySpawnAdmission(ctx, "run-F")
	if err != nil {
		t.Fatalf("Replay#2: %v", err)
	}
	if len(recs2) != len(recs) {
		t.Fatalf("2ª reconstrução = %d registos, quero %d (replay não-determinístico)", len(recs2), len(recs))
	}
	for i := range recs {
		if recs2[i] != recs[i] {
			t.Fatalf("2ª reconstrução difere em %d: %+v != %+v (replay não-determinístico)", i, recs2[i], recs[i])
		}
	}

	// Span OTel com o headroom reservado por spawn.
	spans := tr.SpansByOperation("spawn_admission")
	if len(spans) != 2 { // 2 RequestSpawn (admit + defer); Finish não abre span
		t.Fatalf("spans spawn_admission = %d, quero 2", len(spans))
	}
	foundHeadroomAttr := false
	for _, s := range spans {
		if _, ok := s.Attributes["aos.spawn.headroom_reserved_tokens"]; ok {
			foundHeadroomAttr = true
		}
	}
	if !foundHeadroomAttr {
		t.Fatalf("nenhum span com o atributo de headroom reservado por spawn")
	}
}

// ---------------------------------------------------------------------------
// Concorrência (-race): N spawns concorrentes sobre headroom limitado — os
// admitidos nunca excedem o TPM (sem oversubscription); finish concorrente do
// mesmo ticket é idempotente (sem fuga).
// ---------------------------------------------------------------------------

func TestRequestSpawn_ConcurrentNoOversubscriptionAndIdempotentFinish(t *testing.T) {
	t.Parallel()
	base := time.Unix(7_000_000, 0)
	const cost = int64(1000)
	const capSpawns = 8
	adm, _ := newAdmForSpawn(t, capSpawns*cost, 1_000_000, base)
	b, _ := budget.New("run-G", budget.Amount{Tokens: 100_000_000, CostMicroUSD: 100_000_000})
	sp := &budgetSpawner{b: b}
	coord, _ := scheduler.NewSpawnCoordinator(adm, sp, scheduler.WithSpawnClock(fixed(base)))
	ctx := context.Background()
	slice := budget.Amount{Tokens: 10, CostMicroUSD: 10}

	const workers = 32
	var admitted atomic.Int64
	var tickets sync.Map // idx -> *SpawnTicket
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, err := coord.RequestSpawn(ctx, spawnReq("run-G", "w"+itoa(int64(i)), cost, slice))
			if err != nil && !errors.Is(err, scheduler.ErrSpawnDeferredNoHeadroom) {
				t.Errorf("worker %d: erro inesperado %v", i, err)
				return
			}
			if out.Admitted {
				admitted.Add(1)
				tickets.Store(i, out.Ticket)
			}
		}(i)
	}
	wg.Wait()

	// Nunca mais do que capSpawns admitidos concorrentemente (sem oversubscription).
	if got := admitted.Load(); got > capSpawns {
		t.Fatalf("admitidos = %d > cap %d (OVERSUBSCRIPTION)", got, capSpawns)
	}
	hr, _ := adm.Headroom(ctx, spawnKey, "")
	if hr.Tokens < 0 {
		t.Fatalf("headroom negativo (%d): oversubscription", hr.Tokens)
	}
	if hr.Tokens != capSpawns*cost-admitted.Load()*cost {
		t.Fatalf("headroom = %d incoerente com %d admitidos", hr.Tokens, admitted.Load())
	}

	// Finish concorrente do MESMO ticket (idempotência sob corrida): headroom
	// recupera por inteiro, sem fuga nem dupla libertação.
	tickets.Range(func(_, v any) bool {
		tk := v.(*scheduler.SpawnTicket)
		var fw sync.WaitGroup
		for k := 0; k < 4; k++ {
			fw.Add(1)
			go func() { defer fw.Done(); _ = coord.Finish(ctx, tk, true) }()
		}
		fw.Wait()
		return true
	})
	final, _ := adm.Headroom(ctx, spawnKey, "")
	if final.Tokens != capSpawns*cost {
		t.Fatalf("headroom final = %d, quero %d (fuga de reserva sob finish concorrente)", final.Tokens, capSpawns*cost)
	}
	// Cada ticket consolidou exactamente uma vez.
	if sp.commitCount.Load() != admitted.Load() {
		t.Fatalf("commits = %d, quero %d (idempotência de finish falhou)", sp.commitCount.Load(), admitted.Load())
	}
}

// ---------------------------------------------------------------------------
// Guardas de construção fail-closed.
// ---------------------------------------------------------------------------

func TestNewSpawnCoordinator_DepsRequired(t *testing.T) {
	t.Parallel()
	b, _ := budget.New("x", budget.Amount{Tokens: 1, CostMicroUSD: 1})
	sp := &budgetSpawner{b: b}
	if _, err := scheduler.NewSpawnCoordinator(nil, sp); !errors.Is(err, scheduler.ErrSpawnCoordinatorDeps) {
		t.Fatalf("headroom nil: erro = %v, quero ErrSpawnCoordinatorDeps", err)
	}
	adm, _ := newAdmForSpawn(t, 1, 1, time.Unix(1, 0))
	if _, err := scheduler.NewSpawnCoordinator(adm, nil); !errors.Is(err, scheduler.ErrSpawnCoordinatorDeps) {
		t.Fatalf("delegator nil: erro = %v, quero ErrSpawnCoordinatorDeps", err)
	}
}

// ---------------------------------------------------------------------------
// Tenant: o headroom (e o max_spawn derivado) respeita o tecto do tenant; o
// global DOMINA sempre (min entre global e tenant).
// ---------------------------------------------------------------------------

func TestMaxSpawn_TenantCapBinds(t *testing.T) {
	t.Parallel()
	base := time.Unix(8_000_000, 0)
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	// Global abundante (TPM=100k), mas o tenant "t1" tem tecto de 2000 tokens.
	qp := scheduler.NewStaticQuotaProvider(scheduler.ProviderLimits{TPM: 100_000, RPM: 1_000_000, Window: time.Minute})
	qp.SetTenant(spawnKey, "t1", scheduler.ProviderLimits{TPM: 2000, RPM: 1_000_000, Window: time.Minute})
	adm, err := scheduler.NewAdmission(es, qp, scheduler.WithClock(fixed(base)), scheduler.WithIDGen(seqGen("res")))
	if err != nil {
		t.Fatalf("NewAdmission: %v", err)
	}
	b, _ := budget.New("run-T", budget.Amount{Tokens: 1_000_000, CostMicroUSD: 1_000_000})
	coord, _ := scheduler.NewSpawnCoordinator(adm, &budgetSpawner{b: b}, scheduler.WithSpawnClock(fixed(base)))
	ctx := context.Background()

	// Sem tenant: max_spawn segue o global (100000/1000 = 100).
	if m, _ := coord.MaxSpawn(ctx, spawnKey, "", 1000); m != 100 {
		t.Fatalf("max_spawn global = %d, quero 100", m)
	}
	// Com o tenant "t1": limitado a 2000/1000 = 2 (o tecto do tenant vence).
	if m, _ := coord.MaxSpawn(ctx, spawnKey, "t1", 1000); m != 2 {
		t.Fatalf("max_spawn tenant = %d, quero 2 (tecto do tenant)", m)
	}
	// Confirma o snapshot: headroom = min(global, tenant).
	snap, _ := adm.Headroom(ctx, spawnKey, "t1")
	if snap.Tokens != 2000 || snap.LimitTokens != 2000 {
		t.Fatalf("headroom tenant = %+v, quero Tokens/LimitTokens=2000", snap)
	}
}

// ---------------------------------------------------------------------------
// WithSpawnIDGen: com RequestID vazio, o id da reserva vem do gerador injectado
// (determinismo/replay).
// ---------------------------------------------------------------------------

func TestRequestSpawn_InjectedIDGen(t *testing.T) {
	t.Parallel()
	base := time.Unix(9_000_000, 0)
	adm, es := newAdmForSpawn(t, 1_000_000, 1_000_000, base)
	b, _ := budget.New("run-H", budget.Amount{Tokens: 1_000_000, CostMicroUSD: 1_000_000})
	coord, _ := scheduler.NewSpawnCoordinator(adm, &budgetSpawner{b: b},
		scheduler.WithSpawnClock(fixed(base)),
		scheduler.WithSpawnEventLog(es, eventstore.Producer{NHIID: "nhi:test"}),
		scheduler.WithSpawnIDGen(func() string { return "fixed-headroom-id" }),
	)
	ctx := context.Background()
	slice := budget.Amount{Tokens: 10, CostMicroUSD: 10}

	out, err := coord.RequestSpawn(ctx, spawnReq("run-H", "c1", 1000, slice))
	if err != nil || !out.Admitted {
		t.Fatalf("spawn: err=%v out=%+v", err, out)
	}
	if out.Ticket.HeadroomReservationID != "fixed-headroom-id" {
		t.Fatalf("reservation id = %q, quero o do idGen injectado", out.Ticket.HeadroomReservationID)
	}
	recs, _ := coord.ReplaySpawnAdmission(ctx, "run-H")
	if len(recs) != 1 || recs[0].ReservationID != "fixed-headroom-id" {
		t.Fatalf("evento não usou o id injectado; recs=%+v", recs)
	}
}

// ---------------------------------------------------------------------------
// Finish com erro de consolidação do sub-orçamento: o guard é REVERTIDO e o
// headroom NÃO é libertado (um retry pode voltar a tentar; sem estado meio-feito).
// ---------------------------------------------------------------------------

type errFinishSpawner struct{ called atomic.Int64 }

func (s *errFinishSpawner) Spawn(_ context.Context, req orchestrator.SpawnRequest) (*orchestrator.SpawnHandle, error) {
	return &orchestrator.SpawnHandle{RunID: req.RunID, ChildTaskID: req.ChildTaskID}, nil
}
func (s *errFinishSpawner) Finish(_ context.Context, _ *orchestrator.SpawnHandle, _ bool) error {
	s.called.Add(1)
	return errors.New("boom: backend do budget indisponível")
}

func TestFinish_DelegatorErrorRevertsGuardKeepsHeadroom(t *testing.T) {
	t.Parallel()
	base := time.Unix(10_000_000, 0)
	adm, _ := newAdmForSpawn(t, 1_000_000, 1_000_000, base)
	sp := &errFinishSpawner{}
	coord, _ := scheduler.NewSpawnCoordinator(adm, sp, scheduler.WithSpawnClock(fixed(base)))
	ctx := context.Background()
	slice := budget.Amount{Tokens: 10, CostMicroUSD: 10}
	const cost = int64(1000)

	out, err := coord.RequestSpawn(ctx, spawnReq("run-I", "c1", cost, slice))
	if err != nil || !out.Admitted {
		t.Fatalf("spawn: err=%v out=%+v", err, out)
	}
	// 1ª tentativa de Finish falha na consolidação do sub-orçamento.
	if err := coord.Finish(ctx, out.Ticket, true); err == nil {
		t.Fatalf("Finish devia devolver erro de consolidação")
	}
	// O headroom NÃO foi libertado (a consolidação não completou).
	hr, _ := adm.Headroom(ctx, spawnKey, "")
	if hr.Tokens != 1_000_000-cost {
		t.Fatalf("headroom = %d, quero %d (não libertar em erro de consolidação)", hr.Tokens, 1_000_000-cost)
	}
	// O guard foi revertido: um retry volta a tentar (não é no-op silencioso).
	if err := coord.Finish(ctx, out.Ticket, true); err == nil {
		t.Fatalf("retry de Finish devia voltar a tentar (guard revertido)")
	}
	if sp.called.Load() != 2 {
		t.Fatalf("Delegator.Finish chamado %d vezes, quero 2 (guard revertido em erro)", sp.called.Load())
	}
}

func TestFinish_NilTicket(t *testing.T) {
	t.Parallel()
	adm, _ := newAdmForSpawn(t, 1000, 1000, time.Unix(1, 0))
	b, _ := budget.New("x", budget.Amount{Tokens: 1, CostMicroUSD: 1})
	coord, _ := scheduler.NewSpawnCoordinator(adm, &budgetSpawner{b: b})
	if err := coord.Finish(context.Background(), nil, true); !errors.Is(err, scheduler.ErrNilSpawnTicket) {
		t.Fatalf("Finish(nil): erro = %v, quero ErrNilSpawnTicket", err)
	}
}

// ---------------------------------------------------------------------------
// Finish CONCORRENTE do mesmo ticket COMBINADO com erro de consolidação: prova
// que a serialização por ticket elimina o TOCTOU (um Finish concorrente nunca
// devolve falso-sucesso enquanto o outro está a meio, com o guard por consolidar).
// gatedFinishSpawner controla, com sincronização determinística (sem sleeps de
// corrida), o instante em que delegator.Finish retorna e o seu veredicto.
// ---------------------------------------------------------------------------

type gatedFinishSpawner struct {
	finishCalls atomic.Int64
	entered     chan struct{} // sinaliza que Finish entrou (segura o mutex do ticket)
	release     chan error    // desbloqueia Finish com o veredicto a devolver
}

func (s *gatedFinishSpawner) Spawn(_ context.Context, req orchestrator.SpawnRequest) (*orchestrator.SpawnHandle, error) {
	return &orchestrator.SpawnHandle{RunID: req.RunID, ChildTaskID: req.ChildTaskID}, nil
}
func (s *gatedFinishSpawner) Finish(_ context.Context, _ *orchestrator.SpawnHandle, _ bool) error {
	s.finishCalls.Add(1)
	s.entered <- struct{}{}
	return <-s.release
}

func TestFinish_ConcurrentWithDelegatorErrorNoFalseSuccess(t *testing.T) {
	t.Parallel()
	base := time.Unix(11_000_000, 0)
	adm, _ := newAdmForSpawn(t, 1_000_000, 1_000_000, base)
	const cost = int64(1000)
	sp := &gatedFinishSpawner{entered: make(chan struct{}), release: make(chan error)}
	coord, _ := scheduler.NewSpawnCoordinator(adm, sp, scheduler.WithSpawnClock(fixed(base)))
	ctx := context.Background()
	slice := budget.Amount{Tokens: 10, CostMicroUSD: 10}

	out, err := coord.RequestSpawn(ctx, spawnReq("run-J", "c1", cost, slice))
	if err != nil || !out.Admitted {
		t.Fatalf("spawn: err=%v out=%+v", err, out)
	}

	// T1 entra em Finish e fica retido DENTRO de delegator.Finish (segura o mutex).
	t1err := make(chan error, 1)
	go func() { t1err <- coord.Finish(ctx, out.Ticket, true) }()
	<-sp.entered // T1 está dentro de delegator.Finish, a segurar o mutex do ticket.

	// T2 chama Finish do MESMO ticket: tem de BLOQUEAR no mutex — não pode devolver
	// no-op (falso-sucesso) enquanto T1 não consolidou (era o TOCTOU do guard atómico).
	t2done := make(chan error, 1)
	go func() { t2done <- coord.Finish(ctx, out.Ticket, true) }()
	select {
	case <-t2done:
		t.Fatalf("T2 devolveu enquanto T1 estava a meio da consolidação (falso-sucesso / TOCTOU)")
	case <-time.After(50 * time.Millisecond):
		// T2 correctamente bloqueado no mutex do ticket (comportamento esperado).
	}

	// T1 falha a consolidação e liberta o mutex SEM marcar feito.
	sp.release <- errors.New("boom transitório")
	if err := <-t1err; err == nil {
		t.Fatalf("T1 devia devolver o erro de consolidação")
	}
	// Headroom NÃO libertado (consolidação falhou): sem estado meio-feito.
	hrMid, _ := adm.Headroom(ctx, spawnKey, "")
	if hrMid.Tokens != 1_000_000-cost {
		t.Fatalf("headroom = %d, quero %d (erro de consolidação não liberta)", hrMid.Tokens, 1_000_000-cost)
	}

	// T2, desbloqueado, VOLTA a tentar (não foi no-op): agora delegator.Finish sucede.
	<-sp.entered // T2 entrou em delegator.Finish
	sp.release <- nil
	if err := <-t2done; err != nil {
		t.Fatalf("T2 (retry após desbloqueio) devia consolidar; err=%v", err)
	}
	// Consolidação aconteceu exactamente uma vez ⇒ headroom recupera por inteiro.
	hrFinal, _ := adm.Headroom(ctx, spawnKey, "")
	if hrFinal.Tokens != 1_000_000 {
		t.Fatalf("headroom final = %d, quero 1000000 (consolidação única, sem fuga)", hrFinal.Tokens)
	}
	if sp.finishCalls.Load() != 2 {
		t.Fatalf("delegator.Finish chamado %d vezes, quero 2 (T1 erro + T2 sucesso)", sp.finishCalls.Load())
	}
}
