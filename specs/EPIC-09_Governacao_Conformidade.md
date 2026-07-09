# EPIC-09 — Governação e Conformidade

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — Governação e Conformidade |
| Versão | 1.0 |
| Data | Julho de 2026 |
| Classificação | Documento de Referência — Aberto |
| Documento-fonte | `_FONTE_agentic-os-ideal.md` |
| Documentos relacionados | `tecnica/09_Governacao_Conformidade.md`, `specs/EPIC-01_Fundacoes_Plano_Controlo.md`, `specs/EPIC-07_Seguranca_Isolamento.md`, `specs/00_System_Spec.md`, `specs/01_Engineering_Standards_e_Handoff.md`, `tecnica/12_Contratos_de_Interface.md`, `tecnica/14_Matriz_Conformidade.md` |

---

## 1. Visão do Epic

Este epic materializa a camada transversal **Governação & Learning (GOV)** do AOS — a que envolve todas as outras e transforma segurança e conformidade em propriedades *arquitecturalmente impostas* em vez de aspiracionais. A tese é directa: a governação só é real quando o sistema torna a acção não-autorizada *impossível*, não *desencorajada*. Quando um regulador pergunta *quem autorizou* uma acção, o audit trail nunca pode responder "o pool" — o cenário *The Audit Log Lied*.

Os onze tickets (AOS-087 a AOS-097) entregam os controlos regulatórios do blueprint sobre as fundações já postas nos epics anteriores: enforcement programático via PDP/PEP no boundary de cada tool call com **allowlist capability-scoped default-deny** (AOS-087); **policy-as-code** versionada, assinada e com changelog no audit (AOS-088); a **taxonomia de autonomia L0–L5** com oversight proporcional (AOS-089) e **promoção por fiabilidade medida com demoção automática** (AOS-090); a conformidade GDPR por camadas — **redação de PII na ingestão** (AOS-091), **TTL por classe de dado** (AOS-092) e **crypto-shredding** para o direito ao apagamento (AOS-093); a **soberania por board** com failover proibido de cruzar fronteira (AOS-094); a **supervisão humana efectiva** do Art. 14 com aprovação assinada, fail-closed e override-rate medido (AOS-095); o **gate de ratificação de auto-modificação** (AOS-096); e o **modelo de responsabilização** com relatórios de conformidade (AOS-097).

O epic concretiza directamente **ADR-011** (policy-as-code + GDPR por desenho) e **ADR-014** (taxonomia de autonomia L0–L5), apoiando-se em **ADR-002** (Reference Monitor como PEP), **ADR-003** (identidade não-humana por agente), **ADR-010** (audit hash-chain + WORM), **ADR-012** (eval-gate para auto-modificação) e **ADR-013** (gates SA-ROC e HITL efectivo). É fundamentalmente da **Fase 2 — Governação e observabilidade** do roadmap, com os tickets de autonomia graduada e ratificação a estender-se para a **Fase 4 — UX e evolução**. A referência técnica autoritativa é `tecnica/09_Governacao_Conformidade.md`.

Este epic é o degrau **M3 — Governado** do modelo de maturidade: não se atinge sem a identidade e a durabilidade de M1/M2, porque sem elas a governação é teatro.

---

## 2. Critérios de Saída do Epic

- [ ] Nenhuma tool call é autorizada por *blocklist*; toda a autorização passa por **allowlist capability-scoped default-deny** avaliada pelo PDP no boundary de cada chamada (AOS-087).
- [ ] Toda a política é **policy-as-code** (Rego/OPA ou Cedar) versionada em git, assinada, e cada alteração escreve um changelog no audit trail (AOS-088).
- [ ] A **taxonomia L0–L5** está implementada por par (agente, domínio) com oversight proporcional ao impacto (AOS-089).
- [ ] A **promoção de nível** só ocorre por fiabilidade medida (ex.: erro < 2% por 30 dias) e a **demoção é automática** ao detectar anomalia (AOS-090).
- [ ] PII é **redigida/tokenizada na ingestão**; o que não é necessário nunca é persistido (AOS-091).
- [ ] Cada classe de dado tem um **TTL** próprio, com **legal hold** capaz de o suspender (AOS-092).
- [ ] Um **DSAR de apagamento (Art. 17)** é satisfeito por **crypto-shredding** sem quebrar a cadeia de hashes do audit (AOS-093).
- [ ] O **failover está proibido de cruzar fronteira** de soberania; a região é obrigação imposta pelo PEP (AOS-094).
- [ ] Os **approval gates** têm aprovador autorizado, timeout **fail-closed** para irreversíveis, aprovação **assinada** e **override-rate medido** (AOS-095).
- [ ] Nenhuma **auto-modificação** chega a produção sem **ratificação humana assinada** (AOS-096).
- [ ] Existe um **modelo de responsabilização** explícito (principal completo rastreável até um humano) e **relatórios de conformidade** gerados a partir do audit (AOS-097).
- [ ] Todos os tickets do epic têm DoD de domínio verde (política com teste allow/deny, spans OTel, sem segredos) conforme `specs/01_Engineering_Standards_e_Handoff.md`.

---

## 3. Tabela Resumo de Tickets

| ID | Título | Tipo | Estimativa | Prioridade | Dependências |
|---|---|---|---|---|---|
| AOS-087 | PDP/PEP capability allowlist default-deny (enforcement por tool call) | feature | L | P0 | EPIC-01 (RM/PEP), EPIC-07 (egress) |
| AOS-088 | Policy-as-code versionado/assinado + changelog no audit | feature | M | P1 | AOS-087, EPIC-08 (audit WORM) |
| AOS-089 | Taxonomia de autonomia L0–L5 + oversight proporcional | feature | M | P1 | AOS-087, AOS-095 |
| AOS-090 | Promoção por fiabilidade medida + demoção automática em anomalia | feature | M | P2 | AOS-089, EPIC-08 (métricas/evals) |
| AOS-091 | GDPR: redação de PII na ingestão | feature | M | P0 | EPIC-01 (Event Store), EPIC-08 (spans) |
| AOS-092 | TTL por classe de dado | feature | S | P1 | AOS-091, EPIC-08 (audit WORM) |
| AOS-093 | Crypto-shredding (direito ao apagamento, Art. 17) | feature | L | P0 | AOS-091, AOS-092, EPIC-07 (vault) |
| AOS-094 | Soberania por board (bloqueio cross-border) | feature | M | P1 | AOS-087, EPIC-06 (allowlist regional GW) |
| AOS-095 | Aprovação HITL (Art. 14): assinada, fail-closed, override-rate medido | feature | L | P0 | AOS-087, EPIC-01 (estado waiting_on_human) |
| AOS-096 | Gate de ratificação de auto-modificação | feature | M | P1 | AOS-088, AOS-095, EPIC-08 (eval-gate) |
| AOS-097 | Modelo de responsabilização + relatórios de conformidade | feature | M | P1 | AOS-088, AOS-095, EPIC-08 (audit WORM) |

Estimativas: XS/S/M/L (XL proibido). Prioridades: P0/P1/P2. Dependências cross-epic referidas por epic; intra-epic por ticket.

---

## AOS-087 — PDP/PEP capability allowlist default-deny (enforcement por tool call)

| Campo | Valor |
|---|---|
| Epic | EPIC-09 — Governação e Conformidade |
| Fase | 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | EPIC-01 (Reference Monitor/PEP, identidade por agente), EPIC-07 (egress default-deny) |
| Bloqueia | AOS-088, AOS-089, AOS-094, AOS-095 |
| Responsável sugerido | Engenheiro de Governação |
| Documentos de referência | `tecnica/09_Governacao_Conformidade.md` §4, `tecnica/01_Reference_Monitor_Plano_Controlo.md`, ADR-011, ADR-002 |

### Contexto

O desenho anterior usava uma *blocklist* de tools de sub-agente que *falhava aberta* a cada tool nova: uma tool desconhecida era implicitamente permitida (OWASP LLM/ASI — escalada por omissão). A governação exige o inverso — enforcement programático via par PDP/PEP no boundary de **cada** tool call, em que o que não está explicitamente concedido é negado. O PEP é o Reference Monitor (ADR-002); o PDP avalia policy-as-code por chamada. Cada capacidade é um par (recurso, acção) escopado ao principal (ADR-011).

### Objectivo

Implementar o Policy Decision Point e a sua integração com o Reference Monitor (PEP) de modo a que toda a tool call seja autorizada por uma **allowlist capability-scoped default-deny**, com decisão devolvendo *permit*/*deny*/*escala* e obrigações anexas, dentro do orçamento de mediação p95 < 15 ms.

### Critérios de Aceitação

- [ ] Toda a tool call é avaliada pelo PDP; **nenhum caminho** de código executa uma tool sem decisão prévia (verificado por teste de mediação total).
- [ ] A postura é **default-deny**: uma capacidade ausente da allowlist resulta em *deny* determinístico (teste com tool desconhecida devolve *deny*, nunca *permit*).
- [ ] Cada capacidade é modelada como par (recurso, acção) escopado ao principal; a autoridade efectiva é **utilizador ∩ classe de agente**.
- [ ] A decisão do PDP pode devolver **obrigações** (TTL, região, redação) que o PEP cumpre **antes** de libertar o efeito.
- [ ] O overhead de mediação do PDP é **p95 < 15 ms** medido em benchmark, com política compilada em memória.
- [ ] Cada decisão (permit/deny/escala) é registada no audit com o **principal completo** e o motivo.

### Detalhes Técnicos

- Componentes: **PDP** (novo, plano de controlo), **RM/PEP** (integração), **OBS** (audit da decisão).
- Motor de política: Rego/OPA ou Cedar embebido; política compilada e mantida em memória para latência.
- Modelo de capacidade: `(principal, recurso, acção)`; escopo derivado do token NHI (ver EPIC-01). Resultado: `permit | deny | escalate` + `obligations[]`.
- Hook de mediação no RM que bloqueia execução até decisão; obrigações aplicadas pelo PEP antes do dispatch para o sandbox.

### Testes Requeridos

- Teste de política/PDP cobrindo **allow** e **deny** com default-deny (tool concedida vs tool desconhecida).
- Teste de mediação total: tentativa de tool call fora do PEP falha (nenhuma via directa).
- Teste de obrigações: PDP devolve obrigação de redação/região e o PEP aplica-a antes do efeito.
- Benchmark de latência p95 do PDP < 15 ms.
- Teste de autoridade: capacidade concedida pela classe mas não pelo utilizador é negada (intersecção).

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Política em Rego/Cedar com teste allow/deny default-deny (DoD domínio).
- [ ] Toda a tool call mediada pelo Reference Monitor; sem chamada directa.
- [ ] Spans OTel GenAI da decisão do PDP emitidos com principal e resultado.
- [ ] Sem segredos em código/logs/spans; scan limpo.
- [ ] Benchmark de latência anexado como evidência.
- [ ] Revisão por dois revisores (artefacto P0/segurança).

### Handoff para Claude Code

```text
És o executor do ticket AOS-087 do Agentic OS de Referência (AOS).
Lê o ticket completo em specs/EPIC-09 e tecnica/09 §4, mais ADR-011 e ADR-002.
Implementa o PDP e a sua integração com o Reference Monitor (PEP) para autorizar
TODA a tool call por allowlist capability-scoped default-deny. Modela capacidade
como (principal, recurso, acção); autoridade = utilizador ∩ classe. A decisão
devolve permit/deny/escalate + obrigações (TTL, região, redação) que o PEP cumpre
antes de libertar o efeito. Mantém política compilada em memória (p95 < 15 ms).
Regista cada decisão no audit com o principal completo. NÃO uses blocklist e não
permitas nenhuma via directa de tool call. Escreve testes de política allow/deny,
mediação total, obrigações e benchmark de latência. Não expandas escopo; abre PR
com o template padrão e o checklist de domínio preenchido.
```

---

## AOS-088 — Policy-as-code versionado/assinado + changelog no audit

| Campo | Valor |
|---|---|
| Epic | EPIC-09 — Governação e Conformidade |
| Fase | 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-087, EPIC-08 (audit hash-chain + WORM) |
| Bloqueia | AOS-096, AOS-097 |
| Responsável sugerido | Engenheiro de Governação |
| Documentos de referência | `tecnica/09_Governacao_Conformidade.md` §4, ADR-011, ADR-010 |

### Contexto

A política não pode ser um artefacto opaco e mutável em runtime: a evolução das regras tem de ser ela própria auditável. O blueprint exige policy-as-code **versionada em git, assinada, com o changelog no próprio audit trail** (ADR-011). Uma política adulterada subverteria todo o enforcement; a assinatura e o registo encadeado são a defesa.

