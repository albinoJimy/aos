package dsar

import "errors"

var (
	// ErrNoSubject — pedido DSAR sem subjectID. Sem titular não há chave por-titular
	// a destruir (fail-closed): o pedido é recusado antes de qualquer selagem.
	ErrNoSubject = errors.New("dsar: subject_id obrigatorio")
	// ErrLegalHold — o titular (ou uma partição sua) está sob legal hold: o
	// apagamento é BLOQUEADO (fail-closed) e a chave preservada. O evento
	// dsar.blocked é selado; nenhuma chave é destruída.
	ErrLegalHold = errors.New("dsar: titular sob legal hold (apagamento bloqueado)")
	// ErrNoSealer — fluxo construído sem um EventSealer. O apagamento tem de ser
	// auditável (dsar.received/key_destroyed selados): sem sealer, fail-closed.
	ErrNoSealer = errors.New("dsar: event sealer obrigatorio")
	// ErrNoHoldOracle — fluxo construído sem um HoldOracle. O legal hold é uma
	// garantia de PRESERVAÇÃO P0 (não destruir dados sob litígio): sem um oráculo de
	// hold, o fluxo NÃO consegue provar que o titular não está retido e RECUSA o
	// apagamento fail-closed. Um sistema sem legal holds tem de passar um [NoHold]
	// EXPLÍCITO (opt-in auditável), nunca nil — para o fail-open nunca ser silencioso.
	ErrNoHoldOracle = errors.New("dsar: hold oracle obrigatorio (use NoHold{} para opt-in explicito de sem-preservacao)")
)
