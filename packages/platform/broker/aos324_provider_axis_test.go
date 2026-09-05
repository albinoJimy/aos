package broker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-324 — O EIXO PROVIDER DA TROCA.
//
// O defeito: a chave do Vault é {Provider, Region, Capability} montada a partir do
// PEDIDO, a capability da troca é CONSTANTE (AOS-264) e o eixo provider não tinha
// imposição nenhuma — um principal autorizado para o provedor A obtinha material de
// QUALQUER outro presente no Vault (confusão de deputado). Nenhum teste do repo
// exercia troca cross-provider: todos usavam um único provedor como constante.
//
// Estes testes exercem o eixo com DOIS provedores AMBOS APROVISIONADOS no Vault —
// é isso que torna a negação não-vácua: se o material do provedor B não existisse,
// a troca falharia por [ErrNoMaterial] e nada se provaria sobre política.

// providerOther é o SEGUNDO provedor (o "B" do cenário cross-provider). Tem
// material aprovisionado no Vault em todos os testes que o usam.
const providerOther = "openai"

// enforcedClassProviders é a política do eixo provider dos testes: a classe
// canónica alcança APENAS o provedor "stripe".
func enforcedClassProviders() map[string][]string {
	return map[string][]string{agentClass: {provider}}
}

// providerStack liga o mesmo material do [newStack] com a política do eixo provider
// DECLARADA ([ProviderPostureEnforced]) e com material dos DOIS provedores no
// Vault, para que uma negação cross-provider nunca possa ser confundida com
// ausência de material.
func providerStack(t *testing.T, classProviders map[string][]string) *stack {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	scopes := defaultClassScopes()
	clock := newTestClock()
	vlt := NewMemoryVault()
	// AMBOS os provedores aprovisionados (o de A e o de B).
	vlt.Put(VaultKey{Provider: provider, Region: region, Capability: provInScopeCap}, sentinel)
	vlt.Put(VaultKey{Provider: providerOther, Region: region, Capability: provInScopeCap}, sentinel)

	// O gate impõe AMBOS os eixos (capability + provider) na mediação.
	gate := NewScopeGate(DefaultExchangeToolID, scopes, WithGateClassProviders(classProviders))
	rm := referencemonitor.New(
		referencemonitor.WithHooks(append(referencemonitor.DefaultHooks(), gate)...),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(es)),
	)
	b, err := New(rm, vlt, es,
		WithClock(clock.now),
		WithTTL(time.Minute),
		WithClassScopes(scopes),
		WithClassProviders(classProviders),
	)
	if err != nil {
		t.Fatalf("broker.New: %v", err)
	}
	return &stack{broker: b, rm: rm, es: es, vault: vlt, guest: NewMemoryGuest(), clock: clock}
}

// requestForProvider monta um pedido de troca para um PROVEDOR concreto (o resto
// igual ao pedido canónico: principal com charge, capability em escopo).
func requestForProvider(runID, prov, capability string) ExchangeRequest {
	req := request(runID, capability)
	req.Downstream.Provider = prov
	return req
}

