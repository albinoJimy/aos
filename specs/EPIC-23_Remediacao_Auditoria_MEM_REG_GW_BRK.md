# EPIC-23 — Remediação dos defeitos da auditoria adversarial MEM/REG/GW/BRK

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — Remediação dos defeitos apurados na auditoria adversarial dos serviços de plataforma |
| Versão | 1.0 |
| Data | 2026-09-04 |
| Classificação | Documento de Referência — **Proposta** |
| Documento-fonte | `analises/11_Auditoria_MEM_REG_GW_BRK_Adversarial.md` (§3, §5, §6, §8) |
| Documentos relacionados | `docs/governance/REGISTO-Deferimentos.md`, ADR-006, ADR-008, ADR-012, ADR-017, ADR-023, `tecnica/05`, `tecnica/06`, `tecnica/07`, `specs/EPIC-04`, `EPIC-05`, `EPIC-06`, `EPIC-07`, `EPIC-20` |
| Âmbito | `packages/platform/registry` (incl. `mcp/`), `packages/platform/model-gateway`, `packages/platform/broker`, `packages/platform/memory`, `packages/cmd/aos`, `deploy/` |

---

## 0. Porque este epic existe, e porque inclui o que a EPIC-22 excluía

`analises/11` produziu 32 hipóteses-defeito, atacou-as com o ónus da prova invertido em cinco
refutações independentes, e mediu o resultado no nó real e no pipeline de release. **Sobreviveram
16**; nove caíram por evidência falsa e sete eram deferimentos já registados.

A EPIC-22 cobriu só os defeitos **activos** do plano de controlo, e deixou catorze latentes de fora
com um argumento correcto: o ORQ e o SCH estão fora do grafo de build **por decisão ratificada**
(ADR-018, ADR-023, com guard-test), logo corrigi-los não muda o binário que se instala.

**Esse argumento não se transporta para aqui, e é essa a descoberta central da `analises/11`.**
Procurado em todo o `docs/adr/`: nenhum ADR declara MEM, REG, GW ou BRK deliberadamente
não-compostos. E a cobertura por deferimentos é grosseiramente desigual — em 107 entradas do
`REGISTO-Deferimentos.md`, o GW tem 13 menções, o BRK 9, o REG 3, e o MEM **uma**, que é a DEF-302,
sobre outra coisa.

Para o plano de controlo, «latente» significa *deliberadamente adiado*. Para os serviços de
plataforma significa **inacabado**. Por isso este epic inclui os latentes: não há decisão que os
proteja, e o ticket que fecha essa assimetria (AOS-326) é ele próprio parte da remediação.

| Eixo | Tickets |
|---|---|
| Supply chain do REG | AOS-320 |
| Contabilização de custo no GW | AOS-321 |
| Custódia de credenciais e de chaves (BRK, DSAR) | AOS-322, AOS-323, AOS-324, AOS-327 |
| Veracidade dos artefactos e governação | AOS-325, AOS-326 |

### 0.1 Ordem sugerida

| Prioridade | Tickets | Racional |
|---|---|---|
| **P0** | AOS-320, AOS-321 | Um esvazia um pilar declarado de supply-chain; o outro é fail-open do burn-down no binário entregue |
| **P1** | AOS-323, AOS-326 | Um canal de segredos sem TLS obrigatório, e a raiz de governação da assimetria do §0 |
| **P2** | AOS-322, AOS-324, AOS-325, AOS-327 | Guard-rail da confirmação de custódia; pré-requisito do wiring do broker; reconciliação documental; regra de alerta |

### 0.2 Tabela-resumo

| Ticket | Defeito | P | Alcance | Estado |
|---|---|---|---|---|
| AOS-320 | O digest de um `mcp_server` é uma constante da classe de egress — três valores para todo o universo | P0 | latente | **implementado** |
| AOS-321 | Uma resposta 200 sem `usage` é indistinguível de uma chamada de custo nulo | P0 | **nó** | **implementado** (residual: a marca não cruza a fronteira GW→RT) |
| AOS-322 | A postura de confirmação do *crypto-shred* não é declarada, e nada obriga uma custódia nova a escolher | P2 | endurecimento | **implementado** (enunciado original FALSIFICADO — ver ticket) |
| AOS-323 | O canal do broker para o Vault aceita `http://` e o token nunca é renovado | P1 | **nó** | **implementado (parcial)** — metade do enunciado era falsa; 1 AC deferido |
| AOS-324 | A troca de credenciais não impõe nem exercita o eixo *Provider* | P2 | latente | **implementado** — imposição por CONFIGURAÇÃO; sob `unset` o defeito persiste, fechável por config |
| AOS-325 | Cinco declarações de estado que já não são verdade, e uma contradição interna | P2 | **nó** | **implementado (parcial)** — 7 declarações corrigidas (uma é do AOS-320); AC4 (mecanismo anti-recaída) por fechar em DEF-814 |
| AOS-326 | A não-composição de MEM e REG não está registada em ADR nem em `DEF-NNN` | P1 | — | **implementado** (DEF-811/812 + qualificação dos §2 do EPIC-04/05; o ADR fica ao dono) |
| AOS-327 | A pendência de *shred* não tem regra de alerta, e a série desaparece com o processo | P2 | **nó** | **implementado** (RB-06; âmbito cortado a meio) |

**Mais três, que não vêm da auditoria mas da validação DELA** (§0.3), separados de propósito: **AOS-328** (guard-rail da confirmação de crypto-shred, `DEF-813`), **AOS-329** (o gate que verifica o ESTADO do ticket citado numa declaração, `DEF-814` — o único dos três que ataca a causa-raiz) e **AOS-330** (o namespace da política do broker vs o do path do Vault, `DEF-815`).


### 0.3 Validação adversarial — o que ela encontrou depois de os oito estarem «implementados»

Cinco revisores independentes, sobre eixos disjuntos, com o diff e os critérios mas **sem** o
raciocínio de quem implementou. As mensagens de commit foram-lhes dadas como **alegações a
verificar**, não como verdade. O resultado é o argumento mais forte a favor desta fase: **este epic
reproduziu, em três sítios, o defeito que veio corrigir.**

| # | Achado | Eixo | Estado |
|---|---|---|---|
| A1 | **O digest endurecido não protege o caminho quente.** A revalidação por chamada chaveia por `call.ToolID` = a entrada `kind=tool`, cujo contrato não levava âncora. Um servidor que mudasse de endpoint deixava todas as suas tools byte-a-byte idênticas | REG | **CORRIGIDO** — a tool leva a âncora do servidor; `TestAncoraChegaAsEntradasTool`, com mutação a provar não-vacuidade |
| A2 | **O Vector 8 não tocava no código sob teste.** A fixture inventava a sua forma ancorada e `supplychaintests` nem importava `registry/mcp`; teria ficado verde se a âncora deixasse cair transporte e endpoint | REG | **CORRIGIDO** — usa `mcp.DigestAncorado`; mutação da âncora põe o vector VERMELHO |
| A3 | **`{"usage":{}}` e `usage` só com `total_tokens` continuavam a sair grátis.** O critério era a presença do OBJECTO, não dos contadores que o cálculo lê | GW | **CORRIGIDO** — `Definido()` exige `PromptTokens > 0`, o critério do `cache_sli`; dois testes novos |
| A4 | **Um teste de controlo fixava o defeito.** `TestUsageComZerosExplicitosContinuaAContabilizar` afirmava que zeros explícitos «são um facto medido» | GW | **CORRIGIDO** — reescrito e renomeado, mantido como registo de como o defeito sobreviveu |
| A5 | **A omissão de `gen_ai.usage.*` não tinha teste.** A mutação que a revertia deixava o módulo verde | GW | **CORRIGIDO** — `TestSpanOmiteContadoresQuandoIndefinido` |
| A6 | **A defesa server-side do broker decide sobre dados do pedido.** Lê classe e autoridade do envelope serializado ANTES da mediação; o gate lê de `call.Principal`, que o RM reescreve. Fontes diferentes | BRK | **PARCIAL** — ver AOS-324 |
| A7 | **O banner de crypto-shred mentia para uma custódia injectada.** Dizia «com o vault de REFERENCIA isto é CORRECTO» num ramo que dispara para qualquer custódia sem a porta | Decl | **CORRIGIDO** — três ramos, com o terceiro a dizer «NÃO É CORRECTO» |
| A8 | Quatro declarações novas imprecisas ou falsas: a abertura do `tecnica/06` afirmava o que o resto negava; o banner do MEM contradizia a própria frase e omitia a 4.ª classe; o do REG dizia que o `registry` não está no grafo de build (está); e escapara um **quarto** sítio da alegação do AOS-265 | Decl | **CORRIGIDO** |
| A9 | Dois passos do RB-06 mandavam o operador na direcção errada: a causa 1 era a causa 3 disfarçada, e um Vault selado levanta **os dois** sinais, não um | Decl | **CORRIGIDO** |
| A10 | A alegação de que o guard AOS-285 tranca «os dois WAL» é falsa na configuração que a nota anota: com NATS, só o WORM é trancado | Decl | **CORRIGIDO** no compose e no RB-06 |

**Registados, e agora TODOS com ticket.** A primeira versão deste parágrafo listava oito achados
«sem código nesta ronda». Sete viraram ticket; o oitavo foi julgado e não é defeito:

| Achado | Ticket |
|---|---|
| O *path folding* do Vault KV v2 faz `" "`, `"*"` e `"a/b"` colidirem no mesmo path | **AOS-330** (`DEF-815`) — **implementado** |
| O provedor autorizado não é amarrado ao `ResourceValue` | **AOS-331** — **implementado** |
| A postura do eixo provider não aparece no banner, e a negação não a sela | **AOS-332** — **implementado** |
| `CheckSecureTransportURL` aceita credenciais embutidas e o banner imprime o endereço cru | **AOS-333** — **implementado** |
| Nada exige `ManifestDigest` não-vazio para `kind=mcp_server` | **AOS-334** |
| `ClassifyContract` devolve sempre `ChangeNone` para `mcp_server` | **AOS-335** |
| O custo indefinido não atravessa a fronteira GW→RT, e o `ErrBurndownNoUsage` não apanha um run misto | **AOS-336** — **implementado** |
| O cliente Vault do broker ecoa o endereço nos erros (um ramo cru) — achado na revisão do AOS-333 | **AOS-337** — **implementado** |
| A quebra do AOS-333 deixa o verificador de attestation sem caminho para basic-auth | **AOS-338** — **implementado** |

**O oitavo NÃO tem ticket, e a decisão fica escrita:** a sonda de segunda passagem do AOS-321 custa
+128% de CPU no *parse* (44,4 µs/op contra 101,1 µs/op num corpo de ~11 KB, medido por benchmark).
É um custo real, mas é o preço declarado de uma alternativa pior — uma cópia da struct de resposta
com o campo em ponteiro apodreceria em silêncio à primeira alteração de campo. É *trade-off*
justificado, não dívida; abrir ticket para o registar seria transformar uma decisão em pendência.

**O que a validação confirmou:** os goldens de regressão são genuinamente pré-mudança (um revisor reimplementou a canonicalização anterior e recomputou os oito valores); não existe colisão na canonicalização, atacada a sério; `AcquireInProcess` e `Inject` não permitem que um handle de um provedor resolva material de outro; e as quatro mutações do eixo provider avermelham pelo motivo certo.


---

## AOS-320 — O digest de um `mcp_server` é uma constante da classe de egress

### Contexto

O REG promete «catálogo versionado de skills/tools/MCP com **pin + hash + assinatura**»
(`_BRIEF.md` §2). Para entradas `kind=mcp_server` o pilar do meio está vazio.

`mcp/host.go:283-290` publica a entrada do servidor com `Contract: domain.Contract{Egress: egress}`
— só a classe. O `canonicalContract` (`registry/digest/canonical.go`) serializa
`kind|egress|InputSchema|OutputSchema|scopes`, e um `mcp_server` não tem schemas: os schemas das
tools descobertas vão para entradas **separadas** `kind=tool`, com `ID = serverID+"/"+toolName`
(`host.go:296-304`). O endpoint do servidor vai para `Provenance.Origin` (`serverOrigin`,
`host.go:305`), campo que não entra no digest nem, por consequência, na assinatura.

O resultado foi calculado contra o código: existem **três** digests possíveis para todo o universo
de servidores MCP — um por classe de egress — e `stage` coage egress inválido para `internal`
(`host.go:270`), estreitando ainda mais. Uma assinatura sobre `(id, version, digest)` de um
`mcp_server` autentica `(id, version, classe de egress)` e mais nada: substituir o binário ou o
endpoint por trás de `mcp.fs@1.0.0` preserva digest e assinatura válidos.

`tecnica/05` §3 (linha 62) diz «digest = SHA-256 do conteúdo canonicalizado: schema, **binário** ou
manifesto», e §4 (linha 119) que «o binário do servidor é artefacto de supply-chain —
pin+hash+assinatura obrigatórios». Nenhum digest de binário ou manifesto entra numa `Entry`.

A peça que fecharia isto está a meio caminho e **declarada como reservada há dois tickets**:
`mcp/protocol.go:147` diz «`Digest` RESERVADO (AOS-047). Vazio em AOS-046», e o `Handshake`
(`host.go:190-197`) devolve-o vazio com o mesmo comentário. O AOS-047 **entregou**
`digest.DigestJSON` e `digest.DigestBytes` — que não têm um único chamador não-teste em todo o
repositório.

Mitigação parcial que existe e não basta: `toolset/frozen.go:408 computeHash` inclui `s.MCPServer`
(a origem) no hash do conjunto congelado. Mas `toolset.Expectation` transporta só
`{ID, Version, Kind, Digest}` e é isso que a revalidação por chamada compara — **a origem não é
revalidada por chamada**.

Porque sobreviveu: **nenhum teste do repositório assere o digest de uma entrada `mcp_server`.** Os
vectores 1 e 3 da suite adversarial AOS-054 usam `domain.KindTool`.

### Critérios de Aceitação

- [ ] O `CapabilityManifest.Digest` deixa de ser reservado: o `Handshake` devolve um digest do
      manifesto de capacidades, computado com `digest.DigestJSON` sobre a forma canónica
- [ ] O `Entry.Digest` de uma entrada `kind=mcp_server` deriva desse digest de manifesto — dois
      servidores com a mesma classe de egress e manifestos diferentes têm digests diferentes
