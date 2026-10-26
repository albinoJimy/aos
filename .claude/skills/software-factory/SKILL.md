---
name: software-factory
description: Fábrica de software agêntica para o AOS — admissão e triagem do pedido, seis linhas de produção (achado→ticket, ticket→PR, PR→merge, incidente→correcção, conhecimento→canon, release→produção), ciclo interno com estações que não se avaliam a si próprias, portões de CI roteados pelo eixo tocado, isolamento de escritores concorrentes, revisão adversarial com verificação do achado, métricas e realimentação. Usa esta skill sempre que a tarefa envolver refactoring, alterações transversais a vários packages ou módulos, funcionalidades complexas, migrações, mudanças de arquitectura, correcção de bugs com impacto em consumidores, integração entre componentes, ou risco elevado de regressão — mesmo que o utilizador nunca diga "multiagente", "plano", "subagentes" ou "fábrica". Também quando um ticket AOS-NNN tocar em mais de um package, quando fizer falta validação independente de trabalho já feito, quando o pedido combinar investigação e implementação, quando várias tarefas relacionadas chegarem juntas, quando houver trabalho em paralelo em mais do que um worktree, quando for preciso decidir que gates correr, ou quando se for abrir ticket, fundir PR, cortar release ou responder a um incidente de produção. Não a uses para uma edição pequena e isolada num só ficheiro.
---

# Fábrica de software agêntica

> **Um agente produz evidências; não produz a verdade.**
> **Uma fábrica produz evidência repetível; não produz confiança por decreto.**

Nenhum agente — nem tu — pode declarar sozinho que uma alteração está correcta, completa ou
segura. Toda a conclusão relevante assenta em evidência verificável e, quando o risco o
justificar, em validação independente de quem a implementou.

Esta skill existe porque o modo de falha dominante em bases de código grandes não é escrever
código errado: é **declarar-se pronto cedo demais**. Implementar não é concluir. Um teste verde
valida um cenário, não o sistema. E um agente que revê o próprio trabalho herda os próprios
pontos cegos.

O que a torna uma **fábrica**, e não um método, são cinco propriedades. Se uma delas não se
verificar no teu trabalho de hoje, não estás a correr a fábrica — estás a improvisar com
vocabulário emprestado.

```text
1. PORTA DE ENTRADA   todo o pedido é classificado e roteado antes de começar
2. LINHAS             cada trabalho recorrente tem artefacto de entrada e de saída
3. SEPARAÇÃO          nenhuma estação avalia o seu próprio produto
4. INSTRUMENTAÇÃO     a fábrica mede-se; o que não se mede não melhora
5. REALIMENTAÇÃO      um escape vira portão, não vira nota
```

## Proporcionalidade

O ciclo abaixo é o completo. Aplica as estações que o risco justifica e **diz quais saltaste** —
saltar em silêncio é o defeito que esta skill fecha; saltar com justificação é engenharia. Uma
correcção de typo num comentário não precisa de grafo de dependências.

Um sinal fiável de que precisas do ciclo inteiro: **não consegues nomear todos os consumidores
do que vais mudar**.

---

## 0. Admissão — a porta de entrada

Antes de qualquer coisa, classifica o pedido. A classe determina a linha, a linha determina os
portões, e os portões determinam o que conta como feito. Classificar leva trinta segundos e
evita correr a suite inteira para uma alteração documental, ou tratar um incidente de produção
como se fosse um ticket.

| O pedido é… | Linha | Autoridade |
|---|---|---|
| Hipótese, auditoria, «vê se isto está certo» | **L1** achado → ticket | eu |
| Ticket `AOS-NNN` com critérios de aceitação | **L2** ticket → PR | eu |
| PR aberto à espera de CI, conflito ou comentário | **L3** PR → merge | eu, com CI verde |
| Sintoma em produção, com output colado | **L4** incidente → correcção | o dono, no deploy |
| Decisão, contrato ou risco novo a registar | **L5** conhecimento → canon | eu |
| Cortar versão e pôr em produção | **L6** release → produção | **o dono** |
| Uma edição isolada num só ficheiro | nenhuma — não uses esta skill | eu |

**Antes de abrir ticket ou editar `scripts/ci/`, regista a sessão.** Este repositório é
trabalhado em vários worktrees ao mesmo tempo, em ramos que não se vêem uns aos outros:

```bash
python scripts/ci/sessoes.py registar --tarefa "..." --tickets AOS-NNN --ficheiros <path>
python scripts/ci/sessoes.py ver        # colisões: ticket, ficheiro, base divergente
```

O número que o `reservar` devolve é seguro entre sessões, mas pode não caber **neste** ramo: a
RTM exige a gama contígua localmente. Confirma antes de escrever o ticket — a verificação e o
que fazer quando não cabe estão na L1 da referência abaixo.

As seis linhas, com artefacto de entrada, artefacto de saída, portões e a tabela de autoridade
completa — o que posso fazer sozinho e onde tenho de parar e pedir — estão em
[`references/admissao-e-linhas.md`](references/admissao-e-linhas.md).

---

## 1. O ciclo interno

Cada linha corre este ciclo. A diferença entre linhas está em onde entra, onde sai e que
portões se aplicam — não no rigor.

```text
DISCOVERY → IMPACTO → DECOMPOSIÇÃO → PARALELISMO → IMPLEMENTAÇÃO
   → SELF-VALIDATION → REVISÃO ADVERSARIAL → INTEGRAÇÃO
   → VALIDAÇÃO DE SISTEMA → AUDITORIA DE COMPLETUDE → CONHECIMENTO → RELATÓRIO
```

Cada estação tem um papel **e uma proibição**. A proibição é o que produz o valor: um Explorer
que implementa deixou de ser uma segunda opinião e passou a ser mais uma fonte de código por
rever.

| Estação | Papel | Proibido | Saída verificável |
|---|---|---|---|
| Discovery | `Explore` | implementar | as lacunas, listadas |
| Impacto e desenho | `Plan` | mexer em código para testar hipóteses | contratos, riscos, trade-offs |
| Implementação | `general-purpose` | alargar o escopo em silêncio | diff dentro de `FILES_IN_SCOPE` |
| Testes | agente distinto do autor | derivar casos do diff | o nome da função de teste |
| Revisão adversarial | agente que não vê o meu raciocínio | concordar sem ter procurado | achados `CONFIRMADO` / `PLAUSÍVEL` |
| Integração | eu | aceitar alteração inesperada | changeset auditado |

> **Nenhuma estação avalia o seu próprio produto.** É a regra que não tem excepção de
> conveniência. Quando o risco não justificar um reviewer separado, diz que a revisão foi
> própria — não finjas que houve independência.

### 1.1 Discovery — antes de tocar em código

Não modifiques código nesta fase. O objectivo é saber o que existe, não mudá-lo. Delega a
varredura a um subagente **`Explore`**: é read-only, devolve a conclusão em vez do despejo de
ficheiros, e mantém o teu contexto livre para a decisão.

```text
RESULTADO DA DISCOVERY
Objectivo:
Componentes envolvidos:
Pontos de entrada:
Dependências (quem chama, quem é chamado):
Contratos afectados:
Testes existentes que cobrem isto:
Riscos:
Hipóteses (ainda por validar):
Lacunas de informação:
```

**As lacunas são a parte que importa.** Uma discovery que não lista nada em falta ou não
procurou, ou está a esconder incerteza.

Quando o alvo é um pacote que ninguém da sessão conhece, junta
`Skill(skill: "ponytail:ponytail-audit", args: "packages/<modulo>")` — é read-only e o que sai
dela entra como **riscos e hipóteses**, nunca como trabalho a fazer
([`references/ponytail.md`](references/ponytail.md)).

#### Fontes de verdade neste repositório, por ordem

Quando duas fontes discordam, **não escolhas em silêncio**. Regista o conflito no formato de
[`references/decomposicao.md`](references/decomposicao.md) e resolve-o contra a ordem abaixo.

| Ordem | Fonte |
|---|---|
| 1 | `specs/00_AOS_Carta.md` — decisões congeladas; só muda por emenda |
| 2 | ADRs em vigor (`docs/adr/`) — não contradizer sem ADR de supersessão |
| 3 | `_BRIEF.md` e `specs/01_Engineering_Standards_e_Handoff.md` |
| 4 | O ticket `AOS-NNN` no `specs/EPIC-XX_*.md` e os critérios de aceitação |
| 5 | `tecnica/12_Contratos_de_Interface.md` e os contratos em código |
| 6 | Testes existentes — descrevem o comportamento que alguém decidiu preservar |
| 7 | Código |
| 8 | Evidência obtida em execução (gates, driver, output real) |

O código é fonte de verdade sobre o que **acontece**, não sobre o que **devia** acontecer. Um
teste verde sobre comportamento errado continua a ser comportamento errado.

### 1.2 Análise de impacto

Não julgues o tamanho pela contagem de ficheiros. Uma linha num contrato partilhado é maior do
que trezentas linhas num pacote-folha.

```text
DIRECTO        ficheiros que vais alterar
INDIRECTO      consumidores, integrações, composition-root, APIs, persistência, config
COMPORTAMENTAL o que muda / o que TEM de ficar igual
TESTES         testes afectados, testes novos necessários, regressões plausíveis
OPERACIONAL    deployment, observabilidade, performance, segurança, compatibilidade
```

Neste repositório o impacto indirecto tem três sítios que escapam quase sempre:

- **`packages/integration/`** — o composition-root. Uma porta nova ou uma assinatura mudada
  aparece aqui antes de aparecer em qualquer outro lado.
- **`packages/cmd/aos/`** — o wiring do nó, os banners de postura de arranque e a superfície de
  env vars. Se mudares o que um subsistema exige para compor, o banner que o declara passa a
  mentir.
- **Invariantes de fronteira** (AGENTS.md §3) — `control-plane → kernel → platform/substrate`.
  O RM é o único caminho para tool calls. Violar isto avermelha `layer-lint.sh`, mas
  descobri-lo no gate é tarde: verifica-o no desenho.

O eixo que identificares aqui é o que escolhe os portões na estação 1.7. Impacto e portões são
a mesma pergunta feita duas vezes.

### 1.3 Decomposição e paralelismo

Divide em tasks pequenas o suficiente para um agente compreender, completas o suficiente para
produzir um resultado **verificável por si só**. O template da task, os estados do grafo e o
formato de estado de execução estão em
[`references/decomposicao.md`](references/decomposicao.md).

A `VALIDACAO` de cada task é **um comando**, não uma frase. Um gate, um teste nomeado, um
`driver.sh`. Se não consegues escrever o comando, ainda não sabes o que estás a construir.

> Paralelismo é permitido pela **independência**, não pela conveniência.

```text
[ ] Não alteram os mesmos ficheiros
[ ] Não dependem de alteração ainda não concluída
[ ] Não modificam o mesmo contrato ou interface
[ ] Não tocam no mesmo estado partilhado (composition-root, env surface, tabela de rotas)
[ ] Não colidem semanticamente (duas soluções válidas e incompatíveis para o mesmo problema)
[ ] Cada uma tem critério de validação próprio
```

Em dúvida, sequencial. Uma corrida perdida custa uma tarde; um conflito semântico integrado
custa a confiança em tudo o que veio a seguir.

**A costura natural de paralelismo aqui são os módulos Go.** Duas tasks em módulos sem aresta
`replace` entre si são quase sempre independentes; duas tasks no mesmo módulo quase nunca são.

Quando houver mais do que um agente a **escrever**, lê
[`references/isolamento.md`](references/isolamento.md) antes de os lançar. Worktree por
escritor, registo de sessão, e os recursos que são partilhados sem o parecerem — o driver do
smoke e o `ci-selftest` entre eles.

### 1.4 Implementação

Cada agente recebe um contrato explícito — papel, âmbito, o que pode e não pode assumir, o que
tem de devolver. O formato está em
[`references/contratos-de-agente.md`](references/contratos-de-agente.md).

Duas regras que valem mais do que o resto desta secção:

**Não inventes.** APIs, métodos, campos, endpoints, tabelas, comportamentos, requisitos,
dependências, configurações — se não está confirmado, é `UNKNOWN`. Se precisares de avançar
sobre uma suposição, torna-a visível:

```text
HIPÓTESE
- Hipótese:
- Evidência que a sustenta:
- Confiança:
- Como validar:
```

