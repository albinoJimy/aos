package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	dsar "github.com/aos-ref/control-plane/governance/dsar"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-093 — PROVA FALSIFICÁVEL AO NÍVEL DO NÓ (CA6/CA8): o conteúdo de um run
// persistido no Event Store DURÁVEL é cifrado por chave POR-TITULAR; após POST
// /dsar/erase o texto do conteúdo NÃO aparece no ficheiro do Event Store (WAL) E a
// decifragem falha (irrecuperável), E a hash-chain do WORM continua a validar.
//
// O teste exercita a COMPOSIÇÃO REAL do nó: node.Capturer é o [replay.EventStoreCapturer]
// cablado com o [contentSealer] que detém o MESMO node.DSARVault que o fluxo DSAR
// destrói. Conteúdo de teste é SINTÉTICO (nunca PII real).

// capSynthPII é o texto do conteúdo do run injectado (sintético) que NUNCA deve
// aparecer em claro no WAL do Event Store.
const capSynthPII = "PROMPT-SINTETICO: contactar SUJEITO-ZORG-9911 sobre caso SYNTH-42"

// newDurableNode arranca um nó com execução durável (WAL do ES + WORM em disco).
func newDurableNode(t *testing.T) (*Node, string) {
	t.Helper()
	dir := t.TempDir()
	esPath := filepath.Join(dir, "events.wal")
	cfg := tnBaseConfig()
	cfg.DurableExecution = true
	cfg.EventStorePath = esPath
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (durable): %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	return node, esPath
}

// captureSynthetic grava um turno com conteúdo sintético sob o titular subject,
// através do capturer REAL do nó (que sela por-titular). Devolve o run/stream id.
func captureSynthetic(t *testing.T, node *Node, subject, runID, text, toolOut string) {
	t.Helper()
	tc := agentruntime.TurnCapture{
		RunID:   runID,
		StepID:  "step-000001",
		Turn:    1,
		Subject: subject,
		Response: agentruntime.ModelResponse{
			Text:  text,
			Final: true,
			Usage: agentruntime.Usage{InputTokens: 3, OutputTokens: 4},
		},
		ToolResults: []agentruntime.CapturedToolResult{{
			Invocation: agentruntime.ToolInvocation{ToolID: "echo"},
			Result:     agentruntime.Untrusted([]byte(toolOut)),
		}},
	}
	if err := node.Capturer.Capture(context.Background(), tc); err != nil {
		t.Fatalf("Capture: %v", err)
	}
}

// sealedContentOf lê o stream do run no Event Store e devolve o SealedContent +
// SealedSubject do único evento replay.captured.
func sealedContentOf(t *testing.T, node *Node, runID string) (sealed []byte, subject string) {
	t.Helper()
	events, err := node.EventStore.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("Read stream %q: %v", runID, err)
	}
	for _, e := range events {
		if e.Type != "replay.captured" {
			continue
		}
		var p struct {
			SealedContent []byte `json:"sealed_content"`
			SealedSubject string `json:"sealed_subject"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("unmarshal capturePayload: %v", err)
		}
		return p.SealedContent, p.SealedSubject
	}
	t.Fatalf("nenhum evento replay.captured no stream %q", runID)
	return nil, ""
}

func TestNode_AOS093_SubstrateErase_Falsifiable(t *testing.T) {
	ctx := context.Background()
	node, esPath := newDurableNode(t)
	const subject = "nhi:agent-synth-erase"
	const runID = "run-dsar-synth"

	captureSynthetic(t, node, subject, runID, capSynthPII, "TOOL-OUT-SYNTH-SECRET")

	// (a) CONFIDENCIALIDADE: o texto sintético NÃO está no ficheiro do WAL do ES.
	walBefore, err := os.ReadFile(esPath)
	if err != nil {
		t.Fatalf("ler WAL: %v", err)
	}
	for _, needle := range []string{capSynthPII, "SUJEITO-ZORG-9911", "TOOL-OUT-SYNTH-SECRET"} {
		if bytes.Contains(walBefore, []byte(needle)) {
			t.Fatalf("WAL do ES contém conteúdo em CLARO %q — confidencialidade violada", needle)
		}
	}

	// (b) RECUPERABILIDADE ANTES do erase (não-vácuo): a decifragem devolve o conteúdo.
	sealed, sealedSubj := sealedContentOf(t, node, runID)
	if len(sealed) == 0 || sealedSubj != subject {
		t.Fatalf("evento não selado por-titular: len=%d subject=%q", len(sealed), sealedSubj)
	}
	plainBefore, err := audit.OpenContent(node.DSARVault, subject, sealed)
	if err != nil {
		t.Fatalf("OpenContent antes do erase (devia recuperar): %v", err)
	}
	if !bytes.Contains(plainBefore, []byte(capSynthPII)) {
		t.Fatal("o conteúdo decifrado ANTES do erase devia conter a PII sintética (prova não-vácua)")
	}

	// (c) POST /dsar/erase — destrói a KEK por-titular via o fluxo DSAR real.
	res, err := node.DSAR.Receive(ctx, dsar.Request{RequestID: "req-1", SubjectID: subject})
	if err != nil {
		t.Fatalf("DSAR erase: %v", err)
	}
	if res.Blocked {
		t.Fatal("erase não devia estar bloqueado (sem legal hold)")
	}

	// (d) IRRECUPERABILIDADE REAL: a decifragem do MESMO blob agora FALHA.
	if _, err := audit.OpenContent(node.DSARVault, subject, sealed); !errors.Is(err, audit.ErrDecrypt) {
		t.Fatalf("após erase, OpenContent devia falhar com ErrDecrypt, deu: %v", err)
	}

	// (e) O texto continua ausente do WAL (o log NÃO foi reescrito — só a chave morreu).
	walAfter, err := os.ReadFile(esPath)
	if err != nil {
		t.Fatalf("ler WAL após erase: %v", err)
	}
	if bytes.Contains(walAfter, []byte(capSynthPII)) {
		t.Fatal("após erase o texto sintético apareceu no WAL — impossível (nunca foi cifrado?)")
	}

	// (f) HASH-CHAIN do WORM continua a validar (a partição DSAR foi selada, não mutada).
	head, err := node.WORM.Head(ctx, "governance.dsar")
	if err != nil {
		t.Fatalf("Head WORM governance.dsar: %v", err)
	}
	if head == 0 {
		t.Fatal("partição DSAR do WORM vazia — o erase devia ter selado eventos")
	}
	if err := audit.Verify(ctx, node.WORM, "governance.dsar", 1, head); err != nil {
		t.Fatalf("hash-chain do WORM NÃO valida após o erase: %v", err)
	}

	// (g) O WAL do ES permanece íntegro e reabrível (gapless/crc) — não foi reescrito.
	reopened, err := eventstore.Reopen(esPath)
	if err != nil {
		t.Fatalf("reabrir o WAL do ES após erase (integridade): %v", err)
	}
	_ = reopened.Close()
}

// TestNode_AOS093_PerSubjectIsolation prova que o erase do titular A NÃO afecta o
// conteúdo cifrado do titular B (chave por-titular).
func TestNode_AOS093_PerSubjectIsolation(t *testing.T) {
	ctx := context.Background()
	node, _ := newDurableNode(t)
	const subjA, subjB = "nhi:agent-A", "nhi:agent-B"

	captureSynthetic(t, node, subjA, "run-A", "conteudo A: AAA-111", "outA")
	captureSynthetic(t, node, subjB, "run-B", "conteudo B: BBB-222", "outB")

	sealedB, _ := sealedContentOf(t, node, "run-B")

	// Erase SÓ de A.
	if _, err := node.DSAR.Receive(ctx, dsar.Request{RequestID: "r", SubjectID: subjA}); err != nil {
		t.Fatalf("erase A: %v", err)
	}

	sealedA, _ := sealedContentOf(t, node, "run-A")
	if _, err := audit.OpenContent(node.DSARVault, subjA, sealedA); !errors.Is(err, audit.ErrDecrypt) {
		t.Fatalf("A devia ser irrecuperável após o seu erase, deu: %v", err)
	}
	gotB, err := audit.OpenContent(node.DSARVault, subjB, sealedB)
	if err != nil {
		t.Fatalf("B devia continuar recuperável após erase de A: %v", err)
	}
	if !bytes.Contains(gotB, []byte("BBB-222")) {
		t.Fatalf("conteúdo de B corrompido: %q", gotB)
	}
}

// TestNode_AOS093_LegalHoldCoversSubstrate prova que um legal hold POR-PARTIÇÃO sobre
// o stream do run (registado no DSARIndex pelo capturer) SUSPENDE o crypto-shredding:
// o erase é bloqueado e o conteúdo do run mantém-se decifrável enquanto o hold vigorar.
func TestNode_AOS093_LegalHoldCoversSubstrate(t *testing.T) {
	ctx := context.Background()
	node, _ := newDurableNode(t)
	const subject = "nhi:agent-held"
	const runID = "run-held"

	captureSynthetic(t, node, subject, runID, "conteudo retido: HELD-333", "outHeld")

	// O capturer ligou subject→runID no DSARIndex; retém a PARTIÇÃO (o stream do run).
	node.DSARHolds.HoldPartition(runID)

	res, err := node.DSAR.Receive(ctx, dsar.Request{RequestID: "r", SubjectID: subject})
	if !errors.Is(err, dsar.ErrLegalHold) {
		t.Fatalf("erase sob hold devia dar ErrLegalHold, deu: %v (res=%+v)", err, res)
	}

	// O conteúdo NÃO foi tornado ilegível (o hold cobriu o substrato).
	sealed, _ := sealedContentOf(t, node, runID)
	got, err := audit.OpenContent(node.DSARVault, subject, sealed)
	if err != nil {
		t.Fatalf("sob hold o conteúdo devia continuar decifrável: %v", err)
	}
	if !bytes.Contains(got, []byte("HELD-333")) {
		t.Fatalf("conteúdo retido corrompido: %q", got)
	}

	// Libertado o hold, o erase procede e o conteúdo torna-se irrecuperável.
	node.DSARHolds.ReleasePartition(runID)
	if _, err := node.DSAR.Receive(ctx, dsar.Request{RequestID: "r2", SubjectID: subject}); err != nil {
		t.Fatalf("erase após libertar o hold: %v", err)
	}
	if _, err := audit.OpenContent(node.DSARVault, subject, sealed); !errors.Is(err, audit.ErrDecrypt) {
		t.Fatalf("após libertar o hold e apagar, o conteúdo devia ser irrecuperável, deu: %v", err)
	}
}