### Objectivo

Estabelecer o ciclo de vida da política: versionamento em git, assinatura criptográfica do bundle de política, verificação da assinatura no carregamento pelo PDP, e escrita automática do changelog de cada alteração no audit trail hash-chained.

### Critérios de Aceitação

- [ ] Cada bundle de política tem uma **versão** (SemVer ou hash de conteúdo) e é **assinado**; o PDP recusa carregar um bundle com assinatura inválida (fail-closed).
- [ ] Toda a alteração de política gera um **changelog** (versão anterior → nova, autor, motivo, diff) escrito no **audit trail** hash-chained.
- [ ] A política em runtime é sempre rastreável à versão assinada em git que a originou (manifesto de versão da política).
- [ ] Um bundle não assinado ou adulterado é **rejeitado**, mantendo a política anterior activa (sem janela de política ausente).
- [ ] O carregamento de nova política é um evento auditável com o principal que o efectuou.

### Detalhes Técnicos

- Componentes: **PDP** (carregamento/verificação), **GOV** (pipeline de política), **OBS** (changelog no audit WORM).
- Assinatura: chave de assinatura de política no vault (ver EPIC-07); verificação no PDP no load.
- Pipeline: política em git → build de bundle → assinatura → publicação; PDP faz *pull* e verifica antes de activar.
- Changelog: evento `policy.changed` no audit com diff e versões; ligação ao commit git.

### Testes Requeridos

- Teste de rejeição de bundle não assinado/adulterado (fail-closed, política anterior mantida).
- Teste de changelog: alteração de política escreve evento no audit com diff e versões.
- Teste de rastreabilidade: política em runtime corresponde à versão assinada.
- Teste de integridade: changelog encadeado no audit passa verificação de hash.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Verificação de assinatura testada (fail-closed em assinatura inválida).
- [ ] Changelog escrito no audit hash-chain + WORM (ADR-010).
- [ ] Spans OTel do carregamento/verificação de política emitidos.
- [ ] Sem segredos; chave de assinatura só via vault/broker.
- [ ] Documentação e ADR-011 referenciados; CHANGELOG do repo alimentado.

### Handoff para Claude Code

```text
És o executor do ticket AOS-088 do AOS. Lê specs/EPIC-09 e tecnica/09 §4, ADR-011
e ADR-010. Implementa o ciclo de vida da policy-as-code: versionamento em git,
assinatura do bundle (chave no vault), verificação da assinatura no PDP ao carregar
(fail-closed: bundle inválido é rejeitado e mantém-se a política anterior), e
escrita automática do changelog (versão anterior→nova, autor, motivo, diff) no
audit hash-chained. Garante rastreabilidade da política em runtime à versão git.
Escreve testes de rejeição de bundle adulterado, de changelog no audit e de
integridade da cadeia. Não expandas escopo; abre PR com o template padrão.
```

---

## AOS-089 — Taxonomia de autonomia L0–L5 + oversight proporcional

| Campo | Valor |
|---|---|
| Epic | EPIC-09 — Governação e Conformidade |
| Fase | 4 — UX e evolução |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-087, AOS-095 |
| Bloqueia | AOS-090 |
| Responsável sugerido | Engenheiro de Governação |
| Documentos de referência | `tecnica/09_Governacao_Conformidade.md` §7, ADR-014, ADR-013 |

### Contexto

O desenho anterior era quase-binário: "HITL por default, autonomia opt-in". O AOS substitui-o por uma escada de seis níveis (L0–L5) com **oversight proporcional ao impacto** (ADR-014). O nível é sempre uma propriedade do par **(agente, domínio)**: um agente pode operar a L4 num domínio de baixo risco e a L1 noutro sensível.

### Objectivo

Implementar a taxonomia de autonomia L0–L5 como atributo por (agente, domínio) que o PDP/PEP consulta para decidir o grau de oversight de cada tool call — desde L0 (sugestão, humano executa tudo) a L5 (autonomia plena por domínio com oversight amostral post-hoc).

### Critérios de Aceitação

- [ ] Os seis níveis estão definidos com semântica de oversight: L0 sugestão · L1 aprovação por acção · L2 aprovação por lote · L3 autonomia supervisionada (safe corre, danger confirma) · L4 autonomia por excepção · L5 autonomia plena por domínio (oversight amostral post-hoc).
- [ ] O nível é atribuído por **par (agente, domínio)** e consultado pelo PDP em cada decisão.
- [ ] O grau de gate aplicado a uma tool call é **função do nível** e da classe de risco (integra o tiering SA-ROC via AOS-095).
- [ ] O nível corrente de cada par (agente, domínio) é **auditável** e exposto na observabilidade.
- [ ] Alterações de nível são eventos auditáveis com motivo (manuais nesta fase; automáticas em AOS-090).

### Detalhes Técnicos

- Componentes: **GOV** (registo de níveis), **PDP** (consulta), **PEP/RM** (aplicação do oversight), **OBS** (exposição).
- Modelo: tabela `(agente, domínio) → nível` com histórico; consulta O(1) no caminho de decisão.
- Integração com o tiering SA-ROC (AOS-095) para mapear nível+risco → gate (auto / lote / individual).

### Testes Requeridos

- Teste por nível: cada L0–L5 produz o grau de oversight esperado para uma tool call de risco fixo.
- Teste de granularidade: o mesmo agente a L4 num domínio e L1 noutro é tratado diferentemente.
- Teste de auditoria: alteração de nível gera evento com motivo.
- Teste de integração PDP: a decisão reflecte nível × classe de risco.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Política do PDP a mapear nível × risco com teste allow/deny.
- [ ] Spans OTel expõem o nível corrente por (agente, domínio).
- [ ] Sem segredos; scan limpo.
- [ ] Documentação e ADR-014 referenciados.

### Handoff para Claude Code

```text
És o executor do ticket AOS-089 do AOS. Lê specs/EPIC-09 e tecnica/09 §7, ADR-014
e ADR-013. Implementa a taxonomia L0–L5 como atributo por par (agente, domínio),
consultado pelo PDP em cada decisão. Define a semântica de oversight de cada nível
(L0 sugestão … L5 autonomia plena com oversight amostral) e mapeia nível × classe
de risco para o grau de gate, integrando com o tiering SA-ROC de AOS-095. Expõe o
nível corrente na observabilidade e torna alterações de nível auditáveis (manuais
nesta fase). Escreve testes por nível, de granularidade por domínio e de integração
com o PDP. Não expandas escopo; abre PR com o template padrão.
```

