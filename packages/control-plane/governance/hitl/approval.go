package hitl

import (
	"context"
	"crypto/ed25519"
	"time"

	"github.com/aos-ref/platform/messaging"
)

// approvalDomain é o separador de domínio versionado prefixado à serialização
// canónica de uma decisão de aprovação, para que uma assinatura de aprovação HITL
// nunca colida com a de outro subsistema (token NHI, mensagem inter-agente, cadeia
// de audit) nem entre versões do formato.
const approvalDomain = "aos.hitl.approval.v1"

// approvalNonceMinLen é o comprimento mínimo (bytes) do nonce da decisão. 16 bytes
// (128 bits) de entropia tornam a adivinhação/colisão de nonces impraticável; um
// nonce mais curto é rejeitado (fail-closed). O nonce + o request-id ligam a
// assinatura a ESTE pedido concreto (anti-replay de uma aprovação de outro).
const approvalNonceMinLen = 16

// SignedApproval é a decisão do aprovador sobre um pedido de confirmação: aprovação
// ou recusa, ASSINADA ed25519. A assinatura cobre a serialização canónica de
// (request-id + decisão + aprovador + nonce + timestamp) — adulterar qualquer campo
// invalida-a. É o artefacto de NÃO-REPÚDIO (AC4): verificável contra a chave pública
// pinada do aprovador e selado no audit. A chave PRIVADA nunca entra aqui — o
// [messaging.Signer] (broker/Vault) assina do lado do custodiante e devolve só a
// assinatura.
type SignedApproval struct {
	// RequestID liga a decisão ao pedido concreto apresentado pelo [Channel] (a
	// [Presentation.RequestID]). O Channel exige igualdade: uma aprovação assinada
	// para OUTRO pedido não é aceite (anti-replay).
	RequestID string
	// Approver é o principal que decide (a NHI cuja chave pinada verifica a assinatura
	// e cuja autoridade tem de cobrir a classe).
	Approver string
	// Approved é true para APROVAR, false para RECUSAR. Ambas são assinadas e seladas
	// (uma recusa é também não-repúdio).
	Approved bool
	// Nonce é um valor único (>= [approvalNonceMinLen] bytes aleatórios) por decisão,
	// coberto pela assinatura. Anti-replay/anti-forja.
	Nonce []byte
	// IssuedAt é o instante da decisão, coberto pela assinatura (base da frescura).
	IssuedAt time.Time
	// Signature é a assinatura ed25519 do aprovador sobre a serialização canónica dos
	// campos acima. Preenchida por [SignApproval].
	Signature []byte
}

// canonicalApproval devolve a serialização canónica, determinística e estável
// cross-SO dos campos ASSINÁVEIS de uma decisão (tudo excepto a Signature). Signer
// (aprovador) e Verifier (o Channel) constroem exactamente estes bytes.
func canonicalApproval(a SignedApproval) []byte {
	buf := make([]byte, 0, 128)
	buf = putString(buf, approvalDomain)
	buf = putString(buf, a.RequestID)
	buf = putString(buf, a.Approver)
	if a.Approved {
		buf = append(buf, 1)
	} else {
		buf = append(buf, 0)
	}
	buf = putBytes(buf, a.Nonce)
	buf = putInt64(buf, a.IssuedAt.UTC().UnixNano())
	return buf
}

// SignApproval é o helper do lado do APROVADOR: assina a decisão com a chave privada
// do aprovador via a porta [messaging.Signer] (broker/Vault, server-side — molde
// AOS-073). A chave privada NUNCA entra neste módulo; o Signer assina do lado do
// custodiante e devolve só a assinatura. Devolve uma cópia da decisão com Signature
// preenchida (o argumento não é mutado).
//
// Fail-closed: signer nil ⇒ [ErrNilDeps]; request-id/aprovador em falta, nonce curto
// ou timestamp zero ⇒ [ErrInvalidApproval]; falha do custodiante ⇒ o erro é propagado
// e a decisão NÃO é assinada.
func SignApproval(ctx context.Context, signer messaging.Signer, a SignedApproval) (SignedApproval, error) {
	if signer == nil {
		return SignedApproval{}, ErrNilDeps
	}
	if a.RequestID == "" || a.Approver == "" || len(a.Nonce) < approvalNonceMinLen || a.IssuedAt.IsZero() {
		return SignedApproval{}, ErrInvalidApproval
	}
	sig, err := signer.Sign(ctx, a.Approver, canonicalApproval(a))
	if err != nil {
		return SignedApproval{}, err
	}
	if len(sig) != ed25519.SignatureSize {
		return SignedApproval{}, ErrInvalidApproval
	}
	out := a
	out.Signature = append([]byte(nil), sig...)
	return out, nil
}

// verifyApproval reconstrói os bytes canónicos e valida a assinatura contra a chave
// pública PINADA do aprovador. É o coração da anti-forja/não-repúdio (AC4): mesmo com
// um aprovador que existe, uma assinatura feita por OUTRA chave, ausente ou adulterada
// não valida.
func verifyApproval(pub ed25519.PublicKey, a SignedApproval) bool {
	if len(pub) != ed25519.PublicKeySize || len(a.Signature) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, canonicalApproval(a), a.Signature)
}
