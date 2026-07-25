package attestation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
)

// (a) CAMINHO FELIZ — attestation "packed" com cadeia x5c, AAGUID na allowlist e extensão de
// AAGUID coerente ⇒ ACEITE, devolvendo o AAGUID e o credentialId observados.
func TestVerify_PackedWithX5C_Accepted(t *testing.T) {
	ca := newTestCA(t, "AOS Test Attestation Root")
	v := newVerifier(t, ca)
	s := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: &testAAGUID})

	att, err := v.Verify(context.Background(), s.attObj, s.clientData, s.challenge)
	if err != nil {
		t.Fatalf("attestation válida devia ser ACEITE, veio: %v", err)
	}
	if att.Format != "packed" {
		t.Fatalf("Format = %q, quer packed", att.Format)
	}
	if att.AAGUID != testAAGUID {
		t.Fatalf("AAGUID = %x, quer %x", att.AAGUID, testAAGUID)
	}
	if !bytes.Equal(att.CredentialID, s.credID) {
		t.Fatalf("CredentialID não corresponde ao sintetizado")
	}
	if !att.UserPresent || att.UserVerified {
		t.Fatalf("flags = UP:%v UV:%v, quer UP:true UV:false", att.UserPresent, att.UserVerified)
	}
	if att.SelfAttested {
		t.Fatal("attestation com cadeia x5c não é self-attestation")
	}
	if len(att.DeviceID) != sha256.Size {
		t.Fatalf("DeviceID com %d bytes, quer %d", len(att.DeviceID), sha256.Size)
	}
	// O credentialId devolvido é uma CÓPIA: mutá-lo não pode contaminar nada.
	att.CredentialID[0] ^= 0xff
	att2, err := v.Verify(context.Background(), s.attObj, s.clientData, s.challenge)
	if err != nil || !bytes.Equal(att2.CredentialID, s.credID) {
		t.Fatalf("2.ª verificação devia ser idêntica (err=%v)", err)
	}
	if !bytes.Equal(att2.DeviceID, deviceID(s.aaguid, s.credID)) {
		t.Fatal("DeviceID não é estável/derivável de (AAGUID, credentialId)")
	}
}

// A PORTA: VerifyDeviceAttestation devolve SÓ o identificador opaco — e é ESTÁVEL para o
// mesmo dispositivo e DISTINTO entre dispositivos. É a propriedade de que o gate 4-eyes
// depende para exigir "dois dispositivos atestados distintos".
func TestVerifyDeviceAttestation_StableAndDistinct(t *testing.T) {
	ca := newTestCA(t, "AOS Test Attestation Root")
	v := newVerifier(t, ca)

	credA := randBytes(t, 32)
	s1 := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: &testAAGUID, credID: credA})
	s2 := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: &testAAGUID, credID: credA})
	s3 := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: &testAAGUID})

	id1, err := v.VerifyDeviceAttestation(context.Background(), s1.attObj, s1.clientData, s1.challenge)
	if err != nil {
		t.Fatalf("id1: %v", err)
	}
	id2, err := v.VerifyDeviceAttestation(context.Background(), s2.attObj, s2.clientData, s2.challenge)
	if err != nil {
		t.Fatalf("id2: %v", err)
	}
	id3, err := v.VerifyDeviceAttestation(context.Background(), s3.attObj, s3.clientData, s3.challenge)
	if err != nil {
		t.Fatalf("id3: %v", err)
	}
	if !bytes.Equal(id1, id2) {
		t.Fatal("mesmo (AAGUID, credentialId) devia dar o MESMO deviceID (cerimónias diferentes)")
	}
	if bytes.Equal(id1, id3) {
		t.Fatal("credenciais distintas deviam dar deviceIDs DISTINTOS")
	}
	// O deviceID não pode ser o credentialId em claro (é um digest, não um handle exposto).
	if bytes.Contains(id1, credA) {
		t.Fatal("deviceID não pode conter o credentialId em claro")
	}
}

// (b) AAGUID FORA DA ALLOWLIST ⇒ RECUSADA. NÃO-VACUOSO: a attestation é criptograficamente
// VÁLIDA (cadeia boa, assinatura boa, extensão coerente) — só o modelo é que não é permitido.
func TestVerify_AAGUIDNotAllowed(t *testing.T) {
	ca := newTestCA(t, "AOS Test Attestation Root")
	v := newVerifier(t, ca)
	s := newPackedAttestation(t, packedOpts{ca: ca, aaguid: otherAAGUID, certAAGUID: &otherAAGUID})

	_, err := v.Verify(context.Background(), s.attObj, s.clientData, s.challenge)
	if !errors.Is(err, ErrAAGUIDNotAllowed) {
		t.Fatalf("AAGUID fora da allowlist devia dar ErrAAGUIDNotAllowed, veio: %v", err)
	}
	// Sanidade: o MESMO AAGUID, uma vez permitido, passa — prova que a recusa foi da
	// allowlist e não de outro defeito da síntese.
	v2 := newVerifier(t, ca, func(c *Config) { c.AllowedAAGUIDs = [][16]byte{otherAAGUID} })
	if _, err := v2.Verify(context.Background(), s.attObj, s.clientData, s.challenge); err != nil {
		t.Fatalf("com o AAGUID na allowlist devia ACEITAR, veio: %v", err)
	}
}

// (c) ASSINATURA ADULTERADA ⇒ RECUSADA (o último byte da sig é invertido; tudo o resto é
// válido, incluindo a cadeia).
func TestVerify_TamperedSignature(t *testing.T) {
	ca := newTestCA(t, "AOS Test Attestation Root")
	v := newVerifier(t, ca)
	s := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: &testAAGUID, tamperSig: true})

	if _, err := v.Verify(context.Background(), s.attObj, s.clientData, s.challenge); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("assinatura adulterada devia dar ErrBadSignature, veio: %v", err)
	}
}

// AUTHDATA ADULTERADO ⇒ RECUSADO: a assinatura cobre authData ‖ clientDataHash, pelo que
// mexer no signCount (campo sem significado de política) invalida na mesma. Prova que a
// verificação é sobre os BYTES e não sobre campos re-serializados.
func TestVerify_TamperedAuthData(t *testing.T) {
	ca := newTestCA(t, "AOS Test Attestation Root")
	v := newVerifier(t, ca)
	s := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: &testAAGUID})

	obj, err := decodeAttestationObject(s.attObj)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	authData := append([]byte(nil), obj.AuthData...)
	authData[36] ^= 0x01 // signCount
	var attStmt map[string]any
	if err := decMode.Unmarshal(obj.AttStmt, &attStmt); err != nil {
		t.Fatalf("attStmt: %v", err)
	}
	mutated := marshalAttObj(t, "packed", attStmt, authData)

	if _, err := v.Verify(context.Background(), mutated, s.clientData, s.challenge); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("authData adulterado devia dar ErrBadSignature, veio: %v", err)
	}
}

// (d) CHALLENGE DIFERENTE DO ESPERADO ⇒ RECUSADA. É a ligação da attestation à perna do
// 4-eyes: sem esta recusa, uma attestation legítima seria re-colável noutro pedido.
func TestVerify_ChallengeMismatch(t *testing.T) {
	ca := newTestCA(t, "AOS Test Attestation Root")
	v := newVerifier(t, ca)
	s := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: &testAAGUID})

	other := randBytes(t, 32)
	if _, err := v.Verify(context.Background(), s.attObj, s.clientData, other); !errors.Is(err, ErrChallengeMismatch) {
		t.Fatalf("challenge diferente devia dar ErrChallengeMismatch, veio: %v", err)
	}
	// Prefixo do challenge correcto (mas truncado) também recusa — a comparação é total.
	if _, err := v.Verify(context.Background(), s.attObj, s.clientData, s.challenge[:16]); !errors.Is(err, ErrChallengeMismatch) {
		t.Fatalf("challenge truncado devia dar ErrChallengeMismatch, veio: %v", err)
	}
	// Challenge esperado vazio ⇒ recusa explícita (não "aceita qualquer coisa").
	if _, err := v.Verify(context.Background(), s.attObj, s.clientData, nil); !errors.Is(err, ErrNoChallenge) {
		t.Fatalf("challenge vazio devia dar ErrNoChallenge, veio: %v", err)
	}
}

// (e) ORIGIN e RPID ERRADOS ⇒ RECUSADA (dois vectores distintos, cada um com o seu erro).
func TestVerify_WrongOriginAndRPID(t *testing.T) {
	ca := newTestCA(t, "AOS Test Attestation Root")
	v := newVerifier(t, ca)

	badOrigin := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: &testAAGUID, origin: "https://evil.example"})
	if _, err := v.Verify(context.Background(), badOrigin.attObj, badOrigin.clientData, badOrigin.challenge); !errors.Is(err, ErrOriginNotAllowed) {
		t.Fatalf("origin fora da allowlist devia dar ErrOriginNotAllowed, veio: %v", err)
	}

	badRP := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: &testAAGUID, rpID: "evil.example"})
	if _, err := v.Verify(context.Background(), badRP.attObj, badRP.clientData, badRP.challenge); !errors.Is(err, ErrRPIDMismatch) {
		t.Fatalf("rpId errado devia dar ErrRPIDMismatch, veio: %v", err)
	}

	badType := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: &testAAGUID, clientType: "webauthn.get"})
	if _, err := v.Verify(context.Background(), badType.attObj, badType.clientData, badType.challenge); !errors.Is(err, ErrWrongClientDataType) {
		t.Fatalf("type=webauthn.get devia dar ErrWrongClientDataType, veio: %v", err)
	}
}

// (f) fmt "none" ⇒ RECUSADO. Este verificador existe para EXIGIR attestation; um
// autenticador que não diz nada sobre si não satisfaz o ADR-016 §4.
func TestVerify_NoneFormatRejected(t *testing.T) {
	ca := newTestCA(t, "AOS Test Attestation Root")
	v := newVerifier(t, ca)
	s := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: &testAAGUID})

	obj, err := decodeAttestationObject(s.attObj)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	none := marshalAttObj(t, "none", map[string]any{}, obj.AuthData)
	if _, err := v.Verify(context.Background(), none, s.clientData, s.challenge); !errors.Is(err, ErrNoneAttestation) {
		t.Fatalf("fmt none devia dar ErrNoneAttestation, veio: %v", err)
	}

	unknown := marshalAttObj(t, "apple-magic\x00", map[string]any{}, obj.AuthData)
	err = func() (e error) {
		_, e = v.Verify(context.Background(), unknown, s.clientData, s.challenge)
		return
	}()
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("fmt desconhecido devia dar ErrUnsupportedFormat, veio: %v", err)
	}
	// O fmt hostil não passa bytes de controlo para a mensagem de erro.
	if strings.ContainsRune(err.Error(), 0x00) {
		t.Fatal("mensagem de erro não pode transportar bytes de controlo da entrada")
	}
}

// (g) CADEIA NÃO-CONFIÁVEL ⇒ RECUSADA: a attestation é emitida por uma CA REAL mas
// DIFERENTE da configurada. E sem âncoras configuradas, qualquer cadeia é recusada.
func TestVerify_UntrustedChain(t *testing.T) {
	good := newTestCA(t, "AOS Test Attestation Root")
	rogue := newTestCA(t, "Rogue Root")
	v := newVerifier(t, good)

	s := newPackedAttestation(t, packedOpts{ca: rogue, aaguid: testAAGUID, certAAGUID: &testAAGUID})
	if _, err := v.Verify(context.Background(), s.attObj, s.clientData, s.challenge); !errors.Is(err, ErrUntrustedCertChain) {
		t.Fatalf("CA desconhecida devia dar ErrUntrustedCertChain, veio: %v", err)
	}

	noRoots := newVerifier(t, nil)
	sGood := newPackedAttestation(t, packedOpts{ca: good, aaguid: testAAGUID, certAAGUID: &testAAGUID})
	if _, err := noRoots.Verify(context.Background(), sGood.attObj, sGood.clientData, sGood.challenge); !errors.Is(err, ErrNoTrustAnchors) {
		t.Fatalf("sem âncoras devia dar ErrNoTrustAnchors, veio: %v", err)
	}
}

