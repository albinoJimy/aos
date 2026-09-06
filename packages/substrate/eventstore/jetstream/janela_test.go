package jetstream

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/natsjs"
)

// janela_test.go — AOS-345: a janela de leitura avança pelo fim do LOTE, não pelo fim do
// log.
//
// # O defeito, e porque nenhum teste o apanhava
//
// `lerLote` avançava a janela com `UltimoSeqDoSubject`, o seq da última mensagem do
// SUBJECT INTEIRO. Enquanto o log coubesse numa janela (2048) os dois valores coincidiam.
// Acima disso, o segundo lote arrancava DEPOIS do fim do log, não recebia nada, e a
// leitura morria no prazo — e como `hidratar` precede as escritas, o stream ficava
// ilegível E inescrevível. O maior stream alguma vez medido neste repositório tinha 2000
// eventos (`throughput_test.go`), quarenta e oito aquém da fronteira: o laço multi-lote
// nunca tinha corrido.
//
// # A estrutura das provas, e o que cada uma pode provar
//
// Há duas camadas de propósito, porque só uma delas corre em todo o lado:
//
//   - SEM CLUSTER (correm sempre, incluindo na CI): a aritmética da janela e o critério
//     de avanço, exercitados contra um log FALSO em que o seq do subject é
//     deliberadamente maior do que o do lote — que é exactamente a condição que o defeito
//     precisava para se manifestar. Inclui o CONTROLO POSITIVO: o mesmo log conduzido
//     pela regra ANTIGA falha, e falha com a mensagem que a produção teria dado. Sem esse
//     controlo, um teste verde não distinguiria «a correcção funciona» de «o teste não
//     mede nada».
//
//   - COM CLUSTER (`AOS_NATS_URL`): o log real, acima da janela, lido pelo `Read` público
//     e escrito a seguir. É o que prova que o `$JS.ACK…` traz mesmo o `stream_seq` que
//     este código lê dele — coisa que nenhum log falso pode provar.
//
// Sem cluster, os do segundo grupo são SALTADOS. Um mock do JetStream mediria o mock:
//
//	AOS_NATS_URL=127.0.0.1:14225 go test ./jetstream/ -run Janela -v

// --- Sem cluster: a aritmética e o critério de avanço ------------------------

// logFalso modela um log do JetStream em que o nosso subject está INTERCALADO com outros:
// as suas mensagens ocupam os seqs FÍSICOS passo, 2*passo, …, total*passo.
//
// A intercalação não é decoração — é a condição do defeito. Num log só nosso e sem
// buracos, o seq do subject e o seq do lote andam próximos e a diferença entre avançar por
// um ou por outro fica escondida. Com passo > 1, o fim do subject está muito à frente do
// fim do primeiro lote, e quem avançar pelo lugar errado salta o log inteiro.
type logFalso struct {
	total uint64 // quantas mensagens o subject tem
	passo uint64 // de quantos em quantos seqs físicos aparece uma nossa

	// avancoPeloFimDoLog reproduz a REGRA ANTIGA: devolve o seq da última mensagem do
	// subject em vez da do lote. É o controlo positivo.
	avancoPeloFimDoLog bool

	lotes []loteObservado
}

type loteObservado struct {
	inicio    uint64
	quantos   uint64
	entregues uint64
	ultimoSeq uint64
}

// seqFisico é o seq físico da i-ésima mensagem do subject (i começa em 1).
func (l *logFalso) seqFisico(i uint64) uint64 { return i * l.passo }

