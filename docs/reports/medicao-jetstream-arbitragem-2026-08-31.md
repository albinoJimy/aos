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

---

# ADENDA — validação adversarial das afirmações acima (2026-08-31, fim do dia)

Este relatório foi submetido a quatro auditorias independentes: completude contra o
ticket, revisão adversarial do cliente, auditoria de veracidade das afirmações, e
conformidade com os gates. **Muita coisa acima não sobreviveu.** O que segue corrige-a;
o texto original fica, para que a correcção seja legível como correcção.

## A1. O achado que governa tudo, e que o relatório acima não dizia

**MEDIDO:** `natsjs` não implementa `eventstore.EventStore`, e **ninguém importa
`natsjs`**. `RunArbitration` não tem chamadores. Não existe adaptador.

```
$ grep -rn "eventstore/natsjs" --include=*.go packages/   → ZERO importadores
$ grep -rn "RunArbitragem"     --include=*.go packages/   → ZERO chamadores
```

**Consequência, e é severa:** mediu-se o **substrato**, não o **Event Store do AOS**. O
nó continua a abrir o WAL de sempre. A prova mais limpa é que os três sensores que o
handoff mandava observar — `TestSensor_ReferenciaNaoArbitraEntreEscritores`,
`TestLimite_EventStoreDeReferenciaNaoArbitraEntreProcessos`, e a condição de
`guardDePosseAplicavel` — continuam todos no estado «propriedade ausente», e continuam
**certos**. Nenhum disparou. **O AOS-100 não está cumprido**; ver §A7.

## A2. «cluster R3 real» — qualificação em falta

O cluster são **três contentores no mesmo host**, na mesma rede Docker, com o mesmo
kernel e o mesmo disco. Não houve partição de rede. A ressalva existia no §5 só onde
servia para justificar o que não se mediu (AC5), e faltava onde limitaria o que se
afirma. Onde acima se lê «cluster R3 real», leia-se **«três réplicas JetStream num só
host»**.

## A3. «zero perda» — RETIRADA

O §2 provava «zero perda» com `messages: 4`. Isso prova a **contagem final**, não a
preservação. Faltava o registo dos seqs confirmados antes do kill e uma releitura
pós-failover a reconciliá-los. Tentei essa medição e **não a concluí** (o script não
produziu saída).

**O que sobrevive, e só isto:** o stream continuou a aceitar escritas depois de o líder
morrer, com nova eleição, e a contagem final é coerente com os commits conhecidos.
Retira-se também «com escritas em curso»: observou-se **uma** escrita, e posterior à
eleição.

## A4. A dedup — a conclusão mantém-se, a prova foi refeita

O §4 usava `--dupe-window=1s` com rótulos qualitativos («t=0+»). Cada invocação do CLI
demora 1–2 s, pelo que a 2.ª publicação já caía **fora** da janela — a experiência não
distinguia «a janela expirou» de «a dedup nunca esteve activa neste stream».

Refeita com janela de 30s e **controlo positivo**, que exclui a hipótese rival:

```
t=+0s   1a: {"stream":"JANELA", "seq":1}
t=+2s   2a (DENTRO da janela):  {"stream":"JANELA", "seq":1,"duplicate": true}
t=+38s  3a (FORA da janela):    {"stream":"JANELA", "seq":2}
```

A mesma chave, no mesmo stream, deduplica a +2s e volta a passar a +38s. A única
variável é o tempo. **A conclusão do §4 mantém-se e está agora provada.** Fixada em
`TestIntegracao_DedupExpiraComAJanela`.

## A5. O CAS estava medido só SEQUENCIALMENTE — agora está sob contenção

Todas as medições do §1 e o `TestIntegracao_CASArbitraEntreDuasLigacoes` são
sequenciais. O modo de falha «o CAS existe mas não é serializável sob contenção» ficava
por excluir. Medido com 8 ligações independentes e barreira de largada:

```
8 escritores concorrentes sobre expected_seq=0: 1 vencedor (seq=1), 7 recusados com seq=0
```

## A6. «a recusa não deixa rasto» era inferido do `seq:0` — agora é observado

`TestIntegracao_RecusaNaoDeixaRasto` lê a posição disputada e confirma que contém o
vencedor, e que o stream tem exactamente uma mensagem.

## A7. Transporte push (AC2) — medido, com limite

