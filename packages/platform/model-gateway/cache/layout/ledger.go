package layout

import "sync"

// PinnedRun é o ESTADO POR-RUN do layout cache-estável, pinado no PRIMEIRO turno
// guardado do run e imutável nas suas testemunhas de identidade:
//
//   - PrefixHash — sha256 do prefixo IMUTÁVEL (system + tool set congelado). É a
//     testemunha de byte-identidade: qualquer turno seguinte cujo prefixo não
//     resolva contra este hash reordenou/mutou o prefixo (cache thrash, ADR-009).
//   - ToolSetHash — hash do tool set CONGELADO por run (AOS-050). Um turno de um
//     run cujo tool-set-hash diverge do congelado é REJEITADO: uma tool nova/alterada
//     a meio do run não pode entrar (novas tools só em runs novos, pinning EPIC-05).
//
// e no cursor APPEND-ONLY do ÚLTIMO turno guardado (LastTailLen/LastTailHash), que
// prova que o tail só cresce (o turno N estende byte-a-byte o tail do turno N-1).
//
// É o estado que o Gateway STATELESS mantém FORA de si, atrás da porta
// [RunLayoutLedger] — o data-plane do GW não detém estado autoritativo.
type PinnedRun struct {
	// RunID é o run a que este registo pertence.
	RunID string
	// PrefixHash é sha256("sha256:<hex>") do prefixo imutável pinado no turno 1.
	PrefixHash string
	// ToolSetHash é o hash do tool set congelado pinado no turno 1.
	ToolSetHash string
	// LastTurn é o índice do último turno guardado (append-only, cresce).
	LastTurn int
	// LastTailLen é o comprimento em bytes do tail do último turno guardado.
	LastTailLen int
	// LastTailHash é sha256 do tail do último turno guardado (para provar que o
	// turno seguinte o estende byte-a-byte).
	LastTailHash string
	// LastMaterializedHash é o hash do prompt materializado do último turno.
	LastMaterializedHash string
}

// RunLayoutLedger é a PORTA de estado por-run do layout cache-estável. O
// data-plane do Gateway é STATELESS; o hash do prefixo pinado (turno 1), o
// tool-set-hash congelado e os hashes materializados por turno (o manifesto,
// ADR-010) vivem AQUI. A impl de referência é [MemoryLedger]; produção pode
// respaldá-la num store durável (append-only). Todos os métodos são seguros para
// uso concorrente (runs distintos são chaves distintas).
type RunLayoutLedger interface {
	// Pin regista as testemunhas pinadas do run na PRIMEIRA vez que é visto. Se o
	// run já está pinado, devolve o registo existente e created=false (idempotente);
	// os hashes fornecidos são IGNORADOS após o pin — a comparação de byte-identidade
	// é responsabilidade do [Guard], que consulta o registo devolvido.
	Pin(runID, prefixHash, toolSetHash string) (rec PinnedRun, created bool, err error)
	// Get devolve o registo pinado de um run e se existe.
	Get(runID string) (PinnedRun, bool)
	// Advance regista que um turno foi validado: actualiza o cursor append-only do
	// tail (tailLen/tailHash), o último hash materializado e grava o hash
	// materializado do turno no MANIFESTO (turn -> hash), append-only. Um turno já
	// gravado com um hash DIVERGENTE é recusado ([ErrManifestConflict]) — o
	// manifesto é imutável por turno (replay fiel, ADR-010).
	//
	// NOTA: Advance é a operação de gravação de baixo nível (o Pin observa o cursor,
	// o Advance grava-o) e expõe a janela entre as duas chamadas. O caminho da guarda
	// usa [RunLayoutLedger.AdmitAndAdvance], que funde validação + gravação sob um
	// único lock. Advance mantém-se para uso directo/idempotente e testes de manifesto.
	Advance(runID string, turn, tailLen int, tailHash, materializedHash string) error
	// AdmitAndAdvance valida as invariantes de append-only e AVANÇA o cursor +
	// manifesto ATOMICAMENTE sob o MESMO lock por-run, fechando a janela TOCTOU
	// entre observar o cursor (Pin) e gravá-lo (Advance) para turnos CONCORRENTES do
	// MESMO run. No primeiro turno do run PINA (prefixHash/toolSetHash de req); nos
	// seguintes corre req.Check SOB O LOCK — as invariantes de prefixo byte-idêntico,
	// tool-set congelado e tail append-only são validadas contra o cursor imediatamente
	// anterior, nunca contra um cursor obsoleto. Devolve a [*Violation] tipada da
	// primeira invariante violada (incluindo [KindManifestConflict] se o turno já foi
	// gravado com um hash materializado divergente — replay infiel), sem avançar o
	// estado, ou nil em sucesso.
	AdmitAndAdvance(req AdmitRequest) error
	// TurnHash devolve o hash materializado gravado para um turno (consulta do
	// manifesto, base do replay fiel) e se existe.
	TurnHash(runID string, turn int) (string, bool)
}

// AdmitRequest carrega as testemunhas RECOMPUTADAS de um turno (hashes dos bytes
// materializados) para a operação atómica [RunLayoutLedger.AdmitAndAdvance]. Check
// é o predicado das invariantes de prefixo/tool-set/tail que o ledger invoca SOB O
// SEU LOCK, com o registo pinado imediatamente anterior (pinned) e se o run acabou
// de ser pinado (created); devolver uma [*Violation] aborta a gravação (fail-closed),
// nil admite. Manter o Check no chamador (a guarda) preserva a lógica de invariantes
// no [Guard] enquanto a sua avaliação corre atomicamente com a gravação no ledger.
type AdmitRequest struct {
	RunID            string
	Turn             int
	PrefixHash       string
	ToolSetHash      string
	TailLen          int
	TailHash         string
	MaterializedHash string
	Check            func(pinned PinnedRun, created bool) *Violation
}

