# Desenho R2 — entradas por handle e taint do artefacto (ADR-043 §2.6), 2026-10-23

> Fase R2 do roteiro do ADR-043 §7, a concretização da **opção A do ADR-034** (dual-LLM/CaMeL) e a
> forma forte do ADR-005, que o catálogo nunca materializou.
>
> **v2 (2026-10-23).** A v1 foi sujeita a duas revisões adversariais independentes — factos e
> proposta —, no molde do que o ADR-043 teve. **Seis bloqueantes e catorze achados menores; quatro
> bloqueantes confirmados por leitura própria do código.** A v1 assentava em duas premissas falsas e
> prometia um ganho que a sua própria tabela negava. A **§10 registra o que caiu**. Esta versão
> existe porque a v1 não devia ser implementada.
>
> **Decisões do dono de 2026-10-23:** (1) o handle é **híbrido** — tipo opaco ao modelo, suporte
> durável por baixo; (2) o modelo que decide vê **só factos de nível-esquema**, nunca um resumo do
> conteúdo; (3) **a autoridade conta a media**, não só os handles; (4) a decisão passa por uma
> **política de fluxo de dados** assinada, com cerimónia.

## 1. O que o dono ganha, e o que NÃO ganha

**Ganha:** ler deixa de contaminar as chamadas que **não consomem** o que foi lido; e, com a política
de fluxo de dados (§4.2), um efeito **pode** ser parametrizado por dados untrusted quando a política
assinada o permite para aquele destino. É isso que torna «gerar a partir do que se viu» legítimo sem
uma pessoa a aprovar cada chamada, e é isso que faz a `model_invoke` da R3 nascer legal em vez de
nascer negada.

Hoje esse caminho está fechado: um plano em que um passo usa uma tool protegida **com base no que
outro passo leu** falha sempre — limite de produto registado em
`docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md:177-179`.

**NÃO ganha** — e a v1 dizia que sim:

- **Não poupa turnos.** Os layouts são cumulativos (`layout.go:113-131`) e a 1.7.0 **exige** a 1.6.0
  (`ErrMediaNoTurnoSemPedirAntesDeLer`, `cmd/aos/media_turno.go:70`, imposto em `:107-113`). A 1.8.0
  deriva da 1.7.0 (§5) e herda o adiamento do AOS-540. O anúncio do AOS-542 e a regra P4 do
  `aos-orq` ficam **como estão** — que é o lado bom de herdar.
- **Não levanta nada num run com media** (decisão 3, §4.1). O ganho é para runs sem segmentos de
  media projectados.

## 2. O estado de partida (medido a 2026-10-23)

**Os primitivos da opção A já estão escritos e testados, e não têm chamador de produção.**
`packages/kernel/agent-runtime/taint_plane.go`: `PlaneSegment` (`:21`), `Handle` (`:64`),
`Quarantine` (`:78`), `PlannerView` (`:125`), `SeparatePlanes` (`:139`), `ControlPlanner` (`:176`).
O próprio ficheiro o declara (`:172-175`); só dois comentários o referenciam — `loop.go:772` e
`prompt.go:622`, ambos a apontar ao DEF-806. A R2 é **cablagem**, não invenção.

