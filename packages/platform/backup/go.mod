module github.com/aos-ref/platform/backup

go 1.24

// AOS-101 — Backup imutável + PITR do Event Store. O módulo COMPÕE por path local:
//   - o Event Store (AOS-002/100) para o SNAPSHOT consistente e o RESTAURO que
//     preserva o envelope (portas BackupSource/RestoreSink, zero-dep);
//   - o audit (AOS-072/083) para REUTILIZAR as portas exportadas de cifra em
//     repouso (KeyVault, RandSource) e de retenção/legal-hold (RetentionPolicy,
//     LegalHold) — sem reimplementar KMS nem object-lock policy.
//
// Os replace NÃO são transitivos: re-declaram-se aqui todas as arestas do fecho
// (audit puxa reference-monitor + otel-genai) para o build fechar OFFLINE.
require (
	github.com/aos-ref/platform/audit v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
)

require (
	github.com/aos-ref/kernel/reference-monitor v0.0.0 // indirect
	github.com/aos-ref/substrate/otel-genai v0.0.0 // indirect
)

replace github.com/aos-ref/platform/audit => ../audit

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore

replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor

replace github.com/aos-ref/substrate/otel-genai => ../../substrate/otel-genai
