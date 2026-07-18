package hitl

import (
	"context"
	"testing"
	"time"

	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/platform/audit"
)

// WithIDSource, WithPartitioner e WithTiering são honrados: um id determinista
// aparece no selo, a partição custom recebe a cadeia e a política custom decide o modo.
func TestOptions_IDSourcePartitionerTiering(t *testing.T) {
	t.Parallel()
	vault := newFakeVault()
	pub := vault.provision("approver-1", 0x11)
	src := scriptedSource{fn: func(_ context.Context, p Presentation) (SignedApproval, error) {
		if p.RequestID != "fixed-id-1" {
			t.Errorf("WithIDSource nao aplicado: %q", p.RequestID)
		}
		return signApprovalFor(t, vault, "approver-1", true, p), nil
	}}
	reg := NewMemApproverRegistry()
	reg.Register("approver-1", pub, RequiredAuthority(risk.ClassDanger))
	store := audit.NewMemStore()

	custom := DefaultTieringPolicy()
	ch, err := NewChannel(reg, src, store,
		WithClock(fixedClock()),
		WithIDSource(func() string { return "fixed-id-1" }),
		WithPartitioner(func(requester string) string { return "chain/" + requester }),
		WithTiering(custom),
	)
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}
	if ch.TieringPolicyVersion() != custom.Version() {
		t.Fatalf("TieringPolicyVersion nao reflecte a politica injectada")
	}
	resp, err := ch.Confirm(context.Background(), dangerReq("run-9", "x"))
	if err != nil || !resp.Approved {
		t.Fatalf("esperava aprovada: resp=%+v err=%v", resp, err)
	}
	// A cadeia foi selada na partição custom.
	assertSealedDecision(t, store, "chain/run-9", audit.DecisionAllow)
	rec := lastRecord(t, store, "chain/run-9")
	if got := obligationParam(t, rec, "hitl_decision", "request_id"); got != "fixed-id-1" {
		t.Fatalf("request_id selado = %q, esperava fixed-id-1", got)
	}
}

// SignApproval é fail-closed: signer nil e decisão malformada devolvem erro; nunca
// produz uma assinatura sobre entrada inválida.
func TestSignApproval_FailClosed(t *testing.T) {
	t.Parallel()
	vault := newFakeVault()
	vault.provision("approver-1", 0x11)
	valid := SignedApproval{RequestID: "r1", Approver: "approver-1", Approved: true, Nonce: newNonce(t), IssuedAt: fixedClock()()}

	if _, err := SignApproval(context.Background(), nil, valid); err != ErrNilDeps {
		t.Fatalf("signer nil devia dar ErrNilDeps, deu %v", err)
	}
	short := valid
	short.Nonce = []byte("curto")
	if _, err := SignApproval(context.Background(), vault, short); err != ErrInvalidApproval {
		t.Fatalf("nonce curto devia dar ErrInvalidApproval, deu %v", err)
	}
	noID := valid
	noID.RequestID = ""
	if _, err := SignApproval(context.Background(), vault, noID); err != ErrInvalidApproval {
		t.Fatalf("request-id vazio devia dar ErrInvalidApproval, deu %v", err)
	}
	zeroTS := valid
	zeroTS.IssuedAt = time.Time{}
	if _, err := SignApproval(context.Background(), vault, zeroTS); err != ErrInvalidApproval {
		t.Fatalf("timestamp zero devia dar ErrInvalidApproval, deu %v", err)
	}
	// Aprovador sem material no vault ⇒ o erro do custodiante é propagado.
	unknown := valid
	unknown.Approver = "sem-material"
	if _, err := SignApproval(context.Background(), vault, unknown); err == nil {
		t.Fatalf("aprovador sem material devia propagar erro do custodiante")
	}
}

// verifyApproval rejeita chave/assinatura de dimensão errada (defesa antes do Verify).
func TestVerifyApproval_MalformedInputs(t *testing.T) {
	t.Parallel()
	if verifyApproval(nil, SignedApproval{Signature: make([]byte, 64)}) {
		t.Fatalf("chave publica vazia nao devia validar")
	}
	vault := newFakeVault()
	pub := vault.provision("approver-1", 0x11)
	if verifyApproval(pub, SignedApproval{Signature: []byte("curta")}) {
		t.Fatalf("assinatura de dimensao errada nao devia validar")
	}
}
