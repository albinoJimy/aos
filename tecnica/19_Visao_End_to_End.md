# Visão End-to-End: Arquitetura, Camadas e Processos — AOS

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Documento Técnico — Visão End-to-End (arquitetura · camadas lógicas · camadas de negócio · fluxos · processos) |
| Versão | 1.0 |
| Data | Setembro de 2026 |
| Classificação | Documento de Referência — Aberto |
| Documento-fonte | `_FONTE_agentic-os-ideal.md` |
| Documentos relacionados | `tecnica/00_Arquitectura_Solucao.md` (catálogo e ADRs), `tecnica/16_Rastreabilidade_RTM.md`, `specs/00_System_Spec.md` |

---

## 1. Introdução

### 1.1 Propósito

Este documento consolida num único lugar a visão **end-to-end** do AOS em cinco lentes organizadas: **arquitetura de sistema**, **camadas lógicas** (componentes e as suas responsabilidades), **camadas de negócio** (capacidades funcionais e os papéis que as consomem), **fluxos** (sequências que atravessam componentes) e **processos/sub-processos** (catálogo operacional com triggers, actores, passos e falhas). Não substitui os documentos por subsistema — é o mapa de como eles se encaixam.

### 1.2 Âmbito

Abrange tudo o que é comportamento normativo dos documentos 00–18 e da System Spec. Onde um número ou um limiar não está fixado nos documentos-fonte (valores configuráveis, portas injectáveis), diz-se explicitamente em vez de inventar constantes. Dívidas e estados *não compostos* ficam assinalados em §9, nunca apresentados como controlos vigentes.

### 1.3 Audiência

Arquitectos de plataforma, engenheiros de runtime e de controlo, equipas de segurança e conformidade, operações/SRE e qualquer revisor externo que precise de percorrer o sistema de uma ponta à outra.

### 1.4 Definições e termos

Vocabulário canónico reutilizado em todo o documento:

- **Run:** execução durável de um agente sob um `run_id`; unidade de ordenação do log (`stream_id`).
- **Turno:** iteração do loop do agente (montar → chamar → despachar → verificar); tem `step_id` distinto.
- **Activity:** descrição de um efeito externo mediado pelo RM — não é uma função; a única via de execução é `Monitor.Mediate`.
- **Veredictos do PDP:** `permit | deny | escalate`.
- **Idempotency key:** `f(run_id, step_id) = run_id + ":" + step_id`, com domínios de dedup namespaceados (`ledger-`, `ckpt-`, `cap-`, `comp-`, `state-N`, `ctrl-N`).
- **NHI:** identidade não-humana por agente, com token scoped/time-bound e cadeia de delegação *on-behalf-of* que termina num humano (ADR-003).
- **Autoridade efectiva:** `utilizador ∩ classe de agente` — nem o utilizador concede o que a classe proíbe, nem a classe concede o que o utilizador não possui.

---

## 2. ADRs aplicáveis

| ADR | Decisão | Relevância neste documento |
|---|---|---|
| **ADR-001** | Execução durável como primitivo | Base dos processos P-01 e P-07 e do fluxo F2E-06 |
| **ADR-002** | Reference Monitor mandatório | Base da camada de execução e do processo P-02 |
| **ADR-003** | Identidade não-humana por agente | Base da camada de negócio «Gerir identidade e autoridade» |
| **ADR-005** | Separação control/data-plane + taint | Estrutura das camadas lógicas (§3.2) e regra transversal do §6 |
| **ADR-008** | Admission control global em tokens/$ | Processo P-03 e fluxo F2E-04 |
| **ADR-010** | Observabilidade OTel GenAI + audit WORM | Camadas transversais e processo de observabilidade |
| **ADR-012** | SemVer + eval-gate para auto-modificação | Fluxo F2E-05 |
| **ADR-013** | Gates de risco SA-ROC + controlo bidireccional | Processo P-04 e fluxo F2E-03 |
| **ADR-014** | Taxonomia de autonomia L0–L5 | Camada de negócio «Governar por política» e processo P-04 |

---

## 3. Arquitectura de sistema

### 3.1 Vista de alto nível — cinco camadas

O AOS organiza-se em **cinco camadas**, onde as transversais **envolvem** as restantes em vez de se pendurarem no fim:

```
┌─────────────────────────────────────────────────────────────┐
│  TRANSVERSAIS:  GOV (governação & learning) · OBS (observabilidade & evals)│
├─────────────────────────────────────────────────────────────┤
│  PLANO DE CONTROLO (decide):   ORQ · SCH · PDP               │
├─────────────────────────────────────────────────────────────┤
│  PLANO DE EXECUÇÃO (executa):  RT · RM                        │
├─────────────────────────────────────────────────────────────┤
│  SERVIÇOS DE PLATAFORMA:       MEM · REG · GW · BRK           │
├─────────────────────────────────────────────────────────────┤
│  LOG E SUBSTRATO:              ES (event store) · SBX (sandbox)│
└─────────────────────────────────────────────────────────────┘
```

