package integration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/substrate/eventstore"
)

// newDurableApprovalStore monta a store durável sobre um Event Store real.
func newDurableApprovalStore(t *testing.T) (ApprovalStore, *eventstore.Store) {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	store, err := NewEventStoreApprovalStore(es)
	if err != nil {
		t.Fatalf("NewEventStoreApprovalStore: %v", err)
	}
	return store, es
}

func sampleGrant(id string) ApprovalGrant {
	return ApprovalGrant{
		ID:          id,
		Preview:     []byte{0xDE, 0xAD, 0xBE, 0xEF},
		Approvers:   []string{"human:alice", "human:bob"},
		DualControl: true,
		ExpiresAt:   time.Date(2026, 8, 7, 12, 15, 0, 0, time.UTC),
	}
}

// TestDurableStore_GuardaEDevolve: o caminho básico — o grant persiste e volta intacto.
func TestDurableStore_GuardaEDevolve(t *testing.T) {
	store, _ := newDurableApprovalStore(t)
	want := sampleGrant("g-1")
	if err := store.Put(context.Background(), want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := store.Consume(context.Background(), "g-1")
	if err != nil || !ok {
		t.Fatalf("Consume: ok=%t err=%v", ok, err)
	}
	if got.ID != want.ID || string(got.Preview) != string(want.Preview) ||
		got.DualControl != want.DualControl || len(got.Approvers) != 2 ||
		!got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("grant não voltou intacto: %+v", got)
	}
}

// TestDurableStore_SobreviveARestart é o PONTO desta store: os grants deixam de evaporar.
// Um novo objecto-store sobre o MESMO Event Store (o que acontece num restart do processo
// com WAL durável) continua a resolver o grant.
func TestDurableStore_SobreviveARestart(t *testing.T) {
	store, es := newDurableApprovalStore(t)
	if err := store.Put(context.Background(), sampleGrant("g-restart")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// "Restart": nova instância da store sobre o MESMO log.
	renascida, err := NewEventStoreApprovalStore(es)
	if err != nil {
		t.Fatalf("NewEventStoreApprovalStore (restart): %v", err)
	}
	got, ok, err := renascida.Consume(context.Background(), "g-restart")
	if err != nil || !ok {
		t.Fatalf("um grant devia sobreviver ao restart; ok=%t err=%v", ok, err)
	}
	if got.ID != "g-restart" {
		t.Fatalf("grant errado: %+v", got)
	}
}

// TestDurableStore_UsoUnico: a segunda tentativa não encontra nada — o consumo é uma
// RECLAMAÇÃO idempotente no log (não se apaga: reclama-se).
func TestDurableStore_UsoUnico(t *testing.T) {
	store, _ := newDurableApprovalStore(t)
	if err := store.Put(context.Background(), sampleGrant("g-once")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, ok, _ := store.Consume(context.Background(), "g-once"); !ok {
		t.Fatal("1.ª utilização devia encontrar o grant")
	}
	if _, ok, _ := store.Consume(context.Background(), "g-once"); ok {
		t.Fatal("2.ª utilização NÃO devia encontrar o grant (uso-único)")
	}
}

// TestDurableStore_UsoUnicoSobConcorrencia é a garantia que a versão in-memory dava com um
// mutex e esta dá com o DEDUP do Event Store: com N tentativas concorrentes, EXACTAMENTE
// uma vence. Sem isto, duas retomas simultâneas poderiam executar a acção aprovada 2x.
func TestDurableStore_UsoUnicoSobConcorrencia(t *testing.T) {
	store, _ := newDurableApprovalStore(t)
	if err := store.Put(context.Background(), sampleGrant("g-race")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	const n = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	vencedores := 0
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if _, ok, err := store.Consume(context.Background(), "g-race"); err == nil && ok {
				mu.Lock()
				vencedores++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if vencedores != 1 {
		t.Fatalf("EXACTAMENTE 1 tentativa devia vencer a corrida (uso-único atómico), venceram %d", vencedores)
	}
}

// TestDurableStore_GrantInexistente: um id que nunca foi emitido não devolve nada.
func TestDurableStore_GrantInexistente(t *testing.T) {
	store, _ := newDurableApprovalStore(t)
	if _, ok, err := store.Consume(context.Background(), "nunca-emitido"); ok || err != nil {
		t.Fatalf("id inexistente devia dar (nada, false, nil); ok=%t err=%v", ok, err)
	}
}

// TestDurableStore_LigadaAoBrokerFimAFim prova a store no seu contexto real: cerimónia
// four-eyes → grant DURÁVEL → verificação na mediação → uso-único.
func TestDurableStore_LigadaAoBrokerFimAFim(t *testing.T) {
	gate, reg := newFourEyesGate(t)
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")

	call := callParaAprovar()
	preview := referencemonitor.ApprovalPreview(call)
	req := FourEyesRequest{
		RequestID:           "req-duravel",
		Preview:             preview,
		RiskClass:           risk.ClassDanger,
		DualControlRequired: true,
	}
	legA := SignFourEyesLeg(privA, req, "human:alice", "sess-A", "cred-A", challenge32(t), nil)
	legB := SignFourEyesLeg(privB, req, "human:bob", "sess-B", "cred-B", challenge32(t), nil)

	store, _ := newDurableApprovalStore(t)
	broker, err := NewApprovalBroker(gate, store)
	if err != nil {
		t.Fatalf("NewApprovalBroker: %v", err)
	}
	grant, err := broker.Approve(context.Background(), "req-duravel", req, legA, legB)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if _, err := broker.VerifyApproval(context.Background(), []byte(grant.ID), preview); err != nil {
		t.Fatalf("o grant durável devia verificar: %v", err)
	}
	if _, err := broker.VerifyApproval(context.Background(), []byte(grant.ID), preview); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("reutilizar devia falhar (uso-único sobre a store durável); err=%v", err)
	}
}
