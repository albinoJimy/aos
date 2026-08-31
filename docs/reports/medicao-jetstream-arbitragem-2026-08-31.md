# Medição — o JetStream arbitra entre escritores? (AOS-100, AC1)

**Data:** 2026-08-31 · **Ticket:** AOS-100 · **Eixo:** DEF-282 · **Estado:** medido, com limites nomeados

O handoff do AOS-100 impõe uma regra antes de qualquer desenho: *«NÃO assumas que o
backend novo TEM a propriedade porque a documentação dele o diz.»* Este relatório é essa
medição. Nada aqui é citado de documentação — tudo foi observado contra um cluster real.

## 0. O que foi medido, e onde

Cluster **NATS JetStream R3** levantado no servidor do dono (`37.60.241.150`), com a
imagem **pinada nos `tfvars`** (`nats:2.10-alpine`) e as flags do módulo
`infra/modules/eventstore` (`-js -sd /data/jetstream`, `--cluster`, `--routes`
full-mesh entre 3 nós). Rede Docker dedicada, **sem portas expostas no host**.

> **NÃO se correu `tofu apply` da stack.** O servidor tem um deployment AOS vivo (nó
> `healthy` há 42h, Vault, Keycloak, litellm). Aplicar a stack mexeria no que está de pé.
> Reproduziu-se **só** o módulo `eventstore`, com nomes próprios (`aos-medicao-es-*`).

Cliente da medição: **`natsio/nats-box`** (CLI), deliberadamente — medir a propriedade do
**substrato** não podia depender de qual cliente Go viéssemos a escrever — essa decisão ainda
estava em aberto (ver §6) e a medição não a podia antecipar. O cliente próprio foi medido
depois, contra o mesmo cluster (§7).

## 1. A propriedade que decide o ticket — **PRESENTE**

O `expected_seq` é atómico entre escritores, imposto pelo servidor. Respostas literais:

| Escritor | Cabeçalho | Resposta do JetStream |
|---|---|---|
| A | `Nats-Expected-Last-Subject-Sequence: 0` | `{"stream":"MEAS", "seq":1}` |
| B | `Nats-Expected-Last-Subject-Sequence: 0` | `{"error":{"code":400,"err_code":10071,"description":"wrong last sequence: 1"},"stream":"MEAS","seq":0}` |
| C | `Nats-Expected-Last-Subject-Sequence: 1` | `{"stream":"MEAS", "seq":2}` |

**É a propriedade em falta no DEF-282**, e o mapeamento para o contrato C2 é directo: o
`seq:0` na recusa diz que **nada ficou durável** — o que a interface
[`eventstore.EventStore`](../../packages/substrate/eventstore/store.go) exige a qualquer
implementação («ERRO ⇒ NADA FICOU DURÁVEL»), e de que depende o revert em memória do
`GraphBuilder`.

## 2. Perda de nó — **CONTINUIDADE E ZERO PERDA**

Matou-se o **líder** do stream (`aos-medicao-es-1`) com escritas em curso:

- novo líder eleito: `aos-medicao-es-0`;
- a escrita seguinte, com `Nats-Expected-Last-Subject-Sequence: 2`, **passou** (`seq:4`);
- `messages: 4` — nada se perdeu.

## 3. Append-only — **IMPOSTO PELO SERVIDOR**

Com `--deny-delete --deny-purge`, não é convenção do cliente:

```
apagar msg seq=1 : nats: error: message delete not permitted (10057)
purgar stream    : nats: error: could not purge Stream: stream purge not permitted (10110)
mensagens        : 4
```

## 4. O achado que muda o desenho — a dedup **é uma janela, não um índice**

Esta é a razão de a regra de método existir. A dedup do JetStream *parece* satisfazer a
idempotência do AOS — e não satisfaz.

Com `--dupe-window=2m`, dois escritores com a mesma `Nats-Msg-Id` dão exactamente o
contrato C2: `{"stream":"MEAS", "seq":3}` seguido de
`{"stream":"MEAS", "seq":3,"duplicate": true}` — **o seq ORIGINAL**, que é o
`StatusDuplicate` do AOS.

Mas com `--dupe-window=1s`, a **mesma** `Nats-Msg-Id` ficou committed **três vezes**:

```
t=0   : {"stream":"JAN", "seq":1}
t=0+  : {"stream":"JAN", "seq":2}
t=4s  : {"stream":"JAN", "seq":3}
```

**Consequência para o AOS.** A idempotência por `(run_id, step_id)` é o Princípio 3 do
produto (execução durável) e não tem prazo: um `resume-from-step` depois de uma paragem
de horas repete o mesmo passo, e sob uma janela temporal esse retry **duplicaria o
efeito**. O `doc.go` do Event Store promete o oposto — *«Sobrevive a failover porque o
índice de dedup é mantido em cada réplica (reconstrutível a partir do log committed)»*.

**A dedup do AOS não pode ser delegada na janela do backend.** Tem de continuar
derivada do log, como já é hoje — e é seguro fazê-lo justamente porque §1 dá o CAS: o
índice reconstruído a partir do log é correcto sob concorrência quando a escrita que o
avança é serializada por `expected_seq`. **O CAS é a primitiva; a dedup é derivada.** A
janela do JetStream pode ficar activa como rede de segurança barata para retries
imediatos, nunca como a garantia.

## 5. O que NÃO foi verificado — e é honesto dizê-lo

