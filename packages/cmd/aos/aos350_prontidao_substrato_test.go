package main

// AOS-350 — UM SUBSTRATO MORTO ATRAVESSAVA A PRONTIDÃO, O GAUGE E O SLI SEM ACENDER NADA.
//
// `Healthy()` era `!closed` nos DOIS backends do Event Store, e `closed` só muda em
// `Close()`. Um WAL envenenado — que recusa 100% das escritas em voz alta — mantinha
// `Healthy() == true`, e com ele os três consumidores desta condição: `/readyz` ficava
// 200 VERDE, o gauge `aos_eventstore_healthy` ficava 1, e o SLI `controlPlaneAvailable`
// mantinha-se a 1.0 com o alerta `control_plane_availability_low` calado.
//
// A correcção está no SUBSTRATO ([eventstore.Store.Healthy] passa a reflectir o estado do
// WAL; ver `substrate/eventstore/prontidao_test.go`, que prova a passagem a `false` com o
// WAL a recusar escritas). Este ficheiro prova a OUTRA METADE da cadeia — que os três
// consumidores reagem —, e prova-a onde ela vive: na fronteira HTTP que o orquestrador de
// contentores observa.
//
// A composição é deliberada: o substrato não é alcançável a partir daqui (o estado do WAL
// é privado ao pacote `eventstore`), e um teste que fingisse alcançá-lo mediria a sua
// própria encenação. Aqui injecta-se um store que RECUSA, e mede-se o que o nó faz com
// isso.

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// storeQueRecusa é um [EventStorePort] que declara não estar utilizável. É o estado em
// que um WAL envenenado põe o store de referência — e em que uma ligação perdida põe o
// backend JetStream.
type storeQueRecusa struct {
	EventStorePort
	saudavel bool
}

func (s *storeQueRecusa) Healthy() bool { return s.saudavel }

// TestAOS350_ReadyzEGaugeSeguemOSubstrato mede os dois consumidores de uma vez, porque é
// de uma vez que eles falham: partilham o mesmo predicado.
func TestAOS350_ReadyzEGaugeSeguemOSubstrato(t *testing.T) {
	sonda := func(t *testing.T, saudavel bool) (int, string) {
		t.Helper()
		base, err := eventstore.New()
		if err != nil {
			t.Fatalf("eventstore.New: %v", err)
		}
		cfg := tnBaseConfig()
		cfg.EventStore = &storeQueRecusa{EventStorePort: base, saudavel: saudavel}
		node, err := Bootstrap(context.Background(), cfg, io.Discard)
		if err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
		defer func() { _ = node.Close() }()
		svc, h := newAPI(t, node)
		defer func() { _ = svc.Shutdown(context.Background()) }()

		code, _ := getProbe(h, "/readyz")
		return code, corpoDe(t, h, "/metrics")
	}

	// CONTROLO: com o substrato saudável o nó está pronto e o gauge vale 1. Sem este
	// ramo, «503» seria indistinguível de «o teste nunca chega a estar pronto».
	code, metrics := sonda(t, true)
	if code != http.StatusOK {
		t.Fatalf("/readyz com o substrato SAUDÁVEL devia dar 200, veio %d", code)
	}
	if !strings.Contains(metrics, "aos_eventstore_healthy 1") {
		t.Fatalf("gauge com o substrato saudável não é 1:\n%s", extraiGauge(metrics))
	}

	// O SUBSTRATO RECUSA ESCRITAS. Era aqui que o /readyz ficava 200 verde.
	code, metrics = sonda(t, false)
	if code == http.StatusOK {
		t.Fatal("/readyz respondeu 200 com o Event Store a RECUSAR escritas — o orquestrador " +
			"de contentores continua a encaminhar tráfego para um nó morto (AOS-350)")
	}
	if code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz = %d, quero 503", code)
	}
	if !strings.Contains(metrics, "aos_eventstore_healthy 0") {
		t.Fatalf("gauge aos_eventstore_healthy não foi a 0 com o substrato a recusar:\n%s", extraiGauge(metrics))
	}
}

