package backup

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
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
//
// O prefixo `aos.` é RESERVADO aos titulares internos do nó (AOS-453): o DSAR recusa um
// subject_id que comece por ele, para que um pedido de apagamento de um titular externo nunca
// possa nomear — e destruir — a KEK do backup.
const backupSubjectPrefix = "aos.backup:"

// backupSubjectFor devolve o titular da KEK do backup para uma região de soberania. Fonte ÚNICA:
// o exportador sela sob ele e o restaurador/retoma exigem que o segmento seja dele.
func backupSubjectFor(region string) string {
	return backupSubjectPrefix + normalizeRegion(region)
}

// wrapEnvelope é o discriminador EXPLÍCITO do formato de embrulho da DEK (AOS-453). O formato
// KEK-crua (o de AOS-101) serializa SEMPRE `key_ref` e `dek_nonce`, pelo que o discriminador do
// audit (presença de `key_ref`) não serve aqui: é preciso um campo que só o formato novo tem.
//
//   - `wrap` ausente ⇒ formato KEK-crua: a KEK do titular embrulha a DEK IN-PROCESS (AES-GCM com
//     `dek_nonce`), e a abertura precisa de [audit.KeyVault.Key];
//   - `wrap: "envelope"` ⇒ a DEK foi embrulhada DENTRO da custódia ([audit.KeyWrapper.WrapDEK],
//     p.ex. Vault Transit) — a KEK nunca entrou no processo e a abertura é [audit.KeyWrapper.UnwrapDEK];
//   - qualquer outro valor ⇒ formato desconhecido, recusado (fail-closed).
const wrapEnvelope = "envelope"

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
//   - a KEK do titular do backup embrulha a DEK → WrappedDEK — in-process (KEK-crua, com
//     DEKNonce) ou dentro da custódia (Wrap == [wrapEnvelope], sem DEKNonce).
//
// O blob serializado (JSON) é o que vai INTEIRO para o ImmutableStore; o manifesto
// sela apenas o SHA-256 deste blob, nunca o plaintext.
//
// COMPATIBILIDADE: os dois `omitempty` não mudam UM byte do formato KEK-crua — nele `dek_nonce`
// tem sempre 12 bytes e `wrap` é sempre vazio (fixado em TestAOS453_FormatoKEKCruaByteAByte).
type encryptedSegment struct {
	KeyRef     string `json:"key_ref"`
	WrappedDEK []byte `json:"wrapped_dek"`
	DEKNonce   []byte `json:"dek_nonce,omitempty"`
	Ciphertext []byte `json:"ciphertext"`
	Nonce      []byte `json:"nonce"`
	Wrap       string `json:"wrap,omitempty"`
}

// sealSegment cifra plaintext por envelope sob a KEK do titular subjectID.
//
// Dois caminhos, escolhidos pela CUSTÓDIA (molde de [audit.SealContent]):
//
//   - o vault implementa [audit.KeyWrapper] (custódia *key-never-leaves*, p.ex. Vault Transit) ⇒
//     a DEK é embrulhada DENTRO da custódia e o segmento leva `wrap: "envelope"` (AOS-453);
//   - senão ⇒ o caminho KEK-crua de sempre (a KEK vem de [audit.KeyVault.EnsureKey] e embrulha a
//     DEK in-process), serializado BYTE A BYTE como antes.
//
// Gera a DEK e os nonces pela RandSource injectada (determinística em teste).
func sealSegment(vault audit.KeyVault, subjectID string, plaintext []byte, randSrc audit.RandSource) (encryptedSegment, error) {
	if wrapper, ok := vault.(audit.KeyWrapper); ok {
		return sealSegmentWrapped(wrapper, subjectID, plaintext, randSrc)
	}
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

// sealSegmentWrapped é o caminho de ENVELOPE (AOS-453): DEK fresca por segmento, cifra do
// conteúdo in-process, e o embrulho da DEK DENTRO da custódia. A KEK nunca entra no processo.
func sealSegmentWrapped(wrapper audit.KeyWrapper, subjectID string, plaintext []byte, randSrc audit.RandSource) (encryptedSegment, error) {
	dek := make([]byte, dekSize)
	if err := randSrc(dek); err != nil {
		return encryptedSegment{}, err
	}
	nonce := make([]byte, nonceSize)
	if err := randSrc(nonce); err != nil {
		return encryptedSegment{}, err
	}
	contentGCM, err := newGCM(dek)
	if err != nil {
		return encryptedSegment{}, err
	}
	ciphertext := contentGCM.Seal(nil, nonce, plaintext, nil)
	wrapped, keyRef, err := wrapper.WrapDEK(subjectID, dek)
	if err != nil {
		return encryptedSegment{}, err
	}
	if keyRef == "" || len(wrapped) == 0 {
		// Contrato do wrapper: sem keyRef o segmento não se encaminha para o UnwrapDEK. Fail-closed.
		return encryptedSegment{}, fmt.Errorf("%w: a custodia devolveu um embrulho sem keyRef", ErrKEKCustodyUnavailable)
	}
	return encryptedSegment{
		KeyRef:     keyRef,
		WrappedDEK: wrapped,
		Ciphertext: ciphertext,
		Nonce:      nonce,
		Wrap:       wrapEnvelope,
	}, nil
}

// openSegment decifra um blob de envelope do titular subjectID (o inverso de sealSegment).
//
// SUBJECT-BINDING nos dois formatos (paridade com [audit.OpenContent]): o segmento tem de ser do
// titular pedido — `KeyRef == audit.KeyRefFor(subjectID)` —, senão [ErrRestoreVerify] ANTES de
// tocar na custódia. O titular é o da região do backup; um segmento de outra região/titular não
// se abre com esta KEK, e dizê-lo é melhor do que deixar o GCM falhar como «adulterado».
//
// Fail-closed, e com a causa certa:
//   - KEK ausente/destruída/indisponível (Key → false; UnwrapDEK → false; custódia trocada) ⇒
//     [ErrRestoreVerify] — não há como abrir, e não é adulteração;
//   - o conteúdo (ou, no formato KEK-crua, o embrulho in-process) não autentica ⇒
//     [ErrSegmentTampered].
func openSegment(vault audit.KeyVault, subjectID string, seg encryptedSegment) ([]byte, error) {
	if subjectID == "" || seg.KeyRef != audit.KeyRefFor(subjectID) {
		return nil, fmt.Errorf("%w: o segmento nao e do titular do backup desta regiao (key_ref do segmento != KEK de %q)", ErrRestoreVerify, subjectID)
	}
	var dek []byte
	switch seg.Wrap {
	case wrapEnvelope:
		wrapper, ok := vault.(audit.KeyWrapper)
		if !ok {
			return nil, fmt.Errorf("%w: segmento selado por envelope (custodia key-never-leaves) e a custodia deste processo nao sabe desembrulhar", ErrRestoreVerify)
		}
		d, ok := wrapper.UnwrapDEK(seg.KeyRef, seg.WrappedDEK)
		if !ok {
			return nil, fmt.Errorf("%w: a custodia nao desembrulhou a DEK (KEK do backup destruida, ausente ou indisponivel)", ErrRestoreVerify)
		}
		dek = d
	case "":
		kek, ok := vault.Key(seg.KeyRef)
		if !ok {
			return nil, ErrRestoreVerify
		}
		kekGCM, err := newGCM(kek)
		if err != nil {
			return nil, err
		}
		d, err := kekGCM.Open(nil, seg.DEKNonce, seg.WrappedDEK, nil)
		if err != nil {
			return nil, ErrSegmentTampered
		}
		dek = d
	default:
		return nil, fmt.Errorf("%w: formato de embrulho desconhecido %q", ErrRestoreVerify, seg.Wrap)
	}
	contentGCM, err := newGCM(dek)
	if err != nil {
		return nil, ErrSegmentTampered
	}
	plaintext, err := contentGCM.Open(nil, seg.Nonce, seg.Ciphertext, nil)
	if err != nil {
		return nil, ErrSegmentTampered
	}
	return plaintext, nil
}

// checkKEKCustody faz valer, na COMPOSIÇÃO do exportador, que a custódia sabe selar segmentos
// (AOS-453, critério 3) — em vez de o descobrir ciclo a ciclo com `invalid key size 0`:
//
//   - custódia de ENVELOPE ([audit.KeyWrapper]) ⇒ uma volta completa WrapDEK→UnwrapDEK sob o
//     titular do backup. Prova, antes da retoma, que a custódia RESPONDE e AUTORIZA (Vault
//     alcançável, token válido, mount e política certos). Falhar aqui é
//     [ErrKEKCustodyUnavailable], que é outra coisa que «a KEK não é a que selou a cadeia»: sem
//     esta sonda, uma custódia em baixo lia-se na retoma como KEK errada;
//   - custódia KEK-crua ⇒ [audit.KeyVault.EnsureKey] tem de devolver uma KEK de 32 bytes. Uma que
//     não a entregue (key-never-leaves) sem implementar o envelope é [ErrKEKCustodyUnsupported].
func checkKEKCustody(vault audit.KeyVault, subjectID string, randSrc audit.RandSource) error {
	if wrapper, ok := vault.(audit.KeyWrapper); ok {
		probe := make([]byte, dekSize)
		if err := randSrc(probe); err != nil {
			return err
		}
		wrapped, keyRef, err := wrapper.WrapDEK(subjectID, probe)
		if err != nil {
			return fmt.Errorf("%w: a sonda WrapDEK sob a KEK do backup falhou: %w", ErrKEKCustodyUnavailable, err)
		}
		if keyRef != audit.KeyRefFor(subjectID) {
			return fmt.Errorf("%w: a custodia devolveu um keyRef que nao e o do titular do backup", ErrKEKCustodyUnavailable)
		}
		got, ok := wrapper.UnwrapDEK(keyRef, wrapped)
		if !ok || !bytes.Equal(got, probe) {
			return fmt.Errorf("%w: a sonda UnwrapDEK do que a propria custodia acabou de embrulhar falhou (custodia em baixo, token/politica sem decrypt, ou portao fechado)", ErrKEKCustodyUnavailable)
		}
		return nil
	}
	kek, _, err := vault.EnsureKey(subjectID)
	if err != nil {
		return fmt.Errorf("%w: EnsureKey da KEK do backup falhou: %w", ErrKEKCustodyUnavailable, err)
	}
	if len(kek) != kekSize {
		return fmt.Errorf("%w (%T entregou uma KEK de %d bytes; esperados %d)", ErrKEKCustodyUnsupported, vault, len(kek), kekSize)
	}
	return nil
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
