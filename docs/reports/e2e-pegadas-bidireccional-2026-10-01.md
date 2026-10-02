# E2E de pegadas bidireccional — objectivo ↔ registo físico

| Campo | Valor |
|---|---|
| Data | 2026-10-01 (roteiro e leituras de produção) e 2026-10-02 (gates e correcções) |
| Commit | Duas corridas completas, com binários compilados na hora: `4ef35e0` (a árvore local à partida, 36 commits atrás da origem) e `f7b23f3` (a base, depois do fast-forward). As pegadas citadas são as de `4ef35e0`; as diferenças em `f7b23f3` estão em §6 |
| Roteiro seguido | [`docs/testing/e2e-pegadas-visao-19.md`](../testing/e2e-pegadas-visao-19.md), passos 0–20 (verificado antes a 2026-09-15 em `8e88f88`) |
| Ambiente local | Windows 11 + Git Bash + Go 1.27.1; nó `aos` em `127.0.0.1:18180`, substrato em ficheiro, modelo de referência |
| Produção | Só leitura: cópia `:ro` de `events.wal`, `worm.wal`, `model-audit.wal` (volume `aos_aos-data`) e de `consume.wal` (volume `aos_aos-orq-data`). Nenhum run submetido, nada escrito |
| Classes de evidência | **[VIVO]** binário a correr e os ficheiros que escreveu · **[TESTE]** system-test com `--- PASS` · **[PROD-LEITURA]** registo de produção já existente, lido em cópia |

## 1. Veredicto

A rastreabilidade fecha nos dois sentidos, com três buracos conhecidos e nenhum defeito que falhe aberto.

- **Objectivo → registo:** os passos 0–18 do roteiro passam em `4ef35e0` e em `f7b23f3` (o passo 15, em `f7b23f3`, só depois de corrigir o `snapshot.json` do roteiro). Sequência sem buracos, chave `run_id:step_id` em todos os eventos, PII a zero no WAL e no WORM, recusas fail-closed com `exit=1`.
- **Registo → objectivo:** demonstrado a partir de um selo do WORM e de um evento do WAL, localmente e em produção. Em produção a cadeia vai de um `deny` selado até ao pedido de plano, e fecha num objectivo cifrado por titular.
- **Os três buracos** têm ticket: as dependências do plano não ficam no log (AOS-476), o registo do plano não leva ao objectivo sem partir nomes (AOS-477), e a cadeia de delegação só existe nos eventos de mediação (AOS-478).
- **Gates:** 26 de 28 verdes em `f7b23f3`. Os dois vermelhos eram defeitos dos próprios gates em Windows, corrigidos no AOS-480. Ficou um vermelho intermitente por explicar no gate `nats` (§5).
- **Mediação viva:** não foi exercida. Está coberta por system-test e por leitura de um run de produção de 2026-09-27.

## 2. Sentido directo: objectivo → registo

Objectivo submetido (com PII de propósito): `auditar o pipeline de faturas; o contacto e ana.silva@example.com`, run `run-e2e-001`, leitor `nhi:demo`, board `board:aos-demo`.

