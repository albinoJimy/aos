// Package dr implementa o plano de DISASTER RECOVERY do AOS por REPLAY
// DETERMINÍSTICO com RPO/RTO definidos e validados por game day (AOS-102, EPIC-10).
//
// # A tese de DR do AOS (ADR-001/007/010)
//
// O DR do AOS distingue-se de um sistema convencional: como o Event Store é a fonte
// de verdade append-only (ADR-007) e a execução é durável ao nível do passo (ADR-001),
// a recuperação primária é o REPLAY DETERMINÍSTICO a partir do log, não a restauração
// de estado mutável. Restaura-se o LOG (via AOS-101); o estado reconstrói-se
// resume-from-step. A fidelidade exige que todos os inputs não-determinísticos tenham
// sido capturados por trajectória (model-id/params/seed/prompt_hash, ADR-010/AOS-016).
//
// # Composição, não reimplementação
//
// Este módulo COMPÕE as peças já Done — NÃO reimplementa restauro, replay, resume,
// verificação ou idempotência:
//
//   - [github.com/aos-ref/platform/backup] (AOS-101): Restorer.RestoreTo verifica o
//     manifesto hash-chain e reinsere o log preservando o envelope até ao seq-alvo de
//     PITR; Exporter mede o RPO;
//   - [github.com/aos-ref/platform/audit] (AOS-072/083): VerifyFromCheckpointAtHead
//     verifica a hash-chain do WORM ancorada num checkpoint assinado, rejeitando
//     rollback/truncatura;
//   - [github.com/aos-ref/kernel/agent-runtime/replay] (AOS-016): Replay prova a
//     fidelidade determinística (100% dos passos) reutilizando os inputs
//     não-determinísticos capturados, com ZERO efeitos externos;
//   - o worker durável (AOS-099) + [github.com/aos-ref/kernel/agent-runtime/durable]
//     (AOS-014/015): retomam resume-from-step com o StepLedger a garantir 0 efeitos
//     duplicados (chave de idempotência f(run_id, step_id));
//   - [github.com/aos-ref/substrate/eventstore] (AOS-100): o Event Store de DR LIMPO,
//     construído WithSovereigntyBoard, RECUSA cross-border por construção.
//
// # O orquestrador (transacção fail-closed)
//
// [Recoverer.Recover] encadeia — abortando à primeira falha, SEM tocar em produção:
// resolver a fronteira-alvo (board→região INJECTADA) → construir o Store de DR na
// fronteira → restaurar o log verificado → verificar o WORM → provar a fidelidade do
// replay (Fidelity==1.0 && sem divergência) → retomar resume-from-step (0 duplicados)
// → reafirmar a fronteira de soberania. Qualquer ErrSegmentTampered/ErrChainBroken/
// ErrCheckpointStale/divergência/ErrSovereigntyViolation/ErrIncompleteCapture ABORTA;
// o serviço NÃO é dado por restabelecido.
//
// # O game day (AC2/AC7)
//
// [GameDay.Run] corre o encadeamento contra um Store DESCARTÁVEL, mede RPO (via
// [RPOSource], reutilizando o exportador de AOS-101) e RTO (wall-clock do orquestrador,
// relógio injectável) contra os alvos propostos (RPO <= 1 min, RTO <= 30 min) e PERSISTE
// a evidência combinada, com o próximo exercício agendado (cadência periódica).
//
// # Layering (ADR-011)
//
// platform/dr NÃO importa control-plane/governance/* (seria um up-import ilegal). A
// resolução board→região é INJECTADA ([BoundaryResolver]) e a soberania é REFORÇADA
// pelo próprio guard do eventstore (WithSovereigntyBoard) mais a asserção do
// orquestrador de que região(Store de DR)==região-alvo. O failover de DR NÃO cruza a
// fronteira de soberania.
//
// O runbook operacional está no README do módulo (ligado a AOS-106).
package dr
