package replay

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-214 — REPLAY SOBERANO DE CONTEÚDO SELADO (lado do LEITOR), ao nível do módulo replay.
// Prova, NÃO-VACUOSAMENTE e nos DOIS sentidos, que a reconstrução do lado do leitor de um run cujo
// conteúdo foi cifrado por-titular (AOS-093):
//   - SEM opener/accessor autorizado ⇒ ErrPayloadAccessDenied (nunca o texto em claro);
//   - COM o opener por-titular ATRÁS do gate (accessor com o escopo soberano) ⇒ o CONTEÚDO REAL
//     DECIFRADO;
//   - COM opener mas accessor SEM o escopo ⇒ ErrPayloadAccessDenied (a âncora de autorização);
//   - depois do crypto-shredding (KEK destruída) ⇒ o erro de decifração propaga-se, MESMO ao leitor
//     autorizado — o shred aguenta o replay.
// Correr SEMPRE com -race.

// errFakeDecrypt modela audit.ErrDecrypt sem cruzar a fronteira de módulo (o envelope real é
// provado em platform/audit e no nó em cmd/aos).
var errFakeDecrypt = errors.New("replay-test: decifragem falhou (KEK destruida)")

// fakeSubjectCipher é um [agentruntime.ContentCipher] de teste: cifra/decifra por chave
// POR-TITULAR (XOR determinístico) e permite SHRED de uma chave (torna o conteúdo desse titular
// irrecuperável — modela o crypto-shredding). Seguro para uso concorrente.
type fakeSubjectCipher struct {
	mu       sync.Mutex
	keys     map[string]byte
	shredded map[string]bool
}

func newFakeSubjectCipher() *fakeSubjectCipher {
	return &fakeSubjectCipher{keys: map[string]byte{}, shredded: map[string]bool{}}
}

func (c *fakeSubjectCipher) keyFor(subject string) byte {
	k, ok := c.keys[subject]
	if !ok {
		k = byte(len(c.keys)+1) | 0x40 // não-zero
		c.keys[subject] = k
	}
	return k
}

func (c *fakeSubjectCipher) SealContent(_ context.Context, subject, _ string, pt []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := c.keyFor(subject)
	out := make([]byte, len(pt)+1)
	out[0] = 0x7f
	for i, b := range pt {
		out[i+1] = b ^ k ^ 0x5c
	}
	return out, nil
}

func (c *fakeSubjectCipher) OpenContent(_ context.Context, subject string, sealed []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shredded[subject] {
		return nil, errFakeDecrypt // KEK destruída ⇒ irrecuperável (fail-closed)
	}
	k, ok := c.keys[subject]
	if !ok || len(sealed) == 0 || sealed[0] != 0x7f {
		return nil, errFakeDecrypt
	}
	out := make([]byte, len(sealed)-1)
	for i, b := range sealed[1:] {
		out[i] = b ^ k ^ 0x5c
	}
	return out, nil
}

func (c *fakeSubjectCipher) shred(subject string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shredded[subject] = true
}

var _ agentruntime.ContentCipher = (*fakeSubjectCipher)(nil)

// sealedSubjectCapture devolve uma captura com PII sintética no texto do modelo e no output da tool,
// sob o titular dado.
func sealedSubjectCapture(subject, runID string) agentruntime.TurnCapture {
	tc := sampleTurnCapture()
	tc.RunID = runID
	tc.Subject = subject
	tc.Response.Text = "REPLAY-SINTETICO: dado ZORG-214-SECRET"
	tc.ToolResults[0].Result = agentruntime.Untrusted([]byte("TOOL-OUT-214-SECRET"))
	return tc
}

