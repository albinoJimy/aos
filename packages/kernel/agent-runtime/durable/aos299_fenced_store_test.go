package durable

// AOS-299 — AS ESCRITAS DO LEDGER E DO CHECKPOINTER PASSAM A CARREGAR O FENCING TOKEN.
//
// A AC de `EPIC-02:428` — «escritas no Event Store carregam o fencing token; escritas com token
// inferior ao corrente são rejeitadas (worker obsoleto)» — estava por cumprir para as duas
// escritas do caminho de run. Só o `leaseRecord` e o `transitionRecord` a cumpriam;
// [StepLedger.Apply] e [EventStoreCheckpointer.Checkpoint] escreviam sobre o Event Store CRU.
//
// # A REGRA QUE ESTES TESTES FIXAM
//
// A propriedade não é «trouxe token?» — é «há detentor a quem este escritor possa estar a
// suceder?». O [FencedStore] decide por aí, e os quatro casos abaixo são exaustivos:
//
//   - run NUNCA reclamado, escrita sem token  ⇒ escreve (não há posse a superar);
//   - run COM detentor, escrita sem token     ⇒ recusa (quem escreve sem token não é o detentor);
//   - run COM detentor, token INFERIOR         ⇒ recusa (é a letra da AC);
//   - run COM detentor, token corrente         ⇒ escreve.
//
// A primeira regra é o que preserva a superfície de embedding: um [Bootstrap] com execução
// durável e sem serviço não tem lease manager nenhum, e recusar-lhe as escritas não fecharia
// defeito nenhum — não há concorrente.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

func aos299Entrada(stepID string) eventstore.EventInput {
	return eventstore.EventInput{Type: "work.effect", Payload: []byte(`{}`), StepID: stepID}
}

// TestAOS299_SemDetentorEscreveSemToken é a regra que preserva o embedding — e a âncora de
// não-vacuidade das outras três: sem ela, um FencedStore avariado que recusasse tudo passaria em
// todos os testes de recusa.
func TestAOS299_SemDetentorEscreveSemToken(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	m := newManager(t, store, newTestClock())
	fs, err := NewFencedStore(store, m)
	if err != nil {
		t.Fatalf("NewFencedStore: %v", err)
	}

	// Run nunca reclamado ⇒ CurrentToken = 0 ⇒ não há posse a superar.
	if _, err := fs.Append(context.Background(), "run-sem-detentor", aos299Entrada("s1")); err != nil {
		t.Fatalf("um run NUNCA reclamado nao tem detentor a superar; a escrita devia passar: %v", err)
	}
}

// TestAOS299_ComDetentorRecusaEscritaSemToken é o caso que o `StepLedger` e o
// `EventStoreCheckpointer` produziam TODAS as vezes antes deste ticket: escreviam sobre o store
// cru, sem token nenhum, num run reclamado.
func TestAOS299_ComDetentorRecusaEscritaSemToken(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	m := newManager(t, store, newTestClock())
	fs, err := NewFencedStore(store, m)
	if err != nil {
		t.Fatalf("NewFencedStore: %v", err)
	}
	const run = "run-com-detentor"
	if _, err := m.Claim(context.Background(), run); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	_, err = fs.Append(context.Background(), run, aos299Entrada("s1"))
	if !errors.Is(err, ErrStaleFencingToken) {
		t.Fatalf("escrita SEM token num run COM detentor = %v, quero ErrStaleFencingToken — era exactamente o que o ledger fazia antes de AOS-299", err)
	}
}

// TestAOS299_OTokenDoContextoEEExigido cobre os dois lados da letra da AC: o token corrente
// escreve, o inferior é rejeitado.
func TestAOS299_OTokenDoContextoEEExigido(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	clk := newTestClock()
	m := newManager(t, store, clk)
	fs, err := NewFencedStore(store, m)
	if err != nil {
		t.Fatalf("NewFencedStore: %v", err)
	}
	const run = "run-tokens"
	primeiro, err := m.Claim(context.Background(), run)
	if err != nil {
		t.Fatalf("Claim A: %v", err)
	}

	// (1) O detentor escreve.
	ctxA := ContextWithFencingToken(context.Background(), primeiro.Token)
	if _, err := fs.Append(ctxA, run, aos299Entrada("s1")); err != nil {
		t.Fatalf("o DETENTOR devia poder escrever: %v", err)
	}

	// (2) O lease de A expira e um novo claim supera-o — o cenário real de failover.
	clk.Advance(ttl + time.Nanosecond)
	segundo, err := m.Claim(context.Background(), run)
	if err != nil {
		t.Fatalf("Claim B: %v", err)
	}
	if segundo.Token <= primeiro.Token {
		t.Fatalf("premissa: o segundo claim tinha de mintar token superior (%d <= %d)", segundo.Token, primeiro.Token)
	}

	// (3) O SUPERADO deixa de escrever — a letra da AC de EPIC-02:428.
	if _, err := fs.Append(ctxA, run, aos299Entrada("s2")); !errors.Is(err, ErrStaleFencingToken) {
		t.Fatalf("escrita do worker OBSOLETO = %v, quero ErrStaleFencingToken", err)
	}
	// (4) E o novo detentor escreve.
	ctxB := ContextWithFencingToken(context.Background(), segundo.Token)
	if _, err := fs.Append(ctxB, run, aos299Entrada("s3")); err != nil {
		t.Fatalf("o novo detentor devia poder escrever: %v", err)
	}
}

// TestAOS299_OLedgerRECUSASemTokenNumRunReclamado é a prova de que a ligação chega ao consumidor
// real, e não só ao adaptador. Sem ela, os testes acima provariam que o `FencedStore` fenceia — e
// nada provaria que o `StepLedger` passou a escrever por ele.
func TestAOS299_OLedgerRECUSASemTokenNumRunReclamado(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	m := newManager(t, store, newTestClock())
	fs, err := NewFencedStore(store, m)
	if err != nil {
		t.Fatalf("NewFencedStore: %v", err)
	}
	ledger, err := NewStepLedger(fs)
	if err != nil {
		t.Fatalf("NewStepLedger: %v", err)
	}
	const run = "run-ledger-fenceado"
	lease, err := m.Claim(context.Background(), run)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	const key = "run-ledger-fenceado:s1"
	efeito := func(context.Context) (Result, error) { return Result{Payload: []byte("ok")}, nil }

	// Sem token no contexto, com o run reclamado ⇒ o ledger não escreve.
	if _, _, err := ledger.Apply(context.Background(), key, efeito); !errors.Is(err, ErrStaleFencingToken) {
		t.Fatalf("Apply sem token num run reclamado = %v, quero ErrStaleFencingToken", err)
	}
	// Com o token do detentor ⇒ escreve.
	ctx := ContextWithFencingToken(context.Background(), lease.Token)
	if _, aplicado, err := ledger.Apply(ctx, key, efeito); err != nil || !aplicado {
		t.Fatalf("Apply do detentor = (aplicado=%v, %v), quero (true, nil)", aplicado, err)
	}
}

// TestAOS299_ReadNaoEFenceado fixa a assimetria deliberada: o fencing barra um escritor superado,
// não um leitor. `RebuildLedger` relê o log no início de cada hospedagem, ANTES de o run reclamar
// seja o que for — exigir token aí impediria a reconstrução de que a própria posse depende.
func TestAOS299_ReadNaoEFenceado(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	m := newManager(t, store, newTestClock())
	fs, err := NewFencedStore(store, m)
	if err != nil {
		t.Fatalf("NewFencedStore: %v", err)
	}
	const run = "run-leitura"
	lease, err := m.Claim(context.Background(), run)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	ctx := ContextWithFencingToken(context.Background(), lease.Token)
	if _, err := fs.Append(ctx, run, aos299Entrada("s1")); err != nil {
		t.Fatalf("Append do detentor: %v", err)
	}

	// SEM token no contexto: a leitura passa na mesma.
	evs, err := fs.Read(context.Background(), run, 1)
	if err != nil {
		t.Fatalf("Read sem token devia passar (o fencing e das ESCRITAS): %v", err)
	}
	if len(evs) == 0 {
		t.Fatal("a leitura devia devolver o evento escrito")
	}
}
