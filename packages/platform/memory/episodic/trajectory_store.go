// Package episodic implementa a classe de MEMÓRIA EPISÓDICA do AOS (AOS-038):
// trajectórias de execução passadas persistidas como ÁRVORES DE SPANS, indexadas
// e recuperáveis por objectivo/tags, com replay resume-from-step, TTL por classe e
// crypto-shredding — tudo FORA da hot path do loop.
//
// # Composição (não reimplementação)
//
// A memória episódica COMPÕE as fundações já entregues, não as reimplementa:
//
//   - registo (AOS-036, pacote record): a trajectória completa (árvore de spans +
//     conteúdo cru + manifesto por turno) é emitida para o backend de
//     observabilidade por [record.Persist] — o REGISTO do Princípio 4;
//   - projecção (AOS-036, pacote projection): a recuperação NUNCA devolve a
//     trajectória crua — devolve a PROJECÇÃO resumida de [projection.ProjectContext];
//   - Event Store (AOS-002, ADR-007): o log append-only, fonte de verdade, onde
//     cada episódio (índice + ciphertext) é escrito e de onde a recuperação
//     RECONSTRÓI o índice por replay — sem estado autoritativo em RAM;
//   - audit hash-chain (AOS-011, ADR-010): sela o HASH do ciphertext de cada
//     episódio (tamper-evident) — a base do crypto-shredding;
//   - replay resume-from-step (AOS-015/016, durable.Resumer): um episódio
//     recuperado + o Event Store bastam para retomar o run resume-from-step.
//
// # Crypto-shredding (ADR-011)
//
// O CONTEÚDO do episódio (a projecção resumida) é cifrado por ENVELOPE sob a chave
// do titular (KeyStore). O ciphertext e o seu hash ficam no log append-only; a
// hash-chain sela o HASH, nunca o plaintext. Apagar a chave (crypto-shredding)
// torna o episódio IRRECUPERÁVEL sem partir a cadeia — que continua a verificar.
//
// # Fora da hot path
//
// A escrita episódica é uma FILA DRENÁVEL: [TrajectoryStore.Enqueue] é O(1) e não
// toca no Event Store/cripto/tracer (não bloqueia o turno); o trabalho pesado
// (persist + selagem) corre em [TrajectoryStore.Flush], chamado num checkpoint
// FORA do turno crítico. Determinístico e testável — sem goroutines não-determinísticas.
//
// # Durabilidade da fila (at-most-once até ao Flush)
//
// A fila de escrita é EM MEMÓRIA e VOLÁTIL: um episódio enfileirado por [Enqueue]
// que ainda NÃO tenha sido drenado por [Flush] é PERDIDO se o processo morrer antes
// do Flush (semântica at-most-once dos episódios pendentes). A durabilidade só é
// garantida DEPOIS de o Flush escrever no Event Store (append-only, fonte de
// verdade). Consequências e mitigações:
//
//   - Chame [Flush] em checkpoints frequentes (fora do turno crítico) para LIMITAR a
//     janela de perda; PendingCount expõe a profundidade por drenar.
//   - A fila é LIMITADA ([WithMaxQueue], default [DefaultMaxQueue]): ao atingir o
//     tecto, Enqueue devolve [ErrQueueFull] (backpressure fail-closed) em vez de
//     crescer memória sem limite — nunca há crescimento não-limitado se o Flush atrasar.
//   - Uma vez drenado, o episódio é durável e idempotente: reenfileirá-lo (mesma
//     f(run_id, episode_id)) é no-op, e um Flush interrompido a meio recoloca só os
//     não-persistidos, retomando sem re-emitir nem re-selar (ver [TrajectoryStore.Flush]).
//
// Um WAL/enfileiramento já no Event Store para tornar DURÁVEIS os episódios pendentes
// (em vez de at-most-once) é trabalho futuro deliberadamente fora deste MVP.
package episodic

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/projection"
	"github.com/aos-ref/platform/memory/record"
	"github.com/aos-ref/substrate/eventstore"
)

// EpisodicStreamID é o stream do Event Store onde os episódios são escritos
// (append-only). Um stream dedicado dá um log ordenado e auditável dos episódios,
// distinto dos streams por-run das trajectórias vivas.
const EpisodicStreamID = "memory.episodic.trajectories"

// EventTypeEpisodeRecorded é o tipo canónico do evento de episódio no Event Store.
const EventTypeEpisodeRecorded = "memory.episode.recorded"

// DefaultChainPartition é a partição default da hash-chain de audit que sela os
// episódios. Uma partição única encadeia todos os episódios por ordem de escrita,
// tornando a integridade do log episódico verificável de ponta a ponta.
const DefaultChainPartition = "aos.memory.episodic"

// episodicRecordSchemaVersion é a versão SemVer do envelope de episódio persistido.
const episodicRecordSchemaVersion = "1.0.0"

// DefaultMaxQueue é o tecto default da fila de escrita em memória. Ao atingi-lo,
// [TrajectoryStore.Enqueue] devolve [ErrQueueFull] (backpressure fail-closed) — a
// fila NUNCA cresce sem limite. Ajustável por [WithMaxQueue]; um valor <= 0 desliga
// o tecto (fila ilimitada, opt-out explícito).
const DefaultMaxQueue = 4096

// Nomes/atributos de span da via episódica (namespace próprio aos.memory.episodic.*;
// as operações de memória não são inferência GenAI).
const (
	spanRecord            = "memory.episodic.record"
	attrEpisodeID         = "aos.memory.episodic.episode_id"
	attrTraceID           = "aos.memory.episodic.trace_id"
	attrSubject           = "aos.memory.episodic.subject_id"
	attrAuditSeq          = "aos.memory.episodic.audit_seq"
	attrEmittedSpans      = "aos.memory.episodic.emitted_spans"
	attrProjectedTokens   = "aos.memory.episodic.projected_tokens"
	attrOffHotPathDrained = "aos.memory.episodic.drained"
)

// EventLog é o subconjunto do Event Store de que a memória episódica depende:
// Append (escrita append-only idempotente) e Read (reconstrução por replay).
// *eventstore.Store satisfaz esta interface; um valor desta interface satisfaz
// também durable.EventStore (mesmo método-set), pelo que serve o resume-from-step.
type EventLog interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error)
}

// A hash-chain de audit (AOS-011) que sela o hash do ciphertext de cada episódio é
// consumida pela porta [audit.Store] (Append/Read/Head/At) — *audit.MemStore
// satisfá-la. Reutiliza-se directamente a interface do audit (em vez de duplicar
// uma com o mesmo método-set), o que dá acesso a [audit.Verify] sem type-assertion.

// EpisodeInput é o que o produtor enfileira para registar um episódio. A
// trajectória (Record) é a árvore de spans; Goal/Tags são as dimensões de
// indexação; SubjectID é o titular da chave (crypto-shredding); TTLClass a classe
// de retenção. Policy governa a projecção resumida (default se zero).
type EpisodeInput struct {
	// EpisodeID é a identidade do episódio (idempotência f(run_id, episode_id)).
	EpisodeID string
	// SubjectID é o titular dos dados — a chave por titular que o cifra e que o
	// crypto-shredding apaga.
	SubjectID string
	// AgentID é a NHI que produziu o episódio (responsabilização no audit).
	AgentID string
	// RunID liga o episódio ao run no Event Store (replay resume-from-step).
	RunID string
	// Goal é o objectivo do episódio (indexação por objectivo).
	Goal string
	// Tags são as etiquetas de indexação (indexação por tags).
	Tags []string
	// Outcome é o desfecho ("success"/"failed"/"escalated"/…) — só indexação.
	Outcome string
	// TTLClass é a classe de retenção (TTL por classe; ADR-011).
	TTLClass domain.TTLClass
	// Record é a trajectória COMPLETA (árvore de spans). Emitida na íntegra para o
	// backend (registo) e projectada para o resumo cifrado (recuperação).
	Record *record.TrajectoryRecord
	// Policy é a política de projecção (versionada). Zero-value usa a default.
	Policy projection.Policy
	// CreatedAt é o instante de criação; se zero, a fachada preenche pelo relógio.
	CreatedAt time.Time
}

// validate impõe os campos mínimos (fail-closed), sem tocar em recursos externos —
// é seguro chamá-la na hot path (Enqueue).
func (in EpisodeInput) validate() error {
	if in.Record == nil {
		return ErrNilRecord
	}
	if in.EpisodeID == "" {
		return ErrMissingEpisodeID
	}
	if in.SubjectID == "" {
		return ErrMissingSubjectID
	}
	if in.RunID == "" {
		return ErrMissingRunID
	}
	if in.Goal == "" {
		return ErrMissingGoal
	}
	if !in.TTLClass.Valid() {
		return ErrInvalidTTLClass
	}
	return nil
}

// episodeEnvelope é a forma PERSISTIDA de um episódio no Event Store. Os campos de
// índice (Goal/Tags/Outcome/TraceID/RunID) ficam em CLARO para a recuperação por
// consulta; o CONTEÚDO recuperável (a projecção resumida) vai CIFRADO em Sealed.
// ContentHash é o hash do Sealed que a hash-chain sela.
type episodeEnvelope struct {
	SchemaVersion string   `json:"schema_version"`
	EpisodeID     string   `json:"episode_id"`
	SubjectID     string   `json:"subject_id"`
	AgentID       string   `json:"agent_id"`
	RunID         string   `json:"run_id"`
	TraceID       string   `json:"trace_id"`
	Goal          string   `json:"goal"`
	Tags          []string `json:"tags"`
	Outcome       string   `json:"outcome"`
	StepCount     int      `json:"step_count"`
	TTLClass      string   `json:"ttl_class"`
	CreatedAt     string   `json:"created_at"`
	PolicyVersion string   `json:"policy_version"`
	EmittedSpans  int      `json:"emitted_spans"`
	Sealed        sealed   `json:"sealed"`
	ContentHash   string   `json:"content_hash"`
	AuditSeq      uint64   `json:"audit_seq"`
}

// TrajectoryStore é a memória episódica: escreve trajectórias como árvores de spans
// (append-only, fora da hot path) e sela cada episódio na hash-chain sob
// crypto-shredding. A recuperação vive no mesmo tipo (ver retrieval.go).
type TrajectoryStore struct {
	es        EventLog
	keys      KeyStore
	chain     audit.Store
	tracer    agentruntime.Tracer
	clock     func() time.Time
	rand      RandSource
	partition string
	ttlPolicy TTLPolicy
	maxQueue  int

	mu    sync.Mutex
	queue []EpisodeInput

	// seen é o índice de idempotência (cache, NÃO autoritativo — o Event Store é a
	// fonte de verdade): o conjunto de f(run_id, episode_id) já persistidos. É
	// reconstruído UMA vez por replay do log (seenBuilt) e actualizado a cada commit.
	// Evita a sondagem de existência O(N) por episódio (custo O(N²) por Flush) e o
	// re-selar/re-emitir de um episódio já persistido reenfileirado numa sessão nova.
	seen      map[string]struct{}
	seenBuilt bool

	// progress guarda o trabalho JÁ selado (envelope + audit_seq) de um episódio cuja
	// escrita no Event Store ainda falhou. Ao reenfileirar após falha do es.Append, a
	// próxima tentativa RETOMA a partir da escrita — sem re-emitir a árvore de spans
	// (record.Persist) nem RE-SELAR a cadeia. Fecha a janela selar↔escrever: a cadeia
	// é selada no MÁXIMO uma vez por episódio, mantendo o invariante log↔cadeia 1:1.
	progress map[string]*sealedEpisode
}

// sealedEpisode é o resultado intermédio de um episódio já projectado, cifrado e
// SELADO na hash-chain (audit_seq atribuído), à espera de ser escrito no Event Store.
type sealedEpisode struct {
	env episodeEnvelope
}

// idemKey é a chave de idempotência de um episódio: f(run_id, episode_id). O
// separador NUL evita colisões entre pares distintos de (run, episode).
func idemKey(runID, episodeID string) string {
	return runID + "\x00" + episodeID
}

// Option configura o TrajectoryStore.
type Option func(*TrajectoryStore)

// WithTracer injecta a porta Tracer (default NoopTracer).
func WithTracer(t agentruntime.Tracer) Option {
	return func(s *TrajectoryStore) {
		if t != nil {
			s.tracer = t
		}
	}
}

// WithClock injecta o relógio (default time.Now). Determinismo em teste.
func WithClock(now func() time.Time) Option {
	return func(s *TrajectoryStore) {
		if now != nil {
			s.clock = now
		}
	}
}

