package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	identity "github.com/aos-ref/platform/identity"
)

// cerimoniaMandato corre a cerimónia inteira como o operador a correria: o humano assina com a
// chave dele, o emissor cunha com a sua. Devolve o directório, a pubkey do humano e do emissor.
func cerimoniaMandato(t *testing.T) (dir string, humanoPub, emissorPub ed25519.PublicKey) {
	t.Helper()
	dir = t.TempDir()
	humano := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32))
	if err := os.WriteFile(filepath.Join(dir, "humano.key"), []byte(hex.EncodeToString(humano.Seed())), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	err := run([]string{"mandate-sign", "--key-file", filepath.Join(dir, "humano.key"),
		"--human", "alice", "--board", "board-eu", "--agent", "agent:aos-orq", "--class", "planner",
		"--caps", "run:submit,model:invoke", "--out", filepath.Join(dir, "mandato.json")}, &out, &diag)
	if err != nil {
		t.Fatalf("mandate-sign: %v", err)
	}
	if !strings.Contains(diag.String(), "mandate:") {
		t.Fatalf("o humano tem de sair com a chave de revogacao do mandato; diag=%q", diag.String())
	}
	emissor, err := loadOrCreateKey(filepath.Join(dir, "emissor.key"))
	if err != nil {
		t.Fatal(err)
	}
	return dir, humano.Public().(ed25519.PublicKey), emissor.Public().(ed25519.PublicKey)
}

func TestAOS427CerimoniaDoMandatoPontaAPonta(t *testing.T) {
	dir, humanoPub, emissorPub := cerimoniaMandato(t)
	destino := filepath.Join(dir, "nhi-run.jwt")
	var out bytes.Buffer
	err := run([]string{"mint-mandated", "--mandate", filepath.Join(dir, "mandato.json"),
		"--signer-pubkey", hex.EncodeToString(humanoPub), "--key-file", filepath.Join(dir, "emissor.key"),
		"--out", destino, "--out-mode", "0640"}, &out, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("mint-mandated: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("com --out o token nao pode ir tambem para stdout (acabaria num log): %q", out.String())
	}
	raw, err := os.ReadFile(destino)
	if err != nil {
		t.Fatal(err)
	}
	if restos, _ := filepath.Glob(filepath.Join(dir, ".nhi-*")); len(restos) != 0 {
		t.Fatalf("a escrita atomica deixou temporarios: %v", restos)
	}
	v := identity.NewVerifier(identity.WithMandatedIssuer("iss:aos-issuer-auto", emissorPub,
		map[string]ed25519.PublicKey{"alice": humanoPub}))
	p, err := v.Verify(context.Background(), strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("o no tem de aceitar o token cunhado sob o mandato: %v", err)
	}
	if p.UserID != "alice" || p.AgentID != "agent:aos-orq" || p.Board != "board-eu" || p.MandateID == "" {
		t.Fatalf("principal inesperado: %+v", p)
	}
	if got := p.Expiry.Sub(p.IssuedAt); got.Minutes() != 45 {
		t.Fatalf("sem --ttl o token leva o maximo do mandato (45m), veio %v", got)
	}
}

// Uma pubkey que não é a do humano recusa ANTES de pedir uma assinatura ao emissor.
func TestAOS427MintMandatedRecusaChaveDoHumanoErrada(t *testing.T) {
	dir, _, _ := cerimoniaMandato(t)
	outra := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32)).Public().(ed25519.PublicKey)
	err := run([]string{"mint-mandated", "--mandate", filepath.Join(dir, "mandato.json"),
		"--signer-pubkey", hex.EncodeToString(outra), "--key-file", filepath.Join(dir, "emissor.key")},
		&bytes.Buffer{}, &bytes.Buffer{})
	if !errors.Is(err, identity.ErrMandateInvalid) {
		t.Fatalf("pubkey do humano errada tinha de dar ErrMandateInvalid, veio %v", err)
	}
}

// Pedir mais do que o mandato dá é recusado no emissor, com a causa.
func TestAOS427MintMandatedRecusaEscopoForaDoMandato(t *testing.T) {
	dir, humanoPub, _ := cerimoniaMandato(t)
	err := run([]string{"mint-mandated", "--mandate", filepath.Join(dir, "mandato.json"),
		"--signer-pubkey", hex.EncodeToString(humanoPub), "--key-file", filepath.Join(dir, "emissor.key"),
		"--caps", "run:submit,cap:admin"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("pedir cap:admin fora do mandato tinha de falhar")
	}
}

// A chave humana nunca se cria em silêncio.
func TestAOS427MandateSignNaoCriaAChaveDoHumano(t *testing.T) {
	dir := t.TempDir()
	err := run([]string{"mandate-sign", "--key-file", filepath.Join(dir, "nao-existe.key"),
		"--human", "alice", "--board", "b", "--agent", "a", "--class", "c", "--caps", "x"},
		&bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("mandate-sign com chave inexistente tinha de falhar")
	}
	if _, serr := os.Stat(filepath.Join(dir, "nao-existe.key")); !os.IsNotExist(serr) {
		t.Fatal("mandate-sign criou uma chave humana em silencio")
	}
}
