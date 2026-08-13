// Package planvalidate contém o VALIDADOR PURO de propostas de plano (AOS-231,
// tecnica/18 §3.3, regras 1–4). Recebe um [plan.PlanDocument] (dados *untrusted*,
// ADR-005) e um SNAPSHOT de capabilities PINADO — passado como ARGUMENTO, nunca
// obtido por lookup vivo — e devolve um veredicto ESTRUTURADO e determinístico.
//
// Fronteiras (o que este pacote NÃO faz):
//
//   - NÃO faz I/O nem chama o modelo. É uma função pura sobre (documento, snapshot,
//     tectos): mesmo input ⇒ mesmo veredicto (determinismo exigido pelo CA).
//   - NÃO re-valida a FORMA do schema (tipos, campos desconhecidos, cardinalidades):
//     isso é [plan.Decode] (regra 1 de forma). Aqui a «regra 1» são as invariantes
//     SEMÂNTICAS que a forma não cobre (compatibilidade de MAJOR, integridade
//     referencial das arestas, binding do snapshot).
//   - NÃO reimplementa a detecção de ciclos: REUTILIZA a aciclicidade incremental
//     do DAG de AOS-025 (o primitivo vive no pacote raiz `orchestrator`; a família
//     de eventos de AOS-025 vive no subpacote irmão `orchestrator/contract`).
//   - NÃO computa o hash do snapshot (AOS-243): só ACEITA o snapshot pinado e
//     confere que o seu Hash bate com o `capabilities_hash` que o plano carimbou.
//   - NÃO avalia condições: a gramática das arestas condicionais (ADR-022 §2.1) é
//     validada aqui na sua SEMÂNTICA DE GRAFO, mas a AVALIAÇÃO é do despachante sem
//     estado (`plandispatch`) sobre o resultado REGISTADO — nunca do validador (que
//     não vê resultados) e nunca do LLM em runtime.
//
// ARESTAS CONDICIONAIS (ADR-022 §2.1, AOS-270). O campo `conditional_on` atravessa
// as regras existentes sem regra nova: a regra 1 confere a integridade referencial
// da origem e recusa a sobreposição com `depends_on`; a regra 2 mete as arestas
// condicionais NO MESMO DAG de AOS-025 — é isso, e só isso, que impõe «uma aresta
// condicional nunca fecha ciclo» (o vector «ciclo disfarçado de condicional» morre
// no primitivo que já existia, não num detector novo); a regra 4 conta-as no
// fanout/profundidade, para que o outro canal de aresta não seja uma porta de saída
// dos tectos estruturais.
//
// PAPEL VERIFICADOR (ADR-022 §2.2, AOS-271; verifier.go). O literal
// [plan.RoleVerifier] é o ÚNICO papel reservado do schema, e declarar-o faz o
// validador impor três coisas: (V1) só um verificador emite o `verdict` que um ramo
// de qualidade consome — é isto que fecha a auto-certificação que AOS-270 deixou
// aberta; (V2) «produtor ≠ verificador», lido como «o verificador não certifica
// trabalho da sua PRÓPRIA sub-árvore de delegação» e decidido por ALCANÇABILIDADE no
// MESMO DAG de admissão de AOS-025 (nunca uma travessia própria); (V3) «read-only por
// construção» — um verificador não pina tools de EFEITO, com o critério DERIVADO dos
// eixos de risco pinados que a regra 6 já consome ([IsEffectTool]), não de uma lista
// de nomes. O clamp da NHI na materialização (AOS-237) é a segunda linha e consome o
// MESMO critério via [Snapshot.EffectOracle]; o RiskGate do RM (AOS-074) é a terceira.
//
// PAYLOAD TIPADO POR ARESTA (ADR-022 §2.3, AOS-272; payload.go). O `consumes` de um
// nó atravessa as regras existentes com quatro sub-códigos próprios: a origem tem de
// ser uma ARESTA DE ENTRADA já declarada (o que faz do grafo de dados um SUB-GRAFO do
// DAG de admissão — acíclico por construção, sem travessia nova); a origem tem de
// DECLARAR o output pedido; os tipos têm de ser IGUAIS (compatibilidade é identidade,
// sem subtipagem que o validador tivesse de raciocinar); e um payload de taint
// EFECTIVO `untrusted` não alimenta um consumidor com AUTORIDADE PRIVILEGIADA
// (ADR-005). O taint efectivo DERIVA do PRODUTOR ([plan.Node.EffectiveOutputTaint]:
// forma fechada E papel verificador; o advisory do documento só eleva) e a autoridade
// do consumidor DERIVA dos eixos pinados das suas tools, pelo MESMO critério
// [IsEffectTool] de §2.2 — uma definição, duas perguntas, zero taxonomias novas. O transporte por REFERÊNCIA (o oposto do blackboard) não vive
// aqui: é o facto `plan.payload_published` no emissor e o `PayloadResolver` no
// consumidor.
//
// Regras 5–6 (AOS-232) — RISCO DERIVADO e ORÇAMENTO RE-PREÇADO — vivem no MESMO
// pacote (risk.go, budget.go, resources.go) mas num ponto de entrada PRÓPRIO,
// [ValidateResources] (e o composto [ValidatePlan]), porque exigem inputs que as
// regras 1–4 não têm (um [Pricer] e a política de orçamento/risco) e produzem, além
// do veredicto, o RISCO RESOLVIDO por nó que o gate AOS-236 consome. O [Validate]
// das regras 1–4 mantém a assinatura pura de AOS-231. A regra 6 DERIVA o risco das
// tools PINADAS (o rótulo `risk_class` do LLM só ELEVA o piso, nunca o baixa); a
// regra 5 RE-PREÇA o custo de cada ramo (nunca o ecoa) e impõe o teto duro por-nó.
// Ambas continuam PURAS e sem I/O (assumindo um [Pricer] puro).
//
// Feedback fail-closed: uma proposta inválida devolve um [Verdict] com uma [Rule]
// ALLOWLISTED (as constantes de `orchestrator/plannerevents`, §3.3) e um [Locator]
// com coordenadas ESTRUTURAIS (node_id, coordenadas de tool) — NUNCA os campos de
// texto livre do documento (objectivos, papéis, prosa do modelo). O node_id é o
// único dado derivado do documento propagado ao feedback e é ELE PRÓPRIO limitado
// pela regra 1 a uma grammar de identificador (charset fechado, comprimento máximo),
// pelo que não veicula texto livre arbitrário. O contador de tentativas e o
// esgotamento (N=3 ⇒ falha de intake) são [Ledger].
package planvalidate
