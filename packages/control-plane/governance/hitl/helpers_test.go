package hitl

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// fixedClock devolve um relógio determinista (sem sleeps reais) para os selos.
func fixedClock() func() time.Time {
	t := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

// newNonce devolve um nonce único de 16 bytes (crypto/rand) para cada decisão de
// teste, para que a assinatura não colida entre casos.
func newNonce(t *testing.T) []byte {
	t.Helper()
	n := make([]byte, approvalNonceMinLen)
	if _, err := rand.Read(n); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return n
}

// --- Signer de referência (broker/Vault em testes) ---
//
// fakeVault modela o custodiante server-side: guarda a chave PRIVADA por aprovador e
// assina do seu lado, devolvendo SÓ a assinatura. A chave privada nunca sai — o teste
// nunca a lê depois de a semear. Seeds EFÉMERAS e deterministas (nenhuma chave
// privada hard-coded).
type fakeVault struct {
	mu   sync.Mutex
	priv map[string]ed25519.PrivateKey
}

func newFakeVault() *fakeVault { return &fakeVault{priv: map[string]ed25519.PrivateKey{}} }

// provision semeia um aprovador com uma chave derivada de um seed determinista e
// devolve a chave PÚBLICA (a única que sai do custodiante).
func (f *fakeVault) provision(approver string, seed byte) ed25519.PublicKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	priv := ed25519.NewKeyFromSeed(seedBytes(seed))
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

func seedBytes(b byte) []byte {
	s := make([]byte, ed25519.SeedSize)
	for i := range s {
		s[i] = b
	}
	return s
}

// signApprovalFor constrói e assina (via o fakeVault) uma decisão ligada ao pedido p.
func signApprovalFor(t *testing.T, vault *fakeVault, approver string, approved bool, p Presentation) SignedApproval {
	t.Helper()
	a := SignedApproval{
		RequestID: p.RequestID,
		Approver:  approver,
		Approved:  approved,
		Nonce:     newNonce(t),
		IssuedAt:  fixedClock()(),
	}
	signed, err := SignApproval(context.Background(), vault, a)
	if err != nil {
		t.Fatalf("SignApproval: %v", err)
	}
	return signed
}

// --- ApprovalSource programável (transporte out-of-band em testes) ---

type scriptedSource struct {
	fn func(ctx context.Context, p Presentation) (SignedApproval, error)
}

func (s scriptedSource) Await(ctx context.Context, p Presentation) (SignedApproval, error) {
	return s.fn(ctx, p)
}

// blockingSource bloqueia até o ctx expirar/cancelar e devolve ctx.Err() — modela um
// aprovador SILENCIOSO (o caminho fail-closed do timeout), sem sleeps arbitrários.
func blockingSource() ApprovalSource {
	return scriptedSource{fn: func(ctx context.Context, _ Presentation) (SignedApproval, error) {
		<-ctx.Done()
		return SignedApproval{}, ctx.Err()
	}}
}

// --- MetricSink de captura (verifica a exposição do override-rate) ---

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

// --- Tracer de captura (verifica a emissão do span/atributos) ---

type captureSpan struct {
	mu    sync.Mutex
	name  string
	attrs map[string]any
	ended bool
}

func (s *captureSpan) SetAttribute(k string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attrs[k] = v
}

func (s *captureSpan) End() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
}

func (s *captureSpan) attr(k string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.attrs[k]
	return v, ok
}

type captureTracer struct {
	mu    sync.Mutex
	spans []*captureSpan
}

func (t *captureTracer) StartSpan(ctx context.Context, name string) (context.Context, Span) {
	s := &captureSpan{name: name, attrs: map[string]any{}}
	t.mu.Lock()
	t.spans = append(t.spans, s)
	t.mu.Unlock()
	return ctx, s
}

func (t *captureTracer) last() *captureSpan {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.spans) == 0 {
		return nil
	}
	return t.spans[len(t.spans)-1]
}

// dangerReq constrói uma ConfirmationRequest danger/irreversível para um solicitante.
func dangerReq(requester, preview string) risk.ConfirmationRequest {
	return risk.ConfirmationRequest{
		Class:        risk.ClassDanger,
		Irreversible: true,
		Preview:      preview,
		Principal:    requester,
		Capability:   "cap:fs.delete",
		Resource:     "/data/prod",
	}
}

// grayReq constrói uma ConfirmationRequest gray (lote).
func grayReq(requester string) risk.ConfirmationRequest {
	return risk.ConfirmationRequest{
		Class:      risk.ClassGray,
		Batch:      true,
		Preview:    "cap:http.get -> https://api/x",
		Principal:  requester,
		Capability: "cap:http.get",
		Resource:   "https://api/x",
	}
}
