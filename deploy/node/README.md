# Empacotamento do nó `aos` (AOS-168 / ADR-017)

Imagem **distroless, non-root, root-fs read-only** do nó de referência. A fronteira de
supply-chain é ADR-017 (FIXA na Carta §4.1). Pontos **1/2/4 impostos**, ponto **3 mínimo**
(SBOM + proveniência geradas; **assinatura DEFERIDA para EPIC-10** — declarado, não fingido),
ponto **5 respeitado** (a chave do issuer **nunca** entra na imagem).

## Construir

O contexto de build é a **raiz do repo** (os 42 `replace ... => ../../` de
`packages/cmd/aos/go.mod` são relativos; a árvore `packages/` inteira tem de estar no contexto):

```bash
docker build -f deploy/node/Dockerfile -t aos-node:local .
```

Build **reprodutível/offline**: o `go build` corre com `CGO_ENABLED=0 GOPROXY=off -trimpath`.
O único passo de rede é `go mod download` (prime do cache, verificado contra o `go.sum` pinado —
o projecto não vendoriza por opção; ver `scripts/ci/cache-prime.sh`).

## Correr — root-fs READ-ONLY + estado durável em volume

O root-fs é read-only; o **estado durável** (Event Store / WORM, AOS-170), quando ligado,
escreve num **volume gravável EXPLÍCITO**, nunca no root-fs:

```bash
docker run --rm \
  --read-only \
  --tmpfs /tmp \
  -v aos-data:/var/lib/aos \
  -e AOS_API_ADDR=127.0.0.1:8080 \
  -p 8080:8080 \
  aos-node:local
```

O arranque de referência corre **in-memory** (não escreve no root-fs), pelo que roda limpo sob
`--read-only`. A durabilidade é opt-in e exige o **mount explícito** `-v aos-data:/var/lib/aos`.

> A imagem **não declara `VOLUME`** de propósito: sem ele, uma tentativa de durabilidade sob
> `--read-only` **sem** `-v` falha **visivelmente** em vez de escrever num volume anónimo órfão —
> a verificação de root-fs read-only não é mascarada. O directório `/var/lib/aos` é criado owned
> por `65532:65532`, pelo que o volume nomeado do operador herda a ownership certa.

### Estado durável — variáveis de ambiente (AOS-170 / AOS-180)

| Variável | Default | Efeito |
|---|---|---|
| `AOS_EVENTSTORE_PATH` | *(vazio)* | **Vazio ⇒ Event Store IN-MEMORY** (volátil: perde tudo quando o processo/contentor morre). Definido ⇒ Event Store **durável** (WAL append-only + `fsync` + replay crash-safe no arranque) no caminho dado. **Tem de apontar para DENTRO do mount gravável** (`-v aos-data:/var/lib/aos`, ex.: `/var/lib/aos/events.wal`). |
| `AOS_WORM_PATH` | *(vazio)* | Vazio ⇒ WORM in-memory. Definido ⇒ trilho WORM **hash-chain tamper-evident** em disco. Mesmo requisito de mount (ex.: `/var/lib/aos/worm.wal`). |
| `AOS_DURABLE_EXECUTION` | *(vazio ⇒ **DESLIGADA**)* | Ligam: `1` `true` `t` `yes` `y` `on`. Desligam: `0` `false` `f` `no` `n` `off` (ou ausente/vazia). **Qualquer outro valor ABORTA o arranque** (`enabled`, `tru`, `sim`, … **não** são tratados como `false` — ver abaixo). Ligada ⇒ o nó compõe **checkpointer + capturer de não-determinismo + step-ledger** sobre o Event Store; o tool set congelado (AOS-155) passa a persistir no mesmo store. Desligada ⇒ os três ficam `nil` e o runtime usa os defaults no-op (AOS-013). |

