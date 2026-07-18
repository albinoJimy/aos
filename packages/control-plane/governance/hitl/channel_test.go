package hitl

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"testing"
	"time"

	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/platform/audit"
)

// newChannel monta um Channel de teste com um aprovador "approver-1" autorizado a
// danger+gray, o solicitante "run-7" e o audit em memória. Devolve também os
// colaboradores para inspecção.
func newChannel(t *testing.T, source ApprovalSource, opts ...ChannelOption) (*Channel, *audit.MemStore, *fakeVault, *MemApproverRegistry) {
	t.Helper()
	vault := newFakeVault()
	reg := NewMemApproverRegistry()
	pub := vault.provision("approver-1", 0x11)
	reg.Register("approver-1", pub, RequiredAuthority(risk.ClassDanger), RequiredAuthority(risk.ClassGray))
	store := audit.NewMemStore()
	base := append([]ChannelOption{WithClock(fixedClock())}, opts...)
	ch, err := NewChannel(reg, source, store, base...)
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}
	return ch, store, vault, reg
}

func TestNewChannel_NilDepsFailClosed(t *testing.T) {
	t.Parallel()
	reg := NewMemApproverRegistry()
	src := scriptedSource{fn: func(context.Context, Presentation) (SignedApproval, error) { return SignedApproval{}, nil }}
	store := audit.NewMemStore()
	cases := []struct {
		name string
		reg  ApproverRegistry
		src  ApprovalSource
		st   audit.Store
	}{
		{"nil-registry", nil, src, store},
		{"nil-source", reg, nil, store},
		{"nil-sealer", reg, src, nil},
	}
	for _, tc := range cases {
		if _, err := NewChannel(tc.reg, tc.src, tc.st); err == nil {
			t.Fatalf("%s: esperava ErrNilDeps, obtive nil", tc.name)
		}
	}
}

// AC1 — escalada: uma acção danger PAUSA no gate com o PREVIEW concreto (o Confirm
// recebe o preview) e, com aprovação autorizada+assinada+4-eyes, é aprovada.
func TestConfirm_DangerEscalatesWithPreviewAndApproves(t *testing.T) {
	t.Parallel()
	var gotPreview string
	var gotClass risk.Class
	vault := newFakeVault()
	src := scriptedSource{fn: func(_ context.Context, p Presentation) (SignedApproval, error) {
		gotPreview = p.Preview
		gotClass = p.Class
		return signApprovalFor(t, vault, "approver-1", true, p), nil
	}}
	// Rebind vault to the one the source uses.
	reg := NewMemApproverRegistry()
	reg.Register("approver-1", vault.provision("approver-1", 0x11), RequiredAuthority(risk.ClassDanger))
	store := audit.NewMemStore()
	ch, err := NewChannel(reg, src, store, WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}

	resp, err := ch.Confirm(context.Background(), dangerReq("run-7", "cap:fs.delete -> /data/prod"))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if !resp.Approved {
		t.Fatalf("esperava aprovada")
	}
	if resp.Approver != "approver-1" {
		t.Fatalf("aprovador esperado approver-1, obtive %q", resp.Approver)
	}
	if gotPreview != "cap:fs.delete -> /data/prod" {
		t.Fatalf("preview concreto nao apresentado ao aprovador: %q", gotPreview)
	}
	if gotClass != risk.ClassDanger {
		t.Fatalf("classe apresentada errada: %v", gotClass)
	}
	// A aprovação foi selada no audit como allow.
	assertSealedDecision(t, store, "hitl:run-7", audit.DecisionAllow)
}

// AC3 — timeout fail-closed: o silêncio numa acção IRREVERSÍVEL NEGA (nunca permite);
// conta em Timeouts.
func TestConfirm_IrreversibleTimeoutFailClosed(t *testing.T) {
	t.Parallel()
	ch, store, _, _ := newChannel(t, blockingSource())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	resp, err := ch.Confirm(ctx, dangerReq("run-7", "cap:fs.delete -> /data/prod"))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if resp.Approved {
		t.Fatalf("FAIL-CLOSED violado: silencio numa accao irreversivel APROVOU")
	}
	_, _, _, timeouts, _ := ch.Metrics().Snapshot()
	if timeouts != 1 {
		t.Fatalf("esperava Timeouts=1, obtive %d", timeouts)
	}
	// O silêncio é selado como deny na cadeia de quarentena (sem aprovador autenticado).
	assertSealedDecision(t, store, partitionUnauth, audit.DecisionDeny)
}

