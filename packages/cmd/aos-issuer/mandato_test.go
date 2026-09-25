package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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

// AOS-437: o timer passa a lista do nó, e a chave escolhe-se pelo humano do mandato.
func TestAOS437MintMandatedComAListaDoNo(t *testing.T) {
	dir, humanoPub, _ := cerimoniaMandato(t)
	outro := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32)).Public().(ed25519.PublicKey)
	base := []string{"mint-mandated", "--mandate", filepath.Join(dir, "mandato.json"),
		"--key-file", filepath.Join(dir, "emissor.key")}
	lista := "bob=" + hex.EncodeToString(outro) + ",alice=" + hex.EncodeToString(humanoPub)
	var out bytes.Buffer
	if err := run(append(base, "--signers", lista), &out, &bytes.Buffer{}); err != nil || out.Len() == 0 {
		t.Fatalf("com a lista do no, a chave da alice tinha de ser escolhida: %v", err)
	}
	for _, c := range []struct {
		nome  string
		extra []string
	}{
		{"humano ausente da lista", []string{"--signers", "bob=" + hex.EncodeToString(outro)}},
		{"humano duas vezes", []string{"--signers", "alice=" + hex.EncodeToString(humanoPub) + ",alice=" + hex.EncodeToString(outro)}},
		{"as duas flags", []string{"--signers", lista, "--signer-pubkey", hex.EncodeToString(humanoPub)}},
		{"nenhuma flag", nil},
	} {
		if err := run(append(append([]string(nil), base...), c.extra...), &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Errorf("%s: tinha de recusar", c.nome)
		}
	}
}

// AOS-437: o drenar-planos.sh e o alerta-nhi.sh lêem o prazo do NHI com
// `sed 's/.*"exp":\([0-9]*\),"jti".*/\1/p'` sobre o payload decifrado — sem jq nem Go no host. O
// mandato embebido também tem `exp`, e só o de TOPO é seguido de `jti`. Se a ordem dos campos das
// Claims mudar, os scripts passam a ler o prazo do MANDATO (dias) em vez do do token (minutos), e a
// drenagem nunca recusa um NHI caducado. Este teste é o que o impede.
func TestAOS437OExpDeTopoESeguidoDoJti(t *testing.T) {
	dir, humanoPub, _ := cerimoniaMandato(t)
	var out bytes.Buffer
	if err := run([]string{"mint-mandated", "--mandate", filepath.Join(dir, "mandato.json"),
		"--signer-pubkey", hex.EncodeToString(humanoPub), "--key-file", filepath.Join(dir, "emissor.key")},
		&out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	partes := strings.Split(strings.TrimSpace(out.String()), ".")
	if len(partes) != 3 {
		t.Fatalf("NHI malformado: %q", out.String())
	}
	payload, err := base64.RawURLEncoding.DecodeString(partes[1])
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`"exp":([0-9]+),"jti"`)
	todos := re.FindAllStringSubmatch(string(payload), -1)
	if len(todos) != 1 {
		t.Fatalf("o payload tem de ter exactamente UM `\"exp\":N,\"jti\"` (o de topo); tem %d:\n%s", len(todos), payload)
	}
	var c struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		t.Fatal(err)
	}
	if todos[0][1] != strconv.FormatInt(c.Exp, 10) {
		t.Fatalf("o que os scripts leem (%s) nao e o exp de topo (%d)", todos[0][1], c.Exp)
	}
	if !strings.Contains(string(payload), `"mandate":`) {
		t.Fatal("o NHI do emissor mandatado tem de levar o mandato — sem ele este teste nao provava a ambiguidade")
	}
}
