package hitl

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// TestEventStoreNonceStore_FreshReplayDurable é o AC central de AOS-159: o nonce-store
// durável é atómico (uso-único por (scope, nonce)) E sobrevive a um restart — um nonce
// consumido continua bloqueado por uma NOVA instância sobre o mesmo Event Store.
func TestEventStoreNonceStore_FreshReplayDurable(t *testing.T) {
	ctx := context.Background()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer store.Close()

	n := NewEventStoreNonceStore(store)
	const scope = "ratid-1"
	nonce := []byte("nonce-a")

	// 1º consumo: FRESCO.
	if fresh, err := n.ConsumeNonce(ctx, scope, nonce); err != nil || !fresh {
		t.Fatalf("1º consumo: fresh=%v err=%v, quero (true,nil)", fresh, err)
	}
	// Replay do mesmo (scope, nonce): NÃO fresco.
	if fresh, err := n.ConsumeNonce(ctx, scope, nonce); err != nil || fresh {
		t.Fatalf("replay: fresh=%v err=%v, quero (false,nil)", fresh, err)
	}
	// Nonce diferente, mesmo scope: fresco.
	if fresh, err := n.ConsumeNonce(ctx, scope, []byte("nonce-b")); err != nil || !fresh {
		t.Fatalf("nonce diferente: fresh=%v err=%v, quero (true,nil)", fresh, err)
	}
	// Mesmo nonce, scope diferente: fresco (o uso-único é por (scope, nonce)).
	if fresh, err := n.ConsumeNonce(ctx, "ratid-2", nonce); err != nil || !fresh {
		t.Fatalf("scope diferente: fresh=%v err=%v, quero (true,nil)", fresh, err)
	}

	// DURABILIDADE: um NOVO nonce-store sobre o MESMO Event Store (como um processo que
	// reinicia) ainda bloqueia o nonce já consumido — o estado está no log append-only,
	// não no processo. É o que o memNonceStore in-memory NÃO garante.
	n2 := NewEventStoreNonceStore(store)
	if fresh, err := n2.ConsumeNonce(ctx, scope, nonce); err != nil || fresh {
		t.Fatalf("durável (nova instância pós-restart): fresh=%v err=%v, quero (false,nil)", fresh, err)
	}
}

// failingNonceAppender falha em cada Append — para provar o fail-closed.
type failingNonceAppender struct{}

func (failingNonceAppender) Append(context.Context, string, eventstore.EventInput, ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	return eventstore.AppendResult{}, errors.New("event store indisponível")
}

// TestEventStoreNonceStore_BackendErrorBlocks: um erro de backend NÃO é tratado como
// "fresco" — devolve (false, err), que o gate trata como bloqueio (fail-closed): na
// dúvida sobre se o nonce foi visto, NÃO se promove.
func TestEventStoreNonceStore_BackendErrorBlocks(t *testing.T) {
	n := NewEventStoreNonceStore(failingNonceAppender{})
	fresh, err := n.ConsumeNonce(context.Background(), "s", []byte("x"))
	if fresh || err == nil {
		t.Fatalf("erro de backend: fresh=%v err=%v, quero (false, err) fail-closed", fresh, err)
	}
}

// TestRatify_DurableNonce_ReplayBlocked é o AC de wiring de AOS-159: composto no
// [RatificationGate] via [WithRatifyNonceStore], o nonce-store durável bloqueia a
// RE-PROMOÇÃO da MESMA ratificação assinada — mesmo que ela seja autêntica e fresca —
// selando [ReasonRatificationReplayed]. É o que fecha o buraco de ADR-012: uma
// ratificação (identidade estável de conteúdo) não re-promove N vezes, inclusive
// depois de um rollback.
func TestRatify_DurableNonce_ReplayBlocked(t *testing.T) {
	ctx := context.Background()
	vault := newFakeVault()
	registry := NewMemApproverRegistry()
	pub := vault.provision("ratifier", 0x11)
	registry.Register("ratifier", pub, DefaultRatifyAuthority)
	sealer := audit.NewMemStore()

	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer es.Close()

	gate, err := NewRatificationGate(
		otelgenai.FailClosedGate{MinScore: 0.8},
		registry, sealer,
		WithRatifyClock(fixedClock()),
		WithRatifyNonceStore(NewEventStoreNonceStore(es)), // uso-único durável
	)
	if err != nil {
		t.Fatalf("NewRatificationGate: %v", err)
	}

	art := passingArtifact()
	signed := signRatificationFor(t, vault, "ratifier", true, art)

	// 1ª ratificação: nonce fresco ⇒ promove.
	if admit, err := gate.Ratify(ctx, art, signed); err != nil || !admit {
		t.Fatalf("1ª ratificação: admit=%v err=%v, quero promover", admit, err)
	}

	// 2ª ratificação com a MESMA assinatura+nonce: replay ⇒ bloqueado, NÃO promove.
	admit, err := gate.Ratify(ctx, art, signed)
	if err != nil {
		t.Fatalf("2ª Ratify erro inesperado: %v", err)
	}
	if admit {
		t.Fatal("replay do nonce devia bloquear a re-promoção (admit=false)")
	}
	_, params, _, ok := decisionObligation(t, sealer, "ratification:"+art.ID)
	if !ok {
		t.Fatal("decisão do replay não selada")
	}
	if params["reason"] != ReasonRatificationReplayed {
		t.Fatalf("reason=%q, quero %q", params["reason"], ReasonRatificationReplayed)
	}
}

