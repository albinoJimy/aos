module github.com/aos-ref/control-plane/governance/dsar

go 1.24

// O fluxo DSAR (AOS-093, direito ao apagamento — Art. 17) é o módulo GOV que
// COMPÕE, por porta, os stores de PII já existentes: o crypto-shredding do audit
// (AOS-083) e o KeySource de tokenização da redação (AOS-091). Não reimplementa a
// cifra/vault/shredder — orquestra a erasure UNIFICADA por-titular. Build offline,
// integração por path local (replace) como no resto do monorepo.
require (
	github.com/aos-ref/platform/audit v0.0.0
	github.com/aos-ref/substrate/otel-genai v0.0.0
	github.com/aos-ref/substrate/redaction v0.0.0
)

replace github.com/aos-ref/platform/audit => ../../../platform/audit

replace github.com/aos-ref/substrate/redaction => ../../../substrate/redaction

replace github.com/aos-ref/substrate/otel-genai => ../../../substrate/otel-genai

// Transitivos do audit (AOS-083): o RM e o Event Store entram no grafo de build
// por serem dependências do audit. Os replace directives NÃO são herdados — este
// módulo tem de os resolver localmente para fechar offline.
require (
	github.com/aos-ref/kernel/reference-monitor v0.0.0 // indirect
	github.com/aos-ref/substrate/eventstore v0.0.0 // indirect
)

replace github.com/aos-ref/kernel/reference-monitor => ../../../kernel/reference-monitor

replace github.com/aos-ref/substrate/eventstore => ../../../substrate/eventstore
