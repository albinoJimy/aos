package planmigrate_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planmigrate"
	pe "github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// Sondas de PUREZA do replay. O REG e o RM são injectados no Migrator; a via de
// replay TEM de os deixar intactos. Estes duplos ENVENENADOS falham o teste
// (t.Errorf) no instante em que forem tocados E contam as chamadas, para uma
// asserção adicional de contador==0. Uma via de replay que re-resolvesse o REG ou
// re-atravessasse o RM disparava-os.
// ---------------------------------------------------------------------------

type failResolver struct {
	t     *testing.T
	calls atomic.Int64
}

func (f *failResolver) Resolve(ctx context.Context, ref plan.ToolRef) error {
	f.calls.Add(1)
	f.t.Errorf("REG.Resolve chamado durante o replay (tool=%q) — o replay TEM de ser puro (nunca re-resolve o REG)", ref.Name)
	return errors.New("planmigrate_test: REG envenenado nao devia ser chamado no replay")
}

type failMonitor struct {
	t     *testing.T
	calls atomic.Int64
}

func (f *failMonitor) Mediate(ctx context.Context, nodeID string, tools []plan.ToolRef) error {
	f.calls.Add(1)
	f.t.Errorf("RM.Mediate chamado durante o replay (node=%q) — o replay TEM de ser puro (nunca re-atravessa o RM)", nodeID)
	return errors.New("planmigrate_test: RM envenenado nao devia ser chamado no replay")
}

// ---------------------------------------------------------------------------
// Duplos que CONTAM (e permitem) — provam que o REG/RM SÃO alcançáveis na via de
// ESCRITA (Materialize). Sem isto, um contador==0 no replay seria vacuoso (podia
// ser 0 só porque o REG/RM nunca são chamados por ninguém).
// ---------------------------------------------------------------------------

type countingResolver struct{ calls atomic.Int64 }

func (c *countingResolver) Resolve(ctx context.Context, ref plan.ToolRef) error {
	c.calls.Add(1)
	return nil
}

type countingMonitor struct{ calls atomic.Int64 }

func (c *countingMonitor) Mediate(ctx context.Context, nodeID string, tools []plan.ToolRef) error {
	c.calls.Add(1)
	return nil
}

// countingProposer é o LLM espião da CAPTURA: consultado UMA vez ao gravar
// `plan.proposed`. O contador prova que o replay não re-chama o modelo (fica em 1).
type countingProposer struct {
	hash  string
	meta  pe.PlannerMeta
	calls atomic.Int64
}

func (p *countingProposer) Propose(ctx context.Context) (string, pe.PlannerMeta, error) {
	p.calls.Add(1)
	return p.hash, p.meta, nil
}

// ---------------------------------------------------------------------------
// Construtores de fixtures.
// ---------------------------------------------------------------------------

func newStore(t *testing.T) *eventstore.Store {
	t.Helper()
	s, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustPolicy(t *testing.T, w planmigrate.SupportWindow) planmigrate.Policy {
	t.Helper()
	p, err := planmigrate.NewPolicy(w)
	if err != nil {
		t.Fatalf("NewPolicy(%+v): %v", w, err)
	}
	return p
}

// buildDoc devolve um PlanDocument válido na versão dada, com 2 nós e 3 tools no
// total (n1: 1 tool, n2: 2 tools) — cardinalidades que os testes de contador usam.
func buildDoc(version plan.PlanVersion) plan.PlanDocument {
	return plan.PlanDocument{
		PlanVersion: version,
		Objective:   "objectivo meta do run",
		BudgetTotal: plan.BudgetEstimate{Tokens: 1000, CostMicroUSD: 500},
		PlannerMeta: plan.PlannerMeta{
			Model:            "model-x",
			PromptVersion:    "prompt-v3",
			CapabilitiesHash: "sha256:caps-7",
		},
		Nodes: []plan.Node{
			{
				NodeID: "n1", Role: "researcher", Objective: "pesquisar",
				Tools:     []plan.ToolRef{{Name: "tool:read", Version: "1.0.0", Digest: "sha256:d1"}},
				RiskClass: plan.RiskSafe,
			},
			{
				NodeID: "n2", Role: "writer", Objective: "escrever",
				Tools: []plan.ToolRef{
					{Name: "tool:write", Version: "2.0.0", Digest: "sha256:d2"},
					{Name: "tool:egress", Version: "1.0.0", Digest: "sha256:d3"},
				},
				DependsOn: []string{"n1"},
				RiskClass: plan.RiskGray,
			},
		},
	}
}

// seedApproval emite a captura até à APROVAÇÃO (proposed→validated→approved) no
// stream do plano, com o hash aprovado = HashPlan(doc) e o planner_meta = metaOverride
// (por omissão, o do doc). Devolve o hash aprovado e o proposer espião.
func seedApproval(t *testing.T, store pe.Appender, planID string, doc plan.PlanDocument, metaOverride *pe.PlannerMeta) (string, *countingProposer) {
	t.Helper()
	ctx := context.Background()
	hash, err := planmigrate.HashPlan(doc)
	if err != nil {
		t.Fatalf("HashPlan: %v", err)
	}
	meta := pe.PlannerMeta{
		Model:            doc.PlannerMeta.Model,
		PromptVersion:    doc.PlannerMeta.PromptVersion,
		CapabilitiesHash: doc.PlannerMeta.CapabilitiesHash,
	}
	if metaOverride != nil {
		meta = *metaOverride
	}
	rec, err := pe.NewRecorder(store)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	proposer := &countingProposer{hash: hash, meta: meta}
	if _, err := rec.RecordProposedFrom(ctx, planID, 1, proposer); err != nil {
		t.Fatalf("RecordProposedFrom: %v", err)
	}
	if _, err := rec.RecordValidated(ctx, pe.ValidatedPayload{
		PlanID: planID, PlanHash: hash, NodeCount: len(doc.Nodes),
		BudgetTotal: 1000, MaxDepth: 2, MaxFanout: 2, MaxNodes: 10,
	}); err != nil {
		t.Fatalf("RecordValidated: %v", err)
	}
	if _, err := rec.RecordDecision(ctx, pe.DecisionPayload{
		PlanID: planID, PlanHash: hash, Decision: pe.DecisionApproved, DecisionRef: "hitl:chan:1",
	}); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	return hash, proposer
}

// seedMaterialized emite `plan.materialized` para o hash dado, mapeando cada nó do
// doc para uma folha com os nomes das suas tools.
func seedMaterialized(t *testing.T, store pe.Appender, planID string, doc plan.PlanDocument, hash string) {
	t.Helper()
	rec, err := pe.NewRecorder(store)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	nodes := make([]pe.MaterializedNode, 0, len(doc.Nodes))
	for _, n := range doc.Nodes {
		tools := make([]string, 0, len(n.Tools))
		for _, tr := range n.Tools {
			tools = append(tools, tr.Name)
		}
		nodes = append(nodes, pe.MaterializedNode{NodeID: n.NodeID, Kind: pe.SpawnLeaf, Tools: tools})
	}
	if _, err := rec.RecordMaterialized(context.Background(), pe.MaterializedPayload{
		PlanID: planID, PlanHash: hash, Nodes: nodes,
	}); err != nil {
		t.Fatalf("RecordMaterialized: %v", err)
	}
}
