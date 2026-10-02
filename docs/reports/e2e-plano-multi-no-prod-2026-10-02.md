# E2E em produção — um objectivo multi-nó e todas as suas pegadas

| Campo | Valor |
|---|---|
| Data | 2026-10-02, 15:47–15:52 UTC |
| Ambiente | Produção. Imagem `ghcr.io/albinojimy/aos-node@sha256:afb6a95a…`, já com AOS-476, AOS-477 e AOS-478. Modelo `gpt-4o-mini` |
| Âmbito aprovado pelo dono | Um pedido de plano real pela fila, e leituras. Nada mais foi escrito |
| Plano | `plan-e2e-pegadas-1790956072` |
| Como entrou | `POST /plans` com a identidade do `aos-reader`, pelo `medir-latencia-fila.sh` do servidor com outro objectivo; a drenagem do timer reclamou-o |
| Como foi lido | Cópia `:ro` de `events.wal`, `worm.wal`, `model-audit.wal` (volume `aos_aos-data`) e de `consume.wal`, `model-audit.wal` (volume `aos_aos-orq-data`), analisada localmente; `drenar-planos.log`; `/metrics` pela rede `aos_default`; `GET /plans/<id>` e `GET /runs/<id>` |
| Relatório anterior | [`e2e-pegadas-bidireccional-2026-10-01.md`](e2e-pegadas-bidireccional-2026-10-01.md), que deixou a mediação viva por exercer |

## 1. Veredicto

O plano correu até `terminal`, `exit_code 0`, em 43,9 s, e o objectivo ficou cumprido a metade: o
documento foi lido e o resumo nunca foi produzido.

- **As pegadas estão completas e sem buracos**, com uma excepção: a recusa que travou o segundo nó
  não deixou evento, selo nem contador (AOS-485).
- **O desfecho do plano não distingue este caso de um sucesso** (AOS-484).
- **Os três buracos do relatório de 2026-10-01 estão fechados em produção**: a aresta do plano está
  no log (AOS-476), o registo do plano cita o pedido e compromete-se com o objectivo (AOS-477), e
  nenhum evento tem o produtor vazio (AOS-478).

## 2. O objectivo

> Primeiro le o documento 'notes' com a tool doc_read. Depois, num passo separado que depende do
> primeiro, resume em tres pontos o que foi lido.

142 bytes. O planeador decompôs à primeira em dois nós: `n1_read_notes` (papel, com `doc_read`) e
`n2_summarize` (folha, sem tools), com a aresta `n1_read_notes → n2_summarize`.

## 3. Linha do tempo

| Hora (UTC) | Registo | Pegada |
|---|---|---|
| 15:47:53 | `events.wal`, `aos-internal/plan-requests` seq 47 | `planrequest.submitted`: objectivo cifrado por titular, compromisso `hmac-sha256:3bf0937f…`, principal `91a30a69…` (`aos-reader`), `board:prod`, `eu-west` |
| 15:51:17 | `plan-requests` seq 48 | `planrequest.claimed`, 204 s depois do 201: é a espera pelo timer da drenagem |
| 15:51:17 | `consume.wal`, `lease:<plano>` seq 1 | `lease.claimed token=1 worker=orq`, TTL 30 s. O `aos-orq` confere o compromisso do objectivo com o do pedido #47 |
| 15:51:17 | `model-audit.wal` do `aos-orq`, seq 33 | selo `allow model:invoke`, `step=planstep:decompose:1` |
| 15:51:28 | `consume.wal`, `<plano>-plan` seq 1–3 | `plan.proposed` (`plan_hash=sha256:4ce512f9…`, prompt 1.3.0, `request` = stream e seq 47 do pedido) → `plan.validated` (2 nós, orçamento 1300) → `plan.approved` (`auto:autonomy:L4`) |
| 15:51:28 | `consume.wal`, `<plano>` seq 1–3 | 2 × `task.node.created`, `task.edge.added n1_read_notes → n2_summarize` |
| 15:51:28 | `consume.wal`, `<plano>-plan` seq 4 | `plan.materialized`: `n1_read_notes` `role` com `cap:tool:doc_read`; `n2_summarize` `leaf` sem tools |
| 15:51:28–46 | `events.wal`, `<plano>~n1_read_notes` | 22 eventos, 2 turnos (§4) |
| 15:51:47–59 | `events.wal`, `<plano>~n2_summarize` | 16 eventos, 2 turnos (§5) |
| 15:52:01 | `consume.wal` | `n2_summarize running→complete`; `lease.released token=1` |
| 15:52:01 | `plan-requests` seq 49 | `planrequest.outcome`: `classe=terminal`, `origem=decomposicao geracao=1 nos=2 duracao_s=43.933` |