| # | Por verificar | Porquê |
|---|---|---|
| 1 | **Transporte push (AC2)** | A criação do consumidor push e o `sub` bloquearam e a medição expirou. Não se conclui nada — nem a favor nem contra |
| 2 | **Soberania regional (AC5)** | Não medida: exige *placement tags*/cluster por região, que este cluster de um só host não tem |
| 3 | **Benchmark de throughput** | O AOS-100 pede-o contra o baseline single-writer; não corrido |
| 4 | **Dedup para lá da janela, com o índice derivado** | §4 mede o **limite do backend**; a correcção da solução (índice derivado do log) só se mede contra a implementação, que ainda não existe |

## 6. A decisão do ADR-017 — TOMADA

O conflito era real: o binário do nó é zero-dep (só stdlib + cedar-go, verificado no
`go.mod` de `packages/cmd/aos`), e o Event Store vive dentro do nó.

**Decisão do dono, 2026-08-31: cliente próprio em stdlib.** É o padrão que o próprio
ADR-017 já tinha usado ao escolher `crypto/ed25519` sobre cosign/sigstore — «decisão
declarada, com o custo em §Consequências». O ADR-017 fica **intacto**: sem emenda à
Carta, sem processo extra, sem dependência nova no artefacto do nó.

O custo fica declarado no `doc.go` do pacote: passamos a ser donos de um cliente de
protocolo em caminho crítico. Mitiga-se por âmbito — implementa-se o subconjunto de que
o Event Store precisa, e a medição mostrou que esse subconjunto é estreito, porque as
garantias do AOS-100 são todas do SERVIDOR (CAS por cabeçalho, append-only por
`deny_delete`/`deny_purge`, quórum e failover por Raft). A parte difícil não é nossa.

**Risco nomeado:** o transporte push (AC2) é a parte não medida e é a que traz
flow-control, heartbeats e acks. É aí que este custo pode crescer, e é o próximo sítio
a medir antes de construir.

## 7. O cliente, medido contra o cluster

`packages/substrate/eventstore/natsjs` — stdlib apenas (`net`, `bufio`, `encoding/json`,
`crypto/rand`). Handshake INFO/CONNECT, PING/PONG, PUB/HPUB, SUB/UNSUB, MSG/HMSG,
request-reply, e a API de streams do JetStream.

Medido a partir do **código do AOS**, não do CLI, contra o mesmo cluster R3
(`TestIntegracao_CASArbitraEntreDuasLigacoes`, duas ligações independentes — que é o que
dois processos são um para o outro):

```
A=1; B recusado com seq=0 (natsjs: expected_seq não corresponde ao último seq
do subject: wrong last sequence: 1); B após reler=2
```

As três linhas que interessam: **A ganha; B, afirmando o mesmo `expected_seq`, é
recusado e fica com `seq=0` — nada durável; B relê e passa.** É a arbitragem entre
escritores, a garantia «ERRO ⇒ NADA FICOU DURÁVEL» e a recuperabilidade do conflito, nas
mesmas três chamadas.

`TestIntegracao_DedupDoServidorEUmaJanela` fixa em teste o limite do §4, para que
ninguém volte a assumir que a `Nats-Msg-Id` resolve a idempotência do AOS.

Ambos são **saltados** sem `AOS_NATS_URL` — a CI sem cluster fica verde sem fingir que
mediu. Um mock do JetStream mediria o mock.

## 8. Reprodução

```bash
P=aos-medicao
docker network create ${P}-net
ROUTES="nats://${P}-es-0:6222,nats://${P}-es-1:6222,nats://${P}-es-2:6222"
for i in 0 1 2; do
  docker volume create ${P}-es-$i-data
  docker run -d --name ${P}-es-$i --network ${P}-net -v ${P}-es-$i-data:/data/jetstream \
    nats:2.10-alpine -js -sd /data/jetstream -p 4222 -m 8222 -n ${P}-es-$i \
    --cluster_name ${P}-es --cluster nats://0.0.0.0:6222 --routes "$ROUTES"
done
```

Para correr os testes Go a partir de fora do servidor, um túnel SSH para o nó 0 (o cluster
não expõe portas no host, e não deve passar a expor):

```bash
ssh -N -L 14222:$(docker inspect -f "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}" aos-medicao-es-0):4222 root@SERVIDOR
AOS_NATS_URL=127.0.0.1:14222 go test ./natsjs/ -v
```

Teardown completo (o `-v` do `rm` NÃO apaga volumes nomeados — daí a linha própria):

```bash
docker rm -f aos-medicao-es-0 aos-medicao-es-1 aos-medicao-es-2
docker volume rm aos-medicao-es-0-data aos-medicao-es-1-data aos-medicao-es-2-data
docker network rm aos-medicao-net
```

## 9. O instrumento que mede isto em Go

`packages/substrate/eventstore/conformance` mede as mesmas propriedades contra qualquer
`eventstore.EventStore`, com quatro sondas (visibilidade, CAS, dedup, corrida). Hoje
reporta as quatro **ausentes** contra o store de referência — é o sensor do DEF-282 — e
`TestNaoVacuidade_UmSubstratoQueArbitraEDetectado` prova que sabe reconhecer a presença.
É esse teste que apanhou um defeito no oráculo da sonda do CAS antes de ela ser usada.

O instrumento mediu ainda, na primeira execução, um custo que não estava registado:
**dois escritores commitam ambos `seq=1` e o WAL deixa de abrir** (`E_RESTORE_ORDER`) —
o nó não arranca. É o mesmo desfecho que o AOS-284 mediu para a hash-chain do WORM.
Registado em `TestDefeito_DoisEscritoresTornamOWALInabrivel`.
