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
ausente ou a mentir sobre o que faz. Dezanove tickets, cinco eixos:

| Eixo | Tickets |
|---|---|
| Governação da autonomia (`/autonomy`) | AOS-305, AOS-306, AOS-307 |
| Cerimónia de quatro-olhos (`/challenge`, `/approve`) | AOS-308, AOS-309 |
| Rastreabilidade da política (PDP) | AOS-310, AOS-311 |
| Rastreabilidade do corpus (RTM) | AOS-312, AOS-313, AOS-314, AOS-315, AOS-317, AOS-318, AOS-319, AOS-472, AOS-473, AOS-475 |
| Integridade das ferramentas de gate | AOS-316, AOS-474 |
| Semântica da extracção | AOS-318 (feito) |

> **AOS-312 não vem da §3.** Os sete primeiros são achados activos do documento-fonte; o oitavo
> vem da **§5** (o meta-achado sobre asserções que nenhum gate lê) e nasceu do acto de remediar
> este epic: foi ao regenerar a RTM depois de abrir AOS-305..311 que a §6 mudou de «EPIC-21» para
> «EPIC-22» na linha de AOS-194 — que vive na EPIC-18 — e o defeito do gerador ficou visível. O
> precedente é AOS-279, criado pela remediação da EPIC-20. **AOS-313** tem a mesma
> origem: nasceu da ressalva que AOS-312 deixou por endereçar — a §7 da RTM, que a §5
> nomeia como «o exemplar mais limpo» do meta-achado. **AOS-314** fecha a decisão que
> AOS-313 registou como GAP-07 em vez de tomar, e **AOS-315** corrige o defeito que essa
> decisão descobriu ao acrescentar quatro linhas à §4. **AOS-316** vem do mesmo sítio por
> outra via: foi a ferramenta que prova os gates que corrompeu, ela própria, o trabalho de
> AOS-314 — e nenhum gate deu por isso. **AOS-317** é a crítica ao AOS-314: o canon
> foi fechado com um literal novo (`range(1, 24)`) em vez de derivar do registo, e
> **AOS-318** fecha o marcador de ADRs mencionados que AOS-313 introduziu sem ticket.
> **AOS-319** fecha a última metade: nem a guarda de AOS-312 nem a de AOS-313 lêem
> NÚMEROS, e foi o próprio AOS-314 a deixar um («ADR-001…019» sobre uma tabela de
> vinte e três linhas).

### 0.1 Ordem sugerida

| Prioridade | Tickets | Racional |
|---|---|---|
| **P0** | AOS-305, AOS-306 | Removem ou mentem sobre o gate humano de acções `danger` |
| **P1** | AOS-307, AOS-309, AOS-311 | Corrompem a durabilidade da decisão ou a atribuição de uma negação |
| **P2** | AOS-308, AOS-310 | Superfície mal classificada; lacuna de rastreabilidade de política |
| **P2** | AOS-312 | Atribuição ticket↔epic falsa e auto-renovável na RTM; sem efeito no binário |
| **P2** | AOS-313 | Cobertura afirmada contra as matrizes geradas do próprio ficheiro; sem efeito no binário |
| **P2** | AOS-314, AOS-315 | Âmbito do canon de ADRs e coluna de documentos da §4; sem efeito no binário |
| **P2** | AOS-316 | A suite que prova os gates corrompe trabalho em curso quando concorrente; sem efeito no binário |
| **P2** | AOS-317, AOS-318 | Canon fechado com literal em vez de derivado; ADR mencionado a contar como implementado |
| **P2** | AOS-319 | Contagens e extremos de intervalo gerados sem derivação; sem efeito no binário |
| **P2** | AOS-472 | Resíduo do AOS-318: uma cerca solta desloca pares (ticket, ADR) sem gate que o veja; sem efeito no binário |
| **P2** | AOS-473 | Resíduo do AOS-318: pares que o corpus declara restrições contados como entregas; sem efeito no binário |
| **P2** | AOS-474 | O veredicto do `run.sh` dizia «todos verdes» com etapas saltadas; sem efeito no binário |
| **P2** | AOS-475 | Nenhum gate compara os pares (ticket, ADR) com os do merge-base; sem efeito no binário |

### 0.2 Tabela-resumo

| Ticket | Defeito | P | Estado |
|---|---|---|---|
| AOS-305 | `/autonomy` autoriza-se com a assinatura de um só operador, sem papel, tecto nem four-eyes | P0 | **implementado** (1 AC cumprida na substância, não na forma citada) |
| AOS-306 | Uma selagem falhada aplica o nível de autonomia e a API responde que o recusou | P0 | **implementado** (+ residual da demoção, achado em revisão) |
| AOS-307 | O nível aplicado por `/autonomy` não sobrevive a um reinício do nó | P1 | **implementado** — o ticket criou um vector novo e a remediação teve de o fechar |
| AOS-308 | `POST /runs/{id}/challenge` não autentica nada, e o comentário do handler diz que autentica | P2 | **implementado** (1 residual declarado) |
| AOS-309 | `FourEyesGate.Authorize` não sela nem regista nenhuma negação | P1 | **implementado** — o primeiro teste era tautológico e foi reescrito |
| AOS-310 | `PDP.Reload` nunca corre em produção; o nó não emite `policy.changed` | P2 | **implementado** (1 residual: S-02 partilha raiz com S-01) |
| AOS-311 | `audit.FileStore.Append` não consulta `ctx`; o fail-closed por timeout é condicional ao sink | P1 | **implementado** — partiu o canal HITL e a correcção foi transversal |
| AOS-312 | A §6 da RTM afirmava o epic de um ticket sem o derivar, e nada confrontava o gerador com a fonte | P2 | **ENTREGUE** |
| AOS-313 | A §7 da RTM afirmava cobertura que as suas próprias secções geradas contradiziam | P2 | **ENTREGUE** |
| AOS-314 | O canon de ADRs que os gates lêem parava em ADR-019, quatro aquém do catálogo | P2 | **ENTREGUE** |
| AOS-315 | A coluna de documentos técnicos da §4 resolvia-se pela amplitude do conjunto, não por ticket | P2 | **ENTREGUE** |
| AOS-316 | O `selftest.sh` muta ficheiros do repositório sem exclusão mútua, e dois runs corrompem-se um ao outro | P2 | **ENTREGUE** |
| AOS-317 | O canon de ADRs fechou-se com um literal novo; a fonte continua sem quem a leia | P2 | **ENTREGUE** |
| AOS-318 | Um ticket não pode mencionar um ADR sem alegar que o implementa | P2 | **FEITO** |
| AOS-319 | A RTM escrevia à mão contagens e extremos de intervalo que as suas próprias tabelas contradiziam | P2 | **ENTREGUE** |
| AOS-472 | Uma linha de prosa começada por três crases ou três tis desloca pares (ticket, ADR) sem que nenhum gate dê por isso | P2 | **FEITO** |
| AOS-473 | Pares (ticket, ADR) que o próprio corpus declara restrições contavam como entregas, e uma emenda declarada não contava | P2 | **FEITO** |
| AOS-474 | O veredicto do `run.sh` diz «TODOS OS GATES VERDES» com um gate que saltou etapas | P2 | **FEITO** |
| AOS-475 | Nenhum gate compara os pares (ticket, ADR) de um commit com os do merge-base | P2 | ABERTO |

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

**IMPLEMENTADO.** P0. Mudar para L4/L5 exige **duas assinaturas de operadores distintos**
com a capability `autonomy:set`, e as DUAS provas ficam no selo — o que a rehidratação tem de
poder reverificar é que foram duas pessoas, e isso não se lê de uma string com uma vírgula
(`packages/cmd/aos/autonomy_route.go`, `autonomyDualControlRequired`; teste
`aos305_autonomy_dual_control_test.go`).

**Divergência de forma, declarada:** a capability vive numa lista própria
(`AOS_AUTONOMY_SETTERS`), validada no arranque contra `AOS_OPERATORS`, e não no vocabulário
fechado de `/approve` que o critério nomeava. A substância — só quem tem o papel muda o nível, e
sozinho não muda — está cumprida; a forma citada não.

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

**IMPLEMENTADO.** P0. A ordem está invertida: **sela-se ANTES de aplicar**, e a falha
devolve `ErrSealFailed` sem mutar nada (`packages/control-plane/governance/autonomy/registry.go`
— «1) SELAR … 2) APLICAR — só depois de o selo existir na hash-chain»). O handler distingue o
caso: `503` com «selagem no WORM indisponivel — nivel NAO aplicado», separado do `400` de nível
recusado.

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

**IMPLEMENTADO.** P1. `LevelRegistry.Rehydrate` existe, no molde do `Revocations.Rebuild`,
e é chamado no arranque ANTES de o ambiente ser aplicado, com validador de provas cuja raiz de
confiança está fora do WORM (`packages/cmd/aos/bootstrap.go` → `cfg.Autonomy.provision(...
WithRehydrateValidator)`). Testes `aos306_307_autonomy_node_test.go`, `aos307_precedencia_test.go`,
`aos307_rehydrate_auth_test.go`.

**Desvio deliberado a um critério, declarado:** um registo irreconfirmável é **saltado e
declarado**, não aborta o arranque. O critério pedia abortar; abortar dava um modo de tijolo — um
nó que não volta a arrancar por causa de um registo antigo. Só a falha de leitura aborta.

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

**IMPLEMENTADO.** P2. O pedido de challenge é **assinado pelo aprovador nomeado**, com
chave pinada no roster, nonce de uso único e frescura, e o emissor tem de SER o aprovador — senão
um detentor de uma chave inundava o registo em nome dos outros (`packages/cmd/aos/api.go`,
`handleChallenge`). O comentário que dizia «autenticado pela mesma admission que o /approve»
enquanto a rota não verificava identidade nenhuma foi substituído pela descrição do que o código
faz.

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

**IMPLEMENTADO.** P1. Toda a negação fica no log do operador, correlável por `request_id`
e por `run`, com a razão sanitizada e os MESMOS campos nas duas vias (gate directo e broker) —
`packages/cmd/aos/api.go`; teste `aos309_approve_denial_log_test.go`. A resposta HTTP mantém-se
uniforme, que é o que impede o canal de virar oráculo.

