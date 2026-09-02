package durable

// AOS-290 — O CLARO EM MEMÓRIA ENTRA NO ALCANCE DO APAGAMENTO, E O MAPA PASSA A TER PODA.
//
// O defeito medido: o `StepLedger` é composto UMA vez e partilhado por todos os runs do nó, e
// o seu mapa guarda o resultado de cada passo EM CLARO — o WAL leva o cifrado, a memória não.
// Destruída a KEK do titular, `OpenContent` sobre o blob do WAL falhava («KEK do titular
// DESTRUIDA») e `Applied(key)` continuava a devolver o payload em claro. O apagamento do
// Art. 17 era real no disco e ficção em memória. Um dump de heap isolou o retentor: baseline
// 2 → com o ledger vivo 3 → após o shred 3 → ledger largado 2. Era o mapa `records`.
//
// Segundo eixo, independente: o mapa nunca era podado. Crescimento linear sem patamar até 50
// mil passos, e a superfície exportada eram três métodos — nenhum removia nada.
//
// # PORQUE OS TESTES SÃO ESTES E NÃO OUTROS
//
// O comportamental (`Applied` deixa de devolver) é o que prova a AC1. Mas por si só não
// distingue «o ledger largou o payload» de «o ledger largou a chave e continua a segurar os
// bytes noutro sítio» — que era exactamente a pergunta que o dump de heap da auditoria
// respondia. Em Go um dump de heap não é asseverável de forma estável (o formato de
// `debug.WriteHeapDump` é interno, e um perfil de `pprof` guarda pilhas de alocação, não
// conteúdo). O que É determinista é a ALCANÇABILIDADE: um finalizador sobre o array que
// sustenta o payload corre quando — e só quando — nada no processo lhe chega. É a mesma
// pergunta do dump, feita ao GC em vez de aos bytes.

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// aos290Ledger monta um ledger selado por titular, no molde de aos093_ledger_seal_test.go.
func aos290Ledger(t *testing.T, cipher *fakeCipher, subject string) (*StepLedger, *eventstore.Store) {
	t.Helper()
	store := newStore(t)
	ledger, err := NewStepLedger(store,
		WithProducer(eventstore.Producer{NHIID: subject}), WithContentSealer(cipher))
	if err != nil {
		t.Fatalf("NewStepLedger: %v", err)
	}
	return ledger, store
}

