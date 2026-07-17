module github.com/aos-ref/security-tests

go 1.24

require (
	github.com/aos-ref/kernel/reference-monitor v0.0.0
	github.com/aos-ref/platform/audit v0.0.0
	github.com/aos-ref/platform/broker v0.0.0
	github.com/aos-ref/substrate/eventstore v0.0.0
	github.com/aos-ref/substrate/sandbox v0.0.0
)

// Módulo SÓ-DE-TESTES (AOS-075, o ÚLTIMO do EPIC-07). Integração por path local:
// ZERO dependências externas, build offline (padrão de infra do repo). A suite
// adversarial ORQUESTRA — não reimplementa — os controlos REAIS da fronteira de
// segurança e prova, por adversário, que cada vector prioritário (prompt injection
// LLM01/ASI01 + exfiltração CamoLeak) é reproduzido e BLOQUEADO:
//   - Taint control/data-plane (AOS-069): reference-monitor + reference-monitor/taint
//     (o TaintGate NEGA acção privilegiada autorizada por untrusted);
//   - Egress default-deny + DNS (AOS-067/068): substrate/sandbox/network
//     (EgressFilter/DNSFilter DENY + audit WORM);
//   - Segredos/broker (AOS-070): platform/broker (o segredo NUNCA observável a jusante);
//   - Isolamento (AOS-066): substrate/sandbox + .../seccomp (overlay não persiste,
//     seccomp bloqueia, sem socket do host);
//   - Audit WORM (AOS-072): platform/audit (cada bloqueio sela tamper-evident).
//
// Como os replace de um módulo dependente NÃO são transitivos, TODOS os módulos do
// grafo (broker → sandbox/reference-monitor/eventstore/audit) são re-declarados aqui
// a partir da raiz de packages/. NENHUM destes importa esta suite, logo NÃO há ciclo.
// NÃO alterar os pacotes referenciados (chore de testes; AOS-064..074 são código de
// produção intocado).
replace github.com/aos-ref/kernel/reference-monitor => ../kernel/reference-monitor

replace github.com/aos-ref/platform/audit => ../platform/audit

replace github.com/aos-ref/platform/broker => ../platform/broker

replace github.com/aos-ref/substrate/eventstore => ../substrate/eventstore

replace github.com/aos-ref/substrate/sandbox => ../substrate/sandbox
