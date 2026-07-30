package audit

import "sync"

// KeyWrapper é a PORTA DE ENVELOPE para custódia HSM *key-never-leaves* (AOS-216,
// residual de DEF-302/AOS-215). Estende o modelo de [KeyVault] para o caso em que a
// KEK do titular NUNCA pode sair do módulo de custódia: em vez de devolver a KEK crua
// ([KeyVault.Key] → []byte) para o embrulho da DEK correr IN-PROCESS, o embrulho e o
// desembrulho da DEK correm DENTRO do vault/HSM — só a DEK (efémera, por-registo)
// atravessa a fronteira. É a extensão que um HSM verdadeiro (PKCS#11, cloud KMS com
// AWS KMS `Encrypt`/`Decrypt`, HashiCorp Vault Transit) suporta e que a porta KEK-crua
// de AOS-215 não conseguia servir.
//
// Um vault que implemente [KeyWrapper] ALÉM de [KeyVault] faz [SealContent]/[OpenContent]
// tomarem o caminho de envelope (por type assertion); um vault que só implemente
// [KeyVault] mantém o caminho KEK-crua de AOS-093/215 (fallback), byte-a-byte igual.
//
// KEY-NEVER-LEAVES: a KEK nunca entra no processo do nó — nem no wrap, nem no unwrap,
// nem em log/span/erro. O que sai do módulo é o embrulho OPACO (`wrapped`) e uma
// referência de chave (`keyRef`) que identifica qual KEK (e versão) foi usada, sem a
// revelar. O HSM concreto é INFRA-ORG (fora do binário zero-dep); a impl de referência
// [InMemoryKeyWrapper] prova o CONTRATO in-process com stdlib.
type KeyWrapper interface {
	// WrapDEK embrulha a DEK do registo sob a KEK do titular DENTRO do módulo de
	// custódia e devolve o embrulho opaco mais a keyRef que [UnwrapDEK] exige. A KEK
	// é provisionada na 1ª chamada (idempotente por titular). A KEK NUNCA é devolvida.
	// dek é material efémero por-registo; o chamador não o persiste em claro.
	WrapDEK(subjectID string, dek []byte) (wrapped []byte, keyRef string, err error)
	// UnwrapDEK desembrulha, dentro do módulo, o embrulho produzido por [WrapDEK] e
	// devolve a DEK. FAIL-CLOSED: se a KEK identificada por keyRef foi destruída
	// (crypto-shredding, [KeyVault.Delete]) ou o embrulho não autentica, ok=false e a
	// DEK — logo o conteúdo — é IRRECUPERÁVEL. A KEK nunca entra no processo do nó.
	UnwrapDEK(keyRef string, wrapped []byte) (dek []byte, ok bool)
}

// InMemoryKeyWrapper é a implementação de REFERÊNCIA da porta de envelope [KeyWrapper]
// (AOS-216). Modela um HSM *key-never-leaves* in-process: as KEK por-titular vivem
// APENAS aqui dentro e o embrulho/desembrulho da DEK (AES-256-GCM, stdlib) corre
// INTERNAMENTE — a KEK NUNCA é devolvida ao chamador. Prova o seam sem qualquer
// dependência externa; o HSM concreto (PKCS#11/KMS) vive fora do binário.
//
// Satisfaz também [KeyVault] para poder ser passado onde a porta de vault é esperada
// (ex.: [SealContent], [Config.DSARVault]), MAS honra a custódia key-never-leaves:
//   - [Key] devolve SEMPRE (nil, false) — a KEK crua nunca é surrendida (é isto que
//     torna a via de envelope falsificável: seal/open funcionam mesmo com Key a falhar);
//   - [EnsureKey] provisiona a KEK internamente mas devolve key=nil (nunca a KEK crua);
//   - [Delete] destrói a KEK (crypto-shredding) ⇒ [UnwrapDEK] passa a falhar.
type InMemoryKeyWrapper struct {
	mu   sync.Mutex
	rand RandSource
	keks map[string][]byte // keyRef → KEK, NUNCA devolvida ao exterior
}

// NewInMemoryKeyWrapper constrói um wrapper vazio. randSrc nil cai em crypto/rand
// (produção); os testes injectam uma fonte determinística.
func NewInMemoryKeyWrapper(randSrc RandSource) *InMemoryKeyWrapper {
	if randSrc == nil {
		randSrc = cryptoRand
	}
	return &InMemoryKeyWrapper{rand: randSrc, keks: make(map[string][]byte)}
}

// ensureLocked devolve a KEK do titular (provisionando-a se ausente). Chamado sob lock.
// A KEK devolvida é a referência INTERNA — nunca escapa deste ficheiro.
func (w *InMemoryKeyWrapper) ensureLocked(ref string) ([]byte, error) {
	if kek, ok := w.keks[ref]; ok {
		return kek, nil
	}
	kek := make([]byte, kekSize)
	if err := w.rand(kek); err != nil {
		return nil, err
	}
	w.keks[ref] = kek
	return kek, nil
}

// WrapDEK implementa [KeyWrapper]: embrulha a DEK sob a KEK do titular com AES-256-GCM
// e um nonce fresco (da RandSource), DENTRO do módulo. O embrulho é auto-contido
// (nonce ‖ ciphertext), pelo que [UnwrapDEK] não precisa de nonce externo. A KEK não sai.
func (w *InMemoryKeyWrapper) WrapDEK(subjectID string, dek []byte) ([]byte, string, error) {
	if subjectID == "" {
		return nil, "", ErrNoSubject
	}
	ref := KeyRefFor(subjectID)
	w.mu.Lock()
	defer w.mu.Unlock()
	kek, err := w.ensureLocked(ref)
	if err != nil {
		return nil, "", err
	}
	gcm, err := newPayloadGCM(kek)
	if err != nil {
		return nil, "", err
	}
	nonce := make([]byte, nonceSize)
	if err := w.rand(nonce); err != nil {
		return nil, "", err
	}
	// wrapped = nonce ‖ Seal(...): Seal acrescenta o ciphertext ao prefixo nonce.
	wrapped := gcm.Seal(cloneBytes(nonce), nonce, dek, nil)
	return wrapped, ref, nil
}

// UnwrapDEK implementa [KeyWrapper]: desembrulha DENTRO do módulo. FAIL-CLOSED — KEK
// destruída (shred) ou embrulho adulterado ⇒ ok=false. A KEK nunca entra no processo.
func (w *InMemoryKeyWrapper) UnwrapDEK(keyRef string, wrapped []byte) ([]byte, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	kek, ok := w.keks[keyRef]
	if !ok {
		return nil, false // KEK destruída (crypto-shredding) — irrecuperável
	}
	if len(wrapped) < nonceSize {
		return nil, false
	}
	gcm, err := newPayloadGCM(kek)
	if err != nil {
		return nil, false
	}
	nonce, ct := wrapped[:nonceSize], wrapped[nonceSize:]
	dek, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, false
	}
	return dek, true
}

// EnsureKey implementa [KeyVault] honrando key-never-leaves: provisiona a KEK
// INTERNAMENTE mas NUNCA a devolve (key=nil). O caminho de escrita de conteúdo usa
// [WrapDEK], não isto; o key=nil impede qualquer chamador de obter a KEK crua por engano.
func (w *InMemoryKeyWrapper) EnsureKey(subjectID string) ([]byte, string, error) {
	if subjectID == "" {
		return nil, "", ErrNoSubject
	}
	ref := KeyRefFor(subjectID)
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.ensureLocked(ref); err != nil {
		return nil, "", err
	}
	return nil, ref, nil
}

// Key implementa [KeyVault] mas HONRA a custódia key-never-leaves: a KEK crua NUNCA é
// surrendida ⇒ devolve sempre (nil, false). É esta recusa que faz a via de envelope ser
// falsificável — [OpenContent] decifra SEM nunca obter a KEK crua deste vault.
func (w *InMemoryKeyWrapper) Key(keyRef string) ([]byte, bool) { return nil, false }

// Delete implementa [KeyVault] (crypto-shredding, idempotente): destrói a KEK do titular
// DENTRO do módulo ⇒ [UnwrapDEK] passa a falhar e o conteúdo fica irrecuperável.
func (w *InMemoryKeyWrapper) Delete(subjectID string) {
	ref := KeyRefFor(subjectID)
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.keks, ref)
}

// Asserções de compile-time: o wrapper de referência satisfaz ambas as portas.
var (
	_ KeyVault   = (*InMemoryKeyWrapper)(nil)
	_ KeyWrapper = (*InMemoryKeyWrapper)(nil)
)
