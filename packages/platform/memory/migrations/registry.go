package migrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/substrate/eventstore"
)

// EventType do registo de migrações gravado no Event Store.
const migrationAppliedEventType = "memory.migration.applied"

// migrationRevertedEventType é o evento COMPENSATÓRIO de uma fase revertida. Como o
// stream é append-only e nunca se reescreve, uma reversão é um novo evento (não
// apaga o de aplicação): a linhagem mantém-se honesta e a fase EFECTIVA é a última
// aplicada que não tenha sido posteriormente compensada (ver EffectivePhase).
const migrationRevertedEventType = "memory.migration.reverted"

// migrationStream é o stream append-only onde vive o registo de migrações. Um só
// stream dá uma linhagem ordenada e auditável de todas as fases aplicadas.
const migrationStream = "memory.migrations"

// migrationRunID é o namespace de idempotência das migrações no Event Store. A
// idempotency_key efectiva é migrationRunID + ":" + <migration_id>:<phase>, pelo
// que reaplicar a MESMA fase da MESMA migração deduplica no store (no-op).
const migrationRunID = "mem-migration"

// EventAppender é o subconjunto do Event Store de que o registo depende. Mantê-lo
// mínimo desacopla o registo da superfície completa do store (*eventstore.Store
// satisfá-lo). É o MESMO substrato de idempotência do adaptador de memória.
type EventAppender interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error)
}

// AppliedRecord é uma fase de migração registada de forma durável. É o que se
// reconstrói do log para provar a idempotência (reaplicar = no-op) e para auditar
// a linhagem de evolução de schema.
type AppliedRecord struct {
	MigrationID string             `json:"migration_id"`
	Class       domain.MemoryClass `json:"class"`
	Phase       Phase              `json:"phase"`
	From        string             `json:"from"`
	To          string             `json:"to"`
	Kind        string             `json:"kind"`
}

// Registry é o registo de migrações versionado e IDEMPOTENTE, durável no Event
// Store append-only. Cada (migração, fase) é gravada uma única vez: a segunda
// tentativa deduplica pela idempotency_key do store e devolve applied=false — o
// no-op observável que o critério de aceitação exige. Reconstrói-se por replay,
// nunca mantém estado autoritativo em RAM.
type Registry struct {
	store EventAppender
}

// NewRegistry constrói o registo sobre um Event Store.
func NewRegistry(store EventAppender) *Registry {
	return &Registry{store: store}
}

// stepID deriva a chave de passo (componente de step da idempotency_key) para uma
// fase de uma migração. Estável e determinística.
func stepID(migrationID string, phase Phase) string {
	return migrationID + ":" + string(phase)
}

// revertStepID deriva a chave de passo do evento COMPENSATÓRIO de uma fase. Distinta
// da de aplicação (sufixo :revert) para que aplicar e compensar a mesma fase sejam
// entradas independentes no store idempotente.
func revertStepID(migrationID string, phase Phase) string {
	return migrationID + ":" + string(phase) + ":revert"
}

// Record grava a aplicação de uma fase de migração. Devolve applied=true se foi
// gravada agora (nova) ou applied=false se JÁ estava registada (duplicado — no-op).
// É esta distinção que o motor usa para não reaplicar uma fase já concluída.
func (r *Registry) Record(ctx context.Context, m Migration, phase Phase) (bool, error) {
	if r == nil || r.store == nil {
		return false, nil
	}
	rec := AppliedRecord{
		MigrationID: m.ID,
		Class:       m.Class,
		Phase:       phase,
		From:        m.From.String(),
		To:          m.To.String(),
		Kind:        m.Kind().String(),
	}
	// Guarda de identidade (Finding idempotency-key): a step key é f(ID, phase) e
	// NÃO inclui From/To. Como o AppliedRecord guarda From/To/Kind mas estes não
	// entram na chave, reutilizar o MESMO ID/fase com uma definição DIFERENTE
	// deduplicaria silenciosamente contra o primeiro registo, deixando o log a
	// descrever a migração ERRADA. Antes de gravar, confirma-se que qualquer registo
	// durável já existente para este (ID, fase) tem exactamente o mesmo From/To/Kind;
	// caso contrário é uma redefinição in-place e rejeita-se fail-closed.
	if prior, err := r.findRecord(ctx, m.ID, phase); err != nil {
		return false, err
	} else if prior != nil && (prior.From != rec.From || prior.To != rec.To || prior.Kind != rec.Kind) {
		return false, ErrMigrationRedefined
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		return false, fmt.Errorf("migrations: marshal do registo: %w", err)
	}
	res, err := r.store.Append(ctx, migrationStream, eventstore.EventInput{
		Type:    migrationAppliedEventType,
		Payload: payload,
		RunID:   migrationRunID,
		StepID:  stepID(m.ID, phase),
	})
	if err != nil {
		return false, err
	}
	return res.Status == eventstore.StatusCommitted, nil
}

