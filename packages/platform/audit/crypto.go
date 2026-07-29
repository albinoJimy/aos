package audit

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"io"
)

// Dimensões de chave/nonce da cifra de PII do audit (AOS-083). São iguais às do
// crypto-shredding episódico (AOS-021): AES-256 (KEK por titular e DEK por
// registo) com AES-256-GCM em envelope; nonce GCM standard de 96 bits.
const (
	kekSize   = 32
	dekSize   = 32
	nonceSize = 12
)

// RandSource é a fonte de entropia para material de chave e nonces. Em produção é
// crypto/rand.Read (default); nos TESTES é injectável para reprodutibilidade
// (determinismo — sem crypto/rand real no caminho de asserção). Deve preencher p
// por completo (semântica de io.ReadFull) e devolver erro se não conseguir.
type RandSource func(p []byte) error

// cryptoRand adapta crypto/rand.Read à assinatura de [RandSource] (preenchimento
// total ou erro).
func cryptoRand(p []byte) error {
	_, err := io.ReadFull(rand.Reader, p)
	return err
}

// encryptedPayload é o CIPHERTEXT de envelope da PII de um registo de audit,
// guardado FORA da hash-chain (num [PayloadStore]); a cadeia sela apenas o HASH
// deste blob ([PayloadRef.ContentHash]), nunca o plaintext. Envelope encryption
// em dois níveis (AES-256-GCM, stdlib), molde de AOS-021:
//
//   - uma DEK (data key) aleatória por registo cifra o plaintext → Ciphertext;
//   - a KEK do titular (do [KeyVault]) embrulha a DEK → WrappedDEK.
//
// Apagar a KEK (crypto-shredding, [Shredder.Shred]) impede o desembrulho da DEK e,
// logo, a decifragem — a PII fica irrecuperável, mas este blob (e o seu hash na
// cadeia) NUNCA é mutado, pelo que a cadeia continua a verificar.
type encryptedPayload struct {
	WrappedDEK []byte `json:"wrapped_dek"`
	DEKNonce   []byte `json:"dek_nonce"`
	Ciphertext []byte `json:"ciphertext"`
	Nonce      []byte `json:"nonce"`
}

// sealPayload cifra plaintext por envelope sob a KEK dada. Gera a DEK e os nonces
// pela [RandSource] injectada (determinística em teste). O resultado é
// serializável (JSON) e vai INTEIRO para o [PayloadStore], fora da cadeia.
//
// Modelo de nonces idêntico a AOS-021: a DEK é fresca por registo, logo o par
// (DEK, nonce) é único por construção; o dekNonce (embrulho sob a KEK estável do
// titular) tem unicidade PROBABILÍSTICA de 96 bits (limite de aniversário) —
// aceitável para volumes MVP; produção que o ultrapasse migra para nonce
// determinístico/contador por-KEK.
func sealPayload(kek, plaintext []byte, randSrc RandSource) (encryptedPayload, error) {
	dek := make([]byte, dekSize)
	if err := randSrc(dek); err != nil {
		return encryptedPayload{}, err
	}
	nonce := make([]byte, nonceSize)
	if err := randSrc(nonce); err != nil {
		return encryptedPayload{}, err
	}
	dekNonce := make([]byte, nonceSize)
	if err := randSrc(dekNonce); err != nil {
		return encryptedPayload{}, err
	}

	contentGCM, err := newPayloadGCM(dek)
	if err != nil {
		return encryptedPayload{}, err
	}
	ciphertext := contentGCM.Seal(nil, nonce, plaintext, nil)

	kekGCM, err := newPayloadGCM(kek)
	if err != nil {
		return encryptedPayload{}, err
	}
	wrapped := kekGCM.Seal(nil, dekNonce, dek, nil)

	return encryptedPayload{
		WrappedDEK: wrapped,
		DEKNonce:   dekNonce,
		Ciphertext: ciphertext,
		Nonce:      nonce,
	}, nil
}

