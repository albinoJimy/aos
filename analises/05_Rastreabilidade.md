# Análise de Rastreabilidade de decisões — Documentação AOS

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Análise — Rastreabilidade de decisões |
| Versão | 1.0 |
| Data | Julho de 2026 |
| Classificação | Documento de Referência — Aberto |
| Método | Auditoria multi-agente (painel de 6 + contra-auditoria) |

---

**Score:** 6.5/10

> **Veredicto:** A cadeia ascendente ticket→ADR→documento é forte e consistente (0 ADRs órfãos, 111/118 tickets citam ADRs, 0 links partidos), mas falta o rasto descendente e um artefacto-matriz: os documentos técnicos não apontam para tickets, os NFR/SLO não têm identificadores rastreáveis, e não existe nenhuma matriz ADR×ticket ou NFR×ticket.

## Pontos fortes

- Os 14 ADRs são definidos canonicamente uma vez (tecnica/00 §8) e reproduzidos de forma coerente no _BRIEF §3 e no specs/00_System_Spec §11 — nomenclatura ADR-001..014 estável em todo o corpus.
- Sem ADRs órfãos: cada um dos 14 ADRs é citado por >=3 tickets distintos (ADR-014 por 3: AOS-022/089/090; ADR-013 por 8; ADR-010 por 47) e por vários documentos técnicos — verificado por matriz ADR->ticket construída programaticamente.
- Traçabilidade ascendente rica: 111 de 118 tickets citam pelo menos um ADR no corpo, e TODOS os 118 tickets possuem o campo 'Documentos de referência' na tabela de metadados.
- Integridade de referências: os 27 caminhos distintos de documento citados nos tickets resolvem todos para ficheiros existentes (0 links partidos); não há ADRs-fantasma acima de ADR-014.
- A Matriz de Conformidade (tecnica/14) fornece traçabilidade bidireccional genuína artigo(EU AI Act/GDPR)->controlo->ticket para 21 tickets de observabilidade/governação (AOS-072, 076-097).
- A tabela de drivers não-funcionais (tecnica/00 §9, specs/00 §7) associa consistentemente cada driver ao(s) ADR(s) que o materializa(m), e specs/00 §8 mapeia capacidade->componente->epic.

## Constatações

