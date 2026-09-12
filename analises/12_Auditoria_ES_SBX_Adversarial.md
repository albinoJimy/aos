# Auditoria adversarial do Substrato — ES + SBX

| Campo | Valor |
|---|---|
| Documento | `analises/12_Auditoria_ES_SBX_Adversarial.md` |
| Data | 2026-09-06 |
| Estado auditado | HEAD `b092757` (`fix(AOS-329): o marcador escapava num comentario partido em duas linhas`), branch `fix/AOS-329-marcador-multilinha`, árvore limpa |
| Âmbito | **ES** (`packages/substrate/eventstore`, incl. `jetstream/`, `natsjs/`, `conformance/`), **SBX** (`packages/substrate/sandbox`, incl. `network/`, `seccomp/`), e as costuras com `packages/cmd/aos`, `packages/integration` e `deploy/` |
| Tipo | Cinco lentes independentes → **refutação adversarial** (uma por eixo, mais uma **experimental**) → **medição executada** no módulo, no nó real e numa cópia isolada do WAL |
| Auditoria anterior | `analises/11_Auditoria_MEM_REG_GW_BRK_Adversarial.md` (2026-09-04), cujo §7.2 deixa por medir «o comportamento com N réplicas reais» |
| Contratos verificados contra | `specs/00_System_Spec.md` §11, `specs/EPIC-07`, `EPIC-10`, `tecnica/07`, `10`, `12`, `13`, `14`, `16`, `17`, ADR-015/017/018/023, `docs/governance/REGISTO-Deferimentos.md`, `docs/reports/medicao-jetstream-arbitragem-2026-08-31.md` (736 linhas, **incluindo as oito adendas**) |
| Remediação | Por abrir — ver §6 |

---

## 1. Método

Três passagens, com o ónus da prova invertido a cada uma.

**Passagem 1 — cinco lentes independentes.** ES/conformidade, ES/durabilidade-e-operação,
SBX/isolamento, SBX/egress, e a costura ES↔SBX↔nó↔governação. Instrução comum: âncora
`ficheiro:linha` para tudo; `NÃO VERIFICADO` como resposta legítima; e procurar activamente
**testes que passam pela razão errada**. Resultado: **42 hipóteses-defeito**, oito por lente mais
duas extra, cada uma enunciada de forma falsificável e acompanhada de «como um adversário a poderia
refutar».

**Passagem 2 — seis refutadores.** Regra única: *o objectivo é derrubar a hipótese, não confirmá-la*.
Nenhum viu o raciocínio de quem acusou. Cada um teve de classificar em quatro estados — **defeito**,
**deferimento declarado**, **não-requisito**, **refutada** — e de atribuir **alcançabilidade**
(alcançável hoje, latente, inalcançável).

Um dos seis foi **experimental**: em vez de argumentar contra as hipóteses do WAL, copiou o módulo
para fora da árvore, escreveu testes-experiência e correu-os. É a diferença entre «li o código e
concluo» e «corri o cenário e aqui está o output».

**Passagem 3 — medição.** Suites dos dois módulos executadas e contadas; o nó real levantado e
conduzido ponta-a-ponta (`.claude/skills/run-aos/driver.sh smoke`, **9/9 verde**, run `smoke-1440`,
com o pré-voo a confirmar binários frescos).

### 1.1 O que cada passagem mudou

| | |
|---|---|
| Passagem 1 → 2 | **12 hipóteses caíram** por evidência falsa ou lida fora de contexto; **11 foram reclassificadas** como deferimentos já registados; **8** eram fragilidade e não violação de requisito |
| Passagem 2 → 3 | A execução confirmou quatro hipóteses do WAL que nenhum argumento tinha decidido, **corrigiu uma** e **descobriu uma quinta** que nenhuma lente vira |
| Refutação → tese | **Dois achados centrais não vieram de nenhuma lente** — nasceram de refutadores a verificar o que as lentes tinham assumido |

O saldo — **12 sobreviventes em 42** — é o número mais informativo deste relatório. Mais de dois
terços do que cinco analistas competentes produziram não resistiu a uma tentativa honesta de o
derrubar. É a mesma proporção que a auditoria 11 encontrou, e pela mesma razão: a passagem 1
confunde sistematicamente «o mecanismo não está montado» com «o mecanismo está partido».

### 1.2 Erros desta auditoria, declarados

Três, porque escondê-los invalidaria o método:

1. **Li o corpo do relatório de medição do JetStream e tratei-o como estado corrente.** O ficheiro
   tem 736 linhas e **oito adendas**; o corpo (§1–§7) foi corrigido por elas. O §5 («o que NÃO foi
   verificado») está **fechado** quase por inteiro: AC5 na ADENDA 3 e, com um quarto nó noutra
   região, na ADENDA 7; AC2 push na §A7; throughput na ADENDA 4; falha de nó reconciliada na
   ADENDA 5. Os seis CA do AOS-100 estão hoje `[x]` em `specs/EPIC-10:185-190`. **Duas das pistas
   que dei aos refutadores herdaram este erro** e foram corrigidas por eles.
2. **Apresentei os ADRs não-materializados como fio condutor.** `docs/adr/README.md:57,:60` marcam
   ADR-004 e ADR-007 como «Catálogo, por materializar» — mas isso é um dos **estados formais** do
   registo, e `:55` mostra que **ADR-002, «Reference Monitor mandatório», está no mesmo estado**.
   Se fosse defeito, seria um defeito maior, e a auditoria 09 não o levantou. Linha descartada.
3. **Enviei apenas metade das hipóteses do ES/durabilidade para validação.** L2-H5 a H8 ficaram
   por julgar até eu dar pela lacuna; foram depois submetidas e **nenhuma sobreviveu**.

---

## 2. A tese: o mitigante que sustenta quatro linhas do STRIDE é falso

As auditorias 10 e 11 fixaram um princípio que governa este âmbito:

> **Um buraco que um ADR ratificado descreve não é descoberta — é a leitura do ADR.**

Aplicado ao substrato, esse princípio elimina cinco das oito hipóteses do isolamento e quatro das
oito do egress. `tecnica/17_Analise_STRIDE.md` §5.2-f declara, com eixos nomeados (AOS-064/066/103),
que o substrato de sandbox não está montado no nó. Quem o «descobre» está a ler o documento.

Mas a §5.2-f não se limita a declarar o buraco. **Sustenta-o num mitigante**, e é essa frase que
decide a classificação de quatro linhas STRIDE (§4.6-S/T/D/E) como *deferido-com-eixo*:

> «Mitigante actual: o catálogo de tools do nó está vazio.»

**É falso contra os ficheiros que o próprio repositório entrega.**

| Facto | Âncora |
|---|---|
| O catálogo sancionado de produção declara um bloco `sandbox` | `deploy/server/model-tools/tools.json:18` — `"sandbox": { "command": "read", "path_arg": "doc_id" }` |
| É montado no contentor do nó | `deploy/server/docker-compose.prod.yml:372` |
| O driver de sandbox não tem valor por omissão | `deploy/server/docker-compose.prod.yml:364` — `AOS_SANDBOX_DRIVER: "${AOS_SANDBOX_DRIVER:-}"` |
| Vazio cai no driver que o repositório marca `NUNCA-EM-PRODUCAO` | `packages/cmd/aos/sandboxwiring.go:119` — `kind := sandbox.DriverFake`; `packages/substrate/sandbox/driver.go:31` |
| `AOS_MODE` é operável no mesmo ficheiro | `deploy/server/docker-compose.prod.yml:63` |
| E **não consta** da tabela de pré-requisitos de produção | `deploy/server/README.md:576-583` — seis portas; nenhuma é o driver |

O deployment `dev-hardened` está protegido: fixa `firecracker` e o URL do executor
(`docker-compose.oidc.yml:137-138`). O de produção não.

**A honestidade obriga a duas ressalvas que reduzem o alcance sem eliminar o achado.** Primeira:
`AOS_MODEL_TOOLS` também tem default vazio (`docker-compose.prod.yml:350`), pelo que o catálogo é
*entregue e montado*, não *activo por omissão* — o operador tem de o apontar, que é evidentemente o
propósito de o montar. Segunda: `deploy/server/README.md:135` indica `AOS_SANDBOX_DRIVER=gvisor`, e
é isso que hoje salva o servidor. Mas é uma variável de operador ausente da lista de portas de
produção do mesmo documento — cuja linha imediatamente anterior (`:574`) diz, involuntariamente,
*«que é como a terceira tinha passado despercebida»*.

### 2.1 E o dano está mal descrito — em ambas as direcções

A passagem 1 acusou o driver `fake` de ser um buraco de contenção. Não é. `driver_fake.go:105-205`
mostra um VFS in-memory: uma call sem `Path` é eco de `Command+Args` (`:198-204`); com `Path`,
resolve dentro do jail; o `readHost` (`:227-234`) é o único funil para o host e **não tem chamador a
partir do jail**. Não executa processos — `grep os/exec|net\.|http\.` em `driver_fake.go` devolve
zero.

O risco real é outro, e é mais subtil: **o `fake` é o único dos três drivers que falha aberto.**
`firecracker` e `gvisor` sem URL de executor devolvem `ErrDriverUnavailable` — fail-closed, e o
`tecnica/17` §5.2-f elogia-os por isso. O `fake` **sucede em silêncio**. Um nó de produção com o
catálogo activo e o driver por definir não deixa de executar tools: executa-as contra um simulador e
devolve resultados fabricados, selados na hash-chain WORM como se fossem efeitos reais.

A correcção não precisa de uma nona guarda `ErrProductionNeeds*`: basta **inverter o default** —
`AOS_SANDBOX_DRIVER` ausente passa a recusar, ou a devolver `ErrDriverUnavailable` — e a postura
fica coerente com o resto do binário.

### 2.2 O segundo achado que nenhuma lente viu

Existe no ES um defeito que nada no repositório descreve, que se alcança **por relógio** e não por
carga, e que nenhum teste pode apanhar.

`jetstream/store.go:562`, dentro de `lerLote`, avança a janela de leitura assim:

```go
// O seq físico do último é pedido ao servidor por [Store.lerSubject]; aqui basta
// avançar a janela, e o avanço é feito pelo seq do ÚLTIMO evento lido.
ultimoJS, err = s.cn.UltimoSeqDoSubject(s.stream, subject, prazo)
```

O comentário diz «o seq do último evento lido». O código pede `UltimoSeqDoSubject` — o seq da última
mensagem do **subject inteiro** (`natsjs/jetstream.go:485-492`, `LastBySubject`). O `lerSubject`
usa-o em `:484` como `inicio = ultimoJS + 1`, isto é, **para lá do fim do log**. Com
`janelaDeLeitura = 2048` (`:416`), o segundo lote não recebe nada e morre no timeout de `:553-555`.

É fail-closed, coerente com a filosofia que o próprio ficheiro declara em `:446-449` («um log
servido truncado seria pior do que este erro»). Mas como `hidratar` precede as escritas (`:245`), um
stream com mais de 2048 eventos fica **ilegível e inescrevível**.

**A via de alcance, verificada ponta a ponta:**

| Passo | Âncora |
|---|---|
| `DefaultLeaseTTL = 2 * time.Minute` | `packages/cmd/aos/service.go:43`, usado como default em `:382` |
| `hbInterval = cfg.ttl / 3` → **40 s** | `packages/cmd/aos/service.go:400` |
| A posse do laço de retenção é composta incondicionalmente | `packages/cmd/aos/service.go:489` |
| O ticker corre a `p.hb` e chama `Heartbeat` | `packages/cmd/aos/posse_de_laco.go:194`, `:229` |
| Cada `Heartbeat` faz `Append` de um `lease.renewed` ao mesmo stream | `packages/kernel/agent-runtime/durable/lease.go:485` |
| O stream é `lease:svc:retention` | `packages/cmd/aos/posse_de_laco.go:75-79` |

2048 × 40 s = **81 920 s ≈ 22,7 horas de uptime**. Um nó completamente ocioso chega lá.

Não existe mecanismo que o impeça: `StreamConfig` (`natsjs/jetstream.go:100-118`) não fixa
`MaxMsgsPerSubject`, `MaxMsgs` nem `MaxAge`; não há particionamento de streams; e `deny_purge`
proíbe encolher. E não existe sensor: `grep janelaDeLeitura|lerLote|lerSubject` em `*_test.go`
devolve **zero**, e o maior stream alguma vez medido neste repositório são **2000 eventos**
(`jetstream/throughput_test.go:197`) — quarenta e oito aquém da fronteira.

A correcção está ao alcance da mão e não foi vista: `natsjs.Msg` tem campo `Reply`
(`natsjs/conn.go:79`), e o `$JS.ACK…` de cada mensagem entregue carrega o `stream_seq`.

---

## 3. Achados que sobrevivem

Doze das 42 hipóteses, mais os dois achados nascidos na refutação. Ordenados por gravidade × alcance.

### 3.1 Alcançáveis hoje

| # | Achado | Âncora | Gravidade |
|---|---|---|---|
| **A1** | O mitigante da §5.2-f é falso; `AOS_SANDBOX_DRIVER` cai em `fake` no compose de produção e não está nos pré-requisitos | §2 acima | **Crítica** |
| **A2** | Stream com > 2048 eventos fica ilegível e inescrevível; alcançado por relógio em ~22,7 h | §2.2 acima | **Alta** |
| **A3** | Corrupção de **um byte** no cabeçalho de comprimento do WAL apaga em silêncio todos os registos íntegros seguintes | `durable.go:151-154`, `:327-328`, `:307-309`, `:402-409` | **Crítica** |
| **A4** | Dois `Open` concorrentes atribuem o mesmo `seq`; o nó deixa de arrancar (`E_RESTORE_ORDER`) | `durable.go:102`; `wal_inspect.go:59`, `wal_summary.go:69` | **Crítica** |
| **A5** | Um `Flush` falhado mata o WAL permanentemente e devolve um erro que se lê como transitório | `durable.go:181-183`, `:223-240`, `:165-167` | **Alta** |
| **A6** | `Healthy()` fica `true` com o Event Store a recusar todas as escritas — nos **dois** backends | `store.go:490`; `jetstream/store.go:860` | **Alta** |
| **A7** | O hash do perfil seccomp é selado no WORM por um driver que nunca recebe o perfil | `lifecycle.go:245-292`; `driver_firecracker.go:18,66-107`; `firecrackerexecutor.go:33-40` | **Alta** |
| **A8** | `Streams()` não pode devolver erro; uma falha transitória é indistinguível de «não há streams», e a perna do **legal hold** degrada fail-open | `jetstream/store.go:868-875`; `eventstore_port.go:43`; `governance_restore.go:140` | **Média-alta** |
| **A9** | `E_NO_QUORUM` não existe no backend JetStream; a tolerância a indisponibilidade transitória que o banner promete nunca é armada | `store.go:340,376,472` (zero em `jetstream/`); `progress_wiring.go:370`; `posture_banner.go:426` | **Média** |
| **A10** | `tecnica/14:162` declara «Ausente do grafo» o que `go list -deps` mostra composto | `tecnica/14_Matriz_Conformidade.md:162` | **Média** |
| **A11** | `DEF-701` descreve um estado do código que deixou de ser verdade | `REGISTO-Deferimentos.md:225` vs `driver_fake.go:12-19` | **Baixa** |

