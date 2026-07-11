module github.com/aos-ref/platform/audit

go 1.24

require github.com/aos-ref/kernel/reference-monitor v0.0.0

// O Reference Monitor (AOS-003) é integrado por path local para o adaptador
// AuditSink (rmadapter.go). Zero dependências externas, build offline.
replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor

// O RM depende transitivamente do Event Store (AOS-002); resolvemos o path
// local para que o build do audit feche sem rede. O audit NÃO usa o Event Store
// directamente — a correlação com o run faz-se por run_id/step_id (ADR-010).
require github.com/aos-ref/substrate/eventstore v0.0.0 // indirect

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore
