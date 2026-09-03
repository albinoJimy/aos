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
ausente ou a mentir sobre o que faz. Sete tickets, três eixos:

| Eixo | Tickets |
|---|---|
| Governação da autonomia (`/autonomy`) | AOS-305, AOS-306, AOS-307 |
| Cerimónia de quatro-olhos (`/challenge`, `/approve`) | AOS-308, AOS-309 |
| Rastreabilidade da política (PDP) | AOS-310, AOS-311 |

### 0.1 Ordem sugerida

| Prioridade | Tickets | Racional |
|---|---|---|
| **P0** | AOS-305, AOS-306 | Removem ou mentem sobre o gate humano de acções `danger` |
| **P1** | AOS-307, AOS-309, AOS-311 | Corrompem a durabilidade da decisão ou a atribuição de uma negação |
| **P2** | AOS-308, AOS-310 | Superfície mal classificada; lacuna de rastreabilidade de política |

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
