# Runbook — swaps de produção da autoridade de identidade (config-only)

Este runbook documenta os **três swaps de componente** que levam a realização de referência
(stack `deploy/node/dev-hardened`) a uma postura de produção da autoridade de identidade (D4 /
EPIC-16). **São config/infra, não código** — as costuras estão construídas e provadas; cada swap
troca o *backend* por trás da mesma costura, sem tocar no binário do nó (ADR-017 intacto).

> **Fronteira eu-construo vs. deployment-provisiona** (Carta/EPIC-16 §2). O código destas costuras
> está entregue e verificado live nesta stack de dev. O que segue é o que o **dono/operador**
> provisiona — requer credenciais/hardware/PKI reais que não existem no ambiente de dev.

Estado de referência (o que já está provado em dev):

| Costura | Dev (provado) | Verificação live |
|---|---|---|
| Assinatura do issuer | `crypto.Signer` → Vault Transit ed25519 | `demo-vault-issuer.sh`: NHI Vault-assinado aceite; outra chave → `denied_by=identity` |
| Identidade humana + soberania | OIDC (Keycloak): `AOS_SOVEREIGN_OIDC_*`, `AOS_HUMAN_OIDC_*` | `up-oidc.sh`: run 201 com Bearer verificado; header forjado → 403 |
| Attestation de dispositivo | `RemoteDeviceAttestationVerifier` → `cmd/aos-attestation` (HTTPS) | serviço `/verify` 200+device_id; lixo 422; nó four-eyes com attestation |

---

## Swap 1 — Vault dev → HSM/KMS

**Costura:** o issuer assina através de `crypto.Signer` (`identity.NewIssuerWithSigner`); a KEK do
DSAR é custodiada por `audit.KeyWrapper`. Ambas falam com o Vault por HTTP (`--vault-addr` /
`AOS_DSAR_VAULT_ADDR`). **A chave nunca entra no processo do nó** — só a pubkey (trust-anchor).

**O que muda (nenhum código do nó):**

1. **Vault de produção, não `-dev`** — já feito nesta stack (storage `file` + init/unseal reais).
   Em produção: storage `raft`/integrated ou Consul; **auto-unseal** via cloud KMS (bloco
   `seal "awskms"|"gcpckms"|"azurekeyvault"`) em vez do share único em ficheiro; TLS no listener
   do Vault; tokens de **curta duração** (AppRole / Kubernetes-auth) em vez do root-token de dev.