Sentido de fluxo: GOV/OBS → controlo → execução → plataforma → substrato; o RT só sai para o mundo através do RM (ADR-002). Nota de desenho de `tecnica/00`: «o modelo é a *menor* camada do sistema; o fosso está no runtime, na coordenação e na governação».

### 3.2 Plano de controlo vs. plano de dados

| Plano | Elementos | Papel |
|---|---|---|
| **Controlo** (decide) | ORQ, SCH, PDP, ADM (admission), secrets (Vault) | Decomposição, escalonamento, decisão de política, reserva de headroom. Alvo de disponibilidade **99,9%** |
| **Dados** (executa e regista) | workers stateless (1..N), Event Store replicado, Audit WORM | Executam runs sob lease; o estado vive no ES, não nos workers |

Ligações canónicas: ADM → SCH (reserva de headroom); SCH → workers (push event-driven); PDP → workers (avaliação por tool call, **fora** do caminho de dados); workers → ES → Audit WORM. O objectivo arquitectural é eliminar o SPOF do *single-writer* (ADR-007) e permitir reserva de headroom atómica no *admit* (ADR-008).

### 3.3 Topologia de implantação

- **Separação física de planos:** cada plano tem rede própria com **egress default-deny/allowlist** e escala independente; o plano de dados inclui o *pool* de microVM e o NATS JetStream.
- **Modelos:** `deployment_model ∈ {self_hosted, on_prem, cloud}`; staging usa `on_prem`. Estado remoto S3 com `use_lockfile` nativo.
- **Dev:** NATS `nats://localhost:4222` (monitorização `:8222`), Vault `:8200`; bootstrap MinIO.
- **Imagem de produção** (`deploy/node/Dockerfile`): distroless `static-debian12:nonroot`, UID `65532:65532`, root-fs read-only, sem shell; arranque fail-closed exigindo `AOS_MODE=production` + `AOS_ISSUER_PUBKEY` + `AOS_BOARD_REGIONS` não-vazio.
- **Soberania regional:** failover nunca cruza fronteira; a IaC **falha** se `backup/replica_region` violarem o board (ADR-011).

### 3.4 Mapeamento para código

| Camada | Pacotes (`packages/`) |
|---|---|
| Plano de execução | `kernel/reference-monitor`, `kernel/agent-runtime` |
| Plano de controlo | `control-plane/orchestrator`, `control-plane/scheduler`, `control-plane/pdp`, `control-plane/governance/*` |
| Plataforma | `platform/memory`, `platform/registry`, `platform/model-gateway`, `platform/broker` |
| Substrato | `substrate/eventstore`, `substrate/sandbox`, `substrate/otel-genai`, `substrate/bus`, `substrate/redaction` |
| Transversais | sem pacote próprio — bibliotecas partilhadas e políticas versionadas atravessando todos os pacotes |
| Binários | `cmd/aos` (nó), `cmd/aos-orq` (ciclo de vida sob lease, AOS-281), `cmd/aos-attestation`, `cmd/aos-issuer`, `cmd/aos-demo` |
| Composição | `integration/` (composition-root / ápice de enforcement composto) |

Regras do grafo: sentido de dependências `control-plane → kernel → platform/substrate`; sem ciclos; excepções escopadas em ADR-019 sob gate `layer-lint`.

---

## 4. Camadas lógicas e componentes

Catálogo denso — responsabilidade essencial, portas relevantes e invariante próprio. É a lente «componente»: quem é e o que garante.

