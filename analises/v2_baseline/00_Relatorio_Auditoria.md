# Relatório de Auditoria à Documentação — AOS

> **Produto:** AOS (Agentic Operating System)
> **Versão:** 1.0
> **Data:** Julho de 2026
> **Classificação:** Documento de Referência — Aberto
> **Auditor-chefe:** Consolidação do painel de 6 auditores dimensionais
> **Âmbito auditado:** conjuntos `_BRIEF`, `_FONTE`, `tecnica/00–14 + INDICE`, `specs/00–01 + EPIC-01..11 + INDICE` (15 docs técnicos, 118 tickets AOS-001..118, 14 ADRs)

---

## 1. Sumário executivo

O corpus documental do AOS é um **blueprint de referência forte, largo e invulgarmente honesto** nas suas ressalvas técnicas. A estrutura interna é consistente, o backlog está integralmente enumerado (exactamente 118 tickets, sem sobreposição nem lacuna), os 14 ADRs são canónicos e idênticos entre âncoras, e a remediação P0 anterior (adição dos docs de Contratos, Modelo de Dados e Matriz de Conformidade) foi **real e de qualidade**, resolvendo três lacunas de completude de primeira ordem.

Contudo, a auditoria confirma que essa mesma remediação **não foi propagada** ao aparato de navegação, aos índices nem ao rasto ascendente do backlog — deixando os artefactos mais recentes e *load-bearing* órfãos das vistas que a eles deveriam conduzir. A isto somam-se três defeitos estruturais: (i) uma **conflação pervasiva** entre a latência do PDP (<15 ms) e o overhead total de mediação, tratada como facto firme onde a fonte a marca "a ratificar por benchmark"; (ii) uma **corrupção de referências de dependência** no EPIC-02 que envia o executor a confirmar o ticket errado; e (iii) **classes inteiras de artefacto em falta** para um blueprint deste calibre — UX/DX (Dimensão 6), decomposição STRIDE e uma matriz de rastreabilidade mecânica.

**Veredicto de prontidão:** *Aprovado com reservas.* A documentação é suficiente para orientar a implementação e comunicar a arquitectura, mas **não deve ser publicada como v1.0 definitiva** antes da remediação P0 aqui prescrita — os elos partidos e a contradição de latência corroem a confiança precisamente nos pontos operacionais (handoffs, SLOs, contratos).

### Scorecard

| # | Dimensão | Score | Veredicto (uma frase) |
|---|----------|:-----:|-----------------------|
| 1 | Completude | **7,5** | Largura e profundidade notáveis, reforçadas pela remediação P0, mas ainda sem casa própria para UX/DX, STRIDE, rastreabilidade com IDs estáveis e alguns contratos internos. |
| 2 | Coerência interna | **7,5** | Fortemente coerente nos invariantes carregados, mas arrasta uma conflação real de NFR e vários artefactos de desactualização deixados pela remediação. |
| 3 | Rigor técnico | **7,0** | Tecnicamente sólido e honesto nas ressalvas, mas minado pela contradição do orçamento de latência, por um exemplo que contradiz a própria política e por uma sobre-afirmação da defesa anti-injecção. |
| 4 | Clareza | **7,0** | Excepcionalmente legível e bem estruturado, mas com a navegação desactualizada e o erro de rotulagem a sobreviver dentro dos handoffs executáveis. |
| 5 | Rastreabilidade de decisões | **6,5** | Cadeia descendente sólida em prosa, mas sem espinha dorsal mecânica (nenhuma matriz ADR×ticket) e com os docs P0 desligados do rasto ascendente. |
| 6 | Viabilidade de execução | **6,5** | Backlog bem estruturado e com DoD verificável, mas com corrupção sistémica de dependências e um caminho crítico não-computável. |

### Score global

**7,0 / 10** — média ponderada uniforme das seis dimensões (7,5 · 7,5 · 7 · 7 · 6,5 · 6,5). O global assenta acima do ponto médio devido à qualidade estrutural e à honestidade técnica, mas é puxado para baixo pelas duas dimensões accionáveis (rastreabilidade e viabilidade), onde vivem as duas únicas constatações **críticas** do corpus.

---

## 2. Metodologia

A auditoria correu em painel de **seis auditores dimensionais independentes**, cada um com um mandato disjunto e com obrigação de citar localização exacta (documento, secção, linha) para cada constatação:

1. **Completude** — cobertura de classes de artefacto que um blueprint de referência exige.
2. **Coerência interna** — contradições e inconsistências *entre* e *dentro* de documentos.
3. **Rigor técnico** — correção e solidez das afirmações técnicas e dos alvos não-funcionais.
4. **Clareza** — legibilidade, navegabilidade e adequação à audiência.
5. **Rastreabilidade de decisões** — cadeia síntese-fonte → ADR → doc técnico → epic → ticket.
6. **Viabilidade de execução** — o backlog AOS como grafo executável "pegar e fazer".

Cada auditor produziu score (0–10), pontos fortes, constatações tipificadas por severidade (crítica/alta/média/baixa) com recomendação, e lacunas. As constatações foram verificadas por *grep* dirigido e contagem factual (ex.: 118 headers `## AOS-NNN` únicos; 51 blocos Mermaid; ranges por epic).

**Contra-auditoria.** A fase de contra-auditoria adversarial **não produziu conteúdo** (entrada nula). Consequentemente, não houve disputa formal entre auditores registada, e o auditor-chefe assumiu a função de reconciliação: as **constatações transversais da Secção 4 foram derivadas por consolidação cruzada** dos seis relatórios (identificação de causas-raiz partilhadas entre dimensões), e não de um relatório de contra-auditoria. Regista-se esta ausência como **limitação metodológica**: nenhuma constatação individual foi sujeita a refutação independente, pelo que os scores devem ler-se como avaliações de painel não-contestadas. Recomenda-se correr a contra-auditoria antes de fechar a v1.1.

