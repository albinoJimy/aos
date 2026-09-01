package jetstream_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/jetstream"
)

// throughput_test.go — o benchmark que os Testes Requeridos do AOS-100 pedem:
// «benchmark básico de throughput contra o baseline single-writer».
//
// # O que se compara, e o que NÃO se compara
//
// Compara-se o **substrato replicado** com o **baseline durável local** (WAL append-only
// com fsync), nas duas topologias que decidem se há ou não contenção de escritor único:
// N escritores sobre UM stream (serializados pela ordem-por-stream, que é do contrato) e
// N escritores sobre N streams (que é onde o paralelismo tem de aparecer).
//
// NÃO se comparam garantias iguais, e dizê-lo importa mais do que os números: o WAL local
// sobrevive à morte do PROCESSO; o cluster sobrevive à morte de um NÓ. O replicado paga
// uma ida-e-volta de rede e um quórum Raft por escrita, e o local paga um fsync. Um
// número do replicado «pior» que o local não é uma regressão — é o preço de uma garantia
// que o local não dá. O que o benchmark tem de mostrar é (a) a ordem de grandeza dessa
// diferença e (b) que o paralelismo entre streams existe nos dois.
//
// # A metodologia que torna os números legíveis
//
// Correr isto contra um cluster remoto através de um túnel mede o TÚNEL. Os números só
// significam alguma coisa co-localizados com o cluster — ver o cabeçalho do relatório de
// medição para o procedimento (cross-compilar `go test -c` e correr no servidor).
// A latência de base é reportada como métrica própria (`rtt_ms`) precisamente para que
// quem leia possa descontar o transporte em vez de adivinhar.

const escritoresPorOmissao = 8

// fabricaDeStore devolve um store novo e a função que o liberta.
type fabricaDeStore func(b *testing.B) (eventstore.EventStore, func())

func storeDeReferencia(b *testing.B) (eventstore.EventStore, func()) {
	b.Helper()
	wal := filepath.Join(b.TempDir(), "bench.wal")
	st, err := eventstore.Open(wal)
	if err != nil {
		b.Fatalf("abrir WAL de referência: %v", err)
	}
	return st, func() { _ = st.Close() }
}

// storeEmMemoria é o store de referência SEM WAL. Existe para ATRIBUIR a causa: se o
// in-memory escala com o número de streams e o durável não, o ponto de serialização é o
// WAL — e não a ordem-por-stream, que é do contrato e não se pode remover.
func storeEmMemoria(b *testing.B) (eventstore.EventStore, func()) {
	b.Helper()
	st, err := eventstore.New()
	if err != nil {
		b.Fatalf("abrir store in-memory: %v", err)
	}
	return st, func() { _ = st.Close() }
}

func storeJetStream(b *testing.B) (eventstore.EventStore, func()) {
	b.Helper()
	addr := os.Getenv(envServidor)
	if addr == "" {
		b.Skipf("sem cluster: define %s", envServidor)
	}
	nome := "BENCH_" + sufixoBench(b)
	st, err := jetstream.Abrir(addr,
		jetstream.ComNomeDeStream(nome),
		jetstream.ComReplicas(3),
		jetstream.ComPrazo(30*time.Second))
	if err != nil {
		b.Fatalf("abrir JetStream: %v", err)
	}
	return st, func() {
		_ = st.ApagarStream()
		_ = st.Close()
	}
}