| # | Passo do roteiro | Pegada observada | Critério | Resultado |
|---|---|---|---|---|
| 0 | Binários frescos | `ok aos`, `ok aos-issuer`, `ok aos-orq`, `ok aos-demo` sobre `4ef35e0` | 4 × ok | passa |
| 1 | Arranque composto | `readyz=200`; banner de **70** linhas; `agt-1:fs=L4` do ambiente recusado por falta de prova (AOS-377); GW, broker, orçamento, OTLP e backup declarados ausentes | nenhum ausente como vigente | passa |
| 2 | Submissão | `accepted` → `completed`, `turns=1`, `panicked=false` | idem | passa |
| 3 | Trajectória SSE | 9 eventos, `seq` 1→9; `ready→running` com `token_value=1`; fases `assembled → model_called → turn_recorded → verified`; `turn.recorded` com `prompt_hash=sha256:4e875ddd…`, `system_hash=sha256:e3b0c442…`, `assembly_version=1.3.0`, `model_id=served_model_id=aos-reference-model`; `replay.captured` com `sealed_content`; `running→complete` | gapless; chave `run:step` em todos | passa |
| 4 | Read-path soberano | 404 `{"error":"not found"}` × 3 byte-idênticos; 403 na escrita sem credencial; 400 sem `run_id` | idem | passa |
| 5 | Pause e steer assinados | 2 aceites; 2 recusas idênticas `403 sinal recusado`, `exit=1` | idem | passa |
| 6 | Autonomia | 6b `L2→L3` (1 assinatura); 6c L5 com 1 assinatura → `403 co_emitter em falta`; 6d `L4` com `actor=op:jimy,op:maria`; 6e demoção aceite e o mesmo corpo reenviado → `403` | dual-control e anti-replay | passa |
| 7 | WAL | `streams 11`; `run.state.transition 2`, `step.checkpoint 4`, `turn.recorded 1`, `replay.captured 1`, `run.toolset.frozen 1`, `control.pause 1`, `control.steer 1`, `ratification.nonce.consumed 7`; `wal-count --turns` = 1 | bate com a trajectória | passa |
| 8 | WORM por partição | `autonomy` 4, `governance.control` 5, `policy` 1, `gov.residency/<run>` 1, `gov.read/<run>` 3, `ingestion:<run>` 1, `gov.sovereignty.authority` 1, `trust-anchors` 1; `--denied-only` vazio | recusas não selam | passa |
| 9 | Redacção | `ana.silva` = 0 ocorrências em `es.wal` e em `worm.log`; memória episódica guarda `[REDACTED:email]` | zero nos dois | passa |
| 10 | Métricas | `aos_ready 1`, `aos_worm_partitions 8`, `aos_mediation_* = 0` | partições batem com o passo 8 | passa |
| 11 | DSAR | `reconstruct` 200 → `erase` (`stores_shredded: audit, step-ledger`) → `reconstruct` **410**; `governance.dsar` com `dsar.received` + `dsar.key_destroyed` | 200 → 410 | passa |
| 12 | Restart | WORM verificado sobre 18 partições; crash-resume sobre 17 streams, 0 órfãos; `agt-1:fs=L4` e `agt-1:http=L2` sobrevivem | estado não se perde | passa (ver achados 5 e 6) |
| 13 | WORM adulterado (cópia) | 1 byte (`s`→`X`) → `DANO INTERIOR … particao "governance.control" audit_seq=2 … CRC nao fecha`, `exit=1`, API não levanta. Controlo: a mesma cópia **sem** adulteração arranca (`readyz=200`) | abort fail-closed | passa |
| 14 | Promoção sem ratificador | `403 promocao recusada` × 2; `ratification-unratified` com 2 × `deny cap=ratify:production`; nonces 7 → 7 | negada e selada | passa |
| 15 | `aos-orq` goal → plano → DAG (em `f7b23f3` só com `mutation` no snapshot, ver §6) | `token=1`; `planner_nhi=agent:planner`; gate de plano `APROVADO sem humano (nivel L4, sem nos de risco)`; `oraculo=snapshot(sha256:snap-e2e)`; `recolha` papel spawnado, `nos_despachados=1`; `FORJADO` = 0 ocorrências no WAL; segundo dono `token=2`, `grafo re-hidratado: nos=2` | fencing monotónico, re-hidratação do log | passa (ver achados 1 e 2) |
| 16 | Validação estrutural | `acyclicity/cycle` e `tool_resolution/tool_unknown`, **`exit=9`**; WAL só com `lease.claimed` + `lease.released` | zero nós materializados | passa |
| 17 | `aos-demo` | L0 escala a tarefa `danger`; card `cap:demo.publish`; `aprovadores=[human:demo-approver-2 human:demo-approver-1]` | dois aprovadores distintos | passa |
| 18 | System-tests | 25 testes nomeados com `--- PASS`, 0 FAIL, nas duas corridas; `AOS_DR_REPORT {"mttr_ms":200,"replay_fidelity":1,"events_lost":0,"duplicated_effects":0,"crossed_boundary":false,"pass":true}` | cada nome com o seu PASS | passa |
| 19 | Mediação viva | Não corrida ao vivo. Coberta por leitura de produção (§4) | — | [PROD-LEITURA] |

### Ligação objectivo ↔ `prompt_hash`

Três runs extra, no mesmo nó:

| Run | Objectivo | Titular | `prompt_hash` |
|---|---|---|---|
| `run-e2e-001` | faturas + email | `nhi:demo` | `sha256:4e875ddd…35aa` |
| `run-e2e-002` | **o mesmo** | `nhi:auditor` | `sha256:4e875ddd…35aa` |
| `run-e2e-003` | `resumir o relatorio trimestral de vendas` | `nhi:auditor` | `sha256:6ef8297d…e7e3` |
| `run-e2e-004` | `auditar o pipeline de faturas` | `nhi:demo` | `sha256:7e48d9f2…9043` |

O hash depende só do objectivo (redigido): é igual entre runs e titulares, e é o mesmo valor que o roteiro registou a 2026-09-15 em `8e88f88`. Não é o SHA-256 da string do objectivo; é o do prompt materializado pelo assembler (`packages/kernel/agent-runtime/prompt.go:206`).

## 3. Sentido inverso: registo → objectivo (local)

Os dois ficheiros foram lidos em bruto, sem passar pela API do nó (`inv.py`, no scratchpad da sessão).

**A partir de um selo do WORM.** Registo no offset 9370 de `worm.log`:

```json
{"AuditSeq":2,"Partition":"governance.control","Decision":"allow","Reason":"control_steer",
 "Principal":{"NHIID":"op:jimy","DelegationChain":null},"Capability":"control:steer",
 "RunID":"run-e2e-001","StepID":"","ToolID":"gov.control",
 "PrevHash":"Vs8CDn9+…","EntryHash":"XACfFiz5…"}
```

1. `RunID` → stream `run-e2e-001` no `es.wal`.
2. No stream, `seq=11`, `type=control.steer`, `step_id=ctrl-2`, `idempotency_key=run-e2e-001:ctrl-2`, `payload.emitter_id=op:jimy`, com a assinatura ed25519 e a correcção. O `PrevHash` do selo é o `EntryHash` do selo 1 (`control:pause`).
3. O nonce dessa assinatura está em `ratify-nonce:745c9059…:065d714a…` (`ratification.nonce.consumed`).
4. `op:jimy` é um operador pinado no arranque; o conjunto de âncoras está selado em `trust-anchors` seq 1.
5. O run reconduz ao humano submissor pelo selo `gov.residency/run-e2e-001` seq 1 (`principal=nhi:demo`, `board:aos-demo`, região `eu`).
6. O objectivo está em `aos-internal/memory/episodic` seq 1 (`payload.id=run-e2e-001`, `goal="auditar o pipeline de faturas; o contacto e [REDACTED:email]"`, `source=authenticated_user`) e, como compromisso, no `prompt_hash` do `turn.recorded` (seq 5).

**A partir de um evento do WAL.** `run-e2e-001` seq 5 (`turn.recorded`, offset 4815): `step_id=step-000001`, `idempotency_key=run-e2e-001:step-000001`, `producer.nhi_id=nhi:demo`. Os quatro checkpoints do passo têm `parent_step_id=step-000001`. Daqui segue-se pelos pontos 5 e 6 acima.

O que **não** se consegue reconstituir localmente está nos achados 2 e 3.

## 4. Passo 19 — pegadas de produção (só leitura)

Imagem em produção: `ghcr.io/albinojimy/aos-node@sha256:cc83a945…`, contentor arrancado a 2026-10-01T21:34Z. O registo cobre 2026-08-15 → 2026-10-01.

Agregados sobre os ficheiros inteiros:

| Invariante | Resultado |
|---|---|
| `idempotency_key == run_id:step_id` | **57 861 / 57 861** eventos |
| `seq` gapless por stream | **241 / 241** streams |
| WORM: `AuditSeq` gapless e `PrevHash(n) == EntryHash(n-1)` | **341 / 341** partições, 3 469 selos (3 425 allow, 33 deny, 11 escalate) |
| `consume.wal` do `aos-orq` | 175 / 175 com chave `run:step` |

