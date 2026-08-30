# Análise — mudar a v1 de single-host para distribuído

> **O que é:** análise de decisão pedida pelo dono a 2026-08-30, na sequência do AOS-281.
> Responde a «o que custa, e o que quebra, mudar a forma da v1 declarada na Carta §7».
>
> **Não decide nada.** A decisão é do dono (Carta §6.1) e, se for invocado o §6.4, exige
> arbitragem escrita de **dois** papéis (§6.5). Este documento existe para que essa decisão
> seja tomada sobre factos verificáveis e não sobre impressão.
>
> **HEAD analisado:** `fe4f3ad` (branch `feat/AOS-281-escritor-unico-sob-lease`).

---

## 0. A pergunta certa não é a que parece

«Mudar a v1» soa a re-litigar uma decisão congelada. **Não é** — e começar por aqui poupa
uma discussão inteira.

A Carta §7, emenda 1.2 (2026-07-22), não diz «o AOS é single-host». Diz:

> a v1 single-host / sem-HA / com-SPOF é declarada **non-goal DATADO** — o substrato
> distribuído/sem-SPOF é **milestone posterior (EPIC-10)**, não regressão.

O distribuído nunca foi rejeitado. Foi **agendado**, com epic próprio e tickets P0
(`AOS-098`…`AOS-108`). Logo a pergunta real é:

> **Traz-se o EPIC-10, ou parte dele, para dentro do *Definition-of-Done* da v1 (§5)?**

Isso é uma emenda ao **§5**, não a reabertura de uma decisão FIXA do §4. E já há
precedente exacto: a **emenda 1.1** fez precisamente isto com o D4 — moveu a autoridade de
identidade para **pré-requisito da v1**, com a justificação escrita de que sem ela não
existia «uma configuração onde o nó corra trabalho real E seguro».

**Consequência prática:** se a decisão for enquadrada como emenda ao DoD da v1, **não
consome o contador do §6.6** (o tripwire de «≥2 decisões FIXAS reabertas em 30 dias»). Se
for enquadrada como reabertura da forma do produto, consome. A escolha do enquadramento é
do dono, mas deve ser **deliberada e escrita** — não implícita.

---

## 1. A descoberta que muda a conversa

Esperava encontrar um sistema single-host que precisasse de ser convertido. **Não é o que
está lá.** Grande parte do nó já está escrita **como se** fosse multi-réplica:

| Peça | Estado | Evidência |
|---|---|---|
| Posse por lease com fencing token monotónico | **Construída e testada** | `durable.LeaseManager` (AOS-018); stream `lease:` serializado por `expected_seq` |
| Enforcement de escrita fenced | **Construído**, agora **composto** | `durable.FencedAppender`; composto em `runlifecycle` (AOS-281) |
| Workers sem estado durável no processo | **É a regra** | estado no Event Store; `RebuildLedger` no arranque de cada run |
| Retoma *resume-from-step* sem efeito duplicado | **Construída** | step-ledger com dedup por `(RunID, StepID)` (AOS-180) |
| Estado particionado por run | **É a chave de tudo** | `stream_id == run_id`; dedup por-stream |
| Varredor de órfãos multi-réplica-seguro | **Já escrito assim** | `crash_resume.go`: «SALTA sem roubo se outra réplica detiver o lease vivo» |
| Admission control global do provider (TPM/RPM) | **Construído sobre CAS do log** | `scheduler.Admission` (AOS-027) |
| Re-hidratação do grafo a partir do log | **Entregue** | `orchestrator.NewGraphBuilderFromLog` (AOS-281) |

Isto não é coincidência: o `AOS-099` (workers stateless, P0) exige exactamente estas
propriedades, e elas foram construídas ao longo do caminho.

**O que falta não é a maquinaria. É o chão em que ela assenta.**

---

## 2. O bloqueador central — um só, e é medido

Toda a disciplina de posse depende de **uma** propriedade do Event Store: que a
concorrência optimista (`expected_seq`) do stream `lease:<run_id>` seja atómica **entre
escritores**.

**O Event Store de referência não a tem entre processos.** As réplicas de AOS-100 são
cópias **in-process** do log e o índice de dedup vive em memória; um `Open` faz replay do
WAL no arranque e, a partir daí, cada processo tem a **sua** cabeça.

Medido deterministicamente a 2026-08-30 — dois `eventstore.Open` sobre o mesmo ficheiro,
dois `Claim` do mesmo run:

```
claim1: token=1 err=<nil>
claim2: token=1 err=<nil>
```

**Ambos ganham.** Registado em `DEF-282` (eixo `AOS-100`) com sensor
(`TestLimite_EventStoreDeReferenciaNaoArbitraEntreProcessos`) que falha no dia em que o
substrato ganhar a propriedade.

O `doc.go` do Event Store já o declarava, aliás, sem rodeios: *«Este modelo torna as
invariantes determinísticas e testáveis; não é um Raft completo. Em produção o backend é
NATS JetStream (R3/R5, replicação Raft).»*

