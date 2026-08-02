package plannerevents_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	pe "github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/substrate/eventstore"
)

// countingStore embrulha um *eventstore.Store e conta Append vs Read. Prova que a
// reconstrução é READ-ONLY: durante [pe.Reconstruct] a contagem de Append NÃO se
// move (nenhum efeito de escrita) e a de Read move-se (o replay lê o log).
type countingStore struct {
	inner   *eventstore.Store
	appends atomic.Int64
	reads   atomic.Int64
}

func (c *countingStore) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	c.appends.Add(1)
	return c.inner.Append(ctx, streamID, in, opts...)
}

func (c *countingStore) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	c.reads.Add(1)
	return c.inner.Read(ctx, streamID, fromSeq)
}

// countingProposer é o LLM espião: conta quantas vezes é consultado. É a sonda da
// invariante «nenhum evento re-chama o modelo» — a contagem sobe UMA vez na
// emissão de plan.proposed e NUNCA mais durante a reconstrução.
type countingProposer struct{ calls atomic.Int64 }

func (p *countingProposer) Propose(ctx context.Context) (string, pe.PlannerMeta, error) {
	p.calls.Add(1)
	return "sha256:planhash-1", pe.PlannerMeta{
		Model:            "model-x",
		PromptVersion:    "prompt-v1",
		CapabilitiesHash: "sha256:caps-1",
	}, nil
}

