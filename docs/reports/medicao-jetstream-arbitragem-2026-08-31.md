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

---

# ADENDA 4 — benchmark de throughput (Testes Requeridos do AOS-100)

O ticket pede «*benchmark* básico de throughput contra o baseline single-writer». Aqui
está, com a metodologia primeiro porque sem ela os números não significam nada.

## Metodologia, e o que ela evita

**Corrido CO-LOCALIZADO com o cluster.** Um benchmark contra um cluster remoto através de
um túnel SSH mediria o túnel. O binário de teste é cross-compilado
(`GOOS=linux CGO_ENABLED=0 go test -c`) e corrido no servidor, contra os três nós na
mesma máquina. 8 escritores, `-benchtime=1000x`, `-count=3`.

**Reportam-se MEDIANAS e INTERVALOS, não amostras.** A primeira corrida deu
`referencia-wal/8-streams` a 35 ops/s; a segunda, 57. Reportar a primeira teria produzido
um «achado» — «o WAL fica mais lento com mais streams» — que **não reproduziu**. É a
diferença entre medir e ter sorte.

## Escrita

| Caso | ops/s (mediana) | intervalo | ms/op |
|---|---|---|---|
| `referencia-memoria` / 1 stream | 18 792 | — | 0,053 |
| `referencia-memoria` / 8 streams | 10 972 | — | 0,091 |
| `referencia-wal` / 1 stream | 54,2 | 46–56 | ~18 |
| `referencia-wal` / 8 streams | 48,6 | 44–51 | ~21 |
| `jetstream-r3` / 1 stream | 72,0 | 66–119 | ~14 |
| `jetstream-r3` / 8 streams | **278,8** | 263–522 | ~3,6 |

**1. O caminho durável de referência NÃO escala com o número de streams.** 54 → 49 ops/s,
dentro do ruído; em três corridas a direcção variou e a magnitude nunca melhorou. A causa
é lida no código e consistente com a medição: `wal.append` toma um **mutex global**
(`durable.go:156`) que cobre a escrita e o `fsync`. A ordem-por-stream (stripes) paraleliza
as estruturas em memória — o in-memory faz 18 792 ops/s, ~350× mais — mas o WAL é **um
ficheiro com um fsync por registo**, e é ele que fixa o tecto.

> **Nota de rigor sobre uma frase do `eventstore/doc.go`.** Ela diz que «appends a streams
> DIFERENTES progridem EM PARALELO, sem contenção global». É verdade para o modelo
> in-process a que se refere, e a medição confirma-o (o in-memory é 350× mais rápido). Mas
> **não descreve o caminho DURÁVEL**, que ganhou um ponto de serialização global quando o
> WAL chegou (AOS-170). Não é falsidade — é uma frase que envelheceu ao lado de uma
> funcionalidade posterior, e o benchmark é o que a data.

**2. O substrato replicado ESCALA: ~3,9× de 1 para 8 streams** (72 → 279). É o AC3 —
«múltiplos workers escrevem em paralelo, sem contenção de escritor único» — medido em vez
de afirmado.

**3. O replicado bate o WAL local, ~5,7× a 8 streams — e isso diz tanto do disco como dos
substratos.** O `fsync` desta VPS custa ~18 ms; a «rede» do cluster é uma bridge Docker na
mesma máquina. **Um deployment multi-host inverteria parte desta comparação**, e quem a
citar sem esta linha estará a citar mal.

## Replay — o resultado que muda a avaliação do adaptador

| Caso | eventos/s |
|---|---|
| `referencia-wal` | 3 856 011 – 5 366 429 |
| `jetstream-r3` | **113 – 120** |

Ler um stream de 200 eventos custa **~1,7 s** no replicado contra ~40 µs no local.

Isto estava DECLARADO no `doc.go` do adaptador («um pedido por evento… um stream longo
custa proporcionalmente»), mas declarado como custo proporcional e não como **ordem de
grandeza**. Quantificado, muda a avaliação: a re-hidratação de um run (`RebuildLedger`,
`RebuildDAG`) lê o stream inteiro, pelo que **um run de 200 eventos paga ~1,7 s por
arranque**. Não é aceitável em regime, e é a próxima coisa a fazer no adaptador.

