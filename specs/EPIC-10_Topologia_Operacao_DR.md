# EPIC-10 — Topologia, Operação e DR

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — Topologia, Operação e DR |
| Versão | 1.0 |
| Data | Julho de 2026 |
| Classificação | Documento de Referência — Aberto |
| Documento-fonte | `_FONTE_agentic-os-ideal.md` |
| Documentos relacionados | `tecnica/10_Topologia_Implantacao_Operacao.md`, `specs/EPIC-03_Orquestracao_Escalonamento.md`, `specs/EPIC-08_Observabilidade_Evals.md`, `specs/01_Engineering_Standards_e_Handoff.md` |

---

## 1. Visão do Epic

Este epic operacionaliza a **topologia de implantação de referência** do AOS e o modelo de operação que a sustenta em produção. Transforma em backlog executável o que `tecnica/10_Topologia_Implantacao_Operacao.md` descreve: a separação física entre **plano de controlo** (decide) e **plano de dados** (executa e regista), a **escala horizontal** com degradação graciosa, a **recuperação de desastre (DR)** ancorada no backup do Event Store e no replay determinístico, e a observação operacional a partir dos SLIs.

O princípio orientador é o mesmo de toda a arquitectura: a topologia deve tornar as falhas operacionais **recuperáveis por desenho**. Três alavancas concretizam-no — workers *stateless* sobre um **Event Store replicado** (ADR-007), **admission control global** com reserva de headroom (ADR-008) e trajectória completa em OTel com **audit WORM** (ADR-010). Juntas, transformam incidentes de produção em operações rotineiras de recuperação, e não em perda de dados ou de atribuição.

O epic entrega o provisionamento por Infraestrutura-como-Código (IaC) do plano de controlo e de dados, os workers *stateless* com estado particionado, a replicação e o backup com *Point-In-Time Recovery* (PITR) do Event Store, o plano de DR por replay com RPO/RTO definidos, o pool de microVMs em produção, os dashboards e alertas operacionais, o catálogo de runbooks para os modos de falha canónicos, a escala horizontal com degradação graciosa e, por fim, o **hipercare** que estabiliza o sistema em produção. Não cobre o desenho interno de cada subsistema (remetido aos documentos `tecnica/`) nem o harness de testes e de carga (tratado em `specs/EPIC-11`).

Depende das fundações do plano de controlo (`specs/EPIC-01`, para o Event Store replicado e a identidade), da orquestração e admission control (`specs/EPIC-03`) e da observabilidade (`specs/EPIC-08`, para spans, SLIs e audit). É a Fase 3–4 do roadmap: escala, controlo e operacionalização.

---

## 2. Critérios de Saída do Epic

- [ ] O plano de controlo e o plano de dados são provisionados **exclusivamente** por IaC versionada, reproduzível e revista, sem passos manuais.
- [ ] Os workers são **stateless** e o estado durável vive apenas no Event Store replicado e no estado particionado por *run*; qualquer worker pode morrer e ser substituído sem perda.
- [ ] O Event Store está **replicado por quorum** (ADR-007), com backup imutável e **PITR** validado por restauro de teste.
- [ ] Existe um plano de **DR por replay determinístico** com **RPO ≤ 1 min** e **RTO ≤ 30 min** validados por *game day* documentado.
- [ ] O **pool de microVMs pré-aquecidas** opera em produção com cold-start < 125 ms e restore 5–30 ms (ADR-004), dimensionado ao headroom.
- [ ] Os **dashboards operacionais** cobrem todos os SLIs canónicos, por plano e por *run*, com *drill-down* até à tool call e custo em USD por span.
- [ ] Os **alertas** disparam sobre SLIs (não sobre filtragem no emit-time) e cada alerta liga a um runbook accionável; não há alertas sem runbook.
- [ ] Os **cinco runbooks canónicos** estão escritos, testados em *game day* e ligados aos alertas: colapso de rate limit, zumbi cross-host, esgotamento de orçamento, falha de PDP e rollback de auto-modificação.
- [ ] A **escala horizontal** com degradação graciosa (shed → defer → degradar → rejeitar) está demonstrada sob carga, sem coordenação intra-processo.
- [ ] O **hipercare** encerra com SLOs cumpridos em janela definida, *runbooks* validados em incidente real ou simulado, e transição formal para operação em regime.

---

## 3. Tabela Resumo de Tickets

| ID | Título | Tipo | Estimativa | Prioridade | Dependências |
|---|---|---|---|---|---|
| AOS-098 | Provisionamento IaC do plano de controlo/dados | feature | L | P0 | EPIC-01 |
| AOS-099 | Workers stateless + estado particionado | feature | L | P0 | AOS-098, EPIC-01, EPIC-03 |
| AOS-100 | Replicação do Event Store [ADR-007] | feature | L | P0 | AOS-098, EPIC-01 |
| AOS-101 | Backup + PITR do Event Store | feature | M | P0 | AOS-100 |
| AOS-102 | DR: recuperação por replay (RPO/RTO definidos) | feature | L | P0 | AOS-100, AOS-101, EPIC-08 |
| AOS-103 | Pool de microVMs em produção | feature | M | P0 | AOS-098, EPIC-07 |
| AOS-104 | Dashboards operacionais | feature | M | P1 | EPIC-08 |
| AOS-105 | Alertas operacionais | feature | M | P1 | AOS-104, EPIC-08 |
| AOS-106 | Runbooks operacionais | chore | M | P1 | AOS-104, AOS-105 |
| AOS-107 | Escala horizontal + degradação graciosa em produção | feature | L | P1 | AOS-099, EPIC-03 |
| AOS-108 | Hipercare e operacionalização | chore | M | P2 | AOS-102, AOS-105, AOS-106, AOS-107 |

---

## AOS-098 — Provisionamento IaC do plano de controlo/dados

| Campo | Valor |
|---|---|
| Epic | EPIC-10 — Topologia, Operação e DR |
| Fase | 0 — Fundações |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | EPIC-01 (fundações do plano de controlo) |
| Bloqueia | AOS-099, AOS-100, AOS-103 |
| Responsável sugerido | DevOps/SRE |
| Documentos de referência | `tecnica/10_Topologia_Implantacao_Operacao.md` §3–4, `specs/EPIC-03`, ADR-007, ADR-004 |

