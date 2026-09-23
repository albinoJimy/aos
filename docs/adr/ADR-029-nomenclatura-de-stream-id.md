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

`.`, `*`, `>`, espaço, tabulação, CR e LF — **e todos os restantes caracteres de controlo**
(`< 0x20` e `0x7f`).

**A segunda metade foi acrescentada depois, e a razão importa.** A primeira lista era uma lista
NOMEADA: os caracteres que alguém escreve à mão num literal, que é o que o gate de repositório
varre. Quando o `Append` apertou, apareceu um nome que nenhum humano escreveu — o autenticador
compunha o escopo do nonce como `<domínio>\x00<emissor>`, e esse **NUL ia inteiro para o nome do
stream**. A regra deixava-o passar, porque a lista só continha o que quem a escreveu se lembrou.

Uma lista nomeada cobre o que se enumerou. Para uma CLASSE inteira, a pertença decide-se por
propriedade — e é assim que está escrito em `ValidarStreamID`.

**Convenção:** `-` onde a tentação for um `.`, e `/` para separar níveis. A barra é representável
**e** mantém o stream fora do alcance de `GET /runs/{id}/…`, porque o padrão da stdlib casa
`{id}` com um só segmento — o AOS-426 mediu treze streams internos a serem servidos por essa
rota. Streams internos do nó vivem sob `aos-internal/`.

### 2.3 Recusar, nunca escapar

Escapar `a.b` para `a-b` faria dois `stream_id` distintos colidirem no mesmo subject, e um
stream leria os eventos do outro. **Um nome que não se representa recusa-se; não se aproxima.**

### 2.4 O aperto do `Append` no backend de ficheiro — FEITO

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

Escolheu-se a **saída 2 da §3**: o `childRunID` passou a ESCAPAR o `node_id` de forma injectiva.
O prompt não mudou, o planeador não mudou, e a cadeia fechou-se.

#### O aperto, e onde está

`Store.Append` chama `ValidarStreamID` **antes** de tocar em stripe, líder ou quórum. A recusa é
lexical: não depende de topologia, não deixa rasto, e um nome irrepresentável nunca chega a ter
um seq atribuído. A costura `SemearStreamLegado` é a única excepção, e tem duas barreiras —
`testing.Testing()` em runtime e uma verificação no gate `stream-names`.

Leitura, `StreamHead`, `SnapshotStream` e `IngestStream` **não** validam, pelas razões da §3, e a
assimetria está fixada por teste (`TestAOS424LeituraERestauroAceitamNomeLegado`) precisamente
para que ninguém a «arrume».

#### O QUE O APERTO REVELOU, e que é o essencial desta revisão

A medição anterior deste ADR dizia que o aperto custava «39 testes, na maioria andaime das
migrações». **Estava errada**, e estava errada de uma maneira que vale a pena registar: foi feita
a olhar para uma lista de nomes de teste, não para as suas causas.

As causas verdadeiras, medidas: **catorze das quinze falhas do `cmd/aos` eram um DEFEITO VIVO**,
não andaime. Dois streams do caminho de autorização compunham o nome a partir de valores que
ninguém validava:

| Stream | O escopo vinha de | O carácter |
|---|---|---|
| `ratify-nonce:<escopo>:<hex>` | constantes de domínio do autenticador | `.` em `foureyes.challenge`, `governance.dsar`, `nhi.revoke` |
| `ratify-nonce:<escopo>:<hex>` | o tuplo `<domínio>\x00<emissor>` do `nonceScope` | `\x00` |
| `4eyes-challenge:<escopo>:<hex>` | o `request_id` do CORPO de um pedido | o que o cliente lá puser |

Sobre o substrato de ficheiro nada disto falha — e é por isso que sobreviveu a dez gates, a uma
revisão adversarial e ao smoke, que correm todos sobre ficheiro. **Sobre JetStream o
`ConsumeNonce` devolveria erro de backend, o gate de ratificação trataria isso como bloqueio, e
toda a emissão de challenges e toda a ratificação seriam negadas** — com um `403 aprovador nao
autorizado`, que nomeia a causa errada.

