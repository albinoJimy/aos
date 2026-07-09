# Relatório de Auditoria à Documentação — AOS

> **Produto:** AOS (Agent Operating System)
> **Versão:** 1.0
> **Data:** Julho de 2026
> **Classificação:** Documento de Referência — Aberto
> **Auditor-chefe:** Painel de Auditoria à Documentação AOS (consolidação)
> **Âmbito auditado:** conjunto documental `tecnica/` (00–11, INDICE) e `specs/` (00–01, EPIC-01…11, INDICE, _BRIEF, _FONTE)

---

## 1. Sumário executivo

O AOS possui um dos corpos documentais mais completos e disciplinados que se pode esperar de um blueprint de produto de agentes: **14 ADRs canónicos propagados de forma consistente por ~25 documentos, 118 tickets verificados (AOS-001…118) sem lacunas nem sobreposições, ~50 diagramas Mermaid, critérios de aceitação SMART e um modelo de ameaças alinhado a OWASP LLM Top 10 + ASI.** A largura narrativa, a consistência numérica dos NFR e o rigor do backlog estão claramente acima da média do mercado.

Contudo, a auditoria identifica **um defeito-raiz crítico e um conjunto de fragilidades sistémicas** que impedem, hoje, tratar o corpus como uma referência *implementável e verificável*:

1. **A camada legível-por-máquina está silenciosamente partida.** As anotações de dependência de EPIC-02/03/07 rotulam IDs de tickets com o componente errado. O efeito mais grave: o **Reference Monitor (AOS-003) — a invariante nuclear do produto ("mediação total") — não é dependência real de nenhum ticket cross-epic**, ficando invisível no grafo executável e no caminho crítico. Um executor que siga os handoffs à letra pode construir o Agent Runtime e a microVM **sem o RM merged**.
2. **Os gates de CI fail-closed apresentados como força validam artefactos que não existem** (0 contratos de interface, 0 políticas Rego/Cedar em todo o corpus). Os gates 4 e 7 são, no dia 1, no-ops ocos ou bloqueadores permanentes de merge.
3. **Todos os NFR (125 ms, 15 ms, 80%, 99,9%, 2%, RPO/RTO) são aspiracionais** — afirmados sem uma única medição e propagados como critérios de aceitação "SMART". Vários estão tecnicamente mal-escopados (o "overhead RM p95 < 15 ms" mede apenas o PDP).
4. **A retórica de segurança absolutista** ("arquitecturalmente impossível", "fisicamente incapaz de autorizar") sobre-promete garantias que o estado da arte torna residuais, e **serve de base a uma alegação de conformidade "EU AI Act por desenho" não sustentada** (só o Art. 14 é citado).

### Scorecard

| # | Dimensão | Score | Veredicto de uma frase |
|---|----------|:----:|------------------------|
| 1 | **Completude** | 6.5 | Largura narrativa e de backlog notável, mas faltam as classes de artefacto de uma referência implementável: contratos de interface, modelo de dados/eventos, exemplos de política, cobertura EU AI Act e RTM. |
| 2 | **Coerência interna** | 7.0 | Quantitativamente muito coerente (ranges, NFR, ADRs, PT-PT), com um defeito sistémico crítico: rótulos de dependência apontam para o componente errado. |
| 3 | **Rigor técnico** | 6.5 | Fundações de execução durável e identidade sólidas, manchadas por overclaims de segurança e dois erros técnicos claros (allowlist vs CamoLeak; assinatura vs alucinação). |
| 4 | **Clareza** | 7.5 | Muito legível, com perfis de leitor e glossários exemplares, prejudicada pelo mapeamento ticket→componente contraditório e acrónimos não expandidos. |
| 5 | **Rastreabilidade de decisões** | 7.0 | Cadeia "para a frente" forte e densa; falta o sentido inverso — sem matriz ADR×ticket nem NFR×ticket, sem campo ADR no esquema de ticket. |
| 6 | **Viabilidade de execução** | 7.5 | Backlog genuinamente executável (SMART, DoD, handoffs), com rotulagem de dependências errada, um ciclo cross-epic e dependências ao nível de epic inteiro. |

**Score global: 6.8 / 10.**
A média aritmética das seis dimensões é 7.0; o painel aplica um ajuste descendente de 0.2 porque o defeito-raiz crítico (RM invisível no grafo) e a natureza oca dos gates de CI são *bloqueadores de release da documentação* que nenhum silo isoladamente capturou e que degradam transversalmente a fiabilidade do corpus.

**Veredicto de prontidão:** referência conceptual **madura mas ainda não implementável sem correcções** — nível de maturidade documental **D2** (ver §7). Não deve ser entregue a uma equipa de execução autónoma antes de resolver os itens P0.

---

## 2. Metodologia

A auditoria foi conduzida por um **painel de seis auditores especializados**, cada um responsável por uma dimensão, seguido de uma **contra-auditoria adversarial** independente.

**Painel dimensional:**
1. **Completude** — cobertura das classes de artefacto que um blueprint de referência precisa de fornecer (`tecnica/` + `specs/`).
2. **Coerência interna** — contradições e inconsistências entre documentos (IDs, NFR, ADRs, terminologia PT-PT).
3. **Rigor técnico** — correção e solidez das teses de arquitectura e dos mecanismos.
4. **Clareza** — legibilidade e adequação à audiência (perfis de leitor, glossários, densidade).
5. **Rastreabilidade de decisões** — cadeia fonte → ADR/NFR → doc técnico → epic → ticket.
6. **Viabilidade de execução** — backlog executável (critérios SMART, DoD, grafo de dependências, sequenciamento).

Cada auditor produziu score (0–10), pontos fortes, constatações (id/severidade/localização/problema/recomendação) e lacunas, com verificação directa do corpus (contagens `wc -l`, grep de code fences, contagem de headers de ticket e de ADRs).

**Contra-auditoria.** Uma passagem adversarial validou cada constatação contra o corpus. Resultado: **47 constatações confirmadas** e **4 disputadas** (reclassificadas — ver §4). Mais importante, a contra-auditoria identificou **constatações transversais** que só emergem ao *cruzar* silos — nomeadamente que o bug de rotulagem (Coerência) faz desaparecer o RM do grafo executável (Viabilidade) e que os gates celebrados como força (Viabilidade) validam artefactos inexistentes (Completude). A contra-auditoria também **corrigiu uma recomendação errada e perigosa** dos três auditores (o find-replace cego "RM→AOS-003", que corromperia instâncias já correctas — a corrupção não é sistemática).

---

## 3. Constatações por dimensão

### 3.1 Completude — 6.5

