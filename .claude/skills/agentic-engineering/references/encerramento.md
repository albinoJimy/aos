# Integração, auditoria e relatório final

Lê este ficheiro quando estiveres a integrar, a auditar completude, ou a escrever o relatório
final.

- [Auditoria do changeset](#auditoria-do-changeset)
- [Portão de integração](#portão-de-integração)
- [Auditoria de completude](#auditoria-de-completude)
- [Quality gate](#quality-gate)
- [Auditoria final](#auditoria-final)
- [Relatório final](#relatório-final)

---

## Auditoria do changeset

Antes de integrar, compara o que mudou com o que **era suposto** mudar.

```bash
git status --short
git diff --stat
```

```text
AUDITORIA DO CHANGESET

Ficheiros modificados:
Ficheiros criados:
Ficheiros removidos:
Alterações esperadas:
Alterações inesperadas:        ← investigar, não aceitar
Alterações fora do escopo:     ← justificar ou reverter
```

Toda a alteração inesperada é para investigar. As mais frequentes aqui: um `go.sum` mexido por
um `go build`, um `.exe` deixado por um build manual (está no `.gitignore`, confirma com
`git check-ignore`), ou um ficheiro de cobertura. Nenhum destes se committe.

Se o diff tiver reformatação massiva que não pediste, o `gofmt` corrigiu ficheiros que já
estavam desalinhados antes — separa isso num commit próprio ou reverte-o, porque enterra a
alteração real.

---

## Portão de integração

Uma task só integra quando **todas** fecham:

```text
[ ] Implementação concluída
[ ] Self-validation concluída
[ ] Testes relevantes executados (nomeados, não "os testes")
[ ] Revisão independente concluída
[ ] Problemas críticos resolvidos — não adiados
[ ] Changeset auditado
[ ] Dependências satisfeitas
```

Um problema crítico "resolvido registando-o como limitação conhecida" não passa este portão.
Limitação conhecida é para o que se decidiu não fazer; não é destino para o que falhou.

---

## Auditoria de completude

Compara, um contra o outro:

```text
PEDIDO → REQUISITOS → TASKS → IMPLEMENTAÇÃO → TESTES → VALIDAÇÃO
```

Procura deliberadamente: requisitos esquecidos, tasks não executadas, testes ausentes, caminhos
não validados, integrações incompletas, documentação desactualizada, alterações
**parcialmente** implementadas.

A alteração parcial é a mais perigosa das sete: funciona nos casos que testaste e falha nos
outros, e ninguém vai suspeitar dela porque há evidência de que "está feito".

```text
AUDITORIA DE COMPLETUDE

Requisitos:
- [x] ...
- [x] ...
- [ ] ...          ← se ficar por marcar, diz porquê

Em falta:
- ...

Riscos residuais:
- ...

Conclusão: COMPLETO / INCOMPLETO / PARCIALMENTE VERIFICADO
```

No AOS, a origem dos requisitos são os **Critérios de Aceitação** do ticket `AOS-NNN` no
`specs/EPIC-XX_*.md`. Percorre-os um a um, com a evidência ao lado. Um critério marcado sem
evidência nomeada não está marcado — está presumido.

---

## Quality gate

```text
[ ] Requisitos atendidos
[ ] Implementação validada
[ ] Testes relevantes executados
[ ] Revisão independente concluída
[ ] Alterações auditadas
[ ] Regressões avaliadas
[ ] Integração validada
[ ] Completude verificada
[ ] Riscos residuais documentados
[ ] Conhecimento actualizado quando havia o que actualizar
```

---

## Auditoria final

Dez perguntas, antes de responder ao utilizador:

```text
 1. O pedido original foi totalmente compreendido?
 2. Todas as partes do pedido foram implementadas?
 3. Existem partes não verificadas?
 4. Existem alterações fora do escopo?
 5. Os testes são suficientes — ou apenas presentes?
 6. Existem riscos residuais?
 7. Falta documentação (ADR, banner, README, spec)?
 8. O resultado é reproduzível por outra pessoa?
 9. A integração está consistente?
10. Existe alguma conclusão baseada apenas em suposição?
```

Se alguma resposta crítica for negativa, **não declares a tarefa concluída**. Entrega o que
está feito, nomeia o que ficou por fazer e porquê. Escalar o âmbito para baixo é decisão do
utilizador, não tua — mas esconder que o âmbito encolheu é decisão de ninguém.

---

## Relatório final

Objectivo, baseado em evidência, sem hedging.

```text
RESUMO DE EXECUÇÃO

Objectivo:

Concluído:
- ...

Ficheiros alterados:
- ...

Testes executados:
- ...                    (comando + resultado, não "todos passam")

Validação:
- ...

Revisão:
- ...                    (quem/o quê reviu, e o que encontrou)

Limitações conhecidas:
- ...

Riscos residuais:
- ...

Completude: COMPLETO / PARCIAL / NÃO VERIFICADO

Estado final: DONE / NEEDS ATTENTION
```

Duas notas sobre como isto se lê:

**"Testes executados"** com o comando e o resultado vale mais do que uma lista de nomes de
teste. `bash scripts/ci/policy-test.sh → verde (8 + 3 testes obrigatórios correram)` é reproduzível; "testes
de política passam" não é.

**"Revisão"** tem de dizer o que a revisão *encontrou*, mesmo quando o resultado foi nada. "Revisão
adversarial: 3 achados, 2 corrigidos, 1 aceite como limitação (ver abaixo)" é evidência de que
houve revisão. "Revisto" não é.

---

## Conhecimento persistente

Depois de fechar, actualiza o conhecimento quando houver mesmo o que registar:

| O que descobriste | Onde vive no AOS |
|---|---|
| Decisão arquitectural nova | `docs/adr/ADR-NNN-slug.md`, formato MADR |
| Contrato novo ou alterado | `tecnica/12_Contratos_de_Interface.md` |
| Restrição ou risco novo | o epic em `specs/EPIC-XX_*.md`, ou `tecnica/` |
| Comportamento operacional importante | o banner de arranque em `packages/cmd/aos/`, e o runbook em `docs/` |
| Armadilha de execução ou ambiente | `.claude/skills/run-aos/SKILL.md` §Gotchas |
| Aprendizagem de uma falha | ADR, se mudou uma decisão; senão, o comentário no sítio onde a falha ia reaparecer |

Não registes ruído temporário, e não contradigas um ADR em vigor sem um ADR de supersessão
explícito. Antes de reescrever conhecimento existente, confirma que a versão anterior deixou
mesmo de ser verdade — a maior parte das vezes não deixou, apenas ficou incompleta.
