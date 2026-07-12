package harness

import (
	"context"
	"encoding/json"
	"strings"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	"github.com/aos-ref/substrate/eventstore"
)

// sanitize troca caracteres problemáticos por '_' para compor run_ids de teste.
func sanitize(s string) string {
	r := strings.NewReplacer(" ", "_", "(", "", ")", "", ":", "", "/", "_", ".", "_")
	return r.Replace(s)
}

// turnRecordedPayload é a projecção mínima do payload "turn.recorded" de que o
// [manifestMutatingReader] precisa (turno + manifesto). Casa com as tags JSON do
// agentruntime turnPayload/Manifest.
type turnRecordedPayload struct {
	Turn     int                   `json:"turn"`
	Manifest agentruntime.Manifest `json:"manifest"`
}

// manifestMutatingReader embrulha um [replay.EventReader] e reescreve o manifesto
// dos eventos "turn.recorded" ANTES de os entregar ao motor de replay. Simula uma
// adulteração LOCALIZADA num turno específico (p.ex. o prompt_hash gravado do turno
// 2) sem tocar nos restantes — o único acesso continua a ser Read (zero-efeitos).
type manifestMutatingReader struct {
	inner  replay.EventReader
	mutate func(turn int, m *agentruntime.Manifest)
}

func (r *manifestMutatingReader) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	events, err := r.inner.Read(ctx, streamID, fromSeq)
	if err != nil {
		return nil, err
	}
	out := make([]eventstore.Event, len(events))
	copy(out, events)
	for i := range out {
		if out[i].Type != agentruntime.EventTypeTurnRecorded {
			continue
		}
		var p turnRecordedPayload
		if err := json.Unmarshal(out[i].Payload, &p); err != nil {
			return nil, err
		}
		r.mutate(p.Turn, &p.Manifest)
		raw, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		out[i].Payload = raw
	}
	return out, nil
}
