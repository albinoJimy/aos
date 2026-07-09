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

> **Veredicto:** Corpus estruturalmente muito coerente (ranges de tickets perfeitos, dependências sem IDs órfãos, ADRs e a maioria dos NFRs uniformes), mas maculado por uma contradição semântica pervasiva sobre o alvo de latência de mediação e por afirmações estruturais/contagens desatualizadas após a remediação P0.

## Pontos fortes

- Esquema de tickets AOS-NNN totalmente íntegro: os 118 IDs (AOS-001..118) estão todos definidos, sem lacunas nem sobreposições, e os ranges por epic coincidem exactamente entre _BRIEF §8, specs/INDICE §3.1 e specs/00_System_Spec (linhas 225-235).
- Integridade referencial das dependências: todos os AOS-NNN citados em campos Dependências/Bloqueia resolvem para tickets reais — zero referências pendentes (comm -23 referenced/defined = vazio).
- Os 14 ADRs são numerados e descritos de forma consistente entre _BRIEF §3, tecnica/00 §8, tecnica/INDICE §5 e specs/00_System_Spec (linhas 243-253); tecnica/INDICE §5 aponta correctamente para §8 do doc 00.
- NFRs não-ambíguos uniformes em todo o corpus: cold-start < 125 ms (restore 5-30 ms), disponibilidade 99,9%, cache-hit-rate > 80%, fidelidade de replay 100% e o limiar de promoção de autonomia (erro <2% por 30 dias, ADR-014) aparecem com os mesmos valores em _BRIEF, tecnica e specs.
- RPO ≤ 1 min / RTO ≤ 30 min coincidem entre tecnica/10 (linha 168) e specs/EPIC-10 (AOS-101/AOS-102), ambos marcados como (proposta).
- Catálogo de componentes (RM, RT, ORQ, SCH, PDP, MEM, REG, GW, BRK, ES, SBX, OBS, GOV) usado de forma consistente com os nomes do _BRIEF §2 ao longo dos dois conjuntos.

## Constatações

| ID | Severidade | Localização | Problema | Recomendação |
|---|---|---|---|---|
| COER-01 | alta | _BRIEF.md §4 (l.94-95) vs tecnica/01 (l.83,223,235), tecnica/09 (l.102), tecnica/10 (l.100,181), specs/INDICE §8 (l.187), specs/00_System_Spec (l.294), specs/EPIC-01 (l.31,200,218,228,256), EPIC-05/08/09/10 | Contradição pervasiva entre o alvo de latência do PDP e o alvo de overhead total de mediação. O _BRIEF §4 fixa < 15 ms APENAS para 'Latência de avaliação do PDP p95' e declara explicitamente que o 'Overhead total de mediação por tool call' é um 'Alvo agregado a ratificar por benchmark' (SEM número). Contudo, dezenas de locais afirmam 'overhead de mediação (RM) p95 < 15 ms' como SLO/AC/gate — inclusive tecnica/01:256 ('< 15 ms p95 combinado') e specs/INDICE:187 ('Overhead de mediação (RM) p95 < 15 ms'). Há até contradição intra-specs: specs/00_System_Spec:294 corrige-o ('só avaliação de política; o overhead total de mediação decompõe-se por sub-passo') enquanto specs/INDICE:187 e os epics tratam o total de RM como se fosse 15 ms. | Escolher uma redacção canónica única. Recomenda-se: manter 'PDP p95 < 15 ms' (só avaliação de política) e substituir todas as ocorrências de 'overhead de mediação (RM) p95 < 15 ms' por 'PDP p95 < 15 ms; overhead total de mediação decomposto por sub-passo, alvo a ratificar por benchmark'. Corrigir prioritariamente specs/INDICE §8 métrica 7 e os critérios de aceitação/alertas em EPIC-01/EPIC-10 que hoje impõem um número que o _BRIEF recusa fixar. |
| COER-02 | média | tecnica/INDICE.md §6 (l.156-157) vs tecnica/11 (l.1) e specs/EPIC-11 (l.1) | Afirmação estrutural falsa: 'Cada documento técnico mapeia para o epic homónimo (ex.: tecnica/07 ↔ specs/EPIC-07)'. Isto não se verifica em 11 (tecnica/11 = 'Convenções de Engenharia e Evolução' vs EPIC-11 = 'Testes e Qualidade' — temas diferentes) nem em 12/13/14 (Contratos de Interface, Modelo de Dados, Matriz de Conformidade), que não têm epic homónimo. Um leitor que siga a regra a partir de tecnica/11 aterra no epic errado. | Reescrever §6 para: mapeamento homónimo válido para 00-10; documentar que tecnica/11 (Convenções/Evolução) alimenta EPIC-09 (ratificação/SemVer) e EPIC-11 (gates), e que tecnica/12-14 são transversais cujos requisitos se distribuem por vários epics — fornecendo um crosswalk explícito doc→tickets em vez de uma regra 1:1 que é falsa. |
| COER-03 | média | tecnica/INDICE.md (header l.51 vs versão l.169; diagrama §4 l.100-126; tabelas §3.1-3.3) e specs/INDICE.md §9 (l.198) | Contagens e navegação desatualizadas após a remediação que acrescentou os docs 12-14. tecnica/INDICE afirma 'Total: 15 documentos' (l.51) mas a sua própria tabela de versões (l.169) diz 'Emissão inicial do conjunto técnico (12 documentos)' sem linha de revisão; o diagrama de hierarquia (§4) e as tabelas de leitura por perfil/camada/fase (§3.1-3.3) só listam 00-11, omitindo 12-14. Em paralelo, specs/INDICE §9 (l.198) descreve a Técnica como '12 docs de desenho por subsistema'. | Actualizar: header e versão para 15 docs com uma linha v1.1 na tabela de versões ('acrescentados 12-14 na remediação P0'); incluir 12-14 no diagrama de hierarquia §4 e nas tabelas §3.1-3.3; corrigir specs/INDICE §9 para '15 docs (12 de subsistema + Contratos/Modelo de Dados/Matriz de Conformidade)'. |
| COER-04 | baixa | _BRIEF.md §2 (l.42,58), tecnica/00 (l.86), specs/EPIC-07 (l.19,31,48,63,759) | Terminologia de isolamento incoerente: várias fontes escrevem 'microVM Firecracker/gVisor', categorizando o gVisor como microVM, o que contradiz a formulação cuidada do próprio ADR-004 ('microVM (Firecracker/Kata) ou gVisor') — o gVisor é um kernel em espaço de utilizador, não uma microVM. Coexistem no corpus 'Firecracker/Kata' (12x) e 'Firecracker/gVisor' (8x). | Uniformizar para a formulação do ADR-004: 'isolamento ao nível do kernel: microVM (Firecracker/Kata) ou gVisor', evitando rotular o gVisor de microVM. Aplicar em _BRIEF §2, tecnica/00 §diagrama e em todo o EPIC-07. |
| COER-05 | baixa | tecnica/10 (l.110) e specs/EPIC-10 (l.186) vs _BRIEF §2 (l.42) e ADR-007 | Introdução de um transporte de Event Store fora do conjunto canónico: o _BRIEF §2 e o ADR-007 fixam 'transporte push (NATS/Redis/Postgres)', mas tecnica/10 e EPIC-10/AOS-100 acrescentam 'Kafka' como opção. Não é uma contradição forte (é aditivo) mas alarga o catálogo canónico sem o registar no _BRIEF. | Ou incluir Kafka/JetStream na lista canónica do _BRIEF §2 e ADR-007, ou remover Kafka de tecnica/10 e EPIC-10 para manter o conjunto NATS/Redis/Postgres coerente com a fonte. |
| COER-06 | baixa | specs/00_System_Spec.md (l.225) vs _BRIEF §8 / specs/INDICE / título do ficheiro; specs/INDICE §4.1 (l.85) vs cabeçalhos reais dos tickets | Variantes cosméticas de nomenclatura. (a) EPIC-01 é 'Fundações e Plano de Controlo' em todo o lado excepto System Spec l.225 que diz 'Fundações do Plano de Controlo'. (b) specs/INDICE §4.1 exemplifica o cabeçalho de ticket como '## AOS-016: Replay…' (dois-pontos) enquanto os cabeçalhos reais usam travessão ('## AOS-016 — Replay…'). | Alinhar o título de EPIC-01 em System Spec l.225 com a forma canónica 'Fundações e Plano de Controlo' e corrigir o exemplo em specs/INDICE §4.1 para usar o travessão, coincidindo com o formato real dos cabeçalhos. |

## Lacunas identificadas

- Os documentos técnicos 12 (Contratos de Interface), 13 (Modelo de Dados e Eventos) e 14 (Matriz de Conformidade) não têm epic homónimo nem um crosswalk explícito doc→tickets; os seus requisitos estão dispersos por vários epics sem um mapa que o torne rastreável.
- A tabela de versões de tecnica/INDICE.md não regista a remediação que passou o conjunto de 12 para 15 documentos (falta linha v1.1), pelo que o histórico de versões contradiz o header.
- O diagrama de hierarquia (tecnica/INDICE §4) e as tabelas de navegação por perfil/camada/fase (§3.1-3.3) não foram estendidos para cobrir os docs 12-14, deixando-os invisíveis para quem navega por essas vistas.
- Não existe uma tabela canónica única de SLOs partilhada que reconcilie a distinção PDP-eval vs overhead-total-de-mediação; a ausência desse artefacto único é a causa-raiz da contradição COER-01 replicada em ~10 ficheiros.
