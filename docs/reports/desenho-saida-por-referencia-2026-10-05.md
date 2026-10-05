# Desenho — saída por referência ao resultado da tool

> **Nota de 2026-10-05, posterior ao desenho.** O dono aprovou as quatro recomendacoes principais
> (declarar no plano; falhar sem origem designavel; entregar o resultado tal como a tool o devolveu;
> transporte pelo `aos-orq`). Um ponto mudou em relacao ao texto abaixo: as razoes novas do veredicto
> nao seguem so o modo do no. A declaracao de origem leva um vinculo por run («vinculativa» ou «so
> medicao»), para o AOS-499 poder medir em producao com o no em imposicao. Os tickets AOS-497 a
> AOS-501 sao a fonte que vale onde divergirem deste documento.

> Data: 2026-10-05. Base lida: `C:\Jimy\AOS\.claude\worktrees\fronteira`, HEAD `b677f1ec` (v0.1.46).
> Só leitura: nada foi executado, nenhum gate correu, nenhum pedido foi feito a modelos ou a produção.
>
> **Marcas.** `[V]` = verificado por leitura do código ou do documento citado. `[I]` = inferência minha,
> não exercitada. `[P]` = proposta. Os caminhos são relativos a `packages/` salvo indicação.
> Os números de ticket novos são marcadores (`T1`…`T5`): numeram-se com
> `python3 scripts/ci/sessoes.py reservar 5` (o maior existente é AOS-496).

---

## 0. Resumo

- Hoje a saída de um nó é o **texto final do modelo**; o resultado real da tool fica selado no nó e
  nunca sai dele pelo caminho do plano.
- Proposta: o plano **declara** que uma saída é o resultado de uma tool do nó (`outputs[].from_tool`,
  schema 1.3.0); o **kernel** designa e sela qual chamada é a origem (regra estrutural, sem ler texto);
  o `aos-orq` publica e entrega **esses bytes**, verificados contra o digest selado. O texto final do
  modelo continua a existir e deixa de ser a saída.
- Transporte eleito: **(a)** os bytes passam pelo `aos-orq`, pelo canal `inputs` que já existe.
  **(b)** (o nó consumidor resolve a referência) fica com gatilho declarado.
- Entrega em três passos, cada um sem piorar o anterior: nó às escuras, `aos-orq` em observação,
  schema + entrega.

---

## 1. Como é hoje, passo a passo

### 1.1 Do despacho à saída publicada

1. **Submissão do nó produtor.** O `aos-orq` submete o nó como run filho `<run>~<nó>` com o
   objectivo, a lista-branca de tools, as entradas e o contrato de conclusão
   (`cmd/aos-orq/node_executor.go:391-439`). `[V]`
2. **A tool corre no nó.** O loop medeia cada tool call pelo RM e guarda o resultado em três sítios do
   mesmo turno (`kernel/agent-runtime/loop.go:709-719`): `res.ToolResults` (em memória, `:714`),
   `turnCaptured` (`:719`), de onde saem o segmento `tool_result` do tail (`:669-673`) e a captura
   (`:688-708`). `[V]`
3. **O modelo escreve o texto final.** Um turno sem tool calls termina o run; o desfecho é
   `ConcludeRun`, que põe `FinalText = resp.Text` (`kernel/agent-runtime/completion.go:351-352`;
   `loop.go:803-811`). O veredicto (ADR-037) conta chamadas efectivas por tool do contrato
   (`completion.go:369-383`); **não olha para o texto** (`completion.go:22-29`). `[V]`
4. **O `aos-orq` lê o desfecho** por `GET /runs/{id}`. Ramo em memória: `FinalText: oc.Result.FinalText`
   (`cmd/aos/api.go:1507-1515`). Ramo durável (depois de reinício): o texto do **último turno
   capturado**, decifrado atrás do gate soberano e depois do selo WORM `read:outcome`
   (`cmd/aos/contrato_na_api.go:151-169`; `api.go:1616-1650`). `[V]`
5. **Publicação.** `publicarSaidas` usa `conteudo = st.FinalText` (`node_executor.go:643`) e apensa
   `plan.payload_published` com `record = {store: eventstore, stream: <run filho>, digest:
   sha256(FinalText)}` (`:644-648`). O payload do evento é construído só com `NodeID` e `Output`
   (`:620`): **`DerivedFrom` nunca é preenchido**. `type`, `taint` e `contract_digest` são derivados do
   contrato do documento aprovado (`control-plane/orchestrator/plannerevents/events.go:852-858`). `[V]`
6. **O conteúdo vive na memória do `serve`** (`node_executor.go:310-315`, `:653`); no log do plano fica
   a referência com o digest. O digest é calculado pelo próprio `aos-orq`: «controlo de integridade do
   transporte — não uma prova de origem» (`node_executor.go:598-604`). `[V]`
7. **Limites da publicação** `[V]`: dois contratos de forma aberta no mesmo nó não publicam nenhum
   (`:635-639`); texto acima de 128 KiB não se publica (`:334`, `:640-641`); `metrics` não se publica
   (`:632-634`); saída aberta vazia fecha o produtor `failed` (`:854-869`).

### 1.2 Da publicação ao prompt do consumidor

8. `entradasDe` lê o mapa em memória e monta `{from, output, digest, content}` por cada `consumes`
   do nó (`node_executor.go:443-458`); vai no campo `inputs` do `POST /runs`
   (`cmd/aos-orq/node_client.go:358-370`). `[V]`
9. O nó valida na porta: contrato completo, digest que bate com o conteúdo, 16 entradas, 128 KiB cada,
   512 KiB no conjunto (`cmd/aos/api.go:789-847`), e põe-nas em `Goal.Inputs` (`api.go:1101`). `[V]`
