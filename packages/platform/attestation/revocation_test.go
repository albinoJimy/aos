package attestation

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// REMEDIAÇÃO AOS-177 — os eixos que faltavam ao verificador: REVOGAÇÃO (denylist de modelos,
// denylist de séries, CRL), FORÇA DE PROVA (self-attestation na porta de dual-control) e
// DESACOPLAMENTO do legado U2F da allowlist de modelos. Todos os testes são NÃO-VACUOSOS: a
// MESMA prova que é recusada com o canal ligado é ACEITE com ele desligado.

// (1) DENYLIST DE MODELOS: um AAGUID revogado é recusado MESMO estando na allowlist — é o
// canal de revogação de modelo (chave de lote extraída) que não existia.
func TestRevocation_AAGUIDDenylistBeatsAllowlist(t *testing.T) {
	ca := newTestCA(t, "AOS Test Attestation Root")
	s := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: &testAAGUID})

	// Contraprova: sem denylist, esta prova é ACEITE.
	if _, err := newVerifier(t, ca).Verify(context.Background(), s.attObj, s.clientData, s.challenge); err != nil {
		t.Fatalf("prova base devia ser aceite (não-vacuosidade), veio: %v", err)
	}

	revoked := newVerifier(t, ca, func(c *Config) { c.RevokedAAGUIDs = [][16]byte{testAAGUID} })
	if _, err := revoked.Verify(context.Background(), s.attObj, s.clientData, s.challenge); !errors.Is(err, ErrAAGUIDRevoked) {
		t.Fatalf("AAGUID revogado devia dar ErrAAGUIDRevoked, veio: %v", err)
	}
	// A porta (dual-control) tem de negar pela mesma via.
	if _, err := revoked.VerifyDeviceAttestation(context.Background(), s.attObj, s.clientData, s.challenge); !errors.Is(err, ErrAAGUIDRevoked) {
		t.Fatalf("a porta devia propagar ErrAAGUIDRevoked, veio: %v", err)
	}
}

// (2) DENYLIST DE SÉRIES: o certificado de attestation revogado não passa, mesmo dentro da
// validade e com cadeia que encadeia até à âncora. A forma do número de série é normalizada
// (0x, ":", maiúsculas, zeros à esquerda) para não falhar por cosmética.
func TestRevocation_CertSerialDenylist(t *testing.T) {
	ca := newTestCA(t, "AOS Test Attestation Root")
	s := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: &testAAGUID})

	for _, form := range []string{"2", "02", "0x02", "0X2", "0:2"} {
		v := newVerifier(t, ca, func(c *Config) { c.RevokedCertSerials = []string{form} })
		if _, err := v.Verify(context.Background(), s.attObj, s.clientData, s.challenge); !errors.Is(err, ErrCertificateRevoked) {
			t.Fatalf("série revogada na forma %q devia dar ErrCertificateRevoked, veio: %v", form, err)
		}
	}

	// Contraprova: outra série (não a do certificado) não revoga nada.
	other := newVerifier(t, ca, func(c *Config) { c.RevokedCertSerials = []string{"deadbeef"} })
	if _, err := other.Verify(context.Background(), s.attObj, s.clientData, s.challenge); err != nil {
		t.Fatalf("série NÃO revogada não devia afectar a verificação, veio: %v", err)
	}

	// Config com série ilegível ⇒ o verificador não chega a existir (uma revogação que não
	// revoga nada é pior do que nenhuma).
	if _, err := New(Config{
		RPID:               testRPID,
		AllowedOrigins:     []string{testOrigin},
		AllowedAAGUIDs:     [][16]byte{testAAGUID},
		Roots:              ca.pool,
		RevokedCertSerials: []string{"não-é-hex"},
	}); !errors.Is(err, ErrConfigRevokedSerial) {
		t.Fatalf("série ilegível devia dar ErrConfigRevokedSerial, veio: %v", err)
	}
}

// (3) CRL: uma lista de revogação REAL (emitida e parseada com crypto/x509) que liste a série
// da folha revoga-a. Sem a CRL, a mesma prova passa.
func TestRevocation_CRL(t *testing.T) {
	ca := newTestCA(t, "AOS Test Attestation Root")
	s := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: &testAAGUID})

	crlDER, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{
		Number:     big.NewInt(1),
		ThisUpdate: time.Now().Add(-time.Hour),
		NextUpdate: time.Now().Add(24 * time.Hour),
		RevokedCertificateEntries: []x509.RevocationListEntry{
			{SerialNumber: big.NewInt(2), RevocationTime: time.Now().Add(-time.Minute)},
		},
	}, ca.cert, ca.key)
	if err != nil {
		t.Fatalf("emitir CRL: %v", err)
	}
	crl, err := x509.ParseRevocationList(crlDER)
	if err != nil {
		t.Fatalf("parsear CRL: %v", err)
	}

	v := newVerifier(t, ca, func(c *Config) { c.CRLs = []*x509.RevocationList{crl} })
	if _, err := v.Verify(context.Background(), s.attObj, s.clientData, s.challenge); !errors.Is(err, ErrCertificateRevoked) {
		t.Fatalf("certificado listado na CRL devia dar ErrCertificateRevoked, veio: %v", err)
	}

	// CRL VAZIA (mesmo emissor, sem entradas) ⇒ não revoga: a regra é por série, não por
	// presença de CRL.
	emptyDER, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{
		Number:     big.NewInt(2),
		ThisUpdate: time.Now().Add(-time.Hour),
		NextUpdate: time.Now().Add(24 * time.Hour),
	}, ca.cert, ca.key)
	if err != nil {
		t.Fatalf("emitir CRL vazia: %v", err)
	}
	emptyCRL, err := x509.ParseRevocationList(emptyDER)
	if err != nil {
		t.Fatalf("parsear CRL vazia: %v", err)
	}
	v2 := newVerifier(t, ca, func(c *Config) { c.CRLs = []*x509.RevocationList{emptyCRL} })
	if _, err := v2.Verify(context.Background(), s.attObj, s.clientData, s.challenge); err != nil {
		t.Fatalf("CRL sem entradas não devia revogar, veio: %v", err)
	}
	// Uma entrada nil na lista de CRLs não pode causar panic (entrada de config defensiva).
	v3 := newVerifier(t, ca, func(c *Config) { c.CRLs = []*x509.RevocationList{nil} })
	if _, err := v3.Verify(context.Background(), s.attObj, s.clientData, s.challenge); err != nil {
		t.Fatalf("CRL nil devia ser ignorada sem panic, veio: %v", err)
	}
}

