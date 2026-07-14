package migrations

import (
	"context"

	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/schema"
)

// currentClassVersion devolve a versão de schema corrente da classe da migração no
// ClassRegistry ligado (se houver). O segundo retorno é false quando não há
// ClassRegistry ou a classe ainda não tem versão registada.
func (r *Runner) currentClassVersion() (schema.Version, bool) {
	if r.classReg == nil {
		return schema.Version{}, false
	}
	return r.classReg.Current(r.mig.Class)
}

// canonicalRepr devolve a representação canónica de um registo para a fase
// corrente: From (old) até migrate, To (new) a partir de migrate. É a forma que a
// leitura por omissão serve — o "switch de leitura" do padrão parallel-change.
func (d dualRecord) canonicalRepr(phase Phase) *domain.Record {
	switch phase {
	case PhaseMigrate, PhaseContract:
		return d.new
	default:
		return d.old
	}
}

// ReadCanonical devolve o registo (id) na sua forma CANÓNICA para a fase corrente.
// É por aqui que se observa o "sem downtime": em expand a leitura ainda serve a
// forma antiga; só após migrate serve a nova.
func (r *Runner) ReadCanonical(id string) (domain.Record, error) {
	d, ok := r.recs[id]
	if !ok {
		return domain.Record{}, ErrUnknownRecord
	}
	rec := d.canonicalRepr(r.phase)
	if rec == nil {
		return domain.Record{}, ErrUnknownRecord
	}
	return rec.Clone(), nil
}

// Read implementa a leitura DUAL: devolve o registo na versão PREFERIDA se essa
// representação existir, degradando graciosamente para a versão disponível caso
// contrário. É a prova de que, durante expand, um registo é legível em AMBOS os
// schemas (dual-read).
func (r *Runner) Read(id string, preferred schema.Version) (domain.Record, error) {
	d, ok := r.recs[id]
	if !ok {
		return domain.Record{}, ErrUnknownRecord
	}
	switch {
	case preferred.Equal(r.mig.To) && d.new != nil:
		return d.new.Clone(), nil
	case preferred.Equal(r.mig.From) && d.old != nil:
		return d.old.Clone(), nil
	}
	// Degrada graciosamente para a representação que existir (nova primeiro,
	// canónica-recente).
	if d.new != nil {
		return d.new.Clone(), nil
	}
	if d.old != nil {
		return d.old.Clone(), nil
	}
	return domain.Record{}, ErrUnknownRecord
}

// HasBoth indica se o registo tem AMBAS as representações vivas (dual-write/read
// activo). Verdadeiro exactamente entre expand e contract.
func (r *Runner) HasBoth(id string) bool {
	d, ok := r.recs[id]
	return ok && d.old != nil && d.new != nil
}

// IDs devolve os ids do snapshot por ordem estável de inserção.
func (r *Runner) IDs() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Count devolve o número de registos no snapshot.
func (r *Runner) Count() int { return len(r.order) }

// CanonicalRecords devolve todos os registos na forma canónica corrente, por ordem
// estável. Base das asserções de "sem perda de dados" no round-trip.
func (r *Runner) CanonicalRecords() []domain.Record {
	out := make([]domain.Record, 0, len(r.order))
	for _, id := range r.order {
		d := r.recs[id]
		if rec := d.canonicalRepr(r.phase); rec != nil {
			out = append(out, rec.Clone())
		}
	}
	return out
}

// Put escreve um registo respeitando a coexistência de schemas da fase corrente:
// em expand/migrate escreve em AMBAS as formas (DUAL-WRITE) derivando a
// contrapartida por Up/Down; em none escreve só From; em contract só To. O registo
// de entrada pode vir em From OU em To — a outra forma é derivada deterministicamente.
// Fail-closed: uma versão de schema estranha a From/To é rejeitada.
func (r *Runner) Put(_ context.Context, rec domain.Record) error {
	fromStr := r.mig.From.String()
	toStr := r.mig.To.String()

	var oldRec, newRec *domain.Record
	switch rec.Metadata.SchemaVersion {
	case fromStr:
		o := rec.Clone()
		up, err := r.applyUp(o)
		if err != nil {
			return err
		}
		oldRec, newRec = &o, &up
	case toStr:
		n := rec.Clone()
		down, err := r.applyDown(n)
		if err != nil {
			return err
		}
		oldRec, newRec = &down, &n
	default:
		return ErrRecordSchemaMismatch
	}

	d := &dualRecord{}
	switch r.phase {
	case PhaseNone:
		d.old = oldRec
	case PhaseExpand, PhaseMigrate:
		d.old, d.new = oldRec, newRec // dual-write
	case PhaseContract:
		d.new = newRec
	}

	if _, ok := r.recs[rec.ID]; !ok {
		r.order = append(r.order, rec.ID)
	}
	r.recs[rec.ID] = d
	return nil
}

// ---------------------------------------------------------------------------
// Reverts — cada fase é reversível de forma INDEPENDENTE (AOS-041). Um revert
// restaura EXACTAMENTE o estado anterior à fase, sem perda nem corrupção.
// ---------------------------------------------------------------------------

// RevertContract desfaz a fase contract: recompõe a representação antiga (From) de
// cada registo a partir da nova (To), via Down. Prova a reversibilidade do
// contract. Requer fase contract (fail-closed: ErrPhaseOrder).
func (r *Runner) RevertContract(_ context.Context) error {
	if r.phase != PhaseContract {
		return ErrPhaseOrder
	}
	next := make(map[string]*dualRecord, len(r.recs))
	for _, id := range r.order {
		d := r.recs[id]
		down, err := r.applyDown(*d.new)
		if err != nil {
			return err // estado inalterado: rollback do revert
		}
		nd := d.clone()
		nd.old = &down
		next[id] = &nd
	}
	r.recs = next
	r.phase = PhaseMigrate
	return nil
}

// RevertMigrate desfaz a fase migrate: a leitura canónica volta a From. Ambas as
// formas continuam a coexistir. Requer fase migrate.
//
// Reverte TAMBÉM a versão de schema corrente da classe (se um ClassRegistry estiver
// ligado e a corrente for exactamente o To que ESTA migração fixou) de volta para
// From, via a operação controlada ClassRegistry.Revert — mantendo a autoridade de
// versão coerente com a fase. Se a corrente já não for o To desta migração (outra
// migração avançou-a entretanto), a versão da classe é deixada intacta (não seria
// correcto regredi-la). Fail-closed: uma reversão de versão que o registo recuse
// aborta o RevertMigrate sem mexer na fase.
func (r *Runner) RevertMigrate(_ context.Context) error {
	if r.phase != PhaseMigrate {
		return ErrPhaseOrder
	}
	if cur, ok := r.currentClassVersion(); ok && cur.Equal(r.mig.To) {
		if err := r.classReg.Revert(r.mig.Class, r.mig.From, cur); err != nil {
			return err
		}
	}
	r.phase = PhaseExpand
	return nil
}

// RevertExpand desfaz a fase expand: remove a representação nova (To) de cada
// registo, voltando ao estado inicial (só From). Requer fase expand.
func (r *Runner) RevertExpand(_ context.Context) error {
	if r.phase != PhaseExpand {
		return ErrPhaseOrder
	}
	next := make(map[string]*dualRecord, len(r.recs))
	for _, id := range r.order {
		nd := r.recs[id].clone()
		nd.new = nil
		next[id] = &nd
	}
	r.recs = next
	r.phase = PhaseNone
	return nil
}

// ---------------------------------------------------------------------------
// Snapshot/restore — suporte ao rollback transacional de Run.
// ---------------------------------------------------------------------------

type runnerSnapshot struct {
	phase Phase
	order []string
	recs  map[string]*dualRecord

	// classVer/classSet capturam a versão de schema corrente da classe no
	// ClassRegistry no momento do snapshot. São o que permite ao rollback
	// transacional de Run repor a versão da classe caso uma fase POSTERIOR ao
	// avanço (Migrate) falhe — sem isto, um Run revertido deixaria a versão da
	// classe em To enquanto o estado in-memory volta a From (estado híbrido).
	classVer schema.Version
	classSet bool
}

func (r *Runner) snapshot() runnerSnapshot {
	order := make([]string, len(r.order))
	copy(order, r.order)
	recs := make(map[string]*dualRecord, len(r.recs))
	for id, d := range r.recs {
		nd := d.clone()
		recs[id] = &nd
	}
	s := runnerSnapshot{phase: r.phase, order: order, recs: recs}
	if v, ok := r.currentClassVersion(); ok {
		s.classVer, s.classSet = v, true
	}
	return s
}

func (r *Runner) restore(s runnerSnapshot) {
	r.phase = s.phase
	r.order = s.order
	r.recs = s.recs
	// Repõe a versão de schema da classe se Migrate a avançou entretanto. A
	// reversão é guardada (compare-and-swap): só regride se a corrente for a que
	// observamos agora e não coincidir já com a do snapshot. Best-effort — um
	// registo que recuse (estado divergiu) não deve mascarar o erro original de Run.
	if s.classSet {
		if cur, ok := r.currentClassVersion(); ok && !cur.Equal(s.classVer) {
			_ = r.classReg.Revert(r.mig.Class, s.classVer, cur)
		}
	}
}
