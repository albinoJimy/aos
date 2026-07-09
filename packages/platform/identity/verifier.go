package identity

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"time"
)

// RevocationChecker é a superfície mínima de que o [Verifier] precisa para
// consultar a revogação. [Revocations] implementa-a; testes podem usar um duplo.
type RevocationChecker interface {
	// IsRevoked indica se o jti está revogado. Um erro é tratado fail-closed pelo
	// verificador (revogação indisponível ⇒ token rejeitado).
	IsRevoked(ctx context.Context, jti string) (bool, error)
}

// Principal é a identidade não-humana resolvida por [Verifier.Verify] a partir
// de um token válido. É o que o hook [IdentityCheck] traduz para o
// reference-monitor.Principal consumido pelo PDP (AOS-004).
type Principal struct {
	UserID     string
	AgentID    string
	AgentClass string
	PolicyRef  string
	Issuer     string
	JTI        string
	Scope      []string
	IssuedAt   time.Time
	NotBefore  time.Time
	Expiry     time.Time
}

// Allows indica se a capability pedida está no escopo da NHI. É a fronteira de
// escopo do token (o PDP aplica ainda a sua política por cima).
func (p Principal) Allows(capability string) bool {
	for _, s := range p.Scope {
		if s == capability {
			return true
		}
	}
	return false
}

// Verifier valida tokens NHI contra um conjunto de trust anchors (chaves
// públicas por emissor) e resolve o [Principal]. Construir com [NewVerifier].
type Verifier struct {
	trust       map[string]ed25519.PublicKey
	revocations RevocationChecker
	now         func() time.Time
}

// VerifierOption configura o Verifier.
type VerifierOption func(*Verifier)

// WithTrustedIssuer regista um trust anchor: a chave pública com que o emissor
// iss assina. Um token cujo iss não tenha anchor é rejeitado com
// [ErrUnknownIssuer].
func WithTrustedIssuer(iss string, pub ed25519.PublicKey) VerifierOption {
	return func(v *Verifier) {
		if iss != "" && len(pub) == ed25519.PublicKeySize {
			v.trust[iss] = pub
		}
	}
}

// WithRevocations liga o verificador ao registo de revogação. Sem ele, nenhum
// token é considerado revogado (adequado a testes; produção deve ligá-lo).
func WithRevocations(rc RevocationChecker) VerifierOption {
	return func(v *Verifier) { v.revocations = rc }
}

// WithVerifierClock injecta o relógio (uso interno/testes determinísticos).
func WithVerifierClock(f func() time.Time) VerifierOption {
	return func(v *Verifier) {
		if f != nil {
			v.now = f
		}
	}
}

// NewVerifier constrói um verificador. Registe pelo menos um trust anchor com
// [WithTrustedIssuer], senão TODOS os tokens são rejeitados (fail-closed).
func NewVerifier(opts ...VerifierOption) *Verifier {
	v := &Verifier{
		trust: make(map[string]ed25519.PublicKey),
		now:   time.Now,
	}
	for _, o := range opts {
		o(v)
	}
	return v
}

// Verify valida um token compacto e resolve o [Principal]. Rejeita fail-closed
// (com sentinela comparável por errors.Is): malformado, alg/none, emissor
// desconhecido, assinatura inválida, ainda-não-válido, expirado e revogado. A
// ordem coloca a verificação criptográfica ANTES das temporais/revogação: nunca
// se confia em claims de um token cuja assinatura não foi comprovada.
func (v *Verifier) Verify(ctx context.Context, compact string) (Principal, error) {
	pt, err := parseCompact(compact)
	if err != nil {
		return Principal{}, err
	}

	// 1) Algoritmo: só EdDSA. Rejeita alg/none confusion antes de olhar a chave.
	if pt.header.Alg != algEdDSA {
		return Principal{}, fmt.Errorf("%w: alg=%q", ErrUnsupportedAlg, pt.header.Alg)
	}

	// 2) Emissor conhecido (trust anchor). Necessário para obter a chave pública.
	if pt.claims.Issuer == "" {
		return Principal{}, ErrUnknownIssuer
	}
	pub, ok := v.trust[pt.claims.Issuer]
	if !ok {
		return Principal{}, fmt.Errorf("%w: iss=%q", ErrUnknownIssuer, pt.claims.Issuer)
	}

	// 3) Assinatura: prova criptográfica de origem + integridade.
	if !ed25519.Verify(pub, []byte(pt.signingInput), pt.signature) {
		return Principal{}, ErrSignatureInvalid
	}

	// 3b) Defesa-em-profundidade (SÓ sobre claims/header já autenticados pela
	// assinatura): impõe as invariantes que o Issuer honesto garante, para o caso
	// de um emissor confiável comprometido/buggy ou de confusão de tokens
	// cross-protocol. typ tem de marcar uma NHI; user_id/agent_id não podem ser
	// vazios (uma identidade degenerada nunca é aceite). Fail-closed.
	if pt.header.Typ != typNHI {
		return Principal{}, fmt.Errorf("%w: typ=%q", ErrTokenMalformed, pt.header.Typ)
	}
	if pt.claims.UserID == "" || pt.claims.AgentID == "" {
		return Principal{}, fmt.Errorf("%w: user_id/agent_id vazios", ErrTokenMalformed)
	}

	// 4) Janela temporal (relógio injectável). exp ausente ⇒ expirado (fail-closed).
	now := v.now()
	if pt.claims.NotBefore != 0 && now.Before(time.Unix(pt.claims.NotBefore, 0)) {
		return Principal{}, ErrTokenNotYetValid
	}
	if pt.claims.Expiry == 0 || !now.Before(time.Unix(pt.claims.Expiry, 0)) {
		return Principal{}, ErrTokenExpired
	}

	// 5) Revogação. Erro de consulta ⇒ fail-closed (rejeita).
	if v.revocations != nil {
		revoked, rerr := v.revocations.IsRevoked(ctx, pt.claims.JTI)
		if rerr != nil {
			return Principal{}, fmt.Errorf("%w: %v", ErrTokenRevoked, rerr)
		}
		if revoked {
			return Principal{}, ErrTokenRevoked
		}
	}

	c := pt.claims
	return Principal{
		UserID:     c.UserID,
		AgentID:    c.AgentID,
		AgentClass: c.AgentClass,
		PolicyRef:  c.PolicyRef,
		Issuer:     c.Issuer,
		JTI:        c.JTI,
		Scope:      append([]string(nil), c.Scope...),
		IssuedAt:   time.Unix(c.IssuedAt, 0),
		NotBefore:  time.Unix(c.NotBefore, 0),
		Expiry:     time.Unix(c.Expiry, 0),
	}, nil
}
