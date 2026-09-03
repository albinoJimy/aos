package state

// AOS-301 — LER O ESTADO NÃO PODE ESPERAR PELA ESCRITA.
//
// O defeito: `Transition` fazia `m.mu.Lock()` com `defer` e persistia DENTRO dessa secção;
// `Current()` e `EnteredAt()` tomavam o MESMO mutex. Um Append lento prendia quem só queria LER.
//
// Isto foi medido de fora, ao escrever AOS-291: a primeira versão de
// `TestAOS291_AppendBloqueadoNaoPrendeOMutexDoDisjuntor` usava o wall-clock por omissão e
// FALHAVA — `Snapshot` esperava pelo append, mas por `machine.mu`, não por `b.mu`. Aquele ticket
// isolou o disjuntor injectando uma fonte independente e declarou o resto como limite; este
// fecha-o na máquina, que é onde vive.
//
// E não era só o Append. A secção crítica cobria QUATRO fontes de espera: a consulta à
// [FencingAuthority] (rede), o Append (rede/disco), o `span.End()` (`Exporter.Export`, síncrono)
// e o callback do [TransitionObserver], que é código de quem compõe o nó. `Rebuild` fazia
// `store.Read` sob o mesmo mutex — uma quinta.
//
// # O QUE MUDA, E O QUE NÃO PODE MUDAR
//
// Passam a existir dois locks. `mu` SERIALIZA as mutações e continua a tornar a validação
// (`IsValidTransition` contra o estado corrente) atómica face à escrita — a propriedade de que
// AOS-291 passou a depender ao largar o lock do disjuntor, e que `TestAOS301_...Atomica` abaixo
// fixa. `estadoMu` protege só os três campos mutáveis e nunca é detido durante I/O.
//
// # PORQUE ESTES TESTES NÃO MEDEM TEMPO
//
// Mesmo argumento de AOS-291: um teste que afirmasse «Current() demorou menos de X» seria uma
// aposta na máquina que o corre. Estes prendem o Append num canal e, enquanto ele está preso,
// exigem que a leitura COMPLETE. Antes não completava. O veredicto é binário.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// aos301Limite converte um deadlock num diagnóstico legível, em vez de deixar a suite bater no
// timeout global do `go test`. Não mede desempenho.
const aos301Limite = 5 * time.Second

// storeQuePrende bloqueia UM Append — o primeiro depois de `armar` — até o teste o libertar.
type storeQuePrende struct {
	*eventstore.Store
	armado   bool
	entrou   chan struct{}
	libertar chan struct{}
}

func (s *storeQuePrende) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	if s.armado {
		s.armado = false
		close(s.entrou)
		<-s.libertar
	}
	return s.Store.Append(ctx, streamID, in, opts...)
}

// completaComOAppendPreso corre `op` numa goroutine e exige que termine ENQUANTO o Append ainda
// está bloqueado. Com o código anterior não terminava: ficava à espera de `m.mu`, que só se
// libertaria depois do Append — que só se liberta na linha a seguir a esta.
func completaComOAppendPreso(t *testing.T, nome string, op func()) {
	t.Helper()
	feito := make(chan struct{})
	go func() { op(); close(feito) }()
	select {
	case <-feito:
	case <-time.After(aos301Limite):
		t.Fatalf("%s NAO completou com o Append ainda bloqueado — a maquina voltou a persistir com o mutex de LEITURA detido (AOS-301)", nome)
	}
}

// TestAOS301_UmAppendPresoNaoPrendeCurrentNemEnteredAt é a AC2, no molde de
// `breaker/aos291_seccao_critica_test.go`.
func TestAOS301_UmAppendPresoNaoPrendeCurrentNemEnteredAt(t *testing.T) {
	ctx := context.Background()
	clk := newManualClock()
	base := newStore(t)
	sp := &storeQuePrende{Store: base, entrou: make(chan struct{}), libertar: make(chan struct{})}
	m := mustMachine(t, sp, "run-aos301", WithClock(clk))

	// O claim inicial corre com o store NORMAL: o que fica preso tem de ser o Append da
	// transição sob teste, e não o de arranque.
	if err := m.Transition(ctx, Running, tok); err != nil {
		t.Fatalf("claim ready→running: %v", err)
	}
	antes := m.Current()
	entradaAntes := m.EnteredAt()

	sp.armado = true
	transitou := make(chan error, 1)
	go func() { transitou <- m.Transition(ctx, Paused, TransitionEvent{Reason: "teste"}) }()

	select {
	case <-sp.entrou:
	case <-time.After(aos301Limite):
		t.Fatal("o Append da transicao nunca foi alcancado — o teste nao esta a exercitar nada")
	}

	// AS DUAS ASSERÇÕES QUE IMPORTAM.
	var visto State
	completaComOAppendPreso(t, "Current()", func() { visto = m.Current() })
	var vistoEm time.Time
	completaComOAppendPreso(t, "EnteredAt()", func() { vistoEm = m.EnteredAt() })

	// E o que se lê é o estado AINDA EM VIGOR, não um estado a meio. A máquina só avança depois
	// do commit durável, pelo que durante a janela de I/O o estado correcto é o anterior.
	if visto != antes {
		t.Errorf("durante a escrita, Current()=%q; quero o estado ainda em vigor (%q) — a maquina nao pode publicar um estado que ainda nao esta durável", visto, antes)
	}
	if !vistoEm.Equal(entradaAntes) {
		t.Errorf("durante a escrita, EnteredAt()=%v; quero %v (o par estado/instante tem de ser coerente)", vistoEm, entradaAntes)
	}

	close(sp.libertar)
	if err := <-transitou; err != nil {
		t.Fatalf("a transicao devia concluir depois de libertado o Append: %v", err)
	}
	if got := m.Current(); got != Paused {
		t.Fatalf("depois do commit, Current()=%q, quero paused", got)
	}
}

