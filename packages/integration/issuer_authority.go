package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"

	identity "github.com/aos-ref/platform/identity"
)

// Este ficheiro é a ESPINHA DE TOKEN self-hosted Nível 2 (AOS-156). A ideia central:
// o issuer é uma AUTORIDADE DE IDENTIDADE SEPARADA que DETÉM a chave de assinatura; o
// nó/runtime só recebe a PUBKEY (trust anchor). Assim um nó comprometido NÃO forja
// identidades — a não-forjabilidade é RELATIVA ao nó (o que faltava vs o demo
// self-minted em que o próprio nó gerava a chave e podia, por isso, mintar).
//
// FRONTEIRA (endurecimento POSTERIOR, deferido, NÃO em-falta): HSM/sign-in-place, IdP
// corporativo e attestation WebAuthn/AAGUID. O vault do repo (platform/broker,
// vault.Client) é Fetch-only — NÃO assina no lugar —, logo o Nível 2 (o nó nunca detém
// a chave) exige o issuer como autoridade separada, não bastando buscar a chave ao
// vault. A [HumanDirectory] é a porta plugável para a autenticação humana real
// (OIDC/WebAuthn); a impl de referência é uma allowlist DEMO-GRADE-AUTH.

// Erros da autoridade de identidade (todos fail-closed).
var (
	// ErrNoIssuerID — construção sem identificador de emissor.
	ErrNoIssuerID = errors.New("integration: issuer id vazio")
	// ErrNoHumanDirectory — construção sem [HumanDirectory]. Fail-closed: sem
	// directório não há como autenticar o humano, logo não se minta identidade.
	ErrNoHumanDirectory = errors.New("integration: human directory nil")
	// ErrInvalidMintRequest — pedido de mint com humanID/agentID/class vazios.
	ErrInvalidMintRequest = errors.New("integration: pedido de mint invalido (human/agent/class em falta)")
	// ErrHumanNotAuthenticated — o humano não passou a [HumanDirectory]. Envolve o
	// erro concreto do directório (ex.: [ErrHumanNotRegistered]).
	ErrHumanNotAuthenticated = errors.New("integration: humano nao autenticado")
	// ErrHumanNotRegistered — o humano não consta da allowlist de referência.
	ErrHumanNotRegistered = errors.New("integration: humano nao registado (demo-grade auth)")
)

// HumanDirectory é a PORTA plugável de autenticação humana. Numa v1 é preenchida por
// um IdP corporativo (OIDC) ou attestation WebAuthn; aqui a impl de referência
// ([AllowlistDirectory]) é DEMO-GRADE-AUTH — um mero registo de humanos autorizados,
// NÃO um password/credential store. A autoridade só minta uma identidade para um
// humano que este directório AUTENTIQUE; um humano não-autenticado é recusado
// fail-closed.
type HumanDirectory interface {
	// Authenticate confirma que humanID é um principal humano autorizado. Devolve
	// nil se autenticado; um erro (fail-closed) caso contrário. Recebe o contexto
	// para impls reais (OIDC/WebAuthn) que façam I/O.
	Authenticate(ctx context.Context, humanID string) error
}

// AllowlistDirectory é a impl de referência DEMO-GRADE-AUTH de [HumanDirectory]: um
// registo (allowlist) de humanos autorizados. NÃO é um store de credenciais/passwords
// — a autenticação real (OIDC/WebAuthn) é a porta a preencher depois. Concorrente-seguro.
// NUNCA usar em produção como fronteira de autenticação.
type AllowlistDirectory struct {
	mu      sync.RWMutex
	allowed map[string]struct{}
}

// NewAllowlistDirectory constrói o directório de referência com um conjunto inicial
// de humanos autorizados (opcional; podem ser adicionados depois com [Register]).
func NewAllowlistDirectory(humans ...string) *AllowlistDirectory {
	d := &AllowlistDirectory{allowed: make(map[string]struct{}, len(humans))}
	for _, h := range humans {
		if h != "" {
			d.allowed[h] = struct{}{}
		}
	}
	return d
}

// Register adiciona um humano à allowlist (aprovisionamento server-side; num sistema
// real seria o join ao IdP). Idempotente; ids vazios são ignorados.
func (d *AllowlistDirectory) Register(humanID string) {
	if humanID == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.allowed[humanID] = struct{}{}
}

// Authenticate implementa [HumanDirectory]: nil se o humano estiver na allowlist,
// [ErrHumanNotRegistered] caso contrário (fail-closed).
func (d *AllowlistDirectory) Authenticate(_ context.Context, humanID string) error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if _, ok := d.allowed[humanID]; !ok {
		return ErrHumanNotRegistered
	}
	return nil
}

// AuthorityConfig configura a [IssuerAuthority].
type AuthorityConfig struct {
	// IssuerID identifica o emissor (tem de coincidir com o trust anchor do nó).
	// Obrigatório.
	IssuerID string
	// Classes é a política por classe de agente (TTL + escopo-máximo). O escopo
	// efectivo de cada token é a intersecção do escopo pedido com o da classe.
	Classes map[string]identity.ClassPolicy
	// Directory é a porta de autenticação humana (obrigatória; fail-closed sem ela).
	Directory HumanDirectory
	// SigningKey, quando fornecida, é a chave de assinatura ed25519 do issuer
	// (tipicamente carregada de config/vault, encapsulada). nil ⇒ é GERADA em runtime
	// via crypto/rand. Em qualquer caso, a chave privada NUNCA é devolvida pela
	// autoridade — vive apenas no [identity.Issuer] interno, num campo não-exportado.
	SigningKey ed25519.PrivateKey
	// IssuerOptions são opções do [identity.Issuer] (ex.: relógio/Event Store para
	// auditoria de emissão).
	IssuerOptions []identity.IssuerOption
	// DefaultPolicyRef é o policy_ref a codificar nos tokens quando o mint não o
	// especifica. Vazio ⇒ derivado da classe ("policy://<class>").
	DefaultPolicyRef string
}

// IssuerAuthority é a AUTORIDADE DE IDENTIDADE SEPARADA (AOS-156). Detém a chave de
// assinatura ed25519 do issuer num campo NÃO-EXPORTADO (via o [identity.Issuer]
// interno) e NUNCA a devolve. Minta tokens NHI com o humano responsável na RAIZ da
// cadeia de delegação e expõe SÓ o trust anchor (issuerID + pubkey) ao nó, via
// [IssuerAuthority.TrustAnchor]. O verifier do nó constrói-se a partir do trust anchor
// com [NewVerifierFromAuthority] — o nó nunca vê a chave privada, pelo que um nó
// comprometido não forja identidades. Construir com [NewIssuerAuthority].
type IssuerAuthority struct {
	// issuer detém a chave privada (campo não-exportado do próprio Issuer). Não
	// exportado aqui e nunca devolvido: a única saída do material de chave é a PUBKEY
	// via TrustAnchor.
	issuer *identity.Issuer
	// dir é a porta de autenticação humana consultada antes de cada mint.
	dir              HumanDirectory
	defaultPolicyRef string
}

// NewIssuerAuthority constrói a autoridade. Se cfg.SigningKey for nil, a chave é
// gerada em runtime via crypto/rand e encapsulada no issuer (nunca devolvida). Sem
// IssuerID ([ErrNoIssuerID]) ou sem Directory ([ErrNoHumanDirectory]) é recusada
// fail-closed. NUNCA há segredos hardcoded: a chave é gerada ou injectada de
// config/vault pelo chamador.
func NewIssuerAuthority(cfg AuthorityConfig) (*IssuerAuthority, error) {
	if cfg.IssuerID == "" {
		return nil, ErrNoIssuerID
	}
	if cfg.Directory == nil {
		return nil, ErrNoHumanDirectory
	}

	priv := cfg.SigningKey
	if priv == nil {
		// Gerada em runtime via CSPRNG. A variável local sai de escopo após a
		// construção do issuer; só o issuer (campo não-exportado) a retém.
		_, generated, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("integration: falha a gerar chave do issuer: %w", err)
		}
		priv = generated
	}

	iss, err := identity.NewIssuer(cfg.IssuerID, priv, cfg.Classes, cfg.IssuerOptions...)
	if err != nil {
		return nil, err
	}

	return &IssuerAuthority{
		issuer:           iss,
		dir:              cfg.Directory,
		defaultPolicyRef: cfg.DefaultPolicyRef,
	}, nil
}

// MintForHuman emite um token NHI para o par (humano responsável → agente), com o
// HUMANO na RAIZ da cadeia de delegação (o [identity.Issuer] sela
// delegation.NewRoot(human:<humanID>, agentID, escopo)). Passos fail-closed:
//
//  1. valida os campos obrigatórios (human/agent/class);
//  2. AUTENTICA o humano na [HumanDirectory] — um humano não-autenticado é RECUSADO
//     ([ErrHumanNotAuthenticated]) e nenhum token é mintado;
//  3. minta via [identity.Issuer.Issue], cujo escopo efectivo é (scope ∩ classe).
//
// scope são as capabilities pedidas para o agente (a UserAuthority do pedido); o
// escopo do token nunca alarga além do da classe. A chave privada NUNCA é tocada pelo
// chamador — só o issuer interno assina.
func (a *IssuerAuthority) MintForHuman(ctx context.Context, humanID, agentID, class string, scope []string) (identity.Token, error) {
	if humanID == "" || agentID == "" || class == "" {
		return identity.Token{}, ErrInvalidMintRequest
	}
	// Autenticação humana ANTES de qualquer emissão: sem humano autenticado não há
	// raiz de delegação legítima, logo não se minta (fail-closed).
	if err := a.dir.Authenticate(ctx, humanID); err != nil {
		return identity.Token{}, fmt.Errorf("%w: %w", ErrHumanNotAuthenticated, err)
	}

	policyRef := a.defaultPolicyRef
	if policyRef == "" {
		policyRef = "policy://" + class
	}

	return a.issuer.Issue(ctx, identity.IssueRequest{
		UserID:        humanID, // humano responsável ⇒ raiz "human:<humanID>" da cadeia
		AgentID:       agentID,
		AgentClass:    class,
		PolicyRef:     policyRef,
		UserAuthority: scope,
	})
}

// TrustAnchor devolve SÓ o material público do trust anchor: o identificador do
// emissor e a sua chave pública ed25519. É a ÚNICA saída de material de chave da
// autoridade — a chave privada nunca é devolvida. O nó regista este anchor no seu
// verifier (ver [NewVerifierFromAuthority]) e é tudo o que precisa para VERIFICAR
// (nunca para MINTAR).
func (a *IssuerAuthority) TrustAnchor() (issuerID string, pub ed25519.PublicKey) {
	return a.issuer.IssuerID(), a.issuer.PublicKey()
}

// NewVerifierFromAuthority constrói o [identity.Verifier] do NÓ a partir SÓ do trust
// anchor da autoridade (issuerID + pubkey) — nunca a partir de uma chave que o nó
// controle. É o wiring que fecha a separação de AOS-156: o nó recebe apenas a pubkey
// e, por isso, pode verificar identidades mas nunca forjá-las.
//
// O verifier resultante liga-se ao [SecuredRuntime] via SecuredConfig.Verifier:
//
//	auth, _ := integration.NewIssuerAuthority(cfg)           // autoridade separada (detém a chave)
//	verifier := integration.NewVerifierFromAuthority(auth)   // nó: só a pubkey
//	sec, _ := integration.NewSecuredRuntime(integration.SecuredConfig{
//		Verifier: verifier, // ...restantes colaboradores...
//	})
//
// opts adicionais (ex.: [identity.WithRevocations], [identity.WithVerifierClock])
// compõem sobre o trust anchor.
func NewVerifierFromAuthority(a *IssuerAuthority, opts ...identity.VerifierOption) *identity.Verifier {
	iss, pub := a.TrustAnchor()
	base := []identity.VerifierOption{identity.WithTrustedIssuer(iss, pub)}
	return identity.NewVerifier(append(base, opts...)...)
}
