# EPIC-14 — Integração e Composition-Root (Resolução da Dívida de Backend)

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — Integração e Composition-Root (resolução da dívida de backend, PR-0) |
| Versão | 1.0 |
| Data | Julho de 2026 |
| Classificação | Documento de Referência — Aberto |
| Documento-fonte | `_FONTE_agentic-os-ideal.md`; painel adversarial `wsuca4fcl` (análise da base de código real) |
| Documentos relacionados | `specs/EPIC-13_Frontend.md`, `docs/adr/ADR-006-credential-broker-jit.md`, `docs/adr/ADR-013-gates-risco-sa-roc.md`, `docs/adr/ADR-016-fronteira-confianca-ui.md`, `specs/EPIC-07_Seguranca_Isolamento.md`, `specs/EPIC-09_Governacao_Conformidade.md`, `specs/01_Engineering_Standards_e_Handoff.md` |

---

## 1. Visão do Epic

Este epic materializa **PR-0** — a dívida de wiring de backend que a EPIC-13 ratificou como pré-requisito da camada frontend. Produz o **composition-root** vivo: o processo que compõe as bibliotecas (Event Store, Reference Monitor, `state.Machine`, broker, superfícies EPIC-12) com o enforcement **real**, não *stubbed*. É a dívida catalogada em `[[integration-composition-root]]`.

A análise da base de código real (painel `wsuca4fcl`) **reformulou** a dívida e corrigiu erros de framing — o que se segue foi verificado em `git`/build e é o estado autoritativo:

- **Não há "fundir branches divergentes".** `git merge-base` prova uma **cadeia linear estrita**; o tip de consolidação é **`feature/AOS-128-ux-dx-tests` (41 módulos)**, do qual todas as branches `claude/*` e `feature/AOS-119..128` são ancestrais. As superfícies EPIC-12 (`StateProjector` em `control-surface/reflection.go`, `Renderer` em `surface-adapter/render.go`) **já estão committed** no tip.
- **A dívida de consolidação é estreita (M–L):** reconciliar dois meios-ápice `packages/integration` divergentes (mesmo module path, `go.mod` diferentes, ficheiros disjuntos: Model Gateway vs freeze/revalidação) e portar sobre o *drift* de APIs (~63–81 commits). O custo exacto é **indecidível sem um build-spike real** sobre o tip (AOS-145).
- **O trabalho de ápice/seams estava UNCOMMITTED** em 3 working-dirs de worktree — a um `git clean` de se perder. Foi **resgatado** (AOS-144, feito).
- **O trabalho pesado é a jusante e é de segurança:** o hook de identidade está committed mas **inerte** (o kernel não preenche `Call.Credential` ⇒ nega toda a tool call — AOS-152); `referencemonitor.NewProduction` é **necessário-mas-insuficiente** (embarca `IdentityStub`/`EgressStub` — AOS-153); a espinha de token real está **condicional à autoridade de identidade** (não há IdP/binding humano↔NHI — bloqueador não-técnico a escalar; até lá a identidade é *demo-only self-minted*).

Este epic pertence à **Fase 5 — Operacionalização** e é **predecessor** da execução de código de UI da EPIC-13. **Princípio de ouro (ADR-016):** o enforcement é montado aqui, no ápice, por quem o possui (plataforma/kernel-runtime); a superfície apenas o consome.

---

## 2. Critérios de Saída do Epic

- [x] Todo o código de ápice/seams está committado em branch (nenhuma cópia única em working-dir) — **AOS-144**.
- [x] O tip AOS-128 (41 módulos) compila e testa offline (`GOPROXY=off`) e o *drift*/porting + a colisão dos dois `integration` estão **medidos** — **AOS-145**.
- [x] Existe **um** módulo `packages/integration` reconciliado sobre AOS-128, com os seams `NewProduction` foldados e portados, `go build/test ./...` verde offline, e o módulo nos gates fail-closed (`require_tests`, cobertura ≥ 80%) **reproduzíveis num runner frio**.
- [x] `cmd/aos-demo` compõe o grafo zero-rede e o **AC4 é um teste refutável** (invariantes PROVADAS ou DIFERIDAS-com-seam-nomeado; sem *vacuous pass*).
- [x] O enforcement é **real**: `Call.Credential` preenchido no kernel (**AOS-152**); `NewProductionSecure` recusa `IdentityStub`/`EgressStub`/`ScopeGate` nil (**AOS-153**); cadeia real de hooks com **um único** `audit.Store` WORM (**AOS-154**); guard-test fim-a-fim que nega anónimo/raiz-forjada/taint/egress/scope (**AOS-161**, `TestApexEnforcement_FiveDenials`).
- [x] O long-pole de identidade está explicitamente marcado *demo-only self-minted* com o bloqueio D4 escalado (`docs/reports/D4-escalacao-autoridade-identidade.md`) — sem reivindicar não-forjabilidade inexistente (o default `identity.NewVerifier()` sem anchors nega toda a NHI fail-closed).
- [ ] Todos os tickets com DoD de domínio verde (`specs/01_Engineering_Standards_e_Handoff.md`); sem segredos; gates SAST/SCA na baseline. — **Parcial:** todos os tickets NÃO-bloqueados têm DoD verde (AOS-144–155, 157–159, 161) — com o **residual nomeado de AOS-159** (o CA do wiring no promotion controller foi corrigido de `[x]` para `[ ]` por **AOS-196**/DEF-03; o DoD do mecanismo mantém-se verde); sem segredos (`secrets.sh` verde); a triagem SAST/SCA da baseline é o passo de FECHO do epic (correr `sast.sh`/`sca.sh` e baselinar falsos-positivos ao encerrar). Fica aberto porque **AOS-160/162 dependem de D4** (a espinha de token real) — o epic só encerra na íntegra quando D4 for provisionado; até lá está *feito até onde o código permite*.

---

## 3. Tabela Resumo de Tickets

| ID | Título | Tipo | Estimativa | Prioridade | Dependências |
|---|---|---|---|---|---|
| AOS-144 | Resgate-commit dos seams/ápice uncommitted nas suas branches | chore | S | P0 | — |
| AOS-145 | Build-spike offline sobre o tip AOS-128 (41 módulos): medir drift/porting e colisão dos 2 `integration` | spike | M | P0 | AOS-144 |
| AOS-146 | Reconciliar os 2 `packages/integration` num módulo único sobre AOS-128 | feature | M | P0 | AOS-145 |
| AOS-147 | Fold dos seams `NewProduction` (RM + Model Gateway) e porting sobre o drift | feature | M | P0 | AOS-146 |
| AOS-148 | Módulo apex nos gates fail-closed + vendoring/cache-prime (offline reproduzível) | chore | M | P1 | AOS-147 |
| AOS-149 | `cmd/aos-demo` — ápice mínimo single-process zero-rede | feature | M | P1 | AOS-148 |
| AOS-150 | Backfill do `StateProjector` (backfill+resume sob watermark) | feature | S | P1 | AOS-149 |
| AOS-151 | AC4 refutável (`TestApexMinimalSufficiency` + poison-test) | chore | S | P1 | AOS-149 |
| AOS-152 | Kernel: `Goal.Credential` + preencher `Call.Credential` no loop | feature | M | P0 | AOS-148 |
| AOS-153 | `NewProductionSecure` (recusa stubs de identidade/egress) | feature | M | P0 | AOS-147 |
| AOS-154 | Compor a cadeia real de hooks com um único `audit.Store` WORM | feature | L | P0 | AOS-152, AOS-153 |
| AOS-155 | Portar `SecuredRuntime` (AOS-050/051) + `RunToolSets` durável | feature | M | P1 | AOS-154 |
| AOS-156 | Espinha de token de identidade real (issuer via vault + authn) | feature | L | P1 | AOS-154 |
| AOS-157 | Portas RT/RM no pai `agentruntime` (AOS-021/037/043) | feature | L | P2 | AOS-154 |
| AOS-158 | Wirar `SteerChannel` ao loop (`GracefulPause` + correcção) | feature | M | P1 | AOS-149 |
| AOS-159 | Anti-replay da ratificação: nonce-store durável + freshness | feature | M | P1 | AOS-148 |
| AOS-160 | `Authenticator` de produção ed25519 + nonce-store durável | feature | M | P2 | AOS-156 |
| AOS-161 | Guard-test de enforcement fim-a-fim do apex (AC4 de segurança) | chore | S | P1 | AOS-154 |
| AOS-162 | AOS-132-runtime + AOS-138 (4-eyes): invariante estrutural | feature | M | P2 | AOS-152, AOS-156 |

Estimativas XS/S/M/L (XL proibido; o XL de identidade decompõe-se em AOS-152/154/156/160 + o bloqueio humano D4). Prioridades P0/P1/P2. Toda a Fase 5. Fases locais: PR-0.a (consolidação: AOS-144–148), PR-0.b (ápice mínimo: AOS-149–151), PR-0.c (enforcement de produção: AOS-152–162).

---

## AOS-144 — Resgate-commit dos seams/ápice uncommitted nas suas branches

| Campo | Valor |
|---|---|
| Epic | EPIC-14 — Integração e Composition-Root |
| Fase | 5 — Operacionalização (PR-0.a) |
| Tipo | chore |
| Prioridade | P0 |
| Estimativa | S |
| Dependências | — |
| Bloqueia | AOS-145 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `[[integration-composition-root]]`, painel `wsuca4fcl` |

### Contexto

Todo o código de ápice e seams de produção existia **apenas** como ficheiros staged/untracked em três working-dirs de worktree, em nenhuma branch — a um `git clean` de evaporar. Era o risco mais agudo do programa e cinco das seis lentes do painel nem o mencionaram. **Feito nesta ronda** (commits `307d388`/`9d4efe3`/`0112b70`).

### Objectivo

Committar, em cada worktree e na sua própria branch, o trabalho pendente, para que nenhuma cópia única fique em working-dir.

### Critérios de Aceitação

- [x] `claude/eloquent-colden`: `reference-monitor/production.go`+`production_test.go`+`taint_gate.go` committados.
- [x] `claude/nice-cartwright`: `packages/integration/` + `model-gateway/production.go` + `routing/failover/` committados.
- [x] `claude/admiring-wright`: `packages/integration/` (freeze/revalidação) committado.
- [x] `git status`/`git stash list` de cada worktree sem trabalho pendente (fora `.claude/`).

### Detalhes Técnicos

- Commits de resgate/WIP, um por worktree, sem merge, sem push (não há remote), sem operações destrutivas.
- `CHANGELOG.md` de cada worktree incluído no commit respectivo.

### Testes Requeridos

- Verificação `git status --porcelain` limpo (fora `.claude/`) em cada worktree após o commit.

### Definition of Done

- [x] Nenhuma cópia única de código de ápice/seams fica em working-dir.
- [x] Cada commit na sua branch, com mensagem que identifica o conteúdo e a proveniência (painel `wsuca4fcl`).
- [x] Sem segredos nos commits.

### Handoff para Claude Code

```text
Ticket AOS-144 (AOS). Em cada um dos 3 worktrees (eloquent-colden, nice-cartwright,
admiring-wright), committa na PROPRIA branch o trabalho staged/untracked de apice/seams
(reference-monitor/production.go+taint; packages/integration; model-gateway/production+
routing/failover). Sem merge, sem push, sem operacoes destrutivas. Confirma git status
limpo (fora .claude/). Este trabalho e a UNICA copia — nao o percas.
```

---

## AOS-145 — Build-spike offline sobre o tip AOS-128 (41 módulos)

| Campo | Valor |
|---|---|
| Epic | EPIC-14 — Integração e Composition-Root |
| Fase | 5 — Operacionalização (PR-0.a) |
| Tipo | spike |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-144 |
| Bloqueia | AOS-146, AOS-147 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `scripts/ci/lib.sh` (`discover_modules`), painel `wsuca4fcl` |

### Contexto

A prova de build "26/26 offline" do painel foi feita sobre AOS-103 e está **stale**: o tip real é AOS-128 (41 módulos), cujos 15 módulos de superfície nunca foram build-verificados no fecho de `replace`. Nenhuma estimativa de PR-0.a é defensável antes deste spike. É a acção que converte o maior desconhecido (drift/porting + colisão dos dois `integration`) em conhecido.

### Objectivo

Provar que o tip AOS-128 compila e testa offline e **medir** a superfície real de conflito ao sobrepor os seams resgatados.

### Critérios de Aceitação

- [x] Para cada `go.mod` do tip AOS-128, `GOPROXY=off GOFLAGS=-mod=mod go build ./...` e `go test ./...` correm e o resultado (PASS/FAIL por módulo) é registado.
- [x] O *overlay* dos seams resgatados (RM `NewProduction`, Model Gateway `production.go`, os dois `integration`) é aplicado e o **drift** (símbolos/APIs mudados; `gateway.go`/`taint_gate.go` são *modificados*) é medido e documentado.
- [x] A colisão entre os dois `packages/integration` (símbolos, `go.mod`) é enumerada.
- [x] Relatório com a estimativa de porting fundamentada em evidência de build.

### Detalhes Técnicos

- `discover_modules` = `find packages -name go.mod`; correr por-módulo.
- Não corrigir o drift aqui — **medir** apenas; a correcção é AOS-146/147.
- Registar se `GOPROXY=off` depende de `GOMODCACHE` quente (input para AOS-148).

### Testes Requeridos

- O próprio spike é uma bateria de `go build`/`go test` offline; o entregável é o relatório.

### Definition of Done

- [x] Prova de que o tip AOS-128 compila offline (ou lista dos módulos que falham e porquê).
- [x] Superfície de drift/porting e colisão dos dois `integration` medidas e documentadas.
- [x] Estimativa de PR-0.a fundamentada; comunicada ao dono.

### Handoff para Claude Code

```text
Ticket AOS-145 (AOS). Faz checkout do tip feature/AOS-128-ux-dx-tests (41 modulos, NAO
AOS-103). Para cada go.mod: GOPROXY=off GOFLAGS=-mod=mod go build ./... && go test ./...;
regista PASS/FAIL. Depois sobrepoe os seams resgatados (RM production.go, model-gateway
production.go, os 2 packages/integration) e MEDE o drift (gateway.go/taint_gate.go sao
modificados) e a colisao dos 2 integration. NAO corrijas o drift — mede-o. Entrega um
relatorio com a estimativa de porting fundamentada. Sem este spike nenhuma estimativa de
PR-0.a e defensavel.
```

---

## AOS-146 — Reconciliar os dois `packages/integration` num módulo único

| Campo | Valor |
|---|---|
| Epic | EPIC-14 — Integração e Composition-Root |
| Fase | 5 — Operacionalização (PR-0.a) |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-145 |
| Bloqueia | AOS-147 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | AOS-060 (porta-no-kernel+adaptador-no-pilar), `[[integration-composition-root]]` |

### Contexto

Existem dois meios-ápice `packages/integration` com o mesmo module path (`github.com/aos-ref/integration`), `go.mod` divergentes e ficheiros disjuntos: `nice` (Model Gateway, `AssembleModelGateway`) e `admiring` (freeze/revalidação RT/RM, `NewSecuredRuntime`). Não são superset um do outro; unir é uma decisão de arquitectura com dono, não um cherry-pick (ver decisão D-API na §Detalhes).

### Objectivo

Produzir um único módulo `packages/integration` sobre o tip AOS-128, reconciliando `go.mod`, ficheiros e a API pública.

### Critérios de Aceitação

- [x] `go.mod` único com a **união** de `require`/`replace` re-declarada a partir da raiz `packages/` (fecho transitivo completo; `replace` não-transitivos).
- [x] União dos ficheiros disjuntos (`modelgateway.go` + `secured/freeze/quarantine/revalhook/alerter/doc/wiring`) sem perda.
- [x] Colisões de símbolo package-level (ex.: `ErrNoModel`) resolvidas.
- [x] O idioma AOS-060 (porta-no-kernel+adaptador-no-pilar) é respeitado no módulo unido.
- [x] `GOPROXY=off go build ./...` verde no módulo.

