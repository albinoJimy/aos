package main

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-246 (F2) — provas FALSIFICÁVEIS de que ligar uma velocidade de queima sem fonte
// cablada ABORTA O ARRANQUE em vez de desligar o disjuntor inteiro em silêncio.
//
// Antes desta remediação, o gate estava no [runBreakers.resolve]: a construção falhava com
// breaker.ErrVelocitySourceMissing, o erro era engolido com `return nil` e o run corria SEM
// disjuntor nenhum — sem no-progress e sem wall-clock. O nó arrancava e o banner anunciava
// protecção. Estes testes distinguem as duas posturas: com a env ligada, o arranque tem de
// falhar; sem ela, o disjuntor tem de compor-se e resolver um breaker REAL.

// aos246LimparEnvsBreaker neutraliza as quatro envs do disjuntor para que cada caso parta
// dos defaults e não do ambiente de quem corre os testes.
func aos246LimparEnvsBreaker(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"AOS_BREAKER_MAX_STALE_ITERATIONS", "AOS_BREAKER_MAX_WALL_CLOCK",
		"AOS_BREAKER_MAX_COST_MICRO_USD_PER_SEC", "AOS_BREAKER_MAX_TOKENS_PER_SEC",
	} {
		t.Setenv(k, "")
	}
}

// TestAOS246_BootComVelocidadeSemFonteAborta: o nó REAL (Bootstrap) recusa arrancar quando
// qualquer um dos tectos de velocidade está ligado. É a asserção central do ticket — se o
// arranque passasse, o run correria sem disjuntor.
func TestAOS246_BootComVelocidadeSemFonteAborta(t *testing.T) {
	casos := map[string]string{
		"AOS_BREAKER_MAX_COST_MICRO_USD_PER_SEC": "250.5",
		"AOS_BREAKER_MAX_TOKENS_PER_SEC":         "1000",
	}
	for env, val := range casos {
		t.Run(env, func(t *testing.T) {
			aos246LimparEnvsBreaker(t)
			t.Setenv(env, val)

			node, err := Bootstrap(context.Background(), tnBaseConfig(), io.Discard)
			if node != nil {
				t.Cleanup(func() { _ = node.Close() })
			}
			if !errors.Is(err, ErrBreakerVelocitySourceUnwired) {
				t.Fatalf("%s=%s devia abortar o arranque com ErrBreakerVelocitySourceUnwired; err=%v, node=%v", env, val, err, node != nil)
			}
		})
	}
}

// TestAOS246_BootSemVelocidadeCompoeDisjuntor: o caminho normal continua intacto — sem as
// envs de velocidade o nó arranca e o registo por-run é composto. Esta é a metade que
// impede a remediação de degenerar em "recusar sempre".
func TestAOS246_BootSemVelocidadeCompoeDisjuntor(t *testing.T) {
	aos246LimparEnvsBreaker(t)

	node, err := Bootstrap(context.Background(), tnBaseConfig(), io.Discard)
	if err != nil {
		t.Fatalf("sem as envs de velocidade o nó tem de arrancar: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
}

// TestAOS246_RegistoComposeBreakerReal: com os defaults (no-progress + wall-clock), o
// registo compõe-se sem erro e o [runBreakers.resolve] devolve um breaker REAL sobre a
// máquina de estados do run. Prova que o gate de arranque não passou a estrangular o
// caminho legítimo — e que o disjuntor que o operador julga ter existe mesmo.
func TestAOS246_RegistoComposeBreakerReal(t *testing.T) {
	aos246LimparEnvsBreaker(t)

	prov, err := breakerThresholdsFromEnv()
	if err != nil {
		t.Fatalf("breakerThresholdsFromEnv: %v", err)
	}
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	const runID = "run-aos246"
	gates := newRunStateGates(es, nil, 0)
	if err := gates.Open(context.Background(), runID, state.Uint64Token(1)); err != nil {
		t.Fatalf("Open: %v", err)
	}

	breakers, err := newRunBreakers(gates, prov)
	if err != nil {
		t.Fatalf("com os defaults o registo tem de compor-se: %v", err)
	}
	if breakers == nil {
		t.Fatal("com os defaults o disjuntor devia ser composto")
	}
	if br := breakers.resolve(runID); br == nil {
		t.Fatal("resolve devia devolver um breaker real para um run com gate aberto")
	}
	if breakers.livenessAdapter() == nil {
		t.Fatal("o adaptador de liveness devia estar composto")
	}
}

// TestAOS246_RegistoRecusaVelocidadeSemFonte: a mesma recusa ao nível do construtor, com o
// erro NOMEADO. Isola o gate do resto do arranque — se alguém voltar a mover a decisão para
// o resolve() (onde não há por onde devolver erro), este teste cai.
func TestAOS246_RegistoRecusaVelocidadeSemFonte(t *testing.T) {
	aos246LimparEnvsBreaker(t)
	t.Setenv("AOS_BREAKER_MAX_COST_MICRO_USD_PER_SEC", "1")

	prov, err := breakerThresholdsFromEnv()
	if err != nil {
		t.Fatalf("breakerThresholdsFromEnv: %v", err)
	}
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	breakers, err := newRunBreakers(newRunStateGates(es, nil, 0), prov)
	if !errors.Is(err, ErrBreakerVelocitySourceUnwired) {
		t.Fatalf("devia recusar com ErrBreakerVelocitySourceUnwired; err=%v", err)
	}
	if breakers != nil {
		t.Fatal("na recusa não se devolve registo (fail-closed, não meio-ligado)")
	}
}
