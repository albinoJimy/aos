package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/jetstream"
)

// aos100_no_sobre_substrato_replicado_test.go — a prova de que o nó CORRE sobre o Event
// Store replicado, e não apenas de que o adaptador existe.
//
// Sem cluster estes testes são SALTADOS. Um substrato falso mediria o falso.
//
//	AOS_NATS_URL=127.0.0.1:14225 go test ./ -run AOS100 -v

const envClusterAOS100 = "AOS_NATS_URL"

func clusterAOS100(t *testing.T) string {
	t.Helper()
	addr := os.Getenv(envClusterAOS100)
	if addr == "" {
		t.Skipf("sem cluster: define %s (túnel SSH para o nó 0)", envClusterAOS100)
	}
	return addr
}

func sufixoAOS100(t *testing.T) string {
	t.Helper()
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("sufixo: %v", err)
	}
	return hex.EncodeToString(b[:])
}

// configReplicada devolve uma Config apontada ao cluster, com um stream próprio deste
// teste e um WORM próprio (o WORM continua a ser LOCAL — ver o teste das duas réplicas).
func configReplicada(t *testing.T, addr, stream, wormDir string) Config {
	t.Helper()
	cfg := tnBaseConfig()
	cfg.EventStoreNATS = addr
	cfg.EventStoreNATSStream = stream
	cfg.EventStoreNATSReplicas = 3
	cfg.WORMPath = filepath.Join(wormDir, "worm.wal")
	return cfg
}

