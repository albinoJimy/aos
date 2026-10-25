# Desenho — a VIA DE ENTRADA: enviar um ficheiro para um run, 2026-10-25

> Pedido do dono a 2026-10-25, depois de perguntar se o trabalho da fase R2 levava aos quatro
> exemplos dele: fotografar um compartimento e pedir a projecção dos móveis; enviar imagens ou PDF e
> fazer OCR; enviar um objectivo e o harness trabalhar até terminar gerando artefactos; artefactos
> que possam ser qualquer coisa.
>
> A resposta medida foi: **três dos quatro estão bloqueados pela mesma coisa, e ela não é uma fase
> do roteiro.** O roteiro R0–R6 do ADR-043 tem sete linhas e nenhuma mete um ficheiro no sistema:
> todas assumem que o dono semeia uma pasta no servidor. Este documento desenha a linha que falta.

---

## 1. O que o dono ganha, e o que NÃO ganha

**Ganha:** enviar uma imagem no pedido de um run — ou de um nó de um plano — e ter as tools de
modelo a trabalhar sobre ela, sem copiar nada para o servidor e sem o operador no meio. É o que
transforma «o dono semeia uma pasta e o agente lê de lá» em «o cliente manda o ficheiro».

**NÃO ganha, e vale dizê-lo antes de ler o resto:**

- **a projecção dos móveis.** Isto é a metade da ENTRADA. Gerar uma imagem é a fase **R3** do
  ADR-043 («partes de **saída**; preço por imagem»), e não está feita. Com esta via mais a R3, o
  exemplo 1 do dono funciona; com uma só, não.
- **o PDF.** Não é uma consequência desta via: é uma decisão, com custo, e está na §5.
- **artefactos de qualquer coisa.** É o outro lado (a SAÍDA) e tem o seu próprio problema (§8).

---

## 2. O estado de partida, medido a 2026-10-25

| O que existe | Onde | O que diz |
|---|---|---|
| A única via de media | `packages/cmd/aos/tool_de_modelo.go:202`, `ocr_tool.go:28-31` | A `ocr_read` e a `media_view` lêem de uma montagem **só de leitura** de `AOS_OCR_SEED_DIR`, confinada com `os.Root`, **só o nível de topo**, só ficheiros regulares, PNG ou JPEG pelos primeiros bytes, até 4 MiB |
| Rotas do nó | `packages/cmd/aos/api.go` | `/runs`, `/runs/`, `/steer`, `/pause`, `/exhaustion`, `/config`. **Nenhuma aceita um ficheiro** |
| O canal de entrada que existe | `api.go:810-827` | `plan_input` leva `from`, `output`, `digest`, `content` — **texto**, com `digest` sha256 conferido na porta. Tectos: 16 payloads, **128 KiB** cada, **512 KiB** no agregado, contados ANTES de o run existir |
| O tecto de uma imagem | `packages/platform/model-gateway/port/image.go:41` | `MaxImageBytes = 4 MiB`, por parte e na soma das partes de um pedido |
| Modalidades que o gateway aceita | `port/image.go:34-35` | `image/png` e `image/jpeg`. **E mais nada** |
| A fronteira de ingestão | `packages/integration/ingestion.go:173` | `IngestObjective` trata **só o objectivo**. Os `plan_input` **não passam por lá** |
| O espaço de handles | `packages/cmd/aos/handle_store.go` | Conteúdo cifrado por titular **com o run nos dados associados** (um blob copiado para a pasta de outro run não abre), proveniência selada em `handle.created`, tectos próprios, **nenhuma rota HTTP serve o conteúdo**, emissor próprio |
| Quem já sela entradas | `packages/cmd/aos/handles.go`, `selarEntradasDoPlano` | Num layout por handle, **cada `plan_input` já é selado no espaço de handles** antes do run arrancar, e o modelo recebe uma referência opaca |

---

## 3. A decisão: a via de entrada JÁ EXISTE, e é o espaço de handles

A fase R2 construiu, para conteúdo produzido **dentro** de um run, exactamente a máquina que
conteúdo vindo **de fora** precisa:

- uma **referência opaca** que o modelo pode passar como argumento de uma tool e **nunca ler**;
- cifra por titular **com o run nos dados associados**;
- **proveniência selada** no stream do run;
- tectos, vocabulário de erro e superfície de leitura **próprios**;
- **nenhuma rota HTTP serve o conteúdo** — ingerir um ficheiro não cria uma superfície de leitura;
- apagamento pelo crypto-shred do titular.

Então a via de entrada **não é um mecanismo novo**: é o canal de entrada que já existe a aprender a
carregar bytes tipados, e o selo que já corre a selá-los. Falta-lhe **três coisas pequenas e uma
decisão**.

### 3.1 O que falta (1): a entrada declara um TIPO

O `plan_input` é texto por omissão de qualquer declaração. Passa a levar `media_type`, de um
vocabulário **fechado** — `image/png` e `image/jpeg` na primeira versão, porque são as que o gateway
aceita (§2) e abrir mais do que o gateway aceita é prometer o que não se cumpre.

O tipo é **conferido pelos bytes**, nunca pelo que o cliente declara: os primeiros bytes têm de ser
os do tipo, pela mesma função que a `ocr_read` já usa para o fazer na pasta semeada. Uma declaração
que não bata com os bytes **recusa o pedido** — não se corrige em silêncio, e não se adivinha.

### 3.2 O que falta (2): tectos de media, separados dos de texto

Os tectos actuais são de texto e estão certos para texto: 128 KiB por entrada e 512 KiB no agregado
são generosos para um documento e absurdos para uma fotografia. A entrada tipada ganha tectos
próprios, alinhados com o que o gateway impõe a jusante (`MaxImageBytes`, 4 MiB) — **nunca maiores**,
porque um tecto de entrada maior do que o de saída só serve para aceitar o que vai ser recusado
depois, e com o custo já pago.

Os de texto não mudam. A contagem é **por tipo**, e o agregado de media é próprio.

### 3.3 O que falta (3): as tools lêem do espaço do run, não da pasta

A `ocr_read` e a `media_view` resolvem o `doc_id` contra `AOS_OCR_SEED_DIR`. Passam a resolver
**também** contra o espaço de handles do run — e a ordem é: primeiro o handle do run, depois a pasta
semeada, nunca as duas para o mesmo `doc_id`.

**Porque as duas e não a substituição:** a pasta semeada é o que o dono controla e é a origem de
tudo o que está provado em produção hoje (as séries vid1–vid5, a prova do OCR). Trocá-la por esta
via numa só alteração junta uma capacidade nova com uma regressão possível no que já funciona. A
pasta sai quando a via nova tiver prova em produção, não antes.

### 3.4 A decisão: o transporte é `base64` no canal que existe

O `content` de um `plan_input` é uma string JSON, pelo que bytes vão em `base64` — **+33%** no corpo
do pedido.

**Porque não `multipart` nem uma rota de upload própria.** Os três custam mais do que compram:

| Alternativa | Porque não |
|---|---|
| `multipart/form-data` no `POST /runs` | Duas formas de ler o mesmo pedido, e o corpo deixa de ser um documento que se audita, se repete e se compara. A porta tem hoje UMA forma, e isso é uma propriedade |
| `POST /media` de duas fases, que devolve uma referência que o submit nomeia | Um **ciclo de vida novo**: conteúdo ingerido que nunca chega a ser usado precisa de dono, de tecto e de varrimento. É o problema das declarações órfãs do AOS-556 outra vez, e nesta forma é por desenho em vez de por acidente |
| Uma rota que escreve na pasta semeada | Põe o cliente a escrever numa montagem que é só de leitura **por desenho** (ADR-042 §2.4), e transforma a pasta do dono num depósito partilhado |

**E o `base64` sozinho NÃO SERVE — medido, e inverte a recomendação que esta secção tinha.** O tecto
do corpo de um pedido é **1 MiB** (`DefaultMaxBodyBytes`, `api.go:82`); uma imagem de 4 MiB em
`base64` são ~5,5 MiB, **5,5× acima**. O «dia em que uma entrada legítima não caiba» é o dia um, e
escrever o contrário era a afirmação por medir desta secção.

