package jetstream_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/jetstream"
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

	// ONDE está o consumidor fica escrito ANTES da falha. Este teste mata o nó da LIGAÇÃO, e
	// o servidor sorteia o par de um consumidor R1: uma em cada três execuções, é o mesmo
	// nó. Sem esta linha essa execução lê-se como flake (AOS-449) — com ela, lê-se a causa.
	if lider, err := liderDoConsumidorDaSubscricao(t, st); err == nil {
		t.Logf("consumidor da subscrição alojado em %q (antes da falha)", lider)
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

// envKillNo é o PREFIXO de um comando que mata um nó pelo NOME que o servidor anuncia
// (ex.: `docker stop`); o teste acrescenta o nome. Existe porque o nó a matar só se sabe
// DEPOIS de o servidor colocar o consumidor — não há comando fixo que o acerte.
const envKillNo = "AOS_KILL_NODE_CMD"

// nomeDeNoValido restringe o que se acrescenta ao comando de falha: o nome vem do servidor
// e vai para um `sh -c`.
var nomeDeNoValido = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// liderDoConsumidorDaSubscricao devolve o nó que aloja o ÚNICO consumidor do stream do
// teste — o da subscrição, porque o stream é deste teste e de mais ninguém.
func liderDoConsumidorDaSubscricao(t *testing.T, st *jetstream.Store) (string, error) {
	t.Helper()
	nomes, err := st.ConsumidoresDoStream()
	if err != nil {
		return "", err
	}
	if len(nomes) != 1 {
		return "", fmt.Errorf("esperava um consumidor no stream, há %d: %v", len(nomes), nomes)
	}
	return st.LiderDoConsumidor(nomes[0])
}

// TestReconexao_MorteDoNoDoConsumidor é a versão DETERMINISTA do teste anterior, e é a que
// prova o AOS-449.
//
// O TestReconexao_SubscricaoRECUPERAOIntervalo mata o nó da LIGAÇÃO. O consumidor da
// subscrição é R1 e o servidor sorteia-lhe o par, pelo que esse teste só às vezes mata o nó
// do consumidor — e era aí, e só aí, que ele falhava: o evento escrito no intervalo NUNCA
// chegava. Lido como flake, era um defeito com probabilidade 1/3 por execução.
//
// O mecanismo, lido no nats-server v2.10 e não suposto: o `CREATE` de reafirmação de um
// consumidor que já existe REUTILIZA os mesmos pares (`ca.copyGroup()`), e, com o único
// par morto, ninguém responde. O servidor não move um R1 órfão sozinho. A subscrição ficava
// a reafirmar para sempre um consumidor que não pode voltar enquanto o nó não voltar.
//
// Aqui mata-se o nó do consumidor TODAS as vezes. Sem a recuperação, isto falha sempre.
func TestReconexao_MorteDoNoDoConsumidor(t *testing.T) {
	addr := servidor(t)
	matarNo := os.Getenv(envKillNo)
	if matarNo == "" {
		t.Skipf("define %s (prefixo de comando que mata um nó pelo nome, ex.: docker stop)", envKillNo)
	}
	if restaurar := os.Getenv(envRestore); restaurar != "" {
		t.Cleanup(func() { correrComando(t, "restauro", restaurar) })
	}

	st, err := abrirComOpcoes(t, addr, opcoesBase(t, "RECNO_")...)
	if err != nil {
		t.Fatalf("abrir: %v", err)
	}
	ctx := context.Background()
	const stream = "run-no-do-consumidor"

	recebidos := make(chan string, 256)
	sub, err := st.Subscribe(ctx, eventstore.Filter{Types: []string{"recon.no"}},
		func(ev eventstore.Event) { recebidos <- string(ev.Payload) })
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Unsubscribe()
	time.Sleep(time.Second)

	escrever := func(marca string) error {
		_, err := st.Append(ctx, stream, eventstore.EventInput{
			Type: "recon.no", Payload: json.RawMessage(`{"m":"` + marca + `"}`),
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

	lider, err := liderDoConsumidorDaSubscricao(t, st)
	if err != nil {
		t.Fatalf("onde está o consumidor: %v", err)
	}
	if !nomeDeNoValido.MatchString(lider) {
		t.Fatalf("nome de nó do consumidor inválido para o comando de falha: %q", lider)
	}
	correrComando(t, "matar o nó do consumidor ("+lider+")", matarNo+" "+lider)

	esperadas := map[string]bool{}
	for i := 0; i < 3; i++ {
		marca := "no-" + strconv.Itoa(i)
		for tent := 0; tent < 15; tent++ {
			if err := escrever(marca); err == nil {
				esperadas[marca] = true
				break
			}
			time.Sleep(time.Second)
		}
	}
	if len(esperadas) == 0 {
		t.Fatal("nenhuma escrita passou depois da falha — o stream R3 devia sobreviver à perda de um nó")
	}

	// O prazo cobre, no pior caso, a detecção do silêncio (15 s), a reafirmação que expira
	// sem resposta (o prazo do store) e a recriação num par vivo — com folga.
	prazoFinal := time.After(90 * time.Second)
	for len(esperadas) > 0 {
		select {
		case p := <-recebidos:
			for m := range esperadas {
				if strings.Contains(p, `"`+m+`"`) {
					delete(esperadas, m)
				}
			}
		case <-prazoFinal:
			t.Fatalf("%d evento(s) escritos depois da morte do nó do consumidor (%s) NUNCA chegaram: %v — "+
				"o consumidor R1 ficou órfão no nó morto e a subscrição não o recolocou", len(esperadas), lider, esperadas)
		}
	}
	t.Logf("morreu o nó do consumidor (%s) e os eventos posteriores foram ENTREGUES — o consumidor foi recolocado num par vivo", lider)
}

// TestSubscricao_SilencioDoConsumidorEDETECTADO mede a falha SILENCIOSA — a única contra a
// qual todo este pacote foi desenhado, e a última que ainda estava por cobrir.
//
// # O cenário
//
// O consumidor da subscrição morre DO LADO DO SERVIDOR — o nó R1 que o alojava cai, ou
// alguém o apaga. Do lado do cliente nada acontece: a ligação está viva, o canal está
// aberto, o `SUB` continua registado. Simplesmente **não chega nada**.
//
// Sem batimento, isso é indistinguível de um stream sossegado, e a subscrição fica morta
// para sempre sem ninguém saber. Com batimento, a ausência dele é um SINAL: ao fim de
// `silencioMaximo` o consumidor é dado por morto e a entrega re-estabelecida.
//
// # O que se aceita, e é preciso dizê-lo
//
// O consumidor recriado parte do seq FIXADO na subscrição, pelo que os eventos desde então
// são REENTREGUES. É at-least-once — nada se perde, algumas coisas repetem-se. Para um
// Event Store cuja idempotência é por `(run_id, step_id)` essa é a troca certa; perder era
// a alternativa, e é pior.
func TestSubscricao_SilencioDoConsumidorEDETECTADO(t *testing.T) {
	addr := servidor(t)
	st, err := abrirComOpcoes(t, addr, opcoesBase(t, "SILENCIO_")...)
	if err != nil {
		t.Fatalf("abrir: %v", err)
	}
	ctx := context.Background()
	const stream = "run-silencio"

	recebidos := make(chan string, 64)
	sub, err := st.Subscribe(ctx, eventstore.Filter{Types: []string{"silencio.evento"}},
		func(ev eventstore.Event) { recebidos <- string(ev.Payload) })
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Unsubscribe()
	time.Sleep(time.Second)

	escrever := func(marca string) {
		t.Helper()
		if _, err := st.Append(ctx, stream, eventstore.EventInput{
			Type: "silencio.evento", Payload: json.RawMessage(`{"m":"` + marca + `"}`),
		}); err != nil {
			t.Fatalf("escrever %s: %v", marca, err)
		}
	}
	esperar := func(marca string, prazo time.Duration) bool {
		t.Helper()
		limite := time.After(prazo)
		for {
			select {
			case p := <-recebidos:
				if strings.Contains(p, `"`+marca+`"`) {
					return true
				}
			case <-limite:
				return false
			}
		}
	}

	escrever("antes")
	if !esperar("antes", 15*time.Second) {
		t.Fatal("o evento anterior não chegou — a subscrição não está a funcionar")
	}

	// A MORTE SILENCIOSA: apaga-se o consumidor pelas costas do subscritor.
	nomes, err := st.ConsumidoresDoStream()
	if err != nil {
		t.Fatalf("listar consumidores: %v", err)
	}
	if len(nomes) == 0 {
		t.Fatal("o stream não tem consumidores — a subscrição não criou nenhum")
	}
	for _, n := range nomes {
		if err := st.ApagarConsumidor(n); err != nil {
			t.Fatalf("apagar o consumidor %q: %v", n, err)
		}
	}
	t.Logf("consumidor(es) %v apagado(s) do lado do servidor — o cliente não foi avisado de nada", nomes)

	// Agora escreve-se. Sem detecção do silêncio, isto nunca chegaria.
	escrever("depois-do-silencio")
	if !esperar("depois-do-silencio", 90*time.Second) {
		t.Fatal("o evento escrito depois da morte silenciosa do consumidor NUNCA chegou — " +
			"a subscrição ficou morta sem ninguém saber, que é a falha que o batimento existe para tornar detectável")
	}
	t.Logf("silêncio DETECTADO e entrega re-estabelecida: o evento posterior chegou sem ninguém reiniciar nada")
}
