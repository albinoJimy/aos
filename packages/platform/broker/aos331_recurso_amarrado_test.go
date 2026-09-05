package broker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-331 — O PROVEDOR AUTORIZADO PASSA A ESTAR AMARRADO AO RECURSO DE DESTINO.
//
// O eixo provider (AOS-324) decide QUEM troca para QUE provedor. Não decidia para ONDE a
// credencial ia ser apresentada: um principal autorizado a trocar para `stripe` podia pedir a
// troca com `ResourceValue = https://evil.example/colector`, e nada na cadeia relacionava as
// duas coisas.
//
// A VIA É ALLOWLIST DE HOST POR PROVEDOR, e as outras duas do AC não fechavam o eixo — a razão
// está em `resource_binding.go`. Em resumo: o `EgressGate` real compara com uma allowlist de
// DEPLOYMENT e não sabe o que é um provedor; e uma obrigação do PDP exigiria o bundle assinado,
// que é o próprio bloqueador do `DEF-218` — seria circular.

const (
	hostDoProvedor = "api.stripe.com"
	hostAlheio     = "evil.example"
)

func hostsDeTeste() map[string][]string {
	return map[string][]string{"stripe": {hostDoProvedor}}
}

// TestAOS331_ProvedorAutorizadoRecursoNao é o AC central: o provedor passa e o recurso não. A
// negação tem de ser ATRIBUÍVEL e DISTINGUÍVEL de `ErrProviderOutOfScope` — «este provedor não é
// teu» e «este destino não é deste provedor» levam o operador a sítios diferentes.
func TestAOS331_ProvedorAutorizadoRecursoNao(t *testing.T) {
	t.Parallel()
	err := authorizeResource(hostsDeTeste(), "stripe", "url", "https://"+hostAlheio+"/colector")
	if !errors.Is(err, ErrResourceOutOfScope) {
		t.Fatalf("err = %v, quer ErrResourceOutOfScope", err)
	}
	// A DISTINÇÃO, escrita à letra do AC.
	if errors.Is(err, ErrProviderOutOfScope) {
		t.Error("a negacao de RECURSO nao pode ser indistinguivel da de PROVEDOR")
	}
	// CONTROLO: o mesmo provedor com o SEU host passa. Sem isto, um `return err` incondicional
	// passaria o teste acima.
	if err := authorizeResource(hostsDeTeste(), "stripe", "url", "https://"+hostDoProvedor+"/charges"); err != nil {
		t.Errorf("o host do proprio provedor tem de passar: %v", err)
	}
}

// TestAOS331_EstadoPorOmissaoNaoENegarTudo fixa o AC do default. O AC proíbe explicitamente um
// deny-all silencioso, e a razão é a do eixo provider: um default que negasse tudo partiria todos
// os deployments que não configuram a coisa nova, e um eixo que ninguém consegue ligar não é
// segurança — é uma avaria.
func TestAOS331_EstadoPorOmissaoNaoENegarTudo(t *testing.T) {
	t.Parallel()
	if got := resourceBindingPosture(nil); got != ResourceBindingUnset {
		t.Fatalf("postura por omissao = %q, quer %q", got, ResourceBindingUnset)
	}
	// Sob unset, mesmo um recurso alheio passa — o eixo NÃO é imposto, e isso é declarado.
	if err := authorizeResource(nil, "stripe", "url", "https://"+hostAlheio+"/x"); err != nil {
		t.Errorf("sob unset o eixo nao e imposto: %v", err)
	}
	// E a postura é INTERROGÁVEL, que é o que a distingue de um silêncio: é assim que o banner
	// a pode declarar (AOS-332).
	g := NewScopeGate("tool", nil)
	if got := g.ResourceBindingPosture(); got != ResourceBindingUnset {
		t.Errorf("ScopeGate.ResourceBindingPosture() = %q, quer %q", got, ResourceBindingUnset)
	}
	g2 := NewScopeGate("tool", nil, WithGateProviderHosts(hostsDeTeste()))
	if got := g2.ResourceBindingPosture(); got != ResourceBindingEnforced {
		t.Errorf("com allowlist declarada = %q, quer %q", got, ResourceBindingEnforced)
	}
	// Um mapa VAZIO não-nil é uma declaração — «nenhum provedor alcança recurso nenhum» — e é
	// uma escolha legítima de quem quer o eixo fechado. É a mesma distinção do eixo provider.
	g3 := NewScopeGate("tool", nil, WithGateProviderHosts(map[string][]string{}))
	if got := g3.ResourceBindingPosture(); got != ResourceBindingEnforced {
		t.Errorf("mapa vazio nao-nil = %q, quer %q (declarada, nao ausente)", got, ResourceBindingEnforced)
	}
}

// TestAOS331_RecursoIndeterminadoENegadoSobPolitica — sob política declarada, informação
// insuficiente é recusa. É a mesma postura do envelope ilegível no eixo provider, e a sentinela é
// própria: «não sei o que é este destino» não é «este destino é de outro».
func TestAOS331_RecursoIndeterminadoENegadoSobPolitica(t *testing.T) {
	t.Parallel()
	casos := []struct{ nome, tipo, valor string }{
		{"tipo nao suportado", "queue", "fila-de-pagamentos"},
		{"tipo vazio", "", "https://" + hostDoProvedor},
		{"valor sem esquema", "url", hostDoProvedor + "/charges"},
		{"valor vazio", "url", ""},
		{"valor nao analisavel", "url", "://" + hostDoProvedor},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			t.Parallel()
			err := authorizeResource(hostsDeTeste(), "stripe", c.tipo, c.valor)
			if !errors.Is(err, ErrResourceUndetermined) {
				t.Errorf("err = %v, quer ErrResourceUndetermined", err)
			}
		})
	}
	// CONTROLO: sob UNSET nenhum destes nega — a recusa por indeterminação é consequência da
	// política declarada, não uma validação de forma que corra sempre.
	for _, c := range casos {
		if err := authorizeResource(nil, "stripe", c.tipo, c.valor); err != nil {
			t.Errorf("sob unset %q nao devia negar: %v", c.nome, err)
		}
	}
}

// TestAOS331_APorta80ENoHostNaoNaPorta — o host compara-se sem porta, senão a allowlist teria de
// enumerar portas e um `:443` explícito falharia contra a mesma entrada.
func TestAOS331_APortaNaoEntraNaComparacao(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"https://" + hostDoProvedor + ":443/charges", "https://" + hostDoProvedor + "/x?y=1"} {
		if err := authorizeResource(hostsDeTeste(), "stripe", "url", v); err != nil {
			t.Errorf("%q devia passar: %v", v, err)
		}
	}
	// E a comparação é case-insensitive nos dois lados, porque um host não o é.
	if err := authorizeResource(map[string][]string{"stripe": {"API.Stripe.COM"}}, "stripe", "URL", "https://api.STRIPE.com/x"); err != nil {
		t.Errorf("a comparacao de host tem de ser case-insensitive: %v", err)
	}
}

// TestAOS331_ProvedorSemEntradaNaAllowlistNaoAlcancaNada — sob política declarada, um provedor
// que a allowlist não menciona não alcança recurso nenhum. É o fail-closed por omissão DENTRO da
// política, distinto do estado por omissão DA política.
func TestAOS331_ProvedorSemEntradaNaAllowlistNaoAlcancaNada(t *testing.T) {
	t.Parallel()
	err := authorizeResource(hostsDeTeste(), "openai", "url", "https://api.openai.com/v1")
	if !errors.Is(err, ErrResourceOutOfScope) {
		t.Errorf("err = %v, quer ErrResourceOutOfScope — um provedor sem entrada nao alcanca nada", err)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════════════════
// PONTA-A-PONTA PELA CADEIA DE MEDIAÇÃO.
//
// Os testes acima exercitam a função de autorização; este exercita o CAMINHO — o `ScopeGate`
// dentro do RM, com o pedido real. É a diferença entre «a regra está escrita» e «a regra corre».
// ═══════════════════════════════════════════════════════════════════════════════════════════

// recursoStack levanta a pilha do broker com o eixo recurso↔provedor DECLARADO, no molde de
// `providerStack`. O eixo provider fica em `unset` de propósito: assim, qualquer negação que
// apareça é do eixo NOVO, e não há como confundir a origem.
func recursoStack(t *testing.T, providerHosts map[string][]string) *stack {
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

	gate := NewScopeGate(DefaultExchangeToolID, scopes, WithGateProviderHosts(providerHosts))
	rm := referencemonitor.New(
		referencemonitor.WithHooks(append(referencemonitor.DefaultHooks(), gate)...),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(es)),
	)
	b, err := New(rm, vlt, es, WithClock(clock.now), WithTTL(time.Minute), WithClassScopes(scopes))
	if err != nil {
		t.Fatalf("broker.New: %v", err)
	}
	return &stack{broker: b, rm: rm, es: es, vault: vlt, guest: NewMemoryGuest(), clock: clock}
}

func TestAOS331_PelaCadeia_ProvedorPassaRecursoNao(t *testing.T) {
	t.Parallel()
	// A allowlist declara o host do provedor; o pedido vai para outro.
	s := recursoStack(t, map[string][]string{provider: {hostDoProvedor}})

	req := request("run-331", provInScopeCap)
	req.Downstream.ResourceValue = "https://" + hostAlheio + "/colector"

	_, err := s.broker.Exchange(context.Background(), req)
	if err == nil {
		t.Fatal("uma troca para um recurso alheio ao provedor tinha de ser NEGADA")
	}
	// A negação chega como DeniedError do RM (o gate nega dentro da cadeia), e a razão tem de
	// nomear o eixo do recurso — não o do provedor.
	var de *DeniedError
	if !errors.As(err, &de) {
		t.Fatalf("err = %T (%v), quer *DeniedError", err, err)
	}
	if de.DeniedBy != "broker-scope" {
		t.Errorf("negacao nao atribuida ao gate: DeniedBy=%q", de.DeniedBy)
	}
	if !strings.Contains(de.Reason, ErrResourceOutOfScope.Error()) {
		t.Errorf("razao = %q, quer conter %q", de.Reason, ErrResourceOutOfScope.Error())
	}
	if strings.Contains(de.Reason, ErrProviderOutOfScope.Error()) {
		t.Error("a razao confunde o eixo do RECURSO com o do PROVEDOR")
	}
	// E nenhuma troca foi selada — o que é o ponto: a credencial não chegou a existir. A
	// NEGAÇÃO, essa, tem de estar selada.
	var negada, emitida bool
	for _, e := range readStream(t, s.es, "run-331") {
		switch e.Type {
		case referencemonitor.EventTypeDenied:
			negada = true
		case exchangeEventType:
			emitida = true
		}
	}
	if emitida {
		t.Error("uma troca foi selada apesar da negacao")
	}
	if !negada {
		t.Error("a negacao nao foi selada")
	}
}

// TestAOS331_PelaCadeia_RecursoDoProprioProvedorPassa é o CONTROLO ponta-a-ponta. Sem ele, um
// gate que negasse sempre passaria o teste acima e fecharia todas as trocas.
func TestAOS331_PelaCadeia_RecursoDoProprioProvedorPassa(t *testing.T) {
	t.Parallel()
	s := recursoStack(t, map[string][]string{provider: {hostDoProvedor}})

	req := request("run-331-ok", provInScopeCap)
	req.Downstream.ResourceValue = "https://" + hostDoProvedor + "/charges"

	if _, err := s.broker.Exchange(context.Background(), req); err != nil {
		t.Fatalf("o recurso do proprio provedor tem de passar: %v", err)
	}
	var emitida bool
	for _, e := range readStream(t, s.es, "run-331-ok") {
		if e.Type == exchangeEventType {
			emitida = true
		}
	}
	if !emitida {
		t.Error("a troca legitima nao foi selada")
	}
}
