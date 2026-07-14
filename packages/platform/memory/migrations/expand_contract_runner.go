package migrations

import (
	"context"
	"reflect"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/schema"
)

// recordsEqual compara dois registos por valor (identidade exacta), usado pelo
// backstop de reversibilidade do Expand. Os corpos são value types (clone devolve
// cópia) e os metadados temporais são determinísticos, pelo que reflect.DeepEqual é
// uma igualdade estável e total sobre a forma do registo.
func recordsEqual(a, b domain.Record) bool { return reflect.DeepEqual(a, b) }

// Phase é uma fase do padrão expand → migrate → contract. A ordem é estrita: cada
// fase só é aplicável depois da anterior (o motor recusa fora de ordem).
type Phase string

const (
	// PhaseNone — estado inicial, antes de qualquer fase.
	PhaseNone Phase = "none"
	// PhaseExpand — o novo schema foi adicionado; escreve-se em AMBOS (dual-write) e
	// lê-se de AMBOS (dual-read). SEM downtime. A leitura canónica continua em From.
	PhaseExpand Phase = "expand"
	// PhaseMigrate — a leitura canónica passou para To. Ambas as formas ainda
	// coexistem (reversível).
	PhaseMigrate Phase = "migrate"
	// PhaseContract — a forma antiga (From) foi removida; só To subsiste. Reversível
	// via Down (recompõe a forma antiga a partir da nova).
	PhaseContract Phase = "contract"
)

// dualRecord guarda as DUAS representações de schema de um registo durante a
// migração. Antes de expand só existe old; durante expand/migrate coexistem; após
// contract só existe new. É esta coexistência que materializa o dual-write/read.
type dualRecord struct {
	old *domain.Record // representação na versão From (nil após contract)
	new *domain.Record // representação na versão To (nil antes de expand)
}

func (d dualRecord) clone() dualRecord {
	var cp dualRecord
	if d.old != nil {
		c := d.old.Clone()
		cp.old = &c
	}
	if d.new != nil {
		c := d.new.Clone()
		cp.new = &c
	}
	return cp
}

// Runner é o motor de migração expand/contract de UMA migração sobre um snapshot
// de registos de uma classe. Detém o estado corrente (fase + representações duais)
// e nunca o muta até uma fase concluir com sucesso — uma fase que falha a meio
// deixa o estado inalterado (rollback implícito, sem perda nem corrupção).
type Runner struct {
	mig      Migration
	gate     Gate
	registry *Registry
	classReg *schema.ClassRegistry
	tracer   agentruntime.Tracer

	phase Phase
	order []string               // ordem estável de ids
	recs  map[string]*dualRecord // id → representações duais
}

// Option configura o Runner.
type Option func(*Runner)

// WithGate injecta a porta de eval-gate. Sem gate, o default é fail-closed para
// MAJOR (denyMajorGate): a ausência de configuração nunca deixa passar uma quebra.
func WithGate(g Gate) Option { return func(r *Runner) { r.gate = g } }

// WithRegistry injecta o registo de migrações durável e idempotente.
func WithRegistry(reg *Registry) Option { return func(r *Runner) { r.registry = reg } }

// WithClassRegistry injecta o registo de versões de schema por classe; o motor
// avança a versão corrente da classe (para To) quando a nova forma se torna
// canónica (fase migrate).
func WithClassRegistry(cr *schema.ClassRegistry) Option {
	return func(r *Runner) { r.classReg = cr }
}

// WithTracer injecta a porta Tracer (default NoopTracer).
func WithTracer(t agentruntime.Tracer) Option { return func(r *Runner) { r.tracer = t } }

// Nomes de span e atributos (namespace aos.memory.migration.*).
const (
	opExpand   = "memory.migration.expand"
	opMigrate  = "memory.migration.migrate"
	opContract = "memory.migration.contract"

	attrMigrationID = "aos.memory.migration.id"
	attrPhase       = "aos.memory.migration.phase"
	attrClass       = "aos.memory.migration.class"
	attrKind        = "aos.memory.migration.kind"
	attrResult      = "aos.memory.migration.result"
)

// NewRunner constrói o motor a partir de uma migração e do snapshot inicial de
// registos (todos na versão From). Fail-closed: migração inválida é rejeitada; um
// registo inicial cuja schema_version não seja From é rejeitado
// (ErrRecordSchemaMismatch) para não migrar sobre pressupostos errados.
func NewRunner(mig Migration, initial []domain.Record, opts ...Option) (*Runner, error) {
	if err := mig.Validate(); err != nil {
		return nil, err
	}
	r := &Runner{
		mig:    mig,
		gate:   denyMajorGate{},
		tracer: agentruntime.NoopTracer{},
		phase:  PhaseNone,
		recs:   make(map[string]*dualRecord, len(initial)),
	}
	for _, o := range opts {
		o(r)
	}
	if r.gate == nil {
		r.gate = denyMajorGate{}
	}
	if r.tracer == nil {
		r.tracer = agentruntime.NoopTracer{}
	}
	fromStr := mig.From.String()
	for _, rec := range initial {
		if rec.Metadata.SchemaVersion != fromStr {
			return nil, ErrRecordSchemaMismatch
		}
		c := rec.Clone()
		r.recs[rec.ID] = &dualRecord{old: &c}
		r.order = append(r.order, rec.ID)
	}
	return r, nil
}

// Phase devolve a fase corrente.
func (r *Runner) Phase() Phase { return r.phase }

// applyUp aplica Up a um registo e verifica que o resultado está estampado em To
// (fail-closed). Puro; não muta o estado do runner.
func (r *Runner) applyUp(rec domain.Record) (domain.Record, error) {
	out, err := r.mig.Up(rec)
	if err != nil {
		return domain.Record{}, ErrTransformFailed
	}
	if out.Metadata.SchemaVersion != r.mig.To.String() {
		return domain.Record{}, ErrSchemaConsistency
	}
	return out, nil
}

// applyDown aplica Down a um registo e verifica que o resultado está estampado em
// From (fail-closed). Puro; não muta o estado do runner.
func (r *Runner) applyDown(rec domain.Record) (domain.Record, error) {
	out, err := r.mig.Down(rec)
	if err != nil {
		return domain.Record{}, ErrTransformFailed
	}
	if out.Metadata.SchemaVersion != r.mig.From.String() {
		return domain.Record{}, ErrSchemaConsistency
	}
	return out, nil
}

func (r *Runner) startSpan(ctx context.Context, op string) (context.Context, agentruntime.Span) {
	ctx, span := r.tracer.StartSpan(ctx, op)
	span.SetAttribute(attrMigrationID, r.mig.ID)
	span.SetAttribute(attrClass, r.mig.Class.String())
	span.SetAttribute(attrKind, r.mig.Kind().String())
	return ctx, span
}

// Expand aplica a fase EXPAND: computa a representação To de cada registo (Up),
// passando a haver dual-write/dual-read. A leitura canónica MANTÉM-SE em From (sem
// downtime — nenhum leitor vê a nova forma sem ser por escolha).
//
// Antes de qualquer escrita, submete a migração ao eval-gate: uma mudança MAJOR
// sem aprovação é RECUSADA (ErrMigrationDenied) e NADA é aplicado (fail-closed).
//
// É idempotente: chamar Expand com a fase já em expand-ou-além é um no-op. Se
// algum Up falhar, a fase é abortada e o estado mantém-se byte-idêntico ao inicial.
func (r *Runner) Expand(ctx context.Context) error {
	_, span := r.startSpan(ctx, opExpand)
	span.SetAttribute(attrPhase, string(PhaseExpand))
	defer span.End()

	if r.phase != PhaseNone {
		// Já expandido (ou além): no-op idempotente.
		span.SetAttribute(attrResult, "noop")
		return nil
	}

	// Gate ANTES de tocar no estado (fail-closed para MAJOR).
	dec := r.gate.Evaluate(ctx, GateRequest{
		MigrationID: r.mig.ID,
		Class:       r.mig.Class,
		From:        r.mig.From,
		To:          r.mig.To,
		Kind:        r.mig.Kind(),
	})
	if !dec.Allowed {
		span.SetAttribute(attrResult, "denied")
		return ErrMigrationDenied
	}

	// Computa a nova representação num mapa TEMPORÁRIO; só se comita se todos os
	// transforms passarem (rollback implícito em caso de falha).
	next := make(map[string]*dualRecord, len(r.recs))
	for _, id := range r.order {
		d := r.recs[id]
		up, err := r.applyUp(*d.old)
		if err != nil {
			span.SetAttribute(attrResult, "rollback")
			return err
		}
		// Backstop SEMÂNTICO (Finding gate-fail-open): o eval-gate classifica só pela
		// diferença numérica de versão, não pela semântica do transform. Aqui, por
		// registo, exige-se que a migração seja um INVERSO exacto — Down(Up(old)) tem
		// de reproduzir old byte-a-byte. Se não reproduzir, a migração perde/corrompe
		// dados (ex.: um Up que remove um campo rotulado MINOR) e é RECUSADA
		// fail-closed, antes de tocar no estado, mesmo que o gate a tenha deixado passar.
		back, err := r.applyDown(up)
		if err != nil {
			span.SetAttribute(attrResult, "rollback")
			return err
		}
		if !recordsEqual(*d.old, back) {
			span.SetAttribute(attrResult, "irreversible")
			return ErrIrreversibleMigration
		}
		nd := d.clone()
		nd.new = &up
		next[id] = &nd
	}

	if r.registry != nil {
		if _, err := r.registry.Record(ctx, r.mig, PhaseExpand); err != nil {
			span.SetAttribute(attrResult, "error")
			return err
		}
	}

	r.recs = next
	r.phase = PhaseExpand
	span.SetAttribute(attrResult, "committed")
	return nil
}

// Migrate aplica a fase MIGRATE: a leitura canónica passa de From para To. Ambas
// as formas ainda coexistem (reversível). Requer fase expand (fail-closed:
// ErrPhaseOrder se ainda em none). Idempotente se já em migrate/contract.
//
// Ao migrar, avança a versão de schema CORRENTE da classe (ClassRegistry) para To
// — é aqui que a nova forma se torna a canónica da classe.
func (r *Runner) Migrate(ctx context.Context) error {
	_, span := r.startSpan(ctx, opMigrate)
	span.SetAttribute(attrPhase, string(PhaseMigrate))
	defer span.End()

	switch r.phase {
	case PhaseMigrate, PhaseContract:
		span.SetAttribute(attrResult, "noop")
		return nil
	case PhaseNone:
		span.SetAttribute(attrResult, "phase_order")
		return ErrPhaseOrder
	}

	if r.classReg != nil {
		if err := r.classReg.Register(r.mig.Class, r.mig.To); err != nil {
			span.SetAttribute(attrResult, "error")
			return err
		}
	}
	if r.registry != nil {
		if _, err := r.registry.Record(ctx, r.mig, PhaseMigrate); err != nil {
			span.SetAttribute(attrResult, "error")
			return err
		}
	}

	r.phase = PhaseMigrate
	span.SetAttribute(attrResult, "committed")
	return nil
}

// Contract aplica a fase CONTRACT: remove a representação antiga (From) de cada
// registo; só To subsiste. Requer fase migrate (fail-closed: ErrPhaseOrder).
// Idempotente se já em contract. É reversível via RevertContract (Down recompõe a
// forma antiga).
func (r *Runner) Contract(ctx context.Context) error {
	_, span := r.startSpan(ctx, opContract)
	span.SetAttribute(attrPhase, string(PhaseContract))
	defer span.End()

	switch r.phase {
	case PhaseContract:
		span.SetAttribute(attrResult, "noop")
		return nil
	case PhaseNone, PhaseExpand:
		span.SetAttribute(attrResult, "phase_order")
		return ErrPhaseOrder
	}

	if r.registry != nil {
		if _, err := r.registry.Record(ctx, r.mig, PhaseContract); err != nil {
			span.SetAttribute(attrResult, "error")
			return err
		}
	}

	next := make(map[string]*dualRecord, len(r.recs))
	for _, id := range r.order {
		nd := r.recs[id].clone()
		nd.old = nil // remove a forma antiga
		next[id] = &nd
	}
	r.recs = next
	r.phase = PhaseContract
	span.SetAttribute(attrResult, "committed")
	return nil
}

// Run executa a sequência completa expand → migrate → contract de forma
// TRANSACIONAL: se qualquer fase falhar, faz rollback ao estado INICIAL (via
// Reset) e devolve o erro — nenhuma migração parcial fica visível. É a forma
// canónica de aplicar uma migração de ponta a ponta.
func (r *Runner) Run(ctx context.Context) error {
	snapshot := r.snapshot()
	if err := r.Expand(ctx); err != nil {
		// Expand falha ANTES de qualquer commit durável (transform ou o próprio
		// Record); nada a compensar no log.
		r.restore(snapshot)
		return err
	}
	if err := r.Migrate(ctx); err != nil {
		// Expand foi duravelmente registado; compensa-o para o log reflectir a reversão.
		r.restore(snapshot)
		r.compensate(ctx, PhaseExpand)
		return err
	}
	if err := r.Contract(ctx); err != nil {
		// Expand e migrate foram duravelmente registados; compensa ambos.
		r.restore(snapshot)
		r.compensate(ctx, PhaseExpand, PhaseMigrate)
		return err
	}
	return nil
}

// compensate grava eventos compensatórios (best-effort) para as fases que ficaram
// no log durável mas foram revertidas pelo rollback de Run, mantendo a linhagem
// coerente com o estado efectivo. Sem registo ligado é um no-op. Erros de
// compensação não mascaram o erro original de Run (o log é reconciliável por replay).
func (r *Runner) compensate(ctx context.Context, phases ...Phase) {
	if r.registry == nil {
		return
	}
	for _, p := range phases {
		_, _ = r.registry.RecordRevert(ctx, r.mig, p)
	}
}