// runState é a entrada interna do [MemoryLedger]: o registo pinado + o manifesto
// por turno.
type runState struct {
	pinned   PinnedRun
	manifest map[int]string // turn -> materialized hash (append-only, imutável)
}

// MemoryLedger é a impl de referência IN-MEMORY da [RunLayoutLedger]. Guarda o
// estado por-run num mapa protegido por mutex — determinista e seguro sob -race.
// Não tem relógio nem aleatoriedade: o estado é função pura das chamadas. Produção
// substitui-a por um store durável sem alterar o [Guard].
type MemoryLedger struct {
	mu   sync.Mutex
	runs map[string]*runState
}

// NewMemoryLedger constrói um ledger in-memory vazio.
func NewMemoryLedger() *MemoryLedger {
	return &MemoryLedger{runs: make(map[string]*runState)}
}

// Pin implementa [RunLayoutLedger].
func (l *MemoryLedger) Pin(runID, prefixHash, toolSetHash string) (PinnedRun, bool, error) {
	if runID == "" {
		return PinnedRun{}, false, ErrNoRunID
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if st, ok := l.runs[runID]; ok {
		return st.pinned, false, nil
	}
	st := &runState{
		pinned: PinnedRun{
			RunID:       runID,
			PrefixHash:  prefixHash,
			ToolSetHash: toolSetHash,
		},
		manifest: make(map[int]string),
	}
	l.runs[runID] = st
	return st.pinned, true, nil
}

// Get implementa [RunLayoutLedger].
func (l *MemoryLedger) Get(runID string) (PinnedRun, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.runs[runID]
	if !ok {
		return PinnedRun{}, false
	}
	return st.pinned, true
}

// Advance implementa [RunLayoutLedger].
func (l *MemoryLedger) Advance(runID string, turn, tailLen int, tailHash, materializedHash string) error {
	if runID == "" {
		return ErrNoRunID
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.runs[runID]
	if !ok {
		return ErrRunNotPinned
	}
	// Manifesto imutável por turno: reaplicar o MESMO turno com o MESMO hash é no-op
	// idempotente; com um hash divergente é um conflito (replay fiel exige que o
	// hash gravado de um turno nunca mude).
	if prev, seen := st.manifest[turn]; seen && prev != materializedHash {
		return ErrManifestConflict
	}
	st.manifest[turn] = materializedHash
	if turn >= st.pinned.LastTurn {
		st.pinned.LastTurn = turn
		st.pinned.LastTailLen = tailLen
		st.pinned.LastTailHash = tailHash
		st.pinned.LastMaterializedHash = materializedHash
	}
	return nil
}

// AdmitAndAdvance implementa [RunLayoutLedger]: valida (via req.Check) e grava sob
// o MESMO lock, tornando o check-and-advance atómico por-run (fecha o TOCTOU entre
// Pin e Advance para turnos concorrentes do mesmo run).
func (l *MemoryLedger) AdmitAndAdvance(req AdmitRequest) error {
	if req.RunID == "" {
		return ErrNoRunID
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	st, ok := l.runs[req.RunID]
	created := false
	if !ok {
		// Primeiro turno do run: pina as testemunhas de identidade (imutáveis).
		st = &runState{
			pinned: PinnedRun{
				RunID:       req.RunID,
				PrefixHash:  req.PrefixHash,
				ToolSetHash: req.ToolSetHash,
			},
			manifest: make(map[int]string),
		}
		l.runs[req.RunID] = st
		created = true
	}

	// Invariantes de prefixo/tool-set/tail avaliadas SOB O MESMO LOCK da gravação:
	// um turno concorrente do mesmo run é validado contra o cursor imediatamente
	// anterior, nunca contra um cursor obsoleto observado antes de outro turno gravar.
	if req.Check != nil {
		if v := req.Check(st.pinned, created); v != nil {
			return v
		}
	}

	// Manifesto imutável por turno: reaplicar o MESMO turno com o MESMO hash é no-op
	// idempotente; com um hash divergente é um conflito (replay infiel), sinalizável
	// como violação tipada (ADR-010) — ao contrário do erro cru de [MemoryLedger.Advance].
	if prev, seen := st.manifest[req.Turn]; seen && prev != req.MaterializedHash {
		return &Violation{Kind: KindManifestConflict, RunID: req.RunID, Turn: req.Turn,
			Want: prev, Got: req.MaterializedHash}
	}
	st.manifest[req.Turn] = req.MaterializedHash
	if req.Turn >= st.pinned.LastTurn {
		st.pinned.LastTurn = req.Turn
		st.pinned.LastTailLen = req.TailLen
		st.pinned.LastTailHash = req.TailHash
		st.pinned.LastMaterializedHash = req.MaterializedHash
	}
	return nil
}

// TurnHash implementa [RunLayoutLedger].
func (l *MemoryLedger) TurnHash(runID string, turn int) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.runs[runID]
	if !ok {
		return "", false
	}
	h, ok := st.manifest[turn]
	return h, ok
}
