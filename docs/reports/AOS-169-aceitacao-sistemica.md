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
> RE-MARCADO com evidência nova), **§13.6** (citação FALSA — corrigida e o eixo REABERTO 🟡 por
> AOS-192, porque o que faltava era prova e não redacção; **fechado e RE-MARCADO VERDE por AOS-204 no
> despacho DIRECTO**, com evidência nova e prova negativa em três variantes — uma delas partindo a
> instrumentação de PRODUÇÃO — e o residual da execução DURÁVEL, então nomeado em §6.3, **fechado por
> AOS-210** com a árvore `aos.activity` → `execute_tool` verificada por topologia a partir do nó,
> §6.0/§6.1/§6.3), **§13.7** (implicação atribuída ao teste errado — citação
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

## 6. OBSERVABILIDADE — **VERDE** (despacho DIRECTO **e** execução DURÁVEL) — reaberto 🟡 por AOS-192, re-marcado por AOS-204, **residual §6.3 fechado por AOS-210**

> "Cada *run* produz uma árvore de *spans* OTel GenAI completa e um registo *audit* WORM
> *tamper-evident*."

### 6.0 O defeito que estava aqui, e a prova negativa do seu fecho

**Reaberto por AOS-192** (achado VAC-01, terceira citação errada). O que estava escrito — que
`TestObservabilityEndToEndExportsWellFormedOTLPWithCost` exporta «invoke_agent/chat[+custo]/
**execute_tool**/freeze» — era **FALSO** quanto a `execute_tool`: esse teste usa `tnBaseConfig()`, cujo
`Config.Model` é nil, pelo que o `Bootstrap` injecta o `referenceModel` (`bootstrap.go`), que devolve
`Final: true` **sem nenhuma tool call**. O run não tinha ramo de tool, o teste não asseria
`execute_tool` (só `invoke_agent`, `registry.freeze_toolset` e `chat`) e a palavra «completa» do
critério ficava por provar ao nível do nó.

**AOS-204 fecha o eixo** acrescentando a `packages/cmd/aos/observability_test.go` o teste
`TestAOS204_ObservabilityExportsExecuteToolBranchInSameTrace` (dois sub-testes: caminho PERMITIDO e
caminho NEGADO). **Zero alterações a código de produção ENTREGUES** — o permit é obtido só por
composição, pelas portas de `Config` que já existiam. (A variante (C) da prova negativa, abaixo, parte
deliberadamente a produção para demonstrar o acoplamento e é revertida no acto; nada dela é entregue.)

**PROVA NEGATIVA executada — TRÊS variantes, todas revertidas** (árvore de trabalho conferida no fim:
`md5sum` de cada ficheiro tocado idêntico ao original, zero ocorrências do marcador temporário,
`git diff --numstat` de volta a `377 0` só em `observability_test.go`). As duas primeiras partem o
TESTE; a **terceira parte a INSTRUMENTAÇÃO DE PRODUÇÃO**, e é essa que demonstra que a asserção está
**acoplada ao nó** e não é auto-referencial.

> **Âncora da evidência.** Cita-se o NOME do sub-teste e o TEXTO da mensagem de falha — estáveis e
> pesquisáveis. Os números de linha das variantes (A) e (B) são **deliberadamente omitidos**: dependem
> da FORMA da edição temporária e não se reproduzem a partir do ficheiro committado (execuções
> independentes da mesma variante (B) deram `:798`, `:803` e `:808` para a MESMA asserção). Comando
> em todas as variantes: `go test ./ -race -count=1 -run TestAOS204 -v` em `packages/cmd/aos`.

- **(A) [TESTE] o ramo deixa de ser exportado** — filtrando os spans `execute_tool` do documento OTLP
  recebido pelo colector, mantendo tudo o resto idêntico. `caminho PERMITIDO` fica **VERMELHO**:

  ```
  faltou o ramo "execute_tool" na arvore EXPORTADA PELO NO — §13.6 exige a arvore COMPLETA;
  nomes vistos: [registry.freeze_toolset chat audit_seal chat invoke_agent]
  ```

- **(B) [TESTE] o defeito ORIGINAL de AOS-192 reconstituído** — trocando o modelo que emite tool call
  pelo `referenceModel{}` (o que nunca emite) e neutralizando os portões anti-vacuidade, para o teste
  ALCANÇAR a asserção do ramo em vez de morrer antes dela. `caminho PERMITIDO` fica **VERMELHO**:

  ```
  faltou o ramo "execute_tool" na arvore EXPORTADA PELO NO — §13.6 exige a arvore COMPLETA;
  nomes vistos: [registry.freeze_toolset chat invoke_agent]
  ```

  A lista `[registry.freeze_toolset chat invoke_agent]` é **exactamente** o conjunto que a evidência
  antiga citava como se incluísse `execute_tool`. A variante (B) reproduz o falso-verde e o teste
  novo pinta-o de vermelho.

- **(C) [PRODUÇÃO] a instrumentação REAL é partida** — a variante que fecha o argumento «a asserção
  está ligada ao nó, não a si própria». Duas sub-variantes, aplicadas a código de produção **fora do
  âmbito de escrita deste ticket**, executadas e imediatamente revertidas (`md5sum` de `loop.go` e de
  `monitor.go` conferido contra a cópia original; `git diff` vazio nos dois):

  - **(C1) o RM deixa de receber o tracer do RT** — neutralizando `rt.rm.SetTracer(rt.tracer)` em
    `kernel/agent-runtime/loop.go` (o wiring AOS-076). O RM cai no `NoopTracer` default e o span
    deixa de nascer. **Os DOIS sub-testes ficam VERMELHOS:**

    ```
    caminho PERMITIDO: faltou o ramo "execute_tool" na arvore EXPORTADA PELO NO — §13.6 exige a
                       arvore COMPLETA; nomes vistos:
                       [registry.freeze_toolset chat audit_seal chat invoke_agent]
    caminho NEGADO:    faltou o ramo "execute_tool" de uma tool call NEGADA — a negacao tem de ser
                       observavel na arvore; nomes: [registry.freeze_toolset chat audit_seal chat invoke_agent]
    ```

  - **(C2) o span nasce FORA da árvore** — trocando `m.tracer.StartSpan(ctx, …)` por
    `StartSpan(context.Background(), …)` em `kernel/reference-monitor/monitor.go`. O span
    `execute_tool` **existe, com o nome certo e os atributos certos**, mas noutro trace. Os dois
    sub-testes ficam **VERMELHOS na asserção de TOPOLOGIA**:

    ```
    span "execute_tool" num TRACE DIFERENTE de "invoke_agent":
    "0b084384f8a2ff0800fdd8abb6c42960" != "373284064238a2e3d9329a780422357d" — a arvore esta partida
    ```

    Ou seja: a asserção de topologia de §6.1 (o ponto «mesmo `traceId` + `parentSpanId ==
    invoke_agent.spanId`», implementada em `obsAssertChildOf`) **não é decorativa**. Sem ela, um nó que exportasse
    o ramo DESLIGADO da árvore passaria — e uma árvore desligada não é uma árvore.

**A observação que sustenta a decisão de AOS-192 de reabrir o eixo.** Sob **ambas** as sub-variantes
de produção — com o ramo AUSENTE (C1) e com o ramo DESPARENTEADO (C2) — os testes de observabilidade
**PREEXISTENTES ao nível do nó** (`TestObservabilityEndToEndExportsWellFormedOTLPWithCost`,
`TestAuditTracingStoreEmitsSealSpanLinkingWORM`, `TestAuditSealFlowsToOTLPCollector`) continuaram
**todos a PASSAR**. A bateria antiga era cega não só à AUSÊNCIA do ramo (que é o que a variante (B)
reconstitui) como à sua DESLIGAÇÃO da árvore: nenhum sinal de regressão viria dela. É a justificação
directa para o eixo ter sido reaberto — e para a asserção de topologia, e não só a de presença, ter
sido necessária.

### 6.1 Evidência NOVA (AOS-204) — a árvore COMPLETA exportada pelo NÓ

`packages/cmd/aos/observability_test.go` → `TestAOS204_ObservabilityExportsExecuteToolBranchInSameTrace`:

- **Caminho PERMITIDO (a prova forte).** O nó é composto por `Bootstrap` com a observabilidade ligada
  a um colector `httptest` **e** com a cadeia no estado em que uma tool call legítima é PERMITIDA
  ponta-a-ponta: bundle Cedar **assinado** committado (`pdp.Open("../../control-plane/pdp/policies")`),
  entry `counter` **assinada** + trust store que confia no publicador (o hook de revalidação AOS-051
  corre a sério), `authz.AuthoritySource` para o ScopeGate e um token NHI mintado pela autoridade do
  **próprio nó** (`node.Authority.MintForHuman`) — o mesmo precedente de
  `TestNode_DurableExecution_NoDoubleExecAfterRestart`. Um modelo que EMITE a tool call corre pela
  cadeia REAL de 7 hooks; a tool **executa** (`execs == 1`, `permits >= 1`, `denials == 0`).
  Do documento OTLP recebido assere-se a **topologia**, não só a presença de nomes:
  - exactamente **um** `invoke_agent`, e ele é a **raiz** (`parentSpanId` vazio);
  - cada `chat` e cada `execute_tool` partilham o **mesmo `traceId`** do `invoke_agent` e têm
    `parentSpanId == invoke_agent.spanId` (o `execute_tool` é aberto pelo RM com o ctx do
    `invoke_agent` — `kernel/agent-runtime/loop.go`, `reference-monitor/monitor.go`);
  - existe um `execute_tool` com `aos.decision = "permit"`, `gen_ai.tool.name = "counter"`,
    `aos.principal.nhi`, `aos.tool.call_hash` e `aos.run_id` — o veredicto do efeito é observável;
  - **a segunda metade do critério, na MESMA árvore:** o selo WORM da mediação (`audit_seal`) é
    exportado como **filho do `execute_tool` permitido**, no mesmo trace, com o
    `aos.audit.entry_hash` (a âncora *tamper-evident* da hash-chain) e `aos.decision = allow`.
    A trajectória e o registo WORM ficam ligados por `parent_span_id`, não só por correlação de ids;
  - **sem segredos**: nem nos atributos nem no corpo OTLP bruto POSTado.
- **Caminho NEGADO (cobertura dos dois sentidos).** O chain **default** do nó (PDP `NewUnloaded`,
  deny fail-closed) com o mesmo modelo-que-emite: o ramo `execute_tool` **sai na mesma**, filho do
  `invoke_agent` e no mesmo trace, com `aos.decision = "deny"` e `aos.denied_by` preenchido — uma
  negação não é um buraco na trajectória.

**Porque o caminho PERMITIDO era necessário (justificação da decisão de âmbito).** O span
`execute_tool` é aberto em `Monitor.Mediate` **antes** da avaliação da cadeia e fechado por `defer` em
todos os caminhos, pelo que uma tool call negada já o exportava. Isso prova a **mediação** (§13.1),
mas não a árvore **completa** de §13.6: numa negação nada é despachado, o veredicto é `deny` e o selo
WORM aninhado é o de uma NÃO-execução. Um nó que exportasse bem a árvore dos runs que não fazem nada
e a partisse nos runs que produzem efeitos passaria num teste só-negativo. Por isso a âncora deste
eixo é o caminho **permitido** — onde a tool executa, o veredicto é `permit` e o selo WORM da
execução real fica dentro da árvore — e o negado é o complemento, não o substituto.

### 6.2 Evidência que já existia (permanece VERDE)

- **Exportação OTLP/HTTP bem-formada** de `invoke_agent`, `registry.freeze_toolset` e `chat` com
  `gen_ai.*`/`aos.*` e **custo** (micro-USD + USD), sem segredos, com estatísticas de exportação sem
  falhas/drops — `TestObservabilityEndToEndExportsWellFormedOTLPWithCost` (AOS-173).
- **Ao nível de COMPONENTE:** uma tool call produz **exactamente um** span `execute_tool`, aberto SÓ
  pelo Reference Monitor (a autoridade do span), filho do `aos.activity` do dispatcher durável —
  `packages/kernel/agent-runtime/activity/dispatch_test.go`; o RM anota-lhe o veredicto
  (`monitor.go`). A hierarquia é também reconstruída em
  `packages/control-plane/governance/trajectory-surface/trajectory_surface_test.go`.
- **WORM tamper-evident + ligação trajectória↔hash-chain:** `TestAuditTracingStoreEmitsSealSpanLinkingWORM`,
  `TestAuditSealFlowsToOTLPCollector`. O WORM é a hash-chain tamper-evident única partilhada pelo RM e
  pelo egress (AOS-154); durável (FileStore, AOS-170).
- **Gating fail-open/fail-closed:** `TestObservabilityFailOpenOnCollectorError`,
  `TestObservabilityMalformedEndpointFailsClosed`.

### 6.3 Residual NOMEADO — **FECHADO por AOS-210** (2026-07-27)

> **Estado: FECHADO.** O que esta secção descrevia como pendência de produção sem dono foi convertido
> em ticket (**AOS-210**, `specs/EPIC-18` §8-bis, execução EPIC-14) e **entregue**. O texto original do
> achado fica preservado abaixo, para que a história não seja reescrita.

O `execute_tool` provado ponta-a-ponta pelo nó em AOS-204 é o do despacho **directo**
(`ActivityDispatcher` default, `AOS_DURABLE_EXECUTION` desligado). Com a execução durável ligada
interpõe-se o `activity.Dispatcher`, que abre um span `aos.activity`
(`kernel/agent-runtime/activity/dispatch.go`, `OpActivity`) entre o `invoke_agent` e o `execute_tool`,
anotado com o desfecho durável (dedup/replay/custo do efeito real).

**Achado ORIGINAL de leitura de código (AOS-204, não corrigido nesse ticket — exigia mudança de
PRODUÇÃO):** `integration/secured.go` compunha esse dispatcher com
`activity.NewDispatcher(rm, cfg.Ledger)` **sem lhe passar tracer**, e o default de
`activity.Dispatcher` é `NoopTracer` — pelo que, no nó com execução durável, o span `aos.activity`
**não era exportado**. O ramo `execute_tool` em si continuava a sair (o RM recebe o tracer do RT via
`rt.rm.SetTracer`, e é o RM a autoridade desse span), pelo que §13.6 nunca foi reaberto por isto; o que
faltava era a **camada intermédia de dedup durável** na árvore.

**FECHO (AOS-210).** `SecuredConfig` passou a expor uma via **explícita** de tracer
(`SecuredConfig.Tracer`, `packages/integration/secured.go`) — e não uma extracção de
`RuntimeOptions`/`FreezeOptions`, que são fatias de funções **opacas** e não inspeccionáveis — que
`secured.go` entrega a `activity.NewDispatcher` via `activity.WithTracer`. O nó
(`packages/cmd/aos/bootstrap.go`) preenche-a com o **MESMO** tracer que já entrega em
`agentruntime.WithTracer` e `toolset.WithTracer`, e **só** quando a observabilidade está ligada.

Evidência — tudo com `-race`, contra um colector OTLP `httptest`, com `AOS_DURABLE_EXECUTION` ligado:

- **ÁRVORE (topologia, não presença de nomes):**
  `TestAOS210_DurableExecutionExportsActivitySpanAsParentOfExecuteTool`
  (`packages/cmd/aos/observability_durable_test.go`) — a árvore **exportada pelo nó** contém
  `aos.activity`, e `parentSpanId(execute_tool) == spanId(aos.activity)` no **mesmo** `traceId`; o
  `aos.activity` é por sua vez filho do `invoke_agent`. O selo WORM `audit_seal` continua **filho do
  `execute_tool`** — a interposição **não** parte a ligação trajectória↔hash-chain de §13.6.
- **NÃO-DUPLICAÇÃO (o risco que `dispatch.go` nomeia):** no mesmo teste, `execute_tool` aparece
  **exactamente uma vez** na árvore exportada. A separação de operações mantém-se DELIBERADA: o
  `aos.activity` carrega `aos.decision=permit`/`tool`/`run_id`/`step_id`; o `execute_tool` mantém
  `aos.tool_call.hash` e o veredicto, que **só** o RM anota.
- **ANTI-VACUIDADE:** o teste assere que o modelo EMITIU a tool call, que ela EXECUTOU **exactamente
  uma vez** sob permit da cadeia real de 7 hooks (`permits>=1`, `denials==0`) e que o resultado voltou
  ao loop — sem isso, "não vi spans" seria indistinguível de "nunca houve nada a instrumentar".
- **PROVA NEGATIVA executada (mutação):** removendo a ligação em `bootstrap.go`, o teste fica VERMELHO
  com `esperava EXACTAMENTE 1 span "aos.activity" [...] vieram 0; nomes vistos: [registry.freeze_toolset
  chat audit_seal execute_tool chat invoke_agent]` — a árvore restante intacta, exactamente o defeito
  aqui descrito. A ligação foi reposta.
- **RETRO-COMPATIBILIDADE (dois níveis):**
  `TestAOS210_WithoutChainTracerNoActivitySpanIsEmitted`
  (`packages/integration/durable_activity_tracing_test.go`) é a prova negativa **directa**: com
  `SecuredConfig.Tracer` em branco mas o **mesmo** tracer ligado ao RT/RM, saem **zero** spans
  `aos.activity` e o `execute_tool` sai na mesma (logo a ausência é atribuível ao dispatcher, não a
  "não houve run"). Ao nível do NÓ,
  `TestAOS210_DurableExecutionWithoutTracerKeepsBehaviourAndExportsNothing` mostra que o mesmo cenário
  durável sem `AOS_OTLP_ENDPOINT` não abre exporter, não envia **um único byte** a um colector que está
  a correr ao lado, e produz um desfecho observável **idêntico** ao do nó instrumentado.

**Residual REMANESCENTE — DOIS pontos, ambos com DONO (`AOS-211`) — RESOLVIDOS.** Pôr o `aos.activity` na árvore
exportada tornou visíveis duas lacunas do próprio span. Nenhuma delas estava no âmbito de AOS-210, e
nenhuma ficou **sem dono**: a regra que justificou AOS-210 («residual nomeado tem de ser encaminhado»)
vale para quem a invoca, pelo que ambas foram propriedade de **AOS-211** (`specs/EPIC-18` §8-bis,
execução EPIC-08). Nomear sem encaminhar, no mesmo parágrafo em que se declara ter terminado essa
deriva, seria repeti-la.

**ESTADO (AOS-211, entregue):** o ponto 2 (`gen_ai.operation.name`) foi **FECHADO** — `startSpan` anota-o e
`OpActivity` entrou em `otelgenai.requiredAttrs` (o span deixou de estar isento do contrato semconv de AOS-076).
O ponto 1 (custo por efeito real) foi **DEFERIDO com razão nomeada** — não há fonte declarada do custo do efeito
na via durável do nó (`referencemonitor.Call`/`Decision` e `activity.Result` não o carregam), pelo que
`CostMicroUSD` fica a 0 (0 não emite) e a ausência da fonte está registada como **DEF-810** (`REGISTO-Deferimentos.md`).

1. **Custo por efeito real ausente.** *(→ DEFERIDO, DEF-810.)* O `aos.activity` do nó **não** carrega `gen_ai.usage.cost_usd`: o
   adaptador `integration.DurableDispatcher` traduz o `referencemonitor.Call` numa `activity.Activity`
   **sem** `CostMicroUSD` (`runtime_ports.go`), pelo que a anotação de custo por efeito real de
   `dispatch.go` nunca dispara nesta via. O **desfecho** durável (`permit|dedup|replay|denied|error`) é
   exportado; o **custo por efeito** não.
2. **`gen_ai.operation.name` ausente.** *(→ FECHADO por AOS-211.)* O `aos.activity` era o único span da árvore durável exportada que
   **não** trazia `gen_ai.operation.name` (`invoke_agent`, `chat`, `execute_tool` e `audit_seal` trazem-no).
   Duas consequências verificáveis: (a) `otelgenai.ValidateSpanData` resolve a operação por *fallback* ao
   `Name`, não encontra entrada em `requiredAttrs` e **aceita o span sem validar** — fica isento do
   contrato semconv de AOS-076; (b) consumidores que leem estritamente o atributo, sem *fallback* — p. ex.
   o `operationOf` de `packages/platform/eval/spans.go` — **nunca** vêem este span como uma operação.
   Não é regressão nem foi introduzido por AOS-210 (o conteúdo do span é de AOS-021, e
   `registry.freeze_toolset` tem a mesma forma), e não causa dupla-contagem em agregador nenhum — mas é
   AOS-210 que faz este span chegar pela primeira vez à árvore exportada pelo nó, e por isso é aqui que
   fica registado.

Nenhum dos dois bloqueia §13.6: o custo do turno vive no span `chat` e o agregado do run no
`invoke_agent`, ambos provados em `TestObservabilityEndToEndExportsWellFormedOTLPWithCost`; e a
topologia da árvore — o que o eixo exige — é asserida por `parent_span_id`, não por atributo.

**Nota histórica (redacção de AOS-204, agora superada).** A versão anterior desta secção dizia que, na
configuração durável, a afirmação «o ramo `execute_tool` sai na mesma» era sustentada por **leitura de
código** e não por teste ponta-a-ponta — e por isso o VERDE de §13.6 era declarado **no despacho
DIRECTO**, com qualificador. Dizia ainda que a pendência «**não tem dono atribuído**: tem de ser
entregue a quem detém `packages/integration`, sob pena de reproduzir a deriva "residual nomeado que
nunca é encaminhado"». **Ambas as coisas deixaram de ser verdade:** o dono foi atribuído (AOS-210,
`specs/EPIC-18` §8-bis, execução EPIC-14) e a configuração durável passou a ser verificada **a partir do
nó**, contra um colector OTLP real — o `execute_tool` é agora observado, e não inferido, nessa
configuração. O qualificador do VERDE de §13.6 pode por isso cair (ver cabeçalho de §6 e linha 6 da
tabela-resumo).

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
| 6 | Observabilidade | **VERDE** (despacho DIRECTO **e** execução DURÁVEL) — reaberto 🟡 por AOS-192, re-marcado por AOS-204, **residual §6.3 fechado por AOS-210** (residual remanescente do span `aos.activity` — custo por efeito e `operation.name` — nomeado **e encaminhado a AOS-211**) | árvore COMPLETA exportada PELO NÓ, com topologia (`traceId` partilhado + `parentSpanId`) e o selo WORM `audit_seal` como FILHO do `execute_tool` permitido: `TestAOS204_ObservabilityExportsExecuteToolBranchInSameTrace` (permitido **e** negado; prova negativa executada em duas variantes, §6.0); base: `TestObservabilityEndToEndExportsWellFormedOTLPWithCost` (custo), `TestAuditTracingStoreEmitsSealSpanLinkingWORM`, `TestAuditSealFlowsToOTLPCollector`. **Execução DURÁVEL (residual §6.3 FECHADO por AOS-210):** com `AOS_DURABLE_EXECUTION` ligado, a árvore exportada pelo nó contém `aos.activity` e ele é **PAI** do `execute_tool` (`parentSpanId(execute_tool) == spanId(aos.activity)`, mesmo `traceId`), com `execute_tool` **exactamente uma vez** e o `audit_seal` ainda seu filho: `TestAOS210_DurableExecutionExportsActivitySpanAsParentOfExecuteTool`; retro-compatibilidade provada em `TestAOS210_WithoutChainTracerNoActivitySpanIsEmitted` (prova negativa directa) e `TestAOS210_DurableExecutionWithoutTracerKeepsBehaviourAndExportsNothing`. **Residual remanescente nomeado, com dono AOS-211:** o `aos.activity` do nó não carrega o custo por efeito real (o adaptador `DurableDispatcher` não propaga `CostMicroUSD`) nem o `gen_ai.operation.name` (fica isento do contrato semconv de AOS-076) — ver §6.3 |
| 7 | Conformidade | **VERDE** (citação corrigida, AOS-192) | propriedade: `TestCryptoShreddingRemovesPIIKeepsChain` (`platform/audit`, PII cifrada ⇒ `ErrShredded` ⇒ cadeia íntegra); fluxo no nó: `TestDSARErasesAndSeals`, `TestDSARBlockedByLegalHold`, `TestDSARIdempotent` (a KEK do nó **nunca cifrou nada** — ver §7) |

