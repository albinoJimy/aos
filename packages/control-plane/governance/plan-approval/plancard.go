package planapproval

import (
	"encoding/json"

	approvalcard "github.com/aos-ref/control-plane/governance/approval-card"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// CostEstimate é o custo AGREGADO estimado do plano inteiro, exibido no [PlanCard]
// (opcional). É um enriquecimento de apresentação — o gate não o calcula; o wiring
// popula-o a partir da agregação do scheduler/otel-genai. Serve para o humano ponderar
// o custo de TODO o plano ANTES do spawn (o ponto de AOS-121).
type CostEstimate struct {
	// EstimatedTokens é o custo previsto do plano em tokens.
	EstimatedTokens int64 `json:"estimated_tokens"`
	// MicroUSD é o custo previsto do plano em micro-USD inteiro.
	MicroUSD int64 `json:"micro_usd"`
}

// PlanCard é o MODELO CANÓNICO, serializável e versionado do PLANO proposto (AC3). É a
// COMPOSIÇÃO de:
//   - []approvalcard.ApprovalCard — UM card por nó (via [approvalcard.BuildCard]), o
//     efeito CONCRETO por nó JÁ redigido (reutiliza AOS-120, não reimplementa);
//   - a TOPOLOGIA do grafo — a ordem topológica estável, as arestas, a contagem de nós,
//     a classe AGREGADA (a mais severa) e o custo agregado.
//
// Opera sobre o GRAFO, não sobre uma tool call — é o que o distingue do [ApprovalCard]
// por-acção (AC3). Carimba a sua PRÓPRIA versão/domínio (aos.plan.card.v1), separado do
// approval-card (aos.approval.card.v1).
type PlanCard struct {
	// SchemaVersion é a versão SemVer do contrato deste plan-card (AC5), carimbada no wire.
	SchemaVersion PlanCardSchemaVersion
	// RunID liga o card ao run.
	RunID string
	// Agent é o agente-raiz do plano (o solicitante).
	Agent string
	// Domain é o domínio de autonomia do plano.
	Domain string
	// Order é a ordenação topológica ESTÁVEL dos task_ids (o "plano" reproduzível).
	Order []string
	// Edges são as arestas de dependência From→To.
	Edges [][2]string
	// NodeCards são os cards por-nó, na MESMA ordem de [Order] (o efeito concreto de
	// cada nó, já redigido).
	NodeCards []approvalcard.ApprovalCard
	// AggregateClass é a classe de risco AGREGADA (a mais severa dos nós). Governa o
	// gate humano vs. auto-aprovação (via [autonomy.Oversight]).
	AggregateClass risk.Class
	// AggregateIrreversible indica se algum nó é irreversível (motiva escrutínio reforçado).
	AggregateIrreversible bool
	// EstimatedCost é o custo agregado estimado do plano, opcional (apresentação).
	EstimatedCost *CostEstimate
	// NodeReviews é a TRIAGEM por-nó (na MESMA ordem de [Order]): quais nós exigem revisão
	// FORÇADA item-a-item (Class >= gray ou capability_gap) e quais são colapsáveis. O
	// custo POR RAMO de cada nó vive no card por-nó correspondente ([NodeCards][i].
	// EstimatedCost) — "custo por ramo visível no card".
	NodeReviews []NodeReview
}

// NodeCount devolve o número de nós do plano.
func (c PlanCard) NodeCount() int { return len(c.Order) }

// planCardConfig acumula as opções de [BuildPlanCard].
type planCardConfig struct {
	cost      *CostEstimate
	nodeCosts map[string]*CostEstimate
}

// BuildOption configura [BuildPlanCard].
type BuildOption func(*planCardConfig)

// WithEstimatedCost enriquece o plan-card com o custo AGREGADO estimado do plano.
func WithEstimatedCost(tokens, microUSD int64) BuildOption {
	return func(c *planCardConfig) { c.cost = &CostEstimate{EstimatedTokens: tokens, MicroUSD: microUSD} }
}

// WithNodeCost fixa o custo POR RAMO (por-nó) de um task_id, exibido no card por-nó
// correspondente ([PlanCard.NodeCards][i].EstimatedCost). É o threading por-nó do custo
// quando este vem de FORA do nó (agregação por-subárvore do wiring); TEM precedência
// sobre o [PlanNode.Cost] embutido. CA/DoD: "custo por ramo visível no card".
func WithNodeCost(taskID string, tokens, microUSD int64) BuildOption {
	return func(c *planCardConfig) {
		if c.nodeCosts == nil {
			c.nodeCosts = make(map[string]*CostEstimate)
		}
		c.nodeCosts[taskID] = &CostEstimate{EstimatedTokens: tokens, MicroUSD: microUSD}
	}
}

// nodeCostFor resolve o custo por-ramo efectivo de um nó: a opção [WithNodeCost] tem
// precedência sobre o [PlanNode.Cost] embutido. Nil ⇒ sem custo por-ramo para o nó.
func nodeCostFor(cfg planCardConfig, n PlanNode) *CostEstimate {
	if cfg.nodeCosts != nil {
		if c, ok := cfg.nodeCosts[n.TaskID]; ok {
			return c
		}
	}
	return n.Cost
}

// BuildPlanCard constrói o modelo canónico do plano A PARTIR de um [Plan]: valida o
// grafo, computa a ordenação topológica estável (rejeita ciclos), e constrói UM
// [approvalcard.ApprovalCard] por nó (na ordem topológica) via [approvalcard.BuildCard]
// — LENDO Class/Irreversible/Preview/Capability/Resource de cada nó, sem reclassificar
// (compõe AOS-120). O card resultante é VALIDADO (fail-closed).
//
// Cada card por-nó recebe um request-id determinista "<run_id>#<task_id>" (a
// apresentação por-nó dentro do plano) e o Principal do par (agente do nó / do plano).
func BuildPlanCard(plan Plan, opts ...BuildOption) (PlanCard, error) {
	if err := plan.Validate(); err != nil {
		return PlanCard{}, err
	}
	order, err := plan.TopoOrder()
	if err != nil {
		return PlanCard{}, err
	}

	cfg := planCardConfig{}
	for _, o := range opts {
		o(&cfg)
	}

	byID := make(map[string]PlanNode, len(plan.Nodes))
	for _, n := range plan.Nodes {
		byID[n.TaskID] = n
	}

	cards := make([]approvalcard.ApprovalCard, 0, len(order))
	for _, id := range order {
		n := byID[id]
		agent, _ := plan.nodeAgentDomain(n)
		req := risk.ConfirmationRequest{
			Class:        n.Class,
			Irreversible: n.Irreversible,
			Preview:      n.Preview,
			Principal:    agent,
			Capability:   n.Capability,
			Resource:     n.Resource,
		}
		cardOpts := []approvalcard.BuildOption{approvalcard.WithRequestID(plan.RunID + "#" + id)}
		// Custo POR RAMO: enfia o custo por-nó (opção ou embutido) no card por-nó, para
		// que "custo por ramo" fique visível no card (NodeCards[i].EstimatedCost).
		if cost := nodeCostFor(cfg, n); cost != nil {
			cardOpts = append(cardOpts, approvalcard.WithEstimatedCost(cost.EstimatedTokens, cost.MicroUSD))
		}
		card, cerr := approvalcard.BuildCard(req, cardOpts...)
		if cerr != nil {
			return PlanCard{}, cerr
		}
		cards = append(cards, card)
	}

	pc := PlanCard{
		SchemaVersion:         CurrentVersion,
		RunID:                 plan.RunID,
		Agent:                 plan.Agent,
		Domain:                plan.Domain,
		Order:                 order,
		Edges:                 append([][2]string(nil), plan.Edges...),
		NodeCards:             cards,
		AggregateClass:        aggregateClass(plan.Nodes),
		AggregateIrreversible: aggregateIrreversible(plan.Nodes),
		EstimatedCost:         cfg.cost,
		NodeReviews:           buildNodeReviews(order, byID),
	}
	if verr := pc.Validate(); verr != nil {
		return PlanCard{}, verr
	}
	return pc, nil
}

// Validate impõe as invariantes fail-closed do plan-card (AC5):
//   - versão de schema compatível com [CurrentVersion] (MESMO MAJOR), senão
//     [ErrIncompatibleSchema];
//   - RunID e Agent não-vazios, senão [ErrInvalidPlanCard];
//   - contagem coerente: um card por nó da ordem (len(NodeCards) == len(Order));
//   - cada card por-nó válido (delega em [approvalcard.ApprovalCard.Validate]);
//   - coerência do agregado irreversível: se AggregateIrreversible, ALGUM card por-nó
//     tem de o ser (o rótulo agregado não pode contradizer os cards que embrulha).
func (c PlanCard) Validate() error {
	if !CurrentVersion.Compatible(c.SchemaVersion) {
		return ErrIncompatibleSchema
	}
	if c.RunID == "" || c.Agent == "" {
		return ErrInvalidPlanCard
	}
	if len(c.NodeCards) != len(c.Order) {
		return ErrInvalidPlanCard
	}
	anyIrrev := false
	for i := range c.NodeCards {
		if err := c.NodeCards[i].Validate(); err != nil {
			return err
		}
		if c.NodeCards[i].Irreversible {
			anyIrrev = true
		}
	}
	if c.AggregateIrreversible && len(c.NodeCards) > 0 && !anyIrrev {
		return ErrInvalidPlanCard
	}
	// Coerência da triagem (aditiva/retrocompatível): quando presente, há uma entrada de
	// revisão por nó da ordem. Ausente (nil, ex.: card de wire antigo) é tolerado.
	if len(c.NodeReviews) != 0 && len(c.NodeReviews) != len(c.Order) {
		return ErrInvalidPlanCard
	}
	return nil
}

// planCardWire é a forma serializada ESTÁVEL do plan-card: schema_version carimbado, a
// classe agregada como rótulo textual canónico, a topologia e os cards por-nó (cada um
// na sua própria forma de wire de AOS-120). É o contrato que os adaptadores consomem.
type planCardWire struct {
	SchemaVersion         string                      `json:"schema_version"`
	RunID                 string                      `json:"run_id"`
	Agent                 string                      `json:"agent"`
	Domain                string                      `json:"domain"`
	Order                 []string                    `json:"order"`
	Edges                 [][2]string                 `json:"edges"`
	NodeCards             []approvalcard.ApprovalCard `json:"node_cards"`
	NodeCount             int                         `json:"node_count"`
	AggregateClass        string                      `json:"aggregate_class"`
	AggregateIrreversible bool                        `json:"aggregate_irreversible"`
	EstimatedCost         *CostEstimate               `json:"estimated_cost,omitempty"`
	NodeReviews           []NodeReview                `json:"node_reviews,omitempty"`
}

// MarshalJSON serializa o plan-card na forma de wire estável, carimbando a
// schema_version e o rótulo canónico da classe agregada. A contagem de nós é derivada
// e emitida para consumo directo pelos adaptadores.
func (c PlanCard) MarshalJSON() ([]byte, error) {
	return json.Marshal(planCardWire{
		SchemaVersion:         c.SchemaVersion.String(),
		RunID:                 c.RunID,
		Agent:                 c.Agent,
		Domain:                c.Domain,
		Order:                 c.Order,
		Edges:                 c.Edges,
		NodeCards:             c.NodeCards,
		NodeCount:             c.NodeCount(),
		AggregateClass:        c.AggregateClass.String(),
		AggregateIrreversible: c.AggregateIrreversible,
		EstimatedCost:         c.EstimatedCost,
		NodeReviews:           c.NodeReviews,
	})
}

// UnmarshalJSON desserializa a forma de wire e VALIDA fail-closed: uma versão malformada
// ou um MAJOR incompatível é rejeitado. O rótulo da classe agregada é reparseado para a
// representação interna (fail-closed: desconhecido ⇒ danger, o pior caso).
func (c *PlanCard) UnmarshalJSON(data []byte) error {
	var w planCardWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	ver, err := ParsePlanCardSchemaVersion(w.SchemaVersion)
	if err != nil {
		return err
	}
	out := PlanCard{
		SchemaVersion:         ver,
		RunID:                 w.RunID,
		Agent:                 w.Agent,
		Domain:                w.Domain,
		Order:                 w.Order,
		Edges:                 w.Edges,
		NodeCards:             w.NodeCards,
		AggregateClass:        parseClass(w.AggregateClass),
		AggregateIrreversible: w.AggregateIrreversible,
		EstimatedCost:         w.EstimatedCost,
		NodeReviews:           w.NodeReviews,
	}
	if verr := out.Validate(); verr != nil {
		return verr
	}
	*c = out
	return nil
}

// parseClass reparseia o rótulo textual de classe na representação interna. Fail-closed:
// um rótulo desconhecido resolve [risk.ClassDanger] (o valor-zero, o pior caso),
// espelhando a semântica de [risk.Class].
func parseClass(s string) risk.Class {
	switch s {
	case "safe":
		return risk.ClassSafe
	case "gray":
		return risk.ClassGray
	default:
		return risk.ClassDanger
	}
}
