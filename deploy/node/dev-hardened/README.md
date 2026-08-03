# Stack de DEV endurecido do nó `aos`

Levanta o nó `aos` com **todos** os subsistemas que são configuráveis por ambiente/mount/tooling
do repositório — mais um **edge TLS** (ingresso) e um **collector OTLP**. É o contraponto ao
arranque mínimo (`docker run ... aos-node:local`), cujo banner declara quase tudo DESLIGADO.

```bash
bash deploy/node/dev-hardened/up.sh
```

Ao fim: nó `healthy`, `https://localhost:8443/healthz` a devolver `200` (TLS self-signed, use
`curl -k`).

Para subir também com **OIDC real (Keycloak) + `AOS_MODE=production`** (balde B):

```bash
bash deploy/node/dev-hardened/up-oidc.sh
```
```bash
bash deploy/node/dev-hardened/demo-human-oidc.sh
```

Parar e limpar (inclui o Keycloak e o estado durável):

```bash
docker compose -p aos-dev-hardened -f deploy/node/dev-hardened/docker-compose.yml -f deploy/node/dev-hardened/docker-compose.oidc.yml down -v
```

> **DEV, não produção.** As chaves vivem em ficheiros gerados (`./secrets/`, git-ignored), não em
> HSM/KMS; o TLS (edge e IdP) é assinado por uma CA de dev local. Com `up-oidc.sh` a postura de
> produção e a verificação OIDC são **reais** (o Keycloak é um IdP a sério); o que fica DEFERIDO é
> o **tenant** de soberania da organização, não o mecanismo. Continua a ser dev, não uma fronteira
> de produção com HSM/KMS e IdP corporativo.

---

## Topologia

```
host :8443  ──TLS──►  edge (nginx, termina TLS)  ──cleartext (rede interna)──►  aos :8080
                                                                                  │
                                                            traces OTLP ──────────┴──►  otel (collector)
```

