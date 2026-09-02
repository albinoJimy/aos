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

Fechar os doze produziu **mais cinco** (AOS-300..304, §0.4): propriedades que uma correcção
deixou parcialmente cumpridas. Não vieram da auditoria — vieram da remediação —, e por isso
estão numerados e listados em separado. Registá-los como deferimentos seria o mesmo erro que o
parágrafo acima descreve: nenhum é «sabemos e aceitamos», os três são «isto ainda não é
verdade».

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

| Ticket | Defeito | P | Estado |
|---|---|---|---|
| AOS-288 | A verificação de revogação de tokens NHI nunca corre, e o banner do nó anuncia que corre | P0 | **implementado** (3 ressalvas) |
| AOS-289 | O `admit()` do replay aceita uma captura com menos resultados do que tool calls | P1 | **implementado** |
| AOS-290 | O texto claro retido pelo step-ledger fica fora do alcance do crypto-shredding | P0 | **implementado** (AC2 re-fundamentada) |
| AOS-291 | O mutex do disjuntor cobre I/O durável e o `AlertSink`, congelando o aborto gracioso | P1 | **implementado** `5100a48` (AC2 em parte) |
| AOS-292 | `POST /runs/{id}/resume` contorna o canal de steer e não consome a correcção | P1 | **implementado** `63decf5`+`d169198`+`b1d8322` (AC4 fechada; AC2 escrita e **NÃO CORRIDA** — exige cluster) |
| AOS-293 | A projecção do canal de controlo não é reconstruída no arranque | P2 | **implementado** `1f019ec` |
| AOS-294 | A tabela de `neutralizarDelimitadores` contradiz a função que ilustra | P3 | **implementado** `76d3692` |
| AOS-295 | `activity/doc.go` declara o deferimento sem a ressalva de modo | P3 | **implementado** `4bbd367` |
| AOS-296 | `engine/` é uma porta sem consumidor, e sustenta os únicos `[x]` do EPIC-02 | P2 | **removido** |
| AOS-297 | `WithLeaseHeartbeat` aceita um intervalo superior ao TTL sem validar | P3 | **implementado** `db215f5` |
| AOS-298 | Uma divergência de replay por eviction sairia inatribuível | P2 | **fechado — porta removida** |
| AOS-299 | A AC «escritas no Event Store carregam o fencing token» está por cumprir | P2 | por iniciar |

E os cinco residuais de §0.4, que a remediação produziu:

| Ticket | Defeito | P | Estado |
|---|---|---|---|
| AOS-300 | A revogação de NHI não sobrevive a um restart em produção | P1 | **implementado** `148c7c5` |
| AOS-301 | A `state.Machine` persiste com o seu mutex detido | P2 | **implementado** `c69098f` |
| AOS-302 | A poda do step-ledger não obriga a substrato durável em produção | P3 | **fechado sem código** — premissa falsificada |
| AOS-303 | O payload do contrato de controlo ficou sem consumidor | P3 | por iniciar |
| AOS-304 | A retoma não é selada na hash-chain, e a pausa é | P2 | **implementado** `43845a0` |

**Onze fechados, seis abertos.** Dos seis, quatro — AOS-289, AOS-296, AOS-298, AOS-299 —
esperam uma decisão do dono que a própria AC nomeia («decidido: … ou …»); AOS-303 é da mesma
família; e a AC2 de AOS-292 espera ambiente de cluster. Nenhum espera trabalho que não esteja
identificado.

### 0.3 As citações deste epic foram verificadas contra o código

Ao entrar na implementação, cada `file:line` citado foi aberto. **Oito** afirmações não
resistiram e estão corrigidas no corpo, marcadas onde ocorrem — as duas últimas apareceram na
preparação de AOS-299, muito depois de este parágrafo dizer «seis»:

| Ticket | O que estava escrito | O que é |
|---|---|---|
| AOS-288 | ramos do verifier em `bootstrap.go:1380` e `:1417` | `:1374` e `:1410` — e há um **terceiro** sítio, `integration/secured.go:289` |
| AOS-289 | iteração do motor em `replay/engine.go:487-491` | `:508`; o fallback do dispatcher é `replay_source.go:82-84` e o comentário falso `:78-79` |
| AOS-292 | AC pede selar `control.resume` «a par de pause e steer» | o selo **já existe** e é o mesmo `appendControl`; falta um chamador de produção |
| AOS-296 | «os **únicos** `[x]` (688, 689, 704)» | são **quatro**: 688, 689, 704 e 707 |
| AOS-299 | «o marcador de worker escreve **sem** token» | escreve **com** token (`worker.go:469`); o facto é que `worker.NewWorker` não é composto no nó — o que ALARGA o âmbito da AC2 |
| AOS-299 | `fencing_test.go:298-300` «exige que a escrita **obsoleta** comite» | exige o caso-fronteira token-**igual**; o estritamente inferior é rejeitado em `:308-311` |
| AOS-299 | as escritas do ledger no Event Store, sem fencing token, em `durable/step_ledger.go:473` | `:483` — a linha 473 é o meio do literal de `persistRec` |
| AOS-299 | o token chega ao claim em `cmd/aos/service.go:744` | `:744` é um `defer func()` de recover de panic; o token entra em `:829` e o claim é em `:863` |

Duas afirmações foram VERIFICADAS e estão CORRECTAS, contra a suspeita inicial — e ficam
registadas, porque uma correcção que não era precisa é informação tão útil como as que eram:

1. em AOS-289, o epic diz «tendo capturado `j+1` resultados» e a captura corre mesmo
   (`loop.go:505`) — o texto não induz em erro, apenas não nomeia a assimetria, que ficou agora
   explícita;
2. suspeitou-se que a AC de guard-test de veracidade que fala de «revogacao» tivesse sido
   copiada para AOS-299 por engano. **Não foi.** As quatro ACs de AOS-299 não mencionam banner
   nem revogação; o texto pertence à AC3 de AOS-288, que tomou o
   `aos222_fencing_truthfulness_test.go` — o teste de *fencing* — como **molde** da sua própria
   guarda de banner. A suspeita nasceu de uma extracção mal ancorada, não do documento.

Nada disto invalida nenhum dos doze defeitos: os factos mediram-se e sobreviveram. O que não
sobreviveu foram oito âncoras — o que é, em si, o argumento do §1 deste epic aplicado a ele
próprio.

---

## AOS-288 — Ligar a verificação de revogação de tokens NHI, ou deixar de a anunciar

### Contexto

O passo de revogação do `Verify` vive dentro de `if v.revocations != nil`
(`packages/platform/identity/verifier.go:184-192`). Esse guarda é sempre falso no nó:
`identity.WithRevocations` não tem chamador de produção e `NewRevocations` nunca é sequer
construído — ambos os ramos de composição do verifier (`packages/cmd/aos/bootstrap.go:1374`,
ramo *hardened*, e `:1410`, ramo de referência) montam `verifierOpts` apenas com
`WithVerifierClock` (`:1362-1365`).

E são **três**, não dois. Fora de `cmd/aos` há um terceiro sítio que constrói um verifier:
`packages/integration/secured.go:289` — `identity.NewVerifier()` sem trust anchors, o fallback
fail-closed que nega toda a NHI. Ligar a revogação nos dois ramos do `bootstrap` e esquecer
este deixaria a propriedade por cumprir precisamente no caminho que já é o mais restritivo, e
a AC abaixo diz «nos dois ramos» porque foi escrita antes de este ser contado.

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

- [ ] O nó compõe um `Revocations` real e passa-o ao verifier por `WithRevocations`, nos **três** sítios de composição: os dois ramos de `bootstrap.go` (`:1374`, `:1410`) e o fallback de `integration/secured.go:289`
- [ ] Existe uma via de revogação alcançável (rota ou comando) que grava o `jti` no Event Store, e um teste que prova que o token revogado passa a ser recusado com `ErrTokenRevoked` **pela cadeia do nó**, não por um verifier de teste
- [ ] Sob `AOS_MODE=production` a ausência de registo de revogação **aborta o arranque**, no molde de `ErrProductionNeedsHardenedIdentity` — a propriedade não pode voltar a ficar por ligar em silêncio
- [ ] Um **guard-test de veracidade**, no molde de `aos222_fencing_truthfulness_test.go`, impede o banner de anunciar «revogacao» sem a composição correspondente. Se a decisão for não ligar, é o banner que muda — e o teste falha na direcção oposta
- [ ] `identity/doc.go:25` deixa de afirmar consulta que não acontece, ou passa a ser verdade

