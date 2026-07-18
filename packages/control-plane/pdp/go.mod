module github.com/aos-ref/control-plane/pdp

go 1.24

require (
	github.com/aos-ref/control-plane/governance/autonomy v0.0.0
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/platform/audit v0.0.0
	github.com/aos-ref/platform/identity v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
	github.com/cedar-policy/cedar-go v1.8.0
)

require (
	github.com/aos-ref/substrate/otel-genai v0.0.0
	golang.org/x/exp v0.0.0-20220921023135-46d9e7742f1e // indirect
)

// Reference Monitor (AOS-003) integrado por path local para o adaptador
// PolicyCheck; traz o Event Store (AOS-002) transitivamente. Os replace de um
// módulo dependente NÃO são transitivos, pelo que o eventstore é re-declarado
// aqui a partir da raiz de packages/. Build offline, zero dependências externas
// além do motor Cedar (ADR-005: pin + hash, supply-chain mínima).
replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore

// Taxonomia de autonomia L0–L5 (AOS-089) integrada por path local: o PDP consulta o
// [autonomy.Oracle] no caminho de decisão para compor o oversight (nível × classe).
// Mesmo layer (control-plane/governance), sem ciclo — o autonomy NÃO importa o pdp.
replace github.com/aos-ref/control-plane/governance/autonomy => ../governance/autonomy

// Identidade (AOS-005/006) integrada por path local APENAS em teste: o teste de
// integração cross-package (identity_gate_integration_test.go) compõe o
// IdentityCheck real antes do PolicyCheck para provar que o gate default-deny
// keia na agent_class RESOLVIDA da NHI verificada, não na forjada pelo caller.
replace github.com/aos-ref/platform/identity => ../../platform/identity

replace github.com/aos-ref/substrate/otel-genai => ../../substrate/otel-genai

// Audit tamper-evident (EPIC-08/AOS-083) integrado por path local para o
// adaptador do changelog policy.changed (AOS-088): control-plane → platform,
// direcção descendente já estabelecida. Traz a hash-chain WORM (Store/Append).
replace github.com/aos-ref/platform/audit => ../../platform/audit
