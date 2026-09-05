package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	control "github.com/aos-ref/kernel/agent-runtime/control"
)

// AOS-307 (achado de revisão) — [VerifyEmitterSignature], a verificação SEM consumo de
// nonce e SEM frescura de que a rehidratação dos níveis de autonomia precisa.
//
// O que estes testes fixam é tanto o que ela faz como o que ela DELIBERADAMENTE não faz:
// se algum dia ganhasse nonce ou janela de frescura, o nó deixaria de conseguir reverificar
// os seus próprios registos no arranque — e a falha apareceria como «assinatura inválida»
// num boot que não arranca, que é o pior sítio para descobrir uma decisão de desenho.

// TestAOS307VerifyOnlyAceitaAssinaturaValida — o caso base, sobre o MESMO tuplo que
// [Ed25519Authenticator.Authenticate] usa.
func TestAOS307VerifyOnlyAceitaAssinaturaValida(t *testing.T) {
	pub, priv := operator(t)
	payload := CanonicalAutonomyPayload("agt-1", "fs", "L5", "duas pessoas")
	em, err := SignEmitter("op:a", priv, AutonomyScope, control.SignalAutonomy, payload, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyEmitterSignature(AutonomyScope, control.SignalAutonomy, payload, pub, em); err != nil {
		t.Fatalf("assinatura valida devia verificar: %v", err)
	}
}

// TestAOS307VerifyOnlyNaoConsomeNonceNemExigeFrescura — as DUAS omissões, e porquê.
//
// Reverificar o mesmo emitter N vezes tem de dar sempre o mesmo resultado (senão o segundo
// arranque de um nó recusaria o que o primeiro aceitou), e um carimbo de há um ano — que é
// o que uma decisão de há um ano tem — continua a verificar.
func TestAOS307VerifyOnlyNaoConsomeNonceNemExigeFrescura(t *testing.T) {
	pub, priv := operator(t)
	payload := CanonicalAutonomyPayload("agt-1", "fs", "L3", "decisao antiga")
	antigo := time.Now().UTC().Add(-365 * 24 * time.Hour)
	em, err := SignEmitter("op:a", priv, AutonomyScope, control.SignalAutonomy, payload, antigo)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := VerifyEmitterSignature(AutonomyScope, control.SignalAutonomy, payload, pub, em); err != nil {
			t.Fatalf("verificacao %d falhou — reverificar tem de ser idempotente e indiferente a idade: %v", i, err)
		}
	}

	// CONTRASTE com a fronteira de admissão: o MESMO emitter passado ao Authenticate é
	// recusado por frescura (e, fresco, gastaria o nonce). São propósitos diferentes, e é
	// por isso que são funções diferentes.
	nonces, _ := newNonceStore(t)
	auth, err := NewEd25519Authenticator(nonces, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	auth.Register("op:a", pub)
	if err := auth.Authenticate(context.Background(), AutonomyScope, control.SignalAutonomy, payload, em); !errors.Is(err, ErrStaleSignal) {
		t.Fatalf("Authenticate devia recusar por frescura, veio: %v", err)
	}
}

// TestAOS307VerifyOnlyRecusaOQueNaoBate — fail-closed em cada eixo do tuplo assinado.
func TestAOS307VerifyOnlyRecusaOQueNaoBate(t *testing.T) {
	pub, priv := operator(t)
	outraPub, _ := operator(t)
	payload := CanonicalAutonomyPayload("agt-1", "fs", "L1", "motivo")
	em, err := SignEmitter("op:a", priv, AutonomyScope, control.SignalAutonomy, payload, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}

	casos := []struct {
		nome    string
		correr  func() error
		esperar error
	}{
		{"outro payload (nivel diferente)", func() error {
			return VerifyEmitterSignature(AutonomyScope, control.SignalAutonomy,
				CanonicalAutonomyPayload("agt-1", "fs", "L5", "motivo"), pub, em)
		}, ErrBadSignature},
		{"outra pubkey", func() error {
			return VerifyEmitterSignature(AutonomyScope, control.SignalAutonomy, payload, outraPub, em)
		}, ErrBadSignature},
		{"outro ambito", func() error {
			return VerifyEmitterSignature(RevokeScope, control.SignalAutonomy, payload, pub, em)
		}, ErrBadSignature},
		{"nonce trocado", func() error {
			alt := em
			alt.Nonce = []byte("outro-nonce-com-tamanho-suficiente")
			return VerifyEmitterSignature(AutonomyScope, control.SignalAutonomy, payload, pub, alt)
		}, ErrBadSignature},
		{"carimbo trocado", func() error {
			alt := em
			alt.IssuedAt = em.IssuedAt.Add(time.Nanosecond)
			return VerifyEmitterSignature(AutonomyScope, control.SignalAutonomy, payload, pub, alt)
		}, ErrBadSignature},
		{"nonce ausente", func() error {
			alt := em
			alt.Nonce = nil
			return VerifyEmitterSignature(AutonomyScope, control.SignalAutonomy, payload, pub, alt)
		}, ErrMissingNonce},
		{"carimbo ausente", func() error {
			alt := em
			alt.IssuedAt = time.Time{}
			return VerifyEmitterSignature(AutonomyScope, control.SignalAutonomy, payload, pub, alt)
		}, ErrMissingIssuedAt},
		{"pubkey de tamanho errado", func() error {
			return VerifyEmitterSignature(AutonomyScope, control.SignalAutonomy, payload, []byte{1, 2, 3}, em)
		}, ErrUnknownEmitter},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			if err := c.correr(); !errors.Is(err, c.esperar) {
				t.Fatalf("err = %v; quero %v", err, c.esperar)
			}
		})
	}
}
