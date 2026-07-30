package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync/atomic"
	"testing"

	dsar "github.com/aos-ref/control-plane/governance/dsar"
	audit "github.com/aos-ref/platform/audit"
)

// AOS-216 (residual HSM de DEF-302/AOS-215) — PROVA FALSIFICÁVEL, AO NÍVEL DO NÓ, DA
// PORTA DE ENVELOPE key-never-leaves.
//
// A costura de AOS-215 injecta um vault por [Config.DSARVault] (porta [audit.KeyVault]).
// Um vault que implemente TAMBÉM [audit.KeyWrapper] faz o caminho de cifra/decifra do
// substrato tomar a via de ENVELOPE: a DEK é embrulhada DENTRO do módulo de custódia
// ([WrapDEK]/[UnwrapDEK]) e a KEK CRUA NUNCA entra no processo do nó — nem no seal, nem no
// open. Provamos que o nó inteiro (capturer → Event Store → OpenContent → /dsar/erase)
// atravessa a via de envelope SEM alguma vez pedir a KEK crua ao vault, e que o shred a
// torna irrecuperável.

// envelopeSpyVault é um DOUBLE da porta de ENVELOPE: delega o wrap/unwrap num
// [audit.InMemoryKeyWrapper] de referência (KEK efémeras por crypto/rand, sem fixtures de
// chave) e CONTA as chamadas. Key()/EnsureKey() FALHAM (não surrendem a KEK crua) e contam:
// o teste exige que o nó NUNCA as tenha precisado — é a falsificação de key-never-leaves.
type envelopeSpyVault struct {
	inner       *audit.InMemoryKeyWrapper
	wrapCalls   atomic.Int64
	unwrapCalls atomic.Int64
	keyCalls    atomic.Int64
	ensureCalls atomic.Int64
	deleteCalls atomic.Int64
	lastDeleted atomic.Value // string
}

func newEnvelopeSpyVault() *envelopeSpyVault {
	return &envelopeSpyVault{inner: audit.NewInMemoryKeyWrapper(nil)}
}

func (v *envelopeSpyVault) WrapDEK(subjectID string, dek []byte) ([]byte, string, error) {
	v.wrapCalls.Add(1)
	return v.inner.WrapDEK(subjectID, dek)
}

func (v *envelopeSpyVault) UnwrapDEK(keyRef string, wrapped []byte) ([]byte, bool) {
	v.unwrapCalls.Add(1)
	return v.inner.UnwrapDEK(keyRef, wrapped)
}

// Key FALHA (não devolve a KEK crua) e conta — se o nó a chamasse na via de envelope, o
// teste apanhava-o (keyCalls > 0).
func (v *envelopeSpyVault) Key(string) ([]byte, bool) {
	v.keyCalls.Add(1)
	return nil, false
}

// EnsureKey FALHA em surrender da KEK crua (key=nil) e conta.
func (v *envelopeSpyVault) EnsureKey(subjectID string) ([]byte, string, error) {
	v.ensureCalls.Add(1)
	return v.inner.EnsureKey(subjectID)
}

func (v *envelopeSpyVault) Delete(subjectID string) {
	v.deleteCalls.Add(1)
	v.lastDeleted.Store(subjectID)
	v.inner.Delete(subjectID)
}

var (
	_ audit.KeyVault   = (*envelopeSpyVault)(nil)
	_ audit.KeyWrapper = (*envelopeSpyVault)(nil)
)

// newDurableNodeWithWrapper arranca um nó durável com um vault de ENVELOPE injectado.
func newDurableNodeWithWrapper(t *testing.T, vault audit.KeyVault) *Node {
	t.Helper()
	dir := t.TempDir()
	cfg := tnBaseConfig()
	cfg.DurableExecution = true
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	cfg.DSARVault = vault
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (durable, wrapper injectado): %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	return node
}

