package jetstream_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// reconexao_test.go — o buraco que o AC1 tem enquanto o cliente não reconectar.
//
// O AC1 diz «a perda de uma réplica não perde dados NEM INTERROMPE ESCRITAS». O
// TestAC4 prova-o — mas o script de falha dele evita deliberadamente matar o nó a que o
// cliente está ligado. Ou seja: a propriedade só está provada para a metade sortuda dos
// casos.
//
// Este teste mede a outra metade.
const envKillLigado = "AOS_KILL_CONNECTED_CMD"

// TestReconexao_MorteDoNoDaLigacao mede o que acontece quando morre O NÓ A QUE O CLIENTE
// ESTÁ LIGADO — e não um qualquer.
//
// A propriedade que se quer: a escrita seguinte pode falhar (a ligação partiu-se a meio),
// mas as ESCRITAS RETOMAM sem reiniciar o processo. Um cliente que fique morto até alguém
// o reiniciar transforma a morte de UM nó num incidente do NÓ INTEIRO — que é exactamente
// o que o Event Store replicado existe para evitar.
func TestReconexao_MorteDoNoDaLigacao(t *testing.T) {
	addr := servidor(t)
	matar := os.Getenv(envKillLigado)
	if matar == "" {
		t.Skipf("define %s (comando que mata o nó a que %s aponta)", envKillLigado, envServidor)
	}
	if restaurar := os.Getenv(envRestore); restaurar != "" {
		t.Cleanup(func() { correrComando(t, "restauro", restaurar) })
	}

	st, err := abrirComOpcoes(t, addr, opcoesBase(t, "RECON_")...)
	if err != nil {
		t.Fatalf("abrir: %v", err)
	}
	ctx := context.Background()
	const stream = "run-reconexao"
	escrever := func() error {
		_, err := st.Append(ctx, stream, eventstore.EventInput{
			Type: "recon.escrita", Payload: json.RawMessage(`{}`),
		})
		return err
	}

	if err := escrever(); err != nil {
		t.Fatalf("escrita antes da falha: %v", err)
	}

	correrComando(t, "matar o nó da ligação", matar)

	// A escrita imediata PODE falhar — a ligação partiu-se. Isso não é o defeito.
	if err := escrever(); err != nil {
		t.Logf("escrita imediata após a falha: %v (esperado — a ligação partiu-se)", err)
	}

	// O DEFEITO seria ficar assim para sempre. Dá-se tempo ao cliente para se curar.
	var ultimo error
	for i := 0; i < 20; i++ {
		time.Sleep(time.Second)
		if ultimo = escrever(); ultimo == nil {
			t.Logf("escritas RETOMARAM %ds depois da morte do nó da ligação, sem reiniciar o processo", i+1)
			return
		}
	}
	t.Fatalf("20s depois da morte do nó da ligação as escritas continuam a falhar (%v) — "+
		"o cliente não se cura, e a morte de UM nó vira um incidente do NÓ INTEIRO, "+
		"que é o oposto do que o Event Store replicado existe para dar", ultimo)
}
