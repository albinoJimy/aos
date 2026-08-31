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
| AOS-281 | Composição ORQ/SCH↔nó sob disciplina de lease | feature | L | P1 | AOS-099, AOS-100, EPIC-03, EPIC-19 |
| ~~AOS-282~~ | ~~Tecto do orçamento por árvore partilhado entre réplicas~~ — **INVÁLIDO** (premissa falsa; mandaria violar D-A1.3) | — | — | — | — |
| AOS-283 | Eleição de líder para os laços de serviço do nó *(v1.1)* | feature | M | P0 | AOS-100, AOS-018 |
| AOS-284 | Disciplina de partição da hash-chain de auditoria sob múltiplos escritores *(v1.1)* | feature | M | P0 | AOS-100 |
| AOS-285 | Guard de arranque: o nó recusa arrancar sobre um Event Store já detido | feature | S | P0 | — |
| AOS-286 | Estender o guard de posse do WAL aos restantes escritores | feature | S | P1 | AOS-285 |

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
## AOS-281 — Composição ORQ/SCH↔nó sob disciplina de lease

| Campo | Valor |
|---|---|
| Epic | EPIC-10 — Topologia, Operação e DR |
| Fase | 5 — Operacionalização |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | L |
| Dependências | AOS-099 (workers stateless), AOS-100 (Event Store replicado), EPIC-03 (portas ORQ/SCH), EPIC-19 (planeador, gate e materialização) |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | ADR-018 (fronteira nó↔ORQ/SCH), ADR-007 (uma só fonte de verdade), ADR-015 (execução durável), `packages/kernel/agent-runtime/durable/lease.go`, `specs/EPIC-19` (AOS-237/238), `docs/governance/REGISTO-Deferimentos.md` (DEF-272, DEF-273, DEF-803) |

**Contexto.** O planeador, o gate de aprovação, a materialização e o dispatcher estão **entregues e testados** (EPIC-19, AOS-231..239) — e **nenhum deles corre**. Não é wiring esquecido: é o ADR-018 a impedi-lo por desenho. Na v1 single-host o **loop de serviço do nó é o dono único do ciclo de vida**, com posse por lease durável (`lease:<run_id>`, fencing token monotónico, TTL por heartbeat). Correr em paralelo um `Scheduler` do EPIC-03 — que também conduz `ready→running→complete|failed` — daria **duas autoridades sobre o mesmo run**, que é a segunda fonte de verdade que o ADR-007 proíbe.

As duas fronteiras estão guardadas por teste: `packages/cmd/aos/boundary_orq_sch_test.go` impede o nó de importar o orquestrador e o escalonador; `TestBoundary_ProductionImportsAreAllowlisted` impede o dispatcher de importar o módulo de ciclo de vida. **Nenhum dos lados pode compor o outro**, e não existe hoje um terceiro sítio a quem isso pertença — daí este ticket.

O ADR-018 já nomeia a saída: num deployment distribuído, o ORQ e o SCH tornam-se **componentes autónomos que coordenam *através* do Event Store replicado**, e a invariante «um só dono por run» mantém-se, arbitrada pelo mesmo lease. O mecanismo de fencing não precisa de ser inventado — precisa de passar a ser usado por **mais do que um processo**.

**Objectivo.** Definir e entregar a composição em que o ORQ e o SCH tomam, exercem e largam a posse de um run pelo **mesmo lease durável** que o nó usa, de modo a que em cada instante exista **exactamente um dono** — sem que qualquer das duas fronteiras guardadas por teste seja violada.

