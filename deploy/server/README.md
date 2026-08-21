# CI/CD do nó `aos` — servidor `37.60.241.150`

Cadeia completa de **tag → gates → imagem atestada → GHCR → servidor**, com reversão por um
comando. Este directório é o lado do *servidor*; os workflows que o conduzem são
[`.github/workflows/release.yml`](../../.github/workflows/release.yml) e
[`.github/workflows/deploy.yml`](../../.github/workflows/deploy.yml).

A CI (24 gates fail-closed) já existia e **não foi tocada**: o release **invoca-a** por
`workflow_call`. Não há uma segunda definição, mais permissiva, dos gates para releases — a
entrega passa pelos mesmos que um PR, ou não passa.

---

## O servidor real (levantamento de 2026-08-14)

`37.60.241.150` **não é um host dedicado**. Contabo, Ubuntu 20.04.6, 8 vCPU / 23 GB / 1,2 TB
(51% usado), sem swap, 171 dias de uptime. Corre, em simultâneo:

- **Um control-plane Kubernetes** (kubeadm v1.29.15) com ~30 namespaces — ArgoCD, cert-manager,
  Istio, Keycloak, Kafka/Strimzi, Longhorn, Temporal, Velero, Gatekeeper, ClickHouse, Neo4j,
  MongoDB, MLflow, observability (Grafana/Promtail), um registry interno e `neural-hive*`.
- **Um conjunto de containers Docker órfãos do Coolify** — 3× Nextcloud, 2× n8n, Chatwoot,
  RabbitMQ, Shlink, Evolution API, Weaviate e vários Postgres/Redis. O control-plane do Coolify
  **já não existe** e o seu proxy saiu com código 127 há cinco meses: estes serviços correm, mas
  nada os encaminha (só o RabbitMQ publica no host, em `5672`).

Três factos deste levantamento moldam esta configuração e explicam decisões que de outro modo
pareceriam arbitrárias:

**1. O `ingress-nginx` do cluster ocupa 80, 443, 8443 e 10254.** Por isso o edge publica em
**8444** — a porta interna do nginx continua a ser 8443, só o mapeamento no host muda. O
`bootstrap.sh` recusa arrancar se a porta escolhida já estiver ocupada.

**2. Nenhuma política de firewall é aplicada por estes scripts.** O `ufw` está inactivo e o host
expõe, no IP público e sem filtro, `6443` (kube-apiserver), `10250` (kubelet), `2379/2380`
(etcd) e `8472/udp` (flannel). Um `ufw enable` com default-deny — que a primeira versão deste
script fazia — cortaria o plano de controlo do cluster. O passo 5 do `bootstrap.sh` passou a ser
**só diagnóstico**: imprime as portas de host à escuta e não toca em regra nenhuma.

**3. O cluster não aceita workloads novos.** Dos 6 nós, **5 estão `NotReady`** (*Kubelet stopped
posting node status*) e o control-plane tem o taint `node-role.kubernetes.io/control-plane:
NoSchedule`. Qualquer Pod novo fica `Pending` para sempre — é o que já acontece a cert-manager,
Keycloak, istiod, MLflow, MongoDB e ao `local-path-provisioner`, parados há 25–36 dias. É por
isto que o AOS entra como **stack Docker Compose no host**, e não como manifesto Kubernetes:
o caminho k8s seria o coerente com o resto (ingress + cert-manager + ArgoCD já existem), mas
hoje entregaria um Pod `Pending`, não um serviço.

Quando o cluster for reparado, migrar é directo — a imagem é a mesma e a superfície de
configuração é só ambiente + ficheiros montados.

## Topologia

```
tag v*  ──►  release.yml
              ├─ gates      ci.yml (os 24, fail-closed)
              ├─ publish    package.sh → sbom.sh → sign.sh → verify-attestation.sh → push GHCR
              └─ deploy     deploy.yml  ─── environment: production (revisor humano) ───┐
                                                                                        │
   37.60.241.150                                                                        ▼
   ┌──────────────────────────────────────────────────────────────────┐        rsync + ssh
   │  :8444  edge (nginx)  ── TLS ──┐                                 │◄───────────────┘
   │  :9443  idp  (Keycloak) ───┐   │                                 │
   │                            │   ▼                                 │
   │            aos (distroless, non-root, root-fs read-only)         │   rede interna: sem
   │             ├─ volume aos-data   (Event Store WAL + WORM)        │   porta publicada
   │             ├─► gvisor           (runsc — tool calls)            │
   │             ├─► litellm          (model gateway → Kimi)          │
   │             ├─► vault            (KEK por-titular, Transit)      │
   │             │    ▲ vault-unseal  (watchdog do selo)              │
   │             ├─◄──┘ idp ──► idp-db (Postgres)                     │
   │             └─► otel-collector   (traces + scrape /metrics)      │
   └──────────────────────────────────────────────────────────────────┘
```

**Duas** portas chegam ao mundo: **8444/tcp** (a API, via `edge`) e **9443/tcp** (o IdP). A
segunda existe porque o chamador tem de conseguir obter um token — sem isso, em modo produção,
ninguém fala com o nó. Tudo o resto (`aos`, `vault`, `litellm`, `gvisor`, `idp-db`, `otel`) vive
só na rede interna do compose (`expose`, nunca `ports`).

O nó serve em claro apenas nessa rede interna e declara `AOS_TLS_EXTERNAL_TERMINATION=1` —
declaração que o `edge` honra ao cifrar o transporte. O `idp` termina TLS ele próprio, com
certificado da **CA interna**; o `edge` usa Let's Encrypt real. São cadeias diferentes de
propósito: o IdP não precisa de ser confiável pelo mundo, só pelo nó e pelo operador.

---

## Onde vive cada chave

É a decisão estruturante desta configuração, e a razão de existir o `gen-identity.sh`.

| Material | Nasce em | Vive em | Porquê |
|---|---|---|---|
| `issuer.key` (assinatura de identidade) | máquina do operador | **máquina do operador** | O nó corre *trust-anchor-only*: verifica com a pubkey e nunca assina. Se a privada vivesse no servidor, quem o comprometesse mintaria a sua própria identidade — e a separação de *trust-domains* (ADR-006) seria decorativa. |
| `operator.seed`, `ratifier.seed`, `approver-*.seed` | máquina do operador | **máquina do operador** | Idem: `steer`/`pause`, promoção e *four-eyes* são autoridade **sobre** o nó, não autoridade **do** nó. |
| Pubkeys (issuer, operadores, ratificadores, aprovadores) | derivadas das seeds | `/opt/aos/.env` + `secrets/approvers.json` | Material **público**. É tudo o que o servidor precisa para verificar. |
| Trust anchor do PDP | `packages/control-plane/pdp/policies/trust_anchor.pub` | `/opt/aos/.env` (hex) | Forçado *out-of-band*: nunca lido do directório mutável do bundle, senão quem tivesse escrita lá trocava âncora **e** assinatura de uma vez. |
| Chave TLS do edge | servidor (`provision.sh`) | **servidor** | Cifra transporte; não autentica sujeitos nem autoriza nada. |
| **CA interna** (`internal-ca/ca.key`) | máquina do operador | **máquina do operador** | Assina os certificados do `idp` e do `vault`. Quem a detivesse forjava um certificado para `idp` e **personificava o IdP perante o nó** — isso é fronteira de autoridade, não de transporte, e por isso fica ao lado da `issuer.key`. Só as folhas (`idp.crt/key`, `vault.crt/key`) e a `ca.crt` viajam. |
| Segredo do `aos-reader` | Keycloak (no servidor) | `secrets/reader-client-secret` (0400) | Credencial de máquina, gerada pelo IdP. Nunca escolhida por ninguém. |
| Token do Vault | Vault (no servidor) | `secrets/vault-token` | **Não é o root.** Token periódico com política só sobre `aos-kek-*`. O root fica em `secrets/vault-init.json`. |
| Unseal do Vault | Vault (no servidor) | `secrets/vault-init.json` | Ver §"O selo do Vault" — está aqui por decisão declarada, e limita o que o selo protege. |
| Chave de release (DSSE) | custódia do Arquitecto de Plataforma | secret `AOS_RELEASE_KEY` | Ver [`../node/CUSTODIA-CHAVE-RELEASE.md`](../node/CUSTODIA-CHAVE-RELEASE.md). |

O servidor, portanto, **não guarda nenhuma credencial que conceda autoridade sobre o sistema**.

---

## Sandbox das tool calls — porquê gVisor e não Firecracker

O nó medeia cada tool call, mas **onde** ela corre depende do driver, e os três não são
equivalentes:

| Driver | Fronteira | Neste servidor |
|---|---|---|
| `fake` (default) | Jail **in-process**: overlay read-only, seccomp default-deny, escape bloqueado | Funciona — mas o próprio pacote marca-o **"NUNCA usar em produção"** |
| `firecracker` | microVM com KVM (ADR-004) | ❌ **Impossível**: sem `/dev/kvm`, 0 CPUs com `vmx`/`svm`. O host é ele próprio um convidado sem virtualização aninhada |
| `gvisor` | Interposição de syscalls em user-space (`systrap`) | ✅ **Em uso** — não precisa de KVM |

O `fake` não é um stub vazio: tem isolamento real. Mas a fronteira é o processo do nó, e é por
isso que o repositório o proíbe em produção.

### O componente

O `GVisorDriver` já tinha a porta certa (`WithGVisorExecutor`) e ninguém a injectava — pelo que
`AOS_SANDBOX_DRIVER=gvisor` devolvia `ErrDriverUnavailable`. Faltava o executor, não configuração.

[`gvisor/`](gvisor/) é esse executor, no mesmo molde do componente Firecracker e com o **mesmo
contrato de fio** (`/healthz`, `POST /exec`): o nó não sabe qual dos dois está do outro lado, e
trocar de driver passa a ser topologia.

Cada execução tem bundle OCI **novo e efémero**, rootfs read-only, `/seed` em bind read-only,
zero capabilities, `noNewPrivileges`, uid não-root e sem rede.

```bash
AOS_SANDBOX_DRIVER=gvisor
AOS_SANDBOX_GVISOR_URL=http://gvisor:9101/exec
```

