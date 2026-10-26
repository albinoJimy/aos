# Análise: de método artesanal a fábrica de software agêntica

**Data:** 2026-10-26
**Âmbito:** evolução da skill `.claude/skills/software-factory/` para uma fábrica de
software agêntica assente nos portões e convenções já existentes neste repositório.
**Natureza:** relatório de análise e desenho. Não altera código de produção nem portões de CI.

---

## 1. Ponto de partida

A skill `agentic-engineering` (hoje `software-factory`) existia como **método**: um ciclo de doze estações
(discovery → impacto → decomposição → paralelismo → implementação → self-validation → revisão
adversarial → integração → validação de sistema → auditoria de completude → conhecimento →
relatório), com quatro ficheiros de referência e dez princípios. A qualidade do método não
estava em causa — o princípio central («um agente produz evidências; não produz a verdade») e
o catálogo de padrões de defeito deste repositório são a parte mais valiosa do que existia, e
sobreviveram inteiros.

O que estava em causa é outra coisa: **um método serve uma tarefa; uma fábrica serve trabalho
recorrente.** A diferença não é de rigor, é de forma.

---

## 2. As cinco propriedades de uma fábrica

Da literatura consultada (§6) e da prática deste repositório, extraem-se cinco propriedades.
Faltavam quatro e meia.

| Propriedade | Estado antes |
|---|---|
| **Porta de entrada** — todo o pedido é classificado e roteado | ausente |
| **Linhas** — cada trabalho recorrente tem artefacto de entrada e de saída | ausente |
| **Separação** — nenhuma estação avalia o seu próprio produto | presente em espírito, sem mecanismo de verificação do achado |
| **Instrumentação** — a fábrica mede-se | ausente |
| **Realimentação** — um escape vira portão | parcial («escreve um ADR») |

---

## 3. Lacunas identificadas

### 3.1 Sem porta de entrada

Todo o pedido entrava no mesmo tubo genérico, do typo ao incidente de produção. Sem
classificação, a decisão de proporcionalidade ficava para o meio do trabalho — quando já se
gastou tempo no caminho errado.

### 3.2 Sem linhas de produção

Seis trabalhos recorrentes distintos — triagem de achados, implementação de ticket, fusão de
PR, resposta a incidente, registo de canon, corte de versão — partilhavam um pipeline só. Cada
um tem artefacto de entrada diferente, portões diferentes e, sobretudo, **autoridade
diferente**: três deles param num portão humano e isso não estava escrito em sítio nenhum.

### 3.3 Isolamento de escritores concorrentes não documentado

A lacuna mais cara, e a mais verificável: `.claude/worktrees/` tem dezenas de árvores de
trabalho simultâneo, o repositório tem uma ferramenta própria de registo de sessões
(`scripts/ci/sessoes.py`) com detecção de três classes de colisão, o driver do smoke tem um
`HOME_DIR` partilhado por omissão, e o `ci-selftest` muta a árvore de trabalho real. **Nada
disto constava da skill.** O histórico mostra o custo: tickets duplicados, defeitos fechados
duas vezes por caminhos diferentes no mesmo dia, e uma base reescrita por baixo de um ramo que
já assentava nela, descoberto ao abrir o PR com o trabalho todo feito.

### 3.4 Portões sem roteamento

A lista canónica tem 28 portões no `ALL_GATES` do `scripts/ci/run.sh`, mais o `selftest` e a
entrega (`package.sh`, `sbom.sh`) em jobs próprios. A skill nomeava seis, escolhidos ao calhas,
e — o ponto mais prático — **não dizia que o agregador aceita um subconjunto**. Também não
explicava os três códigos de saída, nem que o `3` (verde parcial) não é verde.

### 3.5 Velocidade não era tratada

A palavra não aparecia na skill. Num sistema com portões que levam de segundos a vinte e cinco
minutos, a ordem de execução é uma decisão de engenharia, não uma preferência.

### 3.6 Nada se media

Zero instrumentação. Uma fábrica que não se mede não melhora: repete.

### 3.7 Realimentação fraca

