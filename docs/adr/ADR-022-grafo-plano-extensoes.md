# ADR-022 — Extensões declarativas ao grafo de plano: arestas condicionais, papel verificador e payload tipado

| Campo | Valor |
|---|---|
| **ADR** | 022 |
| **Título** | Extensões declarativas ao grafo de plano (PlanDocument): arestas condicionais, papel verificador de execução e payload tipado por aresta — sem ciclos, sem blackboard |
| **Estado** | Aceite |
| **Data** | 2026-08-05 (ratificado 2026-08-13) |
| **Deciders** | Equipa AOS (**ratificação de dono**, 2026-08-13) |
| **Contexto-fonte** | Análise comparativa do conceito «Graph Engineering» ([AI Builder Club — Graph Engineering Guide 2026](https://www.aibuilderclub.com/blog/graph-engineering-guide-2026)) contra o grafo de plano do AOS (`packages/control-plane/orchestrator/`, EPIC-03/EPIC-19) |
| **ADRs relacionados** | ADR-002 (RM mandatório), ADR-003 (NHI por agente), ADR-005 (control/data + taint), ADR-008 (admission tokens/$), ADR-010 (observabilidade/replay), ADR-013 (gates SA-ROC), ADR-018 (fronteira nó↔ORQ/SCH), ADR-020 (planeador como agente governado) |
| **Supersede** | — |

> **RATIFICADO (2026-08-13, autoridade de dono).** O estado passou de *Proposto* a
> *Aceite*: a decisão da §2 — as três extensões **e** os cinco invariantes de §2.4 — é
> agora **autoridade congelada** e não se re-litiga sem emenda datada (Carta §6). Em
> particular, ficam congeladas as duas **rejeições**: ciclos livres por aresta (§3-b) e
> *blackboard* mutável (§3-c). O que fica fora — a gramática das condições, o schema do
> veredicto e os tipos de payload (§4 «Fora de escopo») — **continua fora**: é trabalho de
> `tecnica/18` e dos tickets de implementação (**AOS-270..273**), não decisão re-aberta
> por esta ratificação.
>
> Este ADR **propõe** três extensões ao schema do PlanDocument — arestas condicionais,
> papel verificador com semântica de sistema e payload tipado por aresta — capturando o
> que o «graph engineering» da indústria tem de genuinamente útil, **sem** importar os
> dois mecanismos que o AOS já tornou arquitecturalmente impossíveis por boas razões:
> ciclos de execução livres (loop-back por aresta) e estado partilhado mutável tipo
> *blackboard*. Tudo o que aqui se propõe entra pelo mesmo funil de sempre: schema
> fechado, validação pura sem LLM, gate SA-ROC, orçamento da árvore e replay
> byte-a-byte.

---

## 1. Contexto

O grafo de plano do AOS (AOS-025/AOS-230) já é, na prática, um «graph engine» governado:
nós especializados com `role` e tools pinadas, arestas `depends_on`, fan-out limitado
por headroom, fan-in declarado, orçamento hierárquico CAS por nó, e replan de subgrafo
(AOS-239) como recuperação governada. O debate da indústria em 2026 («graph
engineering»: nós/arestas/estado partilhado, arestas condicionais, loop-back de revisão,
verificador dedicado) expõe três lacunas reais desse desenho:

1. **As arestas são só precedência.** O schema do `Node` (`plandocument.go`) não exprime
   «se o resultado do nó X satisfaz a condição C, segue o ramo A; senão, o ramo B». Hoje,
   qualquer desvio por resultado exige replan completo — pesado para decisões locais e
   previsíveis à data do planeamento (p. ex. «se a cobertura de fontes for insuficiente,
   executa o nó de pesquisa suplementar já declarado»).
2. **Não há verificador como papel de execução.** A verificação vive **fora** do grafo
   (validador puro AOS-231, risco derivado AOS-232, gate humano ADR-013, RiskGate no RM).
   A literatura identifica o nó revisor — read-only, agente distinto do produtor — como o
   nó de maior valor de um organigrama; no AOS, um `role: verifier` seria hoje apenas um
   rótulo declarativo, sem semântica.
3. **Não há payload tipado por aresta.** Dados fluem por resumo filho→pai (1–2k tokens,
   «contexto ≠ registo») e MEM governada. Nada no schema declara que *output* de um nó
   alimenta que *input* de outro, com que tipo e que *taint* — o que impede validação
   estática de contratos entre nós e propagação explícita de proveniência.

Ao mesmo tempo, os dois mecanismos centrais do «graph engineering» livre **já foram
rejeitados pela arquitectura**: ciclos por aresta (aciclicidade imposta fail-closed na
admissão, AOS-025) e *blackboard* mutável (o «state drift» que o próprio guia admite ser
a principal causa de podridão de grafos é impossível por desenho: Event Store append-only
+ resumos + MEM com quarentena, ADR-005).

## 2. Decisão

Estender o PlanDocument e o despacho com três capacidades declarativas, sob cinco
invariantes inegociáveis:

### 2.1 Arestas condicionais declarativas

O `Node` passa a admitir arestas com **condição** — expressão declarativa em subconjunto
fechado do schema (sem código arbitrário), avaliada deterministicamente sobre o
**resultado registado** do nó de origem (veredicto, métricas declaradas, estado
terminal). A condição é validada pelo validador puro (AOS-231) como qualquer outro campo
e avaliada pelo despachante sem estado (`plandispatch`, AOS-238) — nunca pelo LLM em
runtime. **Uma aresta condicional nunca pode fechar ciclo**: o ramo de «reprovação»
aponta para nós ainda não executados do mesmo plano (declarados *à priori*), e o retorno
a nós já executados continua a ser **replan de subgrafo** (AOS-239) — a aciclicidade do
DAG de admissão é preservada estruturalmente.

### 2.2 Papel verificador com semântica de sistema

`role: verifier` deixa de ser um rótulo e passa a ter semântica imposta pelo sistema:

- **read-only por construção** — a NHI do nó verificador materializa-se com `Authority[]`
  sem tools de efeito (escrita MEM, egress, spawn); a mediação RM (ADR-002) recusa o
  resto, fail-closed;
- **produtor ≠ verificador** — o validador rejeita plano em que o veredicto de um nó é
  emitido pelo próprio nó produtor ou por nó da sua sub-árvore de delegação;
- **veredicto estruturado** — a saída é um objecto tipado (p. ex. `pass/fail + razões +
  métricas`), registado como evento, e é o **único** tipo de resultado que as condições
  de 2.1 podem consumir para ramos de qualidade;
- débito normal da árvore (ADR-008) — verificar custa tokens e entra no orçamento como
  qualquer nó.

### 2.3 Payload tipado por aresta

Cada nó passa a poder declarar `outputs` e cada aresta `consumes` — contratos tipados
(nome, schema, classificação de *taint*) validados estaticamente pelo validador puro: o
grafo não valida se um nó consome um output inexistente, de tipo incompatível ou com
*taint* incompatível com a autoridade do consumidor (ADR-005 — *untrusted* continua a
não autorizar elevação). O transporte do payload **não é um blackboard**: é referência
a registo no Event Store/MEM com proveniência, respeitando «contexto ≠ registo» (o
consumidor recebe resumo/referência, não o histórico bruto).

### 2.4 Invariantes preservados (pré-condições da proposta)

1. **Aciclicidade na admissão** (AOS-025) — nenhuma extensão reintroduz ciclos por aresta.
2. **Plano como untrusted** (ADR-005) — condições, veredictos e payloads entram no schema
   fechado (`DisallowUnknownFields`) e na validação pura; nada é executado directamente.
3. **Determinismo/replay** (ADR-010) — veredictos e avaliações de condição são eventos;
   o replay reproduz-os, nunca re-avalia nem re-chama LLM; ordenações estáveis.
4. **Orçamento da árvore** (ADR-008) — verificadores e avaliação de condições debitam
   tokens/$ com reserva CAS; teto duro por nó e breaker por árvore inalterados.
5. **Gate como fronteira** (ADR-013) — o humano no gate vê o organigrama **com** as
   condições e os verificadores declarados; auto-aprovação só dentro do envelope L0–L5;
   nós `danger` continuam a exigir revisão humana por efeito.

## 3. Alternativas consideradas

- **(a) Manter arestas de precedência + replan (estado actual).** Rejeitada como estado
  final: torna desvios locais e previsíveis dependentes de um replan completo (com gate
  e custo de planeamento) e deixa o verificador fora do organigrama — quando a
  literatura e a prática convergem em que a verificação dedicada é o nó de maior valor.
- **(b) Ciclos livres por aresta (loop-back à LangGraph).** Rejeitada: quebra a
  aciclicidade imposta na admissão (AOS-025), abre a porta a ciclos de queima de tokens
  bounded apenas pela boa vontade do grafo, e complica o replay (ordem de execução deixa
  de ser uma topologia fixa). O AOS já tem o equivalente governado: replan de subgrafo
  com teto por árvore e autonomia nunca-crescente.
- **(c) Estado partilhado tipo blackboard (scratchpad mutável entre nós).** Rejeitada:
  é o vector de *state drift* que o próprio guia-fonte admite como falha n.º 1, e
  dissolveria a disciplina de *taint*/proveniência (ADR-005). O payload tipado por
  referência (2.3) obtém a interoperabilidade sem o estado mutável partilhado.
- **(d) As três extensões declarativas sobre os invariantes existentes (a decisão).**
  Captura o valor real do «graph engineering» — ramificação por resultado, verificação
  dedicada, contratos entre nós — sem reintroduzir os mecanismos que a arquitectura já
  rejeitou.

## 4. Consequências

- **Positivas:** desvios previsíveis deixam de exigir replan completo; a verificação
  passa a ser parte auditável do organigrama (veredicto como evento, não conversa);
  contratos entre nós validam-se estaticamente, antes de queimar um token; o *taint*
  propaga-se explicitamente pelas arestas.
- **Custos aceites:** o schema do PlanDocument cresce (nova `plan_version`, migração via
  `planmigrate`/AOS-243); o validador puro ganha regras (condições, produtor ≠
  verificador, compatibilidade de payload/taint); o despachante avalia condições (novos
  eventos); golden-sets do planeador (AOS-241) e cenários adversariais (AOS-244) têm de
  cobrir as extensões.
- **Fora de escopo (não-decidido aqui):** a gramática concreta das condições, o schema do
  veredicto e os tipos de payload ficam para o `tecnica/18` e para o(s) ticket(s) de
  implementação que este ADR, se ratificado, originar no EPIC-19.

## 5. Conformidade / Enforcement

- **Validador puro** (`planvalidate/`, AOS-231): rejeita aresta condicional que feche
  ciclo (reusa a aciclicidade incremental do DAG), verificador que seja produtor da
  própria sub-árvore, e aresta com `consumes` incompatível (tipo/ausência/*taint*).
- **Materialização** (`planmaterialize/`, AOS-237): a NHI do verificador é emitida sem
  autoridade de efeito; o RiskGate do RM (AOS-074) é a segunda linha, fail-closed.
- **Despacho** (`plandispatch/`, AOS-238): a avaliação da condição é função pura do
  resultado registado; evento de decisão de ramo emitido; replay reproduz o ramo tomado
  sem re-avaliação.
- **Eventos:** novos factos em `aos.planner.v1` (veredicto do verificador, decisão de
  ramo condicional) — append-only, com StepIDs determinísticos.
- **Gate SA-ROC** (ADR-013): o approval-card apresenta condições e verificadores; edição
  humana revalida sem round-trip ao LLM (AOS-236).
- **Testes:** cenários adversariais (`planadversarial/`, AOS-244) para ciclo disfarçado
  de condicional, verificador auto-referente e payload com *taint* elevado para
  consumidor privilegiado; cobertura dos módulos sem regressão (piso AOS-199).

## 6. Referências

- AI Builder Club, [Graph Engineering Guide (2026)](https://www.aibuilderclub.com/blog/graph-engineering-guide-2026) — conceito-fonte (nós/arestas/estado; verificador dedicado; *state drift*; «tenta primeiro que seja um loop»).
- `specs/EPIC-03_Orquestracao_Escalonamento.md` — AOS-025 (DAG + deadlock), AOS-026 (delegação/fan-out).
- `specs/EPIC-19_Planeador_Meta_Orquestracao.md` — AOS-230..AOS-244 (PlanDocument, validação, gate, despacho, replan).
- `tecnica/03_Orquestracao_Escalonamento.md` §3; `tecnica/18_Planner_Meta_Orquestracao.md` §3, §6.1.
- `packages/control-plane/orchestrator/` — `graph.go`, `plan/plandocument.go`, `planvalidate/`, `plandispatch/`, `replan/`.
- ADR-002, ADR-003, ADR-005, ADR-008, ADR-010, ADR-013, ADR-018, ADR-020.
