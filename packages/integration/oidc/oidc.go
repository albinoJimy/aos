// Package oidc implementa a validação REAL de um ID-token OIDC (AOS-174, frente 1 do
// D4) usando SÓ a stdlib — sem qualquer biblioteca OIDC/JWT externa. É o núcleo que
// autentica um humano contra um IdP OIDC (discovery + JWKS + verificação de assinatura
// do JWS + validação de claims), substituindo a allowlist DEMO-GRADE da autoridade de
// identidade. O adaptador que o liga à porta HumanDirectory vive no pacote integration
// (OIDCDirectory).
//
// INVARIANTE ZERO-DEP: este pacote importa apenas net/http, crypto/{rsa,ecdsa,sha256},
// crypto/elliptic, encoding/{json,base64}, math/big e afins — nada fora da stdlib. A
// exceção da emenda 1.3 (lib WebAuthn) é da frente 4, não desta.
//
// FAIL-CLOSED: qualquer anomalia — alg fora do allowlist, "none", HS* (confusão de
// algoritmo), kid desconhecido, assinatura inválida, iss/aud errados, janela temporal
// violada, nonce divergente, JWKS/discovery indisponível — NEGA com um erro tipado
// (comparável com errors.Is). Nunca "permitir por omissão".
//
// SENSIBILIDADE: o ID-token é material sensível. Este pacote NÃO loga o token cru nem
// claims sensíveis (não escreve para stdout/stderr de todo).
package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Erros tipados (todos fail-closed; comparáveis com errors.Is).
var (
	// ErrNoIssuer — Config.Issuer vazio.
	ErrNoIssuer = errors.New("oidc: issuer vazio")
	// ErrNoAudience — Config.Audience (client id) vazio.
	ErrNoAudience = errors.New("oidc: audience/client vazio")
	// ErrMalformedToken — o ID-token não é um JWS compacto de 3 segmentos válidos.
	ErrMalformedToken = errors.New("oidc: id-token mal formado")
	// ErrUnsupportedAlg — alg fora do allowlist {RS256,PS256,ES256}, incluindo "none".
	ErrUnsupportedAlg = errors.New("oidc: alg nao suportado")
	// ErrAlgConfusion — tentativa de confusão de algoritmo: header HS* (usar a chave
	// pública como segredo HMAC) ou tipo de chave incompatível com o alg assimétrico.
	ErrAlgConfusion = errors.New("oidc: confusao de algoritmo (chave assimetrica nao e segredo HMAC)")
	// ErrUnknownKeyID — kid do header ausente do JWKS (mesmo após refresh limitado).
	ErrUnknownKeyID = errors.New("oidc: kid desconhecido no JWKS")
	// ErrSignatureInvalid — a assinatura não valida sob a chave pública do kid.
	ErrSignatureInvalid = errors.New("oidc: assinatura invalida")
	// ErrIssuerMismatch — claim iss diferente do issuer configurado.
	ErrIssuerMismatch = errors.New("oidc: iss nao corresponde ao issuer configurado")
	// ErrAudienceMismatch — claim aud não contém o audience/client configurado.
	ErrAudienceMismatch = errors.New("oidc: aud nao contem o client configurado")
	// ErrTokenExpired — exp ausente ou no passado (fora do leeway).
	ErrTokenExpired = errors.New("oidc: token expirado")
	// ErrTokenNotYetValid — nbf/iat no futuro (fora do leeway).
	ErrTokenNotYetValid = errors.New("oidc: token ainda nao valido (nbf/iat no futuro)")
	// ErrNonceMismatch — nonce esperado divergente do claim.
	ErrNonceMismatch = errors.New("oidc: nonce nao corresponde")
	// ErrNoSubject — claim sub ausente (sem subject não há humanID verificável).
	ErrNoSubject = errors.New("oidc: subject (sub) ausente")
	// ErrJWKSUnavailable — o endpoint JWKS falhou ou devolveu um documento inválido.
	ErrJWKSUnavailable = errors.New("oidc: JWKS indisponivel")
	// ErrDiscoveryFailed — o discovery (.well-known/openid-configuration) falhou.
	ErrDiscoveryFailed = errors.New("oidc: discovery falhou")
	// ErrUnsupportedKey — JWK com kty/crv não suportado (só RSA e EC P-256).
	ErrUnsupportedKey = errors.New("oidc: tipo de chave JWK nao suportado")
	// ErrInsecureTransport — Issuer/JWKSURI com esquema não-https (fora de loopback).
	// O material de chave nunca é obtido sobre transporte não autenticado (defesa
	// contra MITM a servir chaves forjadas num jwks_uri em claro).
	ErrInsecureTransport = errors.New("oidc: transporte inseguro (exige https)")
	// ErrCritUnsupported — o header JOSE declara extensões críticas (crit, RFC 7515
	// 4.1.11) que este verifier não compreende ⇒ recusa (nunca aceitar semântica de
	// segurança desconhecida).
	ErrCritUnsupported = errors.New("oidc: header 'crit' com extensoes nao suportadas")
	// ErrAzpMismatch — token multi-audience cujo azp (authorized party) não é o client
	// configurado, ou ausente quando exigido (OIDC Core 3.1.3.7).
	ErrAzpMismatch = errors.New("oidc: azp nao corresponde ao client configurado")
	// ErrMissingJTI — RequireJTI activo mas o token não traz jti (sem jti não há
	// deteccao de reutilização por-token).
	ErrMissingJTI = errors.New("oidc: jti obrigatorio ausente")
	// ErrTokenReplayed — o mesmo (iss,jti) já foi consumido dentro da sua janela de
	// validade ⇒ reutilização (replay) recusada.
	ErrTokenReplayed = errors.New("oidc: token reutilizado (replay de jti)")
	// ErrTokenTooOld — iat mais antigo que MaxAge (bound superior à janela de replay,
	// independente do exp).
	ErrTokenTooOld = errors.New("oidc: token demasiado antigo (iat fora de MaxAge)")
)

// algAllowlist — os ÚNICOS algoritmos de assinatura aceites. HS* e "none" ficam de
// fora deliberadamente (defesa contra confusão de algoritmo e alg:none).
var algAllowlist = map[string]struct{}{
	"RS256": {},
	"PS256": {},
	"ES256": {},
}

// Config configura o [Verifier]. Só Issuer e Audience são obrigatórios; o resto tem
// defaults seguros.
type Config struct {
	// Issuer é o iss esperado (e a base do discovery se JWKSURI for vazio). Obrigatório.
	Issuer string
	// Audience é o client id que TEM de constar do claim aud. Obrigatório.
	Audience string
	// JWKSURI, quando fornecido, salta o discovery e usa este endpoint de JWKS
	// directamente. Vazio ⇒ discovery via <Issuer>/.well-known/openid-configuration.
	JWKSURI string
	// HTTPClient injectável (timeout, transport de teste). nil ⇒ cliente com timeout.
	HTTPClient *http.Client
	// Clock injectável para a janela temporal. nil ⇒ time.Now.
	Clock func() time.Time
	// Leeway é a tolerância de relógio aplicada a exp/nbf/iat. 0 ⇒ [defaultLeeway].
	//
	// NÃO É SÓ TOLERÂNCIA DE RELÓGIO. O leeway SOMA-SE ao [Config.MaxAge] na verificação de
	// idade (`now > iat + MaxAge + leeway`), pelo que a janela efectiva em que um ID-token
	// roubado ainda serve é `MaxAge + Leeway` — e não `MaxAge`. Quem afina um destes está a
	// afinar o outro; ver `tectos_test.go`.
	Leeway time.Duration
	// Nonce, quando != "", exige que o claim nonce coincida (defesa anti-replay do
	// fluxo de autenticação).
	Nonce string
	// MinJWKSRefetchInterval é o tecto anti-abuso entre re-fetches do JWKS perante um
	// kid desconhecido: um atacante a martelar kids inexistentes não força fetches
	// ilimitados. 0 ⇒ 1 minuto.
	MinJWKSRefetchInterval time.Duration
	// MaxAge, quando > 0, impõe uma idade MÁXIMA do token medida por iat: um token com
	// iat mais antigo que now-MaxAge (mais leeway) é recusado ([ErrTokenTooOld]) MESMO
	// que ainda não tenha expirado. Estreita a janela de replay de um ID-token capturado
	// (o modelo stateless não tem sessão a fechar), pelo que se recomenda um valor curto
	// em produção (ex.: 2–5 min) a par de exp curto e TLS.
	MaxAge time.Duration
	// RequireJTI exige o claim jti. Sem jti não há detecção de reutilização por-token
	// (ver anti-replay em [Verifier.Validate]); activar quando o IdP o emite e a
	// política pede single-use estrito.
	RequireJTI bool
	// AllowInsecureTransport desliga a exigência de https para Issuer/JWKSURI. SÓ para
	// testes/dev — em produção o material de chave TEM de vir sobre TLS. Loopback
	// (localhost/127.0.0.1/::1) é sempre permitido sem esta flag.
	AllowInsecureTransport bool
}

// Claims são as asserções verificadas devolvidas por [Verifier.Validate]. Só o que é
// necessário para resolver o humanID; nunca o token cru.
type Claims struct {
	// Subject é o sub verificado (o principal humano). Base do humanID.
	Subject string
	// Email é o email verificado, se presente (conveniência; pode ser vazio).
	Email string
	// Issuer é o iss verificado (== Config.Issuer).
	Issuer string
	// Board é o claim `board` VERIFICADO (soberania de leitura, AOS-205): a fronteira de
	// governação a que o titular pertence, tal como o IdP a assere. É devolvido do MESMO
	// payload que a assinatura cobre, pelo que um chamador NÃO o pode forjar num header
	// cru — quem consome o read-path soberano deriva o board DAQUI, não de um cabeçalho
	// auto-declarado. Vazio quando o IdP não emite o claim (o consumidor decide se o exige).
	Board string
}

// Verifier valida ID-tokens OIDC contra um issuer/JWKS fixos. Concorrente-seguro; a
// cache de JWKS é partilhada e protegida por mutex. Construir com [NewVerifier].
type Verifier struct {
	issuer        string
	audience      string
	nonce         string
	httpClient    *http.Client
	clock         func() time.Time
	leeway        time.Duration
	minRefetch    time.Duration
	maxAge        time.Duration
	requireJTI    bool
	allowInsecure bool

	mu        sync.Mutex
	keys      map[string]crypto.PublicKey // kid -> chave pública (RSA/EC)
	jwksURI   string                      // resolvido (config ou discovery)
	lastFetch time.Time
	inflight  *refreshCall // refresh de JWKS em curso (singleflight), se algum

	// Cache anti-replay: (iss\x00jti) -> exp unix. Detecta reutilização de um mesmo
	// ID-token dentro da sua janela de validade. Protegida por replayMu (mutex próprio
	// para não serializar com a I/O de JWKS).
	replayMu sync.Mutex
	seen     map[string]int64
}

// refreshCall coalesce refreshes concorrentes do JWKS (singleflight manual): o líder
// faz a I/O de rede, os restantes esperam em done e reutilizam o resultado.
type refreshCall struct {
	done chan struct{}
	err  error
}

// defaultLeeway é a tolerância de relógio aplicada quando [Config.Leeway] não é dada.
//
// PORQUE É UMA CONSTANTE NOMEADA E NÃO UM 60 INLINE. Este valor não é cosmético: a verificação
// de idade é `now > iat + maxAge + leeway`, pelo que o leeway ALARGA O TECTO DE REPLAY. Com
// maxAge de 5 min, um leeway de 5 min não é «tolerância de relógio» — é o dobro da janela em que
// um ID-token roubado continua a servir. Um valor inline não tem onde levar esta frase, e uma
// varredura adversarial mostrou que era mutável até 299 s sem nenhum teste protestar.
//
// 60 s é a ordem de grandeza do desvio que se tolera entre um IdP e um verificador com NTP. O
// tecto defensável e a bracketing do valor vivem em `tectos_test.go`.
const defaultLeeway = 60 * time.Second