// WithRandSource injecta a fonte de entropia da cripto (default crypto/rand).
// Determinismo em teste (sealed reproduzível).
func WithRandSource(r RandSource) Option {
	return func(s *TrajectoryStore) {
		if r != nil {
			s.rand = r
		}
	}
}

// WithChainPartition sobrepõe a partição da hash-chain (default [DefaultChainPartition]).
func WithChainPartition(p string) Option {
	return func(s *TrajectoryStore) {
		if p != "" {
			s.partition = p
		}
	}
}

// WithTTLPolicy sobrepõe a política de TTL por classe (default [DefaultTTLPolicy]).
func WithTTLPolicy(p TTLPolicy) Option {
	return func(s *TrajectoryStore) {
		if p != nil {
			s.ttlPolicy = p
		}
	}
}

// WithMaxQueue sobrepõe o tecto da fila de escrita (default [DefaultMaxQueue]). Um
// valor <= 0 desliga o tecto (fila ilimitada) — opt-out explícito da backpressure.
func WithMaxQueue(n int) Option {
	return func(s *TrajectoryStore) {
		s.maxQueue = n
	}
}

// NewTrajectoryStore constrói a memória episódica sobre o Event Store, o KeyStore e
// a hash-chain de audit. Os três são obrigatórios (fail-closed na construção).
func NewTrajectoryStore(es EventLog, keys KeyStore, chain audit.Store, opts ...Option) (*TrajectoryStore, error) {
	if es == nil {
		return nil, ErrNilStore
	}
	if keys == nil {
		return nil, ErrNilKeyStore
	}
	if chain == nil {
		return nil, ErrNilAuditStore
	}
	s := &TrajectoryStore{
		es:        es,
		keys:      keys,
		chain:     chain,
		tracer:    agentruntime.NoopTracer{},
		clock:     time.Now,
		rand:      cryptoRand,
		partition: DefaultChainPartition,
		ttlPolicy: DefaultTTLPolicy(),
		maxQueue:  DefaultMaxQueue,
		progress:  make(map[string]*sealedEpisode),
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// Partition devolve a partição da hash-chain (para verificação externa).
func (s *TrajectoryStore) Partition() string { return s.partition }

// Enqueue coloca um episódio na fila de escrita. É O(1) e FORA da hot path: valida
// só os campos mínimos (sem tocar no Event Store, na cripto ou no tracer) e
// devolve — NÃO bloqueia o turno. O trabalho pesado é de [Flush]. Fail-closed: uma
// entrada mal-formada é rejeitada aqui (nunca entra na fila).
//
// Backpressure: a fila é LIMITADA ([WithMaxQueue], default [DefaultMaxQueue]). Se
// estiver cheia, devolve [ErrQueueFull] em vez de crescer memória sem limite — o
// produtor deve drenar com [Flush] antes de reenfileirar. A fila é volátil: episódios
// enfileirados mas ainda não drenados perdem-se num crash antes do Flush (at-most-once;
// ver o doc do pacote).
func (s *TrajectoryStore) Enqueue(in EpisodeInput) error {
	if err := in.validate(); err != nil {
		return err
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = s.clock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.maxQueue > 0 && len(s.queue) >= s.maxQueue {
		return ErrQueueFull
	}
	s.queue = append(s.queue, in)
	return nil
}

// PendingCount devolve o nº de episódios em fila (por drenar).
func (s *TrajectoryStore) PendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue)
}

// Flush DRENA a fila e persiste cada episódio FORA da hot path: emite a árvore de
// spans completa para o backend (registo), projecta o resumo, cifra-o por envelope,
// SELA o hash na hash-chain e escreve-o append-only no Event Store. Devolve o nº de
// episódios persistidos. Fail-closed: ao primeiro erro, PÁRA e recoloca os episódios
// ainda não persistidos no início da fila (nada é perdido; a próxima Flush retoma).
//
// Idempotência e crash-safety (invariante log↔cadeia 1:1):
//
//   - Um episódio já persistido (mesma f(run_id, episode_id)), detectado pelo índice
//     de idempotência reconstruído por replay, é no-op: NÃO re-emite a árvore de
//     spans nem re-sela a cadeia.
//   - Se a escrita no Event Store falhar DEPOIS de a cadeia já ter selado, o episódio
//     é recolocado com o seu trabalho selado STASHED (ver campo progress): a próxima
//     tentativa RETOMA na escrita, sem re-selar — a cadeia sela no máximo uma vez por
//     episódio. Assim o log e a cadeia mantêm-se 1:1 com episódios distintos mesmo sob
//     retries do Event Store.
func (s *TrajectoryStore) Flush(ctx context.Context) (int, error) {
	s.mu.Lock()
	batch := s.queue
	s.queue = nil
	s.mu.Unlock()

	persisted := 0
	for i, in := range batch {
		if err := s.persist(ctx, in); err != nil {
			// Recoloca os não persistidos (deste inclusive) no início da fila.
			s.mu.Lock()
			s.queue = append(append([]EpisodeInput(nil), batch[i:]...), s.queue...)
			s.mu.Unlock()
			return persisted, err
		}
		persisted++
	}
	return persisted, nil
}

// persist é o caminho PESADO (fora da hot path) de um episódio. Compõe as vias
// registo (record.Persist) e projecção (projection.ProjectContext), cifra a
// projecção, SELA o hash na hash-chain e escreve o envelope no Event Store.
//
// Idempotência e crash-safety (invariante log↔cadeia 1:1): um índice de idempotência
// (seen, reconstruído por replay UMA vez) decide ANTES de emitir/selar se o episódio
// já existe — evitando re-emitir a árvore de spans e re-selar a cadeia, a custo O(1)
// (sem a sondagem O(N) por episódio). Se o es.Append falhar DEPOIS de a cadeia selar,
// o envelope selado fica STASHED (progress) e a retoma salta a selagem: a cadeia sela
// no MÁXIMO uma vez por episódio.
func (s *TrajectoryStore) persist(ctx context.Context, in EpisodeInput) error {
	ctx, span := s.tracer.StartSpan(ctx, spanRecord)
	defer span.End()
	span.SetAttribute(attrEpisodeID, in.EpisodeID)
	span.SetAttribute(attrSubject, in.SubjectID)
	span.SetAttribute(agentruntime.AttrRunID, in.RunID)

	key := idemKey(in.RunID, in.EpisodeID)

	// RETOMA: uma tentativa anterior selou a cadeia mas a escrita no ES falhou — o
	// envelope JÁ selado está stashed. Salta emissão/projecção/cifragem/SELAGEM e vai
	// direto à escrita. A cadeia NÃO é re-selada (invariante log↔cadeia 1:1).
	if prog := s.takeProgress(key); prog != nil {
		span.SetAttribute(attrTraceID, prog.env.TraceID)
		span.SetAttribute(attrEmittedSpans, prog.env.EmittedSpans)
		return s.commitEnvelope(ctx, span, key, in, prog.env)
	}

	// ÍNDICE DE IDEMPOTÊNCIA (cache reconstruído por replay; o ES é a fonte de verdade):
	// um episódio já persistido é no-op — sem re-emitir a árvore de spans nem re-selar.
	if err := s.ensureSeenIndex(ctx); err != nil {
		return err
	}
	if s.seenHas(key) {
		span.SetAttribute("aos.memory.episodic.result", "duplicate")
		return nil
	}

	// (1) REGISTO: a árvore de spans COMPLETA vai para o backend de observabilidade
	// (EPIC-08). Nada é descartado — é o lado "registo" do Princípio 4.
	ev, err := record.Persist(ctx, in.Record, s.tracer)
	if err != nil {
		return err
	}
	span.SetAttribute(attrTraceID, ev.TraceID)
	span.SetAttribute(attrEmittedSpans, ev.EmittedSpans)

	// (2) PROJECÇÃO: o resumo higienizado e limitado em tokens — o que a recuperação
	// devolve. NUNCA a trajectória crua (RawContent/árvore de spans).
	policy := in.Policy
	if policy.Version == "" {
		policy = projection.DefaultPolicy()
	}
	iv, err := projection.ProjectContext(record.View(in.Record), policy)
	if err != nil {
		return err
	}
	span.SetAttribute(attrProjectedTokens, iv.TokenCount)

	// (3) CIFRAGEM POR ENVELOPE sob a chave do titular (provisionada on-first-write).
	plaintext, err := json.Marshal(iv)
	if err != nil {
		return err
	}
	kek, err := s.keys.EnsureKey(in.SubjectID)
	if err != nil {
		return err
	}
	blob, err := seal(kek, plaintext, s.rand)
	if err != nil {
		return err
	}
	ch, err := contentHash(blob)
	if err != nil {
		return err
	}

	env := episodeEnvelope{
		SchemaVersion: episodicRecordSchemaVersion,
		EpisodeID:     in.EpisodeID,
		SubjectID:     in.SubjectID,
		AgentID:       in.AgentID,
		RunID:         in.RunID,
		TraceID:       ev.TraceID,
		Goal:          in.Goal,
		Tags:          append([]string(nil), in.Tags...),
		Outcome:       in.Outcome,
		StepCount:     in.Record.TurnCount(),
		TTLClass:      string(in.TTLClass),
		CreatedAt:     in.CreatedAt.UTC().Format(time.RFC3339Nano),
		PolicyVersion: iv.PolicyVersion,
		EmittedSpans:  ev.EmittedSpans,
		Sealed:        blob,
		ContentHash:   hexHash(ch),
	}

	// (4) SELAGEM na hash-chain: sela o HASH do ciphertext + KeyRef/SubjectID (o que
	// habilita o crypto-shredding sem partir a cadeia). ATRIBUI o audit_seq. Ocorre no
	// MÁXIMO uma vez por episódio: a partir daqui, se a escrita no ES falhar, o envelope
	// selado é stashed e a retoma salta esta selagem.
	sealedRec, err := s.chain.Append(ctx, s.auditRecordFor(in, ch))
	if err != nil {
		return err
	}
	env.AuditSeq = sealedRec.AuditSeq

	// (5) ESCRITA APPEND-ONLY no Event Store (idempotente f(run_id, episode_id)).
	return s.commitEnvelope(ctx, span, key, in, env)
}

// commitEnvelope escreve um envelope JÁ selado no Event Store (append-only,
// idempotente por f(run_id, episode_id)). É partilhado pelo caminho normal e pela
// RETOMA pós-falha. Em falha da escrita, STASHA o envelope selado (progress) para que
// a retoma não re-sele a cadeia; em sucesso, marca o episódio como visto e limpa o
// stash. Fecha a janela selar↔escrever do invariante log↔cadeia 1:1.
func (s *TrajectoryStore) commitEnvelope(ctx context.Context, span agentruntime.Span, key string, in EpisodeInput, env episodeEnvelope) error {
	span.SetAttribute(attrAuditSeq, env.AuditSeq)

	payload, err := json.Marshal(env)
	if err != nil {
		// A cadeia já selou; guarda o trabalho para a retoma não re-selar.
		s.stashProgress(key, env)
		return err
	}
	res, err := s.es.Append(ctx, EpisodicStreamID, eventstore.EventInput{
		Type:     EventTypeEpisodeRecorded,
		Payload:  payload,
		RunID:    in.RunID,
		StepID:   "episodic:record:" + in.EpisodeID,
		Producer: eventstore.Producer{NHIID: in.AgentID},
	})
	if err != nil {
		// A cadeia já selou; guarda o trabalho para a retoma não re-selar.
		s.stashProgress(key, env)
		return err
	}
	s.markSeen(key)
	if res.Status == eventstore.StatusDuplicate {
		span.SetAttribute("aos.memory.episodic.result", "duplicate")
		return nil
	}
	span.SetAttribute("aos.memory.episodic.result", "committed")
	return nil
}

// ensureSeenIndex reconstrói, UMA vez, o índice de idempotência (f(run_id,
// episode_id)) por replay do log — o custo de replay é pago uma vez, não por
// episódio. O replay corre FORA do lock (I/O); a publicação FUNDE no conjunto (não
// sobrepõe) para preservar marcas concorrentes. O ES continua a ser a fonte de
// verdade; este índice é só um cache que evita o O(N²) da sondagem por episódio.
func (s *TrajectoryStore) ensureSeenIndex(ctx context.Context) error {
	s.mu.Lock()
	built := s.seenBuilt
	s.mu.Unlock()
	if built {
		return nil
	}
	envs, err := s.readEnvelopes(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.seenBuilt {
		if s.seen == nil {
			s.seen = make(map[string]struct{}, len(envs))
		}
		for _, e := range envs {
			s.seen[idemKey(e.RunID, e.EpisodeID)] = struct{}{}
		}
		s.seenBuilt = true
	}
	return nil
}

// seenHas reporta se o episódio já foi persistido (índice de idempotência).
func (s *TrajectoryStore) seenHas(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.seen[key]
	return ok
}

// markSeen regista um episódio como persistido no índice de idempotência.
func (s *TrajectoryStore) markSeen(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = make(map[string]struct{})
	}
	s.seen[key] = struct{}{}
}

// stashProgress guarda o envelope JÁ selado de um episódio cuja escrita no ES falhou.
func (s *TrajectoryStore) stashProgress(key string, env episodeEnvelope) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.progress == nil {
		s.progress = make(map[string]*sealedEpisode)
	}
	s.progress[key] = &sealedEpisode{env: env}
}

// takeProgress remove e devolve o trabalho selado stashed de um episódio (nil se não
// houver) — a retoma consome-o para escrever no ES sem re-selar a cadeia.
func (s *TrajectoryStore) takeProgress(key string) *sealedEpisode {
	s.mu.Lock()
	defer s.mu.Unlock()
	prog, ok := s.progress[key]
	if !ok {
		return nil
	}
	delete(s.progress, key)
	return prog
}

// auditRecordFor constrói o AuditRecord que sela um episódio na hash-chain. Só
// metadados de responsabilização e o PayloadRef (ContentHash + KeyRef + SubjectID)
// entram — NUNCA o plaintext. O ContentHash é o hash do ciphertext; a KeyRef é o
// titular (a chave que o crypto-shredding apaga). É por selar o HASH e não o
// plaintext que apagar a chave deixa a cadeia intacta.
func (s *TrajectoryStore) auditRecordFor(in EpisodeInput, ch []byte) audit.AuditRecord {
	return audit.AuditRecord{
		Partition:     s.partition,
		Timestamp:     s.clock(),
		Decision:      audit.DecisionAllow,
		Principal:     audit.Principal{NHIID: in.AgentID},
		Capability:    "memory:episodic:record",
		PolicyVersion: episodicRecordSchemaVersion,
		RunID:         in.RunID,
		StepID:        "episodic:record:" + in.EpisodeID,
		ToolID:        "memory.episodic",
		Context:       audit.CallContext{Taint: string(in.TTLClass)},
		PayloadRef: &audit.PayloadRef{
			ContentHash: ch,
			KeyRef:      in.SubjectID,
			SubjectID:   in.SubjectID,
		},
	}
}

