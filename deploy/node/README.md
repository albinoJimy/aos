# Empacotamento do nó `aos` (AOS-168 / ADR-017)

Imagem **distroless, non-root, root-fs read-only** do nó de referência. A fronteira de
supply-chain é ADR-017 (FIXA na Carta §4.1). Pontos **1/2/4 impostos**, ponto **3 mínimo**
(SBOM + proveniência geradas; **assinatura DEFERIDA para EPIC-10** — declarado, não fingido),
ponto **5 respeitado** (a chave do issuer **nunca** entra na imagem).

## Construir

O contexto de build é a **raiz do repo** (os 42 `replace ... => ../../` de
`packages/cmd/aos/go.mod` são relativos; a árvore `packages/` inteira tem de estar no contexto):

```bash
docker build -f deploy/node/Dockerfile -t aos-node:local .
```

Build **reprodutível/offline**: o `go build` corre com `CGO_ENABLED=0 GOPROXY=off -trimpath`.
O único passo de rede é `go mod download` (prime do cache, verificado contra o `go.sum` pinado —
o projecto não vendoriza por opção; ver `scripts/ci/cache-prime.sh`).

## Correr — root-fs READ-ONLY + estado durável em volume

O root-fs é read-only; o **estado durável** (Event Store / WORM, AOS-170), quando ligado,
escreve num **volume gravável EXPLÍCITO**, nunca no root-fs:

```bash
docker run --rm \
  --read-only \
  --tmpfs /tmp \
  -v aos-data:/var/lib/aos \
  -e AOS_API_ADDR=127.0.0.1:8080 \
  -p 8080:8080 \
  aos-node:local
```

O arranque de referência corre **in-memory** (não escreve no root-fs), pelo que roda limpo sob
`--read-only`. A durabilidade é opt-in e exige o **mount explícito** `-v aos-data:/var/lib/aos`.

> A imagem **não declara `VOLUME`** de propósito: sem ele, uma tentativa de durabilidade sob
> `--read-only` **sem** `-v` falha **visivelmente** em vez de escrever num volume anónimo órfão —
> a verificação de root-fs read-only não é mascarada. O directório `/var/lib/aos` é criado owned
> por `65532:65532`, pelo que o volume nomeado do operador herda a ownership certa.

### Superfície de configuração — TODAS as variáveis lidas pelo nó (AOS-203)

O ambiente é a **única** superfície de configuração do binário entregue (`Config` vive em
`package main`: um campo que `nodeConfigFromEnv` não escreva é **inalcançável** por quem corre a
imagem). A tabela abaixo é o **índice completo** — toda a variável lida pelos dois binários da
imagem (o nó, `packages/cmd/aos`, e o `aos-healthprobe` do `HEALTHCHECK`) está aqui.

O teste `TestAOS203EnvSurfaceIsDocumented` (`packages/cmd/aos/env_surface_test.go`) **avermelha**
se alguém acrescentar uma leitura de ambiente sem a documentar **nesta secção**. O que ele impõe,
exactamente: extrai por **AST** (não `grep`) as chamadas `os.Getenv`/`os.LookupEnv`/`envOr` das
duas árvores de código, **recursivamente**; **proíbe** `os.Environ` (leitura por enumeração
escaparia ao gate por construção); e exige, para cada variável, uma linha de tabela **dentro
desta secção** com as células **Default e Efeito preenchidas** — uma linha degenerada
``| `AOS_X` |  |  |`` **não** conta como documentação.

As cinco variáveis com secção própria abaixo (estado durável, plano de controlo) têm aqui a linha
de índice e o detalhe lá.