Três eventos publicados **antes de existir subscritor** foram entregues por push ao
`deliver subject`, com `$JS.ACK`. Inclui replay (`deliver_policy: all`).

**Limite:** medido com `ack_policy: none` e flow-control **desligado**. Um consumidor
durável de produção quer acks e flow control, e é aí que o custo do cliente próprio pode
crescer. O cliente **não tem** consumidores push.

## A8. Estado real dos Critérios de Aceitação

Nenhum AC está satisfeito **para o Event Store do AOS**. O que existe:

| AC | Estado | Nota |
|---|---|---|
| AC1 append-only + quórum | PARCIAL | in-process pré-existente; contra JetStream medido por CLI, sem teste Go de falha de nó |
| AC2 push | MEDIDO no substrato, AUSENTE no cliente | ver §A7 |
| AC3 multi-worker paralelo | PARCIAL | contenção de escrita medida (§A5); **sem API de leitura para replay** |
| AC4 SPOF eliminado | PARCIAL | medição manual, não teste; ver §A3 |
| AC5 soberania | NÃO-SATISFEITO | `StreamConfig` **não tem campo `Placement`** — o cliente é hoje incapaz de exprimir a restrição regional |
| AC6 integridade verificável | PARCIAL | delete/purge recusados medidos por CLI; sem sentinela nem teste no cliente |

**Em falta e nomeado:** o adaptador `natsjs` → `eventstore.EventStore`; benchmark de
throughput; teste de falha de nó em Go; `Placement` para soberania.

## A9. Defeitos MEDIDOS no cliente, e corrigidos

A revisão adversarial mediu-os com um servidor NATS falso. Três matavam o processo do
nó (`send on closed channel` no leitor; `concurrent map writes` em `PublishExpectingSeq`;
panics por tamanhos negativos vindos do fio), um corrompia a ligação a cada invocação
(`PUB` de payload vazio sem CRLF — que tornava `FetchStreamState` inutilizável), dois
eram injecção (cabeçalhos e subject), e um corrompia a semântica em silêncio (uma rotura
depois do escoamento devolvia EOF genérico em vez de indeterminado). Todos fechados, com
nove regressões que correm **sem cluster**.

## A10. Processo — o que foi saltado

Sem PR e sem o template de `specs/01` §7; sem os dois revisores que um P0 exige; o
`CHANGELOG` não foi alimentado até esta adenda; `tecnica/10` §3/§6 não foi tocada — e
**está correcto não tocar**, porque o limite que ela declara continua verdadeiro (§A1).

Um `git add -A` meu varreu para um commit um ficheiro temporário de auditoria de outra
sessão; removido, e declarado no commit que o remove.

---

# ADENDA 2 — o adaptador, e o que ele muda (2026-08-31)

A §A1 dizia que o achado que governava tudo era não existir adaptador: media-se o
substrato, não o Event Store do AOS. **Esse achado deixou de valer.**

`packages/substrate/eventstore/jetstream` implementa `eventstore.EventStore` sobre o
cluster. O **mesmo** instrumento que reporta as quatro sondas AUSENTES contra o store de
referência reporta-as **PRESENTES** contra ele:

```
[visibilidade-entre-handles] presente — A escreveu seq=1; B lê 1 evento(s), o primeiro em seq=1
[cas-entre-handles]          presente — B afirmou o mesmo expected_seq=0 e foi recusado
[dedup-entre-handles]        presente — B vê duplicate no seq ORIGINAL
[corrida-um-so-vencedor]     presente — 4 escritores sobre expected_seq=0: 1 vencedor, 3 recusados
```

Handles independentes são ligações TCP distintas com caches próprias — o que dois
processos são um para o outro.

## O que o adaptador tem de decidir, e como decidiu

| Problema | Decisão | Porquê |
|---|---|---|
| Duas numerações | O `seq` do AOS vive no envelope; o do JetStream é só token de CAS e nunca é exposto | O JetStream numera globalmente por stream físico; o C2 exige gapless desde 1 **por stream**. Confundi-los daria um log com buracos. Fixado em teste com dois streams intercalados |
| Idempotência | Índice **derivado do log**; `Nats-Msg-Id` só como rede de segurança | A janela do servidor é temporal (§4/§A4); a do AOS não tem prazo |
| CAS recusado | Se o chamador afirmou `expected_seq`, a recusa é dele; se não afirmou, re-hidrata e repete | Devolver-lhe um conflito que não pediu seria transformar concorrência entre streams num erro do chamador |
| Indeterminado | Re-hidrata e procura o `EventID` gerado agora | Torna resolúvel o que de outro modo seria fatal para o contrato C2 |
| `stream_id` não representável | **Recusa** | Um ponto seria escapado para um subject vizinho, onde outro stream leria os nossos eventos. `lease:<run>` continua a passar — tem teste |