### Detalhes Técnicos

- **Decisão D-API (dono):** como reconciliar `AssembleModelGateway` (nice) e `NewSecuredRuntime` (admiring) numa API pública única do módulo — decidir antes de codificar.
- Manter a costura pública `NewProduction` (RM e Model Gateway) que contorna `internal/` (no-bypass AOS-055).

### Testes Requeridos

- `go build`/`go test ./...` offline no módulo `integration`.
- Teste de que os dois fluxos (Model Gateway e freeze/revalidação) continuam expostos e compõem.

### Definition of Done

- [x] Um só `packages/integration` compila e testa offline sobre AOS-128.
- [x] API pública única documentada; idioma AOS-060 preservado.
- [x] Sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-146 (AOS). Sobre o tip AOS-128, funde os DOIS packages/integration (nice=Model
Gateway/AssembleModelGateway; admiring=freeze-reval/NewSecuredRuntime) num modulo unico:
uniao de require/replace re-declarada da raiz, uniao dos ficheiros disjuntos, resolve
colisoes de simbolo (ErrNoModel etc.), respeita o idioma AOS-060. Decide primeiro a API
publica unica (decisao com dono). GOPROXY=off go build/test ./... verde. Nao percas nenhum
dos dois fluxos.
```

---

## AOS-147 — Fold dos seams `NewProduction` e porting sobre o drift

| Campo | Valor |
|---|---|
| Epic | EPIC-14 — Integração e Composition-Root |
| Fase | 5 — Operacionalização (PR-0.a) |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-146 |
| Bloqueia | AOS-148, AOS-153 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/kernel/reference-monitor/production.go`, `packages/platform/model-gateway/production.go` |

### Contexto

Os seams `referencemonitor.NewProduction` (de `eloquent`) e `model-gateway` `NewProduction`+routing/failover (de `nice`) foram escritos ~63–81 commits atrás. `gateway.go` e `taint_gate.go` são *modificados* (não adições limpas); o porting contra as APIs evoluídas do tip é o grosso de PR-0.a.

### Objectivo

Foldar os seams sobre o tip e portá-los até compilar e testar offline.

### Critérios de Aceitação

- [x] `referencemonitor.NewProduction` e `model-gateway` `NewProduction`+routing/failover integrados no tip.
- [x] Drift portado (assinaturas/símbolos actualizados) até `GOPROXY=off go build ./... && go test ./...` verde em cada módulo tocado.
- [x] Sem regressão nos testes existentes dos módulos afectados.

### Detalhes Técnicos

- Iterar `cd packages/<mod> && GOPROXY=off go build ./...` após cada adição.
- `gateway.go`/`taint_gate.go` requerem *merge* semântico, não overlay.

### Testes Requeridos

- Build/test offline por-módulo; testes dos seams (`production_test.go`) verdes contra as APIs do tip.

### Definition of Done

- [x] Seams foldados e portados; verde offline.
- [x] Testes dos módulos afectados verdes; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-147 (AOS). Folda os seams NewProduction (RM de eloquent + model-gateway
production/routing/failover de nice) sobre o tip AOS-128 e PORTA-os contra as APIs
evoluidas (gateway.go/taint_gate.go sao modificados — merge semantico). Itera GOPROXY=off
go build/test ./... ate verde em cada modulo. Sem regressoes.
```

---

## AOS-148 — Módulo apex nos gates fail-closed + reprodutibilidade offline

| Campo | Valor |
|---|---|
| Epic | EPIC-14 — Integração e Composition-Root |
| Fase | 5 — Operacionalização (PR-0.a) |
| Tipo | chore |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-147 |
| Bloqueia | AOS-149, AOS-152, AOS-159 |
| Responsável sugerido | Responsável de Segurança |
| Documentos de referência | `scripts/ci/lib.sh`, `scripts/ci/run.sh` (`ALL_GATES`) |

### Contexto

`discover_modules` (`find packages -name go.mod`) apanha `packages/integration` automaticamente no build/test gate, mas falta um gate de cobertura do apex e a reprodutibilidade offline: `GOPROXY=off` só passa hoje por `GOMODCACHE` quente (sem vendor) — um runner frio falha (decisão D-OFF).

### Objectivo

Pôr o módulo apex nos gates fail-closed e tornar o build offline reproduzível.

### Critérios de Aceitação

- [x] Gate `apex.sh` (`require_tests` + cobertura ≥ 80%) no molde de `memory.sh`/`routing.sh`, com `apex` em `ALL_GATES` e nas `needs` do job agregador `gates`.
- [x] Vendoring por-módulo **ou** cache-prime pinado, tornando `GOPROXY=off` reproduzível num runner frio.
- [x] Baseline SAST/SCA multiset actualizada (sem `sort -u`).

### Detalhes Técnicos

- **Decisão D-OFF (dono):** vendoring por-módulo vs cache-prime pinado.
- O gate do apex mede primeiro o ápice **mínimo** (decisão D-GATE); o gate de produção segue após AOS-154.

### Testes Requeridos

- Correr o gate `apex.sh` num ambiente com cache fria (`GOPROXY=off`) e provar que passa.

### Definition of Done

- [x] `apex` fail-closed nos gates; cobertura ≥ 80% no módulo apex.
- [x] Build offline reproduzível provado; baseline actualizada; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-148 (AOS). Cria o gate apex.sh (require_tests + cobertura>=80%) no molde de
memory.sh/routing.sh, mete 'apex' em ALL_GATES e nas needs do job 'gates'. Torna GOPROXY=off
reproduzivel num runner frio (vendoring por-modulo OU cache-prime pinado — decide com o dono).
Actualiza a baseline SAST/SCA (multiset, sem sort -u). Prova o gate com cache fria.
```

---

## AOS-149 — `cmd/aos-demo` — ápice mínimo single-process zero-rede

| Campo | Valor |
|---|---|
| Epic | EPIC-14 — Integração e Composition-Root |
| Fase | 5 — Operacionalização (PR-0.b) |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-148 |
| Bloqueia | AOS-150, AOS-151, AOS-158 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | AOS-130 (EPIC-13), `control-surface/reflection.go`, `surface-adapter/render.go` |

### Contexto

O ápice mínimo prova os contratos end-to-end sem rede. `StateProjector` e `Renderer` já estão committed no tip — não se constroem de raiz.

### Objectivo

Um `cmd/aos-demo` que compõe, in-process, um fluxo completo (spawn → plan-approval → steer → approve com dual-control → trajectória) por CLI/TUI, zero rede.

### Critérios de Aceitação

- [x] Compõe, por esta ordem: `eventstore.New` → `control.NewHMACAuthenticator` (marcado *demo-grade*) → `control.NewChannel` → por-run `state.NewMachine` → `StateProjector` → modelo *fake* in-process → `agentruntime.New`.
- [x] Conduz um fluxo end-to-end in-process; superfície = control-surface + `Renderer` committed.
- [x] Zero dependências externas; `GOPROXY=off go build/test` verde.
- [x] Módulo `cmd/aos-demo` separado (fecho de `replace` próprio) para manter `packages/integration` import-limpo.

### Detalhes Técnicos

- **Refutação a registar (AC4):** o loop **não** consome o `SteerChannel` (wiring canal↔loop diferido em `loop.go`) — pause/steer/resume só é demonstrável *out-of-band* no ápice mínimo (a ligação ao loop é AOS-158).
- RM com stubs é **aceitável** no ápice mínimo (marcado); o RM de produção é PR-0.c.

### Testes Requeridos

- Teste e2e in-process do fluxo; build/test offline.

### Definition of Done

