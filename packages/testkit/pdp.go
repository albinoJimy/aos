package testkit

import (
	"context"
	"sync"
)

// ===========================================================================
// Policy Decision Point (PDP) — CONTRATO ALINHADO ao _BRIEF §2 + fake
//
// {PDP.Decide(ctx,Input)->(Decision,err), Input, Effect} (control-plane/pdp).
//
// LAYERING: o PDP real (control-plane/pdp) arrasta o motor Cedar (cedar-go), uma
// dependência EXTERNA — importá-lo violaria o "zero-dep / build offline" e o
// go.mod LEVE do testkit. Por isso o testkit ESPELHA aqui os tipos PUROS do
// contrato C1 (input.go é puro no PDP real) e define uma interface alinhada. É um
// MOCK ALINHADO AO CONTRATO, não o PDP real: a forma (campos, método Decide,
// efeitos permit/deny/escalate, fail-closed) é idêntica à da porta, para que um
// teste de política escrito contra [PolicyDecisionPoint] troque trivialmente pelo
// adaptador real quando o fecho de dependências for aceitável.
// ===========================================================================

// PolicyEffect é o veredicto de uma decisão de política (espelha pdp.Effect).
type PolicyEffect string

const (
	// PolicyPermit — a capability é autorizada (impondo as obligations devolvidas).
	PolicyPermit PolicyEffect = "permit"
	// PolicyDeny — negação fail-closed (ausência de permit explícito).
	PolicyDeny PolicyEffect = "deny"
	// PolicyEscalate — requer gate humano (ADR-013).
	PolicyEscalate PolicyEffect = "escalate"
)

// PolicyPrincipal espelha pdp.Principal (subconjunto relevante à decisão).
type PolicyPrincipal struct {
	ID         string   // NHIID
	AgentClass string   // chave da allowlist de capabilities
	Board      string   // chave da soberania por board
	Authority  []string // capabilities delegadas
}

// PolicyResource espelha pdp.Resource.
type PolicyResource struct {
	Type   string
	Value  string
	Region string // soberania de dados
}

// PolicyContext espelha pdp.DecisionContext (subconjunto).
type PolicyContext struct {
	Taint         string
	Reversibility string
	Sensitivity   string
	RiskClass     string
}

// PolicyInput é o pedido de decisão (espelha pdp.Input).
type PolicyInput struct {
	RequestID  string
	Principal  PolicyPrincipal
	Capability string
	Resource   PolicyResource
	Context    PolicyContext
}

// PolicyObligation é uma condição a impor após permit (espelha pdp.Obligation).
type PolicyObligation struct {
	Type   string
	Fields []string
	Params map[string]string
}

// PolicyDecision é o resultado da decisão (espelha pdp.Decision). É SEMPRE
// devolvida (mesmo em negação): fail-closed produz um Deny, nunca ausência.
type PolicyDecision struct {
	Effect        PolicyEffect
	Reason        string
	PolicyVersion string
	Obligations   []PolicyObligation
}

// Permitted indica se a decisão autorizou a acção.
func (d PolicyDecision) Permitted() bool { return d.Effect == PolicyPermit }

// PolicyDecisionPoint é a interface ALINHADA ao contrato C1 do PDP (o método
// canónico [Decide]). O adaptador real (pdp.PolicyCheck sobre pdp.PDP) satisfaz a
// mesma forma; um teste de domínio depende desta interface, não do motor Cedar.
type PolicyDecisionPoint interface {
	Decide(ctx context.Context, in PolicyInput) (PolicyDecision, error)
}

// FakePDP é o PDP de referência DETERMINISTA. Por omissão PERMITE tudo com uma
// versão de política fixa; pode ser programado para negar/escalar globalmente, por
// capability, ou devolver um erro de porta (fail-closed). Regista os inputs
// observados. Consolida o stubDecider antes não-exportado do PDP.
//
// Concorrente-seguro (-race). É uma FUNÇÃO PURA da configuração: a mesma entrada
// produz sempre a mesma decisão — sem flakiness.
type FakePDP struct {
	mu sync.Mutex

	// Default é a decisão devolvida quando nenhuma regra por-capability casa.
	Default PolicyDecision
	// ByCapability sobrepõe a decisão para capabilities específicas.
	ByCapability map[string]PolicyDecision
	// Err, se != nil, é devolvido (com uma Decision Deny fail-closed) — simula uma
	// política indisponível / erro de porta.
	Err error

	seen []PolicyInput
}

// NewFakePDP constrói um PDP que PERMITE tudo (policy_version "1.0.0"). Ajuste os
// campos exportados para cenários de deny/escalate/erro.
func NewFakePDP() *FakePDP {
	return &FakePDP{
		Default:      PolicyDecision{Effect: PolicyPermit, Reason: "permit por omissao (fake)", PolicyVersion: "1.0.0"},
		ByCapability: map[string]PolicyDecision{},
	}
}

// DenyOn programa o fake para NEGAR uma capability específica.
func (p *FakePDP) DenyOn(capability, reason string) *FakePDP {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ByCapability == nil {
		p.ByCapability = map[string]PolicyDecision{}
	}
	p.ByCapability[capability] = PolicyDecision{Effect: PolicyDeny, Reason: reason, PolicyVersion: p.Default.PolicyVersion}
	return p
}

// EscalateOn programa o fake para ESCALAR uma capability específica.
func (p *FakePDP) EscalateOn(capability, reason string) *FakePDP {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ByCapability == nil {
		p.ByCapability = map[string]PolicyDecision{}
	}
	p.ByCapability[capability] = PolicyDecision{Effect: PolicyEscalate, Reason: reason, PolicyVersion: p.Default.PolicyVersion}
	return p
}

// Decide implementa [PolicyDecisionPoint]. Fail-closed: se Err estiver definido,
// devolve-o com uma Decision Deny (nunca ausência de resposta).
func (p *FakePDP) Decide(_ context.Context, in PolicyInput) (PolicyDecision, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, in)
	if p.Err != nil {
		return PolicyDecision{Effect: PolicyDeny, Reason: p.Err.Error(), PolicyVersion: p.Default.PolicyVersion}, p.Err
	}
	if d, ok := p.ByCapability[in.Capability]; ok {
		return d, nil
	}
	return p.Default, nil
}

// Seen devolve uma cópia dos inputs que o fake avaliou (ordem de chegada).
func (p *FakePDP) Seen() []PolicyInput {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PolicyInput, len(p.seen))
	copy(out, p.seen)
	return out
}

// compile-time: o FakePDP satisfaz o contrato alinhado.
var _ PolicyDecisionPoint = (*FakePDP)(nil)
