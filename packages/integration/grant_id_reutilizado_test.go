package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	eventstore "github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------------------------
// A PROVA NÃO PODE NOMEAR QUEM NÃO ASSINOU.
//
// Achado 1.11 da varredura adversarial de 2026-08-21, demonstrado com execução:
//
//	cerimónia 1 (alice,bob)   -> grant persistido
//	cerimónia 2 (alice,carol) -> 200 OK
//	VerifyApproval: destrava, PROVA REGISTADA approvers=[alice bob]
//
// O `Put` fazia `Append` e IGNORAVA o `Status` que o `Consume` inspecciona. Na segunda cerimónia
// o Event Store devolvia `StatusDuplicate` — o evento não era escrito — e o `Put` devolvia `nil`
// na mesma. Dois humanos completavam uma cerimónia, recebiam 200, e a cadeia guardava OUTRO par.
//
// A amarra de preview é fail-closed, portanto NÃO há escalada de privilégio. Há sucesso falso a
// dois humanos e uma prova de autorização que mente sobre quem a deu — que, num mecanismo cujo
// propósito É o não-repúdio, é o pior desfecho possível.
// ---------------------------------------------------------------------------------------------

func storeDuravel(t *testing.T) (ApprovalStore, *eventstore.Store) {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	s, err := NewEventStoreApprovalStore(es)
	if err != nil {
		t.Fatalf("NewEventStoreApprovalStore: %v", err)
	}
	return s, es
}

func grantDe(id string, aprovadores ...string) ApprovalGrant {
	return ApprovalGrant{
		ID:          id,
		Preview:     []byte("apagar o bucket de producao"),
		Approvers:   aprovadores,
		DualControl: true,
		ExpiresAt:   time.Now().Add(time.Hour),
	}
}

func TestUmSegundoGrantComOUTROParERecusado(t *testing.T) {
	s, _ := storeDuravel(t)
	ctx := context.Background()

	if err := s.Put(ctx, grantDe("req-1.11", "human:alice", "human:bob")); err != nil {
		t.Fatalf("a primeira cerimonia devia persistir: %v", err)
	}

	// MESMO id, par DIFERENTE — a segunda cerimónia.
	err := s.Put(ctx, grantDe("req-1.11", "human:alice", "human:carol"))
	if !errors.Is(err, ErrGrantIDReused) {
		t.Fatalf("a segunda cerimonia com OUTRO par devia devolver ErrGrantIDReused, veio %v — dois "+
			"humanos recebem 200 e a cadeia guarda outro par", err)
	}

	// E A CADEIA CONTINUA A DIZER A VERDADE sobre a primeira. Sem este ramo, um `Put` que
	// SOBRESCREVESSE passaria no teste acima e o defeito só mudava de sítio.
	g, ok, cerr := s.Consume(ctx, "req-1.11")
	if cerr != nil || !ok {
		t.Fatalf("o grant da PRIMEIRA cerimonia devia continuar consumivel (ok=%v err=%v)", ok, cerr)
	}
	if len(g.Approvers) != 2 || g.Approvers[0] != "human:alice" || g.Approvers[1] != "human:bob" {
		t.Errorf("a prova registada nomeia %v — devia nomear o par que REALMENTE assinou", g.Approvers)
	}
}

// TestReemitirOMESMOGrantContinuaAserNoOp é a âncora da idempotência, e não é generosidade.
//
// O dedup do Event Store existe para isto: uma re-tentativa após timeout de rede reemite o MESMO
// grant e não pode falhar. Sem este teste, «recusar todo o duplicado» passaria no teste acima e
// transformaria uma re-tentativa banal num erro para o operador.
func TestReemitirOMESMOGrantContinuaAserNoOp(t *testing.T) {
	s, _ := storeDuravel(t)
	ctx := context.Background()
	g := grantDe("req-1.11-retry", "human:alice", "human:bob")

	if err := s.Put(ctx, g); err != nil {
		t.Fatalf("primeira emissao: %v", err)
	}
	// Re-tentativa: MESMO id, MESMO conteúdo. O `ExpiresAt` muda, e é deliberado que não conte.
	g2 := grantDe("req-1.11-retry", "human:alice", "human:bob")
	g2.ExpiresAt = time.Now().Add(2 * time.Hour)
	if err := s.Put(ctx, g2); err != nil {
		t.Errorf("reemitir o MESMO grant devia ser no-op; veio %v — uma re-tentativa apos timeout "+
			"de rede passaria a ser um erro para o operador", err)
	}
}

// TestUmGrantParaOUTRAAccaoComOMesmoIdERecusado — a preview também conta.
//
// Sem este ramo, comparar só os aprovadores deixaria passar o caso pior: o MESMO par a "aprovar"
// um efeito DIFERENTE sob um id já usado, com a cadeia a guardar o efeito antigo.
func TestUmGrantParaOUTRAAccaoComOMesmoIdERecusado(t *testing.T) {
	s, _ := storeDuravel(t)
	ctx := context.Background()

	if err := s.Put(ctx, grantDe("req-1.11-preview", "human:alice", "human:bob")); err != nil {
		t.Fatalf("primeira emissao: %v", err)
	}
	outra := grantDe("req-1.11-preview", "human:alice", "human:bob")
	outra.Preview = []byte("apagar OUTRO bucket")
	if err := s.Put(ctx, outra); !errors.Is(err, ErrGrantIDReused) {
		t.Errorf("o mesmo id com OUTRA preview devia dar ErrGrantIDReused; veio %v", err)
	}
}

// TestUmaExigenciaDeDuploControloDIFERENTEERecusada — e o duplo controlo também.
func TestUmaExigenciaDeDuploControloDIFERENTEERecusada(t *testing.T) {
	s, _ := storeDuravel(t)
	ctx := context.Background()

	if err := s.Put(ctx, grantDe("req-1.11-dual", "human:alice", "human:bob")); err != nil {
		t.Fatalf("primeira emissao: %v", err)
	}
	fraco := grantDe("req-1.11-dual", "human:alice", "human:bob")
	fraco.DualControl = false
	if err := s.Put(ctx, fraco); !errors.Is(err, ErrGrantIDReused) {
		t.Errorf("o mesmo id com exigencia de duplo controlo DIFERENTE devia dar ErrGrantIDReused; veio %v", err)
	}
}