## 4. Nó 1 — `n1_read_notes` (cumpriu)

| seq | Evento | Conteúdo |
|---|---|---|
| 1 | `run.plan_origin` | pedido (`aos-internal/plan-requests`, geração 1), `plan_id`, `node_id` |
| 2 | `run.state.transition` | `ready→running`, `token_value=1` |
| 3 | `run.toolset.frozen` | `doc_read` 1.0.0, digest `sha256:cc05b325…`, assinado, `trust=first_seen` |
| 4–5, 7 | `step.checkpoint` | `assembled`, `model_called`, `turn_recorded` do turno 1 |
| 6 | `turn.recorded` | turno 1, `prompt_hash=sha256:620fc142…`, `model_id=served_model_id=gpt-4o-mini`, 293 tokens de entrada, 87 de saída, 1 tool call pedida |
| 8 | `tool.call.mediated` | `permit`, `doc_read`, `cap:fs.read`, `file:doc://notes@eu-west`, `taint=trusted`, `risk_class=gray`, 11,0 ms; obrigações `audit`, `region`, `autonomy` (fs, L4) |
| 9–11 | `sandbox.instance.created` / `exec.completed` / `destroyed` | driver `gvisor`, `exit_code=0`, resultado `taint=untrusted`, isolamento 4/4 |
| 12 | `step.ledger.applied` | efeito único `<run>:step-000001-tool-1`, resultado selado por titular, `result_hash=100cab38…` |
| 13 | `step.checkpoint` | `dispatched` |
| 14 | `replay.captured` | turno 1, conteúdo selado por titular |
| 15–17, 19 | `step.checkpoint` | `verified` do turno 1; `assembled`, `model_called`, `turn_recorded` do turno 2 |
| 18 | `turn.recorded` | turno 2, 795 tokens de entrada, 595 de saída, final |
| 20–21 | `replay.captured`, `step.checkpoint` | captura e `verified` do turno 2 |
| 22 | `run.state.transition` | `running→complete` |

**Identidade na mediação (seq 8):** principal `agt-drenador`, classe `agent-worker`, autoridade
`cap:fs.read`, cadeia `human:a2b5947c… → agt-drenador`, mandato `btmgjRL9…`,
`requested_by=91a30a69…`. Os eventos de sandbox e do ledger levam o mesmo produtor e a mesma cadeia.

**Resultado (`GET /runs/<id>`):** o texto integral da nota de trabalho de 15/08/2026, com a decisão
sobre o gVisor e as duas acções.

## 5. Nó 2 — `n2_summarize` (não cumpriu)

| seq | Evento | Conteúdo |
|---|---|---|
| 1–3 | `run.plan_origin`, `run.state.transition`, `run.toolset.frozen` | como no nó 1; o tool set congelado tem `doc_read` |
| 6 | `turn.recorded` | turno 1, **294 tokens de entrada**, 161 de saída, **1 tool call pedida** |
| — | *(nada)* | **nenhum `tool.call.*`, nenhum evento de sandbox** |
| 8 | `replay.captured` | turno 1 |
| 12 | `turn.recorded` | turno 2, 353 tokens de entrada, 404 de saída, final |
| 16 | `run.state.transition` | `running→complete` |

O que aconteceu:

1. O prompt do turno 1 tem 294 tokens; o turno 2 do nó 1, que já inclui o documento, tem 795. O
   conteúdo lido pelo nó 1 não chegou ao nó 2. O `consume.wal` não tem nenhum
   `plan.payload_published` para este plano: a aresta ordena os nós e não transporta dados.