### Estado

**IMPLEMENTADO**; critérios por fechar formalmente. P0. Decisão do dono: **ligar a revogação**,
com a via a ser uma **rota HTTP do plano de controlo**. As âncoras do Contexto acima são as do
código ANTES da correcção.

O que ficou: `Revocations` composto no `bootstrap` (um ponto, `verifierOpts`, que cobre os dois
ramos); `POST /nhi/revoke` no `planoControlo`, com assinatura ed25519 produzida fora do nó por
`aos-issuer revoke-sign` e selo na hash-chain; a frase do banner passou a ser DERIVADA do estado
composto; e um teste que prova `ErrTokenRevoked` pela cadeia do NÓ, falsificado contra a
composição removida — reproduz `Verify = <nil>`, a medição da auditoria.

**ACHADO NOVO, e é o que o epic não previa:** o `Revocations` **não tinha rebuild**. A projecção
é um mapa em memória; o evento `identity.nhi.revoked` ficava durável e nada o relia, pelo que um
restart ressuscitava todos os tokens revogados por expirar, em silêncio. «O mecanismo não está
partido — está por ligar» era optimista: estava também a esquecer. Acrescentou-se
`Revocations.Rebuild`, fail-closed, chamado no arranque (falha ⇒ o nó não arranca).

**TRÊS RESSALVAS por endereçar:**

1. **AC1, terceiro sítio:** `integration/secured.go` NÃO recebe `WithRevocations`, com razão
   escrita no local — é o fallback sem trust anchors, que nega toda a NHI antes de a revogação
   decidir; compor lá um registo próprio criaria um SEGUNDO conjunto desligado do que a rota
   alimenta.
2. **AC3 (abort em produção):** cumprida no sentido «registo inutilizável ⇒ o arranque aborta»,
   e em TODOS os modos, não só em produção. O que NÃO fica fechado é a durabilidade: sobre o
   substrato de referência in-memory o stream morre com o processo, e a revogação dura até ao
   restart. Uma guarda `AOS_MODE=production ⇒ substrato durável` foi implementada, medida a
   partir **seis** testes de produção sem relação com revogação, e revertida — é um requisito
   mais forte do que a AC pede e merece decisão própria.
3. **Reversão em falha de escrita:** `Revoke` mutava o conjunto em memória antes do `Append` e
   não desfazia em caso de erro, contra o idioma que o contrato do Event Store nomeia. Corrigido
   aqui (fora das ACs, dentro do âmbito do epic).
do banner. A terceira via — deixar como está — é a única que este ticket recusa.

---

## AOS-289 — O `admit()` do replay tem de recusar uma captura incompleta

### Contexto

Ao escalar, o loop sai do laço de tool calls tendo capturado `j+1` resultados
(`packages/kernel/agent-runtime/loop.go:493-517`; a captura CORRE — `captureTurn()` em `:505`,
antes do `return` em `:516`), mas a resposta registada guarda todas as `M` tool calls. O defeito
é a ASSIMETRIA: `turnCaptured` (`:448`, acumulado em `:480`) leva os índices `0..j`, e
`Response: resp` (`:460`) leva a resposta inteira. O motor de replay itera sobre as `M`
(`packages/kernel/agent-runtime/replay/engine.go:508`) e o dispatcher devolve
`Untrusted(nil), nil, nil` para índices fora de alcance
(`packages/kernel/agent-runtime/replay/replay_source.go:82-84`) — sob um comentário
(`replay_source.go:78-79`) que declara um invariante **falso**: «o motor garante
`idx < len(ToolCalls)`», quando o invariante necessário é `idx < len(ToolResults)`, que é outro
número — e é sobre `ToolResults` que o dispatcher é construído (`replay_source.go:66-67`).

`admit()` está em `engine.go:374`, chamada em `:406`, e hoje devolve `ErrIncompleteCapture`
(`replay/errors.go:38`) em duas condições apenas: captura do turno ausente e `prompt_hash`
vazio. Não compara os dois comprimentos.

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

**IMPLEMENTADO**; as cinco ACs fechadas. P1.

**DECISÃO DO DONO: RECUSAR NOS DOIS CAMINHOS.** E a descoberta que a motivou muda o ticket: o
`admit()` **não está no caminho da retoma**. Só corre em `ReplayEngine.Replay`; a retoma usa
`Reconstruct` (`cmd/aos/resume.go`), tal como o read-path soberano. Corrigir apenas o `admit`
fecharia o replay de DR e deixaria aberto **exactamente o caso de uso que o defeito descreve** —
a escalada existe para ser retomada. A AC1 e a AC4, lidas à letra, eram incompatíveis.

**A GUARDA VIVE NUM PREDICADO PARTILHADO** (`capturaCompleta`), usado pelos dois pontos de
entrada. No `Reconstruct` corre **depois** de mode 3 e do envelope selado estarem resolvidos —
antes disso mediria uma captura por re-hidratar e recusaria todos os runs cifrados.

**O SENTIDO DA COMPARAÇÃO É DELIBERADO, e tem teste próprio.** Recusa-se `ToolCalls >
ToolResults` — resultados a menos, que é o fabrico. O caso inverso não fabrica nada (o motor
itera sobre as chamadas e nunca alcança o excedente) e não há caminho no loop que o produza;
recusá-lo alargaria a guarda para além do defeito medido.

**PRODUTOR ÚNICO, MEDIDO.** Enumeradas todas as saídas do laço de tool calls: não há `break`
nem `continue`, e das cinco saídas por `return` só o ramo de escalada produz divergência de
comprimentos. A negação do RM e o erro de tool acumulam o resultado **antes** de qualquer ramo;
orçamento, disjuntor e pausa correm na fronteira de fim-de-turno, depois da captura.

**OS TESTES NÃO CONSTROEM A CAPTURA À MÃO.** A truncada é produzida pelo **loop real**, com o RM
a escalar a primeira de duas tool calls. Um literal provaria que a guarda compara dois inteiros;
isto prova que o produtor existe — e **nenhum teste do repositório combinava escalada com
replay**, que é a razão medida por que o defeito passou e por que a recusa custou **zero testes
partidos**.

**FALSIFICADO CAMINHO A CAMINHO, e é essa a evidência que importa:** removida a guarda só do
`admit`, falha **só** o teste do `Replay`; removida só do `Reconstruct`, falha **só** o da
retoma. Mais uma âncora de não-vacuidade — um run intacto continua admissível nos dois caminhos
—, sem a qual uma guarda avariada que recusasse tudo passaria nos outros testes.

**AC2 — o comentário falso corrigido, e não apagado.** `replay_source.go` declarava «o motor
garante `idx < len(ToolCalls)`»: verdadeiro e **irrelevante**, porque a guarda testa
`len(ToolResults)`, que é outro número. Passa a declarar o invariante que agora **vale** —
`ToolCalls <= ToolResults`, imposto no gate — e o fallback fica como defesa em profundidade,
inalcançável pelo `Replay` e fail-safe para qualquer chamador futuro.

**AC5 — decidido: RECUSAR, e a captura completa NÃO foi feita.** A razão é medida e não de
esforço: a captura completa mexe num tipo **público** do kernel (`CapturedToolResult`), e é
aditiva ao WAL só enquanto o `captureSchemaVersion` ficar em `"1.0"` — subi-lo tornaria
**irrecuperável toda a captura já gravada**, porque `engine.go` rejeita por igualdade estrita
sem janela de compatibilidade. Fica como trabalho próprio, com o desenho por decidir: o que o
motor dobra no tail para uma call não despachada tem de ser **nada**, não um segmento vazio.

**RESIDUAL DECLARADO:** os runs escalados **já gravados** continuam truncados e não são
reescritos na retoma, por dedup de `cap-<step_id>`. Passam a ser inadmissíveis — que é o
fail-closed pretendido, e é uma consequência sobre dados históricos, não um efeito lateral.

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

**IMPLEMENTADO**; critérios por fechar formalmente. P0 pelo eixo de privacidade (AOS-093), P1
pelo de memória. **AC4 decidida como o próprio ticket recomenda: poda do ledger PARTILHADO**,
não ledger por-run.

O que ficou: `StepLedger.ForgetSubject` (privacidade) e `ForgetRun` (memória); um índice reverso
titular→chaves, porque o registo em memória **não guarda o titular** — `clearRec` nunca o
preenche e `toClear` limpa-o; o ledger entra na lista de stores do `dsar.Flow` como
`dsar.StepLedgerStore`; e a poda corre no `hostRun`, colada ao `RebuildLedger` que é o seu par.

**A AC2 estava mal colocada, e é o achado do ticket.** Oferecia «TTL, conclusão de run, ou tecto
de entradas» como equivalentes. Não são: o ledger é o registo de IDEMPOTÊNCIA, e
`TestApplyIdempotentReexecution` exige que uma re-execução devolva o payload memorizado —
o ADR-015 fixa «0 efeitos observáveis duplicados». Um TTL ou um tecto LRU escolhem vítimas sem
saber se a entrada é reponível, e trocam memória por duplicação de efeitos externos. A poda por
run é segura por outra razão, mais forte do que «o run terminou»: o nó chama `RebuildLedger` no
início de CADA hospedagem, pelo que a poda é simétrica com a reposição.

**AC3 — o teste de heap não é um dump.** Em Go um dump não é asseverável de forma estável
(`debug.WriteHeapDump` tem formato interno; um perfil `pprof` guarda pilhas, não conteúdo). Fez-
se a mesma pergunta ao GC: um finalizador sobre o array que sustenta o payload corre quando — e
só quando — nada no processo lhe chega. A primeira versão do teste passava pela razão errada
(sem `runtime.KeepAlive`, o GC colhia o LEDGER inteiro e o teste lia isso como «largou o
payload»); apanhou-se na falsificação, onde a premissa falhava sempre aos 0,00 s.

**Achado da revisão de segurança, corrigido:** o `POST /nhi/revoke` de AOS-288 exigia e
verificava o `reason` como parte do payload assinado — e depois descartava-o. O comentário do
ficheiro afirmava que ficava selado. Passa a ir na hash-chain como obrigação
`gov.nhi.revoke.reason`.

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

**IMPLEMENTADO** em `5100a48`; critérios por fechar formalmente. P1. As âncoras do Contexto
acima são as do código ANTES da correcção e já não resolvem.

O que ficou: a transição durável, o `span.End()` — terceira fonte de bloqueio, que a AC não
nomeava — e o `AlertSink` correm fora de `b.mu`; a AC4 foi decidida a favor do contrato
**não-bloqueante**; dois testes com `-race` que não medem tempo, falsificados contra o
`breaker.go` original.

**A AC2 ficou cumprida em parte, e isso é um residual por endereçar.** `Snapshot` com o
wall-clock por omissão lê `m.EnteredAt()`, e `Abort`/`EscalateToHuman` lêem `Current()`: os
três tomam `machine.mu`, que `state.Machine.Transition` (`state/machine.go:412-416`) segura
DURANTE a persistência. Um Append lento continua a prendê-los — por `machine.mu`, não por
`b.mu`. A correcção vive na máquina e não no disjuntor, pelo que fica fora do âmbito deste
ticket e merece o seu.

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
- [ ] O evento `control.resume` passa a ser MESMO escrito. Nota de precisão: o mecanismo de selagem **já existe e é o mesmo** — `SteerChannel.Resume` (`control/pause_resume.go:147`) chama `appendControl` (`pause_resume.go:184`), a mesma função que serve pause e steer (`steer_channel.go:293` → `:330`), e `EventTypeControlResume` está definido (`steer_channel.go:60`). Não há selo a construir: o que falta é um CHAMADOR de produção. Hoje quem pausou fica no registo e quem retomou não, porque a rota HTTP não passa pelo canal — não porque o evento não exista
- [ ] Decidido o destino do `ControlSurface`: compor no nó, ou remover

### Estado

**IMPLEMENTADO** em `63decf5` (canal) e `d169198` (nó); **duas ACs por fechar**. P1. As âncoras
do Contexto acima são as do código ANTES da correcção.

**Decisões do dono:** (a) a retoma de um run PAUSADO exige emissor assinado de operador — quebra
de API deliberada, e fecha a assimetria de o `/pause` exigir operador e o `/resume` não exigir
ninguém; (b) o `ControlSurface` a remover; (c) a bifurcação da correcção resolvida por
**separar limpar-a-pausa de consumir-a-correcção**, em vez de re-injectar ou emendar a AC2.

**A BIFURCAÇÃO QUE O TICKET NÃO PREVIA.** A AC1 manda passar pelo canal e a AC2 exige a
correcção no `PromptView`; encadeadas, eram incompatíveis. O `Resume` consumia a correcção, e o
loop só a lê DEPOIS de a pausa levantar — pelo que a retoma pelo canal deixava o loop sem nada
para injectar. Separar custou três peças: um sinal durável novo
(`control.correction_consumed`), a porta `SteerSource.PendingCorrection` a ganhar `ctx` (a
consumação tem de acontecer no ponto da entrega), e o wiring do nó.

**POR FECHAR:**

1. **AC2 — o teste existe e NÃO FOI CORRIDO.** `packages/cmd/aos/aos292_retoma_live_test.go`,
   build-tag `aoslive`, no molde de `packages/substrate/sandbox/fclive_test.go`. Compila e faz
   skip com a lista das variáveis em falta; **nunca correu** — esta máquina não tem cluster.
   Não é evidência de que a AC passa: é a evidência que falta produzir, e o sítio onde
   produzi-la.

   A razão que aqui estava antes — «exige uma fixture que corra um turno real antes de pausar»
   — estava errada, e a decisão do dono corrigiu-a: uma fixture que finge o ambiente produz um
   teste que passa sem provar nada, que é a falsificação de teste que este epic persegue. Corre
   no cluster, contra um Model Gateway real, onde o run executa turnos e produz capturas.

   **O que o teste prova:** submete um run, espera que corra, pausa e faz steer assinados,
   exige **403 numa retoma sem emissor** (a metade da AC1 que só é observável por HTTP — antes
   de AOS-292 quem detivesse a credencial do run desfazia a pausa de um operador sem assinar
   nada), retoma com emissor assinado, e exige `Paused == false` a seguir. Depois lê o log
   durável do nó e exige os quatro sinais do ciclo.

   **O que NÃO consegue provar, e está declarado no ficheiro:** «a correcção materializada no
   `PromptView`» ao pé da letra. O `PromptView` não é observável por HTTP — o `turn.recorded`
   carrega o `prompt_hash`, não o texto. A prova de materialização usada é o
   `control.correction_consumed`, escrito **no ponto da entrega ao loop**: se existe, a
   correcção chegou ao prompt. É mais preciso do que comparar hashes, e não é a mesma coisa que
   ler o prompt.
2. ~~**AC4 — o `ControlSurface` não foi removido.**~~ **FECHADA.** Removido: `surface.go`,
   `span.go` e os três testes que só o exerciam (`span_test.go`, `graceful_pause_test.go`,
   `outofband_test.go`), mais os erros `ErrNilChannel`/`ErrNilBinding` e três testes em
   `extras_test.go`. A estimativa que aqui estava — «~1100 linhas de testes» — era alta: são
   **697**, e nenhuma prova algo que sobreviva à remoção. `StateProjector` (`reflection.go`,
   `cmd/aos-demo`), `ChannelID` (`surface-adapter`) e `ControlSchemaVersion` (`approval-card`)
   ficam, porque têm consumidor real. **Residual aberto em AOS-303**, e não fechado aqui em
   silêncio.

