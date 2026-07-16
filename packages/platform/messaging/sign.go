package messaging

import (
	"context"
	"crypto/ed25519"
	"fmt"
)

// SignMessage assina a mensagem inter-agente com a chave privada ed25519 da NHI do
// EMISSOR (msg.Origin), via a porta [Signer] (broker/Vault, server-side). A
// assinatura cobre a serialização canónica de payload + origem + autoridade +
// referência + acção + material anti-replay (nonce + timestamp) — adulterar
// qualquer campo invalida-a. A chave privada NUNCA entra neste módulo: o Signer
// assina do lado do custodiante e devolve só a assinatura.
//
// Fail-closed: origem/acção em falta, nonce curto (< [nonceMinLen]) ou timestamp
// zero ⇒ [ErrInvalidMessage]; falha do custodiante ⇒ o erro é propagado e a
// mensagem NÃO é assinada. O chamador é responsável por preencher um Nonce único e
// um IssuedAt corrente ANTES de assinar (ambos entram na assinatura). Devolve uma
// cópia da mensagem com Signature preenchida (o argumento não é mutado).
func SignMessage(ctx context.Context, signer Signer, msg Message) (Message, error) {
	if signer == nil {
		return Message{}, ErrNilDeps
	}
	if msg.Origin == "" || msg.Action == "" || len(msg.Nonce) < nonceMinLen || msg.IssuedAt.IsZero() {
		return Message{}, fmt.Errorf("%w: origem=%q accao=%q nonce=%dB issuedAt-zero=%v",
			ErrInvalidMessage, msg.Origin, msg.Action, len(msg.Nonce), msg.IssuedAt.IsZero())
	}
	sig, err := signer.Sign(ctx, msg.Origin, canonicalBytes(msg))
	if err != nil {
		return Message{}, fmt.Errorf("messaging: assinar mensagem de %q: %w", msg.Origin, err)
	}
	if len(sig) != ed25519.SignatureSize {
		return Message{}, fmt.Errorf("%w: assinatura de dimensao invalida (%d)", ErrInvalidMessage, len(sig))
	}
	out := msg
	out.Signature = append([]byte(nil), sig...)
	return out, nil
}
