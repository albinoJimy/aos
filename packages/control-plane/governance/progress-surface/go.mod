module github.com/aos-ref/control-plane/governance/progress-surface

go 1.24

// AOS-123 — SUPERFÍCIE DE PROGRESSO + BURN-DOWN DE CUSTO + prompt de exaustão
// graciosa a ~80% (EPIC-12, UX/HITL). Substitui o hard-stop CEGO por budget por uma
// SUPERFÍCIE que LÊ os sinais existentes e apresenta a escolha — NÃO reimplementa a
// contabilidade nem o enforcement. COMPÕE só o LEVE (padrão porta+adaptador, como
// plan-approval/AOS-121):
//   - otel-genai (EPIC-08, AOS-078): o custo por span já contabilizado. O burn-down LÊ
//     a agregação PÚBLICA (AggregateByTrace) — NUNCA re-soma custo.
//   - budget (EPIC-03, ADR-008): a denominação Amount do orçamento por-árvore. O core
//     lê via a PORTA BudgetReader; NÃO muta o orçamento.
// Os adaptadores que tocam o scheduler/state (HeadroomController.Admit para a extensão,
// Degrader.ExecuteChain para a degradação, StateProjector para o progresso) vivem no
// WIRING como portas (BudgetExtender/Degrader/ProgressReflector) — o core não arrasta o
// model-gateway/scheduler. Build offline; integração por path local (replace, não
// herdados). ZERO dependências externas (molde: control-surface/go.mod).
require (
	github.com/aos-ref/control-plane/budget v0.0.0
	github.com/aos-ref/substrate/otel-genai v0.0.0
)

replace github.com/aos-ref/control-plane/budget => ../../budget

replace github.com/aos-ref/substrate/otel-genai => ../../../substrate/otel-genai

// Transitivos do budget (EPIC-03): os replace NÃO são herdados dos módulos requeridos,
// pelo que este módulo resolve-os localmente para fechar o build offline. budget arrasta
// reference-monitor + eventstore (ambos folhas do substrato/kernel, zero-dep externa).
require (
	github.com/aos-ref/kernel/reference-monitor v0.0.0 // indirect
	github.com/aos-ref/substrate/eventstore v0.0.0 // indirect
)

replace github.com/aos-ref/kernel/reference-monitor => ../../../kernel/reference-monitor

replace github.com/aos-ref/substrate/eventstore => ../../../substrate/eventstore
