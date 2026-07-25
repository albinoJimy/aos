package integration

import (
	"context"
	"errors"
)

// Este ficheiro define a PORTA de ATTESTATION DE DISPOSITIVO (AOS-177, EPIC-16 frente 4 —
// a última da Opção A do D4). É o que faz o 4-eyes deixar de ser SÓ estrutural: com a porta
// ligada, cada perna tem de trazer uma attestation WebAuthn VERIFICÁVEL, e as duas pernas
// têm de vir de DISPOSITIVOS ATESTADOS DISTINTOS — a exigência literal do ADR-016 §4
// ("duas credenciais WebAuthn atestadas e distintas"), que a igualdade-de-string de
// principal/sessão/credencial (STUB necessário-mas-insuficiente) não conseguia provar.
//
// A PORTA É STDLIB PURA — E ISSO É UMA INVARIANTE DE SUPPLY-CHAIN, NÃO ESTÉTICA.
// A verificação real de WebAuthn precisa de um descodificador CBOR/COSE (o attestationObject
// é CBOR). A Carta (emenda 1.3, exceção ESCOPADA ao ADR-017) autoriza essa dependência
// APENAS no componente de autoridade EXTERNO — o binário do nó fica zero-dep. Por isso:
//
//   - AQUI (packages/integration, que o nó importa) vive SÓ o contrato: bytes e erros.
//     Nenhum tipo de CBOR/WebAuthn atravessa esta fronteira. O módulo `integration` NÃO
//     ganha a dependência — provado executavelmente por [TestDepIsolation_IntegrationBuildGraphExcludesAttestationDeps].
//   - A IMPLEMENTAÇÃO concreta vive num MÓDULO SEPARADO (packages/platform/attestation,
//     go.mod próprio, única dep externa github.com/fxamacker/cbor/v2) que o nó NUNCA importa
//     — provado por packages/cmd/aos/dep_isolation_test.go (`go list -deps`).
//
// O contrato é satisfeito ESTRUTURALMENTE (Go interfaces são estruturais): a implementação
// não precisa de importar este pacote para o cumprir, pelo que a dependência nunca entra no
// grafo do nó por essa via. A conformidade da assinatura é guardada por um teste do módulo
// de attestation que PARSEIA este ficheiro (drift detectado, não confiado à prosa).
//
// LIGAÇÃO PERNA↔ATTESTATION, E O QUE ELA VALE. A porta recebe o CHALLENGE ESPERADO, e o gate
// passa-lhe o [ApprovalLeg.Challenge] — que já está DENTRO do tuplo assinado ed25519 da perna.
// O clientDataJSON da attestation tem de conter EXACTAMENTE esse challenge, pelo que uma
// attestation só serve na perna cujo challenge ela contém. Não foi preciso alterar o formato
// do tuplo assinado para obter esta ligação.
//
// ATENÇÃO AO ALCANCE (corrigido na remediação de AOS-177): a ligação pelo challenge dá
// COERÊNCIA (a attestation é daquele challenge), não FRESCURA — e só dá frescura quando o
// challenge é EMITIDO PELO SERVIDOR. Se o challenge for escolhido pelo cliente, e o
// anti-replay for apenas "nunca visto" ([NonceConsumer] append-if-empty), quem detenha a chave
// ed25519 de um aprovador pode reutilizar o challenge de um clientDataJSON capturado (é
// público) num pedido NOVO e reapresentar a MESMA attestation indefinidamente. Por isso o gate
// tem a porta [ChallengeIssuance] ([WithChallengeIssuance]): com ela ligada, o challenge da
// perna tem de constar do REGISTO DE EMISSÃO para (pedido, aprovador) e estar dentro do TTL.
// SEM ela, esta attestation prova modelo+posse-de-credencial, NÃO liveness por-cerimónia.

