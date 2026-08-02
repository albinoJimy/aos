# syntax=docker/dockerfile:1
# =============================================================================
# Toolbox de DEV: o binário `aos-issuer` (autoridade de identidade EXTERNA, D4) num container, para
# correr o fluxo `mint --assertion` EM-REDE — onde alcança o Keycloak por https e confia na CA de
# dev via SSL_CERT_FILE (o host Windows não honra SSL_CERT_FILE; um container Linux honra).
#
# Contexto de build = RAIZ do repo (os replaces relativos de packages/cmd/aos-issuer/go.mod exigem
# a árvore packages/ inteira). Mesma disciplina reprodutível/offline do nó (ADR-017).
#   docker build -f deploy/node/dev-hardened/issuer-toolbox/Dockerfile -t aos-issuer-toolbox:local .
# =============================================================================
FROM golang:1.25.12-bookworm@sha256:ea341baa9bd5ba6784f6d7161ace70544349a6242d54d34a0fbfd2c4d51c9d58 AS builder
ENV CGO_ENABLED=0 GOOS=linux GOTOOLCHAIN=local
WORKDIR /src
COPY packages/ ./packages/
WORKDIR /src/packages/cmd/aos-issuer
RUN go mod download && go mod verify
RUN GOPROXY=off go build -trimpath -ldflags="-s -w -buildid=" -o /out/aos-issuer .

FROM gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35
COPY --from=builder /out/aos-issuer /usr/local/bin/aos-issuer
# WORKDIR = onde a issuer.key é montada, para que o chamador use `--key-file issuer.key` (relativo):
# um path de container com barra inicial (/etc/aos/...) seria reescrito pelo Git Bash/MSYS antes de
# chegar ao docker; um path relativo passa incólume.
WORKDIR /etc/aos
ENTRYPOINT ["/usr/local/bin/aos-issuer"]
