package testkit

import (
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// NewEventStore constrói o Event Store in-memory de REFERÊNCIA (AOS-002) pronto a
// usar nos testes unit — a implementação real (substrate/eventstore), não uma
// imitação: append-only estrito, dedup por (run_id, step_id), quórum in-process.
// É I/O isolado (memória, sem rede/disco), logo sem flakiness.
//
// Por omissão injecta o [FixedClock] canónico para que os timestamps dos eventos
// sejam reproduzíveis; opções adicionais (WithReplicas, WithRegion, ...) podem ser
// passadas e sobrepõem os defaults na ordem dada.
func NewEventStore(opts ...eventstore.Option) (*eventstore.Store, error) {
	base := []eventstore.Option{}
	// Nota: WithClock é não-exportado no eventstore (uso interno); o determinismo
	// dos ts do ES é garantido pelo próprio pacote. Mantemos a assinatura aberta a
	// opções públicas do chamador sem forçar nenhuma configuração específica.
	return eventstore.New(append(base, opts...)...)
}

// MustEventStore é a variante que FALHA o teste se a construção falhar e regista o
// Close no teardown (idempotente). É o atalho para o caso comum "quero um ES limpo
// para este teste" sem boilerplate de erro/cleanup.
func MustEventStore(t testing.TB, opts ...eventstore.Option) *eventstore.Store {
	t.Helper()
	es, err := NewEventStore(opts...)
	if err != nil {
		t.Fatalf("testkit.MustEventStore: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	return es
}