// AC3 (variante) — um ctx JÁ expirado antes de apresentar nega de imediato (não chega
// a apresentar), fail-closed.
func TestConfirm_AlreadyExpiredContextDenies(t *testing.T) {
	t.Parallel()
	presented := false
	src := scriptedSource{fn: func(context.Context, Presentation) (SignedApproval, error) {
		presented = true
		return SignedApproval{}, nil
	}}
	ch, _, _, _ := newChannel(t, src)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()
	resp, _ := ch.Confirm(ctx, dangerReq("run-7", "x"))
	if resp.Approved {
		t.Fatalf("ctx expirado devia negar")
	}
	if presented {
		t.Fatalf("nao devia apresentar um pedido com ctx ja expirado")
	}
}

// AC2 — aprovador SEM autoridade: aprovação de principal autêntico mas cuja autoridade
// não cobre a classe é RECUSADA.
func TestConfirm_ApproverWithoutAuthorityRefused(t *testing.T) {
	t.Parallel()
	vault := newFakeVault()
	src := scriptedSource{fn: func(_ context.Context, p Presentation) (SignedApproval, error) {
		return signApprovalFor(t, vault, "approver-weak", true, p), nil
	}}
	reg := NewMemApproverRegistry()
	// Autêntico (chave pinada) MAS só autorizado a gray, não a danger.
	reg.Register("approver-weak", vault.provision("approver-weak", 0x22), RequiredAuthority(risk.ClassGray))
	store := audit.NewMemStore()
	ch, err := NewChannel(reg, src, store, WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}
	resp, _ := ch.Confirm(context.Background(), dangerReq("run-7", "x"))
	if resp.Approved {
		t.Fatalf("aprovador sem autoridade para danger nao devia aprovar")
	}
	assertSealedDecision(t, store, partitionUnauth, audit.DecisionDeny)
}

// AC2 — aprovador DESCONHECIDO (sem chave pinada) é recusado (fail-closed).
func TestConfirm_UnknownApproverRefused(t *testing.T) {
	t.Parallel()
	vault := newFakeVault()
	// Assina com uma chave que existe no vault mas o registo NÃO conhece o aprovador.
	vault.provision("ghost", 0x33)
	src := scriptedSource{fn: func(_ context.Context, p Presentation) (SignedApproval, error) {
		return signApprovalFor(t, vault, "ghost", true, p), nil
	}}
	reg := NewMemApproverRegistry() // vazio
	store := audit.NewMemStore()
	ch, _ := NewChannel(reg, src, store, WithClock(fixedClock()))
	resp, _ := ch.Confirm(context.Background(), dangerReq("run-7", "x"))
	if resp.Approved {
		t.Fatalf("aprovador desconhecido nao devia aprovar")
	}
}

// AC4 — não-repúdio: a aprovação assinada é SELADA e re-verificável a partir do audit
// contra a chave pública pinada.
func TestConfirm_SignedApprovalVerifiableInAudit(t *testing.T) {
	t.Parallel()
	vault := newFakeVault()
	pub := vault.provision("approver-1", 0x11)
	src := scriptedSource{fn: func(_ context.Context, p Presentation) (SignedApproval, error) {
		return signApprovalFor(t, vault, "approver-1", true, p), nil
	}}
	reg := NewMemApproverRegistry()
	reg.Register("approver-1", pub, RequiredAuthority(risk.ClassDanger))
	store := audit.NewMemStore()
	ch, _ := NewChannel(reg, src, store, WithClock(fixedClock()))

	if _, err := ch.Confirm(context.Background(), dangerReq("run-7", "x")); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	// Extrai o selo e reconstrói a decisão assinada; a assinatura tem de validar.
	rec := lastRecord(t, store, "hitl:run-7")
	sig := obligationParam(t, rec, "hitl_signature", "signature")
	nonce := obligationParam(t, rec, "hitl_signature", "nonce")
	issued := obligationParam(t, rec, "hitl_signature", "issued_at")
	reqID := obligationParam(t, rec, "hitl_decision", "request_id")

	sigB, _ := base64.StdEncoding.DecodeString(sig)
	nonceB, _ := base64.StdEncoding.DecodeString(nonce)
	issuedT, err := time.Parse(time.RFC3339Nano, issued)
	if err != nil {
		t.Fatalf("issued_at: %v", err)
	}
	reconstructed := SignedApproval{
		RequestID: reqID,
		Approver:  "approver-1",
		Approved:  true,
		Nonce:     nonceB,
		IssuedAt:  issuedT,
		Signature: sigB,
	}
	if !ed25519.Verify(pub, canonicalApproval(reconstructed), sigB) {
		t.Fatalf("nao-repudio: a assinatura selada NAO re-verifica contra a chave pinada")
	}
}

