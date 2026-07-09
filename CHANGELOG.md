# Changelog

Todas as alterações relevantes deste repositório. Formato baseado em
[Keep a Changelog](https://keepachangelog.com/) e alimentado por
[Conventional Commits](https://www.conventionalcommits.org/) — ver
[specs/01_Engineering_Standards_e_Handoff.md](specs/01_Engineering_Standards_e_Handoff.md) §5.

## [Unreleased]

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
