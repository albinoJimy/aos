package integration

import (
	"context"
	"errors"
)

// Este ficheiro define a PORTA DE EMISSÃO DE CHALLENGES do 4-eyes (remediação AOS-177).
//
// PORQUÊ EXISTE. O anti-replay por [NonceConsumer] é um append-if-empty: aceita QUALQUER
// valor NUNCA VISTO. Isso dá DEDUP (o mesmo challenge não se usa duas vezes no mesmo scope),
// mas NÃO dá FRESCURA: o challenge era escolhido pelo CLIENTE, e o scope é por-request_id,
// pelo que cada novo request_id reabria o espaço de nonces. Quem detivesse a chave ed25519 de
// um aprovador podia ler o challenge de um clientDataJSON capturado (o challenge é PÚBLICO —
// vai em base64url dentro do próprio clientDataJSON), reutilizá-lo como challenge de uma perna
// nova e reapresentar indefinidamente a MESMA attestation WebAuthn: a assinatura ed25519 cobre
// o challenge, logo valida, e a attestation contém exactamente esse challenge, logo também
// valida. A prova de posse do autenticador ficava reduzida a "alguém, algures no passado,
// registou uma credencial num modelo permitido".
//
// O QUE ESTA PORTA ACRESCENTA. Com ela ligada, o gate deixa de aceitar "nunca visto" e passa a
// exigir "EMITIDO POR ESTE SERVIDOR, PARA ESTE APROVADOR, PARA ESTE PEDIDO, E AINDA DENTRO DA
// VALIDADE". O espaço de challenges aceitáveis deixa de ser 2^256 valores à escolha do cliente
// e passa a ser o conjunto FINITO e EXPIRÁVEL que o servidor emitiu. Uma attestation capturada
// só serve dentro da janela de emissão do challenge que a acompanha — e o consumo durável
// ([NonceConsumer]) continua a garantir que serve UMA vez.
//
// A porta é STDLIB PURA (bytes e strings), tal como [DeviceAttestationVerifier]: a
// implementação durável de referência é [hitl.EventStoreChallengeIssuer], sobre o mesmo Event
// Store append-only que já sustenta o anti-replay.

// Erros da emissão de challenges no gate (todos fail-closed).
var (
	// ErrNilChallengeIssuance — [WithChallengeIssuance] recebeu uma porta nil. Fail-closed:
	// pedir emissão e não a exigir é pior do que não a pedir.
	ErrNilChallengeIssuance = errors.New("integration: four-eyes: registo de emissão de challenges nil (fail-closed)")
	// ErrChallengeNotIssued — o challenge da perna NÃO consta do registo de emissão para
	// (pedido, aprovador), ou já EXPIROU. Não basta ser inédito: tem de ter sido emitido.
	ErrChallengeNotIssued = errors.New("integration: four-eyes: challenge não emitido pelo servidor para este aprovador/pedido (ou expirado)")
	// ErrChallengeIssuanceBackend — erro do backend do registo de emissão; tratado
	// fail-closed (um registo indisponível NEGA, não autoriza).
	ErrChallengeIssuanceBackend = errors.New("integration: four-eyes: falha do registo de emissão de challenges (fail-closed)")
)

// ChallengeIssuance é a porta de consulta do registo DURÁVEL de challenges EMITIDOS pelo
// servidor. O gate só a lê; a EMISSÃO (que gera o challenge por CSPRNG, o liga a
// (scope, aprovador) e lhe põe um TTL) é da implementação, fora do caminho de decisão.
type ChallengeIssuance interface {
	// IsChallengeIssued reporta se `challenge` foi EMITIDO por este servidor no `scope` dado
	// (o namespace por-pedido do gate), PARA `approver`, e continua dentro da validade.
	//
	// Fail-closed no chamador: (false, nil) ⇒ [ErrChallengeNotIssued]; (false, err) ⇒
	// [ErrChallengeIssuanceBackend]. A consulta NÃO consome nada — o consumo de uso-único
	// continua a ser do [NonceConsumer], e só acontece na senda que autorizaria.
	IsChallengeIssued(ctx context.Context, scope, approver string, challenge []byte) (bool, error)
}