- [x] Fluxo end-to-end corre in-process, zero rede, zero deps externas.
- [x] Limitações do ápice mínimo marcadas explicitamente (steer out-of-band; RM com stubs).
- [x] Sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-149 (AOS). Cria cmd/aos-demo (modulo separado) que compoe in-process, zero rede:
eventstore.New -> control.NewHMACAuthenticator(demo) -> control.NewChannel -> por-run
state.NewMachine -> StateProjector (committed) -> modelo fake -> agentruntime.New; superficie
= control-surface + Renderer (committed). Conduz spawn->plan-approval->steer->approve(dual-
control)->trajectoria. MARCA as limitacoes: steer so out-of-band (loop nao consome SteerChannel
— e AOS-158); RM com stubs aceitavel aqui. GOPROXY=off build/test verde.
```

---

## AOS-150 — Backfill do `StateProjector`

| Campo | Valor |
|---|---|
| Epic | EPIC-14 — Integração e Composition-Root |
| Fase | 5 — Operacionalização (PR-0.b) |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | S |
| Dependências | AOS-149 |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `control-surface/reflection.go`, `substrate/eventstore/store.go` (`Read`) |

### Contexto

`StateProjector` arranca em `state.Ready` e nunca lê o backlog: um cliente que liga a um run `paused` vê estado errado. É o AOS-134 da EPIC-13. O primitivo `eventstore.Store.Read(ctx, streamID, fromSeq)` **já existe** — o fix é pequeno e in-process.

### Objectivo

Semear o `current` correcto relendo o backlog sob watermark e resumir por seq.

### Critérios de Aceitação

- [x] `EventSubscriber` alargado com `Read`; `NewStateProjector` costura `Read(fromSeq)`+`Subscribe` sob watermark de seq (dedup na sobreposição).
- [x] Um cliente que liga a um run `paused` vê `paused`.
- [x] Reconnect não perde nem duplica; cursor de seq exposto.

### Detalhes Técnicos

- Não afirmar *exactly-once* inter-restart (o eventstore in-memory perde o log no restart — herda a condicionalidade de HA).

### Testes Requeridos

- Teste de cold-start (`paused` reflectido a frio); teste de resume/dedup.

### Definition of Done

- [x] Backfill+resume correcto e testado; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-150 (AOS). Alarga EventSubscriber com Read e faz NewStateProjector costurar
Read(fromSeq)+Subscribe sob watermark de seq (dedup na sobreposicao) — o primitivo
eventstore.Store.Read ja existe. Um cliente que liga a um run paused TEM de ver paused.
Nao afirmes exactly-once inter-restart. Testa cold-start e resume/dedup.
```

---

## AOS-151 — AC4 refutável (`TestApexMinimalSufficiency` + poison-test)

| Campo | Valor |
|---|---|
| Epic | EPIC-14 — Integração e Composition-Root |
| Fase | 5 — Operacionalização (PR-0.b) |
| Tipo | chore |
| Prioridade | P1 |
| Estimativa | S |
| Dependências | AOS-149 |
| Bloqueia | — |
| Responsável sugerido | Responsável de Segurança |
| Documentos de referência | AOS-130 (AC4) |

### Contexto

"O ápice mínimo chega" é um juízo, não um predicado — sem o decompor passa por omissão (*vacuous pass*). O AC4 tem de ser refutável.

### Objectivo

Um teste em tabela em que cada invariante é PROVADA (assercção corre) ou DIFERIDA (`t.Skip` com string que **nomeia** o seam de produção em falta), falhando se alguma linha não for nem uma nem outra.

### Critérios de Aceitação

- [x] `TestApexMinimalSufficiency` em tabela: cada invariante PROVADA ou DIFERIDA-com-seam-nomeado; falha em linha não-classificada.
- [x] Poison-test `AOS_APEX_SELFTEST=1` que injecta uma configuração má e **passa quando o gate falha** (self-test do fail-closed).
- [x] O balanço provado-vs-diferido é o output operacional (resposta a "o ápice mínimo chega").

### Detalhes Técnicos

- Ligar ao `selftest.sh` do CI.

### Testes Requeridos

- O próprio ticket é teste; incluir a variante poison.

### Definition of Done

- [x] AC4 refutável no CI; nenhum *vacuous pass* possível; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-151 (AOS). Escreve TestApexMinimalSufficiency em tabela: cada invariante e
PROVADA (assercao) ou DIFERIDA (t.Skip nomeando o seam em falta); o teste FALHA se alguma
linha nao for nem uma nem outra (proibe vacuous pass). Adiciona poison-test AOS_APEX_SELFTEST=1
que injecta config ma e PASSA quando o gate FALHA. Liga ao selftest.sh.
```

---

## AOS-152 — Kernel: `Goal.Credential` + preencher `Call.Credential` no loop

| Campo | Valor |
|---|---|
| Epic | EPIC-14 — Integração e Composition-Root |
| Fase | 5 — Operacionalização (PR-0.c) |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-148 |
| Bloqueia | AOS-154, AOS-162 |
| Responsável sugerido | Responsável de Segurança |
| Documentos de referência | `agent-runtime/loop.go` (`mediateToolCall`), `identity/rmadapter.go` (`IdentityCheck`) |

### Contexto

O hook `IdentityCheck` já está committed, mas `loop.mediateToolCall` constrói o `referencemonitor.Call` **sem** `Credential` e `Goal` não tem o campo. Consequência: credencial vazia ⇒ anónimo ⇒ o `IdentityCheck` **nega TODA a tool call** fail-closed. É um predecessor duro de toda a cadeia de identidade e uma mudança de *source* no kernel (não wiring do apex).

### Objectivo

Adicionar `Goal.Credential` (token NHI) e preencher `Call.Credential` no *hot-path*.

### Critérios de Aceitação

- [x] `Goal` ganha o campo `Credential` (token NHI).
- [x] `loop.mediateToolCall` preenche `Call.Credential` a partir do `Goal`.
- [x] Com credencial válida, o `IdentityCheck` admite; sem credencial, nega (comportamento fail-closed preservado).
- [x] *Blast-radius* nos testes do loop resolvido (sem regressão em AOS-013/durabilidade).

### Detalhes Técnicos

- Mudança no *hot-path* — cuidado com o hash de prompt cache-estável (ADR-009) e a idempotência por passo.

### Testes Requeridos

- Teste de que `Call.Credential` é preenchido; teste de deny com credencial vazia; regressão do loop.

### Definition of Done

- [x] `Call.Credential` preenchido; `IdentityCheck` deixa de negar tudo; sem regressão; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-152 (AOS). No kernel: adiciona Goal.Credential (token NHI) e preenche
Call.Credential em loop.mediateToolCall. Hoje o Call e construido SEM Credential => o
IdentityCheck (committed) nega TODA a tool call. Com credencial valida tem de admitir; sem,
negar (fail-closed preservado). Cuidado com o hot-path (hash de prompt cache-estavel,
idempotencia por passo). Resolve o blast-radius nos testes do loop.
```

---

## AOS-153 — `NewProductionSecure` (recusa stubs de identidade/egress)

| Campo | Valor |
|---|---|
| Epic | EPIC-14 — Integração e Composition-Root |
| Fase | 5 — Operacionalização (PR-0.c) |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-147 |
| Bloqueia | AOS-154 |
| Responsável sugerido | Responsável de Segurança |
| Documentos de referência | `reference-monitor/production.go`, ADR-016 |

### Contexto

`referencemonitor.NewProduction` verifica `TaintGate`+audit-durável, mas a base `DefaultHooksWithTaint` embarca `IdentityStub`+`EgressStub`: um Monitor por `NewProduction` **sem** override `WithHooks` passa a própria guarda com identidade forjável e egress inerte. É necessário-mas-insuficiente.

### Objectivo

`NewProductionSecure` que também é fail-closed a menos que o slot `identity` seja não-stub, exista `ScopeGate` com `AuthoritySource` não-nil, e o slot `egress` seja não-stub.

### Critérios de Aceitação

- [x] `NewProductionSecure` recusa (erro tipado) se `identity == IdentityStub`, `egress == EgressStub`, ou `ScopeGate` nil/`AuthoritySource` nil.
- [x] Guard-tests que provam a recusa em cada caso.
- [x] A via sancionada deixa de poder embarcar identidade/egress inertes.

### Detalhes Técnicos

- Type-assert dos stubs; mesma família de costura de `NewProduction`.

### Testes Requeridos

- Guard-tests de recusa (identidade-stub, egress-stub, scope nil); teste de aceitação com hooks reais.

### Definition of Done

