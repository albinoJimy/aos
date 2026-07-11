package audit

import (
	"context"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// MediationSink adapta o audit tamper-evident à porta [referencemonitor.EventSink]
// do Reference Monitor (AOS-003): cada decisão FINAL de mediação (allow/deny/
// escalate) é escrita na hash-chain com principal, capability e policy_version.
//
// Integração de alteração ZERO ao RM: o RM já entrega a decisão final pós-cadeia
// via RecordMediation (tanto no caminho de permit — audit-before-effect — como
// na negação). Basta implementar a porta.
//
// Fail-closed: no caminho de permit, se o Append falhar (auditoria indisponível),
// RecordMediation devolve erro e o RM degrada a decisão para Deny — uma acção
// não-auditável não é permitida (ADR-002/010).
type MediationSink struct {
	store       Store
	partitionOf func(rec referencemonitor.MediationRecord) string
	now         func() time.Time
}

// MediationSinkOption configura o MediationSink.
type MediationSinkOption func(*MediationSink)

// WithPartitioner define a função que deriva a partição de audit de cada
// mediação. Por omissão usa o RunID (cadeia contígua por run), caindo em
// "global" quando o RunID é vazio.
func WithPartitioner(fn func(rec referencemonitor.MediationRecord) string) MediationSinkOption {
	return func(a *MediationSink) { a.partitionOf = fn }
}

// withSinkClock injecta o relógio observacional (uso interno/testes).
func withSinkClock(fn func() time.Time) MediationSinkOption {
	return func(a *MediationSink) { a.now = fn }
}

// NewMediationSink constrói o adaptador sobre um [Store].
func NewMediationSink(store Store, opts ...MediationSinkOption) *MediationSink {
	a := &MediationSink{
		store:       store,
		partitionOf: defaultPartition,
		now:         time.Now,
	}
	for _, o := range opts {
		o(a)
	}
	if a.partitionOf == nil {
		a.partitionOf = defaultPartition
	}
	if a.now == nil {
		a.now = time.Now
	}
	return a
}

// RecordMediation implementa [referencemonitor.EventSink]. Devolve o audit_seq
// atribuído na cadeia da partição.
func (a *MediationSink) RecordMediation(ctx context.Context, rec referencemonitor.MediationRecord) (uint64, error) {
	ar := AuditRecord{
		Partition:     a.partitionOf(rec),
		Timestamp:     a.now(),
		Decision:      decisionFor(rec.Effect),
		Capability:    rec.Capability,
		PolicyVersion: rec.PolicyVersion,
		// Correlação e alvo selados na cadeia (não só na Partition): run_id/step_id/
		// parent_step_id/request_id, tool_id, resource e contexto de decisão. Assim
		// cada registo selado é atribuível à chamada mediada concreta (AOS-011-Q3).
		RunID:        rec.RunID,
		StepID:       rec.StepID,
		ParentStepID: rec.ParentStepID,
		RequestID:    rec.RequestID,
		ToolID:       rec.ToolID,
		Resource: Resource{
			Type:   rec.Resource.Type,
			Value:  rec.Resource.Value,
			Region: rec.Resource.Region,
		},
		Context: CallContext{
			Taint:         rec.Context.Taint,
			Reversibility: rec.Context.Reversibility,
			Sensitivity:   rec.Context.Sensitivity,
		},
		// Obligations impostas à decisão, seladas na cadeia (schema §5).
		Obligations: mapObligations(rec.Obligations),
		Principal: Principal{
			NHIID:           rec.Principal.NHIID,
			DelegationChain: mapChain(rec.Principal.DelegationChain),
		},
		// PayloadRef nil: o payload da tool NUNCA entra in-line no audit; a
		// referência cifrada por titular é preenchida por produtores que a possuam.
	}
	sealed, err := a.store.Append(ctx, ar)
	if err != nil {
		return 0, err
	}
	return sealed.AuditSeq, nil
}

// defaultPartition parte por RunID (contiguidade por run); "global" se vazio.
func defaultPartition(rec referencemonitor.MediationRecord) string {
	if rec.RunID != "" {
		return rec.RunID
	}
	return "global"
}

// decisionFor mapeia o Effect do RM para o vocabulário de audit.
func decisionFor(e referencemonitor.Effect) Decision {
	switch e {
	case referencemonitor.EffectPermit:
		return DecisionAllow
	case referencemonitor.EffectEscalate:
		return DecisionEscalate
	default:
		return DecisionDeny
	}
}

// mapObligations projecta as obligations do RM para o modelo do audit, com cópia
// profunda dos campos mutáveis (Fields/Params) para preservar o isolamento
// append-only: o produtor não partilha slices/mapas com o registo selado.
func mapObligations(obs []referencemonitor.Obligation) []Obligation {
	if len(obs) == 0 {
		return nil
	}
	out := make([]Obligation, len(obs))
	for i, ob := range obs {
		o := Obligation{Type: ob.Type}
		if len(ob.Fields) > 0 {
			o.Fields = make([]string, len(ob.Fields))
			copy(o.Fields, ob.Fields)
		}
		if len(ob.Params) > 0 {
			o.Params = make(map[string]string, len(ob.Params))
			for k, v := range ob.Params {
				o.Params[k] = v
			}
		}
		out[i] = o
	}
	return out
}

// mapChain projecta a cadeia de delegação do RM para o modelo do audit.
func mapChain(chain []referencemonitor.DelegationHop) []DelegationHop {
	if len(chain) == 0 {
		return nil
	}
	out := make([]DelegationHop, len(chain))
	for i, h := range chain {
		out[i] = DelegationHop{Sub: h.Sub, ActAs: h.ActAs}
	}
	return out
}