| Peça | Onde está hoje |
|---|---|
| A regra do rótulo | `context_authority.go:56-65` `SegmentAuthority`: trusted só para `objective`, `correction` e `notice`; `producedUnder` para `history` e `tool_call`; o resto untrusted (`default`, fail-closed). `ContextAuthority` (`:70-76`) é dobra monótona |
| Onde o conteúdo entra inline | `plan_input` leva o digest no rótulo e **o conteúdo no corpo** (`prompt.go:521-527`); o corpo de um `tool_result` leva o valor cru, e à frente dele `tool_error=<msg>` quando a tool falhou (`prompt.go:495-502`, decisão documentada em `:482-494`) |
| **Onde os bytes chegam ao modelo que decide** | `platform/model-gateway/projection_media.go:91-111` `anexarMedia` resolve cada `MediaRef` e faz `m.AttachMedia(parte)`. **É a funcionalidade da R1a**, declarada em `cmd/aos/media_turno.go:16-19`. Os bytes não entram no *tail*; entram na *mensagem* |
| Onde a forma final do efeito se fixa | `loop.go:1207-1216`: a reescrita da `Call` corre **na construção, não no despacho**, porque «a reescrita é o que DEFINE o efeito» — e o comentário registra o defeito **observado ao vivo** quando corria no dispatcher: previews divergentes e aprovação humana que nunca casava com a acção |
| Tectos que existem | argumentos 4 KiB + `args_digest` no rótulo (`layout.go:344-367`, `:247`); rótulos 256 B; **`plan_input` 128 KiB por payload, 512 KiB agregados, 16 payloads** (`cmd/aos/api.go:818-826`, impostos em `:858-864`); objectivo 16 KiB (`plan_ingress.go:111`) |
| O armazém (R1b) | chave `<dir>/<sha256(run_id)>/<sha256(conteúdo)>` (`artifact_store.go:191`, `:197`), cifra por titular pelo `contentSealer` (`cmd/aos/dsar.go:52-60`), evento `artifact.created` (constante em `artifact_store.go:60`, corpo em `:284`), tectos 1 MiB por artefacto e **16 artefactos / 16 MiB por run** (`:77-79`), vocabulário de tipos **fechado** (`:85-91`) com `validarConteudo` a recusar binário e UTF-8 inválido (`:419-434`) |
| O que não existe | `artifact_read`; nenhum esquema de referência é **parseado** como entrada; o `artefactoCriado` não tem campo de linhagem; os contadores do armazém não são publicados em `/metrics`; **o layout 1.8.0** (o topo é `AssemblyVersion170`, `layout.go:63`) |

## 3. O handle

| | Decidido |
|---|---|
| O que o modelo vê | o `Handle` **opaco** mais metadados que **o nó cunha**: tipo de media, tamanho, e um nome **do nó** (tool + índice, no molde do `ToolStepID`). Nunca conteúdo, nunca resumo, e **nunca o nome do produtor** (§4.3) |
| Suporte | durável, cifrado por titular pelo **mesmo `contentSealer`** das capturas, com proveniência selada e alcançável pela DSAR e pela retenção. **Num espaço próprio de handles, não pela via da `artifact_write`** (§3.1) |
| Determinismo | o token é cunhado pelo nó, escopado ao run e **não adivinhável**, e a sua sequência é função da ordem dos segmentos. Sem relógio e sem `rand`: o replay por dobra pura exige-o (`replay/engine.go:599-601`) |
| **Quando se resolve** | **no `CallRewriter` (`loop.go:1227-1244`), ANTES da mediação** (§3.2). A v1 dizia «a jusante do RM» e estava errada |
| Token que não resolve | **fail-closed e a call é recusada.** Nunca se passa o literal adiante, e nunca se trata um token com forma de handle como argumento trusted |
| Handle forjado | (a) só uma referência produzida por uma tool de código do nó é reconhecida — a regra de `media.go:26-32`; (b) o par `(run, digest)` consta de um evento do próprio stream. **Nenhuma das duas decide o taint** (§4.2) |
| Entre runs | só pela declaração selada do plano (ADR-038), nunca por um digest que o modelo forneça. É a resposta ao *confused deputy* que o ADR-038 §3 nomeou |

### 3.1 Porque não se reutiliza a via da `artifact_write`

O guarda (b) obriga **todo** o `plan_input` e **todo** o `tool_result` a ter evento. Encaminhá-los
pela `artifact_write` da R1b não funciona, e a revisão mediu porquê:

- os tectos são 16 artefactos e 16 MiB **por run** (`artifact_store.go:77-79`) e o orçamento é
  **partilhado** com o que o modelo grava: um run que leu 14 coisas só podia gravar 2 ficheiros;
- o vocabulário de tipos é **fechado** e `validarConteudo` recusa binário e UTF-8 inválido
  (`:85-91`, `:419-434`): um resultado de tool binário não se guardaria, logo não haveria handle,
  logo o run falhava;
