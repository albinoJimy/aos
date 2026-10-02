package cost

// AOS-397 — a retenção por run/árvore do agregador de custo tem tecto.
//
// Medido ANTES da correcção: 10 000 runs distintos deixavam 10 000 entradas em
// `runAggs`, um mapa sem remoção. Com o tecto, o número de chaves retidas por eixo nunca
// passa de [DefaultRetainedKeys] (ou do valor de [WithRetention]), com despejo do
// menos-recentemente-usado.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
)

func observar(t *testing.T, r *Recorder, run, tree string) Reading {
	t.Helper()
	rd := r.Observe(context.Background(), nil, Sample{
		RunID: run, TreeID: tree, Tenant: "board-eu", Region: "eu", Model: "m",
		Tokens: TokenCounts{PromptTokens: 10, CompletionTokens: 5},
	})
	return rd
}

// TestAOS397_RetencaoPorOmissaoTemTecto: sem opção, 10 000 runs e árvores distintos deixam
// DefaultRetainedKeys chaves por eixo, não 10 000.
func TestAOS397_RetencaoPorOmissaoTemTecto(t *testing.T) {
	r, _, _ := newTestRecorder(t)
	const n = 10000
	for i := 0; i < n; i++ {
		if rd := observar(t, r, fmt.Sprintf("run-%d", i), fmt.Sprintf("tree-%d", i)); rd.Err != nil {
			t.Fatalf("Observe: %v", rd.Err)
		}
	}
	runs, trees := r.RetainedKeys()
	t.Logf("AOS-397 medicao depois: %d runs distintos -> %d runs e %d arvores retidos", n, runs, trees)
	if runs != DefaultRetainedKeys || trees != DefaultRetainedKeys {
		t.Fatalf("retidos runs=%d arvores=%d; o tecto e %d", runs, trees, DefaultRetainedKeys)
	}
	// O mais recente continua lá; o mais antigo foi despejado.
	if _, ok := r.CostForRun(RunKey{RunID: fmt.Sprintf("run-%d", n-1), Tenant: "board-eu"}); !ok {
		t.Fatal("o run mais recente tinha de estar retido")
	}
	if _, ok := r.CostForRun(RunKey{RunID: "run-0", Tenant: "board-eu"}); ok {
		t.Fatal("o run mais antigo tinha de ter sido despejado")
	}
}

// TestAOS397_WithRetentionMudaOTecto: o tecto configurado é respeitado; valores < 1 são
// ignorados (não existe opção sem tecto).
func TestAOS397_WithRetentionMudaOTecto(t *testing.T) {
	r, _, _ := newTestRecorder(t, WithRetention(3))
	for i := 0; i < 50; i++ {
		observar(t, r, fmt.Sprintf("run-%d", i), "")
	}
	if runs, _ := r.RetainedKeys(); runs != 3 {
		t.Fatalf("retidos=%d, quero 3", runs)
	}

	r0, _, _ := newTestRecorder(t, WithRetention(0))
	for i := 0; i < DefaultRetainedKeys+10; i++ {
		observar(t, r0, fmt.Sprintf("run-%d", i), "")
	}
	if runs, _ := r0.RetainedKeys(); runs != DefaultRetainedKeys {
		t.Fatalf("WithRetention(0) devia manter o default; retidos=%d", runs)
	}
}

// TestAOS397_RunActivoNaoEDespejado: o despejo é pelo uso, não pela chegada — um run que
// continua a ser observado sobrevive a runs mais novos e mantém o cumulativo inteiro.
func TestAOS397_RunActivoNaoEDespejado(t *testing.T) {
	r, _, _ := newTestRecorder(t, WithRetention(3))
	activo := RunKey{RunID: "activo", Tenant: "board-eu"}
	observar(t, r, "activo", "")
	observar(t, r, "b", "")
	observar(t, r, "c", "")
	observar(t, r, "activo", "") // tocado: passa a mais recente
	observar(t, r, "d", "")      // despeja b, não o activo

	got, ok := r.CostForRun(activo)
	if !ok {
		t.Fatal("o run activo foi despejado")
	}
	uma := observar(t, NewRecorder(NewCalculator(testTable(t))), "x", "").Amount
	if got.CostMicroUSD != 2*uma.CostMicroUSD {
		t.Fatalf("cumulativo do activo=%d, quero 2 chamadas (%d)", got.CostMicroUSD, 2*uma.CostMicroUSD)
	}
	if _, ok := r.CostForRun(RunKey{RunID: "b", Tenant: "board-eu"}); ok {
		t.Fatal("b era o menos recente e tinha de ter sido despejado")
	}
}

// TestAOS397_OverflowNaArvoreNaoActualizaORun: as duas somas calculam-se antes de gravar.
// Antes, o run já estava gravado quando a árvore transbordava, e os dois eixos divergiam.
func TestAOS397_OverflowNaArvoreNaoActualizaORun(t *testing.T) {
	r, _, _ := newTestRecorder(t)
	r.mu.Lock()
	r.treeAggs.Put(TreeKey{TreeID: "cheia", Tenant: "board-eu"}, Amount{Tokens: math.MaxInt64, CostMicroUSD: math.MaxInt64})
	r.mu.Unlock()

	rd := observar(t, r, "run-x", "cheia")
	if !errors.Is(rd.Err, ErrOverflow) {
		t.Fatalf("esperava ErrOverflow, veio %v", rd.Err)
	}
	if acc, ok := r.CostForRun(RunKey{RunID: "run-x", Tenant: "board-eu"}); ok {
		t.Fatalf("o run nao pode ficar actualizado quando a arvore transborda; ficou %+v", acc)
	}
}

// TestAOS397_OverflowNoRunNaoActualizaAArvore: o caso simétrico — o run transborda e a
// árvore nova não fica gravada.
func TestAOS397_OverflowNoRunNaoActualizaAArvore(t *testing.T) {
	r, _, _ := newTestRecorder(t)
	r.mu.Lock()
	r.runAggs.Put(RunKey{RunID: "cheio", Tenant: "board-eu"}, Amount{Tokens: math.MaxInt64, CostMicroUSD: math.MaxInt64})
	r.mu.Unlock()

	rd := observar(t, r, "cheio", "tree-x")
	if !errors.Is(rd.Err, ErrOverflow) {
		t.Fatalf("esperava ErrOverflow, veio %v", rd.Err)
	}
	if acc, ok := r.CostForTree(TreeKey{TreeID: "tree-x", Tenant: "board-eu"}); ok {
		t.Fatalf("a arvore nao pode ficar gravada quando o run transborda; ficou %+v", acc)
	}
}
