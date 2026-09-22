# ADR-029 — A regra do que um `stream_id` pode ser tem uma só fonte, e o contrato aperta-se de fora para dentro

| Campo | Valor |
|---|---|
| Estado | **Aceite (2026-09-22, AOS-424)** — com a §2.4 declarada BLOQUEADA |
| Decisores | Arquitecto de Plataforma |
| Consultados | ADR-007 (Event Store replicado), ADR-001 (execução durável), AOS-231 (grammar do `node_id`) |
| Supera / emenda | Nada. Fixa por escrito uma regra que existia só em código, em três cópias |

## 1. Contexto

O backend JetStream mapeia cada stream do AOS num *subject* NATS, onde o ponto separa tokens e
`*`/`>` são curingas. Um `stream_id` que os contenha **não é representável**, e a implementação
**recusa** — em vez de escapar em silêncio para um subject vizinho onde outro stream leria os
nossos eventos. A recusa é a escolha certa e não está em causa.

O que estava em causa é a **assimetria**: o backend de ficheiro não valida `stream_id` nenhum.
Um nome inválido funciona em desenvolvimento, em CI e em produção-sobre-ficheiro, e só falha na
topologia replicada — que é a única que arbitra entre processos (DEF-282) e, portanto, a única
onde o caminho do plano pode ter consumidor.

O AOS-424 mediu nove `stream_id` da árvore não representáveis, dois deles compostos em produção.
Foram todos corrigidos, dois com migração dos factos. Mas a **causa** — a assimetria — continua.

E havia uma segunda causa, mais discreta: **a regra vivia em três cópias** — o `ContainsAny` do
`jetstream.Store.subjectDe`, uma constante no nó, e a extracção por regex do gate
`scripts/ci/stream-names`. Nenhuma ligada às outras por nada estrutural. O custo dessa
duplicação foi medido duas vezes no mesmo ticket: a subtileza do `ErrConfig` deixou um defeito
CRÍTICO voltar a meio do trabalho, e a regra dos caracteres esteve a um `ContainsAny`
acrescentado de fazer o gate medir só o ponto e ficar verde com nomes inválidos na árvore.

## 2. Decisão

### 2.1 A regra tem UMA fonte, exportada pelo contrato

`eventstore.ValidarStreamID` e `eventstore.CaracteresNaoRepresentaveis`, em
`packages/substrate/eventstore/stream_id.go`.

A regra **não é do backend JetStream** — é do **contrato do Event Store**. O backend replicado é
apenas o primeiro sítio onde ela se manifestou. Quem precisa dela chama-a: o `subjectDe`
chama-a, o nó chama-a (deixou de ter cópia), e o gate lê essa constante.

### 2.2 Os caracteres recusados

`.`, `*`, `>`, espaço, tabulação, CR e LF.

**Convenção:** `-` onde a tentação for um `.`, e `/` para separar níveis. A barra é representável
**e** mantém o stream fora do alcance de `GET /runs/{id}/…`, porque o padrão da stdlib casa
`{id}` com um só segmento — o AOS-426 mediu treze streams internos a serem servidos por essa
rota. Streams internos do nó vivem sob `aos-internal/`.

### 2.3 Recusar, nunca escapar

Escapar `a.b` para `a-b` faria dois `stream_id` distintos colidirem no mesmo subject, e um
stream leria os eventos do outro. **Um nome que não se representa recusa-se; não se aproxima.**

### 2.4 O aperto do `Append` no backend de ficheiro fica DECIDIDO mas BLOQUEADO

É a correcção da causa-raiz, e está decidido que é para fazer. **Continua por fazer**, mas o
que o bloqueia MUDOU — e a mudança é o essencial desta revisão do ADR.

#### O bloqueio ORIGINAL, e como foi removido

A cadeia era esta, e terminava fora do código:

```text
apertar o Append  →  exige um `node_id` stream-safe
                  →  exige apertar o `plan.ValidNodeID`
                  →  exige uma versão nova do prompt de decomposição
                  →  exige revalidar a decomposição com o MODELO VIVO, em produção
```

Escolheu-se a **saída 2 da §3**: o `childRunID` passou a ESCAPAR o `node_id` de forma
injectiva. O `node_id` continua a poder ter pontos — o prompt não muda, o planeador não muda,
e nada precisa de ser revalidado contra o modelo vivo. O id do run filho é agora sempre um
`stream_id` válido.

Com isso, ligou-se também a validação do `run_id` ao `POST /runs`, que estava desligada pela
mesma razão. **A cadeia acima está fechada.**

#### O que bloqueia AGORA, medido

Experimentou-se ligar a validação ao `Append` do backend de ficheiro e correr as suites.
Resultado: **39 testes falham**, em quatro módulos — e a causa dominante é inesperada e
legítima:

- **Os testes das próprias MIGRAÇÕES** (`integration`, `platform/memory`) escrevem nos nomes
  LEGADOS para construir o mundo «antes». Com o `Append` a validar, **um teste deixa de
  conseguir montar estado legado** — e sem esse estado não se pode provar que a migração o
  transporta. Precisa de uma costura de teste (semear o stream sem passar pela validação), que
  é desenho próprio e não um efeito lateral deste ADR.
- **Cerca de vinte testes** em `cmd/aos` e `cmd/aos-orq` usam `stream_id` ou `run_id` com ponto
  escritos à mão. São correcções mecânicas.
- **A composição em runtime (AOS-425)** continua descoberta, e é o risco REAL que sobra: o
  `stream_id` de admissão contém o nome do modelo, que vem da allowlist ASSINADA. Hoje nenhum
  modelo dessa lista tem ponto (medido), mas apertar o `Append` converteria essa dívida latente
  em avaria viva no dia em que alguém acrescentasse um `gpt-4.1` — e a falha seria na
  ADMISSÃO, isto é, runs a deixarem de ser admitidos.

O bloqueio deixou de ser «isto parte produção hoje» e passou a ser «isto exige uma costura de
teste e o AOS-425». É uma dívida mais pequena e mais nomeada, mas continua a ser dívida.

## 3. Alternativas consideradas

**Apertar na LEITURA também.** Rejeitada, e a razão é não-óbvia: as migrações do AOS-424 leem os
nomes legados para os copiar. Pior — o `CopiarStream` trata `ErrConfig` na leitura da origem como
«não há nada para migrar», o que é verdade no JetStream (lá nunca se pôde escrever esse nome) e
**falso** no ficheiro. Validar a leitura faria as migrações saltarem dados reais e reportarem
zero copiados. A fronteira certa é: **criar um nome valida; ler ou restaurar um nome que já
existe não.**

**Apertar o `IngestStream`.** Rejeitada pela mesma razão, com um agravante: os backups existentes
contêm `gov.approvals` e `memory.*`. Validar o restauro tornaria-os irrestauráveis.

**Escapar o `node_id` no `childRunID`** — **ESCOLHIDA e implementada.** Evita mexer no prompt e
no que o planeador pode emitir, ao custo de ids de run filho menos legíveis nos casos que
precisam de escape (os comuns atravessam intactos).

A marca é `+`, e a escolha não é arbitrária: **não pertence à gramática do `node_id`**, pelo que
um id válido nunca é tocado. A primeira tentativa usou `_`, que pertence — e o próprio teste de
injectividade apanhou a consequência: `a.b` escapava para `a_2eb` e o `node_id` `a_2eb`
atravessava intacto, **colidindo no mesmo run filho**. Dois nós do plano no mesmo stream, que é
pior do que o problema original. O `+` também é literal num segmento de caminho de URL — ao
contrário do `%`, que partiria o `GET /runs/{id}` com que o executor consulta o estado do filho.

**Deixar como está.** Rejeitada: a assimetria é a causa-raiz, e cada `stream_id` novo é uma
oportunidade de a classe reabrir. O gate `stream-names` cobre os nomes **literais**; a
composição em runtime (AOS-425) continua descoberta.

## 4. Consequências

**O que fica melhor.** A regra deixa de poder derivar: uma cópia a mais é apanhada por teste
(`TestAOS424RegraDoStreamIDNaoEDuplicadaNoNo`), e o gate lê a declaração canónica com âncora e
piso — verificado por mutação que renomear a constante ou relaxá-la faz o gate falhar fechado.

**O que fica pior, e é preciso dizê-lo.** A decisão da §2.4 está tomada e não executada, e isso é
uma forma de dívida: quem ler este ADR pode concluir que a assimetria está fechada. **Não está.**
Fecha-se no dia em que o `node_id` for stream-safe — ou por aperto do `ValidNodeID` com prompt
novo e validação com o modelo vivo, ou por escape reversível no `childRunID`.

**Resíduos declarados:**

- **A §2.4**, acima. É o único eixo da causa-raiz que continua aberto.
- **A composição em runtime** (AOS-425): o `stream_id` de admissão contém o nome do modelo, que
  vem da allowlist assinada. Um gate estático não a vê, e a correcção lá é validar onde o valor
  ENTRA.
- **O `Subscribe` falha em silêncio**: não valida o filtro — um filtro por um nome impossível
  não dá erro, nunca casa nada.
- **Duas constantes de nome LEGADO** continuam na baseline do gate. Não são streams em uso: são
  os nomes que as migrações precisam de LER. Saem quando puderem desaparecer.
