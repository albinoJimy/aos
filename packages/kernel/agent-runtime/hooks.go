package agentruntime

import "context"

// ---------------------------------------------------------------------------
// HOOKS DE FUTURO (AOS-014 / AOS-015) — expostos, NÃO implementados aqui.
//
// O escopo de AOS-013 é o loop base. A idempotência de passo e o checkpoint
// intra-iteração são tickets próprios; este ficheiro fixa os PONTOS DE LIGAÇÃO
// (interfaces opcionais com default no-op) para que esses tickets se liguem sem
// alterar a forma do loop.
// ---------------------------------------------------------------------------

// StepIdentity deriva o step_id de um turno. É o ponto de ligação de AOS-014
// (idempotency key = f(run_id, step_id)): o contrato de idempotência substituirá
// o default por uma derivação determinística ancorada na POSIÇÃO no log (seq),
// não num relógio nem UUID (ADR-001, tecnica/13 §3). O default sequencial abaixo
// é suficiente para o esqueleto: gera step_ids estáveis e distintos por turno.
type StepIdentity interface {
	// StepID devolve o identificador do passo lógico do turno dado (1-based).
	StepID(runID string, turn int) string
}

// sequentialStepIdentity é o default: "step-000001", "step-000002", … Estável e
// distinto por turno (evita a deduplicação do Event Store entre turnos).
type sequentialStepIdentity struct{}

func (sequentialStepIdentity) StepID(_ string, turn int) string {
	return "step-" + pad6(turn)
}

// pad6 formata n com zeros à esquerda até 6 dígitos (determinístico, sem fmt).
func pad6(n int) string {
	s := itoa(n)
	for len(s) < 6 {
		s = "0" + s
	}
	return s
}

// CheckpointPhase nomeia o ponto do turno em que um checkpoint é tirado. As fases
// cobrem os checkpoints intra-iteração que AOS-015 vai persistir.
type CheckpointPhase string

const (
	// PhaseAssembled — prompt do turno montado (antes de chamar o modelo).
	PhaseAssembled CheckpointPhase = "assembled"
	// PhaseModelCalled — resposta do modelo recebida.
	PhaseModelCalled CheckpointPhase = "model_called"
	// PhaseTurnRecorded — turno gravado no Event Store.
	PhaseTurnRecorded CheckpointPhase = "turn_recorded"
	// PhaseDispatched — uma tool call foi despachada via RM e o resultado recebido.
	PhaseDispatched CheckpointPhase = "dispatched"
	// PhaseVerified — turno verificado; decisão de continuar/terminar tomada.
	PhaseVerified CheckpointPhase = "verified"
)

// Checkpoint descreve um ponto de checkpoint intra-iteração.
//
// Os campos ConfirmedStepID e PendingActivities são ADITIVOS (AOS-015): dão ao
// Checkpointer real a granularidade de SUB-PASSO (activity) dentro de um turno,
// sem alterar a forma dos checkpoints já observada por AOS-013. Ficam a zero
// ("" / nil) nas fases sem granularidade de activity — nesse caso o Checkpointer
// trata StepID como o passo confirmado (o turno inteiro).
type Checkpoint struct {
	RunID  string
	StepID string
	Turn   int
	Phase  CheckpointPhase

	// ConfirmedStepID é o step_id CONCRETO que este checkpoint confirma. Numa
	// fase de despacho de activity é o sub-passo (StepID + "-tool-" + n, a MESMA
	// convenção do loop e de [durable.StepSequencer.SubStepID]); nas fases ao
	// nível do turno fica vazio e o Checkpointer usa StepID. É esta string que
	// tem de casar com o step_id do step-ledger de AOS-014 para o mesmo passo.
	ConfirmedStepID string
	// PendingActivities são os step_ids das activities AINDA pendentes dentro da
	// iteração corrente DEPOIS deste checkpoint (vazio ⇒ iteração toda despachada).
	// O cursor de progresso persistido em AOS-015 usa esta lista para o resume
	// retomar no próximo sub-passo não confirmado.
	PendingActivities []string
}

// Checkpointer persiste checkpoints intra-iteração. É o ponto de ligação de
// AOS-015: um crash entre fases é recuperável porque cada fase é um checkpoint
// (tecnica/02 §4). O default [noopCheckpointer] não persiste nada — o loop base
// não é durável ao nível intra-iteração (isso é AOS-015).
type Checkpointer interface {
	Checkpoint(ctx context.Context, cp Checkpoint) error
}

type noopCheckpointer struct{}

func (noopCheckpointer) Checkpoint(context.Context, Checkpoint) error { return nil }
