package bus

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestBackpressureBlockNaoBloqueiaProdutorNemOutros cobre o critério: um
// consumidor lento (política Block, buffer pequeno) não bloqueia o produtor nem
// os outros subscritores.
func TestBackpressureBlockNaoBloqueiaProdutorNemOutros(t *testing.T) {
	b, es := newTestBus(t, nil)
	ctx := context.Background()

	const n = 100
	gate := make(chan struct{}) // fecha para libertar o consumidor lento

	// Consumidor lento: bloqueia no gate antes de confirmar.
	var slowDelivered atomic.Int64
	slow := func(d *Delivery) {
		<-gate
		slowDelivered.Add(1)
		d.Ack()
	}
	subSlow, err := b.Subscribe(ctx, SubConfig{
		Name: "slow", Filter: Filter{Streams: []string{"s1"}},
		Handler: slow, Buffer: 2, Overflow: Block,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Consumidor rápido.
	fast := newRecorder()
	subFast, err := b.Subscribe(ctx, SubConfig{
		Name: "fast", Filter: Filter{Streams: []string{"s1"}},
		Handler: ackAll(fast), Buffer: 1024, Overflow: Block,
	})
	if err != nil {
		t.Fatal(err)
	}

	// O produtor deve conseguir escrever os n eventos rapidamente, apesar do
	// consumidor lento estar preso.
	start := time.Now()
	for i := 0; i < n; i++ {
		appendEv(t, es, "s1", "t1", "p1")
	}
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("produtor bloqueado pelo consumidor lento: %v para %d appends", elapsed, n)
	}

	// O consumidor rápido recebe tudo, sem depender do lento.
	fast.waitN(t, n, 3*time.Second)

	// O lento praticamente não avançou (preso no gate); no máximo terá começado a
	// 1.ª entrega.
	if got := slowDelivered.Load(); got > 1 {
		t.Fatalf("consumidor lento não estava bloqueado: entregou %d", got)
	}

	// Liberta o lento e confirma que acaba por receber tudo (nada se perde com Block).
	close(gate)
	deadline := time.Now().Add(3 * time.Second)
	for slowDelivered.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("consumidor lento não drenou: %d/%d", slowDelivered.Load(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	subSlow.Unsubscribe()
	subFast.Unsubscribe()
}

// TestBackpressureDropOldest verifica a política DropOldest: sob overload, o
// buffer descarta os mais antigos (sheds load) e o produtor não bloqueia.
func TestBackpressureDropOldest(t *testing.T) {
	b, es := newTestBus(t, nil)
	ctx := context.Background()

	const n = 200
	gate := make(chan struct{})
	var delivered atomic.Int64
	h := func(d *Delivery) {
		<-gate
		delivered.Add(1)
		d.Ack()
	}
	sub, err := b.Subscribe(ctx, SubConfig{
		Name: "drop", Filter: Filter{Streams: []string{"s1"}},
		Handler: h, Buffer: 4, Overflow: DropOldest,
	})
	if err != nil {
		t.Fatal(err)
	}

	// BARREIRA DE CATCH-UP — sem ela este teste corria uma CORRIDA e falhava ~1 em 5.
	//
	// [subscription.run] tem DUAS fases: a de catch-up lê o histórico do Event Store e
	// entrega DIRECTAMENTE (`s.deliver`), sem passar pelo buffer nem pela política de
	// overflow; só a fase live usa o `offer`. O `Subscribe` devolve ANTES de a fase 1 ter
	// feito a leitura, pelo que o produtor corria à frente e o catch-up apanhava parte dos
	// eventos — entregando-os sem descarte nenhum.
	//
	// Medido com uma sonda antes de corrigir, seis corridas:
	//
	//	entregues=5   descartados=194
	//	entregues=58  descartados=189
	//	entregues=200 descartados=196   <- o caso que fazia cair a asserção final
	//
	// A soma EXCEDE os 200 porque os mesmos eventos seguiam os dois caminhos: entregues pelo
	// catch-up e, em paralelo, oferecidos ao buffer e lá descartados. O `drainLive` já
	// deduplica a costura por watermark, portanto o handler nunca os via duas vezes — mas o
	// CONTADOR de descartes conta-os na mesma (ver a nota de residual no fim deste teste).
	//
	// A barreira: um evento de aquecimento e a espera pela sua ENTREGA. Quando `Delivered`
	// sobe, a leitura de catch-up de "s1" já aconteceu e o consumidor está preso no `gate` —
	// tudo o que se publique a seguir passa OBRIGATORIAMENTE pelo buffer live. Um facto
	// observável em vez de um `sleep`.
	appendEv(t, es, "s1", "t1", "aquecimento")
	waitFor(t, 2*time.Second, "a subscricao concluir o catch-up e prender-se no gate", func() bool {
		return b.Metrics().Delivered >= 1
	})

	start := time.Now()
	for i := 0; i < n; i++ {
		appendEv(t, es, "s1", "t1", "p1")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("produtor bloqueado sob DropOldest: %v", el)
	}

	// Dá tempo ao pipeline para encher e descartar.
	deadline := time.Now().Add(2 * time.Second)
	for b.Metrics().Dropped == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("DropOldest não descartou nada (buffer=4, n=%d)", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(gate)
	// Deve ter entregue estritamente menos do que os n do FLUXO (houve descarte). O +1 é o
	// evento de aquecimento da barreira, que foi entregue pelo catch-up de propósito e não
	// conta como parte do fluxo sob teste.
	time.Sleep(100 * time.Millisecond)
	if got := delivered.Load(); got >= int64(n)+1 {
		t.Fatalf("DropOldest não reduziu entregas: %d de %d (mais o aquecimento)", got, n)
	}
	// CONTROLO ANTI-VACUIDADE: com buffer=4 e o consumidor preso durante todo o fluxo, o que
	// chega tem de ser da ORDEM DO BUFFER, não "um bocadinho menos do que n". Sem esta
	// asserção, uma implementação que descartasse UM único evento passaria a asserção de
	// cima e o teste continuaria a dizer que DropOldest funciona.
	// CONTROLO ANTI-VACUIDADE: a asercao de cima e fraca — UM unico descarte satisfa-la, e o
	// teste continuaria a dizer que DropOldest funciona. O que se exige aqui e que o buffer
	// tenha ALIJADO CARGA a serio: uma reducao de 10x sobre o fluxo.
	//
	// PORQUE n/10 E NAO buffer+1, que era o meu primeiro palpite e estava ERRADO: o que chega
	// nao e so o que esta no buffer no momento do `close(gate)`. Os eventos que a subscricao
	// live ainda tem em voo continuam a chegar durante a janela seguinte, ja sem encontrar o
	// buffer cheio. Medido em 15 corridas: entre 5 e 12 entregas. O tecto de 20 tem folga
	// sobre o observado sem deixar de ser uma reducao inequivoca.
	if got := delivered.Load(); got > int64(n/10) {
		t.Fatalf("DropOldest deixou passar %d entregas de %d com buffer=4 — o buffer nao esta a alijar carga", got, n)
	}
	sub.Unsubscribe()
}

// RESIDUAL DECLARADO, descoberto ao corrigir a corrida acima e NÃO fechado aqui.
//
// O contador `Metrics().Dropped` conta as evicções do buffer SEM consultar o watermark, pelo
// que conta como perda os eventos que a costura catch-up→live duplica — eventos que o
// `drainLive` deduplicaria de qualquer forma e que o subscritor JÁ RECEBEU. Medido: 196
// "descartados" numa corrida em que os 200 chegaram todos ao handler.
//
// Em produção isto faz um subscritor que arranca com histórico mostrar um pico de `Dropped`
// que não corresponde a perda nenhuma.
//
// NÃO se corrige aqui porque a correcção atravessa uma fronteira de posse DOCUMENTADA: o
// cabeçalho de `subscription.go` declara que a goroutine `run` é DONA do `watermark` e que só
// a `queue` é partilhada. O `offer` corre na goroutine da subscrição live — ler o watermark de
// lá introduziria a corrida que o `-race` existe para apanhar. As duas saídas plausíveis
// (adiar a contagem para o `drainLive`, que é quem tem a posse; ou não contar evicções
// enquanto o catch-up decorre) têm compromissos diferentes de sub/sobre-contagem e merecem
// decisão do dono, não um palpite.

// TestBackpressureDeadLetterOverflow verifica a política DeadLetter em overflow:
// os eventos em excesso vão para a dead-letter, inspecionáveis, sem bloquear.
func TestBackpressureDeadLetterOverflow(t *testing.T) {
	b, es := newTestBus(t, nil)
	ctx := context.Background()

	const n = 200
	gate := make(chan struct{})
	h := func(d *Delivery) {
		<-gate
		d.Ack()
	}
	sub, err := b.Subscribe(ctx, SubConfig{
		Name: "dlq-of", Filter: Filter{Streams: []string{"s1"}},
		Handler: h, Buffer: 4, Overflow: DeadLetter,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		appendEv(t, es, "s1", "t1", "p1")
	}
	deadline := time.Now().Add(2 * time.Second)
	for b.DeadLetter().Len() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("overflow não produziu dead-letters")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for _, dl := range b.DeadLetter().Entries() {
		if dl.Reason != "overflow" {
			t.Fatalf("razão inesperada: %q", dl.Reason)
		}
	}
	close(gate)
	sub.Unsubscribe()
}
