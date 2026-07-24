# EPIC-16 — Autoridade de Identidade Real (D4, Opção A)

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — Provisionamento da autoridade de identidade real (Camada B do D4) |
| Versão | 1.0 |
| Data | 2026-07-23 |
| Classificação | Documento de Referência — Aberto |
| Documento-fonte | **`specs/00_AOS_Carta.md` §4 (D4), §7 emenda 1.3** (decisão do dono: Opção A) |
| Documentos relacionados | `docs/reports/D4-escalacao-autoridade-identidade.md`, `packages/integration/issuer_authority.go` (AOS-156, token spine), ADR-003 (raiz humana), ADR-006 (broker/vault), ADR-016 (AAGUID/WebAuthn), EPIC-07 (vault), EPIC-14 (AOS-156/160/162) |

---

## 1. Visão do Epic

A **Camada A** do D4 (o *enforcement* de identidade — verifier fail-closed, cadeia de delegação
hash-linked com raiz humana, separação issuer↔nó, 4-eyes estrutural, canal de controlo ed25519)
está **construída e provada** (AOS-156/160/162, EPIC-14). O que falta é a **Camada B — a AUTORIDADE
REAL**: as impls concretas que substituem os *doubles* demo-grade (`AllowlistDirectory`,
chave CSPRNG in-process, `StaticAuthority`, vault in-memory) por autenticação humana real,
custódia de chave fora do apex, binding auditável e attestation de dispositivo.

Esta epic enacta a **Opção A** (emenda 1.3): provisionar a autoridade completa. Entrega **CÓDIGO
real contra contratos-padrão** (OIDC, ADR-003, `Signer`, WebAuthn) + *doubles* de referência/teste;
as **instâncias de infra** (tenant IdP, HSM/KMS, allowlist AAGUID) são **config de deployment**, não
código desta epic.

**Invariante preservado (emenda 1.3):** o **binário do nó mantém-se zero-dep** (só stdlib +
cedar-go). A verificação WebAuthn/AAGUID (única frente que exige uma lib externa) vive no
**componente de autoridade de identidade EXTERNO** ao nó (o issuer é um processo separado) — o
ADR-017 do artefacto distribuído do nó fica intacto.

## 2. Fronteira eu-construo vs. deployment-provisiona

| Frente | Código desta epic | Config de deployment |
|---|---|---|
| IdP humano | `HumanDirectory` OIDC (discovery + JWKS + validação ID-token, stdlib) | tenant IdP (Okta/Azure/Keycloak…), issuer/client |
| Custódia de chave | interface `Signer` + issuer processo-externo + contrato KMS/HSM + impl referência | instância HSM/KMS + adapter do fornecedor |
| Binding humano↔NHI | ADR-003 formal + registo auditável do binding | política org de quem autoriza NHIs |
| WebAuthn/AAGUID | verificação de attestation (lib vetada, no componente externo) | allowlist AAGUID + enrollment WebAuthn |

## 3. Critérios de Saída do Epic

- [x] **Frente 1 — IdP OIDC**: uma impl real de `HumanDirectory` autentica o humano contra um IdP
      OIDC (discovery + JWKS + validação de ID-token, só stdlib), substituindo a allowlist demo;
      humano não-autenticado ⇒ mint recusado fail-closed — **ENTREGUE por AOS-174**. Código:
      `packages/integration/oidc/` (verifier stdlib: discovery+JWKS+JWS+claims, fail-closed,
      anti-alg-confusion, anti-replay) + `packages/integration/oidc_directory.go` (`OIDCDirectory`
      liga o verifier à porta `HumanDirectory`; consumida por `IssuerAuthority.MintForAssertion`).
      Nota: o **tenant IdP real (issuer/client/JWKS)** continua a ser **config de deployment** — o
      código entrega o contrato OIDC-padrão validado offline (httptest), não uma instância de IdP.