// (h) FLAG UV AUSENTE com user-verification EXIGIDA ⇒ RECUSADA; e a mesma attestation COM UV
// passa (prova que a recusa é da flag, não de outra coisa). UP ausente é sempre recusado.
func TestVerify_UserVerificationFlags(t *testing.T) {
	ca := newTestCA(t, "AOS Test Attestation Root")
	requireUV := newVerifier(t, ca, func(c *Config) { c.RequireUserVerification = true })

	noUV := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: &testAAGUID})
	if _, err := requireUV.Verify(context.Background(), noUV.attObj, noUV.clientData, noUV.challenge); !errors.Is(err, ErrUserNotVerified) {
		t.Fatalf("UV ausente com UV exigida devia dar ErrUserNotVerified, veio: %v", err)
	}

	withUV := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: &testAAGUID, userVerified: true})
	att, err := requireUV.Verify(context.Background(), withUV.attObj, withUV.clientData, withUV.challenge)
	if err != nil {
		t.Fatalf("UV presente devia ACEITAR, veio: %v", err)
	}
	if !att.UserVerified {
		t.Fatal("Attested.UserVerified devia ser true")
	}

	noUP := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: &testAAGUID, omitUP: true})
	v := newVerifier(t, ca)
	if _, err := v.Verify(context.Background(), noUP.attObj, noUP.clientData, noUP.challenge); !errors.Is(err, ErrUserNotPresent) {
		t.Fatalf("UP ausente devia dar ErrUserNotPresent, veio: %v", err)
	}
}

// (i) ENTRADA MALFORMADA ⇒ RECUSADA SEM PANIC. Cobre CBOR truncado/lixo, authData truncado,
// comprimentos mentirosos (o clássico out-of-bounds) e JSON inválido.
func TestVerify_MalformedInputsNeverPanic(t *testing.T) {
	ca := newTestCA(t, "AOS Test Attestation Root")
	v := newVerifier(t, ca)
	good := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: &testAAGUID})

	obj, err := decodeAttestationObject(good.attObj)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	// authData com credentialIdLen a declarar mais bytes do que existem: o caso que uma
	// fatia ingénua transformaria em panic.
	lying := append([]byte(nil), obj.AuthData...)
	lying[53] = 0xff // byte alto de credentialIdLen (32+1+4+16 = 53)
	lying[54] = 0xf0

	cases := []struct {
		name       string
		attObj     []byte
		clientData []byte
	}{
		{"cbor truncado", good.attObj[:len(good.attObj)/2], good.clientData},
		{"cbor lixo", []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, good.clientData},
		{"cbor com bytes a mais", append(append([]byte(nil), good.attObj...), 0x00, 0x01), good.clientData},
		{"authData vazio", marshalAttObj(t, "packed", map[string]any{"alg": -7, "sig": []byte{1}}, []byte{}), good.clientData},
		{"authData truncado", marshalAttObj(t, "packed", map[string]any{"alg": -7, "sig": []byte{1}}, obj.AuthData[:20]), good.clientData},
		{"credentialIdLen mentiroso", marshalAttObj(t, "packed", map[string]any{"alg": -7, "sig": []byte{1}}, lying), good.clientData},
		{"authData com bytes a mais", marshalAttObj(t, "packed", map[string]any{"alg": -7, "sig": []byte{1}}, append(append([]byte(nil), obj.AuthData...), 0xaa)), good.clientData},
		{"clientData não-JSON", good.attObj, []byte("nao é json")},
		{"clientData com lixo no fim", good.attObj, append(append([]byte(nil), good.clientData...), '{')},
		{"clientData vazio", good.attObj, []byte{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("PANIC com entrada malformada (%v) — fail-closed exige erro, não panic", r)
				}
			}()
			att, err := v.Verify(context.Background(), tc.attObj, tc.clientData, good.challenge)
			if err == nil {
				t.Fatalf("entrada malformada devia ser RECUSADA, foi aceite: %+v", att)
			}
			if len(att.DeviceID) != 0 {
				t.Fatal("recusa não pode devolver um deviceID utilizável")
			}
		})
	}

	// Entrada acima do tecto anti-DoS.
	huge := make([]byte, DefaultMaxAttestationObjectBytes+1)
	if _, err := v.Verify(context.Background(), huge, good.clientData, good.challenge); !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("attestationObject gigante devia dar ErrInputTooLarge, veio: %v", err)
	}
}

