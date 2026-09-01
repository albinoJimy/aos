package jetstream_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/jetstream"
)

// backup_integracao_test.go — backup/PITR do substrato replicado, contra um cluster REAL
// (AOS-101 sobre AOS-100).
//
// Nada aqui é mockável. O que se afirma — que o envelope sobrevive intacto a uma
// travessia, que uma retoma não duplica, que uma história divergente é recusada — são
// propriedades do servidor tanto quanto do cliente. Um duplo mediria o duplo.

// abrirBackup abre um Store sobre um stream JetStream NOVO, e devolve-o com o nome e o
// prefixo — que um segundo handle precisa de saber para se ligar ao MESMO stream físico.
func abrirBackup(t *testing.T, addr, marca string) (*jetstream.Store, string, string) {
	t.Helper()
	s := sufixo(t)
	nome, prefixo := "AOSBKP_"+marca+"_"+s, "aosbkp."+marca+"."+s
	st, err := jetstream.Abrir(addr,
		jetstream.ComNomeDeStream(nome),
		jetstream.ComPrefixoDeSubject(prefixo),
		jetstream.ComPrazo(prazo),
		jetstream.ComReplicas(3),
	)
	if err != nil {
		t.Fatalf("abrir (%s): %v", marca, err)
	}
	t.Cleanup(func() {
		_ = st.ApagarStream()
		_ = st.Close()
	})
	return st, nome, prefixo
}

