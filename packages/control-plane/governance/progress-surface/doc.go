// Package progresssurface é a SUPERFÍCIE de progresso, burn-down de custo e prompt de
// exaustão graciosa a ~80% do orçamento (AOS-123, EPIC-12). Substitui o HARD-STOP CEGO por
// budget — em que o run morria ao esgotar o orçamento, sem aviso nem escolha — por
// semântica de progresso + burn-down VISÍVEL e, ao limiar, um PROMPT de exaustão graciosa
// com três opções: ESTENDER / RESUMIR-E-PARAR / ABORTAR, cuja decisão volta ao
// orquestrador.
//
// # LÊ e DELEGA — não recontabiliza, não muta, não morre em silêncio
//
// Esta superfície NÃO reimplementa a contabilidade nem o enforcement. Compõe os sinais que
// JÁ existem:
//
//   - BURN-DOWN (AC1/AC4): o custo consumido é LIDO da agregação PÚBLICA de EPIC-08 —
//     otelgenai.AggregateByTrace(spans)[traceID] soma só os spans `chat` (sem
//     dupla-contagem). A superfície NUNCA re-soma custo a mão; ComputeBurndown devolve
//     EXACTAMENTE esse valor. Ver burndown.go.
//   - ORÇAMENTO (AC1): o Limit por-árvore vem de EPIC-03 (ADR-008) via a porta BudgetReader
//     (leitura pura — a superfície não reserva/debita/muta o orçamento).
//   - EXTENSÃO (AC3): OptionExtend é DELEGADA ao controlo via a porta BudgetExtender
//     (adaptador sobre scheduler.HeadroomController.Admit no wiring) — a superfície PEDE, o
//     admission control IMPÕE. Não há mutação do budget.Budget pela superfície.
//   - DEGRADAÇÃO (AC5): a ausência de resposta ao prompt aplica a política de degradação de
//     EPIC-03 via a porta Degrader (adaptador sobre scheduler.Degrader.ExecuteChain,
//     Reason "exhaustion_prompt_timeout") — NUNCA um hard-stop cego.
//   - PROGRESSO (AC1): a semântica de progresso vem da porta ProgressReflector (adaptador
//     sobre o controlsurface.StateProjector/state.Machine.Current no wiring).
//
// # Padrão porta+adaptador (core mínimo)
//
// O core importa SÓ o LEVE: otel-genai (AggregateByTrace/UsageTotals/Tracer) e budget
// (Amount). As portas (ports.go) desacoplam-no do scheduler/state; os adaptadores que os
// tocam vivem no WIRING (diferido/documentado, padrão budgetbridge/AOS-121) para não
// arrastar o model-gateway/scheduler inteiro para o core.
//
// # Limiar configurável (AC5)
//
// O limiar ~80% é configurável via WithThreshold; um limiar inválido (<=0 ou >=1) cai no
// DefaultThreshold (fail-closed/default). Ver surface.go e prompt.go.
//
// # Observabilidade (DoD)
//
// Emite spans OTel do prompt (aos.control.exhaustion_prompt) e da decisão
// (aos.control.exhaustion_decision) ligados ao run por AttrRunID, com a fracção/opção —
// SEM segredos. Ver span.go.
//
// Referências: ADR-008 (orçamento por-árvore, dinheiro em micro-USD inteiro); EPIC-08
// (custo por span, gen_ai.usage.*); EPIC-03 (admission control, degradação, orçamento).
package progresssurface
