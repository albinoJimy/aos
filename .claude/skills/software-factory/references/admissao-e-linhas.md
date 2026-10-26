# Admissão, linhas de produção e autoridade

Lê este ficheiro quando estiveres a classificar um pedido, a escolher a linha por onde ele
corre, ou a decidir se podes avançar sozinho.

- [Admissão](#admissão)
- [As seis linhas](#as-seis-linhas)
- [Tabela de autoridade](#tabela-de-autoridade)

---

## Admissão

A classe do pedido determina a linha; a linha determina os portões; os portões determinam o que
conta como feito. Classificar leva trinta segundos e evita os dois erros simétricos: correr a
suite inteira para uma alteração documental, e tratar um incidente de produção como se fosse um
ticket por implementar.

```text
ADMISSÃO
Pedido (nas palavras do utilizador):
Classe:                 L1 … L6, ou «sem linha»
Eixo tocado:            kernel · cmd · eventos · canon · módulos · orq · segurança · …
Artefacto de entrada:   o que já existe e serve de base
Artefacto de saída:     o que tem de existir no fim
Portões aplicáveis:
Autoridade:             eu, ou o dono — e em que passo
Orçamento de relógio:   quanto tempo isto devia demorar
```

O **orçamento de relógio** não é promessa, é instrumento: quando o passares para o dobro sem
resultado, pára e reporta em vez de continuares a tentar. Duas tentativas falhadas da mesma
classe significam que a classificação está errada (ver
[`decomposicao.md`](decomposicao.md) §Recuperação de falhas).

### Registo de sessão — obrigatório antes de L1 e de tocar em `scripts/ci/`

Este repositório é trabalhado em vários worktrees ao mesmo tempo, e **ramos que não se fundiram
não se vêem**. Já custou: duas sessões a fechar o mesmo defeito por caminhos diferentes no
mesmo dia, três tickets com números diferentes para o mesmo trabalho, e uma base reescrita por
baixo de um ramo que já assentava nela — descoberto só ao abrir o PR, com o trabalho todo
feito.

```bash
python scripts/ci/sessoes.py registar --tarefa "..." --tickets AOS-NNN --ficheiros <path>
python scripts/ci/sessoes.py ver          # colisões: ticket · ficheiro · base divergente
python scripts/ci/sessoes.py reservar 2   # próximos AOS-NNN livres, sob lock
python scripts/ci/sessoes.py sair
```

**É `python`, não `python3`.** Neste posto o `python3` do PATH é o atalho da Microsoft Store:
não corre nada e responde com um convite a instalar, em português, que não se parece com um
erro. Os gates resolvem-no provisionando um shim em `.tools/python3` e metendo-o no seu próprio
PATH — o que não te serve de nada à linha de comandos. Se vires a mensagem da Store, não é o
script que falta.

**Numera sempre com `reservar`.** A gama `AOS-NNN` é contígua — não se salta números para fugir
a uma colisão.

---

## As seis linhas

### L1 · achado → ticket

Converte hipóteses em trabalho real, ou mata-as com evidência.

```text
ENTRA   hipótese, suspeita, resultado de auditoria, bug avistado de passagem
SAI     ticket em specs/EPIC-XX_*.md com critérios de aceitação
        OU descarte registado com a evidência que o matou
```

| | |
|---|---|
| **Estações** | Discovery → verificação contra o canon → redacção do ticket |
| **Revisão** | **é a estação de verificação** — nesta linha não há diff para rever |
| **Portões** | `ref-lint` `rtm` `deferrals` `estado-citado` (o ticket é documento) |
| **Autoridade** | eu, depois de `sessoes.py reservar` |

**Modo de falha característico:** escrever o ticket antes de verificar. Cerca de **dois terços
das hipóteses adversariais morrem** contra o registo de deferimentos e os ADRs em vigor — o
trabalho já estava deferido com eixo, ou a decisão já tinha sido tomada ao contrário. E uma
**caixa por marcar não é trabalho por fazer**: pode ser trabalho feito cuja caixa ninguém
marcou. Confirma antes de abrir.

A separação de estações aqui não é um reviewer separado: é o **Discovery levantar** e a
**verificação derrubar**. Quem levanta a hipótese não a confirma, e cada afirmação do ticket
leva ao lado o comando que a sustenta — é isso que torna o ticket revisível por quem o for
implementar, em vez de uma lista de alegações.

Um ticket que cite um ticket já fechado não passa o `estado-citado`. Se o eixo natural apontar
para algo fechado, o eixo é inválido e o ticket precisa de outro — não de uma citação criativa.

#### O número que o `reservar` dá pode não caber neste ramo

**Duas regras colidem, e a colisão só aparece depois de escreveres o ticket.** O `reservar` dá um
número seguro **globalmente** — conta o que as outras sessões reivindicaram em ramos que este não
vê. O `rtm-regenerate.py` exige que a gama `AOS-NNN` seja contígua **neste ramo**, e recusa com
«gama de tickets descontínua» quando faltam números pelo meio. Num ramo cujas specs param em 495,
um `AOS-561` reservado deixa 65 lacunas e a RTM não regenera.

Confirma **antes** de escrever, não depois:

```bash
python scripts/ci/sessoes.py reservar 1
grep -hoE '^## AOS-[0-9]+' specs/EPIC-*.md | grep -oE '[0-9]+' | sort -n | tail -1
```

Se o número reservado não for o seguinte ao máximo deste ramo, **o ticket não é deste ramo**.
Pertence ao ramo que hospeda os números intermédios, e abri-lo aqui com um número mais baixo
duplica um que já existe noutro sítio — que é exactamente o acidente que a contiguidade existe
para impedir. Pára, diz em que ramo o ticket deve nascer, e entrega o rascunho; não inventes um
número para o fazer caber.

#### E verifica o achado NO ramo onde o ticket vai viver

**Esta é a parte que custa trabalho quando se salta.** Um achado verificado na tua árvore está
verificado contra o teu ramo. Se o ticket vai nascer noutro, parte dele pode já estar fechada
lá — e ninguém o vê daqui, porque ramos que não se fundiram não se vêem.

```bash
git grep -c -F '<a frase exacta que afirmas estar errada>' <ramo> -- <ficheiro>
git log -1 --format='%h %s' <ramo> -- <ficheiro>
```

Dois cuidados, os dois já pagos:

- **Usa `-F`.** Um padrão com `**`, `*` ou parênteses casa zero por erro de escapamento, e zero
  lê-se como «já corrigido». É o controlo negativo sobre um alvo que não existe, outra vez.
- **Lê a correcção que já lá está, não só a sua existência.** Quem corrigiu primeiro pode ter
  fixado uma formulação mais precisa do que a tua, e o ticket tem de a seguir — senão troca uma
  afirmação absoluta errada por outra absoluta no sentido oposto.

O que sobreviver a isto é o ticket. O que não sobreviver entra nele como «o que já está feito»,
com o PR que o fez — é o que impede a próxima sessão de o redescobrir.

---

### L2 · ticket → PR

A linha principal. É aqui que o ciclo interno corre inteiro.

```text
ENTRA   AOS-NNN com critérios de aceitação, dependências Done
SAI     PR verde, com evidência nomeada por critério
```

| | |
|---|---|
| **Estações** | o ciclo interno completo (SKILL.md §1) |
| **Portões** | os do eixo (ver [`portoes-e-velocidade.md`](portoes-e-velocidade.md)) + `driver.sh smoke` se tocaste no wiring |
| **Autoridade** | eu |

**Antes de arrancar:** confirma que as dependências estão `Done` ou disponíveis. Se bloquearem,
pára e reporta — não contornes.

**Modo de falha característico:** a alteração parcial. Funciona nos casos que testaste e falha
nos outros, e ninguém suspeita dela porque há evidência de que «está feito». Percorre os
critérios de aceitação um a um, com a evidência ao lado; um critério marcado sem evidência
nomeada não está marcado, está presumido.

---

### L3 · PR → merge

```text
ENTRA   PR aberto
SAI     squash merge em main
```

| | |
|---|---|
| **Estações** | vigiar CI → classificar vermelho → corrigir → fundir |
| **Portões** | os *required checks* da protecção de ramo, ou o agregador `gates` |
| **Autoridade** | eu, só com CI verde e revisão feita |

O repositório **não tem auto-merge** e os ramos não se apagam ao fundir. O merge é manual,
depois dos checks:

```bash
gh pr checks <n>                       # sem jq no host: usa gh -q
gh pr merge <n> --squash --match-head-commit <sha>
```

**Três armadilhas desta linha, todas já pagas:**

- **«no checks reported»** não é CI lenta — é o PR em conflito com a base.
- **Fundir a base por squash** deixa o PR encadeado em `CONFLICTING`. Dois PRs em cadeia
  fundem-se pela ordem, não em paralelo.
- **Um vermelho tem causa, não hipótese.** Lê o log antes de formular teorias: dois PRs
  seguidos falharam por motivos que a hipótese mais plausível não cobria.

Para sincronizar com a base dentro de um worktree desta app, usa o `sync_with_base_branch` do
host — o merge local não consegue escrever os ficheiros protegidos pela sandbox.

---

### L4 · incidente → correcção

```text
ENTRA   sintoma em produção, com output REAL colado
SAI     correcção em produção, verificada por evidência natural
```

| | |
|---|---|
| **Estações** | recolher evidência → RCA → correcção pela L2 → release pela L6 |
| **Portões** | os do eixo, mais o smoke; a prova final é em produção |
| **Autoridade** | eu investigo e corrijo; **o dono aprova o deploy** |

**A leitura de produção tem forma exacta:**

```bash
ssh aos-prod '<comando>' < /dev/null
```

Com `-n` em vez do redireccionamento, o pedido é bloqueado. O `/metrics` só responde pela rede
`aos_default`. E dados de produção não se publicam numa página sem pedido explícito.

**«Feito» dito por um operador exige output colado.** Um relato sem output é uma hipótese sobre
o estado do servidor, não uma observação dele.

**Modo de falha característico:** confundir não-determinismo com determinismo. Uma corrida só
não distingue um defeito de um flake — repete antes de afirmar, e compara o SHA da árvore antes
de suspeitar do código.

---

### L5 · conhecimento → canon

```text
ENTRA   decisão arquitectural, contrato novo, risco novo, armadilha descoberta
SAI     ADR / tecnica/ / epic / banner — e a RTM regenerada
```

| | |
|---|---|
| **Estações** | escolher o destino → escrever → regenerar → portões documentais |
| **Portões** | `ref-lint` `rtm` `deferrals` `estado-citado` |
| **Autoridade** | eu |

| O que descobriste | Onde vive |
|---|---|
| Decisão arquitectural nova | `docs/adr/ADR-NNN-slug.md`, formato MADR |
| Contrato novo ou alterado | `tecnica/12_Contratos_de_Interface.md` |
| Restrição ou risco novo | o epic em `specs/EPIC-XX_*.md`, ou `tecnica/` |
| Comportamento operacional | o banner de arranque em `packages/cmd/aos/` e o runbook |
| Armadilha de execução | `.claude/skills/run-aos/SKILL.md` §Gotchas |

**Depois de mexer em ADR, README de ADRs ou Estado de um ticket, regenera a RTM:**

```bash
python scripts/ci/rtm-regenerate.py
```

Uma mudança documental sem regeneração avermelha `rtm` e `selftest` — e só no CI, que é o pior
sítio para descobrir.

Não contradigas um ADR em vigor sem ADR de supersessão explícito, e antes de reescrever
conhecimento existente confirma que a versão anterior deixou mesmo de ser verdade. Na maior
parte das vezes não deixou: ficou incompleta.

---

### L6 · release → produção

```text
ENTRA   main fundida e verde
SAI     tag v* publicada, deploy aprovado, E2E verificado em produção
```

| | |
|---|---|
| **Estações** | preparar o servidor → cortar a tag → **pedir aprovação** → verificar |
| **Portões** | `package` `sbom` + os do eixo; depois o E2E em produção |
| **Autoridade** | **o dono** — o `environment: production` tem revisor obrigatório |

Sequência, pela ordem exacta:

1. **Põe os ficheiros no servidor primeiro.** Registos, `tools.json`, configuração — tudo o que
   a versão nova precisa. Uma versão aprovada antes de o servidor estar pronto cai, e o
   rollback **não desfaz o `rsync`**.
2. As notas de versão vão na **tag anotada**, não no CHANGELOG.
3. O push da tag `v*` dispara o deploy, que **pára no portão humano**.
4. Só então peço a aprovação, com o que vai entrar e o que verifico a seguir.
5. Rollback só com **digest explícito**.
6. Verificação final por evidência natural: um run real, não uma sonda construída para passar.

**Nunca cortes a tag antes do servidor estar pronto.** Uma versão já caiu ~13 minutos
exactamente por esta ordem ter sido invertida.

---

## Tabela de autoridade

Onde a fábrica pára e pede. Não é burocracia: é a fronteira entre o que é reversível e o que
não é.

| Acção | Quem |
|---|---|
| Editar código, testes e documentação no worktree | eu |
| Correr gates, smoke, suites locais | eu |
| Abrir ticket `AOS-NNN` | eu, depois de `reservar` |
| Abrir PR | eu |
| Fundir PR | eu, com CI verde e revisão feita |
| Ler produção | eu, pela forma exacta do `ssh` |
| **Cortar tag `v*`** | **peço ao dono** — dispara deploy |
| **Aprovar o `environment: production`** | **o dono**, sempre |
| **Assinar política, allowlist, rodar chaves** | **o dono** |
| **Publicar dados de produção** | **só a pedido explícito** |
| Apagar, sobrescrever ou forçar push | perguntar primeiro, sempre |

Uma aprovação dada num contexto não se estende ao seguinte. «O dono aprovou o deploy da v0.1.61»
não autoriza o da v0.1.62.
