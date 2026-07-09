package identity

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// ClassPolicy é a configuração de emissão POR CLASSE de agente: o TTL do token e
// o escopo-máximo que a classe concede. A autoridade efectiva embutida no token
// é sempre a intersecção deste escopo com o do utilizador (nunca alarga).
type ClassPolicy struct {
	// TTL é o tempo de vida do token (exp - iat). Curto por desenho: minimiza a
	// janela entre revogação e expiração natural.
	TTL time.Duration
	// Scope é o conjunto-máximo de capabilities que a classe autoriza.
	Scope []string
}

// IssueRequest descreve uma emissão de NHI.
type IssueRequest struct {
	// UserID é o humano responsável (raiz da cadeia de delegação). Obrigatório.
	UserID string
	// AgentID é a identidade única do agente a criar. Obrigatório.
	AgentID string
	// AgentClass selecciona a [ClassPolicy]. Obrigatório e tem de estar
	// configurada, senão a emissão é negada com [ErrUnknownClass].
	AgentClass string
	// PolicyRef é a referência de política a codificar (policy_ref, AOS-004).
	PolicyRef string
	// UserAuthority são as capabilities que o UTILIZADOR possui. A autoridade do
	// token é a intersecção com [ClassPolicy.Scope].
	UserAuthority []string
	// ParentScope, quando não-nil, activa a semântica on-behalf-of: o escopo do
	// filho é ainda intersectado com o do pai, garantindo filho ⊆ pai (a
	// autoridade só pode estreitar ao descer a cadeia, nunca alargar).
	ParentScope []string
}

// Issuer emite tokens NHI assinados. Detém a chave privada ed25519 (mantida FORA
// da árvore do repo — injectada ou gerada em runtime; nunca committada) e a
// configuração por classe. Construir com [NewIssuer].
type Issuer struct {
	iss     string
	priv    ed25519.PrivateKey
	kid     string
	classes map[string]ClassPolicy
	store   appender
	now     func() time.Time
	newJTI  func() (string, error)
}

// IssuerOption configura o Issuer.
type IssuerOption func(*Issuer)

// WithIssuerClock injecta o relógio (uso interno/testes determinísticos).
func WithIssuerClock(f func() time.Time) IssuerOption {
	return func(i *Issuer) {
		if f != nil {
			i.now = f
		}
	}
}

// WithIDSource injecta a fonte de jti (uso interno/testes determinísticos). A
// fonte injectada é considerada infalível (nunca devolve erro); a fonte por
// omissão ([randomJTI]) usa o CSPRNG e falha fail-closed se este falhar.
func WithIDSource(f func() string) IssuerOption {
	return func(i *Issuer) {
		if f != nil {
			i.newJTI = func() (string, error) { return f(), nil }
		}
	}
}

// WithEventStore injecta o Event Store onde a emissão grava identity.nhi.issued.
// Sem store, a emissão funciona mas NÃO é auditada (produção deve injectar um
// store real).
func WithEventStore(store eventstore.EventStore) IssuerOption {
	return func(i *Issuer) {
		if store != nil {
			i.store = store
		}
	}
}

// NewIssuer constrói um emissor. iss identifica o emissor (tem de coincidir com
// o trust anchor do [Verifier]); priv é a chave privada ed25519; classes é a
// política por classe de agente. Uma chave inválida devolve erro.
func NewIssuer(iss string, priv ed25519.PrivateKey, classes map[string]ClassPolicy, opts ...IssuerOption) (*Issuer, error) {
	if iss == "" || len(priv) != ed25519.PrivateKeySize {
		return nil, ErrInvalidRequest
	}
	cp := make(map[string]ClassPolicy, len(classes))
	for k, v := range classes {
		cp[k] = v
	}
	i := &Issuer{
		iss:     iss,
		priv:    priv,
		kid:     iss,
		classes: cp,
		now:     time.Now,
		newJTI:  randomJTI,
	}
	for _, o := range opts {
		o(i)
	}
	return i, nil
}

// PublicKey devolve a chave pública correspondente, para registar como trust
// anchor no verificador (ver [WithTrustedIssuer]).
func (i *Issuer) PublicKey() ed25519.PublicKey {
	return i.priv.Public().(ed25519.PublicKey)
}

// Issuer devolve o identificador do emissor (iss).
func (i *Issuer) IssuerID() string { return i.iss }

