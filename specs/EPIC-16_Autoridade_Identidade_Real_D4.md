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
- [x] **Frente 4 — Attestation WebAuthn/AAGUID** — **ENTREGUE por AOS-177**: verificação REAL de
      attestation de dispositivo. Porta **stdlib pura** `integration.DeviceAttestationVerifier`
      (`packages/integration/device_attestation.go`) ligada ao `FourEyesGate` por opção
      (`WithDeviceAttestation`): presente ⇒ cada perna tem de trazer `attestationObject` +
      `clientDataJSON` verificados e as duas pernas têm de vir de **dispositivos atestados
      distintos** (`ErrSameDevice`) — o `DeviceAttestation` de `foureyes.go` deixa de ser stub e
      ENTRA na decisão; ausente ⇒ comportamento estrutural anterior, retro-compatível (é o modo do
      binário zero-dep do nó). O binding attestation↔perna é o **challenge por-perna**, que já
      está dentro do tuplo assinado ed25519 — nada de re-colar attestations. Impl em
      `packages/platform/attestation` (módulo próprio, única dep externa `fxamacker/cbor/v2`):
      `packed` com cadeia x5c validada contra âncoras da organização + coerência da extensão de
      AAGUID do certificado (OID 1.3.6.1.4.1.45724.1.1.4), `packed` self-attestation (opt-in) e
      `fido-u2f` legado; **allowlist de AAGUID default-deny**; `none` recusado; `rpIdHash`,
      `origin`, `type`, challenge (comparação em tempo constante) e flags UP/UV verificados;
      limites anti-DoS no CBOR e bounds no parse binário do `authData`. Tudo fail-closed.
      Configuração da organização (âncoras x509, allowlist concreta, `rpId`/origem, enrollment dos
      dispositivos) = deployment.
- [x] **Nó zero-dep preservado** — **VERIFICADO por AOS-177**: o binário do nó não ganha
      dependências externas (ADR-017 intacto); a lib CBOR vive só no módulo de attestation, que o
      nó nunca importa (a porta é satisfeita ESTRUTURALMENTE, sem aresta de importação). Guardas
      EXECUTÁVEIS: `packages/cmd/aos/dep_isolation_test.go` e
      `packages/integration/dep_isolation_test.go` correm `go list -deps ./...` e falham se
      cbor/float16/webauthn/attestation aparecerem no fecho transitivo (molde do guarda de
      fronteira do ADR-018); `go.sum` pinado com build offline (`GOPROXY=off`) confirmado; dep
      triada nos gates (govulncheck: só vulns de stdlib já na baseline, nenhuma do cbor a afectar
      código).
- [ ] **Sign-off de Segurança/Arquitectura** obtido (pré-condição da v1, Carta §5) — fora do código.

## 4. Tabela Resumo de Tickets

| ID | Título | Tipo | Est. | Prio | Dependências |
|---|---|---|---|---|---|
| AOS-174 ✅ | `HumanDirectory` OIDC real (discovery + JWKS + validação ID-token, stdlib) — **ENTREGUE** | feature | M | P1 | AOS-156 |
| AOS-175 ✅ | Custódia de chave externa: `crypto.Signer` + issuer processo-separado + contrato KMS/HSM — **ENTREGUE** | feature | M | P1 | AOS-156 |
| AOS-176 ✅ | Binding humano↔NHI auditável + **ADR-003** formal — **ENTREGUE** | feature | S | P1 | AOS-174 |
| AOS-177 ✅ | Attestation **WebAuthn/AAGUID** (lib vetada, componente externo; nó zero-dep) — AOS-162 sai de stub — **ENTREGUE** | feature | L | P1 | AOS-175, AOS-162, ADR-016 |
| AOS-226 ✅ | Issuer externo **runnable** (`cmd/aos-issuer`): monta AOS-174/175 num binário deployável — detém a chave via `crypto.Signer`, exporta o trust anchor, minta NHI; o nó verifica trust-anchor-only sem deter a chave — **ENTREGUE** | feature | M | P1 | AOS-174, AOS-175 |
| AOS-227 ✅ | Autenticação OIDC do humano no `cmd/aos-issuer` (frente 1): `mint --assertion` verifica um ID-token contra o IdP (verifier real de AOS-174) e deriva o humano-raiz do `sub` verificado — **cbor-free** — **ENTREGUE** | feature | S | P1 | AOS-226, AOS-174 |
| AOS-228 ✅ | Costura OIDC do directório humano no **NÓ** (fecha DEF-104/105/110): `AOS_HUMAN_OIDC_*` compõe o `OIDCDirectory` na autoridade de referência (a via sem-prova é recusada), padrão injectável de AOS-220, **cbor-free** — **ENTREGUE** | feature | S | P2 | AOS-174, AOS-220 |

**Sequência:** 174 (humano autenticado real) → 175 (não-forjabilidade real, chave fora do nó) →
176 (binding + ADR-003) → 177 (attestation de dispositivo, a frente com a lib externa). As três
primeiras não tocam o zero-dep; a 177 é a única com a exceção escopada da emenda 1.3. **AOS-226**
monta 174/175 num **processo deployável** (as frentes entregaram bibliotecas; faltava o binário).

### 4-bis. AOS-226 — issuer externo runnable (`cmd/aos-issuer`)

As frentes entregaram a autoridade real como **bibliotecas**; faltava **montá-las num processo
deployável** que o nó consuma. `cmd/aos-issuer` é esse binário — a autoridade de identidade
**externa** (processo separado por desenho: quem minta não pode ser quem verifica, senão o nó
"verificaria" tokens que ele próprio mintou — escalada §2):

- **`pubkey`** exporta a chave pública ed25519 (o `AOS_ISSUER_PUBKEY` do nó);
- **`mint`** emite um token NHI (`identity.NewIssuerWithSigner` → `Issue`, com `AuthMethod` para o
  binding audit de AOS-176) que o nó verifica trust-anchor-only;
- a chave privada vive **fora** do nó — em dev, num ficheiro `0600` que o operador controla; em
  prod, num **HSM/KMS** pela costura `crypto.Signer` (AOS-175). A chave nunca é ecoada.

**Critérios de aceitação**

- [x] `cmd/aos-issuer` compila e é **zero-dep externo** (`go.sum` vazio; só deps `aos-ref` locais;
      `go mod tidy` limpo) — o nó permanece intocado. *(Evidência: build+vet+`go mod tidy` verdes; `go.sum` 0 bytes.)*
- [x] `pubkey` exporta um trust anchor **estável** (64 hex) e `mint` emite um token NHI **verificável**
      contra essa pubkey; um verifier com **outra** chave **recusa** (dois sentidos). *(Evidência:
      `packages/cmd/aos-issuer/main_test.go` — `TestIssuer_MintProducesVerifiableToken`, `-race`.)*
- [x] `mint` é **fail-closed** sem `--human`/`--agent`/`--class` (não emite token degenerado).
      *(Evidência: `TestIssuer_MintFailClosed`.)*
- [x] O **nó** verifica um token do issuer externo **trust-anchor-only, sem deter a chave**; um issuer
      rogue é negado em identity. *(Evidência: `packages/cmd/aos/devharness_test.go` —
      `TestDevHarness_ExternalIssuer_NodeVerifiesTrustAnchorOnly`, `-race`.)*
- [x] A chave privada **nunca** é ecoada (código/log); sem `issuer.key` committada. *(Evidência: gate `secrets` verde.)*

**Residual nomeado (fronteira honesta):** o `OIDCDirectory` (AOS-174) — autenticar o humano contra um
IdP real **antes** do mint — ainda **não está composto** no `cmd/aos-issuer` (o `mint` recebe o humano
por flag; a costura front-1 fica para o próximo incremento, eixo `DEF-104/105/110`). O adaptador
**HSM/KMS** concreto e o **tenant OIDC** são infra-org (deployment), como o resto do D4.

**Fecha:** o "issuer processo-separado" que AOS-175 nomeia mas não entregava como binário.
**Depende de:** AOS-174 (verifier/token), AOS-175 (`crypto.Signer`). **Não duplica:** AOS-177
(attestation, componente externo com a lib WebAuthn).

### 4-ter. AOS-227 — autenticação OIDC do humano no issuer (frente 1)

AOS-226 entregou o issuer com o humano por **flag** (auto-declarado). AOS-227 fecha a **frente 1**
no issuer: `mint --assertion <id-token> --oidc-issuer <url> --oidc-audience <aud>` **autentica** o
humano contra um **IdP real** antes de emitir — o humano-raiz da delegação é **DERIVADO do `sub`
VERIFICADO**, não auto-declarado.

- usa o **verificador OIDC real de AOS-174** (`integration/oidc`: discovery/JWKS + JWS +
  anti-alg-confusion + `aud`/`exp`/`iat`) — a mesma verificação que o `OIDCDirectory` embrulha;
- **cbor-free**: usa só o subpacote `integration/oidc` (stdlib), **não** o pacote `integration`
  (que traria a lib WebAuthn da attestation) — o issuer mantém-se zero-dep externo;
- **fail-closed**: qualquer falha de verificação propaga-se; nenhum humano é derivado de um token
  não-verificado e nenhum NHI é emitido. O método (`oidc:<issuer>`) alimenta o binding audit (AOS-176).

**Critérios de aceitação**

- [x] `mint --assertion` deriva o humano-raiz do `sub` **verificado** (não de uma flag) + método
      `oidc:<issuer>`; um ID-token **adulterado** é recusado fail-closed. *(Evidência:
      `packages/cmd/aos-issuer/main_test.go` — `TestIssuer_OIDCAuthenticatesHumanBeforeMint`, IdP mock RSA/JWKS, `-race`.)*
- [x] **cbor-free** preservado (`go list -deps` sem attestation/cbor; `go.sum` vazio). *(Evidência: `go list -deps` + gate.)*
- [x] **Retro-compat**: `mint --human` (via manual) continua a funcionar. *(Evidência:
      `TestIssuer_MintProducesVerifiableToken` + smoke por flag.)*

**Residual nomeado:** o **tenant OIDC real** (Okta/Azure/Keycloak) é infra-org (deployment); o issuer
fica com o **contrato** (verifica qualquer IdP conforme). A costura OIDC no **modo de referência do
NÓ** (`DEF-110`, `bootstrap.go`) é um seam **distinto** e de menor valor — o modo de referência
auto-assina; o caminho real é endurecido + issuer externo, que é o que AOS-227 serve. Permanece residual.

**Fecha:** a frente 1 do D4 **no issuer** (autenticação humana real antes do mint). **Depende de:**
AOS-226 (issuer runnable), AOS-174 (verifier OIDC).

### 4-quater. AOS-228 — costura OIDC do directório humano no nó (fecha DEF-104/105/110)

O nó compunha **sempre** a allowlist de referência (`bootstrap.go`); não havia ramo de config para
OIDC (`DEF-104/105/110`). AOS-228 fecha-o, **espelhando a costura injectável de AOS-220** (o PDP):

- `Config.HumanDirectory` (injectável); `nodeConfigFromEnv` compõe `integration.NewOIDCDirectory` a
  partir de `AOS_HUMAN_OIDC_ISSUER`/`AUDIENCE`/`JWKS_URI`;
- **fail-closed**: config incompleta ⇒ `ErrBadHumanOIDC` (aborta); o `bootstrap` dá **precedência**
  ao OIDC, senão a allowlist de referência (retro-compat);
- **cbor-free**: o nó já compila `package integration` sem cbor (a `attestation` de AOS-177 fica atrás
  de build-tag); usar `NewOIDCDirectory` não adiciona deps — `go list -deps cmd/aos` continua sem cbor;
- **só no modo de REFERÊNCIA** (autoridade co-localizada); no modo endurecido o directório humano vive
  com o issuer EXTERNO (AOS-226/227), não no nó.

**Critérios de aceitação**

- [x] `AOS_HUMAN_OIDC_*` ⇒ `nodeConfigFromEnv` compõe o `OIDCDirectory` (a via sem-prova é recusada
      `ErrAssertionRequired`); config incompleta ⇒ `ErrBadHumanOIDC`; ausente ⇒ allowlist (retro-compat).
      *(Evidência: `packages/cmd/aos/aos228_human_oidc_test.go` — `TestAOS228_ConfigFromEnv_WiresHumanOIDCDirectory`.)*
- [x] O nó **compõe** o directório injectado com precedência: `MintForHuman` (sem prova) ⇒
      `ErrAssertionRequired`; sem injecção (allowlist) ⇒ funciona (dois sentidos). *(Evidência:
      `TestAOS228_NodeComposesInjectedHumanDirectory`, `-race`.)*
- [x] **cbor-free** preservado (`go list -deps cmd/aos` sem attestation/cbor) + env-surface documentada
      (`AOS_HUMAN_OIDC_*`) + `deferrals` verde. *(Evidência: `TestAOS203EnvSurfaceIsDocumented` + gate.)*

