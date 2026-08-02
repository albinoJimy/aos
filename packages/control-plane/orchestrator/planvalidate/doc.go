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
//   - NÃO deriva risco nem re-preça orçamento (regras 5–6): isso é AOS-232.
//   - NÃO computa o hash do snapshot (AOS-243): só ACEITA o snapshot pinado e
//     confere que o seu Hash bate com o `capabilities_hash` que o plano carimbou.
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
