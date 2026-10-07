package modelgateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-505 — o perfil da rota, o seu digest, os vocabulários fechados e a tradução para o runtime.

// O DIGEST DO PERFIL fica no manifesto de cada turno comparado: é o SHA-256 do JSON canónico do
// perfil, escrito aqui OUTRA VEZ à mão. Mudar um campo de um perfil muda o digest — e é isso que
// se quer ver num manifesto.
func TestAOS505_PerfilDaRota_Digest(t *testing.T) {
	for pedido, canon := range map[string]string{
		"gpt-4o-mini":     `{"requested":"gpt-4o-mini","expected_model":"openai/kimi-for-coding","wire_class":"openai-chat-completions","capabilities":["tools"]}`,
		"gpt-4o":          `{"requested":"gpt-4o","expected_model":"openai/k3","wire_class":"openai-chat-completions","capabilities":["tools"]}`,
		"kimi-for-coding": `{"requested":"kimi-for-coding","expected_model":"openai/kimi-for-coding","wire_class":"openai-chat-completions","capabilities":["tools"]}`,
		"k3":              `{"requested":"k3","expected_model":"openai/k3","wire_class":"openai-chat-completions","capabilities":["tools"]}`,
	} {
		p, ok := RouteProfileFor(pedido)
		if !ok {
			t.Errorf("falta o perfil de %q", pedido)
			continue
		}
		sum := sha256.Sum256([]byte(canon))
		if quer := "sha256:" + hex.EncodeToString(sum[:]); p.Digest() != quer {
			t.Errorf("digest do perfil de %q = %s, quero %s", pedido, p.Digest(), quer)
		}
	}
	if len(RouteProfiles()) != 4 {
		t.Errorf("a tabela tem %d perfis; este teste conhece 4 — acrescente o novo aqui", len(RouteProfiles()))
	}
	// A ordem das capacidades não muda o digest; o conteúdo muda.
	a := RouteProfile{Requested: "x", ExpectedModel: "y", WireClass: WireOpenAIChat, Capabilities: []string{"b", "a"}}
	b := RouteProfile{Requested: "x", ExpectedModel: "y", WireClass: WireOpenAIChat, Capabilities: []string{"a", "b"}}
	if a.Digest() != b.Digest() {
		t.Error("a ordem das capacidades mudou o digest")
	}
	if reflect.DeepEqual(a.Capabilities, b.Capabilities) {
		t.Error("o Digest ordenou as capacidades do proprio perfil")
	}
	b.ExpectedModel = "z"
	if a.Digest() == b.Digest() {
		t.Error("outro modelo esperado deu o mesmo digest")
	}
	if _, ok := RouteProfileFor("GPT-4o"); ok {
		t.Error("a procura do perfil tem de ser exacta")
	}
	// A cópia não deixa mexer na tabela.
	RouteProfiles()[0].ExpectedModel = "adulterado"
	if p, _ := RouteProfileFor("gpt-4o-mini"); p.ExpectedModel != "openai/kimi-for-coding" {
		t.Error("RouteProfiles devolveu a propria tabela")
	}
}

// O RÓTULO DO MODELO SERVIDO é de um conjunto fechado: os modelos esperados dos perfis, `outro`
// e `nao_reportado`. O texto que o proxy declara nunca chega a um rótulo.
func TestAOS505_RotuloDoModeloServido_ConjuntoFechado(t *testing.T) {
	quer := []string{"openai/kimi-for-coding", "openai/k3", "outro", "nao_reportado"}
	if got := ServedModelLabels(); !reflect.DeepEqual(got, quer) {
		t.Fatalf("ServedModelLabels() = %v, quero %v", got, quer)
	}
	for entra, sai := range map[string]string{
		"openai/k3": "openai/k3", "openai/kimi-for-coding": "openai/kimi-for-coding",
		"": "nao_reportado", "openai/K3": "outro", "k3": "outro", "gpt-4o": "outro",
		`nome"} com aspas`: "outro", "outro": "outro", "nao_reportado": "outro",
	} {
		if got := ServedModelLabel(entra); got != sai {
			t.Errorf("ServedModelLabel(%q) = %q, quero %q", entra, got, sai)
		}
	}
}

func TestAOS505_CompareRoute(t *testing.T) {
	p := RouteProfile{Requested: "gpt-4o", ExpectedModel: "openai/k3"}
	for _, c := range []struct {
		nome                  string
		tem                   bool
		servido, host, espera string
		check, causa          string
	}{
		{"sem perfil ganha a tudo", false, "openai/k3", "h", "h", port.RouteCheckDifferent, RouteCauseNoProfile},
		{"modelo por reportar", true, "", "h", "h", port.RouteCheckUnreported, RouteCauseModelUnreported},
		{"modelo diferente ganha ao endpoint", true, "openai/x", "", "h", port.RouteCheckDifferent, RouteCauseModelDifferent},
		{"igual sem host esperado", true, "openai/k3", "qualquer", "", port.RouteCheckEqual, ""},
		{"igual sem host esperado nem declarado", true, "openai/k3", "", "", port.RouteCheckEqual, ""},
		{"endpoint por reportar", true, "openai/k3", "", "h", port.RouteCheckUnreported, RouteCauseEndpointUnreported},
		{"endpoint diferente", true, "openai/k3", "outro", "h", port.RouteCheckDifferent, RouteCauseEndpointDifferent},
		{"igual com host", true, "openai/k3", "h", "h", port.RouteCheckEqual, ""},
		{"a comparacao do modelo e exacta", true, "openai/k3 ", "h", "h", port.RouteCheckDifferent, RouteCauseModelDifferent},
	} {
		if check, causa := CompareRoute(p, c.tem, c.servido, c.host, c.espera); check != c.check || causa != c.causa {
			t.Errorf("%s: (%s, %s), quero (%s, %s)", c.nome, check, causa, c.check, c.causa)
		}
	}
}

func TestAOS505_ParseRouteGovernance(t *testing.T) {
	for _, bom := range []string{"off", "observe", "enforce"} {
		if got, err := ParseRouteGovernance(bom); err != nil || got != bom {
			t.Errorf("%q: %q %v", bom, got, err)
		}
	}
	for _, mau := range []string{"", "OFF", "Observe", " enforce", "on", "audit"} {
		if _, err := ParseRouteGovernance(mau); !errors.Is(err, ErrBadRouteGovernance) {
			t.Errorf("%q: queria ErrBadRouteGovernance, veio %v", mau, err)
		}
	}
}

// A OPÇÃO do gateway montado à mão: desligada é inerte; um modo que não se percebe IMPÕE — nunca
// desliga a comparação em silêncio.
func TestAOS505_WithRouteGovernance_ModoIlegivelImpoe(t *testing.T) {
	for _, modo := range []string{"", RouteGovernanceOff} {
		if g := New(adapters.NewFakeAdapter("openai"), WithRouteGovernance(RouteGovernance{Mode: modo})); g.route != nil {
			t.Errorf("modo %q: a opcao tinha de ser inerte", modo)
		}
	}
	if g := New(adapters.NewFakeAdapter("openai"), WithRouteGovernance(RouteGovernance{Mode: RouteGovernanceObserve})); g.route == nil || g.route.enforce {
		t.Error("observe nao impoe")
	}
	for _, modo := range []string{RouteGovernanceEnforce, "Observe", "qualquer-coisa"} {
		g := New(adapters.NewFakeAdapter("openai"), WithRouteGovernance(RouteGovernance{Mode: modo, ExpectedAPIHost: " API.Exemplo.Test:443 "}))
		if g.route == nil || !g.route.enforce {
			t.Errorf("modo %q: tinha de impor", modo)
			continue
		}
		if g.route.expectedHost != "api.exemplo.test:443" {
			t.Errorf("o host esperado nao foi normalizado: %q", g.route.expectedHost)
		}
		// M4: o ponto final sai do esperado como sai do declarado.
		if p := New(adapters.NewFakeAdapter("openai"), WithRouteGovernance(RouteGovernance{Mode: modo, ExpectedAPIHost: "API.Exemplo.Test."})); p.route.expectedHost != "api.exemplo.test" {
			t.Errorf("o ponto final do host esperado tinha de sair: %q", p.route.expectedHost)
		}
	}
	// Um gateway montado à mão, sem recorder de governação, compara e falha na mesma — só não
	// sela. O adaptador de teste não declara rota: é `nao_reportado`.
	cs := adapters.NewStaticCredentialSource()
	cs.Set("openai", "eu", "sk-teste")
	g := New(adapters.NewFakeAdapter("openai"), WithCredentialSource(cs), WithDefaultRegion("eu"),
		WithRouteGovernance(RouteGovernance{Mode: RouteGovernanceEnforce}))
	_, err := g.Chat(context.Background(), port.ChatRequest{Model: "gpt-4o", Messages: []port.Message{{Role: port.RoleUser, Content: "x"}}})
	var rerr *RouteVarianceError
	if !errors.As(err, &rerr) || rerr.Cause != RouteCauseModelUnreported {
		t.Fatalf("queria modelo_nao_reportado, veio %v", err)
	}
}

