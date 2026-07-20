package env

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aos-ref/substrate/eventstore"
	testkit "github.com/aos-ref/testkit"
)

// ===========================================================================
// Seed — popular o Event Store com uma TRAJECTÓRIA CONHECIDA (AC5)
// ===========================================================================
//
// Os helpers de seed populam o Event Store com uma trajectória DETERMINISTA que
// as suites de replay/idempotência (AOS-111/112) reutilizam: os step_ids derivam
// do sequenciador PURO de AOS-014 (via [testkit.NewStepSequencer]), pelo que a
// MESMA trajectória re-semeada produz as MESMAS idempotency keys — e o Event
// Store deduplica (StatusDuplicate com o seq original). O prompt_hash/model/seed
// de cada turno são função pura de (run_id, turn): sem relógio de parede, sem
// aleatoriedade, sem flakiness.

// ModelRef é o identificador de modelo canónico das trajectórias semeadas
// (estável entre execuções).
const ModelRef = "ref-model-v1"

// TurnPayload é o payload determinista de um evento de trajectória. Os campos
// são função pura de (run_id, turn) — reproduzíveis byte-a-byte.
type TurnPayload struct {
	Turn       int    `json:"turn"`
	PromptHash string `json:"prompt_hash"`
	Model      string `json:"model"`
	Seed       int64  `json:"seed"`
	StepID     string `json:"step_id"`
}

// TrajectoryStep é o resultado de UM passo semeado: o step_id lógico do turno e
// o AppendResult do Event Store (para asserir seq/status).
type TrajectoryStep struct {
	Turn         int
	StepID       string
	EventType    string
	AppendResult eventstore.AppendResult
}

// promptHash deriva um prompt_hash determinista de (run_id, turn). Não é um hash
// criptográfico — é um rótulo estável e legível suficiente para os testes de
// replay asserirem estabilidade entre execuções.
func promptHash(runID string, turn int) string {
	return fmt.Sprintf("ph-%s-%03d", runID, turn)
}

// stepSeq mapeia (turn, slot) num índice de step MONOTÓNICO para o sequenciador.
// Cada turno ocupa DOIS steps lógicos distintos (turn.recorded e replay.captured)
// para que ambos os eventos tenham idempotency keys DISTINTAS (o mesmo step_id
// deduplicaria). slot 0 = recorded, slot 1 = replay.
func stepSeq(turn, slot int) int { return 2*turn - 1 + slot }

// SeedTrajectory popula o Event Store com uma trajectória CONHECIDA de nTurns
// turnos no stream runID. Cada turno emite dois eventos deterministas —
// "turn.recorded" e "replay.captured" — com prompt_hash/model/seed derivados de
// (run_id, turn) e step_ids do sequenciador puro. Devolve os passos semeados
// (em ordem) para asserção. Falha o teste se o Env não tiver Event Store ou se
// algum Append falhar.
//
// Re-semear a MESMA trajectória (mesmo runID, mesmo nTurns) reproduz as MESMAS
// idempotency keys — a base do replay/idempotência determinista de AOS-111/112.
func (e *EphemeralEnv) SeedTrajectory(runID string, nTurns int) []TrajectoryStep {
	e.tb.Helper()
	if e.EventStore == nil {
		e.tb.Fatalf("env.SeedTrajectory: sem Event Store (usa WithEventStore/WithBus)")
	}
	seq := testkit.NewStepSequencer()
	ctx := context.Background()
	out := make([]TrajectoryStep, 0, nTurns*2)

	for turn := 1; turn <= nTurns; turn++ {
		for slot, typ := range []string{"turn.recorded", "replay.captured"} {
			stepID := seq.StepID(runID, stepSeq(turn, slot))
			payload := TurnPayload{
				Turn:       turn,
				PromptHash: promptHash(runID, turn),
				Model:      ModelRef,
				Seed:       int64(turn),
				StepID:     stepID,
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				e.tb.Fatalf("env.SeedTrajectory: marshal turn %d/%s: %v", turn, typ, err)
			}
			res, err := e.EventStore.Append(ctx, runID, eventstore.EventInput{
				Type:    typ,
				Payload: raw,
				RunID:   runID,
				StepID:  stepID,
			})
			if err != nil {
				e.tb.Fatalf("env.SeedTrajectory: append turn %d/%s: %v", turn, typ, err)
			}
			out = append(out, TrajectoryStep{Turn: turn, StepID: stepID, EventType: typ, AppendResult: res})
		}
	}
	return out
}

// SeedEvent appenda UM evento determinista arbitrário no stream runID, com o
// step_id lógico dado pelo turno (sequenciador puro). É o helper de baixo nível
// para trajectórias personalizadas. Devolve o AppendResult.
func (e *EphemeralEnv) SeedEvent(runID, eventType string, turn int, payload any) eventstore.AppendResult {
	e.tb.Helper()
	if e.EventStore == nil {
		e.tb.Fatalf("env.SeedEvent: sem Event Store (usa WithEventStore/WithBus)")
	}
	stepID := testkit.NewStepSequencer().StepID(runID, turn)
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			e.tb.Fatalf("env.SeedEvent: marshal: %v", err)
		}
		raw = b
	}
	res, err := e.EventStore.Append(context.Background(), runID, eventstore.EventInput{
		Type:    eventType,
		Payload: raw,
		RunID:   runID,
		StepID:  stepID,
	})
	if err != nil {
		e.tb.Fatalf("env.SeedEvent: append: %v", err)
	}
	return res
}
