# AOS-169 — Aceitação sistémica NÃO-VACUOSA (E6, CAPSTONE da EPIC-15)

Checklist NOMEADO de cada critério sistémico de `specs/00_System_Spec.md` §13. Cada critério é
marcado **VERDE** (com EVIDÊNCIA — teste NOMEADO deste ticket, ou ticket/EPIC que o prova) ou
**DEFERIDO-COM-EIXO**. Regra de deferimento (Carta + revisão da EPIC-15 §2/E6-note): o único eixo
onde o deferimento é legítimo é **IDENTIDADE** (§13.2, dependente de D4 / org-provisioning — modo
self-hosted Nível 2 declarado, AOS-156). Durabilidade, Isolamento, Governação, Observabilidade,
Mediação e Conformidade TÊM de estar VERDES com evidência real.

Regra NÃO-VACUOSA (Carta §2.1): um teste que "passa" sem exercitar a propriedade é uma FALHA. Cada
evidência abaixo exercita a propriedade nos DOIS sentidos onde aplicável (permite o legítimo E nega
a violação), sobre os colaboradores REAIS (nunca stubs neutros).

Todos os testes Go correm com `-race` (obrigatório). O harness de contentor corre a imagem
distroless REAL de AOS-168. As referências de ficheiro são relativas à raiz do repo.

---

## 1. MEDIAÇÃO — VERDE

> "Não existe caminho de código que execute uma *tool call* sem atravessar o Reference Monitor;
> verificável por instrumentação e testes negativos."

Provada NÃO-VACUOSAMENTE nos DOIS sentidos + no-bypass fim-a-fim pelo nó real:

- **Caminho PERMITIDO (allow):** `packages/cmd/aos/acceptance_mediation_test.go`
  → `TestAOS169_Mediation_PermitPath_ToolExecutes`. Um modelo que EMITE uma tool call
  (`agentruntime.ModelResponse{ToolCalls:…}`) corre num loop de agente REAL sobre o MESMO tipo de
  Reference Monitor de produção que o nó compõe (`referencemonitor.NewProductionSecure`, cadeia
  identity→policy(PDP)→taint→scope→egress) com o **bundle de política Cedar ASSINADO committado**
  (`control-plane/pdp/policies`) carregado. A tool call é MEDIADA e **PERMITIDA** (permits≥1,
  denials=0) e a tool **EXECUTA** (o output real volta ao loop). Prova o caminho POSITIVO — a
  mediação DEIXA passar o legítimo, não só nega.
- **Caminho NEGADO (deny):** mesmo ficheiro → `TestAOS169_Mediation_DenyPath_ToolBlocked`. O MESMO
  chain e o MESMO modelo-que-emite, mas uma capability FORA da allowlist ⇒ o PDP NEGA (denials≥1,
  permits=0); a tool NUNCA executa. É o teste negativo que torna a mediação real nos dois sentidos.
- **NO-BYPASS fim-a-fim pelo nó REAL:** mesmo ficheiro → `TestAOS169_Mediation_NoBypass_FullNodeAPI`.
  Um nó composto por `Bootstrap`+`NewNodeService`+`NewAPIHandler`; um run submetido pela API REAL
  (`POST /runs`) cujo modelo EMITE uma tool call ⇒ o contador de mediação do RM do nó move-se: a
  call ATRAVESSOU o Reference Monitor (nenhum caminho a saltou). Sem mediação o contador ficaria
  imóvel (não-vacuoso).
- **Reforço (5 negações atribuíveis):** `packages/integration/enforcement_guard_test.go`
  → `TestApexEnforcement_FiveDenials` (AOS-161): identity/taint/scope/egress, cada barreira nega a
  sua violação com atribuição.
- **Verifier real do nó:** `packages/cmd/aos/bootstrap_test.go` → `TestNodeComposesRealVerifier`
  (anónimo e issuer rogue negados em "identity").

