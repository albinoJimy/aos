package planapproval

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
	"github.com/aos-ref/control-plane/governance/hitl"
	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/platform/audit"
)

// fakeOracle é um [autonomy.Oracle] que devolve um nível FIXO — o gate CONSOME-o (não o
// decide). Um valor distinto por par não é necessário para os testes de plano.
type fakeOracle struct{ level autonomy.Level }

func (f fakeOracle) LevelFor(_, _ string) autonomy.Level { return f.level }

// spyChannel regista quantas vezes o gate lhe DEVOLVE a decisão binária e com que pedido
// (prova "Channel chamado" iff gatado). Responde com uma decisão canónica programada.
type spyChannel struct {
	mu       sync.Mutex
	calls    int
	seen     []risk.ConfirmationRequest
	approved bool
	approver string
	err      error
}

func (s *spyChannel) Confirm(_ context.Context, req risk.ConfirmationRequest) (risk.ConfirmationResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.seen = append(s.seen, req)
	if s.err != nil {
		return risk.ConfirmationResponse{}, s.err
	}
	return risk.ConfirmationResponse{Approved: s.approved, Approver: s.approver}, nil
}

func (s *spyChannel) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// spySpawner conta os Spawn REALMENTE efectuados (o custo de tokens). O teste pré-spawn
// prova que fica a ZERO até à aprovação do plano.
type spySpawner struct {
	mu   sync.Mutex
	runs []string
}

func (s *spySpawner) Spawn(_ context.Context, runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs = append(s.runs, runID)
	return nil
}

func (s *spySpawner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runs)
}

// fakeReviewer é uma [PlanReviewer] programável: devolve um veredicto rico fixo
// (approve/edit/reject + grafo revisto). Modela a superfície de edição do humano.
type fakeReviewer struct {
	mu   sync.Mutex
	dec  PlanDecision
	err  error
	seen []PlanCard
}

func (r *fakeReviewer) Review(_ context.Context, card PlanCard) (PlanDecision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, card)
	return r.dec, r.err
}

// samplePlan devolve um grafo de tarefas multi-nó determinista: a→b, a→c, b→d (um DAG
// diamante-parcial). A classe/irreversibilidade dos nós é parametrizável.
func samplePlan(runID string, class risk.Class) Plan {
	return Plan{
		RunID:  runID,
		Agent:  "agent:planner",
		Domain: "http",
		Nodes: []PlanNode{
			{TaskID: "a", Class: class, Preview: "cap:http.get -> https://x/a", Capability: "http.get", Resource: "https://x/a"},
			{TaskID: "b", Class: class, Preview: "cap:http.get -> https://x/b", Capability: "http.get", Resource: "https://x/b"},
			{TaskID: "c", Class: class, Preview: "cap:http.get -> https://x/c", Capability: "http.get", Resource: "https://x/c"},
			{TaskID: "d", Class: class, Preview: "cap:http.get -> https://x/d", Capability: "http.get", Resource: "https://x/d"},
		},
		Edges: [][2]string{{"a", "b"}, {"a", "c"}, {"b", "d"}},
	}
}

// --- Infra do [hitl.Channel] REAL (prova de composição + não-repúdio) --------------

func fixedClock() time.Time { return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC) }

func newNonce(t *testing.T) []byte {
	t.Helper()
	n := make([]byte, 16)
	if _, err := rand.Read(n); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return n
}

// fakeVault guarda a chave PRIVADA por aprovador e assina do seu lado (molde AOS-120).
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

type approvalStep struct {
	approver string
	approved bool
}

// seqSource devolve, por ordem, aprovações ASSINADAS ligadas ao request-id apresentado.
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

// realChannel constrói um [hitl.Channel] REAL (assina/sela/impõe 4-eyes/autoridade) com
// os aprovadores dados e a sequência de decisões programada. Devolve o audit MemStore
// para asserir a selagem (prova de que o Channel — não o plan-gate — sela e assina).
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

// containsSecret indica se algum valor de atributo do span contém a substring dada (para
// provar a AUSÊNCIA de segredos nos spans).
func attrsContain(attrs map[string]any, needle string) bool {
	for _, v := range attrs {
		if s, ok := v.(string); ok && strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