- [x] Recusa fail-closed provada; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-153 (AOS). Cria referencemonitor.NewProductionSecure: fail-closed a menos que
slot identity != IdentityStub, slot egress != EgressStub, e ScopeGate com AuthoritySource
!= nil. Guard-tests que provam a recusa em cada caso + aceitacao com hooks reais. A via
sancionada deixa de poder embarcar identidade/egress inertes.
```

---

## AOS-154 — Compor a cadeia real de hooks com um único `audit.Store` WORM

| Campo | Valor |
|---|---|
| Epic | EPIC-14 — Integração e Composition-Root |
| Fase | 5 — Operacionalização (PR-0.c) |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | AOS-152, AOS-153 |
| Bloqueia | AOS-155, AOS-156, AOS-157, AOS-161 |
| Responsável sugerido | Responsável de Segurança |
| Documentos de referência | `reference-monitor/hooks.go`, `substrate/sandbox` (`EgressHook`), ADR-011/013/016 |

### Contexto

O enforcement de produção exige compor a cadeia real de hooks pela ordem correcta, com o `Principal` resolvido pela identidade **antes** dos hooks que o consomem, e uma única espinha de audit WORM (senão dois WORM desconexos).

### Objectivo

Compor no apex, via `NewProductionSecure`, a cadeia: identity → reval → PDP → taint → scope → budget → egress → audit, com um único `audit.Store` WORM partilhado.

### Critérios de Aceitação

- [x] Ordem: `IdentityCheck` → `RevalidationHook` (AOS-051) → PDP (AOS-004) → `TaintGate` (com `StaticPrivilegedSet` sincronizado com o catálogo de capabilities sensíveis) → `ScopeGate` (com `AuthoritySource` do GOV) → budget → `EgressHook`+`EgressPolicyResolver`+`WORMSecuritySink` → audit.
- [x] `IdentityCheck` corre e popula `call.Principal` **antes** de taint/scope/egress/reval.
- [x] **Um único** `audit.Store` WORM partilhado entre o `EventSink` do RM e o `WORMSecuritySink` do egress.

### Detalhes Técnicos

- Substituir a construção do RM em `secured.go` (hoje `New` + stubs) pela cadeia real, mantendo `RevalidationHook` a-seguir-a-identidade.

### Testes Requeridos

- Teste de ordem/composição; teste de que o `Principal` é resolvido antes dos consumidores; teste do WORM único.

### Definition of Done

- [x] Cadeia real composta pela via segura; um só WORM; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-154 (AOS). Compoe no apex, via NewProductionSecure, a cadeia real de hooks:
identity->reval->PDP->taint(StaticPrivilegedSet sincronizado)->scope(AuthoritySource do GOV)
->budget->egress(EgressHook+EgressPolicyResolver+WORMSecuritySink)->audit. IdentityCheck TEM
de correr e popular call.Principal ANTES de taint/scope/egress/reval. UM UNICO audit.Store
WORM partilhado entre EventSink do RM e WORMSecuritySink. Substitui a construcao stub em
secured.go.
```

---

## AOS-155 — Portar `SecuredRuntime` (AOS-050/051) + `RunToolSets` durável

| Campo | Valor |
|---|---|
| Epic | EPIC-14 — Integração e Composition-Root |
| Fase | 5 — Operacionalização (PR-0.c) |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-154 |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/integration` (freeze/revalidação), AOS-050/051 |

### Contexto

O `SecuredRuntime` (freeze+revalidação) constrói hoje o RM com `New`+stubs; e o `RunToolSets` do freeze é in-memory (map+RWMutex): após failover a revalidação passa a default-deny para todas as tool calls do run.

### Objectivo

Portar o `SecuredRuntime` para `NewProductionSecure` (revalhook a-seguir-a-identidade) e tornar o freeze crash-safe.

### Critérios de Aceitação

- [x] `SecuredRuntime` usa `NewProductionSecure`, com `RevalidationHook` na posição a-seguir-a-identidade.
- [x] `RunToolSets` persiste o snapshot congelado no eventstore no arranque e reconstrói (`Rebuild`) na retoma.
- [x] Após failover simulado, a revalidação não colapsa para default-deny.

### Detalhes Técnicos

- `IdentityCheck` popula `Principal`, que o `RevalidationHook` consome.

### Testes Requeridos

- Teste de freeze/rebuild após failover; teste de ordenação identity→reval.

### Definition of Done

- [x] Freeze crash-safe; cadeia segura; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-155 (AOS). Porta o SecuredRuntime (AOS-050/051) de New(stubs) para
NewProductionSecure, com revalhook a-seguir-a-identidade. Torna RunToolSets DURAVEL:
persiste o snapshot congelado no eventstore e reconstroi (Rebuild) na retoma, senao apos
failover a revalidacao vira default-deny para TODAS as tool calls. Testa freeze/rebuild
pos-failover.
```

---

## AOS-156 — Espinha de token de identidade real (issuer via vault + authn)

| Campo | Valor |
|---|---|
| Epic | EPIC-14 — Integração e Composition-Root |
| Fase | 5 — Operacionalização (PR-0.c) |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-154 |
| Bloqueia | AOS-160, AOS-162 |
| Responsável sugerido | Responsável de Segurança |
| Documentos de referência | ADR-006, EPIC-07 (vault), AOS-057 (authn), AOS-005 (identidade) |

### Contexto

É o *long-pole* do programa e atravessa dois epics. **DESBLOQUEADO (emenda 1.1 da Carta, 2026-07-22): PRÉ-REQUISITO da v1.** Decisão do dono após o painel `wamnbffrk`: desbloquear a identidade real ANTES da v1, pela via **self-hosted Nível 2** — o issuer é uma **autoridade SEPARADA** (a chave nunca é entregue ao runtime do nó; o nó só detém a **pubkey do issuer**), com um **directório de autenticação humana interno** (porta plugável, endurecível para IdP corporativo depois). **Constrangimento verificado:** o vault do repo (`platform/broker`, `vault.Client`) é um cofre de segredos (`Fetch`-only), **não assina no lugar** — logo o Nível 2 (nó nunca detém a chave) exige o issuer como autoridade separada, não bastando buscar a chave ao vault. As partes de garantia mais alta (HSM/sign-in-place, IdP corporativo, attestation AAGUID/WebAuthn) são endurecimento POSTERIOR documentado.

### Objectivo

Um issuer com chave de assinatura vinda do vault (EPIC-07), o estágio authn (AOS-057) a mintar o NHI a partir do humano/NHI autenticado, e um verifier cujo trust-anchor é a pubkey do issuer (não uma chave controlada pelo apex).

### Critérios de Aceitação

- [x] Issuer com chave do vault; verifier com trust-anchor = pubkey do issuer. — **FEITO** (`a9ce546`, `packages/integration/issuer_authority.go`): `IssuerAuthority` = autoridade SEPARADA; a chave ed25519 vive num campo NÃO-exportado do `identity.Issuer` detido pela autoridade e NUNCA é devolvida; `TrustAnchor()` expõe só `(issuerID, pubkey)`; `NewVerifierFromAuthority()` constrói o verifier do nó **só da pubkey**. A chave é injectada de config/vault ou gerada em runtime — a custódia vault-fetch/HSM é endurecimento posterior.
- [x] Estágio authn minta o NHI a partir do humano autenticado, a montante do `ScopeGate`. — **FEITO**: porta `HumanDirectory` (plugável) + `AllowlistDirectory` de referência (marcada DEMO-GRADE-AUTH) → `MintForHuman` minta via `Issue` = `delegation.NewRoot(human:<id>, agent, escopo)` com o humano na RAIZ da cadeia; humano não-registado → recusado fail-closed. A autenticação real (OIDC/WebAuthn) é a porta a preencher (endurecimento).
- [x] **Identidade real (reformulado pela emenda 1.1):** deixou de ser *demo-only self-minted*. A identidade é agora emitida por uma autoridade separada com **não-forjabilidade RELATIVA ao nó** — provada por teste (impostor com o mesmo issuer-id mas chave distinta → `ErrSignatureInvalid`). Só a *autenticação humana* da referência é demo-grade (a porta). Sem reivindicar a garantia mais alta (HSM/IdP corporativo/attestation) — endurecimento posterior documentado.
- [x] Bloqueio D4 escalado ao dono. — `docs/reports/D4-escalacao-autoridade-identidade.md`; e desbloqueado pela via construível (emenda 1.1).

