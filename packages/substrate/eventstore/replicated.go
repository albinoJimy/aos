package eventstore

import (
	"sync"
	"sync/atomic"
)

// dedupRef aponta a localização committed de uma idempotency_key dentro do stream.
type dedupRef struct {
	stream string
	seq    uint64
}

// streamLog é o log append-only de um único stream, com o seu próprio índice de
// deduplicação. O slice de eventos e o mapa dedup são mutados/lidos APENAS com o
// stripe do stream detido (serialização por-stream): há no máximo UM escritor por
// stream em qualquer instante e as leituras (Read) detêm o stripe em RLock. Não é
// necessário nenhum lock de réplica para tocar num streamLog — só o stripe. A
// dedup é POR STREAM (a idempotency_key = run_id:step_id vive no stream do run;
// stream_id == run_id), o que remove o último contentor partilhado entre streams
// e permite paralelismo real na escrita.
type streamLog struct {
	events []Event           // índice i => seq i+1 (gapless desde 1)
	dedup  map[string]uint64 // idempotency_key -> seq (dentro deste stream)
}

func newStreamLog() *streamLog {
	return &streamLog{dedup: make(map[string]uint64)}
}

// replica é uma cópia in-process do log. Cada réplica viva mantém o log completo
// (particionado por stream) e o índice de deduplicação por stream (reconstrutível
// a partir do log committed).
//
// AOS-100 — sem single-writer. O antigo mutex global do Store deixou de serializar
// as escritas. O único lock ao nível da réplica (r.mu) protege APENAS a ESTRUTURA
// do mapa streams (a inserção de um stream novo é rara). O caminho quente — append
// ao log de um stream existente e mutação do seu dedup — é protegido pelo STRIPE do
// stream (serialização por-stream, ao nível do Store), não por r.mu: appends a
// streams DIFERENTES adquirem o mapa em RLock e progridem em paralelo, sem
// exclusão mútua. O commit index é atómico.
type replica struct {
	id     int
	region string // região de soberania desta réplica (normalizada); "" = sem fronteira

	// alive é governado pelo lock de MEMBERSHIP do Store (s.mu), não por r.mu:
	// só Kill/Revive/eleição o mutam, sempre sob s.mu.Lock; os appends lêem-no
	// sob s.mu.RLock via aliveReplicas.
	alive bool

	mu      sync.RWMutex          // protege APENAS a estrutura do mapa streams
	streams map[string]*streamLog // valor mutado sob o stripe do stream, não sob mu
	count   atomic.Uint64         // commit index (total de eventos): base da eleição
}

func newReplica(id int, region string) *replica {
	return &replica{
		id:      id,
		region:  region,
		alive:   true,
		streams: make(map[string]*streamLog),
	}
}

// streamOf devolve o streamLog do stream, criando-o se necessário. A leitura do
// ponteiro é sob r.mu.RLock (caminho quente, concorrente entre streams); só a
// criação de um stream novo escala a r.mu.Lock (double-checked). O streamLog
// devolvido é depois mutado sob o stripe do stream detido pelo chamador.
func (r *replica) streamOf(stream string) *streamLog {
	r.mu.RLock()
	sl := r.streams[stream]
	r.mu.RUnlock()
	if sl != nil {
		return sl
	}
	r.mu.Lock()
	if sl = r.streams[stream]; sl == nil {
		sl = newStreamLog()
		r.streams[stream] = sl
	}
	r.mu.Unlock()
	return sl
}

// peekStream devolve o streamLog existente do stream (ou nil). Sob r.mu.RLock.
func (r *replica) peekStream(stream string) *streamLog {
	r.mu.RLock()
	sl := r.streams[stream]
	r.mu.RUnlock()
	return sl
}

// store aplica um evento à réplica: append ao log do stream, actualiza o índice
// de dedup do stream e incrementa o commit index. É chamado com o stripe do stream
// detido (um único escritor por stream), pelo que a mutação de sl.events/sl.dedup
// não precisa de r.mu; só a resolução/criação do streamLog toca r.mu.
func (r *replica) store(ev Event) {
	sl := r.streamOf(ev.StreamID)
	sl.events = append(sl.events, ev)
	if hasIdempotency(ev.RunID, ev.StepID) {
		sl.dedup[ev.IdempotencyKey] = ev.Seq
	}
	r.count.Add(1)
}

// lastSeq devolve o último seq committed do stream (0 se inexistente). O seq é
// gapless a começar em 1, logo é igual ao comprimento do log do stream. Chamado
// com o stripe do stream detido (sl.events estável).
func (r *replica) lastSeq(stream string) uint64 {
	sl := r.peekStream(stream)
	if sl == nil {
		return 0
	}
	return uint64(len(sl.events))
}

// lookupDedup devolve, se existir, o evento committed apontado por uma
// idempotency_key DENTRO do stream dado (clone defensivo). Chamado com o stripe do
// stream detido.
func (r *replica) lookupDedup(stream, key string) (dedupRef, Event, bool) {
	sl := r.peekStream(stream)
	if sl == nil {
		return dedupRef{}, Event{}, false
	}
	seq, ok := sl.dedup[key]
	if !ok || seq == 0 || seq > uint64(len(sl.events)) {
		return dedupRef{}, Event{}, false
	}
	return dedupRef{stream: stream, seq: seq}, sl.events[seq-1].clone(), true
}

// readStream devolve clones dos eventos committed do stream com seq >= fromSeq.
// Chamado com o stripe do stream detido em RLock (sl.events estável face a appends)
// e s.mu.RLock (exclui resync). A resolução do ponteiro é sob r.mu.RLock.
func (r *replica) readStream(stream string, fromSeq uint64) ([]Event, bool) {
	sl := r.peekStream(stream)
	if sl == nil || len(sl.events) == 0 {
		return nil, false
	}
	out := make([]Event, 0, len(sl.events))
	for _, ev := range sl.events {
		if ev.Seq >= fromSeq {
			out = append(out, ev.clone())
		}
	}
	return out, true
}

// getCount devolve o commit index da réplica (atómico; usado pela eleição).
func (r *replica) getCount() uint64 {
	return r.count.Load()
}

// resyncFrom copia integralmente o estado de src para r (log shipping). Usado
// quando uma réplica é revivida, para a repor no in-sync replica set. É chamado
// sob s.mu.Lock (membership exclusivo): nenhum append corre em paralelo (todos
// detêm s.mu.RLock), pelo que a cópia é consistente. Reconstrói streamLogs
// independentes (slices e mapas próprios) para que a réplica revivida não partilhe
// estado com a fonte.
func (r *replica) resyncFrom(src *replica) {
	r.streams = make(map[string]*streamLog, len(src.streams))
	for s, sl := range src.streams {
		cp := &streamLog{
			events: make([]Event, len(sl.events)),
			dedup:  make(map[string]uint64, len(sl.dedup)),
		}
		copy(cp.events, sl.events)
		for k, v := range sl.dedup {
			cp.dedup[k] = v
		}
		r.streams[s] = cp
	}
	r.count.Store(src.count.Load())
}

// aliveReplicas devolve as réplicas vivas (sob s.mu — membership).
func (s *Store) aliveReplicas() []*replica {
	out := make([]*replica, 0, len(s.replicas))
	for _, r := range s.replicas {
		if r.alive {
			out = append(out, r)
		}
	}
	return out
}

// electLeader escolhe como líder a réplica viva mais actualizada (maior commit
// index; desempate pelo menor id). Devolve false se não houver candidato elegível.
// Deve ser chamado sob s.mu.Lock (membership exclusivo).
//
// Durabilidade: uma réplica cujo commit index seja INFERIOR ao último commit
// confirmado por quórum (s.committed) NÃO é elegível — promovê-la serviria um
// log truncado como autoritativo e completo, perdendo silenciosamente eventos
// confirmados. Nesse caso o store fica indisponível (leaderID = -1, Read/Append
// devolvem ErrNoQuorum) até uma réplica suficientemente actualizada regressar.
//
// Soberania (ADR-011): quando há fronteira regional configurada, uma réplica fora
// da região do board NÃO é elegível a líder — o failover fica preso à fronteira,
// nunca elege liderança cross-border. (Em condições normais isto é sempre
// verdade, porque New rejeita fail-closed qualquer réplica fora da fronteira.)
func (s *Store) electLeader() bool {
	committed := s.committed.Load()
	best := -1
	var bestCount uint64
	for _, r := range s.replicas {
		if !r.alive || !s.regionAllowed(r.region) {
			continue
		}
		c := r.getCount()
		if c < committed {
			continue
		}
		if best == -1 || c > bestCount || (c == bestCount && r.id < best) {
			best = r.id
			bestCount = c
		}
	}
	s.leaderID = best
	return best != -1
}

// regionAllowed indica se uma réplica na região dada pode participar no cluster:
// sem fronteira configurada (s.region == "") tudo é permitido; com fronteira, só a
// mesma região (case-insensitive) é permitida — fail-closed para região vazia.
func (s *Store) regionAllowed(region string) bool {
	if s.region == "" {
		return true
	}
	return region != "" && region == s.region
}

// leader devolve a réplica líder actual (sob s.mu). Pode ser nil se todas as
// réplicas estiverem mortas (ou indisponível por perda de quórum durável).
func (s *Store) leader() *replica {
	if s.leaderID < 0 {
		return nil
	}
	return s.replicas[s.leaderID]
}

// --- Controlo de teste do cluster -----------------------------------------

// Leader devolve o id da réplica líder actual, ou -1 se não houver réplica viva.
func (s *Store) Leader() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.leaderID
}

// AliveCount devolve o número de réplicas vivas.
func (s *Store) AliveCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.aliveReplicas())
}

// Quorum devolve o quórum configurado.
func (s *Store) Quorum() int {
	return s.quorum
}

// Region devolve a região de soberania do board (fronteira), ou "" se não houver
// fronteira configurada.
func (s *Store) Region() string {
	return s.region
}

// SovereigntyBoard devolve o id do board de soberania associado à fronteira
// (WithSovereigntyBoard), ou "" se a fronteira foi declarada sem board (WithRegion)
// ou não há fronteira. É um rótulo de observabilidade/auditoria; a decisão da
// fronteira é feita por Region, não por este valor.
func (s *Store) SovereigntyBoard() string {
	return s.board
}

// ReplicaRegion devolve a região da réplica id, ou "" se o id for inválido.
func (s *Store) ReplicaRegion(id int) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if id < 0 || id >= len(s.replicas) {
		return ""
	}
	return s.replicas[id].region
}

// Replicas devolve os ids de todas as réplicas (vivas e mortas).
func (s *Store) Replicas() []int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]int, len(s.replicas))
	for i, r := range s.replicas {
		ids[i] = r.id
	}
	return ids
}

// IsAlive indica se a réplica id está viva.
func (s *Store) IsAlive(id int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if id < 0 || id >= len(s.replicas) {
		return false
	}
	return s.replicas[id].alive
}

// Kill derruba a réplica id. Se era o líder, dispara imediatamente uma eleição.
// Devolve ErrInvalidReplica se o id for inválido ou a réplica já estiver morta.
func (s *Store) Kill(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id < 0 || id >= len(s.replicas) || !s.replicas[id].alive {
		return ErrInvalidReplica
	}
	s.replicas[id].alive = false
	if s.leaderID == id {
		s.electLeader()
	}
	return nil
}

// Revive repõe a réplica id, ressincronizando-a a partir do líder actual para a
// devolver ao in-sync replica set. Devolve ErrInvalidReplica se o id for
// inválido ou a réplica já estiver viva.
func (s *Store) Revive(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id < 0 || id >= len(s.replicas) || s.replicas[id].alive {
		return ErrInvalidReplica
	}
	if l := s.leader(); l != nil {
		s.replicas[id].resyncFrom(l)
	}
	s.replicas[id].alive = true
	// Sem líder vivo (store indisponível por perda de quórum) não há de onde
	// ressincronizar: a réplica é revivida com o seu próprio commit index. A
	// eleição só a promove se estiver ao nível do último commit confirmado por
	// quórum (s.committed); caso contrário o store permanece indisponível
	// (leaderID = -1) em vez de servir um log truncado como autoritativo.
	if s.leaderID == -1 {
		s.electLeader()
	}
	return nil
}