O nó serve em claro **apenas na rede interna** do compose (porta não publicada) e declara
`AOS_TLS_EXTERNAL_TERMINATION=1`: o edge honra essa "responsabilidade assumida" cifrando o
transporte até ao host. É o modelo de **terminação a montante** do
[README do nó](../README.md#terminação-tls-do-ingresso--api-sse-dsar--perna-otlp-aos-209) — o
papel de um ingress/malha em produção. Terminar TLS no próprio nó deixaria o HEALTHCHECK distroless
(probe HTTP loopback, sem skip-verify) a falhar; o edge mantém o nó saudável **e** dá TLS real.

---

## O que fica LIGADO (o "balde A" — resolúvel por config local)

| Subsistema | Como | Prova no banner / verificação |
|---|---|---|
| **Identidade ENDURECIDA** trust-anchor-only | `AOS_ISSUER_PUBKEY` = `aos-issuer pubkey` (autoridade EXTERNA; a chave de assinatura nunca entra no nó) | `modo de IDENTIDADE: REAL ... trust-anchor` **sem** o aviso "CO-LOCALIZADA" |
| **Canal de controlo** (steer/pause) | `AOS_OPERATORS=ops:demo=<hex>` | `1 operador(es) registado(s) via AOS_OPERATORS` |
| **Four-eyes** (approve) | `AOS_APPROVERS_FILE` → `secrets/approvers.json` (2 pubkeys distintas) | `four-eyes gate (AOS-162) composto: 2 aprovador(es)` |
| **Ratificadores** (promoção) | `AOS_RATIFIERS=release:demo=<hex>` | `promotion controller ... ratificador(es) pinado(s)` |
| **PDP / mediação de política** | `AOS_POLICY_BUNDLE_DIR` (bundle assinado do repo) + `AOS_POLICY_TRUST_ANCHOR` (hex, forçado out-of-band) | `PDP com BUNDLE CARREGADO ... versao "1.0.0"` |
| **Substrato durável** (Event Store + WORM) | `AOS_EVENTSTORE_PATH` + `AOS_WORM_PATH` no volume `aos-data` | `substrato: Event Store durável` / hash-chain WORM verificada |
| **Execução durável** | `AOS_DURABLE_EXECUTION=1` | `execucao duravel (AOS-180): LIGADA` |
| **Soberania de leitura** (REGRA) | `AOS_BOARD_REGIONS=board:demo=eu-west` | read-path soberano com 1 board |
| **Observabilidade OTLP** | `AOS_OTLP_ENDPOINT=http://otel:4318` | traces no stdout do serviço `otel` |
| **TLS do transporte** | edge nginx (self-signed) termina a montante | `https://localhost:8443/healthz → 200` |

Redação de PII, DSAR/crypto-shredding e legal hold já vêm **compostos incondicionalmente** no nó.

---

## O que fica POR RESOLVER — e porquê (não são bugs de config)

Estes itens **não** se ligam com uma variável de ambiente. Ou exigem infra externa real, ou são
pontos de injeção de código, ou estão deferidos por decisão. Referências a `file:line` são âncoras
para o estado atual do código.

### B — RESOLVIDO com IdP real (Keycloak) — `bash up-oidc.sh`

O override `docker-compose.oidc.yml` sobe **Keycloak** (IdP OIDC production-grade) **sobre
PostgreSQL** (não a H2 embutida efémera — o realm/utilizadores persistem num volume), promove o nó
a `AOS_MODE=production` e liga a **credencial forte de soberania**. O nó faz verificação OIDC
**genuína** (discovery + JWKS + assinatura RS256 + iss/aud/janela/anti-replay); não é mock.

| Frente | Onde | Como / prova |
|---|---|---|
| **Soberania — credencial FORTE OIDC** (`AOS_SOVEREIGN_OIDC_ISSUER`/`_AUDIENCE`) | no **NÓ** | `POST /runs` exige `Authorization: Bearer <id-token>`; o `board` vem das CLAIMS assinadas. Prova: run aceite **201**; `X-Aos-Board` forjado sem Bearer **403**. |
| **Identidade humana OIDC** (`aos-issuer --assertion`) | no **ISSUER externo** (não no nó, em modo endurecido) | `bash demo-human-oidc.sh`: o humano-raiz do NHI é DERIVADO do `sub` de um ID-token verificado contra o Keycloak, não de uma flag manual. |

Detalhes de topologia que tornam isto real:
- **TLS ao IdP:** o Keycloak serve https com cert da **CA de dev** (gerada em `up-oidc.sh`); o nó
  confia via `SSL_CERT_FILE=/etc/aos/idp-ca.crt` (Go/Linux honra-o). O contrato exige https para um
  IdP não-loopback — cumprido de verdade.
- **`board`:** um *hardcoded claim mapper* do realm põe `board:demo` no ID-token (dev, um só board);
  em produção real seria um *user-attribute mapper* alimentado pelo IdP da organização.
- O **tenant concreto** (o IdP de soberania da organização) permanece a decisão de infra-org
  DEFERIDA (AOS-205/DEF-201); aqui prova-se o **contrato**, com um IdP real no lugar do tenant.

> Consequência: **`AOS_MODE=production` ARRANCA** — as quatro exigências fail-closed estão
> satisfeitas: identidade endurecida (✔), `AOS_BOARD_REGIONS` (✔), soberania OIDC (✔ Keycloak),
> TLS (✔ edge). Verificado: nó `healthy` em produção + banner *"CREDENCIAL FORTE do leitor
> VERIFICADA (OIDC AOS-174)"*.

### C — só injeção de código (sem env; um embedder tem de o costurar)
- **Política de retenção TTL** — **RESOLVIDO por env** (`AOS_RETENTION_VERSION` + `AOS_RETENTION_PERIODS`).
  Adicionou-se a superfície que faltava em [main.go](../../../packages/cmd/aos/main.go)
  (`parseRetentionFromEnv`, fail-closed, gate AOS-203 + teste `retention_env_test.go`). O
  `ExpirationJob` passa a ter política: o `POST /dsar/expire` crypto-shreds a KEK por-titular das
  classes cujo TTL expirou (respeitando legal hold). Ligado neste stack (`pii_operational=720h,…`).
  *Nota:* isto tocou o binário de entrega (governado) — mudança de código verificada, não config pura.
- **Custódia da KEK / DSARVault** — **RESOLVIDO com HashiCorp Vault** (`AOS_DSAR_VAULT_ADDR` +
  `AOS_DSAR_VAULT_TOKEN_PATH`). Adicionou-se [vaultkeyvault.go](../../../packages/cmd/aos/vaultkeyvault.go):
  um adaptador **key-never-leaves** (`audit.KeyWrapper`, AOS-216) sobre o motor **Transit** do Vault,
  **zero-dep** (só stdlib HTTP — o SDK Go do Vault não entra no binário). A KEK por-titular vive no
  Vault; o embrulho/desembrulho da DEK corre lá; o `/dsar/erase` faz **crypto-shred destruindo a chave
  Transit**. Provado vivo (`demo-vault-shred.sh`): a KEK aparece no Vault após um run e é **destruída**
  pelo erase (unwrap passa a falhar). Teste `vaultkeyvault_test.go` (round-trip + shred). *Nota:* tocou
  o binário governado — mudança de código verificada. Sem as vars, mantém-se o in-memory demo-grade.
- **Model Gateway real** — **RESOLVIDO ligando o gateway do EPIC-06** (`AOS_MODEL_ENDPOINT` +
  `AOS_MODEL_NAME`). [modelgatewaywiring.go](../../../packages/cmd/aos/modelgatewaywiring.go) compõe
  o gateway REAL (`packages/platform/model-gateway`, `NewProduction`) — **sem duplicar** o cliente
  OpenAI (que vive em `internal/adapters`): reutiliza-o via o construtor de produção + o
  `NewModelClient` canónico. Traz allowlist regional **assinada** (embebida, trust-anchor pinado),
  keypool, routing de failover, metering/pricing e endurecimento SSRF. Ligado a um endpoint
  **OpenAI-compatível** — o **LiteLLM proxy** (`http://litellm:4000/v1`), um gateway **config-driven**
  (YAML) multi-provider/multi-modelo, **externo e sem código**. **Zero-dep preservado**: o
  model-gateway já estava no grafo do nó e não traz deps externas (binário linka 0 novas; go.sum
  inalterado). Verificado por `modelgatewaywiring_test.go` (nó→GW→provider contra um httptest
  OpenAI-wire).
  - **Duas camadas de config (limpo):** o **nome** que o nó pede é travado pela allowlist ASSINADA
    embebida (`board-eu` → `gpt-4o | gpt-4o-mini | text-embedding-3-large`) — a fronteira de
    governança do NÓ; o **mapeamento nome→provider/modelo real** vive em
    [litellm/config.yaml](litellm/config.yaml) (livre, versionável). Adicionar/trocar provider ou
    modelo = editar o YAML + `secrets/model.env` (keys), **sem tocar no nó**.
  - **Kimi (Moonshot):** `gpt-4o-mini` está mapeado para `moonshot/kimi-k2-0711-preview`
    (`api.moonshot.ai`). Para completar, põe a tua `MOONSHOT_API_KEY` em `secrets/model.env`. Sem
    key o turno devolve erro do provider; o boot do nó **não** depende disto.
  - **Mais providers/modelos:** adiciona entradas em `litellm/config.yaml` (openai, anthropic, …) +
    as keys em `secrets/model.env`. (O nó só pode PEDIR os nomes allowlisted; cada um mapeia
    livremente para um provider/modelo real.)

### D — deferido por decisão/dependência
- **Checkpoint WORM ASSINADO** (âncora de frescura que fecharia a truncatura do tail, AOS-072).
  Não composto: selar checkpoints exige a **chave privada do operador**, que não vive no runtime do
  nó (custódia out-of-process, molde AOS-156). A re-verificação de hash-chain (sem chave privada)
  **está** ligada e deteta mutação/remoção/inserção.
- **Submissão de ratificações por endpoint/CLI** (AOS-096). Pinar ratificadores está feito (balde
  A); a submissão operacional da ratificação depende do pipeline de promoção (AOS-096), deferida.

---

## Ficheiros

| Ficheiro | Papel |
|---|---|
| `up.sh` | Gera chaves/roster/TLS/`.env` (idempotente) e faz `docker compose up` (balde A). |
| `docker-compose.yml` | Serviços `aos` (endurecido), `edge` (TLS), `otel` (collector). |
| `nginx.conf` | Edge que termina TLS e faz proxy (SSE-friendly) para o nó. |
| `otel-collector.yaml` | Collector OTLP → exporter `debug` (stdout). |
| `up-oidc.sh` | Gera CA+cert do IdP + token do Vault, sobe Postgres+Keycloak+Vault + nó em produção, habilita Transit e prova o run com Bearer OIDC (balde B). |
| `docker-compose.oidc.yml` | Override: `postgres` (DB do Keycloak), `idp` (Keycloak), `vault` (Transit), nó em `AOS_MODE=production`+sovereign OIDC+custódia Vault, e o toolbox `issuer` (profile `tools`). |
| `keycloak/realm-aos.json` | Realm importável: client `aos-node`, user `alice`, mapper `board`→claim. |
| `demo-human-oidc.sh` | Autentica o humano por OIDC (`aos-issuer --assertion`) e submete um run. |
| `demo-vault-shred.sh` | Prova o crypto-shred: um run cria a KEK no Vault; o `/dsar/erase` destrói-a. |
| `litellm/config.yaml` | Gateway de modelos EXTERNO (LiteLLM): mapeia os nomes allowlisted → providers/modelos reais (Kimi/Moonshot, …). Multi-provider/modelo, sem código. |
| `secrets/model.env` (git-ignored) | Keys dos providers do LiteLLM (`MOONSHOT_API_KEY`, …). |
| `issuer-toolbox/Dockerfile` | Compila `aos-issuer` num container para o correr EM-REDE (human OIDC). |
| `secrets/` (git-ignored) | Material gerado: chaves privadas, `approvers.json`, CA+certs TLS. |
| `.env` (git-ignored) | Valores derivados (pubkeys, trust anchor) consumidos pelo compose. |
