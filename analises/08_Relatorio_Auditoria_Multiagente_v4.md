# Relatório de Auditoria Multiagente AOS — v4

| Campo | Valor |
|---|---|
| Documento | `analises/08_Relatorio_Auditoria_Multiagente_v4.md` |
| Data | 2026-07-26 |
| Estado auditado | HEAD `cc9895fe160ca0434a691611f843fc1c131bfbff` (2026-07-26 00:35, `feat(AOS-189)`), branch `feature/AOS-128-ux-dx-tests`, **árvore de trabalho limpa** (`git status --porcelain` sem saída) |
| Tipo | Auditoria adversarial multi-perspectiva, 2.ª ronda (lentes NOVAS + contra-exame) |
| Auditoria anterior | `analises/07_Relatorio_Auditoria_Multiagente.md` (v3, 2026-07-24, HEAD `0fec431`) |
| Metodologia | `docs/runbooks/RB-Auditoria_Multiagente_CartavCodebase.md` |
| Plano de remediação em curso | `specs/EPIC-17_Remediacao_Auditoria_Multiagente_v3.md` (Estatuto: **PROPOSTA**) |

---

## 1. Enquadramento e método

Esta ronda **não repete** a v3. A v3 correu 12 lentes genéricas (ARQ/SEC/DUR/COM/COE/RAS/VIA/SUP/OBS/GOV/ADV/UXD) e respondeu à pergunta *«a documentação afirma capacidades que o nó não monta?»*. A v4 correu **oito lentes novas**, cada uma desenhada para ver um ângulo que a v3 estruturalmente não podia ver:

| Lente | Pergunta que faz |
|---|---|
| **REG** — Verificação de Remediação | Os commits AOS-178..189 fecharam mesmo os achados da v3, ou só marcaram checkboxes e reescreveram texto? |
| **VAC** — Vacuidade dos Testes | A v3 perguntou «existe teste?»; esta pergunta é «o teste **prova** a propriedade que a doc reivindica?» |
| **ORF** — Código Órfão | Lente **inversa** (código→doc): o que existe no repo e não tem cobertura documental, dono ou governação? |
| **DEF** — Auditor de Deferimentos | Que dívida se esconde sob rótulos legítimos («deferido», «demo-grade», «non-goal DATADO») sem eixo, dono ou gatilho? |
| **PLA** — Crítico do Plano | O EPIC-17 cobre o que a v3 encontrou? Os seus critérios são falsificáveis? Cria mecanismo anti-recorrência ou correcção pontual? |
| **DAT** — Drift de Contratos | Os contratos de interface (C1–C5) e o modelo de dados/eventos documentados correspondem aos tipos Go reais? |
| **STR** — STRIDE vs realidade | Cada mitigação afirmada em `tecnica/17` está cablada no caminho do nó, ou existe só como biblioteca? |
| **CON** — Matriz de Conformidade | Cada linha marcada «Coberto» em `tecnica/14` tem o mecanismo no caminho real do nó implantável? |

Depois das oito lentes correu um **contra-exame adversarial** (4 fiscais) sobre a totalidade dos achados ALTO/CRÍTICO, com mandato explícito de demolir cada achado: verificar citações, procurar excepções documentadas, distinguir componente-vs-nó, distinguir dívida real de deferimento legítimo e detectar dupla contabilização. Os fiscais reproduziram comandos de forma independente.

**Regra de saída (fail-closed):** só sobreviveu o que tem citação `ficheiro:linha` verificada ou comando reproduzível. Achados de severidade inflacionada foram reclassificados; achados sem sustentação foram refutados (ver §5).

Números desta ronda: **8 lentes → achados brutos → contra-exame → 29 achados materiais sobreviventes**, dos quais **25 distintos** (4 são duplicações entre lentes do mesmo facto — assinaladas em §4). **Zero CRÍTICO sobrevive ao contra-exame.**

---

## 2. Sumário executivo

**VEREDICTO GLOBAL: AMARELO-ESCURO — melhoria real face ao VERMELHO da v3, mas o eixo que a v3 identificou como o problema continua aberto.**

Três conclusões, por ordem de importância.

**Primeira: não houve fraude de checkbox.** Este era o teste principal desta ronda e o repo passou-o. Os quatro tickets que fechariam os achados mais graves da v3 (AOS-181 PDP real, AOS-182 soberania por-run, AOS-183 TaintGate, AOS-184 hardening do GW) têm **todos** os critérios de aceitação honestamente por marcar, e os achados correspondentes (GOV-01, GOV-02, SEC-01, ADV-02) persistem no código exactamente como a v3 os descreveu. Ninguém marcou trabalho inexistente como feito. Vários fechos são de qualidade genuína e exemplar — ADR-019 + gate `layer-lint` executável, RTM regenerada e gated, baseline `govulncheck` com dono/remediação/símbolo atingido, reconciliação documental calibrada.

**Segunda: a remediação foi real na camada de documentação, gates e bibliotecas; não foi efectiva na camada que a v3 nomeou — o nó `aos`.** O padrão canónico desta ronda é AOS-180: acrescentou checkpointer, capturer, step-ledger e dispatcher durável ao `Bootstrap`, código real e testado — mas o flag que os activa (`Config.DurableExecution`) não é escrito por nenhum caminho fora de testes, `main.go` nunca o define, não existe variável de ambiente, e `Config` vive em `package main`, pelo que **nem sequer um embedder externo o pode preencher**. DUR-01 continua aberto na prática. O mesmo padrão repete-se em AOS-189 (construtores de eval-gate correctos, testados, sem um único chamador de produção).

**Terceira, e é o achado mais accionável desta ronda: os gates anti-recorrência criados pelo EPIC-17 não correm na CI que bloqueia merges.** `layer-lint`, `rtm` e `ref-lint` existem, são bons, estão em `scripts/ci/run.sh` e no `Makefile` — e **não estão em `.github/workflows/ci.yml`** (verificado: 20 invocações `bash scripts/ci/*.sh` no workflow, nenhuma é estas três; o agregador `gates` tem 18 `needs:` e nenhum é estas três). Um PR que reintroduza uma inversão de camada ou desactualize a RTM obtém `gates` verde. O EPIC-17 construiu a fechadura e não a montou na porta — reproduzindo, à escala do próprio plano, o defeito estrutural que a v3 apanhou no produto.

Aos três, acrescentam-se dois padrões novos que a v3 não viu por não ter as lentes:

- **Sobre-reivindicação introduzida PELA remediação.** O commit `c168f8f` («ligar motor de redação») não tocou em nenhum composition-root; satisfez o CA pela cláusula alternativa («ou o `doc.go` é actualizado») e o texto substituto passou a afirmar que o motor está *«cablado nos composition-roots (cmd/aos, cmd/aos-demo, integration)»* — verificado com `go list -deps`: falso para `cmd/aos` e `integration`. É a única regressão documental nova da ronda.
- **Artefactos de auditoria que mentem por construção.** A coluna de rastreabilidade ameaça→ticket de `tecnica/17_Analise_STRIDE.md` aponta sistematicamente para os tickets errados (prompt injection→AOS-067, que é «rede default-deny»; sandbox escape→AOS-068, que é «filtragem DNS»; audit tamper-evident→AOS-071, que é «autoridade escopada»). O documento existe para «rastrear cada mitigação até um ticket executável» (`tecnica/17:19,27`) e faz o oposto. Verificado por mim contra `specs/EPIC-07:48-59`.

**Honestidade sobre o valor marginal desta ronda:** parte substancial dos achados ALTO é continuação directa da v3 (REG-01/DUR-01, REG-02/SEC-01, STR-02/SEC-01). O que é genuinamente NOVO e vale a ronda são cinco itens: PLA-01 (gates fora da CI), ORF-02 (plano de controlo inconfigurável), STR-01 (rastreabilidade STRIDE falsa), VAC-01 (teste de aceitação vacuoso) e DEF-01 (linha de conformidade calibrada acima da realidade). As lentes DAT e CON produziram muito material, mas quase todo reclassificado para MÉDIO no contra-exame — é drift documental real, sem consequência de comportamento.

