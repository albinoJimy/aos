# Resumo de revisão para ratificação — `tecnica/18` Planeador e Meta-Orchestração

| Campo | Valor |
|---|---|
| Documento revisto | `tecnica/18_Planner_Meta_Orquestracao.md` |
| Versão à entrada da revisão | v0.1 (proposta de desenho) |
| Versão após emendas | **v0.2** (commit `728acda`) |
| Método | Revisão adversarial multi-perspectiva — 9 lentes independentes, verificação céptica por lente, síntese |
| Veredicto da síntese | **RATIFICAR COM CONDIÇÕES** |
| Estado das condições | **Resolvidas na v0.2** (ver §3) |
| Destinatários | Arquitecto de Plataforma · Responsável de Segurança · Responsável de Produto (tabela §11 do documento) |

> **Para quê este documento.** A tabela de aprovação (§11) do doc 18 está por assinar. Este resumo dá a cada dono o que precisa para decidir: o que foi desafiado, o que foi confirmado, o que foi emendado, e o que cada assinatura atesta. Não substitui a leitura do documento — orienta-a.

---

## 1. Sumário executivo

O doc 18 gradua a decomposição do Orquestrador de *stub* para componente produtivo e especifica a meta-orchestração (um objectivo de alto nível → organigrama executável de sub-agentes, aprovado por humano antes de qualquer *spawn*). O eixo é tratar o plano proposto por um LLM como **dados untrusted** (ADR-005): nunca executado — validado, orçamentado, aprovado e só então materializado.

A revisão sujeitou o documento ao mesmo princípio: uma **proposta** a validar adversarialmente antes de assinar. Nove lentes independentes (Governança/Carta, Arquitectura, Segurança, Red-Team, Determinismo/Replay, FinOps, Produto, Exequibilidade, Rastreabilidade) produziram **52 achados**; todos passaram a verificação céptica (nenhum refutado). O veredicto foi **RATIFICAR COM CONDIÇÕES** — não há contradição estrutural que force redesenho, mas havia **2 bloqueadores** com remédio textual claro. Ambos foram emendados na v0.2, com mais 11 achados maiores e ~23 menores, e o fecho de 7 das 8 lacunas de completude (a 8.ª é esta assinatura).

**Recomendação:** ratificar a v0.2, sujeita à confirmação por cada dono de que as emendas da sua área (§4) satisfazem os achados.

---

## 2. Método da revisão

- **Fase 1 — desafio (9 lentes, cegas entre si).** Cada lente com mandato estreito e não-sobreposto, com acesso ao repositório para verificar referências reais (ADRs, tickets AOS, código, eventos). A cegueira mútua torna a convergência de lentes num sinal forte.
- **Fase 2 — verificação céptica.** Cada achado foi entregue a um céptico independente com instrução de o *refutar*; sobrevive só se sustentado por evidência no documento ou no código. **Nenhum dos 52 foi refutado.**
- **Fase 3 — síntese.** Dedup, reconciliação de tensões entre lentes, ranking por severidade, mapeamento aos donos da §11, veredicto e crítica de completude.

Confiança: os achados marcados **CONFIRMADO** têm evidência directa (citação do documento + referência de código verificada); os **PLAUSÍVEL** são bem fundamentados mas dependem de uma decisão de desenho que a emenda tornou explícita.

---

## 3. Veredicto e resolução das condições

O veredicto **RATIFICAR COM CONDIÇÕES** assentou em dois bloqueadores. Ambos resolvidos na v0.2:

| # | Bloqueador (CONFIRMADO) | Emenda na v0.2 |
|---|---|---|
| **BLK-1** | `risk_class` era proposto pelo LLM (untrusted) e o envelope de auto-aprovação L4/L5 lia esse rótulo self-declared — contradizendo o ADR-013 ("o texto auto-declarado só pode *elevar* o risco"). Um objectivo adversarial rotularia um nó com efeito irreversível como `safe` e escaparia ao olho humano. | §3.3 (schema + regra 6), §7.1, §7.2: o `risk_class` passou a **derivado** das ferramentas pinadas; efeito irreversível ⇒ `danger`; o campo do LLM é *advisory* que só eleva; o envelope L4/L5 avalia risco **resolvido**. |
| **BLK-2** | A validação era declarada "função pura, sem I/O", mas a regra 3 resolvia `tools[]` contra o REG vivo (I/O, não-determinismo, canal de erro) — contradição *load-bearing* que sustenta o ADR-005 e o replay. | §3.3: a validação passou a correr sobre um **snapshot de capabilities pinado no `propose`** (`capabilities_hash`), reproduzível e sem I/O vivo; resolução contra REG vivo é gate de *proposta*, não de *replay*. |

