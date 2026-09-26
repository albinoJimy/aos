# ADR-034 — A autorização de uma tool call é o rótulo do CONTEXTO que o modelo viu, cunhado no runtime

- **Estado:** Aceite
- **Data:** 2026-09-26
- **Deciders:** Dono do produto (decisão, 2026-09-26) · executor de AOS-069 (implementação)
- **Ticket:** AOS-069 (fase 0 em código e fase 1)
- **Materializa:** a semântica de enforcement do **ADR-005** (separação control/data-plane + taint),
  que continua no catálogo de uma linha. Este ADR não a substitui: fixa a forma **provisória** —
  a opção C — com que o nó a cumpre até ao gatilho da opção A (§5.4).
- **Relacionados:** ADR-002 (Reference Monitor mandatório — o rótulo chega-lhe pelo
  `CallContext`), ADR-010 (replay determinístico), ADR-013 (o classificador de risco lê o mesmo
  rótulo), ADR-015 (retoma por replay-then-continue), ADR-022 e ADR-027 §2.4 (os payloads do plano
  entram no nó como `plan_input` untrusted)

## 1. Contexto

A análise crítica do ciclo do plano em produção mediu o caso que o AOS-069 fecha. No plano
`plan-e2e-docread-1790340990`, o nó `n1` leu o documento `notes` e publicou-o como contrato
`document_content`; o nó `n2` — um LLM — consumiu-o marcado `taint=untrusted`, inline no prompt. A
marcação existia; a separação não (DEF-806, DEF-807; ADR-027 §2.4).

Três factos do código determinaram o desenho, verificados antes de o decidir:

- **F1.** A autoridade de uma tool call vinha de um campo da resposta do modelo,
  `ToolInvocation.AuthorizationTaint`. Nenhum adaptador o preenchia (o `toolEnrichingClient` do nó
  deixava-o vazio de propósito) e o vazio resolvia untrusted. Logo **todas** as tool calls do
  modelo saíam autorizadas como untrusted: o sistema não distinguia um nó com contexto limpo (`n1`,
  só o objectivo) de um com `plan_input` untrusted (`n2`).
- **F2.** Por isso, armar `cap:fs.read` no TaintGate (ou pôr a cláusula de taint na regra Cedar
  `allow_fs_read`) partia o `doc_read` de `n1`: proibia a leitura em vez de proibir a leitura
  **depois de conteúdo não-confiável**.
- **F3.** O validador do plano já proíbe tools de EFEITO a consumidores de payload untrusted (P4,
  `planvalidate/payload.go`), e promove a trusted o veredicto de forma fechada de um verificador
  (`plan/payload.go`, `EffectiveOutputTaint`). A fronteira do plano está feita; faltava a do nó.

Um campo de autorização na saída do modelo é, além disso, o defeito estrutural do DEF-807: a
garantia «só o control-plane marca trusted» dependia de nenhum adaptador escrever no campo — uma
convenção, e não uma propriedade.

## 2. Decisão

### 2.1 A autorização é derivada do contexto, e cunhada no runtime

O loop mantém um **join monótono** dos rótulos de taint de tudo o que entra no tail, com a álgebra
que já existia (`reference-monitor/taint`: `Join`, reticulado `{trusted ⊑ untrusted}`). A
autorização de cada tool call pedida no turno *t* é o rótulo do contexto **no Assemble desse
turno** — lido antes de a resposta existir, pelo que nada do que o modelo devolve o pode mudar.

| Segmento | Rótulo com que entra no join |
|---|---|
| prefixo (system + tool set congelado) | trusted — é o ponto de partida |
| objectivo | trusted (submissão autenticada) |
| correcção de steer | trusted (humano autenticado pelo canal de controlo) |
| histórico (texto do modelo) | o rótulo do contexto que o produziu |
| `plan_input` | untrusted |
| `tool_result` (permit, deny ou erro) | untrusted |
| memória | untrusted — **fail-closed** |
| qualquer outro kind | untrusted |