// TestAOS324_CrossProvider_NegadoEAtribuivel é o teste que faltava ao repositório:
// um principal autorizado para o provedor A (stripe) pede material do provedor B
// (openai), COM material de B presente no Vault. A troca é NEGADA, a negação é
// ATRIBUÍVEL (efeito/código/razão/quem-negou), NÃO é [ErrNoMaterial], nenhum handle
// é emitido e nenhuma troca é selada — só a negação.
func TestAOS324_CrossProvider_NegadoEAtribuivel(t *testing.T) {
	st := providerStack(t, enforcedClassProviders())
	ctx := context.Background()

	// (1) provedor AUTORIZADO (A): troca normal.
	h, err := st.broker.Exchange(ctx, requestForProvider("run-a", provider, provInScopeCap))
	if err != nil {
		t.Fatalf("provedor autorizado devia trocar: %v", err)
	}
	if h == "" {
		t.Fatal("handle vazio no provedor autorizado")
	}

	// (2) provedor NÃO-AUTORIZADO (B), com material presente: NEGA.
	h2, err := st.broker.Exchange(ctx, requestForProvider("run-b", providerOther, provInScopeCap))
	if h2 != "" {
		t.Fatalf("handle emitido para provedor fora da autoridade: %q", h2)
	}
	var de *DeniedError
	if !errors.As(err, &de) {
		t.Fatalf("esperado *DeniedError atribuido, obtido %v", err)
	}
	if de.Effect != string(referencemonitor.EffectDeny) {
		t.Errorf("efeito = %q, esperado %q", de.Effect, referencemonitor.EffectDeny)
	}
	if de.Code != referencemonitor.CodeDeniedByHook {
		t.Errorf("codigo = %q, esperado %q", de.Code, referencemonitor.CodeDeniedByHook)
	}
	if de.DeniedBy != "broker-scope" {
		t.Errorf("negacao nao atribuida ao gate: DeniedBy=%q", de.DeniedBy)
	}
	if !strings.Contains(de.Reason, ErrProviderOutOfScope.Error()) {
		t.Errorf("razao nao nomeia o eixo provider: %q", de.Reason)
	}

	// (3) É POLÍTICA, não ausência de material — a distinção que o ticket exige.
	if errors.Is(err, ErrNoMaterial) {
		t.Error("negacao de politica confundida com ErrNoMaterial")
	}

	// (4) O Event Store sela a NEGAÇÃO e NÃO a troca.
	var denied, issued bool
	for _, e := range readStream(t, st.es, "run-b") {
		switch e.Type {
		case referencemonitor.EventTypeDenied:
			denied = true
		case exchangeEventType:
			issued = true
		}
	}
	if !denied {
		t.Error("negacao cross-provider nao selada no Event Store")
	}
	if issued {
		t.Error("troca selada apesar de negada no eixo provider")
	}
}

// TestAOS324_NoMaterial_ContinuaDistintoDaPolitica prova o OUTRO lado da distinção:
// um provedor AUTORIZADO sem material no Vault falha com [ErrNoMaterial] e NÃO com
// uma negação de política. Sem isto, "distinguível de ErrNoMaterial" seria vácuo.
func TestAOS324_NoMaterial_ContinuaDistintoDaPolitica(t *testing.T) {
	st := providerStack(t, enforcedClassProviders())

	// provedor autorizado, região SEM material aprovisionado.
	req := requestForProvider("run-nm", provider, provInScopeCap)
	req.Downstream.Region = "us"
	_, err := st.broker.Exchange(context.Background(), req)
	if !errors.Is(err, ErrNoMaterial) {
		t.Fatalf("esperado ErrNoMaterial, obtido %v", err)
	}
	var de *DeniedError
	if errors.As(err, &de) {
		t.Error("ausencia de material apresentada como negacao de politica")
	}
}

// TestAOS324_DefesaServerSide_SemGate cobre a camada belt-and-suspenders do eixo
// provider: um broker COM política de provedores mas SEM o [ScopeGate] na cadeia do
// RM. A mediação permite e despacha; é a verificação server-side em dispatch que
// nega ANTES de a chave do Vault existir — sem handle e sem registo de troca.
func TestAOS324_DefesaServerSide_SemGate(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	rm := referencemonitor.New(referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(es)))
	vlt := NewMemoryVault()
	vlt.Put(VaultKey{Provider: providerOther, Region: region, Capability: provInScopeCap}, sentinel)

	clock := newTestClock()
	b, err := New(rm, vlt, es,
		WithClock(clock.now),
		WithTTL(time.Minute),
		WithClassScopes(defaultClassScopes()),
		WithClassProviders(enforcedClassProviders()),
	)
	if err != nil {
		t.Fatalf("broker.New: %v", err)
	}

	h, err := b.Exchange(context.Background(), requestForProvider("run-1", providerOther, provInScopeCap))
	if !errors.Is(err, ErrProviderOutOfScope) {
		t.Fatalf("esperado ErrProviderOutOfScope via defesa server-side, obtido %v", err)
	}
	if h != "" {
		t.Fatalf("handle emitido apesar da defesa server-side: %q", h)
	}
	for _, e := range readStream(t, es, "run-1") {
		if e.Type == exchangeEventType {
			t.Fatal("troca registada apesar de negada no eixo provider")
		}
	}
}

