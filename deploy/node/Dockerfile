# syntax=docker/dockerfile:1
# =============================================================================
# Dockerfile do nó `aos` — empacotamento DEPLOYÁVEL (AOS-168 / EPIC-15 §2).
#
# Fronteira de supply-chain: ADR-017 (Aceite, FIXA na Carta §4.1). Impõe:
#   (1) binário REPRODUZÍVEL zero-dep externa (estático, CGO off, GOPROXY=off no build);
#   (2) imagem ENDURECIDA: distroless STATIC, NON-ROOT (UID numérico), root-fs READ-ONLY,
#       sem shell nem package-manager, config só por env/ficheiro montado, SEM segredos;
#   (3) SBOM + proveniência — forma MÍNIMA (gerados por scripts/ci/sbom.sh; a ASSINATURA da
#       atestação + registry assinado ficam DEFERIDOS para EPIC-10 — declarado, não fingido);
#   (4) gates FAIL-CLOSED na entrega (scripts/ci/package.sh reutiliza sast/sca/secrets);
#   (5) a autoridade de IDENTIDADE (issuer, AOS-156) é um trust-domain SEPARADO — a sua chave
#       NUNCA entra nesta imagem; vem do vault em runtime (ADR-006).
#
# Contexto de build = RAIZ do repo (os 42 `replace ... => ../../` de packages/cmd/aos/go.mod
# são relativos; a árvore packages/ inteira tem de estar no contexto para resolverem offline).
#   docker build -f deploy/node/Dockerfile -t aos-node:local .
#
# Bases PINADAS POR DIGEST (não só tag móvel) — ver deploy/node/README.md para a política de
# actualização (repin) dos digests.
# =============================================================================

# ----------------------------------------------------------------------------
# STAGE 1 — builder: compila o binário ESTÁTICO zero-dep + o healthprobe.
# golang:1.24.5-bookworm  (go 1.24.5, casa com `go 1.24` dos módulos)
# ----------------------------------------------------------------------------
FROM golang:1.24.5-bookworm@sha256:ef8c5c733079ac219c77edab604c425d748c740d8699530ea6aced9de79aea40 AS builder

# CGO OFF => binário estático puro (sem libc dinâmica), pré-condição do distroless static.
ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOTOOLCHAIN=local

WORKDIR /src

# A árvore packages/ inteira: os replaces relativos (../../platform/..., ../../kernel/...)
# resolvem a partir de /src/packages/cmd/aos. O healthprobe é um módulo stdlib-only isolado.
COPY packages/ ./packages/
COPY deploy/node/healthprobe/ ./healthprobe/

# --- Binário do nó -----------------------------------------------------------
WORKDIR /src/packages/cmd/aos
# PRIME determinista do módulo-cache a partir do go.sum PINADO (única etapa que toca a rede,
# à imagem de scripts/ci/cache-prime.sh — o projecto não vendoriza por opção). `go mod verify`
# falha se um hash divergir do go.sum. A ÚNICA dependência externa do grafo é cedar-go
# (+ golang.org/x/exp indirect); tudo o resto é stdlib + módulos locais (replace por path).
RUN go mod download && go mod verify
# Build REPRODUZÍVEL e OFFLINE: GOPROXY=off prova que nada é obtido para além do cache já
# verificado; -trimpath remove caminhos absolutos; -buildid= e -s -w tornam o output estável.
RUN GOPROXY=off go build -trimpath -ldflags="-s -w -buildid=" -o /out/aos .

# --- healthprobe (zero-dep: só stdlib => offline trivial, sem cache externo) --
WORKDIR /src/healthprobe
RUN GOPROXY=off go build -trimpath -ldflags="-s -w -buildid=" -o /out/aos-healthprobe .

# ----------------------------------------------------------------------------
# STAGE 2 — final: distroless STATIC, NON-ROOT. Sem shell, sem package-manager,
# sem segredos. Só os dois binários estáticos.
# gcr.io/distroless/static-debian12:nonroot
# ----------------------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35

# Metadados de proveniência (o registo assinado é DEFERIDO — ADR-017 ponto 3 / EPIC-10).
LABEL org.opencontainers.image.title="aos-node" \
      org.opencontainers.image.description="Nó de referência do Agentic OS (AOS-168), distroless non-root read-only" \
      org.opencontainers.image.source="github.com/aos-ref/cmd/aos" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.aos.adr="ADR-017" \
      org.aos.supplychain.sbom="scripts/ci/sbom.sh (forma minima)" \
      org.aos.supplychain.attestation="DEFERIDO-EPIC-10 (por assinar)"

# Binários estáticos (o único conteúdo mutável do artefacto).
COPY --from=builder /out/aos /usr/local/bin/aos
COPY --from=builder /out/aos-healthprobe /usr/local/bin/aos-healthprobe

# NON-ROOT por UID NUMÉRICO (o distroless `nonroot` é uid:gid 65532:65532). UID numérico (não
# nome) para que orquestradores que impõem runAsNonRoot possam validar sem resolver /etc/passwd.
USER 65532:65532

# ESTADO DURÁVEL fora do root-fs (AOS-170): o Event Store (EventStorePath) e o WORM (WORMPath),
# QUANDO o operador os liga, escrevem aqui — um mount GRAVÁVEL EXPLÍCITO (-v aos-data:/var/lib/aos),
# nunca o root-fs read-only. O arranque de referência corre in-memory (não escreve no root-fs),
# pelo que a imagem roda limpa sob `--read-only`.
#
# NÃO se declara `VOLUME` aqui DE PROPÓSITO: um VOLUME implícito criaria um volume ANÓNIMO gravável
# neste caminho mesmo sob `docker run --read-only` sem `-v`, o que (a) MASCARARIA a verificação de
# root-fs read-only (escritas de estado passariam por um volume não-gerido, dando falsa confiança) e
# (b) acumularia volumes órfãos a cada run sem `--rm`. Sem VOLUME, uma tentativa de durabilidade sem
# mount explícito FALHA VISIVELMENTE em vez de escrever em silêncio. O WORKDIR (após USER 65532) cria
# o directório já owned por 65532:65532, para que o mount nomeado do operador herde a ownership certa.
WORKDIR /var/lib/aos

# Porta da API HTTP (AOS-166). Convencional; a porta REAL vem de AOS_API_ADDR em runtime.
EXPOSE 8080

# Config SÓ por ambiente (sem segredos na imagem). O probe DERIVA a URL de liveness da porta de
# AOS_API_ADDR (elimina o acoplamento silencioso a uma porta hardcoded); AOS_HEALTH_URL é um
# override OPCIONAL. Por isso NÃO se fixa aqui uma AOS_HEALTH_URL com porta hardcoded. NENHUM
# segredo/chave/identidade é definido (ponto 5): o issuer pubkey (AOS_ISSUER_PUBKEY), operadores e
# afins são injectados pelo operador/vault em runtime.

# HEALTHCHECK via o probe estático (distroless não tem shell/curl) contra /healthz (AOS-171).
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/aos-healthprobe"]

ENTRYPOINT ["/usr/local/bin/aos"]
CMD ["serve"]
