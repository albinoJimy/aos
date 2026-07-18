package audit

import (
	"errors"
	"fmt"
)

// Sentinelas de erro do audit. Use errors.Is para ramificar. Todos os erros de
// adulteração detectados por [Verify]/[VerifyFromCheckpoint] desembrulham para
// [ErrTampered], permitindo um teste único "a cadeia foi adulterada?".
var (
	// ErrTampered é o sentinela-raiz de qualquer adulteração detectada na cadeia.
	ErrTampered = errors.New("audit: cadeia adulterada")
	// ErrInvalidRange — intervalo [from,to] inválido (from<1 ou to<from).
	ErrInvalidRange = errors.New("audit: intervalo invalido")
	// ErrRangeBeyondHead — o intervalo pedido excede o último audit_seq da partição.
	ErrRangeBeyondHead = errors.New("audit: intervalo alem do head da particao")
	// ErrUnknownPartition — partição sem registos.
	ErrUnknownPartition = errors.New("audit: particao desconhecida")
	// ErrCheckpointSignature — a assinatura do checkpoint não valida contra a
	// chave pública (âncora de confiança rejeitada).
	ErrCheckpointSignature = errors.New("audit: assinatura de checkpoint invalida")
	// ErrCheckpointAnchor — o EntryHash do checkpoint não corresponde ao registo
	// desse audit_seq na cadeia (âncora não ancora nada verificável).
	ErrCheckpointAnchor = errors.New("audit: ancora de checkpoint nao corresponde ao registo")
	// ErrCheckpointStale — o checkpoint apresentado sela um audit_seq INFERIOR ao
	// head que o verificador conhece de forma independente do store (ver
	// [VerifyFromCheckpointAtHead]). Sinaliza rollback de checkpoint: a
	// reapresentação de um checkpoint antigo, validamente assinado, para mascarar
	// truncatura do tail dos registos posteriores a ele.
	ErrCheckpointStale = errors.New("audit: checkpoint anterior ao head conhecido (rollback)")
	// ErrInvalidKey — material de chave ed25519 com dimensão inválida.
	ErrInvalidKey = errors.New("audit: chave ed25519 invalida")
	// ErrNoSubject — ingestão de PII sem SubjectID, ou shred sem titular. Sem
	// titular não há chave por-titular a que cifrar/apagar (fail-closed).
	ErrNoSubject = errors.New("audit: subject_id obrigatorio para PII")
	// ErrDecrypt — o payload cifrado não autentica sob a chave/blob dados
	// (adulteração do ciphertext, ou KEK errada). Fail-closed na decifragem.
	ErrDecrypt = errors.New("audit: decifragem de payload falhou")
	// ErrShredded — a chave por titular foi destruída (crypto-shredding): o payload
	// pessoal é IRRECUPERÁVEL (GDPR Art. 17). A cadeia mantém-se íntegra.
	ErrShredded = errors.New("audit: chave do titular destruida (payload irrecuperavel)")
	// ErrPayloadMissing — a referência não localiza ciphertext no PayloadStore (ref
	// vazia/desconhecida). Distinto de [ErrShredded]: aqui falta o blob, não a chave.
	ErrPayloadMissing = errors.New("audit: payload cifrado ausente")
	// ErrLegalHold — tentativa de shred/purge de um titular sob legal hold. A
	// obrigação de preservação SUSPENDE o apagamento (fail-closed), mesmo após a
	// retenção expirar.
	ErrLegalHold = errors.New("audit: titular sob legal hold (shred suspenso)")
	// ErrRetentionActive — purge automático pedido antes de a retenção da classe
	// expirar. O DSAR explícito ([Shredder.Shred]) não está sujeito a esta barreira.
	ErrRetentionActive = errors.New("audit: retencao ainda activa (purge negado)")
	// ErrNoSigner — [IngestPipeline.Seal] sem um Signer ligado (ver WithIngestSigner).
	ErrNoSigner = errors.New("audit: pipeline sem signer para selagem")
)

// TamperType classifica a natureza da adulteração detectada.
type TamperType string

const (
	// TamperMutation — um campo do registo foi alterado (EntryHash recalculado
	// diverge do armazenado).
	TamperMutation TamperType = "mutation"
	// TamperRemoval — um ou mais registos foram removidos (gap ascendente em
	// audit_seq, ou falta de registos até `to`).
	TamperRemoval TamperType = "removal"
	// TamperInsertion — um registo foi inserido/duplicado (audit_seq fora de
	// ordem ou repetido).
	TamperInsertion TamperType = "insertion"
	// TamperChainBroken — o PrevHash de um registo não corresponde ao EntryHash do
	// anterior (encadeamento quebrado; sintoma de inserção/remoção/mutação vizinha).
	TamperChainBroken TamperType = "chain_broken"
)

// VerifyError identifica o registo e o tipo de adulteração detectados na
// verificação da cadeia. Desembrulha para [ErrTampered].
type VerifyError struct {
	// Type é a natureza da adulteração.
	Type TamperType
	// Partition é a partição afectada.
	Partition string
	// Seq é o audit_seq do registo onde a adulteração foi detectada (ou o seq
	// esperado, no caso de remoção).
	Seq uint64
	// Detail descreve o sintoma concreto.
	Detail string
}

func (e *VerifyError) Error() string {
	return fmt.Sprintf("audit: adulteracao %s na particao %q, audit_seq=%d: %s",
		e.Type, e.Partition, e.Seq, e.Detail)
}

// Unwrap liga o VerifyError ao sentinela-raiz [ErrTampered].
func (e *VerifyError) Unwrap() error { return ErrTampered }

// tamper constrói um *VerifyError.
func tamper(t TamperType, partition string, seq uint64, detail string) *VerifyError {
	return &VerifyError{Type: t, Partition: partition, Seq: seq, Detail: detail}
}
