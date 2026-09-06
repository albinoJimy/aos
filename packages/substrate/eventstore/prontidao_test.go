package eventstore

// AOS-350 — `Healthy()` NÃO CONHECIA O SUBSTRATO.
//
// `store.go` era `func (s *Store) Healthy() bool { return !s.closed.Load() }` — `closed`
// era o único input. Medido:
//
//	Healthy() com o WAL morto pelo Flush pegajoso                = true
//	Healthy() com wal.envenenado=true e o append a dizer
//	            "nao aceita mais escritas"                       = true
//
// Os consumidores são três e são reais: `/readyz`, o gauge `aos_eventstore_healthy` e o
// SLI `controlPlaneAvailable`. Um nó com o Event Store morto recusava todas as escritas, o
// orquestrador de contentores continuava a encaminhar tráfego, e
// `control_plane_availability_low` não disparava.
//
// Este ticket é o AMPLIFICADOR de AOS-348: sozinho não avaria nada; o que faz é garantir
// que um substrato morto atravessa a prontidão, o gauge e o SLI sem acender nada.

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestAOS350_HealthyFicaFalsoComOWALARecusarEscritas é o teste que nasceu VERDE ao
// contrário: antes da correcção `Healthy()` continuava `true` com o store a recusar tudo.
func TestAOS350_HealthyFicaFalsoComOWALARecusarEscritas(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "events.wal")

	// Truncatura de reposição também a falhar ⇒ o WAL envenena no primeiro fsync falhado.
	s, _ := abreComFsyncFalhado(t, path, 1, true)
	defer s.Close()
	appendEv(t, s, "run-A", "s1", "t", `{"n":1}`)

	if !s.Healthy() {
		t.Fatal("Healthy() já era falso antes de o substrato falhar — o sensor mede outra coisa")
	}

	_, err := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":2}`), RunID: "run-A", StepID: "s2"})
	if err == nil {
		t.Fatal("esperava erro")
	}

	// O substrato está morto: o append seguinte é recusado em voz alta.
	if _, err := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":3}`), RunID: "run-A", StepID: "s3"}); err == nil {
		t.Fatal("o WAL envenenado aceitou uma escrita")
	}
	// …e a PRONTIDÃO tem de o dizer. Era aqui que `/readyz` ficava 200 verde.
	if s.Healthy() {
		t.Fatal("Healthy() == true com o Event Store a RECUSAR todas as escritas — " +
			"o orquestrador continua a encaminhar tráfego e o SLI não dispara (AOS-350)")
	}
}

// TestAOS350_HealthySobreviveAUmaFalhaTRANSITORIA guarda a metade que a correcção não
// pode estropiar. Uma falha de I/O da qual o WAL RECUPERA (AOS-348) não é motivo para
// drenar o nó: a prontidão só cai quando o substrato deixa mesmo de aceitar escritas.
func TestAOS350_HealthySobreviveAUmaFalhaTRANSITORIA(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "events.wal")

	s, ff := abreComFsyncFalhado(t, path, 1<<30, false)
	defer s.Close()
	appendEv(t, s, "run-A", "s1", "t", `{"n":1}`)

	ff.falharApós = 0
	ff.syncs = 0
	if _, err := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":2}`), RunID: "run-A", StepID: "s2"}); err == nil {
		t.Fatal("esperava erro do fsync")
	}
	ff.falharApós = 1 << 30 // a avaria passou

	if !s.Healthy() {
		t.Fatal("Healthy() == false depois de uma falha TRANSITÓRIA da qual o WAL recupera — " +
			"drenar o nó por isto seria trocar um erro por uma indisponibilidade")
	}
	if _, err := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":3}`), RunID: "run-A", StepID: "s3"}); err != nil {
		t.Fatalf("o WAL não recuperou: %v", err)
	}
}

// TestAOS350_StoreInMemoryContinuaSaudavel fixa a retro-compatibilidade: um store sem WAL
// não tem substrato que possa recusar, e a prontidão continua a ser só o `closed`.
func TestAOS350_StoreInMemoryContinuaSaudavel(t *testing.T) {
	s, err := New(WithReplicas(1), WithQuorum(1))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !s.Healthy() {
		t.Fatal("um store in-memory recém-criado tem de estar saudável")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if s.Healthy() {
		t.Fatal("Healthy() == true depois de Close")
	}
}

// TestAOS350_HealthyNaoTomaOMutexDoWAL prova a propriedade que o comentário promete: a
// sonda não pode ficar refém do `fsync`. Com o mutex do WAL DETIDO por outra goroutine,
// `Healthy()` tem de responder na mesma.
func TestAOS350_HealthyNaoTomaOMutexDoWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.wal")
	s := openDurable(t, path)
	defer s.Close()
	appendEv(t, s, "run-A", "s1", "t", `{"n":1}`)

	s.wal.mu.Lock()
	defer s.wal.mu.Unlock()
	pronto := make(chan bool, 1)
	go func() { pronto <- s.Healthy() }()
	// Prazo PRÓPRIO e curto. A versão anterior usava `t.Context().Done()`, que só expira
	// com o teste inteiro: se `Healthy()` bloqueasse, isto PENDURAVA até ao timeout global
	// do `go test` em vez de falhar — um sensor que se despenha em vez de acusar não é um
	// sensor. Apontado pela revisão adversarial.
	select {
	case ok := <-pronto:
		if !ok {
			t.Fatal("Healthy() = false com o WAL apenas OCUPADO")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Healthy() BLOQUEOU no mutex do WAL — o /readyz ficaria refém da latência do fsync")
	}
}
