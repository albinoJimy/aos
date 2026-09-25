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
| Segredo do `aos-reader` | Keycloak (no servidor) | `secrets/reader-client-secret` (0644 dentro de `secrets/` em 0700 — AOS-416) | Credencial de máquina, gerada pelo IdP. Nunca escolhida por ninguém. |
| Token do Vault | Vault (no servidor) | `secrets/vault-token` | **Não é o root.** Token periódico com política só sobre `aos-kek-*`. O root fica em `secrets/vault-init.json`. |
| Unseal do Vault | Vault (no servidor) | `secrets/vault-init.json` | Ver §"O selo do Vault" — está aqui por decisão declarada, e limita o que o selo protege. |
| `wormseal.key` (selador do WORM) | máquina do operador | **máquina do operador** | Assina os checkpoints da verificação ancorada; o nó só recebe a pública, em `AOS_WORM_TRUST_ANCHOR`. Quem a detivesse dava uma âncora válida a uma cadeia reescrita. Rodá-la: §"Rotação das chaves de autoridade". |
| Chave de release (DSSE) | custódia do Arquitecto de Plataforma | secret `AOS_RELEASE_KEY` | Ver [`../node/CUSTODIA-CHAVE-RELEASE.md`](../node/CUSTODIA-CHAVE-RELEASE.md). |

O servidor, portanto, **não guarda nenhuma credencial que conceda autoridade sobre o sistema**.

---

## Sandbox das tool calls — porquê gVisor e não Firecracker

O nó medeia cada tool call, mas **onde** ela corre depende do driver, e os três não são
equivalentes:

| Driver | Fronteira | Neste servidor |
|---|---|---|
| `fake` (default **fora** de produção) | Jail **in-process**: overlay read-only, seccomp default-deny, escape bloqueado | Funciona — mas o próprio pacote marca-o **"NUNCA usar em produção"**, e desde **AOS-344** o nó recusa-o sob `AOS_MODE=production` (ver a tabela de portas abaixo) |
| `firecracker` | microVM com KVM (ADR-004) | ❌ **Impossível**: sem `/dev/kvm`, 0 CPUs com `vmx`/`svm`. O host é ele próprio um convidado sem virtualização aninhada |
| `gvisor` | Interposição de syscalls em user-space (`systrap`) | ✅ **Em uso** — não precisa de KVM |

> **Residual do seccomp, neste driver (AOS-351).** O perfil `sbx-seccomp/v1` que o nó carrega
> (`substrate/sandbox/seccomp`) **não é imposto** por este caminho. O `GVisorDriver` recebe-o em
> `Spec.Seccomp` e ignora-o, e o wire host→guest (`POST /exec`) transporta apenas a tool call —
> nenhum byte do perfil chega ao sandbox. O que contém o guest aqui é a **interposição de
> syscalls do `runsc`** (mais o `--network=none` e o rootfs efémero), não esta allowlist. Por
> isso o manifesto selado no WORM traz `seccomp_enforced_by: "none"` para este driver: o
> `seccomp_profile_hash` é uma **declaração de configuração**, não uma atestação de imposição.
> Só o driver de referência (`fake`) sela `"driver"`. Vale o mesmo para o `firecracker` — ver
> [`deploy/node/dev-hardened/firecracker/README.md`](../node/dev-hardened/firecracker/README.md).

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

O `deploy.sh` **já reverte sozinho** quando o `compose up` falha, quando o nó não fica saudável ou
quando o smoke falha. O `rollback.sh` é para a regressão descoberta horas depois, quando esse
contexto já não existe.

