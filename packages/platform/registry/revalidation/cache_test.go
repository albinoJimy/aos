package revalidation

import (
	"context"
	"testing"

	"github.com/aos-ref/platform/registry/digest"
	"github.com/aos-ref/platform/registry/domain"
)

// countingDigester conta as invocações de Digest para provar que o digest SHA-256 é
// RECALCULADO a cada chamada — nunca substituído por um valor cacheado a partir de um
// discriminador não-criptográfico que um adversário (que controla o backing store)
// pudesse colidir para mascarar um drift.
type countingDigester struct {
	inner domain.Digester
	calls int
}

func (d *countingDigester) Digest(kind domain.ArtifactKind, c domain.Contract) string {
	d.calls++
	return d.inner.Digest(kind, c)
}

// TestRevalidate_DigestRecomputedEveryCall: a revalidação recalcula SEMPRE o SHA-256
// da definição actual (sem cache que um fingerprint colidível pudesse mascarar). N
// chamadas com a mesma definição produzem N recálculos — o passo DIGEST liga sempre
// os bytes reais à expectativa.
func TestRevalidate_DigestRecomputedEveryCall(t *testing.T) {
	t.Parallel()
	pub := newSigner(t, "pub:test", 7)
	trust := newTrust(t, pub)
	v1 := ver(1, 0, 0)
	entry := signedEntry("tool.http", v1, domain.KindTool, contractWith("v1", domain.EgressInternal), pub)
	frozen := freeze(t, "run-1", entry)

	cd := &countingDigester{inner: digest.SHA256Digester{}}
	h := newHarness(t, trust, WithDigester(cd))
	req := Request{RunID: "run-1", StepID: "s1", ToolID: "tool.http", Current: entry, Frozen: frozen, Policy: Policy{MaxEgress: domain.EgressExternal}}

	const n = 10
	for i := 0; i < n; i++ {
		if dec, _ := h.rv.Revalidate(context.Background(), req); !dec.Allowed {
			t.Fatalf("iteração %d bloqueada: %+v", i, dec)
		}
	}
	if cd.calls != n {
		t.Fatalf("digester chamado %d vezes, quer %d (o SHA-256 tem de ser recalculado a cada chamada, sem cache que mascare drift)", cd.calls, n)
	}
}

// TestRevalidate_DriftAlwaysDetected: uma primeira chamada íntegra passa; se o backing
// store MUTAR o schema (mesmo id/version, contrato diferente), a chamada seguinte
// recalcula o digest da definição mutada, diverge e BLOQUEIA — sem cache a reutilizar
// o digest íntegro. Ao voltar a definição íntegra, volta a passar.
func TestRevalidate_DriftAlwaysDetected(t *testing.T) {
	t.Parallel()
	pub := newSigner(t, "pub:test", 7)
	trust := newTrust(t, pub)
	v1 := ver(1, 0, 0)
	frozenEntry := signedEntry("tool.http", v1, domain.KindTool, contractWith("v1", domain.EgressInternal), pub)
	frozen := freeze(t, "run-1", frozenEntry)

	cd := &countingDigester{inner: digest.SHA256Digester{}}
	h := newHarness(t, trust, WithDigester(cd))

	// 1) chamada íntegra passa.
	okReq := Request{RunID: "run-1", StepID: "s1", ToolID: "tool.http", Current: frozenEntry, Frozen: frozen, Policy: Policy{MaxEgress: domain.EgressExternal}}
	if dec, _ := h.rv.Revalidate(context.Background(), okReq); !dec.Allowed {
		t.Fatalf("chamada íntegra bloqueada: %+v", dec)
	}
	callsAfterOK := cd.calls

	// 2) o backing store MUTA o schema (mesmo id/version) → digest recalculado diverge
	//    → BLOQUEIO. O digest íntegro anterior NÃO é reutilizado.
	mutated := signedEntry("tool.http", v1, domain.KindTool, contractWith("MUTATED", domain.EgressInternal), pub)
	driftReq := okReq
	driftReq.Current = mutated
	dec, _ := h.rv.Revalidate(context.Background(), driftReq)
	if dec.Allowed || dec.Reason != ReasonDigestMismatch {
		t.Fatalf("drift não bloqueado: %+v", dec)
	}
	if cd.calls <= callsAfterOK {
		t.Fatalf("drift não forçou recálculo: calls=%d (após OK=%d)", cd.calls, callsAfterOK)
	}

	// 3) regresso à definição íntegra volta a passar.
	if dec2, _ := h.rv.Revalidate(context.Background(), okReq); !dec2.Allowed {
		t.Fatalf("regresso à definição íntegra bloqueado: %+v", dec2)
	}
}

// TestRevalidate_PerRunIsolationNoLeak: a revalidação de um run nunca reutiliza o
// veredicto/digest de outro. Dois runs com a MESMA tool mas a mesma definição íntegra
// passam de forma independente; o recálculo por chamada garante que nada de um run
// contamina o outro.
func TestRevalidate_PerRunIsolationNoLeak(t *testing.T) {
	t.Parallel()
	pub := newSigner(t, "pub:test", 7)
	trust := newTrust(t, pub)
	v1 := ver(1, 0, 0)
	entry := signedEntry("tool.http", v1, domain.KindTool, contractWith("v1", domain.EgressInternal), pub)
	frozen1 := freeze(t, "run-1", entry)
	frozen2 := freeze(t, "run-2", entry)
	h := newHarness(t, trust)

	if dec, _ := h.rv.Revalidate(context.Background(), Request{RunID: "run-1", StepID: "s1", ToolID: "tool.http", Current: entry, Frozen: frozen1, Policy: Policy{MaxEgress: domain.EgressExternal}}); !dec.Allowed {
		t.Fatalf("run-1 bloqueado: %+v", dec)
	}
	if dec, _ := h.rv.Revalidate(context.Background(), Request{RunID: "run-2", StepID: "s1", ToolID: "tool.http", Current: entry, Frozen: frozen2, Policy: Policy{MaxEgress: domain.EgressExternal}}); !dec.Allowed {
		t.Fatalf("run-2 bloqueado: %+v", dec)
	}
}
