package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/kernel/agent-runtime/control"
)

// TestDispatch_UnknownSubcommand: um subcomando inválido é recusado (fail-closed).
func TestDispatch_UnknownSubcommand(t *testing.T) {
	err := dispatch([]string{"bogus"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "subcomando desconhecido") {
		t.Fatalf("esperava erro de subcomando desconhecido, tive %v", err)
	}
}

// TestDispatch_Help imprime o uso.
func TestDispatch_Help(t *testing.T) {
	var b strings.Builder
	if err := dispatch([]string{"help"}, &b); err != nil {
		t.Fatalf("help: %v", err)
	}
	if !strings.Contains(b.String(), "aos <subcomando>") {
		t.Fatalf("usage nao impresso: %q", b.String())
	}
}

// TestCmd_MissingAddr: os comandos cliente exigem --addr (fail-closed).
func TestCmd_MissingAddr(t *testing.T) {
	cases := [][]string{
		{"run", "--objective", "x"},
		{"observe", "--run-id", "r"},
		{"steer", "--run-id", "r", "--emitter", "o", "--key", "k"},
	}
	for _, sub := range cases {
		if err := dispatch(sub, io.Discard); !errors.Is(err, ErrAddrRequired) {
			t.Fatalf("%v: esperava ErrAddrRequired, tive %v", sub, err)
		}
	}
}

// TestLoadOperatorKey: seed válida carrega; hex curto / em falta ⇒ ErrBadOperatorKey.
func TestLoadOperatorKey(t *testing.T) {
	dir := t.TempDir()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(dir, "op.key")
	if err := os.WriteFile(good, []byte(hex.EncodeToString(priv.Seed())), 0o600); err != nil {
		t.Fatal(err)
	}
	k, err := loadOperatorKey(good)
	if err != nil {
		t.Fatalf("loadOperatorKey: %v", err)
	}
	if !k.Equal(priv) {
		t.Fatal("chave carregada != original")
	}
	bad := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(bad, []byte("abcd"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOperatorKey(bad); !errors.Is(err, ErrBadOperatorKey) {
		t.Fatalf("hex curto: esperava ErrBadOperatorKey, tive %v", err)
	}
	if _, err := loadOperatorKey(""); !errors.Is(err, ErrBadOperatorKey) {
		t.Fatalf("chave vazia: esperava ErrBadOperatorKey, tive %v", err)
	}
}

// newCLIServer compõe um nó de referência com um operador registado e RELÓGIO REAL de steer
// (a CLI assina com time.Now — a frescura tem de o aceitar), e serve a API num httptest.Server.
// Devolve o URL base, o ficheiro da chave do operador e o ID do operador.
func newCLIServer(t *testing.T) (url, keyFile, operatorID string) {
	t.Helper()
	cfg := tnBaseConfig()
	cfg.SteerClock = nil // relógio REAL (não o tnClock fixo) — casa com o time.Now da CLI
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const opID = "human:cli-op"
	cfg.Operators = map[string]ed25519.PublicKey{opID: pub}

	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	svc, err := NewNodeService(node)
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	h, err := NewAPIHandler(svc, node)
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	kf := filepath.Join(t.TempDir(), "op.key")
	if err := os.WriteFile(kf, []byte(hex.EncodeToString(priv.Seed())), 0o600); err != nil {
		t.Fatal(err)
	}
	return srv.URL, kf, opID
}

// TestCLI_RunSteerObserve_E2E é a prova NÃO-VACUOSA da CLI: contra o handler REAL do nó,
// `run` submete um run, `steer` produz um emitter ed25519 que o nó ACEITA (prova que a
// assinatura da CLI casa com o Ed25519Authenticator, AOS-160), e `observe` relê o estado.
func TestCLI_RunSteerObserve_E2E(t *testing.T) {
	url, keyFile, opID := newCLIServer(t)
	const runID = "run-cli-1"

	var b strings.Builder
	if err := cmdRun([]string{"--addr", url, "--run-id", runID, "--objective", "faz algo", "--nhi", "nhi:x"}, &b); err != nil {
		t.Fatalf("cmdRun: %v", err)
	}
	if !strings.Contains(b.String(), runID) {
		t.Fatalf("run: output nao contem o run_id: %q", b.String())
	}

	// steer ASSINADO — se a assinatura da CLI não fosse válida (ou o tuplo divergisse do de
	// AOS-160), o nó devolveria 403 e cmdControl falharia. O sucesso PROVA a compatibilidade.
	b.Reset()
	if err := cmdControl([]string{"--addr", url, "--run-id", runID, "--emitter", opID, "--key", keyFile, "--correction", "corrige o rumo"}, control.SignalSteer, &b); err != nil {
		t.Fatalf("cmdControl steer (assinatura da CLI recusada pelo no?): %v", err)
	}
	if !strings.Contains(b.String(), "steer enviado") {
		t.Fatalf("steer: output inesperado: %q", b.String())
	}

	b.Reset()
	if err := cmdObserve([]string{"--addr", url, "--run-id", runID}, &b); err != nil {
		t.Fatalf("cmdObserve: %v", err)
	}
	if !strings.Contains(b.String(), runID) {
		t.Fatalf("observe: output nao contem o run_id: %q", b.String())
	}
}

// TestCLI_SteerWrongKeyRejected: uma chave de operador que NÃO corresponde à pubkey registada
// produz uma assinatura que o nó RECUSA (403) — a CLI não contorna a autenticação.
func TestCLI_SteerWrongKeyRejected(t *testing.T) {
	url, _, opID := newCLIServer(t)
	// Chave ERRADA (não a registada no nó).
	_, wrong, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	kf := filepath.Join(t.TempDir(), "wrong.key")
	if err := os.WriteFile(kf, []byte(hex.EncodeToString(wrong.Seed())), 0o600); err != nil {
		t.Fatal(err)
	}
	err = cmdControl([]string{"--addr", url, "--run-id", "run-x", "--emitter", opID, "--key", kf, "--correction", "x"}, control.SignalSteer, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("chave errada devia ser recusada (403) pelo no, tive %v", err)
	}
}
