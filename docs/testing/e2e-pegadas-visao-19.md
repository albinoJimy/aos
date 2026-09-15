# Teste E2E manual — pegadas de dados pela Visão End-to-End

| Campo | Valor |
|---|---|
| Referência | [`tecnica/19_Visao_End_to_End.md`](../../tecnica/19_Visao_End_to_End.md) (fluxos F2E-01…06, processos P-01…07, capacidades §5) |
| Tipo | Roteiro de verificação **manual**, passo a passo, **sem script**: cada passo é um comando isolado, com a saída real e o critério de passagem |
| Ambiente | Local: Windows 11 + Git Bash + Go 1.27.1, **sem Docker**, sem modelo vivo |
| Verificado | 2026-09-15, commit `8e88f88`, binários compilados na hora (passo 0.1) |
| Precedente | [`ciclo-de-vida-manual.md`](ciclo-de-vida-manual.md) (stack `dev-hardened` com modelo real) e [`subprocessos-decomposicao.md`](subprocessos-decomposicao.md) |

---

## 0. Como ler este teste

Cada passo tem quatro partes:

- **Porquê**: o ponto do doc 19 que o passo verifica.
- **Comando**: o que se escreve à mão.
- **Pegada**: a saída **real** observada na verificação de 2026-09-15.
- **Verificar**: o critério objectivo de passagem.

Os ids voláteis (ULIDs, nonces, assinaturas, timestamps) mudam de corrida para corrida. As **formas**, os **tipos de evento**, as **partições** e as **contagens** não mudam.

### Classes de evidência

| Classe | Significado |
|---|---|
| **[VIVO]** | Observado num binário a correr (nó `aos`, `aos-orq`, `aos-demo`) e nos ficheiros que ele escreveu |
| **[TESTE]** | O binário local não chega lá (o modelo de referência não pede tools, e há peças que não estão compostas em nenhum binário). A prova é um system-test do código composto, corrido com `go test -v`. Nunca se apresenta como pegada viva |
| **[SERVIDOR]** | Só observável no stack com modelo e sandbox reais. Aqui fica o que recolher e o critério; **não foi corrido nesta verificação** |

### Onde vivem as pegadas

| Superfície | Como se lê | O que regista |
|---|---|---|
| Banner de arranque | `serve.log` | Que componente ficou composto, dormente ou ausente, e porquê |
| Resposta HTTP | `curl -w ' http=%{http_code}'` | Veredicto da fronteira (201/200/400/403/404/410) |
| Trajectória SSE | `GET /runs/{id}/trajectory` | Eventos do run, com `seq`, `step_id` e `idempotency_key` |
| Event Store (WAL) | `aos wal-summary`, `aos wal-count`, `strings` | Tipos de evento e contagens; payloads |
| Audit WORM | `aos audit-trail --run <partição>` | Decisões seladas por partição, com hash-chain |
| Métricas | `GET /metrics` | Contadores de saúde e de mediação |
| stdout dos binários | terminal | Posse, plano, despacho, recusas |

> **As contagens dependem da ordem.** Os valores abaixo são os observados **no momento indicado**. Correr os passos por outra ordem muda os números, mas não muda os tipos nem as partições.

---

## 1. Mapa de cobertura (doc 19 → passos)

| Doc 19 | O que se verifica | Passos | Classe |
|---|---|---|---|
| §3–§4 camadas/componentes | Postura de cada componente no arranque | 1 | VIVO |
| P-01 ciclo de vida do run | `ready→running→complete`, fencing token, checkpoints por fase, idempotency keys | 2, 3, 7 | VIVO |
| F2E-01 passos 1, 11, 12 | `turn.recorded` com manifesto; `replay.captured`; conclusão | 3 | VIVO |
| F2E-01 passos 3–10 / P-02 | Mediação RM→PDP, deny/escalate, WORM da decisão | 18a, 19 | TESTE + SERVIDOR |
| Capacidade 6/8: contexto ≠ registo | Redacção de PII antes do ES/WORM | 9 | VIVO |
| Capacidade 8: observar e auditar | Read-path soberano, leitura selada, anti-enumeração | 4, 8 | VIVO |
| F2E-03a steer/pause | Canal out-of-band assinado; emissor não pinado recusado | 5 | VIVO |
| P-04 autonomia L0–L5 | Provisionamento recusado sem prova; dual-control L4/L5; anti-replay; reidratação | 1, 6, 12 | VIVO |
| F2E-02 goal→plano→DAG | Planeador governado, validação, materialização, spawn de papel, despacho por deps | 15 | VIVO |
| F2E-02 passo 4 | Validação estrutural fail-closed (ciclo, tool inexistente) | 16 | VIVO |
| S-01a lease/fencing | Posse, token monotónico, handoff por anúncio, re-hidratação | 15 | VIVO |
| F2E-02 passo 5 / F2E-03b/c | Gate de plano, approval-card, dual-control 4-eyes | 17 | VIVO (ápice demo) |
| F2E-04 / P-03 | Admissão, headroom, breaker, aging | 1, 18b | VIVO (banner) + TESTE |
| F2E-05 / P-05 | Promoção sem ratificador negada e selada | 14, 18c | VIVO + TESTE |
| DSAR / crypto-shred | Erase, reconstrução impossível, selos sem PII | 11 | VIVO |
| F2E-06 / P-07 | Restart sobre o mesmo estado; WORM adulterado aborta; replay 100%; DR | 12, 13, 18d | VIVO + TESTE |

---

## Passo 0 — Preparação

### 0.1 Binários frescos

**Porquê:** uma pegada só vale se o binário corresponder ao código. Um binário velho dá tudo verde sobre código antigo.

```bash
E2E=/tmp/aos-e2e; mkdir -p $E2E/bin $E2E/keys $E2E/state $E2E/orq
git log -1 --format='%h %s'
for m in aos aos-issuer aos-orq aos-demo; do (cd packages/cmd/$m && go build -o $E2E/bin/$m . ) && echo "ok $m"; done
```

**Pegada:**

```
8e88f88 feat(worm): alerta por push quando a âncora do WORM deixa de chegar ao servidor
ok aos
ok aos-issuer
ok aos-orq
ok aos-demo
```

**Verificar:** os quatro `ok`, e o hash anotado junto das evidências.

### 0.2 Identidades humanas (seeds ed25519)

**Porquê:** o doc 19 exige que toda a autoridade termine num humano (§5, papéis). Aqui há dois **operadores**, que emitem sinais de controlo e mudam a autonomia, e dois **aprovadores** do 4-eyes.

```bash
for n in op-jimy op-maria ap-ana ap-bruno; do head -c 32 /dev/urandom | xxd -p | tr -d '\n' > $E2E/keys/$n.seed; done
for n in op-jimy op-maria ap-ana ap-bruno; do printf '%s=' $n; $E2E/bin/aos operator-pubkey --key $E2E/keys/$n.seed; done
```

**Pegada:**

```
op-jimy=b51918c14252751fe8056588c37f5fcf59f29bb308a7d3292e7bbe4b3900d4a3
op-maria=81a9a7ba83f005551aad9018153f0569e1544720d94377eb913d67ee0b96faa2
ap-ana=ba2b2aa97ff9ed3d2412b549dc557915184c5db2fa2fd558517633bf46878c67
ap-bruno=058a4f21da8801e8cba7d98ce27fb9bdeb66e6f1c1fc27d650879c6348331e4b
```

**Verificar:** quatro pubkeys hex de 64 caracteres, todas **distintas**. O nó aborta o arranque se dois ids partilharem a mesma pubkey.

> As seeds têm de ser UTF-8 **sem BOM**. Não as escrevas com `>` do PowerShell.

### 0.3 Trust anchor da política e roster de aprovadores

**Porquê:** o PDP só carrega um bundle cuja assinatura verifique contra uma âncora dada **fora** do bundle (doc 19 §4, PDP). O repo guarda-a em base64 e o nó exige-a em hex.

```bash
tr -d '\r\n' < packages/control-plane/pdp/policies/trust_anchor.pub | base64 -d | xxd -p | tr -d '\n'; echo
```

```bash
cat > $E2E/keys/approvers.json <<EOF
{"approvers":[
 {"principal":"human:ana","pubkey":"$($E2E/bin/aos operator-pubkey --key $E2E/keys/ap-ana.seed)","authority":["approve:safe","approve:gray","approve:danger"]},
 {"principal":"human:bruno","pubkey":"$($E2E/bin/aos operator-pubkey --key $E2E/keys/ap-bruno.seed)","authority":["approve:safe","approve:gray","approve:danger"]}
]}
EOF
```

**Pegada:** `b4d1dba379fb98d5ad9791adf867514a4754cab06309d03ef13ba848f1917a86`

**Verificar:** 64 caracteres hex.

---

## Passo 1 — Arranque composto: as camadas declaram-se

**Porquê:** doc 19 §3 e §4. Cada componente tem de dizer se está composto, e com que garantia. Um componente dormente que se apresentasse como vigente seria uma pegada falsa.

Num segundo terminal, a partir da raiz do repo:

```bash
AOS_API_ADDR=127.0.0.1:18180 \
AOS_OPERATORS="op:jimy=$($E2E/bin/aos operator-pubkey --key $E2E/keys/op-jimy.seed),op:maria=$($E2E/bin/aos operator-pubkey --key $E2E/keys/op-maria.seed)" \
AOS_AUTONOMY_SETTERS=op:jimy,op:maria \
AOS_APPROVERS_FILE=$E2E/keys/approvers.json \
AOS_DURABLE_EXECUTION=1 AOS_EVENTSTORE_PATH=$E2E/state/es.wal AOS_WORM_PATH=$E2E/state/worm.log \
AOS_POLICY_BUNDLE_DIR=$PWD/packages/control-plane/pdp/policies \
AOS_POLICY_TRUST_ANCHOR=b4d1dba379fb98d5ad9791adf867514a4754cab06309d03ef13ba848f1917a86 \
AOS_AUTONOMY_LEVELS=agt-1:fs=L4,agt-1:http=L2 AOS_AUTONOMY_DEFAULT=L1 \
$E2E/bin/aos serve 2>&1 | tee $E2E/serve.log
```

No primeiro terminal:

```bash
curl -s -o /dev/null -w 'readyz=%{http_code}\n' http://127.0.0.1:18180/readyz
```

```bash
curl -s http://127.0.0.1:18180/healthz
```

**Pegada:** `readyz=200` e `{"status":"ok"}`. O banner tem 61 linhas; estas são as que respondem ao doc 19:

| Código (doc 19 §4) | Linha do banner (excerto real) | Estado |
|---|---|---|
| GOV / identidade | `modo de IDENTIDADE: REAL (trust-anchor da autoridade AOS-156, issuer="iss:aos-node")` + `AVISO: MODO DE REFERENCIA single-process` | composto, com autoridade co-localizada |
| RT / canal de controlo | `canal de controlo: Ed25519Authenticator (AOS-160) — 2 operador(es) registado(s)` | composto |
| GOV / 4-eyes | `four-eyes gate (AOS-162) composto: 2 aprovador(es) pinado(s)` e attestation `DORMENTE` | estrutural |
| ES | `substrato: duravel em disco (AOS-170)` | composto |
| OBS / WORM | `tamper-evidence do WORM (AOS-221): hash-chain RE-ENCADEADA e verificada no arranque (0 particao(oes))` e `verificacao ancorada ... NAO ANCORADA` | hash-chain sim; âncora não |
| RT / durável | `execucao duravel (AOS-180): LIGADA — checkpointer + capturer + step-ledger` | composto |
| PDP | `PDP com BUNDLE CARREGADO ... politica em vigor versao "1.0.0"` e `changelog de politica (AOS-310): TRANSICAO SELADA ... -> 1.0.0` | composto e selado |
| GOV / autonomia | `autonomia / escalate (AOS-087/AOS-248): ORACULO LIGADO — 1 par(es)` | composto |
| GOV / autonomia | `autonomia / provisionamento (AOS-377): ATENCAO — 1 subida(s) a L4/L5 declarada(s) em AOS_AUTONOMY_LEVELS RECUSADA(S) por falta de prova assinada valida [agt-1:fs=L4 ...]` | **recusa fail-closed** (ver passo 6) |
| GW | `modelo (EPIC-06): MODELO DE REFERENCIA (referenceModel) — ... sem tool calls, com um custo CONSTANTE de 1500 micro-USD` | ausente |
| SCH/ADM (orçamento) | `orcamento / tecto de custo (AOS-008): NAO COMPOSTO` | ausente |
| SCH/ADM (ingresso) | `ingresso / admission (AOS-166/AOS-277): LIGADO ... 64 pedido(s)/segundo com burst de 128` | composto |
| RM / taint | `taint / barreira control-data-plane (AOS-069/AOS-363): INERTE — AOS_PRIVILEGED_CAPS nao esta definida` | inerte |
| RM / canal de mediação | `canal de eventos de mediacao (AOS-379): COMPOSTO e DURAVEL` | composto |
| BRK | `credential broker (AOS-070/EPIC-07, ADR-006): AUSENTE` | ausente |
| MEM | `memoria (MEM/EPIC-04, AOS-326): ... o unico caminho de producao que o usa e uma ESCRITA episodica na ingestao` | parcial |
| REG | `registry (REG/EPIC-05, AOS-326): catalogo VAZIO` | vazio |
| OBS / soberania | `soberania de leitura (AOS-172/AOS-205, D7): READ-PATH SOBERANO FAIL-CLOSED ... credencial do leitor DEMO-GRADE por headers` | composto (credencial demo) |
| OBS / DSAR | `DSAR/crypto-shredding (AOS-172, Art. 17): fluxo composto` + `custodia da KEK ... in-memory — DEMO-GRADE` | composto (KEK volátil) |
| OBS / PII | `redaccao de PII (AOS-091/AOS-208): motor LIGADO` | composto |
| OBS / OTLP | `observabilidade OTLP (AOS-173): DESLIGADA (NoopTracer)` | desligado |
| P-07 / backup | `backup imutavel + PITR (AOS-101): DESLIGADO` | desligado |
| S-01a lease | `[aos-service] loop de servico ... TTL de lease=2m0s, heartbeat=40s` | composto |
| S-01e / crash-resume | `crash-resume / varredura de arranque (AOS-253): CORREU sobre 0 stream(s)` | composto |
| P-04 automático | `democao automatica por anomalia (AOS-090/DEF-908): LIGADA` e `promocao automatica por fiabilidade (AOS-090/ADR-025): LIGADA` | composto |

**Verificar:**
- `readyz=200`.
- Nenhum componente ausente aparece como vigente.
- A linha AOS-377 declara a recusa de `agt-1:fs=L4`.

---

## Passo 2 — Submissão e estado do run (P-01)

**Porquê:** P-01 trigger (`Runtime.Run(ctx, Goal)`). O objectivo leva, de propósito, um email: o passo 9 verifica que ele nunca chega ao registo.

```bash
$E2E/bin/aos run --addr http://127.0.0.1:18180 --run-id run-e2e-001 --objective "auditar o pipeline de faturas; o contacto e ana.silva@example.com" --reader nhi:demo --board board:aos-demo
```

```bash
$E2E/bin/aos observe --addr http://127.0.0.1:18180 --run-id run-e2e-001 --reader nhi:demo --board board:aos-demo
```

```bash
curl -s -H "X-Aos-Reader: nhi:demo" -H "X-Aos-Board: board:aos-demo" http://127.0.0.1:18180/runs/run-e2e-001
```

**Pegada:**

```
run submetido: run_id=run-e2e-001 status=accepted
run run-e2e-001: status=completed terminated=true paused=false panicked=false turns=1 final=no `aos`: modelo de referencia (Model Gateway real = EPIC-06)
{"run_id":"run-e2e-001","status":"completed","terminated":true,"final_text":"no `aos`: modelo de referencia (Model Gateway real = EPIC-06)","turns":1}
```

**Verificar:**
- `accepted`, depois `completed`.
- `turns=1`.
- `panicked=false`.

---

## Passo 3 — Trajectória: o log é a memória do fluxo (P-01, F2E-01 passos 1/11/12)

**Porquê:** o doc 19 §6 diz que todos os fluxos terminam em eventos append-only. Aqui vê-se o run inteiro a partir do ES:
- as transições da máquina de estados;
- os checkpoints por fase (S-01b);
- o manifesto do turno;
- a captura para replay.

```bash
curl -s -N -m 4 -H "X-Aos-Reader: nhi:demo" -H "X-Aos-Board: board:aos-demo" http://127.0.0.1:18180/runs/run-e2e-001/trajectory
```

(O stream nunca fecha: `curl` sai com 28 ao fim de 4 s, e essa é a terminação normal.)

**Pegada:** 9 eventos SSE. Resumo campo a campo, com valores reais:

| seq | type | step_id | idempotency_key | payload relevante |
|---|---|---|---|---|
| 1 | `run.state.transition` | `state-1` | `run-e2e-001:state-1` | `from=ready to=running reason=run_start_claim token_value=1` |
| 2 | `run.toolset.frozen` | `toolset-freeze` | `run-e2e-001:toolset-freeze` | `entries=[]`, producer `nhi:composition-root` |
| 3 | `step.checkpoint` | `ckpt-assembled-step-000001` | `run-e2e-001:ckpt-assembled-step-000001` | `phase=assembled turn=1` |
| 4 | `step.checkpoint` | `ckpt-model_called-step-000001` | … | `phase=model_called` |
| 5 | `turn.recorded` | (turno 1) | … | `manifest{prompt_hash=sha256:4e875ddd…, system_hash=sha256:e3b0c442…, assembly_version=1.3.0, model{model_id="", seed=0}} input_tokens=12 output_tokens=8 cost_micro_usd=1500 tool_calls_requested=0 final=true` |
| 6 | `step.checkpoint` | `ckpt-turn_recorded-step-000001` | … | `phase=turn_recorded` |
| 7 | `replay.captured` | (turno 1) | … | `observed_at_unix_nano=1789485672436725800`, `sealed_content=eyJ3cmFwcGVkX2RlayI6…` (envelope DEK/KEK) |
| 8 | `step.checkpoint` | `ckpt-verified-step-000001` | … | `phase=verified` |
| 9 | `run.state.transition` | `state-2` | `run-e2e-001:state-2` | `from=running to=complete reason=run_complete token_value=1` |

Evento real (seq 1, completo):

```json
{"event_id":"01M2JTGSZEPV58YNENB9CJ3SX0","stream_id":"run-e2e-001","seq":1,"type":"run.state.transition","ts":"2026-09-15T15:21:12.4307039Z","producer":{"nhi_id":"","delegation_chain":null,"scope":null},"payload":{"from":"ready","to":"running","reason":"run_start_claim","token_value":1,"at":"2026-09-15T15:21:12.4307039Z"},"schema_version":"1.0","run_id":"run-e2e-001","step_id":"state-1","idempotency_key":"run-e2e-001:state-1"}
```

**Verificar** (cada linha é um ponto do doc 19):

- `seq` vai de 1 a 9 **sem buracos** (ES: ordenação total gapless por `(stream_id, seq)`).
- `idempotency_key == run_id + ":" + step_id` em **todos** os eventos (§1.4), com os domínios `state-N` e `ckpt-`.
- A transição 1 (`ready→running`) leva `token_value=1`. É a única transição com pré-condição de fencing (P-01, tabela, #1).
- As fases `assembled → model_called → turn_recorded → verified` estão por esta ordem. Não há `dispatched`, porque não houve tool calls (S-01b).
- O manifesto do `turn.recorded` tem `prompt_hash`, `system_hash`, `assembly_version` e `model{model_id, seed}` (F2E-01 passo 1). Com o modelo de referência, `model_id=""`, `seed=0`, e `system_hash` é o SHA-256 da string vazia.
- `replay.captured` traz o conteúdo **cifrado** (`sealed_content`), não em claro (F2E-01 passo 11; dívida §9.3 mitigada no conteúdo capturado).
- A transição final é `running→complete` (P-01 #9, terminal absorvente).

---

## Passo 4 — Read-path soberano e anti-enumeração (capacidade 8)

**Porquê:**
- Uma leitura sem credencial, uma leitura de outra região e a leitura de um run inexistente têm de ser **indistinguíveis**. Um 403 confirmaria que o run existe.
- A escrita recusa com 403.

```bash
curl -s -w ' http=%{http_code}\n' http://127.0.0.1:18180/runs/run-e2e-001
```

```bash
curl -s -w ' http=%{http_code}\n' -H "X-Aos-Reader: nhi:demo" -H "X-Aos-Board: board:outro" http://127.0.0.1:18180/runs/run-e2e-001
```

```bash
curl -s -w ' http=%{http_code}\n' -H "X-Aos-Reader: nhi:demo" -H "X-Aos-Board: board:aos-demo" http://127.0.0.1:18180/runs/run-nao-existe
```

```bash
curl -s -w ' http=%{http_code}\n' -X POST -H 'Content-Type: application/json' -d '{"run_id":"x","objective":"x"}' http://127.0.0.1:18180/runs
```

```bash
curl -s -w ' http=%{http_code}\n' -X POST -H "X-Aos-Reader: nhi:demo" -H "X-Aos-Board: board:aos-demo" -H 'Content-Type: application/json' -d '{"objective":"x"}' http://127.0.0.1:18180/runs
```

**Pegada:**

```
{"error":"not found"} http=404      # sem credencial
{"error":"not found"} http=404      # board de outra região
{"error":"not found"} http=404      # run inexistente
{"error":"nao autorizado"} http=403 # escrita sem credencial
{"error":"run_id em falta"} http=400
```

**Verificar:**
- As três primeiras respostas são **byte-idênticas**.
- A escrita sem credencial dá 403.
- O submit sem `run_id` dá 400.

---

## Passo 5 — Steer e pause out-of-band, assinados (F2E-03a)

**Porquê:** a pausa não depende de o agente "querer" parar. O sinal é autenticado e o emissor fica registado (não-repúdio). Um emissor não pinado, ou uma chave que não é a do emissor, é recusado com a **mesma** mensagem.

```bash
$E2E/bin/aos pause --addr http://127.0.0.1:18180 --run-id run-e2e-001 --emitter op:jimy --key $E2E/keys/op-jimy.seed
```

```bash
$E2E/bin/aos steer --addr http://127.0.0.1:18180 --run-id run-e2e-001 --emitter op:jimy --key $E2E/keys/op-jimy.seed --correction "prioriza as faturas de setembro"
```

```bash
$E2E/bin/aos pause --addr http://127.0.0.1:18180 --run-id run-e2e-001 --emitter op:intruso --key $E2E/keys/ap-ana.seed
```

```bash
$E2E/bin/aos steer --addr http://127.0.0.1:18180 --run-id run-e2e-001 --emitter op:jimy --key $E2E/keys/op-maria.seed --correction x
```

**Pegada:**

```
pause enviado a run-e2e-001 (emissor op:jimy)
steer enviado a run-e2e-001 (emissor op:jimy)
aos: aos: API devolveu 403 Forbidden: {"error":"sinal recusado"}   # emissor nao pinado, exit=1
aos: aos: API devolveu 403 Forbidden: {"error":"sinal recusado"}   # op:jimy com a chave da op:maria, exit=1
```

Os rastos no WAL (`control.pause 1`, `control.steer 1`, `run.resume.record 1`) e no WORM (`governance.control`) verificam-se nos passos 7 e 8.

**Verificar:**
- Os dois sinais legítimos são aceites.
- As duas recusas são **idênticas** (403 `sinal recusado`).

> **Limite:** o modelo de referência termina num só turno, pelo que a pausa graciosa na fronteira de fim de turno (`running→paused`, P-01 #7) não é observável ao vivo. O passo 17 mostra o canal out-of-band no `aos-demo`.

---

## Passo 6 — Autonomia L0–L5 por (agente, domínio) (P-04, ADR-014)

**Porquê:** o nível é propriedade do par. Subir a L4/L5 exige **duas assinaturas** de emissores distintos. Uma assinatura não pode ser reapresentada. A mudança fica selada, com o actor verificado.

### 6a — Nível em vigor (a subida a L4 do ambiente foi recusada no passo 1)

```bash
curl -s -H "X-Aos-Reader: nhi:demo" -H "X-Aos-Board: board:aos-demo" http://127.0.0.1:18180/autonomy
```

**Pegada:** `{"pairs":[{"agent":"agt-1","domain":"http","level":"L2"}],"unregistered_resolves_to":"L1"}`

**Verificar:**
- `agt-1:fs` **não aparece**: a declaração `L4` sem prova foi recusada.
- Um par não registado resolve para o piso `L1`.

### 6b — Subir um degrau (L2→L3), uma assinatura

```bash
B=$($E2E/bin/aos-issuer autonomy-sign --emitter op:jimy --key-file $E2E/keys/op-jimy.seed --agent agt-1 --domain http --level L3 --reason "e2e: promocao um degrau")
```

```bash
curl -s -w ' http=%{http_code}\n' -X POST -H "X-Aos-Reader: nhi:demo" -H "X-Aos-Board: board:aos-demo" -H 'Content-Type: application/json' --data-binary "$B" http://127.0.0.1:18180/autonomy
```

**Pegada:** `{"actor":"op:jimy","agent":"agt-1","domain":"http","from":"L2","status":"applied","to":"L3"} http=200`

### 6c — Pedir L5 com uma só assinatura

```bash
$E2E/bin/aos-issuer autonomy-sign --emitter op:jimy --key-file $E2E/keys/op-jimy.seed --agent agt-1 --domain fs --level L5 --reason "e2e sem co-emissor" 2>/dev/null > $E2E/l5-single.json
```

```bash
head -c 60 $E2E/l5-single.json; echo
```

```bash
curl -s -w ' http=%{http_code}\n' -X POST -H "X-Aos-Reader: nhi:demo" -H "X-Aos-Board: board:aos-demo" -H 'Content-Type: application/json' --data-binary @$E2E/l5-single.json http://127.0.0.1:18180/autonomy
```

**Pegada:**

```
# aviso: mudar para L4/L5 exige uma segunda assinatura (--co
{"error":"corpo invalido"} http=400
```

Com a linha `#` retirada (`grep -v '^#' | tr -d '\r'`), o mesmo pedido dá:

```
{"error":"mudar para L4/L5 exige duas assinaturas de operadores distintos com autonomy:set (co_emitter em falta)"} http=403
```

**Verificar:**
- Em ambos os casos o nível **não muda**.
- **Achado:** em `8e88f88` o `aos-issuer` escrevia o aviso no stdout, dentro do corpo (ver [§Achados](#achados-desta-verificação), n.º 1). A partir do #298 o aviso vai para o stderr. Com `2>/dev/null`, o `head` mostra `{"agent":…` e o nó responde logo o 403 `co_emitter em falta`.

### 6d — Subir a L4 com duas assinaturas (dual-control)

```bash
B=$($E2E/bin/aos-issuer autonomy-sign --emitter op:jimy --key-file $E2E/keys/op-jimy.seed --agent agt-1 --domain fs --level L4 --reason "e2e: dual-control" --co-emitter op:maria --co-key-file $E2E/keys/op-maria.seed)
```

```bash
curl -s -w ' http=%{http_code}\n' -X POST -H "X-Aos-Reader: nhi:demo" -H "X-Aos-Board: board:aos-demo" -H 'Content-Type: application/json' --data-binary "$B" http://127.0.0.1:18180/autonomy
```

```bash
curl -s -H "X-Aos-Reader: nhi:demo" -H "X-Aos-Board: board:aos-demo" http://127.0.0.1:18180/autonomy
```

**Pegada:**

```
{"actor":"op:jimy,op:maria","agent":"agt-1","domain":"fs","from":"(nao registado)","status":"applied","to":"L4"} http=200
{"pairs":[{"agent":"agt-1","domain":"fs","level":"L4"},{"agent":"agt-1","domain":"http","level":"L3"}],"unregistered_resolves_to":"L1"}
```

**Verificar:**
- `actor` tem **dois** emissores.
- O corpo tem `emitter` e `co_emitter`, cada um com o seu `nonce`.

### 6e — Demoção e anti-replay

```bash
B=$($E2E/bin/aos-issuer autonomy-sign --emitter op:jimy --key-file $E2E/keys/op-jimy.seed --agent agt-1 --domain http --level L2 --reason "e2e: democao")
```

Envia-se **o mesmo** `$B` duas vezes:

```bash
curl -s -w ' http=%{http_code}\n' -X POST -H "X-Aos-Reader: nhi:demo" -H "X-Aos-Board: board:aos-demo" -H 'Content-Type: application/json' --data-binary "$B" http://127.0.0.1:18180/autonomy
```

**Pegada:**

```
{"actor":"op:jimy","agent":"agt-1","domain":"http","from":"L3","status":"applied","to":"L2"} http=200
{"error":"emissor nao autorizado"} http=403
```

**Verificar:** a segunda submissão do corpo idêntico é recusada, porque o nonce é de uso único e durável. No WAL, `ratification.nonce.consumed` sobe um por assinatura aceite (passo 7).

---

## Passo 7 — Durabilidade no Event Store (ADR-001, ADR-007)

**Porquê:** o estado vive no ES, não no processo. O `wal-summary` agrega por tipo e não nomeia streams, para não permitir enumeração.

```bash
$E2E/bin/aos wal-summary --path $E2E/state/es.wal
```

```bash
$E2E/bin/aos wal-count --path $E2E/state/es.wal --run run-e2e-001 --turns
```

**Pegada** (capturada depois dos passos 6 e 11):

```
streams 10
control.pause 1
control.steer 1
lease.claimed 1
memory.record.written 1
ratification.nonce.consumed 6
replay.captured 1
run.resume.record 1
run.state.transition 2
run.toolset.frozen 1
step.checkpoint 4
turn.recorded 1
---
1
```

**Verificar:**
- As contagens batem com a trajectória do passo 3: 2 transições, 4 checkpoints, 1 turno, 1 captura, 1 congelamento.
- Os sinais do passo 5 (`control.pause`, `control.steer`) estão no log.
- `wal-count --turns` dá `1`.

> Antes dos passos 6e e 11, os valores eram `streams 9` e `ratification.nonce.consumed 5`.

---

## Passo 8 — Audit WORM por partição (ADR-010)

**Porquê:** cada decisão de governação fica selada numa hash-chain **por partição**, com `seq` gapless. A partição é a fronteira de encadeamento; não é o run id.

```bash
for p in autonomy governance.control policy gov.residency/run-e2e-001 gov.read/run-e2e-001 ingestion:run-e2e-001 gov.sovereignty.authority; do echo "### $p"; $E2E/bin/aos audit-trail --path $E2E/state/worm.log --run "$p"; done
```

```bash
$E2E/bin/aos audit-trail --path $E2E/state/worm.log --run governance.control --denied-only
```

**Pegada** (depois do passo 6e):

```
### autonomy
seq=1 allow tool=- cap=autonomy:set_level      # provisionamento agt-1:http=L2 (arranque)
seq=2 allow tool=- cap=autonomy:set_level      # L2->L3 (6b)
seq=3 allow tool=- cap=autonomy:set_level      # L4 dual (6d)
seq=4 allow tool=- cap=autonomy:set_level      # L3->L2 (6e)
### governance.control
seq=1 allow tool=gov.control cap=control:pause reason="control_pause"
seq=2 allow tool=gov.control cap=control:steer reason="control_steer"
seq=3 allow tool=gov.control cap=control:autonomy reason="control_autonomy"
seq=4 allow tool=gov.control cap=control:autonomy reason="control_autonomy"
seq=5 allow tool=gov.control cap=control:autonomy reason="control_autonomy"
### policy
seq=1 allow tool=- cap=policy:reload
### gov.residency/run-e2e-001
seq=1 allow tool=gov.residency cap=residency:run
### gov.read/run-e2e-001
seq=1 allow tool=gov.read cap=read:outcome
seq=2 allow tool=gov.read cap=read:outcome
seq=3 allow tool=gov.read cap=read:trajectory
### ingestion:run-e2e-001
seq=1 allow tool=- cap=redact:pii
### gov.sovereignty.authority
seq=1 allow tool=gov.sovereignty cap=gov.sovereignty.provision
---
(--denied-only em governance.control: vazio)
```

**Verificar:**
- `autonomy` tem 4 selos: provisionamento + 3 mudanças aceites. As recusas 6c e 6e não selaram.
- `governance.control` tem pause, steer e 3 × autonomy.
- `gov.read` tem **3** leituras com sucesso (observe, GET, trajectória). As três leituras negadas do passo 4 **não** geram selo.
- `residency:run` foi selado na criação do run.
- `ingestion:<run>` tem `redact:pii` (passo 9).

---

## Passo 9 — Redacção na ingestão: contexto ≠ registo (capacidades 6 e 8)

**Porquê:** o objectivo é redigido **antes** de tocar o ES, a memória, os spans ou o audit.

```bash
for f in $E2E/state/es.wal $E2E/state/worm.log; do printf '%s: email=%s\n' $f "$(grep -ac 'ana.silva@example.com' $f)"; done
```

```bash
strings $E2E/state/es.wal | grep -o '"type":"memory.record.written".\{0,500\}'
```

**Pegada:**

```
es.wal: email=0
worm.log: email=0
"type":"memory.record.written",...,"payload":{"id":"run-e2e-001","class":"episodic","metadata":{"agent_id":"nhi:demo","run_id":"run-e2e-001","provenance":"trusted","source":"authenticated_user",...,"ttl_class":"permanent","schema_version":"1.0"},"body":{"trace_id":"run-e2e-001","goal":"auditar o pipeline de faturas; o contacto e [REDACTED:email]","outcome":"ingested",...}
```

**Verificar:**
- Zero ocorrências do email em **ambos** os ficheiros.
- A memória episódica guarda `[REDACTED:email]`, com `provenance` e `ttl_class` (MEM, doc 19 §4).
- O WORM sela o acto de redacção (passo 8), não o conteúdo.

---

## Passo 10 — Métricas (OBS)

```bash
curl -s http://127.0.0.1:18180/metrics | grep -E '^aos_(up|ready|eventstore_healthy|mediation_[a-z_]+|worm_partitions|runs_suspended|slo_evaluations_total|approval_sweeps_total) '
```

**Pegada** (antes do passo 11):

```
aos_up 1
aos_eventstore_healthy 1
aos_ready 1
aos_slo_evaluations_total 1
aos_approval_sweeps_total 1
aos_mediation_permits_total 0
aos_mediation_denials_total 0
aos_mediation_escalations_total 0
aos_mediation_record_failures_total 0
aos_runs_suspended 0
aos_worm_partitions 7
```

**Verificar:**
- `aos_worm_partitions 7` coincide com as 7 partições do passo 8.
- Os contadores `aos_mediation_*` estão a **0**. É a prova honesta de que este nó **não mediou nenhuma tool call**, e por isso a mediação vai para os passos 18a e 19.

---

## Passo 11 — DSAR: apagamento por crypto-shred (Art. 17)

**Porquê:** destruir a chave do titular torna o conteúdo irrecuperável, e a hash-chain **continua íntegra**. Os selos não levam PII.

```bash
curl -s -w ' http=%{http_code}\n' -H "X-Aos-Reader: nhi:demo" -H "X-Aos-Board: board:aos-demo" http://127.0.0.1:18180/runs/run-e2e-001/reconstruct
```

```bash
curl -s -w ' http=%{http_code}\n' -X POST -H "X-Aos-Reader: nhi:demo" -H "X-Aos-Board: board:aos-demo" -H 'Content-Type: application/json' -d '{"subject_id":"nhi:demo"}' http://127.0.0.1:18180/dsar/erase
```

Repetir o `reconstruct`, e depois:

```bash
$E2E/bin/aos audit-trail --path $E2E/state/worm.log --run governance.dsar
```

```bash
strings $E2E/state/worm.log | grep -o '"Partition":"[^"]*"' | sort | uniq -c
```

**Pegada:**

```
{"run_id":"run-e2e-001","turns":[{"turn":1,"step_id":"step-000001","text":"no `aos`: modelo de referencia (Model Gateway real = EPIC-06)","final":true}]} http=200
{"request_id":"","subject_id":"nhi:demo","status":"erased","blocked":false,"stores_shredded":["audit","step-ledger"],"received_seq":1,"outcome_seq":2} http=200
{"error":"reconstrucao indisponivel"} http=410
---
seq=1 allow tool=gov.dsar cap=dsar.received
seq=2 allow tool=gov.dsar cap=dsar.key_destroyed
---
      4 "Partition":"autonomy"
      5 "Partition":"gov.read/run-e2e-001"
      1 "Partition":"gov.residency/run-e2e-001"
      1 "Partition":"gov.sovereignty.authority"
      5 "Partition":"governance.control"
      2 "Partition":"governance.dsar"
      1 "Partition":"ingestion:run-e2e-001"
      1 "Partition":"policy"
```

**Verificar:**
- `reconstruct` passa de 200 para **410**.
- `stores_shredded` nomeia `audit` e `step-ledger`.
- `governance.dsar` tem `received` e `key_destroyed`, sem o conteúdo.
- A 8.ª partição apareceu.
- `gov.read` subiu para 5, porque os dois `reconstruct` são leituras seladas.

---

## Passo 12 — Restart sobre o mesmo estado (P-07, S-01e, AOS-307)

**Porquê:**
- Matar o processo não perde o estado.
- No arranque, o WORM é re-verificado.
- A varredura de crash-resume corre sobre os streams duráveis.
- A autonomia assinada é **reidratada** do WORM e prevalece sobre o ambiente.

Parar o nó (Ctrl-C no segundo terminal) e voltar a correr **exactamente** o comando do passo 1. Depois:

```bash
grep -E 'tamper-evidence|crash-resume|changelog de politica|autonomia / reidratacao' $E2E/serve.log
```

```bash
curl -s -H "X-Aos-Reader: nhi:demo" -H "X-Aos-Board: board:aos-demo" http://127.0.0.1:18180/runs/run-e2e-001
```

```bash
curl -s -H "X-Aos-Reader: nhi:demo" -H "X-Aos-Board: board:aos-demo" http://127.0.0.1:18180/autonomy
```

**Pegada:**

```
[aos] tamper-evidence do WORM (AOS-221): hash-chain RE-ENCADEADA e verificada no arranque (8 particao(oes)) ...
[aos] changelog de politica (AOS-310): CONFIRMACAO SELADA no arranque na particao "policy" — policy.active 1.0.0 (content_hash bca999ac…b88a): a politica coincide com o ultimo selo ...
[aos] autonomia / reidratacao (AOS-307): 5 alteracao(oes) de nivel RELIDA(S) do WORM no arranque — um nivel posto por POST /autonomy SOBREVIVE ao reinicio ...
[aos-service] crash-resume / varredura de arranque (AOS-253): CORREU sobre 10 stream(s) — 0 run(s) orfaos em `running` ...
{"run_id":"run-e2e-001","status":"completed","terminated":true}
{"pairs":[{"agent":"agt-1","domain":"fs","level":"L4"},{"agent":"agt-1","domain":"http","level":"L2"}],"unregistered_resolves_to":"L1"}
```

**Verificar:**
- WORM verificado sobre **8** partições; o arranque de 1.ª vez verificou 0.
- Crash-resume sobre **10** streams (o mesmo número do `wal-summary`), com 0 órfãos.
- `agt-1:fs=L4`, posto por dual-control, **sobrevive** ao restart.
- Cada arranque acrescenta um selo em `policy` (confirmação) e outro em `gov.sovereignty.authority`.

> O `GET /runs` depois do DSAR e do restart já não traz `final_text` nem `turns`. Esta verificação não isola qual dos dois o causou (ver §Achados, n.º 6).

---

## Passo 13 — WORM adulterado aborta o arranque (F2E-06 passo 3)

**Porquê:** adulteração ⇒ **abort fail-closed antes de escrever**. O teste corre sobre uma **cópia** do estado, noutra porta, para não tocar no nó do passo 12.

```bash
mkdir -p $E2E/tamper && cp $E2E/state/worm.log $E2E/state/es.wal $E2E/tamper/
```

```bash
OFF=$(grep -abo 'control_steer' $E2E/tamper/worm.log | head -1 | cut -d: -f1); echo "offset=$OFF"
```

Confirmar que `offset` **não está vazio**. Sem alvo, o `dd` não altera nada e o teste passa em vazio: aconteceu na primeira tentativa desta verificação.

```bash
printf 'X' | dd of=$E2E/tamper/worm.log bs=1 seek=$((OFF+8)) conv=notrunc
```

```bash
cmp -l $E2E/state/worm.log $E2E/tamper/worm.log
```

```bash
AOS_API_ADDR=127.0.0.1:18182 AOS_DURABLE_EXECUTION=1 AOS_EVENTSTORE_PATH=$E2E/tamper/es.wal AOS_WORM_PATH=$E2E/tamper/worm.log $E2E/bin/aos serve; echo "exit=$?"
```

**Pegada:**

```
offset=7363
 7372 163 130
aos: aos: WORM durável (AOS-170) "tamper/worm.log": audit: DANO INTERIOR no WAL "tamper/worm.log" (particao "governance.control" audit_seq=2 offset=7198): CRC nao fecha — registo FISICAMENTE COMPLETO com validacao falhada, NAO e cauda rasgada de crash; a reabertura RECUSA em vez de truncar os registos integros seguintes (causa: bit-rot ou adulteracao). ...
exit=1
```

**Verificar:**
- `cmp` mostra exactamente **1 byte** diferente (`s`→`X`).
- O nó **não** levanta a API e sai com `exit=1`.
- A mensagem nomeia a partição (`governance.control`) e o `audit_seq=2` do registo atingido (o `control:steer` do passo 5).

> Esta via detecta mutação, remoção interna e inserção. A **truncatura da cauda** só é apanhada com a âncora assinada (AOS-268), que este nó não arma: o banner diz `NAO ANCORADA`.

---

## Passo 14 — Promoção sem ratificador é negada e selada (F2E-05 passo 5, P-05)

**Porquê:**
- Nenhuma auto-modificação chega a produção sem ratificação humana assinada contra a allowlist.
- Este nó arranca **sem ratificadores** (banner: `SEM RATIFICADORES — toda a promocao sera NEGADA (ratifier_unknown)`).
- A negação tem de ficar selada.

```bash
H64=$(printf 'skill e2e v1' | sha256sum | cut -d' ' -f1 | xxd -r -p | base64 | tr -d '\n'); echo "$H64"
```

```bash
MSYS_NO_PATHCONV=1 $E2E/bin/aos-issuer ratify-sign --artifact-id skill:e2e-resumo --version 1.0.0 --content-hash "$H64" --ratifier human:ana --key-file $E2E/keys/ap-ana.seed --canary-passed > $E2E/promote.json
```

```bash
curl -s -m 10 -w ' http=%{http_code}\n' -X POST -H 'Content-Type: application/json' --data-binary @$E2E/promote.json http://127.0.0.1:18180/promote
```

(repetir o `curl`)

```bash
$E2E/bin/aos audit-trail --path $E2E/state/worm.log --run ratification-unratified
```

**Pegada:**

```
/s1WLSIShKT92QbIs+IFydGdm5DAOtKo/AJmg7EVx3I=
{"artifact":{"id":"skill:e2e-resumo","kind":"skill","version":"1.0.0","content_hash":"/s1WL…","canary_passed":true,"eval":{"dataset":"golden","verdict":"pass",…}},"ratification":{"request_id":"f78ae93d…","ratifier":"human:ana","approved":true,"nonce":"…","issued_at":"…","signature":"iWNL8TJQ…"}}
{"error":"promocao recusada"} http=403
{"error":"promocao recusada"} http=403
seq=1 deny tool=governance.ratification cap=ratify:production
seq=2 deny tool=governance.ratification cap=ratify:production
```

**Verificar:**
- 403 nas duas submissões.
- Partição `ratification-unratified` com **dois `deny`** selados.
- `ratification.nonce.consumed` **não** sobe, porque o ratificador é recusado antes de consumir nonce.

> **Armadilha do Git Bash:** um base64 que começa por `/` é convertido em caminho Windows, e o `aos-issuer` recusa com `--content-hash tem de ser base64 nao-vazio`. Aplica `MSYS_NO_PATHCONV=1` **só a esse comando**. Exportada para toda a shell, a variável estraga os caminhos `/c/...` do passo 1, e o nó recusa fail-closed: `falha ao carregar o bundle PDP ... E_POLICY_UNAVAILABLE`.

---

## Passo 15 — Goal → plano → DAG → sub-agente, sob lease (F2E-02, S-01a)

**Porquê:** o F2E-02 corre no binário `aos-orq` (ADR-018/ADR-023 mantêm o ORQ/SCH fora do nó). O planeador é governado (NHI `agent:planner`); o plano é validado contra um snapshot **pinado**; nós com dependentes viram **papéis** spawnados; o despacho respeita `depends_on`.

Ficheiros de entrada:

```bash
cat > $E2E/orq/snapshot.json <<'EOF'
{
  "hash": "sha256:snap-e2e",
  "tools": [
    {"name":"fs.read","version":"1.0.0","digest":"sha256:aaa","admissible":true,
     "sensitivity":"public","egress":"none","reversibility":"reversible"},
    {"name":"http.post","version":"2.0.0","digest":"sha256:bbb","admissible":true,
     "sensitivity":"public","egress":"external","reversibility":"reversible"}
  ]
}
EOF
```

```bash
cat > $E2E/orq/plano.json <<'EOF'
{
  "plan_version": "1.0.0",
  "objective": "recolher e analisar",
  "budget_total": {"tokens": 100, "cost_micro_usd": 100},
  "planner_meta": {"model":"FORJADO","prompt_version":"9.9.9","capabilities_hash":"sha256:FORJADO"},
  "nodes": [
    {"node_id":"recolha","role":"worker","objective":"recolher","depends_on":[],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}},
    {"node_id":"analise","role":"worker","objective":"analisar","depends_on":["recolha"],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}}
  ]
}
EOF
```

(`--decompose-fixture` é **não-produção**: substitui o LLM vivo, e o resto do pipeline é real. A `planner_meta` forjada está lá de propósito, para mostrar que não sobrevive.)

### 15a — Posse, decomposição, materialização, despacho, handoff

```bash
cd $E2E/orq && ../bin/aos-orq serve --wal orq.wal --run run-e2e-orq --goal "recolher e analisar dados" --snapshot snapshot.json --decompose-fixture plano.json --worker p1 --release; echo "exit=$?"
```

**Pegada:**

```
substrato: ficheiro orq.wal — NÃO arbitra entre processos (DEF-282); posse SEQUENCIAL, uma instância de cada vez
posse: run=run-e2e-orq plano=run-e2e-orq-plan token=1 worker=p1
grafo re-hidratado: nos=0
decomposto: objectivo -> plano de 2 nos (tentativas=1, planner_nhi=agent:planner)
materializado: plano=run-e2e-orq-plan nos=2 oraculo=snapshot(sha256:snap-e2e)
  no=analise kind=leaf tools=cap:tool:fs.read
  no=recolha kind=role tools=cap:tool:fs.read
  despacho: papel recolha spawnado
despachado: plano=run-e2e-orq-plan nos_despachados=1
posse largada: run=run-e2e-orq token=1 (reclamavel JA, sem esperar TTL)
exit=0
```

### 15b — O que ficou no log

```bash
../bin/aos-orq inspect --wal orq.wal --run run-e2e-orq
```

```bash
../bin/aos wal-summary --path orq.wal
```

```bash
strings orq.wal | grep -oE '"type":"(task\.node\.created|plan\.materialized|task\.node\.state_changed)".{0,400}'
```

**Pegada:**

```
run=run-e2e-orq token_corrente=1 nos=2 ordem=analise,recolha
streams 3
lease.claimed 1
lease.released 1
plan.materialized 1
task.node.created 2
task.node.state_changed 1
"type":"task.node.created",...,"payload":{"run_id":"run-e2e-orq","task_id":"analise","state":"ready","priority":0,"tool_id":"fs.read","capability":"cap:tool:fs.read"},...,"idempotency_key":"run-e2e-orq:node:analise"
"type":"task.node.created",...,"payload":{"run_id":"run-e2e-orq","task_id":"recolha","state":"ready","priority":0},...
"type":"plan.materialized",...,"payload":{"plan_id":"run-e2e-orq-plan","plan_hash":"sha256:39a6f0f8381be5f1e4aafc6c85903a89a6601fdae0bd3f09b46b888feadc33b3","nodes":[{"node_id":"analise","kind":"leaf","tools":["cap:tool:fs.read"]},{"node_id":"recolha","kind":"role","tools":["cap:tool:fs.read"]}]},"schema_version":"aos.planner.v1",...
"type":"task.node.state_changed",...,"payload":{"run_id":"run-e2e-orq","task_id":"recolha","from":"ready","to":"running"},...
```

### 15c — Segundo dono: re-hidratação e fencing token monotónico

```bash
../bin/aos-orq serve --wal orq.wal --run run-e2e-orq --worker p2 --release; echo "exit=$?"
```

**Pegada:**

```
posse: run=run-e2e-orq plano=run-e2e-orq-plan token=2 worker=p2
grafo re-hidratado: nos=2
posse largada: run=run-e2e-orq token=2 (reclamavel JA, sem esperar TTL)
exit=0
```

Depois disto, `wal-summary` mostra `lease.claimed 2` e `lease.released 2`.

**Verificar** (F2E-02 e S-01a, ponto a ponto):

- A posse vem **antes** de qualquer escrita, com `token=1` e `ttl_nanos=30000000000` no `lease.claimed`.
- `planner_nhi=agent:planner` (F2E-02 passo 3).
- O `oraculo=snapshot(sha256:snap-e2e)`: o hash é o do snapshot pinado, não o `sha256:FORJADO` do fixture (F2E-02 passo 4).
- `recolha` tem um dependente e vira `kind=role`, spawnado **no despacho**; `analise` é `leaf` (F2E-02 passo 6, ADR-024).
- `nos_despachados=1`: `analise` espera por `recolha`, e só `recolha` passa `ready→running` (F2E-02 passo 7).
- `plan.materialized` traz `plan_hash`. Tanto este evento como os nós têm `idempotency_key` `run:step`.
- O segundo dono recebe `token=2` (monotónico) e re-hidrata `nos=2` **do log**, não de memória (S-01a).

> **Achados neste passo** (§Achados, n.os 2 e 4):
> - Zero eventos `task.edge.added`: a dependência `analise → recolha` não está no log, e o `inspect` ordena `analise` antes de `recolha`.
> - Não aparece `plan.intake_classified` (F2E-02 passo 1).
> - O gate humano de plano (F2E-02 passo 5) não está composto neste binário (doc 19 §9.7, DEF-274).

---

## Passo 16 — Validação estrutural fail-closed (F2E-02 passo 4)

**Porquê:** um plano inválido é **rejeitado**, sem *trimming* e sem materializar nada.

```bash
sed 's/"depends_on":\[\],/"depends_on":["analise"],/' plano.json > ciclico.json
```

```bash
../bin/aos-orq serve --wal ciclo.wal --run run-e2e-ciclo --goal "x" --snapshot snapshot.json --decompose-fixture ciclico.json --worker p1; echo "exit=$?"
```

```bash
sed '0,/"fs.read","version":"1.0.0","digest":"sha256:aaa"/s//"shell.exec","version":"1.0.0","digest":"sha256:zzz"/' plano.json > tool-inexistente.json
```

```bash
../bin/aos-orq serve --wal tool.wal --run run-e2e-tool --goal "x" --snapshot snapshot.json --decompose-fixture tool-inexistente.json --worker p1; echo "exit=$?"
```

```bash
../bin/aos wal-summary --path ciclo.wal; ../bin/aos wal-summary --path tool.wal
```

**Pegada:**

```
decomposto: objectivo -> plano de 2 nos (tentativas=1, planner_nhi=agent:planner)
aos-orq: plano rejeitado na validação estrutural (AOS-231): cycle
exit=1
decomposto: objectivo -> plano de 2 nos (tentativas=1, planner_nhi=agent:planner)
aos-orq: plano rejeitado na validação estrutural (AOS-231): tool_unknown
exit=1
streams 1
lease.claimed 1
streams 1
lease.claimed 1
```

**Verificar:**
- `exit=1` com o diagnóstico `cycle` ou `tool_unknown`.
- O WAL de cada recusa tem **só** `lease.claimed`: zero `task.node.created` e zero `plan.materialized`.

---

## Passo 17 — Gate de plano, approval-card e dual-control (F2E-03b/c, P-04 S-04a)

**Porquê:**
- Com o modelo de referência, o nó nunca emite `escalate`, e por isso o `/approve` é inalcançável ao vivo.
- O ápice single-process `aos-demo` compõe o **PlanGate real** (revisão forçada e dual-control por efeito), a superfície de aprovação e o canal out-of-band.

```bash
$E2E/bin/aos-demo
```

**Pegada** (linhas do fluxo):

```
[demo] a) SPAWN: a criar o run — transição durável ready → running
[demo] b) StateProjector reflectiu o estado durável: Current() = running
[demo] b') PLAN-APPROVAL via PlanGate real (postura segura AOS-236: revisão forçada + dual-control por-efeito): aprovado=true (L0 escalou a tarefa danger; nó revisto item-a-item + dois aprovadores distintos)
[demo] c) turno corrido: turns=1 terminated=true final="demonstração concluída pelo modelo fake"
[demo] d) superfície renderizada (desktop): título="Aprovacao pendente — danger" corpo="cap:demo.publish -> superfície desktop\nRequer dois aprovadores distintos (irreversivel)." botões=2
[demo]      botão: "Aprovar" (action=approve:card-demo-0001 danger=false)
[demo]      botão: "Rejeitar" (action=reject:card-demo-0001 danger=true)
[demo] d') APPROVE (dual-control estrutural): autorizado=true aprovadores=[human:demo-approver-2 human:demo-approver-1]
[demo] e) steer OUT-OF-BAND despachado; eco da correcção pendente: "corrige o rumo: prioriza a superfície desktop"
[demo] f) pause OUT-OF-BAND despachado; eco de pausa pendente: true
[demo] demo concluído com sucesso
```

**Verificar:**
- Em L0, a tarefa `danger` **escala**.
- O card mostra o efeito resolvido (`cap:demo.publish`), a classe (`danger`) e a irreversibilidade.
- A autorização exige **dois** aprovadores **distintos**.
- Rejeitar tem atrito (`danger=true`).

> **Limites declarados pelo próprio demo:**
> - O RM usa hooks neutros, sem enforcement real.
> - A distinção entre aprovadores é estrutural, sem attestation.
>
> A cerimónia real, com assinatura ed25519 por aprovador, é a do nó (`POST /runs/{id}/approve`), exercida no stack com modelo (passo 19).

---

## Passo 18 — System-tests onde o binário local não chega [TESTE]

Cada comando corre-se da raiz do repo. A pegada é a linha `--- PASS` e, quando existe, o relatório que o teste imprime.

### 18a — Mediação sem bypass pelo nó completo (F2E-01 passos 3–10, P-02)

```bash
(cd packages/cmd/aos && go test -count=1 -v -run 'TestAOS169_Mediation_NoBypass_FullNodeAPI' .)
```

**Pegada:** `--- PASS: TestAOS169_Mediation_NoBypass_FullNodeAPI`. O teste compõe o nó com o RM real e o bundle assinado, e prova permit + deny + no-bypass com a call a atravessar o RM.

### 18b — Admissão, headroom, breaker, aging e PDP indisponível (F2E-04, P-03, RB-01/03/04)

```bash
(cd packages/control-plane/scheduler && go test -count=1 -v -run 'TestGameDay_RB01|TestGameDay_RB03|TestDispatch_AgingPromotesOldLowOverNewHigh|TestBreaker_TripParksTreeNoDuplicateEffects' .)
```

```bash
(cd packages/kernel/reference-monitor && go test -count=1 -v -run 'TestGameDay_RB04' .)
```

**Pegada:**

```
--- PASS: TestBreaker_TripParksTreeNoDuplicateEffects
--- PASS: TestDispatch_AgingPromotesOldLowOverNewHigh
--- PASS: TestGameDay_RB03_BudgetExhaustionTripsBreakerFailClosed
--- PASS: TestGameDay_RB01_AdmissionRefusesSpawnsWithoutHeadroom
--- PASS: TestGameDay_RB04_PDPUnavailableFailsClosed
```

### 18c — Ratificação e rollback de artefacto regredido (F2E-05, RB-05)

```bash
(cd packages/cmd/aos && go test -count=1 -v -run 'TestAOS275_PromoteRoute' .)
```

```bash
(cd packages/control-plane/governance/hitl && go test -count=1 -v -run 'TestGameDay_RB05' .)
```

**Pegada:**

```
--- PASS: TestAOS275_PromoteRouteRatificaESelaComPrincipal
--- PASS: TestAOS275_PromoteRouteRecusaAssinaturaForjada
--- PASS: TestAOS275_PromoteRouteRecusaRatificadorDesconhecido
--- PASS: TestAOS275_PromoteRouteHerdaMTLSDeControlo
--- PASS: TestAOS275_PromoteRouteFronteiraFailClosed        (7 subcasos)
--- PASS: TestAOS275_PromoteRouteCampoDesconhecido
--- PASS: TestGameDay_RB05_RegressedArtifactBlockedRollbackToBaseline
```

### 18d — DR, replay com fidelidade 100% e zero efeitos duplicados (F2E-06, P-07)

```bash
(cd packages/qa/dr-e2e && go test -count=1 -v -run 'TestDR_Failover_ResumeFromStepFidelity|TestDR_ReportsMTTRAndFidelity' .)
```

```bash
(cd packages/platform/dr && go test -count=1 -v -run 'TestGameDay_EndToEnd_Recovery|TestGameDay_RTOExceeded_FailsButPersists|Tamper|Stale|Sovereignty' .)
```

```bash
(cd packages/kernel/agent-runtime && go test -count=1 -v -run 'TestReplayFidelity100|TestReplayResumeFromStepProducesSameState|TestGameDay_RB02_ZombieReassignedWithFencing|TestWorker_KillMidRun_ResumesFromStep_NoDuplicateEffects|TestCompensationIdempotentNoDuplicate' ./replay ./durable ./worker ./saga)
```

**Pegada:**

```
dr_replay_e2e_test.go:706: AOS_DR_REPORT {"mttr_ms":200,"replay_fidelity":1,"events_lost":0,"duplicated_effects":0,"crossed_boundary":false,"pass":true}
--- PASS: TestDR_ReportsMTTRAndFidelity
--- PASS: TestDR_Failover_ResumeFromStepFidelity
--- PASS: TestGameDay_EndToEnd_Recovery
--- PASS: TestGameDay_RTOExceeded_FailsButPersists
--- PASS: TestRecover_TamperedBackup_Aborts                   (ErrSegmentTampered)
--- PASS: TestRecover_StaleAuditCheckpoint_Aborts            (ErrCheckpointStale)
--- PASS: TestSovereignty_CrossBorderReplica_Rejected        (ErrSovereigntyViolation)
--- PASS: TestReplayFidelity100
--- PASS: TestReplayResumeFromStepProducesSameState
--- PASS: TestGameDay_RB02_ZombieReassignedWithFencing
--- PASS: TestWorker_KillMidRun_ResumesFromStep_NoDuplicateEffects
--- PASS: TestCompensationIdempotentNoDuplicate
```

**Verificar:**
- Cada teste nomeado tem a sua linha `--- PASS`. Um nome inexistente dá `ok` **sem** `--- PASS`, e isso conta como falha deste passo.
- `AOS_DR_REPORT` tem `replay_fidelity:1`, `duplicated_effects:0`, `events_lost:0` e `crossed_boundary:false`.

---

## Passo 19 — Mediação viva de uma tool call [SERVIDOR]

**Porquê:** é o caminho quente F2E-01, passos 2–10: GW → RM → PDP → (escalate) → ADM → BRK → SBX → ES/WORM. Precisa de Model Gateway e de modelo que peça tools, o que este ambiente não tem (passo 10: `aos_mediation_* = 0`).

**Onde:** stack `deploy/node/dev-hardened` (ou produção). Segue [`ciclo-de-vida-manual.md`](ciclo-de-vida-manual.md), passos 6–11, e recolhe estas pegadas:

| Ponto (doc 19) | Pegada a recolher | Critério |
|---|---|---|
| F2E-01.1 manifesto pinado | `turn.recorded` com `model.model_id` **não-vazio** e `run.toolset.frozen` com `entries` (digest, assinatura, publisher) | `entries` não-vazio; `model_id` ≠ `""` |
| F2E-01.4–5 identidade + PDP | span `execute_tool`: `aos.decision`, `aos.decision.denied_by`, `aos.taint`, `aos.tool_call.hash` | `web_post` → `deny/policy` com `taint=untrusted`; `doc_read` → passa o PDP |
| P-02 canal durável (AOS-379) | `wal-summary` com `tool.call.mediated` / `tool.call.denied` / `tool.call.escalated` | contagens > 0, iguais às métricas |
| F2E-01.10 selagem | `audit-trail` da partição do run: `Decision`, `Capability`, `StepID`, cadeia `human → agente` | um registo por decisão, hash-chain íntegra |
| OBS | `aos_mediation_permits_total` / `_denials_total` / `_escalations_total` | > 0 e coerentes com o WORM |
| F2E-01.8 credencial JIT | banner `credential broker` | **fica por verificar** enquanto o banner disser `AUSENTE` (doc 19 §9) |
| F2E-01.9 SBX | resultado de `doc_read` executado no sandbox | `status=completed` sem timeout |

**Não corrido nesta verificação.** O resultado não se dá como verde por analogia.

---

## Passo 20 — Arrumar

Parar o nó (Ctrl-C) e, se quiseres repetir do zero:

```bash
rm -rf $E2E/state $E2E/tamper $E2E/orq/*.wal
```

As seeds em `$E2E/keys` são descartáveis e **não** servem para nenhum ambiente real.

---

## Síntese — pontos verificáveis

| # | Ponto (doc 19) | Pegada | Observado a 2026-09-15 | Classe |
|---|---|---|---|---|
| 1 | §4 postura dos componentes | banner | 61 linhas; ausentes declarados (GW, BRK, orçamento, OTLP, backup) | VIVO |
| 2 | P-01 #1 fencing no claim | SSE seq 1 | `ready→running token_value=1` | VIVO |
| 3 | S-01b checkpoints por fase | SSE seq 3,4,6,8 | `assembled, model_called, turn_recorded, verified` | VIVO |
| 4 | §1.4 idempotency key | todos os eventos | `run_id:step_id` sem excepção | VIVO |
| 5 | F2E-01.1 manifesto | `turn.recorded` | `prompt_hash`, `system_hash`, `assembly_version=1.3.0` | VIVO |
| 6 | F2E-01.11 captura | `replay.captured` | `sealed_content` cifrado | VIVO |
| 7 | contexto ≠ registo | grep WAL/WORM | email=0 / email=0; `[REDACTED:email]` | VIVO |
| 8 | anti-enumeração | HTTP | 404 ×3 idênticos; 403 na escrita | VIVO |
| 9 | leitura selada | WORM `gov.read/<run>` | 3 selos (5 após reconstruct ×2) | VIVO |
| 10 | F2E-03a steer/pause assinado | WAL + WORM | `control.pause/steer 1`; `governance.control` seq 1–2 | VIVO |
| 11 | emissor não pinado | HTTP | 403 `sinal recusado` ×2 idênticos | VIVO |
| 12 | P-04 subida a L4 sem prova | banner AOS-377 | recusada no arranque | VIVO |
| 13 | P-04 dual-control L4/L5 | HTTP + WORM | 1 assinatura → recusada; 2 → `actor=op:jimy,op:maria` | VIVO |
| 14 | anti-replay de assinatura | HTTP + WAL | 403 no 2.º envio; `ratification.nonce.consumed` | VIVO |
| 15 | DSAR crypto-shred | HTTP + WORM | 200 → 410; `dsar.received`, `dsar.key_destroyed` | VIVO |
| 16 | restart sem perda | banner + HTTP | WORM 8 partições verificadas; crash-resume 10 streams; L4 reidratado | VIVO |
| 17 | F2E-06.3 adulteração | exit + stderr | 1 byte → `DANO INTERIOR ... exit=1` | VIVO |
| 18 | F2E-05 promoção sem ratificador | HTTP + WORM | 403; `ratification-unratified` 2 × `deny` | VIVO |
| 19 | F2E-02 goal→DAG governado | stdout + WAL | 2 nós, papel spawnado, `nos_despachados=1` | VIVO |
| 20 | S-01a lease/fencing | stdout + WAL | token 1 → 2; re-hidratação `nos=2` | VIVO |
| 21 | F2E-02.4 validação fail-closed | stdout + WAL | `cycle`, `tool_unknown`; zero nós no WAL | VIVO |
| 22 | F2E-03b/c gates e card | `aos-demo` | escala `danger`; 2 aprovadores distintos | VIVO (demo) |
| 23 | F2E-01.3–10 mediação | system-test | `TestAOS169_…` PASS | TESTE |
| 24 | F2E-04 / P-03 | system-tests | RB-01, RB-03, aging, breaker PASS | TESTE |
| 25 | F2E-05 rollback | system-test | RB-05 PASS | TESTE |
| 26 | F2E-06 DR/replay | system-tests | fidelity 1, 0 duplicados, tamper/stale/cross-border abortam | TESTE |
| 27 | F2E-01 caminho quente vivo | spans + WAL + WORM | **não corrido** | SERVIDOR |

---

## Achados desta verificação

1. **`aos-issuer autonomy-sign` mistura o aviso no corpo** — *corrigido depois desta verificação pelo #298 (`39c0eeb`): o aviso passou para o stderr.* Em `8e88f88`, sem `--co-emitter`, para L4/L5, o `aos-issuer` escrevia `# aviso: …` no **stdout** (`packages/cmd/aos-issuer/autonomysign.go`). O corpo capturado com `$(...)` começava por `#`, e o nó respondia `400 corpo invalido` em vez do 403 explícito que o handler tem para esse caso (`packages/cmd/aos/autonomy_route.go`). O resultado continuava fail-closed; perdia-se só o diagnóstico. Passo 6c.
2. **Dependências do plano ausentes do log do `aos-orq --goal`.** O plano declara `analise depends_on recolha` e o despacho respeitou-o em memória (`nos_despachados=1`). Mas o WAL tem **zero** `task.edge.added`, e o `inspect` ordena `analise,recolha`. O `RebuildDAG` só repõe arestas a partir desse evento (`packages/control-plane/orchestrator/graph.go`). Está por investigar se um segundo dono pode despachar fora de ordem. Passo 15b.
3. **Doc 19 §9.1 desactualizado** — *corrigido no mesmo PR: o §9.1, a regra do P-04 e o §2 passam a reflectir o ADR-025.* O texto dizia que a demoção automática não vigorava (DEF-908). O banner deste commit declara `democao automatica por anomalia (AOS-090/DEF-908): LIGADA` e `promocao automatica por fiabilidade (AOS-090/ADR-025): LIGADA`. Passo 1.
4. **Evento citado no F2E-02 passo 1 sem emissão observada.** `plan.intake_classified` não aparece no WAL do `aos-orq`. O gate humano de plano (passo 5) continua fora do binário (§9.7, coerente). Passo 15.
5. **Código HTTP da reconstrução após shred.** Observado `410 reconstrucao indisponivel`; o precedente [`ciclo-de-vida-manual.md`](ciclo-de-vida-manual.md) (passo 10) documenta 404. Passo 11.
6. **`GET /runs/{id}` perde `final_text` e `turns` após DSAR e restart.** Não se isolou qual dos dois o causa; para isolar, repetir o restart num run sem erase. Passo 12.
7. **Recusas sem selo no WORM.** Os sinais de controlo recusados (passo 5), as mudanças de autonomia recusadas (6c, 6e) e as leituras soberanas negadas (passo 4) **não** deixam registo no WORM (`--denied-only` vazio). A promoção recusada **deixa** (passo 14). Fica registado como observação; não se classifica aqui se é postura desejada.
8. **Armadilha de ambiente (Git Bash).** Um base64 que começa por `/` é convertido em caminho. `MSYS_NO_PATHCONV=1` exportado para toda a shell parte os caminhos `/c/...`, e o nó recusa o bundle PDP fail-closed. Passo 14.

## Limites declarados

- **Sem modelo vivo:**
  - nenhuma tool call mediada ao vivo (passo 10 prova-o);
  - nenhum `escalate` real, e portanto nenhuma cerimónia `/approve` assinada;
  - nenhum `running→paused` gracioso.

  Estas partes estão cobertas por [TESTE] ou ficam em [SERVIDOR].
- **Credencial do leitor demo-grade** (headers `X-Aos-Reader`/`X-Aos-Board`, só fora de produção). Em produção é OIDC com anti-replay por `jti` (ver o precedente, passos 2 e 8).
- **KEK do DSAR em memória:** o shred é real no processo, mas a custódia não é durável (banner AOS-215/DEF-302).
- **WORM sem âncora assinada:** a truncatura da cauda não é detectável nesta configuração (passo 13).
- **Multi-réplica com arbitragem real** (`aos-orq --nats`, exit 3 por lease vivo de outro dono) exige NATS JetStream: ver [`PROC-DESPACHO-MULTIPROC.md`](../runbooks/PROC-DESPACHO-MULTIPROC.md). Aqui só se verificou a posse sequencial sobre ficheiro.