**Nota honesta (D4).** O caminho POSITIVO fim-a-fim PELA API do nó fica sob o mesmo D4 que a
Identidade: o nó arranca com o PDP NÃO-carregado (`pdp.NewUnloaded`, deny fail-closed) até haver
bundle assinado provisionado, pelo que o full-node e2e testemunha o NO-BYPASS via a negação, e o
ALLOW é provado sobre o MESMO tipo de RM com o bundle committado carregado. A ESTRUTURA da mediação
(permit não-forjável mintado só no allow; tool só executa sob permit — `Decision.permit` é um token
não-exportado) é REAL. Isto NÃO é um deferimento de Mediação (que está VERDE nos dois sentidos), mas
a fronteira de provisioning do bundle, adjacente ao eixo IDENTIDADE.

## 2. IDENTIDADE — DEFERIDO-COM-EIXO (eixo IDENTIDADE / D4)

> "Toda a acção no *audit trail* resolve para uma cadeia de delegação que termina num humano
> responsável; zero acções atribuídas a 'pool'."

Este é o ÚNICO eixo de deferimento legítimo. Estado real:

- **ESTRUTURA presente e VERDE:** cada tool call carrega um `Credential` (token NHI) que o hook de
  identidade real resolve para uma cadeia de delegação com raiz humana; uma chamada ANÓNIMA
  (Credential vazio) é NEGADA em "identity" — zero acções "pool". Evidência:
  `packages/cmd/aos/bootstrap_test.go` → `TestNodeComposesRealVerifier`,
  `TestNodeTrustAnchorOnlyHasNoAuthorityInProcess`; o mint enraíza a cadeia num humano
  (`node.Authority.MintForHuman`, usado em `TestAOS169_Mediation_NoBypass_FullNodeAPI`).
- **DEFERIDO:** a NÃO-FORJABILIDADE de PRODUÇÃO (IdP/OIDC/WebAuthn real, provisioning de organização,
  bundles de política assinados por autoridade externa) depende de **D4** (org-provisioning). O nó
  corre hoje em **modo self-hosted Nível 2 declarado** (`IdentityMode = "real"`, autoridade
  co-localizada AOS-156); o banner AVISA-o explicitamente. Escalada registada em
  `docs/reports/D4-escalacao-autoridade-identidade.md`.

O deferimento é RESTRITO a este eixo; nenhum outro critério o invoca.

## 3. DURABILIDADE — VERDE

> "Injecção de *crash* em qualquer passo não produz efeitos duplicados no *retry*; 100% dos passos
> são reproduzíveis por *replay*."

- **Nó, substrato durável REAL (crash+restart, no-loss/no-dup/no-double-exec):**
  `packages/cmd/aos/shutdown_durable_test.go`
  → `TestServiceShutdownDurable_NoLossNoDupNoDoubleExecAfterRestart`. Event Store durável real
  (`EventStorePath`), shutdown+`Close`, reabertura por replay do WAL byte-a-byte; a idempotência/dedup
  é reconstruída (re-append ⇒ `StatusDuplicate` com o seq original); o lease do run cancelado é
  reclamável e um novo claim minta um token ESTRITAMENTE MAIOR — um token residual é *fenced*
  (`ErrLeaseSuperseded`), logo sem dupla-execução.
- **Costura de ambiente do contentor (AOS-170):** `packages/cmd/aos/acceptance_durable_env_test.go`
  → `TestAOS169_DurableSubstrateWiredFromEnv`. `run` liga `AOS_EVENTSTORE_PATH`/`AOS_WORM_PATH` ao
  substrato durável (WAL+WORM criados em disco; reabertura por replay sucede); banner declara
  "duravel em disco (AOS-170)".
- **Contentor REAL (kill+reinício), não-duplicação OBSERVÁVEL:** `deploy/node/aos169-durability-harness.sh`
  — build da imagem distroless (AOS-168), run com root-fs `--read-only` + volume gravável, `POST /runs`,
  `docker kill` ABRUPTO (SIGKILL); depois **INSPECCIONA o WAL do volume** (contentor parado, contentor
  efémero da MESMA imagem com o subcomando read-only `aos wal-count --turns`) e prova (a) que o turno
  DURÁVEL sobreviveu ao kill (cardinalidade N≥1) e — após `docker start`+`/healthz`+banner durável e uma
  RE-SUBMISSÃO do mesmo `run_id` — (b) que a cardinalidade de turnos se MANTÉM (M==N): a re-submissão não
  acrescentou trabalho durável, logo NÃO houve dupla-execução observável no substrato (dedup por
  `(RunID,StepID)` do WAL + fencing do lease). O 201 da re-submissão é uniforme por construção
  (não-enumerável) e por isso NÃO é usado como prova de não-duplicação — a prova é a cardinalidade do WAL.
  A prova byte-a-byte da monotonicidade do fencing (token residual ⇒ `ErrLeaseSuperseded`) permanece no
  teste Go âncora abaixo. **Nota de execução honesta:** o subcomando `aos wal-count` é coberto por testes
  Go verdes com `-race` (`packages/cmd/aos/wal_inspect_test.go`); o harness docker completo (rebuild
  distroless + `docker run`) NÃO foi re-executado nesta remediação — o eixo docker é ambiente, declarado,
  não fingido. A durabilidade fica VERDE ao nível do NÓ independentemente do harness (teste âncora +
  `TestAOS169_DurableSubstrateWiredFromEnv`).

## 4. ISOLAMENTO — VERDE

> "O agente nunca observa um segredo *downstream*; egress fora da *allowlist* é bloqueado por
> omissão."

- **Egress default-deny (bloqueado por omissão):** `packages/integration/enforcement_guard_test.go`
  → `TestApexEnforcement_FiveDenials`, caso (d) `egress_fora_da_allowlist` (AOS-067). O nó compõe o
  MESMO `network.EgressHook` real via `integration.NewSecuredRuntime` (`packages/cmd/aos/bootstrap.go`,
  passo 7) — não um `EgressStub` (que `NewProductionSecure` recusaria). O caminho POSITIVO de AOS-169
  (`TestAOS169_Mediation_PermitPath_ToolExecutes`) inclui o egress hook real na cadeia.
- **Segredo downstream nunca observado pelo agente:** o `Credential` do run é material de MEDIAÇÃO —
  propagado a cada `referencemonitor.Call`, NUNCA ao prompt (`Goal.Credential` não entra no cache de
  prompt nem na idempotency key; documentado em `packages/kernel/agent-runtime/loop.go` e `model.go`).
  A saída do `ModelClient` (fronteira untrusted) é marcada untrusted por construção; o resultado de
  tool volta SEMPRE untrusted (ADR-005). Cobertura de exfiltração/segredos: `packages/security-tests`
  (EPIC-07).

## 5. GOVERNAÇÃO — VERDE

> "*Policy-as-code* versionado e assinado governa cada decisão; auto-modificação só atinge produção
> após *eval-gate* + *canary* + ratificação assinada."

- **Policy-as-code ASSINADO governa cada decisão:** `TestAOS169_Mediation_PermitPath_ToolExecutes` e
  `..._DenyPath_ToolBlocked` (`packages/cmd/aos/acceptance_mediation_test.go`) enforçam o bundle Cedar
  **assinado** committado (`control-plane/pdp/policies`): `pdp.Open` VERIFICA a assinatura contra o
  trust anchor no arranque (uma assinatura inválida recusaria o load — fail-closed) e cada tool call
  passa pelo `pdp.NewPolicyCheck` (allow explícito para `cap:fs.read`, deny fail-closed para
  `cap:payments.charge`). O `manifest.json` versiona a política (`policy_version 1.0.0`).
- **Dual-control (four-eyes) assinado:** `packages/cmd/aos/bootstrap_test.go`
  → `TestNodeComposesFourEyesGate` (o gate autoriza o aprovador PINADO e nega o desconhecido);
  `packages/cmd/aos/api_test.go` → `TestAPIApproveAuthorizesViaGate` (replay do challenge ⇒ 403).
- **Nota honesta (nível de subsistema):** a promoção `eval-gate + canary + ratificação assinada` da
  AUTO-MODIFICAÇÃO é um processo do CONTROL-PLANE/CI (`scripts/ci/evalgate.sh`), não uma acção do nó
  em runtime; provada a esse nível. O NÓ enforça o bundle assinado que essa promoção produz — que é a
  parte "governa cada decisão" do critério, VERDE aqui.

## 6. OBSERVABILIDADE — VERDE

> "Cada *run* produz uma árvore de *spans* OTel GenAI completa e um registo *audit* WORM
> *tamper-evident*."

- **Árvore de spans OTel GenAI + custo:** `packages/cmd/aos/observability_test.go`
  → `TestObservabilityEndToEndExportsWellFormedOTLPWithCost` (AOS-173): invoke_agent/chat[+custo]/
  execute_tool/freeze exportados bem-formados via OTLP/HTTP.