**Divergência de via, declarada:** fechou por **log correlável**, não por selo no WORM.
`FourEyesGate.Authorize` continua puro (`packages/integration/foureyes.go`); a correcção ficou no
chamador, como o primeiro critério permitia.

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

**IMPLEMENTADO.** P2. O nó provisiona o changelog de política no arranque, fail-closed e
idempotente por (versão, `ContentHash`), e declara-o no banner (`packages/cmd/aos/policy_changelog.go`,
chamado do `bootstrap.go`; teste `aos310_policy_changelog_test.go`).

**O título deste ticket continua literalmente verdadeiro, e isso é deliberado:** `PDP.Reload` não
tem chamador de produção — uma varredura por `.Reload(` fora de testes devolve vazio. O que o
ticket remediava era o nó não emitir `policy.changed`; o recarregamento a quente não foi composto,
e a ausência está escrita no `bootstrap.go` em vez de ficar por explicar.

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

**IMPLEMENTADO.** P1. `audit.FileStore.Append` consulta o `ctx` em **dois** pontos —
depois de verificada a posse e antes de `persist` — e devolve o próprio `ctx.Err()`, distinguível
de `ErrParticaoAlheia` (`packages/platform/audit/filestore.go`). A ordem posse→ctx está
documentada como não-corrida. Testes `aos311_ctx_test.go` (que fixa exactamente dois pontos de
consulta) e `aos311_selo_nao_cancelavel_test.go`.

**Por verificar:** o critério que manda o `tecnica/17` §4.3-D deixar de declarar «timeout
fail-closed» sem qualificação — `NÃO VERIFICADO`, não foi lido nesta passagem.

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

---

## AOS-316 — O `selftest.sh` muta ficheiros do repositório sem exclusão mútua, e dois runs corrompem-se um ao outro

<!-- rtm: adrs-mencionados -->

### Contexto

`scripts/ci/selftest.sh` prova que os gates bloqueiam falhas injectando cada falha **na árvore
real** e restaurando-a a seguir. O restauro é por processo: cada run tira o seu backup no arranque
(`SIG_BAK`, `RTM_GEN_BAK`, `:53` e `:65`) e repõe-no no `trap cleanup EXIT INT TERM` (`:76`).
Não há exclusão mútua. Dois runs sobrepostos partilham os mesmos ficheiros e backups diferentes,
tirados em instantes diferentes — e o restauro de um escreve por cima do trabalho do outro.

**Medido, não hipotético.** Durante AOS-314/315 corriam três `selftest.sh` em fundo, sobrepostos. Um
deles tinha tirado o backup de `scripts/ci/rtm-regenerate.py` num commit anterior; ao chegar a §R fez
`cp "$RTM_GEN_BAK" "$RTM_GEN"` (`:735`) e repôs essa versão por cima das edições em curso. O
resultado foi um commit cuja mensagem descrevia o alargamento do canon de ADRs e cujo conteúdo
**revertia** a resolução por ticket da §4 — um commit que dizia uma coisa e continha outra, que foi
preciso desfazer com `git reset --hard` e refazer. O defeito não foi detectado por nenhum gate: foi
detectado por comparar a árvore com a da branch anterior.

**Segundo modo de falha, do mesmo sítio:** o `trap` cobre `EXIT INT TERM`, não `KILL`. Um run
terminado à força deixa rasto — verificado: `rtm-regenerate.py` ficou modificado, com 130 linhas
removidas, e a árvore só voltou ao lugar com `git checkout --`. Um run seguinte tomaria esse ficheiro
como «árvore real» e tiraria dele o seu backup, propagando a corrupção em vez de a denunciar.

**Terceiro:** os controlos positivos (§P3, §Q4, §R3, §S3 — «o gate continua verde contra a árvore
REAL») ficam vermelhos quando outro run tem uma mutação em voo. Estão a dizer a verdade, e é essa a
função deles; mas o diagnóstico que produzem — «POSSÍVEL RASTO no repo» — aponta para o repositório
quando a causa é a concorrência, o que manda quem investiga para o sítio errado.

Superfície mutada na árvore real, hoje: `packages/control-plane/pdp/policies/aos_authz.sig` (§B),
`scripts/ci/rtm-regenerate.py` (§R, §S), `packages/_selftest_bad` (§A) e `packages/_selftest_eventcat`
(§O). Os §§P, Q e S4 já trabalham sobre **cópias** em `mktemp -d` — o molde certo já existe no
ficheiro, só não é universal. §R e §S1–S3 mutam o original porque `rtm-regenerate.py` resolve a raiz
do repositório a partir do próprio caminho e não aceita sobreposição, ao contrário de `ref-lint.py`,
que tem `AOS_REFLINT_ROOT` (`scripts/ci/ref-lint.py:90`) precisamente para isto.

*Atenuante:* na CI o gate corre uma vez por job, pelo que a sobreposição não acontece lá. Morde no
uso local e agêntico — onde correr a suite em fundo enquanto se continua a editar é o caso normal, e
foi onde mordeu.

### Critérios de Aceitação

- [x] Um segundo `selftest.sh` iniciado enquanto outro corre **não** muta nada: ou espera pelo
      *lock*, ou sai != 0 com mensagem própria que nomeie a concorrência — nunca uma mensagem de
      «POSSÍVEL RASTO no repo», que manda investigar o sítio errado
- [x] O *lock* é libertado por `trap` E sobrevive a um run morto sem `trap`: um *lock* órfão de um
      processo que já não existe não bloqueia a suite para sempre (PID no ficheiro de *lock*, ou
      `flock`, que o liberta ao fechar o descritor)
- [x] Um run que encontre a superfície mutada no arranque — `git status --porcelain` sujo nos
      caminhos que ele próprio muta — recusa arrancar e diz quais, em vez de tirar backup de uma
      árvore já corrompida e propagar a corrupção
- [x] `rtm-regenerate.py` aceita sobreposição da raiz por variável de ambiente, no molde de
      `AOS_REFLINT_ROOT` (`ref-lint.py:90`), e §R e §S passam a mutar uma **cópia**. Reduz a
      superfície mutada na árvore real à assinatura de política (§B) e aos dois módulos sintéticos
- [x] Prova de que a exclusão funciona: dois runs lançados em paralelo, o segundo tem de recusar ou
      esperar, e a árvore fica limpa no fim — falsificável, no molde dos self-testes existentes
- [x] `AGENTS.md` (ou o README de `scripts/ci/`) declara que a suite muta a árvore de trabalho e não
      deve correr concorrente com edições — hoje isso não está escrito em lado nenhum

### Estado

**ENTREGUE** (2026-09-04). P2.

`scripts/ci/selftest.sh` (lock por `mkdir` no *gitdir*, guarda de superfície, §T1–T5),
`scripts/ci/rtm-regenerate.py` (`AOS_RTM_ROOT`), `AGENTS.md`.

Sem efeito no binário entregue: é higiene de ferramentas de CI. Ficou em P2 e não acima porque na CI
o modo de falha não existe (um job, um run). O custo real já foi pago uma vez, em trabalho refeito e
num commit que teve de ser desfeito.

**A superfície mutada encolheu**, que era o ponto: `scripts/ci/rtm-regenerate.py` saiu dela. §R e §S
passam a injectar as falhas numa cópia do gerador apontada a uma cópia do corpus por `AOS_RTM_ROOT`;
da árvore real só lêem, nos controlos positivos R3/S3. Resta a assinatura de política (§B) e os dois
módulos sintéticos (§A, §O) — os três protegidos pelo *lock* e pela guarda. §T5 fica de sentinela: se
alguém devolver §R/§S à árvore real, `git status` sobre o gerador deixa de vir vazio e o self-test
fica vermelho.

**Duas escolhas que merecem nota.** O *lock* é `mkdir` e não `flock` porque **não há `flock` no Git
Bash de Windows** (verificado); `mkdir` é a primitiva atómica portável. E vive no *gitdir*, não na
árvore: nunca aparece em `git status` e é por worktree, que é o âmbito certo — as mutações são da
árvore de trabalho, e worktrees diferentes não se estorvam.

**Ressalva.** Isto fecha a concorrência entre runs da suite, não a concorrência entre a suite e um
humano (ou agente) a editar. Um run que arranca sobre a árvore limpa e vê os ficheiros mudarem por
baixo dele continua a poder restaurar uma versão velha por cima de uma edição posterior — o
`AOS_RTM_ROOT` retira o gerador desse risco, mas a assinatura de política e os módulos sintéticos
continuam expostos. A mitigação é a linha nova em `AGENTS.md`; fechá-la a sério exigiria a suite
correr sobre um `git worktree` descartável, e isso é decisão de âmbito das ferramentas de CI.

---

## AOS-317 — O canon de ADRs fechou-se com um literal novo; a fonte continua sem quem a leia

<!-- rtm: adrs-mencionados -->
<!-- Este ticket não implementa nenhum ADR: fala do REGISTO deles. Até AOS-318 escrevia os
     números por extenso para não se inscrever na §4 da RTM como implementador do ADR-001 e do
     ADR-014; voltou aos códigos canónicos sob o marcador de bloco acima, que é a forma certa
     para um bloco que só menciona (AOS-318). -->

### Contexto

AOS-314 fechou o canon curto alargando `ADR_RANGE` de `range(1, 20)` para `range(1, 24)`,
nos dois ficheiros onde a constante vive — `scripts/ci/rtm-regenerate.py:35` e
`scripts/ci/ref-lint.py:95`. O número passou a estar certo. **O mecanismo não.**

O que produziu o defeito não foi o valor `20`: foi um literal escrito à mão, duplicado em
dois leitores do corpus, que ninguém compara com a fonte que o devia fixar. Isso continua
inteiro depois de AOS-314. No dia em que entrar o ADR-024 no registo, o canon volta a ficar
curto, **nos mesmos dois sítios**, e nada o dirá — o `ref-lint` deixa outra vez de exigir
ticket implementador ao ADR novo, e fica verde por não olhar. Foi exactamente assim que o
canon parado no ADR-019 sobreviveu a quatro decisões.

O corpus tem três listas de ADRs e elas divergem por natureza, não por descuido:
`_BRIEF` §3 fixa o enunciado do núcleo fundacional; `specs/00` §11 é referência de
enunciado; e `docs/adr/README.md` declara-se **registo canónico**, é a única completa e a
única que regista o **estado** de cada decisão. Só a terceira pode ser fonte de «que ADRs
existem» — e era a única que nenhum gate lia.

### Critérios de Aceitação

- [x] `ADR_RANGE` deixa de ser literal nos dois gates: `scripts/ci/adr_register.py` deriva
      o registo de `docs/adr/README.md` e é importado por `rtm-regenerate.py` **e** por
      `ref-lint.py` — uma fonte, não duas cópias que envelhecem juntas
- [x] A derivação falha **fechada** em três eixos: tabela ausente ou com cabeçalho mudado,
      códigos não contíguos a partir do primeiro (o README promete que «códigos nunca são
      reutilizados»), e estado fora do vocabulário fechado que o próprio README enumera
- [x] O import resolve mesmo quando o gate é carregado **por caminho** e não corrido como
      script — é como o §P1 do `selftest.sh` o carrega, e sem isso o subteste ficava
      vermelho a dizer que «o predicado não discrimina», que é outra coisa
- [x] A §4 mostra o **Estado** de cada decisão, em coluna própria vinda do registo: duas
      das vinte e três estão *Propostas*, e sem a coluna liam-se com a mesma autoridade de
      uma ratificada
- [x] A guarda `assert_numeric_claims` reconhece as **quatro** notações de intervalo que o
      documento usa, incluindo o separador « a » por extenso — que a §1.5 usa e que o padrão
      não via. Buraco com consequência: a forma com dois pontos era recusada e a mesma
      afirmação escrita com « a » no meio passava incólume. Um padrão incompleto não é só cego, **ensina** a
      usar a forma que não vê
- [x] `specs/00` §11 e `_BRIEF` §3 declaram o que são: a primeira é completada e passa a
      dizer que não é o inventário; a segunda assume-se como núcleo fundacional e remete —
      **deliberadamente não** copiada para vinte e três entradas, que seria a terceira cópia
      a envelhecer em silêncio
- [x] Um self-test injecta a falha em cada eixo novo e exige vermelho *pela mensagem da
      guarda*, sobre a **cópia** do corpus que AOS-316 tornou possível

### Estado

**ENTREGUE** (2026-09-04). P2.

`scripts/ci/adr_register.py` (novo — a derivação), `scripts/ci/rtm-regenerate.py` (fonte +
coluna Estado + quarta notação), `scripts/ci/ref-lint.py` (fonte), `scripts/ci/selftest.sh`,
`tecnica/16_Rastreabilidade_RTM.md` (§§1,4 regeneradas), `specs/00_System_Spec.md` (§11),
`_BRIEF.md` (§3). Gates: `rtm.sh`, `ref-lint.sh` e `selftest.sh` verdes.

**O que fica escrito, por ser mais geral do que o ticket.** Corrigir o valor de uma
constante escrita à mão não corrige nada — devolve o gate ao estado em que estava antes de
apodrecer, com o mesmo relógio a andar. A pergunta a fazer a cada literal num gate é «quem
compara isto com a fonte?», e quando a resposta é «ninguém», o número certo de hoje é só o
número errado de amanhã. AOS-314 não estava errado; estava incompleto, e o que faltava não
era um número maior.

---

## AOS-318 — Um ticket não pode mencionar um ADR sem alegar que o implementa

### Contexto

A §4 da RTM é construída por correspondência textual: `extract_all_tickets()` recolhe
**todos** os códigos `ADR-NNN` que aparecem no bloco de um ticket, e cada um vira uma
entrada na coluna «tickets que o implementam». Não há forma de citar uma decisão para a
discutir, para delimitar âmbito, ou para explicar um defeito — a citação *é* a alegação.

Descoberto ao escrever o AOS-317, e por ele: o bloco mencionava duas decisões em prosa
(<!-- rtm: menção -->«o catálogo pára no ADR-014», «códigos contíguos a partir do ADR-001»<!-- /rtm: menção -->)
e inscreveu-se como implementador de ambas, na matriz que o próprio ticket existe para
arrumar. Ficou contornado escrevendo os números por extenso em vez dos códigos, o que
resolvia um caso e não a classe.

**Medido antes de aberto**, para que não se confunda armadilha com dívida: 357 pares
(ticket, ADR) na §4; uma heurística sobre marcadores de delimitação, negação e remissão
sinaliza **2** candidatos, e a leitura dos dois desmente a heurística — AOS-043 («executa
como *activity* durável fora do turno, coerente com…») e AOS-282 («um run é possuído por
exactamente uma réplica — a invariante do…») realizam de facto as decisões que citam.
**Zero atribuições falsas no corpus de hoje.**

O defeito é **prospectivo**: não corrompe a matriz actual, corrompe a próxima que precise
de discutir uma decisão sem a implementar. E já mordeu uma vez — na única ocasião em que o
corpus precisou disso.

### Critérios de Aceitação

- [x] Existe forma de **mencionar** um ADR num bloco de ticket sem entrar na coluna de
      implementadores, e a §4 documenta-a onde o leitor da matriz a encontre
      — duas formas: o bloco inteiro (`<!-- rtm: adrs-mencionados -->`, que já existia desde
      AOS-313) e, novo, o trecho (`<!-- rtm: menção -->` … `<!-- /rtm: menção -->`), que separa
      menção de implementação **no mesmo bloco**. A §4 de `tecnica/16` inclui, logo acima da
      tabela, o parágrafo «Citar não é alegar», com a sintaxe exacta, o efeito e a contagem viva
- [x] O `rtm-regenerate.py` distingue as duas coisas na extracção, e o `ref-lint` não conta
      uma menção como cobertura de ADR (senão a invariante «≥ 1 ticket implementador» passa
      a ser satisfeita por quem só fala da decisão)
      — `adr_citacoes.classificar()` devolve `(implementa, mencionados)` e é a ÚNICA regra, importada
      pelos dois leitores; o gerador guarda as menções à parte (`mencoes`) e conta-as na §4
      (78 pares em 25 ADRs, à data), o `ref-lint` só lê `implementa`. Prova negativa:
      `selftest.sh` §Z1 (só no trecho) e §Z2 (bloco marcado) — o `ref-lint` fica vermelho com o
      ADR só mencionado **na própria lista** «sem ticket implementador»; §Z3 é o controlo positivo
- [x] A escolha do mecanismo fica registada com o custo de migração à frente: um marcador
      inline, semântica por secção do bloco, ou um campo explícito a substituir a extracção
      textual — as duas últimas deslocam pares existentes, e os 357 de hoje são a linha de
      base de não-regressão
      — registada em «Entrega» abaixo e no cabeçalho de `scripts/ci/adr_citacoes.py`, medida sobre a
      linha de base de **hoje**, que já não é 357 mas **445** pares: inline 0, por secção 332,
      campo explícito 214. O conjunto dos 445 pares sai **idêntico** da mudança
- [x] Os blocos de AOS-317 e AOS-318 largam a notação «ADR n.º NN» e voltam aos códigos
      canónicos — é o teste de aceitação mais honesto que estes dois blocos podem ter
      — AOS-317 sob o marcador de bloco (não implementa nenhum ADR), AOS-318 com o trecho; nenhum
      dos dois entra na §4. A única ocorrência que resta da notação é o texto deste critério, que
      a nomeia

### Entrega

**O ticket estava meio feito antes de ser aberto, e o Contexto acima não o sabia.** O marcador de
bloco `<!-- rtm: adrs-mencionados -->` existe desde AOS-313, honrado pelos dois leitores, e 36
blocos já o usavam. Faltava-lhe o que o título deste ticket pede: é **tudo-ou-nada**. Um ticket
que implementa um ADR e precisa de nomear outro como restrição tinha de escolher entre perder a
cobertura do primeiro ou inventar a do segundo — e o corpus escolheu a segunda, **por escrito**,
seis vezes: AOS-417, AOS-423, AOS-424, AOS-427 e AOS-430 declaram nos seus comentários 16 pares
(ticket, ADR) de restrições contadas como entregas; AOS-442 declara o caso inverso, uma emenda que
a §4 não lhe liga. A afirmação «zero atribuições falsas» do Contexto foi medida a 357 pares, antes
de esses blocos existirem; hoje não é verdade, e são os próprios blocos a dizê-lo.

**Mecanismo escolhido: marcador inline, ao nível do trecho, aditivo ao de bloco.**

| Opção | Pares deslocados (de 445) | Porquê |
|---|---|---|
| Marcador inline (escolhido) | **0** | Nenhum bloco o usa até alguém o escrever. O marcador de bloco mantém o significado, com uma diferença: escrito **entre crases** deixou de contar (em 2e218bf contava) — só o AOS-329 o tem assim, sem nenhum ADR no bloco, pelo que o efeito hoje é nulo |
| Semântica por secção (só os Critérios de Aceitação alegam) | 332 | Só 113 pares têm o ADR nos CA; o resto vive na tabela de campos, no Contexto e no Estado. Método: secções com cabeçalho `### Critérios de Aceitação` (ou `####` num ticket `###`), sem distinguir maiúsculas, até ao cabeçalho seguinte. Contando também as 74 secções em negrito (`**Critérios de aceitação**`) até ao cabeçalho seguinte, seriam 170 nos CA e **275** deslocados — a revisão mediu 156/289 com outra fronteira; a ordem de grandeza, e a decisão, não mudam |
| Campo explícito (a linha `Documentos de referência`/`relacionados` como fonte única) | 214 | Só 231 pares têm o ADR nesse campo; não existe nenhum campo `ADRs` no corpus |

O custo do inline é o oposto do das outras duas: é **opt-in**. As atribuições falsas que já estão
escritas ficam até alguém reler o bloco e marcar o trecho — é o resíduo 1.

**O que ficou construído.**