**Fecha:** `DEF-104/105/110` (o seam de config do directório humano no nó). **Depende de:** AOS-174
(`OIDCDirectory`), AOS-220 (padrão de costura injectável). **Residual:** o **tenant OIDC** concreto é
infra; a costura no modo de referência é de **menor valor** (auto-assina) — o caminho de produção é
endurecido + issuer externo (AOS-226/227), já servido.

## 5. Controlo de versões

| Versão | Data | Descrição | Autor |
|---|---|---|---|
| 1.0 | 2026-07-23 | Emissão. Enacta a Opção A do D4 (Carta emenda 1.3): Camada B da autoridade de identidade em 4 tickets (AOS-174–177). Nó zero-dep preservado; lib WebAuthn só no componente externo. | Equipa AOS |
| 1.1 | 2026-07-24 | Frente 2 (custódia de chave externa) marcada ENTREGUE por AOS-175: issuer assina via `crypto.Signer` (chave pode viver em HSM/KMS), issuer processo-separado (nó trust-anchor-only). Adapter KMS concreto e instância HSM = deployment. | Equipa AOS |
| 1.2 | 2026-07-24 | Frente 3 (binding humano↔NHI + ADR-003) marcada ENTREGUE por AOS-176: ADR-003 formal ratificado; binding auditável de primeira classe (`identity.BindingAudit`) sobre o evento append-only `identity.nhi.issued` com `auth_method`, fail-closed contra cadeias órfãs, sem segredos/PII. | Equipa AOS |
| 1.3 | 2026-07-24 | Frente 4 (attestation WebAuthn/AAGUID) marcada ENTREGUE por AOS-177 — ÚLTIMA da Opção A: porta stdlib `DeviceAttestationVerifier` + `FourEyesGate.WithDeviceAttestation` (dispositivos atestados distintos, `ErrSameDevice`); impl `packages/platform/attestation` (packed/x5c + self + fido-u2f, allowlist de AAGUID, extensão de AAGUID do cert, `none` recusado) num módulo com a única dep externa `fxamacker/cbor/v2`. Zero-dep do nó preservado e VERIFICADO por guardas `go list -deps` em cmd/aos e integration. ADR-016 §4 sai de CONDICIONAL-por-código. | Equipa AOS |
| 1.4 | 2026-08-01 | **AOS-226** acrescentado (§4-bis): issuer externo RUNNABLE `cmd/aos-issuer` — monta AOS-174/175 num binário deployável (`pubkey` exporta o trust anchor; `mint` emite NHI via `crypto.Signer`; o nó verifica trust-anchor-only sem deter a chave). Módulo zero-dep externo (`go.sum` vazio, `go mod tidy` limpo); prova ponta-a-ponta no dev-harness (`TestDevHarness_ExternalIssuer`) e no módulo (`TestIssuer_MintProducesVerifiableToken`). Residual: compor o `OIDCDirectory` no issuer (`DEF-104/105/110`); HSM/KMS + tenant OIDC = infra. | Equipa AOS |
| 1.5 | 2026-08-01 | **AOS-227** acrescentado (§4-ter): autenticação OIDC do humano no `cmd/aos-issuer` (frente 1) — `mint --assertion` verifica um ID-token contra o IdP (verifier real de AOS-174: discovery/JWKS/JWS/aud/exp) e deriva o humano-raiz do `sub` VERIFICADO, **cbor-free** (só `integration/oidc`, sem a attestation). Fail-closed; retro-compat com `--human`. Prova: `TestIssuer_OIDCAuthenticatesHumanBeforeMint` (IdP mock RSA/JWKS, -race). Residual: tenant OIDC = infra; `DEF-110` (OIDC no modo de referência do NÓ) = seam distinto de menor valor. | Equipa AOS |
| 1.6 | 2026-08-01 | **AOS-228** acrescentado (§4-quater): costura OIDC do directório humano no **NÓ** — `AOS_HUMAN_OIDC_*` compõe o `OIDCDirectory` na autoridade de referência (padrão injectável de AOS-220; fail-closed `ErrBadHumanOIDC`; precedência sobre a allowlist), **CBOR-FREE** (`go list -deps cmd/aos` sem cbor). **FECHA `DEF-104/105/110`**. Prova: `TestAOS228_ConfigFromEnv_WiresHumanOIDCDirectory` + `TestAOS228_NodeComposesInjectedHumanDirectory` (-race). Residual: tenant OIDC = infra; costura ref-mode de menor valor (o caminho real é endurecido + issuer externo). | Equipa AOS |