- [x] **Frente 2 — Custódia de chave externa**: o issuer assina através do contrato-padrão stdlib
      `crypto.Signer` (`Public()`+`Sign()`), pelo que a chave privada pode viver num HSM/KMS e nunca
      ser detida como bytes crus pelo processo; o issuer corre como **processo separado** (o nó é
      trust-anchor-only — só a pubkey via `TrustAnchor()`/`NewVerifierFromAuthority`); a chave privada
      NUNCA entra no processo do nó — **ENTREGUE por AOS-175**. Código: `identity.NewIssuerWithSigner`
      (via nova, `crypto.Signer` arbitrário) mantendo `NewIssuer` (chave ed25519 crua, compatível) +
      auto-verificação fail-closed-na-origem em `signToken` (nenhum bearer que a pubkey do issuer não
      valide é emitido) + fronteira panic-safe (`signerPublicKey`); `integration.AuthorityConfig.Signer`
      injecta o signer externo (mutuamente exclusivo com `SigningKey`). Impl de referência: a
      `ed25519.PrivateKey` in-process (que já é `crypto.Signer`); doubles de teste de custódia externa
      exercitam a fronteira. Nota: o **adapter KMS/HSM concreto do fornecedor** (AWS/GCP KMS, PKCS#11)
      e a **instância HSM real** continuam a ser **config de deployment** — o binário do nó mantém-se
      zero-dep; a epic entrega o CONTRATO + impl de referência, não uma instância de HSM.
- [x] **Frente 3 — Binding humano↔NHI + ADR-003** — **ENTREGUE por AOS-176**: **ADR-003 formal**
      escrito e ratificado (`docs/adr/ADR-003-identidade-nao-humana-por-agente.md`); o binding
      humano→NHI é registado de forma auditável (evento append-only `identity.nhi.issued`, cadeia de
      delegação raiz-humana) com o processo de autorização declarado (`auth_method`) e resolvel via a
      porta `identity.BindingAudit` (`ResolveByJTI`/`BindingsForAgent`), fail-closed contra cadeias
      órfãs, sem segredos/PII. — AOS-176.
- [ ] **Frente 4 — Attestation WebAuthn/AAGUID**: verificação real de attestation de dispositivo
      (lib vetada, no componente de autoridade externo; nó zero-dep) com allowlist AAGUID; o
      `DeviceAttestation` de `foureyes.go` deixa de ser stub e entra na decisão 4-eyes — AOS-177.
- [ ] **Nó zero-dep preservado**: o binário do nó não ganha dependências externas (ADR-017
      intacto); a lib WebAuthn só no componente externo, passada pelos gates (sca/govulncheck,
      go.sum, SBOM).
- [ ] **Sign-off de Segurança/Arquitectura** obtido (pré-condição da v1, Carta §5) — fora do código.

## 4. Tabela Resumo de Tickets

| ID | Título | Tipo | Est. | Prio | Dependências |
|---|---|---|---|---|---|
| AOS-174 ✅ | `HumanDirectory` OIDC real (discovery + JWKS + validação ID-token, stdlib) — **ENTREGUE** | feature | M | P1 | AOS-156 |
| AOS-175 ✅ | Custódia de chave externa: `crypto.Signer` + issuer processo-separado + contrato KMS/HSM — **ENTREGUE** | feature | M | P1 | AOS-156 |
| AOS-176 ✅ | Binding humano↔NHI auditável + **ADR-003** formal — **ENTREGUE** | feature | S | P1 | AOS-174 |
| AOS-177 | Attestation **WebAuthn/AAGUID** (lib vetada, componente externo; nó zero-dep) — AOS-162 sai de stub | feature | L | P1 | AOS-175, AOS-162, ADR-016 |

**Sequência:** 174 (humano autenticado real) → 175 (não-forjabilidade real, chave fora do nó) →
176 (binding + ADR-003) → 177 (attestation de dispositivo, a frente com a lib externa). As três
primeiras não tocam o zero-dep; a 177 é a única com a exceção escopada da emenda 1.3.

## 5. Controlo de versões

| Versão | Data | Descrição | Autor |
|---|---|---|---|
| 1.0 | 2026-07-23 | Emissão. Enacta a Opção A do D4 (Carta emenda 1.3): Camada B da autoridade de identidade em 4 tickets (AOS-174–177). Nó zero-dep preservado; lib WebAuthn só no componente externo. | Equipa AOS |
| 1.1 | 2026-07-24 | Frente 2 (custódia de chave externa) marcada ENTREGUE por AOS-175: issuer assina via `crypto.Signer` (chave pode viver em HSM/KMS), issuer processo-separado (nó trust-anchor-only). Adapter KMS concreto e instância HSM = deployment. | Equipa AOS |
| 1.2 | 2026-07-24 | Frente 3 (binding humano↔NHI + ADR-003) marcada ENTREGUE por AOS-176: ADR-003 formal ratificado; binding auditável de primeira classe (`identity.BindingAudit`) sobre o evento append-only `identity.nhi.issued` com `auth_method`, fail-closed contra cadeias órfãs, sem segredos/PII. | Equipa AOS |