- `scripts/ci/adr_citacoes.py` (novo): `classificar(bloco) -> (implementa, mencionados)`. Um ADR
  citado também **fora** do trecho continua implementado. As formas escrevem-se exactamente assim
  — sem alias: `mencao` sem acento é erro — e comparam-se depois de normalizar para NFC. Falha
  **fechado**: todo o comentário que comece por `rtm` (sem distinguir maiúsculas, com ou sem
  espaços ou dois-pontos) e não seja canónico, um trecho aberto sem fecho, um fecho sem abertura
  ou aberturas encadeadas são erro nos dois gates — uma gralha ignorada devolvia o ADR à coluna em
  silêncio. Directivas **dentro de código** são texto: é o que deixa este ticket documentar o
  mecanismo sem o accionar. A varredura segue o CommonMark nas cercas (carácter e comprimento da
  abertura, ≤ 3 espaços, info string sem crases), no código em linha de N crases e nos
  comentários; as aproximações que restam (código indentado, cercas em itens de lista com 4+
  espaços, crases escapadas) estão enumeradas no módulo, sem caso no corpus, e todas erram para o
  lado de ler uma directiva — que, sozinha, falha fechado.
- `rtm-regenerate.py` e `ref-lint.py` deixam de ter cada um a sua cópia da regra e importam o
  módulo (o molde de `adr_register.py`, AOS-317), e com ele a detecção de cercas de que o
  `mascarar_fences` dos dois depende. O gerador guarda as menções em `mencoes`; a §4 ganha o
  parágrafo «Citar não é alegar» e a contagem viva dos pares que ficam de fora.
- `selftest.sh` §Z1–Z10, sobre uma **cópia** do corpus a que se acrescenta o ADR e o ticket seguintes
  aos maiores — derivados, para a sonda não envelhecer no dia em que esses códigos existirem.

**Não-regressão, medida.** O conjunto (ticket, ADR) extraído antes e depois é o mesmo, par a par:
445 → 445, `cmp` das duas listas ordenadas sem diferenças. A diff de `tecnica/16` reduz-se ao
parágrafo novo da §4 e às quatro palavras «fora de menção declarada» na frase que o precede.

**Mutação.** Trocar, no gerador, a classificação por `set(re.findall(r"ADR-\d{3}", block))` —
extracção textual pura, **sem** o marcador de bloco, ou seja anterior a AOS-313 e não o código
de 2e218bf, que já o honrava — avermelha o `rtm.sh` contra a árvore real (os 78 pares de menção
voltavam à tabela; contra 2e218bf voltariam só os 6 dos blocos de AOS-317/318) e §Z1, §Z3, §Z4 e §Z5;
desligar só o trecho em `adr_citacoes.py` avermelha §Z1 (nos dois leitores) e §Z3; deixar de
mascarar o código avermelha §Z6 — e só ele, porque a árvore real continua verde: é o subteste que
segura essa propriedade. Restaurados, verdes, com o hash dos dois ficheiros igual ao de antes.

Gates: `rtm.sh`, `ref-lint.sh`, `estado-citado.sh` e `lint.sh` verdes; `selftest.sh` completo
verde (94 subtestes, §Z1–Z6 incluídos; ver a revisão abaixo para a segunda passagem).

**Revisão adversarial independente (2026-10-01), sobre 50ef14d.** Duas falhas abertas de severidade
média e cinco menores, todas fechadas num commit por cima, sem reescrever o anterior:

- **Reconhecedor de directivas estreito de mais** (médio, reproduzido): só `rtm:` minúsculo e
  colado era directiva; `<!-- RTM: menção -->`, `<!-- rtm : menção -->`, `<!-- rtm menção -->` e
  `<!--- rtm: menção --->` eram prosa, a menção voltava a implementação e os dois gates ficavam
  verdes — o contrário do que o cabeçalho do módulo prometia. Agora todo o comentário que comece
  por `rtm` é candidato, e não-canónico é erro. §Z7.
- **Máscara de código divergente do CommonMark nos dois sentidos** (médio, reproduzido): as cercas
  alternavam em qualquer linha começada por três crases ou três tis — uma cerca de quatro crases a
  mostrar uma de três, tis dentro de uma cerca de crases, ou três crases a abrir código em linha
  escondiam como código a directiva real seguinte — e o código em linha só conhecia uma crase
  (uma directiva entre crases duplas abria um trecho real). O mesmo
  defeito de cercas vivia no `mascarar_fences` anterior a este ticket. Uma só varredura em
  `adr_citacoes.py` serve agora as duas coisas; contra o corpus de hoje dá **a mesma máscara nos
  25 ficheiros e a mesma classificação nos 458 blocos** que a de 50ef14d. §Z9, §Z10.
- Menores: o critério 1 dizia que a §4 «abre» com o parágrafo, e é o terceiro; a mutação não dizia
  qual era; o método dos 332/113 não estava escrito; faltavam testes para fecho sem abertura,
  aberturas encadeadas e cercas (§Z8, §Z9), e §Z1/§Z2 casavam a mensagem e o código em sítios
  diferentes da saída (passam a exigir o ADR na própria lista); o alias `mencao` não estava
  documentado (saiu: é erro), não havia normalização NFC (há), uma frase do módulo era agramatical
  e uma linha do `ref-lint.py` tinha 150 colunas; e «o marcador de bloco mantém o significado»
  omitia que, entre crases, deixou de contar.

Mutação da segunda passagem, cada uma revertida e o hash do módulo confirmado: reconhecedor
estreito de volta → §Z7 vermelho (as duas variantes); cercas a alternar em qualquer linha de três crases ou tis →
§Z9 vermelho (os três casos); código em linha só de uma crase → §Z10 vermelho; aberturas
encadeadas aceites → §Z8 vermelho. Gates verdes; `selftest.sh` completo verde, a correr sozinho (102 subtestes, §Z1–Z10 com 17).

**Resíduos declarados.**

1. **Os 16 pares que o corpus declara falsos continuam na §4**, e a emenda do AOS-442 continua sem
   ligação. Migrá-los é marcar o trecho das restrições em seis blocos da EPIC-19 — trabalho
   mecânico, mas de outros tickets, e por isso fora deste. Desloca exactamente 17 pares (16 saem,
   1 entra) e merece ticket próprio. *(Fechado pelo AOS-473, com outra conta: saíram 15 dos 16 — um
   par do AOS-423 é restrição e entrega, e fica —, mais 5 pares que os comentários de declaração
   citam como precedente; a emenda do AOS-442 entrou.)*
2. **O terminador do bloco continua duplicado** entre os dois leitores (`fim_do_bloco`). A
   classificação e a detecção de cercas passaram a ser partilhadas; o terminador não, e o
   comentário «muda o outro no mesmo commit» continua a ser a única guarda dessa metade.
3. **Nenhum gate compara o conjunto de pares (ticket, ADR) de um commit com o do anterior.** Uma
   linha de prosa que comece por três tis ou três crases abre, com toda a razão, uma cerca até ao
   fim do ficheiro, e desloca pares sem que `rtm.sh` dê por isso — regenerada, a RTM fica
   sincronizada com o corpus errado. Aconteceu ao escrever a nota da revisão acima, e só a
   comparação manual dos 445 pares o apanhou antes do commit. *(A causa das cercas e dos
   comentários fechou-a o AOS-472; o comparador entre commits é o AOS-475.)*

### Estado

**FEITO** (2026-10-01). P2.

`scripts/ci/adr_citacoes.py` (novo), `scripts/ci/rtm-regenerate.py`, `scripts/ci/ref-lint.py`,
`scripts/ci/selftest.sh` (§Z1–Z10), `tecnica/16_Rastreabilidade_RTM.md` (§4 regenerada),
`specs/EPIC-22_Remediacao_Auditoria_ORQ_SCH_PDP.md` (blocos de AOS-317 e AOS-318).

---

---
## AOS-319 — A RTM escrevia à mão contagens e extremos de intervalo que as suas próprias tabelas contradiziam

<!-- rtm: adrs-mencionados -->

### Contexto

`rtm-regenerate.py` escrevia à mão o extremo do intervalo dos requisitos funcionais em duas linhas
**geradas** — «as **11 capacidades funcionais**» na §1.2 e `RF["RF-01..RF-11"]` no mermaid da §6 —
enquanto a §2 do mesmo ficheiro cataloga **RF-01 … RF-13** desde que a EPIC-19 acrescentou o
planeador e os meta-runs. Corrigir o markdown não servia de nada: a regeneração seguinte repunha o
11. O mesmo do lado dos NFR — `NFR-{n_nfrs}` era a **contagem** de `NFR_SPECS` (10) a passar-se por
**identidade**, com a §3 já em NFR-12 — e outra vez na §7, que herdava `len(NFR_SPECS)` como
denominador *e* como extremo.

É a metade do meta-achado de `analises/10` §5 que nem `validate_section6` (AOS-312) nem
`validate_section7` (AOS-313) cobrem: uma lê pares epic↔ticket, a outra lê citações, e **nenhuma lê
números**. O exemplar mais incómodo é auto-infligido: **AOS-314** alargou `ADR_RANGE` a ADR-023 e
deixou o cabeçalho da §4 a dizer «ADR-001…019», sobre uma tabela de vinte e três linhas — o mesmo
defeito a nascer da própria correcção que o combatia, e a passar pelos gates que essa correcção
tinha acabado de instalar.

Qual é a **fonte autoritativa** era a pergunta por responder, e a resposta não é a óbvia: para os
`RF-NN`/`NFR-NN` é o próprio RTM (§2 e §3), não `specs/00_System_Spec.md`. RF-12/RF-13 e
NFR-11/NFR-12 entraram pela EPIC-19 e **não têm contrapartida** na System Spec. As 11 capacidades de
`specs/00` §4 e os 10 *drivers* de §7 são outra coisa — a origem dos catálogos, não o seu tamanho —
e confundir esse número com o extremo do intervalo era exactamente o defeito.

### Critérios de Aceitação

- [x] `requirement_catalogue()` lê os identificadores das tabelas §2/§3 do próprio RTM e **exige
      contiguidade** `PREFIX-01`..`PREFIX-NN`: sem ela, contagem e extremo deixam de coincidir e
      tudo o que se segue assume que coincidem
- [x] As capacidades de `specs/00` §4 e os *drivers* de §7 passam a ser **contados dos ficheiros**, e
      a §1.2 nomeia-os pelo que são
- [x] O cabeçalho da §4 deriva o extremo de `ADR_RANGE`, fechando os «ADR-001…019» herdados de
      AOS-314