// A TRADUÇÃO PARA O RUNTIME. Sem resultado da comparação, o modelo servido é o `model` do corpo,
// como sempre (AOS-396). Com resultado, é o que o proxy declarou — vazio se não declarou —, e
// NUNCA o do corpo nem o pedido.
func TestAOS505_TranslateResponse_ModeloServido(t *testing.T) {
	resp := respostaComUsage(port.Usage{PromptTokens: 3, CompletionTokens: 1})
	resp.Model = "gpt-4o" // o alias, carimbado pelo proxy

	resp.Route = port.ServedRoute{Model: "openai/k3"} // lido e NÃO comparado: desligada
	out, err := translateResponse(resp)
	if err != nil || out.Model != "gpt-4o" || out.RouteCheck != "" || out.RouteProfileDigest != "" {
		t.Fatalf("desligada: modelo %q check %q digest %q err %v; quero o do corpo e os dois vazios", out.Model, out.RouteCheck, out.RouteProfileDigest, err)
	}

	resp.Route = port.ServedRoute{Model: "openai/k3", Check: port.RouteCheckEqual, ProfileDigest: "sha256:abc"}
	if out, _ = translateResponse(resp); out.Model != "openai/k3" || out.RouteCheck != "igual" || out.RouteProfileDigest != "sha256:abc" {
		t.Fatalf("comparada: modelo %q check %q digest %q", out.Model, out.RouteCheck, out.RouteProfileDigest)
	}

	resp.Route = port.ServedRoute{Check: port.RouteCheckUnreported, ProfileDigest: "sha256:abc"}
	if out, _ = translateResponse(resp); out.Model != "" || out.RouteCheck != "nao_reportado" {
		t.Fatalf("nao reportado: o modelo servido tinha de ficar VAZIO, veio %q (check %q)", out.Model, out.RouteCheck)
	}

	resp.Route = port.ServedRoute{Model: "a\x00b\nc", Check: port.RouteCheckDifferent}
	if out, _ = translateResponse(resp); out.Model != "abc" {
		t.Fatalf("o modelo servido tinha de sair saneado: %q", out.Model)
	}
}

// E1/E3 — AS MARCAS DE VALOR INEXACTO ganham ao texto: um nome que só é o esperado DEPOIS de
// saneado, ou um cabeçalho repetido com valores diferentes, nunca é `igual`.
func TestAOS505_CompareServedRoute_ValorInexactoNuncaEIgual(t *testing.T) {
	p := RouteProfile{Requested: "gpt-4o", ExpectedModel: "openai/k3"}
	for _, c := range []struct {
		nome         string
		tem          bool
		servida      port.ServedRoute
		espera       string
		check, causa string
	}{
		{"exacto e igual", true, port.ServedRoute{Model: "openai/k3", APIHost: "h"}, "h", port.RouteCheckEqual, ""},
		{"modelo inexacto com o texto do esperado", true, port.ServedRoute{Model: "openai/k3", ModelInexact: true, APIHost: "h"}, "h", port.RouteCheckDifferent, RouteCauseModelDifferent},
		{"modelo inexacto sem host esperado", true, port.ServedRoute{Model: "openai/k3", ModelInexact: true}, "", port.RouteCheckDifferent, RouteCauseModelDifferent},
		{"modelo inexacto e vazio depois de saneado: declarou, nao e por reportar", true, port.ServedRoute{ModelInexact: true}, "", port.RouteCheckDifferent, RouteCauseModelDifferent},
		{"sem perfil ganha a marca", false, port.ServedRoute{Model: "openai/k3", ModelInexact: true}, "", port.RouteCheckDifferent, RouteCauseNoProfile},
		{"endpoint inexacto com o texto do esperado", true, port.ServedRoute{Model: "openai/k3", APIHost: "h", APIHostInexact: true}, "h", port.RouteCheckDifferent, RouteCauseEndpointDifferent},
		{"endpoint inexacto sem host esperado nao e comparado", true, port.ServedRoute{Model: "openai/k3", APIHost: "h", APIHostInexact: true}, "", port.RouteCheckEqual, ""},
	} {
		if check, causa := CompareServedRoute(p, c.tem, c.servida, c.espera); check != c.check || causa != c.causa {
			t.Errorf("%s: (%s, %s), quero (%s, %s)", c.nome, check, causa, c.check, c.causa)
		}
	}
}

// aos505Span guarda os atributos de um span.
type aos505Span struct{ attrs map[string]any }

func (s *aos505Span) SetAttribute(k string, v any)          { s.attrs[k] = v }
func (s *aos505Span) SpanContext() agentruntime.SpanContext { return agentruntime.SpanContext{} }
func (s *aos505Span) End()                                  {}

// aos505Selador é um selador de variância que falha ou guarda o que lhe pediram para selar.
type aos505Selador struct {
	err   error
	recs  []allowlist.GovRecord
	pares [][2]string
}

func (s *aos505Selador) SealRouteVariance(_ context.Context, rec allowlist.GovRecord, expected, served string) (audit.AuditRecord, error) {
	s.recs = append(s.recs, rec)
	s.pares = append(s.pares, [2]string{expected, served})
	return audit.AuditRecord{}, s.err
}

func aos505Governar(t *testing.T, modo string, selador routeSealer, ex *pipeline.Exchange, declarada port.ServedRoute) (port.ChatResponse, *aos505Span, error) {
	t.Helper()
	g := New(adapters.NewFakeAdapter("openai"))
	g.route = newRouteGovernor(RouteGovernance{Mode: modo}, selador)
	span := &aos505Span{attrs: map[string]any{}}
	resp := port.ChatResponse{Model: ex.RequestedModel, Route: port.ServedRoute{Model: declarada.Model}}
	err := g.governRoute(context.Background(), span, ex, &resp, declarada)
	return resp, span, err
}

// M1 (mutação RG) — O PERFIL É O DO NOME QUE FOI PEDIDO AO PROXY, isto é, o RESOLVIDO pelo
// roteamento, e não o que o chamador pediu ao gateway. Com os dois diferentes (um tier que desce
// de `gpt-4o` para `gpt-4o-mini`), o esperado é o do perfil de `gpt-4o-mini`. Escolher o perfil
// pelo nome pedido daria `diferente` a uma rota certa — e `igual` a uma errada.
func TestAOS505_GovernRoute_PerfilPeloNomeResolvido(t *testing.T) {
	pedido, _ := RouteProfileFor("gpt-4o")
	resolvido, _ := RouteProfileFor("gpt-4o-mini")
	if pedido.ExpectedModel == resolvido.ExpectedModel {
		t.Fatal("pre-condicao: os dois perfis tem de esperar modelos diferentes")
	}
	ex := &pipeline.Exchange{RequestedModel: "gpt-4o", ResolvedModel: "gpt-4o-mini", Board: "board-eu"}

	// O proxy serviu o que o perfil do nome RESOLVIDO espera: igual, com o digest desse perfil.
	selador := &aos505Selador{}
	resp, _, err := aos505Governar(t, RouteGovernanceEnforce, selador, ex, port.ServedRoute{Model: resolvido.ExpectedModel})
	if err != nil {
		t.Fatalf("a rota do nome resolvido e a do seu perfil; veio %v", err)
	}
	if quer := (port.ServedRoute{Model: resolvido.ExpectedModel, Check: port.RouteCheckEqual, ProfileDigest: resolvido.Digest()}); resp.Route != quer {
		t.Fatalf("rota = %+v\n quero %+v", resp.Route, quer)
	}
	if len(selador.recs) != 0 {
		t.Fatalf("uma rota igual nao sela: %+v", selador.recs)
	}

	// O proxy serviu o que o perfil do nome PEDIDO espera: é uma variância, selada com o nome
	// resolvido e o esperado do perfil dele.
	_, _, err = aos505Governar(t, RouteGovernanceEnforce, selador, ex, port.ServedRoute{Model: pedido.ExpectedModel})
	var rerr *RouteVarianceError
	if !errors.As(err, &rerr) || rerr.Cause != RouteCauseModelDifferent {
		t.Fatalf("o modelo do perfil do nome pedido nao e o do resolvido: queria modelo_diferente, veio %v", err)
	}
	if len(selador.recs) != 1 || selador.recs[0].Model != "gpt-4o-mini" || selador.pares[0] != [2]string{resolvido.ExpectedModel, pedido.ExpectedModel} ||
		selador.recs[0].PolicyVersion != "route-profile/"+resolvido.Digest() {
		t.Fatalf("selo = %+v pares %v; quero o nome resolvido, o esperado do seu perfil e o seu digest", selador.recs, selador.pares)
	}

	// Sem nome resolvido (um gateway sem estágio de roteamento), vale o pedido.
	resp, _, err = aos505Governar(t, RouteGovernanceEnforce, selador, &pipeline.Exchange{RequestedModel: "gpt-4o"}, port.ServedRoute{Model: pedido.ExpectedModel})
	if err != nil || resp.Route.ProfileDigest != pedido.Digest() {
		t.Fatalf("sem nome resolvido o perfil e o do pedido: digest %q err %v", resp.Route.ProfileDigest, err)
	}
}