**Contexto.** A topologia de referência separa fisicamente o plano de controlo (Orquestrador, Escalonador, admission control, PDP) do plano de dados (workers *stateless*, pool de microVMs, Event Store replicado, audit WORM). Para que essa separação seja reproduzível e auditável — e para que o DR possa recriar o ambiente de forma idêntica — o provisionamento tem de ser **Infraestrutura-como-Código**, sem passos manuais que escapem ao controlo de versões.

**Objectivo.** Entregar módulos IaC versionados que provisionam, de forma idempotente e reproduzível, o plano de controlo e o plano de dados, mantendo os invariantes de soberania e de rede default-deny em qualquer um dos três modelos de implantação (self-hosted, on-prem, nuvem).

**Critérios de Aceitação**
- [ ] Todo o plano de controlo e de dados é criado por IaC versionada; um ambiente limpo levanta-se de zero **sem nenhum passo manual**.
- [ ] O provisionamento é **idempotente**: reaplicar o IaC sobre um ambiente existente não produz alterações não intencionais (*plan* mostra *no-op*).
- [ ] A rede nasce **default-deny** com egress allowlist no substrato (ADR-004); nenhuma regra permissiva por omissão.
- [ ] A separação plano de controlo/plano de dados é topologicamente explícita e cada plano escala de forma independente.
- [ ] O IaC parametriza os três modelos de implantação e **impede** por configuração que réplicas ou backups cruzem a fronteira regional (ADR-011).
- [ ] O estado do IaC é remoto, cifrado e com *locking*; nenhum segredo em texto claro no código ou no estado.

**Detalhes Técnicos.** Módulos IaC (ex.: Terraform/OpenTofu ou Pulumi) organizados por plano; orquestração de containers (Kubernetes ou equivalente) para os workers; provisionamento do substrato de Event Store (Postgres/NATS replicado ou log gerido) e do pool de microVMs. Componentes-alvo: ORQ, SCH, PDP, workers, ES, SBX. Convenções de código e versionamento conforme `specs/01_Engineering_Standards_e_Handoff.md` §5.

**Testes Requeridos.** *Plan/apply* em ambiente efémero com verificação de idempotência (segundo *apply* = *no-op*); teste de política de rede (egress default-deny efectivo); teste de conformidade de soberania (falha se um recurso é colocado fora da região permitida); *scan* de segredos ao código e ao estado.

**Definition of Done**
- [ ] Critérios de Aceitação satisfeitos e demonstráveis num ambiente efémero.
- [ ] Código revisto (dois revisores — artefacto P0 de infraestrutura).
- [ ] Testes de idempotência, de rede e de soberania verdes no CI.
- [ ] Sem segredos no código ou no estado; *scan* limpo (ADR-006).
- [ ] Documentação de operação e diagrama de topologia actualizados em `tecnica/10`.
- [ ] Sem TODOs órfãos nem recursos manuais fora do IaC.

**Handoff para Claude Code**
```text
És o executor do ticket AOS-098 do Agentic OS de Referência (AOS).
Lê AOS-098 na íntegra em specs/EPIC-10_Topologia_Operacao_DR.md e tecnica/10_Topologia_Implantacao_Operacao.md §3–4.
Objectivo: IaC versionada que provisiona plano de controlo e plano de dados, idempotente e reproduzível.
Fundações a respeitar: rede default-deny + egress allowlist (ADR-004); planos separados e escaláveis de forma independente; failover/backup nunca cruzam a fronteira regional (ADR-011).
Estado do IaC remoto, cifrado, com locking; zero segredos no código/estado (ADR-006).
Testes: plan/apply em ambiente efémero (2.º apply = no-op), política de rede, conformidade de soberania, scan de segredos.
Não expandas escopo. Abre PR com o template da secção 7 do 01_Engineering_Standards, checklist de domínio preenchido e evidências.
```

---

## AOS-099 — Workers stateless + estado particionado

| Campo | Valor |
|---|---|
| Epic | EPIC-10 — Topologia, Operação e DR |
| Fase | 3 — Escala e controlo |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | AOS-098, EPIC-01 (Event Store), EPIC-03 (leases/fencing) |
| Bloqueia | AOS-107 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `tecnica/10_Topologia_Implantacao_Operacao.md` §3, `specs/EPIC-03`, ADR-007, ADR-001 |

**Contexto.** A escala horizontal só é possível se os workers forem **stateless**: todo o estado durável tem de viver no Event Store replicado e no estado **particionado por *run***, nunca no processo. Assim, qualquer worker pode morrer e ser substituído sem perda, e o número de réplicas ajusta-se à carga sem coordenação intra-processo. Estado no processo é a origem clássica de perda em *scale-in* e de *split-brain* em *failover*.

**Objectivo.** Garantir que os workers do plano de dados não retêm estado durável no processo, que o estado é particionado por *run* com *sharding* natural, e que a morte ou substituição de um worker é recuperável *resume-from-step* a partir do log.

**Critérios de Aceitação**
- [ ] Um worker pode ser terminado a meio de um *run* e substituído por outro; a execução retoma *resume-from-step* sem perda e **sem efeitos duplicados** (idempotency key = f(run_id, step_id), ADR-001).
- [ ] Nenhum estado durável reside no processo do worker; auditoria de código confirma que o estado vive no Event Store ou no estado particionado.
- [ ] O estado é **particionado por *run***; novas réplicas assumem partições sem *rebalancing* disruptivo.
- [ ] A posse de partição usa **lease/fencing token** (nunca PID) para evitar dupla execução cross-host (ADR-001; cruza com `specs/EPIC-03`).
- [ ] *Scale-in* de N para N-1 réplicas não perde trabalho em curso nem duplica efeitos.

**Detalhes Técnicos.** Workers do plano de dados; particionamento por `run_id`; posse de partição via lease/heartbeat com TTL e fencing token monotónico; leitura/escrita de eventos no Event Store replicado. Componentes-alvo: RT, SCH, ES. Reutiliza a máquina de estados e as leases definidas em `specs/EPIC-03`.

