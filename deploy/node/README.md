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

### Bind-guardrail (fail-closed)

A API **recusa** bind a um endereço **não-loopback** (`0.0.0.0`) enquanto o canal de controlo
não estiver autenticado (identidade real + operadores). Para servir tráfego externo endurecido:

```bash
docker run --read-only --tmpfs /tmp -v aos-data:/var/lib/aos -p 8080:8080 \
  -e AOS_MODE=production \
  -e AOS_API_ADDR=0.0.0.0:8080 \
  -e AOS_ISSUER_PUBKEY=<hex-32B-ed25519>   `# trust-anchor-only; a CHAVE PRIVADA fica no vault` \
  -e AOS_BOARD_REGIONS="board:prod=eu" \
  -e AOS_EVENTSTORE_PATH=/var/lib/aos/events.wal \
  -e AOS_WORM_PATH=/var/lib/aos/worm.wal \
  -e AOS_DURABLE_EXECUTION=1 \
  aos-node:local
```

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
