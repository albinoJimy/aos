package eventstore

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// EventStore é a API pública do Event Store. Deliberadamente NÃO expõe qualquer
// operação de update ou delete: o log é append-only estrito.
type EventStore interface {
	// Append escreve um evento novo no fim do stream e devolve o seq atribuído.
	Append(ctx context.Context, streamID string, in EventInput, opts ...AppendOption) (AppendResult, error)
	// Read devolve os eventos committed do stream com seq >= fromSeq, ordenados
	// por seq ascendente. fromSeq é inclusivo; seq começa em 1.
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]Event, error)
	// Subscribe regista um subscritor push com o filtro dado.
	Subscribe(ctx context.Context, filter Filter, h Handler) (Subscription, error)
	// Close termina o store e liberta todas as subscrições.
	Close() error
}

// Observer é um gancho de observabilidade leve (opcional). Não puxa o SDK OTel
// (isso é EPIC-08); serve apenas para contagem/latência em testes e operação.
//
// Auditoria de rejeições (critério de aceitação C2): toda a tentativa rejeitada
// — incluindo a violação append-only (ErrAppendOnlyViolation) — é sinalizada via
// AppendRejected. A auditoria DURÁVEL dessa rejeição é responsabilidade do
// consumidor: deve injectar um Observer (WithObserver) que persista o registo. O
// Observer por omissão é no-op, pelo que sem configuração a rejeição devolve erro
// mas não é auditada de forma durável por este pacote.
type Observer interface {
	AppendCommitted(streamID string, seq uint64, latency time.Duration)
	AppendDuplicate(streamID string, seq uint64)
	AppendRejected(streamID string, err error)
	Published(streamID string, seq uint64, subscribers int)
}

// nopObserver é o Observer por omissão (não faz nada).
type nopObserver struct{}

func (nopObserver) AppendCommitted(string, uint64, time.Duration) {}
func (nopObserver) AppendDuplicate(string, uint64)                {}
func (nopObserver) AppendRejected(string, error)                  {}
func (nopObserver) Published(string, uint64, int)                 {}

// numStripes é o número de locks listrados por-stream. Fixo (não cresce com o
// número de streams); potência de dois para permitir máscara. 64 dá amplo
// paralelismo entre streams com colisões desprezáveis para o modelo de referência.
const numStripes = 64

// Store é a implementação de referência do EventStore: um cluster de réplicas
// in-process com replicação síncrona por quórum e transporte push.
//
// AOS-100 — sem single-writer. O antigo modelo tinha UM mutex global e UM líder a
// serializar TODAS as escritas (SPOF de escrita). Aqui a serialização é POR-STREAM
// (a ordem total já é por stream): stripes serializam appends ao MESMO stream;
// streams distintos correm em paralelo. O mu do Store passou a proteger apenas a
// MEMBERSHIP (leaderID, alive set) — os appends detêm-no em modo RLock (concorrem
// entre si) e só Kill/Revive/eleição o detêm em modo Lock.
type Store struct {
	// mu protege a MEMBERSHIP do cluster (leaderID e o alive set das réplicas).
	// Appends e Reads detêm-no em RLock (leitura concorrente da topologia);
	// Kill/Revive/eleição detêm-no em Lock. NÃO serializa a escrita de dados —
	// isso é feito por-stream pelos stripes.
	mu       sync.RWMutex
	replicas []*replica
	leaderID int
	quorum   int

	// region é a fronteira regional de soberania do board (ADR-011), normalizada.
	// "" = sem fronteira configurada (soberania dormente; retro-compatível). Quando
	// não-vazia, New rejeita fail-closed qualquer réplica fora dela e a eleição
	// nunca promove liderança cross-border.
	region string

	// board é o id do board de soberania associado à fronteira (WithSovereigntyBoard),
	// ou "" se a fronteira foi declarada sem board (WithRegion) ou não há fronteira.
	// É apenas um rótulo para observabilidade/auditoria (exposto via SovereigntyBoard);
	// o enforcement da fronteira é feito por region, não por este campo.
	board string

	// stripes serializa as operações por stream sem contenção global (ver sharding.go).
	stripes *stripeSet

	// committed é o commit index (total de eventos) confirmado por quórum mais
	// alto alguma vez atingido. É atómico: os appends actualizam-no sob s.mu.RLock
	// (não exclusivo entre si), pelo que precisa de um max-CAS atómico. A eleição
	// recusa promover a líder qualquer réplica com count < committed, evitando
	// servir um log truncado como autoritativo após perda de quórum e revivência
	// de uma réplica desactualizada (durabilidade — ver electLeader).
	committed atomic.Uint64

	// subMu protege o conjunto de subscrições. É RWMutex: os fanouts (um por
	// append) detêm-no em RLock e correm concorrentes entre streams; só
	// Subscribe/Unsubscribe/Close o detêm em Lock. O enqueue por subscritor é
	// serializado pelo lock próprio da subscrição (sub.mu), não por este.
	subMu sync.RWMutex
	subs  map[*subscription]struct{}

	obs    Observer
	closed atomic.Bool

	// wal é a camada de persistência durável (AOS-170). nil ⇒ store puramente
	// in-memory (retro-compatível). Quando não-nil, cada evento committed é
	// gravado e fsync'd antes de Append devolver committed (ver durable.go / Open).
	wal *wal

	now func() time.Time // injectável para testes; por omissão time.Now
}

// Option configura o Store na construção.
type Option func(*config)

type config struct {
	replicas       int
	quorum         int
	region         string
	board          string
	replicaRegions []string
	regionSet      bool
	obs            Observer
	now            func() time.Time
	walTruncaCorr  bool
}

// WithReplicas define o número de réplicas do cluster (>= 1).
func WithReplicas(n int) Option { return func(c *config) { c.replicas = n } }

// WithQuorum define o quórum de escrita. Se 0, usa a maioria (n/2 + 1).
func WithQuorum(q int) Option { return func(c *config) { c.quorum = q } }

// WithRegion declara a fronteira regional de soberania do board (ADR-011): a
// região onde os dados deste store podem residir. Activa o enforcement
// fail-closed — todas as réplicas têm de estar nesta região (ver WithReplicaRegions).
// Uma região vazia é rejeitada na construção (região desconhecida ⇒ deny).
func WithRegion(region string) Option {
	return func(c *config) { c.region = region; c.regionSet = true }
}

// WithSovereigntyBoard declara a soberania por board: associa o id do board à sua
// região autorizada. Equivale a WithRegion(region) para efeitos de fronteira; o
// enforcement fail-closed é feito pela região. O id do board é retido como rótulo
// de observabilidade/auditoria e fica acessível via Store.SovereigntyBoard() — não
// participa na decisão da fronteira. Fail-closed: região vazia é rejeitada.
func WithSovereigntyBoard(board, region string) Option {
	return func(c *config) { c.board = board; c.region = region; c.regionSet = true }
}

// WithReplicaRegions declara a região de cada réplica (uma por réplica, na ordem
// dos ids). Só é válido com uma fronteira configurada (WithRegion/WithSovereigntyBoard);
// o comprimento tem de igualar o número de réplicas. Qualquer réplica fora da
// fronteira — ou com região vazia/desconhecida — faz New falhar com
// ErrSovereigntyViolation (as réplicas NUNCA cruzam a fronteira). Se omitido com
// fronteira activa, todas as réplicas assumem a região do board (cluster
// co-localizado na região de soberania — o caso comum).
func WithReplicaRegions(regions ...string) Option {
	return func(c *config) {
		c.replicaRegions = make([]string, len(regions))
		copy(c.replicaRegions, regions)
	}
}

// WithObserver injecta um gancho de observabilidade.
func WithObserver(o Observer) Option { return func(c *config) { c.obs = o } }

// WithWALTruncateOnCorruption autoriza [Open] a TRUNCAR o WAL num ponto de corrupção
// a MEIO do log — apagando em disco os registos íntegros que vinham depois.
//
// Por omissão [Open] RECUSA esse caso ([ErrWALCorruptedMidLog]), porque truncar ali é
// destruir dados que estão bons. A cauda rasgada de um crash continua a ser truncada
// sem esta opção: aí não há nada íntegro a seguir, e truncar é a recuperação correcta.
//
// É a via de recuperação DELIBERADA de um operador que já leu a mensagem de erro,
// tem cópia de segurança e decidiu que perder o troço vale mais do que ficar parado.
// NUNCA a ligue por omissão num serviço: transforma a recusa num apagamento silencioso,
// que é exactamente o comportamento que a recusa existe para impedir.
func WithWALTruncateOnCorruption() Option { return func(c *config) { c.walTruncaCorr = true } }

// withClock injecta um relógio (uso interno/testes).
func withClock(f func() time.Time) Option { return func(c *config) { c.now = f } }

// New constrói um Store. Por omissão: 3 réplicas, quórum de maioria (2), sem
// fronteira de soberania (retro-compatível).
func New(opts ...Option) (*Store, error) {
	c := &config{replicas: 3}
	for _, o := range opts {
		o(c)
	}
	if c.replicas < 1 {
		return nil, ErrConfig
	}
	if c.quorum == 0 {
		c.quorum = c.replicas/2 + 1
	}
	if c.quorum < 1 || c.quorum > c.replicas {
		return nil, ErrConfig
	}
	if c.obs == nil {
		c.obs = nopObserver{}
	}
	if c.now == nil {
		c.now = time.Now
	}

	// Soberania regional (ADR-011), fail-closed.
	region := normalizeRegion(c.region)
	boundary := c.regionSet
	if boundary && region == "" {
		// Fronteira declarada mas região ausente/vazia ⇒ deny (região desconhecida).
		return nil, ErrSovereigntyViolation
	}
	if len(c.replicaRegions) > 0 {
		// Regiões por réplica só fazem sentido com fronteira declarada e uma por réplica.
		if !boundary || len(c.replicaRegions) != c.replicas {
			return nil, ErrConfig
		}
	}
	replicaRegions, err := resolveReplicaRegions(region, boundary, c.replicas, c.replicaRegions)
	if err != nil {
		return nil, err
	}

	s := &Store{
		replicas: make([]*replica, c.replicas),
		leaderID: 0,
		quorum:   c.quorum,
		region:   region,
		board:    c.board,
		stripes:  newStripeSet(numStripes),
		subs:     make(map[*subscription]struct{}),
		obs:      c.obs,
		now:      c.now,
	}
	for i := range s.replicas {
		s.replicas[i] = newReplica(i, replicaRegions[i])
	}
	return s, nil
}

// resolveReplicaRegions computa a região de cada réplica e valida-a contra a
// fronteira, fail-closed. Sem fronteira, todas as réplicas ficam sem região ("").
// Com fronteira: cada réplica assume a sua região explícita (WithReplicaRegions)
// ou, na ausência desta, a região do board; qualquer região vazia ou fora da
// fronteira faz falhar com ErrSovereigntyViolation — nunca cruza a fronteira.
func resolveReplicaRegions(boardRegion string, boundary bool, n int, explicit []string) ([]string, error) {
	out := make([]string, n)
	if !boundary {
		return out, nil // sem fronteira: réplicas sem região atribuída
	}
	for i := 0; i < n; i++ {
		rr := boardRegion
		if len(explicit) == n {
			rr = normalizeRegion(explicit[i])
		}
		if rr == "" || rr != boardRegion {
			// Região ausente/desconhecida ou fora da fronteira do board ⇒ rejeita.
			return nil, ErrSovereigntyViolation
		}
		out[i] = rr
	}
	return out, nil
}

// Append implementa a semântica de escrita do contrato C2.
//
// Ordem de verificação: idempotência primeiro (o duplicado ganha), depois
// expected_seq, depois quórum e só então a escrita replicada.
//
// Semântica de WithExpectedSeq(n) — n é o último seq committed que o chamador
// afirma ser o corrente (o "prev"); o evento novo ficaria em n+1:
//   - n == último committed  → procede (fresh slot n+1);
//   - n <  último committed  → a posição pretendida (n+1) já está ocupada por
//     história committed → ErrAppendOnlyViolation (escrita no passado);
//   - n >  último committed  → o chamador está adiantado face à realidade →
//     ErrSeqConflict (concorrência optimista).
//
// Nota para retry de CAS concorrente: quando duas escritas com o mesmo
// WithExpectedSeq(n) correm em paralelo, o vencedor materializa n+1 e o perdedor
// passa a ver last=n+1 > n, caindo no ramo expected<last → ErrAppendOnlyViolation
// (e não ErrSeqConflict). Um chamador que implemente concorrência optimista deve,
// por isso, tratar TANTO ErrSeqConflict COMO ErrAppendOnlyViolation como sinal
// para reler o stream e reavaliar — nenhum dos dois é um erro fatal de
// integridade neste contexto; só uma reescrita de uma posição já materializada é
// que representa violação append-only genuína.
func (s *Store) Append(ctx context.Context, streamID string, in EventInput, opts ...AppendOption) (AppendResult, error) {
	if err := ctx.Err(); err != nil {
		return AppendResult{}, err
	}
	var o appendOpts
	for _, opt := range opts {
		opt(&o)
	}
	start := s.now()

	if s.closed.Load() {
		return AppendResult{}, ErrClosed
	}

	// Serialização POR-STREAM: o stripe do stream é detido durante todo o append
	// deste stream (atribuição de seq, CAS, replicação e fanout). Appends ao MESMO
	// stream serializam-se aqui (seq gapless, CAS, ordem de push preservados);
	// appends a streams DIFERENTES adquirem stripes distintos e correm em paralelo
	// — sem serializador global. NÃO há ponto único de escrita.
	stripe := s.stripes.forStream(streamID)
	stripe.Lock()
	defer stripe.Unlock()

	// Membership em modo RLock: a topologia (líder, alive set) fica estável durante
	// o append, mas vários appends concorrem entre si; só Kill/Revive a mutam.
	s.mu.RLock()
	l := s.leader()
	if l == nil {
		s.mu.RUnlock()
		s.obs.AppendRejected(streamID, ErrNoQuorum)
		return AppendResult{}, ErrNoQuorum
	}

	// 1) Idempotência primeiro — o duplicado ganha, mesmo em modo degradado.
	key := idempotencyKey(in.RunID, in.StepID)
	if hasIdempotency(in.RunID, in.StepID) {
		if ref, ev, ok := l.lookupDedup(streamID, key); ok {
			s.mu.RUnlock()
			s.obs.AppendDuplicate(ref.stream, ref.seq)
			return AppendResult{Seq: ref.seq, Status: StatusDuplicate, Event: ev}, nil
		}
	}

	// 2) Concorrência optimista / append-only. O stripe garante que last é estável
	// para ESTE stream durante o append (nenhum outro escritor do stream corre).
	last := l.lastSeq(streamID)
	if o.hasExpected {
		switch {
		case o.expectedSeq == last:
			// ok
		case o.expectedSeq < last:
			s.mu.RUnlock()
			s.obs.AppendRejected(streamID, ErrAppendOnlyViolation)
			return AppendResult{}, ErrAppendOnlyViolation
		default: // expectedSeq > last
			s.mu.RUnlock()
			s.obs.AppendRejected(streamID, ErrSeqConflict)
			return AppendResult{}, ErrSeqConflict
		}
	}

	// 3) Quórum — fail-closed, sem deixar rasto se sub-quórum.
	alive := s.aliveReplicas()
	if len(alive) < s.quorum {
		s.mu.RUnlock()
		s.obs.AppendRejected(streamID, ErrNoQuorum)
		return AppendResult{}, ErrNoQuorum
	}

	// 4) Construção do envelope e replicação síncrona a todas as réplicas vivas.
	seq := last + 1
	schema := in.SchemaVersion
	if schema == "" {
		schema = SchemaVersion
	}
	ev := Event{
		EventID:        newULID(),
		StreamID:       streamID,
		Seq:            seq,
		Type:           in.Type,
		Ts:             s.now().UTC().Format(time.RFC3339Nano),
		Producer:       in.Producer.clone(),
		SchemaVersion:  schema,
		RunID:          in.RunID,
		StepID:         in.StepID,
		ParentStepID:   in.ParentStepID,
		IdempotencyKey: key,
	}
	if in.Payload != nil {
		ev.Payload = make([]byte, len(in.Payload))
		copy(ev.Payload, in.Payload)
	}
	// DURABILIDADE (AOS-170) — WRITE-AHEAD. Persiste o evento no WAL e faz fsync
	// ANTES de o aplicar às réplicas in-memory, elevar o commit index ou fazer
	// fanout. É a ORDEM correcta face a falha (idêntica ao WORM audit/filestore.go
	// que persiste antes de publicar em memória):
	//   - crash APÓS o log estar durável mas antes de aplicar em memória: reparado
	//     pelo replay no arranque (o evento é reconstruído) — sem perda nem dup;
	//   - crash ANTES do fsync: o registo parcial no tail é ignorado pelo replay e o
	//     estado in-memory NUNCA foi mutado — sem divergência nem duplicação;
	//   - ERRO de I/O (disco cheio/EIO/quota) com o processo vivo: devolve erro
	//     FAIL-CLOSED sem tocar no estado in-memory. Não há phantom-commit (Read/
	//     StreamHead não expõem o evento falhado) e o chamador vê a falha — nada foi
	//     acked. A antiga ordem apply-before-log deixava esses defeitos sob um único
	//     erro de escrita.
	//
	//     A cláusula «o seq não foi materializado, um retry reusa last+1» ESTAVA AQUI
	//     e era FALSA — corrigida a 2026-08-30. Ela vale para uma falha do `Write`, mas
	//     não valia para uma falha do `fsync` (nem para um `Flush` que escreva tudo e
	//     devolva erro): aí o registo ficava COMPLETO no ficheiro, o retry reusava o
	//     mesmo seq, e o `Open` seguinte recusava com E_RESTORE_ORDER — o nó deixava de
	//     arrancar. Quem repõe a invariante é agora [wal.desfazer], que trunca o
	//     ficheiro ao tamanho anterior ao registo; a garantia é dele, não deste ponto.
	// Ainda sob o stripe do stream — persistências do MESMO stream serializam na
	// ordem de seq. O ficheiro é único; o wal serializa os seus próprios writes.
	if s.wal != nil {
		if err := s.wal.append(ev); err != nil {
			s.mu.RUnlock()
			s.obs.AppendRejected(streamID, err)
			return AppendResult{}, fmt.Errorf("eventstore: persistir evento committed: %w", err)
		}
	}

	// Só DEPOIS de durável: aplica o evento às réplicas vivas. Réplicas distintas
	// mutam containers próprios (r.mu); a serialização por-stream (stripe) garante
	// que este stream tem um só escritor em cada réplica.
	for _, r := range alive {
		r.store(ev.clone())
	}
	// Regista o commit index confirmado por quórum (monotónico, max-CAS atómico).
	// Serve de piso durável à eleição: nenhuma réplica abaixo deste valor pode
	// tornar-se líder autoritativo (ver electLeader / Revive).
	s.raiseCommitted(l.getCount())

	// Commit atingido. Enfileira para os subscritores (não bloqueante) ainda sob o
	// stripe do stream, para preservar a ordem de seq entre appends DESTE stream.
	// A ordem entre streams distintos é irrelevante (a ordem total é por stream).
	nsent := s.fanout(ev)
	s.mu.RUnlock()

	latency := s.now().Sub(start)
	s.obs.AppendCommitted(streamID, seq, latency)
	s.obs.Published(streamID, seq, nsent)
	return AppendResult{Seq: seq, Status: StatusCommitted, Event: ev.clone()}, nil
}

// raiseCommitted eleva o commit index durável para v se v for maior (monotónico),
// via max-CAS atómico — seguro entre appends concorrentes que só detêm s.mu.RLock.
func (s *Store) raiseCommitted(v uint64) {
	for {
		cur := s.committed.Load()
		if v <= cur {
			return
		}
		if s.committed.CompareAndSwap(cur, v) {
			return
		}
	}
}

// Read devolve cópias dos eventos committed do stream (seq >= fromSeq). Detém o
// stripe do stream em RLock — corre em paralelo com Reads do mesmo stream e com
// appends/Reads de OUTROS streams, mas exclui appends a ESTE stream (que detêm o
// stripe em Lock), garantindo um snapshot consistente e gapless. Detém também
// s.mu.RLock para ler o líder e excluir Kill/Revive (resync). A ordem de aquisição
// (stripe antes de s.mu) é a mesma do Append — sem risco de deadlock.
func (s *Store) Read(ctx context.Context, streamID string, fromSeq uint64) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.closed.Load() {
		return nil, ErrClosed
	}
	stripe := s.stripes.forStream(streamID)
	stripe.RLock()
	defer stripe.RUnlock()

	s.mu.RLock()
	defer s.mu.RUnlock()
	l := s.leader()
	if l == nil {
		return nil, ErrNoQuorum
	}
	out, ok := l.readStream(streamID, fromSeq)
	if !ok {
		return nil, ErrStreamNotFound
	}
	return out, nil
}

// Healthy é uma sonda de PRONTIDÃO barata e SEM efeitos colaterais: devolve true
// enquanto o store está operacional e false depois de [Store.Close] (o mesmo estado
// que faz Append/Read devolverem [ErrClosed]). É uma leitura atómica do flag `closed`
// — não adquire stripes nem s.mu, não aloca, não toca em réplicas nem no WAL — pelo
// que é segura para ser chamada com a frequência de um probe de orquestrador (/readyz)
// sem contender com o caminho de escrita/leitura. Deliberadamente NÃO reflecte a
// degradação de quórum: a prontidão que o nó expõe é "o substrato aceita I/O" (NÃO
// ErrClosed), não uma medida de saúde do cluster (essa é observabilidade, não a
// condição de drain).
func (s *Store) Healthy() bool { return !s.closed.Load() }

// Close termina o store e liberta todas as subscrições (sem fugas).
func (s *Store) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return ErrClosed
	}

	s.subMu.Lock()
	subs := make([]*subscription, 0, len(s.subs))
	for sub := range s.subs {
		subs = append(subs, sub)
	}
	s.subs = make(map[*subscription]struct{})
	s.subMu.Unlock()

	for _, sub := range subs {
		sub.stop()
	}
	// Fecha o WAL durável (flush + fsync + close), se presente. Um evento já
	// committed já foi fsync'd no Append; este close garante o descarregamento final.
	if s.wal != nil {
		if err := s.wal.close(); err != nil {
			return err
		}
	}
	return nil
}
