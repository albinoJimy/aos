package controlsurface

import "errors"

// Os erros de VALIDAÇÃO DE MENSAGEM saíram em AOS-303 com o contrato de payload que os produzia:
// `ErrIncompatibleSchemaVersion`, `ErrUnknownKind`, `ErrEmptyEmitter`, `ErrEmptyCorrection` e
// `ErrEmptyCorrectionSignature`. Todos eram devolvidos por `ControlMessage.Validate` e por mais
// nada — zero consumidores dentro e fora do pacote, verificado antes de remover.
//
// `ErrInvalidSchemaVersion` FICA, e não por herança: é devolvido por
// [ParseControlSchemaVersion] (`version.go`), que sobrevive porque `governance/approval-card`
// consome o contrato de versão.

var (
	// ErrInvalidSchemaVersion — a string de versão não é um SemVer "X.Y.Z" válido. Fail-closed:
	// [ParseControlSchemaVersion] recusa em vez de adivinhar uma versão.
	ErrInvalidSchemaVersion = errors.New("controlsurface: schema_version inválido (esperado SemVer X.Y.Z)")

	// ErrEmptyRunID — falta o run_id. É a âncora que liga a reflexão de estado ao run correcto
	// (o stream_id no Event Store). Rejeitado fail-closed por [NewStateProjector].
	ErrEmptyRunID = errors.New("controlsurface: run_id vazio")

	// ErrNilSubscriber — o [StateProjector] foi construído sem uma fonte de subscrição
	// (nil). Sem Subscribe não há read-model de reflexão.
	ErrNilSubscriber = errors.New("controlsurface: subscritor de eventos em falta")
)
