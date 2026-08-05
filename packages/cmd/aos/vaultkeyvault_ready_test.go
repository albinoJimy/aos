package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Testa a sonda de prontidão do Vault (revisão de prontidão #2): o /readyz tem de reportar
// UNREADY quando a custódia da KEK está SELADA ou INALCANÇÁVEL, senão a via GDPR quebra em
// silêncio com o nó a encaminhar tráfego. A sonda usa /v1/sys/seal-status (não-autenticado).

func TestVaultReady_Unsealed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sys/seal-status" {
			t.Errorf("sonda devia bater em seal-status, veio %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"type":"shamir","initialized":true,"sealed":false}`))
	}))
	defer srv.Close()
	v := newVaultKeyVault(srv.URL, "transit", "tok")
	if err := v.ready(context.Background()); err != nil {
		t.Fatalf("vault unsealed devia estar pronto, veio: %v", err)
	}
}

func TestVaultReady_Sealed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"type":"shamir","initialized":true,"sealed":true}`))
	}))
	defer srv.Close()
	v := newVaultKeyVault(srv.URL, "transit", "tok")
	if err := v.ready(context.Background()); err == nil {
		t.Fatal("vault SELADO NAO devia estar pronto (readyz tem de ser 503)")
	}
}

func TestVaultReady_Unreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	addr := srv.URL
	srv.Close() // fecha ⇒ inalcançável.
	v := newVaultKeyVault(addr, "transit", "tok")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := v.ready(ctx); err == nil {
		t.Fatal("vault INALCANCAVEL NAO devia estar pronto")
	}
}

func TestVaultReady_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	v := newVaultKeyVault(srv.URL, "transit", "tok")
	if err := v.ready(context.Background()); err == nil {
		t.Fatal("seal-status HTTP 500 NAO devia contar como pronto")
	}
}

// Garante que o vault do Vault satisfaz a interface readinessProber usada pelo handleReadyz
// (a in-memory NÃO a satisfaz de propósito — não há como o readyz a sondar).
func TestVaultKeyVaultIsReadinessProber(t *testing.T) {
	var _ readinessProber = newVaultKeyVault("http://x:8200", "transit", "tok")
}