- com o mesmo NHI emissor (`artifactNHI`, `:67`), **todo** o `tool_result` passaria a aparecer em
  `GET /runs/{id}/artifacts` e a ser descarregável com `read:artifact` (`artifact_api.go:40-54`) —
  conteúdo que hoje só a via de trajectória expõe mudava de capability de leitura, sem ninguém o
  declarar;
- a escrita do runtime **não passa pelo RM**, pelo que a regra assinada `allow_artifact_write`
  deixaria de governar todas as escritas que ela governa hoje.

Logo: **espaço próprio**, com os seus tectos, vocabulário aberto e superfície de leitura própria,
reutilizando a *maquinaria* de selagem (o `contentSealer`) e não o *caminho* da tool. A decisão (1)
do dono — tipo opaco com suporte durável — mantém-se; o que muda é por onde.

Selagem com **dados associados** `"handle:" + runID`. O `abrir` da R1b reconfere o digest
(`artifact_store.go:277-279`), o que amarra o cifrado ao conteúdo mas **não ao run**: um blob selado
sob o mesmo titular, copiado para a pasta de outro run, abre e passa. A correcção está já
especificada no resíduo 9 do ADR-034 (`:443-452`).

### 3.2 A resolução é antes da mediação, não depois

`loop.go:1207-1216` é o registo de um defeito **observado ao vivo** em produção: com a reescrita no
dispatcher, «o loop descrevia ao mundo o efeito PRÉ-reescrita e o RM mediava o PÓS-reescrita: as
duas previews divergiam e a aprovação humana, embora emitida e consumida, nunca casava com a acção».
A troca handle→bytes **é** uma reescrita do efeito. A jusante do RM, repunha-o:

- a pessoa do AOS-541 aprovaria `{"body":"h3"}` — a `ApprovalPreview` leva `sha256(c.Input)`
  (`reference-monitor/approval_gate.go:55-65`) — e o nó publicaria os bytes;
- o PDP decidiria e o WORM selaria `doc://h3`, quando `resolveResourceValue` existe precisamente
  «para que o PDP decida — e o WORM sele — sobre o que a execução vai mesmo tocar»
  (`cmd/aos/modeltools.go:519-521`);
- o SAROC-04 passaria sem fazer o seu trabalho: só recusa um `Resource.Value` **vazio**
  (`risk_gate.go:382-385`), e `doc://h3` não é vazio nem diz nada;
- `AttrToolCallHash = toolCallHash(ToolID, Input)` (`monitor.go:269-271`) colidiria entre runs que
  publicassem cada um o seu `h1`.

Com a resolução no `CallRewriter`, a autoridade por argumento calcula-se **na mesma `Call` que o RM
vê** — e isso é o que mantém a paridade das duas vias (§4.4).

### 3.3 Os metadados são de esquema e deterministas

A v2 escreveu «sem validade» a pensar num relógio. A primeira passagem do AOS-550 mostrou que a
palavra era ambígua e que o critério, como estava escrito, proibia algo legítimo. Precisa-se:

- **permitido** — um facto de **forma**, calculado dos bytes: `media_type`, `bytes`, e um
  `well_formed` que diz que os bytes parseiam para o tipo declarado (JSON válido, SVG que passou as
  regras). Diz que a **forma** está certa, nunca o que o conteúdo diz, e é função pura dos bytes;
- **proibido** — qualquer facto que dependa de um relógio ou de estado de fora da sequência: uma
  expiração, um instante, uma contagem global. O prompt re-materializa-se no replay e o
  `prompt_hash` compara-se com o gravado (`replay/engine.go:599-600`): um valor de parede diverge em
  todos os turnos.

### 3.4 O tecto do corpo de um `tool_result`

