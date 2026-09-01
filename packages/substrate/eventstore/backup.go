package eventstore

import (
	"context"
	"sort"
)

// AOS-101 — primitivas de backup/PITR zero-dep, ADITIVAS ao Store concreto.
//
// Estas operações NÃO pertencem à interface EventStore (append-only estrito,
// travada por TestAppendOnly_NoMutationMethods): são métodos do tipo concreto
// *Store e portas separadas (BackupSource/RestoreSink). O objectivo é permitir
// um SNAPSHOT/EXPORT consistente que preserva o ENVELOPE de cada evento intacto
// (EventID/Ts/Seq originais) e um caminho de RESTAURO que reinsere os eventos
// SEM reatribuir o envelope — ao contrário de Append, que gera event_id/ts/seq
// novos. Tudo em stdlib, sem dependências externas.

// BackupSource é a porta de LEITURA para um backup consistente do Event Store.
// Itera os streams e exporta os eventos crus (envelope intacto) até um seq-alvo
// por stream. A consistência é POR-STREAM: cada stream é lido sob o seu stripe
// (RLock), pelo que o snapshot de um stream nunca apanha um append a meio; entre
// streams distintos não há ponto de consistência global (nem faz sentido — a
// ordem total do log é por stream, ADR-007/AOS-100). Expõe também a fronteira de
// soberania (Region/SovereigntyBoard) para o exportador a fazer valer (ADR-011).
type BackupSource interface {
	// Streams devolve os ids de todos os streams com eventos committed, ordenados
	// (determinista).
	Streams() []string
	// StreamHead devolve o último seq committed do stream (0 se inexistente).
	StreamHead(ctx context.Context, streamID string) (uint64, error)
	// SnapshotStream devolve clones dos eventos committed do stream com
	// seq <= throughSeq (throughSeq == 0 ⇒ todos os committed), com o ENVELOPE
	// intacto (EventID/Ts/Seq originais), ordenados por seq ascendente.
	SnapshotStream(ctx context.Context, streamID string, throughSeq uint64) ([]Event, error)
	// Region devolve a fronteira regional de soberania do board.
	Region() string
	// SovereigntyBoard devolve o rótulo do board de soberania.
	SovereigntyBoard() string
}

// RestoreSink é a porta de ESCRITA de restauro que PRESERVA o envelope. Ao
// contrário de Append (reatribui EventID/Ts/Seq), IngestStream reinsere os
// eventos exactamente como estavam no backup, validando a ordem gapless e a
// idempotência. Nunca faz parte da interface EventStore.
//
// # A regra de recuperação, e porque NÃO é «atómico»
//
// A tentação era exigir all-or-nothing. O [Store] de referência dá-o de graça (valida
// o lote todo antes de escrever, e escreve em memória sob lock), mas um substrato
// REPLICADO não pode: no JetStream cada evento é um publish, e uma falha de rede ao
// k-ésimo deixa 1..k-1 DURÁVEIS. Exigir atomicidade produziria uma porta que uma das
// implementações cumpriria a mentir.
//
// Pior: nesse substrato o log é append-only imposto pelo SERVIDOR (deny_purge, AOS-100),
// pelo que um restauro falhado a meio NÃO SE LIMPA — não há como truncar o que ficou.
// Se a porta não previsse isto, um erro a meio envenenaria o stream-alvo para sempre e
// a única saída seria restaurar para outro.
//
// Por isso a propriedade exigida é outra, e é a que o chamador realmente precisa:
//
//	REPETIR A CHAMADA IDÊNTICA APÓS UM ERRO CONVERGE PARA O ESTADO PRETENDIDO.
//
// As duas implementações honram-na por caminhos diferentes — a de referência porque
// não deixou rasto nenhum, a replicada porque RETOMA do que já lá está, verificando
// que o prefixo presente é o mesmo do lote. Um prefixo que NÃO bata certo é
// [ErrRestoreDivergent]: duas histórias diferentes costuradas verificariam como
// íntegras, e é precisamente o que nunca pode passar em silêncio.
//
// Consequência para quem escreve um restaurador: ao erro, repita a MESMA chamada com o
// MESMO lote. Não corte o lote pelo que julga ter passado — quem sabe o que passou é o
// sink, e é ele que o vai descobrir.
type RestoreSink interface {
	// IngestStream reinsere, preservando o envelope, os eventos dados no stream,
	// validando que continuam o log de forma gapless (seq = último+1, +2, ...) e
	// que o envelope é coerente. É replicado a todas as réplicas vivas com quórum.
	IngestStream(ctx context.Context, streamID string, events []Event) error
}

// Garantias de tipo: o Store concreto satisfaz ambas as portas.
var (
	_ BackupSource = (*Store)(nil)
	_ RestoreSink  = (*Store)(nil)
)

// Streams devolve os ids de todos os streams com eventos committed no líder,
// ordenados lexicograficamente (determinista — imagem estável de Replicas()).
// Detém s.mu.RLock (membership) e r.mu.RLock (estrutura do mapa de streams).
func (s *Store) Streams() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	l := s.leader()
	if l == nil {
		return nil
	}
	l.mu.RLock()
	out := make([]string, 0, len(l.streams))
	for name := range l.streams {
		out = append(out, name)
	}
	l.mu.RUnlock()
	sort.Strings(out)
	return out
}

// StreamHead devolve o último seq committed do stream. Detém o stripe do stream
// em RLock (snapshot consistente do comprimento) e s.mu.RLock (líder estável).
func (s *Store) StreamHead(ctx context.Context, streamID string) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if s.closed.Load() {
		return 0, ErrClosed
	}
	stripe := s.stripes.forStream(streamID)
	stripe.RLock()
	defer stripe.RUnlock()
	s.mu.RLock()
	defer s.mu.RUnlock()
	l := s.leader()
	if l == nil {
		return 0, ErrNoQuorum
	}
	return l.lastSeq(streamID), nil
}

// SnapshotStream devolve os eventos committed do stream com seq <= throughSeq
// (0 ⇒ todos), com o envelope intacto. É um snapshot consistente por-stream: o
// stripe em RLock exclui appends a ESTE stream durante a leitura, pelo que o
// resultado é gapless e coerente com um instante. throughSeq acima do head é
// tratado como "todos" (limita ao que está committed — nunca inventa eventos).
func (s *Store) SnapshotStream(ctx context.Context, streamID string, throughSeq uint64) ([]Event, error) {
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
	all, ok := l.readStream(streamID, 1)
	if !ok {
		return nil, ErrStreamNotFound
	}
	if throughSeq == 0 {
		return all, nil
	}
	out := all[:0:0]
	for _, ev := range all {
		if ev.Seq <= throughSeq {
			out = append(out, ev)
		}
	}
	return out, nil
}

// IngestStream reinsere eventos no stream PRESERVANDO o envelope (restauro/PITR).
// Detém o stripe do stream em Lock (único escritor do stream) e s.mu.RLock
// (membership estável), na MESMA ordem de aquisição de Append — sem deadlock.
//
// Validação (fail-closed antes de qualquer escrita):
//   - quórum de réplicas vivas (senão ErrNoQuorum, sem deixar rasto);
//   - cada evento continua o log de forma gapless: o primeiro em último+1 e os
//     seguintes contíguos (senão ErrRestoreOrder);
//   - o envelope é coerente: StreamID igual e EventID não-vazio (senão
//     ErrRestoreEnvelope).
//
// Aplicado o lote, o envelope (EventID/Ts/Seq/IdempotencyKey) fica idêntico ao do
// backup — nada é reatribuído. O índice de dedup por stream é reconstruído a
// partir dos eventos reinseridos, tal como no caminho normal.
func (s *Store) IngestStream(ctx context.Context, streamID string, events []Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.closed.Load() {
		return ErrClosed
	}
	if len(events) == 0 {
		return nil
	}
	stripe := s.stripes.forStream(streamID)
	stripe.Lock()
	defer stripe.Unlock()

	s.mu.RLock()
	defer s.mu.RUnlock()
	l := s.leader()
	if l == nil {
		return ErrNoQuorum
	}
	alive := s.aliveReplicas()
	if len(alive) < s.quorum {
		return ErrNoQuorum
	}

	last := l.lastSeq(streamID)
	for i := range events {
		ev := events[i]
		if ev.StreamID != streamID || ev.EventID == "" {
			return ErrRestoreEnvelope
		}
		want := last + uint64(i) + 1
		if ev.Seq != want {
			return ErrRestoreOrder
		}
	}
	for i := range events {
		ev := events[i]
		for _, r := range alive {
			r.store(ev.clone())
		}
	}
	s.raiseCommitted(l.getCount())
	return nil
}
