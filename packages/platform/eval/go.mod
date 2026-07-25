module github.com/aos-ref/platform/eval

go 1.24

require (
	github.com/aos-ref/platform/memory v0.0.0
	github.com/aos-ref/platform/registry v0.0.0
	github.com/aos-ref/substrate/otel-genai v0.0.0
)

// Integração por path local, ZERO dependências externas, build offline (AOS-114).
//
// O eval harness (EPIC-11) COMPÕE o vocabulário de eval do módulo FOLHA otel-genai
// (AOS-084): EvaluationResult/EvalVerdict/EvalDataset/EvalGate/FailClosedGate/
// EvalRunner/EvalTarget/RecordEvaluation. NÃO reimplementa nenhum destes tipos.
//
// O subpacote gateadapter fornece adaptadores FINOS às portas Evaluate(...) dos
// consumidores já existentes (platform/registry/promotion e
// platform/memory/procedural) — o ponto de injecção Metrics func. Por isso o módulo
// requer registry e memory por path local. Como os replace de um módulo NÃO são
// transitivos, redeclaram-se aqui, a partir da raiz de packages/, todos os módulos
// da árvore transitiva (audit/eventstore/agent-runtime/reference-monitor). NÃO se
// altera nenhum dos pacotes referenciados — só se importam.
replace github.com/aos-ref/substrate/otel-genai => ../../substrate/otel-genai

replace github.com/aos-ref/platform/registry => ../registry

replace github.com/aos-ref/platform/memory => ../memory

replace github.com/aos-ref/platform/audit => ../audit

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore

replace github.com/aos-ref/kernel/agent-runtime => ../../kernel/agent-runtime

replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor
