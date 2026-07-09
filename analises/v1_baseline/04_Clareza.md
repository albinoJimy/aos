# Análise de Clareza — Documentação AOS

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Análise — Clareza |
| Versão | 1.0 |
| Data | Julho de 2026 |
| Classificação | Documento de Referência — Aberto |
| Método | Auditoria multi-agente (painel de 6 + contra-auditoria) |

---

**Score:** 7.5/10

> **Veredicto:** Conjunto muito legível e bem estruturado, com perfis de leitor e glossários exemplares, mas prejudicado por um mapeamento ticket→componente contraditório entre EPICs e por jargão/acrónimos não expandidos na 1ª ocorrência (violando a própria convenção do _BRIEF §9).

## Pontos fortes

- Perfis de leitor por papel no tecnica/INDICE §3.1 (Arquitecto, Runtime, Segurança, etc. com ordem de leitura) mais vistas por camada e por fase — excelente adequação à audiência e navegabilidade.
- Estrutura de Introdução uniforme nos docs técnicos (Propósito → Âmbito → Audiência → Definições e termos → ADRs → conteúdo → Vista de qualidade → Riscos → Glossário → Aprovação), boa ordenação do porquê ao como.
- Diagramas Mermaid clarificam de facto e não decoram: a máquina de estados durável (tecnica/00 §6), o threat model (tecnica/07 §3) e as sequenceDiagram de credential broker (07 §7.1) transmitem informação difícil de dar em prosa.
- Rigor factual nos metadados de navegação: contagens verificadas (tecnica ~3.000 linhas exactas ex-INDICE, specs ~8.910 exactas ex-INDICE, 47 diagramas Mermaid) batem certo.
- Cada documento tem glossário próprio, bloco de metadados, tabela de aprovação e controlo de versões consistentes; termos como durable execution, taint tracking, fencing token e crypto-shredding são bem definidos onde definidos.
- Tickets AOS-NNN muito claros para o executor: Contexto/Objectivo/Critérios SMART/Detalhes/Testes/DoD/Handoff, com prompt de handoff auto-contido para o Claude Code (ex.: EPIC-02 AOS-013/014).

## Constatações

