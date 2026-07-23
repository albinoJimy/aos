package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Este ficheiro prova a validação REAL de ID-tokens OIDC (AOS-174) OFFLINE: um IdP de
// teste (httptest) gera pares de chaves RSA e EC, serve JWKS + discovery e minta
// ID-tokens assinados. Cada caso assere a recusa/aceitação CONCRETA (não-vacuoso).

const (
	testAudience = "aos-node-client"
	rsaKid       = "rsa-key-1"
	ecKid        = "ec-key-1"
)

// fixedNow é o instante determinístico do relógio de teste.
func fixedNow() time.Time { return time.Unix(1_700_000_000, 0).UTC() }

func fixedClock() func() time.Time { return func() time.Time { return fixedNow() } }

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// testIDP é um Identity Provider OIDC de teste em memória.
type testIDP struct {
	server   *httptest.Server
	rsaKey   *rsa.PrivateKey
	ecKey    *ecdsa.PrivateKey
	jwksHits int32 // contador de fetches ao JWKS (para provar ausência de loop)
	failJWKS bool  // quando true, o endpoint JWKS devolve 503
}

func newTestIDP(t *testing.T) *testIDP {
	t.Helper()
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gerar RSA: %v", err)
	}
	ek, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gerar EC: %v", err)
	}
	idp := &testIDP{rsaKey: rk, ecKey: ek}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":   idp.server.URL,
			"jwks_uri": idp.server.URL + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&idp.jwksHits, 1)
		if idp.failJWKS {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(idp.jwksDoc())
	})
	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

func (idp *testIDP) issuer() string { return idp.server.URL }

// jwksDoc devolve o documento JWKS com as chaves públicas RSA e EC.
func (idp *testIDP) jwksDoc() map[string]any {
	pub := &idp.rsaKey.PublicKey
	eBytes := big.NewInt(int64(pub.E)).Bytes()
	rsaJWK := map[string]any{
		"kty": "RSA", "kid": rsaKid, "alg": "RS256", "use": "sig",
		"n": b64(pub.N.Bytes()), "e": b64(eBytes),
	}
	ecPub := &idp.ecKey.PublicKey
	ecJWK := map[string]any{
		"kty": "EC", "kid": ecKid, "alg": "ES256", "use": "sig", "crv": "P-256",
		"x": b64(pad32(ecPub.X.Bytes())), "y": b64(pad32(ecPub.Y.Bytes())),
	}
	return map[string]any{"keys": []any{rsaJWK, ecJWK}}
}

func pad32(b []byte) []byte {
	if len(b) >= 32 {
		return b
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

// validClaims devolve claims válidas para o IdP (relativas ao relógio fixo).
func (idp *testIDP) validClaims() map[string]any {
	now := fixedNow().Unix()
	return map[string]any{
		"iss":   idp.issuer(),
		"sub":   "alice@corp.example",
		"aud":   testAudience,
		"exp":   now + 3600,
		"iat":   now - 30,
		"nbf":   now - 30,
		"email": "alice@corp.example",
	}
}

// sign minta um JWS compacto. Para HS256 (ataque de confusão), secret é o segredo HMAC.
func (idp *testIDP) sign(t *testing.T, alg, kid string, claims map[string]any, hmacSecret []byte) string {
	t.Helper()
	hdr := map[string]any{"alg": alg, "typ": "JWT"}
	if kid != "" {
		hdr["kid"] = kid
	}
	hb, _ := json.Marshal(hdr)
	pb, _ := json.Marshal(claims)
	input := b64(hb) + "." + b64(pb)
	digest := sha256.Sum256([]byte(input))

	var sig []byte
	switch alg {
	case "RS256":
		s, err := rsa.SignPKCS1v15(rand.Reader, idp.rsaKey, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatalf("RS256 sign: %v", err)
		}
		sig = s
	case "PS256":
		s, err := rsa.SignPSS(rand.Reader, idp.rsaKey, crypto.SHA256, digest[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthAuto, Hash: crypto.SHA256})
		if err != nil {
			t.Fatalf("PS256 sign: %v", err)
		}
		sig = s
	case "ES256":
		r, s, err := ecdsa.Sign(rand.Reader, idp.ecKey, digest[:])
		if err != nil {
			t.Fatalf("ES256 sign: %v", err)
		}
		sig = append(pad32(r.Bytes()), pad32(s.Bytes())...)
	case "HS256":
		mac := hmac.New(sha256.New, hmacSecret)
		mac.Write([]byte(input))
		sig = mac.Sum(nil)
	default:
		t.Fatalf("alg de teste nao suportado: %s", alg)
	}
	return input + "." + b64(sig)
}

func (idp *testIDP) verifier(t *testing.T, cfg Config) *Verifier {
	t.Helper()
	if cfg.Issuer == "" {
		cfg.Issuer = idp.issuer()
	}
	if cfg.Audience == "" {
		cfg.Audience = testAudience
	}
	if cfg.Clock == nil {
		cfg.Clock = fixedClock()
	}
	cfg.HTTPClient = idp.server.Client()
	v, err := NewVerifier(cfg)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

// ---- Casos ----

// (1) ID-token VÁLIDO (RS256, via DISCOVERY) → sub verificado.
func TestValidate_ValidRS256_ViaDiscovery(t *testing.T) {
	idp := newTestIDP(t)
	v := idp.verifier(t, Config{}) // JWKSURI vazio ⇒ exercita discovery
	tok := idp.sign(t, "RS256", rsaKid, idp.validClaims(), nil)

	claims, err := v.Validate(context.Background(), tok)
	if err != nil {
		t.Fatalf("token valido recusado: %v", err)
	}
	if claims.Subject != "alice@corp.example" {
		t.Fatalf("sub=%q, quero alice@corp.example", claims.Subject)
	}
}

// (1b) ID-token VÁLIDO (ES256, via jwks_uri directo) → prova o caminho EC.
func TestValidate_ValidES256(t *testing.T) {
	idp := newTestIDP(t)
	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks"})
	tok := idp.sign(t, "ES256", ecKid, idp.validClaims(), nil)

	claims, err := v.Validate(context.Background(), tok)
	if err != nil {
		t.Fatalf("token ES256 valido recusado: %v", err)
	}
	if claims.Subject != "alice@corp.example" {
		t.Fatalf("sub=%q", claims.Subject)
	}
}

// (2) Assinatura ADULTERADA → ErrSignatureInvalid.
func TestValidate_TamperedSignature(t *testing.T) {
	idp := newTestIDP(t)
	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks"})
	tok := idp.sign(t, "RS256", rsaKid, idp.validClaims(), nil)

	parts := strings.Split(tok, ".")
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sig[len(sig)-1] ^= 0xFF // mantém base64 válido, corrompe a assinatura
	parts[2] = base64.RawURLEncoding.EncodeToString(sig)
	tampered := strings.Join(parts, ".")

	if _, err := v.Validate(context.Background(), tampered); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("erro=%v, quero ErrSignatureInvalid", err)
	}
}

// (3a) EXPIRADO → ErrTokenExpired.
func TestValidate_Expired(t *testing.T) {
	idp := newTestIDP(t)
	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks"})
	c := idp.validClaims()
	c["exp"] = fixedNow().Unix() - 3600
	tok := idp.sign(t, "RS256", rsaKid, c, nil)

	if _, err := v.Validate(context.Background(), tok); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("erro=%v, quero ErrTokenExpired", err)
	}
}

// (3b) NBF no FUTURO → ErrTokenNotYetValid.
func TestValidate_NotYetValid(t *testing.T) {
	idp := newTestIDP(t)
	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks"})
	c := idp.validClaims()
	c["nbf"] = fixedNow().Unix() + 3600
	c["exp"] = fixedNow().Unix() + 7200
	tok := idp.sign(t, "RS256", rsaKid, c, nil)

	if _, err := v.Validate(context.Background(), tok); !errors.Is(err, ErrTokenNotYetValid) {
		t.Fatalf("erro=%v, quero ErrTokenNotYetValid", err)
	}
}

// (4) AUD errada → ErrAudienceMismatch.
func TestValidate_WrongAudience(t *testing.T) {
	idp := newTestIDP(t)
	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks"})
	c := idp.validClaims()
	c["aud"] = "outro-client"
	tok := idp.sign(t, "RS256", rsaKid, c, nil)

	if _, err := v.Validate(context.Background(), tok); !errors.Is(err, ErrAudienceMismatch) {
		t.Fatalf("erro=%v, quero ErrAudienceMismatch", err)
	}
}

// (5) ISS errado → ErrIssuerMismatch.
func TestValidate_WrongIssuer(t *testing.T) {
	idp := newTestIDP(t)
	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks"})
	c := idp.validClaims()
	c["iss"] = "https://issuer.malicioso.example"
	tok := idp.sign(t, "RS256", rsaKid, c, nil)

	if _, err := v.Validate(context.Background(), tok); !errors.Is(err, ErrIssuerMismatch) {
		t.Fatalf("erro=%v, quero ErrIssuerMismatch", err)
	}
}

// (6) alg "none" → ErrUnsupportedAlg (com segmento de assinatura presente, para provar
// que a recusa é no gate de algoritmo, não por má-formação).
func TestValidate_AlgNone(t *testing.T) {
	idp := newTestIDP(t)
	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks"})
	hdr, _ := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	pb, _ := json.Marshal(idp.validClaims())
	tok := b64(hdr) + "." + b64(pb) + "." + b64([]byte("assinatura-fantasma"))

	if _, err := v.Validate(context.Background(), tok); !errors.Is(err, ErrUnsupportedAlg) {
		t.Fatalf("erro=%v, quero ErrUnsupportedAlg", err)
	}
}

// (7) Confusão RS256→HS256: assinar com HMAC usando a chave PÚBLICA como segredo,
// header alg=HS256 → ErrAlgConfusion (a chave assimétrica NUNCA é tratada como segredo).
func TestValidate_AlgConfusionHS256(t *testing.T) {
	idp := newTestIDP(t)
	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks"})
	// O segredo do atacante: os bytes da chave pública RSA (DER PKIX) — o material que
	// um verifier ingénuo teria à mão e usaria como segredo HMAC.
	pubDER, err := x509.MarshalPKIXPublicKey(&idp.rsaKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal pubkey: %v", err)
	}
	tok := idp.sign(t, "HS256", rsaKid, idp.validClaims(), pubDER)

	if _, err := v.Validate(context.Background(), tok); !errors.Is(err, ErrAlgConfusion) {
		t.Fatalf("erro=%v, quero ErrAlgConfusion", err)
	}
}

// (8) kid DESCONHECIDO → ErrUnknownKeyID e SEM loop de refresh (JWKS buscado só 1 vez).
func TestValidate_UnknownKid_NoRefreshLoop(t *testing.T) {
	idp := newTestIDP(t)
	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks"})
	tok := idp.sign(t, "RS256", "kid-inexistente", idp.validClaims(), nil)

	if _, err := v.Validate(context.Background(), tok); !errors.Is(err, ErrUnknownKeyID) {
		t.Fatalf("erro=%v, quero ErrUnknownKeyID", err)
	}
	if hits := atomic.LoadInt32(&idp.jwksHits); hits != 1 {
		t.Fatalf("JWKS buscado %d vezes, quero exactamente 1 (sem loop de refresh)", hits)
	}
}

// (9) JWKS INDISPONÍVEL → ErrJWKSUnavailable (fail-closed).
func TestValidate_JWKSUnavailable(t *testing.T) {
	idp := newTestIDP(t)
	idp.failJWKS = true
	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks"})
	tok := idp.sign(t, "RS256", rsaKid, idp.validClaims(), nil)

	if _, err := v.Validate(context.Background(), tok); !errors.Is(err, ErrJWKSUnavailable) {
		t.Fatalf("erro=%v, quero ErrJWKSUnavailable", err)
	}
}

// (extra) sub ausente → ErrNoSubject (sem subject não há humanID).
func TestValidate_NoSubject(t *testing.T) {
	idp := newTestIDP(t)
	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks"})
	c := idp.validClaims()
	delete(c, "sub")
	tok := idp.sign(t, "RS256", rsaKid, c, nil)

	if _, err := v.Validate(context.Background(), tok); !errors.Is(err, ErrNoSubject) {
		t.Fatalf("erro=%v, quero ErrNoSubject", err)
	}
}

// (extra) nonce exigido e divergente → ErrNonceMismatch.
func TestValidate_NonceMismatch(t *testing.T) {
	idp := newTestIDP(t)
	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks", Nonce: "esperado-abc"})
	c := idp.validClaims()
	c["nonce"] = "diferente-xyz"
	tok := idp.sign(t, "RS256", rsaKid, c, nil)

	if _, err := v.Validate(context.Background(), tok); !errors.Is(err, ErrNonceMismatch) {
		t.Fatalf("erro=%v, quero ErrNonceMismatch", err)
	}
}

// (extra) malformação → ErrMalformedToken.
func TestValidate_Malformed(t *testing.T) {
	idp := newTestIDP(t)
	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks"})
	if _, err := v.Validate(context.Background(), "so.duas"); !errors.Is(err, ErrMalformedToken) {
		t.Fatalf("erro=%v, quero ErrMalformedToken", err)
	}
}

// (extra) guardas de construção: sem issuer / sem audience.
func TestNewVerifier_FailClosed(t *testing.T) {
	if _, err := NewVerifier(Config{Audience: "x"}); !errors.Is(err, ErrNoIssuer) {
		t.Fatalf("erro=%v, quero ErrNoIssuer", err)
	}
	if _, err := NewVerifier(Config{Issuer: "x"}); !errors.Is(err, ErrNoAudience) {
		t.Fatalf("erro=%v, quero ErrNoAudience", err)
	}
}

// signHeader minta um JWS RS256 com um header JOSE arbitrário (para exercitar o header,
// ex.: 'crit'). Assinatura genuína sob a chave RSA do IdP.
func (idp *testIDP) signHeader(t *testing.T, hdr map[string]any, claims map[string]any) string {
	t.Helper()
	hb, _ := json.Marshal(hdr)
	pb, _ := json.Marshal(claims)
	input := b64(hb) + "." + b64(pb)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, idp.rsaKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("assinar: %v", err)
	}
	return input + "." + b64(sig)
}

// (10) Header com 'crit' (extensão crítica não suportada) → ErrCritUnsupported, MESMO
// com assinatura genuína (a recusa é de conformidade, não de assinatura).
func TestValidate_CritRejected(t *testing.T) {
	idp := newTestIDP(t)
	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks"})
	hdr := map[string]any{"alg": "RS256", "typ": "JWT", "kid": rsaKid, "crit": []string{"exp-ext"}, "exp-ext": 1}
	tok := idp.signHeader(t, hdr, idp.validClaims())

	if _, err := v.Validate(context.Background(), tok); !errors.Is(err, ErrCritUnsupported) {
		t.Fatalf("erro=%v, quero ErrCritUnsupported", err)
	}
}

// (11) Token multi-audience SEM azp → ErrAzpMismatch (OIDC Core 3.1.3.7).
func TestValidate_MultiAudNoAzp(t *testing.T) {
	idp := newTestIDP(t)
	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks"})
	c := idp.validClaims()
	c["aud"] = []string{testAudience, "outro-servico"}
	tok := idp.sign(t, "RS256", rsaKid, c, nil)

	if _, err := v.Validate(context.Background(), tok); !errors.Is(err, ErrAzpMismatch) {
		t.Fatalf("erro=%v, quero ErrAzpMismatch", err)
	}
}

