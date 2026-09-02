# EPIC-21 — Remediação dos defeitos da auditoria adversarial de RT/RM

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — Remediação dos **defeitos** (não das dívidas) apurados na auditoria adversarial do Plano de Execução |
| Versão | 1.0 |
| Data | 2026-09-02 |
| Classificação | Documento de Referência — **Proposta** |
| Documento-fonte | `analises/09_Auditoria_RT_RM_Adversarial.md` |
| Documentos relacionados | `docs/governance/REGISTO-Deferimentos.md` (DEF-904..907), ADR-002/003/005/008/010/013/018, `tecnica/08`, `specs/EPIC-02`, `EPIC-07`, `EPIC-08`, `EPIC-09`, `EPIC-12` |
| Âmbito | `packages/kernel/agent-runtime`, `packages/kernel/reference-monitor`, `packages/platform/identity`, `packages/cmd/aos` |

---

## 0. Porque este epic existe, e o que ele NÃO contém

A auditoria de `analises/09` produziu 64 achados, atacou 37 com o ónus da prova invertido e
mediu seis. Sobreviveram **16**. Desses, **quatro** eram limites aceitáveis e foram para o
`REGISTO-Deferimentos.md` como **DEF-904..907**, com eixo e dono.

Os **doze** que restam não são dívida — são **defeitos**. Registá-los como deferimentos teria
sido convertê-los em dívida aceite por decreto, que é a lavagem que o §1 daquele documento
existe para impedir. Este epic é o destino deles.

**A distinção que governa o epic:** um deferimento diz «sabemos, aceitamos, eis o eixo». Um
defeito diz «isto está errado». O caso que decidiu a regra é o AOS-288: o nó anuncia no seu
banner que verifica revogação de tokens e não verifica. Declarar isso como dívida aceite seria
oficializar a afirmação falsa.

### 0.1 Ordem sugerida

| Prioridade | Tickets | Racional |
|---|---|---|
| **P0** | AOS-288, AOS-290 | Segurança e privacidade com propriedade anunciada e não cumprida |
| **P1** | AOS-289, AOS-291, AOS-292 | Corrompem prova, disponibilidade ou o ciclo de controlo humano |
| **P2** | AOS-293, AOS-296, AOS-298, AOS-299 | Wiring incompleto ou garantia por fechar antes de alguém depender dela |
| **P3** | AOS-294, AOS-295, AOS-297 | Documentação divergente e validação de configuração |

### 0.2 Tabela-resumo

| Ticket | Defeito | P |
|---|---|---|
| AOS-288 | A verificação de revogação de tokens NHI nunca corre, e o banner do nó anuncia que corre | P0 |
| AOS-289 | O `admit()` do replay aceita uma captura com menos resultados do que tool calls | P1 |
| AOS-290 | O texto claro retido pelo step-ledger fica fora do alcance do crypto-shredding | P0 |
| AOS-291 | O mutex do disjuntor cobre I/O durável e o `AlertSink`, congelando o aborto gracioso | P1 |
| AOS-292 | `POST /runs/{id}/resume` contorna o canal de steer e não consome a correcção | P1 |
| AOS-293 | A projecção do canal de controlo não é reconstruída no arranque | P2 |
| AOS-294 | A tabela de `neutralizarDelimitadores` contradiz a função que ilustra | P3 |
| AOS-295 | `activity/doc.go` declara o deferimento sem a ressalva de modo | P3 |
| AOS-296 | `engine/` é uma porta sem consumidor, e sustenta os únicos `[x]` do EPIC-02 | P2 |
| AOS-297 | `WithLeaseHeartbeat` aceita um intervalo superior ao TTL sem validar | P3 |
| AOS-298 | Uma divergência de replay por eviction sairia inatribuível | P2 |
| AOS-299 | A AC «escritas no Event Store carregam o fencing token» está por cumprir | P2 |

---

## AOS-288 — Ligar a verificação de revogação de tokens NHI, ou deixar de a anunciar

### Contexto

O passo de revogação do `Verify` vive dentro de `if v.revocations != nil`
(`packages/platform/identity/verifier.go:184-192`). Esse guarda é sempre falso no nó:
`identity.WithRevocations` não tem chamador de produção e `NewRevocations` nunca é sequer
construído — ambos os ramos de composição do verifier (`packages/cmd/aos/bootstrap.go:1380` e
`:1417`) montam `verifierOpts` apenas com `WithVerifierClock`.

