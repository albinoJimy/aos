package scheduler_test

// loadscale_test.go — TESTES DE CARGA/ESCALA (AOS-116, EPIC-11, ADR-008).
//
// O modo de falha central do plano-base é "individualmente ok, agregadamente
// colapsa": N boards, cada um DENTRO do seu max_spawn local, saturam
// COLECTIVAMENTE o rate limit PARTILHADO. A defesa (ADR-008) é admission control
// GLOBAL com reserva ATÓMICA de headroom + backpressure com degradação graciosa
// (shed→defer→downgrade→reject). Estes testes REPRODUZEM a saturação agregada e
// PROVAM que o sistema DEGRADA em vez de colapsar.
//
// É um teste de COMPOSIÇÃO: NÃO se cria nenhum primitivo de produção. Compõem-se os
// primitivos PÚBLICOS do pacote (Admission/SpawnCoordinator/PolicyEngine/Degrader/
// PartitionedQueues/Breaker/WaitP95Recorder/HorizontalScaler/RecordingMeter),
// reutilizando os helpers deterministas dos testes existentes (fixed/fixedClock/
// mutClock/seqGen/newAdmForSpawn/budgetSpawner/newES/newTree/newBreaker/consume/
// DeriveMaxSpawnForTest/NewRecordingMeter). A carga agregada é CONTADORES/ITERAÇÕES
// in-process (N boards × spawns), NÃO concorrência wall-clock — determinista,
// -race limpo, sem flakiness dependente de máquina.
//
// PARAMETRIZAÇÃO DA CARGA (documentada): as constantes loadCost/loadCapSpawns/
// loadNBoards/loadSpawnsPerBoard governam a intensidade. O invariante-chave é
// loadNBoards*loadSpawnsPerBoard (carga agregada) > loadCapSpawns (headroom global
// em unidades de sub-agente) — cada board dentro do seu max local, o agregado acima
// do tecto partilhado.
//
// Cobre os critérios de aceitação:
//   AC1 TestScale_AggregateSaturation_AtomicReservationNoOversubscribe
//   AC2 TestScale_GracefulDegradationInOrder
//   AC3 TestScale_QueuesRemainBoundedUnderOverload
//   AC4 TestScale_BudgetBreakerTripsAtThreshold
//   AC5 TestScale_NFRSignalsReported (emite AOS_SCALE_REPORT)
//   meta TestMetaDetects_AggregateCollapseWithoutAdmission (não-tautológico)

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/scheduler"
)

// ---------------------------------------------------------------------------
// Parametrização da carga agregada (documentada). O invariante:
// loadNBoards*loadSpawnsPerBoard > loadCapSpawns — cada board dentro do seu max
// local, o AGREGADO acima do tecto partilhado (o modo de falha do RB-01).
// ---------------------------------------------------------------------------

const (
	loadCost           = int64(1000) // custo (tokens) por sub-agente
	loadCapSpawns      = 6           // headroom global = loadCapSpawns*loadCost tokens
	loadNBoards        = 4           // nº de boards, cada um com o seu SpawnCoordinator
	loadSpawnsPerBoard = 4           // spawns que CADA board tenta (dentro do seu max local)
	// aggregate = 16 tentativas; headroom global só cabe 6 ⇒ 10 DEFER, sem oversubscribe.
)

// scaleBoard é um board: um SpawnCoordinator com o seu sub-orçamento REAL
// (budgetSpawner), TODOS sobre UMA Admission partilhada (o token-bucket global).
type scaleBoard struct {
	id    string
	coord *scheduler.SpawnCoordinator
	sp    *budgetSpawner
}

// newScaleBoards constrói loadNBoards boards sobre a MESMA Admission partilhada. O
// sub-orçamento de cada árvore é FOLGADO — assim a única causa de recusa possível é
// o headroom GLOBAL (o modo de falha do RB-01), não o sub-orçamento (RB-03).
func newScaleBoards(t *testing.T, adm *scheduler.Admission, n int, base time.Time) []scaleBoard {
	t.Helper()
	boards := make([]scaleBoard, 0, n)
	for i := 0; i < n; i++ {
		runID := "board-" + itoa(int64(i))
		b, _ := budget.New(runID, budget.Amount{Tokens: 1_000_000, CostMicroUSD: 1_000_000})
		sp := &budgetSpawner{b: b}
		coord, err := scheduler.NewSpawnCoordinator(adm, sp, scheduler.WithSpawnClock(fixed(base)))
		if err != nil {
			t.Fatalf("NewSpawnCoordinator board %d: %v", i, err)
		}
		boards = append(boards, scaleBoard{id: runID, coord: coord, sp: sp})
	}
	return boards
}

// aggregateResult resume uma passagem de carga agregada.
type aggregateResult struct {
	admitted int
	deferred int
	tickets  []*scheduler.SpawnTicket
}

