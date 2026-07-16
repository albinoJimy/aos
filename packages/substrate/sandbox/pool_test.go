package sandbox

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newPool(t *testing.T, warmN int, opts ...PoolOption) *Pool {
	t.Helper()
	s := newSnap(t)
	p, err := NewPool(s, warmN, opts...)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

func TestNewPool_Validation(t *testing.T) {
	if _, err := NewPool(nil, 1); err != ErrNilSnapshot {
		t.Fatalf("esperava ErrNilSnapshot, obtive %v", err)
	}
	if _, err := NewPool(newSnap(t), -1); err != ErrInvalidPoolSize {
		t.Fatalf("esperava ErrInvalidPoolSize, obtive %v", err)
	}
}

// TestPool_PrewarmedThenReserve prova o caminho quente: o pool arranca com N VMs
// pré-aquecidas; uma reserva serve um warm hit com cold-start = handoff (≈0).
func TestPool_PrewarmedThenReserve(t *testing.T) {
	p := newPool(t, 3, WithSynchronousReplenish())
	if st := p.Stats(); st.Warm != 3 {
		t.Fatalf("esperava 3 VMs pre-aquecidas, obtive %d", st.Warm)
	}
	lease, err := p.Reserve(context.Background())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if lease.Outcome() != OutcomeWarmHit {
		t.Fatalf("esperava warm_hit, obtive %s", lease.Outcome())
	}
	if lease.ColdStart() != 0 {
		t.Fatalf("warm hit devia ter cold-start 0 (handoff default), obtive %v", lease.ColdStart())
	}
	if lease.Overlay().Dirty() {
		t.Fatal("VM entregue devia estar limpa")
	}
}

// TestPool_ColdStartP95UnderTarget é o teste de DESEMPENHO: mede o cold-start sobre
// uma carga MISTA e REALISTA e prova p95 < 125 ms e restore ∈ [5,30] ms. Para uma
// mistura DETERMINISTA (sem depender do timing de goroutines de reposição), dois
// pools da MESMA versão de imagem alimentam o MESMO recorder (mesma [PoolKey], logo
// os agregados fundem-se): um pool quente serve warm hits (cold-start ≈ handoff) e um
// pool a frio (warmN=0) serve expansões que pagam o restore no caminho crítico
// (cold-start = restore, 5–30 ms). Com expansões em maioria, o p95 cai na banda do
// restore — a prova mais exigente de que a CAUDA cumpre o alvo.
func TestPool_ColdStartP95UnderTarget(t *testing.T) {
	rec := NewColdStartRecorder()
	ctx := context.Background()

	// Pool QUENTE: reserva+liberta sempre encontra uma VM pré-aquecida (warm hits).
	warm := newPool(t, 8, WithSynchronousReplenish(), WithColdStartRecorder(rec))
	// Pool A FRIO: sem pré-aquecimento — toda a reserva expande (restore no caminho).
	cold := newPool(t, 0, WithPolicy(PolicyExpand), WithMaxSize(256), WithSynchronousReplenish(), WithColdStartRecorder(rec))

	const warmReserves, coldReserves = 150, 300
	var warmHits, expansions int
	for i := 0; i < warmReserves; i++ {
		l, err := warm.Reserve(ctx)
		if err != nil {
			t.Fatalf("warm.Reserve #%d: %v", i, err)
		}
		if l.Outcome() == OutcomeWarmHit {
			warmHits++
		}
		l.Release()
	}
	for i := 0; i < coldReserves; i++ {
		l, err := cold.Reserve(ctx)
		if err != nil {
			t.Fatalf("cold.Reserve #%d: %v", i, err)
		}
		if l.Outcome() == OutcomeExpanded {
			expansions++
		}
		if d := l.RestoreDuration(); d < MinRestore || d > MaxRestore {
			t.Fatalf("restore = %v fora de [%v,%v]", d, MinRestore, MaxRestore)
		}
		l.Release()
	}

	if warmHits == 0 || expansions == 0 {
		t.Fatalf("carga nao foi mista: warmHits=%d expansions=%d", warmHits, expansions)
	}
	agg, ok := rec.SnapshotAgg(warm.key()) // warm.key()==cold.key() (mesma versao+driver)
	if !ok {
		t.Fatal("sem agregado de SLI")
	}
	if agg.Samples != warmReserves+coldReserves {
		t.Fatalf("esperava %d amostras agregadas, obtive %d", warmReserves+coldReserves, agg.Samples)
	}
	if agg.P95 >= DefaultColdStartTarget {
		t.Fatalf("p95 %v nao cumpre o alvo < %v", agg.P95, DefaultColdStartTarget)
	}
	if agg.Max > MaxRestore {
		t.Fatalf("cold-start maximo %v acima do restore maximo %v", agg.Max, MaxRestore)
	}
	// Com 300 expansões (5–30 ms) e 150 warm hits (≈0), o p95 recai na banda do restore.
	if agg.P95 < MinRestore {
		t.Fatalf("p95 %v abaixo do restore minimo — a cauda deveria reflectir expansoes", agg.P95)
	}
	t.Logf("cold-start p95=%v max=%v sobre %d reservas (warm=%d expand=%d, alvo %v)",
		agg.P95, agg.Max, agg.Samples, warmHits, expansions, DefaultColdStartTarget)
}

// TestPool_ColdMissAllExpansions prova que MESMO quando toda a reserva paga um
// restore fresco (pool a frio, sem pré-aquecimento), o p95 fica < 125 ms.
func TestPool_ColdMissAllExpansions(t *testing.T) {
	rec := NewColdStartRecorder()
	p := newPool(t, 0, // zero pré-aquecidas: toda a reserva expande
		WithMaxSize(128),
		WithPolicy(PolicyExpand),
		WithSynchronousReplenish(),
		WithColdStartRecorder(rec),
	)
	ctx := context.Background()
	for i := 0; i < 200; i++ {
		lease, err := p.Reserve(ctx)
		if err != nil {
			t.Fatalf("Reserve #%d: %v", i, err)
		}
		if lease.Outcome() != OutcomeExpanded {
			t.Fatalf("esperava expanded, obtive %s", lease.Outcome())
		}
		lease.Release()
	}
	p95 := rec.P95For(p.key())
	if p95 < MinRestore || p95 >= DefaultColdStartTarget {
		t.Fatalf("p95 de expansoes %v deve estar em [%v,%v)", p95, MinRestore, DefaultColdStartTarget)
	}
	t.Logf("p95 (todas expansoes) = %v", p95)
}

// TestPool_IsolationNextExecCleanOfPrevious é o teste de ISOLAMENTO: a execução N
// escreve no seu overlay; após Release (descarte), a reserva seguinte recebe uma VM
// LIMPA — nunca observa artefactos da execução anterior.
func TestPool_IsolationNextExecCleanOfPrevious(t *testing.T) {
	// warmN=1, maxN=1, sem expansão: força a MESMA capacidade a ser reciclada por
	// restore fresco (a prova mais forte de que o overlay sujo é descartado).
	p := newPool(t, 1, WithPolicy(PolicyWait), WithWaitDeadline(2*time.Second), WithSynchronousReplenish())
	ctx := context.Background()

	// Execução N: escreve estado sujo.
	leaseN, err := p.Reserve(ctx)
	if err != nil {
		t.Fatalf("Reserve N: %v", err)
	}
	dirtyKey := "scratch/secret-of-N"
	if err := leaseN.Overlay().Write(dirtyKey, []byte("run-N-artifact")); err != nil {
		t.Fatalf("Write N: %v", err)
	}
	ovN := leaseN.Overlay()
	leaseN.Release() // descarta o overlay sujo e repõe (síncrono)

	// O overlay de N foi descartado — o estado sujo desapareceu.
	if !ovN.Discarded() {
		t.Fatal("overlay de N nao foi descartado no Release")
	}

	// Execução N+1: recebe uma VM restaurada de snapshot LIMPO.
	leaseN1, err := p.Reserve(ctx)
	if err != nil {
		t.Fatalf("Reserve N+1: %v", err)
	}
	if leaseN1.Overlay() == ovN {
		t.Fatal("N+1 recebeu o MESMO overlay de N (estado reciclado)")
	}
	if _, ok := leaseN1.Overlay().Read(dirtyKey); ok {
		t.Fatal("N+1 observou artefacto da execucao N (isolamento violado)")
	}
	if leaseN1.Overlay().Dirty() {
		t.Fatal("N+1 recebeu overlay sujo")
	}
	// O base imutável é o mesmo entre execuções.
	if leaseN1.Overlay().BaseDigest() != ovN.BaseDigest() {
		t.Fatal("base digest divergiu entre execucoes")
	}
	leaseN1.Release()
}

// TestPool_ExhaustionReject prova a política REJECT: esgotado, recusa fail-closed
// com ErrPoolExhausted (nunca serve estado sujo) e o esgotamento é observável.
func TestPool_ExhaustionReject(t *testing.T) {
	rec := NewColdStartRecorder()
	p := newPool(t, 1, WithPolicy(PolicyReject), WithColdStartRecorder(rec), WithSynchronousReplenish())
	ctx := context.Background()

	l1, err := p.Reserve(ctx)
	if err != nil {
		t.Fatalf("Reserve 1: %v", err)
	}
	// Segunda reserva com o pool esgotado: rejeição fail-closed.
	if _, err := p.Reserve(ctx); err != ErrPoolExhausted {
		t.Fatalf("esperava ErrPoolExhausted, obtive %v", err)
	}
	agg, _ := rec.SnapshotAgg(p.key())
	if agg.Exhaustions != 1 {
		t.Fatalf("esperava 1 esgotamento observado, obtive %d", agg.Exhaustions)
	}
	// Após libertar, a reposição devolve capacidade e a reserva volta a servir limpo.
	l1.Release()
	l2, err := p.Reserve(ctx)
	if err != nil {
		t.Fatalf("Reserve apos release: %v", err)
	}
	if l2.Overlay().Dirty() {
		t.Fatal("VM apos reposicao devia estar limpa")
	}
	l2.Release()
}

// TestPool_ExhaustionExpand prova a política EXPAND: esgotado, cria VMs novas até ao
// tecto; atingido o tecto, degrada para rejeição fail-closed.
func TestPool_ExhaustionExpand(t *testing.T) {
	p := newPool(t, 1, WithPolicy(PolicyExpand), WithMaxSize(3), WithSynchronousReplenish())
	ctx := context.Background()

	var leases []*Lease
	// 1 warm + 2 expansões = 3 (tecto).
	for i := 0; i < 3; i++ {
		l, err := p.Reserve(ctx)
		if err != nil {
			t.Fatalf("Reserve #%d: %v", i, err)
		}
		leases = append(leases, l)
	}
	// 4ª reserva excede o tecto: degrada para rejeição fail-closed.
	if _, err := p.Reserve(ctx); err != ErrPoolExhausted {
		t.Fatalf("esperava ErrPoolExhausted no tecto, obtive %v", err)
	}
	// Todas as VMs entregues estão limpas e são distintas.
	seen := map[*Overlay]bool{}
	for i, l := range leases {
		if l.Overlay().Dirty() {
			t.Fatalf("lease #%d sujo", i)
		}
		if seen[l.Overlay()] {
			t.Fatalf("lease #%d reutilizou um overlay", i)
		}
		seen[l.Overlay()] = true
	}
	for _, l := range leases {
		l.Release()
	}
}

// TestPool_ExhaustionWait prova a política WAIT: esgotado, bloqueia até uma VM limpa
// ser reposta; um Release desbloqueia. Sem reutilização de estado sujo.
func TestPool_ExhaustionWait(t *testing.T) {
	// Async replenish (default) para que o Release reponha e desbloqueie o waiter.
	p := newPool(t, 1, WithPolicy(PolicyWait), WithWaitDeadline(3*time.Second))
	ctx := context.Background()

	l1, err := p.Reserve(ctx)
	if err != nil {
		t.Fatalf("Reserve 1: %v", err)
	}
	_ = l1.Overlay().Write("scratch/dirt", []byte("N"))

	waited := make(chan *Lease, 1)
	go func() {
		l2, err := p.Reserve(ctx)
		if err != nil {
			t.Errorf("Reserve WAIT: %v", err)
			waited <- nil
			return
		}
		waited <- l2
	}()

	// Liberta: descarta o overlay sujo e repõe -> desbloqueia o waiter.
	time.Sleep(20 * time.Millisecond)
	l1.Release()

	select {
	case l2 := <-waited:
		if l2 == nil {
			t.Fatal("waiter falhou")
		}
		if l2.Outcome() != OutcomeWaited {
			t.Fatalf("esperava outcome waited, obtive %s", l2.Outcome())
		}
		if l2.Overlay().Dirty() {
			t.Fatal("waiter recebeu VM suja")
		}
		if _, ok := l2.Overlay().Read("scratch/dirt"); ok {
			t.Fatal("waiter observou estado sujo da execucao anterior")
		}
		l2.Release()
	case <-time.After(4 * time.Second):
		t.Fatal("WAIT nao desbloqueou apos Release")
	}
}

// TestPool_WaitDeadlineExpires prova que WAIT degrada para rejeição fail-closed
// quando o deadline expira sem reposição.
func TestPool_WaitDeadlineExpires(t *testing.T) {
	p := newPool(t, 1, WithPolicy(PolicyWait), WithWaitDeadline(30*time.Millisecond), WithSynchronousReplenish())
	ctx := context.Background()
	l1, err := p.Reserve(ctx)
	if err != nil {
		t.Fatalf("Reserve 1: %v", err)
	}
	defer l1.Release()
	// Sem libertar l1, o WAIT expira o deadline.
	start := time.Now()
	if _, err := p.Reserve(ctx); err != ErrPoolExhausted {
		t.Fatalf("esperava ErrPoolExhausted por deadline, obtive %v", err)
	}
	if elapsed := time.Since(start); elapsed < 25*time.Millisecond {
		t.Fatalf("WAIT retornou cedo demais (%v) — nao respeitou o deadline", elapsed)
	}
}

// TestPool_WaitContextCancel prova que o WAIT respeita o cancelamento do contexto.
func TestPool_WaitContextCancel(t *testing.T) {
	p := newPool(t, 1, WithPolicy(PolicyWait), WithSynchronousReplenish())
	l1, err := p.Reserve(context.Background())
	if err != nil {
		t.Fatalf("Reserve 1: %v", err)
	}
	defer l1.Release()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, err := p.Reserve(ctx); err != context.Canceled {
		t.Fatalf("esperava context.Canceled, obtive %v", err)
	}
}

// TestPool_AtomicReservationNoCollision é o teste de RESERVA ATÓMICA: N atribuições
// concorrentes recebem N VMs DISTINTAS, sem colisão no contador (-race). Nenhuma VM
// é entregue a dois consumidores.
func TestPool_AtomicReservationNoCollision(t *testing.T) {
	const n = 50
	p := newPool(t, n, WithPolicy(PolicyReject))
	ctx := context.Background()

	var mu sync.Mutex
	seen := map[*Overlay]int{}
	var wg sync.WaitGroup
	var okCount atomic.Int64

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := p.Reserve(ctx)
			if err != nil {
				t.Errorf("Reserve concorrente: %v", err)
				return
			}
			okCount.Add(1)
			mu.Lock()
			seen[l.Overlay()]++
			mu.Unlock()
		}()
	}
	wg.Wait()

	if okCount.Load() != n {
		t.Fatalf("esperava %d reservas com sucesso, obtive %d", n, okCount.Load())
	}
	if len(seen) != n {
		t.Fatalf("esperava %d overlays distintos, obtive %d (colisao no contador)", n, len(seen))
	}
	for ov, c := range seen {
		if c != 1 {
			t.Fatalf("overlay %s entregue %d vezes (deviam ser 1)", ov.ID(), c)
		}
	}
}

// TestPool_WarmReplenishmentUpToN prova que após consumo o pool repõe até N VMs
// pré-aquecidas (warm replenishment).
func TestPool_WarmReplenishmentUpToN(t *testing.T) {
	const n = 5
	p := newPool(t, n, WithSynchronousReplenish())
	ctx := context.Background()

	// Consome todas e liberta: a reposição devolve o pool a N.
	var leases []*Lease
	for i := 0; i < n; i++ {
		l, err := p.Reserve(ctx)
		if err != nil {
			t.Fatalf("Reserve #%d: %v", i, err)
		}
		leases = append(leases, l)
	}
	// Após consumir tudo com reposição síncrona, warm foi reposto a N (as reservas
	// consomem e repõem enquanto há capacidade em uso? maxN==N, logo repõe até
	// maxN-inUse). Liberta tudo e verifica a reposição final.
	for _, l := range leases {
		l.Release()
	}
	p.waitReplenish()
	st := p.Stats()
	if st.Warm != n {
		t.Fatalf("esperava reposicao a %d VMs pre-aquecidas, obtive %d (inUse=%d)", n, st.Warm, st.InUse)
	}
	if st.InUse != 0 {
		t.Fatalf("esperava 0 VMs em uso apos libertar tudo, obtive %d", st.InUse)
	}
}

// TestPool_ReplenishRespectsCeiling prova que a reposição nunca excede o tecto maxN
// de VMs vivas.
func TestPool_ReplenishRespectsCeiling(t *testing.T) {
	const n = 3
	p := newPool(t, n, WithMaxSize(n), WithSynchronousReplenish())
	ctx := context.Background()
	// Reserva 2 (ficam em uso): warm+inUse <= maxN sempre.
	l1, _ := p.Reserve(ctx)
	l2, _ := p.Reserve(ctx)
	p.waitReplenish()
	st := p.Stats()
	if st.Warm+st.InUse > n {
		t.Fatalf("VMs vivas %d excedem o tecto %d", st.Warm+st.InUse, n)
	}
	if st.InUse != 2 {
		t.Fatalf("esperava 2 em uso, obtive %d", st.InUse)
	}
	l1.Release()
	l2.Release()
}

// TestPool_ReleaseIdempotent prova que libertar duas vezes é seguro (contabilidade
// não fica negativa).
func TestPool_ReleaseIdempotent(t *testing.T) {
	p := newPool(t, 2, WithSynchronousReplenish())
	l, err := p.Reserve(context.Background())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	l.Release()
	l.Release() // idempotente
	p.waitReplenish()
	if st := p.Stats(); st.InUse != 0 {
		t.Fatalf("esperava 0 em uso, obtive %d", st.InUse)
	}
}

