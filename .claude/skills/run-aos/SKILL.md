---
name: run-aos
description: Build, run, and drive the AOS reference node (packages/cmd/aos) and its sibling binaries. Use when asked to start AOS, serve the node, submit or observe a run, steer/pause a run, exercise the HTTP API, inspect the WAL/WORM, change autonomy levels, run a smoke test, or run the Go test suites.
---

The runnable thing in this repo is the **`aos` node** — an HTTP service plus operator
CLI at `packages/cmd/aos`, with four sibling binaries (`aos-issuer`, `aos-demo`,
`aos-attestation`, `aos-orq`). Everything else under `packages/` is a Go library
module consumed by it.

Drive it with **`bash .claude/skills/run-aos/driver.sh`**. It composes the node
(signed PDP bundle, four-eyes approvers, pinned operators, autonomy oracle, durable
Event Store + WORM), launches it in the background, and gives you one verb per
surface: `run`, `observe`, `pause`, `steer`, `autonomy-set`, `trajectory`, `wal`,
`worm`, `api`. `driver.sh smoke` proves the whole thing in nine asserted steps.

All paths are relative to the repo root (`C:\Jimy\AOS`). Verified on Windows 11 +
Git Bash + Go 1.27.1.

## Prerequisites

Only Go, bash and curl. **No Docker, no `make`, no Node, no `jq`, no OpenTofu** — the
`README.md` quick-start describes the *infrastructure* (MinIO + OpenTofu + Vault),
which the node does not need to run.

```bash
go version   # >= 1.24 (modules pin go 1.24/1.25; 1.27.1 works)
curl --version
gcc --version   # only needed for `go test -race` (MinGW-w64 ships with Git for Windows)
```

`make` is **not** on PATH in Git Bash here. Every `make ci-*` target is a thin wrapper
over `bash scripts/ci/<gate>.sh`; call those directly.

## Build

```bash
bash .claude/skills/run-aos/driver.sh build
```

Compiles all five binaries into `$(driver.sh home)/bin` (default `/tmp/aos-driver/bin`).
Each `packages/cmd/*` is its own Go module — there is no workspace-wide `go build ./...`.

You rarely need this by hand: `up` and `smoke` rebuild on their own whenever `packages/`
moved ahead of the binaries — see [Binary freshness](#binary-freshness).

To check every module compiles (50 modules, ~2 min):

```bash
bash scripts/ci/build.sh
```

## Run (agent path)

```bash
bash .claude/skills/run-aos/driver.sh smoke
```

That is the one command to run first. It brings up a fully composed node, then asserts:

1. `POST /runs` accepts a run
2. the run reaches `completed`
3. the sovereign read-path denies uncredentialled access (404 on read, 403 on write)
4. signed `pause` + `steer` are accepted on the control channel
5. an **unpinned** emitter is refused
6. a signed `POST /autonomy` moves `agt-1:fs` L4 → L5 and `GET /autonomy` reflects it
7. the SSE trajectory stream emits events
8. the durable WAL has `turn.recorded` and the WORM has the sealed `control:pause`
9. `/metrics` reports `aos_ready 1`

Real output from this session:

```
[driver] binarios frescos (aos com 1m; nada mais recente em packages/)
[driver] OK no pronto em http://127.0.0.1:18080
[driver] pre-voo: binarios correspondem ao codigo (aos com 1m)
[driver] 1/9 submeter run smoke-2145
[driver] 2/9 observar ate completar
[driver] OK run smoke-2145: status=completed terminated=true paused=false panicked=false turns=1 final=no `aos`: modelo de referencia (Model Gateway real = EPIC-06)
[driver] 3/9 read-path soberano NEGA sem credencial (leitura 404, escrita 403)
...
[driver] OK SMOKE VERDE (run=smoke-2145)
```

### Binary freshness

**A green `smoke` can no longer be running over stale code.** `up` — and therefore
`smoke` — rebuilds whenever anything under `packages/` is newer than the binaries in
`$(driver.sh home)/bin`, and when it does reuse them it prints their age.

It used to build only when the binary was *missing*, so from the second run onward
`smoke` launched the previous run's binary and reported 9/9 green without exercising a
single changed line. Measured 2026-09-05 while validating EPIC-23: green smoke, a
seven-hour-old binary (from before the whole epic), and none of the new boot banners in
`serve.log`; with a forced `build`, the same smoke went 9/9 **and** the banners appeared.

That is worst exactly where `smoke` is the only evidence that counts — wiring, boot
banners, HTTP surface (see `agentic-engineering` §7). Those are the changes the unit
suites do not catch, so a stale binary leaves the suite green *and* the smoke green over
old code, with nothing to tell you apart.

```bash
bash .claude/skills/run-aos/driver.sh freshness           # mtime per binary; exit 1 if any lags packages/
bash .claude/skills/run-aos/driver.sh freshness-selftest   # anti-regression assertion (see below)
```

`freshness-selftest` ages the binary to 2000-01-01 (it touches nothing in the repo), runs
the freshness path and asserts a rebuild happened. Restore the old build-if-missing logic
and it fails with `ensure_build REUTILIZOU o binario obsoleto`. `smoke` carries the same
assertion as a pre-flight step before its nine numbered steps, so a stale-binary green is
a hard failure rather than a silent one.

The scan spans **all** of `packages/`, not just `packages/cmd/**`: every `cmd/*` module
`replace`s the libraries to local paths, so a change in `substrate/` or `control-plane/`
lands in the binaries too. It skips `*_test.go` and `testdata` (they do not link in),
costs ~3 s, and a full relink of the five binaries costs ~25 s.

`AOS_DRIVER_ALWAYS_BUILD=1` forces the rebuild. `AOS_DRIVER_NO_BUILD=1` skips the check
with a loud warning — and makes `smoke` refuse to report green, which is the point: if
you opted out of the check, the green would not be proof.

### Interactive driving

```bash
D=.claude/skills/run-aos/driver.sh
bash $D up                          # compose + launch, waits for /readyz 200
bash $D run meu-run "auditar o pipeline"
bash $D observe meu-run
bash $D trajectory meu-run 4 20     # SSE, 4s budget, first 20 lines
bash $D pause meu-run               # ed25519-signed control-plane call
bash $D steer meu-run "muda de rumo"
bash $D autonomy-set agt-1 fs L5
bash $D autonomy-get
bash $D api GET /runs/meu-run       # raw curl, sovereign read headers attached
bash $D status                      # healthz / readyz / key metrics
bash $D banner                      # which subsystems actually composed at boot
bash $D logs                        # full startup log
bash $D partitions                  # WORM partition names (needed by `worm`)
bash $D worm governance.control     # sealed attribution for a partition
bash $D wal                         # wal-summary of the durable Event Store
bash $D down
```

`bash $D` with no argument prints the full command list.

What `up` composes (each is otherwise off, and the node says so loudly at boot):

| Env var | Driver value | Without it |
|---|---|---|
| `AOS_OPERATORS` | `op:jimy=<pubkey>,op:maria=<pubkey>` (two generated seeds) | steer/pause all refused (`ErrUnknownEmitter`) |
| `AOS_AUTONOMY_SETTERS` | `op:jimy,op:maria` | `POST /autonomy` refuses every change (403) — AOS-305; L4/L5 need both signatures (`--co-emitter`) |
| `AOS_APPROVERS_FILE` | 2 generated approvers | `POST /runs/{id}/approve` → 501 |
| `AOS_POLICY_BUNDLE_DIR` + `AOS_POLICY_TRUST_ANCHOR` | the committed signed bundle | PDP unloaded → **default-deny every tool call** |
| `AOS_AUTONOMY_LEVELS` + `AOS_AUTONOMY_DEFAULT` | `agt-1:fs=L4,agt-1:http=L2` / `L1` | no `escalate` verdict is ever emitted; the human-approval bridge is unreachable |
| `AOS_DURABLE_EXECUTION` + `AOS_EVENTSTORE_PATH` | on, `state/es.wal` | in-memory substrate; `wal-*` has nothing to read |
| `AOS_WORM_PATH` | `state/worm.log` | audit is a volatile MemStore |

Overrides: `AOS_DRIVER_PORT`, `AOS_DRIVER_HOME`, `AOS_DRIVER_READER`, `AOS_DRIVER_BOARD`,
`AOS_DRIVER_AUTONOMY`, plus `AOS_DRIVER_ALWAYS_BUILD` / `AOS_DRIVER_NO_BUILD`
([Binary freshness](#binary-freshness)).

### Other binaries

```bash
bash $D demo        # aos-demo: single-process zero-network apex (spawn → turn → approve → steer → pause)
bash $D selftest    # aos-attestation selftest (packed x5c attestation + wrong-challenge refusal)
```

`aos-orq` (lease-disciplined ORQ/SCH composition) prints its usage and exits 1 with no
args; it needs `serve --wal <file> --run <id>` against a WAL no other process holds.

## Direct invocation (what most PRs need)

Across the last 25 commits, `packages/cmd/aos` is by far the most-touched directory (42
changed files; the runner-up, `packages/substrate/eventstore/jetstream`, has 17). For a
change to internal code, the test suite is the fast handle — no node, no ports:

```bash
bash .claude/skills/run-aos/driver.sh test packages/cmd/aos
```

which is `cd packages/cmd/aos && go test -race -count=1 ./...` — **~47 s** for that
module. Second argument narrows the package set, e.g.
`bash $D test packages/platform/audit ./...` (~5 s).

There is no root `go.work`; every module is tested from its own directory.

CI gates, run directly (each is fail-closed and prints its own verdict):

```bash
bash scripts/ci/build.sh        # every module compiles (~2 min)
bash scripts/ci/policy-test.sh  # PDP golden truth table + bundle-signature fail-closed (~1 min)
bash scripts/ci/test.sh         # gate 3: unit suites -race + coverage → coverage/lcov.info (slow: every module)
```

`scripts/ci/run.sh` chains all of those plus `sast.sh` / `sca.sh`, which `go install`
`gosec` and `govulncheck` on first use — it needs network and was not exercised here.

The nine Python-backed gates (`deferrals`, `event-catalog`, `integration`, `ref-lint`,
`rtm`, `sbom`, `selftest`, `sign`, `verify-attestation`) run fine even though bare
`python3` on this machine resolves to the Microsoft Store stub: `ensure_python` in
`scripts/ci/lib.sh` provisions a shim into `.tools/` that delegates to the `python` on
PATH, and prepends it. Call them through their `.sh` wrapper, never the `.py` directly:

```bash
bash scripts/ci/deferrals.sh   # deferral markers ↔ registry, axis must cite a real AOS-NNN
bash scripts/ci/ref-lint.sh    # AOS/ADR cross-references resolve
bash scripts/ci/rtm.sh         # RTM in sync with the corpus
```

## Run (human path)

```bash
cd packages/cmd/aos
go build -o aos.exe .
AOS_API_ADDR=127.0.0.1:8080 ./aos.exe serve
```

This works, but you get a **maximally degraded node**: no operators (control channel
refuses everything and a non-loopback bind is rejected outright), no four-eyes, PDP
unloaded so every mediated tool call is denied, non-durable in-memory substrate. It is
useful only for reading the 50-line startup posture banner, which states exactly which
subsystem is off and which env var turns it on. `Ctrl-C` drains and exits.

## Gotchas

- **`audit-trail --run` takes a WORM *partition*, not a run id.** The flag name and the
  built-in help both say "tipicamente o RunID"; passing a run id returns **silently
  empty**. Real partitions are `gov.read/<run>`, `gov.residency/<run>`,
  `ingestion:<run>`, plus node-level `governance.control`, `autonomy`,
  `gov.sovereignty.authority`. Use `driver.sh partitions` to list them.
- **Denial codes are asymmetric.** `GET /runs/{id}` without `X-Aos-Reader`/`X-Aos-Board`
  returns **404 `{"error":"not found"}`** (anti-enumeration — a 403 would confirm the run
  exists). `POST /runs` returns **403 `{"error":"nao autorizado"}`**. Don't assert 403 on
  reads.
- **`AOS_AUTONOMY_LEVELS` splits the target at the *first* colon.** `agt-1:fs=L4` parses
  as agent `agt-1`, domain `fs`. `agent:demo:dominio:demo=L2` silently parses as agent
  `agent`, domain `demo:dominio:demo` — no error, and `GET /autonomy` shows the mangled
  pair. Use colon-free agent and domain ids (or the explicit `class:<class>:<domain>`
  form).
- **`AOS_POLICY_TRUST_ANCHOR` is hex; the repo stores the key as base64.**
  `packages/control-plane/pdp/policies/trust_anchor.pub` holds
  `tNHbo3n7mNWtl5Gt+GdRSkdUyrBjCdA+8TuoSPGReoY=`; the node wants
  `b4d1dba379fb98d5ad9791adf867514a4754cab06309d03ef13ba848f1917a86`. Convert with
  `base64 -d | xxd -p`. Setting `AOS_POLICY_BUNDLE_DIR` without the anchor aborts boot
  (`ErrPolicyBundleNeedsTrustAnchor`) — deliberately, so nobody reads the anchor out of
  the same mutable directory it is meant to verify.
- **Seed files must be UTF-8 with no BOM.** PowerShell's `>` and `Out-File` write a BOM
  or UTF-16; the loader rejects those (`ErrSeedUTF16`) or reports the far more confusing
  "nao e hex". Write them with `[IO.File]::WriteAllText(path, hex)` in PowerShell, or let
  `driver.sh keys` do it in bash.
- **`AOS_AUTONOMY_LEVELS` alone is ignored in silence.** The escalate oracle is an option
  of a *loaded* PDP: without `AOS_POLICY_BUNDLE_DIR` the levels are parsed and dropped,
  and `POST /autonomy` answers `oraculo de autonomia nao composto`.
- **The SSE trajectory stream never closes.** `curl` always exits 28 (timeout) or 23
  (SIGPIPE from `head`). That is normal termination, not an error — the driver swallows
  it, and any script of your own must too.
- **`cmd | grep -q` is a trap under `set -o pipefail`.** `grep -q` closes the pipe on
  first match, the producer takes SIGPIPE, and the pipeline returns 141 while the
  assertion actually passed. The driver's `smoke` captures into a variable instead; this
  bit twice while writing it.
- **The reference model makes no tool calls.** `referenceModel` returns one fixed, final
  text per turn at a constant 1500 micro-USD that "is not a measurement". So a bare node
  never reaches the PDP mediation path, never escalates, and never exercises four-eyes —
  no matter how many approvers you pin. Use `driver.sh demo` (`aos-demo`) for the
  plan-approval / dual-control apex, or compose a real gateway with `AOS_MODEL_ENDPOINT`
  + `AOS_MODEL_NAME`.
- **`GET /autonomy` is on the control plane but authenticates as a *read*.** It needs the
  `X-Aos-Reader`/`X-Aos-Board` headers, not a signature; without them it returns
  `{"error":"nao autorizado"}`.
- **Boot is loud and it is documentation.** 50 banner lines state, per subsystem,
  whether it is composed and the exact env var that composes it. When something behaves
  unexpectedly, `driver.sh logs` answers it faster than reading `main.go`, which is 140 KB
  on its own.
- **State persists across `up`.** `up` reuses `$(driver.sh home)/state`, so the WORM
  accumulates partitions from previous runs. `rm -rf $(bash $D home)/state` for a clean
  slate.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `aos: API devolveu 403 Forbidden: {"error":"nao autorizado"}` on `aos run` | Missing `--reader`/`--board`. The sovereign read-path is composed **by default**; use `--reader nhi:demo --board board:aos-demo`. |
| `{"error":"not found"}` on a run you just created | Same cause, read side. Add the two headers. |
| `{"error":"oraculo de autonomia nao composto (AOS_AUTONOMY_LEVELS ausente no arranque)"}` | `AOS_AUTONOMY_LEVELS` was set but `AOS_POLICY_BUNDLE_DIR` was not. Both, or neither. |
| `aos-issuer: o ficheiro da seed esta em UTF-16` | The seed was written by PowerShell `>`/`Out-File`. Rewrite it as UTF-8 no-BOM. |
| `make: command not found` | Not on PATH in Git Bash. Use `bash scripts/ci/<gate>.sh`. |
| `python3` opens the Microsoft Store | Only when you call `python3` (or a gate's `.py`) **directly**. The `.sh` wrappers work: `ensure_python` provisions a shim into `.tools/` that delegates to the `python` on PATH. Run `bash scripts/ci/<gate>.sh`, not `python3 scripts/ci/<gate>.py`. |
| `ERROR: The process "aos.exe" not found` from a manual `taskkill` | The driver builds with `go build -o .../aos`, so the Windows image name is **`aos`**, not `aos.exe`. Check with `tasklist \| grep -i aos`; `driver.sh down` kills both names. |
| A second `aos-orq serve --wal <f> --run <id>` exits **3** with `run já tem um lease válido detido` | Working as designed — the first instance holds the lease. Exit 4 = lease superseded/expired, 5 = the WAL is held by another *writer*. The file Event Store does not arbitrate between processes (sequential ownership, DEF-282). |
