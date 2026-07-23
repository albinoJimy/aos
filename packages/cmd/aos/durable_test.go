package main

import (
	"context"
	"crypto/ed25519"
	"io"
	"os"
	"path/filepath"
	"testing"

	audit "github.com/aos-ref/platform/audit"
	eventstore "github.com/aos-ref/substrate/eventstore"
)

// eventInput constrói um EventInput mínimo para exercitar a persistência.
func eventInput() eventstore.EventInput {
	return eventstore.EventInput{
		Type:     "test.event",
		Payload:  []byte(`{}`),
		RunID:    "run-x",
		StepID:   "s1",
		Producer: eventstore.Producer{NHIID: "nhi:test"},
	}
}

// AOS-170 — testes de WIRING do substrato DURÁVEL no Config do nó. Provam que o nó
// compõe stores duráveis a partir dos caminhos e os fecha; a fidelidade do restart do
// próprio substrato é coberta pelos testes dos pacotes eventstore/audit.

// TestNode_DurableSubstrateWired — com EventStorePath/WORMPath, o nó abre stores
// DURÁVEIS (o Event Store persiste em disco; o WORM é um FileStore) e o banner
// declara-o. Os ficheiros são criados no arranque.
func TestNode_DurableSubstrateWired(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	esPath := filepath.Join(dir, "events.wal")
	wormPath := filepath.Join(dir, "worm.wal")

	cfg := tnBaseConfig()
	cfg.EventStorePath = esPath
	cfg.WORMPath = wormPath

	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("bootstrap durável: %v", err)
	}

	// O WORM é o FileStore durável, não o MemStore de referência.
	if _, ok := node.WORM.(*audit.FileStore); !ok {
		t.Fatalf("WORM não é durável: %T", node.WORM)
	}
	// Um append ao Event Store persiste em disco (o ficheiro cresce).
	if _, err := node.EventStore.Append(ctx, "run-x", eventInput()); err != nil {
		t.Fatalf("append: %v", err)
	}
	if fi, err := os.Stat(esPath); err != nil || fi.Size() == 0 {
		t.Fatalf("WAL do Event Store não persistiu: size=%v err=%v", fiSize(fi), err)
	}
	if err := node.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Após Close, os ficheiros existem com conteúdo (durável).
	if fi, err := os.Stat(wormPath); err != nil {
		t.Fatalf("WORM WAL ausente: %v", err)
	} else if fi.Size() == 0 {
		// O WORM só cresce quando há Append; aqui não houve, tamanho 0 é aceitável.
		_ = fi
	}
}

// TestNode_SubstrateModeBanner — o banner distingue durável de in-memory.
func TestNode_SubstrateModeBanner(t *testing.T) {
	inMem := substrateMode(tnBaseConfig())
	if inMem != "in-memory de referencia (nao-duravel)" {
		t.Fatalf("banner in-memory inesperado: %q", inMem)
	}
	cfg := tnBaseConfig()
	cfg.EventStorePath = "x"
	cfg.WORMPath = "y"
	if got := substrateMode(cfg); got != "duravel em disco (AOS-170)" {
		t.Fatalf("banner durável inesperado: %q", got)
	}
}

// TestNode_HardenedRejectsIssuerKeyPath — no modo endurecido (trust-anchor-only)
// NENHUMA chave de assinatura entra no processo: um IssuerKeyPath (que a carregaria de
// disco) é recusado fail-closed com ErrConflictingIssuerKey.
func TestNode_HardenedRejectsIssuerKeyPath(t *testing.T) {
	ctx := context.Background()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("gerar pubkey: %v", err)
	}
	cfg := Config{
		IssuerID:      tnIssuerID,
		IssuerPubKey:  pub, // modo endurecido
		IssuerClasses: tnBaseConfig().IssuerClasses,
		IssuerKeyPath: filepath.Join(t.TempDir(), "seed"),
	}
	if _, err := Bootstrap(ctx, cfg, io.Discard); err != ErrConflictingIssuerKey {
		t.Fatalf("modo endurecido devia recusar IssuerKeyPath: err=%v", err)
	}
}

// TestKeySource_StableAcrossRestarts — a chave carregada de um ficheiro de seed é a
// MESMA entre "reinícios" (dois LoadOrCreateIssuerKey sobre o mesmo path): os tokens
// emitidos antes do restart continuariam válidos. É o oposto de gerar por CSPRNG a
// cada boot.
func TestKeySource_StableAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issuer.seed")

	k1, err := LoadOrCreateIssuerKey(path)
	if err != nil {
		t.Fatalf("primeira carga: %v", err)
	}
	// O ficheiro foi persistido com a seed (32 bytes).
	seed, err := os.ReadFile(path)
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("seed não persistida: len=%d err=%v", len(seed), err)
	}
	k2, err := LoadOrCreateIssuerKey(path)
	if err != nil {
		t.Fatalf("segunda carga (restart): %v", err)
	}
	if !k1.Equal(k2) {
		t.Fatal("a chave do issuer mudou entre reinícios — os tokens invalidariam (não-durável)")
	}
	// A pubkey (trust anchor do verifier) é estável — o verifier antigo reconhece a nova.
	if !k1.Public().(ed25519.PublicKey).Equal(k2.Public().(ed25519.PublicKey)) {
		t.Fatal("pubkey do issuer mudou entre reinícios")
	}
}

// TestKeySource_RejectsMalformedSeed — um ficheiro de seed com tamanho errado é
// recusado fail-closed.
func TestKeySource_RejectsMalformedSeed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.seed")
	if err := os.WriteFile(path, []byte("too-short"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadOrCreateIssuerKey(path); err != ErrBadIssuerKeyFile {
		t.Fatalf("seed malformada devia dar ErrBadIssuerKeyFile: %v", err)
	}
}

// TestNode_IssuerKeyPathWiredInReferenceMode — no modo de referência, IssuerKeyPath faz
// o nó usar a chave PERSISTENTE; dois arranques sobre o mesmo path produzem a MESMA
// pubkey de issuer (trust anchor estável), ao contrário do default CSPRNG-por-boot.
func TestNode_IssuerKeyPathWiredInReferenceMode(t *testing.T) {
	ctx := context.Background()
	keyPath := filepath.Join(t.TempDir(), "issuer.seed")

	cfg := tnBaseConfig()
	cfg.IssuerKeyPath = keyPath

	n1, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("bootstrap 1: %v", err)
	}
	anchor1, pub1 := n1.Authority.TrustAnchor()
	_ = n1.Close()

	n2, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("bootstrap 2 (restart): %v", err)
	}
	anchor2, pub2 := n2.Authority.TrustAnchor()
	defer n2.Close()

	if anchor1 != anchor2 {
		t.Fatalf("issuerID mudou: %q vs %q", anchor1, anchor2)
	}
	if !pub1.Equal(pub2) {
		t.Fatal("pubkey do issuer (trust anchor) mudou entre reinícios — identidade não-durável")
	}
}

func fiSize(fi os.FileInfo) int64 {
	if fi == nil {
		return -1
	}
	return fi.Size()
}
