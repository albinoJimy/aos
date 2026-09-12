# Auditoria adversarial dos Serviços de Plataforma — MEM + REG + GW + BRK

| Campo | Valor |
|---|---|
| Documento | `analises/11_Auditoria_MEM_REG_GW_BRK_Adversarial.md` |
| Data | 2026-09-04 |
| Estado auditado | HEAD `061b9cf` (`wip(AOS-090)… ESTADO INTERMEDIO, NAO FUNDIR`), branch `feature/AOS-090-despromocao-por-anomalia`, árvore limpa. **Âncoras verificadas contra o ramo base** — ver §7.1 |
| Âmbito | **MEM** (`packages/platform/memory`), **REG** (`packages/platform/registry`, incl. `mcp/`), **GW** (`packages/platform/model-gateway`), **BRK** (`packages/platform/broker`), e as costuras entre eles |
| Tipo | Quatro lentes independentes → **refutação adversarial** (uma por eixo + uma por costura) → **medição executada no nó real** e no pipeline de release → **passagem 4**: custódia da KEK do DSAR sob múltiplas réplicas (§8) |
| Auditoria anterior | `analises/10_Auditoria_ORQ_SCH_PDP_Adversarial.md` (2026-09-03), cujo §7 deixa os serviços de plataforma por cobrir |
| Contratos verificados contra | `_BRIEF.md` §2, `specs/00_AOS_Carta.md`, `EPIC-04`, `EPIC-05`, `EPIC-06`, `EPIC-07`, `EPIC-18`, `EPIC-20`, ADR-003/005/006/008/009/011/012/017/019/021, `tecnica/04`, `05`, `06`, `07`, `12`, `14`, `17` |
| Remediação | Por abrir — ver §6 |

---

## 1. Método

Três passagens, com o ónus da prova invertido a cada uma.

**Passagem 1 — quatro lentes independentes.** Uma por subsistema, cada uma com dez dimensões
prescritas (conformidade contrato↔código, a invariante própria do eixo, fail-closed, determinismo,
isolamento, fronteiras de camada, operação). Instrução comum: âncora `ficheiro:linha` para tudo;
`NÃO VERIFICADO` como resposta legítima; e procurar activamente **testes que passam pela razão
errada**. Resultado: **32 hipóteses-defeito**, oito por eixo, cada uma enunciada de forma
falsificável e acompanhada de «como um adversário a poderia refutar».

**Passagem 2 — cinco refutadores.** Regra única: *o objectivo é derrubar a hipótese, não confirmá-la*.
Nenhum viu o raciocínio de quem acusou — receberam as hipóteses e o código, e refizeram cada grep.
Cada refutador teve de distinguir três estados que a passagem 1 confunde sistematicamente: **defeito**
(viola requisito vigente), **deferimento declarado** (o repositório sabe e registou-o) e **não-requisito**.
E teve de classificar a **alcançabilidade**: alcançável hoje, latente, ou inalcançável.

**Passagem 3 — medição.** O nó real levantado e conduzido ponta-a-ponta
(`.claude/skills/run-aos/driver.sh smoke`), e o pipeline de release inspeccionado no CI real
(`gh run view` sobre a execução de `v0.1.10`).

### 1.1 O que cada passagem mudou

| | |
|---|---|
| Passagem 1 → 2 | **9 hipóteses caíram** por evidência falsa ou lida fora de contexto; **7 foram reclassificadas** como deferimentos já registados; **1 foi agravada** além do alegado |
| Passagem 2 → 3 | A execução do nó confirmou a tese do §2 pela boca do próprio sistema; o CI fechou um `UNKNOWN` que a passagem 1 não conseguira resolver |

O saldo — **16 sobreviventes em 32** — é o número mais informativo deste relatório. Metade do que
quatro analistas competentes produziram não resistiu a uma tentativa honesta de o derrubar.

---

## 2. A tese: o padrão do plano de controlo, sem o ADR que o legitima

Os quatro eixos convergem numa frase: *o subsistema existe como biblioteca de qualidade e não está
composto no nó*. É literalmente a mesma frase que a auditoria 10 encontrou para o ORQ e o SCH.

Mas a auditoria 10 fechou-a com um princípio que se aplica aqui e muda tudo:

> **Um buraco que um ADR ratificado descreve não é descoberta — é a leitura do ADR.**

Para o plano de controlo, esse ADR existe: o ADR-018 §4 e o ADR-023 declaram-no, o `EPIC-10`/AOS-281
di-lo em palavras («não é wiring esquecido: é o ADR-018 a impedi-lo por desenho»), e um guard-test
(`packages/cmd/aos/boundary_orq_sch_test.go`) impõe-no.

**Para os serviços de plataforma, não existe.** Procurado em todo o `docs/adr/`: nenhum ADR declara
MEM, REG, GW ou BRK deliberadamente não-compostos. E a cobertura por deferimentos é grosseiramente
desigual — em 107 entradas do `REGISTO-Deferimentos.md`:

| Eixo | Menções | Cobertura da não-composição |
|---|:--:|---|
| **GW** | 13 | Boa — `DEF-280*` cobre routing, tokens, tiers; o banner de postura declara os residuais |
| **BRK** | 9 | Boa — `DEF-214`, `DEF-216`, `DEF-218`; `posture_banner.go:180` declara-o em cada arranque |
| **REG** | 3 | Parcial — `DEF-275` toca-lhe de lado; **o banner não diz nada sobre o REG** |
| **MEM** | 1 | **Nenhuma.** A única menção é a `DEF-302`, que é sobre o *key vault* do DSAR |

A não-composição da memória — a lacuna mais material do EPIC-04 — **não tem uma única entrada** no
registo que existe precisamente para a registar. E o `loop.go:320-331` sabe-o: declara em comentário
que «nenhum caminho de produção atribui `Goal.MemoryContext` (o EPIC-04 ainda não o ligou)», e
explica na linha seguinte que evitou a palavra-marcador para o gate `deferrals` não contar a citação.
A justificação é defensável — cita o `DEF-806`, não cria dívida nova. O efeito não é: a lacuna ficou
fora do registo, e o `DEF-806` é sobre separação de planos, não sobre wiring de memória.

Isto define o que conta como achado neste âmbito, seguindo a taxonomia da auditoria 10: **(a)** um
artefacto que diga algo **falso** sobre o estado; **(b)** um defeito **dentro** de um caminho que
corre; **(c)** uma lacuna **não coberta** por ADR nem por `DEF-NNN`.

---

## 3. Os sobreviventes

### 3.1 O digest constante — o mais grave, e o único agravado pela refutação

**H-REG-1.** O REG promete *pin + hash + assinatura*. Para entradas `kind=mcp_server`, o contrato
publicado por `mcp/host.go:283-290` é `domain.Contract{Egress: egress}` — só a classe de egress. O
`canonicalContract` (`digest/canonical.go`) serializa `kind|egress|InputSchema|OutputSchema|scopes`;
o endpoint do servidor vai para `Provenance.Origin`, campo fora do digest e fora da assinatura.

A passagem 1 alegou colisão entre servidores com a mesma classe. O refutador testou a atenuação
óbvia — *os schemas das tools descobertas entram no contrato?* — e apurou que **não**: entram em
entradas `kind=tool` separadas, com `ID = serverID+"/"+toolName` (`host.go:296-304`). São artefactos
distintos. Depois calculou os valores contra o código Go real:

```
none      → sha256:bd400861…
internal  → sha256:3924dad9…
external  → sha256:b3c1d64d…
```

Não é «dois servidores colidem». É uma **função constante de três valores** para todo o universo de
servidores MCP — e `stage` coage egress inválido para `internal` (`host.go:270`), estreitando ainda
mais. Uma assinatura sobre `(id, version, digest)` de um `mcp_server` autentica `(id, version, classe
de egress)`. Substituir o binário ou o endpoint por trás de `mcp.fs@1.0.0` preserva digest e
assinatura válidos.

