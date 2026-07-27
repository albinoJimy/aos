# ADR-012 — SemVer + eval-gate para auto-modificação

| Campo | Valor |
|---|---|
| ADR | ADR-012 |
| Título | SemVer + eval-gate para auto-modificação |
| Estado | Aceite |
| Data | Julho 2026 |
| Deciders | Equipa AOS |
| Contexto-fonte | Catálogo de ADRs em `_BRIEF.md` (linha 81) e `specs/00_System_Spec.md` (linha 255); EPIC-05 (Registry/SemVer), EPIC-08 (canary/OTel-GenAI), EPIC-09 (governança/ratificação, AOS-096), EPIC-11 (eval harness, AOS-114/115), EPIC-12 (loop de autoria, AOS-126) |

## Contexto

A auto-modificação — skills auto-escritas, prompts e memória procedural que o próprio sistema produz e promove — é a mudança de **maior risco** do AOS: a *misevolution* (degradação silenciosa de comportamento por auto-evolução) ocorre **mesmo sem atacante**. Uma skill que regride, uma memória procedural que aprende um mau hábito, ou um schema que quebra um contrato a jusante podem chegar a produção sem que nenhuma fronteira de segurança clássica dispare, porque não há um adversário — há apenas evolução não-avaliada.

O enunciado canónico do catálogo é: *"Skills/prompts/schemas versionados; auto-modificação passa por staging → eval-gate (golden-set) → canary → ratificação assinada → prod, com rollback atómico."*

A métrica de sucesso associada (`_BRIEF.md`, `specs/00_System_Spec.md`) é dura: **0 auto-modificações não avaliadas em produção**, com o eval-gate a servir de *admission control* comportamental. Isto exige tratar a promoção de um artefacto auto-escrito não como um *deploy*, mas como uma cadeia de gates fail-closed em que cada passo tem de passar e cada transição é selada de forma tamper-evident. O AOS materializa esta decisão como **modelos e protocolos Go in-process** (filosofia reference-model, zero dependências externas além de `cedar-go`/stdlib, build determinista/offline): o pipeline, o harness de avaliação e o gate de ratificação são bibliotecas compostas por porta+adaptador, não serviços em rede.

## Decisão

Toda a auto-modificação atravessa um **pipeline de admissão fail-closed, versionado e auditado**, com cinco propriedades não-negociáveis. A distinção entre um artefacto de terceiros (só verificação de integridade) e uma **skill auto-escrita** (governança completa) é **estrutural** — decidida por um `classifier` por *kind*+origem, não por convenção.

### 1. Versionamento SemVer com validação de *bump*

Skills, prompts, schemas e memória procedural são versionados em SemVer. Antes de qualquer promoção, o *bump* proposto é validado contra a *baseline* active corrente da linha de versões: uma quebra de contrato estrutural (ou uma quebra semântica declarada via `SemanticsBroken`) exige o *bump* correcto, sob pena de rejeição de integridade. A classificação de mudança vive em `packages/platform/registry/semver/classify.go`; a validação é invocada em `Pipeline.Promote` (`packages/platform/registry/promotion/pipeline.go`) através de `semver.ValidateBump(semver.ChangeRequest{...})`.

### 2. Eval-gate (golden-set) como *admission control* comportamental