// NewVerifier constrói o verifier. Sem Issuer ([ErrNoIssuer]) ou sem Audience
// ([ErrNoAudience]) recusa fail-closed.
func NewVerifier(cfg Config) (*Verifier, error) {
	if cfg.Issuer == "" {
		return nil, ErrNoIssuer
	}
	if cfg.Audience == "" {
		return nil, ErrNoAudience
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = newHardenedOIDCClient(cfg.AllowInsecureTransport)
	}
	clk := cfg.Clock
	if clk == nil {
		clk = time.Now
	}
	leeway := cfg.Leeway
	if leeway == 0 {
		leeway = defaultLeeway
	}
	minRefetch := cfg.MinJWKSRefetchInterval
	if minRefetch == 0 {
		minRefetch = time.Minute
	}
	// Transporte: o material de chave (discovery + JWKS) nunca deve vir sobre transporte
	// não autenticado. Recusa fail-closed https em falta (loopback e flag exceptuados).
	if err := checkTransport(cfg.Issuer, cfg.AllowInsecureTransport); err != nil {
		return nil, err
	}
	if cfg.JWKSURI != "" {
		if err := checkTransport(cfg.JWKSURI, cfg.AllowInsecureTransport); err != nil {
			return nil, err
		}
	}
	return &Verifier{
		issuer:        cfg.Issuer,
		audience:      cfg.Audience,
		nonce:         cfg.Nonce,
		httpClient:    hc,
		clock:         clk,
		leeway:        leeway,
		minRefetch:    minRefetch,
		maxAge:        cfg.MaxAge,
		requireJTI:    cfg.RequireJTI,
		allowInsecure: cfg.AllowInsecureTransport,
		jwksURI:       cfg.JWKSURI,
		seen:          make(map[string]int64),
	}, nil
}

// checkTransport recusa fail-closed um URL cujo esquema não seja https, salvo quando o
// host é loopback (localhost/127.0.0.1/::1) ou allowInsecure está activo (dev/testes).
// Garante que chaves de confiança nunca chegam sobre transporte não autenticado.
func checkTransport(raw string, allowInsecure bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: url invalido", ErrInsecureTransport)
	}
	if u.Scheme == "https" {
		return nil
	}
	if allowInsecure || isLoopbackHost(u.Host) {
		return nil
	}
	return fmt.Errorf("%w: esquema %q em %q", ErrInsecureTransport, u.Scheme, raw)
}

// oidcHTTPTimeout é o timeout total do cliente HTTP default do verificador (discovery + JWKS).
const oidcHTTPTimeout = 10 * time.Second

// maxOIDCRedirects limita a cadeia de redirects na busca de discovery/JWKS. O material de chave
// raramente precisa de redirects; uma cadeia longa — ou para um host interno — é um vector de SSRF.
const maxOIDCRedirects = 3

// newHardenedOIDCClient constrói o cliente HTTP DEFAULT do verificador (usado só quando
// [Config.HTTPClient] é nil — o caminho de PRODUÇÃO). Defesa-em-profundidade sobre o transporte do
// material de chave (análogo a AOS-223 no Model Gateway):
//
//   - timeout total (não pendura um pedido num IdP lento/malicioso);
//   - TLS MinVersion 1.2 explícito (recusa handshakes antigos);
//   - [http.Client.CheckRedirect] que LIMITA a cadeia de redirects E RE-VALIDA o transporte de cada
//     salto com [checkTransport]: um redirect do endpoint de discovery/JWKS para http/host interno é
//     RECUSADO (anti-SSRF), com a mesma regra do URL inicial (https salvo loopback/allowInsecure).
//
// Um cliente INJECTADO (testes/httptest, ou um transport de deployment) é respeitado tal-qual e
// NÃO passa por aqui — o endurecimento é do default, nunca uma sobreposição do que o operador liga.
func newHardenedOIDCClient(allowInsecure bool) *http.Client {
	return &http.Client{
		Timeout: oidcHTTPTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxOIDCRedirects {
				return fmt.Errorf("oidc: demasiados redirects na busca de discovery/JWKS (>%d)", maxOIDCRedirects)
			}
			return checkTransport(req.URL.String(), allowInsecure)
		},
	}
}