**Medido** (auditoria §3.1): o verifier construído por cópia literal do `bootstrap` aceita um
token cujo `jti` está revogado, devolvendo `err=<nil>` e um `Principal` completo e utilizável.
O mesmo token com `WithRevocations` ligado dá `E_TOKEN_REVOKED`. **O mecanismo não está
partido — está por ligar.** Janela de aceitação medida: TTL da classe mais leeway, ≈ 16 min.

O que eleva isto de dívida a defeito é a auto-declaração contrária em dois artefactos
entregues: `packages/cmd/aos/posture_banner.go:210` anuncia «token NHI verificado (EdDSA +
janela + **revogacao** + raiz humana ADR-003)», e `packages/platform/identity/doc.go:25`
afirma que o registo é «consultado por `[Verifier.Verify]`». É a classe de defeito que o
AOS-222 abriu um guard-test para impedir, a repetir-se sem guarda.

### Critérios de Aceitação

- [ ] O nó compõe um `Revocations` real e passa-o ao verifier por `WithRevocations`, nos dois ramos de composição
- [ ] Existe uma via de revogação alcançável (rota ou comando) que grava o `jti` no Event Store, e um teste que prova que o token revogado passa a ser recusado com `ErrTokenRevoked` **pela cadeia do nó**, não por um verifier de teste
- [ ] Sob `AOS_MODE=production` a ausência de registo de revogação **aborta o arranque**, no molde de `ErrProductionNeedsHardenedIdentity` — a propriedade não pode voltar a ficar por ligar em silêncio
- [ ] Um **guard-test de veracidade**, no molde de `aos222_fencing_truthfulness_test.go`, impede o banner de anunciar «revogacao» sem a composição correspondente. Se a decisão for não ligar, é o banner que muda — e o teste falha na direcção oposta
- [ ] `identity/doc.go:25` deixa de afirmar consulta que não acontece, ou passa a ser verdade

### Estado

**POR INICIAR.** P0. Depende de uma decisão do dono: ligar a revogação, ou retirar a alegação
do banner. A terceira via — deixar como está — é a única que este ticket recusa.

---

## AOS-289 — O `admit()` do replay tem de recusar uma captura incompleta

### Contexto

Ao escalar, o loop sai do laço de tool calls tendo capturado `j+1` resultados
(`packages/kernel/agent-runtime/loop.go:493-516`), mas a resposta registada guarda todas as
`M` tool calls. O motor de replay itera sobre as `M`
(`packages/kernel/agent-runtime/replay/engine.go:487-491`) e o dispatcher devolve
`Untrusted(nil), nil, nil` para índices fora de alcance
(`packages/kernel/agent-runtime/replay/replay_source.go:76-85`) — sob um comentário que declara
um invariante **falso**: «o motor garante `idx < len(ToolCalls)`», quando o invariante
necessário é `idx < len(ToolResults)`, que é outro número.

**Medido** (auditoria §3.2): `len(response.ToolCalls)=2`, `len(tool_results)=1`, `Fidelity=1`,
`Divergence=nil`, e um segmento `<tool_result taint=untrusted> corpo=""` fabricado no tail. O
`FinalStateHash` — que a documentação chama a prova de que a retoma produz o mesmo estado —
diverge com e sem o segmento fabricado (`dccc2df5…` vs `1ad45cab…`).

E não fica confinado ao run suspenso: a captura truncada **sobrevive à retoma** por dedup de
`cap-<step_id>`, pelo que o turno escalado deixa de ser o último e passa a alimentar um
`prompt_hash` verificado — medido em `Fidelity=0.5`, `Divergence{Turn=2}`. A retoma **é** o
caso de uso da escalada.

### Critérios de Aceitação

- [ ] `admit()` recusa com `ErrIncompleteCapture` quando `len(Response.ToolCalls) != len(ToolResults)` num turno
- [ ] O comentário de `replay_source.go` deixa de declarar o invariante falso
- [ ] Teste que grava um turno com duas tool calls e uma escalada na primeira, e exige que o replay **recuse** em vez de reportar `Fidelity=1.0`
- [ ] Teste de retoma que prova que a captura truncada já não passa a verificação do turno seguinte
- [ ] Decidido e documentado o que fazer ao turno escalado: capturar os resultados em falta como «não despachados», ou recusar o replay — a recusa é o comportamento mínimo, a captura completa é o desejável

### Estado

**POR INICIAR.** P1. A recusa é uma linha; a captura completa é desenho.

---

## AOS-290 — O texto claro do step-ledger fica fora do alcance do crypto-shredding

### Contexto

O `StepLedger` é composto **uma vez** e partilhado por todos os runs do nó
(`packages/cmd/aos/bootstrap.go`). O WAL é selado por titular, mas o mapa em memória guarda o
**claro** — `packages/kernel/agent-runtime/durable/step_ledger.go:503` di-lo por escrito:
«Guarda o CLARO em memória (o WAL tem o cifrado)».

**Medido** (auditoria §3.2): destruída a KEK do titular, `OpenContent` sobre o blob do WAL
falha com «KEK do titular DESTRUIDA» — o disco está apagado — e `Applied(key)` continua a
devolver o payload em claro. Isolamento do retentor por dump de heap com marcador construído em
runtime: baseline **2** → com o ledger vivo **3** → após o shred **3** → ledger largado **2**.
É o mapa `records` e mais nada.

Segundo eixo, independente: o mapa **nunca é podado**. Crescimento linear e sem patamar até 50
mil passos (259–325 B/passo), e a superfície exportada do ledger são três métodos — `Applied`,
`Apply`, `Rebuild`. Não existe via pública para podar.

O AOS-093 promete apagamento **real** por titular. Alcança o WAL e não alcança a memória.

### Critérios de Aceitação

- [ ] O apagamento por titular alcança as entradas em memória do ledger — a leitura pós-shred deixa de devolver plaintext
- [ ] Existe poda: por TTL, por conclusão de run, ou por tecto de entradas — e um teste que prova que a memória não cresce monotonicamente com Σ(runs × passos)
- [ ] Teste de heap, no molde do da auditoria (marcador construído em runtime, referências largadas, GC forçado), que falha se o plaintext sobreviver ao shred
- [ ] Decidido se o ledger passa a ser por-run ou se a poda é do ledger partilhado — a segunda opção é menos invasiva e fecha os dois eixos
- [ ] `step_ledger.go:503` passa a descrever o comportamento novo

### Estado

**POR INICIAR.** P0 pelo eixo de privacidade (AOS-093), P1 pelo de memória.

---

## AOS-291 — O mutex do disjuntor não pode cobrir I/O durável nem o `AlertSink`

### Contexto

`packages/kernel/agent-runtime/breaker/breaker.go:202-203` faz `Lock`/`defer Unlock` em
`Observe`, e a secção crítica abrange `:251` (transição durável no Event Store — I/O de rede no
substrato replicado) e `:265` (`AlertSink` injectado, arbitrário). O mesmo padrão repete-se em
`manualTransition` (`:297-331`).

**Medido** (auditoria §3.2), com um sink bloqueado 3 s:

```
CONTROLO Snapshot() ocioso (media/10k) = 1.669µs
Snapshot() / Abort() / EscalateToHuman() esperaram = 3.0008192s
```

Três ordens de grandeza. Uma segunda sonda isolou a outra metade — sink inerte, `Append`
atrasado 2 s — e `Snapshot()` esperou 2,0014 s.

A consequência não é a latência: **o momento em que o disjuntor dispara é o momento em que se
quer abortar**, e é o momento em que não se consegue. A via de saída graciosa fica bloqueada
pela mesma coisa que a torna necessária.

### Critérios de Aceitação

- [ ] `Alert` corre fora da secção crítica
- [ ] A transição durável corre fora da secção crítica, ou a secção é dividida de forma a que `Snapshot`/`Abort`/`EscalateToHuman` não esperem por I/O
- [ ] Teste concorrente, com `-race`, que prova que um `AlertSink` bloqueado não atrasa `Abort()` — o molde está na sonda da auditoria
- [ ] O `AlertSink` tem contrato explícito quanto a bloqueio: ou se exige não-bloqueante, ou o disjuntor impõe prazo

### Estado

**POR INICIAR.** P1.

---

## AOS-292 — `POST /runs/{id}/resume` tem de fechar o ciclo do canal de steer

### Contexto