// storeWithSealedRun grava, no Event Store real, um run com conteúdo cifrado por-titular via o
// capturer REAL (WithContentSealer). Devolve o store para o motor reconstruir.
func storeWithSealedRun(t *testing.T, cipher agentruntime.ContentCipher, subject, runID string) *eventstore.Store {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cap, err := NewCapturer(store, WithContentSealer(cipher), WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	if err := cap.Capture(context.Background(), sealedSubjectCapture(subject, runID)); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	return store
}

// authorizedAccessor é o accessor do leitor autorizado (detém o escopo soberano de conteúdo).
func authorizedAccessor() Accessor {
	return Accessor{Principal: "nhi:reader-214", Scopes: []string{DefaultSovereignContentScope}}
}

// TestReconstruct_NoOpener_FailClosed prova que reconstruir um run selado SEM opener composto é
// NEGADO fail-closed (ErrPayloadAccessDenied) — o leitor não autorizado nunca obtém o claro.
func TestReconstruct_NoOpener_FailClosed(t *testing.T) {
	cipher := newFakeSubjectCipher()
	const subject, runID = "nhi:agent-214", "run-214-noopener"
	store := storeWithSealedRun(t, cipher, subject, runID)

	e, err := NewEngine(store) // SEM WithContentOpener ⇒ sem gate autorizado
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	_, err = e.Reconstruct(context.Background(), runID)
	if !errors.Is(err, ErrPayloadAccessDenied) {
		t.Fatalf("sem opener a reconstrução devia ser NEGADA (ErrPayloadAccessDenied), deu: %v", err)
	}
}

// TestReconstruct_UnauthorizedAccessor_FailClosed prova a ÂNCORA de autorização: um opener composto
// mas com um accessor SEM o escopo soberano é NEGADO (ErrPayloadAccessDenied) — o gate não é vácuo.
func TestReconstruct_UnauthorizedAccessor_FailClosed(t *testing.T) {
	cipher := newFakeSubjectCipher()
	const subject, runID = "nhi:agent-214", "run-214-badscope"
	store := storeWithSealedRun(t, cipher, subject, runID)

	badAccessor := Accessor{Principal: "nhi:reader-214", Scopes: []string{"payload:read"}} // escopo errado
	e, err := NewEngine(store, WithContentOpener(cipher, badAccessor))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if _, err := e.Reconstruct(context.Background(), runID); !errors.Is(err, ErrPayloadAccessDenied) {
		t.Fatalf("accessor sem escopo soberano devia ser NEGADO, deu: %v", err)
	}
}

// TestReconstruct_Authorized_DecryptsContent prova o PERMIT: um leitor autorizado (opener + accessor
// com o escopo soberano) obtém o CONTEÚDO REAL DECIFRADO — a resposta do modelo e o output da tool.
func TestReconstruct_Authorized_DecryptsContent(t *testing.T) {
	cipher := newFakeSubjectCipher()
	const subject, runID = "nhi:agent-214", "run-214-ok"
	store := storeWithSealedRun(t, cipher, subject, runID)

	e, err := NewEngine(store, WithContentOpener(cipher, authorizedAccessor()))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	turns, err := e.Reconstruct(context.Background(), runID)
	if err != nil {
		t.Fatalf("reconstrução autorizada devia suceder: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("esperava 1 turno reconstruído, obtive %d", len(turns))
	}
	if turns[0].Response.Text != "REPLAY-SINTETICO: dado ZORG-214-SECRET" {
		t.Fatalf("texto decifrado errado: %q", turns[0].Response.Text)
	}
	if len(turns[0].ToolResults) != 1 || !bytes.Contains(turns[0].ToolResults[0].Value, []byte("TOOL-OUT-214-SECRET")) {
		t.Fatalf("output de tool decifrado ausente: %+v", turns[0].ToolResults)
	}
	if turns[0].StepID != "step-000001" {
		t.Fatalf("step_id reconstruído errado: %q", turns[0].StepID)
	}
}

// TestReconstruct_AfterShred_FailsEvenAuthorized prova que o SHRED AGUENTA O REPLAY: depois de a KEK
// do titular ser destruída, MESMO o leitor autorizado obtém o erro de decifração — o replay não
// ressuscita o que a erasure apagou.
func TestReconstruct_AfterShred_FailsEvenAuthorized(t *testing.T) {
	cipher := newFakeSubjectCipher()
	const subject, runID = "nhi:agent-214", "run-214-shred"
	store := storeWithSealedRun(t, cipher, subject, runID)

	e, err := NewEngine(store, WithContentOpener(cipher, authorizedAccessor()))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	// Não-vácuo: ANTES do shred, o autorizado decifra.
	if _, err := e.Reconstruct(context.Background(), runID); err != nil {
		t.Fatalf("antes do shred a reconstrução autorizada devia suceder: %v", err)
	}
	// Crypto-shredding: destrói a KEK do titular.
	cipher.shred(subject)
	// Depois do shred, MESMO o autorizado falha (nunca claro).
	turns, err := e.Reconstruct(context.Background(), runID)
	if !errors.Is(err, errFakeDecrypt) {
		t.Fatalf("depois do shred a reconstrução devia falhar na decifração, deu: %v (turns=%d)", err, len(turns))
	}
}
