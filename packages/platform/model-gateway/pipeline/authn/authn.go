// Package authn é o estágio AUTH-PRINCIPAL REAL do Model Gateway (AOS-057,
// tecnica/06 §4, ADR-011/ADR-003/ADR-002). Substitui o pass-through de AOS-055:
// valida, em CADA chamada, o token OAuth scoped/time-bound do par (utilizador,
// agente) e resolve a autoridade efectiva, fail-closed.
//
// # O que faz (fail-closed em cada passo)
//
//  1. VALIDA o token do principal reutilizando o Verifier da identidade
//     (platform/identity, AOS-005/006) — NÃO reimplementa o formato: assinatura
//     EdDSA, janela temporal, revogação e cadeia on-behalf-of. Token
//     ausente/inválido/expirado ⇒ DENY.
//  2. Exige que a cadeia de delegação termine num HUMANO responsável (ADR-003):
//     "quem autorizou" tem sempre resposta. Cadeia órfã ⇒ DENY.
//  3. Computa a AUTORIDADE EFECTIVA = utilizador ∩ classe de agente (menor
//     privilégio) e reconcilia-a com o escopo SELADO no token (defesa em
//     profundidade). Autoridade vazia ⇒ DENY.
//  4. Aplica a POLÍTICA DE VALIDAÇÃO DO TOKEN (policy-as-code versionada,
//     default-deny): sem regra aplicável ⇒ DENY.
//
// Em sucesso, popula os campos de IDENTIDADE do [pipeline.Exchange]
// (principal/classe/humano/autoridade/cadeia) — o eixo da ATRIBUIÇÃO, SEPARADO do
// eixo da chave de infra (routing/keypool). Não toca em segredos.
package authn

import (
	"context"
	"errors"
	"fmt"

	"github.com/aos-ref/platform/identity"
	"github.com/aos-ref/platform/model-gateway/pipeline"
)

// Erros fail-closed do estágio (comparáveis por errors.Is; o pipeline envolve-os
// em StageError, tornando a recusa atribuível ao estágio auth-principal).
var (
	// ErrNoToken — a chamada não trouxe token do principal. Default-deny.
	ErrNoToken = errors.New("authn: token do principal ausente (fail-closed)")
	// ErrTokenRejected — o Verifier de identidade rejeitou o token (assinatura,
	// janela temporal, revogação ou cadeia inválida). Default-deny.
	ErrTokenRejected = errors.New("authn: token do principal rejeitado (fail-closed)")
	// ErrNoHumanRoot — a cadeia de delegação não termina num humano responsável.
	ErrNoHumanRoot = errors.New("authn: cadeia on-behalf-of nao enraiza num humano (fail-closed)")
	// ErrAuthorityEmpty — a autoridade efectiva (utilizador ∩ classe) é vazia.
	ErrAuthorityEmpty = errors.New("authn: autoridade efectiva vazia (utilizador ∩ classe)")
	// ErrScopeExceedsSeal — a autoridade efectiva excede o escopo selado no token
	// (o token concede menos do que utilizador ∩ classe sugeririam; usa-se o menor).
	ErrScopeExceedsSeal = errors.New("authn: autoridade efectiva excede o escopo selado no token")
	// ErrPolicyDenied — a policy-as-code de validação de token negou (default-deny).
	ErrPolicyDenied = errors.New("authn: politica de validacao de token negou (default-deny)")
	// ErrResolver — falha a resolver as capabilities de utilizador/classe. Deny.
	ErrResolver = errors.New("authn: falha a resolver autoridade de utilizador/classe")
)

// Verifier é a superfície MÍNIMA que o estágio precisa do verificador de
// identidade (AOS-005/006). *identity.Verifier satisfá-la; testes podem usar um
// duplo. O tipo de retorno é o [identity.Principal] canónico — REUTILIZA-SE a
// identidade, não se reinventa.
type Verifier interface {
	Verify(ctx context.Context, compact string) (identity.Principal, error)
}

// Stage é o estágio auth-principal real. Implementa [pipeline.Stage]. Construir
// com [New].
type Stage struct {
	verifier    Verifier
	resolver    AuthorityResolver
	policy      *Policy
	requiredCap string
}

// Option configura o [Stage].
type Option func(*Stage)

// WithRequiredCapability define a capability que o principal tem de possuir para
// invocar um modelo (default "model:invoke"). É reconciliada com a autoridade
// efectiva e com o escopo selado no token.
func WithRequiredCapability(cap string) Option {
	return func(s *Stage) {
		if cap != "" {
			s.requiredCap = cap
		}
	}
}

