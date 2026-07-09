// Package referencemonitor implementa o Reference Monitor (RM) do AOS — o
// Policy Enforcement Point (PEP) e a primeira fundação não-negociável do
// sistema (ADR-002): nenhum caminho de código chama uma tool directamente.
//
// # Superfície única
//
// [Monitor.Mediate] é o ÚNICO ponto por onde uma tool call é autorizada e
// despachada. Toda a tool call atravessa a cadeia de hooks configurada — na
// ordem canónica identidade → política → orçamento → egress → audit fornecida
// por [DefaultHooks] — ANTES de qualquer efeito externo. A cadeia é invocada
// pela ordem dada (o RM não a reordena) e uma cadeia vazia é negada fail-closed.
// Não existe via pública que execute uma tool fora do RM (ver [Monitor.Register]
// e o dispatcher interno em monitor.go).
//
// # Três propriedades clássicas (tecnica/01 §1.4)
//
//   - Sempre invocado: o Agent Runtime não tem via directa para o substrato; a
//     única saída é Mediate.
//   - Inviolável: a execução de uma tool exige um [Permit] não-forjável (campo
//     não-exportado, uso único, ligado ao fingerprint do call) que só este
//     pacote consegue mintar dentro de Mediate.
//   - Verificável: superfície pequena, política externalizada em hooks
//     plugáveis e cada decisão registada no Event Store (AOS-002).
//
// # Fail-closed (ADR-002, contrato C1 em tecnica/12 §4)
//
// A ausência de um permit explícito é negação. Qualquer hook que devolva
// deny/escalate, um erro ou um panic faz Mediate devolver [Deny] (nunca
// [Permit]), registar o evento de negação e NÃO despachar a tool. No caminho de
// permit, se o registo de auditoria no Event Store falhar, Mediate degrada para
// [Deny]: uma acção não-auditável não é permitida (ADR-002/010).
//
// # Escopo (AOS-003)
//
// Este pacote entrega apenas o RM: contratos de hooks, STUBS NEUTROS, no-bypass
// (estrutural + lint de arquitectura), registo no Event Store e benchmark de
// overhead. As implementações REAIS de identidade/política/orçamento/egress/
// audit chegam em AOS-004/005/008/011 — aqui só interfaces estáveis e stubs.
//
// # Overhead
//
// O caminho de permit com stubs neutros e política em memória tem alvo de
// overhead p95 < 15 ms (NFR-01, specs/01 §4: refere-se à avaliação de
// política/mediação em memória; o overhead total composto por tool call —
// admissão, broker, egress, append — é outro orçamento).
package referencemonitor
