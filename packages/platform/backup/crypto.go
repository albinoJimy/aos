package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"io"

	"github.com/aos-ref/platform/audit"
)

// Dimensões de chave/nonce da cifra em repouso do backup (ADR-006). Iguais às do
// envelope do audit (AOS-083) e do crypto-shredding (AOS-021): AES-256 (KEK por
// titular do backup e DEK por segmento) com AES-256-GCM; nonce GCM de 96 bits.
const (
	kekSize   = 32
	dekSize   = 32
	nonceSize = 12
)

// backupSubjectPrefix é o domínio do titular da KEK do backup no KeyVault. Uma KEK
// por região de soberania: a chave nunca é partilhada entre fronteiras.
const backupSubjectPrefix = "aos.backup:"

// cryptoRand adapta crypto/rand.Read à assinatura de audit.RandSource
// (preenchimento total ou erro). Reutiliza-se a MESMA porta do audit para manter
// o determinismo injectável nos testes.
func cryptoRand(p []byte) error {
	_, err := io.ReadFull(rand.Reader, p)
	return err
}

// encryptedSegment é o CIPHERTEXT de envelope de um segmento do backup, guardado
// no ImmutableStore. Envelope encryption em dois níveis (AES-256-GCM, stdlib),
// molde de AOS-083 (as funções sealPayload/openPayload do audit são
// package-private, pelo que o envelope é reimplementado aqui com stdlib):
//
//   - uma DEK aleatória por segmento cifra o plaintext (os eventos) → Ciphertext;
//   - a KEK do titular do backup (do audit.KeyVault) embrulha a DEK → WrappedDEK.
//
// O blob serializado (JSON) é o que vai INTEIRO para o ImmutableStore; o manifesto
// sela apenas o SHA-256 deste blob, nunca o plaintext.
type encryptedSegment struct {
	KeyRef     string `json:"key_ref"`
	WrappedDEK []byte `json:"wrapped_dek"`
	DEKNonce   []byte `json:"dek_nonce"`
	Ciphertext []byte `json:"ciphertext"`
	Nonce      []byte `json:"nonce"`
}

// sealSegment cifra plaintext por envelope sob a KEK do titular subjectID,
// provisionada/lida do KeyVault. Gera a DEK e os nonces pela RandSource injectada
// (determinística em teste). O resultado é serializável e imutável em repouso.
func sealSegment(vault audit.KeyVault, subjectID string, plaintext []byte, randSrc audit.RandSource) (encryptedSegment, error) {
	kek, keyRef, err := vault.EnsureKey(subjectID)
	if err != nil {
		return encryptedSegment{}, err
	}
	dek := make([]byte, dekSize)
	if err := randSrc(dek); err != nil {
		return encryptedSegment{}, err
	}
	nonce := make([]byte, nonceSize)
	if err := randSrc(nonce); err != nil {
		return encryptedSegment{}, err
	}
	dekNonce := make([]byte, nonceSize)
	if err := randSrc(dekNonce); err != nil {
		return encryptedSegment{}, err
	}

	contentGCM, err := newGCM(dek)
	if err != nil {
		return encryptedSegment{}, err
	}
	ciphertext := contentGCM.Seal(nil, nonce, plaintext, nil)

	kekGCM, err := newGCM(kek)
	if err != nil {
		return encryptedSegment{}, err
	}
	wrapped := kekGCM.Seal(nil, dekNonce, dek, nil)

	return encryptedSegment{
		KeyRef:     keyRef,
		WrappedDEK: wrapped,
		DEKNonce:   dekNonce,
		Ciphertext: ciphertext,
		Nonce:      nonce,
	}, nil
}

// openSegment decifra um blob de envelope sob a KEK identificada por seg.KeyRef
// (o inverso de sealSegment). Fail-closed: KEK ausente ou blob adulterado devolve
// erro — a autenticação do GCM falha.
func openSegment(vault audit.KeyVault, seg encryptedSegment) ([]byte, error) {
	kek, ok := vault.Key(seg.KeyRef)
	if !ok {
		return nil, ErrRestoreVerify
	}
	kekGCM, err := newGCM(kek)
	if err != nil {
		return nil, err
	}
	dek, err := kekGCM.Open(nil, seg.DEKNonce, seg.WrappedDEK, nil)
	if err != nil {
		return nil, ErrSegmentTampered
	}
	contentGCM, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	plaintext, err := contentGCM.Open(nil, seg.Nonce, seg.Ciphertext, nil)
	if err != nil {
		return nil, ErrSegmentTampered
	}
	return plaintext, nil
}

// newGCM constrói um AEAD AES-GCM a partir de uma chave de 32 bytes (AES-256).
func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// marshalSegment serializa o blob de envelope (JSON) para persistir no
// ImmutableStore.
func marshalSegment(seg encryptedSegment) ([]byte, error) {
	return json.Marshal(seg)
}

// unmarshalSegment reconstrói o blob de envelope a partir dos bytes persistidos.
func unmarshalSegment(blob []byte) (encryptedSegment, error) {
	var seg encryptedSegment
	if err := json.Unmarshal(blob, &seg); err != nil {
		return encryptedSegment{}, err
	}
	return seg, nil
}
