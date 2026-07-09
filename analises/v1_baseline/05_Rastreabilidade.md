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

**Score:** 7/10

> **Veredicto:** A cadeia de rastreabilidade "para a frente" (doc→ADR, ticket→doc, testes→ADR) é genuinamente forte e densa, mas falta o sentido inverso: não existe matriz ADR×ticket nem NFR×ticket, o esquema de ticket não tem campo de ADR, e há decisões canónicas e a fundação EPIC-01 com âncoras de ADR em falta.

## Pontos fortes

- Cobertura de ADR completa: os 14 ADRs canónicos têm pelo menos um ticket implementador identificável (nenhum ADR órfão) — ex.: ADR-001→AOS-014, ADR-002→AOS-003, ADR-003→AOS-005/006, ADR-004→EPIC-07(microVM), ADR-013→AOS-023, ADR-014→AOS-089/090.
- Cada documento técnico (01-11) tem uma secção dedicada '§2 ADRs aplicáveis' com tabela ADR→relevância-neste-documento — rastreabilidade doc→ADR explícita e sistemática (verificado em tecnica/01,07,09,11).
- Densidade de citação alta e consistente: 563 ocorrências de ADR nas specs e 306 na tecnica; 10 dos 11 epics citam ADRs no campo 'Documentos de referência' dos tickets (EPIC-02..09 a 100%).
- EPIC-11 (Testes) fecha a cadeia requisito→verificação: cada ticket de teste amarra-se ao ADR/NFR que valida (AOS-111 replay↔ADR-001/010, AOS-112 idempotência↔ADR-001, AOS-113 política↔ADR-011/002, AOS-116 carga↔ADR-008, AOS-117 red-team↔ADR-005, AOS-118 DR↔ADR-007).
- Os alvos NFR propagam-se por valor da fonte/_BRIEF para os critérios de aceitação dos tickets: RM p95<15ms (20 ocorrências nas specs), cache-hit>80% (33), cold-start 125ms (EPIC-07/10), disponibilidade 99,9% (EPIC-10/11).
- Tickets referenciam o documento técnico COM secção (ex.: 'tecnica/05_Skill_Tool_Registry_Supply_Chain.md (§5)'), dando granularidade fina ticket→doc.
- Zero drift na identidade dos ADRs: numeração, decisão e racional coincidem entre _BRIEF §3, System Spec §11, tecnica/00 §8 e tecnica/INDICE §5.

## Constatações