// Erros da attestation de dispositivo no gate 4-eyes (todos fail-closed).
var (
	// ErrNilDeviceAttestationVerifier — [WithDeviceAttestation] recebeu um verificador nil.
	// Fail-closed: pedir attestation e passar nil daria um gate que SILENCIOSAMENTE não a
	// exige — pior que não a pedir. Recusa-se a construção.
	ErrNilDeviceAttestationVerifier = errors.New("integration: four-eyes: verificador de attestation nil (fail-closed)")
	// ErrMissingDeviceAttestation — a porta está ligada mas a perna não traz
	// attestationObject e/ou clientDataJSON. Sem prova de dispositivo, a perna é recusada.
	ErrMissingDeviceAttestation = errors.New("integration: four-eyes: perna sem attestation de dispositivo (exigida)")
	// ErrDeviceAttestationRejected — o verificador RECUSOU a attestation (CBOR malformado,
	// AAGUID fora da allowlist, cadeia x509 não-confiável, challenge/origin/rpId errados,
	// flags em falta, assinatura inválida, fmt "none"...). Envolve o erro concreto do
	// verificador, que não transporta segredos.
	ErrDeviceAttestationRejected = errors.New("integration: four-eyes: attestation de dispositivo recusada")
	// ErrSameDevice — as duas pernas apresentam a MESMA CREDENCIAL WebAuthn ATESTADA (mesmo
	// AAGUID de modelo + mesmo credential-id ⇒ mesmo identificador de dispositivo).
	//
	// LIMITE HONESTO DA GARANTIA: isto NÃO prova dois autenticadores FÍSICOS distintos e a
	// attestation WebAuthn não o pode provar — o AAGUID identifica o MODELO e os certificados
	// de attestation são de LOTE (partilhados por >=100k unidades) PRECISAMENTE para impedir
	// correlação de dispositivo. Duas credenciais registadas no MESMO autenticador têm
	// credential-ids distintos e passam esta regra. O que fica imposto é: "duas credenciais
	// WebAuthn ATESTADAS e DISTINTAS, de modelos na allowlist, com prova de presença do
	// utilizador em cada perna". Para aproximar a distinção física é preciso outra via —
	// [DeviceEnrollment] por aprovador (abaixo) e/ou política organizacional de modelos
	// distintos por aprovador.
	ErrSameDevice = errors.New("integration: four-eyes: mesma credencial WebAuthn atestada nas duas pernas (recusado)")
	// ErrNilDeviceEnrollment — [WithDeviceEnrollment] recebeu um registo nil (fail-closed).
	ErrNilDeviceEnrollment = errors.New("integration: four-eyes: registo de enrollment de dispositivos nil (fail-closed)")
	// ErrEnrollmentWithoutAttestation — [WithDeviceEnrollment] sem [WithDeviceAttestation]:
	// não há deviceID a confrontar com o enrollment. Recusa-se a construção em vez de
	// guardar uma porta que nunca seria consultada.
	ErrEnrollmentWithoutAttestation = errors.New("integration: four-eyes: enrollment de dispositivos exige a porta de attestation ligada")
	// ErrDeviceNotEnrolled — o dispositivo atestado NÃO está registado para o APROVADOR que
	// assinou a perna. Sem isto, qualquer autenticador de um modelo na allowlist serviria
	// qualquer perna de qualquer aprovador (a attestation não acrescentaria ATRIBUIÇÃO).
	// Default-deny: registo vazio ⇒ nega.
	ErrDeviceNotEnrolled = errors.New("integration: four-eyes: dispositivo atestado não registado para este aprovador (default-deny)")
	// ErrDeviceEnrollmentBackend — erro do backend do registo de enrollment; fail-closed.
	ErrDeviceEnrollmentBackend = errors.New("integration: four-eyes: falha do registo de enrollment de dispositivos (fail-closed)")
)

// DeviceAttestationVerifier é a PORTA de verificação de attestation de dispositivo
// (WebAuthn/AAGUID). Contrato STDLIB PURO: entram bytes opacos, sai um identificador opaco
// de dispositivo ou um erro. A política (allowlist de AAGUID, rpId, origins permitidas,
// exigência de user-verification, âncoras de confiança x509) vive NA IMPLEMENTAÇÃO — o gate
// não a conhece nem a pode rebaixar.
//
// A implementação de referência é [github.com/aos-ref/platform/attestation].Verifier, num
// MÓDULO SEPARADO (a dep CBOR não pode entrar no grafo do nó — Carta emenda 1.3). Como o
// contrato é estrutural, essa implementação satisfaz esta interface SEM importar este
// pacote.
type DeviceAttestationVerifier interface {
	// VerifyDeviceAttestation verifica uma attestation WebAuthn e devolve o IDENTIFICADOR
	// OPACO E ESTÁVEL do dispositivo atestado — derivado do AAGUID (modelo do autenticador)
	// e do credential-id, e é SÓ isso que o gate compara para exigir dispositivos distintos.
	// Não devolve PII nem material de credencial em claro.
	//
	//   - attestationObject: o attestationObject WebAuthn (CBOR: fmt/attStmt/authData).
	//   - clientDataJSON: os bytes EXACTOS do clientDataJSON (o hash cobre-os).
	//   - expectedChallenge: o challenge que TEM de constar do clientDataJSON. O gate passa
	//     o challenge da perna (já coberto pela assinatura ed25519 da perna), ligando a
	//     attestation àquela perna daquele pedido.
	//
	// FAIL-CLOSED: qualquer falha (incluindo entrada malformada) devolve erro e nunca um
	// deviceID utilizável. Um deviceID vazio com erro nil é tratado pelo gate como recusa.
	VerifyDeviceAttestation(ctx context.Context, attestationObject, clientDataJSON, expectedChallenge []byte) (deviceID []byte, err error)
}

// DeviceEnrollment é a porta de ATRIBUIÇÃO dispositivo↔aprovador (remediação AOS-177).
//
// A attestation prova que a credencial vem de um MODELO permitido e que houve presença do
// utilizador; NÃO diz de QUEM é o dispositivo. Sem esta porta, qualquer autenticador de um
// modelo na allowlist satisfaz QUALQUER perna de QUALQUER aprovador, e a única prova de
// "quem assinou" continua a ser a chave ed25519 do registo — ou seja, um insider com UM
// autenticador permitido (duas credenciais) satisfaria as duas pernas do dual-control.
//
// Com ela ligada, o deviceID devolvido pela attestation tem de constar do enrollment DO
// APROVADOR NOMEADO na perna. É a mesma ideia do pin de pubkey do [hitl.ApproverRegistry],
// aplicada ao dispositivo. STDLIB PURA: o gate só vê bytes opacos.
type DeviceEnrollment interface {
	// IsEnrolled reporta se `deviceID` (o identificador opaco devolvido pela attestation)
	// está registado para `approver`. DEFAULT-DENY: desconhecido ⇒ (false, nil) ⇒ o gate
	// nega com [ErrDeviceNotEnrolled]. Erro ⇒ [ErrDeviceEnrollmentBackend] (também nega).
	IsEnrolled(ctx context.Context, approver string, deviceID []byte) (bool, error)
}

// StaticDeviceEnrollment é um [DeviceEnrollment] IMUTÁVEL construído a partir da configuração
// da organização (aprovador → identificadores de dispositivo registados). Default-deny por
// construção: um aprovador sem entradas não tem dispositivo nenhum registado.
//
// Os deviceIDs NÃO são segredos (são digests SHA-256 opacos), pelo que a comparação é por
// igualdade simples — não há material de credencial a proteger de timing.
type StaticDeviceEnrollment struct {
	// byApprover mapeia aprovador → conjunto de deviceIDs (chaveado pela string dos bytes).
	byApprover map[string]map[string]struct{}
}

// NewStaticDeviceEnrollment constrói o registo a partir de aprovador → deviceIDs. As entradas
// são COPIADAS (o chamador não pode mutar o registo depois). Entradas com aprovador vazio ou
// deviceID vazio são DESCARTADAS: um deviceID vazio nunca pode casar (o gate já recusa um
// deviceID vazio) e uma entrada anónima só serviria para enganar quem lesse a config.
func NewStaticDeviceEnrollment(devices map[string][][]byte) *StaticDeviceEnrollment {
	e := &StaticDeviceEnrollment{byApprover: make(map[string]map[string]struct{}, len(devices))}
	for approver, ids := range devices {
		if approver == "" {
			continue
		}
		for _, id := range ids {
			if len(id) == 0 {
				continue
			}
			set, ok := e.byApprover[approver]
			if !ok {
				set = make(map[string]struct{}, len(ids))
				e.byApprover[approver] = set
			}
			set[string(id)] = struct{}{}
		}
	}
	return e
}

// IsEnrolled implementa [DeviceEnrollment] (default-deny, sem I/O, seguro para uso
// concorrente — o registo é imutável depois da construção).
func (e *StaticDeviceEnrollment) IsEnrolled(_ context.Context, approver string, deviceID []byte) (bool, error) {
	if e == nil || approver == "" || len(deviceID) == 0 {
		return false, nil
	}
	set, ok := e.byApprover[approver]
	if !ok {
		return false, nil
	}
	_, ok = set[string(deviceID)]
	return ok, nil
}