O destino de uma aprendizagem era «escreve um ADR» ou «acrescenta um padrão à lista». Faltava a
regra que fecha o ciclo e que é a tese do próprio AOS aplicada à fábrica: **o que escapou a uma
verificação muda a verificação**.

### 3.8 Autoridade humana implícita

Onde parar e pedir ao dono — cortar tag, aprovar o `environment: production`, assinar política
ou allowlist, publicar dados de produção — não estava escrito.

---

## 4. O desenho

```text
                        ADMISSÃO
             classificar · rotear · registar sessão
                            │
  ┌─────────┬─────────┬─────┼─────┬─────────┬─────────┐
  │ L1      │ L2      │ L3        │ L4      │ L5      │ L6
  │ achado  │ ticket  │ PR        │ incid.  │ conhec. │ release
  │ →ticket │ →PR     │ →merge    │ →correc.│ →canon  │ →produção
  └────┬────┴────┬────┴────┬──────┴────┬────┴────┬────┴────┬────┘
       └─────────┴──── CICLO INTERNO ──┴─────────┴─────────┘
            estações; nenhuma se avalia a si própria
                            │
          PORTÕES POR EIXO — subconjunto, fail-fast
                            │
       MÉTRICAS ──► REALIMENTAÇÃO: escape vira portão
```

**O ciclo interno não foi substituído: foi encaixado.** Continua a ser o mesmo pipeline de doze
estações, agora com papel **e proibição** por estação, e corre dentro de qualquer das seis
linhas. A diferença entre linhas está em onde entra, onde sai e que portões se aplicam — não no
rigor.

### 4.1 Adições materiais

| Adição | Porquê |
|---|---|
| Admissão com classe, eixo, orçamento de relógio e autoridade | decide a proporcionalidade antes de se gastar tempo |
| Seis linhas com artefactos de entrada/saída e portões próprios | torna «feito» diferente por tipo de trabalho |
| Tabela de roteamento eixo → portões | substitui a escolha de gates por memória |
| Verificação do achado (`CONFIRMADO` / `PLAUSÍVEL` / `MORTO`) | um reviewer instruído a achar problemas acha-os haja-os ou não |
| Referência de isolamento | o trabalho paralelo já existe; a skill passa a conhecê-lo |
| Seis indicadores e a regra escape → portão | fecha o ciclo de melhoria |
| Dois princípios novos (11 e 12) | «um escape vira portão»; «a fábrica mede-se» |

### 4.2 Ficheiros

| Ficheiro | Estado |
|---|---|
| `SKILL.md` | reescrito como fábrica; ciclo interno preservado e condensado |
| `references/admissao-e-linhas.md` | novo — triagem, as seis linhas, tabela de autoridade |
| `references/portoes-e-velocidade.md` | novo — roteamento, custo, ordem, seis regras de velocidade |
| `references/isolamento.md` | novo — worktree por escritor, registo de sessão, recursos partilhados |
| `references/metricas-e-realimentacao.md` | novo — indicadores, escape → portão, o que faz de um portão um portão |
| `references/revisao-adversarial.md` | emendado — verificação do achado, calibração, rever a correcção |
| `references/encerramento.md` | emendado — achados confirmados no portão, indicadores no relatório |
| `references/decomposicao.md`, `references/contratos-de-agente.md` | inalterados — são o detalhe do ciclo interno |

A skill passou a chamar-se **`software-factory`**: a invocação é `/software-factory`, e o nome antigo `/agentic-engineering` deixa de existir.

---

## 5. Decisões de desenho, e o que foi recusado

**A fábrica não traz ferramentas novas.** Tudo o que ela roteia já existe: o agregador de
portões, o registo de sessões, o driver do smoke, o portão humano do deploy. Acrescentar um
orquestrador próprio seria construir uma segunda fonte de verdade sobre o que já é fonte de
verdade — e a skill proíbe exactamente isso.

**O `sessoes.py` continua a não ser gate de CI.** Já estava decidido e a razão mantém-se: o
runner tem um só worktree, onde a verificação não teria com que colidir e passaria sempre —
fail-open silencioso, pior do que verificação nenhuma. A fábrica adopta este raciocínio como
critério geral de qualidade de um portão.