**Testes Requeridos.** Teste de *kill* de worker a meio de *run* com verificação de retoma e de zero efeitos duplicados (idempotência); teste de reatribuição de partição com fencing token obsoleto invalidado; teste de *scale-in*/*scale-out* sob carga; verificação estática de ausência de estado durável no processo.

**Definition of Done**
- [ ] Idempotência por passo verificada (reexecução prova 0 efeitos duplicados) (ADR-001).
- [ ] Replay determinístico testado (*resume-from-step*, hashes coincidem) (ADR-010).
- [ ] Toda a tool call continua mediada pelo Reference Monitor (ADR-002).
- [ ] Spans OTel GenAI com custo por span emitidos pelo worker (ADR-010).
- [ ] Sem segredos; testes verdes; cobertura não regride.
- [ ] Revisão por dois revisores (P0); documentação actualizada.

**Handoff para Claude Code**
```text
És o executor do ticket AOS-099 do AOS.
Lê AOS-099 em specs/EPIC-10 e tecnica/10 §3; consulta specs/EPIC-03 para leases/fencing e máquina de estados.
Objectivo: workers stateless com estado particionado por run; morte/substituição de worker recuperável resume-from-step sem efeitos duplicados.
Fundações: idempotency key = f(run_id, step_id) (ADR-001); posse de partição via lease/fencing token, nunca PID; estado durável só no Event Store/estado particionado (ADR-007).
Testes: kill de worker a meio de run, reatribuição com fencing obsoleto invalidado, scale-in/scale-out, ausência de estado no processo.
Emite spans OTel + custo. Não expandas escopo. Abre PR com o template e evidências (traces).
```

---

## AOS-100 — Replicação do Event Store [ADR-007]

| Campo | Valor |
|---|---|
| Epic | EPIC-10 — Topologia, Operação e DR |
| Fase | 0 — Fundações |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | AOS-098, EPIC-01 (Event Store base) |
| Bloqueia | AOS-101, AOS-102 |
| Responsável sugerido | Engenheiro de Dados/Memória |
| Documentos de referência | `tecnica/10_Topologia_Implantacao_Operacao.md` §3, §6, `tecnica/04_Memoria_Persistencia.md`, ADR-007 |

**Contexto.** O plano-base usava SQLite single-writer — um SPOF e um tecto de throughput. O ADR-007 substitui-o por um **Event Store replicado** append-only com transporte push. A replicação por quorum é a base tanto da disponibilidade do plano de controlo (99,9%) como do backup e do replay como recuperação. Sem replicação, o DR não tem de onde restaurar e a fonte de verdade é frágil.

**Objectivo.** Entregar o Event Store replicado por quorum, append-only, com transporte push, eliminando o single-writer e suportando escritas de múltiplos workers e leituras de replay em paralelo, sem cruzar a fronteira de soberania.

**Critérios de Aceitação**
- [ ] O Event Store escreve de forma **append-only** e replica por **quorum**; a perda de uma réplica não perde dados nem interrompe escritas (dentro do quorum).
- [ ] O transporte é **push** (event-driven), não *polling*; os workers recebem eventos sem *busy-wait*.
- [ ] Múltiplos workers escrevem eventos e leem para replay **em paralelo**, sem contenção de single-writer.
- [ ] Não existe nenhum ponto único de escrita (SPOF eliminado); teste de falha de nó demonstra continuidade.
- [ ] As réplicas **nunca** cruzam a fronteira regional de soberania (ADR-011).
- [ ] A ordenação append-only e a integridade do log são preserváveis e verificáveis (base do audit hash-chain, ADR-010).

**Detalhes Técnicos.** Event Store replicado (Postgres com replicação síncrona por quorum, NATS JetStream ou Kafka, conforme modelo de implantação); transporte push; particionamento coerente com AOS-099. Componentes-alvo: ES. Detalhe do modelo de persistência em `tecnica/04_Memoria_Persistencia.md`.

**Testes Requeridos.** Teste de falha de nó (kill de uma réplica) com verificação de continuidade e zero perda dentro do quorum; teste de escrita concorrente multi-worker; teste de ordenação/integridade append-only; teste de conformidade de soberania (réplica fora da região é rejeitada); *benchmark* básico de throughput contra o baseline single-writer.

**Definition of Done**
- [ ] Critérios de Aceitação satisfeitos e demonstráveis.
- [ ] Replicação por quorum e transporte push testados sob falha de nó.
- [ ] Integridade append-only verificada (base do audit WORM, ADR-010).
- [ ] Sem segredos; testes verdes; cobertura não regride.
- [ ] Revisão por dois revisores (P0); ADR-007 referenciado; documentação e `tecnica/10` §3/§6 actualizadas.

**Handoff para Claude Code**
```text
És o executor do ticket AOS-100 do AOS.
Lê AOS-100 em specs/EPIC-10, tecnica/10 §3 e §6, tecnica/04 e o ADR-007.
Objectivo: Event Store replicado por quorum, append-only, transporte push; elimina single-writer/SPOF; suporta escrita e replay multi-worker em paralelo.
Fundações: fonte de verdade append-only (ADR-007); réplicas nunca cruzam fronteira regional (ADR-011); ordenação/integridade verificáveis (base do audit hash-chain, ADR-010).
Testes: falha de nó com continuidade e zero perda no quorum, escrita concorrente, integridade append-only, conformidade de soberania.
Não expandas escopo. Abre PR com o template e evidências.
```

---

## AOS-101 — Backup + PITR do Event Store

| Campo | Valor |
|---|---|
| Epic | EPIC-10 — Topologia, Operação e DR |
| Fase | 3 — Escala e controlo |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-100 |
| Bloqueia | AOS-102 |
| Responsável sugerido | DevOps/SRE |
| Documentos de referência | `tecnica/10_Topologia_Implantacao_Operacao.md` §6, ADR-007, ADR-010, ADR-011 |

**Contexto.** A replicação por quorum protege contra a falha de um nó, mas não contra corrupção lógica, apagamento acidental ou desastre regional. É preciso um **backup imutável** do log e a capacidade de **Point-In-Time Recovery** — restaurar o Event Store até ao último evento íntegro antes de um incidente. Sem PITR, o DR não consegue escolher um ponto de restauro coerente.

**Objectivo.** Entregar backup imutável e contínuo do Event Store com PITR validado, verificação periódica da hash-chain do audit WORM, e conformidade de soberania nas cópias.

**Critérios de Aceitação**
- [ ] O log é exportado para **backup imutável** de forma contínua, adicional à replicação por quorum.
- [ ] É possível fazer **PITR** até um instante arbitrário dentro da janela de retenção, restaurando até ao último evento íntegro.
- [ ] Um restauro de teste reconstrói um Event Store consistente e é **verificado por hash-chain** do audit WORM (ADR-010).
- [ ] A janela de retenção e a periodicidade satisfazem o **RPO ≤ 1 min** dentro de região (cruza com AOS-102).
- [ ] Backups e cópias **nunca** cruzam a fronteira regional de soberania (ADR-011).
- [ ] O restauro é **testado periodicamente** (não apenas configurado); existe evidência do último restauro bem-sucedido.

**Detalhes Técnicos.** Exportação incremental/contínua do log para armazenamento imutável (object storage com *object lock* / WORM); mecanismo de PITR do substrato escolhido; verificação de integridade via hash-chain. Componentes-alvo: ES, OBS (audit WORM). Cifra em repouso via KMS; sem segredos no código.

**Testes Requeridos.** Teste de restauro PITR para um instante-alvo com verificação de consistência; teste de verificação da hash-chain do audit; teste de imutabilidade do backup (tentativa de sobrescrita falha); teste de conformidade de soberania da cópia; medição da janela efectiva de RPO.

**Definition of Done**
- [ ] PITR demonstrado com restauro de teste e verificação de hash-chain (ADR-010).
- [ ] Backup imutável e conforme à soberania (ADR-011).
- [ ] Sem segredos; cifra em repouso via KMS/Vault (ADR-006); testes verdes.
- [ ] Revisão por dois revisores (P0); runbook de restauro esboçado (liga a AOS-106).
- [ ] Documentação e `tecnica/10` §6 actualizadas.

**Handoff para Claude Code**
```text
És o executor do ticket AOS-101 do AOS.
Lê AOS-101 em specs/EPIC-10 e tecnica/10 §6; consulta ADR-007, ADR-010, ADR-011.
Objectivo: backup imutável contínuo do Event Store + PITR validado por restauro de teste.
Fundações: verificação por hash-chain do audit WORM (ADR-010); backup nunca cruza fronteira regional (ADR-011); cifra em repouso, zero segredos (ADR-006); RPO <= 1 min dentro de região.
Testes: restauro PITR para instante-alvo, verificação de hash-chain, imutabilidade do backup, soberania da cópia.
Não expandas escopo. Abre PR com o template e evidências do restauro.
```

---

## AOS-102 — DR: recuperação por replay (RPO/RTO definidos)

| Campo | Valor |
|---|---|
| Epic | EPIC-10 — Topologia, Operação e DR |
| Fase | 3 — Escala e controlo |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | L |
| Dependências | AOS-100, AOS-101, EPIC-08 (replay e captura) |
| Bloqueia | AOS-108 |
| Responsável sugerido | DevOps/SRE |
| Documentos de referência | `tecnica/10_Topologia_Implantacao_Operacao.md` §6, `specs/EPIC-08`, ADR-001, ADR-007, ADR-010 |

**Contexto.** O DR do AOS distingue-se de um sistema convencional: como o Event Store é a fonte de verdade append-only e a execução é durável ao nível do passo (ADR-001), a recuperação primária é o **replay determinístico** a partir do log, não a restauração de estado mutável. Restaura-se o log; o estado reconstrói-se *resume-from-step*. A fidelidade exige que todos os inputs não-determinísticos tenham sido capturados por trajectória (model-id, params, seed, hash do prompt).

**Objectivo.** Entregar o plano de DR por replay com RPO/RTO definidos e validados por *game day*: restauro do log até ao último evento íntegro e retoma por replay determinístico, com efeitos externos não duplicados na retoma.

**Critérios de Aceitação**
- [ ] O procedimento de DR restaura o log (via AOS-101) e retoma a execução por **replay determinístico *resume-from-step*** a partir do último passo durado (ADR-001).
- [ ] **RPO ≤ 1 min** e **RTO ≤ 30 min** *(proposta)* são medidos e cumpridos num *game day* documentado.
- [ ] O replay reutiliza os inputs não-determinísticos capturados (model-id, params, seed, hash do prompt materializado); **100% dos passos** da amostra são reproduzíveis (ADR-010).
- [ ] Efeitos externos **não são duplicados** na retoma (idempotency key = f(run_id, step_id), ADR-001).
- [ ] A integridade do audit WORM é verificada (hash-chain) antes de dar o serviço por restabelecido (ADR-010).
- [ ] O failover de DR **não cruza** a fronteira de soberania (ADR-011).
- [ ] Existe um *game day* periódico agendado que revalida RPO/RTO e a fidelidade do replay.

**Detalhes Técnicos.** Runbook e automação de DR; integração com o backup/PITR (AOS-101) e com a captura de inputs não-determinísticos (`specs/EPIC-08`); manifesto de dependências por trajectória. Componentes-alvo: ES, RT, OBS. A mecânica de replay e captura está em `specs/EPIC-08` e `tecnica/08_Observabilidade_Evals.md`.

**Testes Requeridos.** *Game day* de DR end-to-end com medição de RPO e RTO; teste de fidelidade de replay (amostra 100% reproduzível); teste de não-duplicação de efeitos na retoma (idempotência); verificação de hash-chain pós-restauro; teste de que o failover respeita a fronteira regional.

**Definition of Done**
- [ ] RPO/RTO medidos e cumpridos num *game day* documentado.
- [ ] Replay determinístico testado (100% dos passos, hashes coincidem) (ADR-010).
- [ ] Idempotência por passo verificada na retoma (0 efeitos duplicados) (ADR-001).
- [ ] Audit WORM verificado; failover conforme à soberania (ADR-011).
- [ ] Revisão por dois revisores (P0); runbook de DR ligado a AOS-106; `tecnica/10` §6 actualizada.

**Handoff para Claude Code**
```text
És o executor do ticket AOS-102 do AOS.
Lê AOS-102 em specs/EPIC-10 e tecnica/10 §6; consulta specs/EPIC-08 (replay/captura) e ADR-001, ADR-007, ADR-010.
Objectivo: DR por replay determinístico — restaurar log e retomar resume-from-step; RPO <= 1 min, RTO <= 30 min validados por game day.
Fundações: 100% dos passos reproduzíveis via inputs não-determinísticos capturados (ADR-010); zero efeitos duplicados na retoma (ADR-001); audit WORM verificado; failover não cruza fronteira (ADR-011).
Testes: game day end-to-end com medição de RPO/RTO, fidelidade de replay, não-duplicação, hash-chain pós-restauro.
Não expandas escopo. Abre PR com o template e evidências do game day.
```

---

## AOS-103 — Pool de microVMs em produção

| Campo | Valor |
|---|---|
| Epic | EPIC-10 — Topologia, Operação e DR |
| Fase | 3 — Escala e controlo |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-098, EPIC-07 (microVM/sandbox) |
| Bloqueia | AOS-107 |
| Responsável sugerido | Engenheiro de Segurança |
| Documentos de referência | `tecnica/10_Topologia_Implantacao_Operacao.md` §3, §5, `specs/EPIC-07`, ADR-004 |

**Contexto.** O isolamento ao nível do kernel (microVM Firecracker/Kata ou gVisor, ADR-004) é a fronteira de segurança primária. Em produção, o cold-start de uma microVM tem de ficar **< 125 ms** (restore 5–30 ms) para não penalizar a latência; isso consegue-se com um **pool pré-aquecido** de snapshots, dimensionado ao headroom. O pool também absorve picos de carga sem pagar cold-start (cruza com a escala, AOS-107).

**Objectivo.** Operacionalizar em produção o pool de microVMs pré-aquecidas com snapshot/restore, dimensionado dinamicamente ao headroom, mantendo os invariantes de isolamento (FS read-only + overlay efémero, seccomp, egress default-deny).

**Critérios de Aceitação**
- [ ] O pool serve execuções a partir de **snapshots pré-aquecidos**; cold-start medido **< 125 ms** e restore **5–30 ms** em produção (ADR-004).
- [ ] O tamanho do pool é derivado do **headroom** (não uma constante) e ajusta-se à carga.
- [ ] Cada microVM aplica **FS read-only + overlay efémero**, seccomp mínimo e **rede default-deny** com egress allowlist (ADR-004; cruza com `specs/EPIC-07`).
- [ ] Não há socket do host exposto à microVM; o isolamento é a fronteira primária, jails só como defesa secundária.
- [ ] Sob pico de carga, o pool absorve o aumento **sem cold-start** visível até ao limite do headroom; acima disso, aciona-se a degradação (AOS-107).
- [ ] Métricas do pool (ocupação, cold-start p95, taxa de reciclagem) são emitidas como SLIs (ADR-010).

**Detalhes Técnicos.** Gestor de pool de microVMs com snapshot/restore; reciclagem de instâncias efémeras; integração com o Escalonador para *push* de trabalho. Componentes-alvo: SBX, SCH, OBS. O desenho de isolamento e egress está em `specs/EPIC-07` e `tecnica/07_Seguranca_Isolamento.md`.

**Testes Requeridos.** Teste de cold-start/restore sob carga (p95 < 125 ms); teste de dimensionamento do pool face a variação de headroom; teste de isolamento (FS read-only, egress default-deny, ausência de socket do host); teste de absorção de pico sem cold-start; verificação de emissão de SLIs do pool.

**Definition of Done**
- [ ] Cold-start < 125 ms e restore 5–30 ms demonstrados em produção (ADR-004).
- [ ] Invariantes de isolamento verificados (FS read-only, seccomp, egress default-deny).
- [ ] SLIs do pool emitidos em OTel (ADR-010); sem segredos.
- [ ] Revisão por dois revisores (P0 de segurança); testes verdes.
- [ ] Documentação e `tecnica/10` §3/§5 actualizadas.

**Handoff para Claude Code**
```text
És o executor do ticket AOS-103 do AOS.
Lê AOS-103 em specs/EPIC-10 e tecnica/10 §3/§5; consulta specs/EPIC-07 e o ADR-004.
Objectivo: pool de microVMs pré-aquecidas em produção, cold-start < 125 ms (restore 5–30 ms), dimensionado ao headroom.
Fundações: FS read-only + overlay efémero, seccomp mínimo, rede default-deny + egress allowlist, sem socket do host (ADR-004); isolamento é a fronteira primária.
Testes: cold-start/restore sob carga, dimensionamento por headroom, isolamento, absorção de pico sem cold-start, SLIs do pool.
Não expandas escopo. Abre PR com o template e evidências (métricas de cold-start).
```

---

## AOS-104 — Dashboards operacionais

| Campo | Valor |
|---|---|
| Epic | EPIC-10 — Topologia, Operação e DR |
| Fase | 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | EPIC-08 (spans OTel, SLIs, audit) |
| Bloqueia | AOS-105, AOS-106 |
| Responsável sugerido | Engenheiro de Observabilidade |
| Documentos de referência | `tecnica/10_Topologia_Implantacao_Operacao.md` §7, `specs/EPIC-08`, ADR-010 |

**Contexto.** A observação operacional deriva directamente da camada de observabilidade (ADR-010): os mesmos spans OTel GenAI e métricas que suportam debug e eval alimentam os dashboards. O padrão é *wide events* — capturar tudo, filtrar no query-time — para que os dashboards se construam sobre SLIs e não sobre filtragem no emit-time que esconde padrões sistémicos.

**Objectivo.** Entregar dashboards operacionais organizados por plano (controlo vs dados) e por *run*, com *drill-down* da árvore de spans até à tool call individual e custo em USD por span, cobrindo todos os SLIs canónicos.

**Critérios de Aceitação**
- [ ] Existem dashboards por **plano de controlo** e **plano de dados**, e uma vista **por *run*** com *drill-down* até à tool call individual.
- [ ] Todos os SLIs canónicos são visualizados: disponibilidade do plano de controlo, overhead de mediação p95, cold-start de sandbox, cache-hit-rate, headroom de tokens/$, fidelidade de replay, integridade do audit WORM.
- [ ] O **custo em USD por span** é visível e agregável por *run* e por tenant (ADR-010).
- [ ] Os dashboards assentam em *wide events* (query-time), não em filtragem no emit-time.
- [ ] Cada painel de SLI indica o respectivo **SLO** e a janela de avaliação.
- [ ] Os dashboards são versionados como código (dashboard-as-code), não configurados manualmente.

**Detalhes Técnicos.** Colector OTel GenAI semconv; dashboards-as-code (ex.: JSON/Grafana versionado); consultas sobre métricas e spans (`gen_ai.usage.*`, custo por span). Componentes-alvo: OBS. Fonte dos spans e SLIs em `specs/EPIC-08` e `tecnica/08_Observabilidade_Evals.md`.

**Testes Requeridos.** Teste de renderização dos dashboards contra dados sintéticos; verificação de que cada SLI canónico tem painel e SLO; teste de *drill-down* de *run* → span → tool call; verificação de agregação de custo por *run*/tenant; validação de que os dashboards são reproduzíveis a partir do código.

**Definition of Done**
- [ ] Todos os SLIs canónicos visualizados com SLO e janela (ADR-010).
- [ ] Custo em USD por span visível e agregável.
- [ ] Dashboards versionados como código; reproduzíveis.
- [ ] Sem segredos; testes verdes; revisão por um revisor.
- [ ] Documentação e `tecnica/10` §7 actualizadas.

**Handoff para Claude Code**
```text
És o executor do ticket AOS-104 do AOS.
Lê AOS-104 em specs/EPIC-10 e tecnica/10 §7; consulta specs/EPIC-08 e o ADR-010.
Objectivo: dashboards operacionais por plano (controlo/dados) e por run, com drill-down span→tool call e custo USD por span; cobrir todos os SLIs canónicos.
Fundações: wide events (filtrar no query-time), não emit-time; OTel GenAI semconv; dashboards-as-code versionados.
Testes: renderização com dados sintéticos, cobertura de cada SLI com SLO, drill-down, agregação de custo, reprodutibilidade.
Não expandas escopo. Abre PR com o template e evidências (capturas/JSON dos dashboards).
```

---

## AOS-105 — Alertas operacionais

| Campo | Valor |
|---|---|
| Epic | EPIC-10 — Topologia, Operação e DR |
| Fase | 2 — Governação e observabilidade |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-104, EPIC-08 |
| Bloqueia | AOS-106, AOS-108 |
| Responsável sugerido | Engenheiro de Observabilidade |
| Documentos de referência | `tecnica/10_Topologia_Implantacao_Operacao.md` §7, `specs/EPIC-08`, ADR-008, ADR-010 |

