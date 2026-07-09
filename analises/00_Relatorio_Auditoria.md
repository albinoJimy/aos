# Relatório de Auditoria à Documentação — AOS

> **Produto:** AOS (Agentic Operating System)
> **Versão:** 1.0
> **Data:** Julho 2026
> **Classificação:** Documento de Referência — Aberto
> **Auditor-chefe:** Consolidação do painel de auditoria (6 dimensões + contra-auditoria)
> **Âmbito auditado:** corpus `tecnica/` (15 documentos: 00–14) e `specs/` (System Spec, 11 EPICs, 118 tickets AOS-001..118, 14 ADRs), índices e ficheiros de análise.

---

## 1. Sumário executivo

O AOS é um **blueprint de arquitectura de largura e profundidade invulgares**: 15 documentos técnicos, 118 tickets íntegros e acionáveis, 14 ADRs propagados de forma consistente, >50 diagramas, e uma remediação P0 real e verificável (contratos de porta, modelo de dados/eventos, matriz de conformidade EU AI Act/GDPR). A honestidade de calibração técnica é acima da média do sector.

Contudo, **não está pronto para ser adoptado como "implementation-ready"**. Três problemas estruturais atravessam todas as dimensões e não se resolvem editorialmente:

1. **Não existe esquema de identificadores de requisito** (RF-/NFR-/SLO- = 0 ocorrências em todo o corpus). Sem requisitos identificáveis, a "integridade do backlog" prova apenas coerência de tickets *entre si* — não cobertura do espaço de requisitos. **O score alto de Completude é, em parte, ilusório.**
2. **Vários claims load-bearing são afirmados, não derivados**, e alguns são fisicamente implausíveis: overhead total de mediação <15 ms, replay "determinístico" via `seed` que a API Anthropic não expõe, taint transitivo "estruturalmente impedido", 99,9% sobre cadeia serial fail-closed, e um Reference Monitor simultaneamente "pequeno/verificável" e responsável por tudo.
3. **O processo de remediação não é fiável.** Findings marcados como resolvidos persistem (STRIDE, IDs de requisito, exemplo C1 vs política Rego), jargão de auditoria vaza para o documento publicado, e há colisão de IDs de finding entre rondas.

### Scorecard

| # | Dimensão | Score | Veredicto (uma frase) |
|---|----------|:-----:|-----------------------|
| 1 | **Completude** | **7,5** | Largura e profundidade notáveis com P0 sólido, mas faltam classes de artefacto que um blueprint de referência exige (UX/DX, RTM, STRIDE, capacidade/FinOps, onboarding). |
| 2 | **Coerência interna** | **7,0** | Estruturalmente muito coerente (tickets/ADRs/dependências íntegros), maculada por uma contradição de latência pervasiva e contagens desatualizadas pós-remediação. |
| 3 | **Rigor técnico** | **7,0** | Tecnicamente sólido e honesto, mas com contradição de latência nos critérios, exemplo de contrato que viola a própria política, e sobre-afirmações que sobreviveram a duas rondas. |
| 4 | **Clareza** | **7,5** | Excepcionalmente legível; a P0.1b fechou o defeito crítico de rotulagem, mas deixou 12/13/14 órfãos da navegação e a higiene terminológica por fazer. |
| 5 | **Rastreabilidade** | **6,5** | Cadeia ascendente ticket→ADR forte (0 órfãos), mas sem rasto descendente, sem IDs de requisito e sem matriz de rastreabilidade. |
| 6 | **Viabilidade de execução** | **7,0** | Backlog maioritariamente executável (SMART, DoD, grafo acíclico), comprometido por inversões de fase, um ticket não-atómico e tickets tempo-dependentes não implementáveis. |

**Score global: 6,9 / 10** — a média aritmética das dimensões (~7,1) é deliberadamente ajustada para baixo porque a contra-auditoria demonstra que dois dos scores mais altos (Completude, Coerência) assentam sobre bases que o cruzamento de dimensões revela mais frágeis do que parecem em silo.

---

## 2. Metodologia

A auditoria foi conduzida por um **painel de 6 auditores em silo**, cada um responsável por uma dimensão de qualidade documental, seguido de uma **fase de contra-auditoria adversarial** que cruzou as dimensões entre si.

**Painel dimensional:**

1. **Completude** — cobertura das classes de artefacto e das 7 dimensões de excelência do brief.
2. **Coerência interna** — contradições e inconsistências entre documentos (verificação programática de ranges de tickets, dependências, ADRs, NFRs).
3. **Rigor técnico** — auditoria adversarial da correção e solidez das afirmações técnicas.
4. **Clareza** — legibilidade, navegabilidade e adequação à audiência (3.ª ronda de confirmação pós-P0.1b).
5. **Rastreabilidade de decisões** — cadeia síntese-fonte → ADR → documento → epic → ticket.
6. **Viabilidade de execução** — executabilidade do backlog (atomicidade, faseamento, dependências, estimativas).

**Contra-auditoria.** Verificou cada finding (49 dos 51 **confirmados**; **2 refutados** — COER-05 e VIAB-09 — mais 2 sub-argumentos corrigidos em RIG-04 e VIAB-06) e produziu **6 constatações transversais** que só emergem ao cruzar dimensões: nenhum auditor em silo as poderia ter visto. A contra-auditoria é a peça mais importante deste relatório — é onde os scores altos são postos à prova.

---

## 3. Constatações por dimensão

### 3.1 Completude — 7,5

**Pontos fortes.** Remediação P0 real e verificável (tecnica/12 define 5 contratos de porta C1–C5 com schema, semântica de erro, idempotência e SemVer; tecnica/13 formaliza envelope de evento append-only, audit hash-chain/WORM reconciliado com crypto-shredding); tecnica/14 fornece matriz EU AI Act + GDPR → controlo → ticket com estados calibrados; backlog íntegro (118 headers únicos, contagem por epic coerente com o brief); modelo de domínio ER, máquina de estados durável e >50 diagramas; cobertura operacional real (runbooks RB-01..05 ligados a SLI/SLO).

**Constatações principais.**

| ID | Sev. | Localização | Problema | Recomendação |
|----|------|-------------|----------|--------------|
| COMP-01 | Alta | tecnica/ (salta de 11 p/ anexos 12-14; sem doc UX); §10.6; EPIC-02 l.758 | Dimensão 6 (UX/DX) — inegociável — sem documento nem epic próprios; contrato da superfície HITL e modelo do approval-card inexistentes; paridade Slack/Telegram empurrada para fora do escopo. | Criar `tecnica/15_Experiencia_HITL_UX.md` + tickets dedicados; mapear cada requisito da Dim.6 a AOS-NNN. |
| COMP-02 | Alta | specs/00 §7-§8; grep RF-/NFR-/RNF- = 0 | Sem RTM nem IDs de requisito estáveis; NFRs vivem em tabelas de "drivers" sem IDs; cobertura ADR→ticket não demonstrável. | Catálogo RF-NN/NFR-NN + RTM requisito→ADR→epic→ticket→teste. |
| COMP-03 | Alta | tecnica/07 §3; grep STRIDE = 0 | Modelo de ameaças usa OWASP mas não há decomposição STRIDE por fronteira de confiança, pedida explicitamente no brief. | Acrescentar matriz STRIDE por fronteira, a complementar OWASP. |
| COMP-04 | Média | tecnica/12 §3 (só C1..C5) | Portas nucleares sem contrato: ORQ↔SCH, SCH↔RT, RT↔MEM e a mensagem A2A (elevada a primitivo). | Estender tecnica/12 com estes contratos, em especial A2A. |
| COMP-05 | Média | tecnica/10 §8 vs tabelas de risco | Runbooks (5) não cobrem 1:1 os modos de falha canónicos (rug-pull, prompt injection, outage Broker/Vault, perda de quórum ES, cache-thrash, esgotamento pool microVM); RPO/RTO "(proposta)". | Estender catálogo e ratificar RPO/RTO por game-day. |
| COMP-06 | Média | tecnica/06; EPIC-10 | Sem modelo de capacidade/sizing nem FinOps consolidado além do Model Gateway. | Adicionar secção de capacidade/FinOps com fórmulas por componente. |
| COMP-07 | Média | specs/00 §7; tecnica/10 | NFRs nucleares sem alvo ratificado (overhead total "a ratificar", RPO/RTO "(proposta)", velocity "a calibrar"). | Fixar bandas provisórias com data de revisão; ratificar por game-day. |
| COMP-08 | Baixa | tecnica/INDICE l.51 vs l.169; specs/INDICE §9 | Contagens desatualizadas: "15 documentos" vs "12 documentos" no mesmo índice. | Actualizar contagens e controlo de versões. |
| COMP-09 | Baixa | grep quickstart/walking-skeleton = 0 | Sem guia de onboarding/quickstart nem walking skeleton end-to-end. | Adicionar guia de arranque ligando AOS-001..012 a um primeiro fluxo observável. |
| COMP-10 | Baixa | glossários por-documento | Sem glossário mestre nem registo de ADRs standalone; risco de deriva terminológica. | Publicar glossário e registo de ADRs mestre. |

**Lacunas:** UX/DX, RTM, STRIDE, contratos internos (ORQ↔SCH/SCH↔RT/RT↔MEM/A2A), runbooks canónicos, NFRs ratificados, capacidade/FinOps, quickstart, glossário mestre.

### 3.2 Coerência interna — 7,0

**Pontos fortes.** Esquema de tickets totalmente íntegro (118 IDs, ranges coincidentes entre brief/índice/spec); integridade referencial das dependências (0 referências pendentes); 14 ADRs consistentes; NFRs não-ambíguos uniformes (cold-start <125 ms, disponibilidade 99,9%, cache-hit >80%, replay 100%); catálogo de 13 componentes consistente.

**Constatações principais.**

| ID | Sev. | Localização | Problema | Recomendação |
|----|------|-------------|----------|--------------|
| COER-01 | Alta | brief §4 vs tecnica/01/09/10, specs/INDICE §8, EPIC-01/05/08/09/10 | **Contradição pervasiva:** o brief fixa <15 ms só para o PDP e declara o overhead total "a ratificar", mas dezenas de locais afirmam "overhead de mediação (RM) p95 <15 ms" como SLO/gate. Há contradição intra-specs. | Redacção canónica única: "PDP p95 <15 ms; overhead total decomposto por sub-passo, a ratificar". Corrigir specs/INDICE §8 e ACs de EPIC-01/10. |
| COER-02 | Média | tecnica/INDICE §6 | Afirmação estrutural falsa: "cada doc técnico mapeia para o epic homónimo" — falso em 11 e 12/13/14. | Reescrever §6 com crosswalk explícito doc→tickets em vez da regra 1:1. |
| COER-03 | Média | tecnica/INDICE; specs/INDICE §9 | Contagens/navegação desatualizadas pós-remediação (docs 12-14 omitidos do diagrama e tabelas). | Actualizar para 15 docs + linha v1.1 na tabela de versões. |
| COER-04 | Baixa | brief §2; tecnica/00; EPIC-07 | Terminologia de isolamento incoerente: gVisor rotulado "microVM", contradizendo o ADR-004. | Uniformizar para a formulação do ADR-004. |
| COER-06 | Baixa | specs/00 l.225; specs/INDICE §4.1 | Variantes cosméticas: título de EPIC-01 e formato de cabeçalho de ticket (`:` vs `—`). | Alinhar com a forma canónica. |

*(COER-05 — Kafka fora do conjunto canónico — **refutado** pela contra-auditoria: é aditivo, não fere a coerência semântica; ver §4.)*

**Lacunas:** ausência de uma **tabela canónica única de SLOs** que reconcilie PDP-eval vs overhead-total — causa-raiz de COER-01 replicada em ~10 ficheiros; tabela de versões sem registo da passagem 12→15.

### 3.3 Rigor técnico — 7,0

**Pontos fortes.** Honestidade rara (nota de calibração "arquitecturalmente impossível = objectivo de desenho"); ressalva de que a assinatura garante origem, não veracidade; crypto-shredding correctamente desenhado (hash sobre ciphertext preserva a hash-chain); lease + fencing token (padrão Kleppmann); durabilidade honestamente qualificada (at-least-once + idempotência); separação PEP/PDP e migrações expand/contract corretas.

**Constatações principais.**

| ID | Sev. | Localização | Problema | Recomendação |
|----|------|-------------|----------|--------------|
| RIG-01 | Alta | tecnica/01 §4/§8.3/§9; EPIC-01 | Remediação P0-C incompleta: ACs SMART ainda hard-committam "overhead de mediação (RM) p95 <15 ms" como total — fisicamente implausível (RM = PDP + CAS + broker + Vault + append ES + egress/DNS). Auto-admitido em analises/Comparacao l.54. | Substituir por "PDP eval p95 <15 ms" + orçamento decomposto "a ratificar"; remover o número dos ACs até haver medição. |
| RIG-02 | Alta | tecnica/12 §4 vs política Rego §9 | Exemplo C1 devolve `permit` para pedido que a própria política NEGARIA (cap ausente da authority; taint=untrusted; reversibility=irreversible deveria escalar). | Tornar request/response consistente com §9; adicionar caso ao teste de contrato do gate 7. |
| RIG-04 | Alta | tecnica/13 §3/§6; tecnica/00 §11; EPIC-02 AOS-016 | Replay ancora reprodutibilidade em `params.seed` para claude-opus-4-8, mas a API Anthropic **não expõe seed** e LLMs hospedados são não-determinísticos mesmo a temp 0. O replay só é fiel porque as saídas são **lidas do log**. | Remover seed como garantia; afirmar replay = reprodução das saídas registadas; rever NFR "100% reproduzíveis" para "reconstrutíveis a partir do log". |
| RIG-03 | Média | tecnica/12 §4 vs Rego §9 | Obligation `ttl` no exemplo C1 não é produzida por nenhuma regra da política de referência. | Adicionar regra `ttl` à política ou remover a obligation do exemplo. |
| RIG-05 | Média | tecnica/07 §6/§3 | Taint tracking em linguagem absoluta ("estruturalmente impedido"); rastreio transitivo através do raciocínio de um LLM é problema não resolvido (CaMeL não reivindica completude). | Rebaixar para "mitiga com risco residual"; citar limitação do CaMeL/dual-LLM. |
| RIG-06 | Média | tecnica/01 §1.4/§3 vs tecnica/12 | RM reivindica ser "pequeno e verificável" mas acumula PDP+identidade+CAS+broker+egress+DNS+audit e é hub de 3/5 contratos — TCB grande, auto-contraditório. | Definir núcleo PEP mínimo e delegar o resto para fora do TCB, ou abandonar a alegação de "pequeno". |
| RIG-07 | Média | brief §4; specs/00 §7; tecnica/03 §8.1 | 99,9% afirmado sem análise de composição; cadeia serial fail-closed compõe-se multiplicativamente; CAP do contador global (CAS) sob partição não analisado. | Decompor orçamento de disponibilidade; especificar modos degradados; analisar CAP do contador. |
| RIG-08 | Média | tecnica/07 §4; EPIC-07 AOS-065 | "Cold-start <125 ms (restore 5-30 ms)" atribuído indistintamente a Firecracker/Kata **e** gVisor; conflaciona perfis de segurança/desempenho distintos. | Qualificar números como específicos de Firecracker+snapshot; distinguir gVisor. |
| RIG-09 | Média | tecnica/02 §4 vs tecnica/13 §3 | Derivação de `step_id` inconsistente (contador de replay vs `seq` do ES); idempotency_key precisa de step_id estável **antes** do append — risco de circularidade. | Definir step_id canonicamente como contador de posição de replay, independente do seq. |
| RIG-10 | Baixa | tecnica/06 §7 | SLI cache-hit >80% sem método de medição nem semântica por provider (TTL, breakpoints). | Definir métrica exacta e condicionar à semântica de cada provider. |
| RIG-11 | Baixa | tecnica/07 §3; _FONTE | Números autoritativos sem fonte ("CamoLeak CVSS 9.6", "auto-aprovam >40%", "SA-ROC", "~40% dos pilotos falham"). | Citar fonte ou marcar *(proposta)*/ilustrativo. |

**Lacunas:** decomposição de latência/disponibilidade da cadeia serial; modelo de não-determinismo do provider; análise da dimensão do TCB; semântica de prompt-cache por provider; análise CAP do contador global; contratos ORQ↔SCH/SCH↔RT/RT↔MEM/A2A.

### 3.4 Clareza — 7,5

**Pontos fortes.** Introdução uniforme e bem ordenada; **CLR-01 anterior resolvido** (rotulagem ticket→componente coerente + novo gate CI 2b de lint de referências cruzadas que fecha a causa-raiz); perfis de leitor por papel/camada/fase; tickets auto-contidos e acionáveis; diagramas Mermaid que clarificam; nota de calibração de rigor que desarma leitura céptica.

**Constatações principais.**

| ID | Sev. | Localização | Problema | Recomendação |
|----|------|-------------|----------|--------------|
| CLR-01 | Alta | tecnica/INDICE §3.1-3.3/§4/§5 | Docs 12/13/14 (load-bearing) **órfãos de toda a navegação**: nenhum perfil, camada, fase, diagrama de hierarquia ou mapa ADR os inclui. A remediação corrigiu completude mas degradou a navegabilidade. | Integrar 12/13/14 em todos os aparatos de navegação. |
| CLR-02 | Média | tecnica/INDICE l.169; specs/INDICE §9 | Contradição de contagem (15 vs 12) nos ficheiros de navegação de topo. | Actualizar para 15 + linha v1.1; incluir a contagem num teste de coerência. |
| CLR-03 | Média | tecnica/00 (WORM, TCB, TPM/RPM, HITL, SA-ROC) | Acrónimos não expandidos na 1.ª ocorrência no doc-âncora (HITL nunca expandido em todo o corpus). | Expandir na 1.ª ocorrência + glossário-mestre de acrónimos. |
| CLR-04 | Baixa | campo "Documento" dos metadados | ≥6 formatos distintos do campo "Documento" (incluindo tautologia e backticks). | Fixar prefixo `<tipo>` canónico; validar via lint de metadados. |
| CLR-05 | Baixa | specs/00 §2.1; tecnica/00 §13 | Muros de texto em duas passagens que deviam ser das mais legíveis. | Partir em bullets. |
| CLR-06 | Baixa | tecnica/12 e 14 (1 Mermaid); EPICs (0) | Docs 12/14 violam o mínimo de 2 diagramas; EPICs sem diagrama de dependências. | Acrescentar 2.º Mermaid e mini-flowchart de dependências por epic. |
| CLR-07 | Baixa | tecnica/INDICE §2 l.53 | Jargão de auditoria interna ("resolvem COMP-01, COMP-02, COMP-03") vaza para o documento publicado. | Reescrever em linguagem de produto; mover IDs para changelog interno. |

**Lacunas:** glossário/índice de acrónimos consolidado; o gate 2b não cobre contagem de docs, formato de metadados nem expansão de acrónimos; falta checklist "ao adicionar um doc, actualizar N locais de navegação".

### 3.5 Rastreabilidade — 6,5

**Pontos fortes.** 14 ADRs definidos canonicamente uma vez; **0 ADRs órfãos** (cada um citado por ≥3 tickets); 111/118 tickets citam ADR; **0 links partidos** (27 caminhos resolvem todos); matriz de conformidade (tecnica/14) fornece traçabilidade bidireccional genuína para 21 tickets.

**Constatações principais.**

| ID | Sev. | Localização | Problema | Recomendação |
|----|------|-------------|----------|--------------|
| RAST-01 | Alta | tecnica/00 §8; specs/00 §11 | **Sem rasto descendente ADR→ticket** nem matriz de rastreabilidade; tabela de ADRs sem coluna "implementado por". | Adicionar coluna "Epics/Tickets que implementam" ou doc dedicado de matriz. |
| RAST-02 | Alta | tecnica/00–13 | Rasto unidireccional: **nenhum** dos 14 docs de desenho refere um ticket AOS-NNN (só tecnica/14). | Bloco "Tickets que implementam esta secção" por doc/secção. |
| RAST-03 | Alta | tecnica/12 e 13 vs EPICs | Docs 12/13 (load-bearing) não referenciados por nenhum ticket individual; EPIC-05/06 não citam o doc 12 que define os seus contratos. | Retro-encaixar referências ao nível de ticket. |
| RAST-04 | Média | specs/00 §7/§14 | NFRs/SLOs sem esquema de IDs — só rastreáveis a tickets indirectamente via ADR. | Atribuir NFR-xx/SLO-xx + matriz NFR→ticket-impl + ticket-verificação. |
| RAST-05 | Média | EPIC-01 (AOS-007/009/012) | 3 tickets estruturais sem citar ADR que os fundamenta (AOS-007→ADR-002/005; AOS-009→ADR-007; AOS-012→ADR-008). | Adicionar as citações em falta. |
| RAST-06 | Baixa | tecnica/00 §8 | ADRs são linhas de tabela-resumo, sem Estado/Alternativas/Consequências nem genealogia supersedes. | Expandir para template canónico de ADR. |
| RAST-07 | Baixa | tecnica/INDICE §3/§4/§7 | Metadados desactualizados (changelog "12 documentos"; diagrama para em 11). | Actualizar diagrama e changelog para 15. |

**Lacunas:** artefacto único de matriz síntese-fonte→ADR→doc→epic→ticket-impl→ticket-verificação; esquema de IDs de NFR/SLO; ligações descendentes doc→ticket; ADRs sem ciclo de vida de estado; síntese-fonte nunca citada por âncora ticket-a-ticket.

### 3.6 Viabilidade de execução — 7,0

**Pontos fortes.** ACs genuinamente SMART com alvos verificáveis; handoff acionável + testes + DoD por ticket; DoR/DoD fortes e 15 gates CI/CD fail-closed (incluindo gates de domínio: política/PDP, replay, idempotência, eval-gate); disciplina anti-scope (XL proibido, uso correcto de "spike"); grafo maioritariamente acíclico.

**Constatações principais.**

| ID | Sev. | Localização | Problema | Recomendação |
|----|------|-------------|----------|--------------|
| VIAB-01 | Alta | EPIC-02 AOS-022 | Combina spike (4 PoCs) **e** feature de produção num "L", com ratificação humana no meio — XL disfarçado, não-atómico. | Decompor em spike (com time-box numérico) que bloqueia ticket feature. |
| VIAB-02 | Alta | EPIC-05 AOS-054/053 | Inversão de fase e prioridade: AOS-054 (Fase1/P0) depende de AOS-053 (Fase4/P1). | Re-fasear AOS-052/053 para Fase 1 ou dividir AOS-054. |
| VIAB-03 | Alta | EPIC-10 AOS-108 | Hipercare — actividade operacional multi-semana dependente de calendário e tráfego real; não implementável como ticket-PR pelo Claude Code. | Reclassificar como marco operacional ou decompor em artefactos verificáveis num PR. |
| VIAB-04 | Média | specs/INDICE §7 | Mapa de caminho crítico contradiz os campos Dependências/Bloqueia em várias arestas. | Gerar o mapa automaticamente da fonte de verdade + gate de CI. |
| VIAB-05 | Média | EPIC-* (AOS-035/045/055/076/091/098/104) | ~31 tickets (26%) declaram dependências ao **nível de epic**; 7 só têm deps de epic — DoR não mecanicamente verificável. | Resolver cada dep de epic para ticket(s) mínimo(s); estender gate 2b. |
| VIAB-06 | Média | EPIC-* (Estimativa) | Escala de 4 pontos usada como 3 (0 XS; 30% no tecto L); variância mascarada. | Reintroduzir XS; calibrar com benchmark de velocity **antes** de comprometer datas. |
| VIAB-07 | Média | EPIC-09 AOS-095 vs EPIC-02 AOS-019 | AOS-095 (Fase2) cita "EPIC-01 (waiting_on_human)" mas o estado vive em EPIC-02 AOS-019 (Fase3) — referência incorrecta + inversão de fase. | Corrigir dep para AOS-019 e re-fasear AOS-019 para ≤Fase 2. |
| VIAB-08 | Baixa | EPIC-11 AOS-109/110 | "Bloqueia AOS-110 … AOS-118" com reticências — sobre-restringe o grafo e quebra o gate 2b. | Enumerar arestas explícitas. |
| VIAB-10 | Baixa | AOS-084 vs AOS-114; AOS-031 vs AOS-107 | Fronteiras de responsabilidade difusas (eval harness; degradação graciosa) — risco de retrabalho. | Clarificar core vs binding com referência cruzada. |

*(VIAB-09 — janela de chamadas ao modelo não mediadas nas Fases 0-1 — **refutado**: a mediação é da tool call, não da inferência; ver §4.)*

**Lacunas:** sem estimativa agregada do caminho crítico por fase; sem plano de capacidade/recursos (perfis por fase, paralelização); Fase 0 concentra 29 tickets em cadeia serial sem análise; sem política para tickets tempo-dependentes (AOS-108, AOS-090); time-boxes de spike não quantificados; velocity/lead-time sem baseline empírica.

---

## 4. Constatações transversais

Estas constatações **só emergem ao cruzar dimensões** e são, no juízo do auditor-chefe, mais graves do que qualquer finding dimensional isolado.

### 4.1 Transversais confirmadas (contra-auditoria)

| ID | Sev. | Síntese | Dimensões cruzadas |
|----|------|---------|--------------------|
| **X-01** | **Alta** | **Remediação-fantasma e colisão de IDs entre rondas.** tecnica/INDICE l.53 afirma publicamente que 12-14 "resolvem COMP-01/02/03", mas (verificado) COMP-02 desta ronda continua a 0 ocorrências de IDs de requisito, COMP-03 (STRIDE) a 0, e o exemplo C1 continua quebrado. Os "COMP-01/02/03" embutidos referem findings de **outra ronda**, colidindo com os desta. A etiqueta "P0 resolvido" não é de confiança. | Completude × Rigor × Clareza |
| **X-02** | **Alta** | **Completude estrutural ≠ completude de requisitos.** 118 tickets íntegros não provam cobertura porque não existe conjunto de requisitos identificáveis. O score alto de Completude mede largura de artefactos, não satisfação de requisitos. Um **único** catálogo canónico RF/NFR/SLO + RTM fecha simultaneamente COMP-02, COMP-07, RAST-04 e é causa-raiz de COER-01/RIG-01 — **máxima alavancagem por artefacto.** | Completude × Rastreabilidade |
| **X-03** | **Alta** | **O Reference Monitor como ponto-único-de-tudo: coerente mas frágil.** O RM acumula três papéis mutuamente tensos: kernel "pequeno/verificável" (RIG-06), gargalo de disponibilidade serial fail-closed contra 99,9% (RIG-07), e hub de 3/5 contratos. A decisão (ADR-002) é elegante mas concentra todas as virtudes num componente, tornando o TCB grande, o 99,9% não-derivado e o "pequeno" auto-contraditório — em simultâneo. | Rigor (RIG-06×07) × Contratos |
| **X-04** | Média | **Uma lacuna de processo gera sintomas em 4 dimensões.** O defeito "doc-count 12 vs 15" é contado 4× (COMP-08, COER-03, CLR-02, RAST-07); a orfandade de 12-14 e o mapa homónimo falso partilham **uma** causa-raiz: acrescentar docs não disparou uma checklist de navegação. O gate 2b elogiado valida só a ortografia de rótulos — dá **falsa segurança** de rede de CI. | Completude × Coerência × Clareza × Rastreabilidade |
| **X-05** | Média | **Inversões de fase escondidas atrás de deps de epic + lint superficial.** AOS-054→053 e AOS-095→estado de fase posterior escapam ao gate 2b *precisamente* porque a dep é declarada a granularidade de epic e o lint só compara rótulos que já referem AOS-NNN. O gate publicitado como garantia de integridade do grafo é **estruturalmente cego** às inversões que mais comprometem o faseamento. | Viabilidade × confiança de Coerência/Clareza no gate 2b |
| **X-06** | Média | **NFRs abertos tornam o blueprint não-verificável, mas os critérios a jusante fingem o contrário.** O overhead total está "a ratificar" e RPO/RTO/velocity "(proposta)", enquanto ~20 ACs SMART hard-committam "<15 ms" como gate de merge. Os tickets que **deviam** honrar o deferimento impõem um número que a fonte recusa fixar — DoDs impossíveis de cumprir honestamente ou marcados "done" contra métrica inválida. | Rigor × Viabilidade × Completude |

### 4.2 Disputas resolvidas (findings refutados ou corrigidos)

- **COER-05 (Kafka fora do conjunto canónico)** — **REFUTADO.** É aditivo; Kafka/JetStream é da mesma família push log-oriented; o ADR-007 fixa a *propriedade* "transporte push", não uma allowlist fechada. Nota editorial, não defeito de coerência. Removido do registo.
- **VIAB-09 (chamadas ao modelo não mediadas nas Fases 0-1)** — **REFUTADO.** Auto-refutado no próprio texto: a fronteira de mediação é a tool call (ADR-002), não a inferência do modelo. Bootstrapping normal. Removido do registo.
- **RIG-04 (sub-argumento trace-diffing)** — **verdicto central mantido, sub-argumento corrigido.** O trace-diffing existe para *superficiar* diferenças não-determinísticas — não assume determinismo. A recomendação sólida (replay = reconstrução a partir do log) mantém-se; o thread de raciocínio errado deve sair da justificação.
- **VIAB-06 (relabelar L→XL)** — **observação mantida, prescrição corrigida.** A compressão de escala é real; afirmar que AOS-002/027/064 "são candidatos claros a XL" é juízo não fundamentado. Recomendação correcta: benchmark de velocity antes de comprometer datas, não relabelar a priori.

---

## 5. Registo priorizado de constatações confirmadas

**49 findings dimensionais confirmados** (14 altas, 20 médias, 15 baixas) + **6 transversais**. Ordenados por severidade.

### Severidade ALTA

| ID | Dimensão | Localização | Recomendação (essência) |
|----|----------|-------------|-------------------------|
| X-01 | Transversal | tecnica/INDICE l.53; analises/Comparacao l.54 | Tratar remediação como não-fiável até re-verificação; namespacing de IDs por ronda; gate que falhe se "resolvido" não tiver artefacto verificável. |
| X-02 | Transversal | specs/00; tecnica/00 | Catálogo canónico único RF/NFR/SLO + RTM completa. |
| X-03 | Transversal | tecnica/01/12; brief §4 | Decompor orçamento de latência/disponibilidade; núcleo PEP mínimo; análise CAP do contador global. |
| COMP-01 | Completude | tecnica/ (sem doc UX) | Criar tecnica/15 (HITL/UX) + tickets; mapear Dim.6 a AOS-NNN. |
| COMP-02 | Completude | grep RF-/NFR- = 0 | Catálogo de requisitos + RTM. |
| COMP-03 | Completude | tecnica/07 §3 | Matriz STRIDE por fronteira de confiança. |
| COER-01 | Coerência | brief §4 vs ~10 ficheiros | Redacção canónica única PDP vs overhead total. |
| RIG-01 | Rigor | tecnica/01; EPIC-01 | Concluir P0-C; remover "<15 ms" dos ACs até haver medição. |
| RIG-02 | Rigor | tecnica/12 §4 vs §9 | Tornar exemplo C1 consistente com a política Rego. |
| RIG-04 | Rigor | tecnica/13; EPIC-02 AOS-016 | Remover seed como garantia; replay = reconstrução do log. |
| CLR-01 | Clareza | tecnica/INDICE §3-5 | Integrar 12/13/14 em toda a navegação. |
| RAST-01 | Rastreab. | tecnica/00 §8 | Coluna ADR→ticket ou matriz dedicada. |
| RAST-02 | Rastreab. | tecnica/00–13 | Bloco "Tickets que implementam" por doc/secção. |
| RAST-03 | Rastreab. | tecnica/12/13 vs EPICs | Retro-encaixar referências ao nível de ticket. |
| VIAB-01 | Viabilidade | EPIC-02 AOS-022 | Decompor spike (time-boxed) + feature. |
| VIAB-02 | Viabilidade | EPIC-05 AOS-054/053 | Re-fasear; nenhum P0/Fase-1 depende de fase posterior. |
| VIAB-03 | Viabilidade | EPIC-10 AOS-108 | Reclassificar hipercare como marco ou decompor em PR-verificáveis. |

### Severidade MÉDIA

