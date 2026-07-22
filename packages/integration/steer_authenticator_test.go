package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	hitl "github.com/aos-ref/control-plane/governance/hitl"
	control "github.com/aos-ref/kernel/agent-runtime/control"
	"github.com/aos-ref/substrate/eventstore"
)

// steerClock é um relógio determinístico para os testes de frescura/expiração.
type steerClock struct{ t time.Time }

func (c *steerClock) Now() time.Time { return c.t }

// newNonceStore devolve um EventStoreNonceStore durável e o Store que o suporta (para
// simular um restart reconstruindo um novo store sobre o MESMO log).
func newNonceStore(t *testing.T) (*hitl.EventStoreNonceStore, *eventstore.Store) {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	return hitl.NewEventStoreNonceStore(es), es
}

// operator gera um par de chaves ed25519 e devolve a pubkey (registada no authenticator)
// e a privada (que vive SÓ no lado do emissor/teste, nunca no authenticator).
func operator(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}

func freshNonce(t *testing.T) []byte {
	t.Helper()
	n := make([]byte, 16)
	if _, err := rand.Read(n); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return n
}

// (a) Uma assinatura ed25519 VÁLIDA de um emissor registado é aceite, e o emissor
// (operador humano) fica atribuível no evento durável do canal (não-repúdio).
func TestEd25519_ValidSignatureAccepted(t *testing.T) {
	nonces, _ := newNonceStore(t)
	clock := &steerClock{t: time.Now()}
	auth, err := NewEd25519Authenticator(nonces, time.Minute, WithEd25519Clock(clock.Now))
	if err != nil {
		t.Fatalf("NewEd25519Authenticator: %v", err)
	}
	const emitterID = "human:operator-alice"
	pub, priv := operator(t)
	auth.Register(emitterID, pub)

	// Liga ao SteerChannel via a interface control.Authenticator — o canal não muda.
	chStore, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	ch, err := control.NewChannel(chStore, auth)
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}

	const runID = "run-1"
	correction := []byte("aperta o âmbito ao ticket")
	em := SignSignal(priv, emitterID, runID, control.SignalSteer, correction, freshNonce(t), clock.Now())

	if err := ch.Steer(context.Background(), runID, correction, em); err != nil {
		t.Fatalf("Steer com assinatura válida devia ser aceite, veio: %v", err)
	}
	// A correcção pendente prova que o sinal foi gravado; a atribuição do emissor está no
	// log durável (controlRecord.EmitterID).
	got, ok := ch.PendingCorrection(runID)
	if !ok || string(got) != string(correction) {
		t.Fatalf("correcção pendente = (%q,%v); quer (%q,true)", got, ok, correction)
	}
}

// (b) Chave ERRADA (assinado por outra privada) e assinatura ADULTERADA (bit-flip) são
// rejeitadas fail-closed.
func TestEd25519_WrongKeyAndTamperedSignatureRejected(t *testing.T) {
	nonces, _ := newNonceStore(t)
	clock := &steerClock{t: time.Now()}
	auth, err := NewEd25519Authenticator(nonces, time.Minute, WithEd25519Clock(clock.Now))
	if err != nil {
		t.Fatalf("NewEd25519Authenticator: %v", err)
	}
	const emitterID = "human:operator-bob"
	pub, _ := operator(t)
	auth.Register(emitterID, pub) // regista a pubkey do bob…

	const runID = "run-2"
	payload := []byte("x")

	// …mas o sinal é assinado por uma chave DIFERENTE (impostor).
	_, impostorPriv := operator(t)
	wrong := SignSignal(impostorPriv, emitterID, runID, control.SignalPause, payload, freshNonce(t), clock.Now())
	if err := auth.Authenticate(context.Background(), runID, control.SignalPause, payload, wrong); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("chave errada devia dar ErrBadSignature, veio: %v", err)
	}

	// Assinatura adulterada (bit-flip) do detentor legítimo — mas usamos a pubkey do bob
	// com uma privada que não corresponde, e além disso viramos um bit.
	tampered := wrong
	sig := make([]byte, len(tampered.Signature))
	copy(sig, tampered.Signature)
	sig[0] ^= 0xFF
	tampered.Signature = sig
	if err := auth.Authenticate(context.Background(), runID, control.SignalPause, payload, tampered); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("assinatura adulterada devia dar ErrBadSignature, veio: %v", err)
	}

	// Emissor desconhecido (sem pubkey registada) ⇒ default-deny.
	_, otherPriv := operator(t)
	unknown := SignSignal(otherPriv, "human:nao-registado", runID, control.SignalPause, payload, freshNonce(t), clock.Now())
	if err := auth.Authenticate(context.Background(), runID, control.SignalPause, payload, unknown); !errors.Is(err, ErrUnknownEmitter) {
		t.Fatalf("emissor desconhecido devia dar ErrUnknownEmitter, veio: %v", err)
	}
}

