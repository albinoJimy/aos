module github.com/aos-ref/platform/hipercare

go 1.24

// AOS-108 — Hipercare e operacionalização (FECHO do EPIC-10). Este módulo é um HARNESS
// DE RELATÓRIO que COMPÕE (nunca reimplementa nem altera) as peças já Done do epic:
//
//   - substrate/otel-genai (AOS-104/105): projecta o OperationalSnapshot dos SLIs
//     canónicos sobre a janela de hipercare e lê os SLOs+amostras (anti-vacuidade);
//   - platform/dr (AOS-102): compõe a GameDayEvidence do game day repetido (RPO/RTO);
//   - platform/runbooks (AOS-106): compõe CanonicalIDs para exigir MTTR por RB-01..RB-05.
//
// LAYERING: import DOWN (platform → substrate; platform/hipercare → siblings platform/dr,
// platform/runbooks). NÃO importa control-plane (seria up-import ilegal). Zero deps
// externas — o fecho é 100% aos-ref + stdlib.
//
// Os replace NÃO são transitivos: re-declaram-se aqui TODAS as arestas do fecho de
// platform/dr (backup, audit, agent-runtime, eventstore, reference-monitor, otel-genai)
// para o build fechar OFFLINE.
require (
	github.com/aos-ref/platform/dr v0.0.0
	github.com/aos-ref/platform/runbooks v0.0.0
	github.com/aos-ref/substrate/otel-genai v0.0.0
)

require (
	github.com/aos-ref/kernel/agent-runtime v0.0.0 // indirect
	github.com/aos-ref/kernel/reference-monitor v0.0.0 // indirect
	github.com/aos-ref/platform/audit v0.0.0 // indirect
	github.com/aos-ref/platform/backup v0.0.0 // indirect
	github.com/aos-ref/substrate/eventstore v0.0.0 // indirect
)

replace github.com/aos-ref/platform/dr => ../dr

replace github.com/aos-ref/platform/runbooks => ../runbooks

replace github.com/aos-ref/substrate/otel-genai => ../../substrate/otel-genai

replace github.com/aos-ref/kernel/agent-runtime => ../../kernel/agent-runtime

replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor

replace github.com/aos-ref/platform/audit => ../audit

replace github.com/aos-ref/platform/backup => ../backup

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore
