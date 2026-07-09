# PDP — Policy Decision Point (AOS-004)

O **Policy Decision Point** é o par de decisão do Reference Monitor (PEP/RM,
AOS-003) no contrato **C1 RM↔PDP** (`tecnica/12 §4`). Avalia *policy-as-code*
declarativa compilada **em memória** e devolve, por cada tool call, uma decisão
determinística e pura — `permit` / `deny` / `escalate` — com `reason` legível, a
`policy_version` em vigor e as *obligations* que o PEP deve impor.

- **Módulo:** `github.com/aos-ref/control-plane/pdp`
- **Motor:** Cedar (`github.com/cedar-policy/cedar-go v1.8.0`)
- **API núcleo:** `PDP.Decide(ctx, Input) (Decision, error)` — determinística e sem efeitos
- **Integração:** `PolicyCheck` implementa o hook de política do RM

## Porquê Cedar (e não OPA/Rego)

O contrato C1 é **agnóstico à linguagem** — fixa apenas `request`/`response`
(`tecnica/12 §9`, nota de equivalência). `tecnica/12 §9` sanciona explicitamente
Cedar como equivalente ao Rego ("a escolha é um detalhe de implantação",
ADR-011). Escolhemos Cedar porque:

- **Supply-chain mínima** (ADR-005: pino + hash + assinatura). OPA/Rego arrasta
  uma árvore de dependências grande; `cedar-go` tem uma superfície reduzida
  (essencialmente `golang.org/x/exp`), alinhada com a tese do AOS de dependências
  congeladas e verificáveis.
- **Tipagem de entidades e análise estática** de políticas.
- **Avaliação pura em memória**, compatível com o alvo de overhead do RM.

A semântica da política de referência é **idêntica** à do Rego (ver abaixo); só
muda a sintaxe.

## Equivalência Rego ↔ Cedar

A política de referência `package aos.authz` (`tecnica/12 §9`) é **default-deny**
(fail-closed). Cedar nega por omissão: sem um `permit` aplicável, a decisão é
`deny` — o equivalente exacto de `default allow := false`. Cada `permit`
corresponde a uma regra `allow if { ... }`.

| Rego (`tecnica/12 §9`) | Cedar (`policies/aos_authz.cedar`) |
|---|---|
| `default allow := false` | *default-deny* implícito do Cedar |
| `allow if { capability=="cap:http.post"; "cap:http.post" in principal.authority; resource.region=="eu"; context.taint!="untrusted" }` | `@id("allow_http_post") permit(principal, action==Action::"cap:http.post", resource) when { principal.authority.contains("cap:http.post") && resource.region=="eu" && context.taint!="untrusted" };` |
| `allow if { capability=="cap:fs.read"; "cap:fs.read" in principal.authority }` | `@id("allow_fs_read") permit(principal, action==Action::"cap:fs.read", resource) when { principal.authority.contains("cap:fs.read") };` |
| `obligations contains {"type":"audit","level":"full"} if { allow }` | pós-avaliação determinística: `audit(level=full)` sempre que `permit` |
| `obligations contains {"type":"redact_pii","fields":["email","phone"]} if { allow; context.sensitivity=="confidential" }` | pós-avaliação: `redact_pii([email,phone])` se `permit` e `sensitivity=="confidential"` |

**Nota sobre obligations.** A decisão do Cedar é apenas `allow`/`deny`; as
*obligations* (que o Rego acumula num conjunto) são derivadas por pós-avaliação
determinística **coerente com a política** — só são emitidas sobre um `permit`,
exactamente como as regras Rego que exigem `allow`. Ver `engine_cedar.go`
(`obligationsFor`).

**Mapeamento Input → request Cedar:**

| Contrato C1 (`Input`) | Cedar |
|---|---|
| `principal.id` | entidade `Principal::"<id>"` |
| `principal.authority` | atributo `principal.authority : Set<String>` |
| `capability` | `action` = `Action::"<capability>"` |
| `resource.value` | entidade `Resource::"<value>"` |
| `resource.region` | atributo `resource.region : String` |
| `context.taint` | `context.taint : String` |
| `context.sensitivity` | `context.sensitivity : String` |

### Tabela-verdade (testes golden)

| Caso | Decisão | Obligations |
|---|---|---|
| `http.post` + authority tem `cap:http.post` + region=eu + taint≠untrusted | **permit** | `audit` (+`redact_pii` se sensitivity=confidential) |
| `http.post` mas region≠eu | **deny** | — |
| `http.post` mas taint=untrusted | **deny** | — |
| `http.post` sem a capability na authority | **deny** | — |
| `fs.read` + authority tem `cap:fs.read` | **permit** | `audit` |
| `fs.read` sem a capability | **deny** | — |
| qualquer capability não coberta | **deny** (default-deny) | — |