**Contexto.** Os alertas constroem-se sobre SLIs e não sobre filtragem no emit-time. A regra de ouro é: **cada alerta liga a um runbook accionável**; alertas sem runbook são ruído e removem-se. Os limiares derivam dos SLOs canónicos (disponibilidade 99,9%, mediação p95 < 15 ms, cold-start < 125 ms, cache-hit-rate > 80%, headroom > 0 reservável, fidelidade de replay 100%, integridade do audit).

**Objectivo.** Entregar as regras de alerta sobre os SLIs canónicos, com limiares derivados dos SLOs, cada uma ligada ao runbook correspondente e sem alertas órfãos.

**Critérios de Aceitação**
- [ ] Existe uma regra de alerta para cada SLI canónico, com limiar e janela derivados do SLO (ex.: erro > 0,1% em 5 min; mediação p95 > 15 ms sustentado; cold-start p95 > 125 ms; headroom < limiar de reserva; falha de reprodução em amostra; quebra de hash-chain).
- [ ] **Cada alerta liga a um runbook accionável** (RB-01…RB-05 ou procedimento de escala/DR); não existe nenhum alerta sem runbook.
- [ ] Os alertas disparam sobre SLIs (query-time), não sobre filtragem no emit-time.
- [ ] Os alertas de headroom e orçamento ligam-se ao admission control (ADR-008) e distinguem colapso de rate limit (RB-01) de esgotamento de orçamento (RB-03).
- [ ] As regras são versionadas como código (alerting-as-code) e testadas contra cenários sintéticos que provocam o *trip*.
- [ ] Existe controlo de ruído: agrupamento e supressão de alertas correlacionados, sem esconder padrões sistémicos.