Só uma skill auto-escrita que **passe o eval-gate** é apresentável às etapas seguintes. O harness concreto (`packages/platform/eval/harness.go`, `goldenset.go`, AOS-114) conduz o candidato — uma função **determinista** `input → (output + acções)` — sobre golden-sets curados e versionados, corre **ambos os datasets** (o golden curado, que apanha regressões novas, e o *failure_derived*, que apanha regressões conhecidas), agrega *success-rate*/*unsafe-action-rate* e emite um veredicto tipado ligado ao trace (`gen_ai.evaluation.result`).

O default é **rejeitar**: `DefaultMinScore = 1.0` e **zero acções unsafe** — qualquer acção proibida (`ForbiddenActions`) reprova incondicionalmente. O trace-diffing vs *baseline* aprovada (AOS-115) fornece o segundo sinal (contagem real de regressões) através de `packages/platform/eval/gateadapter/adapter.go`, que liga o harness às portas `ThresholdEvalGate` dos consumidores. Os construtores concretos `NewEvalGateFromHarness` vivem agora nos próprios consumidores — `packages/platform/registry/promotion/evaladapter.go` e `packages/platform/memory/procedural/evaladapter.go` — para evitar dependências cíclicas; o gateadapter fornece as funções `PromotionMetrics`/`ProceduralMetrics` (e as variantes VsBaseline) que alimentam o campo `Metrics` desses gates. Fail-closed em toda a fronteira: um id desconhecido, um artefacto não-admitido, ou um resolver ausente produzem uma métrica que reprova qualquer limiar (`rejectScore`/`rejectRegressions`).

No pipeline de promoção, `Pipeline.runEvalGate` **recusa** (`ErrNoEvalGate`) se nenhum gate estiver ligado — a omissão de *wiring* nunca é um verde por omissão.

### 3. Canary

Entre o eval-gate e a ratificação, o artefacto passa por uma fase de canary (EPIC-08). O gate de ratificação de AOS-096 (`packages/control-plane/governance/hitl/ratification.go`) trata `CanaryPassed` como **pré-condição**: `CanaryPassed=false` ⇒ o artefacto nem é apresentável à ratificação (`ReasonPreconditionFailed`, fail-closed antes de se olhar para a assinatura).

### 4. Ratificação humana assinada (não-repúdio)

Nenhum artefacto auto-escrito chega a produção sem uma **ratificação humana assinada ed25519**, verificada contra a chave **pinada** do humano responsável. O sistema **nunca detém a chave privada humana** — a assinatura é produzida fora do sistema (dispositivo/HSM do ratificador). Existem duas materializações coerentes:

- **REG (`packages/platform/registry/promotion/ratification.go`)**: `RatifierStore` é a *allowlist* de chaves públicas; `CanonicalRatification` é a mensagem canónica *length-prefixed* (domínio versionado `aos.registry.ratification.v1` à cabeça) que amarra `(ratifierID, id, versão, digest)`. `Verify` é fail-closed e amarra a ratificação ao **digest exacto** revisto — impede ratificar uma versão e activar conteúdo diferente.
- **Gate de AOS-096 (`hitl/ratification.go`)**: `RatificationGate.Ratify` compõe eval-gate + canary + `SignedApproval` verificada + selagem WORM. O **anti-transplante** é estrutural: o `RatificationID` é o SHA-256 do canónico do artefacto+eval (`SelfModArtifact.canonical`, que sela Kind, SemVer, ContentHash e a identidade do `EvaluationResult`); o `RequestID` da assinatura tem de o igualar, ou a decisão bloqueia. A **autoridade** é verificada (`DefaultRatifyAuthority = "ratify:production"`): não basta a assinatura ser autêntica, a autoridade do ratificador tem de cobrir a ratificação de produção.

O único caminho que devolve `admit=true` é: pré-condição satisfeita **e** ratificação assinada verificável de um humano autorizado, para **este** artefacto+eval, que é uma **aprovação** (não recusa) **e** a decisão foi selada. Uma recusa assinada (`Approved=false`) é também não-repúdio e é selada, mas devolve `deny`.

### 5. Promoção atómica e rollback atómico

A transição staging→active sela a **intenção** no WORM **antes** de comprometer o estado na fonte de verdade (`stagePromoteIntent` antes de `reg.SetStatus`): um audit indisponível falha **antes** da mutação — nunca fica um artefacto active sem a transição na hash-chain. O rollback (`Pipeline.Rollback`) delega o **swap atómico** no `Lifecycle` de AOS-052 (a active corrente passa a deprecated e o alvo a active sob um único lock, sem estado híbrido observável), re-verifica a integridade do alvo (a reactivação **não herda** a confiança da primeira promoção) e re-atravessa o gate composto. A revogação de emergência (`Revoke`) é terminal e bloqueia imediatamente o artefacto no Reference Monitor.

### 6. A superfície apresenta, nunca é autora do enforcement

O loop de autoria (`packages/control-plane/governance/authoring-surface/`, AOS-126) é **apresentação pura**: mostra dry-run (efeito simulado, `Committed=false` validado fail-closed), atribuição (autor/SemVer/proveniência **lida**, nunca reimplementada), o veredicto do eval-gate/canary **antes** da decisão, e **encaminha** a candidata ao gate de ratificação devolvendo o `RatificationID` que o humano assina. **Não existe caminho de `Ratify` na superfície** — o pipeline de admissão não é duplicado.

### 7. Endurecimento anti-replay (freshness + nonce durável) — invariante ratificada, wiring de produção condicional

A ratificação assinada, por si só, é uma identidade **estável de conteúdo** e portanto reutilizável N vezes (idempotência): a mesma assinatura re-valida sempre, inclusive **após** um rollback que marcou a versão como má. Esta fronteira tem de ser **endurecida** contra re-promoção. A invariante é ratificada agora; os mecanismos já existem como portas opcionais no gate de AOS-096:

- **Freshness** (`WithRatifyFreshness(ttl, skew)`): rejeita uma ratificação cujo `IssuedAt` esteja fora de `[now−ttl, now+skew]` (`ReasonRatificationStale`), limitando a janela temporal em que uma assinatura promove.
- **Uso-único por nonce durável** (`WithRatifyNonceStore`): consome atomicamente o nonce no caminho de promoção; uma reutilização vira `ReasonRatificationReplayed`. O `RatificationNonceStore` é injectável porque a **durabilidade e o âmbito** (por-processo, partilhado, com TTL) são decisões de *wiring*.

**Eixo do endurecimento (corrigido por AOS-196, achado DEF-03).** O mecanismo é **AOS-159**
(`RatificationNonceStore` durável sobre o Event Store/WORM + freshness) e está **entregue**.
O que falta é o **wiring de produção**: `NewProductionRatificationGate` — a via sancionada que
FORÇA freshness+nonce — **não tem chamador de produção** em toda a árvore (só testes), porque o
nó `aos` não compõe nenhum *promotion controller*. Esse wiring **não tem ticket no backlog**:
está registado como **POR ATRIBUIR** em `docs/governance/REGISTO-Deferimentos.md` (`DEF-401`).
**Não** é dívida da EPIC-13 (correcção de AOS-196) — a EPIC-13 é o epic de *Frontend* e nunca
teve ticket para isto; a atribuição anterior era o defeito, não o plano.

Ratifica-se a **forma e a invariante**: em produção, a ratificação de auto-modificação **deve** ser configurada com freshness **e** um nonce-store **durável** (o âmbito é o `RatificationID`, uso-único por identidade de artefacto+eval), fechando a re-promoção pós-rollback. Fica **condicional** apenas a autoridade que ativa o não-repúdio pleno — qual o IdP que faz o *pinning*/enrollment das chaves de ratificador e o binding humano↔NHI (ADR-003/AOS-005 IdentityStub) — porque depende de utilizadores e de uma organização reais que ainda não existem.

