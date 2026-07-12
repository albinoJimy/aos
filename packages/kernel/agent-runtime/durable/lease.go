package durable

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// EventTypeLeaseClaimed é o tipo canónico do evento que materializa um CLAIM de run
// no stream de lease do Event Store — o acto que minta um novo fencing token.
const EventTypeLeaseClaimed = "lease.claimed"

// EventTypeLeaseRenewed é o tipo canónico do evento que materializa um HEARTBEAT
// (renovação de TTL) do lease corrente, sem mintar novo token.
const EventTypeLeaseRenewed = "lease.renewed"

// leaseStreamPrefix namespaceia o stream de lease de um run ("lease:" + run_id),
// SEPARANDO-O do stream de negócio do run (stream_id == run_id, onde vivem as
// transições de AOS-017 e o ledger de AOS-014). A separação é o que permite usar a
// concorrência optimista (expected_seq) do Event Store para serializar os CLAIMS sem
// colidir com o seq dos eventos de negócio do mesmo run.
const leaseStreamPrefix = "lease:"

// leaseStream devolve o stream_id do log de lease de um run.
func leaseStream(runID string) string { return leaseStreamPrefix + runID }

// leaseClaimStepPrefix / leaseRenewStepPrefix namespaceiam o step_id dos eventos de
// lease. Cada evento leva um NONCE globalmente único (crypto/rand) para que a sua
// idempotency_key NUNCA colida na dedup GLOBAL do Event Store — dois workers a
// reclamar em paralelo produzem chaves distintas e é a concorrência optimista
// (expected_seq), não a dedup, que elege o vencedor. Um claim NÃO é idempotente: cada
// claim minta um token FRESCO.
const (
	leaseClaimStepPrefix = "lease-claim-"
	leaseRenewStepPrefix = "lease-hb-"
)

// Clock é o relógio INJECTÁVEL do lease — a fonte do wall-clock que decide a
// expiração do TTL. Injectá-lo torna os testes de expiração DETERMINÍSTICOS, sem
// sleeps frágeis. Default: [systemClock].
type Clock interface {
	Now() time.Time
}

// ClockFunc adapta uma função a [Clock].
type ClockFunc func() time.Time

// Now implementa [Clock].
func (f ClockFunc) Now() time.Time { return f() }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// Lease é a POSSE TEMPORÁRIA de um run por um worker: um fencing token monotónico
// mais um TTL renovável por heartbeat. Enquanto o lease for o corrente e não tiver
// expirado, o worker é o único escritor efectivo (o token fenced-out invalida os
// obsoletos — ver [FencedAppender]). É devolvido por [LeaseManager.Claim] e
// [LeaseManager.Heartbeat].
//
// Token é um [FencingToken] e por isso satisfaz directamente o contrato
// [state.FencingToken] de AOS-017: passa-se a Machine.Transition(ready → running) sem
// conversão. ExpiresAt é medido no relógio INJECTADO no [LeaseManager].
type Lease struct {
	// RunID é o run possuído (chave do stream de lease e do stream de negócio).
	RunID string
	// Token é o fencing token monotónico mintado por este claim.
	Token FencingToken
	// Worker é o identificador (opcional) do detentor, para observabilidade.
	Worker string
	// TTL é a duração de validade concedida a partir do claim/heartbeat.
	TTL time.Duration
	// ExpiresAt é o instante (no relógio injectado) em que o lease expira se não for
	// renovado por heartbeat.
	ExpiresAt time.Time
}

// Expired reporta se o lease já não é válido no instante now (TTL esgotado). A
// fronteira é inclusiva: em now == ExpiresAt o lease JÁ expirou (fail-closed). É
// exposto para o Escalonador de EPIC-03 avaliar reatribuição sobre o relógio dele.
func (l Lease) Expired(now time.Time) bool { return !now.Before(l.ExpiresAt) }

// leaseRecord é o corpo JSON persistido em cada evento de lease. Sem segredos: só o
// token (que é o que o fencing usa — não é segredo), o worker (rótulo) e os instantes
// de validade. Os instantes são gravados em Unix-nanos para reconstrução estável do
// TTL independentemente da formatação.
type leaseRecord struct {
	RunID           string `json:"run_id"`
	Token           uint64 `json:"token"`
	Worker          string `json:"worker,omitempty"`
	Kind            string `json:"kind"` // "claimed" | "renewed"
	TTLNanos        int64  `json:"ttl_nanos"`
	AtUnixNano      int64  `json:"at_unix_nano"`
	ExpiresUnixNano int64  `json:"expires_unix_nano"`
}

