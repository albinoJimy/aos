package uxdx_test

import (
	"context"
	"errors"
	"testing"
	"time"

	approvalcard "github.com/aos-ref/control-plane/governance/approval-card"
	"github.com/aos-ref/control-plane/governance/autonomy"
	autonomysurface "github.com/aos-ref/control-plane/governance/autonomy-surface"
	planapproval "github.com/aos-ref/control-plane/governance/plan-approval"
	progresssurface "github.com/aos-ref/control-plane/governance/progress-surface"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// AC1 — USABILIDADE DOS GATES. Um teste por gate: o preview é COMPLETO, as opções são
// INEQUÍVOCAS e a decisão FAIL-CLOSED é respeitada. COMPÕE as superfícies de AOS-120..
// 125 — não reimplementa nenhuma.

// (AOS-120) approval-card: o preview é completo (não-vazio), o dual-control é coerente
// com a irreversibilidade, e o DualControlCollector rejeita um único aprovador
// (self-quorum) — a decisão fail-closed é inequívoca.
func TestUsability_ApprovalCard_PreviewCompleteAndFailClosedDualControl(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Card DANGER/IRREVERSÍVEL: preview completo, dual-control EXIGIDO.
	danger, err := approvalcard.BuildCard(
		dangerReq("cap:fs.delete -> /data/synthetic"),
		approvalcard.WithRequestID("card-danger-1"),
	)
	if err != nil {
		t.Fatalf("BuildCard(danger): %v", err)
	}
	if danger.Preview == "" {
		t.Fatal("preview VAZIO: o efeito concreto tem de ser apresentado (preview incompleto)")
	}
	if !danger.Irreversible || !danger.DualControlRequired {
		t.Fatalf("card irreversível devia exigir dual-control: irrev=%v dual=%v", danger.Irreversible, danger.DualControlRequired)
	}
	if danger.DualControlRequired != danger.Irreversible {
		t.Fatal("dual-control tem de ser coerente com a irreversibilidade (inequívoco)")
	}

	// O canal REAL de AOS-095 responde SEMPRE com o MESMO aprovador — um único aprovador
	// (self-quorum) NÃO satisfaz o dual-control de uma acção irreversível: fail-closed.
	ch, _ := realChannel(t, true)
	collector, err := approvalcard.NewDualControlCollector(ch)
	if err != nil {
		t.Fatalf("NewDualControlCollector: %v", err)
	}
	dec, err := collector.Authorize(ctx, danger)
	if err != nil {
		t.Fatalf("Authorize(danger): %v", err)
	}
	if dec.Authorized {
		t.Fatal("FAIL-CLOSED violado: um único aprovador autorizou uma acção irreversível (falta o 2.º distinto)")
	}

	// Contraprova (não-tautológica): um card REVERSÍVEL não exige dual-control e UMA
	// aprovação basta — a superfície não injecta fricção injustificada.
	reversible, err := approvalcard.BuildCard(
		risk.ConfirmationRequest{
			Class:      risk.ClassGray,
			Preview:    "cap:http.get -> https://api/synthetic",
			Principal:  requesterID,
			Capability: "cap:http.get",
			Resource:   "https://api/synthetic",
		},
		approvalcard.WithRequestID("card-reversible-1"),
	)
	if err != nil {
		t.Fatalf("BuildCard(reversible): %v", err)
	}
	if reversible.DualControlRequired || reversible.Irreversible {
		t.Fatalf("card reversível NÃO devia exigir dual-control: irrev=%v dual=%v", reversible.Irreversible, reversible.DualControlRequired)
	}
	decR, err := collector.Authorize(ctx, reversible)
	if err != nil {
		t.Fatalf("Authorize(reversible): %v", err)
	}
	if !decR.Authorized || len(decR.Approvers) != 1 {
		t.Fatalf("uma aprovação devia bastar para um card reversível: authorized=%v approvers=%v", decR.Authorized, decR.Approvers)
	}
}

// (AOS-121) plan-approval: o plano é completo (node_count>0), os verdicts são
// inequívocos (approve/edit/reject distintos), e o SpawnGuard recusa fail-closed
// qualquer spawn de um run cujo plano não foi aprovado.
func TestUsability_PlanApproval_CompletePlanUnambiguousVerdictsFailClosedSpawn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	plan := samplePlan(uxdxRunID, risk.ClassDanger)

	// Plano COMPLETO: um card por nó, node_count>0.
	card, err := planapproval.BuildPlanCard(plan)
	if err != nil {
		t.Fatalf("BuildPlanCard: %v", err)
	}
	if card.NodeCount() == 0 {
		t.Fatal("plano VAZIO: um plano sem nós não é apresentável (node_count==0)")
	}
	if len(card.NodeCards) != card.NodeCount() {
		t.Fatalf("plano incompleto: %d cards para %d nós", len(card.NodeCards), card.NodeCount())
	}
	for i := range card.NodeCards {
		if card.NodeCards[i].Preview == "" {
			t.Fatalf("nó %d sem preview: o efeito concreto por-nó tem de ser apresentado", i)
		}
	}

	// Verdicts INEQUÍVOCOS: as três decisões têm rótulos distintos e semântica clara.
	verdicts := map[string]planapproval.Verdict{
		"approve": planapproval.VerdictApprove,
		"edit":    planapproval.VerdictEdit,
		"reject":  planapproval.VerdictReject,
	}
	seen := map[string]bool{}
	for want, v := range verdicts {
		if v.String() != want {
			t.Fatalf("verdict %v.String()=%q, quero %q (rótulo inequívoco)", v, v.String(), want)
		}
		if seen[v.String()] {
			t.Fatalf("verdicts ambíguos: %q repetido", v.String())
		}
		seen[v.String()] = true
	}
	if !planapproval.VerdictApprove.Approved() || !planapproval.VerdictEdit.Approved() || planapproval.VerdictReject.Approved() {
		t.Fatal("semântica de Approved() ambígua: approve/edit libertam, reject não")
	}

	// SpawnGuard fail-closed: NADA spawna antes da aprovação; aprova → spawna; reject
	// mantém o veto.
	spawner := &spySpawner{}
	guard, err := planapproval.NewSpawnGuard(spawner)
	if err != nil {
		t.Fatalf("NewSpawnGuard: %v", err)
	}
	// (a) Antes da aprovação: recusa fail-closed, custo de tokens a ZERO.
	if err := guard.Spawn(ctx, plan.RunID); !errors.Is(err, planapproval.ErrPlanNotApproved) {
		t.Fatalf("Spawn pré-aprovação devia ser ErrPlanNotApproved, got %v", err)
	}
	if spawner.count() != 0 {
		t.Fatalf("nenhum spawn devia ter corrido antes da aprovação, corri %d", spawner.count())
	}

	// (b) Aprovação humana (oracle L0 força o gate; o canal spy aprova) → liberta o spawn.
	channel := &planSpyChannel{approved: true, approver: approverID}
	gate, err := planapproval.NewPlanGate(fakeOracle{level: autonomy.L0}, channel, planapproval.WithSpawnGuard(guard))
	if err != nil {
		t.Fatalf("NewPlanGate: %v", err)
	}
	dec, err := gate.Approve(ctx, plan)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if dec.Verdict != planapproval.VerdictApprove {
		t.Fatalf("verdict=%s, quero approve", dec.Verdict)
	}
	if channel.calls == 0 {
		t.Fatal("o gate devia ter DEVOLVIDO a decisão ao canal HITL (não decidiu sozinho)")
	}
	if err := guard.Spawn(ctx, plan.RunID); err != nil {
		t.Fatalf("Spawn pós-aprovação devia passar, got %v", err)
	}
	if spawner.count() != 1 {
		t.Fatalf("um spawn devia ter corrido após a aprovação, corri %d", spawner.count())
	}

	// (c) Contraprova fail-closed: um plano REJEITADO nunca liberta o spawn.
	rejSpawner := &spySpawner{}
	rejGuard, _ := planapproval.NewSpawnGuard(rejSpawner)
	rejChannel := &planSpyChannel{approved: false}
	rejGate, _ := planapproval.NewPlanGate(fakeOracle{level: autonomy.L0}, rejChannel, planapproval.WithSpawnGuard(rejGuard))
	rejPlan := samplePlan("run-rejected", risk.ClassDanger)
	rejDec, err := rejGate.Approve(ctx, rejPlan)
	if err != nil {
		t.Fatalf("Approve(reject): %v", err)
	}
	if rejDec.Verdict != planapproval.VerdictReject {
		t.Fatalf("verdict=%s, quero reject (canal negou)", rejDec.Verdict)
	}
	if err := rejGuard.Spawn(ctx, rejPlan.RunID); !errors.Is(err, planapproval.ErrPlanNotApproved) {
		t.Fatalf("Spawn de plano rejeitado devia ser ErrPlanNotApproved, got %v", err)
	}
}

// (AOS-123) progress-surface: o prompt de exaustão apresenta EXACTAMENTE as 3 opções
// distintas (extend/summarize-stop/abort) e a ausência de resposta (timeout) DEGRADA —
// nunca morre em silêncio (fail-closed).
func TestUsability_ProgressSurface_ThreeUnambiguousOptionsTimeoutDegrades(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// EXACTAMENTE 3 opções, distintas e nomeadas — a decisão é inequívoca.
	opts := progresssurface.PromptOptions()
	if len(opts) != 3 {
		t.Fatalf("PromptOptions len=%d, quero EXACTAMENTE 3 (extend/summarize-stop/abort)", len(opts))
	}
	wantOrder := []progresssurface.ExhaustionOption{
		progresssurface.OptionExtend,
		progresssurface.OptionSummarizeStop,
		progresssurface.OptionAbort,
	}
	labels := map[string]bool{}
	for i, o := range opts {
		if o != wantOrder[i] {
			t.Fatalf("opção %d = %v, quero %v (ordem estável)", i, o, wantOrder[i])
		}
		if o.String() == "" || o.String() == "unset" {
			t.Fatalf("opção %d sem rótulo legível: %q", i, o.String())
		}
		if labels[o.String()] {
			t.Fatalf("opções ambíguas: rótulo %q repetido", o.String())
		}
		labels[o.String()] = true
	}
	if len(labels) != 3 {
		t.Fatalf("esperava 3 rótulos distintos, tenho %d", len(labels))
	}

	// Sem resposta → DEGRADA (fail-closed): OnPromptTimeout chama o Degrader com a razão
	// canónica, nunca um hard-stop cego nem silêncio.
	deg := &spyDegrader{}
	s := progresssurface.New(nil, nil, deg, nil, nil, progresssurface.WithRunID(uxdxRunID))
	if err := s.OnPromptTimeout(ctx); err != nil {
		t.Fatalf("OnPromptTimeout: %v", err)
	}
	calls, reason := deg.snapshot()
	if calls != 1 {
		t.Fatalf("o timeout devia degradar UMA vez, degradou %d", calls)
	}
	if reason != progresssurface.ReasonExhaustionPromptTimeout {
		t.Fatalf("razão de degradação=%q, quero %q", reason, progresssurface.ReasonExhaustionPromptTimeout)
	}

	// Contraprova fail-closed: SEM rede de degradação, a ausência de resposta é um erro de
	// composição (nunca uma morte em silêncio).
	silent := progresssurface.New(nil, nil, nil, nil, nil)
	if err := silent.OnPromptTimeout(ctx); !errors.Is(err, progresssurface.ErrNilDegrader) {
		t.Fatalf("sem Degrader o timeout devia ser ErrNilDegrader, got %v", err)
	}
}