// AC4 — assinatura FORJADA (feita por outra chave) é recusada.
func TestConfirm_ForgedSignatureRefused(t *testing.T) {
	t.Parallel()
	vault := newFakeVault()
	pub := vault.provision("approver-1", 0x11) // chave legítima pinada
	// O atacante assina com OUTRA chave, clamando ser approver-1.
	attacker := newFakeVault()
	attacker.provision("approver-1", 0x99)
	src := scriptedSource{fn: func(_ context.Context, p Presentation) (SignedApproval, error) {
		return signApprovalFor(t, attacker, "approver-1", true, p), nil
	}}
	reg := NewMemApproverRegistry()
	reg.Register("approver-1", pub, RequiredAuthority(risk.ClassDanger))
	store := audit.NewMemStore()
	ch, _ := NewChannel(reg, src, store, WithClock(fixedClock()))

	resp, _ := ch.Confirm(context.Background(), dangerReq("run-7", "x"))
	if resp.Approved {
		t.Fatalf("assinatura forjada nao devia aprovar")
	}
	assertSealedDecision(t, store, partitionUnauth, audit.DecisionDeny)
}

// AC4 — uma aprovação assinada para OUTRO request-id (replay) é recusada.
func TestConfirm_ReplayedRequestIDRefused(t *testing.T) {
	t.Parallel()
	vault := newFakeVault()
	pub := vault.provision("approver-1", 0x11)
	src := scriptedSource{fn: func(ctx context.Context, p Presentation) (SignedApproval, error) {
		// Assina uma decisão ligada a um request-id DIFERENTE do apresentado.
		stale := p
		stale.RequestID = "outro-pedido"
		return signApprovalFor(t, vault, "approver-1", true, stale), nil
	}}
	reg := NewMemApproverRegistry()
	reg.Register("approver-1", pub, RequiredAuthority(risk.ClassDanger))
	store := audit.NewMemStore()
	ch, _ := NewChannel(reg, src, store, WithClock(fixedClock()))
	resp, _ := ch.Confirm(context.Background(), dangerReq("run-7", "x"))
	if resp.Approved {
		t.Fatalf("aprovacao para outro request-id (replay) nao devia aprovar")
	}
}

// AC6 dual-control — para danger, um aprovador IGUAL ao solicitante é recusado (4-eyes).
func TestConfirm_DualControlSelfApprovalRefused(t *testing.T) {
	t.Parallel()
	vault := newFakeVault()
	pub := vault.provision("run-7", 0x44) // o próprio solicitante tem chave e autoridade
	src := scriptedSource{fn: func(_ context.Context, p Presentation) (SignedApproval, error) {
		return signApprovalFor(t, vault, "run-7", true, p), nil
	}}
	reg := NewMemApproverRegistry()
	reg.Register("run-7", pub, RequiredAuthority(risk.ClassDanger))
	store := audit.NewMemStore()
	ch, _ := NewChannel(reg, src, store, WithClock(fixedClock()))
	resp, _ := ch.Confirm(context.Background(), dangerReq("run-7", "x"))
	if resp.Approved {
		t.Fatalf("4-eyes violado: auto-aprovacao de danger foi aceite")
	}
	if reason := lastReason(t, store, partitionUnauth); reason != "" {
		// self-approval é verificado (assinatura válida) mas recusado por 4-eyes: fica
		// na cadeia do solicitante, não em quarentena.
	}
	assertSealedDecision(t, store, "hitl:run-7", audit.DecisionDeny)
}

