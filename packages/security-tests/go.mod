module github.com/aos-ref/security-tests

go 1.24

require (
	github.com/aos-ref/control-plane/pdp v0.0.0
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/platform/audit v0.0.0
	github.com/aos-ref/platform/broker v0.0.0
	github.com/aos-ref/platform/memory v0.0.0
	github.com/aos-ref/platform/messaging v0.0.0
	github.com/aos-ref/platform/registry v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
	github.com/aos-ref/substrate/sandbox v0.0.0
)

require (
	github.com/aos-ref/control-plane/governance/autonomy v0.0.0 // indirect
	github.com/aos-ref/control-plane/governance/sovereignty v0.0.0 // indirect
	github.com/aos-ref/kernel/agent-runtime v0.0.0 // indirect
	github.com/aos-ref/substrate/otel-genai v0.0.0 // indirect
	github.com/aos-ref/substrate/redaction v0.0.0 // indirect
	github.com/cedar-policy/cedar-go v1.8.0 // indirect
	golang.org/x/exp v0.0.0-20220921023135-46d9e7742f1e // indirect
)

// Módulo SÓ-DE-TESTES (AOS-075, o ÚLTIMO do EPIC-07; ESTENDIDO por AOS-117 no
// EPIC-11). Integração por path local: ZERO dependências externas ALÉM de
// cedar-go (o motor de política do PDP, AOS-113 — pin+hash, supply-chain mínima),
// build offline (padrão de infra do repo). A suite adversarial ORQUESTRA — não
// reimplementa — os controlos REAIS da fronteira de segurança e prova, por
// adversário, que cada vector prioritário (prompt injection LLM01/ASI01 +
// exfiltração CamoLeak) é reproduzido e BLOQUEADO:
//   - Taint control/data-plane (AOS-069): reference-monitor + reference-monitor/taint
//     (o TaintGate NEGA acção privilegiada autorizada por untrusted);
//   - Egress default-deny + DNS (AOS-067/068): substrate/sandbox/network
//     (EgressFilter/DNSFilter DENY + audit WORM);
//   - Segredos/broker (AOS-070): platform/broker (o segredo NUNCA observável a jusante);
//   - Isolamento (AOS-066): substrate/sandbox + .../seccomp (overlay não persiste,
//     seccomp bloqueia, sem socket do host);
//   - Audit WORM (AOS-072): platform/audit (cada bloqueio sela tamper-evident);
//   - Memory poisoning (AOS-042): platform/memory/provenance (untrusted em quarentena,
//     barreira de tipo — DataItem não autoriza);
//   - Hallucination gate (AOS-073): platform/messaging (assinatura+autoridade+referência);
//   - Re-aprovação de schema MCP (AOS-049): platform/registry/tofu (rug-pull in-band recusado);
//   - PDP real (AOS-113): control-plane/pdp (allowlist default-deny COMO hook "policy").
//
// Como os replace de um módulo dependente NÃO são transitivos, TODOS os módulos do
// grafo (memory/messaging/registry/pdp → agent-runtime/governance/identity + broker →
// sandbox/reference-monitor/eventstore/audit/otel-genai) são re-declarados aqui a
// partir da raiz de packages/. NENHUM destes importa esta suite, logo NÃO há ciclo.
// NÃO alterar os pacotes referenciados (chore de testes; o código de produção é intocado).
replace github.com/aos-ref/kernel/reference-monitor => ../kernel/reference-monitor

replace github.com/aos-ref/platform/audit => ../platform/audit

replace github.com/aos-ref/platform/broker => ../platform/broker

replace github.com/aos-ref/substrate/eventstore => ../substrate/eventstore

replace github.com/aos-ref/substrate/sandbox => ../substrate/sandbox

replace github.com/aos-ref/substrate/otel-genai => ../substrate/otel-genai

// AOS-117 (EPIC-11) — cenários novos: memory poisoning, hallucination gate,
// re-aprovação de schema MCP e cenário 1 reforçado com o PDP REAL. Cada módulo
// novo e os seus replace TRANSITIVOS (não-transitivos por desenho do Go) são
// re-declarados aqui a partir da raiz de packages/.
replace github.com/aos-ref/platform/memory => ../platform/memory

replace github.com/aos-ref/platform/messaging => ../platform/messaging

replace github.com/aos-ref/platform/registry => ../platform/registry

replace github.com/aos-ref/control-plane/pdp => ../control-plane/pdp

replace github.com/aos-ref/kernel/agent-runtime => ../kernel/agent-runtime

replace github.com/aos-ref/control-plane/governance/autonomy => ../control-plane/governance/autonomy

replace github.com/aos-ref/control-plane/governance/sovereignty => ../control-plane/governance/sovereignty

replace github.com/aos-ref/platform/identity => ../platform/identity

replace github.com/aos-ref/substrate/redaction => ../substrate/redaction