- [x] A §5 ganha as linhas de NFR-11 e NFR-12 e passa a dizer 12/12. Faltava-lhes a **linha**, não a
      prova: AOS-242 fixa o SLI de fracção de planeamento ≤ 5% e AOS-232 deriva o risco das tools
      pinadas — como `analises/10` §3 já registava
- [x] `assert_numeric_claims()` recusa qualquer contagem, extremo de intervalo ou denominador de
      cobertura que não bata com a fonte, em `--check` **e** na regeneração
- [x] A guarda pára no controlo de versões, de propósito: a entrada 1.2 diz «20/20 ADRs, 12/12 NFRs»
      e está certa **enquanto história**, anotada com a regeneração que a desfez. Alinhá-la com os
      números de hoje seria falsificar o registo
- [x] Anti-recorrência em `selftest.sh` §U1–U5, no molde de §R/§S: o regresso literal de
      `RF-01..RF-11`, a contagem de RF na §1.2, o denominador da §7 e a contagem de capacidades
      divorciada de `specs/00` §4, cada um a avermelhar o gate **pelo motivo certo**, com controlo
      positivo contra a árvore real

### Estado

**ENTREGUE** (2026-09-04). P2.

`scripts/ci/rtm-regenerate.py`, `scripts/ci/selftest.sh`, `tecnica/16_Rastreabilidade_RTM.md`.

**Duas coisas que este ticket descobriu sobre os próprios self-testes**, e que valem mais do que a
correcção que as revelou:

1. **§U4 apanhou um ponto cego da guarda nova.** O padrão exigia «*as* N capacidades» e a frase
   gerada diz «*das* N capacidades» — a asserção não cobria a linha que dizia cobrir. Uma guarda
   escrita e nunca falsificada é uma guarda por verificar.
2. **§S2 media o vazio, e ninguém dava por isso.** Injectava NFR-11 como «NFR ausente de
   `NFR_SPECS`»; NFR-11 **passou a existir** ao longo deste trabalho, pelo que a sonda deixou de
   injectar coisa nenhuma e continuava verde — verde por ausência de falha, não por bloqueio.
   Reapontada para NFR-13. É o modo de falha que `analises/10` §5 descreve para achados velhos, a
   acontecer dentro da própria suite anti-regressão: **um teste que envelhece mal é pior do que um
   teste ausente**, porque conta como prova.

**Ressalva — o que fica fora.** `assert_numeric_claims` cobre a RTM, e só a RTM. A §5 de
`analises/10` mediu 61 asserções numéricas em prosa no corpus, com 34 a não baterem, e diz que 21
dos falhanços vivem nos dois `INDICE.md`. Isso mantém-se, verificado hoje contra 316 tickets em 22
epics: `specs/INDICE.md:16` enumera «duas epics em proposta (EPIC-18 e EPIC-19)» quando são cinco
(EPIC-18…22), `:224` afirma «20 epics … backlog AOS-001..275», e `tecnica/INDICE.md:160` afirma «118
tickets AOS-NNN organizados em 11 epics». *Em abono da verdade*, os «189 tickets atómicos
ratificados» de `:16` são um subconjunto **declarado** e continuam defensáveis — o que envelheceu
foi a enumeração das propostas e os totais, não aquele número. Nenhum destes ficheiros é gerado nem
lido por gate nenhum, pelo que fechá-los exige decidir primeiro se passam a ser gerados ou se saem
do corpus — decisão própria, fora deste ticket.

---

## AOS-472 — Uma linha de prosa começada por três crases ou três tis desloca pares (ticket, ADR) sem que nenhum gate dê por isso

### Contexto

Resíduo 3 do AOS-318. Desde esse ticket, a detecção de cercas que os dois leitores do corpus
partilham (`adr_citacoes.py`, importado por `rtm-regenerate.py` e `ref-lint.py`) segue o CommonMark:
uma linha que comece, com até três espaços, por três ou mais crases ou tis abre uma cerca que só
fecha numa linha do mesmo carácter e de comprimento igual ou maior. É a regra certa — e é por ser a
regra certa que uma linha de **prosa** com esse começo abre, com toda a razão, uma cerca que corre
até ao fim do ficheiro. O que fica lá dentro deixa de ser directiva (um trecho de menção volta a
alegar implementação) e os `#` lá dentro deixam de terminar blocos (um ticket absorve os seguintes,
com os ADRs que eles citam).

Nenhum gate o vê. O `rtm.sh` compara a RTM com a regeneração a partir do corpus: com o corpus mal
lido, as duas concordam no erro. O `ref-lint` exige «≥ 1 ticket implementador», que um par a mais
satisfaz e que um par deslocado raramente quebra. E nenhum gate compara o conjunto de pares de um
commit com o do anterior. Aconteceu ao escrever a nota da revisão do AOS-318, e só a comparação
manual dos 445 pares o apanhou antes do commit.

**Medido antes de corrigido**, com a guarda desligada, sobre uma cópia do corpus a que se
acrescentam dois tickets sintéticos seguidos (os cenários de §RTMX1 e §RTMX4 abaixo): três tis
soltos numa linha de prosa do primeiro fazem-no implementar o ADR que só o segundo cita; três
crases soltas, seguidas no mesmo bloco de uma cerca legítima de bash, fazem um ADR declarado num
trecho de menção voltar à coluna de implementadores. Nos dois casos a regeneração fica verde.

### Critérios de Aceitação

- [x] Existe uma guarda barata e fail-closed, e a invariante que impõe está escolhida e justificada
      — **nem uma cerca nem um comentário HTML atravessam a fronteira de um ticket**, verificada
      sobre cada `specs/EPIC-*.md` inteiro por `adr_citacoes.verificar_cercas`, em três condições:
      (1) toda a cerca e todo o comentário fecham; (2) nenhum contém um cabeçalho `## AOS-NNN —` ou
      `### AOS-NNN —` — e um comentário, nenhum cabeçalho de nível 1 a 3; (3) nenhuma cerca contém
      uma linha que, fora dela, abriria uma cerca do mesmo carácter, com comprimento igual ou maior
      e info string. Justificação em «Entrega»; os comentários entraram na revisão (abaixo)
- [x] É erro nos **dois** leitores, pela mesma função: `rtm-regenerate.py` e `ref-lint.py` chamam-na
      sobre o texto de cada EPIC antes de delimitar blocos, e a mensagem diz o ficheiro, a linha da
      abertura e a condição violada
- [x] Verde no corpus de hoje — 214 cercas e 173 comentários em 25 ficheiros `specs/EPIC-*.md`
      (212 cercas na primeira passagem; o AOS-471 e o AOS-474 acrescentaram uma cada), zero violações das
      três condições; o conjunto de pares (ticket, ADR) sai idêntico, 445 implementados e 78 menções
      antes e depois, `cmp` das listas ordenadas sem diferenças, e igual entre os dois leitores
- [x] `selftest.sh` §RTMX, sobre uma cópia do corpus: três tis soltos (RTMX1), três crases soltas
      (RTMX2), uma cerca por fechar no fim do último bloco (RTMX3), uma linha solta que emparelha com
      a cerca legítima seguinte do mesmo bloco (RTMX4) — cada uma a avermelhar os dois leitores
      **pela condição que a apanha** — e o controlo positivo (RTMX5): cercas legítimas, de crases, de
      tis e de quatro crases a mostrar três, ficam verdes nos dois. Da revisão: um `<!--` solto que
      esconde a cerca do AOS-417 (RTMX6), duas linhas soltas à volta de um cabeçalho de ticket, que
      só a condição (2) apanha (RTMX7), e um `<!--` sem fecho no último bloco (RTMX8)
- [x] Cada caso foi verificado por mutação (abaixo)

### Entrega

**A invariante.** O candidato do enunciado — «uma cerca ainda aberta no fim de um bloco de ticket,
ou a atravessar um cabeçalho `## AOS-`, é erro» — ficou, com uma precisão e um acrescento.

A precisão: o fim de um bloco é calculado sobre o texto **mascarado**, em que os `#` dentro de cercas
já não contam; uma cerca nunca está, portanto, aberta no fim do bloco que os leitores recortam — o
recorte estende-se até ela fechar. «Aberta no fim do bloco» só é observável ao nível do ficheiro, e
desdobra-se nas condições (1) e (2): sem fecho, a cerca corre até ao fim do ficheiro; com fecho
depois do cabeçalho seguinte, os leitores acham esse cabeçalho no texto **cru** (é um ticket novo)
e não o acham no mascarado (não é fronteira do anterior) — a mesma linha lida de duas maneiras.
Verificar uma vez por ficheiro, e não por bloco, é o que torna as duas condições baratas e
independentes do terminador, que continua duplicado entre os leitores (resíduo 2 do AOS-318).

O acrescento é a condição (3), e é a que o candidato não apanhava: uma linha solta de três crases
seguida, **no mesmo bloco**, de uma cerca legítima de bash emparelha com o fecho dessa cerca. Fica
fechada, não atravessa cabeçalho nenhum, e esconde como código o que houver entre as duas — no
cenário medido, um trecho de menção inteiro, cujo ADR volta à §4 em silêncio. O sinal é inequívoco:
a abertura legítima (três crases e info string) fica **dentro** da cerca solta, e uma linha dessas
dentro de uma cerca do mesmo carácter cuja abertura não seja mais comprida só existe quando o autor julgava
estar fora de código. Aninhar cercas de propósito faz-se com uma abertura mais comprida ou do outro
carácter, e isso continua permitido.

**Alternativa adiada, não recusada: comparar o conjunto de pares entre commits.** É o que o
resíduo nomeia, e apanharia qualquer deslocação, não só as de cercas. A primeira versão deste
parágrafo recusava-a por dois motivos, e os dois estavam errados (revisão abaixo): a linha de base
versionada **já existe** — é a §4 de `tecnica/16`, que todo o ticket que cite um ADR já regenera —, e
uma diferença de pares localiza-se sozinha, porque diz que (ticket, ADR) mudou. O que é verdade é
que essa comparação não serve **dentro** do `rtm.sh`, que compara a RTM com o corpus do mesmo commit;
tem de olhar para o commit anterior. A variante barata ficou escrita como **AOS-475**: em CI, com o
histórico completo, extrair os pares no merge-base e no HEAD e recusar uma mudança de pares num
ticket cujo bloco não esteja no diff. Esta guarda fica pela razão que continua certa: localiza a
causa (ficheiro, linha, condição) no próprio commit que a introduz, sem depender de histórico.

