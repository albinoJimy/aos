package durable

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// EventTypeCheckpoint é o tipo canónico do evento de checkpoint intra-iteração
// materializado no Event Store. Um evento por FASE confirmada dentro de um turno
// (assembled/model_called/turn_recorded/dispatched/verified — ver
// [agentruntime.CheckpointPhase]).
const EventTypeCheckpoint = "step.checkpoint"

// checkpointStepPrefix namespaceia o step_id do evento de checkpoint no envelope
// do Event Store, para que a sua idempotency_key (run_id + ":ckpt-" + …) seja
// DISTINTA da do turno (run_id:step_id, AOS-013) e da do ledger
// (run_id:ledger-step_id, AOS-014). Sem este namespace o ES deduplicaria o
// checkpoint contra o evento de turno homónimo (a dedup do ES é global por
// idempotency_key). É o TERCEIRO domínio de dedup por passo (ver [IdempotencyKey]).
const checkpointStepPrefix = "ckpt-"

// Cursor é o CURSOR DE PROGRESSO serializável persistido em cada checkpoint. É a
// forma ESTÁVEL consumida pelo resume (AOS-015) e, adiante, pelo replay
// determinístico (AOS-016) — por isso os nomes dos campos JSON são um contrato.
//
// # O cursor REFERENCIA, não copia
//
// O cursor guarda apenas IDENTIDADE e POSIÇÃO (run_id, step_id confirmado, turno,
// fase, índice do sub-passo e as activities ainda pendentes). NÃO duplica o
// payload já no Event Store — a resposta do modelo vive no turn.recorded (AOS-013)
// e o resultado da activity no step.ledger.applied (AOS-014). O checkpoint é um
// marcador de progresso barato; o conteúdo referencia-se por (run_id, step_id).
type Cursor struct {
	// RunID é a trajectória (stream_id no Event Store).
	RunID string `json:"run_id"`
	// ConfirmedStepID é o step_id LÓGICO do passo que este checkpoint confirma. Para
	// uma activity é o sub-passo (step-NNN-tool-n); para uma fase ao nível do turno é
	// o step_id do turno. É EXACTAMENTE o step_id que o step-ledger de AOS-014 usa
	// para o mesmo passo lógico (consistência checkpoint↔ledger) — casa com o lado
	// step_id de [SplitKey] da chave do ledger.
	ConfirmedStepID string `json:"confirmed_step_id"`
	// Turn é o nº do turno (1-based) a que o checkpoint pertence.
	Turn int `json:"turn"`
	// Phase nomeia o ponto do turno confirmado (ver [agentruntime.CheckpointPhase]).
	Phase string `json:"phase"`
	// StepIndex é o índice (1-based) do sub-passo/activity dentro do turno quando o
	// checkpoint confirma uma activity; 0 nas fases ao nível do turno.
	StepIndex int `json:"step_index"`
	// PendingActivities são os step_ids das activities AINDA pendentes dentro da
	// iteração corrente DEPOIS deste checkpoint. Vazio ⇒ nenhuma activity por
	// despachar. O resume retoma na primeira desta lista sem repetir as confirmadas.
	PendingActivities []string `json:"pending_activities,omitempty"`
}

// CheckpointObserver é o gancho de observabilidade do checkpointer. Recebe apenas
// a forma OPACA (hash da idempotency key) — nunca ids em claro — honrando "sem
// segredos em logs" (DoD). Default: [NopCheckpointObserver].
type CheckpointObserver interface {
	// Checkpointed é chamado quando um checkpoint é escrito de forma nova (committed).
	Checkpointed(keyHash string)
	// CheckpointDeduplicated é chamado quando o checkpoint já existia (retry/replay):
	// o Event Store devolveu StatusDuplicate. É o caso idempotente esperado numa
	// re-tentativa após crash.
	CheckpointDeduplicated(keyHash string)
}

// NopCheckpointObserver descarta os eventos de observabilidade. É o default.
type NopCheckpointObserver struct{}

func (NopCheckpointObserver) Checkpointed(string)           {}
func (NopCheckpointObserver) CheckpointDeduplicated(string) {}

// EventStoreCheckpointer é o [agentruntime.Checkpointer] REAL de AOS-015: persiste
// cada checkpoint intra-iteração como um evento append-only no Event Store
// replicado (ADR-007, fonte de verdade). Substitui, de forma ADITIVA, o
// checkpointer no-op de AOS-013 (ligação via [agentruntime.WithCheckpointer]) sem
// alterar a forma do loop.
//
// # Durabilidade e failover
//
// Os checkpoints vivem no Event Store replicado por quórum: sobrevivem à morte do
// worker que os escreveu. Um worker novo reconstrói o cursor de retoma com o
// [Resumer], que relê o stream do run. A escrita é append-only — só CRESCE o
// registo; nunca reescreve história (não muta o prefixo cache-estável do prompt,
// ADR-009 — o checkpointer nem sequer toca no assembler).
//
// # Idempotência da própria escrita
//
// Cada checkpoint tem uma idempotency_key própria (run_id:ckpt-<phase>-<step_id>).
// Numa re-tentativa após crash, re-escrever o mesmo checkpoint dá StatusDuplicate
// no Event Store (sem duplicar o evento) — a escrita de checkpoint é, ela própria,
// idempotente.
//
// Sem estado mutável ⇒ seguro para uso concorrente e -race limpo.
type EventStoreCheckpointer struct {
	store    EventStore
	producer eventstore.Producer
	obs      CheckpointObserver
}

// CheckpointerOption configura o [EventStoreCheckpointer].
type CheckpointerOption func(*EventStoreCheckpointer)

// WithCheckpointObserver injecta o gancho de observabilidade (default
// [NopCheckpointObserver]).
func WithCheckpointObserver(o CheckpointObserver) CheckpointerOption {
	return func(c *EventStoreCheckpointer) { c.obs = o }
}

// WithCheckpointProducer define a identidade emissora (NHI + cadeia de delegação)
// gravada nos eventos de checkpoint. Default: Producer zero (aceitável em teste;
// em produção o run injecta o principal do agente).
func WithCheckpointProducer(p eventstore.Producer) CheckpointerOption {
	return func(c *EventStoreCheckpointer) { c.producer = p }
}

// NewCheckpointer constrói um checkpointer sobre o Event Store dado. store é
// obrigatório (não-nil).
func NewCheckpointer(store EventStore, opts ...CheckpointerOption) (*EventStoreCheckpointer, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	c := &EventStoreCheckpointer{store: store, obs: NopCheckpointObserver{}}
	for _, o := range opts {
		o(c)
	}
	if c.obs == nil {
		c.obs = NopCheckpointObserver{}
	}
	return c, nil
}

// Checkpoint implementa [agentruntime.Checkpointer]: persiste o cursor de progresso
// do checkpoint dado como um evento append-only. Deriva o step_id CONFIRMADO
// (cp.ConfirmedStepID, ou cp.StepID nas fases ao nível do turno — a mesma
// convenção de AOS-013) e escreve o cursor { turno, fase, índice, pendentes } sem
// copiar o payload já persistido noutros eventos.
//
// O run_id não pode ser vazio nem conter ':' (metade da identidade do passo e do
// namespace da idempotency_key). Propaga o erro do Event Store (p.ex.
// [eventstore.ErrNoQuorum] em perda de quórum — fail-closed).
func (c *EventStoreCheckpointer) Checkpoint(ctx context.Context, cp agentruntime.Checkpoint) error {
	if cp.RunID == "" {
		return ErrEmptyRunID
	}
	if strings.Contains(cp.RunID, keyDelimiter) {
		return ErrDelimiterInInput
	}

	confirmed := cp.ConfirmedStepID
	if confirmed == "" {
		confirmed = cp.StepID
	}
	if confirmed == "" {
		return ErrEmptyStepID
	}

	cur := Cursor{
		RunID:             cp.RunID,
		ConfirmedStepID:   confirmed,
		Turn:              cp.Turn,
		Phase:             string(cp.Phase),
		StepIndex:         activityIndex(confirmed),
		PendingActivities: cp.PendingActivities,
	}
	payload, err := json.Marshal(cur)
	if err != nil {
		return err
	}

	// Envelope namespaced: "ckpt-<phase>-<confirmed>". Distinto por fase E por
	// activity dentro do turno (senão o ES deduplicaria as várias fases do mesmo
	// turno contra a primeira) e distinto do turn.recorded / step.ledger.applied.
	envStepID := checkpointStepPrefix + string(cp.Phase) + "-" + confirmed
	keyHash := OpaqueKeyMust(cp.RunID, envStepID)

	res, err := c.store.Append(ctx, cp.RunID, eventstore.EventInput{
		Type:         EventTypeCheckpoint,
		Payload:      payload,
		RunID:        cp.RunID,
		StepID:       envStepID,
		ParentStepID: cp.StepID,
		Producer:     c.producer,
	})
	if err != nil {
		return err
	}
	if res.Status == eventstore.StatusDuplicate {
		c.obs.CheckpointDeduplicated(keyHash)
	} else {
		c.obs.Checkpointed(keyHash)
	}
	return nil
}

// OpaqueKeyMust devolve a forma opaca (hash) da idempotency key sem propagar erro —
// usa-se onde o par (run_id, step_id) já foi validado a montante. Em caso de par
// inválido devolve a string vazia (o observer apenas perde o hash, sem afectar a
// escrita durável). É um auxiliar de observabilidade, não a chave de deduplicação.
func OpaqueKeyMust(runID, stepID string) string {
	h, err := OpaqueKey(runID, stepID)
	if err != nil {
		return ""
	}
	return h
}

// activityIndex extrai o índice 1-based do sub-passo de um step_id de activity
// (…-tool-N). Devolve 0 se o step_id não for de activity (fase ao nível do turno)
// ou se o sufixo não for um inteiro. Espelha a convenção "-tool-" do loop base e
// de [StepSequencer.SubStepID].
func activityIndex(stepID string) int {
	const marker = "-tool-"
	i := strings.LastIndex(stepID, marker)
	if i < 0 {
		return 0
	}
	n, err := strconv.Atoi(stepID[i+len(marker):])
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// Verificação em tempo de compilação: o EventStoreCheckpointer implementa o hook
// de AOS-013. É a ligação ADITIVA (WithCheckpointer) — sem alterar o loop.
var _ agentruntime.Checkpointer = (*EventStoreCheckpointer)(nil)
