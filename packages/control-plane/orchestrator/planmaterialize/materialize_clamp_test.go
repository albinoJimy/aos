package planmaterialize

import (
	"context"
	"math"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// capturingAdmission regista o Tokens de cada AdmitRequest (fase 1, materialize.go:316).
type capturingAdmission struct{ tokens map[string]int64 }

func (c *capturingAdmission) Admit(_ context.Context, req AdmitRequest) (AdmitVerdict, error) {
	if c.tokens == nil {
		c.tokens = map[string]int64{}
	}
	c.tokens[req.NodeID] = req.Tokens
	return AdmitVerdict{Admitted: true}, nil
}

// capturingSpawner regista o InheritedTokens de cada RoleSpawn (fase 2, materialize.go:353).
type capturingSpawner struct{ inherited map[string]int64 }

func (c *capturingSpawner) Spawn(_ context.Context, r RoleSpawn) error {
	if c.inherited == nil {
		c.inherited = map[string]int64{}
	}
	c.inherited[r.NodeID] = r.InheritedTokens
	return nil
}

// TestMaterialize_UntrustedBudgetOverflowClampsFailClosed prova que um BudgetEstimate
// UNTRUSTED do PlanDocument (proposto pelo LLM) com Tokens/CostMicroUSD >= 2^63 SATURA em
// math.MaxInt64 (positivo) na conversão para o int64 dos débitos de admissão/spawn — nunca
// vira um valor NEGATIVO. Cobre os dois pontos de conversão (admissão e RoleSpawn), ambos
// via clampU64ToInt64.
//
// Falha-antes (VERIFICÁVEL revertendo clampU64ToInt64(x) para int64(x)): int64(math.MaxUint64)
// == -1, e um orçamento gigante viraria um débito NEGATIVO na contabilidade fail-closed a
// jusante (admissão/reserva) — exactamente o defeito G115 (CWE-190) que o gate SAST apanhou.
func TestMaterialize_UntrustedBudgetOverflowClampsFailClosed(t *testing.T) {
	adm := &capturingAdmission{}
	sp := &capturingSpawner{}
	m, err := NewMaterializer(adm, &fakeLeaf{}, sp, &fakeRecorder{})
	if err != nil {
		t.Fatalf("NewMaterializer: %v", err)
	}

	// "arch" tem um dependente ("impl") ⇒ é PAPEL (passa por Admit E Spawn); orçamento
	// adversarial no máximo do uint64.
	arch := plan.Node{
		NodeID: "arch", Role: "role-arch", Objective: "obj-arch",
		Tools:          []plan.ToolRef{tool("toolA")},
		BudgetEstimate: plan.BudgetEstimate{Tokens: math.MaxUint64, CostMicroUSD: math.MaxUint64},
	}
	impl := node("impl", []plan.ToolRef{tool("toolB")}, "arch")
	req := baseReq(arch, impl)

	if _, err := m.Materialize(context.Background(), req); err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	// (1) Admissão (materialize.go:316): Tokens saturado, positivo.
	got := adm.tokens["arch"]
	if got != math.MaxInt64 {
		t.Fatalf("admissão: Tokens=%d, quero math.MaxInt64 (%d) — saturação em falta", got, int64(math.MaxInt64))
	}
	if got < 0 {
		t.Fatalf("VAZAMENTO DE OVERFLOW: Tokens de admissão NEGATIVO (%d) — o uint64 untrusted transbordou", got)
	}

	// (2) RoleSpawn (materialize.go:353): InheritedTokens saturado, positivo.
	inh := sp.inherited["arch"]
	if inh != math.MaxInt64 {
		t.Fatalf("spawn: InheritedTokens=%d, quero math.MaxInt64 (%d) — saturação em falta", inh, int64(math.MaxInt64))
	}
	if inh < 0 {
		t.Fatalf("VAZAMENTO DE OVERFLOW: InheritedTokens NEGATIVO (%d)", inh)
	}
}

// TestClampU64ToInt64_Saturates cobre o helper diretamente na fronteira.
func TestClampU64ToInt64_Saturates(t *testing.T) {
	cases := []struct {
		in   uint64
		want int64
	}{
		{0, 0},
		{42, 42},
		{math.MaxInt64, math.MaxInt64},
		{math.MaxInt64 + 1, math.MaxInt64}, // primeiro valor que transbordaria: satura
		{math.MaxUint64, math.MaxInt64},
	}
	for _, c := range cases {
		if got := clampU64ToInt64(c.in); got != c.want {
			t.Fatalf("clampU64ToInt64(%d)=%d, quero %d", c.in, got, c.want)
		}
	}
}