10. No prompt entram como segmentos **`plan_input`**, com `taint=untrusted` e a proveniência
    (`plan_input_from`, `plan_input_output`, `plan_input_digest`) nos rótulos da linha de delimitação
    (`kernel/agent-runtime/prompt.go:475-480`), **antes** do objectivo (`loop.go:513-521`). `[V]`
11. **Taint e autoridade.** `SegmentAuthority(plan_input) = untrusted`
    (`kernel/agent-runtime/context_authority.go:49`, `:56-65`): um run com `plan_input` tem contexto
    untrusted desde o turno 1, e o `TaintGate` nega qualquer capability privilegiada pedida nele
    (`kernel/reference-monitor/taint_gate.go:132-140`). O preâmbulo do layout 1.4.0 declara `plan_input`
    como DADOS (`kernel/agent-runtime/layout.go:127`). Na admissão do plano, um consumidor com
    autoridade privilegiada não pode consumir um output untrusted
    (`control-plane/orchestrator/planvalidate/payload.go:144`). `[V]`
12. **Taint do payload.** Um nó não-verificador publica sempre `untrusted`, qualquer que seja o tipo
    (`control-plane/orchestrator/plan/payload.go:240-249`). `[V]`
13. **Retoma do `serve` (AOS-418).** O conteúdo aberto relê-se do run filho (`final_text`) e só entra
    se o digest bater com o do evento (`node_executor.go:704-788`). `[V]`
14. **Retoma e replay do run consumidor.** As entradas vão no registo de retoma, selado por titular
    (`integration/resume_records.go:28-32`, `:57-59`), e no `TrajectorySpec.Inputs` do replay
    (`kernel/agent-runtime/replay/engine.go:58-61`, `:870-884`). O `prompt_hash` do consumidor cobre
    os bytes do `plan_input`. `[V]`

### 1.3 Onde está o resultado real da tool

| Sítio | O que guarda | Durável | Em claro | Onde |
|---|---|---|---|---|
| `Result.ToolResults` do desfecho retido | Todos os valores, por ordem de despacho, sem `step_id` | Não (memória, poda FIFO) | Sim, no processo do nó | `loop.go:129-131`, `:714`; `cmd/aos/service.go:146`, `:1581` `[V]` |
| Captura `replay.captured` do turno | `tool_results[i].output`, por turno | Sim | Não: selado por titular com `WithContentSealer` (produção) | `replay/nondeterminism_capture.go:154-167`, `:364-382` `[V]` |
| Step-ledger `step.ledger.applied` (via durável) | `result` selado **e `result_hash` = SHA-256 do claro, em claro** | Sim | O hash sim; o resultado não | `durable/step_ledger.go:63-79`; `activity/dispatch.go:312` `[V]` |
| `tool.call.mediated` / `tool.call.outcome` | Sem output e sem digest do output | Sim | — | `reference-monitor/outcome.go:33-47` `[V]` |

**É legível de forma durável e autorizada?**

- **Pelo nó: sim.** `ReplayEngine.Reconstruct` devolve, por turno, `Response` e `ToolResults`
  decifrados (`replay/sovereign_content.go:99-109`, `:245-257`). É o motor que o AOS-494 já usa para o
  texto final; os resultados de tool vêm na mesma estrutura e hoje são deitados fora
  (`contrato_na_api.go:168` lê só `Response.Text`). A correspondência chamada→resultado é por índice,
  garantida por `capturaCompleta` (`replay/engine.go:484-491`, função em `:488`). `[V]`
- **Pelo `aos-orq`: só através do nó, e hoje não o faz.** A rota `GET /runs/{id}/reconstruct` já serve
  `tool_outputs` por turno, sob o selo `read:reconstruct` (`cmd/aos/sovereign_replay.go:91-171`). O
  `aos-orq` só tem `Submit` e `Status` (`node_executor.go:43-46`). `[V]` Que a credencial do drenador
  passe o gate dessa rota é `[I]` (a admissão é a mesma função do `GET /runs/{id}`,
  `sovereign_replay.go:101`; não exercitei).
- **Não há digest do resultado amarrado a um evento de mediação.** O único digest do resultado em claro
  é o `result_hash` do ledger, e só existe na via durável. `[V]`

**Forma dos bytes.** Para tools com sandbox (o `doc_read`), o resultado é um envelope JSON
`{"stdout_text":"…","exit_code":0}`; `stdout` binário vai em base64 (`substrate/sandbox/mediated.go:118-159`).
É isto, tal e qual, que o modelo do produtor lê no `tool_result`. `[V]`

---

## 2. Quando é que a saída de um nó «é o que a tool devolveu»

### 2.1 O que o plano diz hoje

- `tools` num nó é **autoridade** (lista-branca), não intenção. Não há ligação saída→tool no schema:
  `Output` tem `name`, `type`, `taint` (`plan/payload.go:210-220`). `[V]`
- O `type` não é sinal. `record` está definido como «registo ESTRUTURADO produzido pelo nó»
  (`payload.go:108-111`), e na série de 2026-10-05 o mesmo nó de leitura declarou `record` em 17,
  `summary` em 3 e `artifact` em 1 (dado do enunciado; não tenho os planos). O prompt só diz ao
  planeador que há três formas abertas e que o executor transporta uma
  (`plannerprompt/artifact.go:89-98`). `[V]`

### 2.2 Alternativas de decisão

| | Regra | A favor | Contra |
|---|---|---|---|
| R1 | Inferir do tipo (`record` ⇒ passagem directa) | Sem schema | Falha 4 de 21 hoje; parte nós que transformam e declaram `record` (o tipo foi definido para isso) |
| R2 | Inferir da estrutura: não-verificador, **uma** tool pinada, **uma** saída aberta, sem `consumes` | Sem schema; cobre o nó de leitura medido `[I]` | Muda o que **flui** em planos aprovados por outras regras. Um nó «lê e extrai as acções» tem a mesma estrutura e passaria a entregar o documento cru |
| R3 | Tipo novo `tool_result` no enum | Mudança mínima; o `consumes` do consumidor tem de declarar o mesmo tipo | Com mais de uma tool no nó não diz qual; mistura forma com origem |
| **R4** | **Campo explícito `outputs[].from_tool`** = nome de uma tool pinada no mesmo nó | Liga saída→tool; origem ortogonal ao tipo; entra no `contract_digest` | Schema 1.3.0 e prompt novo; depende de o planeador o declarar |

### 2.3 Regra proposta `[P]`

**A saída é por referência se, e só se, o contrato do documento aprovado declara `from_tool`.**
A inferência estrutural R2 serve **só para medir** (§7.4), nunca para mudar a entrega.

Razão: a inferência decide, sem ninguém a ter aprovado, que o consumidor passa a receber outra coisa.
O contrato de conclusão pôde ser inferido (decisão de 2026-10-04) porque é um gate de qualidade que
não altera dados; aqui altera-se o conteúdo que atravessa a aresta.

Forma: `{"name":"notas","type":"record","from_tool":"doc_read"}`.

**Regras do validador** (extensão de `planvalidate/payload.go`; todas estruturais):

1. `from_tool` é o `name` exacto de uma tool do `tools` do mesmo nó, e conforma a `ValidIdentifier`.
2. Só em formas abertas, e não em `summary` (um resumo é, por definição, transformado): `record` ou
   `artifact`.
3. O nó não é `verifier` e **não tem `consumes`** (v1). É o que garante que a chamada foi pedida com o
   contexto ainda trusted (§3.3).
4. No máximo uma saída com `from_tool` por nó. Mantém-se o limite do executor: uma só saída aberta
   por nó (`node_executor.go:635-639`).
5. Usar o campo obriga a carimbar `plan_version` ≥ 1.3.0 (`plan_version_below_features`).

O taint não muda: `EffectiveOutputTaint` continua `untrusted` para todo o não-verificador. A regra
`consumes_taint_authority` continua a impedir um consumidor privilegiado de o consumir.

### 2.4 Impacto no schema (SemVer) `[P]`

- `Output.FromTool string \`json:"from_tool,omitempty"\``. Com `omitempty`, um documento anterior
  re-serializa com os mesmos bytes — a propriedade que sustenta o replay de `planmigrate`
  (`plan/semver.go:71-81`). `[V]` para a regra, `[I]` para a aplicação.
- **MINOR**: `CurrentPlanVersion` 1.2.0 → 1.3.0 (`plan/semver.go:82`), com uma linha em
  `schemaFeatures` (`semver.go:123-136`).
- `CanonicalOutput` passa a incluir a origem **só quando presente** (`…:tool=<nome>`), para o
  `contract_digest` amarrar a declaração: sem isso, um documento editado para tirar ou pôr `from_tool`
  dava o mesmo carimbo (`plan/payload.go:365-386`). Contratos sem o campo ficam byte a byte iguais.
- **Planos gravados.** Um documento sem o campo decodifica como antes. Um documento **com** o campo é
  recusado por um binário anterior (`DisallowUnknownFields`, `plan/plandocument.go:276-281`). `[V]`
  Consequência: depois de existirem planos 1.3.0, um rollback do `aos-orq` deixa de os ler. Se a
  geração seguinte do pedido re-planeia a partir do objectivo, o plano volta a sair sem o campo `[I]`;
  um plano aprovado e corrido por `serve --plan-doc` (AOS-412) não tem essa saída `[I]`. A verificar
  em T4.
- O cartão de aprovação (ADR-013) tem de mostrar a origem da saída. `[P]`
- Não verifiquei se o mapeador para `planapproval.Plan` copia `outputs` campo a campo.

### 2.5 Impacto no prompt do planeador `[P]`

MINOR 1.4.0 → 1.5.0 (`plannerprompt/artifact.go:148-151`), aditivo:

- bloco SCHEMA: `outputs  lista de {name*, type*, taint, from_tool}` (linha `:33`). O teste que deriva
  os campos dos tipos de `plan` obriga a nomeá-lo (`TestTemplateDeclaraOSchemaQueODecodeExige`, citado em `artifact.go:11`; existe em
  `plannerprompt/aos400_schema_test.go:322` `[V]`, não li o corpo);
- regra 6: «"1.3.0" se algum output usa `from_tool`»;
- regra 13: quando o nó seguinte precisa **do que a tool devolveu** (o documento lido), o output
  declara `from_tool` com o nome da tool do nó e `type: "record"`; o sistema entrega o resultado da
  tool e ignora o texto do nó. Esse nó faz **uma** chamada dessa tool e não tem `consumes`. Quando o
  nó seguinte precisa do que o nó **concluiu** (resumo, classificação), não se usa `from_tool`.

É uma instrução ao modelo, não uma garantia — a mesma classe de resíduo da regra 12
(`artifact.go:146-147`). Mede-se (§7.4).

---

## 3. O mecanismo

### 3.1 O que é comum às alternativas `[P]`

Três peças não dependem do transporte, e são o que torna a saída verificável:

1. **A declaração chega ao run.** O `POST /runs` ganha `output_from_tool` (string), validado contra a
   lista-branca do mesmo pedido, como o contrato de conclusão (`cmd/aos/contrato_na_api.go:52-70`).
   Vai no `Goal`, no registo de retoma e no manifesto do turno (`omitempty`), como
   `CompletionRequires` (`integration/resume_records.go:80-91`; `completion.go:137-143`).
