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
| **P1** | AOS-322, AOS-323, AOS-326 | Um alarme de RGPD que não sabe ficar vermelho, um canal de segredos sem TLS obrigatório, e a raiz de governação que explica os dois |
| **P2** | AOS-324, AOS-325, AOS-327 | Pré-requisito do wiring do broker; reconciliação documental; regra de alerta |

### 0.2 Tabela-resumo

| Ticket | Defeito | P | Alcance | Estado |
|---|---|---|---|---|
| AOS-320 | O digest de um `mcp_server` é uma constante da classe de egress — três valores para todo o universo | P0 | latente | **por implementar** |
| AOS-321 | Uma resposta 200 sem `usage` é indistinguível de uma chamada de custo nulo | P0 | **nó** | **por implementar** |
| AOS-322 | O `/readyz` nunca fica vermelho por *crypto-shred* pendente com a custódia de referência | P1 | **nó** | **por implementar** |
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

**POR IMPLEMENTAR.** P0. Alcançável no nó: o adaptador OpenAI-compatível e `NewProduction` estão
compostos em `packages/cmd/aos/modelgatewaywiring.go`.

---

## AOS-322 — O `/readyz` nunca fica vermelho por *crypto-shred* pendente com a custódia de referência

### Contexto

Quando uma destruição de KEK falha, o fluxo DSAR sela `dsar.shred_unconfirmed` na cadeia e o nó
**deve** ficar *unready* até uma destruição confirmada — é o que `dsar.go:245` diz ao operador em
texto: «o `/readyz` fica VERMELHO ate uma destruicao confirmada e o conteudo pode continuar
recuperavel».

Só que a pendência não chega ao `/readyz` directamente. Chega pela custódia: `api.go:1109` faz uma
asserção de tipo para `readinessProber` (`api.go:1138`), e só o `vaultKeyVault` a satisfaz — o seu
`ready()` termina em `shredFault()` (`vaultkeyvault.go:471-492`), que erra sse `len(shredPend) > 0`.

`audit.InMemoryKeyVault` tem exactamente três métodos — `EnsureKey`, `Key`, `Delete`
(`packages/platform/audit/keyvault.go:54,70,81`). **Não implementa `readinessProber`.** Logo, num
nó que não tenha ligado o Vault Transit — o default fora de `AOS_MODE=production`, e o único modo
que `ErrProductionNeedsDurableKEK` não cobre — uma destruição por confirmar **nunca** põe o nó
*unready*, nem sequer com uma só réplica.

O mesmo vale para a métrica: `api.go:1187` só emite `aos_dsar_vault_shred_unconfirmed` se
`shredPendingOf(h.node.DSARVault)` devolver `ok`. Com a custódia de referência a série **não
existe** — não é que fique a zero (ver AOS-327).

O alarme existe e está bem construído. A única custódia que o dispara é a que produção exige, e é
precisamente nos ambientes que não são produção que uma destruição falhada passa despercebida.

### Critérios de Aceitação

- [ ] Uma destruição por confirmar é observável no `/readyz` **independentemente da implementação de
      custódia composta** — por o `InMemoryKeyVault` passar a reportar pendência, por a pendência
      subir para um nível que não dependa da asserção de tipo, ou por o nó recusar compor uma
      custódia sem `readinessProber` (a escolha faz parte do ticket e fica justificada por escrito)
- [ ] Um teste que force uma destruição falhada com a custódia de **referência** e prove que o
      `/readyz` fica vermelho
- [ ] A afirmação de `dsar.go:245` passa a ser verdadeira para toda a custódia composta, ou é
      qualificada para nomear a que a sustenta
- [ ] A escolha não introduz um caminho em que o `/readyz` fique vermelho por uma pendência que já
      foi confirmada (o inverso de AOS-327)

### Estado

**POR IMPLEMENTAR.** P1. Achado N-01 da §8.2 da `analises/11` — nasceu numa refutação e nunca tinha
sido enunciado. Alcançável hoje, num nó único.

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
4. **`packages/platform/model-gateway/pipeline/stages.go:68-71` e `tecnica/06:325`** — ambos
   afirmam, como facto, a composição `RunPrefix.Turn → Guard.Admit`. Nem a composição existe em
   lado nenhum, nem o método `RunPrefix.Turn` existe: `cache/freeze` expõe `Assemble(turn, tail)`.
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

**POR IMPLEMENTAR.** P2. Barato, e é o que sustenta a confiança em todas as outras declarações — a
`analises/11` só conseguiu descartar sete falsos positivos porque as declarações que leu eram
verdadeiras.

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

**POR IMPLEMENTAR.** P1. É o ticket que fecha a assimetria do §0 e a condição que torna os outros
legíveis: sem ele, «latente» continua a significar coisas diferentes em epics diferentes.

---

## AOS-327 — A pendência de *shred* não tem regra de alerta, e a série desaparece com o processo

### Contexto

`api.go:1188` expõe `aos_dsar_vault_shred_unconfirmed` — «Destruicoes de KEK (crypto-shred) por
CONFIRMAR na custodia; >0 mantem o no unready e o conteudo pode continuar recuperavel». É a métrica
certa, com a descrição certa.

**Nenhuma regra de alerta em `deploy/` ou `docs/` a referencia.** E o gauge é por processo: quando o
processo morre, a série desaparece. Sem `absent()` ou `for`, o desaparecimento é indistinguível de
«resolvido» — e é precisamente o cenário que interessa, porque uma destruição por confirmar mantém o
nó *unready*, o que torna a substituição do processo provável.

Agrava-se com o AOS-322: a métrica só é emitida se `shredPendingOf(h.node.DSARVault)` devolver `ok`
(`api.go:1187`), o que exige a custódia Vault. Com a custódia de referência a série **não existe** —
não fica a zero. Uma regra ingénua sobre `> 0` nunca dispararia nem com o nó a arder.

A operação de remediação já existe e é barata: o `Delete` do `vaultKeyVault` relê a chave e exige
404 (`vaultkeyvault.go:600-608`), logo a destruição é idempotente e re-verificável. **O que falta é
o gatilho, não a operação** — nenhum job re-tenta a partir de sinal partilhado
(`retention_sweeper.go:315` só reporta a contagem).

### Critérios de Aceitação

- [ ] Existe regra de alerta sobre `aos_dsar_vault_shred_unconfirmed` que dispara em `> 0`
      **e** em `absent()`, com `for` que não a torne ruidosa num restart normal
- [ ] A regra distingue «a série não existe porque a custódia não a emite» de «a série não existe
      porque o processo morreu» — ou o AOS-322 remove a primeira possibilidade e isso fica citado
- [ ] Uma destruição por confirmar é re-tentada automaticamente, ou a ausência de re-tentativa fica
      declarada com o seu porquê
- [ ] O runbook de operação nomeia as causas típicas que `dsar.go:245` já lista (política Transit
      sem `deletion_allowed`, replicação, token sem autoridade) e o que fazer com cada uma

### Estado

**POR IMPLEMENTAR.** P2. Achado N-02 da §8.2 da `analises/11`.

---

*Epic derivado de `analises/11_Auditoria_MEM_REG_GW_BRK_Adversarial.md`. Cada âncora foi
reconfirmada contra o código antes da escrita. Ver `specs/INDICE.md`.*