| Código | Componente | Responsabilidade essencial | Portas / invariantes |
|---|---|---|---|
| **RM** | Reference Monitor (PEP) | Gate mandatório de mediação total de tool calls (ADR-002) | Resolve identidade e cadeia NHI; consulta PDP; valida orçamento; aplica egress; escreve audit. As três propriedades clássicas: sempre invocado, inviolável, verificável. Overhead alvo p95 < 15 ms |
| **RT** | Agent Runtime | Loop do agente (montar → chamar → despachar → verificar) sobre base durável (ADR-001) | Detém `*referencemonitor.Monitor`, nunca `ToolFunc`; prompt remontado por turno (prefixo byte-idêntico + tail append-only, ADR-009); resultados devolvidos sempre untrusted (ADR-005) |
| **ORQ** | Orquestrador | Decompõe objectivos em DAG acíclico; delega a sub-agentes; map-reduce recursivo com orçamento hierárquico | Reserva atómica CAS **antes** do spawn; aciclicidade verificada na inserção de cada aresta (fail-closed); delegação mediada pelo RM |
| **SCH** | Escalonador | Durable execution, leases/fencing, prioridade e aging, backpressure, detecção de deadlock | Não despacha sem débito reservado no token-bucket global (ADR-008); push para workers stateless; prioridade efectiva inteira com aging sem tecto (zero starvation) |
| **PDP** | Policy Decision Point | Avalia policy-as-code (Rego/OPA ou Cedar) por tool call | Política compilada em memória (p95 < 15 ms); versionada em git, assinada; só carrega bundles com hash+assinatura verificados, senão falha fechado com a versão anterior |
| **MEM** | Memory Service | Episódica, semântica, procedural e de trabalho; contexto ≠ registo | Proveniência e quarentena para conteúdo untrusted; migrações expand/contract; classes de TTL |
| **REG** | Skill/Tool Registry | Catálogo SemVer de skills/tools/servidores MCP | Pin + hash + assinatura; definição congelada por hash e revalidada por chamada; mudança de schema exige re-aprovação (anti rug-pull, ADR-012) |
| **GW** | Model Gateway | Interface unificada a LLMs (estilo LiteLLM) | Pipeline fixa: auth-principal → allowlist regional → roteamento → cache-layout → metering; identidade desacoplada das chaves pooled; custo em micro-USD int64 com tabela de preços versionada |
| **BRK** | Credential Broker + Vault | Troca token scoped por credenciais downstream JIT, server-side, TTL curto, revogáveis | O agente nunca vê o segredo (ADR-006); injecção dentro da sandbox |
| **ES** | Event Store | Log append-only replicado, fonte de verdade, transporte push (NATS JetStream) | Ordenação total por `(stream_id, seq)` gapless; dedup por `idempotency_key`; substitui o single-writer (ADR-007) |
| **SBX** | Sandbox Substrate | Isolamento ao nível do kernel por execução | microVM Firecracker/Kata ou gVisor; FS read-only + overlay efémero; seccomp mínimo; rede default-deny + egress allowlist + DNS filtrado (ADR-004); cold-start < 125 ms |
| **OBS** | Observabilidade & Evals | Trajectória como árvore de spans OTel GenAI; replay; audit WORM; circuit breaker multi-sinal | `execute_tool` aberto **só** pelo RM (100% de cobertura); audit hash-chain fisicamente separado; fidelidade de replay alvo 100% (ADR-010) |
| **GOV** | Governação & Learning | Identidade NHI, cadeia de delegação, taxonomia L0–L5, conformidade, eval-gate de auto-modificação | Nível de autonomia é propriedade do par (agente, domínio); promoção monótona e opt-in; subir a L4/L5 exige dual-control (ADR-011, ADR-014) |

---

## 5. Camadas de negócio

A lente «negócio» pergunta: **que capacidades o sistema entrega e a quem**. A System Spec (`specs/00_System_Spec.md` §4) fixa onze capacidades funcionais top-level — essa é a classificação de negócio canónica. Não há personas de cliente (o AOS é blueprint sem cliente institucional); os «consumidores» de cada capacidade são papéis operacionais.

| # | Capacidade de negócio | O que entrega | Componentes | Consumidor (papel) |
|---|---|---|---|---|
| 1 | **Executar agentes com durabilidade** | Runs que sobrevivem a falhas de nó, retomáveis por passo, sem efeitos duplicados | RT, SCH, ES | Agente (NHI); operador SRE |
| 2 | **Mediar toda a acção** | Nenhum efeito externo fora do gate; cada tool call com decisão auditada | RM, PDP, BRK | Agente; auditor |
| 3 | **Orquestrar trabalho** | Decomposição de objectivos em DAG, delegação com orçamento hierárquico, map-reduce | ORQ, SCH | Agente; humano responsável |
| 4 | **Gerir identidade e autoridade** | NHI por agente, cadeia on-behalf-of até humano, autoridade = utilizador ∩ classe | GOV, RM | Humano responsável; auditor |
| 5 | **Isolar execução** | Confinamento por execução, rede default-deny, segredos invisíveis | SBX, BRK | Operador; responsável de segurança |
| 6 | **Gerir memória** | 4 tipos de memória com proveniência, TTL e quarentena | MEM | Agente; operador (curadoria) |
| 7 | **Governar por política** | Política-as-code assinada, autonomia L0–L5 por (agente, domínio), default-deny | GOV, PDP | Responsável de segurança; DPO |
| 8 | **Observar e auditar** | Trajectórias GenAI completas, audit WORM tamper-evident, crypto-shredding GDPR | OBS, ES | Auditor; DPO; engenheiro de qualidade |
| 9 | **Controlar orçamento** | Reserva atómica de tokens/USD por árvore, admission global, backpressure graciosa | SCH (ADM), GW | Responsável de produto; operador |
| 10 | **Controlo bidireccional** | Superfície HITL out-of-band: pausa/steer/resume, gates de plano e de acção | GOV, RT, RM | Humano (aprovador); RM |
| 11 | **Evoluir com rede** | Auto-modificação só via eval-gate + canary + ratificação humana assinada | GOV, REG, OBS | Ratificador humano; responsável de segurança |

**Papéis operacionais** (o «quem» transversal — por cadeia de delegação, não por matriz RBAC formal, que não existe documentada; ver §9):

- **Humano responsável** — raiz obrigatória de toda a cadeia NHI; termina a delegação.
- **Aprovador autorizado** — decide gates (plano/acção); assinatura verificada; irreversíveis exigem **segundo aprovador** distinto (dual-control 4-eyes).
- **Ratificador** — na allowlist de ratificação (ed25519) para auto-modificações e skills auto-escritas; L4/L5 exigem duas assinaturas.
- **Operador** — ratifica TOFU de ferramentas externas, cura políticas/tabelas, executa runbooks.
- **DPO** — audiência do audit; DSAR por crypto-shredding (destruir chave do titular mantém a cadeia íntegra).

---

## 6. Fluxos end-to-end

Lente «tempo»: sequências que atravessam componentes. Seis fluxos cobrem o sistema de ponta a ponta. Todos terminam em eventos append-only — o log é a memória de cada fluxo.

### F2E-01 — Ciclo do turno e tool call mediada (o caminho quente)

Participantes: RT → RM → PDP → (humano) → ADM → BRK → SBX → ES.

1. **Montar** — o RT remonta o prompt (prefixo imutável byte-idêntico + tail append-only); grava `turn.recorded` com o manifesto: `prompt_hash`, `system_hash`, `assembly_version`, `model{model_id, params, seed}`, tools/skills pinadas.
2. **Chamar** — o modelo via GW (pipeline: auth-principal → allowlist regional → roteamento → cache-layout → metering).
3. **Pedir tool call** — o RT propõe a activity ao RM com contexto: principal + cadeia, action/tool, resource, taint, orçamento, região, sensibilidade.
4. **Resolver identidade** — o RM verifica a cadeia de delegação (ADR-003); sem cadeia válida até humano, **deny**.
5. **Decidir** — RM → PDP (política compilada em memória). Veredicto `permit | deny | escalate`. `deny` bloqueia e regista a negação.
6. **Escalar se necessário** — se `escalate` (acção danger/irreversível): gate humano com **preview do efeito concreto**; aprovação assinada ou recusa; timeout = **negativa fail-closed** (transição `waiting_on_human → killed`).
7. **Reservar débito** — RM → admission control; reserva no token-bucket global sobre TPM/RPM real; sem headroom, adia com `retry_after` (nunca descarta).
8. **Credencial JIT** — RM → BRK; o broker injecta a credencial **server-side** dentro da SBX; o agente nunca a vê.
9. **Executar** — SBX executa com idempotency key `f(run_id, step_id)`; ledger aplica dedup **antes** do efeito; compensação registada após Apply, em ambos os caminhos.
10. **Registar** — resultado como evento append-only no ES; decisão selada no Audit WORM.
11. **Devolver untrusted** — o resultado regressa ao RT marcado untrusted (fecha o ciclo ADR-005); o turno termina com `replay.captured` (resposta do modelo + outputs + relógio observado).
12. **Verificar** — o RT decide: continuar, pausar ou terminar; burn-down de custo exposto ao longo do run.

### F2E-02 — De objectivo a execução: goal → plano → DAG → sub-agentes

Participantes: principal → ORQ (intake) → PLN (planeador governado) → gate humano → ORQ (materialização) → SCH → workers.

1. **Intake** — classificação meta-run vs. tarefa simples por função pura **sem LLM**, sobre campos declarativos do `Goal`; ambiguidade resolve para meta (mais escrutínio). Evento `plan.intake_classified`.
2. **Reserva de planeamento** — admitida antes da decomposição (contexto × tabela de preços × factor de retry); fail-closed.
3. **Proposta** — o PLN (NHI `agent:planner`, orçamento debitado da árvore, mediação RM) emite um **PlanDocument PROPOSTO** — untrusted, nunca executado como instrução.
4. **Validação estrutural fail-closed** (função pura, sem LLM, sobre snapshot pinado do REG): schema fechado; aciclicidade; resolução de tools contra o REG (inexistente/deprecada/fora de allowlist **rejeita** — nunca *trimming*); tectos `max_depth`/`max_fanout`/`max_nodes`; re-preço determinístico por ramo; risco **derivado** das tools pinadas (`danger` por irreversibilidade ou egress externo) — o `risk_class` do LLM é advisory e só pode elevar. Inválido ⇒ retry ao LLM com diagnóstico (máx. 3), depois falha de intake.
5. **Gate de aprovação-de-plano** (AOS-121) — humano vê o grafo, estimativa de custo por ramo e capacidades requeridas; aprova, edita (re-valida sem round-trip ao LLM) ou rejeita (zero tokens queimados). Até L3 é a última fronteira onde um humano vê o organigrama antes do spawn.
6. **Materialização** — `plan.materialized`; nós-folha viram `task.node.created`; papéis-que-expandem viram `Delegator.Spawn` com tools[] a vincular a `Authority[]` da NHI filha (escopo sempre **subconjunto** do pai — a autoridade só estreita ao descer).
7. **Admissão e despacho** — SCH re-verifica admissão no spawn (TOCTOU): sob pressão **adia**, nunca oversubscreve; despacha nós cujas `depends_on` estão satisfeitas, por push, sob lease + fencing.
8. **Execução** — cada nó é um run (F2E-01); sub-agente devolve ao pai resumo de 1–2 k tokens; a trajectória completa fica no backend de observabilidade (contexto ≠ registo).
9. **Replan** — sub-plano do subgrafo com orçamento residual, atravessa o **mesmo** gate conforme o nível L0–L5; a autonomia de replan nunca excede a do plano original; nós concluídos são intocáveis.

### F2E-03 — Intervenção humana (controlo bidireccional)

Três sub-fluxos, todos sobre a superfície HITL out-of-band (ADR-013, `tecnica/15`).