// TestPool_ReserveOnClosed prova que reservar de um pool fechado devolve ErrPoolClosed.
func TestPool_ReserveOnClosed(t *testing.T) {
	p := newPool(t, 0, WithPolicy(PolicyReject))
	p.Close()
	if _, err := p.Reserve(context.Background()); err != ErrPoolClosed {
		t.Fatalf("esperava ErrPoolClosed, obtive %v", err)
	}
	p.Close() // idempotente
}

// TestPool_ReserveContextAlreadyCancelled prova o fail-fast se o ctx já expirou.
func TestPool_ReserveContextAlreadyCancelled(t *testing.T) {
	p := newPool(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Reserve(ctx); err != context.Canceled {
		t.Fatalf("esperava context.Canceled, obtive %v", err)
	}
}

// TestPool_WaitDefaultDeadlineBounded prova que a política WAIT tem SEMPRE um
// deadline (DoD): sem [WithWaitDeadline] explícito, herda [DefaultWaitDeadline]; um
// deadline explícito de 0 também é elevado ao default (a espera nunca é indefinida à
// mercê apenas do cancelamento do ctx). Políticas não-WAIT não ganham deadline.
func TestPool_WaitDefaultDeadlineBounded(t *testing.T) {
	// WAIT sem deadline explícito -> default não-nulo.
	p := newPool(t, 1, WithPolicy(PolicyWait), WithSynchronousReplenish())
	if p.deadline != DefaultWaitDeadline {
		t.Fatalf("WAIT sem deadline devia herdar %v, obtive %v", DefaultWaitDeadline, p.deadline)
	}
	// WAIT com deadline explícito 0 -> também elevado ao default (nunca indefinido).
	p0 := newPool(t, 1, WithPolicy(PolicyWait), WithWaitDeadline(0), WithSynchronousReplenish())
	if p0.deadline != DefaultWaitDeadline {
		t.Fatalf("WAIT com deadline 0 devia ser elevado a %v, obtive %v", DefaultWaitDeadline, p0.deadline)
	}
	// WAIT com deadline explícito positivo -> respeitado.
	pe := newPool(t, 1, WithPolicy(PolicyWait), WithWaitDeadline(50*time.Millisecond), WithSynchronousReplenish())
	if pe.deadline != 50*time.Millisecond {
		t.Fatalf("WAIT devia respeitar deadline explícito, obtive %v", pe.deadline)
	}
	// Política não-WAIT não ganha deadline.
	pr := newPool(t, 1, WithPolicy(PolicyReject), WithSynchronousReplenish())
	if pr.deadline != 0 {
		t.Fatalf("politica nao-WAIT nao devia ganhar deadline, obtive %v", pr.deadline)
	}
}

// TestPool_CloseRaceWithActiveRelease prova que fechar o pool ENQUANTO um lease ainda
// vivo é libertado (Release -> triggerReplenish -> wg.Add) não dispara o panic
// "sync: WaitGroup misuse: Add called concurrently with Wait". Exercita a janela sob
// -race: leases activos libertados em paralelo com Close.
func TestPool_CloseRaceWithActiveRelease(t *testing.T) {
	s := newSnap(t)
	p, err := NewPool(s, 4, WithPolicy(PolicyExpand), WithMaxSize(16)) // reposição ASSÍNCRONA
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	var leases []*Lease
	for i := 0; i < 4; i++ {
		l, err := p.Reserve(context.Background())
		if err != nil {
			t.Fatalf("Reserve #%d: %v", i, err)
		}
		leases = append(leases, l)
	}
	var wg sync.WaitGroup
	// Liberta os leases ainda activos em paralelo com o Close.
	for _, l := range leases {
		wg.Add(1)
		go func(l *Lease) { defer wg.Done(); l.Release() }(l)
	}
	wg.Add(1)
	go func() { defer wg.Done(); p.Close() }()
	wg.Wait()
	p.Close() // idempotente
}

// TestPool_WarmDepletionObservable prova a HONESTIDADE do SLI (finding metric-honesty):
// sob carga 100% warm, o p95 de cold_start é ≈0 (warm hits, handoff default 0), MAS o
// custo de restore não desapareceu — foi deslocado para a reposição e é observável via
// [MetricWarmReplenish] (+ [MetricRestore] scope "replenish") e o contador WarmRestores.
func TestPool_WarmDepletionObservable(t *testing.T) {
	sink := &MemoryColdStartMetricSink{}
	rec := NewColdStartRecorder(WithColdStartMetricSink(sink))
	p := newPool(t, 4, WithColdStartRecorder(rec), WithSynchronousReplenish())
	ctx := context.Background()

	const cycles = 20
	for i := 0; i < cycles; i++ {
		l, err := p.Reserve(ctx)
		if err != nil {
			t.Fatalf("Reserve #%d: %v", i, err)
		}
		if l.Outcome() != OutcomeWarmHit {
			t.Fatalf("esperava warm_hit, obtive %s", l.Outcome())
		}
		l.Release()
	}
	agg, ok := rec.SnapshotAgg(p.key())
	if !ok {
		t.Fatal("sem agregado")
	}
	// Carga 100% warm: cold_start p95 é 0 (o custo NÃO está no caminho crítico)...
	if agg.P95 != 0 {
		t.Fatalf("esperava p95 de cold_start 0 sob carga warm, obtive %v", agg.P95)
	}
	// ...mas o restore real foi pago off-path e ESTÁ observável (não "sem custo").
	if agg.WarmRestores == 0 {
		t.Fatal("esperava WarmRestores > 0 — o custo de restore deveria ser observavel off-path")
	}
	var sawReplenish, sawRestoreReplenish bool
	for _, m := range sink.Metrics() {
		if m.Name == MetricWarmReplenish && m.Attributes[AttrColdStartScope] == "replenish" {
			sawReplenish = true
		}
		if m.Name == MetricRestore && m.Attributes[AttrColdStartScope] == "replenish" {
			sawRestoreReplenish = true
		}
	}
	if !sawReplenish {
		t.Fatal("esperava metrica MetricWarmReplenish (depleção do warm pool)")
	}
	if !sawRestoreReplenish {
		t.Fatal("esperava metrica de restore com scope replenish (custo off-path visivel)")
	}
}

// TestPool_WarmRestoreNoSecretInMetrics confirma que a métrica de depleção do warm
// pool só transporta eixos NÃO-SECRETOS (versão/driver/scope) — nunca um segredo.
func TestPool_WarmRestoreNoSecretInMetrics(t *testing.T) {
	sink := &MemoryColdStartMetricSink{}
	rec := NewColdStartRecorder(WithColdStartMetricSink(sink))
	p := newPool(t, 2, WithColdStartRecorder(rec), WithSynchronousReplenish())
	l, err := p.Reserve(context.Background())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	l.Release()
	allowed := map[string]bool{AttrImageVersion: true, AttrDriver: true, AttrColdStartScope: true, AttrProvisionOutcome: true}
	for _, m := range sink.Metrics() {
		if m.Name != MetricWarmReplenish && m.Name != MetricRestore {
			continue
		}
		for k := range m.Attributes {
			if !allowed[k] {
				t.Fatalf("atributo inesperado (possivel fuga) em %q: %q", m.Name, k)
			}
		}
	}
}

// TestPool_ReserveAnnotatesProvisionSpan prova que, com um tracer ligado
// ([WithPoolTracer]), o caminho de reserva abre um span de PROVISÃO REAL
// ([OpProvisionSandbox]) e o SLI de cold-start anota-lhe cold_start_ms/p95_ms — o
// "custo por span" do DoD deixa de ser um caminho morto no fluxo composto. O span não
// transporta segredo.
func TestPool_ReserveAnnotatesProvisionSpan(t *testing.T) {
	tr := &recordingTracer{}
	rec := NewColdStartRecorder()
	p := newPool(t, 1, WithColdStartRecorder(rec), WithPoolTracer(tr), WithSynchronousReplenish())
	l, err := p.Reserve(context.Background())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	defer l.Release()

	if _, ok := tr.attr(AttrColdStartMs); !ok {
		t.Fatal("esperava cold_start_ms anotado no span de provisao")
	}
	if _, ok := tr.attr(AttrColdStartP95Ms); !ok {
		t.Fatal("esperava cold_start_p95_ms anotado no span de provisao")
	}
	op, ok := tr.attr(AttrOperationName)
	if !ok || op != OpProvisionSandbox {
		t.Fatalf("esperava span de operacao %q, obtive %v", OpProvisionSandbox, op)
	}
	if _, ok := tr.attr(AttrProvisionOutcome); !ok {
		t.Fatal("esperava provision_outcome no span")
	}
	// Nenhum atributo do span deve conter o handle de credencial nem um segredo.
	for _, v := range tr.attrValues() {
		if s, ok := v.(string); ok && (s == "tok-test" || s == "cap:test.exec") {
			t.Fatalf("possivel fuga de segredo no span: %q", s)
		}
	}
}

// TestPool_ConcurrentReserveReleaseRace martela reserve/release em paralelo (-race)
// para provar a segurança concorrente da reserva atómica + reposição assíncrona.
func TestPool_ConcurrentReserveReleaseRace(t *testing.T) {
	p := newPool(t, 8, WithPolicy(PolicyExpand), WithMaxSize(64))
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				l, err := p.Reserve(ctx)
				if err != nil {
					continue
				}
				_ = l.Overlay().Write("scratch/x", []byte("y"))
				l.Release()
			}
		}()
	}
	wg.Wait()
	p.waitReplenish()
	// Após a poeira assentar, não há VMs em uso e o warm não excede N.
	st := p.Stats()
	if st.InUse != 0 {
		t.Fatalf("esperava 0 em uso apos quiescencia, obtive %d", st.InUse)
	}
	if st.Warm > st.WarmN {
		t.Fatalf("warm %d excede N %d apos quiescencia", st.Warm, st.WarmN)
	}
}
