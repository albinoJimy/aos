package attestation

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
)

// clientDataCreateType é o único type aceite: esta é a cerimónia de REGISTO/attestation
// (navigator.credentials.create). Aceitar "webauthn.get" permitiria reciclar uma asserção de
// autenticação como se fosse prova de dispositivo atestado.
const clientDataCreateType = "webauthn.create"

// collectedClientData é o subconjunto do CollectedClientData que a decisão usa. Campos
// desconhecidos são IGNORADOS de propósito (a estrutura é extensível por desenho:
// tokenBinding, topOrigin, ...) — o que NÃO se ignora são os bytes: a assinatura de
// attestation cobre SHA-256 dos bytes ORIGINAIS, nunca esta re-interpretação.
type collectedClientData struct {
	Type        string `json:"type"`
	Challenge   string `json:"challenge"`
	Origin      string `json:"origin"`
	CrossOrigin bool   `json:"crossOrigin"`
}

// checkClientData valida type, challenge e origin.
//
// O challenge vem em base64url SEM padding (WebAuthn). Compara-se em TEMPO CONSTANTE
// (crypto/subtle) contra o esperado: embora o challenge não seja um segredo de longo prazo,
// uma comparação com curto-circuito daria um oráculo byte-a-byte a quem consiga submeter
// tentativas, e fazê-lo bem custa zero.
//
// crossOrigin=true é RECUSADO: uma cerimónia iniciada dentro de um iframe de terceiros não é
// a prova de intenção deliberada que o 4-eyes exige.
func (v *Verifier) checkClientData(clientDataJSON, expectedChallenge []byte) error {
	var cd collectedClientData
	dec := json.NewDecoder(bytes.NewReader(clientDataJSON))
	if err := dec.Decode(&cd); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformedClientData, err)
	}
	// Bytes a mais depois do objecto JSON ⇒ entrada ambígua (dois parsers poderiam ler
	// coisas diferentes dos mesmos bytes). Recusa-se.
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("%w: dados excedentes após o objecto JSON", ErrMalformedClientData)
	}

	if cd.Type != clientDataCreateType {
		return fmt.Errorf("%w: type=%q", ErrWrongClientDataType, sanitizeFmt(cd.Type))
	}
	if cd.CrossOrigin {
		return fmt.Errorf("%w: cerimónia cross-origin", ErrOriginNotAllowed)
	}
	if _, ok := v.origins[cd.Origin]; !ok {
		return fmt.Errorf("%w: %q", ErrOriginNotAllowed, sanitizeFmt(cd.Origin))
	}

	got, err := base64.RawURLEncoding.DecodeString(cd.Challenge)
	if err != nil {
		return fmt.Errorf("%w: challenge não é base64url sem padding", ErrMalformedClientData)
	}
	if subtle.ConstantTimeCompare(got, expectedChallenge) != 1 {
		return ErrChallengeMismatch
	}
	return nil
}
