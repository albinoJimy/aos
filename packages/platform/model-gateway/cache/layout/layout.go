// Package layout impõe a GUARDA DE LAYOUT de prompt cache-estável do Model Gateway
// (AOS-060, tecnica/06 §7, ADR-009): estrutura o prompt materializado em TRÊS
// ZONAS e valida, por turno, as invariantes que sustentam o prefix caching sem
// contradizer o replay fiel (ADR-010).
//
//	┌─────────────────────────────────────────────────────────────────────┐
//	│ ZONA 1 — Prefixo IMUTÁVEL (system + tool set congelado no run)        │
//	│   byte-idêntico entre turnos do MESMO run ⇒ CACHE HIT                 │
//	├─────────────────────────────────────────────────────────────────────┤
//	│ ZONA 2 — Tail APPEND-ONLY (memory_context, timestamps, resultados)   │
//	│   só cresce; nunca muta o prefixo                                     │
//	├─────────────────────────────────────────────────────────────────────┤
//	│ ZONA 3 — Compressão (só em checkpoints assíncronos, ver cache/compaction) │
//	└─────────────────────────────────────────────────────────────────────┘
//
// # O que a guarda impõe (fail-closed)
//
//   - Prefixo BYTE-IDÊNTICO: o prefixo do turno N tem de resolver contra o hash
//     pinado no turno 1 do run. Uma reordenação/mutação é REJEITADA
//     ([KindPrefixReordered]).
//   - Tool set CONGELADO: um turno cujo tool-set-hash diverge do congelado é
//     recusado ([KindToolSetDrift]) — novas tools MCP só em runs novos (AOS-050).
//   - Tail APPEND-ONLY: o tail do turno N tem de ESTENDER byte-a-byte o do turno
//     N-1; reescrever ([KindTailRewritten]) ou encolher ([KindTailShrunk]) é recusado.
//   - Manifesto por turno: o hash do prompt materializado é gravado por turno; o
//     mesmo hash que sustenta o cache-hit sustenta o replay fiel ([Guard.Replay]).
//
// # Estado por-run FORA do Gateway (stateless)
//
// A guarda NÃO detém estado: o hash do prefixo pinado, o tool-set-hash e os hashes
// materializados por turno vivem numa PORTA [RunLayoutLedger] (impl de referência
// [MemoryLedger]). O Gateway continua stateless — consulta/pina via a porta.
//
// # Determinismo
//
// Sem time.Now nem rand: os hashes são função pura dos bytes materializados
// (sha256 canónico). Os timestamps do tail são DADOS de entrada (bytes já
// serializados pelo chamador), nunca gerados aqui.
package layout

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// hashPrefix é o prefixo dos hashes emitidos (coerente com o resto do repo:
// "sha256:<hex>", tal como o PromptAssembler de AOS-013).
const hashPrefix = "sha256:"

// Erros-sentinela da porta/ledger.
var (
	// ErrNoRunID — runID vazio (fail-closed: um turno sem run não é guardável).
	ErrNoRunID = errors.New("layout: runID vazio")
	// ErrRunNotPinned — Advance sobre um run que nunca foi pinado (invariante interna).
	ErrRunNotPinned = errors.New("layout: run nao pinado")
	// ErrManifestConflict — tentativa de gravar um turno já gravado com um hash
	// DIVERGENTE. O manifesto é imutável por turno (replay fiel, ADR-010).
	ErrManifestConflict = errors.New("layout: conflito de manifesto (hash do turno divergente)")
)

// ViolationKind classifica uma violação do layout cache-estável.
type ViolationKind string

