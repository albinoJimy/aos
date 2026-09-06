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

	// ErrNoQuorum — o substrato NÃO ESTÁ DISPONÍVEL NESTE MOMENTO; a escrita é rejeitada
	// e não deixa rasto (fail-closed).
	//
	// # É O SENTINELA CANÓNICO DA PORTA, NÃO UM DETALHE DO STORE DE REFERÊNCIA (AOS-354)
	//
	// O nome vem do primeiro produtor — o store de referência, onde a condição é
	// literalmente «réplicas vivas insuficientes para quórum». O PAPEL que ele desempenha
	// para quem consome é outro, e é mais largo: «indisponibilidade MOMENTÂNEA do
	// substrato, que volta sozinha, e sobre a qual se retenta na fronteira seguinte». É
	// nesse papel que `cmd/aos/progress_wiring.go` o põe na lista fechada de
	// `burndownTransitorio`, e que `cmd/aos/trajectory.go` o mapeia a HTTP 503.
	//
	// Por isso TODAS as implementações da porta o produzem. O backend replicado traduz
	// [natsjs.ErrDesligado] para aqui (`jetstream.indisponibilidadeTransitoria`),
	// EMBRULHANDO a causa: `errors.Is` responde `true` aos dois, e a mensagem que o
	// operador lê nomeia a desligação. Antes de AOS-354 não o fazia, e o resultado era que
	// a tolerância a N fronteiras consecutivas que `posture_banner.go` promete NUNCA era
	// armada sobre o substrato que AOS-100 tornou preferencial — um `ErrDesligado` caía em
	// «cegueira» e matava o run à primeira.
	//
	// Uma implementação NOVA da porta tem a mesma obrigação: o que for a sua
	// indisponibilidade transitória sai por aqui, ou os consumidores tratam-na como
	// cegueira permanente.
	ErrNoQuorum = &StoreError{Code: "E_NO_QUORUM", msg: "substrato indisponivel neste momento (sem quorum, ou sem ligacao ao substrato replicado)"}

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

	// ErrRestoreDivergent — um lote de restauro sobrepõe-se a eventos JÁ presentes no
	// log e o que lá está NÃO é o que o backup diz (EventID diferente no mesmo seq).
	// É a falha que nunca se pode aceitar em silêncio: significa que o alvo tem OUTRA
	// história, e continuar produziria um log costurado de dois passados diferentes
	// que verificaria como íntegro. Recusa fail-closed, e nomeia o seq onde divergiu.
	ErrRestoreDivergent = &StoreError{Code: "E_RESTORE_DIVERGENT", msg: "o log alvo ja tem outro evento neste seq — o restauro divergiria da historia armazenada"}

	// ErrWALCorruptedMidLog — o WAL tem um registo corrompido com registos ÍNTEGROS
	// depois dele. NÃO é a cauda rasgada de um crash: é corrupção no MEIO do log, e
	// a diferença importa porque o remédio da cauda (truncar) apagaria em disco
	// registos que estão intactos. Fail-closed: [Open] recusa em vez de truncar.
	//
	// Ver [WithWALTruncateOnCorruption] para a via de recuperação DELIBERADA.
	ErrWALCorruptedMidLog = &StoreError{Code: "E_WAL_CORRUPTED_MID_LOG", msg: "registo corrompido no MEIO do WAL, com registos integros a seguir"}

	// ErrReadOnly — o store foi aberto por [OpenReadOnly] e recusa qualquer escrita.
	// Não é uma avaria: é a postura de um abridor de INSPECÇÃO, que existe para poder
	// olhar para um WAL sem lhe poder tocar. Ver AOS-347.
	ErrReadOnly = &StoreError{Code: "E_READ_ONLY", msg: "event store aberto so para leitura — nao aceita escritas"}

	// ErrWALDesincronizado — o tamanho que o WAL julga ter em memória está À FRENTE do
	// ficheiro real: o ficheiro ENCOLHEU por baixo do escritor (outro processo truncou-o,
	// ou o substrato repô-lo). É condição TERMINAL e não de reposição: truncar para um
	// tamanho MAIOR do que o ficheiro ESTENDE-O com zeros em vez de o repor, e um append
	// que tinha FALHADO passaria a durável no meio de um buraco de bytes nulos. Ver
	// AOS-349 e [wal.desfazer].
	ErrWALDesincronizado = &StoreError{Code: "E_WAL_DESSINCRONIZADO", msg: "o tamanho do WAL em memoria esta a frente do ficheiro real — o ficheiro encolheu por baixo do escritor"}
)
