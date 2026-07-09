module github.com/aos-ref/control-plane/pdp

go 1.24

require (
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
	github.com/cedar-policy/cedar-go v1.8.0
)

require golang.org/x/exp v0.0.0-20220921023135-46d9e7742f1e // indirect

// Reference Monitor (AOS-003) integrado por path local para o adaptador
// PolicyCheck; traz o Event Store (AOS-002) transitivamente. Os replace de um
// módulo dependente NÃO são transitivos, pelo que o eventstore é re-declarado
// aqui a partir da raiz de packages/. Build offline, zero dependências externas
// além do motor Cedar (ADR-005: pin + hash, supply-chain mínima).
replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore
