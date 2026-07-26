# AOS-190 — Prova negativa dos gates anti-recorrência (`layer-lint` · `rtm` · `ref-lint`)

Artefacto de evidência exigido pelo 4.º critério de aceitação de
`specs/EPIC-18_Remediacao_Auditoria_Multiagente_v4.md` §8/AOS-190 (achado **PLA-01**, ALTO).

Para cada um dos três gates ligados à CI por este ticket, regista-se: a **violação injectada**
(caminho e conteúdo exactos), o **comando**, o **output literal**, o **código de saída**, o
**comando de reversão** e a confirmação de que a árvore ficou limpa.

---

## 0. Âmbito e honestidade da prova — LER PRIMEIRO

O texto do critério de aceitação fala de «um **PR de teste** [que] torna `gates` vermelho».
**Esse PR não existe e não pode existir neste repositório: não há remote configurado**
(`git remote -v` vazio), logo não há execução real do workflow no GitHub para observar.

A prova aqui registada é, por isso, deliberadamente decomposta em duas metades — uma **executada**,
outra **verificada por inspecção estática** — e nenhuma é apresentada como sendo a outra:

| Elo da cadeia | Como é provado | Estatuto |
|---|---|---|
| violação no código/corpus → **script do gate sai `!= 0`** | **execução real**, registada em §1–§4 | **PROVADO** |
| script `!= 0` → **job do workflow vermelho** | semântica de `run:` do GitHub Actions (o passo herda o exit code) | assumido (plataforma) |
| job vermelho → **agregador `gates` vermelho** | inspecção estática do `needs:` e do `if: always()` — §5 | **VERIFICADO ESTATICAMENTE** |
| `gates` vermelho → **merge bloqueado** | configuração de *branch protection*, fora do repositório | **por configurar pelo dono** |

O último elo é configuração de plataforma e **não é verificável a partir da árvore**. A lista
canónica de *required checks* a configurar está em `.github/workflows/ci.yml` (linha `REQUIRED-CHECKS:`)
e em `CONTRIBUTING.md` §«Required checks»; o self-test **§M** falha se as duas listas divergirem
entre si ou do `needs:` real do agregador.

Todos os comandos foram executados a partir da raiz do repositório, na branch
`feature/AOS-128-ux-dx-tests`, com `PYTHONIOENCODING=utf-8`.

---

## 1. Estado de referência — os três gates VERDES

Sem isto, um gate vermelho na prova negativa não prova nada (podia já estar vermelho antes).

```text
$ bash scripts/ci/ref-lint.sh
Referências cruzadas OK: 203 tickets no backlog, 19 ADRs canónicos com cobertura, 51 ficheiros verificados.
EXIT=0

$ bash scripts/ci/rtm.sh
RTM está sincronizada com o corpus.
EXIT=0

$ bash scripts/ci/layer-lint.sh
== GATE: layer-lint — fronteiras de camadas (AOS-178) ==
--> root: /c/Tsco/AGENTICOS
[... 47 linhas "--> analisar packages/<módulo>" ...]
OK   nenhuma violação de fronteira fora da baseline
EXIT=0
```

O `layer-lint` percorreu os **47 módulos reais** de `packages/` (`control-plane/*`, `kernel/*`,
`platform/*`, `substrate/*`, `cmd/*`, `integration`, `testkit`, `qa/*`, `security-tests`) — não a
árvore sintética do self-test §L.

---

## 2. `layer-lint` — inversão de camada `platform → control-plane`

É exactamente a violação nomeada no critério de aceitação
(«um PR de teste com `import ".../control-plane/..."` dentro de `platform/`»).

**Violação injectada** — ficheiro novo
`packages/platform/model-gateway/routing/tiering/zz_aos190_prova_negativa.go`:

```go
package tiering

// PROVA NEGATIVA AOS-190 — ficheiro temporário. Import de control-plane dentro de
// platform/ viola a fronteira canónica (platform só pode importar platform/substrate).
import _ "github.com/aos-ref/control-plane/scheduler"
```

**Comando** — o mesmo que o job de CI corre (`--root` apontado à checkout real):

```text
$ bash scripts/ci/layer-lint.sh --root "$PWD"

== GATE: layer-lint — fronteiras de camadas (AOS-178) ==
--> root: /c/Tsco/AGENTICOS
FAIL violações de fronteira detectadas fora da baseline:
       github.com/aos-ref/platform/model-gateway/routing/tiering|github.com/aos-ref/control-plane/scheduler # violação de fronteira: platform -> control-plane
EXIT=1
```

**Reversão:** `rm -f packages/platform/model-gateway/routing/tiering/zz_aos190_prova_negativa.go`

---

## 3. `ref-lint` — citação de um ticket inexistente

**Violação injectada** — em `specs/EPIC-17_Remediacao_Auditoria_Multiagente_v3.md`, logo após
`## 1. Contexto`, a linha:

```markdown
Nota injectada para prova negativa: ver AOS-999.
```

**Comando e output:**

```text
$ bash scripts/ci/ref-lint.sh
ERRO: 1 AOS-NNN citado(s) não existem no backlog:
  - AOS-999: C:\Tsco\AGENTICOS\specs\EPIC-17_Remediacao_Auditoria_Multiagente_v3.md
EXIT=1
```

**Reversão:** `git checkout -- specs/EPIC-17_Remediacao_Auditoria_Multiagente_v3.md`

---

## 4. `rtm` — crescimento do corpus (e a correcção do *fail-open* que o precedeu)

Este gate **não bastava ligá-lo à CI**: na versão em `HEAD` era *fail-open* por construção. As
contagens da RTM (`203 tickets`, `AOS-001–AOS-203`, `18 epics`) eram **literais escritos à mão** no
gerador, pelo que o texto gerado era **invariante ao crescimento do corpus** — acrescentar um ticket
não mudava o output e o `--check` continuava verde. O ticket era ele próprio a prova do problema: as
constantes tiveram de ser editadas à mão (`189→203`, `17→18`) precisamente porque o gate não detectou
a deriva que devia impedir.

`scripts/ci/rtm-regenerate.py` passou a **derivar** essas constantes do corpus (`corpus_stats()`).
A prova abaixo é um **antes/depois sobre a mesma violação**, com o gerador de `HEAD` restaurado
temporariamente como `scripts/ci/rtm-regenerate-old.py` e a RTM por ele regenerada (estado «antes»
reconstruído fielmente):

**Violação injectada** — linha acrescentada à tabela resumo de
`specs/EPIC-18_Remediacao_Auditoria_Multiagente_v4.md` §7:

```markdown
| AOS-204 | Ticket fictício de auditoria | chore | S | P2 | AUD-99 | A |
```

```text
=== A. Estado 'ANTES' reconstruído: gerador de HEAD + RTM por ele gerada ===
$ python3 scripts/ci/rtm-regenerate-old.py --check
RTM está sincronizada com o corpus.
EXIT=0

=== B. Violação injectada (AOS-204) ===
$ bash scripts/ci/ref-lint.sh          # confirma que o corpus MUDOU mesmo
Referências cruzadas OK: 204 tickets no backlog, 19 ADRs canónicos com cobertura, 51 ficheiros verificados.
EXIT=0

--- ANTES (gerador de HEAD, contagens hardcoded) ---
$ python3 scripts/ci/rtm-regenerate-old.py --check
RTM está sincronizada com o corpus.
EXIT=0    <-- FAIL-OPEN: o corpus cresceu e o gate ficou VERDE

--- DEPOIS (gerador corrigido, contagens derivadas do corpus) ---
$ python3 scripts/ci/rtm-regenerate.py --check
ERRO: RTM diverge do corpus. Correr sem --check para regenerar.
EXIT=1    <-- FAIL-CLOSED
```

**Reversão:** remoção da linha `AOS-204`, `rm -f scripts/ci/rtm-regenerate-old.py` e reposição da
RTM regenerada pelo gerador corrigido. Confirmação:

```text
$ python3 scripts/ci/rtm-regenerate.py --check
RTM está sincronizada com o corpus.
EXIT=0
$ bash scripts/ci/ref-lint.sh
Referências cruzadas OK: 203 tickets no backlog, 19 ADRs canónicos com cobertura, 51 ficheiros verificados.
EXIT=0
```

### 4.1 Segunda prova — validação de gama, agora fail-closed

A validação de continuidade da gama de tickets escrevia `AVISO:` em `stderr` **sem alterar o exit
code**: um ticket em falta (apagado ou renumerado) não avermelhava o gate. Passou a `sys.exit(1)`.

**Violação injectada:** `AOS-203` renumerado para `AOS-204` em toda a EPIC-18 — cria um buraco na
gama (máximo 204, com 203 ausente).

```text
$ bash scripts/ci/rtm.sh
ERRO: gama de tickets descontínua — 1 em falta entre AOS-001 e AOS-204: AOS-203
EXIT=1

--- após reversão ---
$ bash scripts/ci/rtm.sh
RTM está sincronizada com o corpus.
EXIT=0
```

---

## 5. Elo workflow → merge: o que é verificável estaticamente

O que torna os três gates **bloqueantes** não é existirem como jobs — é constarem do `needs:` do
agregador. Verificação sobre `.github/workflows/ci.yml`:

```text
$ python3 -c "import yaml;d=yaml.safe_load(open('.github/workflows/ci.yml'));print(d['jobs']['gates']['needs'])"
['secrets', 'build', 'lint', 'ref-lint', 'rtm', 'layer-lint', 'test', 'replay', 'memory',
 'supplychain', 'routing', 'apex', 'security', 'evalgate', 'scale', 'dr-e2e', 'ux-dx',
 'sast', 'sca', 'policy-test', 'selftest']
```

21 entradas (18 pré-existentes + `ref-lint`, `rtm`, `layer-lint`), todas correspondendo a jobs reais.

**O agregador teve de ser endurecido para que o critério fosse literalmente verdadeiro.** Um job cujo
`needs:` falha é **saltado** pelo GitHub Actions e reporta a conclusão `skipped` — que a protecção de
branch trata como **passagem**. Na forma original, `gates` era incapaz de ficar vermelho: só podia
ficar saltado. Passou a ter `if: always()` e a avaliar explicitamente `needs.*.result`, terminando
`!= 0` se qualquer gate não ficar `success` (`failure`, `cancelled` **ou** `skipped`).

Nenhum dos três jobs novos tem `continue-on-error`, `if:` condicional ou `|| true`; os wrappers
terminam no interpretador, pelo que um interpretador ausente propaga `127` e avermelha o job —
**não há caminho de "skip" verde**.

### 5.1 Self-test §M — impedir que as listas voltem a divergir

As listas de *required checks* estavam divergentes entre si e do `needs:` real (`ci.yml` omitia
`secrets` e `selftest`; `CONTRIBUTING.md` listava 12 dos 21 e omitia `memory`, `supplychain`,
`routing`, `apex`, `security`, `evalgate`, `scale`, `dr-e2e`, `ux-dx`). Foram reconciliadas nas 21
entradas e na mesma ordem, e o **self-test §M** passou a compará-las — falha se divergirem, e
também se alguém remover o `if: always()` ou a avaliação de `needs.*.result` do agregador.

O §M é **não-vacuoso** (provado, como qualquer outro self-test):

```text
--- 1. estado correcto ---
SELFTEST OK   M: as 3 listas de required checks coincidem (21 checks, mesma ordem)
SELFTEST OK   M: agregador `gates` tem `if: always()` (não pode ficar `skipped` em vez de vermelho)
SELFTEST OK   M: agregador `gates` avalia `needs.*.result` (falha se algum gate não ficar success)
EXIT=0

--- 2. violação injectada: `memory` removido da lista de CONTRIBUTING.md ---
SELFTEST FAIL M: REQUIRED-CHECKS de CONTRIBUTING.md diverge do needs: do agregador
       needs:        secrets build lint ref-lint rtm layer-lint test replay memory supplychain ...
       CONTRIBUTING: secrets build lint ref-lint rtm layer-lint test replay supplychain ...
EXIT=1

--- 3. após reversão ---
SELFTEST OK   M: as 3 listas de required checks coincidem (21 checks, mesma ordem)
EXIT=0
```

---

## 6. Árvore limpa — nenhuma violação ficou para trás

```text
$ git status --porcelain --untracked-files=all | grep "^??"
(sem untracked)
```

Nenhum dos ficheiros de violação (`zz_aos190_prova_negativa.go`, `rtm-regenerate-old.py`)
sobreviveu, e nenhuma das injecções documentais permaneceu no corpus: os três gates estão verdes no
estado final (§4, reversão).