// TestAOS290_ShredAlcancaOClaroEmMemoria é a AC1: depois do apagamento por titular, a leitura
// deixa de devolver plaintext.
//
// A primeira metade — provar que ANTES devolve — não é cerimónia: sem ela, um `ForgetSubject`
// que apagasse tudo, ou um `Applied` sempre-vazio, passariam a segunda metade.
func TestAOS290_ShredAlcancaOClaroEmMemoria(t *testing.T) {
	ctx := context.Background()
	cipher := newFakeCipher()
	const subject = "nhi:titular-aos290"
	ledger, _ := aos290Ledger(t, cipher, subject)

	const key = "run-aos290:step-1"
	const claro = "resultado-sintetico: ACME-AOS290-CLARO"
	if _, _, err := ledger.Apply(ctx, key, func(context.Context) (Result, error) {
		return Result{Status: "ok", Payload: []byte(claro)}, nil
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	res, ok := ledger.Applied(key)
	if !ok || string(res.Payload) != claro {
		t.Fatalf("antes do shred o ledger tem de devolver o claro; veio ok=%v payload=%q", ok, res.Payload)
	}

	// O shred REAL destrói a KEK (o disco fica ilegível) — e é aqui que estava o buraco: isto
	// sozinho não tocava na memória.
	cipher.shred(subject)
	if n := ledger.ForgetSubject(subject); n != 1 {
		t.Fatalf("ForgetSubject removeu %d entradas, quero 1", n)
	}

	if res, ok := ledger.Applied(key); ok {
		t.Fatalf("apos o apagamento o ledger ainda devolve payload em claro: ok=%v payload=%q", ok, res.Payload)
	}
}

// TestAOS290_ShredNaoAtingeOutroTitular fixa o isolamento. Um apagamento que levasse tudo à
// frente passaria o teste de cima e seria um defeito pior do que o que corrige.
func TestAOS290_ShredNaoAtingeOutroTitular(t *testing.T) {
	ctx := context.Background()
	cipher := newFakeCipher()
	ledgerA, _ := aos290Ledger(t, cipher, "nhi:titular-A")
	ledgerB, _ := aos290Ledger(t, cipher, "nhi:titular-B")

	aplica := func(l *StepLedger, key, payload string) {
		t.Helper()
		if _, _, err := l.Apply(ctx, key, func(context.Context) (Result, error) {
			return Result{Status: "ok", Payload: []byte(payload)}, nil
		}); err != nil {
			t.Fatalf("Apply(%s): %v", key, err)
		}
	}
	aplica(ledgerA, "run-a:step-1", "claro-de-A")
	aplica(ledgerB, "run-b:step-1", "claro-de-B")

	ledgerA.ForgetSubject("nhi:titular-A")

	if _, ok := ledgerA.Applied("run-a:step-1"); ok {
		t.Fatal("A devia ter sido apagado")
	}
	if res, ok := ledgerB.Applied("run-b:step-1"); !ok || string(res.Payload) != "claro-de-B" {
		t.Fatalf("o apagamento de A nao pode tocar em B; veio ok=%v payload=%q", ok, res.Payload)
	}
}

// TestAOS290_ForgetSubjectEIdempotenteESemTitular fixa o contrato que a porta
// [dsar.ShreddableKeyStore] exige: apagar um titular ausente é no-op sem erro.
func TestAOS290_ForgetSubjectEIdempotenteESemTitular(t *testing.T) {
	cipher := newFakeCipher()
	ledger, _ := aos290Ledger(t, cipher, "nhi:x")
	if n := ledger.ForgetSubject("nhi:nunca-existiu"); n != 0 {
		t.Fatalf("titular ausente devia devolver 0, veio %d", n)
	}
	if n := ledger.ForgetSubject(""); n != 0 {
		t.Fatalf("titular vazio devia devolver 0, veio %d", n)
	}
	var nilLedger *StepLedger
	if n := nilLedger.ForgetSubject("nhi:x"); n != 0 {
		t.Fatalf("receptor nil devia devolver 0, veio %d", n)
	}
}

// TestAOS290_PodaPorRunNaoDeixaOMapaCrescer é a AC2, e prova-a pelo CONTRATO em vez de por
// bytes — o molde de TestServiceCompletedRetentionPrunesOldest.
//
// Contar bytes de heap seria ruidoso e não distinguiria o mapa do resto; o que importa é que
// as entradas de um run deixem de estar lá. Σ(runs × passos) não cresce se cada run for podado
// à saída.
func TestAOS290_PodaPorRunNaoDeixaOMapaCrescer(t *testing.T) {
	ctx := context.Background()
	cipher := newFakeCipher()
	const subject = "nhi:titular-poda"
	ledger, _ := aos290Ledger(t, cipher, subject)

	const runs, passos = 5, 4
	for r := 0; r < runs; r++ {
		runID := "run-poda-" + string(rune('a'+r))
		for s := 0; s < passos; s++ {
			key := runID + ":step-" + string(rune('1'+s))
			if _, _, err := ledger.Apply(ctx, key, func(context.Context) (Result, error) {
				return Result{Status: "ok", Payload: []byte("payload-" + key)}, nil
			}); err != nil {
				t.Fatalf("Apply(%s): %v", key, err)
			}
		}
		// Poda à saída da hospedagem, como o nó faz no defer de `hostRun`.
		if n := ledger.ForgetRun(runID); n != passos {
			t.Fatalf("ForgetRun(%s) removeu %d, quero %d", runID, n, passos)
		}
	}

	// Nenhuma entrada de nenhum run sobreviveu — o mapa não acumulou Σ(runs × passos).
	for r := 0; r < runs; r++ {
		runID := "run-poda-" + string(rune('a'+r))
		for s := 0; s < passos; s++ {
			key := runID + ":step-" + string(rune('1'+s))
			if _, ok := ledger.Applied(key); ok {
				t.Fatalf("entrada %q sobreviveu a poda — o mapa continua a crescer", key)
			}
		}
	}
	// E o índice por titular encolheu com ele. Se ficasse a apontar chaves removidas, seria
	// ele a fuga que a poda existe para fechar.
	if n := ledger.ForgetSubject(subject); n != 0 {
		t.Fatalf("o indice por titular reteve %d chaves ja podadas", n)
	}
}

// TestAOS290_PodaPorRunNaoAtingeOutroRun fixa o alcance do prefixo. A chave é
// `run_id:step_id` e [IdempotencyKey] recusa um run_id que contenha o delimitador — mas um
// prefixo mal formado ("run-1" a apanhar "run-10") seria um apagamento silencioso a mais.
func TestAOS290_PodaPorRunNaoAtingeOutroRun(t *testing.T) {
	ctx := context.Background()
	cipher := newFakeCipher()
	ledger, _ := aos290Ledger(t, cipher, "nhi:t")

	for _, k := range []string{"run-1:step-1", "run-10:step-1"} {
		if _, _, err := ledger.Apply(ctx, k, func(context.Context) (Result, error) {
			return Result{Status: "ok", Payload: []byte("p-" + k)}, nil
		}); err != nil {
			t.Fatalf("Apply(%s): %v", k, err)
		}
	}
	if n := ledger.ForgetRun("run-1"); n != 1 {
		t.Fatalf("ForgetRun(run-1) removeu %d, quero 1 (run-10 nao e do run-1)", n)
	}
	if _, ok := ledger.Applied("run-10:step-1"); !ok {
		t.Fatal("run-10 foi apanhado pelo prefixo de run-1")
	}
}

// TestAOS290_OLedgerLargaOPayloadDepoisDoShred é a AC3 — a pergunta do dump de heap, feita ao
// GC.
//
// O marcador é construído EM RUNTIME (um array no heap, não uma constante do binário), o
// payload aponta para ele, e o teste larga TODAS as suas referências. Enquanto o ledger o
// segurar, o finalizador não corre; quando o ledger o largar, corre. É a diferença entre
// «`Applied` deixou de devolver» e «o processo deixou de ter os bytes» — e era a segunda que a
// auditoria mediu.
func TestAOS290_OLedgerLargaOPayloadDepoisDoShred(t *testing.T) {
	ctx := context.Background()
	cipher := newFakeCipher()
	const subject = "nhi:titular-heap"
	ledger, _ := aos290Ledger(t, cipher, subject)

	libertado := make(chan struct{})
	const key = "run-heap:step-1"

	// Bloco próprio para que `marcador` saia de scope antes das colheitas.
	func() {
		marcador := new([512]byte)
		for i := range marcador {
			marcador[i] = byte('A' + i%26)
		}
		runtime.SetFinalizer(marcador, func(*[512]byte) { close(libertado) })
		// O payload PARTILHA o array: enquanto o ledger o segurar, o array é alcançável.
		if _, _, err := ledger.Apply(ctx, key, func(context.Context) (Result, error) {
			return Result{Status: "ok", Payload: marcador[:]}, nil
		}); err != nil {
			t.Fatalf("Apply: %v", err)
		}
	}()

	// PREMISSA: com o ledger a segurar, o marcador NÃO é libertado. Sem esta metade o teste
	// passaria mesmo que o payload nunca tivesse ficado retido — e não provaria nada.
	libertouCedo := aos290Libertou(libertado, 300*time.Millisecond)
	// OS `KeepAlive` NÃO SÃO CERIMÓNIA — sem eles este teste passa pela razão errada.
	//
	// O único caminho até ao array é `ledger.records[key].Result`. Se o compilador concluir
	// que `ledger` já não é usado, o GC colhe o LEDGER INTEIRO e o finalizador corre — e o
	// teste leria isso como «o ledger largou o payload», quando o que aconteceu foi o ledger
	// desaparecer. Apanhou-se na falsificação: com `ForgetSubject` neutralizado a não tocar em
	// `l`, o `ledger` ficava morto logo após o `Apply` e a premissa falhava SEMPRE, aos 0,00s.
	// Estes dois pinos amarram a vida do ledger aos dois instantes que o teste compara.
	runtime.KeepAlive(ledger)
	if libertouCedo {
		t.Fatal("premissa invalida: o marcador foi libertado com o ledger ainda a segura-lo")
	}

	cipher.shred(subject)
	ledger.ForgetSubject(subject)

	libertouDepois := aos290Libertou(libertado, 5*time.Second)
	runtime.KeepAlive(ledger)
	if !libertouDepois {
		t.Fatal("apos o apagamento o processo AINDA alcanca o payload em claro — o ledger nao o largou")
	}
}

// aos290Libertou força colheitas até o finalizador correr ou o prazo esgotar. Duas chamadas a
// runtime.GC() por volta porque o finalizador só é agendado na colheita que torna o objecto
// inalcançável e só CORRE depois — uma única colheita não chega.
func aos290Libertou(sinal <-chan struct{}, prazo time.Duration) bool {
	limite := time.Now().Add(prazo)
	for time.Now().Before(limite) {
		runtime.GC()
		runtime.GC()
		select {
		case <-sinal:
			return true
		case <-time.After(10 * time.Millisecond):
		}
	}
	select {
	case <-sinal:
		return true
	default:
		return false
	}
}
