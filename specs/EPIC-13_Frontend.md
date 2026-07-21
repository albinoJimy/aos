# EPIC-13 — Camada Frontend do AOS (Operacionalização da Superfície Humana)

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — Camada Frontend (operacionalização da superfície humana) |
| Versão | 1.0 |
| Data | Julho de 2026 |
| Classificação | Documento de Referência — Aberto |
| Documento-fonte | `_FONTE_agentic-os-ideal.md`; painel adversarial `w3uijpkjy` (proposta) avaliado e desafiado por `wsuca4fcl` |
| Documentos relacionados | `specs/EPIC-14_Integracao_Composition_Root.md` (PR-0, predecessor), `specs/EPIC-12_Experiencia_HITL_UX.md`, `docs/adr/ADR-006-credential-broker-jit.md`, `docs/adr/ADR-011-policy-as-code-gdpr.md`, `docs/adr/ADR-012-semver-eval-gate.md`, `docs/adr/ADR-013-gates-risco-sa-roc.md`, `docs/adr/ADR-016-fronteira-confianca-ui.md`, `specs/01_Engineering_Standards_e_Handoff.md` |

---

## 1. Visão do Epic

Onde o EPIC-12 entregou os **modelos e contratos Go** da superfície de controlo humano (approval-card, plan-card, control-surface, autonomia, trajectória, progresso), este epic **operacionaliza-os** — transforma as *ilhas* não-ligadas numa superfície que um humano real consegue **ver, dirigir e aprovar**, com as garantias de segurança e governança a sobreviverem até ao ponto de decisão.

O epic **não reimplementa** nenhum enforcement. A máquina de estados (`paused`/`waiting_on_human`, AOS-023), a assinatura de aprovações (AOS-095), a ratificação (AOS-096), o tiering de risco (AOS-074), a taxonomia L0–L5 (AOS-089/090), a soberania (AOS-097/098, ADR-011) e o taint (reference-monitor, ADR-005) já existem e são impostos noutros epics. Aqui costura-se uma *fronteira de confiança de rede* (o BFF) e, condicionalmente, uma *superfície de apresentação* que os consome sem os enfraquecer. **Regra de ouro (ADR-016):** a superfície apresenta, nunca é autora do enforcement; o BFF **nunca assina** em nome do humano (a chave de decisão humana nunca vive no cliente nem no servidor); o que o humano vê é o que a assinatura cobre (WYSIWYS); o canal de controlo (trusted) fica separado do canal de dados (untrusted).

Este epic pertence à **Fase 5 — Operacionalização** e está **subordinado** à dívida de backend PR-0, que é materializada e possuída pela **`specs/EPIC-14_Integracao_Composition_Root.md`** (composition-root, dono: plataforma/kernel-runtime). Enquanto o estrato relevante de PR-0/EPIC-14 não fechar, os tickets que dele dependem não arrancam — mas AOS-129 (feito), a parte de assinatura de AOS-131 e a parte de design de AOS-132 arrancam sem gate.

**Advertências estruturais assumidas como risco (ratificadas a olhos abertos):** (W1) o bloqueador duro é a dívida de wiring de backend (EPIC-14), não o frontend; (W2) a superfície web é **condicional** — o `desktop` já modelado (dual-control, sem XSS) e a observabilidade-commodity (Grafana/Tempo/Jaeger) cobrem grande parte do valor sem um frontend web bespoke; a correcção factual é que esse `desktop` é um `Renderer` **modelado/inerte, por-ligar**, não pronto; (W3) um frontend web quebra o invariante zero-dep/offline/determinista e abre uma 2.ª supply-chain — qualquer dependência JS/TS ou WebSocket externa é excepção ratificável caso-a-caso (gate ADR-012/AOS-096), nunca um default.

**Decisões ratificadas com condicionalidade** (aparecem como condição nos tickets afectados): D1(b) web SPA bespoke — **condicional** a existirem utilizadores reais + TCO de um tier de ingress + dono da 2.ª supply-chain; D2 stack no-build HTMX/`go:embed` — **fixado**; D3 transporte SSE stdlib — **fixado**; D4 custódia WebAuthn non-extractable + BFF non-signing + 4-eyes atestado — invariante **fixada**, autoridade de identidade **condicional** (sem IdP/binding humano↔NHI, ver EPIC-14/AOS-156); D5 BFF single-process — postura **fixada**, gatilho de graduação **condicional** a SLO/utilizadores reais; D6 auditoria de leitura sensível — **fixado**; D7 read-path soberano fail-closed — regra **fixada**, topologia operacional **condicional** a regiões/boards reais.

---

## 2. Critérios de Saída do Epic

- [ ] **(Pré — PR-0/EPIC-14)** Existe uma composition-root que compõe Event Store + registry de `state.Machine` por-run + `SteerChannel(Authenticator)` + `StateProjector` + broker + superfícies (predecessor externo, dono plataforma/kernel-runtime).
- [x] **(AOS-129)** Os ADRs 006/011/012/013 existem em `docs/adr` e há um ADR-016 de UI-governança (6 invariantes) + índice `docs/adr/README.md`.
- [ ] A **fronteira de assinatura** está fixada: o BFF é *non-signing* para ambas as chaves; a assinatura de decisão humana vem do autenticador de hardware (WebAuthn non-extractable); o broker-JIT (ADR-006) mantém-se só para credenciais downstream da NHI.
- [ ] **WYSIWYS**: a assinatura cobre um digest canónico determinista do efeito exibido (`Preview‖Capability‖Resource‖Class‖Irreversible`); o verificador recomputa a partir do que vai executar e recusa em *mismatch*; o mesmo digest é o *challenge* WebAuthn.
- [ ] O tempo-real está **correcto a frio e resumível** (backfill `Read(fromSeq)`, resume-from-seq por `Last-Event-ID`, dedup por seq, backpressure limitado por-ligação).
- [ ] **(Se web — condicional, D1(b))** saída untrusted como texto inerte com CSP estrita; canal de controlo separado do de dados; 4-eyes por *attestation* de credenciais distintas; a11y WCAG 2.2 AA; read-path soberano.
- [ ] Todos os tickets com DoD de domínio verde (`specs/01_Engineering_Standards_e_Handoff.md`); sem segredos; PII redigida; acessibilidade quando aplicável.

---

## 3. Tabela Resumo de Tickets

| ID | Título | Tipo | Estimativa | Prioridade | Dependências |
|---|---|---|---|---|---|
| AOS-129 | ADRs em falta (006/011/012/013) + ADR-016 de UI-governança + índice `docs/adr` | chore | S | P0 | — |
| AOS-130 | Demonstrador single-process = superfície canónica de referência | spike | S | P0 | EPIC-14 (PR-0.b) |
| AOS-131 | WYSIWYS: assinatura cobre o efeito exibido (digest = challenge WebAuthn) | feature | M | P1 | EPIC-09 (AOS-095), AOS-120 |
| AOS-132 | Fronteira de assinatura *non-signing* + hook de identidade humano↔NHI | feature | L | P1 | ADR-006, ADR-016, EPIC-14 (AOS-152/156) |
| AOS-133 | BFF Go stdlib (JSON+SSE): ponte `StateProjector`→SSE + POST de controlo | feature | L | P1 | EPIC-14 (PR-0.b), AOS-132 |
| AOS-134 | Tempo-real correcto: backfill + resume-from-seq + dedup por seq | feature | M | P1 | AOS-133 |
| AOS-135 | Backpressure/fan-out: buffer por-ligação, LWW, drop-slow-consumer | feature | M | P1 | AOS-133 |
| AOS-136 | Streaming de spans + burn-down incremental (watermark as-of-seq) | feature | M | P2 | AOS-133, EPIC-08 (AOS-077) |
| AOS-137 | *(web)* Sanitização de output + CSP estrita + separação de canais | feature | M | P1 | AOS-133 |
| AOS-138 | *(web)* 4-eyes real: 2 credenciais WebAuthn atestadas + challenge por-perna | feature | L | P1 | AOS-132, AOS-137 |
| AOS-139 | *(web)* ApprovalCard v2 estruturado/diffável + anti-fadiga + calibração | feature | L | P2 | AOS-131, AOS-133 |
| AOS-140 | *(web)* a11y WCAG 2.2 AA + extracção i18n (PT-PT default) | feature | M | P2 | AOS-139 |
| AOS-141 | *(web)* Read-path soberano + audit WORM de leituras + override-rate por-aprovador | feature | M | P1 | AOS-133, EPIC-09/10 |
| AOS-142 | *(web)* Dockerfile do BFF (distroless/non-root/read-only) + módulo edge/DMZ | feature | L | P1 | AOS-133, EPIC-10 |
| AOS-143 | Testes de frontend/DX: transporte, idempotência por RequestID, a11y, CSP | chore | S | P2 | AOS-133…142 |

Estimativas XS/S/M/L (XL proibido). Prioridades P0/P1/P2. Toda a Fase 5. Fases locais: fundações de confiança (AOS-129–132, qualquer forma), BFF+tempo-real (AOS-133–136), superfície web condicional a D1(b) (AOS-137–143). O predecessor PR-0 (composition-root) é a `specs/EPIC-14_Integracao_Composition_Root.md`.

---

## AOS-129 — ADRs em falta (006/011/012/013) + ADR-016 de UI-governança + índice `docs/adr`

| Campo | Valor |
|---|---|
| Epic | EPIC-13 — Camada Frontend |
| Fase | 5 — Operacionalização |
| Tipo | chore |
| Prioridade | P0 |
| Estimativa | S |
| Dependências | — |
| Bloqueia | AOS-131, AOS-132 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `_BRIEF.md` (catálogo de ADRs), `specs/00_System_Spec.md` §11 |

### Contexto

O código de governança cita ADR-006/011/012/013 como fonte-de-verdade, mas não estavam materializados: no trunk, `docs/adr` continha apenas `ADR-015-durable-execution.md`, e os ADRs 001–014 eram um catálogo de uma linha em `_BRIEF.md`/`00_System_Spec.md` §11. **Feito nesta ronda.**

### Objectivo

Materializar os ADRs citados (fiéis ao catálogo e ao código que os implementa) e emitir o ADR-016 de UI-governança + índice.

### Critérios de Aceitação

- [x] `docs/adr/ADR-006-credential-broker-jit.md`, `ADR-011-policy-as-code-gdpr.md`, `ADR-012-semver-eval-gate.md`, `ADR-013-gates-risco-sa-roc.md` materializados e consistentes com EPIC-06/08/09/12.
- [x] `docs/adr/ADR-016-fronteira-confianca-ui.md` — 6 invariantes de segurança da UI (custódia nunca no cliente + BFF non-signing; WYSIWYS; deadline server-side; 4-eyes atestado; read-path soberano + audit de leitura; separação controlo/dados).
- [x] Índice `docs/adr/README.md` que reconcilia o registo de ADRs — junta os novos 006/011/012/013/016 ao `ADR-015-durable-execution.md` pré-existente no trunk — com o catálogo de `_BRIEF`/`00_System_Spec.md` §11.

### Detalhes Técnicos

- ADRs em formato-padrão (Contexto/Decisão/Consequências/Alternativas/Conformidade/Referências), PT-PT.
- A recomendação de auditoria de um ADR de supply-chain (informal) fica reservada como ADR-017.

### Testes Requeridos

- Verificação de que todas as referências cruzadas entre EPIC-13 e `docs/adr` resolvem.

### Definition of Done

- [x] Cinco ADRs + índice materializados; referências consistentes; sem segredos/PII/nomes de pessoas.

### Handoff para Claude Code

```text
Ticket AOS-129 (AOS). Materializa docs/adr/ADR-006/011/012/013 (fieis ao catalogo de _BRIEF/
00_System_Spec e ao codigo) + ADR-016 (6 invariantes da fronteira de confianca da UI) + indice
README.md que reconcilia o registo de ADRs (junta 006/011/012/013/016 ao ADR-015
pre-existente no trunk). Formato ADR PT-PT. Verifica que
todas as referencias cruzadas resolvem. Sem segredos/PII/nomes.
```

---

## AOS-130 — Demonstrador single-process = superfície canónica de referência

| Campo | Valor |
|---|---|
| Epic | EPIC-13 — Camada Frontend |
| Fase | 5 — Operacionalização |
| Tipo | spike |
| Prioridade | P0 |
| Estimativa | S |
| Dependências | EPIC-14 (PR-0.b, AOS-149) |
| Bloqueia | AOS-133 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `specs/EPIC-14_Integracao_Composition_Root.md` (AOS-149) |

### Contexto

A forma da superfície era especulativa sem um artefacto que exercite os contratos end-to-end. Este ticket realiza-se via o `cmd/aos-demo` da EPIC-14 (AOS-149); é a superfície canónica de referência ratificada (D1(b/c)), não um spike descartável.

### Objectivo

Um demonstrador single-process (zero rede) que costura o apex mínimo e conduz spawn→plan-approval→steer→approve(dual-control)→trajectória pela interface `Renderer`.

### Critérios de Aceitação

- [ ] Fluxo completo corre in-process, zero deps externas.
- [ ] Alimenta D1/D2/D3 com evidência.
- [ ] **Prova ou refuta** se o apex mínimo (PR-0.b) chega (AC4 refutável — ver EPIC-14/AOS-151).

### Detalhes Técnicos

- Realizado pela EPIC-14 (AOS-149/150/151); aqui é a lente frontend.

### Testes Requeridos

- Teste e2e in-process; AC4 refutável.

### Definition of Done

- [ ] Demonstrador corre; evidência para D1/D2/D3; AC4 não-vacuous.

### Handoff para Claude Code

```text
Ticket AOS-130 (AOS). Realiza-se via EPIC-14/AOS-149 (cmd/aos-demo single-process, zero rede):
spawn->plan-approval->steer->approve(dual-control)->trajectoria pela interface Renderer. Prova/
refuta se o apex minimo chega (AC4 refutavel, EPIC-14/AOS-151). Alimenta as decisoes D1/D2/D3
com evidencia. NAO escrevas codigo de UI web aqui.
```

---

## AOS-131 — WYSIWYS: assinatura cobre o efeito exibido

| Campo | Valor |
|---|---|
| Epic | EPIC-13 — Camada Frontend |
| Fase | 5 — Operacionalização |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | EPIC-09 (AOS-095), AOS-120 |
| Bloqueia | AOS-139 |
| Responsável sugerido | Responsável de Segurança |
| Documentos de referência | `hitl/approval.go` (`canonicalApproval`), ADR-016 §2 |

### Contexto

`canonicalApproval` assina `domínio‖requestID‖approver‖…` mas **não** o Preview/Capability/Resource — vulnerável a *preview-swap* (o humano vê X, assina Y). A parte de assinatura arranca já (sem gate PR-0).

### Objectivo

Estender `canonicalApproval` para cobrir um digest canónico determinista do efeito exibido.

