package hitl

import (
	"context"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// TestEventStoreChallengeIssuer_ExpiryDecays é a prova DETERMINISTA da frescura-por-expiração —
// o coração da remediação F10/AOS-266. O ramo de decaimento em [EventStoreChallengeIssuer.IsChallengeIssued]
// (`!i.now().Before(exp) ⇒ continue`) só se alcança com o relógio controlado: os testes de nó não
// conseguem prová-lo porque o Bootstrap não expõe injecção de relógio. Aqui injecta-se via
// [EventStoreChallengeIssuer.WithClock] — sem sleeps reais — e avança-se o tempo para além do TTL.
//
// Prova as três respostas de IsChallengeIssued sobre UM challenge realmente emitido: reconhecido
// dentro da janela; NÃO reconhecido depois de a janela fechar (decaimento); NÃO reconhecido para
// um aprovador diferente daquele a quem foi emitido (atribuição).
func TestEventStoreChallengeIssuer_ExpiryDecays(t *testing.T) {
	ctx := context.Background()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer store.Close()

	// Relógio determinista: o emissor lê `now` por closure, logo reatribuir a variável avança o
	// tempo visto tanto na emissão (ExpiresAt = now+ttl) como na verificação, sem tocar no relógio real.
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	const ttl = time.Minute
	iss, err := NewEventStoreChallengeIssuer(store, ttl)
	if err != nil {
		t.Fatalf("NewEventStoreChallengeIssuer: %v", err)
	}
	iss.WithClock(clock)

	const scope = "4eyes:req-ttl-unit"
	const approver = "human:approver-1"

	challenge, err := iss.IssueChallenge(ctx, scope, approver)
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}

	// DENTRO do TTL (mesmo instante da emissão): reconhecido.
	if ok, err := iss.IsChallengeIssued(ctx, scope, approver, challenge); err != nil || !ok {
		t.Fatalf("dentro do TTL: ok=%v err=%v, quero (true,nil)", ok, err)
	}

	// Aprovador DIFERENTE, dentro do TTL: NÃO reconhecido — a emissão é atribuída a um aprovador.
	if ok, err := iss.IsChallengeIssued(ctx, scope, "human:outro", challenge); err != nil || ok {
		t.Fatalf("aprovador diferente: ok=%v err=%v, quero (false,nil)", ok, err)
	}

	// AVANÇA o relógio para além da expiração (ttl + 1s): a frescura DECAI — deixa de ser reconhecido,
	// ainda que o registo de emissão continue durável no Event Store (o dedup é ortogonal à frescura).
	now = now.Add(ttl + time.Second)
	if ok, err := iss.IsChallengeIssued(ctx, scope, approver, challenge); err != nil || ok {
		t.Fatalf("apos expiracao: ok=%v err=%v, quero (false,nil) — frescura decai", ok, err)
	}
}
