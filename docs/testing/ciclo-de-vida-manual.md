# Roteiro manual — ciclo de vida completo do nó `aos` (EPIC-19)

Este documento é um **roteiro de verificação manual, passo a passo e explicável**, do
ciclo de vida completo de um run no nó `aos` na stack endurecida `deploy/node/dev-hardened`.
Ao contrário de [`demo-ciclo-completo.sh`](../../deploy/node/dev-hardened/demo-ciclo-completo.sh)
(que corre tudo de uma vez), aqui cada fase é um comando isolado com a saída real e a
explicação do **quê / porquê / o que observar** — pensado para onboarding, auditoria e
demonstração.

Segue um único artefacto — uma tool call `web_post` proposta pelo modelo — através de
**todo** o sistema, até ser negada pelo taint-gate, selada no WORM e o conteúdo do titular
apagável por crypto-shred.

> **Pré-requisitos.** Stack a correr (`bash deploy/node/dev-hardened/up-oidc.sh`) com
> `AOS_MODEL_TOOLS_REGISTER=1`, ambas as tools em `model-tools/tools.json`, e uma
> `MOONSHOT_API_KEY` válida em `secrets/model.env`. Os exemplos usam `alice`/`alice`
> (utilizador de dev do realm `aos`).

## Helpers usados nos comandos

```bash
PROJECT=aos-dev-hardened
# Bearer soberano FRESCO (ID-token OIDC do Keycloak). Fresco por chamada: o verificador
# recusa reutilização do mesmo jti (anti-replay).
tok() {
  docker run --rm --network ${PROJECT}_default curlimages/curl:latest -sk \
    -d grant_type=password -d client_id=aos-node -d username=alice -d password=alice -d scope=openid \
    https://idp:8443/realms/aos/protocol/openid-connect/token | sed -n 's/.*"id_token":"\([^"]*\)".*/\1/p'
}
```

---

## Passo 1 — Saúde e prontidão

**Porquê:** confirmar a postura de produção *fail-closed* antes de submeter trabalho. Estas
são as **únicas** rotas não-autenticadas (probes de k8s não assinam tokens) e não consomem
rate-limit.

```bash
curl -sk https://localhost:8443/healthz   # -> 200 {"status":"ok"}
curl -sk https://localhost:8443/readyz    # -> 200 {"status":"ready"}
```

`healthz` = processo vivo. `readyz` = todos os subsistemas compostos (identidade real,
soberania, WORM, PDP, model gateway).

---

## Passo 2 — Identidade humana (Bearer OIDC)

**Porquê:** o read-path exige um ID-token RS256 verificado contra o JWKS do Keycloak; a
claim `board` **assinada** determina a região (o header `X-Aos-Board` auto-declarado já não
autoriza).

```bash
BEARER="$(tok)"
printf '%s' "$BEARER" | cut -d. -f2 | tr '_-' '/+' | base64 -d | python -m json.tool
```

Claims relevantes e o seu papel:

| Claim | Papel no nó |
|---|---|
| `iss` `https://localhost:9443/realms/aos` | o nó verifica a assinatura contra o JWKS deste emissor |
| `aud` `aos-node` | o token tem de ser destinado a este nó |
| `sub` `18743bd4…` | o **titular** — chaveia a KEK de cifra por-titular no Vault |
| `board` `board:demo` | assinado pelo IdP → mapeia para a região |
| `jti` | id único → **anti-replay** (cada jti serve uma vez) |
| `exp` | expiração / frescura |

---

## Passo 3 — Identidade do agente (NHI assinado)

**Porquê:** o `sub` OIDC é o *humano*; o **NHI** é a credencial do *agente* que executa, com
as capabilities delegadas e a cadeia `humano → agente`. Emissor e algoritmo **diferentes**
do Bearer (o Keycloak autentica o humano; o `aos-issuer` emite a identidade do agente).

```bash
NHI="$(packages/cmd/aos-issuer/aos-issuer mint \
  --key-file deploy/node/dev-hardened/secrets/issuer.key --issuer iss:aos-issuer \
  --human human:alice --agent agt-manual --class agent-worker \
  --caps cap:http.post,cap:fs.read --ttl 15m | tr -d '\r\n')"
printf '%s' "$NHI" | cut -d. -f2 | tr '_-' '/+' | base64 -d | python -m json.tool
```

Payload (essencial):

```json
{
  "user_id": "human:alice",
  "agent_id": "agt-manual",
  "agent_class": "agent-worker",
  "policy_ref": "policy://agent-worker",
  "scope": ["cap:http.post", "cap:fs.read"],
  "iss": "iss:aos-issuer",
  "jti": "…", "exp": 1785931240,
  "delegation_chain": [
    { "sub": "human:alice", "act_as": "agt-manual",
      "authority": ["cap:http.post", "cap:fs.read"], "depth": 0 }
  ]
}
```

Header: `{"alg":"EdDSA","typ":"NHI","kid":"iss:aos-issuer"}` — assinatura ed25519.

- `scope` — as capabilities delegadas (a *authority* que o PDP avalia).
- `delegation_chain` — a prova criptográfica `human:alice --act_as--> agt-manual`; é o que o
  WORM sela como `chain=[human:alice → agt-manual]`.
- `policy_ref` — liga à allowlist de capabilities da classe (gate AOS-007, **antes** do Cedar).

**Duas identidades, dois emissores:** o Bearer (humano) autoriza a *chamada à API*; o NHI
(agente) autoriza a *ação mediada*. Ambos têm de bater.

---

## Passo 4 — Submissão do run

**Porquê:** é a fronteira onde as duas identidades entram; o nó sela a **residência de
região** do run na criação (base da soberania por-run).

```bash
RID="run-manual-$(date -u +%H%M%S)"
curl -sk -X POST https://localhost:8443/runs \
  -H "Authorization: Bearer ${BEARER}" -H 'Content-Type: application/json' \
  -d "{\"run_id\":\"${RID}\",\"objective\":\"Publica resultado-42 via HTTP POST para https://api.example.com/results usando a tool web_post. So podes publicar via web_post.\",\"principal_nhi\":\"agt-manual\",\"credential\":\"${NHI}\",\"scope\":[\"cap:http.post\"]}"
# -> 201 {"run_id":"run-manual-…","status":"accepted"}
```

O nó verifica Bearer + NHI, admite o run, sela a residência, e arranca o loop em background.
O objetivo induz a tool `web_post` (`cap:http.post`) — a capability que o taint-gate vai negar.

---

## Passo 5 — Modelo + execução

**Porquê:** entre `accepted` e `completed`, o nó chamou o modelo real (Kimi via LiteLLM) e
mediou cada tool call proposta. Usa-se um Bearer **fresco** por leitura (anti-replay).

```bash
for _ in $(seq 1 40); do
  st="$(curl -sk -H "Authorization: Bearer $(tok)" https://localhost:8443/runs/${RID} \
        | sed -n 's/.*"status":"\([^"]*\)".*/\1/p')"
  [ "$st" = "completed" ] && break; sleep 3
done
echo "status=${st}"   # -> completed
```

---

## Passo 6 — Tool set congelado (evento durável)

**Porquê:** antes de falar com o modelo, o nó **congela** o catálogo de tools assinado — o
snapshot contra o qual cada tool call é revalidada (crash-safe, sobrevive a failover). É o
evento `run.toolset.frozen` no Event Store.

```
frozen_at: 2026-08-05T11:48:42.210016592Z
  tool id=doc_read  v1.0.0  egress=none      status=active
       digest    = sha256:cc05b325678f7332dd30acfb6152e47dacb71457f3677f0eda414a31270d3c3f
       signature = S9znmNPhRjtTYzMCAmkYC1D4rLyfo35CufNvZqTni96Y…  (ed25519)
       publisher = key:aos-node-dev-tool-registry   trust=first_seen
  tool id=web_post  v1.0.0  egress=external  status=active
       digest    = sha256:c8206d3dae095b1e0ec126875537f9df18d883413f51c82ea4ab1c848f5781f5
       signature = 5zr65al0z357QKLf99fDM3YjS23do0EDoZ3Kch4ieyQP…  (ed25519)
       publisher = key:aos-node-dev-tool-registry   trust=first_seen
```

A cada tool call, o Reference Monitor **recomputa o digest** da definição viva e **verifica a
assinatura** contra o trust store (`publisher`). Bate → admite; não bate → nega **antes** do PDP.
(`frozen_at` é um timestamp real desde o fix de injeção do relógio; antes era zero-value.)

---

## Passo 7 — Mediação de uma tool call (o coração do ciclo)

**Porquê:** é o **choke-point único** (ADR-002) por onde toda a ação passa. Ler um span OTLP
`execute_tool` real mostra o veredicto e a razão.

Atributos do span (tool `web_post`):

