// Package planner corre a DECOMPOSIÇÃO de um meta-objectivo como um AGENTE
// GOVERNADO (AOS-234): com identidade não-humana própria (`agent:planner`),
// orçamento (reserva de planeamento admitida ANTES de decompor) e observabilidade
// (spans OTel filhos do traceparent do run). É o PLN-decompositor — produz um
// [plan.PlanDocument] — e está SEPARADO do materializador (o ORQ, AOS-237), que
// fica FORA de escopo aqui.
//
// FRONTEIRA DE AUTORIDADE (ADR-018). O planeador NÃO detém a autoridade do
// ciclo-de-vida do run: é INVOCADO pelo Orquestrador, que é o dono. Este pacote
// não cria uma autoridade concorrente — modela apenas a mediação e a contabilidade
// da invocação, compondo as fundações já existentes (não reimplementa nenhuma):
//
//   - IDENTIDADE (AOS-005/006, platform/identity): a NHI `agent:planner` é emitida
//     on-behalf-of o token do run por [identity.Issuer.IssueChild], estendendo a
//     cadeia de delegação hash-linked (a raiz humana é preservada). A decomposição
//     corre SOB essa identidade.
//   - MEDIAÇÃO (AOS-003, kernel/reference-monitor, ADR-002): a admissão do planeador
//     é MEDIADA — a decomposição só arranca sob um Permit não-forjável do RM. Uma
//     mediação que negue recusa fail-closed, SEM tocar no orçamento.
//   - ORÇAMENTO (AOS-008, control-plane/budget): a RESERVA de planeamento
//     (contexto × tabela de custo × factor de retry) é admitida por reserva CAS
//     ANTES da decomposição. FAIL-CLOSED: sem reserva admitida, o [Decomposer] NÃO
//     é sequer invocado — o planeamento não arranca.
//
// OBSERVABILIDADE (AOS-077, substrate/otel-genai). Toda a fase de planeamento
// emite spans FILHOS do traceparent do run: um span-âncora `invoke_agent` do
// `agent:planner`, N spans `chat` (uma por tentativa de decomposição, portadores do
// custo em tokens/USD — o planeamento CUSTA tokens contabilizados) e um span de
// gate estrutural. Nenhum ponto cego na trajectória: a decomposição deixa de ser
// invisível ao burn-down e ao trace.
//
// O que este pacote NÃO faz (fronteiras deliberadas): a validação de GRAFO
// (aciclicidade, resolução de tools, tectos) e de risco/orçamento por-ramo são
// AOS-231/232; o gate HITL de aprovação-de-plano é AOS-121; a MATERIALIZAÇÃO do
// plano aprovado em eventos do DAG é o ORQ (AOS-237). O gate deste pacote é apenas
// a admissibilidade de FORMA do documento produzido (via [plan.Decode]), a
// fronteira mínima antes do handoff.
package planner