**A3, A4, A5 e A6 foram executadas**, não deduzidas. A força da A3 está no controlo positivo:
mesma posição no ficheiro, mesmo número de bytes trocados — corromper o *payload* dispara
`E_WAL_CORRUPTED_MID_LOG` e não toca no ficheiro; corromper o *cabeçalho* apaga três eventos
confirmados e devolve `err=nil`, encolhendo o WAL de 1480 para 296 bytes. A guarda que existe para
tornar a corrupção barulhenta é contornável escolhendo qual byte se troca; cerca de 1,4% do ficheiro
é zona cega, e bit rot chega lá sem precisar de atacante.

Nota sobre a A10: corrigi-la **agrava** o quadro. A célula sustenta a consequência declarada («o nó
não aplica o isolamento por microVM»); corrigida, essa consequência passa a ser «corre no driver que
o operador escolher, com default `fake`» — ou seja, converte-se na A1. A linha errada estava a
esconder o achado, não a inflacioná-lo.

### 3.2 Latentes

| # | Achado | Âncora |
|---|---|---|
| **A12** | `IngestStream` do store de referência não escreve no WAL: um restauro/PITR sobre um store durável evapora no reinício | `backup.go:213-219` vs `store.go:406-412` |
| **A13** | Um `desfazer` sobre um WAL truncado por baixo chama `os.Truncate` para um tamanho **maior**, estende com zeros e torna durável um append que falhou | executado; `durable.go:223-240` |
| **A14** | `NewProductionSecure` testa a **ausência** do `EgressStub` em vez da **presença** de um hook de egress | `production.go:178-180` vs `:181-183` |

A A12 tem uma subtileza que a lente não vira e que reduz o alcance sem a anular: no replay de
arranque a omissão é **correcta** (`durable.go:375-425` faz `restoreInto` antes de `s.wal = w`;
reescrever duplicaria). O defeito é no *segundo* uso da mesma porta. E nenhum caminho composto o
alcança: `NewRestorer` só aparece em testes.

A A14 é a única do egress que sobrevive, e sobrevive sozinha. O contraste é interno ao próprio
ficheiro: para o taint existem dois predicados de presença (`hasWiredTaintGate`,
`hasActiveTaintGate`), para o escopo existe `hasActiveScopeGate`, e para o egress **nenhum**. A
assimetria repete-se no teste — há `TestNewProductionSecureRejectsMissingScopeGate` e não há o
equivalente de egress; e o teste-veneno de `enforcement_guard_test.go:297-318` testa a mutação que a
guarda apanha, por via crua, sem tocar em `NewProductionSecure`.

---

## 4. O que está bem, e deve ser dito

Uma auditoria que só produz achados mente por omissão. Este substrato tem trabalho de qualidade
invulgar, e três coisas merecem registo porque são exactamente o que impediu a maioria das hipóteses
de sobreviver.

**A rede da sandbox real não é filtrada — é removida.** Não há tradução da `Policy` para um filtro
de kernel porque não é preciso: o `fcConfig` do orchestrator (`orchestrator/main.go:113-125`) tem
quatro chaves e **`network-interfaces` não é uma delas**, pelo que o Firecracker não cria NIC; o
gVisor tem `--network=none` (`component/main.go:156`) *e* netns próprio e vazio (`:230`). O contrato
de fio (`wire/wire.go`) não transporta política nenhuma — e não devia mesmo, porque do outro lado
não há nada a filtrar. Deny-all por remoção é estritamente mais forte do que default-deny com
allowlist. Foi isto que derrubou a hipótese mais grave da lente do egress.

**A medição do JetStream é engenharia exemplar.** Foi feita contra um cluster real, começa por
declarar a regra de método («NÃO assumas que o backend tem a propriedade porque a documentação dele
o diz»), tem um §5 que lista o que **não** foi verificado, e oito adendas que **retractam** partes do
corpo à medida que mediram mais. O §4 é um achado genuíno que mudou o desenho: a dedup do JetStream
é uma *janela*, não um índice, e por isso a idempotência do AOS não podia ser delegada nela. E o
cliente `natsjs` escrito à mão só com stdlib é decisão ratificada do ADR-017 §1, com o custo
declarado no `doc.go` do pacote.

**O repositório sabe distinguir um teste que prova de um teste que passa.** `jail_test.go:155-180`
(`TestSecurity_HostSentinelHasRefutationPower`) estabelece baseline zero, força o acesso pelo único
funil, exige o sentinela virado, e diz porquê em comentário: «sem isto, `HostTouches()==0` seria uma
tautologia». O `security.sh:20` exige por nome oito **meta-testes** que provam que, com o controlo
contornado, o ataque passa. O `dr-e2e.sh` usa `require_tests` contra passagem vazia. Esta disciplina
existe e está escrita — o que a torna o padrão contra o qual o §5 abaixo é medido.

---

## 5. Testes que passam pela razão errada

Verificados um a um; os que não sobreviveram à verificação estão marcados.

| Teste | Acusação | Veredicto |
|---|---|---|
| `TestLockWAL_NaoBloqueiaQuemLe` (`wallock_test.go:177-212`) | Fecha o escritor em `:196` antes de abrir o leitor em `:200` — mede «leitor abre um WAL parado», não o cenário do `wal-inspect` que a própria mensagem de falha nomeia | **Sustenta-se.** O mais claro do lote |
| `TestDurable_CorrupcaoAMeioRecusaEmVezDeApagar` (`:93-116`) | Corrompe o *payload* (protegido por CRC) e nunca o *cabeçalho* (não protegido) | **Sustenta-se** — e é a razão de A3 nunca ter sido apanhada |
| `TestAC4_ComandoDeFalhaEObrigatorio` (`perda_test.go:202-209`) | O nome promete obrigatoriedade; o corpo é `t.Logf` | **Sustenta-se, e é pior.** `go test` não imprime `t.Logf` de um teste que passa sem `-v`, e `scripts/ci/test.sh:20` corre sem `-v`. No único cenário que existe para apanhar, o aviso é **invisível** |
| `TestPool_ExecutionStaysMediated` (`pool_mediation_test.go:13`) | O lease do pool nunca é passado ao `Execute` | **Sustenta-se.** Apagando as linhas do pool, tudo o resto passa idêntico |
| `TestIntegration_ToolCallRunsInMicroVM` (`lifecycle_test.go:40`) | O nome diz microVM, o corpo usa `NewFakeDriver()` | **Sustenta-se só quanto ao nome.** O doc comment diz «na microVM (fake)» e o corpo assere propriedades com poder de refutação provado |
| `TestDriver_IsolationInvariantsEnforced` (`driver_test.go:102-119`) | Assere booleanos literais `true` | **Sustenta-se, mas é inofensivo** — o par negativo `TestDriver_CreateRejectsWeakIsolation` (`:205-219`) carrega a prova |
| `TestNaoVacuidade_UmSubstratoQueArbitraEDetectado` (`referencia_test.go:173-197`) | O «substrato que arbitra» é o mesmo ponteiro devolvido n vezes | Sustenta-se; o doc admite-o em `:165-169` |
| `TestDNS_Rebinding_Negado` (`dns_filter_test.go:143-171`) | Usa `StaticResolver`, logo não testa rebinding | **REFUTADA.** Devolve um host *na* allowlist a resolver para um IP *fora* dela, assere `ReasonDNSRebinding` e verifica o IP selado. A acusação confunde coerência nome→IP com rebinding por TTL |
| `TestAppendOnly_NoMutationMethods` (`store_test.go:91-119`) | Reflecte sobre a interface, não sobre os tipos concretos | Sustenta-se; verdadeiro e irrelevante para o risco real |
| `BenchmarkReplay` (`throughput_test.go:182`) | 200 e 2000 eventos, ambos abaixo de `janelaDeLeitura` | **Sustenta-se, e é a razão de A2 existir** |

**Um corolário que nenhuma hipótese tinha visto**, encontrado a verificar `TestDNS_NaAllowlist_Resolvido`:
uma política cujo principal tenha *hosts* mas nenhum CIDR em regra nenhuma faz `ipAllowed`
(`dns_filter.go:414-431`) devolver sempre `false`, e **toda** a resolução DNS desse principal é
negada como `ReasonDNSRebinding` — incluindo nomes legítimos, com uma razão que descreve mal a causa.
Falha fechada, vive em código não-composto, gravidade baixa. Mas não está documentado, e o comentário
de `:403-413` antecipa metade do problema sem antecipar esta metade.

---

## 6. Remediação proposta

Por ordem de risco, não de esforço. Nenhuma foi implementada — este documento é diagnóstico.

**P0 — o que mente sobre si próprio**

1. **Corrigir o mitigante da §5.2-f** (`tecnica/17`) e reavaliar as quatro linhas §4.6-S/T/D/E que
   dele dependem. O catálogo de produção não está vazio.
2. **Inverter o default de `AOS_SANDBOX_DRIVER`**: ausente ⇒ recusa ou `ErrDriverUnavailable`, para
   que o `fake` deixe de ser o único driver que falha aberto. Acrescentá-lo à tabela de portas de
   produção de `deploy/server/README.md:576-583`.
3. **A2 — a janela de 2048.** Derivar o avanço do `stream_seq` do `$JS.ACK…` (`natsjs/conn.go:79`)
   em vez de `UltimoSeqDoSubject`, e acrescentar um teste com > 2048 eventos num só stream.
4. **A3 — estender o CRC ao cabeçalho de comprimento**, e um teste que corrompa o `len` (não só o
   payload) de um registo a meio.

**P1 — o que degrada sem avisar**

5. **A6 — `Healthy()`** passa a conhecer o estado do WAL e da ligação. Alimenta `/readyz`, o gauge
   `aos_eventstore_healthy` e o SLI `controlPlaneAvailable`; hoje os três mentem juntos.
6. **A5 — `desfazer` faz `w.w.Reset(w.f)`** e marca `envenenado`; e a costura `ficheiroWAL` passa a
   cobrir o caminho de escrita, porque hoje o `Write` de `ficheiroFalhado`
   (`durable_fsync_falhado_test.go:47`) é código morto.
7. **A4 — `Open` recusa** quando o WAL está sob posse, ou os subcomandos de inspecção passam a pedir
   posse partilhada. A convenção documentada («com o principal parado») não é uma restrição imposta.
8. **A7 — qualificar a atestação do seccomp.** `driver.go:73-78` e `lifecycle.go:153-156` dizem o que
   `doc.go:99-101` já diz correctamente; e ou omitir `SeccompProfileHash` quando
   `inst.Kind != DriverFake`, ou acrescentar ao evento quem impôs — a forma que `events.go:53-55` já
   usa para o `RootFSBaseDigest`.
9. **A8 — `Streams()` passa a devolver erro.** Muda a porta (`eventstore_port.go:43`), que é
   contrato; a perna crítica é `governance_restore.go:140`, do legal hold.

**P2 — o que é dívida documental ou endurecimento**

10. A9 (`E_NO_QUORUM` no backend JetStream, ou alargar `burndownTransitorio`), A10 (`tecnica/14:162`),
    A11 (`DEF-701`), A14 (`hasActiveEgressHook` + guard-test), e o `t.Errorf` de uma linha em
    `perda_test.go:206`.
11. **Gate de isolamento real sobre gVisor.** `deploy/server/README.md:117` diz que **não precisa de
    KVM** e o componente já está no repositório — o que torna insustentável, na forma geral, a defesa
    de que exigir isolamento real em CI não é razoável.
