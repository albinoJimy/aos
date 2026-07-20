package env

import (
	"sync"

	"github.com/aos-ref/substrate/eventstore"
	testkit "github.com/aos-ref/testkit"
)

// TB é o subconjunto de [testing.TB] de que o harness precisa: registar o
// teardown (Cleanup), marcar-se como helper e falhar fail-fast (Fatalf). Um
// *testing.T (ou *testing.B) satisfá-la trivialmente. É uma interface (e não
// *testing.T directo) para que o próprio teardown seja testável — um TB de teste
// pode capturar os cleanups e disparar a via de falha sem abortar o teste-pai.
type TB interface {
	Helper()
	Cleanup(func())
	Fatalf(format string, args ...any)
}

// ===========================================================================
// EphemeralEnv — ambiente EFÉMERO de teste (AOS-110, EPIC-11)
// ===========================================================================
//
// Um harness que PROVISIONA dependências FRESCAS por execução (Event Store,
// transporte push/Bus, PDP, Vault de teste), com um lifecycle DETERMINISTA —
// Provision → Seed → uso → Teardown — e teardown GARANTIDO mesmo em falha do
// teste (via t.Cleanup, idempotente). É a fundação reutilizável pelas suites de
// domínio AOS-111..118.
//
// # Modelo de referência in-process (ZERO deps externas)
//
// A spec do EPIC-11 pede "Testcontainers ou equivalente" com dependências REAIS
// (ES replicado, transporte push, PDP, vault). No AOS os cinco componentes
// canónicos SÃO modelos in-process ZERO-DEP e deterministas (o Event Store, o
// bus, o PDP e o vault são Go puro). Logo o EQUIVALENTE coerente com todo o
// repo é este HARNESS IN-PROCESS: cada [EphemeralEnv] provisiona o SEU
// [eventstore.Store] fresco, a SUA subscrição push, o SEU FakePDP e o SEU
// FakeVault — ZERO estado partilhado entre Envs (isolamento estrutural, AC2). A
// variante de PRODUÇÃO (Testcontainers com imagens pinadas por hash) fica
// DOCUMENTADA no README.md deste pacote, atrás da mesma API de fixture.
//
// # Composição, não reinvenção (AOS-109)
//
// O harness COMPÕE os mocks/fixtures de AOS-109 (o [testkit] pai): reutiliza
// [testkit.NewEventStore] (o *eventstore.Store REAL), [testkit.NewFakePDP],
// [testkit.NewFakeBroker] e as fixtures de run_id/step_id. NÃO reimplementa o
// Event Store, o PDP nem o broker. O Vault de teste ([FakeVault]) é definido
// aqui por ser leve e não existir um equivalente exportável (o vault do broker
// é internal).

// EphemeralEnv é o conjunto de dependências efémeras provisionadas para UMA
// execução de teste. Cada campo é ou nil (dependência não pedida) ou uma
// instância FRESCA e ISOLADA — nenhuma é partilhada entre dois Envs. O teardown
// (fecho do Store, cancelamento das subscrições push) corre AUTOMATICAMENTE via
// t.Cleanup e é idempotente.
type EphemeralEnv struct {
	// EventStore é o Event Store in-memory de referência (AOS-002), fresco e
	// vazio. nil se o teste não pediu WithEventStore (nem WithBus).
	EventStore *eventstore.Store
	// Bus é o transporte PUSH sobre o EventStore (subscrições geridas, com
	// captura para asserção). nil se o teste não pediu WithBus.
	Bus *Bus
	// PDP é o Policy Decision Point determinista (permit por omissão). nil se o
	// teste não pediu WithPDP.
	PDP *testkit.FakePDP
	// Vault é o vault de teste determinista (segredos encapsulados; fail-closed).
	// nil se o teste não pediu WithVault.
	Vault *FakeVault
	// Broker é o Credential Broker determinista (opcional, AOS-070). nil se o
	// teste não pediu WithBroker.
	Broker *testkit.FakeBroker

	tb       TB
	teardown sync.Once
}

// spec acumula a declaração das dependências pedidas (padrão options). Um Env é
// construído a partir do spec resolvido por [New].
type spec struct {
	eventStore bool
	bus        bool
	pdp        bool
	vault      bool
	broker     bool
	esOpts     []eventstore.Option
}

// Option declara UMA dependência efémera que o teste precisa. As opções são
// aditivas; ver [New] para o comportamento por omissão (sem opções).
type Option func(*spec)

