package attestation

import (
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"fmt"
	"math/big"

	"github.com/fxamacker/cbor/v2"
)

// Algoritmos COSE SUPORTADOS. Lista deliberadamente CURTA: cada algoritmo extra é superfície
// de ataque e mais uma forma de a mesma prova ser interpretada de duas maneiras. Tudo o resto
// ⇒ [ErrUnsupportedAlgorithm].
const (
	algES256 int64 = -7   // ECDSA P-256 + SHA-256
	algEdDSA int64 = -8   // Ed25519
	algRS256 int64 = -257 // RSASSA-PKCS1-v1_5 + SHA-256
)

// Tipos de chave COSE (kty).
const (
	ktyOKP int64 = 1 // Ed25519
	ktyEC2 int64 = 2 // ECDSA
	ktyRSA int64 = 3
)

// Curvas COSE (crv).
const (
	crvP256    int64 = 1
	crvEd25519 int64 = 6
)

// Limites do módulo RSA aceitável (2048..4096 bits). Abaixo é fraco; acima é convite a
// gastar CPU a verificar assinaturas de entrada não-confiável.
const (
	minRSAModulusBytes = 256
	maxRSAModulusBytes = 512
)

// coseKey é o COSE_Key com chaves inteiras (RFC 8152). Os parâmetros -1/-2/-3 mudam de
// significado conforme o kty, por isso ficam CRUS e são interpretados depois.
type coseKey struct {
	Kty    int64           `cbor:"1,keyasint"`
	Alg    int64           `cbor:"3,keyasint"`
	Param1 cbor.RawMessage `cbor:"-1,keyasint,omitempty"` // crv (EC2/OKP) | n (RSA)
	Param2 cbor.RawMessage `cbor:"-2,keyasint,omitempty"` // x   (EC2/OKP) | e (RSA)
	Param3 cbor.RawMessage `cbor:"-3,keyasint,omitempty"` // y   (EC2)
}

// parseCOSEPublicKey converte uma COSE_Key numa chave pública da stdlib, devolvendo também
// o algoritmo declarado. Valida tudo o que a stdlib não valida sozinha: curva suportada,
// dimensão dos escalares, PONTO EFECTIVAMENTE NA CURVA (via crypto/ecdh, que rejeita pontos
// inválidos), e dimensão do módulo RSA.
func parseCOSEPublicKey(raw []byte) (crypto.PublicKey, int64, error) {
	var k coseKey
	if err := decMode.Unmarshal(raw, &k); err != nil {
		return nil, 0, fmt.Errorf("%w: %v", ErrMalformedCOSEKey, err)
	}

	switch k.Kty {
	case ktyEC2:
		if k.Alg != algES256 {
			return nil, 0, fmt.Errorf("%w: EC2 alg %d", ErrUnsupportedAlgorithm, k.Alg)
		}
		crv, err := decodeInt(k.Param1)
		if err != nil || crv != crvP256 {
			return nil, 0, fmt.Errorf("%w: curva EC2 não suportada", ErrUnsupportedAlgorithm)
		}
		x, err := decodeBytes(k.Param2)
		if err != nil {
			return nil, 0, err
		}
		y, err := decodeBytes(k.Param3)
		if err != nil {
			return nil, 0, err
		}
		if len(x) != 32 || len(y) != 32 {
			return nil, 0, fmt.Errorf("%w: coordenadas P-256 com dimensão errada", ErrMalformedCOSEKey)
		}
		// crypto/ecdh valida que o ponto pertence à curva (e não é o infinito); um ponto
		// inválido aceite aqui seria uma verificação de assinatura sem significado.
		uncompressed := make([]byte, 0, 65)
		uncompressed = append(uncompressed, 0x04)
		uncompressed = append(uncompressed, x...)
		uncompressed = append(uncompressed, y...)
		if _, err := ecdh.P256().NewPublicKey(uncompressed); err != nil {
			return nil, 0, fmt.Errorf("%w: ponto EC2 inválido", ErrMalformedCOSEKey)
		}
		return &ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     new(big.Int).SetBytes(x),
			Y:     new(big.Int).SetBytes(y),
		}, algES256, nil

	case ktyRSA:
		if k.Alg != algRS256 {
			return nil, 0, fmt.Errorf("%w: RSA alg %d", ErrUnsupportedAlgorithm, k.Alg)
		}
		n, err := decodeBytes(k.Param1)
		if err != nil {
			return nil, 0, err
		}
		e, err := decodeBytes(k.Param2)
		if err != nil {
			return nil, 0, err
		}
		if len(n) < minRSAModulusBytes || len(n) > maxRSAModulusBytes {
			return nil, 0, fmt.Errorf("%w: módulo RSA de %d bytes fora de [%d,%d]", ErrMalformedCOSEKey, len(n), minRSAModulusBytes, maxRSAModulusBytes)
		}
		if len(e) == 0 || len(e) > 4 {
			return nil, 0, fmt.Errorf("%w: expoente RSA inválido", ErrMalformedCOSEKey)
		}
		exp := new(big.Int).SetBytes(e)
		if !exp.IsInt64() || exp.Int64() < 3 {
			return nil, 0, fmt.Errorf("%w: expoente RSA inválido", ErrMalformedCOSEKey)
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exp.Int64())}, algRS256, nil

	case ktyOKP:
		if k.Alg != algEdDSA {
			return nil, 0, fmt.Errorf("%w: OKP alg %d", ErrUnsupportedAlgorithm, k.Alg)
		}
		crv, err := decodeInt(k.Param1)
		if err != nil || crv != crvEd25519 {
			return nil, 0, fmt.Errorf("%w: curva OKP não suportada", ErrUnsupportedAlgorithm)
		}
		x, err := decodeBytes(k.Param2)
		if err != nil {
			return nil, 0, err
		}
		if len(x) != ed25519.PublicKeySize {
			return nil, 0, fmt.Errorf("%w: chave Ed25519 com dimensão errada", ErrMalformedCOSEKey)
		}
		return ed25519.PublicKey(x), algEdDSA, nil

	default:
		return nil, 0, fmt.Errorf("%w: kty %d", ErrUnsupportedAlgorithm, k.Kty)
	}
}

