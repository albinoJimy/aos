package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
)

// Constantes do envelope do token NHI. O formato é um JWS compacto:
//
//	base64url(header) "." base64url(claims) "." base64url(assinatura)
//
// assinado com EdDSA (ed25519). É deliberadamente um subconjunto mínimo de JWS,
// implementado só com a stdlib — sem bibliotecas JWT externas (ADR de zero-deps).
const (
	// algEdDSA é o ÚNICO algoritmo aceite. Qualquer outro valor (incluindo "none")
	// é rejeitado por [Verifier.Verify] com [ErrUnsupportedAlg].
	algEdDSA = "EdDSA"
	// typNHI marca o token como uma identidade não-humana do AOS.
	typNHI = "NHI"
)

// header é o cabeçalho JOSE do token.
type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid,omitempty"`
}

// Claims é o conjunto de asserções do token NHI. Codifica o par (utilizador,
// agente), a classe/política e o escopo scoped/time-bound (AOS-005).
type Claims struct {
	// UserID é o humano responsável na raiz da cadeia de delegação.
	UserID string `json:"user_id"`
	// AgentID é a identidade única do agente (a NHI).
	AgentID string `json:"agent_id"`
	// AgentClass é a classe sob cuja política o agente actua.
	AgentClass string `json:"agent_class"`
	// PolicyRef aponta a política (policy_ref) aplicável (AOS-004).
	PolicyRef string `json:"policy_ref,omitempty"`
	// Scope são as capabilities/recursos concedidos (autoridade = utilizador ∩
	// classe, e ⊆ pai em on-behalf-of).
	Scope []string `json:"scope"`
	// Issuer (iss) identifica o emissor; a verificação usa a sua chave pública.
	Issuer string `json:"iss"`
	// IssuedAt (iat), NotBefore (nbf) e Expiry (exp) são segundos Unix. O TTL
	// curto (exp-iat) minimiza a janela de revogação.
	IssuedAt  int64 `json:"iat"`
	NotBefore int64 `json:"nbf"`
	Expiry    int64 `json:"exp"`
	// JTI é o identificador único do token (usado na revogação).
	JTI string `json:"jti"`
}

func b64enc(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func b64dec(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

// signToken serializa header+claims e produz o JWS compacto assinado com priv.
func signToken(priv ed25519.PrivateKey, kid string, claims Claims) (string, error) {
	hb, err := json.Marshal(header{Alg: algEdDSA, Typ: typNHI, Kid: kid})
	if err != nil {
		return "", err
	}
	pb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := b64enc(hb) + "." + b64enc(pb)
	sig := ed25519.Sign(priv, []byte(signingInput))
	return signingInput + "." + b64enc(sig), nil
}

// parsedToken é o resultado de descodificar (SEM verificar) um token compacto.
type parsedToken struct {
	header       header
	claims       Claims
	signature    []byte
	signingInput string // header_b64 + "." + claims_b64 (o que foi assinado)
}

// parseCompact descodifica os três segmentos do token sem validar a assinatura,
// a janela temporal ou a revogação. A validação é responsabilidade de
// [Verifier.Verify]; separá-la mantém o parsing testável e o verificador
// auditável.
func parseCompact(compact string) (parsedToken, error) {
	parts := strings.Split(compact, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return parsedToken{}, ErrTokenMalformed
	}
	hb, err := b64dec(parts[0])
	if err != nil {
		return parsedToken{}, ErrTokenMalformed
	}
	var h header
	if err := json.Unmarshal(hb, &h); err != nil {
		return parsedToken{}, ErrTokenMalformed
	}
	pb, err := b64dec(parts[1])
	if err != nil {
		return parsedToken{}, ErrTokenMalformed
	}
	var c Claims
	if err := json.Unmarshal(pb, &c); err != nil {
		return parsedToken{}, ErrTokenMalformed
	}
	sig, err := b64dec(parts[2])
	if err != nil {
		return parsedToken{}, ErrTokenMalformed
	}
	return parsedToken{
		header:       h,
		claims:       c,
		signature:    sig,
		signingInput: parts[0] + "." + parts[1],
	}, nil
}

// Token é o resultado de uma emissão: a string bearer compacta e as claims que
// nela vão embutidas (para conveniência do chamador; a fonte de verdade é o
// Compact assinado).
type Token struct {
	// Compact é a string bearer a apresentar ao Reference Monitor (Call.Credential).
	Compact string
	// Claims são as asserções embutidas (cópia de conveniência).
	Claims Claims
}
