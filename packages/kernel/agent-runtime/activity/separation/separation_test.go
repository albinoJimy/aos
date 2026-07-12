package separation

import (
	"strings"
	"testing"
)

// TestSeparation_CasoBom assevera que um consumidor que roteia todo o efeito por uma
// activity (Dispatcher.Dispatch) — sem primitivas de I/O directas — NÃO é sinalizado.
func TestSeparation_CasoBom(t *testing.T) {
	t.Parallel()
	violations, err := AnalyzeDir("testdata/good")
	if err != nil {
		t.Fatalf("AnalyzeDir(good): %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("caso BOM devia ter 0 violacoes, obtidas %d: %v", len(violations), violations)
	}
}

// TestSeparation_CasoMau assevera que efeitos externos directos fora de activity
// (http.Get, os.Open, exec.Command) são TODOS sinalizados, com posição utilizável.
func TestSeparation_CasoMau(t *testing.T) {
	t.Parallel()
	violations, err := AnalyzeDir("testdata/bad")
	if err != nil {
		t.Fatalf("AnalyzeDir(bad): %v", err)
	}
	if len(violations) != 3 {
		t.Fatalf("caso MAU devia sinalizar 3 violacoes, obtidas %d: %v", len(violations), violations)
	}

	want := map[string]bool{"http.Get": false, "os.Open": false, "exec.Command": false}
	for _, v := range violations {
		if v.Line == 0 || v.File == "" {
			t.Errorf("violacao sem posicao: %+v", v)
		}
		if _, ok := want[v.Call]; ok {
			want[v.Call] = true
		}
		if s := v.String(); !strings.Contains(s, ".go") {
			t.Errorf("String() malformada: %q", s)
		}
	}
	for call, seen := range want {
		if !seen {
			t.Errorf("faltou sinalizar efeito externo %q; violacoes=%v", call, violations)
		}
	}
}

// TestSeparation_EvasoesConhecidas fixa o LIMITE do lint (AOS021-Q3): as evasões
// idiomáticas — import aliasado, método (*http.Client).Do sobre valor de cliente, e
// valor de função — NÃO são apanhadas pela heurística sintáctica `pkgIdent.Fn`. O
// teste assevera 0 violações para tornar EXPLÍCITO o que a segunda camada não cobre
// (a garantia forte é estrutural; ver cabeçalho do pacote).
func TestSeparation_EvasoesConhecidas(t *testing.T) {
	t.Parallel()
	violations, err := AnalyzeDir("testdata/evasion")
	if err != nil {
		t.Fatalf("AnalyzeDir(evasion): %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("as evasões conhecidas são um limite documentado do lint (esperadas 0 violações), obtidas %d: %v", len(violations), violations)
	}
}

// TestSeparation_RuntimeLimpo corre o analisador RECURSIVAMENTE (AOS021-Q4) sobre toda
// a lógica determinística do Agent Runtime — pacote raiz (loop base, AOS-013) E os
// subpacotes do núcleo (durable, saga, state, liveness, replay, activity) — para
// garantir que NÃO há efeitos externos directos fora de activities. Salta "testdata"
// (fixtures com efeitos de propósito) e "separation" (o próprio analisador, que faz
// I/O de ficheiro por construção). O loop legítimo despacha tudo por rm.Mediate / o
// Dispatcher — nenhuma primitiva de I/O de rede/ficheiro/processo é chamada à mão.
func TestSeparation_RuntimeLimpo(t *testing.T) {
	t.Parallel()
	// Toda a árvore do agent-runtime, recursiva; "separation" é o analisador (I/O por
	// construção), não lógica de loop, pelo que é excluído.
	violations, err := AnalyzeTree("../..", "separation")
	if err != nil {
		t.Fatalf("AnalyzeTree(agent-runtime): %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("o núcleo determinístico não devia ter efeitos externos directos, obtidas %d: %v", len(violations), violations)
	}
}
