package approvalcard

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/governance/hitl"
	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/platform/audit"
)

// fixedClock devolve um instante determinista para os timestamps de aprovação.
func fixedClock() time.Time { return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC) }

// newNonce devolve um nonce de 16 bytes (crypto/rand) por decisão.
func newNonce(t *testing.T) []byte {
	t.Helper()
	n := make([]byte, 16)
	if _, err := rand.Read(n); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return n
}

// fakeVault modela o custodiante server-side (broker/Vault): guarda a chave PRIVADA
// por aprovador e assina do seu lado, devolvendo SÓ a assinatura. Implementa
// [messaging.Signer]. Seeds efémeras deterministas — nenhuma chave hard-coded.
type fakeVault struct {
	mu   sync.Mutex
	priv map[string]ed25519.PrivateKey
}

func newFakeVault() *fakeVault { return &fakeVault{priv: map[string]ed25519.PrivateKey{}} }

func (f *fakeVault) provision(approver string, seed byte) ed25519.PublicKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := make([]byte, ed25519.SeedSize)
	for i := range s {
		s[i] = seed
	}
	priv := ed25519.NewKeyFromSeed(s)
	f.priv[approver] = priv
	return priv.Public().(ed25519.PublicKey)
}

func (f *fakeVault) Sign(_ context.Context, approver string, message []byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	priv, ok := f.priv[approver]
	if !ok {
		return nil, errors.New("fakeVault: sem material para o aprovador")
	}
	return ed25519.Sign(priv, message), nil
}

// approvalStep descreve o que a [seqSource] devolve numa apresentação: quem aprova e
// se aprova.
type approvalStep struct {
	approver string
	approved bool
}

// seqSource é uma [hitl.ApprovalSource] programável: devolve, por ordem, aprovações
// ASSINADAS (via o vault) ligadas ao request-id que o Channel apresenta. Modela o
// transporte out-of-band de N aprovadores distintos numa sequência de Confirm.
type seqSource struct {
	mu    sync.Mutex
	t     *testing.T
	vault *fakeVault
	steps []approvalStep
	i     int
}

func (s *seqSource) Await(ctx context.Context, p hitl.Presentation) (hitl.SignedApproval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.i >= len(s.steps) {
		return hitl.SignedApproval{}, errors.New("seqSource: sem mais passos de aprovacao")
	}
	st := s.steps[s.i]
	s.i++
	a := hitl.SignedApproval{
		RequestID: p.RequestID,
		Approver:  st.approver,
		Approved:  st.approved,
		Nonce:     newNonce(s.t),
		IssuedAt:  fixedClock(),
	}
	return hitl.SignApproval(ctx, s.vault, a)
}

// realChannel constrói um [hitl.Channel] REAL (assina/sela/impõe) com o registo de
// aprovadores dado e a sequência de aprovações programada. Devolve também o audit
// MemStore para asserir a selagem (prova de que o Channel — não o card — sela).
func realChannel(t *testing.T, steps []approvalStep, approvers map[string]byte) (*hitl.Channel, *audit.MemStore) {
	t.Helper()
	vault := newFakeVault()
	reg := hitl.NewMemApproverRegistry()
	for name, seed := range approvers {
		pub := vault.provision(name, seed)
		reg.Register(name, pub, hitl.RequiredAuthority(risk.ClassDanger), hitl.RequiredAuthority(risk.ClassGray))
	}
	store := audit.NewMemStore()
	src := &seqSource{t: t, vault: vault, steps: steps}
	ch, err := hitl.NewChannel(reg, src, store, hitl.WithClock(fixedClock))
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}
	return ch, store
}

// spyChannel regista o(s) [risk.ConfirmationRequest] que o card lhe DEVOLVE e responde
// com respostas canónicas. Prova que o card delega (não decide) e que o pedido leva o
// efeito concreto resolvido.
type spyChannel struct {
	mu    sync.Mutex
	seen  []risk.ConfirmationRequest
	resps []risk.ConfirmationResponse
	i     int
}

func (s *spyChannel) Confirm(_ context.Context, req risk.ConfirmationRequest) (risk.ConfirmationResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, req)
	if s.i >= len(s.resps) {
		return risk.ConfirmationResponse{Approved: false}, nil
	}
	r := s.resps[s.i]
	s.i++
	return r, nil
}
