# Teste E2E manual — pegadas de dados pela Visão End-to-End

| Campo | Valor |
|---|---|
| Referência | [`tecnica/19_Visao_End_to_End.md`](../../tecnica/19_Visao_End_to_End.md) (fluxos F2E-01…06, processos P-01…07, capacidades §5) |
| Tipo | Roteiro de verificação **manual**, passo a passo, **sem script**: cada passo é um comando isolado, com a saída real e o critério de passagem |
| Ambiente | Local: **Linux** x86_64 (kernel 6.18) + bash + Go 1.25.13 com `GOWORK=off`, **sem Docker**, sem modelo vivo. As corridas anteriores foram em Windows 11 + Git Bash + Go 1.27.1: as diferenças estão em [§0, Diferenças de ambiente](#diferenças-de-ambiente-linux-e-git-bash) |
| Verificado | **2026-10-02, commit `7b9a9ff`**, binários compilados na hora (passo 0.1), todos os passos 0–18 re-executados por esta ordem (AOS-479). Antes: 2026-09-15 em `8e88f88`; 2026-10-01 em `4ef35e0` e `f7b23f3` ([relatório](../reports/e2e-pegadas-bidireccional-2026-10-01.md)) |
| Precedente | [`ciclo-de-vida-manual.md`](ciclo-de-vida-manual.md) (stack `dev-hardened` com modelo real) e [`subprocessos-decomposicao.md`](subprocessos-decomposicao.md) |

---

## 0. Como ler este teste

Cada passo tem quatro partes:

- **Porquê**: o ponto do doc 19 que o passo verifica.
- **Comando**: o que se escreve à mão.
- **Pegada**: a saída **real** observada na verificação de 2026-10-02, sobre `7b9a9ff`.
- **Verificar**: o critério objectivo de passagem.

Os ids voláteis (ULIDs, nonces, assinaturas, timestamps, pubkeys, offsets dentro do WORM) mudam de corrida para corrida. As **formas**, os **tipos de evento**, as **partições** e as **contagens** não mudam. Nas pegadas, `…/` no início de um caminho é o valor de `$E2E` da corrida.

### Diferenças de ambiente (Linux e Git Bash)

O roteiro foi escrito em Git Bash e verificado agora em Linux. Os comandos são os mesmos; o que muda está aqui, e não escondido nas pegadas.

| Ponto | Linux (esta verificação) | Git Bash (verificações anteriores) |
|---|---|---|
| `xxd` | Tem de ser o `xxd` real (pacote `xxd`/`vim-common`): o passo 13 usa `-s`/`-l` e o passo 14 usa `-r -p`. Um *shim* que só faça `xxd -p` dá seeds certas no passo 0.2 e falha no 13 e no 14 | Vem com o Git for Windows |
| Caminhos | POSIX em todo o lado; as mensagens de erro do nó repetem o caminho absoluto que recebeu em `AOS_WORM_PATH` | O MSYS converte `/c/…` e `/tmp/…` para a forma Windows nos argumentos e nas variáveis de ambiente de binários nativos, **excepto** com `MSYS_NO_PATHCONV=1` (passo 14) |
| `cygpath` | Não existe; o passo 14 deixa a chave como está | Converte a chave para `C:/…` (passo 14) |
| `kill` de um nó em segundo plano (passo 13) | Paragem graciosa, `exit=0` | Não medido; o critério é o `readyz=200`, não o código de saída do `kill` |
| Corpo JSON das respostas HTTP | Termina em `\n`, pelo que `-w ' http=…'` sai na linha seguinte | Não medido agora; o `\n` é escrito pelo servidor, não pela shell. As pegadas de 2026-09-15 juntavam as duas linhas numa |

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
| F2E-06 / P-07 | Restart sobre o mesmo estado; WORM adulterado aborta; truncatura da cauda só aborta com a âncora armada; replay 100%; DR | 12, 13, 18d | VIVO + TESTE |

---

## Passo 0 — Preparação

### 0.1 Binários frescos

**Porquê:** uma pegada só vale se o binário corresponder ao código. Um binário velho dá tudo verde sobre código antigo.

```bash
E2E=/tmp/aos-e2e; mkdir -p $E2E/bin $E2E/keys $E2E/state $E2E/orq
git log -1 --format='%h %s'
for m in aos aos-issuer aos-orq aos-demo; do (cd packages/cmd/$m && GOWORK=off go build -o $E2E/bin/$m . ) && echo "ok $m"; done
```

(`GOWORK=off` resolve as dependências pelos `replace` de cada `go.mod`, como os gates de CI. Sem ele, o `go.work` da raiz dá o mesmo binário.)

**Pegada:**

```
7b9a9ff docs(AOS-479): corrige a afirmação do passo 15 em f7b23f3 e marca dois ACs já cumpridos
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
op-jimy=f5fa4ee65037c0cc7353b77a7c609078c9b8ab7449d3ef2230d8356c3a4c2beb
op-maria=e69419df03caec037fda72bd5a511e4aaab1fc104c902b24659024c990939934
ap-ana=149ae504a5d2bd7099ef30969260605732dd26584d28849f6959a9bdda813e8b
ap-bruno=3b249e3bc7e71d7169a2bde02e356a39ab9e49ace1a087a04fd731f38e4a90fc
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

**Pegada:** `readyz=200` e `{"status":"ok"}`. O banner tem **70** linhas (eram 61 a 2026-09-15; entraram, entre outras, as de AOS-417, AOS-427, AOS-428/433/435, AOS-436, AOS-439, AOS-446 e AOS-457). Estas são as que respondem ao doc 19:

| Código (doc 19 §4) | Linha do banner (excerto real) | Estado |
|---|---|---|
| GOV / identidade | `modo de IDENTIDADE: REAL (trust-anchor da autoridade AOS-156, issuer="iss:aos-node")` + `AVISO: MODO DE REFERENCIA single-process` | composto, com autoridade co-localizada |
| RT / canal de controlo | `canal de controlo: Ed25519Authenticator (AOS-160) — 2 operador(es) registado(s)` | composto |
| GOV / 4-eyes | `four-eyes gate (AOS-162) composto: 2 aprovador(es) pinado(s)` e attestation `DORMENTE` | estrutural |
| ES | `substrato: duravel em disco (AOS-170)` | composto |
| OBS / WORM | `tamper-evidence do WORM (AOS-221): hash-chain RE-ENCADEADA e verificada no arranque (0 particao(oes))` e `verificacao ancorada ... NAO ANCORADA` | hash-chain sim; âncora não |
| RT / durável | `execucao duravel (AOS-180): LIGADA — checkpointer + capturer + step-ledger` | composto |
| PDP | `PDP com BUNDLE CARREGADO ... politica em vigor versao "1.0.0"` e `changelog de politica (AOS-310): TRANSICAO SELADA ... -> 1.0.0` | composto e selado |
| GOV / âncoras de confiança | `ancoras de confianca (AOS-446 fase 1): SELADAS na particao "trust-anchors" — digest …` | composto e selado (nova partição; ver passo 8) |
| GOV / autonomia | `autonomia / escalate (AOS-087/AOS-248): ORACULO LIGADO — 1 par(es)` | composto |
| GOV / autonomia | `autonomia / provisionamento (AOS-377): ATENCAO — 1 subida(s) a L4/L5 declarada(s) em AOS_AUTONOMY_LEVELS RECUSADA(S) por falta de prova assinada valida [agt-1:fs=L4 ...]` | **recusa fail-closed** (ver passo 6) |
| GW | `modelo (EPIC-06): MODELO DE REFERENCIA (referenceModel) — ... sem tool calls, com um custo CONSTANTE de 1500 micro-USD` | ausente |
| SCH/ADM (orçamento) | `orcamento / tecto de custo (AOS-008): NAO COMPOSTO` | ausente |
| SCH/ADM (ingresso) | `ingresso / admission (AOS-166/AOS-277/AOS-458): LIGADO e nos DEFAULTS do binario ... 64 pedido(s)/segundo com burst de 128` | composto |
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
| 5 | `turn.recorded` | `step-000001` | `run-e2e-001:step-000001` | `manifest{prompt_hash=sha256:4e875ddd…35aa, system_hash=sha256:e3b0c442…b855, assembly_version=1.3.0, model{model_id=aos-reference-model, served_model_id=aos-reference-model, seed=0}} input_tokens=12 output_tokens=8 cost_micro_usd=1500 tool_calls_requested=0 final=true`, producer `nhi:demo` |
| 6 | `step.checkpoint` | `ckpt-turn_recorded-step-000001` | … | `phase=turn_recorded` |
| 7 | `replay.captured` | `cap-step-000001` (`parent_step_id=step-000001`) | `run-e2e-001:cap-step-000001` | `observed_at_unix_nano=1790939948480611447`, `sealed_content=eyJ3cmFwcGVkX2RlayI6…` (envelope DEK/KEK), `sealed_subject=nhi:demo` |
| 8 | `step.checkpoint` | `ckpt-verified-step-000001` | … | `phase=verified` |
| 9 | `run.state.transition` | `state-2` | `run-e2e-001:state-2` | `from=running to=complete reason=run_complete token_value=1` |

Evento real (seq 1, completo):

```json
{"event_id":"01M3Y5DSDY7G86EA8BK2GGQWJX","stream_id":"run-e2e-001","seq":1,"type":"run.state.transition","ts":"2026-10-02T11:19:08.47859228Z","producer":{"nhi_id":"","delegation_chain":null,"scope":null},"payload":{"from":"ready","to":"running","reason":"run_start_claim","token_value":1,"at":"2026-10-02T11:19:08.478552097Z"},"schema_version":"1.0","run_id":"run-e2e-001","step_id":"state-1","idempotency_key":"run-e2e-001:state-1"}
```

**Verificar** (cada linha é um ponto do doc 19):

- `seq` vai de 1 a 9 **sem buracos** (ES: ordenação total gapless por `(stream_id, seq)`).
- `idempotency_key == run_id + ":" + step_id` em **todos** os eventos (§1.4), com os domínios `state-N` e `ckpt-`.
- A transição 1 (`ready→running`) leva `token_value=1`. É a única transição com pré-condição de fencing (P-01, tabela, #1).
- As fases `assembled → model_called → turn_recorded → verified` estão por esta ordem. Não há `dispatched`, porque não houve tool calls (S-01b).
- O manifesto do `turn.recorded` tem `prompt_hash`, `system_hash`, `assembly_version` e `model{model_id, served_model_id, seed}` (F2E-01 passo 1). Desde o AOS-396, com o modelo de referência `model_id` e `served_model_id` são `aos-reference-model`; com o Model Gateway, `model_id` é o `AOS_MODEL_NAME` e `served_model_id` o modelo que o provider devolveu. `seed=0` (nada o envia) e `system_hash` é o SHA-256 da string vazia. O `prompt_hash` é o mesmo valor medido a 2026-09-15: depende só do objectivo redigido.
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
{"error":"not found"}
 http=404
{"error":"not found"}
 http=404
{"error":"not found"}
 http=404
{"error":"nao autorizado"}
 http=403
{"error":"run_id em falta"}
 http=400
```

Pela ordem: sem credencial, board de outra região, run inexistente, escrita sem credencial, submit sem `run_id`. (O `http=` sai na linha seguinte porque o corpo termina em `\n`.)

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
- Os dois sinais legítimos são aceites (`exit=0`).
- As duas recusas são **idênticas** (403 `sinal recusado`, `exit=1`).
- As recusas não gastam nonce nem selam: são recusadas na autenticação (ver o achado n.º 7).

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

**Pegada:**

```
{"actor":"op:jimy","agent":"agt-1","domain":"http","from":"L2","status":"applied","to":"L3"}
 http=200
```

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
{"agent":"agt-1","domain":"fs","emitter":{"id":"op:jimy","is
{"error":"mudar para L4/L5 exige duas assinaturas de operadores distintos com autonomy:set (co_emitter em falta)"}
 http=403
```

O aviso do `aos-issuer` (`# aviso: mudar para L4/L5 exige uma segunda assinatura (--co-emitter/--co-key-file); sem ela o no recusa (AOS-305)`) vai para o stderr, que o `2>/dev/null` deita fora; o corpo começa por `{"agent":…`.

**Verificar:**
- O nível **não muda** (403 `co_emitter em falta`).
- O nonce desta assinatura **foi gasto**. O pedido passa a autenticação do primeiro emissor, que consome o nonce, e só depois é recusado por falta do co-emissor. É deliberado (`packages/cmd/aos/autonomy_route.go:133-138`): a assinatura recusada fica gasta, e um replay dela nunca serve. Conta no `ratification.nonce.consumed` do passo 7.
- Histórico: em `8e88f88` o `aos-issuer` escrevia o aviso no stdout, dentro do corpo, e o nó respondia `400 corpo invalido` (ver [§Achados](#achados-desta-verificação), n.º 1, corrigido pelo #298).

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
{"actor":"op:jimy,op:maria","agent":"agt-1","domain":"fs","from":"(nao registado)","status":"applied","to":"L4"}
 http=200
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
{"actor":"op:jimy","agent":"agt-1","domain":"http","from":"L3","status":"applied","to":"L2"}
 http=200
{"error":"emissor nao autorizado"}
 http=403
```

**Verificar:** a segunda submissão do corpo idêntico é recusada, porque o nonce é de uso único e durável. No WAL, `ratification.nonce.consumed` sobe um por **assinatura que passa a autenticação**, e não por mudança aplicada: conta o 6c, recusado depois de gastar o nonce, e não conta este segundo envio, recusado na autenticação por nonce repetido (passo 7).

---

## Passo 7 — Durabilidade no Event Store (ADR-001, ADR-007)

**Porquê:** o estado vive no ES, não no processo. O `wal-summary` agrega por tipo e não nomeia streams, para não permitir enumeração.

```bash
$E2E/bin/aos wal-summary --path $E2E/state/es.wal
```

```bash
$E2E/bin/aos wal-count --path $E2E/state/es.wal --run run-e2e-001 --turns
```

**Pegada** (capturada depois do passo 6e; o passo 11 não muda nenhuma destas linhas):

```
streams 11
control.pause 1
control.steer 1
lease.claimed 1
memory.record.written 1
ratification.nonce.consumed 7
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
- `ratification.nonce.consumed 7` = 2 (pause e steer do passo 5) + 1 (6b) + 1 (**6c, recusado depois de gastar o nonce**) + 2 (6d, uma por emissor) + 1 (6e). As recusas do passo 5 e o segundo envio do 6e não gastam: falham na autenticação.
- Cada nonce consumido é um stream próprio (`ratify-nonce:…`): `streams 11` = 7 nonces + `run-e2e-001` + `lease:run-e2e-001` + `aos-internal/memory/episodic` + `aos-internal/gov/approvals`.

> Medido entre o 6c e o 6d: `streams 8` e `ratification.nonce.consumed 4`. A pegada de 2026-09-15, no mesmo ponto deste passo, era `streams 10` e `6`, um a menos em cada: em `8e88f88` o 6c não chegava a gastar o nonce ([relatório](../reports/e2e-pegadas-bidireccional-2026-10-01.md) §6).

---

## Passo 8 — Audit WORM por partição (ADR-010)

**Porquê:** cada decisão de governação fica selada numa hash-chain **por partição**, com `seq` gapless. A partição é a fronteira de encadeamento; não é o run id.

```bash
for p in autonomy governance.control policy gov.residency/run-e2e-001 gov.read/run-e2e-001 ingestion:run-e2e-001 gov.sovereignty.authority trust-anchors; do echo "### $p"; $E2E/bin/aos audit-trail --path $E2E/state/worm.log --run "$p"; done
```

```bash
for p in governance.control autonomy; do $E2E/bin/aos audit-trail --path $E2E/state/worm.log --run $p --denied-only; echo "$p --denied-only: exit=$?"; done
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
### trust-anchors
seq=1 allow tool=- cap=audit:trust-anchors code=trust_anchors.changed reason="primeiro registo desta particao — nao ha ancoras anteriores com que comparar"
---
governance.control --denied-only: exit=0
autonomy --denied-only: exit=0
```

**Verificar:**
- **8** partições (a `trust-anchors` é nova, AOS-446).
- `autonomy` tem 4 selos: provisionamento + 3 mudanças aceites. As recusas 6c e 6e não selaram (achado n.º 7).
- `--denied-only` não imprime nada, nem em `governance.control` nem em `autonomy`.
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
aos_slo_evaluations_total 0
aos_mediation_permits_total 0
aos_mediation_denials_total 0
aos_mediation_escalations_total 0
aos_mediation_record_failures_total 0
aos_runs_suspended 0
aos_worm_partitions 8
```

**Verificar:**
- `aos_worm_partitions 8` coincide com as 8 partições do passo 8.
- `aos_slo_evaluations_total` e `aos_approval_sweeps_total` contam passagens periódicas (1 min). Medido no primeiro minuto do nó: o primeiro vale `0` e o segundo ainda não aparece. Numa corrida mais lenta do mesmo dia, valiam `1` e `1`. Não contam para o critério.
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

Por fim, o mesmo `GET` do passo 2, **depois do erase e antes do restart** (é o controlo do achado n.º 6):

```bash
curl -s -H "X-Aos-Reader: nhi:demo" -H "X-Aos-Board: board:aos-demo" http://127.0.0.1:18180/runs/run-e2e-001
```

**Pegada:**

```
{"run_id":"run-e2e-001","turns":[{"turn":1,"step_id":"step-000001","text":"no `aos`: modelo de referencia (Model Gateway real = EPIC-06)","final":true}]}
 http=200
{"request_id":"","subject_id":"nhi:demo","status":"erased","blocked":false,"stores_shredded":["audit","step-ledger"],"received_seq":1,"outcome_seq":2}
 http=200
{"error":"reconstrucao indisponivel"}
 http=410
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
      1 "Partition":"trust-anchors"
---
{"run_id":"run-e2e-001","status":"completed","terminated":true,"final_text":"no `aos`: modelo de referencia (Model Gateway real = EPIC-06)","turns":1}
```

**Verificar:**
- `reconstruct` passa de 200 para **410**.
- `stores_shredded` nomeia `audit` e `step-ledger`.
- `governance.dsar` tem `received` e `key_destroyed`, sem o conteúdo.
- A 9.ª partição apareceu (`aos_worm_partitions 9` no `/metrics`).
- `gov.read` subiu para 5, porque os dois `reconstruct` são leituras seladas. O `GET` final é a 6.ª.
- Depois do erase, o `GET /runs` **ainda** traz `final_text` e `turns`: o DSAR, sozinho, não os tira (passo 12 e achado n.º 6).

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
[aos] tamper-evidence do WORM (AOS-221): hash-chain RE-ENCADEADA e verificada no arranque (9 particao(oes)) ...
[aos] changelog de politica (AOS-310): CONFIRMACAO SELADA no arranque na particao "policy" — policy.active 1.0.0 (content_hash bca999ac…b88a): a politica coincide com o ultimo selo ...
[aos] autonomia / reidratacao (AOS-307): 4 alteracao(oes) de nivel RELIDA(S) do WORM no arranque — um nivel posto por POST /autonomy SOBREVIVE ao reinicio ...
[aos] autonomia / reidratacao (AOS-307): 1 par(es) com nivel de OPERADOR PRESERVADO sobre o que AOS_AUTONOMY_LEVELS declara agora [agt-1:http=L2(env L2, inalterado desde o ultimo provisionamento)] ...
[aos] autonomia / reidratacao (AOS-307): 1 par(es) em que AOS_AUTONOMY_LEVELS MUDOU desde o ultimo provisionamento e por isso GANHOU a uma decisao de operador [agt-1:fs=L4(sem alteracao de nivel, era L4 por decisao de "op:jimy,op:maria")] ... a mudanca foi ela propria selada como config:node
[aos-service] crash-resume / varredura de arranque (AOS-253): CORREU sobre 11 stream(s) — 0 run(s) orfaos em `running` ...
{"run_id":"run-e2e-001","status":"completed","terminated":true}
{"pairs":[{"agent":"agt-1","domain":"fs","level":"L4"},{"agent":"agt-1","domain":"http","level":"L2"}],"unregistered_resolves_to":"L1"}
```

O banner do segundo arranque tem 73 linhas. Face ao passo 1: entram `estado de governacao RE-HIDRATADO do substrato duravel` e as três linhas de reidratação; a do `changelog` passa de `TRANSICAO` a `CONFIRMACAO`; o oráculo passa a `2 par(es)`; e sai a recusa AOS-377, porque o par `agt-1:fs` já está em L4 por decisão assinada.

**Verificar:**
- WORM verificado sobre **9** partições; o arranque de 1.ª vez verificou 0.
- Crash-resume sobre **11** streams (o mesmo número do `wal-summary`), com 0 órfãos.
- `agt-1:fs=L4`, posto por dual-control, **sobrevive** ao restart.
- Cada arranque acrescenta um selo em `policy` (confirmação), em `gov.sovereignty.authority` e em `trust-anchors` (`trust_anchors.active`, «ancoras inalteradas»).
- Este arranque acrescenta também `autonomy` seq=5: o `agt-1:fs=L4` do ambiente, recusado no 1.º arranque por falta de prova, é selado como provisionamento `config:node` sobre o L4 dos dois operadores, e o banner diz que `AOS_AUTONOMY_LEVELS MUDOU` sem o ambiente ter mudado. O nível não muda. Ver o achado n.º 9.
- O `GET /runs` já **não** traz `final_text` nem `turns`, que ainda trazia no fim do passo 11, depois do erase. A causa é o **restart**, não o DSAR: ver o achado n.º 6.

---

## Passo 13 — WORM adulterado aborta o arranque (F2E-06 passo 3)

**Porquê:** adulteração ⇒ **abort fail-closed antes de escrever**. Cada teste corre sobre uma **cópia** do estado, noutra porta, para não tocar no nó do passo 12. São quatro arranques, cada um com o seu par:

| Sub-passo | Cópia | Âncora | Esperado |
|---|---|---|---|
| 13a | 1 byte adulterado a meio | não | **aborta** |
| 13b | a mesma cópia, **sem** adulteração (controlo positivo) | não | arranca |
| 13c | último registo removido (truncatura da cauda) | não | arranca — **limite declarado** |
| 13d | a mesma truncatura | **armada** | **aborta**; sem a truncatura, arranca |

Os arranques que devem levantar a API correm em segundo plano: o comando espera 3 s, lê o `/readyz` e pára o nó.

### 13a — Um byte adulterado

```bash
mkdir -p $E2E/tamper $E2E/controlo && cp $E2E/state/worm.log $E2E/state/es.wal $E2E/tamper/ && cp $E2E/tamper/worm.log $E2E/tamper/es.wal $E2E/controlo/
```

```bash
OFF=$(grep -abo 'control_steer' $E2E/tamper/worm.log | head -1 | cut -d: -f1); echo "offset=$OFF"
```

Confirmar que `offset` **não está vazio**. Sem alvo, o `dd` não altera nada e o teste passa em vazio: aconteceu na primeira tentativa da verificação de 2026-09-15.

```bash
printf 'X' | dd of=$E2E/tamper/worm.log bs=1 seek=$((OFF+8)) conv=notrunc
```

```bash
cmp -l $E2E/controlo/worm.log $E2E/tamper/worm.log
```

```bash
AOS_API_ADDR=127.0.0.1:18182 AOS_DURABLE_EXECUTION=1 AOS_EVENTSTORE_PATH=$E2E/tamper/es.wal AOS_WORM_PATH=$E2E/tamper/worm.log $E2E/bin/aos serve; echo "exit=$?"
```

**Pegada:**

```
offset=9497
 9506 163 130
aos: aos: WORM durável (AOS-170) "…/tamper/worm.log": audit: DANO INTERIOR no WAL "…/tamper/worm.log" (particao "governance.control" audit_seq=2 offset=9330): CRC nao fecha — registo FISICAMENTE COMPLETO com validacao falhada, NAO e cauda rasgada de crash; a reabertura RECUSA em vez de truncar os registos integros seguintes (causa: bit-rot ou adulteracao). ...
exit=1
```

(O `dd` escreve também as suas três linhas de estatística no stderr.)

**Verificar:**
- `cmp` mostra exactamente **1 byte** diferente (`s`→`X`, octal `163`→`130`).
- O nó **não** levanta a API e sai com `exit=1`.
- A mensagem nomeia a partição (`governance.control`) e o `audit_seq=2` do registo atingido (o `control:steer` do passo 5).

### 13b — Controlo positivo: a mesma cópia, sem adulteração

Sem este par, o `exit=1` do 13a podia vir de outra coisa qualquer (ambiente mínimo, porta, lock). A cópia `controlo/` foi tirada da `tamper/` **antes** do `dd`.

```bash
AOS_API_ADDR=127.0.0.1:18183 AOS_DURABLE_EXECUTION=1 AOS_EVENTSTORE_PATH=$E2E/controlo/es.wal AOS_WORM_PATH=$E2E/controlo/worm.log $E2E/bin/aos serve > $E2E/controlo/serve.log 2>&1 & sleep 3; curl -s -o /dev/null -w 'readyz=%{http_code}\n' http://127.0.0.1:18183/readyz; kill $!; wait $!; echo "exit=$?"
```

```bash
grep -o 'tamper-evidence do WORM (AOS-221): hash-chain RE-ENCADEADA e verificada no arranque ([0-9]* particao(oes))' $E2E/controlo/serve.log
```

**Pegada:**

```
readyz=200
exit=0
tamper-evidence do WORM (AOS-221): hash-chain RE-ENCADEADA e verificada no arranque (9 particao(oes))
```

**Verificar:** a cópia íntegra arranca (`readyz=200`) e a cadeia verifica sobre as 9 partições. O `exit=0` é o da paragem graciosa pelo `kill`.

> Este arranque não traz operadores, aprovadores nem política, e o banner avisa que as **âncoras de confiança MUDARAM** face ao último arranque registado (AOS-446) e sela-o em `trust-anchors`. Não aborta: é o registo da mudança, não uma recusa.

### 13c — Truncatura da cauda, sem âncora: arranca (limite declarado)

Cada registo do WORM é `uint32(len) BE || JSON || uint32(crc32) BE` (`packages/platform/audit/filestore.go:30`). O ciclo abaixo percorre os registos e guarda o offset do último.

```bash
mkdir -p $E2E/trunc && cp $E2E/state/worm.log $E2E/state/es.wal $E2E/trunc/
```

```bash
F=$E2E/trunc/worm.log; OFF=0; ULT=0; N=0; TAM=$(stat -c %s $F); while [ $OFF -lt $TAM ]; do ULT=$OFF; OFF=$((OFF + 8 + 16#$(xxd -s $OFF -l 4 -p $F))); N=$((N+1)); done; echo "registos=$N ultimo_offset=$ULT"
```

```bash
tail -c +$((ULT+5)) $F | grep -ao '"AuditSeq":[0-9]*,"Partition":"[^"]*"'; truncate -s $ULT $F
```

Repetir o ciclo (o mesmo comando de cima) para confirmar que ficou um registo a menos. Depois:

```bash
AOS_API_ADDR=127.0.0.1:18184 AOS_DURABLE_EXECUTION=1 AOS_EVENTSTORE_PATH=$E2E/trunc/es.wal AOS_WORM_PATH=$E2E/trunc/worm.log $E2E/bin/aos serve > $E2E/trunc/serve.log 2>&1 & sleep 3; curl -s -o /dev/null -w 'readyz=%{http_code}\n' http://127.0.0.1:18184/readyz; kill $!; wait $!; echo "exit=$?"
```

```bash
grep -oE 'hash-chain RE-ENCADEADA e verificada no arranque \([0-9]+ particao\(oes\)\)|verificacao ancorada do WORM \(AOS-268/AOS-072\): NAO ANCORADA' $E2E/trunc/serve.log
```

**Pegada:**

```
registos=27 ultimo_offset=23223
"AuditSeq":7,"Partition":"gov.read/run-e2e-001"
registos=26 ultimo_offset=22218
readyz=200
exit=0
hash-chain RE-ENCADEADA e verificada no arranque (9 particao(oes))
verificacao ancorada do WORM (AOS-268/AOS-072): NAO ANCORADA
```

**Verificar:** o último selo (a leitura do passo 12) desapareceu e o nó **arranca** e declara a cadeia verificada. É o limite que o banner declara desde o passo 1 (`NAO ANCORADA`): a via sem chave do AOS-221 detecta mutação, remoção interna e inserção, mas uma cadeia truncada re-encadeia como íntegra.

### 13d — A mesma truncatura com a âncora armada: aborta

A defesa é a âncora assinada (AOS-268; deferimento DEF-268 só quanto à cadência da selagem). Arma-se com três variáveis, que só valem juntas: `AOS_WORM_TRUST_ANCHOR` (pubkey do selador), `AOS_WORM_CHECKPOINT_FILE` (as âncoras) e `AOS_WORM_EXPECTED_HEADS_FILE` (o piso por partição, guardado à parte). As âncoras produzem-se **fora do nó**, com a chave privada do selador, sobre uma cópia do WORM: `aos-issuer worm-seal`.

```bash
head -c 32 /dev/urandom | xxd -p | tr -d '\n' > $E2E/keys/selador.seed
```

```bash
mkdir -p $E2E/ancora && cp $E2E/state/worm.log $E2E/state/es.wal $E2E/ancora/
```

```bash
$E2E/bin/aos-issuer worm-seal --worm $E2E/ancora/worm.log --key-file $E2E/keys/selador.seed > $E2E/keys/worm-checkpoints.json; echo "exit=$?"
```

```bash
$E2E/bin/aos-issuer worm-seal --worm $E2E/ancora/worm.log --key-file $E2E/keys/selador.seed --heads > $E2E/keys/worm-heads.json; echo "exit=$?"; cat $E2E/keys/worm-heads.json
```

```bash
mkdir -p $E2E/ancora-ok $E2E/ancora-trunc && cp $E2E/ancora/worm.log $E2E/ancora/es.wal $E2E/ancora-ok/ && cp $E2E/ancora/worm.log $E2E/ancora/es.wal $E2E/ancora-trunc/
```

Primeiro, a âncora armada sobre a cópia **íntegra** (sem este controlo, um `exit=1` a seguir podia ser âncora mal armada):

```bash
AOS_API_ADDR=127.0.0.1:18185 AOS_DURABLE_EXECUTION=1 AOS_EVENTSTORE_PATH=$E2E/ancora-ok/es.wal AOS_WORM_PATH=$E2E/ancora-ok/worm.log AOS_WORM_TRUST_ANCHOR=$($E2E/bin/aos operator-pubkey --key $E2E/keys/selador.seed) AOS_WORM_CHECKPOINT_FILE=$E2E/keys/worm-checkpoints.json AOS_WORM_EXPECTED_HEADS_FILE=$E2E/keys/worm-heads.json $E2E/bin/aos serve > $E2E/ancora-ok/serve.log 2>&1 & sleep 3; curl -s -o /dev/null -w 'readyz=%{http_code}\n' http://127.0.0.1:18185/readyz; kill $!; wait $!; echo "exit=$?"
```

```bash
grep -o 'verificacao ancorada do WORM (AOS-268/AOS-072): ANCORADA em [0-9]* de [0-9]* particao(oes)' $E2E/ancora-ok/serve.log
```

Depois, a truncatura (o mesmo ciclo do 13c, sobre a outra cópia) e o arranque com a mesma âncora:

```bash
F=$E2E/ancora-trunc/worm.log; OFF=0; ULT=0; N=0; TAM=$(stat -c %s $F); while [ $OFF -lt $TAM ]; do ULT=$OFF; OFF=$((OFF + 8 + 16#$(xxd -s $OFF -l 4 -p $F))); N=$((N+1)); done; echo "registos=$N ultimo_offset=$ULT"; truncate -s $ULT $F
```

```bash
AOS_API_ADDR=127.0.0.1:18186 AOS_DURABLE_EXECUTION=1 AOS_EVENTSTORE_PATH=$E2E/ancora-trunc/es.wal AOS_WORM_PATH=$E2E/ancora-trunc/worm.log AOS_WORM_TRUST_ANCHOR=$($E2E/bin/aos operator-pubkey --key $E2E/keys/selador.seed) AOS_WORM_CHECKPOINT_FILE=$E2E/keys/worm-checkpoints.json AOS_WORM_EXPECTED_HEADS_FILE=$E2E/keys/worm-heads.json $E2E/bin/aos serve; echo "exit=$?"
```

**Pegada:**

```
aviso: sem --anterior, a verificacao das ancoras de confianca (AOS-446) NAO corre — nao ha selagem anterior com que comparar. O procedimento do operador passa sempre --anterior.
exit=0
aviso: sem --anterior, a verificacao das ancoras de confianca (AOS-446) NAO corre — nao ha selagem anterior com que comparar. O procedimento do operador passa sempre --anterior.
exit=0
{
  "autonomy": 5,
  "gov.read/run-e2e-001": 7,
  "gov.residency/run-e2e-001": 1,
  "gov.sovereignty.authority": 2,
  "governance.control": 5,
  "governance.dsar": 2,
  "ingestion:run-e2e-001": 1,
  "policy": 2,
  "trust-anchors": 2
}
readyz=200
exit=0
verificacao ancorada do WORM (AOS-268/AOS-072): ANCORADA em 9 de 9 particao(oes)
registos=27 ultimo_offset=23223
aos: aos: verificacao ancorada do WORM (AOS-268/AOS-072) — o no recusa servir um WORM nao-ancorado no arranque (particao "gov.read/run-e2e-001"): audit: intervalo alem do head da particao
exit=1
```

**Verificar:**
- A selagem ancora as 9 partições, com o piso de cada uma (`--heads`) num ficheiro à parte.
- Âncora armada e cópia íntegra: `readyz=200` e `ANCORADA em 9 de 9`.
- Âncora armada e cauda truncada: o nó **não** levanta a API, sai com `exit=1` e nomeia a partição que perdeu o registo (`gov.read/run-e2e-001`): a âncora sela o head 7, o ficheiro ficou com 6 (`audit.ErrRangeBeyondHead`).
- Par com o 13c: a mesma truncatura que passou sem âncora é apanhada com ela.

> **Limite que a âncora não fecha:** só prova até ao `audit_seq` selado. Um registo apendido **depois** da última selagem pode ser truncado sem a verificação o apanhar; cada selagem encolhe essa janela, que nunca fecha porque as partições nascem por run (banner AOS-268). O aviso `sem --anterior` é desta corrida: o procedimento de operador passa sempre a selagem anterior, para recusar selar sobre um WORM que recuou.

**Cobertura automática do mesmo par** (da raiz do repo; dispensa o nó):

```bash
(cd packages/cmd/aos && go test -count=1 -v -run 'TestNode_WORMAnchor_' .)
```

```bash
(cd packages/cmd/aos-issuer && go test -count=1 -v -run 'TestWormSealProduzAncoraQueONoACEITA|TestWormSealRecusaSelarSobreTruncatura' .)
```

**Pegada:**

```
--- PASS: TestNode_WORMAnchor_IntegroAncora
--- PASS: TestNode_WORMAnchor_TruncaturaDoTailDetectada       (sem_ancora_arranca + com_ancora_aborta)
--- PASS: TestNode_WORMAnchor_RollbackStaleRejeitado
--- PASS: TestNode_WORMAnchor_CheckpointForjadoRejeitado
--- PASS: TestWormSealProduzAncoraQueONoACEITA
--- PASS: TestWormSealRecusaSelarSobreTruncatura
```

`TestNode_WORMAnchor_TruncaturaDoTailDetectada` (`packages/cmd/aos/aos268_worm_anchor_test.go`) é o 13c e o 13d em teste; `TestWormSealRecusaSelarSobreTruncatura` (`packages/cmd/aos-issuer/wormseal_test.go`) prova que o selador recusa dar uma âncora válida a uma cópia já truncada.

---

## Passo 14 — Promoção sem ratificador é negada e selada (F2E-05 passo 5, P-05)

**Porquê:**
- Nenhuma auto-modificação chega a produção sem ratificação humana assinada contra a allowlist.
- Este nó arranca **sem ratificadores** (banner: `SEM RATIFICADORES — toda a promocao sera NEGADA (ratifier_unknown)`).
- A negação tem de ficar selada.

```bash
$E2E/bin/aos wal-summary --path $E2E/state/es.wal | grep nonce
```

```bash
H64=$(printf 'skill e2e v1' | sha256sum | cut -d' ' -f1 | xxd -r -p | base64 | tr -d '\n'); echo "$H64"
```

A chave entra pelo caminho em **forma Windows** quando há `cygpath` (Git Bash), e tal como está no resto (Linux, macOS):

```bash
K=$E2E/keys/ap-ana.seed; command -v cygpath >/dev/null && K=$(cygpath -m "$K"); echo "$K"
```

```bash
MSYS_NO_PATHCONV=1 $E2E/bin/aos-issuer ratify-sign --artifact-id skill:e2e-resumo --version 1.0.0 --content-hash "$H64" --ratifier human:ana --key-file "$K" --canary-passed > $E2E/promote.json; echo "exit=$?"; head -c 200 $E2E/promote.json; echo
```

```bash
curl -s -m 10 -w ' http=%{http_code}\n' -X POST -H 'Content-Type: application/json' --data-binary @$E2E/promote.json http://127.0.0.1:18180/promote
```

(repetir o `curl`)

```bash
$E2E/bin/aos audit-trail --path $E2E/state/worm.log --run ratification-unratified
```

```bash
$E2E/bin/aos wal-summary --path $E2E/state/es.wal | grep nonce
```

**Pegada:**

```
ratification.nonce.consumed 7
/s1WLSIShKT92QbIs+IFydGdm5DAOtKo/AJmg7EVx3I=
…/keys/ap-ana.seed
exit=0
{"artifact":{"id":"skill:e2e-resumo","kind":"skill","version":"1.0.0","content_hash":"/s1WLSIShKT92QbIs+IFydGdm5DAOtKo/AJmg7EVx3I=","canary_passed":true,"eval":{"suite":"","eval_id":"","dataset":"gold
{"error":"promocao recusada"}
 http=403
{"error":"promocao recusada"}
 http=403
seq=1 deny tool=governance.ratification cap=ratify:production
seq=2 deny tool=governance.ratification cap=ratify:production
ratification.nonce.consumed 7
```

Em Git Bash, a terceira linha sai em forma Windows (`C:/…/keys/ap-ana.seed`). **Não medido nesta verificação**, que correu em Linux: lá o `cygpath` não existe e a chave segue como está.

**Verificar:**
- 403 nas duas submissões.
- Partição `ratification-unratified` com **dois `deny`** selados. Ao contrário do canal de controlo (achado n.º 7), a rota de promoção sela também as decisões de recusa.
- `ratification.nonce.consumed` **não** sobe (7 → 7), porque o ratificador é recusado antes de consumir nonce.

> **Armadilha do Git Bash, em duas metades.**
> 1. Um base64 que começa por `/` é convertido em caminho Windows, e o `aos-issuer` recusa com `--content-hash tem de ser base64 nao-vazio`. Daí o `MSYS_NO_PATHCONV=1`, aplicado **só a esse comando**. Exportada para toda a shell, a variável estraga os caminhos `/c/...` do passo 1, e o nó recusa fail-closed: `falha ao carregar o bundle PDP ... E_POLICY_UNAVAILABLE`.
> 2. Com a conversão desligada, a **chave** também deixa de ser convertida: um `--key-file /c/…` chega ao binário nativo tal e qual, e o `aos-issuer` falha com `The system cannot find the path specified` (medido a 2026-10-01, [relatório](../reports/e2e-pegadas-bidireccional-2026-10-01.md) §6). Por isso a chave vai pelo `cygpath -m`, que dá `C:/…` com barras para a frente: o binário aceita-o e o MSYS não lhe toca.

---

## Passo 15 — Goal → plano → DAG → sub-agente, sob lease (F2E-02, S-01a)

**Porquê:** o F2E-02 corre no binário `aos-orq` (ADR-018/ADR-023 mantêm o ORQ/SCH fora do nó). O planeador é governado (NHI `agent:planner`); o plano é validado contra um snapshot **pinado**; nós com dependentes viram **papéis** spawnados; o despacho respeita `depends_on`, e a dependência fica no log como `task.edge.added`.

> **Re-medido a 2026-10-02 com o AOS-476 e o AOS-477 já na árvore** (blocos deste passo copiados do ficheiro e corridos por esta ordem). As pegadas 15a–15c abaixo são dessa corrida.

Ficheiros de entrada:

```bash
cat > $E2E/orq/snapshot.json <<'EOF'
{
  "hash": "sha256:snap-e2e",
  "tools": [
    {"name":"fs.read","version":"1.0.0","digest":"sha256:aaa","admissible":true,
     "sensitivity":"public","egress":"none","reversibility":"reversible","mutation":"none"},
    {"name":"http.post","version":"2.0.0","digest":"sha256:bbb","admissible":true,
     "sensitivity":"public","egress":"external","reversibility":"reversible","mutation":"mutates"}
  ]
}
EOF
```

> **`mutation` é obrigatório por tool (AOS-409).** Sem ele o `aos-orq` recusa carregar o snapshot e
> nomeia a tool (`capability sem o campo obrigatorio` … `mutation`). `none` só para quem não altera
> estado nenhum; uma tool `mutates` (ou `unknown`) conta como de efeito — um verificador não a pode
> pinar — e o nó que a usa deriva `danger`, com cartão humano.

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

### 15a — Posse, decomposição, gate de plano, materialização, despacho, handoff

```bash
cd $E2E/orq && ../bin/aos-orq serve --wal orq.wal --run run-e2e-orq --goal "recolher e analisar dados" --snapshot snapshot.json --decompose-fixture plano.json --worker p1 --release; echo "exit=$?"
```

**Pegada** (as três linhas de postura vão encurtadas com `…`; o compromisso e o sal da primeira linha são aleatórios por corrida e vão como padrão):

```
compromisso do objectivo: hmac-sha256:<64 hex> sal=<64 hex> (gerado aqui e so aqui: com o objectivo e este sal verifica-se o plan.proposed; sem o sal o compromisso nao se inverte)
substrato: ficheiro orq.wal — NÃO arbitra entre processos (DEF-282); posse SEQUENCIAL, uma instância de cada vez
posse: run=run-e2e-orq plano=run-e2e-orq-plan token=1 worker=p1
gate de aprovacao de plano (AOS-408, AOS-236): COMPOSTO — nivel L4 (danger exige decisao humana; lacuna de capacidade tambem, mas NADA a abre neste binario hoje — contrato, nao facto). A decisao vem por fora, assinada, com chave PINADA e autoridade por classe (`aos-orq decide`); o pendente e um FACTO no log. 4-eyes FRACO neste caminho: … 
executor de nos (AOS-413): NAO composto — o despacho marca os nos a correr e NADA os executa (defina AOS_ORQ_NODE_URL e o NHI do run em AOS_ORQ_NODE_CREDENTIAL_FILE)
orcamento do plano (AOS-434): raiz da arvore com tecto de 1073741824 tokens / 1073741824 micro-USD (POR OMISSAO — nenhuma das duas variaveis esta definida). …
grafo re-hidratado: nos=0
decomposto: objectivo -> plano de 2 nos (tentativas=1, planner_nhi=agent:planner)
gate de plano: APROVADO sem humano (nivel L4, sem nos de risco) plan_hash=sha256:f5d82c5096b62270d749a89e412ffdcd9c370ad6f51efae1f64314c5c8661cdd
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
strings orq.wal | grep -oE '"type":"(plan\.(proposed|validated|approved|materialized)|task\.node\.created|task\.edge\.added|task\.node\.state_changed)".{0,400}'
```

```bash
printf 'FORJADO=%s goal=%s task.edge.added=%s\n' "$(grep -ac FORJADO orq.wal)" "$(grep -ac 'recolher e analisar dados' orq.wal)" "$(grep -ac task.edge.added orq.wal)"
```

**Pegada:**

```
run=run-e2e-orq token_corrente=1 nos=2 ordem=recolha,analise
streams 3
lease.claimed 1
lease.released 1
plan.approved 1
plan.materialized 1
plan.proposed 1
plan.validated 1
task.edge.added 1
task.node.created 2
task.node.state_changed 1
"type":"plan.proposed",...,"payload":{"plan_id":"run-e2e-orq-plan","plan_hash":"sha256:f5d82c50…1cdd","planner_meta":{"model":"aos-orq/decompose","prompt_version":"1.3.0","capabilities_hash":"sha256:snap-e2e"},"attempt":1,"objective_commitment":"hmac-sha256:<o da linha 1 do 15a>"},"schema_version":"aos.planner.v1","run_id":"run-e2e-orq-plan","step_id":"planstep:proposed","idempotency_key":"run-e2e-orq-plan:planstep:proposed"}
"type":"plan.validated",...,"payload":{"plan_id":"run-e2e-orq-plan","plan_hash":"sha256:f5d82c50…1cdd","node_count":2,"budget_total":100,"max_depth":0,"max_fanout":0,"max_nodes":64,"snapshot_digest":"sha256:9d864689…278d"},...,"idempotency_key":"run-e2e-orq-plan:planstep:validated"}
"type":"plan.approved",...,"payload":{"plan_id":"run-e2e-orq-plan","plan_hash":"sha256:f5d82c50…1cdd","decision":"approved","decision_ref":"auto:autonomy:L4"},...,"idempotency_key":"run-e2e-orq-plan:planstep:decision:approved"}
"type":"task.node.created",...,"payload":{"run_id":"run-e2e-orq","task_id":"analise","state":"ready","priority":0,"tool_id":"fs.read","capability":"cap:tool:fs.read"},...,"idempotency_key":"run-e2e-orq:node:analise"}
"type":"task.node.created",...,"payload":{"run_id":"run-e2e-orq","task_id":"recolha","state":"ready","priority":0},...,"idempotency_key":"run-e2e-orq:node:recolha"}
"type":"task.edge.added",...,"payload":{"run_id":"run-e2e-orq","from":"recolha","to":"analise"},"schema_version":"1.0","run_id":"run-e2e-orq","step_id":"edge:recolha>analise","idempotency_key":"run-e2e-orq:edge:recolha>analise"}
"type":"plan.materialized",...,"payload":{"plan_id":"run-e2e-orq-plan","plan_hash":"sha256:f5d82c5096b62270d749a89e412ffdcd9c370ad6f51efae1f64314c5c8661cdd","nodes":[{"node_id":"analise","kind":"leaf","tools":["cap:tool:fs.read"]},{"node_id":"recolha","kind":"role","tools":["cap:tool:fs.read"]}]},"schema_version":"aos.plan…
"type":"task.node.state_changed",...,"payload":{"run_id":"run-e2e-orq","task_id":"recolha","from":"ready","to":"running"},...,"idempotency_key":"run-e2e-orq:node-st:recolha:running"}
FORJADO=0 goal=0 task.edge.added=1
```

### 15c — Segundo dono: re-hidratação e fencing token monotónico

```bash
../bin/aos-orq serve --wal orq.wal --run run-e2e-orq --worker p2 --release; echo "exit=$?"
```

**Pegada:**

```
substrato: ficheiro orq.wal — NÃO arbitra entre processos (DEF-282); posse SEQUENCIAL, uma instância de cada vez
posse: run=run-e2e-orq plano=run-e2e-orq-plan token=2 worker=p2
grafo re-hidratado: nos=2
grafo re-hidratado: arestas=1 ordem=recolha,analise
posse largada: run=run-e2e-orq token=2 (reclamavel JA, sem esperar TTL)
exit=0
```

Depois disto, `inspect` dá `token_corrente=2 nos=2 ordem=recolha,analise` e `wal-summary` mostra `lease.claimed 2` e `lease.released 2`, e o resto igual. O segundo dono **não despacha nada**: sem `--plan-doc` não tem o documento de que o despacho precisa (ver o achado n.º 2 e DEF-817).

**Verificar** (F2E-02 e S-01a, ponto a ponto):

- A posse vem **antes** de qualquer escrita, com `token=1` e `ttl_nanos=30000000000` no `lease.claimed`.
- `planner_nhi=agent:planner` (F2E-02 passo 3).
- O gate de plano está **composto** (AOS-408, F2E-02 passo 5) e o plano passa por `plan.proposed` → `plan.validated` → `plan.approved`, todos com o mesmo `plan_hash`. Aqui aprova sem humano (`decision_ref=auto:autonomy:L4`): o nível do processo é L4 e o plano não tem nós de risco (as duas tools do plano são `fs.read`, `mutation=none`). Um nó `danger` ficaria pendente, como facto no log, à espera de uma decisão assinada (`aos-orq decide`).
- A `planner_meta` do `plan.proposed` é a do `aos-orq` (`aos-orq/decompose`, `capabilities_hash=sha256:snap-e2e`), não a do fixture: `FORJADO=0`.
- O `oraculo=snapshot(sha256:snap-e2e)`: o hash é o do snapshot pinado, não o `sha256:FORJADO` do fixture (F2E-02 passo 4).
- `recolha` tem um dependente e vira `kind=role`, spawnado **no despacho**; `analise` é `leaf` (F2E-02 passo 6, ADR-024).
- `nos_despachados=1`: `analise` espera por `recolha`, e só `recolha` passa `ready→running` (F2E-02 passo 7).
- `plan.materialized` traz `plan_hash`. Tanto os eventos do plano como os nós e a aresta têm `idempotency_key` `run:step`.
- **Um `task.edge.added` `recolha → analise`**, escrito depois dos dois `task.node.created` e antes do `plan.materialized` (a ordem das linhas do 15b é a do ficheiro). O `inspect`, que só tem o log, ordena `recolha,analise` (AOS-476).
- O segundo dono recebe `token=2` (monotónico) e re-hidrata `nos=2` **e** `arestas=1 ordem=recolha,analise` **do log**, não de memória (S-01a).

> **Achado n.º 2 — a metade do AOS-476 está fechada; a do AOS-477 tem o seu próprio passo.**
> - *Fechado pelo AOS-476:* a dependência `recolha → analise` está no log (`task.edge.added 1`) e o `inspect` ordena `recolha,analise`. Em `7b9a9ff` havia zero arestas e a ordem era `analise,recolha`. Ressalva: um dono seguinte só **despacha** com o documento do plano (`consume` ou `serve --plan-doc`); o `serve` sem documento do 15c re-hidrata e pára (DEF-817);
> - o objectivo (`recolher e analisar dados`) tem **0** ocorrências no WAL: o `plan.proposed` leva `plan_hash` e `planner_meta`, não o objectivo, e a ligação pedido → plano → run faz-se pela convenção de nomes (`<run>-plan`) (AOS-477).
>
> Continua a não aparecer `plan.intake_classified` (F2E-02 passo 1; achado n.º 4).

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

Pegada das duas recusas, sem as linhas de postura que o 15a já mostrou (`substrato`, `posse … token=1`, gate de plano, executor, orçamento, `grafo re-hidratado: nos=0`):

```
aos-orq: decomposição do objectivo: planner: plano recusado pela validacao estrutural apos esgotar as tentativas: acyclicity/cycle
exit=9
aos-orq: decomposição do objectivo: planner: plano recusado pela validacao estrutural apos esgotar as tentativas: tool_resolution/tool_unknown
exit=9
streams 1
lease.claimed 1
lease.released 1
streams 1
lease.claimed 1
lease.released 1
```

**Verificar:**
- `exit=9` (`exitPlanoRecusado`, terminal: o planeador esgotou as tentativas e não se retenta) com o diagnóstico `acyclicity/cycle` ou `tool_resolution/tool_unknown`. A 2026-09-15 era `exit=1`, com uma linha `decomposto:` antes da recusa; a recusa passou para dentro da decomposição.
- O WAL de cada recusa tem **só** a posse (`lease.claimed` + `lease.released`): zero `plan.*`, zero `task.node.created`.

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

**Onde:** stack `deploy/node/dev-hardened` (para exercer) ou produção (só para ler o que já lá está). No `dev-hardened`, segue [`ciclo-de-vida-manual.md`](ciclo-de-vida-manual.md), passos 6–11, e recolhe estas pegadas:

| Ponto (doc 19) | Pegada a recolher | Critério |
|---|---|---|
| F2E-01.1 manifesto pinado | `turn.recorded` com `model.model_id` **não-vazio** e `run.toolset.frozen` com `entries` (digest, assinatura, publisher) | `entries` não-vazio; `model_id` ≠ `""` |
| F2E-01.4–5 identidade + PDP | span `execute_tool`: `aos.decision`, `aos.decision.denied_by`, `aos.taint`, `aos.tool_call.hash` | `web_post` → `deny/policy` com `taint=untrusted`; `doc_read` → passa o PDP |
| P-02 canal durável (AOS-379) | `wal-summary` com `tool.call.mediated` / `tool.call.denied` / `tool.call.escalated` | contagens > 0, iguais às métricas |
| F2E-01.10 selagem | `audit-trail` da partição do run: `Decision`, `Capability`, `StepID`, cadeia `human → agente` | um registo por decisão, hash-chain íntegra |
| OBS | `aos_mediation_permits_total` / `_denials_total` / `_escalations_total` | > 0 e coerentes com o WORM |
| F2E-01.8 credencial JIT | banner `credential broker` | **fica por verificar** enquanto o banner disser `AUSENTE` (doc 19 §9) |
| F2E-01.9 SBX | resultado de `doc_read` executado no sandbox | `status=completed` sem timeout |

### 19a — Via de leitura em produção: cópia `:ro` e análise local

Em produção **não se submete nada**: lê-se o que os runs reais deixaram. A via é tirar uma cópia dos ficheiros por um contentor descartável que monta os volumes **só de leitura**, trazê-la para fora do servidor e analisá-la com os binários do passo 0.1. É o mesmo padrão do `deploy/server/backup.sh` (`-v aos_aos-data:/aos:ro`). Os volumes e os caminhos são os do `deploy/server/docker-compose.prod.yml`: `aos_aos-data` em `/var/lib/aos` (`events.wal`, `worm.wal`, `model-audit.wal`) e `aos_aos-orq-data` em `/var/lib/aos-orq` (`consume.wal`, do `aos-orq consume`).

No servidor:

```bash
mkdir -p ~/aos-leitura && docker run --rm --network none -v aos_aos-data:/aos:ro -v aos_aos-orq-data:/orq:ro -v ~/aos-leitura:/out alpine:3.20 sh -c 'cp /aos/events.wal /aos/worm.wal /aos/model-audit.wal /orq/consume.wal /out/ && ls -l /out'
```

Na máquina local, depois de copiar `~/aos-leitura` para `$E2E/prod` (por `scp`, ou pelo caminho que o operador usa para os backups):

```bash
$E2E/bin/aos wal-summary --path $E2E/prod/events.wal
```

```bash
$E2E/bin/aos wal-summary --path $E2E/prod/consume.wal
```

```bash
$E2E/bin/aos audit-trail --path $E2E/prod/worm.wal --run <particao>
```

As partições do WORM leem-se com `strings $E2E/prod/worm.wal | grep -o '"Partition":"[^"]*"' | sort | uniq -c`, como no passo 11, e os payloads com `strings … | grep -o '"type":"tool.call.mediated".\{0,600\}'`. As ferramentas de inspecção abrem o ficheiro sem o truncar nem lhe escrever (AOS-347 no Event Store, AOS-373 no WORM); mesmo assim, trabalha-se sempre sobre a cópia.

> **Não contes dentro do contentor.** O `grep -a -o` do busybox, corrido no servidor sobre o volume, **subconta**: deu 8 `tool.call.mediated` e 3 `run.state.transition` onde o mesmo ficheiro, copiado e analisado localmente, tem 52 e 202 ([relatório](../reports/e2e-pegadas-bidireccional-2026-10-01.md) §7, n.º 9; [PROD-LEITURA] de 2026-10-01, não repetido nesta verificação). Contagens feitas dentro do contentor não servem de prova; as que valem são as do `aos wal-summary` sobre a cópia.

Limites desta via:
- Lê-se o passado: as pegadas são as do run escolhido, de uma versão que pode ser anterior à imagem que corre hoje.
- O objectivo dos pedidos de plano está **cifrado por titular** (`objective_sealed`); a cadeia prova que existe e quem o submeteu, não o texto.
- Métricas e spans não estão nos ficheiros: lê-los exige um contentor na rede `aos_default` ou o colector OTLP.
- Use binários do **mesmo commit da imagem, ou posteriores**: um binário mais antigo pode não ler o esquema do WORM que a imagem escreve.

**Não corrido nesta verificação** (nem o `dev-hardened`, nem a leitura de produção). O resultado não se dá como verde por analogia. A última leitura de produção está no [relatório de 2026-10-01](../reports/e2e-pegadas-bidireccional-2026-10-01.md) §4.

---

## Passo 20 — Arrumar

Parar o nó (Ctrl-C) e, se quiseres repetir do zero:

```bash
rm -rf $E2E/state $E2E/tamper $E2E/controlo $E2E/trunc $E2E/ancora $E2E/ancora-ok $E2E/ancora-trunc $E2E/orq/*.wal
```

As seeds em `$E2E/keys` (incluindo a do selador do passo 13d e as âncoras que ela assinou) são descartáveis e **não** servem para nenhum ambiente real.

---

## Síntese — pontos verificáveis

| # | Ponto (doc 19) | Pegada | Observado a 2026-10-02 (`7b9a9ff`, Linux) | Classe |
|---|---|---|---|---|
| 1 | §4 postura dos componentes | banner | 70 linhas; ausentes declarados (GW, BRK, orçamento, OTLP, backup) | VIVO |
| 2 | P-01 #1 fencing no claim | SSE seq 1 | `ready→running token_value=1` | VIVO |
| 3 | S-01b checkpoints por fase | SSE seq 3,4,6,8 | `assembled, model_called, turn_recorded, verified` | VIVO |
| 4 | §1.4 idempotency key | todos os eventos | `run_id:step_id` sem excepção | VIVO |
| 5 | F2E-01.1 manifesto | `turn.recorded` | `prompt_hash`, `system_hash`, `assembly_version=1.3.0`, `model_id=served_model_id=aos-reference-model` | VIVO |
| 6 | F2E-01.11 captura | `replay.captured` | `sealed_content` cifrado | VIVO |
| 7 | contexto ≠ registo | grep WAL/WORM | email=0 / email=0; `[REDACTED:email]` | VIVO |
| 8 | anti-enumeração | HTTP | 404 ×3 idênticos; 403 na escrita | VIVO |
| 9 | leitura selada | WORM `gov.read/<run>` | 3 selos (5 após reconstruct ×2) | VIVO |
| 10 | F2E-03a steer/pause assinado | WAL + WORM | `control.pause/steer 1`; `governance.control` seq 1–2 | VIVO |
| 11 | emissor não pinado | HTTP | 403 `sinal recusado` ×2 idênticos, `exit=1` | VIVO |
| 12 | P-04 subida a L4 sem prova | banner AOS-377 | recusada no arranque | VIVO |
| 13 | P-04 dual-control L4/L5 | HTTP + WORM | 1 assinatura → 403 (nonce gasto); 2 → `actor=op:jimy,op:maria` | VIVO |
| 14 | anti-replay de assinatura | HTTP + WAL | 403 no 2.º envio; `ratification.nonce.consumed 7` | VIVO |
| 15 | DSAR crypto-shred | HTTP + WORM | 200 → 410; `dsar.received`, `dsar.key_destroyed` | VIVO |
| 16 | restart sem perda | banner + HTTP | WORM 9 partições verificadas; crash-resume 11 streams; L4 reidratado | VIVO |
| 17 | F2E-06.3 adulteração | exit + stderr | 1 byte → `DANO INTERIOR ... exit=1`; a mesma cópia íntegra → `readyz=200` | VIVO |
| 18 | F2E-06.3 truncatura da cauda | exit + banner | sem âncora → `readyz=200` (limite declarado); com âncora → `exit=1`, `intervalo alem do head`; âncora sobre cópia íntegra → `ANCORADA em 9 de 9` | VIVO + TESTE |
| 19 | F2E-05 promoção sem ratificador | HTTP + WORM | 403; `ratification-unratified` 2 × `deny`; nonces 7 → 7 | VIVO |
| 20 | F2E-02 goal→DAG governado | stdout + WAL | gate de plano composto (`plan.proposed/validated/approved`); 2 nós, papel spawnado, `nos_despachados=1` | VIVO |
| 21 | S-01a lease/fencing | stdout + WAL | token 1 → 2; re-hidratação `nos=2` | VIVO |
| 22 | F2E-02.4 validação fail-closed | stdout + WAL | `acyclicity/cycle`, `tool_resolution/tool_unknown`, `exit=9`; zero nós no WAL | VIVO |
| 23 | F2E-03b/c gates e card | `aos-demo` | escala `danger`; 2 aprovadores distintos | VIVO (demo) |
| 24 | F2E-01.3–10 mediação | system-test | `TestAOS169_…` PASS | TESTE |
| 25 | F2E-04 / P-03 | system-tests | RB-01, RB-03, aging, breaker PASS | TESTE |
| 26 | F2E-05 rollback | system-test | RB-05 PASS | TESTE |
| 27 | F2E-06 DR/replay | system-tests | fidelity 1, 0 duplicados, tamper/stale/cross-border abortam | TESTE |
| 28 | F2E-01 caminho quente vivo | spans + WAL + WORM | **não corrido**; via de leitura de produção descrita (passo 19a) | SERVIDOR |

---

## Achados desta verificação

Os achados 1 a 8 vêm da verificação de 2026-09-15 e estão actualizados com o que se mediu a 2026-10-02; o 9 é novo.

1. **`aos-issuer autonomy-sign` mistura o aviso no corpo** — *corrigido pelo #298 (`39c0eeb`): o aviso passou para o stderr; confirmado a 2026-10-02.* Em `8e88f88`, sem `--co-emitter`, para L4/L5, o `aos-issuer` escrevia `# aviso: …` no **stdout** (`packages/cmd/aos-issuer/autonomysign.go`). O corpo capturado com `$(...)` começava por `#`, e o nó respondia `400 corpo invalido` em vez do 403 explícito que o handler tem para esse caso (`packages/cmd/aos/autonomy_route.go`). O resultado continuava fail-closed; perdia-se só o diagnóstico. Passo 6c.
2. **Dependências do plano ausentes do log do `aos-orq --goal`** — *arestas: fechado pelo AOS-476 (re-medido a 2026-10-02); ligação pedido → plano → run: AOS-477.* O plano declara `analise depends_on recolha` e o despacho respeitava-o só em memória (`nos_despachados=1`): em `7b9a9ff` o WAL tinha **zero** `task.edge.added` e o `inspect` ordenava `analise,recolha`, porque o `RebuildDAG` só repõe arestas a partir desse evento (`packages/control-plane/orchestrator/graph.go`). Com o AOS-476 a materialização escreve um `task.edge.added` por aresta de entrada, depois dos nós e antes do `plan.materialized`; o 15b mostra `task.edge.added 1` e `ordem=recolha,analise`, e o segundo dono do 15c re-hidrata a aresta. **Ressalva:** a pergunta que o achado deixava em aberto («um segundo dono pode despachar fora de ordem?») tem resposta medida: não, e um segundo dono **sem documento** não despacha nada, re-hidrata e pára. A retoma do despacho existe pelo `consume` ou por `serve --plan-doc` com o documento ancorado no `plan.validated`, que levam o plano ao fim (`TestAOS476_DonoSeguinteDespachaSoPelaRetoma`). A via sem documento fica registada como DEF-817; a morte por TTL sobre `--nats` e a pasta `--plan-dir` partilhada não estão verificadas. O objectivo em claro continua a não ficar no WAL (0 ocorrências; AOS-477). Passos 15b e 15c.
3. **Doc 19 §9.1 desactualizado** — *corrigido no mesmo PR de 2026-09-15: o §9.1, a regra do P-04 e o §2 passam a reflectir o ADR-025.* O texto dizia que a demoção automática não vigorava (DEF-908). O banner declara `democao automatica por anomalia (AOS-090/DEF-908): LIGADA` e `promocao automatica por fiabilidade (AOS-090/ADR-025): LIGADA`. Passo 1.
4. **Evento citado no F2E-02 passo 1 sem emissão observada.** `plan.intake_classified` continua sem aparecer no WAL do `aos-orq`. A metade que dizia que o gate humano de plano estava fora do binário **deixou de valer**: desde o AOS-408 o `aos-orq` declara o gate composto e emite `plan.proposed`, `plan.validated` e `plan.approved` (passo 15). Passo 15.
5. **Código HTTP da reconstrução após shred.** Observado `410 reconstrucao indisponivel`; o precedente [`ciclo-de-vida-manual.md`](ciclo-de-vida-manual.md) (passo 10) documenta 404. Passo 11.
6. **`GET /runs/{id}` perde `final_text` e `turns` depois do restart: a causa é o restart, não o DSAR.** Isolado em duas medições. (a) No roteiro: depois do erase e **antes** do restart, o `GET` ainda os traz (fim do passo 11); depois do restart, já não (passo 12). (b) Num nó à parte, sem DSAR nenhum: dois runs, de `nhi:demo` e de `nhi:auditor`, trazem `final_text` e `turns` e o `reconstruct` do segundo dá 200; reiniciado o nó sobre o mesmo estado, os dois perdem-nos e o `reconstruct` do `nhi:auditor`, que nunca foi apagado, passa a **410**. Com a custódia de referência a KEK de cada titular vive em **memória** do processo (banner `custodia da KEK (AOS-215/DEF-302): … as KEK vivem em MEMORIA do processo, NAO-duraveis (perdem-se no restart)`): reiniciar equivale a um crypto-shred de **todos** os titulares. É o deferimento DEF-302 (custódia externa por injectar; em produção, o Vault). Sem ticket de engenharia. Passos 11 e 12.
   *Observação, não investigada:* entre o erase e o restart, o `GET /runs` continua a servir o `final_text` de um titular já apagado. Não se classifica aqui se isso cumpre o Art. 17.
7. **Recusas sem selo no WORM: decisão registada, não lacuna.** Os sinais de controlo recusados (passo 5), as mudanças de autonomia recusadas (6c, 6e) e as leituras soberanas negadas (passo 4) **não** deixam registo no WORM (`--denied-only` vazio em `governance.control` e em `autonomy`). O canal de controlo (pause, steer, autonomia) sela por `sealControlAction`, e o porquê está escrito no código: «Só se selam acções que SURTIRAM EFEITO. Um sinal recusado (assinatura inválida, replay, alvo errado) não muda estado nenhum e não entra na cadeia — mesmo critério da decisão de exaustão. Selá-los daria a quem inunda o canal um vector para inchar o trilho.» (`packages/cmd/aos/control_seal.go:64-66`). O único rasto do 6c é o nonce consumido no WAL (passo 7). A promoção recusada **deixa** selo (passo 14): é outra rota, que sela todas as decisões terminais. Sem ticket de engenharia.
8. **Armadilha de ambiente (Git Bash).** Um base64 que começa por `/` é convertido em caminho; `MSYS_NO_PATHCONV=1` exportado para toda a shell parte os caminhos `/c/...`, e o nó recusa o bundle PDP fail-closed; e, aplicado a um só comando, deixa de converter também o `--key-file`, que tem de ir em forma Windows (`cygpath -m`). Passo 14.
9. **A reidratação reatribui o par ao `config:node`** (novo; [relatório](../reports/e2e-pegadas-bidireccional-2026-10-01.md) §7, n.º 6, reproduzido a 2026-10-02). No segundo arranque, o `agt-1:fs=L4` do ambiente (recusado no primeiro por falta de prova) é selado como provisionamento com `actor=config:node` (`autonomy` seq=5), e o banner diz que `AOS_AUTONOMY_LEVELS MUDOU desde o ultimo provisionamento`, com o mesmo ambiente. O nível não muda (L4), mas o último selo do par deixa de ser o dos dois operadores. Não se investigou o efeito num terceiro arranque. Sem ticket. Passo 12.

## Limites declarados

- **Sem modelo vivo:**
  - nenhuma tool call mediada ao vivo (passo 10 prova-o);
  - nenhum `escalate` real, e portanto nenhuma cerimónia `/approve` assinada;
  - nenhum `running→paused` gracioso.

  Estas partes estão cobertas por [TESTE] ou ficam em [SERVIDOR].
- **Credencial do leitor demo-grade** (headers `X-Aos-Reader`/`X-Aos-Board`, só fora de produção). Em produção é OIDC com anti-replay por `jti` (ver o precedente, passos 2 e 8).
- **KEK do DSAR em memória:** o shred é real no processo, mas a custódia não é durável, e um restart apaga as KEKs de todos os titulares (banner AOS-215/DEF-302; achado n.º 6).
- **WORM do nó principal sem âncora assinada:** a truncatura da cauda não é detectável nessa configuração (passo 13c). A âncora só se arma nas cópias do passo 13d, e só prova até ao último `audit_seq` selado.
- **Multi-réplica com arbitragem real** (`aos-orq --nats`, exit 3 por lease vivo de outro dono) exige NATS JetStream: ver [`PROC-DESPACHO-MULTIPROC.md`](../runbooks/PROC-DESPACHO-MULTIPROC.md). Aqui só se verificou a posse sequencial sobre ficheiro.
- **Ambiente:** esta verificação correu em Linux. O que é específico do Git Bash (passo 14, `cygpath`) está documentado mas não foi medido agora.