## Consequências

### Positivas

- **0 auto-modificações não avaliadas em produção** é imponível por construção: cada gate aplicável é fail-closed e a omissão de *wiring* recusa em vez de admitir.
- **Não-repúdio**: cada ratificação/recusa é uma decisão assinada, selada na hash-chain WORM tamper-evident, atribuível ao humano real (`Principal.NHIID`), re-verificável a partir do audit.
- **Determinismo reprodutível**: o harness é puro (sem I/O/relógio/rand), o mesmo candidato+golden-set produz o mesmo veredicto e o mesmo trace_id — a avaliação é auditável e repetível offline.
- **Anti-transplante e anti-troca-de-conteúdo**: a assinatura amarra o digest exacto e a identidade do eval; uma ratificação não é transferível para outro artefacto nem para outro conteúdo sob o mesmo rótulo SemVer.
- **Rollback sem estado híbrido**: o swap atómico e a re-verificação de integridade na reactivação evitam que um rollback reintroduza confiança não-revalidada.
- **Reference-model**: tudo in-process, zero-dep, offline — os gates de build/SCA/SAST cobrem 100% da superfície de admissão.

### Negativas / trade-offs

- **Fricção humana obrigatória**: nenhuma auto-modificação é totalmente autónoma; há sempre um humano no laço final. É uma escolha deliberada (a auto-modificação é o risco máximo), mas limita a cadência de evolução autónoma.
- **Idempotência por omissão é um pé-de-cabra**: sem freshness+nonce **configurados e duráveis**, a mesma ratificação re-promove indefinidamente — inclusive após rollback. A defesa existe mas é **opcional e por omissão desligada** no gate de AOS-096, e o caminho REG (`RatifierStore.Verify`) **não** modela freshness/nonce de todo. O endurecimento (§7) é, hoje, uma **obrigação de wiring** por cumprir, não um invariante já ativo em produção — registado como `DEF-401` em `docs/governance/REGISTO-Deferimentos.md`, com o mecanismo em AOS-159 (entregue) e o **wiring POR ATRIBUIR** (AOS-196 corrigiu a atribuição anterior à EPIC-13, que é o epic de Frontend).
- **Durabilidade do nonce-store limitada pela postura single-process**: um nonce-store in-memory perde o registo no restart do processo; o uso-único inter-restart fica condicional à graduação do estado para um backend durável (fora de escopo deste ADR).
- **Autoridade de identidade em falta**: o *pinning* de chaves de ratificador (`RatifierStore.Authorize`, `ApproverRegistry`) pressupõe um registador que ateste "esta chave pertence a este humano e este humano cobre `ratify:production`". Enquanto não existir um diretório de identidade real (AOS-005/IdentityStub diferido), o não-repúdio é estruturalmente correto mas operacionalmente incompleto.
- **Custo de curadoria dos golden-sets**: a garantia comportamental é tão boa quanto os golden-sets curados; um set fraco dá um verde fraco. Exige disciplina de revisão contínua.

