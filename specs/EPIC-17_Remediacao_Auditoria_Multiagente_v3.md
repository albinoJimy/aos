# EPIC-17 — Remediação dos Achados da Auditoria Multiagente v3

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | EPIC-17 — Remediação dos Achados da Auditoria Multiagente v3 |
| Versão | 1.0 |
| Data | 2026-07-24 |
| Estatuto | PROPOSTA — depende da ratificação do relatório `analises/07_Relatorio_Auditoria_Multiagente.md` |
| Epic anterior | EPIC-16_Autoridade_Identidade_Real_D4.md |
| Fontes de verdade | `specs/00_AOS_Carta.md`, `specs/00_System_Spec.md`, `specs/01_Engineering_Standards_e_Handoff.md`, `analises/07_Relatorio_Auditoria_Multiagente.md`, `docs/runbooks/RB-Auditoria_Multiagente_CartavCodebase.md` |

---

## 1. Contexto e motivação

A auditoria multiagente de 2026-07-24 (`analises/07_Relatorio_Auditoria_Multiagente.md`) concluiu que o AOS v1 **não está feito** segundo o DoD da Carta §5. As bibliotecas nucleares (RM, NHI, durabilidade, Cedar, OTel) são reais e bem testadas, mas o nó `aos` de produção não as monta; a documentação primária está desactualizada face aos 17 epics/AOS-189 reais; e existem inversões de fronteira canónica e gaps de hardening.

Este epic agrupa o trabalho transversal de remediação. Não substitui os epics funcionais — articula-se com eles, produzindo os tickets de wiring, correção documental e gates de CI que faltam para que a v1 possa ser declarada.

## 2. Objectivo deste epic

Tornar o nó `aos` e a documentação que o acompanha consistentes com a Carta, de modo a que o DoD da v1 (Carta §5) possa passar a **verde**.

## 3. Não-objectivos

- Não re-litigar decisões FIXAS da Carta §4.
- Não reescrever do zero componentes já entregues (RM, agent-runtime, PDP, `otel-genai`, etc.). O trabalho é **wiring, gates e documentação**.
- Não implementar UI web bespoke nem SaaS multi-tenant — fora do escopo da v1 (D1(b) condicional).
- Não substituir a autoridade de identidade externa (EPIC-16); este epic liga-a ao nó quando aplicável.

## 4. Critérios de saída do epic

- [ ] Todas as inversões de fronteira canónica estão resolvidas **ou** formalizadas numa emenda/ADR.
- [ ] Existe um gate de CI que detecte violações do sentido das dependências entre camadas.
- [ ] O nó `aos` carrega uma política PDP real quando provisionada, mantendo o fallback deny-all fail-closed quando não.
- [ ] O nó `aos` monta execução durável (checkpoints, captura, ledger, dispatcher durável) quando configurado.
- [ ] A documentação primária (`_BRIEF.md`, `AGENTS.md`, `specs/INDICE.md`, `packages/README.md`) reflete os 16 epics e o estado real do código.
- [ ] A matriz de rastreabilidade (`tecnica/16_Rastreabilidade_RTM.md`) cobre AOS-001..AOS-189 e os ADRs materializados.
- [ ] Os gates de supply-chain (`package.sh`, `sbom.sh`) estão ligados ao Makefile/CI.
- [ ] Todos os achados CRÍTICO/ALTO da auditoria v3 estão encerrados ou convertidos em tickets justificados.

## 5. Tabela resumo de tickets