// Issue emite um token NHI para o pedido dado. A autoridade embutida é a
// intersecção utilizador ∩ classe (e ⊆ pai em on-behalf-of). Grava um evento
// identity.nhi.issued (só metadados) no Event Store, se configurado.
func (i *Issuer) Issue(ctx context.Context, req IssueRequest) (Token, error) {
	if req.UserID == "" || req.AgentID == "" {
		return Token{}, ErrInvalidRequest
	}
	cp, ok := i.classes[req.AgentClass]
	if !ok {
		return Token{}, ErrUnknownClass
	}

	// Autoridade = utilizador ∩ classe. Nunca alarga: o resultado é subconjunto
	// de AMBOS. Em on-behalf-of, intersecta ainda com o escopo do pai ⇒ filho ⊆ pai.
	scope := intersect(req.UserAuthority, cp.Scope)
	if req.ParentScope != nil {
		scope = intersect(scope, req.ParentScope)
	}

	// O jti tem de ser único e imprevisível: uma falha do CSPRNG é fail-closed
	// (nunca se emite uma NHI sem jti aleatório — ver [randomJTI]).
	jti, err := i.newJTI()
	if err != nil {
		return Token{}, err
	}

	now := i.now()
	claims := Claims{
		UserID:     req.UserID,
		AgentID:    req.AgentID,
		AgentClass: req.AgentClass,
		PolicyRef:  req.PolicyRef,
		Scope:      scope,
		Issuer:     i.iss,
		IssuedAt:   now.Unix(),
		NotBefore:  now.Unix(),
		Expiry:     now.Add(cp.TTL).Unix(),
		JTI:        jti,
	}

	compact, err := signToken(i.priv, i.kid, claims)
	if err != nil {
		return Token{}, err
	}

	if err := i.recordIssued(ctx, claims); err != nil {
		// Emissão não-auditável é uma acção sem rasto: fail-closed (ADR-003/010).
		return Token{}, err
	}

	return Token{Compact: compact, Claims: claims}, nil
}

// recordIssued grava o evento identity.nhi.issued (só metadados). No-op sem store.
func (i *Issuer) recordIssued(ctx context.Context, c Claims) error {
	if i.store == nil {
		return nil
	}
	payload := issuedPayload{
		JTI:        c.JTI,
		UserID:     c.UserID,
		AgentID:    c.AgentID,
		AgentClass: c.AgentClass,
		PolicyRef:  c.PolicyRef,
		Scope:      c.Scope,
		Issuer:     c.Issuer,
		IssuedAt:   c.IssuedAt,
		Expiry:     c.Expiry,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = i.store.Append(ctx, streamIdentity, eventstore.EventInput{
		Type:    EventTypeIssued,
		Payload: raw,
		RunID:   streamIdentity,
		StepID:  "nhi.issued:" + c.JTI, // idempotência por jti
		Producer: eventstore.Producer{
			NHIID:           c.AgentID,
			DelegationChain: []eventstore.DelegationHop{{Sub: c.UserID, ActAs: c.AgentID}},
			Scope:           c.Scope,
		},
	})
	return err
}

// intersect devolve os elementos de a que também estão em b, preservando a ordem
// de a e removendo duplicados. É a operação de estreitamento de autoridade: o
// resultado é sempre subconjunto de AMBOS os operandos (nunca alarga).
func intersect(a, b []string) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	in := make(map[string]struct{}, len(b))
	for _, x := range b {
		in[x] = struct{}{}
	}
	seen := make(map[string]struct{}, len(a))
	var out []string
	for _, x := range a {
		if _, ok := in[x]; !ok {
			continue
		}
		if _, dup := seen[x]; dup {
			continue
		}
		seen[x] = struct{}{}
		out = append(out, x)
	}
	return out
}

// randomJTI gera um identificador de token único (128 bits, base64url). Uma
// falha do CSPRNG é PROPAGADA (nunca engolida): sem entropia não há jti único e
// imprevisível, logo a emissão falha fail-closed. Engolir o erro produziria a
// constante base64url de 16 bytes zero ("AAAAAAAAAAAAAAAAAAAAAA") partilhada por
// todos os tokens — colidindo na chave de idempotência do evento de emissão e
// tornando a revogação por jti demasiado ampla.
func randomJTI() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
