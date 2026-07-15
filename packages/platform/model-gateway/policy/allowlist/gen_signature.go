//go:build ignore

// Command gen_signature é o assinador OFFLINE da allowlist regional (AOS-058,
// ADR-006/ADR-011). Reproduz allowlist_policy.{sig,pub} a partir de uma chave
// PRIVADA custodiada FORA do repositório — NÃO a deriva de nenhuma seed/etiqueta
// pública (essa derivação tornaria a assinatura forjável por qualquer um que lesse o
// repo: ver AOS058-Q1). O trust anchor é a pública cujo fingerprint está pinado em
// allowlist.go; rodar a chave exige actualizar esse fingerprint (code-review) e
// re-executar isto com a nova privada.
//
// Uso normal (assinar com a chave custodiada, seed ed25519 de 32 bytes em base64):
//
//	AOS058_ALLOWLIST_SIGNING_SEED="<base64-32-bytes>" go run gen_signature.go
//
// Rotação (gerar uma chave NOVA com entropia real; guarda a SEED impressa no cofre
// offline e NUNCA a comitas; actualiza trustAnchorFingerprint com o fingerprint
// impresso):
//
//	AOS058_GENERATE_KEY=1 go run gen_signature.go
//
// Este ficheiro NÃO entra no binário (build ignore).
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
)

func main() {
	priv := loadOrGenerateKey()
	pub := priv.Public().(ed25519.PublicKey)

	policyJSON, err := os.ReadFile("allowlist_policy.json")
	if err != nil {
		fatal(err)
	}
	digest, err := allowlist.Digest(policyJSON)
	if err != nil {
		fatal(err)
	}
	sig := ed25519.Sign(priv, []byte(digest))

	if err := os.WriteFile("allowlist_policy.sig", []byte(base64.StdEncoding.EncodeToString(sig)), 0o644); err != nil {
		fatal(err)
	}
	if err := os.WriteFile("allowlist_policy.pub", []byte(base64.StdEncoding.EncodeToString(pub)), 0o644); err != nil {
		fatal(err)
	}
	fp := sha256.Sum256(pub)
	fmt.Println("allowlist_policy.sig e allowlist_policy.pub regenerados; digest:", digest)
	fmt.Println("trustAnchorFingerprint (pina em allowlist.go):", hex.EncodeToString(fp[:]))
}

// loadOrGenerateKey obtém a chave privada de assinatura. Em modo de rotação
// (AOS058_GENERATE_KEY=1) gera uma chave NOVA com crypto/rand e imprime a seed para
// custódia offline. Caso contrário exige a seed via AOS058_ALLOWLIST_SIGNING_SEED —
// NUNCA há uma chave derivada de material público no repositório.
func loadOrGenerateKey() ed25519.PrivateKey {
	if os.Getenv("AOS058_GENERATE_KEY") == "1" {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			fatal(err)
		}
		_ = pub
		fmt.Fprintln(os.Stderr, "CHAVE NOVA GERADA — guarda esta seed no cofre offline (NUNCA a comitas):")
		fmt.Fprintln(os.Stderr, "AOS058_ALLOWLIST_SIGNING_SEED="+base64.StdEncoding.EncodeToString(priv.Seed()))
		return priv
	}
	seedB64 := os.Getenv("AOS058_ALLOWLIST_SIGNING_SEED")
	if seedB64 == "" {
		fatal(fmt.Errorf("falta AOS058_ALLOWLIST_SIGNING_SEED (seed ed25519 base64 custodiada offline); ou usa AOS058_GENERATE_KEY=1 para rodar a chave"))
	}
	seed, err := base64.StdEncoding.DecodeString(seedB64)
	if err != nil {
		fatal(fmt.Errorf("seed nao-base64: %w", err))
	}
	if len(seed) != ed25519.SeedSize {
		fatal(fmt.Errorf("seed ed25519 tem de ter %d bytes; tem %d", ed25519.SeedSize, len(seed)))
	}
	return ed25519.NewKeyFromSeed(seed)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "erro:", err)
	os.Exit(1)
}