Sem a URL, o driver fica o skeleton e o exec é recusado — **o gap honesto**, nunca uma execução
fora do sandbox.

> ⚠️ O componente corre **privilegiado**: o `runsc` precisa de criar namespaces e montar bundles.
> É a concessão desta escolha, e a razão de ser um processo **separado** do nó — o nó nunca corre
> privilegiado, e a fronteira entre os dois é HTTP.

> ⚠️ Ao contrário do `fake`, o skeleton **não** faz verificações de escape em Go. A contenção é a
> interposição de syscalls do `runsc`. A verificação de path no guest é defesa em profundidade,
> não a fronteira.

## Acrescentar uma variável ao `.env` não chega

⚠️ **O bloco `environment:` do `docker-compose.prod.yml` é uma *allowlist*.** O `.env` alimenta
apenas a **interpolação** do compose; o contentor recebe só o que estiver mapeado explicitamente.
Acrescentar `AOS_XPTO=…` ao `.env` e reiniciar não faz nada — o nó continua a declarar a
funcionalidade como não-composta, e a única pista é o banner.

Para ligar uma variável nova são **dois** sítios:

```yaml
# docker-compose.prod.yml, no bloco environment: do serviço `aos`
AOS_XPTO: "${AOS_XPTO:-}"
```
```bash
# /opt/aos/.env
AOS_XPTO=valor
```

É deliberado — a superfície de configuração do nó fica explícita e auditável em vez de herdar
tudo o que estiver no ambiente. Mas custa uma iteração a quem não sabe.

**Verificar sempre pelo banner, não pelo ficheiro:**

```bash
docker logs aos-aos-1 2>&1 | grep -iE 'COMPOSTO|ARMADO|DORMENTE|NAO LIGAD'
```

> Exemplo real: ligar `AOS_BUDGET_MAX_TOKENS` armou **três** subsistemas de uma vez — orçamento,
> burn-down e prompt de exaustão. Os dois últimos estavam dormentes só por lhes faltar o
> denominador; o four-eyes e o operador já lá estavam.

## Os ficheiros JSON montados não toleram um único campo a mais

⚠️ **Não acrescentes comentários, notas ou campos de documentação a `secrets/authority.json` ou
`secrets/approvers.json`.** O nó descodifica-os com `DisallowUnknownFields`: um campo que o
esquema não preveja — mesmo um inofensivo `"_nota"` a explicar o ficheiro — **aborta o arranque**:

```
AOS_AUTHORITY_FILE invalido: json: unknown field "_nota"
```

Foi assim que o primeiro deploy real falhou: os templates traziam um `_nota` explicativo, o
`provision.sh` copiou-o e o nó entrou em *restart loop*. A rigidez é deliberada — o mesmo
descodificador que recusa um campo decorativo recusa um `capabilities` mal escrito que passaria
despercebido e deixaria o directório de autoridade sem efeito.

Por isso a semântica destes ficheiros vive **aqui**, e não dentro deles:

**`authority.json`** — directório de autoridade externo do `ScopeGate`. O escopo efectivo passa a
ser `token ∩ directório`: **restringe e revoga, nunca amplia**. ⚠️ Semântica que engana: um
sujeito **ausente não é restringido** (cai na autoridade plena do seu token) — é o que torna
seguro ligar um directório parcial, mas por isso **revogar não é remover**. Para negar tudo a
alguém, lista-o com `"capabilities": []`. Incrementa `revision` a cada alteração. Não é assinado
(ao contrário do bundle PDP) porque só pode restringir: adulterá-lo nega acções — indisponibilidade
visível e auditável — mas não concede nenhuma.

**`approvers.json`** — roster do *four-eyes*. Só material público: principals, pubkeys ed25519 em
hex e autoridade. As privadas ficam com cada aprovador. As pubkeys **têm de ser distintas** — o
dual-control recusa *self-approval*.

## Instalação, do zero

### 0. Gerar a identidade (na tua máquina, uma vez)

```bash
bash deploy/server/gen-identity.sh
```

Escreve `deploy/server/secrets-local/` (ignorado pelo git): as seeds privadas ficam aí para
sempre, e `server.env` + `approvers.json` são o que segue para o servidor.

> Guarda `secrets-local/` num cofre. Perder `issuer.key` significa que nenhuma credencial nova
> pode ser emitida para este nó; substituí-la invalida todas as que estão em circulação.

### 1. Preparar o servidor (root, uma vez)

```bash
scp deploy/server/bootstrap.sh root@37.60.241.150:/tmp/
ssh root@37.60.241.150 'bash /tmp/bootstrap.sh "$(cat ~/.ssh/id_ed25519_aos_deploy.pub)"'
```

Instala Docker + compose, cria o utilizador `aos` (sem sudo, no grupo docker), monta `/opt/aos`,
liga rotação de logs e imprime o diagnóstico de firewall. É idempotente e **não altera regras de
firewall nem sobrepõe o `daemon.json` existente** — neste host ambos já têm conteúdo alheio ao
AOS. Aborta se a porta do edge estiver ocupada.

> Neste servidor, os passos 0/1 (utilitários e Docker) não fazem nada: já lá estão Docker 28.1.1,
> compose v2.35.1 e `rsync`. O que resta de facto é o utilizador `aos`, a árvore `/opt/aos` e o
> diagnóstico.

> A chave do argumento é a **pública** do par que o CD vai usar. Gera-a com
> `ssh-keygen -t ed25519 -f ~/.ssh/id_ed25519_aos_deploy -C aos-deploy`; a **privada** vai para
> o secret `DEPLOY_SSH_KEY`.

### 2. Levar configuração e material público

```bash
scp deploy/server/{docker-compose.prod.yml,nginx.conf,otel-collector.yaml,deploy.sh,rollback.sh,provision.sh,.env.example} aos@37.60.241.150:/opt/aos/
scp -r deploy/server/templates                aos@37.60.241.150:/opt/aos/
scp -r packages/control-plane/pdp/policies/.  aos@37.60.241.150:/opt/aos/policies/
scp deploy/server/secrets-local/server.env     aos@37.60.241.150:/opt/aos/.env
scp deploy/server/secrets-local/approvers.json aos@37.60.241.150:/opt/aos/secrets/approvers.json
```

Depois do primeiro deploy isto deixa de ser preciso: o `deploy.yml` sincroniza tudo o que é
gerido pelo repositório. O `.env` e `secrets/` **nunca** são sincronizados — são estado do
servidor.

### 3. Provisionar (como `aos`, uma vez)

```bash
ssh aos@37.60.241.150 'bash /opt/aos/provision.sh'
```

Valida o `.env` variável a variável (uma em falta aborta **com o nome**), completa os rosters a
partir dos templates e gera o certificado TLS do edge com o SAN correcto para um IP.

### 4. Segredos e environment do GitHub

Em **Settings → Secrets and variables → Actions**:

| Secret | Valor |
|---|---|
| `DEPLOY_HOST` | `37.60.241.150` |
| `DEPLOY_USER` | `aos` |
| `DEPLOY_SSH_KEY` | conteúdo de `~/.ssh/id_ed25519_aos_deploy` (a **privada**) |
| `DEPLOY_KNOWN_HOSTS` | `ssh-keyscan -H 37.60.241.150` |
| `AOS_RELEASE_KEY` | *(opcional)* seed ed25519 da chave de release |

Variável opcional: `DEPLOY_EDGE_PORT` (default `8444`).

Em **Settings → Environments → `production`**, adiciona **Required reviewers**.

> Sem revisores, o `environment: production` do `deploy.yml` corre à mesma — o gate humano
> existe como configuração do repositório, não como YAML. É o único passo desta lista que o
> código não consegue impor por si.

`DEPLOY_KNOWN_HOSTS` é **obrigatório** e o job falha sem ele: aceitar a chave de host no primeiro
contacto (TOFU) é precisamente a janela de um MITM contra o único canal que escreve no servidor.

### 5. Primeiro release

```bash
git tag v0.1.0 && git push origin v0.1.0
```

Ou, sem construir nada, apontar o servidor a uma imagem já publicada: **Actions → deploy → Run
workflow** com a referência por digest.

---

## Operação

```bash
# estado
ssh aos@37.60.241.150 'docker compose -f /opt/aos/docker-compose.prod.yml --env-file /opt/aos/.env --env-file /opt/aos/image.env ps'

# logs do nó
ssh aos@37.60.241.150 'docker compose -f /opt/aos/docker-compose.prod.yml --env-file /opt/aos/.env --env-file /opt/aos/image.env logs -f aos'

# que versão está a servir
ssh aos@37.60.241.150 'cat /opt/aos/image.env'

# reverter para a anterior
ssh aos@37.60.241.150 'bash /opt/aos/rollback.sh'

# reverter para uma versão à escolha
ssh aos@37.60.241.150 'bash /opt/aos/rollback.sh ghcr.io/albinojimy/aos-node@sha256:...'
```

O `deploy.sh` **já reverte sozinho** quando o nó não fica saudável ou o smoke falha. O
`rollback.sh` é para a regressão descoberta horas depois, quando esse contexto já não existe.

Um deploy **não** toca no volume `aos-data`: o Event Store e o trilho WORM sobrevivem à troca de
imagem. É o que torna a reversão segura.

---

## Submeter um run — a receita que funciona, e porquê

Quatro parâmetros deste nó não são adivinháveis a partir dos exemplos genéricos do repositório.
Errar qualquer um devolve uma recusa correcta mas opaca, por isso ficam aqui fixados:

> ⚠️ **Esta receita mudou com `AOS_MODE=production`.** Os headers `X-Aos-Reader`/`X-Aos-Board`
> deixaram de autorizar — hoje devolvem `403`. O que vale é a versão abaixo. A anterior fica
> descrita em §"O que o corte para produção mudou", porque a diferença explica-se melhor a par.

