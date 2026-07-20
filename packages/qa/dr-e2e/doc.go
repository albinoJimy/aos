// Package dre2e é o TESTE DE FOGO de DR/replay end-to-end de AOS-118 (EPIC-11).
//
// Junta idempotência (AOS-112), replay resume-from-step (AOS-111) e carga
// concorrente (AOS-116) num cenário de desastre REALISTA: perder um nó do Event
// Store a meio de escritas activas, promover a réplica sobrevivente, e provar que
// as trajectórias retomam resume-from-step SEM perder eventos confirmados, SEM
// duplicar efeitos externos e SEM cruzar a fronteira de soberania.
//
// # Composição, não reinvenção
//
// TODOS os primitivos já existem e são COMPOSTOS aqui — nada é reimplementado:
//
//   - substrate/eventstore — o cluster de réplicas in-process com replicação
//     síncrona por quórum. O failover é Store.Kill(Leader()) → electLeader()
//     (promoção da réplica viva mais actualizada, presa à fronteira regional), NÃO
//     restore-from-backup (essa via, dr.Recoverer, é complementar/opcional).
//   - kernel/agent-runtime/harness — Verify/FidelityReport para a fidelidade de
//     replay resume-from-step (ReplayFidelity==1.0, ResumeMismatches==0) e
//     VerifyEffectIdempotency/DriveEffectSchedule para exactamente-uma-vez.
//   - kernel/agent-runtime/worker — o worker durável stateless que retoma um run
//     do log após o failover (RunOutcome.Skipped>0 && Completed).
//   - kernel/agent-runtime/durable — LeaseManager (fencing tokens monotónicos),
//     FencedAppender (rejeita a escrita de nó obsoleto) e StepLedger (dedup por
//     f(run_id,step_id)).
//   - testkit/env — o ambiente efémero que provisiona o Store replicado na
//     fronteira de soberania e garante o teardown.
//
// # Determinismo
//
// A carga de escrita corre sob -race (concorrência real), mas TODAS as asserções
// são sobre o estado COMMITTED (reconciliação do log) — determinista. O MTTR é
// medido com um relógio INJECTADO (bracket Kill→primeiro sucesso pós-failover), e
// o relatório AOS_DR_REPORT é byte-estável entre execuções (-count=2 dá o mesmo
// veredicto). Sem wall-clock nem random no caminho de decisão.
//
// O código de teste vive em dr_replay_e2e_test.go; este ficheiro existe só para
// documentar o pacote (o módulo é uma folha de teste, importado por ninguém).
package dre2e
