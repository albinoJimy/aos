# ADR-044 — A política de fluxo de dados decide por DESTINO e PROVENIÊNCIA, não por um binário

- **Estado:** Proposto (2026-10-25, AOS-556) — ratificação e assinatura do bundle são do dono (§7)
- **Decisores:** dono do produto; Arquitecto de Plataforma
- **Contexto técnico:** fase R2 do ADR-043; materializa a opção A do ADR-034 (§3 A, §5.4)
- **Supersessões:** nenhuma. **Emenda** o ADR-034 (§2.3, resíduo 3) e o ADR-011 (o `context` do bundle
  ganha dois atributos)

---

## 1. Contexto

O AOS-551 entregou o mecanismo da fase R2 e entregou-o **desligado**: o conteúdo não-confiável sai do
contexto do modelo e viaja por handle opaco (layout 1.8.0), e a autoridade de uma tool call passa a
ser `join(piso, taint de cada handle resolvido nos seus argumentos)`. O AOS-556 fechou os quatro
resíduos que bloqueavam o interruptor.

**O que falta é o que dá sentido a tudo isso.** Com a regra binária que existe hoje — «untrusted não
comanda» —, uma call que consome um handle untrusted continua **negada**, exactamente como antes da
separação. A separação, sozinha, não muda nenhuma decisão: é segura e não compra nada. A bateria de
injecção do AOS-551 fixa essa honestidade por escrito, e o `handles.go` di-lo no sítio da decisão.

O motivo de existir da R2 — «gerar a partir do que se viu, sem uma pessoa a aprovar cada chamada» —
só se realiza com uma política que decida **sobre** untrusted em vez de o negar em bloco. A forma, da
decisão (4) do dono de 2026-10-23: um handle untrusted **pode** fluir para o gateway de modelo, e
**não** pode fluir para o `web_post`.

### 1.1 As duas incógnitas, agora medidas

O ticket mandou medir antes de escrever. O resultado inverte o que a v1 do desenho supunha.

**F1 — a decisão não é expressável no motor de hoje, e não é por falta de um campo só.** O motor
Cedar conhece **quatro** atributos e mais nenhum (`principal.authority`, `resource.region`,
`context.taint`, `context.sensitivity`, fixados em `smokeAttrMapaFixo`). Uma regra que leia outro
**compila e assina** e em runtime devolve `ErrMalformedRequest`, que contamina toda a autorização
daquela capability — um deny-all silencioso. É o defeito que `attrRefsForaDoMapa` e
`SmokeDecideRules` existem para apanhar, e é por isso que o `policy-sign` corre o smoke antes de
declarar verde.

**F2 — a capability não distingue o destino, medido no manifesto de produção.** Quatro das cinco
tools declaram `cap:fs.read`: a `doc_read` (sem egress), a `media_view` (sem egress) e a `ocr_read` e
a `video_read` (`egress: external`, com bloco `modelo`). Para o Cedar, ler um documento do disco do
dono e enviar um documento para um fornecedor de modelo são **a mesma `action`**. Mais: a
`allow_fs_read` **já permite** untrusted (é a única entrada da baseline do `policy-taint`), pelo que
quem hoje barra a `ocr_read` depois de conteúdo de fora **não é o Cedar — é o TaintGate**, armado em
produção com `AOS_PRIVILEGED_CAPS=cap:http.post,cap:fs.read`, que a classifica privilegiada pelo
`egress: external` declarado.

**F3 — o `egress` sozinho também não distingue.** A `ocr_read` e o `web_post` declaram **os dois**
`egress: external`. O que os separa é o eixo ao lado: `mutation` — `none` na primeira, `mutates` no
segundo. Uma política que decidisse só por egress abria o `web_post` ao mesmo tempo que o gateway de
modelo, que é precisamente o que esta fase não pode fazer.