> 🔑 **A raiz da delegação já não se declara por *flag*.** O `--human human:alice` abaixo produz
> `auth_method: manual` — sobrevive porque há caminhos sem IdP (CI, dev), mas **não é o caminho
> de produção**. Em produção o humano autentica-se no browser e a autenticação fica **ligada a
> esta delegação concreta**:
>
> ```powershell
> powershell -ExecutionPolicy Bypass -File deploy\server\get-id-token.ps1 `
>   -Cunhar agt-teste-01 -Caps 'model:invoke,cap:fs.read' -Ttl 45m -Submeter
> ```
>
> São **dois logins**, e não é atrito por descuido: o primeiro autoriza a delegação (audiência
> `aos-issuer`), o segundo chama a API (audiência `aos-node`). Ver §"O que continua por fechar",
> ponto 10.

```bash
# 1. Cunhar a credencial NHI (na tua máquina — a issuer.key nunca vai para o servidor).
#    É quem o RUN age em nome de. NÃO é o que autentica a chamada.
cd packages/cmd/aos-issuer
NHI=$(go run . mint --key-file ../../../deploy/server/secrets-local/issuer.key \
  --issuer iss:aos-issuer --human human:alice --agent agt-teste-01 --class agent-worker \
  --caps 'model:invoke,cap:fs.read' --ttl 45m | tr -d '\r\n')

# 2. Obter um token do IdP. É quem CHAMA a API. Token NOVO a cada chamada — ver o aviso do jti.
tok() { curl -s --cacert deploy/server/secrets-local/internal-ca/ca.crt \
  -X POST https://aos.elysiumii.site:9443/realms/aos/protocol/openid-connect/token \
  -d grant_type=client_credentials -d client_id=aos-reader \
  --data-urlencode "client_secret=$READER_SECRET" \
  | python -c 'import sys,json; print(json.load(sys.stdin)["access_token"])'; }

# 3. Submeter, pelo nome público e com TLS validado (sem -k)
RID="run-$(date +%s)"
curl -s -X POST https://aos.elysiumii.site:8444/runs \
  -H "Authorization: Bearer $(tok)" -H 'Content-Type: application/json' \
  -d "{\"run_id\":\"$RID\",\"objective\":\"Le o documento 'notes' com a tool doc_read.\",\
\"principal_nhi\":\"agt-teste-01\",\"credential\":\"$NHI\",\"scope\":[\"cap:fs.read\"]}"

curl -s "https://aos.elysiumii.site:8444/runs/$RID" -H "Authorization: Bearer $(tok)"
```

**Duas credenciais, e confundi-las custa tempo.** O `Bearer` é o token do IdP — quem *chama*. O
NHI vai no campo `credential` do corpo — quem o *run* age em nome de. Pôr o NHI no `Authorization`
dá `403`; trocar a ordem dá uma recusa que parece de escopo e é de autenticação.

> ⚠️ **Um token por chamada.** O nó recusa reutilização de `jti` (anti-replay). O token vale 5
> minutos, mas **não vale duas vezes** — daí `$(tok)` ser uma função invocada em cada `curl`, e
> não uma variável guardada. Reutilizar dá `403` sem explicação melhor.

O `--cacert` é preciso porque o certificado do IdP vem da **CA interna**, não de uma pública. O do
nó (`:8444`) é Let's Encrypt real e valida sem nada.

Porque é que cada parâmetro tem de ser assim:

- **`--caps` tem de incluir `model:invoke`.** O `nodeModelAuthority` concede `model:invoke` a
  qualquer principal verificado, e o estágio `auth-principal` RECONCILIA essa concessão com o
  escopo SELADO no token (menor privilégio: `utilizador ∩ classe ∩ token.Scope`). Um token que
  sele só `cap:fs.read` produz uma intersecção VAZIA e é negado com
  `authn: autoridade efectiva excede o escopo selado no token` — que soa a escopo a mais e é
  escopo a menos. É o corte duro de AOS-278; o contrato está fixado em
  `aos278_model_identity_test.go`.
- **`--caps` tem de incluir também a capability da TOOL** (`cap:fs.read` para `doc_read`, ver
  `model-tools/tools.json`). O ScopeGate de AOS-071 intersecta o token com o `authority.json`, e
  `human:alice` lá tem `cap:fs.read` + `cap:http.post`.
- **O `board` já não é um header.** Vem do claim `board` do token, e tem de constar de
  `AOS_BOARD_REGIONS` (`board:prod=eu-west`). Um board que não resolva para região NEGA
  fail-closed (D7/AOS-094) — a regra não mudou, só a fonte.
- **O leitor já não é um header.** Vem do `sub` do mesmo payload assinado.

> ⚠️ Os `demo-*.sh` de `deploy/node/dev-hardened/` cunham SEM `model:invoke` e não enviam
> credencial de leitura nenhuma. Foram escritos antes de AOS-278 e antes de este nó ter soberania
> composta; **não os uses como referência para este servidor** — falham aqui, e falham por razão
> legítima.

---

## Operar o plano de controlo

Quatro rotas mudam o curso de um run em execução, e **nenhuma** aceita o token do IdP: são
autoridade **sobre** o nó, não autoridade dentro de um run, e autenticam-se por **assinatura
ed25519 por-sinal** feita no dispositivo do operador. O nó nunca vê chave privada nenhuma.

| Rota | Quem assina | Chave |
|---|---|---|
| `POST /runs/{id}/pause` · `/steer` | operador | `secrets-local/operator.seed` |
| `POST /runs/{id}/exhaustion` | operador | idem |
| `POST /runs/{id}/approve` | **dois** aprovadores distintos | `secrets-local/approver-{a,b}.seed` |

### A codificação assinada, e o ataque que ela fecha

Todos os tuplos usam **length-prefix** — `uint64` big-endian com o comprimento, seguido do campo —
e nunca separadores. A razão está no código e vale a pena repetir: um separador de byte único
**não é injectivo**, porque o byte separador pode ocorrer *dentro* de um campo variável (um nonce
binário contém `0x00` em ~6% dos casos). Com separadores, quem capturasse um sinal poderia
**deslizar a fronteira** entre dois campos, obtendo um tuplo logicamente diferente — nonce novo, e
por isso invisível ao anti-replay — com a **mesma** sequência de bytes e a **mesma** assinatura
válida. O comprimento fixa cada fronteira e elimina a ambiguidade.

**`pause` / `steer`:**

```
lp(run_id) ‖ lp(kind) ‖ lp(payload) ‖ lp(nonce) ‖ u64be(issued_at.UnixNano())
```

`kind` é `"pause"` ou `"steer"`; `payload` é vazio no pause e a **correcção em bytes crus** no
steer (que viaja em base64 no campo `payload` do corpo).

**Decisão de exaustão** — `kind = "exhaustion_decision"`, e o payload é ele próprio um tuplo
prefixado, com etiqueta de domínio para não colidir com outro sinal do mesmo autenticador:

```
payload = lp("aos263:exhaustion-decision") ‖ lp(decisão) ‖ lp(step_id)
```

**Perna de aprovação** *four-eyes* — domínio próprio e o `preview` (digest do efeito exibido,
*what you see is what you sign*):

```
lp("aos.integration.foureyes.v1") ‖ lp(request_id) ‖ lp(preview)
  ‖ lp([risk_class, dual_control]) ‖ lp(approver) ‖ lp(session)
  ‖ lp(credential) ‖ lp(challenge)
```

`risk_class` é um byte: **`0` = danger** (o valor-zero, fail-closed), `1` = safe, `2` = gray.

### O corpo de fio

```jsonc
// pause
{"emitter":{"id":"ops:prod","signature":"<b64>","nonce":"<b64>","issued_at":"<RFC3339>"}}
// steer — o mesmo emitter, mais a correcção
{"emitter":{…},"payload":"<b64 da correcção>"}
// decisão de exaustão
{"decision":"continue","step_id":"<o da pending_exhaustion>","emitter":{…}}
// aprovação
{"request":{"request_id":"…","preview":"<b64>","risk_class":0,"dual_control_required":true},
 "legs":[{"approver":"human:alice","session":"…","credential":"…",
          "challenge":"<b64>","signature":"<b64>"}, …]}
```

### O que estes canais garantem, verificado

- **Anti-replay durável, independente da criptografia.** Um sinal **re-assinado** com `issued_at`
  novo mas o **mesmo nonce** é recusado com `403`. A assinatura era válida e fresca; o nonce
  estava consumido. Sobrevive a restart.
- **O alvo está preso à assinatura.** Uma assinatura válida para *outro* `run_id` → `403`.
- **A decisão está presa à assinatura.** Assinar `abort` e enviar `continue` → `403`.
- **Duplo controlo é mesmo duplo.** Uma perna só → `403`; duas, de aprovadores distintos → `200`
  com um grant que **expira**.

### Sequências que a API impõe

**Exaustão de orçamento.** Um run que cruza o limiar suspende-se em `waiting_on_human` com
`pending_exhaustion`. A partir daí, `POST /resume` devolve **`409`** até a pergunta ser
respondida — e `"resume"` **não é** uma decisão aceite em `/exhaustion` (a rota di-lo). A ordem é:
decidir `continue` → depois `resume`.

**Aprovação escalada.** Idêntico: aprovar **autoriza**, não re-hospeda. O run só avança com um
`POST /resume` explícito.

Em ambos os casos o `resume` exige uma **credencial NHI fresca** — *"a original não é
persistida"*. É deliberado: re-autentica-se para retomar.

> ✅ **A emissão de challenges está LIGADA** (`AOS_CHALLENGE_ISSUANCE=1`): `POST /runs/{id}/challenge` devolve um challenge por `(pedido, aprovador)` com TTL de 5 min, e cada perna passa a exigi-lo. Antes devolvia
> `501` — *"frescura por-cerimónia dormente; defina `AOS_CHALLENGE_ISSUANCE=1`"*. Sem ela, o
> anti-replay **por-cerimónia** da aprovação não está armado (o anti-replay por-nonce dos sinais
> de operador **está**, e é outro mecanismo). Ligar exige decidir que o operador consegue pedir
> um challenge antes de cada cerimónia.

---

## TLS real — instalado, via cert-manager do cluster

O nó serve `https://aos.elysiumii.site:8444` com certificado **Let's Encrypt válido**, cadeia
verificada sem `-k`. O `provision.sh` continua a gerar um *self-signed* como ponto de partida —
ele é substituído pelo real assim que o `sync-tls.sh` corre.

