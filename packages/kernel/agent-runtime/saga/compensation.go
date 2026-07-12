package saga

import (
	"context"
	"fmt"

	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/state"
)

// compStepPrefix namespaceia o step_id da COMPENSAÇÃO. A chave de idempotência da
// compensação de um passo é f(run_id, comp-<step_id>), DISTINTA da chave do passo
// original (run_id:step_id) e das demais famílias de dedup (turno, ledger, checkpoint,
// transição de estado). Assim, "aplicar o passo" e "compensar o passo" são dois
// domínios de dedup separados: compensar não colide com o efeito directo.
//
// No envelope do Event Store, o [durable.StepLedger] volta a prefixar a sua própria
// namespace ("ledger-"), pelo que o step_id efectivo do evento é ledger-comp-<step_id>.
const compStepPrefix = "comp-"

// Razões canónicas gravadas nas transições de estado accionadas pela saga (rótulos de
// auditoria, nunca segredos).
const (
	// ReasonEnterCompensating — failed → compensating (início da saga de rollback).
	ReasonEnterCompensating = "saga_enter_compensating"
	// ReasonCompensated — compensating → ready (compensação concluída; retry limpo).
	ReasonCompensated = "saga_compensated_retry_clean"
)

// StatusCompensated é o Status memorizado no ledger para uma reversão bem-sucedida.
// O Payload da reversão é vazio por convenção (a compensação não devolve resultado
// observável — apenas desfaz), honrando "sem segredos".
const StatusCompensated = "compensated"

// CompensationKey deriva a chave de idempotência canónica de uma COMPENSAÇÃO a partir
// de (run_id, step_id) do passo original: f(run_id, "comp-"+step_id). É a chave por
// que [durable.StepLedger.Apply] deduplica a reversão — reexecutar a saga com a mesma
// chave NÃO duplica o efeito de compensação. Propaga a validação de
// [durable.IdempotencyKey] (rejeita run_id/step_id vazios ou com ':').
func CompensationKey(runID, stepID string) (string, error) {
	if stepID == "" {
		return "", ErrEmptyStepID
	}
	return durable.IdempotencyKey(runID, compStepPrefix+stepID)
}

// StepLedger é o subconjunto do ledger de AOS-014 de que a saga depende: Apply (efeito
// idempotente com already-applied antes do efeito) e Rebuild (reconstrução do conjunto
// de compensações já commitadas, para crash-resume). *durable.StepLedger satisfaz-o.
type StepLedger interface {
	Apply(ctx context.Context, key string, effect func(context.Context) (durable.Result, error)) (durable.Result, bool, error)
	Rebuild(ctx context.Context, runID string) error
}

// StateMachine é o subconjunto da máquina de AOS-017 de que a saga depende: o estado
// corrente, o run_id (stream) e a transição durável. *state.Machine satisfaz-o. A saga
// NÃO reimplementa a máquina — apenas aciona as arestas failed → compensating → ready.
type StateMachine interface {
	Current() state.State
	RunID() string
	Transition(ctx context.Context, to state.State, ev state.TransitionEvent) error
}

// Observer é o gancho de observabilidade da saga. Recebe as chaves na forma OPACA
// (hash) — nunca a chave em claro nem o payload da compensação — para honrar "sem
// segredos em logs" (DoD). Default: [NopObserver].
type Observer interface {
	// Started é chamado quando a saga entra (ou retoma) a compensação, com o número de
	// compensações registadas a percorrer.
	Started(runID string, count int)
	// Compensated é chamado por cada compensação resolvida: ranNow=true se a reversão
	// correu AGORA, ranNow=false se foi DEDUPLICADA (já aplicada — crash-resume/reexec).
	Compensated(keyHash string, ranNow bool)
	// Retry é chamado a cada tentativa falhada de uma compensação (attempt começa em 1).
	Retry(keyHash string, attempt int, err error)
	// Escalated é chamado quando uma compensação esgota a política de retry — a saga
	// escala por alerta e o run fica preso em compensating (não finge sucesso).
	Escalated(keyHash string, err error)
	// Completed é chamado quando a saga compensou tudo e transitou compensating → ready.
	Completed(runID string)
}

// NopObserver descarta os eventos de observabilidade. É o default.
type NopObserver struct{}

func (NopObserver) Started(string, int)      {}
func (NopObserver) Compensated(string, bool) {}
func (NopObserver) Retry(string, int, error) {}
func (NopObserver) Escalated(string, error)  {}
func (NopObserver) Completed(string)         {}

// SagaCoordinator orquestra a compensação de um run: aciona failed → compensating,
// executa as compensações registadas por ORDEM INVERSA idempotente (via o ledger de
// AOS-014), e — em sucesso — transita compensating → ready para retry limpo. Sobrevive
// a crash (reusa os Rebuild da máquina e do ledger) e escala honestamente uma
// compensação irrecuperável.
//
// Seguro para uso sequencial por run (a máquina serializa as suas transições; o ledger
// é seguro para uso concorrente).
//
// # Pré-condição: posse do lease (fencing, AOS-018)
//
// [SagaCoordinator.Compensate] NÃO valida posse de lease/fencing token — assume que o
// chamador DETÉM o lease do run (AOS-018) ao invocá-la. O single-flight do ledger colapsa
// Applies concorrentes apenas DENTRO do mesmo processo (ver [durable.StepLedger]); dois
// coordinators em processos distintos sobre o MESMO run correm ambos o loop e comp.Action
// corre uma vez POR processo (efeito inverso observável 2x) antes de a dedup DURÁVEL do ES
// colapsar o REGISTO. A exclusão mútua entre workers é, por isso, responsabilidade do
// fencing (AOS-018), fora do âmbito de AOS-020 — mesma raiz da cardinalidade at-least-once
// documentada em [SagaCoordinator.Compensate].
type SagaCoordinator struct {
	machine    StateMachine
	ledger     StepLedger
	registry   *CompensationRegistry
	obs        Observer
	maxRetries int
}

// Option configura o [SagaCoordinator].
type Option func(*SagaCoordinator)

// WithObserver injecta o gancho de observabilidade (default [NopObserver]).
func WithObserver(o Observer) Option { return func(c *SagaCoordinator) { c.obs = o } }

// WithMaxRetries define quantas RE-tentativas (além da 1.ª) uma compensação falhada
// recebe antes de escalar. Default 2 (⇒ 3 tentativas no total). Valores negativos são
// tratados como 0 (só a 1.ª tentativa, sem retry).
func WithMaxRetries(n int) Option {
	return func(c *SagaCoordinator) {
		if n < 0 {
			n = 0
		}
		c.maxRetries = n
	}
}

// NewSagaCoordinator constrói um coordenador sobre a máquina de AOS-017, o ledger de
// AOS-014 e um registo de compensações. Os três são obrigatórios.
func NewSagaCoordinator(machine StateMachine, ledger StepLedger, registry *CompensationRegistry, opts ...Option) (*SagaCoordinator, error) {
	if machine == nil {
		return nil, ErrNilMachine
	}
	if ledger == nil {
		return nil, ErrNilLedger
	}
	if registry == nil {
		return nil, ErrNilRegistry
	}
	c := &SagaCoordinator{
		machine:    machine,
		ledger:     ledger,
		registry:   registry,
		obs:        NopObserver{},
		maxRetries: 2,
	}
	for _, o := range opts {
		o(c)
	}
	if c.obs == nil {
		c.obs = NopObserver{}
	}
	return c, nil
}

