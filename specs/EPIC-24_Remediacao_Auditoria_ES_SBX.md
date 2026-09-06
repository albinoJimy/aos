# EPIC-24 — Remediação dos defeitos da auditoria adversarial ES/SBX

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Epic — Remediação dos defeitos apurados na auditoria adversarial do Substrato |
| Versão | 1.1 |
| Data | 2026-09-06 |
| Classificação | Documento de Referência — **Executado** (todos os 15 tickets implementados; ver §0.7) |
| Documento-fonte | `analises/12_Auditoria_ES_SBX_Adversarial.md` (§2, §3, §5, §6) |
| Documentos relacionados | `docs/governance/REGISTO-Deferimentos.md`, `tecnica/07`, `tecnica/14`, `tecnica/17`, `specs/EPIC-07`, `EPIC-10`, ADR-004, ADR-007, ADR-017, `docs/reports/medicao-jetstream-arbitragem-2026-08-31.md` |
| Âmbito | `packages/substrate/eventstore` (incl. `jetstream/`), `packages/substrate/sandbox`, `packages/kernel/reference-monitor`, `packages/cmd/aos`, `tecnica/`, `deploy/`, `scripts/ci/` |

---

## 0. Porque este epic existe

`analises/12` produziu 42 hipóteses-defeito, atacou-as com o ónus da prova invertido em seis
refutações independentes — uma delas **experimental**, que correu os cenários numa cópia isolada do
módulo em vez de os argumentar — e mediu o resultado no nó real. **Sobreviveram 12**; doze caíram
por evidência falsa, onze eram deferimentos já registados, e oito eram fragilidade e não violação de
requisito. Mais **dois achados nasceram da própria refutação**, nenhum deles visto por qualquer lente.

A assimetria do saldo é o que define o âmbito deste epic. Das doze hipóteses classificadas CRÍTICA
ou ALTA nos eixos de isolamento e egress, **duas** sobreviveram — porque `tecnica/17` §5.2-f já
declara, com eixos (AOS-064/066/103), que o substrato de sandbox não está montado. No Event Store,
onde as hipóteses eram sobre o interior de código que corre todos os dias, sobreviveram **oito**.

**Este epic não reabre o substrato de sandbox.** O que AOS-064/066/103 cobrem fica onde está. O que
entra aqui é o que nenhum documento vigente descreve: defeitos dentro de caminhos que correm, e
artefactos que dizem algo falso sobre o estado do sistema.

### 0.1 O que a §6 da auditoria omitiu — e este epic corrige

A §6 de `analises/12` listou doze remediações. A §3.2 do mesmo documento lista **catorze** achados.
Dois defeitos confirmados ficaram fora do plano de remediação:

| Achado | Onde está na auditoria | Porque a §6 o perdeu |
|---|---|---|
| `IngestStream` do store de referência não escreve no WAL | §3.2, A12 | Classificado *latente* e agrupado com os «não alcançáveis», que a §6 não enumerou |
| `desfazer` sobre um WAL encolhido torna durável um append que **falhou** | §3.2, A13 | Nasceu na refutação experimental, depois de a §6 estar redigida |

O segundo é o mais grave dos dois e foi **confirmado por execução**: viola a invariante central do
próprio mecanismo que existe para a garantir. Entram como **AOS-353** e **AOS-349**.

Um plano de remediação que deixa cair achados confirmados repete, em miniatura, o defeito que a
auditoria fecha. Fica registado que a omissão foi da §6, não da análise.

### 0.2 Ordem sugerida

| Prioridade | Tickets | Racional |
|---|---|---|
| **P0** | AOS-344, AOS-345, AOS-346 | Um documento de segurança que sustenta quatro classificações num mitigante falso; um cliff de disponibilidade que chega por relógio em ~22,7 h; e uma guarda de integridade contornável escolhendo qual byte se corrompe |
| **P1** | AOS-347, AOS-348, AOS-349, AOS-350, AOS-351, AOS-352 | Perda ou corrupção de dados confirmada por execução, e prontidão que mente sobre um substrato morto |
| **P2** | AOS-353, AOS-354, AOS-355, AOS-356, AOS-357, AOS-358 | Latentes, endurecimento de guardas, veracidade documental e cobertura de gate |

### 0.3 Tabela-resumo

| Ticket | Defeito | P | Alcance | Prova |
|---|---|---|---|---|
| AOS-344 | O mitigante que sustenta quatro linhas STRIDE é falso; `AOS_SANDBOX_DRIVER` cai em `fake` no compose de produção e não está nos pré-requisitos | P0 | **nó** | Leitura verificada |
| AOS-345 | A janela de leitura do JetStream avança pelo fim do log, não pelo fim do lote: > 2048 eventos ⇒ stream ilegível **e** inescrevível | P0 | **nó** (com `AOS_EVENTSTORE_NATS`) | Leitura verificada |
| AOS-346 | O CRC do WAL não cobre o cabeçalho de comprimento; um byte apaga em silêncio todos os registos íntegros seguintes | P0 | **nó** (com WAL) | **Executado** |
| AOS-347 | Dois `Open` concorrentes atribuem o mesmo `seq`; o nó deixa de arrancar | P1 | **nó** (com WAL) | **Executado** |
| AOS-348 | Um `Flush` falhado mata o WAL para sempre e devolve um erro que se lê como transitório | P1 | **nó** (com WAL) | **Executado** |
| AOS-349 | `desfazer` sobre um WAL encolhido chama `Truncate` para um tamanho maior, estende com zeros e torna durável um append falhado | P1 | **nó** (com WAL) | **Executado** |
| AOS-350 | `Healthy()` fica `true` com o Event Store a recusar todas as escritas — nos dois backends | P1 | **nó** | **Executado** |
| AOS-351 | O hash do perfil seccomp é selado no WORM por um driver que nunca recebe o perfil | P1 | **nó** (dev-hardened) | Leitura verificada |
| AOS-352 | `Streams()` não pode devolver erro; a perna do legal hold degrada fail-open | P1 | **nó** (com `AOS_EVENTSTORE_NATS`) | Leitura verificada |
| AOS-353 | `IngestStream` do store de referência não escreve no WAL: um restauro sobre um store durável evapora no reinício | P2 | latente | Leitura verificada |
| AOS-354 | `E_NO_QUORUM` não existe no backend replicado; a tolerância que o banner promete nunca é armada | P2 | **nó** | Leitura verificada |
| AOS-355 | `NewProductionSecure` testa a ausência do `EgressStub` em vez da presença de um hook de egress | P2 | latente | Leitura verificada |
| AOS-356 | Quatro declarações de estado que deixaram de ser verdade — duas delas **subdeclarando** a postura | P2 | documental | Leitura verificada |
| AOS-357 | Instrumentos de teste que não podem falhar, e um argumento escrito que está errado hoje e seria perigoso amanhã | P2 | arnês | Leitura verificada |
| AOS-358 | Não existe gate que exercite isolamento real, apesar de o componente gVisor não precisar de KVM | P2 | CI | Leitura verificada |

### 0.7 Estado da execução (2026-09-06)

Os quinze tickets estão **implementados**. Sessenta e dois dos sessenta e seis critérios de
aceitação estão `[x]`; quatro estão `[~]`, e cada um diz porquê no próprio critério. Nenhum
foi dado por satisfeito sem evidência nomeada.

**Os quatro `[~]`, e a razão de cada um:**

| Critério | Porquê |
|---|---|
| AOS-345 AC2 · AC3 | Exigem cluster NATS, que este ambiente não tem. O teste existe e SALTA; a aritmética da janela é coberta por testes que CORREM, com controlo positivo |
| AOS-352 AC5 | **Não aplicável** — `Streams()` não está declarada em `tecnica/12` |
| AOS-358 AC1 | O gate existe e o caminho de SALTO foi corrido; o caminho REAL exige Linux com docker privilegiado |

**Três desvios ao que o epic prescrevia, todos com razão medida:**

1. **AOS-346** foi fechado pela segunda cláusula do seu AC1, não pela primeira. Estender o
   `crc32` ao cabeçalho não fecha o defeito: para verificar o CRC é preciso localizar o
   trailer, e localizá-lo exige confiar no `len` que se quereria verificar.
2. **AOS-347** — o lado ESCRITOR já estava arbitrado por `tomarPosseDoWAL` (AOS-285). A
   colisão de `seq` entre dois nós não era alcançável; a via do INSPECTOR era, e é essa que
   fecha. A tranca NÃO foi dobrada dentro de `Open`, porque isso avermelharia o sensor de
   `conformance` que mede a ausência de arbitragem — e faria o substrato parecer que ganhou
   uma garantia que não tem.
3. **AOS-349** — verificar em `desfazer` não chega. Em `O_APPEND` o registo já aterrou no fim
   real do ficheiro antes de o `desfazer` correr, e por aterrar numa fronteira de registo
   antigo ficava bem-formado e ressuscitava no replay. A verificação corre antes da primeira
   escrita, e só o ENCOLHIMENTO é recusado — recusar um ficheiro maior seria arbitrar entre
   escritores, que é DEF-282 e está fora deste epic.