// ler entrega até `quantos` mensagens a partir do seq físico `inicio`, como um consumidor
// push com `by_start_sequence` faria.
func (l *logFalso) ler(inicio, quantos uint64) (loteLido, error) {
	// Índice da primeira mensagem nossa com seq físico >= inicio.
	primeiro := uint64(1)
	if inicio > 0 {
		primeiro = (inicio + l.passo - 1) / l.passo
		if primeiro == 0 {
			primeiro = 1
		}
	}
	ultimo := primeiro + quantos - 1
	if ultimo > l.total {
		ultimo = l.total
	}

	obs := loteObservado{inicio: inicio, quantos: quantos}
	if primeiro > l.total {
		// Para lá do fim do log: o servidor não tem nada para empurrar. É o que a
		// regra antiga provocava no segundo lote.
		l.lotes = append(l.lotes, obs)
		return loteLido{}, nil
	}

	evs := make([]eventstore.Event, 0, ultimo-primeiro+1)
	for i := primeiro; i <= ultimo; i++ {
		evs = append(evs, eventstore.Event{
			EventID:  "ev-" + strconv.FormatUint(i, 10),
			StreamID: "run-janela",
			Seq:      i,
			Type:     "janela.facto",
			Payload:  json.RawMessage(`{"i":` + strconv.FormatUint(i, 10) + `}`),
		})
	}

	avanco := l.seqFisico(ultimo)
	if l.avancoPeloFimDoLog {
		avanco = l.seqFisico(l.total)
	}
	obs.entregues, obs.ultimoSeq = uint64(len(evs)), avanco
	l.lotes = append(l.lotes, obs)
	return loteLido{eventos: evs, ultimoSeq: avanco}, nil
}

func TestJanela_LerEmLotes_DevolveTodosEOsLotesSaoOsEsperados(t *testing.T) {
	const (
		total  = 5000 // > 2 janelas, e não múltiplo de nenhuma delas
		janela = janelaDeLeitura
		passo  = 7
	)
	log := &logFalso{total: total, passo: passo}

	evs, err := lerEmLotes("aos.es.teste.run-janela", total, janela, log.ler)
	if err != nil {
		t.Fatalf("lerEmLotes: %v", err)
	}

	// 1) Devolve TODOS, por ordem e sem buracos. Um log truncado em silêncio é o pior
	//    desfecho possível: o replay reconstruiria estado errado sem ninguém dar por isso.
	if uint64(len(evs)) != total {
		t.Fatalf("lerEmLotes devolveu %d eventos, quer %d — o log foi servido truncado", len(evs), total)
	}
	for i, ev := range evs {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("evento na posição %d tem seq %d, quer %d — a ordem ou a completude quebrou", i, ev.Seq, i+1)
		}
	}

	// 2) O número de lotes é o esperado: ceil(total/janela).
	const querLotes = (total + janela - 1) / janela
	if len(log.lotes) != querLotes {
		t.Fatalf("foram lidos %d lotes, quer %d", len(log.lotes), querLotes)
	}

	// 3) E cada lote pediu o que devia e arrancou onde devia: o primeiro do princípio do
	//    log (inicio 0), cada um dos seguintes no seq físico A SEGUIR ao último do lote
	//    anterior. É este o critério que o defeito violava.
	querQuantos := []uint64{janela, janela, total - 2*janela}
	var lidos uint64
	for i, lote := range log.lotes {
		if lote.quantos != querQuantos[i] {
			t.Errorf("lote %d pediu %d eventos, quer %d", i, lote.quantos, querQuantos[i])
		}
		querInicio := uint64(0)
		if i > 0 {
			querInicio = log.lotes[i-1].ultimoSeq + 1
		}
		if lote.inicio != querInicio {
			t.Errorf("lote %d arrancou no seq físico %d, quer %d — a janela avançou pelo lugar errado",
				i, lote.inicio, querInicio)
		}
		lidos += lote.entregues
		// O seq físico do fim do lote tem de estar MUITO aquém do fim do log enquanto
		// houver lotes por ler. Se não estivesse, o teste não distinguiria as duas
		// regras e passaria por acaso.
		if i < len(log.lotes)-1 && lote.ultimoSeq >= log.seqFisico(total) {
			t.Errorf("lote %d acabou no seq físico %d, que já é o fim do log (%d) — o caso não discrimina as duas regras de avanço",
				i, lote.ultimoSeq, log.seqFisico(total))
		}
	}
	if lidos != total {
		t.Errorf("os lotes entregaram %d eventos no total, quer %d", lidos, total)
	}
}

// TestJanela_AvancoPeloFimDoLogMorreNoSegundoLote é o CONTROLO POSITIVO: a regra antiga,
// conduzida pelo mesmo log falso, tem de falhar. Sem ele o teste acima não prova nada —
// um teste que passa com e sem a correcção não mede a correcção.
func TestJanela_AvancoPeloFimDoLogMorreNoSegundoLote(t *testing.T) {
	const (
		total  = 5000
		janela = janelaDeLeitura
		passo  = 7
	)
	log := &logFalso{total: total, passo: passo, avancoPeloFimDoLog: true}

	_, err := lerEmLotes("aos.es.teste.run-janela", total, janela, log.ler)
	if err == nil {
		t.Fatalf("a regra antiga (avanço pelo fim do LOG) leu %d eventos sem erro — "+
			"o log falso não reproduz a condição do defeito", total)
	}
	if !strings.Contains(err.Error(), "lote vazio") {
		t.Fatalf("erro = %v, quer o de lote vazio a meio da leitura (o modo de falha medido em produção)", err)
	}
	// E falhou onde tinha de falhar: no SEGUNDO lote, com a primeira janela já lida.
	if len(log.lotes) != 2 {
		t.Fatalf("a regra antiga leu %d lotes, quer 2 (um bom e um vazio)", len(log.lotes))
	}
	if log.lotes[1].inicio != log.seqFisico(total)+1 {
		t.Errorf("o segundo lote arrancou em %d, quer %d (um a seguir ao fim do log) — não é este o defeito",
			log.lotes[1].inicio, log.seqFisico(total)+1)
	}
}

// TestJanela_LoteQueNaoAvancaERecusado fixa a guarda contra o laço infinito: um seq físico
// que não faz a janela progredir releria o mesmo prefixo para sempre, e um teste que
// espera é indistinguível de um que passa.
func TestJanela_LoteQueNaoAvancaERecusado(t *testing.T) {
	chamadas := 0
	_, err := lerEmLotes("aos.es.teste.run-janela", 10, 4, func(inicio, quantos uint64) (loteLido, error) {
		chamadas++
		if chamadas > 8 {
			t.Fatalf("lerEmLotes girou %d vezes — a guarda contra o laço infinito não está a morder", chamadas)
		}
		evs := make([]eventstore.Event, quantos)
		// Devolve sempre o mesmo seq: a janela nunca sai do sítio.
		return loteLido{eventos: evs, ultimoSeq: 3}, nil
	})
	if err == nil {
		t.Fatalf("um lote que não avança a janela foi aceite — é um laço infinito à espera de acontecer")
	}
	if !strings.Contains(err.Error(), "não avança a janela") {
		t.Errorf("erro = %v, quer a recusa do seq que não avança", err)
	}
}

