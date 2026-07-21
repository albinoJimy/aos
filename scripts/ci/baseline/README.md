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
| G115 (CWE-190) overflow int64→uint64 (×1) | `packages/platform/messaging/message.go` | AOS-073 | **falso positivo** — `uint64(v)` é a serialização binária canónica de um `int64` (`binary.BigEndian.PutUint64`), não uma fronteira de confiança |
| G115 (CWE-190) overflow uint64→int64 (×1) | `packages/substrate/sandbox/snapshot.go` | AOS-065 | **falso positivo** — `time.Duration(seq%(span+1))` é um módulo LIMITADO (span=25; resultado ∈ [0,25]), nunca transborda |
| G101 (CWE-798) "credencial hardcoded" (×2) | `packages/substrate/otel-genai/semconv.go` | AOS-076 | **falso positivo** — nomes OTel semconv (`gen_ai.usage.input_tokens`/`output_tokens`); MIGRADAS de `agent-runtime/tracing.go` (que passou a aliases sem literais) no fecho do EPIC-08 — o vocabulário é agora a fonte-única no módulo folha otel-genai |
| G101 (CWE-798) "credencial hardcoded" (×1) | `packages/kernel/agent-runtime/breaker/breaker.go` | AOS-080 | **falso positivo** — nome de atributo OTel do span de trip (`aos.breaker.tokens_per_s`, contém a substring "token"), não uma credencial |
| G407 (CWE-1204) nonce/IV "hardcoded" (×4) | `packages/platform/audit/crypto.go` | AOS-083 | **falso positivo** — mesmo padrão de crypto-shredding de `episodic/crypto.go`: AES-256-GCM em envelope (KEK-por-titular embrulha DEK-por-registo), nonce de 96 bits gerado por `RandSource` injectável (crypto/rand em produção, determinístico em teste); o gosec não segue a entropia injectada |
| G115 (CWE-190) overflow int64→uint64 (×1) | `packages/control-plane/governance/hitl/encode.go` | AOS-096 | **falso positivo** — `putInt64` faz `uint64(v)` na serialização binária canónica length-prefixed (mesmo molde de `messaging`/`audit`), não uma fronteira de confiança |
| G101 (CWE-798) "credencial hardcoded" (×1) | `packages/control-plane/governance/hitl/ratification.go` | AOS-096 | **falso positivo** — nome de atributo OTel do gate de ratificação (`aos.ratification.canary_passed`, contém a substring "pass"), não uma credencial |
| G115 (CWE-190) overflow int→uint8 (×1) | `packages/control-plane/governance/autonomy/controller.go` | AOS-090 | **falso positivo** — `demote` devolve `Level(t)` com `t` LIMITADO (`int(floor) ≤ t ≤ int(cur) ≤ 255`), nunca transborda o `uint8` do `Level` |
| G115 (CWE-190) overflow uint64→int64 (×2) | `packages/control-plane/governance/compliance/report.go` | AOS-097 | **falso positivo** — `int64(from)`/`int64(to)` são sequence numbers do audit num atributo de telemetria (span), não uma fronteira de confiança |
| G407 (CWE-1204) nonce/IV "hardcoded" (×4) | `packages/platform/backup/crypto.go` | AOS-101 | **falso positivo** — mesmo envelope AES-256-GCM de dois níveis do backup imutável (DEK-por-segmento embrulhada pela KEK-por-região); os 2 `Seal` usam nonces do `RandSource` injectável (crypto/rand em produção, determinístico em teste) e os 2 `Open` lêem o nonce armazenado no blob — o gosec não segue a entropia injectada nem distingue Open de Seal (fecho do EPIC-10) |
| G115 (CWE-190) overflow int64→uint64 (×1) | `packages/platform/backup/manifest.go` | AOS-101 | **falso positivo** — `putInt64` faz `uint64(v)` na serialização binária canónica do manifesto hash-chain (mesmo molde de messaging/audit), não fronteira de confiança |
| G115 (CWE-190) overflow int→uint64 (×1) | `packages/platform/backup/restore.go` | AOS-101 | **falso positivo** — `uint64(i)+1` com `i` índice de range (≥0) na verificação de encadeamento gapless dos segmentos |
| G115 (CWE-190) overflow int→uint64 (×1) | `packages/substrate/eventstore/backup.go` | AOS-101 | **falso positivo** — `last + uint64(i) + 1` com `i` índice de range (≥0) na validação de seq gapless do `IngestStream` de restauro |
| G115 (CWE-190) overflow int→uint64 (×1) | `packages/substrate/eventstore/sharding.go` | AOS-100 | **falso positivo** — `uint64(size-1)` (máscara do stripe) com `size` potência-de-dois ≥1, logo `size-1`≥0 e limitado |
| G115 (CWE-190) overflow int64→uint64 (×3) | `packages/platform/eval/spans.go` | AOS-115 | **falso positivo** — `uint64(InputTokens)`/`uint64(OutputTokens)`/`uint64(CostMicroUSD)` na serialização binária canónica (`binary.BigEndian.PutUint64`) do usage no seed do trace_id determinista; é reinterpretação de padrão de bits para *hashing* (SHA-256), sem aritmética que transborde, e o usage é não-negativo por construção — não fronteira de confiança (mesmo molde de messaging/audit/backup). SURGIU no sweep de fecho do EPIC-11 |
| G407 (CWE-1204) nonce/IV "hardcoded" (×2) | `packages/substrate/redaction/token.go` | AOS-091 | **falso positivo** — tokenização determinística de PII (SIV/DAE): (1) `gcm.Seal` usa `deterministicNonce = HMAC-SHA256(chave-secreta-por-titular, class‖0x00‖value)[:12]` — o nonce é uma PRF sobre o VALOR COMPLETO sob chave secreta, logo a reutilização de nonce ⟺ plaintext idêntico ⟺ token estável desejado (fuga de igualdade é o tradeoff documentado); a injectividade de `class‖0x00‖value` assenta em `Class` ser um enum fechado sem `0x00` (`email`/`phone`/`credit_card`/`iban`/`ip`). (2) `gcm.Open` lê o nonce *prepended* ao ciphertext (`raw[:ns]`) — é descriptografia, não cifra. SURGIU no sweep de fecho do EPIC-09 (a verificação por-ticket de AOS-091 não corria gosec) |

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
| **20 vulns de stdlib** (`crypto/tls`, `crypto/x509`, `net/http`, `net/url`, `net/textproto`, `os` — ex.: GO-2026-5856 ECH, GO-2026-5039/5037 TLS, GO-2026-4971 x509; corrigidas em go1.25.8..go1.25.12) | `platform/model-gateway` (código afetado — o adaptador **HTTPS real** `internal/adapters/openai_http.go` chama `http.Client.Do` → `crypto/tls`/`crypto/x509`) | Manutenção de toolchain | **bump da toolchain Go** (≥ go1.25.12) — triadas no fecho do EPIC-06 (AOS-063) |
| **20 vulns de stdlib** (mesmas classes + GO-2025-3956; corrigidas em go1.25.x) | `platform/registry` (código afetado — caminhos `crypto/x509`/`net/url`/`net/textproto` via assinatura/digest/MCP) | Manutenção de toolchain | **bump da toolchain Go** (≥ go1.25.12) — triadas no fecho do EPIC-06 (AOS-063) |
| **2 vulns de stdlib** (`net/url` — GO-2025-4010 corrigida em go1.24.8, GO-2026-4601 em go1.25.8) | `substrate/sandbox` (código afetado — caminho `net/url` via modelação da rede; o sandbox **modela** o egress, não faz HTTPS real, logo só toca `net/url`, não a superfície TLS/x509 completa) | Manutenção de toolchain | **bump da toolchain Go** (≥ go1.25.8) — triadas no fecho do EPIC-07 (AOS-075) |
| **15 vulns de stdlib** (subconjunto das de `model-gateway` — TLS/x509/HTTP/URL; ex.: GO-2025-4007/4008/4010, GO-2026-5856 ECH, GO-2026-5037 TLS) | `integration` (o composition-root **compõe** o `model-gateway`, alcançando a mesma superfície HTTPS real via o grafo do ápice; nenhum caminho novo — subconjunto do já aceite) | Manutenção de toolchain | **bump da toolchain Go** (≥ go1.25.12) — **herdadas** de `model-gateway`; mesmas entradas, novo módulo; triadas no fecho de PR-0.a (AOS-146/148) |