| ID | Severidade | Localização | Problema | Recomendação |
|---|---|---|---|---|
| CLR-01 | alta | specs/EPIC-02 (l.49,62,75,119,146,412,605,647), specs/EPIC-03 (l.136), specs/EPIC-07 (l.72,412,578) vs specs/EPIC-01 (l.117,176,289) e specs/INDICE §7 (l.140-144) | Mapeamento ticket→componente contraditório e errado nas cross-referências de dependências, que são a espinha dorsal da navegabilidade do backlog. Canonicamente (EPIC-01): AOS-002=Event Store, AOS-003=Reference Monitor, AOS-005=Identidade não-humana. Porém EPIC-02 rotula AOS-002 como 'Reference Monitor' e AOS-005 como 'Event Store replicado'; EPIC-03 rotula AOS-002 como 'Reference Monitor mandatório'; EPIC-07 rotula AOS-001 (que é 'Bootstrap do monorepo') como 'Reference Monitor'. Pior, EPIC-07 usa AOS-005 como 'Event Store' (l.72,412) e como 'identidade' (l.578) no mesmo ficheiro. Os prompts de Handoff propagam o erro ('Confirma que AOS-002 (Reference Monitor) e AOS-005 (Event Store) estão Done', EPIC-02 l.119), instruindo o executor/Claude Code a confirmar os tickets errados. | Corrigir todas as etiquetas de dependência para o mapeamento canónico do EPIC-01/INDICE (AOS-002=Event Store, AOS-003=Reference Monitor, AOS-005=Identidade). AOS-013 deve depender de AOS-003 (RM) e AOS-002 (ES), não de AOS-005. Idealmente gerar as etiquetas a partir de uma fonte única para impedir deriva; adicionar teste de coerência ID↔título no CI de documentação. |
| CLR-02 | média | tecnica/00_Arquitectura_Solucao.md (WORM em §metadados/§2/§10; DSAR §12; TPM/RPM §5/§8; SA-ROC §8/§12), specs/00_System_Spec.md §12, specs/01_Engineering_Standards §4 (SBOM, DORA) | Jargão e acrónimos usados sem expansão na 1ª ocorrência, contrariando a convenção auto-declarada do _BRIEF §9 ('explicar na 1ª ocorrência'). No documento-âncora 00 — que os perfis 'Onboarding' e todos os papéis leem primeiro — 'WORM' aparece 7x (metadados, tabela de camadas, ADR-010, riscos) mas nunca é expandido nesse doc (só em tecnica/07 e 08); 'DSAR' aparece na tabela de riscos §12 sem expansão (definido apenas em tecnica/09); 'TPM/RPM' nunca é expandido em lado nenhum do doc; 'SA-ROC' nunca é expandido em todo o corpus (as letras não são explicadas, apenas parafraseado como 'modelo de escalonamento por risco'). Em Engineering Standards, 'SBOM' e 'DORA' surgem em tabelas sem expansão. | Expandir cada acrónimo na 1ª ocorrência de cada documento (WORM=Write-Once-Read-Many; DSAR=Data Subject Access Request; TPM/RPM=tokens/requests por minuto; SBOM=Software Bill of Materials; DORA metrics). Para SA-ROC, indicar o que a sigla representa ou marcá-la como nome próprio do modelo. Acrescentar as entradas em falta ao glossário do doc-âncora 00. |
| CLR-03 | média | Campo 'Documento' dos metadados: tecnica/00 (l.6), 01, 03 vs 02,06,07,08,09 vs 04,10,11 | O campo 'Documento' do bloco de metadados é inconsistente em formato entre documentos, contrariando o template do _BRIEF §1 ('<tipo> — <título>'). Coexistem 'Técnica — X' (02,06,07,08,09), 'Documento Técnico — X' e 'Documento técnico — X' (01 e 03, com capitalização divergente), 'Operação — X' (10), 'Convenções... — Manutenção Evolutiva' (11) e apenas título-subtítulo (04). O caso mais visível é o doc-âncora 00, com a tautologia 'Arquitectura de Solução — Documento de Arquitectura de Solução'. | Normalizar o prefixo <tipo> (ex.: 'Técnica'/'Foundation'/'Epic') e uniformizar capitalização; corrigir 00 para algo como 'Técnica — Arquitectura da Solução'. É puramente cosmético mas afecta a percepção de rigor logo no cabeçalho. |
| CLR-04 | média | specs/00_System_Spec.md §2.1 'O problema' (l.27); tecnica/00_Arquitectura_Solucao.md §13 'Roadmap por fases' (l.343) | Densidade excessiva (muro de texto) em passagens-chave que deviam ser as mais legíveis. System Spec §2.1 comprime toda a motivação num único parágrafo de ~11 linhas, encadeando 5-6 exemplos de falha separados por ponto-e-vírgula, exigindo releitura. O 'Roadmap por fases' de tecnica/00 §13 mete as 5 fases num só parágrafo denso, apesar de existir tabela/lista noutras secções. Contrasta com o resto do corpus, geralmente bem arejado com bullets. | Partir estes parágrafos em lista de bullets (um por modo de falha em §2.1; um por fase no roadmap), preservando o texto. Melhora o scan sem perder conteúdo. |
| CLR-05 | baixa | specs/00_System_Spec.md §10 vs specs/INDICE.md §2 vs _BRIEF §8 (títulos de epic) | Títulos de epic inconsistentes entre documentos: 'Fundações do Plano de Controlo' (System Spec §10) vs 'Fundações e Plano de Controlo' (INDICE/_BRIEF); alternância entre '&' e 'e' ('Model Gateway & Custos' vs 'Model Gateway e Custos'; 'Agent Runtime & Execução Durável' vs 'e'). Pequenas divergências que enfraquecem a sensação de fonte única ao saltar entre índices. | Fixar uma lista canónica de títulos de epic (com 'e' ou '&', não ambos) e replicar textualmente em System Spec, INDICE e _BRIEF. |
| CLR-06 | baixa | specs/EPIC-01..11 (todos) — apenas 3 diagramas Mermaid em ~8.900 linhas de specs, ambos nos Foundation | Os 11 ficheiros de EPIC (118 tickets, ~8.900 linhas) não têm qualquer diagrama; todo o conjunto specs tem 3 Mermaid, só no System Spec e Engineering Standards. Um leitor que abre um EPIC para perceber o encadeamento dos tickets só dispõe do mapa ASCII isolado no INDICE §7. Aceitável para um backlog textual, mas um mini-grafo de dependências por epic aumentaria a navegabilidade. | Adicionar um pequeno flowchart Mermaid de dependências no topo de cada EPIC (ou na Tabela Resumo de Tickets), reflectindo o campo Dependências/Bloqueia — depois de corrigido o CLR-01. |

## Lacunas identificadas

- Não existe um glossário/índice de acrónimos consolidado ao nível do conjunto: como os perfis de leitura saltam entre documentos (ex.: Segurança lê 00→07→01→09→05), um acrónimo definido só num doc (DSAR em 09, WORM em 07/08) fica por explicar para quem entra por outro. Um glossário mestre partilhado (ou no _BRIEF) fecharia a lacuna.
- Falta um teste/lint de coerência de referências cruzadas (ID de ticket ↔ título de componente, links relativos) que teria apanhado o CLR-01 automaticamente.
- Os EPICs não têm visão gráfica de dependências; o único mapa de caminho crítico vive no specs/INDICE §7 e não é reproduzido/enlaçado a partir de cada epic.
- Ausência de indicação de dificuldade/tempo estimado de leitura por documento nos INDICEs, útil para adequação à audiência (o perfil 'Responsável de Produto' beneficiaria de saber que 00 é denso).
