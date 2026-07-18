package redaction

import (
	"crypto/rand"
	"io"
	"sync"
)

// keyRefPrefix domina as KeyRefs das chaves de tokenização por-titular. Espelha o
// keyRefPrefix do audit (AOS-083) sem lhe criar dependência: a KeyRef é derivável do
// titular, pelo que o shredder localiza a chave a destruir sem indexação lateral.
const keyRefPrefix = "aos.redaction.pii:"

// KeyRefFor devolve a KeyRef canónica da chave de tokenização de um titular.
func KeyRefFor(subject string) string { return keyRefPrefix + subject }

// randFill é a fonte de entropia do vault de referência (injectável para testes
// determinísticos). Preenche p por completo ou devolve erro (semântica io.ReadFull).
type randFill func(p []byte) error

func cryptoRandFill(p []byte) error {
	_, err := io.ReadFull(rand.Reader, p)
	return err
}

// InMemoryKeySource é a impl de REFERÊNCIA da porta [KeySource]: chaves AES-256
// por-titular em memória, segura para concorrência, com [InMemoryKeySource.Shred]
// para modelar o crypto-shredding (AOS-093). Produção injecta o KeyVault do audit
// (AOS-083) ou um KMS/HSM por trás da MESMA porta — este motor não gere o cofre real.
type InMemoryKeySource struct {
	mu   sync.Mutex
	rand randFill
	keys map[string][]byte // indexado por KeyRef
}

// NewInMemoryKeySource constrói um vault de referência vazio. randSrc nil cai em
// crypto/rand (produção); os testes injectam uma fonte determinística.
func NewInMemoryKeySource(randSrc func(p []byte) error) *InMemoryKeySource {
	if randSrc == nil {
		randSrc = cryptoRandFill
	}
	return &InMemoryKeySource{rand: randSrc, keys: make(map[string][]byte)}
}

// KeyFor implementa [KeySource]: provisiona a chave do titular na primeira chamada e
// devolve a existente nas seguintes (idempotente). Chave de 32 bytes (AES-256).
func (s *InMemoryKeySource) KeyFor(subject string) ([]byte, string, error) {
	ref := KeyRefFor(subject)
	s.mu.Lock()
	defer s.mu.Unlock()
	if key, ok := s.keys[ref]; ok {
		return cloneBytes(key), ref, nil
	}
	key := make([]byte, keySize)
	if err := s.rand(key); err != nil {
		return nil, "", err
	}
	s.keys[ref] = key
	return cloneBytes(key), ref, nil
}

// Shred destrói a chave do titular (crypto-shredding, GDPR Art. 17). Após o shred,
// qualquer token desse titular torna-se irresolúvel ([Resolve] devolve ok=false).
// Idempotente. Devolve a chave destruída e se ela existia (para o teste demonstrar
// que ANTES resolvia e DEPOIS já não).
func (s *InMemoryKeySource) Shred(subject string) ([]byte, bool) {
	ref := KeyRefFor(subject)
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.keys[ref]
	if !ok {
		return nil, false
	}
	delete(s.keys, ref)
	return cloneBytes(key), true
}

// KeyByRef devolve a chave por KeyRef e se ela existe (caminho de LEITURA/resolução).
func (s *InMemoryKeySource) KeyByRef(keyRef string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.keys[keyRef]
	if !ok {
		return nil, false
	}
	return cloneBytes(key), true
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
