module github.com/aos-ref/platform/messaging

go 1.24

require github.com/aos-ref/platform/audit v0.0.0

// O audit (AOS-072) depende transitivamente do Reference Monitor (AOS-003, para o
// adaptador AuditSink) e este do Event Store (AOS-002). Os replace NÃO são
// transitivos, pelo que se re-declaram aqui a partir da raiz de packages/ para que
// o build feche offline. messaging NÃO os usa directamente.
require (
	github.com/aos-ref/kernel/reference-monitor v0.0.0 // indirect
	github.com/aos-ref/substrate/eventstore v0.0.0 // indirect
)

replace github.com/aos-ref/kernel/reference-monitor => ../../kernel/reference-monitor

replace github.com/aos-ref/substrate/eventstore => ../../substrate/eventstore

// AOS-073 — mensagens inter-agente assinadas. O módulo COMPÕE por porta (sem
// ciclos, nada o importa): a IDENTIDADE NHI (AOS-005/006, via a porta
// [NHIRegistry] — chave pública pinada + autoridade autoritativa do emissor), o
// BROKER/Vault (AOS-070/ADR-006, via a porta [Signer] — a chave PRIVADA de
// assinatura vive server-side e NUNCA entra neste módulo nem no runtime do
// agente) e o AUDIT tamper-evident (AOS-072/ADR-010, importado concretamente para
// SELAR as rejeições na cadeia WORM). O audit é zero-dep; identity/broker entram
// por porta para evitar puxar o seu grafo (rm/sandbox/eventstore) e qualquer
// ciclo. NÃO alterar o pacote referenciado — consome-se só pela API pública.
replace github.com/aos-ref/platform/audit => ../audit
