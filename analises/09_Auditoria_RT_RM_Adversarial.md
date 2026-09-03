# Auditoria adversarial do Plano de Execução — RT + RM

| Campo | Valor |
|---|---|
| Documento | `analises/09_Auditoria_RT_RM_Adversarial.md` |
| Data | 2026-09-02 |
| Estado auditado | HEAD `aa72b27` (`feat(AOS-101)`), branch `feature/AOS-101-retoma-manifesto`, árvore de trabalho limpa |
| Âmbito | **RT** (`packages/kernel/agent-runtime`) e **RM** (`packages/kernel/reference-monitor`), com as fronteiras que lhes tocam |
| Tipo | Auditoria multi-perspectiva → **refutação adversarial** → **medição executada** |
| Auditoria anterior | `analises/08_Relatorio_Auditoria_Multiagente_v4.md` (v4, 2026-07-26) |
| Contratos verificados contra | `_BRIEF.md` (ADR-001/002/003/005/008/010/013/014), `specs/EPIC-02`, `EPIC-07`, `EPIC-08`, `EPIC-09`, `tecnica/08` |

---

## 1. Método, e o que ele custou

Três passagens, cada uma com o ónus da prova invertido em relação à anterior.

**Passagem 1 — cinco lentes independentes.** Uma por dimensão da vista de qualidade do
`_BRIEF` §5, sobre RT e RM: arquitectura/manutenção, segurança, escalabilidade/resiliência,
observabilidade/replay, governação/UX-DX. Instrução comum: evidência em `ficheiro:linha` para
tudo; «não existe» só como **ausência medida** (o grep vazio, mostrado); e verificar o
**código**, não o comentário — este repositório tem comentários longos que descrevem
intenções, e a divergência entre um e outro é ela própria um achado. Resultado: **64 achados**.

**Passagem 2 — quatro refutadores.** Regra única: *na dúvida, o achado cai*. Nem confirmado
nem refutado conta como queda. Cada um teve de refazer os greps por si, procurar a camada
compensatória noutro sítio, e distinguir **dívida declarada** (com `DEF-NNN` rastreado) de
**divergência não registada**. Dos 64, **37** foram atacados — os CRÍTICO/ALTO mais os que
convergiam entre lentes.

**Passagem 3 — medição.** Seis afirmações passaram de argumento a programa executado, em
módulos de sonda fora do repositório (`replace` para os pacotes), sem escrever um único
ficheiro em `packages/`.

### 1.1 O que cada passagem mudou

| | |
|---|---|
| Passagem 1 → 2 | 5 alegações destruídas por contra-exemplo; **13** com facto correcto e consequência inflacionada |
| Passagem 2 → 3 | **Dois veredictos invertidos, em direcções opostas** |

A inversão dupla é o resultado mais útil desta auditoria, e está em §4.

---

## 2. A tese: o registo é o discriminador

Este programa declara a sua dívida melhor do que a maioria. Há um `REGISTO-Deferimentos.md`
com dono e eixo por linha, comentários que nomeiam o cenário de falha por palavras, e pelo
menos um teste — `packages/cmd/aos/aos222_fencing_truthfulness_test.go` — que existe para
**impedir o nó de alegar uma propriedade que não tem**, e que se auto-desactiva no dia em que a
dívida for paga. Até os gates de CI separam «verde» de «dívida reconhecida com dono» na sua
própria saída.

Isso muda o que conta como achado. **Um buraco escrito no registo não é uma descoberta — é a
leitura do registo.** O sinal é a ausência dele. E o pior caso não é o buraco não registado: é
o artefacto entregue a **declarar o contrário**.

---

## 3. Os sobreviventes

Dezasseis achados resistiram às três passagens. Nenhum tinha, à data desta auditoria, entrada
no `REGISTO-Deferimentos.md`.

### 3.1 Medido, e o nó afirma o contrário — o mais grave

**A verificação de revogação de tokens NHI nunca corre.** Uma sonda construiu o `Verifier` por
cópia literal de `packages/cmd/aos/bootstrap.go:1367-1371` e verificou um token cujo `jti`
estava revogado:

```
Revocations.IsRevoked(...) = true          ← o registo SABE que está revogado
A) Verifier como o nó o constrói (SEM WithRevocations):
   Verify(token revogado) -> err=<nil>     ← ACEITE, com Principal completo
B) MESMO token, verifier COM WithRevocations(rev):
   Verify(token revogado) -> err=E_TOKEN_REVOKED
```

O passo de revogação vive dentro de `if v.revocations != nil`
(`packages/platform/identity/verifier.go:184-192`), sempre falso: `WithRevocations` não tem
chamador de produção e `NewRevocations` nunca é sequer construído. Janela medida: TTL da
classe mais leeway, **≈ 16 minutos**.

O que o torna grave não é o facto — é a auto-declaração contrária em dois artefactos
entregues: `packages/cmd/aos/posture_banner.go:210` («token NHI verificado (EdDSA + janela +
**revogacao** + raiz humana ADR-003)») e `packages/platform/identity/doc.go:25`.

**Classificação: DEFEITO A CORRIGIR.** Declarar isto como dívida aceite seria oficializar a
afirmação falsa do banner. É a classe de defeito que o AOS-222 abriu um guard-test para
impedir, a repetir-se noutro sítio sem guarda.

### 3.2 Medido e confirmado

**O crypto-shredding alcança o disco e não a memória.** O `StepLedger` é composto uma vez e
partilhado por todos os runs; o WAL é selado por titular, o mapa em memória guarda o claro.

```
WAL sealed=true subject="nhi:titular:alice"   marcador EM CLARO no WAL = false
apos shred: OpenContent(WAL) -> err=cifra: KEK do titular DESTRUIDA
apos shred: Applied(key) ok=true payload="…MARCADOR-SEGREDO-…-PII…"
```

Isolamento do retentor (marcador construído em runtime, referências largadas, GC forçado):
baseline **2** → com o ledger vivo **3** → após o shred **3** → ledger largado **2**. É o mapa
`records` e mais nada. Crescimento linear e sem patamar até 50 mil passos (259–325 B/passo); a
superfície exportada do ledger são três métodos — `Applied`, `Apply`, `Rebuild` — pelo que não
há como podar nem querendo. Âncora: `packages/kernel/agent-runtime/durable/step_ledger.go:183,503-505`.

**O aborto gracioso congela exactamente quando é preciso.** O lock de `Observe` cobre a
transição durável no Event Store *e* o `AlertSink` injectado
(`packages/kernel/agent-runtime/breaker/breaker.go:202-203,251,265`):

```
CONTROLO Snapshot() ocioso (media/10k) = 1.669µs
sink bloqueado durante                 = 3s
Snapshot() esperou / Abort() / EscalateToHuman() = 3.0008192s
```

Três ordens de grandeza. A consequência não é a latência: o momento em que o disjuntor dispara
é o momento em que se quer abortar, e é o momento em que não se consegue.

**Dois estados sobrevivem a dez anos de relógio.** Com `humanTTL` e `wallClock` ligados e o
relógio injectado avançado 87 600 h:

```
paused            +87600h -> transitou=false
waiting_on_tool   +87600h -> transitou=false
waiting_on_human  +87600h -> estado=killed     (controlo)
running           +87600h -> estado=timed_out  (controlo)
```

A segunda via também está fechada: `tecnica/08:144` designa o disjuntor como a rede de
segurança com um sinal wall-clock **absoluto**; cablado assim (1 h contra limiar de 1 ms), em
`paused` e `waiting_on_tool` dá `Trip=false` e zero alertas. A causa é a guarda de entrada
`breaker.go:213` sobre `liveness.CountsAsActiveWork` (`liveness/waiting_states.go:356`), que só
admite `running`. *Ressalva: isto mede que o **RT** não tem backstop; não foi verificado se o
escalonador ou o `runlifecycle` impõem um tecto acima.*

**Um turno escalado fabrica um resultado que nunca existiu.** Ao escalar, o loop sai do laço
com *um* resultado capturado, mas a resposta registada guarda *duas* tool calls; o motor de
replay itera sobre as duas e o dispatcher devolve vazio, sem erro, para o índice fora de
alcance (`replay/engine.go:487-491`, `replay/replay_source.go:76-85`).

```
CAPTURA turno 1: len(response.ToolCalls)=2   len(tool_results)=1
Fidelity=1   Divergence=nil
  [4] <tool_result taint=untrusted> corpo="" (len=0)      ← fabricado
```

`admit()` (`replay/engine.go:374-385`) não compara os dois comprimentos. **Correcção: uma
linha** — `len(Response.ToolCalls) != len(ToolResults)` ⇒ `ErrIncompleteCapture`.

**A deduplicação existe uma camada acima, e vem desligada por omissão.** `activity/dispatch.go:156`
deriva `durable.IdempotencyKey(act.RunID, act.StepID)` e `:246` chama `d.ledger.Apply(...)`,
que devolve `StatusDuplicate`. Mas essa camada está atrás de `if cfg.Ledger != nil`
(`packages/integration/secured.go:430`), alimentado por `AOS_DURABLE_EXECUTION` — **desligado
por omissão**. No binário entregue sem essa variável, duas `Mediate` com a mesma chave
produzem dois efeitos:

```
numero de execucoes da tool para o MESMO run:step = 2
[sandbox, caminho real]  execucoes = 2 ; instancias distintas = 2
```

O contrato `ToolFunc` não transporta a chave — uma varredura por reflexão da cadeia de
`context.Context`, incluindo campos não-exportados, não encontrou nenhuma.

### 3.3 Confirmado por leitura

- **A rota HTTP `/resume` contorna o canal de steer.** O único sítio que limpa
  `pauseRequested` e consome a correcção é o ramo `SignalResume`
  (`control/steer_channel.go:376`). `POST /runs/{id}/resume` transita `paused→running`
  directamente na máquina (`resume.go:65` → `service.go:789` → `steer_gates.go:219`), sem
  tocar no canal — a pausa continua «em efeito» e o run re-pausa na fronteira do primeiro
  turno. E o caminho que *fecharia* o ciclo também não está ligado: `ControlSurface` chama
  `channel.Resume` em `control-surface/surface.go:206`, mas `NewControlSurface` só é
  construído em testes.
- **`liveness/`: 550 linhas, um consumidor, metade de uma promessa.** Um só importador
  (`breaker/breaker.go:8`), uma só chamada (`CountsAsActiveWork`). O `doc.go:56` exige ao
  consumidor **duas** coisas: `(a)` construir o gate com `NewWaitingGateFrom` e `(b)` chamar
  `CheckDeadlines`. `(b)` está ligado desde AOS-252 (`cmd/aos/deadline_sweeper.go:94`);
  `NewWaitingGateFrom` continua sem chamador de produção.
- **SAROC-04 fica por impor.** A classe de risco *é* imposta pelo overlay de autonomia no PDP,
  que devolve `Escalate` — mas o específico do hook não tem substituto: uma acção *danger* com
  egress e sem destino concreto deveria ser negada fail-closed
  (`reference-monitor/risk_gate.go:323-334`), e o `RiskGate` não é montado no ápice
  (`integration/secured.go:371-374` compõe `NewRiskClassifier(nil)`).
- **Orçamento por árvore sem enforcement cross-process.** `budget/budget.go:33-41` decide sob
  mutex em memória, sem CAS; `budget.Rebuild` sem chamadores. Contraste: só
  `scheduler/admission.go` usa `WithExpectedSeq`.
- **Uma correcção pendente perde-se no crash.** `SteerChannel.Rebuild` sem chamador de
  produção: o log de controlo é durável, a projecção in-memory não é reconstruída no arranque.
- **Três divergências entre o escrito e o código.** A tabela de `prompt.go:388-389` mapeia
  entradas distintas para a mesma saída, exibindo a não-injectividade que o parágrafo seguinte
  diz ter eliminado (o código está correcto e *é* injectivo); `activity/doc.go` afirma de forma
  incondicional que o loop «ainda NÃO despacha via Dispatcher», verdade só no modo por omissão;
  e o critério «escritas no Event Store carregam o fencing token» continua por marcar e por
  cumprir.

---

## 4. O que caiu, e porquê importa

Cinco alegações destruídas. Duas merecem ficar registadas porque o **modo** como caíram ensina
mais do que o facto.

**«Os deferimentos DEF-801/805 contam dívida que não existe».** O agente acusador leu
`loop.go:827` (`rt.dispatcher.Dispatch` como via única) e concluiu adopção — sem ler
`loop.go:223`, onde `rt.dispatcher` recebe `directDispatcher{rm}` quando ninguém injecta outro,
e isso é `rm.Mediate` cru. Como a execução durável é opt-in e vem desligada, **no binário
entregue por omissão a dívida existe**. O próprio DEF-805 já o dizia.

**«Ligar `WithWindowFactory` quebra a fidelidade de replay».** Medido: não quebra. O adaptador
implementa apenas `Append/Assemble/SystemHash/Signal` e nunca chama `EvictToTailBudget`;
`EvictionSink.Persist` chamado 0 vezes, `Fidelity=1`, `prompt_hash` byte-a-byte idênticos ao
baseline. **Mas é propriedade da implementação, não do contrato:** uma `WindowPort` que aplique
o orçamento diverge no primeiro turno após a primeira eviction (`Fidelity=0.5`, `Turn=2`,
`Reason="prompt_hash"`) — e sairia **inatribuível**, porque não existe `Reason` para eviction.
Fica como advertência ao ticket que ligar a eviction.

