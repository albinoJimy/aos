package runlifecycle

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aos-ref/control-plane/orchestrator"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/substrate/eventstore"
)

// Sentinelas de erro — fail-closed, comparáveis por errors.Is.
var (
	// ErrDeps — dependências obrigatórias em falta na construção.
	ErrDeps = errors.New("runlifecycle: dependências em falta (event store / lease manager)")
	// ErrNotHeld — a posse já foi largada (ou nunca foi tomada) e pediu-se-lhe uma via
	// de escrita. NÃO é a mesma coisa que uma escrita recusada pelo fencing: esta é a
	// recusa LOCAL, antes de tocar no log, de um chamador que já não é dono. A recusa
	// DURÁVEL (a que vale contra outro processo) é a do [durable.FencedAppender].
	//
	// As duas coexistem de propósito e não são redundantes: esta apanha o erro de
	// programação no processo que largou; a outra apanha o processo que largou e
	// continua a tentar, incluindo um que tenha sido superado sem dar por isso.
	ErrNotHeld = errors.New("runlifecycle: posse já largada — este detentor não tem via de escrita")
	// ErrEmptyRunID — run_id vazio.
	ErrEmptyRunID = errors.New("runlifecycle: run_id vazio")
)

// EventStore é o subconjunto do Event Store (AOS-002) de que este pacote depende —
// a MESMA forma que [orchestrator.EventStore], [state.EventStore] e
// [durable.EventStore] declaram. *eventstore.Store satisfá-la.
type EventStore interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error)
}

// LeaseAuthority é a autoridade de posse consumida por [Claim]. É deliberadamente
// mais larga que [durable.LeaseAuthority]: exige também o `Release` (o ANÚNCIO de
// que se largou, que é o que faz o handoff de ADR-023 §2.5 não ter janela) e a
// leitura do lease corrente. O [durable.LeaseManager] satisfá-la.
type LeaseAuthority interface {
	Claim(ctx context.Context, runID string) (durable.Lease, error)
	Heartbeat(ctx context.Context, lease durable.Lease) (durable.Lease, error)
	Release(ctx context.Context, lease durable.Lease) error
	CurrentToken(ctx context.Context, runID string) (durable.FencingToken, error)
}

// Compile-time: o gestor de lease de AOS-018 é a realização de produção.
var _ LeaseAuthority = (*durable.LeaseManager)(nil)

// Tenure é a POSSE de um run por ESTE processo: o fencing token corrente de
// `lease:<run_id>` mais a via de escrita que ele autoriza. Construir com [Claim].
//
// # Porque a posse é um objecto e não um par de chamadas
//
// A regra do ADR-023 §2.1 é que o direito de escrever É a posse. Um par
// `Claim()`/`Append()` solto deixaria o token a viajar como argumento por caminhos
// que ninguém audita — e foi exactamente assim que o `GraphBuilder` de AOS-025 ficou
// sem token nenhum. Aqui o token NÃO é público: quem tem uma Tenure viva tem uma via
// de escrita; quem a largou não tem via nenhuma ([ErrNotHeld]), e o log recusa-o na
// mesma se tentar por fora ([durable.ErrStaleFencingToken]).
//
// Seguro para uso concorrente.
type Tenure struct {
	runID  string
	store  EventStore
	leases LeaseAuthority
	fenced *durable.FencedAppender

	mu       sync.RWMutex
	lease    durable.Lease
	released bool
}

// Claim TOMA a posse do run, mintando um fencing token monotónico no stream
// `lease:<run_id>`. Falha com [durable.ErrLeaseHeld] se outro processo detiver um
// lease AINDA VÁLIDO — não se rouba posse viva; a passagem legítima é o detentor
// anterior ANUNCIAR ([Tenure.Release]), não o TTL expirar (ADR-023 §2.5).
//
// Dois processos a reclamar em simultâneo competem no MESMO `expected_seq` do stream
// de lease: um vence e materializa o token, o outro vê o conflito de concorrência,
// relê e — porque o vencedor já lá está com um lease vivo — recebe
// [durable.ErrLeaseHeld]. É a arbitragem, e é do Event Store, não deste pacote.
func Claim(ctx context.Context, store EventStore, leases LeaseAuthority, runID string) (*Tenure, error) {
	if store == nil || leases == nil {
		return nil, ErrDeps
	}
	if runID == "" {
		return nil, ErrEmptyRunID
	}
	tokens, ok := leases.(durable.TokenSource)
	if !ok {
		// Sem autoridade de token corrente não há enforcement possível, e uma posse
		// sem enforcement é a que o ADR-023 existe para acabar. Fail-closed.
		return nil, fmt.Errorf("%w: a autoridade de lease não é uma durable.TokenSource — sem ela não há escrita fenced", ErrDeps)
	}
	fenced, err := durable.NewFencedAppender(store, tokens)
	if err != nil {
		return nil, err
	}
	lease, err := leases.Claim(ctx, runID)
	if err != nil {
		return nil, err
	}
	return &Tenure{runID: runID, store: store, leases: leases, fenced: fenced, lease: lease}, nil
}

