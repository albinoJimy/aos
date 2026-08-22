package dsar_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	dsar "github.com/aos-ref/control-plane/governance/dsar"
	audit "github.com/aos-ref/platform/audit"
	redaction "github.com/aos-ref/substrate/redaction"
)

// ---------------------------------------------------------------------------------------------
// A INVARIANTE DO /dsar/hold NÃO PODE TER EXCEPÇÕES.
//
//	um 200 do POST /dsar/hold significa que NENHUMA destruição posterior deixa de ver este hold.
//
// O #125 fechou a janela nas TRÊS vias compostas — o varredor de retenção, o `/dsar/erase` pela
// via do store de audit, e a própria colocação do hold. Verificado ao ler cada uma delas: todas
// tomam a barreira à volta de `held()`→destruição.
//
// O que ficava era uma excepção LATENTE. O [dsar.RedactionStore] está EXPORTADO, não é composto
// pelo nó, e destruía a chave de tokenização sem consultar preservação nenhuma e sem barreira. A
// única coisa que o impedia de reintroduzir o achado 1.9 por inteiro era ninguém o ter ligado.
//
// Uma invariante com uma excepção é uma invariante que ninguém consegue citar — e uma excepção
// que só não morde porque ninguém compôs aquela linha é uma armadilha, não uma garantia.
//
// PORQUE POR-STORE E NÃO NO CICLO DO FLUXO, que seria o desenho óbvio: `dsar.AuditStore` delega em
// `audit.Shredder.Shred`, que TOMA a barreira por dentro — e tem outro chamador
// (`PurgeExpired`), pelo que não a pode largar. Tomá-la também no ciclo ANINHARIA duas aquisições
// partilhadas do mesmo `RWMutex`, que bloqueiam para sempre assim que um escritor fique à espera.
// ---------------------------------------------------------------------------------------------

// holdComBarreira é um HoldOracle REAL: delega no `*audit.LegalHold`, tanto a consulta como a
// barreira. É o que o nó compõe, pela via do `*audit.Shredder`.
type holdComBarreira struct{ h *audit.LegalHold }

func (o holdComBarreira) Held(s string) bool       { return o.h.HeldSubject(s) }
func (o holdComBarreira) BeginDestruction() func() { return o.h.BeginDestruction() }

// keySourceLenta destrói devagar — é assim que se PRENDE a janela em vez de correr atrás dela.
type keySourceLenta struct {
	ks        *redaction.InMemoryKeySource
	entrou    chan struct{}
	prossegue chan struct{}
	uma       sync.Once
}

func (k *keySourceLenta) Shred(subject string) ([]byte, bool) {
	k.uma.Do(func() { close(k.entrou) })
	<-k.prossegue
	return k.ks.Shred(subject)
}

func TestRedaccaoTomaABarreiraDeDestruicao(t *testing.T) {
	holds := audit.NewLegalHold()
	lenta := &keySourceLenta{
		ks:        redaction.NewInMemoryKeySource(detRand()),
		entrou:    make(chan struct{}),
		prossegue: make(chan struct{}),
	}
	store := dsar.RedactionStore("redaction", lenta, holdComBarreira{h: holds})

	// A destruição arranca e FICA PRESA lá dentro, com a barreira tomada.
	feito := make(chan error, 1)
	go func() { feito <- store.Shred("nhi:titular") }()
	<-lenta.entrou

	// A colocação do hold TEM de esperar. Se não esperar, a barreira não está a ser tomada — e um
	// operador receberia 200 sobre material já a ser destruído.
	colocado := make(chan struct{})
	go func() {
		fim := holds.BeginPlacement()
		holds.HoldSubject("nhi:titular")
		fim()
		close(colocado)
	}()

	select {
	case <-colocado:
		t.Fatal("a colocacao do hold NAO esperou pela destruicao em curso — a barreira nao esta a " +
			"ser tomada pelo store de redaccao, e a invariante do /dsar/hold tem uma excepcao")
	case <-time.After(150 * time.Millisecond):
		// Esperado: continua bloqueada.
	}

	close(lenta.prossegue)
	if err := <-feito; err != nil {
		t.Fatalf("a destruicao devia concluir: %v", err)
	}
	select {
	case <-colocado:
	case <-time.After(2 * time.Second):
		t.Fatal("a colocacao NUNCA foi libertada — a barreira nao larga")
	}
}

// TestRedaccaoRECUSASobLegalHold — o store passa a defender-se sozinho.
//
// Até 2026-08-22 devolvia `nil` incondicionalmente: destruía a chave de tokenização de um titular
// sob preservação. O fluxo protegia-o por re-consultar o hold antes de cada store, mas isso é
// protecção de FORA — e é exactamente a que tem a janela.
func TestRedaccaoRECUSASobLegalHold(t *testing.T) {
	holds := audit.NewLegalHold()
	holds.HoldSubject("nhi:retido")
	ks := redaction.NewInMemoryKeySource(detRand())
	store := dsar.RedactionStore("redaction", ks, holdComBarreira{h: holds})

	if err := store.Shred("nhi:retido"); !errors.Is(err, audit.ErrLegalHold) {
		t.Errorf("um titular sob legal hold devia dar ErrLegalHold; veio %v", err)
	}
	// CONTROLO: e um titular SEM hold é mesmo destruído. Sem este ramo, «recusar sempre» passaria
	// no teste acima — e um store que nunca apaga não satisfaz apagamento nenhum.
	if err := store.Shred("nhi:livre"); err != nil {
		t.Errorf("um titular sem hold devia ser destruido; veio %v", err)
	}
}

// TestRedaccaoSemOraculoRECUSA — fail-closed por construção.
func TestRedaccaoSemOraculoRECUSA(t *testing.T) {
	store := dsar.RedactionStore("redaction", redaction.NewInMemoryKeySource(detRand()), nil)
	if err := store.Shred("nhi:x"); !errors.Is(err, audit.ErrLegalHold) {
		t.Errorf("sem oraculo de preservacao o store devia RECUSAR; veio %v — destruir sem saber "+
			"e o pior desfecho possivel", err)
	}
}

// TestABarreiraCHEGAPELOShredder é o teste que faltava, e a mutação B4 revelou-o.
//
// Os testes acima passam o `*audit.LegalHold` como oráculo — directo. A composição REAL passa o
// `*audit.Shredder`, que é quem satisfaz o [dsar.HoldOracle] e que DELEGA a barreira. Uma mutação
// que fizesse essa delegação devolver uma barreira falsa não caía em nenhum deles: o store
// continuava a "tomar" uma barreira que não tranca nada.
func TestABarreiraCHEGAPELOShredder(t *testing.T) {
	holds := audit.NewLegalHold()
	shredder := audit.NewShredder(audit.NewInMemoryKeyVault(detRand()), holds, audit.NewRetentionPolicy(nil))
	lenta := &keySourceLenta{
		ks:        redaction.NewInMemoryKeySource(detRand()),
		entrou:    make(chan struct{}),
		prossegue: make(chan struct{}),
	}
	store := dsar.RedactionStore("redaction", lenta, shredder)

	feito := make(chan error, 1)
	go func() { feito <- store.Shred("nhi:pelo-shredder") }()
	<-lenta.entrou

	colocado := make(chan struct{})
	go func() {
		fim := holds.BeginPlacement()
		holds.HoldSubject("nhi:pelo-shredder")
		fim()
		close(colocado)
	}()

	select {
	case <-colocado:
		t.Fatal("a colocacao NAO esperou — a barreira que o Shredder delega nao tranca nada, e o " +
			"store pensa que a tomou")
	case <-time.After(150 * time.Millisecond):
	}
	close(lenta.prossegue)
	if err := <-feito; err != nil {
		t.Fatalf("a destruicao devia concluir: %v", err)
	}
	select {
	case <-colocado:
	case <-time.After(2 * time.Second):
		t.Fatal("a colocacao NUNCA foi libertada")
	}
}