**Medido.** Sobre o corpus de hoje: 214 cercas em 25 ficheiros, todas fechadas, nenhuma com um
cabeçalho de ticket, nenhuma com uma abertura do mesmo tipo. A única com cabeçalhos ATX lá dentro
(EPIC-19, um `# comentário` de bash) tem-nos de nível 1 — o caso que a máscara existe para servir.
O conjunto de pares, extraído pelos dois leitores antes e depois: 445 implementados e 78 menções,
idênticos par a par.

**Mutação**, cada uma aplicada, corrida contra §RTMX isolada, revertida e com o hash do ficheiro
confirmado igual ao de antes:

| Mutação | Subtestes vermelhos |
|---|---|
| `rtm-regenerate.py` sem a chamada | RTMX1, RTMX2, RTMX3, RTMX4 |
| `ref-lint.py` sem a chamada | RTMX1, RTMX2, RTMX3, RTMX4 |
| condição (1) desligada | RTMX3 |
| condição (2) desligada | RTMX1, RTMX2 — os leitores continuam vermelhos pela condição (1), mas pelo motivo errado, e o subteste exige o motivo |
| condição (3) desligada | RTMX4 |
| guarda estrita (toda a cerca é erro) | RTMX5, e o corpus real |

Gates: `rtm.sh`, `ref-lint.sh`, `estado-citado.sh` e `lint.sh` verdes; `selftest.sh` completo, a
correr sozinho, verde (130 subtestes, §RTMX1–RTMX5 incluídos). Medidos com o AOS-471 presente: o
número foi reservado para um ramo paralelo, e sem ele o gerador recusa, com razão, a gama
descontínua — a RTM desta entrega é a do backlog depois de os dois se juntarem.

**Resíduos declarados.**

1. **Uma linha solta que emparelhe com a linha seguinte do mesmo carácter, no mesmo ticket,
   quando essa segunda linha não tem info string** (é um fecho válido) — é, para qualquer leitor de
   Markdown, uma cerca legítima, e a guarda não a distingue. Se a segunda tiver info string, a
   condição (3) apanha-a; a primeira versão deste resíduo descrevia o buraco mais largo do que é.
   O mesmo para um `<!--` solto cujo `-->` seguinte esteja no mesmo ticket sem cabeçalho entre os
   dois: é um comentário legítimo.
2. **Uma deslocação de pares que não venha de uma cerca nem de um comentário continua invisível**
   — um cabeçalho de ticket mal escrito, um terminador alterado num só dos leitores. É o que o
   AOS-475 existe para apanhar.
3. **Falsos vermelhos da condição (3), do lado fechado:** uma cerca que mostre como texto uma linha
   começada pela sua própria marca com info string — três crases `text` com «```bash não fecha» lá
   dentro, `~~~` com «~~~ nota», `~~~~` com «~~~~python». Reescreve-se com uma abertura mais
   comprida ou com o outro carácter. E um cabeçalho de ticket dado como exemplo dentro de uma cerca
   (```` ```markdown ```` com `## AOS-123 — Exemplo`) é recusado pela condição (2): de propósito,
   porque os leitores contá-lo-iam como ticket fantasma — a mensagem passou a dizê-lo.

**Revisão adversarial independente (2026-10-01), sobre 26eae41.** Uma falha média reproduzida e
quatro menores nesta metade, fechadas num commit por cima:

- **Um `<!--` solto na prosa escondia uma cerca** (médio, reproduzido). A varredura do ficheiro
  inteiro lê-o como comentário até ao `-->` seguinte. Com a sonda da revisão («Um comentário HTML
  abre-se com <!-- e fecha mais tarde.» na prosa do AOS-417), esse `-->` era o de um marcador de
  menção vinte linhas abaixo, no mesmo ticket; a cerca de YAML entre os dois deixava de ser
  mascarada, um `# comentário` dela terminava o bloco a meio, e o AOS-417 perdia
  o ADR que implementa — com o `rtm --check` (depois de regenerar) e o `ref-lint` verdes. A correcção sugerida,
  aplicar as condições (1) e (2) aos comentários, entrou, mas **não apanhava a sonda**: o comentário
  fecha e não atravessa cabeçalho de ticket nenhum. O que a apanha é a extensão da (2) aos
  comentários: nenhum cabeçalho de nível 1 a 3 lá dentro, porque a máscara de que o terminador
  dos leitores depende só cobre cercas, e um cabeçalho dentro de um comentário é ao mesmo tempo
  fim de bloco para o leitor e texto escondido para o CommonMark. 173 comentários no corpus, zero
  sem fecho, zero a atravessar, zero com cabeçalho. §RTMX6 (a sonda exacta), §RTMX8 (sem fecho).
- A condição (2) não tinha sonda isolada — §RTMX1/2 são apanhadas também pela (1). §RTMX7: duas
  linhas soltas à volta de um cabeçalho de ticket, a segunda um fecho válido; só a (2) a vê.
- A recusa do comparador entre commits estava mal argumentada (acima, reescrita), e o resíduo 1
  descrevia o buraco mais largo do que é (acima, corrigido).
- «212 cercas» passou a 213 com o AOS-471 e a 214 com o AOS-474; os falsos vermelhos da condição (3) e a mensagem
  enganadora da (2) para um exemplo dentro de cerca não estavam declarados (resíduo 3).

Mutação da segunda passagem, cada uma revertida e o hash confirmado:

| Mutação | Subtestes vermelhos |
|---|---|
| `rtm-regenerate.py` ou `ref-lint.py` sem a chamada | RTMX1–RTMX4, RTMX6–RTMX8 |
| condição (1) desligada nas cercas | RTMX3 |
| condição (1) desligada nos comentários | RTMX8 |
| condição (2) desligada | RTMX7 (e RTMX1/2, pelo motivo errado) |
| condição (3) desligada | RTMX4 |
| cabeçalho dentro de comentário aceite | RTMX6 |
| guarda estrita (toda a cerca é erro) | RTMX5, e o corpus real |

Gates da segunda passagem, sobre 45ff27d: `rtm.sh`, `ref-lint.sh`, `estado-citado.sh`, `deferrals.sh` e
`lint.sh` verdes; `selftest.sh` completo, a correr sozinho, verde (151 subtestes, §RTMX1–RTMX8
incluídos). O conjunto de pares sai idêntico: 426 implementados, 111 menções.

### Estado

**FEITO** (2026-10-01). P2.

`scripts/ci/adr_citacoes.py` (`verificar_cercas`), `scripts/ci/rtm-regenerate.py`,
`scripts/ci/ref-lint.py`, `scripts/ci/selftest.sh` (§RTMX1–RTMX8), `tecnica/16_Rastreabilidade_RTM.md`
(§4 regenerada: uma frase no parágrafo «Citar não é alegar»).

---

## AOS-473 — Pares (ticket, ADR) que o próprio corpus declara restrições contavam como entregas, e uma emenda declarada não contava

<!-- rtm: adrs-mencionados -->

### Contexto

Resíduo 1 do AOS-318. Antes do trecho de menção, o marcador de bloco era tudo-ou-nada, e cinco
blocos da EPIC-19 que implementam um ADR próprio tiveram de escolher entre perder essa cobertura e
inventar a das restrições que só citam. Escolheram a segunda, e escreveram-no num comentário no topo
do bloco — 16 pares ao todo:

| Bloco | Implementa | Declara restrições, contadas como entregas |
|---|---|---|
| AOS-417 | ADR-028 | ADR-018, ADR-023, ADR-027 |
| AOS-423 | ADR-030 | ADR-018, ADR-023, ADR-028 |
| AOS-424 | ADR-029 | ADR-001, ADR-007 |
| AOS-427 | ADR-032 | ADR-003, ADR-006, ADR-016, ADR-027 |
| AOS-430 | ADR-031 | ADR-016, ADR-018, ADR-027, ADR-030 |

O AOS-442 regista o caso inverso: emenda o ADR-030 §2.6 (o `aguarda_humano` estaciona) e manteve o
marcador de bloco porque as outras três citações (ADR-005, ADR-018, ADR-031) são só menção — pelo que
a §4 não lhe ligava a emenda, e o resíduo 8 do bloco di-lo. O AOS-318 previu que migrar os seis
blocos deslocava exactamente 17 pares, 16 a sair e 1 a entrar.

### Critérios de Aceitação

- [x] As restrições declaradas nos cinco blocos ficam dentro do trecho de menção
      (`<!-- rtm: menção -->` … `<!-- /rtm: menção -->`) e saem da §4 — **15 dos 16**: o par
      (AOS-423, ADR-028) é restrição **e** entrega, e fica, pela regra deste ticket (abaixo)
- [x] A emenda do AOS-442 ao ADR-030 conta como implementação: o marcador de bloco sai, as três
      menções e o comentário que as declara ficam em trechos, e a frase da emenda («Decisão registada
      como emenda ao ADR-030 §2.6») fica fora
- [x] O conjunto de pares foi extraído antes e depois pelos dois leitores, e a diferença é a que se
      lista em «Entrega»: **20 saem, 1 entra** — não os 17 previstos; o desvio está explicado par a
      par. Os dois leitores continuam a dar o mesmo conjunto, e tudo o que saiu da tabela está agora
      nas menções
- [x] Só se tocou nos trechos: o texto **visível** da EPIC-19, renderizado em CommonMark com tabelas
      antes e depois, é o mesmo palavra a palavra, com uma excepção acrescentada — a nota de fecho no
      resíduo 8 do AOS-442, que de outro modo afirmaria uma coisa que deixou de ser verdade. Na
      revisão juntaram-se notas, invisíveis, dentro dos seis comentários de declaração (abaixo)
- [x] ~~O estado do AOS-380 na EPIC-25 deixa de abrir com «DECIDIDO» e passa a **FEITO**~~ —
      **revertido na revisão**: o AOS-380 tem 0 de 8 caixas marcadas e o critério da Carta
      declaradamente por cumprir; «DECIDIDO» descreve-o melhor, e a mudança estava fora do âmbito
      deste ticket e sem efeito observável no `estado-citado`

