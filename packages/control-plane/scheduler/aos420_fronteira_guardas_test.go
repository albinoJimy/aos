package scheduler_test

// AOS-420 — AS REDES DE SEGURANÇA DA FRONTEIRA, MEDIDAS.
//
// A revisão adversarial mediu duas coisas sobre a primeira versão desta correcção:
//
//  1. a fronteira SOBREVIVIA a um aumento da janela, e isso produzia OVERSUBSCRIÇÃO. Uma
//     reserva dada por expirada sob `Window` estreita volta a contar sob `Window` larga,
//     sem o relógio recuar nenhum — e a fronteira escondia-a do fold. Medido na altura:
//     TPM=100 com 120 tokens activos concedidos;
//  2. as redes de segurança que o ticket dizia existir tinham ZERO execuções na bateria —
//     removê-las deixava tudo verde.
//
// Este ficheiro responde às duas medindo o VEREDICTO, que é onde a oversubscrição aparece.
// Os testes de `cas_hotpath_test.go` medem leituras; leituras a menos são uma optimização,
// um veredicto errado é um defeito.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/scheduler"
	"github.com/aos-ref/substrate/eventstore"
)

// admissaoSobre constrói uma Admission sobre um EventLog JÁ EXISTENTE, para se poder ter
// duas instâncias a ler o mesmo stream — uma com fronteira quente, outra sem nenhuma.
func admissaoSobre(t *testing.T, log scheduler.EventLog, qp scheduler.QuotaProvider, clk *mutClock) *scheduler.Admission {
	t.Helper()
	adm, err := scheduler.NewAdmission(log, qp, scheduler.WithClock(clk.now))
	if err != nil {
		t.Fatalf("NewAdmission: %v", err)
	}
	return adm
}

func pedir(t *testing.T, ctx context.Context, adm *scheduler.Admission, k scheduler.ProviderKey, id string, custo int64) scheduler.AdmitResult {
	t.Helper()
	res, err := adm.Admit(ctx, scheduler.AdmitRequest{Key: k, Tenant: "t", RequestID: id, EstimatedTokens: custo})
	if err != nil {
		t.Fatalf("Admit(%s): %v", id, err)
	}
	return res
}

// TestAOS420_JanelaQueCresceNaoEscondeReservasActivas é o achado CRÍTICO da revisão. Sem a
// janela memorizada no cursor, o fold lê a partir de uma fronteira calculada para uma
// janela ESTREITA e não vê reservas que a janela LARGA torna activas outra vez.
func TestAOS420_JanelaQueCresceNaoEscondeReservasActivas(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clk := &mutClock{}
	clk.set(time.Unix(1_000_000, 0))

	k := scheduler.ProviderKey{Provider: "p", Model: "m", Region: "r"}
	qp := scheduler.NewStaticQuotaProvider(scheduler.ProviderLimits{TPM: 100, RPM: 1000, Window: time.Hour})
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	adm := admissaoSobre(t, es, qp, clk)

	// A reserva 30 com a janela LARGA.
	if r := pedir(t, ctx, adm, k, "a", 30); !r.Granted {
		t.Fatalf("a devia ser concedida: %+v", r)
	}

	// A janela ENCOLHE: as reservas antigas saem de vista e a fronteira avança.
	qp.SetKey(k, scheduler.ProviderLimits{TPM: 100, RPM: 1000, Window: time.Second})
	clk.advance(10 * time.Second)
	if r := pedir(t, ctx, adm, k, "b", 30); !r.Granted {
		t.Fatalf("b devia ser concedida: %+v", r)
	}
	clk.advance(time.Millisecond)
	if r := pedir(t, ctx, adm, k, "c", 30); !r.Granted {
		t.Fatalf("c devia ser concedida: %+v", r)
	}

	// A janela volta a CRESCER: A, B e C voltam todas a contar — 90 de 100.
	qp.SetKey(k, scheduler.ProviderLimits{TPM: 100, RPM: 1000, Window: time.Hour})
	clk.advance(10 * time.Second)

	if r := pedir(t, ctx, adm, k, "d", 30); r.Granted {
		t.Fatalf("d foi CONCEDIDA (headroom=%d): a fronteira sobreviveu ao aumento da janela e escondeu 90 tokens activos de um tecto de 100 — oversubscrição (AOS-420)", r.HeadroomTokens)
	}
}