// TestAOS301_AValidacaoContinuaATOMICAFaceAEscrita é a AC3, e é a razão para NÃO ter simplesmente
// tirado a persistência do lock.
//
// `mu` continua detido do princípio ao fim de cada mutação, pelo que duas transições concorrentes
// nunca intercalam validação e escrita. Sem isso, ambas leriam `from=running`, ambas passariam
// `IsValidTransition`, e ambas escreveriam — que é precisamente a idempotência de que o disjuntor
// passou a depender em AOS-291 ao largar o seu próprio lock.
//
// A prova: dois `Transition` concorrentes a partir de `running` para `paused` e `complete`. Os
// dois pares estão na tabela, mas o par que sobra NUNCA está: de `paused` só se vai para
// `running`, e `complete` é terminal absorvente. Logo exactamente um tem de vencer e o outro tem
// de ver o estado JÁ avançado e ser recusado — se a validação não fosse atómica face à escrita,
// ambos leriam `from=running` e ambos escreveriam.
func TestAOS301_AValidacaoContinuaATOMICAFaceAEscrita(t *testing.T) {
	ctx := context.Background()
	for i := 0; i < 200; i++ {
		st := newStore(t)
		m := mustMachine(t, st, "run-aos301-atomico", WithClock(newManualClock()))
		if err := m.Transition(ctx, Running, tok); err != nil {
			t.Fatalf("claim: %v", err)
		}

		res := make(chan error, 2)
		arrancar := make(chan struct{})
		for _, destino := range []State{Paused, Complete} {
			go func(to State) {
				<-arrancar
				res <- m.Transition(ctx, to, TransitionEvent{Reason: "corrida"})
			}(destino)
		}
		close(arrancar)

		var okN, recusadas int
		for range 2 {
			switch err := <-res; {
			case err == nil:
				okN++
			case errors.Is(err, ErrInvalidTransition):
				recusadas++
			default:
				t.Fatalf("erro inesperado na corrida: %v", err)
			}
		}
		if okN != 1 || recusadas != 1 {
			t.Fatalf("iteracao %d: %d transicoes aceites e %d recusadas; quero exactamente 1 e 1 — a validacao deixou de ser atomica face a escrita e o par running→{paused,complete} passou duas vezes", i, okN, recusadas)
		}
	}
}

// TestAOS301_UmRebuildLentoNaoPrendeALeitura fecha a quinta fonte de espera, que o ticket não
// nomeava: `Rebuild` faz `store.Read` — I/O — e antes fazia-o com o mesmo mutex das leituras.
func TestAOS301_UmRebuildLentoNaoPrendeALeitura(t *testing.T) {
	ctx := context.Background()
	base := newStore(t)
	sl := &storeQueLeDevagar{Store: base, entrou: make(chan struct{}), libertar: make(chan struct{})}
	m := mustMachine(t, sl, "run-aos301-rebuild", WithClock(newManualClock()))
	if err := m.Transition(ctx, Running, tok); err != nil {
		t.Fatalf("claim: %v", err)
	}

	sl.armado = true
	feito := make(chan error, 1)
	go func() { _, err := m.Rebuild(ctx); feito <- err }()

	select {
	case <-sl.entrou:
	case <-time.After(aos301Limite):
		t.Fatal("o Read do Rebuild nunca foi alcancado")
	}
	completaComOAppendPreso(t, "Current() com o Read do Rebuild preso", func() { _ = m.Current() })

	close(sl.libertar)
	if err := <-feito; err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if got := m.Current(); got != Running {
		t.Fatalf("depois do Rebuild, Current()=%q, quero running", got)
	}
}

// storeQueLeDevagar prende UMA leitura, para exercitar o Rebuild.
type storeQueLeDevagar struct {
	*eventstore.Store
	armado   bool
	entrou   chan struct{}
	libertar chan struct{}
}

func (s *storeQueLeDevagar) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	if s.armado {
		s.armado = false
		close(s.entrou)
		<-s.libertar
	}
	return s.Store.Read(ctx, streamID, fromSeq)
}
