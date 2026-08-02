package capabilitygap

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	budget "github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	identity "github.com/aos-ref/platform/identity"
)

// ---------------------------------------------------------------------------
// Fakes das portas (o wiring real liga *plannerevents.Recorder, *identity.Issuer,
// *budget.Budget, e as implementações do pipeline; aqui usam-se duplos de teste).
// ---------------------------------------------------------------------------

type fakeRecorder struct {
	mu       sync.Mutex
	payloads []plannerevents.CapabilityGapPayload
	err      error
}

func (f *fakeRecorder) RecordCapabilityGap(_ context.Context, p plannerevents.CapabilityGapPayload) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	f.payloads = append(f.payloads, p)
	return uint64(len(f.payloads)), nil
}

func (f *fakeRecorder) count(state plannerevents.GapState) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.payloads {
		if p.State == state {
			n++
		}
	}
	return n
}

func (f *fakeRecorder) last() (plannerevents.CapabilityGapPayload, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.payloads) == 0 {
		return plannerevents.CapabilityGapPayload{}, false
	}
	return f.payloads[len(f.payloads)-1], true
}

type fakeIssuer struct{ err error }

func (f fakeIssuer) IssueChild(_ context.Context, _ string, req identity.ChildRequest) (identity.Token, error) {
	if f.err != nil {
		return identity.Token{}, f.err
	}
	return identity.Token{Compact: "nhi:" + req.AgentID}, nil
}

type reserveCall struct{ kind string }

type fakeReserver struct {
	mu         sync.Mutex
	calls      []reserveCall
	reserveErr error
	commitErr  error
}

func (f *fakeReserver) Reserve(_ context.Context, nodeID string, _ budget.Amount) (budget.Reservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, reserveCall{"reserve"})
	if f.reserveErr != nil {
		return budget.Reservation{}, f.reserveErr
	}
	return budget.Reservation{ID: "res-1", NodeID: nodeID}, nil
}
func (f *fakeReserver) Commit(_ context.Context, _ budget.Reservation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, reserveCall{"commit"})
	return f.commitErr
}
func (f *fakeReserver) Release(_ context.Context, _ budget.Reservation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, reserveCall{"release"})
	return nil
}
func (f *fakeReserver) kinds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.kind
	}
	return out
}

type fakeAuthor struct {
	cand CandidateSkill
	err  error
}

func (f fakeAuthor) Author(_ context.Context, _ GapSpec, _ identity.Token, _ []string) (CandidateSkill, error) {
	if f.err != nil {
		return CandidateSkill{}, f.err
	}
	return f.cand, nil
}

type fakeDry struct {
	res DryRunResult
	err error
}

func (f fakeDry) DryRun(_ context.Context, _ TaintedArtifact) (DryRunResult, error) {
	return f.res, f.err
}

type fakeEval struct {
	seen *TaintedArtifact // captura o artefacto que atravessou o gate (taint)
	v    EvalVerdict
	err  error
}

func (f *fakeEval) Evaluate(_ context.Context, art TaintedArtifact) (EvalVerdict, error) {
	a := art
	f.seen = &a
	return f.v, f.err
}

type fakeCanary struct {
	res CanaryResult
	err error
}

func (f fakeCanary) Canary(_ context.Context, _ TaintedArtifact) (CanaryResult, error) {
	return f.res, f.err
}

// fakeRatifier: por omissão espelha o content-hash apresentado (humano que assina
// exactamente o artefacto que lhe é mostrado). Os testes sobrepõem para simular
// rejeição, substituição, não-verificação ou transplante.
type fakeRatifier struct {
	out      RatificationOutcome
	echoHash bool // se true, BoundContentHash = req.ContentHash
	err      error
	seenHash string
}