**Nenhum teste em todo o repositório faz uma asserção sobre o digest de uma entrada `mcp_server`.**
Os vectores 1 e 3 da suite AOS-054 usam `domain.KindTool`. É por isso que sobreviveu.

*Atenuação honesta:* para `kind=tool` o pilar hash **não** é vacuário — o `InputSchema` sanitizado
entra no contrato e o Vector 1 prova-o. E o escopo declarado do rug-pull (`EPIC-05:17`, `tecnica/05:21`)
é mutação de *schema*, que as entradas `tool` cobrem.

*Agravante:* a peça que fecharia isto — `CapabilityManifest.Digest` — está declarada em
`mcp/protocol.go:147` como «RESERVADO (AOS-047). Vazio em AOS-046». O AOS-047 **entregou**
`digest.DigestJSON` e `DigestBytes`. Ninguém regressou para as ligar. É um deferimento declarado
**caducado** — classe (a) e (c) em simultâneo.

*Mitigação parcial que a passagem 1 não viu:* `toolset/frozen.go:408 computeHash` **inclui**
`s.MCPServer` (a origem) no hash do conjunto congelado. Mas `toolset.Expectation` tem só
`{ID, Version, Kind, Digest}` e a revalidação por chamada compara exactamente esses — **a origem não é
revalidada por chamada**.

**Classe (c). Alcançabilidade: latente** (depende de `mcp.Host`, que não tem chamador).

### 3.2 Medido no nó real

O smoke correu **9/9 verde**: run submetido e completado, read-path soberano a negar sem credencial
(404 na leitura, 403 na escrita), canal de controlo assinado a aceitar *pause*/*steer* e a recusar
emissor não pinado, autonomia assinada aplicada e selada, SSE de trajectória a emitir, WAL e WORM
coerentes, `aos_ready 1`.

**A cadeia de governação que o AOS promete corre mesmo.** Não é um repositório de bibliotecas com um
binário decorativo.

E o mesmo smoke entrega a prova mais limpa desta auditoria. O passo 2 devolve:

```
final=no `aos`: modelo de referencia (Model Gateway real = EPIC-06)
```

O nó completou um run inteiro com o **modelo de referência** — turno fixo, custo constante, nada sai
do processo. O Model Gateway real, com todo o determinismo provado do ADR-021, não participou. É a
tese do §2 dita pelo próprio sistema, em execução.

### 3.3 Confirmados, com alcance delimitado

**H-BRK-1 — confusão de deputado no eixo *Provider*.** `scope.go:82` avalia só `call.Capability`;
`exchange.go:242` monta `vault.Key{Provider, Region, Capability}` a partir do **pedido**; e
`capability.go:35` fixa `cap:http.post` para toda a troca, tornando a capability constante e o
provider o único discriminante.

O refutador encontrou a metade que a passagem 1 falhou: o eixo **Region** *é* mediável — a
`ObligationRegion` (`obligations.go:132-153`, `monitor.go:334`) nega cross-border comparando
`call.Resource.Region`, que `exchange.go:201` alinha com o eixo Region da chave do Vault. O eixo
**Provider** não tem imposição nenhuma. E acrescentou a razão pela qual sobreviveu: **todos os testes
do broker e da `security-tests` usam um único `provider` como constante**. O eixo não está só
desprotegido — não está exercido.

**Classe (c). Alcançabilidade: latente.** É o defeito de desenho mais sério da lista.

**H-GW-1 — `usage` ausente é indistinguível de custo nulo.** A passagem 1 alegou «custo zero por
falta de preço»; o refutador mostrou que isso é impossível (`ErrNoPrice` dispara antes de somar
qualquer token, `cost.go:203-210`) e reformulou para o ramo que sobrevive: `UnmarshalChatResponse`
(`port/normalize.go:107-113`) é um `json.Unmarshal` nu, logo um 200 sem o objecto `usage` produz
`port.Usage{}` zerado sem erro, que desce até ao span, ao agregado por run/árvore e ao evento durável
`turn.recorded`. Fail-open do burn-down (ADR-008), e contradiz o próprio comentário de `recordCost`.

A prova de que a disciplina é conhecida está no mesmo módulo: `metering/cache_sli/cache_sli.go:158`
faz exactamente o certo — `PromptTokens == 0 ⇒ SLI INDEFINIDO, nunca 0`.

**Classe (b). Alcançabilidade: no nó.**

**H-REG-4 / H-REG-6 — o trust store e a allowlist de egress.** O `TrustStore` é um mapa em memória
(`signing/truststore.go:41`) sem re-hidratação a partir do WORM: a «revogação terminal» é
intra-processo e não sobrevive a um reinício. E a verificação de egress por host está
**estruturalmente** inutilizável, não apenas desligada: mesmo que `WithEgressAllowlist` fosse
injectada, nenhum chamador de produção define `WithEgressHost` (`integration/revalhook.go:126`), pelo
que `Request.EgressHost` seria sempre vazio.

**H-MEM-1 / H-MEM-4 / H-MEM-8 — a memória como biblioteca.** O nó compõe `memory.NewService(memPort)`
(`bootstrap.go:2231-2233`), um CRUD genérico parametrizado por `domain.MemoryClass`, e faz **uma**
escrita episódica (`integration/ingestion.go:221`). `memory/episodic`, `semantic`, `procedural`,
`compression` e `migrations` têm **zero importadores externos não-teste**. E a `ports.MemoryPort` é
exportada: o `MemoryPortSink` do próprio pacote já a usa directamente (`window_manager.go:656`),
contornando a fachada que impõe proveniência, sem gate de camadas a impedi-lo.

**H-BRK-3 / H-BRK-4 / H-BRK-7.** O handle é bearer multi-uso sem binding a instância
(`injection.go:40`) e é publicado no payload do evento de auditoria (`exchange.go:284,297`).
`Revoke` e `Inject` não emitem evento nenhum — o que não viola requisito escrito (as citações da
passagem 1 estavam ambas erradas), mas quebra a correlação que o ADR-006 enuncia. E o canal
broker↔Vault não valida esquema: `http://` é aceite (`kvv2.go:75-77`, `broker_vault_env.go:53-84`),
o token é lido uma vez e nunca renovado, e `Ready()` não pertence à porta `vault.Client` — o que faz
disto um defeito de **contrato**, não de implementação.

**H-GW-6 — documentação que descreve wiring inexistente.** `cache/layout`, `cache/freeze` e
`cache/compaction` não têm consumidor de produção; o CA de AOS-060 é satisfeito a montante pelo
`agent-runtime` (`loop.go:645`). O refutador encontrou a forma forte: `pipeline/stages.go:68-71` e
`tecnica/06:325` afirmam ambos, **como facto**, a composição `RunPrefix.Turn → Guard.Admit` — que não
existe, e cujo método `RunPrefix.Turn` também não existe. **Classe (a).**

---

## 4. O que caiu, e porquê o modo importa

Nove hipóteses foram refutadas. O modo de falha é instrutivo, porque é o mesmo em todas: **a passagem
1 leu o código e não leu a decisão, nem seguiu o caminho até ao fim.**

| Hipótese | Porque caiu |
|---|---|
| H-MEM-3 | Alegou que «nada lê `cache_read_input_tokens` do GW». Falso: `metering/cache_sli/cache_sli.go:126-151` fá-lo, e o `EPIC-04:230` atribui explicitamente o SLI medido ao EPIC-06 |
| H-MEM-6 | O crypto-shredding do nó não é o `InMemoryKeyStore` da biblioteca: é `audit.KeyVault`/`Config.DSARVault`, com guarda `ErrProductionNeedsDurableKEK` que **aborta o arranque** com KEK em memória |
| H-MEM-7 | O TTL de produção é o `audit.ExpirationJob` (AOS-092/213) composto em `bootstrap.go:2205-2216`, não o `Sweep` da biblioteca |
| H-GW-5 | Leu `MemoryLedger` como índice de produção; o doc-comment declara-a impl de referência substituível, e os eixos compostos são todos *tenant-aware* (`Board`) |
| H-GW-7 | Ignorou que `FreezeToolSet` (`frozen.go:145`) recusa `runID == ""` fail-closed — o cenário é inconstruível |
| H-GW-8 | Duas afirmações negativas, ambas falsas: `source.go:250-259` **chama** `broker.Revoke`, e `TestSource_Revogacao` **assere** `broker.Revoked(leaseID)` |
| H-BRK-2 | Omitiu `CheckNoEscalation` + `authz.Authorize` no `ScopeGate` (`scope_gate.go:157-176`), que correm antes do dispatch, e `ErrScopeGateMissing`, que impede `NewProductionSecure` de arrancar sem eles |
| H-BRK-5 | Citou o bloco WORM final e ignorou os passos 1-4 do **mesmo teste** (`secrets_test.go:110-153`), que fazem a troca e a injecção reais e varrem quatro serializações e todos os eventos |
| H-REG-8 | Atribuiu ao TOFU uma defesa que o desenho atribui à quarentena de taint — provada pelo Vector 4. Confundiu «indetectável» com «inexplorável» |

E sete foram reclassificadas como **deferimentos declarados**: H-MEM-2, H-MEM-5 (residual nomeado em
`EPIC-18:1685-1690`, decisão de arquitectura registada), H-GW-3 (`DEF-280-TOKENS`, texto exacto e
actual), H-GW-4 (banner `posture_banner.go:229`, declaração verificada como verdadeira), H-BRK-6,
H-BRK-8 (`DEF-703`/`DEF-704`, `NUNCA-EM-PRODUCAO`), H-REG-3 (cabeçalho de `modelcatalog.go:13-19`).

**A cultura de declarar lacunas é o que evitou sete falsos positivos.** É uma força real deste
repositório e merece ser dita como tal.

---

## 5. Meta-achados: as declarações que apodreceram

A força do §4 tem um preço: declarações envelhecem, e uma declaração falsa é pior do que nenhuma,
porque compra confiança que já não sustenta. Cinco estão erradas hoje — todas de **classe (a)**.

1. **`posture_banner.go:109` — «o nó não importa `platform/broker`».** Falso desde o AOS-264:
   `bootstrap.go:67` e `broker_vault_env.go:23` importam-no. O banner vizinho da linha 180 («não
   *compõe*») continua verdadeiro. Um envelheceu, o outro não.
2. **«A troca só medeia algo em AOS-265»** (`bootstrap.go:2416` e banner). O AOS-265 **aterrou**
   (`inprocess.go`, `aos265_inprocess_test.go`) e não ligou a troca. A substância continua certa; o
   ticket apontado já fechou. O bloqueador real é o `DEF-218`. O banner do operador aponta para o
   sítio errado.
3. **`mcp/protocol.go:147` — «Digest RESERVADO (AOS-047). Vazio em AOS-046».** O AOS-047 entregou. É
   a peça que fecharia o §3.1.
4. **`pipeline/stages.go:68-71` e `tecnica/06:325`** afirmam a composição `RunPrefix.Turn →
   Guard.Admit`. Nem a composição nem o método existem.
5. **`memory/doc.go:54-55` contra `compression/async_compactor.go:312-314`.** Um diz que
   `record.Persist` grava «SEMPRE a trajectória completa no backend»; o outro diz que sem tracer real
   o registo não sai dali. Uma das duas frases está errada.

E uma sexta, de forma mais subtil: em `credentials/source.go`, o doc-comment da linha 47 promete «um
pedido para um par fora de `Allowed` falha fail-closed atribuível» e o comentário da linha 59 declara
que «se vazio, a origem é permissiva». **Dois comentários no mesmo ficheiro descrevem posturas
opostas**, e nenhum teste fixa o comportamento com a lista vazia.

---

## 6. Classificação e destino

Ordenado por risco fechado sobre custo de correcção, não por severidade nominal.

| # | Achado | Classe | Alcance | Destino sugerido |
|---|---|:--:|---|---|
| 1 | Ligar `CapabilityManifest.Digest` ao `Entry.Digest` do `mcp_server`; acrescentar asserção de digest de `mcp_server` à suite AOS-054 | (a)+(c) | latente | Ticket novo, eixo EPIC-05 |
| 2 | Distinguir *usage ausente* de *custo zero* no GW, no molde de `cache_sli.go:158` | (b) | **nó** | Ticket novo, eixo EPIC-06 |
| 3 | Validar o esquema do endereço do Vault (`https` obrigatório) | (b) | **nó** | Correcção de uma linha; `broker_vault_env.go` |
| 4 | Impor e **testar** o eixo *Provider* na troca de credenciais | (c) | latente | Pré-requisito de `DEF-218`, antes do wiring |
| 5 | Reconciliar as cinco declarações caducadas do §5 + a incoerência de `source.go` | (a) | — | Correcção documental, barata |
| 6 | Registar a não-composição de MEM e REG — ou como `DEF-NNN`, ou como ADR no molde do ADR-023 | (c) | — | **O item que fecha a lacuna de governação do §2** |
| 7 | `/readyz` nunca fica vermelho por *shred* pendente com o vault de referência (§8.2, N-01) | (b) | **nó** | Ticket novo, eixo EPIC-09/AOS-215 |
| 8 | `aos_dsar_vault_shred_unconfirmed` sem regra de alerta (§8.2, N-02) | (c) | **nó** | Regra `absent()`/`for` em `deploy/` |

O item 6 é o mais importante a médio prazo. O plano de controlo tem um ADR ratificado que torna a
sua não-composição doutrina em vez de dívida; os serviços de plataforma não têm. Registá-la — em
qualquer direcção, *compor* ou *declarar que não se compõe* — vale mais do que ligar um subsistema à
sorte.

---

## 7. Limites desta auditoria

### 7.1 Levantados

**O ramo não afecta as conclusões.** Os seis ficheiros-âncora (`modelgatewaywiring.go`,
`mcp/host.go`, `mcp/protocol.go`, `credentials/source.go`, `integration/ingestion.go`,
`broker_vault_env.go`) são **idênticos** entre o HEAD e `origin/feature/AOS-128-ux-dx-tests`. Nenhuma
linha citada em `bootstrap.go` ou `posture_banner.go` foi tocada pelo WIP. A cadeia da EPIC-22
(`claude/lucid-payne-e9fc91`, 6 commits) **não toca um único ficheiro sob `packages/`**.

**A atestação é real, assinada e verificada.** O `release.yml` correu para `v0.1.10` (2026-09-04) e
passou. O job `publish` executou `package.sh`, que orquestra sbom → sign → verify como sub-gates
fail-closed. Do log: «assinatura VÁLIDA: keyid=`cd8112b1…` estado=active», DSSE a cobrir
`ghcr.io/albinojimy/aos-node:v0.1.10`, cinco subjects recomputados contra o artefacto real,
`AOS_SKIPPED_STEPS none`. O workflow recusa arrancar sem `AOS_RELEASE_KEY`, precisamente para evitar
o «verde parcial» (saída 3). Cinco releases anteriores, mesmo resultado. A proveniência regista
honestamente `reproducible=false/host-rebuild-differs-from-image`.

**A memória não participa no gate de replay.** O `replay.sh` corre um único alvo —
`packages/kernel/agent-runtime`, pacote `./harness/...` — com doze testes exigidos e zero menções a
memória. Coerente com H-MEM-1: sem caminho de leitura, não haveria recall para reproduzir.

**A costura da soberania permissiva caiu.** Submetida a refutação: o facto bruto confirma-se
(`source.go:141`), mas a fronteira real é a allowlist assinada default-deny (`policy/allowlist/stage.go:17`)
mais a guarda de failover, ambas a montante — e `credentials.NewSource` tem zero chamadores. Sobrevive
reduzida à incoerência documental do §5.

### 7.2 Por levantar

- Nada foi corrido contra um **fornecedor de modelo real**, um **Vault real** ou um **servidor MCP
  real**. Exigem infra externa. As conclusões sobre esses caminhos são leitura de código e de log de
  CI, não medição.
- `sbom.sh`, `sign.sh` e `verify-attestation.sh` não correm no ambiente local (sem docker, cosign ou
  syft). Correm em CI, que é evidência melhor — mas a reprodução local fica por fazer.
- O `govulncheck` reporta **dez vulnerabilidades** em `packages/platform/attestation` — a dependência
  WebAuthn da excepção escopada ao zero-dep (Carta, emenda 1.3). O gate compara contra
  `baseline/govulncheck.txt` e só falha em vulns **novas**, pelo que o verde é legítimo. **Não foi
  verificado** se a triagem foi revista ou apenas herdada.
- A **cobertura cross-provider** do broker é zero (§3.3). Não se mediu o que aconteceria com dois
  providers configurados, porque não há wiring que o permita construir.
- O comportamento **com N réplicas reais** não foi medido — a topologia entregue é de nó único e o
  `LockWAL` recusa duas sobre o mesmo WAL. O §8 é leitura de código, tickets e testes, não execução.

---

## 8. Passagem 4 — a custódia da KEK do DSAR sob múltiplas réplicas

Levantada depois de fechado o §7, sobre um eixo que o âmbito original não cobria: o que acontece à
custódia de chaves de PII quando o nó deixa de ser um só processo.

### 8.1 A hipótese, e porque caiu

**H-CLUSTER-1** (formulada e submetida a refutação): *o Event Store pode ser replicado
(`AOS_EVENTSTORE_NATS`, AOS-100), mas o WORM não — `bootstrap.go:1134-1142` só sabe abrir
`audit.OpenFileStore`, um ficheiro local. Como a pendência de crypto-shred é reconstruída
exclusivamente da cadeia DSAR nesse WORM (`restoreShredPending`, partição fixa `"governance.dsar"`),
uma réplica que substitua outra a meio de uma destruição falhada não veria a pendência, e o alarme
desenhado para não se saber desligar desligar-se-ia sozinho.*

**Veredicto: DEFERIMENTO-DECLARADO, latente.** O mecanismo está correctamente descrito, mas o
cenário é inalcançável na topologia entregue e está nomeado como v1.1:

- **A custódia está certa.** Em produção a KEK vive em HashiCorp Vault **Transit**
  (`packages/cmd/aos/vaultkeyvault.go`) e **nunca entra no processo do nó**: o embrulho/desembrulho da
  DEK corre no Vault, e o shred é a destruição da chave Transit. Não há KEK em cache local para
  invalidar entre réplicas. `ErrProductionNeedsDurableKEK` recusa `AOS_MODE=production` com substrato
  durável sem `AOS_DSAR_VAULT_ADDR` — «a chave tem de ser tão durável quanto o substrato que cifra».
- **O caso do volume partilhado está fechado fail-closed.** `guardDoWORMAplicavel` +
  `eventstore.LockWAL` fazem com que duas réplicas sobre o mesmo `worm.wal` **não arranquem ambas**
  (`ErrEventStoreJaDetido`). O guard de AOS-285 passou a trancar os **dois** WALs depois de a medição
  expor a configuração assimétrica (mesmo WORM, Event Stores diferentes).
- **O caso dos volumes separados é a v1.1, e está ticketado.** O **AOS-284** (v1.1, P0) foi **medido**
  a 2026-08-31 com a nota «o problema EXISTE, e é pior do que este ticket descrevia»: dois escritores
  não corrompem em silêncio — **o nó não arranca**, e classifica a cadeia como adulteração. Quatro dos
  cinco AC estão fechados (porta `PosseDeParticao` por lease durável, recusas observáveis por partição,
  fail-closed sobre a incerteza). O `aos100_no_sobre_substrato_replicado_test.go:121` di-lo em
  português claro: «O WORM continua LOCAL e por isso cada réplica tem o SEU».
- **O resíduo é o AC a meio:** *«o que não existe é a ATRIBUIÇÃO: nada mapeia deterministicamente
  réplicas→partições … é uma convenção sem guarda»*. É aí que este eixo aterra — item de checklist do
  AOS-101/AOS-107, não defeito da v1.

**Resíduo documental (classe (a)):** `deploy/server/docker-compose.prod.yml:92-94` afirma que o
Event Store replicado torna «N replicas do no a configuracao pretendida». É verdadeiro quanto ao
Event Store e **materialmente incompleto quanto ao WORM** — a ressalva que `bootstrap.go:482`,
`wal_posse.go:97-100`, o teste do AOS-100 e o EPIC-10 todos carregam falta exactamente no ficheiro
que o operador edita.

### 8.2 O que a refutação encontrou, e a hipótese não dizia

Dois achados novos, um deles **alcançável hoje e independente de réplicas**.

**N-01 — ~~o `/readyz` nunca fica vermelho por *shred* pendente com o vault de referência~~
FALSIFICADO a 2026-09-04, na discovery da EPIC-23.**

Os factos que sustentavam este achado são todos verdadeiros: o `audit.InMemoryKeyVault` tem
exactamente três métodos (`keyvault.go:54,70,81`) e não implementa `readinessProber`, e o `/readyz`
só sonda a custódia por asserção de tipo (`api.go:1109,1138`). **A conclusão que se tirou deles não
é.**

O que faltou seguir foi como *nasce* uma pendência. `dsar/flow.go:275-284` só sela
`EventShredUnconfirmed` se `f.confirmer != nil`, e `confirmadorDeShredDe`
(`cmd/aos/shred_confirmador.go:23-29`) devolve `nil` para uma custódia que não implemente a porta.
Com o vault de referência não há confirmador, logo não há evento, logo **não há pendência a
reportar** — e isso está certo, porque `InMemoryKeyVault.Delete` (`keyvault.go:80-86`) é um
`delete()` num mapa sob mutex e não tem como falhar. A porta `dsar.ShredConfirmer`
(`confirmador.go:21-34`) declara-o: «um vault em memória destrói e sabe-o».

As três ausências — confirmador, `readinessProber`, métrica — são **coerentes entre si e
correctas**. O `/readyz` não fica vermelho porque não há nada pendente.

**O que resta**, e é de outra natureza: a opcionalidade da `ShredConfirmer` é fail-open para
qualquer custódia que não a implemente, e nada torna essa escolha obrigatória nem visível no
arranque. Inofensivo hoje (só existem duas custódias, e a distinção é consciente); é uma via de
omissão aberta para a terceira. **Classe (c), endurecimento.** Reformulado como AOS-322 na EPIC-23,
reclassificado de P1 para P2.

**N-02 — a métrica da pendência não tem regra de alerta.**
`aos_dsar_vault_shred_unconfirmed` é um gauge **por processo**, e nenhuma regra em `deploy/` ou
`docs/` o referencia. Quando o processo morre, a série desaparece; sem `absent()` ou `for`, a via
métrica também não apanha o desaparecimento. O buraco é maior do que o WORM. **Classe (c).**

**N-03 — o gatilho é o que falta, não a operação.** A destruição Transit já é re-verificável: o
`Delete` relê a chave e exige 404 (`vaultkeyvault.go:600-608`). Uma re-tentativa idempotente de uma
destruição por confirmar seria trivial de armar. O que não existe é quem a dispare — nenhum job
re-tenta a partir de sinal partilhado (`retention_sweeper.go:315` só reporta a contagem).

### 8.3 O que isto faz a esta auditoria

Neste eixo o repositório está **à frente do relatório**: o problema foi medido, ticketado, e teve um
guard de v1 entregue por consequência. Registar isto importa tanto como registar os defeitos — uma
auditoria que só nomeia o que falha mede a sua própria atenção, não o estado do sistema.

O que resta como trabalho real são o N-01 e o N-02, que não dependem do distribuído e não estavam
nomeados em lado nenhum.

---

*Auditoria conduzida por painel multi-agente: quatro lentes independentes, seis refutadores com o
ónus da prova invertido, e medição executada no nó real e no pipeline de release. Ver
[09](09_Auditoria_RT_RM_Adversarial.md), [10](10_Auditoria_ORQ_SCH_PDP_Adversarial.md) e o
[Índice](INDICE.md).*