Medido na primeira passagem do AOS-550 e coerente com as três varreduras: **não existe tecto
nenhum**. Há `MaxToolCallArgBytes` 4 KiB e `MaxToolCallLabelBytes` 256 B (`layout.go`),
`maxObjetivoBytes` 16 KiB (`plan_ingress.go:111`), e `tailResultado` faz
`content = append(content, r.Value...)` sem limite (`prompt.go:495-502`) — o `dec.Output` entra
inteiro, e também inteiro na captura do turno.

**256 KiB**, terceiro degrau de uma escada de potências de quatro: 4 KiB (argumentos) → 16 KiB
(objectivo) → **256 KiB (resultado)** → 1 MiB (artefacto). Deixa 4× de margem para o envelope da
cifra e para a expansão de um corpo que viaje como string JSON dentro dos argumentos de uma tool.

**Onde se impõe, e porquê não no tail.** O tecto rege o **caminho por handle** — à selagem e à
resolução —, com recusa nomeada. **Não** se impõe ao corpo que vai para o tail, porque o critério
«desligado por omissão, byte a byte» proíbe-o: aplicá-lo ao tail mudaria o prompt de qualquer run
cuja tool devolva hoje mais de 256 KiB.

A tensão dissolve-se no AOS-551 e **não** precisa de decisão própria: em 1.8.0 o `tool_result` passa
a viajar por handle, logo o corpo do tail é rótulo e digest e o conteúdo vai para a quarentena, onde
o tecto vive. **Resíduo declarado:** em 1.7.0 e abaixo o corpo do tail continua sem tecto, para
sempre. É comportamento preservado, não defeito desta fase.

## 4. A autoridade

### 4.1 Conta a media, não só os handles

A v1 dizia que o modelo que decide deixa de ver conteúdo untrusted. **É falso no layout em que a R2
vive:** a R1a projecta a imagem para o modelo do run (§2). Se a `PlannerView` tornasse o contexto
trusted por construção, o **texto do próprio modelo** passaria a segmento trusted
(`SegmentAuthority(TailHistory, producedUnder=trusted)`, `context_authority.go:56-65`) e uma injecção
visual lavava-se para trusted — caminho que **hoje morre**, porque o `tool_result` da `media_view`
suja o contexto.

**Decisão (3) do dono:** a autoridade conta **tudo o que pode ter influenciado o modelo**.

```
autoridade(call) = join( trusted,
                         taint de cada handle resolvido nos argumentos desta call,
                         taint de cada segmento de media projectado neste run )
```

Consequência honesta: **num run com media, a R2 não levanta nada.** O ganho é para runs sem media. A
R1a fica intacta e o caminho novo não se abre.

### 4.2 A política de fluxo de dados

**Decisão (4) do dono.** Uma call que consome um handle untrusted **não é automaticamente negada**: a
política assinada decide por **(capability, destino, proveniência do que entra)**. É o mecanismo real
da CaMeL, e é o que a v1 não tinha — razão por que a sua própria tabela («decide-se como hoje»)
negava o ganho que o §1 prometia.

A forma: um handle untrusted **pode** fluir para o gateway de modelo (os dados já vão para esse
fornecedor, e o SA-ROC classifica-o pela sensibilidade — ADR-013, que **não muda**) e **não** pode
fluir para o `web_post`.

Isto obriga a regras Cedar novas, logo a **re-assinatura do bundle e cerimónia do dono** (§6). A v1
dizia que não era preciso; com esta decisão, é. Hoje só uma regra lê o taint — `allow_http_post`
(`pdp/policies/aos_authz.cedar:33`); o `allow_fs_read` não a tem e o `allow_artifact_write` está
anotada `@taint_exempt("local_run_write")`.

**O ADR-043 §2.3 fica.** «Todo o artefacto é untrusted, seja qual for o produtor»: uma política de
fluxo não precisa de promover nada — decide **sobre** untrusted. O AOS-552 pede a linhagem e o
guard-test «o artefacto é sempre untrusted» no mesmo ticket, e as duas coisas convivem **se a
linhagem for proveniência registada e não desclassificação**. O ticket di-lo explicitamente.

### 4.3 O nome, e os canais que ficam abertos

