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

**Score:** 7.5/10

> **Veredicto:** O corpus é fortemente coerente nos invariantes carregados (118 tickets sem sobreposição/lacuna, 14 ADRs sem conflito, catálogo de componentes usado à letra), mas arrasta uma conflação real de NFR (overhead de mediação vs avaliação do PDP) e vários artefactos de desactualização deixados pela remediação P0 que acrescentou os docs 12–14 sem os propagar.

## Pontos fortes

- Ranges de tickets AOS-NNN perfeitamente consistentes entre _BRIEF §8, specs/INDICE §3.1 e specs/00_System_Spec §8 (linhas 225-235): 11 epics, 118 tickets, sem sobreposição nem lacuna; nenhuma referência AOS-NNN fora do intervalo 001-118 em todo o corpus.
- Os 14 ADRs são numerados e referenciados de forma consistente; o conteúdo de ADR-014 é idêntico em _BRIEF:83, tecnica/00:249, tecnica/09:49 e specs/00:256; instrução uniforme de 'numerar novos ADRs após ADR-014' (specs/01:103, EPIC-02:680/717) sem qualquer ADR-015 órfão nem contradição de superseding.
- Catálogo de componentes (_BRIEF §2) usado à letra — RM, RT, ORQ, SCH, PDP, MEM, REG, GW, BRK, ES, SBX, OBS, GOV — sem variantes traduzidas/renomeadas (ex.: nenhum 'Broker de Credenciais' ou 'Monitor de Referência').
- Os rótulos do mapa de dependências em specs/INDICE §7 batem certo com os títulos reais dos tickets (ex.: AOS-009=Barramento de eventos, AOS-012=Esqueleto do plano de controlo, AOS-028=max_spawn/headroom, AOS-072=Audit WORM).
- Alvos de NFR estáveis (99,9% disponibilidade; cold-start <125 ms restore 5-30 ms; cache-hit-rate >80%; fidelidade de replay 100%) idênticos em _BRIEF §4, tecnica/00 §9 e specs/00_System_Spec, e reproduzidos coerentemente nos EPIC-07/10.
- A contagem de 51 diagramas Mermaid declarada em tecnica/INDICE:51 corresponde exactamente à contagem real (grep confirma 51 blocos).

## Constatações