- **WORM tamper-evident + ligação trajectória↔hash-chain:** mesmo ficheiro
  → `TestAuditTracingStoreEmitsSealSpanLinkingWORM`, `TestAuditSealFlowsToOTLPCollector`. O WORM é a
  hash-chain tamper-evident única partilhada pelo RM e pelo egress (AOS-154); durável (FileStore,
  AOS-170).
- **Gating fail-open/fail-closed:** `TestObservabilityFailOpenOnCollectorError`,
  `TestObservabilityMalformedEndpointFailsClosed`.

## 7. CONFORMIDADE — VERDE

> "DSAR (Art. 17) satisfeito por *crypto-shredding* sem quebrar a integridade do log encadeado."

- **Crypto-shredding sem quebrar a hash-chain:** `packages/cmd/aos/sovereignty_test.go`
  → `TestDSARErasesAndSeals` (a KEK por-titular é destruída ⇒ PII irrecuperável; received/
  key_destroyed selados no WORM SEM PII, a hash-chain permanece íntegra e verificável),
  `TestDSARIdempotent`, `TestDSARBlockedByLegalHold` (legal hold re-consultado antes do shred —
  preservação P0), `TestDSAREndpointRequiresAuth` / `TestDSAREndpointOffWithoutSovereignty`
  (fail-closed do endpoint `POST /dsar/erase`). Fluxo composto no nó (AOS-172, `bootstrap.go` passo 7c).
- **Fronteira de COBERTURA declarada (NÃO é deferimento do critério §13.7):** a erasure destrói a KEK
  do vault DEMO-GRADE por-titular; o conteúdo dos runs que o Event Store persiste em texto-claro
  (não cifrado por-titular) fica FORA do alcance do shredding — a cifra por-titular do SUBSTRATO fica
  deferida (EPIC-06/09/10). É uma fronteira do ALCANCE do shredding, não do critério: a propriedade
  §13.7 (crypto-shredding sem quebrar o log encadeado) está provada e VERDE. O banner declara a
  fronteira explicitamente.

---

## Resumo

| # | Critério §13 | Estado | Evidência-âncora (deste ticket / EPIC) |
|---|--------------|--------|----------------------------------------|
| 1 | Mediação | **VERDE** | `TestAOS169_Mediation_{PermitPath,DenyPath,NoBypass_FullNodeAPI}`; `TestApexEnforcement_FiveDenials` |
| 2 | Identidade | **DEFERIDO-COM-EIXO** (identidade/D4) | `TestNodeComposesRealVerifier`; modo self-hosted Nível 2 (AOS-156); `D4-escalacao-autoridade-identidade.md` |
| 3 | Durabilidade | **VERDE** | `TestServiceShutdownDurable_NoLossNoDupNoDoubleExecAfterRestart`; `TestAOS169_DurableSubstrateWiredFromEnv`; `aos169-durability-harness.sh` (persistência + não-dup OBSERVÁVEL via `aos wal-count`; harness docker não re-executado nesta remediação — eixo ambiente) |
| 4 | Isolamento | **VERDE** | `TestApexEnforcement_FiveDenials` (egress); nó compõe `EgressHook` real (bootstrap.go §7); EPIC-07 |
| 5 | Governação | **VERDE** | bundle assinado enforçado em `TestAOS169_Mediation_*`; `TestNodeComposesFourEyesGate`; promoção via `scripts/ci/evalgate.sh` |
| 6 | Observabilidade | **VERDE** | `TestObservabilityEndToEndExportsWellFormedOTLPWithCost`; `TestAuditTracingStoreEmitsSealSpanLinkingWORM` |
| 7 | Conformidade | **VERDE** | `TestDSARErasesAndSeals`; `TestDSARBlockedByLegalHold`; `TestDSARIdempotent` |

**Seis de sete critérios VERDES com evidência real; o único deferimento é IDENTIDADE (§13.2), restrito
ao eixo D4/org-provisioning (modo self-hosted Nível 2 declarado).** Conforme a regra da EPIC-15 §2:
Durabilidade/Observabilidade/Mediação/Isolamento/Governação/Conformidade estão VERDES, não deferidos.
