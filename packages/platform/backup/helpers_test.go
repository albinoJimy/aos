package backup

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// t0 é um instante-base fixo para testes com relógio injectado.
var t0 = time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

// fixedClock devolve um relógio que devolve sempre o mesmo instante.
func fixedClock(at time.Time) func() time.Time {
	return func() time.Time { return at }
}

// newExporter monta um exportador de referência (vault determinístico, destino na
// mesma região). Devolve também o ImmutableStore de destino.
func newExporter(t *testing.T, src *eventstore.Store, region string, opts ...ExporterOption) (*Exporter, *InMemoryImmutableStore) {
	t.Helper()
	dst := NewInMemoryImmutableStore(region)
	base := []ExporterOption{WithRandSource(detRand())}
	exp, err := NewExporter(src, dst, newSigner(t), append(base, opts...)...)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	return exp, dst
}

// detRand devolve uma audit.RandSource DETERMINÍSTICA (contador) para testes
// reprodutíveis: preenche p com bytes crescentes, garantindo DEKs/nonces distintos
// por chamada sem crypto/rand real no caminho de asserção.
func detRand() audit.RandSource {
	var n byte
	return func(p []byte) error {
		for i := range p {
			p[i] = n
			n++
		}
		return nil
	}
}

// newSigner devolve um Signer efémero (par ed25519 gerado no arranque do teste). A
// chave privada nunca é persistida.
func newSigner(t *testing.T) *Ed25519Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	s, err := NewEd25519Signer(priv)
	if err != nil {
		t.Fatalf("NewEd25519Signer: %v", err)
	}
	return s
}

// newSourceStore constrói um Event Store com fronteira de soberania e devolve-o.
func newSourceStore(t *testing.T, board, region string) *eventstore.Store {
	t.Helper()
	s, err := eventstore.New(eventstore.WithReplicas(3), eventstore.WithSovereigntyBoard(board, region))
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// seed acrescenta n eventos num stream com um marcador no payload (para testar a
// cifra em repouso). Os step ids CONTINUAM a partir do head corrente, para que
// re-semear o mesmo stream não colida com a idempotência (run_id:step_id).
func seed(t *testing.T, s *eventstore.Store, stream string, n int, marker string) {
	t.Helper()
	ctx := context.Background()
	head, err := s.StreamHead(ctx, stream)
	if err != nil {
		t.Fatalf("StreamHead %s: %v", stream, err)
	}
	for i := uint64(1); i <= uint64(n); i++ {
		seq := head + i
		in := eventstore.EventInput{
			Type:    "test.fact",
			Payload: []byte(fmt.Sprintf(`{"i":%d,"secret":%q}`, seq, marker)),
			RunID:   stream,
			StepID:  fmt.Sprintf("s%d", seq),
		}
		if _, err := s.Append(ctx, stream, in); err != nil {
			t.Fatalf("Append %s#%d: %v", stream, seq, err)
		}
	}
}

// freshDest constrói um Event Store de destino (mesma região) para o restauro.
func freshDest(t *testing.T, board, region string) *eventstore.Store {
	return newSourceStore(t, board, region)
}