// TestJanela_SemSeqFisicoSoDerrubaQuemPrecisaDeAvancar fixa a proporcionalidade da
// correcção.
//
// O seq físico sai do `$JS.ACK…` de cada entrega, e que um consumidor push com
// `ack_policy: none` o traga NÃO foi medido contra um cluster (AOS-345 corrigiu leitura,
// não mediu o protocolo). Se a suposição estiver errada, o preço tem de ser pago só por
// quem depende dela: um log que cabe numa janela não avança nada e não pode ser derrubado
// por um valor que nunca vai usar. Trocar um defeito que aparece acima de 2048 eventos por
// outro que aparece em TODAS as leituras seria uma regressão maior do que a que se corrige.
func TestJanela_SemSeqFisicoSoDerrubaQuemPrecisaDeAvancar(t *testing.T) {
	// `ultimoSeq 0` é o «não sei» de [Store.lerLote] — quando a entrega vem sem subject de
	// resposta, OU com um `$JS.ACK` que este cliente não sabe interpretar. Os dois casos
	// caem aqui de propósito: em ambos o que se tem é a mesma ignorância, e derrubar por
	// causa dela uma leitura que nunca usaria o valor seria trocar um defeito raro por um
	// comum (revisão adversarial de AOS-345).
	semSeq := func(total uint64) func(inicio, quantos uint64) (loteLido, error) {
		var entregues uint64
		return func(inicio, quantos uint64) (loteLido, error) {
			n := total - entregues
			if n > quantos {
				n = quantos
			}
			entregues += n
			return loteLido{
				eventos:            make([]eventstore.Event, n),
				porqueDesconhecido: errors.New("sonda: entrega sem subject de resposta"),
			}, nil
		}
	}

	t.Run("cabe na janela: lê na mesma", func(t *testing.T) {
		evs, err := lerEmLotes("aos.es.teste.run-janela", 100, janelaDeLeitura, semSeq(100))
		if err != nil {
			t.Fatalf("uma leitura de um só lote foi derrubada por um seq físico que não usa: %v", err)
		}
		if len(evs) != 100 {
			t.Errorf("devolveu %d eventos, quer 100", len(evs))
		}
	})

	t.Run("não cabe: falha, e diz porquê", func(t *testing.T) {
		_, err := lerEmLotes("aos.es.teste.run-janela", janelaDeLeitura+1, janelaDeLeitura, semSeq(janelaDeLeitura+1))
		if err == nil {
			t.Fatalf("a janela avançou às cegas sem seq físico — ou releria o mesmo prefixo, ou saltaria o resto do log")
		}
		if !strings.Contains(err.Error(), "seq físico") {
			t.Errorf("erro = %v, quer a recusa de avançar sem seq físico", err)
		}
	})
}

func TestJanela_LerEmLotes_UmSoLoteQuandoCabeNaJanela(t *testing.T) {
	log := &logFalso{total: 2000, passo: 3}
	evs, err := lerEmLotes("aos.es.teste.run-janela", 2000, janelaDeLeitura, log.ler)
	if err != nil {
		t.Fatalf("lerEmLotes: %v", err)
	}
	if len(evs) != 2000 {
		t.Fatalf("devolveu %d eventos, quer 2000", len(evs))
	}
	// É o caso que o repositório já cobria (o maior stream medido tinha 2000 eventos) e
	// que passava COM o defeito. Fica aqui para que a correcção não o quebre.
	if len(log.lotes) != 1 {
		t.Errorf("foram lidos %d lotes, quer 1 — 2000 cabe na janela de %d", len(log.lotes), janelaDeLeitura)
	}
}

// --- Sem cluster: o seq físico sai do subject de resposta --------------------

