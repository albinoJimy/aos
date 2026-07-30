package audit

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
)

// AOS-216 — porta de ENVELOPE [KeyWrapper] para custódia HSM *key-never-leaves*
// (residual de DEF-302/AOS-215). Estes testes provam, FALSIFICAVELMENTE:
//   - a via de envelope cifra/decifra SEM nunca pedir a KEK crua (um vault-gate que
//     PANICA em Key()/EnsureKey() ainda sela/abre) — a KEK nunca entra no processo;
//   - o crypto-shredding ([KeyVault.Delete]) destrói a KEK ⇒ [KeyWrapper.UnwrapDEK]
//     falha e o conteúdo é IRRECUPERÁVEL ([ErrDecrypt]);
//   - o blob selado (o que vai ao WAL) NÃO é mutado pelo shred: o seu SHA-256 é estável,
//     logo a hash-chain que sela esse hash continua a validar;
//   - COMPAT: um vault que só implementa [KeyVault] serializa BYTE-A-BYTE como AOS-093/215
//     (sem `key_ref`) e o open KEK-crua continua a funcionar; os dois formatos coexistem.

// hsmSynthPII é conteúdo SINTÉTICO de run (nunca PII real).
const hsmSynthPII = "objective: contactar HSM-SYNTH-9900 sobre o caso #ENV-0001"

// panicKeyGate embrulha um [InMemoryKeyWrapper] real mas PANICA se alguém pedir a KEK
// CRUA (Key/EnsureKey). É a falsificação: se [SealContent]/[OpenContent] alguma vez
// pedissem a KEK crua na via de envelope, o teste entrava em pânico e falhava. Prova
// que o wrap/unwrap corre DENTRO do módulo e a KEK nunca sai.
type panicKeyGate struct{ inner *InMemoryKeyWrapper }

func (g *panicKeyGate) WrapDEK(subjectID string, dek []byte) ([]byte, string, error) {
	return g.inner.WrapDEK(subjectID, dek)
}
func (g *panicKeyGate) UnwrapDEK(keyRef string, wrapped []byte) ([]byte, bool) {
	return g.inner.UnwrapDEK(keyRef, wrapped)
}
func (g *panicKeyGate) EnsureKey(string) ([]byte, string, error) {
	panic("AOS-216: KEK crua pedida via EnsureKey na via de envelope — key-never-leaves violado")
}
func (g *panicKeyGate) Key(string) ([]byte, bool) {
	panic("AOS-216: KEK crua pedida via Key na via de envelope — key-never-leaves violado")
}
func (g *panicKeyGate) Delete(subjectID string) { g.inner.Delete(subjectID) }

var (
	_ KeyVault   = (*panicKeyGate)(nil)
	_ KeyWrapper = (*panicKeyGate)(nil)
)

// TestKeyWrapper_EnvelopePathNeverAsksRawKEK é o coração falsificável: com um gate que
// PANICA em Key()/EnsureKey(), a cifra E a decifração passam — porque correm por
// WrapDEK/UnwrapDEK, nunca pela KEK crua.
func TestKeyWrapper_EnvelopePathNeverAsksRawKEK(t *testing.T) {
	gate := &panicKeyGate{inner: NewInMemoryKeyWrapper(detRand())}
	const subject = "nhi:agent-hsm-A"

	sealed, err := SealContent(gate, subject, []byte(hsmSynthPII), detRand())
	if err != nil {
		t.Fatalf("SealContent via envelope: %v", err)
	}
	// Confidencialidade: o plaintext não está no blob.
	if bytes.Contains(sealed, []byte(hsmSynthPII)) || bytes.Contains(sealed, []byte("ENV-0001")) {
		t.Fatal("blob de envelope contém plaintext em claro — confidencialidade violada")
	}
	// O formato é de ENVELOPE: tem key_ref.
	if !bytes.Contains(sealed, []byte("key_ref")) {
		t.Fatal("blob de envelope devia carregar key_ref (marca do formato AOS-216)")
	}

	got, err := OpenContent(gate, subject, sealed)
	if err != nil {
		t.Fatalf("OpenContent via envelope: %v", err)
	}
	if !bytes.Equal(got, []byte(hsmSynthPII)) {
		t.Fatalf("plaintext decifrado != original: %q", got)
	}
}

