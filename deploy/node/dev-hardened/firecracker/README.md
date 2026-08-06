# Executor Firecracker (microVM real) — AOS-064

Este componente é a **integração REAL** que o `sandbox.FirecrackerDriver` do nó deixa **fora do
módulo Go** por desenho (ADR-017, nó zero-dep). O driver, sozinho, é um *skeleton*: sem um
`GuestExecutor` injectado devolve `ErrDriverUnavailable`. Aqui vive a orquestração da microVM, e o
nó fala com ela por **HTTP stdlib** — o mesmo padrão do serviço `attestation`.

## Arquitetura

```
  nó (aos, zero-dep)                    firecracker (componente host-side, privilegiado)
  ┌────────────────────┐   HTTP POST    ┌──────────────────────────────────────────────┐
  │ FirecrackerDriver   │  ──/exec──▶    │ orchestrator (Go stdlib)                       │
  │  + remoteFirecracker│               │   arranca 1 microVM DEDICADA por tool call:    │
  │    Executor (stdlib)│               │   firecracker --no-api (kernel + rootfs + vsock)│
  └────────────────────┘   ◀─Result─    │        │                                        │
                                        │        ▼  vsock (host↔guest)                    │
                                        │   ┌─────────────────────────────────────────┐  │
                                        │   │ microVM (KVM): guest-agent = PID 1        │  │
                                        │   │  lê /seed/<path> (RootFS base read-only)  │  │
                                        │   │  devolve Result, poweroff                 │  │
                                        │   └─────────────────────────────────────────┘  │
                                        └──────────────────────────────────────────────┘
```

Fronteira de isolamento: **virtualização de hardware** (kernel do guest separado), sem socket do
host, sem namespace de rede/PID partilhado, rootfs read-only, sem rede (AOS-067 por omissão). O
`ExecResult` volta SEMPRE untrusted (ADR-005), imposto por tipo no nó.

## Peças

| Ficheiro | Papel |
|---|---|
| `orchestrator/` | Serviço HTTP (stdlib) que conduz o firecracker por microVM e faz o handshake vsock host→guest. |
| `guest-agent/` | Binário estático que corre como **init (PID 1)** dentro da microVM; serve 1 tool call por vsock e desliga. |
| `wire/` | Contrato JSON partilhado (o nó define o seu equivalente local para ficar zero-dep). |
| `build-rootfs.sh` | Constrói o `rootfs.ext4` mínimo (agent como `/init` + `/seed`). |
| `entrypoint.sh` | Baixa o kernel do guest + constrói o rootfs + arranca o orchestrator. |
| `seed/` | Documentos semeados no RootFS base read-only (ex.: `notes`). |

## Correr

Ligado na stack via `docker-compose.oidc.yml` (serviço `firecracker` + `AOS_SANDBOX_DRIVER=firecracker`
+ `AOS_SANDBOX_FIRECRACKER_URL` no nó). Standalone:

```bash
docker build -f deploy/node/dev-hardened/firecracker/Dockerfile -t aos-firecracker:local .
docker run -d --name fc-orch --privileged --device /dev/kvm -p 9100:9100 \
  -v fc-art:/art -v "$PWD/deploy/node/dev-hardened/firecracker/seed:/seed:ro" aos-firecracker:local
curl -s -X POST http://localhost:9100/exec \
  -d '{"run_id":"r1","step_id":"s1","call":{"tool_id":"doc_read","command":"read","path":"notes"}}'
```

Prova ponta-a-ponta pelo caminho do driver do nó: `go test -tags fclive -run TestFCLive_RealMicroVM`
no package `substrate/sandbox` com `FC_ORCH_URL` a apontar ao orchestrator.

Exige `/dev/kvm` (virtualização aninhada). Presente na VM do Docker Desktop; num host Linux nativo
é directo.

## Residuais de produção (DEMO-GRADE honesto — eixo AOS-064/EPIC-07)

Isto é substrato **real** (microVM genuína, isolamento por hardware), mas o *provisionamento* é
demo-grade. Para produção, por eixo:

- **jailer**: envolver o firecracker no `jailer` (chroot + cgroups + user ns + seccomp do VMM). Hoje
  o orchestrator corre o firecracker directamente num container privilegiado.
- **overlay por-call (AOS-066)**: o rootfs é read-only partilhado (correcto para tools de leitura);
  tools de ESCRITA precisam de um overlay efémero por execução descartado no destroy.
- **rede default-deny explícita (AOS-067)**: hoje a microVM não tem rede (deny por ausência); tools
  com egress precisariam de um tap dedicado + a allowlist de egress por deployment.
- **credentials handle (ADR-006/AOS-070)**: não cabelado (a tool de referência não usa segredo).
- **proveniência do kernel/rootfs**: o kernel é o artefacto CI do firecracker baixado e o rootfs é
  construído em runtime; produção fixa/atesta ambos (assinatura + digest pinado).
- **pooling (AOS-065)**: hoje é uma microVM fresca por call (isolamento máximo, ADR-004); produção
  pode pré-arrancar um pool para latência.

Estes residuais estão nomeados com eixo; nenhum afecta a INVARIANTE provada aqui — uma tool call
permitida executa numa microVM real, isolada por hardware, e o resultado volta untrusted.
