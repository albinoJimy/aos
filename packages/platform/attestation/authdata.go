package attestation

import (
	"encoding/binary"
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// Flags do authenticator data (WebAuthn §6.1). Só UP/UV/AT/ED nos interessam; os restantes
// bits (BE/BS, backup eligibility/state) são transportados sem interpretação.
const (
	flagUP byte = 1 << 0 // user present (interacção física)
	flagUV byte = 1 << 2 // user verified (PIN/biometria)
	flagAT byte = 1 << 6 // attested credential data presente
	flagED byte = 1 << 7 // extension data presente
)

const (
	// authDataFixedLen = rpIdHash(32) + flags(1) + signCount(4).
	authDataFixedLen = 37
	// maxCredentialIDLen — tecto do credential id (WebAuthn recomenda <= 1023 bytes).
	maxCredentialIDLen = 1023
)

// authData é o authenticator data JÁ PARSEADO. O parse é puramente binário e TODOS os
// índices são validados contra o comprimento restante ANTES de qualquer fatia — entrada
// truncada ou com comprimentos mentirosos devolve erro, nunca causa panic.
//
// Layout:
//
//	rpIdHash(32) ‖ flags(1) ‖ signCount(4)
//	  [se AT] aaguid(16) ‖ credentialIdLen(2 BE) ‖ credentialId ‖ credentialPublicKey(COSE)
//	  [se ED] extensions (mapa CBOR)
type authData struct {
	RPIDHash  [32]byte
	Flags     byte
	SignCount uint32
	AAGUID    [16]byte
	// CredentialID e CredentialPublicKey são FATIAS DE raw — não se copiam (raw é imutável
	// dentro do verificador e não escapa senão por cópia explícita).
	CredentialID        []byte
	CredentialPublicKey []byte
	// Raw são os bytes ORIGINAIS: é sobre eles (concatenados com o clientDataHash) que a
	// assinatura de attestation é verificada. Reserializar seria um vector de discrepância.
	Raw []byte
}

func (a *authData) UserPresent() bool        { return a.Flags&flagUP != 0 }
func (a *authData) UserVerified() bool       { return a.Flags&flagUV != 0 }
func (a *authData) AttestedCredential() bool { return a.Flags&flagAT != 0 }

// parseAuthData valida e parseia o authenticator data. Fail-closed em cada fronteira.
func parseAuthData(raw []byte) (*authData, error) {
	if len(raw) < authDataFixedLen {
		return nil, fmt.Errorf("%w: %d bytes (mínimo %d)", ErrMalformedAuthData, len(raw), authDataFixedLen)
	}
	ad := &authData{Raw: raw}
	copy(ad.RPIDHash[:], raw[:32])
	ad.Flags = raw[32]
	ad.SignCount = binary.BigEndian.Uint32(raw[33:37])

	rest := raw[authDataFixedLen:]

	if ad.AttestedCredential() {
		// aaguid(16) + credentialIdLen(2)
		if len(rest) < 18 {
			return nil, fmt.Errorf("%w: dados de credencial atestada truncados", ErrMalformedAuthData)
		}
		copy(ad.AAGUID[:], rest[:16])
		credLen := int(binary.BigEndian.Uint16(rest[16:18]))
		rest = rest[18:]
		if credLen == 0 || credLen > maxCredentialIDLen {
			return nil, fmt.Errorf("%w: credentialIdLen inválido (%d)", ErrMalformedAuthData, credLen)
		}
		if len(rest) < credLen {
			// O comprimento DECLARADO excede o disponível: é exactamente o caso que uma
			// fatia ingénua transformaria em panic.
			return nil, fmt.Errorf("%w: credentialId declarado %d, disponível %d", ErrMalformedAuthData, credLen, len(rest))
		}
		ad.CredentialID = rest[:credLen]
		rest = rest[credLen:]

		// credentialPublicKey é um item CBOR (COSE_Key) de comprimento não declarado: pede-se
		// ao descodificador ENDURECIDO o PRIMEIRO item e ficamos com o que sobrou.
		var key cbor.RawMessage
		remaining, err := decMode.UnmarshalFirst(rest, &key)
		if err != nil {
			return nil, fmt.Errorf("%w: credentialPublicKey: %v", ErrMalformedCOSEKey, err)
		}
		if len(key) == 0 {
			return nil, fmt.Errorf("%w: credentialPublicKey vazia", ErrMalformedCOSEKey)
		}
		ad.CredentialPublicKey = key
		rest = remaining
	}

	if ad.Flags&flagED != 0 {
		// Extensões: descodificam-se (com os mesmos limites) só para CONFIRMAR que são CBOR
		// bem-formado e que nada sobra depois. Não influenciam a decisão.
		var ext cbor.RawMessage
		remaining, err := decMode.UnmarshalFirst(rest, &ext)
		if err != nil {
			return nil, fmt.Errorf("%w: extensões: %v", ErrMalformedAuthData, err)
		}
		rest = remaining
	}

	// Bytes a mais depois das secções declaradas: entrada ambígua ⇒ recusa. Um verificador
	// tolerante aqui abriria divergência de parsing com outros (mesma prova, leituras
	// diferentes).
	if len(rest) != 0 {
		return nil, fmt.Errorf("%w: %d bytes excedentes no fim", ErrMalformedAuthData, len(rest))
	}
	return ad, nil
}