| ID | Dimensão | Localização | Recomendação (essência) |
|----|----------|-------------|-------------------------|
| X-04 | Transversal | 4 índices | Gerador de navegação de fonte única + estender gate 2b a metadados. |
| X-05 | Transversal | EPIC-05/09 | Resolver deps de epic; gate que falhe em inversão de fase. |
| X-06 | Transversal | EPIC-01/tecnica-01 | Bandas provisórias datadas; propagar redacção canónica a todos os ACs. |
| COMP-04 | Completude | tecnica/12 §3 | Contratos ORQ↔SCH, SCH↔RT, RT↔MEM, A2A. |
| COMP-05 | Completude | tecnica/10 §8 | Runbooks canónicos + ratificar RPO/RTO por game-day. |
| COMP-06 | Completude | tecnica/06 | Modelo de capacidade/FinOps. |
| COMP-07 | Completude | specs/00 §7 | Fechar NFRs abertos com data de revisão. |
| COER-02 | Coerência | tecnica/INDICE §6 | Crosswalk doc→tickets em vez da regra 1:1 falsa. |
| COER-03 | Coerência | índices | Actualizar para 15 docs + v1.1. |
| RIG-03 | Rigor | tecnica/12 §4 | Reconciliar obligation `ttl` exemplo↔política. |
| RIG-05 | Rigor | tecnica/07 §6 | Rebaixar taint para "mitiga com risco residual". |
| RIG-06 | Rigor | tecnica/01 | Núcleo PEP mínimo ou abandonar "pequeno/verificável". |
| RIG-07 | Rigor | brief §4 | Decompor orçamento de disponibilidade; CAP do contador. |
| RIG-08 | Rigor | tecnica/07 §4 | Separar perfis Firecracker vs gVisor. |
| RIG-09 | Rigor | tecnica/02 vs 13 | step_id canónico = contador de replay. |
| CLR-02 | Clareza | índices | Actualizar contagens + teste de coerência. |
| CLR-03 | Clareza | tecnica/00 | Expandir acrónimos na 1.ª ocorrência + glossário-mestre. |
| RAST-04 | Rastreab. | specs/00 §7 | IDs NFR-xx/SLO-xx + matriz impl/verificação. |
| RAST-05 | Rastreab. | EPIC-01 | Citações ADR em falta (AOS-007/009/012). |
| VIAB-04 | Viabilidade | specs/INDICE §7 | Gerar mapa de caminho crítico + gate. |
| VIAB-05 | Viabilidade | ~31 tickets | Resolver deps de epic para ticket(s); estender gate 2b. |
| VIAB-06 | Viabilidade | Estimativas | Reintroduzir XS; calibrar velocity antes de datas. |
| VIAB-07 | Viabilidade | AOS-095 vs AOS-019 | Corrigir dep + re-fasear AOS-019. |

### Severidade BAIXA

| ID | Dimensão | Localização | Recomendação (essência) |
|----|----------|-------------|-------------------------|
| COMP-08 | Completude | índices | Actualizar contagens/versões. |
| COMP-09 | Completude | grep quickstart = 0 | Guia de arranque / walking skeleton. |
| COMP-10 | Completude | glossários | Glossário e registo de ADRs mestre. |
| COER-04 | Coerência | brief §2; EPIC-07 | Uniformizar isolamento (ADR-004). |
| COER-06 | Coerência | specs/00; INDICE | Alinhar título EPIC-01 e formato de cabeçalho. |
| RIG-10 | Rigor | tecnica/06 §7 | Definir métrica de cache-hit por provider. |
| RIG-11 | Rigor | tecnica/07 §3 | Citar fonte ou marcar *(proposta)*. |
| CLR-04 | Clareza | metadados | Fixar formato do campo "Documento" + lint. |
| CLR-05 | Clareza | specs/00 §2.1; tecnica/00 §13 | Partir muros de texto em bullets. |
| CLR-06 | Clareza | tecnica/12/14; EPICs | 2.º Mermaid + mini-flowchart por epic. |
| CLR-07 | Clareza | tecnica/INDICE l.53 | Remover jargão de auditoria do doc publicado. |
| RAST-06 | Rastreab. | tecnica/00 §8 | Template canónico de ADR (Estado/Alternativas/Consequências). |
| RAST-07 | Rastreab. | tecnica/INDICE | Actualizar diagrama e changelog para 15. |
| VIAB-08 | Viabilidade | EPIC-11 AOS-109/110 | Enumerar arestas explícitas de bloqueio. |
| VIAB-10 | Viabilidade | AOS-084/114; AOS-031/107 | Clarificar core vs binding. |

---

## 6. Plano de remediação

### P0 — Bloqueiam a prontidão (fazer antes de qualquer compromisso de execução)

**P0-A — Instituir o catálogo canónico de requisitos + RTM.** *Resolve X-02, COMP-02, COMP-07, RAST-04 e é causa-raiz de COER-01/RIG-01.* Um único artefacto: RF-NN/NFR-NN/SLO-NN com IDs estáveis, RTM requisito→ADR→doc→epic→ticket-impl→ticket-verificação. **Máxima alavancagem do plano.**

**P0-B — Resolver a contradição de latência de ponta a ponta.** *Resolve COER-01, RIG-01, X-06.* Redacção canónica única ("PDP eval p95 <15 ms; overhead total decomposto por sub-passo, a ratificar"), propagada a **todos** os ACs; remover o número dos DoD até haver medição.

**P0-C — Re-verificar a remediação anterior e estabelecer confiança de processo.** *Resolve X-01.* Namespacing de IDs de finding por ronda; remover jargão de auditoria do blueprint (CLR-07); gate de CI que falhe se um item "resolvido" não tiver artefacto verificável (grep positivo).

**P0-D — Corrigir os defeitos técnicos que invalidam artefactos de referência.** *Resolve RIG-02, RIG-03, RIG-04.* Tornar o exemplo C1 consistente com a política Rego (com teste de contrato no gate 7); remover seed como garantia de determinismo e reformular o modelo de replay.

**P0-E — Cobrir a dimensão de excelência em falta.** *Resolve COMP-01.* Criar tecnica/15 (HITL/UX): contrato da superfície de controlo, modelo do approval-card, paridade Slack/Telegram; tickets dedicados mapeados a AOS-NNN.

### P1 — Elevam a solidez e a executabilidade

**P1-A — Rasto descendente e navegabilidade.** *Resolve RAST-01/02/03, CLR-01, X-04.* Coluna ADR→ticket; blocos "Tickets que implementam" por doc; integrar 12/13/14 em toda a navegação; gerador de navegação de fonte única.

**P1-B — Robustez do faseamento e do grafo.** *Resolve VIAB-01/02/03/04/05/07, X-05.* Decompor AOS-022; re-fasear AOS-053/054 e AOS-019/095; resolver deps de epic para tickets; estender o gate 2b para rejeitar deps sem âncora e falhar em inversões de fase e divergência do mapa de caminho crítico.

**P1-C — Derivar (não afirmar) os NFRs nucleares.** *Resolve RIG-06/07/08, COMP-05, X-03.* Orçamento de latência/disponibilidade da cadeia serial; núcleo PEP mínimo vs TCB; análise CAP do contador global; separar perfis Firecracker/gVisor; runbooks canónicos; ratificar RPO/RTO por game-day.

**P1-D — Completar contratos internos e rigor de taint.** *Resolve COMP-04, RIG-05, RIG-09.* Contratos ORQ↔SCH/SCH↔RT/RT↔MEM/A2A; rebaixar linguagem de taint; step_id canónico.

### P2 — Polimento e higiene documental

**P2-A — Coerência de metadados e navegação.** *Resolve COMP-08, COER-03/04/06, CLR-02/04, RAST-07.* Contagens, títulos, formato do campo "Documento", isolamento; lint de metadados.

**P2-B — Legibilidade e artefactos de entrada.** *Resolve COMP-09/10, CLR-03/05/06, RAST-06.* Quickstart/walking skeleton; glossário e registo de ADRs mestre; expansão de acrónimos; bullets; diagramas em falta; template de ADR.

**P2-C — Fundamentação e clarificações.** *Resolve COMP-06, RIG-10/11, RAST-05, VIAB-06/08/10.* Modelo de capacidade/FinOps; métricas por provider; fontes/*(proposta)*; citações ADR; escala XS + velocity; arestas explícitas; fronteiras de responsabilidade.

---

## 7. Veredicto de prontidão

### Escala de maturidade documental (proposta D0–D4)

| Nível | Designação | Critério |
|-------|------------|----------|
| **D0** | Esboço | Ideias e notas dispersas; sem estrutura consistente. |
| **D1** | Documentado | Largura de artefactos existe; estrutura por documento consistente; **sem garantia de coerência nem rastreabilidade.** |
| **D2** | Coerente | Fonte única de nomes/IDs; sem contradições estruturais; navegação e metadados íntegros; grafo de execução válido. |
| **D3** | Rastreável e verificável | RTM completa requisito→ticket→teste; NFRs com alvos ratificados; remediação verificada por CI; claims load-bearing derivados. |
| **D4** | Referência certificado | Implementation-ready; claims validados empiricamente (benchmarks/game-days); walking skeleton executável; zero afirmações não-derivadas. |

### Posicionamento do AOS: **D2⁻ (D2 com reservas)**

O corpus tem a **estrutura** de D2 (tickets, ADRs e dependências íntegros; catálogo de componentes estável; 0 links partidos) mas **não a alcança plenamente** por três razões: a contradição de latência pervasiva (COER-01) permanece; a navegação e os metadados estão desalinhados pós-remediação (X-04); e a remediação anterior não é fiável (X-01). Está claramente **abaixo de D3**: não existe rastreabilidade de requisitos, os NFRs nucleares não estão ratificados, e vários claims são afirmados-não-derivados.

**Interpretação prática:** o AOS está **pronto para orientar desenho e alinhar equipas**, mas **não para ser tratado como especificação implementável verificável**. Adoptá-lo como "implementation-ready" comprometeria equipas a NFRs não validados (overhead <15 ms, replay "determinístico", 99,9% não-derivado) e a um faseamento com inversões que o próprio gate de CI não apanha.

### O que falta para o nível seguinte (D2⁻ → D3)

1. **Catálogo de requisitos + RTM** (P0-A) — sem isto, D3 é matematicamente inalcançável.
2. **Contradição de latência resolvida** e propagada a todos os ACs (P0-B).
3. **Remediação re-verificada** com prova de artefacto por CI (P0-C).
4. **Defeitos de referência corrigidos** — exemplo C1↔política, modelo de replay (P0-D).
5. **NFRs nucleares derivados/ratificados**, não afirmados (P1-C).
6. **Rasto descendente doc→ticket e navegação íntegra** (P1-A).

Concluídos os P0 e P1-A/C, o corpus atinge **D3**. Para **D4 (referência certificado)** faltam ainda os artefactos de validação empírica (benchmarks do overhead, game-days de RPO/RTO, walking skeleton executável) e a cobertura da dimensão UX/DX (P0-E) plenamente instrumentada.

---

## 8. Conclusão

O AOS é um dos corpora de arquitectura mais completos e honestos que este painel auditou: a largura dos artefactos, a integridade do backlog, a qualidade dos ADRs e a calibração explícita de rigor colocam-no bem acima da média do sector. **A boa notícia é genuína e deve ser dita sem reservas.**

Mas um blueprint de referência é julgado pelo que promete provar, não pelo que aparenta. E aqui a auditoria expõe três buracos que a soma de scores dimensionais esconde: **não há requisitos identificáveis para cobrir**, pelo que a "completude" mede largura e não satisfação; **os claims mais load-bearing são afirmados, não derivados**, e vários são fisicamente implausíveis; e **o processo de remediação não é de confiança**, com findings dados como resolvidos que persistem e IDs que colidem entre rondas. Nenhum destes é visível em silo — todos exigem cruzar dimensões, e é por isso que a contra-auditoria, não os scores, é o coração deste relatório.

A remediação é **acionável e tem alta alavancagem**: um único artefacto — o catálogo canónico de requisitos com RTM — fecha simultaneamente quatro findings e ataca a causa-raiz da contradição de latência. Concluídos os cinco pacotes P0, o AOS transita de "documentação de desenho madura" para "especificação verificável", e o seu score subiria substancialmente. Até lá, a recomendação do auditor-chefe é clara: **usar como blueprint de alinhamento, não como contrato de execução.**

*Score global: 6,9 / 10 — nível documental D2⁻.*