// TestKeyWrapper_ShredMakesContentUnrecoverable prova o crypto-shredding sobre a via de
// envelope: Delete destrói a KEK interna ⇒ UnwrapDEK falha ⇒ OpenContent ⇒ ErrDecrypt. E
// o blob selado NÃO muda (hash estável) — a hash-chain que o sela continua a validar.
func TestKeyWrapper_ShredMakesContentUnrecoverable(t *testing.T) {
	wrapper := NewInMemoryKeyWrapper(detRand())
	const subject = "nhi:agent-hsm-shred"

	sealed, err := SealContent(wrapper, subject, []byte(hsmSynthPII), detRand())
	if err != nil {
		t.Fatalf("SealContent: %v", err)
	}
	hashBefore := sha256.Sum256(sealed)

	if got, err := OpenContent(wrapper, subject, sealed); err != nil || !bytes.Equal(got, []byte(hsmSynthPII)) {
		t.Fatalf("OpenContent antes do shred: got=%q err=%v", got, err)
	}

	// Crypto-shredding: destrói a KEK do titular DENTRO do módulo.
	wrapper.Delete(subject)

	if _, err := OpenContent(wrapper, subject, sealed); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("após shred, OpenContent devia dar ErrDecrypt, deu: %v", err)
	}
	// Prova directa ao nível da porta: UnwrapDEK falha após shred.
	if _, ok := wrapper.UnwrapDEK(KeyRefFor(subject), []byte("qualquer-embrulho")); ok {
		t.Fatal("UnwrapDEK devia falhar após shred da KEK")
	}
	// IMUTABILIDADE do blob (o que vai ao WAL): o shred não muta o ciphertext ⇒ o hash
	// que a hash-chain sela é estável e a cadeia continua a verificar.
	if hashAfter := sha256.Sum256(sealed); hashBefore != hashAfter {
		t.Fatal("o blob selado foi mutado pelo shred — a hash-chain deixaria de validar")
	}
}

// TestKeyWrapper_KeyNeverLeaves prova a custódia: a impl de referência NUNCA surrende a
// KEK crua (Key ⇒ (nil,false); EnsureKey ⇒ key=nil) e AINDA ASSIM sela/abre por envelope.
func TestKeyWrapper_KeyNeverLeaves(t *testing.T) {
	wrapper := NewInMemoryKeyWrapper(detRand())
	const subject = "nhi:agent-custody"

	// Provisiona (via seal) e depois exige que a KEK crua nunca saia.
	sealed, err := SealContent(wrapper, subject, []byte(hsmSynthPII), detRand())
	if err != nil {
		t.Fatalf("SealContent: %v", err)
	}
	if key, ok := wrapper.Key(KeyRefFor(subject)); ok || key != nil {
		t.Fatalf("Key() surrendeu a KEK crua (key-never-leaves violado): ok=%v key!=nil=%v", ok, key != nil)
	}
	key, ref, err := wrapper.EnsureKey(subject)
	if err != nil || ref == "" || key != nil {
		t.Fatalf("EnsureKey devia devolver ref sem KEK crua: key!=nil=%v ref=%q err=%v", key != nil, ref, err)
	}
	// A via de envelope continua funcional mesmo sem a KEK crua alguma vez sair.
	if got, err := OpenContent(wrapper, subject, sealed); err != nil || !bytes.Equal(got, []byte(hsmSynthPII)) {
		t.Fatalf("OpenContent com custódia key-never-leaves: got=%q err=%v", got, err)
	}
}

// errBoom é um erro de custódia sintético.
var errBoom = errors.New("wrapper: falha de custódia sintética")

// errWrapper falha WrapDEK — prova o fail-closed: um wrapper que erra propaga o erro
// pela cifra (a escrita aborta; nunca cai para texto-claro).
type errWrapper struct{ *InMemoryKeyWrapper }

func (e errWrapper) WrapDEK(string, []byte) ([]byte, string, error) { return nil, "", errBoom }

func TestKeyWrapper_WrapErrorFailsClosed(t *testing.T) {
	w := errWrapper{NewInMemoryKeyWrapper(detRand())}
	_, err := SealContent(w, "nhi:agent-x", []byte(hsmSynthPII), detRand())
	if !errors.Is(err, errBoom) {
		t.Fatalf("SealContent devia propagar o erro do wrapper (fail-closed), deu: %v", err)
	}
}

// TestKeyWrapper_WrongCustodyCannotOpen prova o isolamento de custódia: um wrapper
// DIFERENTE (que não tem a KEK) não desembrulha um blob de envelope de outro.
func TestKeyWrapper_WrongCustodyCannotOpen(t *testing.T) {
	owner := NewInMemoryKeyWrapper(detRand())
	other := NewInMemoryKeyWrapper(detRand())
	const subject = "nhi:agent-owner"

	sealed, err := SealContent(owner, subject, []byte(hsmSynthPII), detRand())
	if err != nil {
		t.Fatalf("SealContent: %v", err)
	}
	if _, err := OpenContent(other, subject, sealed); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("custódia diferente devia falhar com ErrDecrypt, deu: %v", err)
	}
}