**Seis de sete critérios VERDES com evidência real; um único deferimento (IDENTIDADE §13.2, eixo
D4/org-provisioning, modo self-hosted Nível 2 declarado).** DOIS eixos foram reabertos pela auditoria
v4 e AMBOS estão hoje **re-marcados VERDES com evidência NOVA e prova negativa executada**:
DURABILIDADE §13.3 (reaberta por VAC-01, fechada por AOS-192 — §3.0/§3.1) e OBSERVABILIDADE §13.6
(reaberta por AOS-192, fechada por **AOS-204** — §6.0/§6.1: o ramo `execute_tool` passa a ser
exportado e asserido a partir do NÓ, com a topologia da árvore e o selo WORM ligado por
`parent_span_id`).

**A reserva de âmbito que este sumário carregava CAIU (AOS-210, 2026-07-27).** Até aqui, o VERDE de
§13.6 valia só para a configuração de despacho **DIRECTO**: com `AOS_DURABLE_EXECUTION` ligado — uma
configuração REAL e seleccionável pelo operador — a árvore exportada estava **comprovadamente incompleta
numa camada** (o span `aos.activity` não saía, porque `integration/secured.go` compunha o dispatcher sem
tracer) e essa configuração não tinha sido re-verificada a partir do nó. O **AOS-210** deu a
`SecuredConfig` uma via explícita de tracer, ligou-a no nó, e a configuração durável passou a ser
verificada a partir do NÓ contra um colector OTLP: `aos.activity` é **PAI** do `execute_tool` na árvore
exportada, o `execute_tool` sai **uma só vez**, e sem observabilidade nada muda (§6.3).

