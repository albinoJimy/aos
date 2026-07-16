package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const secret = "TOP-SECRET-value-abc123"

type recSink struct {
	ref, secret string
	calls       int
}

func (s *recSink) Place(ref, sec string) error {
	s.ref, s.secret, s.calls = ref, sec, s.calls+1
	return nil
}

func TestMemory_FetchPutRemove(t *testing.T) {
	m := NewMemory()
	key := Key{Provider: "p", Region: "r", Capability: "c"}

	if _, err := m.Fetch(context.Background(), key); !errors.Is(err, ErrNoMaterial) {
		t.Fatalf("vazio devia falhar: %v", err)
	}
	m.Put(key, secret)
	s, err := m.Fetch(context.Background(), key)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if s.IsZero() {
		t.Fatal("Secret nao devia ser zero")
	}
	m.Remove(key)
	if _, err := m.Fetch(context.Background(), key); !errors.Is(err, ErrNoMaterial) {
		t.Fatalf("apos Remove devia falhar: %v", err)
	}
}

func TestSecret_Redacao(t *testing.T) {
	m := NewMemory()
	key := Key{Provider: "p", Region: "r", Capability: "c"}
	m.Put(key, secret)
	s, _ := m.Fetch(context.Background(), key)

	for name, out := range map[string]string{
		"String":   s.String(),
		"GoString": s.GoString(),
		"%v":       fmt.Sprintf("%v", s),
		"%#v":      fmt.Sprintf("%#v", s),
	} {
		if strings.Contains(out, secret) {
			t.Errorf("%s expoe o segredo: %s", name, out)
		}
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Errorf("JSON expoe o segredo: %s", raw)
	}
}

func TestSecret_DeliverTo(t *testing.T) {
	m := NewMemory()
	key := Key{Provider: "p", Region: "r", Capability: "c"}
	m.Put(key, secret)
	s, _ := m.Fetch(context.Background(), key)

	var sink recSink
	if err := s.DeliverTo(&sink); err != nil {
		t.Fatalf("DeliverTo: %v", err)
	}
	if sink.secret != secret || sink.calls != 1 {
		t.Errorf("entrega server-side incorrecta: %+v", sink)
	}
	if sink.ref != key.id() {
		t.Errorf("ref esperado %q, obtido %q", key.id(), sink.ref)
	}

	// um Secret zero é vazio e a entrega falha fail-closed.
	var zero Secret
	if !zero.IsZero() {
		t.Error("Secret zero devia ser IsZero")
	}
	if err := zero.DeliverTo(&sink); !errors.Is(err, ErrNoMaterial) {
		t.Fatalf("Secret zero DeliverTo devia falhar: %v", err)
	}
}
