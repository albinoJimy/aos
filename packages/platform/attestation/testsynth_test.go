package attestation

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
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// Este ficheiro SINTETIZA attestations WebAuthn REAIS em teste: gera uma CA de attestation,
// emite um certificado de dispositivo com a extensão de AAGUID, monta o authData binário
// byte-a-byte e assina com criptografia verdadeira. Sem isto, os testes seriam vacuosos
// (fixtures opacas ou mocks a "confirmar" o que o código já faz). Aqui, quando um teste diz
// "assinatura adulterada ⇒ recusa", houve mesmo uma assinatura válida que se estragou.

const (
	testRPID   = "aos.example.org"
	testOrigin = "https://aos.example.org"
)

var (
	testAAGUID    = [16]byte{0x9c, 0x83, 0x5a, 0x11, 0x1e, 0x4f, 0x42, 0x0a, 0xb1, 0x2d, 0x77, 0x30, 0x51, 0x0e, 0xa3, 0x01}
	otherAAGUID   = [16]byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb}
	zeroAAGUIDVal = [16]byte{}
)

// testCA é uma autoridade de attestation sintética (raiz auto-assinada).
type testCA struct {
	cert *x509.Certificate
	der  []byte
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newTestCA(t *testing.T, cn string) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gerar chave da CA: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		// CRLSign: a CA de teste também emite CRLs (testes de revogação).
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("criar cert da CA: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse do cert da CA: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &testCA{cert: cert, der: der, key: key, pool: pool}
}

// issueAttestationCert emite um certificado FOLHA de attestation. certAAGUID != nil ⇒ inclui
// a extensão id-fido-gen-ce-aaguid com esse valor (é o que amarra o cert ao modelo).
func (ca *testCA) issueAttestationCert(t *testing.T, certAAGUID *[16]byte) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gerar chave do dispositivo: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "AOS Test Authenticator"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  false,
	}
	if certAAGUID != nil {
		val, err := asn1.Marshal(certAAGUID[:])
		if err != nil {
			t.Fatalf("codificar extensão de AAGUID: %v", err)
		}
		tmpl.ExtraExtensions = []pkix.Extension{{Id: oidAAGUID, Critical: false, Value: val}}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("emitir cert de attestation: %v", err)
	}
	return der, key
}

// coseEC2 constrói a COSE_Key (EC2/P-256/ES256) da credencial.
func coseEC2(t *testing.T, pub *ecdsa.PublicKey) []byte {
	t.Helper()
	x := make([]byte, 32)
	y := make([]byte, 32)
	pub.X.FillBytes(x)
	pub.Y.FillBytes(y)
	b, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	if err != nil {
		t.Fatalf("codificar COSE_Key: %v", err)
	}
	return b
}

// authDataOpts parametriza a construção do authenticator data.
type authDataOpts struct {
	rpID         string
	aaguid       [16]byte
	credID       []byte
	credPubCOSE  []byte
	userPresent  bool
	userVerified bool
	omitAT       bool
	signCount    uint32
}

// buildAuthData monta o authenticator data BINÁRIO exactamente como um autenticador o emite.
func buildAuthData(o authDataOpts) []byte {
	h := sha256.Sum256([]byte(o.rpID))
	var flags byte
	if o.userPresent {
		flags |= flagUP
	}
	if o.userVerified {
		flags |= flagUV
	}
	if !o.omitAT {
		flags |= flagAT
	}
	out := make([]byte, 0, 128)
	out = append(out, h[:]...)
	out = append(out, flags)
	var sc [4]byte
	binary.BigEndian.PutUint32(sc[:], o.signCount)
	out = append(out, sc[:]...)
	if !o.omitAT {
		out = append(out, o.aaguid[:]...)
		var cl [2]byte
		binary.BigEndian.PutUint16(cl[:], uint16(len(o.credID)))
		out = append(out, cl[:]...)
		out = append(out, o.credID...)
		out = append(out, o.credPubCOSE...)
	}
	return out
}

// buildClientData monta o clientDataJSON com o challenge em base64url sem padding.
func buildClientData(t *testing.T, typ, origin string, challenge []byte) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"type":        typ,
		"challenge":   base64.RawURLEncoding.EncodeToString(challenge),
		"origin":      origin,
		"crossOrigin": false,
	})
	if err != nil {
		t.Fatalf("codificar clientDataJSON: %v", err)
	}
	return b
}

// signECDSA assina msg (SHA-256) com a chave dada, em ASN.1 DER — a forma que o WebAuthn usa.
func signECDSA(t *testing.T, key *ecdsa.PrivateKey, msg []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(msg)
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatalf("assinar: %v", err)
	}
	return sig
}

func marshalAttObj(t *testing.T, fmtName string, attStmt any, authData []byte) []byte {
	t.Helper()
	m := map[string]any{"fmt": fmtName, "authData": authData}
	if attStmt != nil {
		m["attStmt"] = attStmt
	}
	b, err := cbor.Marshal(m)
	if err != nil {
		t.Fatalf("codificar attestationObject: %v", err)
	}
	return b
}

// synth é uma attestation sintetizada completa, pronta a submeter ao verificador.
type synth struct {
	attObj     []byte
	clientData []byte
	challenge  []byte
	aaguid     [16]byte
	credID     []byte
}

