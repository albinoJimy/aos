// Package degradation é a POLÍTICA DECLARATIVA de degradação graciosa do Model
// Gateway (AOS-059, tecnica/06 §6, ADR-008) e a porta de ORÇAMENTO que sustenta a
// exaustão graciosa a ~80% do orçamento.
//
// # Fronteira de responsabilidade (crítica — ler antes de mexer)
//
// A CADEIA shed → defer → degradar → rejeitar é DO ESCALONADOR (AOS-031,
// packages/control-plane/scheduler): é ele que EXECUTA cada degrau, garante a
// reversibilidade, emite os eventos append-only e mantém o replay fiel. Este
// pacote NÃO reimplementa essa cadeia. O Gateway é dono apenas da ESCOLHA de tier
// (routing/tiering) e da OFERTA de degradação: exprime a política como DADOS (a
// ordem de preferência, o limiar de exaustão) e, sob pressão de orçamento,
// OFERECE a descida para um tier mais barato como CONTINUAÇÃO — nunca um hard-stop
// cego. O Escalonador consome a escolha de tier (via scheduler.ModelTierRouter) e
// conduz a cadeia.
//
// # Sinal de orçamento por PORTA (não a contabilidade real)
//
// O orçamento é lido por uma PORTA [BudgetProvider] com impl de referência
// determinística ([StaticBudgetProvider]) — a contabilidade REAL de custos em USD
// é AOS-062, NÃO implementada aqui. A ~80% do orçamento a política oferece
// degradação (exaustão graciosa); a 100% continua a OFERECER degradar (enquanto
// houver tier mais barato) em vez de parar cegamente.
//
// # Determinismo
//
// Sem relógio nem aleatoriedade: a decisão é função pura do estado de orçamento e
// da política declarativa. Serialização estável (a ordem é uma slice, não um mapa).
package degradation

import "context"

// Action é um degrau da cadeia de degradação, em forma de DADO. Os valores
// espelham EXACTAMENTE a cadeia canónica do Escalonador (scheduler.DegradationAction:
// "shed"|"defer"|"downgrade"|"reject") para que a política declarativa do GW e a
// cadeia executada pelo Escalonador falem a MESMA linguagem — o GW não inventa
// uma cadeia paralela.
type Action string

const (
	// ActionShed — descartar trabalho opcional/baixa prioridade (executado pelo
	// Escalonador). O GW nunca descarta; só ordena a preferência.
	ActionShed Action = "shed"
	// ActionDefer — adiar trabalho admissível (executado pelo Escalonador).
	ActionDefer Action = "defer"
	// ActionDegrade — degradar para um tier mais barato. É o ÚNICO degrau em que o
	// GW contribui a ESCOLHA (o tier), via routing/tiering; a execução (variância
	// model_downgraded, reversibilidade) é do Escalonador.
	ActionDegrade Action = "downgrade"
	// ActionReject — rejeitar como último recurso (executado pelo Escalonador).
	ActionReject Action = "reject"
)

// DefaultOrder é a ORDEM DE PREFERÊNCIA canónica declarativa: shed → defer →
// degradar → rejeitar. É IDÊNTICA à scheduler.DefaultPreferenceOrder (ADR-008) —
// o GW não redefine a cadeia, apenas a representa como dado para coordenar com o
// Escalonador.
var DefaultOrder = []Action{ActionShed, ActionDefer, ActionDegrade, ActionReject}

// DefaultDegradeThresholdPct é o limiar de EXAUSTÃO GRACIOSA por omissão: a ~80%
// do orçamento consumido, a degradação para tier mais barato é OFERECIDA como
// continuação em vez do hard-stop cego (tecnica/06 §6).
const DefaultDegradeThresholdPct = 80

// Policy é a política declarativa de degradação (DADOS, não código imperativo
// disperso): a ordem de preferência e o limiar de exaustão graciosa. Imutável
// após construção; determinística.
type Policy struct {
	// Order é a ordem de preferência dos degraus (coerente com o Escalonador).
	Order []Action
	// DegradeThresholdPct é a percentagem de orçamento a partir da qual se oferece
	// degradação (exaustão graciosa). ∈ [1,100].
	DegradeThresholdPct int
}

// NewPolicy constrói a política, aplicando defaults seguros: a ordem canónica
// [DefaultOrder] se vazia, e [DefaultDegradeThresholdPct] se o limiar for inválido
// (fora de [1,100]).
func NewPolicy(order []Action, degradeThresholdPct int) Policy {
	p := Policy{Order: order, DegradeThresholdPct: degradeThresholdPct}
	if len(p.Order) == 0 {
		p.Order = DefaultOrder
	}
	if p.DegradeThresholdPct < 1 || p.DegradeThresholdPct > 100 {
		p.DegradeThresholdPct = DefaultDegradeThresholdPct
	}
	return p
}

// DefaultPolicy é a política declarativa por omissão (ordem canónica, limiar 80%).
func DefaultPolicy() Policy { return NewPolicy(DefaultOrder, DefaultDegradeThresholdPct) }

// BudgetKey identifica a dimensão de orçamento (tipicamente board/tenant). A
// contabilidade real é AOS-062; aqui a chave só endereça a porta.
type BudgetKey struct {
	Board  string
	Tenant string
}

// BudgetState é o estado de orçamento observado: consumido vs tecto. Sem floats
// (determinismo): as comparações são racionais inteiros.
type BudgetState struct {
	// Used é o orçamento já consumido (unidade opaca: tokens, micro-USD, …).
	Used int64
	// Limit é o tecto do orçamento (> 0). Limit <= 0 é tratado como SEM tecto
	// (nunca atinge o limiar — orçamento ilimitado não força degradação).
	Limit int64
}

// AtOrAbovePct reporta se o consumo atingiu OU excedeu `pct`% do tecto, por
// comparação inteira (Used*100 >= Limit*pct) — sem floats. Um tecto <= 0
// (ilimitado) devolve sempre false.
func (s BudgetState) AtOrAbovePct(pct int) bool {
	if s.Limit <= 0 {
		return false
	}
	return s.Used*100 >= s.Limit*int64(pct)
}

// Exhausted reporta se o orçamento está esgotado ou excedido (Used >= Limit).
func (s BudgetState) Exhausted() bool {
	if s.Limit <= 0 {
		return false
	}
	return s.Used >= s.Limit
}

// BudgetProvider é a PORTA de sinal de orçamento (headroom) — a impl de referência
// é [StaticBudgetProvider]; a contabilidade REAL de custos é AOS-062 (não aqui). O
// router consome-a para a exaustão graciosa a ~80%.
type BudgetProvider interface {
	// Budget devolve o estado de orçamento da chave. Um erro impede a decisão de
	// exaustão graciosa (o router trata como sem sinal — nunca inventa headroom).
	Budget(ctx context.Context, key BudgetKey) (BudgetState, error)
}

// StaticBudgetProvider é a impl de referência determinística do [BudgetProvider]:
// um mapa fixo de estados por chave, com um estado por omissão. Sem I/O nem
// relógio — segura para replay/teste. NÃO é a contabilidade real de custos
// (AOS-062).
type StaticBudgetProvider struct {
	byKey map[BudgetKey]BudgetState
	def   BudgetState
}

// NewStaticBudgetProvider constrói a impl de referência com um estado por omissão
// (chave ausente). Um def com Limit<=0 significa "sem tecto por omissão" (nunca
// força degradação para chaves não configuradas).
func NewStaticBudgetProvider(def BudgetState) *StaticBudgetProvider {
	return &StaticBudgetProvider{byKey: make(map[BudgetKey]BudgetState), def: def}
}

// Set fixa o estado de orçamento de uma chave específica.
func (p *StaticBudgetProvider) Set(key BudgetKey, st BudgetState) *StaticBudgetProvider {
	p.byKey[key] = st
	return p
}

// Budget implementa [BudgetProvider].
func (p *StaticBudgetProvider) Budget(_ context.Context, key BudgetKey) (BudgetState, error) {
	if st, ok := p.byKey[key]; ok {
		return st, nil
	}
	return p.def, nil
}

// Offer é a decisão de exaustão graciosa: se o orçamento aconselha OFERECER a
// degradação para um tier mais barato (nunca um hard-stop cego). É uma OFERTA — o
// router escolhe o tier; a EXECUÇÃO da cadeia é do Escalonador.
type Offer struct {
	// Degrade indica se a degradação para tier mais barato é oferecida como
	// continuação (true a partir do limiar de exaustão graciosa).
	Degrade bool
	// Exhausted indica que o orçamento está esgotado (>=100%). Mesmo esgotado, a
	// oferta é DEGRADAR (enquanto houver tier mais barato) — nunca um hard-stop
	// cego; só quando não há para onde descer é que a cadeia do Escalonador
	// rejeita.
	Exhausted bool
	// Reason descreve a razão (para o registo por decisão: modelo/tier/razão).
	Reason string
}

// OfferFor deriva a [Offer] de exaustão graciosa a partir do estado de orçamento e
// do limiar da política. Abaixo do limiar: sem degradação (Degrade=false). A
// partir do limiar (~80%): OFERECE degradar (exaustão graciosa) — e continua a
// oferecer mesmo esgotado (>=100%), pois a alternativa é o hard-stop cego que o
// desenho proíbe. Função PURA (sem relógio nem I/O).
func (p Policy) OfferFor(st BudgetState) Offer {
	switch {
	case st.Exhausted():
		return Offer{
			Degrade:   true,
			Exhausted: true,
			Reason:    "orcamento esgotado: degradar para tier mais barato (exaustao graciosa, nunca hard-stop cego)",
		}
	case st.AtOrAbovePct(p.DegradeThresholdPct):
		return Offer{
			Degrade: true,
			Reason:  "orcamento >= limiar de exaustao graciosa: oferece degradar para tier mais barato",
		}
	default:
		return Offer{Degrade: false, Reason: "orcamento abaixo do limiar: sem degradacao por orcamento"}
	}
}