### Critérios de Aceitação

- [ ] Representação canónica estável (paridade entre superfícies).
- [ ] O verificador **recomputa** o digest a partir do que vai executar e recusa em *mismatch*.
- [ ] O mesmo digest é o *challenge* WebAuthn assinado pelo hardware (WYSIWYS até ao secure element).
- [ ] O audit sela o digest do que foi renderizado.

### Detalhes Técnicos

- A base determinista length-prefixed já existe (`hitl/encode.go`); os campos do efeito já estão em `hitl.Presentation`.

### Testes Requeridos

- Teste de digest determinista; teste de recusa em preview-swap; teste de selagem.

### Definition of Done

- [ ] WYSIWYS provado end-to-end; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-131 (AOS). Estende hitl.canonicalApproval para cobrir um digest canonico
determinista do efeito EXIBIDO (Preview||Capability||Resource||Class||Irreversible). O
verificador recomputa o digest do que VAI executar e recusa em mismatch; o mesmo digest e o
challenge WebAuthn. Sela o digest no audit. A base length-prefixed ja existe (hitl/encode.go).
Testa preview-swap.
```

---

## AOS-132 — Fronteira de assinatura *non-signing* + hook de identidade humano↔NHI

| Campo | Valor |
|---|---|
| Epic | EPIC-13 — Camada Frontend |
| Fase | 5 — Operacionalização |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | ADR-006, ADR-016, EPIC-14 (AOS-152/156) |
| Bloqueia | AOS-138 |
| Responsável sugerido | Responsável de Segurança |
| Documentos de referência | ADR-006, ADR-016 §1/§4, EPIC-14 (AOS-152/156) |

### Contexto

Um BFF que assine em nome do humano destrói o modelo. A identidade humano↔NHI está diferida (`IdentityStub`, AOS-005). O design/ADR arranca já; o runtime é gated no hook de identidade (EPIC-14/AOS-152 kernel + AOS-156 espinha de token) e a **autoridade de identidade fica condicional (D4)**.

### Objectivo

Fixar o BFF como non-signing para ambas as chaves: a assinatura de decisão humana vem do autenticador de hardware (WebAuthn non-extractable); o broker-JIT (ADR-006) mantém-se só para credenciais downstream da NHI.

### Critérios de Aceitação

- [ ] BFF nunca detém chave de decisão humana nem assina.
- [ ] Binding humano↔NHI via IdP com TTL definido — **condicional a D4** (autoridade de identidade).
- [ ] Anti-replay: challenge por-perna emitido pelo servidor; seen-set durável.

### Detalhes Técnicos

- O runtime depende de EPIC-14/AOS-152 (kernel `Call.Credential`) e AOS-156 (espinha de token). Enquanto D4 aberta, identidade *demo-only self-minted*.

### Testes Requeridos

- Teste de que o BFF não assina; teste de challenge por-perna; teste de anti-replay.

### Definition of Done

- [ ] Fronteira non-signing fixada; autoridade marcada condicional D4; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-132 (AOS). Fixa o BFF como NON-SIGNING para ambas as chaves: a assinatura de decisao
humana vem do autenticador de hardware (WebAuthn non-extractable), NUNCA de JS a chamar o
broker; o broker-JIT (ADR-006) fica so para credenciais downstream da NHI. Design/ADR arranca
ja; o runtime e gated em EPIC-14/AOS-152 (kernel Call.Credential) + AOS-156 (espinha de token).
A autoridade de identidade fica CONDICIONAL a D4. Ate la, demo-only self-minted.
```

---

## AOS-133 — BFF Go stdlib (JSON+SSE)

| Campo | Valor |
|---|---|
| Epic | EPIC-13 — Camada Frontend |
| Fase | 5 — Operacionalização |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | EPIC-14 (PR-0.b), AOS-132 |
| Bloqueia | AOS-134, AOS-135, AOS-136, AOS-137, AOS-139, AOS-141, AOS-142, AOS-143 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `control-surface/reflection.go`, ADR-005/016 |

### Contexto

Não há transporte de rede; `StateProjector` faz fan-out in-process. O transporte é SSE stdlib (D3 fixado); rejeitam-se gRPC-web/GraphQL/WebSocket (deps externas, cegam o SCA, re-fundem dados+controlo).

### Objectivo

Módulo BFF (`net/http`, JSON+SSE): ponte `StateProjector.Observe`→SSE + endpoints POST para steer/approve/plan-decision/authoring.

### Critérios de Aceitação

- [ ] Zero deps externas.
- [ ] `GET /contracts` publica as 3 versões SemVer; versionamento *pass-through*.
- [ ] Registry de `StateProjector` por-runID com refcount+GC.
- [ ] O BFF depende **só** de `eventstore.EventStore(Read+Subscribe)` e de `StateProjector`, nunca de internals de `*Store` (costura de graduação — D5).

### Detalhes Técnicos

- Canal de dados = SSE untrusted; canal de controlo = POST trusted (separação ADR-005).

### Testes Requeridos