// TestRatify_Freshness_StaleBlocked cobre a outra metade de CA3: com [WithRatifyFreshness]
// ligada, uma ratificação autêntica mas com IssuedAt FORA da janela é bloqueada como
// [ReasonRatificationStale] (limita a janela temporal em que uma assinatura promove).
func TestRatify_Freshness_StaleBlocked(t *testing.T) {
	ctx := context.Background()
	vault := newFakeVault()
	registry := NewMemApproverRegistry()
	pub := vault.provision("ratifier", 0x11)
	registry.Register("ratifier", pub, DefaultRatifyAuthority)
	sealer := audit.NewMemStore()

	gate, err := NewRatificationGate(
		otelgenai.FailClosedGate{MinScore: 0.8},
		registry, sealer,
		WithRatifyClock(fixedClock()),
		WithRatifyFreshness(time.Hour, 0), // janela de 1h, sem skew futuro
	)
	if err != nil {
		t.Fatalf("NewRatificationGate: %v", err)
	}

	art := passingArtifact()
	// Ratificação assinada mas EMITIDA há 2h — fora da janela de 1h ⇒ stale.
	stale, err := SignApproval(ctx, vault, SignedApproval{
		RequestID: art.RatificationID(),
		Approver:  "ratifier",
		Approved:  true,
		Nonce:     newNonce(t),
		IssuedAt:  fixedClock()().Add(-2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("SignApproval: %v", err)
	}

	admit, err := gate.Ratify(ctx, art, stale)
	if err != nil {
		t.Fatalf("Ratify erro inesperado: %v", err)
	}
	if admit {
		t.Fatal("ratificação fora da janela devia bloquear (admit=false)")
	}
	if _, params, _, ok := decisionObligation(t, sealer, "ratification:"+art.ID); !ok || params["reason"] != ReasonRatificationStale {
		t.Fatalf("reason=%q, quero %q", params["reason"], ReasonRatificationStale)
	}
}

// TestNewProductionRatificationGate_ForcesAntiReplay: a via sancionada RECUSA sem
// nonce-store ou sem frescura, e o gate que devolve tem o anti-replay SEMPRE ligado
// (um replay é bloqueado) — o wiring de CA2 imposto por construção.
func TestNewProductionRatificationGate_ForcesAntiReplay(t *testing.T) {
	ctx := context.Background()
	vault := newFakeVault()
	registry := NewMemApproverRegistry()
	pub := vault.provision("ratifier", 0x11)
	registry.Register("ratifier", pub, DefaultRatifyAuthority)
	sealer := audit.NewMemStore()
	eval := otelgenai.FailClosedGate{MinScore: 0.8}

	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer es.Close()
	nonces := NewEventStoreNonceStore(es)

	// Recusas fail-closed.
	if _, err := NewProductionRatificationGate(eval, registry, sealer, nil, time.Hour, 0); !errors.Is(err, ErrNoNonceStore) {
		t.Fatalf("nonce nil: err=%v want ErrNoNonceStore", err)
	}
	if _, err := NewProductionRatificationGate(eval, registry, sealer, nonces, 0, 0); !errors.Is(err, ErrNoFreshness) {
		t.Fatalf("ttl<=0: err=%v want ErrNoFreshness", err)
	}

	// Config válida: constrói, e o anti-replay está ligado (replay bloqueado).
	gate, err := NewProductionRatificationGate(eval, registry, sealer, nonces, time.Hour, 0, WithRatifyClock(fixedClock()))
	if err != nil {
		t.Fatalf("config válida recusada: %v", err)
	}
	art := passingArtifact()
	signed := signRatificationFor(t, vault, "ratifier", true, art)
	if admit, err := gate.Ratify(ctx, art, signed); err != nil || !admit {
		t.Fatalf("1ª ratificação: admit=%v err=%v", admit, err)
	}
	if admit, err := gate.Ratify(ctx, art, signed); err != nil || admit {
		t.Fatalf("replay na via de produção: admit=%v err=%v, quero bloqueado", admit, err)
	}
}
