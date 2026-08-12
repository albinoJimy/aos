package identity

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/aos-ref/platform/identity/delegation"
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
	// DelegationChain é a cadeia on-behalf-of verificada (raiz humana → agente
	// actual), extraída dos claims e validada por [Verifier.Verify] (AOS-006).
	DelegationChain delegation.Chain
}

// HumanPrincipal resolve o humano responsável único na raiz da cadeia de
// delegação (reconstrução de autoria: "quem autorizou"). Falha fail-closed se a
// cadeia for vazia ou órfã.
func (p Principal) HumanPrincipal() (string, error) {
	return p.DelegationChain.HumanPrincipal()
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
	// leeway é a tolerância de relógio aplicada a nbf/exp (AOS-278). Espelha o
	// verificador OIDC ([integration/oidc], 60s por omissão): o issuer e o nó são
	// máquinas SEPARADAS (D4 Opção A — mint externo, verificação no nó), pelo que um
	// token acabado de mintar cujo nbf caia do lado de lá de uma fronteira de segundo
	// (nbf é segundos Unix truncados) NÃO pode ser rejeitado como "ainda não válido"
	// por jitter/skew sub-segundo. Sem esta folga, um mint-then-verify quase-imediato
	// entre relógios não-sincronizados ao milissegundo dá ErrTokenNotYetValid
	// esporádico. É prática JWT padrão; a folga é pequena e simétrica (nbf e exp).
	leeway time.Duration
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

// WithVerifierLeeway define a tolerância de relógio aplicada a nbf/exp (AOS-278),
// no molde de [oidc.Config.Leeway]. Valor negativo é ignorado (mantém o default);
// 0 explícito desliga a folga (verificação estrita — para testes determinísticos
// que injectam o relógio). Sem a opção, [NewVerifier] usa 60s.
func WithVerifierLeeway(d time.Duration) VerifierOption {
	return func(v *Verifier) {
		if d >= 0 {
			v.leeway = d
		}
	}
}

// NewVerifier constrói um verificador. Registe pelo menos um trust anchor com
// [WithTrustedIssuer], senão TODOS os tokens são rejeitados (fail-closed). A folga
// de relógio (nbf/exp) é 60s por omissão, alinhada com o verificador OIDC do repo;
// ajuste com [WithVerifierLeeway].
func NewVerifier(opts ...VerifierOption) *Verifier {
	v := &Verifier{
		trust:  make(map[string]ed25519.PublicKey),
		now:    time.Now,
		leeway: 60 * time.Second,
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

	// 4) Janela temporal (relógio injectável) COM folga de relógio (AOS-278), no molde
	// do verificador OIDC: nbf−leeway .. exp+leeway. exp ausente ⇒ expirado (fail-closed).
	now := v.now()
	if pt.claims.NotBefore != 0 && now.Before(time.Unix(pt.claims.NotBefore, 0).Add(-v.leeway)) {
		return Principal{}, ErrTokenNotYetValid
	}
	if pt.claims.Expiry == 0 || now.After(time.Unix(pt.claims.Expiry, 0).Add(v.leeway)) {
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

	// 6) Cadeia de delegação (AOS-006). Corre por último: só se valida a autoria
	// de um token cuja assinatura, janela temporal e revogação já passaram. A
	// cadeia tem de (a) resolver até um humano responsável na raiz (0 órfãs), (b)
	// não escalar autoridade ao descer, (c) manter o encadeamento de hash intacto,
	// (d) terminar no agente deste token (leaf.ActAs == agent_id), (e) enraizar
	// exactamente no humano do claim (root.Sub == human:<user_id>) e (f) selar uma
	// autoridade na folha que cubra o escopo efectivo (claims.Scope ⊆
	// leaf.Authority). Qualquer falha ⇒ ErrDelegationInvalid (fail-closed): o RM
	// nega e audita.
	c := pt.claims
	if err := c.DelegationChain.Verify(); err != nil {
		return Principal{}, fmt.Errorf("%w: %w", ErrDelegationInvalid, err)
	}
	// (e) Atribuição: a raiz da cadeia SELADA tem de ser exactamente o humano do
	// claim. Sem isto, claims.UserID e a raiz da cadeia seriam duas fontes
	// divergentes de "quem autorizou" (um emissor comprometido/buggy podia gravar
	// UserID=alice num token cuja cadeia enraíza em bob) e o registo de auditoria
	// mentiria. Liga a atribuição do token à autoria selada (AOS-006). Fail-closed.
	root, _ := c.DelegationChain.Root()
	if root.Sub != humanRoot(c.UserID) {
		return Principal{}, fmt.Errorf("%w: raiz da cadeia (%q) nao corresponde ao user_id (%q)", ErrDelegationInvalid, root.Sub, c.UserID)
	}
	leaf, _ := c.DelegationChain.Leaf()
	if leaf.ActAs != c.AgentID {
		return Principal{}, fmt.Errorf("%w: folha da cadeia (%q) nao corresponde ao agent_id (%q)", ErrDelegationInvalid, leaf.ActAs, c.AgentID)
	}
	// (f) Defesa-em-profundidade: o escopo efectivo (claims.Scope, o que o RM/PDP
	// realmente aplicam) tem de ser subconjunto da autoridade SELADA na folha da
	// cadeia. Sem esta reconciliação a autoridade da cadeia não vincularia o
	// enforcement (um token podia selar folha.Authority=[cap:http.get] e ainda
	// assim conceder claims.Scope=[cap:http.get,cap:admin]). Fail-closed.
	if !authoritySubset(c.Scope, leaf.Authority) {
		return Principal{}, fmt.Errorf("%w: escopo do token excede a autoridade selada na folha da cadeia", ErrDelegationInvalid)
	}

	return Principal{
		UserID:          c.UserID,
		AgentID:         c.AgentID,
		AgentClass:      c.AgentClass,
		PolicyRef:       c.PolicyRef,
		Issuer:          c.Issuer,
		JTI:             c.JTI,
		Scope:           append([]string(nil), c.Scope...),
		IssuedAt:        time.Unix(c.IssuedAt, 0),
		NotBefore:       time.Unix(c.NotBefore, 0),
		Expiry:          time.Unix(c.Expiry, 0),
		DelegationChain: c.DelegationChain.Clone(),
	}, nil
}