**F2E-03a — Steer (pausa / correcção / retoma):**
1. Humano emite sinal de pausa pelo canal out-of-band (separado do canal de dados — a pausa não depende do agente "querer" parar).
2. RT confirma e faz **graceful pause** na fronteira de fim de turno (nunca a meio de activity não-idempotente); estado `paused` + causa persistidos no ES.
3. Humano injecta correcção (instrução / edição do plano / facto).
4. Retoma **audit-first**: `control.resume` gravado **antes** de transitar `paused → running`; o `steer` é autenticado, com identidade do emissor (não-repúdio); conteúdo untrusted não carrega credencial de emissor e é rejeitado (ADR-005).

**F2E-03b — Gate de acção (approval-card):** trigger = veredicto `escalate`. Anatomia mínima do card: **efeito resolvido** (sem placeholders), destinatário real, payload real (PII redigida segundo obrigações do PDP), classe SA-ROC + reversibilidade, proveniência (que dados untrusted influenciaram a acção), decisão aprovar/rejeitar/editar. Irreversíveis exigem segundo aprovador (dual-control 4-eyes). Timeout = negação. Override-rate medido — cronicamente alto é anomalia (revisão/demoção de autonomia).

**F2E-03c — Gate de aprovação-de-plano:** ver F2E-02, passo 5.

### F2E-04 — Admissão global, escalonamento e backpressure

1. Pedido de spawn com custo estimado (tokens/$).
2. Orçamento hierárquico do pai tem saldo? — CAS em cascata filho→pai→raiz; não ⇒ rejeita ou enfileira.
3. Token-bucket global: `tokens − headroom ≥ custo`? — não ⇒ **adia** com `retry_after` (nunca descarta).
4. Reserva atómica (Append CAS ao stream `admission/bucket/provider:model:region` no ES; concorrência optimista, sem SPOF): debita **simultaneamente** bucket global e reserva do pai.
5. `max_spawn` derivado do headroom restante: `min(headroom_tokens / custo_por_subagente, headroom_requests)`, monótono, 0 sob headroom nulo.
6. SCH despacha para worker (lease + fencing); claim exige fencing token válido.
7. Reconciliação: `Finish` consolida estimado vs. real (Commit/Release, idempotente); o bucket reabastece à taxa do **TPM/RPM real observado** no GW (porta `QuotaProvider` — limites nunca hard-coded).

**Escada de degradação canónica** (ordem de preferência, reversível degrau a degrau com histerese por watermarks): **shed** (trabalho opcional/baixa prioridade; crítico nunca) → **defer** (preserva o trabalho) → **degradar** (downgrade para modelo mais barato — evento de variância explícito `model_downgraded`, nunca silencioso) → **rejeitar** (último recurso, erro accionável, fail-closed para irreversíveis). Exaustão graciosa do orçamento raiz a ~80%: oferece **Estender / Resumir-e-parar / Abortar** em vez de hard-stop.

### F2E-05 — Auto-modificação com rede (o fluxo mais forte do sistema)

Trigger: artefacto comportamental mutável (skill auto-escrita, memória procedural, módulo de prompt, schema) ou um `capability_gap` no planeador.

1. **Write** — o agente-autor escreve o artefacto.
2. **Staging** — materializado isolado, sem efeito em produção, versão candidata.
3. **Eval-gate** — golden-set **curado e estável** (não apenas falhas passadas) + **trace-diffing** contra a baseline (trajectórias completas, não só saídas — detecta deriva intermédia). Golden-set de decomposição: asserções de **segurança exigem 100%** de K amostragens; de qualidade, limiar ≥ M/K.
4. **Canary** — fatia limitada de tráfego real sob observação de success-rate e unsafe-action rate.
5. **Ratificação humana assinada** — ed25519 verificada contra allowlist de ratificadores; ligada ao triplo `(id, version, digest)`; L4/L5 exigem **duas** assinaturas distintas. Sem assinatura, a promoção fica bloqueada fail-closed. A ratificação é o momento em que o humano **assume a mudança na cadeia de responsabilização**.
6. **Produção** — SemVer atribuída; novos artefactos só entram em **runs novos**.
7. **Rollback atómico automático** para a versão conhecida-boa em qualquer regressão — sem estado híbrido, sem depender de intervenção manual.

Declaração normativa: **nenhuma auto-modificação chega a produção sem eval-gate, canary e ratificação humana assinada**; o alvo é **0** auto-modificações não avaliadas em prod.

### F2E-06 — Desastre e retoma por replay

1. Operador SRE inicia DR (runbook RB-0x).
2. Restauro do log replicado até ao último evento íntegro num ES de DR limpo (exportador contínuo; segmentos imutáveis com manifesto hash-chain SHA-256 e checkpoint assinado ed25519).
3. Verificação da **hash-chain do audit** — adulteração ⇒ **aborta fail-closed** antes de escrever (`ErrSegmentTampered`); rollback de checkpoint rejeitado (`ErrCheckpointStale`); destino cross-border recusado (`ErrSovereigntyViolation`).
4. `ReplayEngine.Replay` **prova fidelidade** (`Fidelity == 1.0`, sem divergência de `prompt_hash`) — lê inputs capturados, nunca ao vivo; o motor só tem `EventReader` + `Tracer` (zero efeitos estrutural).
5. Retoma **resume-from-step** com o StepLedger — efeitos externos **não são duplicados** (idempotência por `f(run_id, step_id)`).
6. Game day persiste evidência combinada (restauro + WORM verificado + fidelidade + timings); qualquer falha ⇒ o serviço **não** é dado por restabelecido.

Valores de referência *(proposta, a validar por game days)*: RPO ≤ 1 min dentro de região (replicação por quórum), RTO ≤ 30 min (restauro + replay). Honestidade operacional: em produção o backup imutável efectivo é o `backup.sh` diário (RPO 24 h) até haver backend durável para a porta `ImmutableStore` — ver §9.

---

## 7. Processos e sub-processos

Lente «operação»: catálogo de processos com **trigger**, **actores**, **sequência**, **outputs** e **falhas**. Cada processo decompõe-se nos sub-processos que o compõem. A numeração (P-01…) é estável para referência em runbooks e auditorias.

### P-01 — Ciclo de vida do run (durable execution)

| | |
|---|---|
| **Trigger** | `Runtime.Run(ctx, Goal)` com `RunID`, `Principal`, escopo, `ModelConfig`, tool set congelado |
| **Actores** | RT, SCH, ES, StepLedger, Machine de estados, humano (gates) |
| **Output** | `Result` (resposta final ou paragem por `MaxTurns`), custo agregado, `ToolResults` untrusted |

**Máquina de estados canónica (10 estados, 13 transições):**

| # | De → Para | Gatilho | Nota |
|---|---|---|---|
| 1 | `ready → running` | claim pelo worker | **exige fencing token válido** (único par com esta pré-condição) |
| 2 | `running → waiting_on_tool` | activity externa | — |
| 3 | `waiting_on_tool → running` | resultado | retoma sob o lease detido |
| 4 | `running → waiting_on_human` | gate de risco | — |
| 5 | `waiting_on_human → running` | aprovação assinada | retoma sob o lease detido |
| 6 | `waiting_on_human → killed` | **timeout fail-closed** | TTL do gate excedido (ADR-013) |
| 7 | `running → paused` | steer/interrupt | pausa graciosa na fronteira de fim de turno |
| 8 | `paused → running` | resume com correcção | audit-first: `control.resume` antes da transição |
| 9 | `running → complete` | sucesso | terminal absorvente |
| 10 | `running → failed` | erro recuperável | não absorvente |
| 11 | `running → timed_out` | wall-clock excedido | terminal absorvente |
| 12 | `failed → compensating` | saga rollback | compensação LIFO |
| 13 | `compensating → ready` | retry idempotente | após compensação |

Os outros 87 pares da matriz 10×10 são inválidos (`ErrInvalidTransition`, verificação exaustiva por teste). Cada transição é evento `run.state.transition` append-only; o estado reconstrói-se por `Machine.Rebuild` (adopta o `to` de maior `seq`); in-memory só avança **após** commit durável.

**Sub-processos:**

- **S-01a Claim e lease:** `Claim(run)` minta `Lease{Token, TTL, ExpiresAt}`; heartbeat renova (`ErrLeaseExpired`/`ErrStaleFencingToken`); fencing token monotónico em stream `lease:<run_id>` com `WithExpectedSeq` — **no máximo um escritor efectivo** por run.
- **S-01b Checkpoint intra-turno:** `step.checkpoint` por fase confirmada (`assembled`/`model_called`/`turn_recorded`/`dispatched`/`verified`); o cursor **referencia, não copia**; `Resumer.Resume` devolve o `ResumePoint` exacto (`verified` do turno T ⇒ retoma em T+1; `dispatched` com pendentes ⇒ 1.ª activity pendente; sem checkpoints ⇒ turno 1).
- **S-01c Idempotência por passo:** `StepLedger.Apply` verifica *already-applied* **antes** do efeito; key injectiva `run_id:step_id`; `step_id` atribuído por posição no log (StepSequencer), nunca relógio/UUID. Garantia honesta: at-least-once + idempotência downstream = **0 efeitos observáveis duplicados**.
- **S-01d Saga:** compensação em **ordem inversa (LIFO)** com key própria `f(run_id, comp-<step_id>)`; crash-resume reitera a sequência; exaustão **não finge sucesso** — run preso em `compensating` com alerta (`ErrCompensationExhausted`).
- **S-01e Classificação de zombies:** estados de espera são **sempre** espera legítima, mesmo com lease expirado; só `running` com lease expirado é zombie; `waiting_on_human` com gate excedido ⇒ `GateExpired → killed` (matar por política, não reatribuir).

### P-02 — Decisão de mediação (o gate quente)

| | |
|---|---|
| **Trigger** | Activity proposta pelo RT (`Monitor.Mediate`) |
| **Actores** | RM (PEP), PDP, humano (gate), ADM, BRK, SBX, ES, Audit WORM |
| **Output** | Resultado untrusted ou negação/escalado; decisão selada no WORM |