| ID | Severidade | Localização | Problema | Recomendação |
|---|---|---|---|---|
| COER-01 | alta | tecnica/01:83,223,235; tecnica/10:181; specs/INDICE:187; specs/EPIC-01:31,194,200,218,228,256; EPIC-05:532,545,554; EPIC-08:574; EPIC-09:84,92,107; EPIC-10:420,425,573 — vs canónico _BRIEF §4, tecnica/00 §9 (linha 261) e specs/00_System_Spec:294 | Conflação de duas NFRs distintas. A fonte canónica separa 'Latência de avaliação do PDP p95 < 15 ms' de 'Overhead total de mediação por tool call', cujo alvo agregado está explicitamente 'a ratificar por benchmark' (decomposto por sub-passo: PDP + CAS + broker→vault + append ES + egress/DNS). No entanto ~15 locais afirmam 'overhead de mediação (RM) p95 < 15 ms' como SLO firme, aplicando o número só-PDP ao caminho de mediação inteiro. O contraste é interno: specs/00_System_Spec:294 corrige ('só avaliação de política; o overhead total decompõe-se por sub-passo'), mas EPIC-10:573 e specs/INDICE:187 listam '<15 ms' como SLO canónico, e EPIC-01 (AOS-003) bake-a como critério de aceitação 'medido em benchmark' — a contradizer o 'a ratificar' da fonte. | Distinguir as duas métricas em todo o lado: manter 'PDP eval p95 < 15 ms' como SLO firme; renomear a métrica agregada para 'Overhead total de mediação (RM) p95 — decomposto por sub-passo, alvo a ratificar por benchmark'. Actualizar specs/INDICE:187, tecnica/10:181 e os critérios de aceitação de AOS-003 (EPIC-01) para não fixarem <15 ms ao overhead total. |
| COER-02 | média | tecnica/INDICE:169 ('12 documentos') e specs/INDICE:198 ('Técnica \| 12 docs de desenho por subsistema') vs tecnica/INDICE:51 ('Total: 15 documentos') | Contagem do conjunto técnico desactualizada após a remediação P0 que acrescentou os docs 12–14. A tabela de controlo de versões de tecnica/INDICE (linha 169) ainda diz 'conjunto técnico (12 documentos)' com uma só linha 1.0 — contradiz o cabeçalho da mesma ficha (linha 51: 15 documentos). specs/INDICE §9 (linha 198) descreve a técnica como '12 docs'. É uma contradição facto-contra-facto entre e dentro de ficheiros. | Actualizar ambas as contagens para 15 e acrescentar uma linha de changelog (ex.: v1.1 — Julho 2026 — 'adicionados docs 12–14 na remediação P0') na tabela de controlo de versões de tecnica/INDICE. |
| COER-03 | média | tecnica/INDICE:156-157 ('Cada documento técnico mapeia para o epic homónimo (ex.: tecnica/07 ↔ specs/EPIC-07)') | A afirmação de mapeamento homónimo é falsa em três casos: (a) tecnica/11 'Convenções de Engenharia e Evolução' vs EPIC-11 'Testes e Qualidade' — tópicos diferentes; o conteúdo de tecnica/11 (SemVer, auto-modificação, padrões de código) dispersa-se por EPIC-05/09 e specs/01, e EPIC-11 (testes) não tem doc técnico dedicado; (b) tecnica/00 mapeia para specs/00_System_Spec, não para um epic; (c) os docs 12, 13 e 14 (Contratos, Modelo de Dados, Matriz de Conformidade) não têm epic homónimo. | Substituir a frase por uma tabela explícita de mapeamento tecnica↔epic que mostre os pares homónimos 01–10 e liste as excepções (00→System Spec; 11 cruza EPIC-05/09/11; 12–14 são transversais sem epic próprio). |
| COER-04 | média | tecnica/INDICE §3.1, §3.2, §3.3 (linhas ~61-94) e diagrama de hierarquia §4 (linhas ~100-126) | Integração incompleta dos docs 12–14: aparecem apenas na tabela-índice §2 (linhas 47-49) mas estão ausentes de todas as vistas de navegação — §3.1 (leitura por perfil), §3.2 (por camada, termina em 11), §3.3 (por fase, termina em 11) e o diagrama ASCII de hierarquia §4 (termina em '11 Convenções e Evolução'). Um leitor que use as vistas de navegação nunca é encaminhado para Contratos/Modelo de Dados/Matriz de Conformidade. | Acrescentar os docs 12–14 às tabelas §3.1/§3.2/§3.3 (ex.: 12 e 13 em 'Serviços/Transversal', 14 em 'Governação') e ao diagrama de hierarquia §4. |
| COER-05 | baixa | tecnica/14_Matriz_Conformidade.md:42,60,104,125 | Violação de PT-PT (mandatado em _BRIEF §0 e §9): uso das formas PT-BR 'ativar'/'ativa'/'ativação' em vez de 'activar'/'activa'/'activação'. O resto do corpus usa consistentemente as formas com -ct- (ex.: 'activa', 'detecção'). Também tecnica/00:147 tem 'deteccao' sem cedilha num nó Mermaid (embora aí seja aceitável para renderização). | Corrigir tecnica/14 para 'activar/activa/activação'; opcionalmente rever o nó Mermaid para 'detecção'. |
| COER-06 | baixa | tecnica/12_Contratos_de_Interface.md (1 diagrama) e tecnica/14_Matriz_Conformidade.md (1 diagrama) vs _BRIEF §7 ('mínimo 2 por doc') | Os dois docs adicionados na remediação P0 têm apenas um bloco Mermaid cada, violando a convenção canónica de mínimo 2 diagramas por documento fixada no _BRIEF §7. | Acrescentar um segundo diagrama a tecnica/12 (ex.: sequência de erro/idempotência) e a tecnica/14 (ex.: fluxo artigo→controlo→ticket), ou registar excepção explícita no _BRIEF. |
| COER-07 | baixa | tecnica/INDICE:35 (doc 00 = '377' linhas) e cabeçalho §2 ('~3.860 linhas') vs contagem real | Metadados de dimensão ligeiramente desactualizados: doc 00 tem 380 linhas reais (declarado 377); o total real do conjunto técnico é 4040 linhas (declarado '~3.860'). Discrepância menor mas de manutenção/coerência. | Recalcular a coluna 'Linhas' e o total agregado, ou remover contagens exactas em favor de intervalos, para evitar drift a cada edição. |

## Lacunas identificadas

- Não existe documento técnico dedicado a Testes e Qualidade (contraparte de EPIC-11): os tickets de EPIC-11 apoiam-se em tecnica/08 (Observabilidade) e tecnica/11 (Convenções), nenhum dos quais tem o teste/eval harness como foco — deixando o epic com 10 tickets sem âncora técnica homónima.
- Falta uma tabela/gate de integridade de cross-references entre conjuntos (tecnica↔specs↔ADR) que reconcilie os docs 12–14 nas vistas de navegação e no mapeamento homónimo; a remediação P0 acrescentou conteúdo mas não uma vista consolidada que garanta que futuras edições não voltem a deixar docs órfãos das tabelas §3/§4.
- O _BRIEF §7/§8 não inclui os docs 12–14 na lista canónica do conjunto TÉCNICA (só enumera 00–11 + INDICE); como o brief é a 'fonte de verdade' que fixa a lista de documentos, os três docs de remediação vivem fora do catálogo canónico — origem-raiz das inconsistências COER-02/03/04.
- Ausência de uma verificação automatizada (lint documental) de reciprocidade de dependências entre tickets (campos 'Dependências'/'Bloqueia'); embora não se tenham encontrado IDs inexistentes, não há garantia de que cada 'Bloqueia X' tenha o 'Depende de' recíproco em X.