// New constrói o estágio sobre o verificador de identidade, o resolver de
// autoridade (utilizador/classe) e a política de validação de token. Qualquer um
// nil torna o estágio fail-closed (recusa toda a chamada) — nunca fail-open.
func New(v Verifier, r AuthorityResolver, p *Policy, opts ...Option) *Stage {
	s := &Stage{verifier: v, resolver: r, policy: p, requiredCap: "model:invoke"}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Name implementa [pipeline.Stage]: mantém o nome canónico do estágio ("auth-principal").
func (s *Stage) Name() string { return "auth-principal" }

// Process implementa [pipeline.Stage]. Valida o token, resolve a autoridade
// efectiva e aplica a política — tudo fail-closed. Em sucesso popula os campos de
// identidade do Exchange (o eixo da atribuição). NUNCA regista segredos.
func (s *Stage) Process(ctx context.Context, ex *pipeline.Exchange) error {
	if s.verifier == nil || s.resolver == nil || s.policy == nil {
		return fmt.Errorf("%w: estagio auth-principal nao configurado", ErrTokenRejected)
	}

	// (1) Token presente. ex.Principal transporta o token scoped/time-bound (o
	// contrato de porta documenta-o como opaco até AOS-057).
	token := ex.Principal
	if token == "" {
		ex.Record(recordName, "deny", "token do principal ausente")
		return ErrNoToken
	}

	// (2) Validação do token: REUTILIZA o Verifier da identidade (fail-closed).
	principal, err := s.verifier.Verify(ctx, token)
	if err != nil {
		ex.Record(recordName, "deny", "token rejeitado pelo verificador de identidade")
		// Duplo %w: a recusa é atribuível ao estágio (ErrTokenRejected) E a causa
		// concreta do verificador (ex.: identity.ErrTokenExpired) permanece
		// inspeccionável por errors.Is.
		return fmt.Errorf("%w: %w", ErrTokenRejected, err)
	}

	// (3) Cadeia on-behalf-of tem de enraizar num humano responsável (ADR-003).
	human, err := principal.HumanPrincipal()
	if err != nil {
		ex.Record(recordName, "deny", "cadeia de delegacao sem humano responsavel")
		return fmt.Errorf("%w: %v", ErrNoHumanRoot, err)
	}

	// (4) Autoridade efectiva = utilizador ∩ classe (menor privilégio).
	userCaps, err := s.resolver.UserAuthority(ctx, principal.UserID)
	if err != nil {
		ex.Record(recordName, "deny", "falha a resolver autoridade do utilizador")
		return fmt.Errorf("%w: %v", ErrResolver, err)
	}
	classCaps, err := s.resolver.ClassAuthority(ctx, principal.AgentClass)
	if err != nil {
		ex.Record(recordName, "deny", "falha a resolver autoridade da classe")
		return fmt.Errorf("%w: %v", ErrResolver, err)
	}
	effective := EffectiveAuthority(userCaps, classCaps)
	if len(effective) == 0 {
		ex.Record(recordName, "deny", "autoridade efectiva vazia")
		return ErrAuthorityEmpty
	}

	// (4b) Defesa em profundidade: a autoridade efectiva computada tem de estar
	// dentro do escopo SELADO no token (o Issuer honesto já intersectou
	// utilizador ∩ classe; um resolver dessincronizado nunca pode CONCEDER mais do
	// que o token selou). Reconcilia os dois — usa o menor privilégio.
	if !subset(effective, principal.Scope) {
		effective = EffectiveAuthority(effective, principal.Scope)
		if len(effective) == 0 {
			ex.Record(recordName, "deny", "autoridade efectiva fora do escopo selado")
			return ErrScopeExceedsSeal
		}
	}

	// (4c) A capability OBRIGATÓRIA de invocação de modelo tem de estar na
	// autoridade efectiva (gate estrutural, defesa em profundidade antes da
	// política): o principal só invoca um modelo se DETIVER model:invoke.
	if !contains(effective, s.requiredCap) {
		ex.Record(recordName, "deny", "autoridade efectiva nao inclui a capability obrigatoria")
		return fmt.Errorf("%w: falta a capability %q", ErrPolicyDenied, s.requiredCap)
	}

	// (5) Política de validação de token (policy-as-code versionada, default-deny).
	effect := s.policy.Evaluate(PolicyInput{
		Operation:  string(ex.Op),
		AgentClass: principal.AgentClass,
		Authority:  effective,
	})
	if effect != EffectAllow {
		ex.Record(recordName, "deny", "policy-as-code negou (default-deny): "+s.policy.Version())
		return fmt.Errorf("%w: politica %s", ErrPolicyDenied, s.policy.Version())
	}

	// Sucesso: popula o eixo da IDENTIDADE (atribuição). ex.Principal passa a ser o
	// identificador NÃO-SECRETO do principal (user/agent) — o token bruto não segue
	// para o rasto de variância/atribuição.
	ex.PrincipalUser = principal.UserID
	ex.PrincipalAgent = principal.AgentID
	ex.AgentClass = principal.AgentClass
	ex.HumanRoot = human
	ex.EffectiveAuthority = effective
	ex.DelegationChain = projectChain(principal)
	ex.PolicyVersion = s.policy.Version()
	ex.Principal = principal.UserID + "/" + principal.AgentID
	ex.Record(recordName, "allow", "principal validado; autoridade utilizador ∩ classe; politica "+s.policy.Version())
	return nil
}

// recordName é o nome canónico do estágio no rasto de decisões.
const recordName = "auth-principal"

// projectChain projecta a cadeia de delegação verificada do principal para a
// forma primitiva (sub/act_as) que o Exchange transporta para a atribuição/WORM.
func projectChain(p identity.Principal) []pipeline.DelegationHop {
	if len(p.DelegationChain) == 0 {
		return nil
	}
	out := make([]pipeline.DelegationHop, len(p.DelegationChain))
	for i, l := range p.DelegationChain {
		out[i] = pipeline.DelegationHop{Sub: l.Sub, ActAs: l.ActAs}
	}
	return out
}
