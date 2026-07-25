package integration

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aos-ref/integration/oidc"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/memory/working"
	"github.com/aos-ref/platform/registry/revalidation"
	"github.com/aos-ref/substrate/eventstore"
)

// TestApexSurface_RecordingAlerterLen cobre a leitura do contador de alertas.
func TestApexSurface_RecordingAlerterLen(t *testing.T) {
	a := NewRecordingAlerter()
	if got := a.Len(); got != 0 {
		t.Fatalf("Len() vazio = %d, esperado 0", got)
	}
	a.Alert(context.Background(), revalidation.Alert{Reason: revalidation.ReasonDigestMismatch})
	if got := a.Len(); got != 1 {
		t.Fatalf("Len() após alerta = %d, esperado 1", got)
	}
}

// TestApexSurface_AllowlistDirectoryRegister cobre o registo idempotente de humanos.
func TestApexSurface_AllowlistDirectoryRegister(t *testing.T) {
	d := NewAllowlistDirectory("alice")
	if err := d.Authenticate(context.Background(), "bob"); !errors.Is(err, ErrHumanNotRegistered) {
		t.Fatalf("esperado ErrHumanNotRegistered, got %v", err)
	}
	d.Register("") // id vazio ignorado
	d.Register("bob")
	if err := d.Authenticate(context.Background(), "bob"); err != nil {
		t.Fatalf("bob devia estar registado: %v", err)
	}
}

// TestApexSurface_OIDCDirectoryFromVerifier cobre o construtor a partir de verifier
// partilhado e as superfícies de autorização/autenticação fail-closed.
func TestApexSurface_OIDCDirectoryFromVerifier(t *testing.T) {
	v, err := oidc.NewVerifier(oidc.Config{Issuer: "https://issuer.example", Audience: "aos"})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	d := NewOIDCDirectoryFromVerifier(v)
	if got := d.AuthorizationMethod(); got != "oidc:https://issuer.example" {
		t.Fatalf("AuthorizationMethod = %q, esperado oidc:https://issuer.example", got)
	}
	if err := d.Authenticate(context.Background(), "alice"); !errors.Is(err, ErrAssertionRequired) {
		t.Fatalf("Authenticate devia exigir asserção: %v", err)
	}
	if got := v.Issuer(); got != "https://issuer.example" {
		t.Fatalf("Issuer = %q, esperado https://issuer.example", got)
	}
}

// TestApexSurface_RemoteAttestationVerifier cobre a construção fail-closed na URL e a
// verificação remota contra um componente de autoridade local (loopback).
func TestApexSurface_RemoteAttestationVerifier(t *testing.T) {
	// URL inválida: http em claro fora de loopback é recusado na construção.
	if _, err := NewRemoteDeviceAttestationVerifier(RemoteAttestationConfig{URL: "http://example.com/attest"}); !errors.Is(err, ErrRemoteAttestationURL) {
		t.Fatalf("http não-loopback devia falhar: %v", err)
	}
	// URL vazia.
	if _, err := NewRemoteDeviceAttestationVerifier(RemoteAttestationConfig{URL: "   "}); !errors.Is(err, ErrRemoteAttestationURL) {
		t.Fatalf("URL vazia devia falhar: %v", err)
	}
	// Esquema inválido.
	if _, err := NewRemoteDeviceAttestationVerifier(RemoteAttestationConfig{URL: "ftp://localhost/x"}); !errors.Is(err, ErrRemoteAttestationURL) {
		t.Fatalf("esquema inválido devia falhar: %v", err)
	}

	deviceID := []byte("device-42")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "método", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if len(body) == 0 {
			http.Error(w, "corpo vazio", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"device_id":"`+base64.StdEncoding.EncodeToString(deviceID)+`"}`)
	}))
	defer srv.Close()

	ver, err := NewRemoteDeviceAttestationVerifier(RemoteAttestationConfig{URL: srv.URL, Timeout: 0})
	if err != nil {
		t.Fatalf("construção com loopback http falhou: %v", err)
	}
	got, err := ver.VerifyDeviceAttestation(context.Background(), []byte("ao"), []byte("cdj"), []byte("chal"))
	if err != nil {
		t.Fatalf("VerifyDeviceAttestation falhou: %v", err)
	}
	if string(got) != string(deviceID) {
		t.Fatalf("deviceID = %q, esperado %q", got, deviceID)
	}

	// Entrada incompleta.
	if _, err := ver.VerifyDeviceAttestation(context.Background(), nil, []byte("cdj"), []byte("chal")); !errors.Is(err, ErrRemoteAttestationBody) {
		t.Fatalf("entrada incompleta devia falhar: %v", err)
	}
}

// TestApexSurface_RevalidationHook_WithEgressHost cobre a opção de extração de host.
func TestApexSurface_RevalidationHook_WithEgressHost(t *testing.T) {
	rv := newRevalidator(t, newTrust(t, context.Background(), audit.NewMemStore(), testSigner(t)), audit.NewMemStore(), NoopQuarantinerForTest{}, NoopAlerterForTest{})
	frozen := NewRunToolSets()
	current := staticResolver{present: true}
	policy := StaticPolicy{}
	extract := func(c referencemonitor.Call) string { return c.Resource.Value }
	h, err := NewRevalidationHook(rv, frozen, current, policy, WithEgressHost(extract))
	if err != nil {
		t.Fatalf("NewRevalidationHook com WithEgressHost falhou: %v", err)
	}
	if h.Name() != "revalidation" {
		t.Fatalf("nome default = %q", h.Name())
	}
}

// TestApexSurface_WindowManagerFactory_WithEvictionSink cobre a opção de sink de eviction.
func TestApexSurface_WindowManagerFactory_WithEvictionSink(t *testing.T) {
	var sink noopEvictionSink
	f, err := NewWindowManagerFactory(1024, WithEvictionSink(sink))
	if err != nil {
		t.Fatalf("NewWindowManagerFactory com WithEvictionSink falhou: %v", err)
	}
	if f == nil {
		t.Fatal("fábrica nil")
	}
}

type noopEvictionSink struct{}

func (noopEvictionSink) Persist(context.Context, working.EvictedSegment) error { return nil }

// TestApexSurface_SecuredRuntimeAccessors cobre os getters de superfície do runtime
// composto e a reconstrução do ledger quando este é nil.
func TestApexSurface_SecuredRuntimeAccessors(t *testing.T) {
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer store.Close()

	sr, err := NewSecuredRuntime(SecuredConfig{
		Model:       &scriptedModel{},
		Recorder:    agentruntime.NewTurnRecorder(store),
		Catalog:     &fakeCatalog{},
		Revalidator: newRevalidator(t, newTrust(t, context.Background(), audit.NewMemStore(), testSigner(t)), audit.NewMemStore(), NoopQuarantinerForTest{}, NoopAlerterForTest{}),
		Policy:      StaticPolicy{},
		WORM:        audit.NewMemStore(),
	})
	if err != nil {
		t.Fatalf("NewSecuredRuntime: %v", err)
	}
	if sr.Monitor() == nil {
		t.Fatal("Monitor() nil")
	}
	if sr.ToolSets() == nil {
		t.Fatal("ToolSets() nil")
	}
	if sr.Metrics() == nil {
		t.Fatal("Metrics() nil")
	}
	if err := sr.RebuildLedger(context.Background(), "run-surface"); err != nil {
		t.Fatalf("RebuildLedger com ledger nil devia ser no-op: %v", err)
	}
}

// TestApexSurface_NewNonce confirma que o helper gera nonces de 32 bytes.
func TestApexSurface_NewNonce(t *testing.T) {
	n, err := NewNonce()
	if err != nil {
		t.Fatalf("NewNonce: %v", err)
	}
	if len(n) != 32 {
		t.Fatalf("len(nonce) = %d, esperado 32", len(n))
	}
}