Sequência: ver F2E-01, passos 4–11. Falhas em qualquer passo bloqueiam **fail-closed**. `deny` regista a negação; `escalate` sem resposta no TTL mata o run. Domínios de dedup por passo: turno, ledger, checkpoint, captura, compensação, transição de estado, controlo.

**Sub-processos:** S-02a resolução de identidade NHI; S-02a avaliação PDP (permit/deny/escalate + obrigações como redacção de PII ou restrição de região); S-02c gate SA-ROC (ver P-04); S-02d reserva de débito (ver P-03); S-02e injecção JIT de credencial; S-02f revalidação criptográfica da tool (LOOKUP→DIGEST→ASSINATURA→SCOPE/EGRESS→EXEC→AUDIT, p95 < 15 ms com cache invalidável).

### P-03 — Admissão global e backpressure

| | |
|---|---|
| **Trigger** | Pedido de spawn/despacho |
| **Actores** | ORQ, SCH (ADM), token-bucket global, ES, GW (QuotaProvider) |
| **Output** | `{granted, retry_after}`; reserva atómica; `max_spawn` derivado |

Sequência: ver F2E-04. Invariantes: soma das reservas activas **nunca excede** o TPM/RPM efectivo; tenant nunca excede o tecto global mesmo com folga na sua partição; se o global concede mas o sub-orçamento nega, a reserva é **libertada** (sem fuga de duas-fases). **Sub-processos:** S-03a reserva CAS de duas fases; S-03a reconciliação estimado-vs-real; S-03c escalonamento por prioridade efectiva inteira `Base + AgingStep·(idade/AgingInterval) + SLOWeight·(idade/SLO)` com aging sem tecto (zero starvation) e tie-break determinístico; S-03d detecção de deadlock sobre o *wait-for graph* (ciclo ⇒ aborta a vítima de menor prioridade com compensação).

### P-04 — Governação de autonomia e intervenção humana

| | |
|---|---|
| **Trigger** | Provisionamento de nível; anomalia; gate SA-ROC |
| **Actores** | GOV (LevelRegistry), PDP (`autonomy.Oversight`), humano (aprovadores), RT/RM |
| **Output** | Nível vigente por par (agente, domínio); decisões de escalonamento auditadas |

A escala L0–L5 (ADR-014) com semântica normativa:

| Nível | Nome | Semântica |
|---|---|---|
| L0 | Sugestão | O agente propõe; o humano executa tudo. **Piso fail-closed**: par sem registo resolve para L0 |
| L1 | Aprovação por acção | Cada tool call espera aprovação individual |
| L2 | Aprovação por lote | Acções *gray* equivalentes agrupadas com resumo; *danger* nunca agrupadas |
| L3 | Autonomia supervisionada | *Safe* corre, *gray* segue política de lote, *danger* confirma — é exactamente o tiering SA-ROC |
| L4 | Autonomia por excepção | Só escala em incerteza/risco alto; *danger* deixa de exigir confirmação sistemática ⇒ **cerimónia de dual-control** para subir |
| L5 | Autonomia plena por domínio | Oversight amostral e post-hoc |

Regras: nível é propriedade do **par (agente, domínio)**; promoção **monótona, um nível de cada vez, opt-in explícito do humano** (o sistema propõe, nunca impõe) com métrica sustentada (referência: erro < 2% ao longo de 30 dias, override-rate baixo); o overlay `nível × classe` compõe-se no PDP e **só aperta** (permit→escalate, nunca deny→permit); subir a L4/L5 exige `autonomy:set` com **duas assinaturas** de emissores distintos; piso `AOS_AUTONOMY_DEFAULT >= L4` é recusado no arranque.

**Sub-processos:** S-04a gate SA-ROC (safe/gray/danger por efeito irreversível ou egress externo; card com efeito resolvido; anti-fadiga: safe sem card, gray em lote expansível, danger individual em destaque com atrito assimétrico); S-04b steer (F2E-03a); S-04c promoção/demoção de nível.

### P-05 — Supply-chain do REG

| | |
|---|---|
| **Trigger** | Publicação de skill/tool/servidor MCP (humano ou agente-autor); descoberta MCP |
| **Actores** | Publicador, Pipeline.Promote, operador (TOFU), ratificadores, RM/RT (consumidores) |
| **Output** | Artefacto `active` com SemVer; catálogo consumível |

Estados do ciclo de vida: `staging → active → deprecated | revoked`. **Promoção (fail-closed):** re-verificação de digest → hash SHA-256 sobre conteúdo canonicalizado → validação de contrato → `ValidateBump` SemVer (quebra publicada como MINOR/PATCH é rejeitada; MAJOR exige re-aprovação) → assinatura ed25519 sobre `(id, version, digest)` → se **auto-escrita**: eval-gate + ratificação humana assinada → `active` selado no WORM.

**Sub-processos:** S-05a consumo pinado (versão exacta + digest; `latest` rejeitado); S-05b TOFU (`first_seen → pinned → changed`; divergência de digest = incidente; re-aprovação exige SemVer estritamente superior, nunca in-band); S-05c congelamento por run (`ActiveEntries` + `FreezeToolSet`; revalidação por chamada; drift ⇒ bloqueio + quarentena); S-05d revogação/rollback atómico (rollback re-atravessa o gate; reactivação também).