func TestJanela_SeqDoStreamNaResposta(t *testing.T) {
	bons := map[string]struct {
		reply string
		quer  uint64
	}{
		// Formato de 9 tokens (sem domínio de JetStream):
		// $JS.ACK.<stream>.<consumidor>.<entregas>.<stream_seq>.<consumidor_seq>.<ts>.<pendentes>
		"sem domínio": {"$JS.ACK.AOS_EVENTS.cons1.1.4096.4096.1725580800000000000.0", 4096},
		"seq 1":       {"$JS.ACK.AOS_EVENTS.cons1.1.1.1.1725580800000000000.0", 1},
		// Formato de 12 tokens (com domínio e hash de conta, e um token aleatório no fim)
		"com domínio": {"$JS.ACK.hub.ACC123.AOS_EVENTS.cons1.1.4096.4096.1725580800000000000.0.xYz", 4096},
		// O seq do stream é o SEXTO campo, não o do consumidor: quando divergem (o que
		// acontece sempre que o filtro do consumidor não é o stream inteiro), ler o
		// campo errado põe a janela no sítio errado.
		"stream_seq != consumer_seq": {"$JS.ACK.AOS_EVENTS.cons1.1.31337.12.1725580800000000000.0", 31337},
	}
	for nome, c := range bons {
		got, err := seqDoStreamNaResposta(c.reply)
		if err != nil {
			t.Errorf("%s: %v", nome, err)
			continue
		}
		if got != c.quer {
			t.Errorf("%s: seq = %d, quer %d", nome, got, c.quer)
		}
	}

	// Fail-closed: nada disto pode devolver 0 e seguir em frente. Zero é «princípio do
	// log», e serviria o mesmo prefixo para sempre.
	maus := map[string]string{
		"vazio":            "",
		"não é ACK":        "aos.es.AOS_EVENTS.run-1",
		"prefixo errado":   "$XX.ACK.AOS_EVENTS.cons1.1.10.10.1725580800000000000.0",
		"tokens a menos":   "$JS.ACK.AOS_EVENTS.cons1.1.10.10.0",
		"tokens a mais":    "$JS.ACK.AOS_EVENTS.cons1.1.10.10.0.0.0.0.0.0.0",
		"seq não numérico": "$JS.ACK.AOS_EVENTS.cons1.1.abc.10.1725580800000000000.0",
		"seq negativo":     "$JS.ACK.AOS_EVENTS.cons1.1.-3.10.1725580800000000000.0",
		"seq zero":         "$JS.ACK.AOS_EVENTS.cons1.1.0.10.1725580800000000000.0",
	}
	for nome, reply := range maus {
		got, err := seqDoStreamNaResposta(reply)
		if err == nil {
			t.Errorf("%s: %q foi aceite e deu seq %d — devolver um seq inventado serve um log truncado em silêncio", nome, reply, got)
			continue
		}
		if !errors.Is(err, natsjs.ErrProtocol) {
			t.Errorf("%s: erro = %v, quer natsjs.ErrProtocol", nome, err)
		}
	}
}

// --- Com cluster: o log real, acima da janela --------------------------------

// envServidorJanela é o mesmo `AOS_NATS_URL` do resto do pacote. Está redeclarado porque
// os testes acima são INTERNOS (precisam de `lerEmLotes` e de `Store.lerLote`) e o
// `envServidor` de `conformidade_test.go` vive no pacote externo `jetstream_test`.
const envServidorJanela = "AOS_NATS_URL"

func servidorDaJanela(t *testing.T) string {
	t.Helper()
	addr := os.Getenv(envServidorJanela)
	if addr == "" {
		t.Skipf("sem cluster: define %s (túnel SSH para o nó 0 do cluster)", envServidorJanela)
	}
	return addr
}

// nomeDeStreamDaJanela deriva o nome do stream do NOME DO TESTE, não do relógio nem de
// aleatoriedade: duas execuções do mesmo teste usam o mesmo stream, e o teste apaga-o no
// fim. Um nome com timestamp deixaria lixo acumulado no cluster a cada corrida.
func nomeDeStreamDaJanela() (string, string) {
	return "AOSJANELA", "aosjanela"
}