**Dois achados que a execução produziu e o epic não previa:**

- **`eventstore.BackupSource` declara a mesma `Streams()`**, pelo que AOS-352 arrastou
  `platform/backup/exporter.go` — um consumidor a mais do que os quatro que a §0.4 nomeia.
- **`scripts/ci/deferrals.py` conta ocorrências de `STUB` em maiúsculas**, e prosa
  explicativa que use a palavra em maiúsculas é contada como dívida nova. Não é defeito do
  gate; é uma convenção que o texto tem de respeitar.

### 0.8 O que a revisão adversarial mudou (2026-09-06)

A execução foi revista por um agente independente, que viu os requisitos e o diff e nunca o
raciocínio de quem implementou. Produziu catorze achados; **seis eram defeitos a sério na
própria remediação**, e estão corrigidos. Fica registado porque o saldo é o ponto: um epic de
remediação que não se deixa rever repete o defeito que fecha.

| Achado | O que estava errado | Correcção |
|---|---|---|
| Arranque | `crash_resume` matava o arranque numa falha de enumeração, contradizendo três comentários — crash-loop por indisponibilidade transitória do substrato | degrada e grita; o contrato de `ResumeInterruptedRuns` volta a ser verdade |
| Egress | `&EgressStub{}` (ponteiro) passava as DUAS guardas de AOS-355: os stubs têm receivers-valor e a assertion era de valor. Uma edição de UM caractere reabria o buraco | as guardas vêem valor e ponteiro; idêntico para o `IdentityStub`, que tinha o mesmo |
| JetStream | um `$JS.ACK` de forma inesperada derrubava **todas** as leituras, não só as multi-lote — pior do que o defeito de AOS-345 | malformado passa a cair no mesmo lado que ausente: «não sei», e só derruba quem precisa de avançar |
| WAL | um `os.Stat` falhado envenenava o WAL para sempre — a assimetria que AOS-348 fecha, reintroduzida | recusa este append e mais nada; um retry recupera |
| Relatório | o gate `isolation-live` declarava `pass:true` e `executor:real` como literais: com uma porta MORTA emitia-os na mesma | o veredicto é derivado de uma chamada positiva que tem de trazer conteúdo da raiz semeada |
| Pipeline | nem `dormencia` nem `isolation-live` estavam ligados a pipeline nenhum — um alvo que ninguém invoca não é gate | `dormencia` entra em `ALL_GATES`; `isolation-live` fica opcional por exigir docker privilegiado, e a assimetria está justificada no script |

Mais quatro achados de **declaração sobre-afirmada**, todos corrigidos no texto: o doc de
`ErrEgressStub` prometia mais do que a guarda impõe; `governance_restore` dizia «o arranque
recusa» quando quem decide é o composition-root (que desarma o varredor de retenção — o
fail-closed está lá, noutro sítio); o doc de `IngestStream` contradizia o residual declarado
40 linhas abaixo; e o residual do seccomp estava declarado só para o Firecracker, faltando o
gVisor — que é o driver **em uso**.

Três achados eram **cobertura a zero** em caminhos de recuperação, agora com teste: o
varrimento multi-janela da ressincronização, a guarda de `desfazer`, e o `Stat` falhado.

**Um achado corrigiu-me sobre método**, e é o que vale a pena reter: declarei que a presença
do `$JS.ACK` numa entrega push não tinha sido medida, quando
`docs/reports/medicao-jetstream-arbitragem-2026-08-31.md` §A7 a **mediu** contra cluster real.
A evidência existia no repositório e não foi consultada. O que continua por medir é a FORMA do
subject de resposta contra a versão de servidor em uso — e é para isso que o ramo de
malformação passou a ser tolerante em vez de fatal.

**Um residual novo, declarado e não fechado:** o cenário P2 do gate de isolamento mede a
cadeia composta, não o gVisor — o guest valida o caminho ele próprio, antes de qualquer
syscall interceptável, pelo que trocar o `runsc` por um `exec` cru manteria P2 verde. Está no
campo `not_proved` do relatório e no cabeçalho do gate. Quem prova execução dentro do sandbox
é P1.

---

**Limites de evidência que se mantêm** (§0.6 continua a valer): nada foi corrido contra um
cluster NATS real, contra Firecracker ou contra gVisor reais. AOS-345 acrescenta a esses um
limite próprio — a suposição de que uma entrega push com `ack_policy: none` traz `$JS.ACK`
não foi medida, e o desenho contém o raio de explosão caso esteja errada.

---

### 0.4 Paralelismo — o que pode e o que não pode correr junto

A costura natural são os módulos Go. Duas restrições dominam:

**Seis tickets tocam `packages/substrate/eventstore`, e quatro deles o mesmo ficheiro.** AOS-346,
AOS-347, AOS-348 e AOS-349 alteram `durable.go`; são **estritamente sequenciais**, e a ordem
sugerida é essa — AOS-348 corrige a costura de teste (`ficheiroWAL`) de que AOS-349 precisa para ser
demonstrável. AOS-345 vive em `jetstream/` e AOS-353 em `backup.go`, pelo que podem correr em
paralelo com o bloco do WAL, mas não entre si e o bloco depois de AOS-350 tocar em `store.go`.

**AOS-352 muda um contrato e não pode partilhar vaga com ninguém.** Altera a assinatura de
`Streams()` na porta (`packages/cmd/aos/eventstore_port.go:43`), nas duas implementações e em quatro
consumidores. É o ticket de maior raio de alcance do epic.

| Podem correr em paralelo | Porquê |
|---|---|
| AOS-344 · AOS-351 · AOS-355 · AOS-358 | Módulos disjuntos (`cmd/aos`+`tecnica`, `substrate/sandbox`, `kernel/reference-monitor`, `scripts/ci`), sem aresta `replace` entre si nos caminhos alterados |
| AOS-356 · AOS-357 | Só documentação e arnês de teste |
| AOS-345 ‖ bloco do WAL | Ficheiros distintos do mesmo módulo; confirmar com `git diff --stat` antes de integrar |

| Nunca em paralelo | Porquê |
|---|---|
| AOS-346 → AOS-347 → AOS-348 → AOS-349 | Mesmo ficheiro (`durable.go`), e AOS-349 depende da costura que AOS-348 repara |
| AOS-352 com qualquer um | Muda contrato partilhado |
| AOS-350 com AOS-346..344 | Toca `store.go` **e** o estado do WAL que os outros alteram |

### 0.5 O que este epic NÃO cobre

Por desenho, e cada exclusão com razão nomeada:

- **O substrato de sandbox não montado no nó.** `tecnica/17` §5.2-f declara-o com eixos
  AOS-064/066/103. Cinco das oito hipóteses do isolamento caíram por isto. O que AOS-344 corrige é o
  **mitigante** invocado por essa declaração, não a declaração.
- **O `DNSFilter` sem chamadores** (AOS-068). Mesmo eixo, e com deny-all de rede no guest um
  resolvedor DNS dentro da sandbox não teria a quem perguntar.
  Pelo mesmo motivo fica de fora o corolário que a §5 de `analises/12` levantou — uma política com
  *hosts* e sem CIDRs faz `ipAllowed` negar **toda** a resolução do principal, com uma razão
  (`ReasonDNSRebinding`) que descreve mal a causa. Falha fechada, vive no mesmo código sem
  chamadores, e volta a ser relevante no dia em que AOS-103 o compuser.
- **O pool de microVMs e o SLI de cold-start.** `tecnica/17` §4.6 linha D endereça-o por nome, eixo
  AOS-103.
- **O RPO de 24 h.** `specs/EPIC-10:32`, `:322`, `:372`, `:377` marcam-no `[ ]`/`[~]`, e a anotação
  do `[x]` de `:325` declara em negrito que o RPO efectivo em produção é o do cron diário. Eixo
  AOS-102.
- **A ausência de re-medição contínua do substrato replicado em CI.** `EPIC-10:202` declara-a
  textualmente, incluindo que a cobertura do CI «subestima, por construção». O que AOS-358 pede é
  menos do que isso: que a dormência não seja silenciosa.
- **A não-materialização de ADR-004 e ADR-007.** É um dos estados formais do registo
  (`docs/adr/README.md`), e **ADR-002 — «Reference Monitor mandatório» — está no mesmo estado**. Se
  fosse defeito, seria um defeito maior, e a auditoria 09 não o levantou.

### 0.6 Limites de evidência

- **Nada foi corrido contra um cluster NATS real.** AOS-345, AOS-352 e AOS-354 são derivados de
  leitura verificada, não de execução. `AOS_NATS_URL` não aparece em `scripts/`, `.github/`,
  `Makefile` nem `deploy/`.
- **Nada foi corrido contra Firecracker ou gVisor reais.** AOS-351 é derivado da leitura do wire
  (`firecrackerexecutor.go:33-40`) e do driver.
- **AOS-346, AOS-347, AOS-348, AOS-349 e AOS-350 foram executados** numa cópia isolada do módulo,
  fora da árvore do repositório.
- **Não foi medido** se o servidor NATS recusa um `STREAM.CREATE` divergente — a premissa que AOS-357
  manda registar.

---

## AOS-344 — O mitigante que sustenta quatro linhas do STRIDE é falso, e o driver de sandbox não tem porta de produção

### Contexto

`tecnica/17_Analise_STRIDE.md` §5.2-f classifica quatro linhas de ameaça (§4.6-S/T/D/E) como
*deferido-com-eixo*, e sustenta a classificação numa frase: «Mitigante actual: o catálogo de tools
do nó está vazio».

O catálogo sancionado de produção **não está vazio**. `deploy/server/model-tools/tools.json:18`
declara `"sandbox": { "command": "read", "path_arg": "doc_id" }` para a tool `doc_read`, e
`deploy/server/docker-compose.prod.yml:372` monta-o no contentor do nó. Ao lado, `:364` define
`AOS_SANDBOX_DRIVER: "${AOS_SANDBOX_DRIVER:-}"` — vazio por omissão —, o que cai em
`sandboxwiring.go:119` (`kind := sandbox.DriverFake`), o driver que `substrate/sandbox/driver.go:31`
marca «NUNCA usar em produção» e que `DEF-701`/`DEF-702` registam com o tipo `NUNCA-EM-PRODUCAO`.
`AOS_MODE` é operável no mesmo ficheiro (`:63`).

O deployment `dev-hardened` está protegido: fixa `firecracker` e o URL do executor
(`docker-compose.oidc.yml:137-138`). O de produção não. E `AOS_SANDBOX_DRIVER` **não consta** da
tabela de pré-requisitos de produção de `deploy/server/README.md:576-583`, que enumera seis portas —
tabela cuja linha anterior (`:574`) diz, involuntariamente, «que é como a terceira tinha passado
despercebida».

**O dano não é escape de sandbox.** `driver_fake.go:105-205` é um VFS in-memory: não executa
processos (`grep os/exec|net\.|http\.` devolve zero) e o `readHost` (`:227-234`) não tem chamador a
partir do jail. O dano é que **o `fake` é o único dos três drivers que falha aberto**:
`firecracker` e `gvisor` sem URL de executor devolvem `ErrDriverUnavailable` — fail-closed, e a
própria §5.2-f elogia-os por isso. O `fake` sucede em silêncio, e o resultado fabricado é selado na
hash-chain WORM como se fosse um efeito real.

Duas ressalvas que reduzem o alcance sem eliminar o defeito: `AOS_MODEL_TOOLS` também tem default
vazio (`docker-compose.prod.yml:350`), pelo que o catálogo é *entregue e montado*, não *activo por
omissão*; e `deploy/server/README.md:135` indica `AOS_SANDBOX_DRIVER=gvisor`, que é o que hoje salva
o servidor — mas é uma variável de operador ausente da lista de portas do mesmo documento.

O padrão da guarda existe e ninguém o replicou: `packages/cmd/aos/main.go` tem nove guardas
`ErrProductionNeeds*` (`:35`, `:54`, `:63`, `:208`, `:241`, `:272`, `:274`, `:287`, `:289`), nenhuma
sobre o sandbox, e `registerSandboxLaunchers` (`sandboxwiring.go:106`)
nem sequer recebe a `Config` — não tem por onde saber que está em produção.

Porque sobreviveu: o gate `deferrals` (`scripts/ci/deferrals.py:146-158`) verifica que cada marcador
`NUNCA-EM-PRODUCAO` tem linha no registo. **Verifica a documentação do deferimento, nunca o seu
cumprimento** — e nunca se o gatilho de saída que a própria entrada declara já ocorreu.

### Critérios de Aceitação

- [x] `AOS_SANDBOX_DRIVER` ausente deixa de eleger o `fake`: ou o arranque recusa, ou
      `buildSandboxDriver` devolve `ErrDriverUnavailable`, alinhando o default com o
      comportamento fail-closed dos outros dois drivers
- [x] Um teste que construa o nó sem `AOS_SANDBOX_DRIVER` e prove a recusa, com controlo negativo
      que prove que o valor explícito `fake` continua a funcionar em desenvolvimento
- [x] `deploy/server/README.md:576-583` ganha a linha do driver de sandbox na tabela de portas de
      produção
- [x] `tecnica/17` §5.2-f deixa de afirmar que o catálogo de tools está vazio, e as quatro linhas
      §4.6-S/T/D/E são reavaliadas contra o estado real
- [x] `DEF-701`/`DEF-702` são reavaliadas: o gatilho de saída que declaram («catálogo de tools
      não-vazio a executar código não-confiável») já ocorreu nos deployments sancionados

### Estado

**IMPLEMENTADO** (2026-09-06). `AOS_SANDBOX_DRIVER` ausente — ou explicitamente `fake` — deixa de
eleger o driver de referência sob `AOS_MODE=production` COM tools de sandbox ligadas:
`ErrProductionNeedsSandboxDriver`, no molde das nove guardas `ErrProductionNeeds*` já existentes.
`registerSandboxLaunchers` passa a receber a postura por parâmetro — até aqui não recebia nada da
`Config` e não tinha por onde saber em que modo corria. O default do compose de produção passa a
`gvisor`. Fora de produção nada muda. Guarda provada não-vacuosa por mutação: com a recusa
desligada, três testes avermelham.

---

## AOS-345 — A janela de leitura do JetStream avança pelo fim do log, não pelo fim do lote

### Contexto

`jetstream/store.go:562`, dentro de `lerLote`, avança a janela de leitura com
`s.cn.UltimoSeqDoSubject(s.stream, subject, prazo)` — que devolve o seq da última mensagem do
**subject inteiro** (`natsjs/jetstream.go:485-492`, `LastBySubject`), não o do lote. O comentário
imediatamente acima (`:560-561`) diz o contrário: «o avanço é feito pelo seq do ÚLTIMO evento lido».

`lerSubject` usa esse valor em `:484` como `inicio = ultimoJS + 1`, isto é, **para lá do fim do
log**. Com `janelaDeLeitura = 2048` (`:416`), o segundo lote arranca depois do fim, não recebe nada,
e morre no timeout de `:553-555`. É fail-closed, coerente com o que o ficheiro declara em `:446-449` e repete literalmente em `:554`
(«um log servido truncado seria pior do que este erro») — mas como `hidratar` precede as escritas
(`:244`), o stream fica **ilegível e inescrevível**.

**A via de alcance é o relógio, não a carga.** `service.go:43` fixa `DefaultLeaseTTL = 2 * time.Minute`
e `:382` usa-o por omissão; `:400` deriva `hbInterval = cfg.ttl / 3` = 40 s; `:489` compõe a posse do
laço de retenção incondicionalmente quando há `LeaseManager`. O ticker de `posse_de_laco.go:194`
chama `Heartbeat` (`:229`), e cada `Heartbeat` faz `Append` de um `lease.renewed` ao stream
`lease:svc:retention` (`durable/lease.go:484`). **2048 × 40 s ≈ 22,7 horas de uptime** — um nó
completamente ocioso chega lá, e a partir daí o laço de retenção não pode ser reclamado.

Não existe mecanismo que o impeça: `StreamConfig` (`natsjs/jetstream.go:100-118`) não fixa
`MaxMsgsPerSubject`, `MaxMsgs` nem `MaxAge`; não há particionamento de streams; e `deny_purge`
proíbe encolher.

Porque sobreviveu: `grep janelaDeLeitura|lerLote|lerSubject` em `*_test.go` devolve **zero**, e o
maior stream alguma vez medido neste repositório são **2000 eventos**
(`jetstream/throughput_test.go:197`) — quarenta e oito aquém da fronteira. O laço multi-lote nunca
correu.

A correcção está ao alcance da mão: `natsjs.Msg` tem campo `Reply` (`natsjs/conn.go:79`), e o
`$JS.ACK…` de cada mensagem entregue carrega o `stream_seq`.

### Critérios de Aceitação

- [x] O avanço da janela deriva do seq físico da **última mensagem do lote**, não do subject
- [~] Um teste com > `janelaDeLeitura` eventos num só `stream_id` que prove que `Read` devolve todos
      — e que o número de lotes é o esperado
      · `TestJanela_AcimaDaJanela_LeTudoEContinuaEscrivel` existe e **SALTA** sem `AOS_NATS_URL`.
        Cobertura equivalente que CORRE: `TestJanela_LerEmLotes_DevolveTodosEOsLotesSaoOsEsperados`
        (5000 eventos, 3 lotes, `quantos` = [2048, 2048, 904]) sobre um log falso onde o seq do
        subject é deliberadamente maior do que o do lote, mais o controlo positivo
        `TestJanela_AvancoPeloFimDoLogMorreNoSegundoLote`. **NÃO VERIFICADO contra cluster real.**
- [~] Um teste que prove que o stream continua **escrevível** depois desse volume (o caminho
      `hidratar` → `Append`)
      · passo (3) do mesmo teste, com handle novo (cache fria). Exige cluster. **NÃO VERIFICADO.**
- [x] O comentário de `store.go:560-561` passa a descrever o que o código faz
- [x] `AOS_NATS_URL` documentado como pré-requisito do teste, no molde de `conformidade_test.go:21`

### Estado

**IMPLEMENTADO** (2026-09-06), com limite de evidência. O avanço deriva do seq físico do `$JS.ACK…`
de cada entrega; o laço de lotes saiu para `lerEmLotes`, o que permite exercitá-lo SEM cluster.

**A suposição central — que uma entrega push com `ack_policy: none` traz `$JS.ACK` — NÃO foi medida
contra cluster.** O raio de explosão está contido por desenho: um `$JS.ACK` malformado falha logo
(violação de protocolo), mas um `Reply` AUSENTE marca «não sei» e só derruba a leitura que precisa
mesmo de avançar — um log que cabe numa janela nunca usa o valor.

---

## AOS-346 — O CRC do WAL não cobre o cabeçalho de comprimento

### Contexto

O enquadramento do WAL é `len` (BE32) + payload + `crc32` (BE32), e `durable.go:151-154` calcula o
CRC **só sobre o payload**: `binary.BigEndian.PutUint32(tr[:], crc32.Checksum(payload, crcTable))`.
O cabeçalho de comprimento fica sem protecção.

A consequência foi **executada**, com controlo positivo. Cinco eventos num WAL real
(offsets `[0 296 592 888 1184]`, 1480 bytes), fechar, corromper **um byte**, reabrir:

| Variante | Byte trocado | Resultado |
|---|---|---|
| `len` maior | @299 `0x20`→`0xFF` | `Open err=nil`, leu 1 de 5, ficheiro 1480 → **296** |
| `len` menor | @299 `0x20`→`0x0A` | `Open err=nil`, ficheiro → **296** |
| `len` +256 | @298 `0x01`→`0x02` | `Open err=nil`, ficheiro → **296** |
| **controlo: payload** | @306 `0x74`→`0x7E` | **`E_WAL_CORRUPTED_MID_LOG`**, `orfaos=3`, ficheiro **intacto** |

O controlo isola a variável: mesma posição, mesmo número de bytes. Corromper o payload dispara o
fail-closed e não toca no ficheiro; corromper o cabeçalho apaga três eventos confirmados e devolve
`err=nil`.

O mecanismo tem dois caminhos, ambos com `orfaos=0`: `ReadFull` falha (`durable.go:322-324` e `:331-332`,
`enquadrado=false`), ou o CRC falha num frame torto e `contaRegistosIntegros` (`:307-309`) continua a
partir de um offset **desalinhado**, onde não reconhece nada. O segundo é o pior: a guarda *corre* e
conclui «cauda rasgada».

**A consequência é que `ErrWALCorruptedMidLog` é contornável escolhendo qual byte se troca.** Quatro
bytes em cada 296 (≈1,4% do ficheiro) são zona cega. Bit rot, um sector rasgado ou um write parcial
de página aterram lá com essa probabilidade, sem precisar de atacante.

Porque sobreviveu: `durable_corrupcao_a_meio_test.go:61-80` (`corrompeMarca`) corrompe **sempre o
payload** — exercita o único caminho onde a detecção funciona.

### Critérios de Aceitação

- [x] O CRC passa a cobrir o cabeçalho de comprimento, ou o enquadramento ganha outra verificação
      que torne um `len` corrompido detectável
      · pela **segunda cláusula**, e a primeira foi rejeitada com razão medida: estender o `crc32`
        ao cabeçalho NÃO fecha nada — para verificar o CRC é preciso localizar o trailer, e
        localizá-lo exige confiar no `len` que se quer verificar. Quem fecha é `contaOrfaos`, que
        RESSINCRONIZA o enquadramento em vez de confiar na posição do leitor.
- [x] Um teste que corrompa o **cabeçalho** de um registo a meio e exija `ErrWALCorruptedMidLog` com
      o ficheiro intacto, nas três variantes medidas (`len` maior, menor, e maior-mas-cabe)
- [x] O controlo positivo existente (corrupção de payload) continua verde, provando que a guarda não
      se tornou indiscriminada
- [x] Compatibilidade de formato declarada: um WAL escrito pela versão anterior ou é legível, ou a
      migração está descrita e testada
      · **é legível, e sem migração**: o enquadramento é byte-a-byte o mesmo. Só mudou o algoritmo
        que decide se há registos íntegros depois de uma quebra.

### Estado

**IMPLEMENTADO** (2026-09-06). Fechado pela ressincronização do enquadramento, e não pelo CRC sobre
o cabeçalho — ver o AC1. Teste provado VERMELHO antes da correcção, nas três variantes: `Open
err=nil`, ficheiro 1544 → 309 bytes, três eventos confirmados apagados. O formato não mudou.

---

## AOS-347 — Dois `Open` concorrentes atribuem o mesmo `seq` e o nó deixa de arrancar

### Contexto

`durable.go:102` abre sempre o WAL em `os.O_CREATE|os.O_WRONLY|os.O_APPEND`, e `LockWAL` é
deliberadamente separado de `Open` (`wallock.go:43-49`), com a promessa de `wallock.go:32-38`: «quem
LÊ abre o WAL como sempre, sem pedir nada e sem ser bloqueado».

Três subcomandos chamam `eventstore.Open` **sem pedir posse**: `wal_inspect.go:59`,
`wal_summary.go:69` e `wal-count`. Os comentários dizem «com o contentor principal PARADO» — o que é
uma convenção documentada, não uma restrição imposta.

Executado: com o escritor A vivo, um segundo `Open` **tem sucesso**, e `LockWAL` passa na mesma —
confirmando que `Open` não toma posse nenhuma (o lock é sobre o ficheiro irmão `…events.wal.lock`; o
WAL nunca é trancado). Cada `Open` reconstrói a sua própria cabeça em memória:

```
seq atribuído por A = 4 ; seq atribuído por B = 4 ; COLIDEM
segunda ronda:  A=5  B=5  COLIDEM
ficheiro final: 7 registos, seqs=[1 2 3 4 4 5 5]
ARRANQUE SEGUINTE FALHOU = E_RESTORE_ORDER: lote de restauro nao e gapless
```

**Correcção ao que a auditoria alegou primeiro:** o `Open` de um inspector **não** destrói eventos
por si só — 888 bytes antes, 888 depois. Só trunca quando `fi.Size() > validEnd`, e um WAL cujos
appends passaram por `Flush`+`fsync` não tem cauda parcial. O dano isolado é a colisão de `seq`, que
é terminal de outra forma: o nó não volta a arrancar.

Composto com AOS-346, porém, a destruição é real: com bit rot no `len` do 3.º registo, um comando de
**leitura** abriu sem erro, leu 2 de 5, e o ficheiro passou de 1480 para 592 bytes — **três eventos
confirmados apagados por um inspector**.

Porque sobreviveu: `wallock_test.go:196-198` chama `escritor.Close()` **antes** de abrir o leitor em
`:201`. Mede «um leitor abre um WAL parado», não o cenário do `wal-inspect` que a própria mensagem de
falha (`:203`) nomeia.

### Critérios de Aceitação

- [x] Abrir um WAL sob posse activa é recusado, ou os subcomandos de inspecção passam a pedir posse
      partilhada — a convenção documentada passa a ser restrição imposta
- [x] Um teste com o escritor **vivo** que prove que o segundo abridor não obtém uma cabeça
      concorrente; `wallock_test.go:177-212` é corrigido para medir o cenário que o seu nome promete
- [x] Um teste que prove que a leitura legítima (nó parado) continua a funcionar sem posse
- [x] O caminho de inspecção deixa de poder truncar: ou abre em leitura apenas, ou a truncatura de
      reposição exige posse

### Estado

**IMPLEMENTADO** (2026-09-06). Novo `eventstore.OpenReadOnly`: replay sem anexar o WAL para append e
sem tocar no ficheiro; `wal-count` e `wal-summary` passam a usá-lo. `wallock_test.go` mantém agora o
escritor VIVO — media «um leitor abre um WAL PARADO», que não é o cenário que o seu nome promete.

**CORRECÇÃO AO CONTEXTO DO TICKET:** o lado ESCRITOR já estava arbitrado. `bootstrap.go` chama
`tomarPosseDoWAL` (AOS-285) antes de `eventstore.Open`, e `aos-orq` faz o mesmo — a colisão de `seq`
entre dois `aos serve` não era alcançável no nó composto. O que era alcançável era a via do
INSPECTOR, e é essa que fecha.

Deliberadamente NÃO se dobrou a tranca dentro de `Open`: o pacote `conformance` mede a ausência de
arbitragem entre escritores com N `Open` concorrentes (o sensor do AOS-100), e dobrá-la faria o
substrato parecer que ganhou uma garantia que não tem — a armadilha que esse pacote existe para
desarmar.

---

## AOS-348 — Um `Flush` falhado mata o WAL para sempre e anuncia-se transitório

### Contexto

