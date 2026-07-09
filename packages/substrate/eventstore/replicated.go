package eventstore

// dedupRef aponta a localização committed de uma idempotency_key.
type dedupRef struct {
	stream string
	seq    uint64
}

// replica é uma cópia in-process do log. Cada réplica viva mantém o log completo
// e o índice de deduplicação (reconstrutível a partir do log committed).
type replica struct {
	id      int
	alive   bool
	streams map[string][]Event  // stream_id -> eventos (índice i => seq i+1)
	dedup   map[string]dedupRef // idempotency_key -> localização
	count   uint64              // commit index (total de eventos): base da eleição
}

func newReplica(id int) *replica {
	return &replica{
		id:      id,
		alive:   true,
		streams: make(map[string][]Event),
		dedup:   make(map[string]dedupRef),
	}
}

// lastSeq devolve o último seq committed do stream (0 se inexistente). O seq é
// gapless a começar em 1, logo é igual ao comprimento do log do stream.
func (r *replica) lastSeq(stream string) uint64 {
	return uint64(len(r.streams[stream]))
}

// store aplica um evento à réplica: append ao log do stream, actualiza o índice
// de dedup e incrementa o commit index.
func (r *replica) store(ev Event) {
	r.streams[ev.StreamID] = append(r.streams[ev.StreamID], ev)
	if hasIdempotency(ev.RunID, ev.StepID) {
		r.dedup[ev.IdempotencyKey] = dedupRef{stream: ev.StreamID, seq: ev.Seq}
	}
	r.count++
}

// lookup devolve o evento committed numa localização de dedup.
func (r *replica) lookup(ref dedupRef) Event {
	return r.streams[ref.stream][ref.seq-1]
}

// resyncFrom copia integralmente o estado de src para r (log shipping). Usado
// quando uma réplica é revivida, para a repor no in-sync replica set.
func (r *replica) resyncFrom(src *replica) {
	r.streams = make(map[string][]Event, len(src.streams))
	for s, log := range src.streams {
		cp := make([]Event, len(log))
		copy(cp, log)
		r.streams[s] = cp
	}
	r.dedup = make(map[string]dedupRef, len(src.dedup))
	for k, v := range src.dedup {
		r.dedup[k] = v
	}
	r.count = src.count
}

// aliveReplicas devolve as réplicas vivas (sob s.mu).
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
// Deve ser chamado sob s.mu.
//
// Durabilidade: uma réplica cujo commit index seja INFERIOR ao último commit
// confirmado por quórum (s.committed) NÃO é elegível — promovê-la serviria um
// log truncado como autoritativo e completo, perdendo silenciosamente eventos
// confirmados. Nesse caso o store fica indisponível (leaderID = -1, Read/Append
// devolvem ErrNoQuorum) até uma réplica suficientemente actualizada regressar.
func (s *Store) electLeader() bool {
	best := -1
	for _, r := range s.replicas {
		if !r.alive || r.count < s.committed {
			continue
		}
		if best == -1 {
			best = r.id
			continue
		}
		cur := s.replicas[best]
		if r.count > cur.count || (r.count == cur.count && r.id < cur.id) {
			best = r.id
		}
	}
	s.leaderID = best
	return best != -1
}

// leader devolve a réplica líder actual (sob s.mu). Pode ser nil se todas as
// réplicas estiverem mortas.
func (s *Store) leader() *replica {
	if s.leaderID < 0 {
		return nil
	}
	return s.replicas[s.leaderID]
}

// --- Controlo de teste do cluster -----------------------------------------

// Leader devolve o id da réplica líder actual, ou -1 se não houver réplica viva.
func (s *Store) Leader() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leaderID
}

// AliveCount devolve o número de réplicas vivas.
func (s *Store) AliveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.aliveReplicas())
}

// Quorum devolve o quórum configurado.
func (s *Store) Quorum() int {
	return s.quorum
}

// Replicas devolve os ids de todas as réplicas (vivas e mortas).
func (s *Store) Replicas() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]int, len(s.replicas))
	for i, r := range s.replicas {
		ids[i] = r.id
	}
	return ids
}

// IsAlive indica se a réplica id está viva.
func (s *Store) IsAlive(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
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