**Porquê pelo cluster e não por `certbot`:** `certbot --standalone` precisa da porta 80, que
pertence ao `ingress-nginx` — o container nem arranca. E o `letsencrypt-prod-dns` (Cloudflare,
DNS-01) tem o selector limitado à zona **`elysiumii.com`**, que não casa com `elysiumii.site`.
O que funciona é **HTTP-01 pelo `letsencrypt-prod`**, o mesmo caminho que renova `api.` e
`longhorn.` neste cluster.

O `Certificate` vive em `default/aos-node-tls`:

```bash
kubectl get certificate -n default aos-node-tls
```

### A ponte, e porque ela é a parte que interessa

O cert-manager renova **dentro** do cluster. O edge é um contentor Docker **fora** dele, que lê
`/opt/aos/secrets/tls`. Sem ponte, o certificado renovava no Kubernetes e o nó servia o antigo
até expirar — **pior do que self-signed, porque expira em silêncio**.

Essa ponte é o [`sync-tls.sh`](sync-tls.sh), agendado por systemd
([`systemd/`](systemd/)):

```bash
install -m 755 sync-tls.sh /opt/aos/sync-tls.sh
install -m 644 systemd/aos-tls-sync.* /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now aos-tls-sync.timer
```

É idempotente (compara *fingerprints*, só recarrega o nginx quando o material muda de facto),
recusa escrever um par cert/chave que não corresponda, e escreve atomicamente.

**Fail-loud, e por duas vias distintas:**

1. O certificado **em vigor no edge** expira dentro de 15 dias → falha.
2. **A ponte partiu-se** — não consegue ler o secret — → falha **mesmo com 89 dias de folga**.

A segunda é a que importa e custou um teste para descobrir: sob systemd o serviço não herda o
ambiente do root, o `kubectl` não encontrava o `~/.kube/config`, e a sincronização falhava **em
silêncio**. À mão funcionava; pelo timer não. Por isso o `KUBECONFIG` é explícito na unidade
**e** detectado no script, e por isso uma leitura falhada é falha e não aviso — esperar pelos
15 dias finais seria descobrir tarde de mais.

Ver o estado:

```bash
systemctl list-timers aos-tls-sync.timer
journalctl -u aos-tls-sync.service -n 20
```

> ⚠️ Isto acopla o TLS do nó à saúde do cluster — o mesmo cluster que deixou um certificado
> expirar durante um mês. É o compromisso aceite em troca de renovação automática, e as duas
> guardas acima existem precisamente para que a falha seja ruidosa em vez de silenciosa.

> O acesso por **IP** continua a falhar a validação, e correctamente: o certificado é para o
> nome. Usa `https://aos.elysiumii.site:8444`.

---

## `AOS_MODE=production` — ligado

O nó corre em modo produção. Não foi um interruptor: são **três** portas fail-closed, e o
arranque aborta em qualquer uma. Foram enumeradas empiricamente — arrancando a imagem num
contentor descartável e acrescentando um requisito de cada vez até passar — e não por leitura do
código, que é como a terceira tinha passado despercebida.

| Porta | Exige | Servida por |
|---|---|---|
| Identidade endurecida | `AOS_ISSUER_PUBKEY` | já estava |
| Soberania de leitura | `AOS_BOARD_REGIONS` | já estava |
| TLS do ingresso | `AOS_TLS_EXTERNAL_TERMINATION=1` | já estava (edge) |
| **Credencial forte** | `AOS_SOVEREIGN_OIDC_ISSUER` + `_AUDIENCE` | **Keycloak** (`idp`, `idp-db`) |
| **Custódia da KEK** | `AOS_DSAR_VAULT_ADDR` + `_TOKEN_PATH` | **Vault** (`vault`, `vault-unseal`) |
| **Credencial do modelo** | `AOS_MODEL_API_KEY_PATH` | master key do LiteLLM |

As duas últimas não constavam da versão anterior deste documento. A da KEK nunca tinha sido
nomeada; a do modelo **nasceu** quando o gateway foi ligado — antes disso `AOS_MODEL_ENDPOINT`
estava vazia e a porta não existia. Um documento sobre pré-requisitos envelhece com a
configuração, e este envelheceu em menos de um dia.

### O que o corte para produção mudou

**A via por headers morreu.** `X-Aos-Reader`/`X-Aos-Board` já não autorizam: devolvem `403`. O
board passa a vir do claim `board` de um token verificado e o leitor do `sub` do mesmo payload
assinado — imune a forja por header, que era o ponto.

**E não guarda só as leituras.** Guarda a **submissão** também:

```go
// api.go, handleSubmit
if h.readGov != nil {
    submitter, ok := h.readGov.authorize(r)
    if !ok { writeError(w, http.StatusForbidden, "nao autorizado"); return }
```

Sem uma identidade no IdP, o nó em produção não aceita **nada** — nem leituras nem runs novos.
Não é degradação parcial: é a API fechada. Provisiona a identidade **antes** de ligar o modo,
não depois.

**Anti-replay por `jti`.** Um token não vale duas vezes, mesmo dentro dos 5 minutos de validade.
Obtém-se um por chamada.

### Porque é que a KEK justifica um Vault

Com substrato durável, a KEK por-titular vivia no vault **em memória** de referência. Um restart
tornaria o conteúdo cifrado dos runs — texto do modelo, resultados de tools — permanentemente
indecifrável. Não é perda de cache: é apagamento silencioso de dados que o *legal hold* promete
preservar. O motor Transit do Vault mantém as KEKs fora do processo, e o `/dsar/erase` destrói-as
lá (crypto-shred real).

O token do nó **não é o root**: é um token periódico com uma política que só permite as operações
Transit sobre `aos-kek-*`. O root fica em `secrets/vault-init.json`, para administração.

### O selo do Vault, e o que ele protege mesmo

Storage `file` significa que o Vault sobe **selado** — e um Vault selado é um nó que não decifra.
O serviço `vault-unseal` destrava-o automaticamente, o que exige que a chave de unseal esteja
acessível à máquina.

Consequência, dita sem rodeios: **o selo protege contra roubo do volume, não contra compromisso
desta máquina.** Quem tiver root aqui destrava o Vault. A alternativa séria é auto-unseal por
KMS/HSM externo, que este servidor não tem; a outra é unseal manual, que troca esta exposição por
indisponibilidade — um reboot não vigiado deixaria o nó sem decifrar até alguém agir. Escolheu-se
a disponibilidade.

O watchdog é um serviço do compose e não uma unidade systemd de propósito: não exige root para
instalar, e cobre mais casos do que um `After=docker.service` — se o Vault selar por qualquer
razão, ele destrava. Verificado selando-o à mão.

### Identidade

Ver [`keycloak/README.md`](keycloak/README.md). Dois clientes:

- **`aos-reader`** — cliente confidencial com *service account*. É o que está em uso. O segredo é
  gerado pelo Keycloak e vive em `secrets/reader-client-secret` (0400).
- **`aos-node`** — cliente público, **código de autorização + PKCE S256**, para leitores
  **humanos**, cada um com o seu atributo `board`. O humano autentica-se no browser com
  [`get-id-token.ps1`](get-id-token.ps1); a password nunca passa pela linha de comandos.

A distinção importa: um service account colapsa "quem lê" numa identidade de máquina. A soberania
**por-leitor** — que é o argumento de todo o mecanismo — só é real quando existirem identidades
humanas distintas. **Já existem:** o WORM tem, na mesma cadeia, leituras do service account e uma
de um humano com o seu próprio `sub` (ver §"O que continua por fechar", ponto 7).

---

## Backup

`backup.sh`, por cron do utilizador `aos` (03:17 diário, sem root), com 14 cópias em rotação.

### As três peças só valem juntas

Copiar o `events.wal` sozinho produz um ficheiro **inútil**. O conteúdo dos runs está cifrado por
KEK-por-titular e as KEKs vivem no Vault; sem `vault-data` o restauro dá metadados e *ciphertext*
indecifrável. E sem `secrets/vault-init.json` nem se destrava o Vault restaurado. O backup leva as
três, mais o `pg_dump` do IdP.

> O Postgres é copiado por **`pg_dump`, nunca a ficheiro**. Um `tar` do `PGDATA` em execução
> apanha páginas a meio de escrita e restaura corrompido — em silêncio, que é pior do que falhar.

### E é por isso que é cifrado

Juntas, aquelas peças valem exactamente o mesmo que a máquina: quem tiver o backup tem o conteúdo,
as chaves que o decifram e o material que destrava o Vault. Uma cópia em claro anularia a cifra em
repouso — e é ao **sair do host** que ela fica exposta.

Cifra-se para um certificado cuja privada **nunca esteve no servidor**
(`secrets-local/backup-key/`, ao lado da `issuer.key`). Duas consequências, ambas deliberadas:

- um atacante com root no servidor **não lê** os backups que a própria máquina produz;
- **perder a chave privada é perder os backups.** Não há recuperação.

### O que protege, e o que não

Ficheiros no mesmo disco protegem contra apagamento do volume, corrupção da aplicação e um deploy
mau. **Não** protegem contra perda do host nem falha de disco — e essa é a razão de existirem
cifrados: para poderem sair daqui.

A recolha para fora **está automatizada** na máquina do operador — é o único passo que cobre a
perda do host:

```powershell
# tarefa AOS-RecolherBackups: 04:30 diário, StartWhenAvailable
powershell -ExecutionPolicy Bypass -File deploy\server\pull-backups.ps1   # à mão, quando precisar
Get-ScheduledTaskInfo -TaskName AOS-RecolherBackups                       # última/próxima execução
```

Destino `%USERPROFILE%\aos-backups`, rotação local de 30 (independente das 14 do servidor, porque
esta é a única que sobrevive à perda da máquina remota). `StartWhenAvailable` faz uma execução
perdida ser recuperada no arranque seguinte em vez de ser saltada.

> ⚠️ **O que isto ainda não cobre:** se a máquina do operador ficar dias desligada, a cópia
> envelhece e **ninguém avisa** — não há alerta de recência. Um destino sempre ligado (bucket
> S3-compatível ou outro host) removeria a dependência; ficou por decidir.

> 🔐 As chaves em `secrets-local/` estavam legíveis por `BUILTIN\Utilizadores` e **modificáveis**
> por `Utilizadores Autenticados` — o OpenSSH do Windows recusou a `deploy_key` por isso, e foi
> assim que apareceu. Todo o directório passou a ACE único do dono. Vale a pena reter: é onde
> vivem a `issuer.key`, as seeds de operador e aprovadores, a CA interna e a chave dos backups.

