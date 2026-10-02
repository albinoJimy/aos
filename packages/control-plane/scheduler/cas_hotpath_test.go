package scheduler_test

// Testes do CAMINHO QUENTE de CAS do escalonador (AOS-420). Fecham os dois
// deferimentos DEF-910 e DEF-911, que vivem no mesmo caminho:
//
//   - DEF-910 — o lock do dispatcher era mantido ATRAVÉS do CAS durável da
//     admissão. [TestDispatch_SubmitProgrideDuranteOAdmitDuravel] observa-o pela
//     única via que não é tempo de relógio: um Admit que PARA lá dentro, e a
//     pergunta «o resto do dispatcher ainda progride?».
//   - DEF-911 — cada admissão relia o stream do bucket desde a seq 1.
//     [TestAdmit_EventosLidosNaoCrescemComOHistorico] mede o NÚMERO DE EVENTOS
//     LIDOS, não o tempo: tempo é flaky e, pior, não distingue O(N) de O(1) sem
//     um limiar arbitrário.
//
// Deterministas: relógio manual (mutClock/fixedClock) e ids sequenciais
// (seqIDGen), reutilizados de admission_test.go / priority_test.go. Nenhuma
// asserção lê time.Now nem rand.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/scheduler"
	"github.com/aos-ref/substrate/eventstore"
)

// bloqueioMax é o tecto de PACIÊNCIA dos testes de progresso. NÃO é uma asserção
// de desempenho: com o lock mantido através do Admit o progresso é IMPOSSÍVEL
// (bloqueio verdadeiro, não lento), e sem ele acontece em microssegundos. O valor
// só existe porque um teste não pode esperar para sempre.
const bloqueioMax = 5 * time.Second

// ---------------------------------------------------------------------------
// DEF-910 — o lock do dispatcher não é mantido através do CAS durável.
// ---------------------------------------------------------------------------

// gateBloqueante é um [scheduler.AdmissionGate] que PARA dentro do Admit até ser
// libertado. É o substituto determinístico de um CAS durável lento: o que
// interessa é que o Admit esteja EM CURSO, não quanto tempo demora.
type gateBloqueante struct {
	entrou   chan struct{}
	liberta  chan struct{}
	umaVez   sync.Once
	soltaVez sync.Once
	n        atomic.Int64
}

func novoGateBloqueante() *gateBloqueante {
	return &gateBloqueante{entrou: make(chan struct{}), liberta: make(chan struct{})}
}

func (g *gateBloqueante) Admit(ctx context.Context, req scheduler.AdmitRequest) (scheduler.AdmitResult, error) {
	g.n.Add(1)
	g.umaVez.Do(func() { close(g.entrou) })
	select {
	case <-g.liberta:
	case <-ctx.Done():
		return scheduler.AdmitResult{}, ctx.Err()
	}
	return scheduler.AdmitResult{Granted: true, ReservationID: "res:" + req.RequestID}, nil
}

func (g *gateBloqueante) solta() { g.soltaVez.Do(func() { close(g.liberta) }) }

func TestDispatch_SubmitProgrideDuranteOAdmitDuravel(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	g := novoGateBloqueante()
	defer g.solta()

	d := mustDispatcher(t,
		scheduler.WithAdmission(g),
		scheduler.WithDispatchClock(fixedClock(base)),
		scheduler.WithDefaultKey(testKey),
	)
	ctx := context.Background()

	if _, err := d.Submit(ctx, scheduler.Task{ID: "t1", Tenant: "acme", Class: "P0"}); err != nil {
		t.Fatalf("Submit t1: %v", err)
	}

	despacho := make(chan error, 1)
	go func() {
		_, err := d.Dispatch(ctx)
		despacho <- err
	}()

	// O Admit está EM CURSO a partir daqui (a admissão é a operação durável).
	<-g.entrou

	submetido := make(chan error, 1)
	go func() {
		_, err := d.Submit(ctx, scheduler.Task{ID: "t2", Tenant: "acme", Class: "P1"})
		submetido <- err
	}()

	select {
	case err := <-submetido:
		if err != nil {
			t.Fatalf("Submit concorrente com o Admit: %v", err)
		}
	case <-time.After(bloqueioMax):
		g.solta()
		t.Fatalf("Submit ficou BLOQUEADO enquanto o Admit durável corria: o dispatcher mantém "+
			"o seu lock através do CAS da admissão (DEF-910). Admits em curso=%d", g.n.Load())
	}

	g.solta()
	if err := <-despacho; err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// A tarefa submetida DURANTE o Admit continua pendente e despachável.
	if got := d.Pending(); got != 1 {
		t.Fatalf("Pending()=%d, esperado 1 (t2 submetida durante o Admit)", got)
	}
}

// TestDispatch_ConcorrenteNaoDuplicaNemPerdeTarefas é um CONTROLO NEGATIVO do
// eixo DEF-910: passa ANTES e DEPOIS da correcção. Não prova o defeito — prova
// que a correcção (tirar o Admit da secção crítica) não introduz a corrida que
// o lock antes tornava impossível: nenhuma tarefa despachada duas vezes, nenhuma
// perdida. Corre sob -race por ser essa exactamente a classe de defeito.
func TestDispatch_ConcorrenteNaoDuplicaNemPerdeTarefas(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	qp := qpTPM(1_000_000, 1_000_000, time.Minute)
	adm, _ := newAdm(t, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithCostEstimator(scheduler.FixedCostEstimator{Tokens: 1}),
		scheduler.WithIDGen(seqIDGen()),
	)
	d := mustDispatcher(t,
		scheduler.WithAdmission(adm),
		scheduler.WithDispatchClock(fixedClock(base)),
		scheduler.WithDefaultKey(testKey),
	)
	ctx := context.Background()

	const nTarefas = 64
	for i := 0; i < nTarefas; i++ {
		id := "t" + itoa2(i)
		if _, err := d.Submit(ctx, scheduler.Task{ID: id, Tenant: "acme", Class: "P1", Cost: 1}); err != nil {
			t.Fatalf("Submit %s: %v", id, err)
		}
	}

	const nWorkers = 8
	var mu sync.Mutex
	vistos := make(map[string]int, nTarefas)
	var wg sync.WaitGroup
	for w := 0; w < nWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				res, err := d.Dispatch(ctx)
				if err != nil {
					t.Errorf("Dispatch: %v", err)
					return
				}
				if !res.Dispatched {
					return
				}
				mu.Lock()
				vistos[res.Task.ID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(vistos) != nTarefas {
		t.Fatalf("tarefas despachadas distintas=%d, esperado %d (perdeu trabalho)", len(vistos), nTarefas)
	}
	for id, n := range vistos {
		if n != 1 {
			t.Fatalf("tarefa %q despachada %d vezes (despacho duplicado)", id, n)
		}
	}
	if got := d.Pending(); got != 0 {
		t.Fatalf("Pending()=%d no fim, esperado 0", got)
	}
}

// itoa2 formata i com dois dígitos (ids determinísticos, sem fmt no caminho de
// asserção).
func itoa2(i int) string {
	const d = "0123456789"
	return string([]byte{d[(i/10)%10], d[i%10]})
}

// ---------------------------------------------------------------------------
// DEF-911 — a admissão não relê o stream inteiro do bucket a cada decisão.
// ---------------------------------------------------------------------------

// logContador envolve um [scheduler.EventLog] e conta as LEITURAS e os EVENTOS
// devolvidos. A unidade de medida do DEF-911 é o número de EVENTOS LIDOS por
// admissão — não o tempo de relógio, que é flaky e não separa O(N) de O(1).
type logContador struct {
	inner    scheduler.EventLog
	mu       sync.Mutex
	leituras int
	eventos  int
}

func (l *logContador) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	return l.inner.Append(ctx, streamID, in, opts...)
}

func (l *logContador) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	evs, err := l.inner.Read(ctx, streamID, fromSeq)
	l.mu.Lock()
	l.leituras++
	l.eventos += len(evs)
	l.mu.Unlock()
	return evs, err
}

// zera reinicia os contadores e devolve o que estava acumulado.
func (l *logContador) zera() (leituras, eventos int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	leituras, eventos = l.leituras, l.eventos
	l.leituras, l.eventos = 0, 0
	return
}

// admContado constrói uma Admission sobre um Event Store real, com o contador de
// leituras interposto.
func admContado(t *testing.T, qp scheduler.QuotaProvider, opts ...scheduler.AdmissionOption) (*scheduler.Admission, *logContador) {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	lc := &logContador{inner: es}
	adm, err := scheduler.NewAdmission(lc, qp, opts...)
	if err != nil {
		t.Fatalf("NewAdmission: %v", err)
	}
	return adm, lc
}