- [ ] Um teste que construa dois `mcp_server` com a mesma classe de egress e prove que os digests
      **divergem**; e um que prove que o mesmo manifesto reproduz o mesmo digest (determinismo)
- [ ] A suite AOS-054 ganha um vector de rug-pull sobre `kind=mcp_server` — substituição de
      endpoint/manifesto com `(id, version)` inalterados — que **bloqueia**
- [ ] O comentário de `mcp/protocol.go:147` deixa de citar AOS-047 como pendente
- [ ] `tecnica/05` §3/§4 e o texto do §«manifesto» ficam verdadeiros sobre o que o digest cobre, ou
      são emendados para o que cobre de facto

### Estado

**IMPLEMENTADO.** P0. Latente enquanto `mcp.NewHost` não tiver chamador (ver AOS-326), mas o defeito
estava materializado no código e a correcção não dependia do wiring.

`Contract.ManifestDigest` é escrito **por último e só quando não-vazio** nas duas gerações de
hashing, o que preserva byte-a-byte os digests de `tool`/`skill` — fixado por
`TestGoldenDigests_ToolSkill_NaoRegridem`, que congela oito valores medidos antes da alteração. Sem
essa prova não se saberia se o pin de tudo o resto tinha sido partido.

**O que entra no digest do manifesto:** tools (nome, descrição, schema sanitizado), resources,
versão de protocolo, a marca de descoberta incompleta, e uma tag de versão da forma canónica —
tudo ancorado ao par (transporte, endpoint) da ligação, que é **local e não-forjável pelo servidor**.
A descrição entra porque é o *payload* do tool-poisoning: ignorá-la deixaria reescrever a instrução
inteira por trás de um pin inalterado. **`ServerInfo` fica de fora**, e a exclusão está fixada por
teste: é auto-declarado, trivialmente copiável numa substituição, e sem limite de churn. Nomes de
tool ou URIs duplicados tornam a forma ambígua e são recusados fail-closed.

**Vector 8** (`TestVector8_MCPServerRugPull_Blocked`) substitui endpoint e manifesto com
`(id, version)` inalterados e **re-assina com a chave legítima** — a revalidação bloqueia. O
meta-teste prova que com o contrato pré-AOS-320 a mesma substituição **passa**. Ambos entraram na
lista `REQUIRED` de `supplychain.sh`, que era o fecho que faltava: sem isso não estavam sob a
protecção anti-vacuidade do gate.

Gate `supplychain` verde, com `"mcp_server_rug_pull_blocked":true` no relatório agregado.
`tecnica/05` §3/§4 e o texto do manifesto passam a dizer o que o digest cobre — **e o que não
cobre**: os bytes do executável não entram, e a defesa desse eixo é a do artefacto (ADR-017/SBOM),
não a do catálogo.

**Residual declarado:** `semver.ClassifyContract` não considera `ManifestDigest`, pelo que uma
mudança de manifesto não é classificada como bump — deixado intacto por mudar a semântica de
AOS-052.
---

## AOS-321 — Uma resposta 200 sem `usage` é indistinguível de uma chamada de custo nulo

### Contexto

`port/normalize.go:107-113` (`UnmarshalChatResponse`) é um `json.Unmarshal` nu: uma resposta 200 de
um provedor que **omita** o objecto `usage` produz um `port.Usage{}` zerado, sem erro. Esse zero
desce por `recordCost` (`gateway.go:624-634`), que não distingue «zero tokens» de «contagem
ausente», e `costForTokens` (`metering/cost/cost.go:270-284`) devolve `0, nil` para `tokens == 0`.

O custo zero acaba escrito no span, no agregado por run e por árvore, e no evento durável
`turn.recorded`. É fail-open do burn-down que o ADR-008 exige — um provedor que não reporte tokens
sai **grátis** — e contradiz o próprio comentário de `recordCost`.

Duas precisões que a refutação estabeleceu e que delimitam o ticket:

- **Não** é «custo zero por falta de preço»: essa via é impossível, porque `ErrNoPrice`
  (`cost.go:203-210`) dispara antes de qualquer token ser somado, e
  `TestGateway_Cost_NoPrice_FailClosed` prova-o. O ramo que sobrevive é exclusivamente o de *usage
  ausente*.
- A disciplina correcta **já existe no mesmo pacote**: `metering/cache_sli/cache_sli.go:158` trata
  `PromptTokens == 0` como **SLI indefinido — nunca 0**. O que falta é aplicá-la ao custo.

Eixo vizinho, do mesmo ticket AOS-259: `Gateway.Embeddings` (`gateway.go:455-500`) nunca escreve
`resp.Usage.CostMicroUSD`, ao contrário do `Chat` (`gateway.go:341`). O caminho de *streaming*
declara o seu limite em comentário (`gateway.go:424-432`); o de `Embeddings` não declara nada — e a
ausência de declaração é o defeito, mais do que o campo em falta.

### Critérios de Aceitação

- [ ] `UnmarshalChatResponse` (ou `recordCost`) distingue `usage` **ausente** de `usage` com zeros —
      a decisão fica registada em tipo ou sentinela, não numa convenção de valor
- [ ] Uma resposta 200 sem objecto `usage` **não** produz um custo de 0 silencioso: ou falha
      fail-closed, ou marca a amostra como indefinida no molde de `cache_sli.go:158`
- [ ] Um teste que force `port.Usage{}` numa resposta de provedor e exija o comportamento acima —
      hoje não existe nenhum
- [ ] O agregado por run/árvore e o evento `turn.recorded` não contabilizam uma amostra indefinida
      como zero
- [ ] `Gateway.Embeddings` escreve `resp.Usage.CostMicroUSD` como o `Chat`, ou declara o seu limite
      no molde do comentário do *streaming*

### Estado

**IMPLEMENTADO.** P0. A distinção vive no tipo (`port.Usage.Ausente` + `Definido()`, com polaridade
deliberada: o zero do campo é «definido», e só o desserializador do wire e o agregador de streaming
marcam ausência). Fail-closed no caminho síncrono via `modelgateway.ErrUsageAusente`; indefinido —
não agregado, não emitido, span anotado — no streaming, onde o conteúdo já foi entregue. `Embeddings`
passa a escrever `resp.Usage.CostMicroUSD`. Sete testes, incluindo três de controlo e um de limite
declarado; prova de mutação executada.

**RESIDUAL FECHADO PELO AOS-336 — o AC4 deixou de ser condicional.** O parágrafo abaixo descreve
o estado à data deste ticket; a marca passou entretanto a atravessar a fronteira GW→RT
(`agentruntime.Usage.Ausente`), o `turn.recorded` regista-a, e o `ErrBurndownNoUsage` passou a
apanhar o run misto a partir do primeiro turno não medido em vez de exigir o run inteiramente cego.

~~**RESIDUAL NOMEADO — o AC4 vale enquanto houver recorder composto.**~~ `translateResponse`
(`packages/platform/model-gateway/runtime_adapter.go:163-169`) copia só os três campos numéricos
para `agentruntime.ModelResponse`, que não tem campo para «indefinido» — a marca **não atravessa a
fronteira GW→RT**. Num deployment em que a tabela de preços não cubra o par configurado,
`parseModelPricingFromEnv` devolve recorder nil, `recordCost` retorna cedo em `if g.cost == nil`, e
o `turn.recorded` volta a receber `cost_micro_usd: 0` para uma chamada não medida. Fechar isto exige
um campo novo em `agentruntime.ModelResponse` — outro módulo, fora do `FILES_IN_SCOPE` deste ticket.
Mitigação em vigor: `ErrBurndownNoUsage` (`packages/cmd/aos/burndown_ledger.go:53`) recusa um
burn-down cuja dimensão decisiva somou zero, o que apanha o caso ao fim de N turnos em vez de ao
primeiro. **Fica por abrir como ticket próprio.**

**DECISÃO DE REVISÃO REGISTADA.** A revisão adversarial levantou que o fail-closed síncrono descarta
uma resposta já paga e convida a dupla cobrança num retry, e propôs a alternativa que este ticket
também admitia («marcar indefinido» também no síncrono). **A objecção foi retirada, e o fail-closed
mantém-se.** Deixar a chamada passar como indefinida põe o run exactamente no estado que o
`ErrBurndownNoUsage` foi criado para denunciar — «N eventos `turn.recorded` com `input_tokens=0` …
o run queimava o tecto inteiro» — e o burn-down só o apanharia N turnos depois, contra um turno do
fail-closed. A perda é de uma chamada, não do tecto inteiro.

---

## AOS-322 — A postura de confirmação do *crypto-shred* não é declarada, e nada obriga uma custódia nova a escolher

> **ENUNCIADO ORIGINAL FALSIFICADO — 2026-09-04, na discovery da execução.** Este ticket dizia:
> «o `/readyz` nunca fica vermelho por *crypto-shred* pendente com a custódia de referência», e
> tratava-o como defeito alcançável hoje. **Não é.** A verificação está abaixo e o ticket foi
> reescrito para o que resta, que é menor e de outra natureza. O registo fica porque um ticket
> que muda de premissa sem o dizer é a doença que a `analises/11` §5 documenta.

### Porque o enunciado original caiu

Os factos citados eram todos verdadeiros — e a conclusão que se tirou deles não.

`audit.InMemoryKeyVault` de facto não implementa `readinessProber` nem `shredPendingReporter`
(`packages/platform/audit/keyvault.go:54,70,81` — tem exactamente três métodos). E o `/readyz` de
facto só sonda a custódia por asserção de tipo (`packages/cmd/aos/api.go:1109,1138`).

O que faltou seguir é **como nasce uma pendência**. `packages/control-plane/governance/dsar/flow.go:275-284`
só sela `EventShredUnconfirmed` se `f.confirmer != nil`, e o confirmador vem de
`confirmadorDeShredDe(dsarVault)` (`packages/cmd/aos/shred_confirmador.go:23-29`), que devolve
`nil` quando a custódia não implementa a porta interna `shredConfirmer`. Com o vault de referência,
portanto: **não há confirmador, não há evento `shred_unconfirmed`, e não há pendência que reportar.**

E isso está certo, porque `audit.InMemoryKeyVault.Delete` (`keyvault.go:80-86`) é um `delete()` num
mapa sob mutex — **não tem como falhar**. A porta `dsar.ShredConfirmer`
(`packages/control-plane/governance/dsar/confirmador.go:21-34`) diz-o em texto: «um vault em memória
destrói e sabe-o; o Vault Transit relê a chave e exige 404; um KMS de terceiros pode não expor a
pergunta».

As três ausências — confirmador, `readinessProber`, métrica — são **coerentes entre si e
correctas**: uma custódia que não pode falhar a destruir não tem pendência para declarar. O
`/readyz` não fica vermelho porque não há nada pendente, não porque o alarme esteja partido.

### O que resta, e é de outra natureza

A opcionalidade da `ShredConfirmer` é **fail-open para qualquer custódia que não a implemente**.
Hoje isso é inofensivo porque só existem duas custódias e a distinção é consciente: a de referência
não pode falhar, a do Vault implementa a porta. Mas nada torna essa escolha obrigatória nem
visível:

1. **Nenhum guard-rail.** Uma custódia futura — o «KMS de terceiros» que a própria porta antecipa —
   que possa falhar a destruir e não implemente `shredConfirmer` faz a cadeia selar
   `dsar.key_destroyed` sobre uma irrecuperabilidade que ninguém verificou. É exactamente o defeito
   que a porta foi criada para fechar (`confirmador.go:15-19`), reaberto pela via da omissão.
2. **Nenhuma declaração de arranque.** O nó não diz, no banner de postura, qual custódia está
   composta nem se a confirmação está armada. Um operador não consegue distinguir «não há pendências»
   de «esta custódia não sabe responder» — e as duas leituras do `/readyz` verde são muito
   diferentes.

### Critérios de Aceitação

- [ ] O banner de postura declara, no arranque, **qual** custódia de KEK está composta e **se a
      confirmação de destruição está armada** — no molde das linhas que já declaram o credential
      broker (`posture_banner.go`), e sem imprimir valor nenhum
- [ ] Uma custódia que não implemente `shredConfirmer` é uma escolha **registada**, não um silêncio:
      ou o composition-root recusa compor uma custódia não-confirmadora sob `AOS_MODE=production`,
      ou a ausência fica declarada no banner com o seu porquê (a escolha faz parte do ticket e é
      justificada por escrito)
- [ ] Um teste que prove que, com a custódia de referência, o `/readyz` fica **verde** e a razão é
      «não há pendência possível» — fixando por teste o comportamento que este ticket concluiu ser
      correcto, para que uma alteração futura não o mude por acidente
- [ ] O comentário de `api.go:1135-1137` («o vault in-memory de referência NÃO [implementa] — nesse
      caso o /readyz não sonda a custódia, a KEK em memória está sempre disponível») ganha a segunda
      metade do raciocínio: não é só que está disponível, é que a sua destruição não pode falhar

### Estado

**IMPLEMENTADO.** ~~P1~~ **P2** — reclassificado com o enunciado, que foi falsificado antes de se
escrever uma linha de código.

O banner de arranque declara agora se a confirmação de destruição está **ARMADA** ou **NÃO ARMADA**,
derivando de `confirmadorDeShredDe(dsarVault) != nil`. A linha do caso não-armado diz explicitamente
que, com o vault de referência, isso é **correcto** e que um `/readyz` verde significa «nada a
confirmar», não «confirmado» — que era precisamente a leitura que faltava.

O comentário de `api.go` ganha a segunda metade do raciocínio: não é só que a KEK em memória está
sempre *disponível*, é que a sua *destruição não pode falhar*. Sem essa metade, a leitura natural da
nota era que o alarme estava mudo; está antes sem nada que reportar.

`aos322_confirmacao_shred_test.go` fixa o raciocínio, não o sintoma: assere as três ausências
(confirmador, `readinessProber`, `shredPendingReporter`) **e a premissa que as justifica** — que o
`Delete` do vault de referência apaga mesmo e é idempotente. Se essa premissa deixar de valer, as
ausências passam a ser um buraco, e é este teste que o denuncia. Um segundo teste prova o corolário
onde o operador o lê: `/readyz` a 200 com a custódia de referência.

`DEF-813` regista a forma do risco que sobra — a opcionalidade da porta não distingue «esta custódia
não precisa de confirmar» de «esta custódia devia confirmar e não confirma» — com gatilho nomeado: a
composição de uma terceira custódia. A nota `N-DEF-813` explica porque não se cria ticket hoje: não
há defeito, há uma forma de risco que nenhuma composição real corre.

