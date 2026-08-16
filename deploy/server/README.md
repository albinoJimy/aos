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
- **`aos-node`** — cliente público, *password grant*, para leitores **humanos**, cada um com o seu
  atributo `board`.

A distinção importa: um service account colapsa "quem lê" numa identidade de máquina. A soberania
**por-leitor** — que é o argumento de todo o mecanismo — só é real quando existirem identidades
humanas distintas. Hoje não existem.

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

```bash
# recolher para a máquina do operador (o único passo que cobre a perda do host)
scp -i deploy/server/secrets-local/deploy_key \
  aos@37.60.241.150:/opt/aos/backups/aos-*.tar.gz.enc ./backups/
```

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
6. **Os backups não saem do host sozinhos.** Existem, são cifrados e o ciclo de restauro está
   exercitado (ver §Backup) — mas vivem no **mesmo disco**. Isso cobre apagamento do volume,
   corrupção e um deploy mau; **não** cobre perda da máquina nem falha de disco. A recolha para
   fora é um comando manual, e enquanto for manual não é uma garantia. Automatizá-la exige um
   destino que ninguém escolheu ainda.
7. **Soberania por-leitor ainda não é real.** O read-path exige credencial verificada e a recusa
   cross-region **está provada**, mas em uso está uma identidade de **máquina** partilhada
   (`aos-reader`). O mecanismo funciona; falta-lhe exercício — enquanto não houver leitores
   humanos distintos com o seu próprio `board`, há uma fronteira só.
10. **A raiz humana da cadeia de delegação é auto-declarada.** O NHI traz
   `delegation_chain` enraizada num humano com `auth_method: manual` — quem detém a `issuer.key`
   declarou-o por *flag*. Nada prova que esse humano autorizou o que quer que seja. O
   `aos-issuer` suporta `--assertion` com ID-token OIDC verificado, e agora existe um IdP para o
   servir; ligar as duas pontas fecha o eixo de ADR-003, que hoje está sintacticamente presente e
   semanticamente vazio.
8. **A verificação ancorada do WORM não corre.** Sem `AOS_WORM_TRUST_ANCHOR` +
   `_CHECKPOINT_FILE` + `_EXPECTED_HEAD`, fica só a re-verificação de hash-chain: apanha mutação,
   remoção e encadeamento quebrado, mas **não** truncatura do tail nem reescrita desde a génese.
   O banner de arranque di-lo em cada boot.
9. **Sem tabela de preços.** O par (`gpt-4o-mini`, `eu`) não consta da tabela embebida, pelo que
   o custo derivado é **zero por ausência de dados** — não custo nulo. A dimensão que decide é
   tokens (`AOS_BUDGET_MAX_TOKENS`); um tecto em dólares seria recusado no arranque por falta de
   fonte de preço, em vez de comparar sempre contra zero.