---

## 3. Constatações por dimensão

### 3.1 Completude — 7,5

**Pontos fortes.** Remediação P0 real: `tecnica/12` define 5 contratos de porta (C1–C5) com schema, semântica de erro, idempotência e SemVer de porta + política Rego de referência; `tecnica/13` define o envelope de evento append-only, audit hash-chain/WORM reconciliado com crypto-shredding e schema de memória versionado; `tecnica/14` fornece matriz EU AI Act + GDPR → controlo → ticket com estados calibrados. Backlog verificável (118 headers únicos), modelo de domínio ER, máquina de estados durável, >50 diagramas, catálogo de runbooks RB-01..05 ligado a SLI/SLO.

| ID | Sev. | Localização | Problema | Recomendação |
|----|------|-------------|----------|--------------|
| COMP-01 | Alta | `tecnica/` (sem doc/epic UX); `_FONTE §Dim 6` | Dimensão 6 (UX/DX) sem documento nem epic dedicado; contrato da superfície HITL e modelo do approval-card inexistentes (grep = 0). | Criar `tecnica/15_Experiencia_HITL_UX.md` (+ tickets) com contrato de superfície out-of-band, approval-card, paridade Slack/Telegram, progresso/burn-down, calibração de confiança; mapear cada requisito inegociável a AOS-NNN. |
| COMP-02 | Alta | `specs/00 §7-8`; grep `RF-/NFR-` = 0 | Sem matriz de rastreabilidade de requisitos nem IDs estáveis; NFR vivem só em tabelas de drivers sem ID. | Catálogo RF-NN/NFR-NN + matriz RF/NFR/ADR → epic → ticket → teste. |
| COMP-03 | Alta | `tecnica/07 §3`; grep `STRIDE` = 0 | Modelo de ameaças usa OWASP mas sem decomposição por fronteira de confiança (STRIDE), pedida no `_BRIEF`. | Acrescentar matriz STRIDE por fronteira do diagrama de componentes, complementando OWASP. |
| COMP-04 | Média | `tecnica/12 §3` (só C1..C5) | Sem contrato para ORQ↔SCH, SCH↔RT (lease/fencing), RT↔MEM e, sobretudo, a mensagem agente-a-agente (A2A), elevada a primitivo de primeira classe. | Estender `tecnica/12` com essas portas e o envelope A2A (taint, identidade delegada, idempotência). |
| COMP-05 | Média | `tecnica/10 §8` (RB-01..05) | Runbooks não cobrem 1:1 os modos de falha canónicos (supply-chain, prompt injection, outage Broker/Vault, perda de quórum do ES, cache-thrash, esgotamento do pool microVM); RPO/RTO ainda "(proposta)". | Estender catálogo por risco canónico e ratificar RPO/RTO via game-days. |
| COMP-06 | Média | `specs/EPIC-10`; `tecnica/06` | Sem modelo de capacidade/sizing nem FinOps consolidado além do Model Gateway. | Secção de capacidade/FinOps com fórmulas por componente e projecção agregada. |
| COMP-07 | Baixa | Glossários por-documento | Sem glossário-mestre nem registo de ADRs standalone; risco de deriva terminológica. | Publicar `GLOSSARIO.md` e `ADRs.md` mestre referenciados pelos locais. |
| COMP-08 | Baixa | `tecnica/INDICE` (12 vs 15); `specs/INDICE §9` | Índices declaram 12 e 15 documentos em simultâneo após a remediação. | Actualizar contagens para 15 + entrada de changelog. |
| COMP-09 | Baixa | grep `quickstart` = 0 | Sem guia de onboarding / walking skeleton end-to-end. | Adicionar guia de arranque ligando AOS-001..012 a um primeiro fluxo observável. |

**Lacunas:** UX/DX; matriz de rastreabilidade com IDs estáveis; STRIDE; contratos internos + A2A; ~6 runbooks canónicos; capacidade/FinOps; glossário mestre; camada RF enumerada; quickstart; índices desactualizados.

### 3.2 Coerência interna — 7,5

**Pontos fortes.** Ranges AOS-NNN perfeitamente consistentes entre `_BRIEF §8`, `specs/INDICE` e `specs/00 §8` (11 epics, 118 tickets, zero fora de intervalo). 14 ADRs idênticos entre âncoras, sem órfão nem superseding conflituoso. Catálogo de 13 componentes usado à letra. Alvos NFR estáveis reproduzidos coerentemente. 51 diagramas Mermaid = contagem declarada.

| ID | Sev. | Localização | Problema | Recomendação |
|----|------|-------------|----------|--------------|
| COER-01 | Alta | ~15 locais em `tecnica/01`, `tecnica/10`, `specs/EPIC-01/05/08/09/10/INDICE` vs canónico `_BRIEF §4`, `tecnica/00 §9`, `specs/00:294` | Conflação: "overhead de mediação (RM) p95 <15 ms" tratado como SLO firme, quando a fonte separa PDP (<15 ms) do overhead total ("a ratificar por benchmark"). | Distinguir as duas métricas em todo o lado; renomear a agregada e marcá-la "a ratificar". |
| COER-02 | Média | `tecnica/INDICE:169/51`; `specs/INDICE:198` | Contagem do conjunto técnico desactualizada (12 vs 15) dentro e entre ficheiros. | Actualizar para 15 + linha de changelog v1.1. |
| COER-03 | Média | `tecnica/INDICE:156-157` | Afirmação "cada doc técnico mapeia para o epic homónimo" é falsa (tecnica/11↔EPIC-11; 00→System Spec; 12–14 sem epic). | Substituir por tabela explícita de mapeamento com excepções. |
| COER-04 | Média | `tecnica/INDICE §3.1/3.2/3.3/§4` | Docs 12–14 ausentes de todas as vistas de navegação. | Acrescentar 12–14 às vistas por perfil/camada/fase e ao diagrama de hierarquia. |
| COER-05 | Baixa | `tecnica/14:42,60,104,125` | Formas PT-BR ("ativar/ativação") num corpus PT-PT. | Corrigir para "activar/activação". |
| COER-06 | Baixa | `tecnica/12`, `tecnica/14` (1 Mermaid cada) | Viola `_BRIEF §7` (mínimo 2 diagramas por doc). | Acrescentar 2.º diagrama ou registar excepção. |
| COER-07 | Baixa | `tecnica/INDICE:35` | Metadados de dimensão (linhas) desactualizados. | Recalcular ou usar intervalos. |

**Lacunas:** ausência de doc técnico de Testes/Qualidade (contraparte do EPIC-11); ausência de gate de integridade de cross-references; docs 12–14 fora do catálogo canónico do `_BRIEF`; ausência de lint de reciprocidade Dependências↔Bloqueia.

### 3.3 Rigor técnico — 7,0

**Pontos fortes.** Nota de calibração explícita que redefine "arquitecturalmente impossível" como objectivo de desenho com risco residual gerido; durabilidade caveatada como at-least-once com idempotência; ressalva de que a assinatura garante origem mas não veracidade; crypto-shredding correcto (hash sobre ciphertext preserva a cadeia); lease/heartbeat + fencing token à la Kleppmann; migração expand/contract não-destrutiva.

| ID | Sev. | Localização | Problema | Recomendação |
|----|------|-------------|----------|--------------|
| RIG-01 | Alta | `tecnica/01/09`, `specs/EPIC-01/05/08/10` vs `tecnica/00 §9`, `specs/00 §14` | "<15 ms" aplicado incoerentemente ao overhead total e hard-committed como critério de aceitação; fisicamente implausível (CAS + broker→Vault + append ES replicado + egress no mesmo orçamento do PDP). | Fixar definição canónica única; substituir por orçamento agregado decomposto e "a ratificar"; remover número dos critérios SMART. |
| RIG-02 | Alta | `tecnica/12 §4` (exemplo C1) vs `§9` (Rego) | O exemplo trabalhado contradiz a própria política de referência: input com `taint=untrusted` e authority sem `http.post` seria NEGADO, mas a response diz "permit"; obligation `ttl` que nenhuma regra produz. | Corrigir o exemplo para ser consistente; adicionar teste de contrato (gate 7) que valide par exemplo↔política. |
| RIG-03 | Alta | `tecnica/07 §6`; `tecnica/12 §9`; ADR-005 | Defesa anti-injecção apresentada como "estruturalmente impedida"; à letra nega toda a acção sobre dados tainted (agente inútil), relaxada deixa de impedir. Mecanismo de uso de dados tainted nunca especificado; limitação residual do CaMeL não reconhecida. | Rebaixar para "mitigado com risco residual"; especificar capabilities data-flow-scoped; citar limitação do CaMeL nos riscos. |
| RIG-04 | Média | `_BRIEF §4`, `tecnica/07 §4`, `EPIC-07` | "cold-start <125 ms (restore 5–30 ms)" atribuído indistintamente a Firecracker/Kata/gVisor, quando são figuras específicas do Firecracker. | Qualificar por substrato ou marcar como específico do Firecracker+snapshot com fonte. |
| RIG-05 | Média | ADR-004; `EPIC-07 AOS-064` | gVisor tratado como microVM/"isolamento ao nível do kernel"; é kernel em user-space (Sentry), não virtualização de hardware. | Corrigir nomenclatura e distinguir risco residual no threat model. |
| RIG-06 | Média | ADR-007; `EPIC-01 L146` | NATS/Redis/Postgres apresentados como backends intercambiáveis da fonte de verdade, com garantias de durabilidade radicalmente diferentes (Redis async; LISTEN/NOTIFY não-durável). | Definir requisitos mínimos de durabilidade e classificar cada opção; marcar Redis Streams inadequado como fonte de verdade. |
| RIG-07 | Média | `tecnica/03 §4-5`; ADR-008 | Contador global de admissão (CAS) é primitivo de forte consistência; trade-off CAP sob partição nunca analisado apesar do alvo 99,9% sem SPOF. | Analisar trade-off (token-bucket local + reconciliação, headroom) e quantificar contribuição para a latência. |
| RIG-08 | Baixa | ADR-014; `EPIC-09 AOS-090` | Critério de promoção "erro <2% por 30 dias" arbitrário e global, incoerente com nível por par (agente, domínio). | Função do impacto/reversibilidade; definir gatilhos de demoção operacionalmente. |
| RIG-09 | Baixa | `tecnica/08 §3`; DoD | OTel GenAI semconv tratada como estável e como gate de DoD, sendo experimental em 2026. | Assinalar estatuto experimental, pinar versão, prever fallback. |
| RIG-10 | Baixa | `tecnica/06 §7`; `EPIC-06 AOS-061` | "cache-hit >80%" como SLI universal ignora semântica por provider (TTL, breakpoints) e método de medição. | Definir a métrica exacta e condicionar o alvo à semântica de cada provider. |

**Lacunas:** orçamento de latência decomposto nunca numericamente alocado; nenhum NFR ancorado a benchmark/fonte; ausência de análise CAP do contador de admissão; mecanismo de uso de dados tainted; matriz de durabilidade dos backends; risco residual por substrato de sandbox; fronteira de idempotência para serviços não-idempotentes; gatilhos de demoção de autonomia.

### 3.4 Clareza — 7,0

