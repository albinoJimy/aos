module github.com/aos-ref/control-plane/governance/compliance

go 1.24

// AOS-097 — o modelo de responsabilização + os relatórios de conformidade. É o
// módulo GOV (irmão de autonomy/dsar/hitl/sovereignty) que PROJECTA o audit
// tamper-evident (AOS-083) em relatórios query-time — NÃO reimplementa a
// hash-chain/Verify nem os eventos dos vários tickets; compõe-nos por leitura.
//   - audit (AOS-083): o Store WORM, os AuditRecord já redigidos/tokenizados e o
//     audit.Verify de que a integridade do relatório deriva.
//   - otel-genai (EPIC-08): o Tracer/span da geração do relatório (sem PII).
// Build offline, integração por path local (replace) como no resto do monorepo.
require (
	github.com/aos-ref/platform/audit v0.0.0
	github.com/aos-ref/substrate/otel-genai v0.0.0
)

replace github.com/aos-ref/platform/audit => ../../../platform/audit

replace github.com/aos-ref/substrate/otel-genai => ../../../substrate/otel-genai

// Transitivos do audit (AOS-083): o RM e o Event Store entram no grafo de build por
// serem dependências do audit. Os replace directives NÃO são herdados — este módulo
// tem de os resolver localmente para fechar offline.
require (
	github.com/aos-ref/kernel/reference-monitor v0.0.0 // indirect
	github.com/aos-ref/substrate/eventstore v0.0.0 // indirect
)

replace github.com/aos-ref/kernel/reference-monitor => ../../../kernel/reference-monitor

replace github.com/aos-ref/substrate/eventstore => ../../../substrate/eventstore
