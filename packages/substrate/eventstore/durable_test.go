package eventstore

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
)

// singleReplica constrói um store durável de 1 réplica/quórum 1 (determinista para
// os testes de persistência — o foco é o WAL, não a replicação).
func openDurable(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path, WithReplicas(1), WithQuorum(1))
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	return s
}

func appendEv(t *testing.T, s *Store, stream, step, typ, payload string) AppendResult {
	t.Helper()
	res, err := s.Append(context.Background(), stream, EventInput{
		Type:     typ,
		Payload:  []byte(payload),
		RunID:    stream,
		StepID:   step,
		Producer: Producer{NHIID: "nhi:test"},
	})
	if err != nil {
		t.Fatalf("Append(%s/%s): %v", stream, step, err)
	}
	return res
}

// TestDurable_RestartFaithful — (a) REINÍCIO FIEL: escreve eventos, fecha, reabre
// sobre o MESMO ficheiro; o store restaurado tem EXACTAMENTE os mesmos eventos/seq/
// heads (Read por stream == original).
func TestDurable_RestartFaithful(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.wal")
	ctx := context.Background()

	s := openDurable(t, path)
	// Dois streams intercalados para provar o agrupamento/ordenação por stream no replay.
	// Cada evento leva um Producer/StepID/ParentStepID/SchemaVersion DISTINTO (e um
	// Producer rico: NHIID + DelegationChain + Scope) para que a fidelidade comparada
	// seja NÃO-TRIVIAL — uma regressão que largasse qualquer destes campos no persist
	// ou no restore seria apanhada pelo reflect.DeepEqual de eventsEqual.
	rich := []struct {
		stream string
		in     EventInput
	}{
		{"run-A", EventInput{Type: "t.a", Payload: []byte(`{"k":1}`), RunID: "run-A", StepID: "s1", SchemaVersion: "1.0",
			Producer: Producer{NHIID: "nhi:alpha", DelegationChain: []DelegationHop{{Sub: "nhi:alpha", ActAs: "human:ana"}}, Scope: []string{"read", "write"}}}},
		{"run-B", EventInput{Type: "t.b", Payload: []byte(`{"k":2}`), RunID: "run-B", StepID: "s1", SchemaVersion: "1.0",
			Producer: Producer{NHIID: "nhi:beta", Scope: []string{"admin"}}}},
		{"run-A", EventInput{Type: "t.a", Payload: []byte(`{"k":3}`), RunID: "run-A", StepID: "s2", ParentStepID: "s1", SchemaVersion: "1.0",
			Producer: Producer{NHIID: "nhi:gamma", DelegationChain: []DelegationHop{{Sub: "nhi:gamma", ActAs: "human:gil"}, {Sub: "human:gil", ActAs: "human:root"}}}}},
		{"run-A", EventInput{Type: "t.a", Payload: []byte(`{"k":4}`), RunID: "run-A", StepID: "s3", ParentStepID: "s2", SchemaVersion: "1.0",
			Producer: Producer{NHIID: "nhi:delta"}}},
		{"run-B", EventInput{Type: "t.b", Payload: []byte(`{"k":5}`), RunID: "run-B", StepID: "s2", ParentStepID: "s1", SchemaVersion: "1.0",
			Producer: Producer{NHIID: "nhi:epsilon", Scope: []string{"x"}}}},
	}
	for _, e := range rich {
		if _, err := s.Append(ctx, e.stream, e.in); err != nil {
			t.Fatalf("append %s/%s: %v", e.stream, e.in.StepID, err)
		}
	}

	origA, _ := s.Read(ctx, "run-A", 1)
	origB, _ := s.Read(ctx, "run-B", 1)
	if len(origA) != 3 || len(origB) != 2 {
		t.Fatalf("pré-restart: A=%d B=%d", len(origA), len(origB))
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// REABRE sobre o mesmo ficheiro.
	s2 := openDurable(t, path)
	defer s2.Close()

	gotA, err := s2.Read(ctx, "run-A", 1)
	if err != nil {
		t.Fatalf("read A pós-restart: %v", err)
	}
	gotB, _ := s2.Read(ctx, "run-B", 1)
	if !eventsEqual(origA, gotA) {
		t.Fatalf("run-A divergiu após restart:\n orig=%v\n got =%v", origA, gotA)
	}
	if !eventsEqual(origB, gotB) {
		t.Fatalf("run-B divergiu após restart")
	}
	// Heads preservados.
	if h, _ := s2.StreamHead(ctx, "run-A"); h != 3 {
		t.Fatalf("head A = %d, quero 3", h)
	}
	if h, _ := s2.StreamHead(ctx, "run-B"); h != 2 {
		t.Fatalf("head B = %d, quero 2", h)
	}
}

// TestDurable_NoDuplicationAfterRestart — (b) NÃO-DUPLICAÇÃO: uma idempotency_key
// repetida APÓS reinício ainda deduplica (não re-executa/duplica); a CAS
// (WithExpectedSeq) ainda barra colisões após replay.
func TestDurable_NoDuplicationAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.wal")
	ctx := context.Background()

	s := openDurable(t, path)
	appendEv(t, s, "run-A", "s1", "t", `{"n":1}`)
	first := appendEv(t, s, "run-A", "s2", "t", `{"n":2}`)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2 := openDurable(t, path)
	defer s2.Close()

	// Dedup: repetir (run-A, s2) devolve o MESMO seq, StatusDuplicate, sem crescer o log.
	dup, err := s2.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":2}`), RunID: "run-A", StepID: "s2", Producer: Producer{NHIID: "nhi:test"}})
	if err != nil {
		t.Fatalf("append duplicado: %v", err)
	}
	if dup.Status != StatusDuplicate || dup.Seq != first.Seq {
		t.Fatalf("dedup falhou após restart: status=%v seq=%d (quero duplicate seq=%d)", dup.Status, dup.Seq, first.Seq)
	}
	if h, _ := s2.StreamHead(ctx, "run-A"); h != 2 {
		t.Fatalf("log cresceu com duplicado: head=%d, quero 2", h)
	}

	// CAS: escrever com expected_seq no passado (1 < head 2) é violação append-only
	// — a CAS foi reconstruída pelo replay.
	_, err = s2.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{}`), RunID: "run-A", StepID: "sX", Producer: Producer{NHIID: "nhi:test"}}, WithExpectedSeq(1))
	if !errors.Is(err, ErrAppendOnlyViolation) {
		t.Fatalf("CAS pós-restart: err=%v, quero ErrAppendOnlyViolation", err)
	}
	// Um novo append com expected_seq correcto (2 == head) prossegue e persiste.
	res, err := s2.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":3}`), RunID: "run-A", StepID: "s3", Producer: Producer{NHIID: "nhi:test"}}, WithExpectedSeq(2))
	if err != nil || res.Seq != 3 {
		t.Fatalf("append pós-CAS: seq=%d err=%v", res.Seq, err)
	}
}

