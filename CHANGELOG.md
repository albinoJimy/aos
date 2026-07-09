# Changelog

Todas as alterações relevantes deste repositório. Formato baseado em
[Keep a Changelog](https://keepachangelog.com/) e alimentado por
[Conventional Commits](https://www.conventionalcommits.org/) — ver
[specs/01_Engineering_Standards_e_Handoff.md](specs/01_Engineering_Standards_e_Handoff.md) §5.

## [Unreleased]

### Added — AOS-003 Reference Monitor (PEP)
- `feat(AOS-003): reference monitor (PEP) em packages/kernel/reference-monitor` — superfície única `Mediate(ctx, call) → Decision` (autoriza e despacha; nenhuma tool executa fora dela), cadeia de hooks `Identity → Policy → Budget → Egress → Audit` (contratos + stubs neutros), fail-closed em deny/erro/panic/escalate/falha-de-auditoria, registo do evento de mediação no Event Store (AOS-002) com run_id/step_id/latência/decisão/principal. Go, zero deps externas (integra o ES por `replace` local).
  - No-bypass em duas camadas: `Permit` não-forjável (token não-exportado, ligado ao *call*, uso único via `CompareAndSwap`) + arch-lint `go/ast` que corre como teste (falha em violação) com testdata bom/mau.
  - Auditoria adversarial apanhou e a remediação corrigiu: **cadeia de hooks vazia permitia tudo (fail-open)** → agora nega fail-closed (`CodeEmptyHookChain`); payload de auditoria enriquecido (Resource/Context/request_id/port_version, alinhado a C1); `Decision.Code` estável; arch-lint documentado honestamente como defesa-em-profundidade heurística (a garantia forte é estrutural).
  - Overhead de mediação `BenchmarkMediate` ~657 ns/op — p95 ~0.0007 ms, muito abaixo do alvo de 15 ms. 14 testes, `-race` limpo, cobertura 89.2%.

### Added — AOS-002 Event Store
- `feat(AOS-002): event store replicado append-only em packages/substrate/eventstore` — API `Append`/`Read`/`Subscribe`, envelope versionado (schema 1.0), ordem total por `(stream_id, seq)`, append-only estrito, idempotência `f(run_id, step_id)`, replicação de referência com quórum + failover, transporte push. Go, zero dependências. 28 testes, `-race` limpo, cobertura 96.5%.
  - Auditoria adversarial de qualidade apanhou e a remediação corrigiu: **perda silenciosa de eventos confirmados** no revive de réplica *stale* pós-perda-de-quórum (agora `electLeader`/`Revive` recusam promover abaixo do commit index confirmado por quórum → `ErrNoQuorum` em vez de servir log truncado); **fuga de goroutine** no `Subscribe` (ciclo de vida ligado ao `ctx`).
  - Benchmarks registados: `Append` ~6.3 µs/op; `Fanout`/50 subs ~70 µs/op.

### Added
- `chore: esqueleto do monorepo AOS (packages/{kernel,control-plane,platform,substrate})`.
- `chore: IaC declarativa dev/staging com estado remoto + locking e módulos network/eventstore/secrets`.
- `docs: README de arranque (<= 30 min)`.
- `chore: lockfile de providers multi-plataforma (linux/darwin/windows) e .gitattributes (eol=lf)`.

### Fixed
- `fix(infra): torna o apply idempotente — remove bloco capabilities/IPC_LOCK do Vault que forçava replace a cada plan` (validado: 2.º plan = *No changes*).
- `fix(infra): parametriza encrypt do backend S3 por ambiente (false no MinIO local sem KMS, true em S3 real) — corrige erro 501 no lock de estado`.
- `fix(infra): fixa MinIO/mc >= 2025 no bootstrap — releases antigas não suportam escrita condicional exigida pelo use_lockfile`.

### Validated (runtime, ambiente dev)
- Ciclo completo provado end-to-end: bootstrap → init (estado remoto S3 + locking) → apply (7 recursos) → plan idempotente (*No changes*) → destroy limpo (0 remanescentes).
