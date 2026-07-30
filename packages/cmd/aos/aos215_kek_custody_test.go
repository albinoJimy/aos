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
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	audit "github.com/aos-ref/platform/audit"
)

// AOS-215 (DEF-302) — PROVA FALSIFICÁVEL DA COSTURA DE CUSTÓDIA EXTERNA DA KEK.
//
// O nó deixa de HARDCODAR o vault de KEK por-titular: quando um vault é INJECTADO por
// [Config.DSARVault] (a porta [audit.KeyVault], molde do Event Store/WORM), é ESSE vault
// que o caminho de cifra/shred usa — EnsureKey/Key/Delete passam por ELE, não por um
// InMemoryKeyVault interno. O /dsar/erase destrói a KEK NO vault injectado e o conteúdo
// fica irrecuperável por essa via. É a mesma classe de defeito que a auditoria v4 nomeia
// (porta existe, artefacto não a expõe) — aqui provada FECHADA ao nível do nó.

// spyKeyVault é um DOUBLE DE REFERÊNCIA da porta [audit.KeyVault]: delega no
// [audit.InMemoryKeyVault] de referência (chaves EFÉMERAS por crypto/rand — SEM segredos
// fixos, sem fixtures de chave) e CONTA cada chamada. Os contadores são atómicos: o teste
// corre com -race e o capturer pode selar concorrentemente. É o que um deployment injecta
// no lugar de um key-service/software-KMS de custódia externa — a prova de que a costura
// serve exactamente essa instância.
type spyKeyVault struct {
	delegate    *audit.InMemoryKeyVault
	ensureCalls atomic.Int64
	keyCalls    atomic.Int64
	deleteCalls atomic.Int64
	lastDeleted atomic.Value // string subjectID
}

func newSpyKeyVault() *spyKeyVault {
	return &spyKeyVault{delegate: audit.NewInMemoryKeyVault(nil)}
}

func (v *spyKeyVault) EnsureKey(subjectID string) ([]byte, string, error) {
	v.ensureCalls.Add(1)
	return v.delegate.EnsureKey(subjectID)
}

func (v *spyKeyVault) Key(keyRef string) ([]byte, bool) {
	v.keyCalls.Add(1)
	return v.delegate.Key(keyRef)
}

func (v *spyKeyVault) Delete(subjectID string) {
	v.deleteCalls.Add(1)
	v.lastDeleted.Store(subjectID)
	v.delegate.Delete(subjectID)
}

var _ audit.KeyVault = (*spyKeyVault)(nil)

