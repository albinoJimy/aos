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

// AOS-339 — A NEGAÇÃO SERVER-SIDE QUE O WORM REGISTAVA COMO PERMIT.
//
// As guardas de composição de [Broker.dispatch] (a de capability, AOS-057; a do
// eixo provider, AOS-324) correm DENTRO da [referencemonitor.ToolFunc], ou seja
// DEPOIS de a cadeia de mediação ter permitido e ter selado o seu
// `MediationRecord` com `Effect: EffectPermit` — por audit-before-effect. O erro
// da guarda sai por `Decision.ToolErr`, que não corrige o registo já selado.
//
// Resultado, medido antes desta correcção: numa pilha SEM o [ScopeGate] composto,
// uma troca negada no eixo provider ficava no Event Store com UM ÚNICO evento,
// `tool.call.mediated` / `decision=permit`, e mais nada. Nenhum evento de negação,
// nenhuma postura, nenhum eixo. O `TestAOS324_DefesaServerSide_SemGate` não via o
// defeito porque só perguntava pela AUSÊNCIA de `credential.exchange.issued`.
//
// Estes testes exercem a via compensatória: o broker sela o seu PRÓPRIO evento
// `credential.exchange.denied`, no molde de [Broker.recordExchange], com quem/para
// quê/eixo/código/postura. O WORM não se reescreve — acrescenta-se.

// semGateStack monta a pilha do cenário: broker COM política declarada mas SEM o
// [ScopeGate] na cadeia do RM, que é o que faz a guarda de composição ser quem
// nega. Devolve o broker e o Event Store para leitura directa do stream.
func semGateStack(t *testing.T, classProviders map[string][]string) (*Broker, *eventstore.Store) {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	rm := referencemonitor.New(referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(es)))
	vlt := NewMemoryVault()
	// AMBOS os provedores aprovisionados: a negação nunca pode ser confundida com
	// ausência de material (mesma disciplina do providerStack do AOS-324).
	vlt.Put(VaultKey{Provider: provider, Region: region, Capability: provInScopeCap}, sentinel)
	vlt.Put(VaultKey{Provider: providerOther, Region: region, Capability: provInScopeCap}, sentinel)
	vlt.Put(VaultKey{Provider: provider, Region: region, Capability: classScopedCap}, sentinel)

	clock := newTestClock()
	b, err := New(rm, vlt, es,
		WithClock(clock.now),
		WithTTL(time.Minute),
		WithClassScopes(defaultClassScopes()),
		WithClassProviders(classProviders),
	)
	if err != nil {
		t.Fatalf("broker.New: %v", err)
	}
	return b, es
}

// denialEvents extrai os payloads dos eventos de negação server-side de um stream.
func denialEvents(t *testing.T, es *eventstore.Store, stream string) []deniedPayload {
	t.Helper()
	var out []deniedPayload
	for _, e := range readStream(t, es, stream) {
		if e.Type != exchangeDeniedEventType {
			continue
		}
		var pay deniedPayload
		if err := json.Unmarshal(e.Payload, &pay); err != nil {
			t.Fatalf("payload de negacao ilegivel: %v", err)
		}
		out = append(out, pay)
	}
	return out
}