**Consequência para a decisão:** o `AOS-100` não é «mais um ticket P0 do EPIC-10». É **o**
ticket. Enquanto ele não estiver feito com um backend real, nenhuma das peças da §1 vale
seja o que for em multi-processo — e correr duas réplicas seria *pior* do que uma, porque
a maquinaria de posse **daria a impressão** de estar a arbitrar.

---

## 3. Os três buracos que o AOS-100 **não** tapa

Se o Event Store fosse trocado amanhã por JetStream, restariam três problemas reais.
Nenhum deles é resolvido pelo substrato. **Não tinham ticket quando este relatório foi escrito; passaram a ter a 2026-08-30** — `AOS-282`, `AOS-283` e `AOS-284`, criados a pedido do dono na sequência desta análise.

### 3.1 Orçamento por árvore — N réplicas = N× o tecto → **AOS-282**

`budget.Budget` mantém os contadores **em memória**, por processo
(`nodes map[string]*node` sob mutex). Existe `budget.Rebuild`, mas **devolve
`map[string]NodeState` e nenhum método do `Budget` o aceita** — não há via de
re-hidratação, e zero chamadores de produção.

Duas consequências, e a segunda é a grave:

- **Restart ⇒ fail-open.** Os contadores nascem a zero enquanto o log durável diz
  `reserved`. Já estava documentado no `desafio-A1`, cuja recomendação é explícita:
  > «**não** ligar o hook em produção multi-réplica sem essa declaração escrita, porque o
  > comportamento após restart é fail-open, não fail-closed.»
- **N réplicas ⇒ N tectos independentes.** Cada processo aplica o tecto da árvore ao *seu*
  tráfego. Com 3 réplicas, o tecto efectivo de tokens/$ por árvore é **o triplo** do
  declarado. Isto é a garantia central do ADR-008 a falhar em silêncio, e falha na
  direcção cara.

**Distinção importante:** o admission control do *provider* (`scheduler.Admission`,
AOS-027) **não** tem este problema — reserva sobre o log com CAS. É o orçamento **por
árvore** que é per-processo. São admissões diferentes; confundi-las levaria a concluir
que o problema já está resolvido.

**Ticket: `AOS-282`** (criado 2026-08-30). O `AOS-027` cobre o token-bucket do provider, não a árvore — não o confundir com este.

### 3.2 Laços de serviço sem eleição de líder → **AOS-283**

O nó corre **oito** laços periódicos: aprovações expiradas, prazos, órfãos, retenção,
avaliador de SLO, renovação do token do Vault, e mais. **Não há eleição de líder em lado
nenhum** do repositório (procurado: zero ocorrências).

Com N réplicas, os oito laços correm N vezes sobre o mesmo Event Store. Alguns são
idempotentes; pelo menos um **declara no código que não é**:

> `expireInFlight` serializa as passagens do `ExpirationJob` […] o check-then-Add da
> idempotency key **não é atómico ao nível do registo**, pelo que duas passagens
> concorrentes poderiam selar **DOIS** eventos `retention.expired` para o mesmo facto.

Esse guard é um `atomic.Bool` **no processo**. Com N processos há N guards e **nenhuma
exclusão entre eles** — que é exactamente a falha que o comentário diz estar a evitar.

O varredor de órfãos é a excepção honrosa: passa por `submit`, que reclama lease, e salta
sem roubo. Foi escrito a pensar nisto.

### 3.3 Cadeia de auditoria com escritor único em processo → **AOS-284**

`audit/filestore.go` serializa as escritas com um mutex **em processo**
(`wmu sync.Mutex // serializa os writes ao ficheiro único`). A hash-chain é sequencial por
construção: cada registo sela o `PrevHash` do anterior. Dois processos a escrever a mesma
partição computam `PrevHash` a partir de vistas diferentes.

A `GenesisHash` fala de «o PRIMEIRO registo de uma **partição**», o que sugere que o
desenho admite particionamento — mas **não verifiquei** se existe disciplina que garanta
que duas réplicas nunca partilham partição. **É a lacuna deste relatório que mais merece
uma segunda leitura** antes de qualquer decisão.

---

## 4. O que isto custa

Ordenado por dependência, não por esforço:

| # | Trabalho | Ticket | Estado | Notas |
|---|---|---|---|---|
| 1 | **Event Store real replicado** (JetStream R3/R5) | `AOS-100` | P0, por fazer | **Bloqueia tudo.** Sem ele o resto não é testável em multi-processo |
| 2 | Backup + PITR | `AOS-101` | P0, por fazer | Depende de (1) |
| 3 | DR por replay com RPO/RTO | `AOS-102` | P0, por fazer | Depende de (1)+(2) |
| 4 | **Orçamento por árvore durável/partilhado** | `AOS-282` | P0, criado 2026-08-30 | §3.1. Correctness, não operação |
| 5 | **Eleição de líder para laços de serviço** | `AOS-283` | P0, criado 2026-08-30 | §3.2 |
| 6 | **Disciplina de partição da hash-chain** | `AOS-284` | P0, criado 2026-08-30 | §3.3 — o AC1 do ticket é confirmar se o problema existe |
| 7 | IaC do plano de controlo/dados | `AOS-098` | P0, por fazer | |
| 8 | Pool de microVMs em produção | `AOS-103` | P0, por fazer | |
| 9 | Escala horizontal + degradação | `AOS-107` | P1, por fazer | Depende de (1) e de `AOS-099` |

**Três dos nove não tinham ticket** quando esta análise foi escrita. Isso foi, por si, um resultado dela: o EPIC-10
foi escrito assumindo que «workers stateless» (AOS-099) cobria o estado por-processo, e
o orçamento por árvore, os laços de serviço e a cadeia de auditoria escaparam.

---

## 5. Recomendação

**Não mudar a forma do produto. Mudar — se for essa a vontade — o DoD da v1, e só depois
de nomear o que falta.**

Três opções, com o que cada uma implica:

### Opção A — manter a v1 como está; distribuído é v1.1 *(recomendada)*

A v1 declara-se com o que o §5 já exige, e o EPIC-10 sai como release seguinte.

- **A favor:** a v1 é honesta hoje. O `DEF-282` está registado com sensor; o
  `tecnica/10` §3-bis declara que correr duas réplicas não é suportado; nada é prometido e
  não entregue — que é precisamente o achado «forma sobre-reivindicada» que a emenda 1.1
  existiu para resolver.
- **Contra:** um nó com SPOF não serve um deployment que exija HA. Se houver um utilizador
  real com esse requisito, a v1 não lhe serve — e adiar não o resolve.

### Opção B — mover um subconjunto NOMEADO para pré-requisito da v1

Como a emenda 1.1 fez com o D4. O subconjunto mínimo coerente seria
**`AOS-100` + os três itens sem ticket (§3.1–3.3)** — porque entregar o (1) sem os outros
três produz um sistema que *parece* distribuído e falha em silêncio no orçamento e nos
varredores.

- **A favor:** é a única forma de dizer «HA» com verdade.
- **Contra:** é a maior expansão de escopo da v1 até hoje. Os três itens em falta **já têm
  ticket** (`AOS-282`/`283`/`284`, criados 2026-08-30), pelo que a emenda deixa de apontar para o
  vazio — que é literalmente o defeito que o `REGISTO-Deferimentos` foi criado para
  impedir («um eixo que aponta para um epic sem ticket é operacionalmente indistinguível
  de não ter eixo nenhum»).

### Opção C — v1 single-host, mas com o limite imposto por código

Manter a forma, e acrescentar ao nó um guard de arranque que **recusa** arrancar se
detectar outra réplica viva sobre o mesmo Event Store.

- **A favor:** barato, e converte uma declaração documental numa barreira real. Hoje, o
  que impede alguém de correr duas réplicas é um parágrafo em `tecnica/10`.
- **Contra:** não dá HA a ninguém. É higiene, não solução — mas é higiene que falta.

**A minha recomendação: A, com C como trabalho imediato**, e B só quando existir um
requisito de HA nomeado por um utilizador real. A razão é a mesma que a Carta usa para o
D1(b): uma decisão condicional só abre quando o **gatilho nomeado** ocorre. Distribuir sem
esse gatilho é construir para uma necessidade suposta — e a §3.1 mostra que o custo de o
fazer mal é uma garantia de orçamento a falhar em silêncio, na direcção cara.

---

## 6. O que NÃO verifiquei

Em voz alta, para não passar por completude que não tenho:

- **Disciplina de partição da hash-chain (§3.3).** Vi o mutex por processo e a existência
  do conceito de partição; **não** segui o caminho até perceber se duas réplicas podem
  colidir na mesma. É o item que mais merece revisão independente.
- **Sandbox / pool de microVMs.** Não investiguei se o `AOS-103` tem dependências
  escondidas de host único.
- **D3 (SSE stdlib) e D5 (BFF single-process).** A própria Carta já os marca como «a
  reavaliar para o modelo de ameaça do nó-serviço» (emenda 1.2). Não os avaliei.
- **Custo em tempo.** Não estimei esforço por ticket — não tenho base histórica de
  velocidade neste repositório para o fazer com honestidade.

---

## 7. Nota de método

Os factos deste relatório vêm de leitura do código no HEAD indicado e de **uma medição
executada** (§2, o duplo `Open`), não de leitura de documentação. Onde citei um relatório
anterior (`desafio-A1`), verifiquei que a afirmação continua verdadeira no HEAD — o
`budget.Rebuild` continua sem chamador e o `Budget` continua sem API de re-hidratação.

Onde não verifiquei, está no §6.
