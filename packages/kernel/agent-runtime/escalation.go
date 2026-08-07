package agentruntime

import "context"

// ---------------------------------------------------------------------------
// AOS-021 — EscalationSink (tool call escalada → espera por humano)
// ---------------------------------------------------------------------------

// PendingApproval descreve a tool call que ficou À ESPERA DE AVAL HUMANO. É o que o
// operador vê (por polling) para decidir, e o que amarra a aprovação à acção concreta.
//
// NÃO transporta o Input da tool: o payload pode conter dados sensíveis e não deve
// atravessar a superfície de administração. A amarra criptográfica é a Preview, que já
// cobre o input POR HASH ([referencemonitor.ApprovalPreview]).
type PendingApproval struct {
	// RunID e StepID localizam a acção na trajectória.
	RunID  string
	StepID string
	// Turn é o turno em que a acção foi proposta (o turno a REPRODUZIR na retoma).
	Turn int
	// ToolID e Capability descrevem O QUE vai executar, para o humano decidir.
	ToolID     string
	Capability string
	// Resource é o alvo concreto (tipo/valor/região) — a parte legível do "o quê".
	ResourceType   string
	ResourceValue  string
	ResourceRegion string
	// Preview é o digest canónico da call ([referencemonitor.ApprovalPreview]): o valor
	// que cada perna de aprovação assina (WYSIWYS) e contra o qual o grant é verificado.
	Preview []byte
}

// EscalationSink recebe uma tool call ESCALADA pelo Reference Monitor (veredicto
// `escalate`: requer gate humano, nenhum efeito ocorreu) e materializa a suspensão
// durável do run (running → waiting_on_human, AOS-017) mais o registo do pendente.
//
// É a porta do loop; o adaptador vive no composition root, que detém a máquina de
// estados por-run e a store de pendentes. O loop não faz transições duráveis por si.
//
// ADITIVA: sem [WithEscalationSink] o loop trata um `escalate` como trata uma negação
// (o marcador vai ao tail e o run prossegue) — o comportamento anterior, byte-idêntico.
type EscalationSink interface {
	// Escalate regista o pendente e suspende o run. Um erro é FATAL para o run
	// (fail-closed: se a suspensão durável falha, prosseguir deixaria o agente a
	// avançar como se nada tivesse ficado por decidir).
	Escalate(ctx context.Context, pending PendingApproval) error
}

// WithEscalationSink injecta o destino das escaladas (AOS-021). Um valor nil é ignorado
// (mantém o comportamento anterior: escalate ≡ negação, o run prossegue).
func WithEscalationSink(s EscalationSink) Option {
	return func(rt *Runtime) {
		if s != nil {
			rt.escalation = s
		}
	}
}

// ApprovalEvidenceSource fornece a EVIDÊNCIA de aprovação humana a anexar a uma tool call
// que está a ser mediada — o outro lado da escalada: é assim que, na RETOMA, a acção
// aprovada volta ao Reference Monitor acompanhada da prova.
//
// A consulta é POR PREVIEW (o digest canónico da call), não por índice nem por nome de
// tool: é a amarra exacta. Uma call que não seja aquela que o humano aprovou tem outra
// preview e não obtém evidência nenhuma.
//
// A fonte é infraestrutura TRUSTED do nó (a store de grants), NUNCA o modelo — o modelo
// não pode anexar autorização a si próprio. Os bytes devolvidos continuam a ser tratados
// como opacos e untrusted pelo [referencemonitor.ApprovalGate], que os verifica.
type ApprovalEvidenceSource interface {
	// EvidenceFor devolve a evidência para a call com esta preview, ou nil se não houver
	// aprovação pendente para ela. Nunca é erro não haver: é o caso normal.
	EvidenceFor(ctx context.Context, runID string, preview []byte) []byte
}

// WithApprovalEvidence injecta a fonte de evidência de aprovação (AOS-021). Um valor nil
// é ignorado (nenhuma call leva evidência — comportamento byte-idêntico).
func WithApprovalEvidence(s ApprovalEvidenceSource) Option {
	return func(rt *Runtime) {
		if s != nil {
			rt.approvalEvidence = s
		}
	}
}
