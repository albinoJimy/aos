package replay

import "errors"

var (
	// ErrNilStore — o [EventStoreCapturer] ou o [ReplayEngine] foi construído sem
	// um Event Store (nil).
	ErrNilStore = errors.New("replay: event store em falta")

	// ErrEmptyRunID — o run_id fornecido ao replay é vazio (é o stream_id).
	ErrEmptyRunID = errors.New("replay: run_id vazio")

	// ErrNoTrajectory — o stream do run não contém eventos "turn.recorded": não há
	// trajectória para reconstruir.
	ErrNoTrajectory = errors.New("replay: trajectória vazia (sem turn.recorded)")

	// ErrMissingCapture — um turno tem "turn.recorded" mas não tem o evento
	// "replay.captured" correspondente. Sem a captura o replay não consegue devolver
	// a resposta do modelo nem os resultados das tools do turno (captura incompleta
	// na execução original — o Capturer não estava ligado ou falhou).
	ErrMissingCapture = errors.New("replay: captura de não-determinismo em falta para o turno")

	// ErrStepNotFound — o FromStepID pedido (resume-from-step) não corresponde a
	// nenhum step_id de turno gravado na trajectória.
	ErrStepNotFound = errors.New("replay: step_id de retoma não encontrado na trajectória")

	// ErrCorruptCapture — o payload de um evento "replay.captured" não descodifica
	// (log corrompido ou schema incompatível).
	ErrCorruptCapture = errors.New("replay: payload de captura corrompido")
)
