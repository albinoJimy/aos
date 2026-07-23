package identity

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/aos-ref/platform/identity/delegation"
)

// ChildRequest descreve a emissão on-behalf-of de um sub-agente (token filho). O
// pai é apresentado como token compacto ([Issuer.IssueChild]); o filho herda a
// cadeia de delegação do pai mais um novo elo.
type ChildRequest struct {
	// AgentID é a identidade única do sub-agente a criar. Obrigatório.
	AgentID string
	// AgentClass selecciona a [ClassPolicy] do filho (TTL/escopo-máximo da classe).
	// Obrigatório e configurada, senão [ErrUnknownClass].
	AgentClass string
	// PolicyRef é a referência de política do filho (policy_ref, AOS-004).
	PolicyRef string
	// Authority são as capabilities PEDIDAS para o filho. Têm de ser subconjunto
	// da autoridade da folha do pai (senão escalada ⇒ [ErrDelegationInvalid]). A
	// autoridade efectiva é ainda intersectada com o escopo-máximo da classe do
	// filho (a classe pode estreitar, nunca alargar).
	Authority []string
}

// IssueChild emite um token NHI filho on-behalf-of um token pai, estendendo a
// cadeia de delegação. Passos (fail-closed em qualquer um):
//
//  1. verifica o token pai (assinatura do emissor, alg, janela temporal) e
//     extrai a sua cadeia de delegação;
//  2. exige que a cadeia do pai resolva até um humano responsável (senão a NHI
//     filha herdaria uma cadeia órfã) — [ErrDelegationInvalid];
//  3. REJEITA se req.Authority pede autoridade fora da folha do pai (escalada);
//  4. autoridade do filho = req.Authority ∩ escopo-da-classe-do-filho (⊆ pai);
//  5. estende a cadeia com o novo elo (Sub = agente-pai, ActAs = filho,
//     Depth = pai+1, PrevHash = hash(folha do pai)) e emite o token filho
//     assinado, com a cadeia estendida SELADA na assinatura.
//
// O humano responsável (raiz) é preservado: a autoria de qualquer efeito do
// filho reconstrói-se até ao mesmo human_principal do pai.
func (i *Issuer) IssueChild(ctx context.Context, parentCompact string, req ChildRequest) (Token, error) {
	if req.AgentID == "" {
		return Token{}, ErrInvalidRequest
	}
	cp, ok := i.classes[req.AgentClass]
	if !ok {
		return Token{}, ErrUnknownClass
	}

	// 1) Verifica o token pai contra a chave do PRÓPRIO emissor (cadeia
	// auto-emitida) e valida a sua janela temporal.
	parent, err := i.verifyParent(parentCompact)
	if err != nil {
		return Token{}, err
	}

	// 2) A cadeia do pai tem de resolver até um humano (não pode ser órfã) e
	// enraizar exactamente no humano do pai (root.Sub == human:<parent.UserID>),
	// espelhando a invariante de atribuição do Verifier: o filho não pode herdar
	// uma cadeia cuja raiz divirja do UserID selado no pai.
	if verr := parent.DelegationChain.Verify(); verr != nil {
		return Token{}, fmt.Errorf("%w: pai: %w", ErrDelegationInvalid, verr)
	}
	if root, _ := parent.DelegationChain.Root(); root.Sub != humanRoot(parent.UserID) {
		return Token{}, fmt.Errorf("%w: raiz da cadeia do pai (%q) nao corresponde ao user_id (%q)", ErrDelegationInvalid, root.Sub, parent.UserID)
	}
	leaf, _ := parent.DelegationChain.Leaf()

	// 3) Não-escalada: cada capability pedida tem de existir na folha do pai.
	if !authoritySubset(req.Authority, leaf.Authority) {
		return Token{}, fmt.Errorf("%w: %w", ErrDelegationInvalid, delegation.ErrScopeEscalation)
	}

	// 4) Autoridade efectiva do filho: pedido ∩ escopo-máximo da classe do filho
	// (⊆ pai, garantido por (3)). A classe pode estreitar; nunca alarga.
	childScope := intersect(req.Authority, cp.Scope)

	// 5) Estende a cadeia (Extend re-verifica ⊆ folha, defesa-em-profundidade).
	childChain, err := parent.DelegationChain.Extend(req.AgentID, childScope)
	if err != nil {
		return Token{}, fmt.Errorf("%w: %w", ErrDelegationInvalid, err)
	}

	jti, err := i.newJTI()
	if err != nil {
		return Token{}, err
	}

	now := i.now()
	claims := Claims{
		UserID:          parent.UserID, // mesmo humano responsável
		AgentID:         req.AgentID,
		AgentClass:      req.AgentClass,
		PolicyRef:       req.PolicyRef,
		Scope:           childScope,
		Issuer:          i.iss,
		IssuedAt:        now.Unix(),
		NotBefore:       now.Unix(),
		Expiry:          now.Add(cp.TTL).Unix(),
		JTI:             jti,
		DelegationChain: childChain,
	}

	compact, err := signToken(i.signer, i.kid, claims)
	if err != nil {
		return Token{}, err
	}
	if err := i.recordIssued(ctx, claims); err != nil {
		return Token{}, err
	}
	return Token{Compact: compact, Claims: claims}, nil
}

// verifyParent descodifica e valida criptograficamente um token pai auto-emitido
// (mesmo emissor): alg EdDSA, assinatura contra a chave pública do emissor, iss
// coincidente, typ NHI e janela temporal. Não consulta revogação (o pai é um
// token efémero apresentado na emissão do filho; a revogação é aplicada na
// mediação de cada tool call pelo Verifier). Devolve os claims autenticados.
func (i *Issuer) verifyParent(compact string) (Claims, error) {
	pt, err := parseCompact(compact)
	if err != nil {
		return Claims{}, err
	}
	if pt.header.Alg != algEdDSA {
		return Claims{}, fmt.Errorf("%w: alg=%q", ErrUnsupportedAlg, pt.header.Alg)
	}
	if pt.claims.Issuer != i.iss {
		return Claims{}, fmt.Errorf("%w: iss=%q", ErrUnknownIssuer, pt.claims.Issuer)
	}
	pub := i.pub
	if !ed25519.Verify(pub, []byte(pt.signingInput), pt.signature) {
		return Claims{}, ErrSignatureInvalid
	}
	if pt.header.Typ != typNHI {
		return Claims{}, fmt.Errorf("%w: typ=%q", ErrTokenMalformed, pt.header.Typ)
	}
	if pt.claims.UserID == "" || pt.claims.AgentID == "" {
		return Claims{}, fmt.Errorf("%w: user_id/agent_id vazios", ErrTokenMalformed)
	}
	now := i.now()
	if pt.claims.NotBefore != 0 && now.Before(time.Unix(pt.claims.NotBefore, 0)) {
		return Claims{}, ErrTokenNotYetValid
	}
	if pt.claims.Expiry == 0 || !now.Before(time.Unix(pt.claims.Expiry, 0)) {
		return Claims{}, ErrTokenExpired
	}
	return pt.claims, nil
}

// authoritySubset indica se todos os elementos de a estão em b (a ⊆ b). Espelha
// a semântica de delegation.subset ao nível do pacote identity (o conjunto vazio
// é subconjunto de qualquer conjunto).
func authoritySubset(a, b []string) bool {
	if len(a) == 0 {
		return true
	}
	in := make(map[string]struct{}, len(b))
	for _, x := range b {
		in[x] = struct{}{}
	}
	for _, x := range a {
		if _, ok := in[x]; !ok {
			return false
		}
	}
	return true
}
