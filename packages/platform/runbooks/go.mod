module github.com/aos-ref/platform/runbooks

go 1.24

// AOS-106 — Registo de runbooks operacionais + gate de NÃO-ÓRFÃOS bidireccional
// alerta↔runbook. Este módulo COMPÕE (não reimplementa) o alerting-as-code de
// AOS-105: importa substrate/otel-genai APENAS para ler os IDs de runbook que os
// alertas referenciam (DefaultOperationalAlertConfig) e validar, fail-closed, que
// todo o ID referenciado resolve para uma entrada de runbook — e que cada runbook
// canónico RB-01..RB-05 está no registo. Import DOWN (platform → substrate); zero
// dependências externas. O replace NÃO é transitivo mas otel-genai é FOLHA zero-dep,
// pelo que basta a sua aresta.
require github.com/aos-ref/substrate/otel-genai v0.0.0

replace github.com/aos-ref/substrate/otel-genai => ../../substrate/otel-genai
