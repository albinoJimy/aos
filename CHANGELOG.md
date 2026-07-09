# Changelog

Todas as alterações relevantes deste repositório. Formato baseado em
[Keep a Changelog](https://keepachangelog.com/) e alimentado por
[Conventional Commits](https://www.conventionalcommits.org/) — ver
[specs/01_Engineering_Standards_e_Handoff.md](specs/01_Engineering_Standards_e_Handoff.md) §5.

## [Unreleased]

### Added — AOS-007 Capability allowlist default-deny
- `feat(AOS-007): capability allowlist default-deny no PDP` — allowlist de capabilities por `agent_class` como recurso de política assinado (`packages/control-plane/pdp/policies/capabilities/allowlist.json`), **parte do content_hash + assinatura ed25519 do bundle** (adicionar capability exige re-assinatura). **Gate default-deny** em `PDP.Decide` antes das regras Cedar (permit = allowlist ∧ Cedar); capability não listada → deny fail-closed, auditado pelo RM com capability + principal (incl. `agent_class`). **Sem wildcards por omissão** (só com `justification` explícita).
  - `agent_class` flui identity → RM → PDP por alterações mínimas aditivas (`Principal.AgentClass`, +10 linhas nos 3 módulos). Fuzz (loop determinístico 3000 + `FuzzDecide` nativo ~273k execs) com **0 allow indevido**.
  - Auditoria adversarial apanhou e a remediação corrigiu (medium): a composição de referência usava o `IdentityStub` (pass-through) → `agent_class` **forjável** para amplificar capabilities. Corrigido no README (composição segura com `IdentityCheck` real) + **teste cross-package** que prova que a classe forjada é ignorada em favor da NHI verificada.
  - `-race` limpo nos 3 módulos; cobertura pdp 83.9%; AOS-002..006 verdes. Bundle re-assinado (chave privada fora do repo).

### Added — AOS-006 Cadeia de delegação on-behalf-of
- `feat(AOS-006): cadeia de delegação on-behalf-of até humano em packages/platform/identity/delegation` — `delegation_chain` de elos `{sub, act_as, authority, depth, prev_hash}` **hash-encadeados** (ordem tamper-evident), embebida no token NHI e **selada pela assinatura do emissor** (AOS-005). `IssueChild` on-behalf-of: filho herda a cadeia do pai + novo elo, **autoridade = intersecção (filho ⊆ pai)**, escalada rejeitada fail-closed. Raiz sempre `human:<id>`; **cadeia órfã** (raiz não-humana) → deny + audit no RM. `delegation_chain` em cada evento de tool call → **reconstrução de "quem autorizou"** (`AuthorFromEvent`).
  - Auditoria adversarial apanhou e a remediação corrigiu (medium): `Verify` não vinculava `user_id` à raiz humana selada → duas fontes divergentes de autoria ("The Audit Log Lied" sob emissor comprometido). Agora exige `root.Sub == human:<user_id>` **e** `scope ⊆ leaf.Authority` (fail-closed).
  - Estende o módulo AOS-005 (não módulo novo); mudança ao reference-monitor limitada a +26 linhas (`delegation_chain` no payload de mediação). `-race` limpo, cobertura 89.4% (delegation 92.2%); AOS-002/003/004/005 **todos verdes**. Modelo de confiança (ancorado no emissor; PKI por-principal = endurecimento futuro) documentado.

### Added — AOS-005 Identidade não-humana (NHI)
- `feat(AOS-005): identidade não-humana por agente em packages/platform/identity` — emissor/verificador de tokens NHI **scoped + time-bound**, assinados **EdDSA/ed25519** (JWS compacto, só stdlib, **zero deps**; rejeita `alg=none`). Claims `(user_id, agent_id, agent_class, policy_ref, scope, exp, jti)`; TTL e escopo **por classe**; autoridade = **utilizador ∩ classe** (filho ⊆ pai em on-behalf-of).
  - `Verify` fail-closed: assinatura, `exp`/`nbf` (relógio injectável), revogação, emissor, `typ` — resolve o `Principal` para o PDP. Integrado no **`IdentityCheck` do RM** (substitui o `IdentityStub` do AOS-003): chamada sem NHI **não prossegue** (proibição de anónimo/round-robin). Emissão/revogação gravadas como eventos no Event Store (só metadados, nunca o token/assinatura).
  - Auditoria adversarial apanhou e a remediação corrigiu: `randomJTI` engolia o erro do CSPRNG (→ `jti` constante, revogação demasiado ampla + gap de auditoria) — agora **fail-closed**; `typ` validado em `Verify` (defesa-em-profundidade); guard contra receiver nil na revogação.
  - Mudança ao reference-monitor: **+7 linhas** (`Call.Credential`), AOS-003 e AOS-004 verdes. `BenchmarkVerify` ~44 µs/op (≈300× abaixo do orçamento). Cobertura 90.8%, `-race` limpo nos três módulos. Sem material de chave no repo.

### Added — AOS-004 PDP (policy-as-code)
- `feat(AOS-004): PDP com policy-as-code (Cedar) em packages/control-plane/pdp` — `Decide(input) → {allow|deny|escalate, reason, policy_version, obligations}`, motor **Cedar** (`cedar-go` v1.8.0) compilado em memória, integrado no Reference Monitor via `PolicyCheck` (substitui o stub do AOS-003). Política de referência (tecnica/12 §9, Rego) reproduzida em Cedar com tabela-verdade idêntica.
  - **Bundle versionado + assinado (ed25519 + sha256 canónico)**: verificação no load, **fail-closed** — `ErrSignatureInvalid` (adulterado/não-assinado/anchor errado) e `ErrPolicyUnavailable` (ausente) tratados como deny. `policy_version` gravada no evento de mediação (mudança mínima aditiva ao reference-monitor, AOS-003 verde).
  - **Sem segredos no repo**: só a chave pública (`trust_anchor.pub`) + assinatura; chave privada gerada fora da árvore (`~/.aos/keys`, e `*.key` gitignored). `cmd/policy-sign` (re)assina.
  - Decisão de motor: Cedar em vez de OPA/Rego — OPA traz uma árvore de deps enorme (supply-chain, contra ADR-005); tecnica/12 §9 sanciona Cedar como equivalente. Documentado no README.
  - `BenchmarkDecide` ~3 µs/op, RM+PDP ~4.3 µs/op — p95 ≪ 15 ms. Cobertura 83.3%, `-race` limpo nos dois módulos.
  - Aberto (documentado): persistir o evento de mudança-de-política diretamente no Event Store (sub-parte adiada; há já `PolicyChangeEvent` + hook `WithReloadAudit`).

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