// (j) EXTENSÃO DE AAGUID DO CERTIFICADO != AAGUID DO AUTHDATA ⇒ RECUSADA. Sem esta
// verificação, um certificado legítimo de um modelo qualquer acompanharia um authData a
// declarar um modelo DA ALLOWLIST e passaria.
func TestVerify_CertAAGUIDMismatch(t *testing.T) {
	ca := newTestCA(t, "AOS Test Attestation Root")
	v := newVerifier(t, ca)

	mismatch := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: &otherAAGUID})
	if _, err := v.Verify(context.Background(), mismatch.attObj, mismatch.clientData, mismatch.challenge); !errors.Is(err, ErrAAGUIDMismatch) {
		t.Fatalf("AAGUID do cert != authData devia dar ErrAAGUIDMismatch, veio: %v", err)
	}

	// Extensão AUSENTE: recusada por omissão (fail-closed), aceite só com opt-in explícito.
	missing := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: nil})
	if _, err := v.Verify(context.Background(), missing.attObj, missing.clientData, missing.challenge); !errors.Is(err, ErrMissingAAGUIDExtension) {
		t.Fatalf("cert sem extensão de AAGUID devia dar ErrMissingAAGUIDExtension, veio: %v", err)
	}
	tolerant := newVerifier(t, ca, func(c *Config) { c.AllowCertWithoutAAGUIDExtension = true })
	if _, err := tolerant.Verify(context.Background(), missing.attObj, missing.clientData, missing.challenge); err != nil {
		t.Fatalf("com opt-in devia ACEITAR, veio: %v", err)
	}
}

// SELF-ATTESTATION (packed sem x5c): recusada por omissão, aceite com opt-in — e marcada
// como tal no resultado, para o audit não a confundir com prova certificada por terceiro.
func TestVerify_SelfAttestation(t *testing.T) {
	ca := newTestCA(t, "AOS Test Attestation Root")
	s := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, selfAttest: true})

	strict := newVerifier(t, ca)
	if _, err := strict.Verify(context.Background(), s.attObj, s.clientData, s.challenge); !errors.Is(err, ErrSelfAttestationNotAllowed) {
		t.Fatalf("self-attestation sem opt-in devia dar ErrSelfAttestationNotAllowed, veio: %v", err)
	}

	// O opt-in EXIGE reconhecimento explícito da degradação (a allowlist de AAGUID passa a
	// auto-declarada): AllowSelfAttestation sozinho não constrói verificador.
	if _, err := New(Config{
		RPID:                 testRPID,
		AllowedOrigins:       []string{testOrigin},
		AllowedAAGUIDs:       [][16]byte{testAAGUID},
		Roots:                ca.pool,
		AllowSelfAttestation: true,
	}); !errors.Is(err, ErrConfigSelfAttestationAck) {
		t.Fatalf("AllowSelfAttestation sem reconhecimento devia dar ErrConfigSelfAttestationAck, veio: %v", err)
	}

	lenient := newVerifier(t, ca, func(c *Config) {
		c.AllowSelfAttestation = true
		c.SelfAttestationAcknowledged = true
	})
	att, err := lenient.Verify(context.Background(), s.attObj, s.clientData, s.challenge)
	if err != nil {
		t.Fatalf("self-attestation com opt-in devia ACEITAR, veio: %v", err)
	}
	if !att.SelfAttested || att.AAGUID != testAAGUID {
		t.Fatalf("resultado = %+v, quer SelfAttested=true e AAGUID de teste", att)
	}

	// Adulterar a assinatura self ⇒ recusa (a verificação é com a chave da credencial).
	bad := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, selfAttest: true, tamperSig: true})
	if _, err := lenient.Verify(context.Background(), bad.attObj, bad.clientData, bad.challenge); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("self-attestation adulterada devia dar ErrBadSignature, veio: %v", err)
	}
}

