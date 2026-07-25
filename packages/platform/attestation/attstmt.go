package attestation

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/asn1"
	"encoding/hex"
	"fmt"
)

// oidAAGUID é a extensão de certificado que declara o AAGUID do MODELO que o certificado de
// attestation cobre (id-fido-gen-ce-aaguid, 1.3.6.1.4.1.45724.1.1.4). É a amarra entre o que
// a CA certificou e o que o authData afirma: sem a conferir, um certificado legítimo de um
// modelo permitido poderia acompanhar um authData a declarar OUTRO AAGUID (p.ex. um modelo
// da allowlist) e passar.
var oidAAGUID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 45724, 1, 1, 4}

// packedAttStmt é o attStmt do formato "packed" (WebAuthn §8.2). x5c ausente ⇒
// self-attestation.
type packedAttStmt struct {
	Alg int64    `cbor:"alg"`
	Sig []byte   `cbor:"sig"`
	X5C [][]byte `cbor:"x5c,omitempty"`
}

// u2fAttStmt é o attStmt do formato legado "fido-u2f" (WebAuthn §8.6): exactamente um
// certificado e uma assinatura ECDSA P-256.
type u2fAttStmt struct {
	Sig []byte   `cbor:"sig"`
	X5C [][]byte `cbor:"x5c"`
}

// verifyPacked verifica o formato "packed". Devolve selfAttested=true quando a prova foi
// self-attestation (sem cadeia). Duas variantes:
//
//   - FULL (com x5c): a assinatura cobre authData ‖ clientDataHash e é verificada com a
//     chave pública do certificado FOLHA; a cadeia tem de encadear até uma âncora
//     configurada; a folha não pode ser CA; e a extensão de AAGUID (quando exigida/presente)
//     tem de coincidir com o AAGUID do authData.
//   - SELF (sem x5c): a mesma mensagem é verificada com a PRÓPRIA credentialPublicKey, e o
//     alg declarado tem de ser o da credencial. Prova posse da chave, NÃO prova modelo —
//     por isso exige opt-in ([Config.AllowSelfAttestation]).
func (v *Verifier) verifyPacked(rawAttStmt []byte, ad *authData, clientDataHash []byte) (bool, error) {
	var st packedAttStmt
	if err := decMode.Unmarshal(rawAttStmt, &st); err != nil {
		return false, fmt.Errorf("%w: attStmt packed: %v", ErrMalformedAttestation, err)
	}
	if len(st.Sig) == 0 {
		return false, fmt.Errorf("%w: attStmt packed sem sig", ErrMalformedAttestation)
	}

	// A mensagem assinada é a CONCATENAÇÃO dos bytes ORIGINAIS do authData com o hash do
	// clientDataJSON — nada é reserializado.
	signed := make([]byte, 0, len(ad.Raw)+len(clientDataHash))
	signed = append(signed, ad.Raw...)
	signed = append(signed, clientDataHash...)

	if len(st.X5C) == 0 {
		if !v.cfg.AllowSelfAttestation {
			return false, ErrSelfAttestationNotAllowed
		}
		pub, alg, err := parseCOSEPublicKey(ad.CredentialPublicKey)
		if err != nil {
			return false, err
		}
		// Confusão de algoritmo: o attStmt não pode declarar um alg diferente do da chave
		// que o vai verificar.
		if st.Alg != alg {
			return false, fmt.Errorf("%w: alg do attStmt (%d) != alg da credencial (%d)", ErrUnsupportedAlgorithm, st.Alg, alg)
		}
		if err := verifySignature(pub, alg, signed, st.Sig); err != nil {
			return false, err
		}
		return true, nil
	}

	leaf, err := v.verifyChain(st.X5C)
	if err != nil {
		return false, err
	}
	if err := v.checkCertAAGUID(leaf, ad.AAGUID); err != nil {
		return false, err
	}
	// A credentialPublicKey NÃO é usada para verificar esta assinatura (é a chave do
	// certificado que a verifica), mas TEM de ser bem-formada e de um algoritmo suportado: é
	// ela que identifica a credencial atestada e que um consumidor futuro (registo/reuso)
	// usaria. Deixá-la passar sem parse era um buraco latente — uma COSE_Key malformada
	// atravessava a verificação sem ninguém reparar.
	if _, _, err := parseCOSEPublicKey(ad.CredentialPublicKey); err != nil {
		return false, err
	}
	if err := verifySignature(leaf.PublicKey, st.Alg, signed, st.Sig); err != nil {
		return false, err
	}
	return false, nil
}