`durable.go:181-183` trata o `Flush` como o `Sync`: `if err := w.w.Flush(); err != nil { return
w.desfazer(antes, err) }`. Mas `desfazer` (`:223-240`) trunca e **nunca faz `w.w.Reset(w.f)`**. O
erro pegajoso do `bufio.Writer` fica, e o append seguinte morre já em `:165-167` (`w.w.Write(hdr[:])`)
com `return err` — **sem passar por `desfazer` e sem marcar `envenenado`** (que só é escrito em
`:225`). `Store.Append` embrulha-o como «eventstore: persistir evento committed» (`store.go:410`),
indistinguível de um `ENOSPC` transitório.

Medido na mesma execução, lado a lado:

```
[Flush]  2.º append falha = "persistir evento committed: sonda: write falhou (ENOSPC)"
         RETRY com a falha REMOVIDA = mesma mensagem ENOSPC   ****  MORREU
         retries extra #1 #2 #3 = ENOSPC, ENOSPC, ENOSPC
[fsync]  falha = "sonda: fsync falhou (EIO)"
         RETRY = <nil>                                        ****  RECUPERA
```

Mesma falha transitória, mesmo retry: um caminho recupera, o outro morre em silêncio. O chamador lê
«ENOSPC», conclui «disco cheio, volto a tentar», e nunca mais escreve nada.

**A causa mecânica de nunca ter sido apanhado é um defeito do próprio arnês.** A costura
`ficheiroWAL` (`durable.go:69-73`) **não cobre o caminho de escrita**: o `bufio.Writer` é construído
em `openWALAppend` sobre o `*os.File` original, pelo que trocar `s.wal.f` — como fazem os testes
existentes — só intercepta `Sync` e `Close`. Medido: `writes=0`. O `Write` de `ficheiroFalhado`
(`durable_fsync_falhado_test.go:47`) é **código morto**. Foi preciso substituir também `s.wal.w` para
disparar a falha.

Isto explica a assimetria dos testes: `TestWAL_FsyncFalhado_RetryNaoDuplicaSeq` (`:148-182`) retenta;
o gémeo do `Flush` (`:238-268`) verifica o ficheiro e **nunca retenta**, pelo que não distingue
«reposto e utilizável» de «reposto e morto».

### Critérios de Aceitação

- [x] `desfazer` repõe o `bufio.Writer` (`Reset`) ou marca `envenenado`, de modo que um erro
      terminal deixe de se anunciar como retentável
- [x] A costura `ficheiroWAL` passa a cobrir o caminho de escrita, ou o teste passa a substituir
      também o writer — o `Write` de `ficheiroFalhado` deixa de ser código morto
- [x] Um teste no molde de `TestWAL_FsyncFalhado_RetryNaoDuplicaSeq` que **retente** depois de um
      `Flush` falhado com a falha removida, e exija sucesso ou erro terminal explícito
- [x] O contraste entre os dois caminhos (`Flush` e `Sync`) fica fixado por teste, para que a
      assimetria não regresse

### Estado

**IMPLEMENTADO** (2026-09-06). `desfazer` repõe o `bufio.Writer` (`Reset`), e os três `Write` passam
também por lá. Nova costura `wal.trocarFicheiro`, que troca descritor E writer de uma vez — antes
trocava-se só `s.wal.f` e o `Write` da sonda era código morto (`writes=0`). Mutação verificada: com
o `Reset` removido, o retry com a falha REMOVIDA devolve a mesma mensagem ENOSPC, reproduzindo o
defeito medido.

---

## AOS-349 — `desfazer` sobre um WAL encolhido torna durável um append que falhou

### Contexto

Este defeito **não estava na §6 de `analises/12`** — nasceu na refutação experimental, depois de a
§6 estar redigida. É o mais grave dos dois que a §0.1 recupera.

`desfazer` (`durable.go:223-240`) repõe o WAL chamando `os.Truncate(path, w.tamanho)` com o tamanho
que tinha antes do append falhado. Se o ficheiro tiver entretanto **encolhido** por baixo — o que
AOS-346 composto com AOS-347 torna alcançável — `w.tamanho` fica à frente do ficheiro real, e
`os.Truncate` para um tamanho **maior** estende com zeros em vez de repor.

Executado:

```
ficheiro real=592 ; A.wal.tamanho (em memória)=1480 ; DESSINCRONIZADO
append com fsync falhado -> desfazer chama os.Truncate(path, 1480) sobre 888 bytes
tamanho após desfazer = 1480 (o ficheiro CRESCEU)
bytes nulos no ficheiro = 606 de 1480
replay final: seq=1, seq=2, seq=6      (3, 4 e 5 desaparecidos)
**o append FALHADO (s9) ficou DURÁVEL**
```

A invariante central — «erro devolvido ⇒ nada ficou durável», que `durable.go:212-222` declara e que
`durable_fsync_falhado_test.go:104-144` prova no caso simples — é violada **pelo próprio código que
existe para a repor**.

### Critérios de Aceitação

- [x] `desfazer` deixa de poder estender o ficheiro: a reposição verifica o tamanho real antes de
      truncar, e um `w.tamanho` à frente do ficheiro é condição de erro terminal (`envenenado`), não
      de truncatura
- [x] Um teste que reproduza a dessincronização e prove que o append falhado **não** fica durável
- [x] Um teste que prove que a reposição normal (ficheiro coerente) continua a funcionar
- [x] O caso de `w.tamanho` dessincronizado é distinguível no erro devolvido ao operador

### Estado

**IMPLEMENTADO** (2026-09-06), com uma correcção ao remédio que o próprio teste obrigou a fazer.
Verificar em `desfazer` NÃO chega: o WAL é aberto em `O_APPEND`, pelo que o registo já aterrou no
fim REAL do ficheiro antes de o `desfazer` correr — e, por aterrar numa fronteira de registo antigo,
ficava bem-formado e ressuscitava no replay. A verificação corre ANTES da primeira escrita.

Só o ENCOLHIMENTO é recusado, e a restrição é deliberada: um ficheiro MAIOR do que a memória é o
outro escritor (DEF-282), e recusá-lo faria o substrato parecer que arbitra entre processos. Medido
ao escrever isto — com `real != antes` o sensor `TestDefeito_DoisEscritoresTornamOWALInabrivel`
ficou VERMELHO.

---

## AOS-350 — `Healthy()` não conhece o substrato, nos dois backends

### Contexto

`store.go:490` é `func (s *Store) Healthy() bool { return !s.closed.Load() }` — `closed` é o único
input. E `jetstream/store.go:860` é o gémeo: `return !s.estaFechado()`. **Os dois backends do Event
Store partilham o mesmo modo de falha de prontidão.**

Executado, no backend de referência:

```
Healthy() com o WAL morto pelo Flush pegajoso                       = true
Healthy() com wal.envenenado=true e o append a dizer
            "nao aceita mais escritas"                              = true
```

Os consumidores são reais e são três: `/readyz` (`api.go:1100` — fica **200 verde**), o gauge
`aos_eventstore_healthy` (`api.go:1177` — fica **1**), e o SLI `controlPlaneAvailable`
(`slo_evaluator.go:468`).

O comentário desse SLI descreve **exactamente este modo de falha**: diz, por palavras suas, que um nó
que recusasse 100% das escritas manteria o SLI a 1.0 e o alerta calado. O eixo que foi tapado foi o
do WORM (`s.seloWORM.aRecusarEscritas()`). **O eixo do WAL ficou aberto.**

O resultado é que um nó com o Event Store morto recusa todas as escritas, o orquestrador de
contentores continua a encaminhar tráfego, e `control_plane_availability_low` não dispara.

Este ticket é o **amplificador** de AOS-348: sozinho não avaria nada; o que faz é garantir que um
substrato morto atravessa a prontidão, o gauge e o SLI sem acender nada.

### Critérios de Aceitação

- [x] `Healthy()` do store de referência reflecte o estado do WAL (envenenado, ou writer morto)
- [x] `Healthy()` do backend JetStream reflecte o estado da ligação
- [x] Um teste por backend que ponha o substrato em estado de recusa e exija `Healthy() == false`
- [x] Um teste que prove que `/readyz` deixa de responder 200 nesse estado, e que o gauge
      `aos_eventstore_healthy` vai a 0
- [x] O comentário de `slo_evaluator.go:468` é actualizado: o eixo do WAL deixa de estar aberto

### Estado

**IMPLEMENTADO** (2026-09-06) nos dois backends. O estado do WAL é publicado num átomo
(`wal.recusaEscritas`) e não lido por `w.mu` — o mutex é detido durante o `fsync`, e o `/readyz` não
pode ficar refém da latência do disco; há teste que prova que `Healthy()` responde com o mutex do
WAL detido. O backend JetStream passa a exigir socket vivo (`natsjs.Conn.Ligada`).

A asserção «um store aberto devia estar Healthy» de `jetstream/logica_test.go` foi INVERTIDA:
aquele store é construído sem cliente nenhum, e dizê-lo saudável era a crença que este ticket
remove.

---

## AOS-351 — O hash do perfil seccomp é selado por um driver que nunca recebe o perfil

### Contexto

Este é o único defeito do eixo do isolamento que sobreviveu à refutação, e sobreviveu porque não é
«o mecanismo não está montado» — que `tecnica/17` §5.2-f cobre — mas «o mecanismo está montado e o
trilho afirma algo falso».

`lifecycle.go:245-292` grava `SeccompProfileHash` e `Driver: inst.Kind` nos **três** eventos de ciclo
de vida selados no WORM. Mas `driver_firecracker.go:66-107` recebe a `Spec` e **nunca lê
`spec.Seccomp`**; e a interface `GuestExecutor.RunInGuest` (`:18`) nem sequer a transporta. O wire
para o guest confirma-o: `firecrackerexecutor.go:36-40` leva `tool_id`, `command`, `args`, `path` e
`write`, e mais nada.

Com `AOS_SANDBOX_DRIVER=firecracker` — que `docker-compose.oidc.yml:137-138` liga, tirando o driver
do estado `ErrDriverUnavailable` — o WORM sela `driver=firecracker` **e** o hash do perfil
`sbx-seccomp/v1`, quando nenhum byte desse perfil chega à microVM. A hash-chain que existe para ser
prova de conformidade inscreve uma atestação falsa.

O repositório afirma o contrário, por escrito, e contradiz-se a si próprio em três ficheiros do mesmo
pacote:

| Ficheiro | O que diz |
|---|---|
| `doc.go:99-101` | **Correcto e qualificado** — «propagado ao driver via `[Spec.Seccomp]` e o `[FakeDriver]` IMPÕE-o» |
| `driver.go:73-78` | Generaliza sem qualificação — «propaga-o para o driver, **que o IMPÕE no `[Exec]`** … o hash atesta o perfil **REALMENTE aplicado** — não uma declaração desligada do caminho de execução» |
| `lifecycle.go:153-156` | Repete a versão generalizada |

Os «residuais de produção» de `deploy/node/dev-hardened/firecracker/README.md:60` nomeiam, em
`:65`, o *seccomp do VMM* (o jailer), que é outra coisa: o filtro do processo Firecracker no host, não a
allowlist de syscalls do guest. A dívida não está declarada em lado nenhum.

**Há evidência quase forense de que é lapso e não decisão.** No campo vizinho da mesma struct, os
autores escreveram a ressalva certa: `events.go:53-55` diz que o `RootFSBaseDigest` fica «vazio
quando não há snapshot configurado (a raiz read-only é então só uma **declaração de config**)». O
campo do seccomp, três linhas acima (`:47-49`), não tem ressalva nenhuma. Sabiam qualificar uma
atestação condicional, fizeram-no para o rootfs, e não o fizeram para o seccomp.

### Critérios de Aceitação

- [x] `driver.go:73-78` e `lifecycle.go:153-156` passam a dizer o que `doc.go:99-101` já diz
      correctamente — quem impõe o perfil, e em que drivers
- [x] O evento de ciclo de vida deixa de atestar um perfil não aplicado: ou `SeccompProfileHash` é
      omitido quando `inst.Kind != DriverFake`, ou o evento ganha um campo que diga **quem** impôs —
      a mesma forma que `events.go:53-55` já usa para o `RootFSBaseDigest`
- [x] Um teste que construa um `Launcher` com o driver Firecracker e prove que o evento selado não
      afirma imposição que não houve
- [x] O residual fica declarado onde o repositório declara residuais deste eixo — o perfil só é
      imposto no `FakeDriver`, e o caminho real depende de o guest o aplicar

### Estado

**IMPLEMENTADO** (2026-09-06) pela opção (b) — o evento ganha `SeccompEnforcedBy`, na mesma forma
que `events.go` já usava para o `RootFSBaseDigest`. A qualificação viaja COLADA ao hash: onde há
hash há sempre um `seccomp_enforced_by` explícito, nunca um hash nu. Teste por driver
(firecracker/gvisor ⇒ `none`, fake ⇒ `driver`).

---

## AOS-352 — `Streams()` não pode devolver erro, e a perna do legal hold degrada fail-open

### Contexto

`jetstream/store.go:868-875` responde a `Streams()` perguntando ao servidor e, em erro,
`return nil` — sem log, sem sinal. Uma falha transitória de rede fica **indistinguível de «não há
streams»**.

Não é um descuido local: a porta declara `Streams() []string` (`packages/cmd/aos/eventstore_port.go:43`),
sem canal de erro, e as duas implementações (`eventstore/backup.go:88`, `jetstream/store.go:868`)
seguem-na. **O defeito está na interface.** Corrigi-lo muda contrato.

Quatro varredores de arranque consomem-na, e degradam em direcções diferentes:

| Consumidor | Direcção | Consequência |
|---|---|---|
| `governance_restore.go:140` | **fail-open** | O índice titular→partição do **legal hold** volta vazio; `restoreSubjectIndex` só devolve erro do `Read`, nunca do `Streams`. Um hold que devia cobrir partições noutros streams deixa de cobrir, e o `ExpirationJob` pode crypto-shred material sob hold |
| `crash_resume.go:93` | degradação | Zero runs órfãos retomados |
| `retention.go:105` | fail-closed | Nada expira — direcção segura |
| `wal_summary.go:75` | diagnóstico | Sumário incompleto |

O comentário de `governance_restore.go:128-131` descreve a consequência pelas suas próprias palavras:
sem o índice, um legal hold por-partição deixaria de cobrir, após um restart, os registos desse
titular.

Porque sobreviveu: `eventstore_port.go:35-42` discute explicitamente a semântica **entre processos**
de `Streams()` — «um índice em memória responde à pergunta errada» — e nunca menciona sinalização de
erro. A porta foi pensada, e este eixo escapou.

### Critérios de Aceitação

- [x] `Streams()` passa a poder devolver erro na porta e nas duas implementações
- [x] `governance_restore.go` distingue «não há streams» de «não foi possível perguntar», e trata o
      segundo como fail-closed — um legal hold não pode ser silenciosamente reduzido
- [x] Os outros três consumidores tratam o erro de forma explícita e declarada
- [x] Um teste que injecte falha na enumeração e prove que o restauro de governação **não** conclui
      com um índice vazio
- [~] `tecnica/12_Contratos_de_Interface.md` reflecte a assinatura nova, se lá estiver declarada
      · **NÃO APLICÁVEL**: a assinatura não está lá declarada (grep por `EventStorePort` e por
        `Streams` nesse ficheiro não devolve nada).

### Estado

**IMPLEMENTADO** (2026-09-06). Raio MAIOR do que o epic previa: `eventstore.BackupSource` declara a
mesma assinatura, pelo que `platform/backup/exporter.go` entrou também — e aí a direcção certa é
fail-closed, porque um export sobre uma enumeração falhada seria um backup incompleto devolvido como
sucesso.

---

## AOS-353 — `IngestStream` do store de referência não escreve no WAL

### Contexto

Este defeito **não estava na §6 de `analises/12`** — foi classificado latente na §3.2 e o plano de
remediação não o enumerou. Ver §0.1.

`backup.go:213-219` aplica o lote restaurado apenas às réplicas em memória (`r.store(ev.clone())`) e
eleva o commit index. Não há uma única chamada a `s.wal`. O contraste é directo: `Append` persiste
**antes** de aplicar (`store.go:406-412`).

Uma subtileza importante reduz o alcance sem anular o defeito: no replay de arranque a omissão é
**correcta** — `durable.go:375-425` faz `restoreInto(s, events)` **antes** de `s.wal = w`, e
reescrever duplicaria. O problema é o **segundo uso** da mesma porta: depois de `Open` devolver,
`s.wal != nil`, e um `IngestStream` de restauro ou PITR não escreve nada em disco. Reinicia-se o nó e
o restauro evaporou.

Alcance hoje: nenhum. `NewRestorer` só aparece em testes; o nó nunca compõe um `Restorer`, e
`bootstrap.go:588-604` declara por escrito que `RestoreTo` recebe manifesto e checkpoint como
argumentos sem que nada os persista, e que o backup está desligado por omissão.

Porque sobreviveu, e é o padrão desta auditoria: **todos** os testes de restauro do store de
referência constroem o destino com `mustNew(t)` (`backup_test.go:118`, `:152`, `:195`) — `New()`
in-memory, **nunca `Open(path)`**. Um restauro para um store com WAL seguido de reinício não é
exercitado em lado nenhum. E `specs/EPIC-10` marca AOS-101 AC1/AC2 com `[~]` (`:322`, `:323`) e AC6 em `:327`, mas o que
declara em falta é o destino durável, o alvo por-instante e o ensaio periódico — nunca que o restauro
não persiste.

### Critérios de Aceitação

- [x] `IngestStream` persiste no WAL quando o store tem um, **sem** duplicar no caminho de replay de
      arranque (a distinção `s.wal == nil` durante `restoreInto` fica explícita e testada)
- [x] Um teste `Open(path)` → `IngestStream` → `Close` → reabrir → `Read` que prove que os eventos
      restaurados sobrevivem ao reinício
- [x] Um teste que prove que o replay de arranque continua a não duplicar
- [x] O comentário de `backup.go` declara qual dos dois caminhos persiste e porquê

### Estado