func TestAdmit_EventosLidosNaoCrescemComOHistorico(t *testing.T) {
	t.Parallel()

	// Cenário A — JANELA DESLIZANTE. O relógio avança 1 s por admissão sobre uma
	// janela de 1 min, pelo que só ~60 reservas contam em cada instante. O stream
	// cresce sem parar; o que o fold PRECISA de ler não.
	t.Run("janela deslizante", func(t *testing.T) {
		t.Parallel()
		clk := &mutClock{}
		clk.set(time.Unix(1_000_000, 0))
		const janela = time.Minute
		const passo = time.Second
		qp := qpTPM(1_000_000, 1_000_000, janela)
		adm, lc := admContado(t, qp,
			scheduler.WithClock(clk.now),
			scheduler.WithCostEstimator(scheduler.FixedCostEstimator{Tokens: 1}),
			scheduler.WithIDGen(seqIDGen()),
		)
		ctx := context.Background()

		// Tecto: 1,5× o número de reservas que cabem na janela. Independente de N.
		const naJanela = int(janela / passo) // 60
		const tecto = naJanela * 3 / 2       // 90
		const n = 400

		porAdmissao := make([]int, n)
		total := 0
		lc.zera()
		for i := 0; i < n; i++ {
			res, err := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey, Tenant: "acme"})
			if err != nil {
				t.Fatalf("Admit[%d]: %v", i, err)
			}
			if !res.Granted {
				t.Fatalf("Admit[%d] adiado com TPM de sobra: %+v", i, res)
			}
			_, ev := lc.zera()
			porAdmissao[i] = ev
			total += ev
			clk.advance(passo)
		}

		// (1) A ÚLTIMA admissão não lê mais do que cabe na janela.
		if porAdmissao[n-1] > tecto {
			t.Fatalf("a admissão #%d leu %d eventos (tecto %d): a leitura do bucket cresce com o "+
				"HISTÓRICO e não com a janela (DEF-911). #%d lera %d",
				n, porAdmissao[n-1], tecto, naJanela*2, porAdmissao[naJanela*2-1])
		}
		// (2) Não cresce: a admissão tardia não lê mais do que uma já em regime.
		if porAdmissao[n-1] > porAdmissao[naJanela*2-1]+naJanela {
			t.Fatalf("leitura por admissão CRESCE: #%d=%d eventos vs #%d=%d",
				naJanela*2, porAdmissao[naJanela*2-1], n, porAdmissao[n-1])
		}
		// (3) O acumulado é O(N), não O(N²).
		if total > n*tecto {
			t.Fatalf("eventos lidos no total=%d > N*tecto=%d (acumulado quadrático)", total, n*tecto)
		}

		// O replay/auditoria continua INTEGRAL: a fronteira de leitura é uma pista
		// do fold, não uma compactação do stream.
		recs, err := adm.Replay(ctx, testKey)
		if err != nil {
			t.Fatalf("Replay: %v", err)
		}
		if len(recs) != n {
			t.Fatalf("Replay devolveu %d registos, esperado %d (o replay tem de ler o stream INTEIRO)", len(recs), n)
		}
	})

	// Cenário B — RESERVAS RECONCILIADAS. Relógio congelado: nada expira pelo
	// refill, mas cada reserva é libertada logo a seguir. O débito activo é sempre
	// zero e, ainda assim, o stream cresce 2 eventos por iteração.
	t.Run("reservas reconciliadas", func(t *testing.T) {
		t.Parallel()
		base := time.Unix(1_000_000, 0)
		qp := qpTPM(1_000_000, 1_000_000, time.Hour)
		adm, lc := admContado(t, qp,
			scheduler.WithClock(fixedClock(base)),
			scheduler.WithCostEstimator(scheduler.FixedCostEstimator{Tokens: 1}),
			scheduler.WithIDGen(seqIDGen()),
		)
		ctx := context.Background()

		const n = 400
		const tecto = 8 // nada activo ⇒ basta a cauda (a âncora do CAS)

		ultimo := 0
		lc.zera()
		for i := 0; i < n; i++ {
			res, err := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey, Tenant: "acme"})
			if err != nil {
				t.Fatalf("Admit[%d]: %v", i, err)
			}
			if !res.Granted {
				t.Fatalf("Admit[%d] adiado: %+v", i, res)
			}
			_, ev := lc.zera()
			ultimo = ev
			if err := adm.Release(ctx, testKey, res.ReservationID, 1, 1); err != nil {
				t.Fatalf("Release[%d]: %v", i, err)
			}
			lc.zera()
		}

		if ultimo > tecto {
			t.Fatalf("a admissão #%d leu %d eventos (tecto %d) com ZERO débito activo: o fold relê "+
				"o stream inteiro a cada decisão (DEF-911)", n, ultimo, tecto)
		}

		recs, err := adm.Replay(ctx, testKey)
		if err != nil {
			t.Fatalf("Replay: %v", err)
		}
		if len(recs) != 2*n {
			t.Fatalf("Replay devolveu %d registos, esperado %d (grants+releases)", len(recs), 2*n)
		}
	})
}
