package durable

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// Fixtures de checkpoint
// ---------------------------------------------------------------------------

const testRun = "run_ckpt_1"

// newStore (Event Store real, 3 réplicas) é partilhado com step_ledger_test.go.

// iterationCheckpoints devolve, na ORDEM DE EXECUÇÃO do loop, os checkpoints que
// uma iteração multi-passo emite: um turno com `nTools` tool calls seguido de um
// turno final sem tools. Espelha exactamente loop.go (cp / cpActivity): fases
// assembled → model_called → turn_recorded → dispatched* → verified, e o cursor de
// activity (ConfirmedStepID = step-…-tool-n, PendingActivities = as seguintes).
func iterationCheckpoints(seq *StepSequencer, runID string, nTools int) []agentruntime.Checkpoint {
	var cps []agentruntime.Checkpoint
	turnLevel := func(turn int, phase agentruntime.CheckpointPhase) agentruntime.Checkpoint {
		return agentruntime.Checkpoint{RunID: runID, StepID: seq.StepID(runID, turn), Turn: turn, Phase: phase}
	}

	// Turno 1 — nTools activities.
	t1 := seq.StepID(runID, 1)
	cps = append(cps,
		turnLevel(1, agentruntime.PhaseAssembled),
		turnLevel(1, agentruntime.PhaseModelCalled),
		turnLevel(1, agentruntime.PhaseTurnRecorded),
	)
	for j := 0; j < nTools; j++ {
		var pending []string
		for k := j + 2; k <= nTools; k++ {
			pending = append(pending, seq.SubStepID(runID, 1, k))
		}
		cps = append(cps, agentruntime.Checkpoint{
			RunID:             runID,
			StepID:            t1,
			Turn:              1,
			Phase:             agentruntime.PhaseDispatched,
			ConfirmedStepID:   seq.SubStepID(runID, 1, j+1),
			PendingActivities: pending,
		})
	}
	cps = append(cps, turnLevel(1, agentruntime.PhaseVerified))

	// Turno 2 — final, sem tools.
	cps = append(cps,
		turnLevel(2, agentruntime.PhaseAssembled),
		turnLevel(2, agentruntime.PhaseModelCalled),
		turnLevel(2, agentruntime.PhaseTurnRecorded),
		turnLevel(2, agentruntime.PhaseVerified),
	)
	return cps
}

// writeUpTo escreve os primeiros `n` checkpoints (simula um crash APÓS o n-ésimo).
func writeUpTo(t *testing.T, cpr *EventStoreCheckpointer, cps []agentruntime.Checkpoint, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if err := cpr.Checkpoint(ctx, cps[i]); err != nil {
			t.Fatalf("Checkpoint[%d]: %v", i, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Recuperação: crash em 3+ pontos distintos de uma iteração multi-passo
// ---------------------------------------------------------------------------

func TestResume_CrashPoints(t *testing.T) {
	seq := NewStepSequencer()
	const nTools = 3
	cps := iterationCheckpoints(seq, testRun, nTools)

	s1 := seq.StepID(testRun, 1) // step-000001
	s2 := seq.StepID(testRun, 2) // step-000002
	tool2 := seq.SubStepID(testRun, 1, 2)
	tool3 := seq.SubStepID(testRun, 1, 3)

	// A ordem de cps é: [0]assembled [1]model_called [2]turn_recorded
	// [3]dispatched-tool-1 [4]dispatched-tool-2 [5]dispatched-tool-3 [6]verified
	// [7]t2-assembled [8]t2-model_called [9]t2-turn_recorded [10]t2-verified
	tests := []struct {
		name        string
		writeN      int // nº de checkpoints escritos antes do crash
		wantTurn    int
		wantNext    string
		wantPending []string
	}{
		{"crash após turn_recorded (antes de despachar)", 3, 1, s1, nil},
		{"crash após despachar tool-1", 4, 1, tool2, []string{tool2, tool3}},
		{"crash após despachar tool-2", 5, 1, tool3, []string{tool3}},
		{"crash após despachar tool-3 (verify pendente)", 6, 1, s1, nil},
		{"crash após verified do turno 1", 7, 2, s2, nil},
		{"crash a meio do turno 2 (assembled)", 8, 2, s2, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newStore(t)
			cpr, err := NewCheckpointer(store)
			if err != nil {
				t.Fatalf("NewCheckpointer: %v", err)
			}
			writeUpTo(t, cpr, cps, tc.writeN)

			resumer, err := NewResumer(store, WithStepIdentity(seq))
			if err != nil {
				t.Fatalf("NewResumer: %v", err)
			}
			rp, err := resumer.Resume(context.Background(), testRun)
			if err != nil {
				t.Fatalf("Resume: %v", err)
			}
			if rp.FromScratch {
				t.Fatalf("não devia ser FromScratch (há %d checkpoints)", tc.writeN)
			}
			if rp.NextTurn != tc.wantTurn {
				t.Fatalf("NextTurn = %d, esperava %d", rp.NextTurn, tc.wantTurn)
			}
			if rp.NextStepID != tc.wantNext {
				t.Fatalf("NextStepID = %q, esperava %q", rp.NextStepID, tc.wantNext)
			}
			if !reflect.DeepEqual(rp.PendingActivities, tc.wantPending) {
				t.Fatalf("PendingActivities = %v, esperava %v", rp.PendingActivities, tc.wantPending)
			}

			// Não repete os já confirmados: o próximo passo NUNCA é um step_id de uma
			// activity já confirmada antes da fronteira.
			assertNoRepeat(t, cps, tc.writeN, rp.NextStepID)
		})
	}
}

// assertNoRepeat verifica que NextStepID não corresponde a uma activity JÁ
// confirmada (dispatched) antes do crash — o cerne do resume-from-step.
func assertNoRepeat(t *testing.T, cps []agentruntime.Checkpoint, writeN int, next string) {
	t.Helper()
	for i := 0; i < writeN; i++ {
		if cps[i].Phase == agentruntime.PhaseDispatched && cps[i].ConfirmedStepID == next {
			t.Fatalf("resume aponta a activity JÁ confirmada %q (repetição proibida)", next)
		}
	}
}

// ---------------------------------------------------------------------------
// Acoplamento de formato VERIFICADO: um StepIdentity divergente entre loop e
// Resumer fail-closed (ErrStepIdentityMismatch) em vez de retomar sob outra
// identidade (finding config-coupling AOS-015).
// ---------------------------------------------------------------------------

func TestResume_StepIdentityMismatch(t *testing.T) {
	// O loop escreveu os checkpoints com o formato canónico ("step-000001").
	loopSeq := NewStepSequencer()
	cps := iterationCheckpoints(loopSeq, testRun, 2)

	// Fronteiras representativas: pré-despacho (turn_recorded), mid-dispatch
	// (dispatched-tool-1) e fronteira de turno (verified do turno 1).
	for _, writeN := range []int{3, 4, 6} {
		store := newStore(t)
		cpr, err := NewCheckpointer(store)
		if err != nil {
			t.Fatalf("NewCheckpointer: %v", err)
		}
		writeUpTo(t, cpr, cps, writeN)

		// O Resumer usa um formato INCOMPATÍVEL (prefixo/largura custom).
		divergent := NewStepSequencer(WithPrefix("s-"), WithWidth(3))
		resumer, err := NewResumer(store, WithStepIdentity(divergent))
		if err != nil {
			t.Fatalf("NewResumer: %v", err)
		}
		if _, err := resumer.Resume(context.Background(), testRun); err != ErrStepIdentityMismatch {
			t.Fatalf("writeN=%d: erro = %v, esperava ErrStepIdentityMismatch", writeN, err)
		}
	}

	// Controlo: o MESMO formato do loop retoma sem erro (o guard não é um falso positivo).
	store := newStore(t)
	cpr, _ := NewCheckpointer(store)
	writeUpTo(t, cpr, cps, 4)
	resumer, _ := NewResumer(store, WithStepIdentity(NewStepSequencer()))
	if _, err := resumer.Resume(context.Background(), testRun); err != nil {
		t.Fatalf("formato coincidente não devia falhar: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Sem checkpoint → retoma do início (step 0 / turno 1)
// ---------------------------------------------------------------------------

func TestResume_FromScratch(t *testing.T) {
	seq := NewStepSequencer()

	t.Run("stream inexistente", func(t *testing.T) {
		store := newStore(t)
		resumer, _ := NewResumer(store, WithStepIdentity(seq))
		rp, err := resumer.Resume(context.Background(), "run_sem_nada")
		if err != nil {
			t.Fatalf("Resume: %v", err)
		}
		if !rp.FromScratch || rp.NextTurn != 1 || rp.NextStepID != seq.StepID("run_sem_nada", 1) {
			t.Fatalf("esperava retoma do início; obtive %+v", rp)
		}
	})

	t.Run("stream com eventos mas sem checkpoints", func(t *testing.T) {
		store := newStore(t)
		// Escreve um turn.recorded (outro tipo) — não é checkpoint.
		if _, err := store.Append(context.Background(), testRun, eventstore.EventInput{
			Type: agentruntime.EventTypeTurnRecorded, RunID: testRun, StepID: seq.StepID(testRun, 1),
			Payload: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatalf("Append: %v", err)
		}
		resumer, _ := NewResumer(store, WithStepIdentity(seq))
		rp, err := resumer.Resume(context.Background(), testRun)
		if err != nil {
			t.Fatalf("Resume: %v", err)
		}
		if !rp.FromScratch || rp.NextTurn != 1 {
			t.Fatalf("esperava FromScratch (nenhum checkpoint); obtive %+v", rp)
		}
	})
}

// ---------------------------------------------------------------------------
// Consistência: o step_id do checkpoint casa com o do ledger de AOS-014
// ---------------------------------------------------------------------------

func TestCheckpoint_ConsistentWithLedger(t *testing.T) {
	seq := NewStepSequencer()
	store := newStore(t)
	ctx := context.Background()

	cpr, err := NewCheckpointer(store)
	if err != nil {
		t.Fatalf("NewCheckpointer: %v", err)
	}
	ledger, err := NewStepLedger(store)
	if err != nil {
		t.Fatalf("NewStepLedger: %v", err)
	}

	// Mesmo passo lógico: a 2ª activity (1-based) do turno 1.
	const turn, activity = 1, 2

	// (a) O ledger regista o efeito sob a sua chave canónica.
	ledgerKey, err := seq.SubKey(testRun, turn, activity)
	if err != nil {
		t.Fatalf("SubKey: %v", err)
	}
	if _, _, err := ledger.Apply(ctx, ledgerKey, func(context.Context) (Result, error) {
		return Result{Status: "ok", Payload: []byte("efeito")}, nil
	}); err != nil {
		t.Fatalf("ledger.Apply: %v", err)
	}
	_, ledgerStepID, err := SplitKey(ledgerKey)
	if err != nil {
		t.Fatalf("SplitKey: %v", err)
	}

	// (b) O checkpoint confirma a MESMA activity (como o loop faria: j+1 = activity).
	cp := agentruntime.Checkpoint{
		RunID:           testRun,
		StepID:          seq.StepID(testRun, turn),
		Turn:            turn,
		Phase:           agentruntime.PhaseDispatched,
		ConfirmedStepID: seq.SubStepID(testRun, turn, activity),
	}
	if err := cpr.Checkpoint(ctx, cp); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	// (c) O ConfirmedStepID persistido no checkpoint == o step_id do ledger.
	cur := lastCheckpointCursor(t, store, testRun)
	if cur.ConfirmedStepID != ledgerStepID {
		t.Fatalf("inconsistência checkpoint↔ledger: checkpoint %q != ledger %q",
			cur.ConfirmedStepID, ledgerStepID)
	}
	if cur.ConfirmedStepID != seq.SubStepID(testRun, turn, activity) {
		t.Fatalf("ConfirmedStepID %q não segue a convenção do StepSequencer", cur.ConfirmedStepID)
	}
	if cur.StepIndex != activity {
		t.Fatalf("StepIndex = %d, esperava %d", cur.StepIndex, activity)
	}
}

// ---------------------------------------------------------------------------
// A idempotency_key do checkpoint é DISTINTA da do turno e da do ledger
// ---------------------------------------------------------------------------

func TestCheckpoint_IdempotencyNamespaceDistinct(t *testing.T) {
	seq := NewStepSequencer()
	store := newStore(t)
	ctx := context.Background()
	cpr, _ := NewCheckpointer(store)

	stepID := seq.StepID(testRun, 1)

	// turn.recorded ocupa run:step-000001.
	if _, err := store.Append(ctx, testRun, eventstore.EventInput{
		Type: agentruntime.EventTypeTurnRecorded, RunID: testRun, StepID: stepID,
		Payload: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("Append turn: %v", err)
	}
	// step.ledger.applied ocupa run:ledger-step-000001.
	ledger, _ := NewStepLedger(store)
	key, _ := seq.Key(testRun, 1)
	if _, _, err := ledger.Apply(ctx, key, func(context.Context) (Result, error) {
		return Result{Status: "ok"}, nil
	}); err != nil {
		t.Fatalf("ledger.Apply: %v", err)
	}

	// O checkpoint da fase turn_recorded do MESMO turno tem de COMMITAR (não dedup),
	// prova de que a sua idempotency_key (run:ckpt-turn_recorded-step-000001) não
	// colide com run:step-000001 (turno) nem run:ledger-step-000001 (ledger).
	before := countType(t, store, testRun, EventTypeCheckpoint)
	if err := cpr.Checkpoint(ctx, agentruntime.Checkpoint{
		RunID: testRun, StepID: stepID, Turn: 1, Phase: agentruntime.PhaseTurnRecorded,
	}); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	after := countType(t, store, testRun, EventTypeCheckpoint)
	if after != before+1 {
		t.Fatalf("checkpoint não committou (colisão de idempotency_key?): antes=%d depois=%d", before, after)
	}
}

// ---------------------------------------------------------------------------
// A própria escrita de checkpoint é idempotente (retry após crash → dedup)
// ---------------------------------------------------------------------------

func TestCheckpoint_RewriteIsIdempotent(t *testing.T) {
	seq := NewStepSequencer()
	store := newStore(t)
	ctx := context.Background()

	obs := &countingCheckpointObs{}
	cpr, _ := NewCheckpointer(store, WithCheckpointObserver(obs))

	cp := agentruntime.Checkpoint{
		RunID: testRun, StepID: seq.StepID(testRun, 1), Turn: 1,
		Phase: agentruntime.PhaseDispatched, ConfirmedStepID: seq.SubStepID(testRun, 1, 1),
	}
	// Primeira escrita: committed. Segunda (retry após crash): duplicate.
	for i := 0; i < 2; i++ {
		if err := cpr.Checkpoint(ctx, cp); err != nil {
			t.Fatalf("Checkpoint #%d: %v", i, err)
		}
	}
	if obs.committed != 1 || obs.deduped != 1 {
		t.Fatalf("esperava 1 committed + 1 dedup, obtive committed=%d dedup=%d", obs.committed, obs.deduped)
	}
	// Um único evento no stream — a re-escrita NÃO duplicou.
	if n := countType(t, store, testRun, EventTypeCheckpoint); n != 1 {
		t.Fatalf("esperava 1 evento de checkpoint, obtive %d", n)
	}
}

// ---------------------------------------------------------------------------
// Validação de entradas
// ---------------------------------------------------------------------------

func TestCheckpoint_InvalidInputs(t *testing.T) {
	store := newStore(t)
	cpr, _ := NewCheckpointer(store)
	ctx := context.Background()

	cases := []struct {
		name string
		cp   agentruntime.Checkpoint
		want error
	}{
		{"run_id vazio", agentruntime.Checkpoint{StepID: "step-000001"}, ErrEmptyRunID},
		{"run_id com ':'", agentruntime.Checkpoint{RunID: "a:b", StepID: "step-000001"}, ErrDelimiterInInput},
		{"sem step_id nem confirmed", agentruntime.Checkpoint{RunID: testRun, Turn: 1}, ErrEmptyStepID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := cpr.Checkpoint(ctx, tc.cp); err != tc.want {
				t.Fatalf("erro = %v, esperava %v", err, tc.want)
			}
		})
	}

	if _, err := NewCheckpointer(nil); err != ErrNilStore {
		t.Fatalf("NewCheckpointer(nil) = %v, esperava ErrNilStore", err)
	}
	if _, err := NewResumer(nil); err != ErrNilStore {
		t.Fatalf("NewResumer(nil) = %v, esperava ErrNilStore", err)
	}
	resumer, _ := NewResumer(store)
	if _, err := resumer.Resume(ctx, ""); err != ErrEmptyRunID {
		t.Fatalf("Resume(\"\") = %v, esperava ErrEmptyRunID", err)
	}
}

// ---------------------------------------------------------------------------
// Auxiliares de leitura do Event Store
// ---------------------------------------------------------------------------

func lastCheckpointCursor(t *testing.T, store *eventstore.Store, runID string) Cursor {
	t.Helper()
	events, err := store.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == EventTypeCheckpoint {
			var cur Cursor
			if err := json.Unmarshal(events[i].Payload, &cur); err != nil {
				t.Fatalf("unmarshal cursor: %v", err)
			}
			return cur
		}
	}
	t.Fatalf("nenhum evento de checkpoint no stream %s", runID)
	return Cursor{}
}

func countType(t *testing.T, store *eventstore.Store, runID, typ string) int {
	t.Helper()
	events, err := store.Read(context.Background(), runID, 1)
	if err != nil {
		if err == eventstore.ErrStreamNotFound {
			return 0
		}
		t.Fatalf("Read: %v", err)
	}
	n := 0
	for _, e := range events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// countingCheckpointObs conta committed/dedup para asserção de observabilidade.
type countingCheckpointObs struct {
	committed, deduped int
	lastHash           string
}

func (o *countingCheckpointObs) Checkpointed(h string)           { o.committed++; o.lastHash = h }
func (o *countingCheckpointObs) CheckpointDeduplicated(h string) { o.deduped++; o.lastHash = h }
