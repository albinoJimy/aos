package modelgateway

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-565 — O FORNECEDOR FIXADO E SELADO POR PEDIDO.
//
// O que estes testes provam:
//
//  1. o DIGEST de um perfil que não fixa o fornecedor é, byte a byte, o de hoje — contra o
//     valor LITERAL, que é o que mantém válidos os registos de entrada já assinados;
//  2. o gateway escreve a fixação no pedido se e só se o perfil RESOLVIDO a declarar, e apaga
//     o que um chamador lhe tivesse posto;
//  3. um fornecedor a montante fora da ordem declarada é uma variância de rota com causa
//     PRÓPRIA — e um fornecedor por reportar NÃO é, porque é o caso esperado hoje.

// aos565DigestsDeHoje são os digests dos quatro perfis da tabela em código, medidos ANTES de o
// campo do fornecedor existir. Não se recalculam: um digest que mude aqui invalida em silêncio
// os registos de entrada que o dono já assinou.
var aos565DigestsDeHoje = map[string]string{
	"gpt-4o-mini":     "sha256:0cbc100e54038f013a6c84867ff08510095f47dd17b2dd07f68b5dc74f090d0f",
	"gpt-4o":          "sha256:efdd13fbdba0b96a8bc83cad7e2fb3f30f7e3ce5553a4eb0f736d7ab69efba36",
	"kimi-for-coding": "sha256:8ed9035604f0339c00e51557c4e0be712126b5f7d381853483acfdd67112cd64",
	"k3":              "sha256:3047b14cc414c48103182b051c0be23b6430c0ad2d53c0b4bb9eafa822958095",
}

// aos565Perfil é um perfil candidato que FIXA o fornecedor, sobre o nome pedido de sempre.
func aos565Perfil() RouteProfile {
	return RouteProfile{
		Requested: "gpt-4o-mini", ExpectedModel: "openai/kimi-for-coding", WireClass: WireOpenAIChat,
		Capabilities: []string{CapabilityTools},
		Provider:     &port.ProviderPolicy{Order: []string{"Google"}, DataCollection: port.DataCollectionDeny},
	}
}

// TestAOS565_DigestSemDeclaracaoEODeHoje — a prova de que a assinatura do dono continua a valer.
func TestAOS565_DigestSemDeclaracaoEODeHoje(t *testing.T) {
	for _, p := range RouteProfiles() {
		quero, ok := aos565DigestsDeHoje[p.Requested]
		if !ok {
			t.Fatalf("perfil %q sem digest de referencia — a tabela em codigo mudou", p.Requested)
		}
		if got := p.Digest(); got != quero {
			t.Errorf("%s: digest = %s\n quero %s", p.Requested, got, quero)
		}
	}
	// Uma declaração VAZIA é a omissão para o digest (e é recusada pela validação, abaixo): o
	// digest não pode depender de o campo existir, só de ele declarar alguma coisa.
	base, _ := RouteProfileFor("gpt-4o-mini")
	vazia := base
	vazia.Provider = &port.ProviderPolicy{}
	if vazia.Digest() != aos565DigestsDeHoje["gpt-4o-mini"] {
		t.Fatalf("uma declaracao vazia mudou o digest: %s", vazia.Digest())
	}
	// E uma declaração a sério MUDA o digest — é o que faz de um perfil com outro fornecedor um
	// perfil diferente, coberto pela assinatura.
	if aos565Perfil().Digest() == aos565DigestsDeHoje["gpt-4o-mini"] {
		t.Fatal("um perfil que fixa o fornecedor tem de ter outro digest")
	}
	// E dois fornecedores diferentes dão digests diferentes (a ordem está debaixo da assinatura).
	outro := aos565Perfil()
	outro.Provider = &port.ProviderPolicy{Order: []string{"Anthropic"}, DataCollection: port.DataCollectionDeny}
	if outro.Digest() == aos565Perfil().Digest() {
		t.Fatal("perfis com ordens diferentes tem de ter digests diferentes")
	}
}

// TestAOS565_LeituraFechadaDoPerfil — o perfil lê-se de JSON com a chave `provider`, e a leitura
// continua FECHADA: `allow_fallbacks` não é declarável (é sempre `false` no corpo), e uma
// declaração vazia recusa.
func TestAOS565_LeituraFechadaDoPerfil(t *testing.T) {
	const base = `{"requested":"r","expected_model":"m","wire_class":"openai-chat-completions","capabilities":["tools"]`
	for nome, c := range map[string]struct {
		json string
		bom  bool
	}{
		"ordem e retencao":       {base + `,"provider":{"order":["Google"],"data_collection":"deny"}}`, true},
		"so ordem":               {base + `,"provider":{"order":["Google"]}}`, true},
		"sem provider":           {base + `}`, true},
		"allow_fallbacks":        {base + `,"provider":{"order":["Google"],"allow_fallbacks":true}}`, false},
		"provider vazio":         {base + `,"provider":{}}`, false},
		"chave desconhecida":     {base + `,"provider":{"order":["Google"],"zdr":true}}`, false},
		"ordem com objecto":      {base + `,"provider":{"order":[{"name":"Google"}]}}`, false},
		"retencao fora do vocab": {base + `,"provider":{"order":["Google"],"data_collection":"talvez"}}`, false},
	} {
		p, err := ParseRouteProfile([]byte(c.json))
		if (err == nil) != c.bom {
			t.Errorf("%s: err = %v", nome, err)
			continue
		}
		if c.bom && err == nil && nome != "sem provider" && p.Provider.IsZero() {
			t.Errorf("%s: a declaracao nao foi lida", nome)
		}
	}
}

