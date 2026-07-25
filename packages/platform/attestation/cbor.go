package attestation

import (
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// Este é o ÚNICO ficheiro do repositório onde a dependência CBOR é usada para descodificar
// dados NÃO-CONFIÁVEIS. O modo de descodificação é ENDURECIDO uma vez, aqui, e todo o resto
// do pacote passa por ele — não há `cbor.Unmarshal` avulso com opções permissivas.

// decMode é o descodificador CBOR endurecido para entrada hostil:
//
//   - MaxNestedLevels/MaxArrayElements/MaxMapPairs: tectos baixos (uma attestation legítima
//     é rasa e pequena) — travam bombas de aninhamento/cardinalidade antes de alocarem;
//   - IndefLengthForbidden: comprimentos indefinidos permitiriam streams sem tecto declarado;
//   - TagsForbidden: nenhuma tag CBOR é legítima aqui, e tags são superfície de ataque grátis;
//   - DupMapKeyEnforcedAPF: chaves duplicadas são erro, não "a última ganha" — impede
//     divergências de parsing entre implementações (um verificador vê X, outro vê Y);
//   - UTF8RejectInvalid: strings inválidas não passam a silenciar-se em texto trocado.
//
// Extraneous data é erro por construção do fxamacker (Unmarshal recusa bytes a mais), o que
// fecha o "append lixo depois do objecto".
var decMode cbor.DecMode

func init() {
	dm, err := cbor.DecOptions{
		MaxNestedLevels:  8,
		MaxArrayElements: 64,
		MaxMapPairs:      64,
		IndefLength:      cbor.IndefLengthForbidden,
		TagsMd:           cbor.TagsForbidden,
		DupMapKey:        cbor.DupMapKeyEnforcedAPF,
		UTF8:             cbor.UTF8RejectInvalid,
	}.DecMode()
	if err != nil {
		// Opções constantes e válidas: um erro aqui é um bug de programação, não uma
		// condição de execução — falhar cedo e alto é a postura correcta.
		panic("attestation: modo CBOR inválido: " + err.Error())
	}
	decMode = dm
}

// attestationObject é a forma canónica do attestationObject WebAuthn: um mapa CBOR com
// chaves-string fmt/attStmt/authData. attStmt fica CRU (a sua forma depende do fmt e só é
// descodificada depois de sabermos qual é).
type attestationObject struct {
	Fmt      string          `cbor:"fmt"`
	AttStmt  cbor.RawMessage `cbor:"attStmt"`
	AuthData []byte          `cbor:"authData"`
}

// decodeAttestationObject descodifica e faz as verificações estruturais mínimas. Qualquer
// desvio ⇒ [ErrMalformedAttestation] (fail-closed, sem panic).
func decodeAttestationObject(raw []byte) (*attestationObject, error) {
	var obj attestationObject
	if err := decMode.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedAttestation, err)
	}
	if obj.Fmt == "" {
		return nil, fmt.Errorf("%w: fmt vazio", ErrMalformedAttestation)
	}
	if len(obj.AuthData) == 0 {
		return nil, fmt.Errorf("%w: authData vazio", ErrMalformedAttestation)
	}
	// "none" é o único fmt legítimo sem attStmt; qualquer outro tem de o trazer, e a sua
	// ausência é recusada aqui em vez de produzir um "verificado" vacuoso mais abaixo.
	if len(obj.AttStmt) == 0 && obj.Fmt != "none" {
		return nil, fmt.Errorf("%w: attStmt vazio", ErrMalformedAttestation)
	}
	return &obj, nil
}
