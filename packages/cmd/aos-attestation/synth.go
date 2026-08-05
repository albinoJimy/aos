package main

// Síntese de attestations WebAuthn "packed" com cadeia x5c REAL, para provisionar/testar o
// componente de autoridade. Vive AQUI (componente externo, que já tem CBOR) e NUNCA no nó —
// é a face oposta do isolamento de dependências de ADR-017. Adaptado do sintetizador de
// referência de packages/platform/attestation (testsynth).

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// oidAAGUID (id-fido-gen-ce-aaguid) amarra o certificado de attestation ao AAGUID do modelo.
var oidAAGUID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 45724, 1, 1, 4}

const (
	flagUP = 0x01 // user present
	flagUV = 0x04 // user verified
	flagAT = 0x40 // attested credential data presente
)

// devCA é uma autoridade de attestation de dev (raiz auto-assinada). Em produção seria a raiz
// FIDO/organizacional cujos autenticadores são realmente certificados.
type devCA struct {
	cert *x509.Certificate
	der  []byte
	key  *ecdsa.PrivateKey
}

func newDevCA(cn string) (*devCA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &devCA{cert: cert, der: der, key: key}, nil
}

// issueLeaf emite o certificado FOLHA do autenticador, com a extensão de AAGUID.
func (c *devCA) issueLeaf(aaguid [16]byte) ([]byte, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	val, err := asn1.Marshal(aaguid[:])
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "AOS Dev Authenticator"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		ExtraExtensions:       []pkix.Extension{{Id: oidAAGUID, Value: val}},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, nil, err
	}
	return der, key, nil
}

func coseEC2(pub *ecdsa.PublicKey) ([]byte, error) {
	x := make([]byte, 32)
	y := make([]byte, 32)
	pub.X.FillBytes(x)
	pub.Y.FillBytes(y)
	return cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
}

func buildAuthData(rpID string, aaguid [16]byte, credID, credPubCOSE []byte) []byte {
	h := sha256.Sum256([]byte(rpID))
	out := make([]byte, 0, 160)
	out = append(out, h[:]...)
	out = append(out, byte(flagUP|flagUV|flagAT))
	out = append(out, 0, 0, 0, 0) // signCount
	out = append(out, aaguid[:]...)
	var cl [2]byte
	binary.BigEndian.PutUint16(cl[:], uint16(len(credID)))
	out = append(out, cl[:]...)
	out = append(out, credID...)
	out = append(out, credPubCOSE...)
	return out
}

func buildClientData(origin string, challenge []byte) ([]byte, error) {
	return json.Marshal(map[string]any{
		"type":        "webauthn.create",
		"challenge":   base64.RawURLEncoding.EncodeToString(challenge),
		"origin":      origin,
		"crossOrigin": false,
	})
}

// synthAttestation produz um attestationObject packed x5c VÁLIDO + o clientDataJSON, amarrados ao
// challenge dado (o desafio por-perna do four-eyes) — pronto a submeter ao verificador.
func synthAttestation(c *devCA, rpID, origin string, aaguid [16]byte, challenge []byte) (attObj, clientData []byte, err error) {
	credKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	cose, err := coseEC2(&credKey.PublicKey)
	if err != nil {
		return nil, nil, err
	}
	credID := make([]byte, 32)
	if _, err = rand.Read(credID); err != nil {
		return nil, nil, err
	}
	ad := buildAuthData(rpID, aaguid, credID, cose)
	clientData, err = buildClientData(origin, challenge)
	if err != nil {
		return nil, nil, err
	}
	cdh := sha256.Sum256(clientData)
	signed := append(append([]byte{}, ad...), cdh[:]...)
	leafDER, leafKey, err := c.issueLeaf(aaguid)
	if err != nil {
		return nil, nil, err
	}
	digest := sha256.Sum256(signed)
	sig, err := ecdsa.SignASN1(rand.Reader, leafKey, digest[:])
	if err != nil {
		return nil, nil, err
	}
	attStmt := map[string]any{"alg": -7, "sig": sig, "x5c": [][]byte{leafDER}}
	attObj, err = cbor.Marshal(map[string]any{"fmt": "packed", "authData": ad, "attStmt": attStmt})
	return attObj, clientData, err
}