O nome de um artefacto **vem do modelo**, não do nó: `pedidoDoModelo.Name` → `entradaDoArtefacto.Name`
→ `artefactoCriado.Name` (`artifact_store.go:331-335`, `:358-359`, `:397-398`), validado só por
`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$` (`:116`) — alfabeto que é subconjunto do dos rótulos do tail
(`prompt.go:677-686`) e sobrevive à sanitização **byte a byte**. O ADR-034 já o aceita por escrito na
emenda do AOS-549 (`:322-324`). São até 64 bytes × 16 artefactos de texto escolhido por um atacante
dentro do contexto que a v1 prometia limpo.

**Logo: o nome que o planeador vê é cunhado pelo nó** (tool + índice). O nome legível do dono
pertence à listagem HTTP (`artifact_api.go:88-91`), não à vista do planeador.

Canais que ficam **abertos e declarados**, não fechados:

- **Fluxo de controlo dependente de dados.** O planeador vê a ordem, a contagem e o tipo dos handles,
  e escolhe qual passa a que tool. É a classe que a CaMeL declara.
- **Texto livre no corpo de um `tool_result`.** `tailResultado` põe `tool_error=` + a mensagem de erro
  no corpo, «e é uma decisão, não um esquecimento» (`prompt.go:470-504`). Uma tool cuja mensagem de
  erro cite a entrada ofensiva repõe o canal inline no único segmento que a R2 converte. O AOS-550
  fecha o vocabulário de erro das tools registadas, no molde do que a `artifact_write` já faz
  (`artifact_store.go:93-104`).
- **R7 do ADR-034:** conteúdo colado no `objective` continua a comandar no turno 1
  (`api.go:1178-1188`, `TestAOS069_InjeccaoNoObjectivoPassaAClausulaDeTaint_R7`).

### 4.4 Invariantes a preservar

- **Um só rótulo para todos os leitores** (ADR-034 §2.3): o classificador de risco lê o mesmo valor
  (`risk_gate.go:373`, `:511`) e untrusted continua a elevar a sensibilidade um nível
  (`risk/classifier.go:186-191`).
- **Paridade das duas vias** (ADR-034 §2.8): `Activity.AuthorizationTaint`
  (`activity/contract.go:184`) e `DurableDispatcher` (`integration/runtime_ports.go:240`). A resolução
  no `CallRewriter` (§3.2) é o que mantém isto verdadeiro: o `Dispatcher` em `ModeReplay` devolve o
  output registado **sem efeito e sem mediação**, e em dedup devolve do step-ledger sem reexecutar
  (`activity/contract.go:195-207`) — uma call deduplicada nunca resolveria nada.
- **O replay passa a precisar de autoridade POR CALL.** `ReplayedTurn.Authority` é **uma** string por
  turno (`replay/engine.go:611-621`) e `authorityDivergence` exige que todos os taints selados do
  turno sejam iguais a ela (`:841-848`). Em 1.8.0 um turno com uma call que consome handle e outra que
  não sela dois valores: o detector dispara `Reason="authority"` no primeiro turno misto. Tem de
  recalcular por call, chaveado pelo `ToolStepID`, e comparar listas.
- **A âncora da designação de origem muda.** `designar` exige que o primeiro turno com tools tenha
  contexto trusted, «o que garante que os argumentos da chamada foram escolhidos por um modelo que só
  tinha visto o prefixo e o objectivo» (`output_source.go:233-276`, em especial `:242-245`). Em 1.8.0
  isso passa a ser verdade em mais turnos, pelo que `OutputSourceInapplicable` fica quase inalcançável
  e runs que hoje fecham `complete` com `inapplicable` passariam a falhar `output_source_missing` com
  `binding` + `enforce` (`completion.go:487-498`). O AOS-551 define a âncora nova — provavelmente «sem
  handles nos argumentos da call designada» — e declara a mudança de veredicto.

## 5. As peças, por ticket