- Teste de contrato de transporte; teste de refcount/GC; teste de rejeição de dep externa (build sem deps).

### Definition of Done

- [ ] BFF stdlib zero-dep; separação de canais; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-133 (AOS). Cria o BFF em Go stdlib (net/http, JSON+SSE): ponte StateProjector.Observe
->SSE (canal de dados untrusted) + POST para steer/approve/plan-decision/authoring (canal de
controlo trusted). Rejeita gRPC-web/GraphQL/WebSocket. GET /contracts publica as 3 versoes
SemVer (pass-through). Registry de StateProjector por-runID com refcount+GC. Depende SO de
eventstore.EventStore(Read+Subscribe) e StateProjector, nunca de internals de *Store.
```

---

## AOS-134 — Tempo-real correcto: backfill + resume-from-seq + dedup por seq

| Campo | Valor |
|---|---|
| Epic | EPIC-13 — Camada Frontend |
| Fase | 5 — Operacionalização |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-133 |
| Bloqueia | AOS-136 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `control-surface/reflection.go`, `eventstore/store.go` (`Read`) |

### Contexto

`StateProjector` assume `Ready` e nunca lê backlog (estado errado a frio); `Subscribe` só entrega futuro (reconnect perde transições). Aplica-se a **qualquer** superfície (desktop **e** web). O primitivo `Read` já existe. É o mesmo trabalho que EPIC-14/AOS-150.

### Objectivo

Costurar atomicamente backfill `Read(fromSeq)` com `Subscribe` sob watermark de seq; resume por `Last-Event-ID`.

### Critérios de Aceitação

- [ ] Um cliente que liga a um run `paused` **vê `paused`**.
- [ ] Reconnect não perde nem duplica; cursor de seq exposto (id do SSE).
- [ ] Comentário falso em `reflection.go` corrigido.

### Detalhes Técnicos

- Não afirmar *exactly-once* inter-restart (herda a condicionalidade de D5 single-process).

### Testes Requeridos

- Teste de cold-start; teste de resume/dedup.

### Definition of Done

- [ ] Read-model verídico e resumível; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-134 (AOS). Faz NewStateProjector costurar atomicamente backfill Read(runID,fromSeq)
+ Subscribe sob watermark de seq (dedup na sobreposicao); o seq vira o id: do SSE, resume por
Last-Event-ID. Um cliente que liga a um run paused TEM de ver paused. Corrige o comentario falso
em reflection.go. Nao afirmes exactly-once inter-restart (D5). Mesmo trabalho que EPIC-14/AOS-150.
```

---

## AOS-135 — Backpressure / fan-out

| Campo | Valor |
|---|---|
| Epic | EPIC-13 — Camada Frontend |
| Fase | 5 — Operacionalização |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-133 |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `eventstore/subscribe.go`, `control-surface/reflection.go` |

### Contexto

Fan-out síncrono + fila ilimitada: um SSE lento causa OOM e trava a projecção de todos os canais.

### Objectivo

Desacoplar a escrita de rede da goroutine do projector; cada ligação SSE tem ring buffer limitado, estado usa last-writer-wins, ticks de burn-down coalescem, cliente lento é dropado.

### Critérios de Aceitação

- [ ] Cliente lento não afecta os outros.
- [ ] Memória limitada por ligação.
- [ ] Política de slow-consumer (drop) testada.

### Detalhes Técnicos

- A subscrição do eventstore é envolvida/limitada para a sua FIFO ilimitada não crescer atrás de um handler travado.

### Testes Requeridos

- Teste de cliente lento isolado; teste de limite de memória; teste de drop.

### Definition of Done

- [ ] Backpressure provado; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-135 (AOS). Desacopla a escrita de rede da goroutine do projector: cada ligacao SSE
com ring buffer limitado; estado = last-writer-wins; ticks de burn-down coalescem/dropam;
cliente lento/preso e DROPADO (fecha SSE) apos overflow, nunca bloqueia os outros. Envolve a
subscricao do eventstore para a FIFO ilimitada nao crescer atras de um handler travado. Testa.
```

---

## AOS-136 — Streaming de spans + burn-down incremental

| Campo | Valor |
|---|---|
| Epic | EPIC-13 — Camada Frontend |
| Fase | 5 — Operacionalização |
| Tipo | feature |
| Prioridade | P2 |
| Dependências | AOS-133, EPIC-08 (AOS-077) |
| Estimativa | M |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `progress-surface/burndown.go`, `otel-genai/cost_aggregation.go` |

### Contexto

`ComputeBurndown`/`BuildTree` recomputam O(n) por tick; o Exporter não tem tail/subscribe.

### Objectivo

Decorador de `Exporter` com fan-out por-run/trace; agregador incremental; unificar a watermark "as-of seq".

### Critérios de Aceitação

- [ ] Burn-down actualiza sem re-agregar o slice inteiro (fold em `UsageTotals`).
- [ ] Watermark as-of-seq partilhada entre painel de estado e de custo (um só cursor monotónico por-run).

### Detalhes Técnicos

- O cursor de seq de AOS-134 **é** a watermark de consistência partilhada.

### Testes Requeridos

- Teste de agregação incremental; teste de consistência estado-vs-custo.

### Definition of Done

- [ ] Burn-down incremental + watermark unificada; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-136 (AOS). Decorador de Exporter com fan-out por-run/trace + agregador incremental
(fold em UsageTotals) para o burn-down. Unifica a watermark as-of-seq: o cursor de seq de
AOS-134 E a watermark partilhada entre painel de estado e de custo (um so cursor monotonico
por-run). Testa consistencia estado-vs-custo.
```

