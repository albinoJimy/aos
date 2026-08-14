# Componente de autoridade de attestation WebAuthn (AOS-177) — corre o descodificador CBOR que o
# binário do nó NUNCA pode importar (ADR-017). Build a partir da raiz do repo (contexto = repo root,
# como o do nó), módulo próprio packages/cmd/aos-attestation.
FROM golang:1.25.13-bookworm@sha256:e401dae1bf814e29204a8cb7915682e1780951e609ca0dd8865ee1937f510c48 AS build
WORKDIR /src
COPY . .
WORKDIR /src/packages/cmd/aos-attestation
RUN CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -ldflags="-s -w" -o /out/aos-attestation .

FROM gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35
COPY --from=build /out/aos-attestation /aos-attestation
EXPOSE 8090
# Args (serve --addr --tls-cert --tls-key … ou --ca em produção) vêm do `command` do compose.
ENTRYPOINT ["/aos-attestation"]