| ID | Severidade | Localização | Problema | Recomendação |
|---|---|---|---|---|
| RAST-01 | alta | specs/00_System_Spec.md §8/§11; tecnica/INDICE.md §5; ausente em todo o corpus | Não existe matriz de rastreabilidade ADR→ticket nem NFR→ticket em lado nenhum. O System Spec §8 mapeia apenas Capacidade→Componente→Epic; o tecnica/INDICE §5 mapeia apenas ADR→'Doc principal' (um documento). É impossível responder 'que tickets implementam o ADR-013 ou o ADR-014?' ou 'que ticket valida o SLO de 99,9%?' sem fazer grep manual. O sentido inverso da rastreabilidade (de uma decisão/NFR para a sua execução) não está materializado. | Acrescentar uma Requirements Traceability Matrix (RTM) — tabelas ADR×ticket e NFR/SLO×ticket — no System Spec ou num anexo do INDICE das specs, mantida como artefacto de primeira classe. |
| RAST-02 | alta | _BRIEF.md §8 (esquema de tickets); tabela de metadados de cada AOS-NNN | O esquema de metadados do ticket definido no _BRIEF §8 (Epic, Fase, Tipo, Prioridade, Estimativa, Dependências, Bloqueia, Responsável, Documentos de referência) NÃO tem campo dedicado para ADRs. A ligação ticket→ADR é ad-hoc: ora está na tag do título, ora dobrada dentro de 'Documentos de referência', ora só na prosa do Contexto. Mecanismo inconsistente que impede extração estruturada. | Adicionar campo obrigatório 'ADRs' (e opcionalmente 'NFRs') à tabela de metadados do ticket no _BRIEF §8 e retro-preencher os 118 tickets. |
| RAST-03 | alta | specs/EPIC-01_Fundacoes_Plano_Controlo.md — tickets AOS-007, AOS-009, AOS-010, AOS-012 | EPIC-01 (Fundações — a epic mais crítica) é a ÚNICA que omite ADRs do campo 'Documentos de referência' (0/12, contra ~100% nas outras 10 epics). Pior: quatro tickets não citam qualquer ADR em todo o seu bloco — AOS-007 (Capability allowlist default-deny, decisão de segurança nuclear, sem âncora de ADR), AOS-009 (barramento, relacionado com ADR-007), AOS-010 (CI/gates) e AOS-012 (esqueleto control-plane, relacionado com ADR-001/008). A fundação do sistema é a parte com pior rastreabilidade decisão→execução. | Retro-preencher referências de ADR nos tickets de EPIC-01 (ex.: AOS-007→ADR-011, AOS-009→ADR-007, AOS-012→ADR-001/008) e incluir ADRs no campo 'Documentos de referência' como nas restantes epics. |
| RAST-05 | alta | tecnica/00_Arquitectura_Solucao.md §2 (princípio 4) e §8; specs/00_System_Spec.md §12 (linha Rug-pull) | Decisões estruturantes sem ADR próprio. (a) 'Contexto ≠ registo' é o princípio orientador nº4 em tecnica/00 §2, dirige o desenho de EPIC-04 e EPIC-08 e aparece em 10+ ficheiros, mas NÃO tem ADR (é o único dos 8 princípios sem citação de ADR). (b) O modelo de confiança da supply-chain (TOFU, pin+hash+assinatura, anti rug-pull) — pilar distinto de EPIC-05 — não tem ADR dedicado; é conflacionado no ADR-012 (SemVer/eval-gate de auto-modificação), incluindo na tabela de riscos ('Rug-pull de tool MCP → ADR-012'), que é uma decisão conceptualmente diferente. | Promover ambas a ADRs próprios (ex.: ADR-015 'Contexto ≠ registo', ADR-016 'Confiança de supply-chain/TOFU') e corrigir a tabela de riscos para apontar ao ADR correcto. |
| RAST-06 | média | tecnica/00_Arquitectura_Solucao.md §8; specs/00_System_Spec.md §11 | Os ADRs estão registados como linhas de tabela de três colunas (ADR \| Decisão \| Racional). Não há Estado/Status, Alternativas consideradas, Consequências/trade-offs, nem data por ADR. Uma auditoria de rastreabilidade de decisões não consegue rastrear o que foi rejeitado nem as consequências a jusante de cada decisão — o 'porquê' fica truncado (as alternativas rejeitadas aparecem só esparsamente na prosa, ex.: 'jails só como defesa secundária' em ADR-004). | Expandir cada ADR para o formato-padrão (Contexto · Decisão · Alternativas consideradas · Consequências · Estado), num log de ADRs dedicado ou em anexo de tecnica/00. |
| RAST-04 | média | specs/EPIC-01,02,05,09,10 — tags '[ADR-NNN]' nos cabeçalhos de ticket | Tagging de ADR no título inconsistente e por vezes semanticamente desalinhado. Apenas 10 dos 118 tickets tagueiam ADR no título; e onde é usado, o ticket implementador PRIMÁRIO frequentemente NÃO é o tagueado: o ticket canónico do ADR-007 (AOS-002, Event Store) fica sem tag enquanto o secundário AOS-100 (replicação) é tagueado; o ticket primário de taint do ADR-005 (AOS-069) fica sem tag enquanto AOS-049 (TOFU de schema) é tagueado com [ADR-005]. | Uniformizar: tagusing o ticket primário de cada ADR de forma consistente, OU abandonar as tags de título em favor do campo estruturado 'ADRs' (ver RAST-02). |
| RAST-09 | média | tecnica/INDICE.md §5 (coluna 'Doc principal') | O único índice inverso existente (ADR→'Doc principal') é grosseiro e ocasionalmente mal-localizado. ADR-007 (Event Store) aponta 'Doc principal = 04 (Memória)', mas a especificação/implementação fundacional está em tecnica/00 e EPIC-01/AOS-002; ADR-013 aponta '01/09' (dois docs), quebrando a singularidade de 'principal'. Sendo este o único mapa reverso, a imprecisão propaga-se. | Substituir a coluna por ADR→{doc principal, epic, tickets primários}, alinhando com a RTM de RAST-01. |
| RAST-07 | baixa | specs/EPIC-11_Testes_Qualidade.md — AOS-111, AOS-112 (campo 'Documentos de referência') | Mis-apontamento menor de referência: os tickets de teste de replay/idempotência apontam 'Documentos de referência' para tecnica/11 (Convenções de Engenharia) em vez dos documentos que efectivamente especificam replay e idempotência (tecnica/02 Agent Runtime e tecnica/08 Observabilidade). O ADR está certo, mas o doc técnico de referência não é o autoritativo do tema. | Apontar AOS-111/112 para tecnica/02 e tecnica/08 (mantendo tecnica/11 se relevante para o versionamento do golden-set). |
| RAST-08 | baixa | tecnica/01 (§2) vs tecnica/07,09,11 (§2) | Nome do cabeçalho da secção de ADRs inconsistente entre documentos técnicos: '## 2. ADRs aplicáveis' (tecnica/01) vs '## 2. Princípios e decisões aplicáveis (ADRs)' (tecnica/07,09,11). Cosmético, mas prejudica extração automática/cross-referência da secção de rastreabilidade. | Normalizar o título da secção 2 em todos os documentos técnicos. |

## Lacunas identificadas

- Falta uma Requirements Traceability Matrix (RTM) bidireccional ligando síntese-fonte → ADR/NFR → documento técnico → epic → ticket; hoje só existe o sentido 'para a frente' e mapas parciais (Capacidade→Epic; ADR→Doc principal).
- Falta campo estruturado 'ADRs' no esquema de ticket (_BRIEF §8), pelo que a rastreabilidade ticket→ADR não é consultável por máquina.
- Falta um log de ADRs com Estado/Consequências/Alternativas e controlo de supersessão — os ADRs são linhas de tabela sem histórico de decisão.
- Duas decisões canónicas não estão registadas como ADRs: 'Contexto ≠ registo' e o modelo de confiança de supply-chain (TOFU/pin+hash+assinatura).
- Falta rastreabilidade explícita NFR/SLO → ticket de teste que o valida sob a forma de matriz (EPIC-11 fá-lo em prosa, não em tabela).
- Falta o back-check risco→ticket: as tabelas de risco (System Spec §12, tecnica/00 §12) mapeiam risco→ADR mas não indicam o ticket mitigador, deixando um elo em aberto entre risco identificado e trabalho executável.