| ID | Título | Tipo | Est. | Prio | Dependências | Área |
|---|---|---|---|---|---|---|
| AOS-178 | Gate de lint de fronteiras de camadas | feature | M | P0 | AOS-003, ARQ-06 | EPIC-01/03 |
| AOS-179 | Resolver inversões canónicas de imports | refactor | M | P0 | AOS-178, ARQ-01/04 | EPIC-01/03/07 |
| AOS-180 | Montar execução durável no composition-root do nó | feature | L | P0 | DUR-01/02/03, AOS-157 | EPIC-02 |
| AOS-181 | Carregar bundle PDP real no nó de produção | feature | M | P0 | GOV-01/05, AOS-156 | EPIC-09 |
| AOS-182 | Implementar read-path soberano fail-closed D6/D7 no nó | feature | L | P1 | GOV-02/03, AOS-160 | EPIC-09 |
| AOS-183 | Activar TaintGate real no nó de produção | feature | M | P0 | SEC-01, AOS-005 | EPIC-07 |
| AOS-184 | Hardening do adaptador HTTP do Model Gateway | fix | S | P1 | ADV-02 | EPIC-06 |
| AOS-185 | Reconciliação documental primária | docs | M | P0 | COE-01/02/03, UXD-01/02 | Transversal |
| AOS-186 | Regenerar e validar RTM na CI | feature | M | P0 | RAS-01/02 | EPIC-11 |
| AOS-187 | Limpar baseline govulncheck e ligar package/sbom ao CI | fix | M | P1 | SUP-01/02 | EPIC-05 |
| AOS-188 | Ligar motor de redacção AOS-091 e corrigir semconv duplicada | feature | M | P1 | OBS-01/02/04 | EPIC-08 |
| AOS-189 | Cablar eval-gate de admissão no registry e memória procedural | feature | L | P2 | OBS-03, AOS-142 | EPIC-08/09 |

## 6. Tickets detalhados

### AOS-178 — Gate de lint de fronteiras de camadas

**Critérios de aceitação:**
- [x] Script em `scripts/ci/` que, dado um grafo canónico de camadas (`control-plane → kernel → platform/substrate`), falhe se encontrar importações inversas.
- [x] Cobertura de todos os módulos `packages/*` (não só do `kernel/reference-monitor`).
- [x] Self-test que prove que uma inversão sintética bloqueia o gate (`scripts/ci/selftest.sh`, secção L).
- [x] Documentação do gate em `specs/01_Engineering_Standards_e_Handoff.md` §4.

**Notas:** Pode reutilizar a lógica de `packages/kernel/reference-monitor/archlint`, mas generalizá-la para todo o repo.

**DoD:** gate verde, testado, mergeado; execução em `make ci`.

---

### AOS-179 — Resolver inversões canónicas de imports

**Critérios de aceitação:**
- [x] `packages/substrate/sandbox` deixa de importar `kernel/reference-monitor` (ARQ-01) — **formalizado como excepção intencional no ADR-019** (remoção total exigiria redefinir a fronteira RM↔sandbox na v1).
- [x] `packages/control-plane/orchestrator` deixa de importar `platform/identity` (ARQ-04) — **formalizado como excepção intencional no ADR-019** (contrato/eventos remetidos para refactor futura).
- [x] Se uma inversão for intencional e permanente, emitir ADR/emenda que a autorize e actualizar `AGENTS.md` §3.
- [x] O gate AOS-178 passa no repo depois das alterações.

**Nota (2026-07-25):** após análise manual, a refactorização completa das inversões
ARQ-01..ARQ-05 seria desproporcionada para a v1. O **ADR-019** regista as excepções
intencionais, a baseline do `layer-lint` passa a citá-lo, e o gate continua verde.
A regra canónica mantém-se como objectivo de desenho; qualquer nova inversão exige
justificação e actualização do ADR (supersessão) ou remoção.

**DoD:** todos os imports resolvidos **ou** documentados; `make ci-lint` e AOS-178 verdes.

---

### AOS-180 — Montar execução durável no composition-root do nó

**Critérios de aceitação:**
- [ ] `packages/cmd/aos/bootstrap.go` passa checkpointer, capturer, ledger e dispatcher durável ao `NewSecuredRuntime` quando configurado. **[EMENDA AOS-191 — ver abaixo: «quando configurado» é INSUFICIENTE.]**
- [ ] Fallback seguro quando o ES/configuração não estiver disponível: deny-all/no-op fail-closed.
- [ ] Teste de aceitação que prove retoma de run após `SIGKILL` simulado (ou restart do processo).
- [ ] `tecnica/02_Agent_Runtime_Execucao_Duravel.md` §4 actualizado para reflectir o wiring real.