---

## AOS-090 — Promoção por fiabilidade medida + demoção automática em anomalia

| Campo | Valor |
|---|---|
| Epic | EPIC-09 — Governação e Conformidade |
| Fase | 4 — UX e evolução |
| Tipo | feature |
| Prioridade | P2 |
| Estimativa | M |
| Dependências | AOS-089, EPIC-08 (métricas, evals, circuit breaker multi-sinal) |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Governação |
| Documentos de referência | `tecnica/09_Governacao_Conformidade.md` §7, ADR-014 |

### Contexto

A promoção de autonomia nunca pode ser concedida por opinião — seria promover um agente pouco fiável para autonomia alta. O blueprint exige **promoção baseada em fiabilidade medida** (ex.: erro < 2% por 30 dias, override-rate baixo) e **demoção automática e imediata** ao detectar anomalia (pico de override-rate, acção insegura, deriva medida), sem esperar por revisão humana (ADR-014).

### Objectivo

Implementar o controlador de autonomia que promove um par (agente, domínio) na escada L0–L5 apenas ao atingir limiares de fiabilidade sustentada, e o demove automaticamente ao detectar anomalia, alimentado pelas métricas e sinais da observabilidade.

### Critérios de Aceitação

- [ ] A **promoção** só ocorre quando a métrica de fiabilidade sustentada é satisfeita (ex.: taxa de erro < 2% por 30 dias **e** override-rate abaixo do limiar); caso contrário o nível mantém-se.
- [ ] A **demoção é automática e imediata** ao detectar anomalia (pico de override-rate, acção insegura sinalizada, deriva medida), sem gate humano.
- [ ] Os limiares de promoção/demoção são **configuráveis por política** (policy-as-code, AOS-088).
- [ ] Cada promoção/demoção é um evento auditável com a métrica e o motivo que a justificou.
- [ ] Uma anomalia rebaixa para um nível **mais supervisionado** (ex.: L4→L2, L3→L1) de forma determinística.

### Detalhes Técnicos

- Componentes: **GOV** (controlador de autonomia), **OBS** (fonte de métricas/sinais: override-rate, unsafe-action rate, deriva, circuit breaker multi-sinal), **PDP** (limiares por política).
- Ligação aos sinais de EPIC-08: override-rate medido (AOS-095), unsafe-action rate, detecção de anomalia/loop semântico.
- Janela deslizante para fiabilidade sustentada; demoção event-driven (reactiva).

### Testes Requeridos

- Teste de promoção: fiabilidade sustentada acima do limiar promove; abaixo mantém.
- Teste de demoção automática: injecção de anomalia (override-rate alto / acção insegura) rebaixa imediatamente.
- Teste de configurabilidade: limiares alterados por política alteram o comportamento.
- Teste de auditoria: cada transição regista métrica e motivo.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Limiares expressos como policy-as-code com teste.
- [ ] Spans/métricas OTel das transições de nível emitidos.
- [ ] Sem segredos; scan limpo.
- [ ] Documentação e ADR-014 referenciados.

### Handoff para Claude Code

```text
És o executor do ticket AOS-090 do AOS. Lê specs/EPIC-09 e tecnica/09 §7, ADR-014.
Implementa o controlador de autonomia sobre a taxonomia L0–L5 (AOS-089): promove um
par (agente, domínio) só por fiabilidade medida sustentada (ex.: erro <2% por 30
dias e override-rate baixo) e demove-o AUTOMÁTICA e IMEDIATAMENTE em anomalia (pico
de override-rate, acção insegura, deriva), para um nível mais supervisionado, sem
gate humano. Usa os sinais da observabilidade (EPIC-08) e limiares configuráveis
por policy-as-code (AOS-088). Torna cada transição auditável com métrica e motivo.
Escreve testes de promoção, demoção automática, configurabilidade e auditoria.
Não expandas escopo; abre PR com o template padrão.
```

---

## AOS-091 — GDPR: redação de PII na ingestão

| Campo | Valor |
|---|---|
| Epic | EPIC-09 — Governação e Conformidade |
| Fase | 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | EPIC-01 (Event Store), EPIC-08 (spans/audit) |
| Bloqueia | AOS-092, AOS-093 |
| Responsável sugerido | Engenheiro de Dados/Memória |
| Documentos de referência | `tecnica/09_Governacao_Conformidade.md` §5, ADR-011 |

### Contexto

A conformidade GDPR faz-se por camadas, e a primeira é a **minimização**: PII deve ser tokenizada ou redigida **antes** de entrar no sistema; o que não é necessário nunca é persistido (ADR-011). Redigir na ingestão reduz a superfície de dados pessoais em todo o pipeline downstream (memória, trajectórias, audit) e é pré-requisito para o TTL e o crypto-shredding.

### Objectivo

Implementar a camada de redação/tokenização de PII na ingestão — aplicada a inputs de utilizador, tool results e conteúdo que entra na memória — de modo que PII seja substituída por tokens reversíveis (quando necessária) ou removida (quando não), antes de qualquer persistência.

### Critérios de Aceitação

- [ ] PII é detectada e **redigida/tokenizada na ingestão**, antes de escrita no Event Store, memória ou audit.
- [ ] Dados não necessários são **removidos** (minimização); dados necessários são **tokenizados** com referência à chave por titular (prepara AOS-093).
- [ ] A redação é aplicada de forma **consistente** em todas as vias de entrada (utilizador, tool result, ingestão de memória).
- [ ] O conteúdo redigido preserva a **utilidade operacional** (a trajectória continua reproduzível na parte não-pessoal) e a **integridade do audit** (hash calculado sobre o payload já tratado).
- [ ] Nenhuma PII em claro aparece em spans, logs ou audit (verificado por scan).

### Detalhes Técnicos

- Componentes: **GOV** (motor de redação), **MEM** (ingestão), **RM/PEP** (obrigação de redação vinda do PDP — ver AOS-087), **OBS** (garantia de ausência de PII em spans).
- Detecção de PII: classificadores/padrões por classe; tokenização com mapa por titular (chave preparada para crypto-shredding).
- Ponto de aplicação: na fronteira de ingestão, antes de qualquer persistência; obrigação `redact` do PDP aciona-a no PEP.

### Testes Requeridos

- Teste de redação: input com PII conhecida é tokenizado/removido antes de persistir.
- Teste de cobertura de vias: utilizador, tool result e memória — todas redigidas.
- Teste de ausência de PII: scan de spans/audit/logs não encontra PII em claro.
- Teste de utilidade: trajectória redigida mantém reprodutibilidade não-pessoal.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Obrigação de redação integrada com o PDP/PEP (AOS-087) e testada.
- [ ] Spans OTel sem PII; scan de PII limpo.
- [ ] Sem segredos; scan limpo.
- [ ] Documentação e ADR-011 referenciados.

### Handoff para Claude Code

```text
És o executor do ticket AOS-091 do AOS. Lê specs/EPIC-09 e tecnica/09 §5, ADR-011.
Implementa a camada de redação/tokenização de PII na INGESTÃO, aplicada a inputs de
utilizador, tool results e ingestão de memória, ANTES de qualquer persistência
(Event Store, memória, audit). Minimiza (remove o desnecessário) e tokeniza o
necessário com referência a uma chave por titular (prepara o crypto-shredding de
AOS-093). Aciona-a via obrigação `redact` do PDP/PEP (AOS-087). Garante ausência de
PII em claro em spans/audit/logs e preserva a reprodutibilidade não-pessoal.
Escreve testes de redação, cobertura de vias, ausência de PII e utilidade.
Não expandas escopo; abre PR com o template padrão.
```

---

## AOS-092 — TTL por classe de dado

| Campo | Valor |
|---|---|
| Epic | EPIC-09 — Governação e Conformidade |
| Fase | 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | S |
| Dependências | AOS-091, EPIC-08 (audit WORM, retenção) |
| Bloqueia | AOS-093 |
| Responsável sugerido | Engenheiro de Dados/Memória |
| Documentos de referência | `tecnica/09_Governacao_Conformidade.md` §5, ADR-011 |

### Contexto

Nem todos os dados têm o mesmo tempo de vida legítimo. Diagnósticos efémeros devem expirar cedo; o audit tamper-evident retém-se mais tempo, por vezes sob **legal hold**. O blueprint exige **TTL por classe de dado** (ADR-011) como camada intermédia entre a minimização (AOS-091) e o crypto-shredding (AOS-093).

### Objectivo

Implementar a classificação de dados e a aplicação de TTL por classe (diagnóstico, trajectória, audit, PII operacional), com um mecanismo de expiração automática e a capacidade de **legal hold** suspender o TTL sobre registos sob obrigação de preservação.

### Critérios de Aceitação

- [ ] Cada dado persistido é **classificado** (diagnóstico, trajectória, audit, PII operacional, …) e tem um **TTL** próprio configurável.
- [ ] Dados **expiram automaticamente** ao fim do seu TTL (diagnósticos cedo; audit conforme retenção regulatória).
- [ ] Um **legal hold** aplicado a registos **suspende** o TTL (e a expiração/crypto-shredding) enquanto vigorar.
- [ ] A configuração de TTL por classe é expressa como **política** (policy-as-code, AOS-088) e auditável.
- [ ] A expiração é um evento auditável (o quê, quando, por que classe/TTL).

### Detalhes Técnicos