// runAggregateLoad corre a carga agregada DETERMINISTA: cada board tenta
// spawnsPerBoard spawns, por ordem. Um defer (ErrSpawnDeferredNoHeadroom) é a
// resposta ESPERADA sob headroom nulo — contabiliza-se, não é erro. Qualquer outro
// erro é fatal. Sem goroutines: a "carga" é a ITERAÇÃO in-process (determinista).
func runAggregateLoad(t *testing.T, boards []scaleBoard, spawnsPerBoard int, cost int64) aggregateResult {
	t.Helper()
	ctx := context.Background()
	slice := budget.Amount{Tokens: 10, CostMicroUSD: 10}
	var res aggregateResult
	for _, bd := range boards {
		for s := 0; s < spawnsPerBoard; s++ {
			child := "c-" + itoa(int64(s))
			out, err := bd.coord.RequestSpawn(ctx, spawnReq(bd.id, child, cost, slice))
			switch {
			case err == nil && out.Admitted:
				res.admitted++
				res.tickets = append(res.tickets, out.Ticket)
			case errors.Is(err, scheduler.ErrSpawnDeferredNoHeadroom):
				// Excesso agregado ADIADO (nunca descarte silencioso nem spawn sem reserva).
				if !out.Deferred || out.RetryAfter <= 0 {
					t.Fatalf("defer sem Deferred/RetryAfter: out=%+v err=%v", out, err)
				}
				res.deferred++
			default:
				t.Fatalf("RequestSpawn(%s/%d): erro inesperado %v (out=%+v)", bd.id, s, err, out)
			}
		}
	}
	return res
}

// ---------------------------------------------------------------------------
// AC1 — SATURAÇÃO AGREGADA / RESERVA ATÓMICA: a soma dos grants NUNCA excede o
// headroom global (sem oversubscription — o invariante CAS); o excesso vem como
// DEFER; max_spawn deriva do headroom (0 sob headroom nulo); recuperação via Finish.
// ---------------------------------------------------------------------------

func TestScale_AggregateSaturation_AtomicReservationNoOversubscribe(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	ctx := context.Background()

	// Admission PARTILHADA: TPM = loadCapSpawns*loadCost (cabe exactamente
	// loadCapSpawns sub-agentes em voo); RPM largo (isola a causa no eixo TOKENS).
	adm, _ := newAdmForSpawn(t, loadCapSpawns*loadCost, 1_000_000, base)
	boards := newScaleBoards(t, adm, loadNBoards, base)

	// DIAGNÓSTICO: com headroom cheio, max_spawn de um board é loadCapSpawns (>0, NÃO
	// constante — a causa-raiz do colapso agregado é um max_spawn fixo cego ao global).
	if m, err := boards[0].coord.MaxSpawn(ctx, spawnKey, "", loadCost); err != nil || m != loadCapSpawns {
		t.Fatalf("max_spawn inicial = %d (err=%v), quero %d", m, err, loadCapSpawns)
	}

	// CARGA AGREGADA: loadNBoards*loadSpawnsPerBoard tentativas > loadCapSpawns headroom.
	attempts := loadNBoards * loadSpawnsPerBoard
	if attempts <= loadCapSpawns {
		t.Fatalf("parametrização inválida: agregado %d <= tecto %d (não satura)", attempts, loadCapSpawns)
	}
	res := runAggregateLoad(t, boards, loadSpawnsPerBoard, loadCost)

	// INVARIANTE CENTRAL (ADR-008): a soma dos grants == headroom global, NUNCA acima.
	if res.admitted != loadCapSpawns {
		t.Fatalf("admitidos = %d, quero EXACTAMENTE %d (o tecto global — nem mais [oversubscribe] nem menos)", res.admitted, loadCapSpawns)
	}
	if res.deferred != attempts-loadCapSpawns {
		t.Fatalf("adiados = %d, quero %d (o excesso agregado é DEFER, não descarte)", res.deferred, attempts-loadCapSpawns)
	}

	// O headroom global está no CHÃO e NUNCA negativo (sem oversubscription). A soma das
	// reservas activas == TPM exacto.
	hr, err := adm.Headroom(ctx, spawnKey, "")
	if err != nil {
		t.Fatalf("Headroom: %v", err)
	}
	if hr.Tokens != 0 {
		t.Fatalf("headroom = %d, quero 0 (reservas activas == TPM; nunca oversubscription)", hr.Tokens)
	}

	// max_spawn derivado colapsa para 0 sob headroom nulo (fail-closed, não constante).
	if m, _ := boards[0].coord.MaxSpawn(ctx, spawnKey, "", loadCost); m != 0 {
		t.Fatalf("max_spawn sob headroom nulo = %d, quero 0", m)
	}
	// deriveMaxSpawn é 0 sob headroom nulo (prova directa da fórmula pura).
	if m := scheduler.DeriveMaxSpawnForTest(0, 1_000_000, loadCost); m != 0 {
		t.Fatalf("DeriveMaxSpawn(0,...) = %d, quero 0", m)
	}

	// Prova DIRECTA de que o Delegator só foi tocado loadCapSpawns vezes no AGREGADO: a
	// soma dos spawnCount de todos os boards == admitidos (nenhum spawn sem reserva).
	var totalSpawns int64
	for _, bd := range boards {
		totalSpawns += bd.sp.spawnCount.Load()
	}
	if totalSpawns != int64(loadCapSpawns) {
		t.Fatalf("sub-agentes criados no agregado = %d, quero %d (spawn sem débito reservado!)", totalSpawns, loadCapSpawns)
	}

	// RECUPERAÇÃO: libertar UMA reserva (Finish) devolve headroom e o próximo admit passa.
	if err := boards[0].coord.Finish(ctx, res.tickets[0], true); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if m, _ := boards[0].coord.MaxSpawn(ctx, spawnKey, "", loadCost); m != 1 {
		t.Fatalf("max_spawn após libertar 1 = %d, quero 1 (recuperação)", m)
	}
	out, err := boards[1].coord.RequestSpawn(ctx, spawnReq(boards[1].id, "recover", loadCost, budget.Amount{Tokens: 10, CostMicroUSD: 10}))
	if err != nil || !out.Admitted {
		t.Fatalf("após libertar headroom o spawn adiado devia ser admitido; err=%v out=%+v", err, out)
	}
}