func sufixoBench(b *testing.B) string {
	b.Helper()
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

func factoBench(i int) eventstore.EventInput {
	return eventstore.EventInput{
		Type:    "bench.append",
		Payload: json.RawMessage(`{"i":` + strconv.Itoa(i) + `}`),
	}
}

// medirEscrita corre b.N appends repartidos por `escritores` goroutines sobre `streams`
// streams, e reporta ops/s.
func medirEscrita(b *testing.B, fab fabricaDeStore, streams int) {
	st, largar := fab(b)
	defer largar()
	ctx := context.Background()

	// Aquecimento: a primeira escrita de cada stream paga a hidratação (no replicado, um
	// round-trip a descobrir que o stream está vazio). Medir isso dentro do laço faria o
	// benchmark reportar o custo de arranque diluído por b.N, que não é throughput.
	for s := 0; s < streams; s++ {
		if _, err := st.Append(ctx, nomeStreamBench(s), factoBench(-1)); err != nil {
			b.Fatalf("aquecimento do stream %d: %v", s, err)
		}
	}

	porEscritor := b.N / escritoresPorOmissao
	if porEscritor == 0 {
		porEscritor = 1
	}
	var wg sync.WaitGroup
	erros := make([]error, escritoresPorOmissao)

	b.ResetTimer()
	inicio := time.Now()
	for w := 0; w < escritoresPorOmissao; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			stream := nomeStreamBench(w % streams)
			for i := 0; i < porEscritor; i++ {
				if _, err := st.Append(ctx, stream, factoBench(i)); err != nil {
					erros[w] = err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	decorrido := time.Since(inicio)
	b.StopTimer()

	for w, err := range erros {
		if err != nil {
			b.Fatalf("escritor %d: %v", w, err)
		}
	}
	total := float64(porEscritor * escritoresPorOmissao)
	b.ReportMetric(total/decorrido.Seconds(), "ops/s")
	b.ReportMetric(float64(decorrido.Nanoseconds())/total/1e6, "ms/op")
}

func nomeStreamBench(i int) string { return "bench-" + strconv.Itoa(i) }

// BenchmarkEscrita compara o baseline durável local com o substrato replicado, nas duas
// topologias que decidem se há contenção de escritor único.
func BenchmarkEscrita(b *testing.B) {
	casos := []struct {
		nome    string
		fab     fabricaDeStore
		streams int
	}{
		{"referencia-memoria/1-stream", storeEmMemoria, 1},
		{"referencia-memoria/8-streams", storeEmMemoria, 8},
		{"referencia-wal/1-stream", storeDeReferencia, 1},
		{"referencia-wal/8-streams", storeDeReferencia, 8},
		{"jetstream-r3/1-stream", storeJetStream, 1},
		{"jetstream-r3/8-streams", storeJetStream, 8},
	}
	for _, c := range casos {
		b.Run(c.nome, func(b *testing.B) { medirEscrita(b, c.fab, c.streams) })
	}
}

// BenchmarkReplay mede a LEITURA de um stream inteiro — a operação de que a
// re-hidratação e o replay dependem.
//
// Expõe de propósito uma fraqueza CONHECIDA e declarada do adaptador: a leitura caminha o
// subject com `next_by_subj`, um round-trip POR EVENTO. Contra o WAL local, que lê de
// memória, a diferença não é de percentagem — é de ordem de grandeza. Está aqui para ser
// vista, não para ser escondida até alguém a descobrir em produção.
// BenchmarkReplay corre com DOIS tamanhos de stream de proposito: 200 e 2000 eventos.
//
// Nao e zelo — e o discriminante que separa duas hipoteses sobre o custo restante depois
// do batching. Se o tempo por leitura for aproximadamente CONSTANTE entre os dois, o custo
// e por LEITURA (criar o consumidor efemero, que num stream R3 e uma operacao replicada);
// se escalar com o tamanho, e por EVENTO. Sem os dois pontos, qualquer explicacao seria
// uma historia.
func BenchmarkReplay(b *testing.B) {
	casos := []struct {
		nome    string
		fab     fabricaDeStore
		eventos int
	}{
		{"referencia-wal/200", storeDeReferencia, 200},
		{"jetstream-r3/200", storeJetStream, 200},
		{"jetstream-r3/2000", storeJetStream, 2000},
	}
	for _, c := range casos {
		eventos := c.eventos
		b.Run(c.nome, func(b *testing.B) {
			st, largar := c.fab(b)
			defer largar()
			ctx := context.Background()
			const stream = "bench-replay"
			for i := 0; i < eventos; i++ {
				if _, err := st.Append(ctx, stream, factoBench(i)); err != nil {
					b.Fatalf("semear evento %d: %v", i, err)
				}
			}

			b.ResetTimer()
			inicio := time.Now()
			for i := 0; i < b.N; i++ {
				evs, err := st.Read(ctx, stream, 1)
				if err != nil {
					b.Fatalf("Read: %v", err)
				}
				if len(evs) != eventos {
					b.Fatalf("Read devolveu %d eventos, quer %d", len(evs), eventos)
				}
			}
			decorrido := time.Since(inicio)
			b.StopTimer()
			b.ReportMetric(float64(eventos*b.N)/decorrido.Seconds(), "eventos/s")
		})
	}
}

// BenchmarkLatenciaDeBase mede a ida-e-volta mínima ao substrato, sem trabalho nenhum
// pelo meio.
//
// Existe para que os números acima possam ser LIDOS: sem saber o RTT, não se distingue
// «o substrato é lento» de «o transporte é lento» — e essa é exactamente a confusão que
// um benchmark corrido através de um túnel produziria.
func BenchmarkLatenciaDeBase(b *testing.B) {
	addr := os.Getenv(envServidor)
	if addr == "" {
		b.Skipf("sem cluster: define %s", envServidor)
	}
	st, largar := storeJetStream(b)
	defer largar()
	js, ok := st.(*jetstream.Store)
	if !ok {
		b.Fatalf("store é %T", st)
	}

	b.ResetTimer()
	inicio := time.Now()
	for i := 0; i < b.N; i++ {
		if _, err := js.Read(context.Background(), "stream-que-nao-existe", 1); err == nil {
			b.Fatal("Read de stream inexistente devia falhar")
		}
	}
	decorrido := time.Since(inicio)
	b.StopTimer()
	b.ReportMetric(float64(decorrido.Nanoseconds())/float64(b.N)/1e6, "rtt_ms")
}

// BenchmarkReplayParalelo fecha a ressalva que ficou escrita ao lado do AC3: «a leitura
// paralela não é serializada por construção (Read não toma o mutex por-stream) mas NÃO
// foi medida em separado».
//
// «Não é serializada por construção» é um argumento sobre o código. Isto é a medição: S
// streams lidos por S goroutines contra os mesmos S lidos em sequência. Se o paralelismo
// existir, o agregado sobe; se houver um serializador escondido (a ligação única, por
// exemplo), fica plano — e é isso que interessa saber antes de alguém desenhar um
// arranque que re-hidrata vinte runs ao mesmo tempo.
func BenchmarkReplayParalelo(b *testing.B) {
	const (
		streams = 4
		eventos = 100
	)
	st, largar := storeJetStream(b)
	defer largar()
	ctx := context.Background()

	for s := 0; s < streams; s++ {
		for i := 0; i < eventos; i++ {
			if _, err := st.Append(ctx, nomeStreamBench(s), factoBench(i)); err != nil {
				b.Fatalf("semear stream %d evento %d: %v", s, i, err)
			}
		}
	}

	ler := func(s int) {
		evs, err := st.Read(ctx, nomeStreamBench(s), 1)
		if err != nil {
			b.Fatalf("Read do stream %d: %v", s, err)
		}
		if len(evs) != eventos {
			b.Fatalf("stream %d devolveu %d eventos, quer %d", s, len(evs), eventos)
		}
	}

	b.Run("sequencial", func(b *testing.B) {
		b.ResetTimer()
		inicio := time.Now()
		for i := 0; i < b.N; i++ {
			for s := 0; s < streams; s++ {
				ler(s)
			}
		}
		b.StopTimer()
		b.ReportMetric(float64(streams*eventos*b.N)/time.Since(inicio).Seconds(), "eventos/s")
	})

	b.Run("paralelo", func(b *testing.B) {
		b.ResetTimer()
		inicio := time.Now()
		for i := 0; i < b.N; i++ {
			var wg sync.WaitGroup
			for s := 0; s < streams; s++ {
				wg.Add(1)
				go func(s int) { defer wg.Done(); ler(s) }(s)
			}
			wg.Wait()
		}
		b.StopTimer()
		b.ReportMetric(float64(streams*eventos*b.N)/time.Since(inicio).Seconds(), "eventos/s")
	})
}