**Residual remanescente, nomeado E ENCAMINHADO (dono: `AOS-211`):** nessa via, o `aos.activity` não
carrega (a) o **custo por efeito real** — o adaptador `integration.DurableDispatcher` não propaga
`CostMicroUSD` para a `activity.Activity` — nem (b) o `gen_ai.operation.name`, o que o deixa isento do
contrato semconv de AOS-076 (`ValidateSpanData` cai no *fallback* ao nome do span e aceita-o sem
validar). O desfecho durável (permit/dedup/replay/denied) é exportado; o custo por efeito não. Nenhum
dos dois bloqueia §13.6 (o custo por turno e o agregado do run estão provados noutros spans; a topologia
é asserida por `parent_span_id`, não por atributo), e ambos ficam escritos em §6.3 **com ticket**
(`specs/EPIC-18` §8-bis) em vez de desaparecer — ou de repetir a deriva «residual nomeado sem dono» que
este mesmo ticket veio terminar.

Nenhum eixo fica AMARELO. A regra continua a ser a da EPIC-15 §2 — e continua subordinada à regra
NÃO-VACUOSA da Carta §2.1: cada re-marcação acima traz a prova negativa que a sustenta (para §13.6,
incluindo a variante que parte a INSTRUMENTAÇÃO DE PRODUÇÃO, §6.0 (C), e a mutação de AOS-210 que
suprime o `aos.activity` da árvore), e o residual que resta é declarado no sumário e não só no detalhe —
com **dono** (AOS-211), porque um residual nomeado sem executor é a deriva que AOS-210 veio terminar.