// RunID devolve o run possuído.
func (t *Tenure) RunID() string { return t.runID }

// Token devolve o valor do fencing token desta posse — para OBSERVABILIDADE e para
// os testes de fronteira. Não é uma via de escrita: escrever exige o appender, que
// verifica o token contra o log a cada `Append`.
func (t *Tenure) Token() uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.lease.Token.Value()
}

// Held reporta se esta posse ainda não foi largada POR ESTE processo. É um facto
// LOCAL: um `true` não prova que o token continue a ser o corrente no log (outro
// processo pode ter reclamado depois de uma expiração por TTL). A prova durável é
// sempre a do `Append` fenced — ver [Tenure.Append].
func (t *Tenure) Held() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return !t.released
}

// Append escreve um facto de ciclo de vida no stream do run ATRAVÉS do enforcement
// de fencing. É a ÚNICA via de escrita deste pacote (ADR-023 §2.4).
//
// Duas recusas, em camadas e não redundantes:
//
//  1. LOCAL — [ErrNotHeld] se este detentor já anunciou que largou. Apanha o erro de
//     programação sem ir ao log.
//  2. DURÁVEL — [durable.ErrStaleFencingToken] do [durable.FencedAppender] se o token
//     for inferior ao corrente (superado por um novo claim) OU se o lease corrente já
//     tiver expirado/sido largado (via [durable.LeaseExpiryAuthority]). É esta que
//     vale contra outro processo, e é ela que faz a escrita obsoleta NÃO CHEGAR ao log.
//
// LIMITE DECLARADO (não é fechado aqui): a janela TOCTOU do caso token-IGUAL do
// [durable.FencedAppender] — o token é lido externamente e não é dobrado no
// `expected_seq` do evento de negócio. O caso token-estritamente-inferior fica
// fechado; o detentor a ser superado NAQUELE instante exacto não. Ver ADR-023 §4 e o
// comentário de `durable.FencedAppender`.
func (t *Tenure) Append(ctx context.Context, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	t.mu.RLock()
	released, token := t.released, t.lease.Token
	t.mu.RUnlock()
	if released {
		return eventstore.AppendResult{}, fmt.Errorf("%w: run %s", ErrNotHeld, t.runID)
	}
	return t.fenced.Append(ctx, t.runID, token, in, opts...)
}

// Read relê o stream do run. A leitura NÃO é fenced e não tem de ser: ler não move
// estado, e o replay é uma função pura do log (ADR-010). É por esta via que um
// componente que toma posse a meio re-hidrata — ver [Tenure.Graph].
func (t *Tenure) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	return t.store.Read(ctx, streamID, fromSeq)
}

// Heartbeat renova o TTL da posse SEM mintar novo token. Um lease já superado
// devolve [durable.ErrLeaseSuperseded]; um já expirado, [durable.ErrLeaseExpired] —
// e em qualquer dos casos a posse local é marcada como LARGADA, para que a via de
// escrita feche imediatamente em vez de continuar a bater no fencing a cada escrita.
//
// É o mesmo desenho do `heartbeat()` do loop de serviço do nó (AOS-164a): quem perde
// a partição pára, não insiste.
func (t *Tenure) Heartbeat(ctx context.Context) error {
	t.mu.RLock()
	released, lease := t.released, t.lease
	t.mu.RUnlock()
	if released {
		return fmt.Errorf("%w: run %s", ErrNotHeld, t.runID)
	}
	renewed, err := t.leases.Heartbeat(ctx, lease)
	if err != nil {
		if errors.Is(err, durable.ErrLeaseSuperseded) || errors.Is(err, durable.ErrLeaseExpired) {
			t.mu.Lock()
			t.released = true
			t.mu.Unlock()
		}
		return err
	}
	t.mu.Lock()
	t.lease = renewed
	t.mu.Unlock()
	return nil
}

