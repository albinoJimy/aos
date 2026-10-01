# Cluster NATS JetStream de produção — Contabo + Hetzner (AOS-469)

O substrato para `AOS_EVENTSTORE_NATS` (AOS-100) em produção: três `nats-server` em dois hosts,
ligados por um túnel WireGuard, com a tag de região de que o nó precisa para criar o stream.
Tudo sai de um script ([`aos-nats.sh`](aos-nats.sh)) e de um ficheiro que descreve o cluster
inteiro ([`cluster.conf.example`](cluster.conf.example)).

> **Este ticket levanta o cluster. Não liga o nó `aos` a ele.** Ligar o `AOS_EVENTSTORE_NATS`
> tem pré-condições que ainda estão abertas, e mudar a variável não chega para as cumprir. Ver
> [«Antes de apontar o nó `aos` ao cluster»](#antes-de-apontar-o-nó-aos-ao-cluster).

## Topologia

```
   Contabo 37.60.241.150                          Hetzner 78.46.209.230
   ┌───────────────────────────────┐             ┌──────────────────────────┐
   │ aos (rede aos_default) ──┐    │             │                          │
   │                          ▼    │  WireGuard  │                          │
   │ aos-es-0  10.77.0.1:4222/6222 │◄═══════════►│ aos-es-2 10.77.0.2:4222  │
   │ aos-es-1  10.77.0.1:4223/6223 │  51820/udp  │          10.77.0.2:6222  │
   └───────────────────────────────┘             └──────────────────────────┘
        server_tags: ["region:eu-west"] nos três — stream R3, placement region:eu-west
```

### Porquê o túnel, e porque o NATS só escuta nele

O cliente do AOS (`packages/substrate/eventstore/natsjs`) fala NATS em TCP simples: o CONNECT
leva `"tls_required":false` e nenhuma credencial. **Quem chega à porta de cliente escreve no log
de produção.** Daí resultam três regras:

- **Todo o tráfego entre hosts passa pelo WireGuard**, tanto o Raft das rotas como o do cliente.
  Vai cifrado e cada par é autenticado pela sua chave. Nada atravessa a internet pública em claro.
- **Cada `nats-server` escuta apenas no IP WireGuard do seu host.** As portas 4222, 6222 e 8222
  não existem no IP público. Por isso não colidem com nada que o Contabo já publica (k8s, Coolify).
- **As regras de firewall vêm e vão com a interface** (`PostUp`/`PostDown` do `wg-quick`).
  Às portas NATS só chegam os pares do túnel, o próprio host e a sub-rede Docker declarada, que é
  a do nó `aos`. Para os outros hosts, através do túnel, só sai essa mesma sub-rede. Os
  contentores órfãos e os pods do cluster k8s do Contabo ficam de fora. As regras só tocam em
  tráfego com destino ao IP WG ou que passa pela interface `aos-es`. As portas do k8s
  (6443, 10250, 2379/2380, 8472/udp) **não são afectadas**.

**O que isto não fecha:** quem tem root num dos hosts consegue escrever no log. Fechar isso exige
autenticação no cliente `natsjs` (nkey ou TLS mútuo), e isso é código. Ver «O que fica por fazer».

A chave privada WireGuard **nunca sai do host onde nasceu**. Fica em
`/etc/wireguard/aos-es.key` (0600) e é carregada no `PostUp` com `wg set … private-key`. O
`cluster.conf` só leva IPs e chaves **públicas**, por isso pode ser copiado entre hosts à vontade.

## O que dois hosts compram, e o que não compram

O stream do AOS é **R3**: o nó só aceita 3 ou 5 réplicas (`ErrBadEventStoreReplicas`), todas na
região do board. Com dois hosts, um deles tem de correr dois nós. Medido contra estas mesmas
configurações, num cluster local com os IPs WG:

| Cenário | Resultado | Como foi medido |
|---|---|---|
| Morre o Hetzner (`aos-es-2`) | **Continua.** 80/80 escritas confirmadas sobreviveram. O cliente religou-se em 1 s e o consumidor foi recolocado num par vivo. | `TestAC4_*`, `TestReconexao_*` do adaptador, com `AOS_KILL_*` a parar o `aos-es-2` |
| Morre o Contabo (`aos-es-0` + `aos-es-1`) | **Pára.** O `aos-es-2` sozinho responde `JetStream system temporarily unavailable (10008)`. | `nats stream ls` contra o nó sobrevivente |

Duas consequências que têm de ser escolhidas conscientemente:

1. **Disponibilidade: não se ganha nada.** O nó `aos` também só corre no Contabo. Se o Contabo
   cai, o AOS cai com ou sem cluster.
2. **Durabilidade fora do Contabo: ganha-se, mas não com RPO zero.** O quórum de commit (2 de 3)
   pode fechar-se só com os dois nós do Contabo, e a réplica do Hetzner pode ficar atrás no
   instante da falha. Em regime normal fica milissegundos atrás. Isto **não foi medido** sob
   carga real. Mesmo assim é muito melhor do que o RPO de 24 h do `backup.sh`.

**Com um terceiro host** (basta um VPS pequeno), muda-se a linha `no aos-es-1` para esse host e
reaplica-se. Passa a haver um nó por host, a perda de **qualquer** host é tolerada, e cada commit
fica em pelo menos dois hosts. O `aos-nats.sh` imprime um `AVISO` enquanto a topologia tiver um
host de que o quórum dependa.

## Instalação

Requisitos em **cada** host: Linux com `apt`, `iptables`, `systemd`, Docker e o plugin
`docker compose` v2. O Contabo já os tem; no Hetzner é preciso confirmar. Comandos como root
(`sudo`).

**Firewall do fornecedor.** Se o Hetzner tiver uma *Cloud Firewall* (ou o Contabo um filtro
equivalente), abre lá **51820/udp** de entrada, **só** a partir do IP do outro host. As portas
NATS **não** se abrem em lado nenhum.

### 1. Chaves (em cada host)

```bash
scp deploy/nats/aos-nats.sh armando@78.46.209.230:      # e o mesmo para o Contabo
ssh armando@78.46.209.230 'sudo bash aos-nats.sh chave'
# imprime a chave PÚBLICA deste host
```

### 2. O `cluster.conf` (uma vez, na tua máquina)

Copia o `cluster.conf.example` e cola as duas chaves públicas. A sub-rede do nó `aos` obtém-se
no Contabo:

```bash
docker network inspect aos_default -f '{{(index .IPAM.Config 0).Subnet}}'
```

Esse valor vai no quinto campo da linha `host contabo`. O exemplo traz `172.18.0.0/16`, que é
uma **suposição**: não foi lido no servidor. Se a sub-rede estiver errada, o `aplicar` avisa e o
nó `aos` não chega ao NATS. Se for larga demais, o firewall deixa entrar contentores a mais.

O script recusa uma especificação mal formada antes de mexer em seja o que for: um número de
nós diferente de 3 ou 5, uma imagem sem digest, um IP fora da `REDE_WG`, portas repetidas no
mesmo host, uma chave WG inválida ou uma região com o prefixo `region:`.

### 3. Aplicar (em cada host, com o MESMO ficheiro)

```bash
sudo install -D -m 0644 cluster.conf /etc/aos-nats/cluster.conf
sudo bash aos-nats.sh aplicar /etc/aos-nats/cluster.conf contabo    # no Contabo
sudo bash aos-nats.sh aplicar /etc/aos-nats/cluster.conf hetzner    # no Hetzner
```

O `aplicar` confirma que a chave pública declarada para este host é mesmo a deste host. Isso
apanha o `aplicar … hetzner` corrido no Contabo. Recusa ainda se alguém que não um
`nats-server` ocupar uma das portas. Depois escreve `/etc/wireguard/aos-es.conf` e
`/etc/aos-nats/*.conf`, levanta `wg-quick@aos-es` (activo no arranque) e o compose
`aos-nats`. É idempotente: reaplicar recarrega pares e regras.

### 4. Verificar

```bash
sudo bash aos-nats.sh estado /etc/aos-nats/cluster.conf contabo
#   == túnel           handshake com cada par
#   == nós deste host  saudável, pares=2/2, meta-leader=…
#   == para o nó aos   AOS_EVENTSTORE_NATS=10.77.0.1:4222,10.77.0.1:4223,10.77.0.2:4222 …
bash aos-nats.sh provar /etc/aos-nats/cluster.conf hetzner
#   cria um stream R3 com placement region:eu-west, publica, confirma réplicas em dia, apaga
```

O `estado` sai com `≠0` se faltar o túnel, um handshake, a saúde de um nó ou o meta-leader.

### Desfazer

```bash
sudo docker compose -p aos-nats -f /etc/aos-nats/docker-compose.yml down   # volumes ficam
sudo systemctl disable --now wg-quick@aos-es                               # leva as regras
```

## Antes de apontar o nó `aos` ao cluster

Pôr no `.env` o que o `estado` imprime não basta. Antes disso, cada um destes pontos tem de ter
resposta:

1. **Backup do log replicado.** Com `AOS_EVENTSTORE_NATS` preenchida o log sai do volume, e o
   `deploy/server/backup.sh` **recusa** produzir artefacto (passo 0). É de propósito: um backup
   verde sem o log é pior do que nenhum. Ver `deploy/server/README.md`, «O backup só é um backup
   se o log estiver onde ele copia».
2. **A história que já existe.** O `events.wal` actual não passa sozinho para o stream. O nó
   passa a usar o NATS e o WAL fica obsoleto. O adaptador tem o caminho para fazer a cópia
   (`IngestStream`, AOS-101, preserva o envelope), mas **não confirmei** que exista um
   procedimento de operador para migrar produção.
3. **O WORM continua local.** O `worm.wal` não é replicado (AOS-325). O cluster protege o log dos
   runs, não a cadeia de auditoria.

## O que fica por fazer

- **Autenticação no cliente `natsjs`.** Hoje a segurança do log depende só da rede (túnel,
  bind, firewall). Com nkey ou TLS mútuo no cliente, podiam ligar-se `authorization` e
  `cluster.authorization` nos servidores.
- **Um terceiro host**, pelas razões da tabela acima.
- **Medir o atraso real** da réplica do Hetzner sob carga de produção.
