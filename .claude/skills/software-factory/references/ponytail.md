# O ponytail nas estações da fábrica

Lê este ficheiro quando fores usar as skills do plugin `ponytail` dentro de uma linha — e
sobretudo antes de aceitar um achado de «cortar».

- [As quatro estações](#as-quatro-estações)
- [Onde divergem, ganha o repositório](#onde-divergem-ganha-o-repositório)
- [O que fica de fora](#o-que-fica-de-fora)

---

## As quatro estações

O ponytail traz um eixo que esta fábrica não tinha: **o que não devia existir**. Os portões
deste repositório medem se o código está correcto, seguro e coberto; nenhum pergunta se ele era
preciso. Entra em quatro estações, e em nenhuma delas substitui o que já lá estava.

| Estação | Skill | Para quê |
|---|---|---|
| **1.1** Discovery | `ponytail:ponytail-audit` | riscos e hipóteses de um pacote que não conheces |
| **1.4** Implementação | `ponytail:ponytail` (`full` / `ultra`) | a mudança mais pequena que fecha a tarefa inteira |
| **1.6** Revisão adversarial | `ponytail:ponytail-review` | defeitos no diff **e** no código ligado, e o que cortar |
| **1.8** Encerramento | `ponytail:ponytail-debt` | o livro da dívida antes do portão `deferrals` |

### 1.1 — na Discovery

Quando o alvo é um pacote que ninguém da sessão conhece, ou a pergunta é «o que está mal aqui?»,
junta a auditoria à varredura. É read-only, o que a torna compatível com a proibição do
Explorer, e devolve achados ordenados por eixo.

```
Skill(skill: "ponytail:ponytail-audit", args: "packages/<modulo>")
```

O que sai de lá entra como **riscos e hipóteses** da discovery — nunca como trabalho a fazer. É
exactamente a classe de achado que a §1.6 obriga a verificar antes de gerar trabalho.

### 1.4 — na Implementação

Activa-o no início da estação, nível `full`; `ultra` quando o pedido ainda merece ser
questionado antes de se construir.

```
Skill(skill: "ponytail:ponytail", args: "full")
```

Ele dá a ordem de escolha que este repositório já quer: não construir o que ninguém pediu,
reutilizar o que já existe no código, depois a stdlib, depois uma dependência já instalada, e só
por fim código novo — o mais curto que fecha a tarefa **inteira**, incluindo chamadores, testes e
fixtures que a mudança parte.

### 1.6 — na Revisão adversarial

Ao lado do `code-review`, não em vez dele.

```
Skill(skill: "ponytail:ponytail-review")
```

**As duas revisões não se substituem.** O `code-review` procura defeitos; o `ponytail-review` lê
também o código ligado ao diff — chamadores, funções chamadas, testes — e acrescenta o eixo do
que não devia existir. Os achados das duas passam pela mesma verificação
(`CONFIRMADO` / `PLAUSÍVEL` / `MORTO`): um achado de «cortar» não é mais verdadeiro por vir de
uma skill de simplicidade.

### 1.8 — no Encerramento

Antes de fechar, faz o livro da dívida — apontado aos marcadores **deste** repositório, não ao
`shortcut:` do ponytail:

```
Skill(skill: "ponytail:ponytail-debt", args: "DEFERIDO")
```

Um marcador novo sem eixo nem gatilho de revisão sai `no-trigger`: é dívida que apodrece em
silêncio e não fecha a auditoria. O portão `deferrals` é quem decide; isto é a leitura humana
antes dele.

**Onde o portão olha, medido e não suposto** (`scripts/ci/deferrals.py`): conta marcadores em
`packages/**/*.go` não-teste, e aplica a regra do eixo-fantasma também a `docs/adr/**.md` e
`tecnica/**.md`. **Não** varre `.claude/` nem `specs/` — é por isso que estas linhas podem
escrever `DEFERIDO` em maiúsculas sem o avermelhar, e foi verificado a correr o portão depois de
as escrever. Num comentário de Go, a mesma palavra conta como dívida nova.

---

## Onde divergem, ganha o repositório

Três regras, e a primeira não tem excepção:

**1. «Mínimo» nunca é retirar uma barreira.** Um gate, um fail-closed, uma verificação de
assinatura, de identidade ou de taint não são «código a mais» — são o produto (AGENTS.md §7).
Um achado de «cortar» que remova uma barreira de governação **rejeita-se**, e o veredicto é
`MORTO` com a razão escrita.

Isto tem precedente medido nesta base de código: escolher o guarda mais largo «por ser
fail-closed» já abriu uma regressão viva onde o estreito já estava garantido a montante. Nos
dois sentidos — alargar e encolher — a pergunta é a mesma: o que é que esta barreira impedia?

**2. Não uses o marcador `shortcut:`.** Um atalho deliberado aqui é um deferimento registado com
eixo e ticket, que o portão `deferrals` conta, ou um ticket novo. Um marcador que só o ponytail
conhece é invisível para os portões deste repositório, o que o torna a pior espécie de dívida:
a que ninguém vai cobrar.

**3. A resposta curta não substitui o relatório.** O ponytail optimiza para uma resposta que um
humano ocupado lê de uma vez. O encerramento desta fábrica exige evidência nomeada por
afirmação ([`encerramento.md`](encerramento.md)). As duas coexistem: a resposta curta é para o
utilizador, o relatório é o que fica.

---

## O que fica de fora

`ponytail:ponytail-gain` e `ponytail:ponytail-help` são ecrãs de consulta. Não entram em
estação nenhuma, e invocá-los a meio de uma linha não produz evidência de nada.