// corpoDe faz um GET e devolve o corpo como string.
func corpoDe(t *testing.T, h http.Handler, target string) string {
	t.Helper()
	rec := postJSON(h, http.MethodGet, target, nil)
	return rec.Body.String()
}

// extraiGauge reduz o dump de métricas às linhas do gauge, para a mensagem de falha
// nomear o que viu em vez de despejar tudo.
func extraiGauge(metrics string) string {
	var out []string
	for _, l := range strings.Split(metrics, "\n") {
		if strings.Contains(l, "aos_eventstore_healthy") {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return "(o gauge aos_eventstore_healthy NÃO aparece no /metrics)"
	}
	return strings.Join(out, "\n")
}

// TestAOS350_ReadyzSegueUmWALREALQueRecusaEscritas é a metade que o teste acima não mede, e
// a revisão adversarial teve razão em o exigir: `storeQueRecusa` sobrepõe `Healthy()` para
// devolver o booleano que o próprio teste define, pelo que teria passado no código
// PRÉ-correcção. Prova a cablagem, não o ticket.
//
// Aqui o substrato é REAL: um Event Store durável sobre um WAL em disco, que se ENCOLHE por
// baixo do nó. O append seguinte detecta a dessincronização (AOS-349), o WAL passa a recusar
// escritas, e é isso — e não um booleano de teste — que tem de fazer o `/readyz` cair.
func TestAOS350_ReadyzSegueUmWALREALQueRecusaEscritas(t *testing.T) {
	ctx := context.Background()
	wal := filepath.Join(t.TempDir(), "events.wal")
	es, err := eventstore.Open(wal)
	if err != nil {
		t.Fatalf("eventstore.Open: %v", err)
	}
	// O store é INJECTADO, logo o nó não é dono dele e o `node.Close()` não o fecha. Sem
	// este defer o ficheiro fica aberto e a limpeza do TempDir falha em Windows.
	defer func() { _ = es.Close() }()
	cfg := tnBaseConfig()
	cfg.EventStore = es
	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = node.Close() }()
	svc, h := newAPI(t, node)
	defer func() { _ = svc.Shutdown(ctx) }()

	// Uma escrita real, para o WAL ter conteúdo e o nó estar genuinamente pronto.
	if _, err := es.Append(ctx, "run-350", eventstore.EventInput{
		Type: "probe", Payload: []byte(`{}`), RunID: "run-350", StepID: "s1",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if code, _ := getProbe(h, "/readyz"); code != http.StatusOK {
		t.Fatalf("/readyz com o substrato saudável = %d, quero 200", code)
	}

	// O FICHEIRO ENCOLHE POR BAIXO — é o que um inspector fazia antes de AOS-347, e o que
	// um operador ou um script de rotação ainda podem fazer.
	if err := os.Truncate(wal, 0); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := es.Append(ctx, "run-350", eventstore.EventInput{
		Type: "probe", Payload: []byte(`{}`), RunID: "run-350", StepID: "s2",
	}); err == nil {
		t.Fatal("o append sobre um WAL encolhido devia ser recusado (AOS-349)")
	}

	// O SUBSTRATO ESTÁ MORTO. Era aqui que o /readyz ficava 200 verde e o orquestrador
	// continuava a encaminhar tráfego.
	code, _ := getProbe(h, "/readyz")
	if code == http.StatusOK {
		t.Fatal("/readyz respondeu 200 com um WAL REAL a recusar todas as escritas (AOS-350)")
	}
	if code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz = %d, quero 503", code)
	}
	if m := corpoDe(t, h, "/metrics"); !strings.Contains(m, "aos_eventstore_healthy 0") {
		t.Fatalf("gauge não foi a 0 com o WAL real morto: %s", extraiGauge(m))
	}
}
