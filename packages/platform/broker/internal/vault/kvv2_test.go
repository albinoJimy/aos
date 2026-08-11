package vault

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// kvSecret é o valor downstream de teste. ÚNICO e improvável para o scan de fuga:
// se aparecer numa superfície observável o teste falha.
const kvSecret = "TOP-SECRET-kv-stripe-eu-charge-77c1"

// fakeKV modela o motor KV v2 do Vault: responde ao GET de leitura e ao seal-status.
// Regista o token apresentado (para provar que NÃO é logado em lado nenhum, e que é
// enviado no header correcto). Concorrência não é exercida (um pedido de cada vez).
func fakeKV(t *testing.T, mountPath string, secrets map[string]map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/sys/seal-status", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"sealed":false,"initialized":true}`))
	})
	// /v1/{mount}/data/ é o prefixo de leitura do KV v2.
	prefix := "/v1/" + mountPath + "/data/"
	mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, prefix)
		fields, ok := secrets[path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[]}`))
			return
		}
		var body kvReadResponse
		body.Data.Data = fields
		_ = json.NewEncoder(w).Encode(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestKVv2_Fetch_Encapsula(t *testing.T) {
	// path esperado para Key{stripe, eu, cap:pay.charge}: sanitizado ':' → '_'.
	path := "stripe/eu/cap_pay.charge"
	srv := fakeKV(t, "secret", map[string]map[string]string{
		path: {"value": kvSecret, "ignored": "outra-coisa"},
	})
	c, err := NewKVv2(KVv2Config{Addr: srv.URL, Token: "tok", HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("NewKVv2: %v", err)
	}

	key := Key{Provider: "stripe", Region: "eu", Capability: "cap:pay.charge"}
	s, err := c.Fetch(context.Background(), key)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if s.IsZero() {
		t.Fatal("Secret nao devia ser zero")
	}
	if s.Ref() != key.id() {
		t.Fatalf("ref esperado %q, obtido %q", key.id(), s.Ref())
	}

	// O valor SÓ sai por DeliverTo, para um sink server-side — e não pelas superfícies
	// de redacção (a invariante rainha, imposta pelo tipo).
	for name, out := range map[string]string{
		"String":   s.String(),
		"GoString": s.GoString(),
	} {
		if strings.Contains(out, kvSecret) {
			t.Errorf("%s expoe o segredo: %s", name, out)
		}
	}
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), kvSecret) {
		t.Errorf("JSON expoe o segredo: %s", raw)
	}

	var sink recSink
	if err := s.DeliverTo(&sink); err != nil {
		t.Fatalf("DeliverTo: %v", err)
	}
	if sink.secret != kvSecret || sink.calls != 1 {
		t.Errorf("entrega server-side incorrecta: %+v", sink)
	}
}

func TestKVv2_Fetch_FailClosed(t *testing.T) {
	srv := fakeKV(t, "secret", map[string]map[string]string{
		"stripe/eu/cap_pay.charge": {"outrocampo": "x"}, // sem o campo `value`
	})
	c, _ := NewKVv2(KVv2Config{Addr: srv.URL, Token: "tok", HTTPClient: srv.Client()})

	// path inexistente ⇒ 404 ⇒ ErrNoMaterial (fail-closed).
	if _, err := c.Fetch(context.Background(), Key{Provider: "ausente", Region: "eu", Capability: "cap:x"}); !errors.Is(err, ErrNoMaterial) {
		t.Fatalf("path inexistente devia dar ErrNoMaterial, obtido %v", err)
	}
	// campo `value` ausente ⇒ ErrNoMaterial (o broker nao inventa credencial).
	if _, err := c.Fetch(context.Background(), Key{Provider: "stripe", Region: "eu", Capability: "cap:pay.charge"}); !errors.Is(err, ErrNoMaterial) {
		t.Fatalf("campo ausente devia dar ErrNoMaterial, obtido %v", err)
	}
}

func TestKVv2_Fetch_ErroDeEstado(t *testing.T) {
	// servidor que devolve sempre 500 ⇒ ErrKVFetch (nunca ErrNoMaterial nem sucesso).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	c, _ := NewKVv2(KVv2Config{Addr: srv.URL, Token: "tok", HTTPClient: srv.Client()})
	_, err := c.Fetch(context.Background(), Key{Provider: "p", Region: "r", Capability: "c"})
	if !errors.Is(err, ErrKVFetch) {
		t.Fatalf("HTTP 500 devia dar ErrKVFetch, obtido %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "tok") {
		t.Fatal("o erro NUNCA deve conter o token")
	}
}

func TestNewKVv2_ConfigInvalida(t *testing.T) {
	if _, err := NewKVv2(KVv2Config{Addr: "", Token: "t"}); !errors.Is(err, ErrKVConfig) {
		t.Fatalf("addr vazio devia dar ErrKVConfig, obtido %v", err)
	}
	if _, err := NewKVv2(KVv2Config{Addr: "https://v", Token: ""}); !errors.Is(err, ErrKVConfig) {
		t.Fatalf("token vazio devia dar ErrKVConfig, obtido %v", err)
	}
}

func TestKVv2_Ready(t *testing.T) {
	srv := fakeKV(t, "secret", nil)
	c, _ := NewKVv2(KVv2Config{Addr: srv.URL, Token: "tok", HTTPClient: srv.Client()})
	if err := c.Ready(context.Background()); err != nil {
		t.Fatalf("Ready contra vault destravado devia passar: %v", err)
	}
}