| ID | Severidade | Localização | Problema | Recomendação |
|---|---|---|---|---|
| RAST-01 | alta | tecnica/00_Arquitectura_Solucao.md §8; specs/00_System_Spec.md §11 | Não existe rasto descendente ADR->ticket nem qualquer matriz de traçabilidade. A tabela de ADRs tem apenas colunas 'ADR \| Decisão \| Racional' (e 'Racional resumido' no System Spec); nenhuma coluna 'implementado por' / 'epic' / 'ticket'. Para descobrir que tickets realizam um ADR é preciso fazer grep manual em ~8.900 linhas de specs — a rastreabilidade da decisão até à execução não é navegável a partir da decisão. | Acrescentar à tabela §8 (e/ou §11) uma coluna 'Epics/Tickets que implementam' (ex.: ADR-004 -> EPIC-07: AOS-064,065,066,067) ou criar um documento dedicado 'Matriz de Rastreabilidade' (ADR x ticket) análogo à tecnica/14, gerado a partir das citações. |
| RAST-02 | alta | tecnica/00–13 (todos os documentos de desenho) | O rasto é unidireccional no boundary tecnica<->specs: os tickets apontam para cima (campo 'Documentos de referência'), mas NENHUM dos 14 documentos de desenho (00-13) refere um único ticket AOS-NNN — só a tecnica/14 (matriz de conformidade) cita tickets. Um leitor de tecnica/07 (Segurança) não consegue saber que tickets o concretizam para além da homonímia grosseira epic<->doc afirmada no _BRIEF. | Adicionar em cada documento técnico (ou por secção major) um bloco 'Tickets que implementam esta secção', fechando o ciclo bidireccional requisito->ticket e ticket->requisito. |
| RAST-03 | alta | tecnica/12_Contratos_de_Interface.md e tecnica/13_Modelo_Dados_Eventos.md vs specs/EPIC-* | Os documentos 12 (Contratos de Interface) e 13 (Modelo de Dados e Eventos) — descritos na tecnica/INDICE §2 como load-bearing e validados por 'gates de CI de contratos' — não são referenciados por NENHUM ticket individual; aparecem apenas no campo 'Documentos relacionados' ao nível de epic, e de forma desigual. EPIC-05 (contrato REG) e EPIC-06 (contrato GW->provider) NÃO citam o doc 12 apesar de este definir exactamente os seus contratos de porta; tickets como AOS-002 (schema de eventos), AOS-003 (RM<->PDP) e AOS-055 (gateway) implementam contratos/envelopes definidos em 12/13 sem os citar. | Retro-encaixar referências ao nível de ticket: cada ticket que implementa uma porta ou persiste eventos deve citar tecnica/12 e/ou tecnica/13; adicionar o doc 12 aos 'Documentos relacionados' de EPIC-03/05/06/10. |
| RAST-04 | média | specs/00_System_Spec.md §7 e §14; _BRIEF §4 | Os drivers não-funcionais e os KPIs/SLOs não têm esquema de identificadores — a string 'NFR' não ocorre nos specs (0 ocorrências, excepto 4 em EPIC-11) e não há IDs NFR-xx/SLO-xx. Consequentemente um NFR só é rastreável a um ticket indirectamente via ADR, e não é possível verificar que cada alvo (cold-start <125 ms, PDP p95 <15 ms, cache-hit >80%, replay 100%) tem um ticket que o implementa e outro que o mede. | Atribuir IDs (ex.: NFR-01..NFR-10, SLO-01..) e criar uma coluna/matriz NFR->ticket de implementação + ticket de verificação (tipicamente em EPIC-11), tal como já existe para conformidade na tecnica/14. |
| RAST-05 | média | specs/EPIC-01 (AOS-007, AOS-009, AOS-012) | 7 tickets não citam qualquer ADR; 3 deles são estruturais e deviam citar: AOS-007 'Capability allowlist default-deny' (implementa ADR-002 e ADR-005) não cita nenhum ADR; AOS-009 'Barramento de eventos push' (implementa ADR-007) idem; AOS-012 'Esqueleto do plano de controlo' (relaciona-se com ADR-008) idem. Uma decisão de desenho P0 de segurança fica sem justificação rastreável. | Adicionar as citações em falta (AOS-007 -> ADR-002/ADR-005; AOS-009 -> ADR-007; AOS-012 -> ADR-008). Os restantes 4 (AOS-001 já cita ADR-007; AOS-010, AOS-108, AOS-109, AOS-110) são chores de tooling/processo e são aceitáveis sem ADR. |
| RAST-06 | baixa | tecnica/00_Arquitectura_Solucao.md §8 | Os 'ADRs' são linhas de tabela-resumo (Decisão + Racional), sem os campos que tornam uma decisão auditável ao longo do tempo: Estado (Proposto/Aceite/Substituído), Alternativas rejeitadas e Consequências distintas do racional. Baselines substituídas (ex.: 'SQLite single-writer' para ADR-007; 'jails' para ADR-004) são mencionadas em prosa mas nenhum ADR regista formalmente estado 'supersedes/superseded', pelo que a genealogia das decisões não é rastreável. | Expandir cada ADR para o template canónico (Estado, Contexto, Decisão, Alternativas, Consequências, Substitui/Substituído-por), mantendo a tabela-resumo como índice. |
| RAST-07 | baixa | tecnica/INDICE.md §3, §4, §7 | Metadados desactualizados degradam o mapa de rastreabilidade: o changelog §7 diz 'conjunto técnico (12 documentos)' enquanto o cabeçalho §2 diz '15 documentos'; o diagrama de dependências §4 e as tabelas por-perfil §3 param no doc 11 e omitem os docs 12-14. Quem usa o índice como ponto de entrada da cadeia não descobre por lá os documentos de contratos/dados/conformidade. | Actualizar o diagrama §4 e as tabelas §3 para incluir os docs 12-14 e corrigir a linha de changelog para '15 documentos'. |

## Lacunas identificadas

- Não existe um artefacto único de matriz de rastreabilidade que percorra a cadeia completa síntese-fonte -> ADR -> documento técnico -> epic -> ticket; a única matriz existente (tecnica/14) está limitada a conformidade EU AI Act/GDPR.
- Ausência de esquema de identificadores para NFR/drivers e para KPIs/SLOs, impossibilitando rastreio directo NFR->ticket de implementação e NFR->ticket de verificação.
- Sem ligações descendentes documento-técnico -> ticket (excepto doc 14); o ciclo bidireccional está aberto do lado da decisão/desenho.
- Documentos 12 (contratos de porta) e 13 (modelo de dados/envelope de evento) não estão ligados ao nível de ticket, apesar de serem invocados como load-bearing pelos gates de CI de contratos.
- ADRs sem ciclo de vida de estado (Aceite/Substituído) nem registo de alternativas — não há rasto de decisões que substituíram outras.
- Não há citação explícita, em nenhum ticket, da síntese-fonte (_FONTE_agentic-os-ideal.md) por secção/âncora; o primeiro elo da cadeia (fonte -> ADR) é assumido, não demonstrado ticket-a-ticket.
