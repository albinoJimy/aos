package planmaterialize

import (
	"context"
	"math"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// TestMaterialize_UntrustedBudgetOverflowClampsFailClosed prova que um BudgetEstimate
// UNTRUSTED do PlanDocument (proposto pelo LLM) com Tokens/CostMicroUSD >= 2^63 SATURA em
// math.MaxInt64 (positivo) na conversão para o int64 dos débitos de ADMISSÃO — nunca vira
// um valor NEGATIVO. A conversão é clampU64ToInt64.
//
// COBERTURA (pós AOS-390, ADR-024): este teste cobria DOIS pontos de conversão — a admissão
// (AdmitRequest.Tokens/CostMicroUSD) e o RoleSpawn (InheritedTokens/InheritedCostMicroUSD). O
// SEGUNDO DEIXOU DE EXISTIR na materialização: o spawn saiu para o DispatchSink (outro
// package), que reconstrói o orçamento a partir do payload. Resta o ponto de admissão, que
// permanece coberto — e reforçado aqui para AMBOS os campos (Tokens e CostMicroUSD).
//
// Falha-antes (VERIFICÁVEL revertendo clampU64ToInt64(x) para int64(x)): int64(math.MaxUint64)
// == -1, e um orçamento gigante viraria um débito NEGATIVO na contabilidade fail-closed a
// jusante (admissão/reserva) — exactamente o defeito G115 (CWE-190) que o gate SAST apanhou.
func TestMaterialize_UntrustedBudgetOverflowClampsFailClosed(t *testing.T) {
	adm := &fakeAdmission{}
	m, err := NewMaterializer(adm, &fakeLeaf{}, &fakeRecorder{})
	if err != nil {
		t.Fatalf("NewMaterializer: %v", err)
	}

	// "arch" tem um dependente ("impl") ⇒ é PAPEL; orçamento adversarial no máximo do
	// uint64. Papel ou folha, todo o nó passa pela admissão (fase 1).
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

	// Admissão (fase 1): Tokens saturado, positivo.
	if got := adm.tokens["arch"]; got != math.MaxInt64 {
		t.Fatalf("admissão: Tokens=%d, quero math.MaxInt64 (%d) — saturação em falta", got, int64(math.MaxInt64))
	}
	if got := adm.tokens["arch"]; got < 0 {
		t.Fatalf("VAZAMENTO DE OVERFLOW: Tokens de admissão NEGATIVO (%d) — o uint64 untrusted transbordou", got)
	}
	// E o mesmo para CostMicroUSD — o outro campo untrusted que alimenta o mesmo clamp.
	if got := adm.costMicroUSD["arch"]; got != math.MaxInt64 {
		t.Fatalf("admissão: CostMicroUSD=%d, quero math.MaxInt64 (%d) — saturação em falta", got, int64(math.MaxInt64))
	}
	if got := adm.costMicroUSD["arch"]; got < 0 {
		t.Fatalf("VAZAMENTO DE OVERFLOW: CostMicroUSD de admissão NEGATIVO (%d)", got)
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
