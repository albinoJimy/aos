package identity

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aos-ref/platform/identity/delegation"
	"github.com/aos-ref/substrate/eventstore"
)

// TTLMaximo é o tecto de validade de um NHI emitido por esta biblioteca (AOS-427, decisão 4).
//
// # PORQUE É QUE ISTO EXISTE
//
// Não existia tecto nenhum: `ClassPolicy.TTL` era aceite tal-qual, e o valor era o que o
// operador escrevesse. Enquanto a cunhagem foi MANUAL isso teve uma defesa acidental — dois
// logins no browser por cada token são atrito a sério, e ninguém emite por engano uma
// credencial de um dia quando tem de a pedir à mão.
//
// O AOS-427 remove esse atrito. O que protegia deixaria de proteger exactamente no momento em
// que a emissão passasse a ser automática — que é o pior momento possível para uma defesa
// desaparecer, porque ninguém a veria sair.
//
// # PORQUÊ UMA CONSTANTE, E NÃO CONFIGURAÇÃO
//
// Um tecto configurável é um tecto que um deployment novo, ou um script esquecido, volta a
// poder levantar. A decisão foi «tornar impossível», não «desencorajar»: o valor vive aqui, e
// quem precisar de mais muda-o com uma revisão — que é o ponto, não o obstáculo.
//
// É também a razão de viver na BIBLIOTECA e não na receita: vale para os três chamadores de
// hoje e para os que ainda não existem, sem depender de nenhuma receita estar certa.
//
// # PORQUE É QUE É UMA HORA
//
// Medido, não escolhido por gosto. Os valores legítimos da árvore são: o nó emite a 15m
// (`cmd/aos/main.go`), o orquestrador a 30m (`tokenTTL`, `planner_wiring.go`), e o CLI tem 15m
// por omissão com a receita de produção a passar 45m (`deploy/server/get-id-token.ps1`). Uma
// hora fica acima de todos — não parte nada do que existe — e continua a tornar impossível o
// que o ADR-006 invariante 2 proíbe ao pedir «TTL curto»: um NHI que dure um turno, ou um dia.
const TTLMaximo = time.Hour

// ClassPolicy é a configuração de emissão POR CLASSE de agente: o TTL do token e
// o escopo-máximo que a classe concede. A autoridade efectiva embutida no token
// é sempre a intersecção deste escopo com o do utilizador (nunca alarga).
//
// O TTL é validado contra [TTLMaximo] na CONSTRUÇÃO do emissor, não na emissão.
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
	// Board é o board de soberania do humano responsável (AOS-407), tal como o autenticador o
	// afirmou (ex.: a claim `board` do ID-token OIDC). Vazio ⇒ o token sai sem board, e a
	// soberania por board, quando ligada, nega as suas tool calls.
	Board string
	// UserAuthority são as capabilities que o UTILIZADOR possui. A autoridade do
	// token é a intersecção com [ClassPolicy.Scope].
	UserAuthority []string
	// ParentScope, quando não-nil, activa a semântica on-behalf-of: o escopo do
	// filho é ainda intersectado com o do pai, garantindo filho ⊆ pai (a
	// autoridade só pode estreitar ao descer a cadeia, nunca alargar).
	ParentScope []string
	// AuthMethod é o CONTEXTO DE AUTORIZAÇÃO do binding humano↔NHI: o método/
	// autoridade pelo qual o humano responsável foi autenticado antes deste mint
	// (ex.: "oidc:<issuer>", "allowlist"). É gravado no registo auditável de binding
	// (evento identity.nhi.issued) — ver ADR-003 e [BindingAudit]. É um RÓTULO de
	// método: NUNCA o token/asserção cru nem PII. Vazio ⇒ [AuthMethodUnspecified].
	AuthMethod string
	// Mandate, quando presente, é o mandato assinado pelo humano sob o qual se cunha (AOS-427).
	// Vai EMBEBIDO no token, e o [Issuer] RECUSA cunhar um token que ele não cubra
	// ([ErrMandateViolated]). Essa recusa é cortesia de um emissor honesto — quem decide é o nó,
	// que verifica o mandato contra a chave pinada do humano. A assinatura do mandato NÃO é
	// verificada aqui: o Issuer não conhece as chaves dos humanos (ver [SignedMandate.VerifySignature]).
	Mandate *SignedMandate
}

// Issuer emite tokens NHI assinados. NÃO detém os bytes crus da chave privada: assina
// ATRAVÉS de um [crypto.Signer] (a abstracão stdlib "a chave privada vive noutro sítio").
// Na via de REFERÊNCIA o signer é uma ed25519.PrivateKey in-process (que já implementa
// crypto.Signer); na via HSM/KMS o signer é um adaptador do fornecedor cuja chave NUNCA
// entra no processo. O Issuer só retém a chave PÚBLICA (derivada do signer), a
// configuração por classe e o kid. Construir com [NewIssuer] (chave ed25519 crua) ou
// [NewIssuerWithSigner] (signer arbitrário — via HSM/KMS).
type Issuer struct {
	iss     string
	signer  crypto.Signer
	pub     ed25519.PublicKey
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

// NewIssuer constrói um emissor a partir de uma chave privada ed25519 CRUA — a via de
// REFERÊNCIA (co-localizada), em que a chave vive in-process. iss identifica o emissor
// (tem de coincidir com o trust anchor do [Verifier]); priv é a chave privada ed25519;
// classes é a política por classe de agente. Uma chave inválida devolve
// [ErrInvalidRequest].
//
// COMPATIBILIDADE: esta é a assinatura histórica e mantém-se inalterada. Uma
// ed25519.PrivateKey JÁ implementa [crypto.Signer], pelo que esta função delega em
// [NewIssuerWithSigner]. Para a via HSM/KMS (a chave privada vive fora do processo)
// usar [NewIssuerWithSigner] directamente.
func NewIssuer(iss string, priv ed25519.PrivateKey, classes map[string]ClassPolicy, opts ...IssuerOption) (*Issuer, error) {
	// Validação da chave crua ANTES de a embrulhar: uma chave de tamanho errado é um
	// pedido inválido (preserva o contrato/erro históricos desta via).
	if len(priv) != ed25519.PrivateKeySize {
		return nil, ErrInvalidRequest
	}
	return NewIssuerWithSigner(iss, priv, classes, opts...)
}

// NewIssuerWithSigner constrói um emissor que assina ATRAVÉS de um [crypto.Signer]
// arbitrário — a via de CUSTÓDIA EXTERNA (AOS-175). O signer pode ser uma
// ed25519.PrivateKey in-process OU um adaptador HSM/KMS cuja chave privada NUNCA entra
// no processo (só Public() e Sign() são chamados; os bytes da chave nunca são pedidos).
// É a fronteira que torna a NÃO-FORJABILIDADE real: o Issuer prova origem sem deter o
// segredo.
//
// Fail-closed: recusa (com [ErrInvalidRequest] para iss vazio, [ErrInvalidSigner] para
// o signer) um signer nil ou cuja Public() não seja uma ed25519.PublicKey de tamanho
// correcto — o envelope do token é EdDSA/ed25519 e o verifier só aceita esse algoritmo,
// logo um signer não-ed25519 nunca produziria um token verificável.
func NewIssuerWithSigner(iss string, signer crypto.Signer, classes map[string]ClassPolicy, opts ...IssuerOption) (*Issuer, error) {
	if iss == "" {
		return nil, ErrInvalidRequest
	}
	if signer == nil {
		return nil, ErrInvalidSigner
	}
	// signerPublicKey obtém e valida a pubkey de forma panic-safe: um chamador que
	// contorne NewIssuer e passe uma ed25519.PrivateKey MALFORMADA (len<32) directamente
	// aqui faria signer.Public() entrar em pânico (copy(pub, priv[32:]) — slice bounds).
	// Converte-se qualquer signer patológico no sentinela fail-closed ErrInvalidSigner
	// em vez de derrubar o processo.
	pub, err := signerPublicKey(signer)
	if err != nil {
		return nil, err
	}
	// O TECTO IMPÕE-SE AQUI, NA CONSTRUÇÃO, E ISSO É DESENHO (AOS-427).
	//
	// Podia impor-se no `Issue`, e seria pior por duas razões. Primeira: um emissor construído
	// com uma política impossível ficaria de pé e só falharia na primeira emissão — longe de
	// quem o configurou, e possivelmente em produção. Segunda: aqui o mapa é COPIADO logo a
	// seguir, pelo que o que se valida é exactamente o que o emissor vai usar para sempre; o
	// chamador não o pode mutar por baixo depois.
	//
	// RECUSA-SE, NÃO SE APARA. Um clamp silencioso seria a pior das três saídas: o banner de
	// arranque diria um TTL e o token teria outro, e a divergência só apareceria a quem fosse
	// descodificar um `exp`. Um tecto que mente sobre si próprio é pior do que tecto nenhum.
	cp := make(map[string]ClassPolicy, len(classes))
	for k, v := range classes {
		if v.TTL <= 0 {
			// TTL ZERO OU NEGATIVO nasce expirado (`exp == iat`, ou antes dele). Nunca foi
			// recusado, e é sempre defeito — ninguém quer emitir uma credencial morta. Entra
			// no mesmo sentinela porque é a mesma pergunta: «esta validade é utilizável?».
			return nil, fmt.Errorf("%w: classe %q tem TTL %v, e tem de ser maior que zero", ErrTTLForaDeGama, k, v.TTL)
		}
		if v.TTL > TTLMaximo {
			return nil, fmt.Errorf("%w: classe %q pede TTL %v, acima do tecto de %v", ErrTTLForaDeGama, k, v.TTL, TTLMaximo)
		}
		cp[k] = v
	}
	i := &Issuer{
		iss:     iss,
		signer:  signer,
		pub:     pub,
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

// signerPublicKey obtém a chave pública ed25519 de um [crypto.Signer] de forma
// PANIC-SAFE e fail-closed. É a fronteira defensiva da custódia externa (AOS-175):
//
//   - recupera de qualquer pânico em signer.Public() (ex.: ed25519.PrivateKey malformada,
//     len<32, faz copy(pub, priv[32:]) e entra em pânico com slice bounds out of range) e
//     converte-o no sentinela [ErrInvalidSigner] — um signer patológico NUNCA derruba o
//     processo, devolve sempre o erro fail-closed;
//   - valida que a pubkey é uma ed25519.PublicKey de tamanho correcto (o envelope é
//     EdDSA/ed25519; qualquer outra coisa nunca produziria um token verificável).
//
// Não expõe nem regista o material da chave; em erro devolve nil e o sentinela.
func signerPublicKey(signer crypto.Signer) (pub ed25519.PublicKey, err error) {
	defer func() {
		if r := recover(); r != nil {
			pub = nil
			err = ErrInvalidSigner
		}
	}()
	p, ok := signer.Public().(ed25519.PublicKey)
	if !ok || len(p) != ed25519.PublicKeySize {
		return nil, ErrInvalidSigner
	}
	return p, nil
}

// PublicKey devolve a chave pública correspondente (derivada do signer na construção),
// para registar como trust anchor no verificador (ver [WithTrustedIssuer]). É a ÚNICA
// saída de material de chave do Issuer — a privada nunca é exposta.
func (i *Issuer) PublicKey() ed25519.PublicKey {
	return i.pub
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

	// Cadeia de delegação raiz: esta NHI é criada on-behalf-of um humano
	// responsável. A raiz é sempre "human:<user_id>" (AOS-006); a autoridade do
	// elo é o escopo do token. Sela-se na assinatura abaixo.
	chain, err := delegation.NewRoot(humanRoot(req.UserID), req.AgentID, scope)
	if err != nil {
		return Token{}, err
	}

	now := i.now()
	claims := Claims{
		UserID:          req.UserID,
		AgentID:         req.AgentID,
		AgentClass:      req.AgentClass,
		PolicyRef:       req.PolicyRef,
		Board:           req.Board,
		Scope:           scope,
		Issuer:          i.iss,
		IssuedAt:        now.Unix(),
		NotBefore:       now.Unix(),
		Expiry:          now.Add(cp.TTL).Unix(),
		JTI:             jti,
		DelegationChain: chain,
	}
	if req.Mandate != nil {
		if err := req.Mandate.Mandate.Covers(claims); err != nil {
			return Token{}, err
		}
		m := *req.Mandate
		m.Mandate.Scope = append([]string(nil), m.Mandate.Scope...)
		claims.Mandate = &m
	}

	compact, err := signToken(i.signer, i.kid, claims)
	if err != nil {
		return Token{}, err
	}

	if err := i.recordIssued(ctx, claims, req.AuthMethod); err != nil {
		// Emissão não-auditável é uma acção sem rasto: fail-closed (ADR-003/010).
		return Token{}, err
	}

	return Token{Compact: compact, Claims: claims}, nil
}

// recordIssued grava o evento identity.nhi.issued — o registo AUDITÁVEL do binding
// humano↔NHI (ADR-003; só metadados + contexto de autorização). No-op sem store.
// authMethod é o rótulo do método/autoridade de autorização (ex.: "oidc:<issuer>");
// vazio é normalizado para [AuthMethodUnspecified] para que o binding declare sempre
// um método. NUNCA grava segredos/asserções crus nem PII.
func (i *Issuer) recordIssued(ctx context.Context, c Claims, authMethod string) error {
	if i.store == nil {
		return nil
	}
	if authMethod == "" {
		authMethod = AuthMethodUnspecified
	}
	payload := issuedPayload{
		JTI:        c.JTI,
		UserID:     c.UserID,
		AgentID:    c.AgentID,
		AgentClass: c.AgentClass,
		PolicyRef:  c.PolicyRef,
		Board:      c.Board,
		Scope:      c.Scope,
		Issuer:     c.Issuer,
		IssuedAt:   c.IssuedAt,
		Expiry:     c.Expiry,
		AuthMethod: authMethod,
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
			DelegationChain: chainToHops(c.DelegationChain),
			Scope:           c.Scope,
		},
	})
	return err
}

// humanRoot normaliza um user_id para a raiz da cadeia de delegação: garante o
// prefixo "human:" exigido por AOS-006. Se o user_id já vier prefixado (ex.:
// "human:alice"), devolve-o inalterado; senão prefixa-o.
func humanRoot(userID string) string {
	if strings.HasPrefix(userID, delegation.HumanPrefix) {
		return userID
	}
	return delegation.HumanPrefix + userID
}

// chainToHops projecta a cadeia de delegação para os hops (sub/act_as) do Event
// Store. Os hashes/autoridade ficam no token selado; o evento regista a ordem
// dos elos, suficiente para reconstruir "quem autorizou" (a raiz humana).
func chainToHops(chain delegation.Chain) []eventstore.DelegationHop {
	if len(chain) == 0 {
		return nil
	}
	out := make([]eventstore.DelegationHop, len(chain))
	for i, l := range chain {
		out[i] = eventstore.DelegationHop{Sub: l.Sub, ActAs: l.ActAs}
	}
	return out
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
