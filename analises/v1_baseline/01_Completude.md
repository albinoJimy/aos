# Análise de Completude — Documentação AOS

| Campo | Valor |
|---|---|
| Produto | AOS — Agentic OS de Referência |
| Documento | Análise — Completude |
| Versão | 1.0 |
| Data | Julho de 2026 |
| Classificação | Documento de Referência — Aberto |
| Método | Auditoria multi-agente (painel de 6 + contra-auditoria) |

---

**Score:** 6.5/10

> **Veredicto:** Corpus de largura narrativa e de backlog notável (14 ADRs propagados, 118 tickets verificados, 7 dimensões em todos os docs), mas incompleto nas classes de artefacto que um blueprint de referência precisa de fornecer: contratos de interface, modelo de dados/eventos, exemplos de política, cobertura EU AI Act e rastreabilidade de requisitos.

## Pontos fortes

- Completude estrutural exemplar e consistente: os 14 ADRs canónicos são definidos em tecnica/00 §8 e propagados por todos os documentos; cada doc técnico repete a estrutura (Introdução/ADRs/conteúdo/Vista de qualidade/Riscos/Glossário/Aprovação/Versões) e cada uma das 7 dimensões de excelência tem secção própria.
- Backlog completo e verificável: contei exactamente 118 headers '## AOS-NNN' distintos (AOS-001..118), ranges por epic coerentes com o _BRIEF §8; cada ticket tem metadados, Critérios de Aceitação SMART, Detalhes Técnicos, Testes Requeridos, DoD e prompt de handoff (ex.: specs/EPIC-11 AOS-109).
- Modelo de domínio (ER) em specs/00 §3, máquina de estados durável em tecnica/00 §6 e diagramas de sequência para os fluxos críticos (tool call end-to-end §7, credential broker tecnica/07 §7.1, decisão PDP tecnica/09 §4, DR por replay tecnica/10 §6) — 50 diagramas Mermaid no total.
- Modelo de ameaças sólido alinhado a OWASP LLM Top 10 + OWASP ASI, com tabela vector→controlo e tabela de riscos/mitigações (tecnica/07 §3, §9).
- Completude operacional real: catálogo de runbooks RB-01..05, tabela SLI/SLO com limiares de alerta ligados a runbooks e RPO/RTO em tecnica/10 §7–§8; epic de testes cobre replay, idempotência, política, eval-harness, red-team, carga e DR e2e (EPIC-11 §2).
- Rastreabilidade parcial presente e útil: mapa Capacidade→Componente→Epic (specs/00 §8) e ADR→Documento (tecnica/INDICE §5).

## Constatações

