package hitl

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// EMISSÃO SERVER-SIDE DE CHALLENGES (remediação AOS-177).
//
// O [EventStoreNonceStore] dá DEDUP: um (scope, nonce) só se consome uma vez. Não dá
// FRESCURA: aceita qualquer valor NUNCA VISTO, e o valor era escolhido pelo CLIENTE. Como o
// scope do 4-eyes é por-request_id, cada pedido novo reabria o espaço de nonces — pelo que uma
// prova capturada (assinatura ed25519 do aprovador + attestation WebAuthn com aquele
// challenge) podia ser reapresentada indefinidamente em pedidos novos.
//
// Este ficheiro fecha isso com um registo DURÁVEL de EMISSÃO sobre o mesmo Event Store
// append-only: o SERVIDOR gera o challenge por CSPRNG, amarra-o a (scope, aprovador) e dá-lhe
// um TTL; o gate só aceita challenges que constem desse registo e ainda estejam válidos. O
// espaço aceitável deixa de ser "tudo o que o cliente inventar" e passa a ser um conjunto
// finito, atribuído e expirável.
//
// EMISSÃO != CONSUMO: este registo diz "foi emitido"; o uso-único continua a ser do
// [EventStoreNonceStore]. Os dois são necessários — emissão sem consumo permitiria usar o
// mesmo challenge nas duas pernas; consumo sem emissão é o buraco que se está a fechar.

const (
	// eventTypeChallengeIssued é o facto "o servidor emitiu este challenge".
	eventTypeChallengeIssued = "foureyes.challenge.issued"
	// challengeStreamPrefix prefixa o stream por-challenge (um stream por challenge emitido).
	challengeStreamPrefix = "4eyes-challenge:"
	// challengeProducerNHI é a identidade emissora do evento (o próprio servidor).
	challengeProducerNHI = "nhi:foureyes-challenge-issuer"
	// ChallengeBytes é o tamanho do challenge emitido (256 bits de CSPRNG: colisão e
	// adivinhação fora de qualquer orçamento).
	ChallengeBytes = 32
	// DefaultChallengeTTL é a janela de validade por omissão de um challenge emitido. Curta:
	// é o tempo de uma cerimónia de aprovação, não uma sessão.
	DefaultChallengeTTL = 5 * time.Minute
)

// Erros do emissor (fail-closed).
var (
	// ErrNoChallengeStore — construção sem store.
	ErrNoChallengeStore = errors.New("hitl: emissor de challenges exige um Event Store")
	// ErrChallengeScope — emissão sem scope ou sem aprovador: um challenge tem de ser
	// atribuído, senão volta a ser um valor livre.
	ErrChallengeScope = errors.New("hitl: emissão de challenge exige scope e aprovador")
)

// ChallengeStore é o subconjunto do Event Store de que o emissor precisa: Append com
// concorrência optimista + Read do stream. *[eventstore.Store] satisfá-lo.
type ChallengeStore interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error)
}

// EventStoreChallengeIssuer emite e regista challenges DURAVELMENTE. Satisfaz
// ESTRUTURALMENTE a porta integration.ChallengeIssuance (só o método de consulta).
type EventStoreChallengeIssuer struct {
	store ChallengeStore
	ttl   time.Duration
	now   func() time.Time
}

// challengeRecord é o payload do evento de emissão. Guarda o APROVADOR a quem o challenge foi
// emitido e o instante de expiração — nunca segredos (o challenge está no stream ID, e um
// challenge não é segredo: viaja em claro no clientDataJSON).
type challengeRecord struct {
	Approver  string `json:"approver"`
	ExpiresAt string `json:"expires_at"` // RFC3339Nano
}

// NewEventStoreChallengeIssuer constrói o emissor. ttl <= 0 ⇒ [DefaultChallengeTTL].
func NewEventStoreChallengeIssuer(store ChallengeStore, ttl time.Duration) (*EventStoreChallengeIssuer, error) {
	if store == nil {
		return nil, ErrNoChallengeStore
	}
	if ttl <= 0 {
		ttl = DefaultChallengeTTL
	}
	return &EventStoreChallengeIssuer{store: store, ttl: ttl, now: time.Now}, nil
}

// WithClock injecta a fonte de tempo (testes deterministas de expiração). nil é ignorado.
func (i *EventStoreChallengeIssuer) WithClock(now func() time.Time) *EventStoreChallengeIssuer {
	if now != nil {
		i.now = now
	}
	return i
}

// IssueChallenge gera um challenge de [ChallengeBytes] por CSPRNG, REGISTA-O duravelmente
// como emitido para (scope, approver) com o TTL configurado, e devolve-o. O challenge só é
// devolvido DEPOIS de o registo estar durável — nunca se devolve um challenge que o gate
// depois não reconheceria.
func (i *EventStoreChallengeIssuer) IssueChallenge(ctx context.Context, scope, approver string) ([]byte, error) {
	if scope == "" || approver == "" {
		return nil, ErrChallengeScope
	}
	challenge := make([]byte, ChallengeBytes)
	if _, err := rand.Read(challenge); err != nil {
		return nil, fmt.Errorf("hitl: CSPRNG do challenge: %w", err)
	}
	payload, err := json.Marshal(challengeRecord{
		Approver:  approver,
		ExpiresAt: i.now().Add(i.ttl).UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return nil, fmt.Errorf("hitl: registo de emissão: %w", err)
	}
	// WithExpectedSeq(0): um stream por challenge, escrito UMA vez. Uma colisão (impossível
	// com 256 bits de CSPRNG) seria um erro, não uma re-emissão silenciosa.
	if _, err := i.store.Append(ctx, challengeStream(scope, challenge), eventstore.EventInput{
		Type:     eventTypeChallengeIssued,
		Payload:  payload,
		Producer: eventstore.Producer{NHIID: challengeProducerNHI},
	}, eventstore.WithExpectedSeq(0)); err != nil {
		return nil, fmt.Errorf("hitl: registar emissão do challenge: %w", err)
	}
	return challenge, nil
}

// IsChallengeIssued implementa a porta de consulta: (true, nil) só se o challenge tiver sido
// EMITIDO neste scope, PARA ESTE aprovador, e ainda estiver dentro da validade.
//
// Fail-closed: stream inexistente, vazio, payload ilegível, aprovador diferente ou prazo
// esgotado ⇒ (false, nil). Só erros de BACKEND (store indisponível) devolvem erro — que o
// gate também trata como negação.
func (i *EventStoreChallengeIssuer) IsChallengeIssued(ctx context.Context, scope, approver string, challenge []byte) (bool, error) {
	if scope == "" || approver == "" || len(challenge) == 0 {
		return false, nil
	}
	events, err := i.store.Read(ctx, challengeStream(scope, challenge), 0)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return false, nil // nunca emitido — o caso comum de um challenge inventado
		}
		return false, err
	}
	for _, ev := range events {
		if ev.Type != eventTypeChallengeIssued {
			continue
		}
		var rec challengeRecord
		if err := json.Unmarshal(ev.Payload, &rec); err != nil {
			continue
		}
		// Comparação em tempo constante do aprovador: mantém o mesmo perfil de timing entre
		// "emitido para outro aprovador" e "emitido para este".
		if subtle.ConstantTimeCompare([]byte(rec.Approver), []byte(approver)) != 1 {
			continue
		}
		exp, perr := time.Parse(time.RFC3339Nano, rec.ExpiresAt)
		if perr != nil {
			continue // registo ilegível ⇒ não conta como emissão válida
		}
		if !i.now().Before(exp) {
			continue // expirado
		}
		return true, nil
	}
	return false, nil
}

// challengeStream namespaceia o stream por (scope, challenge). O challenge vai em hex: não é
// segredo (viaja em claro no clientDataJSON da attestation) e assim o stream ID é imprimível.
func challengeStream(scope string, challenge []byte) string {
	return challengeStreamPrefix + scope + ":" + hex.EncodeToString(challenge)
}
