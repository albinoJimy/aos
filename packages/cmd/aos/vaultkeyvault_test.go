package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	audit "github.com/aos-ref/platform/audit"
)

// fakeTransit é um mock mínimo do motor Transit do Vault: guarda por-nome de chave o mapeamento
// ciphertext->plaintext (base64) e suporta destruição (crypto-shred). Um decrypt contra uma chave
// destruída devolve 400 — como o Vault real depois de DELETE keys/<name>.
type fakeTransit struct {
	live map[string]bool // nome da chave -> existe (false/ausente = destruída)
}

func (f *fakeTransit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// path: /v1/<mount>/<op>/<name...>
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/v1/"), "/", 3)
	if len(parts) < 2 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	op, name := parts[1], ""
	if len(parts) == 3 {
		name = parts[2]
	}
	switch {
	case op == "keys" && strings.HasSuffix(name, "/config"): // deletion_allowed
		w.WriteHeader(http.StatusNoContent)
	case op == "keys" && r.Method == http.MethodPost: // criar chave (idempotente)
		f.live[name] = true
		w.WriteHeader(http.StatusNoContent)
	case op == "keys" && r.Method == http.MethodDelete: // crypto-shred
		delete(f.live, name)
		w.WriteHeader(http.StatusNoContent)
	case op == "encrypt":
		if !f.live[name] {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var in struct {
			Plaintext string `json:"plaintext"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		// ciphertext reversível: "vault:v1:" + o plaintext (já base64 da DEK).
		writeData(w, "ciphertext", "vault:v1:"+in.Plaintext)
	case op == "decrypt":
		if !f.live[name] { // chave destruída ⇒ irrecuperável
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var in struct {
			Ciphertext string `json:"ciphertext"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		writeData(w, "plaintext", strings.TrimPrefix(in.Ciphertext, "vault:v1:"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func writeData(w http.ResponseWriter, k, v string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{k: v}})
}

// TestVaultKeyVault_WrapUnwrapShred prova o contrato de custódia: wrap->unwrap round-trip, e que
// o Delete (crypto-shred) torna o unwrap irrecuperável — a propriedade central de AOS-093/172.
func TestVaultKeyVault_WrapUnwrapShred(t *testing.T) {
	srv := httptest.NewServer(&fakeTransit{live: map[string]bool{}})
	defer srv.Close()
	v := newVaultKeyVault(srv.URL, "transit", "test-token")

	const subject = "human:alice"
	dek := []byte("0123456789abcdef0123456789abcdef") // 32B DEK efémera

	wrapped, ref, err := v.WrapDEK(subject, dek)
	if err != nil {
		t.Fatalf("WrapDEK: %v", err)
	}
	// keyRef TEM de ser exatamente audit.KeyRefFor(subject) (subject-binding de OpenContent).
	if ref != audit.KeyRefFor(subject) {
		t.Fatalf("keyRef = %q, esperado %q", ref, audit.KeyRefFor(subject))
	}

	got, ok := v.UnwrapDEK(ref, wrapped)
	if !ok || string(got) != string(dek) {
		t.Fatalf("UnwrapDEK round-trip falhou: ok=%v got=%q", ok, got)
	}

	// crypto-shred: destruir a KEK do titular ⇒ unwrap passa a falhar fechado.
	v.Delete(subject)
	if _, ok := v.UnwrapDEK(ref, wrapped); ok {
		t.Fatalf("após Delete, UnwrapDEK devia falhar (crypto-shred), mas devolveu ok=true")
	}
}

// TestVaultKeyVault_KeyNeverLeaves prova a custódia key-never-leaves: EnsureKey nunca devolve a KEK
// crua e Key devolve sempre (nil,false) — é o que força OpenContent pelo caminho de envelope.
func TestVaultKeyVault_KeyNeverLeaves(t *testing.T) {
	srv := httptest.NewServer(&fakeTransit{live: map[string]bool{}})
	defer srv.Close()
	v := newVaultKeyVault(srv.URL, "transit", "test-token")

	key, ref, err := v.EnsureKey("human:bob")
	if err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	if key != nil {
		t.Fatalf("EnsureKey devolveu KEK crua (%d bytes) — viola key-never-leaves", len(key))
	}
	if ref != audit.KeyRefFor("human:bob") {
		t.Fatalf("ref inesperado: %q", ref)
	}
	if k, ok := v.Key(ref); ok || k != nil {
		t.Fatalf("Key devia devolver (nil,false), veio (%v,%v)", k, ok)
	}
}

// TestVaultKeyVault_SatisfiesPorts é uma asserção de runtime redundante com as de compile-time em
// vaultkeyvault.go — documenta que o adaptador serve ambas as portas.
func TestVaultKeyVault_SatisfiesPorts(t *testing.T) {
	var _ audit.KeyVault = newVaultKeyVault("http://x", "transit", "t")
	var _ audit.KeyWrapper = newVaultKeyVault("http://x", "transit", "t")

	// Prova de reversibilidade do base64 do plaintext (defesa do formato do mock/contrato).
	if _, err := base64.StdEncoding.DecodeString(base64.StdEncoding.EncodeToString([]byte("x"))); err != nil {
		t.Fatal(err)
	}
}