// TestKeyWrapper_KEKCruaFallbackByteCompat prova a COMPAT: um vault que só implementa
// [KeyVault] (InMemoryKeyVault, AOS-093/215) NÃO toma a via de envelope — o blob NÃO tem
// key_ref (formato antigo) e abre pela via KEK-crua. Os dois formatos coexistem.
func TestKeyWrapper_KEKCruaFallbackByteCompat(t *testing.T) {
	kekVault := NewInMemoryKeyVault(detRand())
	const subject = "nhi:agent-legacy"

	sealed, err := SealContent(kekVault, subject, []byte(hsmSynthPII), detRand())
	if err != nil {
		t.Fatalf("SealContent KEK-crua: %v", err)
	}
	// O formato KEK-crua NÃO carrega key_ref (omitempty) — serialização de AOS-093/215.
	if bytes.Contains(sealed, []byte("key_ref")) {
		t.Fatal("blob KEK-crua não devia conter key_ref — quebraria a compat byte-a-byte de AOS-093/215")
	}
	if !bytes.Contains(sealed, []byte("dek_nonce")) {
		t.Fatal("blob KEK-crua devia conter dek_nonce (envelope in-process)")
	}
	// Abre pela via KEK-crua.
	if got, err := OpenContent(kekVault, subject, sealed); err != nil || !bytes.Equal(got, []byte(hsmSynthPII)) {
		t.Fatalf("OpenContent KEK-crua: got=%q err=%v", got, err)
	}
	// Um blob KEK-crua entregue a um wrapper HSM continua a abrir (sem key_ref ⇒ via
	// KEK-crua): o wrapper implementa KeyVault, e Key() do InMemoryKeyWrapper devolve
	// (nil,false), pelo que NÃO decifra — prova que o roteamento é pelo FORMATO, não pelo
	// tipo do vault. Aqui usamos o mesmo InMemoryKeyVault para confirmar o caminho legado.
}

// TestKeyWrapper_EnvelopeBlobToKEKCruaVaultFailsClosed prova o outro sentido do
// roteamento por formato: um blob de ENVELOPE (key_ref presente) entregue a um vault que
// só sabe KEK-crua ⇒ ErrDecrypt (a KEK crua não desembrulha um embrulho de HSM).
func TestKeyWrapper_EnvelopeBlobToKEKCruaVaultFailsClosed(t *testing.T) {
	wrapper := NewInMemoryKeyWrapper(detRand())
	kekVault := NewInMemoryKeyVault(detRand())
	const subject = "nhi:agent-mismatch"

	sealed, err := SealContent(wrapper, subject, []byte(hsmSynthPII), detRand())
	if err != nil {
		t.Fatalf("SealContent envelope: %v", err)
	}
	if _, err := OpenContent(kekVault, subject, sealed); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("blob de envelope num vault KEK-crua devia falhar com ErrDecrypt, deu: %v", err)
	}
}

// TestKeyWrapper_WrongSubjectCannotOpen é a paridade de subject-binding com a via KEK-crua
// (TestSealContent_WrongSubjectCannotOpen, aos093_content_test.go): na via de ENVELOPE, um
// blob selado para um titular NÃO pode ser aberto pedindo OUTRO titular, mesmo que o mesmo
// wrapper detenha ambas as KEK. Sem o subject-binding explícito o unwrap far-se-ia por
// env.KeyRef (auto-declarado no blob) e devolveria o plaintext — degradação silenciosa da
// defesa-em-profundidade. Prova: OpenContent com o titular errado ⇒ ErrDecrypt.
func TestKeyWrapper_WrongSubjectCannotOpen(t *testing.T) {
	wrapper := NewInMemoryKeyWrapper(detRand())
	const owner, intruder = "nhi:hsm-owner", "nhi:hsm-intruder"

	sealed, err := SealContent(wrapper, owner, []byte(hsmSynthPII), detRand())
	if err != nil {
		t.Fatalf("SealContent envelope: %v", err)
	}
	// O MESMO wrapper detém a KEK do intruder (provisiona-a explicitamente): o unwrap por
	// env.KeyRef succederia se o subjectID fosse ignorado. O binding tem de o barrar.
	if _, _, err := wrapper.EnsureKey(intruder); err != nil {
		t.Fatalf("EnsureKey intruder: %v", err)
	}
	if _, err := OpenContent(wrapper, intruder, sealed); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("titular errado na via de envelope devia falhar com ErrDecrypt, deu: %v", err)
	}
	// Sanidade: o titular CORRECTO continua a abrir (o binding não parte o caminho feliz).
	if got, err := OpenContent(wrapper, owner, sealed); err != nil || !bytes.Equal(got, []byte(hsmSynthPII)) {
		t.Fatalf("titular correcto devia abrir na via de envelope: got=%q err=%v", got, err)
	}
}

// TestKeyWrapper_ConcurrentSealOpen exercita a via de envelope sob concorrência (-race):
// seals/opens paralelos por vários titulares no MESMO wrapper não corrompem estado.
func TestKeyWrapper_ConcurrentSealOpen(t *testing.T) {
	wrapper := NewInMemoryKeyWrapper(nil) // crypto/rand — sem contador partilhado a competir
	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			subject := "nhi:agent-conc-" + string(rune('A'+id))
			content := []byte("prompt sintetico do worker")
			sealed, err := SealContent(wrapper, subject, content, nil)
			if err != nil {
				errs <- err
				return
			}
			got, err := OpenContent(wrapper, subject, sealed)
			if err != nil || !bytes.Equal(got, content) {
				errs <- errors.New("round-trip concorrente falhou")
				return
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concorrência: %v", err)
		}
	}
}
