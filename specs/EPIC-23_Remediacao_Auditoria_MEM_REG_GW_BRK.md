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
| AOS-320 | O digest de um `mcp_server` é uma constante da classe de egress — três valores para todo o universo | P0 | latente | **por implementar** |
| AOS-321 | Uma resposta 200 sem `usage` é indistinguível de uma chamada de custo nulo | P0 | **nó** | **por implementar** |
| AOS-322 | A postura de confirmação do *crypto-shred* não é declarada, e nada obriga uma custódia nova a escolher | P2 | endurecimento | **por implementar** (enunciado original falsificado — ver ticket) |
| AOS-323 | O canal do broker para o Vault aceita `http://` e o token nunca é renovado | P1 | **nó** | **por implementar** |
| AOS-324 | A troca de credenciais não impõe nem exercita o eixo *Provider* | P2 | latente | **por implementar** |
| AOS-325 | Cinco declarações de estado que já não são verdade, e uma contradição interna | P2 | **nó** | **por implementar** |
| AOS-326 | A não-composição de MEM e REG não está registada em ADR nem em `DEF-NNN` | P1 | — | **por implementar** |
| AOS-327 | A pendência de *shred* não tem regra de alerta, e a série desaparece com o processo | P2 | **nó** | **por implementar** |

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

**POR IMPLEMENTAR.** P0. Latente enquanto `mcp.NewHost` não tiver chamador (ver AOS-326), mas o
defeito está materializado no código e a correcção não depende do wiring.

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

**RESIDUAL NOMEADO — o AC4 vale enquanto houver recorder composto.** `translateResponse`
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

**POR IMPLEMENTAR.** P1. A validação de esquema é o único item desta auditoria corrigível numa
linha e independente do wiring; o resto do ticket é contrato.

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

**POR IMPLEMENTAR.** P2 por alcance (latente — `broker.New` não tem chamador de produção), **alta
no dia do wiring**. É o defeito de desenho mais sério da auditoria.

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

**AC4 fica por fechar:** não foi criado mecanismo que faça uma declaração citar o ticket que a
desbloqueia. As sete correcções são pontuais; o que evitaria a próxima ronda é estrutural e fica por
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

**POR IMPLEMENTAR.** P2, âmbito reduzido. Achado N-02 da §8.2 da `analises/11`, cortado a meio por
uma declaração que já existia e por uma durabilidade que a auditoria não tinha visto.

---

*Epic derivado de `analises/11_Auditoria_MEM_REG_GW_BRK_Adversarial.md`. Cada âncora foi
reconfirmada contra o código antes da escrita. Ver `specs/INDICE.md`.*
