package replan

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// --- Fakes STATELESS (sem contadores partilhados ⇒ sem data-races próprias sob -race:
// qualquer race que o detector aponte é do código sob teste, não dos duplos). ---

// barrierBudget bloqueia toda a Reserve até o teste fechar `release`. É o seam que
// TORNA A CORRIDA DETERMINISTA: como Reserve é o passo IMEDIATAMENTE a seguir ao
// guard de nível (pinAndCheckLevel), reter todas as goroutines aqui garante que TODAS
// já passaram o guard antes de qualquer uma prosseguir para o pin/contagem (bump). No
// código antigo (check separado do pin), todas viam ts==nil e passavam; depois de
// libertadas, a que fixasse um nível baixo primeiro deixava as de nível alto escapar —
// a escalada por corrida. No código novo o pin é ATÓMICO com o check, a montante da
// Reserve, pelo que a barreira nada muda: o invariante mantém-se.
type barrierBudget struct{ release chan struct{} }

func (b *barrierBudget) Reserve(context.Context, string, Amount) (Reservation, error) {
	<-b.release
	return Reservation{}, nil
}
func (b *barrierBudget) Commit(context.Context, Reservation) error  { return nil }
func (b *barrierBudget) Release(context.Context, Reservation) error { return nil }

type statelessGate struct{}

func (statelessGate) Review(context.Context, GateRequest) (GateOutcome, error) {
	return GateOutcome{Approved: true}, nil
}

type statelessSched struct{}

func (statelessSched) Suspend(context.Context, string, []string) error { return nil }
func (statelessSched) Resume(context.Context, string, []string) error  { return nil }

type statelessRec struct{}

func (statelessRec) RecordReplan(context.Context, plannerevents.ReplanPayload) (uint64, error) {
	return 1, nil
}

// TestReplan_ConcurrentFirstInvocations_NoAutonomyEscalation fecha o TOCTOU de escalada
// de autonomia por corrida (residual LOW declarado na entrega da vaga 6). Muitas PRIMEIRAS
// invocações CONCORRENTES da MESMA árvore, com níveis MISTOS, correm sobre um Coordinator
// fresco por iteração; uma barreira na Reserve garante que TODAS passam o guard de nível
// antes de qualquer pin. Invariante verificado: NENHUM re-plano ACEITE (err==nil) tem
// Level > o originalLevel FIXADO da árvore.
//
// Falha-antes (VERIFICADA por reversão para o `checkPinnedLevel` antigo: FALHA): com o check
// (nil em ts==nil) SEPARADO do pin (bump, a jusante da Reserve), a barreira faz TODAS passarem
// o check; ao libertar, a que fixa um nível baixo primeiro deixa as de nível alto prosseguir
// para o gate com Level>fixado — escalada. pinAndCheckLevel faz check-and-pin numa só secção
// crítica, a montante da Reserve, fechando-o. -race verde.
func TestReplan_ConcurrentFirstInvocations_NoAutonomyEscalation(t *testing.T) {
	t.Parallel()
	cfg := Config{MaxReplansPerTree: 1000}
	levels := []Level{L5, L0, L4, L1, L5, L2, L3, L0, L4, L1}

	for it := 0; it < 50; it++ {
		bud := &barrierBudget{release: make(chan struct{})}
		c, err := NewCoordinator(bud, statelessGate{}, statelessSched{}, statelessRec{}, cfg)
		if err != nil {
			t.Fatalf("iter %d: NewCoordinator: %v", it, err)
		}

		type outcome struct {
			lvl      Level
			accepted bool
		}
		results := make([]outcome, len(levels))
		var wg sync.WaitGroup
		for i, lv := range levels {
			wg.Add(1)
			go func(i int, lv Level) {
				defer wg.Done()
				req := baseReq()
				req.PlanID = fmt.Sprintf("plan-%d", i)
				req.OriginalLevel = lv
				req.Level = lv // primeira invocação: Level == OriginalLevel (válido por-pedido)
				_, rerr := c.Replan(context.Background(), req)
				results[i] = outcome{lvl: lv, accepted: rerr == nil}
			}(i, lv)
		}
		// Deixa todas as goroutines chegar à barreira (passado o guard de nível) e só
		// então liberta-as em conjunto — força o interleave que expõe o TOCTOU no código antigo.
		time.Sleep(15 * time.Millisecond)
		close(bud.release)
		wg.Wait()

		pinned, ok := c.PinnedLevel("tree-1")
		if !ok {
			t.Fatalf("iter %d: a árvore devia ter ficado fixada por alguma invocação", it)
		}
		for i, r := range results {
			if r.accepted && r.lvl > pinned {
				t.Fatalf("iter %d: re-plano %d ACEITE com Level=%s > originalLevel fixado=%s — escalada de autonomia por corrida (TOCTOU)",
					it, i, r.lvl, pinned)
			}
		}
	}
}