A correcção é na ORIGEM e não no ponto de uso: o escopo entra RESUMIDO no nome
(`hitl.nomeDeEscopo`). Resumir, e não escapar nem recusar — o alfabeto de entrada aqui é
arbitrário (um `RatificationID` é um token opaco de fonte externa), e uma marca de escape que
pertença ao alfabeto de entrada colide, que foi exactamente o erro cometido no escape do
`node_id` e apanhado pelo seu teste de injectividade.

**Custo declarado da renomeação**, porque muda nomes de streams em uso:

- **Nonces consumidos** antes do deploy são esquecidos: o CAS num stream novo e vazio vence, e o
  nonce conta como FRESCO. A janela de replay é a da frescura de ratificação — fora dela o
  sinal já é recusado por idade, antes de se chegar ao nonce.
- **Challenges emitidos** antes do deploy deixam de ser encontrados. É **fail-closed** e está
  contratado: `IsChallengeIssued` devolve `(false, nil)` em stream inexistente. O aprovador pede
  outro challenge; o `ChallengeTTL` em vigor é de 5 minutos.

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

**O que fica pior, e é preciso dizê-lo.** Os nomes dos streams de nonce e de challenge MUDARAM,
com o custo declarado na §2.4. E o aperto trocou um defeito silencioso por uma avaria visível:
onde antes um nome irrepresentável passava e só partia na migração para JetStream, agora a
escrita recusa. Para os caminhos corrigidos isso é o que se quer; para os que a §2.4 não alcança,
é o argumento central do AOS-425 — a recusa dá-se no ponto de USO, longe de onde o valor entrou.

**O que este ADR NÃO permite concluir.** Que a classe está fechada. O aperto encontrou os
defeitos que EXISTEM NA ÁRVORE DE TESTES; um caminho de composição que nenhum teste exercita com
um valor «sujo» continua invisível. A tabela do AOS-425 tem seis linhas e este trabalho fechou
duas.

**Resíduos declarados:**

- **A composição em runtime** (AOS-425): os sítios VIVOS estão fechados; a classe **não**.
  Ver a correcção abaixo e os resíduos declarados no ticket — uma policy com `models: ["*"]`, os
  bytes não-ASCII no escape do `node_id`, e o facto de não haver teste sobre JetStream.

#### CORRECÇÃO (AOS-425) — a gravidade que este ADR atribuiu à admissão estava errada

A revisão anterior afirmava que, com o `Append` apertado, acrescentar um `gpt-4.1` à allowlist
«passa a ser runs a deixarem de ser admitidos». **Isso exigiria que o caminho de admissão
corresse, e ele não corre.**

Medido ao executar o AOS-425: nada na árvore constrói `[]tiering.Tier` fora de testes, o pacote
`control-plane/scheduler` não tem importador de produção a não ser o `tieradapter` (que também
não tem chamador), e o wiring do nó declara-o explicitamente — `DEFERIDO (DEF-280-NO)`. O
`budget.WithEmitter`, que armaria o emissor durável, também não tem chamador.

A afirmação veio de ler a tabela do AOS-425 em vez de verificar se o caminho estava ligado. É a
mesma falha de método que este ADR já regista uma vez (medir por nomes em vez de causas), e é a
segunda vez no mesmo eixo.

**O que era REALMENTE o risco vivo**, e que a tabela não tinha: o flag `--run` do `aos-orq`, que
corre em produção desde a v0.1.20. O valor torna-se quatro nomes de stream e não era validado —
o nó guardava o mesmo valor nas duas portas HTTP e o binário não guardava nenhuma. Fechado pelo
AOS-425.
- **O `Subscribe` falha em silêncio**: não valida o filtro — um filtro por um nome impossível
  não dá erro, nunca casa nada.
- **Duas constantes de nome LEGADO** continuam na baseline do gate. Não são streams em uso: são
  os nomes que as migrações precisam de LER. Saem quando puderem desaparecer.
