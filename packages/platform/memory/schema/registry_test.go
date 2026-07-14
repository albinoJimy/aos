package schema_test

import (
	"errors"
	"testing"

	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/schema"
)

func v(s string) schema.Version { x, _ := schema.ParseVersion(s); return x }

func TestDefaultClassRegistryAnchors(t *testing.T) {
	t.Parallel()
	r := schema.DefaultClassRegistry()
	for _, c := range domain.AllClasses() {
		got, ok := r.Current(c)
		if !ok {
			t.Fatalf("classe %s sem versao no default", c)
		}
		if got != v("1.0.0") {
			t.Fatalf("classe %s = %s, quero 1.0.0", c, got)
		}
	}
}

func TestRegisterMonotonic(t *testing.T) {
	t.Parallel()
	r := schema.DefaultClassRegistry()

	// Avanço MINOR válido.
	if err := r.Register(domain.ClassSemantic, v("1.1.0")); err != nil {
		t.Fatalf("Register 1.1.0: %v", err)
	}
	if got, _ := r.Current(domain.ClassSemantic); got != v("1.1.0") {
		t.Fatalf("current = %s, quero 1.1.0", got)
	}

	// Regressão: rejeitada, corrente mantém-se.
	if err := r.Register(domain.ClassSemantic, v("1.0.5")); !errors.Is(err, schema.ErrNonMonotonic) {
		t.Fatalf("regressao: erro = %v, quero ErrNonMonotonic", err)
	}
	if got, _ := r.Current(domain.ClassSemantic); got != v("1.1.0") {
		t.Fatalf("apos regressao rejeitada current = %s, quero 1.1.0", got)
	}

	// Igual: também rejeitada (estritamente crescente).
	if err := r.Register(domain.ClassSemantic, v("1.1.0")); !errors.Is(err, schema.ErrNonMonotonic) {
		t.Fatalf("igual: erro = %v, quero ErrNonMonotonic", err)
	}

	// MAJOR válido.
	if err := r.Register(domain.ClassSemantic, v("2.0.0")); err != nil {
		t.Fatalf("Register 2.0.0: %v", err)
	}
}

func TestRegisterInvalidClass(t *testing.T) {
	t.Parallel()
	r := schema.NewClassRegistry()
	if err := r.Register(domain.MemoryClass("bogus"), v("1.0.0")); !errors.Is(err, schema.ErrInvalidClass) {
		t.Fatalf("erro = %v, quero ErrInvalidClass", err)
	}
	if err := r.Anchor(domain.MemoryClass("bogus"), v("1.0.0")); !errors.Is(err, schema.ErrInvalidClass) {
		t.Fatalf("Anchor bogus: erro = %v, quero ErrInvalidClass", err)
	}
}

// TestRevertControlled cobre a reversão CONTROLADA da versão de schema de uma
// classe (operação inversa de Register usada no rollback de migração): só regride
// com compare-and-swap correcto e nunca AVANÇA.
func TestRevertControlled(t *testing.T) {
	t.Parallel()
	r := schema.DefaultClassRegistry()

	// Avança semantic para 1.1.0 e reverte-a de volta a 1.0.0 (CAS correcto).
	if err := r.Register(domain.ClassSemantic, v("1.1.0")); err != nil {
		t.Fatalf("Register 1.1.0: %v", err)
	}
	if err := r.Revert(domain.ClassSemantic, v("1.0.0"), v("1.1.0")); err != nil {
		t.Fatalf("Revert valido: %v", err)
	}
	if got, _ := r.Current(domain.ClassSemantic); got != v("1.0.0") {
		t.Fatalf("apos revert current = %s, quero 1.0.0", got)
	}

	// CAS errado (corrente != esperada): rejeitado, corrente mantém-se.
	if err := r.Revert(domain.ClassSemantic, v("0.9.0"), v("2.0.0")); !errors.Is(err, schema.ErrRevertMismatch) {
		t.Fatalf("Revert CAS errado: erro = %v, quero ErrRevertMismatch", err)
	}
	if got, _ := r.Current(domain.ClassSemantic); got != v("1.0.0") {
		t.Fatalf("apos revert rejeitado current = %s, quero 1.0.0", got)
	}

	// Revert nunca AVANÇA (to > corrente): rejeitado.
	if err := r.Revert(domain.ClassSemantic, v("1.5.0"), v("1.0.0")); !errors.Is(err, schema.ErrRevertMismatch) {
		t.Fatalf("Revert a avancar: erro = %v, quero ErrRevertMismatch", err)
	}

	// Classe inválida: fail-closed.
	if err := r.Revert(domain.MemoryClass("bogus"), v("1.0.0"), v("1.0.0")); !errors.Is(err, schema.ErrInvalidClass) {
		t.Fatalf("Revert classe invalida: erro = %v, quero ErrInvalidClass", err)
	}

	// Classe sem versão registada: rejeitado (nada a reverter).
	empty := schema.NewClassRegistry()
	if err := empty.Revert(domain.ClassWorking, v("1.0.0"), v("1.0.0")); !errors.Is(err, schema.ErrRevertMismatch) {
		t.Fatalf("Revert sem versao: erro = %v, quero ErrRevertMismatch", err)
	}
}

func TestAnchorOnceThenRegister(t *testing.T) {
	t.Parallel()
	r := schema.NewClassRegistry()
	if err := r.Anchor(domain.ClassWorking, v("3.0.0")); err != nil {
		t.Fatalf("Anchor: %v", err)
	}
	// Re-ancorar rejeitado (já tem versao).
	if err := r.Anchor(domain.ClassWorking, v("3.1.0")); !errors.Is(err, schema.ErrNonMonotonic) {
		t.Fatalf("re-anchor: erro = %v, quero ErrNonMonotonic", err)
	}
	// Avançar por Register funciona.
	if err := r.Register(domain.ClassWorking, v("3.1.0")); err != nil {
		t.Fatalf("Register apos anchor: %v", err)
	}
}
