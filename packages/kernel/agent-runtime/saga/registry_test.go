package saga

import (
	"context"
	"errors"
	"testing"
)

// noopAction é uma acção de compensação que não faz nada (para testes de registo).
func noopAction(context.Context) error { return nil }

// TestNopObserver exercita o observador default (no-op) — garante que as suas chamadas
// são inócuas e seguras (é o gancho usado quando nenhum [WithObserver] é injectado).
func TestNopObserver(t *testing.T) {
	t.Parallel()
	var o Observer = NopObserver{}
	o.Started("run", 3)
	o.Compensated("hash", true)
	o.Compensated("hash", false)
	o.Retry("hash", 1, errors.New("x"))
	o.Escalated("hash", errors.New("x"))
	o.Completed("run")
}

// TestRegisterPreservesApplicationOrder verifica que o registo preserva a ordem de
// aplicação e que Reversed devolve a ordem inversa (LIFO) exacta.
func TestRegisterPreservesApplicationOrder(t *testing.T) {
	t.Parallel()
	r := NewCompensationRegistry()
	ids := []string{"step-1", "step-2", "step-3", "step-4"}
	for _, id := range ids {
		if err := r.Register(Compensation{StepID: id, Action: noopAction}); err != nil {
			t.Fatalf("Register(%s): %v", id, err)
		}
	}
	if r.Len() != len(ids) {
		t.Fatalf("Len=%d, esperado %d", r.Len(), len(ids))
	}

	applied := r.Applied()
	for i, c := range applied {
		if c.StepID != ids[i] {
			t.Fatalf("Applied[%d]=%s, esperado %s", i, c.StepID, ids[i])
		}
	}

	reversed := r.Reversed()
	for i, c := range reversed {
		want := ids[len(ids)-1-i]
		if c.StepID != want {
			t.Fatalf("Reversed[%d]=%s, esperado %s (LIFO)", i, c.StepID, want)
		}
	}
}

// TestRegisterIdempotentByStepID verifica que re-registar o mesmo step_id NÃO duplica
// a entrada nem desloca a sua posição na ordem (essencial para o crash-resume, em que
// o worker novo re-regista as compensações pela mesma ordem determinística).
func TestRegisterIdempotentByStepID(t *testing.T) {
	t.Parallel()
	r := NewCompensationRegistry()
	_ = r.Register(Compensation{StepID: "a", Action: noopAction, Reason: "v1"})
	_ = r.Register(Compensation{StepID: "b", Action: noopAction})
	// re-registo de "a" com nova acção/rótulo: mantém a posição (índice 0), actualiza.
	_ = r.Register(Compensation{StepID: "a", Action: noopAction, Reason: "v2"})

	if r.Len() != 2 {
		t.Fatalf("Len=%d, esperado 2 (re-registo não duplica)", r.Len())
	}
	applied := r.Applied()
	if applied[0].StepID != "a" || applied[1].StepID != "b" {
		t.Fatalf("ordem alterada pelo re-registo: %s,%s", applied[0].StepID, applied[1].StepID)
	}
	if applied[0].Reason != "v2" {
		t.Fatalf("re-registo devia actualizar a acção/rótulo, veio %q", applied[0].Reason)
	}
}

// TestRegisterValidation verifica as guardas de argumento do registo.
func TestRegisterValidation(t *testing.T) {
	t.Parallel()
	r := NewCompensationRegistry()
	if err := r.Register(Compensation{StepID: "", Action: noopAction}); !errors.Is(err, ErrEmptyStepID) {
		t.Fatalf("StepID vazio devia dar ErrEmptyStepID, veio %v", err)
	}
	if err := r.Register(Compensation{StepID: "x", Action: nil}); !errors.Is(err, ErrNilAction) {
		t.Fatalf("Action nil devia dar ErrNilAction, veio %v", err)
	}
	if _, ok := r.Lookup("x"); ok {
		t.Fatalf("registo inválido não devia deixar entrada")
	}
}

// TestLookup verifica a consulta por step_id.
func TestLookup(t *testing.T) {
	t.Parallel()
	r := NewCompensationRegistry()
	_ = r.Register(Compensation{StepID: "step-7", Action: noopAction, Reason: "undo"})
	c, ok := r.Lookup("step-7")
	if !ok || c.Reason != "undo" {
		t.Fatalf("Lookup(step-7)=%v,%v", c, ok)
	}
	if _, ok := r.Lookup("ausente"); ok {
		t.Fatalf("Lookup de step ausente devia ser falso")
	}
}

// TestCompensationKey verifica a derivação da chave de compensação e as suas guardas.
func TestCompensationKey(t *testing.T) {
	t.Parallel()
	key, err := CompensationKey("run-1", "step-3")
	if err != nil {
		t.Fatalf("CompensationKey: %v", err)
	}
	if key != "run-1:comp-step-3" {
		t.Fatalf("chave=%q, esperado run-1:comp-step-3", key)
	}
	if _, err := CompensationKey("run-1", ""); !errors.Is(err, ErrEmptyStepID) {
		t.Fatalf("step vazio devia dar ErrEmptyStepID, veio %v", err)
	}
	// run_id com ':' é rejeitado a montante pela IdempotencyKey de AOS-014.
	if _, err := CompensationKey("run:x", "step-1"); err == nil {
		t.Fatalf("run_id com ':' devia ser rejeitado")
	}
}