### Detalhes Técnicos

- O trust-anchor **não** pode ser uma chave que o apex controla (senão é teatro criptográfico).

### Testes Requeridos

- Teste de mint/verify contra o issuer; teste de recusa de token de issuer não-confiável.

### Definition of Done

- [x] Espinha ligada **ou** marcada *demo-only* com D4 escalada; sem segredos (a chave vem do vault, nunca em código/log). — **satisfeito pelo ramo "ESPINHA LIGADA"** (`a9ce546`): a espinha real está **construída e provada** (autoridade separada; não-forjabilidade relativa ao nó provada por `TestIssuerAuthority_ImpostorSameIssuerIDRejected`/`RogueKeyRejected`/`NoPrivateKeyEscape`); `secrets.sh` verde; `go build`/`test`/`vet` verdes. **Fronteira honesta:** a composição da autoridade **por omissão no nó** (substituir o default `NewVerifier()` sem anchors) é **AOS-163** (bootstrap — o seam `SecuredConfig.Verifier` + `NewVerifierFromAuthority` deixam-no aditivo e trivial); a custódia vault-fetch/HSM e a auth OIDC/WebAuthn reais são endurecimento. **AOS-160/162 desbloqueiam agora sobre esta espinha.**

*Nota de processo (pipeline `wf_c56dd15a`): implementado por dev→auditoria→remediação→commit. A auditoria de qualidade apanhou um teste de não-forjabilidade VACUOSO (só exercitava `ErrUnknownIssuer`, nunca a assinatura) e a remediação acrescentou o caso do impostor que assere `ErrSignatureInvalid` — a prova criptográfica real. Verificado independentemente (6/6 testes re-corridos, gates re-verificados).*

### Handoff para Claude Code

```text
Ticket AOS-156 (AOS). Constroi a espinha de token: issuer com chave do vault (EPIC-07),
estagio authn (AOS-057) minta o NHI a montante do ScopeGate, verifier trust-anchor = pubkey
do issuer (NUNCA uma chave que o apex controla). CONDICIONAL a D4 (sem IdP/binding humano-NHI):
escala ao dono e, ate la, MARCA a identidade como demo-only self-minted — NAO reivindiques
nao-forjabilidade. A chave vem do vault, nunca em codigo/log.
```

---

## AOS-157 — Portas RT/RM no pai `agentruntime` (AOS-021/037/043)

| Campo | Valor |
|---|---|
| Epic | EPIC-14 — Integração e Composition-Root |
| Fase | 5 — Operacionalização (PR-0.c) |
| Tipo | feature |
| Prioridade | P2 |
| Estimativa | L |
| Dependências | AOS-154 |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `agent-runtime/loop.go`, AOS-021/037/043, AOS-060 |

### Contexto

`activity`/`engine`/`working`/`compression` importam `agentruntime`, logo AOS-021/037/043 **não** são wiring de 3 linhas: exigem definir portas **dentro** de `package agentruntime` (idioma AOS-060) e adaptar os concretos no apex — uma mudança na assinatura de `Run`/`loop.go`.

### Objectivo

Definir as portas no pai (tipos locais, default = comportamento actual) e adaptar os concretos no apex: `activity.Dispatcher` (AOS-021), `WindowManager` (AOS-037), `CheckpointTrigger` (AOS-043).

### Critérios de Aceitação

- [x] Portas definidas em `package agentruntime`, default no-op (comportamento AOS-013 byte-idêntico sem adaptador). — `agent-runtime/ports.go`: `WindowFactory`/`WindowPort`+`WindowSignal` (AOS-037), `CompactionTrigger` (AOS-043), `ActivityDispatcher` (AOS-021); defaults `inlineWindow`/`noopCompactionTrigger`/`directDispatcher`. `TestLoop_DefaultWindow_ByteIdentical` prova o prompt do default idêntico ao `PromptAssembler` directo.
- [x] Concretos adaptados no apex; sem ciclos de import. — `integration/runtime_ports.go`: `WindowManagerFactory` (→ `working.WindowManager`), `CompactionTriggerAdapter` (→ `compression.CheckpointTrigger`), `DurableDispatcher` (→ `activity.Dispatcher`). O kernel importa só `reference-monitor`; os adaptadores vivem no apex (sem ciclos, `go vet` verde).
- [x] **Decisão D-TAIL (dono):** um único prefix-hash por run — **resolvida: o loop delega a `WindowPort`** (a camada força-o — o kernel não pode importar `platform/memory`; o `WindowManager` é o adaptador atrás da porta, nunca o condutor). `TestLoop_SinglePrefixHash` e `TestWindowManagerFactory_ByteIdenticalToInline` provam um só prefix-hash por run e a byte-identidade da troca.

### Detalhes Técnicos

- Sem quebrar o hash de prompt cache-estável (ADR-009).

### Testes Requeridos

- Teste de default byte-idêntico; teste de cada porta com adaptador; teste do único prefix-hash.

### Definition of Done

- [x] Portas no pai + adaptadores no apex; um só prefix-hash por run; sem regressão; sem segredos. — 3 portas no kernel + 3 adaptadores no apex; D-TAIL resolvida (loop delega a `WindowPort`). Sem regressão: `agent-runtime ./... -race` verde, `apex.sh` verde (20 testes obrigatórios incl. os 3 do AOS-157, cobertura 83.5%), `memory.sh` 90.1%, `secrets.sh` verde. Preservação do `Credential` (AOS-152) na via durável provada (`TestDurableDispatcher_PreservesCredential`); `activity.Activity` ganhou o campo `Credential` (aditivo).

### Handoff para Claude Code

```text
Ticket AOS-157 (AOS). Define portas DENTRO de package agentruntime (idioma AOS-060, default
no-op = AOS-013 byte-identico) para AOS-021 (activity.Dispatcher), AOS-037 (WindowManager),
AOS-043 (CheckpointTrigger); adapta os concretos no apex. Resolve D-TAIL: UM prefix-hash por
run (loop delega a WindowPort OU WindowManager e dono). Sem ciclos de import, sem quebrar o
cache-estavel.
```

---

## AOS-158 — Wirar `SteerChannel` ao loop (`GracefulPause` + correcção)

| Campo | Valor |
|---|---|
| Epic | EPIC-14 — Integração e Composition-Root |
| Fase | 5 — Operacionalização (PR-0.c) |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-149 |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `agent-runtime/loop.go`, `control/steer_channel.go`, AOS-023 |

### Contexto

O wiring canal↔loop está diferido em `loop.go`: o loop não chama `GracefulPause` nem injecta correcção, pelo que pause/steer/resume só é demonstrável *out-of-band*. É a primeira mudança de *source* que dá controlo através do loop.

### Objectivo

Ligar o `SteerChannel` ao loop: `GracefulPause` no fim do turno e injecção da correcção como dado de controlo (nunca instrução — separação control/data-plane).

### Critérios de Aceitação

- [x] O loop consome o `SteerChannel`: sinal `interrupt` provoca `GracefulPause` no fim do turno.
- [x] `resume` injecta a correcção como dado de controlo *trusted* (taint), nunca como conteúdo untrusted.
- [x] AC4 do ápice mínimo actualizado (a invariante de steer passa de DIFERIDA a PROVADA).

### Detalhes Técnicos

- Respeitar a separação control/data-plane (ADR-005) e a durabilidade (AOS-023).

### Testes Requeridos

- Teste de pause-no-fim-do-turno; teste de injecção de correcção *trusted*; teste da invariante AC4.

### Definition of Done

- [x] Steer/pause/resume através do loop; separação de taint preservada; sem segredos.

### Handoff para Claude Code

```text
Ticket AOS-158 (AOS). Liga o SteerChannel ao loop: interrupt -> GracefulPause no fim do turno;
resume -> injecta a correccao como dado de controlo TRUSTED (taint), nunca instrucao untrusted
(ADR-005). Actualiza o AC4 do apice minimo (invariante de steer passa de DIFERIDA a PROVADA).
Respeita a durabilidade de AOS-023.
```

