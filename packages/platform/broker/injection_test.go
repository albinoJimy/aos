package broker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/substrate/sandbox"
)

// TestInject_ServerSide_AgenteNuncaVeSegredo prova o fluxo de injecção
// server-side: o handle opaco resolve-se numa credencial injectada no guest, e o
// agente (o chamador de Exchange/Inject) NUNCA recebe o valor.
func TestInject_ServerSide(t *testing.T) {
	st := newStack(t, time.Minute)
	inj, err := st.broker.NewInjector(st.guest)
	if err != nil {
		t.Fatalf("NewInjector: %v", err)
	}
	h, err := st.broker.Exchange(context.Background(), request("run-1", provInScopeCap))
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if err := inj.Inject(context.Background(), string(h), sandbox.Instance{ID: "vm-1"}); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if st.guest.Injections() != 1 {
		t.Fatalf("esperado 1 injeccao server-side, obtido %d", st.guest.Injections())
	}
	// a injecção correlaciona-se pelo ref NÃO-SECRETO (a Key.id()).
	if !st.guest.InjectedRef(provider + "|" + region + "|" + provInScopeCap) {
		t.Error("injeccao nao correlacionada pelo ref nao-secreto")
	}
}

func TestInject_HandleVazio_NoOp(t *testing.T) {
	st := newStack(t, time.Minute)
	inj, _ := st.broker.NewInjector(st.guest)
	if err := inj.Inject(context.Background(), "", sandbox.Instance{}); err != nil {
		t.Fatalf("handle vazio devia ser no-op: %v", err)
	}
	if st.guest.Injections() != 0 {
		t.Fatal("no-op nao devia injectar")
	}
}

func TestInject_HandleDesconhecido_FailClosed(t *testing.T) {
	st := newStack(t, time.Minute)
	inj, _ := st.broker.NewInjector(st.guest)
	err := inj.Inject(context.Background(), "h-lease-desconhecida", sandbox.Instance{ID: "vm-1"})
	if !errors.Is(err, ErrUnknownHandle) {
		t.Fatalf("esperado ErrUnknownHandle, obtido %v", err)
	}
}

func TestNewInjector_NilSink_Rejeitado(t *testing.T) {
	st := newStack(t, time.Minute)
	if _, err := st.broker.NewInjector(nil); !errors.Is(err, ErrNilGuestSink) {
		t.Fatalf("esperado ErrNilGuestSink, obtido %v", err)
	}
}

// verifica em compile-time (e documenta) que o Injector satisfaz a porta do SBX.
func TestInjector_ImplementaPortaSBX(t *testing.T) {
	st := newStack(t, time.Minute)
	inj, _ := st.broker.NewInjector(st.guest)
	var _ sandbox.CredentialInjector = inj
}