> **EMENDA AOS-191 (registo de suficiência de critério, não de execução).** O 1.º CA acima foi
> satisfeito à letra e mesmo assim deixou a capacidade **inalcançável pelo binário entregue**: a
> fórmula «**quando configurado**» exige apenas que o composition-root **reaja** a um campo de
> `Config`, e nunca a **superfície de configuração** que permite escrevê-lo. Como `Config` vive em
> `package main`, sem uma variável de ambiente a lê-la nem um embedder externo a podia preencher —
> `grep AOS_DURABLE .` devolvia **0** e o único escritor em toda a árvore era um teste. O defeito
> sobreviveu à v3 (DUR-01) e reapareceu na v4 como **REG-01 ≡ STR-09 ≡ PLA-03**.
>
> **Regra que fica registada:** um CA de *wiring* TEM de nomear a **via de activação no artefacto**
> (variável de ambiente / flag / ficheiro de config) e a sua **documentação de operador**, não só o
> campo de `Config`. Formulação suficiente: «*…quando `X` estiver activa, sendo `X` escrita a partir
> de `<VAR>` no entrypoint e documentada em `deploy/node/README.md`*». Um CA que se possa fechar sem
> que exista **qualquer** caminho do binário para ligar a capacidade **não** é um CA de wiring — é
> um CA de biblioteca.
>
> Fechado por AOS-191 (`AOS_DURABLE_EXECUTION`, `packages/cmd/aos/main.go` → `nodeConfigFromEnv`).

**Dependências:** AOS-157 (WindowPort/Dispatcher), AOS-018 (fencing-aware ES) opcional para fechar TOCTOU.

**DoD:** gate `ci-replay` e `ci-dr-e2e` verdes; cobertura do `agent-runtime` mantida ou melhorada.
**DoD (emenda AOS-191):** a capacidade é **alcançável pelo binário** — existe variável de ambiente
que a activa, o banner declara o estado composto, e `deploy/node/README.md` documenta-a.

---

### AOS-181 — Carregar bundle PDP real no nó de produção

**Critérios de aceitação:**
- [ ] O nó `aos` aceita configuração de bundle de políticas Cedar (`AOS_POLICY_BUNDLE_DIR` + trust anchor OOB).
- [ ] Quando configurado, chama `pdp.Open` com `WithTrustAnchor` apontando para ficheiro montado fora do bundle.
- [ ] Quando não configurado, mantém `pdp.NewUnloaded()` deny-all fail-closed.
- [ ] Teste de aceitação: nó com bundle válido permite uma ação permitida; nó sem bundle nega tudo.
- [ ] ADR-011 e `AGENTS.md` §7 actualizados se a semântica de fallback mudar.

**Dependências:** AOS-156 (token spine no vault), ADR-011.

**DoD:** `ci-policy` e `ci-apex` verdes; nenhum segredo no bundle ou trust anchor em log/span.

---

### AOS-182 — Implementar read-path soberano fail-closed D6/D7 no nó

**Critérios de aceitação:**
- [x] `readGovernance.authorize` verifica a região de residência do run contra a região declarada pelo leitor, recusando leituras cross-board não autorizadas.
- [ ] Selo de leitura sensível (D6) só é emitido quando o recurso/operador é classificado como sensível, e transporta `PayloadRef`/`KeyRef` conforme ADR-016.
- [ ] Testes de API demonstrando recusa cross-region e auditoria correcta.
- [ ] `docs/adr/ADR-016-fronteira-confianca-ui.md` §4 actualizado se o stub for promovido.

**Dependências:** AOS-160 (HITL assinatura), AOS-162 (attestation 4-eyes).

**DoD:** `ci-security`, `ci-apex` verdes; spans/audits sem PII.

---

### AOS-183 — Activar TaintGate real no nó de produção

