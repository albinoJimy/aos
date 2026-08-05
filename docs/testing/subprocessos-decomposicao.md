# Decomposição de subprocessos do nó `aos` (diagramas)

Este documento decompõe o **fluxo completo da app** nos seus **subprocessos internos**, derivado da
telemetria REAL (spans OTLP, eventos do Event Store, partições WORM) de um run de referência. É o
companheiro do [roteiro do ciclo de vida](ciclo-de-vida-manual.md): aquele mostra as fases
principais; este mostra o que corre *dentro* de cada uma.

> Fonte: run `run-app-195330` (Kimi via LiteLLM), stack `deploy/node/dev-hardened`. Contagens de
> spans/eventos observadas ao vivo.

---

## 0. Dois planos — porque o planeador NÃO aparece no trace de runtime

O AOS separa **plano de DADOS** de **plano de CONTROLO**. A telemetria de runtime (secções 1–4)
mostra só o **plano de dados** — o loop que o nó implantado (`cmd/aos`) corre: objectivo → modelo
propõe tool calls → mediação. O **Planeador/Meta-Orquestração (ORQ) + Escalonador (SCH)** são o
**plano de controlo**: decompõem um objectivo num plano validado (DAG) e delegam a sub-agentes.

```mermaid
flowchart TB
  OBJ(["objectivo"]) --> INT["INTAKE<br/>classify · non-bypass gate<br/><i>routing, NÃO autoridade</i>"]
  INT --> PLN["PLANNER<br/>LLM produz o plano<br/><i>plano = UNTRUSTED</i>"]
  PLN --> VAL["PLAN-VALIDATE<br/>valida sobre snapshot PINADO<br/>risco DERIVADO, não auto-declarado"]
  VAL -->|inválido| REJ["rejeitado fail-closed"]
  VAL -->|válido| DOC["PLAN-DOCUMENT<br/>versionado semver · DAG acíclico<br/>deadlock-check (AOS-025)"]
  DOC --> DEL["DELEGAÇÃO<br/>sub-agentes · orçamento herdado (CAS, AOS-026)"]
  DEL --> DISP["DISPATCH<br/>emite ControlEvents (AOS-009)"]
  DISP --> SCH["SCHEDULER (SCH)<br/>consome os eventos · escalona"]
  SCH -->|cada tarefa escalonada torna-se| RUN["um run do PLANO DE DADOS<br/>invoke_agent → chat → execute_tool<br/><i>(as secções 1–4)</i>"]

  classDef ctrl fill:#fff8e0,stroke:#bb9a40;
  classDef data fill:#e8f0ff,stroke:#4062bb;
  classDef rej fill:#ffe8e8,stroke:#bb4040;
  class INT,PLN,VAL,DOC,DEL,DISP,SCH ctrl; class RUN data; class REJ rej;
```

> **ESTADO (honesto):** este plano de controlo está **construído e RATIFICADO** (spec `tecnica/18`,
> código em `packages/control-plane/orchestrator/`, **módulo próprio**), mas **NÃO está ligado a
> nenhum binário** — nem `cmd/aos` (o nó) nem `cmd/aos-demo` o importam. Por isso **não emite spans
> no runtime**: o nó implantado corre o loop de dados; o planeador/orquestrador é maquinaria de
> plano de controlo ainda não integrada no caminho de execução. Quando ligado, **cada tarefa que o
> SCH escalona torna-se um run do plano de dados** — ou seja, a árvore da secção 1 passa a ser uma
> *folha* deste grafo.

---

## 1. Árvore de subprocessos em runtime (spans OTLP) — plano de DADOS

Todo o run vive sob **um** span `invoke_agent`; cada turno e cada tool call desdobram-se em
subprocessos aninhados. Nada corre sem deixar um span.

```mermaid
flowchart TD
  IA["invoke_agent<br/><i>o run — raiz do trace</i>"]
  IA -->|1 por turno| CHAT["chat &nbsp;[step-N]<br/><i>modelo · LiteLLM → Kimi</i>"]
  IA -->|1 por tool call| ACT["aos.activity &nbsp;[step-N-tool-1]<br/><i>despacho DURÁVEL · AOS-021 (idempotência/replay)</i>"]
  ACT --> EXE["execute_tool<br/><i>MEDIAÇÃO · ponto único · ADR-002</i>"]
  EXE --> SEAL["audit_seal<br/><i>selagem WORM · hash-chain tamper-evident</i>"]

  classDef model fill:#e8f0ff,stroke:#4062bb;
  classDef med fill:#fff0e8,stroke:#bb5a40;
  classDef audit fill:#e8ffe8,stroke:#40bb62;
  class CHAT model; class ACT,EXE med; class SEAL audit;
```

---

## 2. Dentro de `execute_tool` — a cadeia de hooks fail-closed

O `execute_tool` não é atómico: corre uma cadeia de gates onde **cada um pode NEGAR** (o atributo
`aos.decision.denied_by` diz qual). Uma tool call proposta pelo modelo entra com `taint=untrusted`.

```mermaid
flowchart TD
  T(["tool call proposta pelo modelo<br/>taint = untrusted"]) --> H1{"1 · IDENTITY<br/>NHI vs trust-anchor"}
  H1 -->|assinatura inválida| DI["DENY<br/>denied_by = identity"]
  H1 -->|ok| H2{"2 · REVALIDATION<br/>recomputa digest + verifica<br/>assinatura vs frozen set"}
  H2 -->|não bate| DR["DENY<br/>denied_by = revalidation"]
  H2 -->|ok| H3{"3 · PDP / Cedar<br/>allowlist de capability por classe (AOS-007)<br/>+ avaliação sob taint (P4)"}
  H3 -->|não-allowlisted OU<br/>untrusted comanda privilegiada| DP["DENY<br/>denied_by = policy"]
  H3 -->|ok| H4{"4 · SCOPE<br/>escopo do recurso"}
  H4 -->|fora do escopo| DS["DENY<br/>denied_by = scope"]
  H4 -->|ok| P(["PERMIT<br/>+ obligations: audit · redact_pii"])

  classDef deny fill:#ffe8e8,stroke:#bb4040;
  classDef permit fill:#e8ffe8,stroke:#40bb62;
  class DI,DR,DP,DS deny; class P permit;
```

No run de referência, `web_post` (cap:http.post) morre em **3 · PDP** com `denied_by=policy`: a
authority contém a capability e a região é `eu`, mas o `taint=untrusted` faz o gate P4 negar
("untrusted não comanda").

---

## 3. Os quatro planos que correm em paralelo

O plano de DADOS é o fluxo "feliz"; três planos de subprocessos correm ao lado e são o que separa
um agente comum de um **OS agêntico governado**.

```mermaid
flowchart TB
  subgraph DATA["Plano de DADOS — o fluxo principal"]
    direction TB
    S["Submissão<br/>Bearer + NHI → 201"] --> FZ["Congelar catálogo assinado<br/>registry.freeze_toolset"]
    FZ --> LOOP["Loop de turnos"]
    LOOP --> CH["chat · modelo"]
    CH --> MED["execute_tool · 5 hooks"]
    MED --> RD["Leitura soberana · trajetória · reconstrução"]
    RD --> SH["Crypto-shred · DSAR /erase"]
  end

  subgraph DUR["Plano DURÁVEL — por turno"]
    direction LR
    CK["step.checkpoint"] --> CAP["replay.captured<br/><i>não-determinismo</i>"] --> TR["turn.recorded"] --> MEM["memory.record.written"]
  end

  subgraph GOV["Plano de GOVERNANÇA"]
    direction LR
    GR["gov.read/run-id<br/><i>leitura soberana auditada</i>"] --- GS["gov.sovereignty.authority<br/><i>rotação board→região</i>"]
  end

  subgraph SVC["Plano de SERVIÇO"]
    LE["lease.claimed / renewed<br/><i>heartbeat AOS-164</i>"]
  end

  MED -. "sela decisão" .-> GR
  MED -. "grava estado" .-> CK
  RD -. "audita leitura" .-> GR
  LOOP -. "mantém vivo" .-> LE

  classDef data fill:#e8f0ff,stroke:#4062bb;
  classDef dur fill:#f3e8ff,stroke:#7a40bb;
  classDef gov fill:#fff0e8,stroke:#bb5a40;
  classDef svc fill:#e8ffe8,stroke:#40bb62;
  class S,FZ,LOOP,CH,MED,RD,SH data; class CK,CAP,TR,MEM dur; class GR,GS gov; class LE svc;
```

---

## 4. Mapa fase → subprocessos (com evidência)

| Fase principal | Subprocessos internos | Evidência (span / evento / partição) |
|---|---|---|
| **Submissão** | verificar Bearer (JWKS · RS256 · aud/exp · **anti-replay jti**) · admissão (token-bucket) · **selar residência** · `run.created` · lease | `lease.claimed` |
| **Arranque do run** | **congelar** catálogo assinado · manifesto | span `registry.freeze_toolset` · evento `run.toolset.frozen` |
| **Cada turno** | montar prompt · **`chat`** (modelo) · parsear resposta · enriquecer tool calls (capability do registry) | span `chat` |
| **Cada tool call** | `aos.activity` (despacho durável) → `execute_tool` (5 hooks) → `audit_seal` | spans `aos.activity` · `execute_tool` · `audit_seal` |
| **Fim de turno (durável)** | checkpoint · capturar não-determinismo · gravar turno · escrever memória | `step.checkpoint` · `replay.captured` · `turn.recorded` · `memory.record.written` · span `memory.put` |
| **Ingestão** | **redacção** de conteúdo untrusted | span `aos.ingest.redacted` |
| **Leitura soberana** | verificar OIDC do leitor · **região leitor == run** · selar leitura no WORM (D6) | partição `gov.read/run-<id>` |
| **Trajetória / reconstrução** | replay do Event Store (SSE) · desembrulhar KEK · decifrar | `replay.captured` · `GET /reconstruct` |
| **Crypto-shred** | índice partição-por-titular · **re-check de legal-hold** (TOCTOU) · `deletion_allowed`+DELETE da KEK (Transit/TLS) · **verificar WORM pós-shred** | KEK destruída no Vault |
| **Soberania (background)** | rotação/auditoria da fonte board→região | partição `gov.sovereignty.authority` |
| **Serviço (background)** | lease TTL + heartbeat (AOS-164) | `lease.claimed` / `lease.renewed` |

---

*Derivado da telemetria de `run-app-195330`. Os diagramas renderizam no GitHub e em qualquer viewer
com suporte Mermaid.*