**Detalhes Técnicos.** Regras de alerta como código sobre o backend de métricas; roteamento para canais de operação; ligação alerta↔runbook. Componentes-alvo: OBS. Depende dos dashboards e SLIs de AOS-104 e das definições de `specs/EPIC-08`.

**Testes Requeridos.** Teste de disparo por cenário sintético para cada SLI (o alerta *trips* quando o SLI cruza o limiar e recupera quando volta); verificação de que cada alerta tem runbook ligado (falha o CI se algum não tiver); teste de agrupamento/supressão; teste de que nenhum alerta assenta em filtragem no emit-time.

**Definition of Done**
- [ ] Cada SLI canónico tem alerta com limiar/janela derivados do SLO.
- [ ] Cada alerta liga a runbook accionável (verificado no CI).
- [ ] Regras versionadas como código e testadas por cenário sintético.
- [ ] Sem segredos; testes verdes; revisão por um revisor.
- [ ] Documentação e `tecnica/10` §7 actualizadas.

**Handoff para Claude Code**
```text
És o executor do ticket AOS-105 do AOS.
Lê AOS-105 em specs/EPIC-10 e tecnica/10 §7; consulta specs/EPIC-08, ADR-008, ADR-010.
Objectivo: regras de alerta sobre os SLIs canónicos, limiares derivados dos SLOs, cada alerta ligado a um runbook accionável.
Fundações: alertas sobre SLIs (query-time), não emit-time; nenhum alerta sem runbook; distinguir colapso de rate limit (RB-01) de esgotamento de orçamento (RB-03) via admission control (ADR-008).
Testes: disparo por cenário sintético para cada SLI, verificação de ligação a runbook no CI, agrupamento/supressão.
Não expandas escopo. Abre PR com o template e evidências (regras + testes de disparo).
```

---

## AOS-106 — Runbooks operacionais

| Campo | Valor |
|---|---|
| Epic | EPIC-10 — Topologia, Operação e DR |
| Fase | 3 — Escala e controlo |
| Tipo | chore |
| Prioridade | P1 |
| Estimativa | M |
| Dependências | AOS-104, AOS-105 |
| Bloqueia | AOS-108 |
| Responsável sugerido | DevOps/SRE |
| Documentos de referência | `tecnica/10_Topologia_Implantacao_Operacao.md` §8, `specs/EPIC-03`, ADR-008, ADR-001, ADR-011, ADR-012 |

**Contexto.** Cada modo de falha canónico precisa de um procedimento documentado com estrutura **sinal → diagnóstico → mitigação**. `tecnica/10` §8 define cinco runbooks: RB-01 colapso de rate limit, RB-02 zumbi cross-host, RB-03 esgotamento de orçamento, RB-04 falha de PDP e RB-05 rollback de auto-modificação. Todos assumem acesso aos dashboards (AOS-104) e à trajectória OTel, e ligam-se aos alertas (AOS-105).