**Critérios de Aceitação**
- [x] Um run tem, em qualquer instante, **um só detentor de lease**, e todas as escritas de ciclo de vida apresentam o **fencing token** corrente; uma escrita com token obsoleto é recusada, não aplicada. — *Entregue e provado **in-process** (`TestPosse_DisputaConcorrente_UmSoVencedor`, `TestEscrita_TokenObsoleto_Recusada`, `TestGrafo_EscritaDeDonoSuperado_Recusada`). **A disputa ENTRE PROCESSOS não é satisfazível com o Event Store de referência** — as réplicas de AOS-100 são cópias in-process e cada `Open` fica com a sua cabeça; medido deterministicamente. Ver **DEF-282** (eixo AOS-100) e ADR-023 §4.*
- [x] A passagem de autoridade a jusante do gate usa `lease.released` + `lease.claimed` (não a expiração por TTL): o detentor anterior **anuncia** que largou, e o novo reclama sob concorrência optimista no stream `lease:<run_id>`. — *`TestHandoff_PorAnuncio_SemJanelaDeDuplaPosse` e, com processos reais, `TestDoisProcessos_HandoffERehidratacaoPeloLog`.*
- [x] **Um só escritor** das transições de estado por-run. O outro DERIVA do log em vez de escrever — decidido e declarado neste ticket, não deixado ao wiring. — *Decidido e registado em **ADR-023** (a autoridade é o LEASE; o SCH deriva; o ORQ escreve só sob posse). Demonstrado com o despachante REAL em `TestDerivacao_DespachanteDecideDoLogSemEscrever`.*
- [x] Um componente que tome posse a meio **re-hidrata** o grafo a partir do log (`RebuildDAG`) em vez de começar vazio; não existe caminho que admita arestas cegas às já duráveis. — *`orchestrator.NewGraphBuilderFromLog` + guarda `TestGuard_SemBuilderCego`; provado através da fronteira do processo.*
- [x] As duas fronteiras guardadas por teste (ADR-018 no nó; allowlist de imports no dispatcher) continuam **verdes e inalteradas**, ou o ADR-018 é formalmente emendado com a razão registada. — *Verdes e com zero linhas alteradas. **Nenhuma emenda ao ADR-018 foi necessária**: o ADR-023 estende-o ao caso que ele próprio deferiu.*
- [x] `DEF-272` (emissão do veredicto) e `DEF-273` (transporte do payload tipado) passam a ter emissor e implementação de produção, e fecham. — *Ambos **FECHADOS**. `DEF-273` fecha nas DUAS metades: o transporte (emissor + `PayloadView`, provados pelo `PayloadResolver` real) e o **oráculo de efeito**, derivado de `planvalidate.Snapshot.EffectOracle()` por `Tenure.Materializer` — não como opção a ligar, mas por construção (acrescentado depois das opções do chamador; snapshot vazio recusado). A propriedade medida é a consequência: um `verifier` materializa com a tool read-only intacta e a de efeito retirada, provado também através de um processo real.*

**Detalhes Técnicos.** Reutiliza `durable.LeaseManager` (`lease.claimed`/`renewed`/`released`, stream `lease:` namespaceado e serializado por `expected_seq`). Coordenação por Event Store replicado (AOS-100), não por memória partilhada. O `RebuildDAG` de AOS-025 já existe e é fiel; o que falta é uma via de construção que o aceite. Ver o inventário de defeitos do grafo levantado pela auditoria adversarial de 2026-08-30 — vários deles (inversão de ordem entre escritores, `restoreState` sem CAS, ausência de re-hidratação) são **sintomas de não haver árbitro**, e resolvem-se com esta decisão em vez de um a um.

**Testes Requeridos.** Dois processos a disputar o mesmo run: só um ganha o claim, o outro vê o `expected_seq` falhar. Escrita com token obsoleto recusada. Handoff a jusante do gate sem janela de dupla-posse. Tomada de posse a meio reconstrói o grafo e continua sem divergir do log. Os dois guard-tests de fronteira verdes.