**Pontos fortes.** Introdução uniforme e bem ordenada em todos os docs; perfis de leitor por papel com ordem de leitura; tickets AOS-NNN auto-contidos com handoff colável; diagramas que clarificam (regra anti-`;` em sequenceDiagram cumprida); glossário por doc; nota de calibração repetida que desarma a leitura céptica.

| ID | Sev. | Localização | Problema | Recomendação |
|----|------|-------------|----------|--------------|
| CLR-01 | Alta | `specs/EPIC-02` l.62, l.188 | Erro de rotulagem ticket→componente só parcialmente corrigido: prosa e handoff executável ainda mandam confirmar AOS-005 (Identidade) onde a dependência real é AOS-002 (Event Store); o doc discorda de si próprio. | Alinhar todas as etiquetas ao mapeamento canónico do EPIC-01; adicionar lint de CI ID↔título. |
| CLR-02 | Alta | `tecnica/INDICE §3.1/3.2/3.3/§4/§5` | Docs 12–14 órfãos de todo o aparato de navegação; um leitor nunca lá chega. | Integrar 12/13/14 em perfis, camadas, fases, hierarquia e mapa ADR. |
| CLR-03 | Média | `tecnica/INDICE:169`; `specs/INDICE:198` | Contagens desactualizadas (12 vs 15) nos ficheiros de navegação de topo. | Actualizar para 15 + linha de versão preservando rasto. |
| CLR-04 | Média | `tecnica/00`; `specs/01 §4` | Acrónimos sem expansão na 1ª ocorrência (WORM, TCB, DSAR, TPM/RPM, SA-ROC, HITL, SBOM, DORA) contra a convenção do `_BRIEF §9`. | Expandir na 1ª ocorrência + glossário-mestre de acrónimos. |
| CLR-05 | Baixa | Bloco de metadados (campo "Documento") | Formato não uniforme; `00` tem tautologia "Arquitectura de Solução — Documento de Arquitectura de Solução". | Fixar prefixo `<tipo>` e capitalização; validar por lint. |
| CLR-06 | Baixa | `specs/00 §2.1`; `tecnica/00 §13` | Muros de texto em passagens que deviam ser das mais legíveis. | Partir em bullets (um por modo de falha / fase). |
| CLR-07 | Baixa | `tecnica/12`, `tecnica/14`; EPICs sem Mermaid | Dois docs <2 diagramas; 118 tickets sem qualquer diagrama de dependências. | 2.º Mermaid nos docs; mini-flowchart de dependências no topo de cada epic. |

**Lacunas:** glossário de acrónimos consolidado; lint documental no CI; processo de actualização da navegação ao adicionar docs; indicação de dificuldade/tempo de leitura; visão gráfica de dependências por epic.

### 3.5 Rastreabilidade de decisões — 6,5

**Pontos fortes.** Catálogo de 14 ADRs idêntico nos três locais-âncora, sem deriva. Cobertura descendente real: nenhum ADR órfão, 111/118 tickets citam ≥1 ADR. Todos os 118 tickets têm "Documentos de referência" preenchido. Existe mapa Capacidade→Componente→Epic e mapa ADR→doc. `tecnica/14` é uma matriz de rastreabilidade genuína e calibrada (artigo→controlo→ticket→estado).

| ID | Sev. | Localização | Problema | Recomendação |
|----|------|-------------|----------|--------------|
| RAST-01 | **Crítica** | `specs/EPIC-01..11` vs `tecnica/13`, `tecnica/14` | Docs P0 desligados do backlog no sentido ascendente: grep = ZERO referências a `tecnica/13` e `tecnica/14`; `tecnica/12` só em `specs/01`. Tickets load-bearing (AOS-002/011/035+/072/087..097) não apontam para os docs que os justificam. | Actualizar "Documentos de referência" dos tickets afectados; adicionar 12/13/14 a "Documentos relacionados" dos epics 01/02/04/07/08/09. |
| RAST-02 | Alta | `tecnica/00 §8`; `specs/00 ADRs` | Não existe matriz ADR×ticket; a tabela de ADRs não tem Status/Consequências/Implementado-por; o elo vive só em prosa, impossibilitando verificação mecânica e impacto-análise reversa. | Coluna "Tickets" na tabela de ADRs + campo "ADRs" no metadado de cada ticket; verificável por script no CI. |
| RAST-03 | Média | AOS-007/009/010/012/108/109/110 | 7 tickets não citam qualquer ADR, alguns controlos P0 de segurança. | Anotar ADR de origem; nenhum ticket sem ≥1 ADR/NFR. |
| RAST-04 | Média | `_BRIEF §4`; `tecnica/00`; `specs/00 §7` | Drivers NFR sem identificador estável (sem NFR-NN); rasto NFR→ticket implícito por coincidência de valor. | IDs NFR-01..10 + coluna "Ticket(s) de verificação". |
| RAST-05 | Média | `_FONTE` ↔ `_BRIEF §3`/`tecnica/00 §8` | Elo síntese-fonte→ADR afirmado só globalmente, nunca itemizado; impossível detectar tese sem ADR ou ADR sem base. | Coluna "Origem (_FONTE §)" nos ADRs; marcar "(proposta)" os que excedem a fonte. |
| RAST-06 | Baixa | `tecnica/14 §3-4`; ADR-014 | Assimetrias residuais: matriz aponta para 21 tickets sem retorno; ADR-014 (L0–L5) só com 3 tickets. | Fechar ciclo bidireccional; rever cobertura de L0–L5. |
| RAST-07 | Baixa | `tecnica/INDICE:169/51`; `specs/INDICE:198` | Evolução do próprio conjunto documental não rastreável; adição de 12/13/14 sem entrada de versão. | Corrigir contadores + linha de versão v1.1. |

**Lacunas:** matriz ADR×ticket (artefacto central em falta); campo "ADRs" no metadado; IDs de NFR + índice reverso; coluna "Origem (_FONTE §)"; ligação ascendente aos docs P0; campos Status/Consequências nos ADRs; registo de versão dos docs 12/13/14.

### 3.6 Viabilidade de execução — 6,5

**Pontos fortes.** DoD específica do domínio e verificável (idempotência com teste de reexecução, replay resume-from-step com hashes, mediação sem bypass, spans OTel+custo, eval-gate) — não é boilerplate. Handoffs concretos e coláveis, com "não expandas escopo / PÁRA e pergunta". Critérios maioritariamente SMART. Nenhum XL; spike usado onde há incerteza real. O corpus antecipa o próprio modo de falha (gate 2b "Lint de referências cruzadas").

| ID | Sev. | Localização | Problema | Recomendação |
|----|------|-------------|----------|--------------|
| VIAB-01 | **Crítica** | `specs/EPIC-02 §3` (l.49–58, 62), handoffs AOS-014/018 | Tabela-resumo e handoffs referem numeração obsoleta que contradiz o EPIC-01 e os próprios metadados: mandam "Confirma AOS-005 Done" (=Identidade) onde precisam de AOS-002 (Event Store). O executor é enviado a confirmar/bloquear o ticket errado; falsifica a invariante do gate 2b. | Corrigir tabela e todos os handoffs para a numeração canónica; executar o gate 2b sobre todo o corpus antes da v1.1; teste de CI que rejeite rótulos discrepantes. |
| VIAB-02 | Alta | `specs/EPIC-11` (AOS-111/112/113/117/118) | Tickets de teste dependem só da infra de teste e OMITEM a feature sob teste; o grafo agendaria testes antes de existir o que testam. | Acrescentar dependências feature→teste (111→016, 112→014, 113→004, 117→064..075, 118→100/101/102). |
| VIAB-03 | Alta | Deps cross-epic em EPIC-04/05/06/08/09/10 | Dependências a granularidade de EPIC (não de ticket) tornam o caminho crítico não-computável e criam inversões de fase latentes. | Substituir todas as deps de nível-epic por IDs de ticket específicos. |
| VIAB-04 | Média | AOS-002 vs 100; AOS-016 vs 079 vs 024 vs 111 | Sobreposição de escopo sem fronteira de DoD (replicação em AOS-002 e AOS-100; captura de inputs em AOS-016 e AOS-079). | Declarar "o que é entregue aqui vs lá" ou fundir; cravar o delta mensurável. |
| VIAB-05 | Média | `EPIC-02 AOS-022` | Um ticket agrega spike time-boxed + feature de produção bloqueada por ratificação; não é atomicamente encerrável e o "L" subestima (efectivamente XL). | Desdobrar em AOS-022a (spike→ADR) e AOS-022b (feature). |
| VIAB-06 | Média | AOS-069/046/093/064/065/070 | Proibição de XL + tickets XL rotulados L produz subestimação sistemática (taint dual-LLM, 3 transportes MCP num ticket, crypto-shredding+vault). | Decompor os L's claramente XL; permitir XL só para spikes time-boxed. |
| VIAB-07 | Baixa | `EPIC-09 AOS-095`; `INDICE §7` | Cross-refs desalinhadas: AOS-095 atribui `waiting_on_human` ao EPIC-01 (definido em AOS-017/EPIC-02); setas do mapa §7 desinformam. | Corrigir dep de AOS-095 para AOS-017; reconciliar/gerar o diagrama a partir dos campos reais. |
| VIAB-08 | Baixa | `specs/EPIC-10` (AOS-098..108) | Cabeçalhos a negrito em vez de `###`; qualquer gate de parsing por `###` ignora as secções do EPIC-10. | Uniformizar para `###`. |
| VIAB-09 | Baixa | `INDICE §3.2` vs campo Fase por ticket | Narrativa de epic-por-fase contradiz os campos Fase por ticket (dispersos). | Substituir por contagem de tickets por fase; a unidade faseável é o ticket. |

**Lacunas:** grafo de dependências consolidado por ticket + ordem topológica; roll-up de esforço e comprimento do caminho crítico; plano de alocação perfil→tickets; reciprocidade Dependências↔Bloqueia; critério explícito de atomicidade.

---

## 4. Constatações transversais

Sem contra-auditoria formal (entrada nula — ver §2), o auditor-chefe consolidou os seis relatórios e identificou **quatro causas-raiz partilhadas** que atravessam várias dimensões. São estas, e não constatações isoladas, o eixo do plano de remediação.

### T-1 — A remediação P0 corrigiu conteúdo mas não foi propagada *(a causa-raiz dominante)*
A adição dos docs 12–14 resolveu COMP-01/02/03 (nível de conteúdo), mas criou ou deixou em aberto um enxame de defeitos de integração que reaparece em **cinco dimensões**: docs órfãos da navegação (COER-04, CLR-02), índices contraditórios 12 vs 15 (COMP-08, COER-02, CLR-03, RAST-07), mapeamento homónimo falso (COER-03), e — mais grave — **ausência de rasto ascendente** dos tickets para os docs que os justificam (RAST-01, crítica). **Decisão do chefe:** trata-se de um único defeito de processo (remediar sem *checklist* de propagação), não de sete defeitos independentes. A sua correção é o coração do P0.

### T-2 — Conflação do orçamento de latência de mediação (<15 ms)
A mesma contradição é reportada independentemente pela Coerência (COER-01) e pelo Rigor (RIG-01): o número "<15 ms", canónico só para a avaliação do PDP, é aplicado ao overhead **total** de mediação — e chega a ser *hard-committed* como critério de aceitação e limiar de alerta, contra a marca "a ratificar por benchmark" da própria fonte. **Decisão do chefe:** confirmada como contradição interna directa e fisicamente implausível; a definição canónica da fonte (`specs/00 §14`, `tecnica/00 §9`) é a correcta e deve prevalecer sobre os ~15 locais divergentes.