A verificação da cadeia aqui é de **encadeamento** (cada selo aponta para o anterior). O recálculo do hash de cada selo é o que o nó faz no arranque (`audit.Verify`) e não foi repetido por mim.

Run seguido: `plan-e2e-447-1790511800~n1` (2026-09-27), o mais recente com tool call mediada.

**Directo.** `planrequest.submitted` (stream `aos-internal/plan-requests` seq 44: `objective_sealed`, `principal=91a30a69…`, `board:prod`, `eu-west`) → `planrequest.claimed` → no `consume.wal`: `lease.claimed token=1` → `plan.proposed` → `plan.validated` → `plan.approved (auto:autonomy:L4)` → `task.node.created n1` → `plan.materialized` → `n1 ready→running` → no `events.wal`, run `…~n1` com 28 eventos gapless:

| seq | Evento | Conteúdo |
|---|---|---|
| 1 | `run.state.transition` | `ready→running`, `token_value=1` |
| 2 | `run.toolset.frozen` | `doc_read` com digest `sha256:cc05b325…`, assinatura e proveniência |
| 5 | `turn.recorded` | turno 1, `model_id=served_model_id=gpt-4o-mini`, 1 tool call pedida |
| 7 | `tool.call.mediated` | `permit`, `doc_read`, `cap:fs.read`, `doc://notes@eu-west`, `taint=trusted`, 6,5 ms; principal `agt-drenador` com `delegation_chain=[human:a2b5947c… → agt-drenador]`, `mandate_id`, `requested_by` |
| 8–10 | `sandbox.instance.created` / `exec.completed` / `destroyed` | driver `gvisor`, `exit_code=0`, resultado `taint=untrusted` |
| 11 | `step.ledger.applied` | efeito único, resultado selado por titular |
| 19 | `tool.call.denied` | 2.ª chamada, `deny` por `taint` (`E_DENIED_BY_HOOK`, ADR-005): contexto já `untrusted` |
| 28 | `run.state.transition` | `running→complete` |

Selos correspondentes: partição do run seq 1 (`allow cap:fs.read step-000001-tool-1`) e seq 2 (`deny … step-000002-tool-1`), ambos com a cadeia `human → agt-drenador` e `policy=1.0.0`; `registry.revalidation` seq 62 e 63 (revalidação do digest por chamada); gateway `modelgw-gov:board-eu` seq 182–184, um por turno, com `RunID` e `StepID` preenchidos; e seq 32 no gateway do `aos-orq` para a decomposição (`planstep:decompose:1`).

**Inverso.** Do selo `deny` (partição do run, seq 2) → `StepID=step-000002-tool-1` → evento seq 19 com a mesma chave → `parent_step_id=step-000002` → `turn.recorded` seq 17 e selo do gateway seq 183 → principal `agt-drenador`, delegado de `human:a2b5947c…`, sob o mandato `btmgjRL9…`, a pedido de `91a30a69…` → run `…~n1` → nó `n1` do plano `plan-e2e-447-1790511800` no `consume.wal` (`plan_hash=sha256:bf22d2c9…`) → `planrequest.submitted` seq 44, do mesmo principal, com `objective_sealed`.

A cadeia fecha num objectivo **cifrado por titular**. Prova-se que existe e quem o submeteu; o texto não foi lido (não tenho a KEK, nem a devia ter).

Critérios do passo 19 do roteiro:

| Ponto | Critério | Observado |
|---|---|---|
| F2E-01.1 manifesto | `entries` não-vazio; `model_id ≠ ""` | cumpre |
| F2E-01.4–5 identidade + PDP | decisão com taint e principal | cumpre nos eventos de mediação e nos selos; os spans OTLP não foram lidos |
| P-02 canal durável | `tool.call.*` > 0 | 52 `mediated` + 11 `denied` no ficheiro |
| F2E-01.10 selagem | um selo por decisão, cadeia `human → agente` | cumpre |
| OBS métricas | coerentes com o WORM | **não lido** (exige contentor na rede `aos_default`) |
| F2E-01.8 credencial JIT | banner do broker | **não lido** |
| F2E-01.9 SBX | `doc_read` no sandbox, `exit_code=0` | cumpre |

