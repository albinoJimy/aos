package autonomy

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// AOS-306, a metade que faltava — a DEMOÇÃO tem de tratar a selagem falhada como a promoção.
//
// `Controller.Evaluate` foi corrigido para não reverter (a selagem passou a vir antes do efeito,
// pelo que não há concessão a desfazer). `OnAnomaly` ficou como estava e devolvia `changed=true`
// com uma `LevelChange{}` vazia quando o sink falhava: dizia que tinha rebaixado sem ter
// rebaixado, e `emitTransition` emitiria um span de transição `L0→L0` com agente vazio. É a
// assinatura do defeito que AOS-306 existe para fechar, no ramo vizinho — achado de revisão
// adversarial (R-04).

// sinkQueRecusa falha toda a selagem — o WORM em baixo.
type sinkQueRecusa struct{}

var errSinkRecusa = errors.New("worm indisponivel (teste)")

func (sinkQueRecusa) SealLevelChange(context.Context, LevelChange) error { return errSinkRecusa }

// TestAOS306_DemocaoComSelagemFalhadaNaoDizQueMudou — o sink falha; a demoção não pode
// reportar-se como feita, e o nível tem de ficar onde estava.
func TestAOS306_DemocaoComSelagemFalhadaNaoDizQueMudou(t *testing.T) {
	cfg := DefaultAutonomyControlConfig()
	// Parte-se do fixture normal (L4, sink que capta) e TROCA-SE o sink por um que recusa, para
	// que a preparação fique selada e só a demoção falhe. É o cenário real: o WORM cai depois de
	// o nó estar a correr.
	reg, ctl, _, _ := newFixture(t, L4, cfg, Reliability{})
	reg.sink = sinkQueRecusa{}

	ch, changed, err := ctl.OnAnomaly(context.Background(), "agent-1", "http", AnomalyOverrideRateSpike)
	if !errors.Is(err, errSinkRecusa) {
		t.Fatalf("o erro do sink tem de subir, veio: %v", err)
	}
	if changed {
		t.Error("changed=true com a selagem falhada — o chamador registaria uma democao que NAO aconteceu")
	}
	if !reflect.DeepEqual(ch, LevelChange{}) {
		t.Errorf("LevelChange devia vir vazia, veio %+v — um span de transicao seria emitido com agente vazio", ch)
	}
	if got := reg.LevelFor("agent-1", "http"); got != L4 {
		t.Errorf("o nivel mudou para %s apesar de a selagem ter falhado; quero L4", got)
	}
}

// TestAOS306_DemocaoComSelagemOKContinuaAFuncionar — o CONTROLO. Sem ele, um `return` que
// recusasse sempre passaria no teste acima e a demoção automática deixaria de existir em
// silêncio — que é pior do que o defeito que se está a fechar.
func TestAOS306_DemocaoComSelagemOKContinuaAFuncionar(t *testing.T) {
	cfg := DefaultAutonomyControlConfig()
	reg, ctl, _, _ := newFixture(t, L4, cfg, Reliability{})

	ch, changed, err := ctl.OnAnomaly(context.Background(), "agent-1", "http", AnomalyOverrideRateSpike)
	if err != nil || !changed {
		t.Fatalf("com selagem OK a democao devia acontecer: changed=%v err=%v", changed, err)
	}
	if ch.Old != L4 || ch.New != L2 || ch.Agent != "agent-1" {
		t.Errorf("a democao nao baixou o nivel ou nao nomeia o par: %+v", ch)
	}
	if got := reg.LevelFor("agent-1", "http"); got != L2 {
		t.Errorf("nivel apos democao = %s, quero L2", got)
	}
}