// newDurableNodeWithVault arranca um nó durável com o vault de KEK INJECTADO por config.
func newDurableNodeWithVault(t *testing.T, vault audit.KeyVault) *Node {
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
		t.Fatalf("Bootstrap (durable, vault injectado): %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	return node
}

// TestNode_AOS215_InjectedVaultIsTheCustodian prova que um vault INJECTADO é o que o
// caminho de cifra/shred do nó usa — e que o /dsar/erase destrói a KEK NELE, tornando o
// conteúdo irrecuperável por essa via.
func TestNode_AOS215_InjectedVaultIsTheCustodian(t *testing.T) {
	ctx := context.Background()
	spy := newSpyKeyVault()
	node := newDurableNodeWithVault(t, spy)

	const subject = "nhi:agent-injected-custody"
	const runID = "run-injected-215"

	// (a) IDENTIDADE: o vault EM VIGOR no nó É o spy injectado (não um in-memory interno).
	if node.DSARVault != audit.KeyVault(spy) {
		t.Fatal("node.DSARVault não é o vault injectado — a costura de custódia externa não liga")
	}

	// (b) A cifra passa PELO spy: capturar conteúdo provisiona a KEK via spy.EnsureKey.
	if spy.ensureCalls.Load() != 0 {
		t.Fatalf("EnsureKey não devia ter corrido antes de qualquer captura, correu %d", spy.ensureCalls.Load())
	}
	captureSynthetic(t, node, subject, runID, "AOS215-SINTETICO: caso CUSTODIA-EXTERNA", "TOOL-215-SECRET")
	if spy.ensureCalls.Load() == 0 {
		t.Fatal("a cifra do conteúdo NÃO passou pelo vault injectado (EnsureKey nunca correu) — o nó ainda usa um vault interno")
	}

	// (c) RECUPERÁVEL ANTES do erase, decifrando pelo vault injectado (prova não-vácua).
	sealed, sealedSubj := sealedContentOf(t, node, runID)
	if len(sealed) == 0 || sealedSubj != subject {
		t.Fatalf("conteúdo não selado por-titular: len=%d subject=%q", len(sealed), sealedSubj)
	}
	plainBefore, err := audit.OpenContent(spy, subject, sealed)
	if err != nil {
		t.Fatalf("OpenContent pelo vault injectado antes do erase (devia recuperar): %v", err)
	}
	if !bytes.Contains(plainBefore, []byte("CUSTODIA-EXTERNA")) {
		t.Fatal("o conteúdo decifrado pelo vault injectado devia conter o texto sintético")
	}
	if spy.keyCalls.Load() == 0 {
		t.Fatal("a leitura/decifra NÃO passou pelo vault injectado (Key nunca correu)")
	}

	// (d) /dsar/erase destrói a KEK NO vault injectado (spy.Delete corre para o titular).
	deletesBefore := spy.deleteCalls.Load()
	res, err := node.DSAR.Receive(ctx, dsar.Request{RequestID: "req-215", SubjectID: subject})
	if err != nil {
		t.Fatalf("DSAR erase: %v", err)
	}
	if res.Blocked {
		t.Fatal("erase não devia estar bloqueado (sem legal hold)")
	}
	if spy.deleteCalls.Load() <= deletesBefore {
		t.Fatal("o /dsar/erase NÃO destruiu a KEK no vault injectado (Delete nunca correu nele)")
	}
	if got, _ := spy.lastDeleted.Load().(string); got != subject {
		t.Fatalf("Delete correu para o titular errado: %q (esperado %q)", got, subject)
	}

	// (e) A KEK desapareceu DO vault injectado — e o conteúdo é irrecuperável por essa via.
	if _, ok := spy.delegate.Key(audit.KeyRefFor(subject)); ok {
		t.Fatal("a KEK ainda existe no vault injectado após o erase — a destruição não alcançou a custódia externa")
	}
	if _, err := audit.OpenContent(spy, subject, sealed); !errors.Is(err, audit.ErrDecrypt) {
		t.Fatalf("após erase, OpenContent pelo vault injectado devia falhar com ErrDecrypt, deu: %v", err)
	}
}

// failingKeyVault é um double cuja provisão de chave FALHA. Prova o invariante fail-closed:
// um vault injectado que erra propaga o erro pela cadeia de cifra — NUNCA se cai em silêncio
// para o in-memory de referência. Não gera nem devolve qualquer material de chave.
type failingKeyVault struct{ err error }

func (v failingKeyVault) EnsureKey(string) ([]byte, string, error) { return nil, "", v.err }
func (v failingKeyVault) Key(string) ([]byte, bool)                { return nil, false }
func (v failingKeyVault) Delete(string)                            {}

var _ audit.KeyVault = failingKeyVault{}

// TestNode_AOS215_InjectedVaultFailClosed prova que um vault injectado que falha propaga o
// erro pela cifra do capturer (a escrita aborta) em vez de silenciosamente selar sob um
// vault interno de referência.
func TestNode_AOS215_InjectedVaultFailClosed(t *testing.T) {
	sentinel := errors.New("custódia externa indisponível")
	node := newDurableNodeWithVault(t, failingKeyVault{err: sentinel})

	tc := agentruntime.TurnCapture{
		RunID:    "run-fc-215",
		StepID:   "step-000001",
		Turn:     1,
		Subject:  "nhi:agent-failclosed",
		Response: agentruntime.ModelResponse{Text: "nunca-selado", Final: true},
	}
	err := node.Capturer.Capture(context.Background(), tc)
	if err == nil {
		t.Fatal("a captura devia FALHAR (fail-closed) quando o vault injectado erra — houve fallback silencioso")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("o erro do vault injectado devia propagar-se, deu: %v", err)
	}
}
