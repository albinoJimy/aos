# Auditoria das afirmações produzidas na sessão de AOS-281 — e do método que as produziu

> **Porque existe.** Ao começar a implementar o `AOS-287` descobri que o defeito que ele
> descreve **não existe como escrito**: já fora corrigido pelo `AOS-256`. O dono pediu,
> com razão, que antes de reescrever o ticket eu percebesse se o **método** está errado —
> porque um método que produz tickets falsos produz retrabalho em ciclo.
>
> Este documento não defende nada. Audita **todas** as afirmações materiais que produzi
> nesta sessão, diz quais sobrevivem e quais não, e isola a regra que separa umas das
> outras.
>
> **Data:** 2026-08-31 · **HEAD:** `b02425a`

---

## 1. O erro, na sua forma exacta

Inferi **«a propriedade P está ausente»** a partir de **«o mecanismo M não tem chamadores»**.

Concretamente, no orçamento:

```
observado:  budget.WithEmitter e budget.Rebuild não têm chamadores de produção
concluí:    o orçamento não sobrevive a um reinício
```

A inferência é inválida porque **P pode ser fornecida por M′**. E era: o `AOS-256`
resolve-o por outra via — `RunBudget.limiteParaIncarnacao` lê o consumo do **ledger de
turnos** (durável, chaveado por `run_id`, sobrevive à retoma) e faz o nó do run nascer com
`tecto − já consumido`. Está ligado em produção (`bootstrap.go:1609`).

Nunca perguntei **«de que outra maneira poderia esta propriedade estar satisfeita?»**.

### 1.1 O agravante: re-verifiquei um relatório antigo no eixo errado

Apoiei-me no `desafio-A1` (2026-08-08), que dizia o mesmo. Verifiquei que a afirmação dele
**continuava verdadeira** — o `Rebuild` continua sem chamadores — e tomei isso como
confirmação.

Mas o `AOS-256` é **posterior** ao relatório. O que eu tinha de re-verificar não era o
facto que o relatório afirmava (o mecanismo), era a **conclusão** (a propriedade). Confirmei
o eixo onde ele continuava certo e não toquei no eixo onde tinha ficado obsoleto.

> **Re-verificar um relatório antigo no eixo em que ele continua certo não é verificação —
> é confirmação de viés com passos extra.**

---

## 2. O que separa as afirmações que sobreviveram das que caíram

Auditei as onze afirmações materiais da sessão. O padrão é limpo e não tem excepções:

| # | Afirmação | Como a estabeleci | Veredicto |
|---|---|---|---|
| 1 | O Event Store não arbitra entre processos | **MEDI** (dois `Open`, dois `Claim`, ambos ganham) | ✅ sobrevive |
| 2 | O handoff sem janela é possível com o mecanismo actual | **MEDI** (teste com dois processos) | ✅ sobrevive |
| 3 | O builder cego admite arestas cegas | **MEDI** (mutação: o teste cai) | ✅ sobrevive |
| 4 | O guard de posse funciona e liberta na morte | **MEDI** (dois processos, `kill`) | ✅ sobrevive |
| 5 | O SCH não escreve ciclo de vida | **MEDI** (o stream do run não cresce numa passagem) | ✅ sobrevive |
| 6 | O oráculo por omissão neutraliza o verificador | **MEDI** (não-vacuidade reproduz o defeito) | ✅ sobrevive |
| 7 | Os laços de serviço não têm eleição de líder | **LI A DECLARAÇÃO** no código (`«basta para um job de processo único»`) | ✅ sobrevive |
| 8 | A hash-chain pode ter problema multi-escritor | **DECLAREI NÃO TER VERIFICADO**; o AC1 do ticket é descobrir | ✅ honesta |
| 9 | O orçamento não sobrevive a reinício | **INFERI** de ausência de chamadores | ❌ **FALSA** |
| 10 | N réplicas ⇒ N tectos de árvore | **INFERI** de «contadores em memória» | ❌ **FALSA** (§3) |
| 11 | O `AOS-282` deve cindir-se em v1 + v1.1 | **DERIVEI** de (9) e (10) | ❌ **cai com elas** |

**A regra sai sozinha:** tudo o que **medi** ou **li declarado** sobreviveu. Tudo o que
**inferi de uma ausência** caiu. Sem excepções nos dois sentidos.

---

## 3. O segundo erro, que ainda não tinha sido apanhado

A afirmação 10 — «com 3 réplicas o tecto por árvore é o triplo» — é **falsa por três
razões independentes**, e qualquer uma bastava:

**(a) Não existe tecto de árvore para multiplicar.** A raiz é deliberadamente **ilimitada**,
e o código diz porquê:

> A raiz é DELIBERADAMENTE ILIMITADA em tokens: o tecto da v1 é por-run […] Uma raiz finita
> faria o run B ser negado porque o run A gastou, que é precisamente a partilha de tecto que
> **D-A1.3 proíbe**.

**(b) O tecto por-run não é multiplicado por réplicas.** Um run é possuído por **exactamente
uma** réplica — é a invariante do ADR-023, que eu próprio entreguei nesta sessão. O nó de
orçamento do run R existe só na réplica que o serve.

**(c) O que o `AOS-282` mandava construir é PROIBIDO.** Um tecto partilhado entre runs é o
que a decisão **D-A1.3** recusa, e há **guard-test a selá-lo**
(`TestAOS256_DoisRunsSequenciaisNaoPartilhamTecto`). Implementá-lo partiria esse teste.

Ou seja: não escrevi só um ticket com premissa errada — escrevi um ticket que mandava
**violar uma decisão selada**. E fi-lo enquanto avisava, em três outros tickets, contra
construir um segundo mecanismo que divergisse do primeiro.

---

## 4. O que é, então, verdade sobre o orçamento

Estabelecido por leitura do código no HEAD, não por inferência:

| propriedade | estado |
|---|---|
| Tecto **por-run**, não por árvore nem global | ✅ por desenho (D-A1.3, com guard-test) |
| Sobrevive a reinício / re-incarnação, para **turnos de modelo** | ✅ `AOS-256`, ligado em produção |
| Sobrevive para **tool calls** | ❌ **é a fuga que resta** |
| Multiplicado por réplicas | ❌ não se aplica: um run, um dono (ADR-023) |

E o alcance parcial **já está declarado no código**, em dois sítios, sem eufemismo:

> o ledger conta turnos de **modelo** e só eles. As tool calls reservam do mesmo nó e **não
> entram no ledger**, pelo que a fuga **ENCOLHE** — do tecto inteiro por incarnação para o
> consumo de tool calls por incarnação — em vez de fechar.

**O defeito real é esse, e só esse:** o consumo de tool calls não é contabilizado de forma
durável, pelo que cada incarnação o esquece. Um run que escale/retome N vezes pode gastar
até `tecto + N × (tool calls por incarnação)`.

É muito mais estreito do que escrevi, e — importante — **o remédio não é o
`Rebuild`/`WithEmitter`**. Essa é uma via **alternativa não escolhida**: o `AOS-256`
rejeitou-a por escrito e escolheu o ledger. Ligá-la agora criaria duas contabilidades do
mesmo tecto.

---

## 5. Consequências: o que tem de ser corrigido

| artefacto | estado | acção |
|---|---|---|
| `AOS-287` (EPIC-01) | premissa falsa | **reescrever** para a fuga real (tool calls duráveis), com nota de que `Rebuild`/`WithEmitter` são via **não escolhida** |
| `AOS-282` (EPIC-10) | premissa falsa **e** mandaria violar D-A1.3 | **fechar como INVÁLIDO**, com a razão registada |
| Carta, emenda **1.4** | a decisão v1.1 mantém-se; a **justificação** cita a cisão como «o valor da emenda» | **corrigir a justificação** — a decisão não depende dela |
| `analise-v1-single-host…` §3.1 | corrigido uma vez, ainda errado | **corrigir de novo**, com este documento como base |
| `desafio-A1` | correcto em 2026-08-08, obsoleto desde `AOS-256` | **anotar como datado**, não apagar |
| `AOS-283`, `AOS-284` | sobrevivem à auditoria | manter |
| `AOS-285`, `AOS-286`, `AOS-281`, `DEF-282` | medidos | manter |

**A decisão v1.1 não cai.** Ela assentava em várias pernas — o `AOS-100` ser o bloqueador
central (medido), a maquinaria distribuída já existir (lido), a ausência de gatilho de HA
nomeado. A cisão do `AOS-282` era **uma** das justificações e é a que cai.

---

## 6. A regra que impede a repetição

Não é «ter mais cuidado». É uma regra com forma verificável:

> **Uma afirmação de que uma propriedade está AUSENTE só vale se for MEDIDA, ou se a
> ausência estiver DECLARADA no código. Nunca se infere de um mecanismo não ter chamadores.**

Operacionalmente, antes de escrever «X não acontece» num ticket:

1. **Medir** — escrever o teste que falha se X acontecer. É o que fiz no `DEF-282` e é a
   razão de ele ter sobrevivido a esta auditoria.
2. Se não der para medir, **procurar M′**: perguntar «de que outra maneira poderia esta
   propriedade estar satisfeita?» e procurar por *propriedade*, não por *nome de função*.
   Aqui teria bastado procurar `runID` + `consumo`/`limite` em vez de `Rebuild`.
3. Se um relatório anterior concordar, **datá-lo contra o HEAD**: procurar o que mudou
   *desde* ele. Um relatório que concorda é uma hipótese a testar, não corroboração.
4. Se nenhuma das três der, **declarar não-verificado** e fazer disso o AC1 — foi o que
   fiz no `AOS-284`, e é por isso que ele é o único dos três que não precisa de correcção.

### Porque isto não é auto-flagelação

O passo 4 já estava no meu repertório — usei-o no `AOS-284` na mesma sessão em que falhei
no `AOS-287`. A diferença entre os dois não foi cuidado: foi eu **saber que não sabia** num
caso e **julgar que sabia** no outro. A regra existe para transformar essa distinção em
procedimento, em vez de a deixar à sorte da intuição.

---

## 7. Estamos no caminho certo?

Resposta directa: **sim quanto ao que foi medido, não quanto ao que foi inferido** — e a
proporção é conhecível, não uma impressão.

Das onze afirmações, **oito sobrevivem** e todas as oito foram medidas ou lidas como
declaração. As três que caem partilham a mesma causa e estão todas na mesma família
(orçamento). **Nenhum código entregue e mergido depende de uma afirmação falsa** — o
`AOS-281`, o `AOS-285` e o `AOS-286` assentam em medições.

O risco de ciclo é real e vem de outro sítio: **os tickets falsos que ficaram no backlog**.
Um ticket com premissa errada não custa quando se escreve; custa quando alguém o executa
seis meses depois sem repetir a verificação. É por isso que o `AOS-282` tem de ser
**fechado** e não silenciosamente reescrito — o registo de que ele foi inválido é o que
impede que volte.