**O que NÃO se sabe, e não se finge saber:** uma ida-e-volta simples à API mede
**0,46–1,29 ms** (`BenchmarkLatenciaDeBase`), pelo que 200 delas seriam ~200 ms, não
1 700 ms. O custo por evento (~8,5 ms) é **dominado por algo que não é o RTT** — o candidato
óbvio é o `next_by_subj` do lado do servidor, mas isso é hipótese, não medição.
Identificá-lo é o primeiro passo da optimização, não uma conclusão desta adenda.

---

# ADENDA 5 — as duas coisas que faltavam (2026-09-01)

## 1. Leitura em lotes — de ~1,7 s para ~25–51 ms por run

A adenda 4 mediu o replay a **113–120 eventos/s** e chamou-lhe «a próxima coisa a
corrigir». Corrigida: um lote é agora um consumidor efémero que EMPURRA a janela inteira,
em vez de um pedido `next_by_subj` por evento.

| Versão | eventos/s (200 eventos) | por leitura |
|---|---|---|
| Pedido por evento | 113–120 | ~1,7 s |
| Lote, consumidor R3 | 1 724–1 910 | ~105–116 ms |
| Lote, consumidor **R1 em memória** | 3 881–7 967 | ~25–51 ms |

**~32–66×.** A variância desta VPS é alta; são intervalos observados, não garantias.

**A completude é VERIFICÁVEL, e tinha de ser.** Com entrega push e `ack_policy: none`
não há nada que diga «acabou» — só mensagens que param de chegar, o que é indistinguível
de uma que se perdeu. Por isso o número de eventos do subject é pedido ao servidor
ANTES, e um lote incompleto é um ERRO. Servir um log truncado em silêncio seria o pior
desfecho possível: o replay reconstruiria estado errado sem ninguém dar por isso.

**Onde estava o custo, medido e não suposto.** A adenda 4 dizia que o custo por evento
era «dominado por algo que não é o RTT» e recusou-se a nomear a causa. Com dois tamanhos
de stream (200 e 2000), 10× mais eventos custaram só ~1,5–2× mais tempo: o custo era
**por LEITURA**, não por evento — a criação do consumidor. Daí a segunda correcção.

## 2. Zero perda sob falha de nó — a afirmação retractada, agora PROVADA

A §A3 retractou o «zero perda» porque ele assentava numa contagem final. A prova exige
reconciliar, e é o que agora existe:

```
fase A: 40 escritas CONFIRMADAS antes da falha
matar nó: stream=AC4_545f8eac803a lider=aos-medicao-es-1
fase B: 80 escritas confirmadas no total, 0 tentativas falharam durante o failover
reconciliação: 80/80 escritas confirmadas presentes e íntegras depois da morte de um nó
```

Cada `seq` confirmado é registado no momento do ACK e reprocurado no log relido por uma
ligação NOVA, com a carga verificada. E o inverso também: o log não pode ter eventos que
ninguém confirmou — um fantasma seria a garantia «ERRO ⇒ NADA FICOU DURÁVEL» a partir-se,
e é tão grave como uma perda.

**A propriedade medida é precisa, e mais fraca do que «nada se perde»:** *toda a escrita
CONFIRMADA sobrevive*. Escritas que falham durante um failover não são perdas — são
recusas. Confundir as duas foi o que produziu a afirmação retractada.

**Nota de método sobre o comando de falha:** o script mata o LÍDER do stream e nunca o nó
a que o cliente está ligado (força um step-down se a liderança lá estiver). Não é
conveniência: o cliente não tem reconexão automática — limite declarado —, e matar a
ligação mediria o CLIENTE, não o cluster.

## 3. O teste do AC4 apanhou um defeito no trabalho da alínea 1

Na primeira execução, as escritas continuaram (0 falhas) e a **leitura** falhou:
`CONSUMER.CREATE` expirou ao fim de 20 s com um nó em baixo.

Causa: o consumidor de leitura herdava as **3 réplicas** do stream, pelo que criá-lo era
uma operação replicada — e a leitura ficava **menos disponível do que a escrita**,
exactamente no cenário que o AC4 mede. O `next_by_subj` que eu tinha substituído não
tinha esse problema: lia sem criar nada.

Ou seja, a optimização trocou disponibilidade sob falha por velocidade, e só se soube
porque as duas tarefas foram feitas juntas. Os consumidores de leitura passam a ser
**R1 em memória** — não há nada a replicar num consumidor que morre com a leitura — e a
correcção deu, de lado, mais 2–4× de velocidade.

---

