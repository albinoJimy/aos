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

	// ErrNonCanonicalExtension — uma extensão de ADR-022 declarada num nó (papel,
	// aresta condicional, contrato de output/consumo) NÃO está na FORMA CANÓNICA que o
	// cartão apresenta: um símbolo fora do charset fechado, um operando que não é
	// símbolo nem inteiro canónico, uma conjunção vazia, ou uma referência a um nó que
	// não existe no plano.
	//
	// É a imposição ESTRUTURAL da regra de ouro do cartão («sem segredos»): a forma
	// canónica não tem por onde deixar entrar um valor de payload, um excerto de output
	// ou um locator — e um wiring que tente empurrá-los pela porta RECUSA o plano em vez
	// de degradar o cartão. Fail-closed em [Plan.Validate]/[BuildPlanCard]: nada é
	// saneado, truncado nem descartado silenciosamente.
	ErrNonCanonicalExtension = errors.New("planapproval: extensao ADR-022 fora da forma canonica (simbolo/operando/referencia) — recusado (fail-closed)")

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

	// ErrForcedReviewMissing — uma aprovação foi tentada sem rever um nó cuja revisão é
	// FORÇADA pela triagem por risco (Class >= gray ou capability_gap). Fail-closed: a
	// triagem item-a-item não pode ser saltada — o nó forçado tem de constar de
	// [PlanDecision.ReviewedNodes]. Imposto quando [WithForcedReview] está ligado.
	ErrForcedReviewMissing = errors.New("planapproval: no forcado (>=gray/capability_gap) nao revisto — aprovacao recusada (fail-closed)")

	// ErrEffectDualControlFailed — a imposição INLINE do dual-control POR-EFEITO (nós
	// danger) recusou: um card por-efeito danger não foi autorizado pelo
	// [approvalcard.DualControlCollector] (falta de quórum de dois aprovadores DISTINTOS,
	// recusa ou erro do canal). Fail-closed: com [WithPerEffectDualControl] ligado, um nó
	// danger cujo efeito não obteve dual-control NUNCA deixa o plano ser aprovado — a
	// granularidade por-efeito é imposta ANTES da decisão agregada assinada.
	ErrEffectDualControlFailed = errors.New("planapproval: dual-control por-efeito (no danger) nao autorizado — aprovacao recusada (fail-closed)")

	// ErrRevalidationFailed — uma EDIÇÃO do plano NÃO revalidou: a porta [Revalidator]
	// (adaptador do orchestrator/planvalidate ligado no wiring) OU a revalidação
	// estrutural local (validate/topo do grafo revisto) rejeitou o plano editado. Fail-
	// closed: um plano editado só é aprovável APÓS revalidar — uma edição que introduz
	// invalidez nunca é aprovada (sem round-trip ao LLM).
	ErrRevalidationFailed = errors.New("planapproval: plano editado nao revalidou — aprovacao recusada (fail-closed)")
)
