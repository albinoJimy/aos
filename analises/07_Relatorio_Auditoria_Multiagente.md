# Relatório de Auditoria Multiagente AOS — v3

| Campo | Valor |
|---|---|
| Documento | `analises/07_Relatorio_Auditoria_Multiagente.md` |
| Data | 2026-07-24 |
| Estado auditado | working tree ~22:00 (HEAD `0fec431`, com alterações por commitar) |
| Tipo | Auditoria adversarial multi-perspectiva (Carta ↔ codebase) |
| Artefactos de trabalho | `analises/phase1_findings_normalized.md` (62 achados Fase 1) |

## 1. Enquadramento e método

Foi executado o workflow de três fases concebido para este pedido:

1. **Fase 0 — Âncoras:** fixou-se o escopo (`specs/00_AOS_Carta.md`, `specs/00_System_Spec.md`, `specs/01_Engineering_Standards_e_Handoff.md`, `_BRIEF.md`, `tecnica/`, `docs/adr/`) e o commit pinado. As âncoras foram as três fundações (RM, NHI, execução durável), os entrypoints canónicos (`integration.NewSecuredRuntime`, `referencemonitor.NewProductionSecure`, `cmd/aos`, `cmd/aos-demo`), as decisões FIXAS da Carta §4 e os 10 princípios não-negociáveis.
2. **Fase 1 — Avaliação paralela:** 12 agentes especializados avaliaram o codebase de forma independente e read-only, produzindo 62 achados normalizados em `analises/phase1_findings_normalized.md`.
3. **Fase 2 — Contra-exame:** 4 agentes fiscais adversariais re-avaliaram os achados ALTO/CRÍTICO, confirmando, refutando ou reclassificando consoante emendas, excepções documentadas e distinção componente-vs-nó.
4. **Fase 3 — Síntese:** este relatório.

A regra de saída é **fail-closed**: só achados com evidência `caminho:linha` ou comando reproduzível foram aceites; citações não verificáveis foram descartadas.

## 2. Sumário executivo

**VEREDICTO GLOBAL: VERMELHO.**

A v1 do AOS — tal como declarada no Definition-of-Done da Carta (§5) — **não está feita**. As bibliotecas nucleares são genuínas e bem testadas, e a postura de segurança é fail-closed, mas o **nó `aos` de produção não monta as fundações que a documentação afirma activas**: execução durável, PDP/policy-as-code assinado, identidade real (OIDC/WebAuthn/attestation), redacção de PII e eval-gate de admissão permanecem como código entregue mas **não cablados** no caminho runtime. Acumula-se ainda dívida documental material ( contagens de epics/tickets, `_BRIEF.md` desactualizado, matriz de rastreabilidade parada, catálogo de ADRs inconsistente) que dificulta a execução por um agente/executor novo.

Não foi encontrado exploit activo no nó: os defaults deny-all/no-op mantêm-no seguro no estado actual. Contudo, isso é compatível com um produto **não operacional** para trabalho real — exactamente o achado "forma sobre-reivindicada" que o painel `wamnbffrk` já sinalizara e que a emenda 1.1 da Carta tentou evitar exigindo D4 primeiro.

## 3. Veredictos por dimensão