2. **O kernel designa a origem** (§3.3) e **sela** `{tool, step_id, digest, bytes, estado}` na
   transição terminal, ao lado do veredicto. Sem conteúdo. É o digest do `tool_result` gravado,
   calculado por quem executou a tool e não por quem transporta.
3. **O evento do plano nomeia a origem.** `plan.payload_published` ganha `source`
   (`omitempty`): `{kind: "tool_result", tool, step_id}`. `kind` e `tool` são **derivados** do contrato
   em `NewPayloadPublished`, como `type` e `taint`; `step_id` é validado na forma. Regra simétrica:
   contrato com `from_tool` ⇒ `source` obrigatório; sem ele ⇒ proibido. `record.digest` passa a ser o
   digest selado pelo kernel.

**E o `DerivedFrom`?** Não serve para isto: é uma lista de `(node_id, output)` de **outros nós**
(`plannerevents/events.go:702-711`), não de mediações. Para a origem numa tool o campo certo é o
`source` novo. Preenche-se `DerivedFrom` onde ele é verdadeiro e hoje está vazio: na saída de texto de
um nó com `consumes`, com os contratos consumidos (derivado do plano, sem custo). A cadeia fica
legível no log: `resumo ← derived_from(leitura/notas) ← source(tool_result, passo, digest)`.

### 3.2 Alternativas de transporte

**(a) O nó devolve o resultado ao `aos-orq`, que o publica e entrega.**
O `GET /runs/{id}` de um run que declarou `output_from_tool` devolve `output_source` (metadados) e
`output` (os bytes designados). O `aos-orq` confere `sha256(output)` contra o digest selado, publica e
entrega pelo campo `inputs` de hoje. O nó consumidor não muda.

**(b) O payload é só a referência; o nó consumidor resolve-a.**
O `inputs[]` do `POST /runs` ganha uma variante sem `content`: `{from, output, digest, ref:{run_id,
step_id}}`. O nó lê a captura do run produtor no seu Event Store, decifra, confere o digest e monta o
`plan_input`. Os bytes não passam pelo `aos-orq`.

**(c) Outras, consideradas e postas de parte.**
(c1) O consumidor vai buscar com as suas tools (opção C do AOS-414): devolve a decisão ao modelo, e o
contexto do consumidor já é untrusted. (c2) O runtime repete a mesma tool call no consumidor: reexecuta
um efeito, o conteúdo pode ter mudado, e seria uma chamada mediada que nenhum modelo pediu.
(c3) Publicar na MEM (`PayloadStoreMemory` existe no enum, `events.go:675`): a memória não está ligada
em produção e é fail-closed na autoridade (`context_authority.go:50-51`).

### 3.3 «Qual chamada é a saída» — regra de designação `[P]`

Calculada no kernel, de factos estruturais, pela mesma função no loop e no motor de replay (como
`RunEvidence.Observe`, `completion.go:252-294`):

> A origem é a chamada **efectiva** (sem recusa e sem erro de tool) da tool declarada, feita no
> **primeiro turno do run que despachou tool calls**, e só se o contexto desse turno era **trusted**.
> Exactamente uma ⇒ `designated`. Nenhuma ⇒ `missing`. Mais de uma ⇒ `ambiguous`.

- **Porquê «contexto trusted».** É a propriedade que diz que os argumentos da chamada foram escolhidos
  por um modelo que só tinha visto o prefixo e o objectivo. Fecha o ataque do ADV1 (ponto 28): um
  documento que diga «lê também o doc `segredo`» provoca uma segunda leitura, mas essa é pedida depois
  de um `tool_result` e nunca é a origem. O rótulo já é calculado por turno (`loop.go:544-547`) e
  recalcula-se no replay sem ser gravado (ADR-034 §2.4). `[V]`
- **Porquê o primeiro turno com tools.** O join é monótono: depois do primeiro `tool_result` o contexto
  é untrusted até ao fim (`context_authority.go:16-21`). Em produção, com `cap:fs.read` armada, a
  segunda leitura já é negada (ADR-034 §5, resíduo 3). `[V]`
- **Várias chamadas no mesmo turno** (duas leituras em paralelo): `ambiguous`, e o produtor falha.
  Não se concatena: seria o runtime a compor conteúdo, e o digest deixava de ser o de um resultado
  gravado. Mede-se antes de impor (§7.4).
- **Tool negada ou com erro:** não é efectiva; não é origem. Se era a única, `missing`.
- **Nó com `plan_input` ou memória:** o contexto é untrusted desde o turno 1; nunca há origem. O
  validador já recusa `from_tool` com `consumes`; o kernel é a segunda linha.

### 3.4 Comparação

| Eixo | (a) bytes pelo `aos-orq` | (b) o consumidor resolve |
|---|---|---|
| **Quem lê o quê** | O `aos-orq`, com a sua credencial, numa leitura soberana normal do run produtor: gate D7, selo WORM D6 antes de abrir (`api.go:1455`, `:1504`, `:1637`). Nada de novo | O **nó**, com o seu acesso à custódia, decifra o run produtor a pedido de uma referência num corpo de `POST`. É um *confused deputy* a desenhar: tem de provar o direito do chamador ao run produtor, o mesmo plano (`run.plan_origin`, `cmd/aos/plan_origem.go:124`) e o mesmo titular |
| **Residência / titular** | Os do `GET` de hoje. O conteúdo reentra pelo `POST` e é selado sob o titular do consumidor | O nó pode **impor** o mesmo titular nas duas pontas (hoje ninguém o impõe: o nó não sabe de onde veio o `content`) |
| **Selo WORM da leitura** | `read:outcome`, já existente. Exige emenda ao ADR-037 §2.8: num run por referência, a saída **é** um resultado de tool | Rótulo novo (`read:plan_input`) e decisão sobre a quem se imputa a leitura |
| **Exposição no orquestrador** | Igual à de hoje: o conteúdo vive na memória do `serve`, nunca no WAL (`node_executor.go:310-315`) | Menor: o `aos-orq` só vê digest e tamanho |
| **Tamanho** | Tectos de hoje: 128 KiB por payload, 512 KiB no conjunto. Acima ⇒ não se transporta, o produtor falha com causa própria. Hoje o modelo truncaria em silêncio | Deixa de depender do corpo do `POST`; fica o limite da janela |
| **Binário** | O `content` é uma string JSON. O envelope da sandbox é sempre texto (base64 para binário). Um resultado que não seja UTF-8 válido não se transporta em v1 | Possível sem recodificação |
| **Taint** | `plan_input`, `untrusted`, como hoje. Nenhum caminho novo para trusted | Igual |
| **Integridade** | `record.digest` = digest selado pelo kernel; o `aos-orq` confere os bytes ao ler e ao reidratar; o nó consumidor confere o transporte (`api.go:837-843`) | Igual, e o nó confere contra a própria captura |
| **Retoma do `serve` (AOS-418)** | Relê `output` do run filho e compara com o evento, como hoje faz com `final_text` (`node_executor.go:761-788`). O ramo durável passa a servir o resultado designado em vez do texto | A referência do log chega: não há conteúdo a reidratar. Desaparece o resíduo «`serve` retomado com a custódia fechada» |
| **Retoma do run produtor** | Um resultado com êxito está memorizado no ledger e reproduz os mesmos bytes; a designação recalcula-se. Uma chamada que falhou e é negada na segunda vida dá `missing` — a classe já declarada no ADR-037 §5 | Igual |
| **Replay e `prompt_hash` do consumidor** | Inalterados: o conteúdo está em `Goal.Inputs`, no registo de retoma e no `TrajectorySpec` | Depois de resolvida a referência, igual. A resolução tem de acontecer **antes** de o run existir, senão o replay dependia de outro stream |
| **Replay do produtor** | O manifesto ganha `output_from` (`omitempty`); o `prompt_hash` não muda (o modelo não é avisado) | Igual |
| **Crypto-shredding** | Produtor: a captura deixa de abrir ⇒ `output_unavailable`. O registo em memória do nó continua a servir até reinício: é o AOS-496, que passa a cobrir também `output`. O digest em claro no stream do plano sobrevive, como já sobrevive o `result_hash` do ledger | Igual, sem a cópia transitória no `aos-orq` |
| **Compatibilidade** | Dois campos novos no wire do nó (`output_from_tool` no `POST`, `output*` no `GET`). O `POST` recusa desconhecidos (`api.go:2915-2921`) ⇒ **anúncio no `GET /tools`** antes de enviar, como `completion_contract` (`cmd/aos/catalogo_de_tools.go:85-88`). O `GET` é lido sem recusa de desconhecidos (`node_client.go:426-431`) `[V]` | Mais um: variante de `inputs[]` sem `content`, também atrás de anúncio |
| **Custo** | Pequeno a médio. Reutiliza gate, selo, canal `inputs`, reidratação | Médio a grande. Decifração e autorização novas no caminho de admissão do `POST /runs`; semântica de nova tentativa quando a custódia está fechada |
| **Rollback** | Ver §3.6 | Igual, mais a variante de `inputs` |

### 3.5 Eleita: (a), com a âncora no kernel

- Fecha o defeito medido com a menor superfície nova: o que muda é **de onde vêm os bytes** e
  **a que se amarra o digest**; quem lê, com que autorização e por que canal ficam como estão.
- (b) é a direcção certa a prazo (é o primeiro passo real da opção A do ADR-034, e a media de A5/A6
  não cabe inline), mas a sua razão principal de rejeição **agora** é o *confused deputy*: põe o nó a
  decifrar conteúdo de um titular por conta de uma referência escrita num corpo de pedido, dentro do
  caminho de admissão. Cada verificação esquecida ali é conteúdo de um run a entrar noutro que o
  chamador depois lê. Merece ADR próprio e não é preciso para fechar os 2 em 21.
- As três peças comuns (§3.1) são as mesmas para (b): trocar o transporte mais tarde não mexe no
  schema do plano, na designação nem no evento.
- **Gatilho de (b):** o primeiro payload legítimo acima do tecto; ou a fase A5/A6; ou a reabertura do
  DEF-806.

### 3.6 Compatibilidade entre versões e rollback `[P]`

| Nó `aos` | `aos-orq` | Comportamento |
|---|---|---|
| novo | antigo | Nada é enviado. Igual a hoje |
| antigo | novo, plano sem `from_tool` | Igual a hoje |
| antigo | novo, plano com `from_tool` | O nó não anuncia ⇒ o `serve` **recusa correr** o plano, com código próprio e determinista. Não cai para o texto do modelo: publicaria texto sob um contrato que promete outra coisa |
| anúncio ilegível | novo | Pára e o pedido volta à fila, como em `errAnuncioIlegivel` (`contrato_de_conclusao.go:241-245`) |

Ordem de saída: nó primeiro, depois o `aos-orq` (a do AOS-494/495).

Rollback:

- **do nó**, com planos 1.3.0 em fila: esses pedidos saem recusados (linha 3). Reverte-se o `aos-orq`
  antes do nó.
- **do `aos-orq`**: o binário anterior não decodifica documentos com `from_tool` (§2.4). Eventos
  `plan.payload_published` com `source` são lidos sem erro na reidratação (`json.Unmarshal` sem
  recusa de desconhecidos, `node_executor.go:720-728` `[V]`); o digest não bate com o `final_text` e o
  consumidor falha fechado. Não verifiquei `RefFromPublished` nem `PayloadReader`.
- **interruptor**: `AOS_ORQ_SAIDA_POR_REFERENCIA=off|observe|on` no `aos-orq`. Em `off` e `observe` a
  entrega é a de hoje para planos sem o campo, e planos com o campo são recusados. O prompt do
  planeador é um artefacto governado com uma só versão corrente; a 1.5.0 só entra com `on`.

---

## 4. O que acontece ao texto final do modelo

- **Continua a existir.** A regra de terminação não muda: o modelo dá o turno final, o texto é
  capturado e selado, e o `GET /runs/{id}` continua a devolvê-lo em `final_text`.
- **Não é a saída e não é entregue.** Para um contrato com `from_tool`, o `aos-orq` não o publica nem o
  passa ao consumidor, nem como comentário: seria repor conteúdo escrito por um modelo que leu
  untrusted no caminho que acabou de se limpar. Fica para auditoria.
- **A regra «saída vazia não se publica»** (`node_executor.go:854-869`) passa a olhar para os bytes
  designados, não para o texto, nos nós por referência.
- **Custo conhecido:** o modelo continua a transcrever o documento (tokens de saída) para um texto que
  ninguém consome. Duas optimizações ficam **fora de âmbito**, a decidir por medição: (i) um sufixo
  constante no objectivo, escrito pelo `aos-orq`, a dizer que não transcreva (como
  `instrucaoDeVeredicto`, `node_executor.go:54-56`) — muda o prompt e pode mudar a taxa de chamadas;
  (ii) o kernel concluir o run logo após a chamada designada — mexe na regra única de terminação
  (AOS-492).

**O veredicto (ADR-037).** Sim, passa a exigir que a saída derive de uma chamada efectiva, **para os
runs que declaram `output_from_tool`**:

- a designação é um **facto**, calculado em qualquer modo e selado;
- com o modo ≠ `off`, o veredicto ganha duas razões no vocabulário fechado: `output_source_missing` e
  `output_source_ambiguous`; `empty_output` avalia os bytes designados. Em `observe` reporta; em
  `enforce` fecha `failed`. Um `aos-orq` anterior lê uma razão desconhecida como
  `razao_desconhecida` (`contrato_de_conclusao.go:95-98`) `[V]`;
- independentemente do modo do nó, o `aos-orq` **não publica** sem `designated` e fecha o produtor com
  causa própria (`saida_sem_origem`, `saida_ambigua`, `saida_nao_transportavel`,
  `saida_indisponivel`).

Para um nó por referência, isto substitui o resíduo «evidência de tool call não é fidelidade da saída»
por uma igualdade de digests.

---

## 5. O que isto NÃO fecha

1. **Nós que transformam** (resumir, classificar, extrair). O modelo continua no caminho e o resíduo
   R1 do ADR-034 mantém-se. O que seria preciso: um **verificador** que receba a fonte autêntica e o
   produto e emita um veredicto de forma fechada (ADR-022 §2.2). A saída por referência é o que torna
   isso possível, porque dá ao verificador a fonte e não uma transcrição. Fora de âmbito; não é um
   juiz no kernel (o acompanhamento §8 exclui-o).
2. **A chamada errada.** A fidelidade é ao resultado da chamada feita. Se o modelo leu o documento
   errado, entrega-se esse, byte a byte. O `step_id` liga ao `tool.call.mediated` do mesmo passo, onde
   um auditor vê o recurso.
3. **O run que não chama a tool** (cerca de 1 em 5 na v0.1.45): continua vermelho, agora com
   `output_source_missing`. Recuperar é a fase A1.
4. **A tool devolver dados errados ou desactualizados.**
5. **Agregações** (duas leituras num nó): `ambiguous` em v1.
6. **Resultados acima de 128 KiB ou não-texto:** falha explícita. Fecha-se com (b).
7. **O nó sem tools que recusa o próprio objectivo** (1 em 21): é higiene do protocolo (A1).
8. **A separação de planos (DEF-806).** Em (a) o conteúdo untrusted continua inline no tail do
   consumidor. Tira-se o modelo do caminho **do produtor**; não se cria um handle.

---

## 6. Segurança

### 6.1 O conteúdo chega sem a «lavagem» do modelo: melhor ou pior?

**Melhor para integridade, neutro para a exposição do consumidor, com três diferenças a medir.**

- O modelo do produtor **nunca foi um filtro**. O nó de leitura transcreve; ecoar texto untrusted é o
  seu comportamento normal (análise §5.3). Em 19 de 21 planos o consumidor já recebe o documento
  essencialmente inteiro. Contar com a paráfrase para tirar uma injecção é contar com um efeito
  probabilístico.
- A «lavagem» é o caso **pior**: um modelo instruído pelo documento reescreve a intenção do atacante
  na voz do sistema (R1 do ADR-034). Por referência, o produtor deixa de poder ser levado a alterar o
  que segue para jusante.
- O que muda de facto: (i) o atacante passa a controlar os bytes do `plan_input` **com exactidão**
  (interessa a quem tente forjar delimitadores); (ii) chega o envelope JSON cru, com o texto escapado;
  (iii) chega o documento todo, logo mais tokens.

### 6.2 O que já trata `plan_input` como dados `[V]`

