package planapproval

import "errors"

// Sentinelas fail-closed do módulo. Toda a construção/validação/aprovação recusa por
// omissão: uma versão de schema malformada, um plano incoerente, um canal ausente, um
// grafo com ciclo ou um spawn de run não-aprovado NUNCA produzem um efeito
// silenciosamente permissivo.
var (
	// ErrInvalidSchemaVersion — a versão de schema não é "X.Y.Z" com inteiros
	// não-negativos. Fail-closed em [ParsePlanCardSchemaVersion].
	ErrInvalidSchemaVersion = errors.New("planapproval: versao de schema invalida (esperado X.Y.Z)")

	// ErrIncompatibleSchema — o card carimba um MAJOR incompatível com [CurrentVersion].
	// Quebra de contrato, rejeitada (fail-closed) em [PlanCard.Validate].
	ErrIncompatibleSchema = errors.New("planapproval: versao de schema MAJOR incompativel (rejeitado)")

	// ErrInvalidPlan — o plano/grafo está incoerente: RunID/Agent vazios, nó com
	// task_id vazio, task_ids duplicados, ou uma aresta que referencia um nó ausente.
	// Fail-closed em [Plan.Validate]/[BuildPlanCard].
	ErrInvalidPlan = errors.New("planapproval: plano invalido (run_id/agent/nos/arestas)")

	// ErrPlanCycle — o grafo de tarefas contém um ciclo (a ordenação topológica não
	// cobre todos os nós). O plano NÃO é aprovável (fail-closed).
	ErrPlanCycle = errors.New("planapproval: grafo de tarefas contem um ciclo (fail-closed)")

	// ErrInvalidPlanCard — o [PlanCard] está incoerente (contagem de nós divergente da
	// dos cards/ordem, ou um card por-nó inválido). Fail-closed.
	ErrInvalidPlanCard = errors.New("planapproval: plan-card invalido (fail-closed)")

	// ErrNilOracle — o [PlanGate] foi construído sem um [autonomy.Oracle]. Sem porta
	// para consultar o nível, não há como aplicar a auto-aprovação — fail-closed.
	ErrNilOracle = errors.New("planapproval: autonomy.Oracle ausente (fail-closed)")

	// ErrNilChannel — o [PlanGate] foi construído sem um [risk.ConfirmationChannel].
	// Sem porta para DEVOLVER a decisão binária assinada, um plano gatado não pode ser
	// aprovado — fail-closed na construção.
	ErrNilChannel = errors.New("planapproval: risk.ConfirmationChannel ausente (fail-closed)")

	// ErrNilSpawner — o [SpawnGuard] foi construído sem um [Spawner] a envolver.
	// Fail-closed na construção.
	ErrNilSpawner = errors.New("planapproval: spawner ausente (fail-closed)")

	// ErrPlanNotApproved — o [SpawnGuard] recusou um Spawn de um run cujo plano NÃO foi
	// aprovado pelo [PlanGate]. É a prova ESTRUTURAL do AC1: nenhum sub-agente é lançado
	// antes da aprovação do plano (o custo de tokens fica ADIADO). Fail-closed.
	ErrPlanNotApproved = errors.New("planapproval: spawn recusado — plano do run nao aprovado (fail-closed)")
)