O único sítio que limpa `pauseRequested` e consome a correcção pendente é o ramo
`SignalResume` (`packages/kernel/agent-runtime/control/steer_channel.go:376`), alcançável
apenas por `SteerChannel.Resume`. A rota HTTP não passa por lá: `resume.go:65` →
`service.go:789` → `steer_gates.go:219` transita `paused→running` directamente na máquina de
estados. `grep "Steer" resume.go crash_resume.go` → **0**.

Consequência: a pausa continua «em efeito» — o próprio código o diz em
`control/pause_resume.go:103` — e na fronteira de fim do primeiro turno o `GracefulPause` volta
a ver o pedido pendente e o run re-pausa, com a correcção do operador por consumir.

E o caminho que *fecharia* o ciclo também não está ligado: `ControlSurface` chama
`channel.Resume` em `control-plane/governance/control-surface/surface.go:206`, mas
`NewControlSurface` só é construído em testes. **Existem duas vias de retoma: a que está ligada
não fecha o ciclo, e a que o fecharia não está ligada.**

Nota de âmbito: o canal em si funciona. Este ticket é de integração, não do `control/`.

### Critérios de Aceitação

- [ ] A retoma pelo nó passa pelo `SteerChannel`, limpando `pauseRequested` e consumindo a correcção pendente
- [ ] Teste de aceitação que corre o ciclo completo pelo nó real — `POST /pause` → `POST /steer` → `POST /resume` — e exige `Result.Paused == false` no turno seguinte **e** a correcção materializada no `PromptView`
- [ ] O evento `control.resume` é selado no WORM, a par de `control.pause` e `control.steer` — hoje quem pausou fica no registo e quem retomou não
- [ ] Decidido o destino do `ControlSurface`: compor no nó, ou remover

### Estado

**POR INICIAR.** P1.

---

## AOS-293 — Reconstruir a projecção do canal de controlo no arranque

### Contexto

`SteerChannel.Rebuild` (`packages/kernel/agent-runtime/control/steer_channel.go:429`) não tem
chamador de produção. O log de controlo **é** durável — os eventos `control.pause` e
`control.steer` são escritos —, mas a projecção in-memory não é reconstruída: depois de um
reinício, `c.runs` está vazio e uma correcção emitida antes do crash é descartada em silêncio.

O `steer_channel.go:43-46` promete o contrário: «reconstruível relendo-os por ordem de seq
(`[SteerChannel.Rebuild]`)… o ciclo de controlo sobreviver a crash».

Nota: um achado da auditoria citou este defeito com uma referência **inexistente**
(`control/doc.go:100-107`, num ficheiro de 67 linhas). O facto resistiu à verificação; a
citação não. As âncoras corretas são as acima.

### Critérios de Aceitação

- [ ] O arranque do nó chama `Rebuild` para os runs em curso
- [ ] Teste que emite uma correcção, reinicia o processo, e exige que `PendingCorrection` a devolva
- [ ] Se a decisão for não reconstruir, `steer_channel.go:43-46` deixa de prometer que sobrevive a crash

### Estado

**POR INICIAR.** P2. Interage com AOS-292: numa retoma pós-crash a projecção volta vazia, o
que *acidentalmente* anula o defeito daquele ticket — fechá-los em ordem trocada deixaria o
comportamento inconsistente entre retoma normal e retoma pós-crash.

---

## AOS-294 — A tabela de `neutralizarDelimitadores` contradiz a função que ilustra

### Contexto

`packages/kernel/agent-runtime/prompt.go:388-389` mostra:

```
//	<correction>    ->  \<correction>
//	\<correction>   ->  \<correction>
```

