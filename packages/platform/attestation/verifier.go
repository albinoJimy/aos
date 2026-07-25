package attestation

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Limites anti-DoS por omissão. Uma attestation legítima é de poucos KiB; estes tectos são
// generosos para o caso legítimo e apertados para o hostil (evitam alocação guiada pelo
// atacante antes de qualquer verificação criptográfica).
const (
	// DefaultMaxAttestationObjectBytes — tecto do attestationObject CBOR.
	DefaultMaxAttestationObjectBytes = 64 << 10
	// DefaultMaxClientDataBytes — tecto do clientDataJSON.
	DefaultMaxClientDataBytes = 8 << 10
	// maxCertBytes — tecto por certificado DER dentro de x5c.
	maxCertBytes = 16 << 10
	// maxChainCerts — tecto de certificados numa cadeia x5c.
	maxChainCerts = 5
)

// deviceIDDomain é o separador de domínio do identificador opaco de dispositivo: garante que
// o mesmo par (AAGUID, credentialID) nunca colide com um digest de outro subsistema.
const deviceIDDomain = "aos.attestation.device.v1"

// Config é a POLÍTICA de attestation da organização. Todos os campos obrigatórios são
// validados em [New] — um verificador mal configurado não chega a existir (não há
// degradação silenciosa em tempo de verificação).
type Config struct {
	// RPID é o Relying Party ID (ex.: "aos.example.org"). O authData tem de trazer
	// SHA-256(RPID) — é o que impede uma attestation emitida para outro RP de servir aqui.
	RPID string
	// AllowedOrigins são as origens web EXACTAS aceitáveis no clientDataJSON
	// (ex.: "https://aos.example.org"). Comparação por igualdade — sem wildcards, sem
	// normalização criativa.
	AllowedOrigins []string
	// AllowedAAGUIDs é a ALLOWLIST DE MODELOS de autenticador (16 bytes cada). DEFAULT-DENY:
	// um AAGUID que não conste é recusado. É a expressão concreta da "política de
	// dispositivos da organização" que o ADR-016 §4 exigia e que não existia.
	AllowedAAGUIDs [][16]byte
	// Roots são as âncoras de confiança dos certificados de attestation (x5c). nil ⇒
	// qualquer attestation com cadeia é RECUSADA ([ErrNoTrustAnchors]).
	Roots *x509.CertPool
	// AllowSelfAttestation permite packed SEM x5c (o autenticador assina com a própria
	// chave da credencial). Prova posse, NÃO prova modelo por terceiro — daí ser opt-in.
	//
	// DEGRADAÇÃO REAL: com self-attestation a allowlist de AAGUID passa a ser AUTO-DECLARADA.
	// Qualquer software authenticator gera um par de chaves, escreve no authData um AAGUID da
	// allowlist e assina com a própria chave da credencial — e nada o distingue de uma chave
	// certificada. Por isso NÃO basta este bool: exige-se também
	// [Config.SelfAttestationAcknowledged], para que a degradação seja deliberada e VISÍVEL na
	// configuração ([ErrConfigSelfAttestationAck]). E, mesmo permitida, uma prova
	// self-attested NÃO é aceite pela porta de dual-control
	// ([Verifier.VerifyDeviceAttestation]) — só por [Verifier.Verify] (auditoria/registo).
	AllowSelfAttestation bool
	// SelfAttestationAcknowledged é o reconhecimento EXPLÍCITO de que AllowSelfAttestation
	// rebaixa a allowlist de AAGUID a auto-declarada. Sem ele, [New] recusa a configuração.
	SelfAttestationAcknowledged bool
	// AllowU2FLegacy autoriza o formato legado "fido-u2f". É um campo PRÓPRIO — e não uma
	// consequência de se pôr o AAGUID todo-a-zeros na allowlist — porque esse acoplamento era
	// perigoso: o AAGUID nulo na allowlist admitia-o TAMBÉM no caminho `packed`, onde
	// deixaria de haver filtro de MODELO efectivo. Com este campo, o AAGUID nulo é aceite
	// APENAS em fido-u2f e é sempre RECUSADO em packed ([ErrZeroAAGUIDPacked]).
	AllowU2FLegacy bool
	// RequireUserVerification exige a flag UV (PIN/biometria), não bastando a presença
	// física (UP). UP é SEMPRE exigida.
	RequireUserVerification bool
	// AllowCertWithoutAAGUIDExtension tolera certificados de attestation sem a extensão
	// OID 1.3.6.1.4.1.45724.1.1.4. Por omissão (false) a extensão é EXIGIDA quando há x5c:
	// é ela que amarra o certificado ao modelo declarado no authData.
	AllowCertWithoutAAGUIDExtension bool
	// --- REVOGAÇÃO / ESTADO DO AUTENTICADOR (verificada OFFLINE, fail-closed) ---
	//
	// A cadeia x5c ser válida não diz nada sobre estar REVOGADA. Um certificado de attestation
	// revogado — ou um MODELO cuja chave de lote foi extraída, cenário já real na indústria —
	// continuaria a passar durante os ANOS de validade do certificado, e a única remediação
	// seria editar a allowlist e redeployar. Para um controlo que autoriza acções
	// IRREVERSÍVEIS, isso é um fail-open temporal. Estes três canais fecham-no; são consultas
	// OFFLINE (sem OCSP/rede no caminho de decisão: uma verificação online seria um novo modo
	// de falha e uma fuga de metadados).
	//
	// RevokedAAGUIDs é a denylist de MODELOS (ex.: um snapshot assinado do FIDO MDS traduzido
	// para AAGUIDs comprometidos). Tem PRECEDÊNCIA sobre a allowlist: um AAGUID que conste das
	// duas é RECUSADO ([ErrAAGUIDRevoked]).
	RevokedAAGUIDs [][16]byte
	// RevokedCertSerials são os números de série (hex, com ou sem separadores/0x, sem
	// sensibilidade a maiúsculas) de certificados de attestation revogados. Aplica-se à FOLHA
	// e a TODOS os intermediários da cadeia ([ErrCertificateRevoked]).
	RevokedCertSerials []string
	// CRLs são listas de revogação já parseadas (carregadas de ficheiro pelo deployment). A
	// folha e os intermediários são confrontados com as entradas de TODAS elas. A
	// PERIODICIDADE de actualização é um requisito de deployment: uma CRL velha revoga menos
	// do que devia, e este verificador não vai à rede buscá-la.
	CRLs []*x509.RevocationList

	// MaxAttestationObjectBytes / MaxClientDataBytes — limites anti-DoS. <=0 ⇒ defaults.
	MaxAttestationObjectBytes int
	MaxClientDataBytes        int
	// Now é a fonte de tempo para a validade dos certificados (injectável em teste).
	// nil ⇒ time.Now.
	Now func() time.Time
}

// Attested é o resultado de uma verificação BEM-SUCEDIDA. É a visão RICA (para auditoria do
// componente de autoridade); a porta consumida pelo gate 4-eyes só leva o [Attested.DeviceID]
// opaco.
type Attested struct {
	// Format é o fmt verificado ("packed" ou "fido-u2f").
	Format string
	// AAGUID é o identificador de MODELO do autenticador (não identifica o utilizador).
	AAGUID [16]byte
	// CredentialID é o handle da credencial atestada.
	CredentialID []byte
	// UserPresent / UserVerified são as flags efectivamente observadas no authData.
	UserPresent  bool
	UserVerified bool
	// SignCount é o contador do autenticador (0 é legítimo em muitos dispositivos).
	SignCount uint32
	// SelfAttested indica que a prova foi packed self-attestation (sem cadeia x5c): o
	// modelo é AUTO-DECLARADO, não certificado por um terceiro. Fica explícito para que o
	// audit não confunda as duas forças de prova.
	SelfAttested bool
	// DeviceID é o identificador OPACO e estável do dispositivo — SHA-256 sobre
	// (domínio ‖ AAGUID ‖ credentialID) com codificação injectiva. É o que distingue
	// dispositivos sem expor o handle da credencial.
	DeviceID []byte
}

// Verifier verifica attestations WebAuthn segundo uma [Config] imutável. É seguro para uso
// concorrente (não tem estado mutável). Construir com [New].
type Verifier struct {
	cfg      Config
	rpIDHash [32]byte
	origins  map[string]struct{}
	aaguids  map[[16]byte]struct{}
	// revokedAAGUIDs / revokedSerials são as denylists normalizadas (ver [Config]).
	revokedAAGUIDs map[[16]byte]struct{}
	revokedSerials map[string]struct{}
	maxAtt         int
	maxCD          int
	now            func() time.Time
}

// New valida a política e constrói o verificador. Fail-closed: RPID vazio, sem origens ou
// sem allowlist de AAGUID ⇒ erro, não um verificador permissivo.
func New(cfg Config) (*Verifier, error) {
	if cfg.RPID == "" {
		return nil, ErrConfigRPID
	}
	if len(cfg.AllowedOrigins) == 0 {
		return nil, ErrConfigOrigins
	}
	if len(cfg.AllowedAAGUIDs) == 0 {
		return nil, ErrConfigAAGUIDs
	}
	// A self-attestation rebaixa a allowlist de modelos a AUTO-DECLARADA: exige-se um
	// reconhecimento explícito na config para que a degradação não passe num bool discreto.
	if cfg.AllowSelfAttestation && !cfg.SelfAttestationAcknowledged {
		return nil, ErrConfigSelfAttestationAck
	}
	v := &Verifier{
		cfg:            cfg,
		rpIDHash:       sha256.Sum256([]byte(cfg.RPID)),
		origins:        make(map[string]struct{}, len(cfg.AllowedOrigins)),
		aaguids:        make(map[[16]byte]struct{}, len(cfg.AllowedAAGUIDs)),
		revokedAAGUIDs: make(map[[16]byte]struct{}, len(cfg.RevokedAAGUIDs)),
		revokedSerials: make(map[string]struct{}, len(cfg.RevokedCertSerials)),
		maxAtt:         cfg.MaxAttestationObjectBytes,
		maxCD:          cfg.MaxClientDataBytes,
		now:            cfg.Now,
	}
	for _, a := range cfg.RevokedAAGUIDs {
		v.revokedAAGUIDs[a] = struct{}{}
	}
	for _, s := range cfg.RevokedCertSerials {
		n := normalizeSerial(s)
		if n == "" {
			// Uma entrada de denylist ilegível seria uma revogação que não revoga nada —
			// recusa-se a configuração em vez de a ignorar em silêncio.
			return nil, fmt.Errorf("%w: %q", ErrConfigRevokedSerial, s)
		}
		v.revokedSerials[n] = struct{}{}
	}
	for _, o := range cfg.AllowedOrigins {
		if o == "" {
			return nil, ErrConfigOrigins
		}
		v.origins[o] = struct{}{}
	}
	for _, a := range cfg.AllowedAAGUIDs {
		v.aaguids[a] = struct{}{}
	}
	if v.maxAtt <= 0 {
		v.maxAtt = DefaultMaxAttestationObjectBytes
	}
	if v.maxCD <= 0 {
		v.maxCD = DefaultMaxClientDataBytes
	}
	if v.now == nil {
		v.now = time.Now
	}
	return v, nil
}

// VerifyDeviceAttestation satisfaz ESTRUTURALMENTE a porta
// [integration.DeviceAttestationVerifier] (packages/integration/device_attestation.go):
// bytes e erros, sem um único tipo de CBOR na assinatura — é o que permite que este módulo
// (e a sua dependência) nunca entre no grafo de build do nó.
//
// Devolve APENAS o identificador opaco do dispositivo. Quem precise do AAGUID/credentialID
// para auditoria usa [Verifier.Verify].
//
// FORÇA DE PROVA: esta porta é consumida pelo dual-control, que autoriza acções
// IRREVERSÍVEIS, e o seu resultado (um deviceID) NÃO consegue transportar a distinção entre
// uma prova certificada por terceiro e uma AUTO-DECLARADA. Em vez de a deixar cair em
// silêncio, recusa-se aqui a prova self-attested ([ErrSelfAttestationNotAcceptedByPort]),
// mesmo com [Config.AllowSelfAttestation] ligada — nesse modo a allowlist de AAGUID é
// auto-declarada pelo autenticador e um software authenticator seria indistinguível de uma
// chave certificada. A self-attestation continua disponível por [Verifier.Verify], onde
// [Attested.SelfAttested] deixa a força da prova explícita para o audit.
func (v *Verifier) VerifyDeviceAttestation(ctx context.Context, attestationObject, clientDataJSON, expectedChallenge []byte) ([]byte, error) {
	att, err := v.Verify(ctx, attestationObject, clientDataJSON, expectedChallenge)
	if err != nil {
		return nil, err
	}
	if att.SelfAttested {
		return nil, ErrSelfAttestationNotAcceptedByPort
	}
	return att.DeviceID, nil
}