// emitLifecycle apensa uma trajectória completa do ciclo de vida ao store, na
// ordem canónica de §3.2 (que NÃO é alfabética), e devolve a ordem de tipos
// esperada e os bytes de payload apensos por seq, para as asserções byte-a-byte.
func emitLifecycle(t *testing.T, rec *pe.Recorder, proposer pe.Proposer, planID string) (wantTypes []string, wantPayload map[uint64]json.RawMessage) {
	t.Helper()
	ctx := context.Background()
	wantPayload = map[uint64]json.RawMessage{}

	record := func(seq uint64, err error, evType string, payload any) {
		t.Helper()
		if err != nil {
			t.Fatalf("emit %s: %v", evType, err)
		}
		raw, mErr := json.Marshal(payload)
		if mErr != nil {
			t.Fatalf("marshal esperado %s: %v", evType, mErr)
		}
		wantTypes = append(wantTypes, evType)
		wantPayload[seq] = raw
	}

	seq, err := rec.RecordIntakeClassified(ctx, pe.IntakeClassifiedPayload{
		PlanID: planID, GoalID: "goal-1", Classification: pe.ClassificationMeta, Heuristic: "size_and_scope_v1",
	})
	record(seq, err, pe.EventIntakeClassified, pe.IntakeClassifiedPayload{
		PlanID: planID, GoalID: "goal-1", Classification: pe.ClassificationMeta, Heuristic: "size_and_scope_v1",
	})

	seq, err = rec.RecordPlannerAdmitted(ctx, pe.PlannerAdmittedPayload{
		PlanID: planID, PlannerNHI: "nhi:agent:planner", PricingTableVersion: "pt-2", RetryFactor: 2, MaxAttempts: 3,
	})
	record(seq, err, pe.EventPlannerAdmitted, pe.PlannerAdmittedPayload{
		PlanID: planID, PlannerNHI: "nhi:agent:planner", PricingTableVersion: "pt-2", RetryFactor: 2, MaxAttempts: 3,
	})

	seq, err = rec.RecordProposedFrom(ctx, planID, 1, proposer)
	record(seq, err, pe.EventProposed, pe.ProposedPayload{
		PlanID: planID, PlanHash: "sha256:planhash-1", Attempt: 1,
		Meta: pe.PlannerMeta{Model: "model-x", PromptVersion: "prompt-v1", CapabilitiesHash: "sha256:caps-1"},
	})

	vo := pe.ValidationOutcome{
		PlanID: planID, PlanHash: "sha256:planhash-1", Rule: pe.RuleToolResolution, Attempt: 1, MaxAttempts: 3,
		RawDetail: "irrelevante para a ordem",
	}
	vfp, vErr := pe.NewValidationFailed(vo)
	if vErr != nil {
		t.Fatalf("NewValidationFailed: %v", vErr)
	}
	seq, err = rec.RecordValidationFailed(ctx, vo)
	record(seq, err, pe.EventValidationFailed, vfp)

	seq, err = rec.RecordValidated(ctx, pe.ValidatedPayload{
		PlanID: planID, PlanHash: "sha256:planhash-2", NodeCount: 4, BudgetTotal: 1000, MaxDepth: 3, MaxFanout: 5, MaxNodes: 20,
	})
	record(seq, err, pe.EventValidated, pe.ValidatedPayload{
		PlanID: planID, PlanHash: "sha256:planhash-2", NodeCount: 4, BudgetTotal: 1000, MaxDepth: 3, MaxFanout: 5, MaxNodes: 20,
	})

	seq, err = rec.RecordDecision(ctx, pe.DecisionPayload{
		PlanID: planID, PlanHash: "sha256:planhash-2", Decision: pe.DecisionApproved, DecisionRef: "hitl:chan:1",
	})
	record(seq, err, pe.EventApproved, pe.DecisionPayload{
		PlanID: planID, PlanHash: "sha256:planhash-2", Decision: pe.DecisionApproved, DecisionRef: "hitl:chan:1",
	})

	seq, err = rec.RecordMaterialized(ctx, pe.MaterializedPayload{
		PlanID: planID, PlanHash: "sha256:planhash-2",
		Nodes: []pe.MaterializedNode{
			{NodeID: "n1", Kind: pe.SpawnLeaf, Tools: []string{"tool:read"}},
			{NodeID: "n2", Kind: pe.SpawnRole, Tools: []string{"tool:write", "tool:egress"}},
		},
	})
	record(seq, err, pe.EventMaterialized, pe.MaterializedPayload{
		PlanID: planID, PlanHash: "sha256:planhash-2",
		Nodes: []pe.MaterializedNode{
			{NodeID: "n1", Kind: pe.SpawnLeaf, Tools: []string{"tool:read"}},
			{NodeID: "n2", Kind: pe.SpawnRole, Tools: []string{"tool:write", "tool:egress"}},
		},
	})

	seq, err = rec.RecordCapabilityGap(ctx, pe.CapabilityGapPayload{
		PlanID: planID, NodeID: "n2", State: pe.GapOpened, CandidateSkill: "skill:translate",
	})
	record(seq, err, pe.EventCapabilityGapOpened, pe.CapabilityGapPayload{
		PlanID: planID, NodeID: "n2", State: pe.GapOpened, CandidateSkill: "skill:translate",
	})

	seq, err = rec.RecordCapabilityGap(ctx, pe.CapabilityGapPayload{
		PlanID: planID, NodeID: "n2", State: pe.GapResolved, CandidateSkill: "skill:translate", RatificationID: "rat:1",
	})
	record(seq, err, pe.EventCapabilityGapResolved, pe.CapabilityGapPayload{
		PlanID: planID, NodeID: "n2", State: pe.GapResolved, CandidateSkill: "skill:translate", RatificationID: "rat:1",
	})

	seq, err = rec.RecordReplan(ctx, pe.ReplanPayload{
		PlanID: planID, Phase: pe.ReplanRequested, Subgraph: []string{"n2"}, ResidualBudget: 400, NewPlanHash: "sha256:planhash-3",
	})
	record(seq, err, pe.EventReplanRequested, pe.ReplanPayload{
		PlanID: planID, Phase: pe.ReplanRequested, Subgraph: []string{"n2"}, ResidualBudget: 400, NewPlanHash: "sha256:planhash-3",
	})

	seq, err = rec.RecordReplan(ctx, pe.ReplanPayload{
		PlanID: planID, Phase: pe.ReplanApplied, Subgraph: []string{"n2"}, ResidualBudget: 350, NewPlanHash: "sha256:planhash-3",
	})
	record(seq, err, pe.EventReplanApplied, pe.ReplanPayload{
		PlanID: planID, Phase: pe.ReplanApplied, Subgraph: []string{"n2"}, ResidualBudget: 350, NewPlanHash: "sha256:planhash-3",
	})

	return wantTypes, wantPayload
}

// TestReplayReconstructsSequenceByteForByte — CA(a)+(b), ADR-010.
//
// FALSIFICÁVEL: emite a trajectória completa e reconstrói por replay, exigindo (1)
// a MESMA ORDEM de tipos e (2) os MESMOS BYTES de payload por seq. A ordem de
// emissão é a canónica do ciclo de vida (não-alfabética), por isso uma
// reconstrução que ordenasse por tipo, deduplicasse, ou projectasse por mapa (sem
// ordem estável) produziria uma ordem diferente e ESTE teste falharia. Uma
// reconstrução que re-serializasse o payload (em vez de preservar os bytes)
// falharia a asserção byte-a-byte.
func TestReplayReconstructsSequenceByteForByte(t *testing.T) {
	t.Parallel()
	inner, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer inner.Close()
	store := &countingStore{inner: inner}

	rec, err := pe.NewRecorder(store)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	proposer := &countingProposer{}
	planID := "plan-lifecycle-1"

	wantTypes, wantPayload := emitLifecycle(t, rec, proposer, planID)

	got, err := pe.Reconstruct(context.Background(), store, planID)
	if err != nil {
		t.Fatalf("Reconstruct: %v", err)
	}

	gotTypes := pe.Types(got)
	if len(gotTypes) != len(wantTypes) {
		t.Fatalf("nº de eventos: got %d, want %d (%v)", len(gotTypes), len(wantTypes), gotTypes)
	}
	for i := range wantTypes {
		if gotTypes[i] != wantTypes[i] {
			t.Fatalf("ordem divergente na posição %d: got %q, want %q\nfull got: %v", i, gotTypes[i], wantTypes[i], gotTypes)
		}
	}

	// (2) byte-a-byte: os bytes reconstruídos por seq são idênticos aos apensos.
	for _, e := range got {
		want, ok := wantPayload[e.Seq]
		if !ok {
			t.Fatalf("seq %d (%s) sem payload esperado", e.Seq, e.Type)
		}
		if string(e.Payload) != string(want) {
			t.Fatalf("payload divergente em seq %d (%s):\n got=%s\nwant=%s", e.Seq, e.Type, e.Payload, want)
		}
	}

	// Seqs estritamente crescentes 1..N: a ordem total do stream, sem buracos.
	for i, e := range got {
		if e.Seq != uint64(i+1) {
			t.Fatalf("seq não-contígua na posição %d: got %d, want %d", i, e.Seq, i+1)
		}
	}
}

// TestReconstructIsReadOnly_NoAppend_NoLLM — CA: «nenhum evento re-chama o LLM;
// replay é read-only».
//
// FALSIFICÁVEL por duas sondas independentes:
//   - contador de Append do store: fixado após a emissão, TEM de ficar inalterado
//     durante a reconstrução. Uma reconstrução que apensasse (marcador, re-emissão)
//     movê-lo-ia e o teste falharia.
//   - contador do Proposer (o LLM): consultado UMA vez na emissão de plan.proposed;
//     TEM de ficar em 1 após a reconstrução. Um replay que re-derivasse
//     plan.proposed via modelo incrementá-lo-ia e o teste falharia. A reconstrução
//     recebe um EventReader (só Read), pelo que estruturalmente NÃO pode consultar
//     o Proposer — este teste guarda essa fronteira contra regressão.
func TestReconstructIsReadOnly_NoAppend_NoLLM(t *testing.T) {
	t.Parallel()
	inner, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer inner.Close()
	store := &countingStore{inner: inner}

	rec, err := pe.NewRecorder(store)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	proposer := &countingProposer{}
	planID := "plan-readonly-1"

	emitLifecycle(t, rec, proposer, planID)

	appendsAfterEmit := store.appends.Load()
	proposerAfterEmit := proposer.calls.Load()
	if proposerAfterEmit != 1 {
		t.Fatalf("Proposer consultado %d vezes na emissão; esperado exactamente 1", proposerAfterEmit)
	}
	readsBefore := store.reads.Load()

	if _, err := pe.Reconstruct(context.Background(), store, planID); err != nil {
		t.Fatalf("Reconstruct: %v", err)
	}

	if got := store.appends.Load(); got != appendsAfterEmit {
		t.Fatalf("Reconstruct apensou %d evento(s) — replay TEM de ser read-only (Append antes=%d, depois=%d)",
			got-appendsAfterEmit, appendsAfterEmit, got)
	}
	if got := proposer.calls.Load(); got != proposerAfterEmit {
		t.Fatalf("Reconstruct re-consultou o LLM %d vez(es) — nenhum evento pode re-chamar o modelo (antes=%d, depois=%d)",
			got-proposerAfterEmit, proposerAfterEmit, got)
	}
	if got := store.reads.Load(); got <= readsBefore {
		t.Fatalf("Reconstruct não leu o log (Read antes=%d, depois=%d) — reconstrução vazia?", readsBefore, got)
	}
}