// TestAOS565_OGatewayEscreveSeESoSeOPerfilDeclarar — a fixação vem do perfil RESOLVIDO e de mais
// lado nenhum. O que um chamador puser no campo é apagado: o fornecedor de um pedido nunca vem
// de fora do perfil assinado.
func TestAOS565_OGatewayEscreveSeESoSeOPerfilDeclarar(t *testing.T) {
	set, err := NewRouteProfileSet(aos565Perfil())
	if err != nil {
		t.Fatalf("conjunto: %v", err)
	}
	g := New(adapters.NewFakeAdapter("openai"), WithRouteProfiles(set))

	// A rota que fixa: o pedido leva a declaração do perfil, e o manifesto diz qual.
	req := port.ChatRequest{Model: "gpt-4o-mini"}
	manifesto := g.aplicarPerfilDaRota(&req)
	if req.Provider == nil || !req.Provider.Allows("Google") {
		t.Fatalf("o pedido tinha de levar a fixacao do perfil: %+v", req.Provider)
	}
	if manifesto[port.ParamKeyProvider] != "order=Google;data_collection=deny" {
		t.Fatalf("manifesto = %v", manifesto)
	}

	// Uma rota que NÃO fixa: o campo fica nil, e o que o chamador lá tinha posto é apagado.
	semFixacao := port.ChatRequest{Model: "k3", Provider: &port.ProviderPolicy{Order: []string{"OpenAI"}}}
	manifesto = g.aplicarPerfilDaRota(&semFixacao)
	if semFixacao.Provider != nil {
		t.Fatalf("o fornecedor de um chamador nao sobrevive: %+v", semFixacao.Provider)
	}
	if _, tem := manifesto[port.ParamKeyProvider]; tem {
		t.Fatalf("manifesto de uma rota sem fixacao = %v", manifesto)
	}

	// E o conjunto sabe dizer QUE rotas fixam — é daí que a composição decide ligar a sonda.
	if fixam := set.WithProvider(); len(fixam) != 1 || fixam[0] != "gpt-4o-mini" {
		t.Fatalf("WithProvider = %v", fixam)
	}
	if fixam := (*RouteProfileSet)(nil).WithProvider(); len(fixam) != 0 {
		t.Fatalf("a tabela em codigo nao fixa fornecedor nenhum: %v", fixam)
	}
}

// aos565Governar corre a governação da rota com um fornecedor a montante dado.
func aos565Governar(t *testing.T, modo string, set *RouteProfileSet, upstream string, servido string) (port.ChatResponse, []VarianceEvent, *aos505Selador, error) {
	t.Helper()
	var eventos []VarianceEvent
	selador := &aos505Selador{}
	g := New(adapters.NewFakeAdapter("openai"),
		WithRouteProfiles(set),
		WithVarianceSink(VarianceSinkFunc(func(_ context.Context, ev VarianceEvent) { eventos = append(eventos, ev) })))
	g.route = newRouteGovernor(RouteGovernance{Mode: modo}, selador)
	ex := &pipeline.Exchange{RequestedModel: "gpt-4o-mini", ResolvedModel: "gpt-4o-mini", ResolvedProvider: "openai", Board: "board-eu"}
	resp := port.ChatResponse{Model: "gpt-4o-mini", Route: port.ServedRoute{Model: servido}, UpstreamProvider: upstream}
	err := g.governRoute(context.Background(), &aos505Span{attrs: map[string]any{}}, ex, &resp, port.ServedRoute{Model: servido})
	return resp, eventos, selador, err
}

