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

	// ErrIncompleteCapture — GATE DE ADMISSÃO de replay (AOS-079, CA4): a captura de
	// não-determinismo de um passo NÃO está completa (evento "replay.captured" em
	// falta, PayloadRef de mode 3 não resolúvel/retido, ou manifesto sem prompt_hash).
	// O replay RECUSA fail-closed em vez de produzir silenciosamente uma reprodução de
	// baixa fidelidade — "fidelidade é condição, não opção". Um run completamente
	// capturado é admissível e reproduz 100% dos passos; um com captura em falta é
	// INADMISSÍVEL.
	ErrIncompleteCapture = errors.New("replay: captura de não-determinismo incompleta (replay inadmissível)")

	// ErrPayloadStoreRequired — um evento de captura mode 3 carrega uma PayloadRef mas
	// o [ReplayEngine] não tem PayloadStore ligado ([WithPayloadResolver]) para a
	// resolver. Sem o store externo o payload completo é irrecuperável ⇒ fail-closed.
	ErrPayloadStoreRequired = errors.New("replay: payload store em falta para resolver referência de mode 3")

	// ErrPayloadAccessDenied — o [PayloadStore] NEGOU o acesso ao payload: o accessor
	// (principal/escopo) não tem autoridade de leitura. O content-capture mode 3 exige
	// que o payload viva atrás de um IAM PRÓPRIO, separado do escritor do Event Store;
	// um accessor não autorizado é negado fail-closed.
	ErrPayloadAccessDenied = errors.New("replay: acesso ao payload negado (IAM do payload store)")

	// ErrPayloadNotFound — a PayloadRef não corresponde a nenhum payload no store (não
	// escrito, evicted por TTL/crypto-shredding, ADR-011). Para o replay é captura em
	// falta ⇒ traduz-se em [ErrIncompleteCapture] na admissão.
	ErrPayloadNotFound = errors.New("replay: payload não encontrado no store")

	// ErrPayloadIntegrity — o payload resolvido não corresponde ao hash da sua
	// PayloadRef (content-addressable): o conteúdo foi adulterado. A referência PROVA a
	// integridade — uma divergência é rejeitada fail-closed.
	ErrPayloadIntegrity = errors.New("replay: integridade do payload violada (hash não corresponde à referência)")
)