// FIDO-U2F (legado): construção de bytes PRÓPRIA. Exige DOIS opt-ins independentes — o AAGUID
// nulo na allowlist E Config.AllowU2FLegacy —, precisamente para o AAGUID nulo deixar de ser
// a chave-mestra que também abria o caminho packed.
func TestVerify_FidoU2F(t *testing.T) {
	ca := newTestCA(t, "AOS Test Attestation Root")
	s := newU2FAttestation(t, ca)

	strict := newVerifier(t, ca) // allowlist só com testAAGUID
	if _, err := strict.Verify(context.Background(), s.attObj, s.clientData, s.challenge); !errors.Is(err, ErrAAGUIDNotAllowed) {
		t.Fatalf("U2F sem o AAGUID nulo na allowlist devia recusar, veio: %v", err)
	}

	// Só com o AAGUID nulo na allowlist (sem o opt-in do legado) ⇒ continua a RECUSAR.
	aaguidOnly := newVerifier(t, ca, func(c *Config) { c.AllowedAAGUIDs = [][16]byte{zeroAAGUIDVal} })
	if _, err := aaguidOnly.Verify(context.Background(), s.attObj, s.clientData, s.challenge); !errors.Is(err, ErrU2FNotAllowed) {
		t.Fatalf("fido-u2f sem AllowU2FLegacy devia dar ErrU2FNotAllowed, veio: %v", err)
	}

	u2fOK := newVerifier(t, ca, func(c *Config) {
		c.AllowedAAGUIDs = [][16]byte{zeroAAGUIDVal}
		c.AllowU2FLegacy = true
	})
	att, err := u2fOK.Verify(context.Background(), s.attObj, s.clientData, s.challenge)
	if err != nil {
		t.Fatalf("fido-u2f válido devia ACEITAR, veio: %v", err)
	}
	if att.Format != "fido-u2f" || !bytes.Equal(att.CredentialID, s.credID) {
		t.Fatalf("resultado inesperado: %+v", att)
	}

	// Cadeia de outra CA ⇒ recusa também no caminho legado.
	rogue := newTestCA(t, "Rogue Root")
	sRogue := newU2FAttestation(t, rogue)
	if _, err := u2fOK.Verify(context.Background(), sRogue.attObj, sRogue.clientData, sRogue.challenge); !errors.Is(err, ErrUntrustedCertChain) {
		t.Fatalf("U2F de CA desconhecida devia dar ErrUntrustedCertChain, veio: %v", err)
	}
}

// CONFIGURAÇÃO fail-closed: um verificador sem rpId, sem origens ou sem allowlist de AAGUID
// NÃO chega a existir (nada de "permissivo por omissão").
func TestNew_ConfigFailClosed(t *testing.T) {
	base := Config{RPID: testRPID, AllowedOrigins: []string{testOrigin}, AllowedAAGUIDs: [][16]byte{testAAGUID}}

	noRP := base
	noRP.RPID = ""
	if _, err := New(noRP); !errors.Is(err, ErrConfigRPID) {
		t.Fatalf("sem RPID devia dar ErrConfigRPID, veio: %v", err)
	}
	noOrigin := base
	noOrigin.AllowedOrigins = nil
	if _, err := New(noOrigin); !errors.Is(err, ErrConfigOrigins) {
		t.Fatalf("sem origens devia dar ErrConfigOrigins, veio: %v", err)
	}
	emptyOrigin := base
	emptyOrigin.AllowedOrigins = []string{""}
	if _, err := New(emptyOrigin); !errors.Is(err, ErrConfigOrigins) {
		t.Fatalf("origem vazia devia dar ErrConfigOrigins, veio: %v", err)
	}
	noAAGUID := base
	noAAGUID.AllowedAAGUIDs = nil
	if _, err := New(noAAGUID); !errors.Is(err, ErrConfigAAGUIDs) {
		t.Fatalf("sem allowlist devia dar ErrConfigAAGUIDs, veio: %v", err)
	}
}

// O contexto CANCELADO nega imediatamente (o verificador respeita o prazo do chamador).
func TestVerify_ContextCancelled(t *testing.T) {
	ca := newTestCA(t, "AOS Test Attestation Root")
	v := newVerifier(t, ca)
	s := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: &testAAGUID})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := v.Verify(ctx, s.attObj, s.clientData, s.challenge); !errors.Is(err, context.Canceled) {
		t.Fatalf("contexto cancelado devia negar, veio: %v", err)
	}
}

// O verificador é usado CONCORRENTEMENTE pelo gate (duas pernas em paralelo, vários
// pedidos): sem estado mutável, tem de ser seguro sob -race.
func TestVerify_ConcurrentUse(t *testing.T) {
	ca := newTestCA(t, "AOS Test Attestation Root")
	v := newVerifier(t, ca)
	s := newPackedAttestation(t, packedOpts{ca: ca, aaguid: testAAGUID, certAAGUID: &testAAGUID})

	const n = 16
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			_, err := v.VerifyDeviceAttestation(context.Background(), s.attObj, s.clientData, s.challenge)
			errs <- err
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("verificação concorrente falhou: %v", err)
		}
	}
}
