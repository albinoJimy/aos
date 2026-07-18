module github.com/aos-ref/control-plane/governance/autonomy

go 1.24

// AOS-089 — a taxonomia de autonomia L0–L5 com oversight proporcional. É um módulo
// GOV (irmão de dsar/hitl) que COMPÕE por porta — não reimplementa:
//   - risk (AOS-074): a [risk.Class] que [Oversight] compõe com o nível (a linha L3
//     reproduz o tiering SA-ROC base).
//   - audit (AOS-072): a hash-chain WORM onde as alterações de nível são seladas
//     (evento autonomy.level_changed, molde policy.changed de AOS-088).
//   - otel-genai (EPIC-08): o Tracer/span que expõe o nível corrente por (agente,
//     domínio) na observabilidade.
// Build offline, integração por path local (replace) como no resto do monorepo.
require (
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/platform/audit v0.0.0
	github.com/aos-ref/substrate/otel-genai v0.0.0
)

replace github.com/aos-ref/kernel/reference-monitor => ../../../kernel/reference-monitor

replace github.com/aos-ref/platform/audit => ../../../platform/audit

replace github.com/aos-ref/substrate/otel-genai => ../../../substrate/otel-genai

// Transitivo: o audit depende do RM que depende do Event Store. Os replace NÃO são
// herdados — este módulo resolve-o localmente para fechar o build offline.
require github.com/aos-ref/substrate/eventstore v0.0.0 // indirect

replace github.com/aos-ref/substrate/eventstore => ../../../substrate/eventstore
