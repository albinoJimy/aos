# Baselines dos gates (dívida pré-existente triada)

Estes ficheiros listam descobertas **já existentes** dos scanners no momento em
que o CI (AOS-010) foi estabelecido. Os gates de `lint`/`sast`/`sca` são
**fail-closed para código NOVO**: qualquer descoberta que **não** conste da
baseline correspondente faz o gate ficar vermelho e bloqueia o merge. A baseline
apenas impede que dívida pré-existente de **outros tickets** (fora do âmbito
estrito de AOS-010, que é *chore* de CI e não altera código dos módulos) bloqueie
a introdução do próprio pipeline.

Formato: uma descoberta por linha, com número de linha removido (para não partir
com *drift*), semântica de **multiconjunto** (uma nova ocorrência do mesmo código
no mesmo ficheiro também é caçada). Regenerável com a mesma normalização dos
gates — ver `scripts/ci/*.sh`.

## `staticcheck.txt`
| Descoberta | Ficheiro | Dono (ticket) | Remediação |
|---|---|---|---|
| S1016 struct literal → conversão | `packages/kernel/reference-monitor/eventsink.go` | AOS-003 | usar conversão de tipo `delegationHopDTO(h)` |
| SA4000 expressões idênticas em `!=` | `packages/platform/identity/delegation/chain_test.go` | AOS-005/006 | corrigir asserção do teste |
| U1000 `withClock` não usado | `packages/substrate/bus/bus.go` | AOS-002 | remover código morto ou usar |
| U1000 `withClock` não usado | `packages/substrate/eventstore/store.go` | AOS-002 | remover código morto ou usar |

## `gosec.txt`
| Descoberta | Ficheiro | Dono (ticket) | Remediação |
|---|---|---|---|
| G115 (CWE-190) overflow de conversão inteira (×4) | `packages/substrate/bus/metrics.go` | AOS-002 | validar limites / `#nosec` justificado |
| G115 (CWE-190) overflow de conversão inteira (×2) | `packages/substrate/eventstore/ulid.go` | AOS-002 | validar limites / `#nosec` justificado |

Severidade dos G115 é HIGH mas são conversões de contadores/timestamps em código
de métricas/ULID (não fronteira de confiança). Triados como aceitáveis até o
ticket dono os endereçar.

## `govulncheck.txt`
| Vuln | Onde | Dono | Remediação |
|---|---|---|---|
| GO-2026-4602 (stdlib `os`, corrigida em go1.25.8) | `control-plane/pdp`, `kernel/reference-monitor` (código afetado) | Manutenção de toolchain | **bump da toolchain Go** (≥ go1.25.8) — NÃO é código dos módulos, fora do âmbito de AOS-010 |

> Esta é uma vulnerabilidade **real e afetante** detectada pelo govulncheck. Está
> na baseline apenas porque a sua correção é um upgrade de toolchain, não uma
> alteração de código (proibida neste ticket *chore*). Remover esta linha faz o
> gate de SCA ficar imediatamente vermelho — como demonstra o self-test C de
> forma determinista. Assim que a toolchain for actualizada, esta entrada deve
> ser removida.
