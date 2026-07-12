// Package state implementa a MÁQUINA DE ESTADOS DURÁVEL do run do Agent Runtime do
// AOS (AOS-017) — a espinha dorsal de estados sobre a qual assentam a liveness por
// lease/fencing (AOS-018), o Escalonador (EPIC-03) e o canal de steer (AOS-023).
// Substitui a máquina grosseira `ready → running → complete + blocked`, que
// confundia suspensão legítima com falha, pelos DEZ estados canónicos da fonte, com
// waiting_on_human e paused de PRIMEIRA CLASSE (tecnica/02 §5, ADR-001/013).
//
// # Os dez estados e as três famílias
//
//   - ACTIVOS:   [Ready], [Running].
//   - SUSPENSOS: [WaitingOnTool], [WaitingOnHuman], [Paused] — suspensão LEGÍTIMA e
//     retomável, distinta de falha e de zombie (a separação que impede um gate
//     humano de parecer um worker morto).
//   - TERMINAIS/RECUPERAÇÃO: [Complete], [Failed] (falha recuperável → saga),
//     [Compensating], [Killed], [TimedOut].
//
// # Tabela declarativa de transições (DADOS, não if/switch)
//
// A máquina é DADOS: [validTransitions] é o conjunto EXACTO dos 13 pares (from → to)
// permitidos, seguindo o stateDiagram da fonte. [IsValidTransition] é a única fonte
// de verdade da validação; qualquer par ausente é [ErrInvalidTransition]. Tornar a
// máquina declarativa dá testabilidade por matriz 10×10 e permite ao replay
// reconstruir estado sem re-derivar regras.
//
// # Durabilidade e reconstrução por replay (ADR-001)
//
// Cada transição válida é UM evento append-only [EventTypeTransition] no Event Store
// replicado (AOS-002). O estado corrente é sempre reconstruível relendo o log:
// [Machine.Rebuild] adopta o To do evento de transição de seq mais alto. É isto que
// faz a máquina SOBREVIVER A CRASH — um worker novo constrói uma [Machine] sobre o
// mesmo cluster, chama Rebuild e continua. O estado in-memory só avança APÓS o commit
// durável, pelo que uma transição inválida ou uma falha do Event Store NUNCA corrompe
// o estado (persistido ou in-memory).
//
// # Fencing token (contrato partilhado com AOS-018)
//
// A entrada em running pelo CLAIM ([Ready] → [Running]) EXIGE um [FencingToken]
// válido ([RequiresFencingToken]). AOS-017 define só o CONTRATO ([FencingToken],
// [Uint64Token]) e VERIFICA presença/validade; a atribuição monotónica durável, o
// heartbeat e a rejeição de escritas de token inferior são AOS-018. As retomas de
// suspensão para running reentram sob o lease já detido e NÃO re-exigem token.
//
// # Timeout fail-closed (ADR-013)
//
// [Machine.CheckDeadlines] aplica, com um [Clock] INJECTÁVEL (testes determinísticos,
// sem sleeps): waiting_on_human há >= TTL → [Killed] (fail-closed — NUNCA running em
// ambiguidade); running há >= wall-clock → [TimedOut].
//
// # Eventos pause/resume/kill EXPOSTOS
//
// [Machine.Pause]/[Machine.Resume]/[Machine.Kill] expõem as arestas que o steer
// (AOS-023) e o lease (AOS-018) accionam. AOS-017 só as EXPÕE (conveniências sobre
// [Machine.Transition]); a lógica desses tickets NÃO é implementada aqui.
//
// # Fora de âmbito (delegado)
//
// Lease/heartbeat e a origem monotónica do fencing token (AOS-018); o canal de steer
// (AOS-023); a execução da saga de compensação (AOS-020) — o estado [Compensating]
// existe, a reprodução inversa do log é do ticket da saga.
package state