// TestAOS100_NoArrancaSobreOEventStoreReplicado prova que o nó compõe o substrato
// replicado — não o WAL local — e que o escreve e lê através dele.
func TestAOS100_NoArrancaSobreOEventStoreReplicado(t *testing.T) {
	addr := clusterAOS100(t)
	stream := "AOSNODE_" + sufixoAOS100(t)

	node, err := Bootstrap(context.Background(), configReplicada(t, addr, stream, t.TempDir()), io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap sobre substrato replicado: %v", err)
	}
	t.Cleanup(func() {
		if js, ok := node.EventStore.(*jetstream.Store); ok {
			_ = js.ApagarStream() // sem isto cada execucao deixa lixo no cluster
		}
		_ = node.Close()
	})

	// É MESMO o adaptador, e não o store de referência: sem esta asserção o teste
	// passaria com um nó que caiu para in-memory sem ninguém dar por isso.
	if _, ok := node.EventStore.(*jetstream.Store); !ok {
		t.Fatalf("node.EventStore é %T, quer *jetstream.Store — o nó não está sobre o substrato replicado", node.EventStore)
	}
	if !node.EventStore.Healthy() {
		t.Fatal("Healthy() falso num nó acabado de arrancar")
	}

	ctx := context.Background()
	const run = "run-do-no"
	res, err := node.EventStore.Append(ctx, run, eventstore.EventInput{
		Type: "aos100.no.escreveu", Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("Append pelo nó: %v", err)
	}
	if res.Seq != 1 {
		t.Fatalf("Seq = %d, quer 1 — o seq do AOS tem de ser gapless desde 1 por stream", res.Seq)
	}

	evs, err := node.EventStore.Read(ctx, run, 1)
	if err != nil {
		t.Fatalf("Read pelo nó: %v", err)
	}
	if len(evs) != 1 || evs[0].Type != "aos100.no.escreveu" {
		t.Fatalf("Read devolveu %d evento(s): %+v", len(evs), evs)
	}
	got, serr := node.EventStore.Streams()
	if serr != nil {
		t.Fatalf("Streams(): %v", serr)
	}
	if len(got) != 1 || got[0] != run {
		t.Fatalf("Streams() = %v, quer [%s]", got, run)
	}
}

// TestAOS100_DuasReplicasDoNoSobreOMesmoEventStore é o teste que o AOS-285 tornava
// IMPOSSÍVEL, e é a razão de o guard ter sido revisto.
//
// # O que este teste mede, e porque é a prova central
//
// Com o WAL local, duas réplicas do nó sobre o mesmo Event Store eram uma configuração
// INSEGURA — o substrato não arbitrava entre processos (DEF-282, medido) — e o guard de
// arranque de AOS-285 recusava-a fail-closed. Correcto, e continua correcto para esse
// deployment.
//
// Com o substrato REPLICADO, essa mesma configuração passa a ser o OBJECTIVO: é para
// isso que o AOS-100 existe. Se o guard continuasse a aplicar-se, estaria a recusar
// exactamente o que se quer. Este teste falha se alguém o repuser.
//
// O WORM continua LOCAL e por isso cada réplica tem o SEU — dois escritores forkam a
// hash-chain (AOS-284, medido), e nisso o AOS-100 não muda nada. É por isso que os dois
// guards são funções separadas, e o teste dá caminhos de WORM distintos.
func TestAOS100_DuasReplicasDoNoSobreOMesmoEventStore(t *testing.T) {
	addr := clusterAOS100(t)
	stream := "AOSREPL_" + sufixoAOS100(t)

	primeira, err := Bootstrap(context.Background(), configReplicada(t, addr, stream, t.TempDir()), io.Discard)
	if err != nil {
		t.Fatalf("1.ª réplica: %v", err)
	}
	t.Cleanup(func() {
		if js, ok := primeira.EventStore.(*jetstream.Store); ok {
			_ = js.ApagarStream()
		}
		_ = primeira.Close()
	})

	segunda, err := Bootstrap(context.Background(), configReplicada(t, addr, stream, t.TempDir()), io.Discard)
	if err != nil {
		t.Fatalf("2.ª réplica sobre o MESMO Event Store: %v\n"+
			"Com um substrato que arbitra entre processos, N réplicas é o OBJECTIVO — "+
			"recusá-las é recusar exactamente o que o AOS-100 entrega. Ver guardDePosseAplicavel.", err)
	}
	defer func() { _ = segunda.Close() }()

	// E arbitram MESMO uma contra a outra: as duas afirmam o mesmo expected_seq, e só
	// uma pode ganhar. Sem isto, o teste provava só que ambas arrancaram.
	ctx := context.Background()
	const run = "run-disputado"
	facto := eventstore.EventInput{Type: "aos100.disputa", Payload: json.RawMessage(`{}`)}

	if _, err := primeira.EventStore.Append(ctx, run, facto, eventstore.WithExpectedSeq(0)); err != nil {
		t.Fatalf("1.ª réplica não conseguiu escrever: %v", err)
	}
	_, recusa := segunda.EventStore.Append(ctx, run, facto, eventstore.WithExpectedSeq(0))
	if recusa == nil {
		t.Fatal("as DUAS réplicas escreveram afirmando expected_seq=0 — o substrato NÃO está a arbitrar, " +
			"e correr duas réplicas sobre ele não é seguro")
	}

	// A 2.ª vê o que a 1.ª escreveu — é o mesmo log, não duas cópias.
	evs, err := segunda.EventStore.Read(ctx, run, 1)
	if err != nil {
		t.Fatalf("2.ª réplica não leu o stream: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("2.ª réplica vê %d evento(s), quer 1 — as réplicas não partilham o log", len(evs))
	}
	t.Logf("duas réplicas do nó sobre o mesmo Event Store: ambas arrancaram, a disputa foi recusada com %v", recusa)
}

// TestAOS100_GuardDoWORMMantemSeComSubstratoReplicado fixa a metade que NÃO muda.
//
// A revisão do guard é escopada ao Event Store. Duas réplicas apontadas ao mesmo
// ficheiro de WORM continuam a ser recusadas — porque dois escritores FORKAM a
// hash-chain e o arranque seguinte recusa a cadeia como adulterada (AOS-284, medido).
// Um AOS-100 que tivesse desligado os dois guards de uma vez teria aberto esse buraco.
func TestAOS100_GuardDoWORMMantemSeComSubstratoReplicado(t *testing.T) {
	addr := clusterAOS100(t)
	stream := "AOSWORM_" + sufixoAOS100(t)
	wormPartilhado := t.TempDir()

	primeira, err := Bootstrap(context.Background(), configReplicada(t, addr, stream, wormPartilhado), io.Discard)
	if err != nil {
		t.Fatalf("1.ª réplica: %v", err)
	}
	t.Cleanup(func() {
		if js, ok := primeira.EventStore.(*jetstream.Store); ok {
			_ = js.ApagarStream()
		}
		_ = primeira.Close()
	})

	// MESMO WORM: tem de ser recusada, substrato replicado ou não.
	_, err = Bootstrap(context.Background(), configReplicada(t, addr, stream, wormPartilhado), io.Discard)
	if err == nil {
		t.Fatal("a 2.ª réplica arrancou sobre o MESMO ficheiro de WORM — dois escritores forkam a " +
			"hash-chain e o arranque seguinte recusa-a como adulterada (AOS-284). O guard do WORM " +
			"NÃO faz parte da revisão de AOS-100")
	}
}

// TestAOS100_GuardNaoTrancaUmWALQueONoNuncaAbre é a armadilha que a revisão do guard
// teve de desarmar.
//
// Com AOS_EVENTSTORE_NATS preenchido, o Event Store PRECEDE o WAL local — o nó nunca
// abre o ficheiro. Se o guard continuasse a olhar apenas para «existe um path?», um
// EventStorePath residual na config (de um deployment anterior, ou de um default de
// ambiente) faria a 2.ª réplica ser recusada por um ficheiro que NENHUMA das duas usa.
func TestAOS100_GuardNaoTrancaUmWALQueONoNuncaAbre(t *testing.T) {
	addr := clusterAOS100(t)
	stream := "AOSRESID_" + sufixoAOS100(t)
	walResidual := filepath.Join(t.TempDir(), "residual.wal")

	cfg1 := configReplicada(t, addr, stream, t.TempDir())
	cfg1.EventStorePath = walResidual // residual, e o mesmo para as duas
	cfg2 := configReplicada(t, addr, stream, t.TempDir())
	cfg2.EventStorePath = walResidual

	primeira, err := Bootstrap(context.Background(), cfg1, io.Discard)
	if err != nil {
		t.Fatalf("1.ª réplica: %v", err)
	}
	t.Cleanup(func() {
		if js, ok := primeira.EventStore.(*jetstream.Store); ok {
			_ = js.ApagarStream()
		}
		_ = primeira.Close()
	})

	segunda, err := Bootstrap(context.Background(), cfg2, io.Discard)
	if err != nil {
		t.Fatalf("2.ª réplica recusada por um WAL residual que nenhuma das duas abre: %v", err)
	}
	defer func() { _ = segunda.Close() }()

	if _, ok := primeira.EventStore.(*jetstream.Store); !ok {
		t.Fatalf("com EventStoreNATS preenchido o nó abriu %T — o WAL residual ganhou à precedência", primeira.EventStore)
	}
	if _, err := os.Stat(walResidual); err == nil {
		t.Fatal("o WAL residual foi CRIADO — o nó abriu um ficheiro que a precedência diz que não usa")
	}
}

// --- Soberania (AC5 do AOS-100, ADR-011) ------------------------------------

// TestAOS100_BoardsDeclaradosExigemRegiaoDoEventStore — um nó que declara boards com
// região autorizada (read-path soberano, AOS-094) sobre um Event Store REPLICADO sem
// fronteira é uma contradição servida em silêncio: as leituras respeitam a região e os
// DADOS podem estar em qualquer par do cluster.
//
// A recusa é ANTERIOR a qualquer ligação — daí o endereço inalcançável: se dependesse de
// ligar, este teste falharia com um erro de rede em vez do sentinela.
func TestAOS100_BoardsDeclaradosExigemRegiaoDoEventStore(t *testing.T) {
	cfg := tnBaseConfig()
	cfg.EventStoreNATS = "127.0.0.1:1"
	cfg.BoardRegions = map[string]string{"board-ue": "eu-west"}
	cfg.WORMPath = filepath.Join(t.TempDir(), "worm.wal")

	_, err := Bootstrap(context.Background(), cfg, io.Discard)
	if !errors.Is(err, ErrEventStoreReplicadoSemRegiao) {
		t.Fatalf("erro = %v; quer ErrEventStoreReplicadoSemRegiao — um read-path soberano sobre um "+
			"Event Store sem fronteira não pode arrancar", err)
	}
}

// TestAOS100_RegiaoDoEventStoreForaDosBoardsERecusada — config auto-contraditória:
// declarar que os dados vivem numa região que board nenhum autoriza.
func TestAOS100_RegiaoDoEventStoreForaDosBoardsERecusada(t *testing.T) {
	cfg := tnBaseConfig()
	cfg.EventStoreNATS = "127.0.0.1:1"
	cfg.EventStoreNATSRegion = "us-east"
	cfg.BoardRegions = map[string]string{"board-ue": "eu-west"}
	cfg.WORMPath = filepath.Join(t.TempDir(), "worm.wal")

	_, err := Bootstrap(context.Background(), cfg, io.Discard)
	if !errors.Is(err, ErrEventStoreRegiaoForaDosBoards) {
		t.Fatalf("erro = %v; quer ErrEventStoreRegiaoForaDosBoards", err)
	}
}

// TestAOS100_SemBoardsAFronteiraFicaDormente — quem não declara soberania não passa a
// precisar dela. Retro-compatibilidade, medida e não assumida.
func TestAOS100_SemBoardsAFronteiraFicaDormente(t *testing.T) {
	addr := clusterAOS100(t)
	stream := "AOSDORM_" + sufixoAOS100(t)
	cfg := configReplicada(t, addr, stream, t.TempDir()) // sem BoardRegions, sem região

	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap sem fronteira declarada: %v", err)
	}
	t.Cleanup(func() {
		if js, ok := node.EventStore.(*jetstream.Store); ok {
			_ = js.ApagarStream()
		}
		_ = node.Close()
	})
}

// TestAOS100_NoArrancaComFronteiraRegionalImposta — o caminho completo, contra o
// cluster: o nó declara board+região, o stream fica com colocação, e o nó escreve.
func TestAOS100_NoArrancaComFronteiraRegionalImposta(t *testing.T) {
	addr := clusterAOS100(t)
	stream := "AOSSOB_" + sufixoAOS100(t)
	cfg := configReplicada(t, addr, stream, t.TempDir())
	cfg.EventStoreNATSRegion = "eu-west" // a região que o cluster de medição anuncia
	cfg.BoardRegions = map[string]string{"board-ue": "eu-west"}

	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap com fronteira regional: %v", err)
	}
	t.Cleanup(func() {
		if js, ok := node.EventStore.(*jetstream.Store); ok {
			_ = js.ApagarStream()
		}
		_ = node.Close()
	})

	js, ok := node.EventStore.(*jetstream.Store)
	if !ok {
		t.Fatalf("node.EventStore é %T", node.EventStore)
	}
	if js.Region() != "eu-west" {
		t.Fatalf("Region() = %q, quer eu-west — a fronteira não chegou ao substrato", js.Region())
	}
	if _, err := node.EventStore.Append(context.Background(), "run-soberano",
		eventstore.EventInput{Type: "aos100.soberania", Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("Append num nó com fronteira: %v", err)
	}
}
