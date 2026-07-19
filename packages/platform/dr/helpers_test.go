package dr_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/backup"
)

// normalize replica a normalização de região do módulo (trim + lower) para as
// asserções de fronteira dos testes.
func normalize(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ephemeralAuditSigner devolve um audit.Signer com par ed25519 efémero (a chave
// privada nunca é persistida — sem segredos no repo).
func ephemeralAuditSigner() (*audit.Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return audit.NewSigner(priv)
}

// ephemeralBackupSigner devolve um backup.Ed25519Signer efémero.
func ephemeralBackupSigner(t *testing.T) *backup.Ed25519Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	s, err := backup.NewEd25519Signer(priv)
	if err != nil {
		t.Fatalf("NewEd25519Signer: %v", err)
	}
	return s
}

// stepClock devolve um relógio determinístico que devolve start na primeira chamada e
// start+step em cada chamada seguinte — dá um wall-clock de RTO igual a step entre o
// início e o fim do encadeamento (o orquestrador chama-o exactamente duas vezes).
func stepClock(start time.Time, step time.Duration) func() time.Time {
	var mu sync.Mutex
	calls := 0
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		t := start
		if calls > 0 {
			t = start.Add(step)
		}
		calls++
		return t
	}
}
