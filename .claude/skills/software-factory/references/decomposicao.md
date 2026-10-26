# Decomposição, dependências e estado

Lê este ficheiro quando fores partir a tarefa em unidades, modelar o grafo de dependências,
resolver um conflito entre fontes de verdade, recuperar de uma falha, ou manter estado de
execução retomável.

- [Conflito entre fontes](#conflito-entre-fontes)
- [Anatomia de uma task](#anatomia-de-uma-task)
- [Grafo e estados](#grafo-e-estados)
- [Recuperação de falhas](#recuperação-de-falhas)
- [Estado de execução](#estado-de-execução)

---

## Conflito entre fontes

Duas fontes de verdade a discordar é informação, não ruído. O erro é escolher a mais recente,
ou a mais conveniente, sem o dizer. Regista:

```text
CONFLITO
- Fonte A:
- Fonte B:
- Diferença:
- Impacto (o que muda consoante a escolha):
- Decisão:
- Justificação:
```

No AOS a ordem de precedência está na tabela de fontes de verdade do `SKILL.md`. Um caso
frequente e enganador: **o código contradiz um ADR**. O ADR ganha, e a divergência é um defeito
a reportar — não uma licença para replicar o desvio em código novo.

Outro: **um teste codifica comportamento que o ticket quer mudar**. O teste não é obstáculo, é
a prova de que alguém decidiu esse comportamento de propósito. Descobre porquê antes de o
reescrever; se a decisão caducou, actualiza o teste **e** diz que o fizeste.

---

## Anatomia de uma task

Uma task tem de ser pequena o suficiente para um agente a compreender sem reconstruir o
projecto inteiro, e completa o suficiente para produzir um resultado que se possa verificar
isoladamente. Se não consegues escrever `VALIDACAO` sem depender de outra task, ainda não
partiste no sítio certo.

```text
TASK_ID:
OBJECTIVO:              o resultado, não a actividade
CONTEXTO:               o mínimo que torna a task compreensível
INPUTS:                 ficheiros, contratos, decisões já tomadas
OUTPUT_ESPERADO:
FILES_IN_SCOPE:
FILES_OUT_OF_SCOPE:     explícito — é o que impede o alargamento silencioso
DEPENDENCIAS:
RESTRICOES:             invariantes que não podem ser violados
VALIDACAO:              o comando concreto que prova o resultado
CRITERIOS_DE_ACEITACAO:
RISCO:                  o que corre mal se esta task estiver errada
```

`FILES_OUT_OF_SCOPE` merece atenção especial. Listar o que **não** se toca é mais eficaz do que
listar o que se toca: o segundo é uma sugestão, o primeiro é uma fronteira. No AOS, os
candidatos habituais são `packages/integration/`, `packages/cmd/aos/planos.go` (a tabela de
rotas), e qualquer `go.mod`.

---

## Grafo e estados

Modela as dependências antes de começar. Uma task só arranca quando as obrigatórias estiverem
satisfeitas.

```text
TASK-A ─────┐
            ├──> TASK-C ───> TASK-E
TASK-B ─────┘
                 ↑
TASK-D ──────────┘
```

Estados no caminho feliz:

```text
PENDING → ANALYZED → READY → IMPLEMENTING → IMPLEMENTED
        → SELF-VALIDATED → UNDER-REVIEW → REVIEWED
        → INTEGRATING → INTEGRATED → SYSTEM-VALIDATED
        → COMPLETENESS-VALIDATED → DONE
```

Estados de paragem:

```text
BLOCKED        dependência não satisfeita, ou informação em falta
FAILED         a task correu e não produziu o resultado
NEEDS-REWORK   produziu resultado, mas a revisão encontrou defeito
```

A distinção entre `IMPLEMENTED` e `DONE` é o ponto todo desta lista. São dez estados de
distância, e a maioria das entregas prematuras acontece por alguém tratar os dois como
sinónimos.

---

## Recuperação de falhas

Não repitas a mesma tentativa à espera de resultado diferente. Classifica primeiro:

```text
FAILED → CLASSIFICAR → estratégia → repetir ou reatribuir → validar
```

| Classe | Sintoma | Estratégia |
|---|---|---|
| **LOCAL** | erro no código escrito agora | corrigir e repetir a mesma task |
| **DEPENDÊNCIA** | falta algo que outra task devia ter produzido | voltar ao grafo; a dependência estava mal modelada |
| **CONTEXTO** | o agente não tinha informação suficiente | enriquecer o contrato do agente e reatribuir |
| **DESENHO** | a solução está errada, não a execução | voltar ao Architect; implementar melhor não salva |
| **IMPLEMENTAÇÃO** | o desenho está certo, a execução não | reatribuir com o defeito nomeado |

Duas falhas seguidas da mesma classe significam que a classificação está errada. Sobe um nível:
se dois `LOCAL` seguidos, provavelmente é `DESENHO`.

---

## Estado de execução

Para execuções longas, mantém `.claude/state/execution-state.md`. O critério é simples: o
estado tem de permitir retomar **sem depender do histórico da conversa**. Está fora do git por
desenho — é transitório.

```markdown
# Execution State

## Objectivo
...

## Fase actual
IMPLEMENTATION

## Tasks
| ID | Estado | Agente | Dependências |
|----|--------|--------|--------------|
| T01 | DONE | Explorer | - |
| T02 | DONE | Architect | T01 |
| T03 | IMPLEMENTING | Implementer | T02 |
| T04 | READY | Tester | T03 |

## Riscos
- ...

## Decisões
- ... (com a justificação, não só a escolha)

## Bloqueios
- ...

## Evidência
- ... (comando corrido → o que provou)

## Próxima acção
- ...
```

A secção **Decisões** é a que salva a retoma. Uma escolha registada sem o porquê obriga a
refazer o raciocínio — que é exactamente o custo que este ficheiro existe para evitar.
