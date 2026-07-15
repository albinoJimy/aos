package promotion

import (
	"crypto/ed25519"
	"encoding/binary"
	"sync"

	"github.com/aos-ref/platform/registry/domain"
)

// ratificationDomain é o separador de domínio VERSIONADO prefixado à mensagem
// canónica de ratificação. Garante que uma assinatura de ratificação do REG nunca
// colide com a de outro subsistema (ex.: a ratificação procedural de AOS-040 ou a
// assinatura de origem de AOS-048) e permite evoluir o formato sem ambiguidade.
const ratificationDomain = "aos.registry.ratification.v1"

// Ratification é a RATIFICAÇÃO HUMANA ASSINADA exigida antes de uma skill
// auto-escrita chegar a active (ADR-012, não-repúdio). O humano assina — ed25519,
// FORA do sistema — a mensagem canónica que liga o ratificador ao alvo
// (id, versão, digest); o Pipeline verifica-a contra a allowlist de chaves de
// ratificadores autorizados. É o gate final: sem uma ratificação assinada VÁLIDA a
// activação é recusada.
type Ratification struct {
	// ID e Version identificam o artefacto ratificado.
	ID      string
	Version domain.Version
	// Digest é o digest do conteúdo canonicalizado revisto — liga a assinatura aos
	// BYTES exactos, não apenas ao rótulo SemVer (defesa contra ratificação de uma
	// versão e activação de conteúdo diferente).
	Digest string
	// RatifierID identifica o humano ratificador (indexa a chave pública autorizada
	// na allowlist).
	RatifierID string
	// Signature é a assinatura ed25519 (64 bytes) sobre [CanonicalRatification].
	Signature []byte
}

// CanonicalRatification é a mensagem canónica e determinística que o humano assina
// para ratificar (id, versão, digest). Cada campo é precedido do seu comprimento
// (uvarint) para que a concatenação nunca colida (domain separation por
// comprimento, o mesmo padrão de AOS-047/048). É PURA (sem time.Now/rand): o mesmo
// tuplo produz sempre os mesmos bytes, pelo que a verificação é reproduzível e a
// assinatura ed25519 (determinística, RFC 8032) é estável.
func CanonicalRatification(ratifierID, id string, v domain.Version, digest string) []byte {
	buf := make([]byte, 0, 128)
	buf = putString(buf, ratificationDomain)
	buf = putString(buf, ratifierID)
	buf = putString(buf, id)
	buf = putString(buf, v.String())
	buf = putString(buf, digest)
	return buf
}

// SignRatification produz uma [Ratification] assinada — conveniência para o lado
// que detém a chave humana (em produção a assinatura é feita fora do sistema, ex.:
// num HSM/dispositivo do ratificador). O REG NUNCA detém a chave privada humana.
func SignRatification(priv ed25519.PrivateKey, ratifierID, id string, v domain.Version, digest string) Ratification {
	msg := CanonicalRatification(ratifierID, id, v, digest)
	return Ratification{
		ID:         id,
		Version:    v,
		Digest:     digest,
		RatifierID: ratifierID,
		Signature:  ed25519.Sign(priv, msg),
	}
}

// RatifierStore é a ALLOWLIST de chaves públicas de ratificadores humanos
// autorizados. Só uma ratificação assinada por uma chave AQUI presente é aceite (a
// autorização humana é uma decisão de governação, não derivável do artefacto).
// Seguro para concorrência: as leituras (Verify) podem correr concorrentes com as
// escritas (Authorize). Construir com [NewRatifierStore].
type RatifierStore struct {
	mu   sync.RWMutex
	keys map[string]ed25519.PublicKey
}

// NewRatifierStore constrói uma allowlist vazia (fail-closed: sem chaves nenhuma
// ratificação é aceitável).
func NewRatifierStore() *RatifierStore {
	return &RatifierStore{keys: make(map[string]ed25519.PublicKey)}
}

// Authorize regista (ou substitui) a chave pública de um ratificador autorizado.
// Uma chave de tamanho inválido é rejeitada (fail-closed).
func (s *RatifierStore) Authorize(ratifierID string, pub ed25519.PublicKey) error {
	if ratifierID == "" {
		return ErrRatificationInvalid
	}
	if len(pub) != ed25519.PublicKeySize {
		return ErrRatificationInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make(ed25519.PublicKey, len(pub))
	copy(cp, pub)
	s.keys[ratifierID] = cp
	return nil
}

// Revoke remove um ratificador da allowlist (uma chave humana comprometida deixa
// imediatamente de poder ratificar).
func (s *RatifierStore) Revoke(ratifierID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.keys, ratifierID)
}

// Verify valida uma ratificação sobre o tuplo (id, version, digest) esperado,
// FAIL-CLOSED e por esta ordem:
//
//	(a) o tuplo da ratificação COINCIDE com o esperado (id/version/digest) — senão
//	    ErrRatificationInvalid (impede replay de uma ratificação de outro alvo);
//	(b) o ratificador está na ALLOWLIST — senão ErrRatificationInvalid;
//	(c) a assinatura VALIDA sobre a mensagem canónica com a chave autorizada —
//	    senão ErrRatificationInvalid.
//
// Nunca revela qual dos passos falhou de forma que ajude um atacante: todos os
// caminhos são a mesma recusa explícita.
func (s *RatifierStore) Verify(rat Ratification, id string, v domain.Version, digest string) error {
	if rat.ID != id || !rat.Version.Equal(v) || rat.Digest != digest {
		return ErrRatificationInvalid
	}
	if rat.RatifierID == "" || len(rat.Signature) != ed25519.SignatureSize {
		return ErrRatificationInvalid
	}
	s.mu.RLock()
	pub, ok := s.keys[rat.RatifierID]
	s.mu.RUnlock()
	if !ok {
		return ErrRatificationInvalid
	}
	msg := CanonicalRatification(rat.RatifierID, id, v, digest)
	if !ed25519.Verify(pub, msg, rat.Signature) {
		return ErrRatificationInvalid
	}
	return nil
}

// putString escreve uma string com length-prefix (uvarint) seguido dos bytes —
// domain separation por comprimento, self-contained (sem deps externas). Espelha o
// mesmo padrão de AOS-011/047/048.
func putString(buf []byte, s string) []byte {
	var lb [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lb[:], uint64(len(s)))
	buf = append(buf, lb[:n]...)
	return append(buf, s...)
}