2. **Chave respaldada por HSM/KMS** — a chave Transit do issuer (`transit/keys/aos-issuer-key`) e as
   KEKs por-titular passam a **managed keys** respaldadas por HSM (Vault Enterprise, PKCS#11) ou por
   um cloud KMS. É **config do Vault**: a interface HTTP (`transit/sign`, `transit/keys`,
   `transit/encrypt|decrypt`) que o nó/issuer usam **não muda**.
3. **Alternativa sem Vault** (se a organização usa KMS diretamente): adicionar um adaptador
   `crypto.Signer` do KMS ao `cmd/aos-issuer` (componente EXTERNO, não o nó) — pequeno código no
   issuer, zero no nó. A via **config-only** é a (2) acima.

**Env/config afetada:** `AOS_DSAR_VAULT_ADDR=https://vault-prod:8200` (+ `AOS_DSAR_VAULT_TOKEN_PATH`
com token de curta duração); `aos-issuer --vault-addr https://vault-prod:8200`. **Nenhuma** var do
nó muda de semântica.

**Ação do dono:** provisionar o Vault de produção (ou KMS), migrar/gerar a chave do issuer no HSM,
distribuir o trust-anchor (pubkey) ao nó via `AOS_ISSUER_PUBKEY`.

---

## Swap 2 — Keycloak → IdP corporativo

**Costura:** o nó verifica **qualquer** ID-token OIDC-compliant (RS256, discovery/JWKS, `iss`/`aud`/
`exp`, anti-replay por-jti) — Keycloak é só *um* IdP OIDC. O verificador é endurecido (alg
allowlist, rejeita `none`/HS*, anti-SSRF) e **agnóstico ao fornecedor**.

**O que muda (nenhum código):** só **env** do nó e **config do IdP**:

| Var do nó | Dev (Keycloak) | Produção (IdP corporativo) |
|---|---|---|
| `AOS_SOVEREIGN_OIDC_ISSUER` | `https://localhost:9443/realms/aos` | `https://login.corp.example/…` (issuer do IdP) |
| `AOS_SOVEREIGN_OIDC_AUDIENCE` | `aos-node` | a audience que o IdP emite para o nó |
| `AOS_SOVEREIGN_OIDC_JWKS_URI` | (explícito, split de hostname) | via discovery do IdP (ou explícito) |
| `AOS_HUMAN_OIDC_*` | Keycloak | o mesmo IdP corporativo (para `mint --assertion`) |
| `SSL_CERT_FILE` | CA de dev | CA pública/corporativa do IdP |

**Config do IdP (não do nó):** o IdP corporativo tem de emitir a claim **`board`** (mapeador de
claim), tal como o Keycloak de dev faz — é o que mapeia o titular à região soberana. Registar o nó
como *client*/audience e o mapeador de `board` é config do IdP.

**Ação do dono:** registar o nó no IdP corporativo, configurar o mapeador de `board`, apontar as
`AOS_*_OIDC_*` do nó ao issuer corporativo, remover o serviço Keycloak da stack.

---

## Swap 3 — CA de dev → raiz FIDO/organizacional

**Costura:** o componente `cmd/aos-attestation` verifica cadeias **x5c** contra âncoras de confiança
passadas por `--ca`, com uma **allowlist de AAGUID** (`--aaguids`). Em dev corre em MODO DEV (CA
auto-gerada + endpoint `/synth`); em produção passa-se a raiz e a allowlist reais.

**O que muda (nenhum código):** os **args do serviço** `attestation` no compose/deployment:

| Arg | Dev | Produção |
|---|---|---|
| `--ca` | *(ausente ⇒ MODO DEV, CA auto-gerada + `/synth`)* | ficheiro PEM com as **raízes FIDO MDS** e/ou a **PKI de attestation da organização** |
| `--aaguids` | *(ausente ⇒ um AAGUID de dev)* | CSV dos **AAGUID dos modelos de autenticador aprovados** (política de dispositivos, ADR-016 §4) |
| `--tls-cert/--tls-key` | cert da CA de dev | cert do serviço assinado pela PKI interna |

Passar `--ca` **desliga automaticamente** o MODO DEV: o endpoint `/synth` deixa de existir e só
**autenticadores REAIS** certificados por essas raízes (com AAGUID na allowlist) verificam. O
contrato bytes-in/bytes-out com o nó (`RemoteDeviceAttestationVerifier`) **não muda**.

**Ação do dono:** obter as raízes de attestation (FIDO Metadata Service e/ou PKI interna), definir a
allowlist de AAGUID dos modelos homologados, inscrever os dispositivos dos aprovadores (enrollment
WebAuthn), e passar `--ca`+`--aaguids` ao serviço.

---

## Resumo — o que é código (feito) vs. o que é provisionamento (do dono)

| Swap | Código (feito e provado) | Provisionamento do dono (este runbook) |
|---|---|---|
| Vault→HSM/KMS | `crypto.Signer` + `KeyWrapper` (HTTP Transit) | Vault prod/auto-unseal + managed keys HSM, ou KMS |
| Keycloak→IdP corp. | verificador OIDC endurecido agnóstico | registar o nó + mapeador `board` + apontar env |
| CA dev→raiz FIDO | `--ca`/`--aaguids` + x5c + AAGUID allowlist | raízes FIDO/PKI + allowlist + enrollment de dispositivos |

**Nenhum swap toca o binário do nó** (ADR-017). Cada um troca um *backend* por trás de uma costura
já verificada live nesta stack — é a razão pela qual o gap é *fechável por config*, e não trabalho
de engenharia por fazer.