### T-3 — Corrupção de referências de dependência no EPIC-02
A Clareza (CLR-01) e a Viabilidade (VIAB-01, crítica) descrevem o mesmo defeito: rotulagem ticket→componente obsoleta que sobreviveu a uma remediação parcial e vive nos **handoffs executáveis**, enviando o Claude Code a confirmar o ticket errado. **Decisão do chefe:** é a constatação operacionalmente mais perigosa do corpus (afecta a execução directa), pelo que herda a severidade mais alta atribuída por qualquer auditor — **crítica**.

### T-4 — Ausência de espinha dorsal mecânica (matrizes + lint de CI)
Três dimensões convergem na mesma lacuna estrutural: sem matriz ADR×ticket (RAST-02), sem IDs de requisito (COMP-02, RAST-04), sem grafo de dependências computável (VIAB-03) e sem lint documental no CI, o corpus **não consegue provar cobertura nem impedir regressões** — e foi exactamente essa ausência que permitiu a T-1, T-2 e T-3 sobreviverem. **Decisão do chefe:** a mecanização é a fronteira entre o nível de maturidade actual e o seguinte (ver §7).

### Disputas
Não se registaram disputas entre auditores (contra-auditoria nula). Os scores dimensionais são, portanto, avaliações de painel não-contestadas. O único ponto onde os auditores **convergem espontaneamente** (T-1 a T-4) reforça a confiança nessas constatações; inversamente, as constatações de severidade baixa não sujeitas a refutação devem ser lidas com margem.

---

## 5. Registo priorizado de constatações

Todas as constatações confirmadas, ordenadas por severidade. As colunas "→ Transversal" ligam à causa-raiz da §4. Constatações que partilham causa-raiz mantêm IDs distintos (localizações e recomendações distintas) mas resolvem-se em conjunto.

| # | ID | Dimensão | Sev. | → Transv. | Localização | Recomendação (curta) |
|---|----|----------|------|-----------|-------------|----------------------|
| 1 | RAST-01 | Rastreabilidade | **Crítica** | T-1 | `specs/EPIC-*` ↔ `tecnica/13,14` | Ligar tickets load-bearing aos docs P0. |
| 2 | VIAB-01 | Viabilidade | **Crítica** | T-3 | `specs/EPIC-02 §3`, handoffs | Corrigir numeração de dependências + gate 2b real. |
| 3 | COMP-01 | Completude | Alta | — | sem doc UX | Criar `tecnica/15` HITL/UX + tickets. |
| 4 | COMP-02 | Completude | Alta | T-4 | `specs/00 §7-8` | Catálogo RF/NFR + matriz de rastreabilidade. |
| 5 | COMP-03 | Completude | Alta | — | `tecnica/07 §3` | Matriz STRIDE por fronteira de confiança. |
| 6 | COER-01 | Coerência | Alta | T-2 | 15 locais vs canónico | Distinguir PDP vs overhead total. |
| 7 | RIG-01 | Rigor | Alta | T-2 | idem | Orçamento decomposto "a ratificar"; sair dos critérios SMART. |
| 8 | RIG-02 | Rigor | Alta | — | `tecnica/12 §4` vs `§9` | Corrigir exemplo C1 ↔ política; teste de contrato. |
| 9 | RIG-03 | Rigor | Alta | — | `tecnica/07 §6`; ADR-005 | Rebaixar linguagem; especificar uso de dados tainted. |
| 10 | CLR-01 | Clareza | Alta | T-3 | `EPIC-02` l.62/188 | Alinhar prosa + handoffs ao canónico. |
| 11 | CLR-02 | Clareza | Alta | T-1 | `tecnica/INDICE` | Integrar 12–14 na navegação. |
| 12 | RAST-02 | Rastreabilidade | Alta | T-4 | `tecnica/00 §8` | Matriz ADR×ticket + campo ADRs no metadado. |
| 13 | VIAB-02 | Viabilidade | Alta | — | `EPIC-11` | Dependências feature→teste. |
| 14 | VIAB-03 | Viabilidade | Alta | T-4 | deps cross-epic | Deps por ID de ticket, não por epic. |
| 15 | COMP-04 | Completude | Média | — | `tecnica/12 §3` | Contratos ORQ↔SCH/SCH↔RT/RT↔MEM + A2A. |
| 16 | COMP-05 | Completude | Média | — | `tecnica/10 §8` | Runbooks por risco canónico; ratificar RPO/RTO. |
| 17 | COMP-06 | Completude | Média | — | `EPIC-10`/`tecnica/06` | Modelo de capacidade + FinOps. |
| 18 | COER-02 | Coerência | Média | T-1 | `INDICE` | Contagem 12→15 + changelog. |
| 19 | COER-03 | Coerência | Média | T-1 | `tecnica/INDICE:156` | Tabela de mapeamento com excepções. |
| 20 | COER-04 | Coerência | Média | T-1 | `tecnica/INDICE §3/§4` | Docs 12–14 nas vistas. |
| 21 | RIG-04 | Rigor | Média | — | `_BRIEF §4`; `EPIC-07` | Qualificar 125 ms por substrato. |
| 22 | RIG-05 | Rigor | Média | — | ADR-004; `AOS-064` | gVisor ≠ microVM; risco por fronteira. |
| 23 | RIG-06 | Rigor | Média | — | ADR-007 | Requisitos de durabilidade do ES; Redis inadequado. |
| 24 | RIG-07 | Rigor | Média | — | `tecnica/03`; ADR-008 | Analisar CAP do contador de admissão. |
| 25 | CLR-03 | Clareza | Média | T-1 | `INDICE` | Contagens 12→15 nos ficheiros de navegação. |
| 26 | CLR-04 | Clareza | Média | — | `tecnica/00` | Expandir acrónimos na 1ª ocorrência. |
| 27 | RAST-03 | Rastreabilidade | Média | T-4 | 7 tickets | Anotar ADR de origem. |
| 28 | RAST-04 | Rastreabilidade | Média | T-4 | drivers NFR | IDs NFR-NN + ticket de verificação. |
| 29 | RAST-05 | Rastreabilidade | Média | — | `_FONTE`↔ADR | Coluna "Origem (_FONTE §)". |
| 30 | VIAB-04 | Viabilidade | Média | — | AOS-002/100, 016/079 | Fronteira de DoD entre tickets sobrepostos. |
| 31 | VIAB-05 | Viabilidade | Média | — | AOS-022 | Desdobrar spike/feature. |
| 32 | VIAB-06 | Viabilidade | Média | — | AOS-069/046/093… | Decompor L's que são XL. |
| 33 | COMP-07 | Completude | Baixa | T-1 | glossários | Glossário/ADRs mestre. |
| 34 | COMP-08 | Completude | Baixa | T-1 | `INDICE` | Índices 12→15. |
| 35 | COMP-09 | Completude | Baixa | — | grep quickstart=0 | Guia de arranque / walking skeleton. |
| 36 | COER-05 | Coerência | Baixa | — | `tecnica/14` | PT-BR→PT-PT ("activar"). |
| 37 | COER-06 | Coerência | Baixa | — | `tecnica/12,14` | 2.º diagrama Mermaid. |
| 38 | COER-07 | Coerência | Baixa | — | `tecnica/INDICE:35` | Recalcular metadados de linhas. |
| 39 | RIG-08 | Rigor | Baixa | — | ADR-014 | Limiar de autonomia por impacto. |
| 40 | RIG-09 | Rigor | Baixa | — | `tecnica/08 §3` | Pinar OTel GenAI semconv (experimental). |
| 41 | RIG-10 | Rigor | Baixa | — | `tecnica/06 §7` | Definir cache-hit por provider. |
| 42 | CLR-05 | Clareza | Baixa | — | metadados | Formato único do campo "Documento". |
| 43 | CLR-06 | Clareza | Baixa | — | `specs/00 §2.1` | Partir muros de texto em bullets. |
| 44 | CLR-07 | Clareza | Baixa | — | docs/EPICs | Diagramas em falta. |
| 45 | RAST-06 | Rastreabilidade | Baixa | — | `tecnica/14`; ADR-014 | Fechar ciclo bidireccional; rever L0–L5. |
| 46 | RAST-07 | Rastreabilidade | Baixa | T-1 | `INDICE` | Linha de versão dos docs 12–14. |
| 47 | VIAB-07 | Viabilidade | Baixa | T-3 | AOS-095; `§7` | Corrigir dep para AOS-017; gerar diagrama. |
| 48 | VIAB-08 | Viabilidade | Baixa | — | `EPIC-10` | Cabeçalhos `###` uniformes. |
| 49 | VIAB-09 | Viabilidade | Baixa | — | `INDICE §3.2` | Fase por ticket, não por epic. |

