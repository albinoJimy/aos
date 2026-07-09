// Command policy-sign (re)assina o bundle de política do PDP (AOS-004).
//
// Uso:
//
//	go run ./cmd/policy-sign -dir policies -version 1.0.0
//
// Fluxo:
//   - carrega a chave privada ed25519 de -key (por omissão FORA do repositório,
//     em ~/.aos/keys/signing.key); se não existir, GERA um novo par, escreve a
//     chave privada nesse caminho e o trust anchor público em
//     <dir>/trust_anchor.pub;
//   - valida que os .cedar compilam, calcula o content_hash canónico, escreve
//     manifest.json (policy_version + content_hash + created_at) e assina o
//     bundle, gravando aos_authz.sig.
//
// A chave privada NUNCA é committada. A verificação em runtime (PDP.Open) só
// precisa da chave pública committada em trust_anchor.pub.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aos-ref/control-plane/pdp"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "policy-sign: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	dir := flag.String("dir", "policies", "directorio do bundle de politicas")
	version := flag.String("version", "1.0.0", "policy_version SemVer a gravar no manifest")
	keyPath := flag.String("key", "", "caminho da chave privada ed25519 (por omissao ~/.aos/keys/signing.key, FORA do repo)")
	flag.Parse()

	kp := *keyPath
	if kp == "" {
		def, err := defaultKeyPath()
		if err != nil {
			return err
		}
		kp = def
	}

	m, ver, err := sign(*dir, *version, kp)
	if err != nil {
		return err
	}
	fmt.Printf("bundle assinado: policy_version=%s content_hash=%s created_at=%s\n",
		m.PolicyVersion, m.ContentHash, m.CreatedAt)
	fmt.Printf("chave privada: %s (NAO committar — fora do repo)\n", kp)
	fmt.Printf("verificacao OK: PDP carregado com policy_version=%s\n", ver)
	return nil
}

// sign carrega/gera a chave, assina o bundle em dir e verifica-o end-to-end
// (Open). Devolve o manifest e a versão carregada. Isolada de flag/stdout para
// ser testável.
func sign(dir, version, keyPath string) (pdp.Manifest, string, error) {
	priv, err := loadOrGenerateKey(keyPath, dir)
	if err != nil {
		return pdp.Manifest{}, "", err
	}
	m, err := pdp.SignBundle(dir, version, priv)
	if err != nil {
		return m, "", err
	}
	p, err := pdp.Open(dir)
	if err != nil {
		return m, "", fmt.Errorf("verificacao pos-assinatura falhou: %w", err)
	}
	return m, p.Version(), nil
}

// defaultKeyPath devolve o caminho por omissão da chave privada, FORA da árvore
// do repositório (~/.aos/keys/signing.key). Nunca materializar a chave privada
// dentro do projecto: evita `git add -f` acidental e leitura por processos que
// vasculhem a working tree. O trust anchor PÚBLICO continua a viver no bundle.
func defaultKeyPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolver home do utilizador para a chave: %w", err)
	}
	return filepath.Join(home, ".aos", "keys", "signing.key"), nil
}

// loadOrGenerateKey carrega a chave privada ed25519 (base64) de kp; se o
// ficheiro não existir, gera um novo par, escreve a chave privada (0600, criando
// o directório-pai fora do repo) e o trust anchor público em
// <dir>/trust_anchor.pub (base64).
func loadOrGenerateKey(kp, dir string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(kp)
	if err == nil {
		b, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if derr != nil {
			return nil, fmt.Errorf("chave privada nao e base64 valido: %w", derr)
		}
		if len(b) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("chave privada com tamanho invalido (%d)", len(b))
		}
		return ed25519.PrivateKey(b), nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("ler chave privada: %w", err)
	}

	// Gerar novo par.
	pub, priv, gerr := ed25519.GenerateKey(rand.Reader)
	if gerr != nil {
		return nil, fmt.Errorf("gerar par ed25519: %w", gerr)
	}
	if mderr := os.MkdirAll(filepath.Dir(kp), 0o700); mderr != nil {
		return nil, fmt.Errorf("criar directorio da chave: %w", mderr)
	}
	if werr := os.WriteFile(kp, []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0o600); werr != nil {
		return nil, fmt.Errorf("escrever chave privada: %w", werr)
	}
	anchorPath := filepath.Join(dir, "trust_anchor.pub")
	if werr := os.WriteFile(anchorPath, []byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0o644); werr != nil {
		return nil, fmt.Errorf("escrever trust anchor: %w", werr)
	}
	fmt.Printf("novo par ed25519 gerado; trust anchor: %s\n", anchorPath)
	return priv, nil
}
