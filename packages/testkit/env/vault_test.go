package env_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/testkit/env"
)

// recordingSink é um Sink de teste que regista o que foi entregue server-side.
type recordingSink struct {
	ref, secret string
	err         error
}

func (s *recordingSink) Place(ref, secret string) error {
	s.ref, s.secret = ref, secret
	return s.err
}

// TestFakeVault_FetchAndDeliver cobre o caminho feliz: Put → Fetch devolve um
// Secret opaco → DeliverTo entrega o valor a um Sink server-side.
func TestFakeVault_FetchAndDeliver(t *testing.T) {
	t.Parallel()
	v := env.NewFakeVault()
	key := env.VaultKey{Provider: "openai", Region: "eu", Capability: "chat"}
	v.Put(key, "SK-SECRET-123")

	sec, err := v.Fetch(context.Background(), key)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if sec.IsZero() {
		t.Fatal("Secret nao devia ser zero")
	}
	if sec.Ref() != key.Provider+"|"+key.Region+"|"+key.Capability {
		t.Fatalf("Ref inesperado: %s", sec.Ref())
	}

	sink := &recordingSink{}
	if err := sec.DeliverTo(sink); err != nil {
		t.Fatalf("DeliverTo: %v", err)
	}
	if sink.secret != "SK-SECRET-123" {
		t.Fatalf("o sink server-side devia receber o valor, recebeu %q", sink.secret)
	}
}

// TestFakeVault_FailClosed cobre o fail-closed: sem material, Fetch devolve
// ErrNoMaterial (nunca um Secret vazio silencioso); Remove revoga.
func TestFakeVault_FailClosed(t *testing.T) {
	t.Parallel()
	v := env.NewFakeVault()
	key := env.VaultKey{Provider: "p", Region: "eu", Capability: "c"}

	if _, err := v.Fetch(context.Background(), key); !errors.Is(err, env.ErrNoMaterial) {
		t.Fatalf("sem material devia falhar fail-closed, obtive %v", err)
	}
	v.Put(key, "S")
	if _, err := v.Fetch(context.Background(), key); err != nil {
		t.Fatalf("apos Put devia ter material: %v", err)
	}
	v.Remove(key)
	if _, err := v.Fetch(context.Background(), key); !errors.Is(err, env.ErrNoMaterial) {
		t.Fatalf("apos Remove devia falhar fail-closed, obtive %v", err)
	}
}

// TestFakeVault_SecretRedacted cobre a invariante de não-fuga: o valor do segredo
// NUNCA aparece em String()/GoString()/MarshalJSON — só a forma redigida.
func TestFakeVault_SecretRedacted(t *testing.T) {
	t.Parallel()
	v := env.NewFakeVault()
	key := env.VaultKey{Provider: "p", Region: "eu", Capability: "c"}
	v.Put(key, "TOP-SECRET-VALUE")
	sec, _ := v.Fetch(context.Background(), key)

	if strings.Contains(sec.String(), "TOP-SECRET-VALUE") {
		t.Fatal("String() vazou o segredo")
	}
	if strings.Contains(sec.GoString(), "TOP-SECRET-VALUE") {
		t.Fatal("GoString() vazou o segredo")
	}
	if !strings.Contains(sec.String(), "REDACTED") {
		t.Fatalf("String() devia redigir: %s", sec.String())
	}
	raw, err := json.Marshal(sec)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if strings.Contains(string(raw), "TOP-SECRET-VALUE") {
		t.Fatalf("MarshalJSON vazou o segredo: %s", raw)
	}
}

// TestFakeVault_ZeroSecretDeliver cobre o Secret zero: DeliverTo devolve
// ErrNoMaterial.
func TestFakeVault_ZeroSecretDeliver(t *testing.T) {
	t.Parallel()
	var zero env.Secret
	if !zero.IsZero() {
		t.Fatal("Secret{} devia ser zero")
	}
	if err := zero.DeliverTo(&recordingSink{}); !errors.Is(err, env.ErrNoMaterial) {
		t.Fatalf("DeliverTo de Secret zero devia falhar, obtive %v", err)
	}
}