- Autoridade: `plan_input` ⇒ contexto untrusted (`context_authority.go:49`, `:56-65`).
- `TaintGate`: capability privilegiada com contexto untrusted é negada (`taint_gate.go:132-140`).
- Admissão: consumidor privilegiado não consome untrusted (`planvalidate/payload.go:144`).
- Protocolo: `plan_input` é DADOS (`layout.go:127`); a proveniência vive na linha de delimitação, não
  no corpo (`prompt.go:475-480`).

O conteúdo por referência entra pelo **mesmo** campo `inputs`: nada disto muda. Não reverifiquei a
mecânica de escape do corpo do segmento; o corpus adversarial tem de correr pelo caminho novo (§7.4).

### 6.3 Superfície nova

1. O `GET /runs/{id}` passa a servir um resultado de tool sob `read:outcome`. Para um nó fiel é o
   mesmo conteúdo que o `final_text` de hoje; formalmente alarga o que o rótulo cobre (emenda ao
   ADR-037 §2.8).
2. Um campo novo de configuração no `POST /runs`, vindo do submissor autenticado e limitado à
   lista-branca.
3. O kernel passa a **escolher** um resultado. A escolha não pode depender de conteúdo untrusted: é o
   que a regra do contexto trusted garante.
4. O registo de desfechos em memória do nó já retém os resultados (`loop.go:714`); passa a servi-los.
   O AOS-496 tem de cobrir `output`.
5. Mais um digest de conteúdo do titular em claro, no stream do plano (hoje é o do texto final).

### 6.4 Requisitos não-negociáveis

- **RN1.** Um payload por referência é sempre `untrusted`. «Digest verificado» não é «confiável».
- **RN2.** A origem é designada pelo kernel a partir de factos estruturais (tool, efectividade, rótulo
  do contexto do turno). Nunca pelo texto, pelo conteúdo do resultado, pelo modelo ou pelo `aos-orq`.
- **RN3.** A declaração vem do documento aprovado, entra no `contract_digest`, chega ao run por um
  campo do submissor autenticado, e sobrevive à retoma (registo de retoma e manifesto).
- **RN4.** Sem queda para o texto do modelo. Origem em falta, ambígua, indisponível ou não
  transportável ⇒ o produtor falha com causa em vocabulário fechado.
- **RN5.** O digest publicado é o selado pelo kernel. O `aos-orq` confere os bytes contra ele ao
  publicar e ao reidratar.
- **RN6.** O conteúdo só se serve atrás do gate soberano e depois do selo WORM; nunca entra no WAL do
  orquestrador; os eventos novos são sem conteúdo.
- **RN7.** Mesmos tectos, sem truncar.
- **RN8.** Anunciar antes de enviar, para os dois campos de wire; anúncio ilegível pára, não é «não».
- **RN9.** A designação é a mesma função no loop e no replay.
- **RN10.** Observação antes de mudar a entrega.

---

## 7. Recomendação

### 7.1 Alternativa

**(a) com a âncora no kernel**, declarada no plano por `from_tool`. **(b)** rejeitada como primeira
entrega (*confused deputy* no caminho de admissão), registada com gatilho.

### 7.2 Tickets

| # | Epic | Título | Depende de | Critérios de aceitação (resumo) | Est. |
|---|---|---|---|---|---|
| **T1** | EPIC-02 | O kernel designa e sela a origem da saída de um run: o resultado da chamada efectiva da tool declarada | AOS-493 | `Goal.OutputFromTool`; designação pela regra da §3.3, igual no loop e no replay (teste de paridade); `{tool, step_id, digest, bytes, estado}` selado na transição terminal e no `Result`; campo no manifesto e no registo de retoma, `omitempty` (goldens sem mudança de bytes); razões novas do veredicto em `observe`/`enforce`; testes: zero, uma, duas chamadas no turno; chamada negada; erro de tool; segunda leitura em turno posterior; run com `plan_input` | M |
| **T2** | EPIC-19 | O nó aceita `output_from_tool` no `POST /runs`, devolve a origem e a saída no `GET /runs/{id}`, e anuncia-o no `GET /tools` | T1, AOS-494; relaciona AOS-496 | validação contra a lista-branca (400 com mensagem própria); ramo em memória e ramo durável devolvem os mesmos bytes, conferidos contra o digest selado; durável só depois do selo; definitivo ⇒ `output_unavailable`, transitório ⇒ 503; acima do tecto ou não-UTF-8 ⇒ metadados sem conteúdo; anúncio presente; smoke sobre JetStream | M |
| **T3** | EPIC-19 | O `aos-orq` mede a saída por referência sem mudar a entrega | T2 | envia o campo aos candidatos estruturais (R2) **só** com o nó em `observe`; publica o `final_text` como hoje; métricas da §7.4, sem conteúdo; banner com o modo; nenhum desfecho de plano muda (teste) | S |
| **T4** | EPIC-19 | O plano declara a origem de uma saída: `outputs[].from_tool`, schema 1.3.0 | — | campo, `schemaFeatures`, `CanonicalOutput` (contratos antigos byte a byte iguais); as cinco regras do validador com códigos próprios; cartão de aprovação; fixtures congeladas 1.2.0 inalteradas; **o prompt não muda neste ticket** | M |
| **T5** | EPIC-19 | O `aos-orq` entrega por referência as saídas declaradas | T2, T3 (medição lida), T4 | `publicarSaidas` pelos bytes designados; `source` no evento, derivado; `DerivedFrom` preenchido nos nós com `consumes`; reidratação pelo `output`; causas novas; recusa do plano contra nó que não anuncia; prompt 1.5.0 (regra 13) e golden-set; interruptor; série em produção | M |

Não abrir agora: o transporte (b); o sufixo «não transcrevas»; a conclusão antecipada do run; o
verificador de transformações.

### 7.3 ADR