| Dimensão | Veredito | Nota-chave |
|---|---|---|
| ARQ — Arquitectura de Plataforma | **VERMELHO** | Inversão canónica real: `substrate/sandbox` importa `kernel/reference-monitor`; `control-plane/orchestrator` importa `platform/identity`. Não há gate de fronteiras de camadas. |
| SEC — Segurança | **AMARELO** | Fundações de biblioteca fortes (RM não contornável, NHI EdDSA, redacção de segredos); mas o nó corre com TaintGate inerte, PDP deny-all vazio e 4-eyes sem attestation. |
| DUR — Execução Durável | **AMARELO** | Bibliotecas de replay/idempotência/saga bem testadas; nó usa defaults no-op, sem checkpoint, ledger, capturer ou dispatcher durável. |
| COM — Completude | **AMARELO** | A maior parte dos tickets `Done` amostrados tem implementação+testes; o gap concentra-se na fronteira identidade real vs nó zero-dep. |
| COE — Coerência Interna | **AMARELO** | Documentos subordinados (`_BRIEF`, `INDICE.md`, `System Spec §11`, `AGENTS.md`) ficaram presos ao mundo pré-EPIC-13/16. |
| RAS — Rastreabilidade | **VERMELHO** | Matriz de rastreabilidade canónica desactualizada (118 tickets/11 epics vs 16 epics/AOS-177 reais) e CI não a regenera. |
| VIA — Viabilidade de Execução | **VERDE** | Alvos `make`, scripts CI, Dockerfile e IaC dev correspondem à documentação; apenas erros cosméticos de help. |
| SUP — Supply-Chain | **AMARELO** | Zero-dep do nó respeitado e guardado executavelmente; baseline govulncheck de `platform/attestation` sem dono/remediação e com entrada estruturalmente impossível. |
| OBS — Observabilidade/Evals | **AMARELO** | Spans GenAI reais e `otel-genai` substancial; redacção de PII, wide events e eval-gate de admissão não ligados no runtime. |
| GOV — Governação | **VERMELHO** | Motor Cedar e bundle assinado existem, mas o nó de produção não os carrega; D6/D7 não impõem soberania ao recurso real. |
| ADV — Adversarial | **AMARELO** | Não se falsificou mediação total nem replay determinístico; confirmaram-se sobre-reivindicações de egress (IaC dev fail-open, OTLP sem allowlist) e timeout do adaptador GW. |
| UXD — UX/DX | **AMARELO** | Caminho feliz funciona; documentos primários induzem em erro sobre tamanho do backlog e estado do código. |

## 4. Achados críticos e altos (após contra-exame)

