package jetstream_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/conformance"
	"github.com/aos-ref/substrate/eventstore/jetstream"
)

// Sem cluster estes testes são SALTADOS. Um mock do JetStream mediria o mock — e é
// exactamente o erro que o handoff do AOS-100 manda não cometer.
//
//	AOS_NATS_URL=127.0.0.1:14225 go test ./jetstream/ -v
const envServidor = "AOS_NATS_URL"

const prazo = 15 * time.Second

func servidor(t *testing.T) string {
	t.Helper()
	addr := os.Getenv(envServidor)
	if addr == "" {
		t.Skipf("sem cluster: define %s (túnel SSH para o nó 0 do cluster)", envServidor)
	}
	return addr
}

func sufixo(t *testing.T) string {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("sufixo: %v", err)
	}
	return hex.EncodeToString(b[:])
}

// substratoJetStream devolve uma fábrica que, a cada chamada, entrega n handles
// INDEPENDENTES sobre um stream JetStream NOVO.
//
// Independentes é literal: n ligações TCP distintas, cada uma com o seu `Store` e a sua
// cache de estado. É o que n processos são um para o outro — e é a condição sem a qual
// as sondas mediriam o interior de um processo e passariam sempre.
func substratoJetStream(t *testing.T, addr string) conformance.Substrate {
	t.Helper()
	return func(n int) ([]eventstore.EventStore, func(), error) {
		s := sufixo(t)
		nome, prefixo := "AOSCONF_"+s, "aosconf."+s

		var abertos []*jetstream.Store
		release := func() {
			for _, st := range abertos {
				_ = st.Close()
			}
		}
		for i := 0; i < n; i++ {
			opts := []jetstream.Option{
				jetstream.ComNomeDeStream(nome),
				jetstream.ComPrefixoDeSubject(prefixo),
				jetstream.ComPrazo(prazo),
				jetstream.ComReplicas(3),
			}
			if i > 0 {
				opts = append(opts, jetstream.SemCriarStream())
			}
			st, err := jetstream.Abrir(addr, opts...)
			if err != nil {
				release()
				return nil, func() {}, err
			}
			abertos = append(abertos, st)
		}
		handles := make([]eventstore.EventStore, len(abertos))
		for i, st := range abertos {
			handles[i] = st
		}
		return handles, release, nil
	}
}

// TestConformidade_OEventStoreDoAOSPassaAArbitrar é o gate do AOS-100.
//
// É o MESMO instrumento que, contra o Event Store de referência, reporta as quatro
// propriedades AUSENTES (ver TestSensor_ReferenciaNaoArbitraEntreEscritores). Aqui é
// apontado ao adaptador. Se passar, a propriedade que o DEF-282 mediu em falta deixou
// de faltar — e a disciplina de posse do AOS (LeaseManager/AOS-018, FencedAppender,
// ADR-023) passa a assentar em chão firme em vez de numa suposição.
func TestConformidade_OEventStoreDoAOSPassaAArbitrar(t *testing.T) {
	conformance.RunArbitration(t, substratoJetStream(t, servidor(t)))
}

// TestContrato_SeqDoAOSEGaplessDesdeUm fixa a separação entre as DUAS numerações.
//
// O JetStream numera GLOBALMENTE por stream físico: dois streams do AOS a partilhá-lo
// vêem seqs 1,3,5 e 2,4,6 nessa numeração. O contrato C2 exige que o seq do AOS seja
// gapless desde 1 POR STREAM. Confundi-los daria um log com buracos e quebraria replay,
// re-hidratação e a hash-chain que assentam nele — por isso o seq do AOS vive dentro do
// envelope e o do servidor nunca é exposto.
func TestContrato_SeqDoAOSEGaplessDesdeUm(t *testing.T) {
	addr := servidor(t)
	hs, release, err := substratoJetStream(t, addr)(1)
	if err != nil {
		t.Fatalf("abrir substrato: %v", err)
	}
	defer release()
	st, ctx := hs[0], context.Background()

	// Intercala escritas em DOIS streams do AOS sobre o mesmo stream físico.
	for i := 0; i < 3; i++ {
		for _, run := range []string{"run-a", "run-b"} {
			if _, err := st.Append(ctx, run, facto("contrato.intercalado")); err != nil {
				t.Fatalf("Append em %s: %v", run, err)
			}
		}
	}

	for _, run := range []string{"run-a", "run-b"} {
		evs, err := st.Read(ctx, run, 1)
		if err != nil {
			t.Fatalf("Read de %s: %v", run, err)
		}
		if len(evs) != 3 {
			t.Fatalf("%s tem %d eventos, quer 3", run, len(evs))
		}
		for i, ev := range evs {
			if ev.Seq != uint64(i+1) {
				t.Fatalf("%s[%d].Seq = %d, quer %d — o seq do AOS tem de ser gapless desde 1 POR STREAM, "+
					"e não a numeração global do stream físico", run, i, ev.Seq, i+1)
			}
			if ev.StreamID != run {
				t.Fatalf("%s[%d].StreamID = %q — o envelope não corresponde ao stream", run, i, ev.StreamID)
			}
		}
	}
}

