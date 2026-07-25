package scheduler

import (
	"context"
	"errors"
	"time"
)

// DegradationAction é uma acção de degradação graciosa.
type DegradationAction string

const (
	// ActionShed descarta trabalho opcional/baixa prioridade.
	ActionShed DegradationAction = "shed"
	// ActionDefer adia trabalho admissível preservando-o.
	ActionDefer DegradationAction = "defer"
	// ActionDowngrade encaminha para um tier de modelo mais barato.
	ActionDowngrade DegradationAction = "downgrade"
	// ActionReject rejeita o trabalho como último recurso.
	ActionReject DegradationAction = "reject"
)

// ModelTier é um degrau da escada de tiers: um nome lógico de tier, o modelo
// concreto e o CostRank (menor = mais barato). A escada ordena-se por CostRank.
type ModelTier struct {
	// Tier é o nome lógico do tier (ex.: "premium", "standard", "economy").
	Tier string
	// Model é o identificador do modelo concreto do tier.
	Model string
	// CostRank ordena a escada por custo: quanto MENOR, mais barato. O downgrade
	// desce para o CostRank imediatamente inferior ao corrente.
	CostRank int
}

// TierRouteRequest é o pedido à porta [ModelTierRouter]: dado o tier corrente,
// devolver o tier imediatamente mais barato. Class/Tenant permitem roteamento por
// classe/tenant na impl de produção (o Model Gateway); a impl de referência
// usa uma única escada.
type TierRouteRequest struct {
	Key          ProviderKey
	Tenant       string
	Class        string
	CurrentTier  string
	CurrentModel string
}

// TierRouteDecision é a resposta da porta: se há um tier mais barato e qual.
type TierRouteDecision struct {
	// Downgraded indica se existe um tier mais barato para onde descer. false
	// quando o corrente já é o mais barato (não há para onde degradar).
	Downgraded bool
	FromTier   string
	ToTier     string
	FromModel  string
	ToModel    string
}

// ModelTierRouter é a PORTA que encaminha trabalho para um tier de modelo mais
// barato (cost-aware model tiering). O Model Gateway (EPIC-06) é o implementador
// de produção; NÃO é implementado aqui.
type ModelTierRouter interface {
	// Cheaper devolve o tier imediatamente mais barato que o corrente, ou
	// Downgraded=false se o corrente já é o mais barato. Determinística.
	Cheaper(ctx context.Context, req TierRouteRequest) (TierRouteDecision, error)
}

// DegradationItem é a unidade de trabalho sujeita a degradação.
type DegradationItem struct {
	ID           string
	Tenant       string
	Priority     string
	Class        string
	Critical     bool
	Optional     bool
	Deferrable   bool
	Irreversible bool
	CurrentTier  string
	CurrentModel string
	Key          ProviderKey
}

// DegradationTrigger descreve o GATILHO da degradação.
type DegradationTrigger struct {
	Reason        string
	PolicyVersion string
	Partition     string
	FillRatio     float64
	Depth         int
	Capacity      int
}

// DegradationResult é o veredicto de uma acção de degradação executada.
type DegradationResult struct {
	Action     DegradationAction
	ItemID     string
	Applied    bool
	RetryAfter time.Duration
	FromTier   string
	ToTier     string
	FromModel  string
	ToModel    string
	Reversible bool
	Reason     string
}

// Degrader executa as acções de degradação. A implementação vive no
// control-plane/scheduler; os adaptadores usam a interface quando necessário.
type Degrader interface {
	Execute(ctx context.Context, action DegradationAction, item DegradationItem, trigger DegradationTrigger) (DegradationResult, error)
}

// Sentinelas de erro do executor (comparáveis por errors.Is — fail-closed).
var (
	ErrCannotShedCritical       = errors.New("scheduler: shed recusado — trabalho crítico nunca é descartado (fail-closed)")
	ErrWorkRejected             = errors.New("scheduler: trabalho rejeitado — sistema saturado, sem degrau de degradação aplicável")
	ErrUnknownDegradationAction = errors.New("scheduler: acção de degradação desconhecida")
	ErrChainExhausted           = errors.New("scheduler: cadeia de degradação esgotada sem degrau aplicável")
	ErrCannotShedIrreversible   = errors.New("scheduler: shed recusado — trabalho irreversível nunca é descartado (fail-closed)")
	ErrCannotShedNonOptional    = errors.New("scheduler: shed recusado — trabalho não marcado como opcional (fail-closed)")
	ErrMissingReason            = errors.New("scheduler: degradação recusada — gatilho sem razão (fail-closed)")
	ErrDegradationNotApplied    = errors.New("scheduler: acção de degradação seleccionada não teve efeito — escalar")
)
