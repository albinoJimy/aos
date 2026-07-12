package activity

import "errors"

var (
	// ErrNilMediator — o [Dispatcher] em [ModeNormal] foi construído sem um Reference
	// Monitor (nil). Em modo normal o efeito TEM de ser mediado (ADR-002); sem RM não
	// haveria caminho de despacho legítimo — recusa-se na construção (fail-closed).
	ErrNilMediator = errors.New("activity: reference monitor (Mediator) em falta em modo normal")

	// ErrNilLedger — o [Dispatcher] em [ModeNormal] foi construído sem um step-ledger
	// (nil). A idempotência da activity (already-applied antes do efeito) ASSENTA no
	// ledger de AOS-014; sem ele não há memoização durável do resultado.
	ErrNilLedger = errors.New("activity: step-ledger (Ledger) em falta em modo normal")

	// ErrNilReplaySource — o [Dispatcher] em [ModeReplay] foi construído sem uma fonte
	// de resultados registados (nil). Em replay a activity NÃO executa nada: só devolve
	// o resultado do log — sem fonte não há nada a devolver.
	ErrNilReplaySource = errors.New("activity: fonte de replay (ReplaySource) em falta em modo replay")

	// ErrUnknownMode — o [Dispatcher] foi construído com um [Mode] desconhecido.
	ErrUnknownMode = errors.New("activity: modo de dispatch desconhecido")

	// ErrEmptyRunID — a [Activity] não trouxe run_id. É metade da idempotency key
	// (run_id:step_id) e o stream do Event Store — sem ele não há identidade durável.
	ErrEmptyRunID = errors.New("activity: run_id vazio")

	// ErrEmptyStepID — a [Activity] não trouxe step_id. É metade da idempotency key e a
	// âncora de estabilidade entre execução, retry e replay (AOS-014).
	ErrEmptyStepID = errors.New("activity: step_id vazio")

	// ErrEmptyToolID — a [Activity] não identifica a tool a despachar. Uma tool não
	// registada é negada por omissão (default-deny) — exigimo-la à cabeça.
	ErrEmptyToolID = errors.New("activity: tool_id vazio")

	// ErrMediationDenied — o Reference Monitor NÃO permitiu o efeito (deny/escalate). O
	// efeito NÃO ocorreu (o RM só despacha a tool sob permit) e NADA foi memorizado no
	// ledger — o passo não ficou aplicado. Distingue-se de [ErrToolExecution]: aqui a
	// política barrou; ali a tool permitida falhou a jusante.
	ErrMediationDenied = errors.New("activity: efeito negado pelo Reference Monitor (sem permit)")

	// ErrToolExecution — a tool foi PERMITIDA e despachada, mas a execução a jusante
	// falhou (decision.ToolErr). Nada é memorizado (o passo não fica aplicado) e o
	// retry re-corre o efeito — a convergência sem duplicação assenta na idempotência
	// downstream sobre a mesma key (AOS-014). O erro da tool é embrulhado (ver
	// [ToolError]).
	ErrToolExecution = errors.New("activity: tool permitida falhou na execução downstream")

	// ErrCompensationRegister — falhou o registo da compensação da activity no
	// [saga.CompensationRegistry] (AOS-020). Como o registo integra o efeito
	// aplicado, a sua falha aborta a aplicação (nada é memorizado) para não deixar um
	// efeito irreversível sem a sua acção inversa registada.
	ErrCompensationRegister = errors.New("activity: falha ao registar compensação (AOS-020)")

	// ErrReplayMiss — em [ModeReplay] não existe resultado REGISTADO para a idempotency
	// key da activity. O replay NUNCA cai para execução ao vivo como fallback (seria um
	// efeito não-registado): devolve este erro. Indica um log incompleto ou uma
	// divergência de step_id face à captura.
	ErrReplayMiss = errors.New("activity: sem resultado registado para replay (replay-miss)")

	// ErrNoRegistry — a [Activity] trouxe uma [Compensation] mas o [Dispatcher] não tem
	// [CompensationRegistrar] ([WithCompensationRegistry]). Recusa-se em vez de perder
	// silenciosamente a acção inversa.
	ErrNoRegistry = errors.New("activity: compensação declarada sem CompensationRegistrar configurado")

	// ErrNilCompensationAction — a [Activity] trouxe uma [Compensation] com Action nil.
	// Uma compensação sem acção inversa nunca poderia reverter o efeito; recusa-se à
	// cabeça (fail-closed) ANTES de qualquer efeito. É também o que garante que o
	// registo da compensação — que passa a correr TAMBÉM no caminho dedup, fora do
	// efeito do ledger (AOS021-Q1) — nunca falha a meio: com StepID já validado
	// não-vazio e Action não-nil, [saga.CompensationRegistry.Register] não pode errar.
	ErrNilCompensationAction = errors.New("activity: compensação declarada com Action nil")
)

// ToolError embrulha o erro de execução de uma tool PERMITIDA (decision.ToolErr),
// preservando-o para inspecção via errors.As enquanto o desfecho de política
// ([ErrToolExecution]) fica disponível via errors.Is. Não é uma negação.
type ToolError struct{ Err error }

func (e *ToolError) Error() string { return "activity: erro de execução da tool: " + e.Err.Error() }
func (e *ToolError) Unwrap() error { return e.Err }