## O que continua por fazer, medido

**O nó não pode usar isto.** `Config.EventStore` é do tipo **concreto** `*eventstore.Store`,
não da interface. Medido o que falta exactamente: `Healthy()` e `Streams()` são os
**dois únicos** métodos fora da interface que o nó usa — ambos já implementados no
adaptador. Alargar o campo à interface é a decisão seguinte.

Por isso o `DEF-282` **continua ABERTO** e o sensor de `aos-orq` **continua certo**: o
gatilho de fecho está satisfeito num substrato, não no nó.

E continua por fazer, do próprio adaptador: **soberania (AC5)** — falta `placement` no
`StreamConfig`, e enquanto faltar este backend **não serve um board com fronteira
declarada**; consumidor **durável** com acks e flow control; leitura em lote (hoje é um
round-trip por evento); e o **benchmark** de throughput.

---

# ADENDA 3 — soberania regional (AC5), fechada e medida

A §A8 dava o AC5 como **NÃO-SATISFEITO**, com a razão exacta: `StreamConfig` não tinha
campo `Placement`, logo o cliente era *incapaz de exprimir* a restrição regional. Está
fechado.

## O mecanismo, e porque não bastava pedir

A fronteira é imposta pela `placement` do stream, restrita a servidores que anunciem
`server_tags: ["region:<regiao>"]`. Mas **pedir e assumir** seria o mesmo erro de método
que este relatório já retractou uma vez. Há um caminho em que o pedido nem chega a ser
feito: ligar-se a um stream **já existente**. Por isso a colocação é **lida de volta da
configuração armazenada** e verificada no arranque.

## Os três modos de falha, todos medidos

| Caso | Resultado |
|---|---|
| Região que nenhum par do cluster serve | O **servidor** recusa: `no suitable peers for placement, tags not matched ['region:...']` (`err_code=10005`), traduzido para `E_SOVEREIGNTY_VIOLATION` |
| Ligar-se a um stream **sem** colocação | `E_SOVEREIGNTY_VIOLATION: o stream "…" está SEM colocação no servidor, e a fronteira do board exige "region:eu-west" — as réplicas podem estar em qualquer par do cluster` |
| Fronteira declarada **sem região** | Aborta **antes de haver ligação** (região desconhecida ⇒ deny) |

O segundo é o que importa mais: sem ele, um nó com fronteira declarada ligar-se-ia a um
stream sem colocação e **julgar-se-ia soberano**. Tudo funcionaria — escritas, leituras —
e os dados estariam onde a política diz que não podem estar.

## O que o nó acrescenta, e só ele podia

`AOS_BOARD_REGIONS` (read-path soberano, AOS-094) com `AOS_EVENTSTORE_NATS` **sem**
região passa a **negar o arranque**: leituras que respeitam a região sobre dados que podem
estar em qualquer par é uma contradição servida em silêncio. E uma região que não é a de
nenhum board declarado também aborta.

## O que a medição NÃO cobre

Os três servidores do cluster de medição anunciam a **mesma** região, pelo que **não se
exercitou um cluster genuinamente multi-região**. Fica provado que a restrição é pedida,
armazenada e verificada, e que a sua ausência aborta; a distribuição das réplicas por
regiões distintas é lógica do JetStream, não nossa.

## Reprodução — o cluster tem de anunciar as tags

`server_tags` é config de ficheiro; não há flag de CLI. Cada nó corre com:

```
server_name: aos-medicao-es-N
port: 4222
http_port: 8222
server_tags: ["region:eu-west"]
jetstream { store_dir: "/data/jetstream" }
cluster { name: "aos-medicao-es", port: 6222, routes: [...] }
```

```bash
docker run -d --name aos-medicao-es-$i --network aos-medicao-net \
  -v aos-medicao-es-$i-data:/data/jetstream \
  -v /opt/aos-medicao/es-$i.conf:/etc/nats.conf:ro \
  nats:2.10-alpine -c /etc/nats.conf
```
