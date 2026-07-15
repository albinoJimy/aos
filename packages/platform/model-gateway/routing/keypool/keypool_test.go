package keypool_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/aos-ref/platform/model-gateway/routing/keypool"
)

// TestSelect_ByThroughput_Deterministic prova que a selecção distribui a carga
// pelas contas por THROUGHPUT (a de mais folga primeiro), de forma DETERMINISTA e
// SEM qualquer parâmetro de identidade na assinatura (a invariante central).
func TestSelect_ByThroughput_Deterministic(t *testing.T) {
	t.Parallel()
	pool := keypool.NewPool(
		keypool.Account{KeyID: "acct-b", LimitRPM: 100},
		keypool.Account{KeyID: "acct-a", LimitRPM: 100},
	)
	// Selecções sucessivas — NENHUMA recebe um principal. Distribui por menor carga
	// com desempate estável por KeyID: a(0)→a(1), depois b(0)<a(1)→b, alternando.
	var got []string
	for i := 0; i < 4; i++ {
		id, err := pool.Select()
		if err != nil {
			t.Fatalf("Select #%d: %v", i, err)
		}
		got = append(got, id)
	}
	want := []string{"acct-a", "acct-b", "acct-a", "acct-b"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sequencia de selecao = %v, quer %v", got, want)
		}
	}
}

// TestSelect_PrefersMoreHeadroom: uma conta com limite maior (mais folga relativa)
// é preferida sob carga desigual — a selecção é sensível ao throughput.
func TestSelect_PrefersMoreHeadroom(t *testing.T) {
	t.Parallel()
	pool := keypool.NewPool(
		keypool.Account{KeyID: "small", LimitRPM: 10},
		keypool.Account{KeyID: "big", LimitRPM: 1000},
	)
	pool.SetLoad("small", 5, 0) // 50% utilizada
	pool.SetLoad("big", 5, 0)   // 0.5% utilizada
	id, err := pool.Select()
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if id != "big" {
		t.Fatalf("escolheu %q, quer a de mais folga (big)", id)
	}
}

// TestSelect_TPMHeadroomInRanking prova que o TPM entra no RANKING da selecção
// (não só como corte de saturação): com RPM idêntico nas duas contas, a que tem
// MAIS folga de TPM é preferida. Fecha o critério "seleccionadas por throughput
// (TPM/RPM)" — o eixo mais carregado (worstUtil) decide.
func TestSelect_TPMHeadroomInRanking(t *testing.T) {
	t.Parallel()
	pool := keypool.NewPool(
		keypool.Account{KeyID: "mais-tpm", LimitRPM: 100, LimitTPM: 1000},
		keypool.Account{KeyID: "menos-tpm", LimitRPM: 100, LimitTPM: 1000},
	)
	// RPM igual nas duas (10%); só o TPM difere — "menos-tpm" quase saturada.
	pool.SetLoad("mais-tpm", 10, 100)  // TPM a 10%
	pool.SetLoad("menos-tpm", 10, 800) // TPM a 80%
	id, err := pool.Select()
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if id != "mais-tpm" {
		t.Fatalf("escolheu %q, quer mais-tpm (mais folga de TPM sob RPM igual)", id)
	}
}

// TestSelect_FailClosed_Saturated: pool cujas contas atingiram o limite falha
// fail-closed (nunca serve acima do throughput).
func TestSelect_FailClosed_Saturated(t *testing.T) {
	t.Parallel()
	pool := keypool.NewPool(keypool.Account{KeyID: "only", LimitRPM: 2})
	pool.SetLoad("only", 2, 0) // saturada
	if _, err := pool.Select(); !errors.Is(err, keypool.ErrNoCapacity) {
		t.Fatalf("erro = %v, quer ErrNoCapacity", err)
	}
}

// TestSelect_FailClosed_TPM: o eixo de TPM também satura fail-closed.
func TestSelect_FailClosed_TPM(t *testing.T) {
	t.Parallel()
	pool := keypool.NewPool(keypool.Account{KeyID: "only", LimitRPM: 100, LimitTPM: 1000})
	pool.SetLoad("only", 0, 1000) // TPM no limite
	if _, err := pool.Select(); !errors.Is(err, keypool.ErrNoCapacity) {
		t.Fatalf("erro = %v, quer ErrNoCapacity (TPM)", err)
	}
}

