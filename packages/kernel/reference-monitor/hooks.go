package referencemonitor

import "context"

// HookDecision é o veredicto parcial de um hook da cadeia de mediação.
type HookDecision int

const (
	// HookAllow — o hook não se opõe; a cadeia prossegue.
	HookAllow HookDecision = iota
	// HookDeny — o hook nega; a mediação termina em Deny (fail-closed).
	HookDeny
	// HookEscalate — o hook requer gate humano; a mediação termina em Escalate.
	HookEscalate
)

// HookResult é o retorno de um hook. Obligations acumulam-se ao longo da cadeia
// e são impostas apenas se a decisão final for Permit.
type HookResult struct {
	Decision    HookDecision
	Reason      string
	Obligations []Obligation
	// PolicyVersion é a versão (SemVer) da política que produziu este veredicto.
	// Só o hook de política (PDP, AOS-004) a preenche; o RM propaga-a ao evento
	// de mediação para que cada decisão de audit registe a política em vigor
	// (contrato C1, tecnica/12 §4 — campo policy_version). Vazia nos stubs.
	PolicyVersion string
}

// allow é um HookResult neutro reutilizável (sem obrigações).
var allow = HookResult{Decision: HookAllow}

// Hook é um ponto de decisão plugável da cadeia de mediação. A cadeia é
// invocada pela ordem em que é fornecida a [WithHooks]; a ordem canónica de
// mediação — identity → policy → budget → egress → audit — é a que [DefaultHooks]
// devolve (o RM não a reordena). O call é passado por ponteiro para que o hook
// de identidade possa resolver/validar o principal e propagar contexto aos
// hooks seguintes.
//
// Contrato: um Hook NÃO deve entrar em panic; se o fizer, o RM trata-o como
// fail-closed (deny). Um erro devolvido é igualmente fail-closed.
type Hook interface {
	// Name é o identificador estável do hook (usado em DeniedBy e nos spies).
	Name() string
	// Evaluate avalia o call e devolve o veredicto parcial.
	Evaluate(ctx context.Context, call *Call) (HookResult, error)
}

// ---------------------------------------------------------------------------
// STUBS NEUTROS (AOS-003)
//
// As implementações reais chegam noutros tickets; aqui os hooks são neutros e
// documentam o ponto de injecção. Nenhum stub codifica regras de negócio.
// ---------------------------------------------------------------------------

// IdentityStub resolve/valida o principal. Stub NEUTRO: aceita o principal do
// call como está, sem o alterar. A implementação real (AOS-005) resolve a NHI e
// valida a cadeia de delegação (ADR-003).
type IdentityStub struct{}

func (IdentityStub) Name() string { return "identity" }

func (IdentityStub) Evaluate(_ context.Context, _ *Call) (HookResult, error) {
	// Neutro: não muta o principal, não nega. Ponto de injecção AOS-005.
	return allow, nil
}

// PolicyStub é o par de decisão de política (PDP). Stub PLACEHOLDER: devolve
// permit sem obrigações. Substituído pelo PDP real (AOS-004) com policy-as-code
// versionada e assinada, avaliada em memória (contrato C1, ADR-011).
type PolicyStub struct{}

func (PolicyStub) Name() string { return "policy" }

func (PolicyStub) Evaluate(_ context.Context, _ *Call) (HookResult, error) {
	// PLACEHOLDER default-allow documentado. A política real é default-deny
	// (tecnica/01 §6); este stub existe só para o RM ser exercitável em AOS-003.
	return allow, nil
}

// BudgetStub reserva orçamento no admission control global. Stub NEUTRO:
// permite sempre. A implementação real (AOS-008) consulta o token-bucket
// distribuído sobre o TPM/RPM real (ADR-008).
type BudgetStub struct{}

func (BudgetStub) Name() string { return "budget" }

func (BudgetStub) Evaluate(_ context.Context, _ *Call) (HookResult, error) {
	return allow, nil
}

// EgressStub aplica a allowlist de egress default-deny. Stub NEUTRO: permite
// sempre (conceptual). A implementação real corta a exfiltração via tools
// "benignas" na fronteira do RM (tecnica/01 §8.1).
type EgressStub struct{}

func (EgressStub) Name() string { return "egress" }

func (EgressStub) Evaluate(_ context.Context, _ *Call) (HookResult, error) {
	return allow, nil
}

// AuditStub é o gancho para o audit tamper-evident futuro (AOS-011,
// hash-chain + WORM, ADR-010). Stub NO-OP: não bloqueia. O registo DURÁVEL da
// mediação no Event Store é feito pelo RM via [EventSink] (ver eventsink.go),
// independentemente deste hook.
type AuditStub struct{}

func (AuditStub) Name() string { return "audit" }

func (AuditStub) Evaluate(_ context.Context, _ *Call) (HookResult, error) {
	return allow, nil
}

// DefaultHooks devolve a cadeia de stubs neutros na ordem canónica de mediação:
// identity → policy → budget → egress → audit.
func DefaultHooks() []Hook {
	return []Hook{
		IdentityStub{},
		PolicyStub{},
		BudgetStub{},
		EgressStub{},
		AuditStub{},
	}
}