**Objectivo.** Escrever, validar e ligar aos alertas os cinco runbooks canónicos, garantindo que cada um foi exercitado em *game day* e produz recuperação verificável.

**Critérios de Aceitação**
- [ ] **RB-01 — Colapso de rate limit:** sinal (headroom perto de zero, boards a saturar colectivamente), diagnóstico (token-bucket distribuído, taxa de *admit* recusado), mitigação (admission control já a recusar spawns sem headroom; escada de degradação; `max_spawn` derivado do headroom) (ADR-008).
- [ ] **RB-02 — Zumbi cross-host:** sinal (worker `running` sem progresso ou execução duplicada), diagnóstico (**nunca PID**; lease/heartbeat + fencing token; distinguir de `waiting_on_human`), mitigação (deixar lease expirar, reatribuir com novo fencing token; escritas do obsoleto invalidadas) (ADR-001; `specs/EPIC-03`).
- [ ] **RB-03 — Esgotamento de orçamento:** sinal (árvore perto do orçamento tokens/$), diagnóstico (burn-down por *run*, custo por span), mitigação (exaustão graciosa a ~80%; circuit breaker fail-closed sem efeitos parciais não compensados; orçamento em tokens/$, não iterações) (ADR-008).
- [ ] **RB-04 — Falha de PDP:** sinal (latência de decisão a subir ou PDP indisponível), diagnóstico (saúde do PDP, versão de política), mitigação (Reference Monitor **fail-closed**; réplica de PDP com política assinada; reversão para versão anterior assinada) — a indisponibilidade do PDP degrada disponibilidade, nunca segurança (ADR-011).
- [ ] **RB-05 — Rollback de auto-modificação:** sinal (regressão após promoção de skill/prompt/schema — misevolution/drift), diagnóstico (trace-diffing de success-rate/unsafe-action rate vs baseline, versão SemVer), mitigação (**rollback atómico**; reencaminhar para staging → eval-gate → canary; registar no audit WORM) (ADR-012).
- [ ] Cada runbook está **ligado ao alerta** correspondente (AOS-105) e foi **exercitado num *game day*** com recuperação verificada.

**Detalhes Técnicos.** Runbooks versionados junto do código de operação, com estrutura sinal/diagnóstico/mitigação; ligação bidireccional alerta↔runbook. Componentes-alvo: SCH, PDP, RM, OBS, GOV. A máquina de estados e as leases estão em `specs/EPIC-03`; a auto-modificação em `specs/EPIC-11`/ADR-012.

**Testes Requeridos.** *Game day* por runbook, injectando o modo de falha (rate limit saturado, worker zumbi, orçamento esgotado, PDP indisponível, regressão de skill) e verificando que o procedimento recupera; verificação automática de que cada runbook está ligado a um alerta; revisão de conformidade com os ADRs citados.

**Definition of Done**
- [ ] Os cinco runbooks escritos com estrutura sinal/diagnóstico/mitigação.
- [ ] Cada runbook exercitado em *game day* com recuperação verificada.
- [ ] Ligação alerta↔runbook completa (nenhum alerta órfão).
- [ ] Conformidade com ADR-008/001/011/012 revista.
- [ ] Revisão por dois revisores (toca em segurança/governação); `tecnica/10` §8 alinhada.

**Handoff para Claude Code**
```text
És o executor do ticket AOS-106 do AOS.
Lê AOS-106 em specs/EPIC-10 e tecnica/10 §8; consulta specs/EPIC-03 e os ADR-008, ADR-001, ADR-011, ADR-012.
Objectivo: escrever e validar os cinco runbooks canónicos (RB-01 rate limit, RB-02 zumbi cross-host, RB-03 orçamento, RB-04 falha de PDP, RB-05 rollback de auto-modificação), estrutura sinal/diagnóstico/mitigação, ligados aos alertas.
Fundações: RB-02 nunca usa PID (lease/fencing, ADR-001); RB-04 Reference Monitor fail-closed, PDP degrada disponibilidade e nunca segurança (ADR-011); RB-05 rollback atómico + repasse por eval-gate (ADR-012).
Testes: game day por runbook injectando o modo de falha; verificação de ligação alerta↔runbook.
Não expandas escopo. Abre PR com o template e evidências dos game days.
```

---

## AOS-107 — Escala horizontal + degradação graciosa em produção

| Campo | Valor |
|---|---|
| Epic | EPIC-10 — Topologia, Operação e DR |
| Fase | 3 — Escala e controlo |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-099, AOS-103, EPIC-03 (admission control, backpressure) |
| Bloqueia | AOS-108 |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `tecnica/10_Topologia_Implantacao_Operacao.md` §5, `specs/EPIC-03`, ADR-008 |

**Contexto.** A escala horizontal assenta em três propriedades: workers *stateless* (AOS-099), estado particionado por *run* e **admission control global** (o crescimento do plano de dados nunca ultrapassa o headroom real do provider, ADR-008). O Escalonador faz *push* event-driven apenas quando há débito reservado no token-bucket distribuído; `max_spawn` é derivado dinamicamente do headroom, não uma constante. Quando o headroom se esgota, entra a **escada de degradação graciosa**: shed → defer → degradar → rejeitar.

**Objectivo.** Demonstrar em produção a escala horizontal automática dirigida por SLIs e a degradação graciosa em escada, substituindo a acumulação ilimitada de fila e a cascata de timeouts por uma resposta previsível e observável.

**Critérios de Aceitação**
- [ ] Quando a profundidade de fila e a latência p95 sobem **e há headroom**, o sistema adiciona réplicas de worker e amplia o pool de microVMs (AOS-103), absorvendo a carga **sem cold-start** visível.
- [ ] Novas réplicas assumem partições **sem *rebalancing* disruptivo** (estado particionado, AOS-099).
- [ ] `max_spawn` é **derivado dinamicamente do headroom** do token-bucket distribuído, não uma constante (ADR-008).
- [ ] Quando o headroom se esgota, aciona-se a **escada de degradação graciosa** com política declarativa: **shed → defer → degradar → rejeitar** (fail-closed com sinal ao utilizador).
- [ ] A degradação é **observável**: cada nível emite métricas/spans e liga-se ao alerta de headroom (RB-01/RB-03).
- [ ] Sob carga que excede o headroom, o sistema **não acumula fila ilimitada** nem entra em cascata de timeouts.