| Atributo | Valor | Significado |
|---|---|---|
| `gen_ai.tool.name` | `web_post` | a tool que o Kimi quis chamar |
| `aos.taint` | `untrusted` | a autorização veio de dados do **modelo** |
| `aos.decision` | `deny` | negada |
| `aos.decision.denied_by` | `policy` | negada no PDP/Cedar (não na revalidação, não no scope) |
| `aos.tool_call.hash` | `sha256:2ca5…` | hash de (tool+args) — liga o veredicto ao pedido exato |
| `aos.tool.result_taint` | `untrusted` | qualquer resultado de tool é untrusted a jusante |

A cadeia de hooks dentro de `Mediate()`:

```
Kimi propõe:  web_post({url:"https://api.example.com/results", body:"resultado-42"})
                     │
        ┌────────────▼─────────────  Reference Monitor: Mediate() ─────────────┐
        │  hook 1  IDENTIDADE      NHI presente? cadeia human:alice→agt-manual? ✓
        │  hook 2  REVALIDAÇÃO     web_post no frozen set? digest recomputado bate?
        │                         assinatura ed25519 verifica contra o publisher? ✓
        │  hook 3  PDP / CEDAR ◄── AQUI MORRE
        │            action = cap:http.post   (web_post → capability, do registry)
        │            regra allow_http_post EXIGE  context.taint != "untrusted"
        │            authority contém cap:http.post ✓   region == eu ✓
        │            MAS  aos.taint = untrusted   ✗  ── a proposta veio do MODELO
        └──────────────────────────────────────────────────────────────────────┘
                     │
        veredicto:  aos.decision = deny   denied_by = policy
```

**Propriedade central (P4 / AOS-069):** *"untrusted não comanda"*. O plano que o LLM produz
é dado untrusted; não pode **originar** uma capability privilegiada como `cap:http.post`. A
`web_post` nunca executou — foi negada antes de tocar a rede.

---

## Passo 8 — Leitura soberana + anti-replay

**Porquê:** provar que a leitura exige soberania **e** que o anti-replay por-jti bloqueia a
reutilização de um token.

```bash
curl -sk -H "Authorization: Bearer $(tok)"    https://localhost:8443/runs/${RID}
# -> 200 {"run_id":"…","status":"completed",…}

curl -sk -H "Authorization: Bearer ${BEARER}" https://localhost:8443/runs/${RID}
# -> 404 {"error":"not found"}   (o Bearer da submissão já foi visto)
```

O nó devolve *not-found* (404) em vez de *forbidden* para não revelar a existência do run a
um token já-visto.

---

## Passo 9 — Trajetória (SSE, replay do Event Store)

**Porquê:** o read-path reproduz a trajetória inteira a partir do log append-only — é como um
observador audita o run em tempo-real.

```bash
curl -sk -N --max-time 4 -H "Authorization: Bearer $(tok)" \
  https://localhost:8443/runs/${RID}/trajectory
```

Emite eventos SSE `run.toolset.frozen` (seq 1), `step.checkpoint`, `turn.recorded`, cada um
com `event_id` (ULID), `stream_id`, `seq` monotónico e `producer`.

---

## Passo 10 — Reconstrução soberana

**Porquê:** decifra o conteúdo selado por-titular para um leitor autorizado por soberania — o
oposto do crypto-shred (com a KEK recupera; sem ela nunca).

```bash
curl -sk -H "Authorization: Bearer $(tok)" https://localhost:8443/runs/${RID}/reconstruct
# -> 200  (run que selou conteúdo com o Vault de pé)
# -> 404 "reconstrucao indisponivel"  (run cujo selamento falhou: sem conteúdo cifrado, nada a decifrar)
```

O 404 é **fail-closed**, não erro: sem KEK/conteúdo selado, não há nada para reconstruir.

---

## Passo 11 — Registo WORM selado (auditoria imutável)

**Porquê:** cada veredicto é selado num log append-only tamper-evident (`prev_hash →
entry_hash`). **Não** depende da KEK (só o PII é cifrado à parte), portanto o registo do
`deny` existe mesmo se o Vault estiver em baixo.

```
-- registo WORM selado (decisao web_post do run) --
   AuditSeq   = 1
   Partition  = run-manual-114252
   Decision   = deny
   Capability = cap:http.post
   ToolID     = web_post
   StepID     = step-000001-tool-1
   Resource   = type=http region=eu
   Principal  = agt-manual  chain=['human:alice act_as agt-manual']
   Context    = taint=untrusted
   PrevHash   = 3vvzqwLGUc2Nwagfl9L+ncCpJWeMkLZ4JmIytF4PocY=
   EntryHash  = 4qsgqxQ7mHebJqu8GZb88Bi274zzcNKCtJwzURu9gwg=
```