// TestNode_AOS216_EnvelopeCustodyKeyNeverLeaves prova, ao nível do NÓ, que com um vault de
// envelope injectado a cifra/decifra do substrato passa por WrapDEK/UnwrapDEK e a KEK crua
// NUNCA é pedida — e que o /dsar/erase a destrói tornando o conteúdo irrecuperável.
func TestNode_AOS216_EnvelopeCustodyKeyNeverLeaves(t *testing.T) {
	ctx := context.Background()
	spy := newEnvelopeSpyVault()
	node := newDurableNodeWithWrapper(t, spy)

	const subject = "nhi:agent-hsm-custody"
	const runID = "run-hsm-216"

	// (a) IDENTIDADE: o vault em vigor É o wrapper injectado.
	if node.DSARVault != audit.KeyVault(spy) {
		t.Fatal("node.DSARVault não é o wrapper injectado — a costura de envelope não liga")
	}

	// (b) A cifra do substrato passa pela via de ENVELOPE (WrapDEK), não pela KEK crua.
	captureSynthetic(t, node, subject, runID, "AOS216-SINTETICO: caso HSM-ENVELOPE", "TOOL-216-SECRET")
	if spy.wrapCalls.Load() == 0 {
		t.Fatal("a cifra NÃO passou pela via de envelope (WrapDEK nunca correu)")
	}

	// (c) O blob selado no Event Store está em FORMATO DE ENVELOPE (key_ref presente) — a
	// via de envelope atravessou o substrato inteiro, não só a primitiva de audit.
	sealed, sealedSubj := sealedContentOf(t, node, runID)
	if len(sealed) == 0 || sealedSubj != subject {
		t.Fatalf("conteúdo não selado por-titular: len=%d subject=%q", len(sealed), sealedSubj)
	}
	if !bytes.Contains(sealed, []byte("key_ref")) {
		t.Fatal("o blob selado pelo substrato não está em formato de envelope (sem key_ref)")
	}
	if bytes.Contains(sealed, []byte("HSM-ENVELOPE")) {
		t.Fatal("o blob de envelope contém plaintext em claro — confidencialidade violada")
	}

	// (d) RECUPERÁVEL ANTES do erase, pela via de envelope (prova não-vácua).
	plainBefore, err := audit.OpenContent(spy, subject, sealed)
	if err != nil {
		t.Fatalf("OpenContent (envelope) antes do erase: %v", err)
	}
	if !bytes.Contains(plainBefore, []byte("HSM-ENVELOPE")) {
		t.Fatal("o conteúdo decifrado devia conter o texto sintético")
	}
	if spy.unwrapCalls.Load() == 0 {
		t.Fatal("a decifra NÃO passou pela via de envelope (UnwrapDEK nunca correu)")
	}

	// (e) KEY-NEVER-LEAVES: o nó NUNCA pediu a KEK crua ao vault — Key/EnsureKey a zero.
	if spy.keyCalls.Load() != 0 {
		t.Fatalf("a via de envelope pediu a KEK crua via Key() %d vez(es) — key-never-leaves violado", spy.keyCalls.Load())
	}
	if spy.ensureCalls.Load() != 0 {
		t.Fatalf("a via de envelope pediu a KEK crua via EnsureKey() %d vez(es) — key-never-leaves violado", spy.ensureCalls.Load())
	}

	// (f) /dsar/erase destrói a KEK NO wrapper injectado (Delete corre para o titular).
	deletesBefore := spy.deleteCalls.Load()
	res, err := node.DSAR.Receive(ctx, dsar.Request{RequestID: "req-216", SubjectID: subject})
	if err != nil {
		t.Fatalf("DSAR erase: %v", err)
	}
	if res.Blocked {
		t.Fatal("erase não devia estar bloqueado (sem legal hold)")
	}
	if spy.deleteCalls.Load() <= deletesBefore {
		t.Fatal("o /dsar/erase NÃO destruiu a KEK no wrapper injectado (Delete nunca correu nele)")
	}
	if got, _ := spy.lastDeleted.Load().(string); got != subject {
		t.Fatalf("Delete correu para o titular errado: %q (esperado %q)", got, subject)
	}

	// (g) IRRECUPERÁVEL após o shred: UnwrapDEK falha ⇒ OpenContent ⇒ ErrDecrypt. E a KEK
	// crua CONTINUA a nunca ser pedida (o shred manifesta-se na via de envelope).
	if _, err := audit.OpenContent(spy, subject, sealed); !errors.Is(err, audit.ErrDecrypt) {
		t.Fatalf("após erase, OpenContent (envelope) devia dar ErrDecrypt, deu: %v", err)
	}
	if spy.keyCalls.Load() != 0 {
		t.Fatal("a KEK crua foi pedida algures — key-never-leaves violado")
	}
}
