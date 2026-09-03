---
name: agentic-engineering
description: Framework de engenharia multiagente para trabalho complexo — discovery antes de implementar, decomposição, paralelismo só quando é seguro, revisão adversarial independente e auditoria de completude, ancorado nos gates e convenções do AOS. Usa esta skill sempre que a tarefa envolver refactoring, alterações transversais a vários packages ou módulos, funcionalidades complexas, migrações, mudanças de arquitectura, correcção de bugs com impacto em consumidores, integração entre componentes, ou risco elevado de regressão — mesmo que o utilizador nunca diga "multiagente", "plano" ou "subagentes". Também quando um ticket AOS-NNN tocar em mais de um package, quando fizer falta validação independente de trabalho já feito, quando o pedido combinar investigação e implementação, ou quando várias tarefas relacionadas chegarem juntas. Não a uses para uma edição pequena e isolada num só ficheiro.
---

# Engenharia agêntica

> **Um agente produz evidências; não produz a verdade.**

Nenhum agente — nem tu — pode declarar sozinho que uma alteração está correcta, completa ou
segura. Toda a conclusão relevante assenta em evidência verificável e, quando o risco o
justificar, em validação independente de quem a implementou.

Esta skill existe porque o modo de falha dominante em bases de código grandes não é escrever
código errado: é **declarar-se pronto cedo demais**. Implementar não é concluir. Um teste verde
valida um cenário, não o sistema. E um agente que revê o próprio trabalho herda os próprios
pontos cegos.

## Proporcionalidade

O pipeline abaixo é o ciclo completo. Aplica as fases que o risco justifica e diz quais
saltaste — saltar em silêncio é o defeito que esta skill fecha, saltar com justificação é
engenharia. Uma correcção de typo num comentário não precisa de grafo de dependências.

Um sinal fiável de que precisas do ciclo inteiro: **não consegues nomear todos os consumidores
do que vais mudar**.

```text
DISCOVERY → IMPACTO → DECOMPOSIÇÃO → PARALELISMO → IMPLEMENTAÇÃO
   → SELF-VALIDATION → REVISÃO ADVERSARIAL → INTEGRAÇÃO
   → VALIDAÇÃO DE SISTEMA → AUDITORIA DE COMPLETUDE → CONHECIMENTO → RELATÓRIO
```

---

## 1. Discovery — antes de tocar em código

Não modifiques código nesta fase. O objectivo é saber o que existe, não mudá-lo.

Delega a varredura a um subagente **`Explore`**: é read-only, devolve a conclusão em vez do
despejo de ficheiros, e mantém o teu contexto livre para a decisão. Uma varredura larga feita
inline enche a janela com excertos que não vais reler.

```
Agent(subagent_type: "Explore", prompt: "...")
```

O que tem de sair de aqui, explicitamente:

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

### Fontes de verdade neste repositório, por ordem

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

---

## 2. Análise de impacto

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

---

## 3. Decomposição e grafo de dependências

Divide em tasks pequenas o suficiente para um agente compreender, completas o suficiente para
produzir um resultado **verificável por si só**. O template completo da task, os estados do
grafo e o formato de estado de execução estão em
[`references/decomposicao.md`](references/decomposicao.md) — lê-o quando a tarefa tiver mais de
três unidades ou dependências não-triviais.

Para desenhar a decomposição de algo verdadeiramente arquitectural, delega a um subagente
**`Plan`**. Ele não escreve código; devolve o plano, os ficheiros críticos e os trade-offs.

### Paralelismo

> Paralelismo é permitido pela **independência**, não pela conveniência.

Antes de correr tasks em paralelo, todas as caixas têm de fechar:

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

**A costura natural de paralelismo aqui são os módulos Go.** Há 49 `go.mod` em `packages/`,
ligados por `replace` path-local. Duas tasks em módulos sem aresta `replace` entre si são quase
sempre independentes. Duas tasks no mesmo módulo quase nunca são. Confirma com:

```bash
grep -rn "replace github.com/aos-ref" packages/<modulo>/go.mod
```

Quando lançares subagentes em paralelo, manda-os **na mesma mensagem** — senão correm em série
e pagas a latência sem ganhar nada.

---

## 4. Implementação

Cada agente recebe um contrato explícito — papel, âmbito, o que pode e não pode assumir, o que
tem de devolver. O formato está em
[`references/contratos-de-agente.md`](references/contratos-de-agente.md), com a definição de
cada papel (Explorer, Architect, Implementer, Tester, Reviewer, Integrator) e o mapeamento para
os `subagent_type` disponíveis.

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
até estar em produção.

**Não alargues o escopo em silêncio.** Nada de refactoring oportunista, limpeza não
relacionada, ou mudança cosmética. Uma alteração fora de `FILES_IN_SCOPE` só passa se for
tecnicamente necessária, justificada e registada. Bug fora de escopo abre ticket novo
(AGENTS.md §5). Se encontrares um, usa `mcp__ccd_session__spawn_task` para o marcar sem
descarrilar o trabalho actual.

---

## 5. Self-validation

Depois de implementar, valida o teu próprio trabalho — sabendo que isto **não substitui** a
revisão independente. Serve para não desperdiçares um reviewer com defeitos que apanharias
sozinho.

```bash
bash scripts/ci/build.sh                      # todos os módulos compilam
bash scripts/ci/lint.sh                       # gofmt, vet, staticcheck
bash scripts/ci/layer-lint.sh                 # fronteiras de camadas (ADR-019)
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

`make` não está no PATH em Git Bash aqui; chama `bash scripts/ci/<gate>.sh` directamente.

---

## 6. Revisão adversarial

Esta é a fase que dá substância ao princípio central, e é a primeira a ser sacrificada quando
há pressa. Não a sacrifiques.

A revisão **procura razões para a solução estar errada**. Não confirma que está certa. Um
reviewer que só relê o diff e concorda não produziu evidência nenhuma.

Usa a skill de revisão do repositório, que já sabe olhar para o diff:

```
Skill(skill: "code-review", args: "high")
```

Para alterações que toquem em identidade, política, segredos, sandbox, auditoria ou o read-path
soberano, acrescenta:

```
Skill(skill: "security-review")
```

Quando quiseres a revisão verdadeiramente independente — sem o teu raciocínio a contaminá-la —
lança um subagente `general-purpose` que só vê o diff e os requisitos, nunca as tuas
justificações. O guião de perguntas está em
[`references/revisao-adversarial.md`](references/revisao-adversarial.md); as centrais são:

- Como é que isto falha?
- O que foi assumido sem evidência?
- Que requisito pode ter ficado de fora?
- Que consumidor quebra?
- **O teste pode estar a passar pela razão errada?**
- Existe solução mais simples que preserve o comportamento? (`Skill(skill: "simplify")`)

---

## 7. Integração e validação de sistema

Antes de integrar, audita o changeset — o que mudou face ao que **era suposto** mudar:

```bash
git status --short && git diff --stat
```

Qualquer ficheiro que não esperavas ver é para investigar, não para aceitar. O portão de
integração está em [`references/encerramento.md`](references/encerramento.md).

Depois corre a validação que atravessa módulos:

```bash
bash scripts/ci/test.sh          # suite unit -race por módulo + cobertura (lento)
bash scripts/ci/apex.sh          # enforcement composto no composition-root
bash scripts/ci/policy-test.sh   # golden allow/deny do PDP + assinatura do bundle
bash scripts/ci/security.sh      # cenários adversariais de segurança
```

Escolhe os gates pelo eixo que tocaste. O `Makefile` lista todos com uma linha de descrição
cada — é o índice mais rápido do que existe (`replay`, `memory`, `supplychain`, `routing`,
`scale`, `dr-e2e`, `ux-dx`, `evalgate`, `sbom`, …).

**E corre o sistema, não só os testes.** Uma suite verde sobre um nó que não arranca é o caso
exemplar de teste a passar pela razão errada:

```bash
bash .claude/skills/run-aos/driver.sh smoke
```

A skill [`run-aos`](../run-aos/SKILL.md) levanta o nó composto e prova nove passos
ponta-a-ponta. Para mudanças no wiring, nos banners de arranque ou na superfície HTTP, é a
única evidência que conta.

---

## 8. Auditoria de completude e encerramento

Compara o **pedido original** com o que existe agora — requisitos, tasks, implementação,
testes, validação — e procura o que se perdeu pelo caminho. O formato da auditoria, o quality
gate, a auditoria final e o relatório estão em
[`references/encerramento.md`](references/encerramento.md).

Toda a afirmação relevante no relatório precisa de evidência nomeada:

```text
AFIRMAÇÃO: "O endpoint mantém compatibilidade."
EVIDÊNCIA:  TestAOS263_CLIDecisaoExigeOsCamposQueAmarram cobre o corpo enviado;
            tecnica/12_Contratos_de_Interface.md inalterado;
            cmd/aos-orq compila e o smoke do run-aos passa.
```

Sem evidência, escreve `NÃO VERIFICADO`. É informação útil. "Provavelmente funciona" não é.

### Conhecimento persistente

Uma decisão arquitectural nova vira **ADR** em `docs/adr/ADR-NNN-slug.md` (formato MADR) — não
uma nota solta. Um risco novo, um contrato novo ou uma restrição nova vão para o `tecnica/` ou
para o epic correspondente. Não registes ruído temporário, e não reescrevas conhecimento
existente sem confirmar que a versão anterior deixou mesmo de ser verdade.

Se a execução for longa e valer a pena poder retomá-la sem depender do histórico da conversa,
mantém `.claude/state/execution-state.md` (formato em
[`references/decomposicao.md`](references/decomposicao.md)). Está fora do git por desenho — é
estado transitório, não fonte de verdade.

---

## Convenções não negociáveis deste repositório

- **PT-PT** em documentação e comentários novos. Termos técnicos consagrados ficam em inglês
  (`reference monitor`, `taint`, `durable execution`, `replay`, `eval-gate`).
- Conventional Commits; branch `feature/AOS-NNN-<slug>`.
- Nada de binários, cobertura ou artefactos de build no commit.
- Nenhum segredo em texto claro — env var, Vault, ou ficheiro montado em runtime.
- Testes deterministas: usa as fixtures do `packages/testkit` (relógios manuais, ids
  sequenciais, Event Store in-memory) em vez de `time.Now()`, `rand` ou UUID em asserções.

## Os dez princípios

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

## Referências

| Ficheiro | Lê quando… |
|---|---|
| [`references/decomposicao.md`](references/decomposicao.md) | Vais partir a tarefa, modelar dependências, resolver um conflito de fontes ou manter estado de execução |
| [`references/contratos-de-agente.md`](references/contratos-de-agente.md) | Vais lançar um subagente ou definir o que ele devolve |
| [`references/revisao-adversarial.md`](references/revisao-adversarial.md) | Estás a rever — o teu trabalho ou o de outro agente |
| [`references/encerramento.md`](references/encerramento.md) | Estás a integrar, auditar completude ou escrever o relatório final |