---

## AOS-159 — Anti-replay da ratificação: nonce-store durável + freshness

| Campo | Valor |
|---|---|
| Epic | EPIC-14 — Integração e Composition-Root |
| Fase | 5 — Operacionalização (PR-0.c) |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-148 |
| Bloqueia | — |
| Responsável sugerido | Responsável de Segurança |
| Documentos de referência | ADR-012, `hitl/ratification.go` (`WithRatifyFreshness`/`WithRatifyNonceStore`) |

### Contexto

A ratificação assinada é uma identidade estável de conteúdo, reutilizável N vezes — inclusive após um rollback. As portas de endurecimento (`WithRatifyFreshness`/`WithRatifyNonceStore`) existem mas estão por omissão desligadas e o `RatificationNonceStore` durável não existe. **Eixo separado** do wiring do RM runtime (decisão D-EIXO: trilha/dono distintos).

### Objectivo

Um `RatificationNonceStore` durável e ligar freshness+nonce no promotion controller.

### Critérios de Aceitação

- [x] `RatificationNonceStore` durável sobre eventstore/WORM (`ConsumeNonce` atómico check-and-set). — **FEITO** (`packages/control-plane/governance/hitl/nonce_store.go`, `EventStoreNonceStore`).
- [ ] `WithRatifyFreshness`+`WithRatifyNonceStore` ligados no promotion controller. — **POR CUMPRIR** (corrigido de `[x]` por **AOS-196**, achado **DEF-03** da auditoria v4). O CA estava marcado feito e é **falso**: a via sancionada `hitl.NewProductionRatificationGate` (que FORÇA freshness+nonce) **não tem chamador de produção** em toda a árvore — `grep NewProductionRatificationGate packages/**/*.go` fora de `_test.go` → só a própria declaração. A causa é estrutural e não um esquecimento de wiring: **o nó `aos` não compõe nenhum promotion controller** (`grep -n "promotion\|Promote" packages/cmd/aos/*.go` não-teste → 0). O que AOS-159 entregou foi o **mecanismo**; o wiring **não tem ticket atribuído** e está registado como `DEF-401` em `docs/governance/REGISTO-Deferimentos.md`.
- [x] Uma ratificação re-usada após consumo é `ReasonRatificationReplayed`; fora da janela é `ReasonRatificationStale`. — **FEITO** ao nível do gate (`hitl/ratification.go`), provado em `nonce_store_test.go`/`ratification_test.go`; **não** observável em produção enquanto o CA acima não fechar.

### Detalhes Técnicos

- Âmbito do nonce = `RatificationID` (uso-único por identidade de artefacto+eval).

### Testes Requeridos

- Teste de replay (nega 2.º uso); teste de freshness; teste de durabilidade (sobrevive a "restart" simulado do store).

### Definition of Done

- [x] Uso-único durável provado; sem segredos. — **FEITO ao nível do mecanismo** (testes de replay/freshness/durabilidade verdes; `secrets.sh` verde). **Fronteira honesta (AOS-196):** «provado» é *provado em teste sobre o gate*, não *activo em produção* — o segundo CA está `[ ]` e o wiring é `DEF-401` (POR ATRIBUIR).

### Handoff para Claude Code

```text
Ticket AOS-159 (AOS). Implementa RatificationNonceStore DURAVEL sobre eventstore/WORM
(ConsumeNonce atomico check-and-set, ambito=RatificationID) e liga WithRatifyFreshness+
WithRatifyNonceStore no promotion controller (ADR-012). Re-uso apos consumo => Replayed; fora
da janela => Stale. Testa replay, freshness e durabilidade. Eixo separado do RM runtime.
```

---

## AOS-160 — `Authenticator` de produção ed25519 + nonce-store durável

| Campo | Valor |
|---|---|
| Epic | EPIC-14 — Integração e Composition-Root |
| Fase | 5 — Operacionalização (PR-0.c) |
| Tipo | feature |
| Prioridade | P2 |
| Estimativa | M |
| Dependências | AOS-156 |
| Bloqueia | — |
| Responsável sugerido | Responsável de Segurança |
| Documentos de referência | `control/steer_channel.go`, AOS-005, ADR-012 |

### Contexto

O `HMACAuthenticator` do ápice mínimo é *demo-grade* (sem nonce/seq/expiry, replayável). O `Authenticator` de produção tem de ligar à identidade ed25519 (AOS-005) com nonce-store durável.

### Objectivo

Substituir o `HMACAuthenticator` por um `Authenticator` ed25519 + nonce-store durável com frescura/expiração (anti-replay).

### Critérios de Aceitação

- [x] `Authenticator` verifica assinatura ed25519 contra a identidade (AOS-005). — **FEITO** (`packages/integration/steer_authenticator.go`): `Ed25519Authenticator` verifica `ed25519.Verify` sobre o tuplo canónico `(run_id‖kind‖payload‖nonce‖issued_at)` com codificação **injectiva** (length-prefix — provado por `TestEd25519_BoundaryShiftRejected`) contra a pubkey do emissor registada. A chave privada do emissor NUNCA é detida (só pubkeys). A FONTE de identidade real do operador (AOS-005 a alimentar o registo) é a composição de bootstrap = **AOS-163**.
- [x] Nonce-store durável com frescura/expiração; replay recusado. — **FEITO**: anti-replay via `EventStoreNonceStore` (AOS-159) — durável, sobrevive a restart (`TestEd25519_AntiReplayDurable` prova replay recusado após reconstruir o store sobre o mesmo log); janela de frescura `[now-ttl, now+skew]` com relógio injectável (`TestEd25519_StaleSignalRejected`, velho e futuro).
- [x] Substitui `HMACAuthenticator` no `SteerChannel`. — **FEITO via o seam** (`control.Authenticator`): o `Ed25519Authenticator` liga-se por `control.NewChannel(store, auth)` **sem mudar o canal** (provado em `TestEd25519_ValidSignatureAccepted`). A substituição POR OMISSÃO no wiring de produção do nó = **AOS-163** (o `cmd/aos-demo` mantém o HMAC demo até lá — não confundir "authenticator construído" com "nó de produção protegido").

### Detalhes Técnicos

- Depende da espinha de token (AOS-156) para a identidade real.

### Testes Requeridos

- Teste de verificação ed25519; teste de anti-replay; teste de expiração.

### Definition of Done

- [x] `Authenticator` de produção ligado; replay recusado; sem segredos. — **FEITO**: `Ed25519Authenticator` construído + ligável ao `SteerChannel` pelo seam (provado); replay recusado (durável, sobrevive a restart); sem segredos (`secrets.sh` verde; a chave privada do emissor nunca no authenticator). Gates verdes (integration + kernel/control build/test/vet/-race). **Fronteira honesta:** composição por-omissão no nó = AOS-163; attestation WebAuthn/AAGUID = AOS-162/endurecimento.

*Nota de processo (pipeline `wf_0652dd7b`): dev→auditoria→completude correram; a **auditoria de qualidade apanhou uma falha CRIPTOGRÁFICA ALTO** — codificação assinada NÃO-injectiva (separador `0x00` único ⇒ deslize de fronteira `payload|nonce` re-mintava um sinal com correcção mutada + nonce diferente mantendo a mesma assinatura, contornando o anti-replay durável). As etapas de remediação/commit falharam por erro de rede (`ENOTFOUND`); **completei-as manualmente**: verifiquei que o fix (length-prefix) já estava em disco, corrigi um import partido (`encoding/binary` não-usado) que deixava o build vermelho, e ADICIONEI o teste de injectividade em falta (`TestEd25519_BoundaryShiftRejected`). Verificação independente: 7/7 testes verdes, gates re-corridos.*

### Handoff para Claude Code

```text
Ticket AOS-160 (AOS). Substitui o HMACAuthenticator (demo) por um Authenticator ed25519 ligado
a identidade (AOS-005) + nonce-store duravel com frescura/expiracao (anti-replay ADR-012) no
SteerChannel. Testa verificacao, anti-replay e expiracao. Depende da espinha de token AOS-156.
```

