package broker

import (
	"context"
	"encoding/json"
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

// negacaoSelada lê do Event Store o que ficou SELADO — não o que o erro devolveu. A distinção
// importa: o AC é sobre o registo durável, e um teste que só olhasse para o erro em memória
// provaria outra coisa.
//
// negacaoSeladaPayload é a parte do payload de `tool.call.denied` que estes testes leem. Desde o
// AOS-340 a postura viaja em `metadata`, um campo ESTRUTURADO, e não num sufixo do `reason`: o
// teste passa a desserializar em vez de procurar substrings, que é precisamente o que o canal
// novo existe para tornar desnecessário.
type negacaoSeladaPayload struct {
	Reason   string            `json:"reason"`
	Metadata map[string]string `json:"metadata"`
}

func negacaoSelada(t *testing.T, s *stack, runID string) negacaoSeladaPayload {
	t.Helper()
	for _, e := range readStream(t, s.es, runID) {
		if e.Type == referencemonitor.EventTypeDenied {
			var p negacaoSeladaPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatalf("payload da negacao ilegivel: %v", err)
			}
			return p
		}
	}
	t.Fatalf("nenhuma negacao selada no run %q", runID)
	return negacaoSeladaPayload{}
}

// posturaSelada devolve o valor de uma chave de postura, ou falha nomeando o que veio.
func posturaSelada(t *testing.T, s *stack, runID, chave string) string {
	t.Helper()
	p := negacaoSelada(t, s, runID)
	v, ok := p.Metadata[chave]
	if !ok {
		t.Fatalf("a negacao selada nao traz a chave %q em metadata: %+v", chave, p.Metadata)
	}
	return v
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
	selada := negacaoSelada(t, s, "run-332")
	for chave, quer := range map[string]string{
		metaProviderPolicy:  string(ProviderPostureEnforced),
		metaResourceBinding: string(ResourceBindingEnforced),
	} {
		if got := selada.Metadata[chave]; got != quer {
			t.Errorf("metadata[%q] = %q, esperado %q (payload: %+v)", chave, got, quer, selada)
		}
	}
	// A razao ORIGINAL sobrevive INTACTA — e agora sozinha: desde o AOS-340 a postura viaja
	// em `metadata`, pelo que o `reason` volta a ser so a razao. Quem asserta pela razao
	// continua a funcionar; quem quer a postura deixa de a extrair de uma string.
	if !strings.Contains(selada.Reason, ErrProviderOutOfScope.Error()) {
		t.Errorf("a razao original perdeu-se: %q", selada.Reason)
	}
	if strings.Contains(selada.Reason, "provider_policy=") {
		t.Errorf("o sufixo em texto livre sobreviveu a migracao do AOS-340: %q", selada.Reason)
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
	com := posturaSelada(t, comPolitica, "run-com", metaProviderPolicy)
	sem := posturaSelada(t, semPolitica, "run-sem", metaProviderPolicy)

	if com == sem {
		t.Fatal("as duas posturas selam o MESMO valor — registar a postura nao distingue nada")
	}
	if com != string(ProviderPostureEnforced) || sem != string(ProviderPostureUnset) {
		t.Errorf("as posturas nao foram seladas correctamente: com=%q sem=%q", com, sem)
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
	if got := posturaSelada(t, s, "run-vazio", metaProviderPolicy); got != string(ProviderPostureEnforced) {
		t.Errorf("um mapa vazio NAO-nil e uma DECLARACAO e tem de selar enforced, veio %q", got)
	}
}
