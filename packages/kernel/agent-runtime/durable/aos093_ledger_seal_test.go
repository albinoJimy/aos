package durable

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// AOS-093 — cifra POR-TITULAR do Result.Payload do step-ledger (WIRING). Prova que o
// payload não vai em claro para o Event Store, que o dedup/rebuild devolve o CLARO
// enquanto a KEK vive, e que após o crypto-shredding o passo deixa de ser
// reconstruível (o blob mantém-se no log; simplesmente não re-hidrata).
//
// O CIPHER real (envelope DEK/KEK de platform/audit) é provado em
// platform/audit/aos093_content_test.go — a irrecuperabilidade criptográfica REAL não
// se re-prova aqui. Este teste usa um TEST DOUBLE que simula uma KEK por-titular (um
// byte de chave) para exercer a MECÂNICA do ledger sem cruzar a fronteira de módulo
// (kernel/agent-runtime não depende de platform/audit).

// errShredded modela a KEK destruída no test double.
var errShredded = errors.New("test: KEK do titular destruída")

// fakeCipher é um TEST DOUBLE de agentruntime.ContentCipher: mapeia titular→chave e
// "cifra" por XOR (não é crypto de produção — só torna o plaintext ausente dos bytes
// selados e reversível enquanto a chave existe). shred() apaga a chave ⇒ open falha.
type fakeCipher struct {
	mu   sync.Mutex
	keys map[string]byte
}

func newFakeCipher() *fakeCipher { return &fakeCipher{keys: map[string]byte{}} }

func (c *fakeCipher) SealContent(_ context.Context, subject, _ string, pt []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k, ok := c.keys[subject]
	if !ok {
		k = byte(len(c.keys) + 1)
		c.keys[subject] = k
	}
	out := make([]byte, len(pt)+1)
	out[0] = 0x7f // marcador (evita coincidência com o 1º byte do plaintext)
	for i, b := range pt {
		out[i+1] = b ^ k ^ 0xA5
	}
	return out, nil
}

func (c *fakeCipher) OpenContent(_ context.Context, subject string, sealed []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k, ok := c.keys[subject]
	if !ok || len(sealed) == 0 {
		return nil, errShredded
	}
	pt := make([]byte, len(sealed)-1)
	for i := 1; i < len(sealed); i++ {
		pt[i-1] = sealed[i] ^ k ^ 0xA5
	}
	return pt, nil
}

func (c *fakeCipher) shred(subject string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.keys, subject)
}

const ledgerSynthPII = "resultado-sintetico: token ACME-SYNTH-42 do titular"

func TestLedger_ContentSealer_SealsPayloadAndShred(t *testing.T) {
	ctx := context.Background()
	cipher := newFakeCipher()
	const subject = "nhi:agent-ledger-synth"
	store := newStore(t)

	producer := eventstore.Producer{NHIID: subject}
	ledger, err := NewStepLedger(store, WithProducer(producer), WithContentSealer(cipher))
	if err != nil {
		t.Fatalf("NewStepLedger: %v", err)
	}

	const key = "run-ledger-1:step-1"
	res, applied, err := ledger.Apply(ctx, key, func(context.Context) (Result, error) {
		return Result{Status: "ok", Payload: []byte(ledgerSynthPII)}, nil
	})
	if err != nil || !applied {
		t.Fatalf("Apply: applied=%v err=%v", applied, err)
	}
	// O chamador recebe o CLARO (a cifra é transparente para quem aplica).
	if !bytes.Equal(res.Payload, []byte(ledgerSynthPII)) {
		t.Fatalf("Apply devia devolver o payload em claro, deu: %q", res.Payload)
	}

	// O evento persistido NÃO contém o texto-claro (foi cifrado por-titular).
	events, err := store.Read(ctx, "run-ledger-1", 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var found bool
	for _, e := range events {
		if e.Type != EventTypeLedgerApplied {
			continue
		}
		found = true
		if bytes.Contains(e.Payload, []byte(ledgerSynthPII)) {
			t.Fatal("evento do ledger contém o payload em CLARO — confidencialidade violada")
		}
		if bytes.Contains(e.Payload, []byte("ACME-SYNTH-42")) {
			t.Fatal("evento do ledger contém um fragmento do payload em claro")
		}
		rec, derr := decodeRecord(e.Payload)
		if derr != nil {
			t.Fatalf("decodeRecord: %v", derr)
		}
		if !rec.Sealed || rec.Subject != subject {
			t.Fatalf("registo devia estar Sealed com Subject=%q, deu Sealed=%v Subject=%q", subject, rec.Sealed, rec.Subject)
		}
	}
	if !found {
		t.Fatal("nenhum evento step.ledger.applied encontrado")
	}

	// REBUILD com a KEK viva: o resultado memorizado é re-hidratado EM CLARO.
	ledger2, _ := NewStepLedger(store, WithProducer(producer), WithContentSealer(cipher))
	if err := ledger2.Rebuild(ctx, "run-ledger-1"); err != nil {
		t.Fatalf("Rebuild (KEK viva): %v", err)
	}
	got, ok := ledger2.Applied(key)
	if !ok {
		t.Fatal("Rebuild devia re-hidratar o passo enquanto a KEK vive")
	}
	if !bytes.Equal(got.Payload, []byte(ledgerSynthPII)) {
		t.Fatalf("Rebuild devia devolver o claro, deu: %q", got.Payload)
	}

	// CRYPTO-SHREDDING: destrói a KEK do titular.
	cipher.shred(subject)

	// REBUILD após o shred: o passo já NÃO é reconstruível (blob intacto, sem chave).
	ledger3, _ := NewStepLedger(store, WithProducer(producer), WithContentSealer(cipher))
	if err := ledger3.Rebuild(ctx, "run-ledger-1"); err != nil {
		t.Fatalf("Rebuild (pós-shred) não devia falhar (só descarta): %v", err)
	}
	if _, ok := ledger3.Applied(key); ok {
		t.Fatal("após o shred, o passo NÃO devia ser reconstruído (conteúdo irrecuperável)")
	}
}

// TestLedger_SealedRecordWithoutCipher_FailsClosed prova que reler um store cifrado
// sem o cipher ligado é fail-closed (ErrSealedResultNoCipher) — nunca se devolve
// ciphertext como se fosse o resultado em claro.
func TestLedger_SealedRecordWithoutCipher_FailsClosed(t *testing.T) {
	ctx := context.Background()
	cipher := newFakeCipher()
	const subject = "nhi:agent-x"
	store := newStore(t)

	producer := eventstore.Producer{NHIID: subject}
	ledger, _ := NewStepLedger(store, WithProducer(producer), WithContentSealer(cipher))
	if _, _, err := ledger.Apply(ctx, "run-x:step-1", func(context.Context) (Result, error) {
		return Result{Status: "ok", Payload: []byte("dado-sintetico")}, nil
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Rebuild SEM cipher sobre um store que tem registos cifrados ⇒ fail-closed.
	plain, _ := NewStepLedger(store, WithProducer(producer))
	if err := plain.Rebuild(ctx, "run-x"); !errors.Is(err, ErrSealedResultNoCipher) {
		t.Fatalf("Rebuild sem cipher sobre store cifrado devia dar ErrSealedResultNoCipher, deu: %v", err)
	}
}
