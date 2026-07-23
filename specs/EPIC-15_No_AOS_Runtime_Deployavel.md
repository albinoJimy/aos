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

## 2. Critérios de Saída do Epic (mapeados ao DoD da Carta §5) — REVISTOS (emenda pós-painel)

> **Pré-requisito (Carta emenda 1.1):** esta epic depende de **AOS-156** (token spine real,
> autoridade self-hosted Nível 2). O nó da v1 usa **identidade REAL** (verifier com trust-anchor =
> pubkey do issuer), **não** o modo demo-only. Os critérios abaixo incorporam as emendas E4–E8 do
> painel `wamnbffrk` (ver §Revisão).

- [ ] **O nó `aos` corre com identidade REAL**: um binário que compõe `integration.NewSecuredRuntime`
      (RM de produção, cadeia real, WORM único) com o verifier da autoridade AOS-156, e hospeda um
      *run* fim-a-fim — AOS-163/164a.
- [x] **Substrato DURÁVEL** (E4): Event Store/WORM/KeySource persistentes (não in-memory); o
      reinício não perde nem duplica trabalho — AOS-170 (ou DoD de AOS-164b com dono).
      *(AOS-170: Event Store durável write-ahead (WAL persist+fsync ANTES de aplicar às réplicas
      in-memory/committed/fanout, fail-closed sem phantom-commit), WORM/filestore e seed do issuer
      com fsync do directório pai (durabilidade POSIX da entrada de directório); reinício reconstrói
      do WAL sem perda nem duplicação, provado sob `-race` com Append concorrente + restart. Módulos
      eventstore/audit/cmd-aos verdes (build+test+vet). **DEFERIDO para EPIC-10:** replicação
      multi-nó/DR/PITR (transporte remoto — as 3 réplicas são process-local reconstruídas do WAL),
      compactação/GC do WAL a longo prazo, e persistência-em-runtime do IngestStream (restauro
      PITR). Estes ficam [ ] até EPIC-10.)*
- [ ] **Interface externa mínima estável e AUTENTICADA** (E5): CLI + API `net/http` stdlib; o
      **canal de controlo (steer/approve) é autenticado** (não um POST anónimo) e o bind
      não-loopback é recusado até a autenticação estar ligada — AOS-165/166.
- [ ] **Health/readiness + observabilidade** (E5/E7): `/healthz`·`/readyz`; spans OTel (`otel-genai`)
      + custo + WORM ligados por OTLP — AOS-171/173.
- [ ] **Transporte SSE fail-safe** (D3, reavaliado): backfill + resume-from-seq + dedup **+
      backpressure/drop-slow-consumer**, streaming por seq (não snapshots de state) — AOS-167 (**L**).
- [ ] **Soberania/conformidade** (E7): read-path soberano (D7), selo WORM de leitura sensível no SSE
      (D6), DSAR/crypto-shredding (ADR-011) — AOS-172. (Não gated por D4.)
- [ ] **Empacotamento deployável**: contentor distroless/non-root/read-only, com **ADR-017**
      (supply-chain: binário zero-dep, SBOM+proveniência, gates na entrega) — AOS-168.
- [ ] **Aceitação sistémica NÃO-vacuosa** (E6): e2e com um **modelo que EMITE tool calls** (prova o
      caminho PERMITIDO, não só a negação), **contentor real** com kill+reinício sem duplicação,
      bateria de abuso HTTP, e um **checklist NOMEADO de cada critério `00_System_Spec.md §13`**
      (verde ou deferido-com-eixo) — AOS-169 (reescrito).

## 3. Tabela Resumo de Tickets

| ID | Título | Tipo | Estimativa | Prioridade | Dependências |
|---|---|---|---|---|---|
| AOS-163 | Bootstrap do nó `aos`: composição de PRODUÇÃO + verifier da autoridade AOS-156 + config | feature | M | P0 | EPIC-14 (PR-0.c), **AOS-156**, AOS-130 |
| AOS-164a | Loop de serviço do nó (long-running, registo de runs, isolamento de falha por-run) | feature | M | P0 | AOS-163 |
| AOS-164b | Shutdown gracioso durável + fronteira nó↔ORQ/SCH (EPIC-03) + single-writer do WORM sob N runs | feature | M | P0 | AOS-164a, AOS-170 |
| AOS-170 | **Substrato durável** (E4): Event Store/WORM/KeySource persistentes (não in-memory) | feature | M | P0 | AOS-163, EPIC-02 |
| AOS-165 | CLI `aos` (`serve`/`run`/`observe`/`steer`) — só stdlib | feature | S | P1 | AOS-164a |
| AOS-166 | API HTTP stdlib: submeter *goal* + controlo **AUTENTICADO** (steer/approve); bind não-loopback recusado sem authn | feature | M | P1 | AOS-164a, **AOS-160** |
| AOS-167 | SSE de trajectória: `StateProjector`→SSE + backfill + resume-from-seq + dedup **+ backpressure** (D3), streaming por seq | feature | **L** | P1 | AOS-166 |
| AOS-171 | **Health/readiness** (E5): `/healthz`·`/readyz` (bloqueia o Dockerfile) | feature | S | P1 | AOS-164a |
| AOS-172 | **Soberania/conformidade** (E7): read-path soberano (D7) + selo WORM de leitura no SSE (D6) + DSAR/crypto-shredding | feature | M | P1 | AOS-167, AOS-170, EPIC-09/10 |
| AOS-173 | **Observabilidade** (E7): `otel-genai` (spans+custo) + WORM ligados por OTLP | feature | M | P1 | AOS-163 |
| AOS-168 | Empacotamento do nó: distroless/non-root/read-only + **ADR-017** (SBOM+proveniência) | feature | S | P1 | AOS-164a, AOS-171, EPIC-10 |
| AOS-169 | Aceitação sistémica **não-vacuosa** (E6): modelo que emite tools + caminho permitido + contentor real + abuso HTTP + checklist §13 nomeado | chore | M | P0 | AOS-166, AOS-167, AOS-172 |

Estimativas XS/S/M/L (XL proibido). Prioridades P0/P1/P2. Toda a Fase 5 — Operacionalização.
**Pré-requisito: AOS-156** (identidade real). Fases locais: **núcleo do nó** (AOS-163/164a/164b/170,
P0), **interface** (AOS-165/166/167/171), **conformidade/observabilidade** (AOS-172/173),
**entrega** (AOS-168/169). A web é D1(b) (fora do caminho crítico). O single-host/sem-HA da v1 é
**non-goal datado** (Carta §7 emenda 1.2; distribuído = EPIC-10).

---

## Revisão pós-painel `wamnbffrk` (emenda 2026-07-22)

Esta epic foi revista após o painel adversarial (veredicto `ratificar-com-emendas`) e a decisão do
dono (Carta emenda 1.1: **desbloquear o D4 primeiro**). As secções por-ticket abaixo (AOS-163–169)
mantêm a redacção original; **prevalece esta revisão** onde divergirem. Tickets novos/divididos
(AOS-164a/b, 170, 171, 172, 173) têm aqui o seu escopo; a secção de 9 campos de cada um é expandida
quando for executado.

**Re-sequência.** A EPIC-15 depende agora de **AOS-156** (identidade real, autoridade self-hosted
Nível 2). O nó da v1 usa o verifier com trust-anchor = pubkey do issuer — **não** o modo demo-only.
Isto fecha na raiz o achado ALTO "forma sobre-reivindicada" e habilita a autenticação do canal
externo (achados ALTO nº4/nº5).

**E4 — Durabilidade (AOS-170, P0).** *Contexto:* o achado ALTO "nó vs biblioteca" — a capacidade
que distingue um nó de uma lib (ciclo de vida durável) ficava por ligar sobre um Event Store
in-memory. *Objectivo:* Event Store/WORM/KeySource **persistentes**. *AC:* o reinício do nó não
perde nem duplica trabalho durável (idempotência/replay, EPIC-02); a tamper-evidence do WORM e o
KeySource **sobrevivem ao restart** (não reiniciam a cada arranque).

**E8 — Divisão de AOS-164 + fronteiras (AOS-164a/164b, P0).** O AOS-164 original (M) juntava 4
subsistemas — XL disfarçado. Divisão: **164a** = loop de serviço (hospedar runs, isolamento de
falha por-run); **164b** = shutdown gracioso **durável** (sobre AOS-170) + **fronteira nó↔ORQ/SCH**
(desenhar e registar como o loop consome/substitui o Orquestrador+Escalonador de EPIC-03, evitando
duas fontes de verdade do ciclo de vida) + **CA de single-writer** do WORM sob N runs concorrentes
(serialização/ordenação do hash-chain com dono).

**E5 — Canal autenticado + health (AOS-166 amendado, AOS-171 novo).** *AOS-166:* o canal de
controlo (steer/approve) é **autenticado server-side** (não um POST anónimo — o achado ALTO nº4: a
inércia do D4 não protege pause/steer/terminate); depende de **AOS-160** (Authenticator ed25519,
desbloqueado pelo D4); **bind não-loopback recusado** enquanto a autenticação não estiver ligada;
admission/rate-limit no `POST /runs`; teste NEGATIVO (steer não-autenticado é recusado). *AOS-171
(S, P1):* `/healthz`·`/readyz` — um contentor distroless sem probe não é operável; **bloqueia o
Dockerfile (AOS-168)**.

