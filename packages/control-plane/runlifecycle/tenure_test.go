package runlifecycle_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/runlifecycle"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/substrate/eventstore"
)

// tenure_test.go — A PROVA DA POSSE (AOS-281, ADR-023 §2.1/§2.4/§2.5).
//
// Os testes deste ficheiro exercem os quatro requisitos que o ticket enumera:
// disputa do mesmo run, escrita com token obsoleto recusada, handoff sem janela de
// dupla-posse, e tomada de posse a meio que re-hidrata (este último em graph_test.go).
//
// «Dois processos» é modelado como DUAS INSTÂNCIAS INDEPENDENTES de LeaseManager e
// Tenure sobre o MESMO Event Store — que é exactamente a topologia real: os processos
// não partilham memória, partilham o log replicado (AOS-100). Nenhum teste passa
// estado de um lado para o outro por outra via que não o Event Store, e é essa
// disciplina que torna o teste representativo em vez de decorativo. A prova com dois
// binários REAIS está em `packages/cmd/aos-orq` (ver o seu README e o teste de
// aceitação de dois processos).

const testTTL = 30 * time.Second

// clock é um relógio manual: torna a expiração determinística sem sleeps frágeis.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Unix(1_700_000_000, 0).UTC()} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// replica constrói a autoridade de lease de UM processo. Instâncias distintas com o
// mesmo store são réplicas distintas sobre o mesmo log.
func replica(t *testing.T, store *eventstore.Store, clk durable.Clock, workerID string) *durable.LeaseManager {
	t.Helper()
	lm, err := durable.NewLeaseManager(store, testTTL,
		durable.WithLeaseClock(clk),
		durable.WithWorkerID(workerID),
	)
	if err != nil {
		t.Fatalf("LeaseManager(%s): %v", workerID, err)
	}
	return lm
}

func newStore(t *testing.T) *eventstore.Store {
	t.Helper()
	s, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	return s
}

// lifecycleEvent é um facto de ciclo de vida genérico, para exercer a via de escrita
// sem depender do formato de nenhum emissor concreto.
func lifecycleEvent(runID, stepID string) eventstore.EventInput {
	return eventstore.EventInput{
		Type:    "task.node.state_changed",
		Payload: []byte(`{"probe":true}`),
		RunID:   runID,
		StepID:  stepID,
	}
}

// ---------------------------------------------------------------------------
// TESTE 1 — DOIS PROCESSOS DISPUTAM O MESMO RUN: só um ganha o claim.
//
// Falha-antes: sem arbitragem por lease, as duas réplicas teriam ambas uma via de
// escrita e o run teria dois donos — a segunda fonte de verdade que o ADR-007 proíbe.
// ---------------------------------------------------------------------------

func TestPosse_DoisProcessosDisputam_SoUmGanha(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	clk := newClock()
	const runID = "run-disputa"

	a := replica(t, store, clk, "proc-a")
	b := replica(t, store, clk, "proc-b")

	ta, errA := runlifecycle.Claim(ctx, store, a, runID)
	tb, errB := runlifecycle.Claim(ctx, store, b, runID)

	// Exactamente um vencedor. Qual deles é indiferente — a propriedade é a unicidade.
	won := 0
	if errA == nil {
		won++
	}
	if errB == nil {
		won++
	}
	if won != 1 {
		t.Fatalf("vencedores = %d, quer 1 (errA=%v, errB=%v) — a posse não está a ser arbitrada", won, errA, errB)
	}

	// O perdedor vê um lease VIVO detido por outro, não um erro genérico.
	loserErr := errA
	if errA == nil {
		loserErr = errB
	}
	if !errors.Is(loserErr, durable.ErrLeaseHeld) {
		t.Fatalf("erro do perdedor = %v, quer durable.ErrLeaseHeld — um lease vivo não se rouba", loserErr)
	}

	winner := ta
	if ta == nil {
		winner = tb
	}
	if got := winner.Token(); got != 1 {
		t.Fatalf("token do vencedor = %d, quer 1 (primeiro claim do run)", got)
	}
}

// ---------------------------------------------------------------------------
// TESTE 2 — DISPUTA CONCORRENTE REAL: N goroutines a reclamar em paralelo.
//
// A eleição do vencedor é do `expected_seq` do stream de lease, não deste pacote.
// Este teste existe para provar que a propriedade sobrevive à concorrência real e não
// só à sequência do TESTE 1 — e corre sob `-race`.
// ---------------------------------------------------------------------------

func TestPosse_DisputaConcorrente_UmSoVencedor(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	clk := newClock()
	const runID = "run-corrida"
	const replicas = 8

	var wg sync.WaitGroup
	var mu sync.Mutex
	var winners []uint64
	var held int

	for i := 0; i < replicas; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			lm := replica(t, store, clk, "proc")
			ten, err := runlifecycle.Claim(ctx, store, lm, runID)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners = append(winners, ten.Token())
			case errors.Is(err, durable.ErrLeaseHeld):
				held++
			default:
				t.Errorf("réplica %d: erro inesperado %v (nem vitória nem lease-detido)", n, err)
			}
		}(i)
	}
	wg.Wait()

	if len(winners) != 1 {
		t.Fatalf("vencedores = %d, quer exactamente 1 — a arbitragem por expected_seq falhou", len(winners))
	}
	if held != replicas-1 {
		t.Fatalf("perdedores com ErrLeaseHeld = %d, quer %d", held, replicas-1)
	}
}

// ---------------------------------------------------------------------------
// TESTE 3 — ESCRITA COM TOKEN OBSOLETO É RECUSADA, NÃO APLICADA (AC1).
//
// O detentor A perde a posse por EXPIRAÇÃO (deixou de renovar), B reclama e minta um
// token maior. A escrita de A tem de ser recusada E não pode aparecer no log.
//
// Falha-antes: sem o FencedAppender no caminho, a escrita de A chegaria ao Event
// Store — que a aceitaria (a dedup só apanha a MESMA chave, e esta é outra) — e o run
// teria factos de dois donos.
// ---------------------------------------------------------------------------

func TestEscrita_TokenObsoleto_Recusada(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	clk := newClock()
	const runID = "run-obsoleto"

	a := replica(t, store, clk, "proc-a")
	ta, err := runlifecycle.Claim(ctx, store, a, runID)
	if err != nil {
		t.Fatalf("claim de A: %v", err)
	}

	// A escreve enquanto é dono: passa.
	if _, err := ta.Append(ctx, lifecycleEvent(runID, "probe-a-antes")); err != nil {
		t.Fatalf("escrita do dono legítimo recusada: %v", err)
	}

	// A morre (deixa de renovar). O TTL esgota-se e B reclama, mintando token 2.
	clk.advance(testTTL + time.Second)
	b := replica(t, store, clk, "proc-b")
	tb, err := runlifecycle.Claim(ctx, store, b, runID)
	if err != nil {
		t.Fatalf("claim de B após expiração: %v", err)
	}
	if tb.Token() <= ta.Token() {
		t.Fatalf("token de B = %d, tem de ser ESTRITAMENTE maior que o de A = %d", tb.Token(), ta.Token())
	}

	// A ressuscita e tenta escrever com o token velho: RECUSADO.
	before := streamLen(t, store, runID)
	_, err = ta.Append(ctx, lifecycleEvent(runID, "probe-a-zombie"))
	if !errors.Is(err, durable.ErrStaleFencingToken) {
		t.Fatalf("escrita do detentor superado = %v, quer durable.ErrStaleFencingToken", err)
	}
	if after := streamLen(t, store, runID); after != before {
		t.Fatalf("o stream cresceu de %d para %d — a escrita obsoleta CHEGOU ao log; recusar tem de significar não aplicar", before, after)
	}

	// E o novo dono escreve sem problema.
	if _, err := tb.Append(ctx, lifecycleEvent(runID, "probe-b")); err != nil {
		t.Fatalf("escrita do novo dono recusada: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TESTE 4 — HANDOFF POR ANÚNCIO, SEM JANELA DE DUPLA-POSSE (AC2, ADR-023 §2.5).
//
// A largar (`lease.released`) e B reclamar. As três propriedades que fazem disto um
// handoff e não uma corrida:
//
//	(1) B consegue reclamar IMEDIATAMENTE — não espera o TTL;
//	(2) A é recusado a partir do instante em que larga — ANTES sequer de B reclamar;
//	(3) o token de B é estritamente maior.
//
// A propriedade (2) é a que fecha a janela, e é a que este teste isola: mede-se a
// recusa de A no intervalo em que ainda NÃO há novo detentor.
// ---------------------------------------------------------------------------

func TestHandoff_PorAnuncio_SemJanelaDeDuplaPosse(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	clk := newClock()
	const runID = "run-handoff"

	a := replica(t, store, clk, "proc-a")
	ta, err := runlifecycle.Claim(ctx, store, a, runID)
	if err != nil {
		t.Fatalf("claim de A: %v", err)
	}
	if _, err := ta.Append(ctx, lifecycleEvent(runID, "antes-do-gate")); err != nil {
		t.Fatalf("escrita de A antes de largar: %v", err)
	}

	// NÃO-VACUIDADE do sensor durável: ANTES do release, o mesmo token passa. Sem esta
	// linha, um `fencedOutViaLog` que recusasse tudo (ou nada) daria um verde que não
	// prova nada — seria a barreira a existir por acidente, não por causa do release.
	if fencedOutViaLog(ctx, t, store, a, runID, ta.Token()) {
		t.Fatal("o token do dono LEGÍTIMO já era recusado antes de largar — o sensor do teste está avariado")
	}

	// A ANUNCIA que largou. Nenhum tempo passa — o handoff não depende do TTL.
	if err := ta.Release(ctx); err != nil {
		t.Fatalf("release de A: %v", err)
	}

	// (2) NO INTERVALO — ninguém reclamou ainda — A já não escreve.
	before := streamLen(t, store, runID)
	if _, err := ta.Append(ctx, lifecycleEvent(runID, "depois-de-largar")); err == nil {
		t.Fatal("A escreveu DEPOIS de largar e ANTES de haver novo dono — é exactamente a janela de dupla-posse que o handoff tem de não ter")
	}
	if after := streamLen(t, store, runID); after != before {
		t.Fatalf("o stream cresceu de %d para %d após o release de A", before, after)
	}

	// Uma réplica NOVA (sem memória da posse de A) tenta escrever com o token de A:
	// tem de ser recusada pelo LOG, não pela marca local de A. É esta a prova de que a
	// barreira é durável e não um flag em memória deste processo.
	if !fencedOutViaLog(ctx, t, store, a, runID, ta.Token()) {
		t.Fatal("uma escrita com o token largado passou pelo enforcement durável — a barreira é local, não durável")
	}

	// (1) B reclama já, sem esperar TTL nenhum.
	b := replica(t, store, clk, "proc-b")
	tb, err := runlifecycle.Claim(ctx, store, b, runID)
	if err != nil {
		t.Fatalf("claim de B logo após o release de A (não devia esperar TTL): %v", err)
	}
	// (3)
	if tb.Token() <= ta.Token() {
		t.Fatalf("token de B = %d, tem de ser estritamente maior que o de A = %d", tb.Token(), ta.Token())
	}
	if _, err := tb.Append(ctx, lifecycleEvent(runID, "depois-do-gate")); err != nil {
		t.Fatalf("escrita de B após o handoff: %v", err)
	}
}

// fencedOutViaLog verifica que uma escrita com `token` é recusada pelo enforcement
// DURÁVEL — construindo um appender fresco sobre a autoridade de lease, sem passar
// pela marca local de nenhuma [runlifecycle.Tenure].
func fencedOutViaLog(ctx context.Context, t *testing.T, store *eventstore.Store, lm *durable.LeaseManager, runID string, token uint64) bool {
	t.Helper()
	fa, err := durable.NewFencedAppender(store, lm)
	if err != nil {
		t.Fatalf("NewFencedAppender: %v", err)
	}
	_, err = fa.Append(ctx, runID, durable.FencingToken(token), lifecycleEvent(runID, "probe-durable-fence"))
	return errors.Is(err, durable.ErrStaleFencingToken)
}

// ---------------------------------------------------------------------------
// TESTE 5 — A POSSE LARGADA FECHA A VIA DE ESCRITA LOCALMENTE (ErrNotHeld).
//
// Camada (1) do ADR-023 §2.4: a recusa local, antes de ir ao log. Distinta da
// durável — e as duas coexistem de propósito.
// ---------------------------------------------------------------------------

func TestPosse_LargadaFechaViaDeEscrita(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	clk := newClock()
	const runID = "run-largado"

	lm := replica(t, store, clk, "proc-a")
	ten, err := runlifecycle.Claim(ctx, store, lm, runID)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !ten.Held() {
		t.Fatal("posse acabada de reclamar reporta-se como não-detida")
	}
	if err := ten.Release(ctx); err != nil {
		t.Fatalf("release: %v", err)
	}
	if ten.Held() {
		t.Fatal("posse largada continua a reportar-se como detida")
	}
	if _, err := ten.Append(ctx, lifecycleEvent(runID, "x")); !errors.Is(err, runlifecycle.ErrNotHeld) {
		t.Fatalf("escrita após largar = %v, quer runlifecycle.ErrNotHeld", err)
	}
	if _, err := ten.Graph(ctx, eventstore.Producer{}); !errors.Is(err, runlifecycle.ErrNotHeld) {
		t.Fatalf("Graph após largar = %v, quer runlifecycle.ErrNotHeld", err)
	}
	// Release é idempotente: largar duas vezes não é erro nem escreve um segundo facto.
	if err := ten.Release(ctx); err != nil {
		t.Fatalf("segundo release: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TESTE 6 — O HEARTBEAT DE UM DETENTOR SUPERADO FECHA A POSSE.
//
// Cancelamento cooperativo (ADR-018 §2.3, §5-bis(2)): quem perde a partição PÁRA, não
// insiste. Sem isto o detentor superado continuaria a tentar escrever a cada passo e
// a bater no fencing — barulho em vez de paragem.
// ---------------------------------------------------------------------------

func TestHeartbeat_SuperadoFechaPosse(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	clk := newClock()
	const runID = "run-superado"

	a := replica(t, store, clk, "proc-a")
	ta, err := runlifecycle.Claim(ctx, store, a, runID)
	if err != nil {
		t.Fatalf("claim de A: %v", err)
	}
	// Heartbeat de um dono legítimo renova sem mintar token novo.
	if err := ta.Heartbeat(ctx); err != nil {
		t.Fatalf("heartbeat do dono legítimo: %v", err)
	}
	if got := ta.Token(); got != 1 {
		t.Fatalf("token após heartbeat = %d, quer 1 — um heartbeat NÃO minta token", got)
	}

	// A adormece, o TTL esgota-se, B reclama.
	clk.advance(testTTL + time.Second)
	b := replica(t, store, clk, "proc-b")
	if _, err := runlifecycle.Claim(ctx, store, b, runID); err != nil {
		t.Fatalf("claim de B: %v", err)
	}

	// O heartbeat de A descobre a supersessão e FECHA a posse local.
	err = ta.Heartbeat(ctx)
	if !errors.Is(err, durable.ErrLeaseSuperseded) && !errors.Is(err, durable.ErrLeaseExpired) {
		t.Fatalf("heartbeat do superado = %v, quer ErrLeaseSuperseded ou ErrLeaseExpired", err)
	}
	if ta.Held() {
		t.Fatal("a posse de A continua aberta depois de o heartbeat descobrir a supersessão — o cancelamento cooperativo não aconteceu")
	}
}

// streamLen devolve o número de eventos de um stream (0 se não existir).
func streamLen(t *testing.T, store *eventstore.Store, streamID string) int {
	t.Helper()
	evs, err := store.Read(context.Background(), streamID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return 0
		}
		t.Fatalf("Read(%s): %v", streamID, err)
	}
	return len(evs)
}
