package replay

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// captureDroppingReader embrulha um EventReader e SUPRIME o evento "replay.captured"
// de um turno-alvo — simula uma trajectória cujo não-determinismo NÃO foi capturado
// nesse passo (o Capturer não estava ligado/falhou). O único acesso continua a ser
// Read (zero-efeitos preservado).
type captureDroppingReader struct {
	inner    EventReader
	dropTurn int
}

func (r *captureDroppingReader) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	events, err := r.inner.Read(ctx, streamID, fromSeq)
	if err != nil {
		return nil, err
	}
	out := make([]eventstore.Event, 0, len(events))
	for _, ev := range events {
		if ev.Type == EventTypeCaptured {
			var p capturePayload
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				return nil, err
			}
			if p.Turn == r.dropTurn {
				continue // suprime a captura deste turno
			}
		}
		out = append(out, ev)
	}
	return out, nil
}

// GATE DE ADMISSÃO (CA4) — um turno SEM captura de não-determinismo torna o replay
// INADMISSÍVEL: recusa fail-closed com ErrIncompleteCapture em vez de reproduzir a
// baixa fidelidade silenciosamente. "Fidelidade é condição, não opção."
func TestReplayAdmissionRejectsMissingCapture(t *testing.T) {
	or := runOriginal(t, "run_admit_missing")
	reader := &captureDroppingReader{inner: or.store, dropTurn: 2}
	e, err := NewEngine(reader)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if _, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec}); !errors.Is(err, ErrIncompleteCapture) {
		t.Fatalf("captura em falta devia ser INADMISSÍVEL (ErrIncompleteCapture), obtive %v", err)
	}
}

// Um manifesto sem prompt_hash (manifesto incompleto) também é inadmissível — o
// replay não teria âncora para verificar a fidelidade do passo.
func TestReplayAdmissionRejectsIncompleteManifest(t *testing.T) {
	or := runOriginal(t, "run_admit_manifest")
	reader := &manifestMutatingReader{
		inner: or.store,
		mutate: func(turn int, m *agentruntime.Manifest) {
			if turn == 2 {
				m.PromptHash = "" // manifesto incompleto
			}
		},
	}
	e, err := NewEngine(reader)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if _, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec}); !errors.Is(err, ErrIncompleteCapture) {
		t.Fatalf("manifesto incompleto devia ser INADMISSÍVEL, obtive %v", err)
	}
}

// Positivo — um run COMPLETAMENTE capturado é admissível e reproduz 100% dos passos.
// É o outro lado do gate: fidelidade condicionada, não recusa cega.
func TestReplayFullyCapturedIsAdmissible(t *testing.T) {
	or := runOriginal(t, "run_admit_ok")
	e := mustEngine(t, or)
	res, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec})
	if err != nil {
		t.Fatalf("run completo devia ser ADMISSÍVEL: %v", err)
	}
	if res.Fidelity != 1.0 || len(res.Steps) != 3 {
		t.Fatalf("run completo devia reproduzir 100%%: fid=%v steps=%d", res.Fidelity, len(res.Steps))
	}
}
