# EPIC-15 — Nó `aos` (Runtime Deployável de Referência)

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — Graduação de `cmd/aos-demo` para o nó `aos` deployável (a v1 do produto) |
| Versão | 1.0 |
| Data | 2026-07-22 |
| Classificação | Documento de Referência — Aberto |
| Documento-fonte | **`specs/00_AOS_Carta.md` v1.0 (RATIFICADA)** — §2 forma do produto, §5 DoD da v1 |
| Documentos relacionados | `specs/EPIC-14_Integracao_Composition_Root.md` (composition-root/PR-0), `specs/EPIC-13_Frontend.md` (AOS-130/133–136), `specs/00_System_Spec.md` (§13 critérios sistémicos), `docs/reports/D4-escalacao-autoridade-identidade.md`, `specs/01_Engineering_Standards_e_Handoff.md` |

---

## 1. Visão do Epic

Esta epic materializa a decisão de **forma do produto** da Carta do AOS (§2): o AOS v1 é um
**runtime de referência deployável — o nó `aos`** que se instala e corre, hospeda *runs* de
agentes sob a cadeia de governança REAL e expõe uma superfície externa mínima e estável. É a
resposta em código à pergunta *"o AOS resume-se a bibliotecas?"* — **não: corres o nó `aos`.**

O ponto de partida já existe: `cmd/aos-demo` (AOS-130) é a **superfície canónica de referência**
— um demonstrador single-process que compõe os pilares e conduz o fluxo humano. Esta epic
**gradua-o** de demonstrador (one-shot, RM com stubs neutros) para **nó de serviço**
(long-running, RM de PRODUÇÃO via `integration.NewSecuredRuntime`, com CLI + API `net/http`
stdlib + SSE de trajectória). O enforcement já está feito (PR-0.c, EPIC-14); esta epic **não
reimplementa** governança — **compõe e expõe** o que já é imposto.

**Fronteira D4 (Carta §4.2):** a autoridade de identidade real (IdP/binding humano↔NHI) é a
única decisão ABERTA que bloqueia código. O nó arranca em modo **demo-only self-minted
declarado** (o `identity.NewVerifier()` sem anchors nega toda a NHI fail-closed; o modo é
explícito no arranque e nos logs) — sem reivindicar não-forjabilidade. O go-live com identidade
real exige D4.

**Fora de escopo:** a superfície **web** bespoke (EPIC-13 AOS-137–143) está atrás da **D1(b)
CONDICIONAL** (Carta §4.2) — **não** está no caminho crítico da v1. O nó opera-se por CLI/API/SSE
sem SPA. O transporte SSE + backfill/resume (AOS-133–136 da EPIC-13) é reutilizado aqui como o
read-path do nó, sem a camada de apresentação web.

## 2. Critérios de Saída do Epic (mapeados ao DoD da Carta §5)

- [ ] **O nó `aos` corre**: um binário que compõe `integration.NewSecuredRuntime` (RM de produção,
      cadeia real de hooks, WORM único) e hospeda um *run* fim-a-fim — AOS-163/164.
- [ ] **Interface externa mínima estável**: CLI (`aos serve/run/observe/steer`) + API `net/http`
      stdlib (submeter *goal*, observar trajectória por SSE, conduzir/aprovar) — AOS-165/166/167.
- [ ] **Cadeia de governança REAL a mediar** cada tool call no nó (não os stubs do demo); guard-test
      das 5 negações (AOS-161) cobre o enforcement — herdado, verificado no e2e do nó (AOS-169).
- [ ] **Transporte SSE fail-safe** (D3): backfill + resume-from-seq + dedup por seq — AOS-167.
- [ ] **Empacotamento deployável**: contentor distroless/non-root/read-only do nó — AOS-168.
- [ ] **Aceitação sistémica**: e2e do nó (submit→observe→steer→terminate) + critérios do
      `00_System_Spec.md §13`; gates fail-closed verdes — AOS-169.
- [ ] **D4 deferida com honestidade**: o nó arranca em modo demo-only self-minted DECLARADO; sem
      reivindicar não-forjabilidade — AOS-163.

## 3. Tabela Resumo de Tickets

| ID | Título | Tipo | Estimativa | Prioridade | Dependências |
|---|---|---|---|---|---|
| AOS-163 | Bootstrap do nó `aos`: composição de PRODUÇÃO + config + modo demo-only declarado | feature | M | P0 | EPIC-14 (PR-0.c), AOS-130 |
| AOS-164 | Loop de serviço e ciclo de vida do nó (long-running, registo de runs, shutdown gracioso) | feature | M | P0 | AOS-163 |
| AOS-165 | CLI `aos` (`serve`/`run`/`observe`/`steer`) — só stdlib | feature | S | P1 | AOS-164 |
| AOS-166 | API HTTP `net/http` stdlib: submeter *goal* + controlo (steer/approve) | feature | M | P1 | AOS-164 |
| AOS-167 | SSE de trajectória: `StateProjector`→SSE + backfill + resume-from-seq + dedup (D3) | feature | M | P1 | AOS-166, EPIC-13 (AOS-133–136) |
| AOS-168 | Empacotamento do nó: contentor distroless/non-root/read-only | feature | S | P1 | AOS-164, EPIC-10 |
| AOS-169 | Aceitação sistémica e2e do nó + critérios `00_System_Spec.md §13` | chore | M | P0 | AOS-166, AOS-167 |

Estimativas XS/S/M/L (XL proibido). Prioridades P0/P1/P2. Toda a Fase 5 — Operacionalização.
Fases locais: **núcleo do nó** (AOS-163/164, P0), **interface** (AOS-165/166/167), **entrega**
(AOS-168/169). A identidade real é D4 (fora desta epic); a web é D1(b) (fora do caminho crítico).

---

## AOS-163 — Bootstrap do nó `aos`: composição de PRODUÇÃO + config + modo demo-only declarado

| Campo | Valor |
|---|---|
| Epic | EPIC-15 — Nó `aos` |
| Fase | 5 — Operacionalização |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | EPIC-14 (PR-0.c), AOS-130 |
| Bloqueia | AOS-164 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/integration/secured.go` (`NewSecuredRuntime`), `specs/00_AOS_Carta.md §2/§4.2` |

### Contexto

O `cmd/aos-demo` compõe o RM com **stubs neutros** (aceitável no ápice mínimo). O nó de produção
tem de compor a **cadeia REAL** via `integration.NewSecuredRuntime` (RM de produção, WORM único).
A identidade real é D4; até lá, o nó arranca em modo demo-only self-minted **declarado**.

### Objectivo

Um pacote de bootstrap (`cmd/aos` ou `packages/node`) que constrói o runtime seguro de produção a
partir de uma configuração mínima e explícita, fail-closed, com o modo de identidade declarado.

### Critérios de Aceitação

- [ ] O nó compõe via `integration.NewSecuredRuntime` (não os stubs do demo); um colaborador
      obrigatório em falta ABORTA o arranque (fail-closed).
- [ ] Configuração mínima e explícita (WORM store, cliente de modelo, modo de identidade, portas)
      — sem segredos em código (chaves/tokens vêm de config/vault, nunca hardcoded).
- [ ] O modo de identidade **demo-only self-minted** é DECLARADO no arranque e nos logs; sem
      reivindicar não-forjabilidade (Carta §4.2).

### Detalhes Técnicos

- Reutiliza a composição de AOS-130 como referência, trocando os stubs pela via `NewSecuredRuntime`.

### Testes Requeridos

- Teste de composição de produção (fail-closed sem colaborador); teste de que o modo demo-only é declarado.

### Definition of Done

- [ ] Bootstrap de produção compõe e falha-fechado; modo de identidade declarado; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-163 (AOS). Bootstrap do no `aos`: compoe integration.NewSecuredRuntime (RM producao,
WORM unico), config minima explicita (sem segredos em codigo), modo de identidade DEMO-ONLY
self-minted DECLARADO no arranque/logs (D4 aberta, Carta §4.2). Fail-closed sem colaborador.
```

---

## AOS-164 — Loop de serviço e ciclo de vida do nó

| Campo | Valor |
|---|---|
| Epic | EPIC-15 — Nó `aos` |
| Fase | 5 — Operacionalização |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-163 |
| Bloqueia | AOS-165, AOS-166, AOS-168 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/kernel/agent-runtime` (Run), `specs/00_AOS_Carta.md §5` |

### Contexto

O demo é one-shot (compõe, corre um turno, termina). O nó é **long-running**: aceita submissões de
*runs*, gere-os concorrentemente e encerra graciosamente sem perder trabalho durável.

### Objectivo

Um loop de serviço que hospeda *runs*: aceita um *goal*, arranca-o (via o runtime seguro), regista
o run em curso, e encerra com shutdown gracioso (drena/persiste, nunca mata cegamente — AOS-023).

### Critérios de Aceitação

- [ ] O nó aceita e hospeda múltiplos *runs* (registo de runs em curso por RunID).
- [ ] Shutdown gracioso: um sinal de paragem drena/persiste o estado durável (sem efeitos perdidos
      nem duplicados no reinício — a durabilidade é EPIC-02).
- [ ] O nó é resiliente a um run que falha (um run não derruba o nó; fail-closed por-run).

### Detalhes Técnicos

- Concorrência por-run; o Event Store é o estado partilhado (o nó é stateless sobre ele).

### Testes Requeridos

- Teste de hospedagem de múltiplos runs; teste de shutdown gracioso; teste de isolamento de falha por-run.

### Definition of Done

- [ ] Loop de serviço hospeda runs, shutdown gracioso, falha por-run isolada; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-164 (AOS). Loop de servico do no: long-running, aceita/hospeda multiplos runs (registo
por RunID), shutdown gracioso (drena/persiste, nunca mata cego, AOS-023), falha por-run isolada.
Stateless sobre o Event Store.
```

---

## AOS-165 — CLI `aos` (`serve`/`run`/`observe`/`steer`)

| Campo | Valor |
|---|---|
| Epic | EPIC-15 — Nó `aos` |
| Fase | 5 — Operacionalização |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | S |
| Dependências | AOS-164 |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `specs/00_AOS_Carta.md §2` (interface externa mínima) |

### Contexto

A superfície de linha de comando é a via mais simples de operar o nó (arrancar o serviço, submeter
um *goal*, observar uma trajectória, conduzir um run). Só stdlib (`flag`/`os`) — sem deps externas.

### Objectivo

Um comando `aos` com subcomandos: `serve` (arranca o nó), `run` (submete um *goal*), `observe`
(segue a trajectória de um run), `steer` (envia correcção/pausa a um run).

### Critérios de Aceitação

- [ ] `aos serve` arranca o nó (loop de serviço, AOS-164); `aos run --goal …` submete um *goal*.
- [ ] `aos observe <run-id>` segue a trajectória; `aos steer <run-id> …` conduz o run (out-of-band).
- [ ] Só stdlib (`flag`, `net/http` client); zero dependências externas.

### Detalhes Técnicos

- A CLI é um cliente da API HTTP (AOS-166) ou do loop in-process (para `serve`).

### Testes Requeridos

- Teste de parsing de subcomandos; teste e2e da CLI contra um nó in-process.

### Definition of Done