// LeaseAuthority é o CONTRATO PARTILHÁVEL de lease/fencing (RT + SCH). Expõe o mínimo
// de que tanto o Agent Runtime (este ticket, AOS-018) como o Escalonador do EPIC-03
// (AOS-025..034) precisam — para que o SCH partilhe o MESMO token monotónico e a
// MESMA autoridade de expiração, sem reimplementar o mecanismo. [LeaseManager]
// satisfá-lo; documentado para o EPIC-03 reutilizar (ver README / tecnica/03).
type LeaseAuthority interface {
	// Claim reclama o run, mintando um fencing token monotónico e um lease com TTL.
	Claim(ctx context.Context, runID string) (Lease, error)
	// Heartbeat renova o TTL do lease se ele ainda for o corrente e não tiver expirado.
	Heartbeat(ctx context.Context, lease Lease) (Lease, error)
	// CurrentToken devolve o fencing token corrente do run (0 se nunca reclamado).
	CurrentToken(ctx context.Context, runID string) (FencingToken, error)
}

// Compile-time: o LeaseManager honra o contrato partilhável, a autoridade de token e
// a capacidade opcional de expiração consumida pelo enforcement de fencing.
var (
	_ LeaseAuthority       = (*LeaseManager)(nil)
	_ TokenSource          = (*LeaseManager)(nil)
	_ LeaseExpiryAuthority = (*LeaseManager)(nil)
)

// defaultClaimRetries limita as re-tentativas de um claim/heartbeat sob contenção de
// concorrência optimista (relê e reavalia). Um limite generoso: sob contenção real
// cada perdedor recua uma iteração, e a monotonicidade garante progresso.
const defaultClaimRetries = 64

// LeaseManager é a AUTORIDADE de liveness distribuída de AOS-018: minta fencing
// tokens ESTRITAMENTE MONOTÓNICOS e duráveis, concede leases com TTL renovável por
// heartbeat, e serve de [TokenSource] ao enforcement de fencing. NÃO usa PID: a
// liveness é decidida por lease/heartbeat/TTL sobre o relógio injectado.
//
// # Origem e durabilidade do contador monotónico
//
// O token é o contador do CLAIM, materializado no stream de lease do run
// ("lease:"+run_id) do Event Store. A MONOTONICIDADE e a serialização de claims
// concorrentes vêm da CONCORRÊNCIA OPTIMISTA do Event Store (AOS-002): cada claim
// (1) relê o stream para obter o token corrente e o último seq, e (2) faz Append com
// WithExpectedSeq(último_seq) de um evento com token = corrente+1. Dois claims
// concorrentes competem no MESMO expected_seq — um vence (materializa o token), o
// outro é rejeitado (ErrSeqConflict/ErrAppendOnlyViolation), relê e obtém um token
// ESTRITAMENTE MAIOR. O contador é durável porque vive no log replicado e é
// reconstruível por replay (não há estado de token só em memória). Ver [Claim].
//
// Seguro para uso concorrente (é stateless entre chamadas — a verdade vive no Event
// Store; não há mutação de campos partilhados após a construção).
type LeaseManager struct {
	store    EventStore
	clock    Clock
	ttl      time.Duration
	producer eventstore.Producer
	worker   string
	retries  int
}

// LeaseOption configura o [LeaseManager].
type LeaseOption func(*LeaseManager)

// WithLeaseClock injecta o relógio (default [systemClock]). Usar nos testes de
// expiração para determinismo sem sleeps.
func WithLeaseClock(c Clock) LeaseOption {
	return func(m *LeaseManager) {
		if c != nil {
			m.clock = c
		}
	}
}

// WithLeaseProducer define a identidade emissora (NHI + cadeia de delegação) gravada
// nos eventos de lease. Default: Producer zero (aceitável em teste).
func WithLeaseProducer(p eventstore.Producer) LeaseOption {
	return func(m *LeaseManager) { m.producer = p }
}

// WithWorkerID rotula os leases com o identificador do worker (só observabilidade —
// NUNCA é usado para decidir liveness; a decisão é por lease/TTL, não por identidade
// nem PID). Default: "".
func WithWorkerID(id string) LeaseOption {
	return func(m *LeaseManager) { m.worker = id }
}

// NewLeaseManager constrói a autoridade de lease sobre o Event Store dado, com o TTL
// de concessão indicado. store é obrigatório ([ErrNilStore]); ttl tem de ser > 0
// ([ErrInvalidTTL]).
func NewLeaseManager(store EventStore, ttl time.Duration, opts ...LeaseOption) (*LeaseManager, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if ttl <= 0 {
		return nil, ErrInvalidTTL
	}
	m := &LeaseManager{
		store:   store,
		clock:   systemClock{},
		ttl:     ttl,
		retries: defaultClaimRetries,
	}
	for _, o := range opts {
		o(m)
	}
	if m.clock == nil {
		m.clock = systemClock{}
	}
	return m, nil
}

// leaseState é o resultado do fold do stream de lease: o lease CORRENTE (o de maior
// token, com a expiração mais recente desse token) e o último seq committed do stream
// (o expected_seq do próximo Append). exists indica se há algum claim no stream.
type leaseState struct {
	token           uint64
	worker          string
	ttlNanos        int64
	expiresUnixNano int64
	lastSeq         uint64
	exists          bool
}

// readLeaseState relê o stream de lease do run e faz o fold para o [leaseState]
// corrente. Um stream inexistente ⇒ estado vazio (exists=false, lastSeq=0). O token
// corrente é o MAIOR observado; para esse token, a expiração é a mais recente (um
// heartbeat estende-a). Como os tokens são não-decrescentes no log (claims aumentam,
// heartbeats mantêm), o fold é linear e determinístico.
func (m *LeaseManager) readLeaseState(ctx context.Context, runID string) (leaseState, error) {
	events, err := m.store.Read(ctx, leaseStream(runID), 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return leaseState{}, nil
		}
		return leaseState{}, err
	}
	var st leaseState
	for i := range events {
		e := events[i]
		if e.Type != EventTypeLeaseClaimed && e.Type != EventTypeLeaseRenewed {
			continue
		}
		st.lastSeq = e.Seq
		var rec leaseRecord
		if uerr := json.Unmarshal(e.Payload, &rec); uerr != nil {
			return leaseState{}, uerr
		}
		st.exists = true
		switch {
		case rec.Token > st.token:
			// Novo claim: adopta token, worker, ttl e expiração deste evento.
			st.token = rec.Token
			st.worker = rec.Worker
			st.ttlNanos = rec.TTLNanos
			st.expiresUnixNano = rec.ExpiresUnixNano
		case rec.Token == st.token:
			// Heartbeat do token corrente: estende a expiração (toma a mais recente).
			if rec.ExpiresUnixNano > st.expiresUnixNano {
				st.expiresUnixNano = rec.ExpiresUnixNano
			}
			if rec.TTLNanos != 0 {
				st.ttlNanos = rec.TTLNanos
			}
			if rec.Worker != "" {
				st.worker = rec.Worker
			}
		}
	}
	// Nota: st.lastSeq acompanha o último evento de QUALQUER tipo no stream (o stream
	// de lease só contém eventos de lease), garantindo o expected_seq correcto.
	return st, nil
}

// leaseFrom materializa um [Lease] a partir do estado corrente e do run.
func leaseFrom(runID string, st leaseState) Lease {
	return Lease{
		RunID:     runID,
		Token:     FencingToken(st.token),
		Worker:    st.worker,
		TTL:       time.Duration(st.ttlNanos),
		ExpiresAt: time.Unix(0, st.expiresUnixNano),
	}
}

