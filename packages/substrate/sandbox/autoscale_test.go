package sandbox

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeHeadroom é uma [HeadroomSource] determinista de referência (o adaptador real do
// escalonador vive no ápice — ver o cabeçalho de autoscale.go). Concorrente-segura.
type fakeHeadroom struct {
	mu  sync.Mutex
	h   Headroom
	err error
}

func (f *fakeHeadroom) set(h Headroom) {
	f.mu.Lock()
	f.h, f.err = h, nil
	f.mu.Unlock()
}

func (f *fakeHeadroom) fail(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *fakeHeadroom) Headroom(context.Context) (Headroom, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.h, f.err
}

// --- DefaultPoolSizer: propriedades da fórmula (análogas a deriveMaxSpawn) ---

func TestDefaultPoolSizer_Properties(t *testing.T) {
	const absMax = 8
	sizer := DefaultPoolSizer(10 /*custo por VM*/, absMax, 1.0)

	// ZERO sob headroom nulo: pool totalmente fail-closed.
	if sz := sizer(Headroom{Available: 0}); sz.Warm != 0 || sz.Max != 0 {
		t.Fatalf("headroom 0 devia dar tamanho 0/0, obtive %+v", sz)
	}
	// NÃO é constante: varia com o headroom.
	low := sizer(Headroom{Available: 10})
	high := sizer(Headroom{Available: 50})
	if low == high {
		t.Fatalf("sizer nao devia ser constante: low=%+v high=%+v", low, high)
	}
	// slots = available/custo: 50/10 = 5.
	if high.Max != 5 || high.Warm != 5 {
		t.Fatalf("available=50 custo=10 devia dar 5/5, obtive %+v", high)
	}
	// Tecto ABSOLUTO domina: available enorme fixa-se a absMax.
	if sz := sizer(Headroom{Available: 1_000_000}); sz.Max != absMax || sz.Warm != absMax {
		t.Fatalf("available enorme devia fixar-se a absMax=%d, obtive %+v", absMax, sz)
	}

	// MONÓTONA não-decrescente no headroom.
	prev := PoolSize{}
	for a := int64(0); a <= 200; a += 3 {
		sz := sizer(Headroom{Available: a})
		if sz.Max < prev.Max || sz.Warm < prev.Warm {
			t.Fatalf("nao-monotona em available=%d: %+v < %+v", a, sz, prev)
		}
		if sz.Warm > sz.Max {
			t.Fatalf("warm>max em available=%d: %+v", a, sz)
		}
		prev = sz
	}
}

func TestDefaultPoolSizer_Normalizations(t *testing.T) {
	// custo <= 0 normaliza para 1 (nunca divisão por zero / pool ilimitado).
	sizer := DefaultPoolSizer(0, 100, 1.0)
	if sz := sizer(Headroom{Available: 7}); sz.Max != 7 {
		t.Fatalf("custo<=0 devia normalizar para 1 (7/1=7), obtive %+v", sz)
	}
	// warmFraction fora de (0,1] cai para 1.0.
	full := DefaultPoolSizer(1, 100, 5.0) // 5.0 -> 1.0
	if sz := full(Headroom{Available: 4}); sz.Warm != 4 {
		t.Fatalf("warmFraction invalida devia cair para 1.0 (warm=max=4), obtive %+v", sz)
	}
	// warmFraction parcial: pré-aquece só uma fracção, mas warm<=max.
	half := DefaultPoolSizer(1, 100, 0.5)
	sz := half(Headroom{Available: 10}) // max=10, warm=ceil(10*0.5)=5
	if sz.Max != 10 || sz.Warm != 5 {
		t.Fatalf("warmFraction 0.5 sobre 10 devia dar 10/5, obtive %+v", sz)
	}
}

// --- NewAutoscaler: validação fail-closed ---

func TestNewAutoscaler_Validation(t *testing.T) {
	p := newPool(t, 1)
	src := &fakeHeadroom{}
	sizer := DefaultPoolSizer(1, 4, 1.0)
	if _, err := NewAutoscaler(nil, src, sizer); !errors.Is(err, ErrNilPool) {
		t.Fatalf("esperava ErrNilPool, obtive %v", err)
	}
	if _, err := NewAutoscaler(p, nil, sizer); !errors.Is(err, ErrNilHeadroomSource) {
		t.Fatalf("esperava ErrNilHeadroomSource, obtive %v", err)
	}
	if _, err := NewAutoscaler(p, src, nil); !errors.Is(err, ErrNilPoolSizer) {
		t.Fatalf("esperava ErrNilPoolSizer, obtive %v", err)
	}
	if _, err := NewAutoscaler(p, src, sizer); err != nil {
		t.Fatalf("construcao valida: %v", err)
	}
}

// --- Autoscaler.Tick: cresce e encolhe com o headroom (o AC central) ---

func TestAutoscaler_TickGrowsAndShrinksWithHeadroom(t *testing.T) {
	// Pool arranca vazio (warmN=0) mas com folga de crescimento até absMax=6.
	p := newPool(t, 0, WithAbsoluteMax(6), WithSynchronousReplenish())
	src := &fakeHeadroom{}
	as, err := NewAutoscaler(p, src, DefaultPoolSizer(1, 6, 1.0))
	if err != nil {
		t.Fatalf("NewAutoscaler: %v", err)
	}

	// Headroom para 4 VMs → cresce para 4/4.
	src.set(Headroom{Available: 4})
	sz, err := as.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if sz.Warm != 4 || sz.Max != 4 {
		t.Fatalf("tamanho derivado errado: %+v", sz)
	}
	if st := p.Stats(); st.Warm != 4 || st.WarmN != 4 || st.MaxN != 4 {
		t.Fatalf("pool nao cresceu para 4: %+v", st)
	}

	// Headroom cai para 1 VM → encolhe para 1/1, drenando o excesso pré-aquecido.
	src.set(Headroom{Available: 1})
	if _, err := as.Tick(context.Background()); err != nil {
		t.Fatalf("Tick shrink: %v", err)
	}
	if st := p.Stats(); st.Warm != 1 || st.WarmN != 1 || st.MaxN != 1 {
		t.Fatalf("pool nao encolheu para 1: %+v", st)
	}

	// Headroom sobe para lá do tecto absoluto → fixa-se a absMax=6.
	src.set(Headroom{Available: 999})
	if _, err := as.Tick(context.Background()); err != nil {
		t.Fatalf("Tick grow-to-cap: %v", err)
	}
	if st := p.Stats(); st.Warm != 6 || st.MaxN != 6 {
		t.Fatalf("pool devia fixar-se ao tecto absoluto 6: %+v", st)
	}
}

// TestAutoscaler_ZeroHeadroomFailsClosed prova o AC5: sob headroom nulo o pool não
// serve para lá do headroom — degrada (fail-closed), NÃO serve uma VM cold.
func TestAutoscaler_ZeroHeadroomFailsClosed(t *testing.T) {
	p := newPool(t, 2, WithAbsoluteMax(4), WithPolicy(PolicyReject), WithSynchronousReplenish())
	src := &fakeHeadroom{}
	as, _ := NewAutoscaler(p, src, DefaultPoolSizer(1, 4, 1.0))

	src.set(Headroom{Available: 0})
	if _, err := as.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if st := p.Stats(); st.Warm != 0 || st.MaxN != 0 {
		t.Fatalf("headroom 0 devia zerar o pool: %+v", st)
	}
	// Reserva sob pool zerado degrada fail-closed (nunca serve estado sujo/cold).
	if _, err := p.Reserve(context.Background()); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("esperava ErrPoolExhausted sob headroom 0, obtive %v", err)
	}
}