// (c) ANTI-REPLAY: o MESMO sinal (mesmo nonce) é aceite à 1ª e rejeitado à 2ª. E prova
// que a rejeição passa pelo STORE DURÁVEL: um authenticator NOVO sobre um nonce-store
// NOVO backed pelo MESMO Event Store (simulando um restart) continua a detectar o replay
// — não é um set in-memory.
func TestEd25519_AntiReplayDurable(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	clock := &steerClock{t: time.Now()}
	mkAuth := func() *Ed25519Authenticator {
		a, aerr := NewEd25519Authenticator(hitl.NewEventStoreNonceStore(es), time.Minute, WithEd25519Clock(clock.Now))
		if aerr != nil {
			t.Fatalf("NewEd25519Authenticator: %v", aerr)
		}
		return a
	}
	const emitterID = "svc:surface"
	pub, priv := operator(t)

	auth1 := mkAuth()
	auth1.Register(emitterID, pub)

	const runID = "run-3"
	payload := []byte("resume-payload")
	nonce := freshNonce(t)
	em := SignSignal(priv, emitterID, runID, control.SignalResume, payload, nonce, clock.Now())

	// 1ª: fresco ⇒ aceite.
	if err := auth1.Authenticate(context.Background(), runID, control.SignalResume, payload, em); err != nil {
		t.Fatalf("1º uso do nonce devia ser aceite, veio: %v", err)
	}
	// 2ª no MESMO authenticator ⇒ replay.
	if err := auth1.Authenticate(context.Background(), runID, control.SignalResume, payload, em); !errors.Is(err, ErrReplayedSignal) {
		t.Fatalf("2º uso (mesmo authenticator) devia dar ErrReplayedSignal, veio: %v", err)
	}

	// Simula RESTART: authenticator + nonce-store NOVOS sobre o MESMO Event Store. Se o
	// anti-replay fosse um set in-memory, o nonce voltaria a parecer fresco aqui.
	auth2 := mkAuth()
	auth2.Register(emitterID, pub)
	if err := auth2.Authenticate(context.Background(), runID, control.SignalResume, payload, em); !errors.Is(err, ErrReplayedSignal) {
		t.Fatalf("replay após restart devia dar ErrReplayedSignal (store durável), veio: %v", err)
	}
}

// (d) EXPIRAÇÃO: um issued_at fora da janela de frescura (via relógio injectado) é
// recusado — tanto velho de mais como no futuro além do skew.
func TestEd25519_StaleSignalRejected(t *testing.T) {
	nonces, _ := newNonceStore(t)
	base := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	clock := &steerClock{t: base}
	auth, err := NewEd25519Authenticator(nonces, time.Minute, WithEd25519Clock(clock.Now), WithEd25519Skew(5*time.Second))
	if err != nil {
		t.Fatalf("NewEd25519Authenticator: %v", err)
	}
	const emitterID = "human:operator-carol"
	pub, priv := operator(t)
	auth.Register(emitterID, pub)

	const runID = "run-4"
	payload := []byte("p")

	// Velho de mais: issued_at = now - 2min, ttl = 1min ⇒ rejeitado.
	old := SignSignal(priv, emitterID, runID, control.SignalPause, payload, freshNonce(t), base.Add(-2*time.Minute))
	if err := auth.Authenticate(context.Background(), runID, control.SignalPause, payload, old); !errors.Is(err, ErrStaleSignal) {
		t.Fatalf("carimbo velho devia dar ErrStaleSignal, veio: %v", err)
	}

	// No futuro além do skew: issued_at = now + 1min, skew = 5s ⇒ rejeitado.
	future := SignSignal(priv, emitterID, runID, control.SignalPause, payload, freshNonce(t), base.Add(time.Minute))
	if err := auth.Authenticate(context.Background(), runID, control.SignalPause, payload, future); !errors.Is(err, ErrStaleSignal) {
		t.Fatalf("carimbo no futuro devia dar ErrStaleSignal, veio: %v", err)
	}

	// Dentro da janela: issued_at = now - 30s ⇒ aceite.
	ok := SignSignal(priv, emitterID, runID, control.SignalPause, payload, freshNonce(t), base.Add(-30*time.Second))
	if err := auth.Authenticate(context.Background(), runID, control.SignalPause, payload, ok); err != nil {
		t.Fatalf("carimbo dentro da janela devia ser aceite, veio: %v", err)
	}
}