**Interacção `AOS_DURABLE_EXECUTION` × `AOS_EVENTSTORE_PATH` — fail-closed SEMPRE.**
`AOS_DURABLE_EXECUTION=1` **sem** `AOS_EVENTSTORE_PATH` **RECUSA o arranque** (`exit 1`), em
**qualquer** modo — não só em `AOS_MODE=production`. Razão: a execução durável compõe-se
**sobre** o Event Store; sobre um store in-memory os checkpoints, as capturas e o step-ledger
**evaporariam no reinício** e o nó anunciaria uma durabilidade que não cumpre. A ambiguidade
**nega** o arranque em vez de degradar em silêncio — a mesma postura de `AOS_BOARD_REGIONS`
malformado. Um valor não-booleano da própria variável aborta pela mesma razão: quem escreveu
`AOS_DURABLE_EXECUTION=enabled` **tenciona** ligar a durabilidade, e receber um nó silenciosamente
não-durável seria pior do que não arrancar.

> **O que a guarda NÃO consegue detectar.** Ela só vê a *ausência* de caminho. Um
> `AOS_EVENTSTORE_PATH=/tmp/events.wal` — ou qualquer caminho **fora** de `-v aos-data:/var/lib/aos`,
> incluindo o `--tmpfs /tmp` das receitas acima — **passa** a guarda e continua a perder tudo
> quando o contentor é substituído. Apontar `AOS_EVENTSTORE_PATH` e `AOS_WORM_PATH` para dentro do
> volume nomeado é **responsabilidade do operador**; é por isso que está documentado aqui e não só
> imposto em código.

**Verificação pelo operador** — o banner de arranque declara o estado **realmente composto** (não a
intenção da config); uma destas duas linhas sai sempre:

```text
[aos] execucao duravel (AOS-180): LIGADA — checkpointer + capturer + step-ledger COMPOSTOS sobre o event store (duravel em disco (AOS-170)); o tool set congelado (AOS-155) persiste no mesmo store
[aos] execucao duravel (AOS-180): DESLIGADA — checkpointer/capturer/step-ledger NAO compostos (defaults no-op AOS-013); defina AOS_DURABLE_EXECUTION=1 (exige AOS_EVENTSTORE_PATH) para ligar
```

A linha `[aos] substrato: ...` imediatamente antes diz `duravel em disco (AOS-170)` ou
`in-memory de referencia (nao-duravel)` — confirme-a **antes** de assumir que o estado sobrevive
a um reinício.

### Plano de controlo — operadores e aprovadores (AOS-160 / AOS-162, config em AOS-193)

O canal de controlo (`POST /runs/{id}/steer`, `/pause`) e o *four-eyes* (`/approve`) são
**default-deny**: sem configuração, **nenhum** sinal autentica e `/approve` responde `501`. Estas
duas variáveis são o **único** caminho para os ligar no binário entregue.

| Variável | Default | Efeito |
|---|---|---|
| `AOS_OPERATORS` | *(vazio ⇒ **default-deny**)* | Registo `emitterID=hexpubkey,emitterID2=hexpubkey2` das **pubkeys** ed25519 dos operadores autorizados a emitir `steer`/`pause`. `hexpubkey` = **64 hex chars = 32 bytes**, a mesma codificação de `AOS_ISSUER_PUBKEY`. Vazio ⇒ o canal fica composto mas **inoperável** (todo o sinal leva `403`) **e o bind não-loopback é RECUSADO** (ver abaixo). |
| `AOS_APPROVERS_FILE` | *(vazio ⇒ **four-eyes DESLIGADO**)* | Caminho de um **ficheiro JSON montado** com a *roster* de aprovadores do dual-control. Vazio ⇒ o `FourEyesGate` não é composto e `POST /runs/{id}/approve` responde `501` (desligado **por declaração**, não por avaria). |

**Fail-closed, sem degradação silenciosa** (a postura de `AOS_BOARD_REGIONS`/`AOS_DURABLE_EXECUTION`):
uma entrada sem `=`, um `emitterID` vazio, uma pubkey que não seja hex de 32 bytes, ou um
`emitterID` **duplicado** ⇒ o arranque **ABORTA** (`exit 1`). Não se "registam os que der": um
operador silenciosamente descartado daria um nó que arranca a anunciar um canal de controlo e
depois recusa **todos** os sinais desse operador com `403`. O duplicado aborta em vez de "o último
ganha" — dois valores para o mesmo `emitterID` são um conflito de autoridade, não uma preferência.
O ficheiro de aprovadores segue a mesma regra: ilegível, JSON inválido, **campo desconhecido**
(esquema em *drift*), lista vazia, principal duplicado ou autoridade vazia ⇒ **ABORTA**.