// VerifyChain verifica a integridade da hash-chain dos episódios de ponta a ponta.
// Devolve nil se a cadeia está íntegra (mesmo após crypto-shredding: apagar a chave
// não muta a cadeia). Uma partição vazia (nenhum episódio ainda) é nil.
func (s *TrajectoryStore) VerifyChain(ctx context.Context) error {
	head, err := s.chain.Head(ctx, s.partition)
	if err != nil {
		return err
	}
	if head == 0 {
		return nil
	}
	// audit.Verify reencadeia a partição de 1 a head: recalcula cada EntryHash e
	// confere o encadeamento. Devolve audit.ErrTampered se algo foi mutado/removido/
	// inserido — e nil se íntegra (o que continua a valer após crypto-shredding,
	// porque apagar a chave não toca na cadeia).
	return audit.Verify(ctx, s.chain, s.partition, 1, head)
}

// readEnvelopes reconstrói todos os episódios do log por replay (ordem de escrita).
func (s *TrajectoryStore) readEnvelopes(ctx context.Context) ([]episodeEnvelope, error) {
	events, err := s.es.Read(ctx, EpisodicStreamID, 1)
	if err != nil {
		if errorsIsStreamNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]episodeEnvelope, 0, len(events))
	for _, ev := range events {
		if ev.Type != EventTypeEpisodeRecorded {
			continue
		}
		var env episodeEnvelope
		if err := json.Unmarshal(ev.Payload, &env); err != nil {
			return nil, err
		}
		out = append(out, env)
	}
	return out, nil
}
