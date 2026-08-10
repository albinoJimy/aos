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
// # A FONTE do burn-down (AOS-261) e o AVISO (AOS-262)
//
// O parágrafo acima descreve a via ORIGINAL — [ProgressSurface.Evaluate] sobre uma fatia de
// spans recebida por parâmetro. Essa via tinha um defeito estrutural: NADA, em nenhum nó,
// produz ou retém spans (o tracer por omissão é o NoopTracer e o SpanTracer real
// dispara-e-esquece), pelo que o chamador só tinha `nil` para passar e a superfície
// devolvia sempre 0% — verde e falso. Hoje `nil` é [ErrNoBurndownSpans].
//
// A via VIVA é [ProgressSurface.EvaluateRun] sobre a porta [BurndownSource], cujo adaptador
// de produção lê o LEDGER DE TURNOS (os eventos `turn.recorded` do Event Store). A escolha
// entre reter spans e ler o ledger, com as razões, está no cabeçalho de burndown_source.go.
//
// POLÍTICA MULTI-INCARNAÇÃO (AOS-261, critério 2): a chave é o `run_id` e o consumo é
// CUMULATIVO — o prefixo T1 (a incarnação que crashou/pausou) continua a contar, e a
// reprodução T2 (a retoma) não duplica porque a idempotency_key `run_id:step_id` deduplica
// na origem. Agregar por `trace_id` faria o oposto em ambos os casos: cada retoma abre um
// trace novo, o burn-down ressuscitaria a zero e um run em ciclo de retoma nunca atingiria
// o limiar.
//
// PRIMEIRA ENTREGA, SEM DECISÃO (AOS-262): [ProgressSurface.EvaluateRun] produz um
// [BudgetWarning] — um AVISO, com o span `aos.control.budget_warning` emitido UMA VEZ por
// run (latch). NÃO produz o [ExhaustionPrompt] nem as três opções: `extend`,
// `summarize_stop` e `abort` não têm executor nem autoridade no nó (eixo AOS-263), e
// apresentar uma escolha que ninguém consegue executar é prometer o que não existe. As
// portas [BudgetExtender] e [Degrader] continuam a existir e continuam por compor.
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
