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
| G407 (CWE-1204) nonce/IV "hardcoded" (×4) | `packages/platform/memory/episodic/crypto.go` | AOS-038 | **falso positivo** — nonce por `randSrc(nonce)` (CSPRNG injectável); refactor p/ gosec o ver, ou `#nosec G407` justificado |
| G407 (CWE-1204) nonce/IV "hardcoded" (×4) | `packages/platform/memory/semantic/knowledge_base.go` | AOS-039 | **falso positivo** — mesmo padrão de nonce CSPRNG injectável |
| G115 (CWE-190) overflow int64→uint64 (×1) | `packages/platform/memory/procedural/promotion_hooks.go` | AOS-040 | **falso positivo** — `uint64(v)` é a serialização binária canónica em `binary.BigEndian.PutUint64` |
| G115 (CWE-190) overflow uint64→int (×1) | `packages/platform/memory/working/window_manager.go` | AOS-037 | contador de `seq`/turno pequeno; validar limites / `#nosec` |
| G101 (CWE-798) "credencial hardcoded" (×2) | `packages/platform/memory/working/window_manager.go` | AOS-037 | **falso positivo** — nomes de atributos OTel (`aos.working.*_tokens`), não credenciais |
| G101 (CWE-798) "credencial hardcoded" (×1) | `packages/platform/memory/compression/async_compactor.go` | AOS-043 | **falso positivo** — nome de atributo OTel (`aos.memory.compression.summary_tokens`) |
| G101 (CWE-798) "credencial hardcoded" (×2) | `packages/kernel/agent-runtime/tracing.go` | AOS-013 | **falso positivo** — nomes OTel semconv (`gen_ai.usage.input_tokens`/`output_tokens`) |
| G115 (CWE-190) overflow de conversão inteira (×1) | `packages/platform/audit/record.go` | AOS-011 | **falso positivo** — `uint64(len(...))` na serialização binária (`len` é sempre ≥0) |
| G101 (CWE-798) "credencial hardcoded" (×1) | `packages/control-plane/scheduler/breaker.go` | AOS-029 | **falso positivo** — nomes de atributos OTel (`aos.breaker.*_tokens`) |
| G101 (CWE-798) "credencial hardcoded" (×1) | `packages/control-plane/scheduler/routing.go` | AOS-033 | **falso positivo** — nomes de atributos OTel (`aos.routing.*_tokens`) |
| G101 (CWE-798) "credencial hardcoded" (×1) | `packages/control-plane/scheduler/spawn_admission.go` | AOS-028 | **falso positivo** — nomes de atributos OTel (`aos.spawn.*_tokens`) |
| G115 (CWE-190) overflow `uint32(len)` (×2) | `packages/platform/registry/digest/canonical.go` | AOS-047 | **falso positivo** — length-prefix de *domain separation* (`len` ≥ 0; >4 GiB inalcançável) |
| G115 (CWE-190) overflow `uint32(len)` (×2) | `packages/platform/registry/domain/digest.go` | AOS-045 | **falso positivo** — length-prefix de *domain separation* (`len` ≥ 0; >4 GiB inalcançável) |

Severidade dos G115 é HIGH mas são conversões de contadores/timestamps/serialização
binária (não fronteira de confiança). Os **G407** (nonce/IV) são **falsos positivos**:
o crypto-shredding usa AES-256-GCM com nonce aleatório de 96 bits por registo
(`randSrc`/`rnd`, um CSPRNG injectável para determinismo em teste = `rand.Read` em
produção) — o gosec não consegue seguir a função de entropia injectada e trata o
slice como "hardcoded". A unicidade do nonce foi verificada pelas auditorias
adversariais de AOS-038/039/042. Os **G101** são nomes de atributos de span OTel
que contêm a substring "token". Triados como aceitáveis (falsos positivos / código
não-fronteira) até os tickets donos os endereçarem com `#nosec` justificado.

> **Nota de processo (honesta):** estas entradas de memória acumularam-se ao longo
> de AOS-035..043 porque a verificação por-ticket corria `gofmt`/`vet`/`staticcheck`/
> `-race` mas **não** `gosec` (o gate SAST completo é lento, ~5 min). Foram triadas
> em bloco no âmbito de AOS-044 (fecho do EPIC-04). Nenhuma é uma vulnerabilidade
> real.

## `govulncheck.txt`
| Vuln | Onde | Dono | Remediação |
|---|---|---|---|
| GO-2026-4602 (stdlib `os`, corrigida em go1.25.8) | `control-plane/pdp`, `kernel/reference-monitor`, `kernel/agent-runtime` (código afetado — `os.ReadDir` no lint de separação de AOS-021) | Manutenção de toolchain | **bump da toolchain Go** (≥ go1.25.8) — NÃO é código dos módulos, fora do âmbito de AOS-010 |

> Esta é uma vulnerabilidade **real e afetante** detectada pelo govulncheck. Está
> na baseline apenas porque a sua correção é um upgrade de toolchain, não uma
> alteração de código (proibida neste ticket *chore*). Remover esta linha faz o
> gate de SCA ficar imediatamente vermelho — como demonstra o self-test C de
> forma determinista. Assim que a toolchain for actualizada, esta entrada deve
> ser removida.
