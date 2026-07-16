package network

import "errors"

var (
	// ErrNilResolver — [NewEgressFilter] sem [EgressPolicyResolver]. Sem resolver não
	// há forma de resolver uma allowlist; a construção recusa (fail-closed).
	ErrNilResolver = errors.New("network: egress policy resolver nil")

	// ErrNilFilter — [NewEgressHook] sem [EgressFilter].
	ErrNilFilter = errors.New("network: egress filter nil")

	// ErrNilSink — [NewEgressFilter] sem [SecurityAuditSink]. O sink WORM é OBRIGATÓRIO
	// para enforcement: um bloqueio de egress é um evento de segurança que TEM de ser
	// selado no WORM tamper-evident (AOS-067/AOS-072). Sem sink, um bloqueio negaria na
	// mesma mas ficaria por selar SILENCIOSAMENTE — a construção recusa (fail-closed),
	// tornando impossível compor um filtro de enforcement sem audit.
	ErrNilSink = errors.New("network: egress security audit sink nil")
)
