package broker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-343 — A POSTURA SELADA ERA PARCIAL, NOS DOIS EVENTOS DO BROKER.
//
// O AOS-342 fixou a regra: «a forma viaja com a postura em todo o lado onde a postura
// viaja». Dois sítios ficaram por fora, e são os dois eventos que o próprio broker sela:
//
//   - `credential.exchange.denied` levava `provider_policy` e mais nada — nem a FORMA
//     (AOS-342), nem o eixo RECURSO↔PROVEDOR (AOS-331). Um terço da postura;
//   - `credential.exchange.issued` levava postura e forma, mas não o eixo do recurso.
//
// Porque isso importa e não é arrumação: `provider_policy=enforced` sozinho é
// AMBÍGUO — o AOS-342 mediu que uma política com curinga diz `enforced` e não impõe
// nada. Quem lê uma negação no WORM sem a forma não sabe qual dos dois regimes estava
// em vigor. E sem `resource_binding` não sabe se o destino da credencial era imposto.
//
// Estes campos são METADADO DE CONFIGURAÇÃO, não a causa da decisão: viajam ao lado do
// campo `axis`, que é quem diz o que decidiu. É o contraste com a `Reason` do gate, onde
// a postura vai dentro da frase que explica a recusa e por isso só acompanha o eixo que
// realmente decidiu.

// stackAOS343 monta a pilha com os dois eixos declarados NO BROKER e no gate.
//
// Não reutiliza o `stackComPosturas` do AOS-332 de propósito: esse declara a allowlist
// de hosts só no GATE, pelo que o broker devolveria `ResourceBindingUnset`. Como os
// campos novos derivam do BROKER — é a postura dele que o evento dele sela —, um teste
// montado assim assertaria sobre um estado que a pilha não tem.
func stackAOS343(t *testing.T, comGate bool, classProviders, providerHosts map[string][]string) *stack {
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

	opts := []referencemonitor.Option{referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(es))}
	if comGate {
		gate := NewScopeGate(DefaultExchangeToolID, scopes,
			WithGateClassProviders(classProviders), WithGateProviderHosts(providerHosts))
		opts = append(opts, referencemonitor.WithHooks(append(referencemonitor.DefaultHooks(), gate)...))
	}
	rm := referencemonitor.New(opts...)

	b, err := New(rm, vlt, es, WithClock(clock.now), WithTTL(time.Minute),
		WithClassScopes(scopes), WithClassProviders(classProviders), WithProviderHosts(providerHosts))
	if err != nil {
		t.Fatalf("broker.New: %v", err)
	}
	return &stack{broker: b, rm: rm, es: es, vault: vlt, guest: NewMemoryGuest(), clock: clock}
}

// posturaNoEvento é o subconjunto de postura que os dois eventos do broker têm de selar.
type posturaNoEvento struct {
	ProviderPolicy      string `json:"provider_policy"`
	ProviderPolicyShape string `json:"provider_policy_shape"`
	ResourceBinding     string `json:"resource_binding"`
}

// posturaDoEvento extrai a postura do único evento do tipo dado num stream.
func posturaDoEvento(t *testing.T, s *stack, runID, tipo string) posturaNoEvento {
	t.Helper()
	var achados []posturaNoEvento
	for _, e := range readStream(t, s.es, runID) {
		if e.Type != tipo {
			continue
		}
		var p posturaNoEvento
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("payload de %q ilegivel: %v", tipo, err)
		}
		achados = append(achados, p)
	}
	if len(achados) != 1 {
		t.Fatalf("eventos %q = %d, esperado exactamente 1", tipo, len(achados))
	}
	return achados[0]
}

