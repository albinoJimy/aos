package saga

import (
	"context"
	"sync"
)

// Compensation é a ACÇÃO INVERSA de um passo com efeito externo reversível — o que a
// saga corre para desfazer o efeito parcial desse passo. É associada ao step_id do
// passo original (a chave por que o passo foi aplicado no ledger de AOS-014).
type Compensation struct {
	// StepID é o step_id do passo ORIGINAL cujo efeito esta compensação desfaz. É a
	// âncora de identidade: a chave de idempotência da compensação deriva dele
	// ([CompensationKey]).
	StepID string
	// Action é o efeito INVERSO. Corre dentro do [durable.StepLedger.Apply] — se
	// devolver erro, nada fica registado e o retry pode repetir sem duplicar. NÃO deve
	// conter segredos observáveis.
	//
	// PRÉ-CONDIÇÃO (não imposta pelo coordinator): Action DEVE ser IDEMPOTENTE. O ledger
	// é at-least-once — um crash-before-commit (efeito aplicado, registo não commitado)
	// re-corre Action na retoma. Sem idempotência de Action, "0 reversões duplicadas"
	// degrada para at-least-once do efeito inverso. Ver [SagaCoordinator.Compensate].
	Action func(ctx context.Context) error
	// Reason é um rótulo legível da compensação (auditoria); NUNCA um segredo. Opcional.
	Reason string
}

// CompensationRegistry mapeia step_id → [Compensation] PRESERVANDO A ORDEM de registo.
// Cada activity com efeito reversível regista a sua compensação no momento em que
// aplica o efeito; a ordem de registo é, por construção, a ORDEM DE APLICAÇÃO dos
// passos — o que permite ao [SagaCoordinator] compensar por ORDEM INVERSA (LIFO).
//
// # Idempotência do registo (essencial para crash-resume)
//
// [Register] é idempotente por step_id: registar o MESMO step_id outra vez NÃO cria
// uma segunda entrada nem desloca a sua posição na ordem (actualiza apenas a acção).
// Isto é o que torna o registo REPRODUZÍVEL após um crash: um worker novo que remonta
// o run e re-regista as compensações pela mesma ordem determinística obtém a MESMA
// sequência LIFO, sem duplicar passos.
//
// Seguro para uso concorrente.
type CompensationRegistry struct {
	mu     sync.Mutex
	order  []string                // step_ids por ordem de aplicação (1.ª aplicação primeiro)
	byStep map[string]Compensation // step_id → compensação corrente
}

// NewCompensationRegistry constrói um registo de compensações vazio.
func NewCompensationRegistry() *CompensationRegistry {
	return &CompensationRegistry{byStep: make(map[string]Compensation)}
}

// Register associa uma compensação ao seu step_id, preservando a ordem de aplicação.
// É IDEMPOTENTE por step_id: re-registar o mesmo step_id actualiza a acção mas mantém
// a posição na ordem (não duplica) — o que torna o registo reproduzível após crash.
//
// Devolve [ErrEmptyStepID] se StepID for vazio ou [ErrNilAction] se Action for nil.
func (r *CompensationRegistry) Register(c Compensation) error {
	if c.StepID == "" {
		return ErrEmptyStepID
	}
	if c.Action == nil {
		return ErrNilAction
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byStep[c.StepID]; !exists {
		r.order = append(r.order, c.StepID)
	}
	r.byStep[c.StepID] = c
	return nil
}

// Len devolve o número de compensações distintas registadas.
func (r *CompensationRegistry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.order)
}

// Lookup devolve a compensação registada para um step_id, se existir.
func (r *CompensationRegistry) Lookup(stepID string) (Compensation, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.byStep[stepID]
	return c, ok
}

// Applied devolve as compensações por ORDEM DE APLICAÇÃO (a 1.ª aplicada primeiro).
// É uma cópia estável — mutar o slice devolvido não afecta o registo.
func (r *CompensationRegistry) Applied() []Compensation {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Compensation, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.byStep[id])
	}
	return out
}

// Reversed devolve as compensações por ORDEM INVERSA de aplicação (LIFO — o último
// aplicado primeiro). É a ordem exacta em que o [SagaCoordinator] as executa. Cópia
// estável.
func (r *CompensationRegistry) Reversed() []Compensation {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Compensation, 0, len(r.order))
	for i := len(r.order) - 1; i >= 0; i-- {
		out = append(out, r.byStep[r.order[i]])
	}
	return out
}
