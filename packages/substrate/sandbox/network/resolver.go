package network

import (
	"context"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// EgressPolicyResolver é a PORTA que resolve a allowlist de egress POR PRINCIPAL. É
// o ponto de extensão onde o PDP real (cedar, packages/control-plane/pdp) se liga
// por trás: dado um principal, devolve a [Policy] que o rege. A impl de referência
// ([EmbeddedResolver]) usa a policy-as-code embutida (coerente com AOS-057/058/066,
// data-plane zero-dep), mas um deployment pode substituir por um resolver que
// consulta o PDP.
//
// FAIL-CLOSED: um resolver que devolva (nil, nil) — sem allowlist para o principal —
// leva o [EgressFilter] a NEGAR todo o egress desse principal. Um erro de resolução
// é igualmente deny (nunca bypass). Devolver uma [Policy] é a ÚNICA forma de um
// egress poder ser permitido.
type EgressPolicyResolver interface {
	// Resolve devolve a allowlist do principal, ou nil se não houver política
	// resolúvel (⇒ default-deny total para o principal). Um erro é fail-closed.
	Resolve(ctx context.Context, principal referencemonitor.Principal) (*Policy, error)
}

// ResolverFunc adapta uma função a [EgressPolicyResolver].
type ResolverFunc func(ctx context.Context, principal referencemonitor.Principal) (*Policy, error)

// Resolve implementa [EgressPolicyResolver].
func (f ResolverFunc) Resolve(ctx context.Context, principal referencemonitor.Principal) (*Policy, error) {
	return f(ctx, principal)
}

// EmbeddedResolver é a impl de REFERÊNCIA: resolve todos os principais contra a
// mesma allowlist policy-as-code embutida (egress_policy.json). O escopo POR
// PRINCIPAL é imposto por [Policy.Evaluate] (as regras casam por nhi:/class:), não
// por seleccionar policies diferentes — a allowlist de A não permite B porque
// nenhuma regra de B casa o principal A. O PDP real ligar-se-ia por trás desta mesma
// porta devolvendo policies específicas por principal.
type EmbeddedResolver struct {
	policy *Policy
}

// NewEmbeddedResolver carrega a allowlist embutida (fail-closed no carregamento) e
// constrói o resolver de referência.
func NewEmbeddedResolver() (*EmbeddedResolver, error) {
	p, err := Load()
	if err != nil {
		return nil, err
	}
	return &EmbeddedResolver{policy: p}, nil
}

// Resolve devolve a allowlist embutida para qualquer principal (o escopo é aplicado
// por [Policy.Evaluate]).
func (r *EmbeddedResolver) Resolve(_ context.Context, _ referencemonitor.Principal) (*Policy, error) {
	return r.policy, nil
}

// Policy expõe a allowlist embutida (introspecção/changelog: selar a versão em
// vigor no arranque).
func (r *EmbeddedResolver) Policy() *Policy { return r.policy }
