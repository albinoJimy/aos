package audit

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"time"
)

// Checkpoint é uma âncora assinada da hash-chain: sela, num dado audit_seq, o
// EntryHash acumulado dessa partição. Assinado com ed25519, funciona como raiz
// de confiança para verificação eficiente — [VerifyFromCheckpoint] valida a
// assinatura e depois verifica APENAS cp+1..to, sem reprocessar desde a génese.
type Checkpoint struct {
	Partition string
	AuditSeq  uint64
	EntryHash []byte
	Timestamp time.Time
	// Signature é a assinatura ed25519 sobre a serialização canónica do checkpoint
	// (todos os campos EXCEPTO a própria Signature).
	Signature []byte
}

// checkpointDomain separa o domínio de assinatura de checkpoints do de registos.
const checkpointDomain = "aos.audit.checkpoint.v1"

// canonicalCheckpoint serializa o checkpoint de forma determinística e estável
// cross-SO (mesma disciplina de [canonicalContent]), EXCLUINDO a Signature.
func canonicalCheckpoint(cp Checkpoint) []byte {
	buf := make([]byte, 0, 96)
	buf = putString(buf, checkpointDomain)
	buf = putString(buf, cp.Partition)
	buf = putUint64(buf, cp.AuditSeq)
	buf = putBytes(buf, cp.EntryHash)
	buf = putInt64(buf, cp.Timestamp.UTC().UnixNano())
	return buf
}

// Signer sela checkpoints com uma chave privada ed25519. A chave privada vive
// FORA do repositório (KMS/HSM em produção); os testes usam pares efémeros.
type Signer struct {
	priv ed25519.PrivateKey
	now  func() time.Time
}

// SignerOption configura o Signer.
type SignerOption func(*Signer)

// withSignerClock injecta o relógio (uso interno/testes determinísticos).
func withSignerClock(f func() time.Time) SignerOption {
	return func(s *Signer) { s.now = f }
}

// NewSigner constrói um Signer a partir de uma chave privada ed25519.
func NewSigner(priv ed25519.PrivateKey, opts ...SignerOption) (*Signer, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, ErrInvalidKey
	}
	s := &Signer{priv: priv, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s, nil
}

// Public devolve a chave pública correspondente (âncora de confiança a distribuir
// aos verificadores).
func (s *Signer) Public() ed25519.PublicKey {
	return s.priv.Public().(ed25519.PublicKey)
}

// Seal lê o registo de audit_seq=seq na partição e sela o seu EntryHash num
// checkpoint assinado. Falha se o registo não existir.
func (s *Signer) Seal(ctx context.Context, store Store, partition string, seq uint64) (Checkpoint, error) {
	rec, ok, err := store.At(ctx, partition, seq)
	if err != nil {
		return Checkpoint{}, err
	}
	if !ok {
		return Checkpoint{}, ErrUnknownPartition
	}
	cp := Checkpoint{
		Partition: partition,
		AuditSeq:  seq,
		EntryHash: cloneBytes(rec.EntryHash),
		Timestamp: s.now().UTC(),
	}
	cp.Signature = ed25519.Sign(s.priv, canonicalCheckpoint(cp))
	return cp, nil
}

// VerifyCheckpoint valida a assinatura de um checkpoint contra a chave pública.
// Devolve [ErrCheckpointSignature] se a assinatura não corresponder.
func VerifyCheckpoint(pub ed25519.PublicKey, cp Checkpoint) error {
	if len(pub) != ed25519.PublicKeySize {
		return ErrInvalidKey
	}
	if !ed25519.Verify(pub, canonicalCheckpoint(cp), cp.Signature) {
		return ErrCheckpointSignature
	}
	return nil
}

// VerifyFromCheckpoint verifica a cadeia de forma eficiente a partir de uma
// âncora assinada:
//
//  1. valida a assinatura do checkpoint contra a chave pública (raiz de confiança);
//  2. confirma que o registo em cp.AuditSeq existe e o seu EntryHash == cp.EntryHash
//     (a âncora ancora de facto a cadeia real);
//  3. verifica SÓ cp.AuditSeq+1 .. to, usando cp.EntryHash como PrevHash esperado.
//
// Assim, verificar um intervalo recente de uma cadeia longa não exige reprocessar
// desde a génese. Devolve [ErrCheckpointSignature] para assinatura inválida,
// [ErrCheckpointAnchor] se a âncora não corresponder ao registo, ou um
// [*VerifyError] para adulteração no intervalo verificado.
func VerifyFromCheckpoint(ctx context.Context, store Store, pub ed25519.PublicKey, cp Checkpoint, to uint64) error {
	if err := VerifyCheckpoint(pub, cp); err != nil {
		return err
	}
	if to < cp.AuditSeq {
		return ErrInvalidRange
	}
	head, err := store.Head(ctx, cp.Partition)
	if err != nil {
		return err
	}
	if head == 0 {
		return ErrUnknownPartition
	}
	if to > head {
		return ErrRangeBeyondHead
	}

	// A âncora tem de corresponder ao registo real em cp.AuditSeq.
	anchorRec, ok, err := store.At(ctx, cp.Partition, cp.AuditSeq)
	if err != nil {
		return err
	}
	if !ok {
		return ErrCheckpointAnchor
	}
	// A âncora tem de corresponder ao registo real, em DOIS níveis:
	//
	//  (a) o campo EntryHash armazenado == cp.EntryHash (assinado) — apanha a
	//      corrupção directa do próprio campo de hash;
	//  (b) o EntryHash RECOMPUTADO a partir do CONTEÚDO (H(prevHash||conteúdo)) ==
	//      cp.EntryHash — fecha o ciclo do conteúdo (AOS-011-Q1). Sem (b), conteúdo
	//      adulterado (ex.: Decision allow→deny, Capability forjada) cujo campo
	//      EntryHash tenha sido mantido byte-a-byte igual ao checkpoint assinado
	//      passaria como verificado: um auditor leria a Decision/Capability forjadas
	//      e julgá-las-ia autênticas. O conteúdo recomputado de uma âncora adulterada
	//      diverge sempre do hash assinado.
	//
	// Ambas as condições têm de valer ⇒ caso contrário [ErrCheckpointAnchor].
	if !bytes.Equal(anchorRec.EntryHash, cp.EntryHash) {
		return ErrCheckpointAnchor
	}
	if !bytes.Equal(ComputeEntryHash(anchorRec.PrevHash, anchorRec), cp.EntryHash) {
		return ErrCheckpointAnchor
	}

	// Nada a verificar para além da âncora.
	if to == cp.AuditSeq {
		return nil
	}
	return verifyRange(ctx, store, cp.Partition, cp.AuditSeq+1, to, cp.EntryHash)
}
