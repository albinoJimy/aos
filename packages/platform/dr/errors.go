package dr

import "errors"

// Erros sentinela do orquestrador de DR. Todos abortam a transacção fail-closed: em
// qualquer um deles o serviço NÃO é dado por restabelecido e NADA de produção é
// tocado (o DR opera sempre sobre um Event Store LIMPO e descartável na fronteira).
var (
	// ErrNilResolver — [NewRecoverer] sem [BoundaryResolver] (board→região). A
	// resolução é INJECTADA (platform/dr não importa control-plane); sem ela não há
	// fronteira-alvo a impor.
	ErrNilResolver = errors.New("dr: resolvedor de fronteira (board→região) em falta")
	// ErrNilFactory — [NewRecoverer] sem [StoreFactory]. Sem fábrica não há como
	// construir o Event Store de DR LIMPO na fronteira (WithSovereigntyBoard).
	ErrNilFactory = errors.New("dr: fábrica de Event Store de DR em falta")
	// ErrNilRestorer — [NewRecoverer] sem [Restorer] (AOS-101). Sem restaurador não
	// há caminho de PITR verificado do log.
	ErrNilRestorer = errors.New("dr: restaurador (backup/PITR) em falta")
	// ErrEmptyBoard — a [Recovery] não nomeou o board de soberania a recuperar.
	ErrEmptyBoard = errors.New("dr: board de soberania em falta")
	// ErrEmptyRunID — a [Recovery] não nomeou o run/stream a recuperar e retomar.
	ErrEmptyRunID = errors.New("dr: run_id a recuperar em falta")
	// ErrNilResume — a [Recovery] não forneceu a função de retoma resume-from-step.
	ErrNilResume = errors.New("dr: função de retoma (resume-from-step) em falta")
	// ErrNilAuditStore — a [Recovery] não forneceu o Store WORM a verificar (AC5).
	ErrNilAuditStore = errors.New("dr: audit WORM store a verificar em falta")
	// ErrUnknownBoard — o board não resolveu para nenhuma região (região vazia). Uma
	// fronteira desconhecida NUNCA autoriza: fail-closed (ADR-011).
	ErrUnknownBoard = errors.New("dr: board sem região de soberania resolvida")
	// ErrRegionMismatch — a região do Event Store de DR (ou do log restaurado) NÃO
	// corresponde à fronteira-alvo resolvida do board. O failover de DR NÃO cruza a
	// fronteira de soberania (AC6, ADR-011): aborta.
	ErrRegionMismatch = errors.New("dr: região do Event Store de DR não corresponde à fronteira-alvo (cross-border recusado)")
	// ErrReplayInfidelity — o replay determinístico NÃO atingiu fidelidade total
	// (Fidelity != 1.0 ou divergência localizada). O serviço não é restabelecido a
	// partir de uma trajectória que não reproduz 100% (AC3, ADR-010).
	ErrReplayInfidelity = errors.New("dr: replay não atingiu fidelidade total (Fidelity<1.0 ou divergência)")
	// ErrDuplicatedEffects — a retoma resume-from-step reportou efeitos externos
	// duplicados. A idempotência por passo (chave f(run_id,step_id)) garante 0 na
	// retoma (AC4, ADR-001); um valor != 0 é uma violação e aborta fail-closed.
	ErrDuplicatedEffects = errors.New("dr: retoma duplicou efeitos externos (violação de idempotência)")
	// ErrTargetsExceeded — o game day recuperou com INTEGRIDADE (WORM verificado,
	// fidelidade 100%, 0 duplicados) mas o RPO e/ou o RTO MEDIDOS excederam o alvo
	// (AC2 exige "medidos E CUMPRIDOS"): um exercício fora do SLO é um game day
	// FALHADO. [GameDay.Run] devolve este sentinela JUNTO com a [GameDayEvidence]
	// completa (Passed==false) — a evidência fica na mesma persistida e inspeccionável,
	// mas um chamador que só verifique err==nil NÃO confunde uma falha de SLO com
	// sucesso. Distingue-se dos erros de integridade acima (que abortam ANTES, em
	// [Recoverer.Recover], sem sequer chegar à medição de SLO).
	ErrTargetsExceeded = errors.New("dr: game day fora do alvo de RPO/RTO (recuperação íntegra mas SLO não cumprido)")
)
