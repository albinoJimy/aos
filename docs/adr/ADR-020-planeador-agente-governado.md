# ADR-020 — Planeador como agente governado

| Campo | Valor |
|---|---|
| **ADR** | 020 |
| **Título** | Planeador como agente governado (o planeamento é um agente do runtime, não um caminho especial) |
| **Estado** | Aceite |
| **Data** | 2026-08-02 |
| **Deciders** | Equipa AOS (ratificação de dono do `tecnica/18` v1.0) |
| **Contexto-fonte** | `tecnica/18_Planner_Meta_Orquestracao.md` §3.2 (v1.0 Ratificado 2026-08-02); `docs/reports/revisao-tecnica18-planner-para-ratificacao.md`; `specs/EPIC-19_Planeador_Meta_Orquestracao.md` (AOS-234) |
| **ADRs relacionados** | ADR-002 (RM mandatório), ADR-005 (control/data + taint), ADR-008 (admission tokens/$), ADR-010 (observabilidade/replay), ADR-013 (gates SA-ROC), ADR-018 (fronteira nó↔ORQ/SCH) |
| **Supersede** | — |

> Este ADR **regista** a decisão de que o Planeador (PLN) é ele próprio um agente
> governado — não um caminho especial que chama o LLM fora do runtime. Não re-litiga a
> forma do produto nem a autoridade de decomposição (do ORQ, ADR-018); fixa **como** o
> planeamento corre, para que o custo, o *taint*, a observabilidade e o replay do plano
> sejam os mesmos de qualquer outro agente.

---

## 1. Contexto

O ORQ declara decompor objectivos num grafo de tarefas; o `tecnica/18` gradua essa função
de *stub* para componente produtivo (o Planeador). O planeamento **custa tokens e pode ser
atacado**: um objectivo adversarial — ou conteúdo *untrusted* que o influencie — pode tentar
induzir um organigrama hostil. E o plano proposto por um LLM é, por decisão prévia,
**dados *untrusted*** (ADR-005): nunca é interpretado como instrução de execução.

Havia dois atalhos naturais para produzir o plano, ambos com custo escondido:

- o ORQ chamar directamente o *Model Gateway* para obter o plano;
- implementar o planeador como **biblioteca pura** invocada pelo escalonador (SCH).

Ambos retiram o planeamento da cadeia de governação real (RM, orçamento hierárquico,
*taint*, trajectória, replay) que o resto do sistema já atravessa.

## 2. Decisão

O Planeador (PLN) **é um agente que corre no runtime** (kernel), com:

- **NHI própria** — `agent:planner` na cadeia de delegação do *run* (ADR-003);
- **orçamento debitado da árvore**, com uma **reserva de planeamento admitida *antes* da
  decomposição** (ADR-008) — o arranque do próprio planeador é ele próprio admitido
  (evento `plan.planner_admitted`), resolvendo o *chicken-and-egg* de orçar antes de haver plano;
- **mediação RM** das suas chamadas de modelo e de ferramenta (ADR-002) — *o sistema come a
  própria comida*;
- **taint** como qualquer consumidor de *untrusted* (ADR-005): a saída do planeador **só
  propõe** — nunca autoriza elevação;
- **trajectória OTel** ligada ao *run* (`traceparent`) e **replay** determinístico (ADR-010):
  as N tentativas, o gate e a materialização emitem *spans*; o replay reproduz eventos, nunca
  re-chama o LLM.

### 2.1 Autoridade vs. execução (ADR-018)

A **autoridade** de decomposição é do **ORQ** (plano de controlo); a **execução** é a de um
agente-decompositor hospedado pelo *agent-runtime* (kernel), **invocado** pelo ORQ na direcção
control-plane→kernel. O planeador **produz** o PlanDocument mas **não o materializa** — a
materialização (consumo de `plan.approved`, emissão de `plan.materialized`) é do ORQ. O SCH
fica a jusante do gate e não planeia.

## 3. Alternativas consideradas

- **(a) Chamada directa ao *Model Gateway* pelo ORQ.** Rejeitada: quebra a mediação total
  (ADR-002) e esconde o custo do planeamento fora da contabilidade da árvore (ADR-008); sem
  *taint* nem replay do passo de planeamento.
- **(b) Planeador como biblioteca pura invocada pelo SCH.** Rejeitada: mesma perda de mediação
  e contabilidade; e faria o SCH **decidir/planear**, violando o ADR-018 (o SCH só despacha o
  que o ORQ materializou).
- **(c) Tratá-lo como agente normal (a decisão).** Dá, de graça: orçamento, *taint*,
  observabilidade e replay — e alinha a nova fronteira de confiança com defesas já construídas.

## 4. Consequências

- **Positivas:** mediação total preservada; custo de planeamento visível no *burn-down* e
  limitado por admissão; *taint*/observabilidade/replay do plano sem mecanismo novo; a
  superfície de ataque do plano é coberta pelas camadas existentes (validação pura, gate com
  risco resolvido, spawn mediado nó a nó).
- **Custo aceite:** o planeamento consome tokens (alvo ≤ 5% da árvore — NFR-11); exige a reserva
  de planeamento de primeira classe antes de existir plano.
- **Implementação:** AOS-234 (planeador-agente: NHI, reserva, OTel), ligado a AOS-235
  (eventos/replay), AOS-236 (gate) e AOS-237 (materialização pelo ORQ).

## 5. Conformidade / Enforcement

- O planeador atravessa o **RM** como qualquer agente; nenhuma chamada de modelo/ferramenta o
  contorna (ADR-002).
- O guard-test `boundary_orq_sch_test.go` (ADR-018) garante que o decompositor **não importa**
  o módulo de ciclo-de-vida concorrente — a autoridade continua no ORQ.
- **Verificação:** AOS-234 (decomposição não arranca sem reserva admitida; *spans* presentes
  para as N tentativas); AOS-244 (a saída do planeador não autoriza elevação — só propõe).

## 6. Referências

- `tecnica/18_Planner_Meta_Orquestracao.md` §3.2 (planeador é agente governado), §3.4 (replay),
  §6.1 (eventos `aos.planner.v1`).
- `specs/EPIC-19_Planeador_Meta_Orquestracao.md` — AOS-234, AOS-235, AOS-236, AOS-237, AOS-244.
- ADR-002, ADR-003, ADR-005, ADR-008, ADR-010, ADR-013, ADR-018.