// isLoopbackHost indica se host (com ou sem porta) é um endereço de loopback ou
// "localhost".
func isLoopbackHost(host string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// jose é o cabeçalho JOSE do ID-token.
type jose struct {
	Alg  string   `json:"alg"`
	Kid  string   `json:"kid"`
	Typ  string   `json:"typ"`
	Crit []string `json:"crit"` // extensões críticas (RFC 7515 4.1.11); nenhuma suportada
}

// idClaims são as claims do ID-token relevantes para a validação. exp/nbf/iat são
// ponteiros para distinguir ausência de zero.
type idClaims struct {
	Iss   string   `json:"iss"`
	Sub   string   `json:"sub"`
	Aud   audience `json:"aud"`
	Azp   string   `json:"azp"` // authorized party (obrigatório em tokens multi-aud)
	Exp   *int64   `json:"exp"`
	Nbf   *int64   `json:"nbf"`
	Iat   *int64   `json:"iat"`
	Nonce string   `json:"nonce"`
	Jti   string   `json:"jti"`
	Email string   `json:"email"`
	Board string   `json:"board"` // fronteira de soberania asserida pelo IdP (AOS-205)
}

// audience aceita tanto uma string como um array de strings no claim aud (ambos são
// legais em JWT).
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*a = audience{s}
		return nil
	}
	var ss []string
	if err := json.Unmarshal(b, &ss); err == nil {
		*a = audience(ss)
		return nil
	}
	return fmt.Errorf("%w: aud nem string nem array", ErrMalformedToken)
}

func (a audience) contains(v string) bool {
	for _, x := range a {
		if x == v {
			return true
		}
	}
	return false
}

// Issuer devolve o identificador do IdP (o claim iss exigido) contra o qual este
// verifier valida os ID-tokens. É material PÚBLICO de configuração (uma URL de
// issuer), não um segredo: serve para rotular a autoridade de autenticação no
// registo auditável do binding humano↔NHI (ex.: "oidc:<issuer>", ADR-003).
func (v *Verifier) Issuer() string { return v.issuer }

// Validate autentica um ID-token OIDC e devolve as claims verificadas. É a superfície
// central fail-closed: qualquer falha devolve um erro tipado e Claims vazio.
//
// Ordem (nunca confiar em claims antes da assinatura):
//  1. parse do JWS compacto (3 segmentos base64url);
//  2. gate de algoritmo (allowlist {RS256,PS256,ES256}; "none"/HS* recusados);
//  3. localizar a chave pelo kid no JWKS (com refresh limitado anti-abuso);
//  4. VERIFICAR a assinatura sob essa chave;
//  5. validar claims (iss, aud, exp/nbf/iat, nonce, sub).
func (v *Verifier) Validate(ctx context.Context, rawIDToken string) (Claims, error) {
	parts := strings.Split(rawIDToken, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Claims{}, ErrMalformedToken
	}

	hb, err := b64urlDecode(parts[0])
	if err != nil {
		return Claims{}, ErrMalformedToken
	}
	var h jose
	if err := json.Unmarshal(hb, &h); err != nil {
		return Claims{}, ErrMalformedToken
	}

	// (1b) Extensões críticas: um verifier que não compreenda um parâmetro crit DEVE
	// recusar (RFC 7515 4.1.11). Não suportamos nenhuma, logo a mera presença de crit
	// nega — impede que semântica de segurança introduzida por extensões seja ignorada.
	if len(h.Crit) > 0 {
		return Claims{}, ErrCritUnsupported
	}

	// (2) Gate de algoritmo ANTES de qualquer cripto. HS* é a assinatura da confusão
	// RS256<->HS256; "none" e desconhecidos caem em ErrUnsupportedAlg.
	if _, ok := algAllowlist[h.Alg]; !ok {
		if strings.HasPrefix(h.Alg, "HS") {
			return Claims{}, ErrAlgConfusion
		}
		return Claims{}, ErrUnsupportedAlg
	}

	sig, err := b64urlDecode(parts[2])
	if err != nil {
		return Claims{}, ErrMalformedToken
	}
	signingInput := []byte(parts[0] + "." + parts[1])

	// (3) Localizar a chave pelo kid (fetch/refresh limitado).
	key, err := v.keyForKid(ctx, h.Kid)
	if err != nil {
		return Claims{}, err
	}

	// (4) Verificar a assinatura. Nunca tratar uma chave RSA/EC como segredo HMAC.
	if err := verifySignature(h.Alg, key, signingInput, sig); err != nil {
		return Claims{}, err
	}

	// (5) Claims — só agora que a assinatura é boa.
	pb, err := b64urlDecode(parts[1])
	if err != nil {
		return Claims{}, ErrMalformedToken
	}
	var c idClaims
	if err := json.Unmarshal(pb, &c); err != nil {
		return Claims{}, ErrMalformedToken
	}

	now := v.clock()
	if c.Exp == nil {
		return Claims{}, ErrTokenExpired
	}
	if now.After(time.Unix(*c.Exp, 0).Add(v.leeway)) {
		return Claims{}, ErrTokenExpired
	}
	if c.Nbf != nil && now.Before(time.Unix(*c.Nbf, 0).Add(-v.leeway)) {
		return Claims{}, ErrTokenNotYetValid
	}
	if c.Iat != nil && now.Before(time.Unix(*c.Iat, 0).Add(-v.leeway)) {
		return Claims{}, ErrTokenNotYetValid
	}
	if c.Iss != v.issuer {
		return Claims{}, ErrIssuerMismatch
	}
	if !c.Aud.contains(v.audience) {
		return Claims{}, ErrAudienceMismatch
	}
	// Multi-audience: OIDC Core 3.1.3.7 exige azp presente e igual ao client quando o
	// token tem mais que um audience. Sem isto, um token legitimamente emitido para
	// vários serviços (incluindo o nosso) mas AUTORIZADO a outro seria aceite.
	if len(c.Aud) > 1 {
		if c.Azp == "" || c.Azp != v.audience {
			return Claims{}, ErrAzpMismatch
		}
	}
	if v.nonce != "" && c.Nonce != v.nonce {
		return Claims{}, ErrNonceMismatch
	}
	// Idade máxima (bound superior à janela de replay, independente do exp).
	if v.maxAge > 0 && c.Iat != nil {
		if now.After(time.Unix(*c.Iat, 0).Add(v.maxAge + v.leeway)) {
			return Claims{}, ErrTokenTooOld
		}
	}
	if v.requireJTI && c.Jti == "" {
		return Claims{}, ErrMissingJTI
	}
	if c.Sub == "" {
		return Claims{}, ErrNoSubject
	}

	// Anti-replay: gate FINAL (só após assinatura+claims válidas, para não registar jti
	// de tokens que iríamos recusar de qualquer modo). Quando o token traz jti, cada
	// (iss,jti) só é consumível UMA vez dentro da sua janela de validade — um ID-token
	// capturado e reapresentado é recusado ([ErrTokenReplayed]) em vez de mintar de novo.
	// Sem jti, cai-se no modelo stateless (mitigado por exp curto + MaxAge + TLS).
	if c.Jti != "" && c.Exp != nil {
		if err := v.checkReplay(c.Iss, c.Jti, *c.Exp); err != nil {
			return Claims{}, err
		}
	}

	return Claims{Subject: c.Sub, Email: c.Email, Issuer: c.Iss, Board: c.Board}, nil
}

// checkReplay regista e detecta reutilização de (iss,jti). Recusa [ErrTokenReplayed] se
// o par já foi visto dentro da sua janela; caso contrário regista-o (com TTL = exp+leeway).
// Atómico (check-and-record sob o mesmo lock) para que dois replays concorrentes não
// passem ambos. Faz eviction preguiçosa das entradas expiradas para limitar memória.
//
// # O TTL É `exp+leeway` E NÃO `exp` — A ENTRADA TEM DE COBRIR A JANELA DE ACEITAÇÃO INTEIRA
//
// [Verifier.Validate] aceita enquanto `now <= exp + leeway` (a verificação de expiração
// acima). Despejar a entrada em `exp` abria `leeway` segundos — 60 s por omissão — em que o
// token AINDA passava a verificação de expiração e o par (iss,jti) já não estava registado:
// o mesmo token voltava a ser aceite. Um anti-replay que caduca ANTES do que protege não é
// anti-replay no fim da vida do token, que é exactamente quando um token capturado é
// reapresentado.
//
// E no empate exacto `now == exp` o `<=` apagava a entrada enquanto `now.After(exp)` ainda
// dizia «válido» — o mesmo enviesamento de resolução-de-segundo que [saudeDeSelagem] teve, em
// que o desempate no mesmo segundo caía para o lado errado. A comparação é agora ESTRITA, pelo
// que o segundo do empate é retido em vez de despejado.
//
// CUSTO DECLARADO: cada entrada vive `leeway` segundos a mais. Com os tokens de 5 min do realm
// entregue são ~20 % mais entradas retidas em pico. É o preço de a janela de protecção cobrir
// a janela de aceitação, e é a direcção segura — reter a mais nunca aceita um replay, reter a
// menos aceita.
func (v *Verifier) checkReplay(iss, jti string, exp int64) error {
	now := v.clock().Unix()
	// Segundos inteiros: `now` é truncado por [time.Time.Unix]. Truncar para baixo faz a
	// entrada durar ATÉ UM SEGUNDO A MAIS, nunca a menos — a direcção segura.
	leeway := int64(v.leeway.Seconds())
	v.replayMu.Lock()
	defer v.replayMu.Unlock()
	for k, e := range v.seen {
		if e+leeway < now {
			delete(v.seen, k)
		}
	}
	key := iss + "\x00" + jti
	if _, ok := v.seen[key]; ok {
		return ErrTokenReplayed
	}
	v.seen[key] = exp
	return nil
}

// keyForKid devolve a chave pública do kid, buscando/refrescando o JWKS quando
// necessário. Propriedades:
//   - a I/O de rede (discovery + JWKS) ocorre FORA da secção crítica: uma validação
//     concorrente nunca bloqueia no mutex durante a I/O (um IdP lento não paralisa todas
//     as validações);
//   - singleflight: um só refresh corre de cada vez; os concorrentes esperam por ele e
//     reutilizam o resultado (sem fetches duplicados);
//   - anti-abuso: perante kids desconhecidos não se refaz fetch mais que uma vez por
//     minRefetch.
func (v *Verifier) keyForKid(ctx context.Context, kid string) (crypto.PublicKey, error) {
	v.mu.Lock()
	if k, ok := v.keys[kid]; ok {
		v.mu.Unlock()
		return k, nil
	}
	// Já temos chaves e o tecto anti-abuso ainda não expirou ⇒ não refazer fetch.
	if v.keys != nil && v.clock().Sub(v.lastFetch) < v.minRefetch {
		v.mu.Unlock()
		return nil, ErrUnknownKeyID
	}
	// Um refresh já está em curso ⇒ esperar por ele (não iniciar outro).
	if call := v.inflight; call != nil {
		v.mu.Unlock()
		<-call.done
		if call.err != nil {
			return nil, call.err
		}
		return v.lookupKid(kid)
	}
	// Somos o líder: publicar o marcador inflight e fazer a I/O SEM o lock.
	call := &refreshCall{done: make(chan struct{})}
	v.inflight = call
	uri := v.jwksURI
	v.mu.Unlock()

	keys, resolvedURI, err := v.fetchKeys(ctx, uri)

	v.mu.Lock()
	call.err = err
	if err == nil {
		v.keys = keys
		v.jwksURI = resolvedURI
		v.lastFetch = v.clock()
	}
	v.inflight = nil
	v.mu.Unlock()
	close(call.done)

	if err != nil {
		return nil, err
	}
	return v.lookupKid(kid)
}

// lookupKid devolve a chave em cache para kid ou [ErrUnknownKeyID].
func (v *Verifier) lookupKid(kid string) (crypto.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	return nil, ErrUnknownKeyID
}

// fetchKeys resolve o jwks_uri (via discovery, uma vez) e busca as chaves. NÃO toca no
// estado partilhado nem retém o lock — é I/O pura; o chamador publica o resultado.
func (v *Verifier) fetchKeys(ctx context.Context, uri string) (map[string]crypto.PublicKey, string, error) {
	if uri == "" {
		resolved, err := v.discover(ctx)
		if err != nil {
			return nil, "", err
		}
		uri = resolved
	}
	keys, err := fetchJWKS(ctx, v.httpClient, uri)
	if err != nil {
		return nil, "", err
	}
	return keys, uri, nil
}

// discoveryDoc é o subconjunto do .well-known/openid-configuration que consumimos.
type discoveryDoc struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

// discover busca o documento de configuração e devolve o jwks_uri, validando que o
// issuer anunciado coincide com o configurado (defesa contra discovery adulterado).
func (v *Verifier) discover(ctx context.Context) (string, error) {
	url := strings.TrimRight(v.issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDiscoveryFailed, err)
	}
	resp, err := v.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDiscoveryFailed, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: status %d", ErrDiscoveryFailed, resp.StatusCode)
	}
	var doc discoveryDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", fmt.Errorf("%w: %v", ErrDiscoveryFailed, err)
	}
	if doc.Issuer != v.issuer {
		return "", fmt.Errorf("%w: issuer do discovery %q != configurado", ErrDiscoveryFailed, doc.Issuer)
	}
	if doc.JWKSURI == "" {
		return "", fmt.Errorf("%w: jwks_uri ausente", ErrDiscoveryFailed)
	}
	// O jwks_uri anunciado é a origem do material de chave: exigir https (loopback/flag
	// exceptuados) para que um discovery adulterado não aponte para JWKS em claro.
	if err := checkTransport(doc.JWKSURI, v.allowInsecure); err != nil {
		return "", err
	}
	return doc.JWKSURI, nil
}

// verifySignature verifica a assinatura do JWS segundo o alg, recusando qualquer
// incompatibilidade entre o alg e o tipo de chave (guarda de confusão de algoritmo).
func verifySignature(alg string, key crypto.PublicKey, signingInput, sig []byte) error {
	digest := sha256.Sum256(signingInput)
	switch alg {
	case "RS256":
		pub, ok := key.(*rsa.PublicKey)
		if !ok {
			return ErrAlgConfusion
		}
		if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
			return ErrSignatureInvalid
		}
		return nil
	case "PS256":
		pub, ok := key.(*rsa.PublicKey)
		if !ok {
			return ErrAlgConfusion
		}
		opts := &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthAuto, Hash: crypto.SHA256}
		if err := rsa.VerifyPSS(pub, crypto.SHA256, digest[:], sig, opts); err != nil {
			return ErrSignatureInvalid
		}
		return nil
	case "ES256":
		pub, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return ErrAlgConfusion
		}
		keyBytes := (pub.Curve.Params().BitSize + 7) / 8
		if len(sig) != 2*keyBytes {
			return ErrSignatureInvalid
		}
		r := new(big.Int).SetBytes(sig[:keyBytes])
		s := new(big.Int).SetBytes(sig[keyBytes:])
		if !ecdsa.Verify(pub, digest[:], r, s) {
			return ErrSignatureInvalid
		}
		return nil
	default:
		// Inalcançável (o gate de alg já filtrou), mas fail-closed por omissão.
		return ErrUnsupportedAlg
	}
}