### P-06 — Publicação de modelo (Model Gateway)

| | |
|---|---|
| **Trigger** | Chamada ao modelo pelo RT |
| **Actores** | GW, keypool, política de scoring, GW metering |
| **Output** | Resposta do modelo medida; span + selo WORM (principal, modelo, região, KeyID não-secreto) |

Pipeline fixa: authn (token EdDSA, cadeia on-behalf-of, **raiz humana obrigatória**, política default-deny de token) → allowlist regional (policy-as-code assinada) → roteamento (guardas estruturais primeiro: soberania descarta cross-border **antes** do ranking; depois lexicográfico carga>custo>latência ou scoring ponderado determinístico — seis factores em milésimos inteiros, pesos como policy-as-code com bump SemVer; sem tabela válida, **rejeita toda a rota**) → cache-layout (prefixo imutável byte-idêntico; hit-rate alvo > 80% como SLI) → metering (custo em micro-USD int64, quatro rates, `(modelo, região)` sem preço ⇒ `ErrNoPrice` fail-closed).

### P-07 — Recuperação de desastre

| | |
|---|---|
| **Trigger** | Desastre; node loss (runbook RB-02); game day |
| **Actores** | Operador SRE, Recoverer, ES de DR, ReplayEngine, StepLedger |
| **Output** | Serviço restabelecido com evidência combinada; ou abort fail-closed |

Sequência: ver F2E-06. **Sub-processos:** S-07a failover de node loss (diagnóstico **nunca por PID** — lease/heartbeat + fencing; reatribuição com novo fencing token; escritas do obsoleto rejeitadas); S-07b verificação de integridade (hash-chain, checkpoint, soberania); S-07c prova de fidelidade por replay; S-07d retoma resume-from-step.

---

## 8. Rastreabilidade

| Lente deste documento | Fonte canónica |
|---|---|
| Arquitectura (§3, §4) | `tecnica/00_Arquitectura_Solucao.md`; `packages/README.md`; AGENTS.md §3/§8 |
| Camadas de negócio (§5) | `specs/00_System_Spec.md` §4 (11 capacidades) e §7 (NFRs) |
| F2E-01, P-02 | `tecnica/01`, `tecnica/02`, `tecnica/12`, `tecnica/13` |
| F2E-02 | `tecnica/03`, `tecnica/18` |
| F2E-03, P-04 | `tecnica/15`, `tecnica/09`, ADR-013, ADR-014 |
| F2E-04, P-03 | `tecnica/03` |
| F2E-05, P-05 | `tecnica/11`, `tecnica/05`, ADR-012 |
| F2E-06, P-07 | `tecnica/10` |
| P-01 | `tecnica/02` |
| P-06 | `tecnica/06` |

Números normativos citados e os seus documentos: mediação p95 < 15 ms; resumo de delegação 1–2 k tokens; exaustão de orçamento a ~80%; cache-hit-rate > 80%; fidelidade de replay 100%; disponibilidade do plano de controlo 99,9%; cold-start de sandbox < 125 ms (restore 5–30 ms); RPO ≤ 1 min / RTO ≤ 30 min *(proposta)*.

## 9. Dívidas e estados não compostos (não citar como controlos vigentes)

1. **Autonomia automática (DEF-908):** o `autonomy.Controller` (promoção/demoção automáticas por métrica) não está composto — a demoção automática não vigora; mudança de nível é decisão de provisionamento assinada. Fórmula do override-rate não tem limiar numérico fixado.
2. **Backup imutável em produção:** o exportador existe mas sem backend durável para a porta `ImmutableStore`; o RPO real de produção hoje é o do `backup.sh` diário (24 h). Os valores RPO ≤ 1 min / RTO ≤ 30 min são proposta a validar por game days.
3. **Cifra por titular do payload do ES:** o `payload` do Event Store fica em claro (dívida AOS-093; mitigado no audit com crypto-shredding, e a referência de `replay.captured` é digest não-reversível).
4. **Matriz RBAC formal** (papéis × permissões): não existe documentada; o modelo de autorização é cadeia NHI + política, os papéis de §5 são operacionais implícitos.
5. **Tectos estruturais** (`max_depth`/`max_fanout`/`max_nodes`, tecto de replans, tecto de capability gaps): declarados como derivados e auditáveis, sem valores canónicos.
6. **Limiares do circuit breaker** (velocity, wall-clock, dedup, progresso): configuráveis por classe de agente, sem valores publicados.
7. **Wiring do planeador com o gate** (mapeador `PlanDocument → planapproval.Plan`, DEF-274/AOS-238): porta e cartão entregues, composição pendente.
8. **Executor de skills declarativo:** skills ratificadas são artefactos governados sem interpretador (`ExecuteInProd` é só um gate).
9. **Lacunas doc-a-doc conhecidas** (nomes de eventos da v1.0 nunca emitidos, exemplos com envelope «gordo»): registadas em `tecnica/13` §7.

---

*Fim do documento 19 — Visão End-to-End: Arquitetura, Camadas e Processos.*