---

## AOS-323 — O canal do broker para o Vault aceita `http://` e o token nunca é renovado

### Contexto

`broker.NewVaultKVv2` (`packages/platform/broker/internal/vault/kvv2.go:75-77`) delega a validação
do esquema do endereço em quem chama, e o único chamador — `parseBrokerVaultFromEnv`
(`packages/cmd/aos/broker_vault_env.go:53-84`) — não a faz. Um `AOS_BROKER_VAULT_ADDR` com
`http://` é aceite, o nó arranca, e o banner declara-o «CONFIGURADO (KV v2 @ http://…)». O
`X-Vault-Token` viajaria em claro.

O token é lido uma vez de ficheiro montado (`kvv2.go:102`) — o que está certo: material privado
nunca por variável de ambiente, no padrão de `AOS_ISSUER_KEY_PATH` — e **nunca é renovado**. Existe
um `Ready()` (`kvv2.go:234`), mas não pertence à porta `vault.Client` (`internal/vault/vault.go:80-82`),
que tem uma única função, `Fetch`. Por isso o `/readyz` não o sonda.

Essa última observação muda a natureza do ticket: **a porta não tem sítio onde pôr saúde, rotação
de token ou fecho.** Não se corrige em `kvv2.go` — corrige-se no contrato.

Mitigação de facto: hoje nenhum `Fetch` é emitido, porque `cfg.BrokerVault` só alimenta um banner
(ver AOS-326). O token não viaja. Mas o nó arranca e **declara-se configurado**, que é a metade
alcançável hoje.

Nota de âmbito: a `DEF-216` cobre *dynamic secrets*; não cobre TLS nem renovação de token.

### Critérios de Aceitação

- [ ] `parseBrokerVaultFromEnv` recusa fail-closed um endereço que não seja `https://`, com erro
      atribuível no molde de `ErrBadBrokerVault` — e um escape explícito para desenvolvimento, se
      existir, é nomeado e declarado no banner
- [ ] A porta `vault.Client` ganha uma superfície de saúde, e o `/readyz` sonda o Vault do broker
      como já sonda o do DSAR
- [ ] A política de renovação do token fica declarada: ou o cliente renova, ou o banner declara que
      o token tem uma data de morte que ninguém vê chegar (no molde de `tokenOpaco` em
      `vaultkeyvault.go`)
- [ ] Um teste que prove a recusa de `http://` e outro que prove a sonda de saúde

### Estado

**IMPLEMENTADO (parcial, com um AC deferido e a razão escrita).** P1.

> **METADE DO ENUNCIADO ERA FALSA — apurado na implementação.** Este ticket dizia que nenhum dos
> dois Vaults validava o esquema do endereço. **O Vault da KEK já validava**, desde AOS-249:
> `parseVaultDSARFromEnv` chama `integration.CheckSecureTransportURL` e recusa com
> `ErrInsecureVaultDSARAddr` (https exigido, http só em loopback). A auditoria procurou a validação
> em `vaultkeyvault.go` e ela vive no parser — daí a leitura errada.
>
> O defeito real era a **assimetria**: o Vault do BROKER, endereçado pela mesma família de
> variáveis, a transportar o mesmo tipo de material, no mesmo binário, não validava nada. O mesmo
> processo recusava num sítio o que aceitava no outro.

`parseBrokerVaultFromEnv` passa a chamar **o mesmo helper**, com sentinela própria
(`ErrInsecureBrokerVaultAddr`). Usar o mesmo helper é deliberado: um segundo critério, ainda que
equivalente hoje, divergiria à primeira alteração — que é exactamente como esta assimetria nasceu.

Três testes, e o terceiro é o que interessa: `TestAOS323_MesmoCriterioNosDoisVaults` amarra a
propriedade que o ticket existe para garantir — o mesmo endereço tem o mesmo veredicto nos dois
eixos. Um teste por eixo provaria cada guarda em separado e deixaria a assimetria sem cobertura.
O controlo fixa que o loopback em http continua aceite nos dois, que é o padrão de dev documentado.

A **política de renovação do token** fica declarada no banner: lido uma vez do ficheiro montado e
**nunca renovado**, ao contrário do Vault da KEK que sonda e renova (AOS-249) — sem efeito enquanto
nenhum `Fetch` é emitido, e a fechar no dia do wiring.

**AC deferido, com razão:** não se acrescentou superfície de saúde à porta `vault.Client` nem sonda
no `/readyz`. O `Node` não transporta o cliente do broker — só o `bootstrap` o vê, para escolher a
linha de banner — e sondar a saúde de um cliente que **não emite pedido nenhum** seria pôr o
`/readyz` a depender de algo de que o nó não depende. O `kvv2` já tem `Ready()`; o que falta é o
consumidor, e o consumidor é o wiring. Fica amarrado ao `DEF-218`, junto com a renovação do token.
---

## AOS-324 — A troca de credenciais não impõe nem exercita o eixo *Provider*

### Contexto

O `ScopeGate` do broker avalia **só** a capability: `scope.go:82` não olha para `Provider`,
`Region` nem `Resource`. Mas são exactamente esses campos, **vindos do pedido**, que formam a chave
do material no Vault: `exchange.go:242` monta `vault.Key{Provider, Region, Capability}`.

E a decisão de reutilizar `cap:http.post` para toda a troca (`capability.go:35`) torna a capability
**constante**, deixando o provider como único discriminante — que ninguém valida. Um principal
autorizado a trocar credenciais para um provedor pode obter material de qualquer outro presente no
Vault.

O eixo *Region* tem uma imposição possível que a primeira passagem falhou: a `ObligationRegion`
(`kernel/reference-monitor/obligations.go:132-153`, ligada em `monitor.go:334`) nega cross-border
comparando `call.Resource.Region`, que `exchange.go:201` alinha com o mesmo `Downstream.Region` da
chave. Um PDP que emita essa obrigação fecha metade do eixo. **O *Provider* não tem imposição
nenhuma.**

Porque sobreviveu: **todos os testes do broker e da `packages/security-tests` usam um único
`provider` como constante** — `helpers_test.go`, `scope_test.go`,
`aos264_capability_reuse_test.go`, `aos265_inprocess_test.go`, `security_test.go`. O eixo não está
só desprotegido; não está exercido.

Este ticket é **pré-requisito do wiring do broker** (`DEF-218`): ligar a troca antes de o eixo estar
imposto e testado transforma uma dívida latente numa vulnerabilidade viva no mesmo commit.

### Critérios de Aceitação

- [ ] A autorização da troca compara `Downstream.Provider` (e `Resource`, se aplicável) com a
      identidade/autoridade do principal — por gate próprio, por obrigação do PDP no molde da
      `ObligationRegion`, ou por a capability deixar de ser constante; a escolha fica justificada
- [ ] Um teste **cross-provider**: um principal autorizado para o provedor A recebe negação
      atribuível ao pedir material do provedor B
- [ ] A negação é selada com efeito/código/razão, distinguível de `ErrNoMaterial`
- [ ] `tecnica/07` e `tecnica/17` descrevem o modelo de autorização da troca nos três eixos
      (capability, region, provider), dizendo qual é imposto por quem
- [ ] O `DEF-218` passa a nomear este ticket como pré-condição

### Estado

**IMPLEMENTADO COM RESIDUAL DE SEGURANÇA — reaberto em parte pela validação adversarial.** P2 por
alcance (latente — `broker.New` não tem chamador de produção), **alta no dia do wiring**.

> **O QUE A VALIDAÇÃO ENCONTROU (A6).** A «defesa server-side gémea» no `dispatch` lê `AgentClass`
> e `UserAuthority` do **envelope JSON serializado ANTES da mediação** — nunca reescrito. O gate
> lê-os de `call.Principal`, que o hook de identidade **substitui por inteiro** pelos valores do
> token verificado (`identity/rmadapter.go`). São fontes diferentes: a defesa não é uma segunda
> opinião, é a primeira opinião de quem pede.
>
> **Alcance, medido:** numa cadeia de produção (`NewProductionSecure`, hook de identidade real) o
> GATE decide sobre a classe do TOKEN e está correcto. No harness de referência (`DefaultHooks`,
> `IdentityStub`) a classe vem do pedido, e um revisor **reproduziu** a passagem: mudando só
> `Principal.AgentClass` de uma classe com tecto `{stripe}` para outra com `ProviderAny`, a troca
> devolve handle. A confusão de deputado não foi fechada nesse harness — foi deslocada do campo
> `provider` para o campo `class`.
>
> **Porque não se corrige aqui:** a `referencemonitor.ToolFunc` recebe apenas bytes, pelo que o
> `dispatch` não tem acesso ao principal mediado. Fechá-lo exige mudar o contrato do RM — outro
> módulo, outro ticket. O que se fez foi **deixar de o chamar defesa independente**.

Via escolhida: o `ScopeGate` ganha consciência de provedor, com defesa server-side gémea no
`dispatch` **antes de a chave do Vault existir**. A autoridade efectiva é o tecto da classe ∩ os
grants `prov:` do token — que só **estreitam**, nunca ampliam, o mesmo princípio da fonte externa de
autoridade do `ScopeGate` do RM. As outras duas vias foram recusadas com razão escrita: a obrigação
no molde da `ObligationRegion` é **estruturalmente impossível** sem alterar o contrato C1 (`Call`/
`Resource` têm `Region` mas não têm campo de provedor — foi isso que permitiu fechar o eixo região
no kernel e impede fechá-lo aí); e derivar a capability do provedor obrigaria a re-assinar o bundle
de capabilities e reverteria a decisão de AOS-264.

`TestAOS324_CrossProvider_NegadoEAtribuivel` aprovisiona material de **ambos** os provedores — a
negação não é vácua por ausência de material — e assere `Effect=deny`, `Code=E_DENIED_BY_HOOK`,
`DeniedBy=broker-scope`, evento `denied` selado, nenhum `credential.exchange.issued`, e
`!errors.Is(err, ErrNoMaterial)`. O inverso tem teste próprio. Verificação de não-vacuidade: com
`authorizeProvider` neutralizado, quatro testes ficam vermelhos.

**Estado por omissão — `unset`, e a razão de não ser deny-all.** Sem `WithClassProviders` a
comparação por conjunto não corre. Foi uma restrição minha ao contrato do implementador («não pode
transformar-se num deny-all silencioso»), e é a escolha certa por uma razão que a implementação
tornou visível: a postura é **selada no campo `provider_policy` de cada troca**, pelo que uma
composição não-imposta fica auditável e greppável em vez de invisível. O que a postura **não**
relaxa: provedor vazio é negado fail-closed nas duas posturas.

O `DEF-218` passa a nomear isto como **pré-condição do wiring**: declarar a política e assertar
`enforced`. `tecnica/07` documenta o modelo dos três eixos e qual mecanismo impõe cada um, e a
linha **E** (elevação de privilégio) da tabela STRIDE do BRK em `tecnica/17` — que dava a confusão
de deputado por mitigada pelo «escopo» — passa a dizer que «escopo» eram três eixos e só um estava
imposto, com o estado a mudar de «entregue» para «entregue por configuração».

**Residual declarado:** sob `unset` o defeito persiste materialmente — fica registado e fechável por
configuração, não fechado. E a convenção `prov:` na autoridade é **nova**: nenhum emissor a produz
hoje, pelo que na prática vale só o tecto da classe até o `platform/identity` a emitir.
---

## AOS-325 — Cinco declarações de estado que já não são verdade

### Contexto

A cultura de declarar lacunas em banners de postura, cabeçalhos de ficheiro e no
`REGISTO-Deferimentos.md` é o que permitiu à `analises/11` reclassificar sete hipóteses como
não-achados. É uma força real. O seu preço é que **uma declaração falsa é pior do que nenhuma**,
porque compra confiança que já não sustenta.

Cinco estão erradas hoje:

1. **`packages/cmd/aos/posture_banner.go:109`** — «o nó não importa `platform/broker`». Falso desde
   o AOS-264: `bootstrap.go:67` e `broker_vault_env.go:23` importam-no. O banner vizinho da linha
   180 («não *compõe*») continua verdadeiro.
2. **`bootstrap.go:2416` e o banner do Vault do broker** — «a troca só medeia algo em AOS-265». O
   AOS-265 aterrou (`broker/inprocess.go`, `aos265_inprocess_test.go`) e **não** ligou a troca. A
   substância continua certa; o ticket apontado já fechou. O bloqueador real é o `DEF-218`.
3. **`packages/platform/registry/mcp/protocol.go:147`** — «`Digest` RESERVADO (AOS-047). Vazio em
   AOS-046». O AOS-047 entregou. (Fechado por AOS-320.)
4. **`packages/platform/model-gateway/pipeline/stages.go:70` e `tecnica/06:325`** — ambos dizem que
   «o runtime/assembler compõe `freeze.RunPrefix.Turn` → `layout.Guard.Admit` por turno». **A
   composição não existe**: `packages/platform/model-gateway/cache/freeze` não tem um único
   importador não-teste fora de si próprio, e `layout.Guard.Admit` não tem chamador de produção. O
   `tecnica/06:325` é, no resto, cuidadoso — explica que o estágio do GW é pass-through por desenho
   e que o CA é cumprido pelo chamador da montagem; o que está errado é atribuir ao runtime uma
   composição que ele não faz.

   > **CORRECÇÃO — 2026-09-04.** A versão anterior deste item acrescentava «nem o método
   > `RunPrefix.Turn` existe». **É falso**: existe em
   > `packages/platform/model-gateway/cache/freeze/freeze.go:117`. A afirmação veio de uma refutação
   > da `analises/11` e foi transcrita sem ser verificada — o defeito que o princípio «não confiar
   > cegamente noutro agente» existe para apanhar. O que resta do item é a composição ausente, que
   > se confirma.
5. **`packages/platform/memory/doc.go:54-55` contra
   `packages/platform/memory/compression/async_compactor.go:312-314`** — um diz que
   `record.Persist` grava «SEMPRE a trajectória completa no backend»; o outro diz que sem tracer
   real o registo não sai dali. Uma das duas está errada.

E uma sexta, interna a um ficheiro: em
`packages/platform/model-gateway/internal/credentials/source.go`, o doc-comment da linha 47 promete
que «um pedido para um par fora de `Allowed` falha fail-closed atribuível» e o comentário da linha
59 declara que «se vazio, a origem é permissiva». **Duas posturas opostas no mesmo ficheiro**, e
nenhum teste fixa o comportamento com a lista vazia.

Um sétimo item, de âmbito de operador: `deploy/server/docker-compose.prod.yml:92-94` é o **único**
artefacto que afirma que o Event Store replicado torna «N replicas do no a configuracao pretendida»
sem a ressalva do WORM que `bootstrap.go:482`, `wal_posse.go:97-100`, o teste do AOS-100 e o
EPIC-10 todos carregam — e é o ficheiro que o operador edita.

### Critérios de Aceitação

- [ ] As seis declarações acima passam a descrever o estado actual, ou são removidas
- [ ] O comentário de `source.go` deixa de se contradizer, e um teste fixa o comportamento com
      `Allowed` vazio (qualquer que seja a postura escolhida)
- [ ] O comentário do compose ganha a ressalva do WORM que os outros quatro artefactos já têm
- [ ] Fica registado, no `REGISTO-Deferimentos.md` ou no runbook de revisão, um mecanismo que faça
      uma declaração citar o ticket que a desbloqueia — para que fechar o ticket torne a declaração
      revisível em vez de a deixar apodrecer

### Estado

**IMPLEMENTADO (parcial — um item pertence ao AOS-320).** P2.

Corrigidos: **(1)** `posture_banner.go` deixa de dizer que o nó não importa `platform/broker`, e
explica a diferença entre importar e compor; **(2)** `bootstrap.go` e `broker_vault_env.go` — as
menções que apontavam a pendência da troca para AOS-265 passam a nomear o `DEF-218`, dizendo que o
AOS-265 já aterrou sem a fechar; **(4)** `pipeline/stages.go` e `tecnica/06:325` deixam de afirmar
como facto a composição `freeze.RunPrefix.Turn → layout.Guard.Admit`, que não existe, e nomeiam quem
cumpre de facto o CA (o `PromptAssembler` do runtime); **(5)** `memory/doc.go` deixa de dizer que
`record.Persist` grava «SEMPRE a trajectória completa», que o próprio compactador contradiz em dois
sítios; **(6)** `credentials/source.go` reconcilia as duas posturas opostas, com
`TestSource_AllowedVazia_NaoConstrangeEEstaDeclarado` a fixar o comportamento da lista vazia — que
nenhum teste fixava; **(7)** `docker-compose.prod.yml` ganha a ressalva do WORM que os outros quatro
artefactos já carregavam.

**Item (3) — `mcp/protocol.go` «RESERVADO (AOS-047)» — pertence ao AOS-320**, cujo critério de
aceitação o inclui: corrigir o comentário sem ligar o digest seria trocar uma declaração falsa por
outra.

**AC4 fica por fechar, agora com eixo:** não foi criado mecanismo que faça uma declaração citar o
ticket que a desbloqueia — registado em **DEF-814**, com a nota `N-DEF-814` a descrever o gate que
o fecharia (o irmão do `deferrals`: verificar o ESTADO do ticket citado, não só a sua existência). As sete correcções são pontuais; o que evitaria a próxima ronda é estrutural e fica por
desenhar.

---

## AOS-326 — A não-composição de MEM e REG não está registada em ADR nem em `DEF-NNN`

### Contexto

Os quatro serviços de plataforma partilham o mesmo estado: biblioteca de qualidade, com suites
adversariais não-vacuosas e gates verdes, sem chamador em `packages/cmd/aos`.

- **MEM**: o nó compõe `memory.NewService(memPort)` (`bootstrap.go:2231-2233`), um CRUD genérico
  parametrizado por `domain.MemoryClass`, e faz **uma** escrita episódica
  (`packages/integration/ingestion.go:221`). `memory/episodic`, `semantic`, `procedural`,
  `compression` e `migrations` têm zero importadores externos não-teste. `loop.go:320-331` declara
  que «nenhum caminho de produção atribui `Goal.MemoryContext` (o EPIC-04 ainda não o ligou)».
- **REG**: `bootstrap.go` compõe `emptyCatalog{}` e um revalidador de referência com trust store
  vazio. `registry.New` tem um único chamador não-teste — `promotion/pipeline.go:85` — e
  `promotion.NewPipeline` não tem nenhum. `mcp.NewHost` e `tofu.NewMonitor` têm zero.

Para o ORQ e o SCH isto é **doutrina**: o ADR-018 §4 e o ADR-023 declaram-no, o `EPIC-10`/AOS-281
di-lo em palavras, e `packages/cmd/aos/boundary_orq_sch_test.go` impõe-no por guard-test. Nenhum
ADR faz o equivalente para os serviços de plataforma.

E a cobertura por deferimentos é desigual ao ponto de ser um dado: em 107 entradas do
`REGISTO-Deferimentos.md`, o GW tem 13 menções, o BRK 9, o REG 3 (e o `posture_banner.go` não diz
uma palavra sobre o REG não estar composto), e o **MEM uma** — a `DEF-302`, que é sobre o *key
vault* do DSAR.

A lacuna mais material do EPIC-04 não tem entrada no registo que existe para a registar. O
`loop.go:329-331` explica porquê, e a explicação é defensável: a nota **cita** o `DEF-806` em vez de
criar dívida nova, e a palavra-marcador faria o gate `deferrals` contar a citação. Mas o efeito é
que a lacuna ficou fora do registo, e o `DEF-806` é sobre separação de planos, não sobre wiring de
memória.

Este ticket **não decide** se se compõe ou não. Decide que a posição fica escrita.

### Critérios de Aceitação

- [ ] A postura de composição de MEM e de REG fica registada — como `DEF-NNN` com dono e gatilho de
      desbloqueio, ou como ADR no molde do ADR-023 se for decisão de forma do produto
- [ ] O registo distingue os dois casos: o que está fora do nó **por decisão** e o que está fora
      **por inacabamento**
- [ ] O `posture_banner.go` declara a postura do REG no arranque, como já declara a do broker
- [ ] O gate `deferrals` continua verde, e o mecanismo escolhido não obriga a evitar a
      palavra-marcador para não disparar falsos positivos (o caso de `loop.go:329-331`)
- [ ] `specs/EPIC-04` e `specs/EPIC-05` deixam de ter critérios de saída que a composição actual não
      satisfaz, ou os critérios ganham a qualificação que os torna verdadeiros

### Estado

**IMPLEMENTADO.** P1.

`plataformaPostureBanner` declara no arranque o que o nó compõe de MEM e de REG — e o que não compõe
—, derivando do ESTADO (`cfg.Catalog`/`cfg.Revalidator` a nil) e não da intenção da config, na
disciplina que o banner do credential broker já impunha. Uma terceira linha diz explicitamente que,
ao contrário do ORQ/SCH, **não existe ADR** que os declare deliberadamente não-compostos, pelo que o
estado é *inacabado* e não *adiado*.

`DEF-811` (MEM) e `DEF-812` (REG) entram no registo, família **8xx — wiring diferido**, com dono
(Arquitecto de Plataforma), eixo `POR ATRIBUIR` e gatilho escrito: *compor, ou emitir ADR no molde
do ADR-023*. As notas `N-DEF-811`/`N-DEF-812` descrevem o ticket que falta — e dizem porque ele é
uma **decisão do dono da forma do produto** (Carta §2) e não código, que é a razão de o eixo não
poder ser um ticket inventado. A `N-DEF-812` regista ainda uma dependência: o AOS-320 deve fechar
antes de se compor o host MCP, senão liga-se um pino que não distingue servidores.

Gate `deferrals` verde para este eixo — a contagem de `posture_banner.go|DEFERIDO` passou de 2 para
4 e é verificada por máquina.

Os §2 do **`specs/EPIC-04`** e do **`specs/EPIC-05`** ganham a qualificação que o AC5 pedia: os
critérios de saída são propriedades do SISTEMA, hoje satisfeitas ao nível da BIBLIOTECA, e a nota
nomeia quais são falsos lidos como propriedades do nó — no EPIC-05, o primeiro («o REG está
operacional como catálogo append-only e versionado»), o segundo (transportes MCP integrados) e o
quinto (TOFU activo). Sem isso, dois epics continuariam a prometer uma composição que não existe,
que é precisamente o que este ticket veio impedir.

**O que este ticket NÃO faz, por desenho:** não emite o ADR. Essa é uma decisão sobre a forma do
produto que a Carta §2 reserva ao dono. Registar a postura era o que faltava; decidi-la não é do
executor.

---

## AOS-327 — A pendência de *shred* não tem regra de alerta, e a série desaparece com o processo

### Contexto

`api.go:1188` expõe `aos_dsar_vault_shred_unconfirmed` — «Destruicoes de KEK (crypto-shred) por
CONFIRMAR na custodia; >0 mantem o no unready e o conteudo pode continuar recuperavel». É a métrica
certa, com a descrição certa, e nenhuma regra de alerta a referencia.

> **ÂMBITO REDUZIDO — 2026-09-04, na discovery da execução.** Duas verificações cortaram este
> ticket a meio, e ambas apontam para menos trabalho, não mais.

**(a) A ausência de regras de alerta é uma lacuna DECLARADA de todo o deployment, não desta
métrica.** `deploy/server/otel-collector.yaml:56-58` di-lo em texto: «O QUE ISTO NÃO DÁ, e é metade
da verdade: não há retenção histórica nem regras de alerta. Isto torna as séries LEGÍVEIS e
RASPÁVEIS; alertar sobre elas exige um Prometheus (ou equivalente) apontado aqui, **que é uma
decisão de infra por tomar**». Não há Prometheus no `docker-compose.prod.yml` — só um colector OTLP
que serve `:9464`. **Escrever um ficheiro de regras que nada carrega seria produzir exactamente o
artefacto inerte que a `analises/11` critica** — um gate verde sobre código que não corre.

**(b) A pendência SOBREVIVE ao restart.** `bootstrap.go:2094` chama `restoreShredPending(ctx, worm,
"governance.dsar", dsarVault)`, que a reconstrói da cadeia DSAR — logo um processo que morra e volte
**re-levanta o alarme e a série reaparece**. O cenário «a série desaparece e ninguém dá por isso»
fica limitado à substituição PERMANENTE por uma réplica com outro WORM, que é o caso de cluster já
coberto pelo AOS-284 (v1.1) e analisado na §8 da `analises/11`.

O que resta é real e é pequeno: as expressões de alerta não estão escritas em lado nenhum, e o
operador que montar um Prometheus tem de as inventar. E o gatilho de re-tentativa não existe — a
operação existe e é barata (`vaultkeyvault.go:600-608`: o `Delete` relê a chave e exige 404, logo é
idempotente e re-verificável), mas nada a dispara (`retention_sweeper.go:315` só reporta a contagem).

### Critérios de Aceitação

- [ ] As expressões de alerta recomendadas para `aos_dsar_vault_shred_unconfirmed` ficam
      **documentadas** junto da declaração de `otel-collector.yaml:56-58` que diz que alertar exige
      infra por tomar — não como ficheiro de regras que nada carrega
- [ ] A documentação diz explicitamente que a ausência da série com a custódia de referência é
      **correcta** (ver AOS-322), para que ninguém escreva uma regra `absent()` que dispare em todos
      os deployments de referência
- [ ] A documentação regista que a pendência é reconstruída da cadeia no arranque
      (`bootstrap.go:2094`), e que o único cenário de perda silenciosa é a substituição permanente
      com outro WORM — com remissão para o AOS-284
- [ ] Uma destruição por confirmar é re-tentada automaticamente, ou a ausência de re-tentativa fica
      declarada com o seu porquê
- [ ] O runbook nomeia as causas típicas que `dsar.go:245` já lista (política Transit sem
      `deletion_allowed`, replicação, token sem autoridade) e o que fazer com cada uma

### Estado

**IMPLEMENTADO.** P2, âmbito reduzido — e a redução é o resultado mais útil deste ticket.

`docs/runbooks/RB-06.md` cobre o modo de falha inteiro: o sinal, as três causas que o
`dsar.go` já nomeia (política Transit sem `deletion_allowed`, replicação, token sem autoridade), a
remediação sem reinício (repetir o `erase` para o mesmo titular limpa a pendência), a verificação, e
o limite conhecido da substituição permanente de réplica, com remissão para o AOS-284.

A **expressão de alerta** fica lá escrita, com `for: 5m` justificado — e com a armadilha nomeada:
não escrever `absent()` sobre estas séries, porque elas não existem em nenhum deployment com a
custódia de referência e isso é correcto (AOS-322). O `otel-collector.yaml` ganha o ponteiro para o
runbook ao lado da declaração que já dizia que alertar exige infra por tomar.

**Não se criou ficheiro de regras**, e é deliberado: não há Prometheus no compose, e um artefacto que
nada carrega mas parece protecção é o defeito que a `analises/11` documenta. **Não se armou
re-tentativa automática**, e a ausência fica declarada com o porquê: uma destruição que falha por
política não passa a funcionar por ser repetida, e um ciclo de re-tentativa mascararia a causa
enquanto o nó fica unready de qualquer forma — que já é o sinal mais forte que o sistema tem.
---

*Epic derivado de `analises/11_Auditoria_MEM_REG_GW_BRK_Adversarial.md`. Cada âncora foi
reconfirmada contra o código antes da escrita. Ver `specs/INDICE.md`.*

---

# Follow-ups da validação adversarial — AOS-328..330

Os oito tickets acima remediam a `analises/11`. **Estes três não vêm da auditoria: vêm da
validação adversarial DESSA remediação** (§0.3), e é por isso que ficam separados — misturá-los
faria parecer que a auditoria original os tinha encontrado, e ela não os encontrou.

Cada um fecha um `DEF-NNN` cuja nota do §6 do registo dizia «o ticket que falta é X». O registo de
deferimentos não cria tickets por desenho; criá-los é este acto, e os eixos passam de
`POR ATRIBUIR` para os números abaixo.

---

## AOS-328 — A confirmação do crypto-shred é opcional, e a omissão é fail-open para a custódia seguinte

### Contexto

`dsar.ShredConfirmer` é consultada apenas se `WithShredConfirmer` receber algo não-nil, e
`confirmadorDeShredDe` (`packages/cmd/aos/shred_confirmador.go`) devolve `nil` para uma custódia que
não implemente a porta interna. Sem confirmador, o fluxo sela `dsar.key_destroyed` **sem perguntar**.

Hoje isso é correcto e está fixado por teste (AOS-322): as duas custódias existentes fazem a escolha
certa — o `InMemoryKeyVault` não implementa a porta porque o seu `Delete` é um `delete()` num mapa e
não pode falhar; o Vault Transit implementa-a. O risco é a **terceira**: um KMS de terceiros que
*possa* falhar a destruir e não implemente a porta faz a cadeia afirmar uma irrecuperabilidade que
ninguém verificou — o defeito exacto que a porta foi criada para fechar, reaberto pela via da
omissão. **Nada obriga essa escolha a ser consciente.**

A revisão adversarial do AOS-322 mostrou ainda que o banner era pior do que a omissão: dizia «com o
vault de REFERENCIA isto é CORRECTO» num ramo que dispara para *qualquer* custódia sem a porta,
incluindo uma injectada por `Config.DSARVault`. Isso já foi corrigido — o banner tem três ramos e o
terceiro diz «NÃO É CORRECTO» —, mas declarar não é impor.

### Critérios de Aceitação

- [ ] Uma custódia composta sob `AOS_MODE=production` que **não** implemente a porta de confirmação
      é recusada no arranque, ou é aceite mediante uma declaração explícita de que não precisa
      (a escolha faz parte do ticket e é justificada por escrito)
- [ ] O vault de referência continua a compor **sem** declaração extra — a guarda não pode transformar
      o modo de desenvolvimento numa configuração cerimoniosa
- [ ] Um teste que prove a recusa (ou a exigência de declaração) com uma custódia falsa que não
      implemente a porta, e um controlo que prove que as duas custódias reais continuam a compor
- [ ] `DEF-813` passa de `POR ATRIBUIR` para este ticket

### Estado

**POR IMPLEMENTAR.** P2. Eixo do `DEF-813`. Origem: validação adversarial da EPIC-23, não a
`analises/11`.

---

## AOS-329 — Nada obriga uma declaração de estado a citar um ticket ainda aberto

### Contexto

O gate `deferrals` impõe que todo o marcador de dívida no código tenha **eixo verificável**. O que
não impõe é que esse eixo esteja **aberto**: uma linha que diz «pendente até AOS-265» continua a
passar depois de o AOS-265 fechar.

Foi assim que a `analises/11` §5 encontrou **seis** declarações caducadas de uma vez, e o AOS-325
corrigiu sete — e mesmo assim **escapou uma oitava** (`packages/cmd/aos/main.go`), encontrada só na
validação adversarial. Sete correcções pontuais não impediram a oitava, e não impedirão a nona: o
padrão é sempre o mesmo — uma declaração nomeia o ticket que a fecharia, o ticket fecha, e a
declaração fica a comprar confiança que já não sustenta.

O `ref-lint` já resolve tickets contra as epics; o que falta é ligar essa leitura ao eixo das
declarações.

### Critérios de Aceitação

- [ ] Uma linha de postura ou comentário que cite `AOS-NNN` como **bloqueador** é cruzada com o
      estado desse ticket no `specs/EPIC-*.md`, e o gate **falha** quando o ticket citado está
      implementado
- [ ] A verificação distingue «cita como bloqueador» de «cita como origem/referência» — uma nota que
      diga «entregue por AOS-047» não pode disparar. O mecanismo de distinção fica declarado (marcador
      próprio, posição na frase, ou lista explícita), e é testado nos dois sentidos
- [ ] Prova negativa: o gate consegue ficar **vermelho** — um teste-veneno com uma citação a um
      ticket fechado fá-lo falhar
- [ ] As oito declarações que a EPIC-23 corrigiu passam a estar cobertas por este gate (verificação
      retroactiva: se o gate existisse, teria apanhado cada uma)
- [ ] `DEF-814` passa de `POR ATRIBUIR` para este ticket

### Estado

**POR IMPLEMENTAR.** P2. Eixo do `DEF-814`. É o **irmão do `deferrals`** e o único destes três que
ataca a causa-raiz em vez de uma instância: sem ele, a próxima auditoria volta a encontrar
declarações caducadas e alguém volta a corrigi-las uma a uma.

---

## AOS-330 — O namespace da política do broker não é o namespace da chave do Vault

### Contexto

A política de provedores do broker (AOS-324) compara strings **cruas** por igualdade. O cliente
KV v2 (`packages/platform/broker/internal/vault/kvv2.go`) resolve o path com `TrimSpace` e dobra
tudo o que esteja fora de `[A-Za-z0-9._-]` para `_`.

Medido por revisão adversarial: `" "`, `"\t"` e `"*"` produzem **todos** o path `_/eu/…` —
indistinguível de um eixo em branco — e `"acme:eu"` colide com `"acme_eu"`. Sob a postura por
omissão, valores como `" "` e `"stripe/x"` passam o guarda de «provedor indeterminado» e chegam ao
`Fetch`, o que falsifica a afirmação de que o Vault nunca é consultado com um eixo em branco. Sob
`enforced`, um nome autorizado que dobre sobre um proibido dá material do proibido.

Enquanto os dois namespaces forem diferentes, «autorizado» e «o que o Vault serve» não são a mesma
coisa — e a divergência é silenciosa nos dois sentidos.

Inofensivo hoje: o broker não está composto (`DEF-218`) e a postura é `unset` em todo o repositório,
pelo que não há caminho de execução que o alcance. **É por isso que se fecha antes do wiring, e não
depois.**

### Critérios de Aceitação

- [x] Ou a política decide sobre o valor **normalizado** — o mesmo que forma o path —, ou recusa
      fail-closed qualquer valor que a normalização altere. A segunda via é a mais defensável (um
      provedor cujo nome não sobreviva ao path não devia ser aceitável de todo) e a escolha é
      justificada por escrito
- [x] Um teste que prove que dois valores distintos que dobrem no mesmo path **não** são ambos
      aceites, e outro que prove que um valor que a normalização altere é recusado de forma
      atribuível
- [x] O guarda de «provedor indeterminado» passa a apanhar `" "`, `"\t"` e `"*"` — hoje não apanha
- [x] `DEF-218` nomeia este ticket como pré-condição, a par do AOS-324
- [x] `DEF-815` passa de `POR ATRIBUIR` para este ticket

### Estado

**IMPLEMENTADO.** P2 por alcance (latente), **pré-condição do wiring do broker**. Eixo do
`DEF-815`.

**A ESCOLHA FOI A SEGUNDA VIA**, e a razão está escrita no `authorizeProvider`: recusa-se o que a
normalização altere, em vez de decidir sobre o valor normalizado. Decidir sobre o normalizado
tornaria a colisão **invisível** em vez de impossível — a política passaria a tratar `acme:eu` e
`acme_eu` como o mesmo provedor, em silêncio. Um provedor cujo nome não sobrevive ao path não é
um provedor identificável.

**O PREDICADO VIVE ONDE A NORMALIZAÇÃO VIVE.** `vault.SegmentoEstavel` é exportado do pacote que
forma o path, e a política pergunta-lhe. A alternativa — a política aprender a normalizar por si —
são duas cópias da mesma regra a divergir, que foi exactamente o que o AOS-337 mediu noutro eixo
(`(inválido)` contra `(inválida)` no dia em que a segunda cópia nasceu). O `DEF-815` exige-o à
letra: «a normalização do path tem de ser a MESMA em que a política decide».

**A RECUSA É `ErrProviderUndetermined`, NÃO `ErrProviderOutOfScope`.** O eixo não é um provedor
que está fora da autoridade; é um valor que não identifica provedor nenhum. Sob `enforced`, `" "`
era antes atribuído a «fora de escopo» — atribuição errada do mesmo defeito, e o teste fixa a
distinção.

**O `"*"` NÃO ERA SÓ NÃO-APANHADO — ERA AUTORIZADO.** Sob `enforced`, com o tecto da classe a
declarar `ProviderAny`, um pedido com `Provider="*"` passava a comparação por conjunto e produzia
o path `_/…`. O AC3 nomeia-o a par de `" "` e `"\t"`, mas os três não eram o mesmo defeito.

**SÓ O EIXO PROVIDER, e a razão é dura.** O `sanitizeSegment` corre nos **três** segmentos, e a
`Capability` contém **sempre** `:` (`cap:http.post` → `cap_http.post`): a mesma regra aplicada a
ela **negaria todas as trocas do sistema**. A `Region` tem vocabulário fechado que sobrevive. O
ticket nomeia o provedor, e é onde o eixo morde — um nome de provedor vem de configuração de
deployment, não de vocabulário fixo. *(O AC estava escrito como se a regra fosse universal; não
é, e não pode ser.)*

Cinco funções de teste, com **dois controlos**: os provedores legítimos continuam a passar (sem
isso, um `return ErrProviderUndetermined` incondicional passaria tudo o resto e negaria todas as
trocas), e a recusa por **autoridade** continua distinta da recusa por **namespace**. Quatro
provas de mutação — desligar a ancoragem, trocar a sentinela, o predicado a aceitar tudo, o
predicado a recusar tudo — todas vermelhas e revertidas limpas.

**OS DOIS ACs DE REGISTO JÁ ESTAVAM SATISFEITOS** por trabalho anterior, e verifiquei-os em vez de
os reclamar: o `DEF-218` já nomeia este ticket como pré-condição a par do AOS-324
(`REGISTO-Deferimentos.md:186`), e o `DEF-815` já lhe está atribuído (`:244`), não `POR ATRIBUIR`.

**LIMITE DECLARADO.** Isto continua latente: o broker não está composto (`broker.New` não tem
chamador de produção) e a postura é `unset` em todo o repositório. O defeito era latente **duas
vezes** — sem wiring, e sem cobertura de teste que o pudesse expor, porque toda a suite usa o
`MemoryVault`, que chaveia pelo `Key.id()` **cru** e não dobra. Os testes deste ticket são os
primeiros a exercitar o eixo.
---

## AOS-331 — O provedor autorizado não é amarrado ao recurso de destino

### Contexto

O AOS-324 fez a troca de credenciais decidir **que material sai**. Não decide **para onde vai**:
nada em `packages/platform/broker/exchange.go` compara `in.Provider` com `in.ResourceValue`.

Um pedido com `Provider=stripe` — autorizado — e `ResourceValue=https://evil.example/…` obtém o
segredo do Stripe, e o `EgressStub` da cadeia de referência é neutro. A credencial certa sai para o
destino errado, que é a segunda metade da confusão de deputado que o AOS-324 fechou pela primeira.

Medido na revisão adversarial da EPIC-23. Latente: o broker não está composto (`DEF-218`).

### Critérios de Aceitação

- [x] A autorização da troca relaciona `Downstream.Provider` com `Downstream.ResourceValue` — por
      allowlist de host por provedor, por obrigação de egress do PDP, ou por o `EgressGate` real
      passar a correr nesta cadeia. A escolha é justificada por escrito
- [x] Um teste em que o provedor é autorizado e o recurso não, e a negação é atribuível e
      distinguível de `ErrProviderOutOfScope`
- [x] O estado por omissão fica declarado, como o do eixo provider — e não é um deny-all silencioso
- [x] `DEF-218` nomeia este ticket como pré-condição, a par de AOS-324 e AOS-330

### Estado

**IMPLEMENTADO.** P2 por alcance (latente), **pré-condição do wiring do broker**.

**A VIA É ALLOWLIST DE HOST POR PROVEDOR**, e as outras duas que o AC oferecia não fecham o eixo:

- **O `EgressGate` real não amarra provedor a recurso.** O `network.EgressHook` deriva o destino
  do `Resource` e compara-o com uma allowlist de **deployment** — não sabe o que é um provedor.
  `Provider=stripe` com destino `evil.example` **passa** se `evil.example` estiver na allowlist, e
  é negado com `DeniedBy="egress"`, indistinguível de qualquer outro egress fora da lista.
  Resolve o egress, não a confusão de deputado.
- **Uma obrigação de egress do PDP** exigiria o bundle de política assinado — que é precisamente
  o bloqueador declarado do `DEF-218`. Seria circular: este ticket é pré-condição do wiring, não
  consequência dele.

**A DECISÃO LÊ O `Call.Resource`, NÃO O ENVELOPE.** É esse o valor que a mediação **sela**;
decidir sobre um e selar o outro repetiria a divergência de namespaces que o AOS-330 acabou de
fechar no eixo do Vault.

**DUAS SENTINELAS, E A DISTINÇÃO É O AC.** `ErrResourceOutOfScope` («este destino não é deste
provedor») é distinta de `ErrProviderOutOfScope` («este provedor não é teu»): levam o operador a
sítios diferentes — a primeira é a allowlist de hosts, a segunda é a política de classe. E
`ErrResourceUndetermined` cobre o que não permite decidir — valor que não se analisa, ou tipo que
não é de rede —, porque sob política declarada informação insuficiente é recusa, a mesma postura
do envelope ilegível no eixo provider.

**O ESTADO POR OMISSÃO É `unset`, DECLARADO E INTERROGÁVEL.** O AC proíbe um deny-all silencioso,
e a razão é dura: um default que negasse tudo partiria todos os deployments que não configuram a
coisa nova, e um eixo que ninguém consegue ligar não é segurança — é uma avaria. A postura é
legível por `ScopeGate.ResourceBindingPosture()`, que é o que a distingue de um silêncio e o que
o AOS-332 vai imprimir. Um mapa **vazio não-nil** é uma declaração — «nenhum provedor alcança
recurso nenhum» — e é escolha legítima de quem quer o eixo fechado; é a mesma distinção do eixo
provider.

Sete funções de teste, **duas delas ponta-a-ponta pela cadeia de mediação** — porque as outras
cinco exercitam a função de autorização e isso é «a regra está escrita», não «a regra corre». Com
**três controlos**: o host do próprio provedor passa, sob `unset` nada nega, e a negação por
autoridade continua distinta da de recurso. Quatro provas de mutação — o gate a não correr a
verificação, a postura por omissão a virar `enforced`, o indeterminado a ser aceite, e a sentinela
a virar a do provedor — todas vermelhas.

`DEF-218` passa a nomear este ticket como pré-condição, a par do AOS-324 e do AOS-330.

**LIMITE DECLARADO.** Continua latente: o broker não está composto e a postura é `unset` em todo
o repositório. E a allowlist é por **host**, não por caminho — `https://api.stripe.com/qualquer`
passa se `api.stripe.com` estiver na lista. É deliberado: o caminho de um recurso downstream muda
com a API do provedor, e uma allowlist por caminho seria configuração que caduca sozinha.
---

## AOS-332 — A postura do eixo provider não aparece no banner de arranque

### Contexto

O AOS-324 sela a postura (`unset`/`enforced`) no campo `provider_policy` de **cada troca emitida**,
e é isso que a torna auditável em vez de silenciosa. Mas duas lacunas ficaram:

1. **Só o caminho de sucesso sela.** As negações passam pelo `MediationRecord` do Reference Monitor,
   que não tem o campo — uma troca negada não regista sob que postura foi decidida.
2. **O banner de arranque não diz uma palavra sobre o eixo.** Um nó em `unset` que ainda não emitiu
   nenhuma troca é indistinguível de um em `enforced`, e o banner existe precisamente para declarar
   posturas: já declara o credential broker, o Vault do broker, a custódia da KEK, a confirmação do
   *crypto-shred*, e — desde AOS-326 — o MEM e o REG.

O AOS-324 fechou o eixo por configuração. Este fecha a sua **observabilidade**, sem a qual o
`DEF-218` não tem como assertar `enforced` antes de ligar.

### Critérios de Aceitação

- [x] O banner declara, no arranque, a postura do eixo provider — derivada do ESTADO composto, na
      disciplina de `plataformaPostureBanner` e `credentialBrokerPostureBanner`
- [x] Uma troca NEGADA regista a postura sob a qual foi decidida
- [x] Um teste que prove que as duas posturas produzem linhas distintas, e que a negação as sela

### Estado

**IMPLEMENTADO.** P2.

**O QUE ISTO QUEBRA É UMA CIRCULARIDADE.** O `DEF-218` exige assertar que o campo
`provider_policy` selado diz `enforced` — mas isso só é verificável a partir de um
`credential.exchange.issued`, ou seja **depois da primeira troca bem-sucedida**. Um nó em `unset`
que ainda não trocou nada era indistinguível de um em `enforced`. A postura passa a ser observável
no **arranque**, antes de qualquer troca.

**O BANNER DECLARA NÃO-APLICABILIDADE, NÃO `unset`.** O nó não constrói `*broker.Broker` —
`broker.New` não tem chamador de produção. Declarar `unset` derivado de um `nil` que nunca chega a
ser política seria a afirmação que o cabeçalho do `posture_banner.go` proíbe: «uma linha que diga
*ligado* sobre algo que não está composto é **pior** do que o silêncio que substitui». `unset` é
uma política não-declarada num broker que **existe**; aqui o broker não existe, e usar a mesma
palavra faria o operador ler uma postura onde não há nenhuma. O ramo não-composto nomeia, em vez
disso, os **dois** eixos que o wiring terá de declarar — que é informação verdadeira e útil.

**SÃO DOIS EIXOS, NÃO UM.** O ticket foi escrito antes do AOS-331; quando ele aterrou, passou a
haver a política de provedores **e** a allowlist de recurso↔provedor. O banner declara as duas, e
a função pura já aceita o estado composto — no dia em que o wiring ligar, mudam os argumentos, não
a forma.

**A POSTURA VAI NO `Reason` DA NEGAÇÃO, E NÃO NUM CAMPO NOVO DO `MediationRecord`.** O precedente
próximo é o `PolicyVersion`, que o RM propaga também na negação — mas esse é **genérico**:
qualquer hook de política o preenche, e o contrato C1 é do **kernel**. Uma postura do broker é
preocupação de **plataforma**, e enfiá-la no contrato do kernel por conveniência de um hook seria
a fuga de camada que o `layer-lint` existe para impedir. A forma é greppável e estável —
`<razão> [provider_policy=… resource_binding=…]` — com a razão original intacta no prefixo, pelo
que quem já asserta por substring continua a funcionar.

Seis funções de teste em dois módulos. O teste da selagem lê a razão que ficou **no Event Store**,
não a que o erro devolveu — o AC é sobre o registo durável, e um teste que olhasse só para o erro
em memória provaria outra coisa. Com **três controlos**: as duas posturas produzem linhas
distintas (senão registá-las não distinguiria nada), as quatro combinações do ramo composto são
distintas entre si e do não-composto, e a linha **sai no arranque real** — uma função de banner
que não é chamada declara-se a si própria. Quatro provas de mutação, todas vermelhas.

**DEFEITO ENCONTRADO E NÃO FECHADO AQUI, registado como tarefa própria.** A guarda de composição
do `dispatch` (`exchange.go:290`, a defesa server-side do AOS-324) nega **depois** de a cadeia do
RM ter passado — e o `monitor.go` já selou um `MediationRecord` com `EffectPermit` por
audit-before-effect. Uma troca negada por essa via fica no WORM registada como **permitida**, sem
evento de negação e sem postura. O `TestAOS324_DefesaServerSide_SemGate` assevera que não há
`credential.exchange.issued`, mas **não olha para o registo de mediação** — por isso o defeito
passa despercebido. Fechá-lo muda a semântica da defesa server-side e não cabe neste ticket.
---

## AOS-333 — O endereço do Vault aceita credenciais embutidas, e o banner imprime-o cru

### Contexto

`integration.CheckSecureTransportURL` — o helper que o AOS-323 passou a aplicar aos dois Vaults —
valida o **esquema**, não a **forma**: `https://user:pass@vault:8200` passa. E
`brokerVaultPostureBanner` imprime `s.Addr` **cru** via `log()`.

O resultado é uma via de fuga de segredo por um caminho que o repositório fecha em todos os outros:
a senha do Vault aparece em texto claro no log de arranque, que é recolhido, agregado e retido. O
`ADR-006` proíbe segredo em log; aqui não é o agente que o vê, é o operador e quem quer que leia o
colector.

Menor, do mesmo eixo: o ramo de *parse* falhado do helper devolve o URL na mensagem, e o wrap
ecoa-o inteiro — incluindo as credenciais.

Medido na revisão adversarial da EPIC-23. **Alcançável hoje**: basta um operador pôr credenciais no
`AOS_BROKER_VAULT_ADDR` ou no `AOS_DSAR_VAULT_ADDR`, que é uma forma legítima de as passar ao Vault.

### Critérios de Aceitação

- [x] `CheckSecureTransportURL` recusa fail-closed um URL com `userinfo`, com erro atribuível que
      **não** ecoa o URL
- [x] O banner imprime o endereço **redigido** (esquema, host e porta; nunca `userinfo`), ou recusa
      compor — a escolha é justificada
- [x] Nenhum caminho de erro do helper ecoa o URL cru
- [x] Um teste com um URL com `user:pass@` que prove as três coisas, incluindo a ausência da senha
      na mensagem de erro e no banner

### Estado

**IMPLEMENTADO.** P1 — era o único destes seis alcançável hoje, e o eixo era fuga de segredo.

`CheckSecureTransportURL` recusa `user-info` **antes** de olhar para o esquema: uma URL com
credenciais é negada mesmo quando o transporte estaria correcto, e a recusa vale para os **três**
chamadores de uma vez — a attestation remota, a custódia da KEK e o Vault do broker. Um critério,
três eixos, que é a razão de o helper ter sido exportado.

Nenhum caminho de erro ecoa a URL. O ramo de *parse* falhado devolvia `fmt.Errorf("%q", raw)` e era
o pior dos dois: uma URL que o parser recusa é precisamente onde uma credencial mal escapada tem
mais probabilidade de estar. Passa a omitir o valor — uma URL que não se sabe analisar não se sabe
redigir.

`integration.RedactURL` dá ao banner a forma publicável: esquema, host e porta, e mais nada. É
deliberadamente mais estreita do que o necessário — preservar o caminho ajudaria o diagnóstico, mas
um caminho de Vault já carregou tokens em incidentes reais, e um redactor que hesita não serve para
o sítio onde é preciso.

**O doc-comment do helper afirmava que isto estava fechado:** «a mensagem nunca inclui credenciais
(user-info numa URL não é suportado por nenhum chamador)». Era falso nas duas metades. Mais uma
declaração a comprar confiança que não sustentava — o eixo do `DEF-814`.

Seis funções de teste em dois módulos, com **controlos**: as URLs legítimas continuam a passar (sem
isso, um helper que recusasse tudo passaria os testes de recusa) e o banner continua a nomear host e
porta (redigir tudo trocaria uma fuga por um banner inútil). O teste do banner é **defesa em
profundidade** e testa deliberadamente um estado que o parser já não deixa acontecer: se alguém
relaxar o critério de transporte, a redacção continua lá. Prova de mutação executada — trocando
`if u.User != nil` por `if false`, as duas suites ficam vermelhas; revertida e confirmada limpa.

### A revisão adversarial encontrou cinco defeitos nesta correcção, e quatro estão fechados aqui

Um revisor independente, sem acesso ao raciocínio de quem implementou, mediu por leitura, por
execução e por três mutações próprias. **Nenhum dos cinco teria sido apanhado pelos critérios de
aceitação deste ticket.**

**F1 — a recusa é uma QUEBRA REAL, e a mensagem prescrevia o remédio errado.** O `net/http`
converte `req.URL.User` em `Authorization: Basic`, pelo que um verificador de attestation atrás de
um proxy com basic-auth **funcionava** assim — e a primeira mensagem mandava «passe-as pelo
ficheiro de token», que dá `Bearer`. Seguir a instrução não repunha esse deployment. A recusa
**mantém-se** — o eixo é o segredo e não o transporte, e uma credencial num URL de ambiente
aparece na tabela de processos, no `inspect` do contentor e em qualquer erro que ecoe o endereço,
sítios que não se fecham caso a caso —, mas passa a ser declarada como quebra: a mensagem nomeia
os **dois** remédios, as três entradas de `deploy/node/README.md` dizem-no, e o `CHANGELOG` marca-a.
Foi a suposição por trás desta correcção que se revelou falsa: que ninguém usava `user-info`.

A lacuna que a quebra deixa — o nó não tem hoje forma de falar Basic com um verificador de
attestation — fica registada no **AOS-338**, e deliberadamente **condicional**: só se implementa se
alguém nomear um deployment que a usava.

**F2 — a defesa em profundidade parou no banner.** Os caminhos que **falam com a rede** continuavam
a ecoar o endereço, por dois ramos de naturezas diferentes. O `NewRequest` devolve um `*url.Error`
com a URL **como foi escrita** — era o único sítio do nó onde a **senha ia inteira** para o
`/readyz`; e o `Do` devolve um `*url.Error` em que o `net/http` redige a senha e deixa o
**utilizador**, que é precisamente quem este ticket argumenta ser sensível. Ambos fechados em
`cmd/aos/vaultkeyvault.go` (`erroVaultRedigido` preserva o `Op` e a causa e troca só o endereço).
O equivalente em `platform/broker/internal/vault/kvv2.go` **não** — esse módulo não pode importar
`integration` sem violar o `ADR-019` e precisa de redactor próprio: é o **AOS-337**, aberto e não
arrastado para aqui.

**F5 — as duas funções discordavam sobre o que é uma URL.** `CheckSecureTransportURL` só aparava
espaços no teste de vazio; `RedactURL` aparava antes do `Parse`. `"https://vault:8200 "` — um
espaço a mais num `.env` — abortava o arranque com «malformada (valor omitido)», e como esse ramo
por desenho não pode ecoar o valor, o operador não tinha como ver que o problema era um espaço.

**F4 — um ramo sem cobertura.** O caso sem esquema devolvia `vault:8200`, uma forma que o
doc-comment não promete. Passa a compor-se a partir dos dois campos preservados, e tem caso.

**F3 — documentação.** README e `CHANGELOG` não acompanhavam uma mudança fail-closed em três
variáveis documentadas. Fechado.

Mais quatro funções de teste e uma prova de mutação por cada correcção: o ramo de parse a devolver
o erro cru mostra a senha inteira, o de transporte mostra o utilizador, e remover o `TrimSpace`
recusa URLs legítimos. As três revertidas e confirmadas limpas.

---

## AOS-334 — Nada exige que uma entrada `mcp_server` traga `ManifestDigest`

### Contexto

O AOS-320 fez o `Host.stage` gravar o digest do manifesto no contrato de um `mcp_server`. Mas nem
`registry.validateContractSchemas`, nem `validateContract`, nem `Entry.Validate` verificam o campo
por `kind`: uma entrada `mcp_server` publicada com `Contract{Egress: internal}` é **aceite**, e volta
a ter o digest-constante-da-classe que o AOS-320 existe para eliminar.

Agrava-se com o próprio teste de regressão: `digest/golden_regressao_test.go` congela esse valor como
golden suportado — legitimamente, porque prova a não-regressão de `tool`/`skill`, mas o efeito é que
a forma vazia fica fixada como válida.

Hoje só o `Host.stage` publica `mcp_server`, pelo que a exploração exige outro caminho de publicação.
**A propriedade central do AOS-320 está garantida por convenção, não por gate.**

### Critérios de Aceitação

- [ ] Publicar um `mcp_server` sem `ManifestDigest` é recusado fail-closed, com erro atribuível
- [ ] Um teste que prove a recusa, e um controlo que prove que `tool`/`skill` **sem** o campo
      continuam a publicar (o campo é específico do kind)
- [ ] O golden de regressão distingue «forma legada suportada para tool/skill» de «forma vazia
      aceitável para mcp_server» — a segunda deixa de ser fixada como válida

### Estado

**POR IMPLEMENTAR.** P2. Fecha por gate o que o AOS-320 fechou por convenção.

---

## AOS-335 — `ClassifyContract` é cego ao manifesto: uma troca total de superfície promove como PATCH

### Contexto

`semver.ClassifyContract` classifica a mudança de contrato a partir de schemas e scopes. Um
`mcp_server` não tem nem uns nem outros — o seu contrato é `{Egress, ManifestDigest}` —, pelo que a
classificação devolve **sempre** `ChangeNone`, e `ValidateBump` aceita `1.0.0 → 1.0.1`.

Consequência concreta: um servidor MCP que ganhe uma tool `exec` re-pina confiança como **patch**. Não
fura o eval-gate (esse depende de `selfAuthored`), mas fura o **sinal de MAJOR** — e o
`tofu.Monitor.Reapprove` exige apenas versão estritamente superior.

Declarado como residual no AOS-320 «por mudar a semântica de AOS-052». A revisão adversarial mostrou
que não é neutro: **degrada** a classificação, não a deixa indefinida.

### Critérios de Aceitação

- [ ] `ClassifyContract` considera `ManifestDigest`: uma mudança do manifesto é classificada como
      MAJOR (ou como a classe que a equipa decidir, justificada contra o ADR-012)
- [ ] Um teste que prove que dois `mcp_server` com manifestos diferentes não são um bump PATCH
      válido
- [ ] O impacto na semântica de AOS-052 fica escrito — que artefactos passam a exigir MAJOR e porquê

### Estado

**POR IMPLEMENTAR.** P2. Residual nomeado no AOS-320, promovido a ticket porque a revisão mostrou que
não é uma omissão neutra.

---

## AOS-336 — O custo indefinido não atravessa a fronteira GW→RT, e o guarda a jusante é fraco

### Contexto

Dois defeitos do mesmo eixo, e nenhum se fecha sem o outro.

**A marca não atravessa.** `translateResponse` (`model-gateway/runtime_adapter.go`) copia três campos
numéricos para `agentruntime.ModelResponse`, que não tem campo para «indefinido». Num deployment cuja
tabela de preços não cubra o par configurado, o recorder é nil, `recordCost` retorna cedo, e o
`turn.recorded` recebe `cost_micro_usd: 0` para uma chamada **não medida** — o zero silencioso que o
AOS-321 fechou dentro do gateway, reaparecido um passo a jusante.

**E o guarda que devia apanhá-lo é mais fraco do que o AOS-321 declarou.** `ErrBurndownNoUsage`
(`cmd/aos/burndown_ledger.go`) testa `cur.turns > 0 && cur.turnTokens == 0` sobre um cursor
**cumulativo por run**: um único turno com usage desarma-o **para sempre**, e todos os turnos não
medidos seguintes contam 0 em silêncio. Não apanha «ao fim de N turnos» — só apanha se **todos** os
turnos forem zero.

O AOS-321 declarou o primeiro como residual e nomeou o segundo como mitigação. A revisão adversarial
mediu que a mitigação não mitiga o caso misto, que é o realista.

### Critérios de Aceitação

- [x] `agentruntime.ModelResponse` transporta a distinção «custo indefinido» vs «custo zero medido»,
      e o `turn.recorded` regista-a
- [x] `ErrBurndownNoUsage` apanha um run MISTO — turnos medidos e não medidos —, não só o run
      inteiramente a zero
- [x] Um teste de run misto que prove que o burn-down não conta os turnos não medidos como zero
- [x] O AC4 do AOS-321 deixa de valer só «enquanto houver recorder composto»

### Estado

**IMPLEMENTADO.** P1 — era o único destes que reabria um zero silencioso no caminho que o nó corre
hoje.

**A marca atravessa, e o canal fica com um só critério.** `agentruntime.Usage` ganha `Ausente` e
`Definido()`, no molde exacto de `port.Usage` do outro lado da fronteira; `translateResponse`
projecta `!resp.Usage.Definido()` — e **não** `.Ausente` —, porque são duas formas de ausência: o
`usage` em falta e o `usage` presente sem contadores legíveis (`{}`, `{"total_tokens":1500}`), que
é a segunda forma que escapou à primeira versão do AOS-321. Projectar a marca crua deixaria a
segunda atravessar disfarçada de medição.

**O evento durável regista-a**, que é o passo sem o qual nada disto sobrevive à fronteira que o lê:
`turn.recorded` ganha `usage_ausente`. `omitempty` é deliberado e não cosmética — um turno medido
grava exactamente os bytes que gravava antes, pelo que nenhum golden de replay se move e a mudança
é aditiva. Há um teste de controlo só para isso.

**O guarda passa a ser por turno.** `ErrBurndownNoUsage` perguntava `turns > 0 && turnTokens == 0`
sobre um cursor **cumulativo por run**: um único turno com usage desarmava-o para sempre. Passa a
contar `turnsSemUsage` e a denunciar a partir do primeiro. **Duas vias para a mesma conclusão**: a
marca do produtor, que é autoritativa, e `input_tokens <= 0`, que é defensiva e mantém o guarda de
pé para eventos gravados por código que não escreve a marca. A via defensiva é o mesmo critério de
`port.Usage.Definido` e de `cache_sli.CallRate` — sem denominador não há leitura.

**O AC4 do AOS-321 deixa de ser condicional.** Valia «enquanto houver recorder composto», porque
sem contabilidade de custo o gateway serve a resposta e a marca não chegava ao nó. Chega. O
resultado é que a postura deixa de depender de o operador ter montado uma tabela de preços.

**Um teste que existia FIXAVA o defeito.** O bloco de não-vacuosidade de
`TestAOS261_TurnosSemTokens_ErroExplicitoNuncaZero` gravava um quarto turno medido no mesmo run e
exigia que a leitura passasse a valer — declarando como comportamento pretendido exactamente o que
a revisão adversarial mediu como defeito: um turno medido reabilitava três cegos, e 15 tokens em 4
turnos eram apresentados como o consumo do run. Foi reescrito para provar o que um controlo de
não-vacuosidade tem de provar — que a guarda **distingue**, não que se cala — num run com todos os
turnos medidos.

**O banner deixa de mentir.** Prometia fail-closed «quando o ledger existe mas somou ZERO tokens»,
que era a descrição fiel do guarda antigo e do seu buraco. Passa a declarar que basta **um** turno
não medido, e nomeia o run misto.

**A AUDITORIA DE COMPLETUDE apanhou o AC4 a meio.** Os testes provavam a projecção isolada e o
consumo isolado — as duas pontas —, e não a TRAVESSIA na composição que produz o defeito.
`TestAOS336_SemRecorderAMarcaChegaAoRuntime` compõe um gateway **sem contabilidade de custo**, que
é o que um nó tem quando a tabela de preços não cobre o par configurado, e exige que a marca chegue
a `agentruntime.ModelResponse` — com o controlo de que a resposta continua a ser servida, senão
ter-se-ia trocado um zero silencioso por um caminho de modelo partido. Sem ele o AC4 continuaria a
valer «enquanto houver recorder composto», que é a frase que este ticket foi aberto para apagar.

Onze funções de teste em três módulos, com controlos em cada um: o usage medido continua definido,
o turno medido grava os mesmos bytes, e um run inteiramente medido continua a passar. Prova de
mutação executada nas três peças — desligar a projecção, o registo ou o guarda avermelha os testes
do módulo respectivo, e a mutação do guarda avermelha também `AOS-261` e `AOS-287`.

**LIMITES DECLARADOS.** `TurnSettlement` e `Result.TotalCostMicroUSD` continuam a somar zero por um
turno não medido — a marca chega-lhes (`TurnSettlement.Usage` é o mesmo tipo) mas nenhum a lê. O
dano é limitado a um turno, porque o burn-down aborta o run na fronteira seguinte; fechá-lo é outro
eixo, e fica nomeado em vez de arrastado. O caminho de **streaming** não passa por aqui: o
adaptador RT→GW só usa `Chat`, e `translateResponse` é o único funil.

---

## AOS-337 — O cliente Vault do broker ecoa o endereço nos erros, e um dos ramos ecoa-o cru

### Contexto

Medido na **revisão adversarial do AOS-333**, que fechou esta fuga no helper de validação e no
banner e parou aí. Os caminhos que **falam com a rede** continuam a ecoar o endereço, e é neles que
um endereço com credenciais embutidas aparece por extenso.

`packages/platform/broker/internal/vault/kvv2.go`:

- `:236` — `Ready` devolve `err` **cru** do `http.NewRequestWithContext`. Esse erro é um
  `*url.Error` que traz a URL **como foi escrita**, sem a redacção de senha que o `http.Client`
  aplica aos **seus** erros de transporte. Medido: `parse "http://admin:s3cr3t@vault:82 00/x":
  invalid port` — a senha **inteira**.
- `:207` e `:241` — o `%v` do wrap sobre o erro do `Do` traz a forma redigida pelo `net/http`
  (`http://admin:***@host/…`): a senha desaparece, **o utilizador não**. O AOS-333 argumenta
  explicitamente que o utilizador também é sensível — «numa URL de Vault ele identifica o
  principal» — e é precisamente ele que sobrevive aqui. O equivalente em `cmd/aos` já foi fechado
  com `erroVaultRedigido`, que é o molde a replicar (preserva `Op` e causa, troca só o endereço).

~~Os dois caminhos alimentam o `/readyz` e o banner de prontidão, que é onde um segredo seria mais
lido.~~ **ERRADO, e medido na revisão da implementação:** `KVv2.Ready` não tem chamador de
produção e nem podia ter — a porta `vault.Client` declara só `Fetch`; o `/readyz` do nó sonda o
Vault da **KEK**, e o banner do broker imprime `RedactURL(s.Addr)` e nunca um erro. O eixo é real
mas o alcance é **preventivo**, e a frase acima fica riscada em vez de apagada porque foi ela que
justificou a prioridade deste ticket.

**ALCANCE, medido e limitado.** A via do **ambiente** está fechada: `AOS_BROKER_VAULT_ADDR` passa
por `integration.CheckSecureTransportURL`, que recusa `user-info` desde o AOS-333. Fica aberta a via
programática — `NewKVv2` só verifica `addr != ""` (`kvv2.go:112-114`) — que é exactamente a via que
o teste de banner do AOS-333 invoca como justificação da sua própria defesa em profundidade. Ou
seja: o banner ganhou segunda camada e os clientes de rede não.

**PORQUE NÃO FOI FEITO NO AOS-333.** `packages/platform/broker` **não pode** importar
`packages/integration` — é o composition-root, e a fronteira do `ADR-019` corre no sentido
contrário. Reutilizar `integration.RedactURL` está fora de questão; o pacote precisa do seu próprio
redactor, ou de não ecoar de todo. É trabalho de desenho num módulo diferente, e por isso é ticket
e não alargamento silencioso de âmbito. Os dois ramos equivalentes em `cmd/aos/vaultkeyvault.go`,
que **podem** importar `integration`, foram fechados no próprio AOS-333.

### Critérios de Aceitação

- [x] Nenhum caminho de erro de `kvv2.go` ecoa `user-info` — nem a senha, nem o utilizador
- [x] O ramo de `NewRequest` falhado deixa de devolver o `*url.Error` cru
- [x] O erro continua a nomear **onde** o nó falhou a falar (esquema, host e porta), senão troca-se
      uma fuga por um `/readyz` que não diagnostica nada
- [x] O redactor vive no módulo do broker, sem importar `integration` — e `layer-lint.sh` prova-o
- [x] Um teste com `user:pass@` composto **programaticamente** (a via que continua aberta) que
      exija a ausência das duas coisas nas mensagens de `Fetch` e de `Ready`

### Estado

**IMPLEMENTADO.** P2 — o eixo era fuga de segredo, com a via do operador já fechada pelo AOS-333
e a programática aberta.

**ERAM QUATRO RAMOS, NÃO TRÊS.** Este ticket contava `:207`, `:236` e `:241` e falhou o
`NewRequest` do `Fetch`, que tem exactamente o mesmo defeito do de `Ready`. São **dois** ramos de
`NewRequest`, que ecoavam o endereço **cru** — a senha inteira, e com ela o **path do segredo**
(`/v1/secret/data/p/eu/cap_http.get`), que diz a quem lê o log qual a credencial em causa — e
**dois** de `Do`, em que o `net/http` redige a senha e deixa o utilizador. Medido na prova de
mutação, não deduzido.

**DUAS CAMADAS, E A ORDEM IMPORTA.** O controlo primário é `NewKVv2`, que recusa `user-info`
**fail-closed**: fecha os quatro ramos de uma vez no ponto de entrada, em vez de os remendar um a
um à saída, e torna impossível escrever o teste de fuga que existia — que é a forma forte da
garantia. **Não é um critério de transporte** e por isso não duplica o do nó: não decide `http` vs
`https` nem loopback, não tem política de esquema nenhuma. A redacção fica como defesa em
profundidade, e continua a valer para o **path do segredo**, que não é user-info e sairia por ali
na mesma.

**O REDACTOR DESCEU A `substrate/redaction`, E NÃO DEVIA TER SIDO COPIADO.** A primeira versão
desta correcção duplicou-o no pacote `vault` alegando que o `ADR-019` obrigava. **Não obrigava**:
`platform → substrate` é canónico (`LAYER_ALLOWED[platform]="platform substrate"`),
`substrate/redaction` é módulo **folha** com zero dependências, e o próprio `ADR-019` §117
nomeia-o como o sítio da redacção. A fronteira nunca exigiu a cópia — exigia que o código
partilhado **descesse**. E as duas cópias já tinham divergido no dia em que a segunda nasceu:
`(inválido)` contra `(inválida)`, o bastante para um `grep` sobre logs agregados perder metade dos
casos. `integration.RedactURL` e `cmd/aos.erroVaultRedigido` passam a delegar; ficam **zero**
cópias, e a incoerência interna do ticket — recusar duplicar o validador por risco de divergência
e aceitar duplicar o redactor — desaparece.

**`TransportError` desce pelos `*url.Error` aninhados.** `errors.As` apanha o de fora, e
reimprimir o `Err` tal-qual deixava sair **inteiro** um `*url.Error` interior — a credencial de um
proxy de terceiros, pela via do seam público `HTTPClient`. Cada nível é redigido com o **seu**
endereço, para não se perder contra quem se falhou.

**O `mount` passa a ser escapado**, e não era. Vinha de `AOS_BROKER_VAULT_KV_MOUNT` com só
`TrimSpace`, ao contrário dos segmentos do path: um escape inválido fazia o `NewRequest` falhar, e
a mensagem nova acusaria um endereço **perfeito**, mandando o operador depurar a variável errada.

### Duas declarações minhas eram falsas, e a revisão adversarial apanhou-as

**O «segundo controlo» era vacuoso.** Escrevi que «a causa sobrevive à redacção» e amarrei-o com um
`t.Logf` — o revisor apagou a causa por completo e a suite passou. A razão pela qual era um log é
real (o texto do SO varia por sistema e por locale), mas a saída não é desistir da asserção: é
compará-la com o **erro cru**, que é portável. Um controlo que não falha não é um controlo.

**A justificação de alcance era falsa.** Escrevi, no código, no commit e no `CHANGELOG`, que estes
erros «sobem ao `/readyz` e ao banner de prontidão». Não sobem: `KVv2.Ready` **não tem chamador de
produção** e nem podia ter, porque a porta `vault.Client` declara só `Fetch`; o `/readyz` do nó
sonda o Vault da **KEK**, e o banner do broker imprime `RedactURL(s.Addr)` e nunca um erro. A
correcção é **preventiva** — vale, porque o dia em que `Ready` for exposto ninguém terá de reabrir
isto —, mas a razão escrita tem de ser a verdadeira. Foi um doc-comment a comprar confiança que
não sustentava que abriu o AOS-333; repeti-lo no ticket que ele gerou seria o mesmo defeito.

**O `Ready` ganhou também atribuibilidade**: devolvia `err` sem sentinela nenhuma.

Nove funções de teste, com **três controlos**: os endereços legítimos continuam a construir
cliente, a mensagem de transporte continua a nomear host e porta, e a causa sobrevive. Prova de
mutação em oito peças — os quatro ramos de erro, o fail-closed do construtor, o desaparecimento da
causa (a mutação com que o revisor demonstrou a vacuidade), a descida nos aninhados e o escape do
mount. Todas revertidas e confirmadas limpas.

**LIMITES DECLARADOS.** A causa de um erro de transporte pode conter uma URL em **texto**, não
estruturada — o `net/http` produz `failed to parse Location header "…"` a partir de um cabeçalho
que o **servidor** controla. Não é redigível estruturalmente e não é omitida, porque omitir a causa
custaria o diagnóstico que a função existe para preservar; é conteúdo do interlocutor, não segredo
do nó. E o `layer-lint.sh` não cobre imports de **teste** (`go list` sem `TestImports`), pelo que a
prova da fronteira é o `go list -deps` de produção, que dá zero.

---

## AOS-338 — O AOS-333 removeu a basic-auth do verificador de attestation e não pôs nada no lugar

### Contexto

O **AOS-333** passou a recusar `user-info` em `CheckSecureTransportURL`, e isso fechou uma via de
autenticação que **funcionava**: o `net/http` converte `req.URL.User` em `Authorization: Basic`,
pelo que `AOS_ATTESTATION_VERIFIER_URL=https://svc:pw@attest.interno/verify` autenticava contra um
verificador atrás de um reverse-proxy com basic-auth. Medido na revisão adversarial do AOS-333, não
suposto.

A recusa está certa e mantém-se — uma credencial num URL de ambiente aparece na tabela de
processos, no `inspect` do contentor e em qualquer erro que ecoe o endereço, e nenhum desses sítios
se fecha caso a caso. **O que não está certo é o buraco que ficou.**

`RemoteAttestationConfig` só tem `AuthToken`, enviado como `Authorization: Bearer`
(`packages/integration/remote_attestation.go:75-77`, `:247-251`), alimentado por
`AOS_ATTESTATION_VERIFIER_TOKEN_PATH` (`packages/cmd/aos/main.go:809-814`). Um operador cuja
autoridade de attestation fala Basic **não tem hoje caminho no nó**: ou muda o proxy para Bearer,
ou termina a autenticação antes dele. Para os dois Vaults isto não é problema — autenticam por
`X-Vault-Token` e ignoram o Basic —, pelo que o eixo é só o terceiro chamador.

**ESTE TICKET É CONDICIONAL, e é assim que deve ficar registado.** Nenhum deployment que use esta
via foi nomeado; a necessidade é inferida de a via ter existido e de a documentação nunca a ter
desaconselhado. **Não implementar sem um caso real**: acrescentar um segundo esquema de
autenticação a uma superfície de segurança é permanentemente mais caro do que não o ter, e o
remédio já documentado — Bearer por ficheiro montado, ou terminação no proxy — serve a maioria dos
deployments. Se ninguém o pedir, o destino honesto deste ticket é **fechar sem código**, com a
razão escrita.

### Critérios de Aceitação

- [x] A credencial entra por **FICHEIRO MONTADO**, nunca por variável de ambiente — é a mesma
      regra que motivou a recusa do URL, e viola-la aqui reabriria a fuga por outra porta
- [x] Formato do ficheiro declarado e validado fail-closed (uma linha `utilizador:senha`, no molde
      do `-u` do `curl`); ficheiro ilegível, vazio ou sem `:` **ABORTA** o arranque
- [x] **Mutuamente exclusivo com o Bearer.** Os dois definidos ⇒ **ABORTA** com erro atribuível.
      Dois `Authorization` é um defeito; escolher um em silêncio é pior, porque o operador fica a
      crer que a outra credencial está a ser usada
- [x] Nenhum caminho ecoa a credencial — nem o utilizador: o banner de postura, as mensagens de
      erro do adaptador e o `/readyz` seguem o critério que o AOS-333 fixou
- [x] O banner **declara qual dos esquemas está composto**, derivado do estado e não da intenção
      de configuração — um nó que diz «attestation LIGADA» sem dizer como se autentica esconde
      metade da postura
- [x] O critério de transporte **não relaxa**: Basic continua sujeito a `https`, ou `http` só em
      loopback. Basic sobre claro é a credencial em claro, e é o mesmo eixo do AOS-249
- [x] Um teste que prove o header construído, a exclusão mútua a abortar, e a ausência da senha e
      do utilizador em erro e banner
- [x] `deploy/node/README.md` ganha a entrada, e a nota de migração do AOS-333 passa a apontar
      para cá em vez de dizer só «termine a autenticação no proxy»

### Estado

**IMPLEMENTADO.** Era P3 e condicional, e a condição escrita **não foi cumprida como estava
escrita**: dizia «só sobe se alguém **nomear** um deployment que usava a basic-auth embutida no
URL», e nenhum deployment foi nomeado. O que houve foi uma instrução directa do dono do
repositório para implementar — que supera a condição, mas não é a condição. Fica dito assim
porque a alternativa era dressar uma decisão de prioridade como se fosse a evidência que o
ticket pedia, e este epic inteiro é sobre não fazer isso. O custo que o ticket registava mantém-se e é o
que molda o desenho: passa a haver **dois** esquemas de autenticação nesta superfície, e a
exclusão mútua fail-closed entre eles é o que impede que isso vire ambiguidade sobre qual
credencial está realmente em uso.

**A INVARIANTE VIVE NO TIPO QUE A VIOLARIA.** A exclusão mútua está no construtor do adaptador
(`integration.ErrRemoteAttestationAuth`) e não no wiring do nó, porque é o adaptador que emitiria
os dois cabeçalhos. Pô-la em `nodeConfigFromEnv` dispararia **antes** de a URL ser sequer
validada, e daria ao operador uma queixa sobre a credencial quando o que está errado é o
endereço. A ordem passa a ser a dos dois Vaults: transporte primeiro, credencial depois — e há um
teste que a fixa, com os dois erros presentes ao mesmo tempo.

**GUARDA-SE O CABEÇALHO, NÃO A CREDENCIAL.** O campo `token` deu lugar a `authHeader`, o valor já
formado. Há exactamente **um** sítio onde uma credencial de attestation é formatada — o
construtor — e o caminho de pedido não volta a tocar-lhe; um esquema novo não acrescenta um `if`
ao envio.

**O BANNER DECLARA O ESQUEMA, DERIVADO DO ESTADO.** `AuthScheme()` lê o verificador
**construído**, não a configuração pedida. Antes disto, um nó que autentica e um nó que fala
anónimo com o componente produziam a mesma linha — e são posturas materialmente diferentes. O
teste de controlo exige que os três estados produzam linhas **distinguíveis**, porque um banner
que dissesse sempre o mesmo passaria cada caso isolado.

**FAIL-CLOSED DO FORMATO**, no molde do `-u` do curl: sem `:` recusa, sem utilizador antes do `:`
recusa, e caracteres de controlo recusam — um `\r\n` numa credencial lida de ficheiro é
**injecção de cabeçalho**, e o `net/http` recusá-lo-ia no envio, mas isso seria uma falha
por-verificação num gate que já negou a aprovação. A senha **pode** conter `:`; só o primeiro
separa, e há um caso para isso — sem ele, uma implementação que partisse por todos os `:`
passaria os testes de recusa e rejeitaria senhas geradas legítimas.

### Duas coisas que este ticket fechou e não estavam nos seus critérios

**O caminho da attestation era o outlier, e agora aborta em ficheiro vazio.** Os dois Vaults
abortam num ficheiro de credencial vazio; este lia-o, aparava e seguia com a credencial a vazio —
ou seja, com o nó a falar **sem autenticação nenhuma**, e sem nada no arranque a dizê-lo. Foi
fechado porque era tecnicamente necessário: o critério do banner exige declarar o esquema
composto, e um ficheiro vazio tornaria essa declaração dependente de um estado que ninguém pediu.
**É uma QUEBRA** — um deployment com ficheiro de token vazio arranca hoje e passa a abortar — e
está declarada em `deploy/node/README.md` e no `CHANGELOG`.

**O erro de leitura deixou de ecoar o caminho, e isso apareceu num teste meu.** Escrevi
`TestAOS338_ACredencialNaoEntraPorVariavelDeAmbiente` e ele ficou vermelho contra o meu próprio
código: se o operador puser a credencial **na variável** em vez do caminho — o erro que uma
variável chamada `..._PATH` convida —, então «o caminho» É a credencial, e eu ecoava-o. Passa a
nomear a **variável** e a **classe** da falha (inexistente / sem permissão / ilegível), nunca o
valor. É o critério do «malformada (valor omitido)» do AOS-333.

### A revisão adversarial encontrou onze defeitos, e três eram declarações minhas falsas

**A minha prova de mutação não provava o que eu disse.** Declarei cinco provas vermelhas; o
revisor mediu que **três sobreviviam** aos mutantes REALISTAS, e só morriam aos grosseiros que eu
tinha usado. `StdEncoding` → `URLEncoding` passava porque o meu par-fixture codifica
**identicamente** nos dois alfabetos — nenhum dos seus bytes mapeia para `+` ou `/`. Remover a
guarda do ramo **bearer** passava porque o meu teste de controlos só cobria o basic. E derivar o
esquema da `Config` em vez do verificador passava, porque hoje o esquema é função total da
config. Os dois primeiros têm agora gate; o terceiro está declarado abaixo como não-gatável.

**A guarda estava do lado errado, e isso era um defeito real.** No ramo basic o `base64` já
neutraliza qualquer byte, pelo que ali a recusa não previne injecção nenhuma. No ramo **bearer**,
onde o valor vai cru, o meu critério era `CR`/`LF`/`NUL` — mais estreito do que o do `net/http`,
que recusa todo o controlo excepto `TAB`. Medido: um token com `\v`, `\f`, `ESC` ou `DEL`
construía o verificador, o banner declarava «autentica com Authorization: Bearer», e **cada**
verificação falhava no envio: boot verde, `/readyz` verde, e **todas as pernas de aprovação
negadas**. Fail-late num gate é o pior sítio para o ser.

**O AC do README estava marcado `[x]` e era falso.** Actualizei a linha da variável nova e a do
token, e a **nota de migração** vive na linha do `AOS_ATTESTATION_VERIFIER_URL` — que continuava a
dizer «o nó não tem hoje caminho». Um operador cujo boot aborta lê essa linha e migra para o
proxy, que é o desfecho que este ticket existe para evitar.

**A exclusão mútua tinha dois contornos.** O `TrimSpace` corria **antes** do teste de conflito,
pelo que um lado só com espaços desaparecia e o outro era escolhido em silêncio — com os dois
definidos, que é o que o critério proíbe. E a invariante só corre com URL e approvers presentes:
montar os dois ficheiros sem URL arranca verde e ignora as duas credenciais. O primeiro está
fechado (a normalização saiu do resolvedor); o segundo é consequência de a invariante viver no
construtor, e fica declarado.

**Uma armadilha de migração que eu não vi.** O `user-info` de um URL é **percent-decoded** pelo
`net/http` antes de virar `Basic`: `us%40er:p%3Aw` enviava o par real `us@er:p:w`. Uma senha com
`@` ou `:` obriga a percent-encoding no URL — ou seja, é exactamente o deployment que este ticket
serve — e copiar o literal para o ficheiro dá **outra credencial**, sem nada no arranque a
explicá-lo. Está agora no README.

**«Molde do `-u` do curl» não era verdade.** Eu aparava tudo; o `curl` preserva um espaço final
numa senha. Passa a aparar **só o terminador de linha** — que é do ficheiro — e o bearer mantém o
`TrimSpace` que o AOS-177 já fixava, porque um token opaco não tem espaço com significado.

Mais um tecto de tamanho na leitura (apontar a variável a `/dev/urandom` pendurava o arranque), a
recusa de um token com o esquema já colado, um separador em falta no banner, e um teste meu que
imprimia a credencial no ramo de falha.

Dezasseis funções de teste em dois módulos, com **cinco controlos**: o Bearer não se partiu, falar
sem autenticação continua legítimo, o loopback continua aceite com Basic, o `TAB` continua aceite
(recusá-lo endureceria para lá do critério do `net/http`), e os três estados do banner são
distinguíveis. Prova de mutação refeita com os mutantes **realistas** — alfabeto base64, guarda do
bearer removida, critério estreitado, `TrimSpace` de volta ao resolvedor, `TrimSpace` de volta à
leitura — todas vermelhas e revertidas limpas.

**LIMITES DECLARADOS.** O componente de referência `cmd/aos-attestation` **não lê `Authorization`
de todo** — o gap já existia para o Bearer — pelo que o esquema novo não é exercitável
ponta-a-ponta contra o binário do repositório, só contra `httptest`. O molde de leitura de
credencial dos **dois Vaults** ecoa o caminho na mensagem de erro, que é o mesmo defeito que este
ticket fechou do seu lado. Bytes ≥ 0x80 atravessam (o `net/http` aceita-os), pelo que um `U+2028`
numa credencial é enviado — não é injecção, não há CR nem LF, e recusá-lo partiria credenciais
UTF-8 legítimas. E **a propriedade «o banner deriva do estado e não da intenção» não tem gate**:
hoje o esquema é função total da config, pelo que nenhum teste consegue distinguir as duas
implementações pelo comportamento. O `AuthScheme()` mantém-se porque é a forma certa para o dia em
que divirjam — mas isso é um argumento de desenho, não uma prova, e é assim que fica escrito.

**LIMITES DECLARADOS.** O componente de referência `cmd/aos-attestation` **não lê `Authorization`
de todo** — o gap já existia para o Bearer — pelo que o esquema novo não é exercitável
ponta-a-ponta contra o binário do repositório, só contra `httptest`. E o molde de leitura de
credencial dos **dois Vaults** ecoa o caminho na mensagem de erro, que é o mesmo defeito latente
que este ticket fechou do seu lado; fica nomeado em vez de arrastado.
