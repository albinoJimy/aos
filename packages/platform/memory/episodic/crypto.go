package episodic

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"sync"
)

// kekSize é a dimensão da chave por titular (KEK) e da data-key por episódio
// (DEK): AES-256. dekSize é igual — envelope encryption com AES-256-GCM em ambos
// os níveis. nonceSize é o nonce standard do GCM (12 bytes = 96 bits), gerado
// aleatoriamente (unicidade probabilística por limite de aniversário; ver [seal]).
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

// KeyStore é a PORTA das chaves por titular (KEK) que suportam o crypto-shredding
// (ADR-011). O conteúdo de cada episódio é cifrado por envelope sob a KEK do seu
// titular; APAGAR a KEK (DeleteKey) torna o plaintext IRRECUPERÁVEL, mas nada no
// log append-only nem na hash-chain é mutado — a cadeia sela o HASH do ciphertext,
// não o plaintext. É a fronteira estável: produção liga um KMS real por trás desta
// mesma interface, sem alterar o trajectory store.
//
// EnsureKey é idempotente: provisiona a KEK do titular na primeira escrita e
// devolve a existente nas seguintes. DeleteKey é o acto de shredding (idempotente:
// apagar o que não existe é no-op).
type KeyStore interface {
	// EnsureKey devolve a KEK do titular, provisionando-a (via RandSource) se ainda
	// não existir. É o caminho de ESCRITA de um episódio.
	EnsureKey(subjectID string) ([]byte, error)
	// Key devolve a KEK do titular e se ela existe. É o caminho de LEITURA
	// (decifragem): se a chave foi shredded, ok=false e o episódio é irrecuperável.
	Key(subjectID string) (key []byte, ok bool)
	// DeleteKey apaga a KEK do titular (crypto-shredding). Idempotente.
	DeleteKey(subjectID string)
}

// InMemoryKeyStore é a implementação de referência do [KeyStore]: KEKs por titular
// em memória, segura para concorrência. É a implementação MVP; produção liga um KMS
// real (envelope encryption com chaves geridas) por trás da mesma porta.
type InMemoryKeyStore struct {
	mu   sync.Mutex
	rand RandSource
	keys map[string][]byte
}

// NewInMemoryKeyStore constrói um KeyStore vazio. rand nil cai em crypto/rand
// (produção); os testes injectam uma fonte determinística.
func NewInMemoryKeyStore(randSrc RandSource) *InMemoryKeyStore {
	if randSrc == nil {
		randSrc = cryptoRand
	}
	return &InMemoryKeyStore{rand: randSrc, keys: make(map[string][]byte)}
}

// EnsureKey implementa [KeyStore].
func (k *InMemoryKeyStore) EnsureKey(subjectID string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if key, ok := k.keys[subjectID]; ok {
		return cloneBytes(key), nil
	}
	key := make([]byte, kekSize)
	if err := k.rand(key); err != nil {
		return nil, err
	}
	k.keys[subjectID] = key
	return cloneBytes(key), nil
}

// Key implementa [KeyStore].
func (k *InMemoryKeyStore) Key(subjectID string) ([]byte, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	key, ok := k.keys[subjectID]
	if !ok {
		return nil, false
	}
	return cloneBytes(key), true
}

// DeleteKey implementa [KeyStore] (crypto-shredding, idempotente).
func (k *InMemoryKeyStore) DeleteKey(subjectID string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.keys, subjectID)
}

// sealed é o CIPHERTEXT de envelope de um episódio, guardado no log append-only.
// Envelope encryption em dois níveis (AES-256-GCM, stdlib):
//
//   - uma DEK (data key) aleatória por episódio cifra o plaintext → Ciphertext;
//   - a KEK do titular (do KeyStore) embrulha a DEK → WrappedDEK.
//
// Apagar a KEK (crypto-shredding) impede o desembrulho da DEK e, logo, a
// decifragem do Ciphertext — o episódio fica irrecuperável. O que fica no log
// (este blob) NUNCA é mutado; a hash-chain sela o seu HASH ([contentHash]).
type sealed struct {
	WrappedDEK []byte `json:"wrapped_dek"`
	DEKNonce   []byte `json:"dek_nonce"`
	Ciphertext []byte `json:"ciphertext"`
	Nonce      []byte `json:"nonce"`
}