**Nenhum exploit activo foi encontrado no nó.** O PDP em deny-all e o catálogo de tools vazio mascaram várias lacunas: nenhuma tool de terceiros executa hoje. Isto é uma atenuação genuína e está registada em cada achado — mas é também uma condição de sequência perigosa: ligar AOS-181 (bundle PDP) sem antes ligar AOS-183 (TaintGate) torna o nó permissivo sem acordar a barreira control/data-plane.

---

## 3. Veredictos por dimensão (as 8 lentes novas)

| Lente | Veredicto | Nota-chave |
|---|---|---|
| **REG** — Remediação | **AMARELO** | Sem fraude de checkbox. Fechos genuínos em ARQ-01/04, UXD-01/02, RAS-01 (parcial) e supply-chain. Mas AOS-180 é inalcançável pelo binário e AOS-188/189 fecharam por reformulação ou por construtor sem chamador. |
| **VAC** — Vacuidade dos testes | **AMARELO** | Qualidade de teste acima da média do sector, com anti-vacuidade explícita e deliberada (`seen==0` fatal, poison-tests em `selftest.sh`). Mas o teste de aceitação de AOS-180 é vacuoso e o checklist AOS-169 cita evidência errada em três eixos. |
| **ORF** — Código órfão | **AMARELO** | Superfície de rede minúscula e integralmente enumerável; zero TODO/FIXME em produção; zero binários versionados. Mas 1763 LOC de módulos `*/contract` sem importador/teste/spec, e o plano de controlo do nó não tem caminho de configuração. |
| **DEF** — Deferimentos | **AMARELO** | Postura genuinamente boa: o nó imprime as próprias fronteiras no banner de arranque e a excepção zero-dep é imposta por guarda executável. O defeito é sistemático e de outro tipo: **eixos de deferimento errados ou inexistentes** (cifra do substrato → 3 epics, nenhum com ticket; anti-replay ADR-012 → EPIC-13, que é Frontend). |
| **PLA** — Plano EPIC-17 | **AMARELO** | Plano acima da média (12 tickets rastreáveis, não-objectivos apertados, três mecanismos reais). Mas os mecanismos não estão na CI que bloqueia, o ADR-019 não chegou ao registo da Carta, e o próprio plano já está em drift face à sua execução (8 tickets em trunk, Estatuto PROPOSTA, DoR por satisfazer). |
| **DAT** — Contratos/dados | **AMARELO** | C1 (RM↔PDP) e C2 (RT↔ES) fiéis campo-a-campo e código-de-erro a código-de-erro. C3/C4/C5 divergiram integralmente — e são exactamente os três sem rastreio a `tecnica/12` no código. Causa provável: o «gate 4 — Integração», declarado bloqueante de merge, não existe. |
| **STR** — STRIDE | **VERMELHO** | A cadeia de rastreabilidade do modelo de ameaças está partida (tickets errados). Três das seis categorias não têm mitigação cablada no nó. O documento está na v1.0 de emissão e não conhece a superfície real (API HTTP, SSE, DSAR, OTLP, contentor) — reavaliação que a Carta emenda 1.2 mandou fazer e que nunca chegou lá. |
| **CON** — Conformidade | **AMARELO** | Estrutura do documento genuinamente boa (§2 habilitador-não-alto-risco, §5 lacunas do operador, §7 risco de sobre-afirmação). O problema concentra-se nas linhas «Coberto»: quatro das seis linhas GDPR apoiam-se em mecanismos que o nó não monta. |

---

## 4. Achados ALTO após contra-exame

Nenhum achado sobreviveu como CRÍTICO. As duas candidaturas a CRÍTICO (VAC-01, DEF-01) foram reclassificadas para ALTO pelos fiscais; a terceira (CON-01) desceu a MÉDIO.

| ID | Lente | Sev. final | Afirmação-na-doc (`ficheiro:linha`) | Realidade-no-código (`ficheiro:linha`) | Veredicto | vs v3 |
|---|---|---|---|---|---|---|
| **REG-01**<br>*(≡ STR-09, PLA-03)* | REG | **ALTO** | `tecnica/02_Agent_Runtime_Execucao_Duravel.md:~465`: «A execução durável é **exposta no nó `aos`** através do `integration.SecuredRuntime`…»; commit `93c11d4` «monta execução durável no composition-root do nó» | `packages/cmd/aos/bootstrap.go:186` declara `DurableExecution bool`; `:410`/`:580` são os únicos consumidores; `main.go:119-148` nunca o escreve; `grep AOS_DURABLE .` → 0; único escritor em toda a árvore: `bootstrap_durable_execution_test.go:129`. Agravante do fiscal: `bootstrap.go` é `package main` ⇒ nem um embedder externo o pode preencher | **SOBRE-REIVINDICADO** | v3-AINDA-ABERTO (DUR-01) |
| **REG-02**<br>*(≡ STR-02, parte)* | REG | **ALTO** | `specs/EPIC-17…v3.md:139-142` (AOS-183, P0): «O nó `aos` carrega um conjunto `Privileged` real a partir de configuração … ou rejeita arranque sem ele em modo production» — todos os CA `[ ]` | `packages/integration/secured.go:169-171` (`NewStaticPrivilegedSet()` **vazio**); `:218` compõe o TaintGate com ele; `packages/kernel/reference-monitor/taint_gate.go:91-95` curto-circuita para `allow`; `grep Privileged packages/cmd/aos/*.go` (não-teste) → **0**. Fiscal acrescentou: `production.go:126-141` (`NewProductionSecure`) recusa stubs mas **não** exige conjunto privilegiado não-vazio | **CONFIRMADO** | v3-AINDA-ABERTO (SEC-01) |
| **STR-02** | STR | **ALTO** | `tecnica/17:208,109,221`: separação control/data-plane + taint é «a defesa estrutural mais forte» da fronteira de maior exposição — em presente do indicativo, sem qualificador | Dupla inércia. (1) `packages/kernel/agent-runtime/loop.go:304-318` difere a separação de planos; (2) o TaintGate que o deferimento invoca como compensação está inerte (ver REG-02). **O deferimento é auto-derrotante**: `loop.go:310-311` justifica-se com «a defesa activa … é o default fail-closed do TaintGate», e no nó nenhuma capability é privilegiada | **SOBRE-REIVINDICADO** | v3-AINDA-ABERTO, com evidência nova (circularidade + ausência de porta de config) |
| **PLA-01** | PLA | **ALTO** | `CONTRIBUTING.md:6` «a CI (`.github/workflows/ci.yml`) apenas os **invoca**; nada é duplicado no YAML»; `:73` «o agregador único **gates** (job que depende de TODOS)»; `AGENTS.md:211` «qualquer gate vermelho bloqueia merge» | **Verificado por mim**: `.github/workflows/ci.yml` tem 20 `run: bash scripts/ci/*.sh` (secrets…sbom) e **nenhuma** é `layer-lint.sh`/`rtm.sh`/`ref-lint.py`; `grep -c "layer-lint\|rtm\|ref-lint" ci.yml` → **0**; agregador `gates` em `:266` tem 18 `needs:` sem nenhum destes. Existem só em `scripts/ci/run.sh:30` e `Makefile:71-78`. O self-test §L corre `layer-lint --root "$LAYER_TMP"` (árvore sintética), nunca contra `packages/` | **SOBRE-REIVINDICADO** | **NOVO** |
| **ORF-02** | ORF | **ALTO** | `specs/EPIC-15:440` (CA `[x]`) «`aos steer/pause --run-id … --emitter … --key …` conduz o run»; `:490-491` (CA `[x]`) «`POST /runs/{id}/steer` e `/approve` conduzem/aprovam»; `deploy/node/README.md:46-47` «A API **recusa** bind não-loopback enquanto o canal não estiver autenticado (identidade real **+ operadores**)» | **Verificado por mim**: `grep "Operators\|Approvers" packages/cmd/aos/*.go` (não-teste) → só declarações (`bootstrap.go:140-152`), consumos (`:520`,`:530`), logs (`:660`,`:662`) e um comentário (`main.go:145`). **Zero caminhos de leitura**. ⇒ `steer_authenticator.go:172-178` devolve `ErrUnknownEmitter` a todo o steer/pause; `api.go:684-686` devolve sempre 501 ao `approve`. Agravante do fiscal: `Config`/`Bootstrap` estão em `package main` ⇒ registar uma pubkey exige forkar e recompilar | **SOBRE-REIVINDICADO** | **NOVO** |
| **STR-01** | STR | **ALTO** | `tecnica/17:19,27` define o propósito: «que ticket a rastreia?» / «auditores que precisem de rastrear cada mitigação até um ticket executável» | **Verificado por mim** contra `specs/EPIC-07:48-59`: prompt injection→AOS-067 (é «rede default-deny»; o taint é AOS-069); sandbox escape→AOS-068 (é «filtragem DNS»; a microVM é AOS-064); audit tamper-evident→AOS-071 (é «autoridade escopada»; o audit é AOS-072); mensagens assinadas→AOS-074 (é «gate de risco»; são AOS-073); runaway/admission→AOS-073 (são mensagens assinadas). Desvio **sistemático** de 1-2 posições, não erro pontual. Só 3 mapeamentos batem certo | **CONTRADITÓRIO** | **NOVO** |
| **VAC-01** | VAC | **ALTO**<br>*(de CRÍTICO)* | `tecnica/02:488-492` «…re-corre o mesmo `RunID` e assegura que a tool executou exactamente uma vez — a segunda execução obtém **deduplicação do ledger reconstruído**»; `docs/reports/AOS-169-aceitacao-sistemica.md:77-107` marca §13.3 DURABILIDADE **VERDE** | `bootstrap_durable_execution_test.go:133` cria **uma** instância de `twoTurnToolModel` partilhada pelas duas vidas do nó (`:159`,`:193`); o contador é monotónico (`:85`), logo a 2.ª vida entra em `turn=3` e devolve `Final` **sem emitir tool call**. A asserção `execs == 1` (`:217-219`) passa por a tool **nunca ter sido re-tentada**. `durable/resume.go:60-95` confirma que o `ResumePoint` não carrega a invocação. Fiscal correu o teste: PASS em 0.32s | **SOBRE-REIVINDICADO** | **NOVO** |
| **DEF-01** | DEF | **ALTO**<br>*(de CRÍTICO)* | `tecnica/14_Matriz_Conformidade.md:92`: «Art. 17 — Direito ao apagamento … Crypto-shredding … | AOS-093 | **Coberto**» sem ressalva, num documento dirigido a «DPO, equipas jurídicas … auditores externos» (`:29`) cuja §5 (`:100-112`) enumera 7 lacunas e **omite esta** | O próprio nó contradiz a matriz: `packages/cmd/aos/bootstrap.go:672` avisa em runtime que «o conteúdo dos runs no Event Store (texto-claro, não cifrado por-titular) fica **FORA do alcance do shredding**». O eixo do deferimento é inválido em três sítios: `bootstrap.go:626` diz EPIC-06/09/10; `tecnica/02:175` diz **EPIC-13** (que é o epic de *Frontend*); e nenhum dos três tem ticket (`grep '^\| AOS-' specs/EPIC-09/10/13` → nada sobre cifra do Event Store) | **SOBRE-REIVINDICADO** | **NOVO** |