Além dos bloqueadores, a v0.2 emendou **11 achados maiores** e **~23 menores/notas**, e acrescentou 4 subsecções novas (§3.5 intake, §3.6 migração de schema, §4.4 Scheduler, §6.3 golden-sets) que fecham as lacunas de completude.

---

## 4. O que cada assinatura atesta

Cada dono deve confirmar que as emendas da sua área resolvem os achados. Abaixo, os pontos por assinatura.

### 4.1 Arquitecto de Plataforma

Atesta a coerência com a arquitectura congelada e as fronteiras de camada.

- **BLK-2** — validação sobre snapshot pinado (decisão: snapshot no `propose`, não I/O vivo). *[§3.3]*
- **MAI-1** (CONFIRMADO) — a localização do planeador estava afirmada em três camadas sem ponte ("corre no RT" vs "plano de controlo" vs "é o ORQ"). Reconciliado: **autoridade = ORQ** (control-plane), **execução = agente hospedado pelo kernel**, invocado pelo ORQ; não viola o guard-test `boundary_orq_sch_test.go`. *[§2, §3.2]*
- **MAI-3** (CONFIRMADO) — tectos estruturais estavam mal atribuídos a AOS-028 (que governa concorrência, não cardinalidade). Separados: tamanho do plano = plan-time (ORQ); concorrência = run-time (SCH). *[§3.3 r.4, §4.4]*
- **MAI-9** (CONFIRMADO) — "replay materializa a partir do documento" colidia com materialização = spawn-via-RM. Reescrito: replay **reproduz eventos capturados**, nunca re-atravessa o RM. *[§3.4]*
- **MAI-4 / MAI-5** (PLAUSÍVEL) — teto de custo duro por nó; arranque/admissão do próprio planeador. Tornados explícitos. *[§3.3 r.5, §3.2]*
- **Nova §4.4 (Scheduler)** — despacho a jusante do gate; re-verificação TOCTOU com spawn diferido; a espera no gate não consome *headroom*. **Ponto de decisão a subscrever:** o SCH nunca planeia — só despacha o que o ORQ materializou.

### 4.2 Responsável de Segurança

Atesta que a nova fronteira de confiança (o plano como vector) está fechada.

- **BLK-1** — risco derivado, não auto-declarado (o achado de segurança central). *[§3.3, §7.1, §7.2]*
- **MAI-2** (CONFIRMADO) — a regra 6 fixava o piso em "≥ gray"; no ADR-013 um efeito irreversível é `danger`. Corrigido, com card por efeito (AOS-120). *[§3.3 r.6]*
- **MAI-6** (CONFIRMADO) — o eixo `capability_gap` era via de implantação persistente ausente da tabela de riscos, e o agente-autor estava sub-especificado. Emendado: agente-autor governado (NHI/orçamento/allowlist), spec do gap como *input* untrusted, e **nova linha na tabela §9**. *[§5, §9]*
- **MAI-10** (PLAUSÍVEL) — hook `tools[]` → `Authority[]` da NHI filha e mapeamento nó-folha (AOS-025) vs papel-que-expande (AOS-026). Tornado explícito. *[§6.1]*
- **Nova §3.5 (intake)** — **ponto de decisão a subscrever:** a classificação de intake é *routing*, não autoridade; a **invariante de não-bypass** (qualquer delegação reentra no gate por-spawn) garante que manipular o classificador não escapa ao gate. *[§3.5, §9]*

### 4.3 Responsável de Produto

Atesta que o documento entrega o valor prometido, com autonomia honesta.

- **MAI-8** (PLAUSÍVEL) — "organigrama completo" sem triagem é ele próprio vector de fadiga. Emendado: apresentação **triada por risco**, revisão item-a-item dos nós ≥ gray e `capability_gap`. *[§4.3]*
- **MAI-11** (PLAUSÍVEL) — a auto-aprovação L4/L5 é inatingível para o caso *ad-hoc* (promoção exige janela sustentada por domínio). Assumido por escrito: L4/L5 serve domínios **recorrentes**; *ad-hoc* permanece L0 por desenho. *[§7.2]*
- **Framing** (MEN-15/16) — "fábrica/agência" conota persistência que os meta-runs não dão. Moderado; §8 mantém as organizações persistentes como *(proposta)* que exige emenda da Carta.
- **Nova §6.3 (golden-sets)** — **ponto de decisão a subscrever:** avaliar o gerador não-determinístico por asserções + K-amostras (segurança a 100%, qualidade por limiar) e *trace-diff* distribucional; é a substância do sinal de promoção de §7.2.

### 4.4 Transversal (governança — reviste pelos três)

- **MAI-7** (CONFIRMADO) — AOS-128 é *medição*, não controlo anti-fadiga; o sinal de promoção é gameável. Emendado com travão de runtime (dual-control, amostragem post-hoc) e sinal independente do humano (eval-gate de decomposição). *[§9, §7.2]*
- **Correções de referência** (todas CONFIRMADAS): burn-down AOS-127→AOS-123/062; captura de não-determinismo AOS-180→AOS-016; override-rate AOS-128→AOS-095; "Princípio 9"→"Princípio 7" do `_FONTE`; "EPIC-01..16"→"EPIC-01..18".

---

## 5. Âmbito e riscos residuais (não são defeitos — são fronteiras declaradas)

A assinatura ratifica um desenho **dentro da forma congelada da Carta**, com estas fronteiras explícitas:

- **Fora de âmbito, declarado (§1.2):** executor declarativo de skills (desenho separado — sem ele, os nós executam sobre ferramentas concretas já registadas); *wiring* do Model Gateway no bootstrap; meta-orchestração multi-host e soberania por *tenant* (eixo do EPIC-10).
- **Marcado *(proposta)*, exige emenda da Carta (§8):** organizações **persistentes** (identidade/orçamento/memória próprios que sobrevivem a *runs*). O documento **não** as autoriza; os meta-runs efémeros cobrem o caminho sem desvio.
- **Dependência de fundação:** a autoridade de identidade (D4/EPIC-16) está *code-complete* mas ainda "em provisionamento"; o endurecimento (IdP corporativo, HSM, allowlist AAGUID) é config de deployment que a organização efémera herda.

---

## 6. O que a ratificação desbloqueia — e o que não

- **Desbloqueia:** o documento passa de *proposta* a referência ratificada, habilitando o backlog de implementação do planeador (goal→DAG produtivo) dentro da forma congelada.
- **Não implica código pendente:** a revisão não deixou pendências de implementação a resolver antes da assinatura — as condições eram todas textuais e estão emendadas.
- **Não altera a Carta:** nada na v0.2 exige emenda; a única parte que exigiria (organizações persistentes) fica em §8 como *(proposta)*, expressamente não autorizada.

---

## 7. Decisão

| Papel | Decisão | Assinatura | Data |
|---|---|---|---|
| Arquitecto de Plataforma | ☐ Ratifica ☐ Ratifica c/ ressalvas ☐ Devolve |  |  |
| Responsável de Segurança | ☐ Ratifica ☐ Ratifica c/ ressalvas ☐ Devolve |  |  |
| Responsável de Produto | ☐ Ratifica ☐ Ratifica c/ ressalvas ☐ Devolve |  |  |

**Recomendação da revisão:** RATIFICAR a v0.2. A tabela de sign-off vive no próprio documento (§11); esta folha é o material de apoio à decisão.

---

## Anexo A — As 4 subsecções acrescentadas na v0.2

| Secção | Fecha | Decisão de fundo |
|---|---|---|
| §3.5 Classificação de intake | Heurística de intake | *Routing*, não autoridade; invariante de não-bypass |
| §3.6 Evolução/migração do `plan_version` | Migração de schema | Plano congelado na aprovação; replay fora da janela = inadmissível (fail-closed) |
| §4.4 O Scheduler (SCH) | Papel do SCH + concorrência na espera do gate | Despacho a jusante do gate; dois tectos, dois donos |
| §6.3 Golden-sets de decomposição | Viabilidade dos golden-sets | Asserções + K-amostras; *trace-diff* distribucional |

## Anexo B — Rasto de proveniência

- Documento e emendas: `tecnica/18_Planner_Meta_Orquestracao.md` (v0.2, commit `728acda`, ramo `feature/AOS-128-ux-dx-tests`).
- Índice alinhado: `tecnica/INDICE.md`.
- Revisão: 9 lentes × verificação céptica × síntese (19 agentes; 52 achados, 0 refutados).