---

## AOS-161 — Guard-test de enforcement fim-a-fim do apex (AC4 de segurança)

| Campo | Valor |
|---|---|
| Epic | EPIC-14 — Integração e Composition-Root |
| Fase | 5 — Operacionalização (PR-0.c) |
| Tipo | chore |
| Prioridade | P1 |
| Estimativa | S |
| Dependências | AOS-154 |
| Bloqueia | — |
| Responsável sugerido | Responsável de Segurança |
| Documentos de referência | AOS-154, ADR-011/013/016 |

### Contexto

O AC4 de segurança do apex: provar que o RM de produção nega o que tem de negar, e que os gates não são vacuous.

### Objectivo

Um guard-test fim-a-fim que exercita a cadeia real de hooks e um poison-test do fail-closed.

### Critérios de Aceitação

- [x] O RM de produção do apex **nega**: (a) call anónima (Credential vazio); (b) cadeia com raiz forjada / issuer não-confiável; (c) capability privilegiada sob taint untrusted; (d) egress fora da allowlist; (e) capability fora do escopo user∩classe. — `TestApexEnforcement_FiveDenials` (packages/integration/enforcement_guard_test.go): as 5 negações provadas via o `Mediate` REAL de um RM `NewProductionSecure` (cadeia identity→taint→scope→egress de AOS-154), cada uma atribuível à barreira certa (`DeniedBy` ∈ {identity, taint, scope, egress}).
- [x] Poison-test `AOS_APEX_SELFTEST=1` que injecta `EgressStub`/raiz-forjada e **passa quando o gate falha**. — `TestSelftestApexEnforcementBypassReddensGate`: contorna o egress default-deny (EgressStub via a via crua que `NewProductionSecure` recusaria) e assere falsamente a negação; FALHA de propósito, tornando o self-test (scripts/ci/selftest.sh secção K) VERMELHO quando o gate não bloqueia.

### Detalhes Técnicos

- Determinismo em teste (sem rede); usar hooks reais compostos em AOS-154.

### Testes Requeridos

- Os cinco cenários de negação + o poison-test.

### Definition of Done

- [x] Cinco negações provadas; poison-test verde; sem segredos. — apex.sh verde (17 testes obrigatórios incl. `TestApexEnforcement_FiveDenials`, -race, cobertura 83.2% ≥ 80%); selftest.sh secção K verde (o poison-test reddens o gate quando o egress é contornado); secrets.sh verde.

### Handoff para Claude Code

```text
Ticket AOS-161 (AOS). Guard-test fim-a-fim: o RM de producao do apex NEGA (a) call anonima
(Credential vazio), (b) raiz forjada/issuer nao-confiavel, (c) capability privilegiada sob
taint untrusted, (d) egress fora da allowlist, (e) capability fora do escopo user-inter-classe.
+ poison-test AOS_APEX_SELFTEST=1 que injecta EgressStub/raiz-forjada e PASSA quando o gate
FALHA. Deterministico, sem rede.
```

---

## AOS-162 — AOS-132-runtime + AOS-138 (4-eyes): invariante estrutural

| Campo | Valor |
|---|---|
| Epic | EPIC-14 — Integração e Composition-Root |
| Fase | 5 — Operacionalização (PR-0.c) |
| Tipo | feature |
| Prioridade | P2 |
| Estimativa | M |
| Dependências | AOS-152, AOS-156 |
| Bloqueia | — |
| Responsável sugerido | Responsável de Segurança |
| Documentos de referência | `specs/EPIC-13_Frontend.md` (AOS-132/138), ADR-016 |

### Contexto

Fecha a ligação com a EPIC-13: o runtime da fronteira de assinatura (AOS-132) e o 4-eyes real (AOS-138) dependem do predecessor de kernel (AOS-152) e da espinha de token (AOS-156). A *attestation* de dispositivo fica stub e **condicional a D4**.

### Objectivo

Fixar a invariante estrutural do 4-eyes (duas credenciais atestadas distintas + duas sessões + challenge por-perna) e ligar o runtime da assinatura *non-signing*.

### Critérios de Aceitação

- [x] Invariante estrutural do 4-eyes fixada em código (recusa 2.º sign do mesmo principal/sessão/credencial). — **FEITO contra a identidade demo-only**: `verifyLeg` exige 2 credenciais distintas + 2 sessões + challenge por-perna e recusa o 2.º sign do mesmo principal/sessão/credencial (suite pré-existente verde). Endurecida nesta série: `RiskClass` e `DualControlRequired` entram no tuplo assinado (campo `policy` 2-byte length-prefixed), fechando o downgrade dual→single de um relay non-signing (`TestFourEyes_DowngradeDualToSingleRejected` ⇒ `ErrBadLegSignature`), e `verifyLeg` passou a exigir `hitl.RequiredAuthority(RiskClass)` (`TestFourEyes_InsufficientAuthorityRejected` ⇒ `ErrInsufficientAuthority`, fail-closed). **Fronteira honesta:** o valor *não-forjável* de "mesmo principal" depende da espinha de token real e da attestation — **deferido para D4 + AOS-163**; até lá a distinção é estrutural sobre identidade demo-only.
- [x] A *attestation* de dispositivo fica **stub** gated em AOS-152 + **condicional a D4** (sem IdP real). — **FEITO como stub**: `DeviceAttestation` permanece stub/demo, **fora do tuplo assinado**, coberto por `TestFourEyes_DeviceAttestationIsStub`. Attestation real (AAGUID/WebAuthn/IdP) **deferida — BLOQUEADO por D4** (`docs/reports/D4-escalacao-autoridade-identidade.md`) e AOS-163.
- [x] Coerente com ADR-016 (BFF non-signing; WYSIWYS). — **FEITO**: a correcção do downgrade fecha exactamente o vector do relay/BFF *distrusted* non-signing do ADR-016 (uma perna dual não valida reconstruída como single); o `preview` no tuplo mantém a garantia WYSIWYS por-perna.

### Detalhes Técnicos

- Enquanto D4 aberta, a distinção é estrutural (não por attestation real).

### Testes Requeridos

- Teste de recusa de auto-aprovação; teste da invariante estrutural.

### Definition of Done

- [x] Invariante fixada; attestation marcada condicional D4; sem segredos. — **FEITO**: invariante estrutural fixada e endurecida (downgrade dual→single fechado; autoridade exigida), attestation permanece stub condicional a D4/AOS-163, `secrets.sh` verde. Gates verdes no módulo `packages/integration` (build/test/vet). **Fronteira honesta:** o binding não-forjável humano↔NHI e a attestation real ficam deferidos para D4 + AOS-163.

### Handoff para Claude Code

```text
Ticket AOS-162 (AOS). Fixa a invariante estrutural do 4-eyes (2 credenciais atestadas
distintas + 2 sessoes + challenge por-perna; servidor recusa 2.o sign do mesmo principal/
sessao/credencial) e liga o runtime da assinatura non-signing (AOS-132). A attestation de
dispositivo fica STUB gated em AOS-152 + condicional a D4 (sem IdP real). Coerente com ADR-016.
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
| 1.0 | Julho 2026 | Emissão inicial (GO-CONDICIONAL). Materializa PR-0 (dívida de backend) a partir do painel adversarial `wsuca4fcl` sobre a base de código real. Reformula a dívida: cadeia linear (tip AOS-128, 41 módulos), sem merge; resgate dos seams uncommitted (AOS-144, feito); reconciliação dos 2 `integration`; enforcement de produção (kernel `Call.Credential`, `NewProductionSecure`, cadeia real de hooks, espinha de token condicional a D4). 19 tickets AOS-144–162. Estimativas de PR-0.a condicionais ao build-spike AOS-145. | Equipa AOS |

---

*Nota: PR-0 é predecessor da execução de código de UI da EPIC-13. O long-pole de identidade (AOS-156/160/162) é condicional à decisão D4 (autoridade de identidade — não há IdP/binding humano↔NHI reais), um bloqueador não-técnico a escalar ao dono; até lá a identidade é demo-only self-minted, sem reivindicar não-forjabilidade.*