// TestAOS565_FornecedorForaDaOrdemEVariancia — a causa nova, e as três ausências que NÃO são
// variância.
func TestAOS565_FornecedorForaDaOrdemEVariancia(t *testing.T) {
	set, err := NewRouteProfileSet(aos565Perfil())
	if err != nil {
		t.Fatalf("conjunto: %v", err)
	}
	const servido = "openai/kimi-for-coding"

	// (a) O proxy encaminhou um fornecedor que não está na ordem: variância, com causa própria,
	// e em imposição o turno falha.
	resp, eventos, selador, err := aos565Governar(t, RouteGovernanceEnforce, set, "OpenAI", servido)
	var rerr *RouteVarianceError
	if !errors.As(err, &rerr) || rerr.Cause != RouteCauseUpstreamProviderOutOfOrder {
		t.Fatalf("queria %s, veio %v", RouteCauseUpstreamProviderOutOfOrder, err)
	}
	if resp.Route.Check != port.RouteCheckDifferent {
		t.Fatalf("rota = %+v", resp.Route)
	}
	if len(eventos) != 1 || eventos[0].UpstreamProvider != "OpenAI" || eventos[0].Provider != "openai" {
		t.Fatalf("evento = %+v; o fornecedor a montante tem campo PROPRIO, ao lado do adaptador", eventos)
	}
	if len(selador.recs) != 1 || selador.recs[0].UpstreamProvider != "OpenAI" {
		t.Fatalf("selo = %+v; o fornecedor efectivo tem de ficar selado", selador.recs)
	}

	// (b) O fornecedor está na ordem: igual, sem evento e sem selo.
	resp, eventos, selador, err = aos565Governar(t, RouteGovernanceEnforce, set, "Google", servido)
	if err != nil || resp.Route.Check != port.RouteCheckEqual || len(eventos) != 0 || len(selador.recs) != 0 {
		t.Fatalf("um fornecedor na ordem e igual: err=%v rota=%+v eventos=%v selos=%v", err, resp.Route, eventos, selador.recs)
	}

	// (c) O proxy NÃO declarou o fornecedor — o caso ESPERADO em produção hoje. Não é variância:
	// se fosse, toda a rota fixada falhava em imposição sem nada estar errado.
	_, eventos, _, err = aos565Governar(t, RouteGovernanceEnforce, set, port.UpstreamProviderUnreported, servido)
	if err != nil || len(eventos) != 0 {
		t.Fatalf("`nao reportado` nao e variancia: err=%v eventos=%v", err, eventos)
	}

	// (d) A sonda não correu (campo vazio): idem.
	_, _, _, err = aos565Governar(t, RouteGovernanceEnforce, set, "", servido)
	if err != nil {
		t.Fatalf("sem sonda nao ha comparacao a fazer: %v", err)
	}

	// (e) Um perfil que NÃO fixa o fornecedor não julga o que o proxy reportar.
	semFixacao, err := NewRouteProfileSet()
	if err != nil {
		t.Fatalf("conjunto: %v", err)
	}
	_, eventos, _, err = aos565Governar(t, RouteGovernanceEnforce, semFixacao, "OpenAI", servido)
	if err != nil || len(eventos) != 0 {
		t.Fatalf("sem ordem declarada nao ha nada com que comparar: err=%v eventos=%v", err, eventos)
	}
}

// TestAOS565_AGravidadeMandaNaCausa — com o MODELO errado E o fornecedor fora da ordem, a causa
// gravada é a do modelo. Se fosse a do fornecedor, um turno servido pelo modelo errado ficaria
// registado como um problema de fornecedor, e a causa mais grave perdia-se.
func TestAOS565_AGravidadeMandaNaCausa(t *testing.T) {
	set, err := NewRouteProfileSet(aos565Perfil())
	if err != nil {
		t.Fatalf("conjunto: %v", err)
	}
	_, eventos, _, err := aos565Governar(t, RouteGovernanceEnforce, set, "OpenAI", "outro/modelo")
	var rerr *RouteVarianceError
	if !errors.As(err, &rerr) || rerr.Cause != RouteCauseModelDifferent {
		t.Fatalf("queria %s, veio %v", RouteCauseModelDifferent, err)
	}
	// O fornecedor a montante viaja no evento na mesma — o que muda é a causa.
	if len(eventos) != 1 || eventos[0].UpstreamProvider != "OpenAI" {
		t.Fatalf("evento = %+v", eventos)
	}
}

// TestAOS565_EmObservacaoOTurnoSegue — a causa nova respeita o interruptor: em observação
// regista-se e o turno segue.
func TestAOS565_EmObservacaoOTurnoSegue(t *testing.T) {
	set, err := NewRouteProfileSet(aos565Perfil())
	if err != nil {
		t.Fatalf("conjunto: %v", err)
	}
	resp, eventos, selador, err := aos565Governar(t, RouteGovernanceObserve, set, "OpenAI", "openai/kimi-for-coding")
	if err != nil {
		t.Fatalf("em observacao o turno segue: %v", err)
	}
	if resp.Route.Check != port.RouteCheckDifferent || len(eventos) != 1 || len(selador.recs) != 1 {
		t.Fatalf("rota=%+v eventos=%v selos=%v", resp.Route, eventos, selador.recs)
	}
}