// (11b) Token multi-audience com azp DE OUTRO client → ErrAzpMismatch.
func TestValidate_MultiAudWrongAzp(t *testing.T) {
	idp := newTestIDP(t)
	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks"})
	c := idp.validClaims()
	c["aud"] = []string{testAudience, "outro-servico"}
	c["azp"] = "outro-servico"
	tok := idp.sign(t, "RS256", rsaKid, c, nil)

	if _, err := v.Validate(context.Background(), tok); !errors.Is(err, ErrAzpMismatch) {
		t.Fatalf("erro=%v, quero ErrAzpMismatch", err)
	}
}

// (11c) Token multi-audience com azp==nosso client → ACEITE.
func TestValidate_MultiAudCorrectAzp(t *testing.T) {
	idp := newTestIDP(t)
	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks"})
	c := idp.validClaims()
	c["aud"] = []string{testAudience, "outro-servico"}
	c["azp"] = testAudience
	tok := idp.sign(t, "RS256", rsaKid, c, nil)

	if _, err := v.Validate(context.Background(), tok); err != nil {
		t.Fatalf("token multi-aud com azp correcto recusado: %v", err)
	}
}

// (12) MaxAge: iat mais antigo que a idade máxima → ErrTokenTooOld (mesmo dentro do exp).
func TestValidate_MaxAgeExceeded(t *testing.T) {
	idp := newTestIDP(t)
	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks", MaxAge: 2 * time.Minute})
	c := idp.validClaims()
	c["iat"] = fixedNow().Unix() - 3600 // muito mais velho que MaxAge, mas exp ainda no futuro
	tok := idp.sign(t, "RS256", rsaKid, c, nil)

	if _, err := v.Validate(context.Background(), tok); !errors.Is(err, ErrTokenTooOld) {
		t.Fatalf("erro=%v, quero ErrTokenTooOld", err)
	}
}

// (13) RequireJTI: token sem jti → ErrMissingJTI.
func TestValidate_RequireJTIMissing(t *testing.T) {
	idp := newTestIDP(t)
	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks", RequireJTI: true})
	tok := idp.sign(t, "RS256", rsaKid, idp.validClaims(), nil) // validClaims não traz jti

	if _, err := v.Validate(context.Background(), tok); !errors.Is(err, ErrMissingJTI) {
		t.Fatalf("erro=%v, quero ErrMissingJTI", err)
	}
}

// (14) Anti-replay: o MESMO ID-token (com jti) validado duas vezes → a 2ª é recusada
// com ErrTokenReplayed (single-use dentro da janela de validade). A 1ª tem de passar.
func TestValidate_ReplayDetected(t *testing.T) {
	idp := newTestIDP(t)
	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks"})
	c := idp.validClaims()
	c["jti"] = "id-token-unico-123"
	tok := idp.sign(t, "RS256", rsaKid, c, nil)

	if _, err := v.Validate(context.Background(), tok); err != nil {
		t.Fatalf("1ª validacao recusada: %v", err)
	}
	if _, err := v.Validate(context.Background(), tok); !errors.Is(err, ErrTokenReplayed) {
		t.Fatalf("erro=%v na 2ª validacao, quero ErrTokenReplayed", err)
	}
}

// (15) Transporte inseguro: issuer http NÃO-loopback → ErrInsecureTransport na
// construção; e o mesmo com AllowInsecureTransport é aceite.
func TestNewVerifier_InsecureTransport(t *testing.T) {
	if _, err := NewVerifier(Config{Issuer: "http://idp.example.com", Audience: "x"}); !errors.Is(err, ErrInsecureTransport) {
		t.Fatalf("erro=%v, quero ErrInsecureTransport", err)
	}
	// JWKSURI http não-loopback também recusa.
	if _, err := NewVerifier(Config{Issuer: "https://idp.example.com", Audience: "x", JWKSURI: "http://keys.example.com/jwks"}); !errors.Is(err, ErrInsecureTransport) {
		t.Fatalf("erro=%v (jwks http), quero ErrInsecureTransport", err)
	}
	// https é aceite.
	if _, err := NewVerifier(Config{Issuer: "https://idp.example.com", Audience: "x"}); err != nil {
		t.Fatalf("issuer https recusado: %v", err)
	}
	// Loopback http é aceite (dev/testes) sem flag.
	if _, err := NewVerifier(Config{Issuer: "http://127.0.0.1:8080", Audience: "x"}); err != nil {
		t.Fatalf("issuer loopback recusado: %v", err)
	}
	// AllowInsecureTransport permite http não-loopback explicitamente.
	if _, err := NewVerifier(Config{Issuer: "http://idp.example.com", Audience: "x", AllowInsecureTransport: true}); err != nil {
		t.Fatalf("AllowInsecureTransport nao honrado: %v", err)
	}
}

// (16) Anti-replay concorrente: N goroutines apresentam o MESMO token (com jti) em
// paralelo; exactamente UMA passa, as restantes ErrTokenReplayed. Prova a atomicidade
// do check-and-record sob -race.
func TestValidate_ReplayConcurrent(t *testing.T) {
	idp := newTestIDP(t)
	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks"})
	c := idp.validClaims()
	c["jti"] = "concorrente-xyz"
	tok := idp.sign(t, "RS256", rsaKid, c, nil)
	// Aquece a cache de JWKS para que a corrida seja sobre o anti-replay, não a I/O.
	if _, err := v.Validate(context.Background(), tok); err != nil {
		t.Fatalf("aquecimento recusado: %v", err)
	}

	const n = 16
	results := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := v.Validate(context.Background(), tok)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	replays := 0
	for err := range results {
		if errors.Is(err, ErrTokenReplayed) {
			replays++
		} else if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		} else {
			t.Fatalf("uma 2ª+ apresentacao passou (esperado replay)")
		}
	}
	if replays != n {
		t.Fatalf("replays=%d, quero %d (a 1ª ja consumiu o jti no aquecimento)", replays, n)
	}
}