**TORNOU O AOS-293 URGENTE, E ERA PIOR DO QUE ISTO.** Com a projecção vazia, o
`SteerChannel.Resume` recusa por não haver pausa pendente, o `retomarPausaPeloCanal` falha e o
`hostRun` aborta fail-closed: um run pausado deixava de se conseguir retomar **de todo** após um
reinício — e não apenas de perder a correcção. O epic avisou que fechá-los em ordem trocada
deixaria os dois caminhos inconsistentes; foi o que aconteceu. **Reposto em `1f019ec`
(AOS-293).**

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

**IMPLEMENTADO** em `1f019ec`; **as três ACs fechadas**, a terceira por não se aplicar. P2. As
âncoras do Contexto acima são as do código ANTES da correcção.

**O DEFEITO ERA MAIOR DO QUE O CONTEXTO DESCREVE, E A METADE NOVA FOI INTRODUZIDA POR
AOS-292.** Não era só uma correcção descartada em silêncio: desde `d169198`, retomar uma pausa
passa por `SteerChannel.Resume`, que recusa quando a projecção não tem pausa pendente — pelo
que com `c.runs` vazio o `hostRun` abortava fail-closed e **um run pausado não se conseguia
retomar de todo** após um reinício. Este ticket repõe as duas coisas.

**AC1 — POR HOSPEDAGEM, NÃO NO ARRANQUE**, apesar da letra da AC, e a evidência é que o literal
não resolveria o caso:

1. a varredura de arranque **salta deliberadamente** os runs `paused` — `crash_resume.go` trata
   só `state.Running` —, pelo que um rebuild no arranque não tocaria no run que interessa;
2. o padrão dominante do repositório é por hospedagem: `RebuildLedger`, `state.Machine` no
   `Open`, e o `RunToolSets.Rebuild`, que foi fechado exactamente assim depois de ter estado
   sem chamador;
3. um rebuild global exigiria reler todos os streams por inteiro duas vezes e não cobriria os
   runs hospedados depois.

A chamada fica **antes** do `resumeIfWaiting` — depois, a retoma pelo canal já recusou.

**AC2 — TRÊS TESTES, E A SEPARAÇÃO É O PONTO.** Dois provam o *mecanismo* com um reinício
genuíno (novo `Bootstrap` sobre o mesmo WAL em disco, projecção realmente vazia): um recupera a
correcção, o outro prova que uma correcção **já entregue** não ressuscita — a de-duplicação
durável de AOS-292 a atravessar um restart, que o `applied` in-process do `LoopSteer` não podia
dar. O terceiro prova a *cablagem*, hospedando um run pelo caminho real do serviço.
Falsificados: removida a linha do `hostRun`, **só o de cablagem falha** — que é a distinção que
faltava, porque o mecanismo já tinha testes quando o defeito existia.

**AC3 — NÃO SE APLICA.** É condicional à decisão de *não* reconstruir; reconstruiu-se, e o
`steer_channel.go:43-46` passa a dizer a verdade em vez de deixar de a prometer.

**UMA CORRIDA QUE ESTE TICKET TORNOU ALCANÇÁVEL, E FECHOU.** O `Rebuild` lê o stream **fora** do
lock — de propósito, porque prender um mutex durante I/O é o defeito que AOS-291 removeu do
disjuntor — e **substituía** a projecção. Um sinal aceite entre a leitura e a instalação ficava
de fora: evento durável, memória atrasada, `nControls` desalinhado, e o `appendControl` seguinte
a reutilizar um `ctrl-N` já usado ⇒ `ErrControlLogDivergence` a derrubar um sinal legítimo. A
janela existia e era inalcançável sem chamador. Fechada com instalação **condicional** — se a
memória está à frente do que a leitura alcançou, não se substitui —, sem segurar o lock durante
o I/O. Com teste próprio, também falsificado.

**CUSTO DECLARADO:** o `Rebuild` relê o stream **inteiro** e filtra os quatro tipos `control.*`
em memória. Com o `RebuildLedger` logo acima, são duas passagens por hospedagem, e o custo
cresce com a idade do run. Fica escrito no local, para quem investigar uma retoma lenta.

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