// (AOS-125) autonomy-surface: o nível corrente é LEGÍVEL, as transições apresentam o
// seu MOTIVO, e uma demoção é comunicada IMEDIATAMENTE (não escondida).
func TestUsability_AutonomySurface_LevelReadableTransitionsReasonedDemotion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	reg := autonomy.NewLevelRegistry(autonomy.WithClock(fixedClock()))
	// Uma promoção e depois uma demoção seladas por AOS-089/090 (com motivo/actor).
	if _, err := reg.SetLevel(ctx, autonomyPeer, planDomain, autonomy.L3, "promocao sustentada", autonomy.ControllerActor); err != nil {
		t.Fatalf("SetLevel L3: %v", err)
	}
	if _, err := reg.SetLevel(ctx, autonomyPeer, planDomain, autonomy.L1, "anomalia override_rate_spike", autonomy.ControllerActor); err != nil {
		t.Fatalf("SetLevel L1: %v", err)
	}

	cfg, err := autonomy.NewAutonomyControlConfig("1.0.0", 0.02, 0.40, 30*24*time.Hour, 2, autonomy.L1, autonomy.L5)
	if err != nil {
		t.Fatalf("NewAutonomyControlConfig: %v", err)
	}
	s, err := autonomysurface.New(reg, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Nível LEGÍVEL: Current == o que AOS-089 registou; String() não-vazio.
	v := s.BuildLevelView(ctx, autonomyPeer, planDomain)
	if v.Current != reg.LevelFor(autonomyPeer, planDomain) {
		t.Fatalf("Current=%s não reflecte o registo=%s", v.Current, reg.LevelFor(autonomyPeer, planDomain))
	}
	if v.Current.String() == "" {
		t.Fatal("nível corrente sem rótulo legível")
	}

	// Transições com MOTIVO: cada uma explica a decisão de AOS-090; a última é demoção.
	if len(v.Transitions) != 2 {
		t.Fatalf("nº de transições=%d, quero 2 (lidas do histórico)", len(v.Transitions))
	}
	for i, tr := range v.Transitions {
		if tr.Reason == "" {
			t.Fatalf("transição %d sem motivo: a superfície tem de EXPLICAR cada transição", i)
		}
	}
	if !v.Transitions[1].IsDemotion() {
		t.Fatal("a última transição (L3->L1) devia ser marcada como demoção")
	}

	// Demoção IMEDIATA e clara: NotifyLevelChange expõe From>To e o motivo, sem ocultar.
	now := fixedClock()()
	demotion := autonomy.LevelChange{
		Agent:  autonomyPeer,
		Domain: planDomain,
		Old:    autonomy.L4,
		New:    autonomy.L2,
		Reason: "anomalia unsafe_action: democao determinista L4->L2",
		Actor:  autonomy.ControllerActor,
		At:     now,
	}
	notice, ok := s.NotifyLevelChange(ctx, demotion)
	if !ok {
		t.Fatal("NotifyLevelChange devia reconhecer a demoção (To<From)")
	}
	if notice.From != autonomy.L4 || notice.To != autonomy.L2 {
		t.Fatalf("aviso=%s->%s, quero L4->L2 sem ocultação", notice.From, notice.To)
	}
	if notice.To >= notice.From {
		t.Fatal("uma demoção tem To<From (rebaixamento claro)")
	}
	if notice.Reason == "" || notice.Reason != demotion.Reason {
		t.Fatalf("Reason=%q, quero o motivo selado (não escondido)", notice.Reason)
	}
	if notice.At != now {
		t.Fatalf("At=%v, quero o instante da demoção (imediato)", notice.At)
	}
}

// samplePlan devolve um grafo de tarefas multi-nó determinista (a→b, a→c, b→d) com a
// classe dada. Dados sintéticos: sem PII/segredos reais.
func samplePlan(runID string, class risk.Class) planapproval.Plan {
	return planapproval.Plan{
		RunID:  runID,
		Agent:  planAgent,
		Domain: planDomain,
		Nodes: []planapproval.PlanNode{
			{TaskID: "a", Class: class, Preview: "cap:http.get -> https://x/a", Capability: "http.get", Resource: "https://x/a"},
			{TaskID: "b", Class: class, Preview: "cap:http.get -> https://x/b", Capability: "http.get", Resource: "https://x/b"},
			{TaskID: "c", Class: class, Preview: "cap:http.get -> https://x/c", Capability: "http.get", Resource: "https://x/c"},
			{TaskID: "d", Class: class, Preview: "cap:http.get -> https://x/d", Capability: "http.get", Resource: "https://x/d"},
		},
		Edges: [][2]string{{"a", "b"}, {"a", "c"}, {"b", "d"}},
	}
}
