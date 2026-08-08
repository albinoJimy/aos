package durable

import (
	"context"
	"errors"
	"testing"
)

// TestLeaseRelease_TornaOReclamavelJa é a razão de o largar explícito existir: um run
// SUSPENSO à espera de aval humano não está a ser servido por ninguém, e sem este acto a
// própria réplica que o suspendeu ficaria impedida de o re-hospedar até o TTL passar.
func TestLeaseRelease_TornaOReclamavelJa(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	clk := newTestClock()
	m := newManager(t, store, clk)
	ctx := context.Background()

	lease, err := m.Claim(ctx, "run-a")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	// Sem largar, o lease está vivo e não é reclamável.
	if _, err := m.Claim(ctx, "run-a"); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("um lease vivo NAO e reclamavel; err=%v", err)
	}
	if err := m.Release(ctx, lease); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// Largado: reclamável JÁ, sem avançar o relógio um único nanossegundo.
	got, err := m.Claim(ctx, "run-a")
	if err != nil {
		t.Fatalf("apos o largar o run tem de ser reclamavel SEM esperar o TTL: %v", err)
	}
	if got.Token.Value() <= lease.Token.Value() {
		t.Fatalf("o novo claim tem de mintar um token ESTRITAMENTE maior: %d <= %d", got.Token.Value(), lease.Token.Value())
	}
}

// TestLeaseRelease_NaoExpulsaONovoDetentor: largar TARDE (depois de o lease ter sido
// superado por outro claim) não pode roubar o run a quem o detém agora. O largar só
// encurta a expiração do PRÓPRIO token de quem escreve.
func TestLeaseRelease_NaoExpulsaONovoDetentor(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	clk := newTestClock()
	m := newManager(t, store, clk)
	ctx := context.Background()

	velho, err := m.Claim(ctx, "run-b")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	clk.Advance(2 * ttl) // o detentor morreu: o lease expira
	novo, err := m.Claim(ctx, "run-b")
	if err != nil {
		t.Fatalf("re-claim apos expiracao: %v", err)
	}
	if err := m.Release(ctx, velho); !errors.Is(err, ErrLeaseSuperseded) {
		t.Fatalf("largar um lease SUPERADO tem de ser recusado; err=%v", err)
	}
	// O novo detentor continua vivo: ninguém lho tirou.
	if _, err := m.Claim(ctx, "run-b"); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("o lease do novo detentor tem de continuar VIVO; err=%v", err)
	}
	if _, err := m.Heartbeat(ctx, novo); err != nil {
		t.Fatalf("o novo detentor tem de continuar a poder renovar: %v", err)
	}
}

// TestLeaseRelease_Idempotente: largar duas vezes, ou largar o que já expirou, ou largar
// um run sem lease nenhum — nada disso é erro.
func TestLeaseRelease_Idempotente(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	clk := newTestClock()
	m := newManager(t, store, clk)
	ctx := context.Background()

	if err := m.Release(ctx, Lease{RunID: "run-c", Token: 1}); err != nil {
		t.Fatalf("largar um run sem lease e no-op: %v", err)
	}
	lease, err := m.Claim(ctx, "run-c")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := m.Release(ctx, lease); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := m.Release(ctx, lease); err != nil {
		t.Fatalf("largar de novo tem de ser no-op: %v", err)
	}
}

// TestLeaseRelease_ValidacaoDeEntrada mantém o contrato dos outros métodos do gestor.
func TestLeaseRelease_ValidacaoDeEntrada(t *testing.T) {
	t.Parallel()
	m := newManager(t, newStore(t), newTestClock())
	ctx := context.Background()
	if err := m.Release(ctx, Lease{}); !errors.Is(err, ErrEmptyRunID) {
		t.Fatalf("run vazio: %v", err)
	}
	if err := m.Release(ctx, Lease{RunID: "r"}); !errors.Is(err, ErrLeaseSuperseded) {
		t.Fatalf("token invalido: %v", err)
	}
}