// TestAOS343_OsDoisEventosSelamAPosturaCOMPLETA: sob um regime em que os três valores
// são DISTINGUÍVEIS do valor-zero, ambos os eventos têm de os trazer.
//
// A política é de propósito um CURINGA: é o caso em que `provider_policy=enforced`
// sozinho mente, e portanto aquele em que a forma tem de estar presente para o registo
// ser legível. Uma política de conjuntos concretos tornaria o teste mais fraco — passaria
// mesmo que a forma nunca distinguisse nada.
func TestAOS343_OsDoisEventosSelamAPosturaCOMPLETA(t *testing.T) {
	t.Parallel()
	s := stackAOS343(t, true,
		map[string][]string{agentClass: {ProviderAny}},    // provider: enforced + curinga
		map[string][]string{provider: {"api.stripe.com"}}) // recurso: enforced

	quer := posturaNoEvento{
		ProviderPolicy:      string(ProviderPostureEnforced),
		ProviderPolicyShape: string(ProviderPolicyShapeWildcard),
		ResourceBinding:     string(ResourceBindingEnforced),
	}

	// (1) TROCA EMITIDA. O curinga deixa passar, e o recurso está na allowlist.
	req := requestForProvider("run-ok", provider, provInScopeCap)
	req.Downstream.ResourceValue = "https://api.stripe.com/charges"
	if _, err := s.broker.Exchange(context.Background(), req); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if got := posturaDoEvento(t, s, "run-ok", exchangeEventType); got != quer {
		t.Errorf("troca emitida: postura selada = %+v, esperado %+v", got, quer)
	}

	// (2) NEGAÇÃO SERVER-SIDE. Sem o gate na cadeia, é a guarda de composição do
	// dispatch que nega — e é esse o evento que levava um terço da postura.
	//
	// OS DOIS EIXOS TÊM POSTURAS DIFERENTES aqui de propósito — provider `enforced`,
	// recurso `unset`. Com os dois iguais, um mutante que TROCASSE os campos passava
	// invisível, e a prova de mutação apanhou exactamente isso na primeira versão
	// deste teste: `ResourceBinding: string(b.ProviderPosture())` sobrevivia.
	sd := stackAOS343(t, false,
		map[string][]string{agentClass: {provider}},
		nil)
	if _, err := sd.broker.Exchange(context.Background(), requestForProvider("run-neg", providerOther, provInScopeCap)); err == nil {
		t.Fatal("a troca tinha de ser negada pela guarda de composicao")
	}
	querNeg := posturaNoEvento{
		ProviderPolicy:      string(ProviderPostureEnforced),
		ProviderPolicyShape: string(ProviderPolicyShapeByClass),
		ResourceBinding:     string(ResourceBindingUnset),
	}
	if got := posturaDoEvento(t, sd, "run-neg", exchangeDeniedEventType); got != querNeg {
		t.Errorf("negacao server-side: postura selada = %+v, esperado %+v", got, querNeg)
	}
}

// TestAOS343_APosturaPorOMISSAOTambemESelada prova que os campos novos não são
// «preenchidos quando há política»: o estado por omissão é uma DECLARAÇÃO e tem de
// aparecer, senão a sua ausência volta a ser indistinguível de um campo esquecido — que
// é o argumento inteiro do AOS-324.
func TestAOS343_APosturaPorOMISSAOTambemESelada(t *testing.T) {
	t.Parallel()
	sd := stackAOS343(t, false, nil, nil)

	// Sem política de provedores, a guarda de composição nega o provedor em branco.
	if _, err := sd.broker.Exchange(context.Background(), requestForProvider("run-unset", "", provInScopeCap)); err == nil {
		t.Fatal("um provedor em branco tinha de ser negado nas duas posturas")
	}
	quer := posturaNoEvento{
		ProviderPolicy:      string(ProviderPostureUnset),
		ProviderPolicyShape: string(ProviderPolicyShapeNone),
		ResourceBinding:     string(ResourceBindingUnset),
	}
	if got := posturaDoEvento(t, sd, "run-unset", exchangeDeniedEventType); got != quer {
		t.Errorf("postura por omissao: selada = %+v, esperado %+v", got, quer)
	}
}