> A entrada `GO-2026-4602` e os dois blocos de 20 são vulnerabilidades **reais e
> afetantes** detectadas pelo govulncheck — **não são falsos positivos** (ao
> contrário das entradas gosec). Estão na baseline apenas porque a sua correção é um
> **upgrade de toolchain Go** (todas "Fixed in go1.25.x"), não uma alteração de
> código dos módulos. O `model-gateway` e o `registry` são os primeiros módulos com
> caminhos reais de TLS/x509/HTTP/URL, pelo que expõem a superfície de stdlib que os
> restantes módulos não tocam. **Follow-up sinalizado: bumpar a toolchain Go para
> ≥ go1.25.12 e remover TODAS estas entradas** (a correção real; as entradas
> voltam a avermelhar o gate — self-test C — se removidas antes do bump).
>
> **Nota de processo (AOS-063 / fecho do EPIC-06):** o gate `sca.sh` tinha um
> **falso positivo** — os padrões de erro de execução `connection`/`timeout` "nus"
> casavam com prosa de descrição de vulnerabilidade (ex.: "persistent connection")
> quando o govulncheck sai 3 com vulns reais, marcando erradamente uma falha de
> execução e **impedindo** a extração dos IDs para a baseline. Corrigido para as
> formas concretas de erro de rede do Go (`connection refused`/`i/o timeout`/`dial
> tcp`); sem isto o `model-gateway`/`registry` nunca eram comparados com a baseline.
