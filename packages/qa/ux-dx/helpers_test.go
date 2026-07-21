package uxdx_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
	"github.com/aos-ref/control-plane/governance/hitl"
	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/platform/audit"
)

// Esta bateria COMPÕE as superfícies de governação e o gate HITL REAL de AOS-095 —
// os helpers replicam a infra de assinatura das superfícies (molde: os helpers_test
// de approval-card/plan-approval/hitl) para exercitar o [hitl.Channel] REAL. Nada de
// produção é reimplementado; nenhum segredo/PII real (seeds efémeras deterministas).

// fixedClock devolve um instante determinista para os timestamps de aprovação/registo.
func fixedClock() func() time.Time {
	t := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

// newNonce devolve um nonce de 16 bytes (crypto/rand) por decisão, para as assinaturas
// não colidirem entre casos.
func newNonce(t *testing.T) []byte {
	t.Helper()
	n := make([]byte, 16)
	if _, err := rand.Read(n); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return n
}

// fakeVault modela o custodiante server-side (broker/Vault): guarda a chave PRIVADA por
// aprovador e assina do seu lado, devolvendo SÓ a assinatura. Implementa
// [messaging.Signer] estruturalmente. Seeds efémeras deterministas — nenhuma chave
// hard-coded, nenhum segredo real.
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

// scriptedSource é uma [hitl.ApprovalSource] programável: por cada apresentação devolve
// uma decisão ASSINADA (via o vault) ligada ao request-id apresentado — modela o
// transporte out-of-band do aprovador.
type scriptedSource struct {
	t        *testing.T
	vault    *fakeVault
	approver string
	approved bool
}

func (s scriptedSource) Await(ctx context.Context, p hitl.Presentation) (hitl.SignedApproval, error) {
	a := hitl.SignedApproval{
		RequestID: p.RequestID,
		Approver:  s.approver,
		Approved:  s.approved,
		Nonce:     newNonce(s.t),
		IssuedAt:  fixedClock()(),
	}
	return hitl.SignApproval(ctx, s.vault, a)
}

// captureSink CONSOME o override-rate exposto por AOS-095: regista cada
// RecordOverrideRate e cada SignalHighOverrideRate (o sinal anti-fadiga). Implementa
// [hitl.MetricSink]. NÃO recria a métrica — só observa a sua exposição.
type captureSink struct {
	mu          sync.Mutex
	lastRate    float64
	records     int
	signals     int
	signalRate  float64
	signalLimit float64
}

func (s *captureSink) RecordOverrideRate(_ context.Context, rate float64, _, _ uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastRate = rate
	s.records++
}

func (s *captureSink) SignalHighOverrideRate(_ context.Context, rate, threshold float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.signals++
	s.signalRate = rate
	s.signalLimit = threshold
}

func (s *captureSink) snapshot() (rate float64, records, signals int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRate, s.records, s.signals
}

// realChannel constrói um [hitl.Channel] REAL de AOS-095 (assina/sela/impõe 4-eyes/
// autoridade + expõe o override-rate) com um único aprovador autorizado que decide
// sempre `approved`. Devolve o canal e o sink de captura do override-rate. É a
// COMPOSIÇÃO da superfície de métricas — não uma reimplementação.
func realChannel(t *testing.T, approved bool) (*hitl.Channel, *captureSink) {
	t.Helper()
	vault := newFakeVault()
	pub := vault.provision(approverID, 0x11)
	reg := hitl.NewMemApproverRegistry()
	reg.Register(approverID, pub, hitl.RequiredAuthority(risk.ClassDanger), hitl.RequiredAuthority(risk.ClassGray))
	src := scriptedSource{t: t, vault: vault, approver: approverID, approved: approved}
	sink := &captureSink{}
	ch, err := hitl.NewChannel(reg, src, audit.NewMemStore(),
		hitl.WithClock(fixedClock()),
		hitl.WithMetricSink(sink),
		hitl.WithOverrideRateThreshold(hitl.DefaultOverrideRateThreshold),
	)
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}
	return ch, sink
}

const (
	approverID   = "approver-1"
	requesterID  = "agent:requester"
	planAgent    = "agent:planner"
	planDomain   = "http"
	uxdxRunID    = "run-ux-dx"
	uxdxTreeID   = "tree-ux-dx"
	autonomyPeer = "agent:billing"
)

// dangerReq constrói um pedido danger/irreversível sintético (sem PII/segredos reais).
// O Principal (solicitante) é DISTINTO do aprovador — o 4-eyes de danger é satisfeito.
func dangerReq(preview string) risk.ConfirmationRequest {
	return risk.ConfirmationRequest{
		Class:        risk.ClassDanger,
		Irreversible: true,
		Preview:      preview,
		Principal:    requesterID,
		Capability:   "cap:fs.delete",
		Resource:     "/data/synthetic",
	}
}

// --- Fakes de plano (AOS-121) -------------------------------------------------------

// fakeOracle é um [autonomy.Oracle] que devolve um nível FIXO — o gate CONSOME-o (não o
// decide). L0 força o gate humano (sem auto-aprovação) para exercitar o fluxo completo.
type fakeOracle struct{ level autonomy.Level }

func (f fakeOracle) LevelFor(_, _ string) autonomy.Level { return f.level }

// planSpyChannel regista o(s) pedido(s) que o [planapproval.PlanGate] lhe DEVOLVE e
// responde com uma decisão canónica programada — prova que o gate delega (não decide).
type planSpyChannel struct {
	mu       sync.Mutex
	calls    int
	approved bool
	approver string
}

func (s *planSpyChannel) Confirm(_ context.Context, _ risk.ConfirmationRequest) (risk.ConfirmationResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return risk.ConfirmationResponse{Approved: s.approved, Approver: s.approver}, nil
}

// spySpawner conta os Spawn REALMENTE efectuados (o custo de tokens): o teste pré-spawn
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

// --- Fake de degradação (AOS-123) ---------------------------------------------------

// spyDegrader prova que o timeout do prompt de exaustão DEGRADA (não morre em silêncio):
// regista a razão com que [progresssurface.ProgressSurface.OnPromptTimeout] o chama.
type spyDegrader struct {
	mu     sync.Mutex
	calls  int
	reason string
}

func (s *spyDegrader) Degrade(_ context.Context, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.reason = reason
	return nil
}

func (s *spyDegrader) snapshot() (calls int, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.reason
}