// TestAOS420_FronteiraNaoMudaOVeredicto é o controlo que dá sentido ao resto: a fronteira é
// uma optimização de LEITURA, e uma optimização que mude um veredicto não é uma
// optimização. Duas instâncias sobre o MESMO stream — uma com a fronteira quente, outra
// nascida agora e portanto sem fronteira nenhuma — têm de concordar.
func TestAOS420_FronteiraNaoMudaOVeredicto(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clk := &mutClock{}
	clk.set(time.Unix(2_000_000, 0))

	k := scheduler.ProviderKey{Provider: "p", Model: "m", Region: "r"}
	qp := scheduler.NewStaticQuotaProvider(scheduler.ProviderLimits{TPM: 100, RPM: 1000, Window: time.Minute})
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	quente := admissaoSobre(t, es, qp, clk)

	for i, id := range []string{"a", "b", "c"} {
		if r := pedir(t, ctx, quente, k, id, 20); !r.Granted {
			t.Fatalf("%s devia ser concedida: %+v", id, r)
		}
		clk.advance(time.Duration(i+1) * time.Second)
	}

	// A instância FRIA lê o mesmo stream sem fronteira memorizada: leitura integral.
	fria := admissaoSobre(t, es, qp, clk)

	hq, errQ := quente.Headroom(ctx, k, "t")
	hf, errF := fria.Headroom(ctx, k, "t")
	if errQ != nil || errF != nil {
		t.Fatalf("Headroom: quente=%v fria=%v", errQ, errF)
	}
	if hq != hf {
		t.Fatalf("headroom com fronteira=%+v, sem fronteira=%+v — a fronteira MUDOU o veredicto (AOS-420)", hq, hf)
	}
}

// gatePrimeiroBloqueia deixa o PRIMEIRO Admit preso e serve os seguintes de imediato. É o
// que permite ter um despachante com um snapshot em voo enquanto outro materializa.
type gatePrimeiroBloqueia struct {
	entrou  chan struct{}
	liberta chan struct{}
	umaVez  sync.Once
	n       atomic.Int64
}

func novoGatePrimeiroBloqueia() *gatePrimeiroBloqueia {
	return &gatePrimeiroBloqueia{entrou: make(chan struct{}), liberta: make(chan struct{})}
}

func (g *gatePrimeiroBloqueia) Admit(ctx context.Context, req scheduler.AdmitRequest) (scheduler.AdmitResult, error) {
	if g.n.Add(1) == 1 {
		g.umaVez.Do(func() { close(g.entrou) })
		select {
		case <-g.liberta:
		case <-ctx.Done():
			return scheduler.AdmitResult{}, ctx.Err()
		}
	}
	return scheduler.AdmitResult{Granted: true, ReservationID: "res:" + req.RequestID}, nil
}

func (g *gatePrimeiroBloqueia) solta() { close(g.liberta) }

// TestAOS420_ReSubmissaoNaoEDespachadaComOSnapshotAntigo é o segundo achado da revisão: um
// ABA no índice. O despachante A fotografa `t1`/acme e fica dentro do Admit; B materializa
// `t1` e REMOVE o id; `t1` é re-submetido com outro tenant; A acorda e, se a reconfirmação
// só perguntar «o id ainda está lá?», despacha com o SNAPSHOT ANTIGO — dois despachos
// contra uma só reserva, e o `Dequeue` a drenar a partição errada.
func TestAOS420_ReSubmissaoNaoEDespachadaComOSnapshotAntigo(t *testing.T) {
	t.Parallel()
	base := time.Unix(3_000_000, 0)
	g := novoGatePrimeiroBloqueia()

	d := mustDispatcher(t,
		scheduler.WithAdmission(g),
		scheduler.WithDispatchClock(fixedClock(base)),
		scheduler.WithDefaultKey(testKey),
	)
	ctx := context.Background()

	if _, err := d.Submit(ctx, scheduler.Task{ID: "t1", Tenant: "acme", Class: "P0", Cost: 10}); err != nil {
		t.Fatalf("Submit inicial: %v", err)
	}

	// A fotografa `t1`/acme e fica preso no Admit.
	feitoA := make(chan scheduler.DispatchResult, 1)
	go func() {
		res, err := d.Dispatch(ctx)
		if err != nil {
			t.Errorf("Dispatch(A): %v", err)
		}
		feitoA <- res
	}()
	<-g.entrou

	// B passa à frente, materializa `t1` e tira-o do índice.
	resB, err := d.Dispatch(ctx)
	if err != nil {
		t.Fatalf("Dispatch(B): %v", err)
	}
	if !resB.Dispatched || resB.Task.Tenant != "acme" {
		t.Fatalf("B devia ter despachado t1/acme: %+v", resB)
	}

	// O MESMO id volta ao índice, com outro tenant e outro custo.
	if _, err := d.Submit(ctx, scheduler.Task{ID: "t1", Tenant: "outro", Class: "P0", Cost: 999}); err != nil {
		t.Fatalf("re-submissão: %v", err)
	}

	// A acorda com o snapshot antigo na mão.
	g.solta()
	resA := <-feitoA
	if resA.Dispatched {
		t.Fatalf("A despachou tenant=%q cost=%d sobre uma RE-SUBMISSÃO: o snapshot antigo passou a reconfirmação, e são dois despachos contra uma só reserva (AOS-420)", resA.Task.Tenant, resA.Task.Cost)
	}
	if n := d.Pending(); n != 1 {
		t.Fatalf("pendentes=%d, quero 1: a re-submissão tem de continuar por despachar", n)
	}
}
