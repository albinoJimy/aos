# Auditoria de completude + revisão adversarial — AOS-388

**Alvo:** o diff AOS-388 na mainline `feature/AOS-128-ux-dx-tests` (commits `f17dc4b`, `66f0eb4`,
`e03d4d9`, `b75f981`). **Método:** auditoria de completude contra os critérios de aceitação
(EPIC-19) + revisão adversarial independente (subagente que só viu diff+requisitos), com cada
achado re-verificado no código pelo relator. Read-only — nada foi implementado.

---

## 0. Achado decisivo — o deploy não carrega este trabalho

O artefacto deployado é o nó **`cmd/aos`** (imagem `aos-node`). Por ADR-018, o guard
`packages/cmd/aos/boundary_orq_sch_test.go` proíbe `cmd/aos` de importar `control-plane/orchestrator/*`.

**Evidência:** `go -C packages/cmd/aos list -deps ./...` não contém `orchestrator/planner`,
`orchestrator/decompose` nem `cmd/aos-orq`. O `aos-orq` (que carrega todo o wiring AOS-388) **não
consta** dos serviços do `deploy/server/docker-compose.prod.yml` (`aos, edge, gvisor, litellm,
otel, idp, idp-db, vault, vault-unseal`).

**Consequência:** nada do AOS-388 — nem a fix `b75f981` ao `planner.go` — chega ao nó em produção.
Uma tag de release reconstrói uma imagem funcionalmente idêntica. **Não há nada de novo para
deployar a partir deste trabalho.**

---

## 1. Auditoria de completude (calibrada contra o registo de deferimentos)

| # | Critério de aceitação (EPIC-19 §AOS-388) | Estado | Evidência / enquadramento |
|---|---|---|---|
| 1 | `LLMDecomposer` satisfaz porta; `plan.Decode` fail-closed; carimba `planner_meta`; untrusted | ✅ Cumprido | `decompose.go:146-163,206` |
| 2 | Camadas: porta de modelo local; `layer-lint` verde sem baseline nova | ✅ Cumprido | `decompose.go:40`; layer-lint verde |
| 3 | `--goal` end-to-end: Decompose → Validate → **PlanGate.Approve** → Materialize, com **nós-folha E papéis-que-expandem** via Delegator.Spawn | ⚠️ **Parcial + DEFEITO (ver §2.1)** | Delegator real composto (`planner_wiring.go:184`), mas o ramo papéis-que-expandem está **não-funcional**; `PlanGate.Approve` deferido (**DEF-274**, eixo AOS-238) |
| 4 | Determinismo de teste: modelo injectado; fake determinístico; retry no Planner | ✅ Cumprido | `fixtureModel`; `decompose.go:20-22` |
| 5 | Produção fail-closed: exige credencial de modelo (espelha `ErrProductionNeedsModelCredential`) | ⛔ Não cumprido (adjacente a T2-B) | ver §2.3 |
| 6 | Guard `boundary_orq_sch_test.go` verde (nada entra em `cmd/aos`) | ✅ Cumprido | §0 |

**Calibração:** o **modelo LLM real** está deferido para **AOS-391** (T2-B, ticket próprio) e o
**gate humano** para **DEF-274** — ambos registados; caixas por marcar aí **não** são dívida
defeituosa. **MAS** o AC 3 tem um defeito real e não-declarado (§2.1), distinto dessas deferrals.

---

## 2. Achados da revisão adversarial (verificados no código)

### 2.1 [ALTO — CONFIRMADO] O ramo "papéis-que-expandem via Delegator.Spawn" é estruturalmente não-funcional: o RM do wiring não regista `agent.spawn`

> **RESOLVIDO por AOS-393 (2026-09-10).** O sintoma confirmou-se, mas a causa raiz foi **corrigida por execução do binário** para `depth_mismatch` (a `RoleSpawn` não declarava profundidade), sendo o `agent.spawn` não-registado um segundo defeito latente. O fix exigiu **quatro** elementos (profundidade + `agent.spawn` + classe `worker` + autoridade sobre tools). Ver a §Estado de AOS-388 e a §Resolução de AOS-393 em `specs/EPIC-19`.

- **Onde:** `packages/cmd/aos-orq/planner_wiring.go:129-132` (o `mon` só regista `agent.plan`) e
  `:184` (`orchestrator.NewDelegator(bud, mon, iss)` sem opções).
- **Cadeia confirmada:**
  1. `NewDelegator` default → `spawnToolID="agent.spawn"` (`delegation.go:277-278`), mediado pelo
     `mon` injectado (`delegation.go:455-459`).
  2. `mon := rm.New()` regista **só** `agent.plan`; `agent.spawn` fica **default-deny**
     (`reference-monitor/monitor.go:408` → `EffectDeny`).
  3. `DefaultClassifier` classifica um nó com ≥1 dependente como `SpawnRole`
     (`planmaterialize/materialize.go:113-121`); `SpawnRole` → `Delegator.Spawn`
     (`materialize.go:417`).
- **Cenário de falha:** um `PlanDocument` multi-nó com expansão (ex.: `analise depends_on:[recolha]`)
  → `recolha` classificado `SpawnRole` → `Delegator.Spawn` medeia `agent.spawn` → **Deny** →
  `ErrSpawnMediationDenied` → `Materialize` **aborta**. O plano com expansão **nunca materializa**
  por este binário.