// TestAutoscaler_PeakAbsorbedThenDegrades prova o AC5 completo: até ao limite do
// headroom o pico é absorvido SEM cold-start (warm hits); acima, degrada.
func TestAutoscaler_PeakAbsorbedThenDegrades(t *testing.T) {
	p := newPool(t, 0, WithAbsoluteMax(8), WithPolicy(PolicyReject), WithSynchronousReplenish())
	src := &fakeHeadroom{}
	as, _ := NewAutoscaler(p, src, DefaultPoolSizer(1, 8, 1.0))
	src.set(Headroom{Available: 3}) // headroom para 3 VMs
	if _, err := as.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// As 3 reservas dentro do headroom são warm hits com cold-start ≈0.
	var leases []*Lease
	for i := 0; i < 3; i++ {
		l, err := p.Reserve(context.Background())
		if err != nil {
			t.Fatalf("reserva %d dentro do headroom: %v", i, err)
		}
		if l.Outcome() != OutcomeWarmHit || l.ColdStart() != 0 {
			t.Fatalf("reserva %d devia ser warm hit sem cold-start: outcome=%s cold=%v", i, l.Outcome(), l.ColdStart())
		}
		leases = append(leases, l)
	}
	// A 4.ª reserva excede o headroom → degrada fail-closed (AOS-107).
	if _, err := p.Reserve(context.Background()); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("reserva acima do headroom devia degradar, obtive %v", err)
	}
	for _, l := range leases {
		l.Release()
	}
}

// --- Tick: propagação de erro sem tocar no pool ---

func TestAutoscaler_TickHeadroomErrorLeavesPoolUntouched(t *testing.T) {
	p := newPool(t, 2, WithAbsoluteMax(6), WithSynchronousReplenish())
	src := &fakeHeadroom{}
	as, _ := NewAutoscaler(p, src, DefaultPoolSizer(1, 6, 1.0))

	boom := errors.New("headroom indisponivel")
	src.fail(boom)
	if _, err := as.Tick(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("Tick devia propagar o erro do headroom, obtive %v", err)
	}
	// O pool mantém o tamanho anterior (nunca redimensiona a partir de um headroom
	// que não pôde observar).
	if st := p.Stats(); st.Warm != 2 || st.MaxN != 2 {
		t.Fatalf("pool nao devia mudar sob erro de headroom: %+v", st)
	}
}

func TestAutoscaler_TickRespectsCancelledContext(t *testing.T) {
	p := newPool(t, 1, WithSynchronousReplenish())
	src := &fakeHeadroom{}
	src.set(Headroom{Available: 100})
	as, _ := NewAutoscaler(p, src, DefaultPoolSizer(1, 4, 1.0))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := as.Tick(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Tick com ctx cancelado devia devolver Canceled, obtive %v", err)
	}
}

// TestAutoscaler_RunStopsOnContext prova que o laço termina no cancelamento do ctx.
func TestAutoscaler_RunStopsOnContext(t *testing.T) {
	p := newPool(t, 0, WithAbsoluteMax(4), WithSynchronousReplenish())
	src := &fakeHeadroom{}
	src.set(Headroom{Available: 2})
	as, _ := NewAutoscaler(p, src, DefaultPoolSizer(1, 4, 1.0))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- as.Run(ctx, time.Millisecond) }()

	// A avaliação imediata do Run dimensiona o pool ao headroom — espera-a de forma
	// determinista (sem sleep fixo).
	deadline := time.After(2 * time.Second)
	for {
		if p.Stats().Warm == 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Run nao dimensionou o pool ao headroom a tempo")
		default:
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run devia devolver context.Canceled, obtive %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run nao terminou apos o cancelamento")
	}
}

// --- Pool.Resize: tecto absoluto, encolhimento, fecho, retro-compatibilidade ---

func TestPoolResize_ClampsToAbsoluteMax(t *testing.T) {
	p := newPool(t, 1, WithAbsoluteMax(3), WithSynchronousReplenish())
	p.Resize(10, 10) // pedido muito acima do tecto absoluto
	if st := p.Stats(); st.Warm != 3 || st.WarmN != 3 || st.MaxN != 3 {
		t.Fatalf("Resize devia fixar-se ao tecto absoluto 3: %+v", st)
	}
}

func TestPoolResize_ShrinkDrainsWarm(t *testing.T) {
	p := newPool(t, 4, WithAbsoluteMax(4), WithSynchronousReplenish())
	if st := p.Stats(); st.Warm != 4 {
		t.Fatalf("pre-condicao: %+v", st)
	}
	p.Resize(1, 4)
	if st := p.Stats(); st.Warm != 1 || st.WarmN != 1 {
		t.Fatalf("shrink devia drenar warm para 1: %+v", st)
	}
	// O pool continua a servir VMs limpas após o shrink.
	l, err := p.Reserve(context.Background())
	if err != nil {
		t.Fatalf("Reserve pos-shrink: %v", err)
	}
	if l.Overlay().Dirty() {
		t.Fatal("VM servida pos-shrink devia estar limpa")
	}
	l.Release()
}

func TestPoolResize_OnClosedIsNoop(t *testing.T) {
	p := newPool(t, 2, WithAbsoluteMax(4), WithSynchronousReplenish())
	p.Close()
	p.Resize(4, 4) // no-op silencioso sobre pool fechado
	if st := p.Stats(); st.Warm != 0 {
		t.Fatalf("Resize sobre pool fechado nao devia repor VMs: %+v", st)
	}
}

// TestPoolResize_AbsoluteMaxDefaultsToMaxN prova a retro-compatibilidade: sem
// WithAbsoluteMax o autoscaling fica limitado ao tecto inicial (dormente por
// omissão) — nenhum consumidor de AOS-065 vê o pool crescer inesperadamente.
func TestPoolResize_AbsoluteMaxDefaultsToMaxN(t *testing.T) {
	p := newPool(t, 1, WithSynchronousReplenish()) // sem WithAbsoluteMax nem WithMaxSize
	p.Resize(5, 5)
	if st := p.Stats(); st.MaxN != 1 || st.Warm > 1 {
		t.Fatalf("sem WithAbsoluteMax o pool nao devia crescer alem de 1: %+v", st)
	}
}

// --- SLIs de pool: ocupação e reciclagem (AC6) ---

func TestPool_SLIs_OccupancyAndRecycle(t *testing.T) {
	metrics := &MemoryColdStartMetricSink{}
	rec := NewColdStartRecorder(WithColdStartMetricSink(metrics))
	p := newPool(t, 2, WithColdStartRecorder(rec), WithSynchronousReplenish())

	l, err := p.Reserve(context.Background())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	l.Release()

	// Ocupação emitida (por reserva e por libertação) e reciclagem contada.
	var occ, recy int
	for _, m := range metrics.Metrics() {
		switch m.Name {
		case MetricPoolOccupancy:
			occ++
		case MetricPoolRecycle:
			recy++
		}
	}
	if occ == 0 {
		t.Fatal("esperava metricas de ocupacao emitidas")
	}
	if recy == 0 {
		t.Fatal("esperava metrica de reciclagem emitida")
	}
	if agg, ok := rec.SnapshotAgg(p.key()); !ok || agg.Recycles != 1 {
		t.Fatalf("esperava 1 reciclagem no agregado, obtive %+v (ok=%v)", agg, ok)
	}

	// Resize emite os alvos (prova de que o tamanho NÃO é constante no fluxo de
	// metricas).
	p.Resize(1, 2)
	var resize int
	for _, m := range metrics.Metrics() {
		if m.Name == MetricPoolResize {
			resize++
		}
	}
	if resize == 0 {
		t.Fatal("esperava metrica de resize emitida")
	}
}

// TestAutoscaler_ConcurrentResizeAndReserve é a sonda adversarial de concorrência: o
// autoscaler reajusta o pool (cresce/encolhe, incl. até headroom 0) EM PARALELO com
// muitas reservas/libertações. Sob -race prova que Resize/drenagem e Reserve/Release
// não corrompem os contadores nem servem estado sujo.
func TestAutoscaler_ConcurrentResizeAndReserve(t *testing.T) {
	p := newPool(t, 2, WithAbsoluteMax(8), WithPolicy(PolicyExpand))
	src := &fakeHeadroom{}
	src.set(Headroom{Available: 4})
	as, _ := NewAutoscaler(p, src, DefaultPoolSizer(1, 8, 1.0))

	ctx, cancel := context.WithCancel(context.Background())
	var tuner sync.WaitGroup
	tuner.Add(1)
	go func() {
		defer tuner.Done()
		vals := []int64{0, 1, 4, 8, 2, 6}
		for i := 0; ; i++ {
			select {
			case <-ctx.Done():
				return
			default:
			}
			src.set(Headroom{Available: vals[i%len(vals)]})
			_, _ = as.Tick(context.Background())
		}
	}()

	var workers sync.WaitGroup
	for g := 0; g < 8; g++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < 200; i++ {
				l, err := p.Reserve(context.Background())
				if err != nil {
					continue // degradação fail-closed sob headroom baixo — aceitável
				}
				if l.Overlay().Dirty() {
					t.Errorf("VM servida com estado sujo sob concorrencia")
				}
				l.Release()
			}
		}()
	}
	workers.Wait()
	cancel()
	tuner.Wait()

	// Contadores coerentes no fim: nada em uso, dentro do tecto absoluto.
	if st := p.Stats(); st.InUse != 0 || st.Warm < 0 || st.Warm > 8 || st.MaxN > 8 {
		t.Fatalf("estado final incoerente apos concorrencia: %+v", st)
	}
}

// TestPool_SLIs_NoSecretsInPoolMetrics varre os atributos das métricas de pool à
// procura de valores sensíveis (ADR-006): só contagens e eixos versão/driver.
func TestPool_SLIs_NoSecretsInPoolMetrics(t *testing.T) {
	metrics := &MemoryColdStartMetricSink{}
	rec := NewColdStartRecorder(WithColdStartMetricSink(metrics))
	p := newPool(t, 1, WithColdStartRecorder(rec), WithSynchronousReplenish())
	l, _ := p.Reserve(context.Background())
	l.Release()
	p.Resize(1, 1)

	allowed := map[string]bool{
		AttrImageVersion: true, AttrDriver: true, AttrPoolInUse: true,
		AttrPoolWarm: true, AttrPoolMax: true, AttrPoolScope: true,
		AttrProvisionOutcome: true, AttrColdStartScope: true,
	}
	for _, m := range metrics.Metrics() {
		for k := range m.Attributes {
			if !allowed[k] {
				t.Fatalf("atributo inesperado numa metrica de pool: %q (metrica %s)", k, m.Name)
			}
		}
	}
}
