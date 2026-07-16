package sandbox

import (
	"errors"
	"strings"
	"testing"
)

// TestConstructorGuards prova as pré-condições fail-closed dos construtores.
func TestConstructorGuards(t *testing.T) {
	if _, err := NewLauncher(nil); !errors.Is(err, ErrNilDriver) {
		t.Fatalf("NewLauncher(nil) = %v, esperado ErrNilDriver", err)
	}
	launcher, _ := NewLauncher(NewFakeDriver())
	store := newStore(t)
	rm := newPermitMonitor(store)

	if _, err := NewMediatedLauncher(nil, launcher, "t"); !errors.Is(err, ErrNilMonitor) {
		t.Fatalf("esperado ErrNilMonitor, got %v", err)
	}
	if _, err := NewMediatedLauncher(rm, nil, "t"); !errors.Is(err, ErrNilDriver) {
		t.Fatalf("esperado ErrNilDriver, got %v", err)
	}
	if _, err := NewMediatedLauncher(rm, launcher, ""); !errors.Is(err, ErrEmptyToolID) {
		t.Fatalf("esperado ErrEmptyToolID, got %v", err)
	}

	// Registo bem-sucedido expõe o ToolID; um segundo registo no mesmo id falha
	// (o RM é imutável no registo).
	ml, err := NewMediatedLauncher(rm, launcher, "sandbox.exec")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}
	if ml.ToolID() != "sandbox.exec" {
		t.Fatalf("ToolID() = %q", ml.ToolID())
	}
	if _, err := NewMediatedLauncher(rm, launcher, "sandbox.exec"); err == nil {
		t.Fatal("esperado erro ao re-registar o mesmo toolID")
	}
}

// TestDeniedError_Message prova que o erro de negação é atribuível (efeito/código).
func TestDeniedError_Message(t *testing.T) {
	e := &DeniedError{Effect: "deny", Code: "E_DENIED_BY_HOOK", Reason: "negado no teste"}
	msg := e.Error()
	for _, want := range []string{"deny", "E_DENIED_BY_HOOK", "negado no teste"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("mensagem %q nao contem %q", msg, want)
		}
	}
}