// IsApplied indica se uma fase de uma migração já está registada de forma durável.
// Reconstrói do log (append-only); um stream ainda inexistente significa "nenhuma
// migração aplicada".
func (r *Registry) IsApplied(ctx context.Context, migrationID string, phase Phase) (bool, error) {
	if r == nil || r.store == nil {
		return false, nil
	}
	events, err := r.readAll(ctx)
	if err != nil {
		return false, err
	}
	want := stepID(migrationID, phase)
	for _, ev := range events {
		if ev.StepID == want {
			return true, nil
		}
	}
	return false, nil
}

// List reconstrói a linhagem completa de fases de migração aplicadas, por ordem de
// escrita (seq). Prova de que a evolução de schema é observável e auditável.
func (r *Registry) List(ctx context.Context) ([]AppliedRecord, error) {
	if r == nil || r.store == nil {
		return nil, nil
	}
	events, err := r.readAll(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AppliedRecord, 0, len(events))
	for _, ev := range events {
		if ev.Type != migrationAppliedEventType {
			continue
		}
		var rec AppliedRecord
		if uerr := json.Unmarshal(ev.Payload, &rec); uerr != nil {
			return nil, fmt.Errorf("migrations: unmarshal do registo (seq %d): %w", ev.Seq, uerr)
		}
		out = append(out, rec)
	}
	return out, nil
}

func (r *Registry) readAll(ctx context.Context) ([]eventstore.Event, error) {
	events, err := r.store.Read(ctx, migrationStream, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return events, nil
}

// findRecord devolve o AppliedRecord da aplicação (não-compensada) de uma fase de
// uma migração, ou nil se não houver. Serve a guarda de identidade de Record.
func (r *Registry) findRecord(ctx context.Context, migrationID string, phase Phase) (*AppliedRecord, error) {
	if r == nil || r.store == nil {
		return nil, nil
	}
	events, err := r.readAll(ctx)
	if err != nil {
		return nil, err
	}
	want := stepID(migrationID, phase)
	for _, ev := range events {
		if ev.Type != migrationAppliedEventType || ev.StepID != want {
			continue
		}
		var rec AppliedRecord
		if uerr := json.Unmarshal(ev.Payload, &rec); uerr != nil {
			return nil, fmt.Errorf("migrations: unmarshal do registo (seq %d): %w", ev.Seq, uerr)
		}
		return &rec, nil
	}
	return nil, nil
}

// RecordRevert grava o evento COMPENSATÓRIO de uma fase revertida no mesmo stream
// append-only. Devolve applied=true se foi gravado agora, applied=false se já
// existia (idempotente). Mantém o log durável coerente com o estado EFECTIVO após um
// rollback: sem isto, um Run revertido deixaria as fases expand/migrate no log a
// enganar a auditoria.
func (r *Registry) RecordRevert(ctx context.Context, m Migration, phase Phase) (bool, error) {
	if r == nil || r.store == nil {
		return false, nil
	}
	rec := AppliedRecord{
		MigrationID: m.ID,
		Class:       m.Class,
		Phase:       phase,
		From:        m.From.String(),
		To:          m.To.String(),
		Kind:        m.Kind().String(),
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		return false, fmt.Errorf("migrations: marshal da compensacao: %w", err)
	}
	res, err := r.store.Append(ctx, migrationStream, eventstore.EventInput{
		Type:    migrationRevertedEventType,
		Payload: payload,
		RunID:   migrationRunID,
		StepID:  revertStepID(m.ID, phase),
	})
	if err != nil {
		return false, err
	}
	return res.Status == eventstore.StatusCommitted, nil
}

// EffectivePhase reconstrói, a partir da linhagem durável, a fase EFECTIVA de uma
// migração: a fase mais avançada (expand < migrate < contract) que foi aplicada e
// NÃO posteriormente compensada. É a forma de o motor consultar o log (idempotência
// entre processos / retoma) em vez de arrancar sempre em PhaseNone às cegas. Um
// stream inexistente devolve PhaseNone.
func (r *Registry) EffectivePhase(ctx context.Context, migrationID string) (Phase, error) {
	if r == nil || r.store == nil {
		return PhaseNone, nil
	}
	events, err := r.readAll(ctx)
	if err != nil {
		return PhaseNone, err
	}
	active := map[Phase]bool{}
	for _, ev := range events {
		applied := ev.Type == migrationAppliedEventType
		reverted := ev.Type == migrationRevertedEventType
		if !applied && !reverted {
			continue
		}
		var rec AppliedRecord
		if uerr := json.Unmarshal(ev.Payload, &rec); uerr != nil {
			return PhaseNone, fmt.Errorf("migrations: unmarshal do registo (seq %d): %w", ev.Seq, uerr)
		}
		if rec.MigrationID != migrationID {
			continue
		}
		active[rec.Phase] = applied // aplicação activa a fase; compensação desactiva-a
	}
	eff := PhaseNone
	for _, p := range []Phase{PhaseExpand, PhaseMigrate, PhaseContract} {
		if active[p] {
			eff = p
		}
	}
	return eff, nil
}