O rótulo é função do **tipo** dos segmentos que o runtime acrescenta, nunca do seu conteúdo: um
documento que diga `taint=trusted` continua a ser um `plan_input`. O join é monótono — uma
correcção trusted depois de um resultado de tool **não** devolve a autoridade.

Implementação: `SegmentAuthority`/`ContextAuthority` e a janela decorada `authorityWindow`
(`packages/kernel/agent-runtime/context_authority.go`); o loop lê o rótulo a seguir ao `Assemble`
e escreve-o em `CallContext.Taint` (`loop.go`).

### 2.2 A fronteira do `ModelClient` deixa de transportar autoridade

`ToolInvocation.AuthorizationTaint` **sai**, com `AuthorizeTrusted` e `authorizationTaintOf`, que
só existiam para ele. A resposta do modelo não tem campo por onde afirmar autoridade — nem o
modelo, nem um adaptador, nem um normalizador. Um teste estrutural
(`TestModelBoundaryCarriesNoAuthority`) avermelha se um campo de taint/autorização voltar a
`ToolInvocation` ou `ModelResponse`. É o fecho **em substância** do DEF-807.

`SeparatePlanes`, `Quarantine`, `PlannerView` e `ControlPlanner` ficam: são os primitivos da
opção A (§5.4). Quando ela entrar, a autoridade das invocações de um `ControlPlanner` continua a
ser cunhada pelo runtime — a partir de a `PlannerView` ser trusted por construção —, e não por um
campo.

### 2.3 Um só rótulo para todos os leitores

O `CallContext.Taint` cunhado aqui é o que o TaintGate impõe, o que as cláusulas
`context.taint != "untrusted"` do bundle Cedar avaliam e o que o classificador de risco (ADR-013)
usa para elevar a sensibilidade. Não há dois rótulos a divergir.

### 2.4 O rótulo reproduz-se no replay e na retoma, sem ser gravado

`ContextAuthority` é uma dobra pura sobre a sequência de segmentos. O motor de replay recalcula-a
sobre o tail que reconstrói (`ReplayedTurn.Authority`), e a retoma do nó (crash-resume e
aprovação, AOS-021) re-hospeda o run desde o turno 1 com o `Goal` do registo de retoma
(objectivo, `plan_input` e memória incluídos) e as respostas registadas — a mesma sequência de
appends, logo o mesmo rótulo. Provado por `TestAOS069_RetomaReproduzOMesmoRotulo` e
`TestAOS069_ReplayReproduzAAutoridadeDoLoop` (replay completo e resume-from-step).

### 2.5 A sequência de produção

- **Fase 0** (antes da release com este ADR): `AOS_PRIVILEGED_CAPS=cap:http.post` — o TaintGate
  armado só para o efeito externo. Armada pelo dono a 2026-09-26.
- **Fase 1** (depois da release): `AOS_PRIVILEGED_CAPS=cap:http.post,cap:fs.read`. Com a opção C o
  `doc_read` de um nó cujo contexto só tem o objectivo passa, e o de um nó que leu um
  `plan_input` é negado com `denied_by=taint`. É um passo do dono/operador, registado no AOS-069.

### 2.6 A cláusula Cedar de `allow_fs_read` fica para a próxima cerimónia de chave

O critério 6 do AOS-363 (toda a regra `permit` traz `context.taint != "untrusted"`, o que exige
re-assinar o bundle) é **adiado** para a próxima cerimónia de chave, depois desta opção — decisão do
dono. Até lá, `cap:fs.read` é coberta pelo TaintGate quando armada (fase 1), e a baseline do gate
`policy-taint` continua a nomear `allow_fs_read`. Este ADR não mexe no bundle nem nas assinaturas.