// Compensate executa a saga de compensação do run.
//
// Fluxo (idempotente e reentrante):
//
//  1. ENTRADA/RETOMA. Se o run está em [state.Failed], aciona a transição durável
//     failed → compensating (AOS-017). Se já está em [state.Compensating] (crash-resume),
//     retoma. Qualquer outro estado ⇒ [ErrNotCompensating] sem tocar no run.
//  2. RECONSTRUÇÃO. Reconstrói o ledger a partir do log ([StepLedger.Rebuild]) para
//     conhecer as compensações JÁ commitadas — a base do crash-resume.
//  3. LIFO IDEMPOTENTE. Percorre as compensações por ORDEM INVERSA de aplicação;
//     cada uma corre dentro de [StepLedger.Apply] com a sua chave de compensação. Uma
//     já aplicada (registo commitado) é DEDUPLICADA (não corre); uma pendente corre.
//     Uma falha faz RETRY idempotente até à política; esgotada, ESCALA
//     ([ErrCompensationExhausted]) e o run FICA em compensating (não finge sucesso).
//  4. POLÍTICA. Compensado tudo, transita compensating → ready (retry limpo).
//
// # Cardinalidade da reversão (contrato honesto: at-least-once + idempotência downstream)
//
// O ledger de AOS-014 é AT-LEAST-ONCE, não exactly-once do efeito externo (ver
// [durable.StepLedger]). A verificação already-applied precede o efeito, mas assenta no
// COMMIT durável do registo: comp.Action corre ANTES do Append do ledger, logo um
// crash-before-commit (efeito aplicado, registo não commitado) faz o retry — no mesmo
// worker ou na retoma — RE-CORRER comp.Action. Portanto "0 reversões duplicadas
// OBSERVÁVEIS" só se sustenta se comp.Action for IDEMPOTENTE sobre a sua chave (ou o ES
// não falhar após o efeito); caso contrário a garantia degrada para at-least-once do
// efeito inverso. A idempotência de comp.Action é PRÉ-CONDIÇÃO do chamador (contrato de
// [Compensation.Action]), NÃO imposta pelo coordinator. Ver [compensateOne].
//
// Respeita o cancelamento do ctx ENTRE compensações (uma interrupção — p.ex. shutdown —
// pára de forma limpa; a retoma posterior deduplica as já feitas). Devolve nil sse a
// saga concluiu (run em ready), ou o erro que a impediu (o run permanece em compensating).
func (c *SagaCoordinator) Compensate(ctx context.Context) error {
	runID := c.machine.RunID()

	// (1) entrada/retoma.
	switch cur := c.machine.Current(); cur {
	case state.Failed:
		if err := c.machine.Transition(ctx, state.Compensating, state.TransitionEvent{Reason: ReasonEnterCompensating}); err != nil {
			return fmt.Errorf("saga: entrar em compensating (run %s): %w", runID, err)
		}
	case state.Compensating:
		// crash-resume: já em compensating; retoma a partir do log.
	default:
		return fmt.Errorf("%w: estado=%s (run %s)", ErrNotCompensating, cur, runID)
	}

	// (2) reconstrução do ledger — base durável do already-applied (crash-resume).
	if err := c.ledger.Rebuild(ctx, runID); err != nil {
		return fmt.Errorf("saga: rebuild do ledger (run %s): %w", runID, err)
	}

	// (3) compensações por ordem inversa, idempotentes.
	comps := c.registry.Reversed()
	c.obs.Started(runID, len(comps))
	for _, comp := range comps {
		if err := ctx.Err(); err != nil {
			// Interrupção limpa (crash/shutdown): pára; a retoma deduplica as já feitas.
			return fmt.Errorf("saga: interrompida (run %s): %w", runID, err)
		}
		if err := c.compensateOne(ctx, runID, comp); err != nil {
			return err // escalada: o run permanece em compensating.
		}
	}

	// (4) política pós-compensação: retry limpo (compensating → ready).
	if err := c.machine.Transition(ctx, state.Ready, state.TransitionEvent{Reason: ReasonCompensated}); err != nil {
		return fmt.Errorf("saga: transitar compensating→ready (run %s): %w", runID, err)
	}
	c.obs.Completed(runID)
	return nil
}

// compensateOne corre a compensação de UM passo dentro do ledger, com retry idempotente
// e escalada honesta.
//
// # Dedup e cardinalidade (at-least-once)
//
// A dedup é feita pela CHAVE DE COMPENSAÇÃO (run:comp-<step_id>): a pergunta respondida é
// "já compensei este passo?", nunca "o passo original aplicou?". O efeito (comp.Action)
// corre ANTES do commit do registo no ledger; se o Append falhar, nada fica registado e o
// retry (aqui ou na retoma) RE-CORRE comp.Action. Logo "0 reversões duplicadas" é uma
// garantia OBSERVÁVEL só sob idempotência downstream de comp.Action (pré-condição do
// chamador — ver [Compensation.Action]); sem ela, o contrato é at-least-once.
//
// # Não-sobre-compensação (responsabilidade do chamador/reconstrução)
//
// O coordinator NÃO cruza a chave do passo ORIGINAL (run:<step_id>) para confirmar que o
// efeito directo ocorreu: compensa toda [Compensation] que o registry apresentar. A
// invariante "passos não aplicados não são compensados" é, por isso, responsabilidade
// EXCLUSIVA do chamador — registar a compensação apenas NO MOMENTO em que aplica o efeito
// (ver doc.go, "Registo de compensações") e, no crash-resume, re-registar exactamente os
// passos que aplicaram. Não há defesa-em-profundidade contra uma reconstrução que
// sobre-registe.
func (c *SagaCoordinator) compensateOne(ctx context.Context, runID string, comp Compensation) error {
	key, err := CompensationKey(runID, comp.StepID)
	if err != nil {
		return fmt.Errorf("saga: chave de compensação (step %s): %w", comp.StepID, err)
	}
	keyHash := durable.HashKey(key)

	effect := func(ctx context.Context) (durable.Result, error) {
		if aerr := comp.Action(ctx); aerr != nil {
			return durable.Result{}, aerr
		}
		return durable.Result{Status: StatusCompensated}, nil
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		_, ranNow, aerr := c.ledger.Apply(ctx, key, effect)
		if aerr == nil {
			// ranNow=false ⇒ já estava commitada (dedup): reversão NÃO duplicada.
			c.obs.Compensated(keyHash, ranNow)
			return nil
		}
		lastErr = aerr
		c.obs.Retry(keyHash, attempt+1, aerr)
	}

	// Esgotou a política de retry: escala por alerta; NÃO finge sucesso.
	c.obs.Escalated(keyHash, lastErr)
	return fmt.Errorf("%w: step comp %s (%d tentativas): %w", ErrCompensationExhausted, keyHash, c.maxRetries+1, lastErr)
}