// Release ANUNCIA que este detentor deixou de servir o run, tornando-o
// IMEDIATAMENTE reclamável em vez de esperar o TTL (ADR-023 §2.5). Idempotente.
//
// # É O ÚLTIMO ACTO DA POSSE — e o desenho impõe-no, não pede
//
// Depois disto a expiração do PRÓPRIO token passa a `now`, e o
// [durable.FencedAppender] — que consulta [durable.LeaseExpiryAuthority] — recusa as
// escritas deste detentor ANTES sequer de existir um novo claim. Um escritor que
// largue antes de acabar de escrever perde as escritas seguintes, ruidosamente
// ([durable.ErrStaleFencingToken]), nunca em silêncio.
//
// É essa recusa que faz o intervalo entre largar e reclamar ser um intervalo em que
// NINGUÉM escreve — a forma correcta de um handoff, e não uma corrida rápida.
func (t *Tenure) Release(ctx context.Context) error {
	t.mu.Lock()
	if t.released {
		t.mu.Unlock()
		return nil
	}
	lease := t.lease
	// Marca-se ANTES do Append: se o `lease.released` commitar e a resposta perder-se,
	// a posse está de facto largada e continuar a escrever seria escrever a descoberto.
	// Marcar depois seria a ordem que deixa essa janela aberta.
	t.released = true
	t.mu.Unlock()

	if err := t.leases.Release(ctx, lease); err != nil {
		if errors.Is(err, durable.ErrLeaseSuperseded) {
			// Já fomos superados por um claim posterior: não há nada a largar e largar
			// tarde não pode expulsar o novo detentor. A posse local já está fechada.
			return nil
		}
		return err
	}
	return nil
}

// Graph devolve o construtor de grafo desta posse: RE-HIDRATADO a partir do log
// ([orchestrator.RebuildDAG]) e com todas as escritas encaminhadas pelo appender
// fenced. É a única via deste pacote para escrever topologia ou estado por-nó
// (ADR-023 §2.3).
//
// # Porque a re-hidratação é obrigatória e não opcional
//
// `orchestrator.NewGraphBuilder` parte de um DAG VAZIO. Sobre um run que já existe
// no log, isso é o «builder cego» que o `ErrLogAhead` de AOS-025 documenta: `AddNode`
// e `MarkRunning` devolvem `nil`, ZERO eventos novos entram, e o chamador fica com a
// ilusão de retoma — depois admite arestas cegas às que já estão duráveis, o log fica
// com `a→b` E `b→a`, e o `RebuildDAG` falha PARA SEMPRE (é função pura sobre um log
// append-only: não há reparação em banda).
//
// Aqui não há via que produza esse builder. Um componente que tome posse a meio
// re-hidrata por construção, porque construir de outra maneira não é possível a
// partir deste pacote.
func (t *Tenure) Graph(ctx context.Context, producer eventstore.Producer, opts ...orchestrator.GraphOption) (*orchestrator.GraphBuilder, error) {
	t.mu.RLock()
	released := t.released
	t.mu.RUnlock()
	if released {
		return nil, fmt.Errorf("%w: run %s", ErrNotHeld, t.runID)
	}
	return newRehydratedGraph(ctx, t, producer, opts...)
}

// heartbeatLoop mantém a posse viva até ctx terminar ou stop fechar, e chama onLost
// quando a posse se perde (superada/expirada). Exportado como [Tenure.Keep].
func (t *Tenure) heartbeatLoop(ctx context.Context, every time.Duration, stop <-chan struct{}, onLost func(error)) {
	if every <= 0 {
		return
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-tick.C:
			if err := t.Heartbeat(ctx); err != nil {
				if onLost != nil {
					onLost(err)
				}
				return
			}
		}
	}
}

// Keep mantém a posse viva em segundo plano, renovando a cada `every`. Devolve uma
// função que PÁRA o renovador e AGUARDA a sua saída (join) — nunca deixa um heartbeat
// órfão a correr contra uma posse já largada, que é a corrida que o loop de serviço
// do nó fecha da mesma maneira.
//
// `onLost` é chamado UMA vez se a posse se perder (superada por novo claim, ou
// expirada por o renovador não ter conseguido acompanhar). O chamador deve parar o
// trabalho: a partir daí as escritas seriam recusadas na mesma, mas parar cedo é o
// cancelamento cooperativo de ADR-018 §2.3.
func (t *Tenure) Keep(ctx context.Context, every time.Duration, onLost func(error)) (stop func()) {
	stopc := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t.heartbeatLoop(ctx, every, stopc, onLost)
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(stopc) })
		<-done
	}
}