## Alternativas consideradas

- **Deploy directo de auto-modificações com monitorização a posteriori** — rejeitado: viola frontalmente a métrica "0 auto-modificações não avaliadas em prod" e transforma *misevolution* num incidente de produção em vez de a barrar na admissão.
- **Eval-gate sem ratificação humana (auto-promoção se score alto)** — rejeitado: a auto-modificação é o risco máximo; um eval-gate pode ser satisfeito por um golden-set incompleto, e sem um humano responsável não há não-repúdio nem accountability.
- **Ratificação por assinatura de servidor / broker JIT (ADR-006)** — rejeitado para a decisão humana: o broker JIT injecta credenciais **downstream da NHI do agente** e é proibido produzir a assinatura de uma **decisão humana**. A chave de ratificação humana nunca é do sistema.
- **Idempotência pura como única semântica de replay** — mantida como default de compatibilidade, mas **insuficiente** para produção: exige-se freshness+nonce durável por cima (mecanismo em AOS-159; wiring `DEF-401`), para que uma ratificação não re-promova após rollback.
- **Golden-set não-determinista (avaliação com chamadas reais a modelos)** — rejeitado no core: quebraria a reprodutibilidade offline e a auditabilidade; o harness core depende apenas do módulo folha `otel-genai`.

## Conformidade / Enforcement