// (e) Sinal ADULTERADO: trocar payload/kind/runID DEPOIS de assinar invalida a assinatura
// (que cobre o tuplo completo) ⇒ rejeitado. Prova que nonce/issued_at também são cobertos.
func TestEd25519_TamperedSignalRejected(t *testing.T) {
	nonces, _ := newNonceStore(t)
	clock := &steerClock{t: time.Now()}
	auth, err := NewEd25519Authenticator(nonces, time.Minute, WithEd25519Clock(clock.Now))
	if err != nil {
		t.Fatalf("NewEd25519Authenticator: %v", err)
	}
	const emitterID = "human:operator-dan"
	pub, priv := operator(t)
	auth.Register(emitterID, pub)

	const runID = "run-5"
	payload := []byte("original")
	nonce := freshNonce(t)
	issued := clock.Now()
	em := SignSignal(priv, emitterID, runID, control.SignalSteer, payload, nonce, issued)

	ctx := context.Background()

	// payload trocado depois de assinar.
	if err := auth.Authenticate(ctx, runID, control.SignalSteer, []byte("adulterado"), em); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("payload trocado devia dar ErrBadSignature, veio: %v", err)
	}
	// kind trocado.
	if err := auth.Authenticate(ctx, runID, control.SignalPause, payload, em); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("kind trocado devia dar ErrBadSignature, veio: %v", err)
	}
	// runID trocado.
	if err := auth.Authenticate(ctx, "run-OUTRO", control.SignalSteer, payload, em); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("runID trocado devia dar ErrBadSignature, veio: %v", err)
	}
	// nonce trocado (mantendo o resto) — a assinatura cobre o nonce.
	tamperedNonce := em
	tamperedNonce.Nonce = freshNonce(t)
	if err := auth.Authenticate(ctx, runID, control.SignalSteer, payload, tamperedNonce); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("nonce trocado devia dar ErrBadSignature, veio: %v", err)
	}
	// issued_at trocado — a assinatura cobre o carimbo.
	tamperedTime := em
	tamperedTime.IssuedAt = issued.Add(time.Second)
	if err := auth.Authenticate(ctx, runID, control.SignalSteer, payload, tamperedTime); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("issued_at trocado devia dar ErrBadSignature, veio: %v", err)
	}
}

// TestEd25519_BoundaryShiftRejected prova a INJECTIVIDADE da codificação assinada — o
// achado ALTO da auditoria. Um ataque de DESLIZE DE FRONTEIRA (payload|nonce), que
// FUNCIONARIA com separadores 0x00 (o byte separador ocorre DENTRO do nonce), é rejeitado
// pela codificação com length-prefix. Sem esta propriedade, um atacante SEM a chave
// privada re-mintava um sinal com correcção MUTADA e nonce DIFERENTE (novo scope ⇒ o
// anti-replay durável de AOS-159 não o apanharia) mantendo a MESMA assinatura ed25519.
func TestEd25519_BoundaryShiftRejected(t *testing.T) {
	nonces, _ := newNonceStore(t)
	clock := &steerClock{t: time.Now()}
	auth, err := NewEd25519Authenticator(nonces, time.Minute, WithEd25519Clock(clock.Now))
	if err != nil {
		t.Fatalf("NewEd25519Authenticator: %v", err)
	}
	const emitterID = "human:operator-eve"
	pub, priv := operator(t)
	auth.Register(emitterID, pub)

	const runID = "run-shift"
	// Nonce com um 0x00 INTERNO: nonce = n1 ‖ 0x00 ‖ n2. Ambos os lados têm >= MinNonceLen
	// para o deslize chegar à verificação de assinatura (não ser cortado pelo MinNonceLen).
	n1 := bytes.Repeat([]byte{0xaa}, 16)
	n2 := bytes.Repeat([]byte{0xbb}, 16)
	nonce := append(append(append([]byte{}, n1...), 0x00), n2...) // 33 bytes, 0x00 no meio
	payload := []byte("aperta o ambito")
	issued := clock.Now()

	em := SignSignal(priv, emitterID, runID, control.SignalSteer, payload, nonce, issued)

	// Variante DESLIZADA: sob separadores 0x00, payload'‖0x00‖nonce' teria os MESMOS bytes
	// que payload‖0x00‖nonce — logo a MESMA assinatura seria válida. payload' = payload ‖
	// 0x00 ‖ n1; nonce' = n2 (16 bytes, >= MinNonceLen). A assinatura é REUTILIZADA.
	shiftedPayload := append(append(append([]byte{}, payload...), 0x00), n1...)
	shifted := em // MESMA assinatura
	shifted.Nonce = n2

	ctx := context.Background()
	if err := auth.Authenticate(ctx, runID, control.SignalSteer, shiftedPayload, shifted); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("deslize de fronteira devia dar ErrBadSignature (injectividade length-prefix), veio: %v", err)
	}
	// Sanidade: o sinal ORIGINAL (não deslizado) continua a ser aceite.
	if err := auth.Authenticate(ctx, runID, control.SignalSteer, payload, em); err != nil {
		t.Fatalf("sinal original devia ser aceite, veio: %v", err)
	}
}

// Fail-closed na construção: sem nonce-store ou com ttl<=0 é recusado.
func TestEd25519_ConstructionFailClosed(t *testing.T) {
	if _, err := NewEd25519Authenticator(nil, time.Minute); !errors.Is(err, ErrNoNonceStore) {
		t.Fatalf("nonce-store nil devia dar ErrNoNonceStore, veio: %v", err)
	}
	nonces, _ := newNonceStore(t)
	if _, err := NewEd25519Authenticator(nonces, 0); !errors.Is(err, ErrNoFreshnessWindow) {
		t.Fatalf("ttl<=0 devia dar ErrNoFreshnessWindow, veio: %v", err)
	}
}