**Definition of Done**
- [x] Critérios de Aceitação satisfeitos e demonstráveis com dois processos reais. — *`packages/cmd/aos-orq` com testes de dois processos do SO cujo único canal é o WAL do Event Store. **Limite declarado:** a demonstração cobre a posse SEQUENCIAL (handoff + re-hidratação através da fronteira do processo); a contenção CONCORRENTE fica provada in-process, porque o substrato de referência não arbitra entre processos (DEF-282).*
- [x] `-race` verde; nenhum guard-test de fronteira alterado sem emenda registada ao ADR-018. — *Guardas de fronteira: **inalterados e verdes** (zero linhas tocadas — `git status` confirma-o), e **nenhuma emenda ao ADR-018 foi necessária**. `-race`: **VERDE EM CI** (run [33318949655](https://github.com/albinoJimy/aos/actions/runs/33318949655), 2026-08-30) — `test` (`-race -covermode=atomic` por módulo, `CGO_ENABLED=1`) **pass** em 4m50s e `apex` (que corre `go test -race` sobre `packages/integration`) **pass** em 59s. Confirmado no log do job que os dois módulos novos correram sob `-race` por nome, com a MESMA cobertura da corrida local — `control-plane/runlifecycle` 80,4% e `cmd/aos-orq` 18,1% —, o que fecha o código concorrente desta entrega: `TestPosse_DisputaConcorrente_UmSoVencedor` (8 goroutines a reclamar o mesmo run) e `TestKeep_*` (renovador em segundo plano + join). Correu em CI e não localmente por decisão do dono (2026-08-30): a máquina de desenvolvimento não tem toolchain C e `-race` exige cgo. **Os 25 checks do PR passam** (1 skip: `delivery`); em particular `sca` **pass**, confirmando que o vermelho local era falha de DNS a `vuln.go.dev` e não vulnerabilidade.*
- [x] `DEF-272` e `DEF-273` fechados no registo; `DEF-803` reavaliado. — *Os dois **FECHADOS** (`FECHADO-RESIDUAL`); `DEF-803` reavaliado e **mantido ABERTO** com a razão registada. Acrescentado `DEF-282` (o limite medido do substrato).*
- [x] `tecnica/10` actualizado com o diagrama de posse e handoff. — *§3-bis, com o diagrama do ciclo de posse, a tabela de quem escreve o quê e o limite operacional.*

**Handoff para Claude Code**
```text
És o executor do ticket AOS-281 do Agentic OS de Referência (AOS).
Lê AOS-281 na íntegra em specs/EPIC-10_Topologia_Operacao_DR.md, e ADR-018 na íntegra.
NÃO comeces por escrever wiring: a primeira entrega é a DECISÃO de quem escreve as transições
de estado por-run, com a razão registada. Só depois o código.
Fundações a respeitar: um só dono por run arbitrado pelo lease durável (fencing token
monotónico); coordenação através do Event Store replicado, nunca por memória partilhada;
as duas fronteiras guardadas por teste não se alteram sem emenda formal ao ADR-018.
Não expandas escopo: este ticket NÃO reabre a forma do produto v1 (Carta §7).
```

---


## AOS-282 — [INVÁLIDO] Tecto do orçamento por árvore partilhado entre réplicas

| Campo | Valor |
|---|---|
| Epic | EPIC-10 — Topologia, Operação e DR |
| Estado | **INVÁLIDO — fechado a 2026-08-31 sem execução** |
| Tipo | — |
| Prioridade | — |
| Responsável sugerido | — |
| Documentos de referência | `docs/reports/auditoria-das-minhas-proprias-afirmacoes-2026-08-31.md` |

> **ESTE TICKET NÃO SE EXECUTA. Fica no backlog como registo de que foi inválido — apagá-lo
> deixaria a porta aberta a que a mesma ideia voltasse com outro número.**

**Porque foi criado.** A análise de 2026-08-30 afirmou que, com N réplicas, o tecto de
orçamento por árvore seria aplicado N vezes — «com 3 réplicas o tecto efectivo é o triplo».

**Porque é falso.** Três razões independentes, e qualquer uma bastava:

1. **Não existe tecto de árvore para multiplicar.** A raiz (`BudgetTreeID`) é
   DELIBERADAMENTE ilimitada nas duas dimensões. O código di-lo e explica porquê: «o tecto
   da v1 é por-run, e um tecto de árvore seria um tecto GLOBAL do nó — um conceito
   diferente (por-mandato/por-tenant), com outra unidade de tempo e outro dono».
2. **O tecto por-run não é multiplicado por réplicas.** Um run é possuído por EXACTAMENTE
   uma réplica — a invariante do ADR-023. O nó de orçamento do run existe só na réplica que
   o serve; nenhuma outra lhe cria um.
3. **O que este ticket mandava construir é PROIBIDO.** Um tecto partilhado é o que a decisão
   **D-A1.3** recusa, e há guard-test a selá-la
   (`TestAOS256_DoisRunsSequenciaisNaoPartilhamTecto`, em `packages/integration`).
   Executá-lo partiria esse teste — e o teste estaria certo.

**Como o erro foi produzido.** Inferi «a propriedade está ausente» de «os contadores vivem
em memória», sem perguntar se alguma outra coisa a fornecia. É o mesmo erro de método que
produziu a primeira versão do `AOS-287`, e está dissecado no relatório de auditoria acima.

**O que resta de verdadeiro nesta vizinhança.** Uma fuga real e estreita, que é da **v1** e
não do distribuído: o consumo de **tool calls** não é contabilizado de forma durável. É o
`AOS-287`, reescrito.

---

## AOS-283 — Eleição de líder para os laços de serviço do nó

| Campo | Valor |
|---|---|
| Epic | EPIC-10 — Topologia, Operação e DR |
| Fase | 3 — Escala e controlo |
| Milestone | **v1.1 (distribuído)** — decisão do dono, 2026-08-31 |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-100 (Event Store replicado), AOS-018 (lease/fencing) |
| Bloqueia | AOS-107 (escala horizontal em produção) |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/cmd/aos/service.go` (os laços e o `expireInFlight`), `packages/kernel/agent-runtime/durable/lease.go` (AOS-018), ADR-023 (a autoridade é o lease), `docs/reports/analise-v1-single-host-para-distribuido.md` §3.2 |

**Contexto.** O `NodeService` corre **oito** laços periódicos de serviço: aprovações expiradas, prazos duráveis, órfãos, retenção, avaliador de SLO, renovação do token do Vault, e outros. **Não existe eleição de líder em lado nenhum do repositório.**

Com N réplicas, os oito laços correm N vezes sobre o mesmo Event Store. Alguns são idempotentes por construção; pelo menos um **declara no próprio código que não é**:

> `expireInFlight` serializa as passagens do `audit.ExpirationJob` […] o check-then-Add da idempotency key **não é atómico ao nível do registo**, pelo que duas passagens concorrentes poderiam selar **DOIS** eventos `retention.expired` para o mesmo facto.

Esse guard é um `atomic.Bool` **no processo**. Com N processos há N guards e **nenhuma exclusão entre eles** — que é exactamente a falha que o comentário existe para evitar. O comentário chega a explicar que o guard «tem de ser UM SÓ», e em multi-réplica deixa de o ser.

**A excepção honrosa** é o varredor de órfãos: passa por `submit`, que reclama lease, e salta sem roubo um run detido por outra réplica. Foi escrito a pensar nisto — e é o modelo a seguir.

**Objectivo.** Garantir que cada laço de serviço tem, em qualquer instante, **no máximo um executor** entre as réplicas — pelo mesmo mecanismo de lease durável que o ADR-023 já fixa para os runs, sem inventar um segundo.

**Critérios de Aceitação**
- [ ] Cada laço de serviço só corre sob posse de um lease durável próprio (p.ex. `lease:svc:<nome-do-laço>`); uma réplica sem posse **não corre** o laço.
- [ ] A posse é renovada por heartbeat e largada por anúncio no shutdown — a réplica seguinte assume **sem esperar o TTL**.
- [ ] A morte da réplica líder é recuperada por expiração de TTL: outra réplica assume, e o laço volta a correr dentro de um limite declarado.
- [ ] Nenhum facto é selado **duas vezes** por duas réplicas — em particular `retention.expired`, que é o caso que o código já nomeia.
- [ ] Cada laço declara se é **idempotente** ou **exige exclusão**; os que exigem exclusão são fail-closed sem posse (não correm), os idempotentes podem correr sem ela se houver razão escrita.
- [ ] Os contadores/idade de varrimento em `/metrics` distinguem «não sou líder» de «armado e à espera» e de «parado» — as três leem-se de maneira diferente e exigem acções diferentes.

**Detalhes Técnicos.** Reutiliza `durable.LeaseManager` (AOS-018) e o padrão de posse de `runlifecycle.Tenure` (AOS-281): claim → `Keep` com heartbeat → `Release` como último acto. Não introduzir um segundo mecanismo de posse. O `heartbeat` por-run fica **de fora**: é de outra natureza (por-run, não de serviço) e a sua falha já tem consequência observável.

**Testes Requeridos.** Duas réplicas, um laço: só uma o corre. Líder morre sem anunciar: a outra assume por TTL. Líder anuncia no shutdown: a outra assume de imediato. Um facto de retenção não é selado duas vezes com duas réplicas activas. Sem posse, um laço de exclusão obrigatória não corre.

**Definition of Done**
- [ ] Critérios de Aceitação satisfeitos e demonstráveis com duas réplicas.
- [ ] `-race` verde.
- [ ] Cada um dos oito laços classificado (idempotente vs exige exclusão) e a classificação registada no código, junto do laço.
- [ ] `tecnica/10` actualizado com a tabela de laços e o seu regime de posse.

**Handoff para Claude Code**
```text
És o executor do ticket AOS-283 do Agentic OS de Referência (AOS).
Lê AOS-283 na íntegra em specs/EPIC-10_Topologia_Operacao_DR.md, e o ADR-023.
NÃO comeces por escrever wiring: a primeira entrega é a CLASSIFICAÇÃO dos oito laços —
quais são idempotentes e quais exigem exclusão — com a razão de cada um registada.
Só depois o código.
Fundação a respeitar: a posse é arbitrada pelo MESMO lease durável do ADR-023
(claim → heartbeat → release como último acto). Não introduzas um segundo mecanismo de
posse: duas implementações do mesmo conceito divergem.
O varredor de órfãos já está correcto — é o modelo, não o alvo.
Não expandas escopo: este ticket NÃO reabre a forma do produto v1 (Carta §7).
```

---

## AOS-284 — Disciplina de partição da hash-chain de auditoria sob múltiplos escritores

| Campo | Valor |
|---|---|
| Epic | EPIC-10 — Topologia, Operação e DR |
| Fase | 3 — Escala e controlo |
| Milestone | **v1.1 (distribuído)** — decisão do dono, 2026-08-31 |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | M |
| Dependências | AOS-100 (Event Store replicado) |
| Bloqueia | AOS-107 (escala horizontal em produção) |
| Responsável sugerido | Responsável de Segurança |
| Documentos de referência | ADR-010 (observabilidade + audit WORM), `packages/platform/audit/chain.go` (`GenesisHash`), `packages/platform/audit/filestore.go` (`wmu`), `packages/platform/audit/checkpoint.go`, `docs/reports/analise-v1-single-host-para-distribuido.md` §3.3 |


> **AC1 RESPONDIDO — MEDIDO a 2026-08-31. O problema EXISTE, e é pior do que este ticket descrevia.**
>
> Dois `audit.OpenFileStore` sobre o mesmo ficheiro, um `Append` de cada na MESMA
> partição:
>
> ```
> A: seq=1   B: seq=1        → FORK (ambos escrevem audit_seq=1)
> reabrir:   RECUSADO — «adulteracao insertion na particao "gov", audit_seq=1»
> ```
>
> A consequência **não é** «a cadeia deixa de provar o que existe para provar», como este
> ticket dizia. É mais dura: **o nó não arranca**. A verificação de integridade do arranque
> (AOS-221) recusa a cadeia — e classifica-a como **adulteração**, indistinguível de um
> ataque para quem lê o erro. O AC3 deste ticket, que pedia essa distinção, fica assim
> confirmado como necessário.
>
> **CORRECÇÃO AO GUARD, JÁ ENTREGUE.** A medição expôs um buraco que era de **v1**, não de
> v1.1: o guard de AOS-285 trancava só o `AOS_EVENTSTORE_PATH`, e o WORM é um ficheiro
> SEPARADO (`WORMPath`). O caso comum estava coberto por consequência (com ambos os
> caminhos partilhados, o guard do Event Store recusa antes de o WORM abrir), mas a
> configuração **assimétrica** — mesmo WORM, Event Stores diferentes — não era vista por
> guarda nenhum. O guard passou a trancar os dois ficheiros.
>
> **O QUE RESTA PARA ESTE TICKET (v1.1).** O guard impede dois escritores; não dá
> disciplina de partição a um deployment em que dois escritores sejam LEGÍTIMOS. É isso
> que fica, e só isso — os AC abaixo aplicam-se a partir daqui, com o AC1 já respondido.
**Contexto.** A hash-chain de auditoria é sequencial por construção: cada registo sela o `PrevHash` do anterior, e é isso que a torna *tamper-evident*. Hoje as escritas são serializadas por um mutex **em processo** (`filestore.go`: `wmu sync.Mutex // serializa os writes ao ficheiro único`).

Dois processos a escrever a **mesma** partição computariam `PrevHash` a partir de vistas diferentes — e uma cadeia com um elo mal encadeado não é uma cadeia com um defeito: é uma cadeia que **deixa de provar o que existe para provar**. A verificação por checkpoint (`checkpoint.go`) passaria a falhar sem distinguir adulteração de corrida.

O desenho parece antecipar o problema — a `GenesisHash` fala do «PRIMEIRO registo de uma **partição**** —, mas **não está verificado** que exista disciplina a garantir que duas réplicas nunca partilham partição. Esta é a lacuna que o relatório de análise marcou como a que **mais merece segunda leitura**: o trabalho começa por confirmar se o problema existe.

**Objectivo.** Garantir que, com N réplicas, cada partição da hash-chain tem **um só escritor** em qualquer instante — ou, se o desenho já o garantir, tornar essa garantia **verificável** em vez de presumida.

**Critérios de Aceitação**
- [x] **RESPONDIDO 2026-08-31 (medido — ver o bloco acima): o problema EXISTE.** Está **estabelecido por leitura do código, e escrito**, se duas réplicas podem hoje escrever a mesma partição. Se não podem, o mecanismo que o impede é nomeado e ganha teste; se podem, o resto destes critérios aplica-se.
- [ ] Cada partição tem um escritor exclusivo, arbitrado por lease durável (ADR-023) e não por mutex em processo.
- [ ] A verificação da cadeia (`checkpoint.go`) distingue **adulteração** de **elo em falta por corrida** — um diagnóstico que confunde as duas leva o operador a investigar um ataque onde houve uma corrida, ou o contrário.
- [ ] Uma tentativa de escrita numa partição de que a réplica não é dona é **recusada**, não aplicada — e o facto de recusa é observável.
- [ ] O `GenesisHash` e a atribuição de partições são determinísticos e reconstruíveis: quem verifica a cadeia consegue saber que partições existiram sem estado externo.

**Detalhes Técnicos.** Reutiliza a disciplina de posse do ADR-023 (o mesmo lease, o mesmo fencing) — a partição de auditoria é mais um recurso com dono único. O `FencedAppender` é o precedente do enforcement no ponto de escrita. Ver a nota de `filestore.go` sobre o ficheiro único: em multi-réplica o «ficheiro único» é, ele próprio, a suposição a rever.

**Testes Requeridos.** Duas réplicas sobre a mesma partição: só uma escreve, a outra é recusada. Cadeia verificável ponta-a-ponta após handoff de partição entre réplicas. Um elo em falta por corrida é diagnosticado como tal e não como adulteração. Adulteração real continua a ser detectada.

**Definition of Done**
- [ ] Critérios de Aceitação satisfeitos, com o AC1 (existe ou não o problema) respondido por escrito ANTES do código.
- [ ] `-race` verde.
- [ ] Se o problema não existir, o ticket fecha com a prova disso e o §3.3 do relatório de análise é corrigido — fechar por «não se aplica» é um desfecho legítimo e tem de ficar registado.
- [ ] `tecnica/09` e/ou `tecnica/10` actualizados com o modelo de partição.

**Handoff para Claude Code**
```text
És o executor do ticket AOS-284 do Agentic OS de Referência (AOS).
Lê AOS-284 na íntegra em specs/EPIC-10_Topologia_Operacao_DR.md, o ADR-010 e o ADR-023.
A PRIMEIRA entrega NÃO é código: é a resposta, por leitura do código e escrita em voz
alta, à pergunta «duas réplicas podem hoje escrever a mesma partição da hash-chain?».
O relatório de análise (§3.3) declara que NÃO verificou isto — é onde começas.
Se a resposta for não, fecha o ticket com a prova e corrige o relatório. Fechar por
«não se aplica» é um desfecho legítimo; fingir que se corrigiu algo não é.
Se for sim: a posse de partição usa o MESMO lease durável do ADR-023, não um segundo
mecanismo, e a recusa acontece no ponto de escrita.
Não expandas escopo: este ticket NÃO reabre a forma do produto v1 (Carta §7).
```

---

## AOS-285 — Guard de arranque: o nó recusa arrancar sobre um Event Store já detido

| Campo | Valor |
|---|---|
| Epic | EPIC-10 — Topologia, Operação e DR |
| Fase | 0 — Fundações |
| Tipo | feature |
| Prioridade | P0 |
| Estimativa | S |
| Dependências | — |
| Bloqueia | — (é o guard que torna seguro adiar AOS-282/283/284) |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `docs/reports/analise-v1-single-host-para-distribuido.md` §5 Opção C; `DEF-282` no `REGISTO-Deferimentos`; `packages/substrate/eventstore/durable.go` (`Open`); `packages/cmd/aos/bootstrap.go` (`AOS_EVENTSTORE_PATH`); `packages/cmd/aos/wal_inspect.go`, `wal_summary.go`; Carta §7 (v1 single-host) |

**Contexto.** A v1 é single-host por decisão datada (Carta §7, emenda 1.2), e correr duas réplicas do nó sobre o **mesmo** Event Store não é seguro: `DEF-282` mede que o Event Store de referência **não arbitra entre processos** — dois `eventstore.Open` sobre o mesmo WAL e dois `Claim` do mesmo run passam **ambos**, com o mesmo token.

O que hoje impede alguém de o fazer é **um parágrafo** em `tecnica/10` §3-bis. Não há nada no código a impedi-lo, e a consequência de o fazer não é um erro visível: é a maquinaria de posse a **dar a impressão** de estar a arbitrar enquanto duas réplicas escrevem o mesmo run. O `FencedAppender` não salva — ele consulta o token corrente pelo mesmo Event Store que não arbitra.

Este ticket converte a declaração documental numa **barreira real**.

**A ARMADILHA DE DESENHO — ler antes de escolher o mecanismo.** A implementação óbvia é um lease singleton (`lease:node`) reclamado no arranque. **É vacuosa, e pela razão exacta que este ticket existe:** dois processos reclamam, ambos ganham, ambos concluem que são o único. O guard não pode assentar na arbitragem do Event Store — seria circular. Tem de usar um mecanismo **fora** dele: uma tranca de ficheiro ao nível do SO sobre o WAL (`LockFileEx`/`flock`), em que o árbitro é o SO e não o log. Bónus dessa escolha: o SO liberta a tranca na morte do processo, pelo que não há TTL a afinar nem tranca órfã após crash.

**Objectivo.** Um segundo nó sobre o mesmo Event Store **não arranca**, e diz porquê.

**Critérios de Aceitação**
- [x] Um segundo processo do nó apontado ao mesmo `AOS_EVENTSTORE_PATH` **recusa arrancar**, com erro que nomeia o ficheiro, o `DEF-282` e a razão — não um erro genérico de I/O. — *`ErrEventStoreJaDetido`, com o basename do WAL, o `DEF-282` e a acção do operador na mensagem (`TestAOS285_SegundoNoSobreOMesmoEventStoreRecusa`).*
- [x] O guard **não** assenta em lease/CAS do Event Store (seria circular — ver a armadilha acima); o teste que o prova corre **dois processos reais**, não duas goroutines. — *`eventstore.LockWAL` sobre `<wal>.lock`, arbitrado pelo SO (`CreateFile` sem partilha no Windows, `flock LOCK_EX|LOCK_NB` em Unix), zero-dep (stdlib `syscall`). `TestLockWAL_SegundoProcessoERecusado` re-invoca o binário de teste como SUBPROCESSO — exigido porque em Unix o `flock` é por descritor e em Windows a exclusão é por handle: um teste in-process passaria nas duas plataformas por razões diferentes e não provaria a propriedade entre processos.*
- [x] As vias de **LEITURA** continuam a funcionar com o nó a correr: `aos wal-inspect` e `aos wal-summary` abrem o mesmo WAL hoje (`wal_inspect.go:59`, `wal_summary.go:69`) e **não podem** ser quebradas — uma tranca exclusiva ingénua parte as duas, e parte-as no momento em que um operador mais precisa delas (a diagnosticar um incidente). — *a tranca está no ficheiro IRMÃO, não no WAL: `TestLockWAL_NaoBloqueiaQuemLe` abre e lê o mesmo WAL com a posse tomada.*
- [x] A tranca é libertada em shutdown gracioso **e** na morte abrupta do processo (é a propriedade que a tranca do SO dá de graça e um lease com TTL não dá). — *`TestLockWAL_MorteDoDetentorLibertaAPosse` mata o detentor com `kill` e o seguinte arranca. `TestLockWAL_FicheiroResidualNaoBloqueia` prova que arbitra a posse do descritor e não a existência do ficheiro — a diferença face a um lock-file ingénuo.*
- [x] Um store **in-memory** (sem `AOS_EVENTSTORE_PATH`) não é afectado: não há ficheiro a trancar e não há partilha possível.
- [x] **O guard sabe quando deixar de se aplicar.** Com um backend genuinamente partilhado (pós-`AOS-100`), recusar N réplicas seria recusar exactamente o que se quer. O guard é condicional ao substrato ser de escritor único, e essa condição é explícita no código — não uma suposição que alguém terá de descobrir a remover. — *`guardDePosseAplicavel` é função NOMEADA com a condição comentada e `TestAOS285_CondicaoDeAplicabilidade` a exercer os três casos; o eixo do `DEF-282` traz a revisão junto quando o AOS-100 landar.*

**Detalhes Técnicos.** Tranca advisory ao nível do SO sobre o ficheiro do WAL, adquirida no `Open` do caminho de ESCRITA e não no de leitura — o que exige distinguir os dois no `eventstore`, que hoje não os distingue. Alternativa a avaliar: manter o `Open` como está e pôr a tranca no `bootstrap` do nó, deixando o `eventstore` intacto; é menos abrangente (não protege outro binário que abra o WAL directamente) mas não toca no substrato. A escolha entre as duas é do executor, com a razão registada.

**Testes Requeridos.** Dois processos reais sobre o mesmo WAL: o segundo recusa e o código de saída distingue-o de uma avaria. O primeiro morre abruptamente (`kill`): o segundo passa a arrancar. `wal-inspect`/`wal-summary` funcionam com o nó a correr. Store in-memory não é afectado.

**Definition of Done**
- [x] Critérios de Aceitação satisfeitos, demonstrados com dois processos reais. — *dois processos reais em `wallock_test.go`. **RESIDUAL DECLARADO:** o guard cobre o NÓ; um segundo escritor que não seja o nó (`aos-orq`, ou outro binário a abrir o WAL sem pedir a posse) continua a não ser impedido — escolha de escopo, registada em `tecnica/10` §3-bis em vez de descoberta.*
- [x] `-race` verde. — ***VERDE EM CI** (run [33341010989](https://github.com/albinoJimy/aos/actions/runs/33341010989), 2026-08-31): `test` pass em 4m39s sobre **Ubuntu 24.04** com `CGO_ENABLED=1`, e `substrate/eventstore` verde com **92,4%** de cobertura. A confirmação em Linux é a que interessa a este ticket e não é formalidade: o caminho `flock` de `wallock_unix.go` NUNCA correu na máquina de desenvolvimento (Windows) — foi apenas compilado com `GOOS=linux`. É em CI que os cinco testes do `LockWAL`, incluindo o dos DOIS PROCESSOS reais e o da MORTE do detentor, exercitam de facto o `flock`. `apex` pass em 1m1s; `dr-e2e` (35s) e `scale` (36s) também verdes — são os que mexem no Event Store sob falha e carga, e um guard mal posto no arranque tê-los-ia partido.*
- [x] `tecnica/10` §3-bis actualizado: o limite operacional deixa de ser só uma declaração e passa a citar a barreira.
- [x] `DEF-282` actualizado — o deferimento **mantém-se aberto** (o substrato continua sem arbitrar), mas passa a registar que a configuração insegura está agora **impedida**, não apenas desaconselhada.

**Handoff para Claude Code**
```text
És o executor do ticket AOS-285 do Agentic OS de Referência (AOS).
Lê AOS-285 na íntegra em specs/EPIC-10_Topologia_Operacao_DR.md e o DEF-282 no
docs/governance/REGISTO-Deferimentos.md.
LÊ A ARMADILHA DE DESENHO antes de escolheres o mecanismo: um lease singleton sobre o
Event Store é a solução óbvia e é VACUOSA — dois processos reclamam e ambos ganham, que
é precisamente o defeito que este guard existe para cobrir. O árbitro tem de ser o SO.
NÃO quebres as vias de leitura: `aos wal-inspect` e `aos wal-summary` abrem o mesmo WAL
e têm de continuar a funcionar com o nó a correr — são o que um operador usa a meio de
um incidente.
O guard tem de saber quando deixar de se aplicar (pós-AOS-100), e isso fica explícito no
código, não por descobrir.
Não expandas escopo: este ticket NÃO reabre a forma do produto v1 (Carta §7) — pelo
contrário, é o que a torna cumprida em vez de apenas declarada.
```

---

## AOS-286 — Estender o guard de posse do WAL aos restantes escritores

| Campo | Valor |
|---|---|
| Epic | EPIC-10 — Topologia, Operação e DR |
| Fase | 0 — Fundações |
| Tipo | feature |
| Prioridade | P1 |
| Estimativa | S |
| Dependências | AOS-285 (o mecanismo) |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `packages/substrate/eventstore/wallock.go` (`LockWAL`), `packages/cmd/aos/wal_posse.go`, `packages/cmd/aos-orq/main.go`, `tecnica/10` §3-bis (o residual declarado), `DEF-282` |

**Contexto.** O AOS-285 entregou a posse exclusiva de escrita do WAL e ligou-a ao **nó**. O seu escopo foi declarado — os critérios eram sobre o nó — e o residual ficou **nomeado** em `tecnica/10` §3-bis em vez de descoberto mais tarde:

> o guard cobre o **nó**. Um segundo escritor que não seja o nó — `aos-orq`, ou outro binário a abrir o mesmo WAL sem pedir a posse — continua a não ser impedido.

Este ticket paga esse residual. O caso concreto é `aos-orq serve`, que **escreve** no Event Store (topologia, estado por-nó, factos do domínio do plano) e hoje abre o WAL sem pedir posse nenhuma. Correr `aos` e `aos-orq serve` sobre o mesmo WAL é a mesma configuração insegura que o AOS-285 impede — apenas com dois binários diferentes em vez de duas cópias do mesmo.

**Objectivo.** Que a posse do WAL seja pedida por **todos** os caminhos de escrita, e por nenhum caminho de leitura.

**Critérios de Aceitação**
- [x] `aos-orq serve` pede a posse do WAL e **recusa** arrancar se ela estiver tomada, com código de saída próprio — distinto do de «lease do run detido», porque a remediação é outra (parar o outro ESCRITOR do store, não o outro dono daquele run). — *código de saída **5** (`exitWALDetido`), distinto do 3.*
- [x] `aos-orq inspect` **não** pede posse e continua a correr com um escritor activo — é uma via de leitura, como `wal-inspect`. — *`inspect` usa `abrir`, que não pede posse; `TestAOS286_InspectLeSobPosse_ServeERecusado` lê a topologia com a posse tomada.*
- [x] `aos` e `aos-orq serve` sobre o mesmo WAL: o segundo a arrancar recusa, qualquer que seja a ordem. — *mesmo mecanismo (`eventstore.LockWAL`) dos dois lados, pelo que a ordem é indiferente por construção.*
- [x] O teste que documenta o limite do substrato (`TestLimite_EventStoreDeReferenciaNaoArbitraEntreProcessos`) continua a provar o que diz — a sonda determinística in-process (dois `Open`, dois `Claim`, o mesmo token) **não** é afectada pelo guard e continua a ser o sensor do `DEF-282`. — *e o teste ficou MAIS FORTE: a corrida passou de «pelo menos um vencedor» (fraca, porque o desfecho dependia do escalonador e os perdedores iam para um balde `outros` que passaria mesmo que tivessem crashado) para «exactamente 1 vencedor e 3 recusados com o código 5, zero inesperados». É o guard que torna a corrida determinística e, com ela, a asserção honesta. A sonda determinística in-process NÃO foi tocada e continua a medir o substrato: `claim#1 token=1 · claim#2 token=1`.*
- [x] O residual em `tecnica/10` §3-bis é **retirado** ou reescrito para o que sobrar — declarar um residual já pago seria mentir na direcção oposta. — *reescrito: o residual passa a PAGO, com a nota de que um binário futuro que escreva no WAL tem de chamar `LockWAL` — não há nada que o obrigue.*

**Detalhes Técnicos.** Reutiliza `eventstore.LockWAL` (AOS-285) sem o alterar. O padrão do nó (`wal_posse.go`) é o modelo: pedir a posse ANTES de abrir o store, largá-la depois de o fechar.

**Testes Requeridos.** `aos-orq serve` recusado com o WAL detido. `aos-orq inspect` funciona com o WAL detido. A sonda determinística do `DEF-282` mantém-se verde e continua a medir o substrato, não o guard.

**Definition of Done**
- [x] Critérios de Aceitação satisfeitos, demonstrados com processos reais. — *`TestAOS286_*` e `TestLimite_*` com processos reais.*
- [ ] `-race` verde. — ***POR VERIFICAR EM CI***, pela mesma razão dos anteriores: sem toolchain C nesta máquina.
- [x] `tecnica/10` §3-bis actualizado (residual pago ou reduzido ao que resta).

**Handoff para Claude Code**
```text
És o executor do ticket AOS-286 do Agentic OS de Referência (AOS).
Lê AOS-286 e AOS-285 em specs/EPIC-10_Topologia_Operacao_DR.md.
O mecanismo JÁ EXISTE (eventstore.LockWAL) e não se altera — isto é cablagem.
CUIDADO com o teste `TestLimite_EventStoreDeReferenciaNaoArbitraEntreProcessos`: ele
corre processos aos-orq em paralelo de propósito, para documentar que o SUBSTRATO não
arbitra. Com o guard, esses processos passam a ser recusados — o que é correcto, mas
muda o que a parte de corrida do teste mede. A sonda determinística in-process é que é
o sensor do DEF-282 e essa NÃO pode ser tocada: continua a ter de medir o substrato.
Não expandas escopo: este ticket NÃO reabre a forma do produto v1 (Carta §7).
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