2. O modelo pediu `doc_read`. O nó do plano não tem tools, a lista-branca do run é vazia, e a
   chamada foi negada antes da mediação.
3. Texto final (`GET /runs/<id>`): «Não foi possível ler o documento `notes`: o acesso foi negado
   por allowlist (`E_TOOL_OUTSIDE_RUN_ALLOWLIST`). Se você colar o conteúdo aqui ou liberar o
   `doc_read`, eu resumo em três pontos.»
4. O run terminou `complete` e o plano `terminal`, `exit_code 0`.

## 6. Selos

| Registo | Partição | Selos | Conteúdo |
|---|---|---|---|
| `worm.wal` | `gov.residency/<run>` (uma por nó) | 1 + 1 | `allow residency:run`, `eu-west`, principal `91a30a69…` |
| `worm.wal` | `ingestion:<run>` (uma por nó) | 1 + 1 | `allow redact:pii` |
| `worm.wal` | `registry.revalidation` | seq 64 | revalidação do digest de `doc_read` na chamada, política 1.0.0 |
| `worm.wal` | `<plano>~n1_read_notes` | seq 1 | `allow cap:fs.read`, `doc_read`, `step-000001-tool-1`, principal `agt-drenador` com a cadeia até ao humano, obrigações `audit`, `region`, `autonomy` |
| `worm.wal` | `gov.read/<run>` (uma por nó) | 11 + 9 | `allow read:outcome`: o polling do `aos-orq`, de 2 em 2 s |
| `model-audit.wal` do nó | `modelgw-gov:board-eu` | seq 185–188 | um `allow model:invoke` por turno, com run e passo, principal `agt-drenador` com cadeia |
| `model-audit.wal` do `aos-orq` | `modelgw-gov:board-eu` | seq 33 | a decomposição |

São 26 selos WORM em 8 partições, todos `allow`, e 5 selos de gateway. O nó 2 não tem partição de
decisão: não houve decisão selada.

## 7. Invariantes

| Invariante | Resultado |
|---|---|
| `idempotency_key == run_id:step_id` | 64 / 64 eventos (47 no `events.wal`, 17 no `consume.wal`) |
| `seq` sem buracos | 7 / 7 streams inteiramente do plano (4 no nó, 3 no `aos-orq`) |
| WORM: `AuditSeq` sem buracos e `PrevHash(n) == EntryHash(n-1)` | 8 / 8 partições |
| Produtor do envelope preenchido | 64 / 64 eventos |
| Cadeia de delegação no evento | nos 5 eventos de mediação, sandbox e ledger; nos restantes o produtor é o componente ou o submissor, como `tecnica/13` §3.1 descreve desde o AOS-478 |

`/metrics` do nó, depois do run (o contentor tinha cerca de uma hora):
`aos_mediation_permits_total 1`, `aos_mediation_denials_total 0`, uma amostra em cada um dos 9
hooks (política 5,8 ms, revalidação 4,3 ms, identidade 0,6 ms), `aos_budget_run_tokens_peak 1889`,
`aos_worm_partitions 348`, `aos_worm_partitions_anchored 341`. As 7 partições por ancorar são as
deste plano e esperam a selagem diária.

Consumo: 2 982 tokens nos quatro turnos dos nós, mais a decomposição. `cost_micro_usd` é 0 com
`custo_nao_derivado=true`, porque o par (`gpt-4o-mini`, `eu`) não tem preço na tabela embebida.

## 8. Sentido inverso: do selo ao objectivo

A partir do selo `allow cap:fs.read` (partição `<plano>~n1_read_notes`, seq 1):

1. `StepID=step-000001-tool-1` → evento seq 8 do stream do run, com a mesma chave.
2. `parent_step_id=step-000001` → `turn.recorded` seq 6 e selo 185 do gateway.
3. Principal `agt-drenador` → `human:a2b5947c…`, sob o mandato `btmgjRL9…`, a pedido de `91a30a69…`.
4. `run.plan_origin` (seq 1 do run) → pedido em `aos-internal/plan-requests`, geração 1, plano
   `<plano>-plan`, nó `n1_read_notes`.