// ---------------------------------------------------------------------------
// AC2 — DEGRADAÇÃO GRACIOSA POR ORDEM: a escada segue DefaultPreferenceOrder
// shed→defer→downgrade→reject. Prova a ORDEM via PolicyEngine.Select (a pressão
// crescente selecciona o degrau) + Degrader.Execute/ExecuteChain (o degrau
// executa-se), que reject é terminal fail-closed, e liga PartitionedQueues como
// BackpressureSource (WithBackpressure) para conduzir a saturação até ao admit.
// ---------------------------------------------------------------------------

func TestScale_GracefulDegradationInOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := time.Unix(2_000_000, 0)

	// A ordem canónica da fonte (ADR-008) é EXACTAMENTE shed→defer→downgrade→reject.
	want := []scheduler.DegradationAction{
		scheduler.ActionShed, scheduler.ActionDefer, scheduler.ActionDowngrade, scheduler.ActionReject,
	}
	if len(scheduler.DefaultPreferenceOrder) != len(want) {
		t.Fatalf("DefaultPreferenceOrder tem %d degraus, quero %d", len(scheduler.DefaultPreferenceOrder), len(want))
	}
	for i, a := range want {
		if scheduler.DefaultPreferenceOrder[i] != a {
			t.Fatalf("DefaultPreferenceOrder[%d] = %q, quero %q (ordem canónica)", i, scheduler.DefaultPreferenceOrder[i], a)
		}
	}

	// Escada de tiers (premium→standard→economy) para o downgrade ter para onde descer.
	router := scheduler.NewStaticModelTierRouter(
		scheduler.ModelTier{Tier: "premium", Model: "opus", CostRank: 3},
		scheduler.ModelTier{Tier: "standard", Model: "sonnet", CostRank: 2},
		scheduler.ModelTier{Tier: "economy", Model: "haiku", CostRank: 1},
	)
	es := newES(t)
	deg, err := scheduler.NewDegrader(router,
		scheduler.WithDegradationLog(es),
		scheduler.WithDegradationClock(fixed(base)),
		scheduler.WithDeferRetry(250*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewDegrader: %v", err)
	}

	// Política declarativa: a pressão CRESCENTE (fill ratio) escala pela ordem canónica.
	// Regras avaliadas por ordem (1ª que casa vence) — as mais severas primeiro.
	policyJSON := []byte(`{
	  "version":"1.0.0",
	  "rules":[
	    {"min_fill_ratio":0.95,"action":"reject"},
	    {"min_fill_ratio":0.85,"action":"downgrade"},
	    {"min_fill_ratio":0.70,"action":"defer"}
	  ],
	  "default_action":"shed"
	}`)
	doc, err := scheduler.ParsePolicy(policyJSON)
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
	}
	engine, err := scheduler.NewPolicyEngine(doc,
		scheduler.WithPolicyLog(es),
		scheduler.WithPolicyClock(fixed(base)),
	)
	if err != nil {
		t.Fatalf("NewPolicyEngine: %v", err)
	}

	// Pressão crescente ⇒ Select devolve a escada POR ORDEM: shed→defer→downgrade→reject.
	fills := []float64{0.50, 0.75, 0.88, 0.97}
	var selected []scheduler.DegradationAction
	var executed []scheduler.DegradationAction
	for _, fill := range fills {
		cond := scheduler.SaturationCondition{
			Tenant: "acme", Priority: "P2",
			Depth: int(fill * 100), Capacity: 100, FillRatio: fill, Saturated: true,
		}
		action, ver, serr := engine.Select(ctx, cond)
		if serr != nil {
			t.Fatalf("Select(fill=%.2f): %v", fill, serr)
		}
		selected = append(selected, action)

		// EXECUTA o degrau seleccionado sobre um item PLENAMENTE elegível (Optional+
		// reversível com tier mais barato): assim o degrau executado é o SELECCIONADO
		// pela pressão, não a elegibilidade do item.
		item := scheduler.DegradationItem{
			ID: "w-" + string(action), Tenant: "acme", Priority: "P2",
			Optional: true, Deferrable: true,
			CurrentTier: "premium", CurrentModel: "opus", Key: spawnKey,
		}
		trigger := scheduler.TriggerFromCondition(cond, ver, "aggregate_saturation")
		out, xerr := deg.Execute(ctx, action, item, trigger)
		if action == scheduler.ActionReject {
			// Reject é TERMINAL fail-closed: devolve sempre ErrWorkRejected (Applied=true).
			if !errors.Is(xerr, scheduler.ErrWorkRejected) {
				t.Fatalf("reject: erro = %v, quero ErrWorkRejected (terminal fail-closed)", xerr)
			}
			if !out.Applied || out.Reversible {
				t.Fatalf("reject: Applied/Reversible = %v/%v, quero true/false (terminal)", out.Applied, out.Reversible)
			}
		} else if xerr != nil {
			t.Fatalf("Execute(%q): %v", action, xerr)
		}
		executed = append(executed, out.Action)
	}

	// A sequência SELECCIONADA e a EXECUTADA respeitam AMBAS a ordem canónica.
	assertActionOrder(t, "seleccionada", selected, want)
	assertActionOrder(t, "executada", executed, want)

	// ExecuteChain a partir do topo (shed) sobre um item plenamente elegível aplica o
	// PRIMEIRO degrau (shed) — a preferência com fallback respeita a ordem.
	fullItem := scheduler.DegradationItem{
		ID: "chain-full", Tenant: "acme", Priority: "P2", Optional: true, Deferrable: true,
		CurrentTier: "premium", CurrentModel: "opus", Key: spawnKey,
	}
	chainTrig := scheduler.DegradationTrigger{Reason: "chain", Partition: "acme:P2"}
	if r, cerr := deg.ExecuteChain(ctx, fullItem, chainTrig, nil); cerr != nil || r.Action != scheduler.ActionShed {
		t.Fatalf("ExecuteChain(elegível) = %q (err=%v), quero shed (1º degrau aplicável)", r.Action, cerr)
	}

	// ExecuteChain sobre um item que NÃO é shed/defer/downgrade-ável escala até ao
	// degrau TERMINAL reject fail-closed (crítico, não-diferível, já no tier barato).
	terminalItem := scheduler.DegradationItem{
		ID: "chain-terminal", Tenant: "acme", Priority: "P0", Critical: true,
		CurrentTier: "economy", CurrentModel: "haiku", Key: spawnKey,
	}
	rr, cerr := deg.ExecuteChain(ctx, terminalItem, chainTrig, nil)
	if !errors.Is(cerr, scheduler.ErrWorkRejected) {
		t.Fatalf("ExecuteChain(terminal): erro = %v, quero ErrWorkRejected (fail-closed)", cerr)
	}
	if rr.Action != scheduler.ActionReject {
		t.Fatalf("ExecuteChain(terminal) acção = %q, quero reject", rr.Action)
	}

	// LIGA PartitionedQueues como BackpressureSource: uma fila saturada conduz o admit a
	// ADIAR mesmo COM headroom abundante — o sinal propaga-se a montante (o degrau
	// "defer" da escada aplicado no caminho de admissão).
	q, err := scheduler.NewPartitionedQueues(scheduler.DefaultQueueLimits(2),
		scheduler.WithQueueClock(fixed(base)),
		scheduler.WithBackpressureRetry(500*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewPartitionedQueues: %v", err)
	}
	// Satura a partição do tenant "acme" (high watermark de MaxLen=2 é 1).
	if _, err := q.Enqueue(ctx, scheduler.WorkItem{ID: "x", Tenant: "acme", Priority: "P1"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if !q.IsSaturated(scheduler.Partition{Tenant: "acme", Priority: "P1"}) {
		t.Fatal("a partição devia estar saturada após cruzar o high watermark")
	}
	// Admission com headroom ABUNDANTE (TPM enorme) — a ÚNICA causa de defer possível é
	// o backpressure da fila saturada.
	bpES := newES(t)
	qp := scheduler.NewStaticQuotaProvider(scheduler.ProviderLimits{TPM: 1_000_000, RPM: 1_000_000, Window: time.Minute})
	admBP, err := scheduler.NewAdmission(bpES, qp,
		scheduler.WithClock(fixed(base)), scheduler.WithIDGen(seqIDGen()),
		scheduler.WithBackpressure(q),
	)
	if err != nil {
		t.Fatalf("NewAdmission(backpressure): %v", err)
	}
	admit, err := admBP.Admit(ctx, scheduler.AdmitRequest{Key: spawnKey, Tenant: "acme", EstimatedTokens: 10, RequestID: "bp-1"})
	if err != nil {
		t.Fatalf("Admit(sob backpressure): %v", err)
	}
	if admit.Granted || admit.Rejected {
		t.Fatalf("admit sob backpressure = %+v, quero DEFER (Granted=false, Rejected=false)", admit)
	}
	if admit.RetryAfter <= 0 {
		t.Fatalf("defer por backpressure sem RetryAfter aconselhado; got %v", admit.RetryAfter)
	}
	// Um tenant SEM fila saturada (headroom abundante) é admitido — prova que o defer
	// veio do backpressure, não de escassez global.
	admitOther, err := admBP.Admit(ctx, scheduler.AdmitRequest{Key: spawnKey, Tenant: "other", EstimatedTokens: 10, RequestID: "bp-2"})
	if err != nil || !admitOther.Granted {
		t.Fatalf("tenant sem fila saturada devia ser admitido; err=%v out=%+v", err, admitOther)
	}
}

// assertActionOrder confirma que a sequência de acções bate EXACTAMENTE a ordem
// canónica esperada (prova de ORDEM, não só de presença).
func assertActionOrder(t *testing.T, label string, got, want []scheduler.DegradationAction) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("sequência %s tem %d acções, quero %d", label, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sequência %s[%d] = %q, quero %q (ordem shed→defer→downgrade→reject)", label, i, got[i], want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// AC3 — FILAS LIMITADAS SOB SOBRECARGA: PartitionedQueues NÃO cresce além de
// MaxLen (Enqueue aplica a política ao atingir o limite, não acumula); Depth<=MaxLen
// SEMPRE; o excesso é rejeitado/degradado em vez de colapsar (sem cascata de timeouts).
// ---------------------------------------------------------------------------

func TestScale_QueuesRemainBoundedUnderOverload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := time.Unix(3_000_000, 0)

	const maxLen = 8
	const overload = 50 // MUITO acima do tecto: a fila NÃO pode acumular os 50.

	es := newES(t)
	// Política: ao atingir o limite (fill>=1.0) REJEITA; senão faz shed. Prova que a
	// resposta ao limite é degradar, não crescer.
	doc, err := scheduler.ParsePolicy([]byte(`{
	  "version":"1.0.0",
	  "rules":[{"min_fill_ratio":1.0,"action":"reject"}],
	  "default_action":"shed"
	}`))
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
	}
	engine, err := scheduler.NewPolicyEngine(doc, scheduler.WithPolicyClock(fixed(base)))
	if err != nil {
		t.Fatalf("NewPolicyEngine: %v", err)
	}
	q, err := scheduler.NewPartitionedQueues(scheduler.DefaultQueueLimits(maxLen),
		scheduler.WithQueueClock(fixed(base)),
		scheduler.WithQueuePolicy(engine),
		scheduler.WithQueueLog(es),
	)
	if err != nil {
		t.Fatalf("NewPartitionedQueues: %v", err)
	}

	part := scheduler.Partition{Tenant: "acme", Priority: "P2"}
	admitted, degraded := 0, 0
	for i := 0; i < overload; i++ {
		out, err := q.Enqueue(ctx, scheduler.WorkItem{ID: itoa(int64(i)), Tenant: "acme", Priority: "P2"})
		if err != nil {
			t.Fatalf("Enqueue #%d: %v", i, err)
		}
		// INVARIANTE DURO: a profundidade NUNCA excede MaxLen (fila LIMITADA).
		if out.Depth > maxLen {
			t.Fatalf("Enqueue #%d: Depth = %d > MaxLen %d (acumulação ilimitada!)", i, out.Depth, maxLen)
		}
		if d := q.Depth(part); d > maxLen {
			t.Fatalf("Depth() = %d > MaxLen %d após #%d (acumulação ilimitada!)", d, maxLen, i)
		}
		if out.Admitted {
			admitted++
		} else {
			degraded++
			// Ao atingir o limite, aplicou-se a POLÍTICA (acção não-vazia) em vez de crescer.
			if out.Action == "" {
				t.Fatalf("Enqueue #%d recusado sem acção de política (devia degradar, não crescer)", i)
			}
		}
	}

	// Exactamente MaxLen aceites; TODO o excesso degradado (rejeitado/shed), NUNCA
	// absorvido — o sistema degrada em vez de colapsar.
	if admitted != maxLen {
		t.Fatalf("aceites = %d, quero %d (a fila enche até MaxLen e não além)", admitted, maxLen)
	}
	if degraded != overload-maxLen {
		t.Fatalf("degradados = %d, quero %d (o excesso é degradado, não acumulado)", degraded, overload-maxLen)
	}
	// Estado final observável: Depth == MaxLen e partição saturada.
	if d := q.Depth(part); d != maxLen {
		t.Fatalf("Depth final = %d, quero %d (limitada)", d, maxLen)
	}
	if !q.IsSaturated(part) {
		t.Fatal("a partição devia estar saturada sob sobrecarga")
	}
	// O Snapshot (observabilidade) confirma o limite duro em todas as partições.
	for _, qs := range q.Snapshot() {
		if qs.Depth > qs.Capacity {
			t.Fatalf("Snapshot %s: Depth %d > Capacity %d (não-limitada)", qs.Partition, qs.Depth, qs.Capacity)
		}
	}
}

// ---------------------------------------------------------------------------
// AC4 — CIRCUIT BREAKER DE ORÇAMENTO: dispara nos limiares (Exhaustion E Velocity),
// Allow nega fail-closed, e é OBSERVÁVEL via Replay (eventos) + aviso ~80%. Relógio
// mutável (mutClock) determinista — sem wall-clock.
// ---------------------------------------------------------------------------

func TestScale_BudgetBreakerTripsAtThreshold(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// --- (a) ESGOTAMENTO: burn-down até à margem ⇒ trip por exhaustion. ---
	t.Run("exhaustion", func(t *testing.T) {
		clk := &mutClock{}
		clk.set(time.Unix(1_000_000, 0))
		const treeID = "run-exh"
		b := newTree(t, treeID, budget.Amount{Tokens: 1000, CostMicroUSD: 1000})
		es := newES(t)
		th := scheduler.Thresholds{
			ExhaustionMargin: budget.Amount{Tokens: 100, CostMicroUSD: 100},
			WarnFraction:     0.8, // aviso de exaustão graciosa a ~80% ANTES do hard-trip.
			// velocidade desligada (Window 0): isola o eixo de ESGOTAMENTO.
		}
		br := newBreaker(t, es, b, treeID, th, scheduler.WithBreakerClock(clk.now))

		// Estado inicial: closed, permite (caminho feliz).
		if ok, st, _ := br.Allow(ctx); !ok || st != scheduler.BreakerClosed {
			t.Fatalf("inicial = (%v,%s), quero (permite, closed)", ok, st)
		}
		// ~85% consumido: aviso ~80% emitido, ainda SEM trip (remanescente 150 > margem 100).
		consume(t, b, treeID, budget.Amount{Tokens: 850, CostMicroUSD: 850})
		if st, err := br.Observe(ctx, budget.Amount{Tokens: 850, CostMicroUSD: 850}); err != nil || st != scheduler.BreakerClosed {
			t.Fatalf("~85%%: (%s,err=%v), quero closed (só aviso)", st, err)
		}
		// Esgota até <= margem ⇒ TRIP (→ open).
		consume(t, b, treeID, budget.Amount{Tokens: 100, CostMicroUSD: 100}) // resta 50/50 <= 100
		st, err := br.Observe(ctx, budget.Amount{Tokens: 100, CostMicroUSD: 100})
		if err != nil {
			t.Fatalf("Observe (esgotado): %v", err)
		}
		if st != scheduler.BreakerOpen {
			t.Fatalf("estado após esgotamento = %s, quero open (trip)", st)
		}
		// Allow NEGA fail-closed com o breaker open.
		if ok, st, _ := br.Allow(ctx); ok || st != scheduler.BreakerOpen {
			t.Fatalf("Allow(open) = (%v,%s), quero (nega, open)", ok, st)
		}
		// OBSERVÁVEL: o Replay contém o aviso ~80% a PRECEDER o trip por exhaustion.
		recs, err := br.Replay(ctx)
		if err != nil {
			t.Fatalf("Replay: %v", err)
		}
		var warnSeq, tripSeq uint64
		var tripReason string
		for _, r := range recs {
			switch r.Type {
			case scheduler.EventBudgetWarning80Pct:
				if warnSeq == 0 {
					warnSeq = r.Seq
				}
			case scheduler.EventBudgetBreakerTripped:
				if tripSeq == 0 {
					tripSeq, tripReason = r.Seq, r.Reason
				}
			}
		}
		if warnSeq == 0 || tripSeq == 0 || warnSeq >= tripSeq {
			t.Fatalf("aviso/trip no log: warn=%d trip=%d, quero warn < trip (ambos presentes)", warnSeq, tripSeq)
		}
		if tripReason != string(scheduler.ReasonExhaustion) {
			t.Fatalf("motivo do trip = %s, quero exhaustion", tripReason)
		}
	})

	// --- (b) VELOCIDADE: burst de consumo por janela >= limiar ⇒ trip por velocity. ---
	t.Run("velocity", func(t *testing.T) {
		clk := &mutClock{}
		clk.set(time.Unix(2_000_000, 0))
		const treeID = "run-vel"
		// Orçamento FOLGADO (não esgota): isola o eixo de VELOCIDADE.
		b := newTree(t, treeID, budget.Amount{Tokens: 1_000_000, CostMicroUSD: 1_000_000})
		es := newES(t)
		th := scheduler.Thresholds{
			VelocityTokens:   5000,            // atingir 5000 tokens/janela dispara (>=).
			Window:           time.Second,     // janela deslizante.
			ExhaustionMargin: budget.Amount{}, // esgotamento a 0 (não interfere: orçamento folgado).
		}
		br := newBreaker(t, es, b, treeID, th, scheduler.WithBreakerClock(clk.now))

		// Burst abaixo do limiar: sem trip (closed).
		if st, err := br.Observe(ctx, budget.Amount{Tokens: 3000}); err != nil || st != scheduler.BreakerClosed {
			t.Fatalf("burst sub-limiar: (%s,err=%v), quero closed", st, err)
		}
		// Segundo burst na MESMA janela empurra a velocidade acumulada >= limiar ⇒ TRIP.
		clk.advance(100 * time.Millisecond)                     // ainda dentro da janela de 1s.
		st, err := br.Observe(ctx, budget.Amount{Tokens: 2500}) // acumulado 5500 >= 5000
		if err != nil {
			t.Fatalf("Observe (burst): %v", err)
		}
		if st != scheduler.BreakerOpen {
			t.Fatalf("estado após burst = %s, quero open (trip por velocidade)", st)
		}
		if ok, _, _ := br.Allow(ctx); ok {
			t.Fatal("Allow devia NEGAR com o breaker open (fail-closed)")
		}
		// OBSERVÁVEL: o motivo do trip é VELOCIDADE.
		recs, err := br.Replay(ctx)
		if err != nil {
			t.Fatalf("Replay: %v", err)
		}
		var tripReason string
		for _, r := range recs {
			if r.Type == scheduler.EventBudgetBreakerTripped {
				tripReason = r.Reason
				break
			}
		}
		if tripReason != string(scheduler.ReasonVelocity) {
			t.Fatalf("motivo do trip = %s, quero velocity", tripReason)
		}
	})
}

// ---------------------------------------------------------------------------
// AC5 — NFRs COMO SINAIS: injecta NewRecordingMeter (WithAdmissionMeter/
// WithScaleMeter/WithWaitP95Meter) e REPORTA deny/defer-rate, degradation-level
// (gauge 0..4) e wait-p95 como MEDIÇÕES OBSERVADAS (asserção via rec.ByInstrument),
// emitindo uma LINHA MARCADA estável AOS_SCALE_REPORT (oversubscribed:false) —
// sinais, não só pass/fail binário.
// ---------------------------------------------------------------------------

func TestScale_NFRSignalsReported(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := time.Unix(4_000_000, 0)
	rec := scheduler.NewRecordingMeter()

	// (1) DEFER-RATE observado: carga agregada sobre uma Admission REAL instrumentada.
	es, err := admissionWithMeter(t, loadCapSpawns*loadCost, base, rec)
	if err != nil {
		t.Fatalf("admission: %v", err)
	}
	adm := es
	boards := newScaleBoards(t, adm, loadNBoards, base)
	res := runAggregateLoad(t, boards, loadSpawnsPerBoard, loadCost)

	// INVARIANTE de não-oversubscription (a base de oversubscribed:false do relatório).
	hr, _ := adm.Headroom(ctx, spawnKey, "")
	oversubscribed := hr.Tokens < 0 || res.admitted > loadCapSpawns

	admittedM := rec.ByInstrument(scheduler.MetricAdmitted)
	deferredM := rec.ByInstrument(scheduler.MetricDeferred)
	if len(admittedM) == 0 || len(deferredM) == 0 {
		t.Fatalf("métricas de admissão em falta: admitted=%d deferred=%d", len(admittedM), len(deferredM))
	}

	// (2) WAIT-P95 observado: recorder instrumentado com amostras deterministas.
	waitRec := scheduler.NewWaitP95Recorder(scheduler.WithWaitP95Meter(rec))
	waits := []time.Duration{10, 20, 30, 40, 50, 500} // ms; p95 nearest-rank determinista.
	for _, w := range waits {
		waitRec.Observe(ctx, w*time.Millisecond)
	}
	waitP95 := waitRec.P95()
	if len(rec.ByInstrument(scheduler.MetricDispatchWaitP95)) == 0 {
		t.Fatal("gauge de wait-p95 não emitido")
	}

	// (3) DEGRADATION-LEVEL observado: um HorizontalScaler.Tick sob headroom NULO conduz
	// a escada; o gauge do degrau corrente (0..4) é emitido. fakeHeadroom vem de scale_test.go.
	hz := &fakeHeadroom{snap: scheduler.HeadroomSnapshot{Tokens: 0, Requests: 0, LimitTokens: loadCapSpawns * loadCost, LimitRequests: 1_000_000}}
	queues, err := scheduler.NewPartitionedQueues(scheduler.DefaultQueueLimits(16), scheduler.WithQueueClock(fixed(base)))
	if err != nil {
		t.Fatalf("NewPartitionedQueues: %v", err)
	}
	enqueueN(t, queues, "acme", "P2", 20) // fila com pressão para a condição de saturação.
	scaler, err := scheduler.NewHorizontalScaler(spawnKey, hz, queues, waitRec, scaleCfg(),
		scheduler.WithScaleMeter(rec),
	)
	if err != nil {
		t.Fatalf("NewHorizontalScaler: %v", err)
	}
	dec, err := scaler.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if !dec.Degraded {
		t.Fatalf("sob headroom nulo o Tick devia degradar; dec=%+v", dec)
	}
	degLevelM := rec.ByInstrument(scheduler.MetricDegradationLevel)
	if len(degLevelM) == 0 {
		t.Fatal("gauge do degrau de degradação não emitido")
	}
	maxDegLevel := 0
	for _, m := range degLevelM {
		if int(m.Value) > maxDegLevel {
			maxDegLevel = int(m.Value)
		}
	}
	if maxDegLevel < 1 {
		t.Fatalf("degrau de degradação máximo = %d, quero >= 1 (a escada activou)", maxDegLevel)
	}

	// SINAIS (não só pass/fail): deny-rate, degrau máximo, wait-p95. Emite a LINHA
	// MARCADA estável AOS_SCALE_REPORT (molde do AOS_REPLAY_REPORT) — oversubscribed:false.
	denyRate := float64(res.deferred) / float64(res.admitted+res.deferred)
	report := fmt.Sprintf(
		`{"admitted":%d,"deferred":%d,"deny_rate":%.4f,"max_degradation_level":%d,"wait_p95_ms":%d,"oversubscribed":%t}`,
		res.admitted, res.deferred, denyRate, maxDegLevel, waitP95.Milliseconds(), oversubscribed,
	)
	t.Logf("AOS_SCALE_REPORT %s", report)

	if oversubscribed {
		t.Fatalf("oversubscribed=true (headroom=%d, admitidos=%d): o admission control NÃO conteve a carga agregada", hr.Tokens, res.admitted)
	}
}

// admissionWithMeter constrói uma Admission REAL com um RecordingMeter acoplado
// (WithAdmissionMeter) — para o defer/deny-rate ser MEDIDO, não inferido.
func admissionWithMeter(t *testing.T, tpm int64, base time.Time, rec *scheduler.RecordingMeter) (*scheduler.Admission, error) {
	t.Helper()
	es := newES(t)
	qp := scheduler.NewStaticQuotaProvider(scheduler.ProviderLimits{TPM: tpm, RPM: 1_000_000, Window: time.Minute})
	return scheduler.NewAdmission(es, qp,
		scheduler.WithClock(fixed(base)),
		scheduler.WithIDGen(seqIDGen()),
		scheduler.WithAdmissionMeter(rec),
	)
}

// ---------------------------------------------------------------------------
// META-TESTE NÃO-TAUTOLÓGICO (molde routing.sh TestMetaDetects): prova que a
// detecção de AC1 é REAL — com o admission control CONTORNADO (headroom infinito,
// i.e. sem tecto efectivo), a MESMA carga agregada OVERSUBSCREVE (a soma dos spawns
// excede o headroom real). Isto prova que AC1 depende MESMO do admission funcionar
// (não passa trivialmente).
// ---------------------------------------------------------------------------

func TestMetaDetects_AggregateCollapseWithoutAdmission(t *testing.T) {
	t.Parallel()
	base := time.Unix(5_000_000, 0)

	attempts := loadNBoards * loadSpawnsPerBoard

	// (a) COM admission (tecto real TPM=loadCapSpawns*loadCost): admitidos == tecto.
	admReal, _ := newAdmForSpawn(t, loadCapSpawns*loadCost, 1_000_000, base)
	boardsReal := newScaleBoards(t, admReal, loadNBoards, base)
	resReal := runAggregateLoad(t, boardsReal, loadSpawnsPerBoard, loadCost)
	if resReal.admitted != loadCapSpawns {
		t.Fatalf("COM admission: admitidos = %d, quero %d (o tecto real)", resReal.admitted, loadCapSpawns)
	}

	// (b) SEM enforcement efectivo (admission CONTORNADA por headroom ~infinito, muito
	// acima do tecto REAL loadCapSpawns): a MESMA carga OVERSUBSCREVE — TODOS os spawns
	// passam, muito além do que o rate limit partilhado real (loadCapSpawns) permitia.
	realCeiling := int64(loadCapSpawns * loadCost)
	admBypass, _ := newAdmForSpawn(t, 1_000_000_000, 1_000_000_000, base) // headroom ~infinito
	boardsBypass := newScaleBoards(t, admBypass, loadNBoards, base)
	resBypass := runAggregateLoad(t, boardsBypass, loadSpawnsPerBoard, loadCost)

	if resBypass.deferred != 0 {
		t.Fatalf("SEM tecto efectivo NÃO devia haver defer; deferred=%d", resBypass.deferred)
	}
	if resBypass.admitted != attempts {
		t.Fatalf("SEM tecto efectivo devia admitir TODA a carga (%d); admitidos=%d", attempts, resBypass.admitted)
	}
	// A prova de OVERSUBSCRIPTION: a carga admitida sem tecto excede o tecto REAL — o
	// débito agregado (admitidos*custo) ultrapassa o headroom real do rate limit.
	reservedBypass := int64(resBypass.admitted) * loadCost
	if reservedBypass <= realCeiling {
		t.Fatalf("carga sem tecto (%d tokens) não excede o tecto real (%d): parametrização não satura", reservedBypass, realCeiling)
	}
	// E o essencial (não-tautológico): SEM admission admite-se ESTRITAMENTE MAIS do que
	// COM admission. Se AC1 passasse trivialmente (sem depender do admission), estes dois
	// números seriam iguais — a diferença prova que o token-bucket é load-bearing.
	if resBypass.admitted <= resReal.admitted {
		t.Fatalf("meta-teste vácuo: sem admission admitiu %d <= com admission %d — o enforcement não faz diferença", resBypass.admitted, resReal.admitted)
	}
}
