package testkit_test

import (
	"context"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
	tk "github.com/aos-ref/testkit"
)

// Este ficheiro é o EXEMPLO DE REFERÊNCIA citado no README do testkit (AOS-109 AC5):
// mostra como escrever um teste de DOMÍNIO — idempotência, replay e política —
// reutilizando SÓ as fixtures/mocks do testkit, sem improvisar fakes.

// TestDominio_Idempotencia demonstra o padrão IDEMPOTÊNCIA: o MESMO passo lógico
// (mesma run_id/step_id ⇒ mesma idempotency key) escrito duas vezes no Event Store
// deduplica. As fixtures fornecem a chave canónica e o ES in-memory real.
func TestDominio_Idempotencia(t *testing.T) {
	t.Parallel()
	es := tk.MustEventStore(t)
	ctx := context.Background()

	// A idempotency key deriva das fixtures — a MESMA função pura da produção.
	in := eventstore.EventInput{Type: "turn.recorded", RunID: tk.FixtureRunID, StepID: tk.FixtureStepID(1)}

	r1, err := es.Append(ctx, tk.FixtureRunID, in)
	if err != nil {
		t.Fatalf("append#1: %v", err)
	}
	r2, err := es.Append(ctx, tk.FixtureRunID, in) // retry do MESMO passo
	if err != nil {
		t.Fatalf("append#2: %v", err)
	}
	if r2.Status != eventstore.StatusDuplicate || r2.Seq != r1.Seq {
		t.Fatalf("idempotencia falhou: r1=%+v r2=%+v", r1, r2)
	}
}

// TestDominio_Replay demonstra o padrão REPLAY: reexecutar a sequência de passos
// produz step_ids ESTÁVEIS (o sequenciador é puro), pelo que o segundo "replay"
// case exactamente as mesmas chaves — a base do replay determinista (AOS-016).
func TestDominio_Replay(t *testing.T) {
	t.Parallel()
	firstRun := make([]string, 3)
	replay := make([]string, 3)
	for turn := 1; turn <= 3; turn++ {
		k, err := tk.FixtureKey(turn)
		if err != nil {
			t.Fatalf("FixtureKey(%d): %v", turn, err)
		}
		firstRun[turn-1] = k
	}
	for turn := 1; turn <= 3; turn++ {
		k, _ := tk.FixtureKey(turn)
		replay[turn-1] = k
	}
	for i := range firstRun {
		if firstRun[i] != replay[i] {
			t.Fatalf("replay divergiu no passo %d: %q != %q", i+1, firstRun[i], replay[i])
		}
	}
}

// TestDominio_Politica demonstra o padrão POLÍTICA: compõe um Reference Monitor
// (real) com um DenyHook (o duplo do PDP a negar) e prova que a decisão bloqueia o
// efeito — sem depender do motor Cedar. Trocar o DenyHook por um AllowHook exercita
// o caminho permit. O FakePDP serve o mesmo papel quando o teste é do PDP isolado.
func TestDominio_Politica(t *testing.T) {
	t.Parallel()
	// Política nega a capability pedida.
	m, sink := tk.NewMonitor(
		tk.AllowHook("identity"),
		tk.DenyHook("policy", "capability fora da allowlist"),
		tk.AllowHook("audit"),
	)
	tool := tk.NewToolSpy([]byte("efeito"), nil)
	_ = m.Register("tool.echo", tool.Func())

	d, err := m.Mediate(context.Background(), tk.BaseCall())
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if d.Permitted() {
		t.Fatal("a politica devia negar")
	}
	if tool.Called() {
		t.Fatal("o efeito nao deve ocorrer sob deny de politica")
	}
	// O deny fica no rasto de auditoria (o sink de referência).
	if sink.Count() != 1 {
		t.Fatalf("esperava 1 registo de deny, obtive %d", sink.Count())
	}

	// Espelho no PDP isolado: o FakePDP nega a mesma capability.
	pdp := tk.NewFakePDP().DenyOn(tk.BaseCall().Capability, "fora da allowlist")
	dec, _ := pdp.Decide(context.Background(), tk.PolicyInput{Capability: tk.BaseCall().Capability})
	if dec.Permitted() {
		t.Fatal("o FakePDP devia negar a capability")
	}
}
