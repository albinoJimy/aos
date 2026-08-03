# Nota de pesquisa — Arquitecturas de memória para LLMs: RAG a escala, grafos de contexto e o paradigma LLM Wiki

| Campo | Valor |
|---|---|
| Tipo | Nota de pesquisa externa (input de desenho, não normativa) |
| Fontes | 6 vídeos públicos de engenharia de IA (transcrições integrais analisadas; ver §1) |
| Epics afectados | **EPIC-04 (MEM)** principalmente; EPIC-08 (OBS/Evals) e EPIC-09 (GOV) secundariamente |
| Princípios AOS tocados | P3 (execução durável), P4 (untrusted não comanda), P7 (política default-deny), P9 (auto-modificação com rede), P10 (soberania) |
| Estado | Para leitura do Arquitecto de Plataforma; sem alterações a specs ou ADRs propostas |

> **Para quê este documento.** Seis vídeos recentes sobre arquitecturas de memória/recuperação para LLMs convergem numa tese: o futuro não é «recuperar melhor» mas **acumular estado sintetizado, versionado e verificável**. Esta nota sintetiza cada fonte e mapeia os conceitos para os tickets do EPIC-04 e princípios do AOS, como matéria-prima de decisão — nada aqui altera ADRs nem specs sem revisão humana.

---

## 1. Fontes analisadas

| # | Vídeo | Canal | Duração aprox. |
|---|---|---|---|
| V1 | [RAG at 10 Million Documents — System Design](https://youtu.be/NQZqET-jjws) | Code with Lucian | ~15 min |
| V2 | [Knowledge Graph vs Context Graph explained](https://www.youtube.com/shorts/ckAqUjJhx1s) | Vamaze Tech | short (~2 min) |
| V3 | [The Ultimate RAG Pipeline — Explained in Under 3 Minutes](https://www.youtube.com/shorts/A2JJ-TSWfBE) | Vamaze Tech | short (~3 min) |
| V4 | [LLM Wiki vs RAG vs GraphRAG — Which AI Memory Architecture Wins?](https://youtu.be/bv9sbsIOnCM) | Use AI with Tech Dad | ~13 min |
| V5 | [Vector Less RAG Explained in Two Minutes](https://www.youtube.com/shorts/GntzuTcnKPM) | Vamaze Tech | short (~2 min) |
| V6 | [The LLM Wiki Paradigm](https://youtu.be/BzvpasWYESU) | Pastel Sketchbook | ~37 min |

Método: transcrições EN obtidas por `yt-dlp` em ambiente isolado; análise sobre o texto integral.

---

## 2. Síntese por fonte

### V1 — RAG a 10M de documentos (o vídeo de engenharia do conjunto)

Tese: um RAG de tutorial (1.000 PDFs, ~20 linhas) funciona «até deixar de funcionar»; a 10M de documentos cada componente existe para corrigir um modo de falha específico de escala. Três pilares:

- **Ingestão.** Tika (texto uniforme de qualquer formato), Unstructured (elementos tipados), Docling (layout/OCR). *Chunking* é «onde 90% dos RAGs morrem»: tabelas como unidade atómica indivisível (ou serializadas em markdown), cortes em fronteiras semânticas, *breadcrumb* de secção por chunk, metadados pré-computados (sumário, keywords, perguntas hipotéticas).
- **Retrieval como funil, não lookup.** Vector search é aproximada (HNSW) e falha em tokens exactos (códigos de erro, *part numbers*) → hybrid search (denso + BM25), filtros duros em SQL relacional, re-ranking com cross-encoder sobre o top-100.
- **Orquestração e segurança.** Planner + executor (a linha onde RAG vira agente); router condicional (não pagar retrieval por perguntas triviais); multi-agente; feedback loop de auto-correcção; **human-in-the-loop para acções irreversíveis com trilho de auditoria imutável**; red teaming contínuo (Garak, Lakera, PyRIT, NeMo Guardrails) contra prompt injection; avaliação contínua (Ragas, TruLens, DeepEval).

### V2 — Knowledge Graph vs Context Graph

Distinção conceptual: o **knowledge graph** é a memória empresarial completa (estável, curada) — mas um grafo completo é um palheiro para o agente. O **context graph** é um subgrafo pequeno, efémero, montado em runtime por uma *decision layer* que selecciona apenas o que a decisão concreta exige: «pequeno o suficiente para raciocinar sobre, não para pesquisar dentro».

### V3 — Pipeline RAG clássico (baseline)

Os 11 passos canónicos: query → pré-processamento → embedding → vector DB → retrieval (denso/esparso/híbrido + reranking) → top-K → augmentação → LLM → resposta + avaliação com feedback. Conteúdo introdutório; é a baseline que as restantes fontes problematizam.

### V4 — RAG vs GraphRAG vs LLM Wiki

- **RAG**: *stateless*/«amnésico» — redescobre relações entre documentos a cada query; RAG mal optimizado gera **alucinação contextual** (fragmentos conflitantes fundidos numa resposta confiante e errada).
- **GraphRAG** (Microsoft): nós relacionais + *community clusters*; forte em datasets massivos e relacionais, caro de indexar e manter.
- **LLM Wiki** (conceito atribuído a Andrej Karpathy): o LLM como «compilador contínuo» de uma *codebase* markdown — conhecimento composto e persistente em vez de re-derivado por query.

### V5 — Vectorless RAG

Alternativa sem embeddings: índice estrutural que preserva a hierarquia do documento (secções/subsecções); a query é roteada e **navega a árvore** como um índice de livro. Vector RAG ganha em corpora grandes e desestruturados; vectorless ganha em documentos longos e bem estruturados (manuais, contratos, specs). Troca-se o risco de *match* semanticamente próximo mas errado pela dependência da qualidade da estrutura.

### V6 — O paradigma LLM Wiki (tratado arquitectónico, ~37 min)

- **Três camadas**: `raw/` (fontes **imutáveis, read-only** — o LLM está arquitecturalmente proibido de as modificar), Wiki (markdown sintetizado pelo LLM), e schema/config (templates por tipo de entidade; ~7 tipos como «sweet spot»).
- **Três operações em loop**: *ingest* (funil classificar → estreitar → extrair → aprofundar); *filing loop* (**output dual**: cada query gera a resposta ao utilizador **e** um artigo permanente arquivado); *lint* (saúde do grafo: links partidos, claims obsoletos, e **divergence check** — gerar contra-argumentos activamente para sanitizar viés).
- **Navegação sem vector DB** em escala moderada: `index.md` (navegação espacial, catálogo com sumários de uma linha) + `log.md` (histórico temporal **append-only**).
- **Divisão de trabalho cognitivo**: humano como editor-chefe (curadoria, perguntas estratégicas, verificação); IA como bibliotecária (bookkeeping a custo ~zero).
- **Regras de ouro**: *token budgets* com *progressive disclosure* (índice → resultados → artigo completo); *fallback* por templates puros (funciona sem API de LLM); output dual; verificação humana.

---

## 3. Leitura cruzada

As seis fontes contam uma única história em espiral:

1. **V3** é o ponto de partida ingénuo;
2. **V1** mostra o rigor de engenharia necessário para o fazer escalar (funil de retrieval, segurança, avaliação);
3. **V5, V2 e V4/V6** questionam o próprio pressuposto — recuperação *stateless* — e propõem, respectivamente: navegação estrutural, recorte de contexto por decisão, e **memória composta** (conhecimento compilado, persistente e verificável em vez de chat history evanescente).

Consensos transversais dignos de nota:

- **Híbrido vence puro**: vector + keyword + filtros duros + reranking (V1); estrutura *ou* semântica conforme o corpus (V5); grafo completo *mais* recorte por decisão (V2).
- **Avaliação e ataque contínuos** são parte do sistema, não pós-produção (V1: Ragas/Garak; V6: lint + divergence check).
- **Acções irreversíveis exigem humano com trilho imutável** (V1, V6) — convergência independente com o desenho do AOS.

---

## 4. Mapeamento para o AOS (verificado contra o código)

> Estado verificado em 2026-08-02 por inspecção directa de `packages/platform/memory/`, `packages/security-tests/`, `scripts/ci/` e `CHANGELOG.md`. Todos os tickets AOS-035..AOS-044 estão **implementados** (o ficheiro do epic não tem coluna de estado; a evidência é o código + CHANGELOG).

### 4.1 EPIC-04 — Memória (MEM)

| Conceito dos vídeos | Ticket(s) AOS | Estado verificado |
|---|---|---|
| Raw imutável + camada sintetizada (V6) | AOS-036 | **Implementado** — `memory/record` + `memory/projection` com barreira de tipo; o cru nunca vaza para a projecção (testado). Diferença deliberada face ao LLM Wiki: a projecção é read-only, não uma «wiki» reescrita pelo LLM. |
| `log.md` append-only (V6) | AOS-038, ADR-007 | **Implementado e superior** — o histórico episódico vive no Event Store replicado (`adapters/eventstore_adapter.go`), não num ficheiro; `KnowledgeStreamID = "memory.semantic.knowledge"` com estado reconstruído por replay. |
| Proveniência + quarentena de untrusted (V1, V4) | AOS-042, ADR-005 | **Implementado e estrutural** — `memory/provenance/`: taint transitivo a nível de tipo (`TrustedEntry` vs `DataItem`; `AuthorizeToolCall` nem compila sobre dado untrusted), promoção só por curadoria selada na hash-chain. Nenhuma das fontes chega a este rigor — tratam proveniência como tag. |
| Filing loop / output dual com citação (V6) | AOS-039, AOS-042 | **Parcial** — factos derivados voltam à base **sempre com proveniência selada** e promoção por curadoria explícita (`Curate` → `EventTypeFactPromoted`); mas **não há ponteiros de citação** para os registos concretos de origem (só proveniência categórica + `run_id`), nem pipeline de consolidação episódico→semântico («consolidação curada, nunca absorção automática», `tecnica/04` §7). |
| Progressive disclosure índice→sumário→artigo (V6) | AOS-037, AOS-043 | **Parcial** — índice (log) → sumário (`InjectedView`) existe; o «artigo» cru é deliberadamente **não servido ao modelo** (`episodic/retrieval.go` — o cru vai só para observabilidade). Gestão de janela e compressão assíncrona byte-determinística: **implementadas** (`working/`, `compression/`). |
| Context graph — projecção por decisão (V2) | AOS-037, AOS-039 | **Parcial** — `Recall` escopado por principal/goal/tags com score determinístico; não há projecção «decision-scoped» explícita. |
| Alucinação contextual por fragmentos conflitantes (V4) | AOS-042 | **Mitigado estruturalmente** — derivado de untrusted nunca sobe a trusted sem curadoria; a fusão cega de fragmentos conflitantes fica fora do caminho autorizado. |
| RAG a escala, híbrido, reranking (V1) / vectorless (V5) | AOS-039 | **Ausente por desenho (por agora)** — não há embeddings nem busca vectorial; recall por goal/tags determinístico. AOS-039 declarou embeddings opcionais e não foram implementados. Se/quando entrar retrieval por similaridade, aplica-se toda a disciplina do V1 (híbrido + filtros + reranking). |
| Fallback por templates puros (V6) | AOS-043 | **Aderente em espírito** — compressão como função pura + `CompressionPolicy` versionada; degradação fail-closed (P8). |

### 4.2 EPIC-08 / EPIC-09 — Observabilidade, evals e governação

- **Avaliação contínua** (V1, V6): **implementada para comportamento** — eval harness (`packages/platform/eval/`, AOS-084 + AOS-114) com golden-sets, success-rate/unsafe-action-rate, trace-diffing, gate `ci-evalgate`; gate `ci-memory` (`scripts/ci/memory.sh`) com 19 testes obrigatórios + 7 meta-testes de prova-negativa. **Ausente para retrieval**: não há métricas de faithfulness/precision/recall de memória — faria sentido apenas se houvesse retrieval por similaridade (ver §4.1).
- **Red teaming de prompt injection via ingestão** (V1): **implementado** — `packages/security-tests/memory_poisoning_test.go` (quarentena, selo forjado, lavagem de proveniência, barreira de tipo) e `prompt_injection_test.go` (corpus adversarial). Ressalva documentada no próprio código: a defesa é *content-blind* (decide por capability + proveniência, nunca por conteúdo).
- **Editor-chefe humano / ratificação** (V6): **implementado** — pipeline staging → eval-gate → canary → ratificação humana assinada ed25519 para memória procedural (AOS-040, ADR-012 ratificado).

### 4.3 Lacunas que os vídeos não cobrem (e o AOS resolve ou deve registar)

- **NHI e cadeia de delegação** (P2): fora do radar das fontes; resolvido no AOS.
- **Soberania regional** (P10): idem; a ingestão/síntese respeita o board de soberania.
- **Replay determinístico** (P3): **parcial** — o checkpoint (`kernel/agent-runtime/durable`) guarda cursor + `prompt_hash` por turno, que actua como **detector de drift** fail-closed (AOS-079), não como snapshot restaurável da memória semântica lida no passo. Escolha defensável (detecta em vez de reproduzir silenciosamente), mas deve ser registada explicitamente como decisão.
- **Dívida conhecida do repo**: corpo da memória procedural ainda não persistido no ES (lacuna AOS-040-C1, documentada no CHANGELOG); ADR-005 (taint/quarentena) implementado mas por materializar em `docs/adr/`.

---

## 5. Recomendações (não vinculativas, revistas após verificação)

O veredicto da verificação: a base é **aderente nos princípios estruturais** (raw/sintetizado, proveniência, durabilidade, humano-no-loop) — precisamente os mais difíceis de retrofitar. As lacunas são de camada de recuperação e citação, adiadas conscientemente pelo desenho. Daqui:

1. **Citação fonte→síntese**: candidato a novo ticket — ponteiros tipados (episódio/facto de origem) no modelo semântico, pré-condição para qualquer filing loop automático futuro. Sem isto, a auditoria «de onde veio este facto» fica categórica, não factual.
2. **Registar a decisão de pinning por `prompt_hash`** (detector de drift vs snapshot de memória) em ADR ou em `tecnica/04`, para não ser rediscutida nem confundida com lacuna.
3. **Manter embeddings fora do caminho crítico** até haver driver de produto; quando entrarem, abrir epic próprio com a disciplina do V1 (híbrido + filtros duros + reranking + métricas de retrieval no eval harness).
4. **Fechar a dívida existente antes de expandir**: AOS-040-C1 (persistir corpo procedural no ES) e materialização do ADR-005.
5. **Manter a defesa content-blind como está** (decisão sã: conteúdo é untrusted por definição), mas documentá-la como tal nos cenários adversariais.

Nenhuma destas recomendações altera ADRs em vigor; qualquer adopção passa pelo fluxo normal (ticket → desenho → revisão).