**IMPLEMENTADO** em `76d3692`; critérios por fechar formalmente. P3. Documentação; o
comportamento não muda. A tabela passou a mostrar o duplo escape, e
`tabela_de_neutralizacao_test.go` fixa-a linha a linha, mais a injectividade e o alcance da
regra. Falsificado: removido o escape do `\`, o teste de tabela e o de injectividade ficam
vermelhos, e o segundo nomeia a colisão.

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

**IMPLEMENTADO** em `4bbd367`; critérios por fechar formalmente. P3. Documentação. O texto
distingue os dois modos e nomeia o do binário por omissão; o MARCADOR de deferimento ficou, sem
o qual o gate `deferrals` acusaria DEF-801/DEF-805 como obsoletos ao corrigir-se a redacção.
AC2 **verificada**: `bash scripts/ci/deferrals.sh` verde após a alteração — «todos os
deferimentos declarados no código têm entrada no registo com eixo verificável», com o registo
em ABERTO=33 · FECHADO-RESIDUAL=35 · MITIGADO=30.

---

## AOS-296 — `engine/`: consumir a porta ou removê-la

### Contexto

`grep -rn "agent-runtime/engine"` fora do próprio pacote é **vazio**: os únicos importadores são
`engine_contract_test.go` e `fake_engine_test.go`, ambos internos. `loop.go` não importa
`engine`. A produção cabla `activity.Dispatcher`, `durable.EventStoreCheckpointer`,
`durable.Resumer` e `replay.Engine` **individualmente**, contornando a porta que
`engine/engine_adapter.go:33-39` declara ser «a PORTA contra a qual o RT programa».

O que torna isto mais do que código morto: os **únicos** `[x]` do `specs/EPIC-02` — linhas 688,
689, 704 e **707**, quatro e não três — pertencem todos a AOS-022, e os três primeiros afirmam
que o adaptador cumpre o contrato «sem alterações à API do RT». São verdadeiras e cobertas por
um teste de contrato de 581 LOC (`engine/engine_contract_test.go`) contra dois backends — mas
nada consome a porta, logo nada poderia ter de mudar.

### Critérios de Aceitação

- [ ] Decidido: ou o RT passa a programar contra `engine.Engine` no caminho de produção, ou o pacote é removido e os `[x]` de AOS-022 são reavaliados
- [ ] Se removido, o teste de contrato é reaproveitado ou explicitamente descartado com razão escrita
- [ ] Se consumido, existe um teste que prova a troca de backend sem alterar a API do RT — que é o que a AC afirma

### Estado

**REMOVIDO.** As três ACs fechadas. P2.

**DECISÃO DO DONO: REMOVER**, e a medição mostrou que nenhuma das duas vias preservava a
afirmação que o pacote existia para sustentar.

**PORQUE CONSUMIR NÃO ERA WIRING.** Dos cinco métodos da porta, só o `Checkpoint` bate com o
que o RT usa. O `Dispatch` da porta é `(ctx, activity.Activity) (activity.Result, error)`; o do
loop é `(ctx, referencemonitor.Call) (referencemonitor.Decision, error)`. Ligá-los exigiria
mudar a assinatura de `ActivityDispatcher` — **interface pública do kernel** —, o que tornaria
**falso** o próprio critério `EPIC-02:688`, «sem alterações à API do RT». Além disso o default
não-durável (o do binário por omissão) não pode satisfazer a porta, por não ter Event Store; e
dois dos três consumidores de replay do nó chamam `Reconstruct`, que a porta não expõe —
alargá-la contradiria a sua promessa de seguir «exactamente» as APIs de AOS-014/015/016/021.

**O QUE SAIU:** 1113 linhas Go (367 de produção, 746 de teste) e um README. Zero importadores
fora do pacote, nenhum ADR, nenhuma linha da RTM, nenhum gate — verificado, não inferido.

**O QUE SE PERDEU, E ESTÁ DECLARADO:** o teste de contrato tinha a **única prova executável de
backend-swap do repositório** — o mesmo driver contra dois backends com asserções idênticas.
Crash+retoma e divergência de replay continuam cobertos em `durable/`, `replay/`, `harness/`,
`cmd/aos` e `platform/dr`; a **substituibilidade do backend** não. O critério de teste
`EPIC-02:700` passa a dizer isso em vez de ficar silenciosamente vazio.

**AC1 — os quatro `[x]` reavaliados, um a um.** 688, 689 e 704 voltam a `[ ]` com a razão
escrita ao lado; **707 mantém-se**, porque é uma afirmação sobre documentação e a remoção não a
falsifica — e `tecnica/02` §4.4 foi reescrito para registar a porta, a razão da remoção e o que
se perdeu, em vez de a apagar.

**AC2 — o teste de contrato foi DESCARTADO com razão escrita**, e não reaproveitado: prova a
substituibilidade de uma porta que deixou de existir. Reaproveitá-lo exigiria uma porta nova
para ele testar, que é a via que a decisão recusou.

**O que a remoção NÃO desfaz:** o ADR-015 continua ratificado. A reversibilidade é uma
propriedade do desenho — as peças assentam todas num só log — e é isso que distingue o contrato
próprio de um engine externo. O que deixou de existir é a *demonstração em código*, não a
decisão.

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

**IMPLEMENTADO** em `db215f5`; critérios por fechar formalmente. P3. As âncoras de `service.go`
no Contexto acima são anteriores à correcção. A recusa fail-closed vive em `NewNodeService`, e
não no closure da opção, porque as opções são aplicadas por ordem — há um teste que fixa isso.

**AC3 resolvida pela via da «razão escrita»**, que ela própria admitia: a validação NÃO foi
imposta em `worker.WithHeartbeatInterval`. Nenhuma das duas opções tem chamador de produção,
mas do lado do nó a guarda custou zero enquanto aqui parte cinco fixtures em três módulos
(`worker/`, `platform/dr`, `qa/dr-e2e`), todas a passar `time.Hour` sobre um TTL de 30 s com
relógio manual — onde o valor significa «não renovar durante este teste», não um intervalo. A
razão está no doc-comment da opção.

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

**FECHADO — A PORTA DE SINAL FOI REMOVIDA.** As três ACs resolvidas pela decisão. P2.

**DECISÃO DO DONO: REMOVER**, e a medição mostrou que a cadeia não estava «por ligar» — estava
inteira a zero. Não só a porta: o `EvictToTailBudget` que aliviaria a janela, o
`EvictionSink.Persist` que preservaria o despejado e o `CheckpointTrigger.RunCheckpoint` que
drenaria a fila do gatilho, **todos sem chamador de produção ao mesmo tempo**. O sinal
atravessava quatro camadas e terminava num `append` a um slice que ninguém consumia.

**A EVICTION ERA INALCANÇÁVEL PELA PORTA, e é isso que resolve o ticket.** `WindowPort`
declarava quatro métodos — `Append`, `Assemble`, `SystemHash`, `Signal` — e nenhum era eviction.
Mesmo com a fábrica ligada, o loop não tinha por onde a invocar. Logo a divergência inatribuível
que o ticket descreve não era um defeito **presente**: era um defeito **à espera de um
chamador**, e removida a porta deixa de haver quem o chame.

**AC1 — não se acrescenta `Reason` nem se toca no `TrajectorySpec`.** Ambas foram medidas e
ambas eram viáveis: a `Reason` é genuinamente aditiva (é `string` cru, não há `switch` sobre ela
em lado nenhum, não há golden files), e o `TrajectorySpec` não é persistido. O que decidiu foi o
custo real da segunda: os **dados não existem** — não há evento no log que registe que um
segmento saiu da vista, pelo que transportar estado da janela exigiria um **tipo de evento
novo**. Acrescentar vocabulário de divergência para um caminho que ninguém percorre seria
descrever um defeito em vez de o fechar.

**AC2 — o teste não se escreve**, pela mesma razão: exigiria ligar uma `WindowPort` com eviction,
que é precisamente o que a decisão recusa.

**AC3 — o destino do sinal de exaustão é a remoção.** Saem `WindowSignal`, `WindowPort.Signal`,
a `CompactionTrigger`, o `noopCompactionTrigger`, o `WithCompactionTrigger`, a chamada no
`loop.go`, o `windowManagerPort.Signal` e o `CompactionTriggerAdapter` inteiro, com os dois erros
de construção que só ele usava. O binário entregue não muda: nada disto tinha chamador.

**O QUE FICA, E PORQUÊ.** A `WindowFactory` e o resto do `WindowPort` mantêm-se: têm prova de
equivalência **byte-a-byte** com o caminho inline
(`TestWindowManagerFactory_ByteIdenticalToInline`), que é o contrato de D-TAIL — saiu o sinal,
não a posse do tail. E o `working.WindowManager` e o `compression.CheckpointTrigger` ficam
**intactos** em `platform/memory`: são API própria daquele pilar, com testes próprios. Saiu a
ligação ao loop, não a maquinaria. O gate `memory` verde confirma-o.

**RESSALVA SOBRE A MEDIÇÃO DO PRÓPRIO TICKET:** o número «`169 → 1889` contra `Limit=120`» só
existe em prosa — neste epic e na análise 09. **Não há harness committado que o reproduza**, pelo
que não é uma medição verificável no repositório. O *mecanismo* que o produziria está verificado
por leitura: `WindowManager.Append` nunca rejeita nem evicta, e `Signal` escala e devolve sem
agir.

---

## AOS-299 — Cumprir a AC «escritas no Event Store carregam o fencing token»

### Contexto

`specs/EPIC-02_Agent_Runtime_Execucao_Duravel.md:428` declara a AC e ela está por marcar. O
estado verificado: o único payload que grava o token é o `leaseRecord`
(`durable/lease.go:107`) e o `transitionRecord.TokenValue` (`state/machine.go:149`).
`StepLedger.Apply` (o append em `durable/step_ledger.go:483`) e o `EventStoreCheckpointer`
(`durable/checkpoint.go:182`) escrevem **sem** token — ambos sobre um `EventStore` cru, não
sobre um `FencedAppender`.

**Correcção a este ticket:** o marcador de worker **carrega** token. `worker.go:469` escreve por
`w.fenced.Append(ctx, sess.runID, sess.token, …)`, e o doc-comment de `fencedGate` di-lo por
escrito. O facto que sustenta o ticket é outro, e é mais forte: `worker.NewWorker` e
`durable.NewFencedAppender` **não têm chamador em `packages/cmd/aos`** (fora dele, só
`control-plane/runlifecycle/emitters.go:108` e `tenure.go:100`). O caminho que fenceia as
escritas existe e não é composto no nó.

Isso muda o ÂMBITO da AC2 abaixo. A condição de auto-`t.Skip` do guard-test é a presença da
substring `NewFencedAppender` **ou** `worker.NewWorker` nos `.go` não-teste do pacote do nó —
logo o guard-test não se desactiva por se acrescentar token a estas duas escritas: exige
COMPOR o worker ou o appender fenceado em `cmd/aos`. Quem planear este ticket a partir da
redacção anterior subestima-o.

O que **está** composto, e a auditoria confirmou contra uma alegação larga demais: o serviço
passa o token real do lease ao claim `ready→running` (`cmd/aos/service.go:829` → `:863` →
`steer_gates.go:158`). O que falta é o fencing das **escritas** no caminho de `Runtime.Run` — e
isso está declarado em **ADR-018 §5-bis** e vigiado pelo guard-test
`aos222_fencing_truthfulness_test.go`, que se auto-desactiva quando a dívida for paga.

Este ticket existe porque uma dívida declarada com guard-test continua a ser uma **AC aberta**:
o registo diz que se sabe, não que está feito.

### Critérios de Aceitação

- [ ] Decidido o âmbito: fencing de todas as escritas do caminho de run, ou uma lista explícita das que ficam de fora com razão escrita
- [ ] O guard-test do AOS-222 passa a `t.Skip` por si próprio, que é o sinal que ele foi construído para dar
- [ ] A AC de `EPIC-02:428` é marcada, ou emendada para o âmbito que ficar decidido
- [ ] A janela TOCTOU de `durable/fencing.go:102-113` é fechada ou re-declarada como limite aceite. Precisão do caso: `fencing_test.go:298-300` exige que comite a escrita do detentor cujo token era **IGUAL** ao corrente no instante da leitura e foi superado durante o `Append` — o caso-fronteira `==`. Uma escrita de token **estritamente inferior** é rejeitada com `ErrStaleFencingToken`, e o mesmo teste exige-o logo a seguir (`:308-311`). «Escrita obsoleta» é forte demais para o que o teste fixa

### Estado

**POR INICIAR.** P2. Subsistema sensível; escopo próprio.

---

## 0.4 Residuais apurados DURANTE a remediação — AOS-300..304

Os doze acima vieram da auditoria. Estes cinco vieram de fechar os doze: são propriedades que
uma correcção deixou **parcialmente** cumpridas, ou material que uma remoção deixou órfão, e
cada um está declarado no código que o produziu. Registam-se como tickets, e não como
deferimentos, pela mesma regra do §0: nenhum é «sabemos e aceitamos» — são «isto ainda não é
verdade», e dois deles são propriedades que o nó anuncia.

O que os distingue dos doze é a origem, não a natureza. Estão aqui, e não nos epics dos
respectivos eixos, porque o contexto que os torna compreensíveis é a remediação que os
descobriu.

---

## AOS-300 — A revogação de NHI tem de sobreviver a um restart em produção

### Contexto

AOS-288 ligou a revogação e acrescentou `Revocations.Rebuild`, que repovoa a projecção a partir
do stream `identity.nhi.revoked` no arranque. Isso fecha o esquecimento **quando o substrato é
durável**. Não fecha quando não é: sobre o Event Store de referência in-memory (o default, sem
`AOS_EVENTSTORE_PATH` nem `AOS_EVENTSTORE_NATS`) o stream morre com o processo, e um token
revogado volta a ser aceite ao primeiro restart — em silêncio, com um `Principal` completo.

É a mesma forma do defeito que AOS-288 fechou, por outra porta: o banner anuncia «revogacao» —
e agora anuncia-o com verdade, porque o registo ESTÁ composto e consultado —, mas num
deployment não-durável a propriedade dura até ao próximo arranque.

**A guarda foi escrita e revertida, e a razão importa.** Um
`AOS_MODE=production ⇒ AOS_EVENTSTORE_PATH|NATS` na fronteira de ambiente, no molde de
`ErrProductionNeedsHardenedIdentity`, faz o nó recusar arrancar. Mediu-se: quebra **seis**
testes que forçam `AOS_MODE=production` sem substrato durável e que nada têm que ver com
revogação (`aos215_durable_kek_test.go`, `aos247_model_credential_test.go`, `main_test.go`,
entre outros). É um requisito de produção mais forte do que a AC3 de AOS-288 pedia, e por isso
não foi imposto de contrabando dentro daquele ticket.

### Critérios de Aceitação

- [ ] Decidido: produção exige substrato durável (e os seis testes passam a compô-lo), **ou** o banner declara explicitamente que a revogação não sobrevive a restart neste deployment
- [ ] Se a decisão for exigir: fail-closed na fronteira de ambiente, com erro que nomeie a env var em falta, e um teste no molde de `TestRunProductionRequiresHardenedIdentity`
- [ ] Se a decisão for declarar: a linha do banner deriva do estado do substrato, e um guard-test impede que ela volte a afirmar durabilidade que não há
- [ ] A mesma decisão cobre AOS-302 — os dois eixos partilham o predicado

### Estado

**IMPLEMENTADO**; as três ACs aplicáveis fechadas. P1.

**DECISÃO DO DONO: EXIGIR, não degradar** — a mesma que `ErrProductionNeedsDurableApproval` já
tinha registado para o four-eyes. `AOS_MODE=production` passa a exigir `AOS_EVENTSTORE_PATH` ou
`AOS_EVENTSTORE_NATS`, fail-closed com `ErrProductionNeedsDurableSubstrate`.

**INCONDICIONAL, ao contrário das outras duas guardas de durabilidade.**
`ErrDurableExecutionNeedsDurableSubstrate` exige o substrato a quem pede execução durável;
`ErrProductionNeedsDurableApproval` exige-o a quem configura four-eyes. Ambas dependem de uma
opção que o operador ligou. A revogação não é opcional: desde AOS-288 o registo é composto no
verifier **sempre**.

**O CUSTO QUE ESTE TICKET REGISTAVA ESTAVA ERRADO, NOS DOIS SENTIDOS.** Media-se «seis testes»;
são **nove**. E nenhum é conflito de desenho — são guard-tests de recusa que montam o mínimo para
chegar à guarda que testam, e a nova disparava antes. Colocando-a **no fim** do bloco de postura,
depois de identidade, soberania, KEK e four-eyes, caem para **quatro**, e a correcção é uma linha
de `t.Setenv` em **dois** ficheiros (três dos quatro partilham a fixture `aos247ProdEnvBase`).

**A ORDEM PASSOU A SER TESTADA.** Um nó mal configurado deve ouvir primeiro o que é mais
fundamental, e cinco guard-tests já dependiam disso. `TestAOS300_AGuardaVEMDEPOISDasOutrasColunas`
fixa-o, para ninguém a «arrumar» para o início do bloco. Falsificado nas duas direcções: removida
a guarda, a produção arranca (`veio: <nil>`); movida para antes da identidade, o teste de ordem
falha nomeando o erro que passou a ser mascarado.

**UM DOS MEUS PRÓPRIOS TESTES PASSAVA PELA RAZÃO ERRADA**, e foi a asserção forte que o apanhou. O
caso «four-eyes antes do substrato» apontava para um ficheiro de aprovadores **inexistente**: a
config abortava na leitura, sem nunca chegar ao guarda que o caso dizia medir. Enquanto a asserção
era só «não é o erro de AOS-300», passava a verde. Passou a reusar `prodApprovalEnvBase`.

**AC4 — a decisão NÃO cobre AOS-302**, ao contrário do que a AC pedia: os dois não partilham o
predicado. Ver o Estado de AOS-302 — a poda vive **dentro** da execução durável, que já exige
substrato durável; a revogação não.

**Documentação:** `deploy/node/README.md` deixa de dizer que `AOS_MODE=production` torna
obrigatórias «quatro» exigências. São cinco.

---

## AOS-301 — A `state.Machine` persiste com o seu mutex detido

### Contexto

AOS-291 tirou o I/O da secção crítica do disjuntor: a transição durável, o `span.End()` e o
`AlertSink` correm todos fora de `b.mu`. A AC2 pedia mais do que isso — que
`Snapshot`/`Abort`/`EscalateToHuman` **não esperem por I/O** — e essa metade não fica fechada
pelo disjuntor, porque a espera não é dele.

`state.Machine.Transition` (`packages/kernel/agent-runtime/state/machine.go:412-416`) faz
`m.mu.Lock()` com `defer` e persiste DENTRO dessa secção. `Current()` (`:268`) e `EnteredAt()`
(`:285`) tomam o mesmo mutex. Logo: `Abort` e `EscalateToHuman` lêem `Current()`, e `Snapshot`
com o wall-clock por omissão ([`NewMachineWallClock`]) lê `EnteredAt()` — os três esperam por um
Append lento, por `machine.mu` e não por `b.mu`.

Foi medido ao escrever o teste de AOS-291: a primeira versão de
`TestAOS291_AppendBloqueadoNaoPrendeOMutexDoDisjuntor` usava o wall-clock por omissão e falhava.
A versão que ficou injecta uma fonte independente da máquina, precisamente para isolar o que
AOS-291 mudou do que ficou por mudar — e o doc-comment desse teste declara este limite.

### Critérios de Aceitação

- [ ] Decidido o desenho: persistir fora do mutex da máquina (com o CAS a serializar), ou separar a leitura do estado da escrita por dois locks, ou re-declarar como limite aceite com o custo escrito
- [ ] Se corrigido: um teste concorrente com `-race` que prenda o `Append` e exija que `Current()` e `EnteredAt()` COMPLETEM — o molde está em `breaker/aos291_seccao_critica_test.go`
- [ ] A validação de transição (`IsValidTransition` contra o estado corrente) continua a ser atómica face à escrita — é ela que garante a idempotência de que o disjuntor passou a depender em AOS-291
- [ ] O doc-comment do teste de AOS-291 deixa de declarar o limite, ou a declaração é actualizada

### Estado

**IMPLEMENTADO**; as quatro ACs fechadas. P2.

**DESENHO: DOIS LOCKS**, e não «persistir fora do mutex com CAS». `mu` continua detido do
princípio ao fim de cada mutação — é ele que mantém a validação atómica face à escrita (AC3), a
propriedade que AOS-291 assumiu ao largar o lock do disjuntor. Um `estadoMu` novo protege
**apenas** `current`, `enteredAt` e `nStates`, e nunca é detido durante I/O. Ordem de aquisição
sempre `mu` → `estadoMu`; os leitores tomam só o segundo, pelo que não há deadlock possível.

**A SECÇÃO CRÍTICA COBRIA CINCO FONTES DE ESPERA, e o ticket nomeava uma.** Além do Append:
a consulta à `FencingAuthority` (rede), o `span.End()` (`Exporter.Export`, síncrono) e o callback
do `TransitionObserver` — código de quem compõe o nó, exactamente a classe que AOS-291 tirou do
disjuntor. E `Rebuild` fazia `store.Read` sob o mesmo mutex: a quinta.

**O QUE UM LEITOR PASSA A OBSERVAR, e é correcto:** durante a janela de I/O de uma transição, o
estado é o **anterior**. Já era assim — o código só avança o estado in-memory depois do commit
durável —, pelo que o desdobramento não introduz visibilidade nova: troca «o leitor espera pelo
resultado» por «o leitor vê o estado que ainda está em vigor». Os testes afirmam-no
explicitamente, em vez de o deixarem implícito.

**TRÊS TESTES, e o do meio não distingue a correcção — de propósito.**
`UmAppendPresoNaoPrendeCurrentNemEnteredAt` e `UmRebuildLentoNaoPrendeALeitura` ficam vermelhos
com o código anterior; `AValidacaoContinuaATOMICAFaceAEscrita` passa nos **dois**, e é isso que o
torna útil: é a guarda de que o desdobramento não enfraqueceu a AC3. Prova-o com 200 corridas de
`running→{paused, complete}` — os dois pares estão na tabela, o par que sobra nunca está — e exige
exactamente uma aceite e uma recusada.

**AC4 — o limite deixa de ser declarado.** O doc-comment de
`TestAOS291_AppendBloqueadoNaoPrendeOMutexDoDisjuntor` passa a dizer que fechou e onde. A injecção
da fonte de wall-clock **mantém-se**, e não por inércia: aquele teste é sobre `b.mu`, e voltar ao
wall-clock por omissão ataria outra vez o seu veredicto ao lock da máquina — uma regressão em
AOS-301 deve avermelhar o teste de AOS-301, não o de AOS-291.

Validado com `-race` no módulo `agent-runtime` inteiro e no `cmd/aos`; gate `replay` verde
(fidelidade 100%, 0 efeitos duplicados), que é o que exercita a máquina como autoridade de
transição.

---

## AOS-302 — A poda do step-ledger não obriga a substrato durável em produção

### Contexto

AOS-290 pôs o step-ledger a podar por run, e o que torna isso seguro é a simetria: o nó chama
`RebuildLedger` no início de CADA hospedagem, pelo que o que se larga é relido do Event Store se
o run voltar.

A simetria assenta no substrato ser durável. Sobre o Event Store de referência in-memory a
reposição existe na mesma dentro do processo, mas nada disto sobrevive a um restart — e aí a
poda deixa de ser uma cache a encolher e passa a ser perda: um run retomado depois do restart
não encontra as entradas, e a idempotência volta a depender só da dedup do Event Store no commit
e da idempotência downstream (o contrato at-least-once que o `StepLedger` já declara).

Não é um defeito novo — é o alcance honesto da poda, que não estava escrito. E é o MESMO
predicado de AOS-300: «produção exige substrato durável».

### Critérios de Aceitação

- [ ] Decidido em conjunto com AOS-300 (mesmo predicado, mesma env var)
- [ ] O doc-comment de `StepLedger.ForgetRun` declara o alcance sobre substrato não-durável, em vez de descrever só o caso durável
- [ ] Teste que fixe o comportamento no caso não-durável: poda + «restart» + retoma, e o que fica garantido é a dedup do commit, não a memória

### Estado

**FECHADO — A PREMISSA NÃO SOBREVIVEU À VERIFICAÇÃO, e a premissa era minha.** P3. Nenhuma
alteração de código foi precisa, e é esse o resultado.

**O ESTADO QUE O TICKET DESCREVE NÃO É ALCANÇÁVEL PELO CAMINHO DE DEPLOYMENT.** O
`StepLedger` só é composto dentro de `if cfg.DurableExecution` (`bootstrap.go:1291`), e o
`Bootstrap` **recusa** execução durável sem substrato durável — `ErrDurableExecutionNeedsDurableSubstrate`
(`bootstrap.go:993`), fail-closed **sempre**, não só em produção, e imposto nas duas fronteiras
(ambiente e composition-root, com testes próprios em `durable_execution_env_test.go`). Logo: sem
substrato durável não há ledger, e sem ledger não há poda. Não existe o nó que este ticket
descreve.

**O QUE FICA, E É MUITO MAIS ESTREITO:** um *embedder* que injecte `Config.EventStore` in-memory
com `DurableExecution`. A guarda isenta-o **de propósito** — «um EventStore fornecido por config é
do chamador; a sua durabilidade não é atestável aqui» — e o banner **declara-o**
(`bootstrap.go:2230`), nomeando o ledger. É uma fronteira de embedding assumida, não uma
degradação silenciosa.

**AC1 — não se aplica:** não partilha o predicado de AOS-300, ao contrário do que escrevi ao abrir
o ticket. AOS-300 é real porque a revogação é composta **independentemente** da execução durável;
a poda não, porque vive dentro dela.

**AC2 — já estava cumprida** antes de o ticket existir: o doc-comment de `StepLedger.ForgetRun`
declara o alcance sobre substrato não-durável desde AOS-290, na secção «ALCANCE SOBRE SUBSTRATO
NÃO-DURÁVEL».

**AC3 — não se escreve:** o cenário «poda + restart + retoma sobre substrato não-durável» não é
encenável pelo caminho de deployment, e sobre um store injectado in-memory a asserção seria
tautológica (o que não sobrevive a um restart de processo é, por definição, a memória do
processo).

Fica registado como o §1 aplicado a mim: abri este ticket a partir do comportamento do ledger sem
verificar se a configuração que o torna perigoso é sequer construível. Não era.

---

## AOS-303 — O payload do contrato de controlo ficou sem consumidor

### Contexto

A AC4 de AOS-292 removeu a `ControlSurface` — o tradutor que convertia cada `ControlMessage`
validada nas chamadas reais do `control.SteerChannel`. Nunca foi composto em produção: o nó
sempre chamou o canal directamente, e a superfície só se construía em testes.

O tradutor era o **único** consumidor da metade de *payload* do contrato. Depois de sair,
`ControlMessage`, `DecodeMessage`, `NewInterrupt`, `NewSteer`, `NewResume`,
`NewResumeWithCorrection`, `NewStateQuery` e `Kind` não têm consumidor nenhum — nem de produção,
nem de outro pacote, nem sequer interno fora dos testes que os validam a si próprios.

A metade que **tem** consumidor real fica, e é por isso que o pacote não desaparece: `ChannelID`
é usado por `control-plane/governance/surface-adapter` (4 ficheiros), `ControlSchemaVersion` por
`control-plane/governance/approval-card`, e `StateProjector` por `cmd/aos-demo` — este último
rastreado pelos EPIC-13, EPIC-14 e EPIC-15.

**Porque não se resolveu ao remover a superfície:** `ControlMessage` é um deliverable nomeado de
AOS-119/EPIC-12 — um protocolo versionado, com teste de schema próprio (`contract_test.go`) e
uma promessa de estabilidade em SemVer. Alargar uma AC de remoção da *superfície* até apagar o
*protocolo* seria decidir o destino de um deliverable de outro epic em silêncio, que é
exactamente o que o §0 deste epic recusa. E o destino não é óbvio: um protocolo versionado sem
implementação pode ser dívida a limpar **ou** o contrato que um canal futuro (desktop, chatbot)
vai adoptar — a distinção é de produto, não de código.

É o mesmo eixo de AOS-296 (`engine/`: consumir a porta ou removê-la), com a mesma pergunta e
factos diferentes; não é o mesmo ticket porque os deliverables e os epics de origem são outros.

### Critérios de Aceitação

- [ ] Decidido: ou um canal passa a produzir `ControlMessage` no caminho de produção, ou o payload é removido e o que AOS-119/EPIC-12 afirma sobre o protocolo é reavaliado
- [ ] Se removido, `contract_test.go` (156 linhas, schema e fail-closed) é reaproveitado ou descartado com razão escrita
- [ ] `ChannelID`, `ControlSchemaVersion` e `StateProjector` ficam intactos em qualquer dos destinos — têm consumidor
- [ ] O `doc.go` do pacote deixa de declarar o residual e passa a descrever o que o pacote é

### Estado

**POR INICIAR.** P3. Sem urgência técnica — nada quebra por o payload existir. A urgência é de
rastreabilidade, como em AOS-296: um contrato versionado sem produtor não pode ser citado como
evidência de que o protocolo funciona.

---

## AOS-304 — A retoma não é selada na hash-chain, e a pausa é

### Contexto

`sealControlAction` (`packages/cmd/aos/control_seal.go:61`) escreve na partição
`governance.control` do WORM — a cadeia tamper-evidente — uma entrada por acção de governação
exercida. Tem cinco chamadores: `steer` e `pause` (`api.go:1572`, `:1593`), `approve`
(`:1856`, `:1875`), `autonomy` (`autonomy_route.go:127`) e `nhi_revoke`
(`revoke_route.go:110`).

**`handleResume` não é um deles.** Nenhum outro caminho o compensa: os restantes `WORM.Append`
do pacote são de exaustão, legal hold, sweeper de retenção, compensação de saga e soberania —
nenhum sela a retoma.

O efeito: quem lê a hash-chain vê `control:pause` sobre um run e **não vê** quem levantou a
pausa. Um auditor encontra a imposição sem a libertação — e a assinatura do operador que
retomou, que AOS-292 passou a exigir, não fica no registo tamper-evidente onde as outras estão.

**NÃO É A AC3 DE AOS-292, e a distinção importa.** A AC3 pede o evento `control.resume` no LOG
DE CONTROLO, e esse é escrito: `SteerChannel.Resume` (`control/pause_resume.go:183`) chama
`appendControl` audit-first, antes da transição. São dois registos diferentes com propriedades
diferentes — o Event Store dá replay e reconstrução; o WORM dá evidência tamper-evidente para
auditoria. AOS-292 fechou o primeiro; o segundo nunca esteve fechado, e a frase do epic «quem
pausou fica no registo e quem retomou não» continua verdadeira **do WORM**.

Descoberto ao escrever o teste ao vivo da AC2 de AOS-292: a primeira ideia foi usar o WORM como
observável do ciclo, e o `control:resume` não estava lá para ser observado.

### Critérios de Aceitação

- [ ] `handleResume` sela na hash-chain a retoma que SURTIU EFEITO, com a identidade do emissor, pela mesma disciplina dos outros cinco chamadores (selo DEPOIS do efeito, sem devolver erro ao chamador se o WORM falhar)
- [ ] Decidido se a retoma de um `waiting_on_human` — que não tem emissor assinado — também sela, e com que principal; são dois caminhos com autoridades diferentes na mesma rota
- [ ] Teste que exige `control:pause` **e** `control:resume` na partição `governance.control` para o mesmo run, e que falha se só o primeiro existir
- [ ] O passo 4 do `driver.sh` da skill `run-aos` passa a exercer a retoma, para o smoke cobrir o par

### Estado

**IMPLEMENTADO**; **AC1 e AC3 fechadas, AC2 decidida e declarada, AC4 NÃO FEITA com razão
medida.** P2.

**AC1 — O SELO NÃO FICOU NO HANDLER, apesar de a AC dizer `handleResume`.** O `handleResume`
devolve 202 assim que o `NodeService.Resume` re-submete o run; a retoma pelo canal acontece
**depois**, noutra goroutine, dentro do `hostRun`, e pode ainda falhar. Selar no handler gravaria
na cadeia uma retoma que podia não ter acontecido — e uma entrada falsa numa cadeia cuja única
propriedade é a fidedignidade estraga o registo inteiro, não só aquela linha. Isso violaria a
disciplina que o próprio `control_seal.go` declara: «só se selam acções que SURTIRAM EFEITO».
O selo vive em `retomarPausaPeloCanal`, depois de o `SteerChannel.Resume` devolver sem erro.
O registo é construído por `controlSealRecord`, agora partilhado com os outros cinco chamadores:
o selo de uma retoma tem de ser **indistinguível** do de uma pausa, porque é o par que um auditor
compara.

**AC2 — DECIDIDA: a retoma de um `waiting_on_human` NÃO sela**, e a decisão foi tomada sem
consulta porque as duas alternativas são piores por razões que o repositório já escreveu.
Selá-la com principal vazio escreveria «retomado por ninguém» — exactamente o que
`approversObligation` recusa fazer com uma lista de aprovadores vazia. Selá-la com o principal
certo exigiria devolver a identidade resolvida de dentro do `svc.Resume` até à rota, que hoje
não a tem: a credencial só é verificada a jusante. E a autoridade desse caminho **já está na
cadeia**: o aval four-eyes que o destranca é selado como `control:approve`. Fica o registo de que
o acto da retoma em si não aparece — se isso for insuficiente, é trabalho de plumbing próprio.

**AC3 — dois testes**, e o par é o ponto: `TestAOS304_ARetomaFicaNaHashChainComoAPausa` exige as
duas entradas para o mesmo run, porque um teste que exigisse só `control:resume` passaria a verde
num nó onde a pausa deixasse de selar. `TestAOS304_UmaRetomaRECUSADANaoSela` é a face que impede
o selo de virar vector — quem inundasse a rota com retomas sem autoridade inchava a cadeia sem
nunca retomar nada. Falsificado: removida a chamada ao selo, falha com «a cadeia NAO tem
control:resume».

**AC4 — NÃO FEITA, e a razão é medida, não uma estimativa.** O `driver.sh` não consegue exercer
a retoma: o run do smoke **completa** num turno (modelo de referência), e `POST /resume` sobre um
run completo dá 404 porque não está suspenso. Precisaria de um run multi-turno genuinamente
pausável. Faltaria ainda a via de assinatura — o CLI `aos` não tem subcomando `resume` e o
`aos-issuer` não tem `resume-sign`, pelo que o driver não consegue produzir o emissor assinado
que a rota exige desde AOS-292. São duas peças novas, e nenhuma delas é este ticket.

A asserção do par foi para onde pode mesmo correr: `aos292_retoma_live_test.go` (build-tag
`aoslive`), que percorre o ciclo inteiro contra um nó real e verifica `control:pause` +
`control:resume` quando `AOS_LIVE_WORM` estiver definido. **Nunca corrida** — a mesma ressalva da
AC2 de AOS-292.

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