// TestValidationFailedDoesNotEchoSensitiveContent — CA(c): «sem eco de conteúdo
// sensível/PII em plan.validation_failed».
//
// FALSIFICÁVEL: injecta um segredo reconhecível no RawDetail do ValidationOutcome
// (o canal por onde o conteúdo untrusted do PlanDocument PODERIA vazar) e apensa o
// evento. Depois lê o payload REAL gravado no Event Store e exige que os seus bytes
// NÃO contenham o segredo. Uma projecção [NewValidationFailed] que copiasse
// RawDetail (ou caísse para texto livre) vazaria o segredo e o teste falharia. Só
// os metadados classificados (rule, diagnostic, attempt) podem sobreviver.
func TestValidationFailedDoesNotEchoSensitiveContent(t *testing.T) {
	t.Parallel()
	const secret = "SSN-123-45-6789-and-api-key-sk-live-DEADBEEF"

	inner, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer inner.Close()

	rec, err := pe.NewRecorder(inner)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	planID := "plan-pii-1"
	ctx := context.Background()

	// O RawDetail carrega o segredo — como se o validador tivesse capturado o
	// literal ofensor do PlanDocument untrusted. NÃO pode chegar ao evento.
	if _, err := rec.RecordValidationFailed(ctx, pe.ValidationOutcome{
		PlanID: planID, PlanHash: "sha256:h", Rule: pe.RuleSchema, Attempt: 1, MaxAttempts: 3,
		RawDetail: "campo desconhecido `secret`=" + secret,
	}); err != nil {
		t.Fatalf("RecordValidationFailed: %v", err)
	}

	events, err := inner.Read(ctx, planID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("esperado 1 evento, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != pe.EventValidationFailed {
		t.Fatalf("tipo inesperado: %q", ev.Type)
	}
	if strings.Contains(string(ev.Payload), secret) {
		t.Fatalf("VAZAMENTO: o payload de plan.validation_failed contém o segredo:\n%s", ev.Payload)
	}
	// Componentes do segredo, para apanhar um eco parcial (ex.: só o SSN).
	for _, frag := range []string{"123-45-6789", "sk-live-DEADBEEF"} {
		if strings.Contains(string(ev.Payload), frag) {
			t.Fatalf("VAZAMENTO PARCIAL: o payload contém %q:\n%s", frag, ev.Payload)
		}
	}

	// Prova de NÃO-VACUIDADE: o evento carrega SIM os metadados classificados —
	// não passou por estar vazio.
	var got pe.ValidationFailedPayload
	if err := json.Unmarshal(ev.Payload, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got.Rule != pe.RuleSchema || got.Diagnostic != "schema_violation" || got.Attempt != 1 || got.MaxAttempts != 3 {
		t.Fatalf("metadados classificados em falta/errados: %+v", got)
	}
}

// TestReconstructFailsClosedOnUnknownType — fail-closed na reconstrução: um evento
// `plan.*` fora do catálogo aborta o replay em vez de o aceitar em silêncio.
//
// FALSIFICÁVEL: apensa directamente ao store um evento com um tipo `plan.` não
// catalogado (imitando um domínio corrompido/evoluído) e exige que Reconstruct
// devolva ErrUnknownEventType. Um reconstrutor permissivo devolveria o evento e o
// teste falharia.
func TestReconstructFailsClosedOnUnknownType(t *testing.T) {
	t.Parallel()
	inner, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer inner.Close()
	ctx := context.Background()
	planID := "plan-unknown-1"

	rec, _ := pe.NewRecorder(inner)
	if _, err := rec.RecordIntakeClassified(ctx, pe.IntakeClassifiedPayload{
		PlanID: planID, GoalID: "g", Classification: pe.ClassificationSimple, Heuristic: "h",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Evento forjado com tipo plan.* desconhecido (não usa o Recorder típico).
	if _, err := inner.Append(ctx, planID, eventstore.EventInput{
		Type:          "plan.not_a_real_event",
		Payload:       json.RawMessage(`{}`),
		SchemaVersion: pe.DomainVersion,
		RunID:         planID,
		StepID:        "forged",
	}); err != nil {
		t.Fatalf("append forjado: %v", err)
	}

	_, err = pe.Reconstruct(ctx, inner, planID)
	if err == nil {
		t.Fatal("Reconstruct aceitou um tipo desconhecido — deveria falhar fail-closed")
	}
	if !isErr(err, pe.ErrUnknownEventType) {
		t.Fatalf("erro inesperado: %v (esperado ErrUnknownEventType)", err)
	}
}

// TestReconstructFailsClosedOnUnknownDomainVersion — fail-closed: um evento do
// domínio com versão de schema inesperada é inadmissível (tecnica/18 §3.6).
func TestReconstructFailsClosedOnUnknownDomainVersion(t *testing.T) {
	t.Parallel()
	inner, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer inner.Close()
	ctx := context.Background()
	planID := "plan-badver-1"

	if _, err := inner.Append(ctx, planID, eventstore.EventInput{
		Type:          pe.EventIntakeClassified,
		Payload:       json.RawMessage(`{}`),
		SchemaVersion: "aos.planner.v2",
		RunID:         planID,
		StepID:        "s",
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	_, err = pe.Reconstruct(ctx, inner, planID)
	if err == nil {
		t.Fatal("Reconstruct aceitou versão de domínio desconhecida — deveria falhar fail-closed")
	}
	if !isErr(err, pe.ErrUnknownDomainVersion) {
		t.Fatalf("erro inesperado: %v (esperado ErrUnknownDomainVersion)", err)
	}
}

// isErr é um errors.Is local (evita importar errors em cada teste).
func isErr(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
