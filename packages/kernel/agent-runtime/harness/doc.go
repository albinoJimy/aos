// Package harness é o HARNESS reutilizável de replay/idempotência do AOS (AOS-024).
//
// Dado um RUN GRAVADO (uma trajectória no Event Store), o harness VERIFICA
// automaticamente — de forma repetível e determinística — as propriedades
// não-negociáveis do Agent Runtime (specs/01 §4, gate 8; ADR-001, ADR-010):
//
//	(a) REPLAY DETERMINÍSTICO — corre o motor de replay de AOS-016
//	    ([replay.ReplayEngine]) e FALHA se algum passo divergir (hash de prompt,
//	    modelo/seed, versão do assembler ou sequência de step_id). Suporta
//	    resume-from-step e afere a fidelidade (fracção de turnos coincidentes).
//
//	(b) IDEMPOTÊNCIA POR PASSO — reexecuta cada passo com efeito externo sob um
//	    calendário at-least-once com crash intercalado (ledger reconstruído do log)
//	    e confirma ZERO efeitos OBSERVÁVEIS duplicados via o step-ledger de AOS-014
//	    ([durable.StepLedger]).
//
//	(c) FAULT-INJECTION PARAMETRIZÁVEL — pontos de crash configuráveis
//	    ([FaultPoint]); confirma que a retoma (resume-from-step) reconstrói
//	    EXACTAMENTE o mesmo estado que o replay completo.
//
// O desfecho é um RELATÓRIO de fidelidade ([FidelityReport] / [AggregateReport])
// com serialização JSON estável (structs, sem mapas — os mesmos inputs produzem
// sempre os mesmos bytes), consumível pelas métricas operacionais do backlog
// (replay-fidelity, contagem de efeitos duplicados; specs/01 §9).
//
// # Reutilização, NÃO reimplementação
//
// O harness ORQUESTRA e AFERE peças JÁ implementadas e Done — NÃO reimplementa
// nenhuma lógica de replay ou de ledger:
//   - motor de replay determinístico: AOS-016, subpacote
//     github.com/aos-ref/kernel/agent-runtime/replay;
//   - step-ledger de idempotência: AOS-014, subpacote
//     github.com/aos-ref/kernel/agent-runtime/durable.
//
// # Golden trajectories / fixtures
//
// As trajectórias de referência ([BuildEchoGolden], [BuildImmediateFinalGolden],
// [GoldenSet]) são DETERMINÍSTICAS e versionadas: construídas por builders Go que
// correm o loop real de AOS-013 sobre um Event Store em memória com relógios
// injectados (nunca relógio/random reais) e um modelo guionado. São reprodutíveis
// entre execuções e reutilizáveis por outros epics (em particular EPIC-11, que as
// consome sem duplicar — o foco de AOS-024 é replay/idempotência, distinto do
// eval harness de golden-sets de comportamento de AOS-114).
//
// # Alinhamento com EPIC-11
//
// AOS-024 entrega a FUNDAÇÃO transversal (harness + fixtures + gate 8). As suites
// AOS-111 (replay determinístico) e AOS-112 (idempotência por passo) de EPIC-11
// CONSOMEM este harness e as suas fixtures; não reimplementam a mecânica. O eval
// harness de comportamento (AOS-114, golden-sets curados + gen_ai.evaluation.result)
// é um aparelho SEPARADO e complementar — este pacote não lhe toca.
package harness
