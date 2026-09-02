# Papéis, contratos e resultados de agente

Lê este ficheiro quando fores lançar um subagente, decidir que contexto lhe dar, ou definir o
que ele tem de devolver.

- [Os seis papéis](#os-seis-papéis)
- [Contrato do agente](#contrato-do-agente)
- [Controlo de contexto](#controlo-de-contexto)
- [Resultado obrigatório](#resultado-obrigatório)
- [Conflito entre agentes](#conflito-entre-agentes)

---

## Os seis papéis

Um papel é uma **restrição**, não um rótulo. O valor vem do que o agente está proibido de
fazer: um Explorer que implementa deixou de ser uma segunda opinião e passou a ser mais uma
fonte de código por rever.

| Papel | Responsabilidade | Não deve | `subagent_type` |
|---|---|---|---|
| **Explorer** | descoberta, navegação, mapeamento, dependências, levantamento de evidência | implementar; é read-only por construção | `Explore` |
| **Architect** | desenho, análise de impacto, contratos, decisões, riscos | alterar código para testar uma hipótese | `Plan` |
| **Implementer** | implementação dentro do escopo, seguindo os padrões existentes, com testes | alargar o escopo sem justificação | `general-purpose` |
| **Tester** | cenários positivos, negativos, extremos, regressão, validação comportamental | escrever o teste que a implementação já garante passar | `general-purpose` |
| **Reviewer** | revisão independente, procura de defeitos, comparação com requisitos | ver o raciocínio de quem implementou | `general-purpose` ou `Skill(code-review)` |
| **Integrator** | integração, conflitos, validação transversal, estado final | aceitar alteração inesperada sem investigar | agente principal |

**Sobre o Reviewer.** A independência é o mecanismo, não a etiqueta. Um subagente a quem
entregas a tua justificação vai avaliá-la, não o código. Dá-lhe o diff e os requisitos; guarda
o raciocínio para ti.

**Sobre o Tester.** O modo de falha característico é escrever o teste depois de olhar para a
implementação — sai um teste que replica o código em vez de verificar o requisito. Deriva os
casos dos critérios de aceitação, não do diff.

---

## Contrato do agente

Um subagente começa sem o teu contexto. O contrato existe para lhe dar o suficiente sem lhe
dar tudo — e para tornar o âmbito explícito o bastante para se saber, depois, se foi respeitado.

```text
ROLE:
TASK:
OBJECTIVE:
CONTEXT:                  o mínimo que torna a tarefa compreensível
KNOWN_FACTS:              o que já está estabelecido e não precisa de ser redescoberto
ASSUMPTIONS_ALLOWED:      onde pode decidir sozinho
ASSUMPTIONS_FORBIDDEN:    onde tem de parar e perguntar
FILES_IN_SCOPE:
FILES_OUT_OF_SCOPE:
DEPENDENCIES:
CONSTRAINTS:              invariantes que não pode violar
ACCEPTANCE_CRITERIA:
VALIDATION_REQUIRED:      o comando que tem de correr
EXPECTED_EVIDENCE:        o que tem de trazer como prova
```

`KNOWN_FACTS` é o campo que mais poupa. Sem ele, cada subagente refaz a discovery — e dois
agentes a explorar o mesmo código chegam a conclusões ligeiramente diferentes, o que produz
conflitos que não existiam.

`ASSUMPTIONS_FORBIDDEN` é o que mais evita retrabalho. No AOS, os candidatos permanentes: não
assumir que uma env var existe, não assumir que um subsistema está composto, não assumir a
forma de um contrato sem o ler, não assumir que um gate cobre o que o nome sugere.

---

## Controlo de contexto

Contexto a mais é tão prejudicial como contexto a menos: dilui o sinal e faz o agente atender
ao que é acessível em vez do que é relevante.

```text
CONTEXTO DA TASK → FICHEIROS RELEVANTES → CONTRATOS → TESTES
   → CONHECIMENTO RELEVANTE → só então, contexto mais largo
```

O contexto deve ser suficiente, preciso, relevante, verificável e **delimitado**. Prefere
apontar caminhos (`packages/cmd/aos/planos.go:179`) a colar blocos: o agente lê o que precisa e
vê a vizinhança, que os teus excertos escondem.

---

## Resultado obrigatório

Não aceites `"Feito"`. Um agente que reporta sucesso sem estrutura não deixou nada verificável
para trás — e a verificação é o único produto que interessa.

```text
RESULTADO DO AGENTE

Task:
Estado:                   (dos estados do grafo, não "ok")

Implementado:
- ...

Ficheiros alterados:
- ...

Testes:
- ...                     (nome do teste, não "testes passam")

Validação:
- ...                     (comando corrido → resultado)

Evidência:
- ...                     (o que sustenta cada afirmação acima)

Suposições:
- ...

Riscos:
- ...

Limitações conhecidas:
- ...

Próximo passo recomendado:
- ...
```

**Suposições, Riscos e Limitações vazios são suspeitos.** Trabalho real produz incerteza
residual. Três secções vazias significam quase sempre que o agente não procurou, não que não
havia.

---

## Conflito entre agentes

Dois agentes com conclusões incompatíveis é um sinal valioso — normalmente aponta para
ambiguidade real na fonte de verdade. Desperdiçá-lo aceitando o último resultado é o erro caro.

1. Não escolher automaticamente o mais recente.
2. Preservar **ambas** as evidências.
3. Identificar a origem: contexto diferente? fonte diferente? interpretação diferente do mesmo texto?
4. Comparar com a fonte de verdade (ordem de precedência no `SKILL.md`).
5. Pedir nova análise se nenhuma das duas resolver.
6. Registar a decisão.

```text
RESOLUÇÃO DE CONFLITO

Conflito:
Agente A:
Agente B:
Evidência A:
Evidência B:
Fonte de verdade consultada:
Decisão:
Razão:
Validação:
```

Se a origem for **interpretação diferente do mesmo texto**, o defeito está no texto. Corrige a
spec, o ADR ou o comentário — senão o conflito volta na próxima execução.