- **Contradição:** o comentário `planner_wiring.go:182-183` afirma que "um papel-que-expande **passa
  a criar sub-agentes** (AOS-026)" — mas os "mesmos colaboradores" (o `mon` só com `agent.plan`)
  negam-no. Contradiz o AC 3.
- **Não está deferido:** DEF-803 fecha para AOS-388; nada regista este buraco. É **defeito real
  aterrado na mainline**, não âmbito diferido.
- **Correcção mínima (não aplicada):** registar `agent.spawn` no `mon` (espelhando o `agent.plan`),
  ou `NewDelegator(..., WithSpawnCapability("agent.plan", "cap:agent.plan"))`.

### 2.2 [MÉDIO — CONFIRMADO] O único e2e mascara o defeito acima com uma fixture só-folhas

- **Onde:** `packages/cmd/aos-orq/planner_wiring_test.go:27-43` — `planoFixtureDuasFolhas` tem os
  dois nós com `depends_on:[]` ("duas folhas INDEPENDENTES").
- **Porquê passa pela razão errada:** ambos → `SpawnLeaf`; `Delegator.Spawn` **nunca é invocado**;
  não há asserção sobre linhas `spawn: no=…`. O AC pede "nós-folha **E** papéis-que-expandem"; só o
  primeiro é coberto. O ramo partido (§2.1) fica invisível ao teste verde.
- **Correcção:** um caso e2e com um nó de expansão (`depends_on` não-vazio) que asserte a
  materialização do spawn — reproduz o defeito §2.1 (falha-antes) e prova a correcção.

### 2.3 [BAIXO/MÉDIO — CONFIRMADO] AC "produção fail-closed exige credencial de modelo" não implementado como descrito; comentário invoca um banner inexistente

- **Onde:** `packages/cmd/aos-orq/planner_wiring.go:73-82`. O `aos-orq` **não detecta produção**;
  substitui o AC por "exige sempre `--decompose-fixture`". O comentário `:76` invoca "o banner de
  postura que existe para evitar" — mas **esse banner não existe neste binário** (grep confirma zero
  postura/env em `aos-orq`).
- **Enquadramento:** o caminho vivo (Model Gateway) é **T2-B / AOS-391**, fora de âmbito. O
  `--goal` sem gateway recusa fail-closed (genuíno). Mitigação real: o documento é untrusted, passa
  por `planvalidate.Validate` contra o snapshot pinado, e a proveniência é carimbada pelo Decomposer
  (não forjável — ver §3). Resíduo honesto adjacente a AOS-391, mas o comentário afirma um mecanismo
  inexistente e o AC 5 fica por cumprir.

### 2.4 [BAIXO — pré-existente, FORA de âmbito] Commit falhado no Planner não liberta a reserva

- **Onde:** `packages/control-plane/orchestrator/planner/planner.go`, bloco final `reserver.Commit`
  — em erro de `Commit`, retorna sem `Release(res)`: a reserva fica retida (nem consolidada nem
  devolvida). Fail-closed quanto a fundos, mas inconsistente com os outros caminhos de erro (todos
  fazem Release). **Não é deste ticket** (código pré-AOS-388). Candidato a ticket próprio.

---

## 3. Hipóteses que MORRERAM contra o código (calibração — nada a reportar)

- **Proveniência forjável pelo modelo:** não. `decompose.go:158-162` sobrescreve `PlannerMeta` com
  valores autoritativos após `plan.Decode`; `capabilities_hash` carimbado = `snap.Hash` = o snapshot
  que `planvalidate` usa. Testes `TestDecompose_ProveniencaForjada*` não-vacuosos.
- **`extractJSON` aceita lixo:** não. Primeiro-`{`→último-`}` + `plan.Decode` estrito
  (`DisallowUnknownFields`, trailing-data). Lixo ⇒ erro fail-closed.
- **Fail-open no fail-closed de goal/capHash:** não. Validado cedo em `decompose.go:129-136` **e**
  `planner.go` (a fix `b75f981`), antes de orçamento/mediação; TrimSpace; testes de só-espaços.
- **Cadeia de delegação não termina no humano:** termina. `runTok` com `UserID:"human:"+worker`
  (`planner_wiring.go:117-122`); `IssueChild` on-behalf-of.
- **Retry duplicado / leak no Decomposer:** não há retry no Decomposer (é do Planner); sem estado.

---

## 4. O que fica para humano / servidor (não resolúvel daqui)

- **DoD "revisão por 2 revisores":** não cumprida (merge entrou com 0 reviews). Esta auditoria +
  revisão adversarial é evidência P0-adjacente, **não** substitui o sign-off humano.
- **Verificação runtime do gateway de modelo:** irrelevante para o deploy (§0) e pendente de AOS-391.

---

## 5. Recomendação

1. **Deploy:** não deployar "por causa deste trabalho" — é no-op para o nó (§0). Se houver outra
   razão para uma release, ela é independente do AOS-388.
2. **AOS-388 (qualidade):** corrigir o **§2.1 (ALTO)** — trivial: registar `agent.spawn` no `mon` —
   **com** um e2e que exercite o ramo `SpawnRole` (§2.2). Sem isto, o objectivo central do ticket
   (goal→DAG multi-nó com expansão) está partido na mainline por baixo de um teste verde.
3. **§2.3:** corrigir o comentário enganador e decidir se o AC 5 fica formalmente atribuído a AOS-391.
4. **§2.4:** abrir ticket próprio (fora de AOS-388).
