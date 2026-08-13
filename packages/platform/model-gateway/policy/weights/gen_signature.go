//go:build ignore

// Command gen_signature é o assinador OFFLINE da tabela de pesos do scoring
// (AOS-269, ADR-021 regra 3, ADR-006/ADR-012). Reproduz weights_table.{sig,pub} a
// partir de uma chave PRIVADA custodiada FORA do repositório — NÃO a deriva de
// nenhuma seed/etiqueta pública (essa derivação tornaria a assinatura forjável por
// quem lesse o repo). O trust anchor é a pública cujo fingerprint está pinado em
// weights.go; rodar a chave exige actualizar esse fingerprint (code-review) e
// re-executar isto com a nova privada.
//
// É o GÉMEO EXACTO de policy/allowlist/gen_signature.go — o mecanismo de confiança
// da allowlist regional não foi reinventado, foi espelhado.
//
// Uso normal (assinar com a chave custodiada, seed ed25519 de 32 bytes em base64):
//
//	AOS269_WEIGHTS_SIGNING_SEED="<base64-32-bytes>" go run gen_signature.go
//
// Rotação (gerar uma chave NOVA com entropia real; guarda a SEED impressa no cofre
// offline e NUNCA a comitas; actualiza trustAnchorFingerprint com o fingerprint
// impresso):
//
//	AOS269_GENERATE_KEY=1 go run gen_signature.go
//
// Ciclo ADR-012: qualquer alteração de pesos exige (1) bump do campo "semver" na
// tabela, (2) passagem no eval-gate, (3) re-assinatura aqui. Sem os três, o
// carregamento fail-closed recusa e o router deixa de rotear com scoring armado.
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

	"github.com/aos-ref/platform/model-gateway/policy/weights"
)

func main() {
	priv := loadOrGenerateKey()
	pub := priv.Public().(ed25519.PublicKey)

	tableJSON, err := os.ReadFile("weights_table.json")
	if err != nil {
		fatal(err)
	}
	digest, err := weights.Digest(tableJSON)
	if err != nil {
		fatal(err)
	}
	sig := ed25519.Sign(priv, []byte(digest))

	if err := os.WriteFile("weights_table.sig", []byte(base64.StdEncoding.EncodeToString(sig)), 0o644); err != nil {
		fatal(err)
	}
	if err := os.WriteFile("weights_table.pub", []byte(base64.StdEncoding.EncodeToString(pub)), 0o644); err != nil {
		fatal(err)
	}
	fp := sha256.Sum256(pub)
	fmt.Println("weights_table.sig e weights_table.pub regenerados; digest:", digest)
	fmt.Println("trustAnchorFingerprint (pina em weights.go):", hex.EncodeToString(fp[:]))
}

// loadOrGenerateKey obtém a chave privada de assinatura. Em modo de rotação
// (AOS269_GENERATE_KEY=1) gera uma chave NOVA com crypto/rand e imprime a seed em
// STDERR para custódia offline. Caso contrário exige a seed via
// AOS269_WEIGHTS_SIGNING_SEED — NUNCA há chave derivada de material público no repo.
func loadOrGenerateKey() ed25519.PrivateKey {
	if os.Getenv("AOS269_GENERATE_KEY") == "1" {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			fatal(err)
		}
		fmt.Fprintln(os.Stderr, "CHAVE NOVA GERADA — guarda esta seed no cofre offline (NUNCA a comitas):")
		fmt.Fprintln(os.Stderr, "AOS269_WEIGHTS_SIGNING_SEED="+base64.StdEncoding.EncodeToString(priv.Seed()))
		return priv
	}
	seedB64 := os.Getenv("AOS269_WEIGHTS_SIGNING_SEED")
	if seedB64 == "" {
		fatal(fmt.Errorf("falta AOS269_WEIGHTS_SIGNING_SEED (seed ed25519 base64 custodiada offline); ou usa AOS269_GENERATE_KEY=1 para rodar a chave"))
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
