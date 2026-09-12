package pdp

import (
	"context"
	"strings"
	"testing"
)

// TestAttrRefsForaDoMapa cobre o SCANNER estático da avaliação de fumo (AOS-378 alínea c)
// directamente, no pacote pdp — a garantia que recusa assinar um bundle cuja regra refira um
// atributo que o motor não mapeia em runtime (senão assina «verde» e nega tudo em produção).
//
// Os casos "fora do mapa" são as CLASSES que quatro passagens adversariais mediram: atributo de
// topo inexistente, forma de índice de chave não-mapeada, acesso encadeado sobre um escalar, e
// método de Set sobre um atributo String (só principal.authority é Set). Os casos "no mapa" são
// os controlos positivos que TÊM de assinar verde (métodos de set sobre authority, comparações
// sobre os três String, `has`, e índice de chave mapeada normalizada).
func TestAttrRefsForaDoMapa(t *testing.T) {
	foraDoMapa := []struct {
		nome  string
		regra string
	}{
		{"attr de topo em context", `permit ( principal, action, resource ) when { context.reversibility == "x" };`},
		{"attr de topo em resource", `permit ( principal, action, resource ) when { resource.type == "url" };`},
		{"attr de topo em principal", `permit ( principal, action, resource ) when { principal.board == "x" };`},
		{"indice de chave nao-mapeada", `permit ( principal, action, resource ) when { context["foo-bar"] == "x" };`},
		{"acesso encadeado sobre String", `permit ( principal, action, resource ) when { context.taint.foo == "x" };`},
		{"encadeado sobre resource.region", `permit ( principal, action, resource ) when { resource.region.foo == "x" };`},
		{"set-method sobre context.taint (String)", `permit ( principal, action, resource ) when { context.taint.contains("t") };`},
		{"set-method sobre resource.region (String)", `permit ( principal, action, resource ) when { resource.region.containsAny(["eu"]) };`},
		{"attr mau no argumento de um metodo legitimo", `permit ( principal, action, resource ) when { principal.authority.contains(context.reversibility) };`},
		{"index sobre attr mapeado", `permit ( principal, action, resource ) when { context.taint["x"] == "y" };`},
	}
	for _, c := range foraDoMapa {
		if fora := attrRefsForaDoMapa([]byte(c.regra)); len(fora) == 0 {
			t.Errorf("%s: esperava SINALIZADO, veio limpo — regra: %s", c.nome, c.regra)
		}
	}

	noMapa := []struct {
		nome  string
		regra string
	}{
		{"set-method sobre authority (Set)", `permit ( principal, action, resource ) when { principal.authority.contains("cap:x") };`},
		{"containsAll sobre authority", `permit ( principal, action, resource ) when { principal.authority.containsAll(["cap:x"]) };`},
		{"comparacao sobre os tres String", `permit ( principal, action, resource ) when { context.taint != "untrusted" && resource.region == "eu" && context.sensitivity == "public" };`},
		{"has sobre context", `permit ( principal, action, resource ) when { context has taint && context.taint == "x" };`},
		{"sem when", `permit ( principal, action, resource );`},
		{"string de dados com texto pontuado", `@id("regra") permit ( principal, action, resource ) when { context.taint == "context.reversibility" };`},
	}
	for _, c := range noMapa {
		if fora := attrRefsForaDoMapa([]byte(c.regra)); len(fora) != 0 {
			t.Errorf("%s: FALSO POSITIVO %v — regra: %s", c.nome, fora, c.regra)
		}
	}
}

// engineDeFumo compila um engine directamente de uma política inline (sem assinar) para exercitar
// smokeProbeRule / SmokeDecideRules no pacote pdp.
func engineDeFumo(t *testing.T, regra string) *cedarEngine {
	t.Helper()
	eng, err := newCedarEngine(map[string][]byte{"p.cedar": []byte(regra)}, "1.0.0")
	if err != nil {
		t.Fatalf("newCedarEngine: %v", err)
	}
	return eng
}

// TestSmokeDecideRules cobre o caminho de fumo ao nível do motor e do PDP: regra sã ⇒ nil; regra
// com atributo fora do mapa ⇒ erro; engine ausente ⇒ ErrPolicyUnavailable.
func TestSmokeDecideRules(t *testing.T) {
	boa := engineDeFumo(t, `@id("ok")
permit ( principal, action == Action::"cap:http.post", resource )
when { context.taint != "untrusted" };`)
	if err := (&PDP{engine: boa}).SmokeDecideRules(context.Background()); err != nil {
		t.Fatalf("regra sã devia passar a avaliação de fumo, veio: %v", err)
	}

	ma := engineDeFumo(t, `@id("bad")
permit ( principal, action == Action::"cap:http.post", resource )
when { context.reversibility == "x" };`)
	err := (&PDP{engine: ma}).SmokeDecideRules(context.Background())
	if err == nil {
		t.Fatal("regra com atributo fora do mapa devia falhar a avaliação de fumo")
	}
	if !strings.Contains(err.Error(), "context.reversibility") {
		t.Errorf("o erro devia nomear o atributo mau, veio: %v", err)
	}

	if err := (&PDP{}).SmokeDecideRules(context.Background()); err != ErrPolicyUnavailable {
		t.Errorf("engine ausente devia dar ErrPolicyUnavailable, veio: %v", err)
	}
}
