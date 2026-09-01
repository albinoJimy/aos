package jetstream_test

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
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

// TestReconexao_SubscricaoRECUPERAOIntervalo é o que o consumidor durável fecha, e a
// razão de ele existir.
//
// Com um consumidor EFÉMERO, a subscrição RETOMAVA depois de uma quebra mas os eventos
// escritos NO INTERVALO perdiam-se — um buraco silencioso no fluxo, que é a pior forma de
// perder eventos porque ninguém dá por ela. Com um DURÁVEL, o servidor sabe até onde a
// entrega foi confirmada e recomeça aí.
//
// A diferença entre RETOMAR e RECUPERAR é exactamente esta, e é o que este teste mede.
func TestReconexao_SubscricaoRECUPERAOIntervalo(t *testing.T) {
	addr := servidor(t)
	matar := os.Getenv(envKillLigado)
	if matar == "" {
		t.Skipf("define %s", envKillLigado)
	}
	if restaurar := os.Getenv(envRestore); restaurar != "" {
		t.Cleanup(func() { correrComando(t, "restauro", restaurar) })
	}

	st, err := abrirComOpcoes(t, addr, opcoesBase(t, "RECSUB_")...)
	if err != nil {
		t.Fatalf("abrir: %v", err)
	}
	ctx := context.Background()
	const stream = "run-intervalo"

	recebidos := make(chan string, 64)
	sub, err := st.Subscribe(ctx, eventstore.Filter{Types: []string{"recon.intervalo"}},
		func(ev eventstore.Event) { recebidos <- string(ev.Payload) })
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Unsubscribe()
	time.Sleep(time.Second)

	escrever := func(marca string) error {
		_, err := st.Append(ctx, stream, eventstore.EventInput{
			Type: "recon.intervalo", Payload: json.RawMessage(`{"m":"` + marca + `"}`),
		})
		return err
	}

	if err := escrever("antes"); err != nil {
		t.Fatalf("escrita antes: %v", err)
	}
	select {
	case <-recebidos:
	case <-time.After(10 * time.Second):
		t.Fatal("o evento anterior à falha nem sequer chegou — a subscrição não está a funcionar")
	}

	correrComando(t, "matar o nó da ligação", matar)

	// ESCRITAS NO INTERVALO: é isto que um efémero perderia.
	esperadas := map[string]bool{}
	for i := 0; i < 3; i++ {
		marca := "intervalo-" + strconv.Itoa(i)
		for tent := 0; tent < 15; tent++ {
			if err := escrever(marca); err == nil {
				esperadas[marca] = true
				break
			}
			time.Sleep(time.Second)
		}
	}
	if len(esperadas) == 0 {
		t.Fatal("nenhuma escrita passou depois da falha — o problema é a reconexão, não a subscrição")
	}

	// E têm de CHEGAR, sem reiniciar nada.
	prazoFinal := time.After(45 * time.Second)
	for len(esperadas) > 0 {
		select {
		case p := <-recebidos:
			for m := range esperadas {
				if strings.Contains(p, `"`+m+`"`) {
					delete(esperadas, m)
				}
			}
		case <-prazoFinal:
			t.Fatalf("%d evento(s) escritos no intervalo NUNCA chegaram: %v — a subscrição retomou mas não recuperou, "+
				"que é um buraco silencioso no fluxo de eventos", len(esperadas), esperadas)
		}
	}
	t.Logf("todos os eventos escritos durante a quebra foram ENTREGUES depois dela — a subscrição recuperou, não só retomou")
}