### Entrega

**Diferença medida no conjunto de pares (ticket, ADR) da §4**: 445 → 426 implementados, 78 → 97
menções (111 contando as 14 deste próprio bloco, todas sob o marcador de bloco).

| Saem da §4 | Porquê |
|---|---|
| AOS-417 × ADR-018, ADR-023, ADR-027 | declarados restrições no bloco |
| AOS-423 × ADR-018, ADR-023 | declarados restrições no bloco |
| AOS-424 × ADR-001, ADR-007 | declarados restrições no bloco |
| AOS-427 × ADR-003, ADR-006, ADR-016, ADR-027 | declarados restrições no bloco |
| AOS-430 × ADR-016, ADR-018, ADR-027, ADR-030 | declarados restrições no bloco |
| AOS-423 × ADR-029, AOS-424 × ADR-028, AOS-427 × ADR-029, AOS-427 × ADR-031, AOS-430 × ADR-029 | **não previstos** — ver abaixo |

| Entra na §4 | Porquê |
|---|---|
| AOS-442 × ADR-030 | a emenda §2.6, declarada no bloco e no próprio ADR |

**Os cinco pares não previstos.** A declaração das restrições vive num comentário HTML no topo de
cada bloco, e o comentário cita os ADR a que se refere. Um trecho de menção não pode abrir **dentro**
de um comentário — o `<!--` interior é texto, e o comentário fecha no primeiro `-->` —, pelo que a
única forma de tirar da coluna as restrições que o comentário nomeia é envolvê-lo inteiro. Com ele
saem os ADR que o comentário cita **como precedente** («o mesmo preço que o AOS-424 pagou pelo
ADR-029»), e que no bloco não aparecem em mais lado nenhum. São atribuições falsas da mesma espécie —
o ADR de outro ticket, citado como exemplo —, mas não estavam declaradas, e por isso ficam ditas. A
alternativa que as manteria era partir cada comentário em dois à volta da frase das restrições:
mexia na forma de seis comentários para preservar cinco atribuições que nenhum leitor sustentaria, e
não se fez.

**O par que fica: AOS-423 × ADR-028.** O comentário do bloco declara-o restrição, e em parte é — a
secção «O que o ADR-028 §2.2 JÁ decidiu, e que este ticket NÃO reabre». Mas o AOS-423 é também o
consumidor que o ADR-028 §2.2 decidiu e não construiu, toma a decisão de tecto e retenção que o
ADR-028 §4 atribui «ao ticket de implementação», e corrigiu o texto do próprio ADR-028 (a nota
«CORRECÇÃO DE ATRIBUIÇÃO (AOS-423)» no ADR). Restrição **e** entrega: pela regra deste ticket,
fica como estava.

**Como se marcou.** Cada ocorrência das restrições declaradas, em cada um dos cinco blocos, foi lida
no seu contexto e envolvida no trecho mais curto que a contém — o código, ou o código com a sua
pontuação —, nunca uma frase. Os comentários de declaração, com o marcador numa linha sua antes e
depois: colado ao `<!--`, a linha passava a começar por um comentário que fecha nela própria, o bloco
HTML do CommonMark acabava ali, e o resto do comentário passava a aparecer como texto. Pela mesma
razão, duas ocorrências no início de uma linha de prosa abrem o trecho dentro do negrito ou no fim da
linha anterior. Na EPIC-19: 45 trechos de menção — 38 em linha, 6 à volta dos comentários de
declaração e 1 a atravessar uma quebra de linha —, o marcador de bloco do AOS-442 retirado e a
nota no resíduo 8 do mesmo bloco; nenhuma outra palavra de prosa mudou.

**Efeito na cobertura**, medido na §4 regenerada contra a de antes deste ticket: o ADR-029 passa
de 4 implementadores a **1** (AOS-424) e entra na lista de sub-cobertura (≤ 3); o ADR-031 passa de 2
a **1** (AOS-430), e já lá estava. Os restantes descem sem mudar de lado do limiar: ADR-001 26 → 25,
ADR-003 13 → 12, ADR-006 e ADR-007 19 → 18, ADR-016 16 → 14, ADR-018 17 → 14, ADR-023 12 → 10,
ADR-027 7 → 4, ADR-028 7 → 6; o ADR-030 fica em 4 (sai o AOS-430, entra o AOS-442). Nenhum fica a 0.

Gates: `rtm.sh`, `ref-lint.sh` (35 ADRs com cobertura — nenhum ficou sem implementador),
`estado-citado.sh` e `lint.sh` verdes; `selftest.sh` completo, a correr sozinho, verde (130 subtestes).
Medidos, como os do AOS-472, com o AOS-471 do ramo paralelo presente.

**Resíduos declarados.**

1. **Os comentários de declaração continuam a dizer que as restrições «passam a contar como
   implementados»**, e que «não há forma de separar os dois papéis». Ficaram como estavam — a regra
   era não reescrever prosa —, e lêem-se agora como o registo do porquê, não como o estado.
2. **Pares de menção que o corpus não declara ficam na §4** — por exemplo AOS-430 × ADR-028 («que o
   ADR-028 rejeitou») ou AOS-427 × ADR-028 (só na linha dos documentos de referência). O critério
   deste ticket foi a declaração do próprio bloco; o mecanismo continua opt-in, e o resto espera
   quem releia cada bloco.

**Revisão adversarial independente (2026-10-01), sobre 15516ee.** Os dados confirmados par a par
(20 saem, 1 entra, 426 nos dois leitores); três correcções nesta metade, num commit por cima:

- **O AOS-380 voltou a «DECIDIDO»** (médio): a passagem a FEITO estava fora do âmbito, contra o
  próprio bloco (0 de 8 caixas, o critério da Carta por cumprir) e sem efeito no `estado-citado`.
- **Nove afirmações tinham ficado falsas** (médio): os seis comentários de declaração da EPIC-19
  continuavam a dizer que as restrições «passam a contar como implementados», que «não há forma
  de separar os dois papéis» e, no AOS-442, que «o marcador fica»; e na EPIC-22 o resíduo 1 do
  AOS-318, a linha da tabela 0.2 (AOS-318 «ABERTO») e a do eixo («por abrir»). Cada comentário
  ganhou, antes do seu `-->`, uma nota «Desde AOS-473» com o que é verdade para esse bloco — dentro
  do comentário, porque é aí que a afirmação vive, e dentro do trecho, pelo que não move pares;
  as linhas da EPIC-22 passaram ao estado real (o AOS-318 está FEITO e fundido). O resíduo 1 desta
  entrega deixa, por isso, de valer para os comentários.
- **O efeito na cobertura não estava dito** (menor): acima.

Gates da segunda passagem, sobre 45ff27d: `rtm.sh`, `ref-lint.sh`, `estado-citado.sh`, `deferrals.sh` e
`lint.sh` verdes; `selftest.sh` completo, a correr sozinho, verde (151 subtestes). Os pares não
mudam com as notas: 426 implementados e 111 menções, antes e depois, iguais nos dois leitores; e o
texto visível da EPIC-19 renderizada também não.

### Estado

**FEITO** (2026-10-01). P2.

`specs/EPIC-19_Planeador_Meta_Orquestracao.md` (blocos de AOS-417, AOS-423, AOS-424, AOS-427, AOS-430
e AOS-442), `specs/EPIC-25_Remediacao_Auditoria_GOV_OBS.md` (estado do AOS-380 — revertido na
revisão), `tecnica/16_Rastreabilidade_RTM.md` (§4 regenerada).

---

## AOS-474 — O veredicto do `run.sh` diz «TODOS OS GATES VERDES» com um gate que saltou etapas

<!-- rtm: adrs-mencionados -->
<!-- Este ticket NÃO implementa ADR nenhum: é infraestrutura de CI. -->

| Campo | Valor |
|---|---|
| Epic | EPIC-22 (por proximidade, com o AOS-316 e o AOS-472: o eixo são os gates e o seu veredicto) |
| Fase | Prontidão para utilizadores reais |
| Tipo | fix (CI) |
| Prioridade | P2: falso-verde no veredicto local (`make ci`); a CI não chama o `run.sh` |
| Estimativa | S |
| Dependências | AOS-199 (o registo de etapas saltadas, `gate_skip`), AOS-471 (o salto do gate `nats` sem daemon) |
| Bloqueia | — |
| Responsável sugerido | Arquitecto de Plataforma |
| Documentos de referência | `scripts/ci/run.sh`, `scripts/ci/lib.sh` (`gate_skip`), `scripts/ci/package.sh` (saída `3`), `scripts/ci/selftest.sh` §RUN, `CONTRIBUTING.md` §«Registar não é impedir» |

### Contexto

Achado M3 da revisão adversarial independente do AOS-471 (2026-10-01). É pré-existente e vale
para todos os gates. Reproduzido aqui, com o CLI docker e sem daemon:

```text
$ env -u CI -u GITHUB_ACTIONS bash scripts/ci/run.sh nats
   AOS_SKIPPED_STEP  nats (motivo: daemon docker inacessível (…)) -> POR VERIFICAR: o substrato replicado real NÃO foi exercitado; …

================ RESUMO DOS GATES ================
  PASS  nats           0s
  RESULTADO: TODOS OS GATES VERDES
```

Rc 0. Antes do AOS-471, o mesmo posto dava `FAIL` e «PIPELINE VERMELHO», mas pela razão errada
(`AOS_NATS_URL: unbound variable`). Corrigido o gate, o salto declarado apareceu, e o
agregador transformou-o num verde completo.

O `AGENTS.md` diz que «uma etapa saltada é sempre redeclarada no veredicto». O
`CONTRIBUTING.md` diz que «uma etapa que não corre e não aparece no veredicto é um
falso-verde». O `package.sh` cumpria isto desde o AOS-199, e o `run.sh` nunca o cumpriu.

**A causa** é uma fronteira de processo, a mesma que o `package.sh` já tinha fechado para os
filhos dele. O `gate_skip` regista num array da shell do gate, e o `run.sh` corre cada gate
como processo filho e só lê o código de saída. Um gate que salta sai 0, como deve, e o
`run.sh` não tinha por onde saber que houve salto.

### Objectivo

O veredicto do `run.sh` recolhe e redeclara todas as etapas saltadas, de todos os gates que
correu, e nunca diz «todos verdes» se alguma saltou.

### A decisão de desenho

- **O canal é um ficheiro, e não o stdout.** O `gate_skip` (lib.sh) anexa-se a
  `AOS_RUN_SKIP_LEDGER` quando a variável está definida, e o `run.sh` define-a com um ficheiro
  por gate. Herda-se pelo env, e por isso apanha também os saltos de processos netos (um gate
  que chame outro script). É o molde do `AOS_SKIP_SINK` do `package.sh`, generalizado ao
  `gate_skip`. Ler o `AOS_SKIPPED_STEP` do stdout dependeria de cada gate chamar o
  `gate_skip_report`, e obrigaria a meter um `tee` entre o gate e o terminal.
- **A saída é a do `package.sh`, que já está documentada:** `0` é verde, `1` é vermelho
  (ganha a qualquer salto), e `3` é VERDE PARCIAL.
- **Quem chama o `run.sh`, verificado por pesquisa no repo:** o `Makefile` (`ci`, e o `ci-all`
  por dependência), o `CONTRIBUTING.md` e um relatório em `docs/reports/` que o cita como
  comando. Nenhum job do `.github/workflows/ci.yml` o chama, porque cada job corre o seu gate.
  Nenhum ficheiro faz parse do «TODOS OS GATES VERDES».
- **Consequência aceite:** `make ci` sai em erro num VERDE PARCIAL, e o `make ci-all` não
  chega aos self-tests. Num posto sem docker isso acontece sempre. É o comportamento pedido:
  um verde parcial lido como verde completo é exactamente o defeito. Os self-tests correm à
  parte com `make ci-selftest`. Não se acrescentou escape do tipo `AOS_ALLOW_PARTIAL_DELIVERY`:
  ninguém o pediu, e cada escape é uma porta a guardar.
- **Saltos repetidos contam uma vez.** O `package.sh` reabsorve os saltos do `sbom.sh`, e com o
  env herdado o neto e o filho registam a mesma etapa.
- **O `selftest.sh` apaga o `AOS_RUN_SKIP_LEDGER` herdado.** Os saltos que os subtestes
  provocam de propósito, como os do §NX, não são saltos dessa execução. Medido: sem o
  `unset`, a §NX corrida com o registo definido escreveu nele 3 saltos.

### Critérios de Aceitação

- [x] **O defeito está reproduzido.** — *Ver «Contexto»: rc 0 e «TODOS OS GATES VERDES», a
      2026-10-01, sobre `5ede7af`.*
- [x] **O veredicto final redeclara todas as etapas saltadas**, com o gate, a etapa, o motivo e
      a garantia por verificar, e **não** imprime «TODOS OS GATES VERDES» quando algo saltou.
      — *Medido, `env -u CI -u GITHUB_ACTIONS bash scripts/ci/run.sh nats`: `PARCIAL nats`,
      `AOS_SKIPPED_STEP  [nats] nats (motivo: daemon docker inacessível (…)) -> POR VERIFICAR:
      …`, `RESULTADO: VERDE PARCIAL — nenhum gate falhou, mas 1 etapa(s) NÃO correram`, rc 3.
      Self-tests RUN1 e RUN6.*
- [x] **A saída distingue os três estados**: `0` verde, `1` vermelho (ganha ao salto), `3`
      VERDE PARCIAL. A ausência de saltos é ela própria afirmada (`AOS_SKIPPED_STEPS none`). —
      *RUN1 (3), RUN2 (0 com `none`), RUN3 (1, com o salto ainda redeclarado).*
- [x] **Um salto num processo neto chega ao veredicto.** — *RUN4: o gate chama outro script,
      que salta, e não chama `gate_skip` nenhum.*
- [x] **Saltos de vários gates somam-se, e um repetido conta uma vez.** — *RUN5.*
- [x] **Cada caso do self-test morde.** Cada mutante numa cópia de `scripts/ci` fora do repo,
      só a §RUN corrida, e cada um faz avermelhar pelo menos um caso:

      | Mutante | Casos que avermelham |
      |---|---|
      | `gate_skip` não regista | RUN1, RUN3, RUN4, RUN5, RUN6 |
      | o `run.sh` não passa o registo | RUN1, RUN3, RUN4, RUN5, RUN6 |
      | VERDE PARCIAL sai 0 | RUN1, RUN4, RUN5, RUN6 |
      | VERDE PARCIAL diz «todos verdes» | RUN1, RUN4, RUN5, RUN6 |
      | o salto ganha ao vermelho | RUN3 |
      | sem a redeclaração `AOS_SKIPPED_STEP  [gate]` | RUN1, RUN3, RUN4, RUN5, RUN6 |
      | só a última linha do registo | RUN5 |
      | sem deduplicação | RUN5 |

### Entrega

- `scripts/ci/lib.sh`: o `gate_skip` anexa-se a `AOS_RUN_SKIP_LEDGER` quando está definido.
- `scripts/ci/run.sh`: um registo por gate (`mktemp -d`, limpo no `trap`), o estado `PARCIAL`
  no resumo, a redeclaração no veredicto, e a saída `3`.
- `scripts/ci/selftest.sh` §RUN1–RUN6. RUN1–RUN5 correm uma cópia do `run.sh` e do `lib.sh`
  desta árvore com gates sintéticos. RUN6 corre o `run.sh` e o `nats.sh` reais, com um
  `docker` sem daemon. A suite também apaga o `AOS_RUN_SKIP_LEDGER` herdado.
- `CONTRIBUTING.md`: a tabela de saídas do agregador e o efeito no `make ci`/`ci-all`.

### Fora de âmbito, declarado

- **Um gate que salte sem `gate_skip`** (com um `log_warn` solto, por exemplo) continua
  invisível ao veredicto. Hoje, todos os que saltam usam o `gate_skip`: `nats`,
  `isolation-live`, `package`, `sbom`, `sign` e `verify-attestation` (pesquisa por texto em
  `scripts/ci/`). Não há gate que imponha esta regra.
- **O agregador `gates` do `ci.yml`** lê o `success` de cada job e não o `AOS_SKIPPED_STEP`. Em
  CI, o `nats` já não salta (AOS-471). Os outros que podem saltar (`isolation-live`, `package`, `sbom`, `sign`,
  `verify-attestation`) não estão na lista REQUIRED-CHECKS do `ci.yml`.

### Estado

**FEITO** (2026-10-01). Reproduzido e corrigido nesta máquina, com o self-test §RUN e
verificação de mutação. Um `make ci` completo num posto com docker não foi corrido aqui.

---

## AOS-475 — Nenhum gate compara os pares (ticket, ADR) de um commit com os do merge-base

### Contexto

Resíduo 3 do AOS-318 na sua forma geral, e resíduo 2 do AOS-472. O `rtm.sh` compara a RTM com a
regeneração a partir do corpus **do mesmo commit**: se o corpus é mal lido, as duas concordam no
erro. O AOS-472 fechou as causas conhecidas que vêm de cercas e comentários HTML, e localiza-as no
próprio commit; não fecha as que não vêm daí — um cabeçalho de ticket mal escrito, um terminador
alterado num só dos dois leitores (`fim_do_bloco` continua duplicado, resíduo 2 do AOS-318), uma
mudança na regra de extracção que desloque pares longe do que o autor tocou.

A linha de base para os apanhar **já existe e já é versionada**: é a §4 de
`tecnica/16_Rastreabilidade_RTM.md`, que todo o ticket que cite um ADR regenera. O que falta é
compará-la com a do commit de partida, e uma diferença de pares localiza-se sozinha — diz que
(ticket, ADR) entrou ou saiu.

### Proposta — a variante barata

Em CI, no job `rtm`: extrair o conjunto de pares no merge-base e no HEAD, com os leitores do HEAD,
e **recusar a mudança de um par cujo ticket não tenha o bloco no diff**. Um ticket que o autor
editou pode mudar os seus pares; um que ninguém tocou, não.

Apanharia: um ticket que absorve o seguinte por um terminador perdido (o par muda no ticket
**não** tocado); um terminador divergente entre os leitores; uma mudança à regra de extracção sem
declaração; e as linhas soltas que o AOS-472 declara fora do seu alcance, quando o efeito sai do
ticket editado. **Não** apanharia um ticket que muda os seus próprios pares por acidente — o caso
da revisão do AOS-472, um `<!--` solto na prosa do AOS-417 a tirar-lhe o ADR que ele implementa,
fica todo dentro do bloco editado. Para esse continua a guarda do AOS-472.

### Decisões a tomar primeiro

1. **Histórico em CI.** O job `rtm` faz checkout raso; só o `secrets` tem `fetch-depth: 0`
   (`.github/workflows/ci.yml`). Passar o `rtm` a histórico completo, ou buscar só o merge-base.
2. **Leitores de que commit.** Os do HEAD sobre os dois corpora (mede o efeito do corpus) ou cada
   commit com os seus (mede também o efeito da regra). A primeira isola a pergunta; a segunda
   apanha a mudança de regra, mas pede o escape do ponto 3.
3. **Escape declarado** para um commit que mude de propósito a regra de extracção e desloque pares
   por todo o corpus (foi o caso do AOS-473: 21 pares em seis tickets). Sem ele, o gate vermelha a
   própria correcção; com ele em excesso, volta a ser fail-open.
4. **Sem merge-base** (corrida local, ramo órfão): vermelho em CI, nunca verde por omissão.

### Critérios de Aceitação

- [ ] Um script (Python stdlib, no molde dos gates de `scripts/ci/`) extrai os pares no merge-base
      e no HEAD e lista cada (ticket, ADR) que entrou ou saiu
- [ ] Uma mudança de par num ticket cujo bloco não esteja no diff avermelha o gate, com o ticket, o
      ADR e o sentido na mensagem
- [ ] O escape do ponto 3 está decidido, escrito e provado nos dois sentidos
- [ ] Sem merge-base, o gate fica vermelho em CI
- [ ] `selftest.sh`: uma deslocação num ticket não tocado fica vermelha; uma mudança de pares no
      ticket editado fica verde; uma mudança de regra sem escape fica vermelha

### Estado

**ABERTO** (2026-10-01). P2.

---