12. **Escrever a razão da assimetria** entre soberania e imutabilidade em `jetstream/store.go:170-182`:
    o append-only tem prova comportamental (delete e purge recusados, medidos), a soberania só tem
    prova declarativa. Três linhas fecham a lacuna de método.

---

## 7. Limites desta auditoria

### 7.1 Levantados

- Suites dos dois módulos executadas: `eventstore` (150 testes, **32 saltados**) e `sandbox`
  (233 testes, **0 saltados**), ambas verdes.
- Nó real levantado e conduzido: `driver.sh smoke` **9/9**, com pré-voo a confirmar binários frescos.
- As hipóteses A3, A4, A5, A6 e A13 foram **executadas** numa cópia isolada; `git status --short` em
  `C:\Jimy\AOS` ficou vazio.

### 7.2 Por levantar

- **Nada foi corrido contra um cluster NATS real.** `AOS_NATS_URL` não aparece em `scripts/`,
  `.github/`, `Makefile` nem `deploy/`; os 32 testes saltados cobrem CAS, append-only imposto pelo
  servidor, morte de nó, reconexão, soberania (seis testes) e PITR. A A2 é derivada de leitura
  verificada, **não de execução contra JetStream**.
- **Nada foi corrido contra Firecracker ou gVisor reais.** `fclive_test.go:1` está atrás de
  `//go:build fclive` e não chega a compilar; `fclive` não aparece em nenhum script de CI. A A7 é
  derivada de leitura do wire e do driver.
- **Não foi medido** se o servidor NATS recusa de facto um `STREAM.CREATE` sobre um stream existente
  com configuração divergente. A afirmação vive em dois comentários (`natsjs/jetstream.go:124-129`,
  `jetstream/store.go:99-101`) e nenhuma das oito adendas a mediu — é precisamente a classe de
  afirmação que a regra de método do handoff proíbe.
- **Não foram lidos** o `guest-agent` do Firecracker nem o guest do gVisor; a sanitização de caminhos
  dentro do guest não foi verificada.
- **Não foi verificado** se algum runner de CI privado corre `-tags fclive` ou define
  `AOS_NATS_URL`. O grep cobriu `.github/`, `scripts/`, `Makefile` e `deploy/`.
- `packages/substrate/bus/`, `otel-genai/` e `redaction/` estão fora do âmbito — são substrato, mas
  não são ES nem SBX.

---

## 8. Saldo

| Veredicto | Nº |
|---|---:|
| **Defeito** (sobrevive) | **12** |
| Deferimento declarado | 11 |
| Não-requisito | 8 |
| Refutada | 12 |
| **Achados novos, nascidos na refutação** | **2** |

Das doze hipóteses classificadas **CRÍTICA** ou **ALTA** pela passagem 1 no eixo do isolamento e do
egress, **duas** sobreviveram. Do eixo do Event Store, onde as hipóteses eram sobre o interior de um
caminho que corre em vez de sobre composição, sobreviveram **oito**.

Essa assimetria é o resultado mais transferível deste relatório. A passagem 1 gastou a maior parte da
sua energia a redescobrir buracos que a `tecnica/17` §5.2 já declarava com eixo — e falhou, quase
por inteiro, os defeitos **dentro** do código que corre todos os dias. Os dois achados centrais não
vieram de nenhuma lente: vieram de refutadores a verificar aquilo que as lentes tinham assumido ser
verdade.

E o defeito mais grave não foi encontrado a ler código de segurança. Foi encontrado a verificar uma
frase de mitigação num documento de auto-diagnóstico — um documento que é, aliás, dos melhores deste
repositório.

---

*Auditoria adversarial multiagente. Ver [Índice das análises](INDICE.md), [Índice Técnico](../tecnica/INDICE.md) e [Índice do Backlog](../specs/INDICE.md).*
