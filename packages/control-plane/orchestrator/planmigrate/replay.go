package planmigrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	pe "github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// Erros de reconstrução/binding — fail-closed. Uma captura incompleta ou um reader
// que não corresponde à captura NÃO produzem um replay "por omissão": abortam.
var (
	// ErrNotApproved — a captura não contém `plan.approved`: não há plano congelado
	// para materializar nem reproduzir.
	ErrNotApproved = errors.New("planmigrate: captura sem plan.approved — nada a materializar/reproduzir")
	// ErrCaptureIncomplete — não há `plan.proposed` cujo hash coincida com o hash
	// aprovado: prompt_version/capabilities_hash não são recuperáveis da captura.
	ErrCaptureIncomplete = errors.New("planmigrate: captura sem plan.proposed correspondente ao hash aprovado")
	// ErrNotMaterialized — não há `plan.materialized` para o hash aprovado: o run não
	// chegou a materializar, logo não há sequência a reproduzir.
	ErrNotMaterialized = errors.New("planmigrate: captura sem plan.materialized correspondente ao hash aprovado")
	// ErrReaderMismatch — o READER (o PlanDocument dado) não corresponde à CAPTURA: o
	// hash do documento difere do hash aprovado, ou o planner_meta diverge. O run só
	// é replayável se reader E captura concordarem (DoD).
	ErrReaderMismatch = errors.New("planmigrate: reader nao corresponde a captura (hash/planner_meta divergente)")
	// ErrMissingDeps — [Migrator.Materialize] exige REG, RM e Recorder configurados.
	ErrMissingDeps = errors.New("planmigrate: Materialize exige CapabilityResolver, ReferenceMonitor e Recorder")
)

// CapabilityResolver é o REG: resolve uma [plan.ToolRef] PINADA contra o snapshot de
// capabilities. A via de ESCRITA ([Migrator.Materialize]) chama-o para confirmar que
// cada tool do plano existe/está na versão pinada. O REPLAY NUNCA o chama — a
// resolução já está capturada na materialização gravada. É essa a assimetria que os
// testes provam por contadores.
type CapabilityResolver interface {
	Resolve(ctx context.Context, ref plan.ToolRef) error
}

// ReferenceMonitor é o RM: media/autoriza a materialização de um nó (travessia do
// monitor de referência). A via de ESCRITA chama-o por nó; o REPLAY NUNCA o
// atravessa — a decisão de mediação já está no log.
type ReferenceMonitor interface {
	Mediate(ctx context.Context, nodeID string, tools []plan.ToolRef) error
}

// Recorder é a superfície MÍNIMA de [plannerevents.Recorder] de que
// [Migrator.Materialize] depende para PERSISTIR `plan.materialized`. *pe.Recorder
// satisfá-la. Reutiliza o domínio `aos.planner.v1` — não declara tipos novos.
type Recorder interface {
	RecordMaterialized(ctx context.Context, p pe.MaterializedPayload) (uint64, error)
}

// Migrator persiste/materializa o plano aprovado e reproduz o run de forma
// determinística. Detém o REG, o RM e o Recorder porque a via de ESCRITA os usa; a
// via de REPLAY é a garantia negativa — não lhes toca. Construir com [NewMigrator].
type Migrator struct {
	policy Policy
	reg    CapabilityResolver
	rm     ReferenceMonitor
	rec    Recorder
}

// MigratorOption configura o [Migrator].
type MigratorOption func(*Migrator)

// WithResolver injecta o REG (usado só na via de escrita).
func WithResolver(reg CapabilityResolver) MigratorOption {
	return func(m *Migrator) { m.reg = reg }
}

// WithReferenceMonitor injecta o RM (usado só na via de escrita).
func WithReferenceMonitor(rm ReferenceMonitor) MigratorOption {
	return func(m *Migrator) { m.rm = rm }
}

// WithRecorder injecta o gravador de `plan.materialized` (usado só na via de escrita).
func WithRecorder(rec Recorder) MigratorOption {
	return func(m *Migrator) { m.rec = rec }
}