// WithEventStore pede um Event Store fresco. As eventstore.Option adicionais
// (ex.: WithReplicas, WithRegion) são propagadas à construção — o determinismo
// de seq/idempotency-key não depende delas.
func WithEventStore(opts ...eventstore.Option) Option {
	return func(s *spec) {
		s.eventStore = true
		s.esOpts = append(s.esOpts, opts...)
	}
}

// WithBus pede o transporte PUSH (implica um Event Store: o bus é servido pelas
// subscrições do Store). Se WithEventStore não for passado, é activado
// implicitamente.
func WithBus() Option { return func(s *spec) { s.bus = true } }

// WithPDP pede um Policy Decision Point determinista ([testkit.FakePDP]).
func WithPDP() Option { return func(s *spec) { s.pdp = true } }

// WithVault pede um vault de teste determinista ([FakeVault]).
func WithVault() Option { return func(s *spec) { s.vault = true } }

// WithBroker pede um Credential Broker determinista ([testkit.FakeBroker]).
func WithBroker() Option { return func(s *spec) { s.broker = true } }

// New PROVISIONA um ambiente efémero e regista o TEARDOWN AUTOMÁTICO via
// t.Cleanup — corre no fim do teste, INCLUSIVE em falha (t.Fatal/FailNow) ou
// panic recuperado (AC3). O teste DECLARA as dependências que precisa por
// options e recebe-as prontas (AC1):
//
//	e := env.New(t, env.WithEventStore(), env.WithBus(), env.WithPDP(), env.WithVault())
//	// e.EventStore, e.Bus, e.PDP, e.Vault já provisionados; teardown garantido.
//
// SEM opções, provisiona o conjunto CORE (Event Store + Bus + PDP + Vault) — o
// caso comum de uma suite de integração. Cada [New] devolve um Env com estado
// LIMPO, sem partilha com qualquer outro Env (AC2/AC4: corre igual local e em
// CI, sem configuração manual).
func New(tb TB, opts ...Option) *EphemeralEnv {
	tb.Helper()

	sp := &spec{}
	for _, o := range opts {
		o(sp)
	}
	if len(opts) == 0 {
		sp.eventStore, sp.bus, sp.pdp, sp.vault = true, true, true, true
	}
	if sp.bus {
		sp.eventStore = true // o bus é servido pelo Store
	}

	e := &EphemeralEnv{tb: tb}
	e.provision(sp)
	// Teardown GARANTIDO: t.Cleanup corre mesmo após t.Fatal/FailNow e após um
	// panic recuperado pelo runtime de testes. É a diferença face a um defer no
	// corpo do teste (que NÃO corre num t.Fatal de um helper noutra goroutine).
	tb.Cleanup(e.Teardown)
	return e
}

// provision arranca as dependências frescas declaradas no spec. Falha o teste
// (fail-fast) se a construção de uma dependência falhar.
func (e *EphemeralEnv) provision(sp *spec) {
	e.tb.Helper()
	if sp.eventStore {
		es, err := testkit.NewEventStore(sp.esOpts...)
		if err != nil {
			e.tb.Fatalf("env: provisionar Event Store: %v", err)
		}
		e.EventStore = es
	}
	if sp.bus {
		bus, err := newBus(e.EventStore)
		if err != nil {
			e.tb.Fatalf("env: provisionar Bus (transporte push): %v", err)
		}
		e.Bus = bus
	}
	if sp.pdp {
		e.PDP = testkit.NewFakePDP()
	}
	if sp.vault {
		e.Vault = NewFakeVault()
	}
	if sp.broker {
		e.Broker = testkit.NewFakeBroker()
	}
}

// Teardown liberta TODAS as dependências efémeras: cancela as subscrições push
// (fecha as goroutines do bus — sem leak, prova-se com -race) e FECHA o Event
// Store. É IDEMPOTENTE (sync.Once): chamá-lo explicitamente e deixá-lo correr de
// novo via t.Cleanup NÃO entra em panic nem em erro. Ordem: primeiro as
// subscrições (bus), depois o Store — o Close do Store também drena quaisquer
// subscrições remanescentes, pelo que não há recursos órfãos mesmo se o teste
// falhar a meio.
func (e *EphemeralEnv) Teardown() {
	e.teardown.Do(func() {
		if e.Bus != nil {
			e.Bus.close()
		}
		if e.EventStore != nil {
			_ = e.EventStore.Close() // idempotente do lado do Store (ErrClosed no 2º)
		}
	})
}