**Contagem:** 2 críticas · 12 altas · 18 médias · 17 baixas = **49 constatações confirmadas**.

---

## 6. Plano de remediação

Acções agrupadas por prioridade; cada uma nomeia as constatações que resolve. As acções P0 são **pré-condição da publicação da v1.0 definitiva**.

### P0 — Bloqueadores da publicação (correcção de elos partidos e contradições)

- **P0-A — Fechar o rasto ascendente da remediação.** Popular "Documentos de referência" dos tickets load-bearing com `tecnica/12/13/14` e "Documentos relacionados" dos epics 01/02/04/07/08/09. *Resolve:* **RAST-01 (crítica)**, parcialmente T-1.
- **P0-B — Sanar as referências de dependência do EPIC-02.** Alinhar tabela-resumo, notas em prosa e handoffs executáveis ao mapeamento canónico (AOS-002=ES, AOS-003=RM, AOS-005=Identidade); correr o gate 2b sobre todo o corpus. *Resolve:* **VIAB-01 (crítica)**, CLR-01, VIAB-07 (T-3).
- **P0-C — Definição canónica única do orçamento de latência.** Manter "PDP eval p95 <15 ms" como SLO firme; renomear e decompor o overhead total de mediação, marcado "a ratificar por benchmark"; retirar o número dos critérios de aceitação SMART. *Resolve:* COER-01, RIG-01 (T-2).
- **P0-D — Corrigir o exemplo C1 vs política Rego.** Tornar o exemplo consistente com `§9` e adicionar o teste de contrato do gate 7. *Resolve:* RIG-02.
- **P0-E — Rebaixar a linguagem anti-injecção e especificar o mecanismo.** "Mitigado com risco residual"; especificar capabilities data-flow-scoped; citar a limitação do CaMeL. *Resolve:* RIG-03.
- **P0-F — Propagar a remediação à navegação e índices.** Integrar 12–14 em perfis/camadas/fases/hierarquia/mapa ADR; corrigir contagens 12→15 com linha de changelog v1.1. *Resolve:* CLR-02, COER-02/03/04, COMP-08, CLR-03, RAST-07 (T-1).
- **P0-G — Introduzir o lint documental no CI.** Gates de: ID-de-ticket↔título-de-componente, contagem de docs, reciprocidade Dependências↔Bloqueia, cabeçalhos `###`, links relativos. *Habilita e protege* todo o P0 e é a base de T-4.

### P1 — Fechar as lacunas de classe de artefacto e a espinha mecânica

