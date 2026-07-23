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

### Bind-guardrail (fail-closed)

A API **recusa** bind a um endereço **não-loopback** (`0.0.0.0`) enquanto o canal de controlo
não estiver autenticado (identidade real + operadores). Para servir tráfego externo endurecido:

```bash
docker run --read-only --tmpfs /tmp -v aos-data:/var/lib/aos -p 8080:8080 \
  -e AOS_MODE=production \
  -e AOS_API_ADDR=0.0.0.0:8080 \
  -e AOS_ISSUER_PUBKEY=<hex-32B-ed25519>   `# trust-anchor-only; a CHAVE PRIVADA fica no vault` \
  -e AOS_BOARD_REGIONS="board:prod=eu" \
  aos-node:local
```

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