**Duplicações assinaladas (não contabilizar duas vezes):** `REG-01 ≡ STR-09 ≡ PLA-03` (mesmo facto: durabilidade inalcançável pelo binário); `VAC-02 ≡ DEF-02 ≡ CON-03` (mesmo facto: `redaction/doc.go` afirma cablagem inexistente). Três lentes independentes chegaram ao mesmo sítio por caminhos diferentes — o que reforça o facto, não a severidade.

### 4.1 Achados MÉDIO sobreviventes (21, condensados)

| ID | Lente | Facto verificado | vs v3 |
|---|---|---|---|
| REG-03 | REG | Nó continua sem carregar bundle PDP (`secured.go:166-168` `pdp.NewUnloaded()`; `grep AOS_POLICY_BUNDLE` → 0). Rebaixado: é dívida **declarada**, fail-closed, com eixo (AOS-181) e CA honestamente `[ ]` | v3-AINDA-ABERTO (GOV-01) |
| VAC-02 ≡ DEF-02 ≡ CON-03 | VAC/DEF/CON | `packages/substrate/redaction/doc.go:11-12` afirma «cablado nos composition-roots (cmd/aos, cmd/aos-demo, integration)»; `go list -deps` → ausente em `cmd/aos` e `integration`. Rebaixado: o parágrafo seguinte (`:13-15`) declara o limite real, e `aos-demo` **está** de facto no grafo (via `approval-card`) | **NOVO** (regressão documental de AOS-188) |
| ORF-01 | ORF | `packages/{kernel,substrate}/contract` — 1763 LOC, 22 ficheiros, **0 importadores, 0 testes, 0 menções documentais**; auto-declaram-se «contrato canónico». Rebaixado de CONTRADITÓRIO: código sem importadores não entra em nenhum binário | **NOVO** |
| DEF-03 | DEF | `NewRatificationGate`/`NewProductionRatificationGate` sem chamador de produção; ADR-012 defere o endurecimento anti-replay para «EPIC-13» (= Frontend, sem ticket); CA `EPIC-14:901` `[x]` «ligados no promotion controller» é falso | **NOVO** |
| DEF-04 | DEF | `Config` do nó não tem seam para `HumanDirectory`/OIDC (`bootstrap.go:490` fixa `NewAllowlistDirectory` sem ramo alternativo); «issuer processo-externo» listado como código do EPIC-16 não é binário entregue. Rebaixado: em produção o nó **não tem directório nenhum** (`main.go:106-110` exige trust-anchor), logo a allowlist demo é caminho de referência, não único caminho | refina-v3 (não contradiz) |
| PLA-02 | PLA | ADR-019 nasceu «Aceite» dentro do EPIC-17 e não foi inscrito na tabela §4.1 da Carta (que continua a listar 7 ADRs e a intitular-se «todas FIXAS», omitindo 003/018/019). Rebaixado: `AGENTS.md:84` cita ADR-019, e ele está em `docs/adr/README.md:72` e na RTM | v3-AINDA-ABERTO (COE-03), agravado |
| PLA-03 | PLA | *(≡ REG-01)* Defeito de **suficiência do CA**: «quando configurado» foi escrito sem exigir superfície de configuração | v3-AINDA-ABERTO |
| DAT-01 | DAT | Envelope de evento de `tecnica/13:60-89` vs `eventstore/event.go:59-72`: 8 campos documentados ausentes, 5 reais não documentados; schema publicado tem `additionalProperties:false`. Rebaixado: `prompt_hash`/`model.seed`/deps existem um nível abaixo, no `Manifest` do `turn.recorded` (`turn.go:44-52,108-116`) | **NOVO** |
| DAT-02 | DAT | 81 constantes de tipo de evento no código, 80 não registadas em `tecnica/13`; 3 dos 4 nomes «canónicos» documentados não são emitidos (`tool.result.received`, `state.transition`, `tool.call.dispatched`). Rebaixado: a citação é «ex.:», ilustrativa e não normativa | **NOVO** |
| DAT-03 | DAT | Campo `taint` do contrato C2 não existe no envelope (`grep` em `eventstore/` → vazio). Rebaixado: o rótulo de taint **é** persistido no payload da mediação (`eventsink.go:96,159`), logo a propagação C2→C1 não fica sem registo | **NOVO** |
| DAT-04 | DAT | C4 (GW↔provider) integralmente diferente: contrato por referência (`messages_ref`/`output_ref`/`cost_usd`) na doc vs forma OpenAI por valor no código; 3 códigos de erro documentados inexistentes. Rebaixado: `tecnica/12 §1.2` declara os schemas «de desenho, não codificação em wire», e não há integrador externo | **NOVO** |
| DAT-07 | DAT | Manifesto de dependências: doc diz congelado `frozen_at_seq:1`; código recalcula por turno e enche `prompt_hash` com o hash do prompt completo, não do system. Rebaixado: `FromRuntimeManifest` **não tem chamadores** — o artefacto é inerte | **NOVO** |
| STR-03 | STR | Nó não monta substrato de sandbox: tools são `ToolFunc` Go despachadas in-process (`monitor.go:21,395`), no mesmo espaço da hash-chain WORM. Rebaixado: os drivers Firecracker/gVisor existem no repo como skeletons **fail-closed** com ticket nomeado (AOS-064/068), e o catálogo do nó está vazio | **NOVO** |
| STR-04 | STR | `controlAuthenticated()` (`api.go:909-914`) é identicamente verdadeiro em qualquer nó produzido por `Bootstrap` ⇒ o bind-guardrail nunca recusa. Rebaixado: é vacuamente verdadeiro (o canal **está** autenticado e em default-deny), não «deixa passar perigo»; a metade restante duplica ORF-02 | **NOVO** |
| STR-05 | STR | `POST /dsar/erase` (destrutivo, irreversível) autorizado só por dois headers auto-declarados — mais fraco que `/pause`. Rebaixado: ADR-013 governa tool calls de agente, não APIs administrativas; o deferimento é **reanalisado no próprio sítio** (`dsar.go:9-15`); e as KEKs são in-memory | **NOVO** |
| STR-09 | STR | *(≡ REG-01)* A parte distinta (fencing) cai: `durable/doc.go:92-101` declara-o opt-in por desenho, com ticket AOS-018 | v3-AINDA-ABERTO |
| CON-01 | CON | `audit.IngestPipeline` (o que cifra PII por titular) nunca é composto fora de testes; o vault do nó é `NewInMemoryKeyVault(nil)` e nada chama `EnsureKey` em produção. Rebaixado: é consequência aritmética da fronteira **já declarada** em `bootstrap.go:620-627`. Resíduo real: o WORM sela `dsar.key_destroyed` para uma erasure vacuosa | **NOVO** |
| CON-02 | CON | Legal hold e job de expiração sem qualquer superfície de administração no nó (`grep NewExpirationJob` → 0 chamadores de produção; nenhuma rota de hold em `api.go:310-324`). Rebaixado: não há apagamento real para suspender. Resíduo: são as **únicas** dívidas sem eixo/dono/data declarados | **NOVO** |
| CON-03 | CON | *(≡ VAC-02)* | **NOVO** |
| CON-04 | CON | Soberania inerte no caminho de **escrita**: `pdp.WithBoardRegions` sem chamador de produção; `Principal.Board` nunca preenchido (`api.go:385`); `Resource.Region` vem de `inv.ResourceRegion`, campo produzido pela fronteira **untrusted** (`model.go:25-27,36`) e **sem** a invariante que protege `AuthorizationTaint` (`:47-59`). Rebaixado por impacto nulo hoje (PDP deny-all + deny fail-closed com região vazia) — **mas escala para ALTO no momento em que AOS-181 carregar um bundle** | v3-AINDA-ABERTO (GOV-02), alargado |
| DEF-02 | DEF | *(≡ VAC-02)* | v3-AINDA-ABERTO (OBS-01) |