// seal cifra plaintext por envelope sob a KEK dada. Gera a DEK e os nonces pela
// RandSource injectada (determinística em teste). O resultado é serializável
// (JSON) e vai INTEIRO para o log append-only.
//
// # Modelo de nonces (unicidade PROBABILÍSTICA, não absoluta)
//
// Os três valores (dek, nonce, dekNonce) são aleatórios de 96 bits — NÃO há
// contador nem verificação de colisão. A unicidade tem garantias distintas em cada
// nível:
//
//   - nonce (conteúdo): usado com a DEK, que é FRESCA por episódio. O par
//     (DEK, nonce) é, por isso, único por construção — não há reutilização de nonce
//     sob a mesma chave de conteúdo.
//   - dekNonce (embrulho da DEK): usado com a KEK do titular, que é ESTÁVEL e
//     reutilizada em TODOS os episódios do subject. A sua unicidade depende SÓ da
//     aleatoriedade de 96 bits (limite de aniversário ~2^48 embrulhos por KEK), não
//     de uma garantia absoluta. É prática GCM padrão para volumes MVP; produção que
//     ultrapasse o limite de aniversário por titular deve migrar o embrulho da DEK
//     para um nonce determinístico/contador por-KEK. Reutilizar (KEK, dekNonce)
//     vazaria o XOR de duas DEKs — daí a fronteira ser afirmada como probabilística.
func seal(kek, plaintext []byte, randSrc RandSource) (sealed, error) {
	dek := make([]byte, dekSize)
	if err := randSrc(dek); err != nil {
		return sealed{}, err
	}
	nonce := make([]byte, nonceSize)
	if err := randSrc(nonce); err != nil {
		return sealed{}, err
	}
	dekNonce := make([]byte, nonceSize)
	if err := randSrc(dekNonce); err != nil {
		return sealed{}, err
	}

	contentGCM, err := newGCM(dek)
	if err != nil {
		return sealed{}, err
	}
	ciphertext := contentGCM.Seal(nil, nonce, plaintext, nil)

	kekGCM, err := newGCM(kek)
	if err != nil {
		return sealed{}, err
	}
	wrapped := kekGCM.Seal(nil, dekNonce, dek, nil)

	return sealed{
		WrappedDEK: wrapped,
		DEKNonce:   dekNonce,
		Ciphertext: ciphertext,
		Nonce:      nonce,
	}, nil
}

// open decifra um blob de envelope sob a KEK dada (o inverso de [seal]).
// Fail-closed: uma KEK errada/ausente ou um blob adulterado devolve
// [ErrDecrypt] (a autenticação do GCM falha). É por aqui que o crypto-shredding se
// manifesta na leitura: sem a KEK, o open é impossível.
func open(kek []byte, s sealed) ([]byte, error) {
	kekGCM, err := newGCM(kek)
	if err != nil {
		return nil, err
	}
	dek, err := kekGCM.Open(nil, s.DEKNonce, s.WrappedDEK, nil)
	if err != nil {
		return nil, ErrDecrypt
	}
	contentGCM, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	plaintext, err := contentGCM.Open(nil, s.Nonce, s.Ciphertext, nil)
	if err != nil {
		return nil, ErrDecrypt
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

// contentHash é o HASH do ciphertext de envelope que a hash-chain de audit sela
// (PayloadRef.ContentHash). Hasheia a serialização canónica (JSON de campos com
// ordem fixa) do blob [sealed] — qualquer adulteração do ciphertext no log é, por
// isso, detectável pela re-verificação da cadeia. NÃO depende do plaintext nem da
// KEK: apagar a chave não altera este hash, pelo que a cadeia continua a verificar.
func contentHash(s sealed) ([]byte, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	return sum[:], nil
}

// hexHash formata um hash em hex (para o envelope do episódio no Event Store).
func hexHash(h []byte) string { return hex.EncodeToString(h) }

// cloneBytes devolve uma cópia independente (nil-safe).
func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
