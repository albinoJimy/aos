package integration

// aos432_lease_sobre_jetstream_test.go — A DISPUTA DE LEASE SOBRE O SUBSTRATO REAL, E A
// SEPARAÇÃO ENTRE «POSSE NEGADA» E «SUBSTRATO EM BAIXO».
//
// # O defeito (AOS-432)
//
// Sobre um stream acabado de criar, quem perdia a corrida ao lease recebia 503 (no
// responders) em vez da recusa do CAS: o `STREAM.CREATE` responde antes de o grupo R3
// eleger líder, e até lá só o líder — que ainda não existe — subscreveria os subjects. A
// correcção é o `jetstream.Abrir` esperar pelo líder; nenhum erro foi remapeado.
//
// # Porque existe este ficheiro além dos testes multi-processo do `aos-orq`
//
// Aqueles provam o código de saída de processos reais. Este prova a mesma coisa UMA camada
// abaixo — `LeaseManager` sobre `jetstream.Store`, sem binário no meio — e prova a metade
// que um teste de processo não consegue fixar sem derrubar o cluster: que um 503 VERDADEIRO
// do servidor continua a sair como 503, e nunca como posse negada.

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/substrate/eventstore/jetstream"
	"github.com/aos-ref/substrate/eventstore/natsjs"
)

// TestAOS432_LeaseSobreStreamFrescoNegaPeloLease — N ligações abrem o MESMO stream novo em
// paralelo e disputam o mesmo run. Exactamente um vence; TODOS os outros são recusados pelo
// LEASE (ErrLeaseHeld), e nenhum recebe um erro de transporte. Antes da correcção, medido
// no cluster do gate: 1 vencedor e N-1 × 503.
func TestAOS432_LeaseSobreStreamFrescoNegaPeloLease(t *testing.T) {
	addr := clusterFourEyes(t)
	stream := "AOS432_" + sufixoDeStream(t)
	const n = 4
	const run = "run-aos432"

	stores := make([]*jetstream.Store, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	arranque := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-arranque
			st, err := jetstream.Abrir(addr, jetstream.ComNomeDeStream(stream))
			if err != nil {
				errs[i] = err
				return
			}
			stores[i] = st
			lm, err := durable.NewLeaseManager(st, 30*time.Second, durable.WithWorkerID("w"+strconv.Itoa(i)))
			if err != nil {
				errs[i] = err
				return
			}
			_, errs[i] = lm.Claim(context.Background(), run)
		}(i)
	}
	close(arranque)
	wg.Wait()
	t.Cleanup(func() {
		for i := len(stores) - 1; i >= 0; i-- {
			if stores[i] != nil {
				_ = stores[i].ApagarStream()
				_ = stores[i].Close()
			}
		}
	})

	vencedores, negados := 0, 0
	for i, err := range errs {
		switch {
		case err == nil:
			vencedores++
		case errors.Is(err, durable.ErrLeaseHeld):
			negados++
		default:
			t.Errorf("ligação %d: desfecho que não é nem posse nem recusa pelo lease: %v", i, err)
		}
	}
	if vencedores != 1 || negados != n-1 {
		t.Fatalf("vencedores=%d negados-pelo-lease=%d, quer 1 e %d", vencedores, negados, n-1)
	}
}

// TestAOS432_503VerdadeiroNaoEPosseNegada — o critério 4 contra o SERVIDOR. Um Store cujo
// prefixo de subjects nenhum stream captura publica para o vazio: o servidor responde 503
// pela razão que o 503 existe para dizer (subject fora de qualquer stream). O `Claim` tem de
// devolver ESSE erro — [natsjs.ErrNoResponders] — e não [durable.ErrLeaseHeld]. Se um dia
// alguém «resolver» o AOS-432 mapeando o 503 para posse negada, é aqui que fica vermelho.
func TestAOS432_503VerdadeiroNaoEPosseNegada(t *testing.T) {
	addr := clusterFourEyes(t)
	suf := sufixoDeStream(t)
	stream := "AOS432N_" + suf

	dono, err := jetstream.Abrir(addr, jetstream.ComNomeDeStream(stream))
	if err != nil {
		t.Fatalf("abrir o stream: %v", err)
	}
	t.Cleanup(func() { _ = dono.ApagarStream(); _ = dono.Close() })

	// O mesmo stream, mas com um prefixo que o stream NÃO captura — e sem o criar, para
	// que nada do lado do cliente o faça existir.
	orfao, err := jetstream.Abrir(addr, jetstream.ComNomeDeStream(stream), jetstream.SemCriarStream(),
		jetstream.ComPrefixoDeSubject("aos.es.aos432-orfao-"+suf))
	if err != nil {
		t.Fatalf("abrir o store órfão: %v", err)
	}
	t.Cleanup(func() { _ = orfao.Close() })

	lm, err := durable.NewLeaseManager(orfao, 30*time.Second, durable.WithWorkerID("orfao"))
	if err != nil {
		t.Fatalf("NewLeaseManager: %v", err)
	}
	_, err = lm.Claim(context.Background(), "run-aos432-orfao")
	if err == nil {
		t.Fatal("Claim sobre um subject que nenhum stream captura foi ACEITE")
	}
	if errors.Is(err, durable.ErrLeaseHeld) {
		t.Fatalf("um 503 do servidor saiu como POSSE NEGADA — o operador procuraria um dono que não existe: %v", err)
	}
	if !errors.Is(err, natsjs.ErrNoResponders) {
		t.Fatalf("Claim = %v, quer natsjs.ErrNoResponders na cadeia (a causa real)", err)
	}
}