---

## 5. Achados refutados ou reclassificados no contra-exame

Transparência sobre o que **não** sobreviveu. Os fiscais tinham mandato de demolir; isto é o que caiu.

| ID | Decisão | Porquê (evidência do fiscal) |
|---|---|---|
| **CON-05** — «Não existem execuções anónimas» contradito: cadeia de delegação sempre vazia | **REFUTADO** | Grep incompleto do auditor. A cadeia **não vem do wire, vem do token**: `packages/platform/identity/rmadapter.go:70-79` (hook de identidade, primeiro da cadeia em `secured.go:214`) resolve o Principal por mutação do `*Call`, incluindo `DelegationChain: toRMChain(...)`; o token é mintado com o humano na raiz (`integration/issuer_authority.go:270-291`, `identity/issuer.go:252,303`); o RM propaga (`monitor.go:304,345`) e o adaptador copia (`audit/rmadapter.go:90-93`). Uma chamada sem credencial é **negada** a montante (`bootstrap.go:496`, asserido em `bootstrap_test.go:404-415`). A tese «toda a mediação selada é anónima» é falsa, e com ela cai o impacto sobre Art. 30/35/FRIA |
| **VAC-01** (CRÍTICO→ALTO) | RECLASS. | A propriedade **está** provada ao nível do componente: `durable/step_ledger_test.go` (`TestApplyIdempotentReexecution:142`, `TestFaultInjectionCrashBeforeCommit:214`, `…AfterCommit:270`, `TestLedgerSurvivesWorkerRestart:314`, `TestApplyCrossWorkerDuplicateReturnsCanonical:481`). Falta a prova ao nível do **nó**, não a propriedade |
| **DEF-01** (CRÍTICO→ALTO) | RECLASS. | `tecnica/14:70` define a legenda («Coberto = mecanismo AOS existe e suporta o requisito») e a §2 declara que a responsabilidade de configurar/activar/provar é do operador, «evitando-se qualquer afirmação de conformidade absoluta». A regra «está no caminho do nó?» é do auditor, não do documento. Pela própria escada, a linha devia ler «Parcial» |
| **CON-01** (CRÍTICO→MÉDIO) | RECLASS. | Duas premissas caem: (a) a legenda de `tecnica/14:70`, como acima; (b) o banner **não** afirma o oposto — `bootstrap.go:620-627` declara que o conteúdo dos runs é texto-claro e que o shredding não o torna ilegível, com eixo nomeado. O «vault vazio» é consequência aritmética dessa fronteira, não segunda omissão |
| **PLA-02** (ALTO→MÉDIO) | RECLASS. | A Carta não enuncia fronteiras de camada em lado nenhum — quem as enuncia é `AGENTS.md §3`, que **cita** ADR-019 (`:84`). O ADR está em `docs/adr/README.md:72` e na RTM. E alterar a Carta exige emenda datada com arbitragem §6.5, não é entregável de ticket de engenharia |
| **STR-05** (ALTO→MÉDIO) | RECLASS. | ADR-013 governa `risk.Class` de **acções de agente** no RM (`risk_gate.go`), não APIs administrativas HTTP ⇒ «contradiz decisão FIXA» não se sustenta. `dsar.go:9-15` **reanalisa** o deferimento no próprio sítio. KEKs são in-memory e não sobrevivem a restart |
| **STR-03** (ALTO→MÉDIO) | RECLASS. | No repo os controlos não são inexistentes: `packages/substrate/sandbox/` tem `driver_firecracker.go`, `driver_gvisor.go`, `pool.go`, `seccomp/`, `mediated.go` — skeletons **fail-closed** (`ErrDriverUnavailable`). «AUSENTE» aplica-se ao nó, não ao sistema; e AOS-068 tem CA verificáveis por marcar |
| **ORF-01** (ALTO→MÉDIO) | RECLASS. | «CONTRADITÓRIO» não sobrevive: com 0 importadores, a afirmação **operativa** do ADR-019 §3 («a alternativa não foi adoptada na v1») continua verdadeira. Falsa é só a leitura literal de «não existe um directório com esse nome». Imprecisão redaccional, não corrosão de autoridade |
| **DEF-04** (ALTO→MÉDIO, «CONTRADIZ-v3»→«refina-v3») | RECLASS. | `main.go:106-110` impõe `ErrProductionNeedsHardenedIdentity`; em produção o nó não compõe directório nenhum (`bootstrap.go:466-470`). A allowlist demo é caminho de **referência**, não «o único caminho». A refutação v3 de COM-02 fica imprecisa na palavra «fallback», mas correcta na substância |
| **DAT-03/04/07, DAT-01/02** (ALTO→MÉDIO) | RECLASS. | Em todos: drift documental **real e verificável**, mas (a) sem consequência de comportamento, (b) sem integrador externo do contrato, (c) com a informação alegadamente ausente presente noutro nível (manifesto do turno, payload da mediação), ou (d) sobre artefactos **sem chamadores** |
| **STR-04** (ALTO→MÉDIO) | RECLASS. | O guardrail é vacuamente verdadeiro, não permissivo: em toda a configuração entregue o canal de controlo **está** autenticado e em default-deny; não existe configuração em que devesse ter disparado e não disparou. `POST /runs` é não-autenticado **por desenho** (ADR-016) com admission e resposta não-enumerável |
| **CON-02/04, DEF-03, REG-03, PLA-03** (ALTO→MÉDIO) | RECLASS. | Padrão comum: facto confirmado, impacto sobreavaliado — subsistema não composto no nó (promotion pipeline), dívida declarada e fail-closed (PDP), ou defeito de plano em vez de sobre-reivindicação (AOS-180 tem CA `[ ]`; nenhum documento declara a capacidade entregue) |

**Erros de citação detectados e corrigidos pelos fiscais** (registados por honestidade): `tecnica/02` §4.5 está em `~:465`, não `:460-462`; o schema JSON é `packages/substrate/eventstore/schemas/…`, não `schemas/…` na raiz; a âncora de DoD de REG-01 é `00_System_Spec.md:289` (critério §13.3), não `Carta:124-125` (que não qualifica durabilidade) — com a âncora correcta o achado fica **mais** forte.

---

## 6. DELTA vs v3 — a secção decisiva

### 6.1 Achados da v3 AGORA FECHADOS (com evidência)

| Achado v3 | Sev. v3 | Estado v4 | Evidência do fecho |
|---|---|---|---|
| **ARQ-01** — `substrate/sandbox` importa `kernel/reference-monitor` | CRÍTICO | **FECHADO por formalização** | O código **não** mudou (`mediated.go:7` continua a importar). Mudou a governação: `docs/adr/ADR-019-fronteiras-camada-excecoes.md` (Aceite, 2026-07-25) enumera cada inversão com racional e alternativas rejeitadas; `scripts/ci/layer-lint.sh` implementa o grafo canónico com baseline; executado pelo auditor REG: «OK nenhuma violação de fronteira fora da baseline». Fecho **legítimo**: dívida oculta → decisão registada com protecção contra reincidência. **Ressalva:** o gate não corre na CI que bloqueia (PLA-01), e a baseline ainda diz «Serão resolvidas pelo ticket AOS-179» para inversões que AOS-179 decidiu legitimar |
| **ARQ-04** — `control-plane/orchestrator` → `platform/identity` | MÉDIO* | **FECHADO por formalização** | ADR-019 §2.4/§2.5 + baseline (22 entradas) |
| **RAS-01** — RTM desactualizada | ALTO | **FECHADO na parte quantitativa** | `tecnica/16:30` cobre agora «189 tickets AOS-001–AOS-189 … 17 epics» e 19 ADRs (vs 118/11); §4 é gerada por `scripts/ci/rtm-regenerate.py`; gate `rtm` em `run.sh:30`; auditor correu `--check` → «RTM está sincronizada» (exit 0). **Ressalva material:** ver §6.2 |
| **UXD-01** — «12 epics / 128 tickets» | ALTO | **FECHADO** | `AGENTS.md:7` «dezassete epics (EPIC-01..EPIC-17)»; `specs/INDICE.md:16` «189 tickets … 17 epics»; `ls specs/EPIC-*.md \| wc -l` = 17 |
| **UXD-02** — `packages/README.md` afirma esqueleto sem lógica | ALTO | **FECHADO, e bem calibrado** | `packages/README.md:1-9` descreve «a implementação de referência entregue pelos tickets AOS-NNN» com ressalva honesta sobre «seams condicionais documentados nos respectivos epics» — não inverte para o exagero oposto |
| **SUP-01/02** — baseline `govulncheck` sem dono | AMARELO | **FECHADO com qualidade forense** | `scripts/ci/baseline/govulncheck.txt:68-77`: cada CVE de `platform/attestation` com `owner=AOS-187`, remediação datada e o símbolo atingido. O cabeçalho (`:9-11`) **recusa** remover entradas como «estruturalmente impossíveis», preferindo justificar — preferiu a verdade ao fecho fácil do checkbox |
| **UXD-03** — gate 2b de referências inexistente | — | **FECHADO** | `scripts/ci/ref-lint.py` é fail-closed em três classes independentes (AOS-NNN inexistente, ADR fora do catálogo, ADR canónico sem ticket). **Ressalva:** não corre na CI (PLA-01) |

### 6.2 Achados da v3 que PERSISTEM

| Achado v3 | Estado v4 | Nota |
|---|---|---|
| **SEC-01** — TaintGate inerte | **ABERTO**, com evidência nova | REG-02 + STR-02. Novo: não há **porta de configuração** no `Config` do nó ⇒ ligar exige alterar código, não configurar. E o deferimento da separação de planos apoia-se explicitamente na barreira que está morta |
| **DUR-01** — nó corre runs sem execução durável | **ABERTO**, com evidência nova | REG-01. Novo: existe código real no composition-root, mas é **inalcançável** — `package main`, sem env var, sem escritor |
| **DUR-02** — fencing ausente | **ABERTO**, reclassificado | Confirmado por grep, mas `durable/doc.go:92-101` declara-o opt-in por desenho com ticket AOS-018 ⇒ deferimento com eixo. Persiste a sobre-reivindicação nos **comentários** de `service.go` |
| **GOV-01** — nó sem bundle PDP | **ABERTO** | REG-03 (MÉDIO). Dívida declarada, fail-closed, CA honestamente `[ ]` |
| **GOV-02** — read-path D7 sem soberania por-run | **ABERTO e ALARGADO** | CON-04: a lente de conformidade mostra que a soberania também é inerte no caminho de **escrita** (`WithBoardRegions` sem chamador; `Principal.Board` nunca preenchido) — a v3 só viu o read-path |
| **ADV-02** — `http.DefaultClient` no adaptador do GW | **ABERTO** | `openai_http.go:59-62` inalterado; AOS-184 com todos os CA `[ ]` |
| **COE-03** — ADRs fora do registo da Carta | **ABERTO e AGRAVADO** | PLA-02: passou de 2 ADRs omissos (003, 018) para 3 — o próprio plano de remediação criou o ADR-019 e não o levou à Carta |
| **OBS-01** — motor de redacção não ligado | **ABERTO, com regressão documental** | VAC-02/DEF-02/CON-03: fechado pela porta de escape do CA, e o texto substituto afirma uma cablagem inexistente |

### 6.3 O que a v3 NÃO VIU (achados NOVO, por lente)

Esta é a justificação da ronda. Onze factos que a v3 estruturalmente não podia encontrar:

**REG (verificação de remediação)** — a v3 auditou o estado, não a **trajectória**. NOVO: `redaction/doc.go:10-12` afirma cablagem nos composition-roots (afirmação criada **depois** da v3); EPIC-17 executado com 8 tickets em trunk enquanto o Estatuto é «PROPOSTA» e o DoR está por satisfazer; contagem de módulos em `AGENTS.md:22` já derrapou (46 declarados vs 47 reais) dois dias depois de UXD-01 ser fechado — porque não há gate que a verifique.

**VAC (vacuidade)** — a v3 verificou existência de testes. NOVO: o teste de aceitação de AOS-180 é vacuoso; o checklist `AOS-169-aceitacao-sistemica.md` cita evidência errada em três eixos (§13.1 caminho PERMIT provado sobre cadeia que **omite** o hook de revalidação; §13.6 `execute_tool` declarado exportado mas o teste corre com modelo que nunca emite tool call; §13.7 crypto-shredding provado sobre KEK que nunca cifrou nada); o guard de NO-BYPASS compara baseline só-de-permits contra delta de permits+denials.

**ORF (código→doc)** — a v3 correu só doc→código. NOVO: 1763 LOC de módulos `*/contract` órfãos; `Operators`/`Approvers` sem caminho de configuração; `AOS_HUMANS` e `AOS_ISSUER_ID` sem uma linha de documentação em todo o repo; `AOS_BOARD_REGIONS` **definido-vazio** é um kill-switch do read-path soberano documentado só num script de harness; limiares de gate sobreponíveis por env (`EVAL_PASS_RATE_MIN`, `KERNEL_COVERAGE_MIN`, `SKIP_DOCKER`) sem piso nem registo.

**DEF (deferimentos)** — a v3 aceitou deferimentos declarados pelo valor facial. NOVO: o padrão sistemático não é «dívida escondida», é **eixo errado** — cifra do substrato apontada a três epics inconsistentes (um deles Frontend) sem ticket em nenhum; anti-replay do ADR-012 apontado a EPIC-13; assinatura de imagem do ADR-017 apontada a EPIC-10, que não tem ticket para ela. E o **tripwire da Carta §6.6** («≥2 decisões FIXAS reabertas em 30 dias ⇒ o congelamento falhou … este contador é o SLI do próprio processo») **não tem instrumento**: não existe contador nem registo de arbitragens em lado nenhum do corpus. A promessa anti-retrabalho não está falsificada — está **infalsificável**, que é a condição que o §6.6 foi escrito para evitar.

**PLA (crítica do plano)** — a v3 não tinha plano para criticar. NOVO: os três gates anti-recorrência fora da CI que bloqueia; ADR-019 não inscrito na Carta; critério de saída 5 fixa «os 16 epics» quando os seus próprios tickets dizem 17; `rtm-regenerate.py` não é auto-descritivo (`ADR_RANGE = range(1,20)`, prosa literal hard-coded, ticket em falta produz **aviso** e não `exit 1`) ⇒ RAS-01 reaparece com o primeiro ticket do EPIC-18; achados ALTO da v3 sem ticket (ARQ-02/ARQ-03 fecharam por efeito colateral da baseline, não por desenho).

**DAT (contratos)** — a v3 não abriu `tecnica/12`/`tecnica/13`. NOVO e com causa identificada: os dois contratos com rastreio a `tecnica/12` no código (C1, C2) estão **fiéis**; os três sem rastreio (C3, C4, C5) divergiram integralmente; e o «gate 4 — Integração», declarado bloqueante de merge em `specs/01:83` e nomeado em `tecnica/12:351` como a mitigação de «deriva silenciosa de schema», **não existe** em `scripts/ci/`. A deriva não é acidental.

**STR (STRIDE)** — a v3 não abriu `tecnica/17`. NOVO: rastreabilidade ameaça→ticket sistematicamente errada; o documento está na v1.0 de emissão e é **órfão** (nenhuma EPIC/ADR/RTM lhe aponta de volta); e não conhece a superfície real do produto — `grep -i "http|api|sse|dsar|otlp|container"` em `tecnica/17` → **zero ocorrências**, apesar de a Carta emenda 1.2 ter **mandado** reavaliar o modelo de ameaça para o nó exposto como serviço de rede.

**CON (conformidade)** — a v3 não abriu `tecnica/14`. NOVO: quatro das seis linhas GDPR marcadas «Coberto» apoiam-se em mecanismos não montados no nó; legal hold e expiração inarmáveis; lacunas de **âmbito** do próprio documento (Art. 15/16/20 — o «DSAR» do produto só faz apagamento; Art. 22 — decisão individual automatizada, o artigo mais directamente convocado pela forma do produto; Art. 33/34; AI Act Art. 50). A §5 do documento é boa a nomear lacunas de **responsabilidade do operador** e nunca nomeia lacunas de **âmbito da matriz**.

### 6.4 Balanço honesto do delta

A v3 cobria o eixo certo. Esta ronda **não o refuta e não o substitui** — confirma que continua aberto e acrescenta, em cinco pontos, informação que muda decisões:

1. **PLA-01** muda a prioridade de remediação: sem os gates na CI, todos os outros fechos da v3 voltam a derivar. É o item de maior alavancagem e o mais barato (3 jobs + 3 entradas em `needs:`).
2. **REG-01** muda o diagnóstico de DUR-01: não é «falta implementar», é «está implementado e é inalcançável». Custo de fecho: ~3 linhas em `main.go`.
3. **ORF-02** revela uma classe nova: capacidade documentada como entregue, com componente correcto, **inoperável no artefacto**.
4. **STR-01** e **DAT-09** revelam que dois artefactos de auditoria (`tecnica/17`, gate 4) não fazem o que dizem — o que corrói a capacidade do programa de se auto-verificar.
5. **VAC-01** revela que um critério sistémico está VERDE sobre uma prova que não prova.

O resto — DAT e CON, sobretudo — é drift documental genuíno mas de consequência baixa, e foi correctamente rebaixado pelo contra-exame. Se a pergunta for «a v4 justificou-se?», a resposta honesta é: **sim, por cinco achados, não por vinte e nove.**

---

## 7. Implicações para o DoD da v1 (Carta §5) e para o EPIC-17

### 7.1 DoD da v1

| Critério DoD (Carta §5) | v3 | v4 | Racional |
|---|---|---|---|
| O nó `aos` corre e hospeda um run fim-a-fim | 🟡 | 🟡 | Sem alteração material. O nó corre; durabilidade, governação e identidade reais continuam fora do caminho executável |
| Interface externa mínima estável (CLI + API stdlib + SSE) | 🟢 | 🟡 **(regressão de avaliação)** | ORF-02: dois dos seis subcomandos da CLI (`steer`, `pause`) e dois dos endpoints de controlo são **inatingíveis** no binário entregue. A interface é estável mas incompleta como produto |
| Cadeia de governança REAL a mediar cada tool call | 🔴 | 🔴 | GOV-01/SEC-01 inalterados. Acresce CON-04: a soberania também é inerte no caminho de escrita |
| Critérios sistémicos do `00_System_Spec.md §13` | 🟡 | 🟡 | §13.3 (Durabilidade) está marcado VERDE em `AOS-169-aceitacao-sistemica.md` com prova vacuosa (VAC-01) ⇒ o critério deve ser **reaberto** até o teste ser corrigido |
| Gates fail-closed verdes | 🟢 | 🟡 **(regressão de avaliação)** | PLA-01: três gates não correm na CI que bloqueia merges; ORF-06: os limiares (`EVAL_PASS_RATE_MIN`, `KERNEL_COVERAGE_MIN`) são sobreponíveis por env sem piso. «Gates verdes» não é hoje uma prova reproduzível |
| D4 desbloqueado (token spine real) | 🟡 | 🟡 | Sem alteração. DEF-04 refina: o componente OIDC é real, o seam de configuração no nó não existe |

**Conclusão:** o DoD da v1 **não pode ser declarado**. Dois critérios que a v3 dava como 🟢 descem a 🟡 por evidência nova desta ronda.

### 7.2 O EPIC-17 cobre o que sobreviveu?

**Cobre a maior parte, mas faltam-lhe seis tickets e uma correcção de estatuto.**

