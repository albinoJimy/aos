module github.com/aos-ref/platform/identity

go 1.24

require (
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
)

// Reference Monitor (AOS-003) integrado por path local para o adaptador
// IdentityCheck; traz o Event Store (AOS-002) transitivamente. Os replace de um
// módulo dependente NÃO são transitivos, pelo que o eventstore é re-declarado
// aqui a partir da raiz de packages/. Build offline, ZERO dependências externas:
// o token NHI (EdDSA) é implementado só com a stdlib (crypto/ed25519,
// encoding/json, encoding/base64) — sem bibliotecas JWT de terceiros.
replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore
