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

> **Revisão de AOS-192 (achado VAC-01 da auditoria v4).** A auditoria encontrou EVIDÊNCIA CITADA
> ERRADA em quatro eixos deste checklist. Cada um foi re-verificado contra o código e tratado pelo que
> é: **§13.1** (citação imprecisa — corrigida, com âncora nova para o PERMIT sobre a cadeia COMPLETA
> do nó), **§13.3** (âncora VACUOSA — teste corrigido, prova negativa executada, eixo reaberto e
> RE-MARCADO com evidência nova), **§13.6** (citação FALSA — corrigida e o eixo **REABERTO 🟡**, porque
> o que falta é prova, não redacção), **§13.7** (implicação atribuída ao teste errado — citação
> corrigida; a propriedade está genuinamente provada, ao nível da plataforma). Quando a regra da
> EPIC-15 §2 («estes eixos TÊM de estar VERDES») colide com a regra NÃO-VACUOSA, prevalece a segunda:
> marcar VERDE sem prova é o defeito, não a solução.

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
  Reference Monitor de produção que o nó compõe (`referencemonitor.NewProductionSecure`) com o
  **bundle de política Cedar ASSINADO committado** (`control-plane/pdp/policies`) carregado. A tool
  call é MEDIADA e **PERMITIDA** (permits≥1, denials=0) e a tool **EXECUTA** (o output real volta ao
  loop). Prova o caminho POSITIVO — a mediação DEIXA passar o legítimo, não só nega.
  **Precisão da citação (corrigida por AOS-192, achado VAC-01).** A cadeia deste teste é
  `identity → policy(PDP) → taint → scope → egress` — cinco hooks. A cadeia que o NÓ compõe
  (`integration.NewSecuredRuntime`, `secured.go:214-222`) tem **sete**: acrescenta
  `revalidation` (AOS-051, logo a seguir a identity) e `budget` (`BudgetStub`). Este teste
  prova, portanto, o PERMIT sobre a cadeia real **menos o hook de revalidação**; a redacção
  anterior sugeria a cadeia completa do nó.
- **Caminho PERMITIDO sobre a cadeia COMPLETA do nó (com o hook de revalidação):**
  `packages/cmd/aos/bootstrap_durable_execution_test.go` → `TestNode_DurableExecution_NoDoubleExecAfterRestart`
  (1.ª vida) e `packages/cmd/aos/durable_execution_env_test.go` → `TestAOS191_DurableExecutionReachableFromEnv`
  / `TestAOS191_WithoutEnvVarDurableCollaboratorsStayNil`. Nestes, o nó é composto por `Bootstrap`
  (logo com os SETE hooks, incluindo `revalidation`), com um `revalidation.Revalidator` REAL sobre um
  trust store que confia no publicador e uma entry ASSINADA no catálogo; o modelo emite uma tool call
  com `cap:fs.read`, a call atravessa a cadeia inteira e a tool **EXECUTA** (contador de execução = 1).
  É esta a âncora do PERMIT sobre a cadeia com revalidação — não a de `acceptance_mediation_test.go`.
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

## 3. DURABILIDADE — VERDE (REABERTO 🟡 por VAC-01, RE-MARCADO por AOS-192)

> "Injecção de *crash* em qualquer passo não produz efeitos duplicados no *retry*; 100% dos passos
> são reproduzíveis por *replay*."

### 3.0. Histórico: reabertura e re-marcação (AOS-192)

Este eixo foi **REABERTO (🟡)** pelo achado **VAC-01** da auditoria v4: a âncora que sustentava a parte
«uma tool call já executada não volta a executar após restart» era um teste **VACUOSO**.
`TestNode_DurableExecution_NoDoubleExecAfterRestart` partilhava **uma** instância de modelo pelas duas
vidas do nó; como o contador de turnos do modelo é monotónico, a 2.ª vida entrava em `turn>=3` e
devolvia `Final` **sem emitir tool call**. A asserção `execs == 1` passava porque a tool **nunca era
re-tentada** — não porque o ledger deduplicasse.

**Prova negativa (AOS-192, executada).** Com a verificação *already-applied* de
`durable.StepLedger.Apply` deliberadamente desactivada (`packages/kernel/agent-runtime/durable/step_ledger.go`,
guarda `l.records[key]`):

- uma réplica temporária da **forma antiga** do teste (instância de modelo partilhada) **PASSOU**
  (`--- PASS`, exit 0) — a demonstração empírica da vacuidade;
- o teste **CORRIGIDO** ficou **VERMELHO** (`--- FAIL`, exit 1) com
  `a tool foi RE-EXECUTADA após o restart (1 execuções na 2.ª vida): o ledger não deduplicou a tool call re-emitida`;
- os cinco testes de componente (`durable/step_ledger_test.go`) também ficaram vermelhos, confirmando
  que o mecanismo partido era o real.

**Sonda complementar (AOS-192, remediação dirigida) — qual asserção é que DETECTA.** Com o mesmo
mecanismo partido, e neutralizando APENAS a asserção 3 (`execs2 == 0`), o teste corrigido **PASSOU**
com `SONDA: execs2=1 sawP1=true sawP2=false` — ou seja, a tool **RE-EXECUTOU** e mesmo assim os bytes
da 2.ª vida **não** apareceram no prompt. Razão: o ramo `appendRes.Status == StatusDuplicate` de
`runEffect` (`durable/step_ledger.go:321-333`) corre o efeito **primeiro**, o Event Store deduplica o
`Append` e o `Apply` devolve o registo **canónico** da 1.ª vida. Consequência operacional registada:
a asserção 4 fecha a via de falso-verde «a tool nunca chegou a ser despachada», mas **não** detecta
re-execução; a asserção 3 é a única que a detecta e **não pode ser removida** por «redundância». O
comentário do teste diz exactamente isto, para que um mantenedor futuro não restaure a vacuidade.

Ambas as alterações foram **revertidas** (`git diff` vazio em `step_ledger.go`; ficheiro de teste
restaurado) e a réplica temporária apagada. O eixo é **RE-MARCADO VERDE** com a evidência NOVA de
3.1 — não com a antiga.

### 3.1. Deduplicação de TOOL CALL pelo step-ledger, AO NÍVEL DO NÓ (evidência NOVA, AOS-192)

`packages/cmd/aos/bootstrap_durable_execution_test.go` → `TestNode_DurableExecution_NoDoubleExecAfterRestart`.
Cada vida do nó recebe uma instância **NOVA** do modelo, pelo que a 2.ª vida **RE-EMITE** a tool call.
As asserções distinguem explicitamente «não re-executou porque **deduplicou**» de «não re-executou
porque **nunca tentou**»:

1. a 2.ª vida **emitiu ≥ 1 tool call** (sem isto, tudo o resto seria vacuoso);
2. `StepLedger.Applied(key)` — a API que expõe o *already-applied* — é **falso** no nó recém-arrancado
   e **verdadeiro** depois de `RebuildLedger`, com o resultado **canónico** da 1.ª vida; o único
   caminho que povoa esse estado é a releitura do WAL;
3. o contador de execução da tool **da 2.ª vida** fica a **zero** e o total das duas vidas em **1**
   — é **esta** a asserção que detecta re-execução, e é ela que a prova negativa dispara;
4. a tool call foi mesmo **DESPACHADA** e o resultado que voltou ao loop (prompt materializado do
   turno seguinte) foi o **memorizado** da 1.ª vida — fecha a via de falso-verde «a tool nunca
   chegou a ser despachada» (catálogo vazio, *deny* do PDP, run terminado antes do dispatch).
   **Limite declarado:** esta asserção **não** detecta re-execução; com a guarda *already-applied*
   in-memory partida, o efeito corre e o ramo `StatusDuplicate` de `runEffect`
   (`durable/step_ledger.go:321-333`) devolve na mesma o registo canónico, pelo que os bytes da 2.ª
   vida continuam a não aparecer no prompt. Verificado empiricamente; por isso a asserção 3 não
   pode ser removida por «redundância» face a esta;
5. o WAL mantém **exactamente um** evento `step.ledger.applied`, cuja chave é derivada do envelope
   realmente commitado e cruzada com a derivação canónica do sub-passo.

### 3.2. O que cada âncora prova (separação explícita)

- **`TestServiceShutdownDurable_NoLossNoDupNoDoubleExecAfterRestart`** (não-vacuoso, verificado
  independentemente) prova a durabilidade do **SUBSTRATO** e o **fencing do lease**: eventos
  commitados sobrevivem ao restart por replay do WAL byte-a-byte, sem perda nem duplicação
  (re-append ⇒ `StatusDuplicate` com o seq original), e um token de lease residual é *fenced*
  (`ErrLeaseSuperseded`). **NÃO** prova a deduplicação de **tool call** pelo step-ledger — não há
  tool call no seu caminho. Continua VERDE naquilo que prova.
- **`TestNode_DurableExecution_NoDoubleExecAfterRestart`** (3.1) é a **única** âncora ao nível do NÓ
  para a deduplicação de tool call pelo ledger reconstruído.
- **Componente:** `packages/kernel/agent-runtime/durable/step_ledger_test.go` →
  `TestApplyIdempotentReexecution`, `TestFaultInjectionCrashBeforeCommit`,
  `TestFaultInjectionCrashAfterCommit`, `TestLedgerSurvivesWorkerRestart`,
  `TestApplyCrossWorkerDuplicateReturnsCanonical`. A propriedade sempre esteve provada AQUI; o que
  faltava — e AOS-192 fecha — era a prova ao nível do NÓ.
- **Alcance do critério.** A metade «não produz efeitos duplicados no retry» está coberta acima. A
  metade «100% dos passos são reproduzíveis por *replay*» assenta no capturer de não-determinismo
  (`replay.EventStoreCapturer`, composto pelo nó quando `DurableExecution` está activa) e no
  dispatcher de replay (`activity.NewReplayDispatcher`); a sua prova fim-a-fim vive fora deste
  checklist (`packages/qa/dr-e2e`, EPIC-10). Não é reivindicada aqui como provada ao nível do nó.

### 3.3. Substrato e ambiente

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
  "duravel em disco (AOS-170)". **Alcance:** prova a costura do SUBSTRATO, não a da execução durável.
- **Alcançabilidade da EXECUÇÃO durável pelo binário (AOS-191):** `packages/cmd/aos/durable_execution_env_test.go`
  → `TestAOS191_RunComposesDurableExecutionFromEnv` (`AOS_DURABLE_EXECUTION` → `nodeConfigFromEnv` →
  `Bootstrap` compõe checkpointer+capturer+step-ledger), com o par negativo
  `TestAOS191_RunWithoutEnvVarDeclaresDurableExecutionOff` e o fail-closed
  `TestRunRejectsDurableExecutionWithoutDurableEventStore` (`ErrDurableExecutionNeedsDurableSubstrate`).
  Sem esta costura, tudo em 3.1 seria uma propriedade de uma capacidade **inalcançável** no artefacto
  entregue (era o achado REG-01/DUR-01).
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
  distroless + `docker run`) NÃO foi re-executado nesta remediação nem em AOS-192 — o eixo docker é
  ambiente, declarado, não fingido. A durabilidade fica VERDE ao nível do NÓ independentemente do
  harness, agora com a âncora NÃO-VACUOSA de 3.1 (`TestNode_DurableExecution_NoDoubleExecAfterRestart`)
  para a deduplicação de tool call, mais `TestServiceShutdownDurable_…` para o substrato/fencing e
  `TestAOS169_DurableSubstrateWiredFromEnv` + AOS-191 para a costura de ambiente.

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

## 6. OBSERVABILIDADE — 🟡 REABERTO-COM-EIXO (eixo: ramo `execute_tool` na árvore exportada pelo nó; ticket **AOS-204**)

> "Cada *run* produz uma árvore de *spans* OTel GenAI completa e um registo *audit* WORM
> *tamper-evident*."

**Reaberto por AOS-192** (achado VAC-01, terceira citação errada). O que estava escrito — que
`TestObservabilityEndToEndExportsWellFormedOTLPWithCost` exporta «invoke_agent/chat[+custo]/
**execute_tool**/freeze» — é **FALSO** quanto a `execute_tool`: esse teste usa `tnBaseConfig()`, cujo
`Config.Model` é nil, pelo que o `Bootstrap` injecta o `referenceModel` (`bootstrap.go:830-837`), que
devolve `Final: true` **sem nenhuma tool call**. O run não tem ramo de tool, o teste não assere
`execute_tool` (só `invoke_agent`, `registry.freeze_toolset` e `chat`) e a palavra «completa» do
critério ficava por provar ao nível do nó. Marcar isto VERDE era exactamente o defeito que AOS-192
corrige.

- **PROVADO (permanece VERDE):** exportação OTLP/HTTP bem-formada de `invoke_agent`,
  `registry.freeze_toolset` e `chat` com `gen_ai.*`/`aos.*` e **custo** (micro-USD + USD), sem
  segredos, com estatísticas de exportação sem falhas/drops — `packages/cmd/aos/observability_test.go`
  → `TestObservabilityEndToEndExportsWellFormedOTLPWithCost` (AOS-173).
- **PROVADO ao nível de COMPONENTE (não do nó):** uma tool call produz **exactamente um** span
  `execute_tool`, aberto SÓ pelo Reference Monitor (a autoridade do span), filho do `aos.activity` do
  dispatcher durável — `packages/kernel/agent-runtime/activity/dispatch_test.go` (bloco `:553-…`); o RM
  anota-lhe o veredicto (`monitor.go:203`). A hierarquia `invoke_agent → execute_tool → chat` é
  reconstruída e verificada em `packages/control-plane/governance/trajectory-surface/trajectory_surface_test.go`.
- **EIXO REABERTO (por provar) — dono: AOS-204** (`specs/EPIC-18_Remediacao_Auditoria_Multiagente_v4.md`
  §7): **nenhum** teste exporta por OTLP, a partir do NÓ real, a árvore de um run **que contenha uma
  tool call** — logo o ramo `execute_tool` da árvore do nó não está observado ponta-a-ponta. Fecho
  natural (escopo de AOS-204): dar ao teste de observabilidade um modelo que emite tool call (o span
  nasce mesmo sob veredicto de negação) e asserir o `execute_tool` exportado. Fora do âmbito de
  AOS-192 (P0/S), que corrige a citação e nomeia o eixo — com **ticket real**, como exige o CA de
  AOS-196 («todo o deferimento tem eixo válido … com um ticket real») — em vez de o manter verde
  sem prova.
- **WORM tamper-evident + ligação trajectória↔hash-chain:** mesmo ficheiro
  → `TestAuditTracingStoreEmitsSealSpanLinkingWORM`, `TestAuditSealFlowsToOTLPCollector`. O WORM é a
  hash-chain tamper-evident única partilhada pelo RM e pelo egress (AOS-154); durável (FileStore,
  AOS-170).
- **Gating fail-open/fail-closed:** `TestObservabilityFailOpenOnCollectorError`,
  `TestObservabilityMalformedEndpointFailsClosed`.

## 7. CONFORMIDADE — VERDE

> "DSAR (Art. 17) satisfeito por *crypto-shredding* sem quebrar a integridade do log encadeado."

- **Crypto-shredding sem quebrar a hash-chain (ÂNCORA DA PROPRIEDADE — citação corrigida por
  AOS-192):** `packages/platform/audit/aos083_test.go` → `TestCryptoShreddingRemovesPIIKeepsChain`.
  É o único teste que exercita a propriedade INTEIRA: PII **realmente cifrada** sob a KEK do titular e
  ingerida no meio da cadeia; **pré-shred** a cadeia verifica, o checkpoint assinado verifica e a PII é
  **recuperável**; destruída a KEK (`Shredder.Shred`), a PII passa a **irrecuperável**
  (`ErrShredded`) e a cadeia **continua íntegra e verificável** (o `EntryHash` selou o `ContentHash` do
  ciphertext, não do plaintext); o shred é idempotente.