**E7 — Soberania/observabilidade (AOS-172, AOS-173 novos).** *AOS-172 (M, P1):* read-path soberano
fail-closed (D7); selo WORM de leitura sensível no stream SSE (D6, hoje silencioso); DSAR/
crypto-shredding (Art.17, ADR-011). **Não gated por D4.** *AOS-173 (M, P1):* ligar `otel-genai`
(spans + custo) e o WORM por **OTLP** — o Spec §13 (Observabilidade) não é verde por omissão.

**E6 — Aceitação não-vacuosa (AOS-169 reescrito).** O e2e original (in-process, modelo fake que nem
chama tools) é o "funciona-na-demo" que o System Spec §2.1 desqualifica. Reescrita: **modelo que
EMITE tool calls** (prova o caminho PERMITIDO, não só a negação); **contentor real** exercitado com
kill+reinício **sem duplicação**; **bateria de abuso HTTP** (payloads gigantes, enumeração de
RunID, replay, steer não-autenticado); **checklist NOMEADO de cada critério §13** (verde ou
deferido-com-eixo — o deferimento restrito ao eixo identidade, não a durabilidade/observabilidade).

**E10 — SSE repromovido (AOS-167 → L).** Não há handler SSE no repo (é de raiz, não "reutiliza
AOS-133–136"); acresce **backpressure/drop-slow-consumer** e **streaming por seq** (não snapshots
de state). D3/D5 são reavaliados para o modelo de ameaça do nó-serviço (Carta §4.2, emenda 1.2) —
sob single-process, a separação "dois canais" é de protocolo/taint, não física.

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

> **REVISTO (emenda pós-painel, ver §Revisão):** o nó da v1 usa **identidade REAL** (AOS-156),
> não demo-only. Onde o texto abaixo diz "demo-only", prevalece a revisão.

### Contexto

O `cmd/aos-demo` compõe o RM com **stubs neutros** (aceitável no ápice mínimo). O nó de produção
tem de compor a **cadeia REAL** via `integration.NewSecuredRuntime` (RM de produção, WORM único) —
e, por decisão do dono (Carta emenda 1.1), com o **verifier da autoridade de identidade AOS-156**
(trust-anchor = pubkey do issuer), não o self-mint.

### Objectivo

Um pacote de bootstrap (`packages/cmd/aos`) que constrói o runtime seguro de produção a partir de
uma configuração mínima e explícita, fail-closed, ligando o **verifier real de AOS-156**.

### Critérios de Aceitação

- [x] O nó compõe via `integration.NewSecuredRuntime` (não os stubs do demo); um colaborador
      obrigatório em falta ABORTA o arranque (fail-closed). *(bootstrap.go passo 7 via
      `NewProductionSecure`; fail-closed coberto por main_test/bootstrap_test.)*
- [ ] Configuração mínima e explícita (WORM store durável, cliente de modelo, **trust-anchor do
      issuer AOS-156**, portas) — sem segredos em código (chaves/tokens vêm de config/vault).
      *(trust-anchor `Config.IssuerPubKey`/`AOS_ISSUER_PUBKEY` + "sem segredos em código"
      SATISFEITOS; **WORM store durável DEFERIDO para AOS-170** — seam `cfg.WORM`/`cfg.EventStore`
      injectável já existe e o substrato é declarado no banner, mas o default in-memory não
      satisfaz a letra "durável" ⇒ AC fica aberto.)*
- [x] O nó liga o **verifier real** (pubkey da autoridade AOS-156); a chave de assinatura NUNCA
      entra no runtime do nó (não-forjabilidade relativa ao nó). O modo de identidade em vigor é
      **declarado no arranque e nos logs** (real vs, se alguma vez usado, um modo de teste explícito).
      *(MODO ENDURECIDO trust-anchor-only: `IssuerPubKey` compõe só o verifier, `Node.Authority==nil`,
      `ErrConflictingIssuerKey` fail-closed; banner declara os dois modos. Provado por
      TestNodeTrustAnchorOnlyHasNoAuthorityInProcess/TestBootstrapHardenedRejectsSigningKey.)*

### Detalhes Técnicos

- Reutiliza a composição de AOS-130 como referência, trocando os stubs pela via `NewSecuredRuntime`.

### Testes Requeridos

- Teste de composição de produção (fail-closed sem colaborador); teste de que o modo demo-only é declarado.

### Definition of Done

- [x] Bootstrap de produção compõe e falha-fechado; modo de identidade declarado; sem segredos.
      *(cadeia real via `NewProductionSecure`, fail-closed, modo declarado no banner/logs, secrets
      gate verde. Nota: substrato WORM durável deferido para AOS-170 — ver AC2.)*

### Handoff para Claude Code

```text
Ticket AOS-163 (AOS). Bootstrap do no `aos`: compoe integration.NewSecuredRuntime (RM producao,
WORM unico), config minima explicita (sem segredos em codigo), modo de identidade DEMO-ONLY
self-minted DECLARADO no arranque/logs (D4 aberta, Carta §4.2). Fail-closed sem colaborador.
```

---

## AOS-164 — Loop de serviço e ciclo de vida do nó

> **REVISTO (E8, ver §Revisão):** DIVIDIDO em **164a** (loop de serviço + isolamento de falha
> por-run) e **164b** (shutdown durável sobre AOS-170 + fronteira nó↔ORQ/SCH de EPIC-03 + CA de
> single-writer do WORM sob N runs). A redacção abaixo cobre o âmbito conjunto.

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

- [x] O nó aceita e hospeda múltiplos *runs* (registo de runs em curso por RunID). *(AOS-164a:
      `Service.Submit`/`hostRun` regista runs por RunID sob `s.mu`, guarda de duplicação consulta
      `runs`+`completed`, lease por-run com heartbeat de posse; concorrência verificada.)*
- [ ] Shutdown gracioso: um sinal de paragem drena/persiste o estado durável (sem efeitos perdidos
      nem duplicados no reinício — a durabilidade é EPIC-02). *(AOS-164a: drain in-process
      cooperativo — `Shutdown` sinaliza + aguarda os runs, nunca mata cego. **Metade durável
      (persistência do estado de shutdown + replay idempotente no reinício ao nível do binário)
      DEFERIDA para AOS-164b sobre o substrato durável AOS-170.**)*
- [x] O nó é resiliente a um run que falha (um run não derruba o nó; fail-closed por-run). *(AOS-164a:
      isolamento de panic por-run via `recover` no defer de `hostRun`; o nó sobrevive; verificado.)*

### Detalhes Técnicos

- Concorrência por-run; o Event Store é o estado partilhado (o nó é stateless sobre ele).

### Testes Requeridos

- Teste de hospedagem de múltiplos runs; teste de shutdown gracioso; teste de isolamento de falha por-run.

### Definition of Done

- [x] Loop de serviço hospeda runs, shutdown gracioso, falha por-run isolada; sem segredos. *(AOS-164a:
      `packages/cmd/aos/service.go` — loop de serviço long-running com registo/isolamento de falha
      por-run, drain gracioso in-process, heartbeat de posse do lease (fecha a janela de
      dupla-execução), retenção FIFO limitada de `completed`, guarda de re-submissão de RunID
      terminado; gate `go test -race` verde; sem segredos. **Shutdown DURÁVEL + fronteira nó↔ORQ/SCH
      + single-writer do WORM sob N runs DEFERIDOS para AOS-164b/AOS-170.** Wiring `serve` ao
      binário deferido para AOS-165; API HTTP para AOS-166.)*

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

> **REVISTO (E5, ver §Revisão):** o canal de controlo (steer/approve) é **autenticado server-side**
> (depende de AOS-160); bind não-loopback recusado sem authn; admission/rate-limit no `POST /runs`;
> teste NEGATIVO de steer não-autenticado. A redacção abaixo é a original.

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

> **REVISTO (E10, ver §Revisão):** repromovido **M → L** — não há handler SSE no repo (é de raiz);
> acresce **backpressure/drop-slow-consumer** e **streaming por seq** (não snapshots de state). A
> redacção abaixo é a original.

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

> **REVISTO (E6, ver §Revisão):** e2e **não-vacuoso** — modelo que EMITE tool calls (prova o
> caminho PERMITIDO, não só a negação), contentor REAL com kill+reinício sem duplicação, bateria de
> abuso HTTP, e checklist NOMEADO de cada critério §13 (deferimento restrito ao eixo identidade). A
> redacção abaixo é a original.

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
| 1.1 | 2026-07-22 | **REVISÃO pós-painel `wamnbffrk` (Passo 3 do roadmap; Carta emenda 1.1/1.2).** Pré-requisito **AOS-156** (identidade REAL — o nó deixa o modo demo-only). Enactados E4–E8/E10: **AOS-170** (substrato durável), **AOS-164a/164b** (divisão do loop + fronteira ORQ/SCH + single-writer do WORM), **AOS-166** (canal de controlo autenticado, dep. AOS-160), **AOS-171** (health/readiness), **AOS-172** (soberania: D6/D7/DSAR), **AOS-173** (observabilidade OTLP), **AOS-167→L** (backpressure/streaming por seq), **AOS-169 reescrito** (modelo que emite tools + contentor real + abuso HTTP + checklist §13 nomeado), **AOS-168** ganha ADR-017. Critérios de saída (§2) e tabela (§3) revistos; ver a secção **Revisão**. | — |
