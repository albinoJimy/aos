# Módulo `eventstore`

Provisiona o **Event Store (ES)** — NATS com JetStream, o log append-only que é a
fonte de verdade do AOS, com transporte push (ADR-007).

- **dev**: `cluster_size = 1` (single-node, arranque rápido).
- **staging**: `cluster_size >= 3` (cluster replicado full-mesh; sem SPOF).
- **Persistência**: JetStream em volume Docker por nó → `apply` idempotente,
  `destroy` limpo (o volume é removido com o container).
- **Exposição mínima**: só o nó 0 publica portas no host; os restantes comunicam
  apenas na rede interna do ambiente.

> Nota: consome as portas de cliente/monitorização a partir do nó 0. Um cluster
> real de produção usaria orquestração multi-host — aqui provisiona-se o suficiente
> para dev/staging locais *(proposta pragmática de ambiente, não desenho de produção)*.
> Em concreto: o `provider "docker"` da raiz aponta para **um só** `docker_host`, pelo
> que as réplicas provisionadas aqui **partilham domínio de falha** — replicam os
> dados, não a falha. Limitação conhecida e **não** resolvida por este módulo.

## Soberania regional: sem `server_tags`, um nó com fronteira declarada NÃO ARRANCA

Cada nó anuncia `server_tags: ["region:<region>"]`. Não é decoração de
observabilidade: é a metade do contrato de soberania (ADR-011, AC5 do AOS-100) que
vive do lado do **servidor**. A cadeia, medida contra um cluster real:

1. `deploy/server/docker-compose.prod.yml` declara `AOS_BOARD_REGIONS` como
   **obrigatória** (`${AOS_BOARD_REGIONS:?}`) — o container nem sobe sem ela.
2. Com `AOS_BOARD_REGIONS` e `AOS_EVENTSTORE_NATS` preenchidos, a guarda **(1c)** de
   `packages/cmd/aos/bootstrap.go` **exige** `AOS_EVENTSTORE_NATS_REGION`
   (`ErrEventStoreReplicadoSemRegiao`), e exige que essa região pertença a um dos
   boards declarados.
3. O adaptador `packages/substrate/eventstore/jetstream/soberania.go` cria o stream com
   `placement` restrita a `region:<regiao>` — a constante `TagDeRegiao = "region:"` — e
   **lê a colocação de volta** da configuração armazenada, abortando fail-closed se
   divergir. Não basta pedir: verifica-se.
4. Contra um cluster cujos nós **não anunciem essa tag**, nenhum par é elegível e o
   servidor **recusa criar o stream** (`no suitable peers`, `err_code=10005`, medido).
   O arranque do nó aborta aí.

Ou seja: **sem estas tags o Event Store sobe, mas o AOS não.** E a falha aparece no
arranque de um nó soberano — não no `apply` da infra, que fica verde à mesma.

Notas de implementação, e porquê:

- O `nats-server` **não tem flag de linha de comando** para `server_tags` — só
  configuração. O módulo injecta um fragmento próprio em
  `/etc/nats/aos-soberania.conf` (via `upload`) e arranca com `-c` para esse ficheiro.
  Ficheiro **distinto** do `nats-server.conf` da imagem, para não lhe tocar; as flags
  continuam a mandar em tudo o resto.
- A região é normalizada com a **mesma** regra de `normalizarRegiao()` (minúsculas, sem
  espaços em redor). Duas normalizações diferentes fariam `EU-West-1` e `eu-west-1` ser
  a mesma região de um lado e regiões distintas do outro.
- A tag vai a **todos** os nós, incluindo o single-node de dev: um cluster em que só
  alguns pares anunciam a região tem menos pares elegíveis do que réplicas pedidas, e a
  criação do stream é recusada na mesma.
- `region` é a região **nua** (`eu-west-1`). O prefixo `region:` é do módulo; passá-lo
  já prefixado produziria `region:region:...` — recusado por `validation`.
- Na raiz, o módulo recebe `local.effective_replica_region` (a região das **réplicas**,
  que é onde estes nós as guardam), já validada pela `validation` de `replica_region` e
  pelo `check "soberania_regional"` de `infra/main.tf`. As `server_tags` são a terceira
  camada — e a única que o **servidor** impõe.

Teste: `infra/tests/eventstore_server_tags.tftest.hcl` (offline, `mock_provider`).

## Entradas

| Nome | Tipo | Descrição |
|---|---|---|
| `name` | string | Prefixo (ex.: `aos-dev`). |
| `network_name` | string | Rede do módulo `network`. |
| `image` | string | Imagem NATS pinada. |
| `region` | string | **Obrigatória.** Região de soberania nua (ex.: `eu-west-1`). Anunciada como `server_tags: ["region:<region>"]`. Sem ela, o nó com fronteira declarada não arranca. |
| `cluster_size` | number | Nº de nós (1 = dev; ≥3 = staging). |
| `client_port` | number | Porta de cliente do nó 0 (host). |
| `monitoring_port` | number | Porta de monitorização do nó 0 (host). |
| `store_dir` | string | Diretório de JetStream no container. |
| `labels` | map(string) | Etiquetas comuns. |

## Saídas

| Nome | Descrição |
|---|---|
| `client_url` | URL de cliente NATS (host) — consumido por `substrate/ES`. |
| `monitoring_url` | URL de monitorização HTTP. |
| `internal_client_urls` | URLs internos de todos os nós. |
| `cluster_size` / `node_names` | Metadados do cluster. |
| `server_tags` | Tags anunciadas por cada nó (`["region:<region>"]`). Têm de casar com a `placement` que o adaptador exige. |
| `region` | Região efectiva, já normalizada como em `soberania.go`. |
| `node_commands` | Comando efectivo por nó — inclui o `-c` do fragmento de tags. |