**Decisão a tomar NA cerimónia (R8, §5):** a região do `allow_http_post`. Hoje ela exige
`resource.region == "eu"` e o `web_post` dos demos declara `eu-west` (as tools alinham-se ao board
desde o AOS-407), pelo que é a REGIÃO — e não o taint — que mata o `web_post` de contexto limpo onde
ele ainda é oferecido (em produção já não é, §2.7). Alinhá-la abre `cap:http.post` a contexto limpo; se se alinhar, a mesma
cerimónia tem de decidir manter `cap:http.post` sempre atrás de confirmação humana (proibir, ou pelo
menos vigiar, L5 para http). `TestAOS069_WebPostEmContextoLimpo_MorreNoPDPPelaRegiao`
(`packages/cmd/aos`) avermelha nesse dia de propósito, para a decisão não passar em silêncio.

### 2.7 O `web_post` sai do catálogo do nó em produção (decisão do dono, 2026-09-26)

Mitigação do achado M1 da revisão adversarial de segurança: com a opção C, um `web_post` pedido em
contexto limpo deixa de morrer no taint, e a barreira passa a ser a região do Cedar — uma
propriedade acidental de um desalinhamento. O dono decidiu retirar o `web_post` do manifesto de
tools de PRODUÇÃO (`deploy/server/model-tools/tools.json`), que passa a oferecer só `doc_read`, **até
à fase 2 / opção A** deste ADR. O manifesto dos demos (`deploy/node/dev-hardened/model-tools/`)
mantém-no. Voltar a oferecer em produção uma tool de `cap:http.post` ou de egress externo é
**decisão explícita**, e `TestAOS069_ManifestoDeProducaoNaoOfereceEgressExterno` (`packages/cmd/aos`)
avermelha até ela estar escrita.

A mudança chega a produção com a próxima release: o `deploy.yml` sincroniza
`deploy/server/model-tools/` sem `--ignore-existing` e o deploy recria o nó. O snapshot do `aos-orq`
(só `doc_read`) não muda, e a conferência do AOS-441 continua a bater — as tools do nó que o
snapshot não nomeia não contam.

## 3. Alternativas consideradas

- **(A) Dual-LLM / CaMeL completo** — um planeador que só vê trusted + handles e um executor que
  manipula dados por handle. É a forma forte do ADR-005 e fica como **destino**, com gatilho (§5.4):
  hoje nenhuma tool de efeito do nó é parametrizada por dados untrusted, e o custo (dois modelos,
  interpretador de handles, reescrita do prompt) não compra nada que a opção C não compre já.
- **(B) Armar `cap:fs.read`, ou a cláusula Cedar, sem mudar a origem do rótulo** — rejeitada por F2:
  com todas as calls untrusted, proibia toda a leitura.
- **(D) Manter um campo de autorização, preenchido por um planeador trusted** (`AuthorizeTrusted`) —
  rejeitada: era exactamente o DEF-807 — autoridade transportada pela fronteira untrusted e
  protegida por convenção.
- **(E) Taint por argumento** (seguir a proveniência de cada argumento da call) — só tem sentido com
  a separação por handle da opção A; sem ela o modelo mistura tudo no mesmo texto.

## 4. Consequências

- Uma tool call pedida sobre um contexto só com o que o humano deu é **trusted**: passa o
  TaintGate, satisfaz as cláusulas Cedar `context.taint != "untrusted"` (ex. `allow_http_post`) e
  não tem a sensibilidade elevada pelo classificador de risco. Antes deste ADR nada disto acontecia
  a nenhuma tool call do modelo.
- **O `web_post` de contexto limpo continua a NÃO ser possível em nenhum deployment committado —
  mas quem o trava deixa de ser o taint.** Em produção a tool **não é oferecida** (§2.7). Onde ainda
  é (os demos dev-hardened), pela ordem da cadeia do nó: morre no **PDP pela região**
  (`allow_http_post` exige `resource.region == "eu"`; a tool declara `eu-west`, e desde o AOS-407
  não se pode pôr `eu` na tool) — `denied_by=policy`. Se a região fosse alinhada, seguir-se-iam o
  **egress default-deny** embebido, a classe SA-ROC **danger** (egress externo, sem reversibilidade
  declarada) que o oráculo de autonomia só liberta com confirmação humana abaixo de L5, e a **falta
  de executor** para a tool. Fixado por `TestAOS069_WebPostEmContextoLimpo_MorreNoPDPPelaRegiao` com
  o bundle de referência e o manifesto dev-hardened (ver §2.6 e R8).
