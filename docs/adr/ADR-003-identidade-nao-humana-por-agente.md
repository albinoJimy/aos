# ADR-003 — Identidade não-humana por agente (NHI scoped/time-bound, com binding humano↔NHI auditável)

| Campo | Valor |
|---|---|
| Estado | **Aceite — ratificado** (AOS-176, frente 3 do D4 / EPIC-16; Julho 2026). Materializa o enunciado do catálogo (`specs/00_System_Spec.md` §11) e formaliza o **processo/autoridade** do binding humano↔NHI. |
| Data | Julho de 2026 |
| Decisores | Arquitecto de Plataforma + Responsável de Segurança (ver Tabela de aprovação) |
| Tickets | AOS-176 (binding auditável + ADR-003 formal); depende de AOS-174 (IdP OIDC), AOS-175 (custódia de chave externa), AOS-006 (cadeia de delegação), AOS-002 (Event Store) |
| ADRs relacionados | ADR-006 (Credential Broker JIT — separação identidade↔segredos), ADR-010 (audit WORM), ADR-016 (fronteira de confiança da camada de UI — superfície onde o humano autoriza), ADR-002 (Reference Monitor mandatório) |
| Supersede | — |

> Este ADR fixa que **toda a identidade não-humana (NHI) é por-agente, scoped e
> time-bound, e enraíza numa cadeia de delegação que termina num HUMANO responsável**
> (zero acções "pool", `specs/00_System_Spec.md` §13.2). Formaliza o **processo pelo
> qual um humano autenticado autoriza uma NHI** e o **registo auditável desse binding**.

---

## 1. Contexto

O `specs/00_System_Spec.md` §13.2 impõe: *toda a acção resolve para uma cadeia de
delegação que termina num humano responsável; zero acções "pool"*. Uma identidade de
serviço partilhada (uma chave de API por frota, um utilizador técnico "robot" comum)
quebra esta invariante — quando um efeito é observado no log, "quem autorizou" resolve
para um principal não-humano, e a autoria dilui-se ("The Audit Log Lied"). Uma NHI
tem de ser **por-agente** (uma identidade única por agente instanciado), **scoped** (a
autoridade é a intersecção utilizador ∩ classe, nunca alarga) e **time-bound** (TTL
curto, janela de revogação mínima).

O **mecanismo** já estava construído e provado (Camada A do D4, EPIC-14):

| Peça | Ficheiro real | Garantia |
|---|---|---|
| Emissão de NHI scoped/time-bound | `packages/platform/identity/issuer.go` (`Issue`) | autoridade = utilizador ∩ classe; TTL por classe; jti CSPRNG fail-closed |
| Cadeia de delegação raiz-humana | `packages/platform/identity/delegation/chain.go` (`NewRoot`, `Verify`, `HumanPrincipal`) | raiz `human:<id>` obrigatória (senão `ErrOrphanChain`); profundidade monotónica; hash-chain tamper-evident; não-escalada |
| Autoridade de identidade separada | `packages/integration/issuer_authority.go` (`IssuerAuthority`, `MintForHuman`/`MintForAssertion`) | o nó recebe só a pubkey (`TrustAnchor`); um nó comprometido não forja |
| Autenticação humana real (OIDC) | `packages/integration/oidc/`, `packages/integration/oidc_directory.go` (`OIDCDirectory`) | o humano na raiz é o `sub` VERIFICADO de um ID-token (AOS-174) |
| Custódia de chave fora do nó | `identity.NewIssuerWithSigner` + `AuthorityConfig.Signer` (AOS-175) | a chave privada pode viver num HSM/KMS; nunca entra no processo do nó |
| Registo de emissão | `packages/platform/identity/events.go` (`identity.nhi.issued`), `issuer.go` (`recordIssued`) | evento append-only, idempotente por jti, só metadados |

O que **faltava** (AOS-176) não era mecanismo, mas **formalização**: (a) este ADR
escrito; e (b) o binding humano↔NHI como **registo auditável de primeira classe** —
não apenas um evento de emissão, mas um registo consultável que responda, para qualquer
NHI, *quem autorizou · quando · por que método/autoridade*.

---

## 2. Decisão

**Toda a NHI é por-agente, scoped e time-bound, e o binding humano↔NHI cria-se quando
um humano AUTENTICADO autoriza a NHI no MINT; regista-se de forma auditável no Event
Store; e verifica-se pela cadeia de delegação + reconciliação do verifier.**

### 2.1 Processo/autoridade do binding (o "quem cria/autoriza uma NHI")

O binding nasce no acto de emissão (mint), por esta cadeia de responsabilidade — cada
elo num ficheiro real:

1. **O humano autentica-se.** A autoridade de identidade consulta uma
   `HumanDirectory` (`packages/integration/issuer_authority.go`). A impl de produção é
   o `OIDCDirectory` (`oidc_directory.go`): valida um ID-token OIDC
   (discovery + JWKS + assinatura + claims, só stdlib) e devolve o `sub` **verificado**.
   Um humano não-autenticado ⇒ mint **recusado fail-closed** (`ErrHumanNotAuthenticated`);
   nenhuma NHI é emitida nem registada.
2. **A autoridade minta on-behalf-of o humano.** `IssuerAuthority.MintForAssertion`
   (a via de produção — o humano é DERIVADO da prova, não afirmado) chama
   `identity.Issuer.Issue`, que sela `delegation.NewRoot("human:<sub>", agentID, escopo)`.
   `NewRoot` **exige** a raiz humana — uma cadeia órfã é recusada com `ErrOrphanChain`.
3. **O binding é gravado como facto auditável.** `Issue` chama `recordIssued`, que faz
   `Append` de um evento `identity.nhi.issued` ao stream `identity`, **idempotente por
   jti** (`StepID = "nhi.issued:" + jti`), contendo **só metadados + o contexto de
   autorização** (ver §2.3) — nunca o token bearer, a assinatura, a chave, o ID-token
   cru ou PII.
4. **A verificação reconcilia o binding.** Em cada tool call, o `Verifier`
   (`packages/platform/identity/verifier.go`) valida a cadeia selada (raiz humana,
   hash-chain, não-escalada) e a autoria reconstrói-se via
   `Chain.HumanPrincipal()`/`identity.AuthorFromEvent`.

A **política org de quem pode autorizar que NHIs** (que humanos, que classes) é
**config de deployment** (o registo do IdP, os grupos), não código deste ADR — a
fronteira eu-construo vs deployment-provisiona da EPIC-16 §2.

### 2.2 Binding auditável de primeira classe

O registo de binding é resolvel/auditável sem reescrever o log, via a porta
`identity.BindingAudit` (`packages/platform/identity/binding.go`):

- `ResolveByJTI(jti)` e `BindingsForAgent(agentID)` lêem o stream append-only de
  identidade e devolvem um `BindingRecord` — *humano-raiz · agente · classe · escopo ·
  jti · issuer · método de autorização · iat/exp*.
- O **humano responsável** é derivado da **cadeia de delegação** do produtor do evento
  (via `AuthorFromEventChain`), **não** do campo de conveniência `user_id`: isto impõe
  fail-closed a raiz humana. Uma cadeia órfã/vazia devolve `ErrOrphanChain`/`ErrEmptyChain`
  e **nenhum** binding — a auditoria nunca legitima uma NHI "pool".

### 2.3 Contexto de autorização (o que torna o binding *auditável*, não só registado)

O evento `identity.nhi.issued` ganha um campo `auth_method` — o **método/autoridade**
pelo qual o humano-raiz foi autenticado ao mintar:

- `"oidc:<issuer>"` para `MintForAssertion` via `OIDCDirectory` (o issuer é a URL
  pública do IdP — `OIDCDirectory.AuthorizationMethod()`);
- `"allowlist"` para o double demo-grade `AllowlistDirectory` (declara explicitamente,
  no log, que este binding é demo-grade);
- `"delegation"` para uma NHI **filha** (autorizada pela cadeia do pai, que enraíza no
  mesmo humano — `issuer_child.go`);
- `"unspecified"` quando o mint não declara contexto (o registo é honesto sobre isso).

O rótulo é fornecido por uma directory que implemente a interface opcional
`integration.AuthorityReporter` (`AuthorizationMethod() string`). É sempre um **rótulo
de método** — NUNCA o token/asserção cru nem claims sensíveis.

### 2.4 Fail-closed (invariantes que não se negoceiam)

- Uma cadeia **órfã** (sem raiz humana) é recusada no mint (`delegation.NewRoot` /
  `Chain.Verify` ⇒ `ErrOrphanChain`); nenhuma NHI sem humano na raiz é emitida nem
  registada.
- Uma emissão **não-auditável** (falha ao gravar o evento) é fail-closed: `Issue`
  devolve erro e não entrega token (uma acção sem rasto é negada — ADR-010).
- O binding é **append-only e idempotente por jti**: a re-emissão não duplica o
  registo (`StatusDuplicate`) e nunca reescreve o log.

---

## 3. Alternativas consideradas

- **Identidade de serviço partilhada ("pool") por frota/classe.** Rejeitada: viola
  §13.2 (a autoria não resolve para um humano) e torna a revogação grosseira (revogar
  a pool afecta todos). A NHI por-agente é o oposto — jti único, TTL curto, revogável
  individualmente.
- **Binding só no token selado, sem registo separado.** Rejeitada: o token é efémero
  (TTL curto) e não é um log; a auditoria "quem autorizou esta NHI" exigiria reter
  bearers expirados. O Event Store append-only é a fonte auditável durável (ADR-010).
- **Registar o ID-token/asserção no evento para "prova completa".** Rejeitada: é PII e
  material sensível (ADR-006). O binding regista o **método** (`oidc:<issuer>`), não a
  prova — a prova foi validada no mint e não se retém.
- **Estender a interface `HumanDirectory` para obrigar a reportar o método.** Rejeitada
  a favor de uma interface **opcional** (`AuthorityReporter`): não quebra impls
  existentes (AOS-174) e degrada com honestidade para `"unspecified"`.

---

## 4. Consequências

**Positivas:** toda a acção resolve para um humano responsável, com o *método* dessa
autorização gravado e consultável; a revogação é por-jti (fina); a auditoria "quem
autorizou a NHI X" é uma leitura append-only (`BindingAudit`), fail-closed contra
cadeias órfãs; sem segredos/PII no log.

**Negativas / custos assumidos:** a autoria depende da integridade do Event Store —
a tamper-evidence do registo auditável de binding vem da propriedade **append-only /
WORM** do log (ADR-010), NÃO de uma hash-chain no próprio registo. A **hash-chain** da
cadeia de delegação (`PrevHash`/`LinkHash`) protege o **token selado** — e é
reconciliada pelo `Verifier` a cada tool call —, mas **não** a projecção de hops
(`sub`/`act_as`) gravada no evento de binding: `chainToHops` (`issuer.go`) grava só a
ordem dos elos, descartando os hashes. Logo o binding auditável é protegido contra
adulteração pelo append-only/WORM, não pela hash-chain (que se poderia propagar ao
evento no futuro, se se quisesse tamper-evidence do binding independente do WORM). O
`auth_method` é tão bom quanto a directory que o reporta (uma directory que não
implemente `AuthorityReporter` regista `"unspecified"` — honesto, mas menos informativo).

**Enforcement:** `packages/platform/identity/binding_test.go` e
`packages/integration/issuer_authority_binding_test.go` (`-race`): captura completa do
binding (humano-raiz + método + sem segredos/PII), idempotência por jti, e recusa
fail-closed de cadeia órfã (nenhuma NHI "pool" resolvida).

**Reversível?** O `auth_method` é aditivo (`omitempty`): eventos antigos sem o campo
resolvem para `"unspecified"`, sem quebrar a leitura.

---

## 5. Referências

- `specs/00_System_Spec.md` §11 (catálogo ADR-003), §13.2 (identidade → humano; zero pool)
- `specs/EPIC-16_Autoridade_Identidade_Real_D4.md` §3 (frente 3), §2 (fronteira eu-construo vs deployment)
- Ficheiros: `packages/platform/identity/{issuer.go,issuer_child.go,events.go,binding.go,authorship.go}`,
  `packages/platform/identity/delegation/chain.go`,
  `packages/integration/{issuer_authority.go,oidc_directory.go}`, `packages/integration/oidc/`
- ADRs: ADR-006 (broker/segredos), ADR-010 (audit WORM), ADR-016 (fronteira de confiança da camada de UI)

---

## Tabela de aprovação

Ratificação humana registada em Julho de 2026 (ver campo **Estado**). A evidência
formal de sign-off é mantida no registo de ratificação da equipa AOS ligado ao ticket
AOS-176, aqui referenciado por rasto.

| Papel | Nome | Assinatura | Data |
|---|---|---|---|
| Arquitecto de Plataforma | Equipa AOS — Arquitectura de Plataforma | Ratificado — registo AOS-176 | Julho 2026 |
| Responsável de Segurança | Equipa AOS — Segurança | Ratificado — registo AOS-176 | Julho 2026 |

## Controlo de versões

| Versão | Data | Descrição | Autor |
|---|---|---|---|
| 1.0 (aceite) | Julho 2026 | Materialização do ADR-003: NHI por-agente scoped/time-bound + processo/autoridade do binding humano↔NHI + registo auditável (`BindingAudit`, `auth_method`). Frente 3 do D4 (AOS-176). | Equipa AOS |