## 5. Gates automáticos (ticket → código → teste → gate)

Corrida completa de `scripts/ci/run.sh` sobre `f7b23f3`, em Windows, com `NATS_GO_TEST_TIMEOUT=60`. A máquina esteve suspensa durante a noite a meio do `lint`; a corrida retomou sozinha e os tempos registados não são durações reais. `AOS_SKIPPED_STEPS none`.

| Gate | Resultado | Nota |
|---|---|---|
| `secrets`, `lint`, `ref-lint`, `deferrals`, `estado-citado`, `rtm`, `layer-lint` | verde | |
| `build` | **vermelho** | Só pelo `gowork`: em Windows o gate comparava temporários em CRLF com saída em LF e dava os 49 módulos como em falta e a mais. Todos os módulos compilam. Corrigido no AOS-480 |
| `test` (+ cobertura), `integration`, `event-catalog`, `stream-names` | verde | |
| `replay` | verde | fidelidade 100 %, 0 efeitos duplicados |
| `memory`, `supplychain`, `routing`, `apex`, `security`, `evalgate`, `scale`, `ux-dx` | verde | |
| `dr-e2e` | verde | zero perda, zero duplicados, sem cruzar região, MTTR dentro do alvo |
| `nats` | **vermelho** | 1937 PASS, 0 FAIL sobre o cluster JetStream de 4 nós; avermelhou por dois saltos não declarados (`TestAOS445OutboxEAvisos`, `TestAOS450DeployEDrenagem`, que só correm em Linux). Corrigido no AOS-480 |
| `dormencia`, `sast`, `sca`, `policy-test`, `policy-taint` | verde | |