| Variável | Default | Efeito |
|---|---|---|
| `AOS_MODE` | *(vazio ⇒ modo de **referência**)* | `production` (qualquer caixa) activa a **postura de produção fail-closed**. **Segurança:** é o interruptor que torna obrigatórias **quatro** exigências — `AOS_ISSUER_PUBKEY` (senão `ErrProductionNeedsHardenedIdentity`), `AOS_BOARD_REGIONS` **não-vazio** (senão `ErrProductionNeedsSovereignRead`), **credencial forte da soberania de leitura** — `AOS_SOVEREIGN_OIDC_ISSUER`+`AOS_SOVEREIGN_OIDC_AUDIENCE` (senão `ErrProductionNeedsSovereignAuthority`, AOS-205) e **terminação TLS** — `AOS_TLS_CERT_PATH`+`AOS_TLS_KEY_PATH` **ou** `AOS_TLS_EXTERNAL_TERMINATION` (senão `ErrProductionNeedsTLS`, AOS-209). Qualquer outro valor ⇒ modo de referência **sem** essas exigências: um nó exposto sem `AOS_MODE=production` não é um nó de produção, é um nó de referência a servir tráfego. |
| `AOS_API_ADDR` | *(vazio ⇒ **API não levantada**)* | Endereço de bind da API HTTP. Vazio ⇒ o nó faz bootstrap, declara o banner e **sai sem abrir socket**. Não-loopback ⇒ sujeito ao [bind-guardrail](#bind-guardrail-fail-closed) (recusa se não houver operadores). É também o **default do `--addr`** dos subcomandos cliente (`aos run/observe/steer/pause`) e a fonte da porta do `HEALTHCHECK`. |
| `AOS_TLS_CERT_PATH` | *(vazio ⇒ **sem terminação TLS no nó**)* | Caminho do **certificado** (PEM) da terminação TLS do ingresso — ver [Terminação TLS](#terminação-tls-do-ingresso--api-sse-dsar--perna-otlp-aos-209). Exige `AOS_TLS_KEY_PATH` (só um dos dois ⇒ `ErrIncompleteTLSConfig`). Material **público**. |
| `AOS_TLS_KEY_PATH` | *(vazio ⇒ **sem terminação TLS no nó**)* | Caminho da **chave privada** (PEM) da terminação TLS — ver [Terminação TLS](#terminação-tls-do-ingresso--api-sse-dsar--perna-otlp-aos-209). ⚠️ **Material PRIVADO por FICHEIRO montado** (nunca por variável de ambiente), no padrão de `AOS_ISSUER_KEY_PATH`; monte-o read-only e fora da imagem. Par inválido ⇒ `ErrBadTLSKeyPair`. |
| `AOS_TLS_EXTERNAL_TERMINATION` | *(vazio ⇒ **não declarado**)* | **Opt-out ruidoso** — declara que a terminação TLS é feita **a montante** (ingress/malha). Ligam: `1` `true` `t` `yes` `y` `on`; qualquer outro valor ⇒ **ABORTA** (`ErrBadTLSExternalTermination`). Declarado ⇒ o nó serve em claro **por decisão de quem o configurou** e emite um aviso proeminente no banner — ver [Terminação TLS](#terminação-tls-do-ingresso--api-sse-dsar--perna-otlp-aos-209). Ignorado se houver TLS no nó. |
| `AOS_ISSUER_ID` | `iss:aos-node` | Identificador da autoridade de identidade — **é o trust anchor** que o verifier exige no `iss` de cada credencial. **Segurança/operação:** no modo endurecido tem de ser **exactamente** o issuer que emitiu os tokens (o par `(AOS_ISSUER_ID, AOS_ISSUER_PUBKEY)` é o anchor completo); um valor errado não abre nada — faz o nó **rejeitar todas** as credenciais legítimas (fail-closed, mas silencioso do lado da config). Não é segredo: é um nome. |
| `AOS_ISSUER_PUBKEY` | *(vazio ⇒ modo de **referência**, autoridade **co-localizada**)* | Pubkey ed25519 do issuer em hex (**64 hex chars = 32 bytes**). Presente ⇒ **trust-anchor-only endurecido**: o nó compõe só o verifier e **nenhuma chave de assinatura entra no processo**. Malformada ⇒ **ABORTA** (`ErrBadIssuerPubKey`). Material **público** — pode viver na receita de deployment. |
| `AOS_ISSUER_KEY_PATH` | *(vazio ⇒ chave gerada por **CSPRNG a cada arranque**)* | **Só no modo de referência.** Ficheiro de *seed* ed25519 que a autoridade co-localizada carrega/persiste, para que os tokens emitidos **sobrevivam ao reinício**. ⚠️ **É o único caminho por onde material PRIVADO entra no processo do nó** — monte-o read-only e fora da imagem, e prefira o modo endurecido. Com `AOS_ISSUER_PUBKEY` definida esta variável **nem é lida** (no modo endurecido nenhuma chave de assinatura entra; um `Config` composto in-process com ambas aborta com `ErrConflictingIssuerKey`). |
| `AOS_POLICY_BUNDLE_DIR` | *(vazio ⇒ PDP **NÃO-CARREGADO**, default-deny **EXPLÍCITO**)* | Directório do bundle Cedar **assinado** que o nó carrega via `pdp.Open(dir, WithTrustAnchor(...))` (AOS-220, achado #5 / DEF-604). Presente ⇒ o nó **medeia** as tool calls pela **allowlist assinada + regras Cedar** (uma call permitida pela política **passa**); ausente ⇒ o PDP fica `NewUnloaded` e **TODA** a tool call mediada é **negada** fail-closed — o default-deny deixa de ser um acidente silencioso e é **declarado no banner**. **Exige** `AOS_POLICY_TRUST_ANCHOR` (senão `ErrPolicyBundleNeedsTrustAnchor`). Bundle **ausente/adulterado/assinado por outra chave** ⇒ **ABORTA** (`ErrPolicyBundleLoad`) — recusado, **não** carregado. **Retro-compat:** sem a variável o binário arranca na mesma (default-deny explícito). |
| `AOS_POLICY_TRUST_ANCHOR` | *(vazio ⇒ **obrigatória** quando `AOS_POLICY_BUNDLE_DIR` está definido)* | Pubkey ed25519 (**64 hex chars = 32 bytes**) do **trust anchor** da política, forçado **out-of-band** (AOS-220): a assinatura do bundle é verificada contra **esta** chave do ambiente e **nunca** contra o `trust_anchor.pub` do próprio directório mutável do bundle — fecha o cold-start em que quem tem escrita no dir substitui o anchor **e** re-assina o bundle com a sua chave. Malformada ⇒ **ABORTA** (`ErrBadPolicyTrustAnchor`). Só é lida quando `AOS_POLICY_BUNDLE_DIR` está definido. Material **público** — pode viver na receita de deployment; a chave **PRIVADA** assina o bundle **fora** do nó e nunca entra no processo. |
| `AOS_HUMANS` | `operator` | Lista CSV dos **humanos autorizados** na allowlist da autoridade de identidade **de referência** (`integration.NewAllowlistDirectory`) — a raiz de delegação de onde a autoridade minta. **Só tem efeito no modo de referência**: no modo endurecido o directório de humanos vive **com a autoridade externa** e a variável é ignorada. **Fail-closed:** no modo de referência, uma lista definida mas **sem nenhuma entrada válida** (ex.: `AOS_HUMANS=","`) ⇒ **ABORTA** (`ErrNoHumans`) — a autoridade não teria quem autenticar. É `DEMO-GRADE-AUTH`: uma allowlist de nomes, **não** autenticação (OIDC/WebAuthn é a porta por preencher, EPIC-16); o banner declara a cardinalidade (`humanos autorizados na autoridade: N`). |
| `AOS_HUMAN_OIDC_ISSUER` | *(vazio ⇒ **allowlist** de referência)* | Issuer OIDC (URL) do IdP que autentica os **humanos** que mintam NHI — **frente 1 do D4** (AOS-174/AOS-228; fecha `DEF-110`). Presente ⇒ a autoridade de identidade **de referência** compõe o `OIDCDirectory` (em vez da allowlist) e a via **sem prova é RECUSADA**: o humano-raiz da delegação passa a vir do `sub` de um **ID-token verificado**, não de `AOS_HUMANS`. **Só tem efeito no modo de referência** (autoridade co-localizada); no modo endurecido (`AOS_ISSUER_PUBKEY`) o directório humano vive com o **issuer externo** (`cmd/aos-issuer`), não no nó. **Exige** `AOS_HUMAN_OIDC_AUDIENCE` (senão `ErrBadHumanOIDC`, **aborta**). Transporte **https** (loopback exceptuado) ou aborta. Material **público** (uma URL de issuer); o **tenant concreto** é infra-org. |
| `AOS_HUMAN_OIDC_AUDIENCE` | *(vazio ⇒ **allowlist**)* | Client id (`aud`) que o ID-token do humano **tem** de conter. Par obrigatório de `AOS_HUMAN_OIDC_ISSUER` (definir só um ⇒ `ErrBadHumanOIDC`). Material **público**. |
| `AOS_HUMAN_OIDC_JWKS_URI` | *(vazio ⇒ **discovery** via issuer)* | Endpoint JWKS **directo** do IdP humano — salta o `.well-known/openid-configuration`. Só se lê quando `AOS_HUMAN_OIDC_ISSUER` está definido. Material **público**. |
| `AOS_BOARD_REGIONS` | *(**não definida** ⇒ `board:aos-demo=eu`, soberania de leitura **LIGADA**)* | Registo `board=regiao,board2=regiao2` que **semeia** a fonte de autoridade board→região. **Impacto de conformidade — três estados, incluindo um kill-switch:** ver [Soberania de leitura](#soberania-de-leitura--aos_board_regions-e-o-kill-switch-aos-172--d7-endurecido-em-aos-203). **AOS-205:** deixou de ser tratado como verdade congelada — é a **semente** de uma [fonte de autoridade com rotação+auditoria](#credencial-forte-e-fonte-de-autoridade-da-soberania-de-leitura-aos-205); em produção **não basta** (ver `AOS_SOVEREIGN_OIDC_ISSUER`). |
| `AOS_SOVEREIGN_OIDC_ISSUER` | *(vazio ⇒ **credencial por headers demo-grade**, só fora de produção)* | Issuer OIDC (URL) do **IdP de soberania** contra o qual o **leitor de governação** e o **operador DSAR** apresentam uma **credencial forte** (ID-token). Presente ⇒ o read-path soberano **exige** um ID-token verificado (AOS-174) e **deriva o board das CLAIMS verificadas**, não do header `X-Aos-Board` auto-declarado — um `X-Aos-Board` forjado é **recusado**. **Segurança/conformidade:** em `AOS_MODE=production` é **obrigatório** (senão `ErrProductionNeedsSovereignAuthority`, AOS-205); definir só um de issuer/audience ⇒ **ABORTA** (`ErrBadSovereignOIDC`). Transporte **https** (loopback exceptuado) ou aborta. Material **público** (uma URL de issuer). O **tenant concreto** é config — o provisionamento do IdP fica DEFERIDO. |
| `AOS_SOVEREIGN_OIDC_AUDIENCE` | *(vazio ⇒ **credencial por headers demo-grade**)* | Client id (`aud`) que o ID-token do leitor/operador **tem** de conter. Par obrigatório de `AOS_SOVEREIGN_OIDC_ISSUER` (um só ⇒ `ErrBadSovereignOIDC`). Material **público**. |
| `AOS_SOVEREIGN_OIDC_JWKS_URI` | *(vazio ⇒ **discovery** via issuer)* | Endpoint JWKS **directo** do IdP de soberania — salta o `.well-known/openid-configuration`. Transporte **https** (loopback exceptuado) ou aborta. Material **público**. |
| `AOS_SOVEREIGN_OIDC_MAX_AGE` | *(vazio ⇒ **`5m`**)* | Duração Go (ex.: `5m`, `120s`) do **tecto de idade** (`iat`) do ID-token de soberania. **Segurança — anti-replay:** o tecto é aplicado **SEMPRE** (nunca 0), pelo que um ID-token legitimamente emitido e **capturado** deixa de ser reapresentável durante toda a janela `exp` — a janela fica limitada a `MaxAge`+*leeway*, **independentemente** de o IdP emitir `jti`. Só se lê quando `AOS_SOVEREIGN_OIDC_ISSUER`/`AUDIENCE` estão definidos. Valor **não-parseável ou ≤ 0 ⇒ ABORTA** (`ErrBadSovereignOIDC`) — não degrada para "sem tecto". Material **público**. |
| `AOS_SOVEREIGN_OIDC_REQUIRE_JTI` | *(vazio ⇒ **`false`**)* | Booleano (`1`/`true`/`0`/`false`…). **Segurança — single-use estrito:** presente e verdadeiro ⇒ o ID-token de soberania **tem** de trazer `jti` (senão recusado), e a reutilização do mesmo `(iss,jti)` é recusada por-token. Com `false`, um IdP que emita `jti` continua a ter deteccão de reutilização por-token, e o `MaxAge` cobre o caso sem `jti`. Só se lê quando `AOS_SOVEREIGN_OIDC_ISSUER`/`AUDIENCE` estão definidos. Valor **não-booleano ⇒ ABORTA** (`ErrBadSovereignOIDC`). Material **público**. |
| `AOS_EVENTSTORE_PATH` | *(vazio ⇒ Event Store **in-memory**)* | Estado durável — ver [Estado durável](#estado-durável--variáveis-de-ambiente-aos-170--aos-180). |
| `AOS_WORM_PATH` | *(vazio ⇒ WORM **in-memory**)* | Trilho WORM tamper-evident — ver [Estado durável](#estado-durável--variáveis-de-ambiente-aos-170--aos-180). **Conformidade:** in-memory, o trilho de auditoria **não sobrevive** ao contentor. |
| `AOS_DURABLE_EXECUTION` | *(vazio ⇒ **DESLIGADA**)* | Execução durável — ver [Estado durável](#estado-durável--variáveis-de-ambiente-aos-170--aos-180) e a [postura de produção](#postura-de-produção-de-aos_durable_execution--decisão-aos-203) decidida em AOS-203. |
| `AOS_OPERATORS` | *(vazio ⇒ **default-deny**)* | Pubkeys dos operadores do canal de controlo — ver [Plano de controlo](#plano-de-controlo--operadores-e-aprovadores-aos-160--aos-162-config-em-aos-193). **Segurança:** vazio ⇒ `steer`/`pause` recusados **e** bind não-loopback recusado. |
| `AOS_APPROVERS_FILE` | *(vazio ⇒ **four-eyes DESLIGADO**)* | Ficheiro JSON montado com a *roster* do dual-control — ver [Plano de controlo](#plano-de-controlo--operadores-e-aprovadores-aos-160--aos-162-config-em-aos-193). |
| `AOS_ATTESTATION_VERIFIER_URL` | *(vazio ⇒ **attestation DESLIGADA**)* | URL do **componente de autoridade externo** que verifica a **attestation de dispositivo WebAuthn** (AOS-177) — ex.: `http://attestation:8090/verify`. Presente ⇒ liga o `FourEyesGate` ao [`integration.RemoteDeviceAttestationVerifier`] (cliente HTTP **STDLIB**; o descodificador **CBOR** corre no componente externo, **nunca** no nó — ADR-017/zero-dep) e cada perna de aprovação passa a **EXIGIR** `attestationObject`+`clientDataJSON` válidos: o *enforcement* dormente de AOS-177 fica **ACTIVO**. Fail-closed: exige `https` (ou `http` só em loopback), senão o boot **ABORTA** (`ErrRemoteAttestationURL`); um componente indisponível **NEGA** a aprovação (não degrada). Só tem efeito com `AOS_APPROVERS_FILE`. Material **público** (uma URL). |
| `AOS_ATTESTATION_VERIFIER_TOKEN_PATH` | *(vazio ⇒ **sem bearer**)* | Caminho do **ficheiro montado** com um bearer opcional apresentado ao componente de autoridade de attestation. ⚠️ **Material PRIVADO por FICHEIRO montado** (nunca por variável de ambiente), no padrão de `AOS_DSAR_VAULT_TOKEN_PATH`. Ilegível ⇒ **ABORTA** o boot. |
| `AOS_RATIFIERS` | *(vazio ⇒ **toda a promoção NEGADA**)* | Pubkeys dos ratificadores do *promotion controller* (AOS-206) — ver [Plano de controlo](#plano-de-controlo--operadores-e-aprovadores-aos-160--aos-162-config-em-aos-193). **Segurança:** o controller é composto **sempre** pela via sancionada (freshness + nonce-store durável **forçados**); vazio ⇒ nenhum artefacto de auto-modificação pode ser promovido (`ratifier_unknown`). |
| `AOS_RETENTION_VERSION` | *(vazio ⇒ **nada expira**)* | Versão **SemVer** (`MAJOR.MINOR.PATCH`) que rotula a política de retenção TTL (AOS-092/AOS-213) — auditável no selo WORM da expiração. **Ambas-ou-nenhuma** com `AOS_RETENTION_PERIODS`: definir só uma **ABORTA** (`ErrBadRetention`). Ambas vazias ⇒ o `ExpirationJob` continua composto mas **nada expira** (`POST /dsar/expire` varre sem apagar), o comportamento por omissão. Versão não-SemVer ⇒ **ABORTA**. |
| `AOS_RETENTION_PERIODS` | *(vazio ⇒ **nada expira**)* | Política TTL **por classe**: `"classe=duracao,..."` (ex.: `pii_operational=720h,audit=8760h`). `classe` ∈ `{diagnostic,trajectory,audit,pii_operational}` (**classe desconhecida ABORTA** — um typo não deixa dados por expirar em silêncio); `duracao` é um `time.Duration` **> 0**; uma classe **omitida = nunca expira** (semântica de AOS-092). Exige `AOS_RETENTION_VERSION`. **Segurança/conformidade:** a expiração é **crypto-shred da KEK por-titular** (apagamento real, AOS-093) e **RESPEITA o legal hold**; a granularidade é por-titular (residual da KEK única nomeado em `retention.go`). Duração ≤0/ilegível ⇒ **ABORTA** (`ErrBadRetention`). |
| `AOS_DSAR_VAULT_ADDR` | *(vazio ⇒ **vault in-memory demo-grade**)* | URL do **HashiCorp Vault** para a custódia EXTERNA da KEK por-titular (AOS-215/AOS-216) — ex.: `http://vault:8200` (dev) ou `https://vault:8200` (prod, cert via `SSL_CERT_FILE`). Presente ⇒ `Config.DSARVault` liga o adaptador **key-never-leaves** (motor **Transit**): a KEK vive no Vault, o embrulho/desembrulho da DEK corre lá, e o `/dsar/erase`/expiração fazem **crypto-shred** destruindo a chave Transit do titular. Vazio ⇒ o vault de referência in-memory (KEK em memória, perde-se no restart). **Exige** `AOS_DSAR_VAULT_TOKEN_PATH` (senão `ErrBadVaultDSAR`, **ABORTA** — não degrada para o in-memory). Material **público** (uma URL). |
| `AOS_DSAR_VAULT_TOKEN_PATH` | *(vazio ⇒ **sem custódia externa**)* | Caminho do **ficheiro montado** com o token do Vault. ⚠️ **Material PRIVADO por FICHEIRO montado** (nunca por variável de ambiente), no padrão de `AOS_ISSUER_KEY_PATH`/`AOS_TLS_KEY_PATH`; monte-o read-only e fora da imagem. Ilegível/vazio quando `AOS_DSAR_VAULT_ADDR` está definido ⇒ **ABORTA** (`ErrBadVaultDSAR`). Em produção prefira um token de curta duração (AppRole/Kubernetes-auth). |
| `AOS_DSAR_VAULT_TRANSIT_MOUNT` | `transit` | Caminho de mount do motor **Transit** no Vault (só usado quando `AOS_DSAR_VAULT_ADDR` está definido). Permite um mount não-default (ex.: `transit-aos`). Material **público**. |
| `AOS_MODEL_ENDPOINT` | *(vazio ⇒ **modelo de REFERÊNCIA**)* | Raiz de uma API **OpenAI-compatível** (ex.: OmniRoute/OpenRouter/LiteLLM) para o **Model Gateway** do nó (EPIC-06). Presente ⇒ o nó compõe o gateway REAL (`packages/platform/model-gateway`, `NewProduction`) — allowlist regional **assinada** (embebida), keypool, routing de failover, metering/pricing e endurecimento SSRF — e liga-o a `Config.Model`; ausente ⇒ o `referenceModel` (turno único fixo). **Exige** `AOS_MODEL_NAME` (senão `ErrBadModelConfig`). Aceita `http://` (dev, seam de egress delegado) e `https://` (prod). Material **público** (uma URL). |
| `AOS_MODEL_NAME` | *(vazio ⇒ **sem gateway**)* | Id do modelo a pedir ao gateway (o `model` do `/v1/chat/completions`, ex.: o modelo configurado no OmniRoute). Obrigatório quando `AOS_MODEL_ENDPOINT` está definido; senão `ErrBadModelConfig` (**ABORTA**, não degrada para o modelo de referência). Material **público**. |
| `AOS_MODEL_API_KEY_PATH` | *(vazio ⇒ **sem bearer upstream**)* | Caminho do **ficheiro montado** com a API key do gateway upstream (torna-se o segredo de infra que o `CredentialProvider` do gateway serve). ⚠️ **Material PRIVADO por FICHEIRO montado** (nunca por variável de ambiente), no padrão de `AOS_ISSUER_KEY_PATH`. Ilegível/vazio ⇒ **ABORTA** (`ErrBadModelConfig`). Ausente ⇒ o nó usa um bearer de dev não-vazio (o upstream ignora-o se não o exigir). |
| `AOS_MODEL_REGION` | `eu` | Região de soberania que o nó declara ao Model Gateway. Default casa com a allowlist EMBEBIDA (`board-eu`→`eu`); com um bundle externo (`AOS_MODEL_ALLOWLIST_BUNDLE_DIR`) o operador escolhe-a. Tem de constar da allowlist em vigor, senão a chamada é **negada fail-closed**. Material **público**. |
| `AOS_MODEL_BOARD` | `board-eu` | Board de soberania que o nó declara ao Model Gateway. Default casa com a regra `board-eu` da allowlist EMBEBIDA; com um bundle externo o operador escolhe-o. Tem de constar da allowlist, senão **negado fail-closed**. Material **público**. |
| `AOS_MODEL_ALLOWLIST_BUNDLE_DIR` | *(vazio ⇒ **allowlist EMBEBIDA**)* | Directório com um **bundle de allowlist ASSINADO** (`allowlist_policy.json` + `allowlist_policy.sig`) que **substitui** a allowlist regional embebida do gateway — a via gémea do bundle PDP (AOS-220), que remove o acoplamento "modelos fixos no código". Presente ⇒ o operador curadoria/assina o catálogo `(board, modelo, região)` e o nó pode pedir **nomes de modelo reais** (fim dos aliases). **Exige** `AOS_MODEL_ALLOWLIST_TRUST_ANCHOR`. Bundle **ausente/adulterado/assinado por outra chave** ⇒ **ABORTA** (`ErrBadModelAllowlist`). Ausente ⇒ embebida (trust anchor pinado), inalterado. Material **público** (a policy). |
| `AOS_MODEL_ALLOWLIST_TRUST_ANCHOR` | *(vazio ⇒ **sem allowlist externa**)* | **Pubkey ed25519 em base64** do assinante do bundle de allowlist externo — o trust anchor **forçado out-of-band** (nunca lido do próprio bundle, no padrão de `AOS_POLICY_TRUST_ANCHOR`). Obrigatório quando `AOS_MODEL_ALLOWLIST_BUNDLE_DIR` está definido; malformado / bundle que não verifica ⇒ **ABORTA** (`ErrBadModelAllowlist`). Material **público** (a pubkey). |
| `AOS_MODEL_TOOLS_REGISTER` | *(vazio ⇒ **não registado**; revalidação nega)* | Booleano (`1`/`true`/`on`…). Presente e verdadeiro ⇒ as tools de `AOS_MODEL_TOOLS` são **registadas como um catálogo ASSINADO e congelável**, para a **revalidação de registry** do Reference Monitor (AOS-051, o 2.º hook) as **ADMITIR**. **Efeito de segurança — move o gate:** sem isto, uma tool oferecida ao modelo mas sem contrato assinado registado é **negada pela revalidação** (`denied_by=revalidation`, trust store vazio) **antes** do PDP; com isto a revalidação passa e a decisão flui ao **gate seguinte, o PDP (3.º hook)**. O PDP tem, ANTES do Cedar, o gate de **allowlist de capabilities por `agent_class`** (AOS-007, `capabilities/allowlist.json` assinado): a capability tem de constar da allowlist da classe (as committadas são `agent-worker`=cap:http.post+cap:fs.read, `agent-reader`=cap:fs.read, `agent-break-glass`=`*`). Passando esse gate, o Cedar avalia sob o taint: uma tool call originada pelo modelo tem `taint=untrusted`, logo `cap:http.post` é **negada pelo taint-gate** (`allow_http_post` exige `context.taint != "untrusted"`, AOS-069/P4), enquanto `cap:fs.read` (regra sem cláusula de taint) passa o PDP. Ver `demo-pdp-taint-gate.sh` (isola o taint por A/B, com NHI de classe `agent-worker`). ⚠️ **DEV-GRADE (auto-assinado):** o nó gera uma chave ed25519 **efémera** ao arranque, assina o catálogo e **confia nela** (trust store in-process) — análogo, no eixo do registry, à identidade demo-only self-minted; em **produção** o catálogo é assinado **out-of-band** por um publicador confiável e a pubkey é forçada por config, **nunca** a própria chave do nó. Ligado mas com `AOS_MODEL_TOOLS` ausente/mal formado, ou `egress` inválido num spec ⇒ **ABORTA** (`ErrBadModelToolsRegister`). Material **público**. |
| `AOS_MODEL_TOOLS` | *(vazio ⇒ **modelo SEM tools**)* | Caminho de um **ficheiro JSON montado e TRUSTED** com o **registry de tools** OFERECIDAS ao modelo (análogo mínimo do EPIC-05). Cada entrada declara a **face do modelo** (`name`/`description`/`parameters` — o `tools` do request OpenAI, sem o qual o modelo **não emite** `tool_calls`) **e** o **binding de governança** (`capability` + `resource_type`/`resource_value`/`resource_region`) que o **Reference Monitor** avalia. Presente ⇒ cada tool call é **MEDIADA** pelo RM/PDP no ponto único (ADR-002): o binding vem do **registry** (config trusted), o modelo só escolhe **qual** tool pelo nome, e o `AuthorizationTaint` fica **untrusted** (fail-closed) — logo uma capability privilegiada pedida pelo modelo é **NEGADA** pelo taint-gate (AOS-069, "untrusted não comanda"). Tool fora do registry ⇒ capability vazia ⇒ **default-deny**. Ficheiro ilegível / JSON inválido / entrada sem `name`+`capability` ⇒ **ABORTA** (`ErrBadModelTools`). Ausente ⇒ o modelo não recebe tools (comportamento inalterado). Material **público** (schema + binding). |
| `AOS_SANDBOX_DRIVER` | `fake` | Driver de execução de tools em **sandbox** (AOS-005/AOS-064) quando alguma tool de `AOS_MODEL_TOOLS` declara um bloco `sandbox`. `fake` = **jail funcional in-process** (RootFS overlay read-only, seccomp default-deny, escape bloqueado, host **nunca** tocado) — determinista, sem host especial. `firecracker`/`gvisor` = **microVM/gVisor reais**: exigem **KVM**/`runsc` no host; sem eles o registo do launcher passa, mas a execução devolve `ErrDriverUnavailable` (o caminho de produção fica **WIRED**, só falta o host — **infra do dono, não código**). Ausente ⇒ `fake`. Material **público**. |
| `AOS_SANDBOX_SEED_DIR` | *(vazio ⇒ **base vazia**)* | Directório (opcional) cujo **nível de topo** semeia o **RootFS BASE read-only** da sandbox (AOS-066): cada ficheiro vira uma entrada base pelo seu nome, legível pelas tools mediadas (ex.: um `doc_read` que lê `notes`). Sem a variável ⇒ base vazia (a tool lê o que a call montar/produzir). Só se aplica ao driver `fake`; para `firecracker` a semente vive no rootfs do **orchestrator** (`FC_SEED_DIR` do componente externo). Ilegível ⇒ **ABORTA**. Material **público** (conteúdo de referência). |
| `AOS_SANDBOX_FIRECRACKER_URL` | *(vazio ⇒ **skeleton**)* | URL do **orchestrator Firecracker** (componente host-side externo, ex.: `http://firecracker:9100/exec`) que conduz a microVM REAL sobre KVM. Só se aplica com `AOS_SANDBOX_DRIVER=firecracker`: presente ⇒ o nó INJECTA um executor remoto (HTTP stdlib) no driver e uma tool call permitida EXECUTA numa microVM dedicada (fronteira de virtualização de hardware, ADR-004) — o nó fica **zero-dep** (o firecracker/jailer correm no orchestrator, não no nó, no padrão do serviço `attestation`). Ausente ⇒ o driver firecracker é o **skeleton** e o exec devolve `ErrDriverUnavailable` (o gap honesto: o executor não está provisionado). Material **público** (a URL interna). |
| `AOS_CONTROL_MTLS_CA_PATH` | *(vazio ⇒ **mTLS de controlo DESLIGADO**)* | Caminho do **bundle PEM da CA de cliente** que autentica o plano de controlo por **certificado** (DEF-012, EIXO 1) — ver [mTLS do plano de controlo](#mtls-do-plano-de-controlo--autenticação-forte-otlp-def-012). Presente ⇒ `/steer`,`/pause`,`/approve` exigem um **certificado de cliente verificado**, **ALÉM** da assinatura ed25519 do corpo (AOS-160) — é **ADITIVO, nunca um bypass**. Exige **terminação TLS no nó** (senão `ErrControlMTLSNeedsNodeTLS`); bundle inválido ⇒ `ErrBadControlMTLSCA`. Material **público** (a CA). |
| `AOS_OTLP_CLIENT_CERT_PATH` | *(vazio ⇒ **sem mTLS de cliente OTLP**)* | Caminho do **certificado** (PEM) que o exporter apresenta ao colector OTLP no mTLS de cliente (DEF-012, EIXO 2) — ver [mTLS do plano de controlo](#mtls-do-plano-de-controlo--autenticação-forte-otlp-def-012). Exige `AOS_OTLP_CLIENT_KEY_PATH` (só um ⇒ `ErrIncompleteOTLPClientTLS`). Só se aplica com `AOS_OTLP_ENDPOINT` definido. **Fail-open** de AOS-173 preservado. Material **público**. |
| `AOS_OTLP_CLIENT_KEY_PATH` | *(vazio ⇒ **sem mTLS de cliente OTLP**)* | Caminho da **chave privada** (PEM) do certificado de cliente OTLP (DEF-012, EIXO 2). ⚠️ **Material PRIVADO por FICHEIRO montado** (nunca por variável de ambiente), no padrão de `AOS_TLS_KEY_PATH`. Par inválido ⇒ `ErrBadOTLPClientCert`. Ver [mTLS do plano de controlo](#mtls-do-plano-de-controlo--autenticação-forte-otlp-def-012). |
| `AOS_OTLP_BEARER_TOKEN_PATH` | *(vazio ⇒ **sem bearer OTLP**)* | Caminho do ficheiro com o **bearer token** que o exporter envia ao colector OTLP em `Authorization: Bearer …` (DEF-012, EIXO 2). ⚠️ **É um SEGREDO por FICHEIRO montado** — nunca por variável de ambiente, nunca em código/fixtures; o nó **nunca** o ecoa em logs/spans/erros. Ficheiro ilegível/vazio ⇒ `ErrBadOTLPBearerToken`. Só se aplica com `AOS_OTLP_ENDPOINT` definido. **Fail-open** preservado. Ver [mTLS do plano de controlo](#mtls-do-plano-de-controlo--autenticação-forte-otlp-def-012). |
| `AOS_OTLP_ENDPOINT` | *(vazio ⇒ **`NoopTracer`**, zero overhead)* | URL http(s) **absoluto** do colector OTLP/HTTP (ex.: `http://collector:4318`; o nó completa com `/v1/traces`). Presente ⇒ exporta os spans `invoke_agent`/`chat`[+custo]/`execute_tool`/`freeze` e os selos WORM. Um endpoint **malformado ABORTA** o arranque (`ErrBadOTLPEndpoint`) — o nó não sobe a fingir que exporta. A exportação em si é **fail-open** (a telemetria nunca derruba o nó). **Privacidade:** os spans transportam metadados de governação e custo, não conteúdo de *prompts*; ainda assim o destino é uma fronteira de dados — aponte-o para dentro do seu perímetro. |
| `AOS_READER` | *(vazio)* | **Lado CLIENTE** (`aos observe`): default da flag `--reader`, transportada no header `X-Aos-Reader`. É a **identidade de leitura** declarada pelo cliente; com a soberania de leitura ligada, o **nó** é que a exige e a resolve — a CLI só a transporta. Ausente contra um nó soberano ⇒ `404`. |
| `AOS_BOARD` | *(vazio)* | **Lado CLIENTE** (`aos observe`): default da flag `--board`, transportada no header `X-Aos-Board`. Board de governação do leitor, de onde o nó resolve a **região autorizada**. Ausente ou desconhecido contra um nó soberano ⇒ `404` (fail-closed). |
| `AOS_HEALTH_URL` | *(vazio ⇒ derivada de `AOS_API_ADDR`)* | **Override opcional** do URL sondado pelo `aos-healthprobe` do `HEALTHCHECK` (lida por `deploy/node/healthprobe`, **não** pelo nó). Sem ela o probe deriva `127.0.0.1:<porta de AOS_API_ADDR>/healthz` — ver [Health / probes](#health--probes). |

> **Nenhuma destas variáveis transporta segredos**, com as excepções declaradas de
> `AOS_ISSUER_KEY_PATH`, `AOS_TLS_KEY_PATH`, `AOS_OTLP_CLIENT_KEY_PATH` e
> `AOS_OTLP_BEARER_TOKEN_PATH` (que transportam um **caminho** para material privado, não o
> material — o bearer é ele próprio um segredo, e o nó nunca o ecoa em logs/spans/erros). O
> banner de arranque não ecoa valores de chaves: as mensagens de erro de `AOS_OPERATORS` e do
> ficheiro de aprovadores identificam a entrada pelo `emitterID`/`principal` e **nunca** imprimem
> a pubkey.

> **Precedência e formato.** Todas as variáveis são lidas **uma vez, no arranque** (não há
> *reload* a quente: para mudar config, substitua o contentor). Todos os valores são
> `TrimSpace`-ados. A gramática plana `a=b,c=d` é partilhada por `AOS_BOARD_REGIONS` e
> `AOS_OPERATORS`, deliberadamente.

### Soberania de leitura — `AOS_BOARD_REGIONS` e o kill-switch (AOS-172 / D7, endurecido em AOS-203)

`AOS_BOARD_REGIONS` tem **três** estados, e a diferença entre "não definida" e "definida vazia"
é a diferença entre um controlo de conformidade **ligado** e **desligado**:

| Estado da variável | Registo `board→região` | Read-path |
|---|---|---|
| **NÃO definida** (ausente do ambiente) | default de referência `board:aos-demo=eu` | **SOBERANO** — authz por-chamador (D7) + selo WORM da leitura sensível (D6) |
| **DEFINIDA VAZIA** (`-e AOS_BOARD_REGIONS=`) | vazio | **LEGADO** — ⚠️ **KILL-SWITCH**: sem authz por-chamador e **sem selo** |
| **DEFINIDA com valor** (`board:prod=eu`) | o que for declarado | **SOBERANO** |
| **DEFINIDA malformada** (`aos-demo`, sem `=`) | — | **ABORTA** o arranque (`ErrBadBoardRegions`) |

**O que o kill-switch desliga**, exactamente:

1. **Authz POR-CHAMADOR das leituras de governação (D7).** Com ele desligado o nó serve
   **todas** as leituras sem exigir `X-Aos-Reader`/`X-Aos-Board` e sem resolver a região
   autorizada do board do leitor — qualquer chamador que alcance a porta lê qualquer *run*.
2. **Selo WORM da leitura sensível (D6).** Deixa de existir trilho *tamper-evident* de **quem
   leu o quê** — a evidência de acesso a dados de governação desaparece, não fica degradada.

**Postura por modo — o que este ticket mudou e o que não mudou:**

- Em **`AOS_MODE=production`** o estado vazio **RECUSA o arranque** (`ErrProductionNeedsSovereignRead`,
  `exit 1`). **Isto já existia e não foi tocado**: um nó de produção nunca serve o read-path legado.
- **Fora de produção** o estado vazio continua **permitido** (os *harnesses*
  `aos169-durability-harness.sh` e `aos193-control-plane-harness.sh` usam-no deliberadamente, para
  isolarem o eixo que testam) — mas **deixou de ser silencioso**. O banner passa a emitir um aviso
  proeminente (AOS-203):

```text
[aos] AVISO KILL-SWITCH (AOS-203): SOBERANIA DE LEITURA (AOS-172, D7) DESLIGADA — AOS_BOARD_REGIONS esta DEFINIDA-VAZIA (kill-switch explicito: a variavel existe no ambiente com valor vazio)
[aos] => FICA DESLIGADO: (1) AUTHZ POR-CHAMADOR das leituras de governacao (D7) — o no serve TODAS as leituras sem exigir X-Aos-Reader/X-Aos-Board nem resolver a regiao autorizada do board; (2) SELO WORM da leitura sensivel (D6) — nao fica trilho tamper-evident de QUEM leu o que
[aos] => PARA RELIGAR: defina AOS_BOARD_REGIONS="board=regiao" (ex.: AOS_BOARD_REGIONS="board:prod=eu") ou REMOVA a variavel do ambiente para voltar ao default de referencia "board:aos-demo=eu"
[aos] => IGNORE a linha "defina Config.BoardRegions" do banner acima: Config.BoardRegions e um campo de codigo (package main) que quem corre o binario/imagem NAO consegue escrever — o unico remedio alcancavel e AOS_BOARD_REGIONS, na linha anterior
[aos] => AOS_MODE=production RECUSA arrancar neste estado (ErrProductionNeedsSovereignRead) — este aviso so existe porque o no NAO esta em modo de producao
```

> ⚠️ **Uma linha do banner ainda aponta para um remédio inalcançável (residual conhecido).** Umas
> linhas acima do aviso, o *composition-root* imprime `soberania de leitura (AOS-172, D7): read-path
> LEGADO (sem authz por-chamador nem selo) — defina Config.BoardRegions …`. **`Config.BoardRegions`
> é um campo de código** (`package main`): quem corre o binário ou a imagem **não o consegue
> escrever**. É metade do próprio defeito que esta secção fecha — sintoma verdadeiro, remédio
> impossível. Enquanto essa linha não for reescrita (exige tocar em `packages/cmd/aos/bootstrap.go`,
> fora da propriedade de ficheiros de AOS-203), o aviso **neutraliza-a pelo nome** — é a linha
> `IGNORE a linha "defina Config.BoardRegions"` acima. **Se fizer `grep` ao banner, leia o bloco
> `AVISO KILL-SWITCH` inteiro, não só a linha do `read-path LEGADO`.**

> **O gate real do read-path são DUAS coisas.** A *read-governance* só é composta quando o registo
> `board→região` **e** o WORM existem ambos (o selo D6 não teria onde ser gravado). Hoje o
> `Bootstrap` nunca deixa o WORM ausente (cai para um WORM in-memory), pelo que a distinção não é
> alcançável por configuração; ainda assim o aviso avalia **a conjunção**, não só o registo — se um
> dia o WORM se tornar opcional, o nó **avisa** em vez de anunciar uma soberania que não aplica.

> **Porquê avisar e não recusar fora de produção?** Recusar quebraria a retro-compatibilidade da
> superfície de configuração e cortaria um estado que o próprio projecto usa nos *harnesses*. O
> critério é o de AOS-191: o que não se tolera é a **promessa falsa** — um nó que anuncia uma
> postura mais forte do que a que cumpre. Daí o aviso nomear, sem eufemismo, o que ficou
> desligado. Em produção, onde a promessa é implícita, a resposta continua a ser **recusar**.

> **Âmbito honesto do que fica ligado.** Com o registo não-vazio, o selo D6 grava a região do
> **board do leitor**, não a residência **por-run** do dado; a verificação
> `leitor.região == run.região` fica **DEFERIDA** até haver `board→região` por-*run* (EPIC-09/10),
> e o banner declara-o. O provisioning real de regiões/boards (IdP de soberania) é igualmente
> deferido: o registo aqui é **DEMO-GRADE self-hosted**. A **regra** fail-closed é que é fixa.

### Credencial forte e fonte de autoridade da soberania de leitura (AOS-205)

AOS-205 fecha o eixo que os deferimentos `DEF-201/203…211` nomeavam: o registo `board→região`
deixou de ser o **mapa estático de `AOS_BOARD_REGIONS` tratado como verdade**, e os headers
`X-Aos-Reader`/`X-Aos-Board` deixaram de ser **auto-declarados**. Duas mudanças:

1. **Fonte de autoridade com rotação + auditoria.** `AOS_BOARD_REGIONS` passa a ser a **semente**
   de uma `SovereignRegionAuthority`: um registo `board→região` que pode ser **re-provisionado**
   (rotação) e cujo provisionamento inicial e cada rotação são **selados na hash-chain WORM**
   (revisão monotónica + impressão digital do conjunto, **sem PII**). A **regra** fail-closed
   `board→região` continua a ser a de AOS-094 (não se duplica); o que muda é a **fonte**.
2. **Credencial forte verificada.** Com `AOS_SOVEREIGN_OIDC_ISSUER`+`AOS_SOVEREIGN_OIDC_AUDIENCE`
   definidos, o **leitor de governação** e o **operador DSAR** apresentam um **ID-token OIDC** que
   o nó **verifica** (reutilizando a validação real de AOS-174: discovery + JWKS + assinatura JWS +
   anti-alg-confusion + anti-replay). O **board** e o **principal** são **derivados das claims
   verificadas** (`board` e `sub`), **não** do header cru. Um pedido com `X-Aos-Board` **forjado**
   (board válido mas sem credencial, ou credencial de **outro** titular com outro board) é
   **RECUSADO**; um pedido com o ID-token **válido** correspondente é **ACEITE**.

**Anti-replay (o que o *wiring* garante, sem sobre-promessa).** A credencial de leitura é sempre
construída com um **tecto de idade** (`AOS_SOVEREIGN_OIDC_MAX_AGE`, default `5m`, **nunca 0**): um
ID-token de soberania legitimamente emitido e **capturado** deixa de ser reapresentável durante
toda a janela `exp` — a janela fica limitada a `MaxAge`+*leeway*, **independentemente** de o IdP
emitir `jti`. Quando o IdP **emite** `jti`, a reutilização do mesmo `(iss,jti)` é adicionalmente
recusada **por-token**; `AOS_SOVEREIGN_OIDC_REQUIRE_JTI=1` **exige** `jti` (single-use estrito). O
banner de arranque declara a postura em vigor (idade máxima + `jti` obrigatório/oportunista).

**Postura de produção.** Em `AOS_MODE=production` a credencial forte é **obrigatória**
(`ErrProductionNeedsSovereignAuthority`): a produção **nunca** deriva o board de um header
auto-declarado. Fora de produção, sem OIDC configurado, mantém-se a via **legada por headers**
(demo-grade) — retro-compatível para os *harnesses*.

**Fronteira honesta (DEFERIDO).** O **tenant concreto** — a integração com o serviço de
configuração/IdP de soberania **real** da organização que empurra as alterações autoritativas de
`board→região` e emite os ID-tokens — é infra-org, não código do nó. O nó fica com o **CONTRATO**
(fonte rotacionável+auditada e verificação de credencial OIDC/mTLS); o *issuer* concreto e as
rotações entram por config/operador. A coincidência `leitor.região == run.região` no selo D6 é
AOS-182. É o mesmo tratamento de D4/AOS-16x para a identidade.

### DSAR / conformidade — apagamento, legal hold e expiração (AOS-172 / AOS-093 / AOS-213)

O nó expõe quatro rotas de **governança de dados**, todas no plano `POST /dsar/*`. Todas exigem a
**mesma credencial forte** que autentica o read-path soberano (ver [Credencial
forte](#credencial-forte-e-fonte-de-autoridade-da-soberania-de-leitura-aos-205)) — em produção, um
**ID-token OIDC** verificado (o board vem das *claims*, não de um header auto-declarado); fora de
produção, os headers demo-grade `X-Aos-Reader`/`X-Aos-Board`. Todas passam pela *admission* do
plano de controlo (*rate-limit*). **Fail-closed:** sem o gate soberano composto (sem
`AOS_BOARD_REGIONS`), respondem `501`; credencial ausente/forjada/board desconhecido ⇒ `403`.

| Rota | O que faz | Colaborador |
|---|---|---|
| `POST /dsar/erase` | Apagamento (Art. 17): crypto-shred da KEK por-titular; legal hold re-consultado antes do shred; `received`/`key_destroyed`/`blocked` selados no WORM. | `dsar.Flow` (AOS-093) |
| `POST /dsar/hold` | **Coloca** um legal hold (por titular e/ou partição) — SUSPENDE o erase e a expiração desse alvo. | `audit.LegalHold` (AOS-213) |
| `POST /dsar/release` | **Levanta** o legal hold, reabrindo o alvo ao erase/expiração. | `audit.LegalHold` (AOS-213) |
| `POST /dsar/expire` | Conduz **uma passagem** do job de expiração por TTL: expira os registos classificados que cruzaram a retenção e **não** estão sob hold, por crypto-shred da KEK por-titular. | `audit.ExpirationJob` (AOS-092/AOS-213) |

**Contrato do identificador (sem PII).** `subject_id` e `partition` são **pseudónimos/identificadores
opacos** (ULID/UUID/hash *namespaced*, run/stream id) — nunca o dado pessoal em si. São selados
**verbatim** na hash-chain WORM imutável, que o próprio crypto-shredding **não** consegue remover;
por isso a fronteira rejeita (`400`) valores com forma de PII (email, nome, espaços, `@`, `/`,
não-ASCII). Cada acção de hold/release é **selada no WORM sem PII** (quem/quando/subject-pseudónimo/
partição/board) na partição `governance.legalhold`, verificável de forma independente.

**Wire (JSON).**

```jsonc
// POST /dsar/hold  |  POST /dsar/release   — pelo menos um de subject_id/partition
{ "request_id": "req-42", "subject_id": "nhi:agent-7a3f", "partition": "run-19c2" }
// POST /dsar/expire — sem corpo; devolve as contagens da passagem (sem PII)
// 200: { "scanned": 12, "expired": 3, "held": 1, "skipped": 8, "not_expired": 0 }
```

**Expiração por TTL — retenção e granularidade.** O `audit.ExpirationJob` é **composto SEMPRE** no
nó. A política de retenção **TTL-por-classe** (`Config.Retention`, *policy-as-code* versionada) é a
superfície a preencher: **vazia por omissão ⇒ NADA expira** (`POST /dsar/expire` varre e devolve
tudo em `not_expired` — *fail-closed*, nunca se auto-purga o que não tem período definido). A
expiração **materializa** por **crypto-shred da KEK POR-TITULAR** (o mesmo apagamento real de
AOS-093): apagar a chave torna o conteúdo do titular irrecuperável (`audit.OpenContent` →
`ErrDecrypt`) **sem** mutar a hash-chain, que continua a validar. Respeita o legal hold — um titular
sob hold é **saltado**. É conduzida **sob demanda** pela rota (um *scheduler*/*cron* externo
invoca-a periodicamente); o nó não corre um varredor de fundo próprio.

> **Granularidade (residual nomeado, eixo AOS-093/envelope).** O TTL é avaliado **por-registo/classe**
> (idade = relógio − criação), mas o crypto-shred do envelope de AOS-093 é **por-CHAVE-DE-TITULAR**:
> uma KEK embrulha as DEKs de **todos** os registos do titular. A expiração é, por isso,
> **POR-TITULAR** — quando um registo classificado de um titular cruza o TTL (e não há hold), a KEK
> desse titular é destruída, expirando todo o seu conteúdo cifrado. A retenção **diferencial por-classe
> dentro de um mesmo titular** colapsa para a classe que expira primeiro. A granularidade fina
> por-registo exigiria custódia de chave por-registo ou *tombstones* no Event Store (re-arquitectura
> do envelope) — **não previsto**. Ver `DEF-903` em `docs/governance/REGISTO-Deferimentos.md`.

**Postura por `AOS_MODE`.** As rotas herdam a postura do read-path soberano: em `AOS_MODE=production`,
`AOS_BOARD_REGIONS` **e** a credencial forte OIDC (`AOS_SOVEREIGN_OIDC_ISSUER`+`AUDIENCE`) são
**obrigatórias** (senão o arranque recusa), pelo que as rotas `/dsar/*` exigem sempre um ID-token
verificado. Fora de produção, sem soberania configurada, as rotas respondem `501` (desligadas por
declaração). A retenção TTL é **opt-in em qualquer modo** (`Config.Retention` vazia ⇒ nada expira);
o banner declara o estado em cada arranque.

#### Custódia da KEK por-titular — o seam KMS/HSM (AOS-215 / AOS-216 / DEF-302)

Todo o apagamento real (erase e expiração) materializa por **crypto-shred de uma KEK por-titular**: a
chave que embrulha as DEKs do conteúdo cifrado do titular (AOS-093). **Onde essa KEK vive é uma
decisão de deployment**, exposta pela porta `audit.KeyVault` (`EnsureKey`/`Key`/`Delete`) e injectável
por `Config.DSARVault` — o **mesmo molde de precedência** do Event Store e do WORM:

| `Config.DSARVault` | Quem detém a KEK | Durabilidade | Postura |
|---|---|---|---|
| *(nil — omitido)* | `audit.InMemoryKeyVault` de **referência**, dentro do processo do nó | **NÃO-durável**: as KEK vivem em **memória** e **perdem-se no restart** | **DEMO-GRADE** — declarado no banner; adequado só a demo/teste |
| *(injectado)* | um **key-service / software-KMS de custódia EXTERNA** que o operador liga | **do custodiante** (sobrevive ao restart; rotação/backup são dele — o nó **não** os atesta) | **Produção** — a KEK vive **fora** do binário |

O nó entrega o **contrato + a costura + um double de referência** (`InMemoryKeyVault`); a
**implementação concreta** (AWS KMS, HashiCorp Vault, um serviço de chaves interno…) é **infra-org**,
análoga à custódia da chave do issuer (AOS-175/`CUSTODIA-CHAVE-RELEASE.md`) e ao tenant de soberania
(DEF-201/212) — **não** vive no binário e **não** adiciona dependências externas ao nó.

- **Uma única instância** serve o cifrador de conteúdo, o shredder DSAR e o sink de expiração: o
  `/dsar/erase` e o `/dsar/expire` destroem a KEK **onde ela realmente vive** (no vault injectado, se
  houver). O banner de arranque declara qual das duas posturas está composta.
- **Fail-closed:** um vault injectado que **falha** (ex.: custódia externa indisponível) **propaga o
  erro** pela cadeia de cifra/shred e **aborta** a escrita — **nunca** há fallback silencioso para o
  in-memory de referência. Um deployment que exige custódia externa não sela conteúdo sob uma chave
  volátil sem se aperceber.
- **Rotação.** A referência in-memory **não roda** (chave efémera por arranque). Sob custódia externa,
  a rotação é do custodiante: como a KEK é resolvida por `KeyRef` derivada do titular a cada operação,
  o custodiante pode versionar/rodar o material subjacente sem que o nó mude — desde que uma `KeyRef`
  já provisionada continue a resolver para a chave que cifrou o conteúdo existente.

> **Custódia HSM *key-never-leaves* — porta de envelope `WrapDEK`/`UnwrapDEK` (AOS-216, fecha o residual
> de `DEF-302`).** A porta `audit.KeyVault` devolve a **KEK crua** (`Key(keyRef) → []byte`) e o embrulho da
> DEK corre **in-process** (`audit.sealPayload`) — isto serve directamente um **key-service / software-KMS
> que devolve chaves** (custódia externa, o que `DEF-302` fechava). Um **HSM verdadeiro** (a chave **nunca**
> sai do módulo) **não** devolve a chave crua: para o servir, o nó expõe a porta de **envelope**
> `audit.KeyWrapper` — `WrapDEK(subjectID, dek) → (wrapped, keyRef)` e `UnwrapDEK(keyRef, wrapped) → (dek, ok)`,
> com o embrulho/desembrulho a correr **dentro** do módulo de custódia. Um vault injectado por
> `Config.DSARVault` que implemente **também** `KeyWrapper` faz `audit.SealContent`/`OpenContent` tomarem a
> via de envelope **por type assertion**: a **DEK** (efémera, por-registo) é o único material que atravessa
> a fronteira; a **KEK crua NUNCA entra no processo do nó** (nem no seal, nem no open, nem em log/span/erro).
> Um vault que só implemente `KeyVault` mantém a via KEK-crua de AOS-093/215 (**fallback**, serialização
> **byte-a-byte** idêntica — o formato de envelope é versionado retro-compativelmente por um campo `key_ref`
> presente só nesse caminho). O crypto-shredding manifesta-se na via de envelope tal-qual: `Delete` destrói a
> KEK dentro do módulo ⇒ `UnwrapDEK` falha ⇒ o conteúdo é irrecuperável, sem mutar o log (a hash-chain
> continua a validar). A impl de referência `audit.InMemoryKeyWrapper` (stdlib AES-256-GCM, in-process) prova
> o **contrato** e o **seam**; o **HSM concreto** (PKCS#11, AWS KMS `Encrypt`/`Decrypt`, HashiCorp Vault
> Transit) é **infra-org**, vive **fora** do binário zero-dep — análogo à custódia da chave do issuer
> (AOS-175) e ao tenant de soberania (DEF-201/212).

### Estado durável — variáveis de ambiente (AOS-170 / AOS-180)

| Variável | Default | Efeito |
|---|---|---|
| `AOS_EVENTSTORE_PATH` | *(vazio)* | **Vazio ⇒ Event Store IN-MEMORY** (volátil: perde tudo quando o processo/contentor morre). Definido ⇒ Event Store **durável** (WAL append-only + `fsync` + replay crash-safe no arranque) no caminho dado. **Tem de apontar para DENTRO do mount gravável** (`-v aos-data:/var/lib/aos`, ex.: `/var/lib/aos/events.wal`). |
| `AOS_WORM_PATH` | *(vazio)* | Vazio ⇒ WORM in-memory. Definido ⇒ trilho WORM **hash-chain tamper-evident** em disco. Mesmo requisito de mount (ex.: `/var/lib/aos/worm.wal`). |
| `AOS_DURABLE_EXECUTION` | *(vazio ⇒ **DESLIGADA**)* | Ligam: `1` `true` `t` `yes` `y` `on`. Desligam: `0` `false` `f` `no` `n` `off` (ou ausente/vazia). **Qualquer outro valor ABORTA o arranque** (`enabled`, `tru`, `sim`, … **não** são tratados como `false` — ver abaixo). Ligada ⇒ o nó compõe **checkpointer + capturer de não-determinismo + step-ledger** sobre o Event Store; o tool set congelado (AOS-155) passa a persistir no mesmo store. Desligada ⇒ os três ficam `nil` e o runtime usa os defaults no-op (AOS-013). |

**Interacção `AOS_DURABLE_EXECUTION` × `AOS_EVENTSTORE_PATH` — fail-closed SEMPRE.**
`AOS_DURABLE_EXECUTION=1` **sem** `AOS_EVENTSTORE_PATH` **RECUSA o arranque** (`exit 1`), em
**qualquer** modo — não só em `AOS_MODE=production`. Razão: a execução durável compõe-se
**sobre** o Event Store; sobre um store in-memory os checkpoints, as capturas e o step-ledger
**evaporariam no reinício** e o nó anunciaria uma durabilidade que não cumpre. A ambiguidade
**nega** o arranque em vez de degradar em silêncio — a mesma postura de `AOS_BOARD_REGIONS`
malformado. Um valor não-booleano da própria variável aborta pela mesma razão: quem escreveu
`AOS_DURABLE_EXECUTION=enabled` **tenciona** ligar a durabilidade, e receber um nó silenciosamente
não-durável seria pior do que não arrancar.

> **O que a guarda NÃO consegue detectar.** Ela só vê a *ausência* de caminho. Um
> `AOS_EVENTSTORE_PATH=/tmp/events.wal` — ou qualquer caminho **fora** de `-v aos-data:/var/lib/aos`,
> incluindo o `--tmpfs /tmp` das receitas acima — **passa** a guarda e continua a perder tudo
> quando o contentor é substituído. Apontar `AOS_EVENTSTORE_PATH` e `AOS_WORM_PATH` para dentro do
> volume nomeado é **responsabilidade do operador**; é por isso que está documentado aqui e não só
> imposto em código.

**Verificação pelo operador** — o banner de arranque declara o estado **realmente composto** (não a
intenção da config); uma destas duas linhas sai sempre:

```text
[aos] execucao duravel (AOS-180): LIGADA — checkpointer + capturer + step-ledger COMPOSTOS sobre o event store (duravel em disco (AOS-170)); o tool set congelado (AOS-155) persiste no mesmo store
[aos] execucao duravel (AOS-180): DESLIGADA — checkpointer/capturer/step-ledger NAO compostos (defaults no-op AOS-013); defina AOS_DURABLE_EXECUTION=1 (exige AOS_EVENTSTORE_PATH) para ligar
```

A linha `[aos] substrato: ...` imediatamente antes diz `duravel em disco (AOS-170)` ou
`in-memory de referencia (nao-duravel)` — confirme-a **antes** de assumir que o estado sobrevive
a um reinício.

#### Postura de produção de `AOS_DURABLE_EXECUTION` — decisão (AOS-203)

**Decisão: mantém-se OPT-IN, também em `AOS_MODE=production`.** Um nó de produção **arranca sem
execução durável**. A assimetria face às outras duas posturas de produção
(`ErrProductionNeedsHardenedIdentity`, `ErrProductionNeedsSovereignRead`) é **deliberada e
registada aqui**, não tácita — AOS-191 deixou-a em aberto com eixo neste ticket, e é este o
registo que a fecha.

**Critério que separa os três casos: a promessa falsa.**

| Postura | Estado desligado em produção | Porquê |
|---|---|---|
| `AOS_ISSUER_PUBKEY` | **RECUSA** | O nó **serviria** identidade com a autoridade co-localizada — uma postura mais fraca do que a que um nó de produção implicitamente anuncia. |
| `AOS_BOARD_REGIONS` | **RECUSA** | O nó **serviria** leituras sem authz por-chamador nem selo — o mesmo tipo de promessa falsa, sobre um controlo de conformidade. |
| `AOS_DURABLE_EXECUTION` | **permite** (declara `DESLIGADA`) | O nó **não anuncia** durabilidade nenhuma. O banner diz `execucao duravel (AOS-180): DESLIGADA` em cada arranque, e nenhum endpoint promete sobrevivência de *checkpoints*. Não há capacidade anunciada e não cumprida — há uma capacidade **declaradamente ausente**. |

Os dois argumentos secundários, subordinados ao critério acima: (i) exigi-la converteria um
ticket de **superfície de configuração** numa mudança de postura de produção não anunciada aos
operadores existentes — a retro-compatibilidade que AOS-191 impôs; (ii) o eixo perigoso — ligar a
durabilidade sobre um substrato volátil — **já é fail-closed em qualquer modo**
(`AOS_DURABLE_EXECUTION=1` sem `AOS_EVENTSTORE_PATH` aborta), que é onde a promessa falsa
realmente estaria.

> **Consequência para o operador, dita sem rodeios:** se quer que um *run* interrompido retome
> onde ia — em vez de recomeçar — **tem de a ligar explicitamente**, mesmo em produção. Ligue
> `AOS_DURABLE_EXECUTION=1` com `AOS_EVENTSTORE_PATH` dentro do volume gravável, e confirme a
> linha `LIGADA` no banner. Nada no nó a liga por si.

### Terminação TLS do ingresso — API, SSE, DSAR + perna OTLP (AOS-209)

O nó serve API HTTP, o SSE de trajectória e o `POST /dsar/erase`. Sem TLS, **qualquer
intermediário na rota lê o transporte**: a assinatura ed25519 do canal de controlo continua
íntegra, mas o **conteúdo** transportado (trajectória, desfechos, corpos de sinais) fica
observável — o achado §5.2-b de `tecnica/17`. `crypto/tls` é **stdlib**: terminar TLS no nó
**não** puxa dependências nem colide com o ADR-017. Há **duas** posturas legítimas, e o que não
se tolera é a terceira (texto-claro por omissão, sem o operador o saber).

**Duas formas de cifrar o transporte — escolha uma, explicitamente:**

| Postura | Variáveis | Efeito |
|---|---|---|
| **TLS no nó** | `AOS_TLS_CERT_PATH` + `AOS_TLS_KEY_PATH` (ambos) | O nó carrega o par e serve TLS **endurecido** (MinVersion **TLS 1.2**; cipher suites **AEAD sobre ECDHE**). |
| **Terminação a montante** (opt-out) | `AOS_TLS_EXTERNAL_TERMINATION=1` | O nó serve em **claro por decisão sua**; a cifra fica a cargo do ingress/malha. Banner **ruidoso** em cada arranque. |
| **Texto-claro sem opt-out** | *(nenhuma)* | Loopback: permitido. **Não-loopback: RECUSADO** (`ErrRefuseCleartextBind`). Em produção: **arranque RECUSA** (`ErrProductionNeedsTLS`). |

**Certificados e chave.** O certificado é material **público** (`AOS_TLS_CERT_PATH`). A **chave
privada** entra **só por ficheiro montado** (`AOS_TLS_KEY_PATH`) — **nunca** por variável de
ambiente, no mesmo padrão de `AOS_ISSUER_KEY_PATH`. Monte-os read-only e fora da imagem:

```bash
docker run --read-only --tmpfs /tmp -p 8443:8443 \
  -v $PWD/tls/server.crt:/etc/aos/tls/server.crt:ro \
  -v $PWD/tls/server.key:/etc/aos/tls/server.key:ro \
  -e AOS_API_ADDR=0.0.0.0:8443 \
  -e AOS_TLS_CERT_PATH=/etc/aos/tls/server.crt \
  -e AOS_TLS_KEY_PATH=/etc/aos/tls/server.key \
  -e AOS_OPERATORS="ops:alice=<hex-32B-ed25519>" \
  aos-node:local
```

**Rotação.** Todas as variáveis são lidas **uma vez, no arranque** — não há *reload* a quente.
Para rodar o certificado/chave, **substitua os ficheiros montados e reinicie o contentor** (a
substituição de contentor é o modelo de rotação de todo o material do nó). Um par que não carrega
(ficheiro ilegível, PEM malformado, chave que não corresponde ao certificado) ⇒ **ABORTA**
(`ErrBadTLSKeyPair`); definir **só um** dos dois caminhos ⇒ **ABORTA** (`ErrIncompleteTLSConfig`).

**Opt-out — o que o banner declara.** Com `AOS_TLS_EXTERNAL_TERMINATION` declarado (e sem TLS no
nó), o arranque emite:

```text
[aos] AVISO TLS (AOS-209): TERMINACAO A MONTANTE DECLARADA (AOS_TLS_EXTERNAL_TERMINATION) — o no serve API/SSE/DSAR em TEXTO-CLARO por DECISAO de quem o configurou
[aos] => RESPONSABILIDADE ASSUMIDA: a cifra do transporte passa a depender do ingress/malha de servico a montante; se essa camada nao cifrar, o transporte fica legivel por qualquer intermediario na rota
[aos] => O bind NAO-loopback em claro deixa de ser recusado (a quarta conjuncao do bind-guardrail da-se por satisfeita); a assinatura ed25519 do canal de controlo continua integra, mas o CONTEUDO transportado e observavel se a montante nao cifrar
[aos] => PARA TERMINAR TLS NO PROPRIO NO: remova AOS_TLS_EXTERNAL_TERMINATION e defina AOS_TLS_CERT_PATH + AOS_TLS_KEY_PATH (chave privada por ficheiro montado, NUNCA por variavel de ambiente)
```

**Postura por `AOS_MODE`.** Em `AOS_MODE=production`, servir sem TLS **nem** opt-out declarado
**recusa o arranque** (`ErrProductionNeedsTLS`, `exit 1`) — a par de `ErrProductionNeedsHardenedIdentity`
e `ErrProductionNeedsSovereignRead`. Fora de produção, o texto-claro é permitido em **loopback**;
o bind **não-loopback** em claro é **sempre** recusado pela [quarta conjunção do
bind-guardrail](#bind-guardrail-fail-closed).

**Códigos de recusa (todos `exit 1`, fail-closed):**

| Código | Quando |
|---|---|
| `ErrRefuseCleartextBind` | Bind **não-loopback** em texto-claro sem TLS nem opt-out (fora de produção também). |
| `ErrProductionNeedsTLS` | `AOS_MODE=production` sem TLS nem opt-out. |
| `ErrBadTLSKeyPair` | Par certificado+chave ilegível / PEM malformado / chave que não corresponde ao certificado. |
| `ErrIncompleteTLSConfig` | Só um de `AOS_TLS_CERT_PATH`/`AOS_TLS_KEY_PATH` definido. |
| `ErrBadTLSExternalTermination` | `AOS_TLS_EXTERNAL_TERMINATION` com valor não-booleano. |

**Perna OTLP (AOS-173/AOS-209).** Um `AOS_OTLP_ENDPOINT` **`https://`** faz o exporter negociar
TLS **1.2+** contra o colector e validar o certificado dele contra as raízes do sistema. O
**fail-open** de AOS-173 mantém-se **intacto**: uma falha de handshake TLS (colector em baixo,
certificado inválido) é contabilizada e **nunca** quebra um run — cifrar o transporte não
introduz um novo caminho crítico. A **autenticação forte** perante o colector (mTLS de cliente
ou bearer token) é **entregue OPT-IN** por DEF-012 — ver a secção seguinte.

### mTLS do plano de controlo + autenticação forte OTLP (DEF-012)

A terminação TLS de AOS-209 cifra e autentica o **servidor** perante o cliente. DEF-012
acrescenta, **OPT-IN e por FICHEIRO montado**, a autenticação **mútua** de transporte em dois
eixos independentes. Nenhum é a primeira barreira: o plano de controlo já é autenticado na camada
de **aplicação** por assinatura ed25519 no corpo (non-signing, AOS-160), independente do
transporte — o mTLS é uma **segunda** barreira, **ADITIVA, nunca um bypass**.

#### EIXO 1 — mTLS do plano de controlo

`AOS_CONTROL_MTLS_CA_PATH` monta o **bundle PEM da CA de cliente**. Com ele definido, o listener
negoceia `tls.VerifyClientCertIfGiven` (verifica o certificado de cliente contra a CA **se**
apresentado) e as rotas `/steer`, `/pause`, `/approve` **RECUSAM** (`403`) um pedido sem um
certificado de cliente **verificado** — e, a seguir, a assinatura ed25519 do corpo continua a ser
exigida por `node.Steer`/`FourEyes`.

| Propriedade | Comportamento |
|---|---|
| **Escopo** | **ESCOPADO** ao plano de controlo. `/healthz`, `/readyz`, `GET`/`POST /runs` e `/trajectory` **não** exigem certificado de cliente (sondas de orquestrador não assinam; o plano de dados é não-autenticado por ADR-016). Não é `RequireAndVerifyClientCert` no listener, que imporia o certificado a essas rotas. |
| **Aditivo** | Um certificado de cliente **válido** com assinatura ed25519 **ausente/má** continua **RECUSADO**. O mTLS **nunca** substitui a assinatura. |
| **Pré-requisito** | Exige **terminação TLS no nó** (`AOS_TLS_CERT_PATH`+`AOS_TLS_KEY_PATH`) — a autenticação mútua é do handshake. Sem TLS no nó ⇒ **ABORTA** (`ErrControlMTLSNeedsNodeTLS`). |
| **Recusa** | Bundle ilegível/sem CA PEM válida ⇒ **ABORTA** (`ErrBadControlMTLSCA`). |

O banner declara o estado real: `mTLS do plano de controlo (DEF-012): LIGADO … ADITIVO, NAO BYPASS … ESCOPADO ao plano de controlo`.

#### EIXO 2 — autenticação forte da perna OTLP

O exporter autentica-se perante o colector, **preservando o fail-open de AOS-173** (uma recusa do
colector em tempo de run é contabilizada como `Failed` e **nunca** quebra um run). Duas formas,
combináveis:

| Variáveis | Efeito |
|---|---|
| `AOS_OTLP_CLIENT_CERT_PATH` + `AOS_OTLP_CLIENT_KEY_PATH` | **mTLS de cliente**: o par é apresentado ao colector no handshake. Só um ⇒ `ErrIncompleteOTLPClientTLS`; par inválido ⇒ `ErrBadOTLPClientCert`. |
| `AOS_OTLP_BEARER_TOKEN_PATH` | **Bearer**: cada POST leva `Authorization: Bearer <token>`. Ficheiro ilegível/vazio ⇒ `ErrBadOTLPBearerToken`. |

O banner declara `autenticacao OTLP (DEF-012): mTLS de cliente + bearer LIGADOS` / `… LIGADO` /
`… DESLIGADA` conforme composto — **sem** ecoar o token.

#### Material privado, rotação e códigos de recusa

Todo o material privado (chave de cliente OTLP, **bearer**) entra **só por ficheiro montado** —
**nunca** por variável de ambiente, nunca em código/fixtures. A **CA de cliente** do EIXO 1 é
material **público**. Como todo o material do nó, é lido **uma vez no arranque**: para rodar,
**substitua os ficheiros montados e reinicie o contentor**.

```bash
docker run --read-only --tmpfs /tmp -p 8443:8443 \
  -v $PWD/tls/server.crt:/etc/aos/tls/server.crt:ro \
  -v $PWD/tls/server.key:/etc/aos/tls/server.key:ro \
  -v $PWD/tls/control-client-ca.crt:/etc/aos/tls/control-client-ca.crt:ro \
  -v $PWD/otlp/client.crt:/etc/aos/otlp/client.crt:ro \
  -v $PWD/otlp/client.key:/etc/aos/otlp/client.key:ro \
  -v $PWD/otlp/bearer.token:/etc/aos/otlp/bearer.token:ro \
  -e AOS_API_ADDR=0.0.0.0:8443 \
  -e AOS_TLS_CERT_PATH=/etc/aos/tls/server.crt \
  -e AOS_TLS_KEY_PATH=/etc/aos/tls/server.key \
  -e AOS_CONTROL_MTLS_CA_PATH=/etc/aos/tls/control-client-ca.crt \
  -e AOS_OTLP_ENDPOINT=https://collector:4318 \
  -e AOS_OTLP_CLIENT_CERT_PATH=/etc/aos/otlp/client.crt \
  -e AOS_OTLP_CLIENT_KEY_PATH=/etc/aos/otlp/client.key \
  -e AOS_OTLP_BEARER_TOKEN_PATH=/etc/aos/otlp/bearer.token \
  -e AOS_OPERATORS="ops:alice=<hex-32B-ed25519>" \
  aos-node:local
```

**Códigos de recusa (todos `exit 1`/`403`, fail-closed):**

| Código | Quando |
|---|---|
| `ErrControlMTLSNeedsNodeTLS` | `AOS_CONTROL_MTLS_CA_PATH` sem terminação TLS no nó. |
| `ErrBadControlMTLSCA` | Bundle de CA de cliente ilegível ou sem certificado PEM válido. |
| `403` nas rotas de controlo | mTLS de controlo ligado e pedido sem certificado de cliente verificado (a assinatura ed25519 continua a ser exigida a seguir). |
| `ErrIncompleteOTLPClientTLS` | Só um de `AOS_OTLP_CLIENT_CERT_PATH`/`AOS_OTLP_CLIENT_KEY_PATH`. |
| `ErrBadOTLPClientCert` | Par mTLS de cliente OTLP ilegível/PEM malformado/chave≠certificado. |
| `ErrBadOTLPBearerToken` | Ficheiro de bearer OTLP ilegível ou vazio (o erro nomeia só o caminho, nunca o conteúdo). |

> **Eixo em `docs/governance/REGISTO-Deferimentos.md` (DEF-012, nota N-DEF-012):** o mecanismo é
> entregue e fail-closed; o que fica **deferido** é a **provisão de infra** (PKI/emissão de
> certificados de cliente aos operadores, bearer/mTLS do lado do colector), não código do nó.

### Plano de controlo — operadores e aprovadores (AOS-160 / AOS-162, config em AOS-193)

O canal de controlo (`POST /runs/{id}/steer`, `/pause`) e o *four-eyes* (`/approve`) são
**default-deny**: sem configuração, **nenhum** sinal autentica e `/approve` responde `501`. Estas
duas variáveis são o **único** caminho para os ligar no binário entregue.

| Variável | Default | Efeito |
|---|---|---|
| `AOS_OPERATORS` | *(vazio ⇒ **default-deny**)* | Registo `emitterID=hexpubkey,emitterID2=hexpubkey2` das **pubkeys** ed25519 dos operadores autorizados a emitir `steer`/`pause`. `hexpubkey` = **64 hex chars = 32 bytes**, a mesma codificação de `AOS_ISSUER_PUBKEY`. Vazio ⇒ o canal fica composto mas **inoperável** (todo o sinal leva `403`) **e o bind não-loopback é RECUSADO** (ver abaixo). |
| `AOS_APPROVERS_FILE` | *(vazio ⇒ **four-eyes DESLIGADO**)* | Caminho de um **ficheiro JSON montado** com a *roster* de aprovadores do dual-control. Vazio ⇒ o `FourEyesGate` não é composto e `POST /runs/{id}/approve` responde `501` (desligado **por declaração**, não por avaria). |
| `AOS_RATIFIERS` | *(vazio ⇒ **toda a promoção NEGADA**)* | Registo `principal=hexpubkey,principal2=hexpubkey2` das **pubkeys** ed25519 dos ratificadores de produção do *promotion controller* (AOS-159/AOS-206). `hexpubkey` = **64 hex chars = 32 bytes**, a mesma codificação de `AOS_OPERATORS`. A autoridade é **fixa** (`ratify:production`), pelo que — ao contrário dos aprovadores — é uma *env* plana e não um ficheiro. **Ao contrário do 4-eyes, NÃO gateia a composição:** o controller é composto **sempre** pela via sancionada `hitl.NewProductionRatificationGate` (freshness + nonce-store **durável** forçados; uma ratificação re-submetida após consumo ⇒ `ratification_replayed`). Vazio ⇒ composto mas **sem ratificadores** ⇒ nenhum artefacto de auto-modificação (skill/memória procedural) pode ser promovido (`ratifier_unknown`) — o banner declara-o. |

**Fail-closed, sem degradação silenciosa** (a postura de `AOS_BOARD_REGIONS`/`AOS_DURABLE_EXECUTION`):
uma entrada sem `=`, um `emitterID` vazio, uma pubkey que não seja hex de 32 bytes, ou um
`emitterID` **duplicado** ⇒ o arranque **ABORTA** (`exit 1`). Não se "registam os que der": um
operador silenciosamente descartado daria um nó que arranca a anunciar um canal de controlo e
depois recusa **todos** os sinais desse operador com `403`. O duplicado aborta em vez de "o último
ganha" — dois valores para o mesmo `emitterID` são um conflito de autoridade, não uma preferência.
O ficheiro de aprovadores segue a mesma regra: ilegível, JSON inválido, **campo desconhecido**
(esquema em *drift*), lista vazia, principal duplicado ou autoridade vazia ⇒ **ABORTA**.
`AOS_RATIFIERS` segue a gramática e o regime de `AOS_OPERATORS`: entrada sem `=`, `principal`
vazio, pubkey não-hex-de-32-bytes, ou `principal` **duplicado** ⇒ **ABORTA** (`ErrBadRatifiers`) —
um ratificador silenciosamente descartado daria um nó a contar "N ratificador(es)" no banner que
recusaria com `ratifier_unknown` toda a ratificação desse principal.

Duas regras adicionais, que valem **nos dois** caminhos (env e ficheiro) e também para quem compõe
`Config` in-process — o `Bootstrap` impõe-as antes de compor seja o que for:

- **Material de chave não se partilha.** Duas entradas com a **mesma pubkey** ⇒ **ABORTA**, mesmo
  com identificadores diferentes. Nos *aprovadores* isto é **segurança**: a distinção do
  dual-control compara `approver`/`session`/`credential` — três *strings* que o **cliente** escolhe
  na perna —, pelo que a pubkey **pinada** é a única âncora criptográfica de "duas pessoas"; colada
  em duas linhas, **uma** chave privada assina as **duas** pernas e o 4-eyes é anulado em silêncio,
  com o banner a declarar "2 aprovador(es) pinados". Nos *operadores* é **atribuição**: um
  `aos steer --emitter ops:bob` assinado pela chave de `ops:alice` seria aceite e **selado no WORM**
  como sendo de `ops:bob` — o nome do emissor deixaria de ser evidência.
- **`authority` tem vocabulário fechado:** `approve:safe`, `approve:gray`, `approve:danger` (é o que
  `hitl.RequiredAuthority` produz, uma por classe de risco). Qualquer outro valor —
  `approve:dangerous`, `approve:*`, `approver:danger` — ⇒ **ABORTA**. A comparação em runtime é de
  *string* **exacta, sem wildcards**: um *typo* seria *fail-closed* mas **silencioso** — um aprovador
  contado no banner que nunca aprova nada.

> **Só entra material PÚBLICO.** A chave **privada** do operador vive na máquina do operador — é lá
> que `aos steer`/`aos pause` assinam (`--key <ficheiro-da-seed>`); o nó só detém pubkeys e por isso
> **verifica mas não forja**. **Limite honesto:** uma *seed* ed25519 tem também 32 bytes, pelo que o
> nó **não a distingue** estruturalmente de uma pubkey. Colar a seed em `AOS_OPERATORS` produz um
> registo que nunca valida assinatura nenhuma (fail-closed, sem elevação de privilégio) — mas terá
> exposto a chave privada ao ambiente do nó. **Derive sempre a pubkey**, não copie a seed.

Derivar a entrada a partir da seed do operador (só stdlib, sem ferramenta externa):

```bash
aos operator-pubkey --key ./operator.seed --emitter ops:alice
# ops:alice=1f8b…  (64 hex chars)  ← o valor a pôr em AOS_OPERATORS
```

Formato do ficheiro de `AOS_APPROVERS_FILE` (monte-o read-only, ex.: `-v $PWD/approvers.json:/etc/aos/approvers.json:ro`):

```json
{
  "approvers": [
    {"principal": "human:alice", "pubkey": "<64 hex>", "authority": ["approve:danger", "approve:gray"]},
    {"principal": "human:bob",   "pubkey": "<64 hex-DIFERENTE>", "authority": ["approve:danger"]}
  ]
}
```

> **Wire de `POST /runs/{id}/approve`:** `risk_class` é o valor **numérico** de `risk.Class` —
> **`0` = `danger`** (é o **valor-zero**, escolhido para que uma classe não computada seja tratada
> como o pior caso), `1` = `safe`, `2` = `gray`. A capability exigida ao aprovador é
> `approve:<classe>` da classe **do pedido**; um aprovador só com `approve:gray` **não** autoriza um
> pedido `risk_class: 0`.

> **Porquê ficheiro aqui e env ali?** `AOS_OPERATORS` é um mapa `id→escalar` e cabe sem perda na
> gramática plana que o nó já usa (`AOS_BOARD_REGIONS`). Um aprovador **não** é escalar — traz
> `authority[]` —, e espremê-lo numa env exigiria um terceiro nível de delimitador, ilegível e
> irrevisível. Um ficheiro montado é a via que o **ADR-017 ponto 2** já prevê ("config por
> env/ficheiro montado") e é versionável em *code-review*, que é o que uma *roster* de dual-control
> deve ser. Cada colaborador tem **um** caminho de configuração: não há precedência env-vs-ficheiro
> a divergir em silêncio. A codificação do material público (hex de 32 bytes) e a disciplina
> fail-closed são **as mesmas** nos dois.

**Verificação pelo operador** — o banner declara o estado **realmente composto**:

```text
[aos] canal de controlo: Ed25519Authenticator (AOS-160) — 2 operador(es) registado(s) via AOS_OPERATORS; HMACAuthenticator demo DESLIGADO
[aos] canal de controlo: Ed25519Authenticator (AOS-160) composto mas SEM OPERADORES — steer/pause serao TODOS recusados (ErrUnknownEmitter) e o bind NAO-loopback e RECUSADO; defina AOS_OPERATORS="emitterID=hexpubkey" para o tornar operavel
[aos] four-eyes gate (AOS-162) composto: 2 aprovador(es) pinado(s) via AOS_APPROVERS_FILE
[aos] four-eyes gate (AOS-162): DESLIGADO (sem aprovadores) — POST /runs/{id}/approve responde 501; defina AOS_APPROVERS_FILE=<ficheiro JSON montado> para o compor
```

### Bind-guardrail (fail-closed)

A API **recusa** bind a um endereço **não-loopback** (`0.0.0.0`, `:8080`, um IP público, ou um
*hostname* não confirmável como loopback) enquanto **duas** condições sobre o mesmo eixo não
estiverem satisfeitas. A primeira é o **canal de sinais (`steer`/`pause`)** **autenticado E
operável** — a **conjunção** de três coisas:

1. o autenticador ed25519 do canal de controlo está composto (`SteerAuth != nil`);
2. o modo de identidade é real (`real` ou `real-trust-anchor-only`);
3. **existe pelo menos um operador com pubkey registada** (`AOS_OPERATORS` não-vazia).

Falhar qualquer uma ⇒ `ErrRefuseNonLoopbackBind` **antes** do `Listen` (o socket nem chega a
abrir); o *loopback* continua sempre permitido. O log da recusa nomeia o modo de identidade e a
**cardinalidade de operadores**, que é a causa esmagadoramente mais provável.

**Quarta conjunção — transporte cifrado (AOS-209).** Mesmo com o canal de controlo operável, um
bind não-loopback em **texto-claro** ⇒ `ErrRefuseCleartextBind` **antes** do `Listen`. É a
**mesma disciplina, um segundo eixo**: satisfaz-se com TLS no nó (`AOS_TLS_CERT_PATH`+`AOS_TLS_KEY_PATH`)
**ou** com a declaração de terminação a montante (`AOS_TLS_EXTERNAL_TERMINATION=1`) — ver
[Terminação TLS](#terminação-tls-do-ingresso--api-sse-dsar--perna-otlp-aos-209). O *loopback*
continua sempre permitido (sem intermediário na rota).

> **Âmbito exacto (não é omissão):** o predicado **não** olha para os aprovadores. Um nó
> configurado **só** com `AOS_APPROVERS_FILE` tem o `/approve` plenamente operável e é, ainda
> assim, recusado no bind não-loopback — `AOS_OPERATORS` é obrigatória. A escolha é a
> conservadora: `steer`/`pause` são a superfície de **intervenção** (parar um *run* a correr), a
> que justifica expor a porta à rede. Por isso a mensagem do erro diz **"steer/pause
> INOPERAVEIS"** e não "canal de controlo não operável" — o texto nomeia o que a condição
> verifica, nem mais nem menos.

> ⚠️ **MUDANÇA DE COMPORTAMENTO (AOS-193) — leia se já faz bind não-loopback.** Até AOS-193 o
> guardrail exigia só (1)∧(2) — duas condições que o `Bootstrap` satisfaz **sempre**, pelo que a
> condição era **identicamente verdadeira** e nunca recusava nada. Um nó que hoje sobe em
> `0.0.0.0` **sem** `AOS_OPERATORS` **passa a RECUSAR arrancar** (`exit 1`). A correcção é
> acrescentar `-e AOS_OPERATORS="<id>=<hexpubkey>"`. É deliberado: expor à rede um plano de
> controlo que não consegue aceitar **um único** sinal legítimo dá toda a superfície de ataque e
> nenhum benefício. Quem precise de manter o comportamento anterior sem operadores tem uma opção
> honesta — fazer bind ao **loopback** e publicar a porta pelo orquestrador.

Para servir tráfego externo endurecido:

```bash
docker run --read-only --tmpfs /tmp -v aos-data:/var/lib/aos -p 8443:8443 \
  -v $PWD/tls/server.crt:/etc/aos/tls/server.crt:ro \
  -v $PWD/tls/server.key:/etc/aos/tls/server.key:ro \
  -e AOS_MODE=production \
  -e AOS_API_ADDR=0.0.0.0:8443 \
  -e AOS_TLS_CERT_PATH=/etc/aos/tls/server.crt   `# certificado (publico)` \
  -e AOS_TLS_KEY_PATH=/etc/aos/tls/server.key    `# CHAVE PRIVADA por ficheiro montado read-only` \
  -e AOS_ISSUER_PUBKEY=<hex-32B-ed25519>   `# trust-anchor-only; a CHAVE PRIVADA fica no vault` \
  -e AOS_OPERATORS="ops:alice=<hex-32B-ed25519>"   `# PUBKEY do operador; a privada fica na maquina dele` \
  -e AOS_BOARD_REGIONS="board:prod=eu" \
  -e AOS_EVENTSTORE_PATH=/var/lib/aos/events.wal \
  -e AOS_WORM_PATH=/var/lib/aos/worm.wal \
  -e AOS_DURABLE_EXECUTION=1 \
  aos-node:local
```

> Quem termina TLS a montante (ingress/malha) troca as duas linhas de TLS acima por
> `-e AOS_TLS_EXTERNAL_TERMINATION=1` — o nó serve em claro **por decisão declarada** e o banner
> avisa-o em cada arranque. Em `AOS_MODE=production`, **uma** das duas posturas é obrigatória:
> sem nenhuma, o arranque recusa (`ErrProductionNeedsTLS`).

Prova executável desta secção: [`aos193-control-plane-harness.sh`](aos193-control-plane-harness.sh)
— arranca o **contentor real** em `0.0.0.0` sem operadores (recusa), depois com um operador
(arranca) e envia um `aos steer` assinado (aceite); um emissor não registado leva `403`. O mesmo
contentor monta um `approvers.json` read-only (`-v … -e AOS_APPROVERS_FILE=…`): o banner declara o
*four-eyes* **composto** e `/approve` passa a **julgar** (`403` sem pernas válidas, já não `501`);
e um *roster* com a **mesma pubkey em dois principals** faz o nó **recusar arrancar**. A prova
positiva do `200` (duas pernas assinadas por **duas** chaves distintas ⇒ `authorized`) é
in-process, em `TestApproversFileAuthorizesDualControlEndToEnd` — assinar como aprovador é papel
do **dispositivo do humano**, que a CLI do nó deliberadamente não desempenha.

Os três últimos ligam o **estado durável** e a **execução durável** nos caminhos do volume
`aos-data` — ver [Estado durável](#estado-durável--variáveis-de-ambiente-aos-170--aos-180) para a
semântica e o fail-closed. **A execução durável é opt-in mesmo em `AOS_MODE=production`**: o nó
arranca sem ela (declarando `DESLIGADA` no banner, sem anunciar durabilidade nenhuma). A promoção
a exigência de produção, a par de `AOS_ISSUER_PUBKEY`/`AOS_BOARD_REGIONS`, foi **decidida em
AOS-203 — mantém-se opt-in**, com o critério registado em
[Postura de produção de `AOS_DURABLE_EXECUTION`](#postura-de-produção-de-aos_durable_execution--decisão-aos-203).

O `HEALTHCHECK` deriva a porta de `AOS_API_ADDR` (aqui, `8080`) e sonda `127.0.0.1:8080/healthz`
no loopback do contentor — não é preciso definir `AOS_HEALTH_URL`.

**Sem segredos na imagem**: `AOS_ISSUER_PUBKEY` é material **público** (trust anchor). A chave de
assinatura do issuer (AOS-156) é um trust-domain separado (ADR-017 ponto 5) — vem do vault/KeyVault
em runtime (ADR-006), nunca da imagem.

## Exercitar o caminho REAL do nó localmente — o *dev-harness*

Antes de provisionar a infra de produção (IdP OIDC real, provider de modelo, KMS/HSM), o
**dev-harness** exercita o **caminho real** do nó `aos` ponta-a-ponta com colaboradores **locais** —
a mesma composição `NewProductionSecure` que a imagem corre, **não** os *stubs* neutros do `aos-demo`.
É um teste **runnable**, sem Docker nem rede:

```bash
go test -run TestDevHarness -v -race ./packages/cmd/aos
```

Cada cenário conduz o nó pela **API HTTP real** (`NewAPIHandler`) e narra o desfecho. O que cada um
prova é o **mesmo** comportamento que a config de deployment correspondente activa:

| Cenário (`go test -run …`) | Prova, no caminho real | Config de deployment análoga |
|---|---|---|
| `TestDevHarness_RealNode_MediatedExecution` | a política **Cedar assinada** PERMITE `cap:fs.read` (a tool executa) e NEGA `cap:payments.charge` fail-closed | `AOS_POLICY_BUNDLE_DIR` + `AOS_POLICY_TRUST_ANCHOR` |
| `TestDevHarness_CryptoShred_RightToErasure` | reconstruct autorizado decifra → `POST /dsar/erase` → `410 Gone`/`ErrDecrypt`, sem vazamento | `AOS_DURABLE_EXECUTION` + `AOS_EVENTSTORE_PATH`/`AOS_WORM_PATH` |
| `TestDevHarness_SovereignSubmit_TitularAndResidency` | `403` sem credencial; o titular é **derivado** do submissor (o `principal_nhi` do corpo é ignorado); a residência é **selada por-run** | `AOS_BOARD_REGIONS` (+ soberania) |
| `TestDevHarness_SteerReachesLoop` | uma correcção de operador **assinada** é consumida pelo loop e injectada `taint=trusted` no turno seguinte | `AOS_OPERATORS` |
| `TestDevHarness_RestartVerifiesWORM` | ao reiniciar, o nó **re-encadeia e verifica** a hash-chain do WORM; um WORM adulterado **ABORTA fail-closed** | `AOS_WORM_PATH` |

**O que é REAL** — a cadeia `identity→PDP→taint→scope→egress` com o *permit* não-forjável, o bundle
Cedar assinado committado (`packages/control-plane/pdp/policies`), o envelope DEK/KEK por-titular
(AOS-093), o gate soberano D6/D7, a execução durável sobre o Event Store/WORM em disco, o canal de
steer ed25519, e a API HTTP.

**O que é LOCAL/demo** — os dois eixos que ainda separam isto da produção, e **só** esses:

- **identidade**: a credencial é cunhada por uma autoridade **co-localizada** (ou apresentada por
  cabeçalho demo `X-Aos-Reader`/`X-Aos-Board`) em vez de um **IdP OIDC real** — o eixo **D4/EPIC-16**
  (ver [`AOS_ISSUER_PUBKEY`](#superfície-de-configuração--todas-as-variáveis-lidas-pelo-nó-aos-203) e
  [`AOS_SOVEREIGN_OIDC_ISSUER`](#credencial-forte-e-fonte-de-autoridade-da-soberania-de-leitura-aos-205));
- **modelo**: um modelo **determinista** de teste em vez de um provider LLM real — **EPIC-06**.

Substituir esses dois colaboradores locais pelos reais (config `AOS_ISSUER_PUBKEY`/OIDC + um provider
de modelo) é o que promove o harness a um *smoke-test* de deployment; o **código do nó** exercitado é
já o de produção. O harness vive em `packages/cmd/aos/devharness_test.go` e reutiliza o *scaffolding*
dos testes de aceitação (o bundle committado, os *helpers* de composição/HTTP) — não duplica mecanismo.

## Health / probes

- Container `HEALTHCHECK`: binário estático `aos-healthprobe` (distroless não tem shell/curl) →
  `GET /healthz` (liveness, AOS-171). O probe **deriva a porta de `AOS_API_ADDR`**
  (`127.0.0.1:<porta>/healthz`), pelo que segue automaticamente qualquer porta não-8080 — sem
  acoplamento silencioso a uma segunda variável. `AOS_HEALTH_URL` é um **override opcional**.
- **Kubernetes**: prefira sondas `httpGet` nativas — `livenessProbe` em `/healthz`,
  `readinessProbe` em `/readyz` (drain-aware).

## Entrega fail-closed (ADR-017 pontos 3 e 4)

```bash
bash scripts/ci/package.sh              # a cadeia completa (ver abaixo)
bash scripts/ci/sbom.sh                 # só SBOM + proveniência (deploy/node/build/)
bash scripts/ci/sign.sh                 # assina a atestação (precisa de AOS_RELEASE_KEY_FILE)
bash scripts/ci/verify-attestation.sh   # recusa a entrega que não valide
```

> Correr `sbom.sh` **isolado** regenera `provenance.json` e, com isso, invalida qualquer
> atestação anterior: o script **remove** o `attestation.dsse.json` e o `delivery-manifest.json`
> obsoletos e diz que o fez. A entrega fica *não-assinada* (honesta) em vez de ficar com um
> envelope que já não cobre os bytes no disco. Reassine com `sign.sh` (ou corra `package.sh`).

`package.sh` encadeia: `secrets` → `sast` → `sca` → `docker build` → `sbom` → **`sign`** →
**`verify-attestation`**. Reutiliza `sast.sh`/`sca.sh`/`secrets.sh` (baseline **multiset**, nunca
`sort -u`). Uma descoberta nova fora da baseline **avermelha**.

**Atestação assinada (AOS-207, fecha o ponto 3).** `sign.sh` emite um envelope **DSSE v1** com um
**in-toto Statement v1** assinado em **ed25519**, cujos *subjects* são o digest da imagem, o
binário, o SBOM, a proveniência e o manifesto de entrega. `verify-attestation.sh` verifica a
assinatura contra `release-pubkeys.json` e **recompara cada digest com o artefacto real** — mexer
no digest da imagem dentro de `delivery-manifest.json` põe o gate **vermelho**.

O que o `verify-attestation.sh` **NÃO** dá por verde (saída `4`, «por verificar», ⇒ `package.sh`
devolve `3` = **não publicável**):

- não há envelope (build sem `AOS_RELEASE_KEY_FILE`) — o caminho esperado num PR;
- o envelope é válido mas **não tem subject `image:`** (assinou-se sem imagem construída): a
  garantia central do ticket está ausente, e um verde aqui seria um falso-verde;
- a imagem atestada **não está presente** para o digest ser recomparado com a realidade — corra-o
  no host onde a imagem existe (`IMAGE_TAG=…`), senão a comparação com a imagem não acontece.

Se o manifesto **afirmar** cobertura da imagem (`attestation.imageBound=true`) que o statement
assinado não tem, isso não é ausência — é **vermelho**.

A chave privada de release **nunca** entra no repositório: entra por **caminho**
(`AOS_RELEASE_KEY_FILE`), como já acontece com `AOS_ISSUER_KEY_PATH`. Procedimento completo — quem
assina, onde vive, como se roda — em **[`CUSTODIA-CHAVE-RELEASE.md`](CUSTODIA-CHAVE-RELEASE.md)**.

Códigos de saída de `package.sh`: `0` verde · `1` vermelho · `2` configuração inválida ·
`3` **verde parcial** (nada falhou mas algo não correu — inclui a entrega **não-assinada**, a
**assinada-sem-imagem** e a imagem **não recomparada**; nenhuma delas é publicável). Um build
local/PR não tem a chave de release e devolve `3`: é o caminho esperado, declarado, não fingido.
O que ficou por verificar é redeclarado no fim (`AOS_SKIPPED_STEPS`) e ao lado do artefacto
(`deploy/node/build/SKIPPED.txt`) — incluindo os skips declarados **dentro** de `sbom.sh`,
`sign.sh` e `verify-attestation.sh`, que o `package.sh` reabsorve por ficheiro (`AOS_SKIP_SINK`).

## Repin dos digests

As bases estão **pinadas por digest** (não só tag). Para actualizar:

```bash
docker pull golang:1.24.5-bookworm && docker image inspect --format '{{index .RepoDigests 0}}' golang:1.24.5-bookworm
docker pull gcr.io/distroless/static-debian12:nonroot && docker image inspect --format '{{index .RepoDigests 0}}' gcr.io/distroless/static-debian12:nonroot
```

Actualizar os digests em `deploy/node/Dockerfile` **e** em `scripts/ci/sbom.sh` (proveniência).