As duas linhas mapeiam entradas **distintas** para a **mesma** saída — isto é, exibem
exactamente a não-injectividade que o parágrafo seguinte diz ter sido eliminada. O código
(`prompt.go:478-483`) prefixa `\` a qualquer linha que comece por `<` **ou** por `\`, pelo que
a segunda entrada produz `\\<correction>`, com duas barras. **O código está correcto e é
injectivo**; a tabela ficou na versão anterior à correcção.

Numa função cuja injectividade é a propriedade de segurança que ela existe para garantir, a
documentação errada é o pior sítio para deixar um resíduo.

### Critérios de Aceitação

- [ ] A tabela reflecte a transformação real, incluindo o duplo escape
- [ ] Teste que fixa `neutralizarDelimitadores("\\<correction>")` → `\\<correction>`, para a tabela não voltar a divergir sem consequência

### Estado

**POR INICIAR.** P3. Documentação; o comportamento não muda.

---

## AOS-295 — `activity/doc.go` declara o deferimento sem a ressalva de modo

### Contexto

`packages/kernel/agent-runtime/activity/doc.go:87-96` afirma de forma **incondicional** que o
loop «medeia hoje cada tool call DIRECTAMENTE… mas ainda NÃO despacha via
`[Dispatcher.Dispatch]`».

É verdade no modo por omissão — `loop.go:223` atribui `directDispatcher{rm}` quando ninguém
injecta outro, e isso é `rm.Mediate` cru — e **falso** num nó com `AOS_DURABLE_EXECUTION=1`,
em que `packages/integration/secured.go:430-482` compõe o `activity.Dispatcher` ledger-backed.

Uma auditoria anterior concluiu, a partir de `loop.go:827`, que os deferimentos DEF-801/805
contavam dívida inexistente. A conclusão estava errada — mas foi esta redacção incondicional
que a tornou plausível.

### Critérios de Aceitação

- [ ] O texto distingue os dois modos e diz qual é o do binário por omissão
- [ ] DEF-801 e DEF-805 continuam correctos após a alteração (o gate `deferrals` não regride)

### Estado

**POR INICIAR.** P3. Documentação.

---

## AOS-296 — `engine/`: consumir a porta ou removê-la

### Contexto

`grep -rn "agent-runtime/engine"` fora do próprio pacote é **vazio**: os únicos importadores são
`engine_contract_test.go` e `fake_engine_test.go`, ambos internos. `loop.go` não importa
`engine`. A produção cabla `activity.Dispatcher`, `durable.EventStoreCheckpointer`,
`durable.Resumer` e `replay.Engine` **individualmente**, contornando a porta que
`engine/engine_adapter.go:33-39` declara ser «a PORTA contra a qual o RT programa».

O que torna isto mais do que código morto: os **únicos** `[x]` do `specs/EPIC-02` (linhas 688,
689, 704) pertencem a AOS-022 e afirmam que o adaptador cumpre o contrato «sem alterações à API
do RT». São verdadeiras e cobertas por um teste de contrato de 581 LOC contra dois backends —
mas nada consome a porta, logo nada poderia ter de mudar.

### Critérios de Aceitação

- [ ] Decidido: ou o RT passa a programar contra `engine.Engine` no caminho de produção, ou o pacote é removido e os `[x]` de AOS-022 são reavaliados
- [ ] Se removido, o teste de contrato é reaproveitado ou explicitamente descartado com razão escrita
- [ ] Se consumido, existe um teste que prova a troca de backend sem alterar a API do RT — que é o que a AC afirma

### Estado

**POR INICIAR.** P2. Não há urgência técnica; há de rastreabilidade.

---

## AOS-297 — `WithLeaseHeartbeat` tem de validar o intervalo contra o TTL

### Contexto

`packages/cmd/aos/service.go:314-323` aceita qualquer `interval > 0` sem o comparar com o TTL do
lease. Só o **default** é derivado (`cfg.ttl / 3`, `:375-377`). Um `WithLeaseHeartbeat(5*time.Minute)`
com `DefaultLeaseTTL = 2*time.Minute` produz perda de posse determinística aos 2 min em **todos**
os runs, e o nó arranca sem aviso.

A causa não é falta de acesso ao TTL — `cfg.ttl` e `cfg.hbInterval` vivem na **mesma struct** e
são lidos lado a lado. É validação em falta, e destoa do fail-closed de cablagem que o próprio
repositório aplica noutro sítio (`breaker/breaker.go:146-159`, `ErrProgressSourceInert`).

Alcance honesto: `WithLeaseHeartbeat` não tem chamador de produção, pelo que o caminho corrente
é sempre o default seguro.

### Critérios de Aceitação

- [ ] Um intervalo `>= TTL` é recusado fail-closed na construção, com erro que nomeia os dois valores
- [ ] Teste que fixa a recusa
- [ ] A mesma validação em `worker.WithHeartbeatInterval`, ou razão escrita para não a ter

### Estado

**POR INICIAR.** P3.

---

## AOS-298 — Uma divergência de replay por eviction sairia inatribuível

### Contexto

`agentruntime.WithWindowFactory` e `WithCompactionTrigger` não têm chamador de produção, e o
adaptador que as serviria (`integration.NewWindowManagerFactory`) também não é composto.

**Medido** (auditoria §4): ligar a porta com o adaptador actual **não** quebra o replay —
`EvictionSink.Persist` chamado 0 vezes, `Fidelity=1`, `prompt_hash` byte-a-byte idênticos ao
baseline sem a porta. Mas isso é propriedade **desta implementação**, não do contrato: o
`windowManagerPort` implementa apenas `Append/Assemble/SystemHash/Signal` e nunca chama
`EvictToTailBudget`.

O contrafactual foi medido: uma `WindowPort` que aplique o orçamento diverge no primeiro turno
após a primeira eviction — `Fidelity=0.5`, `Turn=2`, `Reason="prompt_hash"`. O motor de replay
dobra o tail integralmente a partir da captura e não tem notícia de que segmentos saíram da
vista, pelo que a divergência sai como `prompt_hash` — **inatribuível**.

Achado colateral, também medido: com o sinal de exaustão a disparar desde o primeiro turno, a
ocupação cresceu `169 → 1889` contra `Limit=120` — 15,7× — e o run continuou. A porta expõe
pressão de janela e não a alivia; o gatilho de compactação só enfileira.

### Critérios de Aceitação

- [ ] Existe uma `Reason` própria para divergência por eviction/compactação, ou o `TrajectorySpec` transporta o estado da janela
- [ ] Teste que liga uma `WindowPort` com eviction e exige que a divergência seja **atribuível**
- [ ] Decidido o destino do sinal de exaustão: um consumidor que alivie a janela, ou a remoção da porta

### Estado

**POR INICIAR.** P2. **Bloqueante para qualquer ticket que ligue a eviction** — fechado depois,
a primeira eviction em produção produz uma divergência que ninguém sabe atribuir.

---

## AOS-299 — Cumprir a AC «escritas no Event Store carregam o fencing token»

### Contexto

`specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md:428` declara a AC e ela está por marcar. O
estado verificado: o único payload que grava o token é o `leaseRecord`
(`durable/lease.go:107`) e o `transitionRecord.TokenValue` (`state/machine.go:149`).
`StepLedger.Apply` (`durable/step_ledger.go:473`), o `EventStoreCheckpointer`
(`durable/checkpoint.go:182`) e o marcador de worker escrevem **sem** token.

O que **está** composto, e a auditoria confirmou contra uma alegação larga demais: o serviço
passa o token real do lease ao claim `ready→running` (`cmd/aos/service.go:744` →
`steer_gates.go:158`). O que falta é o fencing das **escritas** no caminho de `Runtime.Run` — e
isso está declarado em **ADR-018 §5-bis** e vigiado pelo guard-test
`aos222_fencing_truthfulness_test.go`, que se auto-desactiva quando a dívida for paga.

Este ticket existe porque uma dívida declarada com guard-test continua a ser uma **AC aberta**:
o registo diz que se sabe, não que está feito.

### Critérios de Aceitação

- [ ] Decidido o âmbito: fencing de todas as escritas do caminho de run, ou uma lista explícita das que ficam de fora com razão escrita
- [ ] O guard-test do AOS-222 passa a `t.Skip` por si próprio, que é o sinal que ele foi construído para dar
- [ ] A AC de `EPIC-02:428` é marcada, ou emendada para o âmbito que ficar decidido
- [ ] A janela TOCTOU de `durable/fencing.go:102-113` é fechada ou re-declarada como limite aceite — hoje há um teste que **exige** que a escrita obsoleta comite (`fencing_test.go:298-300`)

### Estado

**POR INICIAR.** P2. Subsistema sensível; escopo próprio.

---

## 1. O que este epic não cobre

Os quatro limites aceites — **DEF-904** (`liveness/` com metade do wiring), **DEF-905**
(SAROC-04 sem enforcement no PEP), **DEF-906** (backstop de `paused`/`waiting_on_tool`) e
**DEF-907** (orçamento sem arbitragem entre processos) — estão no
`docs/governance/REGISTO-Deferimentos.md` com eixo e dono, e não são defeitos.

Dez dos dezasseis sobreviventes **não foram executados**: estão escritos em `analises/09` como
afirmações falsificáveis, formuladas para cair com um teste e não com uma discussão. Fechar um
destes tickets sem correr a falsificação correspondente deixa o defeito por provar como
corrigido.
