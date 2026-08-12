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
	// Desde AOS-249 a sonda faz DUAS perguntas: seal-status (o Vault está vivo?) e lookup-self
	// (o NOSSO token ainda serve?). Um servidor que só respondesse à primeira já não é um Vault
	// pronto do ponto de vista do nó — que é exactamente o ponto do ticket.
	var viuSeal, viuLookup bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sys/seal-status":
			viuSeal = true
			_, _ = w.Write([]byte(`{"type":"shamir","initialized":true,"sealed":false}`))
		case "/v1/auth/token/lookup-self":
			viuLookup = true
			_, _ = w.Write([]byte(`{"data":{"ttl":3600,"renewable":true}}`))
		default:
			t.Errorf("sonda bateu num caminho inesperado: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	v := newVaultKeyVault(srv.URL, "transit", "tok")
	if err := v.ready(context.Background()); err != nil {
		t.Fatalf("vault unsealed com token valido devia estar pronto, veio: %v", err)
	}
	if !viuSeal || !viuLookup {
		t.Fatalf("a sonda tem de bater NOS DOIS endpoints (seal=%v, lookup-self=%v) — sem o lookup-self autenticado nao ha prova de que o token ainda serve", viuSeal, viuLookup)
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