Uma hipótese declarada é engenharia. Uma hipótese silenciosa é o defeito que ninguém encontra
até estar em produção. E uma negativa trazida por um subagente — «isso não existe no repo» — é
uma afirmação como as outras: confirma-a com comando próprio antes de a deixares decidir
escopo.

**Não alargues o escopo em silêncio.** Nada de refactoring oportunista, limpeza não
relacionada, ou mudança cosmética. Uma alteração fora de `FILES_IN_SCOPE` só passa se for
tecnicamente necessária, justificada e registada. Bug fora de escopo abre ticket novo
(AGENTS.md §5) — o que o põe na **L1**, não neste PR.

**Implementa no modo ponytail** (`Skill(skill: "ponytail:ponytail", args: "full")`): a mudança
mais pequena que fecha a tarefa **inteira**, incluindo chamadores, testes e fixtures que ela
parte. Com uma fronteira que não se negocia — *mínimo* nunca é retirar uma barreira. Um gate, um
fail-closed, uma verificação de assinatura, identidade ou taint não são código a mais: são o
produto. As três regras de divergência estão em
[`references/ponytail.md`](references/ponytail.md).

### 1.5 Self-validation

Valida o teu próprio trabalho sabendo que isto **não substitui** a revisão independente. Serve
para não desperdiçares um reviewer com defeitos que apanharias sozinho.

```bash
bash scripts/ci/run.sh build lint layer-lint     # subconjunto, não a suite toda
cd packages/<modulo> && go test -race -count=1 ./...
```

```text
[ ] Compila
[ ] Testes do módulo passam com -race
[ ] Critérios de aceitação do ticket atendidos, um a um
[ ] Contratos preservados (ou a quebra é deliberada e registada)
[ ] Nenhuma alteração acidental no diff — lê o `git diff`, não confies na memória
[ ] Sem imports órfãos, sem TODOs novos, sem código morto
[ ] Tratamento de erro adequado; fail-closed onde a governação o exige
[ ] Casos extremos considerados
[ ] Cobertura não regrediu
```

**O lint corre-se depois da última edição.** Correr antes e editar a seguir é como se não se
tivesse corrido — e já partiu o CI três vezes na mesma série.

`make` não está no PATH em Git Bash aqui; chama `bash scripts/ci/<gate>.sh` ou
`bash scripts/ci/run.sh <gate>...` directamente.

### 1.6 Revisão adversarial, com verificação

Esta é a estação que dá substância ao princípio central, e a primeira a ser sacrificada quando
há pressa. Não a sacrifiques.

A revisão **procura razões para a solução estar errada**. Não confirma que está certa.

```
Skill(skill: "code-review", args: "high")
Skill(skill: "security-review")        # identidade, política, segredos, sandbox, auditoria, read-path
Skill(skill: "ponytail:ponytail-review")   # + o eixo do que não devia existir
```

As três não se substituem: o `code-review` procura defeitos, o `ponytail-review` lê também o
código ligado ao diff e acrescenta o que se pode cortar. Um achado de «cortar» que remova uma
barreira de governação **rejeita-se** — e passa pela mesma verificação que os outros.

Para revisão verdadeiramente independente, lança um subagente `general-purpose` que só vê o
diff e os requisitos, **nunca as tuas justificações**. O guião está em
[`references/revisao-adversarial.md`](references/revisao-adversarial.md).

A adição da fábrica é o que vem **depois** do achado. Um reviewer instruído a encontrar
problemas encontra problemas, haja-os ou não; e neste repositório cerca de **dois terços das
hipóteses adversariais morrem** contra o registo de deferimentos e o canon. Por isso nenhum
achado gera trabalho antes de passar pela verificação:

```text
ACHADO → verificar contra o código e o canon → CONFIRMADO | PLAUSÍVEL | MORTO
```

Só o `CONFIRMADO` entra no portão de integração. O `PLAUSÍVEL` vai para o relatório com o que
falta para o decidir. O `MORTO` regista-se com a evidência que o matou — senão volta na próxima
auditoria e paga-se outra vez.

### 1.7 Integração e validação de sistema

Antes de integrar, audita o changeset — o que mudou face ao que **era suposto** mudar:

```bash
git status --short && git diff --stat
```

Qualquer ficheiro que não esperavas ver é para investigar, não para aceitar. O portão de
integração está em [`references/encerramento.md`](references/encerramento.md).

