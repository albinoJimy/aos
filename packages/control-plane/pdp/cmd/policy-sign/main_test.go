package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/pdp"
)

// TestRun cobre o caminho de flags/entrada da ferramenta (run) num directório
// temporário com a política de referência.
func TestRun(t *testing.T) {
	dir := t.TempDir()
	src, err := os.ReadFile(filepath.Join("..", "..", "policies", "aos_authz.cedar"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aos_authz.cedar"), src, 0o644); err != nil {
		t.Fatal(err)
	}

	oldArgs, oldFS := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
	flag.CommandLine = flag.NewFlagSet("policy-sign", flag.ContinueOnError)
	// -key explícito no tempdir: mantém o teste hermético (o default é agora
	// ~/.aos/keys/signing.key, fora do repo, que não devemos poluir nos testes).
	os.Args = []string{"policy-sign", "-dir", dir, "-version", "3.0.0", "-key", filepath.Join(dir, "signing.key")}

	if err := run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	p, err := pdp.Open(dir)
	if err != nil {
		t.Fatalf("Open apos run: %v", err)
	}
	if p.Version() != "3.0.0" {
		t.Errorf("Version()=%q, esperava 3.0.0", p.Version())
	}
}

// TestRun_DefaultKeyPathForaDoRepo assevera que, sem -key, a chave privada é
// materializada FORA do repo em ~/.aos/keys/signing.key (finding secrets): o home
// é redireccionado para um tempdir para o teste ser hermético e não poluir o home
// real. Cobre defaultKeyPath e a criação do directório-pai da chave.
func TestRun_DefaultKeyPathForaDoRepo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home) // Windows
	t.Setenv("HOME", home)        // POSIX

	dir := t.TempDir()
	src, err := os.ReadFile(filepath.Join("..", "..", "policies", "aos_authz.cedar"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aos_authz.cedar"), src, 0o644); err != nil {
		t.Fatal(err)
	}

	oldArgs, oldFS := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
	flag.CommandLine = flag.NewFlagSet("policy-sign", flag.ContinueOnError)
	os.Args = []string{"policy-sign", "-dir", dir, "-version", "1.0.0"} // sem -key ⇒ default

	if err := run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	keyPath := filepath.Join(home, ".aos", "keys", "signing.key")
	if _, err := os.Stat(keyPath); err != nil {
		t.Errorf("chave privada devia estar fora do repo em %s: %v", keyPath, err)
	}
	// A chave NÃO deve ser materializada dentro do dir do bundle (repo).
	if _, err := os.Stat(filepath.Join(dir, "signing.key")); !os.IsNotExist(err) {
		t.Errorf("chave privada NAO devia estar na arvore do bundle: err=%v", err)
	}
}

// TestSign_GeraAssinaEVerifica exercita o fluxo da ferramenta: gera par de
// chaves, assina o bundle e verifica-o (Open). Confirma que a chave privada e o
// trust anchor são escritos e que o bundle assinado carrega.
func TestSign_GeraAssinaEVerifica(t *testing.T) {
	dir := t.TempDir()
	src, err := os.ReadFile(filepath.Join("..", "..", "policies", "aos_authz.cedar"))
	if err != nil {
		t.Fatalf("ler politica: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aos_authz.cedar"), src, 0o644); err != nil {
		t.Fatal(err)
	}

	keyPath := filepath.Join(dir, "signing.key")
	m, ver, err := sign(dir, "1.2.3", keyPath)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if m.PolicyVersion != "1.2.3" || ver != "1.2.3" {
		t.Errorf("versao inesperada: manifest=%q open=%q", m.PolicyVersion, ver)
	}
	if m.ContentHash == "" {
		t.Error("content_hash vazio")
	}

	for _, f := range []string{"manifest.json", "aos_authz.sig", "trust_anchor.pub", "signing.key"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("esperava %s escrito: %v", f, err)
		}
	}

	// Segunda assinatura reutiliza a chave existente (não regenera) e sobe versão.
	if _, _, err := sign(dir, "1.2.4", keyPath); err != nil {
		t.Fatalf("re-sign com chave existente: %v", err)
	}

	// Bundle final verifica.
	p, err := pdp.Open(dir)
	if err != nil {
		t.Fatalf("Open final: %v", err)
	}
	if p.Version() != "1.2.4" {
		t.Errorf("Version()=%q, esperava 1.2.4", p.Version())
	}
}

// TestLoadOrGenerateKey_Branches cobre carregar chave existente, gerar nova, e
// os erros de chave malformada.
func TestLoadOrGenerateKey_Branches(t *testing.T) {
	dir := t.TempDir()

	// 1) Gera nova (ficheiro inexistente): escreve chave privada + trust anchor.
	kp := filepath.Join(dir, "signing.key")
	priv, err := loadOrGenerateKey(kp, dir)
	if err != nil {
		t.Fatalf("gerar: %v", err)
	}
	if len(priv) != ed25519.PrivateKeySize {
		t.Fatalf("chave gerada com tamanho invalido: %d", len(priv))
	}
	if _, err := os.Stat(filepath.Join(dir, "trust_anchor.pub")); err != nil {
		t.Errorf("trust anchor devia existir: %v", err)
	}

	// 2) Carrega a chave existente: devolve a mesma.
	got, err := loadOrGenerateKey(kp, dir)
	if err != nil {
		t.Fatalf("carregar: %v", err)
	}
	if base64.StdEncoding.EncodeToString(got) != base64.StdEncoding.EncodeToString(priv) {
		t.Error("carregar devia devolver a mesma chave")
	}

	// 3) base64 invalido.
	bad := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(bad, []byte("nao-e-base64!!!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrGenerateKey(bad, dir); err == nil || !strings.Contains(err.Error(), "base64") {
		t.Errorf("esperava erro de base64, obtive %v", err)
	}

	// 4) tamanho invalido (base64 valido mas poucos bytes).
	short := filepath.Join(dir, "short.key")
	if err := os.WriteFile(short, []byte(base64.StdEncoding.EncodeToString([]byte("curto"))), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrGenerateKey(short, dir); err == nil || !strings.Contains(err.Error(), "tamanho") {
		t.Errorf("esperava erro de tamanho, obtive %v", err)
	}
}