// TestAOS339_NegacaoServerSide_SeladaComAtribuicaoEPostura é o teste central: nos
// DOIS eixos que a guarda de composição impõe, a negação produz um evento próprio,
// ATRIBUÍVEL (eixo/código/razão/quem-negou) e com a POSTURA sob a qual foi decidida.
func TestAOS339_NegacaoServerSide_SeladaComAtribuicaoEPostura(t *testing.T) {
	tests := []struct {
		name     string
		req      func(runID string) ExchangeRequest
		wantErr  error
		wantAxis string
		wantCode string
	}{
		{
			name:     "eixo provider: provedor fora da autoridade",
			req:      func(r string) ExchangeRequest { return requestForProvider(r, providerOther, provInScopeCap) },
			wantErr:  ErrProviderOutOfScope,
			wantAxis: axisProvider,
			wantCode: "provider_out_of_scope",
		},
		{
			name:     "eixo provider: provedor indeterminado",
			req:      func(r string) ExchangeRequest { return requestForProvider(r, "", provInScopeCap) },
			wantErr:  ErrProviderUndetermined,
			wantAxis: axisProvider,
			wantCode: "provider_undetermined",
		},
		{
			// A classe tem refund; o utilizador canónico só tem charge. O MESMO
			// buraco de audit existia neste eixo, no mesmo bloco de guardas.
			name:     "eixo capability: fora de utilizador ∩ classe",
			req:      func(r string) ExchangeRequest { return request(r, classScopedCap) },
			wantErr:  ErrOutOfScope,
			wantAxis: axisCapability,
			wantCode: "capability_out_of_scope",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, es := semGateStack(t, enforcedClassProviders())

			h, err := b.Exchange(context.Background(), tc.req("run-1"))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("erro = %v, esperado %v", err, tc.wantErr)
			}
			if h != "" {
				t.Fatalf("handle emitido apesar da negacao: %q", h)
			}

			evs := denialEvents(t, es, "run-1")
			if len(evs) != 1 {
				t.Fatalf("eventos de negacao = %d, esperado exactamente 1", len(evs))
			}
			pay := evs[0]
			if pay.Axis != tc.wantAxis {
				t.Errorf("axis = %q, esperado %q", pay.Axis, tc.wantAxis)
			}
			if pay.Code != tc.wantCode {
				t.Errorf("code = %q, esperado %q", pay.Code, tc.wantCode)
			}
			if pay.Reason != tc.wantErr.Error() {
				t.Errorf("reason = %q, esperado %q", pay.Reason, tc.wantErr.Error())
			}
			// ATRIBUÍVEL à CAMADA: quem lê o WORM distingue a guarda de composição
			// do dispatch do [ScopeGate] da mediação ("broker-scope").
			if pay.DeniedBy != dispatchGuardName {
				t.Errorf("denied_by = %q, esperado %q", pay.DeniedBy, dispatchGuardName)
			}
			// A POSTURA sob a qual foi decidida — o que o AOS-332 pede para o
			// caminho do gate e que faltava por inteiro neste.
			if pay.ProviderPolicy != string(ProviderPostureEnforced) {
				t.Errorf("provider_policy = %q, esperado %q", pay.ProviderPolicy, ProviderPostureEnforced)
			}
			// Quem/para quê, sem os quais o evento não é auditável sozinho.
			if pay.PrincipalNHI == "" || pay.Capability == "" || pay.DeniedAt == "" {
				t.Errorf("negacao sem quem/para que/quando: %+v", pay)
			}
			// E NUNCA o valor: a negação corre antes de a chave do Vault existir.
			raw, _ := json.Marshal(pay)
			if strings.Contains(string(raw), sentinel) {
				t.Fatal("SEGREDO no payload da negacao")
			}
		})
	}
}

// TestAOS339_PosturaUnset_SeladaNaNegacao prova que a postura por omissão também
// fica no evento — não é um campo que só apareça quando é "enforced". Sob
// [ProviderPostureUnset] o eixo provider não impõe por conjunto, mas o provedor
// VAZIO continua a ser negado (AOS-324), e é essa negação que aqui se lê.
func TestAOS339_PosturaUnset_SeladaNaNegacao(t *testing.T) {
	b, es := semGateStack(t, nil)
	if got := b.ProviderPosture(); got != ProviderPostureUnset {
		t.Fatalf("postura = %q, esperado %q", got, ProviderPostureUnset)
	}

	_, err := b.Exchange(context.Background(), requestForProvider("run-unset", "", provInScopeCap))
	if !errors.Is(err, ErrProviderUndetermined) {
		t.Fatalf("erro = %v, esperado %v", err, ErrProviderUndetermined)
	}
	evs := denialEvents(t, es, "run-unset")
	if len(evs) != 1 {
		t.Fatalf("eventos de negacao = %d, esperado 1", len(evs))
	}
	if evs[0].ProviderPolicy != string(ProviderPostureUnset) {
		t.Errorf("provider_policy = %q, esperado %q", evs[0].ProviderPolicy, ProviderPostureUnset)
	}
}

// TestAOS339_NegacoesRepetidas_NaoColapsam: a idempotency_key do Event Store é
// f(run_id, step_id) e o step_id do PEDIDO é o mesmo em todas as tentativas do
// mesmo passo. Sem step_id próprio por negação, a segunda tentativa negada seria
// deduplicada e ficaria INVISÍVEL — um agente a martelar o mesmo provedor proibido
// aparecia no audit como uma tentativa só.
func TestAOS339_NegacoesRepetidas_NaoColapsam(t *testing.T) {
	b, es := semGateStack(t, enforcedClassProviders())
	req := requestForProvider("run-rep", providerOther, provInScopeCap)

	const tentativas = 3
	for i := 0; i < tentativas; i++ {
		if _, err := b.Exchange(context.Background(), req); !errors.Is(err, ErrProviderOutOfScope) {
			t.Fatalf("tentativa %d: erro = %v", i, err)
		}
	}
	if got := len(denialEvents(t, es, "run-rep")); got != tentativas {
		t.Fatalf("eventos de negacao = %d, esperado %d (tentativas colapsadas por idempotencia)", got, tentativas)
	}
}

// TestAOS339_CtxCancelado_NaoApagaORasto: o registo é PÓS-DECISÃO (a negação já
// está tomada, o Vault nem foi tocado), pelo que o cancelamento do chamador não o
// pode apagar — mesma razão do `Monitor.fail` (achado adversarial sobre AOS-311).
//
// EXERCITA [Broker.recordDenial] DIRECTAMENTE, e não por [Broker.Exchange]: o
// `Monitor.Mediate` recusa um ctx já cancelado no seu primeiro passo
// (`monitor.go:269`) e a chamada nem chega ao dispatch. A via end-to-end não
// consegue, por construção, pôr um ctx cancelado dentro da guarda — o que este
// teste tem de provar é que, SE lá chegar (cancelamento entre a mediação e a
// guarda, ou um ctx com prazo curto), o [context.WithoutCancel] mantém o rasto.
func TestAOS339_CtxCancelado_NaoApagaORasto(t *testing.T) {
	b, es := semGateStack(t, enforcedClassProviders())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	in := exchangeInput{
		RunID:         "run-cancel",
		StepID:        "step-1",
		PrincipalNHI:  "nhi:agent",
		AgentClass:    agentClass,
		UserAuthority: []string{provInScopeCap},
		Provider:      providerOther,
		Region:        region,
		Capability:    provInScopeCap,
	}
	if err := b.recordDenial(ctx, in, axisProvider, ErrProviderOutOfScope); !errors.Is(err, ErrProviderOutOfScope) {
		t.Fatalf("erro = %v, esperado %v", err, ErrProviderOutOfScope)
	}
	if got := len(denialEvents(t, es, "run-cancel")); got != 1 {
		t.Fatalf("eventos de negacao = %d com ctx cancelado, esperado 1", got)
	}
}

// TestAOS339_FalhaDeRegisto_NaoEhSilenciosaENaoPerdeAtribuicao: se o sink falhar,
// a negação MANTÉM-SE (não há efeito para libertar) e o sentinela atribuível
// continua a casar por errors.Is — mas a falha de auditoria vai JUNTA ao erro, em
// vez de desaparecer num `_`. É a divergência deliberada face ao `Monitor.fail`.
func TestAOS339_FalhaDeRegisto_NaoEhSilenciosaENaoPerdeAtribuicao(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	rm := referencemonitor.New()
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
	// Fecha o store: o Append da negação passa a falhar.
	if err := es.Close(); err != nil {
		t.Fatalf("es.Close: %v", err)
	}

	h, err := b.Exchange(context.Background(), requestForProvider("run-sink", providerOther, provInScopeCap))
	if h != "" {
		t.Fatalf("handle emitido com sink em falha: %q", h)
	}
	// A negação continua ATRIBUÍVEL — trocar o sentinela por um erro de sink
	// apagaria a razão pela qual a troca foi recusada.
	if !errors.Is(err, ErrProviderOutOfScope) {
		t.Fatalf("atribuicao perdida na falha de registo: %v", err)
	}
	// E a perda de auditoria é VISÍVEL, não silenciosa.
	if err.Error() == ErrProviderOutOfScope.Error() {
		t.Error("falha de registo silenciosa: o erro nao denuncia a auditoria perdida")
	}
}
