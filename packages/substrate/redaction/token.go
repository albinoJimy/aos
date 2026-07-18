package redaction

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// tokenPrefix identifica um token de PII deste motor. O formato é
// tok:<titular>:<classe>:<opaco>, em que <opaco> = hex(nonce) || hex(ciphertext) do
// valor cifrado por AES-256-GCM sob a chave POR-TITULAR. O opaco não revela o VALOR
// em claro (o ciphertext GCM é indistinguível sem a chave), mas — como o GCM preserva
// o comprimento — o tamanho do token É proporcional ao comprimento da PII original
// (um oráculo de comprimento residual). A classe da PII já é explícita no token, e o
// comprimento raramente é sensível por si só; se o for, a mitigação é padding do
// plaintext antes de cifrar (não feito por omissão para manter o token compacto).
const tokenPrefix = "tok"

// keySize é o tamanho exigido da chave por-titular: AES-256, igual ao kekSize do
// audit (AOS-083). Uma chave de outro tamanho é rejeitada (fail-closed, [ErrKeySize]).
const keySize = 32

// KeySource é a PORTA das chaves POR-TITULAR usadas para tokenizar. O motor NÃO gere
// o cofre: pede a chave do titular a esta porta e produz o token + a KeyRef. Em
// produção liga-se o KeyVault do audit (AOS-083) ou um KMS/HSM; apagar a chave nesse
// cofre (crypto-shredding, AOS-093) torna o token IRRESOLÚVEL sem alterar o motor.
//
// A chave devolvida tem de ter 32 bytes (AES-256). A KeyRef é a referência estável
// da chave (derivável do titular), gravada no [TokenRef] para o índice/audit.
type KeySource interface {
	KeyFor(subject string) (key []byte, keyRef string, err error)
}

// TokenRef descreve um token produzido: a que classe e titular pertence, que chave o
// resolve (KeyRef) e o próprio token. É devolvido por [Engine.Redact] para o audit
// indexar quem tem PII tokenizada onde (prepara o shred por-titular de AOS-093). Não
// contém o valor em claro.
type TokenRef struct {
	Class   Class
	Subject string
	KeyRef  string
	Token   string
}

// aad liga o ciphertext ao par (titular, classe): o GCM autentica-o como dados
// associados, pelo que um token não pode ser reinterpretado sob outro titular/classe.
func aad(subject string, class Class) []byte {
	return []byte(subject + "|" + string(class))
}

// tokenize cifra value sob a chave do titular e devolve o token e o [TokenRef]. O
// nonce é DETERMINÍSTICO — HMAC-SHA256(chave, classe||value) truncado — pelo que o
// mesmo (valor, titular, classe) produz SEMPRE o mesmo token (estabilidade exigida
// pela utilidade/reprodutibilidade), mantendo-se a autenticação do GCM. Fail-closed:
// titular vazio ⇒ [ErrEmptySubject]; chave de tamanho errado ⇒ [ErrKeySize].
func tokenize(keys KeySource, subject string, class Class, value string) (string, TokenRef, error) {
	if strings.TrimSpace(subject) == "" {
		return "", TokenRef{}, ErrEmptySubject
	}
	key, keyRef, err := keys.KeyFor(subject)
	if err != nil {
		return "", TokenRef{}, err
	}
	if len(key) != keySize {
		return "", TokenRef{}, ErrKeySize
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", TokenRef{}, err
	}
	nonce := deterministicNonce(key, class, value, gcm.NonceSize())
	ct := gcm.Seal(nil, nonce, []byte(value), aad(subject, class))
	opaque := hex.EncodeToString(nonce) + hex.EncodeToString(ct)
	token := strings.Join([]string{tokenPrefix, subject, string(class), opaque}, ":")
	return token, TokenRef{Class: class, Subject: subject, KeyRef: keyRef, Token: token}, nil
}

// deterministicNonce deriva um nonce estável de (chave, classe, valor). Como a chave
// é secreta e por-titular, o nonce não vaza o valor; como é determinístico, o token
// é estável para o mesmo input (síntese SIV-like: seguro sob chave por-titular para
// os volumes deste motor de ingestão).
func deterministicNonce(key []byte, class Class, value string, n int) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(string(class)))
	mac.Write([]byte{0})
	mac.Write([]byte(value))
	return mac.Sum(nil)[:n]
}

// Resolve inverte um token com a chave do titular: devolve o valor em claro e ok=true
// se a chave o decifra. Fail-closed — token malformado, chave errada, ou chave
// AUSENTE (shredded) ⇒ ok=false. É por aqui que o crypto-shredding se manifesta:
// destruída a chave no vault, o token torna-se permanentemente irresolúvel.
func Resolve(token string, key []byte) (string, bool) {
	parts := strings.SplitN(token, ":", 4)
	if len(parts) != 4 || parts[0] != tokenPrefix {
		return "", false
	}
	subject, class, opaque := parts[1], Class(parts[2]), parts[3]
	if len(key) != keySize {
		return "", false
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", false
	}
	raw, err := hex.DecodeString(opaque)
	if err != nil {
		return "", false
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return "", false
	}
	nonce, ct := raw[:ns], raw[ns:]
	pt, err := gcm.Open(nil, nonce, ct, aad(subject, class))
	if err != nil {
		return "", false
	}
	return string(pt), true
}

// newGCM constrói um AEAD AES-256-GCM a partir de uma chave de 32 bytes.
func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