- Um run só pode pedir uma capability privilegiada **antes** de ler conteúdo não-confiável. O
  planeador tem de partir as leituras por nós (§5.3).
- O **veredicto de um verificador** entregue a um consumidor como `plan_input` conta **untrusted**
  no runtime, embora o validador do plano o trate como trusted (F3). É mais restritivo do que a
  admissão do plano: um consumidor privilegiado de um veredicto não usa uma capability armada.
  Nunca é mais restritivo do que o estado anterior (em que toda a call era untrusted). Levar ao nó
  o rótulo efectivo do contrato é uma decisão por tomar, e não é tomada aqui.
- O `AssemblyVersion` não muda: os bytes do prompt são os mesmos; só a autorização muda.
- Demonstrações cuja premissa era «toda a tool call do modelo é untrusted» deixam de a ter (ex.
  `deploy/node/dev-hardened/demo-pdp-taint-gate.sh`, que isola a cláusula Cedar com um `web_post`
  no turno 1).

## 5. Resíduos declarados

1. **R1 — manipulação do texto de um nó que transforma conteúdo untrusted.** Um nó que lê um
   documento e produz um resumo pode ser instruído pelo documento a distorcer o resumo. A opção C
   impede que isso **autorize** uma acção privilegiada nesse nó; não impede que o texto produzido
   seja manipulado. É o limite da opção C, e a razão de a opção A existir.
2. **R2 — o veredicto de forma fechada de um verificador que leu untrusted continua TRUSTED** na
   admissão do plano (F3): é um canal de 1 bit (`pass|fail`) mais identificadores de razão, e um
   documento pode tentar influenciá-lo. Decisão do dono: aceite. (No runtime, ver §4: o veredicto
   entregue como `plan_input` conta untrusted.)
3. **Um nó que já leu um documento não lê outro.** Depois do primeiro `tool_result`, o contexto é
   untrusted até ao fim do run; com `cap:fs.read` armada, a segunda leitura é negada. Duas leituras
   pedidas no **mesmo** turno passam as duas (a autorização é a do Assemble desse turno). O
   planeador tem de partir as leituras por nós.
4. **Gatilho da opção A (dual-LLM/CaMeL).** Quando entrar no nó uma **tool de efeito parametrizada
   por dados untrusted** — um efeito cujos argumentos venham de conteúdo lido (ex. publicar o
   resumo de um documento) — a opção C deixa de chegar: o efeito é, por construção, pedido depois do
   conteúdo. Até lá, `web_post` fica **fora do snapshot** dos planos e **fora do catálogo do nó em
   produção** (§2.7). É o DEF-806, que fica ABERTO
   re-escopado a «efeitos parametrizados por dados untrusted».
5. **`CallContext.Taint` continua a ser uma string no contrato do RM.** Quem constrói a `Call` pode
   escrevê-la; no caminho das tool calls do modelo, quem a constrói é o loop, e o modelo já não tem
   por onde a influenciar. É o resíduo do DEF-807 (FECHADO-RESIDUAL).
6. **Critério 6 do AOS-363 adiado** para a próxima cerimónia de chave (§2.6).
7. **R7 — a fronteira do rótulo é o PRINCIPAL AUTENTICADO, não o conteúdo confiável.** O rótulo é
   função do CAMPO em que o submissor põe o texto: no `POST /runs`, `objective` e `system` são
   trusted e só `inputs` é untrusted (`packages/cmd/aos/api.go`, construção do `Goal`). Conteúdo
   colado no `objective` — um email com uma injecção — comanda no turno 1. No caminho do plano, o
   objectivo de cada nó é escrito pelo `LLMDecomposer` a partir do texto livre do `POST /plans`,
   sem revisão humana quando o drenador corre sem operador. Impacto hoje baixo (medido na revisão
   adversarial): o mandato do drenador só concede `cap:fs.read` e o snapshot só tem `doc_read`.
   O que trava um `web_post` injectado no objectivo não é o taint: é a região (§4), o egress e a
   confirmação de uma acção danger. Documentado por desenho em
   `TestAOS069_InjeccaoNoObjectivoPassaAClausulaDeTaint_R7`.
