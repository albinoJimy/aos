package broker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-332 — A POSTURA SOB A QUAL A NEGAÇÃO DO GATE FOI DECIDIDA.
//
// O AOS-324 sela `provider_policy` em cada troca EMITIDA; o AOS-339 sela-o na
// negação da guarda de composição do `dispatch`. Faltava o terceiro caminho: uma
// negação tomada pelo [ScopeGate] NA MEDIAÇÃO termina na cadeia do Reference
// Monitor e o que fica selado é o `tool.call.denied` do RM — que não tem campo
// para a postura, e cujo payload o broker não possui.
//
// Sem isto, duas negações materialmente diferentes — uma sob política DECLARADA,
// outra sob a postura por omissão — eram indistinguíveis no WORM. A segunda diz que
// o eixo não é imposto por conjunto e que só o provedor em branco foi recusado; ler
// as duas como a mesma coisa é ler `unset` como se fosse `enforced`.
//
// Estes testes leem o payload SELADO, não a mensagem em memória: é a diferença
// entre provar que o valor chega ao audit e provar que a função o devolveu.

// razaoSeladaDaNegacao lê a `reason` do único evento `tool.call.denied` de um stream.
func razaoSeladaDaNegacao(t *testing.T, es *eventstore.Store, stream string) string {
	t.Helper()
	var razoes []string
	for _, e := range readStream(t, es, stream) {
		if e.Type != referencemonitor.EventTypeDenied {
			continue
		}
		var pay struct {
			Reason   string `json:"reason"`
			DeniedBy string `json:"denied_by"`
		}
		if err := json.Unmarshal(e.Payload, &pay); err != nil {
			t.Fatalf("payload de negacao ilegivel: %v", err)
		}
		if pay.DeniedBy != "broker-scope" {
			t.Fatalf("negacao nao atribuida ao gate: denied_by=%q", pay.DeniedBy)
		}
		razoes = append(razoes, pay.Reason)
	}
	if len(razoes) != 1 {
		t.Fatalf("eventos tool.call.denied = %d, esperado exactamente 1", len(razoes))
	}
	return razoes[0]
}

// TestAOS332_NegacaoDoGate_SelaAPosturaEAsDuasSaoDistintas é o teste central do
// ticket: as duas posturas produzem negações SELADAS distintas, e cada uma nomeia a
// postura sob a qual foi decidida.
//
// Os dois casos não podem ser o mesmo pedido, e isso é uma propriedade do eixo e
// não uma comodidade do teste: sob [ProviderPostureUnset] o gate NÃO impõe por
// conjunto, pelo que um pedido cross-provider passa — a única negação que a postura
// por omissão produz é a do provedor em branco (AOS-324, fail-closed nas duas).
func TestAOS332_NegacaoDoGate_SelaAPosturaEAsDuasSaoDistintas(t *testing.T) {
	tests := []struct {
		name      string
		providers map[string][]string
		prov      string
		wantErr   error
		want      ProviderPosture
	}{
		{
			name:      "politica DECLARADA, provedor fora da autoridade",
			providers: enforcedClassProviders(),
			prov:      providerOther,
			wantErr:   ErrProviderOutOfScope,
			want:      ProviderPostureEnforced,
		},
		{
			name:      "postura por OMISSAO, provedor em branco",
			providers: nil,
			prov:      "",
			wantErr:   ErrProviderUndetermined,
			want:      ProviderPostureUnset,
		},
	}

	seladas := make(map[string]string, len(tests))
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := providerStack(t, tc.providers)

			if _, err := st.broker.Exchange(context.Background(), requestForProvider("run-1", tc.prov, provInScopeCap)); err == nil {
				t.Fatal("troca devia ser negada pelo gate")
			}

			razao := razaoSeladaDaNegacao(t, st.es, "run-1")
			// A postura SELADA, sob o mesmo NOME do campo `provider_policy` dos
			// outros dois caminhos (a FORMA difere: lá é JSON, aqui é texto — é o
			// prefixo sem o `=` que é comum aos três).
			if quer := providerPolicyReasonKey + string(tc.want); !strings.Contains(razao, quer) {
				t.Errorf("razao selada nao nomeia a postura: %q, esperado conter %q", razao, quer)
			}
			// E continua a nomear o sentinela — a postura ACRESCENTA atribuição,
			// não a substitui (o AOS-324 assere sobre esta mesma razão).
			if !strings.Contains(razao, tc.wantErr.Error()) {
				t.Errorf("razao selada perdeu o sentinela: %q", razao)
			}
			seladas[tc.name] = posturaNaRazao(t, razao)
		})
	}

	// O CONTROLO, e o que ele mede DEPOIS de uma revisão adversarial o ter apanhado
	// vácuo. A primeira versão comparava as duas razões INTEIRAS — e essas nunca
	// podiam ser iguais, porque os dois casos usam sentinelas diferentes
	// (`ErrProviderOutOfScope` vs `ErrProviderUndetermined`). O controlo passava
	// mesmo com a postura removida por completo: media a distinção entre dois
	// sentinelas e apresentava-a como prova sobre a postura.
	//
	// Compara-se agora só o TOKEN da postura extraído de cada razão, que é a única
	// coisa que este teste tem de distinguir.
	if len(seladas) == len(tests) && seladas[tests[0].name] == seladas[tests[1].name] {
		t.Fatalf("as duas posturas selam o MESMO token: %q", seladas[tests[0].name])
	}
}

// posturaNaRazao extrai o valor de `provider_policy=` de uma razão selada. Falha o
// teste se não estiver lá — a ausência é o defeito, não um caso a ignorar.
func posturaNaRazao(t *testing.T, razao string) string {
	t.Helper()
	i := strings.Index(razao, providerPolicyReasonKey)
	if i < 0 {
		t.Fatalf("razao sem %q: %q", providerPolicyReasonKey, razao)
	}
	return razao[i+len(providerPolicyReasonKey):]
}

// TestAOS332_EnvelopeIlegivel_TambemNomeiaAPostura fecha o TERCEIRO sítio de negação
// do gate: o call que não traz envelope de troca legível, e cujo provedor é portanto
// indeterminável. Sob política DECLARADA isso é informação insuficiente ⇒ nega.
//
// PORQUE ESTE É AO NÍVEL DO HOOK E NÃO DO PAYLOAD SELADO, ao contrário dos outros: o
// envelope é serializado pelo próprio [Broker.Exchange], pelo que este ramo é
// INALCANÇÁVEL por uma troca real — só um call montado à mão lá chega. A evidência é
// mais fraca de propósito, e fica dito em vez de escondido atrás de um teste que
// parecesse equivalente aos outros.
//
// Foi uma PROVA DE MUTAÇÃO que o exigiu: sem ele, remover a postura só deste ramo
// sobrevivia a toda a suite.
func TestAOS332_EnvelopeIlegivel_TambemNomeiaAPostura(t *testing.T) {
	call := &referencemonitor.Call{
		ToolID:     DefaultExchangeToolID,
		Capability: provInScopeCap,
		Principal:  principal(provInScopeCap),
	}
	gate := NewScopeGate(DefaultExchangeToolID, defaultClassScopes(), WithGateClassProviders(enforcedClassProviders()))

	res, err := gate.Evaluate(context.Background(), call)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Decision != referencemonitor.HookDeny {
		t.Fatalf("postura enforced devia negar sem envelope, obtido %v", res.Decision)
	}
	if quer := providerPolicyReasonKey + string(ProviderPostureEnforced); !strings.Contains(res.Reason, quer) {
		t.Errorf("razao nao nomeia a postura: %q, esperado conter %q", res.Reason, quer)
	}
	if !strings.Contains(res.Reason, ErrProviderUndetermined.Error()) {
		t.Errorf("razao perdeu o sentinela: %q", res.Reason)
	}
}

// TestAOS332_EixoCapability_NaoCarimbaAPosturaDoProvider é o controlo do alcance:
// uma negação de CAPABILITY não é decidida sob o regime do eixo provider, e
// carimbá-la com ele diria ao auditor que foi.
//
// ISTO NÃO CONTRADIZ O AOS-339, e a distinção é dos CONTENTORES, não do princípio.
// No `credential.exchange.denied` a postura é um campo TIPADO ao lado de um campo
// `axis`: lê-se `axis=capability` E `provider_policy=enforced`, e percebe-se que o
// segundo é o regime do broker e não a causa. Aqui a postura vai DENTRO da frase que
// explica a recusa, e anexá-la a uma negação de capability leria como se o regime do
// eixo provider a tivesse causado. Campo ao lado do eixo: metadado, seguro em
// qualquer eixo. Sufixo na explicação: só onde o regime decidiu.
func TestAOS332_EixoCapability_NaoCarimbaAPosturaDoProvider(t *testing.T) {
	st := providerStack(t, enforcedClassProviders())
	st.vault.Put(VaultKey{Provider: provider, Region: region, Capability: classScopedCap}, sentinel)

	// A classe tem refund; o utilizador canonico so tem charge.
	if _, err := st.broker.Exchange(context.Background(), requestForProvider("run-cap", provider, classScopedCap)); err == nil {
		t.Fatal("troca fora do escopo de capability devia ser negada")
	}

	razao := razaoSeladaDaNegacao(t, st.es, "run-cap")
	if !strings.Contains(razao, ErrOutOfScope.Error()) {
		t.Fatalf("razao nao nomeia o eixo capability: %q", razao)
	}
	if strings.Contains(razao, providerPolicyReasonKey) {
		t.Errorf("negacao de capability carimbada com a postura do eixo provider: %q", razao)
	}
}

// TestAOS332_FormaDaPolitica_DistingueOQueAPosturaNaoOlha cobre o achado adversarial
// que motivou [Broker.ProviderPolicyShape]: a postura é função da NULIDADE do mapa, e
// duas políticas materialmente opostas — um curinga que não restringe nada e um mapa
// vazio que nega tudo — são AMBAS `enforced`.
//
// Sem esta distinção, o banner declararia "ENFORCED" nos dois casos e a pré-condição
// do DEF-218 passaria sobre exactamente o defeito que o AOS-324 fechou.
func TestAOS332_FormaDaPolitica_DistingueOQueAPosturaNaoOlha(t *testing.T) {
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
			"curinga numa classe", map[string][]string{agentClass: {ProviderAny}, "billing": {provider}},
			ProviderPostureEnforced, ProviderPolicyShapeWildcard, []string{agentClass},
		},
		{
			"conjuntos concretos", map[string][]string{agentClass: {provider}},
			ProviderPostureEnforced, ProviderPolicyShapeByClass, nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := providerStack(t, tc.providers)
			if got := st.broker.ProviderPosture(); got != tc.wantPost {
				t.Errorf("ProviderPosture() = %q, esperado %q", got, tc.wantPost)
			}
			if got := st.broker.ProviderPolicyShape(); got != tc.wantShape {
				t.Errorf("ProviderPolicyShape() = %q, esperado %q", got, tc.wantShape)
			}
			got := st.broker.ProviderClassesComCuringa()
			if len(got) != len(tc.wantCuring) {
				t.Fatalf("classes com curinga = %v, esperado %v", got, tc.wantCuring)
			}
			for i := range got {
				if got[i] != tc.wantCuring[i] {
					t.Errorf("classes com curinga = %v, esperado %v", got, tc.wantCuring)
				}
			}
		})
	}
}

// TestAOS332_CuringaEhEnforcedENaoImpoe prova que o estado que o banner tem de
// distinguir é REAL e não uma hipótese: sob uma política com curinga a postura diz
// `enforced` e a troca cross-provider PASSA. Sem isto, os estados novos do banner
// seriam texto sobre um caso que ninguém mediu.
func TestAOS332_CuringaEhEnforcedENaoImpoe(t *testing.T) {
	st := providerStack(t, map[string][]string{agentClass: {ProviderAny}})
	if got := st.broker.ProviderPosture(); got != ProviderPostureEnforced {
		t.Fatalf("postura = %q, esperado %q", got, ProviderPostureEnforced)
	}
	h, err := st.broker.Exchange(context.Background(), requestForProvider("run-curinga", providerOther, provInScopeCap))
	if err != nil {
		t.Fatalf("o curinga devia deixar passar QUALQUER provedor, obtido %v", err)
	}
	if h == "" {
		t.Fatal("handle vazio")
	}
}

// TestAOS332_AcessoresSaoNilSafe: o wiring do DEF-218 tem de poder interrogar a
// postura ANTES de compor a troca, e um broker por construir e o estado em que essa
// pergunta e mais provavel. Um panic ai trocaria uma pre-condicao por uma paragem.
func TestAOS332_AcessoresSaoNilSafe(t *testing.T) {
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