Com a medição, a escolha é entre duas, e a mais pequena continua a ser esta via — **com uma condição
que o dono aceita, não com uma omissão**:

- **Subir o tecto do corpo**, que já é configurável (`WithMaxBodyBytes`, `api.go:364`), para o que a
  media exigir (~6 MiB). **É uma mudança de postura, não um detalhe:** a rota que cria runs passa a
  aceitar corpos seis vezes maiores. O que a contém é o que já a contém — a rota é autenticada e tem
  rate-limit —, e o tecto de media (§3.2) continua a recusar antes de o run existir. Custo: um
  número na configuração de produção e um corpo maior em memória por pedido em voo.
- **A via de duas fases**, que deixa o tecto do corpo em paz e traz um ciclo de vida novo: conteúdo
  ingerido que nunca chega a ser usado precisa de dono, de tecto e de varrimento — o problema das
  declarações órfãs do AOS-556, desta vez por desenho.

**Recomendação:** subir o tecto, porque é um número contra um subsistema. **O gatilho para a via de
duas fases passa a ser outro, e é o honesto:** o dia em que o tamanho de uma entrada legítima deixar
de ser razoável num corpo de pedido — um vídeo, um PDF de muitas páginas —, e não uma fotografia.

### 3.5 O que NÃO se faz, e porquê

- **Não nasce armazém novo.** O espaço de handles é ele.
- **Não nasce rota de leitura.** Ingerir conteúdo não precisa de o servir de volta, e acrescentar
  uma seria o inverso do que a R2 compra (hoje **nenhuma** rota serve o conteúdo do espaço de
  handles, e isso está declarado no banner).
- **A entrada não vai ao armazém de artefactos.** Ele é a SAÍDA — tem rota de download, tipos de
  texto e validação de conteúdo desenhadas para o que o run produz. Misturar as duas direcções no
  mesmo sítio juntaria uma superfície de leitura a conteúdo que vem de fora.

---

## 4. A consequência que ninguém pediu e tem de ser dita

**O conteúdo ingerido NÃO é redigido.** A fronteira de ingestão trata só o objectivo
(`ingestion.go:173`); os `plan_input` nunca passaram por lá, e bytes de imagem não passam por um
redactor de texto. Uma fotografia de um documento com dados pessoais entra tal e qual, é cifrada por
titular, e viaja para o fornecedor do modelo quando uma tool a consumir.

Isto **já é verdade hoje** para o que o dono semeia na pasta — a diferença é que passa a ser verdade
para o que um cliente envia, o que muda quem pode causá-lo. Não é um defeito desta via: é a sua
consequência, e pertence à decisão do dono, não a um ticket.

O que a contém é o que já existe: o SA-ROC classifica pela sensibilidade efectiva, o taint eleva, o
egress é default-deny com allowlist, e o apagamento é o crypto-shred do titular. O que **não** existe
é redacção de imagem, e desenhá-la não é trabalho desta via.

---

## 5. O PDF é uma decisão, não uma consequência

O gateway aceita `image/png` e `image/jpeg` (§2). PDF não é uma modalidade de entrada em sítio
nenhum do sistema. Há duas saídas, e são diferentes em natureza:

1. **Rasterizar as páginas antes de as mandar.** É código a correr sobre conteúdo vindo de fora —
   isto é, a classe de risco da fase **R5** (`code_exec` na gVisor, sem rede, imagem fixada por
   digest). Um descodificador de PDF é uma superfície de ataque conhecida, e pô-lo noutro sítio que
   não a sandbox seria contradizer a razão pela qual a R5 existe.
2. **Um fornecedor que aceite partes de PDF.** Tira o descodificador de dentro do perímetro e
   põe-no no fornecedor. Depende do catálogo qualificado (R4) e da política de elegibilidade.

**Recomendação:** não fazer nenhuma das duas nesta via. Entregar imagens primeiro, que é o que
desbloqueia dois dos quatro exemplos, e tratar o PDF quando a R5 existir — porque a opção (1) sem a
sandbox é a troca errada, e a (2) é uma decisão de catálogo que não pertence aqui.

---

## 6. As peças, por ticket

A ordem não é de conveniência: cada uma é provável sozinha, e a última é a que muda produção.

| # | Conteúdo | Epic sugerido | Depende de |
|---|---|---|---|
| **A** | A entrada tipada: `media_type` no `plan_input`, vocabulário fechado, tipo conferido **pelos bytes**, tectos de media próprios alinhados com o gateway, e o `digest` conferido como hoje. Desligado por omissão | EPIC-02 | — |
| **B** | O selo da entrada tipada no espaço de handles, com o media type a viajar nos factos do handle (o `MediaType` já existe em `HandleFacts`) e a proveniência a distinguir uma entrada ingerida de um `tool_result` | EPIC-07 | A |
| **C** | A `ocr_read` e a `media_view` a resolver o `doc_id` **primeiro** no espaço de handles do run, depois na pasta semeada, nunca as duas para o mesmo id | EPIC-02 | B |
| **D** | A prova em produção: um `POST /runs` com uma fotografia e um objectivo, e o OCR a correr sobre ela sem nada ter sido copiado para o servidor | — | C |

**A, B e C não precisam de cerimónia de chave.** Nenhuma mexe em política: o `cap:fs.read` da
`ocr_read` é o mesmo, o destino declarado é o mesmo (`external_read`), e a classe de destino do
ADR-044 não muda — o que muda é **de onde** a tool lê os bytes, e isso é config trusted do operador,
não autorização.

---

## 7. O que é do dono

1. **Decidir o PDF** (§5): esperar pela R5, ou abrir a decisão do catálogo.
2. **Aceitar por escrito a consequência da §4**: conteúdo ingerido não é redigido, e quem o pode
   causar passa a ser quem submete.
3. **Subir o tecto do corpo do pedido** de 1 MiB para ~6 MiB na rota de submissão (§3.4). É uma
   mudança de postura, medida e não suposta: sem ela nenhuma fotografia entra.
4. **A prova em produção** (peça D).

---

## 8. Resíduos e o que fica fora

- **Artefactos de qualquer coisa** (o 4.º exemplo do dono) é a SAÍDA e fica fora desta via. O
  armazém de artefactos aceita cinco tipos, todos de texto (`artifact_store.go:96-102`), e a razão
  não é desleixo: a validação de conteúdo que ele faz — JSON que compila, SVG sem script nem
  ligações externas — **não tem equivalente para binário**, e a rota de download serve o que ele
  guarda. Abrir binário ali é uma decisão com custo próprio, e merece o seu desenho.
- **Gerar imagens** é a R3, e sem ela o exemplo 1 do dono não fecha.
- **A pasta semeada não sai** nesta via (§3.3). Sai quando a via nova tiver prova em produção.
- **Vídeo ingerido** fica fora: a `video_read` lê da mesma pasta, e o tecto de um vídeo não é o de
  uma imagem. A mesma máquina serve, com outros números, e é ticket próprio.
- **O tecto do corpo do pedido: MEDIDO, e inverteu a §3.4.** `DefaultMaxBodyBytes` é 1 MiB
  (`api.go:82`), contra ~5,5 MiB de uma imagem de 4 MiB em `base64`. A peça A passa a incluir subir
  o tecto, e isso é uma mudança de postura que o dono aceita (§7).
- **`NÃO VERIFICADO`: o valor em vigor no nó de PRODUÇÃO.** Medi o default do código, não o que o
  `.env` do servidor define — pode já estar acima. É leitura de uma linha em produção, e vale
  fazê-la antes de escrever o número na peça A.

---

## 9. O que isto desbloqueia, dos quatro exemplos

| Exemplo do dono | Com esta via | Falta |
|---|---|---|
| Foto de um compartimento → projectar os móveis | a metade da ENTRADA | **R3** (partes de saída) |
| Enviar imagens e fazer OCR | **fecha** | — |
| Enviar PDF e fazer OCR | — | a decisão da §5 |
| Objectivo → harness até terminar, com artefactos | já funciona; passa a poder receber a entrada do cliente | — |
| Artefactos que podem ser qualquer coisa | — | a decisão da §8 |