**IMPLEMENTADO** (2026-09-06). A persistência é em LOTE (`wal.appendLote`, com reposição ao nível do
lote): um restauro que devolve erro não deixa meio lote durável. Mutação verificada: com a escrita
desligada, o restauro evapora no reinício (`E_STREAM_NOT_FOUND`).

---

## AOS-354 — `E_NO_QUORUM` não existe no backend replicado, e a tolerância que o banner promete não tem gatilho

### Contexto

`ErrNoQuorum` é produzido apenas pelo store de referência (`store.go:340,376,472` e
`backup.go:121,145,195,199`). Em `jetstream/` e `natsjs/` há **zero** ocorrências: o erro canónico de
indisponibilidade transitória do substrato replicado é `natsjs.ErrDesligado`
(`natsjs/conn.go:30-35`, devolvido em `:394`).

Dois consumidores ramificam no sentinela, e a consequência é desigual:

- `cmd/aos/trajectory.go:375` — a perda é cosmética (HTTP 500 em vez de 503).
- `cmd/aos/progress_wiring.go:370` — **não é.** `burndownTransitorio` é a lista **fechada**
  (`:355-372`) que decide se uma leitura falhada do burn-down é indisponibilidade momentânea ou
  cegueira. Sobre JetStream, um `ErrDesligado` cai em «cegueira» e **mata o run à primeira**, em vez
  de tolerar N fronteiras consecutivas.

(`durable/checkpoint.go:146` cita o sentinela num comentário de doc, não num `errors.Is` — não é
consumidor.)

O banner de postura promete o contrário, por escrito. `posture_banner.go:426` diz que a
indisponibilidade transitória do substrato não mata o run, que a leitura se adia para a fronteira
seguinte, e que só passa a fatal ao fim de N fronteiras consecutivas. **Sobre o substrato replicado
essa tolerância nunca é armada**, porque o único erro que a arma nunca é emitido. É uma promessa
declarada que o substrato que o AOS-100 tornou preferencial não pode cumprir.

### Critérios de Aceitação

- [x] O backend JetStream emite `ErrNoQuorum` quando a condição é a que o sentinela nomeia, ou
      `burndownTransitorio` passa a reconhecer `natsjs.ErrDesligado` — uma das duas, com a escolha
      justificada
- [x] Um teste que prove que uma indisponibilidade transitória do substrato replicado **não** mata o
      run à primeira fronteira
- [x] `trajectory.go` devolve 503 e não 500 nessa condição
- [x] A promessa de `posture_banner.go:426` passa a ser verdadeira nos dois substratos, ou é
      qualificada para dizer em qual vigora

### Estado

**IMPLEMENTADO** (2026-09-06) pela primeira opção — o backend replicado traduz `natsjs.ErrDesligado`
para `eventstore.ErrNoQuorum`, EMBRULHANDO a causa. A escolha é o ponto: o sentinela é o CONTRATO da
porta, e um contrato que só uma implementação consegue produzir é um detalhe da implementação de
referência a vazar para o plano de controlo. Alargar a lista de `burndownTransitorio` deixava o
próximo consumidor a ter de conhecer os sentinelas de cada backend.

**NÃO VERIFICADO contra cluster real** — os testes medem a tradução, que é uma função pura da cadeia
de erros.

---

## AOS-355 — A guarda de produção testa a ausência do stub em vez da presença do hook de egress

### Contexto

`packages/kernel/reference-monitor/production.go:175-183` tem três guardas, e as três testam coisas
diferentes. `:175` e `:178` testam **presença de stub** (`containsHook(... EgressStub ...)`); `:181`
testa **ausência de gate activo** (`!m.hasActiveScopeGate()`).

`grep hasActiveEgress|ActiveEgress|hasEgress` em `packages/` devolve **zero definições**. O contraste
é interno ao próprio ficheiro: para o taint existem **dois** predicados de presença
(`hasWiredTaintGate` `:86`, `hasActiveTaintGate` `:102`) e para o escopo existe `hasActiveScopeGate`
(`:222`). Para o egress, nenhum.

O efeito: uma cadeia passada por `WithHooks(...)` **sem hook de egress nenhum** satisfaz
`NewProductionSecure`. `NewProduction` injecta a cadeia default primeiro (`:66-69`), mas as `opts` do
chamador aplicam-se depois e um `WithHooks` sobrepõe-na por inteiro — que é precisamente o que
`secured.go:394-396` faz.

Não é explorável hoje: `secured.go:385` acrescenta sempre o `egressHook` (construído em `:339`). Mas **é exactamente essa
regressão que a guarda existe para tornar impossível**, e não torna. O doc de `ErrEgressStub`
(`:142-144`) chega a afirmar a propriedade forte que o código não implementa: «sem hook de egress
real o default-deny de rede (AOS-067) não corre. Recusado».

A assimetria repete-se no arnês: `production_secure_test.go:82` tem
`TestNewProductionSecureRejectsMissingScopeGate` e **não tem** o equivalente de egress; e o
teste-veneno de `enforcement_guard_test.go:297-318` substitui o hook pelo `EgressStub{}` usando
`referencemonitor.New` **cru**, sem tocar em `NewProductionSecure` — testa a mutação que a guarda
apanha, não a omissão que ela deixa passar.

Nada em `tecnica/17` cobre isto: a §5.4 lista o egress como **entregue**, e nenhum documento distingue
«rejeita o stub» de «exige o hook».

### Critérios de Aceitação

- [x] Existe um predicado de presença para o hook de egress, no molde de `hasActiveScopeGate`
- [x] `NewProductionSecure` recusa uma cadeia sem hook de egress
- [x] Um guard-test `RejectsMissingEgress`, simétrico do `RejectsMissingScopeGate`
- [x] O teste-veneno ganha a mutação por **omissão** (não só por substituição pelo stub), e passa a
      exercitar `NewProductionSecure` em vez da via crua
- [x] O doc de `ErrEgressStub` passa a descrever o que a guarda impõe de facto

### Estado

**IMPLEMENTADO** (2026-09-06). `ErrEgressHookMissing` (sentinela próprio, distinto de
`ErrEgressStub`: a causa é oposta — slot vazio vs. slot ocupado por um no-op — e a correcção do
chamador também) mais `hasActiveEgressHook`. O predicado casa pelo NOME do slot e não pelo tipo,
porque o hook real vive no substrato e a fronteira de camadas proíbe o kernel de o importar; o
limite (presença ≠ eficácia) fica declarado. Guarda provada não-vacuosa por mutação.

---

## AOS-356 — Declarações de estado que deixaram de ser verdade

### Contexto

Quatro artefactos afirmam coisas que o código já não sustenta. **Duas delas subdeclaram a postura**,
o que é a classe inversa da habitual — e foi essa subdeclaração que levou uma lente inteira de
`analises/12` a nunca abrir o caminho onde o enforcement realmente vive.

**(a) `tecnica/17` §5.2-f — os drivers já não são skeletons.** Afirma que «os drivers
Firecracker/gVisor existem como skeletons **fail-closed** (`ErrDriverUnavailable`) … mas não fornecem
a fronteira». Deixou de ser verdade: `firecrackerexecutor.go:100-119` e `gvisorexecutor.go` injectam
executores remotos reais quando `AOS_SANDBOX_FIRECRACKER_URL`/`AOS_SANDBOX_GVISOR_URL` estão
definidos, e os componentes host-side existem e estão escritos
(`deploy/node/dev-hardened/firecracker/orchestrator/`, `deploy/server/gvisor/component/`). **A
fronteira é fornecida — falta topologia, não código.**

**(b) `deploy/node/README.md:147`** descreve o mesmo estado antigo, sem mencionar as duas variáveis de
URL que mudam o comportamento.

**(c) `tecnica/14_Matriz_Conformidade.md:162`** classifica «isolamento microVM e credential broker JIT
(`platform/broker`, `platform/attestation`)» como **«Ausente do grafo»**. Medido com `go list -deps`
em `packages/cmd/aos`: `substrate/sandbox`, `sandbox/seccomp`, `sandbox/network` e `platform/broker`
**estão** no grafo; só `platform/attestation` não está. A legenda do próprio documento (`:152`)
oferece a classificação correcta — «Composto? Não» — e não foi usada. **Corrigir esta linha agrava o
quadro**: a célula sustenta a consequência declarada («o nó não aplica o isolamento por microVM»);
corrigida, essa consequência passa a ser «corre no driver que o operador escolher, com default
`fake`» — ou seja, converte-se em AOS-344. A linha errada estava a esconder o achado.

**(d) `DEF-701`** (`REGISTO-Deferimentos.md:225`) afirma que «o driver de referência **NÃO cria jail
nem impõe** as invariantes de isolamento». `driver_fake.go:12-19` e `Create` (`:76-105`) impõem
`NoHostSocket`/`NoSharedNetNS`/`NoSharedPIDNS` fail-closed, e `:137-160` bloqueia traversal, symlink
e metacaracteres. O próprio `driver.go:31` — o ficheiro **ancorado** por DEF-701 — diz o contrário do
registo. A entrada **sobrestima** o risco: engana para falso alarme, não para falso conforto. É a
classe que `DEF-814` nomeia (`:243`, eixo AOS-329).