| Invariante | Onde é imposto |
|---|---|
| SemVer + validação de *bump* | `packages/platform/registry/semver/classify.go`; `Pipeline.Promote` → `semver.ValidateBump` em `packages/platform/registry/promotion/pipeline.go` |
| Eval-gate golden-set fail-closed (score ≥ limiar, zero unsafe, ambos os datasets) | `packages/platform/eval/harness.go` (`DefaultMinScore=1.0`, `EvaluateArtifact`), `goldenset.go` (`Validate`), `gateadapter/adapter.go` (`rejectScore`/`rejectRegressions`), `packages/platform/registry/promotion/evaladapter.go` e `packages/platform/memory/procedural/evaladapter.go` (`NewEvalGateFromHarness`) |
| Eval-gate obrigatório na promoção (recusa se ausente) | `Pipeline.runEvalGate` → `ErrNoEvalGate` (`promotion/pipeline.go`); `SkillMemory.RunEvalGate` → `ErrNilEvalGate` na construção (`procedural/skill_memory.go`) |
| Canary como pré-condição | `RatificationGate.Ratify` → `ReasonPreconditionFailed` (`hitl/ratification.go`) |
| Ratificação humana assinada + allowlist + digest-binding | `promotion/ratification.go` (`RatifierStore.Verify`, `CanonicalRatification`); `Pipeline.verifyRatification` |
| Anti-transplante (RequestID = RatificationID) + autoridade | `hitl/ratification.go` (`SelfModArtifact.RatificationID`, `DefaultRatifyAuthority`, `ReasonRatificationTransplant`/`ReasonRatifierUnauthorized`) |
| Anti-replay: freshness + nonce durável | `hitl/ratification.go` (`WithRatifyFreshness`, `WithRatifyNonceStore`, `RatificationNonceStore`, `ReasonRatificationStale`/`ReasonRatificationReplayed`) e `hitl/production_gate.go` (`NewProductionRatificationGate`, que os FORÇA) — **mecanismo entregue em AOS-159; wiring de produção POR CUMPRIR e sem ticket atribuído (`DEF-401`), pelo que a linha NÃO está imposta hoje** |
| Selagem WORM de cada transição/decisão (audit-before-effect) | `Pipeline.seal` (`promotion/pipeline.go`); `RatificationGate.seal`/`finish` (`hitl/ratification.go`); partição de quarentena `ratification-unratified` |
| Rollback atómico + re-verificação de integridade | `Pipeline.Rollback` → `Lifecycle.Rollback` (AOS-052); revogação terminal `Pipeline.Revoke` |
| Superfície não-autora do enforcement | `packages/control-plane/governance/authoring-surface/doc.go` (sem caminho de `Ratify`; `ErrEffectCommitted` fail-closed) |

## Referências

- Catálogo de ADRs: `_BRIEF.md` (linha 81), `specs/00_System_Spec.md` (linha 255)
- Métrica de admission control: `_BRIEF.md` (linha 100), `specs/00_System_Spec.md` (linha 183)
- EPIC-11 eval harness: `packages/platform/eval/` (`doc.go`, `harness.go`, `goldenset.go`, `gateadapter/adapter.go`) — AOS-114/115
- EPIC-05 pipeline de promoção: `packages/platform/registry/promotion/` (`pipeline.go`, `evalgate.go`, `ratification.go`); SemVer em `packages/platform/registry/semver/`
- EPIC-09 gate de ratificação: `packages/control-plane/governance/hitl/ratification.go` — AOS-096
- EPIC-12 loop de autoria: `packages/control-plane/governance/authoring-surface/` — AOS-126
- ADRs relacionados: ADR-006 (Credential Broker JIT — a chave humana nunca é do sistema), ADR-010 (audit WORM + OTel GenAI), ADR-011 (policy-as-code/GDPR), ADR-013 (gates de risco SA-ROC), ADR-003/AOS-005 (autoridade de identidade — condicional)
