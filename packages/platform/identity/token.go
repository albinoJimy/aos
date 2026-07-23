package identity

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/aos-ref/platform/identity/delegation"
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
	// DelegationChain é a cadeia de delegação on-behalf-of embebida (AOS-006):
	// ordenada da raiz humana ("human:<user_id>") até ao agente actual. Vai SELADA
	// pela assinatura do token (o emissor assina a cadeia inteira). O verificador
	// valida-a (não-escalada, raiz humana, encadeamento de hash). Ver o subpacote
	// delegation. OBRIGATÓRIA a partir de AOS-006: um token sem cadeia é rejeitado
	// fail-closed por [Verifier.Verify] (ErrDelegationInvalid/E_CHAIN_EMPTY) — a
	// tag omitempty é apenas o encoding on-the-wire, não indica tolerância a
	// tokens legados sem cadeia.
	DelegationChain delegation.Chain `json:"delegation_chain,omitempty"`
}

func b64enc(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func b64dec(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

// signToken serializa header+claims e produz o JWS compacto assinado ATRAVÉS de um
// [crypto.Signer]. O Signer é a FRONTEIRA de custódia de chave: a chave privada pode
// viver in-process (via de referência — ed25519.PrivateKey já implementa crypto.Signer)
// ou num HSM/KMS externo (um adaptador do fornecedor implementa Public()+Sign; a chave
// nunca sai do HSM). O Issuer nunca precisa dos bytes crus da chave.
//
// Ed25519 assina a MENSAGEM directamente (sem pré-hash): opts = crypto.Hash(0). O
// rand.Reader é ignorado pelo ed25519 (a assinatura é determinística) mas é o contrato
// crypto.Signer que os SDKs de HSM/KMS honram.
func signToken(signer crypto.Signer, kid string, claims Claims) (string, error) {
	hb, err := json.Marshal(header{Alg: algEdDSA, Typ: typNHI, Kid: kid})
	if err != nil {
		return "", err
	}
	pb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := b64enc(hb) + "." + b64enc(pb)
	sig, err := signer.Sign(rand.Reader, []byte(signingInput), crypto.Hash(0))
	if err != nil {
		return "", err
	}
	// Fail-closed (forma): um signer que devolva uma assinatura de tamanho errado
	// (double buggy, adaptador HSM mal-configurado, algoritmo trocado) NUNCA produz um
	// token. Recusamos já na origem — nenhum bearer inválido chega a existir.
	if len(sig) != ed25519.SignatureSize {
		return "", ErrInvalidSigner
	}
	// Fail-closed (criptográfico) na ORIGEM: auto-verificar a assinatura contra a
	// pubkey que o signer reporta. O gate de tamanho acima só prova FORMA (64 bytes);
	// não prova que a assinatura VALIDA. Precisamente os modos de falha que a fronteira
	// HSM/KMS aberta por AOS-175 torna prováveis — um adaptador em Ed25519ph (pré-hash
	// SHA-512), um handle de chave errado (Sign com uma chave != à que Public() reporta),
	// ou opts ignoradas — produzem 64 bytes que NÃO validam. Sem esta verificação esse
	// token inválido seria devolvido ao chamador e só o verifier downstream o rejeitaria
	// (falha longe da causa). Aqui recusa-se fail-closed com ErrInvalidSigner: nenhum
	// bearer que a própria pubkey do issuer não verifique é alguma vez emitido. Custo: um
	// ed25519.Verify (~µs). A pubkey é obtida de forma panic-safe (ver signerPublicKey):
	// um signer patológico devolve o sentinela, nunca derruba o processo.
	pub, perr := signerPublicKey(signer)
	if perr != nil {
		return "", perr
	}
	if !ed25519.Verify(pub, []byte(signingInput), sig) {
		return "", ErrInvalidSigner
	}
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
