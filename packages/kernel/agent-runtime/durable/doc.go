// Package durable implementa o CONTRATO DE EXECUÇÃO DURÁVEL do Agent Runtime do
// AOS (AOS-014) — a cláusula de idempotência por passo do ADR-001 (tecnica/02 §4).
// É o alicerce sobre o qual assentam o checkpoint intra-iteração (AOS-015), o
// replay resume-from-step (AOS-016), as sagas de compensação (AOS-020) e as
// activities de efeito externo (AOS-021/022).
//
// # As três peças
//
//   - [IdempotencyKey] — função PURA e DETERMINÍSTICA key = run_id + ":" + step_id.
//     Injectiva (rejeita ':' nos inputs para fechar a colisão de deslocamento do
//     delimitador). A forma casa byte-a-byte com a chave por que o Event Store
//     (AOS-002) deduplica o turn.recorded e com o header downstream. "Espaço único"
//     é conceptual (convenção de derivação), NÃO literal: por passo há até três
//     chaves de ES — run_id:step_id (turno+downstream), run_id:step_id-tool-n
//     (sub-passo) e run_id:ledger-step_id (registo do ledger, domínio de dedup
//     separado). Ver [IdempotencyKey].
//   - [StepSequencer] — atribui step_ids monotónicos e ESTÁVEIS por passo lógico,
//     puros na posição (turno) e por isso invariantes entre execução, retry e
//     replay. Implementa o hook [github.com/aos-ref/kernel/agent-runtime.StepIdentity]
//     de AOS-013 (ligação ADITIVA via WithStepIdentity — sem alterar o loop).
//   - [StepLedger] — o LEDGER DE RESULTADO sobre o Event Store. Apply verifica
//     already-applied ANTES de qualquer efeito, memoriza {key→status,resultado,
//     hash} de forma durável, e é reconstruível do log (Rebuild) após reinício.
//
// # Modelo de garantia (ADR-001) — honesto
//
// Exactly-once VERDADEIRO do efeito externo é impossível sem cooperação downstream.
// O contrato publicado é AT-LEAST-ONCE + idempotência downstream honrando a key =
// ZERO efeitos OBSERVÁVEIS duplicados. O ledger NÃO afirma exactly-once do efeito;
// afirma que um downstream que honre a idempotency key regista o efeito UMA vez
// observável, mesmo com crash entre o efeito e o commit do ledger (o effect pode
// correr mais de uma vez; o downstream deduplica pela chave determinística).
//
// # Contrato para consumidores (AOS-020 / AOS-021 / AOS-022)
//
// Uma activity de efeito externo (AOS-021) executa-se SEMPRE dentro de
// [StepLedger.Apply], derivando a sua chave de [IdempotencyKey] (ou [StepSequencer.SubKey])
// e propagando essa MESMA chave ao downstream (header/campo de idempotência do
// alvo). Uma saga (AOS-020) regista, a par do resultado, a sua compensação, e
// reproduz o log em sentido inverso — cada compensação é, ela própria, um Apply
// idempotente. AOS-022 consome o ledger para observabilidade (contadores
// apply/dedup via [Observer]) e reconciliação. O ledger é reconstruível
// (Rebuild) — o replay (AOS-016) lê os inputs não-determinísticos do log em vez de
// os regenerar.
//
// # Zero dependências externas
//
// Só a stdlib e os pacotes internos por path (eventstore, agent-runtime). O -race
// é limpo; o estado é protegido por mutex.
package durable
