# syntax=docker/dockerfile:1
# Componente host-side do executor gVisor — a integração real, fora do módulo Go do nó.
#
# Duas fases: build dos dois binários stdlib (component + guest), e runtime com o runsc.
# O guest é ESTÁTICO porque corre dentro do sandbox, num rootfs mínimo sem libc.
#
# ⚠️ Corre PRIVILEGIADO. O runsc precisa de criar namespaces (mount/pid/user) e de montar o
# bundle; dentro de um contentor isso exige privilégios que o default não dá. É a mesma postura
# do componente Firecracker deste repositório — e a razão de o componente ser um processo
# SEPARADO do nó: o nó nunca corre privilegiado, e a fronteira entre os dois é HTTP.

FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY deploy/server/gvisor/ .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/component ./component \
 && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/guest ./guest

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*

# runsc — binário oficial do gVisor, verificado pelo checksum publicado. Uma versão fixa seria
# preferível; `latest` fica declarado como residual (o repositório pina bases por digest noutros
# sítios e este ainda não).
ARG GVISOR_URL=https://storage.googleapis.com/gvisor/releases/release/latest/x86_64
RUN curl -fsSL "${GVISOR_URL}/runsc" -o /usr/local/bin/runsc \
 && curl -fsSL "${GVISOR_URL}/runsc.sha512" -o /tmp/runsc.sha512 \
 && (cd /usr/local/bin && sha512sum -c /tmp/runsc.sha512) \
 && chmod 755 /usr/local/bin/runsc \
 && rm -f /tmp/runsc.sha512

COPY --from=build /out/component /component
COPY --from=build /out/guest /guest

ENV GV_ADDR=":9101" GV_SEED_DIR="/seed" GV_GUEST_BIN="/guest" GV_PLATFORM="systrap"
EXPOSE 9101
HEALTHCHECK --interval=10s --timeout=3s --start-period=15s --retries=5 \
  CMD curl -fsS http://127.0.0.1:9101/healthz || exit 1
ENTRYPOINT ["/component"]