// Verify executa a verificação COMPLETA e devolve a visão rica. Ordem (tudo tem de passar;
// o mais barato primeiro para não gastar criptografia com lixo):
//
//  1. limites de tamanho e cancelamento do contexto;
//  2. clientDataJSON: type == "webauthn.create", challenge == esperado (TEMPO CONSTANTE),
//     origin na allowlist;
//  3. attestationObject: CBOR com limites ⇒ fmt / attStmt / authData;
//  4. authData: parse binário com bounds ⇒ rpIdHash, flags, signCount, AAGUID,
//     credentialId, credentialPublicKey (COSE);
//  5. rpIdHash == SHA-256(rpId); flags UP (sempre) e UV (se exigida); flag AT obrigatória;
//  6. AAGUID na allowlist (default-deny);
//  7. attStmt conforme o fmt (packed com x5c / packed self / fido-u2f; "none" recusado),
//     incluindo cadeia x509 e coerência do AAGUID do certificado.
//
// Qualquer falha ⇒ ([Attested] zero, erro). Nunca há resultado parcial utilizável.
func (v *Verifier) Verify(ctx context.Context, attestationObject, clientDataJSON, expectedChallenge []byte) (Attested, error) {
	if err := ctx.Err(); err != nil {
		return Attested{}, err
	}
	if len(attestationObject) == 0 || len(attestationObject) > v.maxAtt {
		return Attested{}, fmt.Errorf("%w: attestationObject %d bytes (máx %d)", ErrInputTooLarge, len(attestationObject), v.maxAtt)
	}
	if len(clientDataJSON) == 0 || len(clientDataJSON) > v.maxCD {
		return Attested{}, fmt.Errorf("%w: clientDataJSON %d bytes (máx %d)", ErrInputTooLarge, len(clientDataJSON), v.maxCD)
	}
	if len(expectedChallenge) == 0 {
		return Attested{}, ErrNoChallenge
	}

	// (2) clientData — barato e decisivo (liga a attestation ao challenge da perna).
	if err := v.checkClientData(clientDataJSON, expectedChallenge); err != nil {
		return Attested{}, err
	}
	clientDataHash := sha256.Sum256(clientDataJSON)

	// (3) attestationObject CBOR.
	obj, err := decodeAttestationObject(attestationObject)
	if err != nil {
		return Attested{}, err
	}

	// (4) authData binário.
	ad, err := parseAuthData(obj.AuthData)
	if err != nil {
		return Attested{}, err
	}

	// (5) rpId e flags.
	if ad.RPIDHash != v.rpIDHash {
		return Attested{}, ErrRPIDMismatch
	}
	if !ad.UserPresent() {
		return Attested{}, ErrUserNotPresent
	}
	if v.cfg.RequireUserVerification && !ad.UserVerified() {
		return Attested{}, ErrUserNotVerified
	}
	if !ad.AttestedCredential() {
		return Attested{}, ErrNoAttestedCredential
	}

	// (6) ALLOWLIST de AAGUID — default-deny sobre o MODELO do autenticador. A DENYLIST tem
	// precedência: um modelo comprometido é recusado mesmo que continue na allowlist (é o
	// canal de revogação de modelo, sem esperar por um redeploy da allowlist).
	if _, revoked := v.revokedAAGUIDs[ad.AAGUID]; revoked {
		return Attested{}, fmt.Errorf("%w: %s", ErrAAGUIDRevoked, hex.EncodeToString(ad.AAGUID[:]))
	}
	if _, ok := v.aaguids[ad.AAGUID]; !ok {
		return Attested{}, fmt.Errorf("%w: %s", ErrAAGUIDNotAllowed, hex.EncodeToString(ad.AAGUID[:]))
	}

	// (7) attStmt conforme o fmt.
	selfAttested := false
	switch obj.Fmt {
	case "packed":
		// O AAGUID todo-a-zeros é o do LEGADO U2F ("não há modelo declarado"). Admiti-lo no
		// caminho packed anularia o filtro de MODELO — e era o efeito colateral de se pôr o
		// AAGUID nulo na allowlist para aceitar U2F. Aqui é sempre recusado.
		if ad.AAGUID == ([16]byte{}) {
			err = ErrZeroAAGUIDPacked
			break
		}
		selfAttested, err = v.verifyPacked(obj.AttStmt, ad, clientDataHash[:])
	case "fido-u2f":
		// O legado exige opt-in PRÓPRIO (não se deduz da allowlist).
		if !v.cfg.AllowU2FLegacy {
			err = ErrU2FNotAllowed
			break
		}
		err = v.verifyU2F(obj.AttStmt, ad, clientDataHash[:])
	case "none":
		err = ErrNoneAttestation
	default:
		err = fmt.Errorf("%w: %q", ErrUnsupportedFormat, sanitizeFmt(obj.Fmt))
	}
	if err != nil {
		return Attested{}, err
	}

	return Attested{
		Format:       obj.Fmt,
		AAGUID:       ad.AAGUID,
		CredentialID: append([]byte(nil), ad.CredentialID...),
		UserPresent:  ad.UserPresent(),
		UserVerified: ad.UserVerified(),
		SignCount:    ad.SignCount,
		SelfAttested: selfAttested,
		DeviceID:     deviceID(ad.AAGUID, ad.CredentialID),
	}, nil
}

