package attestation

import "errors"

// Sentinelas do verificador. TODAS representam NEGAÇÃO (fail-closed): não existe erro
// "aviso". São comparáveis com errors.Is e nunca transportam segredos/PII — o único
// identificador que pode aparecer no texto é o AAGUID (modelo do autenticador) em hex.
var (
	// --- Configuração (recusadas em [New], não em tempo de verificação) ---

	// ErrConfigRPID — Config.RPID vazio. Sem rpId não há como validar o rpIdHash.
	ErrConfigRPID = errors.New("attestation: config: RPID obrigatório")
	// ErrConfigOrigins — Config.AllowedOrigins vazio. Sem allowlist de origem, qualquer
	// site poderia produzir clientData aceitável.
	ErrConfigOrigins = errors.New("attestation: config: pelo menos uma origem permitida é obrigatória")
	// ErrConfigSelfAttestationAck — Config.AllowSelfAttestation=true sem
	// Config.SelfAttestationAcknowledged. A self-attestation rebaixa a allowlist de AAGUID a
	// AUTO-DECLARADA (qualquer software authenticator declara um AAGUID permitido e assina
	// com a própria chave); a degradação tem de ser deliberada e visível na configuração.
	ErrConfigSelfAttestationAck = errors.New("attestation: config: AllowSelfAttestation exige SelfAttestationAcknowledged (a allowlist de AAGUID passa a auto-declarada)")
	// ErrConfigRevokedSerial — entrada de Config.RevokedCertSerials que não é um número de
	// série hex legível. Uma revogação ilegível não revoga nada ⇒ recusa-se a config.
	ErrConfigRevokedSerial = errors.New("attestation: config: número de série revogado ilegível (hex esperado)")
	// ErrConfigAAGUIDs — Config.AllowedAAGUIDs vazio. A allowlist de modelos de
	// autenticador é DEFAULT-DENY: sem entradas, nada seria aceitável, e uma allowlist
	// vazia é quase sempre um erro de configuração — recusa-se à cabeça.
	ErrConfigAAGUIDs = errors.New("attestation: config: allowlist de AAGUID obrigatória (default-deny)")

	// --- Entrada / formato ---

	// ErrInputTooLarge — attestationObject ou clientDataJSON acima do limite anti-DoS.
	ErrInputTooLarge = errors.New("attestation: entrada acima do limite permitido")
	// ErrMalformedAttestation — attestationObject não é CBOR válido, viola os limites do
	// descodificador, ou não tem a forma {fmt, attStmt, authData}.
	ErrMalformedAttestation = errors.New("attestation: attestationObject malformado")
	// ErrMalformedAuthData — authData truncado/inconsistente (bounds do parse binário).
	ErrMalformedAuthData = errors.New("attestation: authData malformado")
	// ErrMalformedClientData — clientDataJSON não é JSON válido ou não tem os campos.
	ErrMalformedClientData = errors.New("attestation: clientDataJSON malformado")
	// ErrMalformedCOSEKey — credentialPublicKey (COSE) malformada ou incompleta.
	ErrMalformedCOSEKey = errors.New("attestation: credentialPublicKey COSE malformada")
	// ErrUnsupportedFormat — fmt de attestation não suportado por este verificador.
	ErrUnsupportedFormat = errors.New("attestation: formato de attestation não suportado")
	// ErrUnsupportedAlgorithm — algoritmo COSE fora da lista curta suportada
	// (ES256 / RS256 / EdDSA).
	ErrUnsupportedAlgorithm = errors.New("attestation: algoritmo COSE não suportado")

	// --- Política ---

	// ErrNoneAttestation — fmt "none": o autenticador não fez qualquer afirmação sobre si.
	// Este verificador existe precisamente para EXIGIR attestation ⇒ recusa sempre.
	ErrNoneAttestation = errors.New("attestation: fmt \"none\" recusado (attestation exigida)")
	// ErrSelfAttestationNotAllowed — packed sem x5c (self-attestation) sem opt-in.
	ErrSelfAttestationNotAllowed = errors.New("attestation: self-attestation não permitida por configuração")
	// ErrSelfAttestationNotAcceptedByPort — a prova é self-attested e foi pedida pela PORTA
	// de dual-control ([Verifier.VerifyDeviceAttestation]), que só transporta um deviceID e
	// não consegue exprimir a força da prova. Recusa-se para que uma prova AUTO-DECLARADA
	// nunca seja indistinguível de uma certificada por terceiro numa acção irreversível.
	ErrSelfAttestationNotAcceptedByPort = errors.New("attestation: prova self-attested não aceite pela porta de dual-control (usar Verify para auditoria)")
	// ErrU2FNotAllowed — fmt "fido-u2f" sem Config.AllowU2FLegacy. O legado exige opt-in
	// PRÓPRIO: antes deduzia-se de se pôr o AAGUID nulo na allowlist, o que também o admitia
	// no caminho packed (onde anularia o filtro de modelo).
	ErrU2FNotAllowed = errors.New("attestation: formato legado fido-u2f não permitido por configuração")
	// ErrZeroAAGUIDPacked — authData com AAGUID todo-a-zeros no caminho "packed". O AAGUID
	// nulo é a marca do legado U2F ("sem modelo declarado"); aceitá-lo em packed deixaria o
	// verificador sem filtro de MODELO efectivo.
	ErrZeroAAGUIDPacked = errors.New("attestation: AAGUID nulo não é aceitável no formato packed (é do legado U2F)")
	// ErrAAGUIDNotAllowed — o AAGUID do autenticador não consta da allowlist (default-deny).
	ErrAAGUIDNotAllowed = errors.New("attestation: AAGUID fora da allowlist")
	// ErrAAGUIDRevoked — o AAGUID consta da DENYLIST de modelos (ex.: chave de lote extraída/
	// comprometida). Tem precedência sobre a allowlist.
	ErrAAGUIDRevoked = errors.New("attestation: AAGUID revogado (modelo comprometido)")
	// ErrCertificateRevoked — o certificado de attestation (folha ou intermediário) consta
	// da denylist de séries ou de uma CRL configurada.
	ErrCertificateRevoked = errors.New("attestation: certificado de attestation revogado")
	// ErrAAGUIDMismatch — o AAGUID declarado na extensão do certificado de attestation
	// (OID 1.3.6.1.4.1.45724.1.1.4) NÃO coincide com o do authData: o certificado atesta um
	// modelo e o authData afirma outro.
	ErrAAGUIDMismatch = errors.New("attestation: AAGUID do certificado != AAGUID do authData")
	// ErrMissingAAGUIDExtension — o certificado de attestation não traz a extensão de
	// AAGUID e a configuração exige-a (default).
	ErrMissingAAGUIDExtension = errors.New("attestation: certificado de attestation sem extensão de AAGUID")
	// ErrRPIDMismatch — rpIdHash do authData != SHA-256(RPID) configurado.
	ErrRPIDMismatch = errors.New("attestation: rpIdHash != SHA-256(rpId)")
	// ErrOriginNotAllowed — a origin do clientDataJSON não consta da allowlist.
	ErrOriginNotAllowed = errors.New("attestation: origin do clientData fora da allowlist")
	// ErrWrongClientDataType — clientData.type != "webauthn.create".
	ErrWrongClientDataType = errors.New("attestation: clientData.type != webauthn.create")
	// ErrChallengeMismatch — o challenge do clientData não é o ESPERADO (comparação em
	// tempo constante). É o que impede re-colar uma attestation noutra perna/pedido.
	ErrChallengeMismatch = errors.New("attestation: challenge do clientData != challenge esperado")
	// ErrNoChallenge — challenge esperado vazio: não há nada a que ligar a attestation.
	ErrNoChallenge = errors.New("attestation: challenge esperado vazio (fail-closed)")
	// ErrUserNotPresent — flag UP ausente: não houve interacção física com o autenticador.
	ErrUserNotPresent = errors.New("attestation: flag UP (user present) ausente")
	// ErrUserNotVerified — flag UV ausente com user-verification exigida por configuração.
	ErrUserNotVerified = errors.New("attestation: flag UV (user verified) ausente e exigida")
	// ErrNoAttestedCredential — flag AT ausente: o authData não traz AAGUID/credential.
	// Sem credential atestada não há dispositivo a identificar.
	ErrNoAttestedCredential = errors.New("attestation: flag AT ausente (sem credencial atestada)")

	// --- Cadeia / assinatura ---

	// ErrNoTrustAnchors — veio uma cadeia x5c mas a configuração não tem âncoras de
	// confiança. Sem raízes não se pode afirmar nada sobre a cadeia ⇒ recusa.
	ErrNoTrustAnchors = errors.New("attestation: cadeia x5c sem âncoras de confiança configuradas")
	// ErrBadCertificate — certificado de attestation ilegível, demasiado grande, ou com
	// perfil inválido (ex.: BasicConstraints CA=true numa folha de attestation).
	ErrBadCertificate = errors.New("attestation: certificado de attestation inválido")
	// ErrUntrustedCertChain — a cadeia não encadeia até uma âncora configurada.
	ErrUntrustedCertChain = errors.New("attestation: cadeia de attestation não confiável")
	// ErrBadSignature — a assinatura de attestation não valida sobre
	// authData ‖ SHA-256(clientDataJSON).
	ErrBadSignature = errors.New("attestation: assinatura de attestation inválida")
)