// verifyU2F verifica o formato legado "fido-u2f" (WebAuthn §8.6). A construção da mensagem é
// PRÓPRIA do U2F (não é authData ‖ clientDataHash):
//
//	0x00 ‖ rpIdHash ‖ clientDataHash ‖ credentialId ‖ (0x04 ‖ x ‖ y)
//
// A assinatura é sempre ECDSA P-256/SHA-256 com a chave do certificado. O AAGUID de um
// autenticador U2F é, por especificação, TODO A ZEROS — o que significa que aceitar U2F
// obriga a pôr o AAGUID nulo na allowlist DE PROPÓSITO (não há modelo declarado a filtrar).
func (v *Verifier) verifyU2F(rawAttStmt []byte, ad *authData, clientDataHash []byte) error {
	var st u2fAttStmt
	if err := decMode.Unmarshal(rawAttStmt, &st); err != nil {
		return fmt.Errorf("%w: attStmt fido-u2f: %v", ErrMalformedAttestation, err)
	}
	if len(st.Sig) == 0 {
		return fmt.Errorf("%w: attStmt fido-u2f sem sig", ErrMalformedAttestation)
	}
	if len(st.X5C) != 1 {
		return fmt.Errorf("%w: fido-u2f exige exactamente 1 certificado", ErrBadCertificate)
	}
	if ad.AAGUID != ([16]byte{}) {
		return fmt.Errorf("%w: fido-u2f com AAGUID não-nulo (%s)", ErrMalformedAuthData, hex.EncodeToString(ad.AAGUID[:]))
	}

	leaf, err := v.verifyChain(st.X5C)
	if err != nil {
		return err
	}
	ec, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || ec.Curve != elliptic.P256() {
		return fmt.Errorf("%w: fido-u2f exige certificado ECDSA P-256", ErrBadCertificate)
	}

	x, y, err := ec2CoordinatesP256(ad.CredentialPublicKey)
	if err != nil {
		return err
	}

	msg := make([]byte, 0, 1+32+len(clientDataHash)+len(ad.CredentialID)+65)
	msg = append(msg, 0x00)
	msg = append(msg, ad.RPIDHash[:]...)
	msg = append(msg, clientDataHash...)
	msg = append(msg, ad.CredentialID...)
	msg = append(msg, 0x04)
	msg = append(msg, x...)
	msg = append(msg, y...)

	return verifySignature(ec, algES256, msg, st.Sig)
}

// verifyChain parseia e VALIDA a cadeia x5c contra as âncoras configuradas, devolvendo a
// folha. Fail-closed em cada passo: sem âncoras não se valida nada ([ErrNoTrustAnchors]);
// cadeia acima do tecto, certificado acima do tecto ou ilegível ⇒ [ErrBadCertificate]; folha
// marcada como CA ⇒ [ErrBadCertificate] (um certificado de attestation é uma folha).
func (v *Verifier) verifyChain(x5c [][]byte) (*x509.Certificate, error) {
	if v.cfg.Roots == nil {
		return nil, ErrNoTrustAnchors
	}
	if len(x5c) > maxChainCerts {
		return nil, fmt.Errorf("%w: cadeia com %d certificados (máx %d)", ErrBadCertificate, len(x5c), maxChainCerts)
	}
	certs := make([]*x509.Certificate, 0, len(x5c))
	for i, der := range x5c {
		if len(der) == 0 || len(der) > maxCertBytes {
			return nil, fmt.Errorf("%w: certificado #%d com %d bytes", ErrBadCertificate, i, len(der))
		}
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, fmt.Errorf("%w: certificado #%d: %v", ErrBadCertificate, i, err)
		}
		certs = append(certs, c)
	}
	leaf := certs[0]
	if leaf.BasicConstraintsValid && leaf.IsCA {
		return nil, fmt.Errorf("%w: folha de attestation marcada como CA", ErrBadCertificate)
	}

	// REVOGAÇÃO (offline, fail-closed) ANTES de gastar a validação de cadeia: um certificado
	// revogado — ou um intermediário revogado, que arrasta tudo o que emitiu — não passa por
	// continuar dentro da validade. Sem isto, a única remediação para uma chave de lote
	// extraída era editar a allowlist de AAGUID e redeployar.
	for i, c := range certs {
		if err := v.checkRevoked(c); err != nil {
			return nil, fmt.Errorf("%w (certificado #%d)", err, i)
		}
	}

	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	// KeyUsageAny: certificados de attestation não trazem EKU de servidor/cliente; o que
	// importa é encadearem até uma âncora que a ORGANIZAÇÃO configurou, dentro da validade.
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         v.cfg.Roots,
		Intermediates: inter,
		CurrentTime:   v.now(),
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUntrustedCertChain, err)
	}
	return leaf, nil
}

// checkRevoked confronta um certificado com os canais de revogação OFFLINE configurados: a
// denylist explícita de números de série ([Config.RevokedCertSerials]) e as CRLs carregadas
// pelo deployment ([Config.CRLs]). Sem canais configurados não há nada a verificar — e isso
// fica documentado como requisito de deployment, não escondido como default seguro.
//
// As CRLs são material de CONFIGURAÇÃO (carregadas pelo operador de uma fonte de confiança),
// pelo que a sua autenticidade é uma propriedade do deployment; aqui só se lêem as entradas.
// Compara-se por NÚMERO DE SÉRIE, que é o identificador que uma CRL revoga.
func (v *Verifier) checkRevoked(c *x509.Certificate) error {
	if c == nil {
		return fmt.Errorf("%w: certificado nulo", ErrBadCertificate)
	}
	key := certSerialKey(c)
	if key == "" {
		return fmt.Errorf("%w: certificado sem número de série", ErrBadCertificate)
	}
	if _, bad := v.revokedSerials[key]; bad {
		return fmt.Errorf("%w: série %s (denylist)", ErrCertificateRevoked, key)
	}
	for _, crl := range v.cfg.CRLs {
		if crl == nil {
			continue
		}
		for i := range crl.RevokedCertificateEntries {
			if crl.RevokedCertificateEntries[i].SerialNumber == nil {
				continue
			}
			if crl.RevokedCertificateEntries[i].SerialNumber.Cmp(c.SerialNumber) == 0 {
				return fmt.Errorf("%w: série %s (CRL)", ErrCertificateRevoked, key)
			}
		}
	}
	return nil
}

// checkCertAAGUID confere a extensão id-fido-gen-ce-aaguid do certificado contra o AAGUID do
// authData. O valor da extensão é uma OCTET STRING DER que ENVOLVE os 16 bytes do AAGUID.
// Ausência ⇒ [ErrMissingAAGUIDExtension] salvo opt-in explícito; divergência ⇒
// [ErrAAGUIDMismatch]. A extensão NÃO pode ser crítica (WebAuthn §8.2.1).
func (v *Verifier) checkCertAAGUID(leaf *x509.Certificate, aaguid [16]byte) error {
	for _, ext := range leaf.Extensions {
		if !ext.Id.Equal(oidAAGUID) {
			continue
		}
		if ext.Critical {
			return fmt.Errorf("%w: extensão de AAGUID marcada como crítica", ErrBadCertificate)
		}
		var raw []byte
		rest, err := asn1.Unmarshal(ext.Value, &raw)
		if err != nil || len(rest) != 0 {
			return fmt.Errorf("%w: extensão de AAGUID mal codificada", ErrBadCertificate)
		}
		if len(raw) != 16 {
			return fmt.Errorf("%w: extensão de AAGUID com %d bytes", ErrBadCertificate, len(raw))
		}
		if [16]byte(raw) != aaguid {
			return fmt.Errorf("%w: cert=%s authData=%s", ErrAAGUIDMismatch, hex.EncodeToString(raw), hex.EncodeToString(aaguid[:]))
		}
		return nil
	}
	if v.cfg.AllowCertWithoutAAGUIDExtension {
		return nil
	}
	return ErrMissingAAGUIDExtension
}