Depois corre **os portões do eixo que tocaste**, não os que te lembrares:

```bash
bash scripts/ci/run.sh test apex policy-test security
```

A tabela completa eixo → portões, o custo de cada um, a ordem fail-fast e o que correr em
background está em [`references/portoes-e-velocidade.md`](references/portoes-e-velocidade.md).
O agregador aceita subconjunto e sai `0` verde, `1` vermelho e **`3` verde parcial** — uma
etapa saltada nunca conta como verde.

**E corre o sistema, não só os testes.** Uma suite verde sobre um nó que não arranca é o caso
exemplar de teste a passar pela razão errada:

```bash
bash .claude/skills/run-aos/driver.sh smoke
```

A skill [`run-aos`](../run-aos/SKILL.md) levanta o nó composto e prova os passos
ponta-a-ponta. Para mudanças no wiring, nos banners de arranque ou na superfície HTTP, é a
única evidência que conta.

### 1.8 Auditoria de completude e encerramento

Compara o **pedido original** com o que existe agora — requisitos, tasks, implementação,
testes, validação — e procura o que se perdeu pelo caminho. O formato da auditoria, o quality
gate e o relatório estão em [`references/encerramento.md`](references/encerramento.md).

Toda a afirmação relevante precisa de evidência nomeada:

```text
AFIRMAÇÃO: "O endpoint mantém compatibilidade."
EVIDÊNCIA:  TestAOS263_CLIDecisaoExigeOsCamposQueAmarram cobre o corpo enviado;
            tecnica/12_Contratos_de_Interface.md inalterado;
            cmd/aos-orq compila e o smoke do run-aos passa.
```

Sem evidência, escreve `NÃO VERIFICADO`. É informação útil. «Provavelmente funciona» não é.

Antes de fechar, faz o livro da dívida com
`Skill(skill: "ponytail:ponytail-debt", args: "DEFERIDO")`, apontado aos marcadores deste
repositório e não ao `shortcut:` do ponytail — um atalho aqui é um deferimento registado com
eixo e ticket, que o portão `deferrals` conta, ou um ticket novo.

---

## 2. Velocidade

A fábrica é rápida por desenho, não por cortes. Seis regras, todas sobre fazer o mesmo trabalho
em menos tempo de relógio — nenhuma sobre fazer menos trabalho.

1. **Fail-fast, e por eixo.** `build` → `lint` → teste do módulo → portões do eixo. Nunca a
   suite inteira primeiro: um erro de compilação não precisa de vinte minutos de `nats` para se
   revelar.
2. **O lint corre-se no fim.** Depois da última edição, não a meio.
3. **Portões lentos em background.** O `test` e o `nats` são os dois caros; lança-os em
   background e continua. Não faças polling — a notificação chega.
4. **Fan-out onde os módulos o permitem.** Tasks em módulos sem aresta `replace` correm em
   paralelo, cada escritor no seu worktree. Lança os subagentes **na mesma mensagem**, senão
   correm em série e pagas a latência sem ganhar nada.
5. **Não refaças a discovery.** `KNOWN_FACTS` no contrato do agente é o campo que mais poupa:
   sem ele cada subagente redescobre o mesmo código e chega a conclusões ligeiramente
   diferentes — o que produz conflitos que não existiam.
6. **Decide e avança.** Quando tens informação para agir, age. Não re-derives factos já
   estabelecidos nem reabras decisões já tomadas.

**O que não se acelera:** o portão humano, a revisão adversarial e o smoke. Velocidade só vale
enquanto preserva confiança — e a evidência da indústria é consistente neste ponto: com agentes
o *throughput* sobe e a estabilidade de entrega desce. A fábrica existe para que as duas subam
juntas.

---

## 3. Métricas e realimentação

Uma fábrica que não se mede não melhora — repete. Seis indicadores, cada um com o seu
instrumento nomeado:

```text
artefactos que já existem
  VERDE À PRIMEIRA    gh pr checks <n>, primeira corrida
  ETAPAS SALTADAS     AOS_SKIPPED_STEP no veredicto dos gates — exit 3
  TEMPO ATÉ MERGE     da abertura do PR ao squash

ledger da fábrica — `sessoes.py medir` / `sessoes.py indicadores`
  CALIBRAÇÃO          achados CONFIRMADOS / achados levantados — percentagem
  RETRABALHO          achados CONFIRMADOS pós-feito, POR ENTREGA — não é percentagem
  ESCAPES POR PORTÃO  achados com o portão que os devia ter apanhado
```