# ADENDA 6 — o cliente não se curava, e isso furava o AC1 (2026-09-01)

## O buraco, e como estava escondido

O AC1 diz que «a perda de uma réplica não perde dados **nem interrompe escritas**». A
adenda 5 provou-o — mas o script de falha usado lá **evita deliberadamente** matar o nó a
que o cliente está ligado, e força um *step-down* se a liderança estiver nele. A razão
estava escrita («o cliente não tem reconexão automática — limite declarado — e matar a
ligação mediria o cliente, não o cluster»), o que a tornava honesta e **incompleta**: a
propriedade só estava provada para a metade sortuda dos casos.

## Medido antes de corrigir

```
matar o nó da ligação: morto: o no da ligacao (es-0)
escrita imediata após a falha: natsjs: ligação fechada
20s depois ... as escritas continuam a falhar (natsjs: ligação fechada)
```

E continuariam indefinidamente. **A morte de UM nó do cluster virava um incidente do NÓ
INTEIRO** — o oposto do que um Event Store replicado existe para dar.

## Depois

```
matar o nó da ligação: morto: o no da ligacao (es-0)
escritas RETOMARAM 1s depois da morte do nó da ligação, sem reiniciar o processo
```

O cliente aceita **vários endereços** (`AOS_EVENTSTORE_NATS=n1:4222,n2:4222,n3:4222`) e
reconecta com recuo exponencial até 5 s, sem desistir — desistir transformaria uma falha
transitória do cluster numa falha permanente do nó.

## Duas decisões que valem mais do que o mecanismo

**Uma operação tentada sem socket falha com `ErrDesligado`, não com `ErrIndeterminate`.**
São promessas diferentes: a primeira nem chegou a sair (nada ficou durável, seguro repetir
sem pensar); a segunda saiu e não sabemos (exige o CAS). Colapsá-las teria feito o
chamador tratar como incerto algo que é certo.

**As subscrições NÃO são retomadas em silêncio.** Reconectar o socket não ressuscita um
consumidor efémero: ele morreu com a ligação. Se o cliente reconectasse e ficasse calado,
uma subscrição deixaria de entregar sem ninguém saber — e uma subscrição morta em silêncio
é pior do que uma que falha, porque quem depende dela nunca desconfia. O canal é fechado,
o Store é informado, e recria o consumidor.

**O que fica por fechar, e é preciso dizê-lo:** o consumidor novo é `deliver_policy: new`,
pelo que os eventos escritos **entre a quebra e o retomar não são entregues**. A
subscrição *retoma*, não *recupera*. Fechar isso exige um consumidor DURÁVEL com acks —
o residual já nomeado do AC2. Retomar sem recuperar o intervalo é melhor do que morrer em
silêncio e pior do que não perder nada: são três coisas diferentes, e esta é a do meio.

---

# ADENDA 7 — os três residuais, fechados (2026-09-01)

## 1. Soberania — a forma FORTE do AC5

A adenda 3 declarava o limite: *«os três servidores anunciam a MESMA região, pelo que não
se exercitou um cluster genuinamente multi-região. Prova-se que a restrição é pedida,
armazenada e verificada; a distribuição por regiões distintas é lógica do JetStream, não
nossa.»*

Acrescentou-se um **quarto nó noutra região** (`region:us-east`), sem reetiquetar os três
existentes — o que já estava medido continua válido, e a medição nova acrescenta. Cria-se
um stream confinado a `eu-west` e pergunta-se ao servidor **onde ele ficou**:

```
fronteira "eu-west" CUMPRIDA: o stream está em
[aos-medicao-es-2 aos-medicao-es-0 aos-medicao-es-1] (líder="aos-medicao-es-2"),
e nenhum dos 1 pares proibidos aparece
```

Deixa de ser «a restrição está armazenada» e passa a ser **«as réplicas não cruzaram a
fronteira»** — que é a promessa do ADR-011.

**O que continua fora do alcance do nó, e é honesto:** o mapeamento par→região não é
legível sem a conta de sistema do cluster. Quem sabe que servidores estão em que região é
quem configura o cluster; o `ColocacaoEfectiva()` dá-lhe os nomes para confrontar.

## 2. Leitura paralela — a ressalva escrita ao lado do AC3

Estava escrito: *«a leitura paralela não é serializada por construção (`Read` não toma o
mutex por-stream) mas NÃO foi medida em separado»*. «Não é serializada por construção» é
um argumento sobre o código; isto é a medição — 4 streams de 100 eventos:

| | eventos/s |
|---|---|
| Sequencial | 1 191 – 2 160 |
| Paralelo (4 goroutines) | 4 334 – 4 993 |

**Escala ~2,3–4×** com 4 streams. Não é linear, e o limitador provável é a **ligação
única** — o que é uma hipótese, não uma medição, e fica dito como tal.

## 3. A subscrição passa a RECUPERAR, não só a retomar

A adenda 6 fechou o cliente que não se curava, e deixou um buraco nomeado: o consumidor
era efémero e recriado com `deliver_policy: new`, pelo que **os eventos escritos entre a
quebra e o retomar não eram entregues**. Um buraco silencioso no fluxo — a pior forma de
perder eventos, porque ninguém dá por ela.

O consumidor passa a ser **DURÁVEL com acks explícitos**. O servidor sabe até onde a
entrega foi confirmada e recomeça aí:

```
matar o nó da ligação: morto: o no da ligacao (es-0)
todos os eventos escritos durante a quebra foram ENTREGUES depois dela
  — a subscrição recuperou, não só retomou
```

**Três decisões que valem mais do que o mecanismo:**

- **O ACK vai DEPOIS do handler.** Confirma o que foi *processado*, não o que *chegou*.
  Confirmar antes tornaria o durável um efémero com passos extra.
- **O ponto de partida é fixado UMA vez** (`by_start_sequence` no seq do momento da
  subscrição) e reafirmado tal-qual na reconexão. Recalculá-lo reabriria exactamente o
  buraco que o durável fecha.
- **Quem cria um durável é dono de o apagar.** O `Unsubscribe` apaga-o: um durável órfão
  fica no servidor para sempre a acumular estado de entrega.

**Limites que ficam, e são reais:** o consumidor é **R1** — se o nó que o aloja morrer
definitivamente, ele perde-se e o intervalo com ele. E não há **flow control** nem
heartbeats, pelo que um subscritor muito lento pode ser ultrapassado. Fechar os dois exige
um consumidor replicado (que a medição de 2026-09-01 mostrou ser caro de criar sob
degradação) e o tratamento das mensagens de controlo do protocolo.

---

# ADENDA 8 — a última falha silenciosa (2026-09-01)

A adenda 7 fechou três residuais e deixou dois nomeados: sem flow control, e o consumidor
R1 que se perde com o nó que o aloja. O segundo escondia uma coisa pior do que a perda.

## O cenário, e porque era o pior de todos

O consumidor da subscrição morre **do lado do servidor**. Do lado do cliente **nada
acontece**: a ligação está viva, o canal está aberto, o `SUB` continua registado.
Simplesmente não chega nada.

Sem batimento, isso é **indistinguível de um stream sossegado**. A subscrição fica morta
para sempre e ninguém dá por isso — nem uma excepção, nem um log, nem um erro devolvido.
É a única classe de falha contra a qual este pacote foi desenhado desde a primeira linha,
e era a última que ainda estava por cobrir.

## Medido

O teste apaga o consumidor **pelas costas do subscritor** e escreve a seguir:

```
consumidor(es) [aos-ef85dd521634b9616b6eb762] apagado(s) do lado do servidor
  — o cliente não foi avisado de nada
silêncio DETECTADO e entrega re-estabelecida: o evento posterior chegou
  sem ninguém reiniciar nada
```

`idle_heartbeat` de 5 s; ao fim de 15 s sem **nada** — nem evento nem batimento — o
consumidor é dado por morto e a entrega re-estabelecida. **Silêncio deixa de ser
indistinguível de paz.**

## E o flow control, que resolve o problema simétrico

Um pedido de fluxo é uma mensagem de estado 100 **com** subject de resposta; um batimento
é a mesma coisa **sem** ele. Distingui-los importa: não responder a um pedido de fluxo
**pára a entrega**, também em silêncio. Com ele, um subscritor lento é **travado**, não
atropelado — antes, as mensagens acumulavam-se e o cliente descartava-as.

## O que se ACEITA, e fica dito

O consumidor recriado parte do seq **fixado na subscrição**, pelo que os eventos desde
então são **reentregues**. É *at-least-once*: nada se perde, algumas coisas repetem-se.
Para um Event Store cuja idempotência é por `(run_id, step_id)` essa é a troca certa —
a alternativa era perder, e é pior.