// M1 (mutação RH) e M2 — QUANDO O SELO DA VARIÂNCIA FALHA, a falha fica no span
// (`aos.route.seal_failed`). Em observação o turno segue na mesma, com o resultado da comparação
// na resposta; em imposição o turno falha com a causa da rota e a falha do selo na mesma cadeia
// de erros. Quando o selo é escrito, ou a rota é igual, o atributo NÃO existe.
func TestAOS505_GovernRoute_FalhaDoSeloFicaNoSpan(t *testing.T) {
	falha := errors.New("worm indisponivel (ensaio)")
	ex := func() *pipeline.Exchange {
		return &pipeline.Exchange{RequestedModel: "gpt-4o", ResolvedModel: "gpt-4o"}
	}
	outro := port.ServedRoute{Model: "openai/outro"}

	resp, span, err := aos505Governar(t, RouteGovernanceObserve, &aos505Selador{err: falha}, ex(), outro)
	if err != nil {
		t.Fatalf("em observacao o turno segue mesmo sem selo; veio %v", err)
	}
	if span.attrs[attrRouteSealFailed] != true {
		t.Fatalf("a falha do selo tinha de ficar no span (%s=true); atributos: %v", attrRouteSealFailed, span.attrs)
	}
	if resp.Route.Check != port.RouteCheckDifferent || span.attrs[attrRouteCheck] != port.RouteCheckDifferent || span.attrs[attrRouteCause] != RouteCauseModelDifferent {
		t.Fatalf("o resultado da comparacao tinha de ficar na resposta e no span: %+v %v", resp.Route, span.attrs)
	}

	_, span, err = aos505Governar(t, RouteGovernanceEnforce, &aos505Selador{err: falha}, ex(), outro)
	var rerr *RouteVarianceError
	if !errors.As(err, &rerr) || rerr.Cause != RouteCauseModelDifferent || !errors.Is(err, falha) {
		t.Fatalf("em imposicao queria a causa da rota E a falha do selo na cadeia; veio %v", err)
	}
	if span.attrs[attrRouteSealFailed] != true {
		t.Fatalf("em imposicao a falha do selo tambem fica no span: %v", span.attrs)
	}
	if strings.Contains(rerr.Error(), "openai/outro") {
		t.Fatalf("a causa nao leva o texto do proxy: %v", rerr)
	}

	// Selo escrito: sem atributo. Rota igual: sem atributo, e o selador nem é chamado.
	bom := &aos505Selador{}
	if _, span, _ = aos505Governar(t, RouteGovernanceObserve, bom, ex(), outro); len(bom.recs) != 1 {
		t.Fatalf("o selador tinha de ser chamado uma vez: %d", len(bom.recs))
	}
	if _, tem := span.attrs[attrRouteSealFailed]; tem {
		t.Fatalf("selo escrito e o span diz que falhou: %v", span.attrs)
	}
	mau := &aos505Selador{err: falha}
	if _, span, err = aos505Governar(t, RouteGovernanceEnforce, mau, ex(), port.ServedRoute{Model: "openai/k3"}); err != nil || len(mau.recs) != 0 {
		t.Fatalf("uma rota igual nao chama o selador: err=%v chamadas=%d", err, len(mau.recs))
	}
	if _, tem := span.attrs[attrRouteSealFailed]; tem {
		t.Fatalf("rota igual e o span diz que o selo falhou: %v", span.attrs)
	}
}

// E1 — A DEFESA NÃO DEPENDE DO ADAPTADOR: um adaptador que entregue o nome por sanear (sem a
// marca) é apanhado pelo saneamento do próprio gateway.
func TestAOS505_GovernRoute_NomePorSanearSemMarca(t *testing.T) {
	resp, _, err := aos505Governar(t, RouteGovernanceEnforce, nil, &pipeline.Exchange{RequestedModel: "gpt-4o"}, port.ServedRoute{Model: "openai/k\u200b3\u202e"})
	var rerr *RouteVarianceError
	if !errors.As(err, &rerr) || rerr.Check != port.RouteCheckDifferent || rerr.Cause != RouteCauseModelDifferent {
		t.Fatalf("queria diferente/modelo_diferente, veio %v", err)
	}
	if resp.Route.Model != "openai/k3" || resp.Route.ModelInexact || resp.Route.APIHostInexact || resp.Route.APIHost != "" {
		t.Fatalf("grava-se o saneado, e as marcas e o host nao saem do gateway: %+v", resp.Route)
	}
}
