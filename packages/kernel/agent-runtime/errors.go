package agentruntime

import "errors"

var (
	// ErrNoModelClient — o Runtime foi construído sem uma [ModelClient].
	ErrNoModelClient = errors.New("agentruntime: model client em falta")
	// ErrNoMonitor — o Runtime foi construído sem um Reference Monitor.
	ErrNoMonitor = errors.New("agentruntime: reference monitor em falta")
	// ErrNoRecorder — o Runtime foi construído sem um [TurnRecorder].
	ErrNoRecorder = errors.New("agentruntime: turn recorder em falta")
	// ErrEmptyRunID — o objectivo não tem RunID (obrigatório: é o stream_id).
	ErrEmptyRunID = errors.New("agentruntime: goal.RunID vazio")
	// ErrNoPrincipal — o objectivo não identifica o principal (NHI) do agente.
	ErrNoPrincipal = errors.New("agentruntime: goal.Principal.NHIID vazio")
	// ErrMaxTurnsExceeded — o loop atingiu MaxTurns sem resposta final. É uma
	// paragem defensiva do esqueleto; a máquina de estados durável (AOS-017)
	// substitui esta terminação simples.
	ErrMaxTurnsExceeded = errors.New("agentruntime: MaxTurns excedido sem resposta final")
	// ErrModelCall — a chamada ao Model Gateway falhou (envolve o erro subjacente).
	ErrModelCall = errors.New("agentruntime: falha na chamada ao modelo")
	// ErrTurnRecord — a gravação do turno no Event Store falhou (envolve o erro).
	ErrTurnRecord = errors.New("agentruntime: falha ao gravar o turno")
	// ErrCapture — a captura de não-determinismo do turno falhou (AOS-016). Envolve
	// o erro subjacente do [Capturer] (p.ex. perda de quórum no Event Store).
	ErrCapture = errors.New("agentruntime: falha ao capturar o não-determinismo do turno")
)