O ledger não é um gate e depende de alguém o chamar; o que muda é que o número acumula e
lê-se, em vez de desaparecer no fim da sessão. Com zero amostras ele responde `sem amostras`,
não `0%` — e o retrabalho conta só o `CONFIRMADO`, porque um achado que morreu não gerou
trabalho nenhum.

E a regra que fecha o ciclo:

> **Um escape que um portão podia ter apanhado vira portão.** Não vira aviso, não vira nota de
> rodapé, não vira «ter cuidado da próxima vez».

É a tese do AOS aplicada à própria fábrica: tornar a falha arquitecturalmente impossível, não
apenas desencorajada. O detalhe — onde cada métrica se lê, e os três destinos legítimos de um
escape — está em
[`references/metricas-e-realimentacao.md`](references/metricas-e-realimentacao.md).

---

## Convenções não negociáveis deste repositório

- **PT-PT** em documentação e comentários novos. Termos técnicos consagrados ficam em inglês
  (`reference monitor`, `taint`, `durable execution`, `replay`, `eval-gate`).
- Conventional Commits com ticket; branch `feature/AOS-NNN-<slug>`.
- Nada de binários, cobertura ou artefactos de build no commit.
- Nenhum segredo em texto claro — env var, Vault, ou ficheiro montado em runtime.
- Testes deterministas: usa as fixtures do `packages/testkit` (relógios manuais, ids
  sequenciais, Event Store in-memory) em vez de `time.Now()`, `rand` ou UUID em asserções.
- Numeração de tickets por `sessoes.py reservar`, nunca a olho.

## Os doze princípios

1. **Não inventar** — sem evidência, é desconhecido.
2. **Não assumir completude** — implementar não é concluir.
3. **Não confundir teste com prova** — um teste verde valida um cenário.
4. **Não paralelizar dependências** — paralelismo inseguro multiplica o risco.
5. **Não alargar escopo em silêncio** — toda a alteração extra precisa de justificação.
6. **Não confiar cegamente noutro agente** — resultados verificam-se, incluindo os teus.
7. **Não esconder incerteza** — a incerteza declarada é barata; a escondida não.
8. **Não trocar qualidade por velocidade** — velocidade só vale enquanto preserva confiança.
9. **Não concluir sem auditoria** — execução complexa passa pelo completeness audit.
10. **Preservar conhecimento** — o que se descobriu tem de sobreviver a esta execução.
11. **Um escape vira portão** — o que passou ao lado de uma verificação muda a verificação.
12. **A fábrica mede-se** — sem indicador, não há melhoria, há repetição.

## Referências

| Ficheiro | Lê quando… |
|---|---|
| [`references/admissao-e-linhas.md`](references/admissao-e-linhas.md) | Estás a classificar o pedido, a escolher a linha, ou precisas de saber onde parar e pedir ao dono |
| [`references/portoes-e-velocidade.md`](references/portoes-e-velocidade.md) | Vais decidir que portões correr, em que ordem, e o que mandar para background |
| [`references/isolamento.md`](references/isolamento.md) | Vai haver mais do que um agente a escrever, ou trabalhas num worktree |
| [`references/decomposicao.md`](references/decomposicao.md) | Vais partir a tarefa, modelar dependências, resolver um conflito de fontes ou manter estado de execução |
| [`references/contratos-de-agente.md`](references/contratos-de-agente.md) | Vais lançar um subagente ou definir o que ele devolve |
| [`references/revisao-adversarial.md`](references/revisao-adversarial.md) | Estás a rever — o teu trabalho ou o de outro agente |
| [`references/ponytail.md`](references/ponytail.md) | Vais usar as skills do `ponytail` numa estação, ou recebeste um achado de «cortar» |
| [`references/encerramento.md`](references/encerramento.md) | Estás a integrar, auditar completude ou escrever o relatório final |
| [`references/metricas-e-realimentacao.md`](references/metricas-e-realimentacao.md) | Fechaste uma linha e vais converter o que aprendeste em portão |