- **P1-A — UX/DX (Dimensão 6).** Criar `tecnica/15_Experiencia_HITL_UX.md` + tickets, mapeando cada requisito inegociável a AOS-NNN. *Resolve:* COMP-01.
- **P1-B — Rastreabilidade mecânica.** Catálogo RF/NFR-NN + matriz RF/NFR/ADR→epic→ticket→teste; matriz ADR×ticket com Status/Consequências; campos "ADRs" e "Origem (_FONTE §)". *Resolve:* COMP-02, RAST-02/03/04/05 (T-4).
- **P1-C — STRIDE.** Matriz por fronteira de confiança complementando OWASP. *Resolve:* COMP-03.
- **P1-D — Grafo de execução computável.** Deps por ID de ticket (não por epic); dependências feature→teste; desdobrar AOS-022; decompor os L's que são XL; fronteiras de DoD entre tickets sobrepostos. *Resolve:* VIAB-02/03/04/05/06.
- **P1-E — Contratos e runbooks em falta.** Portas ORQ↔SCH/SCH↔RT/RT↔MEM + A2A; runbooks por risco canónico + ratificação RPO/RTO. *Resolve:* COMP-04, COMP-05.
- **P1-F — Rigor dos NFR.** Qualificar 125 ms por substrato; gVisor≠microVM; requisitos de durabilidade do ES; análise CAP do contador de admissão. *Resolve:* RIG-04/05/06/07.

### P2 — Polimento e robustez de longo prazo

- **P2-A — Capacidade/FinOps** (COMP-06); **quickstart/walking skeleton** (COMP-09).
- **P2-B — Glossário e registo de ADRs mestre** + expansão de acrónimos + formato de metadados (COMP-07, CLR-04/05).
- **P2-C — Higiene de rigor de baixo risco:** limiar de autonomia por impacto (RIG-08), pinar OTel GenAI (RIG-09), definição de cache-hit por provider (RIG-10).
- **P2-D — Legibilidade e diagramas:** partir muros de texto, 2.º Mermaid, diagramas de dependência por epic, PT-BR→PT-PT, metadados de linhas, fase por ticket (CLR-06/07, COER-05/06/07, VIAB-08/09, RAST-06).

---

## 7. Veredicto de prontidão

### Escala de maturidade documental (D0–D4)

| Nível | Nome | Critério |
|-------|------|----------|
| **D0** | Ad-hoc | Documentação dispersa, sem estrutura comum nem catálogo de decisões. |
| **D1** | Estruturado | Estrutura interna consistente, catálogo de componentes/ADRs, backlog enumerado, diagramas que clarificam. |
| **D2** | Rastreável (em prosa) | Cadeia decisão→ticket presente e navegável, mas mantida manualmente em texto; cobertura demonstrada por leitura, não por máquina. |
| **D3** | Verificável | Matrizes mecânicas (ADR×ticket, RF/NFR×teste), grafo de execução computável, lint documental no CI que impede regressões e drift. |
| **D4** | Vivo / auto-consistente | Artefactos de navegação e matrizes gerados a partir de fonte única; glossário mestre; zero drift estrutural entre edições. |

### Posicionamento do AOS

O corpus atinge plenamente **D1** e ocupa **D2, mas de forma frágil**: a cadeia descendente ADR→ticket existe e é densa, porém o rasto **ascendente** está partido (RAST-01), os índices contradizem-se (T-1) e não há um único mecanismo automático a defender a consistência. Fixa-se o nível actual em **D2⁻ (Rastreável em prosa, com elos partidos)**.

**O que falta para D3 (Verificável):** (1) fechar os elos partidos — todo o **P0**; (2) construir as matrizes mecânicas ADR×ticket e RF/NFR×teste com IDs estáveis — **P1-B**; (3) tornar o grafo de dependências computável por ID de ticket — **P1-D**; (4) tornar o **lint documental (P0-G)** um gate de *build* que rejeite drift de contagem, rotulagem e reciprocidade. Concluídos o P0 e o P1-B/D, o AOS transita para **D3** — o nível adequado a um "blueprint de referência standalone destinado a qualquer equipa que implemente".

D4 fica fora do âmbito imediato; requer geração automática das vistas e matrizes a partir de fonte única (parte do P2), sensato apenas depois de D3 estabilizado.

---

## 8. Conclusão

A documentação do AOS é **substancialmente boa** e merece ser dito sem rodeios: a arquitectura está bem pensada, o backlog é integralmente enumerado e coerente, os ADRs são canónicos, e o corpus distingue-se por uma **honestidade técnica invulgar** — calibra as suas próprias afirmações absolutas, ressalva a durabilidade e a semântica de assinatura, e reconhece limites onde documentos menos rigorosos afirmariam garantias. A remediação P0 anterior foi real e de qualidade.

Os buracos, porém, são igualmente reais e concentram-se num **defeito de processo, não de intelecto**: a última remediação corrigiu conteúdo sem propagar as consequências, deixando os artefactos mais recentes órfãos da navegação e do rasto do backlog, e permitindo que uma contradição de latência e uma corrupção de referências de dependência sobrevivessem até aos handoffs executáveis. As duas únicas constatações **críticas** do corpus (RAST-01 e VIAB-01) são precisamente estas — e são inteiramente remediáveis com esforço modesto e cirúrgico.

O caminho é claro e curto: executar o **P0** (fechar elos, definir a latência canónica, ligar os docs ao backlog, introduzir o lint) desbloqueia a publicação da v1.0 definitiva; o **P1** (UX/DX, STRIDE, matrizes mecânicas, grafo computável) eleva o corpus a maturidade **D3 — Verificável**. Uma nota metodológica honesta: a ausência de contra-auditoria significa que estas conclusões não foram adversarialmente refutadas; recomenda-se essa fase antes de fechar a v1.1.

**Veredicto final: Aprovado com reservas — score global 7,0/10.** Um blueprint forte que está a uma remediação P0 disciplinada de ser um blueprint de referência exemplar.

---
*Fim do relatório.*