- [ ] CLI com serve/run/observe/steer; só stdlib; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-165 (AOS). CLI `aos` (subcomandos serve/run/observe/steer), so stdlib (flag + net/http
client). Cliente da API (AOS-166). Testa parsing e e2e contra um no in-process.
```

---

## AOS-166 — API HTTP `net/http` stdlib: submeter *goal* + controlo

| Campo | Valor |
|---|---|
| Epic | EPIC-15 — Nó `aos` |
| Fase | 5 — Operacionalização |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-164 |
| Bloqueia | AOS-167, AOS-169 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `specs/EPIC-13_Frontend.md` (AOS-133, ponte de controlo), ADR-016, `specs/00_AOS_Carta.md §2` |

### Contexto

A API HTTP é a interface estável do nó para submeter trabalho e conduzi-lo. `net/http` stdlib (D3:
sem gRPC/WS/GraphQL). O canal de **controlo** (trusted) fica separado do de **dados** (untrusted) —
regra de ouro ADR-016; a API nunca assina em nome do humano (BFF non-signing).

### Critérios de Aceitação

- [ ] `POST /runs` submete um *goal* e devolve o RunID; `POST /runs/{id}/steer` e
      `POST /runs/{id}/approve` conduzem/aprovam (canal de controlo, idempotente por RequestID).
- [ ] Só `net/http` stdlib; entrada validada; o canal de controlo é separado do de dados (ADR-016).
- [ ] Fail-closed: um pedido malformado/não-autorizado é recusado sem efeito.

### Detalhes Técnicos

- Idempotência de controlo por RequestID; a API delega no loop de serviço (AOS-164), nunca executa efeitos.

### Testes Requeridos

- Teste de submit/steer/approve; teste de idempotência por RequestID; teste de separação de canais.

### Definition of Done

- [ ] API submit+controlo em net/http stdlib; canais separados; fail-closed; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-166 (AOS). API net/http stdlib do no: POST /runs (submete goal), POST /runs/{id}/steer,
POST /runs/{id}/approve (controlo, idempotente por RequestID). Canal controlo separado de dados
(ADR-016); BFF non-signing. Fail-closed. Delega no loop de servico, nunca executa efeitos.
```

---

## AOS-167 — SSE de trajectória: `StateProjector`→SSE + backfill + resume-from-seq + dedup (D3)

| Campo | Valor |
|---|---|
| Epic | EPIC-15 — Nó `aos` |
| Fase | 5 — Operacionalização |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-166, EPIC-13 (AOS-133–136) |
| Bloqueia | AOS-169 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/control-plane/governance/control-surface` (`StateProjector`), `specs/EPIC-13_Frontend.md` (AOS-134) |

### Contexto

O read-path tempo-real do nó: a `StateProjector.Observe` já faz fan-out por push (AOS-130/150). Esta
etapa fá-la ponte para **SSE** (`text/event-stream`, D3 fixado) com **backfill** (o cliente que liga
tarde vê o histórico), **resume-from-seq** (reconexão sem lacunas) e **dedup por seq**.

### Critérios de Aceitação

- [ ] `GET /runs/{id}/trajectory` (SSE) transmite as reflexões de estado/trajectória por push.
- [ ] Backfill: um cliente que liga a um run já em curso vê o histórico antes do tempo-real.
- [ ] Resume-from-seq + dedup por seq: uma reconexão retoma sem lacunas nem duplicados.

### Detalhes Técnicos

- Reutiliza `eventstore.Read(fromSeq)` (já existe) + a subscrição do `StateProjector`. Só stdlib.

### Testes Requeridos

- Teste de stream SSE; teste de backfill; teste de resume-from-seq + dedup.

### Definition of Done

- [ ] SSE de trajectória com backfill/resume/dedup; só stdlib; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-167 (AOS). SSE de trajectoria: GET /runs/{id}/trajectory (text/event-stream, D3), ponte
StateProjector.Observe->SSE + backfill (eventstore.Read(fromSeq)) + resume-from-seq + dedup por seq.
So stdlib. Reutiliza AOS-133-136 da EPIC-13 sem a camada web.
```

---

## AOS-168 — Empacotamento do nó: contentor distroless/non-root/read-only

| Campo | Valor |
|---|---|
| Epic | EPIC-15 — Nó `aos` |
| Fase | 5 — Operacionalização |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | S |
| Dependências | AOS-164, EPIC-10 |
| Bloqueia | — |
| Responsável sugerido | Responsável de Plataforma/DevOps |
| Documentos de referência | `specs/EPIC-10_Topologia_Operacao_DR.md`, `specs/EPIC-13_Frontend.md` (AOS-142) |

### Contexto

Para ser *deployável* (Carta §2), o nó precisa de uma forma de entrega endurecida: contentor
distroless, non-root, root-fs read-only — coerente com a postura de EPIC-10 e o Dockerfile do BFF
(AOS-142). Zero dependências externas de runtime (o binário Go é estático).

### Critérios de Aceitação

- [ ] Dockerfile multi-stage: build → imagem **distroless**, **non-root**, **read-only rootfs**.
- [ ] Binário estático (CGO off); sem shell nem package manager na imagem final; superfície mínima.
- [ ] Config por variáveis de ambiente/ficheiro montado (sem segredos na imagem).

### Detalhes Técnicos

- Alinhar com o módulo edge/DMZ e a postura de EPIC-10; imagem reprodutível.

### Testes Requeridos

- Build da imagem; smoke-test de que o nó arranca no contentor (non-root, read-only).

### Definition of Done

- [ ] Contentor distroless/non-root/read-only arranca o nó; sem segredos na imagem.

### Handoff para Claude Code

```text
Ticket AOS-168 (AOS). Dockerfile multi-stage do no `aos`: distroless, non-root, read-only rootfs,
binario estatico (CGO off), sem shell/pkg-manager, config por env/ficheiro montado (sem segredos na
imagem). Coerente com EPIC-10 e AOS-142.
```

---

## AOS-169 — Aceitação sistémica e2e do nó + critérios `00_System_Spec.md §13`

| Campo | Valor |
|---|---|
| Epic | EPIC-15 — Nó `aos` |
| Fase | 5 — Operacionalização |
| Tipo | chore |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-166, AOS-167 |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `specs/00_System_Spec.md §13`, `specs/00_AOS_Carta.md §5`, `packages/integration/enforcement_guard_test.go` (AOS-161) |

### Contexto

O critério de "feito" da v1 (Carta §5): o nó corre um fluxo COMPLETO via a interface externa, com a
governança real a mediar, e satisfaz os critérios sistémicos do System Spec §13.

### Objectivo

Um teste e2e do nó que exercita o fluxo submit→observe(SSE)→steer→terminate via a API, prova a
mediação total (a governança real corta o que tem de cortar) e mapeia os critérios sistémicos §13.

### Critérios de Aceitação

- [ ] E2e: submeter um *goal* pela API, observar a trajectória por SSE, conduzir por steer, terminar.
- [ ] Mediação total no nó: uma tool call não-autorizada é NEGADA (herda o guard-test AOS-161; o nó
      usa a cadeia real, não stubs).
- [ ] Os critérios sistémicos do `00_System_Spec.md §13` aplicáveis à v1 estão verdes ou
      explicitamente deferidos (D4).

### Detalhes Técnicos

- Determinista, in-process (sem infra externa); reutiliza o modelo fake do demo.

### Testes Requeridos

- E2e do nó via API/SSE; verificação da mediação total; checklist dos critérios §13.

### Definition of Done

- [ ] E2e do nó verde; mediação total provada; critérios §13 verdes/deferidos; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-169 (AOS). Aceitacao sistemica e2e do no: submit->observe(SSE)->steer->terminate via a
API; prova mediacao total (cadeia real, nao stubs; herda AOS-161); mapeia System Spec §13 (verde ou
deferido D4). Deterministico, in-process, modelo fake.
```

---

## Tabela de aprovação

| Papel | Nome | Decisão | Data |
|---|---|---|---|
| Dono do produto | — | — | — |
| Arquitecto de Plataforma | — | — | — |
| Responsável de Segurança | — | — | — |

## Controlo de versões

| Versão | Data | Alteração | Aprovação |
|---|---|---|---|
| 1.0 | 2026-07-22 | Emissão inicial. Materializa a decisão de forma do produto da Carta do AOS (§2): graduar `cmd/aos-demo` para o nó `aos` deployável. 7 tickets AOS-163–169 (bootstrap de produção, loop de serviço, CLI, API HTTP, SSE, empacotamento, aceitação sistémica), mapeados ao DoD da Carta §5. A identidade real (D4) e a web (D1(b)) ficam fora do caminho crítico. | — |
