# Stack de DEV endurecido do nó `aos`

Levanta o nó `aos` com **todos** os subsistemas que são configuráveis por ambiente/mount/tooling
do repositório — mais um **edge TLS** (ingresso) e um **collector OTLP**. É o contraponto ao
arranque mínimo (`docker run ... aos-node:local`), cujo banner declara quase tudo DESLIGADO.

```bash
bash deploy/node/dev-hardened/up.sh
```

Ao fim: nó `healthy`, `https://localhost:8443/healthz` a devolver `200` (TLS self-signed, use
`curl -k`). Parar e limpar o estado durável:

```bash
docker compose -f deploy/node/dev-hardened/docker-compose.yml down -v
```

> **DEV, não produção.** As chaves vivem em ficheiros gerados (`./secrets/`, git-ignored), não em
> HSM/KMS; o TLS é self-signed; a soberania forte e a identidade humana OIDC continuam a exigir
> IdP real. É uma demonstração fiel da POSTURA, não uma fronteira de produção.

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

### B — exige infra externa (IdP OIDC real)
- **Identidade humana OIDC/WebAuthn** (`AOS_HUMAN_OIDC_ISSUER`/`_AUDIENCE`, [main.go:860](../../../packages/cmd/aos/main.go)).
  Frente 1 do D4. Precisa de um IdP OIDC com issuer `https` e JWKS. Sem ele, o nó usa a allowlist
  de nomes de referência (demo-grade). Ligar isto localmente exigiria montar um IdP (ex.: Keycloak/
  Dex) — fora do âmbito de um self-signed de dev.
- **Soberania de leitura — credencial FORTE OIDC** (`AOS_SOVEREIGN_OIDC_ISSUER`/`_AUDIENCE`).
  A **regra** board→região está ligada (balde A), mas a credencial forte que substitui os headers
  `X-Aos-Reader`/`X-Aos-Board` auto-declarados exige o IdP de soberania da organização. O
  **provisionamento do tenant está DEFERIDO** por decisão (banner AOS-205; DEF-201).

> Consequência: **`AOS_MODE=production` não arranca localmente** — exige, além de identidade
> endurecida (✔) e TLS (✔ via edge), a credencial forte de soberania OIDC (acima). Sem IdP real,
> a produção recusa o arranque (`ErrProductionNeedsSovereignAuthority`), fail-closed **by design**.

### C — só injeção de código (sem env; um embedder tem de o costurar)
- **Política de retenção TTL** (`Config.Retention`, [bootstrap.go:370](../../../packages/cmd/aos/bootstrap.go)).
  O `ExpirationJob` é sempre composto, mas sem env que preencha a política "varre mas nada expira"
  (fail-closed). Ligar exige injetar `audit.RetentionConfig` em código.
- **Custódia da KEK / DSARVault** (`Config.DSARVault`, [bootstrap.go:354](../../../packages/cmd/aos/bootstrap.go)).
  Sem env: o default é um vault in-memory demo-grade (KEK perde-se no restart). Produção injeta um
  key-service/KMS/HSM externo pela mesma porta (`audit.KeyVault`/`KeyWrapper`, AOS-215/AOS-216) —
  infra-org, não configurável por docker.
- **Model Gateway real** ([bootstrap.go:1451](../../../packages/cmd/aos/bootstrap.go)). Sem env; o
  nó usa o modelo de referência. O gateway real é o EPIC-06.

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
| `up.sh` | Gera chaves/roster/TLS/`.env` (idempotente) e faz `docker compose up`. |
| `docker-compose.yml` | Serviços `aos` (endurecido), `edge` (TLS), `otel` (collector). |
| `nginx.conf` | Edge que termina TLS e faz proxy (SSE-friendly) para o nó. |
| `otel-collector.yaml` | Collector OTLP → exporter `debug` (stdout). |
| `secrets/` (git-ignored) | Material gerado: chaves privadas, `approvers.json`, cert/chave TLS. |
| `.env` (git-ignored) | Valores derivados (pubkeys, trust anchor) consumidos pelo compose. |