**A 1.8.0 deriva da 1.7.0** e herda o adiamento e a media. É o que a cadeia cumulativa permite
(`layout.go:113-131`) e o que a decisão (3) torna seguro; e deixa o anúncio do AOS-542 e a regra P4
do `aos-orq` intactos.

| Ticket | Conteúdo | Epic | Depende de |
|---|---|---|---|
| **AOS-550** | O handle: espaço próprio com tectos e vocabulário próprios (§3.1), token cunhado pelo nó e não adivinhável, metadados de nível-esquema **deterministas** (§3.3), selagem com dados associados do run, **resolução no `CallRewriter`** (§3.2), fail-closed no token que não resolve, vocabulário de erro das tools fechado, o tecto do corpo de um `tool_result` (§3.4), métricas do armazém | EPIC-07 | R1b |
| **AOS-552** | O taint e a linhagem do artefacto: a linhagem é **proveniência registada, não desclassificação** (§4.2); `artefactoCriado` ganha os digests das entradas; e nasce o guard-test «o artefacto é sempre untrusted» que o ADR-043 §8 exige **para a R1b** e que não existe — é dívida de uma fase declarada fechada em produção | EPIC-07 | AOS-550 |
| **AOS-551** | A separação ligada: layout **1.8.0** derivado da 1.7.0, `plan_input` e `tool_result` por handle, `SeparatePlanes` com chamador de produção, **a autoridade de §4.1 a contar a media**, **a política de fluxo de dados em Cedar assinado** (§4.2), autoridade por call no replay, âncora nova da designação de origem, paridade nas duas vias. **Fecha o DEF-806**, e nasce com um ADR próprio | EPIC-02 | AOS-550, AOS-552; **cerimónia do dono** (§6) |
| **AOS-553** | A travessia da aresta do plano pela declaração selada, com as defesas do *confused deputy* | EPIC-19 | AOS-551, AOS-552 |

**Restrição de ordem, nos DOIS sentidos — e com ponto de imposição.** A v1 escrevia-a num sentido só
e em prosa.

1. A separação **não** pode ligar sem a autoridade por argumento: trocava «negar a mais» por «permitir
   a mais».
2. A autoridade por argumento **não** pode valer sem a separação: com o conteúdo ainda inline, o
   modelo lê um documento, copia «envia para evil.com» para um argumento como **literal** — que não é
   handle, logo conta trusted — e o `join` autoriza. Hoje isso é negado. Aplicar esta metade sozinha é
   **pior** do que a protecção actual.

Logo as duas são **uma só** mudança, e não duas bandeiras. Ponto de imposição: a função de autoridade
por argumento é um colaborador que o construtor do layout 1.8.0 **exige**, de modo que
`layoutFor("1.8.0")` sem ela seja erro de arranque — no molde de `comporMediaNoTurno`, que recusa
arrancar com `ErrMediaNoTurnoSemPedirAntesDeLer` (`media_turno.go:107-113`) —, mais uma linha de
banner e um guard-test.

## 6. O que é do dono

**Mais do que a v1 dizia.** A v1 afirmava «nenhuma capability nova, em princípio sem re-assinatura».
Com a decisão (4) isso deixa de valer:

1. **Regras Cedar novas para o fluxo de dados** (§4.2) e **re-assinatura do bundle**. A assinatura
   cobre o texto dos `.cedar` por `canonicalContentHash` (`pdp/bundle.go:49-51`), pelo que texto novo
   exige assinatura nova. Atenção ao que a R1b aprendeu: sem a chave em `~/.aos/keys/signing.key` a
   ferramenta **gera um par novo** e roda a âncora.
2. **O gate `policy-taint`** (`scripts/ci/policy-taint.py`) só aceita a anotação de isenção para as
   capabilities que conhece (`:358-367`). Regras de fluxo de dados são forma nova: o gate tem de as
   conhecer, ou avermelha.