- **Fluxo DSAR ao nível do NÓ (endpoint, autorização, legal hold, selagem, destruição da KEK):**
  `packages/cmd/aos/sovereignty_test.go` → `TestDSARErasesAndSeals` (a KEK por-titular é destruída;
  `received`/`key_destroyed` selados no WORM SEM PII; a hash-chain permanece íntegra e verificável),
  `TestDSARIdempotent`, `TestDSARBlockedByLegalHold` (legal hold re-consultado antes do shred —
  preservação P0), `TestDSAREndpointRequiresAuth` / `TestDSAREndpointOffWithoutSovereignty`
  (fail-closed do endpoint `POST /dsar/erase`). Fluxo composto no nó (AOS-172, `bootstrap.go` passo 7c).
  **Limite honesto (AOS-192):** o helper `seedPII` (`sovereignty_test.go:378-388`) apenas chama
  `DSARVault.EnsureKey` — **nunca cifra nada** com a KEK. Estes testes provam, portanto, que a chave é
  destruída (ou preservada sob hold) e que a cadeia sela sem PII; a implicação «⇒ PII irrecuperável»
  é provada no teste de plataforma acima, não aqui. A redacção anterior atribuía essa implicação ao
  teste do nó.
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
| 1 | Mediação | **VERDE** (citação corrigida, AOS-192) | PERMIT sobre cadeia de 5 hooks: `TestAOS169_Mediation_PermitPath_ToolExecutes`; PERMIT sobre a cadeia COMPLETA do nó (7 hooks, **com revalidação**): `TestNode_DurableExecution_NoDoubleExecAfterRestart` (1.ª vida), `TestAOS191_DurableExecutionReachableFromEnv`; DENY: `TestAOS169_Mediation_DenyPath_ToolBlocked`; NO-BYPASS: `TestAOS169_Mediation_NoBypass_FullNodeAPI`; `TestApexEnforcement_FiveDenials` |
| 2 | Identidade | **DEFERIDO-COM-EIXO** (identidade/D4) | `TestNodeComposesRealVerifier`; modo self-hosted Nível 2 (AOS-156); `D4-escalacao-autoridade-identidade.md` |
| 3 | Durabilidade | **VERDE** (reaberto 🟡 por VAC-01, **re-marcado por AOS-192** com evidência nova) | dedup de TOOL CALL pelo ledger no NÓ: `TestNode_DurableExecution_NoDoubleExecAfterRestart` (corrigido; prova negativa executada, §3.0); substrato/WAL + fencing de lease: `TestServiceShutdownDurable_NoLossNoDupNoDoubleExecAfterRestart` (**não prova dedup de tool call**); componente: `durable/step_ledger_test.go` (5 testes); ambiente: `TestAOS169_DurableSubstrateWiredFromEnv` + AOS-191; `aos169-durability-harness.sh` (harness docker não re-executado — eixo ambiente) |
| 4 | Isolamento | **VERDE** | `TestApexEnforcement_FiveDenials` (egress); nó compõe `EgressHook` real (bootstrap.go §7); EPIC-07 |
| 5 | Governação | **VERDE** | bundle assinado enforçado em `TestAOS169_Mediation_*`; `TestNodeComposesFourEyesGate`; promoção via `scripts/ci/evalgate.sh` |
| 6 | Observabilidade | 🟡 **REABERTO-COM-EIXO** (AOS-192; eixo: ramo `execute_tool` na árvore exportada pelo nó; **dono: AOS-204**) | PROVADO: `TestObservabilityEndToEndExportsWellFormedOTLPWithCost` (invoke_agent/chat+custo/freeze), `TestAuditTracingStoreEmitsSealSpanLinkingWORM`; `execute_tool` só ao nível de COMPONENTE (`activity/dispatch_test.go`) — o run do teste do nó **não emite tool call** |
| 7 | Conformidade | **VERDE** (citação corrigida, AOS-192) | propriedade: `TestCryptoShreddingRemovesPIIKeepsChain` (`platform/audit`, PII cifrada ⇒ `ErrShredded` ⇒ cadeia íntegra); fluxo no nó: `TestDSARErasesAndSeals`, `TestDSARBlockedByLegalHold`, `TestDSARIdempotent` (a KEK do nó **nunca cifrou nada** — ver §7) |

**Cinco de sete critérios VERDES com evidência real; um deferimento (IDENTIDADE §13.2, eixo
D4/org-provisioning, modo self-hosted Nível 2 declarado) e um eixo REABERTO (OBSERVABILIDADE §13.6,
ramo `execute_tool` na árvore exportada pelo nó, dono AOS-204).** DURABILIDADE §13.3 foi reaberta por VAC-01 e está
**re-marcada VERDE** com a evidência NOVA de AOS-192 (§3.1), acompanhada da prova negativa (§3.0). A
regra continua a ser a da EPIC-15 §2 — mas prevalece a regra NÃO-VACUOSA da Carta §2.1: um eixo sem
prova sólida fica AMARELO com o eixo NOMEADO em vez de VERDE por inércia.
