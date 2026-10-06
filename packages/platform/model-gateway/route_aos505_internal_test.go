package modelgateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"

	"github.com/aos-ref/platform/model-gateway/internal/adapters"
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
