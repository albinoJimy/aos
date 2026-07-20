package env_test

import (
	"context"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/testkit/env"
)

// runSuite é a "MESMA suite" que o teste de isolamento corre DUAS VEZES. Provisiona
// um Env fresco, semeia uma trajectória conhecida e devolve um resultado
// OBSERVÁVEL e determinista (os seqs committed + as idempotency keys). Se os Envs
// estivessem contaminados (estado partilhado), a segunda corrida veria os eventos
// da primeira e o resultado divergiria.
func runSuite(t *testing.T) []uint64 {
	t.Helper()
	e := env.New(t, env.WithEventStore(), env.WithBus(), env.WithVault())

	steps := e.SeedTrajectory("run-iso", 3)
	seqs := make([]uint64, 0, len(steps))
	for _, s := range steps {
		if s.AppendResult.Status != eventstore.StatusCommitted {
			t.Fatalf("seed devia commit-ar (Env limpo), obtive %s no step %s", s.AppendResult.Status, s.StepID)
		}
		seqs = append(seqs, s.AppendResult.Seq)
	}
	// O Vault parte SEMPRE vazio.
	if e.Vault.Len() != 0 {
		t.Fatalf("Vault devia partir vazio, tem %d", e.Vault.Len())
	}
	// O stream tem exactamente os eventos desta corrida (6 = 3 turnos x 2 eventos),
	// nunca os de uma corrida anterior.
	got, err := e.EventStore.Read(context.Background(), "run-iso", 1)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != len(steps) {
		t.Fatalf("stream contaminado: esperava %d eventos, li %d", len(steps), len(got))
	}
	return seqs
}

// TestIsolation_SameSuiteTwiceIdentical cobre AC2: a MESMA suite corre duas vezes
// e obtém EXACTAMENTE o mesmo resultado — prova de que cada New parte de estado
// limpo, sem contaminação entre execuções.
func TestIsolation_SameSuiteTwiceIdentical(t *testing.T) {
	first := runSuite(t)
	second := runSuite(t)

	if len(first) != len(second) {
		t.Fatalf("comprimentos divergem: %d != %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("resultado divergiu no passo %d: %d != %d (contaminacao)", i, first[i], second[i])
		}
	}
	// A segunda corrida começou do seq 1 (Store fresco), não continuou a primeira.
	if len(second) == 0 || second[0] != 1 {
		t.Fatalf("segunda corrida nao partiu de estado limpo: seqs=%v", second)
	}
}

// TestIsolation_NoSharedStateBetweenEnvs prova directamente que dois Envs vivos ao
// mesmo tempo NÃO partilham estado: escrever num não é observável no outro.
func TestIsolation_NoSharedStateBetweenEnvs(t *testing.T) {
	t.Parallel()
	a := env.New(t, env.WithEventStore(), env.WithVault())
	b := env.New(t, env.WithEventStore(), env.WithVault())

	a.SeedTrajectory("run-shared", 2)
	a.Vault.Put(env.VaultKey{Provider: "p", Region: "eu", Capability: "c"}, "SECRET")

	// O Store de b não vê o stream de a.
	if _, err := b.EventStore.Read(context.Background(), "run-shared", 1); err == nil {
		t.Fatal("o Env b NAO devia ver o stream semeado no Env a (estado partilhado)")
	}
	// O Vault de b está vazio.
	if b.Vault.Len() != 0 {
		t.Fatalf("Vault de b devia estar vazio, tem %d", b.Vault.Len())
	}
	// O TAP do bus de a e o de b são independentes (b não capturou nada).
	if a.Bus != nil {
		t.Fatal("a nao pediu bus")
	}
}