// (4) FORÇA DE PROVA: a self-attestation é aceitável por Verify (auditoria, com
// SelfAttested=true) mas NUNCA pela porta consumida pelo dual-control — que só transporta um
// deviceID e não conseguiria distinguir uma prova AUTO-DECLARADA de uma certificada.
func TestSelfAttestation_RejectedByDualControlPort(t *testing.T) {
	ca := newTestCA(t, "AOS Test Attestation Root")
	s := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, selfAttest: true})

	v := newVerifier(t, ca, func(c *Config) {
		c.AllowSelfAttestation = true
		c.SelfAttestationAcknowledged = true
	})

	att, err := v.Verify(context.Background(), s.attObj, s.clientData, s.challenge)
	if err != nil || !att.SelfAttested {
		t.Fatalf("Verify devia aceitar e marcar SelfAttested (att=%+v err=%v)", att, err)
	}
	if _, err := v.VerifyDeviceAttestation(context.Background(), s.attObj, s.clientData, s.challenge); !errors.Is(err, ErrSelfAttestationNotAcceptedByPort) {
		t.Fatalf("a porta devia recusar prova self-attested, veio: %v", err)
	}

	// Contraprova: a MESMA porta aceita uma prova CERTIFICADA (com cadeia x5c).
	full := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: &testAAGUID})
	if id, err := v.VerifyDeviceAttestation(context.Background(), full.attObj, full.clientData, full.challenge); err != nil || len(id) == 0 {
		t.Fatalf("prova certificada devia passar na porta (id=%d bytes err=%v)", len(id), err)
	}
}

// (5) DESACOPLAMENTO U2F↔allowlist: o AAGUID nulo NUNCA é aceitável no caminho packed, mesmo
// estando na allowlist para admitir o legado. Sem isto, admitir U2F desligava de facto o
// filtro de MODELO no caminho moderno.
func TestZeroAAGUID_RejectedInPackedPath(t *testing.T) {
	ca := newTestCA(t, "AOS Test Attestation Root")
	s := newPackedAttestation(t, packedOpts{ca: ca, aaguid: zeroAAGUIDVal, certAAGUID: &zeroAAGUIDVal})

	v := newVerifier(t, ca, func(c *Config) {
		// A configuração "permissiva" que um operador faria para aceitar U2F.
		c.AllowedAAGUIDs = [][16]byte{testAAGUID, zeroAAGUIDVal}
		c.AllowU2FLegacy = true
	})
	if _, err := v.Verify(context.Background(), s.attObj, s.clientData, s.challenge); !errors.Is(err, ErrZeroAAGUIDPacked) {
		t.Fatalf("packed com AAGUID nulo devia dar ErrZeroAAGUIDPacked, veio: %v", err)
	}

	// Contraprova: a mesma configuração continua a aceitar o legado fido-u2f de verdade.
	u2f := newU2FAttestation(t, ca)
	if _, err := v.Verify(context.Background(), u2f.attObj, u2f.clientData, u2f.challenge); err != nil {
		t.Fatalf("fido-u2f devia continuar a ser aceite com o opt-in, veio: %v", err)
	}
}

// (6) A credentialPublicKey é VALIDADA também no caminho packed FULL (onde a assinatura é
// verificada com a chave do certificado). Antes, uma COSE_Key inválida atravessava a
// verificação sem ser notada.
func TestPackedFull_ValidatesCredentialPublicKey(t *testing.T) {
	ca := newTestCA(t, "AOS Test Attestation Root")

	// CBOR bem-formado (passa o parse do authData) mas COSE inutilizável: kty desconhecido.
	badCOSE, err := cbor.Marshal(map[int]any{1: 42, 3: -7})
	if err != nil {
		t.Fatalf("codificar COSE inválida: %v", err)
	}
	s := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: &testAAGUID, credPubOverride: badCOSE})

	v := newVerifier(t, ca)
	_, verr := v.Verify(context.Background(), s.attObj, s.clientData, s.challenge)
	if !errors.Is(verr, ErrMalformedCOSEKey) && !errors.Is(verr, ErrUnsupportedAlgorithm) {
		t.Fatalf("credentialPublicKey inválida devia recusar (COSE malformada/alg não suportado), veio: %v", verr)
	}
}