Cobertas em `TestDecide_GoldenTruthTable` (`pdp_test.go`). Todos os casos assumem
uma `agent_class` cuja **allowlist de capabilities** (AOS-007, abaixo) concede a
capability pedida — o gate default-deny corre **antes** das regras Cedar.

## Capability allowlist default-deny (AOS-007)

A blocklist de tools de sub-agente **falhava aberta** a cada tool nova. O PDP
substitui-a por uma **allowlist capability-scoped default-deny**: o que não está
explicitamente concedido é negado.

- **Recurso:** `policies/capabilities/allowlist.json` — declarativo, keyed por
  `agent_class`, enumerando as capabilities de cada classe. **Faz parte do bundle
  assinado** (entra no `content_hash` e na assinatura ed25519): adicionar/alterar
  uma capability **exige re-assinatura** (`AC#4` — sem allow implícito).
- **Gate:** `PDP.Decide` impõe, **antes** de qualquer regra Cedar, que a
  `(agent_class, capability)` conste explicitamente da allowlist. A decisão final
  `permit` exige **allowlist ∧ regras Cedar**. Ausência de concessão ⇒ `deny`
  (fail-closed), com `reason` que contém `default-deny` e nomeia a classe.
- **Identidade (AOS-005/006):** a `agent_class` chega ao PDP resolvida pelo hook
  de identidade a partir do claim `agent_class` do token NHI → `rm.Principal.AgentClass`
  → `pdp.Input.Principal.AgentClass`. A negação é auditada pelo RM (o evento de
  mediação carrega `capability` + `principal` incl. `agent_class`).
- **Sem wildcards perigosos por omissão:** uma entrada `"*"` só concede se trouxer
  `justification` não-vazia; um wildcard sem justificação é **ignorado** (não
  concede nada).

```json
{
  "schema_version": 1,
  "classes": {
    "agent-worker": { "capabilities": [ {"cap": "cap:http.post"}, {"cap": "cap:fs.read"} ] },
    "agent-reader": { "capabilities": [ {"cap": "cap:fs.read"} ] },
    "agent-break-glass": { "capabilities": [ {"cap": "*", "justification": "break-glass auditado, revisto trimestralmente"} ] }
  }
}
```

| Caso (gate da allowlist) | Decisão |
|---|---|
| classe lista a capability | segue para as regras Cedar |
| classe **não** lista a capability (tool nova) | **deny** default-deny |
| `agent_class` vazia / classe desconhecida | **deny** default-deny |
| classe com wildcard **justificado** | concede qualquer capability |
| wildcard **sem** justificação | ignorado (não concede) |

Coberto em `capabilities_test.go`: default-deny, "tool nova falha fechada", allow
explícito por política assinada, **fuzz** de capabilities aleatórias (0 falso
allow — `TestDecide_Fuzz_ZeroFalsoAllow` + `FuzzDecide_CapabilityNuncaEscapaAllowlist`),
adulteração da allowlist ⇒ `ErrSignatureInvalid`, e integração RM (negação
auditada com capability + principal).

## Bundle assinado e versionado

A política vive num **bundle** em `policies/`:

| Ficheiro | Papel | Committado? |
|---|---|---|
| `aos_authz.cedar` | fonte da política (regras Cedar) | sim |
| `capabilities/allowlist.json` | allowlist de capabilities por `agent_class` (AOS-007) | sim |
| `manifest.json` | `policy_version` (SemVer) + `content_hash` (sha256 canónico, cobre `.cedar` **e** allowlist) + `created_at` | sim |
| `aos_authz.sig` | assinatura **ed25519** (base64) | sim |
| `trust_anchor.pub` | chave **pública** ed25519 (base64) | sim |
| `signing.key` | chave **privada** ed25519 | **NÃO** (gitignored via `*.key`) |

**Integridade e autenticidade (fail-closed).** A assinatura cobre uma mensagem
canónica que liga `policy_version` **e** `content_hash`
(`aos.policy.bundle.v1\n<version>\n<hash>\n`). No `Open`:

1. recomputa `content_hash` dos `.cedar` **e** da allowlist de capabilities e
   compara com o manifest — divergência ⇒ adulteração ⇒ `ErrSignatureInvalid`
   (`E_SIGNATURE_INVALID`);
2. verifica a assinatura ed25519 contra o `trust_anchor.pub` — falha ⇒
   `ErrSignatureInvalid`;
3. compila a policy set Cedar em memória (uma vez).

Bundle **ausente/não-carregado** ⇒ `ErrPolicyUnavailable` (`E_POLICY_UNAVAILABLE`),
tratado como `deny`. Request malformado ⇒ `ErrMalformedRequest`
(`E_MALFORMED_REQUEST`). Todos resolvem pelo lado seguro: **ausência de `permit`
explícito é negação**.

