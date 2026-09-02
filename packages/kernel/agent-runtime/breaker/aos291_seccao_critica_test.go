package breaker

// AOS-291 — O MUTEX DO DISJUNTOR NÃO PODE COBRIR I/O NEM CÓDIGO DE TERCEIROS.
//
// O defeito: `Observe` fazia Lock/defer Unlock e a secção crítica engolia tudo o que `trip`
// faz — a transição DURÁVEL (append ao Event Store, que no substrato replicado é rede), o
// `span.End()` (que chama `Exporter.Export`, síncrono) e o `AlertSink` INJECTADO, que é código
// arbitrário de quem compõe o nó. `manualTransition` — a mecânica de `Abort` e
// `EscalateToHuman` — repetia o padrão, e `Snapshot` toma o mesmo mutex.
//
// A auditoria mediu, com um sink bloqueado 3 s:
//
//	CONTROLO Snapshot() ocioso (media/10k)          = 1.669µs
//	Snapshot() / Abort() / EscalateToHuman() esperaram = 3.0008192s
//
// Três ordens de grandeza. E o custo não é a latência: **o instante em que o disjuntor dispara
// é o instante em que se quer abortar**, e era esse o instante em que a via de saída graciosa
// ficava trancada — pela mesma coisa que a tornava necessária.
//
// # PORQUE ESTES TESTES NÃO MEDEM TEMPO
//
// Um teste que afirmasse «Abort demorou menos de X» seria uma aposta na máquina que o corre.
// Estes fazem outra coisa: prendem o sink (ou o Append) num canal e, ENQUANTO ele está preso,
// exigem que as operações que tomam `b.mu` COMPLETEM. Com o código anterior não completavam —
// ficavam à espera do mutex até o teste libertar o bloqueio, o que nunca aconteceria, porque
// quem liberta é a linha a seguir. O veredicto é binário e determinista.
//
// O prazo que existe (`limiteDeEspera`) não mede desempenho: converte um deadlock num
// diagnóstico legível, em vez de deixar a suite bater no timeout global do `go test` e sair
// como um dump de goroutines que ninguém lê.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// limiteDeEspera é generoso de propósito: só distingue «completou» de «ficou preso no mutex».
const limiteDeEspera = 5 * time.Second

// completaEnquantoBloqueado corre `op` numa goroutine e exige que ela termine enquanto o
// bloqueio do teste ainda está activo. Devolve o erro de `op`.
func completaEnquantoBloqueado(t *testing.T, nome string, op func() error) error {
	t.Helper()
	feito := make(chan error, 1)
	go func() { feito <- op() }()
	select {
	case err := <-feito:
		return err
	case <-time.After(limiteDeEspera):
		t.Fatalf("%s NAO completou com o AlertSink/Append ainda bloqueado — a seccao critica do disjuntor voltou a cobrir I/O (AOS-291)", nome)
		return nil
	}
}

// TestAOS291_SinkBloqueadoNaoPrendeSnapshotNemAbort é o teste que a AC3 pede, no molde da sonda
// da auditoria. O sink fica preso DEPOIS de a transição durável ter sucesso — que é exactamente
// o instante em que o run acabou de ser parado e em que um operador quereria agir.
func TestAOS291_SinkBloqueadoNaoPrendeSnapshotNemAbort(t *testing.T) {
	ctx := context.Background()
	clk := newManualClock()
	st := newStore(t)
	m := runningMachine(t, st, "run-aos291-sink", clk)

	entrou := make(chan struct{})   // fecha quando o sink começa a bloquear
	libertar := make(chan struct{}) // fecha quando o teste o liberta

	b, err := NewBreaker(m, NewStaticThresholdProvider(Thresholds{MaxCostMicroUSDPerSecond: 500_000}), "greedy",
		WithVelocitySource(VelocityFunc(func() otelgenai.CostVelocity {
			return costVelocity(1_000_000, 0)
		})),
		WithAlertSink(AlertFunc(func(context.Context, Alert) {
			close(entrou)
			<-libertar
		})),
	)
	if err != nil {
		t.Fatalf("NewBreaker: %v", err)
	}

	observado := make(chan error, 1)
	go func() {
		_, oerr := b.Observe(ctx)
		observado <- oerr
	}()

	// Espera que o disjuntor tenha MESMO disparado e esteja preso dentro do sink.
	select {
	case <-entrou:
	case <-time.After(limiteDeEspera):
		t.Fatal("o sink nunca foi chamado — o trip nao ocorreu; o teste nao esta a exercitar nada")
	}

	// A transição já foi consumada antes do alerta (o alerta reflecte um facto consumado).
	if cur := m.Current(); cur != state.Paused {
		t.Fatalf("estado=%q com o sink ja a bloquear; quero paused (a transicao precede o alerta)", cur)
	}

	// AS DUAS ASSERÇÕES QUE IMPORTAM: ambas tomam `b.mu`.
	completaEnquantoBloqueado(t, "Snapshot()", func() error {
		_ = b.Snapshot()
		return nil
	})

	// `Abort` sobre um run JÁ parado devolve ErrNotRunning — e é isso que se espera aqui. O que
	// se prova não é o desfecho, é que ele CHEGA: antes de AOS-291 esta chamada ficava presa no
	// mutex que o sink detinha, e o operador não conseguia abortar precisamente no momento em
	// que o disjuntor lhe disse que devia.
	if err := completaEnquantoBloqueado(t, "Abort()", func() error {
		return b.Abort(ctx, "operador quer sair enquanto o alerta nao entrega")
	}); err != nil && !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Abort devolveu %v; quero nil ou ErrNotRunning (o run ja estava parado pelo trip)", err)
	}

	close(libertar)
	if oerr := <-observado; oerr != nil {
		t.Fatalf("Observe: %v", oerr)
	}
}