As outras três: o lint de separação de efeitos **corre** em CI (o grep vazio era artefacto de
procurar chamadas qualificadas a um consumidor interno ao pacote); o exporter de spans de
produção nunca devolve erro e tem contadores, retry e log; e a alegação de que a retoma
desamarra a aprovação humana tem a causa-raiz falsa — `activity/dispatch.go:245` calcula uma
impressão digital canónica da acção que o ledger impõe fail-closed.

---

## 5. Meta-achados

**Uma revisão humana apanhou o que três camadas de agentes não apanharam.** Depois das três
passagens, uma leitura do repositório encontrou cinco erros: um achado material invertido (a
saga — o código faz retry e escalada, e o relatório dizia «aborta ao primeiro erro»), uma
conclusão generalizada de um âmbito estreito (a idempotência), um facto desactualizado
(`CheckDeadlines` já tem executor) e duas refutações mal enquadradas. O padrão comum não é
falta de rigor técnico — cada erro assentava num facto verificável. Falhou o **alcance da
frase construída sobre o facto**.

**A refutação também erra, e só a medição a apanhou.** O rebaixamento do achado do turno
escalado assentava num argumento plausível e verificável por leitura: *o turno escalado é
sempre o último, logo nunca alimenta um `prompt_hash` verificado*. Verdade dentro de um `Run`;
falsa no que interessa, porque a captura truncada sobrevive à retoma por dedup de
`cap-<step_id>` — e a retoma **é** o caso de uso da escalada. Uma segunda opinião céptica não
substitui um programa a correr.

**A gravidade inflaciona-se sozinha.** Treze achados tinham facto correcto e consequência
exagerada; metade caiu quando o refutador foi ler o comentário à volta da linha citada. É o
mesmo padrão que o v4 já registara.

**A convergência entre lentes independentes não é prova.** Três achados apareceram em agentes
que não falaram entre si, o que sugeriu robustez. Dois eram dívida declarada: a convergência
mediu quão **visível** o buraco é, não quão desconhecido.

**Uma citação era inventada.** Um achado citava `control/doc.go:100-107`; o ficheiro tem 67
linhas. O facto por trás era verdadeiro, mas a evidência apontava para texto inexistente.

---

## 6. Classificação e destino

Dos dezasseis sobreviventes, quatro são **limites aceites** e entram no
`REGISTO-Deferimentos.md` como `DOCUMENTAL` ancorados a este ficheiro — DEF-904 a DEF-907. Os
restantes são **defeitos a corrigir** e **não** entram no registo: registá-los seria convertê-los
em dívida aceite por decreto, que é exactamente a lavagem que o §1 daquele documento existe
para impedir.

| Sobrevivente | Destino |
|---|---|
| Revogação de NHI não ligada (com banner a afirmar o contrário) | **Defeito** — ticket |
| Gate de comprimento em `admit()` | **Defeito** — uma linha |
| Tabela de `prompt.go`; ressalva de modo em `activity/doc.go` | **Defeito** — documentação |
| Mutex do disjuntor através de I/O durável e do `AlertSink` | **Defeito** — ticket |
| Texto claro retido no `StepLedger` após crypto-shredding | **Defeito** — ticket |
| `/resume` a contornar o canal de steer | **Defeito** — ticket |
| `SteerChannel.Rebuild` sem chamador | **Defeito** — ticket |
| `liveness/` com metade do wiring (`NewWaitingGateFrom`) | **DEF-904** |
| SAROC-04 sem enforcement no PEP | **DEF-905** |
| Backstop de `paused`/`waiting_on_tool` | **DEF-906** |
| Orçamento por árvore sem enforcement cross-process | **DEF-907** |

Os restantes (AC do fencing de escritas, `engine/` sem consumidor, `WithLeaseHeartbeat` sem
validação, advertência do `WithWindowFactory`) ficam **descritos aqui** sem entrada própria: o
primeiro já está coberto por ADR-018 §5-bis, e os outros três são observações cuja consequência
não foi demonstrada.

---

## 7. Limites desta auditoria

- **Dez dos dezasseis não foram executados.** Estão escritos como afirmações falsificáveis,
  formuladas para cair com um teste e não com uma discussão.
- **Nenhuma sonda correu dentro do repositório.** Todas viveram em módulos externos com
  `replace`; o que exigiria um ficheiro dentro de um pacote (símbolo não-exportado) ficou por
  correr e está identificado como tal.
- **O âmbito foi RT e RM.** O PDP, o orquestrador e o model-gateway só foram tocados nas
  fronteiras. Um bypass alojado numa política do PDP ou na delegação de sub-agentes ficaria
  fora do que se cobriu.