// TestJanela_AcimaDaJanela_LeTudoEContinuaEscrivel é a prova contra o substrato real.
//
// Semeia mais eventos do que a janela num SÓ stream_id — o caso que nunca tinha corrido —
// e verifica as três coisas que o defeito quebrava, por esta ordem:
//
//  1. `Read` devolve TODOS os eventos, gapless (era um timeout);
//  2. a leitura faz o número de lotes esperado, e cada um arranca onde o anterior acabou;
//  3. um handle NOVO (cache fria, logo `hidratar` → `Append`) consegue ESCREVER. É a
//     consequência que doía: com o defeito, o stream não ficava só ilegível — ficava
//     inescrevível, e um laço de retenção nesse estado não pode ser reclamado.
//
// Requer `AOS_NATS_URL`. Sem ele é SALTADO: um mock do JetStream não pode provar que o
// `$JS.ACK…` traz o `stream_seq`, que é a afirmação sobre o servidor de que tudo depende.
func TestJanela_AcimaDaJanela_LeTudoEContinuaEscrivel(t *testing.T) {
	addr := servidorDaJanela(t)
	nome, prefixo := nomeDeStreamDaJanela()

	// 64 acima da janela: o segundo lote existe e é pequeno, o que torna o teste
	// mais barato sem deixar de atravessar a fronteira que ninguém tinha atravessado.
	const total = janelaDeLeitura + 64
	const streamID = "run-janela"

	semear, err := Abrir(addr,
		ComNomeDeStream(nome),
		ComPrefixoDeSubject(prefixo),
		ComReplicas(3),
		ComPrazo(30*time.Second))
	if err != nil {
		t.Fatalf("abrir para semear: %v", err)
	}
	defer func() {
		_ = semear.ApagarStream()
		_ = semear.Close()
	}()

	ctx := context.Background()
	for i := 1; i <= total; i++ {
		if _, err := semear.Append(ctx, streamID, eventstore.EventInput{
			Type:    "janela.facto",
			Payload: json.RawMessage(`{"i":` + strconv.Itoa(i) + `}`),
		}); err != nil {
			t.Fatalf("semear evento %d de %d: %v", i, total, err)
		}
	}

	// (1) Leitura por um handle NOVO: cache fria, hidratação completa pelo caminho
	// público. Com o defeito, isto morria no prazo do segundo lote.
	leitor, err := Abrir(addr,
		ComNomeDeStream(nome),
		ComPrefixoDeSubject(prefixo),
		SemCriarStream(),
		ComPrazo(30*time.Second))
	if err != nil {
		t.Fatalf("abrir para ler: %v", err)
	}
	defer func() { _ = leitor.Close() }()

	evs, err := leitor.Read(ctx, streamID, 1)
	if err != nil {
		t.Fatalf("Read de %d eventos (acima da janela de %d): %v", total, janelaDeLeitura, err)
	}
	if len(evs) != total {
		t.Fatalf("Read devolveu %d eventos, quer %d — o log foi servido truncado", len(evs), total)
	}
	for i, ev := range evs {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("evento na posição %d tem seq %d, quer %d", i, ev.Seq, i+1)
		}
	}

	// (2) O número de lotes, medido contra o cluster real: conduz-se o MESMO driver de
	// produção com o MESMO leitor de lote, contando as passagens. Nada aqui é um duplo —
	// só o contador é do teste.
	subject, err := leitor.subjectDe(streamID)
	if err != nil {
		t.Fatalf("subjectDe: %v", err)
	}
	var lotes int
	contados, err := lerEmLotes(subject, total, janelaDeLeitura,
		func(inicio, quantos uint64) (loteLido, error) {
			lotes++
			return leitor.lerLote(ctx, subject, inicio, quantos, 30*time.Second)
		})
	if err != nil {
		t.Fatalf("lerEmLotes contra o cluster: %v", err)
	}
	if len(contados) != total {
		t.Fatalf("lerEmLotes devolveu %d eventos, quer %d", len(contados), total)
	}
	querLotes := (total + janelaDeLeitura - 1) / janelaDeLeitura
	if lotes != querLotes {
		t.Errorf("foram lidos %d lotes, quer %d", lotes, querLotes)
	}

	// (3) E o stream continua ESCREVÍVEL. Handle novo outra vez: o Append passa por
	// `hidratar`, que é onde o defeito o matava.
	escritor, err := Abrir(addr,
		ComNomeDeStream(nome),
		ComPrefixoDeSubject(prefixo),
		SemCriarStream(),
		ComPrazo(30*time.Second))
	if err != nil {
		t.Fatalf("abrir para escrever: %v", err)
	}
	defer func() { _ = escritor.Close() }()

	res, err := escritor.Append(ctx, streamID, eventstore.EventInput{
		Type:    "janela.facto",
		Payload: json.RawMessage(`{"depois":true}`),
	})
	if err != nil {
		t.Fatalf("Append depois de %d eventos: %v — o stream ficou inescrevível, que é a consequência que dói", total, err)
	}
	if res.Seq != total+1 {
		t.Errorf("Append devolveu seq %d, quer %d", res.Seq, total+1)
	}
}