// TestContrato_StreamInexistenteEStreamNotFound — o contrato C2 distingue «stream vazio»
// de «erro»: um Read de um stream sem eventos devolve E_STREAM_NOT_FOUND, e há
// chamadores que dependem disso para saber que um run é novo.
func TestContrato_StreamInexistenteEStreamNotFound(t *testing.T) {
	addr := servidor(t)
	hs, release, err := substratoJetStream(t, addr)(1)
	if err != nil {
		t.Fatalf("abrir substrato: %v", err)
	}
	defer release()

	if _, err := hs[0].Read(context.Background(), "run-que-nunca-existiu", 1); !errors.Is(err, eventstore.ErrStreamNotFound) {
		t.Fatalf("Read de stream inexistente devolveu %v, quer E_STREAM_NOT_FOUND", err)
	}
}

// TestContrato_StreamIDNaoRepresentavelERecusado — um stream_id com ponto seria
// escapado em silêncio para um subject VIZINHO, onde outro stream leria os nossos
// eventos. Recusar é a única resposta segura.
func TestContrato_StreamIDNaoRepresentavelERecusado(t *testing.T) {
	addr := servidor(t)
	hs, release, err := substratoJetStream(t, addr)(1)
	if err != nil {
		t.Fatalf("abrir substrato: %v", err)
	}
	defer release()
	ctx := context.Background()

	for _, mau := range []string{"run.com.ponto", "run com espaco", "run*", "run>", ""} {
		if _, err := hs[0].Append(ctx, mau, facto("contrato.mau")); err == nil {
			t.Errorf("stream_id %q foi aceite", mau)
		}
	}
	// E o que É representável continua a passar — incluindo o `lease:<run>` de AOS-018,
	// que é o stream de que a disciplina de posse depende.
	if _, err := hs[0].Append(ctx, "lease:run-1", facto("contrato.lease")); err != nil {
		t.Fatalf("stream_id \"lease:run-1\" recusado (%v) — é o stream do LeaseManager e TEM de passar", err)
	}
}

// TestContrato_SubscribeEntregaPorPush mede o AC2 a partir do código do AOS: um evento
// escrito DEPOIS da subscrição chega ao handler sem ninguém fazer polling.
func TestContrato_SubscribeEntregaPorPush(t *testing.T) {
	addr := servidor(t)
	hs, release, err := substratoJetStream(t, addr)(1)
	if err != nil {
		t.Fatalf("abrir substrato: %v", err)
	}
	defer release()
	st, ctx := hs[0], context.Background()

	recebidos := make(chan eventstore.Event, 4)
	sub, err := st.Subscribe(ctx, eventstore.Filter{Types: []string{"contrato.push"}}, func(ev eventstore.Event) {
		recebidos <- ev
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Unsubscribe()
	time.Sleep(500 * time.Millisecond) // o consumidor tem de existir antes da escrita

	if _, err := st.Append(ctx, "run-push", facto("contrato.push")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// E um que o filtro tem de EXCLUIR — senão o teste passaria com um handler que
	// recebe tudo, e não estaria a medir a filtragem.
	if _, err := st.Append(ctx, "run-push", facto("contrato.ignorado")); err != nil {
		t.Fatalf("Append (ignorado): %v", err)
	}

	select {
	case ev := <-recebidos:
		if ev.Type != "contrato.push" {
			t.Fatalf("chegou %q, quer contrato.push — o filtro não está a ser aplicado", ev.Type)
		}
		if ev.StreamID != "run-push" || ev.Seq != 1 {
			t.Fatalf("envelope entregue incoerente: stream=%q seq=%d", ev.StreamID, ev.Seq)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("nada chegou por push em 10s")
	}

	select {
	case ev := <-recebidos:
		t.Fatalf("o filtro deixou passar %q", ev.Type)
	case <-time.After(2 * time.Second):
	}
}

func facto(tipo string) eventstore.EventInput {
	return eventstore.EventInput{Type: tipo, Payload: json.RawMessage(`{}`)}
}

// TestContrato_StreamsERespondidoPeloServidor mede a diferença que faz um backend
// PARTILHADO: os streams que existem são os que QUALQUER escritor criou.
//
// A escrita é feita por um handle e a listagem por OUTRO. Num backend com índice em
// memória — o de referência — o segundo handle não veria nada; é o mesmo defeito que a
// sonda de visibilidade mede.
func TestContrato_StreamsERespondidoPeloServidor(t *testing.T) {
	addr := servidor(t)
	hs, release, err := substratoJetStream(t, addr)(2)
	if err != nil {
		t.Fatalf("abrir substrato: %v", err)
	}
	defer release()
	escritor, leitor := hs[0].(*jetstream.Store), hs[1].(*jetstream.Store)
	ctx := context.Background()

	if !leitor.Healthy() {
		t.Fatal("Healthy() falso num store acabado de abrir")
	}
	for _, run := range []string{"run-x", "run-y"} {
		if _, err := escritor.Append(ctx, run, facto("contrato.streams")); err != nil {
			t.Fatalf("Append em %s: %v", run, err)
		}
	}

	got := leitor.Streams()
	if len(got) != 2 || got[0] != "run-x" || got[1] != "run-y" {
		t.Fatalf("Streams() do OUTRO handle = %v, quer [run-x run-y] — a listagem não está a vir do servidor", got)
	}
}
