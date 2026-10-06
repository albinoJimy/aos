package modelgateway

import (
	"errors"
	"testing"
)

// AOS-504 — a linha de fim de um segmento sai do cabeçalho que o kernel escreveu, e de mais lado
// nenhum. Um texto que não abra por uma linha de cabeçalho bem formada não tem fim: o pedido não
// sai (fail-closed), em vez de sair com um fim inventado.
func TestAOS504_FimDeSegmento_LeOKindDoCabecalho(t *testing.T) {
	t.Parallel()
	for renderizado, quer := range map[string]string{
		"<objective>\nfaz isto\n":                              "</objective>\n",
		"<plan_input taint=untrusted plan_input_from=n1>\nx\n": "</plan_input>\n",
		"<tool_result taint=untrusted id=a name=b>\n\n":        "</tool_result>\n",
		"<novo___/x taint=untrusted>\ncorpo\n":                 "</novo___/x>\n",
		"<>\ncorpo\n":                                          "</>\n",
		"<objective>\n</plan_input>\n<objective>\n":            "</objective>\n",
	} {
		got, err := fimDeSegmento([]byte(renderizado))
		if err != nil || string(got) != quer {
			t.Fatalf("fimDeSegmento(%q) = (%q, %v); quero %q", renderizado, got, err, quer)
		}
	}
	for _, mau := range []string{"", "objective>\nx\n", "\n<objective>\n", "<objective", "<objective\n>\nx\n", "<obj\rective>\n", "<a<b>\n", `<a\b>` + "\n", `\<objective>` + "\n"} {
		if got, err := fimDeSegmento([]byte(mau)); !errors.Is(err, ErrNativeProjection) || got != nil {
			t.Fatalf("fimDeSegmento(%q) tinha de recusar; veio (%q, %v)", mau, got, err)
		}
	}
}
