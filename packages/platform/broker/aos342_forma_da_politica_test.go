package broker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// AOS-342 — `enforced` NÃO SIGNIFICA IMPOSTO.
//
// [Broker.ProviderPosture] é função da NULIDADE do mapa de política: qualquer mapa
// não-nil devolve `enforced`, e o CONTEÚDO nunca é olhado. Para o que fica SELADO essa
// semântica está certa — um mapa vazio é uma declaração («ninguém alcança nada») e tem de
// se distinguir de «não declarei», que é o que o AOS-332 fixou.
//
// Para uma PRÉ-CONDIÇÃO não chega. O `DEF-218` manda assertar que a postura diz
// `enforced` antes de ligar a troca; uma política `{"payments": {"*"}}` diz `enforced` e
// deixa passar QUALQUER provedor. A pré-condição ficaria verde sobre exactamente o
// defeito que o AOS-324 fechou.
//
// A forma passa a viajar com a postura em todo o lado onde a postura viaja: no evento da
// troca emitida, na razão da negação do gate e no banner de arranque (esse, em cmd/aos).

// TestAOS342_CuringaEhEnforcedENaoImpoe é a MEDIÇÃO que sustenta o resto do ticket: sob
// uma política com curinga a postura diz `enforced` e a troca cross-provider PASSA. Sem
// este teste, os estados novos seriam texto sobre um caso que ninguém mediu.
func TestAOS342_CuringaEhEnforcedENaoImpoe(t *testing.T) {
	t.Parallel()
	s := stackComPosturas(t, map[string][]string{agentClass: {ProviderAny}}, nil)

	if got := s.broker.ProviderPosture(); got != ProviderPostureEnforced {
		t.Fatalf("postura = %q, esperado %q", got, ProviderPostureEnforced)
	}
	h, err := s.broker.Exchange(context.Background(), requestForProvider("run-curinga", providerOther, provInScopeCap))
	if err != nil {
		t.Fatalf("o curinga devia deixar passar QUALQUER provedor, obtido %v", err)
	}
	if h == "" {
		t.Fatal("handle vazio")
	}
	// E é a FORMA que torna isso legível sem ter de correr a troca.
	if got := s.broker.ProviderPolicyShape(); got != ProviderPolicyShapeWildcard {
		t.Errorf("forma = %q, esperado %q", got, ProviderPolicyShapeWildcard)
	}
}

// TestAOS342_FormaDaPolitica_ClassificaOQueAPosturaNaoOlha cobre as quatro formas e as
// classes nomeadas. O par (postura, forma) é o que o DEF-218 tem de assertar.
func TestAOS342_FormaDaPolitica_ClassificaOQueAPosturaNaoOlha(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		providers  map[string][]string
		wantPost   ProviderPosture
		wantShape  ProviderPolicyShape
		wantCuring []string
	}{
		{"sem politica", nil, ProviderPostureUnset, ProviderPolicyShapeNone, nil},
		{"declarada e vazia (deny-all)", map[string][]string{}, ProviderPostureEnforced, ProviderPolicyShapeEmpty, nil},
		{
			"curinga numa classe de duas",
			map[string][]string{agentClass: {ProviderAny}, "billing": {provider}},
			ProviderPostureEnforced, ProviderPolicyShapeWildcard, []string{agentClass},
		},
		{
			"conjuntos concretos",
			map[string][]string{agentClass: {provider}},
			ProviderPostureEnforced, ProviderPolicyShapeByClass, nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := stackComPosturas(t, tc.providers, nil)
			if got := s.broker.ProviderPosture(); got != tc.wantPost {
				t.Errorf("ProviderPosture() = %q, esperado %q", got, tc.wantPost)
			}
			if got := s.broker.ProviderPolicyShape(); got != tc.wantShape {
				t.Errorf("ProviderPolicyShape() = %q, esperado %q", got, tc.wantShape)
			}
			got := s.broker.ProviderClassesComCuringa()
			if strings.Join(got, ",") != strings.Join(tc.wantCuring, ",") {
				t.Errorf("classes com curinga = %v, esperado %v", got, tc.wantCuring)
			}
		})
	}
}

// TestAOS342_AsQuatroFormasNaoColapsam é o controlo: se duas formas materialmente
// diferentes produzissem o mesmo valor, selá-lo não distinguiria nada e o DEF-218
// continuaria a assertar sobre um bit que não separa o que precisa de separar.
func TestAOS342_AsQuatroFormasNaoColapsam(t *testing.T) {
	t.Parallel()
	formas := []ProviderPolicyShape{
		providerPolicyShape(nil),
		providerPolicyShape(map[string][]string{}),
		providerPolicyShape(map[string][]string{agentClass: {ProviderAny}}),
		providerPolicyShape(map[string][]string{agentClass: {provider}}),
	}
	vistas := map[ProviderPolicyShape]bool{}
	for _, f := range formas {
		if vistas[f] {
			t.Fatalf("duas politicas materialmente diferentes colapsam na forma %q", f)
		}
		vistas[f] = true
	}
}

// TestAOS342_ATrocaEmitidaSelaAForma: o `DEF-218` assere sobre o evento
// `credential.exchange.issued`. É lá que a forma tem de estar, senão a asserção continua
// a ser feita sobre um `enforced` que não distingue impor de não impor.
func TestAOS342_ATrocaEmitidaSelaAForma(t *testing.T) {
	t.Parallel()
	s := stackComPosturas(t, map[string][]string{agentClass: {ProviderAny}}, nil)

	if _, err := s.broker.Exchange(context.Background(), requestForProvider("run-selo", provider, provInScopeCap)); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	var visto bool
	for _, e := range readStream(t, s.es, "run-selo") {
		if e.Type != exchangeEventType {
			continue
		}
		var pay struct {
			ProviderPolicy      string `json:"provider_policy"`
			ProviderPolicyShape string `json:"provider_policy_shape"`
		}
		if err := json.Unmarshal(e.Payload, &pay); err != nil {
			t.Fatalf("payload ilegivel: %v", err)
		}
		visto = true
		// O par completo: a postura sozinha diria `enforced` sobre um eixo aberto.
		if pay.ProviderPolicy != string(ProviderPostureEnforced) {
			t.Errorf("provider_policy = %q", pay.ProviderPolicy)
		}
		if pay.ProviderPolicyShape != string(ProviderPolicyShapeWildcard) {
			t.Errorf("provider_policy_shape = %q, esperado %q", pay.ProviderPolicyShape, ProviderPolicyShapeWildcard)
		}
	}
	if !visto {
		t.Fatal("nenhum evento de troca emitida no stream")
	}
}

// TestAOS342_ANegacaoDoGateSelaAForma: o mesmo par tem de estar na negação, senão duas
// negações sob regimes materialmente diferentes voltam a ser indistinguíveis — que é o
// defeito que o AOS-332 fechou, meio fechado.
func TestAOS342_ANegacaoDoGateSelaAForma(t *testing.T) {
	t.Parallel()
	// Conjuntos CONCRETOS: a classe alcança só `provider`, logo o outro é negado.
	s := stackComPosturas(t, map[string][]string{agentClass: {provider}}, nil)

	if _, err := s.broker.Exchange(context.Background(), requestForProvider("run-neg", providerOther, provInScopeCap)); err == nil {
		t.Fatal("a troca tinha de ser negada")
	}
	var razao string
	for _, e := range readStream(t, s.es, "run-neg") {
		if e.Type == referencemonitor.EventTypeDenied {
			razao = string(e.Payload)
		}
	}
	if razao == "" {
		t.Fatal("nenhuma negacao selada")
	}
	for _, quer := range []string{
		"provider_policy=" + string(ProviderPostureEnforced),
		"provider_policy_shape=" + string(ProviderPolicyShapeByClass),
	} {
		if !strings.Contains(razao, quer) {
			t.Errorf("a negacao selada nao regista %q:\n%s", quer, razao)
		}
	}
}

// TestAOS342_AcessoresSaoNilSafe: o wiring do DEF-218 interroga a postura E a forma ANTES
// de compor a troca, e um broker por construir é o estado em que essa pergunta é mais
// provável. Um panic aí trocaria uma pré-condição por uma paragem.
func TestAOS342_AcessoresSaoNilSafe(t *testing.T) {
	t.Parallel()
	var b *Broker
	if got := b.ProviderPosture(); got != ProviderPostureUnset {
		t.Errorf("ProviderPosture() num broker nil = %q, esperado %q", got, ProviderPostureUnset)
	}
	if got := b.ProviderPolicyShape(); got != ProviderPolicyShapeNone {
		t.Errorf("ProviderPolicyShape() num broker nil = %q, esperado %q", got, ProviderPolicyShapeNone)
	}
	if got := b.ProviderClassesComCuringa(); got != nil {
		t.Errorf("ProviderClassesComCuringa() num broker nil = %v, esperado nil", got)
	}
}