**Critérios de aceitação:**
- [ ] O nó `aos` carrega um conjunto `Privileged` real a partir de configuração (ficheiro de capabilities privilegiadas) ou rejeita arranque sem ele em modo production.
- [ ] Quando em modo dev/demonstração, o conjunto pode ser vazio desde que explicitamente assinalado como DEMO.
- [ ] Teste de aceitação: tool call marcada como `trusted` mas sem origem trusted é bloqueada.
- [ ] `packages/kernel/reference-monitor/taint_gate.go` e `AGENTS.md` §7 actualizados.

**Dependências:** AOS-005 (TaintGate), SEC-01.

**DoD:** gate `ci-security` verde; nenhuma regressão nos testes de prompt injection.

---

### AOS-184 — Hardening do adaptador HTTP do Model Gateway

**Critérios de aceitação:**
- [ ] `NewOpenAIHTTPAdapter` rejeita `client == nil` ou instancia um client com timeout, TLS e limites de redirect.
- [ ] Validação do `BaseURL` contra esquema `https` e allowlist de egress.
- [ ] Teste que prova que `http.DefaultClient` não é mais usado por omissão.
- [ ] Alinhado com o hardening já feito em `packages/platform/registry/mcp/remote.go`.

**DoD:** `ci-routing`, `ci-sast` verdes; teste de regressão para slow-loris/timeout.

---

### AOS-185 — Reconciliação documental primária

**Critérios de aceitação:**
- [x] `_BRIEF.md` §1 actualizado para "runtime de referência deployável" (em vez de "blueprint/plataforma standalone"), com referência à Carta.
- [x] `AGENTS.md` corrige "doze epics" → "dezasseis epics" e "45 módulos" conforme realidade (46 com attestation).
- [x] `specs/INDICE.md` actualizado com EPIC-13..EPIC-17 e ranges AOS-001..AOS-189.
- [x] `packages/README.md` remove a afirmação "apenas esqueleto"; lista as subpastas de `packages/` e indica que a lógica é entregue.
- [x] `docs/adr/README.md` e `specs/00_System_Spec.md` §11 alinhados com ADR-003, ADR-016, ADR-017, ADR-018.

**DoD:** todos os documentos de orientação primária consistentes entre si e com a árvore real; `ci-lint` passa.

---

### AOS-186 — Regenerar e validar RTM na CI

**Critérios de aceitação:**
- [x] Script que regenere as secções §4–§6 da `tecnica/16_Rastreabilidade_RTM.md` a partir de `specs/EPIC-*.md`, `docs/adr/*.md` e ficheiros Go.
- [x] Gate de CI que falha se um ADR canónico tiver 0 tickets ou se um AOS-NNN citado não existir no backlog.
- [x] Cobertura completa de AOS-001..AOS-189, 17 epics e todos os ADRs canónicos.
- [x] O gate 2b prometido em `specs/01_Engineering_Standards_e_Handoff.md` §4 existe e valida referências cruzadas.

**DoD:** RTM actualizada e verificada automaticamente; `ci-lint`, `ci-rtm` e `ci-ref-lint` verdes.

---

### AOS-187 — Limpar baseline govulncheck e ligar package/sbom ao CI

**Critérios de aceitação:**
- [x] Revisão de `scripts/ci/baseline/govulncheck.txt`: cada entrada de `platform/attestation` tem dono + remediação; entradas estruturalmente impossíveis removidas.
- [x] `Makefile` ganha alvos `ci-package` e `ci-sbom` que invocam `scripts/ci/package.sh` e `scripts/ci/sbom.sh`.
- [x] `.github/workflows/ci.yml` inclui os novos gates num job de entrega (não bloqueante até EPIC-10, mas visível).
- [x] SBOM cobre também o componente externo de autoridade (`packages/platform/attestation`) quando existir release.

**DoD:** baseline coerente; package/sbom executáveis via `make`; nenhum segredo no SBOM.

---

### AOS-188 — Ligar motor de redacção AOS-091 e corrigir semconv duplicada