8. **R8 — a região do `allow_http_post` é, hoje, a barreira do `web_post` de contexto limpo** (§4).
   Decisão a tomar na cerimónia do AOS-363 critério 6 (§2.6): a região, e manter `cap:http.post`
   sempre atrás de confirmação humana (proibir/vigiar L5 para http). Em produção o risco foi
   mitigado retirando o `web_post` do catálogo (decisão do dono, §2.7); R8 continua a valer para
   qualquer deployment que o ofereça, e para o dia em que volte.
9. **A autoridade da retoma depende da integridade do registo de retoma.** O rótulo é re-dobrado do
   `Goal` que o `ResumeRecord` guarda (objectivo, `inputs`, memória) e das respostas registadas; a
   retoma do nó NÃO confere o `prompt_hash` re-materializado contra o gravado. Um registo que
   perdesse os `inputs` re-autorizaria trusted o que foi untrusted. Detector forense, opt-in e
   determinista: `Options.VerifyAuthority` do motor de replay compara `ReplayedTurn.Authority` com
   o taint SELADO nos eventos `tool.call.*` de cada turno e localiza a diferença com
   `Reason="authority"` (`TestAOS069_AncoraAuthority_DetectaTaintSeladoDivergente`). Opt-in porque
   uma trajectória gravada antes deste ADR tem todas as calls seladas untrusted e divergiria em todo
   o turno de contexto limpo. Não está no caminho da retoma — pô-lo lá é trabalho por decidir.

## 6. Conformidade / enforcement

- `packages/kernel/agent-runtime/context_authority_test.go` — rótulos por segmento, monotonia,
  equivalência janela↔dobra, os cenários `n1`/`n2` com `cap:fs.read` armada, memória fail-closed,
  correcção que não lava, retoma que reproduz o rótulo.
- `packages/kernel/agent-runtime/taint_plane_test.go` — `TestModelBoundaryCarriesNoAuthority` e a
  integração RT↔RM (bloqueio com contexto untrusted, permit com contexto trusted, span `aos.taint`).
- `packages/kernel/agent-runtime/replay/aos069_autoridade_replay_test.go` — o replay reproduz a
  autoridade que o RM viu.
- `packages/security-tests/plan_input_injection_test.go` — gate `security`: a bateria do corpus
  como `plan_input`, pelo loop real, não produz nenhuma call privilegiada permitida (fase 0 e fase
  1), com controlo de contexto limpo e meta-teste com o gate desligado.
- `packages/cmd/aos/aos069_web_post_contexto_limpo_test.go` — o manifesto de produção não oferece
  nenhuma tool de `cap:http.post` nem de egress externo (§2.7); onde o `web_post` é oferecido, morre
  em contexto limpo no PDP pela região (R8); e a injecção no objectivo passa a cláusula de taint
  (R7).
- `packages/kernel/agent-runtime/replay/aos069_autoridade_replay_test.go` — a âncora opt-in
  `authority` (resíduo 9).
- Banner de arranque (`posture_banner.go`): a linha ATIVA do taint declara de onde vem o rótulo.

## 7. Referências

`specs/EPIC-07_Seguranca_Isolamento.md` §AOS-069 · `specs/EPIC-25_Remediacao_Auditoria_GOV_OBS.md`
§AOS-363 · `docs/governance/REGISTO-Deferimentos.md` (DEF-806, DEF-807) ·
`docs/adr/ADR-027-execucao-dos-nos-do-plano-como-runs-do-no.md` §2.4 ·
`tecnica/07_Seguranca_Isolamento.md` §6
