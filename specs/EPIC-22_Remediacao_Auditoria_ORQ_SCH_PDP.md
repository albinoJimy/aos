# EPIC-22 — Remediação dos defeitos activos da auditoria adversarial ORQ/SCH/PDP

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — Remediação dos **defeitos activos** (reachable no binário entregue) apurados na auditoria adversarial do plano de controlo |
| Versão | 1.0 |
| Data | 2026-09-03 |
| Classificação | Documento de Referência — **Proposta** |
| Documento-fonte | `analises/10_Auditoria_ORQ_SCH_PDP_Adversarial.md` (§3.1, §3.3, §8) |
| Documentos relacionados | `docs/governance/REGISTO-Deferimentos.md`, ADR-011, `tecnica/01`, `tecnica/12`, `tecnica/17`, `specs/EPIC-09`, `EPIC-20` (AOS-263) |
| Âmbito | `packages/cmd/aos` (rotas `/autonomy`, `/challenge`, `/approve`), `packages/control-plane/governance/autonomy`, `packages/integration/foureyes.go`, `packages/control-plane/pdp`, `packages/platform/audit` |

---

## 0. Porque este epic existe, e o que ele contém

`analises/10` produziu 63 achados, atacou-os com o ónus da prova invertido, mediu-os no nó real
numa terceira passagem e verificou uma síntese externa numa quarta. Sobreviveram 21. A maioria é
**latente** — vive em `control-plane/orchestrator`/`scheduler`, que o ADR-018 e o ADR-023
mantêm deliberadamente fora do grafo de build do nó; corrigi-los não muda o binário que se
instala hoje.

Este epic cobre só os que são **activos**: alcançáveis pela superfície HTTP do nó `aos` tal como
é entregue, com o mecanismo de autorização, selagem ou observabilidade que devia cobri-los
ausente ou a mentir sobre o que faz. Onze tickets, quatro eixos:

| Eixo | Tickets |
|---|---|
| Governação da autonomia (`/autonomy`) | AOS-305, AOS-306, AOS-307 |
| Cerimónia de quatro-olhos (`/challenge`, `/approve`) | AOS-308, AOS-309 |
| Rastreabilidade da política (PDP) | AOS-310, AOS-311 |
| Rastreabilidade do corpus (RTM) | AOS-312, AOS-313, AOS-314, AOS-315 |

> **AOS-312 não vem da §3.** Os sete primeiros são achados activos do documento-fonte; o oitavo
> vem da **§5** (o meta-achado sobre asserções que nenhum gate lê) e nasceu do acto de remediar
> este epic: foi ao regenerar a RTM depois de abrir AOS-305..311 que a §6 mudou de «EPIC-21» para
> «EPIC-22» na linha de AOS-194 — que vive na EPIC-18 — e o defeito do gerador ficou visível. O
> precedente é AOS-279, criado pela remediação da EPIC-20. **AOS-313** tem a mesma
> origem: nasceu da ressalva que AOS-312 deixou por endereçar — a §7 da RTM, que a §5
> nomeia como «o exemplar mais limpo» do meta-achado. **AOS-314** fecha a decisão que
> AOS-313 registou como GAP-07 em vez de tomar, e **AOS-315** corrige o defeito que essa
> decisão descobriu ao acrescentar quatro linhas à §4.

### 0.1 Ordem sugerida

| Prioridade | Tickets | Racional |
|---|---|---|
| **P0** | AOS-305, AOS-306 | Removem ou mentem sobre o gate humano de acções `danger` |
| **P1** | AOS-307, AOS-309, AOS-311 | Corrompem a durabilidade da decisão ou a atribuição de uma negação |
| **P2** | AOS-308, AOS-310 | Superfície mal classificada; lacuna de rastreabilidade de política |
| **P2** | AOS-312 | Atribuição ticket↔epic falsa e auto-renovável na RTM; sem efeito no binário |
| **P2** | AOS-313 | Cobertura afirmada contra as matrizes geradas do próprio ficheiro; sem efeito no binário |
| **P2** | AOS-314, AOS-315 | Âmbito do canon de ADRs e coluna de documentos da §4; sem efeito no binário |

### 0.2 Tabela-resumo

| Ticket | Defeito | P | Estado |
|---|---|---|---|
| AOS-305 | `/autonomy` autoriza-se com a assinatura de um só operador, sem papel, tecto nem four-eyes | P0 | ABERTO |
| AOS-306 | Uma selagem falhada aplica o nível de autonomia e a API responde que o recusou | P0 | ABERTO |
| AOS-307 | O nível aplicado por `/autonomy` não sobrevive a um reinício do nó | P1 | ABERTO |
| AOS-308 | `POST /runs/{id}/challenge` não autentica nada, e o comentário do handler diz que autentica | P2 | ABERTO |
| AOS-309 | `FourEyesGate.Authorize` não sela nem regista nenhuma negação | P1 | ABERTO |
| AOS-310 | `PDP.Reload` nunca corre em produção; o nó não emite `policy.changed` | P2 | ABERTO |
| AOS-311 | `audit.FileStore.Append` não consulta `ctx`; o fail-closed por timeout é condicional ao sink | P1 | ABERTO |
| AOS-312 | A §6 da RTM afirmava o epic de um ticket sem o derivar, e nada confrontava o gerador com a fonte | P2 | **ENTREGUE** |
| AOS-313 | A §7 da RTM afirmava cobertura que as suas próprias secções geradas contradiziam | P2 | **ENTREGUE** |
| AOS-314 | O canon de ADRs que os gates lêem parava em ADR-019, quatro aquém do catálogo | P2 | **ENTREGUE** |
| AOS-315 | A coluna de documentos técnicos da §4 resolvia-se pela amplitude do conjunto, não por ticket | P2 | **ENTREGUE** |

---

## AOS-305 — `/autonomy` autoriza-se com a assinatura de um só operador, sem papel, tecto nem four-eyes

### Contexto

`POST /autonomy` (`packages/cmd/aos/autonomy_route.go:60-127`, `handleAutonomySet`) autoriza-se
inteiramente com `h.node.SteerAuth.Authenticate(...)` (`:112-118`), sobre o `AutonomyScope` —
uma assinatura ed25519 de **um** emissor registado em `AOS_OPERATORS`
(`packages/integration/steer_authenticator.go:187-228`: pubkey registada, assinatura, nonce de
uso único). Não há verificação de papel, de escopo por-operador, de tecto de nível nem de
segunda assinatura.

O contraste está no mesmo binário: `/approve` exige a capability `approve:<classe>` de
vocabulário fechado e o roster **aborta o arranque** se duas entradas partilharem pubkey
(`packages/cmd/aos/main.go:2097-2113`), precisamente para que «duas pessoas» esteja ancorado em
criptografia. `/promote` exige um ratificador pinado.

**Medido** (auditoria §3.1, com o nó a correr): uma assinatura de um único operador levou o par
`agt-1:fs` de `L0` a `L5`; o efeito verificou-se onde importa — a mesma tool call, antes
escalada com `denied_by=policy code=E_ESCALATED reason="autonomia L0 x gray -> suggest (gate
humano)"`, deixou de escalar depois da promoção. Não há degraus: `L0 → L5` num salto, e um par
nunca antes registado nasce directamente em L5. Não há controlo de papel: um operador promoveu
`class:agent-break-glass`, com que não tinha relação nenhuma, e o nó devolveu 200.

O corpus condena esta classe de regressão por princípio: `specs/EPIC-20_Prontidao_Agentica_Remediacao.md:983`
regista a decisão do dono (2026-08-12) de recusar qualquer «caminho de decisão humana mais fraco
que o four-eyes já entregue — regressão de postura». O PR que criou a rota
(`7e073de`, #71) reutilizou deliberadamente as chaves de `AOS_OPERATORS` via `AutonomyScope` e
deixou «chave por CLASSE — muda a semântica de resolução; merece controlos próprios» como fase 3
por escrito, nunca implementada. O que existe hoje é a fase 1 (a rota) com a autoridade da
fase 3 (mudar quem decide quanta supervisão humana se aplica).

*Atenuante que fica escrito:* o acto é totalmente atribuível — sela-se duas vezes no WORM, na
partição `autonomy` (obrigação `autonomy.level_changed` com `old_level`/`new_level`/`actor`) e
em `governance.control`. O defeito é de controlo preventivo, não de rasto.

### Critérios de Aceitação

- [ ] Uma mudança de nível que atravesse o limiar do gate humano (qualquer transição para `L4`
      ou `L5`, ou qualquer transição a partir de um par não-registado directamente para `L4`/`L5`)
      exige **duas** assinaturas de operadores distintos, no molde da cerimónia de `/approve`
      (`fourEyesMessage`, pubkeys distintas, nenhuma reutilização entre pernas)
- [ ] O emissor de `/autonomy` carrega uma capability própria (`autonomy:set`, no vocabulário
      fechado de `main.go:2097-2113`) distinta de `steer`/`pause`; um operador sem essa capability
      é recusado com `403`
- [ ] Um teste que reproduza a medição da auditoria — uma única assinatura a promover `L0→L5` —
      e prove que passa a ser recusada (ou a exigir a segunda perna) na configuração composta
- [ ] O banner de arranque declara a exigência (ou a ausência dela) tal como declara as outras
      posturas do canal de controlo

### Estado

**ABERTO.** P0.

---

## AOS-306 — Uma selagem falhada aplica o nível de autonomia e a API responde que o recusou

### Contexto

`LevelRegistry.SetLevel` (`packages/control-plane/governance/autonomy/registry.go:147-188`) muta
o mapa em memória sob lock (`:164-179`, `r.levels[k] = level`) e liberta o lock **antes** de
selar (`:181-186`, comentário: «Selagem fora do lock — I/O do audit não bloqueia consultas O(1)
concorrentes»). Se `sink.SealLevelChange` falhar, o erro é devolvido **sem reverter a mutação**.
`registry.go:142-146` declara esta semântica de propósito: «devolve o erro (NÃO o engole), para
que uma alteração de nível sem changelog selado seja detectável».

O único consumidor de produção não cumpre o contrato que essa linha nomeia:
`handleAutonomySet` (`packages/cmd/aos/autonomy_route.go:120-123`) traduz **qualquer** erro de
`SetLevel` em `writeError(w, http.StatusBadRequest, "nivel recusado")` — sem distinguir
`ErrInvalidLevel`/`ErrEmptyPair`/`ErrMissingReason`/`ErrMissingActor` (inalcançáveis: o handler
já pré-valida `agent`/`domain`/`reason` não-vazios e o nível antes de chamar `SetLevel`,
`:78-104`) do erro de selagem, que é o único que pode mesmo chegar aqui.

**Reproduzido** (auditoria §3.1, com sink de selagem a falhar): `SetLevel` devolveu erro
(«WORM em baixo»), a API respondeu `400 "nivel recusado"`, e o nível em vigor **depois** do erro
era `L5`. O contraste no mesmo binário: o Reference Monitor trata a condição equivalente —
efeito sem audit disponível — como `CodeAuditUnavailable` e **nega**
(`packages/kernel/reference-monitor/monitor.go:349-353`, «uma acção não-auditável não é
permitida»).

### Critérios de Aceitação

- [ ] `handleAutonomySet` distingue o erro de selagem dos erros de validação: numa falha de
      selagem, responde `5xx` (não `400`) e o corpo não afirma que o nível foi recusado
- [ ] Decidido o desenho: (a) `SetLevel` reverte a mutação em memória quando a selagem falha —
      preservando a leitura O(1) sem lock durante o `Append` por outro mecanismo (ex.: CAS
      optimista sobre o valor antigo), **ou** (b) o handler, ao ver erro de selagem, força o
      registo de volta ao nível anterior antes de responder
- [ ] Um teste que reproduza a medição da auditoria — sink a falhar, nível a aplicar-se — e prove
      que a resposta HTTP e o estado do registo deixam de divergir
- [ ] O `reason`/código de erro devolvido nomeia a indisponibilidade real (não «nivel recusado»)

### Estado

**ABERTO.** P0.

---

## AOS-307 — O nível aplicado por `/autonomy` não sobrevive a um reinício do nó

### Contexto

`autonomyWiring.provision` (`packages/cmd/aos/autonomy_levels.go:246-280`) constrói, a cada
arranque, um `LevelRegistry` **novo** (`buildAutonomyOracle`, chamado na fronteira de config) e
reaplica `AOS_AUTONOMY_LEVELS` sobre ele (`provision`, `:270-279`, um `SetLevel` por entrada
declarada). `LevelRegistry` não tem `Rebuild` nem qualquer via de rehidratação a partir do WORM
— os seus métodos são só `LevelFor`, `Get`, `SetLevel`, `History`, `HistoryFor`,
`LevelForAgentOrClass`.

Consequência: um nível posto em vigor por `POST /autonomy` — selado no WORM, com `actor` e
`reason` — **não sobrevive a um reinício**. O nó volta a servir o nível que
`AOS_AUTONOMY_LEVELS` declara no ambiente, silenciosamente. O WORM continua a dizer `L5`; o nó a
correr serve o que estiver no ficheiro de configuração. Não há evento nenhum a assinalar a
divergência entre o trilho e o nível efectivo.

Isto agrava directamente AOS-305: precisamente porque `/autonomy` permite uma promoção a `L5`
sem tecto, a garantia de que essa promoção **persiste** (ou que a sua perda é visível) é a
diferença entre «decisão revertida por reinício, sem ninguém a saber» e «decisão que continua em
vigor até ser revertida deliberadamente».

### Critérios de Aceitação

- [ ] `LevelRegistry` ganha uma via de rehidratação a partir do stream `autonomy.level_changed`
      do WORM (no molde de `Revocations.Rebuild`, AOS-288/300), chamada por `provision` **antes**
      de aplicar `AOS_AUTONOMY_LEVELS`
- [ ] Decidido o desenho de precedência: o nível reidratado do WORM prevalece sobre o do ambiente
      para o mesmo par, **ou** o ambiente prevalece e o nó emite um evento explícito
      (`autonomy.level_reset_by_env`) sempre que a rehidratação e o ambiente divergem — de forma
      a que a divergência nunca seja silenciosa
- [ ] Um teste que promova um par por `/autonomy`, reinicie o registo (não o processo — o
      construtor), e prove que o nível pós-reinício é o esperado pela decisão acima, não uma
      surpresa
- [ ] Falha na rehidratação (WORM ilegível, stream corrompido) é fail-closed: o nó não arranca a
      servir um nível que não conseguiu confirmar

### Estado

**ABERTO.** P1.

---

## AOS-308 — `POST /runs/{id}/challenge` não autentica nada, e o comentário do handler diz que autentica

### Contexto

A rota está classificada `planoControlo` (`packages/cmd/aos/planos.go:212`), e a definição da
classe (`:74-77`) declara: «Admission + mTLS do plano de controlo, ambos ANTES do handler, e a
**assinatura ed25519 do corpo (AOS-160) a decidir depois, dentro dele**». O handler
(`packages/cmd/aos/api.go:1668-1696`, `handleChallenge`) não chama `SteerAuth.Authenticate` nem
verifica credencial nenhuma: valida só que `request_id` e `approver` não estão vazios e emite.
`admitControl` (`:1889-1895`) é um token-bucket; `admitControlMTLS` (`:1897-1919`) devolve `true`
sem CA montada — o caso por omissão.

**Medido** (auditoria §3.1): cinco vectores — sem assinatura, sem headers, contra um run
inexistente, com um aprovador arbitrário não pinado, em rajada de 20 — devolveram `HTTP 200`. A
escrita fica durável no Event Store (`foureyes.challenge.issued`), com `producer` uma constante
do próprio nó (`nhi:foureyes-challenge-issuer`) e `run_id` vazio: nada no registo diz quem pediu.

A consequência para autoridade é limitada — ver AOS-309: o challenge emitido anonimamente não é
por si só consumível numa cerimónia de aprovação real, porque essa exige a assinatura da chave
privada do aprovador nomeado. O que sobra é (a) uma escrita durável não atribuída, sob um
bucket partilhado com `/steer`/`/pause`/`/approve`, e (b) um comentário no código que descreve
uma barreira que não existe.

### Critérios de Aceitação

- [ ] `handleChallenge` exige a assinatura do aprovador nomeado (`req.Approver`) sobre o pedido,
      **ou** a classe/comentário de `planos.go:74-77` e `api.go:1667` deixam de afirmar
      autenticação que a rota não impõe — decisão do dono entre as duas
- [ ] Se a decisão for exigir assinatura: um teste que reproduza os cinco vectores da auditoria e
      prove que passam a ser recusados
- [ ] Se a decisão for manter a emissão aberta: o `producer` do evento deixa de ser uma constante
      do nó — carrega alguma atribuição do chamador (mesmo que fraca), e a rota é reclassificada
      para fora de `planoControlo` com a razão escrita

### Estado

**ABERTO.** P2.

---

## AOS-309 — `FourEyesGate.Authorize` não sela nem regista nenhuma negação

### Contexto

`FourEyesGate.Authorize` (`packages/integration/foureyes.go:338-350`) delega em
`authorizeSingle`/`authorizeDual` (`:353-450` aprox.), cujos catorze ramos de recusa devolvem
todos `denied(razão)` — um construtor puro (`:449-451`) que não toca em `Append`, `Seal` nem
`audit`. `handleApprove` (`packages/cmd/aos/api.go:1868-1874`) só chama `h.sealControlAction`
no **sucesso**; a negação vira `403 "aprovacao recusada"` uniforme, com o comentário a afirmar
«Fail-closed: qualquer negação ⇒ 403, sem revelar QUAL invariante falhou (o audit **tem** o erro
dedicado; a resposta HTTP é uniforme)» — mas nenhuma via de audit tem o erro: nem selo, nem
evento, nem log estruturado.

Consequência medida: a própria auditoria não conseguiu, com seis tentativas de cerimónia sobre
um challenge emitido anonimamente (AOS-308), distinguir «o challenge anónimo foi rejeitado» de
«a cerimónia estava malformada» — todas devolveram `403` idêntico. Um operador com uma cerimónia
legítima a falhar por engano de terceira perna, sessão repetida, ou challenge expirado tem
exactamente a mesma ausência de diagnóstico.

### Critérios de Aceitação

- [ ] Toda a chamada a `denied(...)` dentro de `authorizeSingle`/`authorizeDual`/`verifyLeg`
      produz, no chamador (`handleApprove`), um registo estruturado da razão de recusa — selado
      no WORM ou, no mínimo, num log correlável ao `request_id`, sem alargar a resposta HTTP
      (que continua uniforme por desenho)
- [ ] Um teste que force cada classe de recusa (contagem de pernas errada, auto-aprovação, mesma
      sessão, mesma credencial, challenge não-consumível) e prove que o registo distingue-as, ainda
      que a resposta HTTP não distinga
- [ ] O operador consegue, offline, correlacionar uma sequência de `403` do `/approve` com a razão
      real de cada um — fechando a lacuna que impediu a medição de AOS-308

### Estado

**ABERTO.** P1.

---

## AOS-310 — `PDP.Reload` nunca corre em produção; o nó não emite `policy.changed`

### Contexto

`PDP.Reload` (`packages/control-plane/pdp/pdp.go`) e as portas que o rodeiam (`WithReloadAudit`,
`WithReloadAuditSink`, `AuditReloadSink`) não têm chamador de produção em lado nenhum do
repositório: `grep -rn "\.Reload(" --include=*.go packages/ | grep -v _test` devolve vazio. Não
há rota HTTP que o exponha — as 22 rotas de `planos.go` não incluem nenhuma de política. A única
via real de trocar de política é substituir o directório do bundle e **reiniciar o processo**,
que passa por `pdp.Open` e não por `Reload` — pelo que o changelog `policy.changed`, que o CA de
AOS-088 (`policy_version` selado com autor/motivo/hash de conteúdo) promete, nunca é escrito.

*Atenuante:* a mudança não fica sem rasto nenhum. `policy_version` viaja em cada
`MediationRecord` (`packages/kernel/reference-monitor/eventsink.go:39-41,74,145`,
`monitor.go:298-299,346`) e é selado no WORM por decisão, pelo que um auditor consegue ver a
versão mudar no trilho de mediação — só não vê **quando** mudou, **quem** a trocou, nem o
`ContentHash` antigo/novo, porque isso só o changelog dedicado transportaria. O precedente do
mesmo composition-root existe e funciona: AOS-248 selou os níveis de autonomia no arranque
(`autonomyWiring.provision`) e recusa arrancar se a selagem falhar
(`ErrAutonomyProvisioning`); o bundle de política não tem o equivalente.

### Critérios de Aceitação

- [ ] O arranque do nó, ao carregar um bundle de política (`AOS_POLICY_BUNDLE_DIR`), sela um
      evento `policy.changed` na hash-chain WORM com `PolicyVersion` (nova), `ContentHash`,
      `At` — no molde do que `autonomyWiring.provision` já faz para os níveis de autonomia
      (`autonomy_levels.go:270-279`)
- [ ] Se a versão carregada for igual à anterior (mesmo `ContentHash`), nenhum evento novo é
      emitido — só transições reais de política produzem `policy.changed`
- [ ] Falha em selar o evento é fail-closed: o nó não arranca a servir uma política cuja troca
      não conseguiu registar (mesmo padrão de `ErrAutonomyProvisioning`)
- [ ] `aos audit-trail` passa a poder correlacionar, sem leitura crua do WORM, quando uma
      sequência de denies começou face à última troca de política

### Estado

**ABERTO.** P2.

---

## AOS-311 — `audit.FileStore.Append` não consulta `ctx`; o fail-closed por timeout é condicional ao sink

### Contexto

`audit.FileStore.Append` (`packages/platform/audit/filestore.go:151-183`) recebe `ctx` e
passa-o só a `autorizadoAEscrever(ctx, rec.Partition)` (`packages/platform/audit/posse.go:116-130`),
que devolve `nil` imediatamente quando `s.posse == nil` (`:117-119`) — o caso do nó: nenhum dos
quatro sítios de produção que chamam `audit.OpenFileStore` (`bootstrap.go:1096`,
`audit_trail.go:57`, `model_audit_env.go:62`, `cmd/aos-issuer/wormseal.go:104`) passa uma opção
de posse. Nem `Append` nem `persist` (`:189-...`) fazem `ctx.Err()` em nenhum ponto: uma
selagem no WORM de produção **nunca é interrompida por um contexto morto**.

Isto é relevante porque `tecnica/17_Analise_STRIDE.md` §4.3-D declara «timeout fail-closed» como
mitigação entregue para o PDP, sustentada em `Monitor.evaluate` verificar `ctx.Err()` uma vez à
entrada e no `Append` do **eventstore** (`substrate/eventstore/store.go:311-313`) verificar de
novo antes de escrever — mas o sink de mediação de produção é
`audit.NewMediationSink(cfg.WORM)` sobre o `FileStore` acima, não sobre o `eventstore.Store`. Um
prazo que expire depois do check de entrada e antes da selagem produz **`permit`**, e a tentativa
de efeito corre sob um contexto já morto — o oposto do fail-closed que o documento declara para
a classe.

O mesmo `FileStore` é o sink de selagem de `/autonomy` (AOS-305/306) e o `AuditSink` da
autonomia (`packages/control-plane/governance/autonomy/events.go:83-88`) — a ausência de
verificação de `ctx` é transversal a toda a governação selada no WORM, não só ao PDP.

### Critérios de Aceitação

- [ ] `FileStore.Append` verifica `ctx.Err()` antes de tomar `s.mu` e antes de `persist`, no
      molde de `eventstore.Store.Append`
- [ ] Um teste que force um `ctx` a expirar entre a entrada de `Append` e a selagem, e prove que
      o registo **não** é escrito e o erro devolvido é distinguível de `ErrParticaoAlheia`
- [ ] `tecnica/17_Analise_STRIDE.md` §4.3-D deixa de declarar «timeout fail-closed» sem
      qualificação — ou a emenda acima torna a declaração verdadeira sem qualificação
- [ ] A verificação de `ctx` não introduz uma corrida com `autorizadoAEscrever` quando uma posse
      real vier a ser composta (AC1 não regride se `s.posse != nil`)

### Estado

**ABERTO.** P1.

---

## AOS-312 — A §6 da RTM afirmava o epic de um ticket sem o derivar, e nada confrontava o gerador com a fonte

### Contexto

`scripts/ci/rtm-regenerate.py:342` calculava `last_epic = f"EPIC-{stats['n_epics']:02d}"` — o
**total** de epics do corpus — e usava-o em quatro linhas geradas da §6 como se fosse o epic onde
vivem tickets concretos. A linha do STRIDE afirmava «análise em EPIC-21/AOS-194»; ao abrir-se este
epic e regenerar-se a RTM passou a afirmar «EPIC-22/AOS-194». As duas erradas, e erradas de forma
**nova a cada epic acrescentado**: AOS-194 vive na EPIC-18
(`specs/EPIC-18_Remediacao_Auditoria_Multiagente_v4.md:343`). O mesmo valia para as linhas de
`tecnica/09` e `tecnica/11`, cuja gama aberta `AOS-190→` atravessa **nove** epics — os de
remediação e ainda tickets acrescentados a epics antigos, como AOS-287 na EPIC-01 — e era atribuída
a um só. A quarta ocorrência, a gama `EPIC-01..N` do diagrama, era a única legítima, e mesmo essa
assentava na contagem de ficheiros em vez do maior número presente.

Este ticket não vem da §3 de `analises/10` — não é um dos vinte e um achados sobreviventes. Vem da
**§5**, e nasceu do próprio acto de remediar este epic: foi ao regenerar a RTM depois de abrir
AOS-305..311 que a linha mudou de EPIC-21 para EPIC-22 e o defeito ficou visível. O precedente é
AOS-279 na EPIC-20, criado pela remediação do epic que o contém.

O que o torna próprio de registo é ser um **contra-exemplo à conclusão da §5**. Essa secção mede 61
asserções numéricas em prosa (27 batem, 34 não) e separa-as assim: «onde uma máquina escreve (§§1,4,5,6
da RTM) bate; onde ninguém lê, deriva». As §§1,4,5 batem porque derivam do corpus. A §6 não: escrevia
uma atribuição **assumida** com a autoridade de ter sido gerada. Um número escrito à mão que ninguém
lê deriva devagar e vê-se; uma máquina que assume reescreve a afirmação falsa a cada regeneração, e o
gate `scripts/ci/rtm.sh` — que compara o ficheiro com a saída do gerador — dava-a por **verde**,
porque comparava o gerador consigo próprio e nunca com a fonte. A remediação que a §5 pede («um gate
que extraia asserções e as confronte com a fonte») é aqui aplicada à classe ticket↔epic.

### Critérios de Aceitação

- [x] Nenhuma linha da §6 nomeia um epic por assunção: `epic_of()` lê do corpus o epic que **contém**
      um ticket e `epics_covering()` faz o mesmo para uma gama, ambos sobre o `tickets` que o parser
      de `specs/EPIC-*.md` já constrói
- [x] `last_epic` sobrevive apenas para a gama `EPIC-01..N` do diagrama e passa a ser o **maior
      número de epic presente** em `specs/`, não a contagem de ficheiros — que mente se faltar um
      número no meio
- [x] A §6 nomeia **EPIC-18** para AOS-194
- [x] Uma asserção no próprio gerador (`validate_section6`) recusa **qualquer** linha gerada que
      nomeie um epic sem os tickets que a própria linha cita, e falha fechado (exit != 0), pelo que
      `scripts/ci/rtm.sh` fica vermelho antes de a afirmação falsa chegar ao ficheiro
- [x] A asserção corre sobre a tabela inteira, incluindo as linhas escritas à mão — e apanhou uma:
      `tecnica/14_Matriz_Conformidade.md` citava AOS-072 nomeando só EPIC-08 e EPIC-09, e AOS-072
      vive na EPIC-07 (`specs/EPIC-07_Seguranca_Isolamento.md:513`)
- [x] Um self-test injecta as duas atribuições falsas — incluindo o **regresso literal** de
      `last_epic` como epic de tickets concretos — e exige vermelho *pela mensagem da asserção*, não
      por mera divergência de texto, com controlo positivo contra a árvore real
      (`scripts/ci/selftest.sh` §R1–R3, no molde de §P3/§Q4)

### Estado

**ENTREGUE** (2026-09-03). P2.

`scripts/ci/rtm-regenerate.py` (derivações + `validate_section6`), `scripts/ci/selftest.sh` (§R),
`tecnica/16_Rastreabilidade_RTM.md` (§6 regenerada). Gates: `rtm.sh`, `ref-lint.sh` e `selftest.sh`
verdes.

**Uma ressalva por endereçar.** A asserção cobre a classe **ticket↔epic** na §6. Não cobre a §7 da
RTM, que a §5 de `analises/10` aponta como «o exemplar mais limpo»: afirma «20/20 ADRs e 12/12 NFRs»
a setenta linhas de secções geradas no mesmo ficheiro que dizem 19/19 e 10/10, e é a única secção
excluída *tanto* da regeneração *quanto* do `ref-lint` (`scripts/ci/ref-lint.py:323`). Fechar essa
exige decisão própria — regenerar a §7 ou tirá-la da lista de `skip` — e não cabe neste
ticket. **Fechada por AOS-313**, que faz as duas coisas.

---

---

## AOS-313 — A §7 da RTM afirmava cobertura que as suas próprias secções geradas contradiziam

<!-- rtm: adrs-mencionados -->

### Contexto

`tecnica/16_Rastreabilidade_RTM.md` §7 fechava com «20/20 ADRs e 12/12 NFRs têm pelo menos um
ticket associado». A §4, gerada, tem **19** linhas e declara 19/19; a §5, gerada, tem **10** e
declara 10/10 — a setenta linhas de distância, no mesmo ficheiro. `analises/10` §5 nomeia-o «o
exemplar mais limpo» do meta-achado e explica porquê: a §7 é a única secção da RTM excluída
*tanto* da regeneração (`rtm-regenerate.py` fazia §§1,4,5,6 e parava) *quanto* do `ref-lint`
(`scripts/ci/ref-lint.py:323` tinha a RTM inteira em `skip`). Nada a lia.

A história mostra o mecanismo, e é o de AOS-312 ao contrário. `ea0c3c8` («docs(AOS): ADR-020
planeador como agente governado») acrescentou **à mão** uma linha ADR-020 à §4, duas linhas
NFR-11/NFR-12 à §5, e subiu a frase da §7 para 20/20 e 12/12 — mas não tocou em `ADR_RANGE`
nem em `NFR_SPECS`, as listas de onde o gerador tira essas tabelas. A regeneração seguinte,
`60ec30c`, apagou as três linhas. Ficaram órfãs a frase da §7 e a linha «1.2» do controlo de
versões, que ainda declara «cobertura 20/20 ADRs, 12/12 NFRs». Em AOS-312 uma máquina escrevia
uma assunção; aqui uma afirmação escrita à mão **sobreviveu à reescrita automática que a
contradizia**, porque a reescrita não passava por ela.

O dano não é uma linha. Medido contra as matrizes actuais, **as seis lacunas registadas estavam
estales, e duas eram falsas**:

- **GAP-01** afirmava «ADR-014 sub-coberto — 3 tickets» e recomendava criar ticket para métrica
  de fiabilidade e demoção automática. A §4 conta **4** (AOS-022, AOS-089, AOS-090, AOS-125),
  acima do limiar de sub-cobertura, e a acção recomendada **já existe**: AOS-090, em EPIC-09.
- **GAP-03** afirmava «ADR-003 concentrado — dependem de AOS-005/006». A §4 conta **12** tickets,
  e a rotação/revogação tem eixo próprio em AOS-288 e AOS-300.
- GAP-02, GAP-04, GAP-05 e GAP-06 mantêm-se, mas nenhuma citava o corpus com números vivos.

### Critérios de Aceitação

- [x] A §7 passa a ser **gerada** a partir das mesmas matrizes que produzem §§4–5, pelo que a
      frase de cobertura não pode voltar a discordar delas sem que §4 ou §5 mudem primeiro
- [x] A prosa editorial de cada lacuna sobrevive — qual é a lacuna e o que fazer com ela é juízo
      humano — mas todos os **números** e listas de tickets que ela cita são interpolados do
      corpus, não reafirmados à mão
- [x] `validate_section7` recusa qualquer `AOS-NNN` inexistente no backlog, `ADR-NNN` fora do
      catálogo ou `NFR-NN` fora de `NFR_SPECS` citado na §7, e falha fechado
- [x] GAP-01 e GAP-03 são **retiradas com a evidência que as fechou**, registada na própria §7,
      em vez de desaparecerem: uma lacuna que some sem explicação é indistinguível de uma lacuna
      varrida para debaixo do tapete
- [x] A RTM sai da lista de `skip` do `ref-lint`, pelo que uma referência partida na RTM — em
      qualquer secção, gerada ou não — passa a avermelhar o gate
- [x] A linha «1.2» do controlo de versões deixa de declarar 20/20 e 12/12 sem qualificação: o
      registo histórico mantém-se, anotado com a regeneração que o desfez
- [x] Um self-test injecta uma citação falsa na §7 e exige vermelho *pela mensagem da asserção*,
      com controlo positivo contra a árvore real (`scripts/ci/selftest.sh` §S)
- [x] Um ticket que **fala** de ADRs sem os implementar não entra na §4 como implementador
      deles. Escrever este ticket revelou-o: o parser conta qualquer `ADR-NNN` no bloco como
      implementação, pelo que AOS-313 passou a «implementar» ADR-003, ADR-014 e ADR-020…023,
      inflacionando as contagens que a própria §7 cita. Fechado com o marcador
      `<!-- rtm: adrs-mencionados -->`, honrado por `rtm-regenerate.py` **e** por `ref-lint.py`
      para que os dois leitores do corpus nunca discordem sobre o que um ticket implementa

### Estado

**ENTREGUE** (2026-09-03). P2.

`scripts/ci/rtm-regenerate.py` (`generate_section7` + `validate_section7`), `scripts/ci/ref-lint.py`
(fim do `skip` da RTM), `scripts/ci/selftest.sh` (§S), `tecnica/16_Rastreabilidade_RTM.md`.
Gates: `rtm.sh`, `ref-lint.sh` e `selftest.sh` verdes.

**Uma decisão deliberadamente NÃO tomada, e registada como GAP-07.** `ADR_RANGE` cobre
ADR-001…019 nos dois gates, mas o catálogo (`docs/adr/README.md`) tem ADR-020, ADR-021, ADR-022 e
ADR-023. **ADR-020 está *Aceite*, materializado como documento, e não tem um único ticket a
citá-lo**; os outros três têm cobertura por acaso, não por imposição. Alargar `ADR_RANGE` faria
`ref-lint` ficar vermelho por ADR-020 — e é a resposta certa se a decisão for que o canon inclui
os quatro. Isso é decisão de âmbito do corpus, não de um gerador: fica em GAP-07, com a acção
recomendada escrita, em vez de ser tomada em silêncio aqui. Este ticket recusa continuar a
afirmar 20/20; não decide qual dos dois números é o canon. **Decidido por AOS-314**, no
sentido de alargar o canon a ADR-023.

---

---

## AOS-314 — O canon de ADRs que os gates lêem parava em ADR-019, quatro aquém do catálogo

<!-- rtm: adrs-mencionados -->

### Contexto

Decisão de GAP-07, tomada: **o canon gated passa a ser ADR-001…023**.

`ADR_RANGE` valia `range(1, 20)` em `scripts/ci/rtm-regenerate.py` e em `scripts/ci/ref-lint.py`.
Consequência dupla: a §4 da RTM não listava ADR-020…023, e o `ref-lint` — que falha quando um ADR
do canon não tem ticket implementador — não exigia nada deles. O catálogo em `docs/adr/README.md`
tem 23 entradas, três delas com documento materializado e estado (*Aceite*, *Proposto*,
*Ratificado e assinado*). GAP-07 mediu a diferença e registou-a em vez de a decidir; este ticket
decide-a no sentido de alargar.

O alargamento **não era gratuito**: ADR-020 tinha **zero** tickets a citá-lo e, sozinho, punha o
`ref-lint` vermelho. Não foi contornado inventando um implementador. O próprio ADR-020 nomeia os
seus, em `docs/adr/ADR-020-planeador-agente-governado.md` §5 («Verificação: AOS-234 …; AOS-244 …»)
e §6 («AOS-234, AOS-235, AOS-236, AOS-237, AOS-244»). Esses cinco tickets, em `specs/EPIC-19`,
realizavam a decisão e não a citavam — a lacuna era da citação, não da cobertura. A edição v1.2 da
RTM já tinha tentado registá-lo à mão, atribuindo ADR-020 a AOS-234/235/237; foi apagada pela
regeneração seguinte, porque a atribuição vivia na tabela em vez de viver no corpus (ver AOS-313).

Fica um resíduo que este ticket **não** fecha, registado como lacuna própria na §7: os catálogos de
*enunciado* estão atrás do de documentos. `_BRIEF` §3 lista 14 ADRs, `specs/00` §11 lista 19, e
`docs/adr/README.md` lista 23 — e o próprio README declara que os dois primeiros «continuam a ser a
referência de enunciado para todos os ADRs». A §1 da RTM citava `_BRIEF` §3 como fonte da gama, o
que já era falso a 19 e ficaria pior a 23; passa a citar o catálogo que tem de facto a gama.

### Critérios de Aceitação

- [x] `ADR_RANGE` cobre ADR-001…023 nos **dois** gates que o lêem, sem divergirem
- [x] Nenhum ADR do canon alargado fica sem ticket implementador: ADR-020 passa a ser citado pelos
      cinco tickets que o próprio ADR nomeia, na `specs/EPIC-19`, com a citação escrita no bloco de
      cada um — no corpus, não na matriz
- [x] A §1 da RTM deixa de citar `_BRIEF` §3 como fonte da gama de ADRs: cita o catálogo que a tem
- [x] GAP-07 passa a lacuna fechada, com a decisão registada; a divergência que sobra — catálogos
      de enunciado atrás do catálogo de documentos — abre como lacuna nova, com as três contagens
      **derivadas** dos ficheiros, não escritas à mão
- [x] `ref-lint.sh` e `rtm.sh` verdes com 23 ADRs

### Estado

**ENTREGUE** (2026-09-03). P2.

`scripts/ci/rtm-regenerate.py`, `scripts/ci/ref-lint.py`, `specs/EPIC-19_Planeador_Meta_Orquestracao.md`,
`tecnica/16_Rastreabilidade_RTM.md`.

**Nota sobre o que o alargamento passa a exigir.** ADR-021 e ADR-022 estão *Proposto*, não *Aceite*.
Exigir-lhes ticket implementador é um requisito mais forte do que o estado deles justifica — hoje
passa porque ambos têm tickets (ADR-021 com 3, ADR-022 com 6), mas um ADR novo em *Proposto* passará
a avermelhar o `ref-lint` até ter ticket. É a consequência aceite ao escolher alargar; se se revelar
incómoda, a alternativa é filtrar `ADR_RANGE` por estado, e isso é outro ticket.

---

---

## AOS-315 — A coluna de documentos técnicos da §4 resolvia-se pela amplitude do conjunto, não por ticket

<!-- rtm: adrs-mencionados -->

### Contexto

`infer_docs_for_tickets` (`scripts/ci/rtm-regenerate.py`) reduzia a lista de tickets de um ADR a
**um intervalo** — `low, high = min(nums), max(nums)` — e depois somava os documentos de *todas* as
gamas de `DOC_RANGES` que o intervalo intersectasse. Um ADR com dois tickets afastados herdava assim
tudo o que estivesse entre eles.

Medido: **ADR-014** (taxonomia de autonomia L0–L5) é implementado por AOS-022, AOS-089, AOS-090 e
AOS-125. O intervalo 022…125 atravessa dez gamas, e a §4 declarava a decisão desenvolvida em **onze**
documentos — entre eles `tecnica/03` (orquestração) e `tecnica/06` (model gateway), que não a
desenvolvem. Por ticket são **três**: `tecnica/02`, `tecnica/09`, `tecnica/15`. Dezassete das
dezanove linhas da §4 tinham a coluna inflacionada pelo mesmo mecanismo.

Segundo defeito, do mesmo sítio: **`tecnica/18_Planner_Meta_Orquestracao.md` não existia em
`DOC_RANGES`**. Os tickets do planeador (AOS-230…244) caíam na gama aberta `AOS-190→`, cuja
justificação escrita é a EPIC-18 («remediação transversal … `tecnica/11` e `tecnica/09`»). O
resultado é que o documento técnico do planeador era invisível à matriz, e a §4 atribuía as decisões
do planeador aos documentos de governação e de convenções de engenharia. A linha nova de ADR-020
(AOS-314) teria nascido a afirmar isso.

Este ticket é irmão de AOS-312: ali o gerador assumia «o último epic» em vez de derivar o epic de um
ticket; aqui assume «tudo o que está entre o primeiro e o último» em vez de resolver ticket a ticket.
A mesma troca de uma derivação por uma aproximação que ninguém confrontava.

### Critérios de Aceitação

- [x] A coluna resolve-se **por ticket** e une os resultados; um ADR deixa de herdar documentos por
      ter dois tickets afastados
- [x] A gama aberta `AOS-190→` passa a ser **recurso**, aplicada só a tickets que nenhuma gama
      explícita cobre — mantendo a propriedade que a justifica (um ticket novo herda um mapeamento
      em vez de cair em «—» silenciosamente) sem a alastrar a tickets já mapeados
- [x] `DOC_RANGES` ganha a gama do planeador (AOS-230…244 → `tecnica/18`), pelo que ADR-020 nomeia
      o documento que o desenvolve e mais nenhum
- [x] `validate_section4_docs` recusa qualquer documento nomeado na coluna que não exista em
      `tecnica/`, e falha fechado — a coluna passa a ser confrontável com o disco, não só com uma
      tabela
- [x] O efeito é medido e declarado: 17 das 19 linhas existentes mudam de coluna, todas por
      **remoção** de documentos que não desenvolvem a decisão

### Estado

**ENTREGUE** (2026-09-03). P2.

`scripts/ci/rtm-regenerate.py`, `tecnica/16_Rastreabilidade_RTM.md`.

**Ressalva.** Este ticket torna a coluna *mais* verdadeira, não verdadeira. O mapeamento continua a
ser por gama de tickets (`DOC_RANGES`), escrito à mão, e uma gama mal atribuída continua a produzir
uma coluna errada sem que nada o detecte — a asserção nova só garante que o documento nomeado
**existe**, não que desenvolve a decisão. Fechar isso exigiria as citações de ADR a viverem nos
próprios `tecnica/*.md`, e é decisão de âmbito do corpus.

---