// ec2CoordinatesP256 extrai (x,y) de uma COSE_Key EC2 P-256 — necessário para reconstruir a
// chave no formato U2F legado (0x04‖x‖y).
func ec2CoordinatesP256(raw []byte) ([]byte, []byte, error) {
	pub, alg, err := parseCOSEPublicKey(raw)
	if err != nil {
		return nil, nil, err
	}
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok || alg != algES256 {
		return nil, nil, fmt.Errorf("%w: fido-u2f exige credencial EC2 P-256/ES256", ErrUnsupportedAlgorithm)
	}
	x := make([]byte, 32)
	y := make([]byte, 32)
	ec.X.FillBytes(x)
	ec.Y.FillBytes(y)
	return x, y, nil
}

// verifySignature verifica sig sobre msg com a chave e o algoritmo COSE dados. É o ÚNICO
// ponto de verificação de assinatura do pacote: um algoritmo não suportado, ou uma chave que
// não case com o algoritmo declarado, é recusado (nunca "assume-se" o algoritmo pela chave —
// seria confusão de algoritmo).
func verifySignature(pub crypto.PublicKey, alg int64, msg, sig []byte) error {
	if len(sig) == 0 {
		return ErrBadSignature
	}
	switch alg {
	case algES256:
		key, ok := pub.(*ecdsa.PublicKey)
		if !ok || key.Curve != elliptic.P256() {
			return fmt.Errorf("%w: ES256 com chave incompatível", ErrUnsupportedAlgorithm)
		}
		digest := sha256.Sum256(msg)
		if !ecdsa.VerifyASN1(key, digest[:], sig) {
			return ErrBadSignature
		}
		return nil
	case algRS256:
		key, ok := pub.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("%w: RS256 com chave incompatível", ErrUnsupportedAlgorithm)
		}
		if key.Size() < minRSAModulusBytes || key.Size() > maxRSAModulusBytes {
			return fmt.Errorf("%w: módulo RSA fora dos limites", ErrMalformedCOSEKey)
		}
		digest := sha256.Sum256(msg)
		if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
			return ErrBadSignature
		}
		return nil
	case algEdDSA:
		key, ok := pub.(ed25519.PublicKey)
		if !ok || len(key) != ed25519.PublicKeySize {
			return fmt.Errorf("%w: EdDSA com chave incompatível", ErrUnsupportedAlgorithm)
		}
		if !ed25519.Verify(key, msg, sig) {
			return ErrBadSignature
		}
		return nil
	default:
		return fmt.Errorf("%w: alg %d", ErrUnsupportedAlgorithm, alg)
	}
}

// decodeInt descodifica um item CBOR inteiro.
func decodeInt(raw cbor.RawMessage) (int64, error) {
	if len(raw) == 0 {
		return 0, fmt.Errorf("%w: parâmetro inteiro ausente", ErrMalformedCOSEKey)
	}
	var v int64
	if err := decMode.Unmarshal(raw, &v); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrMalformedCOSEKey, err)
	}
	return v, nil
}

// decodeBytes descodifica um item CBOR byte-string.
func decodeBytes(raw cbor.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: parâmetro de bytes ausente", ErrMalformedCOSEKey)
	}
	var v []byte
	if err := decMode.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedCOSEKey, err)
	}
	if len(v) == 0 {
		return nil, fmt.Errorf("%w: parâmetro de bytes vazio", ErrMalformedCOSEKey)
	}
	return v, nil
}
