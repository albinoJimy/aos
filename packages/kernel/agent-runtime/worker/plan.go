package worker

import referencemonitor "github.com/aos-ref/kernel/reference-monitor"

// Step é UM passo lógico com efeito externo de um run, na perspectiva do
// supervisor. É DADOS (o worker executa-o) — não carrega estado durável nem
// step_id: o [Worker] deriva o step_id DETERMINISTICAMENTE da posição do passo no
// plano ([durable.StepSequencer]), para que a idempotency key f(run_id, step_id)
// seja BYTE-IDÊNTICA entre execução, retry e replay (ADR-001). Assim, dois
// processos worker distintos que executem o mesmo plano produzem as mesmas chaves
// e o ledger deduplica os efeitos.
type Step struct {
	// Call é o TEMPLATE da tool call que o Reference Monitor medeia (ADR-002). O
	// chamador fornece ToolID/Capability/Principal/Resource/Context/Credential/Input;
	// o [Worker] SOBREPÕE RunID e StepID (identidade determinística do passo) antes
	// de mediar — nunca contorna o PEP. O Principal transporta a cadeia de delegação
	// (responsabilização, ADR-003); nenhum segredo é anotado em spans/logs.
	Call referencemonitor.Call
	// CostMicroUSD é o custo do passo em micro-USD inteiro (fonte de verdade da
	// agregação de custo, AOS-078), anotado no span execute_tool emitido pelo worker
	// (AttrCostMicroUSD). 0 é válido (passo sem custo atribuído).
	CostMicroUSD int64
}

// RunPlan descreve o TRABALHO de um único run (a sua partição). É a unidade de
// posse-de-partição-por-run: cada run é a sua própria partição (sharding natural,
// AC3). O plano é imutável e SEM estado durável — o estado de execução vive no
// Event Store (checkpoints/ledger) e, transitoriamente, na pilha de [Worker.Run].
// Um processo worker NOVO reconstrói o ponto de retoma inteiramente do log a partir
// do mesmo RunPlan (statelessness, AC2).
type RunPlan struct {
	// RunID identifica o run/partição (stream_id no Event Store e chave do lease).
	RunID string
	// Steps são os passos lógicos por ORDEM. A posição (1-based) é o número do turno
	// que deriva o step_id estável; o resume-from-step salta os turnos já confirmados.
	Steps []Step
}

// RunOutcome resume o desfecho de servir um run (observabilidade; sem estado
// durável). Executed + Skipped == número de passos processados; Skipped são os
// turnos já confirmados que o resume-from-step saltou (não re-executados).
type RunOutcome struct {
	// RunID é o run servido.
	RunID string
	// Completed indica que todos os passos do plano foram processados (executados ou
	// saltados) sem perda de posse nem negação.
	Completed bool
	// Executed é o número de passos que este worker executou de facto neste processo.
	Executed int
	// Skipped é o número de passos saltados por já estarem confirmados no log
	// (resume-from-step) — a prova de que o estado vive no Event Store, não no processo.
	Skipped int
	// ResumeTurn é o turno (1-based) em que o worker retomou (o NextTurn do Resumer).
	ResumeTurn int
}