// NewMigrator constrói um Migrator sobre a Policy de admissão. O REG/RM/Recorder são
// opcionais: o REPLAY não os requer (por design), a MATERIALIZAÇÃO exige-os.
func NewMigrator(policy Policy, opts ...MigratorOption) *Migrator {
	m := &Migrator{policy: policy}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Materialize é a via de ESCRITA: transforma um plano APROVADO e congelado em
// `plan.materialized`, resolvendo cada tool no REG e mediando cada nó no RM, e
// persiste o facto. É AQUI — e só aqui — que o REG e o RM são atravessados.
//
// Gate fail-closed ANTES de qualquer efeito: [Policy.Admit] sobre o `plan_version`
// congelado. Se a versão foi RETIRADA ou está FORA da janela, materializar é recusado
// ([ErrRetired]/[ErrOutsideSupportWindow]) — o run volta a planeamento sem tocar no
// REG/RM. É a materialização da regra "retirada antes da materialização ⇒ invalida".
func (m *Migrator) Materialize(ctx context.Context, planID string, doc plan.PlanDocument) (pe.MaterializedPayload, error) {
	if m.reg == nil || m.rm == nil || m.rec == nil {
		return pe.MaterializedPayload{}, ErrMissingDeps
	}
	// Gate soberano primeiro: nada de REG/RM se a versão não for admissível.
	if err := m.policy.Admit(doc.PlanVersion); err != nil {
		return pe.MaterializedPayload{}, err
	}
	hash, err := HashPlan(doc)
	if err != nil {
		return pe.MaterializedPayload{}, err
	}
	nodes := make([]pe.MaterializedNode, 0, len(doc.Nodes))
	for _, n := range doc.Nodes {
		// REG: cada tool pinada tem de resolver contra o snapshot de capabilities.
		for _, t := range n.Tools {
			if err := m.reg.Resolve(ctx, t); err != nil {
				return pe.MaterializedPayload{}, fmt.Errorf("planmigrate: REG rejeitou tool %q do no %q: %w", t.Name, n.NodeID, err)
			}
		}
		// RM: a materialização do nó é mediada (autorização da NHI filha).
		if err := m.rm.Mediate(ctx, n.NodeID, n.Tools); err != nil {
			return pe.MaterializedPayload{}, fmt.Errorf("planmigrate: RM negou o no %q: %w", n.NodeID, err)
		}
		tools := make([]string, 0, len(n.Tools))
		for _, t := range n.Tools {
			tools = append(tools, t.Name)
		}
		nodes = append(nodes, pe.MaterializedNode{NodeID: n.NodeID, Kind: pe.SpawnLeaf, Tools: tools})
	}
	payload := pe.MaterializedPayload{PlanID: planID, PlanHash: hash, Nodes: nodes}
	if _, err := m.rec.RecordMaterialized(ctx, payload); err != nil {
		return pe.MaterializedPayload{}, err
	}
	return payload, nil
}

// Replay é o resultado de uma reprodução determinística: o manifesto pinado, a
// sequência de factos reconstruída (byte-a-byte, ordem de append) e a materialização
// CAPTURADA. Nada aqui foi re-derivado — tudo veio do log.
type Replay struct {
	Manifest     Manifest
	Events       []pe.PlanEvent
	Materialized pe.MaterializedPayload
}

// Replay reproduz um run APROVADO de forma determinística. Reconstrói a captura via
// [plannerevents.Reconstruct] (read-only por construção: recebe só um EventReader,
// não há Append nem LLM) e devolve a materialização gravada. NÃO consulta o REG nem
// atravessa o RM que o Migrator detém — as decisões já estão no log; os contadores a
// zero nos testes provam-no.
//
// Passos, todos fail-closed:
//
//  1. Reconstruir o stream (nenhuma re-chamada ao modelo).
//  2. Localizar `plan.approved` (o hash congelado) — senão [ErrNotApproved].
//  3. Recuperar prompt_version/capabilities_hash do `plan.proposed` cujo hash coincide
//     — senão [ErrCaptureIncomplete].
//  4. Recuperar `plan.materialized` do mesmo hash — senão [ErrNotMaterialized].
//  5. LIGAR o reader à captura: HashPlan(doc) == hash aprovado E planner_meta igual —
//     senão [ErrReaderMismatch]. (O reader é o eixo plan_version; a captura são os
//     eixos prompt/caps. O run só é replayável se os dois concordarem.)
//  6. ADMITIR o `plan_version` congelado ([Policy.Admit]) — [ErrRetired] se retirada,
//     [ErrOutsideSupportWindow] se fora da janela.
//
// O manifesto devolvido reflecte a versão do READER (congelada), NUNCA
// [plan.CurrentPlanVersion]: não há auto-migração.
func (m *Migrator) Replay(ctx context.Context, reader pe.EventReader, planID string, doc plan.PlanDocument) (*Replay, error) {
	events, err := pe.Reconstruct(ctx, reader, planID)
	if err != nil {
		return nil, err
	}

	approvedHash, ok := lastApprovedHash(events)
	if !ok {
		return nil, ErrNotApproved
	}
	meta, ok := proposedMetaForHash(events, approvedHash)
	if !ok {
		return nil, ErrCaptureIncomplete
	}
	mat, ok := materializedForHash(events, approvedHash)
	if !ok {
		return nil, ErrNotMaterialized
	}

	// (5) Binding reader ↔ captura.
	docHash, err := HashPlan(doc)
	if err != nil {
		return nil, err
	}
	if docHash != approvedHash {
		return nil, fmt.Errorf("%w: hash do reader %s != hash aprovado %s", ErrReaderMismatch, docHash, approvedHash)
	}
	if doc.PlannerMeta.Model != meta.Model ||
		doc.PlannerMeta.PromptVersion != meta.PromptVersion ||
		doc.PlannerMeta.CapabilitiesHash != meta.CapabilitiesHash {
		return nil, fmt.Errorf("%w: planner_meta do reader difere do capturado", ErrReaderMismatch)
	}

	manifest := Manifest{
		PlanID:           planID,
		PlanHash:         approvedHash,
		PlanVersion:      doc.PlanVersion, // congelado: do reader, nunca de CurrentPlanVersion
		PromptVersion:    meta.PromptVersion,
		CapabilitiesHash: meta.CapabilitiesHash,
	}

	// (6) Gate soberano sobre a versão congelada — fail-closed.
	if err := m.policy.Admit(manifest.PlanVersion); err != nil {
		return nil, err
	}

	return &Replay{Manifest: manifest, Events: events, Materialized: mat}, nil
}

// lastApprovedHash devolve o hash do ÚLTIMO `plan.approved` do stream (a aprovação
// efectiva, caso tenha havido edição→revalidação→nova decisão). Ignora eventos de
// decisão que não sejam aprovação. false se não houver aprovação.
func lastApprovedHash(events []pe.PlanEvent) (string, bool) {
	hash := ""
	found := false
	for _, e := range events {
		if e.Type != pe.EventApproved {
			continue
		}
		var d pe.DecisionPayload
		if err := json.Unmarshal(e.Payload, &d); err != nil {
			continue // payload corrompido: ignora este, fail-closed a jusante se nenhum válido
		}
		if d.Decision != pe.DecisionApproved {
			continue
		}
		hash = d.PlanHash
		found = true
	}
	return hash, found
}

// proposedMetaForHash devolve o [plannerevents.PlannerMeta] do `plan.proposed` cujo
// PlanHash coincide com hash — a proveniência (prompt_version, capabilities_hash) da
// proposta que veio a ser aprovada. Percorre do fim para o início para preferir a
// captura mais recente desse hash. false se nenhum coincidir.
func proposedMetaForHash(events []pe.PlanEvent, hash string) (pe.PlannerMeta, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Type != pe.EventProposed {
			continue
		}
		var p pe.ProposedPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			continue
		}
		if p.PlanHash == hash {
			return p.Meta, true
		}
	}
	return pe.PlannerMeta{}, false
}

// materializedForHash devolve o `plan.materialized` cujo PlanHash coincide com hash.
// Percorre do fim para o início. false se nenhum coincidir.
func materializedForHash(events []pe.PlanEvent, hash string) (pe.MaterializedPayload, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Type != pe.EventMaterialized {
			continue
		}
		var p pe.MaterializedPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			continue
		}
		if p.PlanHash == hash {
			return p, true
		}
	}
	return pe.MaterializedPayload{}, false
}