---

## AOS-137 — *(web, condicional D1(b))* Sanitização + CSP + separação de canais

| Campo | Valor |
|---|---|
| Epic | EPIC-13 — Camada Frontend |
| Fase | 5 — Operacionalização |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-133 (condicional a D1(b)) |
| Bloqueia | AOS-138 |
| Responsável sugerido | Responsável de Segurança |
| Documentos de referência | ADR-016 §2/§6, `substrate/redaction` |

### Contexto

Redacção ≠ sanitização XSS (deteta PII, deixa passar `javascript:`/`on*`). O canal de controlo (trusted) e o de dados (untrusted) não estão separados. **Condicional a D1(b)** (só se a superfície web reabrir).

### Objectivo

Output-encoding contextual (nunca `innerHTML`) + CSP estrita (nonce/hash, self-only) + separação física de origens/sessões controlo↔dados.

### Critérios de Aceitação

- [ ] Todo o `Untrusted`/Preview é texto inerte.
- [ ] CSP recusa inline não-nonce.
- [ ] Comprometer o canal de dados não injecta controlo.

### Detalhes Técnicos

- Distinto da redacção de PII (que continua obrigatória).

### Testes Requeridos

- Teste de XSS (markup/`javascript:`/`on*` inertes); teste de CSP; teste de separação de canais.

### Definition of Done

- [ ] Saída untrusted inerte; CSP estrita; canais separados; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-137 (AOS, CONDICIONAL a D1(b)). Adiciona output-encoding contextual (nunca
innerHTML) + CSP estrita (nonce/hash, self-only) + separacao fisica de origens/sessoes
controlo<->dados. Redaccao != XSS: todo o Untrusted/Preview e texto inerte. Comprometer o canal
de dados nao injecta controlo. Testa markup/javascript:/on* inertes.
```

---

## AOS-138 — *(web)* 4-eyes real: 2 credenciais WebAuthn atestadas + challenge por-perna

| Campo | Valor |
|---|---|
| Epic | EPIC-13 — Camada Frontend |
| Fase | 5 — Operacionalização |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-132, AOS-137 |
| Bloqueia | — |
| Responsável sugerido | Responsável de Segurança |
| Documentos de referência | ADR-016 §4, `approval-card/dualcontrol.go` |

### Contexto

O 4-eyes hoje é **desigualdade de string** de NHI — uma sessão/um humano pode produzir ambas as aprovações (STUB necessário-mas-insuficiente). A *attestation* fica **condicional a D4**.

### Objectivo

Requisito estrutural: duas credenciais WebAuthn atestadas distintas (credential-id/AAGUID distintos), duas sessões vivas distintas, challenge por-perna emitido pelo servidor.

### Critérios de Aceitação

- [ ] Duas sessões autenticadas distintas.
- [ ] Servidor recusa 2.º sign do mesmo principal/sessão/credencial.
- [ ] Prova de attestation selada — **condicional a D4** (IdP/AAGUID-allowlist).

### Detalhes Técnicos

- Enquanto D4 aberta, a distinção é estrutural (não por attestation real); marcado STUB.

### Testes Requeridos

- Teste de recusa de auto-aprovação; teste de duas sessões distintas.

### Definition of Done

- [ ] Invariante estrutural fixada; attestation condicional D4; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-138 (AOS). Requisito estrutural do 4-eyes: 2 credenciais WebAuthn atestadas distintas
(credential-id/AAGUID distintos) + 2 sessoes vivas + challenge por-perna; servidor recusa 2.o
sign do mesmo principal/sessao/credencial. A attestation fica CONDICIONAL a D4 (IdP/AAGUID). Ate
la, a distincao e estrutural (STUB). Testa recusa de auto-aprovacao.
```

---

## AOS-139 — *(web, condicional D1(b))* ApprovalCard v2 + anti-fadiga + calibração

| Campo | Valor |
|---|---|
| Epic | EPIC-13 — Camada Frontend |
| Fase | 5 — Operacionalização |
| Tipo | feature |
| Prioridade | P2 |
| Estimativa | L |
| Dependências | AOS-131, AOS-133 (condicional a D1(b)) |
| Bloqueia | AOS-140 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `approval-card/card.go`, `tecnica/15_Experiencia_HITL_UX.md` |

### Contexto

`Preview` como string opaca funde efeito+destinatário+payload = forma canónica do rubber-stamping; a anti-fadiga SA-ROC é só intenção; a calibração não está ancorada ao ponto de decisão.

### Objectivo