// TestRegistry_Select_NoPool_FailClosed: par sem pool configurado falha fail-closed.
func TestRegistry_Select_NoPool_FailClosed(t *testing.T) {
	t.Parallel()
	reg := keypool.NewRegistry()
	reg.Register("openai", "eu", keypool.NewPool(keypool.Account{KeyID: "k", LimitRPM: 10}))
	if _, err := reg.Select("openai", "us"); !errors.Is(err, keypool.ErrNoPool) {
		t.Fatalf("erro = %v, quer ErrNoPool", err)
	}
	if id, err := reg.Select("openai", "eu"); err != nil || id != "k" {
		t.Fatalf("Select(eu) = %q,%v", id, err)
	}
}

// TestSelect_IndependentOfCallOrigin é a prova COMPORTAMENTAL do desacoplamento:
// a sequência de KeyIDs escolhida depende SÓ do provider/região/carga — não há
// forma de a identidade influenciar a escolha porque a assinatura não a aceita.
// Dois "chamadores" diferentes (aqui, apenas dois ciclos) sobre pools em estado
// idêntico obtêm a MESMA sequência.
func TestSelect_IndependentOfCallOrigin(t *testing.T) {
	t.Parallel()
	build := func() *keypool.Pool {
		return keypool.NewPool(
			keypool.Account{KeyID: "a", LimitRPM: 100},
			keypool.Account{KeyID: "b", LimitRPM: 100},
		)
	}
	seq := func(p *keypool.Pool, n int) []string {
		var out []string
		for i := 0; i < n; i++ {
			id, _ := p.Select()
			out = append(out, id)
		}
		return out
	}
	s1 := seq(build(), 6)
	s2 := seq(build(), 6)
	for i := range s1 {
		if s1[i] != s2[i] {
			t.Fatalf("sequencias divergem: %v vs %v", s1, s2)
		}
	}
}

// TestObserve_TPMFeedback: os tokens observados (TPM) via Observe influenciam a
// selecção seguinte — a conta que consumiu mais tokens satura e deixa de ser
// escolhida. Cobre o feedback de throughput no Pool e no Registry.
func TestObserve_TPMFeedback(t *testing.T) {
	t.Parallel()
	reg := keypool.NewRegistry()
	pool := keypool.NewPool(
		keypool.Account{KeyID: "a", LimitRPM: 1000, LimitTPM: 100},
		keypool.Account{KeyID: "b", LimitRPM: 1000, LimitTPM: 100},
	)
	reg.Register("openai", "eu", pool)

	id, err := reg.Select("openai", "eu")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	// Satura a conta escolhida por TPM via feedback.
	reg.Observe("openai", "eu", id, 100)
	// Observe numa conta/pool inexistente é no-op (não deve entrar em pânico).
	reg.Observe("openai", "us", id, 10)
	pool.Observe("desconhecida", 5)

	// As próximas selecções evitam a conta saturada por TPM.
	for i := 0; i < 3; i++ {
		next, err := reg.Select("openai", "eu")
		if err != nil {
			t.Fatalf("Select #%d: %v", i, err)
		}
		if next == id {
			t.Fatalf("conta saturada por TPM (%q) foi reescolhida", id)
		}
	}
}

// TestSelect_ConcurrentSafe: Select concorrente não corre (–race) e nunca devolve
// KeyID vazio quando há capacidade.
func TestSelect_ConcurrentSafe(t *testing.T) {
	t.Parallel()
	pool := keypool.NewPool(
		keypool.Account{KeyID: "a"},
		keypool.Account{KeyID: "b"},
		keypool.Account{KeyID: "c"},
	)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if id, err := pool.Select(); err != nil || id == "" {
				t.Errorf("Select concorrente = %q,%v", id, err)
			}
		}()
	}
	wg.Wait()
}