| ID | Severidade | Localização | Problema | Recomendação |
|---|---|---|---|---|
| COMP-01 | alta | Todo o corpus (grep de code fences: apenas ```mermaid, ```text, ```markdown; 0 json/yaml/proto/rego); tecnica/01 §4-5; specs/01 §4 gate 4 e gate 7 | Não existe um único contrato de interface/API concreto entre os 13 componentes canónicos. As interfaces mais críticas — RM↔PDP (pedido de autorização / decisão + obrigações) e RT↔ES (append/read de eventos) — só aparecem como diagramas de sequência informais (tecnica/09 §4 mostra 'Query(principal, capacidade, recurso, contexto)' e 'Permit(com obrigações)' em prosa). O gate 4 de CI afirma validar 'Contratos entre componentes (RM↔PDP, RT↔ES)' e o gate 7 'teste de política Rego/Cedar', mas nenhum contrato nem exemplo de política é definido em lado nenhum. tecnica/01 (o componente mais crítico) não contém as palavras interface/contrato/API/endpoint. | Adicionar um anexo de contratos de porta (design-level, não implementação) para pelo menos RM↔PDP, RT↔ES, RM↔BRK, GW↔provider e REG: schema de request/response, semântica de erro, idempotência, versionamento SemVer da porta e, no caso do PDP, um exemplo mínimo de política Rego/Cedar allow/deny default-deny. |
| COMP-02 | alta | specs/00 §3 (só ER de domínio); tecnica/04_Memoria_Persistencia.md e tecnica/08 (sem schema); grep: 0 blocos SQL/JSON | Falta um modelo de dados/eventos canónico. O ER de specs/00 §3 é conceptual; não há schema concreto do envelope de evento do Event Store (run_id, step_id, idempotency_key, hash do prompt, manifesto de dependências, taint, cadeia de principal), nem do registo de audit hash-chained (campos da cadeia), nem do schema de memória. Isto é load-bearing: a fidelidade de replay, o princípio contexto≠registo e o crypto-shredding dependem da estrutura exacta desses registos, que nunca é especificada. | Acrescentar secção 'Modelo de dados e eventos' definindo o envelope de evento append-only, o registo de audit WORM (campos de encadeamento por hash), o manifesto de dependências por trajectória e o schema versionado de memória com migrações expand/contract. |
| COMP-03 | alta | tecnica/09 §8; specs/EPIC-09; _BRIEF §4 e specs/00 §2.4 ('EU AI Act por desenho') | A conformidade EU AI Act é repetidamente afirmada 'por desenho', mas em todo o corpus só é citado o Art. 14 (supervisão humana); o resto das 39 ocorrências regulatórias são GDPR Art. 17. Não há Art. 9 (gestão de risco), Art. 10 (governação de dados), Art. 11/Anexo IV (documentação técnica), Art. 12 (logging), Art. 13 (transparência), Anexo III (classificação de alto risco), obrigações GPAI, avaliação de conformidade nem FRIA. A afirmação de conformidade não é sustentada por cobertura. | Adicionar uma matriz de conformidade que mapeie cada artigo relevante do EU AI Act (e GDPR) → controlo AOS → ticket AOS-NNN que o implementa, incluindo classificação de risco do sistema e posição face a GPAI. |
| COMP-04 | média | specs/00 §7–§8; specs/01 §9; grep 'rastreabilidade' só surge em EPIC-09 §179/§199 (versão de política) | Não existe matriz de rastreabilidade de requisitos. Não há identificadores de requisito funcional (RF-) nem não-funcional (NFR-/RNF-); os NFR vivem em tabelas de 'drivers' mas não são individualmente rastreáveis a tickets, e não há matriz ADR→ticket que prove que os 118 tickets cobrem todos os requisitos derivados dos 14 ADRs (ex.: ADR-013/014 aparecem em apenas 3 e 2 epics respectivamente — cobertura assimétrica não justificada). | Adicionar matriz de rastreabilidade NFR/ADR → epic → ticket(s), com IDs de requisito estáveis, demonstrando cobertura completa e expondo lacunas de cobertura por ADR. |
| COMP-05 | média | tecnica/00 §10.6 e tecnica/09 §10.3 (apenas bullets em prosa); ausência de doc/epic dedicado; Slack/Telegram só em prosa | UX/DX é uma das 7 dimensões de excelência mas é a mais subespecificada: não tem documento técnico dedicado (tecnica/ vai de 01 a 11 sem um doc de UX) nem epic próprio. Card de aprovação, paridade de superfície (Slack/Telegram), semântica de progresso/burn-down, calibração de confiança e loop de autoria de skills existem apenas como bullets; o controlo bidireccional está disperso por EPIC-02/EPIC-09 sem contrato de superfície HITL. | Criar um documento (ou secção formal) de UX/DX com o contrato da superfície HITL, o modelo do approval-card, a semântica de progresso e mapear estas capacidades a tickets específicos. |
| COMP-06 | média | tecnica/07 §3 (modelo de ameaças); _BRIEF pede explicitamente 'matriz de segurança/STRIDE' | O modelo de ameaças usa bem OWASP LLM/ASI mas não há decomposição estruturada por componente/fronteira de confiança (STRIDE ou equivalente) sobre o data-flow. grep por STRIDE/DREAD/attack tree = 0 resultados. Falta a análise elemento-a-elemento (spoofing/tampering/repudiation/info-disclosure/DoS/EoP) que a expectativa do brief implica. | Adicionar uma matriz STRIDE por fronteira de confiança do diagrama de componentes (tecnica/00 §4), complementando — não substituindo — o mapeamento OWASP existente. |
| COMP-07 | baixa | tecnica/10 §8 (RB-01..05) vs tabelas de risco em tecnica/00 §12, tecnica/07 §9, tecnica/09 §11 | O catálogo de runbooks (5) não cobre 1:1 os modos de falha canónicos nomeados nas tabelas de risco: falta runbook para rug-pull/compromisso de supply-chain, resposta a incidente de prompt injection, indisponibilidade do Credential Broker/Vault, perda de quórum/partição do Event Store, cache-thrash com explosão de custo e esgotamento do pool de microVMs (a tabela SLI remete para 'escala' sem RB). Além disso RPO≤1min/RTO≤30min estão marcados *(proposta)* e por validar. | Estender o catálogo de runbooks para cobrir cada risco das tabelas de risco e ratificar os alvos de RPO/RTO via game-days documentados. |
| COMP-08 | baixa | Glossários por-documento (tecnica/00 §14, specs/00 §15, etc.); ausência de glossário/índice ADR mestre | Cada documento tem glossário próprio (bom para leitura isolada) mas não há um glossário mestre único nem um índice de ADRs standalone, o que permite deriva terminológica entre docs e obriga a manutenção duplicada. Não há também lista consolidada de acrónimos. | Publicar um glossário e um registo de ADRs mestre (single source) referenciado pelos glossários locais, ou gerar os locais a partir do mestre. |

## Lacunas identificadas

- Contratos de interface entre componentes (RM↔PDP, RT↔ES, RM↔BRK, GW↔provider, REG) — inexistentes em todo o corpus.
- Modelo de dados/eventos concreto: envelope de evento do Event Store, schema do registo de audit hash-chained, schema de memória versionado.
- Exemplo canónico de política policy-as-code (Rego/Cedar) — 0 blocos apesar de ser central (ADR-011) e ser gate de CI.
- Matriz de conformidade EU AI Act (Art. 9/10/11/12/13, Anexo III, GPAI, avaliação de conformidade, FRIA) — só Art. 14 citado.
- Matriz de rastreabilidade requisitos/ADR/NFR → tickets, com IDs de requisito estáveis.
- Documento/epic dedicado a UX/DX com contrato de superfície HITL e approval-card.
- Matriz STRIDE / fronteiras de confiança por componente.
- Runbooks para ~6 modos de falha canónicos ainda sem procedimento; ratificação de RPO/RTO.
- Glossário e registo de ADRs mestre único (anti-deriva).
- Modelo de capacidade/dimensionamento (sizing) e FinOps além do Model Gateway.
