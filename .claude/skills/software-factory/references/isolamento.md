# Isolamento de escritores concorrentes

Lê este ficheiro antes de lançar mais do que um agente a **escrever**, e sempre que trabalhares
num worktree.

- [A regra](#a-regra)
- [Worktree por escritor](#worktree-por-escritor)
- [Recursos partilhados sem o parecerem](#recursos-partilhados-sem-o-parecerem)
- [Ficheiros que não se partilham](#ficheiros-que-não-se-partilham)
- [Integração do trabalho paralelo](#integração-do-trabalho-paralelo)

---

## A regra

> **Quem lê pode partilhar a árvore. Quem escreve precisa da sua.**

Um agente read-only — Discovery, revisão, auditoria — corre na árvore que já tens e não há
nada a isolar. Um agente que chama `Write`, `Edit` ou `Bash` com efeitos precisa de worktree
próprio, ou de uma fronteira de ficheiros tão estreita que o worktree seria redundante.

A versão prática: **dois escritores nunca tocam nos mesmos ficheiros**. Se não consegues
garantir isso por desenho, não é paralelismo — é uma corrida que vais perder mais tarde, quando
já houver trabalho feito sobre o resultado errado.

---

## Worktree por escritor

Nesta app, um subagente recebe árvore própria com `isolation: "worktree"`; a sessão corrente
entra e sai de um worktree com as ferramentas de worktree do host. O repositório já é trabalhado
assim: `.claude/worktrees/` tem dezenas de árvores de trabalho simultâneo.

O que isso implica, e que é a origem de quase todos os acidentes desta classe:

```text
Cada worktree tem ficheiros próprios.
Todos partilham o MESMO .git.
E ramos que não se fundiram NÃO SE VÊEM.
```

### Registo de sessão — a única forma de os ramos se verem

```bash
python scripts/ci/sessoes.py registar --tarefa "..." --tickets AOS-NNN --ficheiros <path>
python scripts/ci/sessoes.py ver          # exit 1 se houver colisão
python scripts/ci/sessoes.py reservar 2
python scripts/ci/sessoes.py sair
```

É `python`, não `python3` — o `python3` do PATH neste posto é o atalho da Microsoft Store
([`admissao-e-linhas.md`](admissao-e-linhas.md) §Registo de sessão).

O `ver` sinaliza três colisões, e a terceira é a que mais custa:

| Colisão | O que significa |
|---|---|
| **ticket** | mais do que uma sessão reivindica o mesmo `AOS-NNN` |
| **ficheiro** | mais do que uma sessão toca o mesmo ficheiro |
| **base divergente** | dois HEAD em que nenhum é ancestral do outro |

A **base divergente** só se descobre ao abrir o PR, com o trabalho todo feito — já aconteceu:
uma base foi reescrita por baixo de um ramo que já assentava nela.

O estado vive em `$(git rev-parse --git-common-dir)/aos-sessoes/`, que os worktrees partilham
fisicamente: escrito num, lido em todos, sem merge e sem rede. **Não é versionado por desenho**
— em `specs/` ou `docs/` cada ramo veria a sua cópia e as sessões só se leriam depois do merge,
que é quando a colisão já custou o trabalho. E **não é gate de CI**: o runner tem um só
worktree, onde `ver` não teria com que colidir e passaria sempre — fail-open silencioso, pior
do que verificação nenhuma.

---

## Recursos partilhados sem o parecerem

Três recursos são comuns a **todos** os worktrees da máquina. Nenhum dá erro quando dois
agentes os usam ao mesmo tempo; dão resultado errado.

### 1. O driver do smoke

```bash
HOME_DIR="${AOS_DRIVER_HOME:-${TMPDIR:-/tmp}/aos-driver}"
```

O default é partilhado. Com trabalho paralelo, **dá a cada sessão o seu**, força o build, e
confirma o banner no `serve.log` antes de acreditares no que o smoke diz:

```bash
AOS_DRIVER_HOME=/tmp/aos-driver-<slug> bash .claude/skills/run-aos/driver.sh build
AOS_DRIVER_HOME=/tmp/aos-driver-<slug> bash .claude/skills/run-aos/driver.sh smoke
```

Sem isto, estás a medir o binário da outra sessão. Um smoke verde sobre o nó de outra pessoa é
pior do que smoke nenhum, porque produz evidência com o nome do teu PR.

### 2. O `ci-selftest`

**Esta suite muta a árvore de trabalho.** Injecta cada falha nos ficheiros reais — a assinatura
do bundle PDP e dois módulos sintéticos em `packages/` — e restaura-os no `trap`. Dois runs
sobrepostos têm backups de instantes diferentes, e o restauro de um escreve por cima do
trabalho do outro.

Há exclusão mútua (recusa com **exit 3**) e guarda de resíduo de um run morto sem `trap`
(**exit 4**). O que ela **não** pode impedir é que edites ficheiros por baixo dela enquanto
corre. Não a corras concorrente contigo a editar, nem duas de uma vez.

### 3. O cluster NATS de teste

Dois clusters de teste ao mesmo tempo disputam nomes e portas. Dá prefixo próprio ao teu, e
lembra-te de que um vermelho intermitente depois de matar um nó costuma ser um consumidor R1
sorteado no nó morto — mede onde ele está antes de chamar flake:

```bash
eval "$(bash scripts/ci/nats-cluster.sh up)"
bash scripts/ci/nats-cluster.sh down
```

---

## Ficheiros que não se partilham

Candidatos permanentes a `FILES_OUT_OF_SCOPE` quando há mais do que um escritor. Listar o que
**não** se toca é mais eficaz do que listar o que se toca: o segundo é uma sugestão, o primeiro
é uma fronteira.

| Ficheiro / área | Porquê |
|---|---|
| `packages/integration/` | composition-root — toda a composição passa aqui |
| `packages/cmd/aos/planos.go` | a tabela de rotas; conjunto fechado, uma rota sem plano aborta o arranque |
| qualquer `go.mod` / `go.sum` | um `go build` mexe-os sem pedir licença |
| `scripts/ci/baseline/*.txt` | baselines de lint/sast/sca — dois apêndices tornam-se um conflito |
| `tecnica/16_*` (RTM) | é gerada; dois geradores produzem dois ficheiros |
| `specs/EPIC-*.md` | é de onde sai a numeração; edita-o com o ticket reservado |

---

## Integração do trabalho paralelo

**Ramos verdes sozinhos já partiram juntos.** Dois PRs passaram cada um os seus gates e a
árvore integrada não compilava. Daí a regra:

```text
VERDE EM CADA RAMO  ≠  VERDE NA ÁRVORE INTEGRADA
```

1. Funde pela **ordem de dependência**, um de cada vez. Há um só integrador.
2. Depois de cada merge, corre os portões do eixo **na árvore integrada**, não no ramo.
3. Lê o diff antes de fundir — não basta o CI ter passado.
4. O repositório **não tem auto-merge** e os ramos não se apagam ao fundir; o merge é um acto,
   não uma consequência.
5. Fundir a base por squash deixa o PR encadeado em `CONFLICTING`. Resolve-o antes de seguir
   para o próximo.

Dentro de um worktree desta app, sincroniza com a base pelo `sync_with_base_branch` do host: o
merge local não consegue escrever os ficheiros protegidos pela sandbox do repositório.

E o trabalho de uma sessão em worktree próprio **não está nesta árvore** até o ramo ser fundido.
Antes de qualquer passo que precise das peças juntas — testar tudo ponta-a-ponta, por exemplo —
nomeia cada ramo pelo ref completo e confirma que é isso que se quer fundir.