| ID | Dim | Sev | Título | Estado pós-contra | Evidência chave | Implicação |
|---|---|---|---|---|---|---|
| ARQ-01 | ARQ | CRÍTICO | Substrato `sandbox` importa directamente `kernel/reference-monitor` | **CONFIRMADO** | `packages/substrate/sandbox/mediated.go:7`, `network/egress_filter.go:7`, `network/policy.go:14`; regra "substrato não conhece camadas acima" (`AGENTS.md:83`) | Violação estrutural da invariante de camadas; não há excepção documentada. |
| ARQ-04 | ARQ | MÉDIO* | `control-plane/orchestrator` importa `platform/identity` | **CONFIRMADO** | `packages/control-plane/orchestrator/delegation.go:49`; `scheduler/go.mod:18` arrasta a mesma dependência indirecta | Inversão canónica `control-plane → kernel → platform`; sem ADR/emenda que a formalize. |
| SEC-01 | SEC | ALTO | TaintGate do nó é no-op: conjunto `Privileged` vazio | **CONFIRMADO** | `packages/cmd/aos/bootstrap.go:537-547` (não passa `Privileged`); `packages/integration/secured.go:155-158` (`NewStaticPrivilegedSet` vazio); `packages/kernel/reference-monitor/taint_gate.go:79-80,92` | O princípio "Untrusted não comanda" é inerte no caminho real; só o deny-all do PDP vazio o mascara. |
| DUR-01 | DUR | ALTO | Nó corre runs sem execução durável | **CONFIRMADO** | `packages/integration/secured.go:224`; `packages/cmd/aos/bootstrap.go:528-546`; `packages/kernel/agent-runtime/loop.go:138-170` (defaults no-op/direct) | Contradiz a System Spec que apresenta durabilidade como fundação entregue. |
| DUR-02 | DUR | ALTO | Fencing de escritas ausente no nó | **CONFIRMADO** | `packages/cmd/aos/service.go:411-413` (comentário afirma protecção); `grep NewFencedAppender/NewWorker` fora de `_test.go` → zero | Sobre-reivindicação: o comentário descreve mecanismo não cablado. |
| GOV-01 | GOV | ALTO | Nó de produção não carrega bundle PDP assinado | **CONFIRMADO** | `packages/cmd/aos/bootstrap.go:508-510`; `packages/integration/secured.go:151-153` (`pdp.NewUnloaded`) | A "governação REAL a mediar cada tool call" (DoD §5) não existe no runtime; só deny-all vazio. |
| GOV-02 | GOV | MÉDIO* | Read-path D7 não impõe soberania ao recurso/run | **CONFIRMADO** (reclass. de ALTO) | `packages/cmd/aos/sovereignty.go:103-117,124-131` | Leitor com board válido pode ler runs de qualquer região; código admite o stub. |
| RAS-01 | RAS | ALTO | Matriz de rastreabilidade desactualizada | **CONFIRMADO** | `tecnica/16_Rastreabilidade_RTM.md:30` (118 tickets/11 epics); 16 ficheiros `specs/EPIC-*.md`; tickets até AOS-177 | A cadeia ticket→ADR→código→teste→gate não é confiável para o estado actual. |
| ADV-02 | ADV | MÉDIO* | Adaptador HTTP do GW é fail-open em timeout/TLS | **CONFIRMADO** | `packages/platform/model-gateway/internal/adapters/openai_http.go:60-63` (aceita `http.DefaultClient`); contraste com hardening do MCP (`packages/platform/registry/mcp/remote.go:17-20,62-76`) | Viola princípio fail-closed; ainda não montado no nó, mas é dívida não declarada. |
| UXD-01 | UXD | ALTO | Contagem errada "12 epics / 128 tickets" | **CONFIRMADO** | `AGENTS.md:7`; `specs/INDICE.md:16`; `_BRIEF.md:158-188`; `ls specs/EPIC-*.md \| wc -l` = 16 | Documentos de orientação primária induzem em erro sobre o backlog. |
| UXD-02 | UXD | ALTO | `packages/README.md` afirma esqueleto sem lógica de negócio | **CONFIRMADO** | `packages/README.md:5-7`; `find packages -name go.mod \| wc -l` = 45/46 | Contradiz a realidade do repo e deslegitima o trabalho já entregue. |

\* Severidade original ALTO; confirmado como gap real mas reclassificado para MÉDIO no contra-exame por estar declarado como dívida/componente externo.

## 5. Achados refutados ou reclassificados no contra-exame

| ID | Decisão pós-contra | Porquê |
|---|---|---|
| ARQ-02 / ARQ-03 | RECLASS. MÉDIO | Imports `platform → kernel` e `model-gateway → control-plane` são adaptadores finos; a excepção do GW é documentada em `tecnica/06`. Mantêm-se como inversões formais, mas sem impacto arquitectural grave. |
| COM-01 | **REFUTADO** | A emenda 1.3 da Carta e a excepção zero-dep escopam a attestation/WebAuthn fora do binário do nó. "RESOLVIDO em CÓDIGO" do ADR-016 §4 refere-se à existência da porta/impl externa, não ao wiring do nó. |
| COM-02 | **REFUTADO** | O componente OIDC real foi entregue e testado; o nó usa allowlist demo como fallback quando nenhum IdP está provisionado. Isso é consistente com a emenda 1.3. |
| GOV-02 | RECLASS. MÉDIO | O código admite explicitamente que a região selada é a declarada pelo leitor e marca a verificação por-run como "DEFERIDO". É dívida declarada, não bug escondido. |
| SEC-03 | **REFUTADO** | Subsume-se em GOV-01: o default `pdp.NewUnloaded()` é deny-all fail-closed e documentado como estado esperado até bundle provisionado. Não representa risco activo. |
| ADV-01 | **REFUTADO** | A rede Docker dev é `internal=false` porque `dev.tfvars` define allowlists não-vazias; a semântica é de etiquetas para enforcement a jusante, não firewall de bridge. |
| ADV-03 | **REFUTADO** | Exportador OTLP é opt-in (vazio ⇒ NoopTracer) e os spans transportam hashes, nunca payloads. Aceitar `http` é legítimo para setups dev. |
| DUR-05 | RECLASS. TÉCNICO MENOR | `tecnica/02` §4.1 está desactualizado textualmente (o loop já usa `ActivityDispatcher`), mas a dívida de wiring mantém-se. |

## 6. Implicações para o DoD da v1 (Carta §5)

| Critério DoD | Estado | Racional |
|---|---|---|
| O nó `aos` corre e hospeda um run fim-a-fim | 🟡 Parcial | O nó corre (`VIA` verde), mas sem durabilidade, governação real nem identidade real no runtime. |
| Interface externa mínima estável (CLI + API stdlib + SSE) | 🟢 Cumprido | Confirmado pelos achados de VIA/OBS. |
| Cadeia de governaça REAL a mediar cada tool call | 🔴 Não | O PDP não é carregado; 4-eyes é stub; taint é inerte. A mediação é deny-all vazio, não decisão de política. |
| Critérios sistémicos do `00_System_Spec.md §13` | 🟡 Parcial | Implementados como bibliotecas; não montados no nó. |
| Gates fail-closed verdes | 🟢 Cumprido | Scripts existem, sintaxe válida, Dockerfile conforme, CI coerente. |
| D4 desbloqueado (token spine real) | 🟡 Parcial | Token spine (Camada A) existe; autoridade real completa (Camada B) entregue como componentes, mas **não ligada ao nó**. |

## 7. Recomendações

1. **Arquitectura:** remover ou formalizar as inversões de camadas (ARQ-01, ARQ-04); introduzir um gate de lint de imports entre camadas na CI.
2. **Governação:** decidir se a v1 corre com bundle PDP real ou se o DoD/ADR-016 actualizam o estado "deny-all vazio" como aceitável; se a opção for real, ligar `pdp.Open` e trust-anchor OOB no `cmd/aos`.
3. **Execução durável:** ligar checkpointer, capturer e dispatcher durável no `NewSecuredRuntime`/`cmd/aos`, ou actualizar a System Spec para não afirmar durabilidade como capacidade entregue no nó.
4. **Identidade:** clarificar no `EPIC-15`/`EPIC-16` e no ADR-016 que "ENTREGUE" significa componente externo entregue, não wiring no nó; ou completar o wiring.
5. **Documentação:** reconciliar `_BRIEF.md`, `AGENTS.md`, `specs/INDICE.md`, `packages/README.md` e `System Spec §11` com os 16 epics e 18 ADRs reais; actualizar a RTM ou remover a pretensão de que é canónica.
6. **Supply-chain:** limpar a baseline `govulncheck.txt` de `platform/attestation` (adicionar dono/remediação ou remover entradas impossíveis); ligar `scripts/ci/package.sh` e `sbom.sh` ao Makefile/CI.
7. **Observabilidade:** ligar o motor de redacção AOS-091 ao Event Store, memória, spans e audit; ou alterar o seu `doc.go`; corrigir o span duplicado `execute_tool` no worker ou a constante redeclarada no sandbox.
8. **Segurança:** corrigir o adaptador HTTP do Model Gateway para não aceitar `http.DefaultClient` sem timeout/TLS; propagar o hardening usado no MCP.

## 8. Anexo A — Artefacto de trabalho

A listagem completa dos 62 achados da Fase 1 (com citações `ficheiro:linha`, evidências e vereditos individuais) encontra-se em:

- `analises/phase1_findings_normalized.md`

Esse ficheiro serve de entrada para tickets de remediação específicos (formato `AOS-NNN`).

---

*Relatório produzido por workflow multiagente: 12 agentes especializados (Fase 1) + 4 fiscais adversariais (Fase 2) + síntese final (Fase 3). Todos os achados foram exigidos a vir acompanhados de evidência verificável (`caminho:linha` ou comando reproduzível).*