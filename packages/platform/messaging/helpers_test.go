package messaging

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"sync"

	"github.com/aos-ref/platform/audit"
)

// newNonce devolve um nonce único de 16 bytes (crypto/rand) para os testes: cada
// mensagem tem o seu, para que a dedup anti-replay não colida entre casos.
func newNonce(t interface{ Fatalf(string, ...any) }) []byte {
	n := make([]byte, nonceMinLen)
	if _, err := rand.Read(n); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return n
}

// --- Tracer de captura (verifica a emissão do span [OpMessageVerify]) ---

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

// --- Signer de referência (broker/Vault em testes) ---
//
// fakeVault modela o custodiante server-side: guarda a chave PRIVADA por NHI e
// assina do seu lado, devolvendo SÓ a assinatura. A chave privada nunca sai — o
// teste nunca a lê depois de a semear. Seeds EFÉMERAS e deterministas (nenhuma
// chave privada hard-coded).
type fakeVault struct {
	priv map[string]ed25519.PrivateKey
}

func newFakeVault() *fakeVault { return &fakeVault{priv: map[string]ed25519.PrivateKey{}} }

// provision semeia uma NHI com uma chave derivada de um seed determinista e
// devolve a chave PÚBLICA (a única que sai do custodiante).
func (f *fakeVault) provision(nhi string, seed byte) ed25519.PublicKey {
	priv := ed25519.NewKeyFromSeed(seedBytes(seed))
	f.priv[nhi] = priv
	return priv.Public().(ed25519.PublicKey)
}

func (f *fakeVault) Sign(_ context.Context, nhi string, message []byte) ([]byte, error) {
	priv, ok := f.priv[nhi]
	if !ok {
		return nil, errors.New("fakeVault: sem material para a NHI")
	}
	return ed25519.Sign(priv, message), nil
}

// seedBytes devolve um seed ed25519 de 32 bytes preenchido com b (determinista).
func seedBytes(b byte) []byte {
	s := make([]byte, ed25519.SeedSize)
	for i := range s {
		s[i] = b
	}
	return s
}

// --- NHIRegistry de referência (identidade em testes) ---

type regEntry struct {
	pub       ed25519.PublicKey
	authority []string
}

type fakeRegistry struct {
	entries map[string]regEntry
	err     error // se != nil, Lookup falha (backend indisponível)
}

func newFakeRegistry() *fakeRegistry { return &fakeRegistry{entries: map[string]regEntry{}} }

func (r *fakeRegistry) put(nhi string, pub ed25519.PublicKey, authority ...string) {
	r.entries[nhi] = regEntry{pub: pub, authority: authority}
}

func (r *fakeRegistry) Lookup(_ context.Context, nhi string) (ed25519.PublicKey, []string, bool, error) {
	if r.err != nil {
		return nil, nil, false, r.err
	}
	e, ok := r.entries[nhi]
	if !ok {
		return nil, nil, false, nil
	}
	return e.pub, e.authority, true, nil
}

// --- ReferenceResolver de referência ---

type fakeRefs struct {
	items map[string][]byte // id → hash de conteúdo autêntico
	err   error
}

func newFakeRefs() *fakeRefs { return &fakeRefs{items: map[string][]byte{}} }

// put regista um item autêntico e devolve o seu hash de conteúdo.
func (r *fakeRefs) put(id string, content []byte) []byte {
	h := sha256.Sum256(content)
	r.items[id] = h[:]
	return h[:]
}

func (r *fakeRefs) Resolve(_ context.Context, id string) ([]byte, bool, error) {
	if r.err != nil {
		return nil, false, r.err
	}
	h, ok := r.items[id]
	if !ok {
		return nil, false, nil
	}
	return h, true, nil
}

// --- Store de audit que falha sempre no Append (teste de fail-closed da selagem) ---

type failingStore struct{}

func (failingStore) Append(context.Context, audit.AuditRecord) (audit.AuditRecord, error) {
	return audit.AuditRecord{}, errors.New("failingStore: append indisponivel")
}
func (failingStore) Head(context.Context, string) (uint64, error) { return 0, nil }
func (failingStore) At(context.Context, string, uint64) (audit.AuditRecord, bool, error) {
	return audit.AuditRecord{}, false, nil
}
func (failingStore) Read(context.Context, string, uint64, uint64) ([]audit.AuditRecord, error) {
	return nil, nil
}

// contentHash é o hash de conteúdo de um item de referência (para os testes
// construírem uma Reference autêntica).
func contentHash(content []byte) []byte {
	h := sha256.Sum256(content)
	return h[:]
}