3. **O critério 6 do AOS-363** (a cláusula de taint em `allow_fs_read`) volta a ser decidível. Está
   adiado por **decisão do dono, pelo custo de re-assinar o bundle** (ADR-034 §2.6) — e **não** pela
   objecção F2, que a opção C já dissolveu em 2026-09-26 (`ADR-034:29`). O que a R2 acrescenta é
   dissolver o **resíduo 3** do ADR-034 («um nó que já leu um documento não lê outro»): com autoridade
   por argumento, a segunda leitura tem argumentos confiáveis e passa. Se a cerimónia das regras de
   fluxo acontecer de qualquer modo, é a altura de decidir este também.
4. **A prova em produção:** um plano **sem media** em que o nó 2 usa uma tool protegida sobre o que o
   nó 1 leu, sem aprovação humana — **mais o controlo negativo**: o mesmo plano com media continua
   negado.

## 7. Resíduos e o que fica fora

- **R7 do ADR-034 permanece aberto** (§4.3).
- **Um run com media não ganha nada** com a R2 (§4.1). É o preço da decisão (3).
- **Fluxo de controlo dependente de dados** (§4.3): o planeador escolhe qual handle passa a que tool,
  pela ordem, contagem e tipo. Classe declarada pela CaMeL, não fechada aqui.
- **Injecção visual numa imagem** não é fechada por nada neste desenho nem nos ADRs citados. O que a
  contém é a decisão (3).
- **O tecto do corpo de um `tool_result` não existe** — confirmado por três varreduras e pela revisão
  de factos: `loop.go:1296` (`Result: Untrusted(dec.Output)`), `Decision.Output` sem limite
  (`reference-monitor/decision.go:145-146`), `inlineWindow.Append` sem corte (`ports.go:100`). Mas o
  caso **genuinamente ilimitado são as tools de sandbox**: a `ocr_read` e a `video_read` já têm
  `max_tokens = 4096` e recusam uma resposta cortada (`ocr_tool.go:89-90`,
  `tool_de_modelo.go:280-286`), e a `media_view` e a `artifact_write` devolvem JSON pequeno de forma
  fixa. O AOS-550 di-lo, senão a primeira medição parece contradizer o ticket.
- O **crypto-shred torna os blobs ilegíveis, não os apaga** (`artifact_store.go:25-26`). Não agrava o
  replay: a captura de não-determinismo **já** é selada por titular pelo mesmo cifrador
  (`replay/nondeterminism_capture.go:414`).
- A decisão «sem deduplicação entre runs» (ADR-043 §2.3) foi tomada com o artefacto a ser **só
  saída**; o AOS-553 é o primeiro caso em que o mesmo conteúdo tem de ser legível por mais de um run.
- **`NÃO VERIFICADO`: onde nos argumentos se procura um handle** — só escalares de topo, como
  `resolveResourceValue` faz (`modeltools.go:564-584`), ou também aninhados. O AOS-550 tem de o fixar:
  um handle aninhado resolvido enquanto o slot do recurso vê a estrutura crua seria uma terceira
  divergência entre recurso auditado e recurso tocado.
- **`NÃO VERIFICADO`: o custo de levantar o guarda (b) ao runtime.** A via HTTP chega aos eventos por
  `portasDaLeituraDeConteudo`, que tem **o selo da leitura no WORM como pré-condição**
  (`artifact_api.go:9-12`). Se o levantamento o mantiver, cada resolução custa uma escrita no WORM no
  caminho de cada tool call.
- **Os comentários caducados** do `replay/engine.go:127-133` e o resíduo 9 do ADR-034 declaram que o
  `ParentStepID` não atravessa a via durável. **É falso desde o AOS-454** (§10). Corrigi-los é
  trabalho real e fica para ticket próprio.

## 8. As emendas que esta fase faz