// Claim reclama o run, mintando um fencing token ESTRITAMENTE MONOTÓNICO e concedendo
// um lease com TTL. Falha com [ErrLeaseHeld] se um lease AINDA VÁLIDO (não expirado)
// for detido — não se rouba um lease vivo; só um run livre (nunca reclamado ou com o
// lease expirado por ausência de heartbeat) é reclamável.
//
// Algoritmo (concorrência optimista — ver [LeaseManager]):
//
//  1. relê o stream de lease → token corrente + último seq;
//  2. se há lease e now < expiração → [ErrLeaseHeld];
//  3. Append(lease.claimed{token: corrente+1}) com WithExpectedSeq(último_seq);
//  4. conflito de concorrência (outro claim venceu) → relê e repete (o novo token
//     será ainda MAIOR); commit → devolve o Lease.
//
// Devolve [ErrEmptyRunID], [ErrLeaseHeld], [ErrClaimContention] (esgotou as
// re-tentativas sob contenção) ou o erro do Event Store.
func (m *LeaseManager) Claim(ctx context.Context, runID string) (Lease, error) {
	if runID == "" {
		return Lease{}, ErrEmptyRunID
	}
	for attempt := 0; attempt <= m.retries; attempt++ {
		st, err := m.readLeaseState(ctx, runID)
		if err != nil {
			return Lease{}, err
		}
		now := m.clock.Now()
		if st.exists && now.UnixNano() < st.expiresUnixNano {
			// Lease vivo detido por outro (ou por este) worker: não é reclamável.
			return Lease{}, ErrLeaseHeld
		}

		newToken := st.token + 1
		expires := now.Add(m.ttl)
		rec := leaseRecord{
			RunID:           runID,
			Token:           newToken,
			Worker:          m.worker,
			Kind:            "claimed",
			TTLNanos:        int64(m.ttl),
			AtUnixNano:      now.UnixNano(),
			ExpiresUnixNano: expires.UnixNano(),
		}
		payload, err := json.Marshal(rec)
		if err != nil {
			return Lease{}, err
		}
		_, err = m.store.Append(ctx, leaseStream(runID), eventstore.EventInput{
			Type:     EventTypeLeaseClaimed,
			Payload:  payload,
			RunID:    runID,
			StepID:   leaseClaimStepPrefix + newNonce(),
			Producer: m.producer,
		}, eventstore.WithExpectedSeq(st.lastSeq))
		if err != nil {
			if isConcurrencyConflict(err) {
				// Outro claim venceu o slot: relê e tenta com um token ainda maior.
				continue
			}
			return Lease{}, err
		}
		return Lease{
			RunID:     runID,
			Token:     FencingToken(newToken),
			Worker:    m.worker,
			TTL:       m.ttl,
			ExpiresAt: expires,
		}, nil
	}
	return Lease{}, ErrClaimContention
}

// Heartbeat renova o TTL do lease SE ele ainda for o corrente (token == corrente) e
// não tiver expirado. Um heartbeat de um lease já SUPERADO por um novo claim devolve
// [ErrLeaseSuperseded]; de um lease já EXPIRADO (TTL esgotado — o run já é reclamável)
// devolve [ErrLeaseExpired]. O sucesso NÃO minta novo token: appenda um lease.renewed
// com o MESMO token e uma nova expiração. Devolve o Lease actualizado.
//
// Sem heartbeat dentro do TTL, o lease expira e o run fica DISPONÍVEL para reclamação
// (um novo [Claim] minta um token MAIOR) — é a base da liveness sem PID.
func (m *LeaseManager) Heartbeat(ctx context.Context, lease Lease) (Lease, error) {
	if lease.RunID == "" {
		return Lease{}, ErrEmptyRunID
	}
	if !lease.Token.Valid() {
		return Lease{}, ErrLeaseSuperseded
	}
	for attempt := 0; attempt <= m.retries; attempt++ {
		st, err := m.readLeaseState(ctx, lease.RunID)
		if err != nil {
			return Lease{}, err
		}
		if !st.exists || st.token != lease.Token.Value() {
			// O lease corrente já é de outro token (superado por um claim posterior),
			// ou o stream não tem lease algum: este heartbeat é obsoleto.
			return Lease{}, ErrLeaseSuperseded
		}
		now := m.clock.Now()
		if now.UnixNano() >= st.expiresUnixNano {
			// TTL esgotado: tarde demais para renovar; o run já é reclamável.
			return Lease{}, ErrLeaseExpired
		}

		expires := now.Add(m.ttl)
		rec := leaseRecord{
			RunID:           lease.RunID,
			Token:           st.token,
			Worker:          m.worker,
			Kind:            "renewed",
			TTLNanos:        int64(m.ttl),
			AtUnixNano:      now.UnixNano(),
			ExpiresUnixNano: expires.UnixNano(),
		}
		payload, err := json.Marshal(rec)
		if err != nil {
			return Lease{}, err
		}
		_, err = m.store.Append(ctx, leaseStream(lease.RunID), eventstore.EventInput{
			Type:     EventTypeLeaseRenewed,
			Payload:  payload,
			RunID:    lease.RunID,
			StepID:   leaseRenewStepPrefix + newNonce(),
			Producer: m.producer,
		}, eventstore.WithExpectedSeq(st.lastSeq))
		if err != nil {
			if isConcurrencyConflict(err) {
				// Escrita concorrente no stream (outro heartbeat/claim): relê e reavalia
				// (pode ter passado a superado/expirado — o loop trata-o).
				continue
			}
			return Lease{}, err
		}
		return Lease{
			RunID:     lease.RunID,
			Token:     FencingToken(st.token), // mesmo token — heartbeat não minta novo
			Worker:    m.worker,
			TTL:       m.ttl,
			ExpiresAt: expires,
		}, nil
	}
	return Lease{}, ErrClaimContention
}

// CurrentToken devolve o fencing token corrente do run (o maior alguma vez mintado),
// ou 0 se o run nunca foi reclamado. Implementa [TokenSource] para o [FencedAppender]
// e faz parte do contrato partilhável [LeaseAuthority] (o Escalonador consome-o para
// resolver o mesmo token corrente sem duplicar o mecanismo).
func (m *LeaseManager) CurrentToken(ctx context.Context, runID string) (FencingToken, error) {
	if runID == "" {
		return 0, ErrEmptyRunID
	}
	st, err := m.readLeaseState(ctx, runID)
	if err != nil {
		return 0, err
	}
	return FencingToken(st.token), nil
}

// CurrentLeaseExpired reporta se o lease CORRENTE do run já EXPIROU no relógio
// injectado (TTL esgotado por ausência de heartbeat) e se existe algum lease. Um run
// nunca reclamado devolve (false, false, nil). Satisfaz [LeaseExpiryAuthority]: serve
// ao [FencedAppender] para negar a escrita de um detentor cujo lease morreu — mesmo
// ANTES de um novo claim o superar — fechando o fail-open de liveness documentado em
// [ErrLeaseExpired]. NÃO minta, renova nem muta nada.
func (m *LeaseManager) CurrentLeaseExpired(ctx context.Context, runID string) (expired, exists bool, err error) {
	if runID == "" {
		return false, false, ErrEmptyRunID
	}
	st, e := m.readLeaseState(ctx, runID)
	if e != nil {
		return false, false, e
	}
	if !st.exists {
		return false, false, nil
	}
	return m.clock.Now().UnixNano() >= st.expiresUnixNano, true, nil
}

// Current devolve o [Lease] corrente do run (token, worker, expiração) e se existe.
// Útil para observabilidade e para o Escalonador decidir reatribuição. Não muta nada.
func (m *LeaseManager) Current(ctx context.Context, runID string) (Lease, bool, error) {
	if runID == "" {
		return Lease{}, false, ErrEmptyRunID
	}
	st, err := m.readLeaseState(ctx, runID)
	if err != nil {
		return Lease{}, false, err
	}
	if !st.exists {
		return Lease{}, false, nil
	}
	return leaseFrom(runID, st), true, nil
}

// isConcurrencyConflict indica se o erro do Event Store é o sinal de concorrência
// optimista que manda RELER e reavaliar — tanto ErrSeqConflict (o expected_seq ficou
// para trás) como ErrAppendOnlyViolation (o vencedor materializou o slot e o perdedor
// vê agora expected < last). Ambos são benignos aqui (não são corrupção); ver a nota
// de retry em eventstore.Store.Append.
func isConcurrencyConflict(err error) bool {
	return errors.Is(err, eventstore.ErrSeqConflict) || errors.Is(err, eventstore.ErrAppendOnlyViolation)
}

// newNonce devolve um nonce hex globalmente único (96 bits de crypto/rand) para o
// step_id dos eventos de lease — garantindo que a idempotency_key de cada claim/
// heartbeat é distinta na dedup GLOBAL do Event Store (a eleição do vencedor é da
// concorrência optimista, não da dedup). Zero dependências externas.
func newNonce() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read não falha em plataformas suportadas; recai no relógio para
		// não bloquear (a colisão real exige o MESMO nano E a MESMA corrida — inócua
		// perante o CAS que serializa na mesma).
		return "t" + hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}
