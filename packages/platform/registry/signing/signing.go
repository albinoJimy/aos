package signing

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"

	"github.com/aos-ref/platform/registry/domain"
)

// signingDomain é o separador de domínio VERSIONADO prefixado à serialização
// canónica do tuplo assinado. Garante que uma assinatura do REG nunca colide com
// a de outro subsistema (ex.: os tokens NHI de AOS-005) e permite evoluir o
// formato sem ambiguidade. Um bump de versão invalida (por desenho) assinaturas
// do formato anterior.
const signingDomain = "aos.registry.signature.v1"

// SigningInput produz a serialização DETERMINISTA, canónica e sem ambiguidade de
// fronteiras do tuplo (id, version, digest) — o conteúdo EXACTO que o publicador
// assina e que o REG verifica. Cada campo é precedido do seu comprimento
// (uvarint) para que a concatenação nunca colida (domain separation por
// comprimento, o mesmo padrão do digest de AOS-047): (id="a", version="1.0.0")
// nunca produz os mesmos bytes que (id="a1", version=".0.0"). O separador de
// domínio versionado abre a serialização.
//
// É PURA (sem time.Now/rand): o mesmo tuplo produz sempre os mesmos bytes, logo a
// verificação é reproduzível e a assinatura Ed25519 (determinística) é estável.
func SigningInput(id string, version domain.Version, digest string) []byte {
	buf := make([]byte, 0, 128)
	buf = putString(buf, signingDomain)
	buf = putString(buf, id)
	buf = putString(buf, version.String())
	buf = putString(buf, digest)
	return buf
}

// Signer detém a chave PRIVADA de um publicador (Ed25519) e o seu identificador
// de chave (key id). É a face PUBLICADOR do esquema — vive FORA do REG (tooling de
// publicação, testes). O REG NUNCA detém uma chave privada; verifica apenas com a
// chave pública correspondente registada no [TrustStore]. Construir com [NewSigner].
type Signer struct {
	keyID string
	priv  ed25519.PrivateKey
}

// NewSigner constrói um assinante. keyID é o identificador estável do publicador
// (a ligação à sua chave confiável no trust store — tipicamente o Provenance.
// Publisher da entrada). priv é a chave privada Ed25519. Fail-closed: key id vazio
// devolve [ErrEmptyKeyID]; chave de tamanho inválido devolve [ErrInvalidKey].
func NewSigner(keyID string, priv ed25519.PrivateKey) (*Signer, error) {
	if keyID == "" {
		return nil, ErrEmptyKeyID
	}
	if len(priv) != ed25519.PrivateKeySize {
		return nil, ErrInvalidKey
	}
	return &Signer{keyID: keyID, priv: priv}, nil
}

// KeyID devolve o identificador da chave deste assinante.
func (s *Signer) KeyID() string { return s.keyID }

// PublicKey devolve a chave PÚBLICA correspondente, para registar no [TrustStore]
// (é a única metade da chave que o REG alguma vez toca).
func (s *Signer) PublicKey() ed25519.PublicKey {
	return s.priv.Public().(ed25519.PublicKey)
}

// Sign assina o tuplo (id, version, digest) e devolve a assinatura em base64
// (RawStd). É a operação do publicador legítimo; o resultado é o valor do campo
// Entry.Signature.
func (s *Signer) Sign(id string, version domain.Version, digest string) string {
	sig := ed25519.Sign(s.priv, SigningInput(id, version, digest))
	return base64.RawStdEncoding.EncodeToString(sig)
}

// Verify verifica uma assinatura base64 sobre o tuplo (id, version, digest) com a
// chave pública dada. Devolve nil se a assinatura é válida; caso contrário
// [ErrSignatureInvalid] (fail-closed). Uma chave de tamanho inválido devolve
// [ErrInvalidKey]; uma codificação base64 corrompida ou uma assinatura de tamanho
// errado são tratadas como assinatura inválida (nunca como um sucesso silencioso).
//
// É a peça criptográfica pura, reutilizável pela verificação de admissão e pela
// revalidação por chamada (AOS-051).
func Verify(pub ed25519.PublicKey, id string, version domain.Version, digest, signature string) error {
	if len(pub) != ed25519.PublicKeySize {
		return ErrInvalidKey
	}
	if signature == "" {
		return ErrSignatureMissing
	}
	raw, err := base64.RawStdEncoding.DecodeString(signature)
	if err != nil || len(raw) != ed25519.SignatureSize {
		return ErrSignatureInvalid
	}
	if !ed25519.Verify(pub, SigningInput(id, version, digest), raw) {
		return ErrSignatureInvalid
	}
	return nil
}

// putString escreve uma string com length-prefix (uvarint) seguido dos bytes —
// domain separation por comprimento, self-contained (sem deps externas).
func putString(buf []byte, s string) []byte {
	var lb [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lb[:], uint64(len(s)))
	buf = append(buf, lb[:n]...)
	return append(buf, s...)
}