ApprovalCard estruturado (destinatário + payload + efeito diffável antes→depois) + proveniência + digest gray expansível + atrito assimétrico (type-to-confirm nos irreversíveis) + calibração/histórico inline.

### Critérios de Aceitação

- [ ] Efeito diffável e proveniência visíveis.
- [ ] Atrito cresce com o risco.
- [ ] Incerteza material realçada junto dos controlos.

### Detalhes Técnicos

- Proveniência = que dados untrusted influenciaram o efeito.

### Testes Requeridos

- Teste de rendering estruturado; teste de atrito assimétrico; teste de calibração inline.

### Definition of Done

- [ ] Card v2 estruturado; anti-fadiga; calibração ancorada; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-139 (AOS, CONDICIONAL a D1(b)). ApprovalCard v2: separa Preview em destinatario +
payload + efeito diffavel (antes->depois) + campo de PROVENIENCIA (que dados untrusted
influenciaram) + digest gray expansivel item-a-item + atrito assimetrico (type-to-confirm nos
irreversiveis) + calibracao/historico inline. Testa.
```

---

## AOS-140 — *(web, condicional D1(b))* a11y WCAG 2.2 AA + i18n

| Campo | Valor |
|---|---|
| Epic | EPIC-13 — Camada Frontend |
| Fase | 5 — Operacionalização |
| Tipo | feature |
| Prioridade | P2 |
| Estimativa | M |
| Dependências | AOS-139 (condicional a D1(b)) |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `tecnica/15_Experiencia_HITL_UX.md`, `surface-adapter/render.go` |

### Contexto

Acessibilidade ausente (risco codificado só por cor); strings PT-PT hardcoded nos renderers.

### Objectivo

Canal de risco redundante (ícone+texto+posição), navegação por teclado, semântica ARIA; extrair strings PT-PT para catálogo i18n.

### Critérios de Aceitação

- [ ] WCAG 2.2 AA nos cards/fila/drill-down.
- [ ] Nenhuma string de apresentação hardcoded no modelo canónico.
- [ ] PT-PT como locale default.

### Detalhes Técnicos

- Risco por cor+ícone+texto+posição, nunca só cor.

### Testes Requeridos

- a11y automatizada (contraste/teclado/ARIA); teste de extracção i18n.

### Definition of Done

- [ ] WCAG 2.2 AA; i18n extraído; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-140 (AOS, CONDICIONAL a D1(b)). a11y WCAG 2.2 AA: canal de risco redundante
(icone+texto+posicao, nao so cor), navegacao por teclado, ARIA nos cards/fila/drill-down.
Extrai as strings PT-PT dos renderers para um catalogo i18n (PT-PT default). a11y automatizada
no CI.
```

---

## AOS-141 — *(web)* Read-path soberano + audit de leitura + override-rate por-aprovador

| Campo | Valor |
|---|---|
| Epic | EPIC-13 — Camada Frontend |
| Fase | 5 — Operacionalização |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-133, EPIC-09/10 |
| Bloqueia | — |
| Responsável sugerido | Responsável de Segurança |
| Documentos de referência | ADR-011/016 §5, `sovereignty/registry.go`, `hitl/metrics.go` |

### Contexto

`sovereignty.Registry` governa efeitos (PEP), não a leitura da UI; o audit sela decisões assinadas, não leituras; override-rate é por-Channel, não por-aprovador. Regra e mecânica **fixadas já**; a topologia é **condicional a D7**.

### Objectivo

Gate de soberania no read-path (fail-closed via a mesma `sovereignty.Registry.Authorized`), audit WORM de leituras sensíveis *producer-bound* em partição dedicada, e override-rate por-aprovador.

### Critérios de Aceitação

- [ ] BFF recusa servir estado/custo/trajectória fora da região do board; BFF por-região; agregador global proibido.
- [ ] Leituras sensíveis (`Taint`/`Sensitivity`/`PayloadRef`/DSAR) seladas em partição WORM dedicada crypto-shreddable.
- [ ] Override-rate agregado por-aprovador (`AttrApprover`), alarme 0.40 por aprovador + piso de amostra.

### Detalhes Técnicos

- Topologia operacional **condicional a D7** (nº de regiões/federação); limiar de amostra a afinar em produção.

### Testes Requeridos

- Teste de recusa cross-região; teste de audit de leitura; teste de override-rate por-aprovador.

### Definition of Done

- [ ] Read-path soberano + audit de leitura + override-rate por-aprovador; sem segredos/PII em claro.

### Handoff para Claude Code

```text
Ticket AOS-141 (AOS). (D7) Gate de soberania no read-path: cada endpoint resolve board->regiao
via sovereignty.Registry.Authorized (a mesma do PEP) e recusa fora-da-regiao; BFF por-regiao;
agregador global PROIBIDO. (D6) Sela leituras sensiveis producer-bound (Taint/Sensitivity/
PayloadRef/DSAR) em particao WORM dedicada crypto-shreddable. Override-rate por-aprovador
(AttrApprover, 0.40 + piso de amostra). Topologia condicional a D7.
```

---