const (
	// KindPrefixReordered — o prefixo do turno diverge do pinado no turno 1
	// (reordenação/mutação do prefixo imutável). Cache thrash.
	KindPrefixReordered ViolationKind = "prefix_reordered"
	// KindToolSetDrift — o tool-set-hash do turno diverge do congelado no run (uma
	// tool nova/alterada a meio do run). Novas tools só em runs novos.
	KindToolSetDrift ViolationKind = "toolset_drift"
	// KindTailRewritten — o tail do turno NÃO estende byte-a-byte o do turno anterior
	// (reescreveu bytes já materializados). Viola append-only.
	KindTailRewritten ViolationKind = "tail_rewritten"
	// KindTailShrunk — o tail do turno é MAIS CURTO que o do turno anterior (o tail
	// só cresce). Viola append-only.
	KindTailShrunk ViolationKind = "tail_shrunk"
	// KindMaterializedSplit — o materializado não começa pelo prefixo declarado
	// (fronteira prefixo/tail corrompida).
	KindMaterializedSplit ViolationKind = "materialized_split"
	// KindReplayMismatch — a re-materialização de um turno não reproduz o hash
	// gravado no manifesto (replay infiel).
	KindReplayMismatch ViolationKind = "replay_mismatch"
	// KindUnknownTurn — pedido de replay de um turno sem entrada no manifesto.
	KindUnknownTurn ViolationKind = "unknown_turn"
	// KindManifestConflict — um turno já gravado é regravado com um hash materializado
	// DIVERGENTE (o manifesto é imutável por turno). É a quebra de fidelidade de replay
	// equivalente a um replay infiel (ADR-010), sinalizada como variância como as demais.
	KindManifestConflict ViolationKind = "manifest_conflict"
)

// Violation é o erro TIPADO de uma violação do layout. Carrega a natureza e as
// testemunhas (Want/Got) para diagnóstico e para o SINAL de variância. É
// comparável por errors.Is contra as sentinelas [ErrPrefixReordered] etc. (compara
// apenas o Kind), preservando as testemunhas na instância concreta.
type Violation struct {
	Kind  ViolationKind
	RunID string
	Turn  int
	// Want/Got são as testemunhas (hashes ou comprimentos) da divergência.
	Want string
	Got  string
}

// Error implementa error.
func (v *Violation) Error() string {
	return fmt.Sprintf("layout: violacao %s no run %q turno %d (quer=%s, obteve=%s)",
		v.Kind, v.RunID, v.Turn, v.Want, v.Got)
}

// Is compara pela NATUREZA (Kind), para que errors.Is(err, ErrPrefixReordered)
// funcione independentemente das testemunhas concretas.
func (v *Violation) Is(target error) bool {
	t, ok := target.(*Violation)
	return ok && t.Kind == v.Kind
}

// Sentinelas por natureza (para errors.Is). Só o Kind é significativo.
var (
	ErrPrefixReordered   = &Violation{Kind: KindPrefixReordered}
	ErrToolSetDrift      = &Violation{Kind: KindToolSetDrift}
	ErrTailRewritten     = &Violation{Kind: KindTailRewritten}
	ErrTailShrunk        = &Violation{Kind: KindTailShrunk}
	ErrMaterializedSplit = &Violation{Kind: KindMaterializedSplit}
	ErrReplayMismatch    = &Violation{Kind: KindReplayMismatch}
	ErrUnknownTurn       = &Violation{Kind: KindUnknownTurn}
	// ErrManifestConflictSignal é a sentinela TIPADA (por Kind) do conflito de
	// manifesto emitido pela guarda em [Guard.Admit]. Distinta do erro-sentinela cru
	// [ErrManifestConflict] (devolvido pelo Advance de baixo nível): esta é uma
	// *Violation com testemunhas Want/Got (hash gravado vs. novo), sinalizável ao sink.
	ErrManifestConflictSignal = &Violation{Kind: KindManifestConflict}
)

// Turn é a materialização guardada de UM turno de um run: a projecção do
// [agentruntime.PromptView] produzido pelo assembler cache-estável (via
// cache/freeze), mais a identidade do run/turno e o tool-set-hash congelado.
type Turn struct {
	// RunID identifica o run (a fronteira de estabilidade do prefixo).
	RunID string
	// Index é o índice 1-based do turno.
	Index int
	// ToolSetHash é o hash do tool set CONGELADO que produziu o prefixo (AOS-050).
	ToolSetHash string
	// View é o prompt materializado do turno (prefixo + tail).
	View agentruntime.PromptView
}

// ViolationSink recebe violações SINALIZADAS pela guarda (para além de as REJEITAR
// por erro). Torna uma reordenação de prefixo / drift de tool set observável como
// evento (variância), sem acoplar a guarda ao Event Store. A impl real (append-only)
// é de observabilidade; testes fornecem um sink em memória.
type ViolationSink interface {
	Signal(v *Violation)
}

// Guard é a GUARDA de layout cache-estável. Stateless: delega o estado por-run à
// porta [RunLayoutLedger]. Construir com [NewGuard].
type Guard struct {
	ledger RunLayoutLedger
	sink   ViolationSink
}

// Option configura o [Guard].
type Option func(*Guard)

// WithSink liga um [ViolationSink]: cada violação é SINALIZADA (evento de variância)
// antes de ser REJEITADA (erro). Default: sem sink (só rejeita).
func WithSink(s ViolationSink) Option {
	return func(g *Guard) {
		if s != nil {
			g.sink = s
		}
	}
}

// NewGuard constrói a guarda sobre um ledger por-run. Um ledger nil usa um
// [MemoryLedger] de referência (nunca fail-open por ausência de estado).
func NewGuard(ledger RunLayoutLedger, opts ...Option) *Guard {
	if ledger == nil {
		ledger = NewMemoryLedger()
	}
	g := &Guard{ledger: ledger}
	for _, o := range opts {
		o(g)
	}
	return g
}

// Admit valida o layout de UM turno e, em sucesso, avança o estado do run
// (append-only). É a operação da HOT PATH da montagem: recomputa os hashes a
// partir dos BYTES materializados (não confia em hashes fornecidos pelo chamador),
// pina o prefixo/tool-set no primeiro turno e, nos seguintes, impõe:
//
//	prefixo byte-idêntico ∧ tool-set congelado ∧ tail append-only
//
// Fail-closed: qualquer violação devolve uma [*Violation] tipada (e é sinalizada
// se houver sink), sem avançar o estado. NÃO invoca compressão — a guarda não tem
// sequer uma referência ao compactor (prova estrutural off-hot-path em cache/compaction).
func (g *Guard) Admit(t Turn) error {
	if t.RunID == "" {
		return ErrNoRunID
	}
	prefix := t.View.Prefix
	mat := t.View.Materialized

	// Fronteira prefixo/tail: o materializado TEM de começar exactamente pelo prefixo.
	if len(mat) < len(prefix) || !bytes.Equal(mat[:len(prefix)], prefix) {
		return g.reject(&Violation{Kind: KindMaterializedSplit, RunID: t.RunID, Turn: t.Index,
			Want: "materializado inicia pelo prefixo", Got: "fronteira corrompida"})
	}
	prefixHash := hashTagged(prefix)
	matHash := hashTagged(mat)
	tail := mat[len(prefix):]
	tailHash := hashTagged(tail)

	// check impõe as invariantes prefixo/tool-set/tail. É invocado pelo ledger SOB O
	// MESMO LOCK da gravação (via AdmitAndAdvance), pelo que turnos CONCORRENTES do
	// MESMO run não podem validar ambos contra um cursor obsoleto: cada turno é
	// validado contra o cursor imediatamente anterior (fecha o TOCTOU entre observar
	// o cursor e avançá-lo). No primeiro turno do run (created) não há invariante a
	// impor — só se pina. A lógica das invariantes fica AQUI, no Guard; só a sua
	// avaliação corre atomicamente no ledger.
	check := func(pinned PinnedRun, created bool) *Violation {
		if created {
			return nil
		}
		// Tool set congelado: um turno cujo tool-set-hash diverge do pinado no run é
		// recusado — uma tool nova/alterada a meio do run NÃO entra (AOS-050/EPIC-05).
		if t.ToolSetHash != pinned.ToolSetHash {
			return &Violation{Kind: KindToolSetDrift, RunID: t.RunID, Turn: t.Index,
				Want: pinned.ToolSetHash, Got: t.ToolSetHash}
		}
		// Prefixo byte-idêntico: recomputado dos bytes, comparado ao pinado no turno 1.
		if prefixHash != pinned.PrefixHash {
			return &Violation{Kind: KindPrefixReordered, RunID: t.RunID, Turn: t.Index,
				Want: pinned.PrefixHash, Got: prefixHash}
		}
		// Tail append-only: o tail do turno tem de ESTENDER byte-a-byte o anterior.
		if len(tail) < pinned.LastTailLen {
			return &Violation{Kind: KindTailShrunk, RunID: t.RunID, Turn: t.Index,
				Want: fmt.Sprintf(">=%d bytes", pinned.LastTailLen), Got: fmt.Sprintf("%d bytes", len(tail))}
		}
		if pinned.LastTailLen > 0 {
			if got := hashTagged(tail[:pinned.LastTailLen]); got != pinned.LastTailHash {
				return &Violation{Kind: KindTailRewritten, RunID: t.RunID, Turn: t.Index,
					Want: pinned.LastTailHash, Got: got}
			}
		}
		return nil
	}

	// Check-and-advance ATÓMICO por-run: valida (check, sob o lock) e grava o cursor +
	// manifesto na mesma secção crítica. Uma invariante violada — incl. um conflito de
	// manifesto (KindManifestConflict, replay infiel) — volta como *Violation tipada e é
	// SINALIZADA ao sink (variância) além de REJEITADA (fail-closed), como as demais.
	if err := g.ledger.AdmitAndAdvance(AdmitRequest{
		RunID:            t.RunID,
		Turn:             t.Index,
		PrefixHash:       prefixHash,
		ToolSetHash:      t.ToolSetHash,
		TailLen:          len(tail),
		TailHash:         tailHash,
		MaterializedHash: matHash,
		Check:            check,
	}); err != nil {
		var v *Violation
		if errors.As(err, &v) {
			return g.reject(v)
		}
		return err
	}
	return nil
}

// Replay verifica a FIDELIDADE de replay de um turno: re-materializado, o prompt
// tem de reproduzir o hash gravado no manifesto (o mesmo que sustenta o cache-hit).
// Não avança o estado — é uma consulta pura. Devolve [KindUnknownTurn] se o turno
// não foi gravado, ou [KindReplayMismatch] se o hash re-materializado diverge.
func (g *Guard) Replay(runID string, turn int, view agentruntime.PromptView) error {
	want, ok := g.ledger.TurnHash(runID, turn)
	if !ok {
		return &Violation{Kind: KindUnknownTurn, RunID: runID, Turn: turn}
	}
	got := hashTagged(view.Materialized)
	if got != want {
		return &Violation{Kind: KindReplayMismatch, RunID: runID, Turn: turn, Want: want, Got: got}
	}
	return nil
}

// PromptHash devolve o hash materializado gravado para um turno (manifesto), o
// registo por turno que preserva cache-hit E replay fiel (ADR-009/010).
func (g *Guard) PromptHash(runID string, turn int) (string, bool) {
	return g.ledger.TurnHash(runID, turn)
}

// Pinned devolve o registo pinado do run (prefixo/tool-set/cursor do tail).
func (g *Guard) Pinned(runID string) (PinnedRun, bool) {
	return g.ledger.Get(runID)
}

// reject sinaliza (se houver sink) e devolve a violação como erro (fail-closed).
func (g *Guard) reject(v *Violation) error {
	if g.sink != nil {
		g.sink.Signal(v)
	}
	return v
}

// hashTagged calcula sha256 e devolve "sha256:<hex>" (formato canónico do repo).
func hashTagged(b []byte) string {
	sum := sha256.Sum256(b)
	return hashPrefix + hex.EncodeToString(sum[:])
}