| O que sobreviveu | Ticket EPIC-17? | Nota |
|---|---|---|
| REG-01 / DUR-01 (durabilidade inalcançável) | AOS-180 — **parcial** | O ticket está mergeado mas o AC não exigia superfície de configuração. Precisa de **emenda ao AC** ou ticket novo |
| REG-02 / SEC-01 (TaintGate) | AOS-183 ✅ | Por executar |
| REG-03 / GOV-01 (PDP) | AOS-181 ✅ | Por executar |
| CON-04 / GOV-02 (soberania) | AOS-182 ✅ | Por executar, **bloqueado por D4** — a tabela §5 apresenta-o como P1 disponível, sem marcador de bloqueio |
| ADV-02 (GW) | AOS-184 ✅ | Por executar |
| **PLA-01** — gates fora da CI | ❌ **FALTA** | Maior alavancagem da ronda |
| **STR-01** — rastreabilidade STRIDE errada | ❌ **FALTA** | AOS-186 cobre a RTM, não `tecnica/17` |
| **VAC-01** — teste de aceitação vacuoso | ❌ **FALTA** | E §13.3 está VERDE com base nele |
| **ORF-02** — `Operators`/`Approvers` sem config | ❌ **FALTA** | |
| **DEF-01/DEF-03/DEF-06** — eixos de deferimento errados | ❌ **FALTA** | Fecham-se corrigindo o eixo, não redesenhando |
| **PLA-02** — ADR-019 fora da Carta | ❌ **FALTA** | Exige emenda §7, não ticket de engenharia |
| **DAT-09** — gate 4 (Integração) inexistente | ❌ **FALTA** | Causa-raiz do drift C3/C4/C5 |
| ARQ-02 / ARQ-03 (ALTO na v3) | ❌ órfãos | Fecharam por efeito colateral da baseline, sem dono atribuído |
| SEC-02, GOV-04, COE-05/06/07/08, UXD-04/06 | ❌ sem ticket | COE-05 é irónico: a Carta §5 lista 9 gates do `run.sh`, que hoje corre 20 — e o EPIC-17 acrescentou três sem tocar nessa lista |

**Problema de governação a montante:** o EPIC-17 tem Estatuto **PROPOSTA**, os 8 critérios de saída todos `[ ]` (três deles demonstravelmente satisfeitos), e o DoR item 3 — *«Decisão tomada sobre v1 zero-dep-com-stubs vs real-wiring (impacta AOS-180/181/182/183)»* — nunca foi fechado, apesar de condicionar os quatro tickets P0/P1 que fecham o DoD. Oito tickets já estão em trunk contra este plano. Pela Carta §6.3, uma decisão ABERTA tem sempre dono e escalada; esta vive numa checkbox no fundo de um epic não ratificado.

---

## 8. Recomendações priorizadas e falsificáveis

Cada uma com critério de verificação executável. Ordenadas por (alavancagem ÷ custo).

| # | Recomendação | Critério de verificação (falsificável) |
|---|---|---|
| **1** | **Ligar `layer-lint`, `rtm` e `ref-lint` ao `.github/workflows/ci.yml` e ao `needs:` do agregador `gates`.** Sem isto, todos os outros fechos derivam | `grep -c "layer-lint\|rtm\|ref-lint" .github/workflows/ci.yml` ≥ 3 **e** os três aparecem no `needs:` de `gates`. Prova negativa: um PR com `import "…/control-plane/…"` em `platform/` deve tornar `gates` **vermelho** |
| **2** | **Dar ao `Config.DurableExecution` uma superfície de configuração** (`AOS_DURABLE_EXECUTION`, no padrão de `AOS_EVENTSTORE_PATH`) e documentá-la em `deploy/node/README.md` | `grep -rn "AOS_DURABLE" packages/cmd/aos/main.go` ≠ vazio **e** um contentor lançado com essa variável regista, no banner, que checkpointer/capturer/ledger estão compostos |
| **3** | **Corrigir `TestNode_DurableExecution_NoDoubleExecAfterRestart`** — instanciar um modelo NOVO para a 2.ª vida — e só então manter §13.3 VERDE | Instrumentar o teste: a 2.ª vida deve emitir ≥1 tool call e o ledger devolver «already-applied». Prova negativa: partir `StepLedger.Apply` deve tornar o teste **vermelho** |
| **4** | **Corrigir a coluna «Ticket» de `tecnica/17_Analise_STRIDE.md`** contra `specs/EPIC-07:48-59` e acrescentar coluna de **estado** (entregue / por-fazer / deferido-com-eixo) | Diff mecânico: cada `AOS-NNN` citado em `tecnica/17` deve casar com o título do ticket em `specs/EPIC-*.md`. Candidato natural a extensão do `ref-lint.py` |
| **5** | **Corrigir `packages/substrate/redaction/doc.go:11-12`** — remover «cablado nos composition-roots (cmd/aos, …, integration)» — e reabrir o CA de AOS-188 ou registar a fronteira como deferimento com ticket | `go list -deps ./...` em `packages/cmd/aos` e `packages/integration` deve **contradizer** qualquer afirmação de cablagem que o `doc.go` faça |
| **6** | **Dar caminho de configuração a `Operators`/`Approvers`** (ficheiro ou env de pubkeys) e corrigir `deploy/node/README.md:46-47` **ou** o predicado `controlAuthenticated()` para exigir ≥1 operador registado | Um nó lançado por `deploy/node/Dockerfile` deve aceitar um `aos steer` assinado. Prova negativa: bind a `0.0.0.0` com zero operadores deve **recusar** (`ErrRefuseNonLoopbackBind`) |
| **7** | **Criar um registo único de deferimentos** (id, eixo, dono, gatilho, estado) e corrigir os eixos errados: cifra do substrato (`bootstrap.go:626` vs `tecnica/02:175`), anti-replay ADR-012 (→ EPIC-13, que é Frontend), assinatura de imagem ADR-017 | Todo o marcador `DEFERIDO`/`demo-grade` em `packages/**/*.go` (não-teste) deve ter entrada no registo com eixo que **contenha um ticket** para ele. Verificável por script |
| **8** | **Reclassificar `tecnica/14:92` (Art. 17) e `:91` (Art. 5) de «Coberto» para «Parcial»** e acrescentar a lacuna à §5; nomear na §5 as lacunas de **âmbito** (Art. 15/16/20/22/33-34, AI Act Art. 50) | Toda a linha «Coberto» deve ter o mecanismo alcançável a partir de `packages/cmd/aos` — verificável por `go list -deps` sobre o pacote que a implementa |
| **9** | **Ratificar ou retirar o EPIC-17**: fechar o DoR item 3 (zero-dep-com-stubs vs real-wiring) com dono e escalada, registar a decisão na Carta §4, e marcar os critérios de saída já satisfeitos | O campo `Estatuto` deixa de dizer «PROPOSTA»; a decisão aparece na tabela §4.2 da Carta com dono nomeado |
| **10** | **Inscrever ADR-003, ADR-018 e ADR-019 na tabela §4.1 da Carta** (via emenda §7) e corrigir o cabeçalho «todas FIXAS» | `for f in docs/adr/ADR-*.md; do grep -q "$(basename)" specs/00_AOS_Carta.md; done` sem falhas. Extensão natural do `ref-lint.py` |
| **11** | **Criar o «gate 4 — Integração»** que `specs/01:83` declara bloqueante, ou retirar a declaração de `specs/01` e de `tecnica/12:351` | `scripts/ci/run.sh` inclui um gate que falha se um tipo Go de porta divergir do contrato declarado em `tecnica/12` (mínimo: presença dos códigos `E_*` documentados) |
| **12** | **Instrumentar o tripwire da Carta §6.6** — contador de decisões FIXAS reabertas + registo de arbitragens §6.5 | Existe um ficheiro/registo com (data, decisão tocada, veredicto do árbitro). Sem ele, o «SLI do próprio processo» não pode disparar e a promessa anti-retrabalho é infalsificável |
| **13** | **Adicionar pisos aos limiares de gate** (`EVAL_PASS_RATE_MIN`, `KERNEL_COVERAGE_MIN`, `APEX_COVERAGE_MIN`) e documentá-los | `EVAL_PASS_RATE_MIN=0 make ci` deve **falhar** por violação de piso, não passar verde |

**Sequência crítica de segurança (não é recomendação, é aviso):** AOS-181 (carregar bundle PDP) **não deve** ser executado antes de AOS-183 (TaintGate) e da correcção de CON-04. Hoje o deny-all do PDP e o catálogo vazio mascaram três lacunas (STR-02, STR-03, CON-04); ligar a política sem ligar a barreira de taint e sem fixar a origem de `Resource.Region` torna o nó permissivo com a defesa control/data-plane inerte e com a região do recurso ditada pela saída do modelo.

---

## 9. Pontos fortes confirmados

Justiça, com evidência. O que está genuinamente bem — e é muito.

**Honestidade estrutural, e é o ponto mais forte de todos.** Nenhum dos oito auditores encontrou um único checkbox marcado sobre trabalho inexistente. Os quatro tickets que fechariam os achados mais graves da v3 têm todos os CA `[ ]`. A baseline `govulncheck` **recusa-se** a remover entradas como «estruturalmente impossíveis» (`:9-11`) preferindo justificar cada uma. `packages/README.md` corrigiu-se sem inverter para o exagero oposto. Os desvios reportados são de **ênfase** (títulos de commit, `doc.go`) e de **activação**, não de falsificação de estado.

**Honestidade em runtime, não só em documento.** O nó imprime as próprias fronteiras no banner de arranque, onde o operador não pode deixar de as ver: `bootstrap.go:653-658` («MODO DE REFERENCIA single-process — a autoridade de identidade e CO-LOCALIZADA»), `:667` (o selo D6 grava a região do board do leitor, não a residência por-run), `:672` (aviso DSAR). Encontram-se poucos projectos que auto-declarem dívida no boot. Foi o próprio código que denunciou DEF-01.

**ADR-019 é um ADR sério, não um carimbo.** Quantifica o custo da refactoração alternativa, nomeia cada inversão com o pacote concreto, rejeita três alternativas com fundamento, e declara o limite: «as excepções são um **tecto**, não um cartão branco».

**Gates com prova negativa.** `scripts/ci/selftest.sh` é raro na indústria: injecta falhas reais e prova que os gates ficam **vermelhos** — módulo `gofmt`-sujo, adulteração da assinatura do bundle Cedar com restauro verificado por `sha256` byte-a-byte, vuln sintética contra o mesmo comparador do gate real **com controlo negativo**. `layer-lint` tem self-test com inversão sintética. `apex.sh` usa `require_tests` como defesa explícita contra *vacuous pass*.

**Anti-vacuidade deliberada nos guard-tests.** `dep_isolation_test.go` interroga o grafo de build efectivo (`go list -deps`) e falha se `seen == 0`, se `seen < 50` («o guarda pode estar a olhar para o alvo errado») e se `integration` não estiver no grafo. `boundary_orq_sch_test.go` faz duas camadas (AST + fecho transitivo), cada uma com o seu fatal de vacuidade. `enforcement_guard_test.go:289-323` é um poison-test correcto. `shutdown_durable_test.go` compara streams byte-a-byte pós-restart, exige seq contíguo, prova dedup com o seq **original** e tem checagens auto-declaradas «(teste vacuoso)» que abortam se o cenário não tiver sido montado.

**Via estrita de construção do RM.** `referencemonitor.NewProductionSecure` recusa fail-closed cadeias com stubs neutros ou sem ScopeGate/TaintGate; o nó nunca chama `New` cru. O hook de identidade corre **primeiro**, resolvendo o principal antes de taint/scope/egress. E — refutando CON-05 — a cadeia de delegação com o humano na raiz é resolvida a partir do **token**, não do wire.

**Contratos C1 e C2 fiéis.** `pdp/input.go:88-135` espelha `tecnica/12:82-117` campo a campo, incluindo os que a política de referência não avalia («por fidelidade ao C1»). Os códigos de erro dos dois contratos existem verbatim com a semântica descrita. O Event Store publica um JSON Schema versionado com `additionalProperties:false` e política de evolução em `$comment`.

**Audit fail-closed no permit e WORM único.** `audit/rmadapter.go:18,97-101`: um nó sem audit **não age**. `integration/secured.go:199-226` encaminha mediações do RM **e** bloqueios de egress para a **mesma** hash-chain. `audit/filestore.go` faz framing `len+json+crc32` com `fsync` por Append e replay que pára no último registo íntegro.

**Selo de leitura como pré-condição, não telemetria.** `sovereignty.go:185-193` **nega** a leitura (503) se o WORM não selar — em contraste deliberado e documentado com o fail-open do OTLP. A distinção entre obrigação de conformidade e telemetria best-effort está feita no sítio certo.

**Hardening de ingresso e higiene de divulgação.** Token-buckets separados para dados e controlo (com a justificação de evitar um `ed25519.Verify` a taxa ilimitada), `MaxBytesReader` + `DisallowUnknownFields`, tecto de streams SSE, drop-slow-consumer por write-deadline. 404 idêntico para run inexistente e leitor não-autorizado; 201 idempotente em vez de 409 para o status não virar oráculo de existência. Contrato de pseudónimo na fronteira DSAR (`dsar.go:60-83`) com o raciocínio correcto e a admissão honesta de que não é prova de pseudonimização.

**Superfície mínima e limpa.** Exactamente 9 rotas HTTP, 1 listener, 3 headers de pedido, **zero** `pprof`/`expvar`/`/debug/`. Zero marcadores TODO/FIXME/XXX/HACK em código de produção. Zero binários versionados. O assinador offline da allowlist regional é `//go:build ignore` com custódia de chave documentada em ficheiro.

**AOS-180 entregou substância, mesmo estando desactivado.** `secured.go:249-258` compõe `activity.NewDispatcher` + `NewDurableDispatcher` + `WithActivityDispatcher`; `service.go:406` invoca `RebuildLedger` **antes** de `Runtime.Run`; o `LeaseManager` com heartbeat está cablado. O gap de REG-01 é de **activação**, não de substância — e a distância entre este commit e AOS-188 (que tomou a porta de escape do CA) é instrutiva para a equipa.

**Deferimentos exemplares existem e devem ser o padrão.** D7 tem os quatro elementos: registado na Carta §4.2 com regra FIXA, dono nomeado, gatilho explícito («topologia CONDICIONAL a regiões/boards reais») e ticket de fecho com CA verificáveis (AOS-182). A excepção zero-dep da emenda 1.3 não é convenção documental — é guarda executável (`dep_isolation_test.go`). Um deferimento cuja fronteira o CI verifica não pode derivar em silêncio; é o padrão que os deferimentos de DEF-01/03/06 deviam copiar.

---

## 10. Anexo — limitações declaradas desta auditoria

Por honestidade, o que esta ronda **não** fez:

- **Não executou a suite Go completa nem `scripts/ci/run.sh`.** Nenhuma afirmação sobre o verde/vermelho global dos gates. As conclusões sobre CI são **estruturais** (conteúdo dos scripts e do workflow, verificado por leitura e `grep`), não observação de execuções. Excepções: dois auditores correram `layer-lint.sh` (OK), `rtm-regenerate.py --check` (sincronizada), `go test -run TestNode_DurableExecution_…` (PASS) e `go list -deps` em três módulos.
- **Não observou o job `delivery` do GitHub Actions em execução** — a leitura é do YAML.
- **Não varreu exaustivamente os 47 módulos.** A lente VAC concentrou-se em `packages/cmd/aos`, `packages/integration` e nos testes citados como evidência; podem existir outros testes vacuosos fora desse perímetro.
- **Não auditou `tecnica/09_Governacao_Conformidade.md` nem os specs EPIC-09 na íntegra** — a lente CON ancorou-se em `tecnica/14`.
- **Não avaliou cobertura de linha**, apenas se a asserção prova a propriedade reivindicada.
- **Achados de AUSÊNCIA** (DEF-06 datas, DEF-07 tripwire, ORF-03 env vars) provam-se por varrimento negativo sobre `specs/`, `tecnica/`, `docs/` e `AGENTS.md`. Se existir registo fora deste corpus (ex.: tracker externo), o achado enfraquece — mas então o defeito passa a ser que o documento canónico não o referencia.
- **`.claude/worktrees/`** foi sistematicamente excluído das buscas (cópias completas do repo que inflacionariam qualquer contagem).

---

*Relatório produzido por workflow multiagente v4: 8 perspectivas novas (REG, VAC, ORF, DEF, PLA, DAT, STR, CON) + 4 fiscais adversariais em contra-exame + síntese final. Estado auditado: HEAD `cc9895f`, árvore limpa. Todos os achados foram exigidos a vir acompanhados de evidência verificável (`ficheiro:linha` ou comando reproduzível); os que não sobreviveram ao contra-exame estão listados em §5 com a razão da queda.*