**Depois das correcções (AOS-480, PR #421):** `gowork.sh verificar` verde num checkout limpo; gate `nats` repetido com `SKIP=8 (0 não declarados)`. O CI do PR, em Linux, ficou verde nos 30 checks.

**Vermelho intermitente, por explicar.** Na repetição local do `nats`, `TestAOS392_DespachoMultiProcessoSobreSubstratoReplicado` falhou uma vez (1936 PASS, 1 FAIL): dos três processos que disputam o lease sobre um stream acabado de criar, dois saíram com `stream not found (code=404 err_code=10059)` na espera pelo líder, em vez de serem negados pelo lease. Na corrida anterior, sobre o mesmo código, e no CI do #421, o teste passou. É a família da janela do stream fresco (AOS-432, AOS-455). Fica por investigar; não tem ticket.

**`ci-selftest`:** estava a correr sobre `af63ab5` quando este relatório foi fechado. O resultado não está aqui. Da secção GW, corrida isolada com o caso novo GW14: 21 de 21.

**Sentido vertical (ticket → epic → código → teste → gate):** é o que `rtm` e `ref-lint` verificam, e passaram. A RTM foi regenerada duas vezes neste trabalho (475 → 479 → 480 tickets) sem ganhar nenhum par ticket × ADR.

## 6. O que mudou face ao roteiro de 2026-09-15

O roteiro continua executável, mas várias pegadas escritas já não coincidem:

- Banner com 70 linhas (eram 61): entraram AOS-417, AOS-427, AOS-428/433/435, AOS-436, AOS-439, AOS-446, AOS-457.
- Nova partição `trust-anchors` (AOS-446): 8 partições antes do DSAR, 9 depois (eram 7 e 8); `aos_worm_partitions 8` no passo 10.
- Passo 7: `streams 11` e `ratification.nonce.consumed 7` (eram 10 e 6). A diferença é o passo 6c, que agora passa a autenticação e gasta o nonce antes de ser recusado por falta de co-emissor. É deliberado (`packages/cmd/aos/autonomy_route.go:133-138`).
- Passo 12: `4 alteracao(oes) de nivel RELIDA(S)` (eram 5) e duas linhas novas de reidratação.
- Passo 15: o `aos-orq` passou a emitir `plan.proposed`, `plan.validated`, `plan.approved` e declara o gate de plano composto (AOS-408). A nota do roteiro «o gate humano de plano não está composto neste binário» está ultrapassada.
- Passo 16: `exit=9` (era 1), mensagem `plano recusado pela validacao estrutural apos esgotar as tentativas`, e o WAL fica com `lease.claimed` + `lease.released`.
- **Só em `f7b23f3`:** o passo 15 deixa de correr como está escrito. O `snapshot.json` do roteiro é recusado com `capability sem o campo obrigatorio mutation (none|mutates|unknown) — AOS-409`, `exit=1`, e o WAL fica só com a posse. Com `"mutation":"none"` em `fs.read` e `"mutates"` em `http.post`, o passo dá o mesmo que em `4ef35e0`. Tudo o resto (passos 0–14, 16–18) é igual nas duas corridas.
- Passo 14 em Git Bash: com `MSYS_NO_PATHCONV=1`, um `--key-file /c/…` deixa de ser convertido e o `aos-issuer` falha com `The system cannot find the path specified`. Passar a chave com `cygpath -m`.

## 7. Achados

1. **As dependências do plano continuam fora do log.** Zero `task.edge.added` no WAL local do `aos-orq` e zero nos 175 eventos do `consume.wal` de produção; `inspect` ordena `analise,recolha`. É o achado n.º 2 do roteiro, por fechar. `specs/EPIC-19_Planeador_Meta_Orquestracao.md:1438` fala de uma «PR aberta» com o marcador `DEF-913`, mas no `docs/governance/REGISTO-Deferimentos.md` o `DEF-913` é outra coisa (tecto da fila de planos, AOS-464). O número colidiu: o PR #300, aberto desde 2026-09-15 e sem ticket, usa `DEF-913` para a segunda metade deste achado. **Ticket: AOS-476.**
2. **No caminho `aos-orq serve --goal`, o objectivo não fica no registo.** O texto do goal tem 0 ocorrências no WAL; `plan.proposed` leva `plan_hash` e `planner_meta`, não o objectivo nem os nós. Nesse caminho o sentido inverso pára no hash. Em produção o objectivo existe, cifrado, em `planrequest.submitted`, mas noutro ficheiro e noutro volume; a ligação plano ↔ pedido ↔ run do nó faz-se pela convenção de nomes (`<run>-plan`, `<run>~<nó>`). Não encontrei um campo de correlação explícito nos eventos. **Ticket: AOS-477.**
3. **A cadeia de delegação só existe nos eventos de mediação.** Em produção, `delegation_chain` está preenchida em 63/63 `tool.call.*` e nos selos de decisão; em `turn.recorded` (184) o produtor é o `sub` do submissor sem cadeia; em `run.state.transition`, `step.checkpoint`, `sandbox.*`, `step.ledger.applied`, `control.*` e `lease.*` o `producer.nhi_id` é vazio. Localmente, com credencial demo-grade, a cadeia é `null` em todo o lado. A recondução ao humano é por `run_id`, não por evento, e `tecnica/13_Modelo_Dados_Eventos.md` §3.1 promete-a por evento. **Ticket: AOS-478.**
4. **Recusas sem selo no WORM** (achado n.º 7 do roteiro): é decisão registada, não lacuna. Sinal de controlo recusado, mudança de autonomia recusada e leitura negada não selam; `--denied-only` vem vazio em `governance.control` e `autonomy`. O único rasto do pedido L5 recusado (6c) é o nonce consumido no WAL. `packages/cmd/aos/control_seal.go` diz porquê: só se selam acções que surtiram efeito, para não dar a quem inunda o canal um vector para inchar o trilho. Sem ticket de engenharia; o roteiro corrige-se no AOS-479.
5. **Achado n.º 6 do roteiro isolado: a causa é o restart.** Depois do DSAR e antes do restart, `GET /runs/run-e2e-001` ainda traz `final_text` e `turns`. Depois do restart perde-os, e o mesmo acontece ao `run-e2e-002`, de um titular que nunca foi apagado, cujo `reconstruct` passa a dar 410. Com a custódia de referência a KEK vive em memória (o banner declara-o): reiniciar equivale a um crypto-shred de todos os titulares. Está coberto pelo deferimento DEF-302 (custódia externa por injectar; produção usa Vault). Sem ticket de engenharia; o roteiro corrige-se no AOS-479.
6. **Reidratação da autonomia reatribui o par ao `config:node`.** No segundo arranque, o `agt-1:fs=L4` do ambiente (recusado no primeiro por falta de prova) é selado como provisionamento `L4→L4` com `actor=config:node`, e o banner diz que `AOS_AUTONOMY_LEVELS MUDOU desde o ultimo provisionamento`, quando o ambiente era o mesmo. O nível não muda. O último selo do par deixa de ser o dos dois operadores. Não investiguei o efeito num terceiro arranque.
7. **Truncatura da cauda do WORM passa sem âncora.** Removi o último selo de uma cópia (40 → 39); o nó arrancou com `readyz=200` e declarou a cadeia verificada. É o limite que o banner declara (`NAO ANCORADA`), agora demonstrado. A defesa existe (âncora assinada, AOS-268, DEF-268) e o roteiro não a arma: entra no AOS-479 como par de controlo.
8. **Observações de produção, não investigadas.** `cost_micro_usd` é 0 em 182 de 184 turnos; `model_id` vazio em 113 (histórico anterior ao AOS-396); `seccomp_enforced_by` é `none` em 52 execuções de sandbox e ausente em 42; `governance.retention` soma 2 308 dos 3 469 selos; o polling do `aos-orq` selou 42 leituras `read:outcome` em 80 s para um só run.
9. **`grep -a -o` do busybox subconta no servidor.** Contou 8 `tool.call.mediated` e 3 `run.state.transition` onde o ficheiro, copiado e analisado localmente, tem 52 e 202. Contagens feitas por `grep` dentro do contentor não servem de prova.
10. **Dois gates falhavam sempre em Windows, sem defeito no código.** O `gowork` (CRLF nos temporários, `python3` não provisionado e cache de comandos do bash) e o `nats` (dois saltos só fora de Linux por declarar). Corrigidos no AOS-480, PR #421.
11. **`TestAOS392` falha de forma intermitente sobre o cluster real**, com 404 na espera pelo líder de um stream fresco (§5). Sem ticket.

## 8. Limites declarados

- **Sem modelo vivo localmente.** Nenhuma tool call mediada ao vivo (`aos_mediation_* = 0`), nenhum `escalate`, nenhuma cerimónia `/approve` assinada, nenhum `running→paused`. A mediação é [TESTE] (passo 18a) e [PROD-LEITURA] (§4). Não corri nenhum run novo em produção.
- **Produção lida, não exercida.** As pegadas são de 2026-09-27, de uma versão anterior à imagem que corre hoje. Métricas, spans e banner de produção não foram lidos.
- **Hash-chain:** verifiquei o encadeamento; o recálculo dos hashes é do nó.
- **Assinaturas ed25519** dos sinais de controlo e das provas de autonomia não foram reverificadas fora do nó.
- **Correcção do steer em claro:** `control.steer` guarda `payload.correction` em texto no WAL. Não testei uma correcção com PII.
- **Credencial do leitor demo-grade** e **KEK em memória**, como no roteiro.
- **`aos-orq` multi-réplica com NATS** não foi exercido ao vivo; fica no gate `nats`.
- **`ci-selftest`** sem resultado à data de fecho (§5).
- **Achados 6, 8 e 11** ficam sem ticket.

## 9. Reprodução

Scripts e saídas brutas no scratchpad da sessão (`a.sh` … `g.sh`, `inv.py`, `prod.py` e os `.out`; a corrida de `4ef35e0` em `run1/`). O estado local (`e2e/state`, `e2e/orq`) ficou preservado; as seeds são descartáveis.
