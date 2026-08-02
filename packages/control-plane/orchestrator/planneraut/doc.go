// Package planneraut implementa a AUTONOMIA L0–L5 DO PLANEADOR e os SLIs de
// planeamento (AOS-242, EPIC-18/tecnica/18 §7). É a GOVERNANÇA do nível de
// autonomia com que o planeador opera — quanto pode auto-aprovar sem humano — e o
// mecanismo que SOBE esse nível por fiabilidade MEDIDA e o BAIXA por anomalia. Não
// planeia nem aprova: consome sinais e produz níveis e decisões que o gate
// (AOS-236, por porta) e o wiring aplicam.
//
// Cinco propriedades fail-closed sustentam o desenho:
//
//  1. SINAIS SOBRE JANELA, PUROS. [ComputeSignals] é uma função PURA sobre
//     contadores de uma janela: taxa de aprovação SEM edição, taxa de re-plano,
//     calibração de custo (AOS-124) e taxa de propostas inválidas. A taxa de
//     override é AUTORITATIVA de AOS-095 e entra por PORTA ([OverrideRateSource]) —
//     nunca auto-reportada pelo planeador. É este ancoramento que torna a promoção
//     NÃO-GAMEÁVEL (DoD): contadores próprios perfeitos não promovem se a fonte de
//     override independente reportar overrides a mais.
//
//  2. ASSIMETRIA — PROMOVER DEVAGAR, DEMOVER JÁ. A promoção exige um DOMÍNIO
//     RECORRENTE: MinRecurrence janelas SUSTENTADAS e SÃS (AOS-014). Um objectivo
//     AD-HOC — cujo domínio não recorre — nunca acumula a série e fica em L0 POR
//     DESENHO. A DEMOÇÃO é imediata: UM sinal fora do envelope BAIXA o nível a L0 e
//     reinicia a série (a autonomia ganha-se em muitas janelas, perde-se numa). É a
//     assimetria que impede o rubber-stamp por conveniência (DoD).
//
//  3. AUTO-APROVAÇÃO SOBRE RISCO DERIVADO. A L4/L5 o planeador auto-aprova DENTRO
//     de um envelope, mas a decisão é avaliada sobre o RISCO DERIVADO das
//     ferramentas pinadas (AOS-232) — NUNCA o rótulo `risk_class` do LLM (advisory
//     untrusted, ADR-005, que só pode ELEVAR o piso, jamais baixá-lo). Um nó de
//     risco derivado `danger`, ou qualquer `capability_gap`, FORÇA SEMPRE revisão
//     humana — nunca auto-aprova, mesmo a L5.
//
//  4. TRAVÃO DE RUNTIME INDEPENDENTE DO HUMANO. O eval-gate de decomposição
//     (AOS-241, porta [DecompositionEvalGate]) é PRÉ-CONDIÇÃO de qualquer
//     auto-aprovação e corre mesmo a L4/L5: se reprovar, a auto-aprovação é travada
//     e a revisão humana é forçada — sem depender de um humano no laço. Há ainda
//     AMOSTRAGEM post-hoc mesmo a L4/L5 (uma fracção das auto-aprovações é marcada
//     para escrutínio a posteriori).
//
//  5. SLI DE FRACÇÃO DE PLANEAMENTO. A fracção do esforço gasta a planear é um SLI
//     exposto como sinal ([Signals.PlanningFraction], [Governor.PlanningFractionSLI])
//     com tecto de 5% ([DefaultMaxPlanningFraction]). Exceder o tecto é uma anomalia
//     de envelope como qualquer outra — demove.
//
// GRANULARIDADE DE DOMÍNIO (declarada). Um «domínio» é o par (tenant, ASSINATURA
// estrutural de capabilities/papéis) — ver [NewDomainKey] — e NUNCA o `objective`
// de texto livre untrusted. Dois objectivos com o mesmo conjunto de classes de
// capability (em qualquer ordem) partilham domínio; um objectivo com um conjunto
// único de classes é ad-hoc e não recorre.
//
// FRONTEIRA. planneraut não emite eventos novos (não há tipo de evento de
// autonomia em plannerevents e é proibido cunhar um); expõe níveis, sinais e
// decisões por valor. As dependências de outros módulos (override AOS-095,
// eval-gate AOS-241, gate AOS-236) entram por PORTA; o [plan.RiskClass] é o tipo
// partilhado do risco derivado, importado (não redeclarado).
package planneraut