> ⚠️ **Depois de um deploy falhado, `rollback.sh` sem argumento pode apontar à imagem partida.**
> Cada execução do `deploy.sh` copia o `image.env` corrente para o `image.env.prev` — incluindo a
> que o `rollback.sh` corre por baixo. Uma tentativa falhada seguida de outra deixa o próprio
> digest partido como "anterior". Foi o que aconteceu em 2026-09-13. Em caso de dúvida, passa o
> digest bom **explicitamente**: `bash /opt/aos/rollback.sh ghcr.io/albinojimy/aos-node@sha256:…`.

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
#    AOS-407: a NHI leva o BOARD de soberania assinado. Com --human declara-se em --board; a via
#    de produção é o get-id-token.ps1 -Cunhar, que usa --assertion e copia o board da claim do IdP.
NHI=$(go run . mint --key-file ../../../deploy/server/secrets-local/issuer.key \
  --issuer iss:aos-issuer --human human:alice --board board:prod --agent agt-teste-01 --class agent-worker \
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

## Orquestrador multi-nó (`aos-orq`)

Desde o **AOS-403** o `aos-orq` vem **na mesma imagem** que o nó, atestado como subject próprio
(`usr/local/bin/aos-orq`, com `sbom-aos-orq.json`). Deixa de haver binário compilado à parte e
copiado para o servidor: corre-se o que o release assinou, a partir do digest que o `deploy.sh`
pinou em `image.env`. **Verificado em produção** a 2026-09-17 na `v0.1.20`: o run
`run-aos403-prod-1789643089` correu por esta receita, decompôs um plano de 2 nós com `gpt-4o-mini` e
selou no volume do orquestrador (evidência no AOS-403, `specs/EPIC-10`).

> Envie o comando **inline** (`ssh aos-prod '…'`) e não por `ssh … bash -s < script`: o
> `docker compose run` lê o stdin e consome o resto do script, pelo que o que viesse depois (o
> `echo $?`, por exemplo) nunca corre.

O serviço `aos-orq` do `docker-compose.prod.yml` está no profile **`orq`**, pelo que o `deploy.sh`
não o arranca: um `serve` possui **um** run, decompõe-o, despacha-o e termina. Corre-se à mão:

```bash
cd /opt/aos
cp snapshot.json orq/snapshot.json      # snapshot PINADO de capabilities (obrigatório com --goal)
docker compose -f docker-compose.prod.yml --env-file .env --env-file image.env \
  --profile orq run --rm aos-orq \
  serve --wal /var/lib/aos-orq/run-X.wal --run run-X \
        --goal "objectivo" --snapshot /etc/aos-orq/snapshot.json
```

| O quê | Onde | Porquê |
|---|---|---|
| WAL do run e WORM de governação do gateway | volume `aos_aos-orq-data`, em `/var/lib/aos-orq` | Volume **próprio**: o WORM pede posse exclusiva do seu caminho, tal como o do nó (AOS-395/AOS-399). Volumes separados tornam impossível apontar os dois ao mesmo ficheiro por engano. |
| Caminho do audit | `AOS_ORQ_MODEL_AUDIT_PATH` (por omissão `/var/lib/aos-orq/model-audit.wal`) | **Não** lê a `AOS_MODEL_AUDIT_PATH` do `.env`, que é a do nó e aponta para outro volume. |
| Entradas (snapshot, plan-doc) | `/opt/aos/orq` → `/etc/aos-orq`, só leitura | Criada pelo `deploy.sh`. |
| Modelo | as `AOS_MODEL_*` (incluindo `AOS_MODEL_EGRESS_TIMEOUT`) e `AOS_MODE` do `.env` do nó, a `model-api.key` e o bundle da CA interna | A mesma config de modelo do nó. Sob `AOS_MODE=production` o egress é o endurecido; com `AOS_MODEL_EGRESS_HOSTS` vazia a allowlist deriva do host do endpoint, como no nó. |

Códigos de saída: `0` ok · `1` erro · `2` flags inválidas · `3` posse do run negada · `4` posse
superada · `5` WAL ou `AOS_MODEL_AUDIT_PATH` detido por outro escritor · `6` plano **pendente** de
decisão humana · `7` decisão **recusada**. Para ler um run sem tomar posse,
`run --rm aos-orq inspect --wal … --run …`.

#### Gate de aprovação de plano (AOS-408)

Um plano cujo risco resolvido seja `danger` (pelas tools do snapshot pinado, não pelo que o
documento diz de si) **não materializa**: o `serve` apensa os factos do pendente, larga a posse e
sai com **`6`**. A decisão vem por fora, assinada, e o `serve` repetido prossegue. Um plano sem
risco auto-aprova e segue como antes.

```bash
C="docker compose -f docker-compose.prod.yml --env-file .env --env-file image.env --profile orq"
# 1. o plano fica pendente (saída 6) e o documento vai para o volume
$C run --rm aos-orq serve --wal /var/lib/aos-orq/run-X.wal --run run-X --goal "…" \
   --snapshot /etc/aos-orq/snapshot.json --plan-out /var/lib/aos-orq/run-X-pendente.json
# 2. o que assinar: imprime o request_id (plan:<run>-plan:<plan_hash>)
$C run --rm aos-orq plans --wal /var/lib/aos-orq/run-X.wal --run run-X
```

Na **máquina do aprovador** (a chave privada nunca vai para o servidor):

```powershell
aos-issuer plan-approve-sign --request-id plan:run-X-plan:sha256:… --approver human:alice `
  --key-file C:\caminho\aprovador.seed --approve --out aprovacao.json
```

De volta ao servidor, com `aprovacao.json` copiada para `/opt/aos/orq/`:

```bash
# 3. a cerimónia: assinatura contra a chave PINADA em /opt/aos/orq/approvers.json (saída 0 ou 7)
$C run --rm aos-orq decide --wal /var/lib/aos-orq/run-X.wal --run run-X \
   --plan-doc /var/lib/aos-orq/run-X-pendente.json --snapshot /etc/aos-orq/snapshot.json \
   --decision approve --approval /etc/aos-orq/aprovacao.json
# 4. executar o organigrama APROVADO — pelo documento, não pelo --goal: reconhece a decisão,
#    materializa e despacha (saída 0)
$C run --rm aos-orq serve --wal /var/lib/aos-orq/run-X.wal --run run-X \
   --plan-doc /var/lib/aos-orq/run-X-pendente.json --snapshot /etc/aos-orq/snapshot.json
```

`/opt/aos/orq/approvers.json` tem o formato do `AOS_APPROVERS_FILE` do nó
(`{"approvers":[{"principal":…,"pubkey":"<64 hex>","authority":["approve:danger"]}]}`). Sem ele,
nenhum plano de risco é aprovável — é a direcção certa do erro.

> ⚠️ **Fronteira de confiança.** O gate governa o PLANO e quem decide sem chave — não quem opera
> este CLI: o Event Store não assina eventos, e o snapshot e os aprovadores são ficheiros do
> operador. Não repita o `--goal` para executar: com o modelo vivo re-decompõe e produz outro
> plano (outro hash), que já não é decidível no mesmo run — o `serve` recusa-o com saída `7` e
> aponta para o `--plan-doc`. Ver os tickets AOS-408 e AOS-412.

#### Executor de nós do plano (AOS-413, ADR-027)

Sem ele, um plano aprovado é despachado e **nada o executa**: os nós ficam `running` para sempre.
Com ele, cada nó despachado é um run do nó `aos`, com as tools pinadas **desse** nó como
lista-branca; a conclusão e o veredicto de cada verificador voltam ao log e o despacho avança
até ao fim do plano.

**Antes da corrida, três coisas que o executor não resolve sozinho:**

- **O snapshot tem de usar os nomes de tool do nó.** A lista-branca compara o nome da tool no
  plano com o `ToolID` das tools do nó (`AOS_MODEL_TOOLS`). Um snapshot com nomes que o nó não tem
  deixa cada nó sem nenhuma tool utilizável — fail-closed, mas o plano não faz nada.
- **O NHI do run é cunhado por si**, com o `aos-issuer`, na sua máquina: as tools do plano,
  `model:invoke` e o board (o `-Cunhar` do `get-id-token.ps1` copia o board do IdP). A validade
  (45 min) é o tecto de duração do plano. Copie-o para `/opt/aos/orq/nhi-run.jwt` e **apague-o no
  fim**.
- **As duas credenciais montadas têm de ser legíveis pelo uid `65532`** (AOS-416), e o `serve`
  recusa arrancar se não forem, dizendo qual e o gesto — em vez de dizer `COMPOSTO` e falhar na
  primeira submissão, que era o comportamento antigo. O `provision-identity.sh` já põe o
  `secrets/reader-client-secret` em `0644`; numa instalação anterior ao AOS-416 ele está em `0400`
  do utilizador `aos` e o contentor **não o lê** — corrija com `chmod 644
  secrets/reader-client-secret`. O mesmo vale para o NHI que copiar para `orq/nhi-run.jwt`: com o
  `umask 077` fica `0600` e é preciso `chmod 644`. **A fronteira do segredo é o directório**, que
  está em `0700`; não faça cópias dos ficheiros.

```bash
$C run --rm \
  -e AOS_ORQ_NODE_URL=http://aos:8080 \
  -e AOS_ORQ_NODE_CREDENTIAL_FILE=/etc/aos-orq/nhi-run.jwt \
  -e AOS_ORQ_OIDC_TOKEN_URL=https://idp:8443/realms/aos/protocol/openid-connect/token \
  -e AOS_ORQ_OIDC_CLIENT_ID=aos-reader \
  -e AOS_ORQ_OIDC_CLIENT_SECRET_FILE=/run/aos-orq/reader-client-secret \
  aos-orq serve --wal /var/lib/aos-orq/run-X.wal --run run-X \
    --plan-doc /var/lib/aos-orq/run-X-pendente.json --snapshot /etc/aos-orq/snapshot.json
```

O `serve` espera pelos runs dos nós até `--plan-timeout` (40 min por omissão). Termina com `0` e
a linha `execucao: n1=complete …` quando o plano chega ao fim; com **`8`** se o prazo acabar com
nós ainda a correr — larga a posse, e a mesma invocação retoma-os. Os runs dos nós são runs
normais do nó (`<run>~<node_id>`), legíveis por `GET /runs/<run>~<node_id>`.

Desde o **AOS-414**, um nó recebe os payloads que o `consumes` dele declara: entram no prompt do
run como segmento próprio, marcado `taint=untrusted` e com a proveniência (nó de origem, output,
digest), nunca como objectivo. O nó verifica o digest. O veredicto de um verificador lê-se da saída
final por uma gramática fechada — qualquer outra resposta conta como `fail`, e o ramo condicional
não corre.

> ⚠️ **O conteúdo vive na memória do `serve`** (ADR-027 §2.4, opção (A)): no log fica a
> referência. Se o `serve` morrer, o consumidor cujo produtor já concluiu **não corre**: fecha em
> `failed` com a razão à vista (`o contrato <no>/<output> ficou por cumprir`) e o plano termina —
> para o refazer, um run novo. O mesmo vale para o que não é publicável: um contrato `metrics`,
> um segundo contrato de forma aberta no mesmo nó, ou uma saída acima de 128 KiB. A separação de
> planos (DEF-806) continua aberta: o canal é próprio e marcado, mas o conteúdo é lido pelo mesmo
> plano que planeia.

**Dois runs ao mesmo tempo precisam de dois caminhos de audit**, não só de dois `--wal`: o caminho
por omissão é um só, e o segundo `serve --goal` sai com `5`. Dê a cada corrida o seu:

```bash
docker compose … --profile orq run --rm -e AOS_MODEL_AUDIT_PATH=/var/lib/aos-orq/run-Y-audit.wal \
  aos-orq serve --wal /var/lib/aos-orq/run-Y.wal --run run-Y --goal "…" --snapshot /etc/aos-orq/snapshot.json
```

O contentor corre com o root-fs só de leitura, sem capabilities, como `65532` e sem o
`HEALTHCHECK` da imagem (que sonda o HTTP do nó). O `backup.sh` inclui o volume
`aos_aos-orq-data` quando ele existe (antes da primeira corrida não existe e fica de fora, e o log
di-lo), leva `orq/` na configuração e escreve `aos-orq-data=volume|ausente` no MANIFEST. O
`restore-drill.sh` não restaura este volume: prova o nó, não o orquestrador.

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

O nó corre em modo produção. Não foi um interruptor: são **dez** portas fail-closed, e o
arranque aborta em qualquer uma. As seis primeiras foram enumeradas empiricamente — arrancando a
imagem num contentor descartável e acrescentando um requisito de cada vez até passar — e não por
leitura do código, que é como a terceira tinha passado despercebida. **A sétima, a oitava, a nona e
a décima só podiam vir da leitura do código** — nenhuma delas negava, arrancavam —, e as notas
depois da tabela explicam porquê.

| Porta | Exige | Servida por |
|---|---|---|
| Identidade endurecida | `AOS_ISSUER_PUBKEY` | já estava |
| Soberania de leitura | `AOS_BOARD_REGIONS` | já estava |
| TLS do ingresso | `AOS_TLS_EXTERNAL_TERMINATION=1` | já estava (edge) |
| **Credencial forte** | `AOS_SOVEREIGN_OIDC_ISSUER` + `_AUDIENCE` | **Keycloak** (`idp`, `idp-db`) |
| **Custódia da KEK** | `AOS_DSAR_VAULT_ADDR` + `_TOKEN_PATH` | **Vault** (`vault`, `vault-unseal`) |
| **Credencial do modelo** | `AOS_MODEL_API_KEY_PATH` | master key do LiteLLM |
| **Driver de sandbox** (condicional) | `AOS_SANDBOX_DRIVER=gvisor` (+`AOS_SANDBOX_GVISOR_URL`) ou `=firecracker` (+`AOS_SANDBOX_FIRECRACKER_URL`) | **componente `gvisor`** (`gvisor/`) |
| **Trilho WORM durável** | `AOS_WORM_PATH` (e, por arrasto da KEK, `AOS_DSAR_VAULT_ADDR`) | **montagem gravável** (`/var/lib/aos`, `worm.wal`) |
| **Egress endurecido do modelo** (condicional) | `AOS_MODEL_ENDPOINT` em `https` + allowlist: `AOS_MODEL_EGRESS_HOSTS` ou, por omissão, o host do próprio endpoint | LiteLLM / gateway externo em https |
| **Autoridade da destruição DSAR** | `AOS_DSAR_ERASERS` (emitterIDs de `AOS_OPERATORS` com `dsar:erase`) | operadores DSAR com chave ed25519 (privada fora do nó) |

As duas últimas não constavam da versão anterior deste documento. A da KEK nunca tinha sido
nomeada; a do modelo **nasceu** quando o gateway foi ligado — antes disso `AOS_MODEL_ENDPOINT`
estava vazia e a porta não existia. Um documento sobre pré-requisitos envelhece com a
configuração, e este envelheceu em menos de um dia.

**A sétima nasceu de uma auditoria, não de um arranque falhado** (AOS-344, 2026-09-06, commit
`2ca2d5c`) — e é por isso que valia a pena escrevê-la aqui. Enumerar portas *empiricamente* só
encontra as que **negam**: esta não negava. `AOS_SANDBOX_DRIVER` vazia elegia o driver
`fake` em silêncio, e o `fake` é o único dos três que falha **aberto** — `firecracker` e `gvisor`
sem executor devolvem `ErrDriverUnavailable` e a chamada morre no caminho de recusa, enquanto o
`fake` sucede e o resultado, que nenhuma fronteira ao nível do kernel produziu, é selado na
hash-chain WORM como se fosse um efeito real. É **condicional**: só exigida quando o catálogo de
`AOS_MODEL_TOOLS` traz pelo menos uma tool com bloco `sandbox` — que é o caso do catálogo
entregue em [`model-tools/tools.json`](model-tools/tools.json). Este servidor já a satisfazia
(`AOS_SANDBOX_DRIVER=gvisor`, secção «Sandbox» acima); o que faltava era a porta existir para
quem copiasse o compose sem essa linha.

**A oitava também nasceu de uma auditoria** (AOS-365, achado O-12) e é do mesmo feitio da sétima:
não negava — arrancava. Sem `AOS_WORM_PATH` o nó caía no WORM `in-memory de referencia
(nao-duravel)` **sem consultar o modo**, e o banner declarava-o com honestidade — o que
*desarmava* a suspeita em vez de a levantar: quem lê «nao-duravel» vê uma declaração correcta e não
pergunta se produção devia tê-la aceite. A honestidade do banner substituiu a guarda. O conteúdo
não se perdia (o Event Store é durável pela porta da soberania, a KEK pela sua); o que morria com o
processo era a **hash-chain tamper-evident** — a prova de quem selou o quê: selo de residência,
changelog de política, legal hold, expiração, atribuição de quem destruiu o quê. Um restart apagava
a **prova**, não o **efeito**. É **incondicional**, como a do Event Store — o WORM sela sempre — e
*arrasta* a porta da KEK que já existia: um WORM durável exige KEK durável (a porta da custódia,
acima). Por isso a mensagem de erro nomeia `AOS_WORM_PATH` **e** `AOS_DSAR_VAULT_ADDR` de uma vez —
para o operador não fazer a coisa certa a meio e trocar um erro por outro. **São essas duas
variáveis, e só elas:** o Vault que `AOS_DSAR_VAULT_ADDR` compõe já sabe confirmar a destruição da
KEK, pelo que a porta da confirmação de shred (AOS-328) passa por si — **não** é preciso declarar
`AOS_DSAR_VAULT_DESTROY_UNCONDITIONAL`, que existe para o caso oposto (uma custódia que destrói às
cegas) e suprimiria o aviso AOS-322. Este servidor já montava um caminho de WORM; a porta é para
quem não o fizer.

**A nona nasceu do mesmo refutador** (AOS-366) e é a mais subtil das três: o nó não só saltava o
endurecimento de egress — *desarmava-o activamente*. O gateway de modelo traz um caminho SSRF
fail-closed (AOS-223): com `HTTPClient` nil, valida o `BaseURL` (https + allowlist, com a porta na
chave) e constrói um transporte com timeout (30 s por omissão; `AOS_MODEL_EGRESS_TIMEOUT` muda-o), limite de redirects e re-validação de **cada**
salto. O nó injectava-lhe um `http.Client` banal em **todas** as configurações — o que faz o gateway
*delegar* a validação nesse transporte, i.e. não validar nada — e um comentário chamava-lhe «seam de
dev» dentro do binário de produção. Um `AOS_MODEL_ENDPOINT` em `http://` ou apontado a um host
arbitrário era aceite sem uma palavra. Duas leituras confirmavam-se sem tocar no código: o gateway
estava correcto e o nó tinha «só um http.Client com timeout». Agora, sob produção, o nó deixa o
`HTTPClient` nil e preenche a allowlist a partir de `AOS_MODEL_EGRESS_HOSTS` (CSV de `host` ou
`host:porta`) ou, por omissão, do host do próprio `AOS_MODEL_ENDPOINT` — o destino já configurado,
sem uma segunda variável a manter em sincronia. É **condicional**: só existe quando o gateway está
ligado (`AOS_MODEL_ENDPOINT` presente). Fora de produção o seam de dev mantém-se — é o que aponta o
nó ao LiteLLM interno em `http`.

**A décima nasceu de outra auditoria** (AOS-367) e é da mesma família da sétima e da oitava: não
negava — arrancava. As quatro rotas de destruição de dados (`/dsar/erase`, `/dsar/hold`,
`/dsar/release`, `/dsar/expire`) autenticavam-se com o **mesmo** ID-token OIDC de LEITURA que serve
`GET /runs/{id}`: um só par issuer/audience serve o leitor de runs e o operador que destrói, pelo que
quem tinha credencial para LER runs da sua região tinha, com a mesma credencial, autoridade para os
DESTRUIR — e o crypto-shred é a operação do nó que se quer irreversível (esta frase dizia que
nenhum *restore drill* a desfaz, e era falso: um restauro de um backup anterior ao apagamento trazia
a KEK de volta até ao [AOS-436](#o-registo-de-apagamentos-sai-do-bundle-em-claro-aos-436)). A distinção
que faltava era **identificação vs autorização**: a OIDC identifica bem, mas não separa quem lê de
quem destrói. `AOS_DSAR_ERASERS` fecha-a — a lista dos emitterIDs de `AOS_OPERATORS` que assinam com
`dsar:erase` — e as quatro rotas passam a exigir, além da identificação OIDC, uma assinatura ed25519
sobre o payload canónico (com a acção amarrada, no molde do `POST /autonomy`); o `/dsar/release`
ganha a **barreira de região** que o `/dsar/erase` já tinha, e o `/dsar/expire` — um varrimento
global sem alvo único — exige **duas** assinaturas de erasers distintos. É **incondicional** em
produção; fora de produção a lista vazia deixa a prova desligada (retro-compatível com dev e testes
por headers). O varredor **automático** de retenção fica intacto: a exigência é sobre quem *ordena*
uma expiração por rota, não sobre o tick agendado.

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
  gerado pelo Keycloak e vive em `secrets/reader-client-secret` (0644 dentro de `secrets/` em 0700, para que o uid 65532 o leia — AOS-416).
- **`aos-node`** — cliente público, **código de autorização + PKCE S256**, para leitores
  **humanos**, cada um com o seu atributo `board`. O humano autentica-se no browser com
  [`get-id-token.ps1`](get-id-token.ps1); a password nunca passa pela linha de comandos.

A distinção importa: um service account colapsa "quem lê" numa identidade de máquina. A soberania
**por-leitor** — que é o argumento de todo o mecanismo — só é real quando existirem identidades
humanas distintas. **Já existem:** o WORM tem, na mesma cadeia, leituras do service account e uma
de um humano com o seu próprio `sub` (ver §"O que continua por fechar", ponto 7).

---

## Rotação das chaves de autoridade

A 2026-09-15 perderam-se as privadas do operador (`ops:prod`), do ratificador (`release:prod`), dos
aprovadores (`human:alice`, `human:bob`) e do selador do WORM (`wormseal.key`), e foram rodadas em
produção. Não havia procedimento escrito. Esta secção descreve o que funcionou nessa rotação e o
que ficou verificado. A `issuer.key` fica de fora: substituí-la invalida todas as credenciais em
circulação (§0), e isso é outra operação.

As duas famílias rodam de formas diferentes, por razão estrutural. Operador, ratificador e
aprovadores são **listas** de pubkeys indexadas por id: troca-se a pubkey e mantém-se o id. O
selador é **uma** pubkey, e tudo o que ele assinou tem de mudar com ela.

### Operador, ratificador e aprovadores

1. **Gerar as seeds novas** na máquina do operador, com `bash deploy/server/gen-identity.sh`. O
   script **só cria seeds em falta**: `gen_seed` não toca num ficheiro não-vazio. Para rodar uma
   seed que ainda existe, arquiva-a primeiro fora de `secrets-local/`. O mesmo script tem duas
   armadilhas:
   - o `server.env` que escreve é um **modelo de instalação**: traz `AOS_MODE=` vazio e nenhuma das
     portas de produção. **Não** o copies por cima do `/opt/aos/.env`;
   - o passo 1/5 corre `aos-issuer pubkey --key-file issuer.key`, que **cria** a seed se ela não
     existir. Confirma que `secrets-local/issuer.key` está presente antes de correr o script. Sem
     ela nasce um issuer novo em silêncio, e a `AOS_ISSUER_PUBKEY` do `server.env` deixa de bater
     com a do servidor.
2. **Trocar só as linhas `AOS_OPERATORS` e `AOS_RATIFIERS`** do `/opt/aos/.env`, com os **mesmos
   ids**. Outras variáveis referem esses ids (`AOS_AUTONOMY_SETTERS`, `AOS_DSAR_ERASERS`), e um id
   que deixe de constar de `AOS_OPERATORS` aborta o arranque.
3. **Trocar o `secrets/approvers.json`**, com os **mesmos principais e as mesmas autoridades** que o
   servidor já tinha. Confirma-as antes de trocar: o ficheiro do `gen-identity.sh` traz as
   autoridades da instalação, que não são necessariamente as de produção.
4. **Redeploy.** A mudança no `.env` recria o serviço `aos`, e é essa recriação que faz o nó reler
   as pubkeys e o `approvers.json` montado.

**O parse é fail-closed, e isso decide a forma da troca.** Não há janela em que a chave antiga e a
nova valham ao mesmo tempo:

- o mesmo id com duas chaves em `AOS_OPERATORS` ou `AOS_RATIFIERS` aborta o arranque (`ErrBadOperators`
  / `ErrBadRatifiers`, «o último NÃO ganha»). A mesma pubkey em dois ids também aborta;
- no `approvers.json` abortam um principal duplicado, uma pubkey repetida entre principais e um
  campo a mais (§"Os ficheiros JSON montados não toleram um único campo a mais").

**O que a rotação não parte:**

- **Nada reverifica assinaturas antigas no arranque.** Os `steer`/`pause`, as revogações, as
  destruições DSAR, as aprovações e as ratificações já seladas ficam como evidência do que foi
  assinado na altura. O nó não as confronta com as pubkeys novas, e o arranque não depende delas.
- **As ADRs não são assinadas.** O ratificador só serve `POST /promote`.

**A excepção, que tem efeito visível: a autonomia reidratada.** No arranque, a reidratação
([`autonomy_rehydrate.go`](../../packages/cmd/aos/autonomy_rehydrate.go)) **verifica** cada
`autonomy.level_changed` de operador contra as pubkeys de **agora**. Um registo assinado pela chave
antiga deixa de verificar e é **saltado**: nunca é aplicado e o arranque não aborta. O banner
declara-o com o seq, e o par volta ao nível de `AOS_AUTONOMY_LEVELS` (ou ao piso). Nenhum nível
sobe por causa disto. Mas uma elevação que um operador tenha assinado por `POST /autonomy` com a
chave antiga **perde-se** no primeiro arranque depois da rotação. Lê o banner e reassina com a
chave nova as elevações que devem manter-se. Na rotação de 2026-09-15 isto não teve efeito:
`AOS_AUTONOMY_SETTERS` está vazio em produção, e sem ele nenhum registo de operador reidrata.

**A prova usada**, para a chave de operador: um `pause` sobre um run **já terminado**, que autentica
a chave sem mexer em trabalho vivo.

```bash
aos pause --addr https://aos.elysiumii.site:8444 --run-id <run terminado> --emitter ops:prod --key <seed>
```

Com uma seed **errada** (aleatória, porque a antiga estava perdida) o nó responde `403`; com a
**nova**, o pedido é aceite. Esta prova cobre só a chave de operador. Para o ratificador e os
aprovadores, confirmou-se que as pubkeys derivadas das seeds novas são iguais às que o servidor
carrega. Nenhum dos dois foi exercitado ao vivo.

### Selador do WORM

**Só existe uma `AOS_WORM_TRUST_ANCHOR`.** O nó verifica todos os checkpoints contra essa pubkey, e
por isso não há sobreposição possível. Com os checkpoints da chave nova e a variável antiga, o nó
aborta o arranque; com os checkpoints antigos e a variável nova, também. **Checkpoints, pisos e
variável mudam juntos.**

**(a) Criar a seed nova.** Em `packages/cmd/aos-issuer`, `go run . pubkey --key-file <novo>` cria a
seed se ela não existir e imprime a pública em hex. É essa pública que vai para a variável.

**(b) Trazer o `worm.wal` vivo sem deixar cópia no servidor.** Corre isto em Git Bash e não em
PowerShell: o `>` do PowerShell 5.1 regrava a saída em UTF-16 e estraga o binário.

```bash
ssh -i <chave-ssh> aos@37.60.241.150 'docker run --rm -v aos_aos-data:/aos:ro alpine:3.20 cat /aos/worm.wal' > worm.wal
```

```bash
N=$(stat -c %s worm.wal); sha256sum worm.wal
```

```bash
ssh -i <chave-ssh> aos@37.60.241.150 "docker run --rm -v aos_aos-data:/aos:ro alpine:3.20 sh -c 'head -c $N /aos/worm.wal | sha256sum'"
```

Os dois hashes têm de coincidir. O nó continua a escrever enquanto se copia, pelo que o ficheiro
local é um **prefixo** do vivo. Por isso se compara o prefixo de `N` bytes e não o ficheiro inteiro.
O `worm.wal` é dado de produção: fica fora do repositório e apaga-se no fim.

**(c) Selar sem `--anterior`.**

```bash
go run . worm-seal --worm worm.wal --key-file wormseal.key > checkpoints.json
```

```bash
go run . worm-seal --worm worm.wal --key-file wormseal.key --heads > heads.json
```

⚠️ **Sem `--anterior`, e esta é a armadilha da rotação.** O `--anterior` é verificado contra a
pubkey da chave **que sela agora**. Os checkpoints anteriores foram assinados pela chave antiga, não
verificam contra a nova, e o selador recusa com `ErrWormSealDivergencia`: «o WORM DIVERGIU», a mesma
mensagem que significaria uma história reescrita. O `selar-worm.ps1` passa o `--anterior` sozinho
sempre que há um `checkpoints.json` na sua pasta — e é por isso que a primeira selagem de uma
rotação **não se faz por ele**, mas à mão, com os comandos acima.

E é assim que fica: a selagem diária **exige** continuidade e recusa selar sem checkpoints em vigor
(#293). Uma selagem sem `--anterior` não compara nada e, numa tarefa que corre sozinha, seria a
porta por onde uma truncatura passava a ser ancorada. O preço é este passo manual, uma vez por
rotação, feito por quem sabe que rodou a chave.

> O switch `-ChaveNova` do script — que omitia o `--anterior` e recusava `-Entregar` — nasceu desta
> rotação e **já não existe**: saiu com o #293, que tornou a continuidade obrigatória. A rotação
> faz-se por estes passos manuais. Se o encontrares numa cópia antiga do script, não o uses.

**(d) Auto-verificação local.** Sela outra vez, agora **com** `--anterior` apontado aos checkpoints
acabados de gerar. Tem de sair com código 0.

```bash
go run . worm-seal --worm worm.wal --key-file wormseal.key --anterior checkpoints.json > /dev/null
```

**O script não serve para os passos (b) a (d), e é deliberado.** O `selar-worm.ps1` é a *cadência*,
não a *rotação*: corre sozinho todos os dias, e um modo que sele sem comparar com nada abriria na
tarefa automática exactamente o buraco que a continuidade fecha. A rotação de 2026-09-15 fez-se à
mão, e a próxima faz-se igual.

O que o script empresta à rotação é o **transporte**: desde que a selagem diária passou pelo gate
(`worm-seal-gate.sh`, §8), a chave da selagem faz o passo (b) sem shell nenhuma, porque o verbo
`worm` é exactamente o `cat` de (b) e não deixa cópia no servidor.

```bash
ssh -i <chave-da-selagem> aos@37.60.241.150 worm > worm.wal
```

⚠️ **Suspenda a tarefa diária durante a rotação.** Antes de (g), a `AOS-SelarWORM` recusa selar —
a chave não bate com o `selador.pub` em vigor. Depois de (g) ela passa a selar **e a entregar**, e o
gate **recusa** a troca enquanto a `AOS_WORM_TRUST_ANCHOR` do `.env` for ainda a antiga — as
assinaturas novas não verificam contra ela. Já não há nó a abortar, mas há uma execução falhada e um
alerta por nada. `Disable-ScheduledTask AOS-SelarWORM` antes de começar,
`Enable-ScheduledTask` depois de (f) e (g) estarem os dois feitos.

**(e) Subir com nomes temporários.**

```bash
scp -i <chave-ssh> checkpoints.json aos@37.60.241.150:/opt/aos/ancoras/.checkpoints.novo
```

```bash
scp -i <chave-ssh> heads.json aos@37.60.241.150:/opt/aos/pisos/.heads.novo
```

**(f) Trocar tudo num só comando, com reversão.** O comando faz, por esta ordem: cópias `.bak` dos
três ficheiros, os dois `mv` lado a lado, a troca da linha `AOS_WORM_TRUST_ANCHOR`, a recriação só
do `aos` e a espera pelo `healthy`. Se o nó não ficar saudável, reverte os três ficheiros e recria
o nó outra vez. O bloco abaixo reproduz esses passos; não é a transcrição literal do que se correu.

```bash
ssh -i <chave-ssh> aos@37.60.241.150 'NOVA=<pubkey hex nova> bash -s' <<'EOF'
set -euo pipefail
cd /opt/aos
[[ "$NOVA" =~ ^[0-9a-f]{64}$ ]] || { echo "NOVA invalida"; exit 1; }
[ -s ancoras/.checkpoints.novo ] && [ -s pisos/.heads.novo ] || { echo "faltam os .novo (passo e)"; exit 1; }
grep -q '^AOS_WORM_TRUST_ANCHOR=' .env || { echo "AOS_WORM_TRUST_ANCHOR ausente do .env"; exit 1; }
dc() { docker compose -f docker-compose.prod.yml --env-file .env --env-file image.env "$@"; }

cp -p .env .env.bak
cp -p ancoras/checkpoints.json ancoras/checkpoints.json.bak
cp -p pisos/heads.json pisos/heads.json.bak

reverter() {
  echo "*** $1 — a reverter"
  dc logs --tail 40 aos || true
  cp -p .env.bak .env
  cp -p ancoras/checkpoints.json.bak ancoras/checkpoints.json
  cp -p pisos/heads.json.bak pisos/heads.json
  dc up -d --no-deps aos
  exit 1
}

mv ancoras/.checkpoints.novo ancoras/checkpoints.json
mv pisos/.heads.novo pisos/heads.json
sed -i "s/^AOS_WORM_TRUST_ANCHOR=.*/AOS_WORM_TRUST_ANCHOR=${NOVA}/" .env
dc up -d --no-deps aos || reverter "compose up falhou"

for _ in $(seq 1 40); do   # 400 s: o start_period do nó é 300 s
  hs=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$(dc ps -q aos)" 2>/dev/null || echo '?')
  if [ "$hs" = healthy ]; then
    dc logs aos | grep -m1 'ANCORADA em' || echo "AVISO: linha ANCORADA nao encontrada no log"
    echo ROTACAO-OK
    exit 0
  fi
  sleep 10
done
reverter "o no nao ficou healthy"
EOF
```

**Resultado esperado no log do nó:** `ANCORADA em N de N`. Se correram runs entre (b) e (f), sai
`N de M` com M > N. Não é erro: as partições desses runs nasceram depois do selo (§8, ponto 8).

**(g) Deixar a selagem diária a par.** A seed nova passa para `secrets-local/wormseal.key`, e os
checkpoints e pisos novos para `secrets-local/ancoras/checkpoints.json` e
`secrets-local/pisos/heads.json` — é este o par que a selagem seguinte passa em `--anterior`. Sem
isto, a tarefa `AOS-SelarWORM` seguinte recusa selar: ou compara com o `--anterior` da chave antiga
e acusa divergência, ou não encontra checkpoints em vigor. As duas recusas estão certas — quem
rodou a chave tem de deixar o par novo no sítio, e é o último passo da rotação.

**O que fica por ensaiar.** Os backups anteriores à rotação levam checkpoints assinados pela chave
antiga, porque o `backup.sh` copia `ancoras/` e `pisos/`. Pelo desenho, restaurar um desses backups
com a `AOS_WORM_TRUST_ANCHOR` nova aborta o arranque. Guarda a pública antiga (é material público)
junto do registo desta rotação. Este caso não foi ensaiado.

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

#### A chave da recolha só recolhe

A tarefa corre sozinha, pelo que a chave **não tem passphrase** — e o `aos` está no grupo `docker`,
onde uma shell é root no servidor. Uma chave assim, a dar shell, seria o host inteiro numa máquina
de secretária. Por isso a recolha usa uma chave **dedicada** (`secrets-local/backup-pull/id_ed25519`,
ACL só do dono) e o servidor força-lhe um comando, [`backup-pull-gate.sh`](backup-pull-gate.sh),
que aceita exactamente quatro pedidos: `listar`, `recente`, `apagamentos` (o nome do registo de
apagamentos mais recente, AOS-436) e `scp -f` de um de dois nomes exactos —
`/opt/aos/backups/aos-<stamp>.tar.gz.enc` ou `/opt/aos/backups/apagamentos-<stamp>.txt`.
Tudo o resto é recusado e registado no syslog (`aos-backup-pull`).

> **O `scp` vai com `-O`, e não é pormenor.** O OpenSSH 9 fala **SFTP** por omissão, e por SFTP o
> pedido não traz um caminho que se possa validar — a chave leria o `.env` e os `secrets/`. Com
> `-O` (protocolo clássico) chega ao gate como `scp -f <caminho>`. O gate foi exercitado contra
> um `sshd` real com o cliente do Windows: `listar`, `recente` e o `scp -O` de um backup passam;
> shell, sessão sem comando, SFTP, `.env`, `..`, glob e forwarding (`-W`) são recusados.

Instalar (uma vez; o gate chega ao servidor pelo deploy):

```bash
# no servidor, com a .pub copiada para /tmp/backup-pull.pub
install -d -m 700 -o aos -g aos /home/aos/.ssh
printf 'restrict,command="bash /opt/aos/backup-pull-gate.sh" %s\n' "$(cat /tmp/backup-pull.pub)" >> /home/aos/.ssh/authorized_keys
chown aos:aos /home/aos/.ssh/authorized_keys && chmod 600 /home/aos/.ssh/authorized_keys
```

```powershell
# na máquina do operador — e confirmar que ficou
schtasks /Create /TN "AOS-RecolherBackups" /SC DAILY /ST 04:30 /RL LIMITED /F /TR "powershell.exe -NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File C:\Jimy\AOS\deploy\server\pull-backups.ps1" /RU $env:USERNAME
schtasks /Query /TN "AOS-RecolherBackups" /FO LIST
```

O `schtasks` não expõe três definições que esta tarefa precisa, e uma delas **impede-a de correr
sem dizer nada**. Ajustam-se logo a seguir:

```powershell
$t = Get-ScheduledTask AOS-RecolherBackups
$t.Settings.StartWhenAvailable         = $true    # recupera uma execução perdida (máquina desligada)
$t.Settings.DisallowStartIfOnBatteries = $false   # ver abaixo
$t.Settings.StopIfGoingOnBatteries     = $false
$t.Settings.ExecutionTimeLimit         = 'PT2H'   # uma execução presa não bloqueia as seguintes (IgnoreNew)
Set-ScheduledTask -InputObject $t
```

> **A bateria.** Por omissão o Agendador **não arranca tarefas a bateria** — e não falha: a tarefa
> fica em `Queued`, sem `pull.log`, sem erro, sem nada no `ESTADO.txt`. Aconteceu no registo desta
> tarefa (2026-09-14), num portátil fora da corrente. O `StartWhenAvailable` não resolve: cobre a
> máquina **desligada**, não a máquina **sem corrente**. Um portátil que passe as 04:30 a bateria
> ficaria dias sem recolha — exactamente o silêncio que o `pull-backups.ps1` existe para impedir.
> A recolha são ~2 MB por dia; corre-la a bateria não custa nada.

E confirmar que ficou, e que corre — não só que existe:

```powershell
Start-ScheduledTask AOS-RecolherBackups
Get-ScheduledTaskInfo AOS-RecolherBackups   # LastTaskResult 0 no fim; 267009 = ainda a correr
Get-Content "$env:USERPROFILE\aos-backups\ESTADO.txt"
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

### O registo de apagamentos sai do bundle, em claro (AOS-436)

O bundle leva o Vault **tal como está**, com as KEKs vivas nesse instante. Restaurá-lo depois de um
apagamento DSAR traz a KEK do titular de volta. O nó mantém por isso um **registo de apagamentos**
(`/var/lib/aos/apagamentos-dsar.txt`, variável `AOS_DSAR_ERASURE_REGISTER`): uma linha
`<id> <instante> <mac>` por cada destruição de KEK **confirmada** pelo Vault, com `id` e `mac` HMAC sob
a chave `/var/lib/aos/apagamentos-dsar.txt.chave`. A chave fica no volume e sai **só dentro do bundle
cifrado**; o registo sai **em claro** como `backups/apagamentos-<stamp>.txt` (só depois de o bundle
verificar, sem a linha a meio que o tar de um ficheiro vivo possa apanhar) e roda com a mesma conta.
Sem a chave, o ficheiro no portátil **não diz quem foi apagado** — o nome `aos-kek-<sha256>` do Vault
seria invertível por um dicionário de utilizadores, o HMAC não — e **não se deixa forjar**: uma linha
sem MAC válido é rejeitada pelo nó e nunca destrói nada.

O `MANIFEST` do bundle diz o que ele traz (`apagamentos=volume|ausente-no-volume`,
`apagamentos-entradas=N`, `apagamentos-chave=volume|ausente`). O `pull-backups.ps1` recolhe o registo
**mais recente**, verifica que ele contém todos os `id` do anterior (um registo que perdeu entradas
**alerta** e os dois ficam) e só então larga o anterior. Não verifica o MAC — a chave não está lá.

> **O que isto cobre, sem arredondar.** A cópia em claro é tirada do **mesmo** tar e no **mesmo**
> instante que o bundle. Para «perdi o servidor, restauro o último bundle» **não acrescenta nada** —
> o último bundle já sabe tudo o que o registo sabe. Serve para **restaurar um bundle anterior ao
> último** (o último está estragado; ou volta-se a um ponto antes de um deploy mau): o registo mais
> recente diz ao nó restaurado o que foi apagado entre esse bundle e o último. Os apagamentos feitos
> **depois do último backup** não estão em cópia nenhuma, e voltam com qualquer restauro.

Com a imagem de AOS-436 no ar, o primeiro arranque faz entrar no registo **todos** os apagamentos que
a cadeia já conhecia — o registo não começa vazio, começa com a história.

### Restaurar

```bash
openssl smime -decrypt -binary -inform DER -in aos-<stamp>.tar.gz.enc \
  -inkey deploy/server/secrets-local/backup-key/backup.key -out bundle.tar.gz
tar xzf bundle.tar.gz          # MANIFEST, idp-db.sql, volumes.tar.gz, config.tar.gz
```

**Ao restaurar um bundle ANTERIOR ao último: importar o registo de apagamentos MAIS RECENTE antes de
arrancar o nó (AOS-436).** É o `apagamentos-<stamp>.txt` mais recente de `%USERPROFILE%\aos-backups`
— **não** o que vem com o bundle, que é tão antigo como ele. Com os volumes já repostos e o nó
**parado**:

```bash
# 0. a CHAVE do registo tem de estar no volume — e este passo IMPEDE continuar sem ela. Um bundle
#    anterior ao AOS-436 não a traz (MANIFEST: apagamentos-chave=ausente). Tire-a do bundle MAIS
#    RECENTE (decifrado como acima) e instale-a 0600 e do uid do nó (65532) — é material privado:
tar xzf volumes.tar.gz aos/apagamentos-dsar.txt.chave          # no bundle MAIS RECENTE
# SEMPRE por cima, e não «só se faltar»: se o nó arrancou antes de a importação estar configurada,
# já criou uma chave NOVA no volume e escreveu sob ela — mantê-la tornava o importado ilegível
# (achado N3 da terceira revisão). A chave certa é a do bundle mais recente, sempre.
docker run --rm -v aos_aos-data:/aos -v "$PWD/aos":/in:ro alpine:3.20 sh -c \
  'install -m 600 -o 65532 -g 65532 /in/apagamentos-dsar.txt.chave /aos/'
# Os passos 1-3 só correm COM a chave: sem ela o bloco pára, e o nó não arranca por este caminho.
if docker run --rm -v aos_aos-data:/aos alpine:3.20 sh -c 'test "$(wc -c < /aos/apagamentos-dsar.txt.chave)" -eq 32'; then
  # 1. o registo mais recente para DENTRO do volume de dados
  docker run --rm -v aos_aos-data:/aos -v /tmp:/in:ro alpine:3.20 sh -c \
    'install -m 644 /in/apagamentos-<stamp>.txt /aos/apagamentos-importado.txt'
  # 2. apontar o nó para ele — SUBSTITUINDO uma definição anterior, não acrescentando uma segunda
  sed -i '/^AOS_DSAR_ERASURE_REGISTER_IMPORT=/d' /opt/aos/.env
  echo 'AOS_DSAR_ERASURE_REGISTER_IMPORT=/var/lib/aos/apagamentos-importado.txt' >> /opt/aos/.env
  # 3. arrancar e CONFIRMAR — PROVADA, FUNDIDO, e o /readyz a 200
  docker compose -f /opt/aos/docker-compose.prod.yml --env-file /opt/aos/.env --env-file /opt/aos/image.env up -d aos
  docker logs aos-aos-1 2>&1 | grep -E 'reconciliacao do arranque|registo importado'
else
  echo 'SEM A CHAVE DO REGISTO (32 bytes) — NAO arranque: traga-a do bundle mais recente'
fi
# 4. DEPOIS de «FUNDIDO»: a variável SAI do .env, e só então o ficheiro pode ir embora
sed -i '/^AOS_DSAR_ERASURE_REGISTER_IMPORT=/d' /opt/aos/.env
```

O nó autentica cada linha (e a cadeia de MACs: uma linha removida, inserida ou trocada a meio parte-a),
exige que o importado contenha **tudo o que o bundle restaurado já sabe** — um registo mais antigo do
que o bundle é recusado —, aplica as linhas válidas e **destrói de novo** cada KEK que o Vault
restaurado tenha e que nasceu antes da destruição registada, com `dsar.key_reshredded` selado. Uma
KEK nascida **depois** (titular que voltou) fica intacta; uma sob **legal hold** não é destruída e
fica fechada. Aceite o importado, funde-o no registo próprio e **deixa de o ler** («registo importado
FUNDIDO»): apagar o ficheiro a seguir não fecha nada nesse processo. A variável tem de sair do `.env`
(passo 4) antes do próximo arranque — definida e sem ficheiro, esse arranque fica por provar.

**Confira o registo antes de o importar.** O encadeamento não vê o corte do **fim** do ficheiro: um
registo truncado é igual a um registo mais antigo. Compare o número de entradas com a última linha
`recolhido registo de apagamentos … (N entrada(s))` do `pull.log` da máquina do operador.

Se a linha disser **POR PROVAR**, a causa vem nela (Vault ainda selado, linha rejeitada, importado mais
antigo do que o bundle, chave em falta, KEK que não se deixou destruir). **Enquanto estiver por provar,
o nó não decifra nem escreve conteúdo por-titular nenhum** — o portão está na custódia, não no
`/readyz`, porque a sonda do contentor é o `/healthz` e o proxy encaminha tudo: um 503 no `/readyz`
sozinho não parava nada. Com o importado recusado, **nada é escrito** no registo próprio. A manutenção
da custódia repete a passagem a cada minuto.

**Se a chave do registo se perdeu, se o registo tem uma linha corrompida, ou se o nó arrancou antes
de a importação estar configurada.** O nó **nunca** cria outra chave por cima de um registo com
entradas — deixava-o ilegível para sempre — e fica por provar a dizê-lo. Recuperar: (a) repor
`apagamentos-dsar.txt.chave` do bundle mais recente (0600, uid 65532), que é o caminho normal e o
único para a chave perdida; (b) quando (a) não resolve — a chave não existe em bundle nenhum, **ou** o
registo tem uma linha corrompida a meio (cada linha autentica também a anterior, pelo que **apagar a
linha má não recupera**: a seguinte deixa de autenticar), **ou** o nó arrancou cedo e escreveu sob uma
chave própria que nenhuma cópia conhece — pôr o registo de lado
(`mv apagamentos-dsar.txt apagamentos-dsar.txt.orfao-<data>` dentro do volume) e arrancar: o nó cria
uma chave nova e volta a encher o registo **a partir da cadeia**. Os apagamentos que só esse registo
conhecia (não os da cadeia) deixam de estar protegidos contra um restauro — e as cópias recolhidas
também não autenticam sob a chave nova. É uma perda, e fica dita.

O [`restore-drill.sh`](restore-drill.sh) faz o mesmo no ensaio e **recusa** correr sem o registo
(segundo argumento), salvo declarado com `RESTORE_DRILL_SEM_REGISTO=1`; um bundle sem a chave pede-a
por `RESTORE_DRILL_CHAVE_DO_REGISTO=<ficheiro>`, e a chave fica 0600 e do uid 65532.

Este ciclo foi **exercitado**, não presumido: recolhido, decifrado com a privada local e o
conteúdo conferido — `events.wal`, `worm.wal`, o `pg_dump` com 87 tabelas, e a chave Transit
`aos-kek-…` no storage do Vault. Sem essa última, o resto não serviria de nada.

O `backup.sh` verifica cada artefacto que produz: PKCS#7 íntegro **e** do tipo `envelopedData` —
que confirma que o conteúdo está mesmo cifrado, e não só que o ficheiro é bem-formado.

### O backup só é um backup se o log estiver onde ele copia

Este script copia um **volume**. Isso só é um backup do Event Store enquanto o Event Store viver
nesse volume — e desde o [AOS-100](../../packages/cmd/aos/bootstrap.go) pode não viver. Com
`AOS_EVENTSTORE_NATS` preenchido, o log dos runs passa a viver num cluster NATS JetStream e
**precede** o WAL local; o `events.wal` do volume fica obsoleto, ou vazio.

O que acontecia sem guarda é o modo de falha caro: o `tar` do volume corria, o envelope PKCS#7
verificava, e o cron saía **verde** sobre um artefacto **sem o log dos runs**. Um backup verde e
vazio é pior do que backup nenhum — não falha o suficiente para alguém ir ver, e **ocupa o lugar
do alarme**.

O `backup.sh` faz agora duas verificações, e são perguntas diferentes:

| | Pergunta | Como responde | Se der errado |
|---|---|---|---|
| **passo 0** | que substrato está **configurado**? | `AOS_EVENTSTORE_NATS` no contentor `aos-aos-1` (o que corre hoje) **e** no `.env` (o que o próximo `deploy.sh` aplica) | **recusa**, nomeando a fonte, e não produz artefacto nenhum |
| **passo 2b** | que ficheiros o `tar` **trouxe mesmo**? | lê o índice de `volumes.tar.gz` e exige lá `aos/events.wal` e `aos/worm.wal` | **recusa**; um `events.wal` presente mas vazio passa com aviso — é legítimo num nó que nunca escreveu |

A primeira apanha o log que se mudou de casa; a segunda apanha o volume que se esvaziou. Nenhuma
substitui a outra. O passo 2b confirma ainda o **mapa** que usa (`AOS_EVENTSTORE_PATH` e
`AOS_WORM_PATH` do contentor contra `/var/lib/aos/…`): se o nó passar a correr com outros
caminhos, a guarda recusa em vez de verificar com sucesso o ficheiro errado.

> **A saída, e o que ela não é.** Com o log num cluster ainda se quer copiar Vault, IdP e
> configuração — e é isso que `BACKUP_EVENTSTORE_EXTERNO='<onde e como>'` permite. O texto fica
> gravado no `MANIFEST` (`eventstore=externo`), é o que o `restore-drill.sh` lê para recusar um
> ensaio que não poderia provar nada, e **não copia coisa nenhuma**: é uma declaração, não um
> mecanismo. Copiar o log replicado continua por fazer — o `backup.sh` deixou de o poder fingir.

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
   > **Mitigação que reduz isto a metade — feita:** o `worm.wal` vem directamente do servidor por
   > SSH (`-PorSSH`) em vez de ser extraído do backup cifrado. A cópia continua off-host — que é a
   > condição que interessa — e a tarefa diária deixa de precisar da `backup.key`. O custo é que o
   > WORM viaja fora do envelope do backup, protegido só pelo transporte. E desde 2026-09-15 a
   > chave SSH dessa tarefa **não é a de deploy**: é uma chave presa a um gate que só lê o WORM e
   > entrega a âncora (ver «A chave da selagem só sela», abaixo).

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
   | exige | checkpoints **em vigor** locais (`--anterior`) e que a chave do selador seja a da última selagem; sem isso **recusa**, antes de tocar no servidor | — |
   | traz | pedido `worm` ao gate: o `worm.wal` **vivo** em stdout, lido como o uid do nó, **sem cópia** no servidor | gate |
   | verifica | re-encadeia o store **antes** de assinar, e exige **continuidade** com a âncora em vigor | — |
   | sela | um checkpoint **por partição**; pisos em ficheiro separado | selador |
   | entrega | `scp -O` dos dois para nomes temporários, e o pedido `trocar` | gate |
   | troca | o gate valida o **par** e renomeia-o lado a lado; devolve o sha256 do que instalou, conferido com o que se selou | — |

   **A `backup.key` não entra, e a chave de deploy também não.** O passo «limpa» da versão anterior
   desapareceu com a cópia que limpava: o WORM passa em fluxo e não fica em disco no servidor.

   **Continuidade obrigatória.** A chave do selador foi rodada a 2026-09-15 e a primeira selagem com
   ela já foi feita; desde então o `selar-worm.ps1` **recusa** selar sem checkpoints em vigor. Uma
   selagem sem anterior não compara nada — numa tarefa que corre sozinha, seria a porta por onde uma
   truncatura passava a ser ancorada.

   **E não tem excepção, nem sequer para rodar a chave do selador.** Essa primeira selagem faz-se
   **à mão** — «Rotação das chaves de autoridade», passos (b) a (d) — e só depois do passo (g), com o
   par novo em `secrets-local`, é que esta tarefa volta a correr. O script é a **cadência**, não a
   rotação: um modo que selasse sem comparar com nada abriria, na tarefa automática, exactamente o
   buraco que a continuidade fecha.

   **O script guarda a pública do selador em `secrets-local/ancoras/selador.pub`** e recusa selar se
   ela mudou. Sem esse ficheiro, uma chave trocada aparecia como «o WORM DIVERGIU» — que se lê como
   adulteração quando é só a chave; com ele, a recusa é **antes** de contactar o servidor e diz que
   foi a chave que mudou, apontando para a rotação manual. É a mesma pública que vai na
   `AOS_WORM_TRUST_ANCHOR`, e fica arquivada com o par a cada selagem.

   > ⚠️ **Suspenda a tarefa diária durante uma rotação.** Antes do passo (g) ela recusa (a chave não
   > bate com o `selador.pub` em vigor); depois de (g) ela sela **e entrega**, e o gate **recusa** a
   > troca enquanto a `AOS_WORM_TRUST_ANCHOR` do `.env` for ainda a antiga — as assinaturas novas não
   > verificam contra ela. Não parte o nó, mas dá uma execução falhada e um alerta por nada. `Disable-ScheduledTask AOS-SelarWORM` antes de começar,
   > `Enable-ScheduledTask` depois de (f) e (g).

   **A entrega não reinicia o nó, nem precisa.** O nó só **usa** a âncora a partir do arranque: o par
   entregue hoje passa a valer no próximo restart, e até lá o nó fica com o que carregou — o que
   continua seguro, porque o WORM é append-only e a âncora anterior continua a verificar. **Mas a
   entrega é visível de imediato**, em `aos_worm_anchor_delivered_age_seconds`, relida a cada
   recolha: é essa — e **não** a `aos_worm_anchor_age_seconds`, que conta desde a âncora carregada —
   que diz se esta tarefa morreu. Ver «Como se sabe que a selagem morreu».

   **Porque a entrega é atómica e não ordenada.** Não há ordem segura entre os dois ficheiros:
   entregar os checkpoints primeiro deixa as partições novas **com checkpoint e sem piso**
   (`ErrBadWormExpectedHead`); entregar os pisos primeiro deixa os checkpoints antigos **abaixo dos
   pisos novos** (`ErrCheckpointStale`). Ambas impedem o nó de arrancar. Por isso os dois sobem com
   nomes temporários e é o `trocar` do gate que os renomeia, depois de validar o par. A janela
   residual — o intervalo entre dois `mv` — fica declarada: não é zero, e um arranque exactamente aí
   apanharia um par incoerente. Recupera-se correndo o ciclo outra vez.

   **Uma regra que só apareceu por correr os dois modos seguidos:** depois de selar do WORM vivo,
   selar de um backup **anterior** é um recuo, e a guarda recusa — com a mesma mensagem que
   significaria «alguém truncou o teu trilho». Escolha-se uma fonte e só se avance no tempo.

   ### A chave da selagem só sela

   A tarefa corre sozinha, pelo que a chave SSH **não tem passphrase**. A versão anterior usava a
   `deploy_key` — shell no `aos`, que está no grupo `docker`: root no servidor numa máquina de
   secretária. Perdeu-se, e não se refez assim. A selagem usa uma chave **dedicada**
   (`secrets-local/worm-seal/id_ed25519`, ACL só do dono) presa a um comando forçado,
   [`worm-seal-gate.sh`](worm-seal-gate.sh) — o desenho do `backup-pull-gate.sh` (§Backup) — que
   aceita exactamente quatro pedidos:

   | pedido | o que faz |
   |---|---|
   | `worm` | `docker run --rm --pull=never --log-driver none --network none --read-only --cap-drop ALL --security-opt no-new-privileges --user 65532:65532 -v aos_aos-data:/aos:ro alpine:3.20 cat /aos/worm.wal` |
   | `scp -t /opt/aos/ancoras/.checkpoints.novo` | recebe os checkpoints, com tecto de 16 MiB |
   | `scp -t /opt/aos/pisos/.heads.novo` | recebe os pisos, com tecto de 16 MiB |
   | `trocar` | valida o par e troca-o lado a lado; responde `TROCADO <sha256> <sha256>` |

   Tudo o resto é recusado. Recusas, leituras (`worm lido`) e trocas (`trocado: … checkpoints=<sha256>
   pisos=<sha256>`) vão ao syslog com a etiqueta `aos-worm-seal` — `journalctl -t aos-worm-seal`.

   **O que o `trocar` valida**, com o `python3` do sistema e a biblioteca ed25519 do sistema
   (`python3-nacl`, ou `cryptography`; sem elas **recusa** — não salta):

   - JSON sem BOM e sem chaves duplicadas;
   - checkpoints: array não vazio, **exactamente** os cinco campos de `audit.Checkpoint`, `EntryHash`
     de 32 bytes, `Signature` de 64, `Timestamp` RFC 3339, partições únicas;
   - **a assinatura de cada checkpoint**, contra a `AOS_WORM_TRUST_ANCHOR` do `.env` — a mesma que o
     nó usa no arranque — sobre a serialização canónica de `audit.canonicalCheckpoint`;
   - pisos: `{"partição": inteiro positivo}`;
   - os dois são **da mesma selagem**: as mesmas partições, e o piso de cada uma igual ao seu `AuditSeq`;
   - **nenhuma partição desaparece nem recua** face ao par em vigor — verificado **depois** das
     assinaturas.

   A não-regressão é a que importa contra quem leve a chave: sem ela, podia repor uma âncora
   **anterior** — legítima e assinada — para mascarar a truncatura do que veio depois. A
   consequência operacional: num host restaurado de um backup mais antigo do que a âncora em vigor, a
   troca **recusa**, e resolve-se à mão, por quem sabe porque é que o WORM recuou.

   **Porque é que a assinatura se verifica no gate, e não só no arranque.** A primeira versão deixava-a
   para o nó e dizia que um par mal assinado «se recupera com uma selagem legítima». Uma revisão
   adversarial (2026-09-15) mostrou que não: um par **bem formado** com `AuditSeq` enormes passava a
   forma e a não-regressão, o nó abortava no restart seguinte, e a partir daí a não-regressão
   **recusava todas as selagens legítimas**, que traziam números menores — só se recuperava com shell,
   e o `mv` já tinha destruído o par anterior. Por isso a assinatura corre antes da não-regressão, e o
   par anterior fica em `checkpoints.json.anterior` / `heads.json.anterior` (nomes que o nó não lê).

   **E a troca instala o que validou.** Na primeira versão o `receber` não usava o lock e o `trocar`
   validava os `.novo` no sítio: uma sessão `scp -t` parada podia escrever no mesmo inode depois da
   validação. Agora o lock é o mesmo nos dois verbos (um `receber` em curso faz o `trocar` recusar) e o
   `trocar` copia os `.novo` para ficheiros privados — inodes novos — e valida e instala essas cópias.

   **O que continua a ser só do nó:** confirmar que o `EntryHash` assinado corresponde ao registo real
   do WORM, o que exige o store composto. Quem levar esta chave consegue **ler o WORM**; entregar um par
   que o nó recuse exige também a `wormseal.key`.

   > **Ensaiado antes de chegar aqui (2026-09-15)** — `sshd` real em contentor (Python 3.8, o do
   > Ubuntu 20.04), cliente OpenSSH do Windows 9.5, `docker` substituído por um que só aceita o argv
   > exacto do gate, WORM de fixture e uma chave de selador de teste:
   >
   > - o ciclo `selar-worm.ps1 -PorSSH -Entregar` passa duas vezes seguidas, e depois pelo invólucro
   >   com um WORM que cresceu (partição nova incluída), sempre com o sha256 instalado igual ao
   >   selado; o par entregue verifica com `audit.VerifyFromCheckpointAtHead` — a função do arranque
   >   do nó — contra o WORM servido, e **falha** contra o mesmo WORM truncado ou reescrito;
   > - recusados **sem mexer no par em vigor**: shell, sessão sem comando, pty, `worm; id`,
   >   `worm <arg>`, SFTP (ler e escrever), `scp -O` para ou de outro caminho (`.env`, o
   >   `checkpoints.json` em vigor, `..`), `scp -p`/`-r` para os temporários, `scp -f` dos
   >   temporários, `-W`, e `-L` (o túnel abre do lado do cliente e não passa um byte);
   > - o `trocar` recusa o par **anterior** legítimo, um recuo coerente numa só partição, um par
   >   cruzado, BOM, campo a mais, chave duplicada, `EntryHash` curto, piso sem checkpoint, array
   >   vazio, só um dos dois, uma troca concorrente, e `python3` ausente; um upload de 17 MiB é
   >   cortado nos 16 MiB e a troca seguinte recusa-o;
   > - o par **real** de produção (234 partições) passa o validador; um par de teste por cima dele é
   >   recusado como recuo;
   > - a selagem recusa, **sem entregar nada**, um WORM truncado e um reescrito; sem checkpoints em
   >   vigor recusa **antes de contactar** o servidor; servidor inalcançável sai ≠ 0; o invólucro
   >   propaga o código e escreve-o no `ULTIMA.txt`;
   > - **rotação da chave do selador, seis casos com estado determinístico:** a cadência com a chave
   >   em vigor sela e entrega; a chave trocada com `selador.pub` presente é recusada **antes** de
   >   contactar o servidor (zero leituras do WORM); sem `selador.pub` é o selador que acusa
   >   (`DIVERGIU`) e a mensagem manda fazer a rotação à mão; sem checkpoints em vigor recusa antes do
   >   servidor; a rotação manual — (b) a (d) mais (g) — devolve a cadência ao normal na execução
   >   seguinte; e o `-ChaveNova` já não é um parâmetro do script;
   > - as flags do `docker run` correram no Docker real: o fluxo sai byte a byte igual, e outro uid
   >   leva `Permission denied` — é o `--user` que decide.
   >
   > **Não ensaiado, e falha em voz alta na primeira execução** (que por isso se corre à mão): a
   > versão do `docker` do servidor (precisa de `--pull`), a posse real do `worm.wal` (o gate assume
   > 600 do uid 65532, como o script anterior documentava) e o dono de `/opt/aos/ancoras` e
   > `/opt/aos/pisos` (o `aos` tem de poder escrever lá).

   #### Instalar — decisão do operador

   Na máquina do operador, a chave (sem passphrase; o `secrets-local/` já tem ACE único do dono, e o
   OpenSSH do Windows recusa uma chave legível por outros):

   ```powershell
   New-Item -ItemType Directory -Force C:\Jimy\AOS\deploy\server\secrets-local\worm-seal | Out-Null
   ssh-keygen -t ed25519 -N '""' -C aos-worm-seal -f C:\Jimy\AOS\deploy\server\secrets-local\worm-seal\id_ed25519
   ```

   O gate chega ao servidor pelo deploy (`deploy.yml` sincroniza-o para `/opt/aos/`). No servidor, como
   `aos`, com a `.pub` copiada para `/tmp/worm-seal.pub`:

   ```bash
   ls -l /opt/aos/worm-seal-gate.sh                   # chegou pelo deploy?
   ls -ld /opt/aos/ancoras /opt/aos/pisos             # o aos tem de poder escrever nos dois
   docker image inspect alpine:3.20 >/dev/null || docker pull alpine:3.20   # o gate não faz pull
   command -v python3                                 # sem ele o trocar recusa
   printf 'restrict,command="bash /opt/aos/worm-seal-gate.sh" %s\n' "$(cat /tmp/worm-seal.pub)" >> /home/aos/.ssh/authorized_keys
   ```

   A primeira execução é **à mão**, a olhar para ela (usa o `known_hosts` do utilizador, que a
   recolha dos backups já preencheu para este servidor):

   ```powershell
   powershell -ExecutionPolicy Bypass -File C:\Jimy\AOS\deploy\server\selar-worm.ps1 -PorSSH -Entregar
   ```

   ### A tarefa diária

   **A CONSOLA IMPORTA, e custou duas tentativas falhadas.** O `%USERNAME%` é sintaxe do
   `cmd.exe`: em PowerShell não expande, vai literalmente como texto, e o `schtasks` responde
   «não foi efetuado qualquer mapeamento entre nomes de contas e IDs de segurança». O nome da
   tarefa também não leva espaços — alinhado com a `AOS-RecolherBackups` que já existe.

   Em **PowerShell**:

   ```powershell
   schtasks /Create /TN "AOS-SelarWORM" /SC DAILY /ST 03:30 /RL LIMITED /F /TR "powershell.exe -NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File C:\Jimy\AOS\deploy\server\selar-worm-diario.ps1" /RU $env:USERNAME
   ```

   Em **cmd.exe**:

   ```
   schtasks /Create /TN "AOS-SelarWORM" /SC DAILY /ST 03:30 /RL LIMITED /F ^
     /TR "powershell.exe -NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File C:\Jimy\AOS\deploy\server\selar-worm-diario.ps1" ^
     /RU %USERNAME%
   ```

   Se a conta for recusada, prefixe-a com a máquina: `/RU "$env:COMPUTERNAME\$env:USERNAME"`.

   O `-WindowStyle Hidden` é o que a `AOS-RecolherBackups` já usa — sem ele, a selagem abre uma
   janela às 03:30.

   O `schtasks` **não expõe** as definições que decidem se a tarefa corre de facto — as mesmas da
   `AOS-RecolherBackups` (§Backup), e pelas mesmas razões. Ajustam-se logo a seguir:

   ```powershell
   $t = Get-ScheduledTask AOS-SelarWORM
   $t.Settings.StartWhenAvailable         = $true    # recupera uma execução perdida (máquina desligada)
   $t.Settings.DisallowStartIfOnBatteries = $false   # a bateria deixa-a em Queued, sem erro nenhum
   $t.Settings.StopIfGoingOnBatteries     = $false
   $t.Settings.ExecutionTimeLimit         = 'PT1H'   # uma execução presa não bloqueia as seguintes
   Set-ScheduledTask -InputObject $t
   ```

   O `ssh` da selagem leva `ConnectTimeout` e `ServerAliveInterval`: uma sessão que pendure depois
   de aberta morre em ~60 s em vez de esperar pelo `ExecutionTimeLimit` (visto na recolha, 2026-09-14).
   O `go run` do selador precisa do `go` no PATH do utilizador da tarefa; em alternativa, o invólucro
   aceita `-IssuerExe` com um `aos-issuer.exe` compilado.

   E CONFIRME QUE FICOU E QUE CORRE, porque uma tarefa presumida é pior do que nenhuma: ficam ambos
   os lados à espera de uma cadência que não corre, e dias depois alguém conclui que a âncora está
   fresca.

   ```powershell
   schtasks /Query /TN "AOS-SelarWORM" /FO LIST
   Start-ScheduledTask AOS-SelarWORM
   Get-ScheduledTaskInfo AOS-SelarWORM   # LastTaskResult 0 no fim; 267009 = ainda a correr
   Get-Content "$env:USERPROFILE\aos-selagem-logs\ULTIMA.txt"
   ```

   **Aponta ao `selar-worm-diario.ps1` e não ao `selar-worm.ps1` directamente.** O invólucro
   guarda a saída em `%USERPROFILE%\aos-selagem-logs` e propaga o código de saída **intacto** para
   o agendador. Sem ele, o que sobra de uma falha diária é uma coluna «Last Result» com um número:
   a selagem toca no WORM de produção, traz material por SSH e entrega uma âncora, e quem for ler
   a falha quer saber porquê — não vai reproduzi-la à mão às 03:30 do dia seguinte.

   Um erro diário que ninguém lê é um erro que não existe.

   Para saber se a cadência está viva sem ordenar um directório por data:

   ```
   type %USERPROFILE%\aos-selagem-logs\ULTIMA.txt
   ```

   O `schtasks` pede a password do Windows por causa do `/RU` — é o Windows a pedi-la, e ela não
   passa por mais lado nenhum. **Sem `/RU`, a tarefa só corre com sessão iniciada** — o que numa
   máquina que se desliga à noite significa que não corre. É a escolha entre dar a password ao
   agendador do Windows ou aceitar que a cadência depende de haver sessão.

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
   número, em vez de dizer que a verificação ancorada está desligada.

### Onde as métricas vão parar — e o que isso NÃO dá

Encontrado a 2026-08-21, a perguntar **«alguém lê estas séries?»** depois de acrescentar nove
delas: o colector raspava `aos:8080/metrics` de 15 em 15 segundos e exportava-as para o `debug`,
cuja saída é de nível **INFO** — enquanto `service.telemetry.logs.level` está em `warn`.

**As séries eram recolhidas, empacotadas e deitadas fora em silêncio.** Tudo o que este documento
descreve como «alertável» — idade da âncora do WORM, folga do orçamento, saúde do varredor de
retenção, falhas de export OTLP — caía num buraco negro. O alvo respondia bem (verificado: HTTP
200, 89 séries); o problema era do outro lado.

Agora o pipeline de métricas exporta para um `prometheus` que serve tudo em `otel:9464`, dentro da
rede do compose e invisível do host.

**O que isto dá:** as séries ficam legíveis e raspáveis num sítio estável.

```
docker run --rm --network aos_default curlimages/curl -s http://otel:9464/metrics | grep aos_worm
```

**O que isto NÃO dá, e é metade da verdade:** não há retenção histórica nem regras de alerta.
Alertar exige um Prometheus (ou equivalente) apontado a este endpoint — decisão de infra **por
tomar**. Até lá, «alertável» quer dizer «um alerta pode ser escrito contra isto», não «há um
alerta a correr».

**As TRACES continuam a ir para o `debug`, ou seja, a ser descartadas.** O
`aos_otlp_spans_exported_total` conta os spans que o colector **aceitou**, não os que alguém
guardou. Fica nomeado em vez de escondido; ligar um backend de traces é a mesma decisão de infra.
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

   Cinco séries em `/metrics` fecham isso:

   | série | o que diz | lida quando |
   |---|---|---|
   | `aos_worm_partitions` | partições que o WORM tem **agora** | na recolha |
   | `aos_worm_partitions_anchored` | partições cobertas pela âncora que passou no arranque | no arranque |
   | `aos_worm_anchor_age_seconds` | segundos desde a selagem que produziu a âncora **EM USO** (verificada) | no arranque |
   | `aos_worm_anchor_delivered_age_seconds` | segundos desde a última selagem presente no **ficheiro montado** | na recolha |
   | `aos_worm_anchor_delivered_unreadable` | `1` = o **par** montado (checkpoints **+ pisos**) não passa a validação de forma do arranque | na recolha |

   **O alerta da selagem morta é `aos_worm_anchor_delivered_age_seconds > 172800 OU < 0`** (48 h). O
   limiar é o dobro da cadência, pela mesma razão que o `pull-backups.ps1` usa 48 h — um dia
   falhado não alerta, dois sim.

   **E corre, desde 2026-09-15 — no servidor, com aviso por push.** Até aí era uma regra escrita
   que nada avaliava: o `otel` expõe a `:9464` e ninguém a lia. [`alerta-ancora.sh`](alerta-ancora.sh)
   corre no cron do `aos` a cada 15 min, lê as duas séries de entrega pela rede interna (a mesma
   alpine fixada do gate da selagem, sem pull e sem capabilities) e publica num tópico **ntfy**
   privado. **No servidor e não na máquina do operador**, por uma razão só: a selagem corre nessa
   máquina, e um alerta avaliado lá ficava cego exactamente quando a selagem morre por ela estar
   desligada.

   | dispara quando | aviso |
   |---|---|
   | idade `> 172800` ou `< 0`, `_unreadable 1`, séries ausentes, ou o `otel:9464` sem resposta | **2 leituras seguidas** (30 min) — a janela sustentada que o corolário abaixo pede |
   | continua em falha | lembrete de 24 h em 24 h |
   | volta a `ok` | aviso de recuperação |

   Um aviso que não sai (ntfy em baixo, tópico inválido) **não conta**: tenta de novo na execução
   seguinte, e fica no syslog (`journalctl -t aos-alerta-ancora`). Para o ntfy só vão o título, o
   motivo (nomes de séries e horas) e o host. O tópico é o segredo — quem o souber lê e publica — e
   vive em `/opt/aos/secrets/ntfy-topico` (600), dentro do backup cifrado.

   ```bash
   # instalar (uma vez; o script chega pelo deploy)
   printf '%s' '<tópico>' > /opt/aos/secrets/ntfy-topico && chmod 600 /opt/aos/secrets/ntfy-topico
   bash /opt/aos/alerta-ancora.sh --teste          # tem de chegar ao telemóvel; não mexe no estado
   ( crontab -l; echo '*/15 * * * * /bin/bash /opt/aos/alerta-ancora.sh >/dev/null 2>&1' ) | crontab -
   ```

   **O `< 0` não é defensivo, é o buraco por onde o alerta se cala.** O carimbo vem do relógio de
   **quem sela** (a máquina que corre a tarefa), comparado com o relógio do nó. Um relógio
   adiantado — fuso mal configurado, *skew*, ou quem tenha escrita no volume — dá idade
   **negativa**, que nunca cruza `172800`. O nó **não apara** o valor para `0` de propósito: `0`
   leria-se «acabado de selar» e seria indistinguível de saúde. Negativo é absurdo à vista.

   **E NÃO É o `aos_worm_anchor_age_seconds`, apesar de o ser até 2026-09-15.** O nó lê
   `AOS_WORM_CHECKPOINT_FILE` **uma vez, no arranque**, e a entrega diária substitui o ficheiro
   **sem reiniciar nada** — por desenho (§"A tarefa diária"). Logo, num nó que fique de pé, a idade
   da âncora em uso cresce **24 h por dia com a tarefa de selagem viva**, e o alerta disparava dois
   dias depois do último arranque, sempre. Um alerta que grita num nó saudável deixa de ser lido no
   dia em que gritar a sério. A série continua a existir, com a pergunta a que responde de verdade:
   **quanto do WORM está por re-encadear desde o selo que este processo verificou**.

   **As duas idades juntas dizem mais do que cada uma:**

   | entregue | em uso | leitura |
   |---|---|---|
   | baixa | alta | **normal** num nó de pé — a entrega de hoje só entra em vigor no próximo arranque |
   | alta | alta | **a tarefa de selagem morreu** |
   | alta | baixa | nó acabado de reiniciar sobre um ficheiro **velho** — a selagem está parada há mais tempo do que o processo |

   **A série entregue NÃO é verificada, e o `HELP` di-lo.** Ninguém validou aquela assinatura contra
   o *trust-anchor* nem aquele piso contra a cadeia: isso exige o store composto e acontece **só no
   arranque**, fail-closed. Quem escreve o ficheiro escolhe o `Timestamp`. Vale como sinal de **vida
   de uma tarefa**, nunca como prova de cobertura — confundir as duas seria trocar uma prova por um
   carimbo de data, que é a forma de falha que a verificação ancorada existe para fechar.

   **`delivered_unreadable 1` é um arranque abortado anunciado com antecedência.** A leitura relê o
   **par** — checkpoints **e** pisos — e repete a validação de **forma** que o arranque faz,
   incluindo a que decide: *toda a partição com checkpoint tem de trazer piso > 0*. Sem esta série,
   um par partido e uma âncora desligada seriam o mesmo silêncio, e a diferença só aparecia num
   restart que já não volta.

   **Lê-se o par, e não só os checkpoints, por causa da própria entrega.** Ela troca os dois
   ficheiros com **dois `mv` consecutivos** (§"Porque a entrega é atómica e não ordenada"): entre
   eles — e **permanentemente** se o segundo falhar, caso em que o script avisa «o par pode estar
   incoerente» e termina — os checkpoints são novos e os pisos velhos, uma partição nova fica com
   checkpoint e **sem piso**, e o nó deixa de arrancar. Uma releitura só-checkpoints dava isso por
   saudável. **Corolário para quem escrever a regra:** alerte com **janela sustentada**, porque a
   troca legítima atravessa esse estado durante alguns milissegundos por dia.

   ⚠️ **A série é ASSIMÉTRICA, e prometer o contrário seria repetir aqui o erro que ela corrige
   noutra:** `1` ⇒ o arranque abortaria; **`0` NÃO garante que o nó arranca**. A assinatura contra
   o *trust-anchor* e a frescura contra a cadeia (`ErrCheckpointStale`) exigem o store composto, e
   isso só existe no arranque. Um par bem-formado mas assinado por outra chave, ou abaixo do piso,
   lê `0` aqui e aborta lá.

   A razão de as três séries de recolha serem lidas **na altura da recolha** e não no arranque: um
   valor medido no boot e servido como *gauge* parece vivo estando congelado. Faria o contrário do
   que a métrica existe para fazer — que foi, à letra, o defeito do `anchor_age`.

   **Sem âncora composta, as séries de âncora NÃO saem** — e sem caminho montado (âncora injectada
   em processo) as duas de entrega também não. Emitir `anchored 0`, `age 0` ou `unreadable 0` faria
   um nó desprotegido parecer um nó acabado de selar — pior do que não emitir nada. É a mesma regra
   das séries de OTLP.

9. **Sem preço por token — o modelo é pago por subscrição.** O alias `gpt-4o-mini` do LiteLLM
   encaminha para `openai/kimi-for-coding` (`api.kimi.com/coding/v1`), pago por subscrição, pelo que
   **não se monta tabela de preços** (decisão do dono, 2026-09-17): o preço da OpenAI daria um custo
   preciso e falso. Desde o AOS-406, cada turno sai marcado como custo **não derivado** — o span `chat`
   leva `aos.cost.undefined=true` em vez de custo, o `turn.recorded` leva `custo_nao_derivado: true`, e
   o SLI `cost_per_trajectory` não conta esses runs: fica **sem amostras**, e o alerta de custo sai
   com `produtor="0"` (a regra nunca dispara neste nó), nunca verde com zeros. A dimensão que decide é tokens (`AOS_BUDGET_MAX_TOKENS`); um tecto em dólares
   continua recusado no arranque por falta de fonte de preço. Se o modelo passar a ser pago por token,
   monta-se a tabela em `AOS_MODEL_PRICING_PATH` com uma entrada **com o nome do alias** (`gpt-4o-mini`,
   `eu` — é esse par que o nó consulta) e as **taxas do modelo que serve** (não as da OpenAI).
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
13. ~~**A auditoria não regista quem aprovou.**~~ **RESOLVIDO — e provado a 2026-08-21.** A
   cerimónia *four-eyes* sela em `governance.control` as **identidades dos aprovadores**
   (`four_eyes.approvers`) e o **id do grant** (`four_eyes.grant`), a par do
   `human_gate: "satisfied"` que o PDP já registava.

   **O código já o fazia; o que faltava era a prova — e este documento dizia o contrário.** Os
   três estados discordavam: o handler preenchia, nenhum teste o exercitava, e a §13 declarava a
   lacuna aberta. Nenhum deles é inofensivo: uma lacuna **inventada** trava a mesma acção que uma
   escondida, e uma correcção **sem prova** é uma que a próxima refactorização apaga sem ninguém
   notar. Para uma autorização cujo propósito **é** o não-repúdio, esta é a propriedade central.

   Três testes, com as mutações a cair: selar sem nomear os aprovadores; a obrigação a devolver
   vazio; e selar **também** as recusas — que inflaria a cadeia com avales que não houve.

   **Limite declarado:** o selo nomeia identidades e nada mais. A assinatura e o challenge da
   perna **não** entram na cadeia (há um teste que o exige): são material de autenticação, e a
   hash-chain é lida por quem tem autoridade de **auditoria**, que não é a mesma coisa que
   autoridade para reapresentar uma prova.

   Contexto original em
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
passa se os controlos também valerem**: token válido `200`, mesmo token outra vez `401`, sem
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

**O ensaio também recusa, e por duas razões distintas.** Nenhuma delas é sobre o backup estar mau:

- **O bundle diz que não traz o log.** O `MANIFEST` com `eventstore=externo` é recusado ao
  desembrulhar. Sem essa leitura, o ensaio seguia e falhava três minutos depois no passo 6 com
  *"não encontrei nenhum run no WORM restaurado"* — que se lê como "o backup está corrompido" e
  não como "este backup nunca teve o log".
- **O nó de produção corre sobre um cluster.** O `env` do nó de ensaio é **copiado** do contentor
  de produção; se este trouxer `AOS_EVENTSTORE_NATS`, o nó do ensaio ligar-se-ia ao cluster
  **real** — passaria a **escrever no log de produção**, e o `200` do passo 7 viria do cluster
  vivo e não do *bundle*. O ensaio passaria sem provar nada, que é o oposto daquilo para que
  existe. Apontá-lo a um cluster de ensaio com
  `RESTORE_DRILL_EXTRA_ENV='AOS_EVENTSTORE_NATS=…'` continua a ser uso legítimo, e diz-se no log.
- **A produção tem a âncora do WORM ligada e o bundle não a traz.** Com `AOS_WORM_TRUST_ANCHOR`
  herdada, o nó só arranca com `ancoras/checkpoints.json` e `pisos/heads.json`. O ensaio monta-os
  **do bundle**, nunca de `/opt/aos` — montados da produção, passaria com um backup que não os leva,
  e um host perdido não tem `/opt/aos` para emprestar. Até 2026-09-14 o `backup.sh` **não os
  copiava**: um host restaurado subia com a âncora ligada e sem os ficheiros, e o nó abortava no
  arranque. Hoje copia-os, grava `worm-ancora=` no `MANIFEST`, e com a âncora ligada a falta deles é
  recusa antes de produzir artefacto.

---

## Passar à v0.1.11 em produção: duas portas novas que o `.env` actual não satisfaz

A primeira tentativa de entregar a `v0.1.11` (2026-09-13) derrubou o nó: o contentor novo abortava
no arranque e reiniciava em ciclo, e a produção só voltou com `rollback.sh` e o **digest explícito**
da `v0.1.10`. A causa não estava na imagem nem no healthcheck: a `v0.1.11` acrescenta portas
fail-closed de produção que um `.env` preparado para a `v0.1.10` não tem.

| Porta nova | O que o nó exige | Sintoma no log |
|---|---|---|
| Autoridade da destruição DSAR (AOS-367) | `AOS_DSAR_ERASERS` não-vazio, com emitterIDs de `AOS_OPERATORS` | `AOS_MODE=production exige AOS_DSAR_ERASERS nao-vazio` |
| Egress endurecido do modelo (AOS-366) | `AOS_MODEL_ENDPOINT` em **https**, verificado no arranque, sem excepção para hosts internos | `ErrInsecureBaseURL` / `BaseURL de egress tem de ser https` |

A segunda só aparece depois de a primeira estar resolvida: o nó aborta na primeira porta que falha.
Por isso as duas resolvem-se **antes** de voltar a entregar, e não uma a cada tentativa.

### `AOS_DSAR_ERASERS`

Decisão do operador, não do repositório: é a lista de quem pode ordenar crypto-shred. Um restauro
**já não** o desfaz em silêncio: o nó re-destrói a KEK que um bundle anterior traga de volta e fecha o
conteúdo enquanto não o provar ([AOS-436](#o-registo-de-apagamentos-sai-do-bundle-em-claro-aos-436));
o que fica de fora são os apagamentos posteriores ao último backup. Ver `.env.example`. Com um só eraser, `/dsar/expire` por rota fica indisponível
(exige duas assinaturas distintas); a expiração automática por TTL continua a correr.

### O modelo em https, sem sair da rede interna

A LiteLLM deste compose servia `http` na 4000. O TLS passa a ser **opt-in** por uma variável:

```bash
bash deploy/server/gen-litellm-tls.sh        # na máquina do operador
```

Gera, em `secrets-local/litellm-ca/`, uma **CA dedicada** e a folha `litellm` (SAN `DNS:litellm`), e
imprime os passos para o servidor: copiar `litellm.crt`/`litellm.key` para
`/opt/aos/tls-internal/litellm/`, **acrescentar** a `ca.crt` ao bundle de `AOS_INTERNAL_CA_BUNDLE`, e
no `.env`:

```bash
LITELLM_TLS_ARGS="--ssl_certfile_path /app/tls/litellm.crt --ssl_keyfile_path /app/tls/litellm.key"
AOS_MODEL_ENDPOINT=https://litellm:4000/v1
```

> ⚠️ **As aspas em `LITELLM_TLS_ARGS` são obrigatórias.** O `deploy.sh` carrega o `.env` como script
> bash (`set -a; . .env` sob `set -e`). Sem aspas, o bash atribui só `--ssl_certfile_path` e tenta
> executar `/app/tls/litellm.crt` como comando: o deploy morre ao ler o `.env`. O compose aceita as
> duas formas, por isso um `docker compose config` não o apanha — a primeira versão desta secção
> trazia o exemplo sem aspas.
>
> Pela mesma razão, **não escrevas esta linha através de um `ssh '...'` a partir do PowerShell 5.1**:
> ele retira as aspas duplas dos argumentos passados a executáveis nativos, e a linha chega ao
> servidor sem elas. Edita o `.env` numa sessão SSH interactiva.

Três decisões deste desenho, e porquê:

- **Opt-in, e não TLS imposto.** O `deploy.yml` sincroniza o compose **antes** de trocar a imagem. Um
  TLS obrigatório deixaria sem modelo um nó ainda configurado com `http`. Com a variável vazia, nada
  muda.
- **CA dedicada, e não a CA interna.** A chave da CA que assinou `idp` e `vault` não está disponível.
  Um bundle aceita várias CAs, portanto acrescenta-se esta e os certificados existentes continuam a
  validar pela antiga. ⚠️ Sem essa chave, os certificados de `idp` e `vault` **não se renovam**: a
  rotação para uma CA nova tem de ser planeada antes de expirarem.
- **`nameConstraints` restrito a `DNS:litellm`.** O `SSL_CERT_FILE` é o trust store do **processo
  inteiro** do nó. Sem restrição, quem obtivesse a chave desta CA podia personificar o `idp` ou o
  `vault` perante o nó; com ela, só consegue personificar a LiteLLM.