**Hot-reload.** `PDP.Reload()` só aceita uma versão **nova e assinada**
(SemVer estritamente crescente); versão igual/anterior ou bundle não-confiável são
rejeitados sem alterar a política em vigor. Não muta decisões já emitidas.

### (Re)assinar o bundle

```sh
go run ./cmd/policy-sign -dir policies -version 1.0.0
```

A chave privada vive **fora do repositório** (por omissão `~/.aos/keys/signing.key`;
sobreponível com `-key`). Se não existir, gera um novo par ed25519, escreve a
chave privada nesse caminho e o `trust_anchor.pub` **público** no bundle; caso
exista, reutiliza-a. A verificação em runtime só precisa da chave **pública**.
**Nenhum segredo é materializado na árvore do projecto nem committado.**

**Trust anchor.** Por omissão o anchor é lido de `trust_anchor.pub` no dir do
bundle — seguro só se esse dir for provisionado out-of-band e read-only. Onde o
dir for gravável por terceiros (ex.: hot-reload), fornecer o anchor a partir de
uma fonte confiável via `pdp.Open(dir, pdp.WithTrustAnchor(anchor))`.

## Integração no Reference Monitor

`PolicyCheck` (`rmadapter.go`) implementa a interface `Hook` do RM, ocupando o
ponto de injecção do antigo `PolicyStub`. Traduz `Call → Input`, invoca
`Decide`, e traduz `Decision → HookResult` — propagando `policy_version` e
`obligations`. O RM leva a `policy_version` ao **evento de mediação** no Event
Store (AOS-002), cumprindo o critério de audit do AOS-004.

```go
p, _ := pdp.Open("policies")
m := rm.New(
    // Identidade REAL antes do PDP: o IdentityCheck resolve e re-deriva o Principal
    // (incl. agent_class) do token NHI verificado — ver "Fronteira de confiança".
    rm.WithHooks(identity.NewIdentityCheck(verifier), pdp.NewPolicyCheck(p), rm.BudgetStub{}, rm.EgressStub{}, rm.AuditStub{}),
    rm.WithEventSink(rm.NewEventStoreSink(store)),
)
```

> **Fronteira de confiança (AOS-007) — não usar `IdentityStub` antes do gate.** O
> gate default-deny da allowlist decide sobre `(agent_class, capability)` e trata a
> `agent_class` como **já resolvida de uma NHI verificada**. Compor o PDP atrás do
> `rm.IdentityStub{}` neutro (pass-through) é **inseguro**: a `agent_class` vem então
> do `Call` bruto do caller e é **forjável** — um caller troca a sua classe real por
> uma de maior privilégio (ex.: `agent-worker`, ou `agent-break-glass` com wildcard)
> e amplifica capabilities. Em produção componha o `identity.NewIdentityCheck`
> (AOS-005/006), que **substitui o `Call.Principal` inteiro** a partir do token
> verificado, imediatamente antes do `PolicyCheck`. O teste
> `TestIntegration_IdentityGate_ForgedAgentClassIgnored_Deny`
> (`identity_gate_integration_test.go`) prova que, nessa composição, uma
> `agent_class` forjada no `Call` é ignorada e o gate decide pela classe do token.

Um `permit` da política ⇒ `Mediate` permit + evento `tool.call.mediated` com
`policy_version`; um `deny` ⇒ `Mediate` deny + evento `tool.call.denied` com
`policy_version` (`TestIntegration_RM_PDP_*`).

### Alteração mínima ao RM (AOS-003)

Aditiva e retro-compatível: campo `PolicyVersion` em `HookResult`,
`MediationRecord` e no payload do evento (`policy_version,omitempty`); o
`Mediate`/`fail` propagam-no. Toda a suite AOS-003 permanece verde.

## Testes

```sh
export CGO_ENABLED=1   # -race exige gcc (mingw)
go vet ./...
go test ./... -race -count=1 -covermode=atomic -coverprofile=cover.out
go tool cover -func=cover.out | tail -1
go test -run '^$' -bench BenchmarkDecide -benchtime=100000x
```

- **Golden de política** — tabela-verdade completa (allow/deny + reason + obligations).
- **Bundle** — verifica válido; rejeita adulterado (conteúdo/versão/hash), não-assinado e anchor errado; ausente ⇒ `ErrPolicyUnavailable`; hot-reload só versão nova assinada.
- **policy_version** — registada na `Decision` (unit) e no evento de mediação do RM (integração).
- **Latência** — `BenchmarkDecide` e `BenchmarkMediate_RM_PDP`; tripwires p95 < 15 ms.

## Âmbito (AOS-004)

**Dentro:** PDP + bundle assinado/versionado + integração via `PolicyCheck` +
capability allowlist default-deny (AOS-007).
**Fora:** identidade real (AOS-005/006, integrada), orçamento/egress reais
(AOS-008/009), CI (AOS-010).
