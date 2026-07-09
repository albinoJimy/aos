# Análise de Coerência interna — Documentação AOS

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Análise — Coerência interna |
| Versão | 1.0 |
| Data | Julho de 2026 |
| Classificação | Documento de Referência — Aberto |
| Método | Auditoria multi-agente (painel de 6 + contra-auditoria) |

---

**Score:** 7/10

> **Veredicto:** Corpus quantitativamente muito coerente (ranges de tickets, alvos de NFR, 14 ADRs, metadados e PT-PT quase impecáveis), mas com um defeito sistémico crítico: as anotações de dependência de EPIC-02/03/07 rotulam IDs de tickets com o componente errado, contradizendo a numeração canónica de EPIC-01 e o próprio specs/INDICE.

## Pontos fortes

- Particionamento perfeito dos 118 tickets: os cabeçalhos '## AOS-NNN' cobrem AOS-001..AOS-118 sem lacuna nem sobreposição, exatamente igual aos ranges por epic do _BRIEF §8 e do specs/INDICE §3.1.
- Nenhuma dependência aponta para um ID inexistente: os 117 IDs distintos citados em campos 'Dependências'/'Bloqueia' existem todos dentro de AOS-001..118.
- Alvos de NFR idênticos em todo o lado: cold-start <125 ms (restore 5-30 ms), overhead de mediação RM p95 <15 ms, cache-hit-rate >80%, disponibilidade 99,9%, erro <2% por 30 dias — repetidos sem divergência entre _BRIEF §4, tecnica/00 §9, specs/00 §NFR, EPIC-07/10 e ambos os INDICE.
- 14 ADRs numerados de forma consistente (ADR-001..014), definidos em tecnica/00 §8 (secção confirmada), com SA-ROC expandido no glossário (tecnica/00:355) e os tags ADR-012/ADR-005 dos tickets de supply-chain (AOS-048/049) alinhados com tecnica/05.
- Metadados auto-consistentes ao detalhe: as contagens de linhas anunciadas no tecnica/INDICE (377,270,214...) e no specs/INDICE (332,268,747...) batem exatamente com o real (wc -l), e a alegação de '47 diagramas Mermaid' bate exatamente (47).
- Terminologia PT-PT quase uniforme: zero ocorrências de 'arquitetura/objetivo/ação/fator/registro' (formas PT-BR) em milhares de linhas; apenas 3 deslizes de 'deteção'.
- Modelo de maturidade M0-M4 e o roadmap de 5 fases coerentes entre _BRIEF §6 e tecnica/00 §13.

## Constatações

| ID | Severidade | Localização | Problema | Recomendação |
|---|---|---|---|---|
| COER-01 | crítica | specs/EPIC-02 (AOS-013,014,018,021), specs/EPIC-03 (AOS-026,027), specs/EPIC-07 (AOS-064,070,072) — campos 'Dependências' | Anotações de dependência rotulam IDs de tickets com o componente ERRADO, contradizendo a numeração canónica de EPIC-01 (AOS-001=Bootstrap, AOS-002=Event Store, AOS-003=Reference Monitor, AOS-005=Identidade, AOS-008=Orçamento hierárquico) e o próprio specs/INDICE §7. Exemplos: AOS-013 lista 'AOS-002 (Reference Monitor), AOS-005 (Event Store replicado)' — mas AOS-002 é o Event Store e AOS-005 é a Identidade; AOS-021/AOS-026 dizem 'AOS-002 (Reference Monitor)'; AOS-027 diz 'AOS-008 (Event Store replicado/transporte push)' quando AOS-008 é o Orçamento; AOS-064/AOS-070 dizem 'AOS-001 (Reference Monitor), AOS-005 (Event Store)' e AOS-072 diz 'AOS-001 (Event Store)' quando AOS-001 é o Bootstrap. Pior: o Reference Monitor é referido como AOS-002 em EPIC-02/03 e como AOS-001 em EPIC-07, sendo o RM canónico o AOS-003 — três IDs diferentes para o mesmo componente. Consequência: nem o loop do agente (AOS-013) nem a microVM (AOS-064) listam o ticket real do RM (AOS-003) como dependência, apesar da invariante de mediação total, e nenhum ticket cross-epic depende de facto de AOS-003. Isto corrompe o grafo de dependências legível por máquina que o passo 5 do specs/INDICE §4.1 ('verifica que dependências estão merged') e o caminho crítico §7 assumem correto. | Reconciliar todas as anotações de dependência de EPIC-02/03/07 com a numeração final de EPIC-01: substituir 'RM' por AOS-003, 'Event Store' por AOS-002, 'Identidade' por AOS-005, 'Orçamento' por AOS-008; acrescentar a dependência em falta de AOS-003 a AOS-013 e AOS-064. Gerar a tabela de dependências a partir de uma matriz única e validá-la em CI (todo par ID↔rótulo tem de bater com o título canónico do ticket). |
| COER-02 | média | tecnica/INDICE.md §5-§6 (linhas 137,152) vs specs/EPIC-01 e specs/EPIC-04 (linha 23 'Fora de âmbito') | O tecnica/INDICE afirma que 'cada documento técnico mapeia para o epic homónimo' (§6) e coloca ADR-007/Event Store como 'doc principal 04' (§5). Mas o desenho do Event Store vive em tecnica/04 enquanto o ticket de implementação (AOS-002) está em EPIC-01 — e o próprio EPIC-04 declara explicitamente 'O Event Store em si (EPIC-01)' como fora de âmbito. Do mesmo modo, o Reference Monitor é desenhado em tecnica/01 mas implementado em EPIC-01 (AOS-003). A regra de mapeamento homónimo tem exceções não assinaladas, o que induz um leitor a procurar a spec do Event Store em EPIC-04. | Qualificar a afirmação de mapeamento homónimo no tecnica/INDICE §6 com uma nota das exceções (Event Store: desenho em tecnica/04, backlog em EPIC-01/EPIC-10; Reference Monitor: desenho em tecnica/01, backlog em EPIC-01), ou adicionar uma tabela explícita 'tópico → doc técnico → epic/ticket'. |
| COER-03 | baixa | tecnica/INDICE.md §3.3 (linha 86) vs specs/INDICE.md §3.2 (linhas 71-72) | Inconsistência no mapeamento de fase do subsistema 06 (Model Gateway). O tecnica/INDICE coloca o doc 06 na Fase 1 com a anotação '(broker)' — mas o Credential Broker é o doc/EPIC-07, não o 06 — e também na Fase 3; o specs/INDICE, por sua vez, coloca EPIC-06 na Fase 2-3 (sem Fase 1). A anotação '(broker)' atribui erradamente o Credential Broker ao Model Gateway. | Corrigir a anotação '(broker)' na Fase 1 do tecnica/INDICE §3.3 (o broker é doc 07) e alinhar a fase declarada do subsistema 06 entre os dois índices (Fase 2-3, como em specs/INDICE §3.1). |
| COER-04 | baixa | specs/EPIC-03_Orquestracao_Escalonamento.md (linhas 407,426,442) | Deslize de PT-BR: 'deteção de saturação' (3 ocorrências) em vez de 'detecção' (forma PT-PT usada em todo o resto do corpus e mandatada pelo _BRIEF, ex.: 'detecção de deadlock'). | Substituir 'deteção' por 'detecção' nas três ocorrências de EPIC-03; opcionalmente adicionar um verificador de lista de termos PT-BR ao gate de lint documental. |

## Lacunas identificadas

- Não existe uma matriz de dependências canónica única contra a qual validar os campos 'Dependências'/'Bloqueia'; o specs/INDICE §7 é um caminho crítico desenhado à mão, não exaustivo, pelo que a divergência COER-01 passou despercebida.
- O campo 'Bloqueia' só regista ligações forward dentro do mesmo epic (ex.: AOS-002.Bloqueia não lista AOS-013, que dele depende), impossibilitando a validação de reciprocidade dependência↔bloqueio cross-epic — um ponto cego de coerência.
- As dependências cross-epic são expressas de forma grosseira ('EPIC-01 (Event Store)') em EPIC-04/05/06/08/09/10, misturando referências a ticket (AOS-NNN) com referências a epic inteiro, sem convenção uniforme.
- Falta um teste/gate de CI que verifique que cada rótulo entre parênteses num campo de dependência corresponde ao título canónico do ticket referenciado — a ausência deste gate é a causa-raiz de COER-01.