**ADR novo** (ADR-038, MADR): «A saída de um nó de passagem directa é o resultado da tool, por
referência». Decide a declaração no plano, a regra de designação, o transporte (a), o destino do texto
final e a rejeição de (b) com gatilho. **Emendas no mesmo PR:** ADR-037 (§2.4 razões novas; §2.8 o que
`read:outcome` cobre; §5 o resíduo fecha para passagem directa), ADR-027 §2.4 (origem do payload),
ADR-022 §2.3 e `tecnica/18` §3.6.1 (linha 1.3.0). Regenerar a RTM (`tecnica/16`) no mesmo commit.

### 7.4 O que tem de ser medido

Em observação (T3), tudo sem conteúdo:

- nós por classe estrutural (candidato / não candidato) e, nos candidatos, o estado da designação
  (`designated`, `missing`, `ambiguous`);
- tamanho do resultado designado, em classes, contra os 128 KiB;
- razão `len(final_text) / len(output)` em classes: é a medida, sem ler o texto, de quantos nós
  resumem em vez de transcrever (os 2 em 21);
- tokens de saída do produtor.

Depois de ligar (T5):

- **invariante**, em 100% dos payloads por referência: digest entregue = digest selado. Divergências
  têm de ser zero; é a prova por construção, e não precisa de amostra;
- candidatos estruturais que o planeador **não** declarou com `from_tool` (cumprimento da regra 13);
- recusas do validador pelas regras novas;
- nós falhados por causa nova, e planos que passam de 0 para 13;
- tokens de entrada e orçamento do consumidor (a estimativa do planeador não conta com o documento
  inteiro);
- a série de 21 planos repetida: perda de factos no nó de leitura (alvo: zero) e qualidade do resumo
  com o envelope cru;
- o corpus adversarial do gate `security` pelo caminho por referência.

Para afirmar menos de 2% de perda de factos **no plano inteiro** são precisas cerca de 150 execuções
limpas; a propriedade do nó de leitura não precisa dessa série.

### 7.5 Ordem de entrega

1. **T1 + T2 no nó, às escuras.** Ninguém envia o campo. Nada muda.
2. **T3, `aos-orq` em observação.** A entrega é a de hoje. Mede-se durante pelo menos uma série.
3. **T4, schema e validador, sem prompt.** O planeador não emite o campo; nenhum plano o usa.
4. **Decisões do dono (§7.6) com os números de 2.**
5. **T5 com o interruptor em `on`:** prompt 1.5.0 e entrega por referência, **só para saídas
   declaradas**. Planos sem o campo ficam como hoje.

Em nenhum passo um plano que hoje sai certo passa a sair errado em silêncio. O pior caso novo é um
vermelho com causa nomeada onde hoje havia um verde (por vezes falso).

### 7.6 Decisões do dono

| # | Pergunta | Recomendação |
|---|---|---|
| D1 | A passagem directa declara-se no plano (`from_tool`, schema 1.3.0), ou infere-se da estrutura do nó sem mudar o schema? | **Declarar.** Inferir só para medir |
| D2 | Com a saída declarada por referência e sem origem designável (nenhuma chamada, mais de uma, grande de mais), o nó falha, ou entrega-se o texto do modelo? | **Falha**, com causa |
| D3 | O consumidor recebe os bytes tal como a tool os devolveu (envelope JSON), ou o texto desembrulhado pelo sistema? | **Tal como devolvidos** em v1; rever com a medição de qualidade |
| D4 | Os bytes passam pelo `aos-orq` (a), ou o nó consumidor resolve a referência (b)? | **(a) agora**; (b) com gatilho |
| D5 | O texto final do nó produtor é ignorado, ou segue para o consumidor como comentário? | **Ignorado** e guardado para auditoria |
| D6 | Um nó candidato (uma tool, uma saída, sem `consumes`) em que o planeador não declarou a origem é recusado pelo validador, ou corre como hoje? | **Corre como hoje**, por agora; decidir com a taxa de omissão medida |

### 7.7 Os três maiores riscos

1. **O planeador declara mal.** Omite `from_tool` e o defeito fica; ou declara-o num nó cujo trabalho é
   transformar, e o consumidor recebe o documento cru. É uma instrução a um modelo. Mitigação: medir a
   omissão; a regra 3 do validador (sem `consumes`) corta parte dos casos errados.
2. **Mais vermelhos.** Duas leituras no mesmo turno, resultados acima de 128 KiB, saída não-texto, nó
   reiniciado com a custódia fechada. São falhas honestas, mas contam contra o «não cumprido abaixo de
   2%». A observação existe para as contar antes.
3. **O consumidor com o documento cru.** Bytes exactos do atacante no `plan_input`, envelope JSON
   escapado, mais tokens e orçamento. As defesas são as de hoje e não foram exercitadas por este
   caminho; e o `read:outcome` alargado mais o registo em memória (AOS-496) ficam por fechar.

---

## 8. Limites deste desenho

- Tudo o que está marcado `[V]` foi lido, não executado. Não li a máquina de estados que grava o
  veredicto na transição (só o chamador, `contrato_na_api.go:113-120`); que a transição aceite mais um
  campo aditivo é `[I]`.
- Não tenho os 21 planos: a distribuição `record`/`summary`/`artifact` e a estrutura do nó de leitura
  vêm do enunciado.
- Não verifiquei: o corpo do teste do prompt que deriva campos do schema; `planmigrate`; o mapeador para
  `planapproval`; `RefFromPublished`/`PayloadReader` perante um campo novo no evento; a política que
  admite o drenador em `GET /runs/{id}/reconstruct`; o que acontece a um pedido em fila quando o
  documento gravado deixa de decodificar.
- Os números de linha são os do HEAD `b677f1ec`.