// AC6 tiering — uma acção SAFE que chegue ao canal é negada (fail-closed; safe corre
// sem gate no risk.Gate, nunca deve ser confirmada).
func TestConfirm_SafeNotGatedDenied(t *testing.T) {
	t.Parallel()
	presented := false
	src := scriptedSource{fn: func(context.Context, Presentation) (SignedApproval, error) {
		presented = true
		return SignedApproval{}, nil
	}}
	ch, _, _, _ := newChannel(t, src)
	resp, _ := ch.Confirm(context.Background(), risk.ConfirmationRequest{Class: risk.ClassSafe, Principal: "run-7"})
	if resp.Approved {
		t.Fatalf("safe nao devia ser aprovada pelo gate (nao gatavel)")
	}
	if presented {
		t.Fatalf("safe nao devia sequer ser apresentada")
	}
}

// AC6 tiering — gray (lote) confirma sem exigir 4-eyes: um aprovador autorizado a gray
// aprova o lote.
func TestConfirm_GrayBatchApprovesWithoutFourEyes(t *testing.T) {
	t.Parallel()
	vault := newFakeVault()
	pub := vault.provision("approver-1", 0x11)
	src := scriptedSource{fn: func(_ context.Context, p Presentation) (SignedApproval, error) {
		if !p.Batch {
			t.Errorf("gray devia apresentar-se como lote")
		}
		return signApprovalFor(t, vault, "approver-1", true, p), nil
	}}
	reg := NewMemApproverRegistry()
	reg.Register("approver-1", pub, RequiredAuthority(risk.ClassGray))
	store := audit.NewMemStore()
	ch, _ := NewChannel(reg, src, store, WithClock(fixedClock()))
	resp, err := ch.Confirm(context.Background(), grayReq("run-7"))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if !resp.Approved {
		t.Fatalf("gray autorizada devia aprovar em lote")
	}
}

// Recusa ASSINADA: o aprovador nega explicitamente; é não-repúdio e é selada como deny
// na cadeia do solicitante (atribuível ao aprovador), não em quarentena.
func TestConfirm_SignedRefusalSealed(t *testing.T) {
	t.Parallel()
	vault := newFakeVault()
	pub := vault.provision("approver-1", 0x11)
	src := scriptedSource{fn: func(_ context.Context, p Presentation) (SignedApproval, error) {
		return signApprovalFor(t, vault, "approver-1", false, p), nil
	}}
	reg := NewMemApproverRegistry()
	reg.Register("approver-1", pub, RequiredAuthority(risk.ClassDanger))
	store := audit.NewMemStore()
	ch, _ := NewChannel(reg, src, store, WithClock(fixedClock()))
	resp, _ := ch.Confirm(context.Background(), dangerReq("run-7", "x"))
	if resp.Approved {
		t.Fatalf("recusa assinada nao devia aprovar")
	}
	if resp.Approver != "approver-1" {
		t.Fatalf("recusa assinada devia atribuir o aprovador, obtive %q", resp.Approver)
	}
	assertSealedDecision(t, store, "hitl:run-7", audit.DecisionDeny)
}

// Selagem fail-closed: se o audit falhar o Append, a decisão (mesmo aprovada) vira DENY.
func TestConfirm_SealFailureForcesDeny(t *testing.T) {
	t.Parallel()
	vault := newFakeVault()
	pub := vault.provision("approver-1", 0x11)
	src := scriptedSource{fn: func(_ context.Context, p Presentation) (SignedApproval, error) {
		return signApprovalFor(t, vault, "approver-1", true, p), nil
	}}
	reg := NewMemApproverRegistry()
	reg.Register("approver-1", pub, RequiredAuthority(risk.ClassDanger))
	ch, err := NewChannel(reg, src, failingStore{}, WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}
	resp, _ := ch.Confirm(context.Background(), dangerReq("run-7", "x"))
	if resp.Approved {
		t.Fatalf("audit-before-effect: uma decisao nao-selavel nao pode aprovar")
	}
}