// deviceID deriva o identificador OPACO e estável do dispositivo a partir do par
// (AAGUID, credentialID), com codificação INJECTIVA (comprimentos em 8 bytes big-endian
// antes de cada campo variável) sob um separador de domínio — dois pares distintos nunca
// produzem o mesmo digest por deslize de fronteira. Publicá-lo não revela o handle da
// credencial (pré-imagem de SHA-256), e o consumidor só precisa de o COMPARAR.
//
// O QUE ESTE IDENTIFICADOR É E NÃO É. É o identificador de uma CREDENCIAL WebAuthn ATESTADA
// (modelo + handle da credencial). NÃO é a identidade do autenticador FÍSICO, e a attestation
// WebAuthn não a pode dar: o AAGUID identifica o MODELO e os certificados de attestation são
// de LOTE (partilhados por >=100k unidades) precisamente para impedir correlação de
// dispositivo. Duas credenciais criadas no MESMO autenticador produzem deviceIDs DISTINTOS.
// Um consumidor que precise de atribuição a uma pessoa tem de a obter por ENROLLMENT
// (aprovador → deviceID registado), não deste digest.
func deviceID(aaguid [16]byte, credentialID []byte) []byte {
	h := sha256.New()
	writeLenPrefixed(h, []byte(deviceIDDomain))
	writeLenPrefixed(h, aaguid[:])
	writeLenPrefixed(h, credentialID)
	return h.Sum(nil)
}

// writeLenPrefixed escreve len(b) em 8 bytes big-endian seguido de b.
func writeLenPrefixed(h interface{ Write([]byte) (int, error) }, b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	_, _ = h.Write(n[:])
	_, _ = h.Write(b)
}

// normalizeSerial põe um número de série em forma canónica para comparação: minúsculas, sem
// "0x", sem separadores (":", "-", espaços) e sem zeros à esquerda. Devolve "" se sobrar algo
// que não seja hex — uma entrada de denylist ilegível é um erro de configuração, não um
// silêncio. A forma canónica é a mesma que [certSerialKey] produz a partir do certificado.
func normalizeSerial(s string) string {
	in := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "0x")
	cleaned := make([]rune, 0, len(in))
	for _, r := range in {
		switch {
		case r == ':' || r == '-' || r == ' ' || r == '_':
			continue
		case (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f'):
			cleaned = append(cleaned, r)
		default:
			return ""
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	out := strings.TrimLeft(string(cleaned), "0")
	if out == "" {
		return "0" // série 0 é legítima
	}
	return out
}

// certSerialKey devolve a forma canónica do número de série de um certificado (hex sem zeros
// à esquerda), comparável com [normalizeSerial].
func certSerialKey(c *x509.Certificate) string {
	if c == nil || c.SerialNumber == nil {
		return ""
	}
	h := strings.TrimLeft(strings.ToLower(c.SerialNumber.Text(16)), "0")
	if h == "" {
		return "0"
	}
	return h
}

// sanitizeFmt limita o fmt desconhecido que entra numa mensagem de erro: entrada hostil não
// dita o tamanho (nem o conteúdo bruto) do que vai para os logs.
func sanitizeFmt(s string) string {
	const max = 32
	out := make([]rune, 0, max)
	for _, r := range s {
		if len(out) == max {
			break
		}
		if r < 0x20 || r == 0x7f {
			r = '?'
		}
		out = append(out, r)
	}
	return string(out)
}
