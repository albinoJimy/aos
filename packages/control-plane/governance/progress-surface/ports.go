package progresssurface

import (
	"context"

	"github.com/aos-ref/control-plane/budget"
)

// As PORTAS deste ficheiro DESACOPLAM o core (que compõe só otel-genai + budget) do
// scheduler/state em construção. Os adaptadores que as satisfazem vivem no WIRING
// (diferido/documentado, padrão budgetbridge/AOS-121): o BudgetReader adapta o
// budget.Budget/TreeBudgetReader; o BudgetExtender adapta o
// scheduler.HeadroomController.Admit; o Degrader adapta o scheduler.Degrader.ExecuteChain;
// o ProgressReflector adapta o controlsurface.StateProjector/state.Machine.Current. Assim
// a superfície não importa o scheduler/model-gateway inteiro.

// BudgetReader é a PORTA de LEITURA do orçamento por-árvore (EPIC-03, ADR-008). A
// superfície SÓ LÊ — o Limit (tecto do nó) e o Available (remanescente) — para derivar a
// fracção consumida. NUNCA reserva, debita nem muta o orçamento (isso é o enforcement do
// scheduler/breaker). No wiring, um adaptador sobre budget.Budget.Snapshot()[treeID].Limit
// e budget.Budget.Available(treeID) satisfá-la.
type BudgetReader interface {
	// Limit devolve o tecto do orçamento do nó/árvore (a base do denominador da fracção).
	Limit(ctx context.Context, treeID string) (budget.Amount, error)
	// Available devolve o remanescente do nó/árvore (Limit − Reserved − Committed).
	Available(ctx context.Context, treeID string) (budget.Amount, error)
}

// ExtensionRequest é o pedido de EXTENSÃO que a superfície ENDEREÇA ao controlo (não a
// impõe). Traz a árvore/run a estender, o headroom adicional pedido e a razão. A decisão
// (conceder/recusar) é do admission control — a superfície só PEDE (AC3).
type ExtensionRequest struct {
	// TreeID é o nó/árvore de orçamento cujo headroom se pede estender.
	TreeID string
	// RunID correlaciona o pedido com o run (para o span da decisão e o audit).
	RunID string
	// Additional é o headroom adicional pedido (tokens/$). É uma reserva NOUTRO plano
	// (token-bucket do admission), não uma mutação do budget.Budget por-árvore.
	Additional budget.Amount
	// Reason é a justificação legível (ex.: "user_requested_extension").
	Reason string
}

// ExtensionOutcome é a RESPOSTA do controlo ao pedido de extensão. A superfície devolve-a
// ao chamador tal-qual — não interpreta nem reconcilia orçamento por si.
type ExtensionOutcome struct {
	// Granted indica que o controlo concedeu o headroom adicional.
	Granted bool
	// Rejected indica que o controlo recusou (headroom insuficiente/política).
	Rejected bool
	// Detail é uma nota legível opcional do controlo (motivo/retry).
	Detail string
}

// BudgetExtender é a PORTA de EXTENSÃO DELEGADA (EPIC-03): a superfície PEDE, o controlo
// IMPÕE. Não existe "ExtendBudget" — a extensão é uma reserva ADICIONAL de headroom via o
// admission control (scheduler.HeadroomController.Admit), adaptado no wiring. A superfície
// NÃO muta o budget.Budget por-árvore.
type BudgetExtender interface {
	// RequestExtension endereça o pedido ao controlo e devolve a decisão deste.
	RequestExtension(ctx context.Context, req ExtensionRequest) (ExtensionOutcome, error)
}

// Degrader é a PORTA de DEGRADAÇÃO graciosa (EPIC-03). Liga a ausência de resposta ao
// prompt (timeout) à cadeia de degradação — NUNCA um hard-stop cego nem uma morte em
// silêncio. No wiring, um adaptador sobre scheduler.Degrader.ExecuteChain (com
// DegradationTrigger{Reason: reason}) satisfá-la.
type Degrader interface {
	// Degrade aplica a política de degradação com a razão dada (fail-closed, obrigatória).
	Degrade(ctx context.Context, reason string) error
}

// ProgressSnapshot são os RÓTULOS legíveis do progresso corrente (que estado, que passo).
// São strings de apresentação — o core NÃO importa o pacote state, mantendo-se leve.
type ProgressSnapshot struct {
	// State é o rótulo do estado durável corrente (ex.: "running", "waiting_on_tool").
	State string
	// Step é o rótulo do passo/actividade corrente (ex.: "chat#3", "tool:search").
	Step string
}

// ProgressReflector é a PORTA de PROGRESSO: reflecte a semântica de progresso corrente
// (AC1). No wiring, um adaptador sobre o controlsurface.StateProjector.Current()
// (state.Machine.Current) satisfá-la, mapeando o state.State para os rótulos.
type ProgressReflector interface {
	// Snapshot devolve o estado/passo corrente como rótulos de apresentação.
	Snapshot() ProgressSnapshot
}
