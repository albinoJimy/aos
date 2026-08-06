# Componente host-side do executor Firecracker (a integração real fora do módulo do nó).
# Duas fases: build dos binários Go (orchestrator stdlib + guest-agent com vsock), e runtime
# com o firecracker + e2fsprogs para construir o rootfs. Corre PRIVILEGIADO com /dev/kvm.
# syntax=docker/dockerfile:1

FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY deploy/node/dev-hardened/firecracker/ .
# guest-agent: ESTÁTICO (é o init da microVM; sem libc do guest). orchestrator: stdlib.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/orchestrator ./orchestrator \
 && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/guest-agent ./guest-agent

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends curl ca-certificates e2fsprogs mount util-linux \
 && rm -rf /var/lib/apt/lists/*
# Binário do firecracker (host-side; conduz a microVM via KVM).
ARG FC_VERSION=v1.7.0
RUN curl -fsSL -o /tmp/fc.tgz "https://github.com/firecracker-microvm/firecracker/releases/download/${FC_VERSION}/firecracker-${FC_VERSION}-x86_64.tgz" \
 && tar -xzf /tmp/fc.tgz -C /tmp \
 && cp "/tmp/release-${FC_VERSION}-x86_64/firecracker-${FC_VERSION}-x86_64" /usr/local/bin/firecracker \
 && chmod +x /usr/local/bin/firecracker \
 && rm -rf /tmp/*
COPY --from=build /out/orchestrator /orchestrator
COPY --from=build /out/guest-agent /guest-agent
COPY deploy/node/dev-hardened/firecracker/build-rootfs.sh /build-rootfs.sh
COPY deploy/node/dev-hardened/firecracker/entrypoint.sh /entrypoint.sh
RUN chmod +x /build-rootfs.sh /entrypoint.sh /guest-agent
ENTRYPOINT ["/entrypoint.sh"]
