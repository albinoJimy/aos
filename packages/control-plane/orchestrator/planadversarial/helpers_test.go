package planadversarial

import (
	"context"
	"testing"

	"github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plandispatch"
	"github.com/aos-ref/control-plane/orchestrator/planvalidate"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// capHash é o carimbo de snapshot pinado usado pelos docs de teste: o
// `capabilities_hash` do documento tem de casar com o Hash do snapshot para o
// plano atravessar a regra 1 (binding fail-closed) e chegar às regras que o
// vector ataca.
const capHash = "cap-hash-adv-244"

// ---- FIXTURES DE PLANO (dados untrusted, ADR-005) --------------------------

// benignTool é uma tool resolúvel e ADMISSÍVEL no snapshot base.
func benignTool() plan.ToolRef {
	return plan.ToolRef{Name: "search", Version: "1.0.0", Digest: "sha256:search"}
}

// baseValidDoc é um plano VÁLIDO na forma e nas regras 1–4: cadeia a<-b<-c com a
// tool `search` resolúvel. Serve de ponto de partida honesto — qualquer rejeição
// num vector vem do ataque, não de um doc partido à partida.
func baseValidDoc() plan.PlanDocument {
	return plan.PlanDocument{
		PlanVersion: plan.CurrentPlanVersion,
		Objective:   "objetivo de topo benigno",
		BudgetTotal: plan.BudgetEstimate{Tokens: 30, CostMicroUSD: 30},
		PlannerMeta: plan.PlannerMeta{
			Model:            "modelo-x",
			PromptVersion:    "1.2.3",
			CapabilitiesHash: capHash,
		},
		Nodes: []plan.Node{
			{NodeID: "a", Role: "r", Objective: "o-a", Tools: []plan.ToolRef{benignTool()}, BudgetEstimate: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 10}},
			{NodeID: "b", Role: "r", Objective: "o-b", DependsOn: []string{"a"}, BudgetEstimate: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 10}},
			{NodeID: "c", Role: "r", Objective: "o-c", DependsOn: []string{"b"}, BudgetEstimate: plan.BudgetEstimate{Tokens: 10, CostMicroUSD: 10}},
		},
	}
}

// advSnapshot é o snapshot PINADO. `search` admissível; `blocked` existe mas é
// INADMISSÍVEL (over-privilege que o over-privileged plan tenta usar); `delete`
// admissível mas com eixos de risco irreversível+externo+sensível (deriva danger).
func advSnapshot() planvalidate.Snapshot {
	return planvalidate.Snapshot{
		Hash: capHash,
		Tools: []planvalidate.Capability{
			{Name: "search", Version: "1.0.0", Digest: "sha256:search", Admissible: true,
				Sensitivity: risk.SensitivityPublic, Egress: risk.EgressNone, Reversibility: risk.Reversible},
			{Name: "blocked", Version: "1.0.0", Digest: "sha256:blocked", Admissible: false},
			{Name: "delete", Version: "1.0.0", Digest: "sha256:delete", Admissible: true,
				Sensitivity: risk.SensitivitySensitive, Egress: risk.EgressExternal, Reversibility: risk.Irreversible},
		},
	}
}

// deleteTool refere a capability irreversível `delete` do snapshot.
func deleteTool() plan.ToolRef {
	return plan.ToolRef{Name: "delete", Version: "1.0.0", Digest: "sha256:delete"}
}

// genrousCeilings desliga os tectos estruturais (para isolar o vector sob teste
// dos tectos de exaustão, que têm o seu próprio teste).
func generousCeilings() planvalidate.Ceilings {
	return planvalidate.Ceilings{MaxNodes: 1000, MaxDepth: 1000, MaxFanout: 1000}
}

// echoPricer re-preça cada nó pelo seu próprio `budget_estimate` declarado —
// divergência sempre zero. Isola o vector do ataque de divergência de custo
// (coberto pela regra 5, que não é o alvo destes testes salvo o breaker por-nó).
type echoPricer struct{}

func (echoPricer) Price(n plan.Node, _ plan.PlannerMeta) budget.Amount {
	return budget.Amount{Tokens: int64(n.BudgetEstimate.Tokens), CostMicroUSD: int64(n.BudgetEstimate.CostMicroUSD)}
}

// resPolicy é uma política de recursos benigna: echoPricer, raiz generosa, sem
// teto por-nó nem tolerância exigida. Deixa a regra 6 (risco) ser o único
// determinante do resultado — o que o teste de downgrade precisa.
func resPolicy() planvalidate.ResourcePolicy {
	return planvalidate.ResourcePolicy{
		Budget: planvalidate.BudgetPolicy{
			Pricer:        echoPricer{},
			RootRemaining: budget.Amount{Tokens: 1_000_000, CostMicroUSD: 1_000_000},
			NodeCeiling:   budget.Amount{}, // desligado
			Tolerance:     budget.Amount{},
		},
	}
}

