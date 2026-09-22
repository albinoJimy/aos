# Registo de Decisões de Arquitectura (ADRs) — AOS

Esta pasta (`docs/adr/`) é o **registo canónico de ADRs** do AOS como **documentos
individuais** — um ficheiro por decisão, em formato ADR e em PT-PT.

## Porquê esta pasta (e o que ela corrige)

Até esta ronda de ratificação (a da `specs/EPIC-13_Frontend.md` v1.0), os ADRs do AOS
existiam **apenas como um catálogo de uma linha** em dois sítios:

- `_BRIEF.md` — tabela «Decisões de Arquitectura (ADRs)», ADR-001..014;
- `specs/00_System_Spec.md` §11 «ADRs em vigor» — ADR-001..014.

Esse catálogo é a **fonte-de-verdade histórica** dos enunciados. No trunk de
implementação (`feature/AOS-128-…`), `docs/adr/` **já existia** mas continha **apenas um**
documento — `ADR-015-durable-execution.md` (ratificado, AOS-022); os restantes ADRs
(001–014) viviam só como catálogo de uma linha. Esta ronda **acrescenta** os ADRs
materializados **006/011/012/013** e o novo **016**, sem contradizer o catálogo — cada
documento deve permanecer **fiel à letra** do enunciado correspondente. Onde um ADR ainda
não foi materializado, a linha da tabela aponta para o catálogo.

### Nota de proveniência sobre a afirmação «`docs/adr` só contém ADR-015»

A EPIC-13 v0.1 afirmava que `docs/adr` continha apenas o ADR-015 — e, para o **trunk**,
isso estava **correcto** (existia só `ADR-015-durable-execution.md`). A ronda de
ratificação seguinte "corrigiu" essa frase dizendo que a pasta não existia; essa
"correcção" foi feita a partir de uma **branch de documentação** (baseada no `master`
scaffold), onde `docs/adr` de facto **não existia** — não a partir do trunk. Fica aqui
**reconciliado**: no trunk a pasta existia com o ADR-015 (execução durável), a que esta
ronda junta os cinco ADRs novos. (Nota: a recomendação informal de auditoria de promover
«Contexto ≠ registo» a ADR — RAST-05 em `analises/v1_baseline/` — é distinta do ADR-015
real, que é sobre execução durável; se materializada, tomará um número livre.)

### Nota sobre a numeração do ADR-016

A mesma auditoria (`analises/v1_baseline/`) **também** recomendou, informalmente, um ADR
de *supply-chain / TOFU* com o número 016. Essa recomendação **nunca foi ratificada**.
Nesta ronda, o número **ADR-016** é atribuído — por ratificação — à **Fronteira de
confiança da camada de UI** (o ADR que a EPIC-13/AOS-129 exige). A recomendação de
auditoria sobre supply-chain foi **posteriormente materializada** como **ADR-017**
(supply-chain do nó `aos`), tomando o **próximo número livre** — não reutilizando o 016. O
*drift* de citação em torno de ADR-005/008 (comentários em `lib.sh`/PDP/`go.mod`) fica
registado como **dívida documental** dessa fronteira de supply-chain (ADR-017) — não é
coberto pelo ADR-016.

## Estado do registo

Materializados nesta ronda: **ADR-006, ADR-011, ADR-012, ADR-013** (fiéis ao catálogo e
ao código já implementado) e **ADR-016** (novo — fronteira de confiança da UI, ratificado
na mesma ronda da EPIC-13 v1.0).

| Código | Título | Estado | Ficheiro |
|---|---|---|---|
| ADR-001 | Execução durável como primitivo | Catálogo, por materializar | catálogo em `specs/00_System_Spec.md` §11 |
| ADR-002 | Reference Monitor mandatório | Catálogo, por materializar | catálogo em `specs/00_System_Spec.md` §11 |
| ADR-003 | Identidade não-humana por agente (NHI scoped/time-bound + binding humano↔NHI auditável) | **Ratificado (AOS-176)** | [`ADR-003-identidade-nao-humana-por-agente.md`](ADR-003-identidade-nao-humana-por-agente.md) |
| ADR-004 | Isolamento ao nível do kernel | Catálogo, por materializar | catálogo em `specs/00_System_Spec.md` §11 |
| ADR-005 | Separação control/data-plane + taint | Catálogo, por materializar | catálogo em `specs/00_System_Spec.md` §11 |
| ADR-006 | Credential Broker com tokens JIT | **Ratificado** | [`ADR-006-credential-broker-jit.md`](ADR-006-credential-broker-jit.md) |
| ADR-007 | Event Store replicado | Catálogo, por materializar | catálogo em `specs/00_System_Spec.md` §11 |
| ADR-008 | Admission control global em tokens/$ | Catálogo, por materializar | catálogo em `specs/00_System_Spec.md` §11 |
| ADR-009 | Layout de prompt cache-estável | Catálogo, por materializar | catálogo em `specs/00_System_Spec.md` §11 |
| ADR-010 | Observabilidade OTel GenAI + audit WORM | Catálogo, por materializar | catálogo em `specs/00_System_Spec.md` §11 |
| ADR-011 | Policy-as-code + GDPR por desenho (soberania por board) | **Ratificado** | [`ADR-011-policy-as-code-gdpr.md`](ADR-011-policy-as-code-gdpr.md) |
| ADR-012 | SemVer + eval-gate para auto-modificação | **Ratificado** | [`ADR-012-semver-eval-gate.md`](ADR-012-semver-eval-gate.md) |
| ADR-013 | Gates de risco SA-ROC + controlo bidireccional | **Ratificado** | [`ADR-013-gates-risco-sa-roc.md`](ADR-013-gates-risco-sa-roc.md) |
| ADR-014 | Taxonomia de autonomia L0–L5 | **Aceite (2026-09-08, AOS-380)** | [`ADR-014-taxonomia-autonomia-l0-l5.md`](ADR-014-taxonomia-autonomia-l0-l5.md) |
| ADR-015 | Durable execution: contrato próprio vs. engine externo | **Ratificado (AOS-022)** | [`ADR-015-durable-execution.md`](ADR-015-durable-execution.md) |
| ADR-016 | Fronteira de confiança da camada de UI | **Ratificado (novo)** | [`ADR-016-fronteira-confianca-ui.md`](ADR-016-fronteira-confianca-ui.md) |
| ADR-017 | Supply-chain do nó `aos` e da sua distribuição (binário zero-dep, imagem distroless/non-root, SBOM+proveniência) | **Ratificado** | [`ADR-017-supply-chain-node.md`](ADR-017-supply-chain-node.md) |
| ADR-018 | Fronteira nó↔ORQ/SCH: o loop de serviço é a fonte única de verdade do ciclo de vida (v1 single-host) | **Aceite** | [`ADR-018-fronteira-no-orq-sch.md`](ADR-018-fronteira-no-orq-sch.md) |
| ADR-019 | Excepções intencionais às fronteiras canónicas de camada (v1 single-host) | **Aceite** | [`ADR-019-fronteiras-camada-excecoes.md`](ADR-019-fronteiras-camada-excecoes.md) |
| ADR-020 | Planeador como agente governado (o planeamento é um agente do runtime, não um caminho especial) | **Aceite** | [`ADR-020-planeador-agente-governado.md`](ADR-020-planeador-agente-governado.md) |
| ADR-021 | Roteamento por scoring ponderado determinístico no Model Gateway (task-fit + perfis de pesos versionados, sem exploração online) | **Aceite** | [`ADR-021-scoring-deterministico-gw.md`](ADR-021-scoring-deterministico-gw.md) |
| ADR-022 | Extensões declarativas ao grafo de plano: arestas condicionais, papel verificador e payload tipado por aresta — sem ciclos, sem blackboard | **Aceite** | [`ADR-022-grafo-plano-extensoes.md`](ADR-022-grafo-plano-extensoes.md) |
| ADR-023 | Escritor único do ciclo de vida por-run: a autoridade é o LEASE, não o componente (o SCH deriva do log; o ORQ escreve só sob posse e sobre grafo re-hidratado) — extende o ADR-018 ao distribuído | **Ratificado (2026-08-30) e ASSINADO (2026-08-31, AOS-281)** | [`ADR-023-escritor-unico-ciclo-vida-por-run.md`](ADR-023-escritor-unico-ciclo-vida-por-run.md) |
| ADR-024 | O efeito de despacho move-se da materialização para o despacho governado: a materialização admite o plano no DAG sem efeito; o `plandispatch.Dispatcher` decide a elegibilidade por passagem e produz o efeito (extende o AOS-237, dentro do ADR-023; supera o spawn-na-materialização do AOS-393, preservando as suas correcções) | **Ratificado (2026-09-11, AOS-390)** | [`ADR-024-despacho-governado-move-efeito.md`](ADR-024-despacho-governado-move-efeito.md) |
| ADR-025 | Fiabilidade medida e o controlador de autonomia: registo de desfecho de execução PÓS-EFEITO (aditivo, sem enfraquecer o audit-before-effect) como sinal honesto de fiabilidade; promoção automática só ABAIXO de L4 e não-durável através de reinício (reverte à base assinada); demoção automática, durável e por CLASSE; os invariantes de direcção da rehidratação fecham a elevação por forja sem custódia de chave (extende o ADR-014, dentro do ADR-010/ADR-023) | **Ratificado (2026-09-12, AOS-090)** | [`ADR-025-fiabilidade-medida-e-controlador-autonomia.md`](ADR-025-fiabilidade-medida-e-controlador-autonomia.md) |
| ADR-026 | O overhead de mediação é a janela da DECISÃO: o SLO de p95 < 15 ms governa a cadeia de política e EXCLUI o despacho da tool; o SLI passa a derivar de `aos.mediation.policy_latency_ns` — até ANTES da escrita do selo, por emenda de AOS-401 depois de a produção medir ~31 ms com a escrita dentro — (sem fallback para a latência do span; a escrita do selo fica observável sem SLO) e a duração da tool call mediada fica deliberadamente SEM SLO até haver alvo ratificado; arbitra a contradição do `tecnica/19` §4 vs §7 e fecha o DEF-281 (dentro do ADR-002/ADR-010) | **Ratificado (2026-09-16, AOS-398); §1 emendado (2026-09-16, AOS-401)** | [`ADR-026-overhead-de-mediacao-e-a-janela-da-decisao.md`](ADR-026-overhead-de-mediacao-e-a-janela-da-decisao.md) |
| ADR-027 | Cada nó despachado de um plano do `aos-orq` executa como um run do nó `aos` (`POST /runs` + `GET /runs/{id}`): NHI do run cunhado pelo operador e montado em ficheiro, campo `tools` como lista-branca imposta antes da mediação (ausente ⇒ sem restrição, vazia ⇒ nenhuma tool), veredicto por gramática fechada (ilegível ⇒ `fail`), conclusão durável escrita pelo `aos-orq` sob o lease (completa o ADR-024, dentro do ADR-018/023) | **Aceite (2026-09-19, AOS-413)** | [`ADR-027-execucao-dos-nos-do-plano-como-runs-do-no.md`](ADR-027-execucao-dos-nos-do-plano-como-runs-do-no.md) |
| ADR-028 | Um objectivo entra no caminho do plano por um FACTO gravado pelo nó, e o `aos-orq` consome-o sob a posse que já governa o run: o ingresso é rota do nó e NÃO importa o orquestrador (ADR-018 intacto); o consumo-uma-só-vez segue o molde de idempotency-key já existente, sem substrato de fila novo; um `serve` continua a possuir um run e a terminar (ADR-023 intacto); e um pedido sobre um run com posse responde `201 accepted` idempotente — NUNCA o estado do run —, para não reabrir o oráculo de existência que o ADR-016 fecha | **Aceite (2026-09-21, AOS-417)** | [`ADR-028-ingresso-do-caminho-do-plano.md`](ADR-028-ingresso-do-caminho-do-plano.md) |
| ADR-029 | A regra do que um `stream_id` pode ser é do CONTRATO do Event Store e tem UMA fonte (`eventstore.ValidarStreamID`), não três cópias; recusa-se um nome não representável em vez de o escapar (escapar faria dois streams colidirem no mesmo subject); e o aperto do `Append` no backend de ficheiro — a correcção da causa-raiz — fica DECIDIDO mas BLOQUEADO por uma cadeia que termina no prompt de decomposição e na revalidação com o modelo vivo | **Aceite (2026-09-22, AOS-424)** | [`ADR-029-nomenclatura-de-stream-id.md`](ADR-029-nomenclatura-de-stream-id.md) |

> Nota: os ficheiros ligados na coluna «Ficheiro» para **006/011/012/013/016** são
> criados nesta ronda de ratificação; **015** (execução durável, AOS-022), **017**
> (supply-chain do nó) e **018** (fronteira nó↔ORQ/SCH) estão **igualmente materializados
> e ligados a ficheiro próprio** — ratificados/aceites, não recomendações pendentes.
> **ADR-003** (identidade não-humana por agente + binding humano↔NHI auditável) foi
> **materializado** numa ronda posterior (AOS-176, frente 3 do D4 / EPIC-16) e está
> ligado a ficheiro próprio. Os restantes (001–002, 004–005, 007–010, 014) permanecem
> **exclusivamente** no catálogo de uma linha até serem promovidos a documento próprio —
> a ligação aponta o leitor para a fonte canónica em vigor, não para um ficheiro inexistente.

## Relação com o catálogo canónico

- O catálogo em `_BRIEF.md` e `specs/00_System_Spec.md` §11 continua a ser a
  **referência de enunciado** para todos os ADRs. Materializar um ADR **não revoga** o
  catálogo — expande-o para o formato-padrão (Contexto · Decisão · Alternativas ·
  Consequências · Estado) sem alterar a decisão.
- Um documento nesta pasta que divergisse da letra do catálogo seria um **defeito**, não
  uma actualização: qualquer mudança de decisão exige um ADR novo (ou um ADR de
  supersessão explícito), nunca uma reescrita silenciosa do enunciado histórico.
- **ADR-016** nasce **já como documento** (não estava no catálogo 001–014): a decisão de
  *fronteira de confiança da UI* é nova, produzida e ratificada nesta ronda a partir da
  EPIC-13. Sela seis invariantes de segurança da superfície humana (custódia de chave
  nunca no cliente + BFF *non-signing*; WYSIWYS; *deadline* server-side; 4-eyes atestado;
  read-path soberano + auditoria de leitura sensível; separação física
  controlo/dados), derivadas todas de pilares já implementados.

## Processo (convenção desta pasta)

- **Um ficheiro por decisão.** Nome do ficheiro: `ADR-NNN-slug-curto.md`.
- **Formato ADR**, secções mínimas: Contexto · Decisão · Alternativas consideradas ·
  Consequências · Conformidade/Enforcement · Referências. Idioma **PT-PT**.
- **Estados** usados: *Ratificado* · *Aceite* · *Proposto* · *Catálogo, por materializar* ·
  *Referenciado/Recomendado em auditoria, por materializar* · *Substituído por ADR-NNN*.
- **Numeração:** ADRs novos numeram-se **após o maior código atribuído** (hoje ADR-022:
  ADR-017 — supply-chain do nó; ADR-018 — fronteira nó↔ORQ/SCH; ADR-019 — excepções
  às fronteiras de camada; ADR-020 — planeador como agente governado; ADR-021 —
  scoring determinístico no GW, **aceite** (ratificado 2026-08-13); ADR-022 — extensões ao grafo de plano,
  **aceite** (ratificado 2026-08-13)). Códigos nunca são reutilizados; um ADR retirado passa a
  *Substituído por*, não desaparece.
- Todo o documento técnico relevante deve **citar os ADRs que o afectam**.