### Restaurar

```bash
openssl smime -decrypt -binary -inform DER -in aos-<stamp>.tar.gz.enc \
  -inkey deploy/server/secrets-local/backup-key/backup.key -out bundle.tar.gz
tar xzf bundle.tar.gz          # MANIFEST, idp-db.sql, volumes.tar.gz, config.tar.gz
```

Este ciclo foi **exercitado**, não presumido: recolhido, decifrado com a privada local e o
conteúdo conferido — `events.wal`, `worm.wal`, o `pg_dump` com 87 tabelas, e a chave Transit
`aos-kek-…` no storage do Vault. Sem essa última, o resto não serviria de nada.

O `backup.sh` verifica cada artefacto que produz: PKCS#7 íntegro **e** do tipo `envelopedData` —
que confirma que o conteúdo está mesmo cifrado, e não só que o ficheiro é bem-formado.

---

## O que está verificado, e por que meio

Um teste ponta-a-ponta que só percorre o caminho feliz confirma que a coisa funciona; não confirma
que os controlos existem. Estas verificações foram feitas contra o servidor real e cada uma tem
um **controlo** — sem ele, um resultado positivo não distingue "o controlo actuou" de "não havia
nada a controlar".

| Afirmação | Como foi provada | Controlo |
|---|---|---|
| O sandbox é fronteira de **kernel** | Diagnóstico corrido dentro de um bundle idêntico ao de produção: `Linux version 4.19.0-gvisor`, só a interface `lo`, `1.1.1.1:53 → network is unreachable`, raiz `9p` read-only, ficheiros do host inexistentes | leitura de `/seed/notes` funciona no mesmo bundle |
| Capability não selada é **negada na execução** | WORM: `Decision:deny`, `Code:E_DENIED_BY_HOOK`, `DeniedBy:identity`, `Reason:E_OUT_OF_SCOPE` | 71 `allow` e **1** `deny` em todo o trilho — a negação é única e atribuível |
| Claims **sobrepõem-se** a headers | Token válido + `X-Aos-Board: board:inexistente` → `200`. Se o header fosse honrado, não resolveria para região e negaria | mesma leitura só com token → `200` |
| Recusa **cross-region** | Run residente em `eu-west`: leitor `eu-west` → `200`, leitor `us-east` → `404`. Dois tokens válidos; a única variável é a região | ambos os boards resolvíveis, ambos os tokens verificados |
| Conteúdo **cifrado em repouso** | Cinco frases distintas do run: **zero** ocorrências em claro em `events.wal` | metadados (transições de estado, `run_id`) legíveis ao lado — não é o ficheiro a ser opaco |
| **Crypto-shred** real | `/dsar/erase` → KEK desaparece do Vault, `reconstrucao indisponivel` | run de **outro** titular reconstrói na íntegra, `tool_outputs` intactos |
| A cadeia **sobrevive** ao shred | Reinício após o apagamento: 55 partições re-encadeadas e verificadas | uma cadeia adulterada abortaria o arranque fail-closed |
| **Anti-replay** por `jti` | Mesmo token, três leituras ao mesmo run: `200` → `404` → `404` | ⚠️ inferência de comportamento, não linha de log |

### Duas armadilhas de diagnóstico

**A recusa por replay devolve `404`, não `403`.** É o 404 uniforme e não-enumerável: negar e "não
existe" são deliberadamente indistinguíveis, para ninguém poder sondar run IDs válidos. O efeito
prático é que um token reutilizado faz o run **parecer ter desaparecido**, sem mensagem nenhuma.
Testar replay contra um run inexistente é, por isso, inconclusivo por construção.

**Um `grep` ao `events.wal` não encontra a negação** — porque o conteúdo está cifrado. O registo
autoritativo das decisões de governação está no `worm.wal`, cujos metadados são legíveis por
desenho. Procurar no sítio errado dá zero resultados e parece ausência de controlo.

---

## O que esta configuração ainda não fecha

Nomeado, não escondido:

1. **Atestação não anexada no registry.** O envelope DSSE é um artefacto separado (vai para a
   GitHub Release); um `docker pull` não o traz. Sem OCI *referrers*, o servidor **não** verifica
   a assinatura antes de correr — verifica o **digest**, que o release fixou. Residual já
   declarado em ADR-017.
2. **Roster de release vazio.** `../node/release-pubkeys.json` tem `keys: []`, pelo que a
   verificação recusa tudo por omissão. Sem o secret `AOS_RELEASE_KEY` a entrega segue
   declaradamente **não-assinada** (o workflow emite o aviso e a Release di-lo). Com a chave
   provisionada, a verificação passa a bloqueante.
3. **Nó único.** Uma máquina, sem réplica. O DR de EPIC-10 (Event Store replicado, failover)
   não está aqui — o que existe é durabilidade local mais reversão por digest.
4. **O host não tem firewall, e estes scripts não lha põem.** Ver §"O servidor real", ponto 2.
   A contenção do *cleartext* do nó é a topologia (`expose` em vez de `ports`), que não depende
   de firewall nenhuma — mas o resto do host continua exposto, e isso não é problema que um
   script de deploy possa resolver sem risco.
5. **O nó partilha 8 vCPU com um control-plane saturado.** O `kube-apiserver` sozinho consome
   ~95% de um core e a *load average* observada foi 17–36 numa máquina de 8. O `mem_limit` de
   1 GB protege os vizinhos do nó, mas não protege o nó dos vizinhos: sob contenção, espera
   latência de mediação acima dos alvos de `tecnica/10`. Os serviços acrescentados para o modo
   produção têm `cpus:` declarado; os mais antigos deste ficheiro **não têm** — dívida conhecida,
   e a razão pela qual o arranque da JVM do Keycloak leva 1–2 minutos aqui.
6. **A cópia off-host depende de a máquina do operador estar ligada — e agora AVISA quando algo
   envelhece.** A tarefa `AOS-RecolherBackups` (Windows, 04:30 diário, `StartWhenAvailable`) puxa
   os `.enc` para `%USERPROFILE%\aos-backups` via [`pull-backups.ps1`](pull-backups.ps1), com
   rotação própria de 30. Verificado ponta-a-ponta: recolhida, **decifrada** com a privada local,
   e a chave Transit confirmada lá dentro.

   **O que a versão anterior escondia.** Ela era idempotente por nome e terminava com
   `FEITO — 0 novo(s)`, código de saída zero. Se o cron do servidor morresse, diria exactamente
   isso **para sempre** — a mensagem de um sistema saudável e a de um que não produz backups há
   semanas eram a mesma. Um sucesso vacuoso.

   Passa a verificar a idade dos **dois** lados: a cópia local (apanha "a máquina esteve
   desligada") e o backup **remoto** (apanha "o cron morreu", que é o caso que ninguém notaria,
   porque a recolha continua a correr bem). Alerta por três canais deliberadamente redundantes —
   código de saída (visível no *Last Run Result* do Agendador), `ESTADO.txt` no destino, e o
   Registo de Eventos, que degrada em silêncio se a tarefa não correr elevada.

   Verificado a falhar quando deve, que é o que torna o "OK" informativo: tecto de 1h → saída
   `3` com os dois alertas nomeados; servidor inalcançável → saída `2` **com `ESTADO.txt`
   escrito**; normal → `0`.

   > O controlo do servidor inalcançável apanhou um defeito real: com `$ErrorActionPreference
   > = 'Stop'`, o PowerShell trata a escrita do `ssh` para *stderr* como erro **terminante**, e o
   > script **morria** antes de alertar — sem `ESTADO.txt`, com código `1` de crash. O único
   > cenário em que o aviso interessa era o único em que não saía.

   **Residual:** a máquina desligada não alerta enquanto está desligada — nenhum processo local
   pode. O que deixou de existir é a cópia velha **silenciosa** com a máquina ligada.
   de **máquina**: um humano (`jimy`, `board:prod`) autenticou-se por **código de autorização +
   PKCE S256** no browser e leu um run em produção. A prova não é o `200` — é o WORM. Na mesma
   cadeia de hash da partição `gov.read/run-humano-1787005443`:

   | `AuditSeq` | `Principal.NHIID` | quem |
   |---|---|---|
   | 1, 2 | `91a30a69-781d-448e-90c9-1de9f5e7bcbe` | service account `aos-reader` |
   | **3** | **`a2b5947c-09e2-40bc-8c58-a7f4b0bbdfef`** | **`jimy`**, UUID do Keycloak |

   Mesmo run, mesma `read:outcome`, mesmas obrigações (`gov.read.board: board:prod`,
   `gov.read.residency: eu-west`), `PrevHash` a encadear. **A única variável é o principal** — o
   controlo está embutido na prova, não ao lado dela.

   O *password grant* foi **fechado** a seguir (`directAccessGrantsEnabled: false`), e verificado:
   `400 unauthorized_client / Client not allowed for direct access grants`, com o fluxo de código
   a continuar a responder `200`. Ficou ligado só até o caminho novo estar provado ponta-a-ponta,
   para não existir uma janela sem caminho nenhum.

   **O que fica por exercer:** há **um só board** (`AOS_BOARD_REGIONS=board:prod=eu-west`). A
   recusa cross-region está provada (ver §"Provar a recusa cross-region"), mas com um board só não
   há como voltar a exercê-la com leitores humanos — para isso é preciso uma segunda região no
   mapa e um segundo leitor.
10. ~~A raiz humana da cadeia de delegação é auto-declarada.~~ **✅ AUTORIZADA, não só autenticada.**
   Era `auth_method: manual` — quem detinha a `issuer.key` declarava o humano por *flag*, e nada
   provava que ele tivesse autorizado o que quer que fosse.

   **A correcção óbvia não bastava.** Trocar `--human` por `--assertion` sobe de *"declarado"*
   para *"esteve presente"*, e não para *"autorizou isto"*: um ID-token não diz nada sobre
   `--agent`, `--class`, `--caps` ou `--ttl`. Quem detivesse a chave **mais** um token fresco do
   humano cunhava *qualquer* NHI enraizada nele. O defeito sobreviveria com uma etiqueta melhor.

   **O que fecha de facto** é o `nonce` do fluxo de código a transportar o **digest da
   delegação**: o IdP ecoa-o no ID-token, e o `aos-issuer` **calcula o esperado a partir das
   flags que está a cunhar** — nunca o aceita por parâmetro, senão far-se-ia coincidir com o que
   quer que se estivesse a cunhar. O digest é *length-prefixed* (molde de
   [`hitl/encode.go`](../../packages/control-plane/governance/hitl/encode.go)) porque com um
   separador simples `(agent="a", class="bc")` e `(agent="ab", class="c")` dariam os mesmos
   bytes, e quem controlasse um campo deslizava a fronteira para o seguinte.

   O rótulo passa a distinguir os dois estados, e o fraco continua a existir porque nem toda a
   autenticação tem *nonce* — mas fica **escrito no registo**:

   | `auth_method` | o que significa |
   |---|---|
   | `manual` | declarado por *flag*. Nada prova nada. |
   | `oidc:<iss>` | o humano **esteve presente** (`--assertion-unbound`) |
   | `oidc-bound:<iss>` | o humano **autorizou esta delegação** |

   **Verificado em produção:** `auth_method = oidc-bound:…`, raiz
   `human:a2b5947c-09e2-40bc-8c58-a7f4b0bbdfef` — o `sub` do IdP, não um nome escrito à mão.
   O controlo que torna isto não-vacuoso está em
   [`delegationbinding_test.go`](../../packages/cmd/aos-issuer/delegationbinding_test.go): um
   token do **mesmo humano**, com assinatura igualmente válida, emitido para uma delegação com
   uma capability a mais é **recusado**. Sem esse caso, um token que passa seria compatível com
   "o verificador aceita tudo".

   **Audiências separadas.** O cliente `aos-issuer` existe para que um ID-token obtido para
   **ler um run** não sirva para **cunhar uma raiz de delegação**. Verificado: recusa
   *password grant* (`400 unauthorized_client`), e o fluxo de código responde `200` em ambos.

   **O que fica por fechar, e é preciso dizê-lo:**

   - **O nó não verifica nada disto — confia no issuer.** A âncora do nó é a pubkey do issuer, e
     `auth_method` é uma afirmação *dele*. Com o issuer comprometido, `oidc-bound:` é tão
     forjável como `manual`. A garantia vive no issuer, e o valor de auditoria está limitado
     pela integridade dele.
   - **O `RequireJTI` aqui seria um placebo.** O armazém anti-replay é um campo do `Verifier`, e
     o `aos-issuer` é um processo de vida curta: o mapa nasce vazio a cada invocação. Pareceria
     anti-replay e não seria nenhum. O que ficou foi o `MaxAge` de 5 min — este era o **mais
     fraco** dos três verificadores OIDC do sistema, o único sem tecto de idade.
   - **Um run com uma NHI assim JÁ passou pelo WORM (2026-08-20).** Submetido por
     `get-id-token.ps1 -Cunhar agt-prova-96 -Submeter`, o caminho exacto que este ponto nomeava;
     `POST /autonomy/simular` passou depois a contar `avaliados: 3` onde antes contava `0`, o que
     confirma que as tool calls do run foram seladas.

     **O que fica por confirmar, e é uma linha:** o script imprime `auth_method` e distingue
     `oidc-bound:` (a verde — a delegação ficou **ligada** à autenticação) de qualquer outro valor
     (a amarelo — o registo diz «esteve presente», não «autorizou isto»). Essa linha não foi
     registada. Enquanto não o for, o que está provado é que a cadeia **passa** pelo WORM, não que
     ficou **enraizada** num `sub` verificado — e a diferença entre as duas é precisamente o que
     este ponto existia para medir.
8. **A verificação ancorada do WORM não corre — mas já não é trabalho de desenho.** Sem
   `AOS_WORM_TRUST_ANCHOR` + `AOS_WORM_CHECKPOINT_FILE` + `AOS_WORM_EXPECTED_HEADS_FILE`, fica só
   a re-verificação de hash-chain: apanha mutação, remoção e encadeamento quebrado, mas **não**
   truncatura do tail nem reescrita desde a génese. O banner de arranque di-lo em cada boot.

   **As duas razões pelas quais parei aqui foram RESOLVIDAS a 2026-08-20.** Este ponto dizia que
   fechar isto exigia «um selador que emite um checkpoint por partição e um nó que os verifica em
   conjunto», e classificava-o como trabalho de desenho. É agora o que existe:

   - **(a) O selador existe.** `aos-issuer worm-seal` (PR #88) percorre o store, re-encadeia
     **antes** de assinar, e emite um `audit.Checkpoint` **por partição**. Os pisos de frescura
     saem por `--heads`, num ficheiro **à parte** — de propósito: se viajassem com os
     checkpoints, quem trocasse o ficheiro trocava os dois, e o piso deixaria de morder no
     rollback de checkpoint que existe para fechar.
   - **(b) O nó verifica-os em conjunto.** `WormAnchor.Checkpoints` é `[]audit.Checkpoint` e
     `ExpectedHeads` é `map[string]uint64`. A forma singular antiga é recusada **em voz alta**
     (`ErrWormExpectedHeadObsoleta`) em vez de degradar para «sem âncora» com a env lá e o
     operador convencido.

   **A cobertura nunca será completa, e isso é do desenho.** As partições nascem por run
   (`run-<id>`, `ingestion:<id>`, `gov.residency/<id>`), pelo que o run seguinte cria uma partição
   que nenhuma selagem anterior cobre. A propriedade honesta é **«ancorado até ao último selo;
   depois disso, só re-encadeamento»** — e é isso que o banner declara, com o número. Selar mais
   vezes **encolhe a janela; não a fecha**. Quem espere «o WORM está ancorado» sem qualificação
   vai ler mal o banner.

   **O que falta é operacional, e é isto — por ordem:**

   1. **Gerar a chave do selador**, na máquina do operador, e **nunca** a pôr no servidor. É a
      mesma regra do molde AOS-156 que já governa a `issuer.key`: a chave assina **fora** do nó;
      o nó só recebe a pública, em hex, na `AOS_WORM_TRUST_ANCHOR`.
   2. **Selar contra a cópia do backup**, não contra o servidor vivo. `audit.Signer.Seal` precisa
      do *store*, e o store vive onde a chave não pode estar. `pull-backups.ps1` traz a cópia
      off-host; é essa que se sela.
   3. **Montar os dois ficheiros e a pública**, e só então definir as três env. **As três em
      conjunto ou nenhuma** — definir algumas **aborta** o arranque (`ErrWormAnchorIncomplete`)
      em vez de degradar.
   4. **Decidir a cadência**, que é a única decisão que sobra e não tem resposta certa: é o
      tamanho da janela não-ancorada que se aceita. Cada selagem nova tem de recusar selar sobre
      uma história divergente — `exigirContinuidade` corre a **mesma** `VerifyFromCheckpoint` que
      o nó corre no arranque — e tem de **avançar** o piso de frescura, senão o piso deixa de
      distinguir uma âncora fresca de uma antiga reapresentada.

   Nenhum destes quatro é código. O primeiro é custódia, o segundo é o ensaio de restauro que já
   está provado, o terceiro é configuração e o quarto é uma decisão de operação.

   **DECIDIDO a 2026-08-20: selagem DIÁRIA e automática** (`selar-worm.ps1`, tarefa agendada na
   máquina do operador). A janela não-ancorada passa a ser ≤ 24 h.

   > ⚠️ **O QUE ISSO CUSTA, e é maior do que parece à primeira.** Uma tarefa que sela sozinha é
   > uma tarefa que tem de alcançar **duas** chaves privadas sem ninguém presente: a do **selador**
   > (forja âncoras) e a do **backup** (`backup.key` — decifra **todas** as cópias de produção,
   > incluindo a base do IdP). O segundo não estava no enunciado quando a cadência foi escolhida, e
   > fica aqui porque a escolha foi feita sem ele.
   >
   > Quem comprometer a máquina do operador durante a janela diária leva as duas capacidades ao
   > mesmo tempo. Não é a mesma coisa que «a chave do selador corre sozinha».
   >
   > **Mitigação que reduz isto a metade, e não está feita:** puxar `worm.wal` directamente do
   > servidor por SSH (chave de deploy) em vez de o extrair do backup cifrado. A cópia continua
   > off-host — que é a condição que interessa — e a tarefa diária deixa de precisar da
   > `backup.key`. O custo é que o WORM viaja fora do envelope do backup, protegido só pelo
   > transporte.

   **O que o `selar-worm.ps1` já faz, e foi provado contra o WORM real de produção (120 partições,
   re-encadeadas sem erro):** decifra, extrai, sela **todas** as partições, escreve as duas metades
   em directórios separados, arquiva a âncora anterior, e apaga sempre os dados decifrados — também
   quando falha, que é quando alguém estaria distraído a ler o erro.

   **Dois defeitos apanhados a correr o ciclo DUAS vezes**, e nenhum apareceria numa só execução:

   1. o `Set-Content -Encoding utf8` do PowerShell 5.1 escreve **BOM**, e o `encoding/json` do Go
      recusa-o. A segunda selagem não conseguia ler o ficheiro que a primeira escrevera, e o **nó
      lê os mesmos ficheiros** — teria abortado no arranque com «invalid character 'ï'», que é
      fail-closed correcto e diagnóstico inútil. Corrigido nos dois lados: o script escreve sem
      BOM, e os três leitores (checkpoints e pisos no nó, `--anterior` no selador) retiram um BOM
      inicial — **um prefixo conhecido e mais nada**, para que normalizar não vire tolerar;
   2. o script anunciava «**1** partição ancorada» sobre um ficheiro com **120** — `@(...)` à volta
      de um `ConvertFrom-Json` não conta elementos em PowerShell 5.1. A âncora estava certa; a
      mensagem repetia exactamente a falha «1 em 108» que esta secção descrevia.

   ### O ciclo, como está montado hoje

   ```
   selar-worm.ps1 -PorSSH -Entregar
   ```

   | passo | o que faz | que chave usa |
   |---|---|---|
   | traz | `worm.wal` **vivo** do servidor, como root dentro de um contentor, entregando logo a posse | deploy |
   | verifica | re-encadeia o store **antes** de assinar, e exige **continuidade** com a âncora anterior | — |
   | sela | um checkpoint **por partição**; pisos em ficheiro separado | selador |
   | entrega | sobe os dois com nomes temporários e troca-os **lado a lado** | deploy |
   | limpa | apaga a cópia no servidor **e** confirma que desapareceu | deploy |

   **A `backup.key` não entra.** Foi essa a razão de existir o `-PorSSH`: uma tarefa que corre
   sozinha não deve alcançar a chave que decifra todas as cópias de produção.

   **Porque a entrega é atómica e não ordenada.** Não há ordem segura entre os dois ficheiros:
   entregar os checkpoints primeiro deixa as partições novas **com checkpoint e sem piso**
   (`ErrBadWormExpectedHead`); entregar os pisos primeiro deixa os checkpoints antigos **abaixo dos
   pisos novos** (`ErrCheckpointStale`). Ambas impedem o nó de arrancar. Por isso os dois sobem com
   nomes temporários e são renomeados num só comando. A janela residual — o intervalo entre dois
   `mv` — fica declarada: não é zero, e um arranque exactamente aí apanharia um par incoerente.
   Recupera-se correndo o ciclo outra vez.

   **Uma regra que só apareceu por correr os dois modos seguidos:** depois de selar do WORM vivo,
   selar de um backup **anterior** é um recuo, e a guarda recusa — com a mesma mensagem que
   significaria «alguém truncou o teu trilho». Escolha-se uma fonte e só se avance no tempo.

   ### A tarefa diária

   ```
   schtasks /create /tn "AOS selar WORM" /sc DAILY /st 03:30 ^
     /tr "powershell -NoProfile -ExecutionPolicy Bypass -File C:\Jimy\aos\deploy\server\selar-worm.ps1 -PorSSH -Entregar" ^
     /ru %USERNAME%
   ```

   O `schtasks` pede a password do Windows — é o Windows a pedi-la, e ela não passa por mais lado
   nenhum. Sem `/ru`, a tarefa só corre com sessão iniciada.

   ### Ligar a verificação — o último gesto, e o único irreversível sem outro deploy

   **Só depois de a entrega diária já estar a correr há dias.** No `.env` do servidor, as três
   **juntas**:

   ```
   AOS_WORM_TRUST_ANCHOR=<saída de `aos-issuer pubkey --key-file wormseal.key`>
   AOS_WORM_CHECKPOINT_FILE=/etc/aos/ancoras/checkpoints.json
   AOS_WORM_EXPECTED_HEADS_FILE=/etc/aos/pisos/heads.json
   ```

   O `deploy.sh` verifica-as antes de mexer no que corre (passo `0b/6`), e o nó verifica a âncora
   no arranque — **fail-closed por partição**. A partir daí o banner declara a cobertura com


### «A expiração por TTL está a correr?»

Era uma pergunta **sem resposta em runtime**. O escalonador declara-se no banner de arranque e a
partir daí é invisível — e o que deixa de acontecer quando ele morre é o apagamento de dados fora
do TTL, uma obrigação com prazo, e a única que não dá sinal nenhum por si mesma.

Quatro séries, porque há **quatro estados** que se leem de maneira diferente e exigem acções
diferentes:

| observado | significa |
|---|---|
| `armed=0` | nunca foi armado — política ausente, ou intervalo ≤ 0. **Nada expira sozinho.** |
| `armed=1`, `stopped=1` | parou por incidente de integridade da hash-chain. **Não volta sozinho**: investigar o WORM e reiniciar o nó |
| `armed=1`, `stopped=0`, sem `age` | armado, à espera do primeiro tick |
| `armed=1`, `stopped=0`, `age` alta | deixou de correr sem o dizer — alerta acima do **dobro** de `AOS_RETENTION_SWEEP_INTERVAL` |

Com o escalonador desarmado, as séries que descrevem **passagens** não saem: emitir `sweeps_total 0`
e `age 0` faria um nó que nunca vai expirar nada parecer um nó acabado de varrer.

**Por provar, e declarado:** o marcador de paragem definitiva (`stopped`) não é exercitado por
nenhum teste pelo caminho real — fazer o `VerifyWORM` falhar depois de uma passagem exigiria
adulterar um store em memória, o que nem o `MemStore` nem o `FileStore` permitem. O que está
provado é que a métrica **lê** o campo, não que o varredor o **escreve**.
   ### Como se sabe que a selagem morreu

   A âncora é produzida **fora** do nó, por uma tarefa que corre sozinha. Se essa tarefa morrer,
   **nada no nó dá por isso**: a verificação continua a passar — o que ela verifica não mudou — e
   a cobertura congela enquanto o WORM continua a crescer. Uma âncora de há um ano verifica
   exactamente como a de ontem.

   Três séries em `/metrics` fecham isso:

   | série | o que diz |
   |---|---|
   | `aos_worm_partitions` | partições que o WORM tem **agora** (lidas na altura da recolha, não no arranque) |
   | `aos_worm_partitions_anchored` | partições cobertas pela âncora que passou no arranque |
   | `aos_worm_anchor_age_seconds` | segundos desde a **última** selagem |

   **O alerta que interessa** é o terceiro: com cadência diária, `> 172800` (48 h) significa que a
   tarefa de selagem morreu. É o dobro da cadência, pela mesma razão que o `pull-backups.ps1` usa
   48 h — um dia falhado não alerta, dois sim.

   A razão do primeiro ser lido **na altura da recolha** e não no arranque: as partições nascem por
   run, e um valor medido no boot e servido como *gauge* pareceria vivo estando congelado. Faria o
   contrário do que a métrica existe para fazer.

   **Sem âncora composta, as duas séries de âncora NÃO saem.** Emitir `anchored 0` e `age 0` faria
   um nó desprotegido parecer um nó acabado de selar — pior do que não emitir nada. É a mesma regra
   das séries de OTLP.
   número, em vez de dizer que a verificação ancorada está desligada.
9. **Sem tabela de preços.** O par (`gpt-4o-mini`, `eu`) não consta da tabela embebida, pelo que
   o custo derivado é **zero por ausência de dados** — não custo nulo. A dimensão que decide é
   tokens (`AOS_BUDGET_MAX_TOKENS`); um tecto em dólares seria recusado no arranque por falta de
   fonte de preço, em vez de comparar sempre contra zero.
11. ~~A frescura por-cerimónia da aprovação está dormente.~~ **✅ LIGADA.** `AOS_CHALLENGE_ISSUANCE=1`
   ⇒ `POST /runs/{id}/challenge` emite um challenge por `(pedido, aprovador)` com TTL de 5 min, e
   cada perna da cerimónia passa a exigi-lo. Dormente, o anti-replay ficava só pelo uso-único
   durável, e o banner dizia o que isso custava: **quem detivesse a chave de um aprovador podia
   reapresentar uma prova capturada num pedido novo**. Verificado em produção — o endpoint passou
   de `501` a `200` com challenges distintos por aprovador. Ver §"Operar o plano de controlo".
12. **O orçamento está configurado onde nunca morde.** `AOS_BUDGET_MAX_TOKENS=200000` contra um
   consumo medido de ~1 750 tokens por run: o tecto e o aviso aos 80% ficam ~114× acima do uso
   real. O mecanismo **funciona** — verificado forçando-o a 400 tokens, com suspensão em
   `waiting_on_human` — mas na configuração actual é protecção que não engata.

   **Isto passou a ser MENSURÁVEL a 2026-08-21, e não só declarado aqui.** Duas séries em
   `/metrics` dão a folga:

   | série | |
   |---|---|
   | `aos_budget_max_tokens_per_run` | o tecto em vigor |
   | `aos_budget_run_tokens_peak` | o **maior** consumo por-run que este processo viu |

   O rácio entre elas é a folga: hoje ~114×, ou seja um tecto decorativo; perto de 1 seria um
   tecto prestes a suspender runs. **Nenhum dos dois extremos se via no banner**, que declara o
   tecto em detalhe e por isso se lê como protecção activa — e uma protecção cuja folga ninguém
   mede é indistinguível de uma que morde.

   O pico **não sai** antes de o primeiro run fechar: emitir `0` diria «nada gasta nada» e a folga
   apareceria infinita num nó que ainda não mediu coisa nenhuma. É por processo, não durável — um
   restart repõe-o, e a pergunta que a métrica responde («este tecto chega a apertar?»)
   responde-se com dias de observação, não com histórico eterno.
13. **A auditoria não regista quem aprovou.** Uma cerimónia *four-eyes* sela na hash-chain que
   **um** gate humano foi satisfeito (`human_gate: "satisfied"`), mas não **quem** o satisfez: as
   identidades dos aprovadores e o `request_id` do grant não aparecem no `worm.wal`. Para uma
   autorização cujo propósito é o não-repúdio, é a peça que falta. Detalhe e evidência em
   [`../../docs/reports/auditoria-2026-08-17-plano-de-dados-em-producao.md`](../../docs/reports/auditoria-2026-08-17-plano-de-dados-em-producao.md).

> 🔍 **Nota de método, porque me enganou primeiro.** Contar partições no `worm.wal` com
> `grep -ao '"Partition":"[^"]*"'` devolve **69** — e está errado. O WAL é binário enquadrado, e o
> `grep` processa-o por linhas: registos cujo enquadramento parte a linha antes do padrão
> escapam-lhe. `strings -n 8` sobre o mesmo ficheiro devolve **108**, que é o número que fecha com
> o banner (104 no arranque + 2 partições por cada um dos 2 runs submetidos desde então).
>
> O erro era silencioso e plausível: 69 é um número credível, e nada indicava que faltasse um
> terço. Só apareceu por confrontar a contagem com o que o **próprio nó** declara no arranque —
> que é o hábito que vale a pena reter, e não a correcção em si.

---

## Telemetria: o canal está em claro, e um bearer não o resolvia

O nó exporta traces para `AOS_OTLP_ENDPOINT=http://otel:4318` — **HTTP em claro**, na rede do
compose. O banner diz que a autenticação do cliente está desligada (DEF-012) e sugere
`AOS_OTLP_BEARER_TOKEN_PATH`. Seguir essa sugestão seria **teatro**: sobre um canal em claro, o
token viaja em claro *na mesma rede de onde vem a ameaça*, e quem o capturasse forjava à mesma.

O que autentica de facto é **mTLS**, que o nó já suporta (`AOS_OTLP_CLIENT_CERT_PATH` + `_KEY`) e
que de caminho cifra o canal — coisa que o bearer não faz.

[`otel-collector-mtls.yaml`](otel-collector-mtls.yaml) é a variante endurecida, **pronta e não
activa**. Provada num coletor descartável no servidor (porta 14318, sem tocar no que corre):

| Cliente | Resultado |
|---|---|
| sem certificado de cliente | handshake **recusado** |
| com o certificado do nó | `200` |
| em HTTP claro (como hoje) | `400` |

Para ligar — e é um passo **deliberado**, não um `sed`:

```bash
# 1. levar o material para o servidor (as privadas nascem e ficam na máquina do operador)
scp -i deploy/server/secrets-local/deploy_key \
  deploy/server/secrets-local/internal-ca/{ca.crt,otel.crt,otel.key,node-otlp.crt,node-otlp.key} \
  aos@37.60.241.150:/opt/aos/tls-internal/otlp/
# 2. apontar o volume do serviço `otel` para otel-collector-mtls.yaml e montar /opt/aos/tls-internal/otlp
# 3. no .env:  AOS_OTLP_ENDPOINT=https://otel:4318
#              AOS_OTLP_CLIENT_CERT_PATH=/etc/aos/otlp/node.crt
#              AOS_OTLP_CLIENT_KEY_PATH=/etc/aos/otlp/node.key
# 4. reiniciar `otel` e `aos` JUNTOS
```

> ⚠️ **O passo 4 tem de ser verificado, não presumido.** O exportador OTLP do nó é **fail-open**:
> com o mTLS mal configurado os spans param **em silêncio** e o nó continua a servir como se nada
> fosse. A observabilidade desaparece sem um erro — o pior modo de falha possível justamente para
> observabilidade. Confirme que os spans voltam a chegar antes de dar o passo por feito.

---

## Um backup fiel não é um backup correcto

Descoberto ao reparar o `litellm/config.yaml` que o deploy tinha esmagado (ver o histórico do
`deploy.sh`). O ficheiro foi substituído no host a **17/08 23:04**. Os backups correm às 03:17.
Logo:

| Cópia | `litellm/config.yaml` lá dentro |
|---|---|
| `aos-20260817T011703Z` | **2 modelos activos** — a configuração real |
| `aos-20260818T011701Z` | **0 modelos** — o *placeholder* |

O backup mais recente levava a configuração partida. Um restauro a partir dele teria produzido um
nó **sem modelo** — e a rotação de 14 dias acabaria por levar a última cópia boa, altura em que a
configuração deixaria de existir em qualquer sítio.

**O backup fez exactamente o que devia.** Copiou fielmente o que estava no host. O problema é que
o que estava no host era o placeholder, e nada no processo de backup podia sabê-lo.

### O que isto corrige na forma de verificar

Eu tinha escrito que os backups estavam *"verificados ponta-a-ponta: recolhida, **decifrada** com a
privada local, e a chave Transit confirmada lá dentro"*. Isso é verdade e continua a ser — mas
verifica **integridade**, não **correcção**. Prova que o artefacto abre; não prova que o que lá
está serve para levantar o sistema.

São perguntas diferentes, e a segunda é a que interessa no dia mau:

- *o artefacto decifra e não está truncado?* — verificado, e automatizado no `pull-backups.ps1`
- *o que lá está levantaria o sistema?* — só se sabe **restaurando**, e isso nunca foi feito aqui

Corrida uma cópia nova depois da reparação (`aos-20260818T123900Z`), decifrada, e confirmado que
leva os dois modelos e **nenhuma chave em claro** — as chaves continuam a vir de `os.environ`, não
do ficheiro.

### O restauro de ensaio — feito, e o que provou

Corrido a **18/08** sobre `aos-20260818T123900Z`, num ambiente descartável no próprio servidor,
**sem tocar no que estava a correr** (nomes próprios, sem portas publicadas, volumes à parte).
A chave privada nunca foi para o servidor: o artefacto foi decifrado na máquina do operador e só o
conteúdo viajou.

| Passo | Resultado |
|---|---|
| Vault restaurado, desselado com a chave **de dentro do backup** | `Sealed false` |
| Transit no Vault restaurado | a KEK lá está |
| Nó arrancado contra os dados restaurados | `healthy` |
| Hash-chain do WORM re-encadeada no arranque | **108 partições**, iguais às de produção |
| Estado de governação re-hidratado | 68 ligações titular→partição |
| `GET /runs/{id}` de um run que veio do backup | **`200`** |
| O mesmo `GET` sem credencial | `404` |
| Produção durante todo o exercício | `healthy`, `/healthz` `200` |

A última linha da tabela é o que faz as outras significarem alguma coisa: sem o controlo do `404`,
o `200` provaria apenas que o nó responde, não que o read-path restaurado ainda **decide**.

**Portanto a resposta à segunda pergunta é sim, e agora é um facto e não uma hipótese:** o
artefacto levanta o sistema — Vault, WORM, event store, governação e read-path — a partir de um
ficheiro cifrado e da chave privada que vive noutra máquina.

Tudo foi removido no fim, e o bundle em claro apagado com `shred` e não `rm`: enquanto existiu,
tinha lá dentro `.env`, `secrets/` e o material TLS interno.


#### Segunda metade: a identidade também volta

O primeiro ensaio deixou uma lacuna nomeada — *"o que está provado é que os dados e o nó voltam; a
identidade voltar é plausível e não está exercida"*. Exercida a **18/08**, do mesmo artefacto:

| Passo | Resultado |
|---|---|
| `idp-db.sql` restaurado num Postgres descartável | **87 tabelas**, zero erros do `psql` |
| Realms no schema restaurado | `aos`, `master` |
| Keycloak arrancado sobre essa base | ~110 s (a JVM, sob carga 22) |
| Admin do IdP restaurado | autentica com a password do `.env` do backup |
| Clientes no realm restaurado | `aos-issuer`, `aos-node`, `aos-reader` |
| Utilizador humano | `jimy`, com `board:["board:prod"]` |
| Segredo do `aos-reader` **vindo do backup** | emite token no IdP restaurado |
| **Ciclo fechado:** token do IdP restaurado → nó restaurado | **`200`** |

E os controlos, no sistema restaurado e não no de produção:

| Pedido | Resposta |
|---|---|
| sem credencial | `404` |
| `X-Aos-Board: board:prod` forjado | `404` |
| token válido | `200` |
| o **mesmo** token outra vez | `404` |

O último par é o que interessa: o anti-replay do `jti` **também** voltou. Não é só que o sistema
arranque — as suas decisões continuam a ser tomadas.

#### O restauro tem de reproduzir os NOMES, não só os dados

A primeira tentativa deste ciclo deu `404` em tudo, incluindo com um token válido. A causa não era
o backup: eu tinha chamado aos contentores `drill-idp` e `drill-vault`, e os certificados internos
têm SAN para **`idp`** e **`vault`**. O nó buscou o JWKS a `https://drill-idp:8443`, a verificação
TLS falhou, e ele **negou tudo** — com o `404` uniforme, indistinguível de "não existe".

Isso é o comportamento correcto, e é a razão pela qual a tentativa seguinte usou uma rede própria
com `--alias idp` e `--alias vault`. Fica como aviso operacional: um restauro que renomeie os
serviços **falha, e falha em silêncio semântico** — o sintoma é uma recusa opaca e não um erro de
TLS visível no pedido.

Tudo removido no fim — contentores, rede, e o *bundle* em claro apagado com `shred`. Produção
`healthy` durante todo o exercício, com tectos de CPU explícitos nos contentores de ensaio porque
o host corre com *load average* ~22 em 8 vCPU.

### O ensaio é repetível — [`restore-drill.sh`](restore-drill.sh)

Um ensaio feito uma vez prova o dia em que foi feito. O que acima está descrito é agora um script
que se corre depois de qualquer mudança:

```bash
# na máquina do operador, onde vive a chave privada
openssl smime -decrypt -binary -inform DER -in ~/aos-backups/aos-<stamp>.tar.gz.enc \
  -inkey deploy/server/secrets-local/backup-key/backup.key -out bundle.tar.gz
scp -i deploy/server/secrets-local/deploy_key bundle.tar.gz aos@37.60.241.150:/tmp/
```
```bash
# no servidor
bash /opt/aos/restore-drill.sh /tmp/bundle.tar.gz
```

Levanta Vault, Postgres, Keycloak e nó numa rede isolada, faz uma leitura autenticada, e **só
passa se os controlos também valerem**: token válido `200`, mesmo token outra vez `404`, sem
credencial `404`, header forjado `404`. Limpa tudo num `trap` — incluindo `shred` do material em
claro, porque enquanto corre tem o `.env`, os `secrets/` e as chaves TLS desembrulhados em disco.

> **Escrevê-lo apanhou três defeitos que o ensaio à mão não tinha mostrado**, e vale a pena
> registá-los porque são todos do género que passa despercebido:
>
> - **`set -o pipefail` + `grep -q`.** O `grep -q` fecha o *pipe* mal encontra o padrão, o comando
>   a montante leva `SIGPIPE` e sai não-zero, e **o pipeline inteiro falha apesar de o padrão ter
>   sido encontrado**. O Vault desselava sempre; era a verificação que mentia. Todas as buscas do
>   script capturam para variável antes de procurar.
> - **`config.tar.gz` e `volumes.tar.gz` têm ambos `vault/`** — um é a *configuração*, o outro são
>   os *dados*. Extraídos para o mesmo sítio colidem, e o sintoma é o Vault a não desselar: lê-se
>   como "o backup está mau" e não como "o ensaio está mal montado".
> - **O Postgres não estava pronto** e o `psql` rebentava contra ele; o sintoma era "o dump
>   restaurou 0 tabelas". Falhou uma vez e passou na seguinte — o pior comportamento possível,
>   porque um ensaio intermitente ensina a ignorá-lo. Passou a esperar explicitamente e a falhar
>   com a razão certa.