**F4 — a proveniência colapsa num escalar antes de chegar ao PDP, mas é distinguível a montante.** A
autoridade da call é **uma** string, e um run com media e um run com um handle untrusted nos
argumentos chegam ambos `untrusted`, indistinguíveis. O controlo negativo que o dono pediu («o mesmo
plano com media continua negado») exige separá-los. E são separáveis: no sítio da re-junção
(`agentruntime.Runtime.mediateToolCall`, em `kernel/agent-runtime/loop.go`) o kernel tem o **piso**
e o valor **da reescrita** em mãos, cada um por si, imediatamente antes de o `join` os colapsar.

---

## 2. Decisão

### 2.1 A decisão é por (capability, destino, proveniência do que entra), e vive na política assinada

A autorização de uma tool call que consome conteúdo não-confiável deixa de ser um binário sobre o
rótulo do contexto e passa a ser uma função de três coordenadas:

- **capability** — a `action` Cedar, como hoje;
- **destino** — a classe do efeito que a tool DECLARA no registo de tools, config trusted do
  operador e nunca saída do modelo (§2.2);
- **proveniência do que entra** — de onde vem o `untrusted` desta call concreta (§2.3).

A decisão continua a ser **default-deny** e continua a viver no bundle Cedar **assinado**. Não há
excepção nova em código: o que entra em código é o transporte das duas coordenadas novas até ao
motor, e o motor a saber lê-las.

### 2.2 O destino é uma CLASSE derivada dos três eixos que o operador JÁ declara

Nenhum eixo novo no manifesto de tools. A classe deriva-se dos três que o AOS-538 e o AOS-549 já
exigem explícitos (`egress`, `mutation`, `reversibility`), no molde de `ToolDestination.IsLocalRead`
e `IsLocalRunWrite`, que já fazem exactamente isto:

| Classe | Deriva de | Tools de produção |
|---|---|---|
| `local_read` | `egress: none`, `mutation: none`, `reversibility: reversible` | `doc_read`, `media_view` |
| `run_artifact` | `egress: none`, `mutation: run_artifact`, `reversibility: reversible` | `artifact_write` |
| `external_read` | `egress: external`, `mutation: none`, `reversibility: reversible` | `ocr_read`, `video_read` |
| `external_effect` | tudo o mais com `egress` ou `mutation` declarados | nenhuma em produção: o `web_post` saiu do manifesto em 2026-09-26 (ADR-034 §2.7) e o dos demos deixa a `reversibility` implícita, logo classifica `unknown` |
| `unknown` | um eixo implícito, ou tool fora do registo | — |

**Porque uma classe e não os três eixos crus.** Três atributos novos no motor são três superfícies
de regra mal-atributada, e a regra que os combinasse repetiria em Cedar a derivação que o kernel já
faz — duas cópias da mesma regra, a forma de defeito que este repositório paga repetidamente. A
classe é **uma** string, derivada **uma** vez, pela função que o gate de taint e o classificador de
risco já consultam.

**`unknown` é fail-closed:** não satisfaz nenhuma regra de fluxo, pelo que uma tool sem destino
declarado fica com a regra de antes. Um eixo implícito nunca se lê como benigno — é a invariante que
o AOS-538 fixou e que esta classe herda.

### 2.3 A proveniência é selada onde ainda é distinguível, e NÃO é o rótulo de taint

Um campo novo no `CallContext`, escrito pelo kernel no mesmo sítio e no mesmo instante em que faz a
re-junção, com o vocabulário fechado:

| Valor | Significa |
|---|---|
| `clean` | o piso é trusted e a call não resolveu handle nenhum |
| `handle_argument` | o piso é trusted e a call resolveu ao menos um handle untrusted nos argumentos |
| `context` | o piso já era untrusted — media projectada, um `tool_error`, memória, um steer |

`context` **domina**: se o piso é untrusted, a proveniência é `context`, qualquer que seja o que a
call consuma. É isso que preserva a decisão (c) do dono de 2026-10-23 («num run com media a R2 não
levanta nada») **sem a política ter de saber o que é media**.

Três propriedades que não se negoceiam:

1. **Não substitui o taint.** `context.taint` continua a ser o rótulo da autoridade, continua a ser
   o que o TaintGate impõe e o que o classificador de risco eleva. A proveniência **acrescenta**
   porquê, e só a política de fluxo a lê.
2. **Não é auto-declarável.** É escrita pelo kernel, do piso e do resultado da reescrita, e é
   **sempre reescrita** — um valor que um chamador ponha na `Call` nunca chega ao PDP como se fosse
   a derivação do kernel. É o molde de `Context.TaintPrivilege` (AOS-538).
3. **Atravessa as DUAS vias de despacho.** O ADR-034 §2.8 nomeia a classe de defeito «um campo novo
   que uma das vias esquece», e o AOS-454 mostrou que a tradução já perdeu um campo do `Call` quatro
   vezes. O campo é escrito no kernel, antes do despacho, pelo que as duas vias o levam por
   construção — e o guard-test do §2.4 impõe-no.

### 2.4 Dois atributos novos no motor é mudança de MOTOR, declarada, com guard-test próprio

O critério 3 do AOS-556 pedia a medição e ela deu afirmativo. Entram:

- `pdp.DecisionContext` ganha a proveniência e a classe de destino;
- `inputFromCall` copia-as (é o único fio entre o RM e o PDP, e o comentário dele registra que
  esquecer uma cópia já custou a taxonomia de autonomia inteira);
- `cedarEngine.evaluate` monta `context.provenance` e `context.destination`;
- `smokeAttrMapaFixo` e o conjunto do smoke aprendem os dois nomes, senão uma regra que os use
  assina e nega tudo.

**O guard-test é por reflexão sobre `pdp.Input`**, no molde exacto de
`TestAOS454_AuditoriaCampoACampoCallActivityCall`: cada campo exportado é preenchido com uma
sentinela e tem de chegar ao PDP igual, ou constar de uma lista de não-propagados **com razão
escrita** — e nesse caso chegar diferente. Hoje não existe guard-test nenhum sobre a forma do
`Input` (zero reflexão nos testes do PDP); o que há é indirecto. Passa a existir, e é o que torna a
próxima omissão vermelha em vez de silenciosa.

### 2.5 Quem sela o destino é o classificador de risco, e a ORDEM da cadeia é portadora

`RiskClassifier.Evaluate` já corre **antes** da política na cadeia do ápice, já detém o registo de
destinos e já deriva os eixos declarados (`riskAxesForCall`). Passa a selar também a classe de
destino no `CallContext`, no mesmo sítio em que sela a classe de risco.

Não é conveniência: é a única posição que funciona. O gate de taint, que é quem hoje carimba o
porquê da classificação pelo destino, corre **depois** da política — um carimbo dele chegaria tarde
para o Cedar o ler. A ordem «classificador antes da política» já está imposta por
`cadeia_ordem_risco_test.go`, que avermelha se o classificador sair da cadeia ou vier depois da
política; esse teste ganha a segunda razão.

### 2.6 A `allow_fs_read` PARTE-SE por destino, e a baseline do `policy-taint` encolhe para ZERO

**Decisão do dono, 2026-10-25.** A regra única é substituída por regras por classe de destino:

- **`allow_fs_read_local`** — `cap:fs.read` com `context.destination == "local_read"`. Permitida
  **também** com o contexto untrusted: é a emenda de 2026-10-19 ao ADR-034 («depois de ver algo de
  fora, o agente pode ler à vontade; só não pode enviar nem mudar nada»), que hoje vive só na
  classificação estrutural e passa a estar **na política assinada**, onde quem assina a vê.
- **`allow_fs_read_external`** — `cap:fs.read` com `context.destination == "external_read"`.
  Permitida com contexto limpo, e com contexto untrusted **só** quando a proveniência é
  `handle_argument`. É a regra de fluxo: o conteúdo viaja por handle para um fornecedor de modelo
  que não muda nada no mundo, e o SA-ROC continua a classificá-lo pela sensibilidade (ADR-013
  **não muda**).

**Consequência de governação, e é o ganho que paga a cerimónia:** cada regra nova traz a cláusula de
taint ou a forma de fluxo que o gate reconhece, pelo que a entrada `allow_fs_read` da baseline fica
**obsoleta** — e a baseline do `policy-taint` só encolhe, logo o gate exige a sua remoção. Com ela
sai a última entrada: a baseline fica vazia. **Isto fecha o critério 6 do AOS-363**, adiado desde
2026-09-26 pelo custo de re-assinar o bundle (ADR-034 §2.6), na mesma assinatura e sem cerimónia
adicional. O critério 2 do AOS-556 — «a baseline não cresce» — cumpre-se por excesso: ela desaparece.

### 2.7 A `allow_http_post` NÃO muda: um handle untrusted nunca flui para um efeito

A regra mantém `context.taint != "untrusted"` como conjunção de topo. Nenhuma proveniência a
satisfaz: `handle_argument` é untrusted, logo a cláusula falha. É a assimetria que o critério 1 pede,
e fica imposta pela regra mais simples que existe — a ausência de excepção.

O resíduo **R8 do ADR-034** (a região do `allow_http_post` ser hoje a barreira acidental de um
`web_post` de contexto limpo) **não é resolvido aqui** e continua decisão da cerimónia.

### 2.8 O gate `policy-taint` aprende a forma, e a excepção é por classe de destino fechada

Um `permit` sem a cláusula de taint como conjunção de topo continua a ser violação. Acrescentam-se
**duas** formas aceitáveis, e só estas:

- `@taint_exempt("local_read")` numa regra cuja **cabeça** nomeia uma capability do conjunto fechado
  de leitura local — o molde exacto do `local_run_write` do AOS-549, generalizado de uma classe para
  duas;
- `@flow_from_handle("external_read")` numa regra cuja cabeça nomeia uma capability do conjunto
  fechado de leitura com egress externo, **e** cujo `when {}` tenha a forma fixa
  `taint != "untrusted" || provenance == "handle_argument"` — a disjunção é aceita **só** nesta
  forma literal e nesta anotação, nunca numa cabeça livre, nunca com `unless`, nunca com a cláusula
  negada.

A sintaxe da regra desenha-se **com** o gate, e não contra ele: a forma acima é a que o parser de
conjunções de topo já sabe recusar, tornada aceitável por uma anotação visível a quem assina.

### 2.9 A 1.8.0 pede-se pelas DUAS vias: interruptor do nó e campo do pedido

**Decisão do dono, 2026-10-25.** O precedente do repositório é duplo e contraditório — o
`AOS_MEDIA_NO_TURNO` põe **todos** os runs novos no layout novo, e o `requires_first_turn_trusted` é
um **pedido** do submissor que o nó recusa com 400 quando o seu estado composto não o garante. A
1.8.0 leva as duas, e cada uma tem um papel distinto:

- **o interruptor do nó HABILITA**, e é o recuo. Desligado por omissão (critério 9 do AOS-556) e
  até o bundle assinado estar em vigor. Um operador desliga-o e todos os runs novos voltam ao layout
  anterior, sem release;
- **o campo do pedido PEDE**, por run. Validado contra o estado composto do nó e recusado com **400**
  quando o nó não o habilita — nunca aceito e silenciosamente ignorado, que é o modo de falha que o
  molde do `requires_first_turn_trusted` existe para evitar. É um **booleano**, e não uma versão de
  layout: a porta não oferece a escolha de um layout arbitrário, que seria uma superfície muito
  maior pelo mesmo ganho.

**HABILITAR NÃO É MOVER, e é isso que torna as duas vias úteis em vez de redundantes.** Nenhum run
muda de layout por a env estar `on`: sem o campo, o layout é o de sempre. Se a env sozinha movesse
todos os runs novos — o molde da `AOS_MEDIA_NO_TURNO` —, o campo não servia para nada. O campo serve
para o plano pedir a 1.8.0 **nó a nó**, que é o que o caso de uso precisa (o nó 2 do plano é que
consome o que o nó 1 leu). Um run **retomado** não passa por nenhuma das duas: continua no layout do
seu registo de retoma, como desde o AOS-489.

