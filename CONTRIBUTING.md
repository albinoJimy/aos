# Contribuir para o AOS — gates de CI e execução local

Todos os *merges* passam pelos **gates de qualidade** de `specs/01 §4`. O
*pipeline* é **fail-closed**: qualquer gate vermelho bloqueia. A **fonte de
verdade** dos gates é **local e testável** — os scripts em `scripts/ci/` — e a CI
(`.github/workflows/ci.yml`) apenas os **invoca**; nada é duplicado no YAML.

## Reproduzir os gates localmente — UM comando

```bash
make ci
# equivalente (sem make instalado):
bash scripts/ci/run.sh
```

Corre, por ordem canónica, `secrets → build → lint → test → sast → sca → policy-test`
e termina com `exit != 0` se qualquer gate falhar. Provar que as falhas **são
bloqueadas**:

```bash
make ci-selftest          # ou: bash scripts/ci/selftest.sh
make ci-all               # gates + self-tests
```

Correr um gate isolado: `make ci-secrets | ci-build | ci-lint | ci-test | ci-sast | ci-sca | ci-policy`
(ou `bash scripts/ci/<gate>.sh`).

## Pré-requisitos

| Ferramenta | Versão | Notas |
|---|---|---|
| **Go** | 1.24 | módulos em `packages/**` (descobertos por `find packages -name go.mod`) |
| **gcc** | qualquer | exigido pelo `go test -race` (CGO). Windows: mingw do scoop; Linux: gcc do sistema |
| **bash** | 4+ | Git Bash em Windows |
| staticcheck / gosec / govulncheck | pinadas | **auto-instaladas** por `go install` (idempotente) em `$(go env GOPATH)/bin`; nunca committadas |

Pins das ferramentas em `scripts/ci/lib.sh` (`*_PIN`). Em Windows, o runner
acrescenta ao `PATH` o mingw/shims do scoop e o `bin` do GOPATH, e força
`CGO_ENABLED=1` — não é preciso configuração manual.

## Os gates

| # | Gate | Script | O que valida | Bloqueia |
|---|---|---|---|---|
| 1 | build | `build.sh` | `go build ./...` em cada módulo | merge |
| 2 | lint | `lint.sh` | `gofmt -l`, `go vet`, `staticcheck` **+ arch-lint AOS-003** (proibição de despacho directo) | merge |
| 3 | test | `test.sh` | `go test ./... -race -covermode=atomic`; **cobertura do kernel ≥ 80%** | merge |
| 4 | sast | `sast.sh` | `gosec` — falha em findings **HIGH/CRITICAL** | merge |
| 5 | sca | `sca.sh` | `govulncheck` — falha em vuln que **afecta** o código | merge |
| 6 | policy-test | `policy-test.sh` | bundle do PDP (AOS-004): golden allow/deny (default-deny) **+ verificação de assinatura**; bundle não-assinado/adulterado é rejeitado | merge |

### Baselines (dívida pré-existente triada)

`lint`, `sast` e `sca` comparam as descobertas com uma **baseline**
(`scripts/ci/baseline/*.txt`): são **fail-closed para código NOVO** e apenas
toleram dívida já existente de outros tickets (AOS-010 é *chore* de CI e não
altera código dos módulos). Cada entrada da baseline está documentada, com dono e
remediação, em [`scripts/ci/baseline/README.md`](scripts/ci/baseline/README.md).
Qualquer descoberta fora da baseline faz o gate ficar vermelho.

### Auto-testes dos gates (`selftest.sh`)

Provam, de forma isolada e reversível (sem rasto no repo), que os gates bloqueiam:

- **A** — módulo mau injectado (gofmt sujo + teste que falha) ⇒ `lint`/`test` vermelhos;
- **B** — assinatura do bundle do PDP adulterada (backup+restore) ⇒ `policy-test` vermelho;
- **C** — vuln afetante fora da baseline ⇒ comparador do `sca` vermelho (determinista, offline).

## Required checks (bloqueiam o merge em `main`)

Configurar em *branch protection* de `main` os checks:
`secrets · build · lint · test · sast · sca · policy-test · selftest`, ou o
agregador único **`gates`** (job que depende de todos). O **scan de segredos**
(regra transversal de `specs/01 §4`) tem o seu próprio job e é pré-condição de merge.

## Tempo aceitável

**Local (sequencial):** ~10 min numa máquina de programador — dominado por
`sast`/gosec e `sca`/govulncheck (que descarrega a base de vulnerabilidades).
Repartição observada: `secrets` ~1 s · `build` ~10 s · `lint` ~30 s ·
`test -race` ~107 s · `sast` ~278 s (gosec, dominante) · `sca` ~151 s
(govulncheck, rede) · `policy-test` ~10 s · `selftest` ~78 s.

**Na CI:** os jobs correm em **paralelo** com cache de módulos (`actions/setup-go`),
pelo que o *wall-clock* é ≈ o gate mais lento (~5 min, `sast`), não a soma.
Para iteração rápida local, correr gates isolados (`make ci-lint`, `make ci-test`).