**Detalhes Técnicos.** Autoscaling dirigido por SLIs (profundidade de fila, latência p95, headroom); integração com o admission control global e o backpressure de `specs/EPIC-03`; política declarativa de degradação. Componentes-alvo: SCH, ES, SBX, OBS. A mecânica de backpressure, prioridade e *aging* está em `specs/EPIC-03` e `tecnica/03_Orquestracao_Escalonamento.md`.

**Testes Requeridos.** Teste de carga com *scale-out* dirigido por SLI e verificação de absorção sem cold-start; teste de que `max_spawn` acompanha o headroom; teste da escada de degradação (cada nível é accionado na ordem correcta sob esgotamento de headroom); teste de ausência de acumulação ilimitada de fila; verificação de emissão de métricas de degradação.

**Definition of Done**
- [ ] Escala horizontal dirigida por SLIs demonstrada sob carga.
- [ ] Escada de degradação (shed → defer → degradar → rejeitar) demonstrada e observável (ADR-008).
- [ ] `max_spawn` derivado do headroom, não constante.
- [ ] Spans/métricas de degradação emitidos (ADR-010); sem segredos.
- [ ] Revisão por dois revisores; `tecnica/10` §5 alinhada.

**Handoff para Claude Code**
```text
És o executor do ticket AOS-107 do AOS.
Lê AOS-107 em specs/EPIC-10 e tecnica/10 §5; consulta specs/EPIC-03 e o ADR-008.
Objectivo: escala horizontal dirigida por SLIs e degradação graciosa em escada (shed → defer → degradar → rejeitar) em produção.
Fundações: workers stateless + estado particionado (assume partições sem rebalancing disruptivo); max_spawn derivado do headroom do token-bucket distribuído, não constante (ADR-008); nunca acumular fila ilimitada.
Testes: carga com scale-out por SLI e absorção sem cold-start, max_spawn acompanha headroom, escada de degradação na ordem correcta, ausência de fila ilimitada.
Não expandas escopo. Abre PR com o template e evidências (testes de carga).
```

---

## AOS-108 — Hipercare e operacionalização

| Campo | Valor |
|---|---|
| Epic | EPIC-10 — Topologia, Operação e DR |
| Fase | 4 — UX e evolução |
| Tipo | chore |
| Prioridade | P2 |
| Estimativa | M |
| Dependências | AOS-102, AOS-105, AOS-106, AOS-107 |
| Bloqueia | — |
| Responsável sugerido | DevOps/SRE |
| Documentos de referência | `tecnica/10_Topologia_Implantacao_Operacao.md` §7–9, `specs/EPIC-08`, `specs/01_Engineering_Standards_e_Handoff.md` §9 |

**Contexto.** Depois de a topologia, o DR, os dashboards, os alertas, os runbooks e a escala estarem entregues, o sistema entra num período de **hipercare**: operação vigiada de perto, com resposta acelerada a incidentes, até que os SLOs se estabilizem e os runbooks provem eficácia em condições reais. É a ponte entre a entrega do epic e a operação em regime, e a última oportunidade de calibrar limiares de alerta e afinar procedimentos antes de reduzir a supervisão.

**Objectivo.** Conduzir e encerrar o hipercare: validar SLOs em janela definida, confirmar a eficácia dos runbooks em incidente real ou simulado, calibrar alertas com base no ruído observado, e formalizar a transição para operação em regime.

**Critérios de Aceitação**
- [ ] Existe um **plano de hipercare** com duração definida, escalões de resposta e critérios de saída explícitos.
- [ ] Os **SLOs canónicos são cumpridos** de forma sustentada na janela de hipercare (disponibilidade 99,9%, mediação p95 < 15 ms, cold-start < 125 ms, cache-hit-rate > 80%, fidelidade de replay 100%).
- [ ] Cada runbook (AOS-106) foi **validado em incidente real ou simulado** durante o hipercare, com MTTR medido.
- [ ] Os alertas (AOS-105) são **calibrados** com base no ruído observado; o *override-rate* e o *gate escape rate* são medidos (cruza com `specs/01` §9).
- [ ] O *game day* de DR (AOS-102) é **repetido** no hipercare e revalida RPO/RTO.
- [ ] O hipercare encerra com um **relatório de transição** para operação em regime, com métricas operacionais (MTTR, change failure rate, deploy freq.) e acções de acompanhamento.

**Detalhes Técnicos.** Plano de hipercare e critérios de saída; painel de métricas operacionais (`specs/01` §9: MTTR, DORA, gate escape rate); recolha de feedback de incidentes; calibração de limiares de alerta. Componentes-alvo: OBS, GOV. Sem alteração de comportamento do sistema — é operacionalização e afinação.

**Testes Requeridos.** Verificação de cumprimento dos SLOs na janela (relatório de conformidade); *game day* de DR repetido com RPO/RTO revalidados; exercício de cada runbook com MTTR registado; revisão da taxa de falsos-positivos de alerta antes/depois da calibração.

**Definition of Done**
- [ ] Plano de hipercare executado; critérios de saída cumpridos.
- [ ] SLOs canónicos cumpridos de forma sustentada; evidência anexada.
- [ ] Todos os runbooks validados com MTTR medido; alertas calibrados.
- [ ] *Game day* de DR revalidado no hipercare (RPO/RTO).
- [ ] Relatório de transição para operação em regime aprovado; revisão por um revisor.

**Handoff para Claude Code**
```text
És o executor do ticket AOS-108 do AOS.
Lê AOS-108 em specs/EPIC-10 e tecnica/10 §7–9; consulta specs/01_Engineering_Standards §9 (métricas operacionais) e specs/EPIC-08.
Objectivo: conduzir e encerrar o hipercare — validar SLOs em janela definida, validar runbooks em incidente real/simulado, calibrar alertas, formalizar transição para operação em regime.
Nota: é operacionalização e afinação, sem alterar o comportamento do sistema. Não introduzas features.
Entrega: plano de hipercare com critérios de saída, relatório de conformidade de SLOs, MTTR por runbook, game day de DR revalidado (RPO/RTO), relatório de transição.
Não expandas escopo. Abre PR com o template e evidências (relatórios, métricas DORA/MTTR).
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
