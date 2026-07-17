package actiondedup_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aos-ref/kernel/agent-runtime/breaker"
	"github.com/aos-ref/kernel/agent-runtime/breaker/actiondedup"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// runningMachine constrói uma máquina durável já em running (para exercitar a construção do
// breaker directamente, sem passar pelo t.Fatal de [runningBreaker]).
func runningMachine(t *testing.T) *state.Machine {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	m, err := state.NewMachine(store, "run-inert")
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	if err := m.Transition(context.Background(), state.Running, state.TransitionEvent{Token: state.Uint64Token(1)}); err != nil {
		t.Fatalf("ready→running: %v", err)
	}
	return m
}

// TestInertDetectorRejectedByBreaker é a regressão do achado AOS-081 (MEDIUM): um detector
// INERTE — construído de um Registry com Config{} (Threshold=0, o zero-value) — é não-nil e o
// seu MadeProgress é sempre true. Antes da porta [breaker.EnabledSource] isto passava a
// nil-check do fail-closed e produzia um breaker que se julga configurado mas está CEGO ao
// sinal no_progress (50 acções idênticas nunca disparavam). Agora a construção RECUSA
// ([breaker.ErrProgressSourceInert]), fechando o buraco simetricamente com a nil-check.
func TestInertDetectorRejectedByBreaker(t *testing.T) {
	m := runningMachine(t)
	inert := actiondedup.NewRegistry(actiondedup.Config{}) // Threshold=0 ⇒ detector inerte
	provider := breaker.NewStaticThresholdProvider(breaker.Thresholds{MaxStaleIterations: 2})

	_, err := breaker.NewBreaker(m, provider, "test", breaker.WithProgressSource(inert.Source("run-inert")))
	if !errors.Is(err, breaker.ErrProgressSourceInert) {
		t.Fatalf("breaker devia recusar um detector inerte com ErrProgressSourceInert, obteve: %v", err)
	}
}

// TestArmedDetectorAcceptedByBreaker: o complemento do teste acima — um detector ARMADO
// (Threshold>0) satisfaz a cablagem fail-closed e a construção passa.
func TestArmedDetectorAcceptedByBreaker(t *testing.T) {
	m := runningMachine(t)
	armed := actiondedup.NewRegistry(actiondedup.Config{WindowSize: 5, Threshold: 3})
	provider := breaker.NewStaticThresholdProvider(breaker.Thresholds{MaxStaleIterations: 2})

	if _, err := breaker.NewBreaker(m, provider, "test", breaker.WithProgressSource(armed.Source("run-inert"))); err != nil {
		t.Fatalf("breaker devia aceitar um detector armado, obteve erro: %v", err)
	}
}

// runningBreaker constrói uma máquina durável em running e um breaker com o sinal de
// no-progress ligado (MaxStaleIterations) e a fonte de progresso cablada no detector.
func runningBreaker(t *testing.T, det *actiondedup.Detector, maxStale int) *breaker.Breaker {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	m, err := state.NewMachine(store, "run-actiondedup")
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	if err := m.Transition(context.Background(), state.Running, state.TransitionEvent{Token: state.Uint64Token(1)}); err != nil {
		t.Fatalf("ready→running: %v", err)
	}

	provider := breaker.NewStaticThresholdProvider(breaker.Thresholds{MaxStaleIterations: maxStale})
	b, err := breaker.NewBreaker(m, provider, "test", breaker.WithProgressSource(det))
	if err != nil {
		t.Fatalf("NewBreaker: %v", err)
	}
	return b
}

// TestRepeatedActionTripsBreaker é a INTEGRAÇÃO exigida por AOS-081: um agente que repete a
// MESMA tool call N vezes faz o detector sinalizar ausência de progresso (MadeProgress==false)
// que, consumido pelo avaliador multi-sinal de AOS-080, contribui para o trip do breaker.
func TestRepeatedActionTripsBreaker(t *testing.T) {
	det := actiondedup.NewDetector(actiondedup.Config{WindowSize: 5, Threshold: 3})
	b := runningBreaker(t, det, 2) // trip a 2 iterações estéreis consecutivas

	const h = "sha256:same-action"
	ctx := context.Background()

	tripped := false
	var final breaker.Decision
	for i := 0; i < 6; i++ {
		det.Observe(h) // o RT observaria isto quando o execute_tool fecha
		dec, err := b.Observe(ctx)
		if err != nil {
			t.Fatalf("iteração %d: Observe erro: %v", i, err)
		}
		if dec.Trip {
			tripped = true
			final = dec
			break
		}
	}

	if !tripped {
		t.Fatal("repetir a mesma acção devia acabar por disparar o breaker")
	}
	// O sinal primário é no-progress → estado durável paused; e no-progress consta dos
	// sinais cruzados (a prova de que compõe no avaliador multi-sinal de AOS-080).
	if final.Reason != breaker.SignalNoProgress {
		t.Errorf("razão do trip = %q, esperava %q", final.Reason, breaker.SignalNoProgress)
	}
	if final.Target != state.Paused {
		t.Errorf("alvo do trip = %q, esperava %q", final.Target, state.Paused)
	}
	if !containsSignal(final.Crossed, breaker.SignalNoProgress) {
		t.Errorf("no_progress devia constar dos sinais cruzados: %v", final.Crossed)
	}
}