| ADR | O que muda |
|---|---|
| ADR-034 | A opção A deixa de ser destino; o resíduo 3 dissolve-se; a alternativa E («taint por argumento») entra |
| ADR-043 §2.4 | O «resumo» passa a factos de nível-esquema — o tipo `PlannerView` não tem campo para um resumo |
| ADR-043 §2.3 | **Fica** («todo o artefacto é untrusted»): a política de fluxo decide **sobre** untrusted e não precisa de promover. A linhagem é proveniência, não desclassificação |
| ADR-043 §7 | O roteiro põe `artifact_read` na **R1b** (`:270`) e o EPIC-07 passou-o para a R2 (`:1627`). **Não nasce tool de leitura nenhuma:** a resolução é interna, e uma tool daria ao modelo um pedido por digest — o vector que o guarda (b) fecha |
| ADR-038 §3 | O gatilho disparou: «a reabertura do DEF-806». O mesmo parágrafo diz que merece **ADR próprio** |
| ADR-005 | Materializa-se a forma forte |
| ADR-013, Carta | **Nada** |

O ADR próprio nasce com o AOS-551; o número atribui-se na emissão, porque há sessões em paralelo a
reclamar numeração.

## 9. Ordem

AOS-550 (desligado, provado com testes e smoke) → AOS-552 (a linhagem e o guard-test em falta) →
cerimónia do dono (§6) → AOS-551 (a separação, a política de fluxo, o ADR e a 1.8.0) → prova em
produção com o controlo negativo do run com media → AOS-553 (a aresta).

## 10. O que a v1 dizia e caiu

Registado para não se repetir, no molde da §8 da análise crítica do ADR-043.

| A v1 dizia | Porque caiu |
|---|---|
| «O modelo que decide deixa de ver conteúdo untrusted» | A R1a projecta a imagem para ele (`projection_media.go:91-111`). Ler «os bytes nunca entram no *tail*» e concluir «nunca entram no contexto» foi erro meu |
| «A resolução é a jusante da decisão do RM» | Repunha um defeito **observado ao vivo** em produção (`loop.go:1207-1216`): aprovação humana, recurso auditado e selo do WORM deixavam de descrever o efeito executado |
| «Gerar a partir do que se viu deixa de exigir uma pessoa» **e** «uma call que consome um handle untrusted decide-se como hoje» | Contradição interna: a `model_invoke` consome esse handle. Sem política de fluxo de dados, a R2 comprava pouco mais do que o AOS-540 já compra por um turno |
| «Sem o custo dos dois turnos» | Os layouts são cumulativos e a 1.7.0 exige a 1.6.0: a 1.8.0 herda o adiamento |
| «Metadados que o nó afirma — … nome» | O nome vem do modelo (`artifact_store.go:331-335`), com alfabeto que sobrevive à sanitização de rótulos |
| «O `PlannerView` não tem campo por onde bytes cheguem» | `Trusted []PlaneSegment` transporta `Content []byte` (`taint_plane.go:127`, `:33`). O invariante do código é mais estreito e correcto: nunca bytes **untrusted** |
| «O armazém da R1b por baixo», pela via da `artifact_write` | Tectos partilhados, vocabulário fechado, superfície de leitura nova não declarada, escrita fora do RM (§3.1) |
| «Nenhuma capability nova, em princípio sem re-assinatura» | A política de fluxo de dados exige regras novas e cerimónia (§6) |
| «O critério 6 do AOS-363 foi adiado pela objecção F2» | A F2 descrevia o estado **anterior** ao ADR-034, em que todas as calls saíam untrusted (`ADR-034:29`). A opção C dissolveu-a em 2026-09-26. O adiamento é por custo de re-assinatura (§2.6). O que a R2 dissolve é o **resíduo 3** |
| «O `ParentStepID` não é propagado na via durável — resíduo 9, sem ticket» | **Falso.** Atravessa desde o AOS-454 (`integration/runtime_ports.go:222`), ticket **FEITO** e verificado em produção na v0.1.39. Confiei num comentário caducado (`replay/engine.go:127-133`) em vez de medir — o padrão do DEF-814 |
| «Há tecto para os argumentos e para os rótulos» | Lista incompleta: o `plan_input` **tem** tectos (128 KiB / 512 KiB / 16 payloads) |
| A restrição de ordem num sentido só | A recíproca também vale, e é pior: autoridade por argumento com conteúdo inline é um **downgrade** da protecção actual (§5) |