5. `plan.proposed` cita o pedido (stream e seq 47) e leva o mesmo compromisso
   `hmac-sha256:3bf0937f…` que o `planrequest.submitted`.

A cadeia fecha por campos explícitos. A 2026-10-01 só fechava pela convenção de nomes
(`<run>-plan`, `<run>~<nó>`). O texto do objectivo continua cifrado por titular; prova-se que existe,
quem o submeteu e que o plano se comprometeu com ele.

## 9. Achados

1. **Plano `terminal` com código 0 e o objectivo por cumprir.** O nó dependente correu sem os dados
   do anterior e terminou `complete` a dizer que não conseguiu. Nada entre a conclusão do nó e o
   desfecho do plano olha para o que o nó produziu. O canal de dados entre nós existe (AOS-414) e
   não foi usado, porque o planeador declarou a dependência sem `outputs`/`consumes`. **Ticket:
   AOS-484.**
2. **A recusa pela lista-branca do run não deixa pegada.** Sem `tool.call.denied`, sem selo,
   `aos_mediation_denials_total 0`. A recusa acontece no loop
   (`packages/kernel/agent-runtime/loop.go`, `toolPermitidaNoRun`), antes do Reference Monitor. Só
   se infere do turno que pediu uma tool call sem nenhum `tool.call.*` a seguir. As outras recusas
   de tool call deixam evento e selo (33 `deny` no WORM de produção). **Ticket: AOS-485.**
3. **O nó oferece ao modelo uma tool que a lista-branca nega.** O manifesto do nó 2 lista
   `doc_read` com a lista-branca vazia; o modelo pediu-a e gastou um turno (294 + 161 tokens).
   **Ticket: AOS-486.**
4. **Retirado: `replay.captured.response.final=false` em turnos finais.** Tinha-o dado como
   discrepância com o `turn.recorded`. É deliberado: com o conteúdo selado, o `response` do evento
   leva só o consumo (`consumo()`, AOS-448), e o `final` verdadeiro está dentro do conteúdo cifrado,
   que é o que o replay lê. O `false` é o valor-zero do campo. Sem ticket.
5. **Já conhecidos e inalterados:** `cost_micro_usd=0` por falta de preço; `seccomp_enforced_by=none`
   nos eventos de sandbox; 20 dos 26 selos WORM do plano são leituras do polling do `aos-orq`.

## 10. Limites declarados

- **Uma só corrida.** A decomposição é do modelo vivo: outra corrida do mesmo objectivo pode
  declarar `consumes` e cumprir. O achado 1 é que nada o garante nem o detecta, e não que falhe
  sempre.
- **Hash-chain:** conferi o encadeamento dos selos; o recálculo dos hashes é do nó, no arranque.
- **Assinaturas** do tool set e dos selos não foram reverificadas fora do nó.
- **Conteúdo selado** (`replay.captured`, resultado do ledger, objectivo) não foi decifrado. Os
  textos finais citados vêm de `GET /runs/<id>` com a identidade do submissor.
- **Spans OTLP** não foram lidos.
- **O que a medição deixou em produção:** o pedido e os dois runs, um aviso de plano terminado no
  outbox, e três leituras (`GET /plans/<id>` e os dois `GET /runs/<id>`) que selaram mais
  `read:outcome` depois da cópia dos registos.
- **Sem `escalate` nem aprovação humana:** o plano não tinha nós de risco e passou o gate em L4.

## 11. Reprodução

Scripts e saídas brutas no scratchpad da sessão: `submeter.sh` (o `medir-latencia-fila.sh` do
servidor com `OBJECTIVO` e prefixo do run trocados, por `ssh aos-prod 'bash -s'`, sem escrever nada
no servidor), `pegadas.py` (extractor sobre as cópias dos registos; `pegadas.out` tem as 307 linhas),
`ler.sh` (as três leituras). As cópias dos registos não saíram da máquina local.