func (f *fakeRatifier) Ratify(_ context.Context, req RatificationRequest) (RatificationOutcome, error) {
	f.seenHash = req.ContentHash
	if f.err != nil {
		return RatificationOutcome{}, f.err
	}
	out := f.out
	if f.echoHash {
		out.BoundContentHash = req.ContentHash
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Harness.
// ---------------------------------------------------------------------------

type harness struct {
	rec      *fakeRecorder
	issuer   fakeIssuer
	reserver *fakeReserver
	author   *fakeAuthor
	dry      *fakeDry
	eval     *fakeEval
	canary   *fakeCanary
	ratifier *fakeRatifier
	co       *Coordinator
}

func newHarness(t *testing.T, max int) *harness {
	t.Helper()
	h := &harness{
		rec:      &fakeRecorder{},
		issuer:   fakeIssuer{},
		reserver: &fakeReserver{},
		author: &fakeAuthor{cand: CandidateSkill{
			Name:           "skill.summarize",
			Version:        "1.0.0",
			Content:        []byte("opaque-skill-bytes"),
			RequestedTools: []string{"tool.read"},
			Meta:           plannerevents.PlannerMeta{Model: "m", PromptVersion: "p", CapabilitiesHash: "h"},
		}},
		dry:      &fakeDry{res: DryRunResult{Passed: true}},
		eval:     &fakeEval{v: EvalVerdict{Admitted: true, Score: 0.9}},
		canary:   &fakeCanary{res: CanaryResult{Passed: true}},
		ratifier: &fakeRatifier{echoHash: true, out: RatificationOutcome{Disposition: DispositionApproved, Verified: true, RatificationID: "rat-123"}},
	}
	co, err := NewCoordinator(h.rec, h.issuer, h.reserver, h.author, h.dry, h.eval, h.canary, h.ratifier, Config{
		MaxGapsPerPlan:   max,
		ParentToken:      "parent-token",
		AuthorClass:      "author",
		Allowlist:        []string{"tool.read", "tool.write"},
		AuthorBudgetNode: "author-node",
		AuthorReserve:    budget.Amount{Tokens: 1000, CostMicroUSD: 500},
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	h.co = co
	return h
}

func (h *harness) openAndRunToCanary(t *testing.T, spec GapSpec) *GapNode {
	t.Helper()
	n, err := h.co.OpenGap(context.Background(), spec)
	if err != nil {
		t.Fatalf("OpenGap: %v", err)
	}
	steps := []func(context.Context) error{n.Author, n.DryRun, n.EvalGate, n.Canary}
	for i, s := range steps {
		if err := s(context.Background()); err != nil {
			t.Fatalf("etapa %d: %v", i, err)
		}
	}
	if n.State() != StateCanary {
		t.Fatalf("estado após pipeline = %q, quer %q", n.State(), StateCanary)
	}
	return n
}

func specN(i int) GapSpec {
	return GapSpec{PlanID: "plan-1", NodeID: fmt.Sprintf("node-%d", i), CandidateSkill: "skill.x"}
}

// ---------------------------------------------------------------------------
// Testes.
// ---------------------------------------------------------------------------

// O nó NÃO despacha até `capability_gap_resolved`. FALHA-ANTES: sem o gate de
// estado em CanDispatch (se devolvesse nil em StateWaiting), o nó despacharia logo
// na abertura — este teste apanha-o.
func TestNode_DoesNotDispatch_UntilResolved(t *testing.T) {
	h := newHarness(t, 4)
	n, err := h.co.OpenGap(context.Background(), specN(0))
	if err != nil {
		t.Fatalf("OpenGap: %v", err)
	}
	// Recém-aberto: bloqueado.
	if err := n.CanDispatch(); !errors.Is(err, ErrNodeBlocked) {
		t.Fatalf("CanDispatch no estado inicial = %v, quer ErrNodeBlocked", err)
	}
	// Em cada etapa intermédia continua bloqueado (não despacha a meio do pipeline).
	for _, step := range []func(context.Context) error{n.Author, n.DryRun, n.EvalGate, n.Canary} {
		if err := step(context.Background()); err != nil {
			t.Fatalf("etapa: %v", err)
		}
		if err := n.CanDispatch(); !errors.Is(err, ErrNodeBlocked) {
			t.Fatalf("CanDispatch em %q = %v, quer ErrNodeBlocked", n.State(), err)
		}
	}
	// Só a ratificação assinada resolve e destranca.
	if err := n.Ratify(context.Background()); err != nil {
		t.Fatalf("Ratify: %v", err)
	}
	if n.State() != StateResolved {
		t.Fatalf("estado = %q, quer resolved", n.State())
	}
	if err := n.CanDispatch(); err != nil {
		t.Fatalf("CanDispatch após resolved = %v, quer nil", err)
	}
	// resolved emitido exactamente uma vez com a RatificationID.
	if got := h.rec.count(plannerevents.GapResolved); got != 1 {
		t.Fatalf("gap_resolved emitido %d vezes, quer 1", got)
	}
	last, _ := h.rec.last()
	if last.State != plannerevents.GapResolved || last.RatificationID != "rat-123" {
		t.Fatalf("payload resolved = %+v", last)
	}
}

// Um artefacto auto-escrito NÃO chega a produção unilateralmente: tem de passar
// TODAS as etapas + ratificação assinada. Aqui prova-se que saltar directamente
// para Ratify (bypass) é recusado, e que sem o pipeline completo o nó nunca resolve.
func TestSelfAuthored_NotToProduction_Unilaterally(t *testing.T) {
	h := newHarness(t, 4)
	n, err := h.co.OpenGap(context.Background(), specN(0))
	if err != nil {
		t.Fatalf("OpenGap: %v", err)
	}
	if err := n.Author(context.Background()); err != nil {
		t.Fatalf("Author: %v", err)
	}
	// Bypass: tentar ratificar sem dry-run/eval/canary.
	if err := n.Ratify(context.Background()); !errors.Is(err, ErrStageOutOfOrder) {
		t.Fatalf("Ratify após só Author = %v, quer ErrStageOutOfOrder", err)
	}
	if n.State() == StateResolved {
		t.Fatal("nó resolveu por bypass — proibido")
	}
	if err := n.CanDispatch(); err == nil {
		t.Fatal("nó despacharia sem pipeline completo — proibido")
	}
	// Nenhum resolved foi emitido.
	if got := h.rec.count(plannerevents.GapResolved); got != 0 {
		t.Fatalf("gap_resolved emitido %d vezes num bypass, quer 0", got)
	}
}

// Nenhum bypass do pipeline: cada etapa exige o seu predecessor exacto.
func TestNoBypass_EachStageRequiresPredecessor(t *testing.T) {
	ctx := context.Background()
	// DryRun/EvalGate/Canary/Ratify chamados antes do seu predecessor ⇒ out-of-order.
	cases := []struct {
		name string
		call func(*GapNode) error
	}{
		{"DryRun antes de Author", func(n *GapNode) error { return n.DryRun(ctx) }},
		{"EvalGate antes de DryRun", func(n *GapNode) error {
			if err := n.Author(ctx); err != nil {
				return err
			}
			return n.EvalGate(ctx)
		}},
		{"Canary antes de EvalGate", func(n *GapNode) error {
			if err := n.Author(ctx); err != nil {
				return err
			}
			if err := n.DryRun(ctx); err != nil {
				return err
			}
			return n.Canary(ctx)
		}},
		{"Ratify antes de Canary", func(n *GapNode) error {
			if err := n.Author(ctx); err != nil {
				return err
			}
			if err := n.DryRun(ctx); err != nil {
				return err
			}
			if err := n.EvalGate(ctx); err != nil {
				return err
			}
			return n.Ratify(ctx)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, 4)
			n, err := h.co.OpenGap(ctx, specN(0))
			if err != nil {
				t.Fatalf("OpenGap: %v", err)
			}
			if err := c.call(n); !errors.Is(err, ErrStageOutOfOrder) {
				t.Fatalf("%s = %v, quer ErrStageOutOfOrder", c.name, err)
			}
		})
	}
}

// Teto de gaps por plano excedido bloqueia (fail-closed).
func TestGapCeiling_ExceededBlocks(t *testing.T) {
	h := newHarness(t, 2)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := h.co.OpenGap(ctx, specN(i)); err != nil {
			t.Fatalf("OpenGap %d dentro do teto = %v", i, err)
		}
	}
	// O terceiro excede o teto.
	if _, err := h.co.OpenGap(ctx, specN(2)); !errors.Is(err, ErrGapCeilingExceeded) {
		t.Fatalf("OpenGap além do teto = %v, quer ErrGapCeilingExceeded", err)
	}
	// Um plano DIFERENTE tem o seu próprio teto (isolamento).
	if _, err := h.co.OpenGap(ctx, GapSpec{PlanID: "plan-2", NodeID: "n0", CandidateSkill: "s"}); err != nil {
		t.Fatalf("OpenGap noutro plano = %v, quer nil", err)
	}
	if got := h.rec.count(plannerevents.GapOpened); got != 3 {
		t.Fatalf("gap_opened emitido %d vezes, quer 3 (2 do plano-1 + 1 do plano-2)", got)
	}
}

// Teto sob concorrência: exatamente `max` aberturas passam no mesmo plano (o resto
// bloqueia). Exercita o mutex do contador sob -race.
func TestGapCeiling_Concurrent(t *testing.T) {
	const max = 5
	const goroutines = 50
	h := newHarness(t, max)
	var ok, blocked int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := h.co.OpenGap(context.Background(), specN(i))
			mu.Lock()
			if err == nil {
				ok++
			} else if errors.Is(err, ErrGapCeilingExceeded) {
				blocked++
			}
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if ok != max {
		t.Fatalf("aberturas bem-sucedidas = %d, quer %d", ok, max)
	}
	if blocked != goroutines-max {
		t.Fatalf("aberturas bloqueadas = %d, quer %d", blocked, goroutines-max)
	}
}

// Allowlist restrita: uma tool pedida FORA da allowlist (via input untrusted do
// autor) é recusada fail-closed, e a reserva de orçamento é LIBERTADA (sem leak).
func TestAllowlistViolation_FailClosedAndReleasesBudget(t *testing.T) {
	h := newHarness(t, 4)
	h.author.cand.RequestedTools = []string{"tool.read", "tool.DELETE_EVERYTHING"}
	n, err := h.co.OpenGap(context.Background(), specN(0))
	if err != nil {
		t.Fatalf("OpenGap: %v", err)
	}
	if err := n.Author(context.Background()); !errors.Is(err, ErrAllowlistViolation) {
		t.Fatalf("Author com tool fora da allowlist = %v, quer ErrAllowlistViolation", err)
	}
	if n.State() != StateWaiting {
		t.Fatalf("estado após falha de autoria = %q, quer waiting", n.State())
	}
	// Orçamento: reserve seguido de release (sem commit).
	kinds := h.reserver.kinds()
	if len(kinds) != 2 || kinds[0] != "reserve" || kinds[1] != "release" {
		t.Fatalf("sequência de orçamento = %v, quer [reserve release]", kinds)
	}
}

// Autoria bem-sucedida consolida o orçamento (reserve→commit) e o artefacto nasce
// TAINTADO; o eval-gate vê o taint (o artefacto que o atravessa é self_authored).
func TestAuthor_CommitsBudget_AndTaintTravels(t *testing.T) {
	h := newHarness(t, 4)
	n := h.openAndRunToCanary(t, specN(0))
	kinds := h.reserver.kinds()
	if len(kinds) != 2 || kinds[0] != "reserve" || kinds[1] != "commit" {
		t.Fatalf("sequência de orçamento = %v, quer [reserve commit]", kinds)
	}
	if n.Artifact().Origin != OriginSelfAuthored {
		t.Fatalf("origem do artefacto = %q, quer self_authored", n.Artifact().Origin)
	}
	// O taint acompanhou o artefacto ATÉ ao eval-gate.
	if h.eval.seen == nil || h.eval.seen.Origin != OriginSelfAuthored {
		t.Fatalf("eval-gate não viu o artefacto taintado: %+v", h.eval.seen)
	}
	if h.eval.seen.ContentHash == "" || h.eval.seen.ContentHash != n.Artifact().ContentHash {
		t.Fatalf("content-hash não acompanhou o artefacto pelo eval-gate")
	}
}

// Rejeição humana ⇒ nó bloqueado, sem resolved.
func TestRatification_HumanReject(t *testing.T) {
	h := newHarness(t, 4)
	h.ratifier.out = RatificationOutcome{Disposition: DispositionRejected}
	h.ratifier.echoHash = false
	n := h.openAndRunToCanary(t, specN(0))
	if err := n.Ratify(context.Background()); !errors.Is(err, ErrRatificationRefused) {
		t.Fatalf("Ratify (rejeição) = %v, quer ErrRatificationRefused", err)
	}
	if n.State() != StateRejected {
		t.Fatalf("estado = %q, quer rejected", n.State())
	}
	if err := n.CanDispatch(); err == nil {
		t.Fatal("nó rejeitado não pode despachar")
	}
	if got := h.rec.count(plannerevents.GapResolved); got != 0 {
		t.Fatalf("gap_resolved emitido numa rejeição, quer 0")
	}
}

// Substituição humana ⇒ nó rejeitado, com o substituto exposto.
func TestRatification_HumanReplace(t *testing.T) {
	h := newHarness(t, 4)
	h.ratifier.out = RatificationOutcome{Disposition: DispositionReplaced, ReplacementNodeID: "node-manual-7"}
	h.ratifier.echoHash = false
	n := h.openAndRunToCanary(t, specN(0))
	if err := n.Ratify(context.Background()); !errors.Is(err, ErrRatificationReplaced) {
		t.Fatalf("Ratify (substituição) = %v, quer ErrRatificationReplaced", err)
	}
	if n.ReplacementNodeID() != "node-manual-7" {
		t.Fatalf("substituto = %q, quer node-manual-7", n.ReplacementNodeID())
	}
	if err := n.CanDispatch(); err == nil {
		t.Fatal("nó substituído não pode despachar")
	}
}

// Aprovação NÃO verificada não promove (fail-closed).
func TestRatification_UnverifiedApprovalDoesNotPromote(t *testing.T) {
	h := newHarness(t, 4)
	h.ratifier.echoHash = true
	h.ratifier.out = RatificationOutcome{Disposition: DispositionApproved, Verified: false, RatificationID: "rat-x"}
	n := h.openAndRunToCanary(t, specN(0))
	if err := n.Ratify(context.Background()); !errors.Is(err, ErrRatificationRefused) {
		t.Fatalf("Ratify (não-verificada) = %v, quer ErrRatificationRefused", err)
	}
	if n.State() == StateResolved {
		t.Fatal("aprovação não-verificada resolveu — proibido")
	}
}

// Transplante: aprovação verificada mas amarrada a OUTRO artefacto (content-hash
// divergente) é recusada fail-closed.
func TestRatification_TransplantRejected(t *testing.T) {
	h := newHarness(t, 4)
	h.ratifier.echoHash = false // não espelha; devolve um hash de outro artefacto
	h.ratifier.out = RatificationOutcome{
		Disposition:      DispositionApproved,
		Verified:         true,
		RatificationID:   "rat-other",
		BoundContentHash: "deadbeef-hash-de-outro-artefacto",
	}
	n := h.openAndRunToCanary(t, specN(0))
	if err := n.Ratify(context.Background()); !errors.Is(err, ErrRatificationTransplant) {
		t.Fatalf("Ratify (transplante) = %v, quer ErrRatificationTransplant", err)
	}
	if n.State() == StateResolved {
		t.Fatal("ratificação transplantada resolveu — proibido")
	}
}

// Uma etapa do pipeline que reprova bloqueia o nó (fail-closed), sem resolved.
func TestPipelineStageFailure_Blocks(t *testing.T) {
	ctx := context.Background()
	t.Run("dry-run reprova", func(t *testing.T) {
		h := newHarness(t, 4)
		h.dry.res = DryRunResult{Passed: false, Reason: "efeito irreversível"}
		n, _ := h.co.OpenGap(ctx, specN(0))
		_ = n.Author(ctx)
		if err := n.DryRun(ctx); !errors.Is(err, ErrDryRunFailed) {
			t.Fatalf("DryRun reprovado = %v, quer ErrDryRunFailed", err)
		}
		if n.State() != StateBlocked {
			t.Fatalf("estado = %q, quer blocked", n.State())
		}
		if err := n.CanDispatch(); err == nil {
			t.Fatal("nó bloqueado não despacha")
		}
	})
	t.Run("eval-gate não admite", func(t *testing.T) {
		h := newHarness(t, 4)
		h.eval.v = EvalVerdict{Admitted: false, Reason: "score abaixo do piso"}
		n, _ := h.co.OpenGap(ctx, specN(0))
		_ = n.Author(ctx)
		_ = n.DryRun(ctx)
		if err := n.EvalGate(ctx); !errors.Is(err, ErrEvalRejected) {
			t.Fatalf("EvalGate não-admitido = %v, quer ErrEvalRejected", err)
		}
		if n.State() != StateBlocked {
			t.Fatalf("estado = %q, quer blocked", n.State())
		}
	})
	t.Run("canary reprova", func(t *testing.T) {
		h := newHarness(t, 4)
		h.canary.res = CanaryResult{Passed: false, Reason: "regressão"}
		n, _ := h.co.OpenGap(ctx, specN(0))
		_ = n.Author(ctx)
		_ = n.DryRun(ctx)
		_ = n.EvalGate(ctx)
		if err := n.Canary(ctx); !errors.Is(err, ErrCanaryFailed) {
			t.Fatalf("Canary reprovado = %v, quer ErrCanaryFailed", err)
		}
		if n.State() != StateBlocked {
			t.Fatalf("estado = %q, quer blocked", n.State())
		}
	})
}

// gap_opened é emitido na abertura (o nó começa bloqueado à espera).
func TestOpenGap_EmitsOpened(t *testing.T) {
	h := newHarness(t, 4)
	if _, err := h.co.OpenGap(context.Background(), specN(0)); err != nil {
		t.Fatalf("OpenGap: %v", err)
	}
	if got := h.rec.count(plannerevents.GapOpened); got != 1 {
		t.Fatalf("gap_opened = %d, quer 1", got)
	}
	last, _ := h.rec.last()
	if last.State != plannerevents.GapOpened || last.RatificationID != "" {
		t.Fatalf("payload opened = %+v (RatificationID deve ser vazio)", last)
	}
}

// Dependências obrigatórias e config inválida são fail-closed na construção.
func TestNewCoordinator_FailClosed(t *testing.T) {
	good := Config{MaxGapsPerPlan: 1, Allowlist: []string{"t"}, AuthorBudgetNode: "n", AuthorReserve: budget.Amount{Tokens: 1}}
	rec := &fakeRecorder{}
	iss := fakeIssuer{}
	rsv := &fakeReserver{}
	au := &fakeAuthor{}
	dr := &fakeDry{}
	ev := &fakeEval{}
	cn := &fakeCanary{}
	rt := &fakeRatifier{}
	if _, err := NewCoordinator(nil, iss, rsv, au, dr, ev, cn, rt, good); !errors.Is(err, ErrCoordinatorDeps) {
		t.Fatalf("recorder nil = %v, quer ErrCoordinatorDeps", err)
	}
	if _, err := NewCoordinator(rec, iss, rsv, au, dr, ev, cn, rt, Config{MaxGapsPerPlan: 0, Allowlist: []string{"t"}, AuthorBudgetNode: "n", AuthorReserve: budget.Amount{Tokens: 1}}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("teto 0 = %v, quer ErrInvalidConfig", err)
	}
	if _, err := NewCoordinator(rec, iss, rsv, au, dr, ev, cn, rt, Config{MaxGapsPerPlan: 1, Allowlist: nil, AuthorBudgetNode: "n", AuthorReserve: budget.Amount{Tokens: 1}}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("allowlist vazia = %v, quer ErrInvalidConfig", err)
	}
	if _, err := NewCoordinator(rec, iss, rsv, au, dr, ev, cn, rt, Config{MaxGapsPerPlan: 1, Allowlist: []string{"t"}, AuthorBudgetNode: "n", AuthorReserve: budget.Amount{}}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("reserva zero = %v, quer ErrInvalidConfig", err)
	}
}