// openPayload decifra um blob de envelope sob a KEK dada (o inverso de
// [sealPayload]). Fail-closed: uma KEK errada/ausente (shredded) ou um blob
// adulterado devolve [ErrDecrypt] — a autenticação do GCM falha. É por aqui que o
// crypto-shredding se manifesta na leitura: sem a KEK, o open é impossível.
func openPayload(kek []byte, s encryptedPayload) ([]byte, error) {
	kekGCM, err := newPayloadGCM(kek)
	if err != nil {
		return nil, err
	}
	dek, err := kekGCM.Open(nil, s.DEKNonce, s.WrappedDEK, nil)
	if err != nil {
		return nil, ErrDecrypt
	}
	contentGCM, err := newPayloadGCM(dek)
	if err != nil {
		return nil, err
	}
	plaintext, err := contentGCM.Open(nil, s.Nonce, s.Ciphertext, nil)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}

// SealContent cifra o CONTEÚDO de um run (AOS-093) sob a KEK POR-TITULAR do vault,
// reutilizando o MESMO envelope DEK/KEK da PII de audit ([sealPayload]): uma DEK
// aleatória por registo cifra o plaintext e a KEK do titular embrulha a DEK. A KEK
// é provisionada na 1ª escrita ([KeyVault.EnsureKey]) — a mesma chave por-titular que
// o crypto-shredding destrói. Devolve o blob de envelope serializável (opaco) que os
// escritores do Event Store persistem no lugar do texto-claro; o plaintext NUNCA vai
// ao WAL. randSrc nil cai em crypto/rand (produção); os testes injectam determinismo.
//
// É a FRONTEIRA estável reutilizada pelo substrato (kernel/agent-runtime) via a porta
// [ContentSealer] cablada no composition root — sem duplicar crypto nem adicionar libs.
func SealContent(vault KeyVault, subjectID string, plaintext []byte, randSrc RandSource) ([]byte, error) {
	if vault == nil {
		return nil, ErrNoSubject
	}
	if subjectID == "" {
		return nil, ErrNoSubject
	}
	if randSrc == nil {
		randSrc = cryptoRand
	}
	kek, _, err := vault.EnsureKey(subjectID)
	if err != nil {
		return nil, err
	}
	env, err := sealPayload(kek, plaintext, randSrc)
	if err != nil {
		return nil, err
	}
	return json.Marshal(env)
}

// OpenContent decifra o blob que [SealContent] selou, resolvendo a KEK do titular no
// vault. FAIL-CLOSED: se a KEK foi destruída (crypto-shredding via [Shredder.Shred] /
// [KeyVault.Delete]) devolve [ErrDecrypt] — o conteúdo do run é IRRECUPERÁVEL. É por
// aqui que a erasure DSAR se manifesta sobre o substrato: sem a KEK, o open é
// impossível, mas o blob (e o seu hash na cadeia/WAL) NUNCA é mutado.
func OpenContent(vault KeyVault, subjectID string, blob []byte) ([]byte, error) {
	if vault == nil || subjectID == "" {
		return nil, ErrDecrypt
	}
	kek, ok := vault.Key(KeyRefFor(subjectID))
	if !ok {
		return nil, ErrDecrypt
	}
	var env encryptedPayload
	if err := json.Unmarshal(blob, &env); err != nil {
		return nil, ErrDecrypt
	}
	return openPayload(kek, env)
}

// newPayloadGCM constrói um AEAD AES-GCM a partir de uma chave de 32 bytes (AES-256).
func newPayloadGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// marshalPayload serializa o blob de envelope (JSON canónico de campos com ordem
// fixa) e devolve os bytes a persistir no [PayloadStore] MAIS o seu SHA-256 — o
// [PayloadRef.ContentHash] que a hash-chain sela. O hash NÃO depende do plaintext
// nem da KEK: apagar a chave não o altera, pelo que a cadeia continua a verificar.
func marshalPayload(s encryptedPayload) (blob []byte, contentHash []byte, err error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(raw)
	return raw, sum[:], nil
}
