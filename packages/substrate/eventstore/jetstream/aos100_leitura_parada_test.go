package jetstream

// aos100_leitura_parada_test.go — o segundo mecanismo do TestAOS100 (a leitura parada), medido no
// gate nats a 2026-10-19 (run 39301696184): «leitura de "…lease:run-paralelo" parou em 0 de 1
// eventos ao fim de 10s» em dois de quatro processos. A leitura parada recoloca-se num consumidor
// novo, até tentativasDeLeitura; outro erro sobe à primeira.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestAOS100_LeituraParadaRecolocaNumConsumidorNovo(t *testing.T) {
	chamadas := 0
	lido, err := recolocarLeituraParada(context.Background(), func() (loteLido, error) {
		chamadas++
		if chamadas < 3 {
			return loteLido{}, fmt.Errorf("%w: parou em 0 de 1", errLeituraParada)
		}
		return loteLido{ultimoSeq: 7}, nil
	})
	if err != nil || chamadas != 3 || lido.ultimoSeq != 7 {
		t.Fatalf("a terceira tentativa entrega: err=%v chamadas=%d lido=%+v", err, chamadas, lido)
	}
}

func TestAOS100_LeituraParadaSempreSobeComAContagem(t *testing.T) {
	chamadas := 0
	_, err := recolocarLeituraParada(context.Background(), func() (loteLido, error) {
		chamadas++
		return loteLido{}, fmt.Errorf("%w: parou em 0 de 1", errLeituraParada)
	})
	if !errors.Is(err, errLeituraParada) || chamadas != tentativasDeLeitura || !strings.Contains(err.Error(), "consumidores tentados") {
		t.Fatalf("parada em todas: err=%v chamadas=%d", err, chamadas)
	}
}

func TestAOS100_OutroErroNaoRecoloca(t *testing.T) {
	outro := errors.New("envelope ilegivel")
	chamadas := 0
	_, err := recolocarLeituraParada(context.Background(), func() (loteLido, error) {
		chamadas++
		return loteLido{}, outro
	})
	if !errors.Is(err, outro) || chamadas != 1 {
		t.Fatalf("outro erro sobe a primeira: err=%v chamadas=%d", err, chamadas)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	chamadas = 0
	_, err = recolocarLeituraParada(ctx, func() (loteLido, error) {
		chamadas++
		return loteLido{}, fmt.Errorf("%w: parou", errLeituraParada)
	})
	if chamadas != 1 || err == nil {
		t.Fatalf("com o contexto cancelado nao se recoloca: err=%v chamadas=%d", err, chamadas)
	}
}
