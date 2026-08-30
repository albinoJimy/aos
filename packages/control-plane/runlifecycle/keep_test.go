package runlifecycle_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/runlifecycle"
	"github.com/aos-ref/kernel/agent-runtime/durable"
)

// keep_test.go — O RENOVADOR DE POSSE (ADR-023 §2.1, cancelamento cooperativo).
//
// [Tenure.Keep] é a única parte deste pacote que corre sozinha em segundo plano, e é
// a que decide se um processo que PERDE a posse pára ou continua a bater no fencing.
// Sem cancelamento cooperativo, um dono superado não fica perigoso — o log recusa-o na
// mesma — mas fica RUIDOSO e cego: continuaria a trabalhar sobre um run que já não é
// seu, e só descobriria pelo erro de cada escrita.

// ---------------------------------------------------------------------------
// TESTE — Keep RENOVA enquanto a posse é válida e PÁRA em join, sem heartbeat órfão.
// ---------------------------------------------------------------------------

func TestKeep_RenovaEParaEmJoin(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	clk := newClock()
	const runID = "run-keep"

	lm := replica(t, store, clk, "proc-a")
	ten, err := runlifecycle.Claim(ctx, store, lm, runID)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	perdas := make(chan error, 1)
	parar := ten.Keep(ctx, time.Millisecond, func(e error) { perdas <- e })

	// Espera activamente por pelo menos uma renovação — sem sleep fixo, que seria
	// frágil. O stream de lease cresce a cada `lease.renewed`.
	inicial := streamLen(t, store, "lease:"+runID)
	prazo := time.Now().Add(2 * time.Second)
	for streamLen(t, store, "lease:"+runID) == inicial && time.Now().Before(prazo) {
		time.Sleep(time.Millisecond)
	}
	if streamLen(t, store, "lease:"+runID) == inicial {
		t.Fatal("o renovador não escreveu um único lease.renewed — a posse não está a ser mantida viva")
	}

	// O join tem de esperar pela saída da goroutine: nenhuma renovação depois disto.
	parar()
	depoisDoJoin := streamLen(t, store, "lease:"+runID)
	time.Sleep(20 * time.Millisecond)
	if agora := streamLen(t, store, "lease:"+runID); agora != depoisDoJoin {
		t.Fatalf("o stream de lease cresceu de %d para %d DEPOIS do join — ficou um heartbeat órfão", depoisDoJoin, agora)
	}

	// Idempotente: parar duas vezes não bloqueia nem entra em pânico.
	parar()

	select {
	case e := <-perdas:
		t.Fatalf("onLost foi chamado com a posse ainda válida: %v", e)
	default:
	}
}

// ---------------------------------------------------------------------------
// TESTE — Keep AVISA e FECHA a posse quando ela é superada.
//
// É o cancelamento cooperativo: quem perde a partição PÁRA. Falha-antes: sem o
// `onLost` e sem o fecho da posse, o processo continuaria a trabalhar sobre um run que
// já é de outro, e cada escrita voltaria a bater no fencing.
// ---------------------------------------------------------------------------

func TestKeep_AvisaEFechaAPosseQuandoSuperada(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	clk := newClock()
	const runID = "run-keep-superado"

	a := replica(t, store, clk, "proc-a")
	ta, err := runlifecycle.Claim(ctx, store, a, runID)
	if err != nil {
		t.Fatalf("claim de A: %v", err)
	}

	perdas := make(chan error, 1)
	parar := ta.Keep(ctx, time.Millisecond, func(e error) { perdas <- e })
	defer parar()

	// O TTL esgota-se e B supera A.
	clk.advance(testTTL + time.Second)
	b := replica(t, store, clk, "proc-b")
	if _, err := runlifecycle.Claim(ctx, store, b, runID); err != nil {
		t.Fatalf("claim de B: %v", err)
	}

	select {
	case e := <-perdas:
		if !errors.Is(e, durable.ErrLeaseSuperseded) && !errors.Is(e, durable.ErrLeaseExpired) {
			t.Fatalf("onLost recebeu %v, quer ErrLeaseSuperseded ou ErrLeaseExpired", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("o renovador NÃO avisou que a posse se perdeu — não há cancelamento cooperativo")
	}

	if ta.Held() {
		t.Fatal("a posse de A continua aberta depois de o renovador descobrir a supersessão")
	}
	if _, err := ta.Append(ctx, lifecycleEvent(runID, "zombie")); err == nil {
		t.Fatal("A ainda escreve depois de o renovador ter fechado a posse")
	}
}

// ---------------------------------------------------------------------------
// TESTE — Keep com intervalo <= 0 é um no-op que não bloqueia o join.
// ---------------------------------------------------------------------------

func TestKeep_IntervaloNuloNaoBloqueia(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	lm := replica(t, store, newClock(), "proc-a")
	ten, err := runlifecycle.Claim(ctx, store, lm, "run-keep-zero")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if got := ten.RunID(); got != "run-keep-zero" {
		t.Fatalf("RunID = %q", got)
	}
	feito := make(chan struct{})
	go func() {
		defer close(feito)
		ten.Keep(ctx, 0, nil)()
	}()
	select {
	case <-feito:
	case <-time.After(2 * time.Second):
		t.Fatal("o join de um renovador desligado (intervalo <= 0) bloqueou")
	}
}