## AOS-142 — *(web, condicional D1(b))* Dockerfile do BFF + edge/DMZ

| Campo | Valor |
|---|---|
| Epic | EPIC-13 — Camada Frontend |
| Fase | 5 — Operacionalização |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-133, EPIC-10 (infra) |
| Bloqueia | — |
| Responsável sugerido | Responsável de Segurança |
| Documentos de referência | `infra/` (AOS-098), ADR-004 |

### Contexto

A topologia AOS-098 não tem tier de ingress; o endurecimento adiado (read-only/non-root/CPU) é tolerável para placeholders mas **não** para o BFF adjacente-a-internet. **Condicional a D1(b)**.

### Objectivo

1.º Dockerfile de serviço real (build Go→distroless/scratch, non-root, read-only rootfs, `@sha256`, caps dropped, limites) + módulo de rede edge/DMZ na IaC.

### Critérios de Aceitação

- [ ] Imagem non-root/read-only pinada por `@sha256`.
- [ ] Terceira zona de rede (edge/DMZ) com `tftest`; ingress→BFF, hop interno autenticado.
- [ ] Egress default-deny preservado.

### Detalhes Técnicos

- Reutiliza o endurecimento de `infra/modules/*` (security_opts, memory).

### Testes Requeridos

- `tofu test` da zona edge; verificação da imagem non-root/read-only.

### Definition of Done

- [ ] Dockerfile + edge/DMZ; egress default-deny mantido; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-142 (AOS, CONDICIONAL a D1(b)). Escreve o 1.o Dockerfile de servico real (o do BFF):
build Go -> distroless/scratch, non-root, read-only rootfs, @sha256, caps dropped, limites
CPU+memoria. Adiciona um modulo de rede edge/DMZ a IaC (ingress->BFF, hop interno autenticado,
egress default-deny preservado) com tftest. Reutiliza o endurecimento de infra/modules/*.
```

---

## AOS-143 — Testes de frontend/DX

| Campo | Valor |
|---|---|
| Epic | EPIC-13 — Camada Frontend |
| Fase | 5 — Operacionalização |
| Tipo | chore |
| Prioridade | P2 |
| Estimativa | S |
| Dependências | AOS-133…142 |
| Bloqueia | — |
| Responsável sugerido | Responsável de Segurança |
| Documentos de referência | `specs/01_Engineering_Standards_e_Handoff.md` |

### Contexto

O gate de qualidade da camada frontend: contrato de transporte, idempotência, a11y, CSP.

### Objectivo

Testes de contrato de transporte (SSE resume, versionamento pass-through), idempotência por RequestID, a11y automatizada, CSP e integridade dos assets embebidos.

### Critérios de Aceitação

- [ ] Gate de transporte fail-closed.
- [ ] **Idempotência exactly-once por RequestID** provada (dedup de duplo-clique/reentrega **dentro do tempo de vida da ligação**) — **não** exactly-once inter-restart (D5 single-process).
- [ ] a11y + CSP no CI.

### Detalhes Técnicos

- O BFF já corre nos gates Go; adicionar os gates específicos de frontend.

### Testes Requeridos

- Teste de resume SSE; teste de idempotência por RequestID; a11y/CSP automatizados.

### Definition of Done

- [ ] Gates de frontend/DX fail-closed; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-143 (AOS). Gates de frontend/DX: contrato de transporte (SSE resume, versionamento
pass-through), idempotencia exactly-once por RequestID (dedup de duplo-clique/reentrega DENTRO
do tempo de vida da ligacao — NAO inter-restart, D5), a11y automatizada, CSP e integridade dos
assets embebidos. Fail-closed no CI.
```

---

## Tabela de aprovação

| Papel | Nome | Assinatura | Data |
|---|---|---|---|
| Arquitecto de Plataforma |  |  |  |
| Responsável de Segurança |  |  |  |
| Responsável de Produto |  |  |  |

---

## Controlo de versões

| Versão | Data | Descrição | Autor |
|---|---|---|---|
| 0.1 | Julho 2026 | Proposta inicial (painel adversarial `w3uijpkjy`): PR-0 + 15 tickets AOS-129–143 + 7 decisões humanas. Não ratificada. | Painel AOS |
| 1.0 | Julho 2026 | Ratificada (painel `wsuca4fcl`): decisões §Visão fixadas/condicionais (D2/D3/D6 fixos; D1(b)/D4-autoridade/D5-graduação/D7-topologia condicionais); AOS-129 feito (ADRs materializados); PR-0 destacado para a EPIC-14 (composition-root, dono plataforma/kernel-runtime); correcções factuais (desktop = `Renderer` modelado/inerte; resume-from-seq honesto, não exactly-once inter-restart). Reformatado no formato canónico dos EPIC-01…12. | Equipa AOS |

---

*Nota: este epic é subordinado à `specs/EPIC-14_Integracao_Composition_Root.md` (PR-0). As advertências W1–W3 são risco assumido; o long-pole de identidade (autoridade D4) é um bloqueador não-técnico a escalar. A superfície apresenta, nunca é autora do enforcement (ADR-016).*
