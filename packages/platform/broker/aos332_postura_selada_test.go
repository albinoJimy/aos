package broker

import (
	"context"
	"strings"
	"testing"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-332 — UMA TROCA NEGADA REGISTA A POSTURA SOB A QUAL FOI DECIDIDA.
//
// Uma negação dizia «provedor fora de escopo» e mais nada. Duas negações com a MESMA razão podiam
// vir de posturas opostas, e no WORM eram indistinguíveis. Quem audita precisa de saber contra
// que regra a decisão correu, não só qual foi.
//
// A postura vai no `Reason` e não num campo novo do `MediationRecord`: o precedente próximo é o
// `PolicyVersion`, mas esse é GENÉRICO — qualquer hook de política o preenche, e o contrato C1 é
// do kernel. Uma postura do broker é preocupação de PLATAFORMA, e enfiá-la no contrato do kernel
// por conveniência de um hook seria a fuga de camada que o `layer-lint` existe para impedir.

// stackComPosturas levanta a pilha com os DOIS eixos configuráveis, para que o teste possa variar
// a postura e observar o que a negação sela.
func stackComPosturas(t *testing.T, classProviders, providerHosts map[string][]string) *stack {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	scopes := defaultClassScopes()
	clock := newTestClock()
	vlt := NewMemoryVault()
	vlt.Put(VaultKey{Provider: provider, Region: region, Capability: provInScopeCap}, sentinel)
	vlt.Put(VaultKey{Provider: providerOther, Region: region, Capability: provInScopeCap}, sentinel)

	gate := NewScopeGate(DefaultExchangeToolID, scopes,
		WithGateClassProviders(classProviders), WithGateProviderHosts(providerHosts))
	rm := referencemonitor.New(
		referencemonitor.WithHooks(append(referencemonitor.DefaultHooks(), gate)...),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(es)),
	)
	b, err := New(rm, vlt, es, WithClock(clock.now), WithTTL(time.Minute),
		WithClassScopes(scopes), WithClassProviders(classProviders))
	if err != nil {
		t.Fatalf("broker.New: %v", err)
	}
	return &stack{broker: b, rm: rm, es: es, vault: vlt, guest: NewMemoryGuest(), clock: clock}
}

// razaoDaNegacaoSelada lê do Event Store a razão que ficou SELADA — não a que o erro devolveu.
// A distinção importa: o AC é sobre o que fica no registo durável, e um teste que só olhasse para
// o erro em memória provaria outra coisa.
func razaoDaNegacaoSelada(t *testing.T, s *stack, runID string) string {
	t.Helper()
	for _, e := range readStream(t, s.es, runID) {
		if e.Type == referencemonitor.EventTypeDenied {
			return string(e.Payload)
		}
	}
	t.Fatalf("nenhuma negacao selada no run %q", runID)
	return ""
}

func TestAOS332_ANegacaoSelaAPostura(t *testing.T) {
	t.Parallel()
	// Postura DECLARADA nos dois eixos: a classe alcança só `provider`, e esse só o seu host.
	s := stackComPosturas(t,
		map[string][]string{agentClass: {provider}},
		map[string][]string{provider: {"api.stripe.com"}})

	// Troca para um provedor fora da autoridade ⇒ negação do eixo provider.
	_, err := s.broker.Exchange(context.Background(), requestForProvider("run-332", providerOther, provInScopeCap))
	if err == nil {
		t.Fatal("a troca tinha de ser negada")
	}
	selada := razaoDaNegacaoSelada(t, s, "run-332")
	for _, quer := range []string{"provider_policy=enforced", "resource_binding=enforced"} {
		if !strings.Contains(selada, quer) {
			t.Errorf("a negacao selada nao regista %q:\n%s", quer, selada)
		}
	}
	// A razão ORIGINAL tem de sobreviver intacta: o sufixo acrescenta, não substitui.
	if !strings.Contains(selada, ErrProviderOutOfScope.Error()) {
		t.Errorf("a razao original perdeu-se:\n%s", selada)
	}
}

// TestAOS332_AsDuasPosturasProduzemLinhasDistintas é o AC escrito à letra, e é o que impede que a
// selagem seja decorativa: se as duas posturas produzissem o mesmo texto, registá-lo não
// distinguiria nada.
func TestAOS332_AsDuasPosturasProduzemLinhasDistintas(t *testing.T) {
	t.Parallel()
	// MESMA negação (provedor indeterminado, que nega nas duas posturas), posturas diferentes.
	comPolitica := stackComPosturas(t, map[string][]string{agentClass: {provider}}, map[string][]string{provider: {"api.stripe.com"}})
	semPolitica := stackComPosturas(t, nil, nil)

	req := func(run string) ExchangeRequest {
		r := requestForProvider(run, " ", provInScopeCap) // eixo em branco: negado nas duas
		return r
	}
	if _, err := comPolitica.broker.Exchange(context.Background(), req("run-com")); err == nil {
		t.Fatal("um eixo em branco tinha de ser negado sob politica declarada")
	}
	if _, err := semPolitica.broker.Exchange(context.Background(), req("run-sem")); err == nil {
		t.Fatal("um eixo em branco tinha de ser negado tambem sem politica")
	}
	com := razaoDaNegacaoSelada(t, comPolitica, "run-com")
	sem := razaoDaNegacaoSelada(t, semPolitica, "run-sem")

	if com == sem {
		t.Fatal("as duas posturas selam a MESMA linha — registar a postura nao distingue nada")
	}
	if !strings.Contains(com, "provider_policy=enforced") || !strings.Contains(sem, "provider_policy=unset") {
		t.Errorf("as posturas nao foram seladas correctamente:\n com: %s\n sem: %s", com, sem)
	}
}

// TestAOS332_APosturaEDoESTADONaoDaIntencao — a postura selada tem de vir do gate CONSTRUÍDO. Um
// mapa vazio não-nil é uma declaração («ninguém alcança nada»), e tem de selar `enforced`, não
// `unset`: é a distinção entre «não declarei» e «declarei que não».
func TestAOS332_APosturaEDoESTADONaoDaIntencao(t *testing.T) {
	t.Parallel()
	s := stackComPosturas(t, map[string][]string{}, map[string][]string{})
	if _, err := s.broker.Exchange(context.Background(), requestForProvider("run-vazio", provider, provInScopeCap)); err == nil {
		t.Fatal("com politica vazia declarada, nenhuma troca passa")
	}
	selada := razaoDaNegacaoSelada(t, s, "run-vazio")
	if !strings.Contains(selada, "provider_policy=enforced") {
		t.Errorf("um mapa vazio NAO-nil e uma DECLARACAO e tem de selar enforced:\n%s", selada)
	}
}