**A habilitação efectiva exige também o PRODUTOR do `tool_result`** (a execução durável com ledger
composto). Com a env `on` e sem ele, o layout seria alcançável mas inútil: o tail materializa um
erro de tool em vez do conteúdo e o piso do run cai à primeira call. O nó **corre e não habilita** —
não aborta, ao contrário do `AOS_TAINT_ESCALA` sem aprovadores, que deixaria runs suspensos para
sempre —, recusa o pedido com 400, e o banner diz qual das duas metades falta.

E a linha de postura declara o estado das duas no arranque, porque neste repositório postura
anunciada é postura ligada.

---

## 3. Alternativas consideradas

- **(A) Isenção por capability, sem destino no motor.** Uma regra que permitisse `cap:fs.read` com
  untrusted, anotada como excepção. **Rejeitada por F2 e pelo controlo negativo:** a capability não
  distingue a `doc_read` da `ocr_read`, pelo que a isenção abriria as duas; e ignora o *porquê* do
  untrusted, pelo que um run com media passaria — o controlo negativo do dono deixava de medir.
- **(B) Um eixo novo declarado no manifesto** («esta tool é um gateway de modelo»). **Rejeitada:** a
  classe é derivável dos três eixos que o manifesto já exige explícitos, e um eixo novo é uma
  superfície nova para o operador errar, com o mesmo poder.
- **(C) `resource.value` como destino.** Já viaja até ao `Input`. **Rejeitada:** não é atributo do
  motor, e sobretudo **vazio acontece** — o `RiskGate` nega fail-closed «danger com egress sem
  destino concreto resolvido», o que prova que acontece. Uma regra que condicionasse nele teria de
  tratar o vazio, e tratá-lo mal é fail-open.
- **(D) Promover o handle a trusted quando a política o autoriza.** **Rejeitada:** o ADR-043 §2.3
  fica («todo o artefacto é untrusted, seja qual for o produtor»). Uma política de fluxo decide
  **sobre** untrusted e não precisa de promover nada; promover perdia o rótulo para o classificador
  de risco e para a auditoria, que é o inverso de «um só rótulo para todos os leitores» (ADR-034
  §2.3).
- **(E) Deixar a decisão no TaintGate, sem tocar no Cedar.** É onde a barreira hoje está (F2), e
  seria a mudança mais pequena. **Rejeitada:** o critério pede a política em **Cedar assinado**, e
  por boa razão — a decisão de que conteúdo pode sair para onde é governação do dono, versionada e
  assinada, não uma classificação em código que muda com um deploy. **Mas não é uma alternativa: é a
  OUTRA METADE.** A primeira versão desta secção dizia «o TaintGate continua a impor a invariante
  estrutural» sem notar que a invariante estrutural é exactamente o que **bloqueia** a regra nova —
  medido em `TestAOS556_Bateria_ACadeiaRealDecideOQue`. As duas camadas têm de aprender a mesma
  coordenada, e isso é o **resíduo 1 (§5)**: uma decisão do dono, não um ticket.

---

## 4. Consequências

- **O que esta decisão, SOZINHA, faz correr: nada.** E a primeira versão deste ADR afirmava o
  contrário, o que é o defeito mais grave que a revisão adversarial desta fatia encontrou. A
  cadeia do ápice é `… risk-classify → POLÍTICA → TAINT → …` e pára no primeiro não-permit: a
  política passa a PERMITIR a leitura com egress externo sobre um handle untrusted, e o
  **TaintGate nega-a a seguir**, porque a `ocr_read` declara `egress: external`, logo
  `DeclaresEffect` é verdadeiro, logo é privilegiada com o `AOS_PRIVILEGED_CAPS` armado em
  produção. **Medido**, não deduzido: `TestAOS556_Bateria_ACadeiaRealDecideOQue` compõe os dois
  na ordem real e regista «politica=permit · taint=deny».
  **O ganho de produto da fase R2 exige a decisão do resíduo 1 (§5).**
- **O que continua negado, e por desenho:** qualquer efeito (`external_effect`) pedido de um contexto
  untrusted, qualquer que seja a proveniência. E **tudo** num run com media, porque aí a proveniência
  é `context`.
- **O resíduo 3 do ADR-034 dissolve-se** («um nó que já leu um documento não lê outro»): com a
  autoridade por argumento e a `allow_fs_read_local` na política assinada, a segunda leitura tem
  argumentos confiáveis e passa.
- **A baseline do `policy-taint` fica vazia**, e o critério 6 do AOS-363 fecha (§2.6).
- **O bundle sobe a 1.2.0** e a âncora pode rodar. Se rodar, rodam com ela o `trust_anchor.pub`, os
  seis literais da âncora em `packages/integration` e o `AOS_POLICY_TRUST_ANCHOR` do `.env` do
  servidor. O bundle é config de runtime (`AOS_POLICY_BUNDLE_DIR`), pelo que a política entra em
  vigor **sem release** — assinar, sincronizar a pasta, reiniciar.
- **`TestPolicyRuleCoverage` exige par allow+deny por regra:** as duas regras novas entram na matriz
  de cobertura, senão o gate `policy-test` avermelha.
- **O interruptor da 1.8.0 continua desligado por omissão** até o bundle assinado estar em vigor. Um
  nó que o ligue sem a política nova decide como hoje — nega a mais, que é seguro e não compra nada.

---

## 5. Resíduos declarados

1. **BLOQUEANTE — a barreira ESTRUTURAL nega o que a política passa a permitir, e fechá-la é
   uma decisão do dono.** Achado da revisão adversarial desta fatia, e medido em
   `TestAOS556_Bateria_ACadeiaRealDecideOQue`: com a 1.2.0 em vigor e o
   `AOS_PRIVILEGED_CAPS` armado como em produção, a política PERMITE a leitura com egress
   externo sobre um handle untrusted e o **TaintGate nega-a a seguir** — a `ocr_read` declara
   `egress: external` e por isso é privilegiada. A cadeia pára no primeiro não-permit, pelo
   que o permit do Cedar é irrelevante.

   **Consequência honesta: assinar a 1.2.0 como está esvazia a baseline do `policy-taint`,
   aperta `cap:fs.read` para quem não sela o destino (resíduo 7), e NÃO entrega o critério 1
   nem a prova em produção.** A cerimónia não deve acontecer antes desta decisão.

   **A mudança mínima que entrega o ganho** é dar ao gate a mesma coordenada que a política
   aprendeu, no único sítio que já consulta o registo de destinos: uma terceira isenção em
   `DestinationPrivileged.ClassifyCall`, para `Class() == external_read` **e**
   `TaintProvenance == handle_argument`.

   **O que isso custa, e é por isso que é do dono:** RELAXA a invariante **P0 do ADR-005** —
   «só dados trusted podem ORIGINAR uma acção privilegiada» — para uma classe de destino. O
   argumento a favor é o da CaMeL, e é o mesmo que sustenta toda a fase: o modelo que
   ESCOLHEU a call só viu trusted e handles opacos, pelo que a injecção não escolheu a acção
   — apenas viaja —, e o destino não muda nada no mundo. O argumento contra é que enviar É
   agir, e foi assim que a emenda de 2026-10-19 ao ADR-034 o escreveu. Exige emenda declarada
   ao ADR-005 e ao ADR-034, e torna o resíduo 2 **bloqueante**: a partir daí uma decisão de
   segurança depende da derivação da proveniência, que tem de ter guarda própria.

2. **Fluxo de controlo dependente de dados.** O modelo continua a escolher **qual** handle passa a
   que tool: vê a ordem, a contagem e o tipo. Uma injecção no documento A pode, indirectamente,
   levá-lo a passar o documento B a um destino permitido. Classe declarada aberta pela CaMeL, e esta
   decisão não a fecha — o que a limita é o destino: `external_read` não muda nada no mundo.
2. **Um run com media não ganha nada** (§2.3). É o preço da decisão (c) do dono.
3. **R7 do ADR-034 permanece aberto:** texto colado no `objective` comanda no turno 1.
4. **R8 do ADR-034 (a região do `allow_http_post`) continua decisão da cerimónia** (§2.7).
5. **O canal das mensagens de erro das tools** continua a ser a única porta por onde texto de fora
   volta ao segmento que a R2 converte. O vocabulário fechado de erro das tools registadas limita-o,
   não o fecha.
6. **A classe `external_read` assume que «não muda nada no mundo» vale para o fornecedor.** O que o
   fornecedor faz com os dados é governado pela política de elegibilidade e pelo ZDR (ADR-043 §2.7),
   não por esta decisão. Enviar continua a ser enviar: o SA-ROC classifica-o pela sensibilidade e o
   egress continua default-deny com allowlist.

---

## 6. Conformidade / enforcement

| Invariante | Onde é imposta |
|---|---|
| A proveniência é derivada pelo kernel e nunca auto-declarada | reescrita sempre, no sítio da re-junção; guard-test de reflexão sobre o `Input` |
| Os dois campos novos atravessam as duas vias de despacho | escritos antes do despacho; guard-test no molde do AOS-454 |
| Uma regra com atributo fora do mapa não entra em vigor | `attrRefsForaDoMapa` + `SmokeDecideRules`, corridos pelo `policy-sign` antes de declarar verde |
| A excepção de fluxo não se estende a outra capability nem a outra forma | `policy-taint` por cabeça exacta e forma literal; self-tests por `AOS_POLICY_TAINT_POLICY` |
| A baseline do `policy-taint` só encolhe | o gate falha com entrada obsoleta |
| Cada regra tem par allow+deny | `TestPolicyRuleCoverage` |
| Um handle untrusted nunca flui para um efeito | `allow_http_post` mantém a cláusula; bateria de injecção do AOS-556 |
| Com media, nada se levanta | `context` domina na proveniência; controlo negativo da bateria |

---

## 7. O que é do dono

1. **Ratificar este ADR.**
2. **Assinar o bundle 1.2.0** (`go run ./cmd/policy-sign -version 1.2.0`, com a chave da política).
   **Atenção:** sem a chave em `~/.aos/keys/signing.key` a ferramenta **gera um par novo** e roda a
   âncora — e a rotação parte seis testes de integração com a âncora literal, além do `.env` do
   servidor.
3. **Decidir, na mesma cerimónia:** o R8 do ADR-034 (a região do `allow_http_post`) e se
   `cap:http.post` fica sempre atrás de confirmação humana.
4. **A prova em produção:** um plano **sem media** em que o nó 2 usa uma tool de leitura externa
   sobre o que o nó 1 leu, sem aprovação humana por chamada — **mais o controlo negativo**, o mesmo
   plano com media, que tem de continuar negado.

---

## 8. Referências

- `docs/adr/ADR-034-autorizacao-derivada-do-contexto.md` — §3 (opção A), §5.4 (o gatilho), §2.6 e R8
- `docs/adr/ADR-043-harness-por-capacidades.md` — §2.6 (a opção A como pré-condição), §2.3
- `docs/adr/ADR-038-a-saida-de-um-no-de-passagem-directa-e-o-resultado-da-tool.md` — §3, que exigiu
  ADR próprio ao rejeitar «o nó consumidor resolve a referência»
- `docs/adr/ADR-011-policy-as-code-gdpr.md` — a assinatura do bundle e o que ela cobre
- `docs/adr/ADR-013-*` — SA-ROC, que **não muda**
- `docs/reports/desenho-r2-entradas-por-handle-2026-10-23.md` — §4.2 (a decisão (4) do dono), §6
- `specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md` — `## AOS-556`
- `docs/governance/REGISTO-Deferimentos.md` — DEF-806, que fecha com a política em vigor
