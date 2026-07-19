// Package hipercare é o HARNESS DE RELATÓRIO de prontidão/transição do hipercare
// (AOS-108, FECHO do EPIC-10). É OPERACIONALIZAÇÃO E AFINAÇÃO: não altera o
// comportamento de nenhum subsistema, não introduz features e não reimplementa SLOs,
// alertas, runbooks ou DR — COMPÕE os contratos já Done num relatório versionado e num
// GATE FAIL-CLOSED de critérios de saída.
//
// # O que compõe (leituras, nunca escritas de comportamento)
//
//   - AC2 — conformidade de SLOs: projecta o [otelgenai.OperationalSnapshot] (AOS-104)
//     sobre a janela de hipercare e verifica que os SLOs canónicos estão MET de forma
//     SUSTENTADA. Anti-vacuidade herdada de [otelgenai.SLIValue]: um SLI não avaliado
//     (Samples==0) NÃO conta como cumprido — não se encerra sobre ausência de dados.
//   - AC3 — MTTR por runbook: um registo de MTTR por RB-01..RB-05 cruzado com
//     [runbooks.CanonicalIDs] (AOS-106). Um runbook sem MTTR medido não está validado.
//   - AC4 — calibração de alertas: override-rate + gate escape rate + taxa de
//     falsos-positivos antes/depois (a calibração do alerting-as-code de AOS-105).
//   - AC5 — revalidação de DR: compõe a [dr.GameDayEvidence] (AOS-102); revalidado sse
//     Passed && RPOWithin && RTOWithin.
//   - AC6 — relatório de transição: métricas DORA (MTTR, change failure rate, deploy
//     freq.) e acções de acompanhamento.
//
// # O gate FAIL-CLOSED (o cerne verificável, AC1)
//
// [HipercareReport.CanExit] só permite ENCERRAR o hipercare (transitar para operação em
// regime) se TODOS os critérios de saída estiverem cumpridos: todos os SLOs canónicos
// avaliados+cumpridos e sustentados, todos os runbooks validados com MTTR, os alertas
// calibrados e o DR revalidado (RPO/RTO dentro). Se QUALQUER um falhar, o hipercare NÃO
// encerra e [HipercareReport.ExitCriteria] lista o que falta. Um SLO não avaliado
// (Samples==0) NÃO conta como cumprido (anti-vacuidade — não se encerra sobre lacuna).
//
// # Serialização
//
// Todos os tipos são JSON-serializáveis com round-trip reproduzível (stdlib, zero deps).
// Nenhum relatório carrega segredos nem PII — só rótulos, limiares e métricas agregadas.
package hipercare