// --- helpers de audit ---

func lastRecord(t *testing.T, store *audit.MemStore, partition string) audit.AuditRecord {
	t.Helper()
	head, err := store.Head(context.Background(), partition)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head == 0 {
		t.Fatalf("particao %q vazia", partition)
	}
	rec, ok, err := store.At(context.Background(), partition, head)
	if err != nil || !ok {
		t.Fatalf("At(%d): ok=%v err=%v", head, ok, err)
	}
	return rec
}

func assertSealedDecision(t *testing.T, store *audit.MemStore, partition string, want audit.Decision) {
	t.Helper()
	rec := lastRecord(t, store, partition)
	if rec.Decision != want {
		t.Fatalf("particao %q: decisao selada = %q, esperava %q", partition, rec.Decision, want)
	}
	if len(rec.EntryHash) == 0 {
		t.Fatalf("registo selado sem EntryHash (nao encadeado)")
	}
}

func obligationParam(t *testing.T, rec audit.AuditRecord, obType, key string) string {
	t.Helper()
	for _, ob := range rec.Obligations {
		if ob.Type == obType {
			if v, ok := ob.Params[key]; ok {
				return v
			}
		}
	}
	t.Fatalf("obligation %q/%q ausente no registo", obType, key)
	return ""
}

func lastReason(t *testing.T, store *audit.MemStore, partition string) string {
	t.Helper()
	head, _ := store.Head(context.Background(), partition)
	if head == 0 {
		return ""
	}
	rec, _, _ := store.At(context.Background(), partition, head)
	for _, ob := range rec.Obligations {
		if ob.Type == "hitl_decision" {
			return ob.Params["reason"]
		}
	}
	return ""
}

// failingStore é um audit.Store que falha sempre o Append (teste do fail-closed da
// selagem).
type failingStore struct{}

func (failingStore) Append(context.Context, audit.AuditRecord) (audit.AuditRecord, error) {
	return audit.AuditRecord{}, context.DeadlineExceeded
}
func (failingStore) Head(context.Context, string) (uint64, error) { return 0, nil }
func (failingStore) At(context.Context, string, uint64) (audit.AuditRecord, bool, error) {
	return audit.AuditRecord{}, false, nil
}
func (failingStore) Read(context.Context, string, uint64, uint64) ([]audit.AuditRecord, error) {
	return nil, nil
}

// TestConfirm_FourEyesKeyedOnIntrinsicRisk: o dual-control 4-eyes segue o RISCO
// INTRÍNSECO (Irreversible/ClassDanger), NÃO o Mode do tiering. Uma acção
// IRREVERSÍVEL classificada gray (que uma política podia mapear para um modo mais
// leve) exige na mesma um aprovador distinto — um self-approval é recusado. Antes do
// endurecimento (4-eyes atado a ModeConfirm) este self-approval passava.
func TestConfirm_FourEyesKeyedOnIntrinsicRisk(t *testing.T) {
	t.Parallel()
	vault := newFakeVault()
	pub := vault.provision("run-9", 0x55)
	src := scriptedSource{fn: func(_ context.Context, p Presentation) (SignedApproval, error) {
		return signApprovalFor(t, vault, "run-9", true, p), nil
	}}
	reg := NewMemApproverRegistry()
	reg.Register("run-9", pub, RequiredAuthority(risk.ClassGray))
	store := audit.NewMemStore()
	ch, _ := NewChannel(reg, src, store, WithClock(fixedClock()))
	// gray MAS irreversível: o Mode não é ModeConfirm, mas o risco intrínseco exige 4-eyes.
	req := risk.ConfirmationRequest{Class: risk.ClassGray, Irreversible: true, Principal: "run-9", Preview: "cap:fs.delete -> /dados"}
	resp, _ := ch.Confirm(context.Background(), req)
	if resp.Approved {
		t.Fatal("4-eyes intrinseco violado: self-approval de accao IRREVERSIVEL foi aceite")
	}
}