**Métricas sobre artefactos existentes, não telemetria nova.** Os seis indicadores leem-se do
que o repositório já produz: checks do PR, veredicto dos portões, achados da revisão. Nenhum
exige infra-estrutura.

**Recusado: contagem de PRs ou de linhas como indicador.** Premeia o comportamento que esta
skill existe para travar. A literatura é consistente — com agentes o *throughput* sobe e a
estabilidade de entrega desce, e foi por isso que o DORA de 2025 acrescentou a taxa de
retrabalho como quinta métrica.

**Recusado: um gate de CI para a própria fábrica.** Uma skill não se verifica mecanicamente, e
um portão que passa sempre é o anti-padrão que o §4 do `metricas-e-realimentacao.md` nomeia.

---

## 6. Base da pesquisa

Fontes consultadas, com a ressalva de que a maioria é material de fornecedor ou de prática, não
estudo independente; os números auto-reportados não foram verificados e não sustentam nenhuma
decisão deste desenho.

| Fonte | O que contribuiu |
|---|---|
| [LeadDev — the engineer's guide to building a software factory](https://leaddev.com/software-quality/the-engineers-guide-to-building-a-software-factory) | a noção de linha de produção que possui um trabalho recorrente de ponta a ponta; estações com objectivos separados para que nenhum agente avalie o próprio trabalho |
| [FutureProofing — agentic coding workflow 2026](https://www.futureproofing.dev/resources/ai-native-team/agentic-coding-workflow-2026) | a condição de «feito» como comando que o agente pode correr; reviewer em contexto limpo, sem o raciocínio do autor |
| [GitHub Blog — spec-driven development toolkit](https://github.blog/ai-and-ml/github-copilot/spec-driven-development-with-ai-get-started-with-a-new-open-source-toolkit/) | a especificação como artefacto autoritativo e a «constituição» de princípios não-negociáveis — papel que aqui já é desempenhado pela Carta, pelos ADRs e pelo `AGENTS.md` |
| [GitHub Next — Agentic Workflows / Continuous AI](https://githubnext.com/projects/agentic-workflows) | automação agêntica a aumentar o CI/CD existente em vez de o substituir |
| [DoD Enterprise DevSecOps Fundamentals](https://dodcio.defense.gov/Portals/0/Documents/Library/DoD%20Enterprise%20DevSecOps%20Fundamentals%20v2.5.pdf) | o sentido original de *software factory*: caminho repetível e padronizado, com portões, não um conjunto de ferramentas |
| [GitKraken — proving AI impact with DORA metrics](https://www.gitkraken.com/blog/proving-ai-impact-dora-and-velocity-metrics-guide-2026) · [getunblocked — DORA metrics in the AI era](https://getunblocked.com/blog/dora-metrics-ai-era/) | taxa de retrabalho como quinta métrica; divergência entre *throughput* e estabilidade |
| [MindStudio — parallel agentic development with git worktrees](https://www.mindstudio.ai/blog/parallel-agentic-development-git-worktrees) · [Restato Labs — Claude Code git parallel workflows](https://blog.restatolabs.com/claude-code-git-parallel-workflows/) | worktree por escritor; leitores partilham a árvore; um integrador aplica por ordem de dependência |

A contribuição mais específica deste repositório não vem de nenhuma destas fontes: é o catálogo
de escapes reais — defeito de forma que passa todos os portões porque eles só correm sobre
ficheiro, validação dirigida que não cobre o pacote, medição única que apresenta
não-determinismo como determinismo, correcção que abre defeito pior do que o achado.

---

## 7. O que não foi verificado

- **A fábrica não foi exercitada.** O desenho está escrito; nenhuma linha correu ponta-a-ponta
  sob a nova forma. A primeira execução real é a validação que falta.
- **Os seis indicadores não têm linha de base.** Sabe-se onde cada um se lê; não há valor
  histórico com que comparar.
- **A tabela de roteamento eixo → portões é derivada** da descrição canónica de cada portão e
  da lista de *required checks*, não de uma medição de cobertura por eixo. É um ponto de
  partida verificável, não uma garantia de suficiência.
- **A calibração de ~2/3 de hipóteses mortas** é observação acumulada de auditorias anteriores,
  não uma contagem formal.