**(e) Menor:** `network/doc.go:61-63` e `tecnica/07:148` dizem que os drivers reais «traduziriam a
MESMA allowlist para o filtro de rede do kernel». Não é falso (é condicional), mas descreve um
desenho que a implementação já não escolheu: os drivers reais **não traduzem allowlist nenhuma —
removem a rede por inteiro** (`orchestrator/main.go:113-125` sem `network-interfaces`;
`component/main.go:156` `--network=none` + `:230` netns vazio).

### Critérios de Aceitação

- [x] As cinco declarações passam a descrever o estado real, com a nota de data e commit que
      `tecnica/14` §5.2 já usa como método
- [x] `tecnica/14:162` usa a classificação correcta da sua própria legenda, e a «Consequência
      declarada» é reescrita — reconhecendo que agrava
- [x] `DEF-701` é corrigida ou fechada, conforme o que o código sustenta hoje
- [x] O texto de `deploy/node/README.md:147` deixa de descrever o estado antigo — as duas variáveis
      de URL **já** constam da mesma tabela (`:149`, `:150`), e é só a linha do driver que as ignora
- [x] Fica registado que o desenho escolhido é **remoção** de rede e não filtragem, para que o
      condicional de `network/doc.go:61-63` não volte a ser lido como plano em vigor

### Estado

**IMPLEMENTADO** (2026-09-06). As cinco declarações corrigidas. `DEF-701` passou de ABERTO a
MITIGADO com a redacção reescrita — a anterior era FALSA e SOBRESTIMAVA o risco (é a classe que
`DEF-814` nomeia), e o gatilho de saída que declarava já tinha ocorrido.

---

## AOS-357 — Instrumentos de teste que não podem falhar, e um argumento escrito que está errado

### Contexto

Três itens do arnês, todos pequenos, todos com o mesmo padrão: um instrumento que não faz o que o seu
próprio comentário diz.

**(a) `TestAC4_ComandoDeFalhaEObrigatorio`** (`jetstream/perda_test.go:202-209`). O nome promete uma
obrigação; o corpo tem duas saídas — `t.Skip` sem cluster, `t.Logf` com cluster e sem `AOS_KILL_CMD`.
**Nenhuma falha.** E é pior do que parece: `go test` não imprime `t.Logf` de um teste que passa sem
`-v`, e `scripts/ci/test.sh:20` corre sem `-v`. No único cenário que o teste existe para apanhar
— cluster presente, comando de morte esquecido — o aviso é **invisível**. O guarda não falha *e* não
avisa. O remédio é de uma linha (`t.Errorf`), só dispara com cluster, e nunca avermelharia o CI actual.

**(b) O argumento de `durable_corrupcao_a_meio_test.go:27`** justifica a guarda de corrupção a meio
dizendo que um write rasgado «perde o próprio enquadramento … por construção, zero». **É falso.** O
`append` faz `Flush` (write(2)) e só depois `Sync`; entre os dois os bytes estão na page cache, e numa
queda de **máquina** a escrita de volta é por página enquanto o dispositivo persiste por sector — um
registo que atravesse fronteiras de sector pode ficar com header e trailer persistidos e o miolo não,
isto é, **frame completo com CRC inválido**. O argumento verdadeiro é **temporal**, não estrutural: um
write rasgado só pode ser o último registo, porque cada `append` anterior fez `fsync` com sucesso
antes de devolver `committed` (`durable.go:181-190`); logo `orfaos > 0` prova que a quebra não é de um
crash. O veredicto do teste continua correcto — é a justificação escrita que está errada, e
tornar-se-ia perigosa se alguém acrescentasse group-commit, hipótese que `store.go:31-34`
explicitamente antecipa.

**(c) A razão da assimetria entre soberania e imutabilidade não está escrita.**
`jetstream/store.go:170-182` lê a configuração armazenada do stream **só sob `cfg.fronteira`**, e
verifica apenas `Placement` (`soberania.go:97-111`); `NumReplicas`, `DenyDelete` e `DenyPurge` chegam
em `StreamConfigLida` (`natsjs/jetstream.go:456-461`) e não são lidos por ninguém. Não é defeito — o
append-only tem prova **comportamental** (delete e purge recusados, medidos com `10057`/`10110`,
`EPIC-10:190`), enquanto a soberania só tem prova **declarativa**, e por isso só esta precisava de ser
lida de volta. Mas essa razão não está em lado nenhum, e sem ela a assimetria parece um esquecimento.

### Critérios de Aceitação

- [x] `perda_test.go:207` passa a `t.Errorf`, ou equivalente que falhe quando há cluster e falta o
      comando de morte
- [x] O comentário de `durable_corrupcao_a_meio_test.go:27` passa a dar o argumento temporal
- [x] `jetstream/store.go:170-182` ganha o comentário que explica porque só a colocação é lida de
      volta
- [x] Fica registado que a recusa do servidor a um `STREAM.CREATE` divergente
      (`natsjs/jetstream.go:124-129`) é **afirmação de documentação não medida** — nenhuma das oito
      adendas do relatório de medição a mediu

### Estado

**IMPLEMENTADO** (2026-09-06). (a) `t.Logf` → `t.Errorf`; (b) o argumento passa a ser temporal;
(c) a razão da assimetria fica escrita; (d) registado como `DEF-816`.

---

## AOS-358 — Não existe gate que exercite isolamento real, e o componente gVisor não precisa de KVM

### Contexto

Nenhum gate de CI exercita ADR-004 ou ADR-007 contra a fronteira que eles especificam. O gate
`security` (AOS-075) corre a bateria de isolamento sobre `sandbox.NewFakeDriver()`
(`packages/security-tests/isolation_test.go:81,130,167,197`); o gate `dr-e2e` (AOS-118) mata um campo
`alive bool` (`replicated.go:49`, via `Store.Kill` em `dr_replay_e2e_test.go:282`) numa réplica
**in-process** construída em `:88-93`; `replicated.go:30-33` di-lo: «`replica` é uma cópia in-process
do log». E os testes que exigem `AOS_NATS_URL`
nunca correm, porque nenhum ficheiro de CI o define — numa execução medida do módulo `eventstore`,
**32 dos 150** são saltados por essa razão.

**A defesa habitual — «não é razoável exigir KVM em CI» — é correcta para o Firecracker e
insustentável na sua forma geral.** `deploy/server/README.md:117` diz explicitamente que o componente
gVisor **não precisa de KVM**, o componente está no repositório (`deploy/server/gvisor/component/`), e
o nó fala com ele por HTTP através de `AOS_SANDBOX_GVISOR_URL`. Um gate sobre o executor gVisor é
tecnicamente viável hoje com peças que já existem.

A limitação está declarada em três sítios — `tecnica/07` §4 («o timing real de microVM não é medível
neste ambiente»), `ADR-015:112` («HA de produção depende do ES replicado real, validado em staging»),
`docs/reports/prontidao-modelos-agenticos.md:226` («observada ao vivo, não em CI») — mas nenhuma está
no registo de deferimentos nem na §5.4, e **nada falha quando a suite real adormece**.

Nota de método: o gate `security` **não** é vácuo — `security.sh:20` declara a propriedade, e a lista
`REQUIRED` (`:60-68`, `:83-86`) exige por nome treze `TestMetaDetects_*` que provam que, com o
controlo contornado, o ataque passa, e o `selftest.sh` secção H prova que
desligar um controlo o avermelha. A crítica correcta não é «é vácuo»; é «prova o **contrato** com
fidelidade demonstrada, e o contrato não é a fronteira».

### Critérios de Aceitação

- [~] Existe um gate — ou um alvo opcional documentado — que levante o componente gVisor e corra pelo
      menos um cenário de isolamento contra o executor real
      · `scripts/ci/isolation-live.sh` + `make ci-isolation-live`. O caminho de SALTO foi corrido e é
        ruidoso (compila a suite `-tags gvlive`, exige cada cenário por nome, corre o contrafactual).
        O caminho REAL exige Linux + docker privilegiado e **NÃO FOI CORRIDO** neste ambiente.
- [x] O gate declara o que prova e o que **não** prova, distinguindo fronteira de contrato
- [x] A dormência das suites que exigem `AOS_NATS_URL` e `-tags fclive` deixa de ser silenciosa: ou
      um relatório de cobertura as nomeia, ou existe uma linha no registo que a declare
- [x] O procedimento manual continua documentado para o Firecracker, onde a exigência de KVM é
      legítima

### Estado

**IMPLEMENTADO** (2026-09-06), com o caminho real por correr neste ambiente. Dois artefactos:
`scripts/ci/isolation-live.sh` (gate opcional, salta RUIDOSAMENTE e corre na mesma o contrafactual)
e `scripts/ci/dormencia.sh` (nomeia as 45 suites que exigem `AOS_NATS_URL` e EXIGE que as suites
atrás de build tag COMPILEM). O segundo foi provado a avermelhar: com um símbolo inexistente na
suite `gvlive`, «packages/security-tests NÃO compila com -tags gvlive».

---