Duas regras adicionais, que valem **nos dois** caminhos (env e ficheiro) e também para quem compõe
`Config` in-process — o `Bootstrap` impõe-as antes de compor seja o que for:

- **Material de chave não se partilha.** Duas entradas com a **mesma pubkey** ⇒ **ABORTA**, mesmo
  com identificadores diferentes. Nos *aprovadores* isto é **segurança**: a distinção do
  dual-control compara `approver`/`session`/`credential` — três *strings* que o **cliente** escolhe
  na perna —, pelo que a pubkey **pinada** é a única âncora criptográfica de "duas pessoas"; colada
  em duas linhas, **uma** chave privada assina as **duas** pernas e o 4-eyes é anulado em silêncio,
  com o banner a declarar "2 aprovador(es) pinados". Nos *operadores* é **atribuição**: um
  `aos steer --emitter ops:bob` assinado pela chave de `ops:alice` seria aceite e **selado no WORM**
  como sendo de `ops:bob` — o nome do emissor deixaria de ser evidência.
- **`authority` tem vocabulário fechado:** `approve:safe`, `approve:gray`, `approve:danger` (é o que
  `hitl.RequiredAuthority` produz, uma por classe de risco). Qualquer outro valor —
  `approve:dangerous`, `approve:*`, `approver:danger` — ⇒ **ABORTA**. A comparação em runtime é de
  *string* **exacta, sem wildcards**: um *typo* seria *fail-closed* mas **silencioso** — um aprovador
  contado no banner que nunca aprova nada.

> **Só entra material PÚBLICO.** A chave **privada** do operador vive na máquina do operador — é lá
> que `aos steer`/`aos pause` assinam (`--key <ficheiro-da-seed>`); o nó só detém pubkeys e por isso
> **verifica mas não forja**. **Limite honesto:** uma *seed* ed25519 tem também 32 bytes, pelo que o
> nó **não a distingue** estruturalmente de uma pubkey. Colar a seed em `AOS_OPERATORS` produz um
> registo que nunca valida assinatura nenhuma (fail-closed, sem elevação de privilégio) — mas terá
> exposto a chave privada ao ambiente do nó. **Derive sempre a pubkey**, não copie a seed.

Derivar a entrada a partir da seed do operador (só stdlib, sem ferramenta externa):

```bash
aos operator-pubkey --key ./operator.seed --emitter ops:alice
# ops:alice=1f8b…  (64 hex chars)  ← o valor a pôr em AOS_OPERATORS
```

Formato do ficheiro de `AOS_APPROVERS_FILE` (monte-o read-only, ex.: `-v $PWD/approvers.json:/etc/aos/approvers.json:ro`):

```json
{
  "approvers": [
    {"principal": "human:alice", "pubkey": "<64 hex>", "authority": ["approve:danger", "approve:gray"]},
    {"principal": "human:bob",   "pubkey": "<64 hex-DIFERENTE>", "authority": ["approve:danger"]}
  ]
}
```

> **Wire de `POST /runs/{id}/approve`:** `risk_class` é o valor **numérico** de `risk.Class` —
> **`0` = `danger`** (é o **valor-zero**, escolhido para que uma classe não computada seja tratada
> como o pior caso), `1` = `safe`, `2` = `gray`. A capability exigida ao aprovador é
> `approve:<classe>` da classe **do pedido**; um aprovador só com `approve:gray` **não** autoriza um
> pedido `risk_class: 0`.