// ---- SPIES DE DESPACHO (plandispatch: portas reais) ------------------------

// fixedGate reporta um estado de materialização FIXO. Modela a autoridade do gate
// (AOS-237): só materializa planos aprovados. Nos testes ligamos `materialized`
// ao resultado da validação real — um plano rejeitado nunca é materializado.
type fixedGate struct{ materialized bool }

func (g fixedGate) Materialized(context.Context, string) (bool, error) { return g.materialized, nil }

// allPendingLifecycle reporta TODOS os nós como pendentes (candidatos a despacho):
// remove qualquer razão de espera que não seja o gate/headroom sob teste.
type allPendingLifecycle struct{}

func (allPendingLifecycle) State(context.Context, string, string) (plandispatch.NodeState, error) {
	return plandispatch.NodePending, nil
}

// grantingHeadroom concede sempre um slot (Acquire=true): usado quando o teste quer
// que o ÚNICO travão seja o gate.
type grantingHeadroom struct{}

func (grantingHeadroom) Available(context.Context) (int, error) { return 1_000, nil }
func (grantingHeadroom) Acquire(context.Context) (bool, error)  { return true, nil }
func (grantingHeadroom) Release(context.Context) error          { return nil }

// pressuredHeadroom nega SEMPRE o slot (Acquire=false): o circuit breaker de
// concorrência (AOS-028/029) — adia todos os elegíveis, nunca oversubscreve.
type pressuredHeadroom struct{}

func (pressuredHeadroom) Available(context.Context) (int, error) { return 0, nil }
func (pressuredHeadroom) Acquire(context.Context) (bool, error)  { return false, nil }
func (pressuredHeadroom) Release(context.Context) error          { return nil }

// clearingCards resolve sempre o cartão (não é o eixo sob teste aqui).
type clearingCards struct{}

func (clearingCards) Cleared(context.Context, string, string) (bool, error) { return true, nil }

// blockingCards NUNCA resolve o cartão (Cleared=false): modela um nó `danger` cujo
// approval-card ainda não foi dado pelo gate humano (AOS-236). Um nó com
// RequiresCard=true fica em espera de cartão e NÃO despacha.
type blockingCards struct{}

func (blockingCards) Cleared(context.Context, string, string) (bool, error) { return false, nil }

// spySink CONTA e regista cada despacho efectivo (contador + ids). É o «efeito
// indevido» que os testes provam NÃO acontecer: se um plano hostil, ou um nó `danger`
// sem cartão, chegasse ao spawn, o contador subiria e o id apareceria em `nodes`.
type spySink struct {
	calls int
	nodes []string
}

func (s *spySink) Dispatch(_ context.Context, n plandispatch.Node) error {
	s.calls++
	s.nodes = append(s.nodes, n.NodeID)
	return nil
}

// dispatched indica se um dado node_id foi entregue ao sink nesta passagem.
func (s *spySink) dispatched(nodeID string) bool {
	for _, id := range s.nodes {
		if id == nodeID {
			return true
		}
	}
	return false
}

// dispatchPlanFrom projecta um plandispatch.Plan a partir dos nós de um documento,
// preservando as arestas depends_on. Content-free (só ids + arestas), como a
// projecção real do que o ORQ materializa.
func dispatchPlanFrom(doc plan.PlanDocument) plandispatch.Plan {
	nodes := make([]plandispatch.Node, 0, len(doc.Nodes))
	for _, n := range doc.Nodes {
		nodes = append(nodes, plandispatch.Node{
			NodeID:    n.NodeID,
			DependsOn: append([]string(nil), n.DependsOn...),
		})
	}
	return plandispatch.Plan{PlanID: "plan-adv", Nodes: nodes}
}

// mustDecode afirma que doc passa a FORMA (plan.Decode). É o que fixa «plano como
// DADOS»: o documento hostil é sintacticamente legítimo — a defesa NÃO é o parser,
// é a validação semântica/de risco a jusante.
func mustDecode(t *testing.T, doc plan.PlanDocument) {
	t.Helper()
	raw, err := plan.Encode(doc)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if _, err := plan.Decode(raw); err != nil {
		t.Fatalf("doc hostil devia passar a FORMA (é dados válidos), mas Decode rejeitou: %v", err)
	}
}