// semear escreve n eventos no stream e devolve o que o log ficou a ter.
func semear(t *testing.T, st *jetstream.Store, stream string, n int) []eventstore.Event {
	t.Helper()
	ctx := context.Background()
	for i := 1; i <= n; i++ {
		// RunID/StepID vão preenchidos de propósito: é o que dá ao evento uma chave de
		// idempotência, e é ela que a travessia tem de preservar tal e qual — um restauro
		// que a reatribuísse quebraria a dedup do log restaurado.
		_, err := st.Append(ctx, stream, eventstore.EventInput{
			Type:    "aos.teste.semente.v1",
			Payload: []byte(`{"i":` + itoa(i) + `}`),
			RunID:   stream,
			StepID:  "passo-" + itoa(i),
		})
		if err != nil {
			t.Fatalf("semear %d: %v", i, err)
		}
	}
	evs, err := st.Read(ctx, stream, 1)
	if err != nil {
		t.Fatalf("reler a semente: %v", err)
	}
	if len(evs) != n {
		t.Fatalf("semeados %d, lidos %d", n, len(evs))
	}
	return evs
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestAOS101_UmLogTravessaParaOutroStreamComOEnvelopeINTACTO é a prova central do ticket.
//
// É também, literalmente, o caminho de migração que faltava: um log lido de um substrato e
// reinserido noutro SEM que o envelope mude. Se o EventID, o Ts ou o Seq mudassem, a
// travessia não teria movido a história — teria fabricado outra parecida, que é o que o
// Append público faz por desenho e o que tornava a migração impossível.
func TestAOS101_UmLogTravessaParaOutroStreamComOEnvelopeINTACTO(t *testing.T) {
	addr := servidor(t)
	origem, _, _ := abrirBackup(t, addr, "org")
	destino, _, _ := abrirBackup(t, addr, "dst")
	ctx := context.Background()
	const stream = "run-travessia"

	original := semear(t, origem, stream, 12)

	// (1) ORIGEM — a porta de leitura de backup.
	snap, err := origem.SnapshotStream(ctx, stream, 0)
	if err != nil {
		t.Fatalf("SnapshotStream: %v", err)
	}
	if len(snap) != len(original) {
		t.Fatalf("snapshot trouxe %d eventos, o log tem %d", len(snap), len(original))
	}

	// (2) DESTINO — a porta de escrita de restauro, noutro stream JetStream.
	if err := destino.IngestStream(ctx, stream, snap); err != nil {
		t.Fatalf("IngestStream: %v", err)
	}

	// (3) O ENVELOPE, campo a campo. É esta comparação que distingue «restaurou» de
	// «escreveu outra vez»: um Append teria dado EventIDs novos e Ts de agora, e a
	// contagem — que é o que uma verificação preguiçosa olharia — bateria certo na mesma.
	relido, err := destino.Read(ctx, stream, 1)
	if err != nil {
		t.Fatalf("reler o destino: %v", err)
	}
	if len(relido) != len(original) {
		t.Fatalf("destino tem %d eventos, a origem tinha %d", len(relido), len(original))
	}
	for i := range original {
		o, r := original[i], relido[i]
		switch {
		case r.EventID != o.EventID:
			t.Fatalf("seq %d: EventID %q != %q — o envelope foi REATRIBUÍDO, isto não é um restauro", o.Seq, r.EventID, o.EventID)
		case r.Seq != o.Seq:
			t.Fatalf("seq %d: veio com seq %d", o.Seq, r.Seq)
		case r.Ts != o.Ts:
			t.Fatalf("seq %d: Ts %q != %q — o carimbo é do restauro, não do evento", o.Seq, r.Ts, o.Ts)
		case r.IdempotencyKey != o.IdempotencyKey:
			t.Fatalf("seq %d: chave de idempotência %q != %q", o.Seq, r.IdempotencyKey, o.IdempotencyKey)
		case string(r.Payload) != string(o.Payload):
			t.Fatalf("seq %d: payload diferente", o.Seq)
		}
	}

	// (4) E o head do destino é o da origem — medido no SERVIDOR, não na nossa vista.
	head, err := destino.StreamHead(ctx, stream)
	if err != nil {
		t.Fatalf("StreamHead: %v", err)
	}
	if head != uint64(len(original)) {
		t.Fatalf("StreamHead = %d, quer %d", head, len(original))
	}
}

// TestAOS101_PITRAteUmAlvoParaExactamenteNoAlvo — o P de PITR.
//
// O corte é do lado da LEITURA (SnapshotStream até ao seq-alvo), e não do lado da escrita,
// porque no substrato replicado não existe lado de escrita que corte: deny_purge torna
// impossível truncar um stream. Restaurar até N é materializar 1..N noutro stream, e é
// isso que este teste faz.
func TestAOS101_PITRAteUmAlvoParaExactamenteNoAlvo(t *testing.T) {
	addr := servidor(t)
	origem, _, _ := abrirBackup(t, addr, "porg")
	destino, _, _ := abrirBackup(t, addr, "pdst")
	ctx := context.Background()
	const stream = "run-pitr"
	const alvo = 7

	original := semear(t, origem, stream, 20)

	snap, err := origem.SnapshotStream(ctx, stream, alvo)
	if err != nil {
		t.Fatalf("SnapshotStream(alvo): %v", err)
	}
	if len(snap) != alvo {
		t.Fatalf("snapshot até %d trouxe %d eventos", alvo, len(snap))
	}
	if snap[len(snap)-1].Seq != alvo {
		t.Fatalf("o último do snapshot é o seq %d, quer %d", snap[len(snap)-1].Seq, alvo)
	}
	if err := destino.IngestStream(ctx, stream, snap); err != nil {
		t.Fatalf("IngestStream: %v", err)
	}
	head, err := destino.StreamHead(ctx, stream)
	if err != nil {
		t.Fatalf("StreamHead: %v", err)
	}
	if head != alvo {
		t.Fatalf("o restauro parou em %d, quer %d", head, alvo)
	}
	// E o que ficou é o PREFIXO da origem, não uma amostra: mesmo EventID no mesmo seq.
	relido, err := destino.Read(ctx, stream, 1)
	if err != nil {
		t.Fatalf("reler: %v", err)
	}
	for i := 0; i < alvo; i++ {
		if relido[i].EventID != original[i].EventID {
			t.Fatalf("seq %d: EventID %q != %q", i+1, relido[i].EventID, original[i].EventID)
		}
	}
}

// TestAOS101_UmRestauroInterrompidoRETOMA é a propriedade que a porta exige, medida.
//
// No substrato replicado um restauro pode morrer a meio e o prefixo fica DURÁVEL — e não
// se limpa, porque deny_purge. Se repetir a chamada não convergisse, o stream-alvo ficava
// envenenado para sempre e a única saída era restaurar para outro. Aqui simula-se a morte
// entregando primeiro só metade do lote, e depois o lote INTEIRO, que é exactamente o que
// um restaurador faz ao repetir: ele não sabe o que passou, e não tem de saber.
func TestAOS101_UmRestauroInterrompidoRETOMA(t *testing.T) {
	addr := servidor(t)
	origem, _, _ := abrirBackup(t, addr, "rorg")
	destino, _, _ := abrirBackup(t, addr, "rdst")
	ctx := context.Background()
	const stream = "run-retoma"

	original := semear(t, origem, stream, 10)
	snap, err := origem.SnapshotStream(ctx, stream, 0)
	if err != nil {
		t.Fatalf("SnapshotStream: %v", err)
	}

	// A "morte a meio": metade entregue e durável.
	if err := destino.IngestStream(ctx, stream, snap[:5]); err != nil {
		t.Fatalf("primeira metade: %v", err)
	}

	// A retoma: o MESMO lote, inteiro. Um IngestStream não-retomável recusaria isto com
	// ErrRestoreOrder (o lote começa em 1 e o log está em 5), e o alvo ficaria preso.
	if err := destino.IngestStream(ctx, stream, snap); err != nil {
		t.Fatalf("retoma com o lote inteiro: %v", err)
	}

	relido, err := destino.Read(ctx, stream, 1)
	if err != nil {
		t.Fatalf("reler: %v", err)
	}
	if len(relido) != len(original) {
		t.Fatalf("depois da retoma o destino tem %d eventos, quer %d — a retoma DUPLICOU ou perdeu", len(relido), len(original))
	}
	for i := range original {
		if relido[i].EventID != original[i].EventID || relido[i].Seq != original[i].Seq {
			t.Fatalf("seq %d: a retoma não reconstruiu a mesma história", original[i].Seq)
		}
	}

	// E repetir uma terceira vez, já completo, é um não-acontecimento.
	if err := destino.IngestStream(ctx, stream, snap); err != nil {
		t.Fatalf("terceira chamada, já completa: %v", err)
	}
	if fim, _ := destino.StreamHead(ctx, stream); fim != uint64(len(original)) {
		t.Fatalf("head depois da terceira chamada = %d, quer %d", fim, len(original))
	}
}

// TestAOS101_UmAlvoComOUTRAHistoriaERecusado — a falha que nunca pode passar em silêncio.
//
// Se o alvo já tem eventos naqueles seqs mas são OUTROS eventos, continuar costuraria dois
// passados diferentes num log que depois verificaria como íntegro: gapless, append-only,
// hash-chain coerente — e falso. É o pior desfecho possível de um restauro, e por isso a
// sobreposição é confrontada evento a evento em vez de saltada.
func TestAOS101_UmAlvoComOUTRAHistoriaERecusado(t *testing.T) {
	addr := servidor(t)
	origem, _, _ := abrirBackup(t, addr, "dorg")
	destino, _, _ := abrirBackup(t, addr, "ddst")
	ctx := context.Background()
	const stream = "run-divergente"

	snapA, err := func() ([]eventstore.Event, error) {
		semear(t, origem, stream, 6)
		return origem.SnapshotStream(ctx, stream, 0)
	}()
	if err != nil {
		t.Fatalf("snapshot da origem: %v", err)
	}

	// O destino ganha uma história PRÓPRIA nos mesmos seqs — eventos legítimos, mas outros.
	semear(t, destino, stream, 6)

	err = destino.IngestStream(ctx, stream, snapA)
	if !errors.Is(err, eventstore.ErrRestoreDivergent) {
		t.Fatalf("IngestStream sobre outra história = %v, quer ErrRestoreDivergent", err)
	}

	// E o alvo NÃO foi tocado: continua com a sua própria história, com 6 eventos.
	head, errH := destino.StreamHead(ctx, stream)
	if errH != nil {
		t.Fatalf("StreamHead: %v", errH)
	}
	if head != 6 {
		t.Fatalf("depois da recusa o alvo está em %d — a recusa escreveu alguma coisa", head)
	}
}

// TestAOS101_UmLoteQueComecaDepoisDoHeadAbririaUmBuraco.
func TestAOS101_UmLoteQueComecaDepoisDoHeadAbririaUmBuraco(t *testing.T) {
	addr := servidor(t)
	origem, _, _ := abrirBackup(t, addr, "borg")
	destino, _, _ := abrirBackup(t, addr, "bdst")
	ctx := context.Background()
	const stream = "run-buraco"

	semear(t, origem, stream, 8)
	snap, err := origem.SnapshotStream(ctx, stream, 0)
	if err != nil {
		t.Fatalf("SnapshotStream: %v", err)
	}
	// O destino está vazio; entregar a partir do seq 4 deixaria 1..3 por preencher.
	err = destino.IngestStream(ctx, stream, snap[3:])
	if !errors.Is(err, eventstore.ErrRestoreOrder) {
		t.Fatalf("lote a começar depois do head = %v, quer ErrRestoreOrder", err)
	}
	if head, _ := destino.StreamHead(ctx, stream); head != 0 {
		t.Fatalf("o destino ficou em %d depois de uma recusa", head)
	}
}

// TestAOS101_StreamHeadPerguntaAoServidorENaoAVistaLocal.
//
// A distinção não é académica: é a mesma que o DEF-282 mediu. Um head lido da cache do
// processo diria «8» a um exportador enquanto outro processo já escreveu o 9.º — e o
// backup ficaria com um buraco que ninguém veria até ao restauro.
func TestAOS101_StreamHeadPerguntaAoServidorENaoAVistaLocal(t *testing.T) {
	addr := servidor(t)
	a, nome, prefixo := abrirBackup(t, addr, "horg")
	ctx := context.Background()
	const stream = "run-head"

	semear(t, a, stream, 4)

	// Um SEGUNDO handle, ligação independente, sobre o MESMO stream físico.
	b, err := jetstream.Abrir(addr,
		jetstream.ComNomeDeStream(nome),
		jetstream.ComPrefixoDeSubject(prefixo),
		jetstream.ComPrazo(prazo),
		jetstream.SemCriarStream(),
	)
	if err != nil {
		t.Fatalf("segundo handle: %v", err)
	}
	defer func() { _ = b.Close() }()

	if head, errH := b.StreamHead(ctx, stream); errH != nil || head != 4 {
		t.Fatalf("head no segundo handle = %d (err %v), quer 4", head, errH)
	}
	// O primeiro handle escreve; o segundo tem de VER, sem reabrir nada.
	if _, err := a.Append(ctx, stream, eventstore.EventInput{
		Type: "aos.teste.semente.v1", Payload: []byte(`{"i":5}`),
		RunID: stream, StepID: "passo-5",
	}); err != nil {
		t.Fatalf("quinto append: %v", err)
	}
	head, err := b.StreamHead(ctx, stream)
	if err != nil {
		t.Fatalf("StreamHead depois da escrita alheia: %v", err)
	}
	if head != 5 {
		t.Fatalf("head = %d depois de outro processo escrever o 5.º — a porta respondeu da CACHE", head)
	}
}
