package bus

import "sync"

// CursorStore persiste o cursor durável de cada subscrição nomeada, por stream.
// O cursor é o último seq CONFIRMADO (ACK). A implementação DEVE ser segura para
// uso concorrente (várias subscrições podem escrever em paralelo).
//
// Contrato de durabilidade: Save só deve devolver nil quando o valor está
// durável o suficiente para sobreviver a um reinício do consumidor. A
// implementação de referência (MemoryCursorStore) é durável apenas enquanto o
// processo vive — modela o cursor externo à subscrição, que é o que permite a
// retoma após "reinício" da subscrição no mesmo processo. Produção usa um store
// persistente (ver SnapshotCursorStore e a doc do pacote).
type CursorStore interface {
	// Load devolve o último seq confirmado da subscrição para o stream. ok é
	// falso se nunca houve confirmação (arranque de raiz).
	Load(sub, stream string) (seq uint64, ok bool)
	// Save persiste o novo seq confirmado (monotónico) da subscrição no stream.
	Save(sub, stream string, seq uint64) error
}

func cursorKey(sub, stream string) string { return sub + "\x00" + stream }

// MemoryCursorStore é a implementação de referência: um mapa protegido por
// mutex. Sobrevive à "morte" de uma subscrição (Unsubscribe) porque é externo a
// ela — é isso que permite a retoma por cursor. Não sobrevive à morte do
// processo; para isso use SnapshotCursorStore com um sink persistente.
type MemoryCursorStore struct {
	mu sync.Mutex
	m  map[string]uint64
}

// NewMemoryCursorStore constrói um MemoryCursorStore vazio.
func NewMemoryCursorStore() *MemoryCursorStore {
	return &MemoryCursorStore{m: make(map[string]uint64)}
}

// Load implementa CursorStore.
func (s *MemoryCursorStore) Load(sub, stream string) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[cursorKey(sub, stream)]
	return v, ok
}

// Save implementa CursorStore. O avanço é monotónico: um Save com seq inferior
// ao já guardado é ignorado (protege contra reordenações).
func (s *MemoryCursorStore) Save(sub, stream string, seq uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := cursorKey(sub, stream)
	if cur, ok := s.m[k]; ok && seq <= cur {
		return nil
	}
	s.m[k] = seq
	return nil
}

// SnapshotCursorStore é a variante durável de referência: mantém o estado em
// memória (para leitura rápida) e ESPELHA cada escrita para um sink (persist).
// Em produção o sink escreve para disco/KV/DB; aqui documenta-se o ponto de
// integração sem puxar dependências. Se persist devolver erro, Save propaga-o e
// o estado em memória NÃO avança (fail-closed: o cursor não avança sem
// durabilidade).
type SnapshotCursorStore struct {
	mu      sync.Mutex
	m       map[string]uint64
	persist func(sub, stream string, seq uint64) error
}

// NewSnapshotCursorStore constrói um store durável. seed pré-carrega o estado
// recuperado do meio persistente (pode ser nil). persist é chamado a cada Save
// bem-sucedido; se nil, comporta-se como um MemoryCursorStore.
func NewSnapshotCursorStore(seed map[string]uint64, persist func(sub, stream string, seq uint64) error) *SnapshotCursorStore {
	m := make(map[string]uint64, len(seed))
	for k, v := range seed {
		m[k] = v
	}
	return &SnapshotCursorStore{m: m, persist: persist}
}

// Load implementa CursorStore.
func (s *SnapshotCursorStore) Load(sub, stream string) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[cursorKey(sub, stream)]
	return v, ok
}

// Save implementa CursorStore com espelhamento durável fail-closed.
func (s *SnapshotCursorStore) Save(sub, stream string, seq uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := cursorKey(sub, stream)
	if cur, ok := s.m[k]; ok && seq <= cur {
		return nil
	}
	if s.persist != nil {
		if err := s.persist(sub, stream, seq); err != nil {
			return err
		}
	}
	s.m[k] = seq
	return nil
}