// TestDistinctActionsDoNotTrip: acções todas distintas nunca produzem o sinal de
// no-progress — o breaker não dispara (ausência de falsos-positivos).
func TestDistinctActionsDoNotTrip(t *testing.T) {
	det := actiondedup.NewDetector(actiondedup.Config{WindowSize: 5, Threshold: 3})
	b := runningBreaker(t, det, 2)
	ctx := context.Background()

	for i := 0; i < 30; i++ {
		det.Observe(fmt.Sprintf("sha256:action-%d", i))
		dec, err := b.Observe(ctx)
		if err != nil {
			t.Fatalf("iteração %d: Observe erro: %v", i, err)
		}
		if dec.Trip {
			t.Fatalf("iteração %d: acções distintas não deviam disparar o breaker", i)
		}
	}
}

// TestSemanticallyEquivalentArgsTripBreaker liga (a)+(c) de AOS-081 de ponta a ponta: a
// MESMA tool call com args JSON SEMANTICAMENTE EQUIVALENTES mas formatados de forma diferente
// (chaves reordenadas, espaçamento distinto) produz — via [otelgenai.CanonicalToolCallHash] —
// o MESMO hash, pelo que o detector as conta como repetição e o breaker dispara. Sem a
// normalização canónica, cada formatação daria um hash diferente (falso-negativo de dedup) e
// o breaker NUNCA dispararia. É a prova de "ausência de falsos negativos por formatação".
func TestSemanticallyEquivalentArgsTripBreaker(t *testing.T) {
	det := actiondedup.NewDetector(actiondedup.Config{WindowSize: 5, Threshold: 3})
	b := runningBreaker(t, det, 2)
	ctx := context.Background()

	const tool = "fs.read"
	// A MESMA intenção semântica, escrita de 6 formas sintacticamente diferentes.
	variants := [][]byte{
		[]byte(`{"path":"/etc/hosts","mode":"r"}`),
		[]byte(`{"mode":"r","path":"/etc/hosts"}`),
		[]byte("{ \"path\": \"/etc/hosts\",  \"mode\":\"r\" }"),
		[]byte("\n{\"mode\":\"r\",\n \"path\":\"/etc/hosts\"}\n"),
		[]byte(`{  "path" : "/etc/hosts" , "mode" : "r"  }`),
		[]byte(`{"mode":"r","path":"/etc/hosts"}`),
	}

	// Confirma primeiro que a canonicalização colapsa todas as variantes ao MESMO hash.
	base := otelgenai.CanonicalToolCallHash(tool, variants[0])
	for i, v := range variants {
		if got := otelgenai.CanonicalToolCallHash(tool, v); got != base {
			t.Fatalf("variante %d devia canonicalizar ao mesmo hash: %s != %s", i, got, base)
		}
	}

	tripped := false
	for i, v := range variants {
		det.Observe(otelgenai.CanonicalToolCallHash(tool, v))
		dec, err := b.Observe(ctx)
		if err != nil {
			t.Fatalf("variante %d: Observe erro: %v", i, err)
		}
		if dec.Trip {
			tripped = true
			break
		}
	}
	if !tripped {
		t.Fatal("args semanticamente equivalentes deviam ser deduplicados e disparar o breaker")
	}
}

// TestArrayOrderDoesNotTrip: a ordem de um array é SEMÂNTICA — args que só diferem na ordem
// do array têm hashes distintos e NÃO são deduplicados (não disparam por falsa igualdade).
func TestArrayOrderDoesNotTrip(t *testing.T) {
	det := actiondedup.NewDetector(actiondedup.Config{WindowSize: 5, Threshold: 3})
	b := runningBreaker(t, det, 2)
	ctx := context.Background()

	const tool = "batch"
	orders := [][]byte{
		[]byte(`{"ids":[1,2,3]}`),
		[]byte(`{"ids":[3,2,1]}`),
		[]byte(`{"ids":[2,1,3]}`),
		[]byte(`{"ids":[1,3,2]}`),
	}
	for i, v := range orders {
		det.Observe(otelgenai.CanonicalToolCallHash(tool, v))
		dec, err := b.Observe(ctx)
		if err != nil {
			t.Fatalf("ordem %d: Observe erro: %v", i, err)
		}
		if dec.Trip {
			t.Fatalf("ordem %d: arrays em ordens diferentes são acções distintas, não deviam disparar", i)
		}
	}
}

func containsSignal(sigs []breaker.Signal, want breaker.Signal) bool {
	for _, s := range sigs {
		if s == want {
			return true
		}
	}
	return false
}
