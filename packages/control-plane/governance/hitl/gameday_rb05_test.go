package hitl

// gameday_rb05_test.go — GAME DAY do RB-05 (rollback de auto-modificação, ADR-012).
//
// Injecta o MODO DE FALHA REAL — uma REGRESSÃO comportamental após promoção
// (misevolution/drift): a nova versão do artefacto FALHA o eval-gate (trace-diffing
// success/unsafe-action vs baseline). Prova a MITIGAÇÃO-CHAVE sobre o RatificationGate
// REAL (AOS-096): o artefacto regredido NÃO é apresentado a ratificação nem chega a
// produção (admit=false, precondition_failed), MESMO com uma ratificação assinada
// válida em mãos — fica retido em staging/canary, tem de repassar staging→eval-gate→
// canary. A baseline (versão anterior) permanece promovível — o rollback é atómico. A
// decisão é SELADA no audit WORM tamper-evident, cuja cadeia se VERIFICA íntegra.
//
// Sem stubs: usa o gate real, o eval-gate FailClosedGate real e o audit.MemStore real
// (via newRatHarness/passingArtifact/signRatificationFor de ratification_test.go).

import (
	"context"
	"testing"

	"github.com/aos-ref/platform/audit"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// TestGameDay_RB05_RegressedArtifactBlockedRollbackToBaseline prova sinal→diagnóstico
// →mitigação do RB-05 e o selo WORM da decisão.
func TestGameDay_RB05_RegressedArtifactBlockedRollbackToBaseline(t *testing.T) {
	h := newRatHarness(t)
	ctx := context.Background()

	// BASELINE (versão anterior estável) — passa o eval-gate e o canary. É o alvo do
	// rollback atómico: continua promovível.
	baseline := passingArtifact()
	baseline.Version = "1.4.0"
	sigBaseline := signRatificationFor(t, h.vault, "ratifier", true, baseline)
	admit, err := h.gate.Ratify(ctx, baseline, sigBaseline)
	if err != nil {
		t.Fatalf("Ratify (baseline): %v", err)
	}
	if !admit {
		t.Fatal("a baseline (eval+canary OK, ratificação assinada) devia promover — é o alvo do rollback")
	}

	// MODO DE FALHA: a versão 1.5.0 REGRIDE (misevolution). O trace-diffing detecta a
	// regressão ⇒ o eval-gate reprova (EvalFail). O drift é o SINAL.
	regressed := passingArtifact()
	regressed.Version = "1.5.0"
	regressed.EvalResult.Verdict = otelgenai.EvalFail

	// Mesmo com uma ratificação assinada VÁLIDA para 1.5.0, o gate NÃO a apresenta: a
	// pré-condição (eval-gate) falha ANTES da ratificação. MITIGAÇÃO: não chega a prod.
	sigRegressed := signRatificationFor(t, h.vault, "ratifier", true, regressed)
	admit, err = h.gate.Ratify(ctx, regressed, sigRegressed)
	if err != nil {
		t.Fatalf("Ratify (regredido): %v", err)
	}
	if admit {
		t.Fatal("artefacto regredido (eval reprovado) NÃO pode chegar a produção — rollback/retenção falhou")
	}
	// O motivo é a pré-condição reprovada (repassar staging→eval-gate→canary), não uma
	// recusa de assinatura — o bloqueio é ANTES da ratificação.
	_, params, principal, ok := decisionObligation(t, h.store, partitionUnratified)
	if !ok {
		t.Fatal("a decisão de bloqueio devia estar selada no audit WORM")
	}
	if params["reason"] != ReasonPreconditionFailed {
		t.Fatalf("motivo = %q, quero %q (eval-gate reprovado)", params["reason"], ReasonPreconditionFailed)
	}
	// O bloqueio de pré-condição NEM autentica o ratificador (não se apresenta sequer).
	if principal.NHIID != "" {
		t.Fatal("bloqueio de pré-condição não deve autenticar o ratificador")
	}

	// RECUPERAÇÃO verificável: a decisão foi registada no audit WORM e a hash-chain da
	// partição de quarentena está ÍNTEGRA (tamper-evident) — o incidente é auditável e
	// reconstruível.
	recs, err := h.store.Read(ctx, partitionUnratified, 1, 1<<62)
	if err != nil {
		t.Fatalf("Read (quarentena): %v", err)
	}
	if len(recs) == 0 {
		t.Fatal("nenhum registo WORM na partição de quarentena")
	}
	if err := audit.Verify(ctx, h.store, partitionUnratified, 1, uint64(len(recs))); err != nil {
		t.Fatalf("cadeia WORM da quarentena não íntegra: %v", err)
	}
}
