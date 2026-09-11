# ADR-024 — O efeito de despacho move-se da materialização para o despacho governado

| Campo | Valor |
|---|---|
| **ADR** | 024 |
| **Título** | A materialização passa a ADMITIR o plano no DAG sem produzir efeito; o efeito por-nó (spawn de papel, arranque de folha) move-se para o despacho governado (`plandispatch.Dispatcher`), que decide a elegibilidade passagem a passagem |
| **Estado** | **Aceite** — ratificado por autoridade de dono a 2026-09-11 (AOS-390) |
| **Data** | 2026-09-11 (proposto e **ratificado** na mesma data) |
| **Deciders** | Executor de AOS-390 (proposta) · **Dono do produto (ratificação, 2026-09-11)** |
| **Contexto-fonte** | `specs/EPIC-19_Planeador_Meta_Orquestracao.md` §AOS-237/AOS-238/AOS-390; ADR-022 §2.1 (arestas condicionais, `branch_not_taken`); ADR-023 (escritor único por-run; o SCH deriva); ADR-018 (fronteira nó↔ORQ/SCH); `packages/control-plane/orchestrator/planmaterialize/materialize.go`; `packages/control-plane/orchestrator/plandispatch/dispatch.go`; `packages/cmd/aos-orq/dispatch_wiring.go` |
| **ADRs relacionados** | **ADR-022** (torna a semântica condicional de §2.1 efectiva em runtime), **ADR-023** (opera DENTRO dele — o despacho não escreve ciclo de vida; o efeito é sob a posse do lease), **ADR-018** (nada muda no nó `aos`), ADR-020 (planeador como agente governado) |
| **Supersede** | — (EXTENDE o AOS-237; não o descarta) |

---

## 1. Contexto: a fronteira que o AOS-237 fundiu

O AOS-237 ratificou «materialização → DAG + spawn»: o `Materializer.Materialize`, num único passe, **admite** os nós E **produz efeito** (papéis via `Delegator.Spawn`, folhas via `AdmitLeaf`). Admissão e efeito estavam **fundidos**.

O `plandispatch.Dispatcher` (AOS-239, nomeado no ADR-023) foi desenhado para o oposto: decidir, **por passagem e por nó**, se um nó pode arrancar AGORA — observando o gate, o estado do ciclo de vida, `depends_on`, as arestas `conditional_on` (com poda de `branch_not_taken`, ADR-022 §2.1), o cartão de risco e o tecto de concorrência. Projecta o conjunto despachável **a partir do materializado** e entrega os nós elegíveis a um `DispatchSink`.

Duas consequências medidas: (1) **duplo-efeito** — compor o Dispatcher sobre o Materializer actual faria o Materializer spawnar tudo e o Dispatcher tornar a despachar; (2) **violação fail-open do ADR-022 §2.1** (medida a 2026-09-10) — como o efeito é up-front e a materialização não lê `conditional_on`, um nó condicional era executado sem a condição ser avaliada. O AOS-389 fechou isto de forma interina (recusa fail-closed); a semântica final é **avaliar e podar**.

## 2. Decisão

**O efeito de despacho sai da materialização e passa para o despacho governado.**

- **Materialização = admissão-pura.** `Materialize` apenas: apensa `plan.materialized`; admite TODOS os nós no DAG como **pendentes** (folhas com a sua tool call, papéis SEM tool — placeholder). **Não** produz efeito.
- **Despacho = efeito governado.** O `plandispatch.Dispatcher`, composto sob `runlifecycle.Tenure` e re-invocado por passagem, decide a elegibilidade e entrega os nós elegíveis a um `DispatchSink` de produção: papel → `Delegator.Spawn`; folha → arranque. A poda de `branch_not_taken` é uma propriedade do sistema em execução, com **um único caminho de efeito**.
- **Dentro do ADR-023**: o Dispatcher não escreve ciclo de vida (o SCH deriva); o efeito é sob a posse do lease. **Fora do nó (ADR-018)**: a composição vive no `aos-orq serve`.

## 3. Alternativas rejeitadas

- **(B) O Dispatcher gateia ANTES da materialização.** Rejeitada: `plandispatch.PlanFrom` projecta o conjunto despachável a partir do **materializado** — inverter a ordem luta contra o desenho do próprio Dispatcher.
- **(C) Dois caminhos — eager sem condicionais, diferido só com condicionais.** Rejeitada: dois mecanismos divergentes para o mesmo problema — o anti-padrão que o ADR-021/DEF-271 já custou.

## 4. Consequências

- O comportamento do Materializer muda (o spawn sai): o AOS-390 migra os testes; o guard de AOS-389 é superado pela avaliação real.
- Papéis passam a ser representados no DAG como nós pendentes (antes eram spawnados directamente).
- Portas de produção novas no composition root: `DispatchSink`, `Gate`, `CardOracle`, adaptador de `Headroom`. As portas de leitura (`LifecycleView`/`ResultView`/`BranchJournal`/`BranchBudget`) já são produção e reutilizam-se.
- Residuais DECLARADOS (com eixo): spawn de papel espera o cutover de identidade (o token do run traz `cap:plan`, a NHI filha pede `cap:tool:*`, família AOS-278); a integração de `Headroom` com `scheduler.SpawnCoordinator` (AOS-028) e o gating de cartão no despacho são follow-up; `planmigrate.Materialize` reconcilia a projecção de `Tools`.
- O que NÃO muda: o schema do plano (ADR-022), a disciplina de escritor único (ADR-023), a forma do nó v1 (ADR-018), e o facto `plan.materialized`.

## 5. Estado e ratificação

**Aceite (ratificado a 2026-09-11, autoridade de dono).** A decisão da §2 é agora **autoridade congelada** e não se re-litiga sem emenda datada (Carta §6): o efeito de despacho vive no despacho governado, não na materialização. A ratificação **supera** a abordagem de spawn-na-materialização do AOS-393 (que fez o role-spawn funcional no `--goal`): as correcções habilitadoras do AOS-393 (registo `agent.spawn` no RM, classe `worker`, união de autoridade do pai, gate de profundidade) são PRESERVADAS e portadas para o `DispatchSink`; o que muda é o *momento* do efeito (no despacho, após avaliar elegibilidade e condicionais), não essas correcções. Este ADR **não** reabre o AOS-237: extende-o (a materialização continua a existir e a apensar `plan.materialized`), movendo apenas o momento e o gating do efeito.