> **Porquê ficheiro aqui e env ali?** `AOS_OPERATORS` é um mapa `id→escalar` e cabe sem perda na
> gramática plana que o nó já usa (`AOS_BOARD_REGIONS`). Um aprovador **não** é escalar — traz
> `authority[]` —, e espremê-lo numa env exigiria um terceiro nível de delimitador, ilegível e
> irrevisível. Um ficheiro montado é a via que o **ADR-017 ponto 2** já prevê ("config por
> env/ficheiro montado") e é versionável em *code-review*, que é o que uma *roster* de dual-control
> deve ser. Cada colaborador tem **um** caminho de configuração: não há precedência env-vs-ficheiro
> a divergir em silêncio. A codificação do material público (hex de 32 bytes) e a disciplina
> fail-closed são **as mesmas** nos dois.

**Verificação pelo operador** — o banner declara o estado **realmente composto**:

```text
[aos] canal de controlo: Ed25519Authenticator (AOS-160) — 2 operador(es) registado(s) via AOS_OPERATORS; HMACAuthenticator demo DESLIGADO
[aos] canal de controlo: Ed25519Authenticator (AOS-160) composto mas SEM OPERADORES — steer/pause serao TODOS recusados (ErrUnknownEmitter) e o bind NAO-loopback e RECUSADO; defina AOS_OPERATORS="emitterID=hexpubkey" para o tornar operavel
[aos] four-eyes gate (AOS-162) composto: 2 aprovador(es) pinado(s) via AOS_APPROVERS_FILE
[aos] four-eyes gate (AOS-162): DESLIGADO (sem aprovadores) — POST /runs/{id}/approve responde 501; defina AOS_APPROVERS_FILE=<ficheiro JSON montado> para o compor
```

### Bind-guardrail (fail-closed)

A API **recusa** bind a um endereço **não-loopback** (`0.0.0.0`, `:8080`, um IP público, ou um
*hostname* não confirmável como loopback) enquanto o **canal de sinais (`steer`/`pause`)** não
estiver **autenticado E operável**. A condição imposta pelo código é a **conjunção** de três coisas:

1. o autenticador ed25519 do canal de controlo está composto (`SteerAuth != nil`);
2. o modo de identidade é real (`real` ou `real-trust-anchor-only`);
3. **existe pelo menos um operador com pubkey registada** (`AOS_OPERATORS` não-vazia).

Falhar qualquer uma ⇒ `ErrRefuseNonLoopbackBind` **antes** do `Listen` (o socket nem chega a
abrir); o *loopback* continua sempre permitido. O log da recusa nomeia o modo de identidade e a
**cardinalidade de operadores**, que é a causa esmagadoramente mais provável.

> **Âmbito exacto (não é omissão):** o predicado **não** olha para os aprovadores. Um nó
> configurado **só** com `AOS_APPROVERS_FILE` tem o `/approve` plenamente operável e é, ainda
> assim, recusado no bind não-loopback — `AOS_OPERATORS` é obrigatória. A escolha é a
> conservadora: `steer`/`pause` são a superfície de **intervenção** (parar um *run* a correr), a
> que justifica expor a porta à rede. Por isso a mensagem do erro diz **"steer/pause
> INOPERAVEIS"** e não "canal de controlo não operável" — o texto nomeia o que a condição
> verifica, nem mais nem menos.

> ⚠️ **MUDANÇA DE COMPORTAMENTO (AOS-193) — leia se já faz bind não-loopback.** Até AOS-193 o
> guardrail exigia só (1)∧(2) — duas condições que o `Bootstrap` satisfaz **sempre**, pelo que a
> condição era **identicamente verdadeira** e nunca recusava nada. Um nó que hoje sobe em
> `0.0.0.0` **sem** `AOS_OPERATORS` **passa a RECUSAR arrancar** (`exit 1`). A correcção é
> acrescentar `-e AOS_OPERATORS="<id>=<hexpubkey>"`. É deliberado: expor à rede um plano de
> controlo que não consegue aceitar **um único** sinal legítimo dá toda a superfície de ataque e
> nenhum benefício. Quem precise de manter o comportamento anterior sem operadores tem uma opção
> honesta — fazer bind ao **loopback** e publicar a porta pelo orquestrador.

Para servir tráfego externo endurecido:

```bash
docker run --read-only --tmpfs /tmp -v aos-data:/var/lib/aos -p 8080:8080 \
  -e AOS_MODE=production \
  -e AOS_API_ADDR=0.0.0.0:8080 \
  -e AOS_ISSUER_PUBKEY=<hex-32B-ed25519>   `# trust-anchor-only; a CHAVE PRIVADA fica no vault` \
  -e AOS_OPERATORS="ops:alice=<hex-32B-ed25519>"   `# PUBKEY do operador; a privada fica na maquina dele` \
  -e AOS_BOARD_REGIONS="board:prod=eu" \
  -e AOS_EVENTSTORE_PATH=/var/lib/aos/events.wal \
  -e AOS_WORM_PATH=/var/lib/aos/worm.wal \
  -e AOS_DURABLE_EXECUTION=1 \
  aos-node:local
```

Prova executável desta secção: [`aos193-control-plane-harness.sh`](aos193-control-plane-harness.sh)
— arranca o **contentor real** em `0.0.0.0` sem operadores (recusa), depois com um operador
(arranca) e envia um `aos steer` assinado (aceite); um emissor não registado leva `403`. O mesmo
contentor monta um `approvers.json` read-only (`-v … -e AOS_APPROVERS_FILE=…`): o banner declara o
*four-eyes* **composto** e `/approve` passa a **julgar** (`403` sem pernas válidas, já não `501`);
e um *roster* com a **mesma pubkey em dois principals** faz o nó **recusar arrancar**. A prova
positiva do `200` (duas pernas assinadas por **duas** chaves distintas ⇒ `authorized`) é
in-process, em `TestApproversFileAuthorizesDualControlEndToEnd` — assinar como aprovador é papel
do **dispositivo do humano**, que a CLI do nó deliberadamente não desempenha.

Os três últimos ligam o **estado durável** e a **execução durável** nos caminhos do volume
`aos-data` — ver [Estado durável](#estado-durável--variáveis-de-ambiente-aos-170--aos-180) para a
semântica e o fail-closed. **A execução durável é opt-in mesmo em `AOS_MODE=production`**: o nó
arranca sem ela (declarando `DESLIGADA` no banner, sem anunciar durabilidade nenhuma) — a promoção
a exigência de produção, a par de `AOS_ISSUER_PUBKEY`/`AOS_BOARD_REGIONS`, é decidida em **AOS-203**.

O `HEALTHCHECK` deriva a porta de `AOS_API_ADDR` (aqui, `8080`) e sonda `127.0.0.1:8080/healthz`
no loopback do contentor — não é preciso definir `AOS_HEALTH_URL`.

**Sem segredos na imagem**: `AOS_ISSUER_PUBKEY` é material **público** (trust anchor). A chave de
assinatura do issuer (AOS-156) é um trust-domain separado (ADR-017 ponto 5) — vem do vault/KeyVault
em runtime (ADR-006), nunca da imagem.

## Health / probes

- Container `HEALTHCHECK`: binário estático `aos-healthprobe` (distroless não tem shell/curl) →
  `GET /healthz` (liveness, AOS-171). O probe **deriva a porta de `AOS_API_ADDR`**
  (`127.0.0.1:<porta>/healthz`), pelo que segue automaticamente qualquer porta não-8080 — sem
  acoplamento silencioso a uma segunda variável. `AOS_HEALTH_URL` é um **override opcional**.
- **Kubernetes**: prefira sondas `httpGet` nativas — `livenessProbe` em `/healthz`,
  `readinessProbe` em `/readyz` (drain-aware).

## Entrega fail-closed (ADR-017 ponto 4)

```bash
bash scripts/ci/package.sh      # secrets + sast + sca + sbom + docker build
bash scripts/ci/sbom.sh         # só SBOM + proveniência (deploy/node/build/)
```

Reutiliza `sast.sh`/`sca.sh`/`secrets.sh` (baseline **multiset**, nunca `sort -u`). Uma descoberta
nova fora da baseline **avermelha**.

## Repin dos digests

As bases estão **pinadas por digest** (não só tag). Para actualizar:

```bash
docker pull golang:1.24.5-bookworm && docker image inspect --format '{{index .RepoDigests 0}}' golang:1.24.5-bookworm
docker pull gcr.io/distroless/static-debian12:nonroot && docker image inspect --format '{{index .RepoDigests 0}}' gcr.io/distroless/static-debian12:nonroot
```

Actualizar os digests em `deploy/node/Dockerfile` **e** em `scripts/ci/sbom.sh` (proveniência).