// packedOpts parametriza a síntese de uma attestation "packed".
type packedOpts struct {
	ca           *testCA
	aaguid       [16]byte // AAGUID declarado no authData
	certAAGUID   *[16]byte
	credID       []byte
	rpID         string
	origin       string
	clientType   string
	challenge    []byte
	userVerified bool
	omitUP       bool
	selfAttest   bool // sem x5c: assina com a chave da própria credencial
	tamperSig    bool
	// credPubOverride substitui a COSE_Key da credencial no authData por bytes arbitrários
	// (CBOR bem-formado, mas COSE inválida) — serve para provar que o caminho packed FULL
	// também valida a credentialPublicKey, e não só a chave do certificado.
	credPubOverride []byte
}

// newPackedAttestation sintetiza uma attestation "packed" completa e VÁLIDA (salvo o desvio
// pedido nas opções). Devolve também o material para asserções.
func newPackedAttestation(t *testing.T, o packedOpts) synth {
	t.Helper()
	if o.rpID == "" {
		o.rpID = testRPID
	}
	if o.origin == "" {
		o.origin = testOrigin
	}
	if o.clientType == "" {
		o.clientType = clientDataCreateType
	}
	if len(o.credID) == 0 {
		o.credID = randBytes(t, 32)
	}
	if len(o.challenge) == 0 {
		o.challenge = randBytes(t, 32)
	}

	credKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gerar chave da credencial: %v", err)
	}
	cose := coseEC2(t, &credKey.PublicKey)
	if len(o.credPubOverride) > 0 {
		cose = o.credPubOverride
	}

	ad := buildAuthData(authDataOpts{
		rpID:         o.rpID,
		aaguid:       o.aaguid,
		credID:       o.credID,
		credPubCOSE:  cose,
		userPresent:  !o.omitUP,
		userVerified: o.userVerified,
	})
	clientData := buildClientData(t, o.clientType, o.origin, o.challenge)
	cdh := sha256.Sum256(clientData)

	signed := append(append([]byte{}, ad...), cdh[:]...)

	attStmt := map[string]any{"alg": -7}
	if o.selfAttest {
		attStmt["sig"] = maybeTamper(signECDSA(t, credKey, signed), o.tamperSig)
	} else {
		leafDER, leafKey := o.ca.issueAttestationCert(t, o.certAAGUID)
		attStmt["sig"] = maybeTamper(signECDSA(t, leafKey, signed), o.tamperSig)
		attStmt["x5c"] = [][]byte{leafDER}
	}

	return synth{
		attObj:     marshalAttObj(t, "packed", attStmt, ad),
		clientData: clientData,
		challenge:  o.challenge,
		aaguid:     o.aaguid,
		credID:     o.credID,
	}
}

// newU2FAttestation sintetiza uma attestation legada "fido-u2f" (AAGUID nulo, por spec).
func newU2FAttestation(t *testing.T, ca *testCA) synth {
	t.Helper()
	credID := randBytes(t, 32)
	challenge := randBytes(t, 32)
	credKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gerar chave da credencial: %v", err)
	}
	cose := coseEC2(t, &credKey.PublicKey)
	ad := buildAuthData(authDataOpts{
		rpID:        testRPID,
		aaguid:      zeroAAGUIDVal,
		credID:      credID,
		credPubCOSE: cose,
		userPresent: true,
	})
	clientData := buildClientData(t, clientDataCreateType, testOrigin, challenge)
	cdh := sha256.Sum256(clientData)
	rpHash := sha256.Sum256([]byte(testRPID))

	x := make([]byte, 32)
	y := make([]byte, 32)
	credKey.PublicKey.X.FillBytes(x)
	credKey.PublicKey.Y.FillBytes(y)

	msg := []byte{0x00}
	msg = append(msg, rpHash[:]...)
	msg = append(msg, cdh[:]...)
	msg = append(msg, credID...)
	msg = append(msg, 0x04)
	msg = append(msg, x...)
	msg = append(msg, y...)

	leafDER, leafKey := ca.issueAttestationCert(t, nil)
	attStmt := map[string]any{"sig": signECDSA(t, leafKey, msg), "x5c": [][]byte{leafDER}}

	return synth{
		attObj:     marshalAttObj(t, "fido-u2f", attStmt, ad),
		clientData: clientData,
		challenge:  challenge,
		aaguid:     zeroAAGUIDVal,
		credID:     credID,
	}
}

func maybeTamper(sig []byte, tamper bool) []byte {
	if !tamper {
		return sig
	}
	out := append([]byte(nil), sig...)
	out[len(out)-1] ^= 0xff
	return out
}

func randBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return b
}

// newVerifier constrói um verificador com a política base dos testes (allowlist com o AAGUID
// de teste), aplicando as mutações pedidas.
func newVerifier(t *testing.T, ca *testCA, mutate ...func(*Config)) *Verifier {
	t.Helper()
	cfg := Config{
		RPID:           testRPID,
		AllowedOrigins: []string{testOrigin},
		AllowedAAGUIDs: [][16]byte{testAAGUID},
	}
	if ca != nil {
		cfg.Roots = ca.pool
	}
	for _, m := range mutate {
		m(&cfg)
	}
	v, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return v
}