// TestAOS324_ProvedorVazio_NegadoNasDuasPosturas: sem provedor não há chave
// legítima. É a parte do eixo que é fail-closed INDEPENDENTEMENTE de haver política
// declarada — a postura por omissão não relaxa isto.
func TestAOS324_ProvedorVazio_NegadoNasDuasPosturas(t *testing.T) {
	tests := []struct {
		name      string
		providers map[string][]string
		posture   ProviderPosture
	}{
		{"postura unset (sem politica)", nil, ProviderPostureUnset},
		{"postura enforced", enforcedClassProviders(), ProviderPostureEnforced},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := providerStack(t, tc.providers)
			if got := st.broker.ProviderPosture(); got != tc.posture {
				t.Fatalf("postura = %q, esperado %q", got, tc.posture)
			}
			h, err := st.broker.Exchange(context.Background(), requestForProvider("run-vazio", "", provInScopeCap))
			if h != "" {
				t.Fatalf("handle emitido sem provedor: %q", h)
			}
			var de *DeniedError
			if !errors.As(err, &de) {
				t.Fatalf("esperado *DeniedError, obtido %v", err)
			}
			if !strings.Contains(de.Reason, ErrProviderUndetermined.Error()) {
				t.Errorf("razao nao nomeia o provedor indeterminado: %q", de.Reason)
			}
		})
	}
}

// TestAOS324_PosturaPorOmissao_DeclaradaESelada prova que o estado por omissão NÃO
// é silencioso: a postura é interrogável ([Broker.ProviderPosture]) e fica SELADA
// em CADA troca no campo `provider_policy` do evento — auditável e greppável. É o
// que impede que a ausência de política passe despercebida no dia do wiring.
func TestAOS324_PosturaPorOmissao_DeclaradaESelada(t *testing.T) {
	tests := []struct {
		name      string
		providers map[string][]string
		want      ProviderPosture
	}{
		{"sem politica declarada", nil, ProviderPostureUnset},
		{"politica declarada", enforcedClassProviders(), ProviderPostureEnforced},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := providerStack(t, tc.providers)
			if got := st.broker.ProviderPosture(); got != tc.want {
				t.Fatalf("Broker.ProviderPosture() = %q, esperado %q", got, tc.want)
			}
			if got := st.broker.ScopeGate().ProviderPosture(); got != tc.want {
				t.Fatalf("ScopeGate.ProviderPosture() = %q, esperado %q", got, tc.want)
			}
			if _, err := st.broker.Exchange(context.Background(), requestForProvider("run-sela", provider, provInScopeCap)); err != nil {
				t.Fatalf("Exchange: %v", err)
			}
			var seen int
			for _, e := range readStream(t, st.es, "run-sela") {
				if e.Type != exchangeEventType {
					continue
				}
				seen++
				var pay exchangePayload
				if err := json.Unmarshal(e.Payload, &pay); err != nil {
					t.Fatalf("payload: %v", err)
				}
				if pay.ProviderPolicy != string(tc.want) {
					t.Errorf("provider_policy selado = %q, esperado %q", pay.ProviderPolicy, tc.want)
				}
				if pay.Provider != provider {
					t.Errorf("provedor selado = %q, esperado %q", pay.Provider, provider)
				}
			}
			if seen != 1 {
				t.Fatalf("esperado 1 registo de troca, obtido %d", seen)
			}
		})
	}
}

// TestAOS324_EffectiveProviders cobre a álgebra da autoridade de provedor: o tecto
// da classe, os grants do principal (que só ESTREITAM) e o curinga explícito.
func TestAOS324_EffectiveProviders(t *testing.T) {
	tests := []struct {
		name      string
		authority []string
		class     []string
		want      []string
	}{
		{"sem grants ⇒ tecto da classe", []string{provInScopeCap}, []string{"a", "b"}, []string{"a", "b"}},
		{"grants estreitam o tecto", []string{"prov:a", "prov:z"}, []string{"a", "b"}, []string{"a"}},
		{"grants sem sobreposicao ⇒ vazio", []string{"prov:z"}, []string{"a"}, nil},
		{"tecto curinga ⇒ exactamente os grants", []string{"prov:z"}, []string{ProviderAny}, []string{"z"}},
		{"tecto curinga sem grants ⇒ curinga", nil, []string{ProviderAny}, []string{ProviderAny}},
		{"classe sem tecto ⇒ vazio", []string{"prov:a"}, nil, nil},
		{"dedup e ordenacao", []string{"prov:b", "prov:a", "prov:a"}, []string{"a", "b", "b"}, []string{"a", "b"}},
		{"grant vazio ignorado", []string{"prov:"}, []string{"a"}, []string{"a"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EffectiveProviders(tc.authority, tc.class)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestAOS324_AutorizaProvider cobre a REGRA partilhada pelo gate e pela defesa
// server-side, incluindo a classe ausente da política (não alcança provedor nenhum)
// e a postura por omissão.
func TestAOS324_AutorizaProvider(t *testing.T) {
	pol := map[string][]string{
		agentClass:      {provider},
		"classe-aberta": {ProviderAny},
	}
	tests := []struct {
		name      string
		policy    map[string][]string
		class     string
		authority []string
		provider  string
		wantErr   error
	}{
		{"unset: provedor qualquer passa", nil, agentClass, nil, providerOther, nil},
		{"unset: provedor vazio NEGA", nil, agentClass, nil, "", ErrProviderUndetermined},
		{"enforced: provedor no tecto passa", pol, agentClass, nil, provider, nil},
		{"enforced: provedor fora do tecto NEGA", pol, agentClass, nil, providerOther, ErrProviderOutOfScope},
		{"enforced: classe ausente da politica NEGA", pol, "classe-desconhecida", nil, provider, ErrProviderOutOfScope},
		{"enforced: curinga da classe passa", pol, "classe-aberta", nil, providerOther, nil},
		{"enforced: grant do principal estreita o curinga", pol, "classe-aberta", []string{"prov:" + provider}, providerOther, ErrProviderOutOfScope},
		{"enforced: grant do principal nao amplia o tecto", pol, agentClass, []string{"prov:" + providerOther}, providerOther, ErrProviderOutOfScope},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := authorizeProvider(tc.policy, tc.class, tc.authority, tc.provider)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("erro = %v, esperado %v", err, tc.wantErr)
			}
		})
	}
}

// TestAOS324_Gate_SemEnvelopeDeTroca fixa o comportamento do gate quando o call não
// traz o envelope da troca (o provedor é indeterminável): sob política DECLARADA
// nega fail-closed; sem política, o gate não se opõe — é o que mantém os testes de
// unidade do eixo capability (AOS-264) a exercer apenas esse eixo.
func TestAOS324_Gate_SemEnvelopeDeTroca(t *testing.T) {
	call := func() *referencemonitor.Call {
		return &referencemonitor.Call{
			ToolID:     DefaultExchangeToolID,
			Capability: provInScopeCap,
			Principal:  principal(provInScopeCap),
		}
	}

	unset := NewScopeGate(DefaultExchangeToolID, defaultClassScopes())
	res, err := unset.Evaluate(context.Background(), call())
	if err != nil {
		t.Fatalf("Evaluate (unset): %v", err)
	}
	if res.Decision != referencemonitor.HookAllow {
		t.Fatalf("postura unset devia permitir sem envelope, obtido %v (%s)", res.Decision, res.Reason)
	}

	enforced := NewScopeGate(DefaultExchangeToolID, defaultClassScopes(), WithGateClassProviders(enforcedClassProviders()))
	res2, err := enforced.Evaluate(context.Background(), call())
	if err != nil {
		t.Fatalf("Evaluate (enforced): %v", err)
	}
	if res2.Decision != referencemonitor.HookDeny {
		t.Fatalf("postura enforced devia negar sem envelope, obtido %v", res2.Decision)
	}
	if !strings.Contains(res2.Reason, ErrProviderUndetermined.Error()) {
		t.Errorf("razao = %q", res2.Reason)
	}
}
