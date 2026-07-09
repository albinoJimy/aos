# `platform/identity` — Identidade não-humana por agente (NHI)

**Ticket:** AOS-005 · **ADR:** ADR-003 · **Componente:** parte de `platform/` (GOV/identidade)
**Módulo Go:** `github.com/aos-ref/platform/identity` · **Dependências externas:** _nenhuma_ (só stdlib)

Segunda fundação não-negociável do AOS: **identidade antes de autoridade**. Cada
agente e sub-agente é uma *non-human identity* (NHI) única, portadora de um token
*scoped* (capabilities/recursos) e *time-bound* (TTL curto) que codifica o par
`(utilizador, agente)` e a classe/política sob a qual actua. O `credential pool`
round-robin anónimo — que destrói a atribuição de identidade — é **proibido**.

## Formato do token NHI

Envelope **JWS compacto** assinado com **EdDSA (ed25519)**, construído só com a
stdlib (`crypto/ed25519`, `encoding/json`, `encoding/base64`) — sem bibliotecas
JWT de terceiros:

```
base64url(header) "." base64url(claims) "." base64url(assinatura)
```

**Header:** `{"alg":"EdDSA","typ":"NHI","kid":"<iss>"}`. O único `alg` aceite é
`EdDSA`; qualquer outro (incluindo `none`) é rejeitado **antes** de olhar a chave
(defesa contra confusão *alg/none*).

**Claims:**

| Claim | Significado |
|---|---|
| `user_id` | Humano responsável (raiz da cadeia de delegação). |
| `agent_id` | Identidade única do agente — a NHI. |
| `agent_class` | Classe sob cuja política o agente actua. |
| `policy_ref` | Referência de política (AOS-004). |
| `scope[]` | Capabilities/recursos concedidos = **utilizador ∩ classe** (⊆ pai em on-behalf-of). |
| `iss` | Emissor; a verificação usa a sua chave pública (trust anchor). |
| `iat` / `nbf` / `exp` | Janela temporal (segundos Unix); `exp-iat` = TTL curto por classe. |
| `jti` | Identificador único do token (usado na revogação). |

A assinatura cobre `base64url(header).base64url(claims)`. Adulterar qualquer
segmento invalida a assinatura.

## Componentes

- **`Issuer.Issue(ctx, IssueRequest) (Token, error)`** — emite o token. TTL e
  escopo-máximo são configuráveis **por classe** (`ClassPolicy`). A autoridade
  embutida é a **intersecção `UserAuthority ∩ ClassPolicy.Scope`** — nunca
  alarga. Se `ParentScope` for fornecido (delegação *on-behalf-of*), o escopo do
  filho é ainda intersectado com o do pai, garantindo **filho ⊆ pai**. A emissão
  grava `identity.nhi.issued` no Event Store (só metadados: `jti`, `user_id`,
  `agent_id`, `agent_class`, `exp`, …) — **nunca o token bearer nem a assinatura**.

- **`Verifier.Verify(ctx, token) (Principal, error)`** — valida, por esta ordem,
  algoritmo (EdDSA), emissor (trust anchor), **assinatura**, janela temporal
  (`nbf`/`exp`, com relógio injectável) e **revogação**. Em sucesso resolve um
  `Principal` (`agent_id`, `scope`, cadeia de delegação até ao humano). Rejeita
  **fail-closed** (sentinela comparável por `errors.Is`):

  | Sentinela | Causa |
  |---|---|
  | `ErrTokenMalformed` | Não é um JWS de 3 segmentos base64url. |
  | `ErrUnsupportedAlg` | `alg` ≠ EdDSA (inclui `none`). |
  | `ErrSignatureInvalid` | Assinatura inválida / token adulterado / chave errada. |
  | `ErrUnknownIssuer` | `iss` sem trust anchor. |
  | `ErrTokenNotYetValid` | `now < nbf`. |
  | `ErrTokenExpired` | `now >= exp` ou sem `exp`. |
  | `ErrTokenRevoked` | `jti` revogado ou revogação indisponível. |

- **`Revocations.Revoke(ctx, jti)`** — acrescenta o `jti` ao conjunto de
  revogação e grava `identity.nhi.revoked` no Event Store (idempotente por
  `jti`). O `Verifier` consulta-o; o TTL curto minimiza a janela.

## Integração no Reference Monitor — proibição de anónimo

`IdentityCheck` é o hook `identity` do RM (AOS-003), no lugar do antigo
`IdentityStub`. Em cada `Monitor.Mediate`:

1. Lê o token de `Call.Credential`.
2. Verifica-o (`Verifier.Verify`).
3. Impõe a **fronteira de escopo**: a `Call.Capability` tem de estar no `scope`
   do token (senão `ErrOutOfScope`); o PDP (AOS-004) aplica ainda a sua política.
4. Em sucesso **resolve o `Principal`** (`NHIID = agent_id`, `Authority = scope`,
   cadeia de delegação) por mutação do `*Call` partilhado, e devolve *permit*.

> **Proibição de identidade anónima/round-robin (ADR-003):** sem token, ou com
> token inválido/expirado/fora-de-escopo/revogado, o hook devolve **DENY** — a
> chamada mediada **NÃO prossegue** e a negação é auditada pelo RM. Nenhuma tool
> call executa sem NHI resolvida.

```go
v := identity.NewVerifier(
    identity.WithTrustedIssuer(iss.IssuerID(), iss.PublicKey()),
    identity.WithRevocations(revocations),
)
m := rm.New(
    rm.WithHooks(identity.NewIdentityCheck(v), pdp.NewPolicyCheck(engine) /* … */),
    rm.WithEventSink(rm.NewEventStoreSink(store)),
)
// call.Credential = tok.Compact  → permit + Principal resolvido
// call.Credential = ""           → deny (anónimo proibido)
```

## Cadeia de delegação on-behalf-of até humano (AOS-006)

O subpacote [`delegation`](delegation/README.md) estende a NHI com a **cadeia de
delegação** completa, embebida no claim `delegation_chain` e **selada pela
assinatura do token**. A cadeia é ordenada da **raiz humana** (`human:<user_id>`)
até ao agente actual; cada elo é encadeado por hash ao anterior (ordem
*tamper-evident*).

- **`Issuer.Issue`** embute a cadeia **raiz** (`human:<user_id> → agente`).
- **`Issuer.IssueChild(ctx, parentToken, ChildRequest)`** — emissão *on-behalf-of*:
  verifica o token pai, extrai a sua cadeia, **rejeita escalada** de autoridade
  (`ErrDelegationInvalid`, que envolve `delegation.ErrScopeEscalation`), estende a
  cadeia com um novo elo (`Depth+1`, `PrevHash = hash(folha)`) e emite o token
  filho. A autoridade do filho é `pedido ∩ classe`, sempre **⊆ folha do pai**.
- **`Verifier.Verify`** valida a cadeia embebida (raiz humana, não-escalada,
  encadeamento de hash, folha = `agent_id`) — falha **fail-closed** com
  `ErrDelegationInvalid`. Expõe `Principal.DelegationChain` e
  `Principal.HumanPrincipal()`.
- **`IdentityCheck`** propaga a cadeia **completa** ao `Principal` do RM, que a
  grava em cada evento de tool call (`Producer.DelegationChain` **e** no payload
  da mediação).
- **`AuthorFromEvent(ev)`** reconstrói *quem autorizou* a partir de um evento de
  tool call lido do Event Store: o autor é o humano na raiz da cadeia
  (fail-closed em cadeia vazia/órfã).

> Uma cadeia que não resolva até um humano (**órfã**) é **negada e auditada** pelo
> RM — 0 cadeias órfãs. Ver o modelo de confiança em [`delegation/README.md`](delegation/README.md).

## Chaves

O emissor detém a chave **privada** ed25519 **fora da árvore do repo** (injectada
ou gerada em runtime). Como os tokens são efémeros (TTL curto, emitidos em
runtime), **nenhum material de chave é committado**. Os testes geram pares
efémeros. A separação identidade vs chaves de infra do provider é reforçada pelo
Credential Broker (EPIC-06/07), fora do escopo de AOS-005.

## Testes

```bash
export PATH="$HOME/scoop/apps/mingw/current/bin:$HOME/scoop/shims:$PATH"; export CGO_ENABLED=1
go vet ./...
go test ./... -race -count=1 -covermode=atomic -coverprofile=cover.out
go tool cover -func=cover.out | tail -1
```

Cobre: emissão+verificação de NHI válida; rejeição de expirado / ainda-não-válido
/ adulterado / alg-none / chave-errada / emissor-desconhecido / revogado
(fail-closed); `user ∩ classe` e `filho ⊆ pai`; proibição de chamada mediada sem
NHI (deny + audit, RM real); emissão **e** revogação como eventos no Event Store
**real** (AOS-002); `BenchmarkVerify` e o caminho `identity+policy` do RM.