O WORM tem o que o span não mostrava: **`Capability = cap:http.post`** — prova de que a
`web_post` foi enriquecida para a capability governada, mediada e negada. O `EntryHash` sela
todos os campos + o `PrevHash` do registo anterior; alterar um bit de qualquer decisão passada
parte a cadeia a partir daí.

> **Esquema de partição do WORM.** A `Partition` é uma fronteira de encadeamento com namespace
> por plano de atividade, **não** uma partição plana por run. Um run gera registos em várias:
> `run-<id>` (decisões de mediação de dados), `gov.read/run-<id>` (o read-path soberano é ele
> próprio auditado), `gov.residency/run-<id>` (residência na criação), mais partições globais
> cross-run (ex.: `gov.sovereignty.authority`). O `RunID`/`StepID` vão selados no conteúdo de
> **todas**, independentemente da partição — a correlação run↔registo é ela própria
> tamper-evident. A hash-chain é **por-partição** (`AuditSeq` gapless em cada).

---

## Passo 12 — Crypto-shred (direito ao apagamento, Art. 17)

**Porquê:** provar o apagamento por *crypto-shredding* real — key-never-leaves, e destruir a
KEK do titular torna o conteúdo irrecuperável.

```bash
SUB="…"   # o sub do titular (Passo 2)
KEK="aos-kek-$(printf 'aos.audit.pii:%s' "${SUB}" | sha256sum | cut -d' ' -f1)"

# 12a) um run sela conteúdo sob a KEK -> ela aparece no Vault (motor Transit)
#      (list em http://vault:8200/v1/transit/keys?list=true)
# 12b) apagar:
curl -sk -X POST https://localhost:8443/dsar/erase \
  -H "Authorization: Bearer $(tok)" -H 'Content-Type: application/json' \
  -d "{\"subject_id\":\"${SUB}\"}"
# -> 200  ; a KEK deixa de constar no Vault -> conteúdo do titular irrecuperável
```

`subject_id` tem de ser um **pseudónimo opaco** (o nó recusa formatos típicos de PII, para não
selar dados pessoais verbatim). O erase precisa de um Bearer fresco (anti-replay).

---

## Síntese

| # | Fase | Rota / comando | Propriedade provada |
|---|---|---|---|
| 1 | Saúde | `GET /healthz` `/readyz` | postura production fail-closed |
| 2 | Identidade humana | token Keycloak | OIDC RS256, board assinado, anti-replay |
| 3 | Identidade agente | `aos-issuer mint` | delegação human→agente (ed25519) |
| 4 | Submissão | `POST /runs` | dupla identidade + residência selada |
| 5 | Execução | poll status | loop + modelo real (Kimi) |
| 6 | Tool set frozen | `run.toolset.frozen` | catálogo assinado congelado |
| 7 | **Mediação** | span OTLP `execute_tool` | **taint-gate P4: untrusted não comanda** |
| 8 | Leitura + replay | `GET /runs/{id}` ×2 | soberania + anti-replay por-jti |
| 9 | Trajetória | `GET …/trajectory` (SSE) | replay append-only do Event Store |
| 10 | Reconstrução | `GET …/reconstruct` | fail-closed: sem KEK, sem recuperação |
| 11 | Auditoria | registo WORM | audit imutável tamper-evident |
| 12 | Crypto-shred | `POST /dsar/erase` | Art. 17 por crypto-shred real |

**O fio condutor:** uma tool call (`web_post`) atravessou todo o sistema — proposta pelo
modelo (untrusted) → oferecida via registry assinado → congelada no arranque → revalidada por
digest+assinatura → **negada pelo taint-gate do PDP** → selada no WORM com a capability e a
cadeia de delegação → observável no OTLP → e o conteúdo do titular apagável por crypto-shred.
O LLM propõe, o nó dispõe — e regista tudo de forma imutável.

## Nota operacional (dev-hardened)

O **Vault de dev é in-memory**: se o Docker/Vault reiniciar, o motor Transit perde-se e o
selamento de KEK devolve `404` (o nó recusa-se a fingir que selou). Re-ativar:

```bash
docker run --rm --network aos-dev-hardened_default curlimages/curl:latest -sk \
  -H "X-Vault-Token: aos-dev-root" -X POST -d '{"type":"transit"}' \
  http://vault:8200/v1/sys/mounts/transit
```

O `up-oidc.sh` já faz isto no arranque; em produção o Vault seria persistente.

---

*Verificado live em 2026-08-05 (run `run-manual-114252`), stack `deploy/node/dev-hardened`,
modelo Kimi via LiteLLM. Automação equivalente:
[`demo-ciclo-completo.sh`](../../deploy/node/dev-hardened/demo-ciclo-completo.sh).*
