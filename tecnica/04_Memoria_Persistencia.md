# Memória e Persistência — AOS

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Memória e Persistência — Memory Service, Event Store e proveniência |
| Versão | 1.0 |
| Data | Julho de 2026 |
| Classificação | Documento de Referência — Aberto |
| Documento-fonte | `_FONTE_agentic-os-ideal.md` |
| Documentos relacionados | `tecnica/02_Agent_Runtime_Execucao_Duravel.md`, `tecnica/08_Observabilidade_Evals.md`, `specs/EPIC-04_Memoria_Persistencia.md` |

---

## 1. Introdução

### 1.1 Propósito

Este documento especifica a camada de **memória e persistência** do AOS — Agentic OS de Referência. Cobre o **Memory Service (MEM)** e os seus quatro tipos de memória (episódica, semântica, procedural e de trabalho); o princípio fundacional **contexto ≠ registo**, que separa a projecção injectada no modelo da trajectória persistida no backend; o **Event Store (ES)** replicado append-only como fonte de verdade; o versionamento de schema de memória com migrações **expand/contract**; e a **proveniência e quarentena** de memória derivada de conteúdo untrusted, mitigando *memory poisoning*.

A tese é directa: a memória de um agente não é um cache conveniente — é estado durável com consequências de segurança, conformidade e reprodutibilidade. O que o modelo vê e o que o sistema regista são projecções distintas do mesmo substrato, e confundi-las é a raiz de duas falhas graves: perder o audit trail em nome da higiene de contexto, ou envenenar o planeamento com dados que nunca deveriam comandar.

### 1.2 Âmbito

Abrange: a taxonomia de memória e as suas fronteiras de escrita/leitura; a materialização do princípio contexto ≠ registo; a arquitectura do Event Store replicado e as suas garantias de durabilidade (substituindo SQLite single-writer, ADR-007); a disciplina de versionamento de schema com migrações expand/contract (ADR-012); e o modelo de proveniência e quarentena de memória untrusted (ADR-005). Ficam fora do âmbito o loop de execução durável (ver `tecnica/02`), a captura de trajectória em spans OTel (ver `tecnica/08`) e o backlog executável (ver `specs/EPIC-04`).

### 1.3 Audiência

Engenheiros de Dados/Memória, engenheiros de runtime, engenheiros de segurança e de governação, e arquitectos de plataforma que precisem de compreender como o estado do agente é escrito, projectado, versionado e protegido.

### 1.4 Definições e termos

- **Memory Service (MEM):** serviço de plataforma que gere os quatro tipos de memória sobre o Event Store, aplicando proveniência e projecção.
- **Event Store (ES):** log append-only replicado, com transporte push (NATS/Redis/Postgres), que é a fonte de verdade de toda a trajectória.
- **Contexto ≠ registo:** princípio segundo o qual a projecção injectada no modelo é distinta da trajectória persistida no backend.
- **Expand/contract:** padrão de migração de schema em duas fases não-destrutivas (expandir e depois contrair) que evita janelas de indisponibilidade e permite rollback.
- **Memory poisoning:** contaminação persistente da memória por conteúdo untrusted que, se lido como instrução, desvia o comportamento futuro do agente (ASI06).

---

## 2. ADRs aplicáveis

O desenho desta camada é governado por três decisões de arquitectura canónicas:

- **ADR-007 — Event Store replicado.** Substitui o SQLite single-writer (SPOF e tecto de throughput) por um log replicado append-only com transporte push. Todo o estado de memória materializa-se a partir deste log, que é a fonte de verdade.
- **ADR-005 — Separação control/data-plane + taint (quarentena).** Conteúdo untrusted — tool results, web, memória derivada, schemas MCP — é dados, nunca instruções. Memória derivada de fontes não-confiáveis é marcada com proveniência e posta em quarentena, mitigando prompt injection (OWASP LLM01) e memory poisoning (ASI06).
- **ADR-012 — SemVer + eval-gate para auto-modificação.** O schema de memória é um artefacto comportamental mutável e versionado; as suas migrações seguem o padrão expand/contract com rollback atómico, e a memória procedural auto-escrita passa por eval-gate antes de produção.

Aplicam-se ainda, transversalmente, o ADR-001 (execução durável — a memória de trabalho materializa-se do log por replay) e o ADR-010 (observabilidade — a projecção ao pai e a árvore de spans completa são o corolário directo de contexto ≠ registo).

---

## 3. Taxonomia de memória

O Memory Service organiza-se em quatro tipos, cada um com um horizonte temporal, uma fronteira de escrita e uma política de leitura distintos. Tratá-los como um único "armazém de contexto" é o erro de base dos frameworks ingénuos.

```mermaid
flowchart TD
    ES["Event Store replicado (fonte de verdade)"]
    subgraph MEM["Memory Service (MEM)"]
        WORK["Memoria de trabalho: estado do run activo, materializado por replay do log"]
        EPI["Memoria episodica: trajectorias passadas, o que aconteceu e quando"]
        SEM["Memoria semantica: factos e conhecimento consolidado, indexado por embedding"]
        PROC["Memoria procedural: skills e heuristicas auto-escritas, versionadas SemVer"]
    end
    ES --> WORK
    ES --> EPI
    EPI -->|consolidacao curada| SEM
    EPI -->|extraccao de padroes| PROC
    WORK -->|projeccao higienizada| MODEL["Contexto injectado no modelo"]
    SEM -->|recuperacao top-k| MODEL
    PROC -->|carregamento de skill| MODEL
```

- **Memória de trabalho.** É o estado volátil do run activo: o histórico do turno, os resultados de tools pendentes, os *scratchpads*. Não é uma estrutura em RAM que um crash apaga — materializa-se por *replay* do Event Store (ADR-001), pelo que sobrevive a falhas e é reproduzível *resume-from-step*.
- **Memória episódica.** Regista trajectórias passadas — o que o agente fez, quando, com que resultado. É a matéria-prima do replay determinístico, da análise de causa-raiz (RCA) e do *eval-driven development*. Nunca é descartada do registo.
- **Memória semântica.** Conhecimento consolidado e factos, indexados por *embedding* para recuperação por semelhança (top-k). Deriva de episódios curados, não de captura bruta.
- **Memória procedural.** Skills e heurísticas que o próprio agente escreve. É a mudança de maior risco do sistema (*misevolution*) e por isso é versionada com SemVer e sujeita a eval-gate + canário + ratificação assinada (ADR-012) antes de chegar a produção.

A regra de fluxo é unidireccional e curada: o episódico alimenta o semântico e o procedural por consolidação explícita, nunca por absorção automática de tudo o que passa pelo contexto.

---

## 4. Contexto ≠ registo

O princípio mais consequente desta camada resolve a maior contradição dos frameworks de agentes: a higiene de contexto (injectar pouco, barato, limpo) colide com a observabilidade (persistir tudo, para debug, eval e conformidade). O AOS recusa escolher — **desacopla os dois eixos**.

A **projecção** é o que se injecta no modelo: uma vista comprimida, higienizada, cache-estável, optimizada para tokens. O **registo** é a trajectória completa persistida no Event Store: cada turno, cada resultado de tool, cada decisão, com proveniência e hash. Descartar da injecção é legítimo — é economia. Descartar do audit trail nunca é — é destruição de evidência.

```mermaid
flowchart LR
    RUN["Run do agente / sub-agente"]
    RUN -->|resumo 1-2k tokens| PROJ["Projeccao: contexto injectado no modelo"]
    RUN -->|trajectoria completa append-only| REC["Registo: Event Store (fonte de verdade)"]
    PROJ --> MODEL["Modelo LLM: higiene, cache-estavel, menos custo"]
    REC --> REPLAY["Replay deterministico resume-from-step"]
    REC --> RCA["RCA e debug drill-down"]
    REC --> EVAL["Eval-driven development"]
    REC --> AUDIT["Audit WORM hash-chained"]
```

Esta separação tem três corolários operacionais. Primeiro, um **sub-agente devolve ao pai um resumo de 1–2k tokens**, mas a sua árvore de spans completa vive sempre no backend (ver `tecnica/08`) — o pai poupa custo sem que o auditor perca nada. Segundo, a **compressão/sumarização** da memória de trabalho corre apenas em *checkpoints assíncronos*, fora da hot path, para não destruir a estabilidade de cache do prefixo de prompt (ADR-009). Terceiro, o **manifesto por trajectória** (hash do prompt materializado, model-id, versões de skills/tools/memória) é gravado por turno, tornando o replay fiel mesmo após evolução de código.

### 4.1 Barreira arquitectural (AOS-036)

O Princípio 4 materializa-se em **duas vias fisicamente separadas** no módulo `platform/memory`, que **nunca partilham o caminho de descarte**:

1. **`project_context(record) → injected_view`** (pacote `projection`) — produz o que o modelo vê: um resumo higienizado e **limitado em tokens** (alvo ~1–2k, configurável pela política). Descartar/truncar aqui é economia legítima.
2. **`persist(record) → event`** (pacote `record`) — persiste **sempre** a trajectória completa (cada turno com o conteúdo cru e o manifesto por turno, mais a árvore de spans completa) no backend. Descartar do registo **nunca** é legítimo — não existe operação que o permita.

A barreira é imposta a **nível de tipo**, não por convenção. A projecção recebe uma vista *read-only* do registo (`record.RecordView`), cujo conjunto de métodos **não inclui qualquer mutador** (não há `AppendTurn`, `AppendSpan`, `Delete` nem `Persist`). Consequências verificadas por teste:

- `view.AppendTurn(...)` ou `view.Delete(...)` **não compilam** (erro de tipo) — a projecção não tem acesso de escrita ao registo;
- `record.View()` devolve um wrapper de **tipo não exportado**, e o registo mutável (`*TrajectoryRecord`) **nem sequer implementa** `RecordView` (falta-lhe `TurnSummaries`/`isRecordView`) — pelo que uma fuga por `type-assertion` da vista para o registo é uma **impossibilidade de compilação** (`go vet: impossible type assertion`).

O **manifesto por turno** (hash do prompt materializado, model-id/params, `assembly_version`, `manifest_schema_version`) é gravado no registo **independentemente** do que a projecção higienizou — reutilizando o conceito do `TurnRecorder`/`PromptAssembler` do Agent Runtime (AOS-013/016, ADR-010). A **política de projecção é versionada em SemVer**: a mesma trajectória sob a mesma política produz a mesma injecção **byte-a-byte** (determinística — sem `time.Now`/`rand`). O resumo ao pai liga-se à trajectória completa no backend pelo `trace_id`.

```mermaid
flowchart LR
    REC["TrajectoryRecord (append-only)\nturnos: cru + manifesto | arvore de spans"]

    REC -->|record.View → RecordView\nREAD-ONLY, sem mutadores| PROJ
    REC -->|Persist: registo concreto| PERS

    subgraph VIA1["Via 1 — projeccao (contexto)"]
        PROJ["project_context(view, policy)"]
        PROJ -->|resumo higienizado ≤ orcamento tokens\npolitica SemVer, determinista| INJ["InjectedView → contexto do pai"]
    end

    subgraph VIA2["Via 2 — persist (registo)"]
        PERS["persist(record) → event"]
        PERS -->|trajectoria COMPLETA\ncru + manifesto por turno + spans| BK["Backend OBS / Event Store\n(fonte de verdade, EPIC-08)"]
    end

    INJ -. ligado por trace_id .-> BK

    classDef barrier stroke-dasharray: 5 5;
    class PROJ barrier;
```

A leitura do diagrama: as duas vias partem do mesmo registo mas por **portas distintas** — a projecção só alcança a vista read-only (a tracejado, sem escrita), enquanto a persist opera sobre o registo concreto e emite tudo. A contagem de spans no backend é sempre estritamente maior do que a vista injectada (provado por teste): o pai poupa tokens, o auditor não perde nada.

---

## 5. Event Store e durabilidade

O plano-base ingénuo usava SQLite single-writer como event bus. Isso é simultaneamente um ponto único de falha (SPOF) e um tecto de throughput: um só escritor, um só host, sem replicação. O ADR-007 substitui-o por um **Event Store replicado, append-only, com transporte push**.

```mermaid
flowchart LR
    subgraph W["Workers stateless"]
        W1["Worker 1"]
        W2["Worker N"]
    end
    subgraph ESR["Event Store replicado (append-only)"]
        L["Log ordenado por particao"]
        R1["Replica 1"]
        R2["Replica 2"]
    end
    W1 -->|append evento idempotente| L
    W2 -->|append evento idempotente| L
    L --> R1
    L --> R2
    L -->|transporte push NATS/Redis/Postgres| SUB["Subscritores: MEM, OBS, PDP"]
    L --> WORM["Audit WORM hash-chained"]
    R1 -.failover.-> L
```

As garantias são quatro. **Fonte de verdade única:** todo o estado de memória — trabalho, episódico, e as vistas materializadas semânticas — é derivável por replay do log; não há estado autoritativo escondido em RAM. **Append-only com idempotência:** cada evento carrega a idempotency key = f(run_id, step_id) (ADR-001), pelo que um retry após crash não duplica efeitos nem entradas de memória. **Replicação sem SPOF:** o log é replicado, os workers são *stateless*, e o failover não perde o commit — servindo o alvo de disponibilidade de 99,9% do plano de controlo. **Transporte push:** subscritores (o próprio MEM, a observabilidade, o PDP) recebem eventos *event-driven*, sem *polling*, reduzindo latência de propagação.

O Event Store é também a fronteira onde o audit se separa dos diagnósticos efémeros: o log alimenta um audit **WORM hash-chained** (ADR-010), *tamper-evident*, distinto dos dados operacionais que se auto-limpam. "Imutável" significa aqui *tamper-evidence do registo*, não retenção eterna do payload — a reconciliação com o direito ao apagamento faz-se por crypto-shredding, tratado em `tecnica/09`.

---

## 6. Migrações expand/contract

O schema de memória é um artefacto comportamental mutável e, como tal, é versionado com SemVer (ADR-012) e ancorado a um contrato público. A evolução do schema — novos campos, reindexação, mudança de formato de *embedding* — não pode exigir *stop-the-world* nem impedir o rollback. O AOS adopta o padrão **expand/contract** (também dito *parallel-change*), em duas fases não-destrutivas.

```mermaid
flowchart TD
    V1["Schema v1 em producao"] --> EXP["Fase EXPAND: adiciona campos/indices novos, escreve em ambos, le v1"]
    EXP --> DUAL["Dual-write v1+v2, backfill assincrono do historico"]
    DUAL --> SWITCH["Switch de leitura para v2, validado por eval-gate"]
    SWITCH --> CON["Fase CONTRACT: remove campos/indices v1 legados"]
    CON --> V2["Schema v2 em producao"]
    SWITCH -.regressao detectada.-> RB["Rollback atomico: volta a ler v1"]
    RB --> EXP
```

Na fase **expand**, o novo schema é aditivo: adicionam-se campos e índices sem remover nada. O MEM escreve em ambos os formatos (*dual-write*) e um *backfill* assíncrono migra o histórico do Event Store, fora da hot path. Enquanto ambos coexistem, a leitura continua sobre v1, pelo que nenhuma incompatibilidade é exposta. O *switch* de leitura para v2 só ocorre depois de o backfill completar e de um **eval-gate** confirmar, contra golden-set, que a nova projecção não introduz regressão. Só na fase **contract**, já com v2 estável, se removem os campos e índices legados.

Como as duas fases são não-destrutivas até ao contract, o **rollback é atómico**: perante regressão detectada em canário, o MEM volta a ler v1 sem perda de dados, porque a escrita dupla nunca parou. Isto materializa, para a memória, a mesma disciplina de porta versionada que o Model Gateway impõe ao modelo — coerência por contrato, não por vendor único.

---

## 7. Proveniência e quarentena

A memória é um vector de ataque persistente: se conteúdo untrusted for absorvido como facto ou instrução, uma injecção de hoje contamina o planeamento de amanhã — o padrão *memory poisoning* (ASI06). A defesa não são tags in-band (`memory_context`), que são triviais de forjar, mas **taint tracking real com proveniência** (ADR-005).

Cada entrada de memória carrega metadados de proveniência: a origem (system/utilizador autenticado vs. tool result/web/schema MCP), o nível de confiança, e a cadeia de derivação. Memória derivada de fonte untrusted herda o *taint* e entra em **quarentena**: é dados, nunca instruções, e é estruturalmente impedido de autorizar uma tool call privilegiada.

```mermaid
flowchart TD
    T["Origem TRUSTED: system / utilizador autenticado"] -->|proveniencia confiavel| PLAN["Planeador: opera sobre dados confiaveis"]
    U["Origem UNTRUSTED: tool result / web / memoria derivada / schema MCP"] -->|taint + proveniencia| QUAR["Quarentena de memoria: dados, nunca instrucoes"]
    PLAN --> WRITE["Escrita em memoria semantica/procedural"]
    QUAR -.nao pode autorizar.-> ACT["Tool call privilegiada"]
    WRITE -->|consolidacao curada com revisao| SEM["Memoria semantica confiavel"]
    QUAR -->|so leitura como evidencia| SEM
```

A promoção de memória em quarentena para conhecimento confiável (semântico) exige **consolidação curada** — revisão explícita ou eval-gate — nunca absorção automática. A memória procedural auto-escrita, sendo executável, recebe o tratamento mais estrito: staging → eval-gate (golden-set + trace-diffing) → canário → ratificação humana assinada → produção com versão SemVer (ADR-012). Assim, o vector nº1 (OWASP LLM01) e o envenenamento persistente (ASI06) tornam-se *arquitecturalmente* contidos, não meramente desencorajados.

---

## 8. Modelo de domínio do Memory Service (implementação AOS-035)

O ticket AOS-035 estabelece a **fundação** executável desta camada: o modelo de
domínio das quatro classes, a porta versionada e os dois adaptadores. É sobre
este esqueleto que os tickets seguintes (projecção, janela, classes concretas,
migrações, quarentena, compressão) assentam. O que se segue documenta o que está
implementado em `packages/platform/memory` (módulo `github.com/aos-ref/platform/memory`,
Go 1.24, zero dependências externas).

### 8.1 As quatro classes como abstracções distintas

Cada classe é um tipo de **corpo tipado** (`domain.Body`) próprio — não um saco de
campos partilhado — e a `domain.MemoryClass` faz parte da identidade de todo o
registo. Um corpo colocado na classe errada é rejeitado (`ErrClassMismatch`), e
uma leitura de uma classe nunca devolve registos de outra (as operações são
escopadas por classe).

| Classe | Constante | Corpo tipado | Ciclo de vida (documentado) |
|---|---|---|---|
| Episódica | `ClassEpisodic` | `EpisodicBody` (trace_id, goal, outcome, step_count, summary) | append-only; nunca descartada do registo; TTL longo/permanente |
| Semântica | `ClassSemantic` | `SemanticBody` (subject, predicate, object, confidence) | consolidada por curadoria; proveniência decisiva (AOS-042) |
| Procedural | `ClassProcedural` | `ProceduralBody` (skill_name, version SemVer, definition_hash, stage) | staging → eval-gate → produção → rollback (AOS-040) |
| De trabalho | `ClassWorking` | `WorkingBody` (turn_index, content, token_count) | efémero; gerido pela janela de contexto (AOS-037) |

```mermaid
classDiagram
    class Record {
        +string ID
        +MemoryClass Class
        +Metadata Metadata
        +Body Body
        +Validate() error
    }
    class Metadata {
        +string AgentID
        +string RunID
        +Provenance Provenance
        +time CreatedAt
        +TTLClass TTLClass
        +string SchemaVersion
        +Validate() error
    }
    class Body {
        <<interface>>
        +Class() MemoryClass
    }
    class EpisodicBody
    class SemanticBody
    class ProceduralBody
    class WorkingBody
    Record --> Metadata : metadados obrigatorios
    Record --> Body : corpo tipado por classe
    Body <|.. EpisodicBody
    Body <|.. SemanticBody
    Body <|.. ProceduralBody
    Body <|.. WorkingBody
```

### 8.2 Metadados obrigatórios (fail-closed)

Todo o `Record` carrega os seis metadados obrigatórios do critério de aceitação —
`agent_id`, `run_id`, `provenance` (trusted|untrusted), `created_at`, `ttl_class`,
`schema_version`. `Metadata.Validate()` impõe a presença de **todos** por ordem
estável; em particular, escrever sem `provenance` **ou** sem `schema_version`
devolve erro sentinela e **não persiste** — nunca há default silencioso. A
proveniência prepara a quarentena (AOS-042) e o `ttl_class` prepara o TTL/GDPR
(ADR-011); nenhum desses mecanismos é implementado aqui, apenas o metadado que os
habilita.

### 8.3 Porta versionada e backend-swap

A porta `ports.MemoryPort` (SemVer `ports.PortVersion = "1.0.0"`) expõe
CRUD/query **por classe** e **não vaza** o backend — nenhum tipo do Event Store
ou de qualquer adaptador aparece nas assinaturas, só entidades de domínio. Dois
adaptadores implementam-na com semântica observável idêntica, e um **contract
test partilhado** (table-driven) corre contra ambos — é a prova de que o backend
é substituível por configuração sem alterar chamadores. A idempotência de escrita
é `f(run_id, class, mem_id)`: um retry após crash não duplica o registo (o
duplicado devolve o registo original).

```mermaid
flowchart TD
    CALLER["Chamador (runtime, servicos)"]
    SVC["memory.Service (fachada): relogio + id injectaveis, valida fail-closed"]
    PORT["ports.MemoryPort v1.0.0 (CRUD/query por classe, SemVer)"]
    ESADP["adapters.EventStoreAdapter (FONTE DE VERDADE)"]
    INADP["adapters.InMemoryAdapter (teste)"]
    ES["Event Store replicado append-only (ADR-007)"]
    CT["contract_test partilhado (ambos verdes)"]
    TR["agentruntime.Tracer (spans gen_ai.*)"]
    CALLER --> SVC --> PORT
    PORT -.implementado por.-> ESADP
    PORT -.implementado por.-> INADP
    ESADP -->|append eventos + rebuild do log| ES
    ESADP -.span por operacao.-> TR
    INADP -.span por operacao.-> TR
    CT -.corre contra.-> ESADP
    CT -.corre contra.-> INADP
```

O `EventStoreAdapter` escreve cada operação como evento append-only
(`memory.record.written` / `memory.record.deleted`, um stream por classe) e
**reconstrói toda a leitura por replay do log** — não mantém estado autoritativo
em RAM, honrando o ADR-007 e proibindo o single-writer como fonte primária. O
`Delete` é um tombstone (novo evento), nunca uma mutação. Cada operação de porta
emite um span OTel via a porta `Tracer` zero-dep do Agent Runtime; o relógio
(`created_at`) e o gerador de IDs são injectáveis na fachada, sem `time.Now`/`rand`
no caminho de decisão.

### 8.4 Memória de trabalho e gestão da janela de contexto (implementação AOS-037)

A memória de trabalho é o **contexto activo do turno** — a janela que o modelo
efectivamente vê. É aqui que o contrato de cache do ADR-009 vive ou morre. O pacote
`working` (`packages/platform/memory/working`) implementa a gestão da janela com o
layout **cache-estável** e ZERO dependências externas, reutilizando o
`agentruntime.PromptAssembler` (AOS-013) em vez de reimplementar o layout.

**Prefixo imutável + tail append-only (ADR-009).** O `WindowManager` é construído
UMA vez por run: congela o **prefixo** (system prompt + tool set na ordem fixada) e
calcula o seu hash. A janela cresce **só pelo tail** (`Append` de segmentos
`memory`/`tool_result`/`history`/…); NUNCA existe um método que mute ou reordene o
prefixo. A imutabilidade é provada por teste — o `PrefixHash()` é byte-idêntico ao
longo de todos os turnos do run, enquanto o `PromptHash` (prefixo + tail) muda a
cada turno porque o tail cresce.

**Contabilidade em tokens (não em nº de mensagens).** A `Occupancy` mede a ocupação
da janela EM TOKENS, separando a parte cacheável (`PrefixTokens`, constante) da nova
(`TailTokens`, só cresce), face ao `Limit` do modelo. O estimador de tokens é uma
função **pura injectável** (default: palavras com piso por caracteres, coerente com
a projecção de AOS-036); a contagem exacta por modelo é de EPIC-06.

**Sinal de exaustão graciosa a ~80% (ADR-008).** Ao atingir o limiar
(`floor(Limit × ExhaustionRatio)`, default 0.80), a janela emite um `Exhaustion`
com acção `mark_for_compression` (marcar para compressão em checkpoint — AOS-043) e,
acima do limite do modelo, `escalate` ao runtime. NUNCA é um hard-stop cego: o
`Append` é sempre aceite e a ocupação continua a ser contabilizada — a decisão de
comprimir/ramificar/escalar é do runtime. A compressão em si **não** acontece aqui
(é AOS-043); a memória de trabalho apenas **prepara/marca**.

**Pinning de prefixo — novas tools só em runs novos.** `RequireTool` rejeita com
`ErrPrefixPinned` qualquer tool que não esteja no tool set congelado (ou com
versão/digest divergente) — introduzi-la mutaria o prefixo. Uma tool nova SÓ entra
por `NewRunWith`, que deriva um **run novo** com o tool set aumentado e, logo, um
prefixo (e hash) diferentes; o run corrente permanece intocado. É fail-closed para
a estabilidade do prefixo, coerente com o pinning de supply-chain do ADR-009.

**Eviction que preserva o registo (Princípio 4, AOS-036).** `EvictToTailBudget`
faz eviction do **tail** (nunca do prefixo) por prioridade ascendente e, dentro da
mesma prioridade, FIFO, até caber num orçamento de tokens. Cada segmento evictado é
**preservado no backend ANTES** de sair da vista, via um `EvictionSink`; o
`MemoryPortSink` escreve-o como registo `ClassWorking` na `MemoryPort`
(AOS-035/036). Se o sink falhar — ou não existir — a eviction é **recusada**
(`ErrNoEvictionSink`): o que sai da janela é apenas da VISTA injectada, o registo
nunca é apagado. A eviction PREPARA a compressão assíncrona (AOS-043) sem a executar.

**SLI de cache-hit-rate.** O `CacheHitRate()` mede, EM TOKENS, a fracção do prompt
servida da cache ao longo dos turnos: o primeiro turno estabelece a cache do
prefixo (miss); nos seguintes, por o prefixo ser byte-idêntico, os seus tokens são
hits. Num cenário de referência (prefixo grande e estável, tails pequenos, muitos
turnos) o SLI fica **acima do alvo (>80%)** e não regride — a poupança de prefix
caching do ADR-009 tornada observável.

**Observabilidade.** Cada `Turn` emite um span (`working.window.turn`) com a
ocupação na chave canónica `gen_ai.usage.input_tokens`, o `aos.prefix_hash` (o SLI
de estabilidade do prefixo) e o `aos.prompt_hash`, mais os atributos
`aos.working.*` (tokens de prefixo/tail, limiar, exhausted, marcado para compressão,
cache-hit-rate) — via a porta `agentruntime.Tracer` zero-dep. A eviction emite
`working.window.evict` com a prova `aos.working.eviction_preserved=true` e o
prefixo inalterado.

### 8.5 Memória episódica (implementação AOS-038)

A memória episódica guarda **trajectórias de execução passadas** — o que o agente
fez, em que sequência, com que resultado — e torna-as recuperáveis como memória do
agente. O pacote `episodic` (`packages/platform/memory/episodic`) implementa-a com
ZERO dependências externas (cripto via stdlib), **compondo** as fundações já
entregues em vez de as reimplementar.

**Persistência append-only como árvore de spans (ADR-007/010).** Cada episódio
persiste a trajectória através da via de REGISTO de AOS-036: `record.Persist` emite
a árvore de spans COMPLETA (raiz + um span por turno com o manifesto + a árvore
registada) para o backend de observabilidade (EPIC-08), ligada por `trace_id`. O
episódio em si é escrito como evento append-only (`memory.episode.recorded`) no
Event Store replicado — um stream dedicado, ordenado, idempotente por
`f(run_id, episode_id)` (um retry não duplica). A leitura RECONSTRÓI o índice por
replay do log; não há estado autoritativo em RAM.

**Recuperação devolve PROJECÇÃO resumida, nunca a trajectória crua (Princípio 4).**
`Recall(goal, tags)` reconstrói o índice, filtra por objectivo e/ou tags e ordena
por relevância **determinística** (score de tags + bónus de objectivo; tie-break
estável por `audit_seq` e depois `episode_id`). O conteúdo devolvido é a
**projecção** de `projection.ProjectContext` (resumo higienizado, limitado em
tokens) — NUNCA o `RawContent` nem a árvore de spans. O agente recorda episódios
sem reinjectar tudo: o `EmittedSpans` (backend) é sempre estritamente maior do que
os turnos incluídos na projecção. O embedding/similaridade é opcional (proposta) —
a implementação foca objectivo/tags.

**Replay resume-from-step (complementa o Event Store).** Um episódio recuperado dá
o `run_id`; `ResumeFrom` compõe-no com o Event Store reutilizando o `durable.Resumer`
de AOS-015 (relê os checkpoints do run e devolve o próximo passo não confirmado). O
episódio COMPLEMENTA a indexação — não substitui o ES como fonte de verdade do
replay.

**Crypto-shredding com hash-chain intacta (ADR-011).** O CONTEÚDO recuperável (a
projecção) é cifrado por **envelope encryption** (AES-256-GCM, stdlib): uma DEK
aleatória por episódio cifra o resumo, e a **chave do titular (KEK)** — do
`KeyStore` — embrulha a DEK. O ciphertext e o seu **hash** ficam no log append-only;
a **hash-chain de audit (AOS-011)** sela o HASH do ciphertext (`PayloadRef`:
`ContentHash` + `KeyRef` + `SubjectID`), NUNCA o plaintext. Apagar a KEK
(`DeleteKey` — crypto-shredding) torna o episódio **irrecuperável**
(`ErrEpisodeShredded`), mas a cadeia **não é mutada** e continua a **verificar**
(`VerifyChain` → `audit.Verify`). É a reconciliação do direito ao apagamento com a
tamper-evidence: apaga-se a chave, não o registo.

**TTL por classe.** `Sweep(now)` aplica o TTL por classe (`TTLPolicy`): episódios
cuja classe tem TTL finito e cujo `created_at + TTL` já passou são **crypto-shredded**
(a chave do titular é apagada) — expiram por política **sem partir a cadeia**;
classes permanentes sobrevivem.

**Escrita fora da hot path.** A escrita episódica é uma **fila drenável**: `Enqueue`
é O(1) e NÃO toca no Event Store, na cripto nem no tracer (não bloqueia o turno);
o trabalho pesado (registo + projecção + cifragem + selagem) corre em `Flush`,
chamado num checkpoint fora do turno crítico. É determinístico e testável (relógio,
IDs e entropia da cripto injectáveis; sem goroutines não-determinísticas). Ao
primeiro erro, `Flush` recoloca os episódios não persistidos na fila (nada é perdido).

```mermaid
flowchart TD
    subgraph HOT["Hot path (turno)"]
        ENQ["Enqueue(episódio) — O(1), sem ES/cripto"]
    end
    subgraph OFF["Fora da hot path (checkpoint) — Flush"]
        PERS["record.Persist → árvore de spans COMPLETA"]
        PROJ["projection.ProjectContext → resumo (projecção)"]
        SEAL["envelope AES-GCM sob KEK do titular"]
        ES["Event Store: memory.episode.recorded (append-only)"]
        CHAIN["hash-chain audit: sela HASH do ciphertext"]
    end
    ENQ -->|fila drenável| PERS
    PERS --> BK["Backend OBS (EPIC-08) — registo completo, trace_id"]
    PERS --> PROJ --> SEAL --> ES
    SEAL -->|ContentHash + KeyRef + SubjectID| CHAIN

    subgraph RECALL["Recuperação"]
        Q["Recall(goal, tags) — ranking determinístico"]
        DEC["decifra sob a KEK → PROJECÇÃO resumida"]
        RES["ResumeFrom + Event Store → resume-from-step"]
    end
    ES -->|replay do índice| Q --> DEC
    Q --> RES

    SHRED["DeleteKey (crypto-shredding / TTL Sweep)"]
    SHRED -.apaga a KEK.-> DEC
    DEC -.sem KEK: ErrEpisodeShredded.-> IRR["irrecuperável"]
    SHRED -.NÃO toca.-> CHAIN
    CHAIN -->|VerifyChain: continua a verificar| OK["cadeia intacta"]
```

A leitura do diagrama: a via quente só enfileira; tudo o que é pesado (as duas vias
do Princípio 4, a cifragem e a selagem) corre no `Flush` fora do turno. Na
recuperação, o índice vem do log por replay e o conteúdo é a projecção decifrada —
nunca a trajectória crua. O crypto-shredding (por apagamento de chave ou por TTL)
corta a recuperação do conteúdo sem tocar na hash-chain, que continua a verificar.

### 8.6 Proveniência e quarentena (implementação AOS-042)

O ticket AOS-042 executa a fronteira de segurança da §7: transforma a proveniência
(já metadado obrigatório de AOS-035) numa **barreira estrutural** e adiciona a
quarentena. Está implementado em `packages/platform/memory/provenance` (mesmo
módulo, Go 1.24, zero dependências externas), assente no metadado obrigatório de
AOS-035 e na hash-chain de audit de AOS-011.

**Proveniência obrigatória e imutável.** Todo o registo que atravessa esta camada
é selado num `Ingested` — um tipo cujo campo de proveniência é **não exportado e
sem mutador**. Uma vez admitido, o estatuto de confiança não muda; o construtor
clona o registo, pelo que mutar o registo do chamador depois da ingestão não altera
o selo. `Seal` falha-fecha (`ErrMissingProvenance`) uma escrita com proveniência
ausente ou não-canónica: não há default silencioso.

**Marcação automática de untrusted.** A classificação é feita pela **fonte**,
estruturalmente (`Classify`) — nunca por uma tag in-band que o conteúdo carregue.
Só `system` e `authenticated_user` são trusted; tool results, web e schemas MCP —
e qualquer fonte desconhecida — são untrusted (lado seguro). A ingestão IMPÕE a
proveniência classificada, prevalecendo sobre o que o chamador tenha posto no campo.

**Taint transitivo (sem lavagem).** `Derive` propaga o taint dos pais: qualquer pai
untrusted (ou não-canónico) torna o derivado untrusted; só todos-trusted resulta
trusted; sem pais, untrusted. Uma mistura trusted+untrusted é sempre untrusted — o
taint é contagioso, não há caminho de lavagem ao passar pela memória.

**Barreira estrutural control-plane / data-plane.** A separação é a nível de
tipo/caminho, à imagem da barreira read-only de AOS-036. A admissão (`Partition.Admit`)
encaminha cada registo pela sua proveniência selada: trusted para a `TrustedView`
(o único tipo que o planeador lê, e que só expõe `TrustedEntry`), untrusted para a
`Quarantine` (servida como `DataItem` via a porta `DataPlane`). Só `TrustedEntry`
satisfaz `PrivilegedAuthorizer` (a capacidade de autorizar uma tool call); um
`DataItem` **não o implementa** — a asserção de tipo falha e `item.AuthorizeToolCall(…)`
nem sequer compila. É esta a prova de que a memória em quarentena é
*estruturalmente* incapaz de comandar uma acção privilegiada.

**Promoção auditável.** `Promoter.Promote` promove untrusted → trusted exigindo
validação **explícita** (`policy` ou `human` + validador não vazio; senão
`ErrPromotionNotValidated`) e regista a promoção na hash-chain tamper-evident de
AOS-011 (taint de origem e validação selados). Não há promoção silenciosa nem
automática; a promoção cria um registo trusted NOVO — o original untrusted permanece
imutável, coerente com o event-sourcing.

**Integração EPIC-07 por porta.** O mecanismo real de taint control/data-plane
(SBX / dual-LLM / CaMeL) é EPIC-07 e não é reimplementado: esta camada depende só
das interfaces `TaintController` e `DataPlane`, com implementações de referência
(`DefaultTaintController`, `ReferenceDataPlane`) que EPIC-07 substituirá.

Determinismo: relógio injectável no `Promoter` (sem `time.Now` na decisão),
serialização estável herdada do audit. Observabilidade via a porta `Tracer` zero-dep
do Agent Runtime, no namespace `aos.memory.provenance.*`; sem segredos nos spans.

---

### 8.7 Memória semântica — base de conhecimento (implementação AOS-039)

O ticket AOS-039 implementa a classe **semântica** — a base de conhecimento factual
consultável (factos/entidades/relações) — em `packages/platform/memory/semantic`
(mesmo módulo, Go 1.24, zero dependências externas). É a superfície clássica do
*memory poisoning* (ASI06), pelo que **compõe** as fundações de segurança já
entregues em vez de as reimplementar: a proveniência/quarentena de AOS-042, o
crypto-shredding de AOS-038 (porta `KeyStore`) e a hash-chain de AOS-011, com o
Event Store como fonte de verdade (reconstrução por replay).

**Proveniência obrigatória (fail-closed).** `KnowledgeBase.Write(ctx, fact, source)`
declara a proveniência pela **fonte** estruturada — nunca por uma tag in-band. Uma
fonte ausente ou não-canónica é **rejeitada** (`ErrMissingProvenanceSource`), tal
como a falta do `run_id` de origem ou dos metadados obrigatórios (via a ingestão de
AOS-042, que impõe a classificação e valida). A escrita sem proveniência não deixa
sequer rasto na hash-chain — a selagem só ocorre depois de a ingestão validar.

**Quarentena de untrusted (barreira de AOS-042).** Conhecimento derivado de
`tool_result`/`web`/`mcp_schema` entra em **quarentena** (untrusted) e é servido como
`DataItem` taint-marcado — estruturalmente incapaz de autorizar uma acção: não
satisfaz `PrivilegedAuthorizer`. As **duas superfícies de consulta são disjuntas**:
`ControlPlaneView` (o que o planeador lê) só expõe conhecimento trusted como
`TrustedView`; `Recall` serve conhecimento como dados, devolvendo **sempre** a
etiqueta de proveniência e **preservando o taint** até ao consumidor (um facto
untrusted expõe um `DataItem`, nunca um autorizador). O ranking é determinístico
(score de tags/chave desc; `audit_seq` asc; `fact_id` asc).

**Curadoria/promoção auditável (Promoter de AOS-042).** `KnowledgeBase.Curate`
sela o facto em quarentena como untrusted (`Seal` — que recusa promover algo já
trusted) e delega em `Promoter.Promote`: exige validação **explícita** (política ou
humano) e regista a promoção na hash-chain (taint de origem + validação selados).
Um evento de promoção é persistido no Event Store; a reconstrução passa a servir o
facto como trusted, admitindo-o pela **fonte-curadora** (autenticado/sistema) que a
validação implica — só assim se obtém um `TrustedEntry` (a autoridade de
control-plane é inforjável). O facto original untrusted permanece imutável.

**Conformidade — TTL, redação de PII, crypto-shredding (padrão de AOS-038).** A
asserção é cifrada por **envelope AES-256-GCM** sob a KEK do titular (`KeyStore`); o
índice (chave/tags/proveniência) fica em claro. Os campos marcados PII são
**redigidos** (`[REDACTED]`) antes de servir, tanto no data-plane como no
control-plane, e a obrigação `redact_pii` é selada na cadeia. Apagar a KEK
(**crypto-shredding**, directo ou via `Sweep` de TTL por classe) torna o facto
irrecuperável (`Recoverable=false`) **sem** apagar o índice nem partir a hash-chain
— que continua a verificar (a cadeia sela o HASH do ciphertext, não o plaintext). O
`Sweep` só apaga a KEK de um titular quando **todos** os seus factos expiraram (um
não-expirado, ou de classe `permanent`, retém a chave).

Determinismo: relógio/entropia/ids injectáveis; ranking e serialização estáveis.
Observabilidade via a porta `Tracer` zero-dep, namespace `aos.memory.semantic.*`;
sem segredos nos spans.

---

### 8.8 Versionamento de schema e motor expand/contract (implementação AOS-041)

O ticket AOS-041 dá **execução** à estratégia da §6: o versionamento de schema de
memória **por classe** (SemVer) e um **motor de migração** expand → migrate →
contract com rollback, dual-write/dual-read e um registo idempotente. Não
reimplementa o modelo — o `schema_version` obrigatório em cada registo já vem de
AOS-035 (§8.2); este ticket dá-lhe **semântica** e um motor que o faz evoluir.

**Schema versionado por classe (pacote `memory/schema`).** Cada uma das quatro
classes tem uma **versão de schema corrente em SemVer** custodiada por um
`ClassRegistry`. O parser SemVer (`schema.Version`) reutiliza o padrão do scheduler
(AOS-030): `MAJOR.MINOR.PATCH` numérico, ordenação total. A evolução é **monótona**
— `Register` recusa (fail-closed, `ErrNonMonotonic`) qualquer versão que não seja
estritamente mais recente que a corrente, tal como o hot-reload de política recusa
uma versão não-monótona. `schema.Classify(from,to)` classifica a mudança em termos
de **contrato** (`ChangeMajor`/`ChangeMinor`/`ChangePatch`) — o discriminador que o
motor consulta para decidir o gate.

**Motor expand → migrate → contract (pacote `memory/migrations`).** Uma `Migration`
é a definição declarativa e **bidireccional** de uma evolução de uma classe entre
duas versões: `Up` migra From→To e `Down` migra To→From — a reversibilidade é
construtiva, não um add-on. O `Runner` opera sobre um **snapshot** de registos e
guarda, por registo, as **duas representações** (`old`/`new`); cada fase é aplicável
e reversível de forma independente:

```mermaid
flowchart LR
    N["none: so From"] -->|Expand| E["expand: From+To (dual-write/read), le From"]
    E -->|Migrate| M["migrate: le To, ambas coexistem"]
    M -->|Contract| C["contract: so To"]
    E -.RevertExpand.-> N
    M -.RevertMigrate.-> E
    C -.RevertContract (Down).-> M
```

- **Expand** computa `Up` de cada registo (nova forma), passando a haver
  **dual-write** (escrita nas duas formas via `Put`) e **dual-read** (`Read(id,ver)`
  serve a versão pedida, degradando graciosamente para a disponível). A leitura
  **canónica mantém-se em From** — nenhum leitor vê a nova forma sem a pedir: **sem
  downtime**.
- **Migrate** faz o *switch* da leitura canónica para To e **avança a versão da
  classe** no `ClassRegistry` (é aqui que a nova forma se torna a da classe).
- **Contract** remove a forma antiga; só To subsiste. Reversível: `RevertContract`
  recompõe a forma antiga por `Down`.

**Rollback sem perda nem corrupção.** O motor **nunca muta o estado corrente até
uma fase concluir com sucesso**: cada fase computa num mapa temporário e só o comita
se **todos** os transforms passarem. Uma migração falhada (um `Up` que devolve erro)
deixa o estado **byte-idêntico ao inicial** — provado por teste. `Run` encadeia as
três fases de forma **transacional**: qualquer falha reverte ao snapshot inicial. O
Event Store append-only nunca é mutado; o rollback reverte por estado/nova fase,
coerente com a §6 e o ADR-012.

**Registo de migrações durável e idempotente.** Cada `(migração, fase)` aplicada é
gravada como evento append-only no stream `memory.migrations`, com a
**idempotency_key** do Event Store (`mem-migration:<id>:<fase>`, coerente com
ADR-001). **Reaplicar é um no-op**: a segunda gravação deduplica no store e devolve
`applied=false`; a linhagem reconstrói-se por replay (`List`). A idempotência também
vale ao nível do motor: reaplicar uma fase já concluída é um no-op observável.

**MAJOR por eval-gate (porta, fail-closed).** A porta `migrations.Gate` é o
admission control da evolução de contrato (ADR-012, `tecnica/11`): `Expand` submete a
migração ao gate **antes** de tocar no estado. Mudanças MINOR/PATCH passam sempre;
uma mudança **MAJOR sem aprovação é RECUSADA** (`ErrMigrationDenied`, nada aplicado).
O default, sem gate configurado, é fail-closed total para MAJOR. A implementação de
referência (`EvalGate`) aprova por ID de migração; a porta completa (golden-set,
trace-diffing, ratificação assinada) é do EPIC-05 — aqui vive só o contrato.

