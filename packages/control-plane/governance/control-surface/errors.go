package controlsurface

import "errors"

var (
	// ErrInvalidSchemaVersion — o campo schema_version de uma [ControlMessage] não é
	// uma versão SemVer "X.Y.Z" válida. Fail-closed: uma mensagem sem versão parseável
	// é rejeitada antes de tocar em qualquer canal (um adaptador de plataforma que não
	// carimba a versão não pode dirigir o agente).
	ErrInvalidSchemaVersion = errors.New("controlsurface: schema_version inválido (esperado SemVer X.Y.Z)")

	// ErrIncompatibleSchemaVersion — a versão da mensagem é INCOMPATÍVEL com a versão
	// corrente da superfície (MAJOR diferente = quebra de contrato). Fail-closed: um
	// adaptador que fale uma versão MAJOR distinta é recusado, nunca interpretado à
	// força (evita acoplamento silencioso à implementação — AC5).
	ErrIncompatibleSchemaVersion = errors.New("controlsurface: schema_version incompatível (MAJOR diferente da superfície corrente)")

	// ErrEmptyRunID — a mensagem não traz run_id. É a âncora que liga o sinal
	// out-of-band ao run correcto (o stream_id no Event Store). Rejeitada fail-closed.
	ErrEmptyRunID = errors.New("controlsurface: run_id vazio")

	// ErrUnknownKind — o kind da mensagem não é um dos quatro do contrato (steer,
	// interrupt, resume, state). Fail-closed: um kind desconhecido nunca é traduzido.
	ErrUnknownKind = errors.New("controlsurface: kind de controlo desconhecido")

	// ErrEmptyEmitter — uma mensagem que muta o run (steer/interrupt/resume) não traz
	// emitter_id. Sem identidade de emissor não há NÃO-REPÚDIO (ADR-013): o log tem de
	// provar QUEM sinalizou. Rejeitada antes de tocar no canal.
	ErrEmptyEmitter = errors.New("controlsurface: emitter_id vazio (não-repúdio exige identidade do emissor)")

	// ErrEmptyCorrection — uma mensagem steer não traz correcção. Uma correcção sem
	// conteúdo não é uma instrução de controlo — rejeitada (espelha
	// [control.ErrEmptyCorrection]).
	ErrEmptyCorrection = errors.New("controlsurface: correcção de steer vazia")

	// ErrEmptyCorrectionSignature — um resume com correcção inline não traz a
	// assinatura da injecção ([ControlMessage.CorrectionSignature]). O resume-com-
	// correcção são DUAS operações autenticadas (steer + resume); sem a assinatura do
	// steer a correcção não pode ser injectada como control-plane. Fail-closed.
	ErrEmptyCorrectionSignature = errors.New("controlsurface: resume com correcção inline sem correction_signature (a injecção steer exige assinatura própria)")

	// ErrNilSubscriber — o [StateProjector] foi construído sem uma fonte de subscrição
	// (nil). Sem Subscribe não há read-model de reflexão.
	ErrNilSubscriber = errors.New("controlsurface: subscritor de eventos em falta")
)
