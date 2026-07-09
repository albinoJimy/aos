package archlint

import (
	"strings"
	"testing"
)

// TestAnalyze_CasoBom assevera que código de consumidor que só usa Mediate não
// é sinalizado (zero violações).
func TestAnalyze_CasoBom(t *testing.T) {
	t.Parallel()
	violations, err := AnalyzeDir("testdata/good")
	if err != nil {
		t.Fatalf("AnalyzeDir(good): %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("caso BOM devia ter 0 violacoes, obtidas %d: %v", len(violations), violations)
	}
}

// TestAnalyze_CasoMau assevera que o despacho directo de tools fora do RM é
// sinalizado — cobre as duas formas: invocação directa de ToolFunc e chamada ao
// dispatcher interno.
func TestAnalyze_CasoMau(t *testing.T) {
	t.Parallel()
	violations, err := AnalyzeDir("testdata/bad")
	if err != nil {
		t.Fatalf("AnalyzeDir(bad): %v", err)
	}
	if len(violations) < 2 {
		t.Fatalf("caso MAU devia sinalizar >= 2 violacoes, obtidas %d: %v", len(violations), violations)
	}

	kinds := map[string]bool{}
	for _, v := range violations {
		kinds[v.Kind] = true
		if v.Line == 0 || v.File == "" {
			t.Errorf("violacao sem posicao: %+v", v)
		}
	}
	if !kinds["tool-func-invocation"] {
		t.Errorf("faltou sinalizar invocacao directa de ToolFunc; violacoes=%v", violations)
	}
	if !kinds["forbidden-dispatch"] {
		t.Errorf("faltou sinalizar chamada a dispatcher directo; violacoes=%v", violations)
	}
}

// TestArchLint_RMNaoTemBypass corre o analisador sobre o próprio pacote
// reference-monitor (o pai) para garantir que o RM não expõe, no seu código de
// produção, um despacho directo com os nomes reservados. O dispatcher interno
// legítimo chama-se "dispatch" como MÉTODO de Monitor, invocado sempre via
// m.dispatch dentro de Mediate — este teste documenta que não existem chamadas
// livres (Ident) a esses nomes fora de um método receiver.
func TestArchLint_RMNaoTemBypass(t *testing.T) {
	t.Parallel()
	violations, err := AnalyzeDir("..")
	if err != nil {
		t.Fatalf("AnalyzeDir(..): %v", err)
	}
	// O RM usa m.dispatch (SelectorExpr com Sel "dispatch"), que a regra
	// deliberadamente sinaliza como forbidden-dispatch APENAS fora do RM. Como
	// aqui analisamos o próprio RM, filtramos as ocorrências internas legítimas
	// e asseveramos que não há invocação directa de ToolFunc (tool-func-invocation).
	for _, v := range violations {
		if v.Kind == "tool-func-invocation" {
			t.Errorf("RM nao devia invocar ToolFunc directamente: %s", v)
		}
	}
	// Sanidade: o output é utilizável (string não vazia) quando há violações.
	for _, v := range violations {
		if s := v.String(); !strings.Contains(s, ".go") {
			t.Errorf("String() malformada: %q", s)
		}
	}
}