Determinismo: transforms `Up`/`Down` puros (mesma entrada → mesma saída); sem
`time.Now`/`rand`; namespace de idempotência injectável; serialização estável.
Observabilidade via a porta `Tracer` zero-dep, namespace
`aos.memory.migration.*`; sem segredos nos spans.

---

## 9. Vista de qualidade

**Arquitectura.** A camada de memória assenta inteiramente sobre o Event Store como fonte de verdade única (ADR-007): não há estado autoritativo fora do log, o que elimina divergências entre réplicas e torna todo o estado reconstruível por replay. A separação contexto ≠ registo é o que permite escalar horizontalmente — workers stateless, projecção barata ao modelo, registo completo particionado — sem escolher entre custo e observabilidade.

**Segurança.** A proveniência e a quarentena (ADR-005) tornam a memória incapaz de comandar acções privilegiadas, fechando o vector de *memory poisoning* na origem. O *taint* propaga-se por derivação, pelo que não há caminho pelo qual conteúdo untrusted "lave" o seu estatuto ao passar pela memória. A promoção a conhecimento confiável exige curadoria explícita, e a memória procedural passa por eval-gate.

**Manutenção evolutiva.** O schema de memória é versionado SemVer e evolui por migrações expand/contract com rollback atómico (ADR-012), garantindo que uma mudança de formato nunca provoca indisponibilidade nem perda de dados. O manifesto por trajectória preserva a reprodutibilidade mesmo quando o schema evolui, tornando o replay e a RCA fiáveis ao longo do tempo.

---

## 10. Riscos e mitigações

| Risco | Impacto | Mitigação |
|---|---|---|
| Memory poisoning por conteúdo untrusted | Desvio persistente do planeamento (ASI06) | Proveniência + taint + quarentena; promoção só por consolidação curada (ADR-005) |
| Perda de audit trail por higiene de contexto | RCA e conformidade impossíveis | Contexto ≠ registo: projecção descartável, registo append-only sempre no ES |
| SQLite single-writer como event bus | SPOF e tecto de throughput | Event Store replicado append-only com transporte push (ADR-007) |
| Migração de schema com indisponibilidade | Janela de downtime, rollback impossível | Expand/contract com dual-write e backfill assíncrono; rollback atómico (ADR-012) |
| Duplicação de memória no retry | Estado corrompido, factos duplicados | Idempotency key = f(run_id, step_id) por evento (ADR-001) |
| Skill procedural auto-escrita com regressão | Misevolution silenciosa | Staging → eval-gate → canário → ratificação assinada (ADR-012) |
| Compressão na hot path destrói cache | Explosão de custo de tokens | Sumarização só em checkpoints assíncronos (ADR-009) |
| Prompt remontado/tools dinâmicas mutam o prefixo | Cache-hit despenca, custo explode | Prefixo imutável byte-idêntico + tail append-only; novas tools só em runs novos (AOS-037, ADR-009) |
| Janela satura sem aviso e faz hard-stop | Turno abortado cego, trabalho perdido | Contabilidade em tokens + sinal de exaustão graciosa a ~80% (marca compressão/escala), nunca hard-stop (AOS-037, ADR-008) |
| Eviction da janela apaga o registo | Perda de evidência (viola Princípio 4) | Eviction só da vista; segmento preservado no backend ANTES de sair, senão recusada (AOS-037/AOS-036) |
| Recuperação de episódio reinjecta a trajectória crua | Custo de tokens explode; higiene de contexto corrompida | Recall devolve a PROJECÇÃO resumida (AOS-036), nunca o RawContent/árvore de spans (AOS-038, Princípio 4) |
| Apagamento GDPR de episódio parte o audit trail | Tamper-evidence perdida ou direito ao apagamento impossível | Crypto-shredding: cifra por titular; apagar a KEK torna irrecuperável e a hash-chain sela só o HASH — continua a verificar (AOS-038, ADR-011) |
| Escrita episódica na hot path bloqueia o turno | Latência do loop, cache thrash | Fila drenável: Enqueue O(1) sem ES/cripto; persist só em Flush fora do turno (AOS-038) |
| Facto untrusted da base de conhecimento autoriza uma acção | Memory poisoning comanda o planeador (ASI06) | Consulta trusted-only (`ControlPlaneView`) vs. dados taint-marcados (`Recall`/`DataItem`); untrusted nunca satisfaz `PrivilegedAuthorizer` (AOS-039/AOS-042) |
| PII na base de conhecimento exposta ou apagamento GDPR parte a cadeia | Violação de conformidade (ADR-011) | Redação de PII antes de servir + crypto-shredding por titular (KEK); apagar a chave torna irrecuperável e a hash-chain sela só o HASH (AOS-039/AOS-038) |
| Promoção silenciosa de conhecimento untrusted a confiável | Contaminação da base sem rasto | `Curate` exige validação explícita (política/humano) e sela a promoção na hash-chain; sem promoção automática (AOS-039/AOS-042) |

---

## 11. Glossário

- **Memory Service (MEM):** serviço que gere os quatro tipos de memória sobre o Event Store, aplicando proveniência e projecção.
- **Event Store (ES):** log append-only replicado, com transporte push, fonte de verdade de toda a trajectória.
- **Memória de trabalho:** estado do run activo, materializado por replay do log, reproduzível resume-from-step.
- **Memória episódica:** registo de trajectórias passadas, matéria-prima de replay, RCA e eval.
- **Memória semântica:** conhecimento consolidado, indexado por embedding para recuperação top-k.
- **Memória procedural:** skills e heurísticas auto-escritas, versionadas SemVer e sujeitas a eval-gate.
- **Contexto ≠ registo:** desacoplamento entre a projecção injectada no modelo e a trajectória persistida no backend.
- **Expand/contract:** migração de schema em duas fases não-destrutivas que evita downtime e permite rollback atómico.
- **Proveniência:** metadados de origem, confiança e cadeia de derivação de cada entrada de memória.
- **Quarentena de memória:** estatuto de dados-nunca-instruções aplicado a memória derivada de fonte untrusted.
- **Memory poisoning:** contaminação persistente da memória por conteúdo untrusted (ASI06).

---

## 12. Tabela de aprovação

| Papel | Nome | Assinatura | Data |
|---|---|---|---|
| Arquitecto de Plataforma |  |  |  |
| Responsável de Segurança |  |  |  |
| Responsável de Produto |  |  |  |

---

## 13. Controlo de versões

| Versão | Data | Descrição | Autor |
|---|---|---|---|
| 1.0 | Julho 2026 | Emissão inicial | Equipa AOS |