**Critérios de aceitação:**
- [x] `packages/substrate/redaction` é ligado ao Event Store, `platform/memory`, `substrate/otel-genai` e `platform/audit` (ou o `doc.go` é actualizado para refectar o escopo real).
  - *Nota de encerramento (AOS-195, CA2): este CA fechou pela **porta de escape disjuntiva** — «ou o `doc.go` é actualizado» —, **não** pela cablagem. E até ao commit `d355551` nem essa via estava genuinamente satisfeita: o texto substituto do `doc.go` afirmava uma cablagem inexistente, pelo que o `[x]` descrevia um facto falso. AOS-195 corrigiu o `doc.go` para o escopo REAL, e é isso — e só isso — que o `[x]` hoje atesta. Mantém-se marcado porque desmarcá-lo tornaria o CA falso no sentido inverso: a disjunção está satisfeita. A **ligação substantiva** do motor de redacção aos quatro consumidores continua por fazer e tem eixo próprio em **AOS-208** (`specs/EPIC-18` §8-bis) — não é dívida escondida atrás deste `[x]`.*
- [x] Remover a redeclaração de `OpExecuteTool` em `packages/substrate/sandbox/tracer.go`; usar `otelgenai.OpExecuteTool`.
- [x] Testes que provem que PII de exemplo não persiste em eventos/spans/audits.
- [x] `tecnica/08_Observabilidade_Evals.md` actualizado.

**DoD:** `ci-memory`, `ci-evalgate`, `ci-apex` verdes; spans sem payloads sensíveis. ✅

---

### AOS-189 — Cablar eval-gate de admissão no registry e memória procedural

**Critérios de aceitação:**
- [x] `packages/platform/eval/gateadapter` é instanciado em `packages/integration` ou `packages/control-plane/registry/promotion` e `packages/platform/memory/procedural`.
- [x] Uma skill/memória auto-escrita só é promovida após trace-diffing + golden-set >= 90%.
- [x] Teste de end-to-end que prove bloqueio de regressão e aprovação de melhoria.
- [x] ADR-012 e `tecnica/08` §8.2 actualizados.

**Dependências:** AOS-142 (eval harness), OBS-03.

**DoD:** `ci-evalgate` verde; comportamento fail-closed mantido. ✅

## 7. Riscos e dependências

| Risco | Mitigação |
|---|---|
| EPIC-17 cresce e engole escopo funcional | Cada ticket mapeia para um epic funcional; manter foco em wiring/gates/docs. |
| Resolver inversões de camadas quebra interfaces | Fazer refactor por etapas; manter testes de integração existentes. |
| D4 ainda não aprovado por Segurança/Arquitectura | AOS-181/182 mantêm fallback fail-closed; não se declara "identidade real" até sign-off. |
| RTM automática torna-se frágil | Usar parsing simples (headers `## AOS-NNN`, front-matter ADR) e validar com self-tests. |

## 8. Handoff / DoR / DoD

### Definition of Ready (DoR)

- [ ] Relatório `analises/07_Relatorio_Auditoria_Multiagente.md` ratificado.
- [ ] Cada ticket deste epic tem dependências externas identificadas e disponíveis.
- [ ] Decisão tomada sobre v1 zero-dep-com-stubs vs real-wiring (impacta AOS-180/181/182/183).

### Definition of Done (DoD) por ticket

- [ ] Código/alteração documental mergeada na branch `feature/AOS-NNN-<slug>`.
- [ ] Testes unitários/integração passam (`make ci-test`).
- [ ] Lint e gates relevantes passam (`make ci-lint`, gates específicos).
- [ ] Scan de segredos limpo (`make ci-secrets`).
- [ ] Documentação actualizada quando aplicável.
- [ ] Review por pelo menos um par (preferencialmente pelos papéis de Arquitectura + Segurança para os tickets P0).

---

*EPIC-17 proposto como resposta aos achados da auditoria multiagente v3. A sua aprovação depende do dono do produto e dos sign-offs de Arquitectura/Segurança (Carta §5).*