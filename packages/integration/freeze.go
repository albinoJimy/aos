package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/toolset"
	"github.com/aos-ref/substrate/eventstore"
)

// eventTypeFrozenToolSet é o tipo canónico do evento que persiste o tool set
// congelado de um run no Event Store (AOS-155). Distinto de "run.state.transition":
// o [controlsurface.StateProjector] filtra por tipo e ignora-o.
const eventTypeFrozenToolSet = "run.toolset.frozen"

// toolSetProducerNHI é a identidade emissora do evento de persistência do freeze — o
// próprio composition root (não um agente).
const toolSetProducerNHI = "nhi:composition-root"

// ToolSetStore é o subconjunto do Event Store (AOS-002) que o [RunToolSets] durável
// usa: Append (persistir o snapshot no arranque) + Read (reconstruir na retoma).
// *[eventstore.Store] satisfá-lo. Interface mínima para desacoplar o registo do store
// concreto e o tornar testável.
type ToolSetStore interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error)
}

// RunToolSets é o registo, seguro para concorrência, dos tool sets CONGELADOS por
// run (AOS-050). O composition root regista o snapshot no arranque de cada run
// ([RunToolSets.Freeze] ou [RunToolSets.Put]) e a revalidação por chamada consulta-o
// ([RunToolSets.Frozen]) para obter a EXPECTATIVA imutável contra a qual verifica a
// definição actual. É a ponte entre o congelamento (arranque) e a mediação (caminho
// quente): sem uma entrada para o run, a revalidação nega fail-closed (default-deny —
// um run sem tool set congelado não executa tool nenhuma).
//
// Durabilidade (AOS-155): construído com [WithToolSetStore], o registo PERSISTE cada
// snapshot no Event Store ([RunToolSets.Freeze]) e RECONSTRÓI-o ([RunToolSets.Rebuild])
// após um failover — sem isto, um restart deixaria o mapa in-memory vazio e a
// revalidação colapsaria para default-deny em TODAS as tool calls do run em curso.
//
// Satisfaz [FrozenProvider].
type RunToolSets struct {
	mu    sync.RWMutex
	byRun map[string]*toolset.FrozenToolSet

	// store, quando não-nil, torna o registo DURÁVEL (persiste/reconstrói o snapshot).
	// nil ⇒ apenas in-memory (sem crash-safety) — retro-compat.
	store ToolSetStore
}

// RunToolSetsOption configura o [RunToolSets].
type RunToolSetsOption func(*RunToolSets)

// WithToolSetStore torna o registo DURÁVEL sobre o Event Store dado: cada [Freeze]
// persiste o snapshot e [Rebuild] reconstrói-o na retoma. Um store nil é ignorado
// (mantém-se in-memory).
func WithToolSetStore(store ToolSetStore) RunToolSetsOption {
	return func(r *RunToolSets) {
		if store != nil {
			r.store = store
		}
	}
}

// NewRunToolSets constrói um registo vazio. Sem opções é in-memory (retro-compat);
// com [WithToolSetStore] é durável (crash-safe).
func NewRunToolSets(opts ...RunToolSetsOption) *RunToolSets {
	r := &RunToolSets{byRun: make(map[string]*toolset.FrozenToolSet)}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Put regista (ou substitui) o tool set congelado de um run EM MEMÓRIA. O snapshot é
// imutável por construção (ver [toolset.FrozenToolSet]); guardá-lo por ponteiro não
// expõe mutação. Um frozen nil é ignorado. NÃO persiste — use [Freeze] para o registo
// durável no arranque de um run.
func (r *RunToolSets) Put(frozen *toolset.FrozenToolSet) {
	if frozen == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byRun[frozen.RunID()] = frozen
}

// Freeze é o registo DURÁVEL do arranque: persiste o snapshot no Event Store (se o
// registo for durável) E regista-o em memória. Fail-closed: um erro de persistência
// aborta (o run não seria crash-safe, pelo que não deve prosseguir como se fosse). Sem
// store configurado é equivalente a [Put]. Um frozen nil é no-op.
func (r *RunToolSets) Freeze(ctx context.Context, frozen *toolset.FrozenToolSet) error {
	if frozen == nil {
		return nil
	}
	if r.store != nil {
		if err := r.persist(ctx, frozen); err != nil {
			return fmt.Errorf("integration: persistir tool set congelado do run %q: %w", frozen.RunID(), err)
		}
	}
	r.Put(frozen)
	return nil
}

// persist serializa o snapshot ({run_id, frozen_at, entries}) e acrescenta-o ao stream
// do run como um evento imutável. O StepID fixo dá idempotência por (run_id, step_id).
func (r *RunToolSets) persist(ctx context.Context, frozen *toolset.FrozenToolSet) error {
	payload, err := json.Marshal(frozenSnapshotDTO{
		RunID:    frozen.RunID(),
		FrozenAt: frozen.FrozenAt(),
		Entries:  frozen.Entries(),
	})
	if err != nil {
		return err
	}
	_, err = r.store.Append(ctx, frozen.RunID(), eventstore.EventInput{
		Type:     eventTypeFrozenToolSet,
		Payload:  payload,
		RunID:    frozen.RunID(),
		StepID:   "toolset-freeze",
		Producer: eventstore.Producer{NHIID: toolSetProducerNHI},
	})
	return err
}

// Rebuild reconstrói o tool set congelado de um run a partir do Event Store (após um
// failover) e regista-o em memória. Devolve (true, nil) se reconstruído, (false, nil)
// se não há registo durável (store nil, stream inexistente ou sem snapshot), e um erro
// se a leitura/desserialização/reconstrução falhar. Se o run já está em memória,
// devolve (true, nil) sem reler. É idempotente.
//
// A reconstrução é BYTE-IDÊNTICA: refunde os MESMOS [domain.Entry] persistidos via
// [toolset.FreezeToolSet] com o relógio do instante original — mesma ordem, mesmos
// specs, mesmo hash (o hash deriva dos specs, não do relógio). A revalidação por
// chamada obtém a mesma expectativa que antes do restart.
func (r *RunToolSets) Rebuild(ctx context.Context, runID string) (bool, error) {
	if r.store == nil {
		return false, nil
	}
	if _, ok := r.Frozen(runID); ok {
		return true, nil // já em memória
	}
	events, err := r.store.Read(ctx, runID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return false, nil
		}
		return false, err
	}
	// Último snapshot de tool set do run (o freeze é único por run, mas lemos o último
	// por robustez).
	var dto *frozenSnapshotDTO
	for _, ev := range events {
		if ev.Type != eventTypeFrozenToolSet {
			continue
		}
		var d frozenSnapshotDTO
		if err := json.Unmarshal(ev.Payload, &d); err != nil {
			return false, fmt.Errorf("integration: snapshot de tool set do run %q ilegível: %w", runID, err)
		}
		dto = &d
	}
	if dto == nil {
		return false, nil
	}
	frozen, err := toolset.FreezeToolSet(ctx, replayCatalog{entries: dto.Entries}, dto.RunID, nil, toolset.WithClock(frozenClock(dto.FrozenAt)))
	if err != nil {
		return false, fmt.Errorf("integration: reconstruir tool set do run %q: %w", runID, err)
	}
	r.Put(frozen)
	return true, nil
}

// Frozen devolve o tool set congelado do run e um booleano de presença. Um run sem
// entrada devolve (nil, false) — a revalidação trata isso como default-deny.
func (r *RunToolSets) Frozen(runID string) (*toolset.FrozenToolSet, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.byRun[runID]
	return f, ok
}

// Release remove a entrada de um run terminado. É idempotente. Liberta a referência
// ao snapshot quando o run acaba (o composition root chama-o no defer de Run).
func (r *RunToolSets) Release(runID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byRun, runID)
}

// frozenSnapshotDTO é a forma serializada do snapshot durável (AOS-155). Os
// [domain.Entry] são serializáveis por JSON (campos públicos com tags) e bastam para
// refundir um [toolset.FrozenToolSet] idêntico.
type frozenSnapshotDTO struct {
	RunID    string         `json:"run_id"`
	FrozenAt string         `json:"frozen_at"`
	Entries  []domain.Entry `json:"entries"`
}

// replayCatalog é um [toolset.Catalog] que devolve um conjunto FIXO de entradas — as
// persistidas. Serve a reconstrução: [toolset.FreezeToolSet] sobre ele produz o MESMO
// snapshot (mesmas entradas ⇒ mesmos specs/hash), sem consultar o REG vivo (que teria
// evoluído — o drift que o freeze existe para congelar).
type replayCatalog struct{ entries []domain.Entry }

func (c replayCatalog) ActiveEntries(context.Context) ([]domain.Entry, error) {
	return c.entries, nil
}

// frozenClock devolve um relógio fixo no instante de congelamento persistido, para a
// reconstrução restaurar o mesmo FrozenAt. Um timestamp ilegível cai no zero-value
// (observacional; não afecta o hash nem a revalidação).
func frozenClock(frozenAt string) func() time.Time {
	if t, err := time.Parse(time.RFC3339Nano, frozenAt); err == nil {
		return func() time.Time { return t }
	}
	return func() time.Time { return time.Time{} }
}

// ApplyFrozenToGoal materializa o tool set congelado no [agentruntime.Goal] do run:
// fixa Goal.Tools na projecção pinada do snapshot ([toolset.FrozenToolSet.Specs],
// ordem estável congelada) — a MESMA lista que o prefixo imutável do prompt e o
// manifesto de dependências fixam a jusante (o loop constrói ambos a partir de
// Goal.Tools). O RunID do goal é alinhado com o do snapshot (consistência: a
// revalidação indexa o frozen por RunID). Devolve uma CÓPIA do goal — não muta o
// argumento.
//
// NOTA (tools vs skills): o snapshot funde tools+skills+servidores MCP numa única
// projecção na ordem congelada (id, version), pelo que Goal.Skills fica vazio e
// TODAS as dependências pinadas (versões+digests) vão a Goal.Tools. O que importa
// para a integridade de supply-chain — o pin exacto de cada dependência no prefixo
// e no manifesto — é preservado; a categorização tool/skill do manifesto (que
// [toolset.FrozenToolSet.ApplyToManifest] faria) é uma fidelidade menor que o loop
// base não distingue ao materializar Goal.Tools. Preferimos a estabilidade
// byte-a-byte do prefixo (ADR-009), que exige a ordem congelada ÚNICA.
func ApplyFrozenToGoal(goal agentruntime.Goal, frozen *toolset.FrozenToolSet) agentruntime.Goal {
	if frozen == nil {
		return goal
	}
	goal.RunID = frozen.RunID()
	goal.Tools = frozen.Specs()
	goal.Skills = nil
	return goal
}
