package episodic

import (
	"context"
	"testing"

	"github.com/aos-ref/platform/memory/domain"
)

// TestAOS478_EpisodioLevaOAgente (AOS-478): `memory.episode.recorded` leva no envelope o
// agente autor do episódio.
func TestAOS478_EpisodioLevaOAgente(t *testing.T) {
	es := newES(t)
	s, _, _ := newStore(t, es)
	ctx := context.Background()
	mustEnqueue(t, s, baseInput("ep-478", "subj-478", "run-478", "objetivo", []string{"t1"}, domain.TTLPermanent, 2))
	if _, err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	evs, err := es.Read(ctx, EpisodicStreamID, 1)
	if err != nil || len(evs) != 1 {
		t.Fatalf("Read: err=%v n=%d", err, len(evs))
	}
	if evs[0].Type != EventTypeEpisodeRecorded || evs[0].Producer.NHIID != "agent-1" {
		t.Fatalf("%s producer=%+v, quer o agente autor agent-1", evs[0].Type, evs[0].Producer)
	}
}
