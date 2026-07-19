package eventstore

// StoreError é o tipo de erro sentinela do Event Store. Carrega o código
// canónico do contrato C2 (E_*) e é comparável com errors.Is por identidade.
type StoreError struct {
	// Code é o código estável do contrato (ex.: "E_SEQ_CONFLICT").
	Code string
	// msg é a descrição legível.
	msg string
}

func (e *StoreError) Error() string { return e.Code + ": " + e.msg }

// Sentinelas do contrato C2 (tecnica/12 §5) e do modelo de replicação.
// Todos são comparáveis com errors.Is.
var (
	// ErrAppendOnlyViolation — tentativa de escrever numa posição já ocupada / no
	// passado. O log é imutável; correcções são novos eventos.
	ErrAppendOnlyViolation = &StoreError{Code: "E_APPEND_ONLY_VIOLATION", msg: "posicao ja ocupada; o log e append-only"}

	// ErrSeqConflict — expected_seq não corresponde ao último seq committed
	// (escrita concorrente; o chamador relê e reavalia — optimistic concurrency).
	ErrSeqConflict = &StoreError{Code: "E_SEQ_CONFLICT", msg: "expected_seq nao corresponde ao ultimo seq committed"}

	// ErrStreamNotFound — read de um stream sem eventos committed.
	ErrStreamNotFound = &StoreError{Code: "E_STREAM_NOT_FOUND", msg: "stream inexistente"}

	// ErrNoQuorum — réplicas vivas insuficientes para atingir quórum; a escrita é
	// rejeitada e não deixa rasto (fail-closed).
	ErrNoQuorum = &StoreError{Code: "E_NO_QUORUM", msg: "replicas vivas insuficientes para quorum"}

	// ErrClosed — o store (ou subscrição) já foi fechado.
	ErrClosed = &StoreError{Code: "E_CLOSED", msg: "event store fechado"}

	// ErrInvalidReplica — id de réplica inválido ou já no estado pedido.
	ErrInvalidReplica = &StoreError{Code: "E_INVALID_REPLICA", msg: "id de replica invalido ou estado inalterado"}

	// ErrConfig — configuração de cluster inválida.
	ErrConfig = &StoreError{Code: "E_CONFIG", msg: "configuracao de cluster invalida"}

	// ErrSovereigntyViolation — uma réplica foi colocada fora da fronteira regional
	// de soberania do board (ADR-011), ou a sua região é ausente/desconhecida. A
	// construção é recusada fail-closed: réplicas e backups NUNCA cruzam a fronteira.
	ErrSovereigntyViolation = &StoreError{Code: "E_SOVEREIGNTY_VIOLATION", msg: "replica fora da fronteira regional de soberania (ou regiao ausente); fail-closed"}

	// ErrRestoreOrder — um lote de restauro (IngestStream) não continua o log de
	// forma gapless (seq esperado = último committed + 1, contíguo). O restauro
	// preserva a ordem-por-stream; não abre buracos nem reescreve o passado.
	ErrRestoreOrder = &StoreError{Code: "E_RESTORE_ORDER", msg: "lote de restauro nao e gapless a partir do ultimo seq committed"}

	// ErrRestoreEnvelope — um evento de restauro tem envelope incoerente (StreamID
	// diferente do stream alvo ou EventID vazio). O restauro reinsere o envelope
	// intacto; recusa fail-closed qualquer envelope malformado.
	ErrRestoreEnvelope = &StoreError{Code: "E_RESTORE_ENVELOPE", msg: "envelope de evento de restauro incoerente (stream_id/event_id)"}
)
