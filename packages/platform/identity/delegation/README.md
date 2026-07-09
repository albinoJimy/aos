# delegation — Cadeia de delegação on-behalf-of até humano (AOS-006)

Subpacote de `platform/identity` que implementa a **cadeia de delegação
on-behalf-of** do AOS: a estrutura que liga cada tool call de um agente, subindo
por cada delegação, até um **humano responsável** identificável na raiz.

Resolve o cenário de governação **"The Audit Log Lied"**: quando o regulador
pergunta *quem autorizou* uma acção, a resposta nunca pode ser "o pool" — é
sempre o `human_principal` único na raiz da cadeia (ADR-003, `tecnica/09` §3).

## Modelo

Cada elo (`Link`) declara:

| Campo | Significado |
|---|---|
| `Sub` | quem delega neste elo (na raiz, `human:<user_id>`; depois, o agente-pai) |
| `ActAs` | para quem se delega (o agente que passa a agir *on-behalf-of* `Sub`) |
| `Authority` | escopo (capabilities) concedido **neste** elo |
| `Depth` | profundidade (0 na raiz, +1 por delegação) |
| `PrevHash` | hash do elo anterior (vazio na raiz — âncora de génese) |

A `Chain` é **ordenada da raiz (humano) até à folha (agente actual)**. Encadear
cada elo pelo `sha256` do anterior (`PrevHash`) torna a **ordem** da cadeia
*tamper-evident*: reordenar, inserir ou remover um elo quebra o encadeamento.

```
human:alice ──delega──▶ orchestrator ──▶ planner ──▶ worker
 Depth 0                 Depth 1          Depth 2      Depth 3
 {a,b,c}         ⊇       {a,b}      ⊇     {a}     ⊇    {a}
 PrevHash=""             hash(elo0)       hash(elo1)   hash(elo2)
```

## Invariantes verificadas (`Chain.Verify`)

Toda a verificação é **fail-closed** (erro sentinela comparável com `errors.Is`):

- **raiz humana** — o primeiro elo tem `Sub` com prefixo `human:`; senão a cadeia
  é **órfã** (`ErrOrphanChain`) e é negada. *0 cadeias órfãs.*
- **profundidade monotónica** — `Depth` começa em 0 e incrementa +1 por elo
  (`ErrDepthNonMonotonic`).
- **continuidade** — `PrevHash` de cada elo é exactamente `LinkHash(elo anterior)`
  (`ErrHashMismatch`).
- **não-escalada** — `Authority(i) ⊆ Authority(i-1)`: a autoridade só pode
  **estreitar** ao descer, nunca alargar (`ErrScopeEscalation`).

## Modelo de confiança (declarado honestamente)

A cadeia **não é auto-suficiente criptograficamente**. Os hashes encadeados só
provam a **integridade da ordem** interna dos elos — não a autenticidade de quem
os emitiu. A **autenticidade** vem de a cadeia inteira ir embebida nos claims do
token NHI e ser **selada pela assinatura `ed25519` do emissor** (AOS-005):

> `token = base64(header).base64(claims{…, delegation_chain}).base64(sig_emissor)`

Adulterar qualquer elo (sujeito, autoridade, ordem) invalida a assinatura do
token. Assim, a propriedade "cada elo assina o seguinte" está **ancorada na
assinatura única do emissor sobre a cadeia completa**. O verificador
(`identity.Verifier.Verify`) valida primeiro a assinatura e só depois percorre a
cadeia; o Reference Monitor (AOS-003) nega e audita se a verificação falhar.

**Consequência (assumida):** a confiança é depositada no **emissor**. Um emissor
comprometido poderia forjar cadeias arbitrárias (dentro das invariantes). Para o
mitigar existe defesa-em-profundidade no verificador (impõe as invariantes
independentemente do emissor) e o TTL curto dos tokens.

### Endurecimento futuro (fora do escopo de AOS-006)

Uma variante mais forte daria a **cada principal a sua própria chave**, com cada
elo assinado pela chave do respectivo `Sub` (delegação por prova de posse, à la
*macaroons*/*biscuit* ou *DPoP* encadeado). Isso removeria o emissor como ponto
único de confiança, ao custo de gestão de chaves por agente. Fica registado como
endurecimento futuro.

## API

```go
// Construção
chain, err := delegation.NewRoot("human:alice", "orchestrator", authority)
child, err := chain.Extend("planner", narrowerAuthority) // recusa escalada

// Verificação e reconstrução de autoria
err := chain.Verify()                    // todas as invariantes, fail-closed
human, err := chain.HumanPrincipal()     // "quem autorizou" = raiz humana
leaf, ok := chain.Leaf()                 // o agente actual
```

No pacote `identity`, a emissão e o consumo estão integrados:

- `Issuer.Issue` — embute a cadeia **raiz** (`human:<user_id> → agente`).
- `Issuer.IssueChild(ctx, parentToken, ChildRequest)` — verifica o pai, rejeita
  escalada (`ErrDelegationInvalid` que envolve `ErrScopeEscalation`), estende a
  cadeia e emite o token filho selado.
- `Verifier.Verify` — valida a cadeia embebida e expõe `Principal.DelegationChain`
  e `Principal.HumanPrincipal()`.
- `identity.AuthorFromEvent` — reconstrói o autor humano a partir de um evento de
  tool call lido do Event Store (AOS-002).

## Propriedades

- **Zero dependências externas** — só a stdlib (`crypto/sha256`, `encoding/json`).
- **Puro** — não importa `identity` (é `identity` que importa este subpacote).
- **Determinístico** — `LinkHash` normaliza a autoridade (`nil` ≡ `[]`) e usa
  ordem de campos fixa; testes *table-driven* e `-race` limpos.