- Componentes: **GOV** (política de retenção), **MEM**/**ES** (expiração), **OBS** (retenção do audit WORM, legal hold).
- Classes de dado e TTLs em policy-as-code; job de expiração idempotente sobre o Event Store/memória.
- Legal hold como flag que suspende o job de expiração para os registos marcados (cruza com AOS-093).

### Testes Requeridos

- Teste de expiração por classe: cada classe expira ao seu TTL; audit retém-se conforme configurado.
- Teste de legal hold: registo sob hold não expira mesmo após o TTL.
- Teste de configurabilidade: TTL alterado por política altera a expiração.
- Teste de idempotência do job de expiração (reexecução não duplica efeitos).

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] TTL por classe expresso como policy-as-code com teste.
- [ ] Job de expiração idempotente (idempotency key por passo) testado.
- [ ] Spans/eventos OTel de expiração emitidos.
- [ ] Sem segredos; scan limpo.

### Handoff para Claude Code

```text
És o executor do ticket AOS-092 do AOS. Lê specs/EPIC-09 e tecnica/09 §5, ADR-011.
Implementa a classificação de dados e o TTL por classe (diagnóstico, trajectória,
audit, PII operacional), configurável como policy-as-code (AOS-088). Adiciona um job
de expiração automática IDEMPOTENTE (idempotency key por passo) sobre Event Store e
memória, e um mecanismo de LEGAL HOLD que suspende TTL e expiração para registos
marcados. Torna a expiração auditável. Escreve testes de expiração por classe, de
legal hold, de configurabilidade e de idempotência do job.
Não expandas escopo; abre PR com o template padrão.
```

---

## AOS-093 — Crypto-shredding (direito ao apagamento, Art. 17)

| Campo | Valor |
|---|---|
| Epic | EPIC-09 — Governação e Conformidade |
| Fase | 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | AOS-091, AOS-092, EPIC-07 (vault/credential broker), EPIC-08 (audit hash-chain) |
| Bloqueia | AOS-097 |
| Responsável sugerido | Engenheiro de Segurança |
| Documentos de referência | `tecnica/09_Governacao_Conformidade.md` §5, ADR-011, ADR-010 |

### Contexto

A maior tensão da governação é *audit imutável* **vs** *direito ao apagamento* (GDPR Art. 17). Um log hash-chained é, por construção, imutável, mas o titular tem o direito de ver os seus dados pessoais apagados. A resolução é redefinir "imutável" como **tamper-evidence do registo**, não retenção eterna do payload: os dados pessoais são cifrados com uma chave **por titular**, e satisfazer um DSAR de apagamento consiste em **destruir essa chave** — o payload torna-se irrecuperável, mas os hashes continuam a validar (ADR-011).

### Objectivo

Implementar o crypto-shredding: cifra de PII por chave-por-titular guardada no vault, e o fluxo de DSAR de apagamento que destrói a chave, tornando o payload cifrado irrecuperável **sem** reescrever o log encadeado nem quebrar a cadeia de hashes.

### Critérios de Aceitação

- [ ] Toda a PII persistida é **cifrada com uma chave por titular** (chave no vault; o agente nunca a vê — EPIC-07).
- [ ] Um **DSAR de apagamento** destrói a chave do titular; após isso o payload cifrado é **irrecuperável** (teste prova que decifração falha).
- [ ] A **cadeia de hashes do audit permanece íntegra e válida** após o crypto-shredding (verificação de integridade passa).
- [ ] O registo de **quem fez o quê, quando** (metadados não-pessoais) é preservado como facto de conformidade.
- [ ] Um titular sob **legal hold** (AOS-092) **não** é sujeito a crypto-shredding enquanto o hold vigorar.
- [ ] O apagamento é um evento auditável (DSAR recebido, chave destruída, timestamp).

### Detalhes Técnicos

- Componentes: **GOV** (fluxo DSAR), **BRK/Vault** (chave por titular, destruição), **OBS/ES** (audit hash-chain intacto).
- Cifra envelope: chave por titular no vault cifra PII antes de persistir; destruição da chave = shredding.
- Interacção com legal hold (AOS-092): shredding bloqueado enquanto hold activo.
- O hash do audit é calculado sobre o payload cifrado, pelo que a destruição da chave não altera hashes.

### Testes Requeridos

- Teste de irrecuperabilidade: após destruir a chave, a decifração do payload falha determinísticamente.
- Teste de integridade: verificação da cadeia de hashes passa antes **e** depois do shredding.
- Teste de legal hold: DSAR sobre titular sob hold é bloqueado.
- Teste de auditoria: o fluxo DSAR gera eventos (recepção, destruição) sem expor PII.
- Teste de isolamento de segredo: a chave por titular nunca é exposta ao agente/logs.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Chave por titular gerida via vault/broker JIT; sem segredos em código/logs (ADR-006).
- [ ] Integridade da cadeia de hashes verificada pós-shredding (ADR-010).
- [ ] Interacção com legal hold (AOS-092) testada.
- [ ] Spans/eventos OTel do fluxo DSAR sem PII.
- [ ] Revisão por dois revisores (artefacto P0/segurança).

### Handoff para Claude Code

```text
És o executor do ticket AOS-093 do AOS. Lê specs/EPIC-09 e tecnica/09 §5, ADR-011,
ADR-010 e ADR-006. Implementa o crypto-shredding: cifra toda a PII persistida com
uma chave POR TITULAR guardada no vault (agente nunca a vê). Implementa o fluxo de
DSAR de apagamento (Art. 17) que DESTRÓI a chave, tornando o payload irrecuperável
SEM reescrever o log encadeado — os hashes do audit têm de continuar a validar antes
e depois. Preserva os metadados não-pessoais (quem/quando). Respeita o legal hold
(AOS-092): não fazer shredding enquanto vigorar. Torna o fluxo auditável sem expor
PII. Escreve testes de irrecuperabilidade, integridade da cadeia de hashes, legal
hold, auditoria e isolamento de segredo. Não expandas escopo; abre PR com o template.
```

---

## AOS-094 — Soberania por board (bloqueio cross-border)

| Campo | Valor |
|---|---|
| Epic | EPIC-09 — Governação e Conformidade |
| Fase | 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-087, EPIC-06 (allowlist regional do Model Gateway) |
| Bloqueia | AOS-097 |
| Responsável sugerido | Engenheiro de Governação |
| Documentos de referência | `tecnica/09_Governacao_Conformidade.md` §6, ADR-011 |

### Contexto

A soberania de dados é imposta por **board** (fronteira regional). A allowlist de modelos do Model Gateway é regional, e o **failover está proibido de cruzar fronteira**: um board europeu que perca capacidade não pode fazer failover para uma região fora da UE, porque isso constituiria transferência ilegal de PII (ADR-011). A soberania tem de ser uma propriedade do **enforcement**, não uma política em papel.

### Objectivo

Implementar a imposição de soberania por board: a região autorizada é codificada no escopo de identidade e devolvida pelo PDP como **obrigação**; o PEP recusa qualquer roteamento (incluindo failover) que a viole, cruzando com a allowlist regional do Model Gateway.

### Critérios de Aceitação

- [ ] Cada board tem uma **fronteira regional** associada; o escopo de identidade codifica a região autorizada.
- [ ] O PDP devolve a **região como obrigação**; o PEP **recusa** qualquer roteamento que a viole.
- [ ] O **failover está proibido de cruzar fronteira**: perda de capacidade num board europeu **não** encaminha para região fora da UE (teste prova recusa).
- [ ] A allowlist regional do Model Gateway (EPIC-06) é respeitada — nenhuma chamada de modelo sai da região autorizada.
- [ ] Uma tentativa de roteamento cross-border é um evento auditável (deny + motivo de soberania).

### Detalhes Técnicos

- Componentes: **PDP/PEP** (obrigação de região), **GW** (allowlist regional, ver EPIC-06), **GOV** (mapa board→região), **OBS** (audit do deny).
- Região no escopo do token NHI (EPIC-01); obrigação `region` propagada ao PEP e ao Model Gateway.
- Política de failover restrita à mesma fronteira de soberania.

### Testes Requeridos

- Teste de bloqueio cross-border: failover para fora da região é recusado (fail-closed).
- Teste de obrigação de região: PDP devolve região, PEP aplica-a.
- Teste de allowlist regional: chamada de modelo fora da região é negada.
- Teste de auditoria: tentativa cross-border gera deny com motivo de soberania.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Obrigação de região expressa em policy-as-code com teste allow/deny.
- [ ] Integração com allowlist regional do Model Gateway verificada (EPIC-06).
- [ ] Spans OTel do roteamento com região; deny auditado.
- [ ] Sem segredos; scan limpo.

### Handoff para Claude Code

```text
És o executor do ticket AOS-094 do AOS. Lê specs/EPIC-09 e tecnica/09 §6, ADR-011.
Implementa a soberania por board: associa cada board a uma fronteira regional,
codifica a região no escopo de identidade, e faz o PDP devolvê-la como OBRIGAÇÃO que
o PEP impõe. O failover fica PROIBIDO de cruzar fronteira (perda de capacidade num
board UE não encaminha para fora da UE — fail-closed). Cruza com a allowlist regional
do Model Gateway (EPIC-06) para que nenhuma chamada de modelo saia da região. Audita
tentativas cross-border com motivo de soberania. Escreve testes de bloqueio
cross-border, obrigação de região, allowlist regional e auditoria.
Não expandas escopo; abre PR com o template padrão.
```

---

## AOS-095 — Aprovação HITL (Art. 14): assinada, fail-closed, override-rate medido

| Campo | Valor |
|---|---|
| Epic | EPIC-09 — Governação e Conformidade |
| Fase | 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | AOS-087, EPIC-01 (estado durável waiting_on_human) |
| Bloqueia | AOS-089, AOS-096 |
| Responsável sugerido | Engenheiro de Governação |
| Documentos de referência | `tecnica/09_Governacao_Conformidade.md` §8, ADR-013, ADR-011 |

### Contexto

O Art. 14 do EU AI Act exige supervisão humana **efectiva**, não teatro de aprovações. O risco documentado é a *approval fatigue*: utilizadores experientes auto-aprovam mais de 40% dos pedidos, anulando a governação. A resposta são approval gates com quatro propriedades: **aprovador autorizado**, **timeout fail-closed** para irreversíveis, **aprovação assinada** (não-repúdio) e **medição de override-rate** anti-rubber-stamping (ADR-013). O tiering SA-ROC (safe corre, gray agrupa, danger confirma) é o eixo que os torna efectivos.

### Objectivo

Implementar o gate HITL sobre o estado durável `waiting_on_human`: escalada de acções danger/irreversíveis com preview do efeito concreto, aprovação assinada por um principal autorizado, timeout fail-closed (silêncio nega para irreversíveis), e medição do override-rate como sinal de governação.

### Critérios de Aceitação

- [ ] Acções de classe **danger/irreversível** escalam para um gate HITL com **preview do efeito concreto** resolvido; a execução pausa no estado durável `waiting_on_human`.
- [ ] A aprovação vem de um **aprovador autorizado** (principal com autoridade); aprovações de principal sem autoridade são recusadas.
- [ ] Para acções **irreversíveis**, o **timeout é fail-closed**: o silêncio **nega**, nunca permite (estado transita para `killed`/negado).
- [ ] Cada aprovação/recusa é **assinada criptograficamente** (não-repúdio) e registada no audit.
- [ ] O **override-rate** (aprovações concedidas sem escrutínio efectivo) é **medido** e exposto como sinal; um override-rate cronicamente alto dispara revisão (alimenta AOS-090).
- [ ] Acções `safe` correm sem gate e `gray` agrupam em lote (tiering SA-ROC).

### Detalhes Técnicos

- Componentes: **RM/PEP** (escalada), **GOV** (fluxo de aprovação, assinatura), **RT/SCH** (estado `waiting_on_human`, timeout — ver EPIC-01/EPIC-02), **OBS** (override-rate, audit da aprovação).
- Preview do efeito concreto resolvido antes do gate; dual-control 4-eyes para danger.
- Assinatura de aprovação com chave do aprovador; timeout fail-closed configurável por classe.
- Métrica `approval.override_rate` exposta em OTel.

### Testes Requeridos

- Teste de escalada: acção danger pausa em `waiting_on_human` com preview.
- Teste de timeout fail-closed: silêncio em acção irreversível nega (nunca permite).
- Teste de aprovador autorizado: aprovação de principal sem autoridade é recusada.
- Teste de assinatura: aprovação/recusa assinada e verificável no audit (não-repúdio).
- Teste de override-rate: métrica calculada e exposta; limiar dispara sinal.
- Teste de tiering: safe corre, gray agrupa, danger confirma.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Estado durável `waiting_on_human` integrado (EPIC-01/02) com timeout fail-closed.
- [ ] Política de tiering SA-ROC expressa como policy-as-code com teste.
- [ ] Aprovação assinada; override-rate em spans/métricas OTel.
- [ ] Sem segredos; chave de assinatura via vault.
- [ ] Revisão por dois revisores (artefacto P0/segurança).

### Handoff para Claude Code

```text
És o executor do ticket AOS-095 do AOS. Lê specs/EPIC-09 e tecnica/09 §8, ADR-013 e
ADR-011. Implementa o gate HITL (Art. 14) sobre o estado durável waiting_on_human:
acções danger/irreversíveis escalam com PREVIEW do efeito concreto; a aprovação vem
de um APROVADOR AUTORIZADO e é ASSINADA (não-repúdio, chave via vault); o timeout é
FAIL-CLOSED para irreversíveis (silêncio nega); o OVERRIDE-RATE é medido e exposto
como sinal (alimenta AOS-090). Aplica o tiering SA-ROC (safe corre, gray agrupa,
danger confirma; dual-control 4-eyes para danger). Escreve testes de escalada,
timeout fail-closed, aprovador autorizado, assinatura, override-rate e tiering.
Não expandas escopo; abre PR com o template padrão.
```

---

## AOS-096 — Gate de ratificação de auto-modificação

| Campo | Valor |
|---|---|
| Epic | EPIC-09 — Governação e Conformidade |
| Fase | 4 — UX e evolução |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-088, AOS-095, EPIC-08 (eval-gate, canary) |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Governação |
| Documentos de referência | `tecnica/09_Governacao_Conformidade.md` §9, ADR-012 |

### Contexto

A auto-modificação (skills auto-escritas, memória procedural) é a mudança de **maior risco** do sistema — a *misevolution* ocorre mesmo sem atacante. A governação trata-a como classe de mudança distinta, sujeita a um gate de **ratificação assinada**. O pipeline completo (staging → eval-gate → canary → ratificação → produção com SemVer e rollback atómico) pertence a ADR-012; do ponto de vista da governação, o ponto não-negociável é que **nenhuma auto-modificação chega a produção sem ratificação humana assinada** — o momento em que um humano responsável assume a mudança na cadeia de responsabilização.

### Objectivo

Implementar o gate de ratificação de governação que se interpõe entre o canary e a produção no pipeline de auto-modificação: sem assinatura de um humano responsável, a promoção a produção é **bloqueada fail-closed**.

### Critérios de Aceitação

- [ ] Nenhum artefacto auto-escrito (skill, memória procedural) é promovido a **produção** sem **ratificação humana assinada**.
- [ ] A ratificação é **assinada** (não-repúdio) e liga o humano responsável à mudança na cadeia de responsabilização (audit).
- [ ] Na ausência de ratificação, a promoção é **bloqueada fail-closed** (o artefacto fica em canary/staging, nunca em prod).
- [ ] O gate consome o resultado do **eval-gate** e do **canary** (EPIC-08); só apresenta para ratificação o que passou essas etapas.
- [ ] A ratificação/recusa é auditável (quem ratificou, versão SemVer, resultado do eval, timestamp).

### Detalhes Técnicos

- Componentes: **GOV** (gate de ratificação), **REG** (versão SemVer do artefacto — ver EPIC-05), **OBS** (audit da ratificação, ligação ao eval `gen_ai.evaluation.result`).
- Interface reutiliza a infra de aprovação assinada de AOS-095; ratificação como classe de aprovação de auto-modificação.
- Bloqueio fail-closed no *promotion controller* do pipeline de auto-modificação (ADR-012, detalhe em `tecnica/11`).

### Testes Requeridos

- Teste fail-closed: artefacto sem ratificação **não** chega a prod (fica em canary/staging).
- Teste de ratificação assinada: promoção só após assinatura verificável do humano responsável.
- Teste de pré-condição: artefacto que falhou eval-gate/canary não é apresentado para ratificação.
- Teste de auditoria: ratificação ligada ao eval e à versão SemVer no audit.

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Bloqueio fail-closed da promoção sem ratificação testado (ADR-012).
- [ ] Ratificação assinada e auditada; ligação ao eval result (ADR-010).
- [ ] Reutiliza a aprovação assinada de AOS-095; sem duplicação.
- [ ] Sem segredos; scan limpo.

### Handoff para Claude Code

```text
És o executor do ticket AOS-096 do AOS. Lê specs/EPIC-09 e tecnica/09 §9, ADR-012.
Implementa o gate de ratificação de governação entre o canary e a produção no
pipeline de auto-modificação: NENHUM artefacto auto-escrito (skill, memória
procedural) chega a produção sem RATIFICAÇÃO HUMANA ASSINADA — sem ela, a promoção é
BLOQUEADA FAIL-CLOSED (fica em canary/staging). Consome o resultado do eval-gate e do
canary (EPIC-08); só apresenta para ratificação o que passou. Liga a ratificação ao
humano responsável na cadeia de responsabilização e ao eval result / versão SemVer no
audit. Reutiliza a aprovação assinada de AOS-095. Escreve testes de fail-closed,
ratificação assinada, pré-condição de eval e auditoria. Não expandas escopo; abre PR.
```

---

## AOS-097 — Modelo de responsabilização + relatórios de conformidade

| Campo | Valor |
|---|---|
| Epic | EPIC-09 — Governação e Conformidade |
| Fase | 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-088, AOS-093, AOS-094, AOS-095, EPIC-08 (audit WORM) |
| Bloqueia | — |
| Responsável sugerido | Engenheiro de Governação |
| Documentos de referência | `tecnica/09_Governacao_Conformidade.md` §8, ADR-011, ADR-003, ADR-010 |

### Contexto

O modelo de responsabilização é explícito e assenta na cadeia de delegação: **cada acção tem um principal completo rastreável até um humano; cada decisão de política fica no audit com o seu motivo; cada aprovação é assinada. Não existem execuções anónimas.** Isto satisfaz simultaneamente a supervisão humana efectiva (Art. 14) e a exigência de **atribuição inequívoca** perante um regulador. Para tornar essa atribuição utilizável, o sistema precisa de gerar **relatórios de conformidade** a partir do audit tamper-evident.

### Objectivo

Formalizar o modelo de responsabilização (garantia de que toda a acção é rastreável ao principal completo até um humano) e implementar a geração de relatórios de conformidade a partir do audit — cobrindo atribuição, decisões de política, aprovações, DSARs e soberania.

### Critérios de Aceitação

- [ ] Toda a acção no sistema tem um **principal completo** (a cadeia de delegação inteira) rastreável até um **humano responsável**; **não existem execuções anónimas** (verificado por auditoria de completude).
- [ ] Cada **decisão de política** e cada **aprovação** ficam no audit com motivo/assinatura, ligadas ao principal.
- [ ] O sistema **gera relatórios de conformidade** a partir do audit: atribuição de acções, decisões PDP (permit/deny), aprovações HITL e override-rate, DSARs/crypto-shredding, e eventos de soberania.
- [ ] Os relatórios são **derivados do audit tamper-evident** e a sua integridade é verificável (hashes validam).
- [ ] Os relatórios **não expõem PII** em claro (respeitam a redação e o crypto-shredding).

### Detalhes Técnicos

- Componentes: **GOV** (modelo de responsabilização, gerador de relatórios), **OBS** (fonte: audit WORM hash-chained), **PDP** (decisões), **BRK** (eventos DSAR).
- Relatório como projecção *query-time* sobre o audit (padrão wide events, ver EPIC-08); sem duplicar dados.
- Verificação de completude: nenhuma acção sem principal; alerta se detectada.

### Testes Requeridos

- Teste de completude: injectar acção sem principal completo é detectado/rejeitado (sem anonimato).
- Teste de relatório: gera relatório de atribuição, decisões, aprovações, DSARs e soberania a partir de audit conhecido.
- Teste de integridade: relatório derivado passa verificação de hash do audit.
- Teste de ausência de PII: relatório não contém PII em claro (pós-redação/shredding).

### Definition of Done

- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Relatórios derivados do audit hash-chain + WORM; integridade verificável (ADR-010).
- [ ] Completude do principal garantida (ADR-003); sem execuções anónimas.
- [ ] Spans OTel da geração de relatório; sem PII.
- [ ] Sem segredos; scan limpo.

### Handoff para Claude Code

```text
És o executor do ticket AOS-097 do AOS. Lê specs/EPIC-09 e tecnica/09 §8, ADR-011,
ADR-003 e ADR-010. Formaliza o modelo de responsabilização: TODA a acção tem um
principal completo rastreável até um humano; cada decisão de política e cada
aprovação ficam no audit com motivo/assinatura; NÃO existem execuções anónimas
(auditoria de completude). Implementa a geração de RELATÓRIOS DE CONFORMIDADE como
projecção query-time sobre o audit tamper-evident (atribuição, decisões PDP,
aprovações HITL + override-rate, DSARs/crypto-shredding, soberania), sem duplicar
dados e sem expor PII em claro. Garante que os relatórios validam contra os hashes do
audit. Escreve testes de completude, geração de relatório, integridade e ausência de
PII. Não expandas escopo; abre PR com o template padrão.
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
| 1.0 | Julho 2026 | Emissão inicial | Equipa AOS |
</content>
</invoke>
