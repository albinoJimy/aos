package broker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/substrate/sandbox"
)

const ttl = 60 * time.Second

// leaseIDFromHandle resolve o id de lease NÃO-SECRETO (gestão/auditoria) a partir
// do handle OPACO via o store server-side. O leaseID NÃO é derivável do handle (o
// handle é alta-entropia, BRK-01); um operador obtê-lo-ia do registo da troca no
// Event Store. Aqui o teste consulta o store directamente (mesmo pacote).
func leaseIDFromHandle(st *stack, h Handle) string {
	l, ok := st.broker.store.get(h)
	if !ok {
		return ""
	}
	return l.ID
}

// TestTTL_ExpiraAutomaticamente prova que a credencial expira no TTL: dentro do
// TTL a injecção server-side ocorre; passado o TTL a injecção falha fail-closed e
// NÃO entrega o valor.
func TestTTL_ExpiraAutomaticamente(t *testing.T) {
	st := newStack(t, ttl)
	inj, err := st.broker.NewInjector(st.guest)
	if err != nil {
		t.Fatalf("NewInjector: %v", err)
	}
	h, err := st.broker.Exchange(context.Background(), request("run-1", provInScopeCap))
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	inst := sandbox.Instance{ID: "vm-1"}

	// dentro do TTL: injecta.
	st.clock.advance(ttl - time.Second)
	if err := inj.Inject(context.Background(), string(h), inst); err != nil {
		t.Fatalf("injeccao dentro do TTL falhou: %v", err)
	}
	if st.guest.Injections() != 1 {
		t.Fatalf("esperado 1 injeccao, obtido %d", st.guest.Injections())
	}

	// no/after TTL: expira (ExpiresAt não é mais "depois de now").
	st.clock.advance(2 * time.Second) // total = ttl+1s
	if err := inj.Inject(context.Background(), string(h), inst); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("esperado ErrLeaseExpired, obtido %v", err)
	}
	if st.guest.Injections() != 1 {
		t.Fatalf("injeccao apos expiracao: %d", st.guest.Injections())
	}
}

// TestRevogacao_CortaAcessoImediatamente prova que a revogação por id de lease
// corta o acesso na hora seguinte, mesmo dentro do TTL.
func TestRevogacao_CortaAcessoImediatamente(t *testing.T) {
	st := newStack(t, time.Hour) // TTL longo: isola o efeito da revogação
	inj, err := st.broker.NewInjector(st.guest)
	if err != nil {
		t.Fatalf("NewInjector: %v", err)
	}
	h, err := st.broker.Exchange(context.Background(), request("run-1", provInScopeCap))
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	inst := sandbox.Instance{ID: "vm-1"}

	if ok := st.broker.Revoke(leaseIDFromHandle(st, h)); !ok {
		t.Fatal("Revoke devia encontrar a lease")
	}
	if err := inj.Inject(context.Background(), string(h), inst); !errors.Is(err, ErrLeaseRevoked) {
		t.Fatalf("esperado ErrLeaseRevoked, obtido %v", err)
	}
	if st.guest.Injections() != 0 {
		t.Fatalf("injeccao apos revogacao: %d", st.guest.Injections())
	}

	// revogar um id desconhecido é seguro (false, sem panic).
	if st.broker.Revoke("lease-inexistente") {
		t.Error("Revoke de id desconhecido devia devolver false")
	}
}
