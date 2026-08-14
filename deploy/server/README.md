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
   │                                ▼                                 │
   │             aos (distroless, non-root, root-fs read-only)        │   rede interna: sem
   │              ├─ volume aos-data  (Event Store WAL + WORM)        │   porta publicada
   │              └─► otel-collector  (traces + scrape /metrics)      │
   └──────────────────────────────────────────────────────────────────┘
```

Uma só porta chega ao mundo: **8444/tcp**. O nó serve em claro apenas na rede interna do
compose (`expose`, nunca `ports`) e declara `AOS_TLS_EXTERNAL_TERMINATION=1` — declaração que o
`edge` honra ao cifrar o transporte.

---

## Onde vive cada chave

É a decisão estruturante desta configuração, e a razão de existir o `gen-identity.sh`.

| Material | Nasce em | Vive em | Porquê |
|---|---|---|---|
| `issuer.key` (assinatura de identidade) | máquina do operador | **máquina do operador** | O nó corre *trust-anchor-only*: verifica com a pubkey e nunca assina. Se a privada vivesse no servidor, quem o comprometesse mintaria a sua própria identidade — e a separação de *trust-domains* (ADR-006) seria decorativa. |
| `operator.seed`, `ratifier.seed`, `approver-*.seed` | máquina do operador | **máquina do operador** | Idem: `steer`/`pause`, promoção e *four-eyes* são autoridade **sobre** o nó, não autoridade **do** nó. |
| Pubkeys (issuer, operadores, ratificadores, aprovadores) | derivadas das seeds | `/opt/aos/.env` + `secrets/approvers.json` | Material **público**. É tudo o que o servidor precisa para verificar. |
| Trust anchor do PDP | `packages/control-plane/pdp/policies/trust_anchor.pub` | `/opt/aos/.env` (hex) | Forçado *out-of-band*: nunca lido do directório mutável do bundle, senão quem tivesse escrita lá trocava âncora **e** assinatura de uma vez. |
| Chave TLS do edge | servidor (`provision.sh`) | **servidor** | Única privada no servidor. Cifra transporte; não autentica sujeitos nem autoriza nada. |
| Chave de release (DSSE) | custódia do Arquitecto de Plataforma | secret `AOS_RELEASE_KEY` | Ver [`../node/CUSTODIA-CHAVE-RELEASE.md`](../node/CUSTODIA-CHAVE-RELEASE.md). |

O servidor, portanto, **não guarda nenhuma credencial que conceda autoridade sobre o sistema**.

---

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

## TLS real

O `provision.sh` gera *self-signed* porque um IP não tem certificado de CA pública.

⚠️ **O caminho óbvio não funciona neste host.** `certbot --standalone` precisa da porta 80, que
pertence ao `ingress-nginx` do cluster — o container nem chega a arrancar. O desafio **HTTP-01
está fechado** a qualquer emissor fora do cluster.

Restam dois caminhos, e o domínio já existe (`elysiumii.site`, com `api.`, `grafana.` e
`longhorn.` servidos pelo ingress):

**DNS-01 — independente do cluster, é o recomendado.** Não usa a porta 80 de todo: prova-se a
posse do domínio por registo TXT. Com o *plugin* do teu operador de DNS:

```bash
docker run --rm -it -v /opt/aos/letsencrypt:/etc/letsencrypt certbot/dns-cloudflare certonly --dns-cloudflare --dns-cloudflare-credentials /etc/letsencrypt/cf.ini -d aos.elysiumii.site
```

Depois aponta `secrets/tls/edge.crt`/`edge.key` para `fullchain.pem`/`privkey.pem`, põe
`AOS_EDGE_HOST=aos.elysiumii.site` no `.env` e tira o `-k` do smoke externo em `deploy.yml` —
passa a ser também um teste da cadeia de confiança, e não só da porta.

**Via cert-manager do cluster.** Seria o coerente com os certificados que já lá estão, mas **não
é viável hoje**: os três pods do `cert-manager` estão `Pending` há 25 dias. Os certificados
existentes (`grafana-tls`, `neural-hive-gateway-tls`, `longhorn-tls`) ainda constam como válidos
porque foram emitidos há 195–247 dias — mas **ninguém os vai renovar** enquanto o cert-manager
não voltar. É um problema do cluster que existe independentemente do AOS, e convém olhar para
ele antes de expirarem.

---

## Passar a `AOS_MODE=production`

O nó **aborta o arranque** em modo `production` sem, todas: `AOS_ISSUER_PUBKEY` (já está),
`AOS_BOARD_REGIONS` não-vazio (já está), terminação TLS declarada (já está), e credencial forte
de soberania — `AOS_SOVEREIGN_OIDC_ISSUER` + `AOS_SOVEREIGN_OIDC_AUDIENCE`.

Só esta última falta, e exige um **IdP OIDC real**. O que muda com ela: o *read-path* soberano
deixa de aceitar o header auto-declarado `X-Aos-Board` e passa a derivar o board das *claims* de
um ID-token verificado. Há um Keycloak com realm pronto em
[`../node/dev-hardened/`](../node/dev-hardened/) (`keycloak/realm-aos.json`,
`docker-compose.oidc.yml`) que serve de ponto de partida.

Enquanto isso não existir, correr sem `AOS_MODE=production` é a postura **honesta**: um nó
exposto sem essa variável não é um nó de produção, é um nó de referência a servir tráfego — e o
banner de arranque di-lo em cada boot.

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
   ~95% de um core e a *load average* observada foi 21–36 numa máquina de 8. O `mem_limit` de
   1 GB protege os vizinhos do nó, mas não protege o nó dos vizinhos: sob contenção, espera
   latência de mediação acima dos alvos de `tecnica/10`.
5. **Sem Model Gateway.** Por omissão o nó usa o modelo de **referência** (turno único fixo):
   valida o pipeline, não faz trabalho real. Ligar `AOS_MODEL_ENDPOINT`/`AOS_MODEL_NAME` —
   secção comentada no `.env.example`.