**Pontos fortes.** Completude estrutural exemplar (14 ADRs em `tecnica/00 §8` propagados por todos os docs; estrutura repetida; 7 dimensões de excelência com secção própria). Backlog completo e verificável (118 headers `## AOS-NNN`, ranges por epic coerentes com o `_BRIEF §8`). Modelo de domínio ER, máquina de estados durável, diagramas de sequência para os fluxos críticos. Modelo de ameaças alinhado a OWASP LLM Top 10 + ASI. Completude operacional real (runbooks RB-01..05, SLI/SLO com limiares, RPO/RTO). Rastreabilidade parcial (Capacidade→Componente→Epic; ADR→Documento).

**Constatações principais.**

| ID | Sev. | Localização | Problema | Recomendação |
|----|------|-------------|----------|--------------|
| COMP-01 | Alta | Todo o corpus; `tecnica/01 §4-5`; `specs/01 §4` gates 4/7 | **Zero contratos de interface** entre os 13 componentes (RM↔PDP, RT↔ES só existem como sequência informal). Gate 4 "valida contratos" e gate 7 "teste de política Rego/Cedar" mas nenhum existe. | Anexo de contratos de porta (schema request/response, erro, idempotência, SemVer) + política Rego/Cedar default-deny de referência. |
| COMP-02 | Alta | `specs/00 §3`; `tecnica/04`, `tecnica/08` | **Falta modelo de dados/eventos canónico** (envelope de evento, audit hash-chained, schema de memória). É load-bearing: replay, contexto≠registo e crypto-shredding dependem da estrutura exacta. | Secção "Modelo de dados e eventos": envelope append-only, registo WORM, manifesto de dependências, schema versionado + migrações expand/contract. |
| COMP-03 | Alta | `tecnica/09 §8`; `specs/EPIC-09`; `_BRIEF §4` | **Conformidade EU AI Act afirmada "por desenho" mas só o Art. 14 é citado**; as restantes ocorrências regulatórias são GDPR Art. 17. Sem Art. 9/10/11/12/13, Anexo III, GPAI, FRIA. | Matriz de conformidade: artigo → controlo AOS → ticket, incluindo classificação de risco e posição GPAI. |
| COMP-04 | Média | `specs/00 §7-§8`; `specs/01 §9` | **Sem matriz de rastreabilidade de requisitos** (sem IDs RF-/NFR-); cobertura assimétrica de ADR-013/014 não justificada. | Matriz NFR/ADR → epic → ticket(s) com IDs estáveis. |
| COMP-05 | Média | `tecnica/00 §10.6`; `tecnica/09 §10.3` | **UX/DX subespecificada** — dimensão de excelência sem doc técnico nem epic próprio; approval-card e paridade Slack/Telegram só em bullets. | Documento/secção de UX/DX com contrato de superfície HITL e mapeamento a tickets. |
| COMP-07 | Baixa | `tecnica/10 §8` vs tabelas de risco | **Runbooks (5) não cobrem 1:1 os modos de falha** (rug-pull, prompt-injection, indisponibilidade do broker, partição do ES, cache-thrash, esgotamento de microVMs); RPO/RTO marcados *(proposta)*. | Estender catálogo de runbooks e ratificar RPO/RTO via game-days. |
| COMP-08 | Baixa | Glossários por-documento | **Sem glossário/registo de ADRs mestre único** → deriva terminológica e manutenção duplicada. | Glossário e registo de ADRs mestre (single source) referenciado pelos locais. |

**Lacunas:** contratos de interface; modelo de dados/eventos; exemplo de policy-as-code; matriz EU AI Act; RTM; doc de UX/DX; runbooks em falta; glossário mestre; modelo de capacidade/FinOps.

---

### 3.2 Coerência interna — 7.0

**Pontos fortes.** Particionamento perfeito dos 118 tickets (AOS-001..118, sem lacuna/sobreposição). Nenhuma dependência aponta para ID inexistente. Alvos NFR idênticos em todo o lado. 14 ADRs numerados de forma consistente. Metadados auto-consistentes ao detalhe (contagens de linhas e "47 diagramas Mermaid" batem exactamente). PT-PT quase uniforme (zero formas PT-BR nucleares; 3 deslizes de "deteção"). Modelo M0-M4 e roadmap coerentes.

**Constatações principais.**

| ID | Sev. | Localização | Problema | Recomendação |
|----|------|-------------|----------|--------------|
| COER-01 | **Crítica** | `specs/EPIC-02/03/07` — campos "Dependências" | **Rótulos de dependência apontam para o componente ERRADO.** AOS-013 lista "AOS-002 (Reference Monitor), AOS-005 (Event Store)" — mas AOS-002 é o ES e AOS-005 é a Identidade; o RM é AOS-003. O RM recebe **três IDs diferentes** (AOS-002 em EPIC-02/03, AOS-001 em EPIC-07). Nenhum ticket cross-epic depende de AOS-003. Corrompe o grafo legível-por-máquina que o caminho crítico assume correcto. | Reconciliar par-a-par contra os títulos canónicos de EPIC-01; acrescentar AOS-003 como dependência de AOS-013/064; gerar a partir de matriz única e validar em CI. |
| COER-02 | Média | `tecnica/INDICE §5-§6` vs `specs/EPIC-01/04` | Regra "doc técnico mapeia para epic homónimo" tem **exceções não assinaladas** (Event Store: desenho em `tecnica/04`, backlog em EPIC-01; EPIC-04 declara-o "fora de âmbito"). | Qualificar a afirmação com nota de exceções ou tabela "tópico → doc → epic/ticket". |
| COER-03 | Baixa | `tecnica/INDICE §3.3` vs `specs/INDICE §3.2` | Anotação "(broker)" atribui erradamente o Credential Broker (doc 07) ao Model Gateway (doc 06); fase do subsistema 06 divergente entre índices. | Corrigir "(broker)" e alinhar a fase (2-3). |
| COER-04 | Baixa | `specs/EPIC-03` (l.407,426,442) | **Deslize PT-BR:** "deteção de saturação" (3×) em vez de "detecção". | Substituir; adicionar verificador de termos PT-BR ao lint. |

**Lacunas:** sem matriz de dependências canónica única; campo "Bloqueia" só regista ligações forward intra-epic (sem validação de reciprocidade cross-epic); dependências cross-epic grosseiras (epic inteiro); falta gate de CI que valide ID↔rótulo — **causa-raiz de COER-01**.

---

### 3.3 Rigor técnico — 6.5

**Pontos fortes.** Modelo de execução durável correcto (resume-from-step vs task, fencing tokens monotónicos, sagas, idempotency key = f(run_id, step_id)) — a parte mais rigorosa do corpus. Diagnóstico correcto da falha da liveness por PID e do remédio lease/heartbeat + fencing. Separação identidade scoped vs chaves pooled sólida. Hash-chain + WORM + migrações expand/contract corretas. Allowlist default-deny vs blocklist fail-open bem raciocinada. Consistência notável entre ~25 ficheiros; alvos não validados honestamente marcados *(proposta)*.

**Constatações principais.**

| ID | Sev. | Localização | Problema | Recomendação |
|----|------|-------------|----------|--------------|
| RIG-01 | **Crítica** | `_FONTE §Dim.2`; `tecnica/07 §6`; `tecnica/00 §2` | **Overclaim-mãe:** prompt injection "arquitecturalmente impossível" via taint/dual-LLM/CaMeL não é sustentado — o LLM "lava" o taint; os próprios autores do CaMeL documentam risco residual. Reduz, não elimina. | Reformular para "redução estrutural com risco residual"; especificar o mecanismo de taint sobre texto gerado por LLM; reconhecer taint-laundering. |
| RIG-02 | Alta | `tecnica/07 §3,§5,§9` | **Desencontro factual:** CamoLeak (CVSS 9.6) exfiltra por um domínio de primeira-parte JÁ allowlisted (proxy Camo). Uma allowlist de egress não o impede. | Content-security (bloquear fetch automático de imagens/markdown, sanitização) mitiga CamoLeak; parar de o apresentar como caso da allowlist. |
| RIG-03 | Alta | `tecnica/07 §7.2`; `tecnica/09 §3` | **Erro de categoria:** assinar mensagens inter-agente "resolve o hallucination gate" — mas assinatura autentica origem/integridade, não veracidade. O sub-agente autêntico assina o resumo alucinado com assinatura válida. | Separar: assinatura resolve forja/confused-deputy; alucinação exige verificação de conteúdo (cross-check contra spans/fontes, evals). |
| RIG-04 | Alta | Driver "0 efeitos duplicados no retry" | Execução durável é **at-least-once**: crash entre POST bem-sucedido e gravação → o efeito repete-se salvo idempotência downstream. A saga compensa a posteriori, não previne. | Requalificar para "at-least-once com idempotência downstream exigida"; tornar explícita a propagação da idempotency key. |
| RIG-05 | Alta | Driver "RM p95 < 15 ms" | **Mal-escopado:** os 15 ms medem só o PDP em memória, ignorando CAS de admissão distribuída, broker→vault, append ao ES replicado e egress/DNS — os passos caros. | Separar dois SLIs: "latência PDP" e "overhead total de mediação" com decomposição por sub-passo. |
| RIG-06 | Alta | `tecnica/01 §3` vs §4 | **Contradição interna:** o RM reivindica ser "pequeno e verificável" mas acumula na hot path identidade, PDP, admissão CAS, egress+DNS, brokering, audit hash-chain, spans e taint — um monólito de alto fan-out. | Reduzir o RM a núcleo mínimo (decidir+registar) OU abandonar a alegação de "pequeno e verificável" e assumir a superfície de ataque. |
| RIG-08 | Alta | `tecnica/07 §4,§8.3`; AOS-065 | **Risco de segurança ignorado:** VMs restauradas do mesmo snapshot Firecracker partilham estado do RNG → reutilização de nonces/TLS no guest. | Especificar re-seeding de entropia pós-restore (virtio-rng) como condição obrigatória do pool. |
| RIG-09 | Média | `tecnica/08 §5`; driver "replay 100%" | Inferência de LLM hospedado é **não-determinística** mesmo com seed fixo; os 100% só valem para replay-por-leitura-do-log, não para reexecução. | Distinguir "replay por leitura" (100%) de "reexecução" (não determinística, trace-diffing tolerante). |
| RIG-11 | Média | `tecnica/03 §4-5`; ADR-008 | Admission control não conhece o custo em tokens de saída antes da execução; janela admit→settle pode exceder rate limits; o breaker é reactivo. "Impede o colapso agregado" é mais forte do que o mecanismo garante. | Requalificar para "reduz a probabilidade"; especificar estimativa de saída e headroom. |
| RIG-12 | Média | `tecnica/09 §5`; `tecnica/08 §8` | **Tensão crypto-shredding vs audit:** a identidade do actor é PII; apagar a chave para o Art. 17 destrói a atribuição que o audit promete reter para sempre. | Distinguir conteúdo (shreddable) de metadados de atribuição (retidos por base legal); documentar o que sobrevive. |
| RIG-13 | Média | `tecnica/08 §6` | Sinal "ausência de progresso útil" do disjuntor não é operacionalmente definido (progresso semântico é, em geral, não computável) — hand-waving disfarçado de métrica. | Definir proxy mensurável (sem novas escritas/hashes durante N iterações) ou remover o sinal. |
| RIG-14 | Média | `tecnica/09 §7`; ADR-014; AOS-090 | "Erro < 2% por 30 dias" **plano em todos os níveis** contradiz a proporcionalidade ao impacto do próprio ADR; "erro" nunca é definido; 2% sem justificação. | Limiares condicionados ao tier de impacto; definir a métrica; marcar 2% como exemplo, não critério hardcoded. |
| RIG-15 | Baixa | ADR-011, ADR-004 | Imprecisões de "intercambiabilidade": Rego/Cedar diferem materialmente (Cedar restrito para analisabilidade); "Firecracker/Kata" confunde VMM com runtime OCI. | Analisar trade-off Rego vs Cedar ligado à verificabilidade do RM; reescrever a lista de isolamento. |

**Lacunas:** sem decomposição do orçamento de latência; topologia do PDP ambígua (control-plane vs sidecar); sem threat model do próprio RM/PDP/broker (*quis custodiet*); sem modelo de custo/armazenamento do "capturar tudo"; mecanismo concreto de propagação de taint não especificado; disponibilidade do PDP em hot-path fail-closed não analisada face aos 99,9%.

---

### 3.4 Clareza — 7.5

**Pontos fortes.** Perfis de leitor por papel (`tecnica/INDICE §3.1`) e vistas por camada/fase — navegabilidade exemplar. Estrutura de Introdução uniforme. Diagramas Mermaid que clarificam (máquina de estados, threat model, sequência do broker). Rigor factual nos metadados (contagens verificadas). Glossário, metadados e controlo de versões por documento. Tickets AOS-NNN muito claros para o executor (handoff auto-contido para o Claude Code).

**Constatações principais.**

| ID | Sev. | Localização | Problema | Recomendação |
|----|------|-------------|----------|--------------|
| CLR-01 | Alta | `specs/EPIC-02/03/07` vs `EPIC-01` e `INDICE §7` | **Mesma raiz de COER-01 vista pela navegabilidade:** os prompts de Handoff propagam o erro ("Confirma que AOS-002 (Reference Monitor) e AOS-005 (Event Store) estão Done"), instruindo o executor a confirmar os tickets errados. | Corrigir todas as etiquetas para o mapeamento canónico; gerar de fonte única; teste de coerência ID↔título no CI. |
| CLR-02 | Média | `tecnica/00` (WORM, DSAR, TPM/RPM, SA-ROC); `specs/01` (SBOM, DORA) | **Acrónimos sem expansão na 1ª ocorrência**, contra a convenção do `_BRIEF §9`. No doc-âncora 00, WORM aparece 7× sem expansão; SA-ROC nunca é expandido em todo o corpus. | Expandir cada acrónimo na 1ª ocorrência de cada doc; acrescentar ao glossário do doc 00. |
| CLR-03 | Média | Campo "Documento" dos metadados | **Formato inconsistente** entre docs ("Técnica —", "Documento Técnico —", "Operação —"); doc 00 tem a tautologia "Arquitectura de Solução — Documento de Arquitectura de Solução". | Normalizar prefixo `<tipo>` e capitalização. |
| CLR-04 | Média | `specs/00 §2.1`; `tecnica/00 §13` | **Muros de texto** em passagens-chave (motivação e roadmap comprimidos num parágrafo denso). | Partir em bullets (um por modo de falha; um por fase). |
| CLR-05 | Baixa | `specs/00 §10` vs INDICE vs `_BRIEF` | **Títulos de epic inconsistentes** ("Fundações do Plano de Controlo" vs "Fundações e Plano de Controlo"; "&" vs "e"). | Fixar lista canónica e replicar textualmente. |
| CLR-06 | Baixa | `specs/EPIC-01..11` | **Zero diagramas nos 11 EPICs** (~8.900 linhas); só 3 Mermaid em todo o `specs/`. | Mini-flowchart de dependências por epic (após CLR-01). |

**Lacunas:** sem glossário/índice de acrónimos consolidado ao nível do conjunto; sem lint de coerência de referências cruzadas; EPICs sem visão gráfica de dependências; sem indicação de tempo/dificuldade de leitura por documento.

---

### 3.5 Rastreabilidade de decisões — 7.0

**Pontos fortes.** Cobertura de ADR completa (nenhum ADR órfão). Cada doc técnico com secção "§2 ADRs aplicáveis". Densidade de citação alta (563 ocorrências nas specs, 306 na tecnica). EPIC-11 fecha a cadeia requisito→verificação (cada teste amarra-se ao ADR/NFR que valida). NFR propagam-se por valor da fonte aos critérios de aceitação. Tickets referenciam doc técnico COM secção. Zero drift na identidade dos ADRs.

**Constatações principais.**

| ID | Sev. | Localização | Problema | Recomendação |
|----|------|-------------|----------|--------------|
| RAST-01 | Alta | `specs/00 §8/§11`; `tecnica/INDICE §5` | **Sem matriz ADR→ticket nem NFR→ticket.** Impossível responder "que tickets implementam ADR-013?" ou "que ticket valida os 99,9%?" sem grep manual. O sentido inverso não está materializado. | RTM bidireccional (ADR×ticket e NFR/SLO×ticket) como artefacto de primeira classe. |
| RAST-02 | Alta | `_BRIEF §8`; metadados de cada ticket | **Esquema de ticket sem campo dedicado para ADRs.** A ligação ticket→ADR é ad-hoc (tag de título / dobrada em "Documentos de referência" / só na prosa). | Campo obrigatório "ADRs" (e "NFRs") na tabela de metadados; retro-preencher os 118 tickets. |
| RAST-05 | Alta | `tecnica/00 §2` (princípio 4); `specs/00 §12` | **Decisões estruturantes sem ADR próprio:** "Contexto ≠ registo" (único princípio sem ADR) e o modelo de confiança de supply-chain (TOFU/pin+hash+assinatura, conflacionado no ADR-012). | Promover a ADR-015/ADR-016; corrigir a tabela de riscos. |
| RAST-06 | Média | `tecnica/00 §8`; `specs/00 §11` | ADRs são linhas de tabela de 3 colunas — **sem Estado, Alternativas, Consequências, data**. O "porquê" e o rejeitado ficam truncados. | Expandir para formato-padrão (Contexto·Decisão·Alternativas·Consequências·Estado). |
| RAST-04 | Média | `specs/EPIC-01,02,05,09,10` | **Tagging de ADR no título inconsistente** e por vezes desalinhado (o ticket primário do ADR-007 fica sem tag; o secundário é tagueado). | Uniformizar no ticket primário OU abandonar tags em favor do campo estruturado. |
| RAST-09 | Média | `tecnica/INDICE §5` | O único índice inverso (ADR→"Doc principal") é **grosseiro e por vezes mal-localizado** (ADR-007→"04" quando a base está em 00/EPIC-01; ADR-013→dois docs). | Substituir por ADR→{doc principal, epic, tickets primários}. |
| RAST-07 | Baixa | AOS-111, AOS-112 | Tickets de teste de replay/idempotência apontam para `tecnica/11` em vez de `tecnica/02` e `08` (autoritativos). | Reapontar para os docs autoritativos. |
| RAST-08 | Baixa | `tecnica/01` vs 07/09/11 | Nome do cabeçalho da secção de ADRs inconsistente ("ADRs aplicáveis" vs "Princípios e decisões aplicáveis (ADRs)"). | Normalizar o título da secção 2. |

**Lacunas:** falta RTM bidireccional; falta campo "ADRs" no esquema de ticket; falta log de ADRs com Estado/Consequências/supersessão; duas decisões canónicas sem ADR; falta matriz NFR/SLO→teste; falta back-check risco→ticket.

> **Nota do auditor-chefe (disputa RAST-03).** A afirmação de que "EPIC-01 é a parte com pior rastreabilidade" foi **rejeitada** pela contra-auditoria: EPIC-01 é, ao contrário, a epic com mais acoplamento ADR→ticket via tags de título. O problema real é estreito — **4 tickets órfãos de ADR (AOS-007/009/010/012)**, com destaque para AOS-007 (allowlist nuclear) — reclassificado para **média** e absorvido em RAST-01/02.

---

### 3.6 Viabilidade de execução — 7.5

**Pontos fortes.** Critérios de aceitação genuinamente SMART com testes negativos ("0 overshoot" em CAS, "100% detecção" de tampering, "0 allow indevido"). DoD específica de domínio ancorada a ADRs e imposta por gates fail-closed. Handoffs concretos e de escopo fechado ("não expandas escopo"). Regra "sem XL" cumprida (0 XL em 118). Grafos intra-epic acíclicos e faseáveis; roadmap mapeado a M0–M4.

**Constatações principais.**

| ID | Sev. | Localização | Problema | Recomendação |
|----|------|-------------|----------|--------------|
| VIAB-01 | Alta | `specs/EPIC-02` (l.49,62,75,119) vs catálogo EPIC-01 | **Mesma raiz de COER-01/CLR-01 pela execução:** o loop que despacha TODA a tool call (AOS-013) não lista AOS-003 (RM) como dependência; o handoff manda verificar IDs errados → risco de construir o runtime sem o RM merged. | AOS-013 depende de AOS-003 e AOS-002; rever AOS-014/018/021; reescrever nota e handoff com IDs canónicos. |
| VIAB-02 | Alta | AOS-058 (EPIC-06) ↔ AOS-094 (EPIC-09) | **Dependência circular** lida à letra (AOS-058→"EPIC-09"⊃AOS-094; AOS-094→"EPIC-06"=AOS-058) + duplicação funcional (allowlist regional) sem delta de escopo. | Pinar AOS-058→AOS-087 (não o epic); fundir/clarificar fronteira (AOS-094 define a obrigação, AOS-058 consome-a). |
| VIAB-03 | Média | `specs/EPIC-09` (l.60) + ~25 tickets | **Dependências ao nível de epic inteiro** ("EPIC-08") em vez de AOS-NNN → grafo não-agendável, caminho crítico inflado, ciclos mascarados. | Substituir por ticket(s) mínimo(s); manter etiqueta de epic só como anotação. |
| VIAB-04 | Média | AOS-108 (Hipercare); parte de AOS-106 | **Actividade operacional tratada como entregável de código** (DoD "SLOs sustentados na janela de hipercare"/"game day repetido" não fecha num sprint nem é implementável por agente). | Reclassificar como actividade operacional fora do fluxo ticket→PR→Claude Code; decompor a parte automatizável. |
| VIAB-05 | Média | AOS-069, 064, 055, 035, 093, 025 | **Proibição de XL empurra para "L" tickets plausivelmente XL** (dual-LLM/CaMeL, microVM, gateway multi-provedor) escondendo risco de calendário. | Decompor os L de maior incerteza em subtickets, precedidos de spike onde há incerteza. |
| VIAB-06 | Média | AOS-022 (spike) | **Inversão de sequenciamento:** o spike build-vs-buy de durable execution depende de primitivas já construídas à mão (AOS-014/015/016) → retrabalho se concluir por adoptar engine. | Antecipar AOS-022 para antes de AOS-014; definir kill-criteria na DoD. |
| VIAB-07 | Baixa | "Bloqueia" de AOS-002/003/005 vs EPIC-02 | **Reciprocidade Dependências↔Bloqueia não mantida** entre epics; bookkeeping manual já inconsistente. | Gerar/validar o grafo programaticamente (espelho Dependências↔Bloqueia). |
| VIAB-08 | Baixa | AOS-004/007 vs 087; 011 vs 072/083; 004 vs 088 | **Capacidades fundacionais replicadas** em vários epics sem delta de escopo → risco de duplo-desenvolvimento e propriedade ambígua. | Declarar em cada ticket a jusante se é "enforcement aditivo sobre AOS-00X" ou "implementação". |
| VIAB-09 | Baixa | `specs/INDICE §8` | **Sem modelo de capacidade** (velocity "a calibrar", sem mapa sprint↔ticket, sem duração do caminho crítico); Fase 0 profundamente serial com 30% tickets "L". | Calibração de pontos por tamanho + ordenação em ondas + estimativa da Fase 0. |

**Lacunas:** sem modelo de capacidade/calendário; dependências ao nível de epic; sem validação de aciclicidade/reciprocidade; não distingue ticket de engenharia de actividade operacional; DoD de spikes fraca; sobreposição funcional entre epics; sem plano de paralelização por perfil.

---

## 4. Constatações transversais

Estas constatações **só emergem ao cruzar os silos** e são, no conjunto, a principal razão do ajuste descendente do score global. Foram produzidas pela contra-auditoria.

### 4.1 Transversais confirmadas

| ID | Sev. | Cruzamento | Constatação |
|----|------|-----------|-------------|
| **XC-01** | **Crítica** | Coerência × Viabilidade × Rigor | **O Reference Monitor (AOS-003) — invariante nuclear "mediação total" — está invisível no grafo executável.** Por efeito do bug de rotulagem, nenhum ticket cross-epic depende de facto do RM; o caminho crítico deriva desses mesmos campos. Um executor pode construir o Agent Runtime e a microVM SEM o RM merged, violando a garantia de segurança nº1. **Bloqueador de release da documentação.** |
| **XC-02** | Alta | Meta (correcção proposta) × corpus | **A correcção "find-replace sistemático RM→AOS-003" proposta por três auditores está ERRADA.** A corrupção NÃO é sistemática: no mesmo EPIC-07, AOS-005 surge rotulado "Event Store" (errado) E "identidade" (correcto). Um remap cego corromperia instâncias já correctas. **Exige reconciliação par-a-par contra os títulos canónicos**, com gate de CI. |
| **XC-03** | Alta | Viabilidade × Completude | **Gates de CI celebrados como força validam artefactos inexistentes.** 0 blocos rego/cedar/json/yaml/sql/proto em todo o corpus → gates 4 (contratos) e 7 (política) são no-ops ocos ou bloqueadores permanentes no dia 1. Entregar os artefactos antes de declarar os gates como DoD. |
| **XC-04** | Alta | Rigor × Rastreabilidade | **A "forte rastreabilidade para a frente" AMPLIFICA um erro técnico.** O NFR mal-escopado "RM p95 < 15 ms" (RIG-05) propaga-se fielmente para ~20 critérios de aceitação e para o teste de carga de EPIC-11 — um número indefensável entra hardcoded no backlog e no seu teste. Idem "0 duplicados" (RIG-04) e "erro < 2%" plano (RIG-14). Corrigir na fonte e deixar re-propagar. |
| **XC-05** | Alta | Rigor × Completude (× exposição legal) | **A retórica absolutista é load-bearing para a alegação de conformidade EU AI Act "por desenho".** O desenho não entrega o absoluto que promete (taint-laundering, limites do CaMeL) E o mapeamento regulatório não existe (só Art. 14). Um regulador poderia considerar a alegação enganosa. |
| **XC-06** | Alta | Rigor (RIG-06) × lacuna de threat model | **Ponto único de compromisso total não examinado.** O RM concentra TODO o TCB, é alegado "pequeno e verificável" (a), é de facto grande e de alto fan-out (b), e nunca é modelado quanto ao seu próprio comprometimento (c) — *quis custodiet ipsos custodes*. |
| **XC-07** | Média | Governação × Rigor | **Contradição crypto-shredding vs finalidade do audit** (RIG-12 × COMP-03): apagar a chave do actor para o Art. 17 destrói a atribuição que a governação promete reter para sempre; a base legal de retenção do audit é ignorada. |
| **XC-08** | Baixa | Meta-auditoria | **Ausência de rubrica de severidade partilhada:** o mesmo defeito-raiz recebeu severidades divergentes (crítica/alta/alta) entre silos. Adoptar rubrica única e de-duplicar COER-01 = CLR-01 = VIAB-01 num só item (crítico) com múltiplas manifestações. |

### 4.2 Disputas relevantes (reclassificações do auditor-chefe)

| Constatação original | Decisão | Fundamento |
|----------------------|---------|------------|
| **COMP-06** (matriz STRIDE em falta, média) | **Rejeitada → melhoria opcional (baixa)** | Premissa factualmente errada: o `_BRIEF` NÃO pede STRIDE; pediu um "Threat model", que foi entregue (OWASP LLM+ASI). STRIDE seria enriquecimento legítimo, não lacuna. |
| **RAST-03** (EPIC-01 pior rastreabilidade, alta) | **Reduzida → média, âmbito estreito** | EPIC-01 é a epic com mais tags ADR→ticket. Problema real: 4 tickets órfãos (AOS-007/009/010/012). Absorvida por RAST-01/02. |
| **RIG-07** (cold-start funde 3 regimes, média) | **Parcial → só o sub-ponto gVisor** | O alvo é escrito "Cold-start < 125 ms (restore 5-30 ms)" — o doc anota o mecanismo, não o esconde. Válido: gVisor não tem história equivalente de snapshot/restore com pooling. |
| **RIG-10** (NATS/Redis/Postgres intercambiáveis, média) | **Parcial → só a crítica ao Redis** | Confirma-se: Redis async pode perder escritas confirmadas, incompatível com RPO≤1min por quórum síncrono. Retira-se a desqualificação do Postgres (append-only + LISTEN/NOTIFY é event-sourcing legítimo). |

### 4.3 Riscos globais

- **Falsa confiança em NFRs aspiracionais** — todos os alvos são afirmados sem medição e propagados como critérios "provados".
- **Camada legível-por-máquina silenciosamente partida ou oca** — rótulos errados (RM ausente), gates que validam artefactos inexistentes, sem RTM, reciprocidade inconsistente. Qualquer automação vai mis-sequenciar.
- **Retórica de segurança absolutista** que sustenta uma alegação de conformidade não fundamentada — exposição regulatória e complacência de engenharia.
- **Ausência de fonte única de verdade** em todo o lado (glossários, ADRs, títulos de epic, rótulos) já em deriva.
- **Backlog aparenta executável mas é não-agendável e não-verificável quanto a aciclicidade.**
- **TCB sobredimensionado e não-threat-modeled** (o RM) como SPOF de segurança.

---

## 5. Registo priorizado de constatações

Todas as **47 constatações confirmadas** mais as 8 transversais, ordenadas por severidade. O defeito-raiz de rotulagem é registado uma vez (XC-01) com as suas manifestações COER-01/CLR-01/VIAB-01 apensas.

### Crítica

| ID | Dimensão | Localização | Recomendação |
|----|----------|-------------|--------------|
| XC-01 / COER-01 / CLR-01 / VIAB-01 | Transversal (Coer./Clar./Viab.) | `specs/EPIC-02/03/07` (Dependências + handoffs) | Reconciliar par-a-par os rótulos de dependência contra os títulos canónicos de EPIC-01; acrescentar AOS-003 como dependência de AOS-013/064; gate de CI ID↔título. **Bloqueador de release.** |
| RIG-01 | Rigor | `_FONTE §Dim.2`; `tecnica/07 §6` | Reformular "arquitecturalmente impossível" → "redução estrutural com risco residual"; especificar o mecanismo de taint e reconhecer taint-laundering. |

### Alta

| ID | Dimensão | Localização | Recomendação |
|----|----------|-------------|--------------|
| XC-03 | Transversal (Viab.×Comp.) | Gates 4/7 de CI | Entregar contratos de porta e política Rego/Cedar antes de declarar os gates como DoD; marcar como "pendente de artefacto". |
| XC-04 | Transversal (Rig.×Rast.) | NFR → ~20 critérios + EPIC-11 | Rever NFRs na fonte antes de propagar; separar "latência PDP" de "overhead total". |
| XC-05 | Transversal (Rig.×Comp.) | Retórica + conformidade | Reformular absolutos; substituir alegação genérica por matriz EU AI Act. |
| XC-06 | Transversal (Rig.) | RM/TCB | Reduzir o RM a núcleo mínimo OU threat model dedicado ao seu comprometimento. |
| XC-02 | Meta | Correcção proposta | Proibir remap mecânico; reconciliação par-a-par + gate. |
| COMP-01 | Completude | `tecnica/01 §4-5`; gates | Anexo de contratos de porta + política de referência. |
| COMP-02 | Completude | `specs/00 §3`; `tecnica/04,08` | Secção "Modelo de dados e eventos". |
| COMP-03 | Completude | `tecnica/09 §8` | Matriz de conformidade EU AI Act/GDPR. |
| RIG-02 | Rigor | `tecnica/07 §3,§5,§9` | Content-security para CamoLeak, não allowlist. |
| RIG-03 | Rigor | `tecnica/07 §7.2` | Separar assinatura (origem) de verificação de conteúdo (alucinação). |
| RIG-04 | Rigor | Driver "0 duplicados" | Requalificar para at-least-once + idempotência downstream. |
| RIG-05 | Rigor | Driver "RM p95 < 15 ms" | Separar dois SLIs + decomposição do orçamento. |
| RIG-06 | Rigor | `tecnica/01 §3` | Reduzir o RM OU abandonar "pequeno e verificável". |
| RIG-08 | Rigor | `tecnica/07 §4`; AOS-065 | Re-seeding de entropia pós-restore obrigatório. |
| RAST-01 | Rastreabilidade | `specs/00 §8`; `tecnica/INDICE §5` | RTM bidireccional ADR×ticket e NFR×ticket. |
| RAST-02 | Rastreabilidade | `_BRIEF §8` | Campo obrigatório "ADRs"/"NFRs" no ticket. |
| RAST-05 | Rastreabilidade | `tecnica/00 §2` | Promover "Contexto≠registo" e "TOFU supply-chain" a ADRs. |
| VIAB-02 | Viabilidade | AOS-058 ↔ AOS-094 | Quebrar o ciclo (pinar AOS-058→AOS-087) + clarificar fronteira. |

### Média

| ID | Dimensão | Localização | Recomendação |
|----|----------|-------------|--------------|
| XC-07 | Transversal | Crypto-shredding vs audit | Distinguir conteúdo shreddable de metadados de atribuição retidos por base legal. |
| COMP-04 | Completude | `specs/00 §7-8` | Matriz de rastreabilidade de requisitos com IDs estáveis. |
| COMP-05 | Completude | `tecnica/00 §10.6` | Documento/secção de UX/DX com contrato HITL. |
| COER-02 | Coerência | `tecnica/INDICE §5-6` | Nota de exceções ao mapeamento homónimo. |
| RIG-09 | Rigor | `tecnica/08 §5` | Distinguir replay-por-leitura de reexecução. |
| RIG-11 | Rigor | `tecnica/03 §4-5` | "Reduz a probabilidade"; especificar estimativa de saída. |
| RIG-12 | Rigor | `tecnica/09 §5` | Base legal de retenção do audit. |
| RIG-13 | Rigor | `tecnica/08 §6` | Proxy mensurável de progresso ou remover o sinal. |
| RIG-14 | Rigor | `tecnica/09 §7`; AOS-090 | Limiares por tier de impacto; definir "erro". |
| RIG-10 (parcial) | Rigor | `tecnica/04 §5`; ADR-007 | Excluir Redis async como fonte de verdade. |
| RIG-07 (parcial) | Rigor | `tecnica/07 §4` | Tratar gVisor em alvo separado (sem snapshot/restore pooling). |
| RAST-04 | Rastreabilidade | EPIC-01/02/05/09/10 | Uniformizar tags no ticket primário ou abandonar em favor do campo. |
| RAST-06 | Rastreabilidade | `tecnica/00 §8` | Expandir ADRs para formato-padrão. |
| RAST-09 | Rastreabilidade | `tecnica/INDICE §5` | ADR→{doc, epic, tickets primários}. |
| RAST-03 (reclass.) | Rastreabilidade | EPIC-01 (AOS-007/009/010/012) | Retro-preencher ADRs nos 4 tickets órfãos. |
| CLR-02 | Clareza | `tecnica/00`; `specs/01` | Expandir acrónimos na 1ª ocorrência. |
| CLR-03 | Clareza | Metadados "Documento" | Normalizar prefixo e capitalização. |
| CLR-04 | Clareza | `specs/00 §2.1`; `tecnica/00 §13` | Partir muros de texto em bullets. |
| VIAB-03 | Viabilidade | `specs/EPIC-09` l.60 + ~25 tickets | Substituir dependências de epic inteiro por AOS-NNN. |
| VIAB-04 | Viabilidade | AOS-108 | Reclassificar actividade operacional fora do fluxo de código. |
| VIAB-05 | Viabilidade | AOS-069/064/055… | Decompor os "L" de maior incerteza. |
| VIAB-06 | Viabilidade | AOS-022 | Antecipar o spike build-vs-buy; kill-criteria na DoD. |

### Baixa

| ID | Dimensão | Localização | Recomendação |
|----|----------|-------------|--------------|
| XC-08 | Meta | Painel | Rubrica de severidade única + de-duplicação de defeitos-raiz. |
| COMP-07 | Completude | `tecnica/10 §8` | Estender runbooks; ratificar RPO/RTO via game-days. |
| COMP-08 | Completude | Glossários | Glossário e registo de ADRs mestre único. |
| COMP-06 (reclass.) | Completude | `tecnica/07 §3` | STRIDE como melhoria opcional (não lacuna). |
| COER-03 | Coerência | `tecnica/INDICE §3.3` | Corrigir "(broker)" e alinhar fase do doc 06. |
| COER-04 | Coerência | `specs/EPIC-03` | "deteção"→"detecção"; lint PT-BR. |
| RIG-15 | Rigor | ADR-011/004 | Analisar Rego vs Cedar; reescrever lista de isolamento. |
| CLR-05 | Clareza | `specs/00 §10` | Fixar títulos de epic canónicos. |
| CLR-06 | Clareza | `specs/EPIC-01..11` | Mini-flowchart de dependências por epic. |
| RAST-07 | Rastreabilidade | AOS-111/112 | Reapontar para `tecnica/02` e `08`. |
| RAST-08 | Rastreabilidade | `tecnica/01` vs 07/09/11 | Normalizar título da secção 2. |
| VIAB-07 | Viabilidade | Campos "Bloqueia" EPIC-01 | Gerar/validar grafo programaticamente. |
| VIAB-08 | Viabilidade | AOS-004/007/011/087… | Declarar "enforcement aditivo" vs "implementação". |
| VIAB-09 | Viabilidade | `specs/INDICE §8` | Calibração de capacidade + ondas de sprint. |

---

## 6. Plano de remediação

### P0 — Bloqueadores de release da documentação (fazer antes de qualquer entrega a uma equipa de execução)

1. **Reconciliar o grafo de dependências par-a-par** e tornar o RM visível.
   *Resolve:* XC-01, XC-02, COER-01, CLR-01, VIAB-01, VIAB-02, VIAB-07.
   *Como:* construir uma matriz de dependências única a partir dos títulos canónicos de EPIC-01; reconciliar cada campo `Dependências`/`Bloqueia`/handoff instância-a-instância (proibir find-replace cego); acrescentar AOS-003 como dependência de AOS-013/064 e de todo o consumidor de tool call; quebrar o ciclo AOS-058↔AOS-094; adicionar **gate de CI que falhe se qualquer rótulo entre parênteses divergir do título canónico do AOS-NNN referido**.

2. **Entregar os artefactos que os gates de CI validam** (senão os gates são ocos).
   *Resolve:* XC-03, COMP-01 (parcial), COMP-02 (parcial).
   *Como:* anexo de contratos de porta para RM↔PDP, RT↔ES, RM↔BRK, GW↔provider; pelo menos uma política Rego/Cedar allow/deny default-deny de referência; envelope de evento + registo de audit hash-chained + schema de memória. Marcar gates 4/7 como "pendente de artefacto" até lá.

3. **Rever os NFR na fonte antes de os propagar como critérios.**
   *Resolve:* XC-04, RIG-04, RIG-05, RIG-14.
   *Como:* separar "latência de avaliação PDP" de "overhead total de mediação por tool call" com decomposição por sub-passo; requalificar "0 duplicados" → "at-least-once + idempotência downstream"; parametrizar "erro < 2%" por tier de impacto e definir "erro". Deixar a rastreabilidade re-propagar os valores corrigidos.

4. **Desactivar a retórica absolutista e sustentar a conformidade.**
   *Resolve:* XC-05, RIG-01, RIG-02, RIG-03, COMP-03.
   *Como:* substituir "arquitecturalmente impossível"/"fisicamente incapaz" por "redução estrutural com risco residual gerido"; corrigir o mapeamento CamoLeak→content-security e assinatura→origem (não veracidade); publicar matriz EU AI Act (Art. 9/10/11/12/13, Anexo III, GPAI, FRIA; GDPR) → controlo → ticket, com classificação de risco do sistema.

### P1 — Elevar de referência conceptual a implementável e verificável

5. **Construir a Requirements Traceability Matrix (RTM) bidireccional** e o campo estruturado de ADR.
   *Resolve:* RAST-01, RAST-02, RAST-04, RAST-09, COMP-04, RAST-03.
   *Como:* tabelas ADR×ticket e NFR/SLO×ticket como artefacto de primeira classe; campo obrigatório "ADRs"/"NFRs" no esquema de ticket (`_BRIEF §8`), retro-preenchido nos 118 tickets (incl. AOS-007/009/010/012).

6. **Endereçar o TCB e as tensões técnicas de segurança.**
   *Resolve:* XC-06, XC-07, RIG-06, RIG-08, RIG-12.
   *Como:* threat model dedicado ao comprometimento do RM/PDP/broker OU redução do RM a núcleo mínimo; re-seeding de entropia pós-restore (AOS-065); distinguir conteúdo shreddable de metadados de atribuição do audit com base legal documentada.

7. **Formalizar decisões e o log de ADRs.**
   *Resolve:* RAST-05, RAST-06.
   *Como:* promover "Contexto≠registo" (ADR-015) e "Confiança de supply-chain/TOFU" (ADR-016); expandir todos os ADRs para formato-padrão (Contexto·Decisão·Alternativas·Consequências·Estado).

8. **Sanear o backlog para agendabilidade.**
   *Resolve:* VIAB-03, VIAB-04, VIAB-05, VIAB-06, VIAB-08, VIAB-09.
   *Como:* substituir dependências de epic inteiro por AOS-NNN; reclassificar hipercare/game-day como actividade operacional; decompor os "L" incertos; antecipar o spike AOS-022; declarar "aditivo vs implementação" nas capacidades replicadas; calibração de capacidade + ondas de sprint.

### P2 — Higiene, consistência e navegabilidade

9. **Fonte única de verdade e correcções técnicas menores.**
   *Resolve:* COMP-07, COMP-08, COER-02, COER-03, COER-04, RIG-07(gVisor), RIG-09, RIG-10(Redis), RIG-11, RIG-13, RIG-15, CLR-02, CLR-03, CLR-04, CLR-05, CLR-06, RAST-07, RAST-08, XC-08, COMP-06.
   *Como:* glossário/registo de ADRs mestre; lint PT-BR e lint de referências cruzadas; normalização de metadados/títulos/cabeçalhos; mini-diagramas por epic; correcções pontuais de rigor (gVisor, replay, Redis, breaker, Rego/Cedar); rubrica de severidade única; STRIDE como enriquecimento opcional.

---

## 7. Veredicto de prontidão

### Escala de maturidade documental (proposta D0–D4)

| Nível | Designação | Critério |
|-------|-----------|----------|
| **D0** | Rascunho | Ideias dispersas, sem estrutura nem decisões registadas. |
| **D1** | Narrativa coerente | Blueprint conceptual consistente: visão, princípios, ADRs, sem backlog executável. |
| **D2** | Referência estruturada | Estrutura uniforme + backlog executável com critérios SMART e ADRs propagados, **mas** contratos, modelo de dados, RTM e conformidade ainda informais ou ausentes; camada legível-por-máquina não validada. |
| **D3** | Referência implementável e verificável | Contratos de porta, modelo de dados/eventos, política de referência, RTM bidireccional, grafo de dependências validado em CI, NFRs escopados/mensuráveis, matriz de conformidade. Uma equipa autónoma pode executar sem reinterpretar. |
| **D4** | Referência certificável | Provas de validação dos NFR (benchmarks/game-days), conformidade auditável ponta-a-ponta, fonte-única-de-verdade governada contra deriva, threat model completo do TCB. Pronta para auditoria externa/regulatória. |

### Nível actual: **D2** (sólido, mas travado no limiar de D3 por um defeito crítico)

O corpus tem a **estrutura e o backlog de D2 com qualidade acima da média**, e pontos de D3 já presentes (perfis de leitor, diagramas de sequência, DoD ancorada a ADRs, EPIC-11 a fechar requisito→teste). **Não atinge D3** porque: (a) a camada legível-por-máquina está partida — o RM está invisível no grafo (XC-01); (b) os gates de CI validam artefactos inexistentes (XC-03); (c) faltam contratos, modelo de dados e RTM; (d) os NFR são aspiracionais e por vezes mal-escopados; (e) a conformidade é afirmada mas não mapeada.

### O que falta para o nível seguinte (D2 → D3)

- Resolver **todos os itens P0** (grafo reconciliado + RM visível; artefactos dos gates; NFRs re-escopados; retórica desactivada).
- Entregar **contratos de porta, modelo de dados/eventos e ≥1 política de referência** (COMP-01/02).
- Publicar a **RTM bidireccional e o campo ADR estruturado** (RAST-01/02).
- Publicar a **matriz de conformidade EU AI Act/GDPR** (COMP-03).
- **Validar aciclicidade e reciprocidade** do grafo em CI (VIAB-07).

Para **D3 → D4**, adicionalmente: validar empiricamente os NFR (benchmarks, game-days de DR ratificando RPO/RTO), threat model do TCB (XC-06), e instituir a fonte-única-de-verdade governada que impeça a deriva já observada.

---

## 8. Conclusão

O AOS documenta-se com uma **ambição e uma disciplina invulgares**: 118 tickets particionados sem folga, 14 ADRs propagados sem drift, um modelo de execução durável tecnicamente correcto e um backlog cujos critérios de aceitação são genuinamente SMART. Como *blueprint conceptual*, é forte e coerente — merece o crédito de estar no topo da faixa D2.

Mas uma referência de engenharia vale pela sua **camada verificável**, e é aí que o corpus falha de forma que a leitura em silos não revela. O defeito-raiz de rotulagem de dependências não é um erro cosmético: **faz desaparecer o Reference Monitor — a invariante nuclear de segurança do produto — do grafo executável e dos handoffs**, ao ponto de um executor diligente poder construir o runtime sem ele. À volta desse defeito acumulam-se três fragilidades que se reforçam mutuamente: **gates de CI que validam artefactos inexistentes, NFRs aspiracionais propagados como se fossem provados, e uma retórica de segurança absolutista que sustenta uma alegação de conformidade regulatória por mapear.**

Nenhuma destas quatro questões é fatal, e nenhuma exige repensar a arquitectura — todas são **corrigíveis com trabalho documental focado**, concentrado nos quatro itens P0. Corrigidos esses, e entregues os contratos, o modelo de dados, a RTM e a matriz de conformidade (P1), o corpus atravessa o limiar para **D3 — uma referência que uma equipa autónoma pode executar sem reinterpretar**.

**Recomendação final:** não entregar a documentação a uma equipa de execução autónoma no estado actual. Executar P0 como pré-condição de entrega (bloqueador de release), P1 como condição de maturidade D3, e P2 em contínuo. A qualidade da fundação justifica plenamente o investimento — o que falta é fechar o fosso entre a narrativa excelente e a camada verificável que a deve suportar.

---
*Fim do relatório. Todas as constatações referenciadas foram validadas contra o corpus pela contra-auditoria; as quatro disputas foram arbitradas e reclassificadas nesta consolidação (§4.2).*