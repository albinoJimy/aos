# Carta do AOS — Forma do Produto, Registo de Decisões e Definition-of-Done da v1

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | **Carta** — autoridade sobre a forma do produto, as decisões congeladas e o *done* da v1 |
| Versão | **1.0 — RATIFICADA (CONGELADA)** |
| Data | 2026-07-22 |
| Estatuto | **É a fonte única** sobre *o que construímos* e *o que está decidido*. Supera qualquer ambiguidade em documentos subordinados. A partir daqui, a execução mede-se contra o §5 e as decisões só mudam pelo §6 (emenda datada). |

---

## 0. Porque esta Carta existe

O AOS foi construído **bottom-up e com rigor** — mas três coisas nunca foram fixadas, e é
por isso que cada epic reabria a discussão e o programa entrava em **retrabalho sem fim**:

1. **A forma do produto** ("o que entregamos e consideramos *feito*") nunca foi decidida — o
   próprio `00_System_Spec.md` hesita entre *"blueprint"* e *"produto/plataforma standalone"*.
2. **O `_FONTE_agentic-os-ideal.md` é uma visão *ideal*, não congelada** — o alvo deslocava-se.
3. **As decisões estavam espalhadas e re-litigadas** (ADRs, D1–D7, D-TAIL, D4) — sem um
   registo único congelado, voltava-se sempre atrás. O **PR-0** (dívida de integração
   descoberta a meio) foi o sintoma clássico.

Esta Carta resolve os três pontos de uma vez. É deliberadamente curta: não repete o
*quê/como* técnico (isso é o `00_System_Spec.md` e os `tecnica/`), fixa o **enquadramento**.

## 1. Hierarquia de documentos (quem manda sobre o quê)

| Camada | Documento | Autoridade |
|---|---|---|
| **Visão (congelada)** | `_FONTE_agentic-os-ideal.md` | O ideal ratificado. **Só muda por emenda** (§6). Não se toca em execução. |
| **Carta (esta)** | `specs/00_AOS_Carta.md` | **Forma do produto + registo de decisões + DoD da v1 + regra de congelamento.** |
| **Especificação** | `specs/00_System_Spec.md` | O *quê/como* detalhado (domínio, arquitectura, capacidades). Subordinado à Carta na forma do produto. |
| **Decisões arquitecturais** | `docs/adr/ADR-*.md` | Decisões técnicas estruturantes. Referenciadas no registo (§4). |
| **Execução** | `specs/EPIC-*.md` → tickets AOS-NNN | Decompõem e implementam. Medem-se contra o **DoD da Carta**, não contra o "ideal". |
| **Engenharia** | `specs/01_Engineering_Standards_e_Handoff.md` | Padrões de código/DoD-de-domínio por ticket. |

Em caso de divergência sobre **a forma do produto ou o estado de uma decisão**, prevalece
esta Carta. Em caso de divergência técnica de detalhe, prevalece o `_FONTE`/`tecnica/`.

## 2. A forma do produto (DECIDIDA — 2026-07-22)

> **O AOS v1 é um RUNTIME DE REFERÊNCIA DEPLOYÁVEL: um nó `aos` que se instala e corre,
> hospedando *runs* de agentes sob a cadeia de governança REAL, com uma interface externa
> mínima (CLI + API stdlib).**

- **É:** um binário/serviço que se executa (`aos`), que compõe os pilares (o composition-root
  `packages/integration` graduado), que hospeda *runs* de agentes através do loop base + o
  Reference Monitor de produção, e que expõe uma superfície estável (CLI + API `net/http`
  stdlib) para submeter *goals*, observar trajectórias e conduzir/aprovar (steer/HITL).
- **NÃO é:** *só bibliotecas* (as bibliotecas são o kernel + drivers; o "OS" é o nó montado
  que corre). NÃO é (na v1) um SaaS multi-tenant gerido, nem um produto com UI web bespoke
  (isso é condicional — ver D1(b)).
- **A resposta ao ponto de partida:** "corres o nó AOS", não "importas um conjunto de
  bibliotecas". O `cmd/aos-demo` (superfície canónica de referência, AOS-130) é o **embrião**
  deste nó; a v1 gradua-o de demonstrador single-process para um nó de serviço.

## 3. Como se usa o `_FONTE`

O `_FONTE_agentic-os-ideal.md` é a **visão ratificada e congelada**. A Carta + o System Spec +
os ADRs são o **subconjunto CONSTRUÍVEL** dessa visão. O `_FONTE` **não se edita durante a
execução**; se a realidade obrigar a desviar do ideal (ex.: D4), regista-se uma **emenda**
(§6) — nunca uma alteração silenciosa. Isto elimina o "alvo móvel".

## 4. Registo único de decisões (a fonte anti-retrabalho)

Cada decisão tem um estado — **FIXA** (não se re-litiga sem emenda), **CONDICIONAL** (fixada
na invariante, dependente de um gatilho externo nomeado) ou **ABERTA** (por decidir/escalada).

### 4.1 Decisões arquitecturais (ADRs) — todas FIXAS

| ADR | Assunto | Estado |
|---|---|---|
| ADR-006 | Credential broker JIT (segredos que o agente nunca vê) | FIXA |
| ADR-011 | Policy-as-code + GDPR/soberania | FIXA |
| ADR-012 | SemVer + eval-gate para auto-modificação | FIXA |
| ADR-013 | Gates de risco SA-ROC (tiering) | FIXA |
| ADR-015 | Execução durável (idempotência/replay/liveness) | FIXA |
| ADR-016 | Fronteira de confiança da UI (BFF non-signing, WYSIWYS, 4-eyes) | FIXA |

*(ADR-017 supply-chain foi reservado mas ainda não redigido — ABERTO, dono: Segurança.)*

### 4.2 Decisões de programa (D-series)

| D | Assunto | Estado | Dono | Referência |
|---|---|---|---|---|
| **D1(b)** | Superfície web SPA bespoke | **CONDICIONAL** — condicional a utilizadores reais + TCO de ingress + dono de 2.ª supply-chain | Produto | EPIC-13 §25 |
| **D2** | Stack no-build (HTMX/`go:embed`) | **FIXA** | Plataforma | EPIC-13 §25 |
| **D3** | Transporte SSE stdlib (não gRPC/WS/GraphQL) | **FIXA** | Plataforma | EPIC-13 §25 |
| **D4** | **Autoridade de identidade** (IdP + AAGUID + binding humano↔NHI + custódia de chave no vault) | **ABERTA — ESCALADA** | Segurança + Produto | `docs/reports/D4-escalacao-autoridade-identidade.md` |
| **D5** | BFF single-process | **FIXA** (postura); gatilho de graduação CONDICIONAL a SLO/utilizadores | Plataforma | EPIC-13 §25 |
| **D6** | Auditoria de leitura sensível | **FIXA** | Governança | EPIC-13 §25 |
| **D7** | Read-path soberano fail-closed | **FIXA** (regra); topologia CONDICIONAL a regiões/boards reais | Governança | EPIC-13 §25 |
| **D-TAIL** | Dono do tail/assembly (um só prefix-hash por run) | **FIXA — RESOLVIDA**: o loop delega a `WindowPort` | Plataforma | EPIC-14/AOS-157 |

**A única decisão ABERTA que bloqueia código é a D4.** Está escalada (relatório dedicado); a
identidade é *demo-only self-minted* até ser provisionada, sem reivindicar não-forjabilidade.
Todas as outras estão FIXAS ou CONDICIONAIS-a-gatilho-externo (não a código pendente).

## 5. Definition-of-Done da v1

A **v1 do AOS** (o nó `aos` deployável) está *feita* quando **todos** os seguintes forem verdes:

- [ ] **O nó `aos` corre**: um binário que compõe o `packages/integration` (RM de produção via
      `NewProductionSecure`, cadeia real de hooks, WORM único) e hospeda um *run* fim-a-fim.
- [ ] **Interface externa mínima estável**: CLI + API `net/http` stdlib para submeter *goal*,
      observar trajectória (SSE, D3) e conduzir/aprovar (steer/HITL).
- [ ] **Cadeia de governança REAL a mediar** cada tool call (mediação total; guard-test das 5
      negações — AOS-161, já feito).
- [ ] **Critérios de aceitação sistémicos** do `00_System_Spec.md §13` verdes.
- [ ] **Gates fail-closed verdes** (`run.sh`: build/test/-race/lint/secrets/sast/sca/apex/selftest).
- [ ] **Decisões ABERTAS resolvidas ou deferidas com dono** — em particular a **D4** (nó `aos`
      arranca em modo *demo-only self-minted* declarado; go-live com identidade real exige D4).

Enquanto a D4 estiver aberta, a v1 é **"feita até onde o código permite"**: o nó corre e
governa, mas a identidade é demo-only e o go-live com garantia de identidade fica gated.

## 6. Regra de congelamento (o mecanismo anti-retrabalho)

1. **Nenhuma decisão FIXA se re-litiga** sem uma **emenda** registada nesta Carta — uma linha
   datada no §7 (Controlo de versões) que diz *o quê* mudou, *porquê* e *quem* aprovou.
2. Uma decisão **CONDICIONAL** só "abre" quando o seu **gatilho nomeado** ocorre (ex.: D1(b)
   quando houver utilizadores reais + dono de supply-chain). Até lá, não se re-discute.
3. Uma decisão **ABERTA** tem sempre um **dono** e uma **escalada** (não fica em limbo).
4. Descobrir dívida escondida (como o PR-0) **não** é re-litigar — é uma emenda que se regista
   e se executa. O que se proíbe é reabrir o que já está FIXO por preferência ou dúvida.

## 7. Controlo de versões (emendas)

| Versão | Data | Alteração | Aprovação |
|---|---|---|---|
| 0.1 | 2026-07-22 | Emissão inicial (PROPOSTA). Fixa a forma do produto (runtime de referência deployável — nó `aos`), consolida o registo de decisões (ADRs + D1–D7 + D-TAIL + D4), define o DoD da v1 e a regra de congelamento. | Proposta |
| **1.0** | **2026-07-22** | **RATIFICADA e CONGELADA.** A forma do produto, o registo de decisões, o DoD da v1 e a regra de congelamento passam a autoridade. A partir daqui, nenhuma decisão FIXA se re-litiga sem uma emenda datada nesta tabela. | **Ratificada pelo dono do produto** |

---

*Esta Carta está RATIFICADA (v1.0) e é a autoridade congelada. A execução (epics/tickets)
mede-se contra o §5; as decisões só mudam pelo §6 (emenda datada acima).*