// blockingStore prende o PRÓXIMO Append até ser libertado. É a outra metade da sonda da
// auditoria — a que mediu `Snapshot()` a esperar 2,0014 s por um Append atrasado, com o sink
// inerte. Sem ela, o teste de cima provaria só que o ALERTA saiu do lock, e a AC2 fala também
// da transição durável.
type blockingStore struct {
	*eventstore.Store
	blockNext bool
	entrou    chan struct{}
	libertar  chan struct{}
}

func (s *blockingStore) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	if s.blockNext {
		s.blockNext = false
		close(s.entrou)
		<-s.libertar
	}
	return s.Store.Append(ctx, streamID, in, opts...)
}

// TestAOS291_AppendBloqueadoNaoPrendeOMutexDoDisjuntor prova a metade da AC2 que é ALCANÇÁVEL
// dentro deste pacote: com um Append preso, o MUTEX DO DISJUNTOR está livre.
//
// # A FRONTEIRA, e porque este teste injecta um wall-clock
//
// A primeira versão deste teste usava o wall-clock por omissão e FALHAVA. A causa não era o
// disjuntor: `Snapshot` → `snapshotLocked` → `b.wall.Elapsed()` e, no default,
// [NewMachineWallClock] chama `m.EnteredAt()`, que tomava o mutex da [state.Machine] — o MESMO
// que `Transition` segurava durante o Append. `Snapshot` esperava pelo append, mas por
// `machine.mu`, não por `b.mu`.
//
// ESSE LIMITE FECHOU — **AOS-301**. A máquina passou a ter dois locks: `mu` serializa as mutações
// (e mantém a validação atómica face à escrita, que é o que AOS-291 assumiu ao largar `b.mu`),
// e um segundo, tomado só para publicar/ler os três campos de estado, nunca é detido durante I/O.
// `Current()` e `EnteredAt()` deixaram de esperar por um Append lento; a prova está em
// `state/aos301_leitura_sem_io_test.go`.
//
// A INJECÇÃO DA FONTE MANTÉM-SE, e não por inércia: este teste é sobre `b.mu`, e depender do
// wall-clock por omissão voltaria a atar o seu veredicto ao lock da máquina. Uma regressão em
// AOS-301 deve avermelhar o teste de AOS-301, não este.
//
// O que este teste isola, injectando uma fonte que NÃO toca na máquina, é a parte que AOS-291
// mudou mesmo: `b.mu` deixou de ser detido durante o I/O. Antes, esta asserção falhava com
// QUALQUER wall-clock, porque o lock do disjuntor cobria o append.
func TestAOS291_AppendBloqueadoNaoPrendeOMutexDoDisjuntor(t *testing.T) {
	ctx := context.Background()
	clk := newManualClock()
	base := newStore(t)
	bs := &blockingStore{Store: base, entrou: make(chan struct{}), libertar: make(chan struct{})}

	// A máquina chega a running com o store NORMAL; só depois se arma o bloqueio, para que o
	// que fica preso seja o append do TRIP e não o do claim inicial.
	m := runningMachine(t, bs, "run-aos291-append", clk)
	bs.blockNext = true

	b, err := NewBreaker(m, NewStaticThresholdProvider(Thresholds{MaxCostMicroUSDPerSecond: 500_000}), "greedy",
		WithVelocitySource(VelocityFunc(func() otelgenai.CostVelocity {
			return costVelocity(1_000_000, 0)
		})),
		// Fonte de wall-clock INDEPENDENTE da máquina: isola `b.mu` de `machine.mu`.
		WithWallClockSource(WallClockFunc(func() time.Duration { return time.Second })),
	)
	if err != nil {
		t.Fatalf("NewBreaker: %v", err)
	}

	observado := make(chan error, 1)
	go func() {
		_, oerr := b.Observe(ctx)
		observado <- oerr
	}()

	select {
	case <-bs.entrou:
	case <-time.After(limiteDeEspera):
		t.Fatal("o Append do trip nunca foi alcancado — o teste nao esta a exercitar nada")
	}

	completaEnquantoBloqueado(t, "Snapshot() com Append preso (wall-clock independente da maquina)", func() error {
		_ = b.Snapshot()
		return nil
	})

	close(bs.libertar)
	if oerr := <-observado; oerr != nil {
		t.Fatalf("Observe: %v", oerr)
	}
	if cur := m.Current(); cur != state.Paused {
		t.Fatalf("estado=%q apos libertar o append; quero paused", cur)
	}
}