// TestDurable_CrashTruncatedTail — (c) CRASH: truncar o ficheiro a meio do último
// registo (e, noutra variante, corromper o checksum) e o Reopen IGNORA-o, restaurando
// até ao último registo íntegro, sem erro.
func TestDurable_CrashTruncatedTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.wal")
	ctx := context.Background()

	s := openDurable(t, path)
	appendEv(t, s, "run-A", "s1", "t", `{"n":1}`)
	appendEv(t, s, "run-A", "s2", "t", `{"n":2}`)
	appendEv(t, s, "run-A", "s3", "t", `{"n":3}`)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read wal: %v", err)
	}
	// Simula um crash a meio do write do 3º registo: corta 3 bytes do fim (o checksum
	// do último registo fica truncado). O replay deve parar no 2º registo íntegro.
	truncated := full[:len(full)-3]
	if err := os.WriteFile(path, truncated, 0o600); err != nil {
		t.Fatalf("write truncated: %v", err)
	}

	s2 := openDurable(t, path)
	got, err := s2.Read(ctx, "run-A", 1)
	if err != nil {
		t.Fatalf("read pós-crash: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("crash-safety: restaurou %d eventos, quero 2 (último truncado ignorado)", len(got))
	}
	// O store continua a partir do último íntegro: o próximo append é seq 3, e persiste.
	res := appendEv(t, s2, "run-A", "s3b", "t", `{"n":33}`)
	if res.Seq != 3 {
		t.Fatalf("append após crash: seq=%d, quero 3", res.Seq)
	}
	if err := s2.Close(); err != nil {
		t.Fatalf("close s2: %v", err)
	}

	// Persistiu correctamente: reabrir de novo vê 3 eventos.
	s3 := openDurable(t, path)
	defer s3.Close()
	got3, _ := s3.Read(ctx, "run-A", 1)
	if len(got3) != 3 {
		t.Fatalf("após recuperação+append: %d eventos, quero 3", len(got3))
	}
}

// TestDurable_CrashCorruptChecksum — variante de (c): um checksum corrompido no
// último registo é detectado e o registo ignorado (pára no anterior íntegro).
func TestDurable_CrashCorruptChecksum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.wal")
	ctx := context.Background()

	s := openDurable(t, path)
	appendEv(t, s, "run-A", "s1", "t", `{"n":1}`)
	appendEv(t, s, "run-A", "s2", "t", `{"n":2}`)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	full, _ := os.ReadFile(path)
	// Corrompe o último byte (parte do checksum do 2º registo) sem mudar o comprimento.
	corrupt := make([]byte, len(full))
	copy(corrupt, full)
	corrupt[len(corrupt)-1] ^= 0xFF
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}

	s2 := openDurable(t, path)
	defer s2.Close()
	got, err := s2.Read(ctx, "run-A", 1)
	if err != nil {
		t.Fatalf("read pós-corrupção: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("checksum inválido: restaurou %d, quero 1 (registo corrompido ignorado)", len(got))
	}
}

// TestDurable_GarbageLengthHeader — um header de comprimento absurdo (lixo de um
// write parcial) não faz o replay alocar nem falhar: pára no último íntegro.
func TestDurable_GarbageLengthHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.wal")
	ctx := context.Background()

	s := openDurable(t, path)
	appendEv(t, s, "run-A", "s1", "t", `{"n":1}`)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	full, _ := os.ReadFile(path)
	// Anexa um header com comprimento gigante e nenhum payload (write interrompido).
	var junk [4]byte
	binary.BigEndian.PutUint32(junk[:], 0xFFFFFFF0)
	if err := os.WriteFile(path, append(full, junk[:]...), 0o600); err != nil {
		t.Fatalf("write junk: %v", err)
	}

	s2 := openDurable(t, path)
	defer s2.Close()
	got, err := s2.Read(ctx, "run-A", 1)
	if err != nil {
		t.Fatalf("read pós-junk: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("header absurdo: restaurou %d, quero 1", len(got))
	}
}

// TestDurable_OpenFreshCreatesFile — Open sobre um path inexistente cria um store
// durável novo (WAL vazio) sem erro.
func TestDurable_OpenFreshCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.wal")
	s := openDurable(t, path)
	defer s.Close()
	appendEv(t, s, "run-A", "s1", "t", `{}`)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("WAL não foi criado: %v", err)
	}
}

// TestDurable_DefaultReplicasRestart — o caminho de PRODUÇÃO do nó abre o store com
// os defaults (3 réplicas, quórum 2). Prova que o replay via IngestStream reconstrói
// fielmente também no cluster replicado (não só 1 réplica).
func TestDurable_DefaultReplicasRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.wal")
	ctx := context.Background()

	s, err := Open(path) // defaults: 3 réplicas, quórum 2
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 1; i <= 4; i++ {
		appendEv(t, s, "run-A", stepName(i), "t", `{}`)
	}
	orig, _ := s.Read(ctx, "run-A", 1)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	got, err := s2.Read(ctx, "run-A", 1)
	if err != nil {
		t.Fatalf("read pós-restart: %v", err)
	}
	if !eventsEqual(orig, got) {
		t.Fatalf("cluster replicado divergiu após restart: %d vs %d", len(orig), len(got))
	}
	// Todas as réplicas ficam consistentes: o líder serve o log completo.
	if h, _ := s2.StreamHead(ctx, "run-A"); h != 4 {
		t.Fatalf("head = %d, quero 4", h)
	}
}

func stepName(i int) string { return "s" + string(rune('0'+i)) }

// TestDurable_PersistErrorNoPhantom — REGRESSÃO do defeito write-behind (achado ALTO).
// Sob um erro de I/O do WAL com o processo VIVO (disco cheio/EIO/quota), o Append tem
// de falhar FAIL-CLOSED sem deixar rasto in-memory — nada de phantom-commit. Prova a
// ordem write-ahead: como o persist ocorre ANTES de aplicar às réplicas/elevar o head/
// registar o dedup, um persist falhado não altera head/Read/dedup. Sob a antiga ordem
// apply-before-log este teste falharia (head passaria a 2, Read veria o evento e um
// retry devolveria StatusDuplicate para trabalho que desaparece no restart).
func TestDurable_PersistErrorNoPhantom(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.wal")
	ctx := context.Background()

	s := openDurable(t, path)
	appendEv(t, s, "run-A", "s1", "t", `{"n":1}`)

	// Simula um erro de I/O persistente: fecha o handle subjacente do ficheiro mas deixa
	// o wal "aberto" (w.closed continua false), pelo que o próximo append falha no
	// Flush/Sync — exactamente o caso "erro de escrita com o processo vivo".
	if err := s.wal.f.Close(); err != nil {
		t.Fatalf("fechar handle subjacente: %v", err)
	}

	// Append cujo persist FALHA: deve devolver erro e NÃO materializar o evento.
	_, err := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":2}`), RunID: "run-A", StepID: "s2", Producer: Producer{NHIID: "nhi:test"}})
	if err == nil {
		t.Fatalf("Append devia falhar com erro de persist")
	}
	// Sem phantom-commit: o head continua 1 e o Read não vê o evento falhado.
	if h, _ := s.StreamHead(ctx, "run-A"); h != 1 {
		t.Fatalf("phantom-commit: head=%d, quero 1 (evento falhado não deve aparecer)", h)
	}
	got, _ := s.Read(ctx, "run-A", 1)
	if len(got) != 1 {
		t.Fatalf("phantom-commit: Read devolve %d eventos, quero 1", len(got))
	}
	// Sem trabalho 'acked' fantasma: um retry da MESMA idempotency_key NÃO devolve
	// StatusDuplicate — o dedup nunca registou o evento falhado, logo o store tenta
	// committar de novo (e volta a falhar o persist) em vez de fingir um duplicado.
	dup, derr := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":2}`), RunID: "run-A", StepID: "s2", Producer: Producer{NHIID: "nhi:test"}})
	if derr == nil && dup.Status == StatusDuplicate {
		t.Fatalf("dedup fantasma: retry devolveu StatusDuplicate para evento nunca committed")
	}
}

// TestDurable_ConcurrentAppendRestart — (d) CONCORRÊNCIA + DURABILIDADE: N goroutines
// apendam em paralelo (streams distintos, cada um com muitos eventos); depois fecha,
// reabre e verifica que Read/heads são fiéis, sem perda nem duplicação. Sob
// `go test -race` cobre também a segurança de memória do caminho durável concorrente
// (o wal.mu serializa o ficheiro único; os stripes serializam por-stream) — a
// propriedade fica com regressão automatizada, não apenas verificada manualmente.
func TestDurable_ConcurrentAppendRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.wal")
	ctx := context.Background()

	s := openDurable(t, path)

	const nStreams = 8
	const perStream = 25
	var wg sync.WaitGroup
	for g := 0; g < nStreams; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			stream := "run-" + strconv.Itoa(g)
			for i := 0; i < perStream; i++ {
				_, err := s.Append(ctx, stream, EventInput{
					Type:     "t",
					Payload:  []byte(`{"i":` + strconv.Itoa(i) + `}`),
					RunID:    stream,
					StepID:   "s" + strconv.Itoa(i),
					Producer: Producer{NHIID: "nhi:" + stream},
				})
				if err != nil {
					t.Errorf("append %s/%d: %v", stream, i, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	// Snapshot pré-restart de cada stream (gapless, perStream eventos).
	pre := make(map[string][]Event, nStreams)
	for g := 0; g < nStreams; g++ {
		stream := "run-" + strconv.Itoa(g)
		evs, err := s.Read(ctx, stream, 1)
		if err != nil {
			t.Fatalf("read pré %s: %v", stream, err)
		}
		if len(evs) != perStream {
			t.Fatalf("pré %s: %d eventos, quero %d", stream, len(evs), perStream)
		}
		pre[stream] = evs
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reabre: fidelidade completa (reflect.DeepEqual) + heads intactos, sem duplicação.
	s2 := openDurable(t, path)
	defer s2.Close()
	for g := 0; g < nStreams; g++ {
		stream := "run-" + strconv.Itoa(g)
		got, err := s2.Read(ctx, stream, 1)
		if err != nil {
			t.Fatalf("read pós %s: %v", stream, err)
		}
		if !eventsEqual(pre[stream], got) {
			t.Fatalf("stream %s divergiu após restart concorrente", stream)
		}
		if h, _ := s2.StreamHead(ctx, stream); h != perStream {
			t.Fatalf("head %s = %d, quero %d", stream, h, perStream)
		}
	}
}

// eventsEqual compara dois cortes de eventos pela IGUALDADE COMPLETA do envelope
// (reflect.DeepEqual sobre o Event inteiro): EventID/StreamID/Seq/Type/Ts/Payload E
// TAMBÉM Producer (NHIID/DelegationChain/Scope), RunID, StepID, ParentStepID e
// SchemaVersion. Uma regressão que largasse qualquer destes campos no persist ou no
// restore faz a comparação falhar — a fidelidade byte-a-byte anunciada pelo AC é
// realmente exercitada, não apenas a contagem de eventos.
func eventsEqual(a, b []Event) bool {
	return reflect.DeepEqual(a, b)
}
